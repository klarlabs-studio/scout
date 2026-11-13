package agent

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	browse "go.klarlabs.de/scout"
	cdpclient "go.klarlabs.de/scout/internal/cdp"
)

type traceState struct {
	events                     []TraceEvent
	actionOrdinal              int
	screenshotCaptureAttempts  int
	screenshotCaptureSuccesses int
	screenshotCaptureFailures  int
	screenshotUnavailable      int
	policyDecisions            []TraceURLPolicyDecision
	policyDropped              int
	policyCursors              map[*browse.Page]int
	policyPages                map[*browse.Page]bool
	screenshotCaptureSequence  uint64
	screenshotCaptureStates    map[uint64]string
	screenshotCaptureConsumed  map[uint64]bool
	startTime                  time.Time
	run                        TraceRunMetadata
}

type traceNetworkManifest struct {
	SchemaVersion          int    `json:"schema_version"`
	ContentTrust           string `json:"content_trust,omitempty"`
	CoverageScope          string `json:"coverage_scope"`
	Coverage               string `json:"coverage"`
	CaptureMode            string `json:"capture_mode"`
	CaptureStateAtExport   string `json:"capture_state_at_export"`
	CaptureEnabled         bool   `json:"capture_enabled"`
	CaptureEverEnabled     bool   `json:"capture_ever_enabled"`
	CaptureInterrupted     bool   `json:"capture_interrupted"`
	URLPatternFilterActive bool   `json:"url_pattern_filter_active"`
	RetainedRequests       int    `json:"retained_requests"`
	PendingRequests        int    `json:"pending_requests"`
	DroppedRequests        int    `json:"dropped_requests"`
	HistoryLimit           int    `json:"history_limit"`
}

type traceEvidenceManifest struct {
	SchemaVersion              int    `json:"schema_version"`
	ContentTrust               string `json:"content_trust,omitempty"`
	CoverageScope              string `json:"coverage_scope"`
	Coverage                   string `json:"coverage"`
	TraceEvents                int    `json:"trace_events"`
	ScreenshotCaptureExpected  int    `json:"screenshot_capture_expected"`
	ScreenshotsWritten         int    `json:"screenshots_written"`
	ScreenshotCaptureAttempts  int    `json:"screenshot_capture_attempts"`
	ScreenshotCaptureSuccesses int    `json:"screenshot_capture_successes"`
	ScreenshotCaptureFailures  int    `json:"screenshot_capture_failures"`
	ScreenshotUnavailable      int    `json:"screenshot_unavailable"`
	ScreenshotStatusMismatches int    `json:"screenshot_status_mismatches"`
	UnattachedCaptureAttempts  int    `json:"unattached_capture_attempts"`
}

type traceScreenshotCapture struct {
	data   []byte
	status string
	id     uint64
}

type traceScreenshotReference struct {
	data   []byte
	status string
	id     uint64
}

func newRunID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Sprintf("run-%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(id[:])
}

func (s *Session) StartTrace() error {
	return s.StartTraceContext(context.Background())
}

// StartTraceContext starts recording after acquiring the session lock under ctx.
func (s *Session) StartTraceContext(ctx context.Context) error {
	return s.StartTraceWithOutputBudgetContext(ctx, 0)
}

// StartTraceWithOutputBudgetContext starts a trace and records the caller's
// serialized output ceiling in the run manifest. Zero means no ceiling was supplied.
func (s *Session) StartTraceWithOutputBudgetContext(ctx context.Context, outputBudgetBytes int64) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()

	if s.tracing {
		return fmt.Errorf("trace already in progress")
	}
	traceID := newRunID()

	s.tracing = true
	startedAt := time.Now()
	s.trace = &traceState{
		startTime: startedAt,
		run: TraceRunMetadata{
			TraceID:             traceID,
			ContentTrust:        ContentTrustUntrustedPage,
			SchemaVersion:       1,
			StartedAt:           startedAt.UnixMilli(),
			ConfiguredTimeoutMS: s.timeout.Milliseconds(),
			OutputBudgetBytes:   outputBudgetBytes,
			AllowedOrigins:      append([]string(nil), s.allowedOrigins...),
			AllowPrivateIPs:     s.allowPrivateIPs,
			PolicyID:            taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs),
			ProxyConfigured:     s.proxyServer != "",
			ProxyAppliedByScout: s.proxyAppliedByScout,
			RemoteBrowser:       s.remoteBrowserURL != "",
			BrowserOwnership:    s.browserOwnership,
			NetworkRedacted:     true,
			EgressCoverage: TraceEgressCoverage{
				KnownUnobservedPaths: []string{"service_worker_requests", "websocket_handshakes"},
				ProxyEnforcement:     "unverified",
			},
		},
	}
	return nil
}

func (s *Session) StopTrace(path string) (*TraceResult, error) {
	return s.StopTraceContext(context.Background(), path)
}

// StopTraceContext exports the current trace while honoring ctx. If export is
// interrupted, the trace remains active and can be exported again.
func (s *Session) StopTraceContext(ctx context.Context, path string) (*TraceResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if !s.tracing || s.trace == nil {
		return nil, fmt.Errorf("no trace in progress")
	}

	trace := s.trace
	if s.trace.policyPages == nil {
		s.trace.policyPages = make(map[*browse.Page]bool)
	}
	if s.tabs != nil {
		for _, tab := range s.tabs.tabs {
			s.collectPageTraceURLPolicyDecisions(tab.page)
		}
	} else {
		s.collectTraceURLPolicyDecisions()
	}
	trace.run.EgressCoverage.PageTargetsCount = len(trace.policyPages)
	trace.run.EgressCoverage.PagePolicyDecisions = len(trace.policyDecisions)
	for _, page := range trace.policyPages {
		if page {
			trace.run.EgressCoverage.PageTargetsPolicyActive++
		}
	}
	result, err := s.writeTraceZipContext(ctx, path, trace)
	if err != nil {
		return nil, err
	}
	s.tracing = false
	s.trace = nil
	return result, nil
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) writeTraceZip(path string, trace *traceState) (*TraceResult, error) {
	return s.writeTraceZipContext(context.Background(), path, trace)
}

func (s *Session) writeTraceZipContext(ctx context.Context, path string, trace *traceState) (*TraceResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Write to a sibling temporary file so cancellation or an encode error does
	// not leave a partial artifact at the requested destination.
	f, err := os.CreateTemp(filepath.Dir(path), ".scout-trace-*.zip")
	if err != nil {
		return nil, fmt.Errorf("failed to create trace file: %w", err)
	}
	tempPath := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	defer func() { _ = f.Close() }()

	w := zip.NewWriter(f)
	defer func() { _ = w.Close() }()

	cleanEvents := make([]TraceEvent, len(trace.events))
	for i, ev := range trace.events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cleanEvents[i] = ev
		cleanEvents[i].ContentTrust = ContentTrustUntrustedPage
		cleanEvents[i].BeforeImg = ""
		cleanEvents[i].AfterImg = ""
		cleanEvents[i].URL = redactTraceURL(ev.URL)
		cleanEvents[i].Evidence = redactTraceTextURLs(ev.Evidence)
		cleanEvents[i].RecoveryHint = redactTraceTextURLs(ev.RecoveryHint)
		cleanEvents[i].Error = redactTraceTextURLs(ev.Error)
		if ev.Value != "" {
			cleanEvents[i].Value = ""
			cleanEvents[i].ActionValueRedacted = true
		}
	}

	traceJSON, err := json.MarshalIndent(cleanEvents, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal trace events: %w", err)
	}
	tw, err := w.Create("trace.json")
	if err != nil {
		return nil, err
	}
	if _, err := tw.Write(traceJSON); err != nil {
		return nil, err
	}
	runMetadata := trace.run
	runMetadata.ContentTrust = ContentTrustUntrustedPage
	runMetadata.AllowedOrigins = append([]string(nil), trace.run.AllowedOrigins...)
	for i, origin := range runMetadata.AllowedOrigins {
		runMetadata.AllowedOrigins[i] = redactTraceOrigin(origin)
	}
	runJSON, err := json.MarshalIndent(runMetadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal trace run metadata: %w", err)
	}
	rw, err := w.Create("run.json")
	if err != nil {
		return nil, err
	}
	if _, err := rw.Write(runJSON); err != nil {
		return nil, err
	}

	networkData := make([]NetworkCapture, 0)
	networkManifest := traceNetworkManifest{SchemaVersion: 1, ContentTrust: ContentTrustUntrustedPage, CoverageScope: "session_network_capture_window", Coverage: "not_enabled", CaptureStateAtExport: "capture_never_enabled", CaptureMode: "not_started"}
	if s.network != nil {
		s.network.mu.Lock()
		networkData = make([]NetworkCapture, len(s.network.history))
		copy(networkData, s.network.history)
		networkManifest.CaptureEnabled = s.network.enabled
		networkManifest.CaptureEverEnabled = s.network.captureEverEnabled
		networkManifest.CaptureMode = networkCaptureMode(s.network)
		networkManifest.CaptureInterrupted = s.network.captureInterrupted
		networkManifest.URLPatternFilterActive = len(s.network.patterns) > 0
		networkManifest.RetainedRequests = len(s.network.history)
		networkManifest.PendingRequests = len(s.network.pending)
		networkManifest.DroppedRequests = s.network.droppedRequests
		networkManifest.HistoryLimit = networkRecordLimit(s.network)
		switch {
		case !s.network.observersInstalled && (s.network.enabled || s.network.observerMode != networkObserverNone):
			networkManifest.Coverage = "unavailable"
			networkManifest.CaptureStateAtExport = "observer_unavailable"
		case networkManifest.PendingRequests > 0 || networkManifest.DroppedRequests > 0 || networkManifest.CaptureInterrupted:
			networkManifest.Coverage = "known_gaps"
			networkManifest.CaptureStateAtExport = "known_gaps"
		case !s.network.captureEverEnabled && networkManifest.CaptureMode == "diagnostics_only":
			networkManifest.Coverage = "diagnostics_only"
			networkManifest.CaptureStateAtExport = "diagnostics_only"
		case s.network.captureEverEnabled:
			networkManifest.Coverage = "active_no_known_loss"
			networkManifest.CaptureStateAtExport = "no_known_loss_at_export"
			if !s.network.enabled {
				networkManifest.Coverage = "disabled_at_read"
				networkManifest.CaptureStateAtExport = "capture_disabled_at_export"
			}
		}
		s.network.mu.Unlock()
	}
	networkData = redactNetworkCapturesForTrace(networkData)
	for i := range networkData {
		networkData[i].ContentTrust = ContentTrustUntrustedPage
	}
	netJSON, err := json.MarshalIndent(networkData, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal network data: %w", err)
	}
	nw, err := w.Create("network.json")
	if err != nil {
		return nil, err
	}
	if _, err := nw.Write(netJSON); err != nil {
		return nil, err
	}
	nmJSON, err := json.MarshalIndent(networkManifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal network manifest: %w", err)
	}
	nmw, err := w.Create("network-manifest.json")
	if err != nil {
		return nil, err
	}
	if _, err := nmw.Write(nmJSON); err != nil {
		return nil, err
	}
	policyDecisions := append([]TraceURLPolicyDecision(nil), trace.policyDecisions...)
	for i := range policyDecisions {
		policyDecisions[i].URL = redactTraceURL(policyDecisions[i].URL)
		policyDecisions[i].Reason = redactTraceTextURLs(policyDecisions[i].Reason)
		policyDecisions[i].ResolutionError = redactTraceTextURLs(policyDecisions[i].ResolutionError)
	}
	if policyDecisions == nil {
		policyDecisions = []TraceURLPolicyDecision{}
	}
	policyJSON, err := json.MarshalIndent(struct {
		Decisions []TraceURLPolicyDecision `json:"decisions"`
		Dropped   int                      `json:"dropped"`
	}{policyDecisions, trace.policyDropped}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal URL policy data: %w", err)
	}
	pw, err := w.Create("url-policy.json")
	if err != nil {
		return nil, err
	}
	if _, err := pw.Write(policyJSON); err != nil {
		return nil, err
	}

	for i, ev := range trace.events {
		if ev.BeforeImg != "" {
			iw, err := w.Create(fmt.Sprintf("screenshots/%03d_before.png", i))
			if err != nil {
				return nil, err
			}
			if _, err := iw.Write([]byte(ev.BeforeImg)); err != nil {
				return nil, err
			}
		}
		if ev.AfterImg != "" {
			iw, err := w.Create(fmt.Sprintf("screenshots/%03d_after.png", i))
			if err != nil {
				return nil, err
			}
			if _, err := iw.Write([]byte(ev.AfterImg)); err != nil {
				return nil, err
			}
		}
	}
	evidenceManifest := traceEvidenceManifest{
		SchemaVersion:             1,
		ContentTrust:              ContentTrustUntrustedPage,
		CoverageScope:             "trace_event_screenshots",
		Coverage:                  "no_trace_events",
		TraceEvents:               len(trace.events),
		ScreenshotCaptureExpected: len(trace.events) * 2,
	}
	for _, event := range trace.events {
		for _, status := range []string{event.BeforeScreenshotStatus, event.AfterScreenshotStatus} {
			switch status {
			case "captured":
				evidenceManifest.ScreenshotCaptureAttempts++
				evidenceManifest.ScreenshotCaptureSuccesses++
			case "failed":
				evidenceManifest.ScreenshotCaptureAttempts++
				evidenceManifest.ScreenshotCaptureFailures++
			case "unavailable":
				evidenceManifest.ScreenshotCaptureAttempts++
				evidenceManifest.ScreenshotUnavailable++
			}
		}
		if event.BeforeImg != "" {
			evidenceManifest.ScreenshotsWritten++
			if event.BeforeScreenshotStatus != "captured" {
				evidenceManifest.ScreenshotStatusMismatches++
			}
		} else if event.BeforeScreenshotStatus == "captured" {
			evidenceManifest.ScreenshotStatusMismatches++
		}
		if event.AfterImg != "" {
			evidenceManifest.ScreenshotsWritten++
			if event.AfterScreenshotStatus != "captured" {
				evidenceManifest.ScreenshotStatusMismatches++
			}
		} else if event.AfterScreenshotStatus == "captured" {
			evidenceManifest.ScreenshotStatusMismatches++
		}
	}
	for captureID, status := range trace.screenshotCaptureStates {
		if trace.screenshotCaptureConsumed[captureID] {
			continue
		}
		evidenceManifest.UnattachedCaptureAttempts++
		switch status {
		case "captured":
			evidenceManifest.ScreenshotCaptureSuccesses++
		case "failed":
			evidenceManifest.ScreenshotCaptureFailures++
		case "unavailable":
			evidenceManifest.ScreenshotUnavailable++
		}
	}
	evidenceManifest.ScreenshotCaptureAttempts += evidenceManifest.UnattachedCaptureAttempts
	if evidenceManifest.ScreenshotCaptureAttempts != trace.screenshotCaptureAttempts ||
		evidenceManifest.ScreenshotCaptureSuccesses != trace.screenshotCaptureSuccesses ||
		evidenceManifest.ScreenshotCaptureFailures != trace.screenshotCaptureFailures ||
		evidenceManifest.ScreenshotUnavailable != trace.screenshotUnavailable {
		evidenceManifest.ScreenshotStatusMismatches++
	}
	if len(trace.events) > 0 {
		evidenceManifest.Coverage = "complete_screenshots"
		if evidenceManifest.ScreenshotsWritten == 0 {
			evidenceManifest.Coverage = "screenshots_not_captured"
		} else if evidenceManifest.ScreenshotCaptureFailures > 0 || evidenceManifest.ScreenshotUnavailable > 0 || evidenceManifest.UnattachedCaptureAttempts > 0 || evidenceManifest.ScreenshotsWritten < evidenceManifest.ScreenshotCaptureExpected {
			evidenceManifest.Coverage = "known_screenshot_gaps"
		}
	} else if evidenceManifest.UnattachedCaptureAttempts > 0 {
		evidenceManifest.Coverage = "known_screenshot_gaps"
	}
	evidenceJSON, err := json.MarshalIndent(evidenceManifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal trace evidence manifest: %w", err)
	}
	ew, err := w.Create("evidence-manifest.json")
	if err != nil {
		return nil, err
	}
	if _, err := ew.Write(evidenceJSON); err != nil {
		return nil, err
	}

	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return nil, fmt.Errorf("failed to finalize trace file: %w", err)
	}
	committed = true

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var totalDuration int64
	if len(trace.events) > 0 {
		totalDuration = time.Since(trace.startTime).Milliseconds()
	}

	return &TraceResult{
		TraceID:      trace.run.TraceID,
		ContentTrust: ContentTrustUntrustedPage,
		Path:         path,
		EventCount:   len(trace.events),
		Duration:     totalDuration,
		Size:         info.Size(),
		Artifact:     &ArtifactReference{Path: path, Kind: "trace", MIMEType: "application/zip", SizeBytes: info.Size(), SHA256: fileSHA256(path)},
	}, nil
}

// redactNetworkCapturesForTrace keeps diagnostic routing/status information
// while removing common secret-bearing URL and payload data from portable
// artifacts. Live CapturedRequests results retain their original detail.
func redactNetworkCapturesForTrace(captures []NetworkCapture) []NetworkCapture {
	for i := range captures {
		captures[i].URL = redactTraceURL(captures[i].URL)
		captures[i].RequestHeaders = safeTraceHeaders(captures[i].RequestHeaders)
		captures[i].ResponseHeaders = safeTraceHeaders(captures[i].ResponseHeaders)
		captures[i].RequestBody = ""
		captures[i].ResponseBody = ""
	}
	return captures
}

func redactTraceURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[redacted URL]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

var traceURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// redactTraceTextURLs removes credentials, query strings, and fragments from
// URLs embedded in portable trace text while retaining their path for context.
func redactTraceTextURLs(value string) string {
	return traceURLPattern.ReplaceAllStringFunc(value, redactTraceURL)
}

func redactTraceOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[redacted origin]"
	}
	parsed.User = nil
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func safeTraceHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"Accept": true, "Content-Length": true, "Content-Type": true,
		"Cache-Control": true,
	}
	redacted := make(map[string]string)
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if allowed[canonical] {
			redacted[canonical] = value
		}
	}
	if len(redacted) == 0 {
		return nil
	}
	return redacted
}

func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) captureTraceScreenshot() []byte {
	return s.captureTraceScreenshotContext(context.Background())
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) captureTraceScreenshotContext(ctx context.Context) []byte {
	return s.captureTraceScreenshotEvidenceContext(ctx).data
}

func (s *Session) captureTraceScreenshotEvidenceContext(ctx context.Context) traceScreenshotCapture {
	if !s.tracing || s.trace == nil {
		return traceScreenshotCapture{status: "not_captured"}
	}
	s.trace.screenshotCaptureAttempts++
	s.trace.screenshotCaptureSequence++
	captureID := s.trace.screenshotCaptureSequence
	if s.trace.screenshotCaptureStates == nil {
		s.trace.screenshotCaptureStates = make(map[uint64]string)
	}
	if s.page == nil {
		s.trace.screenshotUnavailable++
		s.trace.screenshotCaptureStates[captureID] = "unavailable"
		return traceScreenshotCapture{status: "unavailable", id: captureID}
	}
	data, err := s.page.ScreenshotWithOptionsContext(ctx, browse.ScreenshotOptions{
		MaxSize: 200 * 1024,
	})
	if err != nil || len(data) == 0 {
		s.trace.screenshotCaptureFailures++
		s.trace.screenshotCaptureStates[captureID] = "failed"
		return traceScreenshotCapture{status: "failed", id: captureID}
	}
	s.trace.screenshotCaptureSuccesses++
	s.trace.screenshotCaptureStates[captureID] = "captured"
	return traceScreenshotCapture{data: data, status: "captured", id: captureID}
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) traceBeforeAction() (time.Time, traceScreenshotReference) {
	return s.traceBeforeActionContext(context.Background())
}

func (s *Session) traceBeforeActionContext(ctx context.Context) (time.Time, traceScreenshotReference) {
	if !s.tracing {
		if s.tracer != nil {
			return time.Now(), traceScreenshotReference{}
		}
		return time.Time{}, traceScreenshotReference{}
	}
	capture := s.captureTraceScreenshotEvidenceContext(ctx)
	return time.Now(), traceScreenshotReference(capture)
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) traceAfterAction(start time.Time, before traceScreenshotReference, selector string, actionErr error) {
	s.traceAfterActionContext(context.Background(), start, before, "click", selector, "", "", actionErr)
}

func (s *Session) traceAfterActionContext(ctx context.Context, start time.Time, before traceScreenshotReference, action, selector, value, url string, actionErr error) {
	endedAt := time.Now()
	if !start.IsZero() {
		s.lastActionName = traceActionForResult(action)
		s.lastActionStartedAt = start
		s.lastActionEndedAt = endedAt
	}
	if !s.tracing || s.trace == nil {
		return
	}
	if before.id != 0 {
		if s.trace.screenshotCaptureConsumed == nil {
			s.trace.screenshotCaptureConsumed = make(map[uint64]bool)
		}
		s.trace.screenshotCaptureConsumed[before.id] = true
	}
	if s.tabs != nil && s.tabs.active != "" {
		if tab := s.tabs.tabs[s.tabs.active]; tab != nil {
			s.collectPageTraceURLPolicyDecisions(tab.page)
		}
	} else {
		s.collectTraceURLPolicyDecisions()
	}

	afterCapture := s.captureTraceScreenshotEvidenceContext(ctx)
	if afterCapture.id != 0 {
		if s.trace.screenshotCaptureConsumed == nil {
			s.trace.screenshotCaptureConsumed = make(map[uint64]bool)
		}
		s.trace.screenshotCaptureConsumed[afterCapture.id] = true
	}

	s.trace.actionOrdinal++
	var outcomeErr *ActionOutcomeError
	operationOrdinal := 0
	if errors.As(actionErr, &outcomeErr) {
		operationOrdinal = outcomeErr.OperationOrdinal
	}
	if operationOrdinal == 0 {
		operationOrdinal = s.nextOperationOrdinal()
	}
	ev := TraceEvent{
		Index:                  len(s.trace.events),
		ContentTrust:           ContentTrustUntrustedPage,
		ActionOrdinal:          s.trace.actionOrdinal,
		OperationOrdinal:       operationOrdinal,
		Action:                 action,
		BrowserOwnership:       s.browserOwnership,
		Outcome:                string(OutcomeEffectUnverified),
		Selector:               selector,
		Value:                  value,
		URL:                    url,
		Timestamp:              start.UnixMilli(),
		Duration:               time.Since(start).Milliseconds(),
		BeforeScreenshotStatus: before.status,
		AfterScreenshotStatus:  afterCapture.status,
	}
	if deadline, ok := ctx.Deadline(); ok {
		ev.DeadlineAt = deadline.UnixMilli()
		ev.BudgetRemainingMS = time.Until(deadline).Milliseconds()
	}
	if s.page != nil {
		ev.TargetID = s.page.TargetID()
	}
	if actionErr != nil {
		ev.Error = actionErr.Error()
		ev.Outcome = string(OutcomeActionFailed)
		if errors.As(actionErr, &outcomeErr) {
			ev.Action = outcomeErr.Action
			ev.Selector = outcomeErr.Target
			ev.Outcome = string(outcomeErr.Outcome)
			ev.TargetID = outcomeErr.TargetID
			ev.URL = redactTraceURL(outcomeErr.URL)
			ev.Evidence = outcomeErr.Evidence
			ev.RecoveryHint = outcomeErr.RecoveryHint
		} else {
			var navigationErr *browse.NavigationOutcomeUnknownError
			if errors.As(actionErr, &navigationErr) {
				ev.Outcome = string(OutcomeUnknown)
				ev.URL = redactTraceURL(navigationErr.URL)
				ev.Evidence = "Navigation may have been dispatched; no authoritative completion was received"
				ev.RecoveryHint = "Inspect the current page URL and session status before retrying; navigation may already have completed."
			}
			var dispatchErr *cdpclient.DispatchOutcomeUnknownError
			if !errors.As(actionErr, &navigationErr) && errors.As(actionErr, &dispatchErr) {
				ev.Outcome = string(OutcomeUnknown)
				ev.Evidence = "CDP " + dispatchErr.Method + " may have been dispatched; no authoritative response was received"
				ev.RecoveryHint = "Inspect the current page and session status before retrying; the action may already have completed."
			}
		}
	}
	if before.data != nil {
		ev.BeforeImg = string(before.data)
	}
	if afterCapture.data != nil {
		ev.AfterImg = string(afterCapture.data)
	}

	s.trace.events = append(s.trace.events, ev)
}

func (s *Session) collectTraceURLPolicyDecisions() {
	if !s.tracing || s.trace == nil || s.page == nil {
		return
	}
	s.collectPageTraceURLPolicyDecisions(s.page)
}

func (s *Session) collectPageTraceURLPolicyDecisions(page *browse.Page) {
	if !s.tracing || s.trace == nil || page == nil {
		return
	}
	if s.trace.policyCursors == nil {
		s.trace.policyCursors = make(map[*browse.Page]int)
	}
	if s.trace.policyPages == nil {
		s.trace.policyPages = make(map[*browse.Page]bool)
	}
	s.trace.policyPages[page] = page.URLPolicyInstalled()
	pageID := page.TargetID()
	decisions, dropped := page.URLPolicyDecisions()
	cursor := s.trace.policyCursors[page]
	if cursor > len(decisions) {
		cursor = len(decisions)
	}
	for _, decision := range decisions[cursor:] {
		s.trace.policyDecisions = append(s.trace.policyDecisions, TraceURLPolicyDecision{
			Index: len(s.trace.policyDecisions), TargetID: pageID,
			Timestamp: decision.Timestamp, URL: decision.URL, Method: decision.Method,
			ResourceType: decision.ResourceType, Allowed: decision.Allowed,
			ReasonCode: decision.ReasonCode, PolicyID: decision.PolicyID, Reason: decision.Reason,
			Resolution: decision.Resolution, ResolutionError: decision.ResolutionError,
		})
	}
	s.trace.policyDropped += dropped
	s.trace.policyCursors[page] = len(decisions)
}

func (s *Session) traceActionFailureContext(ctx context.Context, start time.Time, before traceScreenshotReference, action, selector, value, url string, actionErr error) error {
	var outcomeErr *ActionOutcomeError
	if errors.As(actionErr, &outcomeErr) {
		outcomeErr.TraceActionOrdinal = 0
		action = outcomeErr.Action
		selector = outcomeErr.Target
		if outcomeErr.FrameID == "" {
			outcomeErr.FrameID = s.frameID
			if outcomeErr.TargetID == "" && s.page != nil {
				outcomeErr.TargetID = s.page.TargetID()
			}
		}
		if outcomeErr.RecoveryHint == "" {
			outcomeErr.RecoveryHint = "Inspect the current page and session status before retrying; the action may already have completed."
		}
	}
	s.traceAfterActionContext(ctx, start, before, action, selector, value, url, actionErr)
	if errors.As(actionErr, &outcomeErr) {
		if outcomeErr.OperationOrdinal == 0 && s.tracing && s.trace != nil && len(s.trace.events) > 0 {
			outcomeErr.OperationOrdinal = s.trace.events[len(s.trace.events)-1].OperationOrdinal
		} else if outcomeErr.OperationOrdinal == 0 {
			outcomeErr.OperationOrdinal = s.nextOperationOrdinal()
		}
		if deadline, ok := ctx.Deadline(); ok {
			outcomeErr.DeadlineAt = deadline.UnixMilli()
		}
		outcomeErr.ConfiguredTimeoutMS = s.timeout.Milliseconds()
		outcomeErr.SessionDead = s.sessionDead
		outcomeErr.ConsecutiveTimeouts = s.consecutiveTimeouts
		outcomeErr.InFlightCommands = s.inflightCommands
		if outcomeErr.TargetID == "" && s.page != nil {
			outcomeErr.TargetID = s.page.TargetID()
		}
		if outcomeErr.FrameID == "" && s.page != nil && outcomeErr.TargetID == s.page.TargetID() {
			outcomeErr.FrameID = s.frameID
		}
		if outcomeErr.RecoveryHint == "" {
			outcomeErr.RecoveryHint = "Inspect the current page and session status before retrying; the action may already have completed."
		}
		if s.tracing && s.trace != nil && len(s.trace.events) > 0 {
			outcomeErr.TraceID = s.trace.run.TraceID
			outcomeErr.TraceStep = s.trace.events[len(s.trace.events)-1].Index + 1
			outcomeErr.TraceActionOrdinal = s.trace.events[len(s.trace.events)-1].ActionOrdinal
		}
		outcomeErr.Receipt = s.newActionReceipt(outcomeErr.Action, outcomeErr.Target, outcomeErr.TargetID, string(outcomeErr.Outcome), outcomeErr.Evidence, outcomeErr.RecoveryHint, false, outcomeErr.OperationOrdinal, outcomeErr.ExpectedPostcondition, ctx)
		outcomeErr.Receipt.ResponseEvidence = outcomeErr.ResponseEvidence
		outcomeErr.Receipt.FrameID = outcomeErr.FrameID
		outcomeErr.Receipt.DeadlineAt = outcomeErr.DeadlineAt
		outcomeErr.Receipt.TraceID = outcomeErr.TraceID
		outcomeErr.Receipt.TraceAction = outcomeErr.TraceActionOrdinal
	}
	return actionErr
}

// applyTraceResult attaches the outcome returned by the corresponding action
// helper to the most recent compatible trace event. Actions are serialized by
// the session lock, so reverse lookup identifies the event just recorded.
func (s *Session) applyTraceResult(action, target, targetID, outcome, evidence, recoveryHint string, verified bool) {
	if !s.tracing || s.trace == nil {
		return
	}
	for i := len(s.trace.events) - 1; i >= 0; i-- {
		ev := &s.trace.events[i]
		if ev.Action != action {
			continue
		}
		if ev.Selector != target && ev.URL != target && target != "" {
			continue
		}
		if ev.TargetID == "" {
			ev.TargetID = targetID
		}
		ev.Outcome = outcome
		ev.Verified = verified
		ev.VerificationKnown = true
		ev.Evidence = evidence
		ev.RecoveryHint = recoveryHint
		return
	}
}
