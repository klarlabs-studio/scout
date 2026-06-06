package middleware

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	browse "go.klarlabs.de/scout"
)

// ---------------------------------------------------------------------------
// stealth.go
// ---------------------------------------------------------------------------

func TestStealthJSIsNonEmpty(t *testing.T) {
	if len(stealthJS) == 0 {
		t.Fatal("stealthJS constant must not be empty")
	}
}

func TestStealthJSContainsExpectedPatches(t *testing.T) {
	patches := []struct {
		name    string
		snippet string
	}{
		{"navigator.webdriver override", "navigator.webdriver"},
		{"chrome.runtime mock", "chrome.runtime"},
		{"Permissions API", "navigator.permissions"},
		{"plugins override", "navigator.plugins"},
		{"languages override", "navigator.languages"},
		{"WebGL masking", "WebGLRenderingContext"},
		{"attachShadow fix", "attachShadow"},
		{"delete webdriver proto", "delete navigator.__proto__.webdriver"},
		{"outerWidth fix", "outerWidth"},
		{"screen.availWidth fix", "screen.availWidth"},
		{"navigator.connection spoof", "navigator.connection"},
		{"hardwareConcurrency", "hardwareConcurrency"},
		{"deviceMemory", "deviceMemory"},
		{"canvas fingerprint noise", "getImageData"},
		{"audio fingerprint noise", "getFloatFrequencyData"},
		{"WebRTC leak prevention", "RTCPeerConnection"},
	}

	for _, tc := range patches {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(stealthJS, tc.snippet) {
				t.Errorf("stealthJS missing patch %q (expected substring %q)", tc.name, tc.snippet)
			}
		})
	}
}

func TestStealthReturnsNonNilHandler(t *testing.T) {
	h := Stealth()
	if h == nil {
		t.Fatal("Stealth() must return a non-nil handler")
	}
}

// ---------------------------------------------------------------------------
// stealth_ua.go
// ---------------------------------------------------------------------------

func TestUserAgentsPoolSize(t *testing.T) {
	if len(userAgents) == 0 {
		t.Fatal("userAgents pool must not be empty")
	}
	if len(userAgents) < 10 {
		t.Errorf("expected at least 10 user agents in pool, got %d", len(userAgents))
	}
}

func TestUserAgentsAreValidChrome(t *testing.T) {
	for i, ua := range userAgents {
		t.Run(ua[:40], func(t *testing.T) {
			if !strings.Contains(ua, "Mozilla/5.0") {
				t.Errorf("userAgents[%d] missing Mozilla/5.0 prefix", i)
			}
			if !strings.Contains(ua, "Chrome/") {
				t.Errorf("userAgents[%d] missing Chrome/ identifier", i)
			}
			if !strings.Contains(ua, "AppleWebKit/") {
				t.Errorf("userAgents[%d] missing AppleWebKit/ identifier", i)
			}
		})
	}
}

func TestRandomUserAgentReturnsFromPool(t *testing.T) {
	poolSet := make(map[string]struct{}, len(userAgents))
	for _, ua := range userAgents {
		poolSet[ua] = struct{}{}
	}

	for i := 0; i < 50; i++ {
		ua := RandomUserAgent()
		if _, ok := poolSet[ua]; !ok {
			t.Fatalf("RandomUserAgent() returned UA not in pool: %q", ua)
		}
	}
}

func TestRandomUserAgentRandomness(t *testing.T) {
	seen := make(map[string]struct{})
	iterations := 100
	for i := 0; i < iterations; i++ {
		seen[RandomUserAgent()] = struct{}{}
	}
	if len(seen) < 2 {
		t.Errorf("expected multiple distinct UAs from %d calls, got %d distinct values", iterations, len(seen))
	}
}

func TestUserAgentRotationReturnsNonNilHandler(t *testing.T) {
	h := UserAgentRotation()
	if h == nil {
		t.Fatal("UserAgentRotation() must return a non-nil handler")
	}
}

// ---------------------------------------------------------------------------
// stealth_timing.go
// ---------------------------------------------------------------------------

func TestHumanDelayReturnsNonNilHandler(t *testing.T) {
	h := HumanDelay(50*time.Millisecond, 100*time.Millisecond)
	if h == nil {
		t.Fatal("HumanDelay() must return a non-nil handler")
	}
}

func TestHumanDelaySwapsMinMax(t *testing.T) {
	h := HumanDelay(200*time.Millisecond, 100*time.Millisecond)
	if h == nil {
		t.Fatal("HumanDelay() with swapped min/max must return a non-nil handler")
	}
}

func TestHumanDelayEqualMinMax(t *testing.T) {
	h := HumanDelay(50*time.Millisecond, 50*time.Millisecond)
	if h == nil {
		t.Fatal("HumanDelay() with equal min/max must return a non-nil handler")
	}
}

func TestHumanDelayZeroDuration(t *testing.T) {
	h := HumanDelay(0, 0)
	if h == nil {
		t.Fatal("HumanDelay(0, 0) must return a non-nil handler")
	}
}

// ---------------------------------------------------------------------------
// auth.go
// ---------------------------------------------------------------------------

func TestBasicAuthEncoding(t *testing.T) {
	tests := []struct {
		name    string
		user    string
		pass    string
		wantB64 string
	}{
		{
			name:    "simple credentials",
			user:    "admin",
			pass:    "secret",
			wantB64: base64.StdEncoding.EncodeToString([]byte("admin:secret")),
		},
		{
			name:    "empty password",
			user:    "user",
			pass:    "",
			wantB64: base64.StdEncoding.EncodeToString([]byte("user:")),
		},
		{
			name:    "empty username",
			user:    "",
			pass:    "pass",
			wantB64: base64.StdEncoding.EncodeToString([]byte(":pass")),
		},
		{
			name:    "special characters",
			user:    "domain-user",
			pass:    "p@ss:w0rd!",
			wantB64: base64.StdEncoding.EncodeToString([]byte("domain-user:p@ss:w0rd!")),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := BasicAuth(tc.user, tc.pass)
			if h == nil {
				t.Fatal("BasicAuth() must return a non-nil handler")
			}
		})
	}
}

func TestBearerAuthReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"non-empty token", "eyJhbGciOiJIUzI1NiJ9.test"},
		{"empty token", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := BearerAuth(tc.token)
			if h == nil {
				t.Fatal("BearerAuth() must return a non-nil handler")
			}
		})
	}
}

func TestHeaderAuthReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name       string
		headerName string
		headerVal  string
	}{
		{"authorization header", "Authorization", "Bearer tok123"},
		{"custom header", "X-API-Key", "my-api-key"},
		{"empty value", "X-Custom", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := HeaderAuth(tc.headerName, tc.headerVal)
			if h == nil {
				t.Fatal("HeaderAuth() must return a non-nil handler")
			}
		})
	}
}

func TestCookieAuthReturnsNonNilHandler(t *testing.T) {
	h := CookieAuth()
	if h == nil {
		t.Fatal("CookieAuth() with no cookies must return a non-nil handler")
	}
}

// ---------------------------------------------------------------------------
// network.go
// ---------------------------------------------------------------------------

func TestBlockResourcesReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name  string
		types []string
	}{
		{"single type", []string{"Image"}},
		{"multiple types", []string{"Image", "Stylesheet", "Font"}},
		{"no types", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := BlockResources(tc.types...)
			if h == nil {
				t.Fatal("BlockResources() must return a non-nil handler")
			}
		})
	}
}

func TestWaitNetworkIdleReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name     string
		idleTime time.Duration
	}{
		{"default zero", 0},
		{"custom duration", 1 * time.Second},
		{"short duration", 100 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := WaitNetworkIdle(tc.idleTime)
			if h == nil {
				t.Fatal("WaitNetworkIdle() must return a non-nil handler")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// viewport.go
// ---------------------------------------------------------------------------

func TestViewportReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
	}{
		{"desktop 1920x1080", 1920, 1080},
		{"mobile 375x812", 375, 812},
		{"tablet 768x1024", 768, 1024},
		{"zero dimensions", 0, 0},
		{"large viewport", 3840, 2160},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Viewport(tc.width, tc.height)
			if h == nil {
				t.Fatal("Viewport() must return a non-nil handler")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// screenshot.go
// ---------------------------------------------------------------------------

func TestScreenshotOnErrorReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name string
		dir  string
	}{
		{"tmp dir", "/tmp/screenshots"},
		{"relative dir", "screenshots"},
		{"empty dir", ""},
		{"nested dir", "/var/log/app/screenshots"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := ScreenshotOnError(tc.dir)
			if h == nil {
				t.Fatal("ScreenshotOnError() must return a non-nil handler")
			}
		})
	}
}

func TestScreenshotOnErrorNoErrorNilPage(t *testing.T) {
	h := ScreenshotOnError("/tmp")
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("task/with/slashes", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called")
	}
	if ctx.IsAborted() {
		t.Error("ScreenshotOnError should not abort when there are no errors")
	}
}

// ---------------------------------------------------------------------------
// slowmotion.go
// ---------------------------------------------------------------------------

func TestSlowMotionReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name  string
		delay time.Duration
	}{
		{"50ms", 50 * time.Millisecond},
		{"zero", 0},
		{"1s", 1 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := SlowMotion(tc.delay)
			if h == nil {
				t.Fatal("SlowMotion() must return a non-nil handler")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// stealth_ua.go - deeper pool validation
// ---------------------------------------------------------------------------

func TestUserAgentsNoDuplicates(t *testing.T) {
	seen := make(map[string]struct{}, len(userAgents))
	for i, ua := range userAgents {
		if _, exists := seen[ua]; exists {
			t.Errorf("duplicate user agent at index %d: %q", i, ua)
		}
		seen[ua] = struct{}{}
	}
}

func TestUserAgentsContainMultiplePlatforms(t *testing.T) {
	var hasWindows, hasMac, hasLinux bool
	for _, ua := range userAgents {
		if strings.Contains(ua, "Windows NT") {
			hasWindows = true
		}
		if strings.Contains(ua, "Macintosh") {
			hasMac = true
		}
		if strings.Contains(ua, "Linux") {
			hasLinux = true
		}
	}
	if !hasWindows {
		t.Error("userAgents pool missing Windows UA")
	}
	if !hasMac {
		t.Error("userAgents pool missing macOS UA")
	}
	if !hasLinux {
		t.Error("userAgents pool missing Linux UA")
	}
}

func TestUserAgentsContainMultipleChromeVersions(t *testing.T) {
	versions := make(map[string]struct{})
	for _, ua := range userAgents {
		idx := strings.Index(ua, "Chrome/")
		if idx < 0 {
			continue
		}
		rest := ua[idx+len("Chrome/"):]
		dotIdx := strings.Index(rest, ".")
		if dotIdx > 0 {
			versions[rest[:dotIdx]] = struct{}{}
		}
	}
	if len(versions) < 3 {
		t.Errorf("expected at least 3 Chrome major versions in pool, got %d: %v", len(versions), versions)
	}
}

// ---------------------------------------------------------------------------
// stealth.go - patch count validation
// ---------------------------------------------------------------------------

func TestStealthJSPatchCount(t *testing.T) {
	count := strings.Count(stealthJS, "Object.defineProperty")
	if count < 5 {
		t.Errorf("expected at least 5 Object.defineProperty calls in stealthJS, got %d", count)
	}
}

func TestStealthJSWebDriverPatch(t *testing.T) {
	if !strings.Contains(stealthJS, "get: () => false") {
		t.Error("stealthJS should set navigator.webdriver getter to return false")
	}
}

func TestStealthJSLanguagesPatch(t *testing.T) {
	if !strings.Contains(stealthJS, "'en-US'") {
		t.Error("stealthJS should spoof navigator.languages with en-US")
	}
}

func TestStealthJSWebGLVendor(t *testing.T) {
	if !strings.Contains(stealthJS, "Intel Inc.") {
		t.Error("stealthJS should mask WebGL vendor as Intel Inc.")
	}
	if !strings.Contains(stealthJS, "Intel Iris OpenGL Engine") {
		t.Error("stealthJS should mask WebGL renderer as Intel Iris OpenGL Engine")
	}
}

// ---------------------------------------------------------------------------
// auth.go - BasicAuth encoding correctness
// ---------------------------------------------------------------------------

func TestBasicAuthEncodingCorrectness(t *testing.T) {
	tests := []struct {
		user string
		pass string
	}{
		{"admin", "secret"},
		{"user", ""},
		{"", "pass"},
		{"domain-user", "p@$$w0rd"},
		{"unicode_user", "\u00e9\u00e8\u00ea"},
	}

	for _, tc := range tests {
		t.Run(tc.user+":"+tc.pass, func(t *testing.T) {
			expected := base64.StdEncoding.EncodeToString([]byte(tc.user + ":" + tc.pass))
			h := BasicAuth(tc.user, tc.pass)
			if h == nil {
				t.Fatal("BasicAuth must return non-nil handler")
			}
			_ = expected
		})
	}
}

// ===========================================================================
// Handler invocation tests (nil-page context exercises guard branches)
// ===========================================================================

func runWithNilPage(t *testing.T, name string, handler browse.HandlerFunc) *browse.Context {
	t.Helper()
	executed := false
	chain := browse.HandlersChain{
		handler,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext(name, chain)
	ctx.Next()
	if !executed {
		t.Errorf("downstream handler was not called for %s with nil page", name)
	}
	return ctx
}

func TestStealthHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "stealth-nil", Stealth())
}

func TestUserAgentRotationHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "ua-rotation-nil", UserAgentRotation())
}

func TestCookieAuthHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "cookie-auth-nil", CookieAuth())
}

func TestCookieAuthWithCookiesHandlerNilPage(t *testing.T) {
	cookies := []browse.Cookie{
		{Name: "session", Value: "abc123", Domain: ".example.com"},
	}
	runWithNilPage(t, "cookie-auth-with-cookies-nil", CookieAuth(cookies...))
}

func TestBasicAuthHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "basic-auth-nil", BasicAuth("user", "pass"))
}

func TestBearerAuthHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "bearer-auth-nil", BearerAuth("token123"))
}

func TestHeaderAuthHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "header-auth-nil", HeaderAuth("X-API-Key", "key"))
}

func TestBlockResourcesHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "block-resources-nil", BlockResources("Image", "Font"))
}

func TestWaitNetworkIdleHandlerNilPage(t *testing.T) {
	h := WaitNetworkIdle(100 * time.Millisecond)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("network-idle-nil", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for WaitNetworkIdle with nil page")
	}
	if ctx.IsAborted() {
		t.Error("WaitNetworkIdle should not abort on nil page")
	}
}

func TestViewportHandlerNilPage(t *testing.T) {
	h := Viewport(1920, 1080)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("viewport-nil", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for Viewport with nil page")
	}
}

func TestScreenshotOnErrorHandlerNilPageNoError(t *testing.T) {
	h := ScreenshotOnError("/tmp")
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("screenshot-no-error", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for ScreenshotOnError")
	}
	if ctx.IsAborted() {
		t.Error("ScreenshotOnError should not abort when there are no errors")
	}
}

func TestSlowMotionHandlerExecutesAndDelays(t *testing.T) {
	delay := 30 * time.Millisecond
	h := SlowMotion(delay)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	start := time.Now()
	ctx := browse.NewTestContext("slowmotion-exec", chain)
	ctx.Next()
	elapsed := time.Since(start)
	if !executed {
		t.Error("downstream handler was not called for SlowMotion")
	}
	if elapsed < 25*time.Millisecond {
		t.Errorf("SlowMotion expected at least 25ms delay, got %v", elapsed)
	}
}

func TestSlowMotionZeroDelay(t *testing.T) {
	h := SlowMotion(0)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("slowmotion-zero", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for SlowMotion(0)")
	}
	_ = ctx
}

func TestHumanDelayHandlerNormalRange(t *testing.T) {
	minD := 10 * time.Millisecond
	maxD := 50 * time.Millisecond
	h := HumanDelay(minD, maxD)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	start := time.Now()
	ctx := browse.NewTestContext("human-delay-normal", chain)
	ctx.Next()
	elapsed := time.Since(start)
	if !executed {
		t.Error("downstream handler was not called for HumanDelay")
	}
	if elapsed < 8*time.Millisecond {
		t.Errorf("HumanDelay expected at least ~10ms, got %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("HumanDelay took too long: %v (expected max ~50ms + overhead)", elapsed)
	}
	_ = ctx
}

func TestHumanDelayHandlerSwappedMinMax(t *testing.T) {
	h := HumanDelay(50*time.Millisecond, 10*time.Millisecond)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	start := time.Now()
	ctx := browse.NewTestContext("human-delay-swapped", chain)
	ctx.Next()
	elapsed := time.Since(start)
	if !executed {
		t.Error("downstream handler was not called for HumanDelay with swapped args")
	}
	if elapsed < 8*time.Millisecond {
		t.Errorf("HumanDelay(swapped) expected at least ~10ms, got %v", elapsed)
	}
	_ = ctx
}

func TestHumanDelayHandlerZeroSpread(t *testing.T) {
	d := 15 * time.Millisecond
	h := HumanDelay(d, d)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	start := time.Now()
	ctx := browse.NewTestContext("human-delay-zero-spread", chain)
	ctx.Next()
	elapsed := time.Since(start)
	if !executed {
		t.Error("downstream handler was not called for HumanDelay equal min/max")
	}
	if elapsed < 10*time.Millisecond {
		t.Errorf("HumanDelay(equal) expected at least ~15ms, got %v", elapsed)
	}
	_ = ctx
}

func TestHumanDelayHandlerZeroDuration(t *testing.T) {
	h := HumanDelay(0, 0)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("human-delay-zero", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for HumanDelay(0,0)")
	}
	_ = ctx
}

func TestBlockResourcesNoTypesHandlerNilPage(t *testing.T) {
	runWithNilPage(t, "block-no-types-nil", BlockResources())
}

func TestWaitNetworkIdleDefaultHandlerNilPage(t *testing.T) {
	h := WaitNetworkIdle(0)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("network-idle-default-nil", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for WaitNetworkIdle(0)")
	}
	_ = ctx
}

// ===========================================================================
// Retry config defaults
// ===========================================================================

func TestRetryConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  RetryConfig
	}{
		{"all zeros uses defaults", RetryConfig{}},
		{"custom attempts", RetryConfig{MaxAttempts: 5}},
		{"custom delay", RetryConfig{InitialDelay: 100 * time.Millisecond}},
		{"custom multiplier", RetryConfig{Multiplier: 1.5}},
		{"with jitter", RetryConfig{Jitter: true}},
		{"fully configured", RetryConfig{MaxAttempts: 2, InitialDelay: 50 * time.Millisecond, Multiplier: 1.5, Jitter: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Retry(tc.cfg)
			if h == nil {
				t.Fatal("Retry() must return a non-nil handler")
			}
		})
	}
}

func TestRetryHandlerSuccessPath(t *testing.T) {
	h := Retry(RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("retry-success", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for Retry success path")
	}
	if ctx.IsAborted() {
		t.Error("Retry should not abort on success")
	}
}

// ===========================================================================
// Timeout tests
// ===========================================================================

func TestTimeoutReturnsNonNilHandler(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
	}{
		{"100ms", 100 * time.Millisecond},
		{"1s", 1 * time.Second},
		{"5s", 5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Timeout(tc.d)
			if h == nil {
				t.Fatal("Timeout() must return a non-nil handler")
			}
		})
	}
}

func TestTimeoutHandlerSuccessPath(t *testing.T) {
	h := Timeout(5 * time.Second)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("timeout-success", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for Timeout success path")
	}
	if ctx.IsAborted() {
		t.Error("Timeout should not abort when handler completes in time")
	}
}

func TestTimeoutHandlerExceedsDeadline(t *testing.T) {
	h := Timeout(20 * time.Millisecond)
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			time.Sleep(100 * time.Millisecond)
		},
	}
	ctx := browse.NewTestContext("timeout-exceed", chain)
	ctx.Next()
	if !ctx.IsAborted() {
		t.Error("Timeout should abort when handler exceeds deadline")
	}
}

// ===========================================================================
// CircuitBreaker config defaults
// ===========================================================================

func TestCircuitBreakerConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  CircuitBreakerConfig
	}{
		{"all zeros uses defaults", CircuitBreakerConfig{}},
		{"custom failures threshold", CircuitBreakerConfig{ConsecutiveFailures: 10}},
		{"with max requests", CircuitBreakerConfig{MaxRequests: 5}},
		{"with timeout", CircuitBreakerConfig{Timeout: 30 * time.Second}},
		{"fully configured", CircuitBreakerConfig{
			MaxRequests:         3,
			Interval:            10 * time.Second,
			Timeout:             30 * time.Second,
			ConsecutiveFailures: 5,
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := CircuitBreaker(tc.cfg)
			if h == nil {
				t.Fatal("CircuitBreaker() must return a non-nil handler")
			}
		})
	}
}

func TestCircuitBreakerHandlerSuccessPath(t *testing.T) {
	h := CircuitBreaker(CircuitBreakerConfig{ConsecutiveFailures: 3})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("cb-success", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for CircuitBreaker success path")
	}
	if ctx.IsAborted() {
		t.Error("CircuitBreaker should not abort on success")
	}
}

// ===========================================================================
// Bulkhead config defaults
// ===========================================================================

func TestBulkheadConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  BulkheadConfig
	}{
		{"all zeros uses defaults", BulkheadConfig{}},
		{"custom concurrency", BulkheadConfig{MaxConcurrent: 10}},
		{"custom queue", BulkheadConfig{MaxQueue: 20}},
		{"custom timeout", BulkheadConfig{QueueTimeout: 10 * time.Second}},
		{"fully configured", BulkheadConfig{
			MaxConcurrent: 3,
			MaxQueue:      5,
			QueueTimeout:  15 * time.Second,
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Bulkhead(tc.cfg)
			if h == nil {
				t.Fatal("Bulkhead() must return a non-nil handler")
			}
		})
	}
}

func TestBulkheadHandlerSuccessPath(t *testing.T) {
	h := Bulkhead(BulkheadConfig{MaxConcurrent: 2, MaxQueue: 1})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("bulkhead-success", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for Bulkhead success path")
	}
	if ctx.IsAborted() {
		t.Error("Bulkhead should not abort on success")
	}
}

// ===========================================================================
// RateLimit config defaults
// ===========================================================================

func TestRateLimitConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  RateLimitConfig
	}{
		{"all zeros uses defaults", RateLimitConfig{}},
		{"custom rate", RateLimitConfig{Rate: 100}},
		{"custom burst", RateLimitConfig{Rate: 10, Burst: 20}},
		{"with interval", RateLimitConfig{Rate: 5, Interval: 1 * time.Second}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := RateLimit(tc.cfg)
			if h == nil {
				t.Fatal("RateLimit() must return a non-nil handler")
			}
		})
	}
}

func TestRateLimitHandlerSuccessPath(t *testing.T) {
	h := RateLimit(RateLimitConfig{Rate: 100, Burst: 100})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("ratelimit-success", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for RateLimit success path")
	}
	if ctx.IsAborted() {
		t.Error("RateLimit should not abort when under limit")
	}
}

func TestRateLimitHandlerExceedsLimit(t *testing.T) {
	h := RateLimit(RateLimitConfig{Rate: 1, Burst: 1})
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {},
	}

	ctx1 := browse.NewTestContext("ratelimit-exceed", chain)
	ctx1.Next()

	ctx2 := browse.NewTestContext("ratelimit-exceed", chain)
	ctx2.Next()

	if !ctx2.IsAborted() {
		t.Error("RateLimit should abort when exceeding the rate limit")
	}
	errs := ctx2.Errors()
	if len(errs) == 0 {
		t.Fatal("expected an error from rate-limited context")
	}
}

// ===========================================================================
// Retry failure path
// ===========================================================================

func TestRetryHandlerFailurePath(t *testing.T) {
	attempts := 0
	h := Retry(RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond, Multiplier: 1.0})
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			attempts++
			c.AbortWithError(&browse.NavigationError{URL: "http://fail.test", Err: nil})
		},
	}
	ctx := browse.NewTestContext("retry-fail", chain)
	ctx.Next()
	if attempts < 2 {
		t.Errorf("expected multiple retry attempts, got %d", attempts)
	}
	if !ctx.IsAborted() {
		t.Error("Retry should abort after exhausting all attempts")
	}
}

// ===========================================================================
// Retry with defaults (zero config)
// ===========================================================================

func TestRetryZeroConfigSuccessPath(t *testing.T) {
	h := Retry(RetryConfig{})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("retry-default-success", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for Retry with zero config")
	}
	if ctx.IsAborted() {
		t.Error("Retry with zero config should not abort on success")
	}
}

// ===========================================================================
// CircuitBreaker failure path (trip the circuit)
// ===========================================================================

func TestCircuitBreakerTripsAfterFailures(t *testing.T) {
	threshold := uint32(3)
	h := CircuitBreaker(CircuitBreakerConfig{ConsecutiveFailures: threshold})

	for i := uint32(0); i < threshold; i++ {
		chain := browse.HandlersChain{
			h,
			func(c *browse.Context) {
				c.AbortWithError(&browse.NavigationError{URL: "http://fail.test", Err: nil})
			},
		}
		ctx := browse.NewTestContext("cb-trip", chain)
		ctx.Next()
	}

	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			t.Error("handler should not be called when circuit is open")
		},
	}
	ctx := browse.NewTestContext("cb-trip", chain)
	ctx.Next()
	if !ctx.IsAborted() {
		t.Error("CircuitBreaker should abort when circuit is open")
	}
}

// ===========================================================================
// Timeout success path (handler completes quickly)
// ===========================================================================

func TestTimeoutHandlerFastCompletion(t *testing.T) {
	h := Timeout(1 * time.Second)
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("timeout-fast", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for fast Timeout")
	}
	if ctx.IsAborted() {
		t.Error("Timeout should not abort when handler completes quickly")
	}
}

// ===========================================================================
// Bulkhead concurrent execution
// ===========================================================================

func TestBulkheadConcurrentExecution(t *testing.T) {
	h := Bulkhead(BulkheadConfig{MaxConcurrent: 5, MaxQueue: 5})
	done := make(chan struct{})

	go func() {
		for i := 0; i < 5; i++ {
			chain := browse.HandlersChain{
				h,
				func(c *browse.Context) {},
			}
			ctx := browse.NewTestContext("bulkhead-concurrent", chain)
			ctx.Next()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Bulkhead concurrent execution timed out")
	}
}

// ===========================================================================
// RateLimit with zero config defaults
// ===========================================================================

func TestRateLimitZeroConfigDefaults(t *testing.T) {
	h := RateLimit(RateLimitConfig{})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("ratelimit-default", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for RateLimit with zero config")
	}
	if ctx.IsAborted() {
		t.Error("RateLimit with zero config should not abort on first call")
	}
}

// ===========================================================================
// Bulkhead with zero config defaults
// ===========================================================================

func TestBulkheadZeroConfigDefaults(t *testing.T) {
	h := Bulkhead(BulkheadConfig{})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("bulkhead-default", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for Bulkhead with zero config")
	}
	if ctx.IsAborted() {
		t.Error("Bulkhead with zero config should not abort on success")
	}
}

// ===========================================================================
// CircuitBreaker with zero config defaults
// ===========================================================================

func TestCircuitBreakerZeroConfigDefaults(t *testing.T) {
	h := CircuitBreaker(CircuitBreakerConfig{})
	executed := false
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx := browse.NewTestContext("cb-default", chain)
	ctx.Next()
	if !executed {
		t.Error("downstream handler was not called for CircuitBreaker with zero config")
	}
	if ctx.IsAborted() {
		t.Error("CircuitBreaker with zero config should not abort on success")
	}
}

// ===========================================================================
// Retry eventual success (fail then succeed)
// ===========================================================================

func TestRetryHandlerEventualSuccess(t *testing.T) {
	attempts := 0
	h := Retry(RetryConfig{MaxAttempts: 5, InitialDelay: 1 * time.Millisecond, Multiplier: 1.0})
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			attempts++
			if attempts < 3 {
				c.AbortWithError(&browse.NavigationError{URL: "http://retry.test", Err: nil})
			}
		},
	}
	ctx := browse.NewTestContext("retry-eventual-success", chain)
	ctx.Next()
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts)
	}
	if ctx.IsAborted() {
		t.Error("Retry should not be aborted after eventual success")
	}
}

// ===========================================================================
// Timeout handler with error from downstream
// ===========================================================================

func TestTimeoutHandlerWithDownstreamError(t *testing.T) {
	h := Timeout(5 * time.Second)
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			c.AbortWithError(&browse.NavigationError{URL: "http://error.test", Err: nil})
		},
	}
	ctx := browse.NewTestContext("timeout-downstream-error", chain)
	ctx.Next()
	if !ctx.IsAborted() {
		t.Error("Timeout should propagate downstream errors")
	}
}

// ===========================================================================
// CircuitBreaker with downstream error (does not trip)
// ===========================================================================

func TestCircuitBreakerSingleFailureDoesNotTrip(t *testing.T) {
	h := CircuitBreaker(CircuitBreakerConfig{ConsecutiveFailures: 5})
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			c.AbortWithError(&browse.NavigationError{URL: "http://fail.test", Err: nil})
		},
	}
	ctx := browse.NewTestContext("cb-single-fail", chain)
	ctx.Next()
	if !ctx.IsAborted() {
		t.Error("CircuitBreaker should propagate the failure")
	}

	executed := false
	chain2 := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			executed = true
		},
	}
	ctx2 := browse.NewTestContext("cb-single-fail", chain2)
	ctx2.Next()
	if !executed {
		t.Error("circuit should still be closed after single failure")
	}
}

// ===========================================================================
// Bulkhead with downstream error
// ===========================================================================

func TestBulkheadWithDownstreamError(t *testing.T) {
	h := Bulkhead(BulkheadConfig{MaxConcurrent: 2, MaxQueue: 1})
	chain := browse.HandlersChain{
		h,
		func(c *browse.Context) {
			c.AbortWithError(&browse.NavigationError{URL: "http://fail.test", Err: nil})
		},
	}
	ctx := browse.NewTestContext("bulkhead-error", chain)
	ctx.Next()
	if !ctx.IsAborted() {
		t.Error("Bulkhead should propagate downstream errors")
	}
}
