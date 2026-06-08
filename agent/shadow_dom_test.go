//go:build integration

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

// shadowClickHTML wires a counter inside a shadow root so we can
// prove dispatch_event(click) routes through the shadow boundary.
const shadowClickHTML = `<!DOCTYPE html>
<html>
<head><title>Shadow click</title></head>
<body>
  <my-counter></my-counter>
  <output id="echo">0</output>
  <script>
    class MyCounter extends HTMLElement {
      constructor() {
        super();
        const root = this.attachShadow({mode: 'open'});
        root.innerHTML = '<button id="tick">tick</button>';
        let n = 0;
        root.querySelector('#tick').addEventListener('click', () => {
          n++;
          document.getElementById('echo').textContent = String(n);
        });
      }
    }
    customElements.define('my-counter', MyCounter);
  </script>
</body></html>`

// TestShadowDOM_DispatchEvent_PiercesShadowRoots verifies the
// dispatch_event MCP tool can reach into shadow roots via the
// same deep-find helper the wait/discovery paths use.
func TestShadowDOM_DispatchEvent_PiercesShadowRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, shadowClickHTML)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Dispatch two clicks via the shadow-piercing path. Each one
	// must bump the in-shadow counter that updates the page-level
	// <output>.
	for i := 0; i < 2; i++ {
		if err := s.DispatchEvent("#tick", "click", nil); err != nil {
			t.Fatalf("DispatchEvent click #%d: %v", i+1, err)
		}
	}

	text, err := s.ReadableText()
	if err != nil {
		t.Fatalf("ReadableText: %v", err)
	}
	if !strings.Contains(text, "2") {
		t.Fatalf("expected counter to read 2, got %q", text)
	}
}

// TestShadowDOM_AnnotatedScreenshot_EnumeratesShadowChildren
// verifies the annotation pass picks up interactive elements that
// live inside shadow roots. Without piercing the element count
// would be 0; with piercing we see the button.
func TestShadowDOM_AnnotatedScreenshot_EnumeratesShadowChildren(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, shadowClickHTML)
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	out, err := s.AnnotatedScreenshot()
	if err != nil {
		t.Fatalf("AnnotatedScreenshot: %v", err)
	}
	var sawShadowButton bool
	for _, el := range out.Elements {
		if el.Tag == "button" && strings.Contains(el.Text, "tick") {
			sawShadowButton = true
			break
		}
	}
	if !sawShadowButton {
		t.Fatalf("expected to annotate the shadow-root button, got %d elements: %+v", len(out.Elements), out.Elements)
	}
}
