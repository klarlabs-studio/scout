package agent

import (
	"testing"
	"time"
)

func recoveryHandoffFixture(t *testing.T) (RecoveryHandoffEnvelope, map[string][]byte, RecoveryHandoffObservedBinding, time.Time) {
	t.Helper()
	const (
		owner      = "tenant/orders"
		generation = "broker-generation-7"
		target     = "target-17"
		keyID      = "test-key-v1"
	)
	key := []byte("0123456789abcdef0123456789abcdef")
	origins := []string{"https://example.test"}
	policyID := taskOriginPolicyID(origins, false)
	task := TaskRunRecord{
		ID: "order-42", Fingerprint: "plan-fingerprint", TargetID: target,
		BrowserInstanceID: "scout-browser-fingerprint",
		Plan:              &TaskRunPlan{PolicyID: policyID, AllowedOrigins: origins},
	}
	action := ActionReceipt{
		OperationOrdinal: 1, Action: "click", TargetID: target,
		BrowserInstanceID: task.BrowserInstanceID, Verification: VerificationUnknown,
		FrameID: "main-frame",
	}
	receipt, err := NewRecoveryReceipt(task, action)
	if err != nil {
		t.Fatalf("NewRecoveryReceipt() error = %v", err)
	}
	now := time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)
	envelope, err := NewRecoveryHandoffEnvelope(receipt, owner, generation, keyID, key, now.Add(-time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewRecoveryHandoffEnvelope() error = %v", err)
	}
	observed := RecoveryHandoffObservedBinding{
		OwnerScope: owner, BrowserGeneration: generation, TargetID: target,
		FrameID: "main-frame", FrameBindingKnown: true,
		PolicyID: policyID, PolicyDigest: receipt.Policy.Digest,
	}
	return envelope, map[string][]byte{keyID: key}, observed, now
}

func TestCompareRecoveryHandoffBindingRequiresExactTrustedContext(t *testing.T) {
	envelope, keys, observed, now := recoveryHandoffFixture(t)
	matched, err := CompareRecoveryHandoffBinding(envelope, keys, observed.OwnerScope, now, observed)
	if err != nil {
		t.Fatalf("CompareRecoveryHandoffBinding() error = %v", err)
	}
	if !matched.SameContext || matched.ReplayAllowed {
		t.Fatalf("matching context comparison = %+v; want same context and replay denied", matched)
	}

	cases := []struct {
		name   string
		change func(*RecoveryHandoffObservedBinding)
	}{
		{"different browser generation", func(v *RecoveryHandoffObservedBinding) { v.BrowserGeneration = "replacement-generation" }},
		{"different target", func(v *RecoveryHandoffObservedBinding) { v.TargetID = "neighbor-target" }},
		{"different frame", func(v *RecoveryHandoffObservedBinding) { v.FrameID = "child-frame" }},
		{"frame identity unknown", func(v *RecoveryHandoffObservedBinding) { v.FrameBindingKnown = false }},
		{"different policy", func(v *RecoveryHandoffObservedBinding) {
			v.PolicyDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{"different owner", func(v *RecoveryHandoffObservedBinding) { v.OwnerScope = "tenant/other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := observed
			tc.change(&candidate)
			comparison, err := CompareRecoveryHandoffBinding(envelope, keys, observed.OwnerScope, now, candidate)
			if err != nil {
				t.Fatalf("CompareRecoveryHandoffBinding() error = %v", err)
			}
			if comparison.SameContext || comparison.ReplayAllowed {
				t.Fatalf("mismatched context comparison = %+v; want no context match and replay denied", comparison)
			}
		})
	}
}

func TestVerifyRecoveryHandoffRejectsTamperingAndExpiry(t *testing.T) {
	envelope, keys, observed, now := recoveryHandoffFixture(t)
	tampered := envelope
	tampered.Receipt.Task.ID = "another-task"
	if err := VerifyRecoveryHandoffEnvelope(tampered, keys, observed.OwnerScope, now); err == nil {
		t.Fatal("VerifyRecoveryHandoffEnvelope() accepted a modified signed receipt")
	}
	if err := VerifyRecoveryHandoffEnvelope(envelope, keys, observed.OwnerScope, now.Add(time.Minute)); err == nil {
		t.Fatal("VerifyRecoveryHandoffEnvelope() accepted an expired handoff")
	}
	if err := VerifyRecoveryHandoffEnvelope(envelope, keys, "tenant/other", now); err == nil {
		t.Fatal("VerifyRecoveryHandoffEnvelope() accepted a different owner")
	}
}
