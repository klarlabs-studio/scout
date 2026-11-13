//go:build integration

package agent_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
	cdpclient "go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

func TestIntegrationUploadFileResult(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><input id="file" type="file">`)
	}))
	defer srv.Close()
	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := s.UploadFileResult("#file", path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "value_verified" || result.Evidence == "" || !result.Verified || !strings.Contains(result.Evidence, "site-side upload acceptance was not asserted") {
		t.Fatalf("unexpected upload outcome: %+v", result)
	}
}

func TestIntegrationSubmitFormOutcomes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><form id="form" action="/submit" method="post"><input name="q" value="ok"><button>Send</button></form>`)
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `<!doctype html><p>Rejected</p>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	result, err := s.SubmitForm("#form", "/submit", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "response_failed" || result.Verified || result.ApplicationAccepted || !result.ResponseObserved || result.StatusCode != http.StatusBadRequest || result.RecoveryHint == "" {
		t.Fatalf("expected observed HTTP failure: %+v", result)
	}
}

func TestIntegrationSubmitFormRequiresAcceptancePostcondition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `received`)
			return
		}
		fmt.Fprint(w, `<!doctype html><form id="form" method="post" onsubmit="event.preventDefault();fetch('/submit',{method:'POST'}).then(()=>{document.querySelector('#notice').textContent='Request received'})"><input name="q" value="ok"><button>Send</button></form><p id="notice"></p>`)
	}))
	defer srv.Close()
	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	observed, err := s.SubmitForm("#form", "", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Verified || observed.ApplicationAccepted || !observed.ResponseObserved || observed.Outcome != "response_observed" || observed.StatusCode != http.StatusOK {
		t.Fatalf("HTTP response must not imply application acceptance: %+v", observed)
	}
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	verified, err := s.SubmitFormWithAcceptance("#form", "", 3000, agent.Postcondition{Text: "Request received"})
	if err != nil {
		t.Fatal(err)
	}
	if !verified.Verified || !verified.ApplicationAccepted || !verified.ResponseObserved || verified.Outcome != "postcondition_verified" || !strings.Contains(verified.Evidence, "Request received") {
		t.Fatalf("expected explicit acceptance verification: %+v", verified)
	}
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	rejectedUI, err := s.SubmitFormWithPostcondition("#form", "", 3000, agent.Postcondition{Text: "Payment complete"})
	if err != nil {
		t.Fatal(err)
	}
	if rejectedUI.Outcome != "postcondition_failed" || !rejectedUI.ResponseObserved || rejectedUI.ApplicationAccepted || rejectedUI.RecoveryHint == "" {
		t.Fatalf("expected observed response with failed acceptance condition: %+v", rejectedUI)
	}
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	navigationOnly, err := s.SubmitFormWithPostcondition("#form", "", 3000, agent.Postcondition{URLContains: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if !navigationOnly.Verified || navigationOnly.ApplicationAccepted || !navigationOnly.ResponseObserved {
		t.Fatalf("generic page predicate must not imply application acceptance: %+v", navigationOnly)
	}
	if _, err := s.SubmitFormWithAcceptance("#form", "", 3000, agent.Postcondition{URLContains: "/"}); err == nil {
		t.Fatal("URL-only acceptance condition should be rejected")
	}
}

func TestIntegrationPlaybookTextExpectation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Playbook</title><h1 id="ready">Ready</h1>`)
	}))
	defer srv.Close()
	s := integrationSession(t)
	result, err := s.ReplayPlaybook(&agent.Playbook{Name: "expect-text", URL: srv.URL, Actions: []agent.Action{{Type: "wait", Selector: "#ready", Expected: &agent.ActionExpect{Text: "Saved"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.FailurePhase != "expectation" || result.FailedAt != 1 || result.Evidence == "" || result.RecoveryHint == "" {
		t.Fatalf("text expectation was not enforced with evidence: %+v", result)
	}
}

// ---------------------------------------------------------------------------
// Tabs: OpenTab, SwitchTab, ListTabs, CloseTab
// ---------------------------------------------------------------------------

func TestIntegrationTabsLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Page A</title></head><body><p>Content A</p></body></html>`)
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Page B</title></head><body><p>Content B</p></body></html>`)
	}))
	defer srvB.Close()

	s := integrationSession(t)

	// Navigate to the first page (becomes "default" tab internally).
	if _, err := s.Navigate(srvA.URL); err != nil {
		t.Fatalf("Navigate A: %v", err)
	}

	// ListTabs before any OpenTab — should return a single default entry.
	tabs, err := s.ListTabs()
	if err != nil {
		t.Fatalf("ListTabs initial: %v", err)
	}
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(tabs))
	}
	if tabs[0].Title != "Page A" {
		t.Errorf("expected title 'Page A', got %q", tabs[0].Title)
	}
	if err := s.StartTrace(); err != nil {
		t.Fatalf("StartTrace: %v", err)
	}

	// Open a second tab.
	result, err := s.OpenTab("second", srvB.URL)
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if result.Title != "Page B" {
		t.Errorf("expected title 'Page B', got %q", result.Title)
	}
	if result.Action != "opened_tab" || result.Target != "second" || result.TargetID == "" || result.Outcome != string(agent.OutcomePostconditionVerified) || !result.Verified {
		t.Errorf("open tab result missing active-target evidence: %+v", result)
	}
	openedTargetID := result.TargetID

	// ListTabs — should now have 2 tabs, "second" being active.
	tabs, err = s.ListTabs()
	if err != nil {
		t.Fatalf("ListTabs after open: %v", err)
	}
	if len(tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(tabs))
	}
	activeCount := 0
	for _, ti := range tabs {
		if ti.Active {
			activeCount++
			if ti.Name != "second" {
				t.Errorf("expected active tab 'second', got %q", ti.Name)
			}
		}
	}
	if activeCount != 1 {
		t.Errorf("expected exactly 1 active tab, got %d", activeCount)
	}

	// SwitchTab back to default.
	result, err = s.SwitchTab("default")
	if err != nil {
		t.Fatalf("SwitchTab: %v", err)
	}
	if result.Title != "Page A" {
		t.Errorf("expected title 'Page A' after switch, got %q", result.Title)
	}
	if result.Action != "switched_tab" || result.Target != "default" || result.TargetID == "" || result.Outcome != string(agent.OutcomePostconditionVerified) || !result.Verified {
		t.Errorf("switch tab result missing active-target evidence: %+v", result)
	}

	// Cannot close the active tab.
	if err := s.CloseTab("default"); err == nil {
		t.Error("expected error when closing active tab")
	}

	// Close the non-active tab and retain evidence about the removed target.
	closed, err := s.CloseTabResult("second")
	if err != nil {
		t.Fatalf("CloseTabResult: %v", err)
	}
	if closed.Action != "closed_tab" || closed.Target != "second" || closed.TargetID == "" || closed.Outcome != string(agent.OutcomePostconditionVerified) || !closed.Verified {
		t.Errorf("close tab result missing closure evidence: %+v", closed)
	}
	unknownClose := &agent.ActionOutcomeError{
		RunID: "portable-close-recovery", OperationOrdinal: closed.OperationOrdinal,
		Action: "close_tab", Target: "second", TargetID: closed.TargetID,
		BrowserInstanceID:     closed.Receipt.BrowserInstanceID,
		Outcome:               agent.OutcomeUnknown,
		ExpectedPostcondition: &agent.Postcondition{TargetAbsent: closed.TargetID},
	}
	recovery, err := agent.NewActionRecoveryRecord(unknownClose)
	if err != nil {
		t.Fatalf("create close recovery record: %v", err)
	}
	recoveryJSON, err := json.Marshal(recovery)
	if err != nil {
		t.Fatalf("encode close recovery record: %v", err)
	}
	recovery, err = agent.DecodeActionRecoveryRecord(recoveryJSON)
	if err != nil {
		t.Fatalf("decode close recovery record: %v", err)
	}
	reconciled, err := s.ReconcileActionRecoveryRecordContext(t.Context(), recovery)
	if err != nil {
		t.Fatalf("reconcile closed tab from portable record: %v", err)
	}
	if reconciled.Outcome != string(agent.OutcomePostconditionVerified) || !reconciled.Verified || reconciled.TargetID != closed.TargetID {
		t.Fatalf("close recovery reconciliation = %+v, want verified absence of original target", reconciled)
	}

	// ListTabs — should have 1 tab remaining.
	tabs, err = s.ListTabs()
	if err != nil {
		t.Fatalf("ListTabs after close: %v", err)
	}
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab after close, got %d", len(tabs))
	}
	tracePath := filepath.Join(t.TempDir(), "tabs-trace.zip")
	if _, err := s.StopTrace(tracePath); err != nil {
		t.Fatalf("StopTrace: %v", err)
	}
	archive, err := zip.OpenReader(tracePath)
	if err != nil {
		t.Fatalf("open trace archive: %v", err)
	}
	defer archive.Close()
	var events []agent.TraceEvent
	for _, file := range archive.File {
		if file.Name != "trace.json" {
			continue
		}
		reader, openErr := file.Open()
		if openErr != nil {
			t.Fatalf("open trace events: %v", openErr)
		}
		decodeErr := json.NewDecoder(reader).Decode(&events)
		_ = reader.Close()
		if decodeErr != nil {
			t.Fatalf("decode trace events: %v", decodeErr)
		}
	}
	for _, expected := range []struct {
		action, tab, targetID string
	}{
		{"open_tab", "second", openedTargetID},
		{"switch_tab", "default", result.TargetID},
		{"close_tab", "second", closed.TargetID},
	} {
		found := false
		for _, event := range events {
			if event.Action == expected.action && event.Selector == expected.tab && event.TargetID == expected.targetID && event.Outcome == string(agent.OutcomePostconditionVerified) && event.Verified && event.Evidence != "" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("trace missing verified %s evidence for tab %q target %s: %+v", expected.action, expected.tab, expected.targetID, events)
		}
	}
}

func TestIntegrationLongRunExternalTabActivationPreservesTargetBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{RemoteCDP: browser.WSEndpoint(), AllowPrivateIPs: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		marker := strings.TrimPrefix(r.URL.Path, "/run/")
		if marker == r.URL.Path {
			marker = "human"
		}
		fmt.Fprintf(w, `<!doctype html><html><body data-run=%q><span id="marker">%s</span></body></html>`, marker, marker)
	}))
	defer server.Close()
	initial, err := session.NavigateWithPostconditionContext(ctx, server.URL+"/run/initial", agent.Postcondition{URLContains: "/run/initial"})
	if err != nil {
		t.Fatal(err)
	}
	boundTarget := initial.TargetID
	if boundTarget == "" {
		t.Fatal("initial navigation returned no target identity")
	}
	connection, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	externalTarget, err := connection.CreateTargetCtx(ctx, server.URL+"/human")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.CloseTargetCtx(context.Background(), externalTarget) }()

	// 28 navigation + viewport cycles match the scale of the upstream long-run
	// report. Each cycle also changes browser focus through an independent CDP
	// connection and independently reads the session's target, URL, marker, and
	// viewport before accepting success.
	for i := 0; i < 28; i++ {
		wantURL := fmt.Sprintf("%s/run/%d", server.URL, i)
		navigation, err := session.NavigateWithPostconditionContext(ctx, wantURL, agent.Postcondition{URLContains: wantURL})
		if err != nil {
			t.Fatalf("cycle %d navigation: %v", i, err)
		}
		if navigation.TargetID != boundTarget || navigation.URL != wantURL {
			t.Fatalf("cycle %d navigation identity/result = target %q URL %q, want target %q URL %q", i, navigation.TargetID, navigation.URL, boundTarget, wantURL)
		}
		width, height := 360+i, 700+i
		viewport, err := session.SetViewportWithOutcomeContext(ctx, width, height, 1, false)
		if err != nil {
			t.Fatalf("cycle %d set viewport: %v", i, err)
		}
		if viewport.TargetID != boundTarget || viewport.Width != width || viewport.Height != height || !viewport.Verified {
			t.Fatalf("cycle %d viewport result = %+v, want bound target and verified %dx%d", i, viewport, width, height)
		}
		if err := connection.ActivateTargetContext(ctx, externalTarget); err != nil {
			t.Fatalf("cycle %d external tab activation: %v", i, err)
		}
		observed, err := session.EvalContext(ctx, `({url:location.href,marker:document.body.dataset.run,width:window.innerWidth,height:window.innerHeight})`)
		if err != nil {
			t.Fatalf("cycle %d read bound page after external activation: %v", i, err)
		}
		state, ok := observed.(map[string]any)
		if !ok || state["url"] != wantURL || state["marker"] != fmt.Sprint(i) || state["width"] != float64(width) || state["height"] != float64(height) {
			t.Fatalf("cycle %d bound page state = %#v, want URL %q marker %q viewport %dx%d", i, observed, wantURL, fmt.Sprint(i), width, height)
		}
		if i == 13 {
			if err := connection.CloseTargetCtx(ctx, externalTarget); err != nil {
				t.Fatalf("close externally activated tab: %v", err)
			}
			afterClose, err := session.EvalContext(ctx, `location.href`)
			if err != nil {
				t.Fatalf("read bound page after external tab closure: %v", err)
			}
			if afterClose != wantURL || session.Page().TargetID() != boundTarget {
				t.Fatalf("after external tab closure, URL/target = %v/%q, want %q/%q", afterClose, session.Page().TargetID(), wantURL, boundTarget)
			}
			externalTarget, err = connection.CreateTargetCtx(ctx, server.URL+"/human")
			if err != nil {
				t.Fatalf("recreate external tab: %v", err)
			}
		}
	}
}

func TestIntegrationTabsOpenDuplicate(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Dup</title></head><body>ok</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if _, err := s.OpenTab("t1", srv.URL); err != nil {
		t.Fatalf("OpenTab t1: %v", err)
	}

	// Opening a tab with the same name should fail.
	if _, err := s.OpenTab("t1", srv.URL); err == nil {
		t.Error("expected error opening duplicate tab name")
	}
}

func TestIntegrationOpenTabExpectedTargetAllowsIntentionalTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Target transition</title><body>ready</body>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	first, err := s.Navigate(srv.URL)
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	ctx := agent.WithExpectedTargetID(context.Background(), first.TargetID)
	opened, err := s.OpenTabContext(ctx, "second", srv.URL)
	if err != nil {
		t.Fatalf("OpenTabContext with current source target: %v", err)
	}
	if opened.TargetID == "" || opened.TargetID == first.TargetID {
		t.Fatalf("expected a distinct destination target, source=%q result=%+v", first.TargetID, opened)
	}

	staleCtx := agent.WithExpectedTargetID(context.Background(), first.TargetID)
	if _, err := s.OpenTabContext(staleCtx, "third", srv.URL); err == nil || !strings.Contains(err.Error(), "target identity mismatch") {
		t.Fatalf("stale source target should be rejected, got %v", err)
	}
}

func TestIntegrationObserveWithBudgetRejectsStaleExpectedTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Budget target binding</title><main><p>ready</p></main>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	first, err := s.Navigate(srv.URL)
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := s.OpenTab("second", srv.URL); err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	ctx := agent.WithExpectedTargetID(context.Background(), first.TargetID)
	if _, err := s.ObserveWithBudgetContext(ctx, 100); err == nil || !strings.Contains(err.Error(), "target identity mismatch") {
		t.Fatalf("budgeted observation should reject stale target evidence, got %v", err)
	}
}

func TestIntegrationTabsSwitchNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/child" {
			fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Child</title></head><body><h1 id="child-title">Frame remains selected</h1></body></html>`)
			return
		}
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>X</title></head><body>x<iframe id="frame" src="/child"></iframe></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := s.OpenTab("a", srv.URL); err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if _, err := s.SwitchTab("default"); err != nil {
		t.Fatalf("SwitchTab default: %v", err)
	}
	if _, err := s.SwitchToFrame("#frame"); err != nil {
		t.Fatalf("SwitchToFrame: %v", err)
	}

	if _, err := s.SwitchTab("nonexistent"); err == nil {
		t.Error("expected error switching to nonexistent tab")
	}
	if !s.InFrame() {
		t.Fatal("failed tab switch cleared the selected frame")
	}
	observation, err := s.Observe()
	if err != nil {
		t.Fatalf("Observe after failed tab switch: %v", err)
	}
	if observation.FrameID == "" || observation.URL != srv.URL+"/child" {
		t.Fatalf("failed tab switch changed selected frame evidence: frame_id=%q url=%q", observation.FrameID, observation.URL)
	}
	value, err := s.Eval(`document.querySelector('#child-title').textContent`)
	if err != nil || value != "Frame remains selected" {
		t.Fatalf("failed tab switch changed active page/frame: value=%v err=%v", value, err)
	}
}

// ---------------------------------------------------------------------------
// SPA: DetectedFrameworks, GetAppState, WaitForSPA, DispatchEvent
// ---------------------------------------------------------------------------

func TestIntegrationDetectedFrameworksPlainPage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Plain</title></head>
		<body><h1>No Frameworks</h1><p>Just static HTML.</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	frameworks, err := s.DetectedFrameworks()
	if err != nil {
		t.Fatalf("DetectedFrameworks: %v", err)
	}
	if len(frameworks) != 0 {
		t.Errorf("expected no frameworks on plain page, got %v", frameworks)
	}
}

func TestIntegrationGetAppStatePlainPage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Plain</title></head>
		<body><p>No framework state.</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	state, err := s.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	// On a plain page with no framework data, state should be nil.
	if state != nil {
		t.Errorf("expected nil state on plain page, got %v", state)
	}
}

func TestIntegrationGetAppStateWithHydrationScript(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Hydration</title></head>
		<body>
			<script type="application/json" id="app-data">{"key":"value"}</script>
			<p>Content</p>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	state, err := s.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state with hydration script")
	}
	if _, ok := state["_hydrationScripts"]; !ok {
		t.Error("expected _hydrationScripts key in state")
	}
}

func TestIntegrationWaitForSPA(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	// Page with enough body text (>100 chars) to trigger the readiness condition.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>SPA</title></head>
		<body><div id="root"><p>`+strings.Repeat("Content here. ", 20)+`</p></div></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if err := s.WaitForSPA(); err != nil {
		t.Fatalf("WaitForSPA: %v", err)
	}
}

func TestIntegrationDispatchEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Events</title></head>
		<body>
			<div id="target">Waiting</div>
			<script>
				document.getElementById('target').addEventListener('my-event', function(e) {
					this.textContent = 'Received: ' + JSON.stringify(e.detail);
				});
			</script>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.DispatchEventWithPostcondition("#target", "my-event", map[string]any{"foo": "bar"}, agent.Postcondition{Text: "Received:"})
	if err != nil {
		t.Fatalf("DispatchEventResult: %v", err)
	}
	if result.Outcome != "postcondition_verified" || !result.Verified || !strings.Contains(result.Evidence, "Received:") || result.Action != "dispatch_event" {
		t.Fatalf("unexpected event outcome: %+v", result)
	}

	// Verify the event was received.
	val, err := s.Eval(`document.getElementById('target').textContent`)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	text, _ := val.(string)
	if !strings.Contains(text, "bar") {
		t.Errorf("expected target to contain 'bar', got %q", text)
	}
}

func TestIntegrationClickPostconditions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Before</title><button id="go">Go</button><script>document.querySelector('#go').addEventListener('click',()=>{const p=document.createElement('p');p.id='done';p.textContent='Done';document.body.appendChild(p);history.pushState({},'','/complete')})</script>`)
	}))
	defer srv.Close()
	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	result, err := s.ClickWithPostcondition("#go", agent.Postcondition{VisibleSelector: "#done"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Outcome != "postcondition_verified" {
		t.Fatalf("visible postcondition: %+v", result)
	}
	result, err = s.VerifyPostcondition("clicked", "#go", agent.Postcondition{URLContains: "/complete"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || !strings.Contains(result.Evidence, "/complete") {
		t.Fatalf("URL postcondition: %+v", result)
	}
	if _, err := s.ClickWithPostcondition("#go", agent.Postcondition{Text: "Done", URLContains: "/complete"}, false); err == nil {
		t.Fatal("expected conflicting postconditions to fail")
	}
}

func TestIntegrationNavigatePostcondition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Final</title><p>Welcome</p>`)
	}))
	defer final.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/landed", http.StatusFound)
	}))
	defer redirect.Close()
	s := integrationSession(t)
	if err := s.StartTrace(); err != nil {
		t.Fatal(err)
	}
	result, err := s.NavigateWithPostcondition(redirect.URL, agent.Postcondition{URLContains: "/landed"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Outcome != "postcondition_verified" || !strings.Contains(result.URL, "/landed") {
		t.Fatalf("redirect destination not verified: %+v", result)
	}
	tracePath := filepath.Join(t.TempDir(), "navigation-trace.zip")
	if _, err := s.StopTrace(tracePath); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var traced []agent.TraceEvent
	for _, file := range archive.File {
		if file.Name != "trace.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		decodeErr := json.NewDecoder(reader).Decode(&traced)
		closeErr := reader.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		break
	}
	if len(traced) != 1 || traced[0].Outcome != result.Outcome || !traced[0].Verified || !traced[0].VerificationKnown || traced[0].Evidence != result.Evidence {
		t.Fatalf("trace did not retain verified navigation result: %+v; result=%+v", traced, result)
	}
	result, err = s.NavigateWithPostcondition(redirect.URL, agent.Postcondition{URLContains: "/missing"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified || result.Outcome != "postcondition_failed" || result.RecoveryHint == "" {
		t.Fatalf("expected explicit mismatch result: %+v", result)
	}
}

func TestIntegrationTypeValueOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><input id="plain"><input id="trim" oninput="this.value=this.value.trim()">`)
	}))
	defer srv.Close()
	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	verified, err := s.Type("#plain", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if verified.Outcome != "value_verified" || !verified.Verified {
		t.Fatalf("expected value_verified: %+v", verified)
	}
	mismatch, err := s.Type("#trim", " hello ")
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Outcome != "value_mismatch" || mismatch.Verified || mismatch.RecoveryHint == "" {
		t.Fatalf("expected value_mismatch with recovery: %+v", mismatch)
	}
}

func TestIntegrationDragDropPostcondition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><div id="from" draggable="true">Drag</div><div id="to">Drop</div><p id="status">Waiting</p><script>document.querySelector('#to').addEventListener('drop',()=>document.querySelector('#status').textContent='Dropped')</script>`)
	}))
	defer srv.Close()
	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatal(err)
	}
	result, err := s.DragDropWithPostcondition("#from", "#to", agent.Postcondition{Text: "Dropped"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Outcome != "postcondition_verified" {
		t.Fatalf("drag postcondition: %+v", result)
	}
}

func TestIntegrationDispatchEventNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><p>empty</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	err := s.DispatchEvent("#nonexistent", "click", nil)
	if err == nil {
		t.Error("expected error dispatching event on nonexistent element")
	}
}

// ---------------------------------------------------------------------------
// Patterns: AutoExtract, ScrollAndCollect
// ---------------------------------------------------------------------------

func TestIntegrationAutoExtract(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		var items string
		for i := 1; i <= 5; i++ {
			items += fmt.Sprintf(`<li>
				<a href="/item/%d">Item %d</a>
				<span class="price">$%d.99</span>
				<p class="description">Description for item %d</p>
			</li>`, i, i, i*10, i)
		}
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Products</title></head>
		<body><ul id="products">%s</ul></body></html>`, items)
	}))
	defer srv.Close()

	s := integrationSession(t)
	s.SetContentOptions(agent.ContentOptions{MaxItems: 2})
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	pattern, err := s.AutoExtract()
	if err != nil {
		t.Fatalf("AutoExtract: %v", err)
	}
	if pattern.Count < 5 {
		t.Errorf("expected at least 5 items, got %d", pattern.Count)
	}
	if len(pattern.Items) == 0 {
		t.Fatal("expected extracted items, got none")
	}
	if len(pattern.Fields) == 0 {
		t.Error("expected at least one detected field")
	}
	if !pattern.Truncated || pattern.Offset != 0 || pattern.Limit != 2 || len(pattern.Items) != 2 {
		t.Errorf("expected bounded first page, got offset=%d limit=%d items=%d truncated=%t", pattern.Offset, pattern.Limit, len(pattern.Items), pattern.Truncated)
	}
	nextPage, err := s.AutoExtractPage(3, 2)
	if err != nil {
		t.Fatalf("AutoExtractPage: %v", err)
	}
	if nextPage.Count != pattern.Count || nextPage.Offset != 3 || nextPage.Limit != 2 || len(nextPage.Items) != 2 || nextPage.Truncated {
		t.Errorf("unexpected second page: count=%d offset=%d limit=%d items=%d truncated=%t", nextPage.Count, nextPage.Offset, nextPage.Limit, len(nextPage.Items), nextPage.Truncated)
	}

	// Verify some structured data was extracted.
	first := pattern.Items[0]
	if _, ok := first["title"]; !ok {
		if _, ok2 := first["text"]; !ok2 {
			t.Error("expected title or text field in first item")
		}
	}
}

func TestIntegrationAutoExtractNoPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Empty</title></head>
		<body><p>Single paragraph with no repeating pattern.</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.AutoExtract()
	if err == nil {
		t.Error("expected error on page with no repeating pattern")
	}
}

func TestIntegrationAutoExtractTable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Table</title></head>
		<body><table id="data"><tbody>
			<tr><td>Alice</td><td>30</td></tr>
			<tr><td>Bob</td><td>25</td></tr>
			<tr><td>Carol</td><td>28</td></tr>
		</tbody></table></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	pattern, err := s.AutoExtract()
	if err != nil {
		t.Fatalf("AutoExtract table: %v", err)
	}
	if pattern.Count < 3 {
		t.Errorf("expected at least 3 rows, got %d", pattern.Count)
	}
}

func TestIntegrationScrollAndCollect(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	// Build a tall page so items are already in the DOM (no lazy loading), just needs scrolling.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		var items string
		for i := 1; i <= 10; i++ {
			items += fmt.Sprintf(`<div class="item" style="height:200px;margin:10px;background:#eee;">Item %d content</div>`, i)
		}
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Scroll</title></head>
		<body style="margin:0;">%s</body></html>`, items)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Allow the page to fully load before scrolling.
	time.Sleep(500 * time.Millisecond)

	result, err := s.ScrollAndCollect(".item", 10)
	if err != nil {
		t.Fatalf("ScrollAndCollect: %v", err)
	}
	if result.Count < 10 {
		t.Errorf("expected at least 10 items, got %d", result.Count)
	}
	if result.Selector != ".item" {
		t.Errorf("expected selector '.item', got %q", result.Selector)
	}
	page, err := s.ScrollAndCollectPage(".item", 5, 5)
	if err != nil {
		t.Fatalf("ScrollAndCollectPage: %v", err)
	}
	if page.Count != 5 || page.Total != 10 || page.Offset != 5 || page.Limit != 5 || page.Truncated {
		t.Fatalf("unexpected second page: %+v", page)
	}
	if !strings.Contains(page.Items[0], "Item 6") {
		t.Fatalf("offset page began with wrong item: %q", page.Items[0])
	}
}

// ---------------------------------------------------------------------------
// Playbook: StartRecordingPlaybook, StopRecordingPlaybook, ReplayPlaybook
// ---------------------------------------------------------------------------

func TestIntegrationPlaybookRecordAndReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Home</title></head>
			<body><a href="/page2" id="link">Go to Page 2</a></body></html>`)
		case "/page2":
			fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Page 2</title></head>
			<body><h1 id="heading">Page Two</h1>
			<input id="name" type="text" placeholder="Name" /></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := integrationSession(t)

	// Navigate to home first, then start recording.
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	s.StartRecordingPlaybook("test-flow")

	// Perform actions that get recorded.
	if _, err := s.Navigate(srv.URL + "/page2"); err != nil {
		t.Fatalf("Navigate page2: %v", err)
	}
	if _, err := s.Click("#name"); err != nil {
		t.Fatalf("Click: %v", err)
	}

	pb, err := s.StopRecordingPlaybook()
	if err != nil {
		t.Fatalf("StopRecordingPlaybook: %v", err)
	}
	if pb.Name != "test-flow" {
		t.Errorf("expected playbook name 'test-flow', got %q", pb.Name)
	}
	if len(pb.Actions) < 2 {
		t.Fatalf("expected at least 2 recorded actions, got %d", len(pb.Actions))
	}
}

func TestIntegrationPlaybookReplayManual(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Home</title></head>
			<body><h1>Welcome</h1><a href="/about" id="about-link">About</a></body></html>`)
		case "/about":
			fmt.Fprint(w, `<!DOCTYPE html><html><head><title>About</title></head>
			<body><h1 id="about-heading">About Us</h1></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := integrationSession(t)

	// Build a playbook manually — use navigate + extract (avoids click navigation DOM caching issues).
	pb := &agent.Playbook{
		Name: "manual",
		URL:  srv.URL,
		Actions: []agent.Action{
			{Type: "navigate", Value: srv.URL + "/about"},
			{Type: "wait", Selector: "#about-heading"},
			{Type: "extract", Selector: "#about-heading", Value: "heading_text"},
		},
		CreatedAt: time.Now(),
	}

	result, err := s.ReplayPlaybook(pb)
	if err != nil {
		t.Fatalf("ReplayPlaybook: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected replay success, got error at step %d: %s", result.FailedAt, result.Error)
	}
	if result.StepsRun != 3 {
		t.Errorf("expected 3 steps run, got %d", result.StepsRun)
	}
	if text, ok := result.Extracted["heading_text"]; !ok || text != "About Us" {
		t.Errorf("expected extracted heading_text='About Us', got %q", result.Extracted["heading_text"])
	}
}

func TestIntegrationPlaybookStopWithoutStart(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	s := integrationSession(t)
	_, err := s.StopRecordingPlaybook()
	if err == nil {
		t.Error("expected error when stopping recording without starting")
	}
}

func TestIntegrationPlaybookReplayFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Fail</title></head>
		<body><p>Nothing clickable here</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)

	pb := &agent.Playbook{
		Name: "failing",
		URL:  srv.URL,
		Actions: []agent.Action{
			{Type: "click", Selector: "#nonexistent-button"},
		},
		CreatedAt: time.Now(),
	}

	result, err := s.ReplayPlaybook(pb)
	if err != nil {
		t.Fatalf("ReplayPlaybook returned Go error: %v", err)
	}
	if result.Success {
		t.Error("expected replay failure")
	}
	if result.FailedAt != 1 {
		t.Errorf("expected failure at step 1, got %d", result.FailedAt)
	}
	if result.FailurePhase != "action" || !strings.HasPrefix(result.CurrentURL, srv.URL) || result.CurrentTitle != "Fail" {
		t.Errorf("expected failure context, got phase=%q url=%q title=%q", result.FailurePhase, result.CurrentURL, result.CurrentTitle)
	}
	if !strings.Contains(result.Evidence, "Nothing clickable here") || result.RecoveryHint == "" {
		t.Errorf("expected page evidence and recovery hint, got evidence=%q hint=%q", result.Evidence, result.RecoveryHint)
	}
}

func TestIntegrationPlaybookResumeAfterRepair(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><head><title>Repair</title></head><body><p>Initial page</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	pb := &agent.Playbook{
		Name: "resume-after-repair",
		URL:  srv.URL,
		Actions: []agent.Action{
			{Type: "extract", Selector: "#later", Value: "later"},
			{Type: "extract", Selector: "#done", Value: "done"},
		},
	}
	first, err := s.ReplayPlaybook(pb)
	if err != nil || first.Success || first.FailedAt != 1 {
		t.Fatalf("expected first action failure, result=%+v err=%v", first, err)
	}
	if !first.ResumeSafe || first.ResumeToken == "" {
		t.Fatalf("safe replay failure should include a session checkpoint: %+v", first)
	}
	modified := *first
	modified.StepsRun++
	if _, err := s.ResumePlaybook(pb, &modified); err == nil || !strings.Contains(err.Error(), "changed after replay") {
		t.Fatalf("modified replay result error = %v, want checkpoint integrity rejection", err)
	}
	if _, err := s.Eval(`document.body.innerHTML += '<p id="later">repaired</p><p id="done">complete</p>'`); err != nil {
		t.Fatalf("repair page: %v", err)
	}
	resumed, err := s.ResumePlaybook(pb, first)
	if err != nil || !resumed.Success {
		t.Fatalf("expected successful resume, result=%+v err=%v", resumed, err)
	}
	if resumed.StepsRun != 2 || resumed.Extracted["later"] != "repaired" || resumed.Extracted["done"] != "complete" {
		t.Fatalf("resume result did not carry progress and extracted data: %+v", resumed)
	}
}

// ---------------------------------------------------------------------------
// Diagnostics: ConsoleErrors, DetectAuthWall, CompareTabs
// ---------------------------------------------------------------------------

func TestIntegrationConsoleErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Console</title></head>
		<body><p>Hello</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// First call installs the console interceptor; no errors yet.
	msgs, err := s.ConsoleErrors()
	if err != nil {
		t.Fatalf("ConsoleErrors (install): %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages initially, got %d", len(msgs))
	}

	// Inject console.error and console.warn via Eval.
	if _, err := s.Eval(`console.error("test error message")`); err != nil {
		t.Fatalf("Eval console.error: %v", err)
	}
	if _, err := s.Eval(`console.warn("test warning message")`); err != nil {
		t.Fatalf("Eval console.warn: %v", err)
	}
	if _, err := s.Eval(`console.error("x".repeat(3000))`); err != nil {
		t.Fatalf("Eval oversized console.error: %v", err)
	}

	// Retrieve the captured messages.
	msgs, err = s.ConsoleErrors()
	if err != nil {
		t.Fatalf("ConsoleErrors (retrieve): %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 console messages, got %d", len(msgs))
	}

	foundError, foundWarn, foundTruncated := false, false, false
	for _, m := range msgs {
		if m.Level == "error" && strings.Contains(m.Text, "test error message") {
			foundError = true
		}
		if m.Level == "warn" && strings.Contains(m.Text, "test warning message") {
			foundWarn = true
		}
		if m.Truncated && len(m.Text) == 2000 {
			foundTruncated = true
		}
	}
	if !foundError {
		t.Error("expected to find the injected console.error message")
	}
	if !foundWarn {
		t.Error("expected to find the injected console.warn message")
	}
	if !foundTruncated {
		t.Error("expected oversized console message to be truncated and marked")
	}
}

func TestIntegrationDetectAuthWallLogin(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Login</title></head>
		<body>
			<div id="login-form">
				<h2>Sign In</h2>
				<form action="/auth/login">
					<input type="email" placeholder="Email" />
					<input type="password" placeholder="Password" />
					<button type="submit">Log In</button>
				</form>
				<a href="/auth/forgot">Forgot password?</a>
			</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.DetectAuthWall()
	if err != nil {
		t.Fatalf("DetectAuthWall: %v", err)
	}
	if !result.Detected {
		t.Error("expected auth wall to be detected")
	}
	if result.Type != "login" {
		t.Errorf("expected type 'login', got %q", result.Type)
	}
	if result.Confidence < 30 {
		t.Errorf("expected confidence >= 30, got %d", result.Confidence)
	}
}

func TestIntegrationDetectAuthWallNone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Blog</title></head>
		<body><h1>Welcome to the Blog</h1><p>Here is some content.</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.DetectAuthWall()
	if err != nil {
		t.Fatalf("DetectAuthWall: %v", err)
	}
	if result.Detected {
		t.Errorf("expected no auth wall, but detected type=%q confidence=%d", result.Type, result.Confidence)
	}
}

func TestIntegrationCompareTabs(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Product A</title></head>
		<body>
			<h1>Widget Alpha</h1>
			<span class="price">$19.99</span>
			<a href="/details">Details</a>
		</body></html>`)
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Product B</title></head>
		<body>
			<h1>Widget Beta</h1>
			<span class="price">$29.99</span>
			<a href="/buy">Buy Now</a>
		</body></html>`)
	}))
	defer srvB.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srvA.URL); err != nil {
		t.Fatalf("Navigate A: %v", err)
	}
	if _, err := s.OpenTab("b", srvB.URL); err != nil {
		t.Fatalf("OpenTab B: %v", err)
	}

	diff, err := s.CompareTabs("default", "b")
	if err != nil {
		t.Fatalf("CompareTabs: %v", err)
	}
	if diff.Title1 != "Product A" {
		t.Errorf("expected Title1='Product A', got %q", diff.Title1)
	}
	if diff.Title2 != "Product B" {
		t.Errorf("expected Title2='Product B', got %q", diff.Title2)
	}
	// There should be differences or items unique to each tab.
	totalDiffs := len(diff.OnlyIn1) + len(diff.OnlyIn2) + len(diff.Different)
	if totalDiffs == 0 {
		t.Error("expected some differences between the two tabs")
	}
}

func TestIntegrationCompareTabsNoTabs(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	s := integrationSession(t)

	// CompareTabs without any tabs open should error.
	_, err := s.CompareTabs("a", "b")
	if err == nil {
		t.Error("expected error comparing tabs when no tabs open")
	}
}

// ---------------------------------------------------------------------------
// Budget: ObserveWithBudget
// ---------------------------------------------------------------------------

func TestIntegrationObserveWithBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		var links string
		for i := 0; i < 50; i++ {
			links += fmt.Sprintf(`<a href="/page/%d">Link %d</a> `, i, i)
		}
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Budget</title></head>
		<body><h1>Many Links</h1>%s<p>%s</p></body></html>`,
			links, strings.Repeat("Some text content. ", 100))
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Use a small budget — expect fewer elements and truncated text.
	obs, err := s.ObserveWithBudget(200)
	if err != nil {
		t.Fatalf("ObserveWithBudget: %v", err)
	}
	if obs.URL == "" {
		t.Error("expected non-empty URL in observation")
	}

	// A larger budget should return more data.
	obsLarge, err := s.ObserveWithBudget(5000)
	if err != nil {
		t.Fatalf("ObserveWithBudget large: %v", err)
	}

	// The small-budget observation should have fewer or equal links.
	if len(obs.Links) > len(obsLarge.Links) {
		t.Errorf("smaller budget returned more links (%d) than larger budget (%d)", len(obs.Links), len(obsLarge.Links))
	}

	// A one-token request allocates no budget to interactive elements. Zero is
	// an intentional cap here, not the default content limit.
	obsTiny, err := s.ObserveWithBudget(1)
	if err != nil {
		t.Fatalf("ObserveWithBudget tiny: %v", err)
	}
	if len(obsTiny.Links) != 0 || len(obsTiny.Inputs) != 0 || len(obsTiny.Buttons) != 0 || !obsTiny.Truncated {
		t.Fatalf("tiny budget ignored its zero interactive allowance: links=%d inputs=%d buttons=%d truncated=%t", len(obsTiny.Links), len(obsTiny.Inputs), len(obsTiny.Buttons), obsTiny.Truncated)
	}
}

func TestIntegrationObservationAndDiffReportTruncation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	links := strings.Builder{}
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&links, `<a href="/item/%d">item %d</a>`, i, i)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head><title>%s</title></head><body>%s</body></html>`, strings.Repeat("t", 600), links.String())
	}))
	defer server.Close()

	session := integrationSession(t)
	longURL := server.URL + "/" + strings.Repeat("u", 2100)
	if _, err := session.Navigate(longURL); err != nil {
		t.Fatal(err)
	}
	obs, _, err := session.ObserveDiff()
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Links) != 25 || obs.LinksTotal != 150 || len(obs.Title) != 500 || !obs.Truncated {
		t.Fatalf("observation limits not reported: links=%d/%d title=%d truncated=%t", len(obs.Links), obs.LinksTotal, len(obs.Title), obs.Truncated)
	}
	if len(obs.URL) > 2048 || obs.URLLength <= len(obs.URL) {
		t.Fatalf("URL was not clipped with original length: %d/%d", len(obs.URL), obs.URLLength)
	}

	if _, err := session.Eval(`(() => { const f=document.createDocumentFragment(); for(let i=0;i<150;i++){const n=document.createElement('div');n.textContent='new '+i;f.appendChild(n)} document.body.appendChild(f); return true })()`); err != nil {
		t.Fatal(err)
	}
	_, diff, err := session.ObserveDiff()
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Added) != 20 || diff.AddedTotal < 150 || !diff.Truncated {
		t.Fatalf("diff limits not reported: added=%d/%d truncated=%t", len(diff.Added), diff.AddedTotal, diff.Truncated)
	}
}

// ---------------------------------------------------------------------------
// Cookies: NavigateAndDismissCookies (covers autoNavigate path)
// ---------------------------------------------------------------------------

func TestIntegrationAutoNavigateWithCookieBanner(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Cookies</title></head>
		<body>
			<div id="cookie-banner">
				<p>We use cookies.</p>
				<button id="accept-cookies">Accept</button>
			</div>
			<p>Main content</p>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)

	// NavigateAndDismissCookies covers the autoNavigate code path.
	result, err := s.NavigateAndDismissCookies(srv.URL)
	if err != nil {
		t.Fatalf("NavigateAndDismissCookies: %v", err)
	}
	if result.Title != "Cookies" {
		t.Errorf("expected title 'Cookies', got %q", result.Title)
	}
}

// ---------------------------------------------------------------------------
// Selector: findByText via :text() selector syntax
// ---------------------------------------------------------------------------

func TestIntegrationFindByTextSelector(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Text Select</title></head>
		<body>
			<button id="btn-a" onclick="document.getElementById('output').textContent='clicked-submit'">Submit Order</button>
			<button id="btn-b" onclick="document.getElementById('output').textContent='clicked-cancel'">Cancel Order</button>
			<span id="output"></span>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Click using :text() Playwright-style selector via Click, which uses resolveSelector -> findByText.
	_, err := s.Click(`button:text('Cancel Order')`)
	if err != nil {
		t.Fatalf("Click :text(): %v", err)
	}

	// Verify the correct button was clicked.
	val, err := s.Extract("#output")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if val.Text != "clicked-cancel" {
		t.Errorf("expected 'clicked-cancel', got %q", val.Text)
	}
}

func TestIntegrationFindByTextNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><p>Nothing here</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.Click(`button:text('Nonexistent Button')`)
	if err == nil {
		t.Error("expected error clicking nonexistent :text() selector")
	}
}

func TestIntegrationFindByHasTextSelector(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>HasText</title></head>
		<body>
			<button id="btn-premium" onclick="document.getElementById('out').textContent='premium'">Premium Plan</button>
			<button id="btn-basic" onclick="document.getElementById('out').textContent='basic'">Basic Plan</button>
			<span id="out"></span>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// :has-text() should find the element containing that text.
	_, err := s.Click(`button:has-text('Premium Plan')`)
	if err != nil {
		t.Fatalf("Click :has-text(): %v", err)
	}

	val, err := s.Eval(`document.getElementById('out').textContent`)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	text, _ := val.(string)
	if text != "premium" {
		t.Errorf("expected 'premium', got %q", text)
	}
}

func TestIntegrationValueAndCountPostconditions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
		<input id="status" value="saved"><ul><li class="row">one</li><li class="row">two</li></ul>
		<button id="add" onclick="const li=document.createElement('li');li.className='row';li.textContent='three';document.querySelector('ul').appendChild(li)">Add</button>
		</body></html>`)
	}))
	defer srv.Close()
	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	wantRows := 3
	result, err := s.ClickWithPostcondition("#add", agent.Postcondition{CountSelector: ".row", Count: &wantRows}, false)
	if err != nil {
		t.Fatalf("ClickWithPostcondition count: %v", err)
	}
	if !result.Verified || result.Outcome != "postcondition_verified" || !strings.Contains(result.Evidence, "matched 3") {
		t.Fatalf("count postcondition result: %+v", result)
	}

	result, err = s.VerifyPostcondition("submitted", "#status", agent.Postcondition{ValueSelector: "#status", Value: "saved"})
	if err != nil {
		t.Fatalf("VerifyPostcondition value: %v", err)
	}
	if !result.Verified || result.Outcome != "postcondition_verified" || !strings.Contains(result.Evidence, `"saved"`) {
		t.Fatalf("value postcondition result: %+v", result)
	}

	wantRows = 4
	result, err = s.VerifyPostcondition("checked", ".row", agent.Postcondition{CountSelector: ".row", Count: &wantRows})
	if err != nil {
		t.Fatalf("VerifyPostcondition failed count: %v", err)
	}
	if result.Verified || result.Outcome != "postcondition_failed" || !strings.Contains(result.Evidence, "matched 3 elements") {
		t.Fatalf("failed count postcondition result: %+v", result)
	}

	wantRows = 3
	batch, err := s.ExecuteBatch([]agent.BatchAction{{
		Action: "wait", Selector: "#add", ExpectedCountSelector: ".row", ExpectedCount: &wantRows,
	}})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if batch.Failed != 0 || len(batch.Results) != 1 || !batch.Results[0].Verified || !strings.Contains(batch.Results[0].Evidence, "matched 3") {
		t.Fatalf("batch count postcondition result: %+v", batch)
	}
}
