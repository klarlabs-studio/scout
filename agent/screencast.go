package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// debugScreencast enables verbose tracing of frame arrival. Off in normal builds.
var debugScreencast = os.Getenv("SCOUT_DEBUG_SCREENCAST") == "1"

// ScreenRecordingOptions configures a screen recording.
type ScreenRecordingOptions struct {
	Width     int    // capture width in CSS pixels (default 1280)
	Height    int    // capture height in CSS pixels (default 800)
	FPS       int    // target frames per second 1-30 (default 15; 30 is hard cap)
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
//
// We poll Page.captureScreenshot on a ticker rather than subscribe to
// Page.screencastFrame events. The CDP screencast event stream is unreliable
// in headless Chrome — frames are gated on actual visual updates and the
// "page visible" criterion, neither of which behaves consistently with
// --headless=new. Polled captureScreenshot calls work in any mode and give
// us deterministic FPS control. Trade-off: synchronous capture latency caps
// realistic FPS around 15-20 on a typical page.
type screenRecording struct {
	id        string
	startedAt time.Time
	framesDir string
	format    string
	fps       int
	width     int
	height    int
	quality   int

	// session, not page. Session.Navigate replaces s.page on every navigate
	// (closes the old, opens a fresh one) — pinning a *browse.Page would
	// silently lose every frame after the first navigation. We dereference
	// session.page under the session mutex on each tick instead.
	session *Session

	frames   []frameMeta
	framesMu sync.Mutex

	stop   chan struct{} // closed by StopScreenRecording to signal capture loop
	done   chan struct{} // closed by capture goroutine after it exits
	closed atomic.Bool
}

type frameMeta struct {
	path      string
	timestamp float64 // seconds since recording start
}

// StartScreenRecording begins capturing the active page by polling
// Page.captureScreenshot on a ticker. Returns an error if a recording is
// already in progress on this session.
func (s *Session) StartScreenRecording(opts ScreenRecordingOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return err
	}
	if s.screenRec != nil {
		return fmt.Errorf("screen recording already in progress")
	}

	if opts.FPS <= 0 {
		opts.FPS = 15
	}
	if opts.FPS > 30 {
		opts.FPS = 30 // captureScreenshot polling realistically tops out around here
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
		session:   s,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}

	if debugScreencast {
		fmt.Printf("[screencast] start fps=%d size=%dx%d format=%s dir=%s\n",
			rec.fps, rec.width, rec.height, rec.format, rec.framesDir)
	}

	go rec.captureLoop()

	s.screenRec = rec
	return nil
}

// captureLoop runs in its own goroutine until stop is signalled. Each tick
// calls Page.captureScreenshot, base64-decodes the JPEG, and writes it to
// the frames directory.
func (rec *screenRecording) captureLoop() {
	defer close(rec.done)

	interval := time.Second / time.Duration(rec.fps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Capture an immediate first frame instead of waiting one tick.
	rec.captureOne()

	for {
		select {
		case <-rec.stop:
			return
		case <-ticker.C:
			if rec.closed.Load() {
				return
			}
			rec.captureOne()
		}
	}
}

func (rec *screenRecording) captureOne() {
	// Resolve the current page under the session lock. Navigate / OpenTab /
	// SwitchTab all swap s.page; reading it briefly here keeps recording
	// alive across navigations and tab switches. Hold time is microseconds.
	rec.session.mu.Lock()
	page := rec.session.page
	closed := rec.session.closed
	rec.session.mu.Unlock()
	if closed || page == nil {
		return
	}

	result, err := page.Call("Page.captureScreenshot", map[string]any{
		"format":  "jpeg",
		"quality": rec.quality,
	})
	if err != nil {
		if debugScreencast {
			fmt.Printf("[screencast] capture error: %v\n", err)
		}
		return
	}

	var payload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &payload); err != nil || payload.Data == "" {
		return
	}

	raw, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		return
	}

	rec.framesMu.Lock()
	idx := len(rec.frames)
	ts := time.Since(rec.startedAt).Seconds()
	framePath := filepath.Join(rec.framesDir, fmt.Sprintf("frame-%06d.jpg", idx))
	rec.frames = append(rec.frames, frameMeta{path: framePath, timestamp: ts})
	rec.framesMu.Unlock()

	_ = os.WriteFile(framePath, raw, 0o600)
}

// StopScreenRecording signals the capture loop to stop, waits for it,
// and (optionally) encodes the captured frames into a video.
func (s *Session) StopScreenRecording() (*ScreenRecordingResult, error) {
	s.mu.Lock()

	rec := s.screenRec
	if rec == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("no screen recording in progress")
	}
	s.screenRec = nil
	if rec.closed.CompareAndSwap(false, true) {
		close(rec.stop)
	}
	s.mu.Unlock()

	// Wait for the capture goroutine to finish (drains in-flight CDP call).
	<-rec.done

	duration := time.Since(rec.startedAt)
	rec.framesMu.Lock()
	frameCount := len(rec.frames)
	frames := make([]frameMeta, frameCount)
	copy(frames, rec.frames)
	rec.framesMu.Unlock()

	if frameCount == 0 {
		return nil, fmt.Errorf("no frames captured")
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

func (rec *screenRecording) cleanup() {
	if rec == nil {
		return
	}
	if rec.closed.CompareAndSwap(false, true) {
		close(rec.stop)
	}
	<-rec.done
}

// writeConcatList writes an ffmpeg concat-demuxer list with real per-frame
// durations derived from capture timestamps. Falls back to 1/fps if the
// timestamp delta looks bogus.
func writeConcatList(path string, frames []frameMeta) error {
	const fallbackDur = 1.0 / 30.0
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

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
