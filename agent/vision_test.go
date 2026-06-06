package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func visionTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Vision Test</title></head>
<body>
  <a href="/about" id="about-link" style="display:inline-block;position:absolute;left:10px;top:10px;width:100px;height:30px;">About</a>
  <button id="action-btn" style="position:absolute;left:200px;top:50px;width:120px;height:40px;">Click Me</button>
  <input id="search" name="search" type="text" placeholder="Search..." style="position:absolute;left:10px;top:100px;width:200px;height:30px;" />
</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestHybridObserve(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := visionTestServer()
	defer ts.Close()

	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.HybridObserve()
	if err != nil {
		t.Fatalf("HybridObserve: %v", err)
	}

	if len(result.Screenshot) == 0 {
		t.Error("expected non-empty screenshot")
	}
	if result.Width == 0 || result.Height == 0 {
		t.Errorf("expected non-zero viewport: %dx%d", result.Width, result.Height)
	}
	if len(result.Elements) < 3 {
		t.Errorf("expected at least 3 elements, got %d", len(result.Elements))
	}

	found := map[string]bool{}
	for _, el := range result.Elements {
		found[el.Tag] = true
		if el.Width == 0 || el.Height == 0 {
			t.Errorf("element %d (%s) has zero size", el.Index, el.Tag)
		}
		if el.Selector == "" {
			t.Errorf("element %d (%s) has empty selector", el.Index, el.Tag)
		}
	}
	if !found["a"] {
		t.Error("expected to find an <a> element")
	}
	if !found["button"] {
		t.Error("expected to find a <button> element")
	}
	if !found["input"] {
		t.Error("expected to find an <input> element")
	}
}

func TestFindByCoordinates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := visionTestServer()
	defer ts.Close()

	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.HybridObserve()
	if err != nil {
		t.Fatalf("HybridObserve: %v", err)
	}

	var btnEl *agent.HybridElement
	for i := range result.Elements {
		if result.Elements[i].Tag == "button" {
			btnEl = &result.Elements[i]
			break
		}
	}
	if btnEl == nil {
		t.Fatal("no button element found in hybrid observe")
	}

	cx := int(btnEl.X + btnEl.Width/2)
	cy := int(btnEl.Y + btnEl.Height/2)

	found, err := s.FindByCoordinates(cx, cy)
	if err != nil {
		t.Fatalf("FindByCoordinates(%d, %d): %v", cx, cy, err)
	}
	if found.Tag != "button" {
		t.Errorf("expected button, got %s", found.Tag)
	}
	if found.Selector == "" {
		t.Error("expected non-empty selector")
	}
}

func TestFindByCoordinatesMiss(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := visionTestServer()
	defer ts.Close()

	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	_, err = s.FindByCoordinates(9999, 9999)
	if err == nil {
		t.Error("expected error for coordinates outside any element")
	}
}
