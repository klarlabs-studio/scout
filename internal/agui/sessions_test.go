package agui

import (
	"testing"
	"time"
)

func TestNewSessionManager_InitializesMap(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	defer sm.Close()
	if sm.sessions == nil {
		t.Fatal("sessions map must be initialized")
	}
	if len(sm.sessions) != 0 {
		t.Errorf("expected empty session map, got %d entries", len(sm.sessions))
	}
}

func TestSessionManager_TouchUnknownThread_NoOp(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	defer sm.Close()
	// Should not panic or error.
	sm.Touch("nonexistent-thread")
}

func TestSessionManager_CloseEmpty(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	sm.Close() // should be safe to close empty manager
}
