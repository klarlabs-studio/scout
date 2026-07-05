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
	if !strings.Contains(msg, "SCOUT_PANIC") {
		t.Errorf("error message lacks SCOUT_PANIC code: %q", msg)
	}
	if !strings.Contains(msg, "navigate") {
		t.Errorf("error message lacks tool name: %q", msg)
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
