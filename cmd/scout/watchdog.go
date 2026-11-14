package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	mcp "go.klarlabs.de/mcp"
	"go.klarlabs.de/mcp/protocol"
)

// defaultToolTimeout is the per-tool-call deadline applied to every
// MCP request when no override is configured. 60s is long enough for
// real navigations and screenshot capture but short enough that a
// wedged CDP session can't stall the calling agent indefinitely.
const defaultToolTimeout = 60 * time.Second

type sessionStartFailure struct{ err error }

func (e sessionStartFailure) Error() string {
	return fmt.Sprintf("failed to create browser session: %v", e.err)
}

// resolveToolTimeout reads SCOUT_TOOL_TIMEOUT and falls back to the
// default. Values are parsed with time.ParseDuration. Empty, malformed,
// or non-positive values fall through to the default.
func resolveToolTimeout() time.Duration {
	raw := os.Getenv("SCOUT_TOOL_TIMEOUT")
	if raw == "" {
		return defaultToolTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultToolTimeout
	}
	return d
}

const defaultSessionIdleTimeout = time.Hour

// resolveSessionIdleTimeout reads SCOUT_SESSION_IDLE_TIMEOUT. Idle sessions
// expire after one hour by default so abandoned owned browsers are reclaimed.
// Set the value to "off" to retain sessions until explicit close or shutdown.
// Invalid values are rejected so a typo cannot silently disable cleanup.
func resolveSessionIdleTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("SCOUT_SESSION_IDLE_TIMEOUT"))
	if raw == "" {
		return defaultSessionIdleTimeout, nil
	}
	if strings.EqualFold(raw, "off") {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("SCOUT_SESSION_IDLE_TIMEOUT must be a positive duration (for example 30m): %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("SCOUT_SESSION_IDLE_TIMEOUT must be a positive duration (for example 30m)")
	}
	return d, nil
}

// resolveSessionLimit reads SCOUT_MAX_SESSIONS. A non-positive or malformed
// configured value is rejected so a typo cannot silently disable the cap.
func resolveSessionLimit() (int, error) {
	raw := strings.TrimSpace(os.Getenv("SCOUT_MAX_SESSIONS"))
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("SCOUT_MAX_SESSIONS must be a positive integer: %w", err)
	}
	if limit <= 0 {
		return 0, fmt.Errorf("SCOUT_MAX_SESSIONS must be a positive integer")
	}
	return limit, nil
}

type sessionLimitReachedError struct{ limit int }

func (e sessionLimitReachedError) Error() string {
	return fmt.Sprintf("session limit of %d reached; close or allow an idle session to expire before creating another", e.limit)
}

func sessionLimitError(limit int) error { return sessionLimitReachedError{limit: limit} }

// watchdogMiddleware guards every tool call with a deadline and
// guarantees the MCP RPC returns even if the underlying handler
// hangs (broken CDP session, wedged page, slow eval). On timeout we
// surface a structured error envelope so the LLM caller can decide
// whether to retry, reset, or back off — instead of stalling.
//
// The handler runs in its own goroutine. If it overruns the budget
// we abandon it (it continues until ctx.Done() or it returns) and
// reply to the JSON-RPC request immediately. This is the behavior
// requested in issue #15.
func watchdogMiddleware(budget time.Duration) mcp.Middleware {
	return func(next mcp.MiddlewareHandlerFunc) mcp.MiddlewareHandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			// Notifications have no ID — pass through unchanged.
			if req == nil || req.IsNotification() {
				return next(ctx, req)
			}

			ctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()

			type result struct {
				resp *protocol.Response
				err  error
			}
			done := make(chan result, 1)
			go func() {
				// Recover any handler panic (e.g. the session helper panics when
				// Chrome can't launch) into a structured error, so one bad tool
				// call can't crash the whole MCP server. The buffered channel keeps
				// this non-blocking even if the watchdog already timed out and
				// abandoned us.
				defer func() {
					if r := recover(); r != nil {
						panicText := fmt.Sprint(r)
						code := "SCOUT_PANIC"
						cause := "panic"
						hint := "The handler failed unexpectedly and browser effects may be uncertain. Inspect browser or authoritative application state before retrying; for task_run, use recover_task_run and reconcile before any follow-up action. Reset the session only after confirming no handler work remains."
						var startFailure sessionStartFailure
						panicErr, _ := r.(error)
						hasStartFailure := panicErr != nil && errors.As(panicErr, &startFailure)
						var limitFailure sessionLimitReachedError
						switch {
						case hasStartFailure && errors.As(startFailure.err, &limitFailure):
							code, cause = "SCOUT_SESSION_LIMIT", "session_limit"
							hint = "Close an unused session or wait for an idle session to expire, then retry."
						case hasStartFailure && errors.Is(startFailure.err, context.Canceled):
							code, cause = "SCOUT_CANCELED", "canceled"
							hint = "The request was canceled while the browser session was starting; retry when ready."
						case hasStartFailure && errors.Is(startFailure.err, context.DeadlineExceeded):
							code, cause = "SCOUT_TIMEOUT", "timeout"
							hint = "Browser session startup exceeded the request deadline; retry or configure a longer SCOUT_TOOL_TIMEOUT."
						case hasStartFailure:
							code, cause = "SCOUT_BROWSER_START_FAILED", "browser_start_failed"
							hint = "Install Chrome or Chromium and ensure its executable is on PATH, or set SCOUT_REMOTE_CDP to a trusted browser DevTools WebSocket URL."
						case strings.HasPrefix(panicText, "failed to create browser session:"):
							code = "SCOUT_BROWSER_START_FAILED"
							cause = "browser_start_failed"
							hint = "Install Chrome or Chromium and ensure its executable is on PATH, or set SCOUT_REMOTE_CDP to a trusted browser DevTools WebSocket URL."
						}
						env := MCPErrorEnvelope{
							Code:    code,
							Message: fmt.Sprintf("tool %q failed: %v", extractToolName(req), r),
							Phase:   "handler",
							Cause:   cause,
							Detail:  fmt.Sprintf("method=%s", req.Method),
							Hint:    hint,
						}
						payload, _ := json.Marshal(env)
						done <- result{resp: protocol.NewErrorResponse(req.ID, protocol.NewInternalError(string(payload))), err: nil}
					}
				}()
				resp, err := next(ctx, req)
				done <- result{resp: resp, err: err}
			}()

			select {
			case r := <-done:
				return r.resp, r.err
			case <-ctx.Done():
				// ctx.Err() will be DeadlineExceeded here (Canceled
				// only fires on outer cancellation, which is also
				// surfaced as a timeout from the agent's POV).
				toolName := extractToolName(req)
				env := MCPErrorEnvelope{
					Code:    "SCOUT_TIMEOUT",
					Message: fmt.Sprintf("tool %q exceeded watchdog budget of %s", toolName, budget),
					Phase:   "watchdog",
					Cause:   "timeout",
					Detail:  fmt.Sprintf("method=%s ctx=%s", req.Method, ctx.Err()),
					Hint:    "The handler may still be unwinding, and a mutation may have taken effect. Do not reset or retry solely because this call timed out. Inspect the browser or authoritative application state first; for task_run, use recover_task_run and reconcile before any follow-up action. If this recurs, review the timeout and browser responsiveness.",
				}
				payload, _ := json.Marshal(env)
				return protocol.NewErrorResponse(
					req.ID,
					protocol.NewInternalError(string(payload)),
				), nil
			}
		}
	}
}

// extractToolName pulls the tool name from a tools/call request so
// the timeout envelope can name the offending tool. Falls back to
// the JSON-RPC method when the request isn't a tools/call.
func extractToolName(req *protocol.Request) string {
	if req == nil {
		return ""
	}
	if req.Method != protocol.MethodToolsCall {
		return req.Method
	}
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(req.Params, &params); err == nil && params.Name != "" {
		return params.Name
	}
	return req.Method
}
