package browse

import (
	"errors"
	"fmt"

	"go.klarlabs.de/scout/internal/cdp"
)

// ErrNoHistoryEntry is returned by GoBack/GoForward when there is no history
// entry in the requested direction.
var ErrNoHistoryEntry = errors.New("browse: no navigation history entry in that direction")

// URLPolicyError reports a stable policy denial for one URL. PolicyID is an
// opaque identity supplied by the caller; TargetID and EvidenceReference are
// populated by Page when the denial is tied to a browser target. It never
// authorizes access or retry.
type URLPolicyError struct {
	Code              string
	PolicyID          string
	TargetID          string
	EvidenceReference string
	Err               error
}

func (e *URLPolicyError) Error() string {
	if e == nil || e.Err == nil {
		return "browse: URL blocked by policy"
	}
	return e.Err.Error()
}

func (e *URLPolicyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// TimeoutError is returned when an operation exceeds its deadline.
type TimeoutError struct {
	Operation string
	Selector  string
}

func (e *TimeoutError) Error() string {
	if e.Selector != "" {
		return fmt.Sprintf("browse: timeout waiting for %s on %q", e.Operation, e.Selector)
	}
	return fmt.Sprintf("browse: timeout during %s", e.Operation)
}

// ElementNotFoundError is returned when a selector matches no elements.
type ElementNotFoundError struct {
	Selector string
}

func (e *ElementNotFoundError) Error() string {
	return fmt.Sprintf("browse: element not found: %q", e.Selector)
}

// NavigationError is returned when page navigation fails.
type NavigationError struct {
	URL string
	Err error
}

// NavigationOutcomeUnknownError indicates a navigation command may have been
// dispatched but its completion could not be confirmed.
type NavigationOutcomeUnknownError struct {
	URL string
	Err error
}

func (e *NavigationOutcomeUnknownError) Error() string {
	return fmt.Sprintf("browse: navigation outcome unknown for %q: %v", e.URL, e.Err)
}

func (e *NavigationOutcomeUnknownError) Unwrap() error { return e.Err }

// IsOutcomeUnknown reports whether err indicates that a browser command may
// have been dispatched without an authoritative completion response. Callers
// must inspect application state before replaying such an operation.
func IsOutcomeUnknown(err error) bool {
	var navigationErr *NavigationOutcomeUnknownError
	if errors.As(err, &navigationErr) {
		return true
	}
	var dispatchErr *cdp.DispatchOutcomeUnknownError
	return errors.As(err, &dispatchErr)
}

func (e *NavigationError) Error() string {
	var policyErr *URLPolicyError
	if errors.As(e.Err, &policyErr) {
		return fmt.Sprintf("browse: navigation denied by URL policy: %v", policyErr)
	}
	return fmt.Sprintf("browse: navigation to %q failed: %v", e.URL, e.Err)
}

func (e *NavigationError) Unwrap() error {
	return e.Err
}

// RateLimitError is returned when a task is rejected by the rate limiter.
type RateLimitError struct {
	TaskName string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("browse: rate limit exceeded for task %q", e.TaskName)
}

// CircuitOpenError is returned when the circuit breaker is open.
type CircuitOpenError struct {
	TaskName string
}

func (e *CircuitOpenError) Error() string {
	return fmt.Sprintf("browse: circuit open, task %q rejected", e.TaskName)
}

// BulkheadFullError is returned when the bulkhead is at capacity.
type BulkheadFullError struct {
	TaskName string
}

func (e *BulkheadFullError) Error() string {
	return fmt.Sprintf("browse: bulkhead full, task %q rejected", e.TaskName)
}
