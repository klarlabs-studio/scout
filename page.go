package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/felixgeelhaar/scout/internal/cdp"
	"github.com/felixgeelhaar/scout/internal/wait"
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
	conn           *cdp.Conn
	ctx            context.Context
	cancel         context.CancelFunc
	targetID       string
	sessionID      string
	timeout        time.Duration
	rootNodeID     int64 // cached DOM root node ID
	urlValidator   URLValidator
	flattenedNodes []flatNode // cached flattened DOM (pierce: true)
}

// call sends a CDP command scoped to this page's session and context.
func (p *Page) call(method string, params any) (json.RawMessage, error) {
	return p.conn.CallSessionCtx(p.ctx, p.sessionID, method, params)
}

func newPage(conn *cdp.Conn, targetID string, timeout time.Duration, validator URLValidator) (*Page, error) {
	sessionID, err := conn.AttachToTarget(targetID)
	if err != nil {
		return nil, fmt.Errorf("browse: failed to attach to target: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Page{
		conn:         conn,
		ctx:          ctx,
		cancel:       cancel,
		targetID:     targetID,
		sessionID:    sessionID,
		timeout:      timeout,
		urlValidator: validator,
	}

	// Enable required domains on this session
	for _, domain := range []string{"Page", "DOM", "Runtime"} {
		if _, err := p.call(domain+".enable", nil); err != nil {
			cancel()
			return nil, fmt.Errorf("browse: failed to enable %s: %w", domain, err)
		}
	}

	return p, nil
}

// Navigate loads the given URL and waits for the page to finish loading.
// Only http:// and https:// URLs are allowed. Private IPs are blocked by default.
func (p *Page) Navigate(rawURL string) error {
	if err := p.urlValidator.Validate(rawURL); err != nil {
		return &NavigationError{URL: rawURL, Err: err}
	}

	// Invalidate cached root node ID and flattened DOM on navigation
	p.rootNodeID = 0
	p.flattenedNodes = nil

	params := map[string]string{"url": rawURL}
	result, err := p.call("Page.navigate", params)
	if err != nil {
		return &NavigationError{URL: rawURL, Err: err}
	}

	var resp struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return &NavigationError{URL: rawURL, Err: err}
	}
	if resp.ErrorText != "" {
		return &NavigationError{URL: rawURL, Err: fmt.Errorf("%s", resp.ErrorText)}
	}

	return p.WaitLoad()
}

// WaitLoad waits for the page load event (document.readyState == "complete").
func (p *Page) WaitLoad() error {
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	if err := wait.ForLoad(ctx, p); err != nil {
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
		observer.observe(document.body || document.documentElement, {childList: true, subtree: true, attributes: true});
		timer = setTimeout(() => { clearTimeout(hard); observer.disconnect(); resolve(true); }, %d);
	})`, hardTimeout.Milliseconds(), d.Milliseconds(), d.Milliseconds())
	_, err := p.Evaluate(js)
	return err
}

// URL returns the current page URL.
func (p *Page) URL() (string, error) {
	result, err := p.Evaluate(`window.location.href`)
	if err != nil {
		return "", err
	}
	s, _ := result.(string)
	return s, nil
}

// HTML returns the full page HTML.
func (p *Page) HTML() (string, error) {
	result, err := p.Evaluate(`document.documentElement.outerHTML`)
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
	data, err := p.captureRaw(opts)
	if err != nil {
		return nil, err
	}

	if opts.MaxSize <= 0 || len(data) <= opts.MaxSize {
		return data, nil
	}

	// Image too large — progressively reduce until it fits.
	// Strategy: switch to JPEG, then reduce quality, then downscale.
	qualities := []int{80, 60, 40, 20}
	scales := []float64{1.0, 0.75, 0.5, 0.25}

	for _, scale := range scales {
		for _, q := range qualities {
			shrunk, err := p.captureCompressed(opts, q, scale)
			if err != nil {
				continue
			}
			if len(shrunk) <= opts.MaxSize {
				return shrunk, nil
			}
		}
	}

	// Last resort: smallest possible
	return p.captureCompressed(opts, 10, 0.25)
}

func (p *Page) captureRaw(opts ScreenshotOptions) ([]byte, error) {
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

	result, err := p.call("Page.captureScreenshot", params)
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

func (p *Page) captureCompressed(opts ScreenshotOptions, quality int, scale float64) ([]byte, error) {
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
		vpResult, err := p.Evaluate(viewportJS)
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

	result, err := p.call("Page.captureScreenshot", params)
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
	// Get the element's bounding box
	result, err := p.call("DOM.getBoxModel", map[string]any{"nodeId": nodeID})
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

	return p.ScreenshotWithOptions(ScreenshotOptions{
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

	result, err := p.call("Page.printToPDF", params)
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

// SetViewport sets the page viewport dimensions.
func (p *Page) SetViewport(width, height int) error {
	params := map[string]any{
		"width":             width,
		"height":            height,
		"deviceScaleFactor": 1,
		"mobile":            false,
	}
	_, err := p.call("Emulation.setDeviceMetricsOverride", params)
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
// Supports #id, .class, and tag selectors against the pierce:true node list.
func matchFlatNode(nodes []flatNode, selector string) int64 {
	for _, n := range nodes {
		if n.NodeType != 1 {
			continue
		}
		attrs := attrMap(n.Attributes)
		if strings.HasPrefix(selector, "#") {
			if attrs["id"] == selector[1:] {
				return n.NodeID
			}
		} else if strings.HasPrefix(selector, ".") {
			cls := selector[1:]
			for _, c := range strings.Fields(attrs["class"]) {
				if c == cls {
					return n.NodeID
				}
			}
		} else if !strings.ContainsAny(selector, "#.[]:>+~ ") {
			if strings.EqualFold(n.NodeName, selector) {
				return n.NodeID
			}
		}
	}
	return 0
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

// SetUserAgent sets the user agent string for this page.
func (p *Page) SetUserAgent(ua string) error {
	_, _ = p.call("Network.enable", nil)
	_, err := p.call("Network.setUserAgentOverride", map[string]any{
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
	if err := wait.ForSelector(ctx, p, selector); err != nil {
		return &TimeoutError{Operation: "wait for selector", Selector: selector}
	}
	return nil
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
	result, err := p.call("Network.getCookies", nil)
	if err != nil {
		// Enable network domain and retry
		_, _ = p.call("Network.enable", nil)
		result, err = p.call("Network.getCookies", nil)
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
	_, _ = p.call("Network.enable", nil)
	_, err := p.call("Network.setCookie", c)
	return err
}

// ClearBrowserCookies removes all browser cookies for this session.
func (p *Page) ClearBrowserCookies() error {
	_, _ = p.call("Network.enable", nil)
	_, err := p.call("Network.clearBrowserCookies", nil)
	return err
}

// DeleteCookie removes one cookie by name (and optional URL/domain/path).
func (p *Page) DeleteCookie(name, url, domain, path string) error {
	_, _ = p.call("Network.enable", nil)
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
	_, err := p.call("Network.deleteCookies", params)
	return err
}

// Close closes the page/tab and cleans up resources.
func (p *Page) Close() error {
	p.cancel()
	p.conn.RemoveSessionHandlers(p.sessionID)
	return p.conn.CloseTarget(p.targetID)
}
