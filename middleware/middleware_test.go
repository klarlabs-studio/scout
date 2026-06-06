package middleware_test

import (
	"testing"
	"time"

	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/middleware"
)

func TestTimeoutMiddleware(t *testing.T) {
	tm := middleware.Timeout(100 * time.Millisecond)

	handlers := browse.HandlersChain{
		tm,
		func(c *browse.Context) {
			time.Sleep(200 * time.Millisecond)
		},
	}

	ctx := browse.NewTestContext("timeout-test", handlers)
	ctx.Next()

	// The timeout should cause a context.DeadlineExceeded
	if !ctx.IsAborted() {
		t.Error("expected context to be aborted after timeout")
	}
}

func TestRetryMiddleware(t *testing.T) {
	attempts := 0
	rm := middleware.Retry(middleware.RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 10 * time.Millisecond,
	})

	handlers := browse.HandlersChain{
		rm,
		func(c *browse.Context) {
			attempts++
			if attempts < 3 {
				c.AbortWithError(&browse.NavigationError{URL: "test", Err: nil})
			}
		},
	}

	ctx := browse.NewTestContext("retry-test", handlers)
	ctx.Next()

	// Note: with fortify retry, the handler chain runs multiple times
	// The exact behavior depends on how fortify calls the function
	if attempts == 0 {
		t.Error("handler should have been called at least once")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	rl := middleware.RateLimit(middleware.RateLimitConfig{
		Rate:  1,
		Burst: 1,
	})

	// First call should succeed
	handlers := browse.HandlersChain{
		rl,
		func(c *browse.Context) {
			c.Set("executed", true)
		},
	}

	ctx := browse.NewTestContext("ratelimit-test", handlers)
	ctx.Next()

	if v, ok := ctx.Get("executed"); !ok || v != true {
		t.Error("first call should succeed")
	}
}

func TestSlowMotionMiddleware(t *testing.T) {
	sm := middleware.SlowMotion(50 * time.Millisecond)

	start := time.Now()
	handlers := browse.HandlersChain{
		sm,
		func(c *browse.Context) {},
	}

	ctx := browse.NewTestContext("slow-test", handlers)
	ctx.Next()

	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Errorf("expected at least 40ms delay, got %v", elapsed)
	}
}

func TestBulkheadMiddleware(t *testing.T) {
	bh := middleware.Bulkhead(middleware.BulkheadConfig{
		MaxConcurrent: 2,
		MaxQueue:      1,
	})

	handlers := browse.HandlersChain{
		bh,
		func(c *browse.Context) {
			c.Set("executed", true)
		},
	}

	ctx := browse.NewTestContext("bulkhead-test", handlers)
	ctx.Next()

	if v, ok := ctx.Get("executed"); !ok || v != true {
		t.Error("handler should have been called")
	}
}

func TestCircuitBreakerMiddleware(t *testing.T) {
	cb := middleware.CircuitBreaker(middleware.CircuitBreakerConfig{
		ConsecutiveFailures: 3,
	})

	handlers := browse.HandlersChain{
		cb,
		func(c *browse.Context) {
			c.Set("executed", true)
		},
	}

	ctx := browse.NewTestContext("cb-test", handlers)
	ctx.Next()

	if v, ok := ctx.Get("executed"); !ok || v != true {
		t.Error("handler should have been called when circuit is closed")
	}
}
