package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/agent"
)

func TestSavePipeScreenshotReportsWriteFailures(t *testing.T) {
	t.Run("writes screenshot", func(t *testing.T) {
		dir := t.TempDir()
		filename, err := savePipeScreenshot(dir, "https://example.com/page", []byte("png data"))
		if err != nil {
			t.Fatalf("savePipeScreenshot() error: %v", err)
		}
		if filename != "example.com_page.png" {
			t.Fatalf("filename = %q", filename)
		}
		data, err := os.ReadFile(filepath.Join(dir, filename))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "png data" {
			t.Fatalf("screenshot data = %q", data)
		}
	})

	t.Run("returns filesystem error", func(t *testing.T) {
		_, err := savePipeScreenshot(filepath.Join(t.TempDir(), "missing"), "https://example.com/page", []byte("png data"))
		if err == nil {
			t.Fatal("savePipeScreenshot() unexpectedly succeeded")
		}
	})
}

func TestReadRecoveryFileAcceptsLegacyV1AndRejectsTrailingContent(t *testing.T) {
	legacy := `{"schema_version":1,"code":"SCOUT_ACTION_OUTCOME_UNKNOWN","outcome":"outcome_unknown","action":"click","target":"#save","target_id":"target-1","expected_postcondition":{"text":"Saved"}}`
	validPath := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(validPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var outcome agent.ActionOutcomeError
	if err := readRecoveryFile(validPath, &outcome); err != nil {
		t.Fatalf("readRecoveryFile() rejected legacy v1 record: %v", err)
	}
	if outcome.Action != "click" || outcome.TargetID != "target-1" || outcome.ExpectedPostcondition == nil || outcome.ExpectedPostcondition.Text != "Saved" {
		t.Fatalf("legacy record decoded incompletely: %+v", outcome)
	}

	trailingPath := filepath.Join(t.TempDir(), "trailing.json")
	if err := os.WriteFile(trailingPath, []byte(legacy+` {"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readRecoveryFile(trailingPath, &agent.ActionOutcomeError{}); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("readRecoveryFile() accepted trailing content: %v", err)
	}
}

func TestCLIErrorJSONPreservesUnknownOutcome(t *testing.T) {
	outcome := &agent.ActionOutcomeError{
		Action: "click", Target: "#save", Outcome: agent.OutcomeUnknown, TraceActionOrdinal: 9, OperationOrdinal: 17,
		TargetID: "target-7", FrameID: "frame-3", URL: "https://example.com/account",
		Evidence: "click may have dispatched", RecoveryHint: "inspect before retrying",
		ResponseEvidence: &agent.ActionResponseEvidence{Observed: true, Method: "POST", URL: "https://example.test/checkout", StatusCode: 422},
		Cause:            errors.New("connection lost after write"),
	}
	var envelope cliErrorEnvelope
	if err := json.Unmarshal([]byte(cliErrorJSON(outcome)), &envelope); err != nil {
		t.Fatalf("decode JSON error: %v", err)
	}
	if envelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || envelope.Outcome != "outcome_unknown" {
		t.Fatalf("unexpected outcome classification: %+v", envelope)
	}
	if envelope.Action != "click" || envelope.Target != "#save" || envelope.Evidence == "" || envelope.Hint == "" || envelope.Detail == "" {
		t.Fatalf("structured recovery context missing: %+v", envelope)
	}
	if envelope.RecoveryHint != envelope.Hint {
		t.Fatalf("recovery_hint and legacy hint differ: %+v", envelope)
	}
	if envelope.TargetID != "target-7" || envelope.FrameID != "frame-3" || envelope.URL != "https://example.com/account" {
		t.Fatalf("target/frame location missing: %+v", envelope)
	}
	if envelope.TraceActionOrdinal != 9 {
		t.Fatalf("trace action ordinal missing: %+v", envelope)
	}
	if envelope.OperationOrdinal != 17 {
		t.Fatalf("operation ordinal missing: %+v", envelope)
	}
	if envelope.ResponseEvidence == nil || !envelope.ResponseEvidence.Observed || envelope.ResponseEvidence.Method != "POST" || envelope.ResponseEvidence.StatusCode != 422 {
		t.Fatalf("typed response evidence missing: %+v", envelope)
	}
}

func TestCLIErrorJSONPreservesCloseTargetAbsencePredicate(t *testing.T) {
	outcome := &agent.ActionOutcomeError{
		Action: "close_tab", Target: "temporary", TargetID: "target-closed",
		BrowserInstanceID: "browser-fingerprint", OperationOrdinal: 17,
		Outcome: agent.OutcomeUnknown, ExpectedPostcondition: &agent.Postcondition{TargetAbsent: "target-closed"},
		Cause: errors.New("close response timed out"),
	}
	var envelope cliErrorEnvelope
	if err := json.Unmarshal([]byte(cliErrorJSON(outcome)), &envelope); err != nil {
		t.Fatalf("decode close JSON error: %v", err)
	}
	if envelope.ExpectedPostcondition == nil || envelope.ExpectedPostcondition.TargetAbsent != "target-closed" {
		t.Fatalf("CLI error omitted target_absent predicate: %+v", envelope)
	}
	recovery, err := agent.NewActionRecoveryRecord(outcome)
	if err != nil {
		t.Fatalf("create CLI portable recovery record: %v", err)
	}
	if recovery.ActionOutcome.ExpectedPostcondition == nil || recovery.ActionOutcome.ExpectedPostcondition.TargetAbsent != "target-closed" {
		t.Fatalf("CLI recovery record omitted target_absent predicate: %+v", recovery)
	}
}

func TestCLIErrorJSONNavigationUnknownUsesRecoveryEnvelope(t *testing.T) {
	err := &browse.NavigationOutcomeUnknownError{
		URL: "https://example.test/orders?token=secret#latest",
		Err: errors.New("connection lost after navigation dispatch"),
	}
	var envelope cliErrorEnvelope
	if err := json.Unmarshal([]byte(cliErrorJSON(err)), &envelope); err != nil {
		t.Fatalf("decode JSON error: %v", err)
	}
	if envelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || envelope.Outcome != string(agent.OutcomeUnknown) || envelope.Action != "navigate" {
		t.Fatalf("unexpected navigation outcome classification: %+v", envelope)
	}
	if envelope.URL != "https://example.test/orders" || envelope.Evidence == "" || envelope.Hint == "" || envelope.Detail == "" {
		t.Fatalf("navigation recovery context missing: %+v", envelope)
	}
}

func TestNavigationPostconditionFailureIsProcessError(t *testing.T) {
	result := &agent.PageResult{
		Action: "navigate", Outcome: string(agent.OutcomePostconditionFailed),
		URL: "https://example.test/orders?token=secret", TargetID: "target-7",
		ContentTrust: agent.ContentTrustUntrustedPage, Evidence: "expected text was not observed; Ready",
		RecoveryHint: "Inspect the page and update the expected condition.",
		Receipt:      &agent.ActionReceipt{Postcondition: &agent.Postcondition{Text: "Complete"}},
	}
	if err := navigationPostconditionError(result); err == nil {
		t.Fatal("failed navigation postcondition returned success")
	} else {
		var envelope cliErrorEnvelope
		if err := json.Unmarshal([]byte(cliErrorJSON(err)), &envelope); err != nil {
			t.Fatalf("decode structured postcondition failure: %v", err)
		}
		if envelope.Code != "SCOUT_POSTCONDITION_FAILED" || envelope.Outcome != string(agent.OutcomePostconditionFailed) || envelope.Action != "navigate" {
			t.Fatalf("unexpected failure envelope: %+v", envelope)
		}
		if envelope.TargetID != "target-7" || envelope.URL != "https://example.test/orders" || envelope.Evidence == "" || envelope.ExpectedPostcondition == nil || envelope.ExpectedPostcondition.Text != "Complete" {
			t.Fatalf("postcondition evidence missing or sensitive URL retained: %+v", envelope)
		}
	}
	if err := navigationPostconditionError(&agent.PageResult{Outcome: string(agent.OutcomeNavigationCompleted)}); err != nil {
		t.Fatalf("unverified navigation without a requested predicate failed: %v", err)
	}
}

func TestCLINavigationOutputIncludesExecutionPhaseTiming(t *testing.T) {
	result := &agent.PageResult{Action: "navigate", URL: "https://example.test/", TargetID: "target-1", Verified: true}
	output := cliNavigationOutput{
		PageResult: result,
		ExecutionTiming: cliExecutionTiming{
			BrowserStartupMS: 11, PageNavigationVerificationMS: 22, BrowserCleanupMS: 3,
		},
	}
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal CLI navigation output: %v", err)
	}
	var decoded struct {
		Action          string `json:"action"`
		TargetID        string `json:"target_id"`
		Verified        bool   `json:"verified"`
		ExecutionTiming struct {
			BrowserStartupMS             int64 `json:"browser_startup_ms"`
			PageNavigationVerificationMS int64 `json:"page_navigation_verification_ms"`
			BrowserCleanupMS             int64 `json:"browser_cleanup_ms"`
		} `json:"execution_timing"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode CLI navigation output: %v", err)
	}
	if decoded.Action != "navigate" || decoded.TargetID != "target-1" || !decoded.Verified {
		t.Fatalf("navigation result fields were lost: %+v", decoded)
	}
	if decoded.ExecutionTiming.BrowserStartupMS != 11 || decoded.ExecutionTiming.PageNavigationVerificationMS != 22 || decoded.ExecutionTiming.BrowserCleanupMS != 3 {
		t.Fatalf("execution phase timing was lost: %+v", decoded.ExecutionTiming)
	}
}

func TestCLIErrorJSONKeepsPostconditionClassificationWhenCleanupAlsoFails(t *testing.T) {
	pageErr := navigationPostconditionError(&agent.PageResult{
		Action: "navigate", Outcome: string(agent.OutcomePostconditionFailed), URL: "https://example.test/",
		Evidence: "requested marker was absent", TargetID: "target-1",
	})
	joined := errors.Join(pageErr, errors.New("browser cleanup failed"))
	var envelope cliErrorEnvelope
	if err := json.Unmarshal([]byte(cliErrorJSON(joined)), &envelope); err != nil {
		t.Fatalf("decode joined navigation/cleanup error: %v", err)
	}
	if envelope.Code != "SCOUT_POSTCONDITION_FAILED" || envelope.Outcome != string(agent.OutcomePostconditionFailed) || envelope.TargetID != "target-1" {
		t.Fatalf("cleanup error obscured the observed page failure: %+v", envelope)
	}
}

func TestCLIErrorJSONRedactsActionOutcomeURL(t *testing.T) {
	err := &agent.ActionOutcomeError{
		Action: "click", Target: "#save", Outcome: agent.OutcomeUnknown,
		URL:          "https://user:secret@example.test/orders?token=secret#latest",
		RecoveryHint: "inspect before retrying",
	}
	var envelope cliErrorEnvelope
	if err := json.Unmarshal([]byte(cliErrorJSON(err)), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.URL != "https://example.test/orders" {
		t.Fatalf("sensitive URL components were not redacted: %q", envelope.URL)
	}
}

func TestCLIErrorJSONPlainError(t *testing.T) {
	var envelope cliErrorEnvelope
	if err := json.Unmarshal([]byte(cliErrorJSON(errors.New("bad selector"))), &envelope); err != nil {
		t.Fatalf("decode JSON error: %v", err)
	}
	if envelope.Code != "SCOUT_ERROR" || envelope.Message != "bad selector" {
		t.Fatalf("unexpected plain error envelope: %+v", envelope)
	}
}

func TestCLIErrorJSONIncludesURLContext(t *testing.T) {
	var envelope cliErrorEnvelope
	if err := json.Unmarshal([]byte(cliErrorJSONWithContext(errors.New("navigation failed"), "https://example.com")), &envelope); err != nil {
		t.Fatalf("decode JSON error: %v", err)
	}
	if envelope.URL != "https://example.com" {
		t.Fatalf("URL context = %q", envelope.URL)
	}
}

func TestExtractJSONErrorsFlagAtAnyPosition(t *testing.T) {
	args, enabled := extractJSONErrorsFlag([]string{"click", "https://example.com", "#save", "--headless", "--json-errors"})
	if !enabled {
		t.Fatal("JSON error flag was not detected")
	}
	want := []string{"click", "https://example.com", "#save", "--headless"}
	if len(args) != len(want) {
		t.Fatalf("filtered args = %#v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("filtered args = %#v, want %#v", args, want)
		}
	}
}

func TestParseFlags_KeyValue(t *testing.T) {
	f := parseFlags([]string{"--foo=bar", "--baz=qux", "positional"})
	if got := f.get("foo", ""); got != "bar" {
		t.Errorf("foo = %q, want bar", got)
	}
	if got := f.get("baz", ""); got != "qux" {
		t.Errorf("baz = %q, want qux", got)
	}
}

func TestParseFlags_BareFlagEmptyValue(t *testing.T) {
	f := parseFlags([]string{"--headless"})
	if got := f.get("headless", "X"); got != "" {
		t.Errorf("bare flag value = %q, want empty", got)
	}
	if got, err := f.getBool("headless", false); err != nil || !got {
		t.Error("bare flag must read as bool true")
	}
}

func TestParseFlags_PositionalIgnored(t *testing.T) {
	f := parseFlags([]string{"https://example.com", "--timeout=5s"})
	if got, err := f.getDuration("timeout", 0); err != nil || got != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", got)
	}
}

func TestCliFlags_GetDefault(t *testing.T) {
	f := parseFlags(nil)
	if got := f.get("missing", "fallback"); got != "fallback" {
		t.Errorf("default fallback failed: %q", got)
	}
}

func TestCliFlags_GetBoolValues(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"", true}, // bare presence
		{"false", false},
		{"0", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			args := []string{"--flag=" + tc.raw}
			if tc.raw == "" {
				args = []string{"--flag"}
			}
			f := parseFlags(args)
			if got, err := f.getBool("flag", false); err != nil || got != tc.want {
				t.Errorf("getBool(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCliFlags_InvalidBoolReturnsError(t *testing.T) {
	f := parseFlags([]string{"--headless=tru"})
	if _, err := f.getBool("headless", false); err == nil {
		t.Fatal("expected invalid boolean to return an error")
	}
}

func TestCliFlags_GetDurationFallback(t *testing.T) {
	f := parseFlags([]string{"--timeout=not-a-duration"})
	if _, err := f.getDuration("timeout", 7*time.Second); err == nil {
		t.Error("invalid duration must return an error")
	}
}

func TestCliFlags_GetDurationMissing(t *testing.T) {
	f := parseFlags(nil)
	if got, err := f.getDuration("missing", 3*time.Second); err != nil || got != 3*time.Second {
		t.Errorf("missing duration must use default, got %v", got)
	}
}
