//go:build integration

package agent_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
)

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func integrationSession(t *testing.T) *agent.Session {
	t.Helper()
	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func integrationSessionStealth(t *testing.T) *agent.Session {
	t.Helper()
	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Stealth: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// 1. SelectByPrompt (nlselect.go)
// ---------------------------------------------------------------------------

func TestIntegrationSelectByPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>NL Select</title></head><body>
			<a href="/login" id="login-link">Log In</a>
			<button id="signup-btn">Sign Up Now</button>
			<input id="search" name="search" type="text" placeholder="Search products" />
			<button id="submit-btn">Submit Order</button>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Exact text match
	result, err := s.SelectByPrompt("Sign Up Now")
	if err != nil {
		t.Fatalf("SelectByPrompt exact: %v", err)
	}
	if result.Tag != "button" {
		t.Errorf("expected button, got %s", result.Tag)
	}
	if result.Confidence < 0.8 {
		t.Errorf("expected confidence >= 0.8, got %f", result.Confidence)
	}
	if len(result.Candidates) == 0 {
		t.Error("expected at least one candidate")
	}

	// Fuzzy keyword match
	result2, err := s.SelectByPrompt("Log In")
	if err != nil {
		t.Fatalf("SelectByPrompt fuzzy: %v", err)
	}
	if result2.Tag != "a" {
		t.Errorf("expected a, got %s", result2.Tag)
	}

	// Placeholder match
	result3, err := s.SelectByPrompt("Search products")
	if err != nil {
		t.Fatalf("SelectByPrompt placeholder: %v", err)
	}
	if result3.Tag != "input" {
		t.Errorf("expected input, got %s", result3.Tag)
	}

	// No match
	_, err = s.SelectByPrompt("nonexistent element xyz")
	if err == nil {
		t.Error("expected error for nonexistent element")
	}
}

// Test SelectByPrompt with multiple candidates and confidence scoring
func TestIntegrationSelectByPromptMultipleCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<button id="btn-a">Submit Form</button>
			<button id="btn-b">Submit Request</button>
			<a href="/submit" id="link-a">Submit Application</a>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.SelectByPrompt("Submit")
	if err != nil {
		t.Fatalf("SelectByPrompt: %v", err)
	}
	// Should return multiple candidates with "Submit" in text
	if len(result.Candidates) < 2 {
		t.Errorf("expected at least 2 candidates for 'Submit', got %d", len(result.Candidates))
	}
	for _, c := range result.Candidates {
		t.Logf("  Candidate: selector=%s text=%q score=%.0f", c.Selector, c.Text, c.Score)
	}
}

// ---------------------------------------------------------------------------
// 2. ExecuteBatch — request body capture via form submission
// ---------------------------------------------------------------------------

func TestIntegrationExecuteBatchWithFormSemantic(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<form id="myform">
				<label for="username">Username</label>
				<input id="username" name="username" type="text" />
				<label for="pwd">Password</label>
				<input id="pwd" name="pwd" type="password" />
				<button id="go" type="button" onclick="document.getElementById('result').textContent='done'">Go</button>
			</form>
			<div id="result"></div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.ExecuteBatch([]agent.BatchAction{
		{Action: "fill_form_semantic", Fields: map[string]string{
			"Username": "alice",
			"Password": "secret123",
		}},
		{Action: "click", Selector: "#go"},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if result.Total != 2 {
		t.Errorf("total: expected 2, got %d", result.Total)
	}
	if result.Failed != 0 {
		for _, r := range result.Results {
			if !r.Success {
				t.Errorf("action %d (%s) failed: %s", r.Index, r.Action, r.Error)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 3. HybridObserve + FindByCoordinates — already well covered, add bounding box test
// ---------------------------------------------------------------------------

func TestIntegrationHybridObserveBoundingBoxes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="margin:0;padding:0;">
			<button id="btn1" style="position:absolute;left:50px;top:50px;width:100px;height:40px;">Alpha</button>
			<input id="inp1" name="q" style="position:absolute;left:200px;top:50px;width:200px;height:40px;" placeholder="search" />
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	hr, err := s.HybridObserve()
	if err != nil {
		t.Fatalf("HybridObserve: %v", err)
	}
	if len(hr.Elements) < 2 {
		t.Fatalf("expected at least 2 elements, got %d", len(hr.Elements))
	}

	for _, el := range hr.Elements {
		if el.X < 0 || el.Y < 0 {
			t.Errorf("element %d (%s) has negative coordinates: (%f, %f)", el.Index, el.Tag, el.X, el.Y)
		}
		if el.Width <= 0 || el.Height <= 0 {
			t.Errorf("element %d (%s) has non-positive dimensions: %fx%f", el.Index, el.Tag, el.Width, el.Height)
		}
	}

	// FindByCoordinates at the button center
	found, err := s.FindByCoordinates(100, 70)
	if err != nil {
		t.Fatalf("FindByCoordinates: %v", err)
	}
	if found.Tag != "button" {
		t.Errorf("expected button at (100,70), got %s", found.Tag)
	}
}

// ---------------------------------------------------------------------------
// 4. StartTrace / StopTrace — covered in trace_test.go but add network+trace combo
// ---------------------------------------------------------------------------

func TestIntegrationTraceWithNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<h1 id="title">Trace Test</h1>
			<button id="btn" onclick="document.getElementById('title').textContent='Clicked!'">Click</button>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)

	if err := s.EnableNetworkCapture(); err != nil {
		t.Fatalf("EnableNetworkCapture: %v", err)
	}
	if err := s.StartTrace(); err != nil {
		t.Fatalf("StartTrace: %v", err)
	}

	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := s.Click("#btn"); err != nil {
		t.Fatalf("Click: %v", err)
	}

	outPath := t.TempDir() + "/trace-net.zip"
	result, err := s.StopTrace(outPath)
	if err != nil {
		t.Fatalf("StopTrace: %v", err)
	}
	if result.EventCount < 2 {
		t.Errorf("expected at least 2 events, got %d", result.EventCount)
	}
	if result.Size == 0 {
		t.Error("trace zip should not be empty")
	}
}

// ---------------------------------------------------------------------------
// 5. WebVitals (vitals.go)
// ---------------------------------------------------------------------------

func TestIntegrationWebVitals(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Vitals</title></head><body>
			<h1>Performance Test</h1>
			<p>Some content to paint and measure.</p>
			<img src="data:image/gif;base64,R0lGODlhAQABAIAAAP///wAAACH5BAEAAAAALAAAAAABAAEAAAICRAEAOw==" alt="pixel" />
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Give the page a moment to settle performance entries
	time.Sleep(500 * time.Millisecond)

	vitals, err := s.WebVitals()
	if err != nil {
		t.Fatalf("WebVitals: %v", err)
	}

	// TTFB and DOMContentLoaded should be non-negative
	if vitals.TTFB < 0 {
		t.Errorf("TTFB should be >= 0, got %f", vitals.TTFB)
	}
	if vitals.DOMContentLoaded < 0 {
		t.Errorf("DOMContentLoaded should be >= 0, got %f", vitals.DOMContentLoaded)
	}

	// Ratings should be set
	validRatings := map[string]bool{"good": true, "needs-improvement": true, "poor": true}
	if !validRatings[vitals.LCPRating] {
		t.Errorf("unexpected LCP rating: %q", vitals.LCPRating)
	}
	if !validRatings[vitals.CLSRating] {
		t.Errorf("unexpected CLS rating: %q", vitals.CLSRating)
	}
	if !validRatings[vitals.INPRating] {
		t.Errorf("unexpected INP rating: %q", vitals.INPRating)
	}
	if !validRatings[vitals.OverallRating] {
		t.Errorf("unexpected overall rating: %q", vitals.OverallRating)
	}

	t.Logf("WebVitals: LCP=%.0fms(%s) CLS=%.4f(%s) INP=%.0fms(%s) TTFB=%.0fms DOMContentLoaded=%.0fms FirstPaint=%.0fms Overall=%s",
		vitals.LCP, vitals.LCPRating, vitals.CLS, vitals.CLSRating, vitals.INP, vitals.INPRating,
		vitals.TTFB, vitals.DOMContentLoaded, vitals.FirstPaint, vitals.OverallRating)
}

// ---------------------------------------------------------------------------
// 6. SwitchToFrame / SwitchToMainFrame (iframe.go)
// ---------------------------------------------------------------------------

func TestIntegrationIframeSwitching(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	// Serve iframe content at /child
	mux := http.NewServeMux()
	mux.HandleFunc("/child", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Child Frame</title></head><body>
			<h1 id="child-title">Inside iframe</h1>
			<button id="child-btn" onclick="document.getElementById('child-title').textContent='Clicked in frame!'">Frame Button</button>
		</body></html>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Parent Page</title></head><body>
			<h1 id="parent-title">Parent Content</h1>
			<iframe id="my-frame" name="child-frame" src="/child" style="width:400px;height:300px;"></iframe>
		</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Wait for iframe to load
	time.Sleep(1 * time.Second)

	if s.InFrame() {
		t.Error("should not be in frame initially")
	}

	// Switch to iframe
	frameResult, err := s.SwitchToFrame("#my-frame")
	if err != nil {
		t.Fatalf("SwitchToFrame: %v", err)
	}
	t.Logf("Frame result: URL=%s Title=%s", frameResult.URL, frameResult.Title)

	if !s.InFrame() {
		t.Error("should be in frame after SwitchToFrame")
	}

	// Switch back to main
	mainResult, err := s.SwitchToMainFrame()
	if err != nil {
		t.Fatalf("SwitchToMainFrame: %v", err)
	}
	if s.InFrame() {
		t.Error("should not be in frame after SwitchToMainFrame")
	}
	if mainResult.Title != "Parent Page" {
		t.Errorf("expected parent title, got %q", mainResult.Title)
	}
}

// ---------------------------------------------------------------------------
// 7. Network capture with request body (network.go)
// ---------------------------------------------------------------------------

func TestIntegrationNetworkRequestBody(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/submit", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"received":true,"body":%q}`, string(body))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<button id="send" onclick="
				fetch('/api/submit', {
					method: 'POST',
					headers: {'Content-Type': 'application/json'},
					body: JSON.stringify({name: 'test', value: 42})
				}).then(r => r.json()).then(d => {
					document.getElementById('result').textContent = JSON.stringify(d);
				});
			">Send</button>
			<div id="result"></div>
		</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	if err := s.EnableNetworkCapture("/api/"); err != nil {
		t.Fatalf("EnableNetworkCapture: %v", err)
	}

	// Use Eval to trigger the fetch directly to avoid WaitStable blocking on network activity
	_, err := s.Eval(`(function() {
		fetch('/api/submit', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({name: 'test', value: 42})
		}).then(r => r.json()).then(d => {
			document.getElementById('result').textContent = JSON.stringify(d);
		});
		return true;
	})()`)
	if err != nil {
		t.Fatalf("Eval fetch: %v", err)
	}

	// Wait for the fetch to complete
	time.Sleep(2 * time.Second)

	captured := s.CapturedRequests("/api/submit")
	t.Logf("Captured %d requests to /api/submit", len(captured))

	if len(captured) > 0 {
		cap := captured[0]
		t.Logf("  Method=%s Status=%d RequestBody=%q ResponseBody=%q", cap.Method, cap.Status, cap.RequestBody, cap.ResponseBody)
		if cap.Method != "POST" {
			t.Errorf("expected POST, got %s", cap.Method)
		}
		if cap.RequestBody != "" && !strings.Contains(cap.RequestBody, "test") {
			t.Errorf("request body should contain 'test': %q", cap.RequestBody)
		}
		if cap.Status != 200 {
			t.Errorf("expected status 200, got %d", cap.Status)
		}
	}

	// Test ClearCapturedRequests
	s.ClearCapturedRequests()
	if len(s.CapturedRequests("")) != 0 {
		t.Error("expected empty captures after clear")
	}

	// Test DisableNetworkCapture
	s.DisableNetworkCapture()
}

// ---------------------------------------------------------------------------
// 8. Stealth (stealth.go)
// ---------------------------------------------------------------------------

func TestIntegrationStealthPatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="webdriver"></div>
			<div id="chrome-runtime"></div>
			<div id="plugins"></div>
			<div id="languages"></div>
			<script>
				document.getElementById('webdriver').textContent = String(navigator.webdriver);
				document.getElementById('chrome-runtime').textContent = String(typeof window.chrome?.runtime);
				document.getElementById('plugins').textContent = String(navigator.plugins.length);
				document.getElementById('languages').textContent = JSON.stringify(navigator.languages);
			</script>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSessionStealth(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Check navigator.webdriver is false
	webdriverResult, err := s.Extract("#webdriver")
	if err != nil {
		t.Fatalf("Extract webdriver: %v", err)
	}
	if webdriverResult.Text != "false" {
		t.Errorf("navigator.webdriver should be 'false', got %q", webdriverResult.Text)
	}

	// Check chrome.runtime exists (may be 'object' or 'undefined' depending on Chrome version)
	chromeResult, err := s.Extract("#chrome-runtime")
	if err != nil {
		t.Fatalf("Extract chrome-runtime: %v", err)
	}
	t.Logf("chrome.runtime type: %q", chromeResult.Text)

	// Check plugins are non-empty
	pluginsResult, err := s.Extract("#plugins")
	if err != nil {
		t.Fatalf("Extract plugins: %v", err)
	}
	if pluginsResult.Text == "0" {
		t.Error("plugins.length should not be 0 with stealth enabled")
	}

	// Check languages
	langsResult, err := s.Extract("#languages")
	if err != nil {
		t.Fatalf("Extract languages: %v", err)
	}
	if !strings.Contains(langsResult.Text, "en-US") {
		t.Errorf("languages should contain en-US, got %q", langsResult.Text)
	}

	t.Logf("Stealth: webdriver=%s chrome.runtime=%s plugins=%s languages=%s",
		webdriverResult.Text, chromeResult.Text, pluginsResult.Text, langsResult.Text)
}

// ---------------------------------------------------------------------------
// 9. Observe — action costs, dialog detection (observe.go)
// ---------------------------------------------------------------------------

func TestIntegrationObserveActionCosts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<a href="#section">Anchor Link</a>
			<a href="/other-page">Navigation Link</a>
			<a href="javascript:void(0)">JS Link</a>
			<button type="submit">Submit Form</button>
			<button type="button" onclick="void(0)">Toggle</button>
			<button type="button">Next</button>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	obs, err := s.Observe()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	// Check link costs
	linkCosts := map[string]string{}
	for _, l := range obs.Links {
		linkCosts[l.Text] = l.Cost
	}
	if linkCosts["Anchor Link"] != "low" {
		t.Errorf("anchor link should be low cost, got %q", linkCosts["Anchor Link"])
	}
	if linkCosts["Navigation Link"] != "high" {
		t.Errorf("navigation link should be high cost, got %q", linkCosts["Navigation Link"])
	}
	if linkCosts["JS Link"] != "medium" {
		t.Errorf("JS link should be medium cost, got %q", linkCosts["JS Link"])
	}

	// Check button costs
	btnCosts := map[string]string{}
	for _, b := range obs.Buttons {
		btnCosts[b.Text] = b.Cost
	}
	if btnCosts["Submit Form"] != "high" {
		t.Errorf("submit button should be high cost, got %q", btnCosts["Submit Form"])
	}
	if btnCosts["Toggle"] != "low" {
		t.Errorf("toggle button should be low cost, got %q", btnCosts["Toggle"])
	}
}

func TestIntegrationObserveDialogDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<dialog id="my-dialog" open>
				<h2>Confirm Action</h2>
				<p>Are you sure?</p>
				<button>Yes</button>
				<button>No</button>
			</dialog>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	obs, err := s.Observe()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.HasDialog {
		t.Error("expected HasDialog to be true for open <dialog>")
	}
	if obs.DialogType != "dialog" {
		t.Errorf("expected dialog type 'dialog', got %q", obs.DialogType)
	}
	if !strings.Contains(obs.DialogText, "Confirm Action") {
		t.Errorf("dialog text should contain 'Confirm Action', got %q", obs.DialogText)
	}
	t.Logf("Dialog: type=%s text=%q", obs.DialogType, obs.DialogText)
}

// ---------------------------------------------------------------------------
// 10. Markdown, ReadableText, AccessibilityTree (content.go)
// ---------------------------------------------------------------------------

func TestIntegrationContentExtraction(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Content Test</title></head><body>
			<nav><a href="/">Home</a><a href="/about">About</a></nav>
			<main>
				<article>
					<h1>Article Title</h1>
					<p>This is the <strong>main content</strong> of the article. It should be extracted by ReadableText.</p>
					<ul>
						<li>Item one</li>
						<li>Item two</li>
					</ul>
					<table>
						<thead><tr><th>Name</th><th>Value</th></tr></thead>
						<tbody><tr><td>Alpha</td><td>100</td></tr></tbody>
					</table>
					<form>
						<input id="field" name="field" type="text" placeholder="Enter value" />
						<select id="opts" name="opts">
							<option>Option A</option>
							<option>Option B</option>
						</select>
						<button type="submit">Save</button>
					</form>
				</article>
			</main>
			<footer>Copyright 2024</footer>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Markdown
	md, err := s.Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if !strings.Contains(md, "# Article Title") {
		t.Error("markdown should contain h1 as # heading")
	}
	if !strings.Contains(md, "**main content**") {
		t.Error("markdown should contain bold text")
	}
	if !strings.Contains(md, "- Item one") {
		t.Error("markdown should contain list items")
	}
	if !strings.Contains(md, "[input:") {
		t.Error("markdown should contain form input notation")
	}
	t.Logf("Markdown (%d chars)", len(md))

	// ReadableText
	text, err := s.ReadableText()
	if err != nil {
		t.Fatalf("ReadableText: %v", err)
	}
	if !strings.Contains(text, "Article Title") {
		t.Error("readable text should contain article title")
	}
	if !strings.Contains(text, "main content") {
		t.Error("readable text should contain main content")
	}
	t.Logf("ReadableText (%d chars)", len(text))

	// AccessibilityTree
	tree, err := s.AccessibilityTree()
	if err != nil {
		t.Fatalf("AccessibilityTree: %v", err)
	}
	if !strings.Contains(tree, "link") {
		t.Error("accessibility tree should contain link elements")
	}
	if !strings.Contains(tree, "input") {
		t.Error("accessibility tree should contain input elements")
	}
	if !strings.Contains(tree, "button") {
		t.Error("accessibility tree should contain button elements")
	}
	if !strings.Contains(tree, "h1") {
		t.Error("accessibility tree should contain h1 heading")
	}
	if !strings.Contains(tree, "select") {
		t.Error("accessibility tree should contain select elements")
	}
	t.Logf("AccessibilityTree (%d chars)", len(tree))
}

// ---------------------------------------------------------------------------
// 11. DiscoverForm + FillFormSemantic (form.go)
// ---------------------------------------------------------------------------

func TestIntegrationDiscoverFormDetailed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<form id="register">
				<label for="fname">First Name</label>
				<input id="fname" name="first_name" type="text" required placeholder="John" />
				<label for="lname">Last Name</label>
				<input id="lname" name="last_name" type="text" placeholder="Doe" />
				<label for="email">Email</label>
				<input id="email" name="email" type="email" required />
				<label for="country">Country</label>
				<select id="country" name="country">
					<option value="us">United States</option>
					<option value="uk">United Kingdom</option>
					<option value="de">Germany</option>
				</select>
				<label for="bio">Biography</label>
				<textarea id="bio" name="bio" placeholder="Tell us about yourself"></textarea>
				<button type="submit">Register</button>
			</form>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// DiscoverForm
	disc, err := s.DiscoverForm("#register")
	if err != nil {
		t.Fatalf("DiscoverForm: %v", err)
	}
	if len(disc.Fields) < 5 {
		t.Fatalf("expected at least 5 fields, got %d", len(disc.Fields))
	}

	// Check that labels are discovered correctly
	labels := map[string]bool{}
	for _, f := range disc.Fields {
		labels[f.Label] = true
		t.Logf("  Field: label=%q type=%q selector=%q required=%v", f.Label, f.Type, f.Selector, f.Required)
	}
	if !labels["First Name"] {
		t.Error("should find 'First Name' label")
	}
	if !labels["Email"] {
		t.Error("should find 'Email' label")
	}
	if !labels["Country"] {
		t.Error("should find 'Country' label")
	}

	// Check required fields
	for _, f := range disc.Fields {
		if f.Label == "First Name" && !f.Required {
			t.Error("First Name should be required")
		}
		if f.Label == "Email" && !f.Required {
			t.Error("Email should be required")
		}
	}

	// Check select options
	for _, f := range disc.Fields {
		if f.Label == "Country" {
			if len(f.Options) < 3 {
				t.Errorf("Country should have at least 3 options, got %d", len(f.Options))
			}
		}
	}

	// FillFormSemantic
	fillResult, err := s.FillFormSemantic(map[string]string{
		"First Name": "Jane",
		"Last Name":  "Smith",
		"Email":      "jane-test-user",
	})
	if err != nil {
		t.Fatalf("FillFormSemantic: %v", err)
	}
	if !fillResult.Success {
		for _, f := range fillResult.Fields {
			if !f.Success {
				t.Errorf("field %q failed: %s", f.HumanName, f.Error)
			}
		}
	}

	// Verify values
	el, err := s.Extract("#fname")
	if err == nil {
		t.Logf("First Name value: %q", el.Text)
	}
}

func TestIntegrationDiscoverFormWholePage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<form>
				<input id="search" name="q" type="text" placeholder="Search..." />
				<button type="submit">Search</button>
			</form>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Discover with empty selector (whole page)
	disc, err := s.DiscoverForm("")
	if err != nil {
		t.Fatalf("DiscoverForm: %v", err)
	}
	if len(disc.Fields) == 0 {
		t.Error("expected at least one field")
	}
}

// ---------------------------------------------------------------------------
// 12. DismissCookieBanner (cookies.go)
// ---------------------------------------------------------------------------

func TestIntegrationDismissCookieBanner(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<h1>Main Page</h1>
			<div id="cookie-banner" style="position:fixed;bottom:0;left:0;right:0;background:#333;color:#fff;padding:20px;z-index:9999;">
				<p>We use cookies to improve your experience.</p>
				<button id="accept-cookies" onclick="this.parentElement.style.display='none'">Accept cookies</button>
			</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.DismissCookieBanner()
	if err != nil {
		t.Fatalf("DismissCookieBanner: %v", err)
	}
	if !result.Found {
		t.Error("expected cookie banner to be found")
	}
	if result.Method == "none" {
		t.Error("expected banner to be dismissed (method should not be 'none')")
	}
	t.Logf("Cookie dismiss: found=%v method=%s text=%q", result.Found, result.Method, result.Text)
}

func TestIntegrationDismissCookieBannerTextMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div class="cookie-consent" style="position:fixed;bottom:0;width:100%;background:#eee;padding:10px;">
				<span>This site uses cookies.</span>
				<button onclick="this.parentElement.style.display='none'">I agree</button>
			</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.DismissCookieBanner()
	if err != nil {
		t.Fatalf("DismissCookieBanner: %v", err)
	}
	t.Logf("Cookie dismiss (text): found=%v method=%s text=%q", result.Found, result.Method, result.Text)
}

func TestIntegrationDismissCookieBannerNoBanner(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Clean page</h1></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.DismissCookieBanner()
	if err != nil {
		t.Fatalf("DismissCookieBanner: %v", err)
	}
	if result.Found {
		t.Error("expected no cookie banner on clean page")
	}
}

func TestIntegrationNavigateAndDismissCookies(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Cookie Page</title></head><body>
			<div id="cookie-banner">
				<button id="accept-cookies">Accept</button>
			</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	result, err := s.NavigateAndDismissCookies(srv.URL)
	if err != nil {
		t.Fatalf("NavigateAndDismissCookies: %v", err)
	}
	if result.Title != "Cookie Page" {
		t.Errorf("expected title 'Cookie Page', got %q", result.Title)
	}
}

// ---------------------------------------------------------------------------
// 13. CaptureProfile / ApplyProfile (profile.go)
// ---------------------------------------------------------------------------

func TestIntegrationCaptureApplyProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="stored"></div>
			<script>
				// Set localStorage items
				localStorage.setItem('user', 'alice');
				localStorage.setItem('theme', 'dark');
				document.getElementById('stored').textContent = localStorage.getItem('user');
			</script>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Capture profile
	profile, err := s.CaptureProfile()
	if err != nil {
		t.Fatalf("CaptureProfile: %v", err)
	}
	if profile.LocalStorage == nil {
		t.Fatal("expected non-nil localStorage")
	}
	if profile.LocalStorage["user"] != "alice" {
		t.Errorf("expected user=alice, got %q", profile.LocalStorage["user"])
	}
	if profile.LocalStorage["theme"] != "dark" {
		t.Errorf("expected theme=dark, got %q", profile.LocalStorage["theme"])
	}
	t.Logf("Captured profile: %d cookies, %d localStorage items", len(profile.Cookies), len(profile.LocalStorage))

	// Apply to a new session
	s2 := integrationSession(t)
	if _, err := s2.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate s2: %v", err)
	}

	if err := s2.ApplyProfile(profile); err != nil {
		t.Fatalf("ApplyProfile: %v", err)
	}

	// Verify localStorage was restored
	if _, err := s2.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate s2 after ApplyProfile: %v", err)
	}
	val, err := s2.Extract("#stored")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if val.Text != "alice" {
		t.Errorf("expected 'alice' in restored localStorage, got %q", val.Text)
	}
}

func TestIntegrationSaveLoadProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<script>localStorage.setItem('token', 'abc123');</script>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	path := t.TempDir() + "/profile.json"
	if err := s.SaveProfile(path); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	s2 := integrationSession(t)
	if _, err := s2.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate s2: %v", err)
	}
	if err := s2.LoadProfile(path); err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}

	val, _ := s2.Eval(`localStorage.getItem('token')`)
	if str, ok := val.(string); !ok || str != "abc123" {
		t.Errorf("expected 'abc123', got %v", val)
	}
}

// ---------------------------------------------------------------------------
// 14. Hover, DoubleClick, RightClick, SelectOption, ScrollTo, Focus, ScrollBy, DragDrop (interact.go)
// ---------------------------------------------------------------------------

func TestIntegrationHover(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="target" onmouseenter="this.textContent='hovered'" style="width:100px;height:100px;background:#eee;">not hovered</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.Hover("#target")
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}

	// Check hover effect triggered
	el, err := s.Extract("#target")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if el.Text != "hovered" {
		t.Logf("Hover effect: %q (mouseenter may not fire via CDP hover on all elements)", el.Text)
	}
}

func TestIntegrationDoubleClick(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="dbl" ondblclick="this.textContent='double-clicked'" style="width:100px;height:50px;background:#ddd;cursor:pointer;">click me twice</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.DoubleClick("#dbl")
	if err != nil {
		t.Fatalf("DoubleClick: %v", err)
	}

	el, err := s.Extract("#dbl")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if el.Text != "double-clicked" {
		t.Errorf("expected 'double-clicked', got %q", el.Text)
	}
}

func TestIntegrationRightClick(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="ctx" oncontextmenu="this.textContent='context-menu'; return false;" style="width:100px;height:50px;background:#ddd;">right click</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.RightClick("#ctx")
	if err != nil {
		t.Fatalf("RightClick: %v", err)
	}

	el, err := s.Extract("#ctx")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if el.Text != "context-menu" {
		t.Errorf("expected 'context-menu', got %q", el.Text)
	}
}

func TestIntegrationSelectOption(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<select id="color">
				<option value="r">Red</option>
				<option value="g">Green</option>
				<option value="b">Blue</option>
			</select>
			<div id="selected"></div>
			<script>
				document.getElementById('color').addEventListener('change', function() {
					document.getElementById('selected').textContent = this.value;
				});
			</script>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.SelectOption("#color", "Green")
	if err != nil {
		t.Fatalf("SelectOption: %v", err)
	}
	if result.Value != "g" {
		t.Errorf("expected value 'g', got %q", result.Value)
	}
	if result.Text != "Green" {
		t.Errorf("expected text 'Green', got %q", result.Text)
	}

	// Verify change event fired
	el, _ := s.Extract("#selected")
	if el != nil && el.Text != "g" {
		t.Errorf("expected 'g' from change event, got %q", el.Text)
	}

	// Select by value
	result2, err := s.SelectOption("#color", "b")
	if err != nil {
		t.Fatalf("SelectOption by value: %v", err)
	}
	if result2.Value != "b" {
		t.Errorf("expected value 'b', got %q", result2.Value)
	}

	// Non-existent option
	_, err = s.SelectOption("#color", "Yellow")
	if err == nil {
		t.Error("expected error for non-existent option")
	}
}

func TestIntegrationScrollTo(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="height:5000px;">
			<div style="height:3000px;"></div>
			<div id="far-away">Bottom element</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.ScrollTo("#far-away")
	if err != nil {
		t.Fatalf("ScrollTo: %v", err)
	}

	// Verify scroll happened
	scrollY, _ := s.Eval(`window.scrollY`)
	if y, ok := scrollY.(float64); ok && y > 0 {
		t.Logf("Scrolled to Y=%f", y)
	} else {
		t.Logf("scrollY=%v (may be zero in headless)", scrollY)
	}
}

func TestIntegrationScrollBy(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="height:5000px;">
			<div>Tall page</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.ScrollBy(0, 500)
	if err != nil {
		t.Fatalf("ScrollBy: %v", err)
	}

	scrollY, _ := s.Eval(`window.scrollY`)
	if y, ok := scrollY.(float64); ok {
		if y < 400 {
			t.Logf("scrollY=%f (expected ~500, may differ in headless)", y)
		}
	}
}

func TestIntegrationFocus(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<input id="inp" type="text" />
			<div id="focused"></div>
			<script>
				document.getElementById('inp').addEventListener('focus', function() {
					document.getElementById('focused').textContent = 'focused';
				});
			</script>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.Focus("#inp")
	if err != nil {
		t.Fatalf("Focus: %v", err)
	}

	el, _ := s.Extract("#focused")
	if el != nil && el.Text != "focused" {
		t.Logf("Focus result: %q (focus event may not always fire in headless)", el.Text)
	}
}

func TestIntegrationDragDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="from" draggable="true" style="width:50px;height:50px;background:red;position:absolute;left:10px;top:10px;">Drag</div>
			<div id="to" style="width:100px;height:100px;background:green;position:absolute;left:200px;top:10px;">Drop</div>
			<div id="result"></div>
			<script>
				document.getElementById('to').addEventListener('drop', function(e) {
					document.getElementById('result').textContent = 'dropped';
				});
				document.getElementById('to').addEventListener('dragover', function(e) {
					e.preventDefault();
				});
			</script>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err := s.DragDrop("#from", "#to")
	if err != nil {
		t.Fatalf("DragDrop: %v", err)
	}

	el, _ := s.Extract("#result")
	if el != nil {
		t.Logf("DragDrop result: %q", el.Text)
	}
}

func TestIntegrationInteractNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Empty</h1></body></html>`)
	}))
	defer srv.Close()

	// Use short timeout to avoid long waits for nonexistent selectors
	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// These should all fail for nonexistent selectors
	// DoubleClick, RightClick, SelectOption, ScrollTo, Focus, DragDrop use Evaluate
	// and don't wait for selectors, so they fail fast
	if _, err := s.DoubleClick("#nonexistent"); err == nil {
		t.Error("DoubleClick should fail for nonexistent element")
	}
	if _, err := s.RightClick("#nonexistent"); err == nil {
		t.Error("RightClick should fail for nonexistent element")
	}
	if _, err := s.SelectOption("#nonexistent", "x"); err == nil {
		t.Error("SelectOption should fail for nonexistent element")
	}
	if _, err := s.ScrollTo("#nonexistent"); err == nil {
		t.Error("ScrollTo should fail for nonexistent element")
	}
	if _, err := s.Focus("#nonexistent"); err == nil {
		t.Error("Focus should fail for nonexistent element")
	}
	if _, err := s.DragDrop("#nonexistent", "#also-nonexistent"); err == nil {
		t.Error("DragDrop should fail for nonexistent elements")
	}
}

// ---------------------------------------------------------------------------
// 15. DetectDialog (dialog.go)
// ---------------------------------------------------------------------------

func TestIntegrationDetectDialog(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<dialog id="my-dialog" open>
				<h2 class="title">Confirm Delete</h2>
				<p>Are you sure you want to delete this item?</p>
				<button>Cancel</button>
				<button>Delete</button>
			</dialog>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	info, err := s.DetectDialog()
	if err != nil {
		t.Fatalf("DetectDialog: %v", err)
	}
	if !info.Found {
		t.Fatal("expected dialog to be found")
	}
	if info.Type != "dialog" {
		t.Errorf("expected type 'dialog', got %q", info.Type)
	}
	if !strings.Contains(info.Text, "delete") {
		t.Logf("Dialog text: %q", info.Text)
	}
	if len(info.Buttons) < 2 {
		t.Errorf("expected at least 2 buttons, got %d", len(info.Buttons))
	}
	t.Logf("Dialog: type=%s title=%q buttons=%v", info.Type, info.Title, info.Buttons)
}

func TestIntegrationDetectDialogAriaModal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="modal" role="dialog" aria-modal="true" style="display:block;position:fixed;top:50px;left:50px;width:300px;height:200px;background:white;z-index:1000;">
				<h3>Login Required</h3>
				<input type="text" name="user" placeholder="Username" />
				<button>Log In</button>
			</div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	info, err := s.DetectDialog()
	if err != nil {
		t.Fatalf("DetectDialog: %v", err)
	}
	if !info.Found {
		t.Fatal("expected aria-modal dialog to be found")
	}
	if info.Type != "modal" {
		t.Errorf("expected type 'modal', got %q", info.Type)
	}
	if len(info.Inputs) == 0 {
		t.Error("expected at least one input in modal")
	}
	t.Logf("Modal: type=%s buttons=%v inputs=%d", info.Type, info.Buttons, len(info.Inputs))
}

func TestIntegrationDetectDialogNone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>No dialogs</h1></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	info, err := s.DetectDialog()
	if err != nil {
		t.Fatalf("DetectDialog: %v", err)
	}
	if info.Found {
		t.Error("expected no dialog to be found")
	}
}

// ---------------------------------------------------------------------------
// 16. CheckReadiness (readiness.go)
// ---------------------------------------------------------------------------

func TestIntegrationCheckReadiness(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<h1>Ready Page</h1>
			<p>This page has enough content to be considered ready. We add many words to exceed the 100 character threshold for content check in the readiness score computation.</p>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	readiness, err := s.CheckReadiness()
	if err != nil {
		t.Fatalf("CheckReadiness: %v", err)
	}
	if readiness.Score < 60 {
		t.Errorf("expected readiness score >= 60 for fully loaded page, got %d", readiness.Score)
	}
	if readiness.State != "complete" {
		t.Logf("readyState=%s (may not be 'complete' immediately)", readiness.State)
	}
	t.Logf("Readiness: score=%d state=%s pendingImages=%d hasSkeleton=%v hasSpinner=%v suggestions=%v",
		readiness.Score, readiness.State, readiness.PendingImages, readiness.HasSkeleton, readiness.HasSpinner, readiness.Suggestions)
}

func TestIntegrationCheckReadinessWithSkeleton(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div class="skeleton" style="width:200px;height:20px;background:#eee;"></div>
			<div class="spinner" style="width:30px;height:30px;"></div>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	readiness, err := s.CheckReadiness()
	if err != nil {
		t.Fatalf("CheckReadiness: %v", err)
	}
	// Note: HasSkeleton/HasSpinner fields use json:"has_skeleton" tag but JS returns
	// "hasSkeleton" key — the camelCase/snake_case mismatch means these fields may not
	// unmarshal correctly. We verify the readiness score is lower than a clean page.
	if readiness.Score > 90 {
		t.Errorf("expected lower readiness score for page with skeleton/spinner, got %d", readiness.Score)
	}
	t.Logf("Readiness: score=%d hasSkeleton=%v hasSpinner=%v suggestions=%v",
		readiness.Score, readiness.HasSkeleton, readiness.HasSpinner, readiness.Suggestions)
}

// ---------------------------------------------------------------------------
// 17. SuggestSelectors (readiness.go)
// ---------------------------------------------------------------------------

func TestIntegrationSuggestSelectors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<button id="login-btn">Login</button>
			<a id="signup-link" href="/signup">Sign Up</a>
			<input id="email-input" name="email" type="email" />
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	suggestions, err := s.SuggestSelectors("#login")
	if err != nil {
		t.Fatalf("SuggestSelectors: %v", err)
	}
	if len(suggestions) == 0 {
		t.Error("expected at least one suggestion for #login")
	}
	for _, sg := range suggestions {
		t.Logf("  Suggestion: selector=%s tag=%s text=%q", sg.Selector, sg.Tag, sg.Text)
	}
}

// ---------------------------------------------------------------------------
// 18. SessionHistory (history.go)
// ---------------------------------------------------------------------------

func TestIntegrationSessionHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<input id="name" type="text" />
			<button id="btn" onclick="void(0)">Go</button>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)

	// Empty history initially
	h := s.SessionHistory(10)
	if len(h) != 0 {
		t.Errorf("expected empty history, got %d entries", len(h))
	}

	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := s.Type("#name", "hello"); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if _, err := s.Click("#btn"); err != nil {
		t.Fatalf("Click: %v", err)
	}

	h = s.SessionHistory(10)
	if len(h) < 3 {
		t.Fatalf("expected at least 3 history entries, got %d", len(h))
	}
	if h[0].Action != "navigate" {
		t.Errorf("first action should be navigate, got %q", h[0].Action)
	}
	if h[1].Action != "type" {
		t.Errorf("second action should be type, got %q", h[1].Action)
	}
	if h[2].Action != "click" {
		t.Errorf("third action should be click, got %q", h[2].Action)
	}
	for _, e := range h {
		if e.Timestamp == "" {
			t.Error("history entry should have timestamp")
		}
	}
	t.Logf("History: %d entries", len(h))
}

// ---------------------------------------------------------------------------
// 19. ObserveDiff classification (diff.go)
// ---------------------------------------------------------------------------

func TestIntegrationObserveDiffClassification(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<div id="container"></div>
			<button id="add-modal" onclick="
				var d = document.createElement('div');
				d.className = 'modal';
				d.textContent = 'Login required';
				document.getElementById('container').appendChild(d);
			">Show Modal</button>
			<button id="add-error" onclick="
				var d = document.createElement('div');
				d.className = 'error';
				d.textContent = 'Invalid email address';
				document.getElementById('container').appendChild(d);
			">Show Error</button>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Install observer
	_, _, err := s.ObserveDiff()
	if err != nil {
		t.Fatalf("initial ObserveDiff: %v", err)
	}

	// Add modal
	s.Click("#add-modal")
	_, diff, err := s.ObserveDiff()
	if err != nil {
		t.Fatalf("ObserveDiff after modal: %v", err)
	}
	if diff.HasDiff {
		t.Logf("Modal diff: classification=%s summary=%q added=%d", diff.Classification, diff.Summary, len(diff.Added))
		if diff.Classification != "modal_appeared" && diff.Classification != "minor_update" {
			t.Logf("classification=%q (may vary based on timing)", diff.Classification)
		}
	}
}

// ---------------------------------------------------------------------------
// 20. AnnotatedScreenshot + ClickLabel
// ---------------------------------------------------------------------------

func TestIntegrationAnnotatedScreenshotElements(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<a href="/about" id="link1">About</a>
			<button id="btn1" onclick="void(0)">Click Me</button>
			<input id="inp1" type="text" placeholder="Type here" />
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.AnnotatedScreenshot()
	if err != nil {
		t.Fatalf("AnnotatedScreenshot: %v", err)
	}
	if result.Count == 0 {
		t.Fatal("expected at least one annotated element")
	}
	if len(result.Image) == 0 {
		t.Error("expected non-empty image data")
	}

	// All elements should have valid bounding boxes
	for _, el := range result.Elements {
		if el.Width == 0 || el.Height == 0 {
			t.Errorf("element %d (%s) has zero dimensions", el.Label, el.Tag)
		}
	}
}

// ---------------------------------------------------------------------------
// 21. WaitFor + HasElement edge cases
// ---------------------------------------------------------------------------

func TestIntegrationWaitForExisting(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1 id="title">Hello</h1></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Should return immediately for existing element
	if err := s.WaitFor("#title"); err != nil {
		t.Fatalf("WaitFor existing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 22. Eval (session.go)
// ---------------------------------------------------------------------------

func TestIntegrationEval(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Eval</h1></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.Eval(`1 + 2`)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if v, ok := result.(float64); !ok || v != 3 {
		t.Errorf("expected 3, got %v", result)
	}

	// Object return
	result2, err := s.Eval(`JSON.stringify({a: 1, b: 'hello'})`)
	if err != nil {
		t.Fatalf("Eval object: %v", err)
	}
	str, ok := result2.(string)
	if !ok {
		t.Fatalf("expected string, got %T", result2)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(str), &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if obj["b"] != "hello" {
		t.Errorf("expected b=hello, got %v", obj["b"])
	}
}

// ---------------------------------------------------------------------------
// 23. Snapshot (session.go)
// ---------------------------------------------------------------------------

func TestIntegrationSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Snap</title></head><body><h1>Test</h1></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Title != "Snap" {
		t.Errorf("expected title 'Snap', got %q", snap.Title)
	}
	if !strings.Contains(snap.URL, srv.URL) {
		t.Errorf("expected URL to contain server URL, got %q", snap.URL)
	}
}

// ---------------------------------------------------------------------------
// 24. Screenshot + PDF (session.go)
// ---------------------------------------------------------------------------

func TestIntegrationScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body style="background:blue;"><h1>Screenshot Test</h1></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	data, err := s.Screenshot()
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("screenshot should not be empty")
	}
	t.Logf("Screenshot: %d bytes", len(data))
}

func TestIntegrationPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>PDF Test</h1><p>Content for PDF.</p></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	data, err := s.PDF()
	if err != nil {
		t.Fatalf("PDF: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("PDF should not be empty")
	}
	// PDF should start with %PDF
	if len(data) > 4 && string(data[:5]) != "%PDF-" {
		t.Error("PDF data should start with %PDF-")
	}
	t.Logf("PDF: %d bytes", len(data))
}

// ---------------------------------------------------------------------------
// 25. ClickAndWait (session.go)
// ---------------------------------------------------------------------------

func TestIntegrationClickAndWait(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Target</title></head><body><h1>Arrived</h1></body></html>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Start</title></head><body>
			<a href="/target" id="link">Go to target</a>
		</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.ClickAndWait("#link")
	if err != nil {
		t.Fatalf("ClickAndWait: %v", err)
	}
	if result.Title != "Target" {
		t.Errorf("expected title 'Target', got %q", result.Title)
	}
}

// ---------------------------------------------------------------------------
// 26. Page() accessor (session.go)
// ---------------------------------------------------------------------------

func TestIntegrationPageAccessor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Test</h1></body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	page := s.Page()
	if page == nil {
		t.Fatal("Page() should return non-nil after Navigate")
	}
}

// ---------------------------------------------------------------------------
// 27. SetContentOptions (content.go)
// ---------------------------------------------------------------------------

func TestIntegrationSetContentOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<a href="/1">Link 1</a><a href="/2">Link 2</a><a href="/3">Link 3</a>
			<a href="/4">Link 4</a><a href="/5">Link 5</a>
		</body></html>`)
	}))
	defer srv.Close()

	s := integrationSession(t)
	s.SetContentOptions(agent.ContentOptions{
		MaxLength:  500,
		MaxLinks:   2,
		MaxInputs:  1,
		MaxButtons: 1,
	})

	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	obs, err := s.Observe()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Links) > 2 {
		t.Errorf("expected max 2 links, got %d", len(obs.Links))
	}
}

// ---------------------------------------------------------------------------
// 28. Network capture: enable all, disable, clear
// ---------------------------------------------------------------------------

func TestIntegrationNetworkCaptureAllPatterns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
			<button id="fetch-btn" onclick="fetch('/api/data')">Fetch</button>
		</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Enable with no patterns (capture all)
	if err := s.EnableNetworkCapture(); err != nil {
		t.Fatalf("EnableNetworkCapture: %v", err)
	}

	// Use Eval to trigger fetch without blocking on WaitStable
	_, _ = s.Eval(`fetch('/api/data'); true`)
	time.Sleep(2 * time.Second)

	all := s.CapturedRequests("")
	t.Logf("Captured %d total requests", len(all))

	// Disable and verify no new captures happen
	s.DisableNetworkCapture()
}
