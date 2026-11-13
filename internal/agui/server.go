package agui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServerConfig configures the AG-UI HTTP server.
type ServerConfig struct {
	Port           int
	Provider       string // "claude" (default), "openai", "ollama", or any OpenAI-compatible
	APIKey         string // API key for the provider
	Model          string // Model name (provider-specific)
	BaseURL        string // Base URL override (for Ollama, Groq, Together, etc.)
	ProxyServer    string // Optional proxy for locally launched browser sessions
	SmallModel     bool   // Use reduced tool set for small/local models
	IdleTimeout    time.Duration
	AllowedOrigins []string
}

// acquireRunLock serializes runs for a thread and lets a canceled request
// abandon the queue without waiting for the active run to finish.
func acquireRunLock(ctx context.Context, lock chan struct{}) (func(), error) {
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type threadRunLock struct {
	semaphore chan struct{}
	users     int
}

type threadRunLocks struct {
	mu    sync.Mutex
	locks map[string]*threadRunLock
}

func (locks *threadRunLocks) acquire(ctx context.Context, threadID string) (func(), error) {
	locks.mu.Lock()
	if locks.locks == nil {
		locks.locks = make(map[string]*threadRunLock)
	}
	entry := locks.locks[threadID]
	if entry == nil {
		entry = &threadRunLock{semaphore: make(chan struct{}, 1)}
		locks.locks[threadID] = entry
	}
	entry.users++
	locks.mu.Unlock()

	releaseSemaphore, err := acquireRunLock(ctx, entry.semaphore)
	if err != nil {
		locks.release(threadID, entry, nil)
		return nil, err
	}
	return func() { locks.release(threadID, entry, releaseSemaphore) }, nil
}

func (locks *threadRunLocks) release(threadID string, entry *threadRunLock, releaseSemaphore func()) {
	if releaseSemaphore != nil {
		releaseSemaphore()
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry.users--
	if entry.users == 0 && locks.locks[threadID] == entry {
		delete(locks.locks, threadID)
	}
}

// Serve starts the AG-UI HTTP server.
func Serve(ctx context.Context, cfg ServerConfig) error {
	if cfg.Port == 0 {
		cfg.Port = 4200
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 10 * time.Minute
	}
	allowedOrigins, err := normalizeAllowedOrigins(cfg.AllowedOrigins)
	if err != nil {
		return fmt.Errorf("agui: invalid allowed origins: %w", err)
	}
	listenerOrigins, err := uiListenerOrigins(cfg.Port)
	if err != nil {
		return fmt.Errorf("agui: invalid listener origins: %w", err)
	}

	sessions := NewSessionManager(cfg.IdleTimeout, cfg.ProxyServer)
	defer func() {
		if err := sessions.Close(); err != nil {
			log.Printf("agui: close browser sessions: %v", err)
		}
	}()

	llm, err := newProvider(cfg)
	if err != nil {
		return err
	}
	// Auto-detect small model for local providers
	if cfg.Provider == "ollama" {
		cfg.SmallModel = true
	}
	tools := CuratedTools()
	if cfg.SmallModel {
		tools = CoreTools()
	}
	log.Printf("agui: using %s provider (model: %s, tools: %d)", cfg.Provider, providerModel(cfg), len(tools))

	handler := &Handler{
		LLM:      llm,
		Sessions: sessions,
		Tools:    tools,
	}
	var runLocks threadRunLocks

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/session", sessionDeleteHandler(sessions, &runLocks))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

		var input RunAgentInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(input.ThreadID) == "" || strings.TrimSpace(input.RunID) == "" {
			http.Error(w, "threadId and runId are required", http.StatusBadRequest)
			return
		}
		if !validRunIdentifier(input.ThreadID) || !validRunIdentifier(input.RunID) {
			http.Error(w, "threadId and runId must be 1-128 ASCII letters, digits, hyphens, or underscores", http.StatusBadRequest)
			return
		}
		if len(input.Messages) == 0 || len(input.Messages) > 100 {
			http.Error(w, "messages must contain between 1 and 100 entries", http.StatusBadRequest)
			return
		}
		for i, message := range input.Messages {
			if message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
				http.Error(w, fmt.Sprintf("messages[%d].role must be user, assistant, or tool", i), http.StatusBadRequest)
				return
			}
			if message.Role == "user" && strings.TrimSpace(message.Content) == "" {
				http.Error(w, fmt.Sprintf("messages[%d].content is required", i), http.StatusBadRequest)
				return
			}
		}
		releaseRunLock, err := runLocks.acquire(r.Context(), input.ThreadID)
		if err != nil {
			return
		}
		defer releaseRunLock()

		sse, err := NewSSEWriter(w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := handler.HandleRun(r.Context(), sse, input); err != nil {
			log.Printf("agui: run error: %v", err)
		}
	})

	guardedMux := originGate(allowedOrigins, listenerOrigins, mux)
	// AG-UI currently has no authentication or per-user session identity. Keep
	// its browser session endpoint reachable only from the local machine.
	addr := uiListenAddress(cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           guardedMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() { //nolint:gosec // G118: shutdown deliberately uses a fresh context; the server ctx is already canceled (that is the trigger)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("agui: serving on http://%s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("agui: server error: %w", err)
	}
	return nil
}

func sessionDeleteHandler(sessions *SessionManager, runLocks *threadRunLocks) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		threadID := r.URL.Query().Get("threadId")
		if !validRunIdentifier(threadID) {
			http.Error(w, "threadId must be 1-128 ASCII letters, digits, hyphens, or underscores", http.StatusBadRequest)
			return
		}
		releaseRunLock, err := runLocks.acquire(r.Context(), threadID)
		if err != nil {
			return
		}
		defer releaseRunLock()
		if err := sessions.Remove(threadID); err != nil {
			log.Printf("agui: close browser session: %v", err)
			http.Error(w, "Could not close browser session", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func validRunIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func uiListenAddress(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

func uiListenerOrigins(port int) (map[string]struct{}, error) {
	return normalizeAllowedOrigins([]string{
		fmt.Sprintf("http://localhost:%d", port),
		fmt.Sprintf("http://127.0.0.1:%d", port),
	})
}

func normalizeAllowedOrigins(origins []string) (map[string]struct{}, error) {
	allowed := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			return nil, fmt.Errorf("empty origin entry")
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return nil, fmt.Errorf("%q must be an exact HTTP(S) origin", raw)
		}
		canonical, err := canonicalOrigin(parsed)
		if err != nil {
			return nil, err
		}
		allowed[canonical] = struct{}{}
	}
	return allowed, nil
}

func allowRequestOrigin(w http.ResponseWriter, r *http.Request, allowed map[string]struct{}, sameOriginMaps ...map[string]struct{}) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		return origin == "" // non-browser requests have no ambient browser origin
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	canonical, err := canonicalOrigin(parsed)
	if err != nil {
		return false
	}
	if _, ok := allowed[canonical]; ok {
		w.Header().Set("Access-Control-Allow-Origin", canonical)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		return true
	}
	for _, sameOrigin := range sameOriginMaps {
		if _, ok := sameOrigin[canonical]; ok {
			return true
		}
	}
	return false
}

func originGate(allowed, sameOrigin map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && !allowRequestOrigin(w, r, allowed, sameOrigin) {
			http.Error(w, "origin is not allowed", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodOptions && r.URL.Path != "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func canonicalOrigin(parsed *url.URL) (string, error) {
	if parsed == nil || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("invalid origin")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("invalid origin host")
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("invalid origin port")
		}
		if (parsed.Scheme == "http" && value == 80) || (parsed.Scheme == "https" && value == 443) {
			port = ""
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return strings.ToLower(parsed.Scheme) + "://" + host, nil
}

// newProvider creates the appropriate LLMProvider based on config.
func newProvider(cfg ServerConfig) (LLMProvider, error) {
	switch cfg.Provider {
	case "", "claude", "anthropic":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("agui: %s is required for claude provider", "ANTHROPIC_API_KEY")
		}
		return NewClaudeProvider(cfg.APIKey, cfg.Model), nil

	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("agui: %s is required for openai provider", "OPENAI_API_KEY")
		}
		return NewOpenAIProvider(cfg.APIKey, cfg.Model, cfg.BaseURL), nil

	case "ollama":
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		model := cfg.Model
		if model == "" {
			model = "llama3.1"
		}
		// Ollama exposes an OpenAI-compatible API
		return NewOpenAIProvider("", model, baseURL), nil

	default:
		// Treat unknown providers as OpenAI-compatible with a custom base URL
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("agui: unknown provider %q — set --base-url for OpenAI-compatible endpoints", cfg.Provider)
		}
		return NewOpenAIProvider(cfg.APIKey, cfg.Model, cfg.BaseURL), nil
	}
}

func providerModel(cfg ServerConfig) string {
	if cfg.Model != "" {
		return cfg.Model
	}
	switch cfg.Provider {
	case "", "claude", "anthropic":
		return "claude-sonnet-4-20250514"
	case "openai":
		return "gpt-4o"
	case "ollama":
		return "llama3.1"
	default:
		return "(default)"
	}
}
