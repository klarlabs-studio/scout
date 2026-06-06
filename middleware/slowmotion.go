package middleware

import (
	"time"

	browse "go.klarlabs.de/scout"
)

// SlowMotion returns middleware that adds an artificial delay after each task handler.
// Useful for demos and recordings where actions happen too fast to follow.
func SlowMotion(delay time.Duration) browse.HandlerFunc {
	return func(c *browse.Context) {
		c.Next()
		time.Sleep(delay)
	}
}
