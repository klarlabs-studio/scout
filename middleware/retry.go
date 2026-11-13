package middleware

import (
	"context"
	"time"

	browse "go.klarlabs.de/scout"

	"go.klarlabs.de/fortify/retry"
)

// RetryConfig configures the Retry middleware.
type RetryConfig struct {
	MaxAttempts  int
	InitialDelay time.Duration
	Multiplier   float64
	Jitter       bool
	// IsRetryable overrides the default retry predicate. By default, errors
	// reporting an unknown browser outcome are not retried because the command
	// may already have changed application state.
	IsRetryable func(error) bool
}

// Retry returns middleware that retries failed task handlers using fortify's Retry pattern.
func Retry(cfg RetryConfig) browse.HandlerFunc {
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.InitialDelay == 0 {
		cfg.InitialDelay = 500 * time.Millisecond
	}
	if cfg.Multiplier == 0 {
		cfg.Multiplier = 2.0
	}
	if cfg.IsRetryable == nil {
		cfg.IsRetryable = func(err error) bool { return !browse.IsOutcomeUnknown(err) }
	}

	r := retry.New[struct{}](retry.Config{
		MaxAttempts:   cfg.MaxAttempts,
		InitialDelay:  cfg.InitialDelay,
		Multiplier:    cfg.Multiplier,
		BackoffPolicy: retry.BackoffExponential,
		Jitter:        cfg.Jitter,
		IsRetryable:   cfg.IsRetryable,
	})

	return func(c *browse.Context) {
		savedIdx := c.SaveIndex()

		_, err := r.Execute(c.GoContext(), func(ctx context.Context) (struct{}, error) {
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
