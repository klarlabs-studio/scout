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

// HeaderAuth returns middleware that injects a custom header, scoped to the
// navigation's own origin. Unlike a session-wide Network.setExtraHTTPHeaders
// (which sends the header to every host the page contacts), this adds the header
// only to same-origin requests — so an auth token isn't leaked to cross-origin
// subresources or a redirect destination.
func HeaderAuth(name, value string) browse.HandlerFunc {
	return func(c *browse.Context) {
		page := c.Page()
		if page == nil {
			c.Next()
			return
		}
		remove, err := page.InterceptRequests(browse.RequestRule{
			Name: "auth-header:" + name,
			Decide: func(r browse.InterceptedRequest) browse.RequestVerdict {
				if r.SameOriginAsTop() {
					return browse.RequestVerdict{AddHeaders: map[string]string{name: value}}
				}
				return browse.RequestVerdict{}
			},
		})
		if err != nil {
			c.AbortWithError(fmt.Errorf("browse: failed to set auth header: %w", err))
			return
		}
		defer remove()
		c.Next()
	}
}
