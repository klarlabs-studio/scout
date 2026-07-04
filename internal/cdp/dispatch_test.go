package cdp

import (
	"encoding/json"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func sendCDPResult(ws *websocket.Conn, id any, result map[string]any) {
	data, _ := json.Marshal(map[string]any{"id": id, "result": result})
	_ = ws.WriteMessage(websocket.TextMessage, data)
}

func sendCDPEvent(ws *websocket.Conn, method string, params map[string]any) {
	data, _ := json.Marshal(map[string]any{"method": method, "params": params})
	_ = ws.WriteMessage(websocket.TextMessage, data)
}

// newTriggerServer replies to every call and, on "Test.trigger", also emits a
// "Test.event" so a test can drive the client's event dispatch deterministically.
func newTriggerServer(t *testing.T) *httptest.Server {
	return mockCDPServer(t, func(ws *websocket.Conn, req map[string]any) {
		method, _ := req["method"].(string)
		id := req["id"]
		if method == "Test.trigger" {
			sendCDPResult(ws, id, map[string]any{})
			sendCDPEvent(ws, "Test.event", map[string]any{})
			return
		}
		sendCDPResult(ws, id, map[string]any{"ok": true})
	})
}

// TestEventHandlerCanCallCDPWithoutDeadlock is the core regression: an event
// handler that makes a CDP call must not deadlock. Handlers now run on a
// dedicated dispatch goroutine, so readLoop stays free to route the call's
// response. With the old synchronous-on-readLoop dispatch this hangs forever.
func TestEventHandlerCanCallCDPWithoutDeadlock(t *testing.T) {
	conn, err := Dial(wsURL(newTriggerServer(t)))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	conn.On("Test.event", func(_ json.RawMessage) {
		_, err := conn.Call("Test.ping", nil)
		done <- err
	})

	if _, err := conn.Call("Test.trigger", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handler's in-CDP call failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: event handler's CDP call never completed")
	}
}

// TestUnsubscribeRemovesHandler verifies the unsubscribe func removes the entry
// (not just flags it), so re-subscribing observers don't leak dead handlers.
func TestUnsubscribeRemovesHandler(t *testing.T) {
	conn, err := Dial(wsURL(newTriggerServer(t)))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	var count atomic.Int32
	unsub := conn.On("Test.event", func(_ json.RawMessage) { count.Add(1) })
	unsub()

	conn.eventsMu.RLock()
	keys := len(conn.events)
	conn.eventsMu.RUnlock()
	if keys != 0 {
		t.Errorf("unsubscribe should remove the entry; events still holds %d key(s)", keys)
	}

	if _, err := conn.Call("Test.trigger", nil); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if count.Load() != 0 {
		t.Errorf("removed handler was still invoked %d time(s)", count.Load())
	}
}

// TestHandlerPanicDoesNotKillDispatch verifies a panicking handler is recovered
// so the dispatch goroutine survives and later handlers/events still run.
func TestHandlerPanicDoesNotKillDispatch(t *testing.T) {
	conn, err := Dial(wsURL(newTriggerServer(t)))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	var good atomic.Int32
	conn.On("Test.event", func(_ json.RawMessage) { panic("boom") })
	conn.On("Test.event", func(_ json.RawMessage) { good.Add(1) })

	for i := 0; i < 2; i++ {
		if _, err := conn.Call("Test.trigger", nil); err != nil {
			t.Fatalf("trigger %d: %v", i, err)
		}
	}

	deadline := time.After(3 * time.Second)
	for good.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("good handler ran %d/2 times — dispatch died on the panic", good.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
