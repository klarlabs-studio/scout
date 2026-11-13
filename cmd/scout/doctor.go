package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/scout/agent"
	"go.klarlabs.de/scout/internal/launcher"
)

type doctorCheck struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Code        string `json:"code"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	Remediation string `json:"remediation"`
	Detail      string `json:"detail"`
}

type doctorReport struct {
	SchemaVersion int           `json:"schema_version"`
	Version       string        `json:"version"`
	Commit        string        `json:"commit"`
	Status        string        `json:"status"`
	Checks        []doctorCheck `json:"checks"`
}

type doctorSession interface {
	StatusContext(context.Context) (*agent.SessionStatus, error)
	ObserveContext(context.Context) (*agent.Observation, error)
	OpenTabContext(context.Context, string, string) (*agent.PageResult, error)
	ListTabsContext(context.Context) ([]agent.TabInfo, error)
	SwitchTabForTargetContext(context.Context, string, string) (*agent.PageResult, error)
	CloseTabResultForTargetContext(context.Context, string, string) (*agent.PageResult, error)
	AssertTextContainsContext(context.Context, string, string, bool) (*agent.AssertResult, error)
	CloseContext(context.Context) error
}

var newDoctorSession = func(ctx context.Context, cfg agent.SessionConfig) (doctorSession, error) {
	return agent.NewSessionContext(ctx, cfg)
}

var findDoctorBrowser = launcher.FindChromePath

// runDoctor performs local, read-only configuration checks. It never launches
// a browser, contacts a CDP endpoint, creates a store, or prints secret values.

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func runDoctorWithOptions(taskStorePath, profile, requiredTools, httpListen string, live bool, liveTimeout time.Duration) doctorReport {
	return runDoctorWithProbeOptions(taskStorePath, profile, requiredTools, httpListen, live, liveTimeout, "", "")
}

func runDoctorWithProbeOptions(taskStorePath, profile, requiredTools, httpListen string, live bool, liveTimeout time.Duration, probeURL, probeText string) doctorReport {
	report := doctorReport{
		SchemaVersion: 3,
		Version:       version,
		Commit:        commit,
		Status:        "ok",
		Checks:        make([]doctorCheck, 0, 12),
	}
	addWithRemediation := func(name, status, remediation, detail string) {
		report.Checks = append(report.Checks, doctorCheck{
			Name: name, Status: status, Code: doctorCheckCode(name, status),
			Remediation: remediation, Detail: detail,
		})
		//nolint:gocritic // This branch layout is clearer for this multi-condition check.
		if status == "error" {
			report.Status = "error"
		} else if status == "warning" && report.Status == "ok" {
			report.Status = "warning"
		} else if name == "browser_live" && status == "not_checked" && report.Status == "ok" {
			// Keep a configuration-only preflight from looking like an overall
			// browser-readiness pass to callers that inspect only the summary.
			report.Status = "warning"
		}
	}
	add := func(name, status, detail string) {
		addWithRemediation(name, status, doctorRemediation(name, status), detail)
	}
	addTimed := func(name, status, detail string, started time.Time) {
		addWithRemediation(name, status, doctorRemediation(name, status), detail)
		duration := time.Since(started).Milliseconds()
		if duration == 0 {
			duration = 1
		}
		report.Checks[len(report.Checks)-1].DurationMS = duration
	}
	if err := validateMCPRequiredTools(profile, requiredTools); err != nil {
		add("mcp_profile", "error", "MCP profile or required-tools configuration is invalid")
	} else {
		add("mcp_profile", "ok", "MCP profile and required tools are valid")
	}
	proxyServer := configuredProxyServer()
	remoteCDP := strings.TrimSpace(os.Getenv("SCOUT_REMOTE_CDP"))
	proxyConfigInvalid := false
	//nolint:gocritic // This branch layout is clearer for this multi-condition check.
	if proxyServer != "" && agent.ValidateProxyServer(proxyServer) != nil {
		proxyConfigInvalid = true
		add("browser_proxy", "error", "SCOUT_PROXY_SERVER must be an absolute HTTP(S) or SOCKS5 URL without credentials, path, query, fragment, or an invalid port")
	} else if proxyServer != "" && remoteCDP != "" {
		proxyConfigInvalid = true
		add("browser_proxy", "error", "SCOUT_PROXY_SERVER only configures locally launched browsers; remove SCOUT_REMOTE_CDP or configure the proxy on the remote browser host")
	} else if proxyServer != "" {
		add("browser_proxy", "warning", "SCOUT_PROXY_SERVER routes locally launched browser traffic; proxy policy enforcement is external and not verified by doctor")
	} else {
		add("browser_proxy", "ok", "no Scout-managed browser proxy configured")
	}

	allowPrivateRaw := strings.TrimSpace(os.Getenv("SCOUT_ALLOW_PRIVATE_IPS"))
	if allowPrivateRaw == "" {
		add("private_ip_policy", "ok", "private and loopback targets are blocked by default")
	} else if _, err := strconv.ParseBool(allowPrivateRaw); err != nil {
		add("private_ip_policy", "error", "SCOUT_ALLOW_PRIVATE_IPS must be true or false")
	} else if strings.EqualFold(allowPrivateRaw, "true") {
		add("private_ip_policy", "warning", "SCOUT_ALLOW_PRIVATE_IPS enables private and loopback targets for MCP sessions")
	} else {
		add("private_ip_policy", "ok", "private and loopback targets are blocked by default")
	}

	_, originsConfigured := os.LookupEnv("SCOUT_MCP_ALLOWED_ORIGINS")
	origins, _, originErr := configuredMCPAllowedOrigins()
	//nolint:gocritic // This branch layout is clearer for this multi-condition check.
	if originErr != nil {
		// The parser's error includes the rejected value, which may contain
		// credentials. Keep doctor output safe for logs and CI artifacts.
		add("origin_policy", "error", "SCOUT_MCP_ALLOWED_ORIGINS must be unset or contain comma-separated exact HTTP(S) origins without credentials, paths, queries, fragments, or wildcards; empty values are invalid")
	} else if len(origins) == 0 {
		add("origin_policy", "ok", "no global origin allowlist configured")
	} else {
		add("origin_policy", "ok", fmt.Sprintf("global allowlist contains %d validated origin(s)", len(origins)))
	}

	principalsRaw := os.Getenv("SCOUT_MCP_HTTP_PRINCIPALS_JSON")
	principals, principalErr := loadMCPHTTPPrincipalPolicies(principalsRaw)
	//nolint:gocritic // This branch layout is clearer for this multi-condition check.
	if principalErr != nil {
		// Do not echo principal configuration: it contains bearer credentials.
		add("http_principals", "error", "SCOUT_MCP_HTTP_PRINCIPALS_JSON is invalid; review its JSON, unique tokens, remote endpoints, isolation, and origin policies")
	} else if len(principals) == 0 {
		add("http_principals", "ok", "no principal mapping configured")
	} else {
		privateIPPrincipalCount := 0
		for _, policy := range principals {
			if policy.AllowPrivateIPs {
				privateIPPrincipalCount++
			}
		}
		if privateIPPrincipalCount > 0 {
			add("http_principals", "warning", fmt.Sprintf("%d HTTP principal(s) validated; %d allow private and loopback targets", len(principals), privateIPPrincipalCount))
		} else {
			add("http_principals", "ok", fmt.Sprintf("%d HTTP principal(s) validated", len(principals)))
		}
	}

	//nolint:gocritic // This branch layout is clearer for this multi-condition check.
	if originErr != nil || principalErr != nil {
		add("http_policy_mode", "error", "one or more HTTP policy inputs are invalid")
	} else if originsConfigured && strings.TrimSpace(principalsRaw) != "" {
		add("http_policy_mode", "error", "configure either SCOUT_MCP_ALLOWED_ORIGINS or SCOUT_MCP_HTTP_PRINCIPALS_JSON, not both")
	} else {
		add("http_policy_mode", "ok", "HTTP origin policy configuration is internally consistent")
	}

	if httpListen != "" {
		if _, err := mcpHTTPServeOptionsWithPrincipalSessions(httpListen, os.Getenv("SCOUT_MCP_HTTP_TOKEN"), principals, nil); err != nil {
			add("http_listener", "error", "HTTP listener configuration is invalid or lacks required authentication")
		} else {
			add("http_listener", "ok", "HTTP listen address and authentication configuration are valid")
		}
	}
	probeOrigin := ""
	if probeURL != "" {
		origin, err := doctorProbeOrigin(probeURL)
		//nolint:gocritic // This branch layout is clearer for this multi-condition check.
		if err != nil {
			add("app_probe", "error", "--probe-url must be an absolute HTTP(S) URL without credentials or fragment")
		} else if !live {
			addWithRemediation("app_probe", "error", "run_live_check", "--probe-url requires --live")
		} else if originErr != nil || principalErr != nil || originsConfigured && strings.TrimSpace(principalsRaw) != "" {
			add("app_probe", "error", "probe is blocked because configured browser policy is invalid or conflicting")
		} else if len(origins) > 0 && !doctorOriginAllowed(origin, origins) {
			add("app_probe", "error", "probe origin is outside the configured browser allowlist")
		} else if len(principals) > 0 && !doctorOriginAllowedForEveryPrincipal(origin, principals) {
			add("app_probe", "error", "probe origin is not allowed for every configured HTTP principal")
		} else {
			probeOrigin = origin
		}
	} else if probeText != "" {
		add("app_probe", "error", "--probe-text requires --probe-url")
	}

	if taskStorePath == "" {
		add("task_store", "warning", "no durable task store configured; task recovery is limited to the session's in-memory registry")
	} else if err := inspectTaskStorePath(taskStorePath); err != nil {
		add("task_store", "error", err.Error())
	} else if live {
		if _, err := os.Stat(taskStorePath); os.IsNotExist(err) {
			addWithRemediation("task_store", "not_checked", "initialize_task_store_on_service_start", "store file does not exist; live inspection did not create it; the service will initialize it on startup")
		} else {
			busy, err := agent.CheckBoltTaskRunStore(taskStorePath)
			//nolint:gocritic // This branch layout is clearer for this multi-condition check.
			if busy {
				addWithRemediation("task_store", "warning", "verify_store_owner_or_retry", "task store is locked by another process; this may be the running Scout service, and its health cannot be inferred from lock ownership")
			} else if err != nil {
				add("task_store", "error", "task store could not be opened and validated read-only; inspect file permissions, database integrity, and process ownership")
			} else {
				add("task_store", "ok", "existing task store opened read-only and required buckets are present; write capacity was not checked and no task data was changed")
			}
		}
	} else {
		add("task_store", "ok", "task-store path and parent permissions look suitable; database was not opened, locked, or modified")
	}

	canLiveCheck := !proxyConfigInvalid
	//nolint:gocritic // This branch layout is clearer for this multi-condition check.
	if remoteCDP == "" {
		if browserPath, err := findDoctorBrowser(); err != nil {
			canLiveCheck = false
			addWithRemediation("local_browser", "error", "install_chrome_or_chromium_or_set_remote_cdp", "no supported Chrome or Chromium executable found on this host; install one, add it to PATH, or configure SCOUT_REMOTE_CDP")
		} else if live {
			add("local_browser", "ok", fmt.Sprintf("browser executable is available at %s; live launch result is reported by browser_live", browserPath))
		} else {
			add("local_browser", "ok", fmt.Sprintf("browser executable is available at %s; doctor does not launch it", browserPath))
		}
		if live {
			add("remote_cdp", "ok", "no remote CDP endpoint configured; local launch was selected and checked by browser_live")
		} else {
			add("remote_cdp", "not_checked", "no remote CDP endpoint configured; live browser readiness was not checked")
		}
	} else if !validRemoteCDPEndpoint(remoteCDP) {
		canLiveCheck = false
		add("remote_cdp", "error", "SCOUT_REMOTE_CDP must be a ws, wss, http, or https endpoint; HTTP(S) endpoints cannot contain credentials, query, or fragment")
	} else if live {
		add("remote_cdp", "ok", "remote CDP endpoint syntax is valid; live attach result is reported by browser_live")
	} else {
		add("remote_cdp", "not_checked", "remote CDP endpoint syntax is valid; browser availability was not checked")
	}
	if live {
		if !canLiveCheck {
			addWithRemediation("browser_live", "not_checked", "repair_configuration_before_live_check", "live check skipped because browser configuration is invalid")
			addWithRemediation("browser_session", "not_checked", "repair_configuration_before_live_check", "live check skipped because browser configuration is invalid")
			addWithRemediation("browser_status", "not_checked", "repair_configuration_before_live_check", "live check skipped because browser configuration is invalid")
			addWithRemediation("browser_first_read", "not_checked", "repair_configuration_before_live_check", "live check skipped because browser configuration is invalid")
			addWithRemediation("browser_cleanup", "not_checked", "none", "no browser session was created, so there was nothing to clean up")
			if probeOrigin != "" {
				addWithRemediation("app_probe", "not_checked", "repair_configuration_before_live_check", "application probe skipped because browser configuration is invalid")
			}
			return report
		}
		if liveTimeout <= 0 {
			liveTimeout = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
		defer cancel()
		browserCheckStarted := time.Now()
		allowPrivateIPs, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("SCOUT_ALLOW_PRIVATE_IPS")))
		cfg := agent.SessionConfig{Headless: true, Timeout: liveTimeout, AllowPrivateIPs: allowPrivateIPs, RemoteCDP: remoteCDP, ProxyServer: proxyServer}
		if probeOrigin != "" {
			cfg.AllowedOrigins = []string{probeOrigin}
		}
		session, err := newDoctorSession(ctx, cfg)
		if err != nil {
			addTimed("browser_live", "error", fmt.Sprintf("browser launch or CDP attach failed within %s; check the endpoint, browser process, and network reachability", liveTimeout), browserCheckStarted)
			addTimed("browser_session", "error", "browser launch or CDP attach failed", browserCheckStarted)
			addWithRemediation("browser_status", "not_checked", "repair_browser_session_before_status", "no browser session was created")
			addWithRemediation("browser_first_read", "not_checked", "repair_browser_session_before_read", "no browser session was created")
			addWithRemediation("browser_cleanup", "not_checked", "none", "no browser session was created, so there was nothing to clean up")
			if probeOrigin != "" {
				addWithRemediation("app_probe", "not_checked", "repair_browser_attach_before_app_probe", "application probe skipped because browser launch or attach failed")
			}
		} else {
			addTimed("browser_session", "ok", "browser session was created or attached", browserCheckStarted)
			statusStarted := time.Now()
			status, statusErr := session.StatusContext(ctx)
			if statusErr != nil || status == nil || !status.BrowserAlive {
				addTimed("browser_status", "error", "browser session did not report a live browser", statusStarted)
				addTimed("browser_live", "error", "browser attached but did not report a live session; reconnect or replace the browser", browserCheckStarted)
				addWithRemediation("browser_first_read", "not_checked", "repair_browser_status_before_read", "bounded active-page read skipped because status did not confirm a live browser")
				if probeOrigin != "" {
					addWithRemediation("app_probe", "not_checked", "repair_browser_status_before_app_probe", "application probe skipped because session status did not confirm a live browser")
				}
			} else {
				addTimed("browser_status", "ok", "browser status reports a live session", statusStarted)
				// Session status proves the browser connection, but some drivers can
				// still hold a live connection to an unresponsive page. Require one
				// bounded read before reporting live readiness.
				readCtx, readCancel := context.WithTimeout(ctx, min(liveTimeout, 2*time.Second))
				readStarted := time.Now()
				_, readErr := session.ObserveContext(readCtx)
				readCancel()
				if readErr != nil {
					addTimed("browser_first_read", "error", "bounded active-page read failed", readStarted)
					addTimed("browser_live", "error", "browser session attached but the active page did not complete a bounded read; check target responsiveness or reconnect", browserCheckStarted)
					report.Checks[len(report.Checks)-1].Code = "SCOUT_DOCTOR_BROWSER_LIVE_PAGE_READ_FAILED"
				} else {
					addTimed("browser_first_read", "ok", "bounded active-page read completed", readStarted)
					addTimed("browser_live", "ok", fmt.Sprintf("browser is reachable and a bounded active-page read completed (ownership: %s)", status.BrowserOwnership), browserCheckStarted)
				}
				if probeOrigin != "" && readErr == nil {
					probeStarted := time.Now()
					if err := runDoctorApplicationProbe(ctx, session, probeURL, probeText); err != nil {
						detail := "probe navigation failed; check the fixture and browser policy"
						code := "SCOUT_DOCTOR_APP_PROBE_FAILED"
						remediation := "check_probe_url_and_browser_policy"
						if errors.Is(err, errDoctorProbeMarker) {
							detail = "health marker was not found; original target was restored and temporary tab was closed"
							code = "SCOUT_DOCTOR_APP_PROBE_MARKER_MISSING"
							remediation = "verify_health_marker_and_application_readiness"
						}
						if errors.Is(err, errDoctorProbeCleanup) {
							detail = "temporary probe tab cleanup failed; inspect browser tabs before continuing"
							code = "SCOUT_DOCTOR_APP_PROBE_CLEANUP_FAILED"
							remediation = "inspect_browser_tabs_and_restore_original_session"
						}
						addWithRemediation("app_probe", "error", remediation, detail)
						duration := time.Since(probeStarted).Milliseconds()
						if duration == 0 {
							duration = 1
						}
						report.Checks[len(report.Checks)-1].DurationMS = duration
						report.Checks[len(report.Checks)-1].Code = code
					} else {
						addTimed("app_probe", "ok", "probe URL loaded in a temporary tab; the original tab was restored and the probe tab closed", probeStarted)
					}
				} else if probeOrigin != "" {
					addWithRemediation("app_probe", "not_checked", "repair_browser_read_before_app_probe", "application probe skipped because the active browser page failed its bounded read")
				}
			}
			cleanupStarted := time.Now()
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
			closeErr := session.CloseContext(cleanupCtx)
			cleanupCancel()
			if closeErr != nil {
				addTimed("browser_cleanup", "error", "the doctor probe session could not be closed cleanly; inspect browser processes and tabs before continuing", cleanupStarted)
			} else {
				addTimed("browser_cleanup", "ok", "the doctor probe session closed cleanly", cleanupStarted)
			}
		}
	} else {
		add("browser_live", "not_checked", "pass --live to launch or attach to the configured browser and check session health")
		add("browser_session", "not_checked", "pass --live to launch or attach to a browser")
		add("browser_status", "not_checked", "pass --live to inspect browser status")
		add("browser_first_read", "not_checked", "pass --live to perform a bounded active-page read")
	}
	return report
}

func doctorCheckCode(name, status string) string {
	return "SCOUT_DOCTOR_" + strings.ToUpper(name) + "_" + strings.ToUpper(status)
}

func doctorOriginAllowed(origin string, allowed []string) bool {
	return mcpOriginSubset([]string{origin}, allowed)
}

func doctorOriginAllowedForEveryPrincipal(origin string, principals map[string]mcpHTTPPrincipal) bool {
	if len(principals) == 0 {
		return false
	}
	for _, principal := range principals {
		if !doctorOriginAllowed(origin, principal.AllowedOrigins) {
			return false
		}
	}
	return true
}

func doctorProbeOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid probe URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("probe URL must use HTTP or HTTPS")
	}
	origin := scheme + "://" + strings.ToLower(parsed.Host)
	validated, err := parseMCPAllowedOrigins(origin)
	if err != nil || len(validated) != 1 {
		return "", fmt.Errorf("invalid probe origin")
	}
	return validated[0], nil
}

var (
	errDoctorProbeMarker  = errors.New("doctor application probe marker was not found")
	errDoctorProbeCleanup = errors.New("doctor application probe cleanup failed")
)

func runDoctorApplicationProbe(ctx context.Context, session doctorSession, probeURL, probeText string) (retErr error) {
	const probeTab = "scout-doctor-probe"
	initialPage, err := session.ObserveContext(ctx)
	if err != nil || initialPage == nil || initialPage.TargetID == "" {
		return fmt.Errorf("inspect current target identity")
	}
	tabs, err := session.ListTabsContext(ctx)
	if err != nil {
		return fmt.Errorf("list existing browser tabs")
	}
	activeName, activeTarget := "default", initialPage.TargetID
	for _, tab := range tabs {
		if tab.Active {
			activeName, activeTarget = tab.Name, tab.TargetID
			break
		}
	}
	if activeTarget == "" {
		return fmt.Errorf("active browser target is unavailable")
	}
	page, err := session.OpenTabContext(ctx, probeTab, probeURL)
	if err != nil {
		return fmt.Errorf("probe navigation failed")
	}
	probeTarget := page.TargetID
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer func() {
		if _, switchErr := session.SwitchTabForTargetContext(cleanupCtx, activeName, activeTarget); switchErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: restore original target", errDoctorProbeCleanup))
		}
		if _, closeErr := session.CloseTabResultForTargetContext(cleanupCtx, probeTab, probeTarget); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: close probe target", errDoctorProbeCleanup))
		}
	}()
	if page.TargetID == "" || page.URL == "" {
		return fmt.Errorf("probe navigation did not return a target and URL")
	}
	if probeText != "" {
		result, err := session.AssertTextContainsContext(ctx, probeText, "", false)
		if err != nil {
			return fmt.Errorf("health marker inspection failed")
		}
		if result == nil || !result.OK {
			return errDoctorProbeMarker
		}
	}
	return nil
}

// doctorRemediation is a stable machine-readable hint. It does not declare a
// browser action safe to retry; mutation outcomes require task reconciliation.
func doctorRemediation(name, status string) string {
	if status == "ok" {
		return "none"
	}
	if status == "not_checked" {
		return "run_live_check"
	}
	switch name {
	case "task_store":
		return "configure_or_repair_task_store"
	case "local_browser":
		return "install_chrome_or_chromium_or_set_remote_cdp"
	case "remote_cdp":
		return "repair_remote_cdp_endpoint"
	case "browser_live":
		return "check_browser_endpoint_and_connection"
	case "browser_session", "browser_status":
		return "reconnect_browser_session"
	case "browser_first_read":
		return "inspect_unresponsive_target"
	case "browser_cleanup":
		return "inspect_browser_processes_and_targets"
	case "browser_proxy":
		return "verify_proxy_health_and_decisions_or_configure_remote_browser_egress"
	case "http_listener":
		return "repair_listener_or_auth_configuration"
	case "private_ip_policy", "origin_policy", "http_principals", "http_policy_mode", "mcp_profile":
		return "repair_policy_or_profile_configuration"
	case "app_probe":
		return "repair_probe_url_or_application"
	default:
		return "inspect_check_detail"
	}
}

func validRemoteCDPEndpoint(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
		return true
	case "http", "https":
		return u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
	default:
		return false
	}
}

func inspectTaskStorePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("task-store path cannot be empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("cannot resolve task-store path")
	}
	parentInfo, err := os.Stat(filepath.Dir(absPath))
	if err != nil {
		return fmt.Errorf("task-store parent directory is unavailable")
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("task-store parent path is not a directory")
	}
	storeInfo, err := os.Stat(absPath)
	if err == nil {
		if !storeInfo.Mode().IsRegular() {
			return fmt.Errorf("task-store path exists but is not a regular file")
		}
		if storeInfo.Mode().Perm()&0600 != 0600 {
			return fmt.Errorf("existing task-store file must be readable and writable by its owner")
		}
		if storeInfo.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("existing task-store file is accessible to group or other users; restrict permissions to 0600")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("task-store path cannot be inspected")
	}
	// The store will be created by the server with mode 0600. Check that the
	// parent permissions do not make the path structurally impossible, without
	// probing write access by creating a file.
	if parentInfo.Mode().Perm()&0700 != 0700 {
		return fmt.Errorf("task-store parent directory needs owner read, write, and execute permissions")
	}
	return nil
}

func printDoctor(report doctorReport) {
	label := strings.ToUpper(report.Status)
	fmt.Printf("Scout doctor: %s (version %s, commit %s)\n", label, report.Version, report.Commit)
	for _, check := range report.Checks {
		fmt.Printf("[%s] %s: %s\n", strings.ToUpper(check.Status), check.Name, check.Detail)
	}
}

func printDoctorJSON(report doctorReport) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// runDoctor performs local, read-only configuration checks. It never launches
// a browser, contacts a CDP endpoint, creates a store, or prints secret values.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func runDoctor(taskStorePath, profile, requiredTools, httpListen string) doctorReport {
	return runDoctorWithOptions(taskStorePath, profile, requiredTools, httpListen, false, 5*time.Second)
}
