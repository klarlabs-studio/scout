package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	mcp "github.com/felixgeelhaar/mcp-go"
	"github.com/felixgeelhaar/mcp-go/protocol"
)

// defaultToolTimeout is the per-tool-call deadline applied to every
// MCP request when no override is configured. 60s is long enough for
// real navigations and screenshot capture but short enough that a
// wedged CDP session can't stall the calling agent indefinitely.
const defaultToolTimeout = 60 * time.Second

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
					Hint:    "Try `configure { fresh: true }` to reset the browser session, then retry the action. If this recurs, raise SCOUT_TOOL_TIMEOUT.",
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
