package browse

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Recorder captures screencast frames from a page and assembles them into a video.
type Recorder struct {
	page       *Page
	dir        string
	format     string
	quality    int
	maxWidth   int
	maxHeight  int
	frameCount atomic.Int64
	recording  atomic.Bool
	wg         sync.WaitGroup
}

// RecorderOptions configures video recording.
type RecorderOptions struct {
	// Format is "jpeg" (default, smaller) or "png" (lossless).
	Format string
	// Quality is JPEG quality 1-100. Default 80.
	Quality int
	// MaxWidth limits the frame width. 0 means no limit.
	MaxWidth int
	// MaxHeight limits the frame height. 0 means no limit.
	MaxHeight int
}

// NewRecorder creates a recorder for the given page.
// Frames are saved to a temporary directory until Stop is called.
func NewRecorder(page *Page, opts RecorderOptions) (*Recorder, error) {
	dir, err := os.MkdirTemp("", "browse-recording-*")
	if err != nil {
		return nil, fmt.Errorf("browse: failed to create recording dir: %w", err)
	}

	format := opts.Format
	if format == "" {
		format = "jpeg"
	}
	quality := opts.Quality
	if quality == 0 {
		quality = 80
	}

	return &Recorder{
		page:      page,
		dir:       dir,
		format:    format,
		quality:   quality,
		maxWidth:  opts.MaxWidth,
		maxHeight: opts.MaxHeight,
	}, nil
}

// Start begins capturing screencast frames.
func (r *Recorder) Start() error {
	if r.recording.Load() {
		return fmt.Errorf("browse: recorder already running")
	}

	// Set recording flag before starting screencast to avoid missing early frames
	r.recording.Store(true)

	// Listen for screencast frames
	r.page.OnSession("Page.screencastFrame", func(params map[string]any) {
		if !r.recording.Load() {
			return
		}

		sessionID, _ := params["sessionId"].(float64)
		data, _ := params["data"].(string)
		if data == "" {
			return
		}

		// Acknowledge the frame asynchronously to avoid blocking the readLoop
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			_, _ = r.page.call("Page.screencastFrameAck", map[string]any{
				"sessionId": int(sessionID),
			})
		}()

		// Save frame to disk
		frameNum := r.frameCount.Add(1)
		ext := r.format
		if ext == "jpeg" {
			ext = "jpg"
		}
		path := filepath.Join(r.dir, fmt.Sprintf("frame_%05d.%s", frameNum, ext))

		decoded, err := decodeBase64(data)
		if err != nil {
			return
		}
		_ = os.WriteFile(path, decoded, 0o600)
	})

	// Start the screencast
	params := map[string]any{
		"format":  r.format,
		"quality": r.quality,
	}
	if r.maxWidth > 0 {
		params["maxWidth"] = r.maxWidth
	}
	if r.maxHeight > 0 {
		params["maxHeight"] = r.maxHeight
	}

	_, err := r.page.call("Page.startScreencast", params)
	if err != nil {
		r.recording.Store(false)
		return fmt.Errorf("browse: failed to start screencast: %w", err)
	}

	return nil
}

// Stop ends the screencast capture and waits for in-flight frame acks to complete.
func (r *Recorder) Stop() error {
	if !r.recording.CompareAndSwap(true, false) {
		return nil
	}

	// Wait for in-flight ack goroutines to complete
	r.wg.Wait()

	_, err := r.page.call("Page.stopScreencast", nil)
	return err
}

// FrameCount returns the number of frames captured so far.
func (r *Recorder) FrameCount() int64 {
	return r.frameCount.Load()
}

// FramesDir returns the directory containing captured frames.
func (r *Recorder) FramesDir() string {
	return r.dir
}

// SaveVideo assembles captured frames into an MP4 video using ffmpeg.
// Returns the path to the generated video file.
// Requires ffmpeg to be installed on the system.
func (r *Recorder) SaveVideo(outputPath string, fps int) error {
	if r.recording.Load() {
		if err := r.Stop(); err != nil {
			return err
		}
	}

	if r.frameCount.Load() == 0 {
		return fmt.Errorf("browse: no frames captured")
	}

	if fps <= 0 {
		fps = 30
	}

	ext := r.format
	if ext == "jpeg" {
		ext = "jpg"
	}

	inputPattern := filepath.Join(r.dir, fmt.Sprintf("frame_%%05d.%s", ext))

	cmd := exec.Command("ffmpeg", //nolint:gosec // G204: ffmpeg invocation with internal frame paths is intentional
		"-y",
		"-framerate", fmt.Sprintf("%d", fps),
		"-i", inputPattern,
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "fast",
		outputPath,
	)
	cmd.Stderr = nil
	cmd.Stdout = nil

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("browse: ffmpeg failed (is ffmpeg installed?): %w", err)
	}

	return nil
}

// SaveGIF assembles captured frames into an animated GIF using ffmpeg.
func (r *Recorder) SaveGIF(outputPath string, fps int) error {
	if r.recording.Load() {
		if err := r.Stop(); err != nil {
			return err
		}
	}

	if r.frameCount.Load() == 0 {
		return fmt.Errorf("browse: no frames captured")
	}

	if fps <= 0 {
		fps = 15
	}

	ext := r.format
	if ext == "jpeg" {
		ext = "jpg"
	}

	inputPattern := filepath.Join(r.dir, fmt.Sprintf("frame_%%05d.%s", ext))

	cmd := exec.Command("ffmpeg", //nolint:gosec // G204: ffmpeg invocation with internal frame paths is intentional
		"-y",
		"-framerate", fmt.Sprintf("%d", fps),
		"-i", inputPattern,
		"-vf", fmt.Sprintf("fps=%d,scale=800:-1:flags=lanczos", fps),
		outputPath,
	)
	cmd.Stderr = nil
	cmd.Stdout = nil

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("browse: ffmpeg failed: %w", err)
	}

	return nil
}

// Cleanup removes the temporary frames directory.
func (r *Recorder) Cleanup() error {
	return os.RemoveAll(r.dir)
}

// Frames returns all captured frame file paths.
func (r *Recorder) Frames() ([]string, error) {
	ext := r.format
	if ext == "jpeg" {
		ext = "jpg"
	}
	pattern := filepath.Join(r.dir, fmt.Sprintf("frame_*.%s", ext))
	return filepath.Glob(pattern)
}
