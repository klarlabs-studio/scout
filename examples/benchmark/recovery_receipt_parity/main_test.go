package main

import (
	"testing"

	"go.klarlabs.de/scout/agent"
)

func TestValidatePythonResultBindsVerifierOutputToReceipt(t *testing.T) {
	receipt := normalizedReceipt{
		Schema:        "scout.recovery-receipt/v1",
		Task:          agent.RecoveryReceiptTask{ID: "task-123"},
		Operation:     agent.RecoveryReceiptOperation{ID: "scout-op-v1.dGFzay0xMjM.2", Outcome: "unknown"},
		ReplayAllowed: false,
	}
	valid := map[string]any{
		"valid": true, "schema": receipt.Schema, "task_id": receipt.Task.ID,
		"operation_id": receipt.Operation.ID, "outcome": receipt.Operation.Outcome,
		"replay_authorized": false,
	}
	if err := validatePythonResult(valid, receipt); err != nil {
		t.Fatalf("matching verifier output rejected: %v", err)
	}
	tests := []struct {
		name  string
		field string
		value any
	}{
		{name: "schema", field: "schema", value: "other/v1"},
		{name: "task", field: "task_id", value: "other-task"},
		{name: "operation", field: "operation_id", value: "other-operation"},
		{name: "outcome", field: "outcome", value: "verified"},
		{name: "replay", field: "replay_authorized", value: true},
		{name: "missing replay", field: "replay_authorized", value: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mismatch := make(map[string]any, len(valid))
			for key, value := range valid {
				mismatch[key] = value
			}
			if test.value == nil {
				delete(mismatch, test.field)
			} else {
				mismatch[test.field] = test.value
			}
			if err := validatePythonResult(mismatch, receipt); err == nil {
				t.Fatalf("mismatched %s was accepted", test.field)
			}
		})
	}
}

func TestPassedScenariosExcludesFailedCases(t *testing.T) {
	covered := passedScenarios([]caseResult{
		{Scenario: "interrupted", Passed: true},
		{Scenario: "completed", Passed: false},
		{Scenario: "unknown", Passed: true},
	})
	if !covered["interrupted"] {
		t.Fatal("passing interrupted case was not reported as covered")
	}
	if covered["completed"] || covered["unknown"] {
		t.Fatalf("failed or unsupported scenarios reported as covered: %#v", covered)
	}
}

func TestNonFirstStepRequiresCompletedVerifiedOperation(t *testing.T) {
	valid := normalizedReceipt{
		Task:          agent.RecoveryReceiptTask{State: "completed"},
		Target:        agent.RecoveryReceiptTarget{ID: "target", FrameID: "frame"},
		Operation:     agent.RecoveryReceiptOperation{Ordinal: 2, Verification: "verified"},
		Policy:        &agent.RecoveryReceiptPolicy{Digest: "sha256:policy"},
		ReplayAllowed: false,
	}
	if err := validateScenario(caseInput{Scenario: "non_first_step"}, valid); err != nil {
		t.Fatalf("valid completed non-first step rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*normalizedReceipt)
	}{
		{name: "interrupted task", mutate: func(r *normalizedReceipt) { r.Task.State = "interrupted" }},
		{name: "unverified operation", mutate: func(r *normalizedReceipt) { r.Operation.Verification = "unknown" }},
		{name: "replay authorized", mutate: func(r *normalizedReceipt) { r.ReplayAllowed = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := validateScenario(caseInput{Scenario: "non_first_step"}, candidate); err == nil {
				t.Fatal("invalid non-first-step receipt was accepted")
			}
		})
	}
}
