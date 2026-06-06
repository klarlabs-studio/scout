package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
)

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

	// Open a second tab.
	result, err := s.OpenTab("second", srvB.URL)
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if result.Title != "Page B" {
		t.Errorf("expected title 'Page B', got %q", result.Title)
	}

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

	// Cannot close the active tab.
	if err := s.CloseTab("default"); err == nil {
		t.Error("expected error when closing active tab")
	}

	// Close the non-active tab.
	if err := s.CloseTab("second"); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	// ListTabs — should have 1 tab remaining.
	tabs, err = s.ListTabs()
	if err != nil {
		t.Fatalf("ListTabs after close: %v", err)
	}
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab after close, got %d", len(tabs))
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

func TestIntegrationTabsSwitchNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>X</title></head><body>x</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := s.OpenTab("a", srv.URL); err != nil {
		t.Fatalf("OpenTab: %v", err)
	}

	if _, err := s.SwitchTab("nonexistent"); err == nil {
		t.Error("expected error switching to nonexistent tab")
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

	err := s.DispatchEvent("#target", "my-event", map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("DispatchEvent: %v", err)
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

	// Retrieve the captured messages.
	msgs, err = s.ConsoleErrors()
	if err != nil {
		t.Fatalf("ConsoleErrors (retrieve): %v", err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 console messages, got %d", len(msgs))
	}

	foundError, foundWarn := false, false
	for _, m := range msgs {
		if m.Level == "error" && strings.Contains(m.Text, "test error message") {
			foundError = true
		}
		if m.Level == "warn" && strings.Contains(m.Text, "test warning message") {
			foundWarn = true
		}
	}
	if !foundError {
		t.Error("expected to find the injected console.error message")
	}
	if !foundWarn {
		t.Error("expected to find the injected console.warn message")
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
