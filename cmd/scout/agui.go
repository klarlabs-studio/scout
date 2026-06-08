package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"go.klarlabs.de/scout/internal/agui"
)

func serveUI(args []string) {
	if err := runServeUI(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runServeUI(args []string) error {
	flags := parseFlags(args)

	provider := flags.get("provider", os.Getenv("SCOUT_PROVIDER"))
	if provider == "" {
		provider = "claude"
	}

	cfg := agui.ServerConfig{
		Port:     4200,
		Provider: provider,
		Model:    flags.get("model", os.Getenv("SCOUT_MODEL")),
		BaseURL:  flags.get("base-url", os.Getenv("SCOUT_BASE_URL")),
	}

	// Resolve API key from flag or environment
	switch provider {
	case "claude", "anthropic", "":
		cfg.APIKey = flags.get("api-key", os.Getenv("ANTHROPIC_API_KEY"))
	case "openai":
		cfg.APIKey = flags.get("api-key", os.Getenv("OPENAI_API_KEY"))
	case "ollama":
		// No API key needed
	default:
		cfg.APIKey = flags.get("api-key", os.Getenv("LLM_API_KEY"))
	}

	if p := flags.get("port", ""); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			cfg.Port = port
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	return agui.Serve(ctx, cfg)
}
