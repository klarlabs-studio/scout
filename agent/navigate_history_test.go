package agent

import (
	"errors"
	"testing"

	browse "go.klarlabs.de/scout"
)

// A session whose page has never been initialized must surface a clear error
// rather than panicking on a nil page. ensurePage returns an error when the
// session was constructed without a browser (the test-session case).
func TestGoBack_NoPageReturnsError(t *testing.T) {
	s := newTestSession()
	s.closed = true // forces ensurePage to return an error deterministically
	if _, err := s.GoBack(); err == nil {
		t.Fatal("expected error when no live page, got nil")
	}
}

func TestGoForward_NoPageReturnsError(t *testing.T) {
	s := newTestSession()
	s.closed = true
	if _, err := s.GoForward(); err == nil {
		t.Fatal("expected error when no live page, got nil")
	}
}

func TestReload_NoPageReturnsError(t *testing.T) {
	s := newTestSession()
	s.closed = true
	if _, err := s.Reload(false); err == nil {
		t.Fatal("expected error when no live page, got nil")
	}
}

// ErrNoHistoryEntry must be re-exported and identical to the browse sentinel so
// callers can detect "nowhere to go" without importing the browse package.
func TestErrNoHistoryEntry_ReExported(t *testing.T) {
	if !errors.Is(ErrNoHistoryEntry, browse.ErrNoHistoryEntry) {
		t.Fatal("agent.ErrNoHistoryEntry must alias browse.ErrNoHistoryEntry")
	}
}
