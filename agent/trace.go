package agent

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	browse "go.klarlabs.de/scout"
)

type traceState struct {
	events    []TraceEvent
	startTime time.Time
}

func (s *Session) StartTrace() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tracing {
		return fmt.Errorf("trace already in progress")
	}

	s.tracing = true
	s.trace = &traceState{
		startTime: time.Now(),
	}
	return nil
}

func (s *Session) StopTrace(path string) (*TraceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.tracing || s.trace == nil {
		return nil, fmt.Errorf("no trace in progress")
	}

	trace := s.trace
	s.tracing = false
	s.trace = nil

	return s.writeTraceZip(path, trace)
}

func (s *Session) writeTraceZip(path string, trace *traceState) (*TraceResult, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Path is the caller-requested trace destination (CLI/MCP input).
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace file: %w", err)
	}
	defer func() { _ = f.Close() }()

	w := zip.NewWriter(f)
	defer func() { _ = w.Close() }()

	cleanEvents := make([]TraceEvent, len(trace.events))
	for i, ev := range trace.events {
		cleanEvents[i] = ev
		cleanEvents[i].BeforeImg = ""
		cleanEvents[i].AfterImg = ""
	}

	traceJSON, err := json.MarshalIndent(cleanEvents, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal trace events: %w", err)
	}
	tw, err := w.Create("trace.json")
	if err != nil {
		return nil, err
	}
	if _, err := tw.Write(traceJSON); err != nil {
		return nil, err
	}

	var networkData []NetworkCapture
	if s.network != nil {
		s.network.mu.Lock()
		networkData = make([]NetworkCapture, len(s.network.requests))
		copy(networkData, s.network.requests)
		s.network.mu.Unlock()
	}
	netJSON, err := json.MarshalIndent(networkData, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal network data: %w", err)
	}
	nw, err := w.Create("network.json")
	if err != nil {
		return nil, err
	}
	if _, err := nw.Write(netJSON); err != nil {
		return nil, err
	}

	for i, ev := range trace.events {
		if ev.BeforeImg != "" {
			iw, err := w.Create(fmt.Sprintf("screenshots/%03d_before.png", i))
			if err != nil {
				return nil, err
			}
			if _, err := iw.Write([]byte(ev.BeforeImg)); err != nil {
				return nil, err
			}
		}
		if ev.AfterImg != "" {
			iw, err := w.Create(fmt.Sprintf("screenshots/%03d_after.png", i))
			if err != nil {
				return nil, err
			}
			if _, err := iw.Write([]byte(ev.AfterImg)); err != nil {
				return nil, err
			}
		}
	}

	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var totalDuration int64
	if len(trace.events) > 0 {
		totalDuration = time.Since(trace.startTime).Milliseconds()
	}

	return &TraceResult{
		Path:       path,
		EventCount: len(trace.events),
		Duration:   totalDuration,
		Size:       info.Size(),
	}, nil
}

func (s *Session) captureTraceScreenshot() []byte {
	if s.page == nil {
		return nil
	}
	data, err := s.page.ScreenshotWithOptions(browse.ScreenshotOptions{
		MaxSize: 200 * 1024,
	})
	if err != nil {
		return nil
	}
	return data
}

func (s *Session) traceBeforeAction() (time.Time, []byte) {
	if !s.tracing {
		return time.Time{}, nil
	}
	return time.Now(), s.captureTraceScreenshot()
}

func (s *Session) traceAfterAction(start time.Time, before []byte, action, selector, value, url string, actionErr error) {
	if !s.tracing || s.trace == nil {
		return
	}

	after := s.captureTraceScreenshot()

	ev := TraceEvent{
		Index:     len(s.trace.events),
		Action:    action,
		Selector:  selector,
		Value:     value,
		URL:       url,
		Timestamp: start.UnixMilli(),
		Duration:  time.Since(start).Milliseconds(),
	}
	if actionErr != nil {
		ev.Error = actionErr.Error()
	}
	if before != nil {
		ev.BeforeImg = string(before)
	}
	if after != nil {
		ev.AfterImg = string(after)
	}

	s.trace.events = append(s.trace.events, ev)
}
