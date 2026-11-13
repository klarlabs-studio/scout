package agui

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
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

func TestSessionManagerConcurrentGetSharesStartup(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	sm.mu.Lock()
	sm.newSession = func(agent.SessionConfig) (*agent.Session, error) {
		calls.Add(1)
		close(started)
		<-release
		return &agent.Session{}, nil
	}
	sm.mu.Unlock()

	const callers = 2
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session, err := sm.Get("shared-thread")
			if err == nil && session == nil {
				err = errors.New("Get returned nil session without an error")
			}
			results <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("session startup did not begin")
	}
	// Both callers must share the in-progress entry; give the second lookup a
	// chance to block on the same readiness channel before releasing creation.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("session factory calls = %d, want exactly one", got)
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSessionManagerSlowStartupDoesNotBlockOtherThread(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	sm.mu.Lock()
	sm.newSession = func(agent.SessionConfig) (*agent.Session, error) {
		close(started)
		<-release
		return &agent.Session{}, nil
	}
	sm.mu.Unlock()

	slowResult := make(chan error, 1)
	go func() {
		_, err := sm.Get("slow-thread")
		slowResult <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("slow session startup did not begin")
	}

	// Make the unrelated thread's startup return immediately while the first
	// call remains blocked. The registry lock must not span startup.
	sm.mu.Lock()
	sm.newSession = func(agent.SessionConfig) (*agent.Session, error) {
		return &agent.Session{}, nil
	}
	sm.mu.Unlock()
	otherResult := make(chan error, 1)
	go func() {
		session, err := sm.Get("other-thread")
		if err == nil && session == nil {
			err = errors.New("unrelated Get returned nil session without an error")
		}
		otherResult <- err
	}()
	select {
	case err := <-otherResult:
		if err != nil {
			close(release)
			t.Fatalf("unrelated Get: %v", err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("slow startup blocked an unrelated thread lookup")
	}
	close(release)
	if err := <-slowResult; err != nil {
		t.Fatalf("slow Get: %v", err)
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSessionManagerCloseWaitsForStartupAndRejectsNewGets(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	sm.mu.Lock()
	sm.newSession = func(agent.SessionConfig) (*agent.Session, error) {
		close(started)
		<-release
		return &agent.Session{}, nil
	}
	sm.mu.Unlock()

	getResult := make(chan error, 1)
	go func() {
		_, err := sm.Get("starting-thread")
		getResult <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("session startup did not begin")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- sm.Close() }()
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned before startup finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := sm.Get("late-thread"); err == nil {
		t.Fatal("Get accepted a new thread after shutdown began")
	}
	close(release)
	if err := <-getResult; err == nil {
		t.Fatal("startup completing during shutdown should be rejected")
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
}

func TestSessionManagerCloseReportsSessionCleanupError(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	sentinel := errors.New("injected browser cleanup failure")
	sm.mu.Lock()
	sm.newSession = func(agent.SessionConfig) (*agent.Session, error) { return &agent.Session{}, nil }
	sm.closeSession = func(*agent.Session) error { return sentinel }
	sm.mu.Unlock()
	if _, err := sm.Get("cleanup-error-thread"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := sm.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("Close error = %v, want wrapped cleanup error", err)
	}
}

func TestSessionManagerRemoveClosesSessionAndAllowsFreshSession(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	var closed atomic.Int32
	sm.mu.Lock()
	sm.newSession = func(agent.SessionConfig) (*agent.Session, error) { return &agent.Session{}, nil }
	sm.closeSession = func(*agent.Session) error { closed.Add(1); return nil }
	sm.mu.Unlock()
	if _, err := sm.Get("clear-thread"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := sm.Remove("clear-thread"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("closed sessions = %d, want 1", got)
	}
	if _, ok := sm.sessions["clear-thread"]; ok {
		t.Fatal("removed thread remains in session map")
	}
	if _, err := sm.Get("clear-thread"); err != nil {
		t.Fatalf("Get after Remove: %v", err)
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := closed.Load(); got != 2 {
		t.Fatalf("closed sessions after fresh session cleanup = %d, want 2", got)
	}
}

func TestSessionManagerRemoveWaitsForStartup(t *testing.T) {
	sm := NewSessionManager(time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	var closed atomic.Int32
	sm.mu.Lock()
	sm.newSession = func(agent.SessionConfig) (*agent.Session, error) {
		close(started)
		<-release
		return &agent.Session{}, nil
	}
	sm.closeSession = func(*agent.Session) error { closed.Add(1); return nil }
	sm.mu.Unlock()
	getDone := make(chan error, 1)
	go func() { _, err := sm.Get("starting-clear-thread"); getDone <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("session startup did not begin")
	}
	removeDone := make(chan error, 1)
	go func() { removeDone <- sm.Remove("starting-clear-thread") }()
	select {
	case err := <-removeDone:
		t.Fatalf("Remove returned before startup finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-getDone; err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := <-removeDone; err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("closed sessions = %d, want 1", got)
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
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
