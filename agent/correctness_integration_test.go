//go:build integration

package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func correctnessTestServer(body string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Correctness</title></head><body>%s</body></html>`, body)
	})
	return httptest.NewServer(mux)
}

func newCorrectnessSession(t *testing.T) *agent.Session {
	t.Helper()
	s, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestClickText_AmbiguousIdenticalButtons guards the fix for the dedupe that
// collapsed distinct elements: several identical-text buttons with no id/name
// must be reported as ambiguous, not silently resolved to the first one.
func TestClickText_AmbiguousIdenticalButtons(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := correctnessTestServer(`
		<button>Add to cart</button>
		<button>Add to cart</button>
		<button>Add to cart</button>`)
	defer ts.Close()

	s := newCorrectnessSession(t)
	s.Navigate(ts.URL)

	res, err := s.ClickText("Add to cart", "")
	if err == nil {
		t.Fatalf("expected an ambiguity error for 3 identical buttons, got nil (result=%+v)", res)
	}
	if !strings.Contains(err.Error(), "elements match") {
		t.Errorf("error should report multiple matches, got: %v", err)
	}
	if res == nil || len(res.Candidates) != 3 {
		t.Errorf("expected 3 distinct candidates, got %+v", res)
	}
}

// TestClickText_SingleMatchClicks confirms the identity dedupe doesn't over-block
// a genuinely unique match.
func TestClickText_SingleMatchClicks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := correctnessTestServer(`<button id="only">Checkout</button>`)
	defer ts.Close()

	s := newCorrectnessSession(t)
	s.Navigate(ts.URL)

	res, err := s.ClickText("Checkout", "")
	if err != nil {
		t.Fatalf("ClickText on a single unique match: %v", err)
	}
	if res.MatchType != "button_text" {
		t.Errorf("expected button_text match, got %q", res.MatchType)
	}
}

// TestFillFormSemantic_RadioNoMatchFails guards the fix for fillRadio silently
// selecting the fuzzy-matched radio when the requested value matches no option.
func TestFillFormSemantic_RadioNoMatchFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := correctnessTestServer(`
		<form id="prefs">
		  <fieldset>
		    <legend>Gender</legend>
		    <label><input type="radio" name="gender" id="gender-male" value="male"> Male</label>
		    <label><input type="radio" name="gender" id="gender-female" value="female"> Female</label>
		  </fieldset>
		</form>`)
	defer ts.Close()

	s := newCorrectnessSession(t)
	s.Navigate(ts.URL)

	res, err := s.FillFormSemantic(map[string]string{"Gender": "Nonbinary"})
	if err != nil {
		t.Fatalf("FillFormSemantic: %v", err)
	}
	radio := radioFieldResult(t, res)
	if radio.Success {
		t.Errorf("radio fill for a non-existent option should fail, got Success=true (value=%q, error=%q)", radio.Value, radio.Error)
	}
	if radio.Error == "" {
		t.Errorf("expected an explanatory error for the unmatched radio value, got none")
	}
}

// TestFillFormSemantic_RadioMatchSucceeds confirms a value that names a real
// option still selects it.
func TestFillFormSemantic_RadioMatchSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := correctnessTestServer(`
		<form id="prefs">
		  <fieldset>
		    <legend>Gender</legend>
		    <label><input type="radio" name="gender" id="gender-male" value="male"> Male</label>
		    <label><input type="radio" name="gender" id="gender-female" value="female"> Female</label>
		  </fieldset>
		</form>`)
	defer ts.Close()

	s := newCorrectnessSession(t)
	s.Navigate(ts.URL)

	res, err := s.FillFormSemantic(map[string]string{"Gender": "Female"})
	if err != nil {
		t.Fatalf("FillFormSemantic: %v", err)
	}
	radio := radioFieldResult(t, res)
	if !radio.Success {
		t.Errorf("radio fill for a real option should succeed, got error=%q", radio.Error)
	}
}

func radioFieldResult(t *testing.T, res *agent.SemanticFillResult) agent.SemanticFieldResult {
	t.Helper()
	for _, f := range res.Fields {
		if strings.EqualFold(f.Type, "radio") {
			return f
		}
	}
	t.Fatalf("no radio field in fill result: %+v", res.Fields)
	return agent.SemanticFieldResult{}
}
