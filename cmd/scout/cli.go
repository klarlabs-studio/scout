package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/scout/agent"
)

// cliSession wraps an agent.Session for single-command execution.
type cliSession struct {
	session           *agent.Session
	initialNavigation *agent.PageResult
	browserStartupMS  int64
	navigationMS      int64
	cleanupMS         int64
}

type cliExecutionTiming struct {
	BrowserStartupMS             int64 `json:"browser_startup_ms"`
	PageNavigationVerificationMS int64 `json:"page_navigation_verification_ms"`
	BrowserCleanupMS             int64 `json:"browser_cleanup_ms"`
}

type cliNavigationOutput struct {
	*agent.PageResult
	ExecutionTiming cliExecutionTiming `json:"execution_timing"`
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

func (f cliFlags) getBool(key string, defaultVal bool) (bool, error) {
	v, ok := f.flags[key]
	if !ok {
		return defaultVal, nil
	}
	if v == "" {
		return true, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid --%s value %q (use true or false)", key, v)
	}
	return b, nil
}

func (f cliFlags) getDuration(key string, defaultVal time.Duration) (time.Duration, error) {
	v, ok := f.flags[key]
	if !ok {
		return defaultVal, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s duration %q: %w", key, v, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--%s must be greater than zero", key)
	}
	return d, nil
}

func clickPostcondition(flags cliFlags) (agent.Postcondition, error) {
	return parsePostcondition(flags, "click", "wait")
}

func navigatePostcondition(flags cliFlags) (agent.Postcondition, error) {
	return parsePostcondition(flags, "navigate")
}

type submitOptions struct {
	matchURL   string
	timeoutMS  int
	expected   agent.Postcondition
	acceptance agent.Postcondition
	hasAccept  bool
}

func parseSubmitOptions(flags cliFlags) (submitOptions, error) {
	allowed := map[string]struct{}{
		"headless": {}, "timeout": {}, "match-url": {}, "timeout-ms": {}, "allow-private-ips": {},
		"target-id":     {},
		"expected-text": {}, "expected-url-contains": {}, "expected-visible-selector": {},
		"expected-value-selector": {}, "expected-value": {}, "expected-count-selector": {}, "expected-count": {},
		"acceptance-text": {}, "acceptance-visible-selector": {}, "acceptance-value-selector": {},
		"acceptance-value": {}, "acceptance-count-selector": {}, "acceptance-count": {},
	}
	for key := range flags.flags {
		if _, ok := allowed[key]; !ok {
			return submitOptions{}, fmt.Errorf("unknown submit flag --%s", key)
		}
	}
	options := submitOptions{matchURL: flags.get("match-url", "")}
	if raw, ok := flags.flags["timeout-ms"]; ok {
		timeout, err := strconv.Atoi(raw)
		if err != nil || timeout < 0 {
			return submitOptions{}, fmt.Errorf("invalid --timeout-ms value %q: expected a non-negative integer", raw)
		}
		options.timeoutMS = timeout
	}
	expectedFlags := make(map[string]string)
	acceptanceFlags := make(map[string]string)
	for source, target := range map[string]string{
		"expected-text": "expected-text", "expected-url-contains": "expected-url-contains",
		"expected-visible-selector": "expected-visible-selector", "expected-value-selector": "expected-value-selector",
		"expected-value": "expected-value", "expected-count-selector": "expected-count-selector", "expected-count": "expected-count",
	} {
		if value, ok := flags.flags[source]; ok {
			expectedFlags[target] = value
		}
	}
	for source, target := range map[string]string{
		"acceptance-text": "expected-text", "acceptance-visible-selector": "expected-visible-selector",
		"acceptance-value-selector": "expected-value-selector", "acceptance-value": "expected-value",
		"acceptance-count-selector": "expected-count-selector", "acceptance-count": "expected-count",
	} {
		if value, ok := flags.flags[source]; ok {
			acceptanceFlags[target] = value
		}
	}
	var err error
	options.expected, err = parsePostcondition(cliFlags{flags: expectedFlags}, "submit")
	if err != nil {
		return submitOptions{}, err
	}
	options.hasAccept = len(acceptanceFlags) > 0
	if options.hasAccept && len(expectedFlags) > 0 {
		return submitOptions{}, fmt.Errorf("--acceptance-* flags cannot be combined with --expected-* flags")
	}
	if options.hasAccept {
		options.acceptance, err = parsePostcondition(cliFlags{flags: acceptanceFlags}, "submit")
		if err != nil {
			return submitOptions{}, err
		}
	}
	return options, nil
}

func parsePostcondition(flags cliFlags, command string, extraAllowed ...string) (agent.Postcondition, error) {
	allowed := map[string]struct{}{"headless": {}, "timeout": {}, "target-id": {}, "allow-private-ips": {}}
	for _, key := range extraAllowed {
		allowed[key] = struct{}{}
	}
	for _, key := range []string{"expected-text", "expected-url-contains", "expected-visible-selector", "expected-value-selector", "expected-value", "expected-count-selector", "expected-count"} {
		allowed[key] = struct{}{}
	}
	for key := range flags.flags {
		if _, ok := allowed[key]; !ok {
			return agent.Postcondition{}, fmt.Errorf("unknown %s flag --%s", command, key)
		}
	}
	condition := agent.Postcondition{
		Text:            flags.get("expected-text", ""),
		URLContains:     flags.get("expected-url-contains", ""),
		VisibleSelector: flags.get("expected-visible-selector", ""),
		ValueSelector:   flags.get("expected-value-selector", ""),
		Value:           flags.get("expected-value", ""),
		CountSelector:   flags.get("expected-count-selector", ""),
	}
	if raw, ok := flags.flags["expected-count"]; ok {
		count, err := strconv.Atoi(raw)
		if err != nil {
			return agent.Postcondition{}, fmt.Errorf("invalid --expected-count value %q: must be an integer", raw)
		}
		condition.Count = &count
	}
	provided := false
	for _, key := range []string{"expected-text", "expected-url-contains", "expected-visible-selector", "expected-value-selector", "expected-value", "expected-count-selector", "expected-count"} {
		if _, ok := flags.flags[key]; ok {
			provided = true
			break
		}
	}
	if provided {
		if err := condition.Validate(); err != nil {
			return agent.Postcondition{}, err
		}
	}
	return condition, nil
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
	exitOnErr(runOnePageWithPostcondition(url, flags, agent.Postcondition{}, func(s *cliSession) error {
		fn(s)
		return nil
	}))
}

func runOnePageWithPostcondition(url string, flags cliFlags, condition agent.Postcondition, fn func(s *cliSession) error) error {
	// Set up + navigate before installing the cleanup defer, so the failure
	// exits below don't skip a pending Close().
	opened, err := openSession(url, flags, condition)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = opened.session.Close()
		}
	}()

	// Run the action
	callErr := fn(opened)
	cleanupStarted := time.Now()
	closeErr := opened.session.Close()
	opened.cleanupMS = time.Since(cleanupStarted).Milliseconds()
	closed = true
	if closeErr != nil {
		closeErr = fmt.Errorf("browser cleanup failed: %w", closeErr)
	}
	return errors.Join(callErr, closeErr)
}

type pagePostconditionError struct {
	result *agent.PageResult
}

func navigationPostconditionError(result *agent.PageResult) error {
	if result != nil && result.Outcome == string(agent.OutcomePostconditionFailed) {
		return &pagePostconditionError{result: result}
	}
	return nil
}

func (e *pagePostconditionError) Error() string {
	if e == nil || e.result == nil {
		return "page postcondition failed"
	}
	if e.result.Evidence != "" {
		return "page postcondition failed: " + e.result.Evidence
	}
	return "page postcondition failed"
}

// openSession launches a browser session and navigates to url. On any failure
// it closes the partially-initialized session and returns an error, so callers
// can exit without a pending cleanup defer.
func openSession(url string, flags cliFlags, condition agent.Postcondition) (*cliSession, error) {
	// CLI defaults to visible browser; use --headless for scripts/CI
	headless, err := flags.getBool("headless", false)
	if err != nil {
		return nil, err
	}
	timeout, err := flags.getDuration("timeout", 30*time.Second)
	if err != nil {
		return nil, err
	}
	allowPrivateIPs, err := flags.getBool("allow-private-ips", false)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	startupStarted := time.Now()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless:        headless,
		Timeout:         timeout,
		AllowPrivateIPs: allowPrivateIPs,
		RemoteCDP:       os.Getenv("SCOUT_REMOTE_CDP"),
		ProxyServer:     configuredProxyServer(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}
	startupMS := time.Since(startupStarted).Milliseconds()

	ctx = agent.WithExpectedTargetID(ctx, flags.get("target-id", ""))
	navigationStarted := time.Now()
	navigation, err := session.NavigateWithPostconditionContext(ctx, url, condition)
	navigationMS := time.Since(navigationStarted).Milliseconds()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("navigation failed: %w", err)
	}

	return &cliSession{session: session, initialNavigation: navigation, browserStartupMS: startupMS, navigationMS: navigationMS}, nil
}
