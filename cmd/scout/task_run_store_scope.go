package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"go.klarlabs.de/scout/agent"
)

// scopedTaskRunStore partitions a shared durable store by MCP owner and named
// session. IDs and stored results are rewritten internally so task receipts
// from one MCP client cannot be returned to another client that chose the same
// caller ID and plan.
type scopedTaskRunStore struct {
	store     agent.TaskRunStore
	namespace string
}

func (s scopedTaskRunStore) storageID(id string) string {
	digest := sha256.Sum256([]byte(s.namespace))
	return "scope:" + hex.EncodeToString(digest[:]) + ":" + id
}

func (s scopedTaskRunStore) ReserveTaskRun(ctx context.Context, proposed agent.TaskRunRecord) (agent.TaskRunRecord, bool, error) {
	originalID := proposed.ID
	proposed.ID = s.storageID(originalID)
	record, created, err := s.store.ReserveTaskRun(ctx, proposed)
	if err != nil {
		return agent.TaskRunRecord{}, false, s.externalizeError(err, originalID)
	}
	record.ID = originalID
	if record.Result != nil {
		record.Result = cloneTaskResultWithID(record.Result, originalID)
	}
	return record, created, nil
}

func (s scopedTaskRunStore) BindTaskRunTarget(ctx context.Context, id, fingerprint, targetID, browserInstanceID string) error {
	return s.externalizeError(s.store.BindTaskRunTarget(ctx, s.storageID(id), fingerprint, targetID, browserInstanceID), id)
}

func (s scopedTaskRunStore) UpdateTaskRunPlan(ctx context.Context, id, fingerprint string, plan agent.TaskRunPlan) error {
	store, ok := s.store.(agent.TaskRunPlanStore)
	if !ok {
		return nil
	}
	return s.externalizeError(store.UpdateTaskRunPlan(ctx, s.storageID(id), fingerprint, plan), id)
}

func (s scopedTaskRunStore) BindTaskRunHandoff(ctx context.Context, id, fingerprint, targetID, browserInstanceID string, plan agent.TaskRunPlan, recovery agent.TaskRunRecovery) error {
	if store, ok := s.store.(agent.TaskRunHandoffStore); ok {
		return s.externalizeError(store.BindTaskRunHandoff(ctx, s.storageID(id), fingerprint, targetID, browserInstanceID, plan, recovery), id)
	}
	if err := s.BindTaskRunTarget(ctx, id, fingerprint, targetID, browserInstanceID); err != nil {
		return err
	}
	if err := s.UpdateTaskRunPlan(ctx, id, fingerprint, plan); err != nil {
		return err
	}
	return s.UpdateTaskRunRecovery(ctx, id, fingerprint, recovery)
}

func (s scopedTaskRunStore) UpdateTaskRunRecovery(ctx context.Context, id, fingerprint string, recovery agent.TaskRunRecovery) error {
	store, ok := s.store.(agent.TaskRunProgressStore)
	if !ok {
		return nil
	}
	return s.externalizeError(store.UpdateTaskRunRecovery(ctx, s.storageID(id), fingerprint, recovery), id)
}

func (s scopedTaskRunStore) CompleteTaskRun(ctx context.Context, record agent.TaskRunRecord) error {
	originalID := record.ID
	record.ID = s.storageID(originalID)
	if record.Result != nil {
		record.Result = cloneTaskResultWithID(record.Result, record.ID)
	}
	return s.externalizeError(s.store.CompleteTaskRun(ctx, record), originalID)
}

func (s scopedTaskRunStore) RetainUncertainTaskRun(ctx context.Context, id, fingerprint string, result agent.TaskRunResult, failure string) error {
	store, ok := s.store.(agent.TaskRunUncertainResultStore)
	if !ok {
		return errors.New("underlying task run store does not support uncertain-result retention")
	}
	storedResult := cloneTaskResultWithID(&result, s.storageID(id))
	return s.externalizeError(store.RetainUncertainTaskRun(ctx, s.storageID(id), fingerprint, *storedResult, failure), id)
}

func (s scopedTaskRunStore) ResolveTaskRun(ctx context.Context, id, fingerprint string, resolution agent.TaskRunResolution) error {
	store, ok := s.store.(agent.TaskRunResolutionStore)
	if !ok {
		return errors.New("underlying task run store does not support interrupted-task resolution")
	}
	return s.externalizeError(store.ResolveTaskRun(ctx, s.storageID(id), fingerprint, resolution), id)
}

func (s scopedTaskRunStore) ReleaseTaskRun(ctx context.Context, id, fingerprint string) error {
	return s.externalizeError(s.store.ReleaseTaskRun(ctx, s.storageID(id), fingerprint), id)
}

func (s scopedTaskRunStore) GetTaskRun(ctx context.Context, id string) (agent.TaskRunRecord, bool, error) {
	record, found, err := s.store.GetTaskRun(ctx, s.storageID(id))
	if err != nil || !found {
		return agent.TaskRunRecord{}, found, err
	}
	record.ID = id
	if record.Result != nil {
		record.Result = cloneTaskResultWithID(record.Result, id)
	}
	return record, true, nil
}

func (s scopedTaskRunStore) externalizeError(err error, id string) error {
	var conflict *agent.TaskRunConflictError
	if errors.As(err, &conflict) {
		return &agent.TaskRunConflictError{TaskID: id, Reason: conflict.Reason}
	}
	return err
}

func cloneTaskResultWithID(result *agent.TaskRunResult, id string) *agent.TaskRunResult {
	if result == nil {
		return nil
	}
	copy := *result
	copy.ID = id
	return &copy
}

var _ agent.TaskRunStore = scopedTaskRunStore{}
var _ agent.TaskRunResolutionStore = scopedTaskRunStore{}
var _ agent.TaskRunProgressStore = scopedTaskRunStore{}
var _ agent.TaskRunPlanStore = scopedTaskRunStore{}
var _ agent.TaskRunHandoffStore = scopedTaskRunStore{}
var _ agent.TaskRunUncertainResultStore = scopedTaskRunStore{}
