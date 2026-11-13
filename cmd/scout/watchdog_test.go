package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mcp/protocol"
)

func TestResolveToolTimeout_default(t *testing.T) {
	t.Setenv("SCOUT_TOOL_TIMEOUT", "")
	if got := resolveToolTimeout(); got != defaultToolTimeout {
		t.Errorf("default = %s, want %s", got, defaultToolTimeout)
	}
}

func TestResolveToolTimeout_override(t *testing.T) {
	t.Setenv("SCOUT_TOOL_TIMEOUT", "5s")
	if got := resolveToolTimeout(); got != 5*time.Second {
		t.Errorf("override = %s, want 5s", got)
	}
}

func TestResolveToolTimeout_malformed(t *testing.T) {
	t.Setenv("SCOUT_TOOL_TIMEOUT", "definitely-not-a-duration")
	if got := resolveToolTimeout(); got != defaultToolTimeout {
		t.Errorf("malformed fell through to %s, want default %s", got, defaultToolTimeout)
	}
}

func TestResolveToolTimeout_negative(t *testing.T) {
	t.Setenv("SCOUT_TOOL_TIMEOUT", "-1s")
	if got := resolveToolTimeout(); got != defaultToolTimeout {
		t.Errorf("negative fell through to %s, want default %s", got, defaultToolTimeout)
	}
}

func TestResolveSessionIdleTimeout(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr string
	}{
		{name: "disabled by default", value: "", want: 0},
		{name: "valid", value: "30m", want: 30 * time.Minute},
		{name: "malformed", value: "30 minutes", wantErr: "SCOUT_SESSION_IDLE_TIMEOUT"},
		{name: "zero", value: "0s", wantErr: "SCOUT_SESSION_IDLE_TIMEOUT"},
		{name: "negative", value: "-1s", wantErr: "SCOUT_SESSION_IDLE_TIMEOUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SCOUT_SESSION_IDLE_TIMEOUT", tt.value)
			got, err := resolveSessionIdleTimeout()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want an error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSessionIdleTimeout: %v", err)
			}
			if got != tt.want {
				t.Errorf("timeout = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestResolveSessionLimit(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{name: "disabled by default", value: "", want: 0},
		{name: "valid", value: "12", want: 12},
		{name: "malformed", value: "twelve", wantErr: "SCOUT_MAX_SESSIONS"},
		{name: "zero", value: "0", wantErr: "SCOUT_MAX_SESSIONS"},
		{name: "negative", value: "-1", wantErr: "SCOUT_MAX_SESSIONS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SCOUT_MAX_SESSIONS", tt.value)
			got, err := resolveSessionLimit()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want an error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSessionLimit: %v", err)
			}
			if got != tt.want {
				t.Errorf("session limit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunMCPRejectsInvalidSessionLifecycleConfiguration(t *testing.T) {
	tests := []struct {
		name string
		key  string
		val  string
	}{
		{name: "idle timeout", key: "SCOUT_SESSION_IDLE_TIMEOUT", val: "soon"},
		{name: "session cap", key: "SCOUT_MAX_SESSIONS", val: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SCOUT_SESSION_IDLE_TIMEOUT", "")
			t.Setenv("SCOUT_MAX_SESSIONS", "")
			t.Setenv(tt.key, tt.val)
			err := runMCP("", "core", "")
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("runMCP error = %v, want an error containing %q", err, tt.key)
			}
		})
	}
}

func TestWatchdogMiddleware_passesThroughFastHandler(t *testing.T) {
	mw := watchdogMiddleware(500 * time.Millisecond)
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"observe"}`),
	}
	want := protocol.NewResponse(req.ID, "ok")
	handler := mw(func(ctx context.Context, r *protocol.Request) (*protocol.Response, error) {
		return want, nil
	})
	resp, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp != want {
		t.Errorf("resp = %+v, want %+v", resp, want)
	}
}

func TestWatchdogMiddleware_timesOutSlowHandler(t *testing.T) {
	mw := watchdogMiddleware(50 * time.Millisecond)
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"navigate"}`),
	}
	handler := mw(func(ctx context.Context, r *protocol.Request) (*protocol.Response, error) {
		<-ctx.Done() // simulate handler that honors ctx
		return nil, ctx.Err()
	})

	start := time.Now()
	resp, err := handler(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("middleware should swallow the timeout and return a response, got err=%v", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("watchdog took %s — should fire near 50ms", elapsed)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("expected error response with timeout envelope, got %+v", resp)
	}
	msg := resp.Error.Message
	if !strings.Contains(msg, "SCOUT_TIMEOUT") {
		t.Errorf("error message lacks SCOUT_TIMEOUT code: %q", msg)
	}
	if !strings.Contains(msg, "navigate") {
		t.Errorf("error message lacks tool name: %q", msg)
	}
	if !strings.Contains(msg, "mutation may have taken effect") || !strings.Contains(msg, "recover_task_run") || !strings.Contains(msg, "Do not reset or retry") {
		t.Errorf("timeout guidance does not preserve mutation uncertainty: %q", msg)
	}
}

func TestWatchdogMiddleware_doesNotBlockOnRunawayHandler(t *testing.T) {
	mw := watchdogMiddleware(30 * time.Millisecond)
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"observe"}`),
	}
	// Handler ignores ctx — the entire point of issue #15 is that
	// scout MCP must still return promptly even if the underlying
	// CDP call hangs without honoring context cancellation.
	handler := mw(func(ctx context.Context, r *protocol.Request) (*protocol.Response, error) {
		time.Sleep(2 * time.Second)
		return protocol.NewResponse(r.ID, "too-late"), nil
	})

	start := time.Now()
	resp, err := handler(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("middleware returned err=%v", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("watchdog blocked on runaway handler for %s — should not exceed budget", elapsed)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("expected error response, got %+v", resp)
	}
}

func TestWatchdogMiddleware_recoversHandlerPanic(t *testing.T) {
	mw := watchdogMiddleware(500 * time.Millisecond)
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"navigate"}`),
	}
	// A handler that panics (as the session helper does when Chrome can't
	// launch) must not crash the server: the middleware recovers it into a
	// structured error. Without recovery this test would abort the process.
	handler := mw(func(_ context.Context, _ *protocol.Request) (*protocol.Response, error) {
		panic("failed to create browser session: chrome not found")
	})

	resp, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("middleware should recover the panic and return a response, got err=%v", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("expected an error response for a panicking handler, got %+v", resp)
	}
	msg := resp.Error.Message
	if !strings.Contains(msg, "SCOUT_BROWSER_START_FAILED") {
		t.Errorf("error message lacks SCOUT_BROWSER_START_FAILED code: %q", msg)
	}
	if !strings.Contains(msg, "navigate") {
		t.Errorf("error message lacks tool name: %q", msg)
	}
	if !strings.Contains(msg, "SCOUT_REMOTE_CDP") {
		t.Errorf("error message lacks remote-browser recovery guidance: %q", msg)
	}
}

func TestWatchdogMiddlewareClassifiesSessionStartupContextErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		code  string
		cause string
	}{
		{name: "canceled", err: context.Canceled, code: "SCOUT_CANCELED", cause: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, code: "SCOUT_TIMEOUT", cause: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := watchdogMiddleware(time.Second)
			req := &protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"observe"}`)}
			handler := mw(func(context.Context, *protocol.Request) (*protocol.Response, error) {
				panic(sessionStartFailure{err: tt.err})
			})
			resp, err := handler(context.Background(), req)
			if err != nil || resp == nil || resp.Error == nil {
				t.Fatalf("handler response = (%+v, %v), want structured error", resp, err)
			}
			if !strings.Contains(resp.Error.Message, tt.code) || !strings.Contains(resp.Error.Message, tt.cause) {
				t.Fatalf("error message = %q, want code %s and cause %s", resp.Error.Message, tt.code, tt.cause)
			}
		})
	}
}

func TestWatchdogMiddleware_classifiesUnrelatedPanic(t *testing.T) {
	mw := watchdogMiddleware(500 * time.Millisecond)
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"observe"}`),
	}
	handler := mw(func(_ context.Context, _ *protocol.Request) (*protocol.Response, error) {
		panic("unexpected invariant failure")
	})
	resp, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("middleware should recover panic, got err=%v", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("expected an error response, got %+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "SCOUT_PANIC") {
		t.Errorf("unrelated panic should retain generic classification: %q", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Message, "browser effects may be uncertain") || !strings.Contains(resp.Error.Message, "recover_task_run") || strings.Contains(resp.Error.Message, "then retry") {
		t.Errorf("panic guidance does not preserve effect uncertainty: %q", resp.Error.Message)
	}
}

func TestWatchdogMiddleware_passesNotificationsThrough(t *testing.T) {
	mw := watchdogMiddleware(10 * time.Millisecond)
	// Notification has no ID — middleware must not impose a deadline,
	// because notifications have no JSON-RPC response slot to write
	// a timeout envelope into.
	req := &protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		Method:  "notifications/cancelled",
	}
	called := false
	handler := mw(func(ctx context.Context, r *protocol.Request) (*protocol.Response, error) {
		called = true
		// Beyond the budget but should still complete.
		time.Sleep(50 * time.Millisecond)
		return nil, nil
	})
	_, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !called {
		t.Error("notification handler should have been invoked")
	}
}

func TestExtractToolName_toolsCall(t *testing.T) {
	req := &protocol.Request{
		Method: protocol.MethodToolsCall,
		Params: json.RawMessage(`{"name":"screenshot","arguments":{}}`),
	}
	if got := extractToolName(req); got != "screenshot" {
		t.Errorf("got %q, want screenshot", got)
	}
}

func TestExtractToolName_nonToolsCall(t *testing.T) {
	req := &protocol.Request{Method: "tools/list"}
	if got := extractToolName(req); got != "tools/list" {
		t.Errorf("got %q, want tools/list", got)
	}
}

func TestExtractToolName_nilOrBadParams(t *testing.T) {
	if got := extractToolName(nil); got != "" {
		t.Errorf("nil request: got %q, want empty", got)
	}
	req := &protocol.Request{Method: protocol.MethodToolsCall, Params: json.RawMessage(`{garbage`)}
	if got := extractToolName(req); got != protocol.MethodToolsCall {
		t.Errorf("bad params: got %q, want %q", got, protocol.MethodToolsCall)
	}
}
