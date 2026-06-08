//go:build integration

package agent

import "testing"

func TestInFrame_DefaultFalse(t *testing.T) {
	s := &Session{}
	if s.InFrame() {
		t.Error("InFrame() should be false for a new session")
	}
}

func TestInFrame_TrueWhenSet(t *testing.T) {
	s := &Session{frameID: "abc123", frameContextID: 42}
	if !s.InFrame() {
		t.Error("InFrame() should be true when frameID is set")
	}
}

func TestSwitchToMainFrame_ClearsFrameState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}

	s, err := NewSession(SessionConfig{Headless: true, AllowPrivateIPs: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = s.Close() }()

	s.mu.Lock()
	s.frameID = "test-frame"
	s.frameContextID = 99
	s.mu.Unlock()

	result, err := s.SwitchToMainFrame()
	if err != nil {
		t.Fatalf("SwitchToMainFrame: %v", err)
	}
	if result == nil {
		t.Fatal("SwitchToMainFrame returned nil result")
	}
	if s.InFrame() {
		t.Error("InFrame() should be false after SwitchToMainFrame")
	}
}
