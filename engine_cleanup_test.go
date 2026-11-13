package browse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.klarlabs.de/scout/internal/cdp"
)

func TestNewPageJoinsTargetCloseFailureAfterAttachFailure(t *testing.T) {
	const attachErr = "attach rejected by fixture"
	const closeErr = "target close rejected by fixture"
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			method, _ := req["method"].(string)
			id := req["id"]
			var response map[string]any
			switch method {
			case "Target.createTarget":
				response = map[string]any{"id": id, "result": map[string]any{"targetId": "created-target"}}
			case "Target.attachToTarget":
				response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": attachErr}}
			case "Target.closeTarget":
				response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": closeErr}}
			default:
				response = map[string]any{"id": id, "result": map[string]any{}}
			}
			if err := ws.WriteJSON(response); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, err := cdp.Dial(wsURL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	engine := New(WithHeadless(true))
	engine.conn = conn
	_, err = engine.NewPageContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), attachErr) || !strings.Contains(err.Error(), closeErr) {
		t.Fatalf("NewPageContext error = %v, want attach and target close failures", err)
	}
}

func TestNewPageJoinsDetachFailureAfterPolicySetupFailure(t *testing.T) {
	const setupErr = "URL policy enable rejected by fixture"
	const detachErr = "page detach rejected by fixture"
	var detachCalls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			method, _ := req["method"].(string)
			id := req["id"]
			var response map[string]any
			switch method {
			case "Target.attachToTarget":
				response = map[string]any{"id": id, "result": map[string]any{"sessionId": "page-session"}}
			case "Fetch.enable":
				response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": setupErr}}
			case "Target.detachFromTarget":
				detachCalls.Add(1)
				response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": detachErr}}
			default:
				response = map[string]any{"id": id, "result": map[string]any{}}
			}
			if err := ws.WriteJSON(response); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, err := cdp.Dial(wsURL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	_, err = newPageContext(context.Background(), conn, "caller-page", time.Second, URLValidator{AllowedOrigins: []string{"https://allowed.example"}})
	if err == nil || !strings.Contains(err.Error(), setupErr) || !strings.Contains(err.Error(), detachErr) {
		t.Fatalf("newPageContext error = %v, want setup and detach failures", err)
	}
	if got := detachCalls.Load(); got != 1 {
		t.Fatalf("detach calls = %d, want one bounded cleanup attempt", got)
	}
}

func TestAttachFrameTargetJoinsDetachFailureAfterSetupFailure(t *testing.T) {
	const setupErr = "frame Page.enable rejected by fixture"
	const detachErr = "frame detach rejected by fixture"
	var detachCalls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			method, _ := req["method"].(string)
			id := req["id"]
			session, _ := req["sessionId"].(string)
			var response map[string]any
			switch {
			case method == "Target.attachToTarget":
				params, _ := req["params"].(map[string]any)
				targetID, _ := params["targetId"].(string)
				sessionID := "parent-session"
				if targetID == "child-target" {
					sessionID = "child-session"
				}
				response = map[string]any{"id": id, "result": map[string]any{"sessionId": sessionID}}
			case method == "Page.enable" && session == "child-session":
				response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": setupErr}}
			case method == "Target.detachFromTarget":
				if detachCalls.Add(1) == 1 {
					response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": detachErr}}
				} else {
					response = map[string]any{"id": id, "result": map[string]any{}}
				}
			default:
				response = map[string]any{"id": id, "result": map[string]any{}}
			}
			if err := ws.WriteJSON(response); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, err := cdp.Dial(wsURL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	parent, err := newPageContext(context.Background(), conn, "parent-target", time.Second, URLValidator{AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("attach parent page: %v", err)
	}
	_, err = parent.AttachFrameTargetContext(context.Background(), "child-target")
	if err == nil || !strings.Contains(err.Error(), setupErr) || !strings.Contains(err.Error(), detachErr) {
		t.Fatalf("AttachFrameTargetContext error = %v, want setup and detach failures", err)
	}
	var attachErr *FrameTargetAttachError
	if !errors.As(err, &attachErr) || attachErr.Page == nil || attachErr.AttachedTargetID != "child-target" {
		t.Fatalf("attach error did not retain retryable child session: %#v", attachErr)
	}
	if err := attachErr.Page.DetachContext(context.Background()); err != nil {
		t.Fatalf("retry detach: %v", err)
	}
	if got := detachCalls.Load(); got != 2 {
		t.Fatalf("detach calls = %d, want failed cleanup and one successful retry", got)
	}
}

func TestAttachFrameTargetDetachesLateResponseAfterCallerDeadline(t *testing.T) {
	var attached, detached atomic.Int32
	var parentDetached atomic.Int32
	childDetachFailed := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			method, _ := req["method"].(string)
			switch method {
			case "Target.attachToTarget":
				params, _ := req["params"].(map[string]any)
				if params["targetId"] == "child-target" {
					attached.Add(1)
					time.Sleep(40 * time.Millisecond)
					_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{"sessionId": "late-child-session"}})
				} else {
					_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{"sessionId": "parent-session"}})
				}
			case "Target.detachFromTarget":
				params, _ := req["params"].(map[string]any)
				if params["sessionId"] == "parent-session" {
					parentDetached.Add(1)
					_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{}})
				} else if detached.Add(1) == 1 {
					_ = ws.WriteJSON(map[string]any{"id": req["id"], "error": map[string]any{"code": -32000, "message": "injected first detach failure"}})
					close(childDetachFailed)
				} else {
					_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{}})
				}
			default:
				_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{}})
			}
		}
	}))
	defer srv.Close()
	conn, err := cdp.Dial("ws" + strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	parent, err := newPageContext(context.Background(), conn, "parent-target", time.Second, URLValidator{AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("parent: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = parent.AttachFrameTargetContext(ctx, "child-target")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("attach error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Millisecond {
		t.Fatalf("attach took %s after caller deadline", elapsed)
	}
	deadline := time.Now().Add(time.Second)
	for detached.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if attached.Load() != 1 || detached.Load() != 1 {
		t.Fatalf("attach/detach calls = %d/%d, want 1/1 before retry", attached.Load(), detached.Load())
	}
	if _, err := parent.CallSessionContext(context.Background(), "Runtime.evaluate", map[string]any{"expression": "1"}); err != nil {
		t.Fatalf("parent session unusable after late attach: %v", err)
	}
	select {
	case <-childDetachFailed:
	case <-time.After(time.Second):
		t.Fatal("late child detach was not attempted")
	}
	deadline = time.Now().Add(time.Second)
	for {
		parent.orphanFrameMu.Lock()
		pending := len(parent.orphanFramePages)
		parent.orphanFrameMu.Unlock()
		if pending > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := parent.DetachContext(context.Background()); err != nil {
		t.Fatalf("parent detach retry: %v", err)
	}
	if detached.Load() != 2 || parentDetached.Load() != 1 {
		t.Fatalf("child/parent detach calls after parent cleanup = %d/%d, want 2/1", detached.Load(), parentDetached.Load())
	}
}

func TestPageCallContextHonorsPageLifetime(t *testing.T) {
	var calls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			calls.Add(1)
			_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{"sessionId": "page-session"}})
		}
	}))
	defer srv.Close()
	conn, err := cdp.Dial("ws" + strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	page, err := newPageContext(context.Background(), conn, "target", time.Second, URLValidator{AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	page.CloseContext(context.Background())
	before := calls.Load()
	if _, err := page.CallContext(context.Background(), "Runtime.evaluate", map[string]any{"expression": "1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CallContext error = %v, want canceled page lifetime", err)
	}
	if got := calls.Load(); got != before {
		t.Fatalf("post-close call reached transport: calls %d -> %d", before, got)
	}
}

func TestPageContextHelpersHonorCallerCancellation(t *testing.T) {
	var calls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			calls.Add(1)
			_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{"sessionId": "page-session"}})
		}
	}))
	defer srv.Close()
	conn, err := cdp.Dial("ws" + strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	page, err := newPageContext(context.Background(), conn, "target", time.Second, URLValidator{AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	before := calls.Load()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := page.URLContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("URLContext error = %v", err)
	}
	if err := page.SetViewportContext(ctx, 800, 600); !errors.Is(err, context.Canceled) {
		t.Fatalf("SetViewportContext error = %v", err)
	}
	if err := page.SetCookieContext(ctx, Cookie{Name: "a", Value: "b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SetCookieContext error = %v", err)
	}
	if err := page.SetUserAgentContext(ctx, "agent"); !errors.Is(err, context.Canceled) {
		t.Fatalf("SetUserAgentContext error = %v", err)
	}
	if got := calls.Load(); got != before {
		t.Fatalf("canceled helper reached transport: calls %d -> %d", before, got)
	}
}

func TestCookieMutationStopsWhenNetworkEnableFails(t *testing.T) {
	var mutations atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			method, _ := req["method"].(string)
			if method == "Network.enable" {
				_ = ws.WriteJSON(map[string]any{"id": req["id"], "error": map[string]any{"code": -32000, "message": "network domain rejected"}})
				continue
			}
			if method == "Network.setCookie" {
				mutations.Add(1)
			}
			_ = ws.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{"sessionId": "page-session"}})
		}
	}))
	defer srv.Close()
	conn, err := cdp.Dial("ws" + strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	page, err := newPageContext(context.Background(), conn, "target", time.Second, URLValidator{AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	err = page.SetCookieContext(context.Background(), Cookie{Name: "session", Value: "secret"})
	if err == nil || !strings.Contains(err.Error(), "network domain rejected") {
		t.Fatalf("SetCookieContext error = %v, want Network.enable rejection", err)
	}
	if mutations.Load() != 0 {
		t.Fatalf("cookie mutations = %d, want none after setup failure", mutations.Load())
	}
}
