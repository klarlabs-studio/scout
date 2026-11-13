package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/agent"
)

func TestJoinWorkerPoolErrorsPreservesCancellationAndCleanupFailures(t *testing.T) {
	cleanupErr := errors.New("session cleanup failed")
	outputErr := errors.New("stdout closed")
	got := joinWorkerPoolErrors(outputErr, context.Canceled, []error{cleanupErr})
	for _, want := range []error{outputErr, context.Canceled, cleanupErr} {
		if !errors.Is(got, want) {
			t.Errorf("joined error %v does not preserve %v", got, want)
		}
	}
}

func TestCloseWorkerSessionReportsFailureAfterPoolCancellation(t *testing.T) {
	cleanupErr := errors.New("session cleanup failed")
	poolCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var reported error
	closeWorkerSession(3, func(ctx context.Context) error {
		if poolCtx.Err() == nil {
			t.Fatal("test pool context is not canceled")
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("session close context unexpectedly canceled: %v", err)
		}
		return cleanupErr
	}, func(err error) { reported = err })
	if !errors.Is(reported, cleanupErr) {
		t.Fatalf("close error was not reported after pool cancellation: %v", reported)
	}
	if got := reported.Error(); got == "" || !strings.Contains(got, "worker 3") {
		t.Fatalf("close error lacks worker context: %q", got)
	}
}

func TestWorkerErrorCodeUsesStableClasses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "policy", err: &browse.URLPolicyError{Code: "SCOUT_POLICY_PRIVATE_ADDRESS_BLOCKED"}, want: "SCOUT_POLICY_PRIVATE_ADDRESS_BLOCKED"},
		{name: "operation", err: &agent.OperationError{Phase: "page_read"}, want: "SCOUT_OPERATION_PAGE_READ"},
		{name: "generic", err: errors.New("fixture failed"), want: "SCOUT_JOB_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workerErrorCode(tt.err); got != tt.want {
				t.Fatalf("workerErrorCode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPublishJobFailurePreservesUnknownOutcome(t *testing.T) {
	unknown := &agent.ActionOutcomeError{
		Action: "click", TargetID: "target-1", BrowserInstanceID: "browser-1",
		OperationOrdinal: 2, Outcome: agent.OutcomeUnknown,
	}
	results := make(chan jobResult, 1)
	result := jobResult{JobID: 7, Worker: 1}
	publishJobFailure(results, &result, unknown, "click")
	got := <-results
	if got.Status != "needs_reconciliation" || got.Phase != "click" || got.ErrorCode != "SCOUT_ACTION_OUTCOME_UNKNOWN" {
		t.Fatalf("unexpected unknown outcome record: %+v", got)
	}
	if got.Recovery == nil {
		t.Fatalf("unknown outcome did not retain recovery record: %+v", got)
	}
	if err := got.Recovery.Validate(); err != nil {
		t.Fatalf("recovery record is invalid: %v", err)
	}
}
