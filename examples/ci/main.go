// Example: ci demonstrates a deterministic, self-contained browser check suitable
// for a Go application's CI job.
//
// Usage:
//
//	go run ./examples/ci
//	go run ./examples/ci -playbook ./ci-playbook.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

type report struct {
	Success        bool                  `json:"success"`
	PlaybookName   string                `json:"playbook_name"`
	PlaybookPath   string                `json:"playbook_path,omitempty"`
	Action         *agent.PageResult     `json:"action"`
	RecordedSteps  int                   `json:"recorded_steps"`
	PlaybookSHA256 string                `json:"playbook_sha256"`
	Replay         *agent.PlaybookResult `json:"replay"`
}

func main() {
	playbookPath := flag.String("playbook", "", "optional path to keep the recorded playbook JSON")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx, *playbookPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, playbookPath string) (retErr error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>CI smoke check</title></head><body><main><h1>Scout is ready</h1><button id="complete" onclick="document.querySelector('#status').textContent='all systems go'">Complete task</button><p id="status">waiting</p></main></body></html>`)
	}))
	defer server.Close()

	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless:        true,
		Timeout:         15 * time.Second,
		AllowPrivateIPs: true, // httptest listens on loopback
	})
	if err != nil {
		return fmt.Errorf("start Scout session: %w", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		if err := session.CloseContext(closeCtx); err != nil && retErr == nil {
			retErr = fmt.Errorf("close Scout session: %w", err)
		}
	}()

	if _, err := session.NavigateWithPostconditionContext(ctx, server.URL, agent.Postcondition{Text: "Scout is ready"}); err != nil {
		return fmt.Errorf("navigate: %w", err)
	}
	if err := session.StartRecordingPlaybookContext(ctx, "ci-smoke"); err != nil {
		return fmt.Errorf("start playbook recording: %w", err)
	}
	action, err := session.ClickWithPostconditionContext(ctx, "#complete", agent.Postcondition{Text: "all systems go"}, false)
	if err != nil {
		return fmt.Errorf("record task click: %w", err)
	}
	if !action.Verified || action.Outcome != string(agent.OutcomePostconditionVerified) {
		return fmt.Errorf("live click did not verify its postcondition: outcome=%q evidence=%q", action.Outcome, action.Evidence)
	}
	if action.Receipt == nil || action.Receipt.Policy == nil || !action.Receipt.Policy.AllowPrivateIPs {
		return fmt.Errorf("loopback fixture ran without an explicit private-IP policy exception")
	}
	if err := session.ExpectRecordedActionContext(ctx, agent.ActionExpect{Text: "all systems go"}); err != nil {
		return fmt.Errorf("attach replay expectation: %w", err)
	}
	playbook, err := session.StopRecordingPlaybookContext(ctx)
	if err != nil {
		return fmt.Errorf("finish playbook recording: %w", err)
	}
	if len(playbook.Actions) != 1 || playbook.Actions[0].Expected == nil || playbook.Actions[0].Expected.Text != "all systems go" {
		return fmt.Errorf("recorded playbook is missing its click or explicit text expectation")
	}
	cleanupDir := ""
	if playbookPath == "" {
		cleanupDir, err = os.MkdirTemp("", "scout-ci-playbook-")
		if err != nil {
			return fmt.Errorf("create temporary playbook directory: %w", err)
		}
		defer os.RemoveAll(cleanupDir)
		playbookPath = filepath.Join(cleanupDir, "ci-playbook.json")
	}
	if err := agent.SavePlaybook(playbook, playbookPath); err != nil {
		return fmt.Errorf("save playbook: %w", err)
	}
	loaded, err := agent.LoadPlaybook(playbookPath)
	if err != nil {
		return fmt.Errorf("load saved playbook: %w", err)
	}
	replay, err := session.ReplayPlaybookContext(ctx, loaded)
	if err != nil {
		return fmt.Errorf("replay saved playbook: %w", err)
	}
	if !replay.Success || replay.StepsRun != 1 || replay.FailedAt != 0 || len(replay.PlaybookSHA256) != 64 {
		_ = json.NewEncoder(os.Stderr).Encode(replay)
		return fmt.Errorf("CI playbook replay failed")
	}
	result := report{Success: true, PlaybookName: loaded.Name, Action: action, RecordedSteps: len(loaded.Actions), PlaybookSHA256: replay.PlaybookSHA256, Replay: replay}
	if cleanupDir == "" {
		result.PlaybookPath = playbookPath
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return fmt.Errorf("write replay result: %w", err)
	}
	return nil
}
