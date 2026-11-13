package browse

import (
	"testing"
	"time"
)

func TestFingerprintBrowserEndpointIncludesBrowserContext(t *testing.T) {
	base := fingerprintBrowserEndpoint("ws://127.0.0.1:9222/devtools/browser/secret\x00context-a")
	if base == "" || base != fingerprintBrowserEndpoint("ws://127.0.0.1:9222/devtools/browser/secret\x00context-a") {
		t.Fatal("browser identity fingerprint is empty or not deterministic")
	}
	if base == fingerprintBrowserEndpoint("ws://127.0.0.1:9222/devtools/browser/other\x00context-a") {
		t.Fatal("different browser endpoint produced the same identity")
	}
	if base == fingerprintBrowserEndpoint("ws://127.0.0.1:9222/devtools/browser/secret\x00context-b") {
		t.Fatal("different browser context produced the same identity")
	}
	if base == "ws://127.0.0.1:9222/devtools/browser/secret\x00context-a" {
		t.Fatal("fingerprint exposed the browser endpoint")
	}
}

func TestDefaultOptions(t *testing.T) {
	opts := defaultOptions()

	if !opts.headless {
		t.Error("default headless should be true")
	}
	if opts.timeout != 30*time.Second {
		t.Errorf("default timeout: got %v, want 30s", opts.timeout)
	}
	if opts.width != 1280 {
		t.Errorf("default width: got %d, want 1280", opts.width)
	}
	if opts.height != 720 {
		t.Errorf("default height: got %d, want 720", opts.height)
	}
	if opts.slowmo != 0 {
		t.Errorf("default slowmo should be 0, got %v", opts.slowmo)
	}
	if opts.userAgent != "" {
		t.Errorf("default userAgent should be empty, got %q", opts.userAgent)
	}
	if opts.proxyServer != "" {
		t.Errorf("default proxyServer should be empty, got %q", opts.proxyServer)
	}
	if opts.poolSize != 0 {
		t.Errorf("default poolSize should be 0, got %d", opts.poolSize)
	}
	if opts.allowPrivateIPs {
		t.Error("default allowPrivateIPs should be false")
	}
	if opts.remoteCDP != "" {
		t.Errorf("default remoteCDP should be empty, got %q", opts.remoteCDP)
	}
}

func TestWithHeadless(t *testing.T) {
	tests := []struct {
		name string
		val  bool
	}{
		{"true", true},
		{"false", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithHeadless(tt.val)})
			if opts.headless != tt.val {
				t.Errorf("WithHeadless(%v): got %v", tt.val, opts.headless)
			}
		})
	}
}

func TestWithChromePath(t *testing.T) {
	opts := applyOptions([]Option{WithChromePath("/custom/chrome")})
	if opts.chromePath != "/custom/chrome" {
		t.Fatalf("WithChromePath() = %q, want %q", opts.chromePath, "/custom/chrome")
	}
}

func TestWithTimeout(t *testing.T) {
	tests := []struct {
		name string
		dur  time.Duration
	}{
		{"1 second", 1 * time.Second},
		{"5 minutes", 5 * time.Minute},
		{"100ms", 100 * time.Millisecond},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithTimeout(tt.dur)})
			if opts.timeout != tt.dur {
				t.Errorf("WithTimeout(%v): got %v", tt.dur, opts.timeout)
			}
		})
	}
}

func TestWithSlowMotion(t *testing.T) {
	tests := []struct {
		name string
		dur  time.Duration
	}{
		{"500ms", 500 * time.Millisecond},
		{"1s", time.Second},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithSlowMotion(tt.dur)})
			if opts.slowmo != tt.dur {
				t.Errorf("WithSlowMotion(%v): got %v", tt.dur, opts.slowmo)
			}
		})
	}
}

func TestWithViewport(t *testing.T) {
	tests := []struct {
		name  string
		w, h  int
		wantW int
		wantH int
	}{
		{"1920x1080", 1920, 1080, 1920, 1080},
		{"800x600", 800, 600, 800, 600},
		{"zero", 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithViewport(tt.w, tt.h)})
			if opts.width != tt.wantW || opts.height != tt.wantH {
				t.Errorf("WithViewport(%d,%d): got %dx%d", tt.w, tt.h, opts.width, opts.height)
			}
		})
	}
}

func TestWithUserAgent(t *testing.T) {
	tests := []struct {
		name string
		ua   string
	}{
		{"custom UA", "Mozilla/5.0 Custom"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithUserAgent(tt.ua)})
			if opts.userAgent != tt.ua {
				t.Errorf("WithUserAgent(%q): got %q", tt.ua, opts.userAgent)
			}
		})
	}
}

func TestWithProxy(t *testing.T) {
	tests := []struct {
		name  string
		proxy string
	}{
		{"http proxy", "http://proxy:8080"},
		{"socks5 proxy", "socks5://proxy:1080"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithProxy(tt.proxy)})
			if opts.proxyServer != tt.proxy {
				t.Errorf("WithProxy(%q): got %q", tt.proxy, opts.proxyServer)
			}
		})
	}
}

func TestWithPoolSize(t *testing.T) {
	tests := []struct {
		name string
		n    int
	}{
		{"4 workers", 4},
		{"1 worker", 1},
		{"zero", 0},
		{"negative", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithPoolSize(tt.n)})
			if opts.poolSize != tt.n {
				t.Errorf("WithPoolSize(%d): got %d", tt.n, opts.poolSize)
			}
		})
	}
}

func TestWithRemoteCDP(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"ws URL", "ws://localhost:9222/devtools/browser/abc"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithRemoteCDP(tt.url)})
			if opts.remoteCDP != tt.url {
				t.Errorf("WithRemoteCDP(%q): got %q", tt.url, opts.remoteCDP)
			}
		})
	}
}

func TestWithAllowPrivateIPs(t *testing.T) {
	tests := []struct {
		name string
		val  bool
	}{
		{"true", true},
		{"false", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := applyOptions([]Option{WithAllowPrivateIPs(tt.val)})
			if opts.allowPrivateIPs != tt.val {
				t.Errorf("WithAllowPrivateIPs(%v): got %v", tt.val, opts.allowPrivateIPs)
			}
		})
	}
}

func TestApplyOptionsMultiple(t *testing.T) {
	opts := applyOptions([]Option{
		WithHeadless(false),
		WithTimeout(10 * time.Second),
		WithViewport(1920, 1080),
		WithUserAgent("test-agent"),
		WithProxy("http://proxy:8080"),
		WithPoolSize(4),
		WithAllowPrivateIPs(true),
		WithRemoteCDP("ws://localhost:9222"),
		WithSlowMotion(200 * time.Millisecond),
	})

	if opts.headless {
		t.Error("headless should be false")
	}
	if opts.timeout != 10*time.Second {
		t.Errorf("timeout: got %v", opts.timeout)
	}
	if opts.width != 1920 || opts.height != 1080 {
		t.Errorf("viewport: got %dx%d", opts.width, opts.height)
	}
	if opts.userAgent != "test-agent" {
		t.Errorf("userAgent: got %q", opts.userAgent)
	}
	if opts.proxyServer != "http://proxy:8080" {
		t.Errorf("proxyServer: got %q", opts.proxyServer)
	}
	if opts.poolSize != 4 {
		t.Errorf("poolSize: got %d", opts.poolSize)
	}
	if !opts.allowPrivateIPs {
		t.Error("allowPrivateIPs should be true")
	}
	if opts.remoteCDP != "ws://localhost:9222" {
		t.Errorf("remoteCDP: got %q", opts.remoteCDP)
	}
	if opts.slowmo != 200*time.Millisecond {
		t.Errorf("slowmo: got %v", opts.slowmo)
	}
}

func TestApplyOptionsLastWins(t *testing.T) {
	opts := applyOptions([]Option{
		WithHeadless(true),
		WithHeadless(false),
	})
	if opts.headless {
		t.Error("last option should win: headless should be false")
	}
}

func TestApplyOptionsEmpty(t *testing.T) {
	opts := applyOptions(nil)
	defaults := defaultOptions()

	if opts.headless != defaults.headless {
		t.Error("nil options should produce defaults")
	}
	if opts.timeout != defaults.timeout {
		t.Error("nil options should produce default timeout")
	}
}

func TestNewWithOptions(t *testing.T) {
	e := New(
		WithHeadless(false),
		WithTimeout(5*time.Second),
		WithViewport(800, 600),
		WithUserAgent("scout/test"),
		WithPoolSize(2),
		WithAllowPrivateIPs(true),
	)

	if e.opts.headless {
		t.Error("headless should be false")
	}
	if e.opts.timeout != 5*time.Second {
		t.Errorf("timeout: got %v", e.opts.timeout)
	}
	if e.opts.width != 800 || e.opts.height != 600 {
		t.Errorf("viewport: got %dx%d", e.opts.width, e.opts.height)
	}
	if e.opts.userAgent != "scout/test" {
		t.Errorf("userAgent: got %q", e.opts.userAgent)
	}
	if e.opts.poolSize != 2 {
		t.Errorf("poolSize: got %d", e.opts.poolSize)
	}
	if !e.opts.allowPrivateIPs {
		t.Error("allowPrivateIPs should be true")
	}
}
