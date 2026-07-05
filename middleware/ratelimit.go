package middleware

import (
	"time"

	browse "go.klarlabs.de/scout"

	"go.klarlabs.de/fortify/ratelimit"
)

// RateLimitConfig configures the RateLimit middleware.
type RateLimitConfig struct {
	// Rate is the number of tasks allowed per Interval.
	Rate int
	// Burst is the maximum burst capacity.
	Burst int
	// Interval is the time window for rate counting. Defaults to 1 second.
	Interval time.Duration
}

// RateLimit returns middleware that throttles task execution using fortify's token bucket rate limiter.
// Tasks that exceed the rate are rejected with an error.
func RateLimit(cfg RateLimitConfig) browse.HandlerFunc {
	if cfg.Rate == 0 {
		cfg.Rate = 10
	}
	if cfg.Burst == 0 {
		cfg.Burst = cfg.Rate
	}

	rl := ratelimit.New(ratelimit.Config{
		Rate:     cfg.Rate,
		Burst:    cfg.Burst,
		Interval: cfg.Interval,
	})

	return func(c *browse.Context) {
		if !rl.Allow(c.GoContext(), c.TaskName()) {
			c.AbortWithError(&browse.RateLimitError{TaskName: c.TaskName()})
			return
		}
		c.Next()
	}
}
