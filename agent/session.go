// Package agent provides a high-level, agent-optimized API for browser automation.
//
// Unlike the core browse package which follows Gin's middleware/handler pattern for
// human developers, the agent package is designed for AI agents and programmatic callers.
// It provides:
//   - Stateful sessions with automatic page lifecycle management
//   - Structured JSON-serializable results (not plain strings)
//   - Built-in retry and auto-wait on all operations
//   - High-level compound actions (FillForm, ExtractTable, ClickAndWait)
//   - Snapshot-based page state for agent context windows
package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	browse "github.com/felixgeelhaar/scout"
)

// Session manages a stateful browser automation session for an agent.
// All methods are goroutine-safe via an internal mutex.
type Session struct {
	mu                  sync.Mutex
	browser             browse.Browser
	page                *browse.Page
	timeout             time.Duration
	contentOpts         ContentOptions
	network             *networkState
	diffInstalled       bool
	closed              bool
	stealth             bool
	tabs                *tabManager
	recording           *recording
	history             []HistoryEntry
	tracing             bool
	trace               *traceState
	screenRec           *screenRecording
	frameID             string
	frameContextID      int64
	headless            bool
	userAgent           string
	viewport            [2]int
	allowPrivateIPs     bool
	remoteBrowserURL    string
	recoverTimeoutN     int
	consecutiveTimeouts int
	lastError           string
	lastSuccessAt       time.Time
	lastRecoveryAt      time.Time
	inflightCommands    int
	sessionDead         bool
	deadReason          string
}

// SessionConfig configures a new Session.
type SessionConfig struct {
	Headless        bool
	Timeout         time.Duration
	UserAgent       string
	Viewport        [2]int // [width, height], zero means default
	AllowPrivateIPs bool   // Allow navigation to private/loopback IPs
	RemoteCDP       string // WebSocket URL for remote Chrome (skips local launch)
	Stealth         bool   // Apply anti-detection stealth patches to every new page
}

// NewSession creates and launches a new browser session.
func NewSession(cfg SessionConfig) (*Session, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	opts := []browse.Option{
		browse.WithHeadless(cfg.Headless),
		browse.WithTimeout(cfg.Timeout),
	}
	if cfg.UserAgent != "" {
		opts = append(opts, browse.WithUserAgent(cfg.UserAgent))
	}
	if cfg.Viewport[0] > 0 && cfg.Viewport[1] > 0 {
		opts = append(opts, browse.WithViewport(cfg.Viewport[0], cfg.Viewport[1]))
	}
	if cfg.AllowPrivateIPs {
		opts = append(opts, browse.WithAllowPrivateIPs(true))
	}
	if cfg.RemoteCDP != "" {
		opts = append(opts, browse.WithRemoteCDP(cfg.RemoteCDP))
	}

	engine := browse.New(opts...)
	if err := engine.Launch(); err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	network := &networkState{
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: 200,
	}

	return &Session{
		browser:          engine,
		timeout:          cfg.Timeout,
		contentOpts:      DefaultContentOptions(),
		stealth:          cfg.Stealth,
		headless:         cfg.Headless,
		userAgent:        cfg.UserAgent,
		viewport:         cfg.Viewport,
		allowPrivateIPs:  cfg.AllowPrivateIPs,
		remoteBrowserURL: cfg.RemoteCDP,
		network:          network,
		recoverTimeoutN:  3,
	}, nil
}

// NewSessionFromBrowser creates a session from an existing Browser implementation.
// Use this to inject a mock browser for testing or to reuse a pre-configured Engine.
func NewSessionFromBrowser(b browse.Browser, cfg SessionConfig) *Session {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	network := &networkState{
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: 200,
	}

	return &Session{
		browser:          b,
		timeout:          cfg.Timeout,
		contentOpts:      DefaultContentOptions(),
		stealth:          cfg.Stealth,
		headless:         cfg.Headless,
		userAgent:        cfg.UserAgent,
		viewport:         cfg.Viewport,
		allowPrivateIPs:  cfg.AllowPrivateIPs,
		remoteBrowserURL: cfg.RemoteCDP,
		network:          network,
		recoverTimeoutN:  3,
	}
}

func (s *Session) runWithRecovery(phase string, fn func() error) error {
	s.inflightCommands++
	err := fn()
	s.inflightCommands--
	if err == nil {
		s.consecutiveTimeouts = 0
		s.lastSuccessAt = time.Now()
		s.sessionDead = false
		s.deadReason = ""
		return nil
	}

	if reason := connectionDeadReason(err); reason != "" {
		s.sessionDead = true
		s.deadReason = reason
	}

	wrapped := s.wrapDetailedError(phase, err)
	s.lastError = wrapped.Error()
	if isTimeoutLike(err) {
		s.consecutiveTimeouts++
		if s.consecutiveTimeouts >= s.recoverTimeoutN {
			if resetErr := s.resetLocked(); resetErr == nil {
				s.lastRecoveryAt = time.Now()
				s.consecutiveTimeouts = 0
				return fmt.Errorf("%w (auto-recovered)", wrapped)
			}
		}
	}
	return wrapped
}

func isTimeoutLike(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, "timeout", "deadline exceeded", "context canceled")
}

// markIfDead inspects err and, if it looks like a dead CDP socket, sets the
// session-dead flag so a follow-up Status call surfaces it. Returns err unchanged.
// Caller must hold s.mu.
func (s *Session) markIfDead(err error) error {
	if err == nil {
		return nil
	}
	if reason := connectionDeadReason(err); reason != "" {
		s.sessionDead = true
		s.deadReason = reason
	}
	return err
}

// connectionDeadReason returns a short non-empty reason when err looks like a
// dead CDP socket (broken pipe, reset, websocket closed). Empty string means
// the connection is still considered usable.
func connectionDeadReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(msg, "connection reset"):
		return "connection_reset"
	case strings.Contains(msg, "use of closed network connection"):
		return "connection_closed"
	case strings.Contains(msg, "websocket: close"):
		return "websocket_closed"
	case strings.Contains(msg, "eof") && strings.Contains(msg, "websocket"):
		return "websocket_eof"
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(strings.ToLower(s), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

func (s *Session) wrapDetailedError(phase string, err error) error {
	msg := err.Error()
	op := &OperationError{Phase: phase, OriginalError: msg}
	if u, parseErr := url.Parse(msg); parseErr == nil && u != nil && u.Host != "" {
		op.URL = u.String()
	}
	if opErr, ok := err.(*net.OpError); ok {
		op.Cause = "connection_error"
		op.Detail = opErr.Op
	}
	if strings.Contains(strings.ToLower(msg), "404") {
		op.Cause = "http_404"
		op.StatusCode = 404
	}
	if strings.Contains(strings.ToLower(msg), "403") {
		op.Cause = "http_403"
		op.StatusCode = 403
	}
	if strings.Contains(strings.ToLower(msg), "401") {
		op.Cause = "http_401"
		op.StatusCode = 401
	}
	if strings.Contains(strings.ToLower(msg), "connection refused") {
		op.Cause = "connection_refused"
	}
	if strings.Contains(strings.ToLower(msg), "timeout") || strings.Contains(strings.ToLower(msg), "deadline exceeded") {
		op.Cause = "timeout"
	}
	if strings.Contains(strings.ToLower(msg), "browser") && strings.Contains(strings.ToLower(msg), "closed") {
		op.Cause = "browser_closed"
	}
	if op.Cause == "" {
		op.Cause = "unknown"
	}
	if op.StatusCode == 0 {
		for _, token := range strings.Fields(msg) {
			if len(token) == 3 {
				if n, convErr := strconv.Atoi(token); convErr == nil && n >= 100 && n <= 599 {
					op.StatusCode = n
					break
				}
			}
		}
	}
	return op
}

func (s *Session) resetLocked() error {
	if s.page != nil {
		_ = s.page.Close()
		s.page = nil
	}
	if s.browser != nil {
		_ = s.browser.Close()
	}
	opts := []browse.Option{browse.WithHeadless(s.headless), browse.WithTimeout(s.timeout)}
	if s.userAgent != "" {
		opts = append(opts, browse.WithUserAgent(s.userAgent))
	}
	if s.viewport[0] > 0 && s.viewport[1] > 0 {
		opts = append(opts, browse.WithViewport(s.viewport[0], s.viewport[1]))
	}
	if s.allowPrivateIPs {
		opts = append(opts, browse.WithAllowPrivateIPs(true))
	}
	if s.remoteBrowserURL != "" {
		opts = append(opts, browse.WithRemoteCDP(s.remoteBrowserURL))
	}
	engine := browse.New(opts...)
	if err := engine.Launch(); err != nil {
		return fmt.Errorf("failed to reset session: %w", err)
	}
	s.browser = engine
	s.diffInstalled = false
	s.frameID = ""
	s.frameContextID = 0
	if s.network != nil {
		s.network.enabled = false
		s.network.unsub = nil
		s.network.pending = make(map[string]*NetworkCapture)
		s.network.pendingAll = make(map[string]*NetworkCapture)
		s.network.observersInstalled = false
	}
	return nil
}

// Reset force-resets browser and page state.
func (s *Session) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("session is closed")
	}
	err := s.resetLocked()
	if err == nil {
		s.lastRecoveryAt = time.Now()
		s.consecutiveTimeouts = 0
		s.lastError = ""
		s.sessionDead = false
		s.deadReason = ""
	}
	return err
}

// Status returns current session/browser health information.
func (s *Session) Status() *SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := &SessionStatus{
		BrowserAlive:        s.browser != nil && !s.closed,
		SessionAlive:        !s.closed,
		SessionDead:         s.sessionDead,
		DeadReason:          s.deadReason,
		InFlightCommands:    s.inflightCommands,
		ConsecutiveTimeouts: s.consecutiveTimeouts,
		LastError:           s.lastError,
	}
	if !s.lastSuccessAt.IsZero() {
		st.LastSuccessAt = s.lastSuccessAt.Format(time.RFC3339)
	}
	if !s.lastRecoveryAt.IsZero() {
		st.LastRecoveryAt = s.lastRecoveryAt.Format(time.RFC3339)
	}
	if s.page != nil {
		u, err := s.page.URL()
		if err == nil {
			st.CurrentURL = u
		} else if reason := connectionDeadReason(err); reason != "" {
			s.sessionDead = true
			s.deadReason = reason
			st.SessionDead = true
			st.DeadReason = reason
		}
	}
	if s.network != nil {
		s.network.mu.Lock()
		st.PendingRequests = len(s.network.pending)
		s.network.mu.Unlock()
	}
	return st
}

// Close shuts down the browser and releases all resources.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.screenRec != nil {
		s.screenRec.cleanup()
		s.screenRec = nil
	}
	if s.page != nil {
		_ = s.page.Close()
		s.page = nil
	}
	return s.browser.Close()
}

// ensurePage creates a page if none exists.
func (s *Session) ensurePage() error {
	if s.closed {
		return fmt.Errorf("session is closed")
	}
	if s.page != nil {
		return nil
	}
	// Reuse the initial about:blank page Chrome opened at launch
	page, err := s.browser.ExistingPage()
	if err != nil || page == nil {
		page, err = s.browser.NewPage()
		if err != nil {
			return fmt.Errorf("failed to create page: %w", err)
		}
	}
	if s.stealth {
		s.applyStealthPatches(page)
	}
	s.page = page
	if s.network != nil {
		_ = s.ensureNetworkObserversLocked()
	}
	return nil
}

// Navigate loads a URL and returns structured page info.
func (s *Session) Navigate(url string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	start, before := s.traceBeforeAction("navigate", "", "", url)

	// Close existing page (or the initial about:blank tab) and create fresh at target URL.
	// NewPageAt tells Chrome to create the target directly at the URL, which is more
	// reliable than attaching to an existing blank page and navigating it.
	if s.page != nil {
		_ = s.page.Close()
	} else {
		// Close the initial about:blank tab Chrome opened at launch
		if blank, err := s.browser.ExistingPage(); err == nil && blank != nil {
			_ = blank.Close()
		}
	}
	page, err := s.browser.NewPage()
	if err != nil {
		s.traceAfterAction(start, before, "navigate", "", "", url, err)
		return nil, fmt.Errorf("failed to navigate to %s: %w", url, err)
	}
	if s.stealth {
		s.applyStealthPatches(page)
	}
	s.page = page
	s.diffInstalled = false
	s.frameID = ""
	s.frameContextID = 0

	// Navigate explicitly so we attach event listeners before the load fires
	if err := s.runWithRecovery("navigate", func() error { return page.Navigate(url) }); err != nil {
		s.traceAfterAction(start, before, "navigate", "", "", url, err)
		return nil, fmt.Errorf("failed to navigate to %s: %w", url, err)
	}
	if s.network != nil {
		s.network.observersInstalled = false
	}

	s.traceAfterAction(start, before, "navigate", "", "", url, nil)
	s.recordAction(Action{Type: "navigate", Value: url})
	s.addHistory("navigate", "", url, "")
	return s.pageResult()
}

// Snapshot returns the current page state without performing any action.
func (s *Session) Snapshot() (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.pageResult()
}

func (s *Session) pageResult() (*PageResult, error) {
	url, _ := s.page.URL()
	title, _ := s.page.Evaluate(`document.title`)
	titleStr, _ := title.(string)

	return &PageResult{
		URL:   url,
		Title: titleStr,
	}, nil
}

// Click clicks an element and returns the updated page state.
func (s *Session) Click(selector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	start, before := s.traceBeforeAction("click", selector, "", "")

	if err := s.waitAndResolve(selector); err != nil {
		s.traceAfterAction(start, before, "click", selector, "", "", err)
		return nil, err
	}

	if err := s.withStaleNodeRetry(selector, func(nodeID int64) error {
		return browse.NewSelection(s.page, nodeID, selector).Click()
	}); err != nil {
		s.traceAfterAction(start, before, "click", selector, "", "", err)
		return nil, err
	}

	// Wait for any resulting navigation or DOM update
	_ = s.page.WaitStable(300 * time.Millisecond)

	s.traceAfterAction(start, before, "click", selector, "", "", nil)
	s.recordAction(Action{Type: "click", Selector: selector})
	s.addHistory("click", selector, "", "")
	return s.pageResult()
}

// ClickAndWait clicks an element and waits for a full page navigation.
func (s *Session) ClickAndWait(selector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	if err := s.withStaleNodeRetry(selector, func(nodeID int64) error {
		return browse.NewSelection(s.page, nodeID, selector).Click()
	}); err != nil {
		return nil, err
	}

	if err := s.page.WaitLoad(); err != nil {
		return nil, err
	}

	return s.pageResult()
}

// Type types text into an input element.
func (s *Session) Type(selector, text string) (*ElementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	start, before := s.traceBeforeAction("type", selector, text, "")

	if err := s.waitAndResolve(selector); err != nil {
		s.traceAfterAction(start, before, "type", selector, text, "", err)
		return nil, err
	}

	var sel *browse.Selection
	if err := s.withStaleNodeRetry(selector, func(nodeID int64) error {
		sel = browse.NewSelection(s.page, nodeID, selector)
		return sel.Input(text)
	}); err != nil {
		s.traceAfterAction(start, before, "type", selector, text, "", err)
		return nil, s.decodeNodeError(selector, err)
	}

	val, _ := sel.Value()
	s.traceAfterAction(start, before, "type", selector, text, "", nil)
	s.recordAction(Action{Type: "type", Selector: selector, Value: text})
	s.addHistory("type", selector, "", text)
	return &ElementResult{
		Selector: selector,
		Value:    val,
		Action:   "typed",
	}, nil
}

// FillForm fills multiple form fields and returns their resulting values.
func (s *Session) FillForm(fields map[string]string) (*FormResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	result := &FormResult{
		Fields: make([]FieldResult, 0, len(fields)),
	}

	for selector, value := range fields {
		var sel *browse.Selection
		if err := s.withStaleNodeRetry(selector, func(nodeID int64) error {
			sel = browse.NewSelection(s.page, nodeID, selector)
			return sel.Input(value)
		}); err != nil {
			result.Fields = append(result.Fields, FieldResult{
				Selector: selector,
				Error:    s.decodeNodeError(selector, err).Error(),
			})
			continue
		}
		actual, _ := sel.Value()
		result.Fields = append(result.Fields, FieldResult{
			Selector: selector,
			Value:    actual,
			Success:  true,
		})
	}

	result.Success = true
	for _, f := range result.Fields {
		if !f.Success {
			result.Success = false
			break
		}
	}

	return result, nil
}

// Extract returns the text content of an element.
func (s *Session) Extract(selector string) (*ElementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	if err := s.waitAndResolve(selector); err != nil {
		return nil, err
	}

	nodeID, err := s.querySelector(selector)
	if err != nil {
		return nil, err
	}
	sel := browse.NewSelection(s.page, nodeID, selector)
	text, err := sel.Text()
	if err != nil {
		return nil, err
	}

	return &ElementResult{
		Selector: selector,
		Text:     text,
		Action:   "extracted",
	}, nil
}

// ExtractAll returns text content from all matching elements.
func (s *Session) ExtractAll(selector string) (*ExtractAllResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	nodeIDs, err := s.page.QuerySelectorAll(selector)
	if err != nil {
		return nil, err
	}

	maxItems := s.contentOpts.MaxItems
	if maxItems == 0 {
		maxItems = 50
	}

	items := make([]string, 0, len(nodeIDs))
	for _, nid := range nodeIDs {
		if len(items) >= maxItems {
			break
		}
		sel := browse.NewSelection(s.page, nid, selector)
		text, err := sel.Text()
		if err != nil {
			continue
		}
		items = append(items, text)
	}

	total := len(nodeIDs)
	return &ExtractAllResult{
		Selector:  selector,
		Count:     len(items),
		Total:     total,
		Truncated: total > len(items),
		Items:     items,
	}, nil
}

// ExtractTable extracts structured table data.
func (s *Session) ExtractTable(tableSelector string) (*TableResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	// Use JS to extract everything in one call
	js := `(function() {
		const table = document.querySelector(` + jsonQuote(tableSelector) + `);
		if (!table) return null;
		const headers = Array.from(table.querySelectorAll('th')).map(h => h.textContent.trim());
		const rows = [];
		for (const tr of table.querySelectorAll('tbody tr, tr')) {
			const cells = tr.querySelectorAll('td');
			if (cells.length === 0) continue;
			rows.push(Array.from(cells).map(c => c.textContent.trim()));
		}
		return {headers: headers, rows: rows, rowCount: rows.length, colCount: headers.length};
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("table %q not found", tableSelector)
	}

	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected table result type")
	}

	tr := &TableResult{Selector: tableSelector}

	if headers, ok := m["headers"].([]any); ok {
		for _, h := range headers {
			s, _ := h.(string)
			tr.Headers = append(tr.Headers, s)
		}
	}
	if rows, ok := m["rows"].([]any); ok {
		for _, row := range rows {
			if cols, ok := row.([]any); ok {
				r := make([]string, 0, len(cols))
				for _, c := range cols {
					s, _ := c.(string)
					r = append(r, s)
				}
				tr.Rows = append(tr.Rows, r)
			}
		}
	}
	maxRows := s.contentOpts.MaxRows
	if maxRows == 0 {
		maxRows = 100
	}
	tr.RowCount = len(tr.Rows)
	tr.ColCount = len(tr.Headers)
	if len(tr.Rows) > maxRows {
		tr.Rows = tr.Rows[:maxRows]
		tr.Truncated = true
	}

	return tr, nil
}

// HasElement reports whether an element matching the selector exists right now.
// It uses the same resolver path as Click/Type so the contract is consistent:
// if HasElement returns true, a follow-up action will find the same element
// (modulo intervening DOM mutations, which the stale-node retry handles).
func (s *Session) HasElement(selector string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.page == nil {
		return false
	}
	// Invalidate root cache so we don't return a positive based on a stale tree.
	s.page.InvalidateNodeCache()
	_, err := s.querySelector(selector)
	return err == nil
}

// WaitFor waits until an element matching the selector appears.
func (s *Session) WaitFor(selector string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return err
	}
	if err := s.page.WaitForSelector(selector); err != nil {
		return s.enrichSelectorError(selector, err)
	}
	return nil
}

// Eval executes JavaScript and returns the result.
func (s *Session) Eval(js string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.page.Evaluate(js)
}

// Page returns the underlying Page for advanced operations.
// The caller must not hold the session mutex.
func (s *Session) Page() *browse.Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.page
}

// Screenshot captures the page as an image.
// Automatically compresses to fit within MaxScreenshotBytes (default 5MB).
func (s *Session) Screenshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.page.ScreenshotWithOptions(browse.ScreenshotOptions{
		MaxSize: s.contentOpts.MaxScreenshotBytes,
	})
}

// PDF generates a PDF of the page.
func (s *Session) PDF() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.page.PDF()
}

// waitAndResolve waits for an element to appear before interacting.
// Supports both CSS selectors and Playwright-style :text('...') selectors.
func (s *Session) waitAndResolve(selector string) error {
	// Try CSS first
	err := s.page.WaitForSelector(selector)
	if err == nil {
		return nil
	}
	// Try Playwright-style text selector as fallback
	_, resolveErr := s.resolveSelector(selector)
	if resolveErr == nil {
		return nil
	}
	return s.enrichSelectorError(selector, err) // wrap original CSS error with diagnostics
}

// querySelector resolves a selector to a nodeID, supporting Playwright-style syntax.
func (s *Session) querySelector(selector string) (int64, error) {
	return s.resolveSelector(selector)
}

// withStaleNodeRetry runs an action with a freshly-resolved nodeID. If the action
// fails with a CDP "Could not find node" error — typically Vue/React reconciling
// the DOM between resolution and action — the selector is re-resolved against a
// fresh DOM cache and the action is retried once. Caller must hold s.mu.
func (s *Session) withStaleNodeRetry(selector string, action func(nodeID int64) error) error {
	nodeID, err := s.querySelector(selector)
	if err != nil {
		return s.markIfDead(err)
	}
	if err = action(nodeID); err == nil {
		return nil
	}
	if !browse.IsStaleNodeError(err) {
		return s.markIfDead(err)
	}
	// Re-resolve from a fresh DOM cache and retry once.
	s.page.InvalidateNodeCache()
	nodeID, resolveErr := s.querySelector(selector)
	if resolveErr != nil {
		return s.markIfDead(resolveErr)
	}
	return s.markIfDead(action(nodeID))
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
