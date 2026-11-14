package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	browse "go.klarlabs.de/scout"
)

// PressKey dispatches a key name or modifier chord to the currently focused element.
func (s *Session) PressKey(key string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.PressKeyContext(ctx, key)
}

// PressKeyContext dispatches a key under the caller's deadline. Examples:
// Enter, ArrowDown, and Control+A.
func (s *Session) PressKeyContext(ctx context.Context, key string) (*PageResult, error) {
	return s.PressKeyWithPostconditionContext(ctx, key, Postcondition{})
}

// PressKeyWithPostcondition dispatches a key or chord and optionally verifies
// the resulting page state. A successful dispatch alone remains unverified.
func (s *Session) PressKeyWithPostcondition(key string, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.PressKeyWithPostconditionContext(ctx, key, condition)
}

// PressKeyWithPostconditionContext dispatches a key or chord and optionally
// verifies the resulting page state under ctx.
func (s *Session) PressKeyWithPostconditionContext(ctx context.Context, key string, condition Postcondition) (*PageResult, error) {
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
	start, before := s.traceBeforeActionContext(ctx)
	if err := browse.NewSelection(s.page, 0, "").PressKeyWithModifiersContext(ctx, key, s.heldKeyModifiers); err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.traceActionFailureContext(ctx, start, before, "press_key", key, "", "", s.actionOutcomeAfterDispatch("press_key", key, "key events may have been dispatched but could not be confirmed", err, condition))
		}
		return nil, s.traceActionFailureContext(ctx, start, before, "press_key", key, "", "", err)
	}
	if err := s.page.WaitStableContext(ctx, 100*time.Millisecond); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "press_key", key, "", "", s.actionOutcomeAfterDispatch("press_key", key, "key events were dispatched; page settling was interrupted", err, condition))
	}
	if condition.count() == 1 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "press_key", key, condition)
		if verifyErr != nil {
			return nil, s.traceActionFailureContext(ctx, start, before, "press_key", key, "", "", s.actionOutcomeAfterDispatch("press_key", key, "key events were dispatched; postcondition could not be verified", verifyErr, condition))
		}
		s.traceAfterActionContext(ctx, start, before, "press_key", key, "", result.URL, nil)
		return result, nil
	}
	s.traceAfterActionContext(ctx, start, before, "press_key", key, "", "", nil)
	result, err := s.actionPageResultUnverifiedContext(ctx, "pressed_key", key, "key events dispatched; application effect was not asserted")
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("press_key", key, "key events were dispatched; page result could not be read", err)
	}
	return result, nil
}

// KeyDown holds a key until KeyUp is called. Use a deferred KeyUp in callers
// so modifiers are released when a workflow exits early.
func (s *Session) KeyDown(key string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.KeyDownContext(ctx, key)
}

func (s *Session) KeyDownContext(ctx context.Context, key string) (*PageResult, error) {
	return s.keyControlContext(ctx, key, true)
}

func (s *Session) KeyUp(key string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.KeyUpContext(ctx, key)
}

func (s *Session) KeyUpContext(ctx context.Context, key string) (*PageResult, error) {
	return s.keyControlContext(ctx, key, false)
}

func (s *Session) keyControlContext(ctx context.Context, key string, down bool) (*PageResult, error) {
	if key != "Control" && key != "Shift" && key != "Alt" && key != "Meta" {
		if err := browse.ResolveKeyForControl(key); err != nil {
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
	start, before := s.traceBeforeActionContext(ctx)
	modifierBit := keyModifierBit(key)
	wasHeld := false
	for _, held := range s.heldKeys {
		if held == key {
			wasHeld = true
			break
		}
	}
	if down && wasHeld {
		return nil, fmt.Errorf("key %q is already held", key)
	}
	mask := s.heldKeyModifiers
	if down {
		mask |= modifierBit
	} else {
		mask &^= modifierBit
	}
	if err := browse.NewSelection(s.page, 0, "").KeyControlWithModifiersContext(ctx, key, down, mask); err != nil {
		if down {
			s.heldKeys = append(s.heldKeys, key)
			s.heldKeyModifiers = mask
		}
		action := "key_up"
		if down {
			action = "key_down"
		}
		return nil, s.traceActionFailureContext(ctx, start, before, action, key, "", "", err)
	}
	s.heldKeyModifiers = mask
	if down {
		s.heldKeys = append(s.heldKeys, key)
	} else if wasHeld {
		for i := len(s.heldKeys) - 1; i >= 0; i-- {
			if s.heldKeys[i] == key {
				s.heldKeys = append(s.heldKeys[:i], s.heldKeys[i+1:]...)
				break
			}
		}
	}
	if err := s.page.WaitStableContext(ctx, 50*time.Millisecond); err != nil {
		return nil, err
	}
	action, verb := "key_up", "released_key"
	if down {
		action, verb = "key_down", "held_key"
	}
	s.traceAfterActionContext(ctx, start, before, action, key, "", "", nil)
	return s.actionPageResultUnverifiedContext(ctx, verb, key, "key state changed; application effect was not asserted")
}

func keyModifierBit(key string) int {
	switch key {
	case "Alt":
		return 1
	case "Control":
		return 2
	case "Meta":
		return 4
	case "Shift":
		return 8
	default:
		return 0
	}
}

// releaseHeldKeysLocked clears keys left down by explicit key_down calls.
// The caller holds s.mu; cleanup gets its own short deadline so a cancelled
// request cannot leave the remote browser with stuck keys or modifiers.
func (s *Session) releaseHeldKeysLocked() {
	if s == nil || len(s.heldKeys) == 0 {
		return
	}
	if s.page == nil {
		s.heldKeyModifiers = 0
		s.heldKeys = nil
		return
	}
	mask := s.heldKeyModifiers
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := len(s.heldKeys) - 1; i >= 0; i-- {
		key := s.heldKeys[i]
		mask &^= keyModifierBit(key)
		_ = browse.NewSelection(s.page, 0, "").KeyControlWithModifiersContext(ctx, key, false, mask)
	}
	// Flush the renderer's input queue before Close tears down the page. The
	// CDP dispatch acknowledgment only confirms that Chromium accepted the
	// command; page keyup handlers may still be queued on the renderer thread.
	_, _ = s.page.EvaluateContext(ctx, `void 0`)
	s.heldKeyModifiers = 0
	s.heldKeys = nil
}

// Hover moves the mouse over an element, triggering CSS :hover states and tooltips.
func (s *Session) Hover(selector string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.HoverContext(ctx, selector)
}

// HoverContext moves the pointer to an element using the caller's context.
func (s *Session) HoverContext(ctx context.Context, selector string) (*PageResult, error) {
	return s.HoverWithPostconditionContext(ctx, selector, Postcondition{})
}

// HoverWithPostconditionContext moves the pointer to an element and optionally
// verifies a resulting page condition under the caller's context.
func (s *Session) HoverWithPostconditionContext(ctx context.Context, selector string, condition Postcondition) (*PageResult, error) {
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
	nodeID, err := s.page.QuerySelectorPiercingContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	if err := browse.NewSelection(s.page, nodeID, selector).HoverContext(ctx); err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.traceActionFailureContext(ctx, start, before, "hover", selector, "", "", s.actionOutcomeAfterDispatch("hover", selector, "pointer movement may have been dispatched but could not be confirmed", err, condition))
		}
		return nil, s.traceActionFailureContext(ctx, start, before, "hover", selector, "", "", err)
	}
	if err := s.page.WaitStableContext(ctx, 100*time.Millisecond); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "hover", selector, "", "", s.actionOutcomeAfterDispatch("hover", selector, "pointer moved; hover state could not be verified", err, condition))
	}
	if condition.count() == 1 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "hover", selector, condition)
		if verifyErr != nil {
			return nil, s.traceActionFailureContext(ctx, start, before, "hover", selector, "", "", s.actionOutcomeAfterDispatch("hover", selector, "pointer moved; expected page condition could not be verified", verifyErr, condition))
		}
		s.traceAfterActionContext(ctx, start, before, "hover", selector, "", result.URL, nil)
		return result, nil
	}
	state, err := formNodeCallContext(ctx, s.page, nodeID, `function(){return this.matches(':hover')||Array.from(this.querySelectorAll('*')).some(n=>n.matches(':hover'))}`, nil)
	if err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "hover", selector, "", "", s.actionOutcomeAfterDispatch("hover", selector, "pointer moved; hover state could not be read", err, condition))
	}
	verified, _ := state.(bool)
	s.traceAfterActionContext(ctx, start, before, "hover", selector, "", "", nil)
	if verified {
		result, resultErr := s.actionPageResultContext(ctx, "hovered", selector, "target matched :hover after pointer movement")
		if resultErr != nil {
			return nil, s.actionOutcomeAfterDispatch("hover", selector, "pointer moved and hover was observed; page result could not be read", resultErr, condition)
		}
		return result, nil
	}
	result, resultErr := s.actionPageResultUnverifiedContext(ctx, "hovered", selector, "pointer moved over the target; browser did not report a :hover state")
	if resultErr != nil {
		return nil, s.actionOutcomeAfterDispatch("hover", selector, "pointer moved; page result could not be read", resultErr, condition)
	}
	return result, nil
}

// DoubleClick double-clicks an element.
func (s *Session) DoubleClick(selector string) (*PageResult, error) {
	return s.DoubleClickWithPostcondition(selector, Postcondition{})
}

// DoubleClickWithPostcondition double-clicks a selector and optionally verifies
// the resulting page state before returning.
func (s *Session) DoubleClickWithPostcondition(selector string, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.DoubleClickWithPostconditionContext(ctx, selector, condition)
}

// DoubleClickWithPostconditionContext dispatches dblclick under the request context.
func (s *Session) DoubleClickWithPostconditionContext(ctx context.Context, selector string, condition Postcondition) (*PageResult, error) {
	return s.dispatchMouseLikeContext(ctx, "double_clicked", selector, "dblclick", condition)
}

// RightClick right-clicks an element (opens context menu).
func (s *Session) RightClick(selector string) (*PageResult, error) {
	return s.RightClickWithPostcondition(selector, Postcondition{})
}

// RightClickWithPostcondition right-clicks a selector and optionally verifies
// the resulting page state before returning.
func (s *Session) RightClickWithPostcondition(selector string, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.RightClickWithPostconditionContext(ctx, selector, condition)
}

// RightClickWithPostconditionContext dispatches contextmenu under the request context.
func (s *Session) RightClickWithPostconditionContext(ctx context.Context, selector string, condition Postcondition) (*PageResult, error) {
	return s.dispatchMouseLikeContext(ctx, "right_clicked", selector, "contextmenu", condition)
}

func (s *Session) dispatchMouseLikeContext(ctx context.Context, action, selector, eventType string, condition Postcondition) (*PageResult, error) {
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
	nodeID, err := s.page.QuerySelectorPiercingContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	state, err := formNodeCallContext(ctx, s.page, nodeID, `function(type){let count=0;this.addEventListener(type,()=>count++,{once:true});const accepted=this.dispatchEvent(new MouseEvent(type,{bubbles:true,cancelable:true}));return {observed:count===1,accepted}}`, []any{eventType})
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.traceActionFailureContext(ctx, start, before, action, selector, "", "", s.actionOutcomeAfterDispatch(action, selector, eventType+" dispatch was interrupted; the browser may have received it", err, condition))
		}
		return nil, s.traceActionFailureContext(ctx, start, before, action, selector, "", "", err)
	}
	observed, _ := state.(map[string]any)
	if observed == nil {
		return nil, fmt.Errorf("element %s could not receive %s", selector, eventType)
	}
	if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, action, selector, "", "", s.actionOutcomeAfterDispatch(action, selector, eventType+" was dispatched; page settling was interrupted", err, condition))
	}
	s.traceAfterActionContext(ctx, start, before, action, selector, "", "", nil)
	if condition.count() > 0 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, action, selector, condition)
		if verifyErr != nil {
			return nil, s.actionOutcomeAfterDispatch(action, selector, eventType+" was dispatched; postcondition could not be verified", verifyErr, condition)
		}
		return result, nil
	}
	accepted := observed["accepted"]
	evidence := fmt.Sprintf("target received %s; event dispatch was accepted=%v; application effect was not asserted", eventType, accepted)
	if observed["observed"] == true {
		result, resultErr := s.actionEventObservedContext(ctx, action, selector, evidence)
		if resultErr != nil {
			return nil, s.actionOutcomeAfterDispatch(action, selector, eventType+" was delivered; page result could not be read", resultErr, condition)
		}
		return result, nil
	}
	result, resultErr := s.actionPageResultUnverifiedContext(ctx, action, selector, eventType+" dispatch completed; page postcondition was not specified")
	if resultErr != nil {
		return nil, s.actionOutcomeAfterDispatch(action, selector, eventType+" was dispatched; page result could not be read", resultErr, condition)
	}
	return result, nil
}

// SelectOption selects an option from a <select> element by visible text.
func (s *Session) SelectOption(selector, optionText string) (*ElementResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SelectOptionContext(ctx, selector, optionText)
}

// SelectOptionContext selects and verifies an option under the caller context.
func (s *Session) SelectOptionContext(ctx context.Context, selector, optionText string) (*ElementResult, error) {
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
	nodeID, err := s.page.QuerySelectorPiercingContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	// Resolve the option before dispatch so an interrupted change handler still
	// leaves us with the exact value predicate needed to reconcile safely.
	selected, err := formNodeCallContext(ctx, s.page, nodeID, `function(want){if(this.tagName!=='SELECT')return null;for(const option of this.options){if(option.text.trim()===want||option.value===want)return {value:String(option.value),text:option.text.trim()}}return null}`, []any{optionText})
	if err != nil {
		s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", err)
		return nil, err
	}
	if selected == nil {
		err := fmt.Errorf("option %q not found in %s", optionText, selector)
		s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", err)
		return nil, err
	}
	selection, _ := selected.(map[string]any)
	value, _ := selection["value"].(string)
	text, _ := selection["text"].(string)
	condition := Postcondition{ValueSelector: selector, Value: value}
	selected, err = formNodeCallContext(ctx, s.page, nodeID, `function(want){if(this.tagName!=='SELECT')return null;let found=false;for(const option of this.options){if(String(option.value)===want){found=true;break}}if(!found)return null;const setter=Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype,'value').set;setter.call(this,want);this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}));return {value:String(this.value)}}`, []any{value})
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			outcomeErr := s.actionOutcomeAfterDispatch("select_option", selector, "selection command was interrupted; the selected value may have changed", err, condition)
			s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", outcomeErr)
			return nil, outcomeErr
		}
		s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", err)
		return nil, err
	}
	if selected == nil {
		err := fmt.Errorf("option %q is no longer available in %s", optionText, selector)
		s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", err)
		return nil, err
	}
	selection, _ = selected.(map[string]any)
	value, _ = selection["value"].(string)
	if err := s.page.WaitStableContext(ctx, 50*time.Millisecond); err != nil {
		outcomeErr := s.actionOutcomeAfterDispatch("select_option", selector, "option was selected; value verification was interrupted", err, condition)
		s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", outcomeErr)
		return nil, outcomeErr
	}
	if freshID, refreshErr := s.page.QuerySelectorPiercingContext(ctx, selector); refreshErr == nil {
		nodeID = freshID
	}
	actual, err := formNodeCallContext(ctx, s.page, nodeID, `function(){return String(this.value)}`, nil)
	if err != nil {
		outcomeErr := s.actionOutcomeAfterDispatch("select_option", selector, "option was selected; the committed value could not be read", err, condition)
		s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", outcomeErr)
		return nil, outcomeErr
	}
	observed, _ := actual.(string)
	verified := observed == value
	s.traceAfterActionContext(ctx, start, before, "select_option", selector, optionText, "", nil)
	result := &ElementResult{OperationOrdinal: s.operationOrdinalForResult("select_option", selector), Selector: selector, TargetID: s.page.TargetID(), Value: value, ValueCommitted: observed, Text: text, Action: "selected", Verified: verified, Outcome: elementOutcome(verified), Evidence: "selected option value re-read from the live select element", RecoveryHint: elementRecoveryHint(verified)}
	result.Receipt = s.newActionReceipt("select_option", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, &condition, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("select_option", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
	return result, nil
}

// ScrollTo scrolls to bring an element into view.
func (s *Session) ScrollTo(selector string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ScrollToContext(ctx, selector)
}

// ScrollToContext scrolls a target into view and verifies that its box intersects the viewport.
func (s *Session) ScrollToContext(ctx context.Context, selector string) (*PageResult, error) {
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
	nodeID, err := s.page.QuerySelectorPiercingContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	if _, err = formNodeCallContext(ctx, s.page, nodeID, `function(){this.scrollIntoView({behavior:'smooth',block:'center'})}`, nil); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "scroll_to", selector, "", "", s.actionOutcomeAfterDispatch("scroll_to", selector, "scroll may have been dispatched but viewport state could not be confirmed", err))
	}
	if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "scroll_to", selector, "", "", s.actionOutcomeAfterDispatch("scroll_to", selector, "scroll was dispatched; viewport verification was interrupted", err))
	}
	state, err := formNodeCallContext(ctx, s.page, nodeID, `function(){const r=this.getBoundingClientRect();return r.bottom>0&&r.top<innerHeight&&r.right>0&&r.left<innerWidth}`, nil)
	if err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "scroll_to", selector, "", "", s.actionOutcomeAfterDispatch("scroll_to", selector, "scroll was dispatched; viewport state could not be read", err))
	}
	verified, _ := state.(bool)
	s.traceAfterActionContext(ctx, start, before, "scroll_to", selector, "", "", nil)
	if verified {
		result, resultErr := s.actionPageResultContext(ctx, "scrolled_to", selector, "target bounding box intersects the viewport")
		if resultErr != nil {
			return nil, s.actionOutcomeAfterDispatch("scroll_to", selector, "scroll was dispatched; page result could not be read", resultErr)
		}
		return result, nil
	}
	result, resultErr := s.actionPageResultUnverifiedContext(ctx, "scrolled_to", selector, "scrollIntoView ran but the target did not intersect the viewport")
	if resultErr != nil {
		return nil, s.actionOutcomeAfterDispatch("scroll_to", selector, "scroll was dispatched; page result could not be read", resultErr)
	}
	return result, nil
}

// ScrollBy scrolls the page by a pixel offset.
func (s *Session) ScrollBy(x, y int) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ScrollByContext(ctx, x, y)
}

// ScrollByContext scrolls the page under the caller's context.
func (s *Session) ScrollByContext(ctx context.Context, x, y int) (*PageResult, error) {
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
	target := fmt.Sprintf("%d,%d", x, y)
	start, beforeImage := s.traceBeforeActionContext(ctx)
	state, err := s.page.EvaluateContext(ctx, fmt.Sprintf(`(function(){const before=scrollY;window.scrollBy(%d,%d);return {before,after:scrollY}})()`, x, y))
	if err != nil {
		return nil, s.traceActionFailureContext(ctx, start, beforeImage, "scroll_by", target, "", "", s.actionOutcomeAfterDispatch("scroll_by", target, "scroll may have been dispatched but its position could not be read", err))
	}
	if err := s.page.WaitStableContext(ctx, 200*time.Millisecond); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, beforeImage, "scroll_by", target, "", "", s.actionOutcomeAfterDispatch("scroll_by", target, "scroll was dispatched; settling was interrupted", err))
	}
	s.traceAfterActionContext(ctx, start, beforeImage, "scroll_by", target, "", "", nil)
	values, _ := state.(map[string]any)
	before, after := observationInt(values["before"]), observationInt(values["after"])
	if before != after || y == 0 {
		result, resultErr := s.actionPageResultContext(ctx, "scrolled_by", target, fmt.Sprintf("scrollY changed from %d to %d", before, after))
		if resultErr != nil {
			return nil, s.actionOutcomeAfterDispatch("scroll_by", target, "scroll was dispatched; page result could not be read", resultErr)
		}
		return result, nil
	}
	result, resultErr := s.actionPageResultUnverifiedContext(ctx, "scrolled_by", target, fmt.Sprintf("scroll requested but scrollY remained %d (page may be at a boundary)", after))
	if resultErr != nil {
		return nil, s.actionOutcomeAfterDispatch("scroll_by", target, "scroll was dispatched; page result could not be read", resultErr)
	}
	return result, nil
}

// Focus sets focus on an element (triggers :focus CSS state).
func (s *Session) Focus(selector string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.FocusContext(ctx, selector)
}

// FocusContext focuses an element and verifies the browser's active element.
func (s *Session) FocusContext(ctx context.Context, selector string) (*PageResult, error) {
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
	nodeID, err := s.page.QuerySelectorPiercingContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	state, err := formNodeCallContext(ctx, s.page, nodeID, `function(){this.focus();return document.activeElement===this}`, nil)
	if err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "focus", selector, "", "", s.actionOutcomeAfterDispatch("focus", selector, "focus may have been dispatched but active-element state could not be read", err))
	}
	verified, _ := state.(bool)
	if !verified {
		s.traceAfterActionContext(ctx, start, before, "focus", selector, "", "", nil)
		result, resultErr := s.actionPageResultUnverifiedContext(ctx, "focused", selector, "focus() ran but document.activeElement did not match the requested element")
		if resultErr != nil {
			return nil, s.actionOutcomeAfterDispatch("focus", selector, "focus was dispatched but active-element verification failed", resultErr)
		}
		return result, nil
	}
	s.traceAfterActionContext(ctx, start, before, "focus", selector, "", "", nil)
	result, resultErr := s.actionPageResultContext(ctx, "focused", selector, "document.activeElement matched the requested element")
	if resultErr != nil {
		return nil, s.actionOutcomeAfterDispatch("focus", selector, "focus was verified but page result could not be read", resultErr)
	}
	return result, nil
}

// DragDrop drags an element from one selector to another.
func (s *Session) DragDrop(fromSelector, toSelector string) (*PageResult, error) {
	return s.DragDropWithPostcondition(fromSelector, toSelector, Postcondition{})
}

// DragDropWithPostcondition drags an element and checks an optional page state.
func (s *Session) DragDropWithPostcondition(fromSelector, toSelector string, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.DragDropWithPostconditionContext(ctx, fromSelector, toSelector, condition)
}

// DragDropWithPostconditionContext performs drag-and-drop and verifies its optional postcondition within ctx.
func (s *Session) DragDropWithPostconditionContext(ctx context.Context, fromSelector, toSelector string, condition Postcondition) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if condition.count() > 0 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
	}

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	target := fromSelector + " -> " + toSelector
	start, before := s.traceBeforeActionContext(ctx)

	fromJSON, _ := json.Marshal(fromSelector)
	toJSON, _ := json.Marshal(toSelector)
	js := fmt.Sprintf(`(function() {
		const from = document.querySelector(%s);
		const to = document.querySelector(%s);
		if (!from || !to) return false;

		const fromRect = from.getBoundingClientRect();
		const toRect = to.getBoundingClientRect();
		const fromX = fromRect.x + fromRect.width/2;
		const fromY = fromRect.y + fromRect.height/2;
		const toX = toRect.x + toRect.width/2;
		const toY = toRect.y + toRect.height/2;

		from.dispatchEvent(new DragEvent('dragstart', {bubbles: true, clientX: fromX, clientY: fromY}));
		to.dispatchEvent(new DragEvent('dragenter', {bubbles: true, clientX: toX, clientY: toY}));
		to.dispatchEvent(new DragEvent('dragover', {bubbles: true, clientX: toX, clientY: toY}));
		to.dispatchEvent(new DragEvent('drop', {bubbles: true, clientX: toX, clientY: toY}));
		from.dispatchEvent(new DragEvent('dragend', {bubbles: true, clientX: toX, clientY: toY}));
		return true;
	})()`, fromJSON, toJSON)

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "drag_drop", target, "", "", s.actionOutcomeAfterDispatch("drag_drop", target, "drag events may have been partially dispatched", err, condition))
	}
	if b, ok := result.(bool); !ok || !b {
		return nil, fmt.Errorf("drag from %s to %s failed", fromSelector, toSelector)
	}

	if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return nil, s.traceActionFailureContext(ctx, start, before, "drag_drop", target, "", "", s.actionOutcomeAfterDispatch("drag_drop", target, "drag events were dispatched; settling was interrupted", err, condition))
	}
	s.traceAfterActionContext(ctx, start, before, "drag_drop", target, "", "", nil)
	if condition.count() > 0 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "dragged", target, condition)
		if verifyErr != nil {
			return nil, s.actionOutcomeAfterDispatch("drag_drop", fromSelector+" -> "+toSelector, "drag events were dispatched; postcondition could not be verified", verifyErr, condition)
		}
		return result, nil
	}
	pageResult, resultErr := s.actionPageResultUnverifiedContext(ctx, "dragged", target, "drag and drop events dispatched; destination state was not independently asserted")
	if resultErr != nil {
		return nil, s.actionOutcomeAfterDispatch("drag_drop", fromSelector+" -> "+toSelector, "drag events were dispatched; page result could not be read", resultErr, condition)
	}
	return pageResult, nil
}
