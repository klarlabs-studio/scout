package middleware

import (
	"fmt"
	"time"

	browse "go.klarlabs.de/scout"
)

// BlockResources returns middleware that blocks requests for specified resource types.
// Common types: "Image", "Stylesheet", "Font", "Media", "Script".
// This speeds up page loads by not downloading unnecessary resources.
func BlockResources(resourceTypes ...string) browse.HandlerFunc {
	return func(c *browse.Context) {
		page := c.Page()
		if page == nil {
			c.Next()
			return
		}

		// Enable Fetch domain to intercept requests
		patterns := make([]map[string]string, 0, len(resourceTypes))
		for _, rt := range resourceTypes {
			patterns = append(patterns, map[string]string{
				"resourceType": rt,
			})
		}

		_, err := page.Call("Fetch.enable", map[string]any{
			"patterns": patterns,
		})
		if err != nil {
			// Fetch domain might not be available; proceed without blocking
			c.Next()
			return
		}

		// Listen for intercepted requests and fail them
		page.OnSession("Fetch.requestPaused", func(params map[string]any) {
			requestID, _ := params["requestId"].(string)
			if requestID != "" {
				_, _ = page.Call("Fetch.failRequest", map[string]any{
					"requestId":   requestID,
					"errorReason": "BlockedByClient",
				})
			}
		})

		c.Next()

		// Disable Fetch after task completes
		_, _ = page.Call("Fetch.disable", nil)
	}
}

// WaitNetworkIdle returns middleware that waits for network to become idle after task execution.
// It polls for zero in-flight requests for the given duration.
func WaitNetworkIdle(idleTime time.Duration) browse.HandlerFunc {
	if idleTime == 0 {
		idleTime = 500 * time.Millisecond
	}

	return func(c *browse.Context) {
		c.Next()

		page := c.Page()
		if page == nil {
			return
		}

		// Use JS to wait for no pending requests
		js := fmt.Sprintf(`new Promise(resolve => {
			let pending = 0;
			let timer;
			const done = () => { resolve(true); };
			const check = () => {
				clearTimeout(timer);
				timer = setTimeout(done, %d);
			};
			const observer = new PerformanceObserver(list => {
				for (const entry of list.getEntries()) {
					if (entry.duration === 0) pending++;
					else { pending--; check(); }
				}
			});
			try {
				observer.observe({entryTypes: ['resource']});
			} catch(e) {}
			check();
		})`, idleTime.Milliseconds())

		_, _ = page.Evaluate(js)
	}
}
