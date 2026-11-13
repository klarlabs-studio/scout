// Command saved_flow_go_worker exercises Scout's Go SDK across worker loss.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/scout/agent"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: saved_flow_go_worker run|recover [flags]")
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "complete":
		err = completeTask(os.Args[2:])
	case "recover":
		err = recoverTask(os.Args[2:])
	case "verify":
		err = verifyReceipt(os.Args[2:])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fatal("%v", err)
	}
}

func completeTask(args []string) error {
	flags := flag.NewFlagSet("complete", flag.ContinueOnError)
	remote := flags.String("remote-cdp", "", "remote Chrome DevTools endpoint")
	origin := flags.String("origin", "", "allowed origin")
	pageURL := flags.String("url", "", "fixture task URL")
	taskID := flags.String("task-id", "", "durable task ID")
	storePath := flags.String("store", "", "durable task store")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *remote == "" || *origin == "" || *pageURL == "" || *taskID == "" || *storePath == "" {
		return fmt.Errorf("complete requires --remote-cdp, --origin, --url, --task-id, and --store")
	}
	store, err := agent.OpenBoltTaskRunStore(*storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		RemoteCDP: *remote, AllowedOrigins: []string{*origin}, AllowPrivateIPs: true,
		TaskRunStore: store, Timeout: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	defer session.Close()
	if _, err := session.NavigateWithPostconditionContext(ctx, *pageURL, agent.Postcondition{Text: "Ready"}); err != nil {
		return fmt.Errorf("navigate completed fixture: %w", err)
	}
	if _, err := session.SwitchToFrameContext(ctx, "#task-frame"); err != nil {
		return fmt.Errorf("select completed fixture frame: %w", err)
	}
	spec := completedSpec(*taskID)
	result, err := session.RunTaskContext(ctx, spec)
	if err != nil {
		return fmt.Errorf("complete two-step task: %w", err)
	}
	if result.Status != "completed" {
		return fmt.Errorf("two-step task status = %q, want completed", result.Status)
	}
	record, found, err := session.GetTaskRunContext(ctx, *taskID)
	if err != nil || !found {
		return fmt.Errorf("read completed task record: found=%v err=%w", found, err)
	}
	receipts := make([]agent.RecoveryReceipt, 0, 2)
	for _, ordinal := range []int{1, 2} {
		receipt, err := session.ExportTaskRunRecoveryReceiptContext(ctx, *taskID, ordinal)
		if err != nil {
			return fmt.Errorf("export completed operation %d: %w", ordinal, err)
		}
		receipts = append(receipts, receipt)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"record": record, "receipts": receipts})
}

func completedSpec(taskID string) agent.TaskRunSpec {
	return agent.TaskRunSpec{
		ID: taskID, Timeout: 5 * time.Minute,
		Actions: []agent.BatchAction{
			{Action: "click", Selector: "#step-one-button", ExpectedText: "First step complete"},
			{Action: "click", Selector: "#submit", ExpectedText: "Acknowledged"},
		},
	}
}

func verifyReceipt(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	path := flags.String("receipt", "", "recovery receipt JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("verify requires --receipt")
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	receipt, err := agent.DecodeRecoveryReceipt(data)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"valid": true, "task_id": receipt.Task.ID})
}

func run(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	remote := flags.String("remote-cdp", "", "remote Chrome DevTools endpoint")
	origin := flags.String("origin", "", "allowed origin")
	pageURL := flags.String("url", "", "fixture task URL")
	taskID := flags.String("task-id", "", "durable task ID")
	storePath := flags.String("store", "", "durable task store")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *remote == "" || *origin == "" || *pageURL == "" || *taskID == "" || *storePath == "" {
		return fmt.Errorf("run requires --remote-cdp, --origin, --url, --task-id, and --store")
	}
	store, err := agent.OpenBoltTaskRunStore(*storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		RemoteCDP: *remote, AllowedOrigins: []string{*origin}, AllowPrivateIPs: true,
		TaskRunStore: store, Timeout: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	defer session.Close()
	if _, err := session.NavigateWithPostconditionContext(ctx, *pageURL, agent.Postcondition{Text: "Ready"}); err != nil {
		return fmt.Errorf("navigate fixture: %w", err)
	}
	if _, err := session.SwitchToFrameContext(ctx, "#task-frame"); err != nil {
		return fmt.Errorf("select fixture frame: %w", err)
	}
	_, err = session.RunTaskContext(ctx, agent.TaskRunSpec{
		ID: *taskID, Timeout: 5 * time.Minute,
		Actions: []agent.BatchAction{{Action: "click", Selector: "#submit", ExpectedText: "Acknowledged"}},
	})
	return err
}

func recoverTask(args []string) error {
	flags := flag.NewFlagSet("recover", flag.ContinueOnError)
	storePath := flags.String("store", "", "durable task store")
	taskID := flags.String("task-id", "", "durable task ID")
	origin := flags.String("origin", "", "allowed origin used by the original task")
	evidenceRef := flags.String("evidence-reference", "", "fixture ledger operation ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *storePath == "" || *taskID == "" || *origin == "" {
		return fmt.Errorf("recover requires --store, --task-id, and --origin")
	}
	store, err := agent.OpenBoltTaskRunStore(*storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	session := agent.NewSessionFromBrowser(nil, agent.SessionConfig{
		TaskRunStore: store, AllowedOrigins: []string{*origin}, AllowPrivateIPs: true,
	})
	record, found, err := session.GetTaskRunContext(ctx, *taskID)
	if err != nil {
		return err
	}
	if !found || !record.InFlight {
		return fmt.Errorf("task %q is not retained as an interrupted reservation", *taskID)
	}
	receipt, err := session.ExportTaskRunRecoveryReceiptContext(ctx, *taskID, 1)
	if err != nil {
		return fmt.Errorf("export interrupted receipt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("validate interrupted receipt: %w", err)
	}
	_, retryErr := session.RunTaskContext(ctx, agent.TaskRunSpec{
		ID: *taskID, Timeout: 5 * time.Minute,
		Actions: []agent.BatchAction{{Action: "click", Selector: "#submit", ExpectedText: "Acknowledged"}},
	})
	if retryErr == nil {
		return fmt.Errorf("same-ID retry unexpectedly succeeded")
	}
	conflict, ok := retryErr.(*agent.TaskRunConflictError)
	if !ok || !strings.Contains(conflict.Reason, "previous execution may still be in progress") {
		return fmt.Errorf("same-ID retry returned %T (%v), want the in-flight replay guard", retryErr, retryErr)
	}
	ledger, err := os.ReadFile(*evidenceRef)
	if err != nil {
		return fmt.Errorf("read host ledger evidence: %w", err)
	}
	var ledgerState struct {
		OperationID string `json:"operation_id"`
		Committed   int    `json:"committed"`
		Requests    int    `json:"requests"`
	}
	if err := json.Unmarshal(ledger, &ledgerState); err != nil {
		return fmt.Errorf("decode host ledger evidence: %w", err)
	}
	if ledgerState.OperationID == "" || ledgerState.Committed != 1 || ledgerState.Requests != 1 {
		return fmt.Errorf("host ledger does not prove exactly one effect and request: %+v", ledgerState)
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	resolved, err := session.ResolveInterruptedTaskRunWithEvidenceContext(ctx, *taskID, record.Fingerprint, agent.TaskRunResolution{
		Status: "verified_complete", Evidence: "fixture ledger reports one committed effect and one request after Go worker loss",
		EvidenceSource: "saved_flow_fixture_ledger", EvidenceReference: ledgerState.OperationID, ObservedAt: observedAt,
	})
	if err != nil {
		return fmt.Errorf("resolve interrupted task: %w", err)
	}
	output := struct {
		Record       agent.TaskRunRecord   `json:"record"`
		Receipt      agent.RecoveryReceipt `json:"receipt"`
		RetryBlocked bool                  `json:"same_id_retry_blocked"`
		Ledger       any                   `json:"ledger"`
	}{Record: resolved, Receipt: receipt, RetryBlocked: true, Ledger: ledgerState}
	return json.NewEncoder(os.Stdout).Encode(output)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
