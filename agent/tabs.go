package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	browse "go.klarlabs.de/scout"
)

// tabEntry holds a named tab/page.
type tabEntry struct {
	name      string
	page      *browse.Page
	lastURL   string
	lastTitle string
}

// tabManager manages multiple named tabs within a session.
// Synchronization is provided by the enclosing Session.mu — tabManager
// methods are only ever called with that lock held.
type tabManager struct {
	tabs   map[string]*tabEntry
	active string
}

func newTabManager() *tabManager {
	return &tabManager{
		tabs: make(map[string]*tabEntry),
	}
}

// TabInfo describes an open tab.
type TabInfo struct {
	ContentTrust    string `json:"content_trust,omitempty"`
	Name            string `json:"name"`
	TargetID        string `json:"target_id,omitempty"`
	URL             string `json:"url"`
	Title           string `json:"title"`
	VisibilityState string `json:"visibility_state,omitempty"`
	MetadataStale   bool   `json:"metadata_stale,omitempty"`
	Active          bool   `json:"active"`
}

// ensureTabRendererContext checks that a tab's renderer can answer promptly.
// Browser-level activation can wake a discarded renderer without issuing a
// renderer-bound command to the sleeping target. Activation also changes the
// browser's selected target; callers track that even if the follow-up page
// probe cannot confirm responsiveness.
func ensureTabRendererContext(ctx context.Context, page *browse.Page) (rendererRecoveryState, error) {
	return ensureRendererResponsiveContext(ctx,
		func(probeCtx context.Context) error {
			_, err := page.CallContext(probeCtx, "Runtime.evaluate", map[string]any{"expression": "1", "returnByValue": true})
			return err
		},
		page.ActivateTargetContext,
	)
}

type rendererRecoveryState uint8

const (
	rendererAlreadyResponsive rendererRecoveryState = iota
	rendererRecovered
	rendererActivatedUnconfirmed
	rendererActivationUnknown
)

func ensureRendererResponsiveContext(ctx context.Context, probe func(context.Context) error, activate func(context.Context) error) (rendererRecoveryState, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	probeErr := probe(probeCtx)
	cancel()
	if probeErr == nil {
		return rendererAlreadyResponsive, nil
	}
	if ctx.Err() != nil {
		return rendererAlreadyResponsive, ctx.Err()
	}
	// A caller may pass a context without its own deadline. Keep browser-level
	// activation bounded as well as the renderer probes so recovery cannot pin
	// the session lock indefinitely on an unresponsive CDP endpoint.
	activateCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	if err := activate(activateCtx); err != nil {
		cancel()
		if mutationOutcomeMayBeUnknown(err) {
			return rendererActivationUnknown, fmt.Errorf("tab renderer did not respond and target activation outcome is unknown: %w", err)
		}
		return rendererAlreadyResponsive, fmt.Errorf("tab renderer did not respond and target activation failed: %w", err)
	}
	cancel()
	probeCtx, cancel = context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if err := probe(probeCtx); err != nil {
		return rendererActivatedUnconfirmed, fmt.Errorf("tab renderer did not respond after activation: %w", err)
	}
	return rendererRecovered, nil
}

// OpenTab creates a new named tab and navigates to the URL.
// The new tab becomes the active tab.
func (s *Session) OpenTab(name, url string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.OpenTabContext(ctx, name, url)
}

// OpenTabContext creates and navigates a named tab within the caller's deadline.
func (s *Session) OpenTabContext(ctx context.Context, name, url string) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	ctx = beginExpectedTargetTransition(ctx)

	if s.tabs == nil {
		s.tabs = newTabManager()
		// Store the current page as "default" tab
		entry := &tabEntry{name: "default", page: s.page}
		if current, err := s.pageResultForPageContext(ctx, s.page); err == nil {
			entry.lastURL, entry.lastTitle = current.URL, current.Title
		}
		s.tabs.tabs["default"] = entry
		s.tabs.active = "default"
	}

	if _, exists := s.tabs.tabs[name]; exists {
		return nil, fmt.Errorf("tab %q already exists", name)
	}
	traceStart, traceBefore := s.traceBeforeActionContext(ctx)

	var page *browse.Page
	var err error
	if contextual, ok := s.browser.(interface {
		NewPageContext(context.Context) (*browse.Page, error)
	}); ok {
		page, err = contextual.NewPageContext(ctx)
	} else {
		page, err = s.browser.NewPage()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open tab at %s: %w", url, err)
	}
	if err := s.applyPageURLPolicy(page); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cleanupErr := page.CloseContext(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "open_tab", name, "tab target was created but URL policy setup failed and cleanup could not confirm it was closed", errors.Join(err, cleanupErr))
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "open_tab", name, url, "", outcomeErr)
		}
		return nil, err
	}
	if err := s.restoreFrameRouteLocked(ctx); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cleanupErr := page.CloseContext(cleanupCtx)
		cancel()
		return nil, errors.Join(fmt.Errorf("detach selected frame before opening tab: %w", err), cleanupErr)
	}
	previousPage := s.page
	s.page = page
	committed := false
	defer func() {
		if committed {
			return
		}
		// Opening is transactional from Scout's active-page perspective. If
		// navigation or readback fails, return to the previous target and
		// restore its observer before releasing the session lock.
		s.page = previousPage
		if s.network != nil {
			if err := s.reattachNetworkObserversLocked(); err != nil {
				s.markNetworkObserverUnavailableLocked()
			}
		}
	}()
	// Capture the new tab's document and early application requests. The page
	// must be the observer target before NavigateContext dispatches navigation.
	if s.network != nil {
		if err := s.reattachNetworkObserversLocked(); err != nil {
			s.markNetworkObserverUnavailableLocked()
		}
	}
	if err := page.NavigateContext(ctx, url); err != nil {
		_ = s.traceActionFailureContext(ctx, traceStart, traceBefore, "open_tab", name, url, "", err)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cleanupErr := page.CloseContext(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "open_tab", name, "tab navigation failed or may still be in progress; cleanup could not confirm the uncommitted target was closed", errors.Join(err, cleanupErr))
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "open_tab", name, url, "", outcomeErr)
		}
		return nil, fmt.Errorf("failed to open tab at %s: %w", url, err)
	}
	result, err := s.pageResultForPageContext(ctx, page)
	if err != nil {
		_ = s.traceActionFailureContext(ctx, traceStart, traceBefore, "open_tab", name, url, "", err)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cleanupErr := page.CloseContext(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "open_tab", name, "tab navigation completed but target readback failed and cleanup could not confirm the uncommitted target was closed", errors.Join(err, cleanupErr))
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "open_tab", name, url, "", outcomeErr)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cleanupErr := page.CloseContext(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "open_tab", name, "tab navigation completed after the caller deadline; cleanup could not confirm the uncommitted tab was closed", errors.Join(err, cleanupErr))
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "open_tab", name, url, "", outcomeErr)
		}
		_ = s.traceActionFailureContext(ctx, traceStart, traceBefore, "open_tab", name, url, "", err)
		return nil, err
	}
	// A transient pre-navigation attach failure may have cleared by the time
	// the document commits. Retry now, retaining the known-gap marker set by
	// markNetworkObserverUnavailableLocked when explicit capture was active.
	if s.network != nil {
		s.network.mu.Lock()
		observersInstalled := s.network.observersInstalled
		s.network.mu.Unlock()
		if !observersInstalled {
			if err := s.reattachNetworkObserversLocked(); err != nil {
				s.markNetworkObserverUnavailableLocked()
			}
		}
	}

	s.tabs.tabs[name] = &tabEntry{name: name, page: page, lastURL: result.URL, lastTitle: result.Title}
	s.tabs.active = name
	s.page = page
	s.diffInstalled = false
	// Frame execution contexts belong to a page target. The new tab starts in
	// its main frame; never carry the previous target's frame ID across tabs.
	s.frameID = ""
	s.frameContextID = 0
	committed = true
	s.traceAfterActionContext(ctx, traceStart, traceBefore, "open_tab", name, url, result.URL, nil)

	result.Action = "opened_tab"
	result.Target = name
	result.Outcome = string(OutcomePostconditionVerified)
	result.Verified = true
	result.Evidence = fmt.Sprintf("new tab %q became active at %s (target %s)", name, result.URL, result.TargetID)
	result.OperationOrdinal = s.operationOrdinalForResult("open_tab", name)
	result.Receipt = s.newActionReceipt("open_tab", name, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("open_tab", name, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true)
	return result, nil
}

// SwitchTab activates a named tab.
func (s *Session) SwitchTab(name string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SwitchTabContext(ctx, name)
}

// SwitchTabContext activates a named tab within the caller's deadline.
func (s *Session) SwitchTabContext(ctx context.Context, name string) (*PageResult, error) {
	return s.SwitchTabForTargetContext(ctx, name, "")
}

// SwitchTabForTargetContext activates a named tab only when it still refers to
// the expected browser target. This binds navigation to the destination the
// caller inspected, even if tab names are reused.
func (s *Session) SwitchTabForTargetContext(ctx context.Context, name, expectedTabTargetID string) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}
	ctx = beginExpectedTargetTransition(ctx)

	if s.tabs == nil {
		return nil, fmt.Errorf("no tabs open")
	}

	tab, ok := s.tabs.tabs[name]
	if !ok {
		return nil, fmt.Errorf("tab %q not found", name)
	}
	if expectedTabTargetID != "" && tab.page.TargetID() != expectedTabTargetID {
		return nil, &OperationError{
			Phase:         "tab_target_binding",
			Cause:         "stale_target",
			TargetID:      tab.page.TargetID(),
			Detail:        fmt.Sprintf("tab %q now refers to target %s, expected %s; inspect the tab list before retrying", name, tab.page.TargetID(), expectedTabTargetID),
			OriginalError: "tab target identity mismatch",
		}
	}
	traceStart, traceBefore := s.traceBeforeActionContext(ctx)
	page := tab.page
	recoveryState, err := ensureTabRendererContext(ctx, page)
	rebound := false
	if err != nil {
		// Chrome replaces the CDP target ID when it urgently discards a page.
		// The old Page session cannot be awakened; rebind only when the last
		// observed URL/title identify exactly one replacement in our context.
		targetChecker, ok := s.browser.(interface {
			HasTargetContext(context.Context, string) (bool, error)
		})
		if !ok {
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", err)
		}
		inspectCtx, inspectCancel := context.WithTimeout(ctx, time.Second)
		exists, inspectErr := targetChecker.HasTargetContext(inspectCtx, page.TargetID())
		inspectCancel()
		if inspectErr != nil {
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", inspectErr)
		}
		if !exists {
			page, err = s.reattachDiscardedTabContext(ctx, tab)
			if err != nil {
				return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", err)
			}
			if err := s.applyPageURLPolicy(page); err != nil {
				cleanupErr := s.cleanupUninitializedPage(page, false)
				return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", errors.Join(err, cleanupErr))
			}
			tab.page = page
			rebound = true
			recoveryState, err = ensureTabRendererContext(ctx, page)
		}
	}
	if err != nil {
		if recoveryState == rendererActivatedUnconfirmed || recoveryState == rendererActivationUnknown {
			if routeErr := s.selectTabLocked(ctx, name, tab.page); routeErr != nil {
				return nil, routeErr
			}
			if observerErr := s.reattachNetworkObserversLocked(); observerErr != nil {
				s.markNetworkObserverUnavailableLocked()
			}
			detail := "browser accepted target activation, but renderer responsiveness could not be confirmed"
			if recoveryState == rendererActivationUnknown {
				detail = "target activation may have been dispatched; selected tab could not be confirmed"
			}
			outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "switch_tab", name, detail, err)
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", outcomeErr)
		}
		return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", fmt.Errorf("failed to prepare tab %q: %w", name, err))
	}
	// Keep the CDP target and Scout's logical active tab in sync. In a real
	// browser, actions against a background target can be dropped or behave
	// differently for visibility-sensitive controls.
	if _, err := page.CallContext(ctx, "Page.bringToFront", nil); err != nil {
		if recoveryState != rendererAlreadyResponsive {
			if routeErr := s.selectTabLocked(ctx, name, page); routeErr != nil {
				return nil, routeErr
			}
			if observerErr := s.reattachNetworkObserversLocked(); observerErr != nil {
				s.markNetworkObserverUnavailableLocked()
			}
			outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "switch_tab", name, "browser target was activated, but page activation could not be confirmed", err)
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", outcomeErr)
		}
		if mutationOutcomeMayBeUnknown(err) {
			if routeErr := s.selectTabLocked(ctx, name, page); routeErr != nil {
				return nil, routeErr
			}
			if observerErr := s.reattachNetworkObserversLocked(); observerErr != nil {
				s.markNetworkObserverUnavailableLocked()
			}
			outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "switch_tab", name, "tab activation may have been dispatched; active target could not be confirmed", err)
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", outcomeErr)
		}
		return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", fmt.Errorf("failed to activate tab %q: %w", name, err))
	}
	s.collectTraceURLPolicyDecisions()
	// The browser accepted the activation command. Update the selected target
	// before readback so a failed inspection cannot leave Scout routing later
	// actions to the previously active page.
	tab.page = page
	if err := s.selectTabLocked(ctx, name, page); err != nil {
		return nil, err
	}
	if err := s.reattachNetworkObserversLocked(); err != nil {
		s.markNetworkObserverUnavailableLocked()
	}

	result, err := s.pageResultForPageContext(ctx, page)
	if err != nil {
		outcomeErr := s.actionOutcomeAfterDispatch("switch_tab", name, "tab activation was accepted; target state could not be read back", err)
		return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "switch_tab", name, "", "", outcomeErr)
	}
	s.traceAfterActionContext(ctx, traceStart, traceBefore, "switch_tab", name, "", result.URL, nil)

	result.Action = "switched_tab"
	result.Target = name
	result.Outcome = string(OutcomePostconditionVerified)
	result.Verified = true
	result.Evidence = fmt.Sprintf("tab %q is Scout's active page at %s (target %s)", name, result.URL, result.TargetID)
	if recoveryState == rendererRecovered {
		result.Evidence += "; target activation restored renderer responsiveness"
	}
	if rebound {
		result.Evidence += "; reattached to the unique browser replacement for the previously observed tab"
	}
	result.OperationOrdinal = s.operationOrdinalForResult("switch_tab", name)
	result.Receipt = s.newActionReceipt("switch_tab", name, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("switch_tab", name, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true)
	return result, nil
}

func (s *Session) selectTabLocked(ctx context.Context, name string, page *browse.Page) error {
	if err := s.restoreFrameRouteLocked(ctx); err != nil {
		return fmt.Errorf("detach selected frame before switching tabs: %w", err)
	}
	s.tabs.active = name
	s.page = page
	s.diffInstalled = false
	// Frame execution contexts belong to a page target. Switching tabs resets
	// frame selection so reads and actions cannot target stale frame state.
	s.frameID = ""
	s.frameContextID = 0
	return nil
}

// CloseTabResult closes an inactive named tab and returns structured evidence.
func (s *Session) CloseTabResult(name string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.CloseTabResultContext(ctx, name)
}

// CloseTab closes a named tab. Cannot close the active tab.
func (s *Session) CloseTab(name string) error {
	_, err := s.CloseTabResult(name)
	return err
}

// CloseTabContext closes an inactive named tab within the caller's deadline.
func (s *Session) CloseTabContext(ctx context.Context, name string) error {
	_, err := s.CloseTabResultContext(ctx, name)
	return err
}

// CloseTabResultContext closes an inactive named tab and verifies it was
// removed from Scout's tab registry before returning structured evidence.
func (s *Session) CloseTabResultContext(ctx context.Context, name string) (*PageResult, error) {
	return s.CloseTabResultForTargetContext(ctx, name, "")
}

// CloseTabResultForTargetContext closes an inactive named tab only when it
// still refers to the expected browser target. This binds the destructive
// operation to the target the caller inspected, even if the name was reused.
func (s *Session) CloseTabResultForTargetContext(ctx context.Context, name, expectedTabTargetID string) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}

	if s.tabs == nil {
		return nil, fmt.Errorf("no tabs open")
	}

	if name == s.tabs.active {
		return nil, fmt.Errorf("cannot close active tab %q — switch to another tab first", name)
	}

	tab, ok := s.tabs.tabs[name]
	if !ok {
		return nil, fmt.Errorf("tab %q not found", name)
	}
	if expectedTabTargetID != "" && tab.page.TargetID() != expectedTabTargetID {
		return nil, &OperationError{
			Phase:         "tab_target_binding",
			Cause:         "stale_target",
			TargetID:      tab.page.TargetID(),
			Detail:        fmt.Sprintf("tab %q now refers to target %s, expected %s; inspect the tab list before retrying", name, tab.page.TargetID(), expectedTabTargetID),
			OriginalError: "tab target identity mismatch",
		}
	}
	traceStart, traceBefore := s.traceBeforeActionContext(ctx)

	if err := tab.page.CloseContext(ctx); err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			outcomeErr := s.actionOutcomeAfterDispatchForPage(tab.page, "close_tab", name, "tab close may have been dispatched; tab state could not be confirmed", err, Postcondition{TargetAbsent: tab.page.TargetID()})
			return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "close_tab", name, "", "", outcomeErr)
		}
		return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "close_tab", name, "", "", err)
	}
	targetID := tab.page.TargetID()
	if checker, ok := s.browser.(interface {
		HasTargetContext(context.Context, string) (bool, error)
	}); ok {
		// Target.closeTarget may be acknowledged before Chrome publishes the
		// target-detached event in its browser-wide target inventory. Wait for
		// that postcondition so callers never treat a still-live browser tab as
		// closed merely because Scout has stopped tracking it locally.
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			present, checkErr := checker.HasTargetContext(waitCtx, targetID)
			if checkErr != nil {
				outcomeErr := s.actionOutcomeAfterDispatchForPage(tab.page, "close_tab", name, "tab close was acknowledged but browser target absence could not be confirmed", checkErr, Postcondition{TargetAbsent: targetID})
				return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "close_tab", name, "", "", outcomeErr)
			}
			if !present {
				break
			}
			select {
			case <-waitCtx.Done():
				cause := fmt.Errorf("browser target %s remained present after close acknowledgement: %w", targetID, waitCtx.Err())
				outcomeErr := s.actionOutcomeAfterDispatchForPage(tab.page, "close_tab", name, "tab close was acknowledged but browser target is still present", cause, Postcondition{TargetAbsent: targetID})
				return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "close_tab", name, "", "", outcomeErr)
			case <-ticker.C:
			}
		}
	}
	if s.tracing && s.trace != nil {
		s.collectPageTraceURLPolicyDecisions(tab.page)
	}
	delete(s.tabs.tabs, name)
	if _, remains := s.tabs.tabs[name]; remains {
		outcomeErr := s.actionOutcomeAfterDispatchForPage(tab.page, "close_tab", name, "tab close completed but the tab remained in Scout's registry", fmt.Errorf("tab %q is still registered", name))
		return nil, s.traceActionFailureContext(ctx, traceStart, traceBefore, "close_tab", name, "", "", outcomeErr)
	}
	// Closing may leave Chrome focused on a discarded successor. Revival is
	// best-effort because target closure is already committed and must not be
	// reported as failed merely because the next page cannot be recovered.
	activeTabRecovery := rendererAlreadyResponsive
	if next, ok := s.tabs.tabs[s.tabs.active]; ok {
		reviveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		activeTabRecovery, _ = ensureTabRendererContext(reviveCtx, next.page)
		cancel()
	}
	s.traceAfterActionContext(ctx, traceStart, traceBefore, "close_tab", name, "", "", nil)
	result := &PageResult{
		TargetID: tab.page.TargetID(),
		FrameID:  "",
		Action:   "closed_tab",
		Target:   name,
		Outcome:  string(OutcomePostconditionVerified),
		Verified: true,
		Evidence: fmt.Sprintf("tab %q (target %s) is closed and absent from Scout's tab registry", name, tab.page.TargetID()),
	}
	switch activeTabRecovery {
	case rendererRecovered:
		result.Evidence += "; successor activation restored renderer responsiveness"
	case rendererActivatedUnconfirmed:
		result.Evidence += "; successor was activated but renderer responsiveness remains unconfirmed"
	case rendererActivationUnknown:
		result.Evidence += "; successor activation outcome is unknown"
	}
	result.OperationOrdinal = s.operationOrdinalForResult("close_tab", name)
	result.Receipt = s.newActionReceipt("close_tab", name, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true, result.OperationOrdinal, nil, ctx)
	result.Receipt.FrameID = result.FrameID
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("close_tab", name, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true)
	if s.tracing && s.trace != nil && len(s.trace.events) > 0 {
		last := &s.trace.events[len(s.trace.events)-1]
		if last.Action == "close_tab" && last.Selector == name {
			last.TargetID = result.TargetID
		}
	}
	return result, nil
}

// ListTabs returns open tabs with their URLs, titles, and browser visibility
// states. If a tab renderer does not respond within the read bound, its last
// observed URL and title are returned with MetadataStale set.
func (s *Session) ListTabs() ([]TabInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ListTabsContext(ctx)
}

// ListTabsContext lists tabs using the caller's deadline for page reads.
func (s *Session) ListTabsContext(ctx context.Context) ([]TabInfo, error) {
	items, _, err := s.ListTabsPageContext(ctx, 0, 0)
	return items, err
}

// ListTabsPageContext returns a stable page of tab metadata. A zero limit
// returns all entries; positive limits are capped at 50 to bound each read.
func (s *Session) ListTabsPageContext(ctx context.Context, offset, limit int) ([]TabInfo, int, error) {
	if offset < 0 {
		return nil, 0, fmt.Errorf("tab offset must be non-negative")
	}
	if limit < 0 {
		return nil, 0, fmt.Errorf("tab limit must be non-negative")
	}
	if limit > 50 {
		limit = 50
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, 0, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, 0, err
	}

	if s.tabs == nil || len(s.tabs.tabs) == 0 {
		// Single page mode
		if s.page == nil {
			return nil, 0, nil
		}
		if offset > 0 {
			return nil, 1, nil
		}
		state, err := s.page.EvaluateContext(ctx, `({url: location.href, title: document.title, visibility: document.visibilityState})`)
		if err != nil {
			return nil, 1, err
		}
		values, _ := state.(map[string]any)
		url, _ := values["url"].(string)
		titleStr, _ := values["title"].(string)
		visibilityStr, _ := values["visibility"].(string)
		s.cacheTabTargetState(s.page, url, titleStr)
		return []TabInfo{{ContentTrust: ContentTrustUntrustedPage, Name: "default", TargetID: s.page.TargetID(), URL: url, Title: titleStr, VisibilityState: visibilityStr, Active: true}}, 1, nil
	}

	total := len(s.tabs.tabs)
	names := make([]string, 0, len(s.tabs.tabs))
	for name := range s.tabs.tabs {
		names = append(names, name)
	}
	sort.Strings(names)
	if offset >= total {
		return nil, total, nil
	}
	names = names[offset:]
	if limit > 0 && len(names) > limit {
		names = names[:limit]
	}
	tabs := make([]TabInfo, 0, len(names))
	for _, name := range names {
		tab := s.tabs.tabs[name]
		readCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		state, readErr := tab.page.EvaluateContext(readCtx, `({url: location.href, title: document.title, visibility: document.visibilityState})`)
		cancel()
		url, titleStr, visibilityStr := tab.lastURL, tab.lastTitle, ""
		metadataStale := readErr != nil
		if readErr == nil {
			values, _ := state.(map[string]any)
			url, _ = values["url"].(string)
			titleStr, _ = values["title"].(string)
			visibilityStr, _ = values["visibility"].(string)
			s.cacheTabTargetState(tab.page, url, titleStr)
		} else if ctx.Err() != nil {
			return nil, total, ctx.Err()
		}
		tabs = append(tabs, TabInfo{
			ContentTrust:    ContentTrustUntrustedPage,
			Name:            name,
			TargetID:        tab.page.TargetID(),
			URL:             url,
			Title:           titleStr,
			VisibilityState: visibilityStr,
			MetadataStale:   metadataStale,
			Active:          name == s.tabs.active,
		})
	}
	return tabs, total, nil
}

func (s *Session) pageResultForPageContext(ctx context.Context, page *browse.Page) (*PageResult, error) {
	state, err := page.EvaluateContext(ctx, `({url: location.href, title: document.title})`)
	if err != nil {
		return nil, err
	}
	values, _ := state.(map[string]any)
	url, _ := values["url"].(string)
	title, _ := values["title"].(string)
	s.cacheTabTargetState(page, url, title)
	return &PageResult{URL: url, ContentTrust: ContentTrustUntrustedPage, Title: title, TargetID: page.TargetID()}, nil
}

func (s *Session) cacheTabTargetState(page *browse.Page, url, title string) {
	if page == nil || s.tabs == nil {
		return
	}
	targetID := page.TargetID()
	for _, tab := range s.tabs.tabs {
		if tab.page != nil && tab.page.TargetID() == targetID {
			tab.lastURL, tab.lastTitle = url, title
			return
		}
	}
}

func (s *Session) reattachDiscardedTabContext(ctx context.Context, tab *tabEntry) (*browse.Page, error) {
	if tab == nil || tab.page == nil || tab.lastURL == "" || tab.lastTitle == "" {
		return nil, &OperationError{
			Phase:         "tab_rebind",
			Cause:         "target_replaced",
			TargetID:      targetIDForTab(tab),
			Detail:        "the browser target was replaced and Scout has no complete last-observed identity to rebind safely",
			RecoveryHint:  "inspect the browser's current tabs and create a fresh Scout session or explicitly reconcile the target",
			OriginalError: "tab target identity is incomplete",
		}
	}
	lister, canList := s.browser.(interface {
		PageTargetsContext(context.Context) ([]browse.PageTarget, error)
	})
	attacher, canAttach := s.browser.(interface {
		PageContext(context.Context, string) (*browse.Page, error)
	})
	if !canList || !canAttach {
		return nil, fmt.Errorf("browser does not support safe tab target rebinding")
	}
	targets, err := lister.PageTargetsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect browser page targets for tab recovery: %w", err)
	}
	var match string
	matches := 0
	registeredTargets := make(map[string]struct{}, len(s.tabs.tabs))
	for name, entry := range s.tabs.tabs {
		if name != tab.name && entry != nil && entry.page != nil {
			registeredTargets[entry.page.TargetID()] = struct{}{}
		}
	}
	for _, target := range targets {
		if target.TargetID == tab.page.TargetID() || target.URL != tab.lastURL || target.Title != tab.lastTitle {
			continue
		}
		if _, alreadyBound := registeredTargets[target.TargetID]; alreadyBound {
			continue
		}
		match = target.TargetID
		matches++
	}
	if matches != 1 {
		cause := "replacement_missing"
		detail := "no browser target matches the tab's last observed identity"
		if matches > 1 {
			cause = "replacement_ambiguous"
			detail = "multiple browser targets match the tab's last observed identity"
		}
		return nil, &OperationError{
			Phase:         "tab_rebind",
			Cause:         cause,
			TargetID:      tab.page.TargetID(),
			Detail:        detail + "; Scout left tab state unchanged",
			RecoveryHint:  "inspect current tabs and reconcile the mapping before retrying",
			OriginalError: "browser target replacement could not be identified uniquely",
		}
	}
	activator, canActivate := s.browser.(interface {
		ActivateTargetContext(context.Context, string) error
	})
	if !canActivate {
		return nil, fmt.Errorf("browser does not support activating a replacement tab target")
	}
	if err := activator.ActivateTargetContext(ctx, match); err != nil {
		return nil, fmt.Errorf("failed to activate the uniquely matched replacement target: %w", err)
	}
	page, err := attacher.PageContext(ctx, match)
	if err != nil {
		return nil, fmt.Errorf("failed to attach to the uniquely matched replacement target: %w", err)
	}
	return page, nil
}

func targetIDForTab(tab *tabEntry) string {
	if tab == nil || tab.page == nil {
		return ""
	}
	return tab.page.TargetID()
}
