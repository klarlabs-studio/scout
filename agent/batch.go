package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	browse "go.klarlabs.de/scout"
)

// MaxBatchInputBytes bounds serialized action input so recovery data derived
// from batch predicates can remain inspectable within MCP's response budget.
const MaxBatchInputBytes = 96 * 1024

// BatchOptions controls how a batch handles uncertain and unverified outcomes.
type BatchOptions struct {
	// ContinueOnUnknown allows later actions to run after an action may have
	// taken effect but could not be verified. The default is to stop and require
	// the caller to reconcile before deciding whether to retry or continue.
	ContinueOnUnknown bool
	// RequireVerified stops the batch when a step does not have positive
	// verification evidence. Actions without a postcondition are not verified.
	RequireVerified bool
	// OnActionProgress durably records action boundaries for task recovery.
	// It is called before an action is dispatched and after its result is read.
	OnActionProgress func(index int, status string) error
	// FinalPostcondition is checked after all actions while the session lock is
	// still held.
	FinalPostcondition      *Postcondition
	beforeFinalVerification func() error
}

// BatchStoppedError reports that execution stopped because an action outcome
// was unknown or a required verification contract was not met. The
// accompanying BatchResult is partial and identifies the first unattempted
// action.
type BatchStoppedError struct {
	Result *BatchResult
}

func (e *BatchStoppedError) Error() string {
	if e == nil || e.Result == nil {
		return "batch stopped after an action with an unknown outcome"
	}
	if e.Result.StopReason == "verification_required" {
		if e.Result.ResumeBlocked {
			return "batch stopped because require_verified was set and a step has an unknown outcome; reconcile before continuing"
		}
		return "batch stopped because require_verified was set and a step was not positively verified"
	}
	if e.Result.StoppedOnUnknownAt == nil {
		return "batch stopped after an action with an unknown outcome"
	}
	return fmt.Sprintf("batch stopped after action %d with an unknown outcome; reconcile before retrying or continuing", *e.Result.StoppedOnUnknownAt)
}

type batchPartialActionError struct {
	result *SemanticFillResult
	cause  error
}

func (e *batchPartialActionError) Error() string { return e.cause.Error() }
func (e *batchPartialActionError) Unwrap() error { return e.cause }

func (s *Session) ExecuteBatch(actions []BatchAction) (*BatchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ExecuteBatchContext(ctx, actions)
}

// ExecuteBatchContext executes a serialized batch within the caller's deadline.
func (s *Session) ExecuteBatchContext(ctx context.Context, actions []BatchAction) (*BatchResult, error) {
	return s.ExecuteBatchWithOptionsContext(ctx, actions, BatchOptions{})
}

// ExecuteBatchWithOptionsContext executes a serialized batch using the caller's
// deadline and the selected uncertainty policy.
func (s *Session) ExecuteBatchWithOptionsContext(ctx context.Context, actions []BatchAction, options BatchOptions) (*BatchResult, error) {
	if len(actions) > 100 {
		return nil, fmt.Errorf("execute batch accepts at most 100 actions (got %d)", len(actions))
	}
	if options.RequireVerified {
		if err := validateVerifiedBatch(actions); err != nil {
			return nil, err
		}
	}
	if options.FinalPostcondition != nil {
		if err := options.FinalPostcondition.Validate(); err != nil {
			return nil, fmt.Errorf("invalid final postcondition: %w", err)
		}
	}
	if batchRawStringBytes(actions) > MaxBatchInputBytes {
		return nil, fmt.Errorf("execute batch input exceeds %d bytes; split it into smaller batches", MaxBatchInputBytes)
	}
	input, err := json.Marshal(actions)
	if err != nil {
		return nil, fmt.Errorf("encode batch input: %w", err)
	}
	if len(input) > MaxBatchInputBytes {
		return nil, fmt.Errorf("execute batch input exceeds %d bytes (got %d); split it into smaller batches", MaxBatchInputBytes, len(input))
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if err := s.checkExpectedPageURLContext(ctx); err != nil {
		return nil, err
	}
	if err := s.checkExpectedFrameIDContext(ctx); err != nil {
		return nil, err
	}
	if err := s.checkExpectedTaskDocumentContext(ctx); err != nil {
		return nil, err
	}

	result := &BatchResult{
		ContentTrust: ContentTrustUntrustedPage,
		Total:        len(actions),
		Results:      make([]BatchActionResult, 0, len(actions)),
	}

	for i, action := range actions {
		if err := ctx.Err(); err != nil {
			result.NextActionIndex = i
			return result, err
		}
		if options.OnActionProgress != nil {
			if err := options.OnActionProgress(i, "started"); err != nil {
				result.NextActionIndex = i
				return result, fmt.Errorf("persist task action start: %w", err)
			}
		}
		// Revalidate after the durable action-start checkpoint. That callback can
		// block on storage, leaving time for an external CDP client to navigate
		// the same target after the batch's initial binding check.
		if err := s.checkExpectedPageURLContext(ctx); err != nil {
			result.NextActionIndex = i
			return result, err
		}
		if err := s.checkExpectedFrameIDContext(ctx); err != nil {
			result.NextActionIndex = i
			return result, err
		}
		if err := s.checkExpectedTaskDocumentContext(ctx); err != nil {
			result.NextActionIndex = i
			return result, err
		}
		ar := BatchActionResult{
			Index:    i,
			TargetID: s.page.TargetID(),
			Action:   action.Action,
		}

		condition := Postcondition{
			Text: action.ExpectedText, URLContains: action.ExpectedURLContains,
			VisibleSelector: action.ExpectedVisibleTarget, ValueSelector: action.ExpectedValueSelector,
			Value: action.ExpectedValue, CountSelector: action.ExpectedCountSelector, Count: action.ExpectedCount,
		}
		conditionCount := 0
		if condition.Text != "" {
			conditionCount++
		}
		if condition.URLContains != "" {
			conditionCount++
		}
		if condition.VisibleSelector != "" {
			conditionCount++
		}
		if condition.ValueSelector != "" || condition.Value != "" {
			conditionCount++
		}
		if condition.CountSelector != "" || condition.Count != nil {
			conditionCount++
		}
		err := error(nil)
		var verifiedAction *PageResult
		switch conditionCount {
		case 0:
		case 1:
			err = condition.Validate()
		default:
			err = fmt.Errorf("batch action accepts exactly one postcondition: expected_text, expected_url_contains, expected_visible_selector, expected_value_selector/expected_value, or expected_count_selector/expected_count")
		}
		if err == nil {
			if action.Action == "webmcp_invoke" && conditionCount == 1 {
				verifiedAction, err = s.invokeWebMCPToolLockedContext(ctx, s.page, s.page.TargetID(), action.WebMCPFrameID, action.WebMCPName, action.WebMCPInput, condition)
			} else {
				err = s.executeSingleActionContext(ctx, action)
			}
		}
		//nolint:gocritic // This branch layout is clearer for this multi-condition check.
		if err != nil {
			ar.Error = err.Error()
			var outcomeErr *ActionOutcomeError
			if errors.As(err, &outcomeErr) {
				if conditionCount == 1 {
					conditionCopy := condition
					outcomeErr.ExpectedPostcondition = &conditionCopy
					if outcomeErr.Receipt != nil {
						receiptCondition := conditionCopy
						outcomeErr.Receipt.Postcondition = &receiptCondition
					}
				}
				ar.Outcome = string(outcomeErr.Outcome)
				ar.Target = outcomeErr.Target
				ar.Evidence = outcomeErr.Evidence
				ar.RecoveryHint = outcomeErr.RecoveryHint
				ar.PartialResult = outcomeErr.PartialResult
				if outcomeErr.Outcome == OutcomeUnknown {
					result.Unknown++
					if recovery, recoveryErr := NewActionRecoveryRecord(outcomeErr); recoveryErr == nil {
						ar.RecoveryRecord = &recovery
					}
				} else {
					result.Rejected++
				}
			} else {
				var partial *batchPartialActionError
				if errors.As(err, &partial) {
					ar.Outcome = partial.result.Outcome
					ar.TargetID = partial.result.TargetID
					ar.Evidence = partial.result.Evidence
					ar.RecoveryHint = partial.result.RecoveryHint
					ar.PartialResult = partial.result
					if partial.result.Outcome == string(OutcomeUnknown) {
						result.Unknown++
					} else {
						result.Rejected++
					}
				} else {
					ar.Outcome = "action_failed"
					ar.RecoveryHint = "Inspect the reported action error and current page state, then retry the failed step."
					result.Rejected++
				}
			}
			result.Failed++
		} else if conditionCount == 1 && verifiedAction != nil {
			ar.Success, ar.Verified, ar.Outcome = verifiedAction.Verified, verifiedAction.Verified, verifiedAction.Outcome
			ar.Target, ar.TargetID, ar.Evidence, ar.RecoveryHint = verifiedAction.Target, verifiedAction.TargetID, verifiedAction.Evidence, verifiedAction.RecoveryHint
			ar.Receipt = verifiedAction.Receipt
			if ar.Verified {
				result.Succeeded++
				result.Verified++
			} else {
				ar.Error = verifiedAction.Evidence
				result.Failed++
				result.Rejected++
			}
		} else if conditionCount == 1 {
			verified, verifyErr := s.verifyPagePostconditionContextLocked(ctx, action.Action, batchActionTarget(action), condition)
			if verifyErr != nil {
				ar.Error = verifyErr.Error()
				ar.Outcome = string(OutcomeUnknown)
				ar.Target = batchActionTarget(action)
				ar.Evidence = "batch action was dispatched, but its postcondition could not be verified: " + verifyErr.Error()
				ar.RecoveryHint = "Inspect the current page and session status before retrying; the batch action may already have completed."
				result.Failed++
				result.Unknown++
			} else {
				ar.Success, ar.Verified, ar.Outcome = verified.Verified, verified.Verified, verified.Outcome
				ar.Target, ar.Evidence, ar.RecoveryHint = verified.Target, verified.Evidence, verified.RecoveryHint
				if !verified.Verified {
					ar.Error = verified.Evidence
					result.Failed++
					result.Rejected++
				} else {
					result.Succeeded++
					result.Verified++
				}
			}
		} else {
			ar.Success = true
			ar.Target = batchActionTarget(action)
			switch action.Action {
			case "wait":
				ar.Verified = true
				ar.Outcome = string(OutcomePostconditionVerified)
				ar.Evidence = "the requested selector was observed before the wait completed"
			case "fill_form_semantic":
				ar.Verified = true
				ar.Outcome = string(OutcomeValueVerified)
				ar.Evidence = "each requested form field matched its value after browser readback"
			default:
				ar.Outcome = string(OutcomeEffectUnverified)
				ar.Evidence = "batch action completed; resulting application state was not asserted"
				ar.RecoveryHint = "Inspect the page or add a postcondition after the batch to verify the intended effect."
			}
			result.Succeeded++
			if ar.Verified {
				result.Verified++
			} else {
				result.Unverified++
			}
		}
		if err != nil {
			var outcomeErr *ActionOutcomeError
			if errors.As(err, &outcomeErr) {
				ar.Receipt = outcomeErr.Receipt
			}
		}
		if ar.Receipt == nil {
			ordinal := s.operationOrdinalForResult(action.Action, ar.Target)
			var postcondition *Postcondition
			if conditionCount == 1 {
				copy := condition
				postcondition = &copy
			}
			ar.Receipt = s.newActionReceipt(ar.Action, ar.Target, ar.TargetID, ar.Outcome, ar.Evidence, ar.RecoveryHint, ar.Verified, ordinal, postcondition, ctx)
			actionReceiptDeadline(ctx, ar.Receipt)
		}
		if ar.Outcome == string(OutcomeUnknown) && ar.RecoveryRecord == nil && ar.TargetID != "" && ar.Receipt != nil {
			unknown := &ActionOutcomeError{
				ContentTrust: ContentTrustUntrustedPage,
				RunID:        ar.Receipt.RunID, OperationOrdinal: ar.Receipt.OperationOrdinal,
				Action: ar.Action, Target: ar.Target, TargetID: ar.TargetID,
				FrameID: ar.Receipt.FrameID, Outcome: OutcomeUnknown,
				Evidence: ar.Evidence, RecoveryHint: ar.RecoveryHint,
				ExpectedPostcondition: ar.Receipt.Postcondition, Receipt: ar.Receipt,
			}
			if recovery, recoveryErr := NewActionRecoveryRecord(unknown); recoveryErr == nil {
				ar.RecoveryRecord = &recovery
			}
		}

		result.Results = append(result.Results, ar)
		result.NextActionIndex = i + 1
		if options.OnActionProgress != nil {
			if err := options.OnActionProgress(i, "finished"); err != nil {
				return result, fmt.Errorf("persist task action result: %w", err)
			}
		}
		stopForContract := false
		if options.RequireVerified && !ar.Verified {
			stoppedAt := i
			result.StoppedOnContractAt = &stoppedAt
			result.StopReason = "verification_required"
			stopForContract = true
			result.ResumeBlocked = ar.Outcome == string(OutcomeUnknown)
			if result.ResumeBlocked {
				result.StoppedOnUnknownAt = &stoppedAt
			}
		} else if !options.ContinueOnUnknown && ar.Outcome == string(OutcomeUnknown) {
			stoppedAt := i
			result.StoppedOnUnknownAt = &stoppedAt
			result.StopReason = "unknown_action_outcome"
			result.ResumeBlocked = true
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if stopForContract || result.ResumeBlocked {
			return result, &BatchStoppedError{Result: result}
		}
	}

	if options.FinalPostcondition != nil {
		if options.beforeFinalVerification != nil {
			if err := options.beforeFinalVerification(); err != nil {
				return result, fmt.Errorf("persist final verification phase: %w", err)
			}
		}
		verification, err := s.verifyPagePostconditionContextLocked(ctx, "task_final", "task final postcondition", *options.FinalPostcondition)
		result.FinalVerification = verification
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// checkExpectedTaskBindingContext revalidates the task's browser binding as
// close as possible to a batch action's first mutating CDP command. External
// CDP clients can still race after this check because CDP offers no transaction
// spanning identity inspection and input dispatch.
func (s *Session) checkExpectedTaskBindingContext(ctx context.Context) error {
	if err := s.checkExpectedPageURLContext(ctx); err != nil {
		return err
	}
	if err := s.checkExpectedFrameIDContext(ctx); err != nil {
		return err
	}
	return s.checkExpectedTaskDocumentContext(ctx)
}

// validateVerifiedBatch checks the complete strict batch contract before any
// session or browser work begins. This avoids dispatching early actions before
// discovering a later step cannot satisfy the requested evidence contract.
func validateVerifiedBatch(actions []BatchAction) error {
	if len(actions) == 0 {
		return fmt.Errorf("require_verified needs at least one batch action")
	}
	for i, action := range actions {
		condition := Postcondition{
			Text: action.ExpectedText, URLContains: action.ExpectedURLContains,
			VisibleSelector: action.ExpectedVisibleTarget, ValueSelector: action.ExpectedValueSelector,
			Value: action.ExpectedValue, CountSelector: action.ExpectedCountSelector, Count: action.ExpectedCount,
		}
		conditionCount := 0
		if condition.Text != "" {
			conditionCount++
		}
		if condition.URLContains != "" {
			conditionCount++
		}
		if condition.VisibleSelector != "" {
			conditionCount++
		}
		if condition.ValueSelector != "" || condition.Value != "" {
			conditionCount++
		}
		if condition.CountSelector != "" || condition.Count != nil {
			conditionCount++
		}
		if conditionCount > 1 {
			return fmt.Errorf("batch action %d accepts exactly one postcondition", i)
		}
		if conditionCount == 1 {
			if err := condition.Validate(); err != nil {
				return fmt.Errorf("batch action %d has an invalid postcondition: %w", i, err)
			}
		}

		builtInVerified := false
		switch action.Action {
		case "click":
			if action.Selector == "" {
				return fmt.Errorf("batch action %d: click action requires a selector", i)
			}
		case "type":
			if action.Selector == "" {
				return fmt.Errorf("batch action %d: type action requires a selector", i)
			}
		case "fill_form_semantic":
			if len(action.Fields) == 0 {
				return fmt.Errorf("batch action %d: fill_form_semantic action requires fields", i)
			}
			builtInVerified = true
		case "wait":
			if action.Selector == "" {
				return fmt.Errorf("batch action %d: wait action requires a selector", i)
			}
			builtInVerified = true
		case "scroll_to":
			if action.Selector == "" {
				return fmt.Errorf("batch action %d: scroll_to action requires a selector", i)
			}
		case "click_label":
			if action.Label <= 0 {
				return fmt.Errorf("batch action %d: click_label action requires a positive label number", i)
			}
		case "webmcp_invoke":
			if action.WebMCPName == "" || action.WebMCPFrameID == "" {
				return fmt.Errorf("batch action %d: webmcp_invoke requires webmcp_name and webmcp_frame_id", i)
			}
		default:
			return fmt.Errorf("batch action %d: unknown batch action %q", i, action.Action)
		}
		if conditionCount == 0 && !builtInVerified {
			return fmt.Errorf("batch action %d (%s) requires one postcondition when require_verified is enabled", i, action.Action)
		}
	}
	return nil
}

// batchRawStringBytes provides a cheap lower bound before exact JSON encoding.
// It prevents an oversized string from causing a second full-size allocation
// merely to reject the batch; JSON escaping and structural bytes are accounted
// for by the exact check that follows.
func batchRawStringBytes(actions []BatchAction) int {
	total := 0
	add := func(value string) {
		if total > MaxBatchInputBytes {
			return
		}
		if len(value) > MaxBatchInputBytes-total {
			total = MaxBatchInputBytes + 1
			return
		}
		total += len(value)
	}
	for _, action := range actions {
		add(action.Action)
		add(action.Selector)
		add(action.Value)
		add(action.WebMCPName)
		add(action.WebMCPFrameID)
		add(action.ExpectedText)
		add(action.ExpectedURLContains)
		add(action.ExpectedVisibleTarget)
		add(action.ExpectedValueSelector)
		add(action.ExpectedValue)
		add(action.ExpectedCountSelector)
		for name, value := range action.Fields {
			add(name)
			add(value)
		}
	}
	return total
}

func batchActionTarget(action BatchAction) string {
	if action.Selector != "" {
		return action.Selector
	}
	if action.Action == "click_label" {
		return fmt.Sprintf("label:%d", action.Label)
	}
	if action.Action == "webmcp_invoke" {
		return action.WebMCPName
	}
	return ""
}

func (s *Session) executeSingleActionContext(ctx context.Context, action BatchAction) error {
	switch action.Action {
	case "click":
		return s.batchClickContext(ctx, action.Selector)
	case "type":
		return s.batchTypeContext(ctx, action.Selector, action.Value)
	case "fill_form_semantic":
		return s.batchFillFormSemanticContext(ctx, action.Fields)
	case "wait":
		return s.waitForSelectorContext(ctx, action.Selector)
	case "scroll_to":
		return s.batchScrollToContext(ctx, action.Selector)
	case "click_label":
		return s.batchClickLabelContext(ctx, action.Label)
	case "webmcp_invoke":
		return fmt.Errorf("WebMCP invocation requires a verified batch postcondition")
	default:
		return fmt.Errorf("unknown batch action %q", action.Action)
	}
}

func (s *Session) batchClickContext(ctx context.Context, selector string) error {
	if selector == "" {
		return fmt.Errorf("click action requires a selector")
	}

	nodeID, err := s.resolveSelectorContext(ctx, selector)
	if err != nil {
		return err
	}
	sel := browse.NewSelection(s.page, nodeID, selector)
	err = sel.ClickContextWithGuard(ctx, func() error {
		return s.checkExpectedTaskBindingContext(ctx)
	})
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return s.actionOutcomeAfterDispatch("click", selector, "batch click command was interrupted; the browser may have received it", err)
		}
		return err
	}

	if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return s.actionOutcomeAfterDispatch("click", selector, "batch click was dispatched; page settling was interrupted", err)
	}
	s.recordAction(Action{Type: "click", Selector: selector})
	s.addHistory("click", selector, "", "")
	return nil
}

func (s *Session) batchTypeContext(ctx context.Context, selector, text string) error {
	if selector == "" {
		return fmt.Errorf("type action requires a selector")
	}
	if err := s.checkExpectedTaskBindingContext(ctx); err != nil {
		return err
	}

	selectorJSON, _ := json.Marshal(selector)
	textJSON, _ := json.Marshal(text)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return false;
		el.focus();
		// Use native setter to trigger React/Vue/Angular synthetic event systems.
		const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype
			: el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype
			: HTMLInputElement.prototype;
		const nativeSetter = Object.getOwnPropertyDescriptor(proto, 'value');
		if (nativeSetter && nativeSetter.set) {
			nativeSetter.set.call(el, %s);
		} else {
			el.value = %s;
		}
		el.dispatchEvent(new Event('input', {bubbles: true}));
		el.dispatchEvent(new Event('change', {bubbles: true}));
		return true;
	})()`, selectorJSON, textJSON, textJSON)

	result, err := s.evaluateInFrameContext(ctx, s.frameContextID, js)
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return s.actionOutcomeAfterDispatch("type", selector, "batch input command was interrupted; the field may have changed", err)
		}
		return err
	}
	if b, ok := result.(bool); !ok || !b {
		return fmt.Errorf("element %s not found", selector)
	}

	s.recordAction(Action{Type: "type", Selector: selector, Value: text})
	s.addHistory("type", selector, "", text)
	return nil
}

func (s *Session) batchFillFormSemanticContext(ctx context.Context, fields map[string]string) error {
	if len(fields) == 0 {
		return fmt.Errorf("fill_form_semantic action requires fields")
	}
	if err := s.checkExpectedTaskBindingContext(ctx); err != nil {
		return err
	}

	discovery, err := s.discoverFormInternalContext(ctx, "")
	if err != nil {
		return err
	}

	fillResult := &SemanticFillResult{ContentTrust: ContentTrustUntrustedPage, TargetID: s.page.TargetID(), Fields: make([]SemanticFieldResult, 0, len(fields)), Success: true}
	var errs []string
	names := make([]string, 0, len(fields))
	for humanName := range fields {
		names = append(names, humanName)
	}
	sort.Strings(names)
	for _, humanName := range names {
		if err := ctx.Err(); err != nil {
			if len(fillResult.Fields) == 0 {
				return err
			}
			return s.semanticFillOutcomeAfterDispatch(fillResult, humanName, "batch form fill was interrupted; one or more earlier fields may have changed", err)
		}
		if err := s.checkExpectedTaskBindingContext(ctx); err != nil {
			if len(fillResult.Fields) == 0 {
				return err
			}
			return s.semanticFillOutcomeAfterDispatch(fillResult, humanName, "task browser binding changed after one or more earlier fields may have changed", err)
		}
		fieldResult := s.fillSemanticFieldContext(ctx, humanName, fields[humanName], discovery.Fields, func() error {
			return s.checkExpectedTaskBindingContext(ctx)
		})
		fillResult.Fields = append(fillResult.Fields, fieldResult)
		if err := ctx.Err(); err != nil {
			return s.semanticFillOutcomeAfterDispatch(fillResult, humanName, "batch form fill was interrupted; one or more earlier fields may have changed", err)
		}
		if fieldResult.Outcome == string(OutcomeUnknown) {
			cause := errors.New(fieldResult.Error)
			if fieldResult.Error == "" {
				cause = errors.New("field outcome could not be confirmed")
			}
			return s.semanticFillOutcomeAfterDispatch(fillResult, fieldResult.Selector, "a batch form field may have changed but its value could not be confirmed", cause)
		}
		if !fieldResult.Success {
			fillResult.Success = false
			errs = append(errs, fmt.Sprintf("field %q: %s", humanName, fieldResult.Error))
		}
	}

	if len(errs) > 0 {
		fillResult.Outcome = string(OutcomeValueMismatch)
		fillResult.Evidence = "some semantic form fields were updated while one or more field outcomes failed; inspect the per-field results before retrying"
		fillResult.RecoveryHint = "Review each field result and the current form values, then retry only fields that still need changes."
		for _, field := range fillResult.Fields {
			if field.Outcome == string(OutcomeActionFailed) {
				fillResult.Outcome = string(OutcomeActionFailed)
				break
			}
		}
		return &batchPartialActionError{result: fillResult, cause: fmt.Errorf("some fields failed: %s", errs[0])}
	}
	return nil
}

func (s *Session) batchScrollToContext(ctx context.Context, selector string) error {
	if selector == "" {
		return fmt.Errorf("scroll_to action requires a selector")
	}
	if err := s.checkExpectedTaskBindingContext(ctx); err != nil {
		return err
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return false;
		el.scrollIntoView({behavior: 'smooth', block: 'center'});
		return true;
	})()`, selectorJSON)

	result, err := s.evaluateInFrameContext(ctx, s.frameContextID, js)
	if err != nil {
		return err
	}
	if b, ok := result.(bool); !ok || !b {
		return fmt.Errorf("element %s not found", selector)
	}

	return s.page.WaitStableContext(ctx, 300*time.Millisecond)
}

func (s *Session) batchClickLabelContext(ctx context.Context, label int) error {
	if label <= 0 {
		return fmt.Errorf("click_label action requires a positive label number")
	}
	if err := s.checkExpectedTaskBindingContext(ctx); err != nil {
		return err
	}

	js := fmt.Sprintf(`(function() {
		let idx = 0;
		const selectors = 'a[href], button, input, textarea, select, [role="button"], [onclick], [tabindex]';
		for (const el of document.querySelectorAll(selectors)) {
			const rect = el.getBoundingClientRect();
			if (rect.width < 5 || rect.height < 5) continue;
			const style = window.getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') continue;
			idx++;
			if (idx === %d) {
				el.click();
				return true;
			}
		}
		return false;
	})()`, label)

	result, err := s.evaluateInFrameContext(ctx, s.frameContextID, js)
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return s.actionOutcomeAfterDispatch("click", fmt.Sprintf("label:%d", label), "batch label click command was interrupted; the browser may have received it", err)
		}
		return err
	}
	if b, ok := result.(bool); !ok || !b {
		return fmt.Errorf("label %d not found", label)
	}

	if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return s.actionOutcomeAfterDispatch("click", fmt.Sprintf("label:%d", label), "batch label click was dispatched; page settling was interrupted", err)
	}
	s.addHistory("click_label", "", "", fmt.Sprintf("%d", label))
	return nil
}
