package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// shadowFormHTML serves a small page with a custom element that
// puts its inputs inside an open shadow root — exactly the shape
// every Lit / Stencil / Web Components admin presents. Verifies
// FillFormSemantic + a piercing CSS click both reach into the
// shadow tree.
const shadowFormHTML = `<!DOCTYPE html>
<html>
<head><title>Shadow form</title></head>
<body>
  <my-form id="host"></my-form>
  <output id="echo"></output>
  <script>
    class MyForm extends HTMLElement {
      constructor() {
        super();
        const root = this.attachShadow({mode: 'open'});
        root.innerHTML = ` + "`" + `
          <form id="inner">
            <label>Email <input type="email" name="email" id="e"></label>
            <label>Password <input type="password" name="password" id="p"></label>
            <button type="submit" id="go">Sign in</button>
          </form>
        ` + "`" + `;
        root.querySelector('form').addEventListener('submit', e => {
          e.preventDefault();
          document.getElementById('echo').textContent =
            root.querySelector('#e').value + '|' + root.querySelector('#p').value;
        });
      }
    }
    customElements.define('my-form', MyForm);
  </script>
</body></html>`

// TestShadowDOM_FillFormSemantic_PiercesShadowRoots is the
// regression for v1.9.0 — discoverForm must walk shadow roots
// and the resulting selectors must round-trip through the fill
// path.
func TestShadowDOM_FillFormSemantic_PiercesShadowRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, shadowFormHTML)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	disc, derr := s.DiscoverForm("")
	if derr != nil {
		t.Fatalf("DiscoverForm: %v", derr)
	}
	t.Logf("discover: form=%q fields=%d", disc.FormSelector, len(disc.Fields))
	for _, f := range disc.Fields {
		t.Logf("  field selector=%q label=%q type=%q name=%q",
			f.Selector, f.Label, f.Type, f.Name)
	}

	out, err := s.FillFormSemantic(map[string]string{
		"Email":    "felix@example.com",
		"Password": "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("FillFormSemantic: %v", err)
	}
	if !out.Success {
		for _, f := range out.Fields {
			t.Logf("field %q: success=%v err=%q value=%q observed=%q",
				f.HumanName, f.Success, f.Error, f.Value, f.ValueObserved)
		}
		t.Fatal("expected success across both shadow-DOM fields")
	}

	// Click the submit button inside the shadow root via the
	// piercing selector fallback — proves the standard click
	// path benefits from the same shadow-walk.
	if _, err := s.Click("#go"); err != nil {
		t.Fatalf("Click submit (shadow): %v", err)
	}

	// The page-level <output> mirrors the form values; if the
	// fill landed correctly, both show up there.
	text, err := s.ReadableText()
	if err != nil {
		t.Fatalf("ReadableText: %v", err)
	}
	want := "felix@example.com|hunter2hunter2"
	if !strings.Contains(text, want) {
		t.Fatalf("expected output to contain %q, got %q", want, text)
	}
}
