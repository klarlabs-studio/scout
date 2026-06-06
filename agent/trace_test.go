package agent_test

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func TestStartTrace_AlreadyTracing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	s := newSession(t)

	if err := s.StartTrace(); err != nil {
		t.Fatalf("first StartTrace: %v", err)
	}
	if err := s.StartTrace(); err == nil {
		t.Fatal("expected error on duplicate StartTrace, got nil")
	}
}

func TestStopTrace_NoTrace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	s := newSession(t)

	_, err := s.StopTrace(filepath.Join(t.TempDir(), "trace.zip"))
	if err == nil {
		t.Fatal("expected error on StopTrace without StartTrace, got nil")
	}
}

func TestTrace_NavigateClickType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	s := newSession(t)

	if err := s.StartTrace(); err != nil {
		t.Fatalf("StartTrace: %v", err)
	}

	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if _, err := s.Click("#action"); err != nil {
		t.Fatalf("Click: %v", err)
	}

	if _, err := s.Type("#name", "test-user"); err != nil {
		t.Fatalf("Type: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "trace.zip")
	result, err := s.StopTrace(outPath)
	if err != nil {
		t.Fatalf("StopTrace: %v", err)
	}

	if result.EventCount != 3 {
		t.Errorf("event_count: expected 3, got %d", result.EventCount)
	}
	if result.Path != outPath {
		t.Errorf("path: expected %q, got %q", outPath, result.Path)
	}
	if result.Size == 0 {
		t.Error("file_size_bytes should not be zero")
	}
	if result.Duration == 0 {
		t.Error("total_duration_ms should not be zero")
	}

	r, err := zip.OpenReader(outPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	fileNames := make(map[string]bool)
	for _, f := range r.File {
		fileNames[f.Name] = true
	}

	if !fileNames["trace.json"] {
		t.Error("zip missing trace.json")
	}
	if !fileNames["network.json"] {
		t.Error("zip missing network.json")
	}

	for _, f := range r.File {
		if f.Name == "trace.json" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open trace.json: %v", err)
			}
			defer rc.Close()

			var events []agent.TraceEvent
			if err := json.NewDecoder(rc).Decode(&events); err != nil {
				t.Fatalf("decode trace.json: %v", err)
			}

			if len(events) != 3 {
				t.Fatalf("trace.json: expected 3 events, got %d", len(events))
			}

			if events[0].Action != "navigate" {
				t.Errorf("event[0].action: expected 'navigate', got %q", events[0].Action)
			}
			if events[1].Action != "click" {
				t.Errorf("event[1].action: expected 'click', got %q", events[1].Action)
			}
			if events[1].Selector != "#action" {
				t.Errorf("event[1].selector: expected '#action', got %q", events[1].Selector)
			}
			if events[2].Action != "type" {
				t.Errorf("event[2].action: expected 'type', got %q", events[2].Action)
			}
			if events[2].Value != "test-user" {
				t.Errorf("event[2].value: expected 'test-user', got %q", events[2].Value)
			}

			for i, ev := range events {
				if ev.Timestamp == 0 {
					t.Errorf("event[%d].timestamp_ms should not be zero", i)
				}
				if ev.BeforeImg != "" || ev.AfterImg != "" {
					t.Errorf("event[%d] should have cleared screenshot data from JSON", i)
				}
			}
			break
		}
	}
}

func TestTrace_ZipContainsScreenshots(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	s := newSession(t)

	if err := s.StartTrace(); err != nil {
		t.Fatalf("StartTrace: %v", err)
	}

	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if _, err := s.Click("#action"); err != nil {
		t.Fatalf("Click: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "trace.zip")
	_, err := s.StopTrace(outPath)
	if err != nil {
		t.Fatalf("StopTrace: %v", err)
	}

	r, err := zip.OpenReader(outPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()

	hasAfter := false
	for _, f := range r.File {
		if f.Name == "screenshots/001_after.png" {
			hasAfter = true
			if f.UncompressedSize64 == 0 {
				t.Error("screenshot file is empty")
			}
		}
	}
	if !hasAfter {
		t.Error("zip missing screenshots/001_after.png")
	}
}

func TestTrace_OutputDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}

	s := newSession(t)

	if err := s.StartTrace(); err != nil {
		t.Fatalf("StartTrace: %v", err)
	}

	outPath := filepath.Join(t.TempDir(), "nested", "dir", "trace.zip")
	result, err := s.StopTrace(outPath)
	if err != nil {
		t.Fatalf("StopTrace: %v", err)
	}
	if result.EventCount != 0 {
		t.Errorf("expected 0 events for empty trace, got %d", result.EventCount)
	}

	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("trace file should exist: %v", err)
	}
}
