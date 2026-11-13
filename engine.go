package browse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

// taskEntry stores a registered task with its full handler chain.
type taskEntry struct {
	name     string
	handlers HandlersChain
}

// Engine manages the browser lifecycle, global middleware, and task registry.
type Engine struct {
	opts                     options
	middleware               HandlersChain
	groups                   map[string]*Group
	tasks                    map[string]*taskEntry
	browser                  *launcher.Browser
	conn                     *cdp.Conn
	remoteBrowserContextID   string
	ownsRemoteBrowserContext bool
	browserInstanceID        string
	mu                       sync.RWMutex
}

// Launch starts the browser or connects to a remote CDP endpoint.
// If WithRemoteCDP was set, connects to a remote WebSocket URL or discovers
// one from an HTTP(S) DevTools endpoint instead of launching Chrome.
func (e *Engine) Launch() error {
	return e.LaunchContext(context.Background())
}

// LaunchContext starts the browser or connects to remote CDP within ctx.
// The context only bounds startup; it does not own a successfully launched browser.
func (e *Engine) LaunchContext(ctx context.Context) error {
	if e.conn != nil {
		return fmt.Errorf("browse: already launched")
	}
	if e.opts.remoteBrowserContext && e.opts.remoteCDP == "" {
		return fmt.Errorf("browse: remote browser contexts require WithRemoteCDP")
	}

	if e.opts.remoteCDP != "" {
		if e.opts.proxyServer != "" {
			return fmt.Errorf("browse: WithProxy cannot configure an already-running remote CDP browser; configure its network proxy on the browser host")
		}
		wsURL, err := resolveRemoteCDP(ctx, e.opts.remoteCDP)
		if err != nil {
			return err
		}
		conn, err := cdp.DialContext(ctx, wsURL)
		if err != nil {
			return fmt.Errorf("browse: failed to connect to remote CDP: %w", err)
		}
		e.conn = conn
		if e.opts.remoteBrowserContext {
			contextID, err := conn.CreateBrowserContext(ctx)
			if err != nil {
				_ = conn.Close()
				e.conn = nil
				return fmt.Errorf("browse: create isolated remote browser context: %w", err)
			}
			e.remoteBrowserContextID = contextID
			e.ownsRemoteBrowserContext = true
		} else if e.opts.remoteBrowserContextID != "" {
			if err := conn.AttachBrowserContext(ctx, e.opts.remoteBrowserContextID); err != nil {
				_ = conn.Close()
				e.conn = nil
				return fmt.Errorf("browse: attach to remote browser context: %w", err)
			}
			e.remoteBrowserContextID = e.opts.remoteBrowserContextID
			e.ownsRemoteBrowserContext = false
		}
		e.browserInstanceID = fingerprintBrowserEndpoint(wsURL + "\x00" + e.remoteBrowserContextID)
		return nil
	}

	b, err := launcher.LaunchContext(ctx, launcher.Options{
		Headless:    e.opts.headless,
		ProxyServer: e.opts.proxyServer,
		ChromePath:  e.opts.chromePath,
		ChromeArgs:  e.opts.chromeArgs,
	})
	if err != nil {
		return err
	}
	e.browser = b

	conn, err := cdp.DialContext(ctx, b.WSEndpoint())
	if err != nil {
		_ = b.Close()
		return fmt.Errorf("browse: failed to connect CDP: %w", err)
	}
	e.conn = conn
	e.browserInstanceID = fingerprintBrowserEndpoint(b.WSEndpoint() + "\x00" + e.remoteBrowserContextID)
	return nil
}

// BrowserInstanceID returns a non-reversible fingerprint of the active
// browser's DevTools WebSocket endpoint and BrowserContext. It is stable across
// Scout processes attached to the same running browser/context and changes
// when Chrome restarts or a different BrowserContext is selected.
func (e *Engine) BrowserInstanceID() string {
	if e == nil {
		return ""
	}
	return e.browserInstanceID
}

// RemoteBrowserContextID returns the current remote BrowserContext identifier.
// It is an opaque CDP identifier, not an authorization token; callers must
// protect it and may only reuse it with the same trusted browser endpoint.
func (e *Engine) RemoteBrowserContextID() string {
	if e == nil {
		return ""
	}
	return e.remoteBrowserContextID
}

// DisposeRemoteBrowserContext disposes a specified BrowserContext through this
// connected remote CDP browser. The ID must come from trusted host state.
func (e *Engine) DisposeRemoteBrowserContext(ctx context.Context, contextID string) error {
	if e == nil || e.conn == nil || e.opts.remoteCDP == "" {
		return fmt.Errorf("browse: remote browser is not connected")
	}
	if contextID == "" {
		return fmt.Errorf("browse: browser context ID is required")
	}
	return e.conn.DisposeBrowserContext(ctx, contextID)
}

// RemoteBrowserContextExists reports whether a remote BrowserContext is still
// present. It is useful for making cleanup idempotent after a lost CDP reply.
func (e *Engine) RemoteBrowserContextExists(ctx context.Context, contextID string) (bool, error) {
	if e == nil || e.conn == nil || e.opts.remoteCDP == "" {
		return false, fmt.Errorf("browse: remote browser is not connected")
	}
	if contextID == "" {
		return false, fmt.Errorf("browse: browser context ID is required")
	}
	return e.conn.BrowserContextExists(ctx, contextID)
}

// PreserveRemoteBrowserContext transfers context cleanup responsibility to
// the durable recovery owner, so closing this Engine only disconnects.
func (e *Engine) PreserveRemoteBrowserContext(contextID string) error {
	if e == nil || e.conn == nil || e.opts.remoteCDP == "" || contextID == "" || e.remoteBrowserContextID != contextID {
		return fmt.Errorf("browse: remote browser context identity mismatch")
	}
	e.ownsRemoteBrowserContext = false
	return nil
}

func fingerprintBrowserEndpoint(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

// resolveRemoteCDP accepts a browser WebSocket URL directly or an HTTP(S)
// DevTools endpoint. Resolving the latter on each launch lets Reset reconnect
// after Chrome restarts with a new browser WebSocket path.
func resolveRemoteCDP(ctx context.Context, endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("browse: invalid remote CDP endpoint %q", endpoint)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
		return endpoint, nil
	case "http", "https":
	default:
		return "", fmt.Errorf("browse: remote CDP endpoint must use ws, wss, http, or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("browse: remote DevTools HTTP endpoint cannot contain credentials, query, or fragment")
	}
	if !strings.HasSuffix(u.Path, "/json/version") {
		u.Path = strings.TrimRight(u.Path, "/") + "/json/version"
	}
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("browse: create remote DevTools discovery request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("browse: discover remote CDP WebSocket: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("browse: remote DevTools discovery returned HTTP %d", resp.StatusCode)
	}
	var metadata struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&metadata); err != nil {
		return "", fmt.Errorf("browse: decode remote DevTools discovery response: %w", err)
	}
	wsURL, err := url.Parse(metadata.WebSocketDebuggerURL)
	if err != nil || wsURL.Host == "" || (wsURL.Scheme != "ws" && wsURL.Scheme != "wss") {
		return "", fmt.Errorf("browse: remote DevTools response has an invalid browser WebSocket URL")
	}
	return metadata.WebSocketDebuggerURL, nil
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
	return e.NewPageContext(context.Background())
}

// NewPageContext creates and attaches a browser page using the caller's
// deadline for target creation, session attachment, and required domain setup.
func (e *Engine) NewPageContext(ctx context.Context) (*Page, error) {
	targetID, err := e.conn.CreateTargetInContextCtx(ctx, "about:blank", e.remoteBrowserContextID)
	if err != nil {
		return nil, fmt.Errorf("browse: failed to create page: %w", err)
	}
	page, err := newPageContext(ctx, e.conn, targetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs, PolicyID: e.opts.policyID, AllowedOrigins: e.opts.allowedOrigins})
	if err != nil {
		return nil, errors.Join(err, e.closeCreatedTarget(targetID))
	}
	return page, nil
}

// ExistingPage attaches to an existing browser page (e.g. the initial about:blank tab).
// Returns nil if no existing page target is found.
func (e *Engine) ExistingPage() (*Page, error) {
	return e.ExistingPageContext(context.Background())
}

// ExistingPageContext attaches to an existing page using the caller's deadline.
func (e *Engine) ExistingPageContext(ctx context.Context) (*Page, error) {
	targets, err := e.conn.GetTargetsCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("browse: failed to get targets: %w", err)
	}
	for _, t := range targets {
		if t.Type == "page" && (e.remoteBrowserContextID == "" || t.BrowserContextID == e.remoteBrowserContextID) {
			return newPageContext(ctx, e.conn, t.TargetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs, PolicyID: e.opts.policyID, AllowedOrigins: e.opts.allowedOrigins})
		}
	}
	return nil, nil
}

// PageContext attaches to a specific existing page target. It never creates a
// target, which makes it suitable for reconnecting to a caller-owned page.
func (e *Engine) PageContext(ctx context.Context, targetID string) (*Page, error) {
	if e.conn == nil {
		return nil, fmt.Errorf("browse: browser is not connected")
	}
	if targetID == "" {
		return nil, fmt.Errorf("browse: target ID is required")
	}
	targets, err := e.conn.GetTargetsCtx(ctx)
	if err != nil {
		return nil, fmt.Errorf("browse: failed to get targets: %w", err)
	}
	for _, target := range targets {
		if target.TargetID == targetID && target.Type == "page" && (e.remoteBrowserContextID == "" || target.BrowserContextID == e.remoteBrowserContextID) {
			return newPageContext(ctx, e.conn, targetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs, PolicyID: e.opts.policyID, AllowedOrigins: e.opts.allowedOrigins})
		}
	}
	return nil, fmt.Errorf("browse: page target %q does not exist", targetID)
}

// PageTarget describes a page target visible to this browser connection.
type PageTarget struct {
	TargetID string
	URL      string
	Title    string
}

// BrowserTarget describes a target visible to this browser connection.
// ParentFrameID is populated for frame and worker targets when Chromium
// exposes their parent relationship.
type BrowserTarget struct {
	TargetID         string
	Type             string
	URL              string
	ParentFrameID    string
	ParentID         string
	BrowserContextID string
}

// BrowserTargetsContext lists targets visible to this engine. For an isolated
// remote BrowserContext, targets from other contexts are excluded.
func (e *Engine) BrowserTargetsContext(ctx context.Context) ([]BrowserTarget, error) {
	if e.conn == nil {
		return nil, fmt.Errorf("browse: browser is not connected")
	}
	targets, err := e.conn.GetTargetsCtx(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]BrowserTarget, 0, len(targets))
	for _, target := range targets {
		if e.remoteBrowserContextID != "" && target.BrowserContextID != e.remoteBrowserContextID {
			continue
		}
		result = append(result, BrowserTarget{
			TargetID: target.TargetID, Type: target.Type, URL: target.URL,
			ParentFrameID: target.ParentFrameID, ParentID: target.ParentID,
			BrowserContextID: target.BrowserContextID,
		})
	}
	return result, nil
}

// PageTargetsContext lists page targets in the browser context owned by this
// engine. The result is a point-in-time snapshot and does not reserve targets.
func (e *Engine) PageTargetsContext(ctx context.Context) ([]PageTarget, error) {
	if e.conn == nil {
		return nil, fmt.Errorf("browse: browser is not connected")
	}
	targets, err := e.conn.GetTargetsCtx(ctx)
	if err != nil {
		return nil, err
	}
	pages := make([]PageTarget, 0, len(targets))
	for _, target := range targets {
		if target.Type != "page" || (e.remoteBrowserContextID != "" && target.BrowserContextID != e.remoteBrowserContextID) {
			continue
		}
		pages = append(pages, PageTarget{TargetID: target.TargetID, URL: target.URL, Title: target.Title})
	}
	return pages, nil
}

// HasTargetContext reports whether a CDP target is currently present in the
// connected browser. The result is a point-in-time snapshot and does not lock
// the target against later closure.
func (e *Engine) HasTargetContext(ctx context.Context, targetID string) (bool, error) {
	if e.conn == nil {
		return false, fmt.Errorf("browse: browser is not connected")
	}
	targets, err := e.conn.GetTargetsCtx(ctx)
	if err != nil {
		return false, err
	}
	for _, target := range targets {
		if target.TargetID == targetID && target.Type == "page" && (e.remoteBrowserContextID == "" || target.BrowserContextID == e.remoteBrowserContextID) {
			return true, nil
		}
	}
	return false, nil
}

// ActivateTargetContext asks the browser to activate a page target without
// first attaching a renderer session. This can wake a discarded page whose
// CDP target has already been replaced by Chromium.
func (e *Engine) ActivateTargetContext(ctx context.Context, targetID string) error {
	if e.conn == nil {
		return fmt.Errorf("browse: browser is not connected")
	}
	if targetID == "" {
		return fmt.Errorf("browse: target ID is required")
	}
	return e.conn.ActivateTargetContext(ctx, targetID)
}

// NewPageAt creates a new browser page/tab and navigates to the URL after
// attaching its request policy. Attaching first ensures redirects on the
// initial navigation are subject to the same URL checks as later navigations.
//
// Waits for the page load event before returning so callers can read URL/title
// immediately. Returning before the navigation committed produced a flake in
// integration_full_test.go:TestIntegrationEngineNewPageAndNewPageAt — under
// load, URL() would still report "about:blank" because the target swap hadn't
// finished. A navigation or load failure closes the new target and is returned.
func (e *Engine) NewPageAt(url string) (*Page, error) {
	targetID, err := e.conn.CreateTargetInContextCtx(context.Background(), "about:blank", e.remoteBrowserContextID)
	if err != nil {
		return nil, fmt.Errorf("browse: failed to create page: %w", err)
	}
	page, err := newPage(e.conn, targetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs, PolicyID: e.opts.policyID, AllowedOrigins: e.opts.allowedOrigins})
	if err != nil {
		return nil, errors.Join(err, e.closeCreatedTarget(targetID))
	}
	if err := page.Navigate(url); err != nil {
		return nil, errors.Join(err, e.closeCreatedTarget(targetID))
	}
	return page, nil
}

func (e *Engine) closeCreatedTarget(targetID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.conn.CloseTargetCtx(ctx, targetID); err != nil {
		return fmt.Errorf("browse: close page target %q after setup failure: %w", targetID, err)
	}
	return nil
}

// Close shuts down the browser and releases resources.
func (e *Engine) Close() error {
	return e.CloseContext(context.Background())
}

// CloseContext shuts down the CDP connection and releases browser resources.
// For a locally launched browser it requests Browser.close, closes the CDP
// socket, then waits for the Chrome process until ctx expires before force
// killing the process. For remote CDP it disposes any Scout-owned isolated
// context and disconnects Scout without stopping the external browser.
func (e *Engine) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var closeErr error
	conn := e.conn
	browser := e.browser
	contextID := e.remoteBrowserContextID
	ownsContext := e.ownsRemoteBrowserContext
	e.conn = nil
	e.browser = nil
	e.remoteBrowserContextID = ""
	e.ownsRemoteBrowserContext = false
	if browser != nil {
		if conn != nil && ctx.Err() == nil {
			// Ask Chromium to terminate its renderer and utility helpers cleanly
			// before falling back to killing the browser process.
			_, _ = conn.CallSessionCtx(ctx, "", "Browser.close", nil)
		}
		if conn != nil {
			closeErr = conn.Close()
		}
		browserErr := browser.CloseContext(ctx)
		return errors.Join(closeErr, browserErr)
	}
	if conn != nil {
		if contextID != "" && ownsContext {
			// Context disposal is owned cleanup. Give it a small independent
			// window even when the caller's operation deadline has expired; the
			// connection close below remains the final disposeOnDetach fallback.
			cleanupCtx := ctx
			cancel := func() {}
			if ctx.Err() != nil {
				cleanupCtx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			}
			closeErr = errors.Join(closeErr, conn.DisposeBrowserContext(cleanupCtx, contextID))
			cancel()
		}
		closeErr = errors.Join(closeErr, conn.Close())
	}
	return closeErr
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

func (e *Engine) executeTask(task *taskEntry) (retErr error) {
	// Create a new page/tab
	targetID, err := e.conn.CreateTargetInContextCtx(context.Background(), "about:blank", e.remoteBrowserContextID)
	if err != nil {
		return fmt.Errorf("browse: failed to create page: %w", err)
	}

	page, err := newPage(e.conn, targetID, e.opts.timeout, URLValidator{AllowPrivateIPs: e.opts.allowPrivateIPs, PolicyID: e.opts.policyID, AllowedOrigins: e.opts.allowedOrigins})
	if err != nil {
		return errors.Join(err, e.closeCreatedTarget(targetID))
	}
	defer func() {
		retErr = errors.Join(retErr, page.Close())
	}()

	if e.opts.width > 0 && e.opts.height > 0 {
		if err := page.SetViewport(e.opts.width, e.opts.height); err != nil {
			return fmt.Errorf("browse: failed to set viewport: %w", err)
		}
	}

	if e.opts.userAgent != "" {
		if err := page.SetUserAgent(e.opts.userAgent); err != nil {
			return fmt.Errorf("browse: failed to set user agent: %w", err)
		}
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
