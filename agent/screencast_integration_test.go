package agent_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felixgeelhaar/scout/agent"
)

// End-to-end screen recording: drive a real Chrome via CDP, capture frames
// for ~2s, encode through ffmpeg, assert we have a non-empty .webm file.
// Skipped on -short.
func TestIntegrationScreenRecording_WebM(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome and ffmpeg")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html><head><title>rec</title>
<style>body{margin:0;background:#0a0e1a;color:#fff;font:48px system-ui;display:flex;align-items:center;justify-content:center;height:100vh}
.dot{width:120px;height:120px;border-radius:50%;background:#3b82f6;animation:p 1s linear infinite}
@keyframes p{0%,100%{transform:scale(1)}50%{transform:scale(.6)}}
</style></head><body><div class="dot"></div></body></html>`))
	}))
	defer srv.Close()

	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if err := s.StartScreenRecording(agent.ScreenRecordingOptions{
		Width:  640,
		Height: 480,
		FPS:    20,
		Format: "webm",
	}); err != nil {
		t.Fatalf("StartScreenRecording: %v", err)
	}

	// Capture ~1.5s of the animated dot.
	time.Sleep(1500 * time.Millisecond)

	res, err := s.StopScreenRecording()
	if err != nil {
		t.Fatalf("StopScreenRecording: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	if res.FrameCount == 0 {
		t.Fatal("no frames captured")
	}
	if res.Format != "webm" {
		t.Errorf("Format = %q, want webm", res.Format)
	}
	if res.Encoder != "ffmpeg" {
		t.Skipf("ffmpeg not detected on host (encoder=%q); skipping content assertion", res.Encoder)
	}
	if !strings.HasSuffix(res.Path, ".webm") {
		t.Errorf("Path = %q, expected .webm suffix", res.Path)
	}
	st, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("output not on disk: %v", err)
	}
	if st.Size() < 1024 {
		t.Errorf("output suspiciously small: %d bytes", st.Size())
	}
	t.Logf("recorded %d frames over %dms → %s (%d bytes)", res.FrameCount, res.DurationMs, res.Path, st.Size())

	// Cleanup the output we just wrote.
	_ = os.Remove(res.Path)
	_ = os.RemoveAll(res.FramesDir)
}

func TestIntegrationScreenRecording_FramesFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h1>x</h1></body></html>`))
	}))
	defer srv.Close()

	headless := os.Getenv("SCOUT_TEST_HEADFUL") != "1"
	s, err := agent.NewSession(agent.SessionConfig{Headless: headless, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := s.StartScreenRecording(agent.ScreenRecordingOptions{
		Format: "frames",
		FPS:    15,
	}); err != nil {
		t.Fatalf("StartScreenRecording: %v", err)
	}
	time.Sleep(800 * time.Millisecond)
	res, err := s.StopScreenRecording()
	if err != nil {
		t.Fatalf("StopScreenRecording: %v", err)
	}
	if res.Format != "frames" || res.Encoder != "none" {
		t.Errorf("expected frames-only result, got %+v", res)
	}
	if res.FrameCount == 0 {
		t.Error("no frames captured")
	}
	if res.Path != res.FramesDir {
		t.Errorf("frames mode: Path should equal FramesDir, got %q vs %q", res.Path, res.FramesDir)
	}
	_ = os.RemoveAll(res.FramesDir)
}
