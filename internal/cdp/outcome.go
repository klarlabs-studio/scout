package cdp

import "fmt"

// DispatchOutcomeUnknownError means a command was written to the WebSocket but
// no authoritative CDP response was received. The browser may have executed it.
// A CDP protocol error response is returned directly instead because it
// confirms the command was rejected.
type DispatchOutcomeUnknownError struct {
	Method string
	Err    error
}

func (e *DispatchOutcomeUnknownError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("cdp: outcome unknown for %s: %v", e.Method, e.Err)
}

func (e *DispatchOutcomeUnknownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
