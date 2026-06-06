package browse

import (
	"go.klarlabs.de/statekit"
)

// Task lifecycle events
const (
	EventStart   statekit.EventType = "START"
	EventSuccess statekit.EventType = "SUCCESS"
	EventFail    statekit.EventType = "FAIL"
	EventRetry   statekit.EventType = "RETRY"
	EventAbort   statekit.EventType = "ABORT"
	EventReset   statekit.EventType = "RESET"
)

// Task lifecycle states
const (
	StatePending  statekit.StateID = "pending"
	StateRunning  statekit.StateID = "running"
	StateSuccess  statekit.StateID = "success"
	StateFailed   statekit.StateID = "failed"
	StateAborted  statekit.StateID = "aborted"
	StateRetrying statekit.StateID = "retrying"
)

// TaskLifecycleContext holds the context for a task's state machine.
type TaskLifecycleContext struct {
	TaskName string
	Attempt  int
	LastErr  error
}

// NewTaskLifecycle creates a statekit machine modeling the task execution lifecycle.
//
//	pending → START → running
//	running → SUCCESS → success (final)
//	running → FAIL → failed (final)
//	running → ABORT → aborted (final)
//	running → RETRY → retrying
//	retrying → START → running
//	failed → RESET → pending
func NewTaskLifecycle(taskName string) (*statekit.MachineConfig[TaskLifecycleContext], error) {
	return statekit.NewMachine[TaskLifecycleContext]("task-lifecycle").
		WithInitial(StatePending).
		WithContext(TaskLifecycleContext{TaskName: taskName}).
		WithAction("incrementAttempt", func(ctx *TaskLifecycleContext, event statekit.Event) {
			ctx.Attempt++
		}).
		WithAction("recordError", func(ctx *TaskLifecycleContext, event statekit.Event) {
			if err, ok := event.Payload.(error); ok {
				ctx.LastErr = err
			}
		}).
		// pending: waiting to be executed
		State(StatePending).
		On(EventStart).Target(StateRunning).Do("incrementAttempt").Done().
		// running: actively executing
		State(StateRunning).
		On(EventSuccess).Target(StateSuccess).
		On(EventFail).Target(StateFailed).Do("recordError").
		On(EventAbort).Target(StateAborted).
		On(EventRetry).Target(StateRetrying).Done().
		// retrying: waiting before re-execution
		State(StateRetrying).
		On(EventStart).Target(StateRunning).Do("incrementAttempt").Done().
		// terminal states
		State(StateSuccess).Final().Done().
		State(StateFailed).
		On(EventReset).Target(StatePending).Done().
		State(StateAborted).Final().Done().
		Build()
}

// TaskTracker wraps a statekit Interpreter to track task execution state.
type TaskTracker struct {
	interp *statekit.Interpreter[TaskLifecycleContext]
}

// NewTaskTracker creates a tracker for the given task name.
func NewTaskTracker(taskName string) (*TaskTracker, error) {
	machine, err := NewTaskLifecycle(taskName)
	if err != nil {
		return nil, err
	}
	interp := statekit.NewInterpreter(machine)
	interp.Start()
	return &TaskTracker{interp: interp}, nil
}

// Start transitions the task to running state.
func (t *TaskTracker) Start() {
	t.interp.Send(statekit.Event{Type: EventStart})
}

// Success transitions the task to success state.
func (t *TaskTracker) Success() {
	t.interp.Send(statekit.Event{Type: EventSuccess})
}

// Fail transitions the task to failed state with an error.
func (t *TaskTracker) Fail(err error) {
	t.interp.Send(statekit.Event{Type: EventFail, Payload: err})
}

// Abort transitions the task to aborted state.
func (t *TaskTracker) Abort() {
	t.interp.Send(statekit.Event{Type: EventAbort})
}

// Retry transitions the task to retrying state.
func (t *TaskTracker) Retry() {
	t.interp.Send(statekit.Event{Type: EventRetry})
}

// Reset transitions a failed task back to pending.
func (t *TaskTracker) Reset() {
	t.interp.Send(statekit.Event{Type: EventReset})
}

// State returns the current state ID.
func (t *TaskTracker) State() statekit.StateID {
	return t.interp.State().Value
}

// Context returns the current task lifecycle context.
func (t *TaskTracker) Context() TaskLifecycleContext {
	return t.interp.State().Context
}

// IsDone returns true if the task is in a terminal state.
func (t *TaskTracker) IsDone() bool {
	return t.interp.Done()
}

// Matches checks if the task is in the given state.
func (t *TaskTracker) Matches(state statekit.StateID) bool {
	return t.interp.Matches(state)
}

// Stop cleans up the interpreter.
func (t *TaskTracker) Stop() {
	t.interp.Stop()
}
