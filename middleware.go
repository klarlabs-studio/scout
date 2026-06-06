package browse

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"

	"go.klarlabs.de/bolt"
)

// logger holds the package-level bolt logger (atomic for concurrent safety).
var logger atomic.Pointer[bolt.Logger]

func init() {
	l := bolt.New(bolt.NewConsoleHandler(os.Stderr))
	logger.Store(l)
}

func getLogger() *bolt.Logger {
	return logger.Load()
}

// SetLogger replaces the default logger used by the Logger and Recovery middleware.
func SetLogger(l *bolt.Logger) {
	logger.Store(l)
}

// Logger returns middleware that logs task execution using bolt.
func Logger() HandlerFunc {
	return func(c *Context) {
		start := time.Now()
		getLogger().Info().Str("task", c.TaskName()).Msg("task started")

		c.Next()

		elapsed := time.Since(start)
		if errs := c.Errors(); len(errs) > 0 {
			getLogger().Error().
				Str("task", c.TaskName()).
				Dur("duration", elapsed).
				Err(errs[0]).
				Msg("task failed")
		} else {
			getLogger().Info().
				Str("task", c.TaskName()).
				Dur("duration", elapsed).
				Msg("task completed")
		}
	}
}

// Recovery returns middleware that recovers from panics and records the error.
func Recovery() HandlerFunc {
	return func(c *Context) {
		defer func() {
			if r := recover(); r != nil {
				var err error
				switch v := r.(type) {
				case error:
					err = v
				default:
					err = fmt.Errorf("%v", v)
				}
				getLogger().Error().
					Str("task", c.TaskName()).
					Err(err).
					Str("stack", string(debug.Stack())).
					Msg("panic recovered")
				c.AbortWithError(err)
			}
		}()
		c.Next()
	}
}
