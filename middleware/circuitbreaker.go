package middleware

import (
	"context"
	"time"

	browse "go.klarlabs.de/scout"

	"go.klarlabs.de/fortify/circuitbreaker"
)

// CircuitBreakerConfig configures the CircuitBreaker middleware.
type CircuitBreakerConfig struct {
	// MaxRequests allowed in half-open state.
	MaxRequests uint32
	// Interval to clear counts in closed state.
	Interval time.Duration
	// Timeout for open state before transitioning to half-open.
	Timeout time.Duration
	// ConsecutiveFailures threshold to trip the circuit.
	ConsecutiveFailures uint32
}

// CircuitBreaker returns middleware that prevents cascading failures using fortify's CircuitBreaker.
// When too many tasks fail, subsequent tasks are rejected immediately until the circuit recovers.
func CircuitBreaker(cfg CircuitBreakerConfig) browse.HandlerFunc {
	if cfg.ConsecutiveFailures == 0 {
		cfg.ConsecutiveFailures = 5
	}
	threshold := cfg.ConsecutiveFailures

	cb := circuitbreaker.New[struct{}](circuitbreaker.Config{
		MaxRequests: cfg.MaxRequests,
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		ReadyToTrip: func(counts circuitbreaker.Counts) bool {
			return counts.ConsecutiveFailures >= threshold
		},
	})

	return func(c *browse.Context) {
		savedIdx := c.SaveIndex()

		_, err := cb.Execute(c.GoContext(), func(ctx context.Context) (struct{}, error) {
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
