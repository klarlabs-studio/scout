//go:build integration

package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func spaTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>SPA Test</title></head>
<body>
  <div id="app" x-data="{count: 0, name: 'test'}">
    <h1 id="title">SPA Page</h1>
    <button id="nav-btn" onclick="
      history.pushState({}, '', '/page2');
      document.getElementById('title').textContent = 'Page 2';
    ">Go to Page 2</button>
    <div id="event-target"></div>
  </div>
  <script>
    window.__INITIAL_STATE__ = {user: 'test', count: 42};
    window.__NEXT_DATA__ = {page: '/home', buildId: 'abc123', props: {pageProps: {items: [1,2,3]}}};
    window.htmx = {version: '1.9.0', config: {defaultSwapStyle: 'innerHTML'}};
    document.getElementById('event-target').addEventListener('my-event', function(e) {
      this.textContent = 'Event received: ' + JSON.stringify(e.detail);
    });
  </script>
</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestDetectedFrameworks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := spaTestServer()
	defer ts.Close()

	s, _ := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	defer s.Close()
	s.Navigate(ts.URL)

	frameworks, err := s.DetectedFrameworks()
	if err != nil {
		t.Fatalf("DetectedFrameworks: %v", err)
	}
	t.Logf("Detected frameworks: %v", frameworks)

	// Should detect at least htmx and nextjs (from our test globals)
	found := make(map[string]bool)
	for _, f := range frameworks {
		found[f] = true
	}
	if !found["htmx"] {
		t.Error("expected to detect htmx")
	}
	if !found["nextjs"] {
		t.Error("expected to detect nextjs")
	}
}

func TestWaitForSPA(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := spaTestServer()
	defer ts.Close()

	s, _ := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	defer s.Close()
	s.Navigate(ts.URL)

	if err := s.WaitForSPA(); err != nil {
		t.Fatalf("WaitForSPA: %v", err)
	}
}

func TestGetAppState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := spaTestServer()
	defer ts.Close()

	s, _ := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	defer s.Close()
	s.Navigate(ts.URL)

	state, err := s.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}

	t.Logf("App state keys: %v", keys(state))

	if _, ok := state["__INITIAL_STATE__"]; !ok {
		t.Error("expected __INITIAL_STATE__")
	}
	if _, ok := state["nextjs"]; !ok {
		t.Error("expected nextjs")
	}
	if _, ok := state["htmx"]; !ok {
		t.Error("expected htmx")
	}
}

func TestDispatchEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := spaTestServer()
	defer ts.Close()

	s, _ := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	defer s.Close()
	s.Navigate(ts.URL)

	err := s.DispatchEvent("#event-target", "my-event", map[string]any{
		"message": "hello from browse-go",
	})
	if err != nil {
		t.Fatalf("DispatchEvent: %v", err)
	}

	result, _ := s.Extract("#event-target")
	if result != nil {
		t.Logf("Event target text: %q", result.Text)
	}
}

func TestWaitForRouteChange(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := spaTestServer()
	defer ts.Close()

	s, _ := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	defer s.Close()
	s.Navigate(ts.URL)

	s.Click("#nav-btn")
	result, _ := s.Snapshot()
	t.Logf("After SPA nav: URL=%s Title=%s", result.URL, result.Title)
}

func keys(m map[string]any) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}
