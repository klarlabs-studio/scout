package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	browse "go.klarlabs.de/scout"
)

func TestEnsureNetworkObserversMarksFailedCDPEnableUnavailable(t *testing.T) {
	s := &Session{page: &browse.Page{}}
	s.network = &networkState{
		enabled:            true,
		observerMode:       networkObserverCapture,
		captureEverEnabled: true,
		pending:            make(map[string]*NetworkCapture),
		pendingAll:         make(map[string]*NetworkCapture),
		enablePageNetwork: func(context.Context, *browse.Page) error {
			return errors.New("injected Network.enable failure")
		},
	}
	err := s.ensureNetworkObserversContextLocked(context.Background())
	if err == nil {
		t.Fatal("expected observer attach error")
	}
	if got := networkCaptureMode(s.network); got != "unavailable" {
		t.Fatalf("capture mode = %q, want unavailable", got)
	}
	if !s.network.captureInterrupted {
		t.Fatal("failed explicit capture attach did not mark capture interrupted")
	}
}

func TestIntegrationNetworkObserverAttachFailureCoverageAndRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	requests := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/api" {
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>network observer</title><script>fetch('/api')</script>`)
	}))
	defer srv.Close()
	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.EnableNetworkCapture(); err != nil {
		t.Fatalf("EnableNetworkCapture before navigation: %v", err)
	}
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	deadline := time.After(5 * time.Second)
	seenDocument, seenAPI := false, false
	for !seenDocument || !seenAPI {
		select {
		case path := <-requests:
			seenDocument = seenDocument || path == "/"
			seenAPI = seenAPI || path == "/api"
		case <-deadline:
			t.Fatalf("fixture did not receive document and API requests: document=%v api=%v", seenDocument, seenAPI)
		}
	}
	var initial *NetworkSummary
	captureDeadline := time.After(5 * time.Second)
	for {
		initial = s.NetworkSummary("")
		if initial.Total >= 2 && initial.Pending == 0 {
			break
		}
		select {
		case <-captureDeadline:
			t.Fatalf("initial navigation capture incomplete: %+v", initial)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if initial.Coverage != "active_no_known_loss" {
		t.Fatalf("initial navigation coverage is not complete: %+v", initial)
	}

	s.mu.Lock()
	s.network.mu.Lock()
	s.network.enablePageNetwork = func(context.Context, *browse.Page) error {
		s.network.enablePageNetwork = nil
		return fmt.Errorf("injected Network.enable failure")
	}
	s.network.mu.Unlock()
	s.mu.Unlock()
	if _, err := s.OpenTab("failure", srv.URL); err != nil {
		t.Fatalf("OpenTab after transient observer failure: %v", err)
	}
	failed := s.NetworkSummary("")
	if failed.CaptureMode != "full_capture" || !failed.CaptureInterrupted || failed.Coverage != "known_gaps" {
		t.Fatalf("transient attach gap was not preserved after recovery: %+v", failed)
	}
	if _, err := s.OpenTab("recovered", srv.URL); err != nil {
		t.Fatalf("OpenTab after observer failure recovered: %v", err)
	}
	recovered := s.NetworkSummary("")
	if recovered.CaptureMode != "full_capture" || recovered.Coverage != "known_gaps" {
		t.Fatalf("observer did not recover with honest historical coverage: %+v", recovered)
	}
}

func TestExtractStringMap(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want map[string]string
	}{
		{
			name: "valid headers",
			m:    map[string]any{"headers": map[string]any{"Content-Type": "application/json", "Accept": "text/html"}},
			key:  "headers",
			want: map[string]string{"Content-Type": "application/json", "Accept": "text/html"},
		},
		{
			name: "missing key",
			m:    map[string]any{"other": "value"},
			key:  "headers",
			want: nil,
		},
		{
			name: "non-map value",
			m:    map[string]any{"headers": "not a map"},
			key:  "headers",
			want: nil,
		},
		{
			name: "mixed value types",
			m:    map[string]any{"headers": map[string]any{"good": "value", "bad": 123, "nil": nil}},
			key:  "headers",
			want: map[string]string{"good": "value"},
		},
		{
			name: "empty inner map",
			m:    map[string]any{"headers": map[string]any{}},
			key:  "headers",
			want: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStringMap(tt.m, tt.key)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("length mismatch: got %d, want %d", len(got), len(tt.want))
				return
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestExtractStringMapRedactsSensitiveHeaders(t *testing.T) {
	input := map[string]any{"headers": map[string]any{
		"Authorization":            "Bearer request-secret",
		"proxy-authorization":      "Basic proxy-secret",
		"Cookie":                   "session=request-cookie",
		"set-cookie":               "session=response-cookie; HttpOnly",
		"X-API-Key":                "api-secret",
		"XAPIKey":                  "camel-api-secret",
		"X-Auth-Token":             "auth-secret",
		"XAuthToken":               "camel-auth-secret",
		"X-Access-Token":           "access-secret",
		"X-Refresh-Token":          "refresh-secret",
		"X-CSRF-Token":             "csrf-secret",
		"X-XSRF-Token":             "xsrf-secret",
		"X-Forwarded-Access-Token": "forwarded-secret",
		"Content-Type":             "application/json",
	}}

	got := extractStringMap(input, "headers")
	for name, value := range got {
		if name == "Content-Type" {
			if value != "application/json" {
				t.Fatalf("non-sensitive header changed: %q", value)
			}
			continue
		}
		if value != redactedNetworkHeaderValue {
			t.Errorf("%s = %q, want redacted marker", name, value)
		}
	}
	for _, secret := range []string{
		"request-secret", "proxy-secret", "request-cookie", "response-cookie", "api-secret", "camel-api-secret",
		"auth-secret", "camel-auth-secret", "access-secret", "refresh-secret", "csrf-secret", "xsrf-secret", "forwarded-secret",
	} {
		for name, value := range got {
			if strings.Contains(value, secret) {
				t.Errorf("sensitive value from %s survived extraction", name)
			}
		}
	}
}

func TestIsSensitiveNetworkHeaderRedactsCredentialVariants(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"X-Auth-Token-Alt", true},
		{"XAuthToken", true},
		{"XAPIKey", true},
		{"X-CSRFToken", true},
		{"Api-Key", true},
		{"x_vendor_session_secret", true},
		{"X-Product-JWT", true},
		{"Content-Type", false},
		{"WWW-Authenticate", false},
		{"Proxy-Authenticate", false},
		{"ProxyAuthenticate", false},
		{"X-Content-Security-Policy", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isSensitiveNetworkHeader(test.name); got != test.want {
				t.Fatalf("isSensitiveNetworkHeader(%q) = %t, want %t", test.name, got, test.want)
			}
		})
	}
}

func TestMatchesNetworkPattern(t *testing.T) {
	tests := []struct {
		name    string
		network *networkState
		url     string
		want    bool
	}{
		{
			name:    "nil network",
			network: nil,
			url:     "https://example.com",
			want:    false,
		},
		{
			name:    "disabled",
			network: &networkState{enabled: false},
			url:     "https://example.com",
			want:    false,
		},
		{
			name:    "enabled no patterns matches all",
			network: &networkState{enabled: true, patterns: nil},
			url:     "https://example.com/api",
			want:    true,
		},
		{
			name:    "enabled empty patterns matches all",
			network: &networkState{enabled: true, patterns: []string{}},
			url:     "https://example.com/api",
			want:    true,
		},
		{
			name:    "pattern match",
			network: &networkState{enabled: true, patterns: []string{"/api/"}},
			url:     "https://example.com/api/users",
			want:    true,
		},
		{
			name:    "pattern no match",
			network: &networkState{enabled: true, patterns: []string{"/api/"}},
			url:     "https://example.com/static/img.png",
			want:    false,
		},
		{
			name:    "multiple patterns one match",
			network: &networkState{enabled: true, patterns: []string{"/api/", "/graphql"}},
			url:     "https://example.com/graphql",
			want:    true,
		},
		{
			name:    "multiple patterns no match",
			network: &networkState{enabled: true, patterns: []string{"/api/", "/graphql"}},
			url:     "https://example.com/static/css",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{network: tt.network}
			got := s.matchesNetworkPattern(tt.url)
			if got != tt.want {
				t.Errorf("matchesNetworkPattern(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestDefaultMaxBodySize(t *testing.T) {
	if defaultMaxBodySize != 32*1024 {
		t.Errorf("defaultMaxBodySize: got %d, want %d", defaultMaxBodySize, 32*1024)
	}
}

func TestRequestCaptureFiltersBeforeRetainingBody(t *testing.T) {
	s := &Session{network: &networkState{
		enabled:      true,
		patterns:     []string{"/api/"},
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	s.onRequestWillBeSent(map[string]any{
		"requestId": "miss",
		"request": map[string]any{
			"url":      "https://example.com/static",
			"method":   "POST",
			"postData": "must-not-be-retained",
		},
	})
	if len(s.network.pendingAll) != 0 {
		t.Fatalf("nonmatching request retained: %d pending requests", len(s.network.pendingAll))
	}

	s.onRequestWillBeSent(map[string]any{
		"requestId": "match",
		"request": map[string]any{
			"url":      "https://example.com/api/submit",
			"method":   "POST",
			"postData": "sensitive-value",
		},
	})
	if got := s.network.pendingAll["match"]; got == nil || got.RequestBody != "sensitive-value" {
		t.Fatalf("matching explicit capture did not retain request body: %#v", got)
	}
}

func TestNetworkRedirectFinalizesPreviousHopBeforeReusingRequestID(t *testing.T) {
	s := &Session{network: &networkState{
		enabled:      true,
		observerMode: networkObserverCapture,
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	targetID := "target-redirect"
	requestID := "request-redirect"
	s.onRequestWillBeSentForTarget(targetID, map[string]any{
		"requestId": requestID,
		"request":   map[string]any{"url": "https://example.test/start", "method": "GET"},
	})
	s.onRequestWillBeSentForTarget(targetID, map[string]any{
		"requestId": requestID,
		"redirectResponse": map[string]any{
			"status": float64(302), "mimeType": "text/html",
			"headers": map[string]any{"Location": "https://example.test/landed"},
		},
		"request": map[string]any{"url": "https://example.test/landed", "method": "GET"},
	})

	if len(s.network.history) != 1 {
		t.Fatalf("redirect hop history count = %d, want 1", len(s.network.history))
	}
	first := s.network.history[0]
	if first.URL != "https://example.test/start" || first.Status != 302 || first.MimeType != "text/html" || first.ResponseHeaders["Location"] != "https://example.test/landed" {
		t.Fatalf("first redirect hop was not finalized with its own response: %+v", first)
	}
	key := networkRequestKey(targetID, requestID)
	if got := s.network.pendingAll[key]; got == nil || got.URL != "https://example.test/landed" {
		t.Fatalf("redirect destination was not installed as the pending hop: %+v", got)
	}
	s.onResponseReceivedForTarget(targetID, map[string]any{
		"requestId": requestID,
		"response": map[string]any{
			"status": float64(200), "mimeType": "application/json",
			"headers": map[string]any{"Content-Type": "application/json"},
		},
	})
	s.onLoadingFinishedForTarget(targetID, nil, map[string]any{"requestId": requestID})
	if len(s.network.history) != 2 {
		t.Fatalf("completed redirect chain history count = %d, want 2", len(s.network.history))
	}
	last := s.network.history[1]
	if last.URL != "https://example.test/landed" || last.Status != 200 || last.MimeType != "application/json" {
		t.Fatalf("redirect destination was paired with the wrong response: %+v", last)
	}
}

func TestNetworkRedirectFinalizesPreviouslyFilteredHop(t *testing.T) {
	s := &Session{network: &networkState{
		enabled:      true,
		patterns:     []string{"/landed"},
		observerMode: networkObserverCapture,
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	targetID := "target-filtered-redirect"
	requestID := "request-filtered-redirect"
	s.onRequestWillBeSentForTarget(targetID, map[string]any{
		"requestId": requestID,
		"request":   map[string]any{"url": "https://example.test/start", "method": "GET"},
	})
	s.onRequestWillBeSentForTarget(targetID, map[string]any{
		"requestId":        requestID,
		"redirectResponse": map[string]any{"status": float64(302), "mimeType": "text/html"},
		"request":          map[string]any{"url": "https://example.test/landed", "method": "GET"},
	})

	if len(s.network.history) != 0 {
		t.Fatalf("URL-filtered redirect hop was unexpectedly retained: %+v", s.network.history)
	}
	key := networkRequestKey(targetID, requestID)
	if got := s.network.pendingAll[key]; got == nil || got.URL != "https://example.test/landed" {
		t.Fatalf("matching redirect destination was not retained: %+v", got)
	}
}

func TestPassiveDiagnosticsRetainNoRequestBody(t *testing.T) {
	s := &Session{network: &networkState{
		observerMode: networkObserverDiagnostics,
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	s.onRequestWillBeSent(map[string]any{
		"requestId": "diagnostic",
		"request": map[string]any{
			"url":      "https://example.com/api/submit",
			"method":   "POST",
			"postData": "sensitive-value",
		},
	})
	if got := s.network.pendingAll["diagnostic"]; got == nil || got.RequestBody != "" {
		t.Fatalf("passive diagnostics retained request body: %#v", got)
	}
}

func TestInFlightPassiveRequestDoesNotBecomeExplicitCapture(t *testing.T) {
	s := &Session{network: &networkState{
		observerMode: networkObserverDiagnostics,
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	s.onRequestWillBeSent(map[string]any{
		"requestId": "passive-in-flight",
		"request": map[string]any{
			"url":    "https://example.com/api/before-capture",
			"method": "GET",
		},
	})

	s.network.mu.Lock()
	s.network.enabled = true
	s.network.observerMode = networkObserverCapture
	s.network.mu.Unlock()
	s.onResponseReceived(map[string]any{
		"requestId": "passive-in-flight",
		"response":  map[string]any{"status": float64(200), "mimeType": "application/json"},
	})
	s.onLoadingFinished(map[string]any{"requestId": "passive-in-flight"})

	if len(s.network.history) != 0 {
		t.Fatalf("request begun during passive diagnostics was reported as explicit capture: %+v", s.network.history)
	}
	if len(s.network.pending) != 0 || len(s.network.pendingAll) != 0 {
		t.Fatal("completed passive request remained pending")
	}
}

func TestInFlightPassiveFailureRemainsFailureOnlyDiagnostic(t *testing.T) {
	s := &Session{network: &networkState{
		observerMode: networkObserverDiagnostics,
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	s.onRequestWillBeSent(map[string]any{
		"requestId": "passive-failure",
		"request": map[string]any{
			"url":      "https://example.com/api/before-capture",
			"method":   "POST",
			"postData": "must-not-be-retained",
		},
	})
	s.network.mu.Lock()
	s.network.enabled = true
	s.network.observerMode = networkObserverCapture
	s.network.mu.Unlock()
	s.onLoadingFailed(map[string]any{"requestId": "passive-failure"})

	if len(s.network.history) != 1 || s.network.history[0].Status != 0 {
		t.Fatalf("passive failure history=%+v, want one network-failure diagnostic", s.network.history)
	}
	if s.network.history[0].RequestBody != "" {
		t.Fatalf("passive failure retained request body: %+v", s.network.history[0])
	}
}

func TestInFlightExplicitRequestIsNotRetainedAfterCaptureDisabled(t *testing.T) {
	s := &Session{network: &networkState{
		enabled:      true,
		observerMode: networkObserverCapture,
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	s.onRequestWillBeSent(map[string]any{
		"requestId": "explicit-in-flight",
		"request": map[string]any{
			"url":      "https://example.com/api/submit",
			"method":   "POST",
			"postData": "sensitive-request-body",
		},
	})

	s.network.mu.Lock()
	s.network.enabled = false
	s.network.observerMode = networkObserverNone
	s.network.mu.Unlock()
	s.onResponseReceived(map[string]any{
		"requestId": "explicit-in-flight",
		"response":  map[string]any{"status": float64(500), "mimeType": "text/plain"},
	})
	s.onLoadingFinished(map[string]any{"requestId": "explicit-in-flight"})

	if len(s.network.history) != 0 {
		t.Fatalf("request body/result was retained after capture was disabled: %+v", s.network.history)
	}
	if len(s.network.pending) != 0 || len(s.network.pendingAll) != 0 {
		t.Fatal("completed post-disable request remained pending")
	}
}

func TestInFlightExplicitFailureIsNotRetainedAfterCaptureDisabled(t *testing.T) {
	s := &Session{network: &networkState{
		enabled:      true,
		observerMode: networkObserverCapture,
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: defaultMaxNetworkRecords,
	}}
	s.onRequestWillBeSent(map[string]any{
		"requestId": "explicit-failure",
		"request": map[string]any{
			"url":      "https://example.com/api/submit",
			"method":   "POST",
			"postData": "sensitive-request-body",
		},
	})
	s.network.mu.Lock()
	s.network.enabled = false
	s.network.observerMode = networkObserverNone
	s.network.mu.Unlock()
	s.onLoadingFailed(map[string]any{"requestId": "explicit-failure"})

	if len(s.network.history) != 0 {
		t.Fatalf("explicit failure was retained after capture was disabled: %+v", s.network.history)
	}
	if len(s.network.pending) != 0 || len(s.network.pendingAll) != 0 {
		t.Fatal("failed post-disable request remained pending")
	}
}

func TestReattachNetworkObserversKeepsPassiveDiagnosticsMode(t *testing.T) {
	s := &Session{network: &networkState{
		observerMode:       networkObserverDiagnostics,
		observersInstalled: true,
		pending:            map[string]*NetworkCapture{"old": {TargetID: "old-target"}},
		pendingAll:         map[string]*NetworkCapture{"old": {TargetID: "old-target"}},
		historyLimit:       defaultMaxNetworkRecords,
	}}

	err := s.reattachNetworkObserversLocked()
	if err == nil {
		t.Fatal("reattachNetworkObserversLocked() without a page returned nil")
	}
	if s.network.observerMode != networkObserverDiagnostics {
		t.Fatalf("observer mode = %v, want passive diagnostics retained", s.network.observerMode)
	}
	if s.network.observersInstalled {
		t.Fatal("observers reported installed without a current page")
	}
	if len(s.network.pending) != 0 || len(s.network.pendingAll) != 0 {
		t.Fatal("page-scoped pending requests were not cleared")
	}
	if s.network.captureInterrupted || s.network.droppedRequests != 0 {
		t.Fatal("passive diagnostics page change was reported as explicit capture loss")
	}
}

func TestFailedObserverAttachPreservesCaptureIntent(t *testing.T) {
	s := &Session{network: &networkState{enabled: true}}
	s.markNetworkObserverUnavailableLocked()
	if !s.network.enabled || s.network.observerMode != networkObserverCapture || s.network.observersInstalled {
		t.Fatalf("failed attach lost explicit capture intent: enabled=%t mode=%v installed=%t", s.network.enabled, s.network.observerMode, s.network.observersInstalled)
	}

	s.network.enabled = false
	s.markNetworkObserverUnavailableLocked()
	if s.network.observerMode != networkObserverDiagnostics || s.network.observersInstalled {
		t.Fatalf("failed attach lost passive diagnostics intent: mode=%v installed=%t", s.network.observerMode, s.network.observersInstalled)
	}
}

func TestFailedObserverAttachRetainsExistingUnsubscribeCallbacks(t *testing.T) {
	called := 0
	s := &Session{network: &networkState{
		enabled:            true,
		observersInstalled: true,
		observedPages:      map[string]*browse.Page{"old-target": nil},
		unsub:              []func(){func() { called++ }},
	}}

	s.markNetworkObserverUnavailableLocked()
	if s.network.observersInstalled {
		t.Fatal("failed active-page attachment must be reported as unavailable")
	}
	if !s.network.captureInterrupted {
		t.Fatal("explicit capture should record the attachment gap")
	}
	if len(s.network.unsub) != 1 {
		t.Fatalf("retained unsubscribe callbacks = %d, want 1", len(s.network.unsub))
	}

	s.detachNetworkObserversLocked()
	if called != 1 {
		t.Fatalf("existing observer cleanup calls = %d, want 1", called)
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	if !matchesAnyPattern("https://example.com", nil) {
		t.Fatal("expected nil patterns to match")
	}
	if !matchesAnyPattern("https://example.com/api/users", []string{"/api/"}) {
		t.Fatal("expected /api/ to match")
	}
	if matchesAnyPattern("https://example.com/static", []string{"/api/"}) {
		t.Fatal("expected /api/ to not match")
	}
}

func TestDetachNetworkObserversLocked(t *testing.T) {
	called := 0
	s := &Session{
		network: &networkState{
			enabled:            true,
			observersInstalled: true,
			unsub: []func(){
				func() { called++ },
				func() { called++ },
			},
		},
	}

	s.detachNetworkObserversLocked()

	if called != 2 {
		t.Fatalf("unsubscribe calls: got %d, want 2", called)
	}
	if s.network.observersInstalled {
		t.Fatal("expected observersInstalled to be false")
	}
	if len(s.network.unsub) != 0 {
		t.Fatalf("expected unsub callbacks to be cleared, got %d", len(s.network.unsub))
	}
}
