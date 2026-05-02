package agui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestWriteCORS_HeadersSet(t *testing.T) {
	rec := httptest.NewRecorder()
	writeCORS(rec)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Access-Control-Allow-Methods missing POST: %q", got)
	}
}
