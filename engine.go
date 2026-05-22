package browse

import (
	"fmt"
	"sync"

	"github.com/felixgeelhaar/scout/internal/cdp"
	"github.com/felixgeelhaar/scout/internal/launcher"
)

// taskEntry stores a registered task with its full handler chain.
type taskEntry struct {
	name     string
	handlers HandlersChain
}

// Engine manages the browser lifecycle, global middleware, and task registry.
type Engine struct {
	opts       options
	middleware HandlersChain
	groups     map[string]*Group
	tasks      map[string]*taskEntry
	browser    *launcher.Browser
	conn       *cdp.Conn
	mu         sync.RWMutex
}

// Launch starts the browser or connects to a remote CDP endpoint.
// If WithRemoteCDP was set, connects to the remote WebSocket URL instead of launching Chrome.
func (e *Engine) Launch() error {
	if e.conn != nil {
		return fmt.Errorf("browse: already launched")
	}

	if e.opts.remoteCDP != "" {
		conn, err := cdp.Dial(e.opts.remoteCDP)
		if err != nil {
			return fmt.Errorf("browse: failed to connect to remote CDP: %w", err)
		}
		e.conn = conn
		return nil
	}

	b, err := launcher.Launch(launcher.Options{
		Headless:    e.opts.headless,
		ProxyServer: e.opts.proxyServer,
	})
	if err != nil {
		return err
	}
	e.browser = b

	conn, err := cdp.Dial(b.WSEndpoint())
	if err != nil {
		_ = b.Close()
		return fmt.Errorf("browse: failed to connect CDP: %w", err)
	}
	e.conn = conn
	return nil
}

// MustLaunch calls Launch and panics on error.
func (e *Engine) MustLaunch() *Engine {
	if err := e.Launch(); err != nil {
		panic(err)
	}
	return e
}

// NewPage creates a new browser page/tab at about:blank.
func (e *Engine) NewPage() (*Page, error) {
	return e.NewPageAt("about:blank")
}

// ExistingPage attaches to an existing browser page (e.g. the initial about:blank tab).
// Returns nil if no existing page target is found.
func (e *Engine) ExistingPage() (*Page, error) {
	targets, err := e.conn.GetTargets()
	if err != nil {
		return nil, fmt.Errorf("browse: failed to get targets: %w", err)
	}
	for _, t := range targets {
		if t.Type == "page" {
			return newPage(e.conn, t.TargetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs})
		}
	}
	return nil, nil
}

// NewPageAt creates a new browser page/tab and navigates directly to the URL.
// Faster than NewPage + Navigate because Chrome loads the URL during target creation.
//
// Waits for the page load event before returning so callers can read URL/title
// immediately. Returning before the navigation committed produced a flake in
// integration_full_test.go:TestIntegrationEngineNewPageAndNewPageAt — under
// load, URL() would still report "about:blank" because the target swap hadn't
// finished. WaitLoad failures are non-fatal: we return the page anyway so the
// caller can decide what to do (the previous behavior).
func (e *Engine) NewPageAt(url string) (*Page, error) {
	targetID, err := e.conn.CreateTarget(url)
	if err != nil {
		return nil, fmt.Errorf("browse: failed to create page: %w", err)
	}
	page, err := newPage(e.conn, targetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs})
	if err != nil {
		return nil, err
	}
	_ = page.WaitLoad()
	return page, nil
}

// Close shuts down the browser and releases resources.
func (e *Engine) Close() error {
	if e.conn != nil {
		_ = e.conn.Close()
	}
	if e.browser != nil {
		return e.browser.Close()
	}
	return nil
}

// Use appends global middleware to the engine.
func (e *Engine) Use(middleware ...HandlerFunc) {
	e.middleware = append(e.middleware, middleware...)
}

// Group creates a named group with optional middleware.
func (e *Engine) Group(name string, middleware ...HandlerFunc) *Group {
	g := &Group{
		name:       name,
		engine:     e,
		middleware: middleware,
		tasks:      make(map[string]*taskEntry),
	}
	e.mu.Lock()
	e.groups[name] = g
	e.mu.Unlock()
	return g
}

// Task registers a named task with handlers on the engine (root group).
func (e *Engine) Task(name string, handlers ...HandlerFunc) {
	chain := make(HandlersChain, 0, len(e.middleware)+len(handlers))
	chain = append(chain, e.middleware...)
	chain = append(chain, handlers...)

	e.mu.Lock()
	e.tasks[name] = &taskEntry{name: name, handlers: chain}
	e.mu.Unlock()
}

// Run executes a single task by name.
func (e *Engine) Run(taskName string) error {
	task, err := e.findTask(taskName)
	if err != nil {
		return err
	}
	return e.executeTask(task)
}

// RunAll executes all registered tasks (root and groups).
// When WithPoolSize is set, tasks run concurrently up to the pool limit.
// Otherwise, tasks run sequentially.
func (e *Engine) RunAll() error {
	all := e.collectAllTasks()

	if e.opts.poolSize <= 0 {
		for _, t := range all {
			if err := e.executeTask(t); err != nil {
				return err
			}
		}
		return nil
	}

	return e.runConcurrent(all)
}

func (e *Engine) runConcurrent(tasks []*taskEntry) error {
	sem := make(chan struct{}, e.opts.poolSize)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, t := range tasks {
		wg.Add(1)
		go func(task *taskEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := e.executeTask(task); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(t)
	}

	wg.Wait()

	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return fmt.Errorf("browse: %d tasks failed; first: %w", len(errs), errs[0])
}

// RunGroup executes all tasks within a named group.
func (e *Engine) RunGroup(groupName string) error {
	e.mu.RLock()
	g, ok := e.groups[groupName]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("browse: group %q not found", groupName)
	}
	return e.runGroup(g)
}

func (e *Engine) runGroup(g *Group) error {
	g.mu.RLock()
	tasks := make([]*taskEntry, 0, len(g.tasks))
	for _, t := range g.tasks {
		tasks = append(tasks, t)
	}
	g.mu.RUnlock()

	for _, t := range tasks {
		if err := e.executeTask(t); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) findTask(name string) (*taskEntry, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if t, ok := e.tasks[name]; ok {
		return t, nil
	}
	for _, g := range e.groups {
		g.mu.RLock()
		t, ok := g.tasks[name]
		g.mu.RUnlock()
		if ok {
			return t, nil
		}
	}
	return nil, fmt.Errorf("browse: task %q not found", name)
}

// collectAllTasks gathers all root tasks and group tasks into a single slice.
func (e *Engine) collectAllTasks() []*taskEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()

	all := make([]*taskEntry, 0, len(e.tasks))
	for _, t := range e.tasks {
		all = append(all, t)
	}
	for _, g := range e.groups {
		g.mu.RLock()
		for _, t := range g.tasks {
			all = append(all, t)
		}
		g.mu.RUnlock()
	}
	return all
}

func (e *Engine) executeTask(task *taskEntry) error {
	// Create a new page/tab
	targetID, err := e.conn.CreateTarget("about:blank")
	if err != nil {
		return fmt.Errorf("browse: failed to create page: %w", err)
	}

	page, err := newPage(e.conn, targetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs})
	if err != nil {
		return err
	}
	defer func() { _ = page.Close() }()

	if e.opts.width > 0 && e.opts.height > 0 {
		if err := page.SetViewport(e.opts.width, e.opts.height); err != nil {
			return fmt.Errorf("browse: failed to set viewport: %w", err)
		}
	}

	if e.opts.userAgent != "" {
		_ = page.SetUserAgent(e.opts.userAgent)
	}

	tracker, _ := NewTaskTracker(task.name)
	if tracker != nil {
		defer tracker.Stop()
		tracker.Start()
	}

	ctx := newContext(page, task.name, task.handlers)
	ctx.Next()
	ctx.cancel()

	if len(ctx.errors) > 0 {
		if tracker != nil {
			tracker.Fail(ctx.errors[0])
		}
		return ctx.errors[0]
	}
	if tracker != nil {
		tracker.Success()
	}
	return nil
}
