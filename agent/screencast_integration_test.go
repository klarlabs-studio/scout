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
	// If ffmpeg is absent (CI Ubuntu, sandboxed runners), scout falls back
	// to a frames-only result with format "frames" and encoder "none".
	// Skip the webm-specific assertions in that case — frame capture is
	// already verified by the FramesFallback test.
	if res.Encoder != "ffmpeg" {
		t.Skipf("ffmpeg not detected on host (encoder=%q, format=%q); content assertions skipped", res.Encoder, res.Format)
	}
	if res.Format != "webm" {
		t.Errorf("Format = %q, want webm", res.Format)
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

// Recording must survive navigation. Session.Navigate replaces s.page with
// a fresh page; an earlier implementation pinned a stale *browse.Page in
// the recording and silently dropped every frame after the first navigate.
func TestIntegrationScreenRecording_AcrossNavigations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	pageHTML := func(label string) string {
		return `<!DOCTYPE html><html><body style="margin:0;background:#000;color:#fff;font:80px system-ui;display:flex;align-items:center;justify-content:center;height:100vh">` + label + `</body></html>`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			_, _ = w.Write([]byte(pageHTML("A")))
		case "/b":
			_, _ = w.Write([]byte(pageHTML("B")))
		case "/c":
			_, _ = w.Write([]byte(pageHTML("C")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.Navigate(srv.URL + "/a"); err != nil {
		t.Fatalf("Navigate /a: %v", err)
	}

	if err := s.StartScreenRecording(agent.ScreenRecordingOptions{
		FPS:    10,
		Format: "frames",
	}); err != nil {
		t.Fatalf("StartScreenRecording: %v", err)
	}

	// Capture across two more navigations. The capture loop must keep
	// shooting frames against the new page each time.
	time.Sleep(400 * time.Millisecond)
	if _, err := s.Navigate(srv.URL + "/b"); err != nil {
		t.Fatalf("Navigate /b: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := s.Navigate(srv.URL + "/c"); err != nil {
		t.Fatalf("Navigate /c: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	res, err := s.StopScreenRecording()
	if err != nil {
		t.Fatalf("StopScreenRecording: %v", err)
	}
	// At 10 fps over ~1.2s with 2 navigations, we should still bank a
	// double-digit number of frames if recording survived. The earlier
	// stale-page bug would have us at ≤2.
	if res.FrameCount < 5 {
		t.Errorf("expected ≥5 frames across navigations, got %d (recording likely lost the session)", res.FrameCount)
	}
	t.Logf("captured %d frames across 3 pages over %dms", res.FrameCount, res.DurationMs)
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
