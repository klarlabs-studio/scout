package browse

import "time"

// options holds configuration for the Engine.
type options struct {
	headless               bool
	timeout                time.Duration
	slowmo                 time.Duration
	width                  int
	height                 int
	userAgent              string
	proxyServer            string
	poolSize               int
	allowPrivateIPs        bool
	allowedOrigins         []string
	policyID               string
	remoteCDP              string
	remoteBrowserContext   bool
	remoteBrowserContextID string
	chromePath             string
	chromeArgs             []string
}

func defaultOptions() options {
	return options{
		headless: true,
		timeout:  30 * time.Second,
		width:    1280,
		height:   720,
	}
}

// Option configures an Engine.
type Option func(*options)

func applyOptions(opts []Option) options {
	o := defaultOptions()
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// WithHeadless sets whether the browser runs in headless mode.
func WithHeadless(h bool) Option {
	return func(o *options) { o.headless = h }
}

// WithChromeArgs appends arguments to a locally launched Chrome process.
// It is intended for browser features that require explicit Chrome flags.
// When WithProxy is configured, arguments that override proxy routing, host
// resolver isolation, or WebRTC egress policy are rejected. It has no effect
// when attaching through WithRemoteCDP.
func WithChromeArgs(args ...string) Option {
	return func(o *options) { o.chromeArgs = append([]string(nil), args...) }
}

// WithChromePath selects the executable used for a locally launched browser.
// It overrides SCOUT_CHROME_PATH and automatic discovery. It has no effect
// when WithRemoteCDP is configured.
func WithChromePath(path string) Option {
	return func(o *options) { o.chromePath = path }
}

// WithTimeout sets the default timeout for browser operations.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithSlowMotion adds artificial delay between actions.
func WithSlowMotion(d time.Duration) Option {
	return func(o *options) { o.slowmo = d }
}

// WithViewport sets the browser viewport dimensions.
func WithViewport(width, height int) Option {
	return func(o *options) {
		o.width = width
		o.height = height
	}
}

// WithUserAgent sets a custom User-Agent string for all pages.
func WithUserAgent(ua string) Option {
	return func(o *options) { o.userAgent = ua }
}

// WithProxy routes a locally launched browser through the specified proxy
// server. It cannot configure a browser attached through WithRemoteCDP; using
// both options makes Launch return an error. Format: "http://host:port" or
// "socks5://host:port".
func WithProxy(proxy string) Option {
	return func(o *options) { o.proxyServer = proxy }
}

// WithPoolSize sets the number of reusable pages in the page pool.
// When > 0, RunAll executes tasks concurrently up to this limit.
// Default 0 means sequential execution with no pooling.
func WithPoolSize(n int) Option {
	return func(o *options) { o.poolSize = n }
}

// WithRemoteCDP connects to an already-running Chrome instance via its browser
// WebSocket URL or an HTTP(S) DevTools endpoint. HTTP(S) endpoints are resolved
// through /json/version on every launch, allowing Reset to reconnect after a
// browser restart that changes the WebSocket path. Use with Browserbase, Steel,
// or self-hosted Chrome.
//
// engine := browse.New(browse.WithRemoteCDP("http://localhost:9222"))
func WithRemoteCDP(endpoint string) Option {
	return func(o *options) { o.remoteCDP = endpoint }
}

// WithRemoteBrowserContext creates an isolated incognito BrowserContext on a
// remote CDP browser. Scout owns and disposes this context for the Engine.
func WithRemoteBrowserContext() Option {
	return func(o *options) { o.remoteBrowserContext = true }
}

// WithRemoteBrowserContextID attaches to an existing remote BrowserContext.
// Treat the identifier as sensitive operational state and pair it only with
// its trusted RemoteCDP endpoint. This is intended for owner-controlled
// recovery, not arbitrary caller selection.
func WithRemoteBrowserContextID(id string) Option {
	return func(o *options) { o.remoteBrowserContextID = id }
}

// WithAllowPrivateIPs permits navigation to private/loopback IP addresses.
// By default, navigation to private IPs is blocked to prevent SSRF.
// Enable for testing or internal network automation.
func WithAllowPrivateIPs(allow bool) Option {
	return func(o *options) { o.allowPrivateIPs = allow }
}

// WithAllowedOrigins restricts top-level navigation, redirects, and
// page-target Fetch-interceptable HTTP(S) requests to exact origins. Scheme and
// non-default port are part of the origin; default ports are normalized and
// paths are ignored. It includes subresources, fetch/XHR, and downloads, but
// does not govern service-worker-owned traffic or WebSocket handshakes. For
// browser-wide egress control, configure an enforcing proxy with WithProxy.
// Remote CDP browser hosts must enforce their own network policy.
func WithAllowedOrigins(origins ...string) Option {
	return func(o *options) { o.allowedOrigins = append([]string(nil), origins...) }
}

// WithPolicyID attaches an opaque policy identifier to URL policy denials.
// Embedding applications can use this to correlate denials with receipts.
func WithPolicyID(id string) Option {
	return func(o *options) { o.policyID = id }
}
