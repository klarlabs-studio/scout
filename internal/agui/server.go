package agui

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// ServerConfig configures the AG-UI HTTP server.
type ServerConfig struct {
	Port        int
	Provider    string // "claude" (default), "openai", "ollama", or any OpenAI-compatible
	APIKey      string // API key for the provider
	Model       string // Model name (provider-specific)
	BaseURL     string // Base URL override (for Ollama, Groq, Together, etc.)
	SmallModel  bool   // Use reduced tool set for small/local models
	IdleTimeout time.Duration
}

// Serve starts the AG-UI HTTP server.
func Serve(ctx context.Context, cfg ServerConfig) error {
	if cfg.Port == 0 {
		cfg.Port = 4200
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 10 * time.Minute
	}

	sessions := NewSessionManager(cfg.IdleTimeout)
	defer sessions.Close()

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

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
			writeCORS(w)
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeCORS(w)

		var input RunAgentInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
			return
		}

		sse, err := NewSSEWriter(w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := handler.HandleRun(r.Context(), sse, input); err != nil {
			log.Printf("agui: run error: %v", err)
		}
	})

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("agui: serving on http://localhost%s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("agui: server error: %w", err)
	}
	return nil
}

func writeCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
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
