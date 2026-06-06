package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func testServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Agent Test</title></head>
<body>
  <h1 id="title">Hello Agent</h1>
  <a href="/about">About Us</a>
  <a href="/contact">Contact</a>
  <!-- Card-style anchor: wraps image + heading, no direct text node.
       Exercises the link-text fallback chain (heading inside). -->
  <a href="/recipes/rye-70" class="card">
    <img src="/img/rye.jpg" alt="">
    <h3>70 % Rye Sourdough</h3>
  </a>
  <a href="/products/lievito-madre" aria-label="Lievito Madre starter kit">
    <img src="/img/lm.jpg" alt="">
  </a>
  <a href="/blog/kaisersemmel-2024"><img src="/img/sem.jpg" alt=""></a>
  <form>
    <input id="name" name="name" type="text" placeholder="Your name" />
    <input id="email" name="email" type="email" placeholder="Email" />
    <button type="submit">Submit</button>
  </form>
  <table id="data">
    <thead><tr><th>Name</th><th>Role</th></tr></thead>
    <tbody>
      <tr><td>Alice</td><td>Engineer</td></tr>
      <tr><td>Bob</td><td>Designer</td></tr>
    </tbody>
  </table>
  <button id="action" onclick="document.getElementById('title').textContent='Updated!'">Action</button>
</body>
</html>`)
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>About</title></head><body><h1>About Page</h1></body></html>`)
	})
	return httptest.NewServer(mux)
}

func newSession(t *testing.T) *agent.Session {
	t.Helper()
	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNavigate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	result, err := s.Navigate(ts.URL)
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if result.Title != "Agent Test" {
		t.Errorf("title: expected 'Agent Test', got %q", result.Title)
	}
}

func TestObserve(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	obs, err := s.Observe()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if obs.Title != "Agent Test" {
		t.Errorf("title: %q", obs.Title)
	}
	if len(obs.Links) < 2 {
		t.Errorf("expected at least 2 links, got %d", len(obs.Links))
	}
	if len(obs.Inputs) < 2 {
		t.Errorf("expected at least 2 inputs, got %d", len(obs.Inputs))
	}
	if len(obs.Buttons) < 2 {
		t.Errorf("expected at least 2 buttons, got %d", len(obs.Buttons))
	}
	if obs.Interactive == 0 {
		t.Error("expected interactive > 0")
	}

	t.Logf("Observation: %d links, %d inputs, %d buttons, %d interactive",
		len(obs.Links), len(obs.Inputs), len(obs.Buttons), obs.Interactive)
}

func TestObserveLinkTextFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	obs, err := s.Observe()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	got := make(map[string]string, len(obs.Links))
	for _, l := range obs.Links {
		got[l.Href] = l.Text
	}

	cases := []struct {
		href string
		want string
		why  string
	}{
		{"/recipes/rye-70", "70 % Rye Sourdough", "heading inside anchor"},
		{"/products/lievito-madre", "Lievito Madre starter kit", "aria-label on anchor"},
		// No heading, no aria-label, no img alt → slug fallback.
		{"/blog/kaisersemmel-2024", "kaisersemmel 2024", "url slug"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			text, ok := got[tc.href]
			if !ok {
				t.Fatalf("href %q not in Links %+v", tc.href, obs.Links)
			}
			if text != tc.want {
				t.Errorf("href %q text = %q, want %q", tc.href, text, tc.want)
			}
		})
	}
}

func TestClick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	_, err := s.Click("#action")
	if err != nil {
		t.Fatalf("Click: %v", err)
	}

	result, err := s.Extract("#title")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Text != "Updated!" {
		t.Errorf("expected 'Updated!', got %q", result.Text)
	}
}

func TestType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	result, err := s.Type("#name", "Alice")
	if err != nil {
		t.Fatalf("Type: %v", err)
	}
	if result.Value != "Alice" {
		t.Errorf("value: expected 'Alice', got %q", result.Value)
	}
}

func TestFillForm(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	result, err := s.FillForm(map[string]string{
		"#name":  "Alice",
		"#email": "alice-test-user",
	})
	if err != nil {
		t.Fatalf("FillForm: %v", err)
	}
	if !result.Success {
		t.Error("expected success")
	}
	if len(result.Fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(result.Fields))
	}
}

func TestExtractTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	table, err := s.ExtractTable("#data")
	if err != nil {
		t.Fatalf("ExtractTable: %v", err)
	}
	if table.ColCount != 2 {
		t.Errorf("expected 2 columns, got %d", table.ColCount)
	}
	if table.RowCount != 2 {
		t.Errorf("expected 2 rows, got %d", table.RowCount)
	}
	if len(table.Rows) > 0 && table.Rows[0][0] != "Alice" {
		t.Errorf("first cell: expected 'Alice', got %q", table.Rows[0][0])
	}
}

func TestExtractAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	result, err := s.ExtractAll("a")
	if err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}
	if result.Count < 2 {
		t.Errorf("expected at least 2 links, got %d", result.Count)
	}
}

func TestHasElement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	if !s.HasElement("#title") {
		t.Error("expected #title to exist")
	}
	if s.HasElement("#nonexistent") {
		t.Error("expected #nonexistent to not exist")
	}
}
