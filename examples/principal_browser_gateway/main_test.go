package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/scout/examples/benchmark/fixture"
	"go.klarlabs.de/scout/internal/launcher"
)

func TestGatewayRequiresTrustedPrincipalContext(t *testing.T) {
	g := newGateway(map[string]browserPolicy{"tenant-a": {RemoteCDP: "http://127.0.0.1:9222", AllowedOrigins: []string{"https://app.example"}}})
	r := httptest.NewRequest("POST", "/navigate", nil)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestGatewayRejectsPrincipalWithoutAssignedEndpoint(t *testing.T) {
	g := newGateway(map[string]browserPolicy{"tenant-a": {RemoteCDP: "http://127.0.0.1:9222", AllowedOrigins: []string{"https://app.example"}}})
	r := httptest.NewRequest("POST", "/navigate", nil)
	r = r.WithContext(WithAuthenticatedPrincipal(context.Background(), "tenant-b"))
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestOriginAllowedRequiresExactHTTPOrigin(t *testing.T) {
	allowed := []string{"https://app.example", "http://localhost:8080"}
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://app.example/orders", true},
		{"https://app.example.evil.test/orders", false},
		{"https://app.example:444/orders", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"http://localhost:8080/fixture", true},
	} {
		t.Run(tc.url, func(t *testing.T) {
			if got := originAllowed(tc.url, allowed); got != tc.want {
				t.Fatalf("originAllowed(%q) = %t, want %t", tc.url, got, tc.want)
			}
		})
	}
}

func TestNavigateRejectsDisallowedOriginBeforeBrowserAttach(t *testing.T) {
	g := newGateway(map[string]browserPolicy{"tenant-a": {
		RemoteCDP: "http://127.0.0.1:1", AllowedOrigins: []string{"https://app.example"},
	}})
	r := httptest.NewRequest("POST", "/navigate", strings.NewReader(`{"url":"http://127.0.0.1:9222/json/version"}`))
	r = r.WithContext(WithAuthenticatedPrincipal(context.Background(), "tenant-a"))
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestAuthenticatedPrincipalTrimsWhitespace(t *testing.T) {
	ctx := WithAuthenticatedPrincipal(context.Background(), " tenant-a ")
	principal, ok := authenticatedPrincipal(ctx)
	if !ok || principal != "tenant-a" {
		t.Fatalf("authenticatedPrincipal() = (%q, %t), want (tenant-a, true)", principal, ok)
	}
}

func TestGatewayCopiesPrincipalPolicies(t *testing.T) {
	allowed := []string{"https://app.example"}
	policies := map[string]browserPolicy{"tenant-a": {RemoteCDP: "http://127.0.0.1:9222", AllowedOrigins: allowed}}
	g := newGateway(policies)
	allowed[0] = "https://changed.example"
	policies["tenant-b"] = browserPolicy{RemoteCDP: "http://127.0.0.1:9223", AllowedOrigins: []string{"https://other.example"}}
	if !originAllowed("https://app.example/path", g.policyByPrincipal["tenant-a"].AllowedOrigins) {
		t.Fatal("gateway policy changed after caller mutated configuration")
	}
	if _, ok := g.policyByPrincipal["tenant-b"]; ok {
		t.Fatal("gateway observed a principal added after construction")
	}
}

func TestGatewayIdleCleanupAndShutdown(t *testing.T) {
	g := newGatewayWithIdleTimeout(map[string]browserPolicy{"tenant-a": {}, "tenant-b": {}}, time.Minute)
	active := &principalSession{lastUsedAt: time.Now()}
	idle := &principalSession{lastUsedAt: time.Now().Add(-2 * time.Minute)}
	g.sessions["tenant-a"] = active
	g.sessions["tenant-b"] = idle
	active.mu.Lock()
	g.evictIdleSessions(time.Now())
	active.mu.Unlock()
	if g.sessions["tenant-a"] != active {
		t.Fatal("active principal session was evicted")
	}
	if _, ok := g.sessions["tenant-b"]; ok {
		t.Fatal("idle principal session remained in pool")
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := g.acquireRequest(); err == nil {
		t.Fatal("request lease acquired after gateway shutdown")
	}
}

func TestGatewayReaperEvictsIdleSessionWithoutAnotherRequest(t *testing.T) {
	g := newGatewayWithIdleTimeout(map[string]browserPolicy{"tenant-a": {}}, 40*time.Millisecond)
	entry := &principalSession{lastUsedAt: time.Now()}
	g.mu.Lock()
	g.sessions["tenant-a"] = entry
	g.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	g.mu.Lock()
	_, exists := g.sessions["tenant-a"]
	g.mu.Unlock()
	if exists {
		t.Fatal("idle session remained cached without follow-up requests")
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestPrincipalPoliciesIsolateRemoteBrowserState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	firstBrowser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer firstBrowser.Close()
	secondBrowser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("second local Chrome unavailable: %v", err)
	}
	defer secondBrowser.Close()

	fixtureServer := httptest.NewServer(fixture.Handler())
	defer fixtureServer.Close()
	origin := fixtureServer.URL
	policies := map[string]browserPolicy{
		"tenant-a": {RemoteCDP: firstBrowser.WSEndpoint(), AllowedOrigins: []string{origin}, AllowPrivateIPs: true},
		"tenant-b": {RemoteCDP: secondBrowser.WSEndpoint(), AllowedOrigins: []string{origin}, AllowPrivateIPs: true},
	}
	gateway := newGateway(policies)
	app := httptest.NewServer(withPrincipalFromTest(gateway.Handler()))
	defer app.Close()
	defer func() {
		if err := gateway.Close(); err != nil {
			t.Errorf("close gateway sessions: %v", err)
		}
	}()
	client := app.Client()

	request := func(principal, path string) (navigateResponse, error) {
		body := `{"url":"` + origin + path + `"}`
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/navigate?read_state=1", strings.NewReader(body))
		if err != nil {
			return navigateResponse{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		// This test middleware is trusted test infrastructure, analogous to the
		// application's verified auth middleware.
		req.Header.Set("X-Test-Principal", principal)
		resp, err := client.Do(req)
		if err != nil {
			return navigateResponse{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return navigateResponse{}, fmt.Errorf("gateway returned %s", resp.Status)
		}
		var result navigateResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return result, err
		}
		return result, nil
	}
	seed, err := request("tenant-a", "/session-owner/seed")
	if err != nil {
		t.Fatal(err)
	}
	if seed.State != `["scout_owner=primary","primary"]` {
		t.Fatalf("tenant A seed state = %q", seed.State)
	}
	probe, err := request("tenant-b", "/session-owner/probe")
	if err != nil {
		t.Fatal(err)
	}
	if probe.State != `["",null]` {
		t.Fatalf("tenant B observed tenant A state: %q", probe.State)
	}
	checkB, err := request("tenant-b", "/session-owner/check")
	if err != nil {
		t.Fatal(err)
	}
	if checkB.State != `["scout_owner=secondary","secondary"]` {
		t.Fatalf("tenant B state after write = %q", checkB.State)
	}
	checkA, err := request("tenant-a", "/session-owner/check")
	if err != nil {
		t.Fatal(err)
	}
	if checkA.State != `["scout_owner=primary","primary"]` {
		t.Fatalf("tenant A state was contaminated by tenant B: %q", checkA.State)
	}
	if seed.TargetID == probe.TargetID || checkA.TargetID == checkB.TargetID {
		t.Fatalf("principals shared target IDs: A=%s B=%s", checkA.TargetID, checkB.TargetID)
	}
}

func withPrincipalFromTest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := r.Header.Get("X-Test-Principal")
		next.ServeHTTP(w, r.WithContext(WithAuthenticatedPrincipal(r.Context(), principal)))
	})
}

func TestGatewayRejectsBadMethod(t *testing.T) {
	g := newGateway(nil)
	req := httptest.NewRequest(http.MethodGet, "/navigate", nil)
	req.RemoteAddr = net.JoinHostPort("127.0.0.1", "1")
	resp := httptest.NewRecorder()
	g.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.Code)
	}
}
