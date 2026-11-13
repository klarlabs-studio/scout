package main

import (
	"context"
	"path/filepath"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func TestScopedTaskRunStoreIsolatesMCPOwners(t *testing.T) {
	ctx := context.Background()
	base, err := agent.OpenBoltTaskRunStore(filepath.Join(t.TempDir(), "task-runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ownerA := scopedTaskRunStore{store: base, namespace: "principal/a/session/default"}
	ownerB := scopedTaskRunStore{store: base, namespace: "principal/b/session/default"}
	reservation := agent.TaskRunRecord{ID: "same-caller-id", Fingerprint: "same-plan", InFlight: true}
	if _, created, err := ownerA.ReserveTaskRun(ctx, reservation); err != nil || !created {
		t.Fatalf("owner A reservation: created=%v err=%v", created, err)
	}
	if err := ownerA.BindTaskRunTarget(ctx, reservation.ID, reservation.Fingerprint, "target-a", "browser-a"); err != nil {
		t.Fatal(err)
	}
	completed := agent.TaskRunRecord{
		ID: reservation.ID, Fingerprint: reservation.Fingerprint, TargetID: "target-a", BrowserInstanceID: "browser-a",
		Result: &agent.TaskRunResult{ID: reservation.ID, Status: "completed", TargetID: "target-a", BrowserInstanceID: "browser-a", PlanID: reservation.Fingerprint, Batch: &agent.BatchResult{}},
	}
	if err := ownerA.CompleteTaskRun(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if got, found, err := ownerB.GetTaskRun(ctx, reservation.ID); err != nil || found {
		t.Fatalf("owner B read owner A result: found=%v record=%+v err=%v", found, got, err)
	}
	if _, created, err := ownerB.ReserveTaskRun(ctx, reservation); err != nil || !created {
		t.Fatalf("owner B reservation: created=%v err=%v, want independent reservation", created, err)
	}
	retried, created, err := ownerA.ReserveTaskRun(ctx, reservation)
	if err != nil || created || retried.Result == nil || retried.Result.TargetID != "target-a" {
		t.Fatalf("owner A retry: created=%v record=%+v err=%v, want its own completed result", created, retried, err)
	}
}
