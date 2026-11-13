package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestBoltTaskRunStoreCompletedResultSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task-runs.db")
	store, err := OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reservation := TaskRunRecord{ID: "checkout-1", Fingerprint: "plan-a", InFlight: true}
	if got, created, err := store.ReserveTaskRun(ctx, reservation); err != nil || !created || got.ID != reservation.ID {
		t.Fatalf("ReserveTaskRun() = (%+v, %v, %v), want newly created reservation", got, created, err)
	}
	if err := store.BindTaskRunTarget(ctx, reservation.ID, reservation.Fingerprint, "target-1", "browser-1"); err != nil {
		t.Fatal(err)
	}
	want := &TaskRunResult{
		ID: "checkout-1", Status: "completed", TargetID: "target-1", BrowserInstanceID: "browser-1",
		PlanID: "plan-a", Batch: &BatchResult{Total: 1, Succeeded: 1, Verified: 1, NextActionIndex: 1,
			Results: []BatchActionResult{{Index: 0, Action: "click", TargetID: "target-1", Target: "#submit", Success: true, Verified: true, Outcome: "verified"}}},
	}
	completed := TaskRunRecord{ID: reservation.ID, Fingerprint: reservation.Fingerprint, TargetID: "target-1", BrowserInstanceID: "browser-1", Result: want}
	if err := store.CompleteTaskRun(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, found, err := store.GetTaskRun(ctx, reservation.ID)
	if err != nil || !found || got.InFlight || got.Result == nil {
		t.Fatalf("GetTaskRun() = (%+v, %v, %v), want retained completed result", got, found, err)
	}
	if !reflect.DeepEqual(got.Result, want) {
		t.Fatalf("persisted result differs:\n got: %+v\nwant: %+v", got.Result, want)
	}
	retry, created, err := store.ReserveTaskRun(ctx, reservation)
	if err != nil || created || !reflect.DeepEqual(retry, got) {
		t.Fatalf("ReserveTaskRun(retry) = (%+v, %v, %v), want same completed record", retry, created, err)
	}
	_, _, err = store.ReserveTaskRun(ctx, TaskRunRecord{ID: reservation.ID, Fingerprint: "different-plan", InFlight: true})
	var conflict *TaskRunConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ReserveTaskRun(conflicting plan) error = %v, want TaskRunConflictError", err)
	}
}

func TestBoltTaskRunStoreInterruptedReservationSurvivesReopenWithoutReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "task-runs.db")
	store, err := OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reservation := TaskRunRecord{ID: "payment-1", Fingerprint: "plan-payment", InFlight: true}
	if _, created, err := store.ReserveTaskRun(ctx, reservation); err != nil || !created {
		t.Fatalf("ReserveTaskRun() created=%v err=%v, want new reservation", created, err)
	}
	plan := TaskRunPlan{SchemaVersion: 1, TargetID: "target-pay", Actions: []TaskRunRecoveryStep{{Action: "click", Selector: "#pay", Postcondition: "text"}}}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "super-secret") {
		t.Fatalf("portable task plan contains sensitive action input: %s", encoded)
	}
	recovery := TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: plan.Actions, Current: &TaskRunRecoveryCursor{Index: 0, Status: "started"}}
	initial := TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: plan.Actions}
	if err := store.BindTaskRunHandoff(ctx, reservation.ID, reservation.Fingerprint, "target-pay", "browser-pay", plan, initial); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTaskRunRecovery(ctx, reservation.ID, reservation.Fingerprint, recovery); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, found, err := store.GetTaskRun(ctx, reservation.ID)
	if err != nil || !found || !got.InFlight || got.TargetID != "target-pay" || got.BrowserInstanceID != "browser-pay" || got.Plan == nil || !reflect.DeepEqual(got.Plan, &plan) {
		t.Fatalf("GetTaskRun() = (%+v, %v, %v), want bound in-flight reservation", got, found, err)
	}
	if reflect.DeepEqual(got.Plan, &TaskRunPlan{Actions: []TaskRunRecoveryStep{{Action: "click", Selector: "#different-secret-value"}}}) {
		t.Fatal("portable task plan unexpectedly contains arbitrary action input")
	}
	_, created, err := store.ReserveTaskRun(ctx, TaskRunRecord{ID: reservation.ID, Fingerprint: "different-plan", InFlight: true})
	if created {
		t.Fatal("ReserveTaskRun() created a second reservation after reopen; this could replay side effects")
	}
	var conflict *TaskRunConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ReserveTaskRun() error = %v, want fail-closed TaskRunConflictError", err)
	}
}

func TestBoltTaskRunStoreRetainsUncertainResultAndAllowsHostResolution(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "uncertain-task.db")
	store, err := OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reservation := TaskRunRecord{ID: "final-readback-1", Fingerprint: "plan-final-readback", InFlight: true}
	if _, created, err := store.ReserveTaskRun(ctx, reservation); err != nil || !created {
		t.Fatalf("ReserveTaskRun() created=%v err=%v", created, err)
	}
	plan := TaskRunPlan{SchemaVersion: 1, TargetID: "target-1", Actions: []TaskRunRecoveryStep{{Action: "click", Selector: "#submit"}}}
	recovery := TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: plan.Actions}
	if err := store.BindTaskRunHandoff(ctx, reservation.ID, reservation.Fingerprint, "target-1", "browser-1", plan, recovery); err != nil {
		t.Fatal(err)
	}
	recovery.Phase = "final_verification"
	recovery.Current = &TaskRunRecoveryCursor{Index: 0, Status: "finished"}
	recovery.NextAction = 1
	if err := store.UpdateTaskRunRecovery(ctx, reservation.ID, reservation.Fingerprint, recovery); err != nil {
		t.Fatal(err)
	}
	want := TaskRunResult{ID: reservation.ID, Status: "needs_reconciliation", FailureReason: "verification_failed", TargetID: "target-1", BrowserInstanceID: "browser-1", PlanID: reservation.Fingerprint, ResumeFrom: 1, Batch: &BatchResult{Total: 1, NextActionIndex: 1}}
	if err := store.RetainUncertainTaskRun(ctx, reservation.ID, reservation.Fingerprint, want, "final postcondition not observed"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	got, found, err := store.GetTaskRun(ctx, reservation.ID)
	if err != nil || !found || !got.InFlight || got.Result == nil || got.Result.Status != "needs_reconciliation" || got.Result.FailureReason != "verification_failed" {
		t.Fatalf("GetTaskRun() = (%+v, %v, %v), want retained uncertain result and active replay guard", got, found, err)
	}
	if _, created, err := store.ReserveTaskRun(ctx, reservation); err != nil || created {
		t.Fatalf("same-ID retry created=%v err=%v, want existing reservation returned", created, err)
	}
	resolution := TaskRunResolution{Status: "verified_complete", Evidence: "ledger shows one commit", EvidenceSource: "fixture_ledger", EvidenceReference: "task-final-readback-1", ObservedAt: time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), ResolvedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := store.ResolveTaskRun(ctx, reservation.ID, reservation.Fingerprint, resolution); err != nil {
		t.Fatalf("ResolveTaskRun() after uncertain result: %v", err)
	}
	got, found, err = store.GetTaskRun(ctx, reservation.ID)
	if err != nil || !found || got.InFlight || got.Resolution == nil || got.Resolution.Status != "verified_complete" {
		t.Fatalf("resolved GetTaskRun() = (%+v, %v, %v), want host resolution", got, found, err)
	}
}

func TestBoltTaskRunStoreRejectedResolutionKeepsTaskIDReservedForNewHostAttempt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rejected-resolution.db")
	store, err := OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	original := TaskRunRecord{ID: "checkout-attempt-1", Fingerprint: "scout-plan-1", InFlight: true}
	if _, created, err := store.ReserveTaskRun(ctx, original); err != nil || !created {
		t.Fatalf("reserve original: created=%v err=%v", created, err)
	}
	steps := []TaskRunRecoveryStep{{Action: "click", Selector: "#submit"}}
	if err := store.BindTaskRunHandoff(ctx, original.ID, original.Fingerprint, "target-old", "browser-old",
		TaskRunPlan{SchemaVersion: 1, TargetID: "target-old", Actions: steps},
		TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: steps},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTaskRunRecovery(ctx, original.ID, original.Fingerprint, TaskRunRecovery{
		SchemaVersion: 1, Phase: "actions", Steps: steps, Current: &TaskRunRecoveryCursor{Index: 0, Status: "started"},
	}); err != nil {
		t.Fatal(err)
	}

	resolution := TaskRunResolution{
		Status: "rejected", Evidence: "authoritative ledger has no accepted operation",
		EvidenceSource: "fixture_ledger", EvidenceReference: "payment-key-1",
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), ResolvedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := store.ResolveTaskRun(ctx, original.ID, original.Fingerprint, resolution); err != nil {
		t.Fatalf("resolve original as rejected: %v", err)
	}
	got, found, err := store.GetTaskRun(ctx, original.ID)
	if err != nil || !found || got.InFlight || got.Resolution == nil || got.Resolution.Status != "rejected" {
		t.Fatalf("resolved record = (%+v, %v, %v), want terminal rejected resolution", got, found, err)
	}
	retained, created, err := store.ReserveTaskRun(ctx, original)
	if err != nil || created || retained.Resolution == nil || retained.Resolution.Status != "rejected" {
		t.Fatalf("same-ID reservation = (%+v, %v, %v), want retained rejection without new reservation", retained, created, err)
	}

	// The host may start a new Scout execution only under a new Scout task ID.
	// Its application idempotency key is a separate host concern and is retained
	// by the fixture ledger, not by Scout's task-ID replay registry.
	followup := TaskRunRecord{ID: "checkout-attempt-2", Fingerprint: "scout-plan-2", InFlight: true}
	if _, created, err := store.ReserveTaskRun(ctx, followup); err != nil || !created {
		t.Fatalf("reserve follow-up with new task ID: created=%v err=%v", created, err)
	}
}

func TestBoltSessionRecoveryRecordsSurviveReopenAndStayOwnerScoped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery.db")
	store, err := OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	want := SessionRecoveryRecord{
		SchemaVersion: 1, Handle: "0123456789abcdefghijklmnopqrstuv", OwnerScope: "owner-hash-a",
		SessionName: "checkout", BrowserInstanceID: "browser-fingerprint", TargetID: "target-7",
		PolicyDigest: "policy-digest", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.SaveSessionRecoveryRecord(ctx, want); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"remote_cdp", "token", "cookie", "page_content"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("session recovery record contains forbidden field %q: %s", forbidden, encoded)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenBoltTaskRunStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, found, err := store.GetSessionRecoveryRecord(ctx, want.Handle, "owner-hash-b"); err != nil || found {
		t.Fatalf("foreign owner lookup found=%v err=%v, want indistinguishable miss", found, err)
	}
	got, found, err := store.GetSessionRecoveryRecord(ctx, want.Handle, want.OwnerScope)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("owner lookup = (%+v, %v, %v), want persisted record", got, found, err)
	}
	changed := want
	changed.TargetID = "another-target"
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
	if err := store.SaveSessionRecoveryRecord(ctx, changed); err == nil {
		t.Fatal("SaveSessionRecoveryRecord accepted an existing handle; handles must be immutable")
	}
	got, found, err = store.GetSessionRecoveryRecord(ctx, want.Handle, want.OwnerScope)
	if err != nil || !found || got.TargetID != want.TargetID {
		t.Fatalf("duplicate save changed original record = (%+v, %v, %v), want target %q", got, found, err, want.TargetID)
	}
	if deleted, err := store.DeleteSessionRecoveryRecord(ctx, want.Handle, "owner-hash-b"); err != nil || deleted {
		t.Fatalf("foreign owner delete = (%v, %v), want no-op", deleted, err)
	}
	if _, found, err := store.GetSessionRecoveryRecord(ctx, want.Handle, want.OwnerScope); err != nil || !found {
		t.Fatalf("foreign delete removed owner record: found=%v err=%v", found, err)
	}
	if deleted, err := store.DeleteSessionRecoveryRecord(ctx, want.Handle, want.OwnerScope); err != nil || !deleted {
		t.Fatalf("owner delete = (%v, %v), want revoked record", deleted, err)
	}
}

func TestBoltSessionRecoveryRecordsExpireAndValidate(t *testing.T) {
	ctx := context.Background()
	store, err := OpenBoltTaskRunStore(filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	record := SessionRecoveryRecord{
		SchemaVersion: 1, Handle: "0123456789abcdefghijklmnopqrstuv", OwnerScope: "owner-hash",
		SessionName: "default", BrowserInstanceID: "browser", TargetID: "target", PolicyDigest: "policy",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := store.SaveSessionRecoveryRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteExpiredSessionRecoveryRecords(ctx, now.Add(2*time.Minute))
	if err != nil || deleted != 1 {
		t.Fatalf("expired cleanup = (%d, %v), want one record removed", deleted, err)
	}
	if _, found, err := store.GetSessionRecoveryRecord(ctx, record.Handle, record.OwnerScope); err != nil || found {
		t.Fatalf("expired lookup found=%v err=%v, want miss", found, err)
	}
	invalid := record
	invalid.SchemaVersion = 2
	if err := store.SaveSessionRecoveryRecord(ctx, invalid); err == nil {
		t.Fatal("SaveSessionRecoveryRecord accepted unsupported schema version")
	}
}

func TestBoltSessionRecoveryMalformedRecordIsNotEnumerable(t *testing.T) {
	ctx := context.Background()
	store, err := OpenBoltTaskRunStore(filepath.Join(t.TempDir(), "malformed-recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handle := "0123456789abcdefghijklmnopqrstuv"
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(sessionBoltBucket)).Put([]byte(handle), []byte("{"))
	}); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"owner-hash-a", "owner-hash-b"} {
		if _, found, err := store.GetSessionRecoveryRecord(ctx, handle, owner); err != nil || found {
			t.Fatalf("malformed lookup for %s = (found=%t, err=%v), want indistinguishable miss", owner, found, err)
		}
		if deleted, err := store.DeleteSessionRecoveryRecord(ctx, handle, owner); err != nil || deleted {
			t.Fatalf("malformed revoke for %s = (deleted=%t, err=%v), want indistinguishable no-op", owner, deleted, err)
		}
	}
	err = store.db.View(func(tx *bolt.Tx) error {
		if got := tx.Bucket([]byte(sessionBoltBucket)).Get([]byte(handle)); !bytes.Equal(got, []byte("{")) {
			t.Fatalf("malformed record unexpectedly changed during lookup/revoke: %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTaskRunRecoveryPlanIsRedactedAndValidated(t *testing.T) {
	actions := []BatchAction{{Action: "type", Selector: "#email", Value: "super-secret@example.com", ExpectedText: "Welcome super-secret@example.com"}}
	recovery := taskRunRecoveryManifest(actions, &Postcondition{Text: "Receipt for super-secret@example.com"}, "")
	plan := taskRunPlanFromRecovery(&recovery, "target-1", []string{"https://shop.example"}, false, "policy-digest", "session_page_policy", 15*time.Second)
	if err := ValidateTaskRunPlan(*plan); err != nil {
		t.Fatalf("ValidateTaskRunPlan() error = %v", err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret@example.com", "Welcome", "Receipt for"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("portable task plan contains secret %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"postcondition":"text"`) || !strings.Contains(string(encoded), `"selector":"#email"`) {
		t.Fatalf("portable plan lost action/postcondition shape: %s", encoded)
	}
	bad := *plan
	bad.SchemaVersion = 99
	if err := ValidateTaskRunPlan(bad); err == nil {
		t.Fatal("ValidateTaskRunPlan() accepted an unsupported schema")
	}
}
