//go:build integration

package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func submitOutcomeTestServer() *httptest.Server {
	mux := http.NewServeMux()
	// Form that runs a client-side validation, fails it, and surfaces
	// an inline [role=alert] without firing a network request.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Submit Outcome Test</title></head>
<body>
  <form id="login" onsubmit="event.preventDefault(); document.getElementById('err').textContent = 'Password too short'; document.getElementById('pw').setAttribute('aria-invalid','true'); return false;">
    <label for="email">Email</label>
    <input id="email" name="email" type="email" required>
    <label for="pw">Password</label>
    <input id="pw" name="password" type="password">
    <small id="err" role="alert"></small>
    <button type="submit" id="submit">Sign in</button>
  </form>
</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestSubmitOutcome_capturesDefaultPreventedAndAlerts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := submitOutcomeTestServer()
	defer ts.Close()

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Type into both fields (the form's onsubmit fires preventDefault
	// regardless of validity — we just need a submit to happen).
	if _, err := s.Type("#email", "user@example.com"); err != nil {
		t.Fatalf("Type email: %v", err)
	}
	if _, err := s.Type("#pw", "short"); err != nil {
		t.Fatalf("Type pw: %v", err)
	}
	if _, err := s.Click("#submit"); err != nil {
		t.Fatalf("Click submit: %v", err)
	}

	out, err := s.LastSubmitOutcome()
	if err != nil {
		t.Fatalf("LastSubmitOutcome: %v", err)
	}

	if !out.TrackerInstalled {
		t.Error("expected tracker_installed = true after Navigate")
	}
	if !out.DefaultPrevented {
		t.Errorf("expected default_prevented=true, got %+v", out)
	}
	if out.SubmittedForm == "" {
		t.Errorf("expected submitted_form to identify the form, got empty (%+v)", out)
	}
	if len(out.AlertsVisible) == 0 {
		t.Errorf("expected alerts_visible to include the inline error, got %+v", out)
	} else if !strings.Contains(out.AlertsVisible[0], "Password too short") {
		t.Errorf("alert text = %q, want to contain 'Password too short'", out.AlertsVisible[0])
	}
	if len(out.AriaInvalidFields) == 0 {
		t.Errorf("expected aria_invalid_fields to include password, got %+v", out)
	}
	if out.NavigationCommitted {
		t.Errorf("expected navigation_committed=false (form preventDefault'd), got %+v", out)
	}
	if out.XHRCount != 0 {
		t.Errorf("expected xhr_count=0 (no network), got %d", out.XHRCount)
	}
}
