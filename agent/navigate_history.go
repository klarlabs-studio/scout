package agent

import (
	browse "go.klarlabs.de/scout"
)

// GoBack navigates to the previous entry in the browser history and returns the
// resulting page state. Returns an error if there is no previous entry.
func (s *Session) GoBack() (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	start, before := s.traceBeforeAction()
	if err := s.runWithRecovery("back", s.page.GoBack); err != nil {
		s.traceAfterAction(start, before, "back", "", "", "", err)
		return nil, err
	}
	res, _ := s.pageResult()
	s.traceAfterAction(start, before, "back", "", "", res.URL, nil)
	s.recordAction(Action{Type: "back"})
	s.addHistory("back", "", res.URL, "")
	s.diffInstalled = false
	return res, nil
}

// GoForward navigates to the next entry in the browser history and returns the
// resulting page state. Returns an error if there is no next entry.
func (s *Session) GoForward() (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	start, before := s.traceBeforeAction()
	if err := s.runWithRecovery("forward", s.page.GoForward); err != nil {
		s.traceAfterAction(start, before, "forward", "", "", "", err)
		return nil, err
	}
	res, _ := s.pageResult()
	s.traceAfterAction(start, before, "forward", "", "", res.URL, nil)
	s.recordAction(Action{Type: "forward"})
	s.addHistory("forward", "", res.URL, "")
	s.diffInstalled = false
	return res, nil
}

// Reload reloads the current page and returns the resulting page state.
// When ignoreCache is true a hard reload bypasses the browser cache.
func (s *Session) Reload(ignoreCache bool) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	start, before := s.traceBeforeAction()
	if err := s.runWithRecovery("reload", func() error { return s.page.Reload(ignoreCache) }); err != nil {
		s.traceAfterAction(start, before, "reload", "", "", "", err)
		return nil, err
	}
	res, _ := s.pageResult()
	s.traceAfterAction(start, before, "reload", "", "", res.URL, nil)
	s.recordAction(Action{Type: "reload"})
	s.addHistory("reload", "", res.URL, "")
	s.diffInstalled = false
	return res, nil
}

// ErrNoHistoryEntry is re-exported from the browse package so agent callers can
// detect "no previous/next page" without importing browse.
var ErrNoHistoryEntry = browse.ErrNoHistoryEntry
