package agent

import (
	"context"
	"errors"

	browse "go.klarlabs.de/scout"
)

// GoBack navigates to the previous entry in browser history.
func (s *Session) GoBack() (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.GoBackContext(ctx)
}

// GoBackContext navigates to the previous history entry under ctx.
func (s *Session) GoBackContext(ctx context.Context) (*PageResult, error) {
	return s.historyActionContext(ctx, "navigated_back", "back", func(p *browse.Page) error { return p.GoBackContext(ctx) })
}

// GoForward navigates to the next entry in browser history.
func (s *Session) GoForward() (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.GoForwardContext(ctx)
}

// GoForwardContext navigates to the next history entry under ctx.
func (s *Session) GoForwardContext(ctx context.Context) (*PageResult, error) {
	return s.historyActionContext(ctx, "navigated_forward", "forward", func(p *browse.Page) error { return p.GoForwardContext(ctx) })
}

// Reload reloads the current page and returns the resulting page state.
func (s *Session) Reload(ignoreCache bool) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ReloadContext(ctx, ignoreCache)
}

// ReloadContext reloads the current page under ctx.
func (s *Session) ReloadContext(ctx context.Context, ignoreCache bool) (*PageResult, error) {
	return s.historyActionContext(ctx, "reloaded", "reload", func(p *browse.Page) error { return p.ReloadContext(ctx, ignoreCache) })
}

func (s *Session) historyActionContext(ctx context.Context, resultAction, traceAction string, run func(*browse.Page) error) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	if err := run(s.page); err != nil {
		var unknown *browse.NavigationOutcomeUnknownError
		if errors.As(err, &unknown) {
			outcomeErr := s.actionOutcomeAfterDispatch(traceAction, unknown.URL, "history navigation may have been dispatched, but page load could not be confirmed", err)
			s.traceAfterActionContext(ctx, start, before, traceAction, "", "", unknown.URL, outcomeErr)
			return nil, outcomeErr
		}
		s.traceAfterActionContext(ctx, start, before, traceAction, "", "", "", err)
		return nil, err
	}
	result, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch(traceAction, "", "history navigation completed but resulting page state could not be read", err)
	}
	result.Action = resultAction
	result.Target = result.URL
	result.Outcome = "navigation_completed"
	result.Verified = true
	result.Evidence = "document URL and title read after browser history navigation; final URL: " + result.URL
	s.traceAfterActionContext(ctx, start, before, traceAction, "", "", result.URL, nil)
	result.OperationOrdinal = s.operationOrdinalForResult(traceAction, result.URL)
	result.Receipt = s.newActionReceipt(traceAction, result.URL, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult(traceAction, result.URL, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
	s.recordAction(Action{Type: traceAction})
	s.addHistory(traceAction, "", result.URL, "")
	s.diffInstalled = false
	return result, nil
}

// ErrNoHistoryEntry is re-exported from the browse package so agent callers can
// detect "no previous/next page" without importing browse.
var ErrNoHistoryEntry = browse.ErrNoHistoryEntry
