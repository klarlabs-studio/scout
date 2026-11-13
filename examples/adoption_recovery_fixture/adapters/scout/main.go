package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"go.klarlabs.de/scout/agent"
)

type report struct {
	Status          string `json:"status"`
	VerifiedText    string `json:"verified_text,omitempty"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	TaskID          string `json:"task_id"`
	Dispatch        int    `json:"dispatch_count,omitempty"`
	LedgerCount     int    `json:"ledger_count,omitempty"`
	Initial         string `json:"initial_status,omitempty"`
	Inspected       bool   `json:"ledger_inspected"`
	Retried         bool   `json:"retry_attempted"`
	TaskState       string `json:"task_state,omitempty"`
	TaskInFlight    bool   `json:"task_in_flight,omitempty"`
	TaskRecovered   bool   `json:"task_record_recovered,omitempty"`
	ReceiptState    string `json:"receipt_outcome,omitempty"`
	RecoveryOutcome string `json:"recovery_outcome,omitempty"`
	Resolved        bool   `json:"resolution_recorded,omitempty"`
	RecordTaskID    string `json:"record_task_id,omitempty"`
	PlanFingerprint string `json:"plan_fingerprint,omitempty"`
	RecordDispatch  string `json:"record_dispatch,omitempty"`
	Resolution      string `json:"record_resolution,omitempty"`
	EvidenceSource  string `json:"record_evidence_source,omitempty"`
}

func main() {
	stage := flag.String("stage", "first-success", "first-success, control, submit, or reconcile")
	taskID := flag.String("task-id", "", "task identity")
	fixture := flag.String("fixture", "", "fixture base URL or first-success page URL")
	storePath := flag.String("store", "", "durable task store path shared by submit and reconcile")
	flag.Parse()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := run(ctx, *stage, *taskID, *fixture, *storePath, started)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func run(ctx context.Context, stage, taskID, fixture, storePath string, started time.Time) (report, error) {
	if taskID == "" || fixture == "" {
		return report{}, fmt.Errorf("--task-id and --fixture are required")
	}
	result := report{TaskID: taskID}
	switch stage {
	case "first-success":
		session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, Timeout: 30 * time.Second, AllowPrivateIPs: true})
		if err != nil {
			return report{}, err
		}
		defer session.Close()
		if _, err := session.Navigate(fixture); err != nil {
			return report{}, err
		}
		if _, err := session.Click("#complete"); err != nil {
			return report{}, err
		}
		observed, err := session.Extract("#status")
		if err != nil {
			return report{}, err
		}
		if observed.Text != "Complete" {
			return report{}, fmt.Errorf("#status=%q, want Complete", observed.Text)
		}
		result.Status, result.VerifiedText = "verified", observed.Text
	case "submit", "control":
		if storePath == "" {
			return report{}, fmt.Errorf("--store is required for submit")
		}
		approvalURL := fmt.Sprintf("%s/?task=%s", fixture, taskID)
		store, err := agent.OpenBoltTaskRunStore(storePath)
		if err != nil {
			return report{}, err
		}
		defer store.Close()
		session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
			Headless: true, Timeout: 30 * time.Second, AllowPrivateIPs: true,
			AllowedOrigins: []string{fixture}, TaskRunStore: store,
		})
		if err != nil {
			return report{}, err
		}
		defer session.Close()
		if _, err := session.Navigate(approvalURL); err != nil {
			return report{}, err
		}
		_, err = session.RunTaskContext(ctx, agent.TaskRunSpec{
			ID: taskID, AllowedOrigins: []string{fixture}, Timeout: 75 * time.Second,
			Actions: []agent.BatchAction{{Action: "click", Selector: "#approve", ExpectedText: "Accepted 1"}},
		})
		if err != nil && stage == "submit" {
			// The facilitator intentionally kills this worker after the independent
			// ledger commits and before the held response is released. Keep waiting
			// so the durable record remains at the actual interrupted boundary.
			<-ctx.Done()
			return report{}, ctx.Err()
		}
		if err != nil {
			return report{}, err
		}
		if stage == "submit" {
			return report{}, fmt.Errorf("approval unexpectedly completed before facilitator interruption")
		}
		ledgerCount, err := fixtureLedgerCount(ctx, fixture, taskID)
		if err != nil {
			return report{}, err
		}
		return report{TaskID: taskID, Status: "verified", VerifiedText: "Accepted 1", Dispatch: 1, LedgerCount: ledgerCount, ElapsedMS: time.Since(started).Milliseconds()}, nil
	case "reconcile":
		if storePath == "" {
			return report{}, fmt.Errorf("--store is required for reconcile")
		}
		return reconcile(ctx, fixture, taskID, storePath, started)
	default:
		return report{}, fmt.Errorf("unknown --stage %q", stage)
	}
	result.ElapsedMS = time.Since(started).Milliseconds()
	return result, nil
}

func fixtureLedgerCount(ctx context.Context, fixture, taskID string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture+"/ledger?task="+url.QueryEscape(taskID), nil)
	if err != nil {
		return 0, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fixture ledger returned %s", response.Status)
	}
	var ledger struct {
		TaskID string `json:"task_id"`
		Count  int    `json:"count"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ledger); err != nil {
		return 0, err
	}
	if ledger.TaskID != taskID {
		return 0, fmt.Errorf("fixture ledger task mismatch: %q", ledger.TaskID)
	}
	return ledger.Count, nil
}

func reconcile(ctx context.Context, fixture, taskID, storePath string, started time.Time) (report, error) {
	store, err := agent.OpenBoltTaskRunStore(storePath)
	if err != nil {
		return report{}, err
	}
	defer store.Close()
	task, found, err := store.GetTaskRun(ctx, taskID)
	if err != nil {
		return report{}, err
	}
	if !found {
		return report{}, fmt.Errorf("durable task record %q not found", taskID)
	}
	if !task.InFlight || task.TargetID == "" || task.BrowserInstanceID == "" || task.Recovery == nil || task.Recovery.Current == nil || task.Recovery.Current.Status != "started" {
		return report{}, fmt.Errorf("durable task record is not a bound interrupted action: in_flight=%t target=%q browser=%q recovery=%+v", task.InFlight, task.TargetID, task.BrowserInstanceID, task.Recovery)
	}
	initialStatus := "unknown"
	if !task.InFlight && task.Result != nil {
		initialStatus = task.Result.Status
	}
	result := report{TaskID: taskID, Initial: initialStatus, TaskState: "interrupted", TaskInFlight: task.InFlight, RecordTaskID: task.ID, PlanFingerprint: task.Fingerprint}
	result.TaskRecovered = true
	if !task.InFlight {
		result.TaskState = "terminal"
	}
	if receipt, receiptErr := agent.ExportTaskRunRecoveryReceipt(task, 0); receiptErr == nil {
		result.ReceiptState = receipt.Operation.Outcome
		if receipt.Operation.Dispatch != "may_have_dispatched" {
			return report{}, fmt.Errorf("interrupted task receipt dispatch=%q, want may_have_dispatched", receipt.Operation.Dispatch)
		}
		result.RecordDispatch = receipt.Operation.Dispatch
	} else if task.InFlight {
		return report{}, fmt.Errorf("export interrupted task receipt: %w", receiptErr)
	}
	if task.InFlight && result.ReceiptState != "unknown" {
		return report{}, fmt.Errorf("interrupted task receipt outcome=%q, want unknown", result.ReceiptState)
	}
	result.RecoveryOutcome = result.ReceiptState
	// The task record establishes what Scout durably knows before consulting
	// the independent business ledger. Only the ledger may resolve the outcome.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture+"/ledger?task="+url.QueryEscape(taskID), nil)
	if err != nil {
		return report{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return report{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return report{}, fmt.Errorf("fixture ledger returned %s", response.Status)
	}
	var ledger struct {
		TaskID string `json:"task_id"`
		Count  int    `json:"count"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ledger); err != nil {
		return report{}, err
	}
	if ledger.TaskID != taskID {
		return report{}, fmt.Errorf("ledger task mismatch: %q", ledger.TaskID)
	}
	status := "rejected"
	if ledger.Count == 1 {
		status = "verified"
	} else if ledger.Count == 0 {
		status = "unknown"
	}
	if task.InFlight {
		resolutionStatus := "rejected"
		if ledger.Count == 1 {
			resolutionStatus = "verified_complete"
		} else if ledger.Count == 0 {
			resolutionStatus = "unresolved"
		}
		err = store.ResolveTaskRun(ctx, taskID, task.Fingerprint, agent.TaskRunResolution{
			Status: resolutionStatus, Evidence: fmt.Sprintf("fixture ledger count=%d", ledger.Count),
			EvidenceSource: "study_fixture_ledger", EvidenceReference: taskID,
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ResolvedAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return report{}, fmt.Errorf("persist host reconciliation: %w", err)
		}
		resolved, found, err := store.GetTaskRun(ctx, taskID)
		if err != nil {
			return report{}, fmt.Errorf("read persisted host reconciliation: %w", err)
		}
		if !found || resolved.Resolution == nil || resolved.Resolution.Status != resolutionStatus || resolved.InFlight != (resolutionStatus == "unresolved") {
			return report{}, fmt.Errorf("persisted host reconciliation does not match requested status %q", resolutionStatus)
		}
		result.Resolution, result.EvidenceSource = resolved.Resolution.Status, resolved.Resolution.EvidenceSource
		result.Resolved = true
	}
	result.Status, result.LedgerCount, result.Inspected = status, ledger.Count, true
	result.Retried, result.Dispatch, result.ElapsedMS = false, ledger.Count, time.Since(started).Milliseconds()
	return result, nil
}
