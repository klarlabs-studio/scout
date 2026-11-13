// Command saved_flow_scout drives the same non-idempotent benchmark fixture as
// the agent-browser saved-flow adapter, using Scout's durable task store.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/scout/agent"
)

type report struct {
	Stage          string `json:"stage"`
	TaskID         string `json:"task_id"`
	Status         string `json:"status,omitempty"`
	RetryBlocked   bool   `json:"retry_blocked,omitempty"`
	Resolution     string `json:"resolution,omitempty"`
	LedgerCount    int    `json:"ledger_count,omitempty"`
	LedgerRequests int    `json:"ledger_requests,omitempty"`
	Error          string `json:"error,omitempty"`
}

func main() {
	stage := flag.String("stage", "submit", "control, submit, retry-check, or reconcile")
	taskID := flag.String("task-id", "", "stable operation identity")
	baseURL := flag.String("base-url", "", "shared benchmark fixture origin")
	storePath := flag.String("store", "", "durable task-run store shared across worker processes")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := run(ctx, *stage, *taskID, strings.TrimRight(*baseURL, "/"), *storePath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, stage, taskID, baseURL, storePath string) error {
	if taskID == "" || baseURL == "" || storePath == "" {
		return fmt.Errorf("--task-id, --base-url, and --store are required")
	}
	store, err := agent.OpenBoltTaskRunStore(storePath)
	if err != nil {
		return err
	}
	defer store.Close()

	switch stage {
	case "control", "submit", "retry-check":
		return runBrowserStage(ctx, stage, taskID, baseURL, store)
	case "reconcile":
		return reconcile(ctx, taskID, baseURL, store)
	default:
		return fmt.Errorf("unknown stage %q", stage)
	}
}

func runBrowserStage(ctx context.Context, stage, taskID, baseURL string, store agent.TaskRunStore) error {
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless: true, Timeout: 45 * time.Second, AllowPrivateIPs: true,
		AllowedOrigins: []string{baseURL}, TaskRunStore: store,
	})
	if err != nil {
		return fmt.Errorf("start Scout session: %w", err)
	}
	defer session.Close()

	spec := agent.TaskRunSpec{
		ID: taskID, AllowedOrigins: []string{baseURL}, Timeout: 10 * time.Minute,
		Actions: []agent.BatchAction{{Action: "click", Selector: "#submit", ExpectedText: "Acknowledged"}},
	}
	if stage == "retry-check" {
		_, err = session.RunTaskContext(ctx, spec)
		var conflict *agent.TaskRunConflictError
		if !errors.As(err, &conflict) {
			return fmt.Errorf("same-ID retry was not rejected as an interrupted task: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(report{Stage: stage, TaskID: taskID, RetryBlocked: true, Error: conflict.Error()})
	}
	pageURL := baseURL + "/saved-flow-task?operation_id=" + url.QueryEscape(taskID)
	if _, err := session.Navigate(pageURL); err != nil {
		return fmt.Errorf("navigate to shared fixture: %w", err)
	}
	if _, err := session.SwitchToFrame("#task-frame"); err != nil {
		return fmt.Errorf("select shared fixture task frame: %w", err)
	}
	result, err := session.RunTaskContext(ctx, spec)
	if err != nil {
		if stage == "submit" {
			// The controller kills this process after the fixture commits but before
			// its held response is released. Keep the browser worker alive here.
			<-ctx.Done()
		}
		return fmt.Errorf("run Scout task: %w", err)
	}
	if result.Status != "completed" {
		return fmt.Errorf("task returned status %q", result.Status)
	}
	if stage == "submit" {
		return fmt.Errorf("interrupted submit unexpectedly completed")
	}
	state, err := fixtureState(ctx, baseURL, taskID)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report{Stage: stage, TaskID: taskID, Status: result.Status, LedgerCount: state.Committed, LedgerRequests: state.Requests})
}

type operationState struct {
	OperationID string `json:"operation_id"`
	Committed   int    `json:"committed"`
	Requests    int    `json:"requests"`
}

func fixtureState(ctx context.Context, baseURL, taskID string) (operationState, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/saved-flow-state?operation_id="+url.QueryEscape(taskID), nil)
	if err != nil {
		return operationState{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return operationState{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return operationState{}, fmt.Errorf("fixture state returned %s", response.Status)
	}
	var state operationState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		return operationState{}, err
	}
	if state.OperationID != taskID {
		return operationState{}, fmt.Errorf("fixture task mismatch: got %q, want %q", state.OperationID, taskID)
	}
	return state, nil
}

func reconcile(ctx context.Context, taskID, baseURL string, store *agent.BoltTaskRunStore) error {
	record, found, err := store.GetTaskRun(ctx, taskID)
	if err != nil {
		return err
	}
	if !found || !record.InFlight || record.TargetID == "" || record.BrowserInstanceID == "" {
		return fmt.Errorf("no bound interrupted task record for %q", taskID)
	}
	state, err := fixtureState(ctx, baseURL, taskID)
	if err != nil {
		return err
	}
	status := "unresolved"
	if state.Committed == 1 && state.Requests == 1 {
		status = "verified_complete"
	} else if state.Committed == 0 && state.Requests == 1 {
		status = "rejected"
	}
	resolutionStore, ok := any(store).(agent.TaskRunResolutionStore)
	if !ok {
		return fmt.Errorf("durable store does not support host resolution")
	}
	if err := resolutionStore.ResolveTaskRun(ctx, taskID, record.Fingerprint, agent.TaskRunResolution{
		Status: status, Evidence: fmt.Sprintf("shared fixture operation count=%d requests=%d", state.Committed, state.Requests),
		EvidenceSource: "saved_flow_shared_fixture_ledger", EvidenceReference: taskID,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ResolvedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("persist host reconciliation: %w", err)
	}
	resolved, found, err := store.GetTaskRun(ctx, taskID)
	if err != nil {
		return err
	}
	if !found || resolved.Resolution == nil || resolved.Resolution.Status != status || resolved.InFlight {
		return fmt.Errorf("host resolution did not persist: found=%t record=%+v", found, resolved)
	}
	return json.NewEncoder(os.Stdout).Encode(report{
		Stage: "reconcile", TaskID: taskID, Status: status, Resolution: resolved.Resolution.Status,
		LedgerCount: state.Committed, LedgerRequests: state.Requests,
	})
}
