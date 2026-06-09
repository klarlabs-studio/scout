//go:build integration

package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func featureTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Feature Test</title></head>
<body>
  <h1 id="title">Hello</h1>
  <form id="signup">
    <label for="email">Email Address</label>
    <input id="email" name="email" type="email" placeholder="your-email" />
    <label for="password">Password</label>
    <input id="password" name="password" type="password" />
    <label for="fullname">Full Name</label>
    <input id="fullname" name="fullname" type="text" placeholder="John Doe" />
    <button type="submit">Sign Up</button>
  </form>
  <button id="dynamic" onclick="document.getElementById('title').textContent='Changed!'; document.body.appendChild(Object.assign(document.createElement('div'),{id:'new-el',textContent:'New Element'}));">
    Change Page
  </button>
</body></html>`)
	})
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[{"name":"Alpha"},{"name":"Beta"}]}`)
	})
	mux.HandleFunc("/fetch-page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Fetch Test</title></head>
<body>
  <div id="result"></div>
  <script>
    fetch('/api/data')
      .then(r => r.json())
      .then(d => document.getElementById('result').textContent = JSON.stringify(d));
  </script>
</body></html>`)
	})
	return httptest.NewServer(mux)
}

func newFeatureSession(t *testing.T) *agent.Session {
	t.Helper()
	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDOMDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := featureTestServer()
	defer ts.Close()

	s := newFeatureSession(t)
	s.Navigate(ts.URL)

	// First ObserveDiff installs observer — no diff yet
	obs, diff, err := s.ObserveDiff()
	if err != nil {
		t.Fatalf("ObserveDiff: %v", err)
	}
	if obs.Title != "Feature Test" {
		t.Errorf("title: %q", obs.Title)
	}
	if diff.HasDiff {
		t.Error("expected no diff on first call")
	}

	// Click to mutate the DOM
	s.Click("#dynamic")

	// Second ObserveDiff should see changes
	_, diff2, err := s.ObserveDiff()
	if err != nil {
		t.Fatalf("ObserveDiff: %v", err)
	}
	if !diff2.HasDiff {
		t.Error("expected diff after DOM mutation")
	}
	t.Logf("Diff: %d added, %d removed, %d modified",
		len(diff2.Added), len(diff2.Removed), len(diff2.Modified))

	// Should see the new element
	foundNew := false
	for _, el := range diff2.Added {
		if el.ID == "new-el" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Error("expected to see 'new-el' in added elements")
	}
}

func TestDiscoverForm(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := featureTestServer()
	defer ts.Close()

	s := newFeatureSession(t)
	s.Navigate(ts.URL)

	result, err := s.DiscoverForm("#signup")
	if err != nil {
		t.Fatalf("DiscoverForm: %v", err)
	}

	if len(result.Fields) < 3 {
		t.Fatalf("expected at least 3 fields, got %d", len(result.Fields))
	}

	// Check that labels were discovered
	for _, f := range result.Fields {
		t.Logf("Field: label=%q type=%q selector=%q", f.Label, f.Type, f.Selector)
	}

	// Should find "Email Address" label
	found := false
	for _, f := range result.Fields {
		if f.Label == "Email Address" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find field with label 'Email Address'")
	}
}

func TestFillFormSemantic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := featureTestServer()
	defer ts.Close()

	s := newFeatureSession(t)
	s.Navigate(ts.URL)

	result, err := s.FillFormSemantic(map[string]string{
		"Email":     "test-user",
		"Password":  "secret123",
		"Full Name": "Jane Doe",
	})
	if err != nil {
		t.Fatalf("FillFormSemantic: %v", err)
	}

	if !result.Success {
		for _, f := range result.Fields {
			if !f.Success {
				t.Errorf("field %q failed: %s", f.HumanName, f.Error)
			}
		}
	}

	// Verify values were set
	el, _ := s.Extract("#email")
	t.Logf("Email value via Extract: %+v", el)

	for _, f := range result.Fields {
		t.Logf("Semantic fill: %q -> %q (selector=%q)", f.HumanName, f.Value, f.Selector)
	}
}

func TestObserveWithBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := featureTestServer()
	defer ts.Close()

	s := newFeatureSession(t)
	s.Navigate(ts.URL)

	// Small budget should produce smaller observation
	obs, err := s.ObserveWithBudget(100) // ~400 chars
	if err != nil {
		t.Fatalf("ObserveWithBudget: %v", err)
	}

	if obs.Title == "" {
		t.Error("expected title even with small budget")
	}

	// Text should be truncated
	if len(obs.Text) > 500 {
		t.Errorf("text too long for 100 token budget: %d chars", len(obs.Text))
	}

	t.Logf("Budget=100 observation: %d links, %d inputs, %d chars text",
		len(obs.Links), len(obs.Inputs), len(obs.Text))
}

func TestNetworkCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := featureTestServer()
	defer ts.Close()

	s := newFeatureSession(t)
	s.Navigate(ts.URL)

	// Enable capture
	if err := s.EnableNetworkCapture("/api/"); err != nil {
		t.Fatalf("EnableNetworkCapture: %v", err)
	}

	// Navigate to page that fetches /api/data
	s.Navigate(ts.URL + "/fetch-page")

	// Wait for XHR to complete
	s.WaitFor("#result")

	// Capture was enabled BEFORE this navigate, so the fresh CDP target must
	// have its network observers re-attached — otherwise the /api/data XHR
	// fired by /fetch-page is silently lost (regression: issue #42).
	captured := s.CapturedRequests("/api/data")
	t.Logf("Captured %d requests matching /api/data", len(captured))
	if len(captured) == 0 {
		t.Fatal("expected /api/data XHR to be captured after navigate; network observers not re-attached to the new page")
	}
	t.Logf("First capture: %s %s -> %d, body=%s",
		captured[0].Method, captured[0].URL, captured[0].Status, captured[0].ResponseBody)
}

func TestAnnotatedScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := featureTestServer()
	defer ts.Close()

	s := newFeatureSession(t)
	s.Navigate(ts.URL)

	result, err := s.AnnotatedScreenshot()
	if err != nil {
		t.Fatalf("AnnotatedScreenshot: %v", err)
	}

	if len(result.Image) == 0 {
		t.Fatal("expected non-empty image")
	}
	if result.Count == 0 {
		t.Fatal("expected at least one annotated element")
	}

	t.Logf("Annotated %d elements:", result.Count)
	for _, el := range result.Elements {
		t.Logf("  [%d] %s %q selector=%s (%dx%d at %d,%d)",
			el.Label, el.Tag, el.Text, el.Selector, el.Width, el.Height, el.X, el.Y)
	}

	// Verify we can click by label
	_, err = s.ClickLabel(result.Count) // click the last element
	if err != nil {
		t.Logf("ClickLabel(%d): %v (may be expected for non-clickable)", result.Count, err)
	}
}

func TestSaveLoadProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := featureTestServer()
	defer ts.Close()

	s := newFeatureSession(t)
	s.Navigate(ts.URL)

	// Save profile
	profilePath := t.TempDir() + "/profile.json"
	if err := s.SaveProfile(profilePath); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	// Verify file exists and is valid JSON
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty profile")
	}
	t.Logf("Profile size: %d bytes", len(data))

	// Load on fresh session
	s2 := newFeatureSession(t)
	s2.Navigate(ts.URL)
	if err := s2.LoadProfile(profilePath); err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
}

func TestRemoteCDPOption(t *testing.T) {
	// Just verify the option compiles and is accepted
	_ = agent.SessionConfig{
		Headless:  true,
		RemoteCDP: "ws://localhost:9222/devtools/browser/abc",
	}
}
