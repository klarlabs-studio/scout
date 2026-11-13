// Example hello demonstrates Scout's smallest useful Go workflow: open a
// deterministic page, click one control, and verify the resulting page state.
// It uses no model provider, account, remote site, or persistent data.
//
// Usage:
//
//	go run ./examples/hello
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (retErr error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><h1>Scout is ready</h1><button id="complete" onclick="document.querySelector('#status').textContent='Verified: task complete'">Complete task</button><p id="status">Waiting</p></body></html>`)
	}))
	defer server.Close()

	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless:        true,
		Timeout:         15 * time.Second,
		AllowPrivateIPs: true, // The fixture binds to loopback.
	})
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "chrome/chromium not found") || strings.Contains(message, "scout_chrome_path") {
			return fmt.Errorf("start Scout session: %w\nInstall Chrome or Chromium and ensure its executable is on PATH, or set SCOUT_REMOTE_CDP to a trusted browser DevTools WebSocket URL", err)
		}
		return fmt.Errorf("start Scout session: %w", err)
	}
	defer func() {
		if err := session.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close Scout session: %w", err)
		}
	}()

	if _, err := session.NavigateWithPostconditionContext(ctx, server.URL, agent.Postcondition{Text: "Scout is ready"}); err != nil {
		return fmt.Errorf("open the local fixture: %w", err)
	}
	result, err := session.ClickWithPostconditionContext(ctx, "#complete", agent.Postcondition{Text: "Verified: task complete"}, false)
	if err != nil {
		return fmt.Errorf("run verified click: %w", err)
	}
	if !result.Verified || result.Outcome != string(agent.OutcomePostconditionVerified) {
		return fmt.Errorf("click did not verify the expected page state: outcome=%q evidence=%q", result.Outcome, result.Evidence)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"success":   true,
		"verified":  result.Verified,
		"outcome":   result.Outcome,
		"evidence":  result.Evidence,
		"target_id": result.TargetID,
	})
}
