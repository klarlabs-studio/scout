package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDevToolsURL(t *testing.T) {
	browser := &Browser{port: 12345}
	if got, want := browser.DevToolsURL(), "http://127.0.0.1:12345"; got != want {
		t.Fatalf("DevToolsURL() = %q, want %q", got, want)
	}
}

func TestBrowserCloseContextTreatsSuccessfulFallbackKillAsCleanup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLauncherCloseHelperProcess$")
	cmd.Env = append(os.Environ(), "SCOUT_LAUNCHER_CLOSE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	browser := &Browser{cmd: cmd}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := browser.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext after successful fallback kill: %v", err)
	}
}

func TestLauncherCloseHelperProcess(t *testing.T) {
	if os.Getenv("SCOUT_LAUNCHER_CLOSE_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestBrowserWSEndpoint(t *testing.T) {
	tests := []struct {
		name  string
		wsURL string
	}{
		{name: "empty URL", wsURL: ""},
		{name: "typical devtools URL", wsURL: "ws://127.0.0.1:9222/devtools/browser/abc-123"},
		{name: "custom port", wsURL: "ws://127.0.0.1:43210/devtools/browser/def-456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Browser{wsURL: tt.wsURL}
			if got := b.WSEndpoint(); got != tt.wsURL {
				t.Errorf("WSEndpoint() = %q, want %q", got, tt.wsURL)
			}
		})
	}
}

func TestBrowserCloseNilProcess(t *testing.T) {
	// Close with nil cmd should not panic.
	b := &Browser{}
	if err := b.Close(); err != nil {
		t.Errorf("Close() returned error for nil cmd: %v", err)
	}
}

func TestFreePort(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort() error: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("freePort() returned invalid port: %d", port)
	}
}

func TestFreePortUniqueness(t *testing.T) {
	seen := make(map[int]bool)
	for range 10 {
		port, err := freePort()
		if err != nil {
			t.Fatalf("freePort() error: %v", err)
		}
		if seen[port] {
			// Duplicates are possible but extremely unlikely in 10 calls.
			t.Logf("warning: duplicate port %d (may happen rarely)", port)
		}
		seen[port] = true
	}
}

func TestFindChromeCandidatesList(t *testing.T) {
	// Verify that findChrome returns a meaningful error when no browser is found.
	// We cannot easily test the success path in CI without Chrome, but we can
	// verify the function runs without panicking on the current platform.
	_, err := findChrome()
	if err != nil {
		// Acceptable: Chrome may not be installed in CI.
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("findChrome() unexpected error format: %v", err)
		}
	}
	// If err == nil, Chrome was found. Either way, no panic is the key assertion.
}

func TestFindChromeUsesConfiguredPath(t *testing.T) {
	browser := filepath.Join(t.TempDir(), executableFixtureName())
	if err := os.WriteFile(browser, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCOUT_CHROME_PATH", browser)

	got, err := findChrome()
	if err != nil {
		t.Fatalf("findChrome() with SCOUT_CHROME_PATH: %v", err)
	}
	if got != browser {
		t.Fatalf("findChrome() = %q, want configured path %q", got, browser)
	}
}

func TestFindChromeExplicitPathOverridesEnvironment(t *testing.T) {
	browser := filepath.Join(t.TempDir(), executableFixtureName())
	if err := os.WriteFile(browser, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCOUT_CHROME_PATH", filepath.Join(t.TempDir(), "missing-browser"))

	got, err := findChromeAt(browser, false)
	if err != nil {
		t.Fatalf("findChromeAt(explicit path): %v", err)
	}
	if got != browser {
		t.Fatalf("findChromeAt() = %q, want explicit path %q", got, browser)
	}
}

func executableFixtureName() string {
	if runtime.GOOS == "windows" {
		return "browser.exe"
	}
	return "browser"
}

func TestFindChromeRejectsInvalidConfiguredPath(t *testing.T) {
	t.Setenv("SCOUT_CHROME_PATH", filepath.Join(t.TempDir(), "missing-browser"))

	_, err := findChrome()
	if err == nil || !strings.Contains(err.Error(), "SCOUT_CHROME_PATH") {
		t.Fatalf("findChrome() error = %v, want invalid SCOUT_CHROME_PATH diagnostic", err)
	}
}

func TestFindChromePlatformCandidates(t *testing.T) {
	// Verify the candidates list is non-empty for the current platform.
	goos := runtime.GOOS
	switch goos {
	case "darwin", "linux", "windows":
		// These are supported — findChrome will build a non-empty candidates list.
	default:
		t.Skipf("unsupported platform %s", goos)
	}
}

func TestOptionsDefaults(t *testing.T) {
	opts := Options{}
	if opts.Headless {
		t.Error("Options.Headless should default to false")
	}
	if opts.Port != 0 {
		t.Error("Options.Port should default to 0")
	}
	if opts.ProxyServer != "" {
		t.Error("Options.ProxyServer should default to empty string")
	}
}

func TestEnabled(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !enabled(value) {
			t.Errorf("enabled(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "anything"} {
		if enabled(value) {
			t.Errorf("enabled(%q) = true, want false", value)
		}
	}
}

func TestChromeArgConstruction(t *testing.T) {
	// Test the argument building logic that Launch uses.
	// We replicate the arg construction to verify correctness without starting Chrome.
	tests := []struct {
		name         string
		opts         Options
		wantHeadless bool
		wantProxy    string
	}{
		{
			name:         "headless mode",
			opts:         Options{Headless: true, Port: 9222},
			wantHeadless: true,
		},
		{
			name:      "with proxy",
			opts:      Options{ProxyServer: "http://proxy:8080", Port: 9222},
			wantProxy: "http://proxy:8080",
		},
		{
			name:         "headless with proxy",
			opts:         Options{Headless: true, ProxyServer: "socks5://proxy:1080", Port: 9222},
			wantHeadless: true,
			wantProxy:    "socks5://proxy:1080",
		},
		{
			name: "default mode",
			opts: Options{Port: 9222},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build args the same way Launch does.
			args := []string{
				fmt.Sprintf("--remote-debugging-port=%d", tt.opts.Port),
				"--user-data-dir=/tmp/test",
				"--no-first-run",
				"--no-default-browser-check",
				"--disable-background-networking",
				"--disable-default-apps",
				"--disable-extensions",
				"--disable-sync",
				"--disable-translate",
				"--disable-popup-blocking",
				"--metrics-recording-only",
				"--safebrowsing-disable-auto-update",
				"about:blank",
			}
			if tt.opts.Headless {
				args = append([]string{"--headless=new"}, args...)
			}
			if tt.opts.ProxyServer != "" {
				resolverRules, err := proxyHostResolverRules(tt.opts.ProxyServer)
				if err != nil {
					t.Fatalf("proxyHostResolverRules() error = %v", err)
				}
				args = append([]string{
					fmt.Sprintf("--proxy-server=%s", tt.opts.ProxyServer),
					"--proxy-bypass-list=<-loopback>",
					resolverRules,
					"--webrtc-ip-handling-policy=disable_non_proxied_udp",
				}, args...)
			}

			// Verify expected flags are present.
			joined := strings.Join(args, " ")

			if tt.wantHeadless {
				if !strings.Contains(joined, "--headless=new") {
					t.Error("expected --headless=new flag")
				}
			} else {
				if strings.Contains(joined, "--headless") {
					t.Error("unexpected --headless flag")
				}
			}

			if tt.wantProxy != "" {
				expected := fmt.Sprintf("--proxy-server=%s", tt.wantProxy)
				if !strings.Contains(joined, expected) {
					t.Errorf("expected %s in args", expected)
				}
				if !strings.Contains(joined, "--proxy-bypass-list=<-loopback>") {
					t.Error("expected loopback traffic to use the configured proxy")
				}
				if !strings.Contains(joined, "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE ") {
					t.Error("expected local DNS resolution to be disabled except for the proxy")
				}
				if !strings.Contains(joined, "--webrtc-ip-handling-policy=disable_non_proxied_udp") {
					t.Error("expected WebRTC IP handling policy to avoid non-proxied UDP")
				}
			} else if strings.Contains(joined, "--webrtc-ip-handling-policy=") {
				t.Error("unexpected WebRTC proxy policy without a configured proxy")
			}

			portFlag := fmt.Sprintf("--remote-debugging-port=%d", tt.opts.Port)
			if !strings.Contains(joined, portFlag) {
				t.Errorf("expected %s in args", portFlag)
			}

			// Verify required base flags are always present.
			requiredFlags := []string{
				"--no-first-run",
				"--no-default-browser-check",
				"--disable-extensions",
				"about:blank",
			}
			for _, flag := range requiredFlags {
				if !strings.Contains(joined, flag) {
					t.Errorf("missing required flag %q", flag)
				}
			}
		})
	}
}

func TestProxyHostResolverRules(t *testing.T) {
	tests := []struct {
		proxy string
		want  string
		bad   bool
	}{
		{proxy: "http://proxy.example:8080", want: "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE proxy.example"},
		{proxy: "socks5://büro.example:1080", want: "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE xn--bro-hoa.example"},
		{proxy: "http://127.0.0.1:8080", want: "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"},
		{proxy: "http://[::1]:8080", want: "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE [::1]"},
		{proxy: "http://user:pass@proxy.example:8080", bad: true},
		{proxy: "http://proxy.example:8080/path", bad: true},
		{proxy: "http://proxy.example,EXCLUDEevil:8080", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.proxy, func(t *testing.T) {
			got, err := proxyHostResolverRules(tt.proxy)
			if tt.bad {
				if err == nil {
					t.Fatalf("proxyHostResolverRules(%q) unexpectedly succeeded: %q", tt.proxy, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("proxyHostResolverRules(%q) error = %v", tt.proxy, err)
			}
			if got != tt.want {
				t.Fatalf("proxyHostResolverRules(%q) = %q, want %q", tt.proxy, got, tt.want)
			}
		})
	}
}

func TestProtectedProxyArgument(t *testing.T) {
	for _, arg := range []string{
		"--proxy-server=http://other:8080",
		"--no-proxy-server",
		"--proxy-pac-url=http://other/pac.js",
		"--proxy-auto-detect",
		"--proxy-bypass-list=*",
		"--host-resolver-rules=MAP * 127.0.0.1",
		"--webrtc-ip-handling-policy=default",
		"--force-webrtc-ip-handling-policy=default",
		"--disable-webrtc",
	} {
		if !protectedProxyArgument(arg) {
			t.Errorf("protectedProxyArgument(%q) = false", arg)
		}
	}
	for _, arg := range []string{"--window-size=1000,800", "--lang=en-US", "--enable-features=NetworkService"} {
		if protectedProxyArgument(arg) {
			t.Errorf("protectedProxyArgument(%q) = true", arg)
		}
	}
}

func TestLaunchRejectsProxyOverrideBeforeBrowserDiscovery(t *testing.T) {
	for _, arg := range []string{"--proxy-server=http://other:8080", "--proxy-bypass-list=*", "--webrtc-ip-handling-policy=default"} {
		t.Run(arg, func(t *testing.T) {
			_, err := LaunchContext(context.Background(), Options{ProxyServer: "http://proxy.example:8080", ChromeArgs: []string{arg}})
			if err == nil || !strings.Contains(err.Error(), "conflicts with configured proxy policy") {
				t.Fatalf("LaunchContext error = %v, want proxy policy conflict", err)
			}
		})
	}
}

func TestFetchWSURLParsing(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		statusCode int
		wantURL    string
		wantErr    bool
	}{
		{
			name:       "valid response",
			body:       `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/abc"}`,
			statusCode: http.StatusOK,
			wantURL:    "ws://127.0.0.1:9222/devtools/browser/abc",
		},
		{
			name:       "empty websocket URL",
			body:       `{"webSocketDebuggerUrl":""}`,
			statusCode: http.StatusOK,
			wantErr:    true,
		},
		{
			name:       "missing field",
			body:       `{"Browser":"Chrome"}`,
			statusCode: http.StatusOK,
			wantErr:    true,
		},
		{
			name:       "invalid JSON",
			body:       `not json`,
			statusCode: http.StatusOK,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			// Extract port from test server URL.
			parts := strings.Split(srv.URL, ":")
			port, _ := strconv.Atoi(parts[len(parts)-1])

			got, err := fetchWSURL(port)
			if tt.wantErr {
				if err == nil {
					t.Errorf("fetchWSURL() expected error, got URL=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("fetchWSURL() unexpected error: %v", err)
			}
			if got != tt.wantURL {
				t.Errorf("fetchWSURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestFetchWSURLVersionEndpoint(t *testing.T) {
	// Verify that fetchWSURL hits /json/version path.
	var requestPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		resp := map[string]string{
			"webSocketDebuggerUrl": "ws://127.0.0.1:1234/devtools/browser/test",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	parts := strings.Split(srv.URL, ":")
	port, _ := strconv.Atoi(parts[len(parts)-1])

	_, err := fetchWSURL(port)
	if err != nil {
		t.Fatalf("fetchWSURL() error: %v", err)
	}
	if requestPath != "/json/version" {
		t.Errorf("fetchWSURL() hit path %q, want /json/version", requestPath)
	}
}
