package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func annotateHandleTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Handle Test</title></head>
<body>
  <button id="target" onclick="document.getElementById('out').textContent='clicked';">Target</button>
  <!-- Mutation source: pressing this prepends a new button BEFORE #target,
       so per-call label numbers shift but data-scout-handle stays put. -->
  <button id="mutate" onclick="document.body.insertBefore(Object.assign(document.createElement('button'),{textContent:'New'}), document.getElementById('target'));">Mutate</button>
  <div id="out"></div>
</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestClickByHandle_survivesDOMMutation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := annotateHandleTestServer()
	defer ts.Close()

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	annotated, err := s.AnnotatedScreenshot()
	if err != nil {
		t.Fatalf("AnnotatedScreenshot: %v", err)
	}

	var targetHandle string
	for _, el := range annotated.Elements {
		if el.Text == "Target" || el.Selector == "#target" {
			targetHandle = el.NodeHandle
			break
		}
	}
	if targetHandle == "" {
		t.Fatalf("no handle for Target button; elements=%+v", annotated.Elements)
	}
	if !strings.HasPrefix(targetHandle, "h_") {
		t.Errorf("handle %q should start with h_ prefix", targetHandle)
	}

	// Mutate the DOM — inserts a new button before the target so the
	// per-call label of "Target" would shift if it were rescanned.
	if _, err := s.Click("#mutate"); err != nil {
		t.Fatalf("Click mutate: %v", err)
	}

	// Click by handle — must still land on the original Target button.
	if _, err := s.ClickByHandle(targetHandle); err != nil {
		t.Fatalf("ClickByHandle after mutation: %v", err)
	}

	out, err := s.Extract("#out")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Text != "clicked" {
		t.Errorf("expected target's onclick to have fired, #out = %q", out.Text)
	}
}

func TestClickByHandle_staleReturnsStructuredError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := annotateHandleTestServer()
	defer ts.Close()

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := s.AnnotatedScreenshot(); err != nil {
		t.Fatalf("AnnotatedScreenshot: %v", err)
	}

	_, err = s.ClickByHandle("h_doesnotexist_42")
	if err == nil {
		t.Fatal("expected stale_handle error for nonexistent handle")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Errorf("error should mention stale handle, got: %v", err)
	}
}
