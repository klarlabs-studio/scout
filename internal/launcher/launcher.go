// Package launcher finds and starts a Chrome/Chromium process.
package launcher

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Browser holds the launched browser process and its DevTools WebSocket URL.
type Browser struct {
	cmd     *exec.Cmd
	wsURL   string
	dataDir string
}

// WSEndpoint returns the DevTools WebSocket debugger URL.
func (b *Browser) WSEndpoint() string {
	return b.wsURL
}

// Close kills the browser process and cleans up the temp profile.
func (b *Browser) Close() error {
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
	}
	if b.dataDir != "" {
		_ = os.RemoveAll(b.dataDir)
	}
	return nil
}

// Options for launching the browser.
type Options struct {
	Headless    bool
	Port        int
	ProxyServer string
}

// Launch starts a Chrome/Chromium instance and returns the connection info.
func Launch(opts Options) (*Browser, error) {
	chromePath, err := findChrome()
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

	if opts.Headless {
		args = append([]string{"--headless=new"}, args...)
	}
	if opts.ProxyServer != "" {
		args = append([]string{fmt.Sprintf("--proxy-server=%s", opts.ProxyServer)}, args...)
	}

	cmd := exec.Command(chromePath, args...) //nolint:gosec // G204: launching the resolved Chrome binary is the launcher's purpose
	cmd.Stderr = nil
	cmd.Stdout = nil

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("browse: failed to start chrome: %w", err)
	}

	wsURL, err := waitForDevTools(opts.Port, 10*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(dataDir)
		return nil, err
	}

	return &Browser{cmd: cmd, wsURL: wsURL, dataDir: dataDir}, nil
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
			"chromium", "chromium-browser",
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

func waitForDevTools(port int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			wsURL, err := fetchWSURL(port)
			if err == nil {
				return wsURL, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("browse: timeout waiting for devtools on port %d", port)
}

func fetchWSURL(port int) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)

	resp, err := http.Get(url) //nolint:gosec // local devtools endpoint
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
