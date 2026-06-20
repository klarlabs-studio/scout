package browse

import (
	"errors"
	"fmt"
)

// ErrNoHistoryEntry is returned by GoBack/GoForward when there is no history
// entry in the requested direction.
var ErrNoHistoryEntry = errors.New("browse: no navigation history entry in that direction")

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

func (e *NavigationError) Error() string {
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
