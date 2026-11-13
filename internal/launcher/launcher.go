// Package launcher finds and starts a Chrome/Chromium process.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"
)

// Browser holds the launched browser process and its DevTools WebSocket URL.
type Browser struct {
	cmd      *exec.Cmd
	wsURL    string
	port     int
	dataDir  string
	waitOnce sync.Once
	waitDone chan struct{}
	waitErr  error
}

// WSEndpoint returns the DevTools WebSocket debugger URL.
func (b *Browser) WSEndpoint() string {
	return b.wsURL
}

// DevToolsURL returns the local HTTP discovery URL for browser automation
// clients that accept a CDP endpoint instead of a browser WebSocket URL.
func (b *Browser) DevToolsURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", b.port)
}

// ProcessID returns the operating-system process ID of the launched browser.
// It is zero when the Browser was not created with a live local process.
func (b *Browser) ProcessID() int {
	if b == nil || b.cmd == nil || b.cmd.Process == nil {
		return 0
	}
	return b.cmd.Process.Pid
}

// Close kills the browser process and cleans up the temp profile.
func (b *Browser) Close() error {
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	return b.CloseContext(context.Background())
}

// CloseContext waits for the browser process to exit until ctx expires, then
// kills it as a fallback. Engine.CloseContext sends Browser.close over CDP
// before calling this method so Chromium can stop its helper processes cleanly.
func (b *Browser) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		forcedKill := false
		b.waitOnce.Do(func() {
			b.waitDone = make(chan struct{})
			go func() {
				b.waitErr = b.cmd.Wait()
				close(b.waitDone)
			}()
		})
		select {
		case <-b.waitDone:
		case <-ctx.Done():
			if err := b.cmd.Process.Kill(); err == nil || errors.Is(err, os.ErrProcessDone) {
				forcedKill = true
			}
			<-b.waitDone
		}
		// A successful forced kill is the documented cleanup fallback. The
		// child process reports that signal as an ExitError, but cleanup itself
		// succeeded and the temporary profile is removed below.
		if forcedKill {
			b.waitErr = nil
		}
	}
	if b.dataDir != "" {
		_ = os.RemoveAll(b.dataDir)
	}
	return b.waitErr
}

// Options for launching the browser.
type Options struct {
	Headless    bool
	Port        int
	ProxyServer string
	ChromePath  string
	ChromeArgs  []string
}

// Launch starts a Chrome/Chromium instance and returns the connection info.
func Launch(opts Options) (*Browser, error) {
	return LaunchContext(context.Background(), opts)
}

// LaunchContext starts Chrome and waits for its DevTools endpoint within ctx.
// Cancellation during startup kills the new process and removes its profile;
// the context does not own the lifetime of a successfully launched browser.
func LaunchContext(ctx context.Context, opts Options) (*Browser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.ProxyServer != "" {
		for _, arg := range opts.ChromeArgs {
			if protectedProxyArgument(arg) {
				return nil, fmt.Errorf("browse: Chrome argument %q conflicts with configured proxy policy", strings.SplitN(arg, "=", 2)[0])
			}
		}
	}
	chromePath, err := findChromeAt(opts.ChromePath, opts.ChromePath == "")
	if err != nil {
		return nil, err
	}

	if opts.Port == 0 {
		opts.Port, err = freePort()
		if err != nil {
			return nil, fmt.Errorf("browse: failed to find free port: %w", err)
		}
	}

	dataDir, err := os.MkdirTemp("", "browse-go-*")
	if err != nil {
		return nil, fmt.Errorf("browse: failed to create temp dir: %w", err)
	}

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", opts.Port),
		fmt.Sprintf("--user-data-dir=%s", dataDir),
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
	if enabled(os.Getenv("SCOUT_CHROME_NO_SANDBOX")) {
		args = append([]string{"--no-sandbox"}, args...)
	}

	if opts.Headless {
		args = append([]string{"--headless=new"}, args...)
	}
	if opts.ProxyServer != "" {
		resolverRules, err := proxyHostResolverRules(opts.ProxyServer)
		if err != nil {
			_ = os.RemoveAll(dataDir)
			return nil, err
		}
		args = append([]string{
			fmt.Sprintf("--proxy-server=%s", opts.ProxyServer),
			// Chromium bypasses loopback destinations by default. Disable that
			// exception so a configured policy proxy also sees localhost traffic.
			"--proxy-bypass-list=<-loopback>",
			resolverRules,
			// WebRTC otherwise sends STUN/ICE UDP directly, outside the browser
			// proxy. Keep UDP only when the configured proxy supports it. Use the
			// policy switch; the "force-" preference-mimicking switch is weaker.
			"--webrtc-ip-handling-policy=disable_non_proxied_udp",
		}, args...)
	}
	// Append caller tuning flags last for non-policy switches. Proxy routing,
	// resolver isolation, and WebRTC egress controls are protected below so a
	// duplicate Chrome argument cannot silently override the configured proxy.
	args = append(args, opts.ChromeArgs...)

	cmd := exec.Command(chromePath, args...) //nolint:gosec // G204: launching the resolved Chrome binary is the launcher's purpose
	cmd.Stderr = nil
	cmd.Stdout = nil

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("browse: failed to start chrome: %w", err)
	}

	wsURL, err := waitForDevToolsContext(ctx, opts.Port, 10*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(dataDir)
		return nil, err
	}

	return &Browser{cmd: cmd, wsURL: wsURL, port: opts.Port, dataDir: dataDir}, nil
}

func protectedProxyArgument(arg string) bool {
	for _, prefix := range []string{"--proxy-server", "--no-proxy-server", "--proxy-pac-url", "--proxy-auto-detect", "--proxy-bypass-list", "--host-resolver-rules", "--webrtc-ip-handling-policy", "--force-webrtc-ip-handling-policy", "--disable-webrtc"} {
		if arg == prefix || strings.HasPrefix(arg, prefix+"=") {
			return true
		}
	}
	return false
}

func enabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// proxyHostResolverRules prevents Chromium from resolving destination names
// locally while proxied. The configured proxy host is the only exception so
// Chromium can reach that proxy; URL destinations are resolved upstream.
func proxyHostResolverRules(proxyServer string) (string, error) {
	parsed, err := url.Parse(proxyServer)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("browse: invalid proxy URL for host resolver policy")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
	default:
		return "", fmt.Errorf("browse: unsupported proxy scheme for host resolver policy")
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("browse: invalid proxy URL for host resolver policy")
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	} else {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil || host == "" {
			return "", fmt.Errorf("browse: invalid proxy hostname for host resolver policy")
		}
		host = strings.ToLower(host)
	}
	if strings.ContainsAny(host, ", \t\r\n") {
		return "", fmt.Errorf("browse: invalid proxy hostname for host resolver policy")
	}
	return "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE " + host, nil
}

// FindChromePath resolves the browser executable Scout will launch, without
// starting it. It is also used by diagnostics so their result matches launch.
func FindChromePath() (string, error) {
	return findChrome()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return 0, fmt.Errorf("browse: unexpected listener address type")
	}
	port := addr.Port
	_ = l.Close()
	return port, nil
}

// findChrome searches for any Chromium-based browser.
// Checks Chrome, Edge, Brave, Arc, Opera, Vivaldi, and Chromium in order.
func findChrome() (string, error) {
	return findChromeAt(os.Getenv("SCOUT_CHROME_PATH"), true)
}

func findChromeAt(configuredPath string, fromEnvironment bool) (string, error) {
	configured := strings.TrimSpace(configuredPath)
	setting := "ChromePath"
	if fromEnvironment {
		setting = "SCOUT_CHROME_PATH"
	}
	if configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("browse: %s %q is not an executable browser: %w", setting, configured, err)
		}
		return path, nil
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			// Chrome
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			// Edge
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			// Brave
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			// Arc
			"/Applications/Arc.app/Contents/MacOS/Arc",
			// Opera
			"/Applications/Opera.app/Contents/MacOS/Opera",
			// Vivaldi
			"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
			// Chromium
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "linux":
		candidates = []string{
			"google-chrome", "google-chrome-stable",
			"microsoft-edge", "microsoft-edge-stable",
			"brave-browser", "brave-browser-stable",
			"opera",
			"vivaldi", "vivaldi-stable",
			"chromium", "chromium-browser", "headless-shell", "chrome-headless-shell",
		}
	case "windows":
		for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if root == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(root, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
				filepath.Join(root, "Opera Software", "Opera Stable", "opera.exe"),
				filepath.Join(root, "Vivaldi", "Application", "vivaldi.exe"),
			)
		}
	}

	for _, c := range candidates {
		if filepath.IsAbs(c) {
			if _, err := os.Stat(c); err == nil { //nolint:gosec // G703: candidate Chrome paths come from a known list + operator env/config, not attacker input
				return c, nil
			}
		} else {
			if p, err := exec.LookPath(c); err == nil {
				return p, nil
			}
		}
	}

	return "", fmt.Errorf("browse: chrome/chromium not found; install Chrome or set PATH")
}

func waitForDevToolsContext(ctx context.Context, port int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
		cancel()
		if err == nil {
			_ = conn.Close()
			wsURL, err := fetchWSURLContext(ctx, port)
			if err == nil {
				return wsURL, nil
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", fmt.Errorf("browse: timeout waiting for devtools on port %d", port)
}

func fetchWSURLContext(ctx context.Context, port int) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // local devtools endpoint
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("browse: failed to parse devtools version: %w", err)
	}
	if info.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("browse: empty webSocketDebuggerUrl")
	}
	return info.WebSocketDebuggerURL, nil
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func fetchWSURL(port int) (string, error) {
	return fetchWSURLContext(context.Background(), port)
}
