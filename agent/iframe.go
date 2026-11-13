package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	browse "go.klarlabs.de/scout"
	waitutil "go.klarlabs.de/scout/internal/wait"
)

type frameTreeNode struct {
	Frame       frameTreeNodeFrame `json:"frame"`
	ChildFrames []frameTreeNode    `json:"childFrames"`
}

type frameTreeNodeFrame struct {
	ID       string `json:"id"`
	LoaderID string `json:"loaderId"`
	URL      string `json:"url"`
	Name     string `json:"name"`
}

func (s *Session) cleanupFrameAttachment(ctx context.Context, page *browse.Page, cause error) error {
	if page == nil {
		return cause
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := page.DetachContext(ctx); err != nil {
		if isTargetGoneError(err) {
			// Chrome may detach an OOPIF session before Scout can issue its
			// explicit detach. The session is already gone, so cleanup is complete.
			return cause
		}
		for _, pending := range s.orphanedFramePages {
			if pending == page {
				return errors.Join(cause, fmt.Errorf("detach iframe target: %w", err))
			}
		}
		s.orphanedFramePages = append(s.orphanedFramePages, page)
		return errors.Join(cause, fmt.Errorf("detach iframe target: %w", err))
	}
	return cause
}

func (s *Session) retainFailedFrameAttachment(err error) {
	var attachErr *browse.FrameTargetAttachError
	if errors.As(err, &attachErr) && attachErr.Page != nil {
		for _, pending := range s.orphanedFramePages {
			if pending == attachErr.Page {
				return
			}
		}
		s.orphanedFramePages = append(s.orphanedFramePages, attachErr.Page)
		return
	}
}

// FrameInfo describes an iframe discovered on the page.
type FrameInfo struct {
	ContentTrust string `json:"content_trust,omitempty"`
	FrameID      string `json:"frame_id"`
	URL          string `json:"url"`
	Name         string `json:"name,omitempty"`
	Selector     string `json:"selector,omitempty"`
}

// restoreFrameRouteLocked detaches a selected OOPIF session and returns to the
// top-level page. The caller holds s.mu.
func (s *Session) restoreFrameRouteLocked(ctx context.Context) error {
	if len(s.frameRouteStack) == 0 && s.frameRoute == nil && len(s.orphanedFramePages) == 0 {
		return nil
	}
	var errs error
	pages := append([]*browse.Page(nil), s.orphanedFramePages...)
	s.orphanedFramePages = nil
	seen := make(map[*browse.Page]struct{}, len(pages)+len(s.frameRouteStack)+1)
	for _, page := range pages {
		if page != nil {
			seen[page] = struct{}{}
		}
	}
	for i := len(s.frameRouteStack) - 1; i >= 0; i-- {
		level := s.frameRouteStack[i]
		if level.unsubscribe != nil {
			level.unsubscribe()
		}
		if level.child != nil {
			if _, ok := seen[level.child]; !ok {
				pages = append(pages, level.child)
				seen[level.child] = struct{}{}
			}
		}
	}
	if len(s.frameRouteStack) == 0 && s.frameRoute != nil {
		if s.frameDetachUnsubscribe != nil {
			s.frameDetachUnsubscribe()
		}
		if _, ok := seen[s.frameRoute]; !ok {
			pages = append(pages, s.frameRoute)
			seen[s.frameRoute] = struct{}{}
		}
	}
	for _, page := range pages {
		if err := s.cleanupFrameAttachment(ctx, page, nil); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	main := s.mainPage
	if len(s.frameRouteStack) > 0 && s.frameRouteStack[0].parent != nil {
		main = s.frameRouteStack[0].parent
	}
	if main != nil {
		s.page = main
	}
	s.frameRouteStack = nil
	s.frameDetached = nil
	s.frameRoute = nil
	s.mainPage = nil
	s.frameOwnerSelector = ""
	s.frameParentID = ""
	s.frameID = ""
	s.frameContextID = 0
	return errs
}

// ensureFrameRouteContext reattaches a selected OOPIF only after confirming
// that its owner document still exposes the selected iframe and Chrome now
// reports exactly one matching child target. The caller holds s.mu.
func (s *Session) ensureFrameRouteContext(ctx context.Context) error {
	if len(s.frameRouteStack) == 0 {
		return nil
	}
	rebound := false
	var cleanupErr error
	for i := range s.frameRouteStack {
		level := &s.frameRouteStack[i]
		if i == 0 {
			level.parent = s.mainPage
		} else {
			level.parent = s.frameRouteStack[i-1].child
			level.parentFrameID = s.frameRouteStack[i-1].frameID
		}
		if level.child != nil && (level.detached == nil || !level.detached.Load()) {
			if _, err := level.child.EvaluateContext(ctx, `location.href`); err == nil {
				continue
			} else if !isTargetGoneError(err) {
				return err
			}
		}
		parent := level.parent
		if parent == nil || level.ownerSelector == "" || level.parentFrameID == "" {
			return targetGoneOperationError("frame_binding", level.child, fmt.Errorf("selected OOPIF binding state is unavailable"))
		}
		owner := jsonString(level.ownerSelector)
		state, err := parent.EvaluateContext(ctx, fmt.Sprintf(`(function(){const e=document.querySelector(%s);if(!e||e.tagName!=='IFRAME')return null;const src=e.src||'';return {src,owners:Array.from(document.querySelectorAll('iframe')).filter(f=>f.src===src).length}})()`, owner))
		if err != nil {
			return fmt.Errorf("inspect selected iframe owner after target detach: %w", err)
		}
		values, _ := state.(map[string]any)
		currentURL, _ := values["src"].(string)
		ownerCount, _ := values["owners"].(float64)
		if currentURL == "" || ownerCount != 1 {
			return fmt.Errorf("selected iframe owner %q is no longer present", level.ownerSelector)
		}
		target, count, err := s.siteIsolatedFrameTarget(ctx, level.parentFrameID, currentURL)
		if err != nil {
			return fmt.Errorf("inspect browser targets while reattaching iframe %q: %w", level.ownerSelector, err)
		}
		if count != 1 {
			return fmt.Errorf("selected iframe owner %q has no unique replacement target", level.ownerSelector)
		}
		frame, err := parent.AttachFrameTargetContext(ctx, target.TargetID)
		if err != nil {
			s.retainFailedFrameAttachment(err)
			return fmt.Errorf("reattach selected iframe target: %w", err)
		}
		bound, err := s.siteIsolatedFrameTargetStillBound(ctx, target.TargetID, level.parentFrameID, currentURL)
		if err != nil {
			return s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("recheck replacement iframe target binding: %w", err))
		}
		if !bound {
			return s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("selected iframe target changed during reattachment"))
		}
		tree, err := frame.CallContext(ctx, "Page.getFrameTree", nil)
		if err != nil {
			return s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("verify replacement iframe frame tree: %w", err))
		}
		var root struct {
			FrameTree struct {
				Frame frameTreeNodeFrame `json:"frame"`
			} `json:"frameTree"`
		}
		if err := json.Unmarshal(tree, &root); err != nil || root.FrameTree.Frame.ID == "" {
			return s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("replacement iframe target returned no frame identity"))
		}
		state, err = frame.EvaluateContext(ctx, `({url:location.href,title:document.title})`)
		if err != nil {
			return s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("verify replacement iframe document: %w", err))
		}
		values, _ = state.(map[string]any)
		actualURL, _ := values["url"].(string)
		if !sameFrameURL(actualURL, currentURL) {
			return s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("replacement iframe URL does not match its owner element"))
		}
		detached, unsubscribe := watchPageDetach(frame)
		if _, err := frame.EvaluateContext(ctx, `location.href`); err != nil {
			unsubscribe()
			return s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("replacement iframe detached during route setup: %w", err))
		}
		old, oldUnsubscribe := level.child, level.unsubscribe
		if oldUnsubscribe != nil {
			oldUnsubscribe()
		}
		level.child, level.frameID, level.detached, level.unsubscribe = frame, root.FrameTree.Frame.ID, detached, unsubscribe
		rebound = true
		if old != nil {
			if err := s.cleanupFrameAttachment(ctx, old, nil); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("detach previous iframe target after reattachment: %w", err))
			}
		}
	}
	leaf := s.frameRouteStack[len(s.frameRouteStack)-1]
	s.frameRoute, s.page = leaf.child, leaf.child
	if rebound {
		s.frameID, s.frameContextID = leaf.frameID, 0
	}
	s.frameDetached, s.frameDetachUnsubscribe = leaf.detached, leaf.unsubscribe
	s.frameOwnerSelector, s.frameParentID = leaf.ownerSelector, leaf.parentFrameID
	return cleanupErr
}

func watchPageDetach(page *browse.Page) (*atomic.Bool, func()) {
	detached := &atomic.Bool{}
	unsubscribe := page.OnDetached(func() { detached.Store(true) })
	return detached, unsubscribe
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// SwitchToFrame switches the session's execution context to the iframe matching
// the given CSS selector. Subsequent Evaluate, Click, Type, etc. calls will
// operate inside the iframe until SwitchToMainFrame is called.
func (s *Session) SwitchToFrame(selector string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SwitchToFrameContext(ctx, selector)
}

// SwitchToFrameContext selects an iframe within the caller's deadline.
func (s *Session) SwitchToFrameContext(ctx context.Context, selector string) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if err := s.ensureFrameRouteContext(ctx); err != nil {
		return nil, err
	}
	frameTree, err := s.page.CallContext(ctx, "Page.getFrameTree", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get frame tree: %w", err)
	}

	var tree struct {
		FrameTree struct {
			Frame       frameTreeNodeFrame `json:"frame"`
			ChildFrames []frameTreeNode    `json:"childFrames"`
		} `json:"frameTree"`
	}
	if err := json.Unmarshal(frameTree, &tree); err != nil {
		return nil, fmt.Errorf("failed to parse frame tree: %w", err)
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		const iframe = document.querySelector(%s);
		if (!iframe || iframe.tagName !== 'IFRAME') return '';
		return iframe.src || '';
	})()`, selectorJSON)

	result, err := s.evaluateCurrentContext(ctx, js)
	if err != nil {
		return nil, fmt.Errorf("failed to find iframe %s: %w", selector, err)
	}
	iframeSrc, _ := result.(string)
	if iframeSrc != "" {
		srcJSON, _ := json.Marshal(iframeSrc)
		countResult, countErr := s.evaluateCurrentContext(ctx, fmt.Sprintf(`(function(){const src=%s;return Array.from(document.querySelectorAll('iframe')).filter(frame=>frame.src===src).length})()`, srcJSON))
		if countErr != nil {
			return nil, fmt.Errorf("failed to verify iframe URL uniqueness for %s: %w", selector, countErr)
		}
		if count, ok := countResult.(float64); ok && count > 1 {
			return nil, fmt.Errorf("iframe %q shares URL %q with %d owner elements; target binding is ambiguous", selector, iframeSrc, int(count))
		}
	}

	// Resolve the selector from the selected document, then look only among
	// that frame's direct children. This keeps duplicate iframe URLs elsewhere
	// in the page tree from binding to the wrong element.
	root := frameTreeNode{Frame: tree.FrameTree.Frame, ChildFrames: tree.FrameTree.ChildFrames}
	selectedFrameTree := &root
	if s.frameID != "" {
		selectedFrameTree = nil
		var findSelected func(*frameTreeNode) bool
		findSelected = func(node *frameTreeNode) bool {
			if node.Frame.ID == s.frameID {
				selectedFrameTree = node
				return true
			}
			for i := range node.ChildFrames {
				if findSelected(&node.ChildFrames[i]) {
					return true
				}
			}
			return false
		}
		findSelected(&root)
	}
	if selectedFrameTree == nil {
		return nil, fmt.Errorf("selected frame %q is missing from the frame tree", s.frameID)
	}
	findFrame := func(nodes []frameTreeNode) string {
		for _, node := range nodes {
			if (iframeSrc != "" && node.Frame.URL == iframeSrc) || node.Frame.Name == selector {
				return node.Frame.ID
			}
		}
		return ""
	}
	targetFrameID := findFrame(selectedFrameTree.ChildFrames)

	if targetFrameID == "" {
		nameJS := fmt.Sprintf(`(function() {
			const iframe = document.querySelector(%s);
			if (!iframe) return '';
			return iframe.name || iframe.id || '';
		})()`, selectorJSON)
		nameResult, err := s.evaluateCurrentContext(ctx, nameJS)
		if err != nil {
			return nil, err
		}
		frameName, _ := nameResult.(string)

		findNamedFrame := func(nodes []frameTreeNode) string {
			for _, node := range nodes {
				if frameName != "" && node.Frame.Name == frameName {
					return node.Frame.ID
				}
			}
			return ""
		}
		targetFrameID = findNamedFrame(selectedFrameTree.ChildFrames)
	}

	if targetFrameID == "" {
		target, matches, inventoryErr := s.siteIsolatedFrameTarget(ctx, selectedFrameTree.Frame.ID, iframeSrc)
		if inventoryErr != nil {
			return nil, fmt.Errorf("inspect browser targets while selecting iframe %q: %w", selector, inventoryErr)
		}
		if matches == 1 {
			frame, attachErr := s.page.AttachFrameTargetContext(ctx, target.TargetID)
			if attachErr != nil {
				s.retainFailedFrameAttachment(attachErr)
				return nil, fmt.Errorf("attach site-isolated iframe %q: %w", selector, attachErr)
			}
			bound, bindingErr := s.siteIsolatedFrameTargetStillBound(ctx, target.TargetID, selectedFrameTree.Frame.ID, iframeSrc)
			if bindingErr != nil {
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("recheck site-isolated iframe %q target binding: %w", selector, bindingErr))
			}
			if !bound {
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("site-isolated iframe %q changed target binding during attachment", selector))
			}
			frameTree, treeErr := frame.CallContext(ctx, "Page.getFrameTree", nil)
			if treeErr != nil {
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("read site-isolated iframe frame tree: %w", treeErr))
			}
			var frameRoot struct {
				FrameTree struct {
					Frame frameTreeNodeFrame `json:"frame"`
				} `json:"frameTree"`
			}
			if err := json.Unmarshal(frameTree, &frameRoot); err != nil {
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("decode site-isolated iframe %q frame tree: %w", selector, err))
			}
			if frameRoot.FrameTree.Frame.ID == "" {
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("site-isolated iframe %q returned no root frame identity", selector))
			}
			state, stateErr := frame.EvaluateContext(ctx, `({url: location.href, title: document.title})`)
			if stateErr != nil {
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("read site-isolated iframe %q: %w", selector, stateErr))
			}
			values, _ := state.(map[string]any)
			urlStr, _ := values["url"].(string)
			titleStr, _ := values["title"].(string)
			if urlStr == "" || !sameFrameURL(urlStr, target.URL) {
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("site-isolated iframe %q changed URL during target attachment", selector))
			}
			detached, unsubscribe := watchPageDetach(frame)
			if _, err := frame.EvaluateContext(ctx, `location.href`); err != nil {
				unsubscribe()
				return nil, s.cleanupFrameAttachment(ctx, frame, fmt.Errorf("site-isolated iframe %q detached during route setup: %w", selector, err))
			}
			parentPage := s.page
			if len(s.frameRouteStack) == 0 {
				s.mainPage = parentPage
			}
			s.frameRouteStack = append(s.frameRouteStack, frameRouteLevel{
				parent: parentPage, child: frame, ownerSelector: selector,
				parentFrameID: selectedFrameTree.Frame.ID, frameID: frameRoot.FrameTree.Frame.ID,
				detached: detached, unsubscribe: unsubscribe,
			})
			s.frameRoute = frame
			s.page = frame
			s.frameDetached = detached
			s.frameDetachUnsubscribe = unsubscribe
			s.frameOwnerSelector = selector
			s.frameParentID = selectedFrameTree.Frame.ID
			s.frameID = frameRoot.FrameTree.Frame.ID
			s.frameContextID = 0
			s.addHistory("switch_to_frame", selector, "", "")
			pageResult := &PageResult{URL: urlStr, ContentTrust: ContentTrustUntrustedPage, Title: titleStr, TargetID: frame.TargetID(), FrameID: s.frameID, Action: "switched_frame", Target: selector, Outcome: string(OutcomeFrameSelected), Verified: true, Evidence: "OOPIF target parent, URL, frame identity, and child document state were confirmed"}
			pageResult.OperationOrdinal = s.nextOperationOrdinal()
			pageResult.Receipt = s.newActionReceipt("switch_to_frame", selector, pageResult.TargetID, pageResult.Outcome, pageResult.Evidence, "", true, pageResult.OperationOrdinal, nil, ctx)
			pageResult.Receipt.FrameID = s.frameID
			actionReceiptDeadline(ctx, pageResult.Receipt)
			return pageResult, nil
		}
		if matches > 1 {
			return nil, fmt.Errorf("iframe %q matches multiple site-isolated targets under frame %q; selection is ambiguous", selector, selectedFrameTree.Frame.ID)
		}
		// Chromium can represent a cross-site iframe as a separate OOPIF target.
		// In that case the owner document can resolve the iframe element, but the
		// page target's Page.getFrameTree response does not include the child. Do
		// not guess a binding from URL alone: report that CDP has not exposed a
		// frame ID so callers can distinguish this from a missing selector.
		if iframeSrc != "" && len(selectedFrameTree.ChildFrames) == 0 {
			return nil, fmt.Errorf("iframe %q is present in the selected document, but CDP has not exposed its child frame ID; it may still be attaching or be a site-isolated OOPIF, which Scout cannot select yet", selector)
		}
		return nil, fmt.Errorf("iframe %q not found among %d direct children of frame %q", selector, len(selectedFrameTree.ChildFrames), selectedFrameTree.Frame.ID)
	}

	worldResult, err := s.page.CallContext(ctx, "Page.createIsolatedWorld", map[string]any{
		"frameId":             targetFrameID,
		"grantUniveralAccess": true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create execution context for frame: %w", err)
	}

	var world struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}
	if err := json.Unmarshal(worldResult, &world); err != nil {
		return nil, fmt.Errorf("failed to parse execution context: %w", err)
	}

	url, err := s.evaluateInFrameContext(ctx, world.ExecutionContextID, `window.location.href`)
	if err != nil {
		return nil, err
	}
	title, err := s.evaluateInFrameContext(ctx, world.ExecutionContextID, `document.title`)
	if err != nil {
		return nil, err
	}
	urlStr, _ := url.(string)
	titleStr, _ := title.(string)
	if urlStr == "" {
		return nil, fmt.Errorf("selected iframe %q returned no URL from its execution context", selector)
	}
	s.frameID = targetFrameID
	s.frameContextID = world.ExecutionContextID

	s.addHistory("switch_to_frame", selector, "", "")
	pageResult := &PageResult{
		URL:          urlStr,
		ContentTrust: ContentTrustUntrustedPage,
		Title:        titleStr,
		TargetID:     s.page.TargetID(),
		FrameID:      targetFrameID,
		Action:       "switched_frame",
		Target:       selector,
		Outcome:      string(OutcomeFrameSelected),
		Verified:     true,
		Evidence:     "frame URL and title read from the selected execution context",
	}
	pageResult.OperationOrdinal = s.nextOperationOrdinal()
	pageResult.Receipt = s.newActionReceipt("switch_to_frame", selector, pageResult.TargetID, pageResult.Outcome, pageResult.Evidence, "", true, pageResult.OperationOrdinal, nil, ctx)
	pageResult.Receipt.FrameID = targetFrameID
	actionReceiptDeadline(ctx, pageResult.Receipt)
	return pageResult, nil
}

func (s *Session) siteIsolatedFrameTarget(ctx context.Context, parentFrameID, frameURL string) (browse.BrowserTarget, int, error) {
	if s == nil || s.browser == nil || parentFrameID == "" || frameURL == "" {
		return browse.BrowserTarget{}, 0, nil
	}
	provider, ok := s.browser.(interface {
		BrowserTargetsContext(context.Context) ([]browse.BrowserTarget, error)
	})
	if !ok {
		return browse.BrowserTarget{}, 0, nil
	}
	targets, err := provider.BrowserTargetsContext(ctx)
	if err != nil {
		return browse.BrowserTarget{}, 0, fmt.Errorf("list browser targets: %w", err)
	}
	var match browse.BrowserTarget
	count := 0
	for _, target := range targets {
		if target.Type == "iframe" && target.ParentFrameID == parentFrameID && sameFrameURL(target.URL, frameURL) {
			match = target
			count++
		}
	}
	return match, count, nil
}

func (s *Session) siteIsolatedFrameTargetStillBound(ctx context.Context, targetID, parentFrameID, frameURL string) (bool, error) {
	if s == nil || s.browser == nil {
		return false, nil
	}
	provider, ok := s.browser.(interface {
		BrowserTargetsContext(context.Context) ([]browse.BrowserTarget, error)
	})
	if !ok {
		return false, nil
	}
	targets, err := provider.BrowserTargetsContext(ctx)
	if err != nil {
		return false, fmt.Errorf("list browser targets: %w", err)
	}
	for _, target := range targets {
		if target.TargetID == targetID && target.Type == "iframe" && target.ParentFrameID == parentFrameID && sameFrameURL(target.URL, frameURL) {
			return true, nil
		}
	}
	return false, nil
}

func sameFrameURL(a, b string) bool {
	left, leftErr := url.Parse(a)
	right, rightErr := url.Parse(b)
	if leftErr != nil || rightErr != nil || left.Scheme != right.Scheme || left.Host != right.Host || left.RawQuery != right.RawQuery {
		return false
	}
	left.Fragment, right.Fragment = "", ""
	if left.Path == "" {
		left.Path = "/"
	}
	if right.Path == "" {
		right.Path = "/"
	}
	return left.EscapedPath() == right.EscapedPath()
}

// SwitchToMainFrame resets the session back to the main frame context.
func (s *Session) SwitchToMainFrame() (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SwitchToMainFrameContext(ctx)
}

// SwitchToMainFrameContext returns to the main frame within the caller's deadline.
func (s *Session) SwitchToMainFrameContext(ctx context.Context) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	mainPage := s.page
	if len(s.frameRouteStack) > 0 && s.frameRouteStack[0].parent != nil {
		mainPage = s.frameRouteStack[0].parent
	} else if s.frameRoute != nil && s.mainPage != nil {
		mainPage = s.mainPage
	}
	state, err := mainPage.EvaluateContext(ctx, `({url: location.href, title: document.title})`)
	if err != nil {
		return nil, fmt.Errorf("read main-frame state after context reset: %w", err)
	}
	values, _ := state.(map[string]any)
	url, _ := values["url"].(string)
	titleStr, _ := values["title"].(string)
	if len(s.frameRouteStack) > 0 {
		if detachErr := s.restoreFrameRouteLocked(ctx); detachErr != nil {
			return nil, fmt.Errorf("detach selected iframe targets: %w", detachErr)
		}
	} else if s.frameRoute != nil {
		if detachErr := s.restoreFrameRouteLocked(ctx); detachErr != nil {
			return nil, fmt.Errorf("detach selected iframe target: %w", detachErr)
		}
	}
	s.frameID = ""
	s.frameContextID = 0

	s.addHistory("switch_to_main_frame", "", "", "")
	pageResult := &PageResult{
		URL:          url,
		ContentTrust: ContentTrustUntrustedPage,
		Title:        titleStr,
		TargetID:     s.page.TargetID(),
		FrameID:      "",
		Action:       "switched_to_main_frame",
		Target:       "main",
		Outcome:      string(OutcomeMainFrameSelected),
		Verified:     true,
		Evidence:     "main-frame URL and title read after execution context reset",
	}
	pageResult.OperationOrdinal = s.nextOperationOrdinal()
	pageResult.Receipt = s.newActionReceipt("switch_to_main_frame", "main", pageResult.TargetID, pageResult.Outcome, pageResult.Evidence, "", true, pageResult.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, pageResult.Receipt)
	return pageResult, nil
}

func (s *Session) evaluateInFrameContext(ctx context.Context, contextID int64, expression string) (any, error) {
	if contextID == 0 {
		return s.page.EvaluateContext(ctx, expression)
	}
	result, err := s.page.CallContext(ctx, "Runtime.evaluate", map[string]any{
		"expression": expression, "contextId": contextID, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		// A cross-document navigation destroys the isolated execution context
		// even though the frame itself remains selected. Recreate it once for
		// the selected frame, then retry this read in the replacement context.
		if contextID == 0 || contextID != s.frameContextID || s.frameID == "" || !isMissingFrameExecutionContext(err) {
			return nil, err
		}
		worldResult, worldErr := s.page.CallContext(ctx, "Page.createIsolatedWorld", map[string]any{
			"frameId": s.frameID, "grantUniveralAccess": true,
		})
		if worldErr != nil {
			return nil, fmt.Errorf("recreate execution context for selected frame %s: %w", s.frameID, worldErr)
		}
		var world struct {
			ExecutionContextID int64 `json:"executionContextId"`
		}
		if worldErr := json.Unmarshal(worldResult, &world); worldErr != nil {
			return nil, fmt.Errorf("parse recreated execution context: %w", worldErr)
		}
		if world.ExecutionContextID == 0 {
			return nil, fmt.Errorf("recreated execution context for selected frame %s is empty", s.frameID)
		}
		s.frameContextID = world.ExecutionContextID
		result, err = s.page.CallContext(ctx, "Runtime.evaluate", map[string]any{
			"expression": expression, "contextId": world.ExecutionContextID, "returnByValue": true, "awaitPromise": true,
		})
		if err != nil {
			return nil, err
		}
	}
	var resp struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	if resp.ExceptionDetails != nil {
		return nil, fmt.Errorf("js error in frame: %s", resp.ExceptionDetails.Text)
	}
	if len(resp.Result.Value) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(resp.Result.Value, &value); err != nil {
		return nil, fmt.Errorf("decode frame javascript result: %w", err)
	}
	return value, nil
}

func isMissingFrameExecutionContext(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "cannot find context with specified id") ||
		strings.Contains(message, "execution context was destroyed")
}

// evaluateCurrentContext runs JavaScript in the currently selected frame, or
// in the main document when no frame is selected. Caller holds s.mu.
func (s *Session) evaluateCurrentContext(ctx context.Context, expression string) (any, error) {
	return s.evaluateInFrameContext(ctx, s.frameContextID, expression)
}

type frameEvaluator struct {
	s         *Session
	ctx       context.Context
	contextID int64
}

func (e frameEvaluator) Evaluate(expression string) (any, error) {
	return e.s.evaluateInFrameContext(e.ctx, e.contextID, expression)
}

func (s *Session) waitForSelectorContext(ctx context.Context, selector string) error {
	// Playwright-style text selectors are not CSS. The wait utility deliberately
	// skips CSS validation for these selectors, so polling them as CSS would
	// consume the full timeout before resolveSelectorContext gets its fallback.
	// Their resolver performs an immediate text lookup after this no-op.
	if strings.Contains(selector, ":text(") || strings.Contains(selector, ":has-text(") {
		return ctx.Err()
	}
	if s.frameContextID == 0 {
		return s.page.WaitForSelectorContext(ctx, selector)
	}
	return waitutil.ForSelector(ctx, frameEvaluator{s: s, ctx: ctx, contextID: s.frameContextID}, selector)
}

func (s *Session) waitForSelector(selector string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.waitForSelectorContext(ctx, selector)
}

func (s *Session) querySelectorInFrameContext(ctx context.Context, selector string) (int64, error) {
	result, err := s.page.CallContext(ctx, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true})
	if err != nil {
		return 0, err
	}
	var document struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(result, &document); err != nil {
		return 0, err
	}
	selectorJSON, _ := json.Marshal(selector)
	mark := fmt.Sprintf("frame-%d", time.Now().UnixNano())
	markJSON, _ := json.Marshal(mark)
	marked, err := s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`(function(){const out=[];function walk(root){try{out.push(...root.querySelectorAll(%s))}catch(_){};const all=root.querySelectorAll?root.querySelectorAll('*'):[];for(const el of all)if(el.shadowRoot)walk(el.shadowRoot)}walk(document);const e=out[0];if(!e)return false;e.setAttribute('data-scout-frame-node',%s);return true})()`, selectorJSON, markJSON))
	if err != nil {
		return 0, err
	}
	if marked != true {
		return 0, fmt.Errorf("element not found in frame %q: %s", s.frameID, selector)
	}
	defer func() {
		cleanupCtx := ctx
		var cancel context.CancelFunc
		if ctx.Err() != nil {
			cleanupCtx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
		}
		_, _ = s.evaluateInFrameContext(cleanupCtx, s.frameContextID, fmt.Sprintf(`(function(){function walk(root){try{for(const e of root.querySelectorAll('[data-scout-frame-node="%s"]'))e.removeAttribute('data-scout-frame-node')}catch(_){};const all=root.querySelectorAll?root.querySelectorAll('*'):[];for(const el of all)if(el.shadowRoot)walk(el.shadowRoot)}walk(document)})()`, mark))
	}()
	if document.Root.NodeID == 0 {
		return 0, fmt.Errorf("frame %q returned no DOM root", s.frameID)
	}
	flattened, err := s.page.CallContext(ctx, "DOM.getFlattenedDocument", map[string]any{"depth": -1, "pierce": true})
	if err != nil {
		return 0, err
	}
	var nodes struct {
		Nodes []frameFlatNode `json:"nodes"`
	}
	if err := json.Unmarshal(flattened, &nodes); err != nil {
		return 0, err
	}
	nodeID := matchFrameFlatNode(nodes.Nodes, mark)
	if nodeID == 0 {
		return 0, fmt.Errorf("element not found in frame %q: %s", s.frameID, selector)
	}
	return nodeID, nil
}

func (s *Session) querySelectorForCurrentContext(ctx context.Context, selector string) (int64, error) {
	if s.frameContextID != 0 {
		return s.querySelectorInFrameContext(ctx, selector)
	}
	return s.page.QuerySelectorPiercingContext(ctx, selector)
}

// clickInSelectedFrameContext dispatches a hit-tested pointer and mouse sequence
// in an in-process frame's execution context.
func (s *Session) clickInSelectedFrameContext(ctx context.Context, nodeID int64) error {
	if s.frameContextID == 0 || s.frameRoute != nil || s.frameID == "" {
		return fmt.Errorf("selected frame does not use an in-process execution context")
	}
	objectID, err := s.page.ResolveNodeContext(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("resolve selected-frame click target: %w", err)
	}
	result, err := s.page.CallContext(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            objectID,
		"functionDeclaration": `function(){const e=this,r=e.getBoundingClientRect();if(r.width<=0||r.height<=0)throw new Error('selected frame target is not visible');const x=r.left+r.width/2,y=r.top+r.height/2,hit=document.elementFromPoint(x,y);if(!hit||!(hit===e||e.contains(hit)))return {obscured:true};for(const [type,init] of [['pointerover',{pointerId:1,pointerType:'mouse',isPrimary:true}],['pointermove',{pointerId:1,pointerType:'mouse',isPrimary:true}],['pointerdown',{pointerId:1,pointerType:'mouse',isPrimary:true,button:0,buttons:1}],['mousedown',{button:0,buttons:1}],['pointerup',{pointerId:1,pointerType:'mouse',isPrimary:true,button:0,buttons:0}],['mouseup',{button:0,buttons:0}],['click',{button:0,buttons:0,detail:1}]]){const C=type.startsWith('pointer')?PointerEvent:MouseEvent;hit.dispatchEvent(new C(type,{...init,bubbles:true,cancelable:true,clientX:x,clientY:y,view:window}))}return {obscured:false}}`,
		"returnByValue":       true,
		"awaitPromise":        true,
	})
	if err != nil {
		return fmt.Errorf("activate selected-frame click target: %w", err)
	}
	var response struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return fmt.Errorf("decode selected-frame click result: %w", err)
	}
	if response.ExceptionDetails != nil {
		return fmt.Errorf("selected-frame click failed: %s", response.ExceptionDetails.Text)
	}
	value, _ := response.Result.Value.(map[string]any)
	if value == nil {
		return fmt.Errorf("selected-frame click target was not activated")
	}
	if obscured, _ := value["obscured"].(bool); obscured {
		return fmt.Errorf("selected frame target is obscured")
	}
	return nil
}

type frameFlatNode struct {
	NodeID     int64    `json:"nodeId"`
	NodeName   string   `json:"nodeName"`
	Attributes []string `json:"attributes"`
}

func matchFrameFlatNode(nodes []frameFlatNode, mark string) int64 {
	for _, node := range nodes {
		if node.NodeName != "" && len(node.Attributes)%2 == 0 {
			for i := 0; i+1 < len(node.Attributes); i += 2 {
				if node.Attributes[i] == "data-scout-frame-node" && node.Attributes[i+1] == mark {
					return node.NodeID
				}
			}
		}
	}
	return 0
}

// evaluateInFrame executes JS in the current frame's execution context.
// Must be called with s.mu held.
func (s *Session) evaluateInFrame(expression string) (any, error) {
	if s.frameContextID == 0 {
		return s.page.Evaluate(expression)
	}

	params := map[string]any{
		"expression":    expression,
		"contextId":     s.frameContextID,
		"returnByValue": true,
		"awaitPromise":  true,
	}
	result, err := s.page.Call("Runtime.evaluate", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	if resp.ExceptionDetails != nil {
		return nil, fmt.Errorf("js error in frame: %s", resp.ExceptionDetails.Text)
	}

	var val any
	if len(resp.Result.Value) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(resp.Result.Value, &val); err != nil {
		return nil, nil //nolint:nilerr
	}
	return val, nil
}

// InFrame returns true if the session is currently targeting an iframe.
func (s *Session) InFrame() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frameID != ""
}
