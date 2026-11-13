// Example: route authenticated application principals to dedicated remote
// browser endpoints. The authentication middleware is intentionally supplied
// by the host application; this example never trusts a principal header from
// an external caller.
//
// Run with SCOUT_BROWSER_ENDPOINTS_JSON containing a JSON map from stable
// principal IDs to browser policies, then integrate Handler with the host's
// authentication middleware. A policy includes trusted CDP endpoint and
// allowed HTTP(S) origins. This is an in-process Go API example; it is not an
// MCP HTTP gateway and does not make one shared MCP endpoint principal-aware.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

type principalKey struct{}

// WithAuthenticatedPrincipal is for trusted authentication middleware after
// it has validated a credential. Never call it with a caller-controlled header
// unless that header was authenticated by a trusted upstream proxy.
func WithAuthenticatedPrincipal(ctx context.Context, principalID string) context.Context {
	return context.WithValue(ctx, principalKey{}, strings.TrimSpace(principalID))
}

func authenticatedPrincipal(ctx context.Context) (string, bool) {
	principal, ok := ctx.Value(principalKey{}).(string)
	return principal, ok && principal != ""
}

type gateway struct {
	policyByPrincipal map[string]browserPolicy
	mu                sync.Mutex
	sessions          map[string]*principalSession
	idleTimeout       time.Duration
	closed            bool
	activeRequests    sync.WaitGroup
	stopReaper        chan struct{}
	reaperDone        chan struct{}
}

// principalSession serializes browser work for a principal so two requests do
// not race navigation or extraction on the same active tab.
type principalSession struct {
	mu         sync.Mutex
	session    *agent.Session
	lastUsedAt time.Time
}

type browserPolicy struct {
	RemoteCDP       string   `json:"remote_cdp"`
	AllowedOrigins  []string `json:"allowed_origins"`
	AllowPrivateIPs bool     `json:"allow_private_ips,omitempty"`
}

type navigateResponse struct {
	TargetID string `json:"target_id"`
	URL      string `json:"url"`
	State    string `json:"state,omitempty"`
}

func newGateway(policyByPrincipal map[string]browserPolicy) *gateway {
	return newGatewayWithIdleTimeout(policyByPrincipal, 30*time.Minute)
}

func newGatewayWithIdleTimeout(policyByPrincipal map[string]browserPolicy, idleTimeout time.Duration) *gateway {
	if idleTimeout <= 0 {
		idleTimeout = 30 * time.Minute
	}
	policies := make(map[string]browserPolicy, len(policyByPrincipal))
	for principal, policy := range policyByPrincipal {
		policy.AllowedOrigins = append([]string(nil), policy.AllowedOrigins...)
		policies[principal] = policy
	}
	g := &gateway{
		policyByPrincipal: policies, sessions: make(map[string]*principalSession), idleTimeout: idleTimeout,
		stopReaper: make(chan struct{}), reaperDone: make(chan struct{}),
	}
	go g.reapLoop()
	return g
}

// Handler expects the host authentication middleware to place a verified
// stable principal in request context. Principal IDs are used only to select
// from trusted server configuration; request parameters cannot select an
// endpoint. Scout keeps one session per configured principal and serializes
// that principal's browser operations. Idle sessions are disconnected after
// 30 minutes; call Close during application shutdown to disconnect all
// remaining sessions. AllowPrivateIPs is
// a host policy for the remote browser; keep it false unless private network
// access is specifically required and separately constrained by egress rules.
func (g *gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /navigate", g.navigate)
	return mux
}

func (g *gateway) navigate(w http.ResponseWriter, r *http.Request) {
	principal, ok := authenticatedPrincipal(r.Context())
	if !ok {
		http.Error(w, "authenticated principal required", http.StatusUnauthorized)
		return
	}
	policy, ok := g.policyByPrincipal[principal]
	if !ok {
		http.Error(w, "no browser assigned to principal", http.StatusForbidden)
		return
	}
	var input struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if input.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	if !originAllowed(input.URL, policy.AllowedOrigins) {
		http.Error(w, "url origin is not allowed for principal", http.StatusForbidden)
		return
	}
	if err := g.acquireRequest(); err != nil {
		http.Error(w, "gateway is shutting down", http.StatusServiceUnavailable)
		return
	}
	defer g.activeRequests.Done()

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	entry, err := g.sessionFor(ctx, principal, policy)
	if err != nil {
		http.Error(w, "attach to assigned browser failed", http.StatusBadGateway)
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.session == nil {
		http.Error(w, "browser session was closed", http.StatusServiceUnavailable)
		return
	}
	page, err := entry.session.NavigateWithPostconditionContext(ctx, input.URL, agent.Postcondition{URLContains: input.URL})
	if err != nil {
		http.Error(w, "navigation failed", http.StatusBadGateway)
		return
	}
	response := navigateResponse{TargetID: page.TargetID, URL: page.URL}
	if r.URL.Query().Get("read_state") == "1" {
		state, err := entry.session.ExtractContext(ctx, "#state")
		if err != nil {
			http.Error(w, "read page state failed", http.StatusBadGateway)
			return
		}
		response.State = state.Text
	}
	g.touchSession(principal, entry)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (g *gateway) sessionFor(ctx context.Context, principal string, policy browserPolicy) (*principalSession, error) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, errors.New("gateway is closed")
	}
	entry := g.sessions[principal]
	if entry == nil {
		entry = &principalSession{lastUsedAt: time.Now()}
		g.sessions[principal] = entry
	}
	g.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.session != nil {
		return entry, nil
	}
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		RemoteCDP:       policy.RemoteCDP,
		AllowedOrigins:  append([]string(nil), policy.AllowedOrigins...),
		AllowPrivateIPs: policy.AllowPrivateIPs,
		Timeout:         20 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	entry.session = session
	entry.lastUsedAt = time.Now()
	return entry, nil
}

func (g *gateway) acquireRequest() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("gateway is closed")
	}
	g.activeRequests.Add(1)
	return nil
}

func (g *gateway) touchSession(principal string, entry *principalSession) {
	g.mu.Lock()
	if !g.closed && g.sessions[principal] == entry {
		entry.lastUsedAt = time.Now()
	}
	g.mu.Unlock()
}

func (g *gateway) reapLoop() {
	defer close(g.reaperDone)
	interval := g.idleTimeout / 4
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stopReaper:
			return
		case <-ticker.C:
			g.evictIdleSessions(time.Now())
		}
	}
}

func (g *gateway) evictIdleSessions(now time.Time) {
	var idle []*principalSession
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	for key, candidate := range g.sessions {
		if now.Sub(candidate.lastUsedAt) < g.idleTimeout || !candidate.mu.TryLock() {
			continue
		}
		if now.Sub(candidate.lastUsedAt) >= g.idleTimeout {
			delete(g.sessions, key)
			idle = append(idle, candidate)
		} else {
			candidate.mu.Unlock()
		}
	}
	g.mu.Unlock()
	for _, candidate := range idle {
		old := candidate.session
		candidate.session = nil
		if old != nil {
			_ = old.Close()
		}
		candidate.mu.Unlock()
	}
}

// Close disconnects all cached Scout sessions. It is safe to call more than
// once and should be called during host application shutdown.
func (g *gateway) Close() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	close(g.stopReaper)
	entries := make([]*principalSession, 0, len(g.sessions))
	for _, entry := range g.sessions {
		entries = append(entries, entry)
	}
	g.sessions = make(map[string]*principalSession)
	g.mu.Unlock()
	<-g.reaperDone
	g.activeRequests.Wait()

	var closeErr error
	for _, entry := range entries {
		entry.mu.Lock()
		if entry.session != nil {
			closeErr = errors.Join(closeErr, entry.session.Close())
			entry.session = nil
		}
		entry.mu.Unlock()
	}
	return closeErr
}

func originAllowed(rawURL string, allowed []string) bool {
	target, err := url.Parse(rawURL)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return false
	}
	targetOrigin := strings.ToLower(target.Scheme + "://" + target.Host)
	for _, candidate := range allowed {
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		if (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.ToLower(parsed.Scheme+"://"+parsed.Host) == targetOrigin {
			return true
		}
	}
	return false
}

func loadPolicies() (map[string]browserPolicy, error) {
	raw := os.Getenv("SCOUT_BROWSER_ENDPOINTS_JSON")
	if raw == "" {
		return nil, errors.New("SCOUT_BROWSER_ENDPOINTS_JSON is required")
	}
	var policies map[string]browserPolicy
	if err := json.Unmarshal([]byte(raw), &policies); err != nil {
		return nil, fmt.Errorf("decode SCOUT_BROWSER_ENDPOINTS_JSON: %w", err)
	}
	if len(policies) == 0 {
		return nil, errors.New("SCOUT_BROWSER_ENDPOINTS_JSON must contain at least one principal")
	}
	for principal, policy := range policies {
		if strings.TrimSpace(principal) == "" || strings.TrimSpace(policy.RemoteCDP) == "" || len(policy.AllowedOrigins) == 0 {
			return nil, errors.New("principal IDs, remote_cdp, and non-empty allowed_origins must be configured")
		}
		cdpEndpoint, err := url.ParseRequestURI(policy.RemoteCDP)
		if err != nil || cdpEndpoint.Host == "" || (cdpEndpoint.Scheme != "http" && cdpEndpoint.Scheme != "https" && cdpEndpoint.Scheme != "ws" && cdpEndpoint.Scheme != "wss") {
			return nil, fmt.Errorf("principal %q has invalid remote_cdp URL", principal)
		}
		for _, origin := range policy.AllowedOrigins {
			parsed, err := url.Parse(origin)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return nil, fmt.Errorf("principal %q has invalid allowed origin %q", principal, origin)
			}
		}
	}
	return policies, nil
}

func main() {
	policies, err := loadPolicies()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// In a real application, wrap this handler in authentication middleware
	// that calls WithAuthenticatedPrincipal only after credential validation.
	// Do not expose this example's bare handler directly to an untrusted network.
	gateway := newGateway(policies)
	server := &http.Server{Addr: "127.0.0.1:8088", Handler: gateway.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, err)
		}
	case <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "HTTP shutdown:", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	if err := gateway.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "browser session cleanup:", err)
	}
}
