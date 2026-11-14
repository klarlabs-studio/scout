package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	browse "go.klarlabs.de/scout"
	cdpclient "go.klarlabs.de/scout/internal/cdp"
)

type targetPresenceBrowser struct {
	browse.Browser
	present    bool
	instanceID string
	ctxErr     error
	ctxDone    bool
}

func TestCleanupUninitializedRemotePageReportsDetachFailure(t *testing.T) {
	const policyErr = "policy setup rejected by fixture"
	const detachErr = "detach rejected by fixture"
	var closeTargetCalls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			method, _ := req["method"].(string)
			id := req["id"]
			if method == "Target.closeTarget" {
				closeTargetCalls.Add(1)
			}
			var response map[string]any
			switch method {
			case "Target.getTargets":
				response = map[string]any{"id": id, "result": map[string]any{"targetInfos": []any{map[string]any{"targetId": "caller-owned", "type": "page"}}}}
			case "Target.attachToTarget":
				response = map[string]any{"id": id, "result": map[string]any{"sessionId": "session-1"}}
			case "Target.detachFromTarget":
				response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": detachErr}}
			case "Fetch.enable":
				response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": policyErr}}
			default:
				response = map[string]any{"id": id, "result": map[string]any{}}
			}
			if err := ws.WriteJSON(response); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	s, err := NewSessionContext(context.Background(), SessionConfig{RemoteCDP: wsURL, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	s.allowedOrigins = []string{"https://allowed.example"}
	err = s.ensurePageContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), policyErr) || !strings.Contains(err.Error(), detachErr) || !strings.Contains(err.Error(), "failed to detach uninitialized remote page") {
		t.Fatalf("setup error = %v, want policy setup and remote detach failures", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := closeTargetCalls.Load(); got != 0 {
		t.Fatalf("cleanup closed caller-owned target %d time(s), want detach-only cleanup", got)
	}
}

func TestRetainFailedFrameAttachmentDeduplicatesHandle(t *testing.T) {
	page := &browse.Page{}
	err := &browse.FrameTargetAttachError{Page: page, AttachedTargetID: "child-target", SetupError: errors.New("setup failed"), CleanupError: errors.New("detach failed")}
	s := &Session{}
	s.retainFailedFrameAttachment(err)
	s.retainFailedFrameAttachment(err)
	if len(s.orphanedFramePages) != 1 || s.orphanedFramePages[0] != page {
		t.Fatalf("orphaned frame pages = %v, want one retained handle", s.orphanedFramePages)
	}
}

func TestRestoreFrameRouteRetriesFailedDetach(t *testing.T) {
	var detachCalls atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req map[string]any
			if json.Unmarshal(msg, &req) != nil {
				continue
			}
			method, _ := req["method"].(string)
			id := req["id"]
			var response map[string]any
			switch method {
			case "Target.getTargets":
				response = map[string]any{"id": id, "result": map[string]any{"targetInfos": []any{map[string]any{"targetId": "parent-target", "type": "page"}}}}
			case "Target.attachToTarget":
				params, _ := req["params"].(map[string]any)
				targetID, _ := params["targetId"].(string)
				sessionID := "parent-session"
				if targetID == "child-target" {
					sessionID = "child-session"
				}
				response = map[string]any{"id": id, "result": map[string]any{"sessionId": sessionID}}
			case "Target.detachFromTarget":
				if detachCalls.Add(1) == 1 {
					response = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "temporary detach failure"}}
				} else {
					response = map[string]any{"id": id, "result": map[string]any{}}
				}
			default:
				response = map[string]any{"id": id, "result": map[string]any{}}
			}
			if err := ws.WriteJSON(response); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	s, err := NewSessionContext(context.Background(), SessionConfig{RemoteCDP: wsURL, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	if err := s.ensurePageContext(context.Background()); err != nil {
		t.Fatalf("ensure parent page: %v", err)
	}
	parent := s.page
	child, err := parent.AttachFrameTargetContext(context.Background(), "child-target")
	if err != nil {
		t.Fatalf("attach child page: %v", err)
	}
	s.mainPage = parent
	s.frameRoute = child
	s.frameRouteStack = []frameRouteLevel{{parent: parent, child: child}}
	s.page = child

	err = s.restoreFrameRouteLocked(context.Background())
	if err == nil || !strings.Contains(err.Error(), "temporary detach failure") {
		t.Fatalf("first route restoration error = %v, want detach failure", err)
	}
	if s.page != parent || len(s.frameRouteStack) != 0 || s.frameRoute != nil || len(s.orphanedFramePages) != 1 {
		t.Fatalf("failed restoration state: page=%p stack=%d route=%p orphans=%d", s.page, len(s.frameRouteStack), s.frameRoute, len(s.orphanedFramePages))
	}
	if err := s.restoreFrameRouteLocked(context.Background()); err != nil {
		t.Fatalf("retry route restoration: %v", err)
	}
	if len(s.orphanedFramePages) != 0 || detachCalls.Load() != 2 {
		t.Fatalf("retry state: orphans=%d detach calls=%d, want 0 and 2", len(s.orphanedFramePages), detachCalls.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

type browserTargetInventory struct {
	browse.Browser
	targets []browse.BrowserTarget
	err     error
}

func (b browserTargetInventory) BrowserTargetsContext(context.Context) ([]browse.BrowserTarget, error) {
	return b.targets, b.err
}

func TestSiteIsolatedFrameTargetPreservesInventoryFailures(t *testing.T) {
	inventoryErr := errors.New("target inventory unavailable")
	s := &Session{browser: browserTargetInventory{err: inventoryErr}}
	if _, _, err := s.siteIsolatedFrameTarget(context.Background(), "parent-frame", "https://child.example/"); !errors.Is(err, inventoryErr) {
		t.Fatalf("inventory error = %v, want wrapped %v", err, inventoryErr)
	}

	targets := []browse.BrowserTarget{
		{TargetID: "other-parent", Type: "iframe", ParentFrameID: "elsewhere", URL: "https://child.example/"},
		{TargetID: "matching", Type: "iframe", ParentFrameID: "parent-frame", URL: "https://child.example/"},
		{TargetID: "page", Type: "page", ParentFrameID: "parent-frame", URL: "https://child.example/"},
	}
	s.browser = browserTargetInventory{targets: targets}
	match, count, err := s.siteIsolatedFrameTarget(context.Background(), "parent-frame", "https://child.example/")
	if err != nil || count != 1 || match.TargetID != "matching" {
		t.Fatalf("target match = (%+v, %d, %v), want unique matching iframe", match, count, err)
	}
}

func (b targetPresenceBrowser) HasTargetContext(ctx context.Context, _ string) (bool, error) {
	if b.ctxDone {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	return b.present, b.ctxErr
}

func TestActiveTargetGoneProbeOutlivesExpiredActionContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Session{browser: targetPresenceBrowser{present: false, ctxDone: true}, page: &browse.Page{}}
	if !s.activeTargetGoneAfterFailure(ctx) {
		t.Fatal("target absence was not confirmed with an expired action context")
	}
}

func TestClickReportsTargetGoneBeforePageValidation(t *testing.T) {
	s := &Session{browser: targetPresenceBrowser{present: false}, page: &browse.Page{}}
	_, err := s.ClickWithPostconditionContext(context.Background(), "#submit", Postcondition{}, false)
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Cause != "target_gone" {
		t.Fatalf("click error = %T (%v), want structured target_gone", err, err)
	}
}

func (b targetPresenceBrowser) BrowserInstanceID() string { return b.instanceID }

func TestPortableRecoveryRejectsDifferentOrUnknownBrowserInstance(t *testing.T) {
	for _, tc := range []struct {
		name      string
		currentID string
		recordID  string
	}{
		{name: "different browser", currentID: "browser-b", recordID: "browser-a"},
		{name: "unknown current browser", currentID: "", recordID: "browser-a"},
		{name: "missing record identity", currentID: "browser-a", recordID: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{browser: targetPresenceBrowser{instanceID: tc.currentID}}
			recordID := tc.recordID
			if recordID == "" {
				recordID = "browser-a"
			}
			record, err := NewActionRecoveryRecord(&ActionOutcomeError{
				RunID: "old-run", Action: "close_tab", TargetID: "closed-target", BrowserInstanceID: recordID,
				Outcome: OutcomeUnknown, OperationOrdinal: 1,
				ExpectedPostcondition: &Postcondition{TargetAbsent: "closed-target"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.recordID == "" {
				record.ActionOutcome.BrowserInstanceID = ""
			}
			if _, err := s.ReconcileActionRecoveryRecordContext(context.Background(), record); err == nil {
				t.Fatalf("portable recovery was not rejected: %v", err)
			}
		})
	}
}

func TestReconcileCloseTabUsesTargetPresenceInsteadOfActivePage(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(fmt.Sprintf("present_%t", present), func(t *testing.T) {
			s := &Session{runID: "run-close", browser: targetPresenceBrowser{present: present}}
			outcome := &ActionOutcomeError{
				RunID: "run-close", Action: "close_tab", Target: "temporary", TargetID: "closed-target", Outcome: OutcomeUnknown,
				ExpectedPostcondition: &Postcondition{TargetAbsent: "closed-target"},
			}
			result, err := s.ReconcileActionOutcomeContext(context.Background(), outcome)
			if err != nil {
				t.Fatalf("reconcile close_tab: %v", err)
			}
			if result.TargetID != "closed-target" || result.Verified == present {
				t.Fatalf("reconciliation result = %+v, present=%t", result, present)
			}
			want := string(OutcomePostconditionVerified)
			if present {
				want = string(OutcomeUnknown)
			}
			if result.Outcome != want {
				t.Fatalf("outcome = %q, want %q", result.Outcome, want)
			}
		})
	}
}

func TestJsonQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", `"hello"`},
		{"", `""`},
		{`with "quotes"`, `"with \"quotes\""`},
		{"with\nnewline", `"with\nnewline"`},
		{"with\ttab", `"with\ttab"`},
		{`back\slash`, `"back\\slash"`},
		{"unicode: 日本語", `"unicode: 日本語"`},
		{`<script>alert('xss')</script>`, `"\u003cscript\u003ealert('xss')\u003c/script\u003e"`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := jsonQuote(tt.input)
			if got != tt.want {
				t.Errorf("jsonQuote(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultContentOptions_Values(t *testing.T) {
	opts := DefaultContentOptions()
	if opts.MaxLength != 4000 {
		t.Errorf("MaxLength: got %d", opts.MaxLength)
	}
	if opts.MaxLinks != 25 {
		t.Errorf("MaxLinks: got %d", opts.MaxLinks)
	}
	if opts.MaxInputs != 20 {
		t.Errorf("MaxInputs: got %d", opts.MaxInputs)
	}
	if opts.MaxButtons != 15 {
		t.Errorf("MaxButtons: got %d", opts.MaxButtons)
	}
	if opts.MaxItems != 50 {
		t.Errorf("MaxItems: got %d", opts.MaxItems)
	}
	if opts.MaxRows != 100 {
		t.Errorf("MaxRows: got %d", opts.MaxRows)
	}
	if opts.MaxScreenshotBytes != 5*1024*1024 {
		t.Errorf("MaxScreenshotBytes: got %d", opts.MaxScreenshotBytes)
	}
}

func TestSetContentOptions(t *testing.T) {
	s := newTestSession()
	custom := ContentOptions{
		MaxLength:  1000,
		MaxLinks:   5,
		MaxInputs:  3,
		MaxButtons: 2,
		MaxItems:   10,
		MaxRows:    20,
	}
	s.SetContentOptions(custom)
	if s.contentOpts.MaxLength != 1000 {
		t.Errorf("MaxLength: got %d", s.contentOpts.MaxLength)
	}
	if s.contentOpts.MaxLinks != 5 {
		t.Errorf("MaxLinks: got %d", s.contentOpts.MaxLinks)
	}
}

func TestTraceBeforeAction_NotTracing(t *testing.T) {
	s := newTestSession()
	start, before := s.traceBeforeAction()
	if !start.IsZero() {
		t.Error("start should be zero when not tracing")
	}
	if before.data != nil || before.status != "" {
		t.Error("before capture should be empty when not tracing")
	}
}

func TestTraceAfterAction_NotTracing(t *testing.T) {
	s := newTestSession()
	s.traceAfterActionContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "#btn", "", "", nil)
	if s.trace != nil {
		t.Error("trace should remain nil when not tracing")
	}
}

func TestTraceAfterAction_Tracing(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start := time.Now()
	s.traceAfterActionContext(context.Background(), start, traceScreenshotReference{}, "navigate", "", "", "https://example.com", nil)

	if len(s.trace.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(s.trace.events))
	}
	ev := s.trace.events[0]
	if ev.Action != "navigate" {
		t.Errorf("action: got %q", ev.Action)
	}
	if ev.URL != "https://example.com" {
		t.Errorf("url: got %q", ev.URL)
	}
	if ev.Error != "" {
		t.Errorf("error should be empty, got %q", ev.Error)
	}
}

func TestTraceAfterAction_WithError(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start := time.Now()
	s.traceAfterActionContext(context.Background(), start, traceScreenshotReference{}, "click", "#btn", "", "", fmt.Errorf("element not found"))

	if len(s.trace.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(s.trace.events))
	}
	if s.trace.events[0].Error != "element not found" {
		t.Errorf("error: got %q", s.trace.events[0].Error)
	}
}

func TestTraceAfterAction_UnknownDispatchOutcome(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}
	err := &cdpclient.DispatchOutcomeUnknownError{Method: "Input.dispatchMouseEvent", Err: fmt.Errorf("connection closed")}
	s.traceAfterActionContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "#submit", "", "", err)

	if len(s.trace.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(s.trace.events))
	}
	event := s.trace.events[0]
	if event.Outcome != string(OutcomeUnknown) || event.VerificationKnown || event.Verified {
		t.Fatalf("unexpected trace outcome: %+v", event)
	}
	if event.Evidence == "" || event.RecoveryHint == "" {
		t.Fatalf("trace omitted ambiguity evidence or recovery hint: %+v", event)
	}
}

func TestTraceAfterAction_UnknownActionRetainsRecoveryIdentity(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}
	err := &ActionOutcomeError{
		Action: "click", Target: "#submit", TargetID: "target-7",
		URL:     "https://example.test/checkout?token=secret#confirm",
		Outcome: OutcomeUnknown, Evidence: "click may have been dispatched",
		RecoveryHint: "inspect before retrying",
	}

	s.traceAfterActionContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "#submit", "", "", err)
	if len(s.trace.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(s.trace.events))
	}
	event := s.trace.events[0]
	if event.TargetID != "target-7" || event.URL != "https://example.test/checkout" || event.Outcome != string(OutcomeUnknown) {
		t.Fatalf("unknown action context not retained or URL not redacted: %+v", event)
	}
	if event.Evidence != err.Evidence || event.RecoveryHint != err.RecoveryHint {
		t.Fatalf("recovery evidence not retained: %+v", event)
	}
}

func TestTraceAfterAction_UnknownNavigationHasRecoveryRecord(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}
	err := &browse.NavigationOutcomeUnknownError{
		URL: "https://example.test/next?token=secret#confirm",
		Err: fmt.Errorf("connection closed"),
	}

	s.traceAfterActionContext(context.Background(), time.Now(), traceScreenshotReference{}, "navigate", "", "", err.URL, err)
	if len(s.trace.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(s.trace.events))
	}
	event := s.trace.events[0]
	if event.Outcome != string(OutcomeUnknown) || event.URL != "https://example.test/next" || event.Evidence == "" || event.RecoveryHint == "" {
		t.Fatalf("navigation recovery evidence incomplete or URL not redacted: %+v", event)
	}
}

func TestActionOutcomeAfterDispatchIncludesRecoveryLocation(t *testing.T) {
	s := newTestSession()
	s.frameID = "frame-3"
	err := s.actionOutcomeAfterDispatch("click", "#save", "click may have dispatched", fmt.Errorf("connection lost"))
	var outcomeErr *ActionOutcomeError
	if !errors.As(err, &outcomeErr) {
		t.Fatalf("error type = %T, want ActionOutcomeError", err)
	}
	if outcomeErr.FrameID != "frame-3" {
		t.Fatalf("recovery target context missing: %+v", outcomeErr)
	}
}

func TestApplyTraceResult_AttachesEvidence(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{events: []TraceEvent{{Action: "click", Selector: "#submit", Outcome: string(OutcomeEffectUnverified)}}}

	s.applyTraceResult("click", "#submit", "target-7", string(OutcomePostconditionVerified), "success message visible", "", true)

	event := s.trace.events[0]
	if event.TargetID != "target-7" || event.Outcome != string(OutcomePostconditionVerified) || !event.Verified || !event.VerificationKnown || event.Evidence != "success message visible" {
		t.Fatalf("trace event did not retain action evidence: %+v", event)
	}
}

func TestTraceActionFailureAddsTraceCorrelation(t *testing.T) {
	s := &Session{tracing: true, trace: &traceState{run: TraceRunMetadata{TraceID: "trace-abc"}, actionOrdinal: 7, events: []TraceEvent{{Index: 6, ActionOrdinal: 7}}}}
	err := &ActionOutcomeError{Action: "click", Target: "#submit", Outcome: OutcomeUnknown}
	got := s.traceActionFailureContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "#submit", "", "", err)
	if got != err {
		t.Fatalf("returned error = %v, want original error", got)
	}
	if err.TraceID != "trace-abc" || err.TraceStep != 2 || err.TraceActionOrdinal != 8 || err.OperationOrdinal != 1 {
		t.Fatalf("trace correlation = %q/%d trace ordinal %d operation ordinal %d, want trace-abc/2/8/1", err.TraceID, err.TraceStep, err.TraceActionOrdinal, err.OperationOrdinal)
	}
}

func TestTraceActionFailureCarriesRecoveryContextWithoutTracing(t *testing.T) {
	s := newTestSession()
	s.frameID = "frame-9"
	err := &ActionOutcomeError{Action: "click", Target: "#submit", Outcome: OutcomeUnknown, Evidence: "click may have reached Chrome"}
	got := s.traceActionFailureContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "#submit", "", "", err)
	if got != err {
		t.Fatalf("returned error = %v, want original error", got)
	}
	if err.FrameID != "frame-9" || err.RecoveryHint == "" {
		t.Fatalf("recovery context missing without tracing: %+v", err)
	}
	if err.TraceID != "" || err.TraceStep != 0 {
		t.Fatalf("trace correlation should be absent when tracing is disabled: %+v", err)
	}
}

func TestRedactTraceURLRemovesCredentialsQueryAndFragment(t *testing.T) {
	got := redactTraceURL("https://user:pass@example.com/path?token=secret#private")
	want := "https://example.com/path"
	if got != want {
		t.Fatalf("redacted URL = %q, want %q", got, want)
	}
}

func TestTraceAfterAction_WithScreenshots(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start := time.Now()
	before := traceScreenshotReference{data: []byte("before-screenshot-data"), status: "captured"}
	s.traceAfterAction(start, before, "#btn", nil)

	if len(s.trace.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(s.trace.events))
	}
	if s.trace.events[0].BeforeImg != "before-screenshot-data" {
		t.Errorf("BeforeImg: got %q", s.trace.events[0].BeforeImg)
	}
}

func TestTraceBeforeAction_Tracing(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start, _ := s.traceBeforeAction()
	if start.IsZero() {
		t.Error("start should not be zero when tracing")
	}
}

func TestCaptureTraceScreenshot_NilPage(t *testing.T) {
	s := newTestSession()
	result := s.captureTraceScreenshot()
	if result != nil {
		t.Error("expected nil for nil page")
	}
}

func TestEnsurePage_ClosedSession(t *testing.T) {
	s := newTestSession()
	s.closed = true
	err := s.ensurePage()
	if err == nil {
		t.Error("expected error for closed session")
	}
	if err.Error() != "session is closed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSessionConfig_Defaults(t *testing.T) {
	cfg := SessionConfig{}
	if cfg.Timeout != 0 {
		t.Errorf("default Timeout should be 0 (set in constructor)")
	}
	if cfg.Headless {
		t.Error("default Headless should be false")
	}
}

func TestNewSessionFromBrowser_DefaultTimeout(t *testing.T) {
	cfg := SessionConfig{}
	s := NewSessionFromBrowser(nil, cfg)
	if s.timeout != 30*time.Second {
		t.Errorf("default timeout: got %v, want 30s", s.timeout)
	}
}

func TestNewSessionFromBrowser_CustomTimeout(t *testing.T) {
	cfg := SessionConfig{Timeout: 10 * time.Second}
	s := NewSessionFromBrowser(nil, cfg)
	if s.timeout != 10*time.Second {
		t.Errorf("timeout: got %v, want 10s", s.timeout)
	}
}

func TestSessionConfig_ProxyServerIsRetainedForReset(t *testing.T) {
	cfg := SessionConfig{ProxyServer: "http://proxy.example:8080"}
	s := NewSessionFromBrowser(nil, cfg)
	if s.proxyServer != cfg.ProxyServer {
		t.Fatalf("proxyServer: got %q, want %q", s.proxyServer, cfg.ProxyServer)
	}
}

func TestSessionStatusReportsProxyApplicationScope(t *testing.T) {
	local := NewSessionFromBrowser(nil, SessionConfig{ProxyServer: "http://proxy.example:8080"})
	local.browserOwnership = "scout_launched"
	local.proxyAppliedByScout = true
	localStatus, err := local.StatusContext(context.Background())
	if err != nil {
		t.Fatalf("local status: %v", err)
	}
	if !localStatus.ProxyConfigured || !localStatus.ProxyAppliedByScout || localStatus.RemoteHostPolicy != "not_applicable" {
		t.Fatalf("local proxy status = %+v, want configured, applied by Scout, and no remote-host policy", localStatus)
	}

	remote := NewSessionFromBrowser(nil, SessionConfig{RemoteCDP: "https://browser.example/cdp", ProxyServer: "http://remote-proxy.example:8080"})
	remote.browserOwnership = "remote_cdp"
	remote.remoteBrowserURL = "https://browser.example/cdp"
	remote.proxyAppliedByScout = false
	remoteStatus, err := remote.StatusContext(context.Background())
	if err != nil {
		t.Fatalf("remote status: %v", err)
	}
	if !remoteStatus.ProxyConfigured || remoteStatus.ProxyAppliedByScout || remoteStatus.RemoteHostPolicy != "unverified" {
		t.Fatalf("remote proxy status = %+v, want configured but not applied by Scout and remote policy unverified", remoteStatus)
	}
}

func TestNewSessionFromBrowser_ContentOpts(t *testing.T) {
	cfg := SessionConfig{}
	s := NewSessionFromBrowser(nil, cfg)
	if s.contentOpts.MaxLength != 4000 {
		t.Errorf("MaxLength: got %d, want 4000", s.contentOpts.MaxLength)
	}
}

func TestNewSessionFromBrowser_Stealth(t *testing.T) {
	cfg := SessionConfig{Stealth: true}
	s := NewSessionFromBrowser(nil, cfg)
	if !s.stealth {
		t.Error("stealth should be true")
	}
}

func TestTraceAfterAction_NilTrace(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = nil
	s.traceAfterActionContext(context.Background(), time.Now(), traceScreenshotReference{}, "click", "", "", "", nil)
}

func TestTraceAfterAction_MultipleEvents(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	for i := 0; i < 5; i++ {
		start := time.Now()
		s.traceAfterActionContext(context.Background(), start, traceScreenshotReference{}, "click", "#btn", "", "", nil)
	}

	if len(s.trace.events) != 5 {
		t.Errorf("expected 5 events, got %d", len(s.trace.events))
	}
	for i, ev := range s.trace.events {
		if ev.Index != i {
			t.Errorf("event[%d].Index = %d", i, ev.Index)
		}
	}
}

func TestStatus_BasicFields(t *testing.T) {
	s := newTestSession()
	s.lastError = "boom"
	s.consecutiveTimeouts = 2
	s.inflightCommands = 1
	st := s.Status()
	if st == nil {
		t.Fatal("expected status")
	}
	if !st.SessionAlive {
		t.Fatal("expected session_alive=true")
	}
	if st.BrowserAlive {
		t.Fatal("expected browser_alive=false for test session without browser")
	}
	if st.LastError != "boom" {
		t.Fatalf("last_error: got %q", st.LastError)
	}
	if st.ConsecutiveTimeouts != 2 {
		t.Fatalf("consecutive_timeouts: got %d", st.ConsecutiveTimeouts)
	}
	if st.InFlightCommands != 1 {
		t.Fatalf("inflight_command_count: got %d", st.InFlightCommands)
	}
}

func TestStatus_ReportsActiveTargetIdentity(t *testing.T) {
	if tab, target := activePageIdentity(nil, nil); tab != "" || target != "" {
		t.Fatalf("missing page identity = (%q, %q), want empty", tab, target)
	}
	if tab := activeTabName(&tabManager{active: "checkout"}); tab != "checkout" {
		t.Fatalf("named tab = %q, want checkout", tab)
	}
	if tab := activeTabName(nil); tab != "default" {
		t.Fatalf("single-page tab = %q, want default", tab)
	}
}

func TestCheckExpectedFrameIDRejectsDrift(t *testing.T) {
	s := &Session{frameID: "frame-b"}
	ctx := context.WithValue(context.Background(), expectedFrameIDKey{}, "frame-a")
	err := s.checkExpectedFrameIDContext(ctx)
	op, ok := err.(*OperationError)
	if !ok {
		t.Fatalf("error = %T (%v), want *OperationError", err, err)
	}
	if op.Phase != "task_binding" || op.Cause != "frame_changed" {
		t.Fatalf("frame drift error = %+v", op)
	}
}

func TestCheckExpectedFrameIDAllowsMatchingAndUnboundContexts(t *testing.T) {
	s := &Session{frameID: "frame-a"}
	if err := s.checkExpectedFrameIDContext(context.WithValue(context.Background(), expectedFrameIDKey{}, "frame-a")); err != nil {
		t.Fatalf("matching frame rejected: %v", err)
	}
	if err := s.checkExpectedFrameIDContext(context.Background()); err != nil {
		t.Fatalf("unbound context rejected: %v", err)
	}
}

func TestIsTargetGoneErrorRecognizesAlreadyDetachedCDPSession(t *testing.T) {
	if !isTargetGoneError(errors.New("cdp: error -32602: No session with given id")) {
		t.Fatal("already-detached CDP session was not classified as gone")
	}
	if isTargetGoneError(errors.New("cdp: error -32602: invalid session parameters")) {
		t.Fatal("unrelated CDP error was classified as a gone target")
	}
}

func TestExtractionPageStateSeparatesPaginationFromItemTruncation(t *testing.T) {
	tests := []struct {
		name                     string
		offset, count, total     int
		itemsTruncated           bool
		wantHasMore, wantClipped bool
		wantNextOffset           int
		wantTruncated            bool
	}{
		{name: "more results", offset: 0, count: 8, total: 12, wantHasMore: true, wantNextOffset: 8, wantTruncated: true},
		{name: "last page with clipped item", offset: 8, count: 4, total: 12, itemsTruncated: true, wantClipped: true, wantNextOffset: 12, wantTruncated: true},
		{name: "complete last page", offset: 8, count: 4, total: 12, wantNextOffset: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasMore, nextOffset, itemsTruncated, truncated := extractionPageState(tt.offset, tt.count, tt.total, tt.itemsTruncated)
			if hasMore != tt.wantHasMore || nextOffset != tt.wantNextOffset || itemsTruncated != tt.wantClipped || truncated != tt.wantTruncated {
				t.Fatalf("state = (%t, %d, %t, %t), want (%t, %d, %t, %t)", hasMore, nextOffset, itemsTruncated, truncated, tt.wantHasMore, tt.wantNextOffset, tt.wantClipped, tt.wantTruncated)
			}
		})
	}
}

func TestExtractAllResultPublishesExplicitPaginationMetadata(t *testing.T) {
	result := ExtractAllResult{HasMore: true, NextOffset: 8, ItemsTruncated: true, Truncated: true}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got["has_more"] != true || got["next_offset"] != float64(8) || got["items_truncated"] != true || got["truncated"] != true {
		t.Fatalf("pagination metadata = %v", got)
	}
}

func TestWrapDetailedError_Timeout(t *testing.T) {
	s := newTestSession()
	err := s.wrapDetailedError("navigate", fmt.Errorf("context deadline exceeded while loading"))
	op, ok := err.(*OperationError)
	if !ok {
		t.Fatalf("expected *OperationError, got %T", err)
	}
	if op.Cause != "timeout" {
		t.Fatalf("cause: got %q", op.Cause)
	}
	if op.Phase != "navigate" {
		t.Fatalf("phase: got %q", op.Phase)
	}
}

func TestWrapDetailedError_NetOp(t *testing.T) {
	s := newTestSession()
	netErr := &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}
	err := s.wrapDetailedError("navigate", netErr)
	op, ok := err.(*OperationError)
	if !ok {
		t.Fatalf("expected *OperationError, got %T", err)
	}
	if op.Cause != "connection_refused" {
		t.Fatalf("cause: got %q", op.Cause)
	}
	if op.Detail != "dial" {
		t.Fatalf("detail: got %q", op.Detail)
	}
}
