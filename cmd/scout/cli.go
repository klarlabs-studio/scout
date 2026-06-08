package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/scout/agent"
)

// cliSession wraps an agent.Session for single-command execution.
type cliSession struct {
	session *agent.Session
}

// cliFlags holds parsed --key=value flags.
type cliFlags struct {
	flags map[string]string
}

func (f cliFlags) get(key, defaultVal string) string {
	if v, ok := f.flags[key]; ok {
		return v
	}
	return defaultVal
}

func (f cliFlags) getBool(key string, defaultVal bool) bool {
	v, ok := f.flags[key]
	if !ok {
		return defaultVal
	}
	return v == "true" || v == "1" || v == ""
}

func (f cliFlags) getDuration(key string, defaultVal time.Duration) time.Duration {
	v, ok := f.flags[key]
	if !ok {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultVal
	}
	return d
}

// parseFlags extracts --key=value flags from args.
func parseFlags(args []string) cliFlags {
	flags := make(map[string]string)
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") {
			key := strings.TrimPrefix(arg, "--")
			if idx := strings.IndexByte(key, '='); idx >= 0 {
				flags[key[:idx]] = key[idx+1:]
			} else {
				flags[key] = ""
			}
		}
	}
	return cliFlags{flags: flags}
}

// runOnePage creates a session, navigates to the URL, runs the action, and cleans up.
func runOnePage(url string, flags cliFlags, fn func(s *cliSession)) {
	// Set up + navigate before installing the cleanup defer, so the failure
	// exits below don't skip a pending Close().
	session, err := openSession(url, flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = session.Close() }()

	// Run the action
	fn(&cliSession{session: session})
}

// openSession launches a browser session and navigates to url. On any failure
// it closes the partially-initialized session and returns an error, so callers
// can exit without a pending cleanup defer.
func openSession(url string, flags cliFlags) (*agent.Session, error) {
	// CLI defaults to visible browser; use --headless for scripts/CI
	headless := flags.getBool("headless", false)
	timeout := flags.getDuration("timeout", 30*time.Second)

	session, err := agent.NewSession(agent.SessionConfig{
		Headless:        headless,
		Timeout:         timeout,
		AllowPrivateIPs: true, // CLI usage is trusted
	})
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	if _, err := session.Navigate(url); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("navigation failed: %w", err)
	}

	return session, nil
}
