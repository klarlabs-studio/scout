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

		blocked := make(map[string]struct{}, len(resourceTypes))
		for _, rt := range resourceTypes {
			blocked[rt] = struct{}{}
		}

		// Register a rule on the page's shared request interceptor so resource
		// blocking coexists with the URL-policy guard and header injection
		// instead of fighting over Fetch.enable.
		remove, err := page.InterceptRequests(browse.RequestRule{
			Name:          "block-resources",
			ResourceTypes: resourceTypes,
			Decide: func(r browse.InterceptedRequest) browse.RequestVerdict {
				if _, ok := blocked[r.ResourceType]; ok {
					return browse.RequestVerdict{Block: true, BlockReason: "BlockedByClient"}
				}
				return browse.RequestVerdict{}
			},
		})
		if err != nil {
			// Fetch domain might not be available; proceed without blocking.
			c.Next()
			return
		}
		defer remove()

		c.Next()
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
