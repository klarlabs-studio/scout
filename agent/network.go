package agent

import (
	"encoding/json"
	"strings"
	"sync"

	browse "go.klarlabs.de/scout"
)

const defaultMaxBodySize = 32 * 1024 // 32KB

// networkState tracks captured network requests for a session.
type networkState struct {
	mu                 sync.Mutex
	enabled            bool
	patterns           []string
	requests           []NetworkCapture
	history            []NetworkCapture
	pending            map[string]*NetworkCapture // requestId -> partial
	pendingAll         map[string]*NetworkCapture
	unsub              []func()
	observersInstalled bool
	historyLimit       int
}

// EnableNetworkCapture starts capturing XHR/fetch responses matching the given URL patterns.
// Empty patterns captures all requests. Patterns are matched as substrings.
func (s *Session) EnableNetworkCapture(patterns ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return err
	}

	if s.network == nil {
		s.network = &networkState{
			pending:      make(map[string]*NetworkCapture),
			pendingAll:   make(map[string]*NetworkCapture),
			historyLimit: 200,
		}
	}

	// enabled/patterns are read by the request handler under network.mu (on the
	// CDP dispatch goroutine), so write them under the same lock — not just s.mu.
	s.network.mu.Lock()
	s.network.patterns = patterns
	s.network.enabled = true
	s.network.mu.Unlock()

	if err := s.ensureNetworkObserversLocked(); err != nil {
		return err
	}

	if len(s.network.requests) == 0 && len(s.network.history) > 0 {
		for _, req := range s.network.history {
			if len(patterns) == 0 || matchesAnyPattern(req.URL, patterns) {
				fromHistory := req
				fromHistory.FromHistory = true
				s.network.requests = append(s.network.requests, fromHistory)
			}
		}
	}
	return nil
}

func (s *Session) ensureNetworkObserversLocked() error {
	if s.network == nil {
		s.network = &networkState{pending: make(map[string]*NetworkCapture), pendingAll: make(map[string]*NetworkCapture), historyLimit: 200}
	}
	if s.network.observersInstalled {
		return nil
	}
	if _, err := s.page.Call("Network.enable", nil); err != nil {
		return err
	}
	unsub1 := s.page.OnSession("Network.requestWillBeSent", func(params map[string]any) { s.onRequestWillBeSent(params) })
	unsub2 := s.page.OnSession("Network.responseReceived", func(params map[string]any) { s.onResponseReceived(params) })
	unsub3 := s.page.OnSession("Network.loadingFinished", func(params map[string]any) { s.onLoadingFinished(params) })
	s.network.unsub = append(s.network.unsub, unsub1, unsub2, unsub3)
	s.network.observersInstalled = true
	return nil
}

func (s *Session) detachNetworkObserversLocked() {
	if s.network == nil {
		return
	}
	for _, fn := range s.network.unsub {
		fn()
	}
	s.network.unsub = nil
	s.network.observersInstalled = false
}

// reattachNetworkObserversLocked rebinds the CDP network listeners onto the
// current s.page after a page swap (Navigate/OpenTab/SwitchTab). The previous
// page's session-scoped handlers are detached, then observers are installed on
// the new page if capture is enabled. Per-page requestIds from the old page are
// stale, so the pending maps are cleared.
// Must be called with s.mu held.
func (s *Session) reattachNetworkObserversLocked() error {
	if s.network == nil || !s.network.enabled {
		return nil
	}
	s.detachNetworkObserversLocked()

	s.network.mu.Lock()
	s.network.pending = make(map[string]*NetworkCapture)
	s.network.pendingAll = make(map[string]*NetworkCapture)
	s.network.mu.Unlock()

	return s.ensureNetworkObserversLocked()
}

// DisableNetworkCapture stops capturing network requests.
func (s *Session) DisableNetworkCapture() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.network == nil {
		return
	}
	s.detachNetworkObserversLocked()
	s.network.mu.Lock()
	s.network.enabled = false
	s.network.mu.Unlock()
}

// CapturedRequests returns captured network requests, optionally filtered by URL pattern.
func (s *Session) CapturedRequests(pattern string) []NetworkCapture {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.network == nil {
		return nil
	}

	s.network.mu.Lock()
	defer s.network.mu.Unlock()

	if pattern == "" {
		result := make([]NetworkCapture, len(s.network.requests))
		copy(result, s.network.requests)
		return result
	}

	var filtered []NetworkCapture
	for _, r := range s.network.requests {
		if strings.Contains(r.URL, pattern) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// IsNetworkCaptureEnabled reports whether enable_network_capture has been called
// for the current session. Used to surface a hint when network_requests is empty.
func (s *Session) IsNetworkCaptureEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.network != nil && s.network.enabled
}

// NetworkSummary aggregates captured network traffic into a single
// view designed for the "did this action succeed at the network
// layer" question. Replaces a typical three-step dance
// (enable_network_capture → act → failed_requests + console_errors).
type NetworkSummary struct {
	Total          int              `json:"total"`
	ByStatus       map[string]int   `json:"by_status"`
	Failures       []NetworkCapture `json:"failures,omitempty"`
	Pending        int              `json:"pending"`
	CaptureEnabled bool             `json:"capture_enabled"`
	Hint           string           `json:"hint,omitempty"`
}

// NetworkSummary returns a rolled-up view of the current capture
// buffer, optionally filtered by URL substring. Failures are
// every request with status >= 400.
func (s *Session) NetworkSummary(pattern string) *NetworkSummary {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := &NetworkSummary{
		ByStatus: map[string]int{},
	}
	if s.network == nil {
		out.Hint = "Network capture not enabled. Call enable_network_capture before triggering actions you want to observe."
		return out
	}
	out.CaptureEnabled = s.network.enabled

	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	for _, r := range s.network.requests {
		if pattern != "" && !strings.Contains(r.URL, pattern) {
			continue
		}
		out.Total++
		bucket := statusBucket(r.Status)
		out.ByStatus[bucket]++
		if r.Status >= 400 {
			out.Failures = append(out.Failures, r)
		}
	}
	for _, p := range s.network.pending {
		if pattern != "" && !strings.Contains(p.URL, pattern) {
			continue
		}
		out.Pending++
	}
	if !out.CaptureEnabled && out.Total == 0 {
		out.Hint = "Network capture not enabled. Call enable_network_capture before triggering actions you want to observe."
	}
	return out
}

// statusBucket groups HTTP status codes for the by_status map.
// Status 0 = pending or no response (CDP failed-load) → bucketed
// separately so callers can tell network failures from 4xx/5xx.
func statusBucket(status int) string {
	switch {
	case status == 0:
		return "0"
	case status < 200:
		return "1xx"
	case status < 300:
		return "2xx"
	case status < 400:
		return "3xx"
	case status < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

// ClearCapturedRequests clears all captured requests.
func (s *Session) ClearCapturedRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.network != nil {
		s.network.mu.Lock()
		s.network.requests = nil
		s.network.mu.Unlock()
	}
}

func matchesAnyPattern(url string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if strings.Contains(url, p) {
			return true
		}
	}
	return false
}

func (s *Session) matchesNetworkPattern(url string) bool {
	if s.network == nil || !s.network.enabled {
		return false
	}
	if len(s.network.patterns) == 0 {
		return true
	}
	for _, p := range s.network.patterns {
		if strings.Contains(url, p) {
			return true
		}
	}
	return false
}

func (s *Session) onRequestWillBeSent(params map[string]any) {
	req, _ := params["request"].(map[string]any)
	if req == nil {
		return
	}
	reqURL, _ := req["url"].(string)
	// History captures every request regardless of the active filter pattern;
	// the filter only narrows what surfaces in CapturedRequests.

	reqID, _ := params["requestId"].(string)
	method, _ := req["method"].(string)
	headers := extractStringMap(req, "headers")

	capture := &NetworkCapture{
		URL:            reqURL,
		Method:         method,
		RequestHeaders: headers,
	}

	if postData, ok := req["postData"].(string); ok && postData != "" {
		if len(postData) > defaultMaxBodySize {
			capture.RequestBody = postData[:defaultMaxBodySize]
			capture.RequestBodyTruncated = true
		} else {
			capture.RequestBody = postData
		}
	}

	s.network.mu.Lock()
	s.network.pendingAll[reqID] = capture
	if s.matchesNetworkPattern(reqURL) {
		s.network.pending[reqID] = capture
	}
	s.network.mu.Unlock()
}

func (s *Session) onResponseReceived(params map[string]any) {
	reqID, _ := params["requestId"].(string)

	s.network.mu.Lock()
	capture, ok := s.network.pending[reqID]
	allCapture, allOK := s.network.pendingAll[reqID]
	s.network.mu.Unlock()

	if !ok && !allOK {
		return
	}

	resp, _ := params["response"].(map[string]any)
	if resp != nil {
		if status, hasStatus := resp["status"].(float64); hasStatus {
			if ok {
				capture.Status = int(status)
			}
			if allOK {
				allCapture.Status = int(status)
			}
		}
		if mimeType, hasMime := resp["mimeType"].(string); hasMime {
			if ok {
				capture.MimeType = mimeType
			}
			if allOK {
				allCapture.MimeType = mimeType
			}
		}
		headers := extractStringMap(resp, "headers")
		if ok {
			capture.ResponseHeaders = headers
		}
		if allOK {
			allCapture.ResponseHeaders = headers
		}
	}
}

func (s *Session) onLoadingFinished(params map[string]any) {
	reqID, _ := params["requestId"].(string)

	s.network.mu.Lock()
	capture, ok := s.network.pending[reqID]
	allCapture, allOK := s.network.pendingAll[reqID]
	if !ok {
		capture = nil
	}
	delete(s.network.pending, reqID)
	if allOK {
		delete(s.network.pendingAll, reqID)
	}
	s.network.mu.Unlock()
	if !ok && !allOK {
		return
	}

	// Record the captures synchronously so the request count is visible to
	// callers as soon as the load finishes. The response body is fetched
	// afterwards, off this goroutine (see below), and patched in by index.
	reqIdx, histIdx := -1, -1
	s.network.mu.Lock()
	if allCapture != nil {
		s.network.history = append(s.network.history, *allCapture)
		if s.network.historyLimit > 0 && len(s.network.history) > s.network.historyLimit {
			s.network.history = s.network.history[len(s.network.history)-s.network.historyLimit:]
		}
		histIdx = len(s.network.history) - 1
	}
	if capture != nil {
		reqIdx = len(s.network.requests)
		s.network.requests = append(s.network.requests, *capture)
	}
	s.network.mu.Unlock()

	// Network.getResponseBody is a synchronous CDP call. onLoadingFinished runs
	// on the CDP readLoop goroutine, which is also what delivers that call's
	// response — so calling it inline deadlocks the connection (issue #42).
	// Fetch the body off-loop and patch the recorded captures by index.
	if allCapture != nil {
		// NB: read s.page without s.mu on purpose. This runs on the CDP dispatch
		// goroutine, and Session methods hold s.mu while waiting for events this
		// goroutine delivers — taking s.mu here would stall event delivery. The
		// pointer read is benign (fetchResponseBody nil-checks and URL-guards it).
		go s.fetchResponseBody(s.page, reqID, allCapture.URL, reqIdx, histIdx)
	}
}

// fetchResponseBody retrieves a finished request's response body via CDP and
// patches it into the captured request (and matching history entry) by index.
// Runs on its own goroutine: Network.getResponseBody must not be issued from
// the readLoop goroutine that dispatches the loadingFinished event. The URL
// guard avoids mis-patching after history trimming reuses an index.
func (s *Session) fetchResponseBody(page *browse.Page, reqID, url string, reqIdx, histIdx int) {
	if page == nil {
		return
	}
	result, err := page.Call("Network.getResponseBody", map[string]any{
		"requestId": reqID,
	})
	if err != nil {
		return
	}
	var body struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := json.Unmarshal(result, &body); err != nil || body.Base64Encoded {
		return
	}
	text := body.Body
	truncated := false
	if len(text) > defaultMaxBodySize {
		text = text[:defaultMaxBodySize]
		truncated = true
	}

	s.network.mu.Lock()
	if reqIdx >= 0 && reqIdx < len(s.network.requests) && s.network.requests[reqIdx].URL == url {
		s.network.requests[reqIdx].ResponseBody = text
		s.network.requests[reqIdx].ResponseBodyTruncated = truncated
	}
	if histIdx >= 0 && histIdx < len(s.network.history) && s.network.history[histIdx].URL == url {
		s.network.history[histIdx].ResponseBody = text
		s.network.history[histIdx].ResponseBodyTruncated = truncated
	}
	s.network.mu.Unlock()
}

func extractStringMap(m map[string]any, key string) map[string]string {
	raw, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	return result
}
