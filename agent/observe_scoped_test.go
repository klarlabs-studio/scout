package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSectionRootJS_landmarks(t *testing.T) {
	got := sectionRootJS([]string{"nav", "main", "footer"})
	for _, want := range []string{`"nav"`, `"main"`, `"footer"`, `[role=\"navigation\"]`, `[role=\"contentinfo\"]`, `[role=\"main\"]`, `#main`, `#content`, `.content`} {
		if !strings.Contains(got, want) {
			t.Errorf("sectionRootJS missing %q in %s", want, got)
		}
	}
}

func TestSectionRootJS_rawSelectorPassthrough(t *testing.T) {
	got := sectionRootJS([]string{"#sidebar", ".product-grid"})
	for _, want := range []string{`"#sidebar"`, `".product-grid"`} {
		if !strings.Contains(got, want) {
			t.Errorf("sectionRootJS dropped raw selector %q (got %s)", want, got)
		}
	}
}

func TestSectionRootJS_aliasesExpand(t *testing.T) {
	got := sectionRootJS([]string{"search"})
	if !strings.Contains(got, `[role=\"search\"]`) {
		t.Errorf("expected [role=search] alias in %s", got)
	}
}

func scopedTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Scoped Test</title></head>
<body>
  <nav>
    <a href="/about">About</a>
    <a href="/contact">Contact</a>
  </nav>
  <main id="main">
    <h1>Main heading</h1>
    <a href="/article-1">Article one</a>
    <a href="/article-2">Article two</a>
    <a href="/article-3">Article three</a>
    <button id="cta">Read more</button>
  </main>
  <footer>
    <a href="/privacy">Privacy</a>
    <a href="/terms">Terms</a>
  </footer>
</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestObserveScoped_landmarkFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := scopedTestServer()
	defer ts.Close()

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Scope to main only — nav and footer links must be filtered out.
	obs, err := s.ObserveScoped(ObserveOptions{Sections: []string{"main"}})
	if err != nil {
		t.Fatalf("ObserveScoped: %v", err)
	}
	for _, l := range obs.Links {
		if strings.HasPrefix(l.Href, "/about") || strings.HasPrefix(l.Href, "/contact") {
			t.Errorf("nav link leaked into main scope: %+v", l)
		}
		if strings.HasPrefix(l.Href, "/privacy") || strings.HasPrefix(l.Href, "/terms") {
			t.Errorf("footer link leaked into main scope: %+v", l)
		}
	}
	if len(obs.Links) != 3 {
		t.Errorf("expected 3 main-scope links, got %d (%+v)", len(obs.Links), obs.Links)
	}
	if len(obs.Buttons) != 1 {
		t.Errorf("expected 1 main-scope button, got %d", len(obs.Buttons))
	}
}

func TestObserveScoped_linksLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	ts := scopedTestServer()
	defer ts.Close()

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	obs, err := s.ObserveScoped(ObserveOptions{LinksLimit: 2})
	if err != nil {
		t.Fatalf("ObserveScoped: %v", err)
	}
	if len(obs.Links) != 2 {
		t.Errorf("links_limit=2 returned %d links: %+v", len(obs.Links), obs.Links)
	}
}
