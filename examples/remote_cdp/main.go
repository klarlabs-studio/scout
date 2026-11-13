// Example: remote_cdp attaches Scout to a Chrome instance that is already
// running with its DevTools Protocol endpoint enabled. For multiple principals,
// a trusted gateway should assign a separate endpoint/profile to each caller;
// this process-wide endpoint must not be shared as a multi-tenant credential.
//
// Usage:
//
//	export SCOUT_REMOTE_CDP='http://127.0.0.1:9222'
//	export SCOUT_URL='https://example.com'
//	go run ./examples/remote_cdp
//
// Scout disconnects when the session closes. It does not start or stop the
// remote Chrome process.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (retErr error) {
	cdpURL := os.Getenv("SCOUT_REMOTE_CDP")
	if cdpURL == "" {
		return fmt.Errorf("SCOUT_REMOTE_CDP must contain a Chrome DevTools WebSocket URL or HTTP(S) DevTools endpoint")
	}
	targetURL := os.Getenv("SCOUT_URL")
	if targetURL == "" {
		targetURL = "https://example.com"
	}

	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		RemoteCDP: cdpURL,
		Timeout:   30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("attach to remote Chrome: %w", err)
	}
	defer func() {
		if err := session.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("disconnect from remote Chrome: %w", err)
		}
	}()

	page, err := session.NavigateWithPostconditionContext(ctx, targetURL, agent.Postcondition{URLContains: targetURL})
	if err != nil {
		return fmt.Errorf("navigate: %w", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(page); err != nil {
		return fmt.Errorf("write page result: %w", err)
	}
	return nil
}
