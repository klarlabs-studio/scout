package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type recoveryViewStore struct {
	record TaskRunRecord
	found  bool
}

func (s recoveryViewStore) ReserveTaskRun(context.Context, TaskRunRecord) (TaskRunRecord, bool, error) {
	return TaskRunRecord{}, false, nil
}
func (s recoveryViewStore) BindTaskRunTarget(context.Context, string, string, string, string) error {
	return nil
}
func (s recoveryViewStore) CompleteTaskRun(context.Context, TaskRunRecord) error { return nil }
func (s recoveryViewStore) ReleaseTaskRun(context.Context, string, string) error { return nil }
func (s recoveryViewStore) GetTaskRun(context.Context, string) (TaskRunRecord, bool, error) {
	return s.record, s.found, nil
}

func TestRecoverTaskRunViewReturnsRecordAndOptionalReceipt(t *testing.T) {
	ctx := context.Background()
	task := TaskRunRecord{ID: "checkout-42", Fingerprint: "plan", TargetID: "target", BrowserInstanceID: "browser"}
	t.Run("receipt available", func(t *testing.T) {
		withReceipt := TaskRunRecord{ID: task.ID, Fingerprint: task.Fingerprint, TargetID: task.TargetID, BrowserInstanceID: task.BrowserInstanceID, Result: &TaskRunResult{Batch: &BatchResult{Results: []BatchActionResult{{Index: 0, Receipt: &ActionReceipt{OperationOrdinal: 1, Action: "click", TargetID: "target", BrowserInstanceID: "browser", Verification: VerificationUnknown}}}}}}
		session := &Session{taskRunStore: recoveryViewStore{record: withReceipt, found: true}}
		view, err := session.RecoverTaskRunContext(ctx, task.ID, 0)
		if err != nil || !view.Found || view.Record == nil || view.Receipt == nil {
			t.Fatalf("RecoverTaskRunContext() = %+v, %v; want record and receipt", view, err)
		}
		if view.Receipt.Recovery.ReplayAuthorized {
			t.Fatal("recovery view receipt authorized replay")
		}
	})
	t.Run("receipt unavailable retains record", func(t *testing.T) {
		withoutReceipt := TaskRunRecord{ID: task.ID, Fingerprint: task.Fingerprint, TargetID: task.TargetID, BrowserInstanceID: task.BrowserInstanceID, Result: &TaskRunResult{Batch: &BatchResult{Results: []BatchActionResult{{Index: 0, Action: "click"}}}}}
		session := &Session{taskRunStore: recoveryViewStore{record: withoutReceipt, found: true}}
		view, err := session.RecoverTaskRunContext(ctx, task.ID, 0)
		if err != nil || !view.Found || view.Record == nil || view.Receipt != nil || view.ReceiptUnavailableReason == "" {
			t.Fatalf("RecoverTaskRunContext() = %+v, %v; want record with receipt omission reason", view, err)
		}
	})
	t.Run("missing record", func(t *testing.T) {
		session := &Session{taskRunStore: recoveryViewStore{}}
		view, err := session.RecoverTaskRunContext(ctx, task.ID, 0)
		if err != nil || view.Found || view.Record != nil || view.Receipt != nil {
			t.Fatalf("RecoverTaskRunContext() = %+v, %v; want not-found", view, err)
		}
	})
}

func TestRecoveryReceiptDoesNotInventBrowserGeneration(t *testing.T) {
	task := TaskRunRecord{
		ID: "checkout-42", Fingerprint: "plan-fingerprint", TargetID: "target-1",
		BrowserInstanceID: "browser-fingerprint",
	}
	action := ActionReceipt{
		OperationOrdinal: 1, Action: "click", TargetID: "target-1",
		BrowserInstanceID: "browser-fingerprint", Verification: VerificationUnknown,
	}
	receipt, err := NewRecoveryReceipt(task, action)
	if err != nil {
		t.Fatalf("NewRecoveryReceipt() error = %v", err)
	}
	if receipt.Browser.Generation != "" {
		t.Fatalf("browser generation = %q, want omitted without an independent host token", receipt.Browser.Generation)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	receipt.Browser.Generation = "broker-generation-2"
	comparison, err := CompareRecoveryReceiptBinding(receipt, RecoveryReceiptBinding{
		BrowserInstanceID: "browser-fingerprint", BrowserGeneration: "broker-generation-1",
		TargetID: "target-1", FrameBindingKnown: true,
	})
	if err != nil {
		t.Fatalf("CompareRecoveryReceiptBinding() error = %v", err)
	}
	if comparison.SameBrowser {
		t.Fatal("browser binding matched a different host-issued generation")
	}
}

func TestExportTaskRunRecoveryReceiptUsesTaskLocalOperationOrdinal(t *testing.T) {
	task := TaskRunRecord{
		ID: "task-with-prefix-actions", Fingerprint: "plan-fingerprint", TargetID: "target-1",
		BrowserInstanceID: "browser-1",
		Plan:              &TaskRunPlan{Actions: []TaskRunRecoveryStep{{Action: "navigate"}, {Action: "click"}}},
		Result: &TaskRunResult{Status: "completed", Batch: &BatchResult{Results: []BatchActionResult{{
			Index: 1, Action: "click", TargetID: "target-1",
			Receipt: &ActionReceipt{
				OperationOrdinal: 47, Action: "click", TargetID: "target-1", BrowserInstanceID: "browser-1",
				Verification: VerificationUnknown, Outcome: OutcomeUnknown,
			},
		}}}},
	}

	receipt, err := ExportTaskRunRecoveryReceipt(task, 0)
	if err != nil {
		t.Fatalf("ExportTaskRunRecoveryReceipt() error = %v", err)
	}
	if receipt.Operation.Ordinal != 2 || receipt.Operation.ID != RecoveryOperationID(task.ID, 2) {
		t.Fatalf("operation identity = (%d, %q), want task-local step 2", receipt.Operation.Ordinal, receipt.Operation.ID)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("exported receipt does not validate: %v", err)
	}
}

func TestRecoveryOperationIDRoundTrip(t *testing.T) {
	for _, taskID := range []string{"checkout-42", "tenant/a.task", "こんにちは/🧭", "a..b/9"} {
		t.Run(taskID, func(t *testing.T) {
			id := RecoveryOperationID(taskID, 12)
			if strings.Contains(id, "/") {
				t.Fatalf("operation ID %q contains an unescaped task delimiter", id)
			}
			gotTaskID, gotOrdinal, err := parseRecoveryOperationID(id)
			if err != nil || gotTaskID != taskID || gotOrdinal != 12 {
				t.Fatalf("parseRecoveryOperationID(%q) = %q, %d, %v", id, gotTaskID, gotOrdinal, err)
			}
		})
	}
}

func TestRecoveryReceiptRejectsNonCanonicalOperationID(t *testing.T) {
	receipt, err := NewRecoveryReceipt(TaskRunRecord{
		ID: "tenant/a", Fingerprint: "plan", TargetID: "target", BrowserInstanceID: "browser",
	}, ActionReceipt{
		OperationOrdinal: 1, Action: "click", TargetID: "target", BrowserInstanceID: "browser", Verification: VerificationUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Operation.ID = "tenant/a/1"
	if err := receipt.Validate(); err == nil {
		t.Fatal("Validate() accepted the ambiguous legacy operation ID")
	}
}

func TestRecoveryReceiptAlwaysRequiresAuthoritativeReconciliation(t *testing.T) {
	cases := []struct {
		name         string
		verification VerificationState
	}{
		{name: "verified", verification: VerificationVerified},
		{name: "rejected", verification: VerificationRejected},
		{name: "unknown", verification: VerificationUnknown},
		{name: "unverified", verification: VerificationUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := TaskRunRecord{ID: "task", Fingerprint: "plan", TargetID: "target", BrowserInstanceID: "browser"}
			action := ActionReceipt{OperationOrdinal: 1, Action: "click", TargetID: "target", BrowserInstanceID: "browser", Verification: tc.verification}
			receipt, err := NewRecoveryReceipt(task, action)
			if err != nil {
				t.Fatalf("NewRecoveryReceipt() error = %v", err)
			}
			if receipt.Recovery.ReplayAuthorized {
				t.Fatal("receipt authorized replay")
			}
			if receipt.Recovery.NextStep != "reconcile_with_authoritative_application_state" {
				t.Fatalf("next step = %q", receipt.Recovery.NextStep)
			}
			receipt.Recovery.NextStep = "none"
			if err := receipt.Validate(); err == nil {
				t.Fatal("Validate() accepted a next step that does not require authoritative reconciliation")
			}
		})
	}
}

func TestRecoveryReceiptOmitsPageTextAndExpectedValues(t *testing.T) {
	const pageSentinel = "PAGE_SECRET_CONFIRMATION_6f5e"
	const valueSentinel = "EXPECTED_SECRET_VALUE_92ac"
	task := TaskRunRecord{
		ID: "sensitive-task", Fingerprint: "plan-fingerprint", TargetID: "target-1",
		BrowserInstanceID: "browser-1",
		Plan: &TaskRunPlan{Actions: []TaskRunRecoveryStep{{
			Action: "click", Selector: "#submit", Postcondition: "text", PostconditionTarget: pageSentinel,
		}}},
		Result: &TaskRunResult{Status: "completed", Batch: &BatchResult{Results: []BatchActionResult{{
			Index: 0, Action: "click", TargetID: "target-1",
			Receipt: &ActionReceipt{
				OperationOrdinal: 3, Action: "click", TargetID: "target-1", BrowserInstanceID: "browser-1",
				Verification: VerificationUnknown, Outcome: OutcomeUnknown,
				Evidence: "page showed " + pageSentinel, RecoveryHint: "look for " + pageSentinel,
				TraceID:       "trace-safe-reference",
				Postcondition: &Postcondition{ValueSelector: "#confirmation", Value: valueSentinel},
			},
		}}}},
	}

	receipt, err := ExportTaskRunRecoveryReceipt(task, 1)
	if err != nil {
		t.Fatalf("ExportTaskRunRecoveryReceipt() error = %v", err)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, sentinel := range []string{pageSentinel, valueSentinel} {
		if strings.Contains(string(data), sentinel) {
			t.Fatalf("portable receipt exposed sensitive sentinel %q: %s", sentinel, data)
		}
	}
	if receipt.Postcondition == nil || receipt.Postcondition.Kind != "value" || receipt.Postcondition.Captured {
		t.Fatalf("portable postcondition leaked or lost its safe type: %+v", receipt.Postcondition)
	}
	if len(receipt.Evidence.References) != 1 || receipt.Evidence.References[0] != "trace:trace-safe-reference" {
		t.Fatalf("portable receipt trace reference = %v, want safe trace reference", receipt.Evidence.References)
	}
}
