package agui

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"go.klarlabs.de/scout/agent"
)

// SessionManager maps thread IDs to browser sessions with idle cleanup.
type SessionManager struct {
	mu           sync.Mutex
	sessions     map[string]*sessionEntry
	timeout      time.Duration
	proxy        string
	done         chan struct{}
	cleanupDone  chan struct{}
	closeOnce    sync.Once
	closed       bool
	closeErr     error
	work         sync.WaitGroup
	newSession   func(agent.SessionConfig) (*agent.Session, error)
	closeSession func(*agent.Session) error
}

type sessionEntry struct {
	session  *agent.Session
	lastUsed time.Time
	ready    chan struct{}
	err      error
}

// NewSessionManager creates a manager that cleans up idle sessions.
func NewSessionManager(idleTimeout time.Duration, proxyServers ...string) *SessionManager {
	proxy := ""
	if len(proxyServers) > 0 {
		proxy = proxyServers[0]
	}
	sm := &SessionManager{
		sessions:     make(map[string]*sessionEntry),
		timeout:      idleTimeout,
		proxy:        proxy,
		done:         make(chan struct{}),
		cleanupDone:  make(chan struct{}),
		newSession:   agent.NewSession,
		closeSession: func(session *agent.Session) error { return session.Close() },
	}
	go sm.cleanup()
	return sm
}

// Get returns the session for a thread, creating one if needed.
func (sm *SessionManager) Get(threadID string) (*agent.Session, error) {
	sm.mu.Lock()
	if sm.closed {
		sm.mu.Unlock()
		return nil, fmt.Errorf("AG-UI session manager is closed")
	}

	if entry, ok := sm.sessions[threadID]; ok {
		entry.lastUsed = time.Now()
		ready := entry.ready
		if ready == nil {
			session := entry.session
			err := entry.err
			sm.mu.Unlock()
			return session, err
		}
		sm.mu.Unlock()
		<-ready
		return entry.session, entry.err
	}
	entry := &sessionEntry{lastUsed: time.Now(), ready: make(chan struct{})}
	sm.sessions[threadID] = entry
	ready := entry.ready
	sm.mu.Unlock()

	sm.mu.Lock()
	newSession := sm.newSession
	sm.mu.Unlock()
	s, err := newSession(agent.SessionConfig{
		Headless:    false,
		Timeout:     30 * time.Second,
		ProxyServer: sm.proxy,
	})

	sm.mu.Lock()
	entry.session = s
	entry.err = err
	entry.lastUsed = time.Now()
	entry.ready = nil
	if sm.closed && err == nil {
		entry.err = fmt.Errorf("AG-UI session manager closed during session startup")
	}
	if err != nil && sm.sessions[threadID] == entry {
		delete(sm.sessions, threadID)
	}
	close(ready)
	resultErr := entry.err
	sm.mu.Unlock()
	if resultErr != nil {
		return nil, resultErr
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

// Remove closes and forgets the session for a thread. An in-progress session
// startup is allowed to finish before the session is closed.
func (sm *SessionManager) Remove(threadID string) error {
	sm.mu.Lock()
	if sm.closed {
		sm.mu.Unlock()
		return fmt.Errorf("AG-UI session manager is closed")
	}
	entry, ok := sm.sessions[threadID]
	if !ok {
		sm.mu.Unlock()
		return nil
	}
	delete(sm.sessions, threadID)
	ready := entry.ready
	sm.work.Add(1)
	sm.mu.Unlock()
	defer sm.work.Done()

	if ready != nil {
		<-ready
	}
	if entry.session == nil {
		return entry.err
	}
	err := sm.closeSession(entry.session)
	sm.mu.Lock()
	sm.closeErr = errors.Join(sm.closeErr, err)
	sm.mu.Unlock()
	return err
}

// Close shuts down all sessions and stops the cleanup goroutine.
func (sm *SessionManager) Close() error {
	sm.closeOnce.Do(func() {
		sm.mu.Lock()
		sm.closed = true
		close(sm.done)
		sm.mu.Unlock()
		<-sm.cleanupDone

		sm.mu.Lock()
		entries := make([]*sessionEntry, 0, len(sm.sessions))
		ready := make([]<-chan struct{}, 0, len(sm.sessions))
		for id, entry := range sm.sessions {
			entries = append(entries, entry)
			ready = append(ready, entry.ready)
			delete(sm.sessions, id)
		}
		sm.mu.Unlock()
		sm.work.Wait()

		for i, entry := range entries {
			if ready[i] != nil {
				<-ready[i]
			}
			if entry.session != nil {
				sm.closeErr = errors.Join(sm.closeErr, sm.closeSession(entry.session))
			}
		}
	})
	return sm.closeErr
}

func (sm *SessionManager) closeExpired(now time.Time) {
	sm.mu.Lock()
	entries := make([]*sessionEntry, 0)
	for id, entry := range sm.sessions {
		if entry.ready == nil && now.Sub(entry.lastUsed) > sm.timeout {
			entries = append(entries, entry)
			delete(sm.sessions, id)
			sm.work.Add(1)
		}
	}
	sm.mu.Unlock()
	for _, entry := range entries {
		err := sm.closeSession(entry.session)
		sm.mu.Lock()
		sm.closeErr = errors.Join(sm.closeErr, err)
		sm.mu.Unlock()
		sm.work.Done()
	}
}

func (sm *SessionManager) cleanup() {
	defer close(sm.cleanupDone)
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-sm.done:
			return
		case <-ticker.C:
			sm.closeExpired(time.Now())
		}
	}
}
