//go:build integration

package browse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// redirectServer serves /start (302 → /blocked) and records whether /blocked was
// ever actually fetched, plus any X-Test header seen on /start.
func redirectServer(blockedHit *atomic.Bool, sawHeader *atomic.Bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") == "yes" {
			sawHeader.Store(true)
		}
		http.Redirect(w, r, "/blocked", http.StatusFound)
	})
	mux.HandleFunc("/blocked", func(w http.ResponseWriter, _ *http.Request) {
		blockedHit.Store(true)
		_, _ = w.Write([]byte("SECRET-INTERNAL-DATA"))
	})
	return httptest.NewServer(mux)
}

// TestIntegrationInterceptor_BlocksRedirect proves the shared interceptor fails a
// redirect at the CDP Fetch layer: with a rule blocking /blocked, Chrome must
// never fetch it even though /start redirects there. This is the mechanism the
// URL-policy (redirect-SSRF) guard relies on.
func TestIntegrationInterceptor_BlocksRedirect(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	var blockedHit, sawHeader atomic.Bool
	ts := redirectServer(&blockedHit, &sawHeader)
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	engine.Task("block", func(c *Context) {
		page := c.Page()
		remove, err := page.InterceptRequests(RequestRule{
			Name:          "block-blocked",
			ResourceTypes: []string{"Document"},
			Decide: func(r InterceptedRequest) RequestVerdict {
				if strings.Contains(r.URL, "/blocked") {
					return RequestVerdict{Block: true, BlockReason: "AccessDenied"}
				}
				return RequestVerdict{}
			},
		})
		if err != nil {
			c.AbortWithError(err)
			return
		}
		defer remove()
		_ = page.Navigate(ts.URL + "/start") // the redirect must be blocked
	})
	_ = engine.Run("block") // navigation may error on the blocked redirect — fine

	if blockedHit.Load() {
		t.Error("interceptor did not block the redirect: /blocked was fetched")
	}
}

// TestIntegrationInterceptor_AllowsWhenNoMatch proves the continue path doesn't
// break navigation: a rule that never blocks must let /start → /blocked proceed.
func TestIntegrationInterceptor_AllowsWhenNoMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	var blockedHit, sawHeader atomic.Bool
	ts := redirectServer(&blockedHit, &sawHeader)
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	engine.Task("allow", func(c *Context) {
		page := c.Page()
		remove, err := page.InterceptRequests(RequestRule{
			Name:          "noop",
			ResourceTypes: []string{"Document"},
			Decide:        func(InterceptedRequest) RequestVerdict { return RequestVerdict{} },
		})
		if err != nil {
			c.AbortWithError(err)
			return
		}
		defer remove()
		c.MustNavigate(ts.URL + "/start")
	})
	if err := engine.Run("allow"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !blockedHit.Load() {
		t.Error("interceptor broke navigation: /blocked never reached with a no-op rule")
	}
}

// TestIntegrationInterceptor_InjectsSameOriginHeader proves header injection is
// applied to same-origin requests via the continue-with-headers path.
func TestIntegrationInterceptor_InjectsSameOriginHeader(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	var blockedHit, sawHeader atomic.Bool
	ts := redirectServer(&blockedHit, &sawHeader)
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	engine.Task("hdr", func(c *Context) {
		page := c.Page()
		remove, err := page.InterceptRequests(RequestRule{
			Name:          "inject",
			ResourceTypes: []string{"Document"},
			Decide: func(r InterceptedRequest) RequestVerdict {
				if r.SameOriginAsTop() {
					return RequestVerdict{AddHeaders: map[string]string{"X-Test": "yes"}}
				}
				return RequestVerdict{}
			},
		})
		if err != nil {
			c.AbortWithError(err)
			return
		}
		defer remove()
		_ = page.Navigate(ts.URL + "/start")
	})
	_ = engine.Run("hdr")

	if !sawHeader.Load() {
		t.Error("same-origin header injection failed: /start did not receive X-Test")
	}
}
