package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const ActionRecoverySchemaVersion = 3

// ActionRecoveryRecord is the portable, versioned handoff for an action whose
// browser effect could not be confirmed. It can be passed between Scout's Go,
// CLI, and MCP interfaces. Records may contain selectors, page evidence, and
// postcondition text; callers should protect them like other browser state.
// A record never authorizes replay.
type ActionRecoveryRecord struct {
	SchemaVersion    int                `json:"schema_version"`
	ContentTrust     string             `json:"content_trust,omitempty"`
	CreatedAt        string             `json:"created_at"`
	Code             string             `json:"code"`
	Message          string             `json:"message"`
	OperationOrdinal int                `json:"operation_ordinal,omitempty"`
	ActionOutcome    ActionOutcomeError `json:"action_outcome"`
}

// NewActionRecoveryRecord converts an unknown action outcome into the current
// portable recovery schema.
func NewActionRecoveryRecord(outcome *ActionOutcomeError) (ActionRecoveryRecord, error) {
	if outcome == nil {
		return ActionRecoveryRecord{}, fmt.Errorf("action outcome is nil")
	}
	safeOutcome := *outcome
	// Partial results can contain user-entered form values. Keep the portable
	// recovery artifact focused on the action identity and postcondition; the
	// caller still receives PartialResult in the immediate error response.
	safeOutcome.PartialResult = nil
	if safeOutcome.URL != "" {
		safeOutcome.URL = redactTraceURL(safeOutcome.URL)
	}
	if safeOutcome.ResponseEvidence != nil {
		response := *safeOutcome.ResponseEvidence
		response.URL = redactTraceURL(response.URL)
		safeOutcome.ResponseEvidence = &response
	}
	record := ActionRecoveryRecord{
		SchemaVersion:    ActionRecoverySchemaVersion,
		ContentTrust:     ContentTrustUntrustedPage,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Code:             "SCOUT_ACTION_OUTCOME_UNKNOWN",
		Message:          "The action may have completed, but its resulting state could not be verified.",
		OperationOrdinal: outcome.OperationOrdinal,
		ActionOutcome:    safeOutcome,
	}
	if err := record.Validate(); err != nil {
		return ActionRecoveryRecord{}, err
	}
	return record, nil
}

// DecodeActionRecoveryRecord parses and validates a portable recovery record.
func DecodeActionRecoveryRecord(data []byte) (ActionRecoveryRecord, error) {
	var record ActionRecoveryRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return ActionRecoveryRecord{}, fmt.Errorf("decode action recovery record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ActionRecoveryRecord{}, fmt.Errorf("decode action recovery record: trailing JSON value")
		}
		return ActionRecoveryRecord{}, fmt.Errorf("decode action recovery record: trailing content: %w", err)
	}
	if err := record.Validate(); err != nil {
		return ActionRecoveryRecord{}, err
	}
	return record, nil
}

// Validate checks that the record contains enough identity to safely inspect
// the original target. Reconciliation still verifies the live target and
// postcondition before reporting success.
func (r ActionRecoveryRecord) Validate() error {
	if r.SchemaVersion != ActionRecoverySchemaVersion {
		return fmt.Errorf("unsupported action recovery schema version %d", r.SchemaVersion)
	}
	if r.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || r.ActionOutcome.Outcome != OutcomeUnknown || r.ActionOutcome.Action == "" || r.ActionOutcome.TargetID == "" || r.ActionOutcome.BrowserInstanceID == "" || r.OperationOrdinal <= 0 {
		return fmt.Errorf("action recovery record is incomplete")
	}
	if r.OperationOrdinal != r.ActionOutcome.OperationOrdinal {
		return fmt.Errorf("action recovery operation ordinal does not match its outcome")
	}
	if r.CreatedAt == "" {
		return fmt.Errorf("action recovery record has no creation timestamp")
	}
	if r.ActionOutcome.ExpectedPostcondition != nil {
		if err := r.ActionOutcome.ExpectedPostcondition.Validate(); err != nil {
			return fmt.Errorf("action recovery record has invalid postcondition: %w", err)
		}
		if absent := r.ActionOutcome.ExpectedPostcondition.TargetAbsent; absent != "" && (r.ActionOutcome.Action != "close_tab" || absent != r.ActionOutcome.TargetID) {
			return fmt.Errorf("action recovery target_absent postcondition must match a close_tab target")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err != nil {
		return fmt.Errorf("action recovery record has invalid creation timestamp: %w", err)
	}
	return nil
}

// Outcome returns a copy of the captured typed outcome for inspection or
// read-only reconciliation. The returned value carries no replay instruction.
func (r ActionRecoveryRecord) Outcome() *ActionOutcomeError {
	outcome := r.ActionOutcome
	return &outcome
}

// ReconcileActionRecoveryRecordContext validates a portable record and checks
// its saved postcondition without replaying the action. Page conditions are
// checked on the original live target; close_tab target absence is checked
// against browser target inventory.
func (s *Session) ReconcileActionRecoveryRecordContext(ctx context.Context, record ActionRecoveryRecord) (*PageResult, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return s.ReconcileActionRecoveryOutcomeContext(ctx, record.Outcome())
}
