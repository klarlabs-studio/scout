package middleware

import (
	"encoding/base64"
	"fmt"

	browse "go.klarlabs.de/scout"
)

// CookieAuth returns middleware that injects cookies before task execution.
func CookieAuth(cookies ...browse.Cookie) browse.HandlerFunc {
	return func(c *browse.Context) {
		page := c.Page()
		if page == nil {
			c.Next()
			return
		}
		for _, ck := range cookies {
			if err := page.SetCookie(ck); err != nil {
				c.AbortWithError(fmt.Errorf("browse: failed to set auth cookie: %w", err))
				return
			}
		}
		c.Next()
	}
}

// BasicAuth returns middleware that injects HTTP Basic Authentication headers.
func BasicAuth(username, password string) browse.HandlerFunc {
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return HeaderAuth("Authorization", "Basic "+encoded)
}

// BearerAuth returns middleware that injects a Bearer token via extra headers.
func BearerAuth(token string) browse.HandlerFunc {
	return HeaderAuth("Authorization", "Bearer "+token)
}

// HeaderAuth returns middleware that sets a custom header on all requests.
func HeaderAuth(name, value string) browse.HandlerFunc {
	return func(c *browse.Context) {
		page := c.Page()
		if page == nil {
			c.Next()
			return
		}
		_, _ = page.Call("Network.enable", nil)
		_, err := page.Call("Network.setExtraHTTPHeaders", map[string]any{
			"headers": map[string]string{
				name: value,
			},
		})
		if err != nil {
			c.AbortWithError(fmt.Errorf("browse: failed to set auth header: %w", err))
			return
		}
		c.Next()
	}
}
