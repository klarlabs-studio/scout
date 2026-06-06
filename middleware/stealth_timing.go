package middleware

import (
	"crypto/rand"
	"math/big"
	"time"

	browse "go.klarlabs.de/scout"
)

// HumanDelay returns middleware that adds a random delay between min and max
// before each action, simulating human interaction timing.
// Uses crypto/rand for non-deterministic timing that resists fingerprinting.
func HumanDelay(min, max time.Duration) browse.HandlerFunc {
	if max < min {
		min, max = max, min
	}
	spread := max - min
	return func(c *browse.Context) {
		if spread > 0 {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(spread)))
			if err == nil {
				time.Sleep(min + time.Duration(n.Int64()))
			} else {
				time.Sleep(min)
			}
		} else {
			time.Sleep(min)
		}
		c.Next()
	}
}
