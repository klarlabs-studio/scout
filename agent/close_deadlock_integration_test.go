//go:build integration

package agent_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
)

// TestSessionClose_WhileScreenRecording_NoDeadlock guards the fix for a Close
// deadlock: Close used to hold s.mu while waiting for the capture goroutine,
// which itself takes s.mu on every tick (captureOne) — so closing during an
// active recording wedged the session forever.
func TestSessionClose_WhileScreenRecording_NoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h1>rec</h1></body></html>`))
	}))
	defer srv.Close()

	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := s.Navigate(srv.URL); err != nil {
		_ = s.Close()
		t.Fatalf("Navigate: %v", err)
	}
	if err := s.StartScreenRecording(agent.ScreenRecordingOptions{FPS: 15, Format: "frames"}); err != nil {
		_ = s.Close()
		t.Fatalf("StartScreenRecording: %v", err)
	}

	// Let the capture loop run a few ticks so it is actively contending for s.mu.
	time.Sleep(200 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- s.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: Session.Close hung while a screen recording was active")
	}
}
