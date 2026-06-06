package middleware

import (
	"context"
	"time"

	browse "go.klarlabs.de/scout"

	"go.klarlabs.de/fortify/bulkhead"
)

// BulkheadConfig configures the Bulkhead middleware.
type BulkheadConfig struct {
	// MaxConcurrent is the maximum number of tasks that can run simultaneously.
	MaxConcurrent int
	// MaxQueue is the size of the waiting queue when at capacity.
	MaxQueue int
	// QueueTimeout is how long a task can wait in the queue before being rejected.
	QueueTimeout time.Duration
}

// Bulkhead returns middleware that limits concurrent task execution using fortify's Bulkhead pattern.
// This prevents resource exhaustion by capping the number of simultaneous browser pages.
func Bulkhead(cfg BulkheadConfig) browse.HandlerFunc {
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 5
	}
	if cfg.MaxQueue == 0 {
		cfg.MaxQueue = 10
	}
	if cfg.QueueTimeout == 0 {
		cfg.QueueTimeout = 30 * time.Second
	}

	bh := bulkhead.New[struct{}](bulkhead.Config{
		MaxConcurrent: cfg.MaxConcurrent,
		MaxQueue:      cfg.MaxQueue,
		QueueTimeout:  cfg.QueueTimeout,
	})

	return func(c *browse.Context) {
		savedIdx := c.SaveIndex()

		_, err := bh.Execute(c.GoContext(), func(ctx context.Context) (struct{}, error) {
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
