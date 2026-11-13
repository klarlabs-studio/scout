package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	browse "go.klarlabs.de/scout"
)

// AnnotatedScreenshot captures a screenshot with numbered labels overlaid on
// interactive elements. Returns the image data and a mapping of label numbers
// to element info (selector, text, type). Designed for multimodal LLMs that
// can reference elements by number instead of CSS selectors.
func (s *Session) AnnotatedScreenshot() (*AnnotatedResult, error) {
	return s.AnnotatedScreenshotWithLimits(-1, 0)
}

// AnnotatedScreenshotWithLimits captures the annotated page with optional byte
// and width limits. A negative maxBytes uses the session default; zero is uncapped.
func (s *Session) AnnotatedScreenshotWithLimits(maxBytes, maxWidth int) (*AnnotatedResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.AnnotatedScreenshotWithLimitsContext(ctx, maxBytes, maxWidth)
}

// AnnotatedScreenshotWithLimitsContext captures the annotated page under the
// caller's deadline. Cleanup uses a short independent deadline so cancellation
// during capture does not leave label overlays on the page.
func (s *Session) AnnotatedScreenshotWithLimitsContext(ctx context.Context, maxBytes, maxWidth int) (*AnnotatedResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if s.frameContextID != 0 {
			_, _ = s.evaluateInFrameContext(cleanupCtx, s.frameContextID, `document.querySelectorAll('.__browse_label').forEach(el => el.remove())`)
		} else {
			_, _ = s.page.EvaluateContext(cleanupCtx, `document.querySelectorAll('.__browse_label').forEach(el => el.remove())`)
		}
	}()

	// Inject labels and collect element mapping. Each annotated
	// element also gets a data-scout-handle attribute — that's the
	// stable identifier ClickByHandle resolves against later, so the
	// caller can survive DOM mutations that would shuffle the
	// per-call label numbers.
	js := fmt.Sprintf(`(function() {
		// Remove any previous annotations
		document.querySelectorAll('.__browse_label').forEach(el => el.remove());

		// Walk into open shadow roots so Lit / Stencil custom
		// elements expose their interactive nodes to annotation.
		function deepAll(root, sel, out) {
			if (!root) return;
			try { root.querySelectorAll(sel).forEach(e => out.push(e)); } catch(_) {}
			const kids = root.querySelectorAll ? root.querySelectorAll('*') : [];
			for (const el of kids) if (el.shadowRoot) deepAll(el.shadowRoot, sel, out);
		}

		const handlePrefix = 'h_%d_';
		const elements = [];
		let idx = 0;
		const selectors = 'a[href], button, input, textarea, select, [role="button"], [onclick], [tabindex]';

		const candidates = [];
		deepAll(document, selectors, candidates);
		for (const el of candidates) {
			// Skip hidden/tiny elements
			const rect = el.getBoundingClientRect();
			if (rect.width < 5 || rect.height < 5) continue;
			const style = window.getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') continue;

			idx++;
			const tag = el.tagName.toLowerCase();
			const handle = el.getAttribute('data-scout-handle') || (handlePrefix + idx);
			el.setAttribute('data-scout-handle', handle);

			// Build selector
			let selector = tag;
			if (el.id) selector = '#' + CSS.escape(el.id);
			else if (el.name) selector = tag + '[name="' + el.name + '"]';

			// Build label
			let text = '';
			if (tag === 'input' || tag === 'textarea') {
				text = tag === 'input' && (el.type === 'password' || el.type === 'file') ? (el.placeholder || '') : (el.placeholder || el.value || '');
			} else if (tag === 'select') {
				text = el.options[el.selectedIndex] ? el.options[el.selectedIndex].text : '';
			} else {
				text = el.textContent.trim().slice(0, 60);
			}

			if (elements.length < %d) elements.push({
				label: idx,
				node_handle: handle,
				selector: selector,
				tag: tag,
				type: el.type || '',
				text: text,
				href: el.getAttribute('href') || '',
				x: Math.round(rect.x),
				y: Math.round(rect.y),
				width: Math.round(rect.width),
				height: Math.round(rect.height)
			});

			// Create visual label overlay
			const marker = document.createElement('div');
			marker.className = '__browse_label';
			marker.textContent = idx;
			marker.style.cssText = 'position:fixed;z-index:999999;pointer-events:none;' +
				'background:#e63946;color:#fff;font:bold 11px/14px monospace;' +
				'padding:0 4px;border-radius:7px;min-width:14px;text-align:center;' +
				'left:' + Math.max(0, rect.x - 2) + 'px;' +
				'top:' + Math.max(0, rect.y - 14) + 'px;' +
				'box-shadow:0 1px 3px rgba(0,0,0,0.4);';
			document.body.appendChild(marker);
		}

		return JSON.stringify({elements: elements, total: idx});
	})()`, time.Now().UnixNano(), elementLimit(s.contentOpts))

	var result any
	var err error
	if s.frameContextID != 0 {
		result, err = s.evaluateInFrameContext(ctx, s.frameContextID, js)
	} else {
		result, err = s.page.EvaluateContext(ctx, js)
	}
	if err != nil {
		return nil, fmt.Errorf("annotation failed: %w", err)
	}

	// Parse element mapping
	var elements []AnnotatedElement
	total := 0
	if str, ok := result.(string); ok {
		var payload struct {
			Elements []AnnotatedElement `json:"elements"`
			Total    int                `json:"total"`
		}
		if err := json.Unmarshal([]byte(str), &payload); err != nil {
			return nil, fmt.Errorf("parse annotated elements: %w", err)
		}
		elements = payload.Elements
		for i := range elements {
			elements[i].ContentTrust = ContentTrustUntrustedPage
		}
		total = payload.Total
	}

	// Capture screenshot with labels visible
	if maxBytes < 0 {
		maxBytes = s.contentOpts.MaxScreenshotBytes
		if maxBytes == 0 {
			maxBytes = 5 * 1024 * 1024
		}
	}
	screenshot, imageInfo, err := s.page.ScreenshotWithOptionsInfoContext(ctx, browse.ScreenshotOptions{MaxSize: maxBytes, MaxWidth: maxWidth})

	if err != nil {
		return nil, fmt.Errorf("annotated screenshot failed: %w", err)
	}

	return &AnnotatedResult{
		ContentTrust: ContentTrustUntrustedPage,
		TargetID:     s.page.TargetID(),
		FrameID:      s.frameID,
		Image:        screenshot,
		ImageInfo:    imageInfo,
		Elements:     elements,
		Count:        len(elements),
		Total:        total,
		Truncated:    total > len(elements),
		LabelScope:   "per_call",
	}, nil
}

// ClickByHandle clicks the element associated with a stable node
// handle stamped during AnnotatedScreenshot or AutoExtract. Unlike
// ClickLabel, the lookup happens by attribute, so DOM mutations that
// shuffle label numbers don't break the action — as long as the element itself
// wasn't detached. If the element is gone, a "stale" OperationError
// is returned so the caller can re-annotate instead of clicking
// blindly into the wrong node.
func (s *Session) ClickByHandle(handle string) (*PageResult, error) {
	return s.ClickByHandleWithPostcondition(handle, Postcondition{})
}

// ClickByHandleWithPostcondition clicks a previously annotated element and can
// verify the resulting page state before returning.
func (s *Session) ClickByHandleWithPostcondition(handle string, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClickByHandleWithPostconditionContext(ctx, handle, condition)
}

// ClickByHandleWithPostconditionContext resolves and clicks a stable handle under ctx.
func (s *Session) ClickByHandleWithPostconditionContext(ctx context.Context, handle string, condition Postcondition) (*PageResult, error) {
	return s.ClickByHandleWithIdentityContext(ctx, handle, "", "", condition)
}

// ClickByHandleWithIdentityContext clicks a handle only while the expected
// CDP target and selected frame are active. Pass targetID to enforce both
// identities; an empty expected frame ID means the main frame.
func (s *Session) ClickByHandleWithIdentityContext(ctx context.Context, handle, targetID, frameID string, condition Postcondition) (*PageResult, error) {
	if targetID == "" && frameID != "" {
		return nil, fmt.Errorf("target_id is required when frame_id is provided")
	}
	if condition.count() > 0 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
	}
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
	if targetID != "" && targetID != s.page.TargetID() {
		return nil, &OperationError{Phase: "click_by_handle", Cause: "stale_handle", Detail: "handle " + handle + " belongs to target " + targetID + ", but active target is " + s.page.TargetID() + " — re-extract the current page", OriginalError: "target identity mismatch"}
	}
	if targetID != "" && frameID != s.frameID {
		return nil, &OperationError{Phase: "click_by_handle", Cause: "stale_handle", Detail: "handle " + handle + " belongs to frame " + frameID + ", but active frame is " + s.frameID + " — select the source frame or re-extract", OriginalError: "frame identity mismatch"}
	}
	handleJSON, _ := json.Marshal(handle)
	state, err := s.evaluateCurrentContext(ctx, fmt.Sprintf(`(function(){const sel='[data-scout-handle='+JSON.stringify(%s)+']';function findAll(root,out){if(!root)return;try{out.push(...root.querySelectorAll(sel))}catch(_){}for(const host of (root.querySelectorAll?root.querySelectorAll('*'):[])){if(host.shadowRoot)findAll(host.shadowRoot,out)}}const matches=[];findAll(document,matches);if(matches.length===0)return 'stale';if(matches.length!==1)return 'ambiguous';const el=matches[0],r=el.getBoundingClientRect(),s=getComputedStyle(el);if(r.width<1||r.height<1||s.display==='none'||s.visibility==='hidden')return 'stale';el.scrollIntoView({block:'center'});el.click();return 'clicked'})()`, string(handleJSON)))
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("click", handle, "handle click may have been dispatched; inspect the active page before retrying", err, condition)
		}
		if s.frameContextID == 0 {
			return nil, &OperationError{Phase: "click_by_handle", Cause: "stale_handle", Detail: "handle " + handle + " no longer resolves — re-annotate or re-extract the page", OriginalError: err.Error()}
		}
		return nil, err
	}
	if state != "clicked" {
		if state == "ambiguous" {
			return nil, &OperationError{Phase: "click_by_handle", Cause: "ambiguous_handle", Detail: "handle " + handle + " matches multiple DOM nodes — re-annotate or re-extract the page", OriginalError: "duplicate data-scout-handle attribute"}
		}
		return nil, &OperationError{Phase: "click_by_handle", Cause: "stale_handle", Detail: "handle " + handle + " no longer resolves to a visible element — re-annotate or re-extract the page", OriginalError: "stale node handle"}
	}
	if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return nil, s.actionOutcomeAfterDispatch("click", handle, "handle click was dispatched; page settling was interrupted", err, condition)
	}
	if condition.count() > 0 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "clicked", handle, condition)
		if verifyErr != nil {
			return nil, s.actionOutcomeAfterDispatch("click", handle, "handle click was dispatched; postcondition could not be verified", verifyErr, condition)
		}
		return result, nil
	}
	return s.actionPageResultUnverifiedContext(ctx, "clicked", handle, "visible element handle resolved and click dispatched")
}

// ClickLabel clicks the element with the given annotation label number.
// Use after AnnotatedScreenshot to interact by label instead of selector.
func (s *Session) ClickLabel(label int) (*PageResult, error) {
	return s.ClickLabelWithPostcondition(label, Postcondition{})
}

// ClickLabelWithPostcondition clicks an annotated element and can verify the
// resulting page state before returning.
func (s *Session) ClickLabelWithPostcondition(label int, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClickLabelWithPostconditionContext(ctx, label, condition)
}

// ClickLabelWithPostconditionContext resolves a screenshot label and clicks it under ctx.
func (s *Session) ClickLabelWithPostconditionContext(ctx context.Context, label int, condition Postcondition) (*PageResult, error) {
	if condition.count() > 0 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
	}
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
	state, err := s.page.EvaluateContext(ctx, fmt.Sprintf(`(function(){function deepAll(root,sel,out){if(!root)return;try{root.querySelectorAll(sel).forEach(e=>out.push(e))}catch(_){}for(const e of (root.querySelectorAll?root.querySelectorAll('*'):[]))if(e.shadowRoot)deepAll(e.shadowRoot,sel,out)}const candidates=[];deepAll(document,'a[href], button, input, textarea, select, [role="button"], [onclick], [tabindex]',candidates);let n=0;for(const e of candidates){const r=e.getBoundingClientRect(),s=getComputedStyle(e);if(r.width<5||r.height<5||s.display==='none'||s.visibility==='hidden')continue;if(++n===%d){const marker=document.createElement('div');marker.setAttribute('data-scout-context-label-marker','%d');marker.textContent=String(n);marker.style.cssText='position:fixed;z-index:999999;pointer-events:none;left:'+Math.max(0,r.x-2)+'px;top:'+Math.max(0,r.y-14)+'px;background:#e63946;color:#fff;font:bold 11px/14px monospace;padding:0 4px;border-radius:7px;min-width:14px;text-align:center;';(document.body||document.documentElement).appendChild(marker);e.setAttribute('data-scout-context-label','%d');return true}}return false})()`, label, label, label))
	if err != nil {
		return nil, err
	}
	found, _ := state.(bool)
	if !found {
		return nil, fmt.Errorf("label %d not found", label)
	}
	selector := "[data-scout-context-label=" + mustJSON(fmt.Sprint(label)) + "]"
	nodeID, err := s.page.QuerySelectorPiercingContext(ctx, selector)
	cleanupMarker := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, _ = s.page.EvaluateContext(cleanupCtx, fmt.Sprintf(`(function(){document.querySelectorAll('[data-scout-context-label="%d"]').forEach(e=>e.removeAttribute('data-scout-context-label'));document.querySelectorAll('[data-scout-context-label-marker="%d"]').forEach(e=>e.remove())})()`, label, label))
	}
	defer cleanupMarker()
	if err != nil {
		return nil, fmt.Errorf("label %d not found: %w", label, err)
	}
	if _, err := formNodeCallContext(ctx, s.page, nodeID, `function(){this.click();return true}`, nil); err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("click", fmt.Sprintf("label:%d", label), "label click command was interrupted; the browser may have received it", err, condition)
		}
		return nil, err
	}
	if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return nil, s.actionOutcomeAfterDispatch("click", fmt.Sprintf("label:%d", label), "label click was dispatched; page settling was interrupted", err, condition)
	}
	target := fmt.Sprintf("label:%d", label)
	if condition.count() > 0 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "clicked", target, condition)
		if verifyErr != nil {
			return nil, s.actionOutcomeAfterDispatch("click", target, "label click was dispatched; postcondition could not be verified", verifyErr, condition)
		}
		return result, nil
	}
	return s.actionPageResultUnverifiedContext(ctx, "clicked", target, "visible element label resolved and click dispatched")
}
