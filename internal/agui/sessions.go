package agui

import (
	"sync"
	"time"

	"go.klarlabs.de/scout/agent"
)

// SessionManager maps thread IDs to browser sessions with idle cleanup.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*sessionEntry
	timeout  time.Duration
	done     chan struct{}
}

type sessionEntry struct {
	session  *agent.Session
	lastUsed time.Time
}

// NewSessionManager creates a manager that cleans up idle sessions.
func NewSessionManager(idleTimeout time.Duration) *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*sessionEntry),
		timeout:  idleTimeout,
		done:     make(chan struct{}),
	}
	go sm.cleanup()
	return sm
}

// Get returns the session for a thread, creating one if needed.
func (sm *SessionManager) Get(threadID string) (*agent.Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if entry, ok := sm.sessions[threadID]; ok {
		entry.lastUsed = time.Now()
		return entry.session, nil
	}

	s, err := agent.NewSession(agent.SessionConfig{
		Headless: false,
		Timeout:  30 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	sm.sessions[threadID] = &sessionEntry{
		session:  s,
		lastUsed: time.Now(),
	}
	return s, nil
}

// Touch updates the last-used timestamp for a thread session.
func (sm *SessionManager) Touch(threadID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if entry, ok := sm.sessions[threadID]; ok {
		entry.lastUsed = time.Now()
	}
}

// Close shuts down all sessions and stops the cleanup goroutine.
func (sm *SessionManager) Close() {
	close(sm.done)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for id, entry := range sm.sessions {
		_ = entry.session.Close()
		delete(sm.sessions, id)
	}
}

func (sm *SessionManager) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-sm.done:
			return
		case <-ticker.C:
			sm.mu.Lock()
			now := time.Now()
			for id, entry := range sm.sessions {
				if now.Sub(entry.lastUsed) > sm.timeout {
					_ = entry.session.Close()
					delete(sm.sessions, id)
				}
			}
			sm.mu.Unlock()
		}
	}
}
