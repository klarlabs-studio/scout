package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	browse "go.klarlabs.de/scout"
)

const defaultMaxBodySize = 32 * 1024 // 32KB
const defaultMaxNetworkRecords = 200
const maxNetworkHeaderCount = 64
const maxNetworkHeaderValueBytes = 2 * 1024
const maxNetworkURLBytes = 4 * 1024

const redactedNetworkHeaderValue = "[REDACTED]"

var sensitiveNetworkHeaders = map[string]struct{}{
	"authorization":            {},
	"proxy-authorization":      {},
	"cookie":                   {},
	"set-cookie":               {},
	"x-api-key":                {},
	"x-auth-token":             {},
	"x-access-token":           {},
	"x-refresh-token":          {},
	"x-csrf-token":             {},
	"x-xsrf-token":             {},
	"x-forwarded-access-token": {},
	"api-key":                  {},
	"apikey":                   {},
}

var safeNetworkHeaderCredentialTerms = map[string]struct{}{
	"content": {},
	"www":     {},
	"proxy":   {},
}

type networkObserverMode uint8

const (
	networkObserverNone networkObserverMode = iota
	networkObserverDiagnostics
	networkObserverCapture
)

// networkState tracks captured network requests for a session.
type networkState struct {
	mu                 sync.Mutex
	enabled            bool
	captureEverEnabled bool
	captureInterrupted bool
	patterns           []string
	history            []NetworkCapture
	pending            map[string]*NetworkCapture // targetId + requestId -> partial
	pendingAll         map[string]*NetworkCapture
	unsub              []func()
	observedPages      map[string]*browse.Page
	observersInstalled bool
	observerMode       networkObserverMode
	historyLimit       int
	nextCaptureID      uint64
	droppedRequests    int
	// enablePageNetwork is an internal seam for deterministic attach-failure
	// coverage. Production sessions leave it nil and call CDP directly.
	enablePageNetwork func(context.Context, *browse.Page) error
}

// EnableNetworkCapture starts capturing XHR/fetch responses matching the given URL patterns.
// Empty patterns captures all requests. Patterns are matched as substrings.
func (s *Session) EnableNetworkCapture(patterns ...string) error {
	return s.EnableNetworkCaptureContext(context.Background(), patterns...)
}

// EnableNetworkCaptureContext starts capture while honoring ctx for lock
// queueing, page setup, and Network.enable.
func (s *Session) EnableNetworkCaptureContext(ctx context.Context, patterns ...string) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return err
	}

	if s.network == nil {
		s.network = &networkState{
			pending:      make(map[string]*NetworkCapture),
			pendingAll:   make(map[string]*NetworkCapture),
			historyLimit: 200,
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(patterns) == 0 {
		patterns = nil
	} else {
		patterns = append([]string(nil), patterns...)
	}

	// enabled/patterns are read by the request handler under network.mu (on the
	// CDP dispatch goroutine), so write them under the same lock — not just s.mu.
	s.network.mu.Lock()
	wasEnabled := s.network.enabled
	if wasEnabled && s.network.observersInstalled && !sameNetworkPatterns(s.network.patterns, patterns) {
		s.network.mu.Unlock()
		return fmt.Errorf("disable network capture before changing URL patterns")
	}
	s.network.patterns = patterns
	// Request callbacks use enabled to switch from passive failures-only
	// diagnostics into explicit capture mode.
	s.network.enabled = true
	s.network.mu.Unlock()

	if err := s.ensureNetworkObserversContextLocked(ctx); err != nil {
		s.network.mu.Lock()
		s.network.enabled = wasEnabled
		if !wasEnabled && s.network.observerMode == networkObserverCapture {
			s.network.observerMode = networkObserverDiagnostics
		}
		s.network.mu.Unlock()
		return err
	}
	if err := ctx.Err(); err != nil {
		s.network.mu.Lock()
		s.network.enabled = wasEnabled
		if !wasEnabled && s.network.observerMode == networkObserverCapture {
			s.network.observerMode = networkObserverDiagnostics
		}
		s.network.mu.Unlock()
		return err
	}
	s.network.mu.Lock()
	s.network.captureEverEnabled = true
	s.network.mu.Unlock()

	if !wasEnabled {
		s.network.mu.Lock()
		s.network.droppedRequests = 0
		s.network.mu.Unlock()
	}
	return nil
}

func sameNetworkPatterns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Session) ensureNetworkObserversLocked() error {
	return s.ensureNetworkObserversContextLocked(context.Background())
}

func (s *Session) ensureNetworkObserversContextLocked(ctx context.Context) error {
	if s.network == nil {
		s.network = &networkState{pending: make(map[string]*NetworkCapture), pendingAll: make(map[string]*NetworkCapture), historyLimit: 200}
	}
	if s.page == nil {
		s.markNetworkObserverUnavailableLocked()
		s.network.mu.Lock()
		s.network.pending = make(map[string]*NetworkCapture)
		s.network.pendingAll = make(map[string]*NetworkCapture)
		s.network.mu.Unlock()
		return fmt.Errorf("cannot attach network observers without an active page")
	}
	s.network.mu.Lock()
	enabled := s.network.enabled
	if s.network.observedPages == nil {
		s.network.observedPages = make(map[string]*browse.Page)
	}
	if _, installed := s.network.observedPages[s.page.TargetID()]; installed {
		if enabled {
			s.network.observerMode = networkObserverCapture
		} else {
			s.network.observerMode = networkObserverDiagnostics
		}
		s.network.observersInstalled = true
		s.network.mu.Unlock()
		return nil
	}
	s.network.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	enablePageNetwork := func(ctx context.Context, page *browse.Page) error {
		_, err := page.CallContext(ctx, "Network.enable", nil)
		return err
	}
	if s.network.enablePageNetwork != nil {
		enablePageNetwork = s.network.enablePageNetwork
	}
	if err := enablePageNetwork(ctx, s.page); err != nil {
		s.markNetworkObserverUnavailableLocked()
		return err
	}
	page := s.page
	targetID := page.TargetID()
	unsub1 := page.OnSession("Network.requestWillBeSent", func(params map[string]any) { s.onRequestWillBeSentForTarget(targetID, params) })
	unsub2 := page.OnSession("Network.responseReceived", func(params map[string]any) { s.onResponseReceivedForTarget(targetID, params) })
	unsub3 := page.OnSession("Network.loadingFinished", func(params map[string]any) { s.onLoadingFinishedForTarget(targetID, page, params) })
	unsub4 := page.OnSession("Network.loadingFailed", func(params map[string]any) { s.onLoadingFailedForTarget(targetID, params) })
	s.network.mu.Lock()
	s.network.unsub = append(s.network.unsub, unsub1, unsub2, unsub3, unsub4)
	s.network.observedPages[targetID] = page
	if s.network.enabled {
		s.network.observerMode = networkObserverCapture
	} else {
		s.network.observerMode = networkObserverDiagnostics
	}
	s.network.observersInstalled = true
	s.network.mu.Unlock()
	return nil
}

func (s *Session) detachNetworkObserversLocked() {
	if s.network == nil {
		return
	}
	s.network.mu.Lock()
	for _, fn := range s.network.unsub {
		fn()
	}
	s.network.unsub = nil
	s.network.observedPages = make(map[string]*browse.Page)
	s.network.observersInstalled = false
	s.network.observerMode = networkObserverNone
	s.network.mu.Unlock()
}

// reattachNetworkObserversLocked ensures the current page has a network
// listener. Existing tab listeners remain active so background tabs continue
// to be observed.
// Must be called with s.mu held.
func (s *Session) reattachNetworkObserversLocked() error {
	if s.network == nil {
		return nil
	}
	s.network.mu.Lock()
	shouldObserve := s.network.enabled || s.network.observerMode == networkObserverDiagnostics
	s.network.mu.Unlock()
	if !shouldObserve {
		return nil
	}
	return s.ensureNetworkObserversLocked()
}

func networkRequestKey(targetID, reqID string) string {
	if targetID == "" {
		return reqID
	}
	return targetID + "\x00" + reqID
}

// markNetworkObserverUnavailableLocked records a failed best-effort attach
// without discarding whether explicit capture or passive diagnostics was
// requested. The next page-backed read can retry installation.
func (s *Session) markNetworkObserverUnavailableLocked() {
	if s.network == nil {
		return
	}
	s.network.mu.Lock()
	// Other tabs may still have active listeners. Keep their unsubscribe
	// callbacks so DisableNetworkCapture and session close can detach them even
	// when installation on the current page failed.
	s.network.observersInstalled = false
	if s.network.enabled {
		s.network.observerMode = networkObserverCapture
		// The requested capture interval crossed a period without an attached
		// page observer, so a later successful attach cannot make coverage whole.
		s.network.captureInterrupted = true
	} else {
		s.network.observerMode = networkObserverDiagnostics
	}
	s.network.mu.Unlock()
}

// DisableNetworkCapture stops capturing network requests.
func (s *Session) DisableNetworkCapture() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.network == nil {
		return
	}
	s.network.mu.Lock()
	if s.network.enabled {
		pending := len(s.network.pending)
		s.network.droppedRequests += pending
		if pending > 0 {
			s.network.captureInterrupted = true
		}
	}
	s.network.enabled = false
	mode := s.network.observerMode
	s.network.mu.Unlock()
	if mode == networkObserverCapture {
		s.network.mu.Lock()
		for _, fn := range s.network.unsub {
			fn()
		}
		s.network.unsub = nil
		s.network.observedPages = make(map[string]*browse.Page)
		s.network.observersInstalled = false
		s.network.observerMode = networkObserverNone
		s.network.mu.Unlock()
	}
}

// CapturedRequests returns captured network requests, optionally filtered by URL pattern.
func (s *Session) CapturedRequests(pattern string) []NetworkCapture {
	result, _ := s.CapturedRequestsContext(context.Background(), pattern)
	return result
}

// CapturedRequestsContext returns captured requests while honoring ctx for
// session-lock queueing.
func (s *Session) CapturedRequestsContext(ctx context.Context, pattern string) ([]NetworkCapture, error) {
	requests, _, _, err := s.CapturedRequestsSnapshotContext(ctx, pattern)
	return requests, err
}

// CapturedRequestsSnapshotContext returns the retained request window and its
// capture metadata from one session/network snapshot.
func (s *Session) CapturedRequestsSnapshotContext(ctx context.Context, pattern string) ([]NetworkCapture, bool, int, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, false, 0, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, false, 0, err
	}

	if s.network == nil {
		if err := ctx.Err(); err != nil {
			return nil, false, 0, err
		}
		return nil, false, 0, nil
	}

	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	enabled, dropped := s.network.enabled, s.network.droppedRequests

	if pattern == "" {
		result := make([]NetworkCapture, 0, len(s.network.history))
		for _, request := range s.network.history {
			if request.Status >= 100 {
				result = append(result, request)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, false, 0, err
		}
		return result, enabled, dropped, nil
	}

	var filtered []NetworkCapture
	for _, r := range s.network.history {
		if r.Status >= 100 && strings.Contains(r.URL, pattern) {
			filtered = append(filtered, r)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, 0, err
	}
	return filtered, enabled, dropped, nil
}

// IsNetworkCaptureEnabled reports whether enable_network_capture has been called
// for the current session. Used to surface a hint when network_requests is empty.
func (s *Session) IsNetworkCaptureEnabled() bool {
	enabled, _ := s.IsNetworkCaptureEnabledContext(context.Background())
	return enabled
}

// IsNetworkCaptureEnabledContext reports capture state under ctx's deadline.
func (s *Session) IsNetworkCaptureEnabledContext(ctx context.Context) (bool, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	enabled := s.network != nil && s.network.enabled
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return enabled, nil
}

// NetworkSummary aggregates captured network traffic into a single
// view designed for the "did this action succeed at the network
// layer" question. Replaces a typical three-step dance
// (enable_network_capture → act → failed_requests + console_errors).
type NetworkSummary struct {
	ContentTrust           string           `json:"content_trust,omitempty"`
	Total                  int              `json:"total"`
	DroppedTotal           int              `json:"dropped_total,omitempty"`
	ByStatus               map[string]int   `json:"by_status"`
	Failures               []NetworkCapture `json:"failures,omitempty"`
	FailureTotal           int              `json:"failure_total"`
	FailuresTruncated      bool             `json:"failures_truncated"`
	Pending                int              `json:"pending"`
	CaptureMode            string           `json:"capture_mode"`
	CaptureEnabled         bool             `json:"capture_enabled"`
	CaptureEverEnabled     bool             `json:"capture_ever_enabled"`
	CaptureInterrupted     bool             `json:"capture_interrupted"`
	URLPatternFilterActive bool             `json:"url_pattern_filter_active"`
	HistoryLimit           int              `json:"history_limit"`
	CoverageScope          string           `json:"coverage_scope"`
	Coverage               string           `json:"coverage"`
	Hint                   string           `json:"hint,omitempty"`
}

func networkCaptureMode(state *networkState) string {
	if state == nil {
		return "not_started"
	}
	if !state.observersInstalled && (state.enabled || state.observerMode != networkObserverNone) {
		return "unavailable"
	}
	if state.enabled {
		return "full_capture"
	}
	if state.observerMode == networkObserverDiagnostics {
		return "diagnostics_only"
	}
	if state.captureEverEnabled {
		return "disabled"
	}
	return "not_started"
}

// NetworkSummary returns a rolled-up view of the current capture
// buffer, optionally filtered by URL substring. Failures are
// every request with status >= 400.
func (s *Session) NetworkSummary(pattern string) *NetworkSummary {
	out, _ := s.NetworkSummaryContext(context.Background(), pattern)
	return out
}

// NetworkSummaryContext returns capture totals under ctx's deadline.
func (s *Session) NetworkSummaryContext(ctx context.Context, pattern string) (*NetworkSummary, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}

	out := &NetworkSummary{
		ContentTrust: ContentTrustUntrustedPage,
		ByStatus:     map[string]int{},
	}
	if s.network == nil {
		out.CaptureMode = networkCaptureMode(nil)
		out.CoverageScope = "session_network_capture_window"
		out.Coverage = "not_enabled"
		out.Hint = "Network capture not enabled. Call enable_network_capture before triggering actions you want to observe."
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}
	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	out.CaptureMode = networkCaptureMode(s.network)
	out.CaptureEnabled = s.network.enabled
	out.CaptureEverEnabled = s.network.captureEverEnabled
	out.CaptureInterrupted = s.network.captureInterrupted
	out.URLPatternFilterActive = len(s.network.patterns) > 0
	out.HistoryLimit = networkRecordLimit(s.network)
	out.CoverageScope = "session_network_capture_window"
	out.DroppedTotal = s.network.droppedRequests
	switch {
	case !s.network.observersInstalled && (s.network.enabled || s.network.observerMode != networkObserverNone):
		out.Coverage = "unavailable"
	case out.CaptureInterrupted || out.DroppedTotal > 0 || len(s.network.pending) > 0:
		out.Coverage = "known_gaps"
	case !out.CaptureEverEnabled && out.CaptureMode == "diagnostics_only":
		out.Coverage = "diagnostics_only"
	case !out.CaptureEverEnabled:
		out.Coverage = "not_enabled"
	case !out.CaptureEnabled:
		out.Coverage = "disabled_at_read"
	default:
		out.Coverage = "active_no_known_loss"
	}
	for _, r := range s.network.history {
		if r.Status < 100 || (pattern != "" && !strings.Contains(r.URL, pattern)) {
			continue
		}
		out.Total++
		bucket := statusBucket(r.Status)
		out.ByStatus[bucket]++
		if r.Status >= 400 {
			out.FailureTotal++
			if len(out.Failures) < 50 {
				out.Failures = append(out.Failures, r)
			}
		}
	}
	out.FailuresTruncated = out.FailureTotal > len(out.Failures)
	for _, p := range s.network.pending {
		if pattern != "" && !strings.Contains(p.URL, pattern) {
			continue
		}
		out.Pending++
	}
	if !out.CaptureEnabled && out.Total == 0 {
		out.Hint = "Network capture not enabled. Call enable_network_capture before triggering actions you want to observe."
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
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
		s.network.history = nil
		pending := 0
		if s.network.enabled {
			pending = len(s.network.pending)
		}
		s.network.droppedRequests = pending
		s.network.captureInterrupted = pending > 0
		s.network.pending = make(map[string]*NetworkCapture)
		s.network.pendingAll = make(map[string]*NetworkCapture)
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

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) matchesNetworkPattern(url string) bool {
	if s.network == nil {
		return false
	}
	return s.network.enabled && (len(s.network.patterns) == 0 || matchesAnyPattern(url, s.network.patterns))
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) onRequestWillBeSent(params map[string]any) {
	s.onRequestWillBeSentForTarget("", params)
}

func (s *Session) onRequestWillBeSentForTarget(targetID string, params map[string]any) {
	reqID, _ := params["requestId"].(string)
	if redirectResponse, ok := params["redirectResponse"].(map[string]any); ok {
		s.finishNetworkRedirectForTarget(targetID, reqID, redirectResponse)
	}
	req, _ := params["request"].(map[string]any)
	if req == nil {
		return
	}
	reqURL, _ := req["url"].(string)
	reqURL = truncateNetworkField(reqURL, maxNetworkURLBytes)
	requestKey := networkRequestKey(targetID, reqID)
	method, _ := req["method"].(string)

	s.network.mu.Lock()
	explicitCaptureAtStart := s.network.enabled
	diagnosticsAtStart := !explicitCaptureAtStart && s.network.observerMode == networkObserverDiagnostics
	if !explicitCaptureAtStart && !diagnosticsAtStart {
		s.network.mu.Unlock()
		return
	}
	if s.network.enabled && !matchesAnyPattern(reqURL, s.network.patterns) {
		s.network.mu.Unlock()
		return
	}
	if len(s.network.pendingAll) >= networkRecordLimit(s.network) {
		s.network.droppedRequests++
		s.network.mu.Unlock()
		return
	}
	if s.network.nextCaptureID == ^uint64(0) {
		s.network.nextCaptureID = 0
	}
	s.network.nextCaptureID++
	captureID := s.network.nextCaptureID
	enabled := s.network.enabled
	s.network.mu.Unlock()

	headers := extractStringMap(req, "headers")

	capture := &NetworkCapture{
		TargetID:               targetID,
		ObservedAtUnixMS:       time.Now().UTC().UnixMilli(),
		URL:                    reqURL,
		ContentTrust:           ContentTrustUntrustedPage,
		Method:                 truncateNetworkField(method, 32),
		RequestHeaders:         headers,
		captureID:              captureID,
		explicitCaptureAtStart: explicitCaptureAtStart,
		diagnosticsAtStart:     diagnosticsAtStart,
	}

	if explicitCaptureAtStart {
		if postData, ok := req["postData"].(string); ok && postData != "" {
			if len(postData) > defaultMaxBodySize {
				capture.RequestBody = truncateNetworkField(postData, defaultMaxBodySize)
				capture.RequestBodyTruncated = true
			} else {
				capture.RequestBody = postData
			}
		}
	}

	s.network.mu.Lock()
	if s.network.enabled != enabled || (!s.network.enabled && s.network.observerMode != networkObserverDiagnostics) {
		s.network.mu.Unlock()
		return
	}
	if len(s.network.pendingAll) >= networkRecordLimit(s.network) {
		if s.network.enabled {
			s.network.droppedRequests++
		}
		s.network.mu.Unlock()
		return
	}
	s.network.pendingAll[requestKey] = capture
	s.network.pending[requestKey] = capture
	s.network.mu.Unlock()
}

// finishNetworkRedirectForTarget closes the previous hop in a CDP redirect
// chain. Chromium reuses the request ID for the next URL and includes the
// completed hop as redirectResponse on the following requestWillBeSent event.
// Finalize it before installing the next hop so URL, status, and headers stay
// paired with the request that produced them.
func (s *Session) finishNetworkRedirectForTarget(targetID, reqID string, response map[string]any) {
	if reqID == "" || response == nil || s.network == nil {
		return
	}
	requestKey := networkRequestKey(targetID, reqID)
	status, hasStatus := response["status"].(float64)
	mimeType, hasMimeType := response["mimeType"].(string)
	headers := extractStringMap(response, "headers")
	finishedAt := time.Now().UTC().UnixMilli()

	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	capture, captureOK := s.network.pending[requestKey]
	allCapture, allOK := s.network.pendingAll[requestKey]
	if !captureOK && !allOK {
		return
	}
	if hasStatus {
		if captureOK {
			capture.Status = int(status)
		}
		if allOK {
			allCapture.Status = int(status)
		}
	}
	if hasMimeType {
		if captureOK {
			capture.MimeType = truncateNetworkField(mimeType, 128)
		}
		if allOK {
			allCapture.MimeType = truncateNetworkField(mimeType, 128)
		}
	}
	if captureOK {
		capture.ResponseHeaders = headers
		capture.FinishedAtUnixMS = finishedAt
	}
	if allOK {
		allCapture.ResponseHeaders = headers
		allCapture.FinishedAtUnixMS = finishedAt
	}
	delete(s.network.pending, requestKey)
	delete(s.network.pendingAll, requestKey)

	if allOK && s.network.enabled && allCapture.explicitCaptureAtStart {
		completed := *allCapture
		if captureOK {
			completed = *capture
		}
		s.network.history = append(s.network.history, completed)
		trimNetworkRecordsLocked(s.network, &s.network.history, true)
	} else if allOK && allCapture.diagnosticsAtStart && allCapture.Status >= 400 {
		s.network.history = append(s.network.history, *allCapture)
		trimNetworkRecordsLocked(s.network, &s.network.history, false)
	}
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) onResponseReceived(params map[string]any) {
	s.onResponseReceivedForTarget("", params)
}

func (s *Session) onResponseReceivedForTarget(targetID string, params map[string]any) {
	reqID, _ := params["requestId"].(string)
	requestKey := networkRequestKey(targetID, reqID)
	resp, _ := params["response"].(map[string]any)
	if resp == nil {
		return
	}
	status, hasStatus := resp["status"].(float64)
	mimeType, hasMimeType := resp["mimeType"].(string)
	headers := extractStringMap(resp, "headers")

	s.network.mu.Lock()
	defer s.network.mu.Unlock()
	capture, ok := s.network.pending[requestKey]
	allCapture, allOK := s.network.pendingAll[requestKey]
	if !ok && !allOK {
		return
	}
	if hasStatus {
		if ok {
			capture.Status = int(status)
		}
		if allOK {
			allCapture.Status = int(status)
		}
	}
	if hasMimeType {
		if ok {
			capture.MimeType = truncateNetworkField(mimeType, 128)
		}
		if allOK {
			allCapture.MimeType = truncateNetworkField(mimeType, 128)
		}
	}
	if ok {
		capture.ResponseHeaders = headers
	}
	if allOK {
		allCapture.ResponseHeaders = headers
	}
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) onLoadingFinished(params map[string]any) {
	s.onLoadingFinishedForTarget("", s.page, params)
}

func (s *Session) onLoadingFinishedForTarget(targetID string, page *browse.Page, params map[string]any) {
	reqID, _ := params["requestId"].(string)
	requestKey := networkRequestKey(targetID, reqID)

	s.network.mu.Lock()
	capture, ok := s.network.pending[requestKey]
	allCapture, allOK := s.network.pendingAll[requestKey]
	finishedAt := time.Now().UTC().UnixMilli()
	if capture != nil {
		capture.FinishedAtUnixMS = finishedAt
	}
	if allCapture != nil {
		allCapture.FinishedAtUnixMS = finishedAt
	}
	captureEnabled := s.network.enabled && allCapture != nil && allCapture.explicitCaptureAtStart
	diagnosticEnabled := allCapture != nil && allCapture.diagnosticsAtStart
	delete(s.network.pending, requestKey)
	if allOK {
		delete(s.network.pendingAll, requestKey)
	}
	s.network.mu.Unlock()
	if !ok && !allOK {
		return
	}

	// Record captures synchronously so the request count is visible as soon as
	// the load finishes. Response bodies are patched later by stable capture ID.
	s.network.mu.Lock()
	if allCapture != nil {
		if captureEnabled {
			s.network.history = append(s.network.history, *allCapture)
			trimNetworkRecordsLocked(s.network, &s.network.history, true)
			if capture != nil {
				for i := range s.network.history {
					if s.network.history[i].captureID == allCapture.captureID {
						s.network.history[i] = *capture
						break
					}
				}
			}
		} else if diagnosticEnabled {
			if capture != nil && allCapture.Status >= 400 {
				s.network.history = append(s.network.history, *allCapture)
				trimNetworkRecordsLocked(s.network, &s.network.history, false)
			}
		}
	}
	s.network.mu.Unlock()

	// Network.getResponseBody is a synchronous CDP call. onLoadingFinished runs
	// on the CDP readLoop goroutine, which is also what delivers that call's
	// response — so calling it inline deadlocks the connection (issue #42).
	// Fetch the body off-loop and patch the recorded captures by index.
	if allCapture != nil && captureEnabled && capture != nil {
		// Keep the page handle captured when this listener was registered. A
		// later tab switch must not direct this request ID to another CDP target.
		go s.fetchResponseBody(page, reqID, allCapture.captureID)
	}
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) onLoadingFailed(params map[string]any) {
	s.onLoadingFailedForTarget("", params)
}

func (s *Session) onLoadingFailedForTarget(targetID string, params map[string]any) {
	reqID, _ := params["requestId"].(string)
	requestKey := networkRequestKey(targetID, reqID)
	s.network.mu.Lock()
	allCapture, observed := s.network.pendingAll[requestKey]
	finishedAt := time.Now().UTC().UnixMilli()
	if allCapture != nil {
		allCapture.FinishedAtUnixMS = finishedAt
	}
	captureEnabled := s.network.enabled && allCapture != nil && allCapture.explicitCaptureAtStart
	diagnosticEnabled := allCapture != nil && allCapture.diagnosticsAtStart
	delete(s.network.pending, requestKey)
	delete(s.network.pendingAll, requestKey)
	if allCapture != nil {
		allCapture.Status = 0
	}
	if observed && (captureEnabled || diagnosticEnabled) && allCapture != nil {
		s.network.history = append(s.network.history, *allCapture)
		if captureEnabled {
			trimNetworkRecordsLocked(s.network, &s.network.history, true)
		} else {
			trimNetworkRecordsLocked(s.network, &s.network.history, false)
		}
	}
	s.network.mu.Unlock()
}

// fetchResponseBody retrieves a finished request's response body via CDP and
// patches it into the retained records with the matching capture ID.
// Runs on its own goroutine: Network.getResponseBody must not be issued from
// the readLoop goroutine that dispatches the loadingFinished event.
func (s *Session) fetchResponseBody(page *browse.Page, reqID string, captureID uint64) {
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
		text = truncateNetworkField(text, defaultMaxBodySize)
		truncated = true
	}

	s.network.mu.Lock()
	for i := range s.network.history {
		if s.network.history[i].captureID == captureID {
			s.network.history[i].ResponseBody = text
			s.network.history[i].ResponseBodyTruncated = truncated
			break
		}
	}
	s.network.mu.Unlock()
}

func extractStringMap(m map[string]any, key string) map[string]string {
	raw, ok := m[key].(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(raw))
	count := 0
	for k, v := range raw {
		if s, ok := v.(string); ok {
			if count >= maxNetworkHeaderCount {
				break
			}
			name := truncateNetworkField(k, 256)
			value := truncateNetworkField(s, maxNetworkHeaderValueBytes)
			if isSensitiveNetworkHeader(name) {
				value = redactedNetworkHeaderValue
			}
			result[name] = value
			count++
		}
	}
	return result
}

func isSensitiveNetworkHeader(name string) bool {
	name = strings.TrimSpace(name)
	// HTTP field names are ASCII tokens. Split common camelCase and acronym
	// boundaries before lowercasing so names such as XAuthToken and XAPIKey
	// receive the same treatment as their hyphenated equivalents.
	var separated strings.Builder
	separated.Grow(len(name) + 8)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if i > 0 && c >= 'A' && c <= 'Z' {
			prev := name[i-1]
			nextIsLower := i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z'
			prevIsLowerOrDigit := prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9'
			prevIsUpper := prev >= 'A' && prev <= 'Z'
			if prevIsLowerOrDigit || (prevIsUpper && nextIsLower) {
				separated.WriteByte('-')
			}
		}
		separated.WriteByte(c)
	}
	normalized := strings.ToLower(separated.String())
	if _, ok := sensitiveNetworkHeaders[normalized]; ok {
		return true
	}
	// Header names are tokenized on common separators after camel-case splitting.
	// Treat auth/token/key/credential-bearing names as sensitive even when an
	// application adds a product-specific prefix or suffix.
	tokens := strings.FieldsFunc(normalized, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for _, token := range tokens {
		if _, safe := safeNetworkHeaderCredentialTerms[token]; safe {
			continue
		}
		if token == "auth" || token == "authorization" || token == "token" ||
			token == "apikey" || token == "key" || token == "secret" || token == "credential" ||
			token == "credentials" || token == "cookie" || token == "session" ||
			token == "csrf" || token == "xsrf" || token == "jwt" {
			return true
		}
	}
	return false
}

func networkRecordLimit(state *networkState) int {
	if state != nil && state.historyLimit > 0 {
		return state.historyLimit
	}
	return defaultMaxNetworkRecords
}

func trimNetworkRecordsLocked(state *networkState, records *[]NetworkCapture, countDropped bool) {
	limit := networkRecordLimit(state)
	if len(*records) > limit {
		if countDropped {
			state.droppedRequests += len(*records) - limit
		}
		*records = (*records)[len(*records)-limit:]
	}
}

func truncateNetworkField(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && value[end]&0xc0 == 0x80 {
		end--
	}
	return value[:end]
}
