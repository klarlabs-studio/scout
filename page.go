package browse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/wait"
)

// flatNode holds a subset of a DOM node from getFlattenedDocument.
type flatNode struct {
	NodeID     int64    `json:"nodeId"`
	NodeType   int      `json:"nodeType"`
	NodeName   string   `json:"nodeName"`
	Attributes []string `json:"attributes,omitempty"`
}

// Page wraps a CDP session for a single browser tab.
type Page struct {
	conn               *cdp.Conn
	ctx                context.Context
	cancel             context.CancelFunc
	targetID           string
	closeTarget        bool
	sessionID          string
	timeout            time.Duration
	rootNodeID         int64 // cached DOM root node ID
	urlValidator       URLValidator
	urlPolicyMu        sync.RWMutex
	urlPolicyInstalled bool
	orphanFrameMu      sync.Mutex
	orphanFramePages   []*Page
	initCtx            context.Context // non-nil only while attaching/configuring the page
	flattenedNodes     []flatNode      // cached flattened DOM (pierce: true)

	fetch         *fetchInterceptor // lazy CDP Fetch interception multiplexer
	navOrigin     string            // origin of the last caller-initiated Navigate
	navOriginMu   sync.Mutex
	policyMu      sync.Mutex
	policyLog     []URLPolicyDecision
	policyDropped int
}

// FrameTargetAttachError reports setup and cleanup failures while retaining
// the attached CDP session so callers can retry DetachContext during cleanup.
type FrameTargetAttachError struct {
	Page             *Page
	AttachedTargetID string
	SetupError       error
	CleanupError     error
}

// LateFrameAttachError reports a canceled attach while retaining any late
// session cleanup handles for the owning agent session.
type LateFrameAttachError struct {
	Cause error
}

func (e *LateFrameAttachError) Error() string {
	return fmt.Sprintf("browse: frame target attachment canceled: %v", e.Cause)
}
func (e *LateFrameAttachError) Unwrap() error { return e.Cause }

func (p *Page) retainOrphanFramePage(frame *Page) {
	if p == nil || frame == nil {
		return
	}
	p.orphanFrameMu.Lock()
	defer p.orphanFrameMu.Unlock()
	for _, pending := range p.orphanFramePages {
		if pending == frame || pending.sessionID == frame.sessionID {
			return
		}
	}
	p.orphanFramePages = append(p.orphanFramePages, frame)
}

func (e *FrameTargetAttachError) Error() string {
	if e == nil {
		return "browse: frame target attachment failed"
	}
	joined := errors.Join(e.SetupError, e.CleanupError)
	if joined == nil {
		return "browse: frame target attachment failed"
	}
	return joined.Error()
}

func (e *FrameTargetAttachError) Unwrap() []error {
	if e == nil {
		return nil
	}
	var errs []error
	if e.SetupError != nil {
		errs = append(errs, e.SetupError)
	}
	if e.CleanupError != nil {
		errs = append(errs, e.CleanupError)
	}
	return errs
}

// URLPolicyDecisions returns a snapshot of intercepted URL policy decisions.
// The per-page log is bounded; dropped reports decisions omitted after the cap.
func (p *Page) URLPolicyDecisions() (decisions []URLPolicyDecision, dropped int) {
	p.policyMu.Lock()
	defer p.policyMu.Unlock()
	return append([]URLPolicyDecision(nil), p.policyLog...), p.policyDropped
}

// URLPolicyInstalled reports whether this page has an active page-target URL
// policy interceptor. It does not report service-worker or WebSocket coverage.
func (p *Page) URLPolicyInstalled() bool {
	if p == nil {
		return false
	}
	p.urlPolicyMu.RLock()
	defer p.urlPolicyMu.RUnlock()
	return p.urlPolicyInstalled
}

func (p *Page) setURLPolicyInstalled(installed bool) {
	p.urlPolicyMu.Lock()
	p.urlPolicyInstalled = installed
	p.urlPolicyMu.Unlock()
}

func (p *Page) recordURLPolicyDecision(decision URLPolicyDecision) {
	p.policyMu.Lock()
	defer p.policyMu.Unlock()
	if len(p.policyLog) >= maxURLPolicyDecisions {
		p.policyDropped++
		return
	}
	p.policyLog = append(p.policyLog, decision)
}

func (p *Page) finishURLPolicyDecision(decision *URLPolicyDecision, err error) {
	if decision == nil {
		return
	}
	if err != nil {
		decision.Resolution = "failed"
		decision.ResolutionError = err.Error()
	} else {
		decision.Resolution = "resolved"
	}
	p.recordURLPolicyDecision(*decision)
}

// TargetID returns the stable CDP target ID for this page within its browser.
// It is useful for correlating tab operations and diagnostics across page state
// changes; callers should not treat it as a persistent identifier across browser restarts.
func (p *Page) TargetID() string {
	if p == nil {
		return ""
	}
	return p.targetID
}

// call sends a CDP command scoped to this page's session, with a per-call
// deadline. Without one, a call blocks until the connection closes — so a hung
// Chrome (or a handler abandoned by the watchdog) wedges every operation and
// holds the session lock indefinitely. The deadline bounds that to the session
// timeout.
func (p *Page) call(method string, params any) (json.RawMessage, error) {
	parent := p.ctx
	if p.initCtx != nil {
		parent = p.initCtx
	}
	ctx, cancel := context.WithTimeout(parent, p.callTimeout())
	defer cancel()
	return p.conn.CallSessionCtx(ctx, p.sessionID, method, params)
}

// CallSessionContext sends a target-session command using the page's CDP
// session. It is intended for experimental protocol domains that Scout does
// not wrap yet; callers remain responsible for validating the response.
func (p *Page) CallSessionContext(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.CallContext(ctx, method, params)
}

// ActivateTargetContext sends a browser-level target activation. Unlike
// Page.bringToFront, this command is answered by the browser process and can
// revive a discarded renderer without waiting on that renderer.
func (p *Page) ActivateTargetContext(ctx context.Context) error {
	if p == nil || p.conn == nil || p.targetID == "" {
		return fmt.Errorf("browse: page target is unavailable")
	}
	callCtx, cancel := mergeContexts(ctx, p.ctx)
	defer cancel()
	_, err := p.conn.CallSessionCtx(callCtx, "", "Target.activateTarget", map[string]string{"targetId": p.targetID})
	return err
}

// OnRawSession registers a page-session event handler with raw CDP JSON.
func (p *Page) OnRawSession(method string, handler func(json.RawMessage)) func() {
	if p == nil || p.conn == nil {
		return func() {}
	}
	return p.conn.OnSession(p.sessionID, method, handler)
}

// OnDetached registers a callback for detachment of this page's CDP session.
// Target.detachedFromTarget is a browser-level event, so the handler filters it
// by this Page's private session ID. The callback must not issue CDP calls.
func (p *Page) OnDetached(handler func()) func() {
	if p == nil || p.conn == nil || p.sessionID == "" || handler == nil {
		return func() {}
	}
	return p.conn.On("Target.detachedFromTarget", func(raw json.RawMessage) {
		var event struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(raw, &event) == nil && event.SessionID == p.sessionID {
			handler()
		}
	})
}

// callTimeout is the per-CDP-call deadline: the session timeout, falling back to
// the CDP default when unset so a zero timeout can't mean "wait forever".
func (p *Page) callTimeout() time.Duration {
	if p.timeout > 0 {
		return p.timeout
	}
	return cdp.DefaultCallTimeout
}

func newPage(conn *cdp.Conn, targetID string, timeout time.Duration, validator URLValidator) (*Page, error) {
	return newPageContext(context.Background(), conn, targetID, timeout, validator)
}

func newPageContext(ctx context.Context, conn *cdp.Conn, targetID string, timeout time.Duration, validator URLValidator) (*Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID, err := conn.AttachToTargetCtx(ctx, targetID)
	if err != nil {
		return nil, fmt.Errorf("browse: failed to attach to target: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Page{
		conn:         conn,
		ctx:          ctx,
		cancel:       cancel,
		targetID:     targetID,
		closeTarget:  true,
		sessionID:    sessionID,
		timeout:      timeout,
		urlValidator: validator,
		initCtx:      ctx,
	}
	cleanupAttach := func() error {
		cancel()
		conn.RemoveSessionHandlers(sessionID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		err := conn.DetachFromTargetCtx(cleanupCtx, sessionID)
		cleanupCancel()
		if err != nil {
			return fmt.Errorf("browse: failed to detach target %q after page setup failure: %w", targetID, err)
		}
		return nil
	}

	// Enable required domains on this session
	for _, domain := range []string{"Page", "DOM", "Runtime"} {
		if _, err := p.call(domain+".enable", nil); err != nil {
			return nil, errors.Join(fmt.Errorf("browse: failed to enable %s: %w", domain, err), cleanupAttach())
		}
	}

	// Re-validate every navigation/redirect against the URL policy at request
	// time — this blocks a public URL that redirects to an internal host, which
	// the one-shot pre-navigation check can't catch. Trusted (AllowPrivateIPs)
	// sessions skip it.
	if !p.urlValidator.AllowPrivateIPs || len(p.urlValidator.AllowedOrigins) > 0 {
		if err := p.installURLPolicy(); err != nil {
			return nil, errors.Join(fmt.Errorf("browse: failed to install URL policy: %w", err), cleanupAttach())
		}
		p.setURLPolicyInstalled(true)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, cleanupAttach())
	}
	p.initCtx = nil

	return p, nil
}

// AttachFrameTargetContext attaches a CDP frame target while retaining this
// page's stable top-level target identity. The returned Page routes protocol
// calls to the frame session and DetachContext releases only Scout's session.
// The caller must verify the target's parent-frame binding before attaching.
func (p *Page) AttachFrameTargetContext(ctx context.Context, targetID string) (*Page, error) {
	if p == nil || p.conn == nil || targetID == "" {
		return nil, fmt.Errorf("browse: frame target is unavailable")
	}
	// Keep observing a bounded late response after returning on cancellation so
	// a child session created by Chrome can be detached without extending ctx.
	sessionID, lateResult, err := p.conn.AttachToTargetLateResultCtx(ctx, targetID, 250*time.Millisecond)
	if err != nil {
		if lateResult != nil {
			go func() {
				select {
				case response := <-lateResult:
					if response == nil || response.Error != nil {
						return
					}
					var attached struct {
						SessionID string `json:"sessionId"`
					}
					if json.Unmarshal(response.Result, &attached) != nil || attached.SessionID == "" {
						return
					}
					cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
					detachErr := p.conn.DetachFromTargetCtx(cleanupCtx, attached.SessionID)
					cancel()
					if detachErr != nil {
						frameCtx, frameCancel := context.WithCancel(p.ctx)
						frame := &Page{conn: p.conn, ctx: frameCtx, cancel: frameCancel, targetID: p.targetID, sessionID: attached.SessionID, timeout: p.timeout, urlValidator: p.urlValidator, closeTarget: false}
						p.retainOrphanFramePage(frame)
					}
				case <-p.conn.Done():
				}
			}()
		}
		p.orphanFrameMu.Lock()
		var pending *Page
		if n := len(p.orphanFramePages); n > 0 {
			pending = p.orphanFramePages[n-1]
		}
		p.orphanFrameMu.Unlock()
		if pending != nil {
			return nil, &FrameTargetAttachError{Page: pending, AttachedTargetID: targetID, SetupError: err, CleanupError: errors.New("browse: late frame session cleanup will be retried when the owner page closes")}
		}
		return nil, &LateFrameAttachError{Cause: err}
	}
	frameCtx, frameCancel := context.WithCancel(p.ctx)
	frame := &Page{
		conn: p.conn, ctx: frameCtx, cancel: frameCancel, targetID: p.targetID, sessionID: sessionID,
		timeout: p.timeout, urlValidator: p.urlValidator, closeTarget: false,
		urlPolicyInstalled: p.URLPolicyInstalled(),
	}
	cleanup := func() error {
		frameCancel()
		frame.conn.RemoveSessionHandlers(sessionID)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := frame.conn.DetachFromTargetCtx(cleanupCtx, sessionID); err != nil {
			return fmt.Errorf("browse: failed to detach frame target %q after setup failure: %w", targetID, err)
		}
		return nil
	}
	for _, domain := range []string{"Page", "DOM", "Runtime"} {
		if _, err := frame.call(domain+".enable", nil); err != nil {
			setupErr := fmt.Errorf("browse: failed to enable %s on frame: %w", domain, err)
			if cleanupErr := cleanup(); cleanupErr != nil {
				return nil, &FrameTargetAttachError{Page: frame, AttachedTargetID: targetID, SetupError: setupErr, CleanupError: cleanupErr}
			}
			return nil, setupErr
		}
	}
	if !frame.urlValidator.AllowPrivateIPs || len(frame.urlValidator.AllowedOrigins) > 0 {
		if err := frame.installURLPolicy(); err != nil {
			setupErr := fmt.Errorf("browse: failed to install frame URL policy: %w", err)
			if cleanupErr := cleanup(); cleanupErr != nil {
				return nil, &FrameTargetAttachError{Page: frame, AttachedTargetID: targetID, SetupError: setupErr, CleanupError: cleanupErr}
			}
			return nil, setupErr
		}
		frame.setURLPolicyInstalled(true)
	}
	return frame, nil
}

// SetURLValidator replaces this page's navigation policy. Request interception
// checks the current validator for each document request, including redirects.
func (p *Page) SetURLValidator(validator URLValidator) error {
	p.urlValidator = validator
	if p.URLPolicyInstalled() {
		return nil
	}
	if err := p.installURLPolicy(); err != nil {
		return err
	}
	p.setURLPolicyInstalled(true)
	return nil
}

// Navigate loads the given URL and waits for the page to finish loading.
// Only http:// and https:// URLs are allowed. Private IPs are blocked by default.
func (p *Page) Navigate(rawURL string) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.NavigateContext(ctx, rawURL)
}

// NavigateContext loads a URL using the caller's end-to-end deadline,
// including the Page.navigate command and document-ready wait.
func (p *Page) NavigateContext(ctx context.Context, rawURL string) error {
	if err := ctx.Err(); err != nil {
		return &NavigationError{URL: rawURL, Err: err}
	}
	if err := p.urlValidator.Validate(rawURL); err != nil {
		var policyErr *URLPolicyError
		if errors.As(err, &policyErr) {
			copy := *policyErr
			copy.TargetID = p.targetID
			if p.targetID != "" {
				copy.EvidenceReference = "target:" + p.targetID
			}
			err = &copy
		}
		return &NavigationError{URL: rawURL, Err: err}
	}

	// Record the intended target origin so header-injection rules can scope to it
	// (and not leak to cross-origin subresources or redirect destinations).
	p.setTopLevelOrigin(originOf(rawURL))

	// Invalidate cached root node ID and flattened DOM on navigation
	p.rootNodeID = 0
	p.flattenedNodes = nil

	params := map[string]string{"url": rawURL}
	result, err := p.CallContext(ctx, "Page.navigate", params)
	if err != nil {
		return navigationDispatchError(rawURL, err)
	}

	var resp struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return &NavigationOutcomeUnknownError{URL: rawURL, Err: err}
	}
	if resp.ErrorText != "" {
		return &NavigationError{URL: rawURL, Err: fmt.Errorf("%s", resp.ErrorText)}
	}

	if err := p.WaitLoadContext(ctx); err != nil {
		return &NavigationOutcomeUnknownError{URL: rawURL, Err: err}
	}
	return nil
}

// Reload reloads the current page and waits for it to finish loading.
// If ignoreCache is true, the browser cache is bypassed (hard reload).
func (p *Page) Reload(ignoreCache bool) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.ReloadContext(ctx, ignoreCache)
}

// ReloadContext reloads the current page under the caller's deadline.
func (p *Page) ReloadContext(ctx context.Context, ignoreCache bool) error {
	if err := ctx.Err(); err != nil {
		return &NavigationError{URL: "", Err: err}
	}
	// Invalidate cached DOM state on reload.
	p.rootNodeID = 0
	p.flattenedNodes = nil

	if _, err := p.CallContext(ctx, "Page.reload", map[string]any{"ignoreCache": ignoreCache}); err != nil {
		return navigationDispatchError("", err)
	}
	if err := p.WaitLoadContext(ctx); err != nil {
		return &NavigationOutcomeUnknownError{URL: "", Err: err}
	}
	return nil
}

// GoBack navigates to the previous entry in the page's navigation history and
// waits for the page to finish loading. Returns ErrNoHistoryEntry if there is
// no previous entry.
func (p *Page) GoBack() error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.GoBackContext(ctx)
}

// GoBackContext navigates to the previous history entry under the caller's deadline.
func (p *Page) GoBackContext(ctx context.Context) error { return p.navigateHistoryContext(ctx, -1) }

// GoForward navigates to the next entry in the page's navigation history and
// waits for the page to finish loading. Returns ErrNoHistoryEntry if there is
// no next entry.
func (p *Page) GoForward() error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.GoForwardContext(ctx)
}

// GoForwardContext navigates to the next history entry under the caller's deadline.
func (p *Page) GoForwardContext(ctx context.Context) error { return p.navigateHistoryContext(ctx, +1) }

func (p *Page) navigateHistoryContext(ctx context.Context, delta int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := p.CallContext(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return &NavigationError{URL: "", Err: err}
	}
	var hist struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &hist); err != nil {
		return &NavigationError{URL: "", Err: err}
	}

	target := hist.CurrentIndex + delta
	if target < 0 || target >= len(hist.Entries) {
		return ErrNoHistoryEntry
	}

	// Invalidate cached DOM state before navigating.
	p.rootNodeID = 0
	p.flattenedNodes = nil

	entry := hist.Entries[target]
	if _, err := p.CallContext(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": entry.ID}); err != nil {
		return navigationDispatchError(entry.URL, err)
	}
	if err := p.WaitLoadContext(ctx); err != nil {
		return &NavigationOutcomeUnknownError{URL: entry.URL, Err: err}
	}
	if err := p.waitForURLContext(ctx, entry.URL); err != nil {
		return &NavigationOutcomeUnknownError{URL: entry.URL, Err: err}
	}
	return nil
}

// waitForURLContext prevents a history command from returning after the old
// document (often about:blank) reports readyState=complete but before Chrome
// commits the selected history entry.
func (p *Page) waitForURLContext(ctx context.Context, want string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := p.EvaluateContext(ctx, `location.href`)
		if err == nil {
			if got, ok := result.(string); ok && got == want {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func navigationDispatchError(rawURL string, err error) error {
	var unknown *cdp.DispatchOutcomeUnknownError
	if errors.As(err, &unknown) {
		return &NavigationOutcomeUnknownError{URL: rawURL, Err: err}
	}
	return &NavigationError{URL: rawURL, Err: err}
}

// WaitLoad waits for the page load event (document.readyState == "complete").
func (p *Page) WaitLoad() error {
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	return p.WaitLoadContext(ctx)
}

// WaitLoadContext waits for the document load state using the caller context.
func (p *Page) WaitLoadContext(ctx context.Context) error {
	if err := wait.ForLoad(ctx, &contextEvaluator{ctx: ctx, page: p}); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("browse: wait for page load: %w", ctx.Err())
		}
		return &TimeoutError{Operation: "page load"}
	}
	return nil
}

// WaitStable waits until no DOM mutations occur for the given duration.
// Has a hard timeout of max(d*3, 3s) to prevent hanging on SPAs with constant updates.
func (p *Page) WaitStable(d time.Duration) error {
	if d == 0 {
		d = 500 * time.Millisecond
	}
	// Hard timeout: never wait longer than 3x the stability window or 3 seconds
	hardTimeout := d * 3
	if hardTimeout < 3*time.Second {
		hardTimeout = 3 * time.Second
	}
	js := fmt.Sprintf(`new Promise(resolve => {
		let timer;
		const hard = setTimeout(() => { observer.disconnect(); resolve(true); }, %d);
		const observer = new MutationObserver(() => {
			clearTimeout(timer);
			timer = setTimeout(() => { clearTimeout(hard); observer.disconnect(); resolve(true); }, %d);
		});
		const root = document.body || document.documentElement;
		if (!root) { clearTimeout(hard); resolve(true); return; }
		observer.observe(root, {childList: true, subtree: true, attributes: true});
		timer = setTimeout(() => { clearTimeout(hard); observer.disconnect(); resolve(true); }, %d);
	})`, hardTimeout.Milliseconds(), d.Milliseconds(), d.Milliseconds())
	_, err := p.Evaluate(js)
	return err
}

// WaitStableContext waits for DOM mutations to settle using the caller's
// context, so cancellation interrupts the pending Runtime.evaluate request.
func (p *Page) WaitStableContext(ctx context.Context, d time.Duration) error {
	if d == 0 {
		d = 500 * time.Millisecond
	}
	hardTimeout := d * 3
	if hardTimeout < 3*time.Second {
		hardTimeout = 3 * time.Second
	}
	js := fmt.Sprintf(`new Promise(resolve => {
		let timer;
		const hard = setTimeout(() => { observer.disconnect(); resolve(true); }, %d);
		const observer = new MutationObserver(() => {
			clearTimeout(timer);
			timer = setTimeout(() => { clearTimeout(hard); observer.disconnect(); resolve(true); }, %d);
		});
		const root = document.body || document.documentElement;
		if (!root) { clearTimeout(hard); resolve(true); return; }
		observer.observe(root, {childList: true, subtree: true, attributes: true});
		timer = setTimeout(() => { clearTimeout(hard); observer.disconnect(); resolve(true); }, %d);
	})`, hardTimeout.Milliseconds(), d.Milliseconds(), d.Milliseconds())
	_, err := p.EvaluateContext(ctx, js)
	return err
}

// URL returns the current page URL.
func (p *Page) URL() (string, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.URLContext(ctx)
}

// URLContext returns the current page URL using ctx and the page lifetime.
func (p *Page) URLContext(ctx context.Context) (string, error) {
	result, err := p.EvaluateContext(ctx, `window.location.href`)
	if err != nil {
		return "", err
	}
	s, _ := result.(string)
	return s, nil
}

// HTML returns the full page HTML.
func (p *Page) HTML() (string, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.HTMLContext(ctx)
}

// HTMLContext returns the page's current document HTML using ctx and the page
// lifetime. The returned content is unbounded; callers should bound their own
// output or prefer a targeted evaluation for large pages.
func (p *Page) HTMLContext(ctx context.Context) (string, error) {
	result, err := p.EvaluateContext(ctx, `document.documentElement.outerHTML`)
	if err != nil {
		return "", err
	}
	s, _ := result.(string)
	return s, nil
}

// ScreenshotOptions configures screenshot capture.
type ScreenshotOptions struct {
	// Format is "png" (default) or "jpeg".
	Format string
	// Quality is JPEG quality 1-100. Ignored for PNG.
	Quality int
	// FullPage captures the entire scrollable page, not just the viewport.
	FullPage bool
	// Clip captures a specific region of the page.
	Clip *ClipRegion
	// MaxSize is the maximum allowed size in bytes. If the screenshot exceeds
	// this limit, it is automatically re-captured as JPEG with progressively
	// lower quality and downscaled resolution until it fits.
	// 0 means no limit. Recommended: 5*1024*1024 (5MB) for LLM contexts.
	MaxSize int
	// MaxWidth downscales the capture to this width if set. Height scales proportionally.
	// Applied via CDP's clip.scale parameter. 0 means no downscaling.
	MaxWidth int
}

// ScreenshotInfo describes the requested capture and the image returned after
// any size-budget compression. Original dimensions and size describe the first
// capture using the requested options; final values describe Data.
type ScreenshotInfo struct {
	OriginalBytes      int     `json:"original_bytes"`
	OriginalWidth      int     `json:"original_width,omitempty"`
	OriginalHeight     int     `json:"original_height,omitempty"`
	FinalBytes         int     `json:"final_bytes"`
	FinalWidth         int     `json:"final_width,omitempty"`
	FinalHeight        int     `json:"final_height,omitempty"`
	CompressionApplied bool    `json:"compression_applied"`
	Quality            int     `json:"quality,omitempty"`
	Scale              float64 `json:"scale,omitempty"`
}

// ClipRegion defines a rectangular area for screenshot clipping.
type ClipRegion struct {
	X, Y, Width, Height float64
}

// Screenshot captures the page as a PNG image (viewport only, no size limit).
func (p *Page) Screenshot() ([]byte, error) {
	return p.ScreenshotWithOptions(ScreenshotOptions{})
}

// ScreenshotCompact captures the page with a 5MB size limit.
// Automatically switches to JPEG and downscales if needed.
// Use this for LLM/agent contexts where size matters.
func (p *Page) ScreenshotCompact() ([]byte, error) {
	return p.ScreenshotWithOptions(ScreenshotOptions{
		MaxSize: 5 * 1024 * 1024,
	})
}

// ScreenshotFullPage captures the entire scrollable page as a PNG image.
func (p *Page) ScreenshotFullPage() ([]byte, error) {
	return p.ScreenshotWithOptions(ScreenshotOptions{FullPage: true})
}

// ScreenshotWithOptions captures the page with the given options.
// If MaxSize is set and the result exceeds it, the image is automatically
// re-captured with progressive quality/resolution reduction.
func (p *Page) ScreenshotWithOptions(opts ScreenshotOptions) ([]byte, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.ScreenshotWithOptionsContext(ctx, opts)
}

// ScreenshotWithOptionsContext captures a screenshot using ctx for each CDP
// call, including retries needed to meet MaxSize.
func (p *Page) ScreenshotWithOptionsContext(ctx context.Context, opts ScreenshotOptions) ([]byte, error) {
	data, _, err := p.ScreenshotWithOptionsInfoContext(ctx, opts)
	return data, err
}

// ScreenshotWithOptionsInfoContext captures an image and reports the effect of
// the MaxSize budget. It preserves the byte-only API in
// ScreenshotWithOptionsContext for callers that do not need diagnostics.
func (p *Page) ScreenshotWithOptionsInfoContext(ctx context.Context, opts ScreenshotOptions) ([]byte, ScreenshotInfo, error) {
	var info ScreenshotInfo
	if err := ctx.Err(); err != nil {
		return nil, info, err
	}
	data, err := p.captureRawContext(ctx, opts)
	if err != nil {
		return nil, info, err
	}
	info.OriginalBytes = len(data)
	info.OriginalWidth, info.OriginalHeight = screenshotDimensions(data)
	info.FinalBytes = len(data)
	info.FinalWidth, info.FinalHeight = info.OriginalWidth, info.OriginalHeight
	info.Quality = opts.Quality
	info.Scale = 1

	if opts.MaxSize <= 0 || len(data) <= opts.MaxSize {
		return data, info, nil
	}

	// Image too large — progressively reduce until it fits.
	// Strategy: switch to JPEG, then reduce quality, then downscale.
	qualities := []int{80, 60, 40, 20, 10, 5, 1}
	scales := []float64{1.0, 0.75, 0.5, 0.25, 0.125, 0.0625, 0.03125, 0.015625, 0.0078125, 0.00390625}

	for _, scale := range scales {
		for _, q := range qualities {
			if err := ctx.Err(); err != nil {
				return nil, info, err
			}
			shrunk, err := p.captureCompressedContext(ctx, opts, q, scale)
			if err != nil {
				continue
			}
			if len(shrunk) <= opts.MaxSize {
				return shrunk, screenshotInfoForResult(info, shrunk, q, scale), nil
			}
		}
	}

	// Last resort: smallest possible
	data, err = p.captureCompressedContext(ctx, opts, 1, 0.00390625)
	if err != nil {
		return nil, info, err
	}
	if len(data) > opts.MaxSize {
		return nil, screenshotInfoForResult(info, data, 1, 0.00390625), fmt.Errorf("screenshot exceeds maximum size of %d bytes after compression (got %d)", opts.MaxSize, len(data))
	}
	return data, screenshotInfoForResult(info, data, 1, 0.00390625), nil
}

func screenshotInfoForResult(info ScreenshotInfo, data []byte, quality int, scale float64) ScreenshotInfo {
	info.FinalBytes = len(data)
	info.FinalWidth, info.FinalHeight = screenshotDimensions(data)
	info.CompressionApplied = true
	info.Quality = quality
	info.Scale = scale
	return info
}

func screenshotDimensions(data []byte) (int, int) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func (p *Page) captureRawContext(ctx context.Context, opts ScreenshotOptions) ([]byte, error) {
	format := opts.Format
	if format == "" {
		format = "png"
	}

	params := map[string]any{
		"format":      format,
		"fromSurface": true,
	}
	if opts.Quality > 0 && format == "jpeg" {
		params["quality"] = opts.Quality
	}
	if opts.FullPage {
		params["captureBeyondViewport"] = true
	}
	if opts.Clip != nil {
		scale := 1.0
		if opts.MaxWidth > 0 && opts.Clip.Width > float64(opts.MaxWidth) {
			scale = float64(opts.MaxWidth) / opts.Clip.Width
		}
		params["clip"] = map[string]any{
			"x":      opts.Clip.X,
			"y":      opts.Clip.Y,
			"width":  opts.Clip.Width,
			"height": opts.Clip.Height,
			"scale":  scale,
		}
	}
	if opts.MaxWidth > 0 && opts.Clip == nil {
		// Use Emulation to override device metrics for smaller capture
		params["optimizeForSpeed"] = true
	}

	result, err := p.CallContext(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}

	return decodeBase64(resp.Data)
}

func (p *Page) captureCompressedContext(ctx context.Context, opts ScreenshotOptions, quality int, scale float64) ([]byte, error) {
	params := map[string]any{
		"format":                "jpeg",
		"quality":               quality,
		"fromSurface":           true,
		"optimizeForSpeed":      true,
		"captureBeyondViewport": opts.FullPage,
	}
	if opts.Clip != nil {
		params["clip"] = map[string]any{
			"x":      opts.Clip.X,
			"y":      opts.Clip.Y,
			"width":  opts.Clip.Width,
			"height": opts.Clip.Height,
			"scale":  scale,
		}
	} else if scale < 1.0 {
		// Get current viewport size and create a clip with downscale
		viewportJS := `JSON.stringify({w: window.innerWidth, h: window.innerHeight})`
		vpResult, err := p.EvaluateContext(ctx, viewportJS)
		if err == nil {
			if vpStr, ok := vpResult.(string); ok {
				var vp struct {
					W float64 `json:"w"`
					H float64 `json:"h"`
				}
				if err := json.Unmarshal([]byte(vpStr), &vp); err == nil && vp.W > 0 {
					params["clip"] = map[string]any{
						"x":      0,
						"y":      0,
						"width":  vp.W,
						"height": vp.H,
						"scale":  scale,
					}
				}
			}
		}
	}

	result, err := p.CallContext(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}

	return decodeBase64(resp.Data)
}

// ScreenshotElement captures a screenshot of a specific element by its node ID.
func (p *Page) ScreenshotElement(nodeID int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.ScreenshotElementContext(ctx, nodeID)
}

// ScreenshotElementContext captures a specific element using the caller's
// deadline for both its box-model lookup and screenshot capture.
func (p *Page) ScreenshotElementContext(ctx context.Context, nodeID int64) ([]byte, error) {
	// Get the element's bounding box
	result, err := p.CallContext(ctx, "DOM.getBoxModel", map[string]any{"nodeId": nodeID})
	if err != nil {
		return nil, fmt.Errorf("browse: failed to get box model: %w", err)
	}

	var box struct {
		Model struct {
			Content []float64 `json:"content"` // quad: [x1,y1, x2,y2, x3,y3, x4,y4]
		} `json:"model"`
	}
	if err := json.Unmarshal(result, &box); err != nil {
		return nil, err
	}
	if len(box.Model.Content) < 8 {
		return nil, fmt.Errorf("browse: invalid box model for element screenshot")
	}

	// Content quad: top-left, top-right, bottom-right, bottom-left
	x := box.Model.Content[0]
	y := box.Model.Content[1]
	width := box.Model.Content[2] - box.Model.Content[0]
	height := box.Model.Content[5] - box.Model.Content[1]

	return p.ScreenshotWithOptionsContext(ctx, ScreenshotOptions{
		Clip: &ClipRegion{
			X:      x,
			Y:      y,
			Width:  width,
			Height: height,
		},
	})
}

// PDFOptions configures PDF generation.
type PDFOptions struct {
	Landscape       bool
	PrintBackground bool
	Scale           float64
	PaperWidth      float64 // inches, default 8.5
	PaperHeight     float64 // inches, default 11
	MarginTop       float64 // inches, default 0.4
	MarginBottom    float64 // inches, default 0.4
	MarginLeft      float64 // inches, default 0.4
	MarginRight     float64 // inches, default 0.4
	PageRanges      string  // e.g. "1-5", "1,3,5-7"
}

// PDF generates a PDF of the current page with default options.
func (p *Page) PDF() ([]byte, error) {
	return p.PDFWithOptions(PDFOptions{})
}

// PDFWithOptions generates a PDF with the given options.
func (p *Page) PDFWithOptions(opts PDFOptions) ([]byte, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.PDFWithOptionsContext(ctx, opts)
}

// PDFWithOptionsContext generates a PDF of the current page using ctx for the
// underlying CDP command.
func (p *Page) PDFWithOptionsContext(ctx context.Context, opts PDFOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params := map[string]any{
		"transferMode": "ReturnAsBase64",
	}
	if opts.Landscape {
		params["landscape"] = true
	}
	if opts.PrintBackground {
		params["printBackground"] = true
	}
	if opts.Scale > 0 {
		params["scale"] = opts.Scale
	}
	if opts.PaperWidth > 0 {
		params["paperWidth"] = opts.PaperWidth
	}
	if opts.PaperHeight > 0 {
		params["paperHeight"] = opts.PaperHeight
	}
	if opts.MarginTop > 0 {
		params["marginTop"] = opts.MarginTop
	}
	if opts.MarginBottom > 0 {
		params["marginBottom"] = opts.MarginBottom
	}
	if opts.MarginLeft > 0 {
		params["marginLeft"] = opts.MarginLeft
	}
	if opts.MarginRight > 0 {
		params["marginRight"] = opts.MarginRight
	}
	if opts.PageRanges != "" {
		params["pageRanges"] = opts.PageRanges
	}

	result, err := p.CallContext(ctx, "Page.printToPDF", params)
	if err != nil {
		return nil, fmt.Errorf("browse: PDF generation failed: %w", err)
	}

	var resp struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}

	return decodeBase64(resp.Data)
}

// SetViewport sets the page viewport dimensions (non-mobile, scale 1).
func (p *Page) SetViewport(width, height int) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.SetViewportContext(ctx, width, height)
}

// SetViewportContext sets a non-mobile, scale-1 viewport using ctx.
func (p *Page) SetViewportContext(ctx context.Context, width, height int) error {
	return p.SetDeviceMetricsContext(ctx, width, height, 1, false)
}

// SetDeviceMetrics overrides the page's device metrics via the CDP Emulation
// domain, so width-based CSS media queries resolve against the given size.
// scale is the device pixel ratio (<=0 defaults to 1); mobile toggles mobile
// emulation (touch + meta-viewport handling). The override persists across
// navigations on the same page until changed.
func (p *Page) SetDeviceMetrics(width, height int, scale float64, mobile bool) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.SetDeviceMetricsContext(ctx, width, height, scale, mobile)
}

// SetDeviceMetricsContext overrides device metrics using ctx and the page
// lifetime.
func (p *Page) SetDeviceMetricsContext(ctx context.Context, width, height int, scale float64, mobile bool) error {
	if scale <= 0 {
		scale = 1
	}
	params := map[string]any{
		"width":             width,
		"height":            height,
		"deviceScaleFactor": scale,
		"mobile":            mobile,
	}
	_, err := p.CallContext(ctx, "Emulation.setDeviceMetricsOverride", params)
	return err
}

// Evaluate executes JavaScript and returns the result value.
func (p *Page) Evaluate(expression string) (any, error) {
	params := map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	result, err := p.call("Runtime.evaluate", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	if resp.ExceptionDetails != nil {
		return nil, fmt.Errorf("browse: js error: %s", resp.ExceptionDetails.Text)
	}

	var val any
	if len(resp.Result.Value) == 0 {
		return nil, nil // undefined result
	}
	if err := json.Unmarshal(resp.Result.Value, &val); err != nil {
		return nil, nil //nolint:nilerr // unmarshal of undefined/void JS results is expected
	}
	return val, nil
}

// EvaluateContext executes JavaScript using the caller's context for the CDP
// request. It is useful in compound operations with a shared deadline.
func (p *Page) EvaluateContext(ctx context.Context, expression string) (any, error) {
	return (&contextEvaluator{ctx: ctx, page: p}).Evaluate(expression)
}

// getRootNodeID returns the cached DOM root node ID, fetching it if needed.
func (p *Page) getRootNodeID() (int64, error) {
	if p.rootNodeID != 0 {
		return p.rootNodeID, nil
	}
	result, err := p.call("DOM.getDocument", map[string]any{"depth": 0})
	if err != nil {
		return 0, err
	}
	var doc struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		return 0, err
	}
	p.rootNodeID = doc.Root.NodeID
	return p.rootNodeID, nil
}

// QuerySelector finds the first element matching the CSS selector and returns its node ID.
// On a stale root nodeId (SPA reconciled the document), the cache is invalidated and
// the lookup is retried once.
func (p *Page) QuerySelector(selector string) (int64, error) {
	nodeID, err := p.querySelectorOnce(selector)
	if err != nil && isStaleNodeError(err) {
		p.InvalidateNodeCache()
		nodeID, err = p.querySelectorOnce(selector)
	}
	return nodeID, err
}

// QuerySelectorContext finds the first element matching a CSS selector using
// only CDP commands bound to ctx. It deliberately avoids the shared node cache.
func (p *Page) QuerySelectorContext(ctx context.Context, selector string) (int64, error) {
	result, err := p.CallContext(ctx, "DOM.getDocument", map[string]any{"depth": 0})
	if err != nil {
		return 0, err
	}
	var doc struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		return 0, err
	}
	qResult, err := p.CallContext(ctx, "DOM.querySelector", map[string]any{"nodeId": doc.Root.NodeID, "selector": selector})
	if err != nil {
		return 0, err
	}
	var qResp struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := json.Unmarshal(qResult, &qResp); err != nil {
		return 0, err
	}
	if qResp.NodeID == 0 {
		return 0, &ElementNotFoundError{Selector: selector}
	}
	return qResp.NodeID, nil
}

// ResolveNodeContext resolves a DOM node using the caller's context.
func (p *Page) ResolveNodeContext(ctx context.Context, nodeID int64) (string, error) {
	result, err := p.CallContext(ctx, "DOM.resolveNode", map[string]any{"nodeId": nodeID})
	if err != nil {
		return "", err
	}
	var resp struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", err
	}
	return resp.Object.ObjectID, nil
}

func (p *Page) querySelectorOnce(selector string) (int64, error) {
	rootID, err := p.getRootNodeID()
	if err != nil {
		return 0, err
	}

	qResult, err := p.call("DOM.querySelector", map[string]any{
		"nodeId":   rootID,
		"selector": selector,
	})
	if err != nil {
		return 0, err
	}

	var qResp struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := json.Unmarshal(qResult, &qResp); err != nil {
		return 0, err
	}
	if qResp.NodeID == 0 {
		return 0, &ElementNotFoundError{Selector: selector}
	}
	return qResp.NodeID, nil
}

// InvalidateNodeCache clears the cached root nodeId + flattened DOM, forcing the
// next selector lookup to re-fetch from CDP. Call this after observing a stale
// nodeId error (-32000 "Could not find node with given id") to force re-resolution.
func (p *Page) InvalidateNodeCache() {
	p.rootNodeID = 0
	p.flattenedNodes = nil
}

// isStaleNodeError reports whether an error indicates a CDP nodeId that no
// longer maps to a live DOM node — typically after a framework re-render.
func isStaleNodeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not find node") ||
		strings.Contains(msg, "no node with given id") ||
		strings.Contains(msg, "no node found")
}

// IsStaleNodeError is the exported predicate for callers wrapping Selection methods
// who want to detect stale-nodeId errors and retry resolution.
func IsStaleNodeError(err error) bool { return isStaleNodeError(err) }

// QuerySelectorAll finds all elements matching the CSS selector.
func (p *Page) QuerySelectorAll(selector string) ([]int64, error) {
	rootID, err := p.getRootNodeID()
	if err != nil {
		return nil, err
	}

	qResult, err := p.call("DOM.querySelectorAll", map[string]any{
		"nodeId":   rootID,
		"selector": selector,
	})
	if err != nil {
		return nil, err
	}

	var qResp struct {
		NodeIDs []int64 `json:"nodeIds"`
	}
	if err := json.Unmarshal(qResult, &qResp); err != nil {
		return nil, err
	}
	return qResp.NodeIDs, nil
}

// QuerySelectorPiercing finds the first element matching the selector,
// piercing through shadow DOM boundaries. Uses DOM.getFlattenedDocument
// with pierce:true for a single-call flattened DOM traversal, falling
// back to JS-based search if the flattened approach finds no match.
func (p *Page) QuerySelectorPiercing(selector string) (int64, error) {
	nodeID, err := p.QuerySelector(selector)
	if err == nil {
		return nodeID, nil
	}

	nodes, flatErr := p.getFlattenedNodes()
	if flatErr == nil {
		if nid := matchFlatNode(nodes, selector); nid != 0 {
			return nid, nil
		}
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		function deepFind(root, sel) {
			const result = root.querySelector(sel);
			if (result) { result.setAttribute('data-scout-shadow', 'true'); return true; }
			for (const el of root.querySelectorAll('*')) {
				if (el.shadowRoot && deepFind(el.shadowRoot, sel)) return true;
			}
			return false;
		}
		return deepFind(document, %s);
	})()`, selectorJSON)

	result, evalErr := p.Evaluate(js)
	if evalErr != nil {
		return 0, err
	}
	if b, ok := result.(bool); !ok || !b {
		return 0, err
	}

	nodeID, err2 := p.QuerySelector("[data-scout-shadow]")
	if err2 != nil {
		return 0, err
	}

	_, _ = p.Evaluate(`document.querySelector('[data-scout-shadow]')?.removeAttribute('data-scout-shadow')`)
	return nodeID, nil
}

// QuerySelectorPiercingContext resolves a CSS selector across open shadow roots
// with all CDP work tied to the caller's context.
func (p *Page) QuerySelectorPiercingContext(ctx context.Context, selector string) (int64, error) {
	selectorJSON, _ := json.Marshal(selector)
	mark := fmt.Sprintf("scout-%d", time.Now().UnixNano())
	markJSON, _ := json.Marshal(mark)
	cleanupJS := fmt.Sprintf(`(function(){function clean(root){if(!root)return;for(const e of root.querySelectorAll('[data-scout-context-node="%s"]'))e.removeAttribute('data-scout-context-node');for(const e of root.querySelectorAll('*'))if(e.shadowRoot)clean(e.shadowRoot)}clean(document)})()`, mark)
	defer func() {
		cleanupCtx := ctx
		var cancel context.CancelFunc
		if ctx.Err() != nil {
			cleanupCtx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
		}
		_, _ = p.EvaluateContext(cleanupCtx, cleanupJS)
	}()
	js := fmt.Sprintf(`(function(){function find(root,sel){if(!root)return null;try{const found=root.querySelector(sel);if(found)return found}catch(_){}const all=root.querySelectorAll?root.querySelectorAll('*'):[];for(const el of all){if(el.shadowRoot){const found=find(el.shadowRoot,sel);if(found)return found}}return null}const el=find(document,%s);if(!el)return false;el.setAttribute('data-scout-context-node',%s);return true})()`, string(selectorJSON), string(markJSON))
	found, err := p.EvaluateContext(ctx, js)
	if err != nil {
		return 0, err
	}
	if found != true {
		return 0, &ElementNotFoundError{Selector: selector}
	}
	result, err := p.CallContext(ctx, "DOM.getFlattenedDocument", map[string]any{"depth": -1, "pierce": true})
	if err != nil {
		return 0, err
	}
	var resp struct {
		Nodes []flatNode `json:"nodes"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return 0, err
	}
	nodeID := matchFlatNode(resp.Nodes, `[data-scout-context-node="`+mark+`"]`)
	if nodeID == 0 {
		return 0, &ElementNotFoundError{Selector: selector}
	}
	return nodeID, nil
}

// getFlattenedNodes returns a cached flattened DOM tree that pierces shadow roots.
// The cache is invalidated when rootNodeID is reset (on navigation).
func (p *Page) getFlattenedNodes() ([]flatNode, error) {
	if p.flattenedNodes != nil {
		return p.flattenedNodes, nil
	}
	result, err := p.call("DOM.getFlattenedDocument", map[string]any{
		"depth":  -1,
		"pierce": true,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Nodes []flatNode `json:"nodes"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	p.flattenedNodes = resp.Nodes
	return p.flattenedNodes, nil
}

// matchFlatNode searches flattened nodes for a simple selector match.
// Supports #id, .class, tag, and bare-attribute selectors against the
// pierce:true node list. Examples:
//
//	#login           — id="login"
//	.btn             — class contains "btn"
//	input            — any <input>
//	[data-scout-id="f1"]   — bare attribute
//	input[type="email"]    — tag + attribute
func matchFlatNode(nodes []flatNode, selector string) int64 {
	tag, attrKey, attrVal, ok := splitAttrSelector(selector)
	for _, n := range nodes {
		if n.NodeType != 1 {
			continue
		}
		attrs := attrMap(n.Attributes)
		switch {
		case strings.HasPrefix(selector, "#"):
			if attrs["id"] == selector[1:] {
				return n.NodeID
			}
		case strings.HasPrefix(selector, "."):
			cls := selector[1:]
			for _, c := range strings.Fields(attrs["class"]) {
				if c == cls {
					return n.NodeID
				}
			}
		case ok:
			if tag != "" && !strings.EqualFold(n.NodeName, tag) {
				continue
			}
			if attrs[attrKey] == attrVal {
				return n.NodeID
			}
		case !strings.ContainsAny(selector, "#.[]:>+~ "):
			if strings.EqualFold(n.NodeName, selector) {
				return n.NodeID
			}
		}
	}
	return 0
}

// splitAttrSelector parses "tag[attr=\"val\"]" or "[attr=\"val\"]"
// shapes. Returns ok=false for anything more elaborate. Caller falls
// back to other branches when ok=false.
func splitAttrSelector(sel string) (tag, key, val string, ok bool) {
	lb := strings.Index(sel, "[")
	rb := strings.LastIndex(sel, "]")
	if lb < 0 || rb != len(sel)-1 || rb-lb < 3 {
		return "", "", "", false
	}
	tag = sel[:lb]
	inner := sel[lb+1 : rb]
	eq := strings.Index(inner, "=")
	if eq < 1 {
		return "", "", "", false
	}
	key = strings.TrimSpace(inner[:eq])
	val = strings.TrimSpace(inner[eq+1:])
	val = strings.Trim(val, `"'`)
	if key == "" {
		return "", "", "", false
	}
	return tag, key, val, true
}

func attrMap(attrs []string) map[string]string {
	m := make(map[string]string, len(attrs)/2)
	for i := 0; i+1 < len(attrs); i += 2 {
		m[attrs[i]] = attrs[i+1]
	}
	return m
}

// ResolveNode resolves a DOM nodeId to a Runtime remote object ID.
func (p *Page) ResolveNode(nodeID int64) (string, error) {
	result, err := p.call("DOM.resolveNode", map[string]any{
		"nodeId": nodeID,
	})
	if err != nil {
		return "", err
	}

	var resp struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", err
	}
	return resp.Object.ObjectID, nil
}

// Call sends a raw CDP command scoped to this page's session.
// This is an escape hatch for advanced CDP operations not covered by the Page API.
func (p *Page) Call(method string, params any) (json.RawMessage, error) {
	return p.call(method, params)
}

// CallContext sends a page-scoped CDP command using the caller's context.
func (p *Page) CallContext(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if p == nil || p.conn == nil {
		return nil, fmt.Errorf("browse: page is unavailable")
	}
	callCtx, cancel := mergeContexts(ctx, p.ctx)
	defer cancel()
	return p.conn.CallSessionCtx(callCtx, p.sessionID, method, params)
}

func mergeContexts(primary, lifetime context.Context) (context.Context, context.CancelFunc) {
	if primary == nil {
		primary = context.Background()
	}
	ctx, cancel := context.WithCancel(primary)
	stop := context.AfterFunc(lifetime, cancel)
	// AfterFunc schedules its callback asynchronously. Observe an already-ended
	// page lifetime before the caller can dispatch work with the merged context.
	if lifetime.Err() != nil {
		stop()
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

// SetUserAgent sets the user agent string for this page.
func (p *Page) SetUserAgent(ua string) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.SetUserAgentContext(ctx, ua)
}

// SetUserAgentContext sets the user agent override under ctx and the page
// lifetime.
func (p *Page) SetUserAgentContext(ctx context.Context, ua string) error {
	if _, err := p.CallContext(ctx, "Network.enable", nil); err != nil {
		return fmt.Errorf("browse: enable Network domain before setting user agent: %w", err)
	}
	_, err := p.CallContext(ctx, "Network.setUserAgentOverride", map[string]any{
		"userAgent": ua,
	})
	return err
}

// OnSession registers an event handler scoped to this page's session.
// Events from other pages/sessions are filtered out.
// Returns an unsubscribe function to remove the handler.
func (p *Page) OnSession(method string, handler func(params map[string]any)) func() {
	return p.conn.OnSession(p.sessionID, method, func(raw json.RawMessage) {
		var params map[string]any
		if err := json.Unmarshal(raw, &params); err == nil {
			handler(params)
		}
	})
}

// WaitForSelector waits until an element matching the selector exists in the DOM.
func (p *Page) WaitForSelector(selector string) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	return p.WaitForSelectorContext(ctx, selector)
}

// WaitForSelectorContext waits for a matching element until ctx is cancelled
// or its deadline expires. The context also bounds each in-flight CDP query.
func (p *Page) WaitForSelectorContext(ctx context.Context, selector string) error {
	if err := wait.ForSelector(ctx, &contextEvaluator{ctx: ctx, page: p}, selector); err != nil {
		var invalid *wait.InvalidSelectorError
		if errors.As(err, &invalid) {
			return err
		}
		var targetGone *wait.TargetGoneError
		if errors.As(err, &targetGone) {
			return err
		}
		if ctx.Err() != nil {
			return fmt.Errorf("browse: wait for selector %q: %w", selector, ctx.Err())
		}
		return &TimeoutError{Operation: "wait for selector", Selector: selector}
	}
	return nil
}

type contextEvaluator struct {
	ctx  context.Context
	page *Page
}

func (e *contextEvaluator) Evaluate(expression string) (any, error) {
	ctx, cancel := mergeContexts(e.ctx, e.page.ctx)
	defer cancel()
	params := map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}
	result, err := e.page.CallContext(ctx, "Runtime.evaluate", params)
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, err
	}
	if response.ExceptionDetails != nil {
		return nil, fmt.Errorf("browse: js error: %s", response.ExceptionDetails.Text)
	}
	if len(response.Result.Value) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(response.Result.Value, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// Cookie represents a browser cookie.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

// Cookies returns all cookies for the current page.
func (p *Page) Cookies() ([]Cookie, error) {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.CookiesContext(ctx)
}

// CookiesContext returns cookies for the current page under ctx.
func (p *Page) CookiesContext(ctx context.Context) ([]Cookie, error) {
	result, err := p.CallContext(ctx, "Network.getCookies", nil)
	if err != nil {
		// Enable network domain and retry
		if _, enableErr := p.CallContext(ctx, "Network.enable", nil); enableErr != nil {
			return nil, errors.Join(err, fmt.Errorf("browse: enable Network domain before reading cookies: %w", enableErr))
		}
		result, err = p.CallContext(ctx, "Network.getCookies", nil)
		if err != nil {
			return nil, err
		}
	}

	var resp struct {
		Cookies []Cookie `json:"cookies"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	return resp.Cookies, nil
}

// SetCookie sets a cookie on the page.
func (p *Page) SetCookie(c Cookie) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.SetCookieContext(ctx, c)
}

// SetCookieContext sets a cookie under ctx and the page lifetime.
func (p *Page) SetCookieContext(ctx context.Context, c Cookie) error {
	if _, err := p.CallContext(ctx, "Network.enable", nil); err != nil {
		return fmt.Errorf("browse: enable Network domain before setting cookie: %w", err)
	}
	_, err := p.CallContext(ctx, "Network.setCookie", c)
	return err
}

// ClearBrowserCookies removes all browser cookies for this session.
func (p *Page) ClearBrowserCookies() error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.ClearBrowserCookiesContext(ctx)
}

// ClearBrowserCookiesContext clears browser cookies under ctx and page lifetime.
func (p *Page) ClearBrowserCookiesContext(ctx context.Context) error {
	if _, err := p.CallContext(ctx, "Network.enable", nil); err != nil {
		return fmt.Errorf("browse: enable Network domain before clearing cookies: %w", err)
	}
	_, err := p.CallContext(ctx, "Network.clearBrowserCookies", nil)
	return err
}

// DeleteCookie removes one cookie by name (and optional URL/domain/path).
func (p *Page) DeleteCookie(name, url, domain, path string) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.callTimeout())
	defer cancel()
	return p.DeleteCookieContext(ctx, name, url, domain, path)
}

// DeleteCookieContext removes one cookie under ctx and the page lifetime.
func (p *Page) DeleteCookieContext(ctx context.Context, name, url, domain, path string) error {
	if _, err := p.CallContext(ctx, "Network.enable", nil); err != nil {
		return fmt.Errorf("browse: enable Network domain before deleting cookie: %w", err)
	}
	params := map[string]any{"name": name}
	if url != "" {
		params["url"] = url
	}
	if domain != "" {
		params["domain"] = domain
	}
	if path != "" {
		params["path"] = path
	}
	_, err := p.CallContext(ctx, "Network.deleteCookies", params)
	return err
}

// Close closes the page/tab and cleans up resources.
func (p *Page) Close() error {
	return p.CloseContext(context.Background())
}

// CloseContext closes the page target using the caller's deadline.
func (p *Page) CloseContext(ctx context.Context) error {
	p.cancel()
	p.conn.RemoveSessionHandlers(p.sessionID)
	if err := p.detachOrphanFramePages(ctx); err != nil {
		return err
	}
	if !p.closeTarget {
		return p.conn.DetachFromTargetCtx(ctx, p.sessionID)
	}
	return p.conn.CloseTargetCtx(ctx, p.targetID)
}

// DetachContext releases Scout's CDP session while leaving the browser target
// open. Use this when Scout attached to a caller-owned page and setup cannot
// continue; CloseContext would close the caller's tab.
func (p *Page) DetachContext(ctx context.Context) error {
	p.cancel()
	p.conn.RemoveSessionHandlers(p.sessionID)
	if err := p.detachOrphanFramePages(ctx); err != nil {
		return err
	}
	return p.conn.DetachFromTargetCtx(ctx, p.sessionID)
}

func (p *Page) detachOrphanFramePages(ctx context.Context) error {
	p.orphanFrameMu.Lock()
	pages := append([]*Page(nil), p.orphanFramePages...)
	p.orphanFrameMu.Unlock()
	var errs error
	var remaining []*Page
	for _, page := range pages {
		if page == nil {
			continue
		}
		if err := page.DetachContext(ctx); err != nil {
			errs = errors.Join(errs, err)
			remaining = append(remaining, page)
		}
	}
	p.orphanFrameMu.Lock()
	p.orphanFramePages = remaining
	p.orphanFrameMu.Unlock()
	return errs
}
