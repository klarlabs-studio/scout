package agent

import (
	"context"
	"fmt"
)

// HumanHandoffRequest describes the active page to a host-owned human viewing
// or approval flow. URL has credentials, query, and fragment removed. Hosts
// should use TargetID to bind their own viewer to the same browser target.
type HumanHandoffRequest struct {
	RunID    string `json:"run_id"`
	TargetID string `json:"target_id"`
	URL      string `json:"url,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// HumanHandoffResult reports what Scout could verify after the host callback
// returned. Callback completion alone does not prove an application effect.
type HumanHandoffResult struct {
	Action           string         `json:"action"`
	TargetID         string         `json:"target_id"`
	Outcome          string         `json:"outcome"`
	Verified         bool           `json:"verified"`
	Evidence         string         `json:"evidence"`
	RecoveryHint     string         `json:"recovery_hint,omitempty"`
	OperationOrdinal int            `json:"operation_ordinal"`
	Receipt          *ActionReceipt `json:"action_receipt,omitempty"`
}

// HumanHandoffFunc lets an application open its own viewer or approval UI and
// wait for a person. Scout releases the session lock while this callback runs.
type HumanHandoffFunc func(context.Context, HumanHandoffRequest) error

// HandoffToHumanContext gives the host a target-bound description of the
// active page, waits for the host's human workflow, then rechecks the same
// target and optionally verifies a page postcondition. Scout does not implement
// a viewing transport, and this callback API does not carry credential fields.
// The callback must respect ctx.
func (s *Session) HandoffToHumanContext(ctx context.Context, reason string, handoff HumanHandoffFunc, postcondition *Postcondition) (*HumanHandoffResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if handoff == nil {
		return nil, fmt.Errorf("human handoff callback is required")
	}
	if postcondition != nil {
		if err := postcondition.Validate(); err != nil {
			return nil, fmt.Errorf("invalid human handoff postcondition: %w", err)
		}
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	targetID := s.page.TargetID()
	urlValue, err := s.page.EvaluateContext(ctx, "window.location.href")
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	currentURL, _ := urlValue.(string)
	request := HumanHandoffRequest{
		RunID: s.runID, TargetID: targetID, URL: redactTraceURL(currentURL), Reason: reason,
	}
	s.mu.Unlock()

	if err := handoff(ctx, request); err != nil {
		return nil, unknownHumanHandoffOutcome(s, ctx, targetID, postcondition, fmt.Errorf("human handoff callback failed: %w", err), "The human handoff ended before Scout could recheck the page; inspect the original target before continuing.")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, unknownHumanHandoffOutcome(s, ctx, targetID, postcondition, err, "The handoff callback returned, but Scout could not reacquire the session to inspect the page; inspect the original target before continuing.")
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, unknownHumanHandoffOutcome(s, ctx, targetID, postcondition, err, "The handoff callback returned, but Scout could not read the browser state; inspect the original target before continuing.")
	}
	if actual := s.page.TargetID(); actual != targetID {
		return nil, unknownHumanHandoffOutcome(s, ctx, targetID, postcondition,
			fmt.Errorf("active target changed during human handoff: expected %s, now %s", targetID, actual),
			fmt.Sprintf("Human interaction was bound to target %s, but the active target is now %s. Inspect both targets before continuing.", targetID, actual))
	}

	result := &HumanHandoffResult{
		Action: "human_handoff", TargetID: targetID,
		Outcome:  string(OutcomeEffectUnverified),
		Evidence: "host human handoff completed; no application postcondition was requested",
	}
	if postcondition != nil {
		verified, err := s.verifyPagePostconditionContextLocked(ctx, result.Action, "human handoff", *postcondition)
		if err != nil {
			return nil, unknownHumanHandoffOutcome(s, ctx, targetID, postcondition, err, "The human handoff completed, but Scout could not verify the requested page state; inspect the original target before continuing.")
		}
		result.Outcome = verified.Outcome
		result.Verified = verified.Verified
		result.Evidence = verified.Evidence
		result.RecoveryHint = verified.RecoveryHint
		result.OperationOrdinal = verified.OperationOrdinal
		result.Receipt = verified.Receipt
		return result, nil
	}
	result.OperationOrdinal = s.nextOperationOrdinal()
	result.Receipt = s.newActionReceipt(result.Action, "human handoff", targetID, result.Outcome, result.Evidence, "Inspect the page and verify the expected state before continuing.", false, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	return result, nil
}

func unknownHumanHandoffOutcome(s *Session, ctx context.Context, targetID string, postcondition *Postcondition, cause error, evidence string) error {
	if s == nil {
		return &OperationError{Phase: "human_handoff", Cause: "session_unavailable", OriginalError: "session is unavailable"}
	}
	ordinal := s.nextOperationOrdinal()
	var expected *Postcondition
	if postcondition != nil {
		copy := *postcondition
		expected = &copy
	}
	receipt := &ActionReceipt{
		SchemaVersion:    1,
		ContentTrust:     ContentTrustUntrustedPage,
		RunID:            s.runID,
		OperationOrdinal: ordinal,
		PolicyID:         taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs),
		Policy:           s.actionPolicyEvidence(),
		Action:           "human_handoff",
		Target:           "human handoff",
		TargetID:         targetID,
		Outcome:          OutcomeUnknown,
		Verification:     VerificationUnknown,
		Evidence:         evidence,
		RecoveryHint:     "Inspect the original browser target and current application state before retrying or continuing.",
		Postcondition:    expected,
	}
	if deadline, ok := ctx.Deadline(); ok {
		receipt.DeadlineAt = deadline.UnixMilli()
	}
	outcomeErr := &ActionOutcomeError{
		ContentTrust: ContentTrustUntrustedPage,
		RunID:        s.runID, Action: "human_handoff", Target: "human handoff", TargetID: targetID,
		OperationOrdinal: ordinal,
		Outcome:          OutcomeUnknown, Evidence: evidence, RecoveryHint: receipt.RecoveryHint,
		ExpectedPostcondition: expected, Cause: cause, Receipt: receipt,
	}
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			outcomeErr.DeadlineAt = deadline.UnixMilli()
		}
	}
	return outcomeErr
}
