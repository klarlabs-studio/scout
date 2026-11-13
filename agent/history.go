package agent

import (
	"context"
	"time"
)

// HistoryEntry records a single action in the session history.
type HistoryEntry struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Action       string `json:"action"`
	Selector     string `json:"selector,omitempty"`
	URL          string `json:"url,omitempty"`
	Result       string `json:"result,omitempty"`
	Timestamp    string `json:"timestamp"`
}

// SessionHistory returns the last N actions performed in this session.
// Provides conversation-aware context so agents don't lose track of what they've done.
func (s *Session) SessionHistory(n int) []HistoryEntry {
	result, _ := s.SessionHistoryContext(context.Background(), n)
	return result
}

// SessionHistoryContext returns recent actions while honoring ctx for
// session-lock queueing.
func (s *Session) SessionHistoryContext(ctx context.Context, n int) ([]HistoryEntry, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if n <= 0 || len(s.history) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if n > len(s.history) {
		n = len(s.history)
	}
	// Return the last N entries
	result := make([]HistoryEntry, n)
	copy(result, s.history[len(s.history)-n:])
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// addHistory appends an entry to the session history. Caller must hold s.mu.
// Keeps at most 20 entries to avoid unbounded growth.
func (s *Session) addHistory(action, selector, url, result string) {
	entry := HistoryEntry{
		ContentTrust: ContentTrustUntrustedPage,
		Action:       action,
		Selector:     selector,
		URL:          url,
		Result:       result,
		Timestamp:    time.Now().Format("15:04:05"),
	}
	s.history = append(s.history, entry)
	if len(s.history) > 20 {
		s.history = s.history[len(s.history)-20:]
	}
}
