package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRejectDuplicateJSONKeysAcrossNestedObjects(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"one","schema":"two"}`,
		`{"outer":{"value":1,"value":2}}`,
		`[{"value":1,"value":2}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			if err := rejectDuplicateJSONKeys([]byte(raw)); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
				t.Fatalf("rejectDuplicateJSONKeys(%s) error = %v; want duplicate-key rejection", raw, err)
			}
		})
	}
}

func TestDecodeRecoveryReceiptRejectsDuplicateNestedKey(t *testing.T) {
	task := TaskRunRecord{
		ID: "task-1", Fingerprint: "fingerprint", TargetID: "target-1",
		BrowserInstanceID: "browser-1",
		Plan:              &TaskRunPlan{PolicyID: taskOriginPolicyID(nil, false)},
	}
	action := ActionReceipt{OperationOrdinal: 1, Action: "click", TargetID: "target-1", BrowserInstanceID: "browser-1", Verification: VerificationUnknown}
	receipt, err := NewRecoveryReceipt(task, action)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Replace(string(encoded), `"id":"task-1"`, `"id":"task-1","id":"task-2"`, 1)
	if raw == string(encoded) {
		t.Fatalf("test fixture did not contain expected task ID: %s", encoded)
	}
	if _, err := DecodeRecoveryReceipt([]byte(raw)); err == nil || !strings.Contains(err.Error(), "duplicate JSON key: id") {
		t.Fatalf("DecodeRecoveryReceipt() error = %v; want nested duplicate-key rejection", err)
	}
}

func TestRecoveryDecodersRejectDuplicateKeys(t *testing.T) {
	duplicate := []byte(`{"schema":"first","schema":"second"}`)
	tests := []struct {
		name string
		call func([]byte) error
	}{
		{"receipt", func(data []byte) error { _, err := DecodeRecoveryReceipt(data); return err }},
		{"handoff", func(data []byte) error { _, err := DecodeRecoveryHandoffEnvelope(data); return err }},
		{"observation envelope", func(data []byte) error { _, err := DecodeRecoveryHandoffObservationEnvelope(data); return err }},
		{"observed binding", func(data []byte) error { _, err := DecodeRecoveryHandoffObservedBinding(data); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate JSON key: schema") {
				t.Fatalf("decoder error = %v; want duplicate-key rejection", err)
			}
		})
	}
}
