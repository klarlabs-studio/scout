package middleware

import (
	browse "go.klarlabs.de/scout"
)

// Viewport returns middleware that sets the viewport dimensions for the task's page.
// This overrides the engine-level viewport for specific tasks or groups.
func Viewport(width, height int) browse.HandlerFunc {
	return func(c *browse.Context) {
		page := c.Page()
		if page != nil {
			_ = page.SetViewport(width, height)
		}
		c.Next()
	}
}
