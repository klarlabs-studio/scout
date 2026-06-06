package middleware

import (
	"path/filepath"
	"strings"
	"time"

	browse "go.klarlabs.de/scout"
)

// ScreenshotOnError returns middleware that captures a screenshot when a task fails.
// Screenshots are saved to the given directory with the task name and timestamp.
func ScreenshotOnError(dir string) browse.HandlerFunc {
	return func(c *browse.Context) {
		c.Next()

		if errs := c.Errors(); len(errs) > 0 {
			ts := time.Now().Format("20060102-150405")
			// Sanitize task name for filesystem (replace path separators)
			safeName := strings.ReplaceAll(c.TaskName(), "/", "_")
			safeName = strings.ReplaceAll(safeName, "\\", "_")
			path := filepath.Join(dir, safeName+"-"+ts+".png")
			_ = c.ScreenshotTo(path)
		}
	}
}
