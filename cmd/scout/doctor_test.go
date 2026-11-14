package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
)

type doctorSessionStub struct {
	status       *agent.SessionStatus
	statusErr    error
	observeErr   error
	observation  *agent.Observation
	closed       bool
	probeURL     string
	tabs         []agent.TabInfo
	openResult   *agent.PageResult
	openErr      error
	assertResult *agent.AssertResult
	assertErr    error
	switchErr    error
	closeErr     error
}

func (s *doctorSessionStub) StatusContext(context.Context) (*agent.SessionStatus, error) {
	return s.status, s.statusErr
}

func (s *doctorSessionStub) ObserveContext(context.Context) (*agent.Observation, error) {
	return s.observation, s.observeErr
}

func (s *doctorSessionStub) OpenTabContext(_ context.Context, _ string, rawURL string) (*agent.PageResult, error) {
	s.probeURL = rawURL
	return s.openResult, s.openErr
}

func (s *doctorSessionStub) ListTabsContext(context.Context) ([]agent.TabInfo, error) {
	return s.tabs, nil
}

func (s *doctorSessionStub) SwitchTabForTargetContext(context.Context, string, string) (*agent.PageResult, error) {
	return nil, s.switchErr
}

func (s *doctorSessionStub) CloseTabResultForTargetContext(context.Context, string, string) (*agent.PageResult, error) {
	return nil, s.closeErr
}

func (s *doctorSessionStub) AssertTextContainsContext(context.Context, string, string, bool) (*agent.AssertResult, error) {
	return s.assertResult, s.assertErr
}

func (s *doctorSessionStub) CloseContext(context.Context) error {
	s.closed = true
	return nil
}

func doctorCheckNamed(t *testing.T, report doctorReport, name string) doctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor report has no %q check: %+v", name, report.Checks)
	return doctorCheck{}
}

func TestDoctorRejectsInvalidOriginPolicyWithoutEchoingCredentials(t *testing.T) {
	t.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", "https://user:doctor-secret@example.test")
	t.Setenv("SCOUT_MCP_HTTP_PRINCIPALS_JSON", "")
	t.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "")
	t.Setenv("SCOUT_REMOTE_CDP", "")

	report := runDoctor("", "all", "", "")
	if report.Status != "error" {
		t.Fatalf("doctor status = %q, want error", report.Status)
	}
	check := doctorCheckNamed(t, report, "origin_policy")
	if check.Status != "error" || check.Code != "SCOUT_DOCTOR_ORIGIN_POLICY_ERROR" || check.Remediation == "" {
		t.Fatalf("origin policy diagnostic = %+v, want stable error and remediation", check)
	}
	if !strings.Contains(check.Detail, "empty values are invalid") || !strings.Contains(check.Detail, "must be unset") {
		t.Fatalf("origin policy detail = %q, want actionable empty-value guidance", check.Detail)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "doctor-secret") || strings.Contains(string(encoded), "user:") {
		t.Fatalf("doctor report exposed origin credentials: %s", encoded)
	}
}

func TestDoctorRequiresLiveModeForApplicationProbe(t *testing.T) {
	t.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", "https://doctor-fixture.invalid")
	t.Setenv("SCOUT_MCP_HTTP_PRINCIPALS_JSON", "")
	t.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "")
	t.Setenv("SCOUT_REMOTE_CDP", "")

	report := runDoctorWithProbeOptions("", "all", "", "", false, 0, "https://fixture.example/health", "ready")
	check := doctorCheckNamed(t, report, "app_probe")
	if check.Status != "error" || check.Code != "SCOUT_DOCTOR_APP_PROBE_ERROR" || check.Remediation != "run_live_check" {
		t.Fatalf("app probe diagnostic = %+v, want explicit live-mode requirement", check)
	}
	if report.Status != "error" {
		t.Fatalf("doctor summary status = %q, want error", report.Status)
	}
}

func TestDoctorProxyWarningHasActionableRemediation(t *testing.T) {
	clearDoctorEnvironment(t)
	t.Setenv("SCOUT_PROXY_SERVER", "http://127.0.0.1:8080")
	check := doctorCheckNamed(t, runDoctor("", "all", "", ""), "browser_proxy")
	if check.Status != "warning" || check.Remediation != "verify_proxy_health_and_decisions_or_configure_remote_browser_egress" {
		t.Fatalf("proxy diagnostic = %+v, want warning with actionable remediation", check)
	}
}

func TestDoctorLiveReadFailureIsNotGreenAndClosesSession(t *testing.T) {
	clearDoctorEnvironment(t)
	stub := &doctorSessionStub{
		status:     &agent.SessionStatus{BrowserAlive: true, BrowserOwnership: "remote_cdp"},
		observeErr: context.DeadlineExceeded,
	}
	originalFactory := newDoctorSession
	newDoctorSession = func(context.Context, agent.SessionConfig) (doctorSession, error) { return stub, nil }
	t.Cleanup(func() { newDoctorSession = originalFactory })
	report := runDoctorWithOptions("", "all", "", "", true, time.Second)
	check := doctorCheckNamed(t, report, "browser_live")
	if check.Status != "error" || check.Code != "SCOUT_DOCTOR_BROWSER_LIVE_PAGE_READ_FAILED" || check.Remediation != "check_browser_endpoint_and_connection" {
		t.Fatalf("browser live check = %+v, want bounded-read failure", check)
	}
	if report.Status != "error" {
		t.Fatalf("doctor status = %q, want error", report.Status)
	}
	if !stub.closed {
		t.Fatal("doctor did not close the attached session after page-read failure")
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "ok" {
		t.Fatalf("browser cleanup check = %+v, want ok", cleanup)
	}
	if check := doctorCheckNamed(t, report, "browser_session"); check.Status != "ok" || check.DurationMS <= 0 {
		t.Fatalf("browser session phase = %+v, want timed success", check)
	}
	if check := doctorCheckNamed(t, report, "browser_status"); check.Status != "ok" || check.DurationMS <= 0 {
		t.Fatalf("browser status phase = %+v, want timed success", check)
	}
	if check := doctorCheckNamed(t, report, "browser_first_read"); check.Status != "error" || check.DurationMS <= 0 || check.Remediation != "inspect_unresponsive_target" {
		t.Fatalf("browser first-read phase = %+v, want timed failure", check)
	}
}

func TestDoctorLiveAttachFailureHasStableCodeAndNoSecrets(t *testing.T) {
	clearDoctorEnvironment(t)
	t.Setenv("SCOUT_REMOTE_CDP", "http://127.0.0.1:1")
	report := runDoctorWithOptions("", "all", "", "", true, time.Second)
	check := doctorCheckNamed(t, report, "browser_live")
	if check.Status != "error" || check.Code != "SCOUT_DOCTOR_BROWSER_LIVE_ERROR" || check.DurationMS <= 0 || check.Remediation != "check_browser_endpoint_and_connection" {
		t.Fatalf("browser live attach diagnostic = %+v, want timed stable error", check)
	}
	if report.Status != "error" {
		t.Fatalf("doctor status = %q, want error", report.Status)
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "not_checked" {
		t.Fatalf("browser cleanup = %+v, want explicit not_checked when no session started", cleanup)
	}
	if check := doctorCheckNamed(t, report, "browser_session"); check.Status != "error" || check.DurationMS <= 0 {
		t.Fatalf("browser session phase = %+v, want timed attach error", check)
	}
	if check := doctorCheckNamed(t, report, "browser_status"); check.Status != "not_checked" {
		t.Fatalf("browser status phase = %+v, want skipped", check)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "127.0.0.1:1") {
		t.Fatalf("doctor report disclosed configured endpoint: %s", encoded)
	}
}

func TestDoctorReportsMissingBrowserAndSkipsLiveCheck(t *testing.T) {
	clearDoctorEnvironment(t)
	t.Setenv("SCOUT_REMOTE_CDP", "")
	originalFinder := findDoctorBrowser
	findDoctorBrowser = func() (string, error) { return "", errors.New("no browser") }
	t.Cleanup(func() { findDoctorBrowser = originalFinder })
	report := runDoctorWithOptions("", "all", "", "", true, time.Second)
	local := doctorCheckNamed(t, report, "local_browser")
	if local.Status != "error" || local.Code != "SCOUT_DOCTOR_LOCAL_BROWSER_ERROR" || local.Remediation != "install_chrome_or_chromium_or_set_remote_cdp" {
		t.Fatalf("local browser diagnostic = %+v, want missing-browser error with actionable remediation", local)
	}
	live := doctorCheckNamed(t, report, "browser_live")
	if live.Status != "not_checked" || live.Remediation != "repair_configuration_before_live_check" {
		t.Fatalf("browser live diagnostic = %+v, want skipped due to missing browser", live)
	}
	if report.Status != "error" {
		t.Fatalf("doctor status = %q, want error", report.Status)
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "not_checked" {
		t.Fatalf("browser cleanup = %+v, want explicit not_checked when browser is missing", cleanup)
	}
}

func TestDoctorTimeoutDuringFirstReadStillClosesSession(t *testing.T) {
	clearDoctorEnvironment(t)
	stub := &doctorSessionStub{
		status:     &agent.SessionStatus{BrowserAlive: true, BrowserOwnership: "remote_cdp"},
		observeErr: context.DeadlineExceeded,
	}
	originalFactory := newDoctorSession
	newDoctorSession = func(context.Context, agent.SessionConfig) (doctorSession, error) { return stub, nil }
	t.Cleanup(func() { newDoctorSession = originalFactory })
	report := runDoctorWithOptions("", "all", "", "", true, 5*time.Millisecond)
	check := doctorCheckNamed(t, report, "browser_live")
	if check.Status != "error" || check.Code != "SCOUT_DOCTOR_BROWSER_LIVE_PAGE_READ_FAILED" {
		t.Fatalf("timed first-read diagnostic = %+v, want bounded-read error", check)
	}
	if !stub.closed {
		t.Fatal("doctor did not close timed-out session")
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "ok" {
		t.Fatalf("browser cleanup = %+v, want ok after timeout", cleanup)
	}
}

func TestDoctorLiveProbeSeparatesBrowserReadFromApplicationMarker(t *testing.T) {
	clearDoctorEnvironment(t)
	probeURL := "https://doctor-fixture.example/health"
	t.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", "https://doctor-fixture.example")
	makeSession := func(assertOK bool) *doctorSessionStub {
		result := &agent.AssertResult{OK: assertOK}
		return &doctorSessionStub{
			status:       &agent.SessionStatus{BrowserAlive: true, BrowserOwnership: "local"},
			observation:  &agent.Observation{TargetID: "original-target"},
			tabs:         []agent.TabInfo{{Name: "default", TargetID: "original-target", Active: true}},
			openResult:   &agent.PageResult{TargetID: "probe-target", URL: probeURL},
			assertResult: result,
		}
	}
	stub := makeSession(true)
	originalFactory := newDoctorSession
	newDoctorSession = func(context.Context, agent.SessionConfig) (doctorSession, error) { return stub, nil }
	t.Cleanup(func() { newDoctorSession = originalFactory })
	passed := runDoctorWithProbeOptions("", "all", "", "", true, 10*time.Second, probeURL, "ready here")
	if check := doctorCheckNamed(t, passed, "browser_live"); check.Status != "ok" || !strings.Contains(check.Detail, "bounded active-page read completed") {
		t.Fatalf("positive first-read diagnostic = %+v", check)
	}
	if check := doctorCheckNamed(t, passed, "browser_first_read"); check.Status != "ok" || check.DurationMS <= 0 {
		t.Fatalf("positive first-read phase = %+v, want timed success", check)
	}
	if check := doctorCheckNamed(t, passed, "app_probe"); check.Status != "ok" {
		t.Fatalf("positive application probe = %+v, want ok", check)
	}
	if check := doctorCheckNamed(t, passed, "browser_cleanup"); check.Status != "ok" {
		t.Fatalf("positive probe cleanup = %+v, want ok", check)
	}
	missingMarkerStub := makeSession(false)
	newDoctorSession = func(context.Context, agent.SessionConfig) (doctorSession, error) { return missingMarkerStub, nil }
	report := runDoctorWithProbeOptions("", "all", "", "", true, 10*time.Second, probeURL, "missing-marker")
	browser := doctorCheckNamed(t, report, "browser_live")
	if browser.Status != "ok" || !strings.Contains(browser.Detail, "bounded active-page read completed") {
		t.Fatalf("browser readiness did not report successful bounded read: %+v", browser)
	}
	probe := doctorCheckNamed(t, report, "app_probe")
	if probe.Status != "error" || probe.Code != "SCOUT_DOCTOR_APP_PROBE_MARKER_MISSING" || probe.Remediation != "verify_health_marker_and_application_readiness" {
		t.Fatalf("application probe = %+v, want distinct missing-marker failure and actionable remediation", probe)
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "ok" {
		t.Fatalf("browser cleanup = %+v, want ok", cleanup)
	}
	if report.Status != "error" {
		t.Fatalf("doctor status = %q, want error for missing app marker", report.Status)
	}
}

func TestDoctorProbeCleanupFailureHasStableRecoveryGuidance(t *testing.T) {
	clearDoctorEnvironment(t)
	t.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true")
	t.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", "http://doctor-fixture.invalid")
	stub := &doctorSessionStub{
		status:      &agent.SessionStatus{BrowserAlive: true, BrowserOwnership: "remote_cdp"},
		observation: &agent.Observation{TargetID: "original-target"},
		tabs:        []agent.TabInfo{{Name: "default", TargetID: "original-target", Active: true}},
		openResult:  &agent.PageResult{TargetID: "probe-target", URL: "http://doctor-fixture.invalid/health"},
		switchErr:   errors.New("injected restore failure"),
		closeErr:    errors.New("injected close failure"),
	}
	originalFactory := newDoctorSession
	newDoctorSession = func(context.Context, agent.SessionConfig) (doctorSession, error) { return stub, nil }
	t.Cleanup(func() { newDoctorSession = originalFactory })

	report := runDoctorWithProbeOptions("", "all", "", "", true, time.Second, "http://doctor-fixture.invalid/health", "")
	probe := doctorCheckNamed(t, report, "app_probe")
	if probe.Status != "error" || probe.Code != "SCOUT_DOCTOR_APP_PROBE_CLEANUP_FAILED" || probe.Remediation != "inspect_browser_tabs_and_restore_original_session" {
		t.Fatalf("probe cleanup diagnostic = %+v, want stable cleanup failure guidance", probe)
	}
	if !strings.Contains(probe.Detail, "temporary probe tab cleanup failed") {
		t.Fatalf("probe cleanup detail = %q, want affected temporary tab called out", probe.Detail)
	}
	if cleanup := doctorCheckNamed(t, report, "browser_cleanup"); cleanup.Status != "ok" {
		t.Fatalf("browser cleanup = %+v, want session close reported separately", cleanup)
	}
	if !stub.closed {
		t.Fatal("doctor did not close the probe session after tab cleanup failed")
	}
}

func clearDoctorEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SCOUT_MCP_HTTP_PRINCIPALS_JSON",
		"SCOUT_MCP_HTTP_TOKEN", "SCOUT_ALLOW_PRIVATE_IPS", "SCOUT_REMOTE_CDP",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", "https://doctor-fixture.invalid")
}
