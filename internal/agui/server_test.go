package agui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
)

func TestAcquireRunLockCancellationWhileQueued(t *testing.T) {
	lock := make(chan struct{}, 1)
	releaseFirst, err := acquireRunLock(context.Background(), lock)
	if err != nil {
		t.Fatalf("acquire first run: %v", err)
	}
	defer releaseFirst()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		release, err := acquireRunLock(ctx, lock)
		if release != nil {
			release()
		}
		done <- err
	}()
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("queued acquire error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued acquire did not honor cancellation")
	}

	// Cancellation should not consume or release the active run's lock token.
	if len(lock) != 1 {
		t.Fatalf("active run lock occupancy = %d, want 1", len(lock))
	}
}

func TestSessionDeleteHandlerValidatesAndRemoves(t *testing.T) {
	sessions := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = sessions.Close() })
	sessions.mu.Lock()
	sessions.newSession = func(agent.SessionConfig) (*agent.Session, error) { return &agent.Session{}, nil }
	sessions.closeSession = func(*agent.Session) error { return nil }
	sessions.mu.Unlock()
	if _, err := sessions.Get("thread_1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	locks := &threadRunLocks{}
	handler := sessionDeleteHandler(sessions, locks)

	for _, test := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodGet, path: "/session?threadId=thread_1", want: http.StatusMethodNotAllowed},
		{method: http.MethodDelete, path: "/session?threadId=bad%2Fid", want: http.StatusBadRequest},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != test.want {
			t.Fatalf("%s %s status = %d, want %d", test.method, test.path, recorder.Code, test.want)
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/session?threadId=thread_1", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want %d: %s", recorder.Code, http.StatusNoContent, strings.TrimSpace(recorder.Body.String()))
	}
	if _, ok := sessions.sessions["thread_1"]; ok {
		t.Fatal("successful DELETE left the session in the manager")
	}
}

func TestSessionDeleteHandlerWaitsForThreadRunLock(t *testing.T) {
	sessions := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = sessions.Close() })
	locks := &threadRunLocks{}
	release, err := locks.acquire(context.Background(), "thread_1")
	if err != nil {
		t.Fatalf("acquire thread lock: %v", err)
	}
	handler := sessionDeleteHandler(sessions, locks)
	done := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/session?threadId=thread_1", nil))
		done <- recorder.Code
	}()
	select {
	case status := <-done:
		t.Fatalf("DELETE returned while thread run lock was held: status %d", status)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case status := <-done:
		if status != http.StatusNoContent {
			t.Fatalf("DELETE status = %d, want %d", status, http.StatusNoContent)
		}
	case <-time.After(time.Second):
		t.Fatal("DELETE did not continue after thread lock was released")
	}
}

func TestThreadRunLocksSerializeAndReleaseEntries(t *testing.T) {
	var locks threadRunLocks
	release, err := locks.acquire(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(locks.locks) != 1 {
		t.Fatalf("lock entries = %d, want 1", len(locks.locks))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		queuedRelease, err := locks.acquire(ctx, "thread-a")
		if queuedRelease != nil {
			queuedRelease()
		}
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("queued acquire error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued acquire did not cancel")
	}
	release()
	if len(locks.locks) != 0 {
		t.Fatalf("lock entries after release = %d, want 0", len(locks.locks))
	}
}

func TestUIListenAddressIsLoopbackOnly(t *testing.T) {
	if got, want := uiListenAddress(4200), "127.0.0.1:4200"; got != want {
		t.Fatalf("uiListenAddress() = %q, want %q", got, want)
	}
}

func TestValidRunIdentifier(t *testing.T) {
	for _, value := range []string{"a", "thread-123", "Scout_run_9", strings.Repeat("x", 128)} {
		if !validRunIdentifier(value) {
			t.Errorf("validRunIdentifier(%q) = false", value)
		}
	}
	for _, value := range []string{"", " ", "a/b", "a b", "é", strings.Repeat("x", 129)} {
		if validRunIdentifier(value) {
			t.Errorf("validRunIdentifier(%q) = true", value)
		}
	}
}

func TestNewProvider_ClaudeRequiresKey(t *testing.T) {
	if _, err := newProvider(ServerConfig{Provider: "claude"}); err == nil {
		t.Fatal("expected claude without APIKey to error")
	}
}

func TestNewProvider_OpenAIRequiresKey(t *testing.T) {
	if _, err := newProvider(ServerConfig{Provider: "openai"}); err == nil {
		t.Fatal("expected openai without APIKey to error")
	}
}

func TestNewProvider_OllamaUsesDefaultBaseURL(t *testing.T) {
	p, err := newProvider(ServerConfig{Provider: "ollama"})
	if err != nil {
		t.Fatalf("ollama provider: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNewProvider_DefaultIsClaude(t *testing.T) {
	if _, err := newProvider(ServerConfig{}); err == nil {
		t.Fatal("default (claude) without APIKey must error")
	}
	if _, err := newProvider(ServerConfig{APIKey: "k"}); err != nil {
		t.Errorf("default with APIKey should work, got %v", err)
	}
}

func TestNewProvider_AnthropicAlias(t *testing.T) {
	if _, err := newProvider(ServerConfig{Provider: "anthropic", APIKey: "k"}); err != nil {
		t.Errorf("anthropic alias should resolve to claude, got %v", err)
	}
}

func TestNewProvider_UnknownNeedsBaseURL(t *testing.T) {
	if _, err := newProvider(ServerConfig{Provider: "weird"}); err == nil {
		t.Fatal("unknown provider without BaseURL must error")
	}
	if _, err := newProvider(ServerConfig{Provider: "weird", BaseURL: "http://x", APIKey: "k"}); err != nil {
		t.Errorf("unknown provider with BaseURL should work as OpenAI-compatible, got %v", err)
	}
}

func TestProviderModel_DefaultsByProvider(t *testing.T) {
	cases := map[string]string{
		"claude":    "claude-sonnet-4-20250514",
		"anthropic": "claude-sonnet-4-20250514",
		"":          "claude-sonnet-4-20250514",
		"openai":    "gpt-4o",
		"ollama":    "llama3.1",
		"weird":     "(default)",
	}
	for prov, want := range cases {
		if got := providerModel(ServerConfig{Provider: prov}); got != want {
			t.Errorf("providerModel(%q) = %q, want %q", prov, got, want)
		}
	}
}

func TestProviderModel_ExplicitOverrides(t *testing.T) {
	if got := providerModel(ServerConfig{Provider: "claude", Model: "custom"}); got != "custom" {
		t.Errorf("explicit Model must override, got %q", got)
	}
}

func TestNormalizeAllowedOrigins(t *testing.T) {
	allowed, err := normalizeAllowedOrigins([]string{"https://app.example", "http://localhost:3000"})
	if err != nil || len(allowed) != 2 {
		t.Fatalf("normalizeAllowedOrigins = %v, %v", allowed, err)
	}
	for _, raw := range []string{"", "https://", "https://user:pass@example.com", "https://example.com/path", "https://example.com?x=1", "*"} {
		if _, err := normalizeAllowedOrigins([]string{raw}); err == nil {
			t.Errorf("normalizeAllowedOrigins(%q) accepted invalid origin", raw)
		}
	}
}

func TestAllowRequestOrigin(t *testing.T) {
	allowed, err := normalizeAllowedOrigins([]string{"https://app.example", "http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, origin string
		want         bool
	}{
		{name: "server-to-server", want: true},
		{name: "allowed", origin: "https://app.example", want: true},
		{name: "case-normalized", origin: "HTTPS://APP.EXAMPLE:443", want: true},
		{name: "unlisted", origin: "https://evil.example"},
		{name: "null-origin", origin: "null"},
		{name: "path-origin", origin: "https://app.example/path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/", nil)
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if got := allowRequestOrigin(rec, req, allowed); got != test.want {
				t.Fatalf("allowRequestOrigin(%q) = %t, want %t", test.origin, got, test.want)
			}
			if test.want && test.origin != "" && test.origin != "null" && rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
				t.Errorf("allow-origin header = %q", rec.Header().Get("Access-Control-Allow-Origin"))
			}
			if !test.want && rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("denied origin got CORS grant %q", rec.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
}

func TestOriginGateProtectsPostAndPreflight(t *testing.T) {
	allowed, err := normalizeAllowedOrigins([]string{"http://localhost:3000"})
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	sameOrigin, err := uiListenerOrigins(4200)
	if err != nil {
		t.Fatal(err)
	}
	h := originGate(allowed, sameOrigin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name, method, origin string
		want                 int
	}{
		{name: "allowed post", method: http.MethodPost, origin: "http://localhost:3000", want: http.StatusNoContent},
		{name: "same-origin localhost alias", method: http.MethodPost, origin: "http://localhost:4200", want: http.StatusNoContent},
		{name: "same-origin loopback address", method: http.MethodPost, origin: "http://127.0.0.1:4200", want: http.StatusNoContent},
		{name: "denied post", method: http.MethodPost, origin: "https://evil.example", want: http.StatusForbidden},
		{name: "allowed preflight", method: http.MethodOptions, origin: "http://localhost:3000", want: http.StatusOK},
		{name: "denied preflight", method: http.MethodOptions, origin: "https://evil.example", want: http.StatusForbidden},
		{name: "health origin ignored", method: http.MethodGet, origin: "https://evil.example", want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := "/"
			if test.name == "health origin ignored" {
				path = "/health"
			}
			req := httptest.NewRequest(test.method, path, nil)
			req.Header.Set("Origin", test.origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d", rec.Code, test.want)
			}
		})
	}
	if called != 4 {
		t.Fatalf("handler calls = %d, want allowed cross-origin and same-origin requests plus health probe", called)
	}
}

func TestConvertMessages_PreservesToolHistory(t *testing.T) {
	got := convertMessages([]InputMessage{
		{Role: "assistant", Content: "Searching", ToolCalls: []InputToolCall{{ID: "call-1", Name: "navigate", Arguments: `{"url":"https://example.com"}`}}},
		{Role: "tool", ToolCallID: "call-1", Content: `{"title":"Example"}`},
	})
	if len(got) != 2 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("tool-call history not preserved: %#v", got)
	}
	if got[0].ToolCalls[0].ID != "call-1" || got[0].ToolCalls[0].Args != `{"url":"https://example.com"}` {
		t.Errorf("tool call corrupted: %#v", got[0].ToolCalls[0])
	}
	if got[1].ToolCallID != "call-1" || got[1].Role != "tool" {
		t.Errorf("tool result association corrupted: %#v", got[1])
	}
}
