//go:build integration

package agent_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNavigateHistory_BackForwardReload drives a real browser through two pages
// and verifies GoBack, GoForward, and Reload move the document as expected.
func TestNavigateHistory_BackForwardReload(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/one", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>Page One</h1></body></html>`))
	})
	mux.HandleFunc("/two", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>Page Two</h1></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := integrationSession(t)

	if _, err := s.Navigate(srv.URL + "/one"); err != nil {
		t.Fatalf("navigate one: %v", err)
	}
	if _, err := s.Navigate(srv.URL + "/two"); err != nil {
		t.Fatalf("navigate two: %v", err)
	}

	back, err := s.GoBack()
	if err != nil {
		t.Fatalf("GoBack: %v", err)
	}
	if !strings.HasSuffix(back.URL, "/one") {
		t.Errorf("GoBack URL = %q, want suffix /one", back.URL)
	}

	fwd, err := s.GoForward()
	if err != nil {
		t.Fatalf("GoForward: %v", err)
	}
	if !strings.HasSuffix(fwd.URL, "/two") {
		t.Errorf("GoForward URL = %q, want suffix /two", fwd.URL)
	}

	rel, err := s.Reload(false)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !strings.HasSuffix(rel.URL, "/two") {
		t.Errorf("Reload URL = %q, want suffix /two", rel.URL)
	}
}

// TestNavigateHistory_NoEntry verifies GoForward at the end of history returns
// ErrNoHistoryEntry rather than hanging or silently succeeding.
func TestNavigateHistory_NoEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>Only</h1></body></html>`))
	}))
	defer srv.Close()

	s := integrationSession(t)
	if _, err := s.Navigate(srv.URL); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	if _, err := s.GoForward(); err == nil {
		t.Error("expected GoForward at end of history to error, got nil")
	}
}
