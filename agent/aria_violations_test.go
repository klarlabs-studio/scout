package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func ariaTestServer(body string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, body)
	})
	return httptest.NewServer(mux)
}

func TestAriaViolations_catchesCommonIssues(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := ariaTestServer(`<!DOCTYPE html>
<html><head><title>Bad</title></head>
<body>
  <img src="/x.png">
  <button></button>
  <a href="/x"><img src="/icon.png"></a>
  <input type="text" name="email">
  <div id="dup"></div>
  <div id="dup"></div>
  <p>Long enough text body so the missing-main-landmark check fires. Lorem ipsum dolor sit amet consectetur. Lorem ipsum dolor sit amet consectetur. Lorem ipsum dolor sit amet consectetur. Lorem ipsum dolor sit amet consectetur.</p>
</body></html>`)
	defer ts.Close()

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	report, err := s.AriaViolations()
	if err != nil {
		t.Fatalf("AriaViolations: %v", err)
	}

	want := map[string]bool{}
	for _, v := range report.Critical {
		want[v.Rule] = true
	}
	for _, v := range report.Serious {
		want[v.Rule] = true
	}
	for _, v := range report.Moderate {
		want[v.Rule] = true
	}
	for _, v := range report.Minor {
		want[v.Rule] = true
	}

	for _, rule := range []string{"image-alt", "button-name", "link-name", "label", "duplicate-id", "html-has-lang", "landmark-one-main"} {
		if !want[rule] {
			t.Errorf("expected rule %q in report, got rules: %v (count %d)", rule, want, report.Count)
		}
	}
}

func TestAriaViolations_cleanPage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := ariaTestServer(`<!DOCTYPE html>
<html lang="en"><head><title>Good</title></head>
<body>
  <main>
    <img src="/x.png" alt="Logo">
    <button>OK</button>
    <a href="/about">About</a>
    <label for="email">Email</label>
    <input id="email" type="email" name="email">
  </main>
</body></html>`)
	defer ts.Close()

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	report, err := s.AriaViolations()
	if err != nil {
		t.Fatalf("AriaViolations: %v", err)
	}
	if report.Count != 0 {
		t.Errorf("expected 0 violations on clean page, got %d: %+v", report.Count, report)
	}
}
