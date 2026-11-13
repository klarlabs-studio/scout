// Command task_run is the smallest Go example for a verified, durably
// idempotent browser task. It uses only a local fixture and installed Chrome.
//
// Run with: go run ./examples/task_run
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
	"sync"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

type report struct {
	TaskID                 string `json:"task_id"`
	Status                 string `json:"status"`
	Verified               bool   `json:"verified"`
	IdempotentReplay       bool   `json:"idempotent_replay"`
	CommitRequests         int    `json:"commit_requests"`
	AcceptedOperations     int    `json:"accepted_operations"`
	DurableStoreConfigured bool   `json:"durable_task_store_configured"`
	PolicyID               string `json:"policy_id"`
	ReplayAllowed          bool   `json:"replay_authorized"`
}

var taskStorePath = flag.String("task-store", "", "bbolt task store path; omit to use a disposable temporary store")

func main() {
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := run(ctx, *taskStorePath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, taskStorePath string) (retErr error) {
	taskID := "example-task-001"
	if taskStorePath != "" {
		// A retained database can contain results from earlier runs. Give this
		// fresh fixture its own ID so a cached result is never presented as a
		// result verified against the current run.
		taskID = "example-task-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	var ledgerMu sync.Mutex
	acceptedTasks := make(map[string]struct{})
	commitRequests := 0
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/commit" {
			requestTaskID := r.URL.Query().Get("task_id")
			if requestTaskID != taskID || r.Method != http.MethodPost {
				http.Error(w, "task_id and POST are required", http.StatusBadRequest)
				return
			}
			ledgerMu.Lock()
			commitRequests++
			acceptedTasks[requestTaskID] = struct{}{}
			ledgerMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Scout task</title>
<button id="complete" onclick="fetch('/commit?task_id=%s',{method:'POST'}).then(()=>{document.querySelector('#result').textContent='done'})">Complete</button>
<p id="result">waiting</p>`, taskID)
	}))
	defer fixture.Close()

	durableStore := taskStorePath != ""
	if !durableStore {
		stateDir, err := os.MkdirTemp("", "scout-task-run-")
		if err != nil {
			return fmt.Errorf("create task store directory: %w", err)
		}
		defer os.RemoveAll(stateDir)
		taskStorePath = filepath.Join(stateDir, "tasks.db")
	} else if err := os.MkdirAll(filepath.Dir(taskStorePath), 0700); err != nil {
		return fmt.Errorf("create task store parent directory: %w", err)
	}
	store, err := agent.OpenBoltTaskRunStore(taskStorePath)
	if err != nil {
		return fmt.Errorf("open durable task store: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close durable task store: %w", err)
		}
	}()

	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless:        true,
		Timeout:         15 * time.Second,
		AllowPrivateIPs: true, // The temporary fixture listens on loopback.
		AllowedOrigins:  []string{fixture.URL},
		TaskRunStore:    store,
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
	if _, err := session.NavigateWithPostconditionContext(ctx, fixture.URL, agent.Postcondition{Text: "waiting"}); err != nil {
		return fmt.Errorf("navigate to local fixture: %w", err)
	}

	result, err := session.RunTaskContext(ctx, agent.TaskRunSpec{
		ID:             taskID,
		AllowedOrigins: []string{fixture.URL},
		Timeout:        30 * time.Second,
		Actions: []agent.BatchAction{{
			Action:       "click",
			Selector:     "#complete",
			ExpectedText: "done",
		}},
		FinalPostcondition: &agent.Postcondition{Text: "done"},
	})
	if err != nil {
		return fmt.Errorf("run verified task: %w", err)
	}
	if result.Status != "completed" || result.Batch == nil || result.Batch.Verified != 1 || result.FinalVerification == nil || !result.FinalVerification.Verified {
		return fmt.Errorf("task did not complete with verified evidence: status=%q", result.Status)
	}

	// A caller retry with the same task ID and plan must reuse the durable
	// result, without clicking the page or committing the fixture operation a
	// second time.
	retry, err := session.RunTaskContext(ctx, agent.TaskRunSpec{
		ID:             taskID,
		AllowedOrigins: []string{fixture.URL},
		Timeout:        30 * time.Second,
		Actions: []agent.BatchAction{{
			Action:       "click",
			Selector:     "#complete",
			ExpectedText: "done",
		}},
		FinalPostcondition: &agent.Postcondition{Text: "done"},
	})
	if err != nil {
		return fmt.Errorf("reuse completed task result: %w", err)
	}
	if !retry.IdempotencyReplay || retry.Status != "completed" {
		return fmt.Errorf("same-ID request did not return the saved completed result: status=%q replay=%t", retry.Status, retry.IdempotencyReplay)
	}
	ledgerMu.Lock()
	accepted := len(acceptedTasks)
	requests := commitRequests
	_, acceptedForTask := acceptedTasks[result.ID]
	ledgerMu.Unlock()
	if !acceptedForTask || accepted != 1 || requests != 1 {
		return fmt.Errorf("fixture ledger shows %d accepted task IDs from %d commit requests; expected one of each for %q", accepted, requests, result.ID)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report{
		TaskID: result.ID, Status: result.Status, Verified: true,
		IdempotentReplay: retry.IdempotencyReplay, CommitRequests: requests, AcceptedOperations: accepted,
		DurableStoreConfigured: durableStore, PolicyID: result.PolicyID, ReplayAllowed: false,
	}); err != nil {
		return fmt.Errorf("write task report: %w", err)
	}
	return nil
}
