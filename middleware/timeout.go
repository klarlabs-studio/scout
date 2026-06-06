// Package middleware provides reusable middleware for browse-go tasks.
package middleware

import (
	"context"
	"time"

	browse "go.klarlabs.de/scout"

	"go.klarlabs.de/fortify/timeout"
)

// Timeout returns middleware that enforces a per-task deadline using fortify's Timeout pattern.
func Timeout(d time.Duration) browse.HandlerFunc {
	tm := timeout.New[struct{}](timeout.Config{
		DefaultTimeout: d,
	})

	return func(c *browse.Context) {
		savedIdx := c.SaveIndex()

		_, err := tm.Execute(c.GoContext(), 0, func(ctx context.Context) (struct{}, error) {
			c.RestoreIndex(savedIdx)
			c.Next()
			if errs := c.Errors(); len(errs) > 0 {
				return struct{}{}, errs[0]
			}
			return struct{}{}, nil
		})
		if err != nil {
			c.AbortWithError(err)
		}
	}
}
