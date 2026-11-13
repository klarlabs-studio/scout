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

	"github.com/gorilla/websocket"
	"go.klarlabs.de/scout/internal/cdp"
)

func TestSelectionClickContextGuardRunsAfterGeometryBeforeInput(t *testing.T) {
	var gotGeometry atomic.Bool
	var inputEvents atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, message, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(message, &request) != nil {
				continue
			}
			result := map[string]any{}
			switch request.Method {
			case "Target.attachToTarget":
				result["sessionId"] = "click-guard-session"
			case "DOM.getContentQuads":
				gotGeometry.Store(true)
				result["quads"] = [][]float64{{0, 0, 10, 0, 10, 10, 0, 10}}
			case "Input.dispatchMouseEvent":
				inputEvents.Add(1)
			}
			response, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
			if ws.WriteMessage(websocket.TextMessage, response) != nil {
				return
			}
		}
	}))
	defer server.Close()
	conn, err := cdp.Dial("ws" + strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	page, err := newPage(conn, "target", 0, URLValidator{AllowPrivateIPs: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = page.DetachContext(context.Background()) }()

	wantErr := errors.New("page identity changed")
	selection := NewSelection(page, 7, "#target")
	err = selection.ClickContextWithGuard(context.Background(), func() error {
		if !gotGeometry.Load() {
			return errors.New("guard ran before click geometry was resolved")
		}
		if inputEvents.Load() != 0 {
			return errors.New("input was dispatched before the guard")
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ClickContextWithGuard() error = %v, want wrapped guard error", err)
	}
	if got := inputEvents.Load(); got != 0 {
		t.Fatalf("input events = %d, want none after guard rejection", got)
	}
}

func TestSelectionInputContextGuardRunsAgainBeforeInsertText(t *testing.T) {
	var functionCalls atomic.Int32
	var insertTextCalls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, message, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var request struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(message, &request) != nil {
				continue
			}
			result := map[string]any{}
			switch request.Method {
			case "Target.attachToTarget":
				result["sessionId"] = "input-guard-session"
			case "DOM.resolveNode":
				result["object"] = map[string]any{"objectId": "input-target"}
			case "Runtime.callFunctionOn":
				functionCalls.Add(1)
			case "Input.insertText":
				insertTextCalls.Add(1)
			}
			response, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
			if ws.WriteMessage(websocket.TextMessage, response) != nil {
				return
			}
		}
	}))
	defer server.Close()
	conn, err := cdp.Dial("ws" + strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	page, err := newPage(conn, "target", 0, URLValidator{AllowPrivateIPs: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = page.DetachContext(context.Background()) }()

	wantErr := errors.New("document changed after focus")
	var guardCalls atomic.Int32
	err = NewSelection(page, 7, "#target").InputContextWithGuard(context.Background(), "text", func() error {
		if guardCalls.Add(1) == 2 {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("InputContextWithGuard() error = %v, want second guard error", err)
	}
	if got := functionCalls.Load(); got != 1 {
		t.Fatalf("Runtime.callFunctionOn calls = %d, want only clear/focus before the rejected second guard", got)
	}
	if got := insertTextCalls.Load(); got != 0 {
		t.Fatalf("Input.insertText calls = %d, want none after second guard rejection", got)
	}
}
