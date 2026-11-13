// Example: profile demonstrates saving browser cookies and localStorage, then
// restoring them into a fresh Scout session.
//
// Usage:
//
//	go run ./examples/profile
//
// By default, the profile is created in a private temporary directory and
// removed after the example. An explicit -profile path is preserved. Profile
// files can contain authentication state and are written with mode 0600.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

func main() {
	profilePath := flag.String("profile", "", "Path for the saved profile (default: private temporary file, removed after the example)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Usage: go run ./examples/profile [-profile path]")
		os.Exit(2)
	}
	path := *profilePath
	if path == "" {
		dir, err := os.MkdirTemp("", "scout-profile-example-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "create temporary profile directory: %v\n", err)
			os.Exit(1)
		}
		defer os.RemoveAll(dir)
		path = filepath.Join(dir, "profile.json")
	}
	if err := run(ctx, path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, profilePath string) (retErr error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Scout profile demo</title></head><body>
<main><h1>Profile demo</h1><p id="status">checking saved state</p></main>
<script>
const cookie = document.cookie.split('; ').find(v => v.startsWith('scout_demo='));
const saved = localStorage.getItem('scout_demo');
document.querySelector('#status').textContent = cookie && saved === 'restored'
  ? 'profile restored: ' + cookie.split('=')[1] + ' / ' + saved
  : 'fresh session';
</script></body></html>`)
	}))
	defer server.Close()

	session, err := newSession(ctx)
	if err != nil {
		return err
	}
	if _, err := session.NavigateWithPostconditionContext(ctx, server.URL, agent.Postcondition{Text: "Profile demo"}); err != nil {
		_ = closeSession(session)
		return fmt.Errorf("navigate: %w", err)
	}
	if err := session.SetCookieContext(ctx, agent.CookieInput{Name: "scout_demo", Value: "saved", Domain: "127.0.0.1", Path: "/"}); err != nil {
		_ = closeSession(session)
		return fmt.Errorf("set demo cookie: %w", err)
	}
	if _, err := session.EvalContext(ctx, `localStorage.setItem('scout_demo', 'restored')`); err != nil {
		_ = closeSession(session)
		return fmt.Errorf("set demo localStorage: %w", err)
	}
	if err := session.SaveProfileContext(ctx, profilePath); err != nil {
		_ = closeSession(session)
		return fmt.Errorf("save profile: %w", err)
	}
	if err := closeSession(session); err != nil {
		return fmt.Errorf("close original session: %w", err)
	}

	// This second session is independent and begins with a fresh local browser.
	session, err = newSession(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := closeSession(session); err != nil && retErr == nil {
			retErr = fmt.Errorf("close restored session: %w", err)
		}
	}()
	if err := session.LoadProfileContext(ctx, profilePath); err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	result, err := session.AssertTextContainsContext(ctx, "profile restored: saved / restored", "#status", false)
	if err != nil {
		return fmt.Errorf("verify restored page: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("profile did not restore page state: %s", result.Reason)
	}
	fmt.Printf("Saved and restored URL, cookie, and localStorage in separate sessions: %s\n", result.Snippet)
	return nil
}

func newSession(ctx context.Context) (*agent.Session, error) {
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless:        true,
		Timeout:         15 * time.Second,
		AllowPrivateIPs: true,
	})
	if err != nil {
		return nil, fmt.Errorf("start Scout session: %w", err)
	}
	return session, nil
}

func closeSession(session *agent.Session) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return session.CloseContext(ctx)
}
