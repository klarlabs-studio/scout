package agent

import (
	"encoding/json"
	"strings"
	"sync"
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

	s.network.patterns = patterns
	s.network.enabled = true

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

// DisableNetworkCapture stops capturing network requests.
func (s *Session) DisableNetworkCapture() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.network == nil {
		return
	}
	for _, fn := range s.network.unsub {
		fn()
	}
	s.network.enabled = false
	s.network.unsub = nil
	s.network.observersInstalled = false
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

	if s.page != nil && allCapture != nil {
		result, err := s.page.Call("Network.getResponseBody", map[string]any{
			"requestId": reqID,
		})
		if err == nil {
			var body struct {
				Body          string `json:"body"`
				Base64Encoded bool   `json:"base64Encoded"`
			}
			if err := json.Unmarshal(result, &body); err == nil && !body.Base64Encoded {
				if len(body.Body) > defaultMaxBodySize {
					allCapture.ResponseBody = body.Body[:defaultMaxBodySize]
					allCapture.ResponseBodyTruncated = true
				} else {
					allCapture.ResponseBody = body.Body
				}
				if capture != nil {
					capture.ResponseBody = allCapture.ResponseBody
					capture.ResponseBodyTruncated = allCapture.ResponseBodyTruncated
				}
			}
		}
	}

	s.network.mu.Lock()
	if allCapture != nil {
		s.network.history = append(s.network.history, *allCapture)
		if s.network.historyLimit > 0 && len(s.network.history) > s.network.historyLimit {
			s.network.history = s.network.history[len(s.network.history)-s.network.historyLimit:]
		}
	}
	if capture != nil {
		s.network.requests = append(s.network.requests, *capture)
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
