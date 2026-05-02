package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	browse "github.com/felixgeelhaar/scout"
)

// ScreenRecordingOptions configures a screen recording.
type ScreenRecordingOptions struct {
	Width     int    // capture width in CSS pixels (default 1280)
	Height    int    // capture height in CSS pixels (default 800)
	FPS       int    // target frames per second 1-60 (default 30)
	Quality   int    // JPEG quality 1-100 (default 80)
	Format    string // "webm", "mp4", or "frames"; auto-detected if empty
	OutputDir string // parent directory for frames/output; defaults to os.TempDir()
}

// ScreenRecordingResult is returned by StopScreenRecording.
type ScreenRecordingResult struct {
	Path       string `json:"path"`        // path to encoded video, or frames directory
	Format     string `json:"format"`      // "webm" | "mp4" | "frames"
	Encoder    string `json:"encoder"`     // "ffmpeg" | "none"
	FrameCount int    `json:"frame_count"` // number of frames captured
	DurationMs int64  `json:"duration_ms"` // wall-clock recording duration
	FramesDir  string `json:"frames_dir"`  // raw JPEG frame directory (preserved)
}

// screenRecording is the internal in-flight recording state.
type screenRecording struct {
	id          string
	startedAt   time.Time
	framesDir   string
	format      string
	fps         int
	width       int
	height      int
	quality     int
	frames      []frameMeta
	framesMu    sync.Mutex
	unsubscribe func()
	closed      atomic.Bool
}

type frameMeta struct {
	path      string
	timestamp float64
	sessionID float64
}

// StartScreenRecording begins capturing the active page as a screencast.
// Returns an error if a recording is already in progress on this session.
func (s *Session) StartScreenRecording(opts ScreenRecordingOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return err
	}
	if s.screenRec != nil {
		return fmt.Errorf("screen recording already in progress")
	}

	if opts.FPS <= 0 || opts.FPS > 60 {
		opts.FPS = 30
	}
	if opts.Width <= 0 {
		opts.Width = 1280
	}
	if opts.Height <= 0 {
		opts.Height = 800
	}
	if opts.Quality <= 0 || opts.Quality > 100 {
		opts.Quality = 80
	}
	if opts.Format == "" {
		if hasFFmpeg() {
			opts.Format = "webm"
		} else {
			opts.Format = "frames"
		}
	}
	switch opts.Format {
	case "webm", "mp4", "frames":
	default:
		return fmt.Errorf("unsupported format %q (want webm|mp4|frames)", opts.Format)
	}

	parent := opts.OutputDir
	if parent == "" {
		parent = os.TempDir()
	}
	framesDir, err := os.MkdirTemp(parent, "scout-rec-*")
	if err != nil {
		return fmt.Errorf("create frames dir: %w", err)
	}

	rec := &screenRecording{
		id:        filepath.Base(framesDir),
		startedAt: time.Now(),
		framesDir: framesDir,
		format:    opts.Format,
		fps:       opts.FPS,
		width:     opts.Width,
		height:    opts.Height,
		quality:   opts.Quality,
	}

	page := s.page
	rec.unsubscribe = page.OnSession("Page.screencastFrame", func(params map[string]any) {
		rec.handleFrame(page, params)
	})

	everyNth := 60 / opts.FPS
	if everyNth < 1 {
		everyNth = 1
	}
	if _, err := page.Call("Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       opts.Quality,
		"maxWidth":      opts.Width,
		"maxHeight":     opts.Height,
		"everyNthFrame": everyNth,
	}); err != nil {
		rec.unsubscribe()
		_ = os.RemoveAll(framesDir)
		return fmt.Errorf("start screencast: %w", err)
	}

	s.screenRec = rec
	return nil
}

// StopScreenRecording finishes the active recording and (optionally) encodes
// frames to a video. Returns the result describing where output landed.
func (s *Session) StopScreenRecording() (*ScreenRecordingResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.screenRec
	if rec == nil {
		return nil, fmt.Errorf("no screen recording in progress")
	}
	s.screenRec = nil
	rec.closed.Store(true)

	if s.page != nil {
		_, _ = s.page.Call("Page.stopScreencast", nil)
	}
	rec.unsubscribe()

	duration := time.Since(rec.startedAt)
	rec.framesMu.Lock()
	frameCount := len(rec.frames)
	frames := make([]frameMeta, frameCount)
	copy(frames, rec.frames)
	rec.framesMu.Unlock()

	if frameCount == 0 {
		return nil, fmt.Errorf("no frames captured (page may have been closed)")
	}

	result := &ScreenRecordingResult{
		FrameCount: frameCount,
		DurationMs: duration.Milliseconds(),
		FramesDir:  rec.framesDir,
	}

	if rec.format == "frames" {
		result.Path = rec.framesDir
		result.Format = "frames"
		result.Encoder = "none"
		return result, nil
	}

	listPath := filepath.Join(rec.framesDir, "concat.txt")
	if err := writeConcatList(listPath, frames); err != nil {
		return nil, fmt.Errorf("build concat list: %w", err)
	}

	if !hasFFmpeg() {
		result.Path = rec.framesDir
		result.Format = "frames"
		result.Encoder = "none"
		return result, nil
	}

	outPath := filepath.Join(filepath.Dir(rec.framesDir), rec.id+"."+rec.format)
	if err := encodeFFmpeg(listPath, outPath, rec.format); err != nil {
		return nil, err
	}
	result.Path = outPath
	result.Format = rec.format
	result.Encoder = "ffmpeg"
	return result, nil
}

// IsScreenRecording reports whether a recording is currently active.
func (s *Session) IsScreenRecording() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screenRec != nil
}

func (rec *screenRecording) handleFrame(page *browse.Page, params map[string]any) {
	if rec.closed.Load() {
		return
	}
	data, _ := params["data"].(string)
	sessionID, _ := params["sessionId"].(float64)
	var ts float64
	if md, ok := params["metadata"].(map[string]any); ok {
		ts, _ = md["timestamp"].(float64)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Still ack so Chrome keeps streaming.
		_, _ = page.Call("Page.screencastFrameAck", map[string]any{"sessionId": sessionID})
		return
	}

	rec.framesMu.Lock()
	idx := len(rec.frames)
	rec.framesMu.Unlock()

	framePath := filepath.Join(rec.framesDir, fmt.Sprintf("frame-%06d.jpg", idx))
	if err := os.WriteFile(framePath, raw, 0o644); err == nil {
		rec.framesMu.Lock()
		rec.frames = append(rec.frames, frameMeta{
			path:      framePath,
			timestamp: ts,
			sessionID: sessionID,
		})
		rec.framesMu.Unlock()
	}

	_, _ = page.Call("Page.screencastFrameAck", map[string]any{"sessionId": sessionID})
}

func (rec *screenRecording) cleanup(page *browse.Page) {
	if rec == nil {
		return
	}
	rec.closed.Store(true)
	if page != nil {
		_, _ = page.Call("Page.stopScreencast", nil)
	}
	if rec.unsubscribe != nil {
		rec.unsubscribe()
	}
}

// writeConcatList writes an ffmpeg concat-demuxer list with real per-frame
// durations derived from CDP timestamps. Falls back to 1/30s if timestamps
// look bogus (zero/negative/excessive).
func writeConcatList(path string, frames []frameMeta) error {
	const fallbackDur = 1.0 / 30.0
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for i, fr := range frames {
		if _, err := fmt.Fprintf(f, "file '%s'\n", fr.path); err != nil {
			return err
		}
		var dur float64
		if i < len(frames)-1 {
			dur = frames[i+1].timestamp - fr.timestamp
		}
		if dur <= 0 || dur > 5 {
			dur = fallbackDur
		}
		if _, err := fmt.Fprintf(f, "duration %.6f\n", dur); err != nil {
			return err
		}
	}
	// ffmpeg concat-demuxer requires the last file repeated without duration.
	last := frames[len(frames)-1]
	if _, err := fmt.Fprintf(f, "file '%s'\n", last.path); err != nil {
		return err
	}
	return nil
}

func hasFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func encodeFFmpeg(listPath, outPath, format string) error {
	args := []string{"-y", "-f", "concat", "-safe", "0", "-i", listPath}
	switch format {
	case "webm":
		args = append(args, "-c:v", "libvpx-vp9", "-b:v", "1M",
			"-deadline", "good", "-cpu-used", "4", "-pix_fmt", "yuv420p")
	case "mp4":
		args = append(args, "-c:v", "libx264", "-preset", "veryfast",
			"-crf", "23", "-pix_fmt", "yuv420p", "-movflags", "+faststart")
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
	args = append(args, outPath)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg encode failed: %w: %s", err, string(out))
	}
	return nil
}
