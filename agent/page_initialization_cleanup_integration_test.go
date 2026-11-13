package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

func TestRemotePageInitializationCleanupFailureAgainstChrome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()

	conn, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("dial Chrome: %v", err)
	}
	targets, err := conn.GetTargetsCtx(ctx)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("list Chrome targets: %v", err)
	}
	var targetID string
	for _, target := range targets {
		if target.Type == "page" {
			targetID = target.TargetID
			break
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close target discovery connection: %v", err)
	}
	if targetID == "" {
		t.Fatal("Chrome did not expose its initial page target")
	}

	proxy := newPageSetupFaultProxy(t, browser.WSEndpoint())
	defer proxy.server.Close()
	_, err = NewSessionContext(ctx, SessionConfig{
		RemoteCDP: proxy.url, TargetID: targetID, AllowPrivateIPs: true,
		AllowedOrigins: []string{"https://allowed.example"}, Timeout: 10 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), proxy.setupError) || !strings.Contains(err.Error(), proxy.detachError) {
		t.Fatalf("NewSessionContext error = %v, want policy setup and detach failures", err)
	}
	if got := proxy.fetchEnableCalls.Load(); got != 1 {
		t.Fatalf("injected Fetch.enable failures = %d, want 1", got)
	}
	if got := proxy.detachCalls.Load(); got != 1 {
		t.Fatalf("Target.detachFromTarget calls = %d, want 1", got)
	}
	if got := proxy.closeTargetCalls.Load(); got != 0 {
		t.Fatalf("Target.closeTarget calls = %d, caller-owned page must stay open", got)
	}

	check, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("redial Chrome after failed attachment: %v", err)
	}
	defer check.Close()
	remaining, err := check.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("relist Chrome targets: %v", err)
	}
	for _, target := range remaining {
		if target.TargetID == targetID && target.Type == "page" {
			return
		}
	}
	t.Fatalf("caller-owned Chrome page %q was closed during failed setup", targetID)
}

type pageSetupFaultProxy struct {
	server           *httptest.Server
	url              string
	setupError       string
	detachError      string
	fetchEnableCalls atomic.Int32
	detachCalls      atomic.Int32
	closeTargetCalls atomic.Int32
}

func newPageSetupFaultProxy(t *testing.T, upstreamURL string) *pageSetupFaultProxy {
	t.Helper()
	proxy := &pageSetupFaultProxy{
		setupError:  "injected URL policy setup failure",
		detachError: "injected target detach failure",
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	proxy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer client.Close()
		upstream, _, err := websocket.DefaultDialer.DialContext(r.Context(), upstreamURL, nil)
		if err != nil {
			return
		}
		defer upstream.Close()

		writes := make(chan []byte, 16)
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			for message := range writes {
				if err := client.WriteMessage(websocket.TextMessage, message); err != nil {
					return
				}
			}
		}()

		var mu sync.Mutex
		pendingDetach := make(map[string]struct{})
		upstreamDone := make(chan struct{})
		go func() {
			defer close(upstreamDone)
			for {
				_, message, err := upstream.ReadMessage()
				if err != nil {
					return
				}
				var response struct {
					ID json.RawMessage `json:"id"`
				}
				if json.Unmarshal(message, &response) == nil && len(response.ID) > 0 {
					key := string(response.ID)
					mu.Lock()
					_, replace := pendingDetach[key]
					delete(pendingDetach, key)
					mu.Unlock()
					if replace {
						message, _ = json.Marshal(map[string]any{
							"id":    json.RawMessage(response.ID),
							"error": map[string]any{"code": -32000, "message": proxy.detachError},
						})
					}
				}
				select {
				case writes <- message:
				case <-r.Context().Done():
					return
				}
			}
		}()

		for {
			_, message, err := client.ReadMessage()
			if err != nil {
				break
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(message, &request) == nil {
				switch request.Method {
				case "Fetch.enable":
					proxy.fetchEnableCalls.Add(1)
					failure, _ := json.Marshal(map[string]any{
						"id":    json.RawMessage(request.ID),
						"error": map[string]any{"code": -32000, "message": proxy.setupError},
					})
					writes <- failure
					continue
				case "Target.detachFromTarget":
					proxy.detachCalls.Add(1)
					mu.Lock()
					pendingDetach[string(request.ID)] = struct{}{}
					mu.Unlock()
				case "Target.closeTarget":
					proxy.closeTargetCalls.Add(1)
				}
			}
			if err := upstream.WriteMessage(websocket.TextMessage, message); err != nil {
				break
			}
		}
		_ = client.Close()
		_ = upstream.Close()
		close(writes)
		<-writerDone
		<-upstreamDone
	}))
	proxy.url = "ws" + strings.TrimPrefix(proxy.server.URL, "http")
	return proxy
}
