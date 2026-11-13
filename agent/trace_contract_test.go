package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTraceActionOrdinalIsMonotonicAndReturnedWithUnknownOutcome(t *testing.T) {
	s := &Session{tracing: true, trace: &traceState{run: TraceRunMetadata{TraceID: "trace-ordinal"}}}
	s.traceAfterActionContext(context.Background(), time.Now(), traceScreenshotReference{}, "observe", "", "", "", nil)
	err := &ActionOutcomeError{Action: "click", Target: "#save", Outcome: OutcomeUnknown}
	s.traceActionFailureContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "#save", "", "", err)
	if len(s.trace.events) != 2 || s.trace.events[0].ActionOrdinal != 1 || s.trace.events[1].ActionOrdinal != 2 {
		t.Fatalf("trace action ordinals are not monotonic: %+v", s.trace.events)
	}
	if err.TraceActionOrdinal != 2 || err.TraceStep != 2 || err.TraceID != "trace-ordinal" {
		t.Fatalf("unknown outcome trace correlation = %+v", err)
	}
	if s.trace.events[0].OperationOrdinal != 1 || s.trace.events[1].OperationOrdinal != 2 || err.OperationOrdinal != 2 {
		t.Fatalf("session operation ordinals are not correlated: events=%+v error=%+v", s.trace.events, err)
	}
}

func TestOperationOrdinalIsSharedByResultsAndUnknownOutcomesWithoutTrace(t *testing.T) {
	s := &Session{runID: "run-ordinal"}
	first := s.nextOperationOrdinal()
	outcome, ok := s.actionOutcomeAfterDispatch("click", "#save", "may be dispatched", nil).(*ActionOutcomeError)
	if !ok {
		t.Fatal("action outcome error was not returned")
	}
	third := s.nextOperationOrdinal()
	if first != 1 || outcome.OperationOrdinal != 2 || outcome.Step != 1 || third != 3 {
		t.Fatalf("operation numbering: first=%d error=%+v third=%d", first, outcome, third)
	}
}

func TestActionRecoveryRecordRoundTripsOperationOrdinal(t *testing.T) {
	record, err := NewActionRecoveryRecord(&ActionOutcomeError{
		RunID: "run-recovery", Action: "click", Target: "#save", TargetID: "target-1", BrowserInstanceID: "browser-fingerprint", Outcome: OutcomeUnknown,
		Evidence: "click may have been dispatched", OperationOrdinal: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeActionRecoveryRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.OperationOrdinal != 12 || got.ActionOutcome.OperationOrdinal != 12 || got.Outcome().OperationOrdinal != 12 || got.ActionOutcome.RunID != "run-recovery" || got.ActionOutcome.Evidence != "click may have been dispatched" {
		t.Fatalf("recovery ordinal did not round trip: %+v", got)
	}
}

func TestDecodeActionRecoveryRecordRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	record, err := NewActionRecoveryRecord(&ActionOutcomeError{
		RunID: "run-recovery", Action: "click", Target: "#save", TargetID: "target-1", BrowserInstanceID: "browser-fingerprint", Outcome: OutcomeUnknown,
		Evidence: "click may have been dispatched", OperationOrdinal: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}

	var withUnknown map[string]any
	if err := json.Unmarshal(data, &withUnknown); err != nil {
		t.Fatal(err)
	}
	withUnknown["replay"] = true
	unknownData, err := json.Marshal(withUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeActionRecoveryRecord(unknownData); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown recovery field was accepted: %v", err)
	}

	if _, err := DecodeActionRecoveryRecord(append(data, []byte(` {"schema_version":2}`)...)); err == nil || !strings.Contains(err.Error(), "trailing JSON value") {
		t.Fatalf("trailing recovery JSON value was accepted: %v", err)
	}
	if _, err := DecodeActionRecoveryRecord(append(data, []byte(" trailing")...)); err == nil || !strings.Contains(err.Error(), "trailing content") {
		t.Fatalf("trailing recovery content was accepted: %v", err)
	}
}

func TestActionRecoveryRecordPreservesSanitizedResponseEvidence(t *testing.T) {
	record, err := NewActionRecoveryRecord(&ActionOutcomeError{
		RunID: "run-submit", Action: "submit_form", Target: "#checkout", TargetID: "page-1", BrowserInstanceID: "browser-fingerprint",
		Outcome: OutcomeUnknown, OperationOrdinal: 4,
		ResponseEvidence: &ActionResponseEvidence{Observed: true, Method: "POST", URL: "https://user:pass@example.test/checkout?token=secret#result", StatusCode: 422},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeActionRecoveryRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	response := got.ActionOutcome.ResponseEvidence
	if response == nil || !response.Observed || response.Method != "POST" || response.StatusCode != 422 || response.URL != "https://example.test/checkout" {
		t.Fatalf("response evidence roundtrip = %+v", response)
	}
	for _, secret := range []string{"user:pass", "token=secret", "#result"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("recovery record retained sensitive URL component %q: %s", secret, data)
		}
	}
}

func TestActionRecoveryRecordAcceptsTargetAbsenceOnlyForMatchingCloseTab(t *testing.T) {
	valid := &ActionOutcomeError{
		RunID: "run-close", Action: "close_tab", Target: "temporary", TargetID: "target-closed", BrowserInstanceID: "browser-fingerprint", Outcome: OutcomeUnknown,
		OperationOrdinal:      1,
		ExpectedPostcondition: &Postcondition{TargetAbsent: "target-closed"},
	}
	if _, err := NewActionRecoveryRecord(valid); err != nil {
		t.Fatalf("valid close_tab recovery record rejected: %v", err)
	}
	invalid := *valid
	invalid.Action = "click"
	if _, err := NewActionRecoveryRecord(&invalid); err == nil || !strings.Contains(err.Error(), "target_absent") {
		t.Fatalf("target_absent accepted for non-close action: %v", err)
	}
	invalid = *valid
	invalid.ExpectedPostcondition = &Postcondition{TargetAbsent: "another-target"}
	if _, err := NewActionRecoveryRecord(&invalid); err == nil || !strings.Contains(err.Error(), "target_absent") {
		t.Fatalf("mismatched target_absent accepted: %v", err)
	}
}

func TestReconcileActionOutcomeRejectsDifferentRunBeforeBrowserAccess(t *testing.T) {
	s := &Session{runID: "current-run"}
	outcome := &ActionOutcomeError{
		RunID: "previous-run", Action: "click", TargetID: "target-1", Outcome: OutcomeUnknown,
	}
	if _, err := s.ReconcileActionOutcomeContext(context.Background(), outcome); err == nil || !strings.Contains(err.Error(), "different or unspecified Scout run") {
		t.Fatalf("foreign run was not rejected: %v", err)
	}
	if _, err := s.ReconcileActionOutcomeContext(context.Background(), &ActionOutcomeError{
		Action: "click", TargetID: "target-1", Outcome: OutcomeUnknown,
	}); err == nil || !strings.Contains(err.Error(), "different or unspecified Scout run") {
		t.Fatalf("missing run identity was not rejected: %v", err)
	}
}

func TestReconcilePortableRecoveryRecordAllowsFreshRunIdentity(t *testing.T) {
	// Portable recovery is intentionally permitted to cross a process/session
	// boundary. Live-session reconciliation still uses the strict public method.
	s := &Session{runID: "new-process-run"}
	record, err := NewActionRecoveryRecord(&ActionOutcomeError{
		RunID: "original-run", Action: "click", TargetID: "target-1", BrowserInstanceID: "browser-fingerprint", Outcome: OutcomeUnknown, OperationOrdinal: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("portable recovery record invalid: %v", err)
	}
	// The method delegates to target verification after the run boundary; this
	// test establishes the intended distinction without requiring a browser.
	if _, err := s.ReconcileActionOutcomeContext(context.Background(), record.Outcome()); err == nil || !strings.Contains(err.Error(), "different or unspecified Scout run") {
		t.Fatalf("live reconciliation accepted a foreign run: %v", err)
	}
}

func TestStartTraceCapturesExecutionContract(t *testing.T) {
	s := newTestSession()
	s.timeout = 12 * time.Second
	s.allowedOrigins = []string{"https://app.example", "https://api.example"}
	s.allowPrivateIPs = true
	s.proxyServer = "http://proxy.example:8080"
	s.remoteBrowserURL = "ws://browser.example/devtools"

	if err := s.StartTraceWithOutputBudgetContext(context.Background(), 256*1024); err != nil {
		t.Fatal(err)
	}
	metadata := s.trace.run
	if metadata.SchemaVersion != 1 || metadata.ConfiguredTimeoutMS != 12000 || metadata.OutputBudgetBytes != 256*1024 || !metadata.AllowPrivateIPs || !metadata.ProxyConfigured || !metadata.RemoteBrowser {
		t.Fatalf("execution contract metadata incomplete: %+v", metadata)
	}
	if metadata.EgressCoverage.PageTargetsCount != 0 || metadata.EgressCoverage.PageTargetsPolicyActive != 0 || metadata.EgressCoverage.PagePolicyDecisions != 0 || metadata.EgressCoverage.ProxyEnforcement != "unverified" || len(metadata.EgressCoverage.KnownUnobservedPaths) != 2 {
		t.Fatalf("egress coverage overstates or omits coverage: %+v", metadata.EgressCoverage)
	}
	if len(metadata.AllowedOrigins) != 2 || metadata.AllowedOrigins[0] != "https://app.example" {
		t.Fatalf("allowed origins not captured: %+v", metadata.AllowedOrigins)
	}
}

func TestTraceActionCapturesCallerDeadline(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{}
	deadline := time.Now().Add(5 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	s.traceAfterActionContext(ctx, time.Now(), traceScreenshotReference{}, "click", "#save", "", "", nil)
	if len(s.trace.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(s.trace.events))
	}
	event := s.trace.events[0]
	if event.DeadlineAt != deadline.UnixMilli() || event.BudgetRemainingMS <= 0 || event.BudgetRemainingMS > 5000 {
		t.Fatalf("caller deadline not captured accurately: %+v", event)
	}
}

func TestTraceActionCapturesBrowserOwnership(t *testing.T) {
	for _, ownership := range []string{"scout_launched", "remote_cdp"} {
		t.Run(ownership, func(t *testing.T) {
			s := &Session{tracing: true, browserOwnership: ownership, trace: &traceState{}}
			s.traceAfterActionContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "#save", "", "", nil)
			if len(s.trace.events) != 1 || s.trace.events[0].BrowserOwnership != ownership {
				t.Fatalf("trace event ownership = %+v, want %q", s.trace.events, ownership)
			}
		})
	}
}

func TestTraceSubmitRecordsApplicationAcceptance(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{}
	result := &SubmitFormResult{TargetID: "page-1", Submitted: true, ResponseObserved: true, ApplicationAccepted: true, Verified: true, Outcome: string(OutcomePostconditionVerified), Evidence: "confirmation visible"}
	s.recordSubmitTrace(context.Background(), time.Now(), traceScreenshotReference{}, "#checkout", result)
	if len(s.trace.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(s.trace.events))
	}
	event := s.trace.events[0]
	if event.Action != "submit_form" || event.TargetID != "page-1" || event.Outcome != string(OutcomePostconditionVerified) || !event.Verified || !event.VerificationKnown || event.Selector != "#checkout" {
		t.Fatalf("submission result not reflected in trace: %+v", event)
	}
}

func TestTraceFillFormRecordsValueMismatch(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{}
	start := time.Now()
	s.traceAfterActionContext(context.Background(), start, traceScreenshotReference{}, "fill_form", "", "", "", nil)
	s.applyTraceResult("fill_form", "", "page-1", string(OutcomeValueMismatch), "one field differed", "inspect the field", false)
	event := s.trace.events[0]
	if event.Action != "fill_form" || event.TargetID != "page-1" || event.Outcome != string(OutcomeValueMismatch) || !event.VerificationKnown || event.Verified || event.RecoveryHint != "inspect the field" {
		t.Fatalf("form fill mismatch not reflected in trace: %+v", event)
	}
}

func TestTraceNavigationRecordsPostconditionResult(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{events: []TraceEvent{{Action: "navigate", URL: "https://example.test/", Outcome: string(OutcomeEffectUnverified)}}}
	result := &PageResult{TargetID: "page-1", Outcome: string(OutcomePostconditionVerified), Verified: true, Evidence: "expected text observed"}
	s.applyTraceResult("navigate", "https://example.test/", result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
	event := s.trace.events[0]
	if event.TargetID != result.TargetID || event.Outcome != result.Outcome || !event.Verified || !event.VerificationKnown || event.Evidence != result.Evidence {
		t.Fatalf("navigation postcondition not reflected in trace: %+v", event)
	}
}

func TestAggregateFillFormOutcomeDistinguishesFieldFailure(t *testing.T) {
	outcome, _, _ := aggregateFillFormOutcome(&FormResult{Success: false, Fields: []FieldResult{{Selector: "#missing", Outcome: string(OutcomeActionFailed)}}})
	if outcome != string(OutcomeActionFailed) {
		t.Fatalf("aggregate outcome = %q, want %q", outcome, OutcomeActionFailed)
	}
	outcome, _, _ = aggregateFillFormOutcome(&FormResult{Success: false, Fields: []FieldResult{{Selector: "#quantity", Outcome: string(OutcomeValueMismatch)}}})
	if outcome != string(OutcomeValueMismatch) {
		t.Fatalf("aggregate outcome = %q, want %q", outcome, OutcomeValueMismatch)
	}
}

func TestTraceArchiveIncludesRunManifest(t *testing.T) {
	s := newTestSession()
	path := filepath.Join(t.TempDir(), "trace.zip")
	trace := &traceState{run: TraceRunMetadata{SchemaVersion: 1, StartedAt: 1234, ConfiguredTimeoutMS: 5000}}
	if _, err := s.writeTraceZip(path, trace); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()

	for _, file := range archive.File {
		if file.Name != "run.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		var decoded TraceRunMetadata
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.SchemaVersion != 1 || decoded.StartedAt != 1234 || decoded.ConfiguredTimeoutMS != 5000 {
			t.Fatalf("unexpected run manifest: %+v", decoded)
		}
		if decoded.ContentTrust != ContentTrustUntrustedPage {
			t.Fatalf("trace run content_trust = %q, want %q", decoded.ContentTrust, ContentTrustUntrustedPage)
		}
		if decoded.EgressCoverage.PageTargetsCount != 0 || decoded.EgressCoverage.PageTargetsPolicyActive != 0 || decoded.EgressCoverage.PagePolicyDecisions != 0 || decoded.EgressCoverage.ProxyEnforcement != "" {
			t.Fatalf("legacy zero-value metadata unexpectedly claims egress coverage: %+v", decoded.EgressCoverage)
		}
		return
	}
	t.Fatal("trace archive missing run.json")
}

func TestTraceArchiveRedactsURLsEmbeddedInEvidenceAndErrors(t *testing.T) {
	s := newTestSession()
	trace := &traceState{events: []TraceEvent{{
		Index: 0, Action: "navigate", URL: "https://example.test/path?token=secret",
		Evidence:     "navigation completed at https://user:pass@example.test/path?token=secret#fragment",
		Error:        "request to https://example.test/path?token=secret failed",
		RecoveryHint: "inspect https://example.test/path?token=secret before retrying",
	}}}
	path := filepath.Join(t.TempDir(), "trace.zip")
	if _, err := s.writeTraceZip(path, trace); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != "trace.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		for _, secret := range []string{"token=secret", "user:pass", "#fragment"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("trace archive retained %q: %s", secret, data)
			}
		}
		for _, path := range []string{"https://example.test/path"} {
			if !bytes.Contains(data, []byte(path)) {
				t.Fatalf("trace archive lost useful URL path %q: %s", path, data)
			}
		}
		return
	}
	t.Fatal("trace archive missing trace.json")
}

func TestTraceEvidenceManifestReconcilesPerEventScreenshotStatus(t *testing.T) {
	s := newTestSession()
	trace := &traceState{
		screenshotCaptureAttempts: 3, screenshotCaptureSuccesses: 1,
		screenshotCaptureFailures: 1, screenshotUnavailable: 1,
		screenshotCaptureStates:   map[uint64]string{1: "captured", 2: "failed", 3: "unavailable"},
		screenshotCaptureConsumed: map[uint64]bool{1: true, 2: true, 3: true},
		events: []TraceEvent{
			{Index: 0, Action: "click", BeforeImg: "before", BeforeScreenshotStatus: "captured", AfterScreenshotStatus: "failed"},
			{Index: 1, Action: "navigate", BeforeScreenshotStatus: "unavailable", AfterScreenshotStatus: "not_captured"},
		},
	}
	path := filepath.Join(t.TempDir(), "trace-evidence.zip")
	if _, err := s.writeTraceZip(path, trace); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var manifest traceEvidenceManifest
	var events []TraceEvent
	var screenshotAssets int
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		switch file.Name {
		case "evidence-manifest.json":
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
		case "trace.json":
			if err := json.Unmarshal(data, &events); err != nil {
				t.Fatal(err)
			}
		case "network.json":
			var captures []NetworkCapture
			if err := json.Unmarshal(data, &captures); err != nil {
				t.Fatal(err)
			}
			for i, capture := range captures {
				if capture.ContentTrust != ContentTrustUntrustedPage {
					t.Fatalf("network capture %d content_trust = %q", i, capture.ContentTrust)
				}
			}
		case "screenshots/000_before.png":
			screenshotAssets++
		}
	}
	if manifest.ContentTrust != ContentTrustUntrustedPage || manifest.ScreenshotCaptureAttempts != 3 || manifest.ScreenshotCaptureSuccesses != 1 || manifest.ScreenshotCaptureFailures != 1 || manifest.ScreenshotUnavailable != 1 || manifest.ScreenshotsWritten != 1 || manifest.ScreenshotStatusMismatches != 0 || manifest.UnattachedCaptureAttempts != 0 || manifest.Coverage != "known_screenshot_gaps" {
		t.Fatalf("unexpected evidence manifest: %+v", manifest)
	}
	if screenshotAssets != 1 || len(events) != 2 || events[0].ContentTrust != ContentTrustUntrustedPage || events[1].ContentTrust != ContentTrustUntrustedPage || events[0].BeforeScreenshotStatus != "captured" || events[0].AfterScreenshotStatus != "failed" || events[0].BeforeImg != "" || events[1].BeforeScreenshotStatus != "unavailable" {
		t.Fatalf("event evidence does not match archive: assets=%d events=%+v", screenshotAssets, events)
	}
}

func TestTraceArchiveAddsProvenanceToLegacyEventAndNetworkEntries(t *testing.T) {
	s := newTestSession()
	s.network = &networkState{history: []NetworkCapture{{URL: "https://example.test/api", Method: "GET", Status: 200}}}
	trace := &traceState{events: []TraceEvent{{Action: "navigate", URL: "https://example.test"}}}
	path := filepath.Join(t.TempDir(), "trace-provenance.zip")
	if _, err := s.writeTraceZip(path, trace); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	seenTrace, seenNetwork := false, false
	for _, file := range archive.File {
		if file.Name != "trace.json" && file.Name != "network.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		switch file.Name {
		case "trace.json":
			var events []TraceEvent
			if err := json.Unmarshal(data, &events); err != nil {
				t.Fatal(err)
			}
			seenTrace = len(events) == 1 && events[0].ContentTrust == ContentTrustUntrustedPage
		case "network.json":
			var captures []NetworkCapture
			if err := json.Unmarshal(data, &captures); err != nil {
				t.Fatal(err)
			}
			seenNetwork = len(captures) == 1 && captures[0].ContentTrust == ContentTrustUntrustedPage
		}
	}
	if !seenTrace || !seenNetwork {
		t.Fatalf("archive provenance missing: trace=%t network=%t", seenTrace, seenNetwork)
	}
}

func TestTraceNetworkManifestUsesSharedCoverageVocabulary(t *testing.T) {
	cases := []struct {
		name        string
		state       *networkState
		mode        string
		coverage    string
		exportState string
	}{
		{name: "never started", mode: "not_started", coverage: "not_enabled", exportState: "capture_never_enabled"},
		{
			name: "diagnostics only", state: &networkState{observerMode: networkObserverDiagnostics, observersInstalled: true},
			mode: "diagnostics_only", coverage: "diagnostics_only", exportState: "diagnostics_only",
		},
		{
			name: "full capture", state: &networkState{enabled: true, captureEverEnabled: true, observersInstalled: true},
			mode: "full_capture", coverage: "active_no_known_loss", exportState: "no_known_loss_at_export",
		},
		{
			name: "observer unavailable", state: &networkState{enabled: true, captureEverEnabled: true, observerMode: networkObserverCapture},
			mode: "unavailable", coverage: "unavailable", exportState: "observer_unavailable",
		},
		{
			name: "disabled capture", state: &networkState{captureEverEnabled: true},
			mode: "disabled", coverage: "disabled_at_read", exportState: "capture_disabled_at_export",
		},
		{
			name: "known gaps", state: &networkState{enabled: true, captureEverEnabled: true, captureInterrupted: true, observersInstalled: true},
			mode: "full_capture", coverage: "known_gaps", exportState: "known_gaps",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession()
			s.network = tc.state
			path := filepath.Join(t.TempDir(), "trace.zip")
			if _, err := s.writeTraceZip(path, &traceState{}); err != nil {
				t.Fatal(err)
			}
			archive, err := zip.OpenReader(path)
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Close()
			var manifest traceNetworkManifest
			found := false
			for _, file := range archive.File {
				if file.Name != "network-manifest.json" {
					continue
				}
				found = true
				reader, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				decodeErr := json.NewDecoder(reader).Decode(&manifest)
				_ = reader.Close()
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
			}
			if !found {
				t.Fatal("trace archive missing network-manifest.json")
			}
			if manifest.Coverage != tc.coverage || manifest.CaptureMode != tc.mode || manifest.CaptureStateAtExport != tc.exportState {
				t.Fatalf("manifest state = (%q, %q, %q), want (%q, %q, %q)", manifest.Coverage, manifest.CaptureMode, manifest.CaptureStateAtExport, tc.coverage, tc.mode, tc.exportState)
			}
		})
	}
}

func TestTraceEvidenceManifestCountsAbandonedBeforeCapture(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{}
	_, before := s.traceBeforeActionContext(context.Background())
	if before.status != "unavailable" {
		t.Fatalf("before capture status = %q, want unavailable", before.status)
	}
	path := filepath.Join(t.TempDir(), "trace-abandoned-capture.zip")
	if _, err := s.writeTraceZip(path, s.trace); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != "evidence-manifest.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		var manifest traceEvidenceManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.TraceEvents != 0 || manifest.ScreenshotCaptureAttempts != 1 || manifest.ScreenshotUnavailable != 1 || manifest.UnattachedCaptureAttempts != 1 || manifest.ScreenshotStatusMismatches != 0 || manifest.Coverage != "known_screenshot_gaps" {
			t.Fatalf("abandoned capture not represented accurately: %+v", manifest)
		}
		return
	}
	t.Fatal("trace archive missing evidence-manifest.json")
}

func TestTraceArchiveIncludesURLPolicyDecisions(t *testing.T) {
	s := newTestSession()
	trace := &traceState{policyDecisions: []TraceURLPolicyDecision{{Index: 0, TargetID: "page-1", URL: "https://user:secret@blocked.example/path?token=secret#fragment", Method: "GET", ResourceType: "Document", Allowed: false, Reason: "origin denied at https://blocked.example/path?token=secret", Resolution: "failed", ResolutionError: "Fetch.failRequest failed for https://blocked.example/path?token=secret"}}, policyDropped: 2}
	path := filepath.Join(t.TempDir(), "trace.zip")
	if _, err := s.writeTraceZip(path, trace); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != "url-policy.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		var decoded struct {
			Decisions []TraceURLPolicyDecision `json:"decisions"`
			Dropped   int                      `json:"dropped"`
		}
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Decisions) != 1 || decoded.Decisions[0].Allowed || decoded.Dropped != 2 || decoded.Decisions[0].TargetID != "page-1" {
			t.Fatalf("unexpected policy decision archive: %+v", decoded)
		}
		decision := decoded.Decisions[0]
		if decision.Resolution != "failed" || !strings.Contains(decision.ResolutionError, "blocked.example/path") || strings.Contains(decision.ResolutionError, "token=secret") {
			t.Fatalf("resolution evidence or redaction is incorrect: %+v", decision)
		}
		if strings.Contains(decision.URL, "secret") || strings.Contains(decision.URL, "token=") || strings.Contains(decision.Reason, "token=secret") {
			t.Fatalf("URL policy trace leaked URL credentials or query data: %+v", decision)
		}
		return
	}
	t.Fatal("trace archive missing url-policy.json")
}

func TestTraceRunMetadata_JSON(t *testing.T) {
	metadata := TraceRunMetadata{TraceID: "trace-123", SchemaVersion: 1, StartedAt: 1234, ConfiguredTimeoutMS: 5000, AllowedOrigins: []string{"https://example.com"}, ProxyConfigured: true}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TraceRunMetadata
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TraceID != metadata.TraceID || decoded.SchemaVersion != metadata.SchemaVersion || !decoded.ProxyConfigured || len(decoded.AllowedOrigins) != 1 {
		t.Fatalf("metadata JSON roundtrip failed: %+v", decoded)
	}
}
