package browse

import (
	"errors"
	"testing"

	"go.klarlabs.de/statekit"
)

func TestTaskLifecycleHappyPath(t *testing.T) {
	tracker, err := NewTaskTracker("test-task")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	if tracker.State() != StatePending {
		t.Errorf("expected pending, got %s", tracker.State())
	}

	tracker.Start()
	if tracker.State() != StateRunning {
		t.Errorf("expected running, got %s", tracker.State())
	}
	if tracker.Context().Attempt != 1 {
		t.Errorf("expected attempt=1, got %d", tracker.Context().Attempt)
	}

	tracker.Success()
	if tracker.State() != StateSuccess {
		t.Errorf("expected success, got %s", tracker.State())
	}
	if !tracker.IsDone() {
		t.Error("expected done after success")
	}
}

func TestTaskLifecycleFailAndReset(t *testing.T) {
	tracker, err := NewTaskTracker("fail-task")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	tracker.Start()
	testErr := errors.New("something broke")
	tracker.Fail(testErr)

	if tracker.State() != StateFailed {
		t.Errorf("expected failed, got %s", tracker.State())
	}
	if tracker.Context().LastErr == nil || tracker.Context().LastErr.Error() != "something broke" {
		t.Errorf("expected error to be recorded, got %v", tracker.Context().LastErr)
	}

	tracker.Reset()
	if tracker.State() != StatePending {
		t.Errorf("expected pending after reset, got %s", tracker.State())
	}
}

func TestTaskLifecycleRetry(t *testing.T) {
	tracker, err := NewTaskTracker("retry-task")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	tracker.Start()
	if tracker.Context().Attempt != 1 {
		t.Errorf("expected attempt=1, got %d", tracker.Context().Attempt)
	}

	tracker.Retry()
	if tracker.State() != StateRetrying {
		t.Errorf("expected retrying, got %s", tracker.State())
	}

	tracker.Start()
	if tracker.State() != StateRunning {
		t.Errorf("expected running, got %s", tracker.State())
	}
	if tracker.Context().Attempt != 2 {
		t.Errorf("expected attempt=2, got %d", tracker.Context().Attempt)
	}

	tracker.Success()
	if !tracker.IsDone() {
		t.Error("expected done after success on retry")
	}
}

func TestTaskLifecycleAbort(t *testing.T) {
	tracker, err := NewTaskTracker("abort-task")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	tracker.Start()
	tracker.Abort()

	if tracker.State() != StateAborted {
		t.Errorf("expected aborted, got %s", tracker.State())
	}
	if !tracker.IsDone() {
		t.Error("expected done after abort")
	}
}

func TestTaskLifecycleMatches(t *testing.T) {
	tracker, err := NewTaskTracker("matches-task")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	if !tracker.Matches(StatePending) {
		t.Error("expected to match pending")
	}
	if tracker.Matches(StateRunning) {
		t.Error("should not match running")
	}
}

func TestTaskLifecycleContextTaskName(t *testing.T) {
	tracker, err := NewTaskTracker("my-task")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	ctx := tracker.Context()
	if ctx.TaskName != "my-task" {
		t.Errorf("TaskName = %q, want %q", ctx.TaskName, "my-task")
	}
}

func TestTaskLifecycleMultipleRetries(t *testing.T) {
	tracker, err := NewTaskTracker("multi-retry")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	tracker.Start()
	tracker.Retry()
	tracker.Start()
	tracker.Retry()
	tracker.Start()

	if tracker.Context().Attempt != 3 {
		t.Errorf("expected attempt=3, got %d", tracker.Context().Attempt)
	}

	tracker.Success()
	if !tracker.IsDone() {
		t.Error("should be done after success")
	}
}

func TestTaskLifecycleAbortIsFinal(t *testing.T) {
	tracker, err := NewTaskTracker("abort-final")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	tracker.Start()
	tracker.Abort()

	if !tracker.IsDone() {
		t.Error("aborted should be a final state")
	}
	if !tracker.Matches(StateAborted) {
		t.Error("should match StateAborted")
	}
}

func TestTaskLifecycleSuccessIsFinal(t *testing.T) {
	tracker, err := NewTaskTracker("success-final")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	tracker.Start()
	tracker.Success()

	if !tracker.IsDone() {
		t.Error("success should be a final state")
	}
	if !tracker.Matches(StateSuccess) {
		t.Error("should match StateSuccess")
	}
}

func TestTaskLifecycleNotDoneWhileRunning(t *testing.T) {
	tracker, err := NewTaskTracker("running")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	tracker.Start()
	if tracker.IsDone() {
		t.Error("running should not be done")
	}
}

func TestTaskLifecycleNotDoneWhilePending(t *testing.T) {
	tracker, err := NewTaskTracker("pending")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	if tracker.IsDone() {
		t.Error("pending should not be done")
	}
}

func TestTaskLifecycleContextInitialValues(t *testing.T) {
	tracker, err := NewTaskTracker("init-ctx")
	if err != nil {
		t.Fatalf("NewTaskTracker: %v", err)
	}
	defer tracker.Stop()

	ctx := tracker.Context()
	if ctx.Attempt != 0 {
		t.Errorf("initial Attempt = %d, want 0", ctx.Attempt)
	}
	if ctx.LastErr != nil {
		t.Errorf("initial LastErr = %v, want nil", ctx.LastErr)
	}
}

func TestNewTaskLifecycleReturnsValidConfig(t *testing.T) {
	cfg, err := NewTaskLifecycle("test")
	if err != nil {
		t.Fatalf("NewTaskLifecycle: %v", err)
	}
	if cfg == nil {
		t.Fatal("NewTaskLifecycle returned nil config")
	}
}

func TestEventAndStateConstants(t *testing.T) {
	events := []statekit.EventType{EventStart, EventSuccess, EventFail, EventRetry, EventAbort, EventReset}
	for _, e := range events {
		if e == "" {
			t.Error("event type should not be empty")
		}
	}

	states := []statekit.StateID{StatePending, StateRunning, StateSuccess, StateFailed, StateAborted, StateRetrying}
	for _, s := range states {
		if s == "" {
			t.Error("state ID should not be empty")
		}
	}

	unique := make(map[statekit.StateID]bool)
	for _, s := range states {
		if unique[s] {
			t.Errorf("duplicate state: %s", s)
		}
		unique[s] = true
	}
}
