//go:build integration

package agui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
)

func TestIntegrationSessionManagerBrowserLifecycle(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<!doctype html><title>AG-UI lifecycle</title><main>browser ready</main>")
	}))
	defer fixture.Close()

	sm := NewSessionManager(time.Hour)
	sm.mu.Lock()
	sm.newSession = func(config agent.SessionConfig) (*agent.Session, error) {
		config.Headless = true
		config.AllowPrivateIPs = true
		return agent.NewSession(config)
	}
	sm.mu.Unlock()

	session, err := sm.Get("integration-thread")
	if err != nil {
		t.Fatalf("Get browser session: %v", err)
	}
	result, err := session.Navigate(fixture.URL)
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if result.Title != "AG-UI lifecycle" {
		t.Fatalf("page title = %q, want %q", result.Title, "AG-UI lifecycle")
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close browser session: %v", err)
	}
	if _, err := sm.Get("after-close"); err == nil {
		t.Fatal("Get after Close unexpectedly created a browser session")
	}
}
