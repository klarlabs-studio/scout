package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"go.klarlabs.de/scout/internal/agui"
)

func serveUI(args []string) {
	if err := runServeUI(args); err != nil {
		writeCLIError(err, "")
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
		Port:        4200,
		Provider:    provider,
		Model:       flags.get("model", os.Getenv("SCOUT_MODEL")),
		BaseURL:     flags.get("base-url", os.Getenv("SCOUT_BASE_URL")),
		ProxyServer: configuredProxyServer(),
	}
	var err error
	cfg.AllowedOrigins, err = configuredUIOrigins(flags, os.LookupEnv)
	if err != nil {
		return err
	}
	for key := range flags.flags {
		if key != "provider" && key != "model" && key != "base-url" && key != "api-key" && key != "port" && key != "allowed-origins" {
			return fmt.Errorf("unknown ui serve flag --%s", key)
		}
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

func configuredUIOrigins(flags cliFlags, lookupEnv func(string) (string, bool)) ([]string, error) {
	if raw, configured := flags.flags["allowed-origins"]; configured {
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("--allowed-origins is set but empty")
		}
		return strings.Split(raw, ","), nil
	}
	if raw, configured := lookupEnv("SCOUT_UI_ALLOWED_ORIGINS"); configured {
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("SCOUT_UI_ALLOWED_ORIGINS is set but empty")
		}
		return strings.Split(raw, ","), nil
	}
	return nil, nil
}
