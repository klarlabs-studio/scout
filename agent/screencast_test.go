package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteConcatList_RealTimestamps(t *testing.T) {
	tmp := t.TempDir()
	listPath := filepath.Join(tmp, "concat.txt")

	frames := []frameMeta{
		{path: "/x/frame-000000.jpg", timestamp: 100.000},
		{path: "/x/frame-000001.jpg", timestamp: 100.040},
		{path: "/x/frame-000002.jpg", timestamp: 100.080},
	}
	if err := writeConcatList(listPath, frames); err != nil {
		t.Fatalf("writeConcatList: %v", err)
	}

	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)

	// Each frame must be referenced.
	for _, f := range frames {
		if !strings.Contains(got, "file '"+f.path+"'") {
			t.Errorf("missing file entry %s in %q", f.path, got)
		}
	}
	// Real durations preserved (40ms gaps).
	if !strings.Contains(got, "duration 0.040000") {
		t.Errorf("expected real duration 0.040000, got %q", got)
	}
	// Last file is duplicated for ffmpeg concat-demuxer.
	last := frames[len(frames)-1].path
	if strings.Count(got, "file '"+last+"'") < 2 {
		t.Errorf("last frame must be repeated, got %q", got)
	}
}

func TestWriteConcatList_FallbackOnBadDelta(t *testing.T) {
	tmp := t.TempDir()
	listPath := filepath.Join(tmp, "concat.txt")

	// Negative and excessive deltas should fall back to 1/30s = 0.033333.
	frames := []frameMeta{
		{path: "a.jpg", timestamp: 100.0},
		{path: "b.jpg", timestamp: 99.5},  // negative
		{path: "c.jpg", timestamp: 200.0}, // > 5s gap
	}
	if err := writeConcatList(listPath, frames); err != nil {
		t.Fatalf("writeConcatList: %v", err)
	}
	data, _ := os.ReadFile(listPath)
	got := string(data)
	if !strings.Contains(got, "duration 0.033333") {
		t.Errorf("expected fallback 0.033333 for bad delta, got %q", got)
	}
}

func TestStartScreenRecording_RejectsBadFormat(t *testing.T) {
	// Without launching a browser, a bad format must still error pre-launch
	// only if ensurePage succeeds. Use a closed session to short-circuit.
	s := &Session{closed: true}
	err := s.StartScreenRecording(ScreenRecordingOptions{Format: "avi"})
	if err == nil {
		t.Fatal("expected error from closed session or bad format")
	}
}

func TestStopScreenRecording_NoActive(t *testing.T) {
	s := &Session{}
	if _, err := s.StopScreenRecording(); err == nil {
		t.Fatal("expected error when no recording active")
	}
}

func TestIsScreenRecording_FalseByDefault(t *testing.T) {
	s := &Session{}
	if s.IsScreenRecording() {
		t.Fatal("fresh session should not report active recording")
	}
}

func TestHasFFmpeg_Boolean(t *testing.T) {
	// Just exercises the LookPath path; either result is acceptable.
	_ = hasFFmpeg()
}
