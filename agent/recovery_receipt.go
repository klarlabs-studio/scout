package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const RecoveryReceiptSchema = "scout.recovery-receipt/v1"

// RecoveryOperationID returns a delimiter-safe, reversible operation ID for a
// task and its one-based step. Encoding the task ID prevents separators inside
// caller-selected IDs from making the ordinal ambiguous to other runtimes.
func RecoveryOperationID(taskID string, ordinal int) string {
	return "scout-op-v1." + base64.RawURLEncoding.EncodeToString([]byte(taskID)) + "." + strconv.Itoa(ordinal)
}

func parseRecoveryOperationID(operationID string) (string, int, error) {
	parts := strings.Split(operationID, ".")
	if len(parts) != 3 || parts[0] != "scout-op-v1" || parts[1] == "" {
		return "", 0, fmt.Errorf("invalid recovery operation ID")
	}
	taskID, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(taskID) == 0 || base64.RawURLEncoding.EncodeToString(taskID) != parts[1] {
		return "", 0, fmt.Errorf("invalid recovery operation ID task encoding")
	}
	ordinal, err := strconv.Atoi(parts[2])
	if err != nil || ordinal < 1 || strconv.Itoa(ordinal) != parts[2] {
		return "", 0, fmt.Errorf("invalid recovery operation ID ordinal")
	}
	return string(taskID), ordinal, nil
}

// RecoveryReceipt is the portable, read-only envelope for one task operation.
// It carries no action inputs and never authorizes replay. Scout's browser
// generation is reserved for an independently issued browser-owner token.
// Scout's local Engine fingerprint belongs in InstanceID and is not a browser
// lifecycle generation, so receipts omit Generation unless a trusted host
// explicitly adds that separately authenticated binding.
type RecoveryReceipt struct {
	Schema        string                        `json:"schema"`
	Task          RecoveryReceiptTask           `json:"task"`
	Runtime       RecoveryReceiptRuntime        `json:"runtime"`
	Browser       RecoveryReceiptBrowser        `json:"browser"`
	Target        RecoveryReceiptTarget         `json:"target"`
	Policy        *RecoveryReceiptPolicy        `json:"policy,omitempty"`
	Operation     RecoveryReceiptOperation      `json:"operation"`
	Postcondition *RecoveryReceiptPostcondition `json:"postcondition,omitempty"`
	Evidence      RecoveryReceiptEvidence       `json:"evidence"`
	Recovery      RecoveryReceiptRecovery       `json:"recovery"`
}

type RecoveryReceiptTask struct {
	ID              string `json:"id"`
	PlanFingerprint string `json:"plan_fingerprint"`
	State           string `json:"state"`
}

type RecoveryReceiptRuntime struct {
	Name          string `json:"name"`
	Version       string `json:"version,omitempty"`
	ReceiptSchema int    `json:"receipt_schema"`
}

type RecoveryReceiptBrowser struct {
	InstanceID string `json:"instance_id"`
	Generation string `json:"generation,omitempty"`
	Ownership  string `json:"ownership"`
}

type RecoveryReceiptTarget struct {
	ID      string `json:"id"`
	FrameID string `json:"frame_id,omitempty"`
}

type RecoveryReceiptPolicy struct {
	ID              string   `json:"id,omitempty"`
	Digest          string   `json:"digest,omitempty"`
	Origins         []string `json:"origins"`
	AllowPrivateIPs bool     `json:"allow_private_ips"`
	InputsComplete  bool     `json:"inputs_complete"`
	Enforcement     string   `json:"enforcement"`
}

func (p RecoveryReceiptPolicy) MarshalJSON() ([]byte, error) {
	type policyAlias RecoveryReceiptPolicy
	if p.Origins == nil {
		p.Origins = []string{}
	}
	return json.Marshal(policyAlias(p))
}

type RecoveryReceiptOperation struct {
	ID           string `json:"id"`
	Ordinal      int    `json:"ordinal"`
	Name         string `json:"name"`
	Dispatch     string `json:"dispatch"`
	Outcome      string `json:"outcome"`
	Verification string `json:"verification"`
	DeadlineAt   int64  `json:"deadline_at_ms,omitempty"`
}

type RecoveryReceiptPostcondition struct {
	Kind     string `json:"kind"`
	Captured bool   `json:"captured"`
}

type RecoveryReceiptEvidence struct {
	CreatedAt  string  `json:"created_at"`
	ObservedAt *string `json:"observed_at"`
	// Trust is runtime_metadata in receipt v1. Host attestations are carried
	// separately by the signed recovery handoff; page content is never upgraded
	// to trusted evidence by embedding it in this receipt.
	Trust      string   `json:"trust"`
	References []string `json:"references"`
}

type RecoveryReceiptRecovery struct {
	ReplayAuthorized bool   `json:"replay_authorized"`
	NextStep         string `json:"next_step"`
}

// RecoveryReceiptBinding is the runtime identity independently observed by a
// receiving runner. Values must come from trusted browser and policy state,
// not from the portable receipt itself.
type RecoveryReceiptBinding struct {
	BrowserInstanceID string
	BrowserGeneration string
	TargetID          string
	FrameID           string
	// FrameBindingKnown distinguishes a confirmed top-level frame (empty
	// FrameID) from a receiver that did not observe frame identity.
	FrameBindingKnown bool
	PolicyID          string
	PolicyDigest      string
}

// RecoveryReceiptBindingComparison reports whether a receipt still refers to
// the exact browser context, target, frame, and policy observed by a receiving
// runner. A match only permits the host to consider read-only page inspection;
// it never authorizes an action, replay, or business-outcome conclusion.
type RecoveryReceiptBindingComparison struct {
	SameBrowser bool
	SameTarget  bool
	SameFrame   bool
	SamePolicy  bool
	SameContext bool
}

// CompareRecoveryReceiptBinding compares portable receipt identity with
// independently observed runtime identity. The caller must authenticate the
// owner before exposing either browser or application state. The v1 receipt
// carries no authenticated owner identity, so this function cannot establish
// authorization across principals.
func CompareRecoveryReceiptBinding(receipt RecoveryReceipt, observed RecoveryReceiptBinding) (RecoveryReceiptBindingComparison, error) {
	if err := receipt.Validate(); err != nil {
		return RecoveryReceiptBindingComparison{}, fmt.Errorf("compare recovery receipt binding: %w", err)
	}
	sameBrowser := observed.BrowserInstanceID != "" && receipt.Browser.InstanceID == observed.BrowserInstanceID
	if receipt.Browser.Generation != "" {
		sameBrowser = sameBrowser && observed.BrowserGeneration != "" && receipt.Browser.Generation == observed.BrowserGeneration
	}
	comparison := RecoveryReceiptBindingComparison{
		SameBrowser: sameBrowser,
		SameTarget:  observed.TargetID != "" && receipt.Target.ID == observed.TargetID,
		SameFrame:   observed.FrameBindingKnown && receipt.Target.FrameID == observed.FrameID,
	}
	if receipt.Policy != nil && receipt.Policy.ID != "" && receipt.Policy.Digest != "" && observed.PolicyID != "" && observed.PolicyDigest != "" {
		comparison.SamePolicy = receipt.Policy.ID == observed.PolicyID && receipt.Policy.Digest == observed.PolicyDigest
	}
	comparison.SameContext = comparison.SameBrowser && comparison.SameTarget && comparison.SameFrame && comparison.SamePolicy
	return comparison, nil
}

// NewRecoveryReceipt combines a retained task reservation with one action
// receipt. It exports only the postcondition kind, never caller-supplied
// expected values or page text.
func NewRecoveryReceipt(task TaskRunRecord, action ActionReceipt) (RecoveryReceipt, error) {
	task = normalizeTaskRunRecord(task)
	if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Fingerprint) == "" {
		return RecoveryReceipt{}, fmt.Errorf("recovery receipt requires a task ID and plan fingerprint")
	}
	if action.OperationOrdinal < 1 || strings.TrimSpace(action.Action) == "" || strings.TrimSpace(action.TargetID) == "" || strings.TrimSpace(action.BrowserInstanceID) == "" {
		return RecoveryReceipt{}, fmt.Errorf("recovery receipt action identity is incomplete")
	}
	if task.TargetID != "" && task.TargetID != action.TargetID {
		return RecoveryReceipt{}, fmt.Errorf("recovery receipt target does not match task target")
	}
	if task.BrowserInstanceID != "" && task.BrowserInstanceID != action.BrowserInstanceID {
		return RecoveryReceipt{}, fmt.Errorf("recovery receipt browser does not match task browser")
	}
	if task.Plan != nil && task.Plan.PolicyID != "" && action.PolicyID != "" && task.Plan.PolicyID != action.PolicyID {
		return RecoveryReceipt{}, fmt.Errorf("recovery receipt policy does not match task policy")
	}
	state := recoveryReceiptTaskState(task)
	outcome := recoveryReceiptOutcome(action.Verification)
	dispatch := "dispatched"
	// VerificationRejected is also used for action-level failures that may have
	// happened before the browser received a mutation. Preserve uncertainty
	// unless the action receipt provides evidence that dispatch completed.
	if outcome == "unknown" || action.Outcome == OutcomeActionFailed || action.Outcome == OutcomeResponseFailed || action.Outcome == OutcomeUnknown {
		dispatch = "may_have_dispatched"
	}
	created := time.Now().UTC()
	if action.StartedAt > 0 {
		created = time.UnixMilli(action.StartedAt).UTC()
	}
	evidence := RecoveryReceiptEvidence{
		CreatedAt: created.Format(time.RFC3339Nano), Trust: "runtime_metadata", References: []string{},
	}
	if action.StartedAt > 0 && action.DurationMS > 0 {
		observed := time.UnixMilli(action.StartedAt).Add(time.Duration(action.DurationMS) * time.Millisecond).UTC().Format(time.RFC3339Nano)
		evidence.ObservedAt = &observed
	}
	if action.TraceID != "" {
		evidence.References = append(evidence.References, "trace:"+action.TraceID)
	}
	receipt := RecoveryReceipt{
		Schema:  RecoveryReceiptSchema,
		Task:    RecoveryReceiptTask{ID: task.ID, PlanFingerprint: task.Fingerprint, State: state},
		Runtime: RecoveryReceiptRuntime{Name: "scout", ReceiptSchema: 1},
		Browser: RecoveryReceiptBrowser{InstanceID: action.BrowserInstanceID, Ownership: recoveryReceiptOwnership(action.BrowserOwnership)},
		Target:  RecoveryReceiptTarget{ID: action.TargetID, FrameID: action.FrameID},
		Operation: RecoveryReceiptOperation{
			ID: RecoveryOperationID(task.ID, action.OperationOrdinal), Ordinal: action.OperationOrdinal,
			Name: action.Action, Dispatch: dispatch, Outcome: outcome, Verification: outcome,
			DeadlineAt: action.DeadlineAt,
		},
		Evidence: evidence,
		Recovery: RecoveryReceiptRecovery{ReplayAuthorized: false, NextStep: "reconcile_with_authoritative_application_state"},
	}
	if action.Postcondition != nil {
		receipt.Postcondition = &RecoveryReceiptPostcondition{Kind: postconditionKind(*action.Postcondition), Captured: false}
	}
	if task.Plan != nil {
		policyInputsComplete := task.Plan.PolicyID != "" && (len(task.Plan.AllowedOrigins) > 0 || task.Plan.PolicyID == taskOriginPolicyID(nil, task.Plan.AllowPrivateIPs))
		receipt.Policy = &RecoveryReceiptPolicy{
			ID: task.Plan.PolicyID, Digest: recoveryReceiptPolicyDigest(task.Plan.PolicyID),
			Origins: append([]string{}, task.Plan.AllowedOrigins...), AllowPrivateIPs: task.Plan.AllowPrivateIPs,
			InputsComplete: policyInputsComplete,
			Enforcement:    recoveryReceiptEnforcement(task.Plan.PolicyEnforcement),
		}
	}
	if err := receipt.Validate(); err != nil {
		return RecoveryReceipt{}, err
	}
	return receipt, nil
}

// ExportTaskRunRecoveryReceipt exports one saved action from a task record. For
// an interrupted task, operationOrdinal is the one-based task step and only the
// currently started step can be exported. For a completed task, it is the
// ActionReceipt operation ordinal returned in the saved batch result.
func ExportTaskRunRecoveryReceipt(task TaskRunRecord, operationOrdinal int) (RecoveryReceipt, error) {
	task = normalizeTaskRunRecord(task)
	if operationOrdinal < 0 {
		return RecoveryReceipt{}, fmt.Errorf("operation ordinal cannot be negative")
	}
	if task.InFlight {
		if task.Recovery == nil || task.Recovery.Current == nil || task.Recovery.Current.Status != "started" {
			return RecoveryReceipt{}, fmt.Errorf("task has no started action to export")
		}
		stepIndex := task.Recovery.Current.Index
		if stepIndex < 0 || stepIndex >= len(task.Recovery.Steps) || task.Plan == nil || stepIndex >= len(task.Plan.Actions) {
			return RecoveryReceipt{}, fmt.Errorf("interrupted task cursor is inconsistent with its plan")
		}
		stepOrdinal := stepIndex + 1
		if operationOrdinal != 0 && operationOrdinal != stepOrdinal {
			return RecoveryReceipt{}, fmt.Errorf("only the currently started task step can be exported")
		}
		step := task.Plan.Actions[stepIndex]
		frameID := step.FrameID
		if step.WebMCPFrameID != "" {
			frameID = step.WebMCPFrameID
		}
		action := ActionReceipt{
			OperationOrdinal: stepOrdinal, PolicyID: task.Plan.PolicyID, Action: step.Action, TargetID: task.TargetID,
			BrowserInstanceID: task.BrowserInstanceID, FrameID: frameID, Verification: VerificationUnknown,
			Outcome: OutcomeUnknown,
		}
		receipt, err := NewRecoveryReceipt(task, action)
		if err != nil {
			return RecoveryReceipt{}, err
		}
		if step.Postcondition != "" {
			receipt.Postcondition = &RecoveryReceiptPostcondition{Kind: step.Postcondition, Captured: false}
		}
		return receipt, receipt.Validate()
	}
	if task.Result == nil || task.Result.Batch == nil {
		return RecoveryReceipt{}, fmt.Errorf("task has no saved batch result to export")
	}
	for _, step := range task.Result.Batch.Results {
		// The public selector is task-local (one-based), just like interrupted
		// task steps. ActionReceipt.OperationOrdinal is session-global and can
		// therefore differ after unrelated browser actions.
		taskStepOrdinal := step.Index + 1
		if step.Receipt == nil || (operationOrdinal != 0 && taskStepOrdinal != operationOrdinal) {
			continue
		}
		if task.Plan != nil {
			if step.Index < 0 || step.Index >= len(task.Plan.Actions) {
				return RecoveryReceipt{}, fmt.Errorf("saved action receipt index is inconsistent with task plan")
			}
			planned := task.Plan.Actions[step.Index]
			if planned.Action != step.Receipt.Action {
				return RecoveryReceipt{}, fmt.Errorf("saved action receipt does not match task plan action")
			}
			if planned.WebMCPName != "" && planned.WebMCPFrameID != step.Receipt.FrameID {
				return RecoveryReceipt{}, fmt.Errorf("saved WebMCP action receipt frame does not match task plan frame")
			}
			if planned.FrameID != "" && planned.WebMCPFrameID == "" && planned.FrameID != step.Receipt.FrameID {
				return RecoveryReceipt{}, fmt.Errorf("saved action receipt frame does not match task plan frame")
			}
		}
		receipt, err := NewRecoveryReceipt(task, *step.Receipt)
		if err != nil {
			return RecoveryReceipt{}, err
		}
		// Persisted ActionReceipt ordinals are session-global. Normalize both
		// portable fields to the stable, one-based task step.
		receipt.Operation.Ordinal = step.Index + 1
		receipt.Operation.ID = RecoveryOperationID(task.ID, receipt.Operation.Ordinal)
		return receipt, receipt.Validate()
	}
	return RecoveryReceipt{}, fmt.Errorf("task action receipt was not found")
}

// ExportTaskRunRecoveryReceiptContext loads one retained task and exports its
// selected action as a portable, read-only envelope.
func (s *Session) ExportTaskRunRecoveryReceiptContext(ctx context.Context, taskID string, operationOrdinal int) (RecoveryReceipt, error) {
	task, found, err := s.GetTaskRunContext(ctx, taskID)
	if err != nil {
		return RecoveryReceipt{}, err
	}
	if !found {
		return RecoveryReceipt{}, fmt.Errorf("task run %q was not found", taskID)
	}
	return ExportTaskRunRecoveryReceipt(task, operationOrdinal)
}

// TaskRunRecoveryView composes a retained record with its portable receipt.
// Receipt absence does not invalidate the record; a task may have no saved
// action receipt to export.
type TaskRunRecoveryView struct {
	ContentTrust             string           `json:"content_trust,omitempty"`
	Found                    bool             `json:"found"`
	Record                   *TaskRunRecord   `json:"record,omitempty"`
	Receipt                  *RecoveryReceipt `json:"receipt,omitempty"`
	ReceiptUnavailableReason string           `json:"receipt_unavailable_reason,omitempty"`
	Guidance                 TaskRunGuidance  `json:"guidance"`
}

// TaskRunGuidance is a bounded host instruction derived only from durable
// task state. It never authorizes replay or claims an application outcome.
type TaskRunGuidance struct {
	Recommendation   string   `json:"recommendation"`
	Rationale        string   `json:"rationale"`
	Prerequisites    []string `json:"prerequisites"`
	ReplayAuthorized bool     `json:"replay_authorized"`
}

const (
	TaskRunGuidanceVerifyStoreScope            = "verify_task_store_scope"
	TaskRunGuidanceInspectSavedResult          = "inspect_saved_task_result"
	TaskRunGuidanceReviewResolution            = "review_host_resolution_then_use_new_task_id"
	TaskRunGuidanceConfirmWorkerStopped        = "confirm_worker_stopped_then_start_new_task"
	TaskRunGuidanceReconcileAuthoritativeState = "reconcile_with_authoritative_application_state"
)

func taskRunGuidance(found bool, record *TaskRunRecord) TaskRunGuidance {
	guidance := TaskRunGuidance{ReplayAuthorized: false, Prerequisites: []string{}}
	if !found || record == nil {
		guidance.Recommendation = TaskRunGuidanceVerifyStoreScope
		guidance.Rationale = "No record was found in the selected store and namespace; absence does not establish whether browser work occurred."
		guidance.Prerequisites = []string{"confirm the configured store, owner, session namespace, and task ID"}
		return guidance
	}
	if record.Resolution != nil && record.Resolution.Status != "unresolved" {
		guidance.Recommendation = TaskRunGuidanceReviewResolution
		guidance.Rationale = "A terminal host reconciliation is recorded; it does not authorize reuse of this task ID or replay."
		guidance.Prerequisites = []string{"review the resolution evidence and provenance", "use a new task ID for follow-up work"}
		return guidance
	}
	if record.Result != nil && record.Result.Status == "needs_reconciliation" {
		guidance.Recommendation = TaskRunGuidanceReconcileAuthoritativeState
		guidance.Rationale = "The saved task result is uncertain; a host observation has not established the authoritative application outcome."
		guidance.Prerequisites = []string{"review the saved result and identify uncertain or partially completed effects", "inspect the authoritative application state", "record unresolved or a terminal host resolution with evidence provenance", "use a new task ID for follow-up work"}
		return guidance
	}
	if record.Result != nil && record.Result.Status == "completed" {
		guidance.Recommendation = TaskRunGuidanceInspectSavedResult
		guidance.Rationale = "A completed task result is retained; this lookup does not execute browser actions."
		guidance.Prerequisites = []string{"review the saved result and its verification evidence"}
		return guidance
	}
	if record.Result != nil {
		guidance.Recommendation = TaskRunGuidanceReconcileAuthoritativeState
		guidance.Rationale = "A non-completed task result is retained and may include dispatched effects that need host reconciliation."
		guidance.Prerequisites = []string{"review the saved result and identify uncertain or partially completed effects", "inspect the authoritative application state", "record unresolved or a terminal host resolution with evidence provenance", "use a new task ID for follow-up work"}
		return guidance
	}
	if record.InFlight && record.Recovery != nil && record.Recovery.Phase == "actions" && record.Recovery.Current == nil && record.Recovery.NextAction == 0 {
		guidance.Recommendation = TaskRunGuidanceReconcileAuthoritativeState
		guidance.Rationale = "No action-start checkpoint is present. Stores without per-action progress support may retain this initial cursor even after dispatch, so the record cannot establish whether browser work occurred or whether the previous worker has stopped."
		guidance.Prerequisites = []string{"confirm the previous worker cannot continue", "verify the intended browser target and current policy", "inspect authoritative application state for possible effects", "record unresolved or a terminal host resolution with evidence provenance", "use a new task ID for follow-up work"}
		return guidance
	}
	guidance.Recommendation = TaskRunGuidanceReconcileAuthoritativeState
	guidance.Rationale = "An action may have been dispatched or task completion may not have been persisted; browser metadata cannot establish the business outcome."
	guidance.Prerequisites = []string{"inspect the authoritative application state", "record unresolved or a terminal host resolution with evidence provenance", "use a new task ID for follow-up work"}
	return guidance
}

// RecoverTaskRunContext performs a read-only task lookup and attaches the
// selected portable receipt when one is available. It never resolves or
// authorizes replay of the task.
func (s *Session) RecoverTaskRunContext(ctx context.Context, taskID string, operationOrdinal int) (TaskRunRecoveryView, error) {
	task, found, err := s.GetTaskRunContext(ctx, taskID)
	if err != nil {
		return TaskRunRecoveryView{}, err
	}
	view := TaskRunRecoveryView{ContentTrust: ContentTrustUntrustedPage, Found: found, Guidance: taskRunGuidance(found, nil)}
	if !found {
		return view, nil
	}
	view.Record = &task
	view.Guidance = taskRunGuidance(true, &task)
	if receipt, receiptErr := ExportTaskRunRecoveryReceipt(task, operationOrdinal); receiptErr == nil {
		view.Receipt = &receipt
	} else {
		view.ReceiptUnavailableReason = receiptErr.Error()
	}
	return view, nil
}

// Validate rejects unsupported or contradictory fields before a receipt is
// handed to another runtime. Validation cannot prove that the browser is live.
func (r RecoveryReceipt) Validate() error {
	if r.Schema != RecoveryReceiptSchema {
		return fmt.Errorf("unsupported recovery receipt schema %q", r.Schema)
	}
	if r.Task.ID == "" || r.Task.PlanFingerprint == "" || r.Runtime.Name == "" || r.Runtime.ReceiptSchema < 1 || r.Browser.InstanceID == "" || r.Target.ID == "" {
		return fmt.Errorf("recovery receipt identity is incomplete")
	}
	if r.Browser.Generation != "" && strings.TrimSpace(r.Browser.Generation) == "" {
		return fmt.Errorf("recovery receipt browser generation is empty")
	}
	if r.Operation.ID == "" || r.Operation.Ordinal < 1 || r.Operation.Name == "" {
		return fmt.Errorf("recovery receipt operation is incomplete")
	}
	operationTaskID, operationOrdinal, err := parseRecoveryOperationID(r.Operation.ID)
	if err != nil || operationTaskID != r.Task.ID || operationOrdinal != r.Operation.Ordinal {
		return fmt.Errorf("recovery receipt operation ID does not match task and ordinal")
	}
	if r.Operation.DeadlineAt < 0 {
		return fmt.Errorf("recovery receipt operation deadline is invalid")
	}
	if !oneOf(r.Task.State, "completed", "interrupted", "rejected", "needs_reconciliation") {
		return fmt.Errorf("recovery receipt has invalid task state %q", r.Task.State)
	}
	if !oneOf(r.Browser.Ownership, "owned", "shared", "attached", "unknown") {
		return fmt.Errorf("recovery receipt has invalid browser ownership %q", r.Browser.Ownership)
	}
	if !oneOf(r.Operation.Dispatch, "not_started", "may_have_dispatched", "dispatched", "unknown") || !oneOf(r.Operation.Outcome, "verified", "rejected", "unknown", "unverified") || !oneOf(r.Operation.Verification, "verified", "rejected", "unknown", "unverified") {
		return fmt.Errorf("recovery receipt has invalid operation state")
	}
	if r.Operation.Outcome != r.Operation.Verification {
		return fmt.Errorf("recovery receipt outcome and verification disagree")
	}
	if r.Operation.Outcome == "unknown" && r.Operation.Dispatch != "may_have_dispatched" && r.Operation.Dispatch != "unknown" {
		return fmt.Errorf("unknown recovery receipt outcome has a contradictory dispatch state")
	}
	if r.Evidence.Trust != "runtime_metadata" || r.Evidence.CreatedAt == "" || r.Evidence.References == nil {
		return fmt.Errorf("recovery receipt evidence is incomplete")
	}
	if _, err := time.Parse(time.RFC3339Nano, r.Evidence.CreatedAt); err != nil {
		return fmt.Errorf("recovery receipt creation time is invalid: %w", err)
	}
	if r.Evidence.ObservedAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *r.Evidence.ObservedAt); err != nil {
			return fmt.Errorf("recovery receipt observation time is invalid: %w", err)
		}
	}
	if r.Recovery.ReplayAuthorized {
		return fmt.Errorf("recovery receipt cannot authorize replay")
	}
	if r.Recovery.NextStep != "reconcile_with_authoritative_application_state" {
		return fmt.Errorf("recovery receipt has invalid next step %q", r.Recovery.NextStep)
	}
	if r.Policy != nil && !oneOf(r.Policy.Enforcement, "session_page_policy", "proxy", "combined", "none", "unknown") {
		return fmt.Errorf("recovery receipt has invalid policy enforcement %q", r.Policy.Enforcement)
	}
	if r.Policy != nil && r.Policy.Digest != "" && !validRecoveryReceiptDigest(r.Policy.Digest) {
		return fmt.Errorf("recovery receipt has invalid policy digest")
	}
	if r.Policy != nil && r.Policy.Digest != "" {
		if r.Policy.InputsComplete {
			if r.Policy.Origins == nil {
				return fmt.Errorf("recovery receipt marks policy inputs complete without canonical origins")
			}
			computed := taskOriginPolicyID(r.Policy.Origins, r.Policy.AllowPrivateIPs)
			if recoveryReceiptPolicyDigest(computed) != r.Policy.Digest || r.Policy.ID != computed {
				return fmt.Errorf("recovery receipt policy digest does not match canonical policy inputs")
			}
		} else if r.Policy.ID != "scout-origin-v1:"+strings.TrimPrefix(r.Policy.Digest, "sha256:") {
			return fmt.Errorf("recovery receipt policy ID and digest disagree")
		}
	} else if r.Policy != nil && r.Policy.InputsComplete {
		return fmt.Errorf("recovery receipt marks policy inputs complete without a digest")
	}
	if r.Postcondition != nil && strings.TrimSpace(r.Postcondition.Kind) == "" {
		return fmt.Errorf("recovery receipt postcondition has no kind")
	}
	return nil
}

// DecodeRecoveryReceipt strictly decodes one envelope and rejects trailing or
// unknown JSON so producers cannot accidentally accept a newer contract.
func DecodeRecoveryReceipt(data []byte) (RecoveryReceipt, error) {
	var receipt RecoveryReceipt
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return RecoveryReceipt{}, fmt.Errorf("decode recovery receipt: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return RecoveryReceipt{}, fmt.Errorf("decode recovery receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return RecoveryReceipt{}, fmt.Errorf("decode recovery receipt: trailing JSON value")
		}
		return RecoveryReceipt{}, fmt.Errorf("decode recovery receipt: trailing content: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return RecoveryReceipt{}, err
	}
	return receipt, nil
}

func recoveryReceiptTaskState(task TaskRunRecord) string {
	if task.Resolution != nil {
		switch task.Resolution.Status {
		case "unresolved":
			return "needs_reconciliation"
		case "rejected":
			return "rejected"
		default:
			return "completed"
		}
	}
	if task.Result != nil && task.Result.Status == "needs_reconciliation" {
		return "needs_reconciliation"
	}
	if task.InFlight {
		return "interrupted"
	}
	if task.Result == nil {
		return "needs_reconciliation"
	}
	switch task.Result.Status {
	case "completed":
		return "completed"
	case "rejected":
		return "rejected"
	default:
		return "needs_reconciliation"
	}
}

func recoveryReceiptOutcome(verification VerificationState) string {
	switch verification {
	case VerificationVerified:
		return "verified"
	case VerificationRejected:
		return "rejected"
	case VerificationUnverified:
		return "unverified"
	default:
		return "unknown"
	}
}

func recoveryReceiptOwnership(value string) string {
	switch value {
	case "owned", "launched", "scout_launched":
		return "owned"
	case "shared":
		return "shared"
	case "attached", "remote_cdp", "injected_browser":
		return "attached"
	default:
		return "unknown"
	}
}

func recoveryReceiptEnforcement(value string) string {
	switch value {
	case "session_page_policy", "proxy", "combined", "none":
		return value
	default:
		return "unknown"
	}
}

func recoveryReceiptPolicyDigest(policyID string) string {
	const prefix = "scout-origin-v1:"
	if !strings.HasPrefix(policyID, prefix) {
		return ""
	}
	digest := strings.TrimPrefix(policyID, prefix)
	if len(digest) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ""
	}
	return "sha256:" + digest
}

// RecoveryReceiptPolicyDigest returns the digest embedded in a Scout origin
// policy ID. It is an identifier helper, not a policy enforcement check.
func RecoveryReceiptPolicyDigest(policyID string) (string, error) {
	digest := recoveryReceiptPolicyDigest(policyID)
	if digest == "" {
		return "", fmt.Errorf("invalid Scout origin policy ID")
	}
	return digest, nil
}

func validRecoveryReceiptDigest(digest string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) || len(digest) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, prefix))
	return err == nil && strings.ToLower(digest) == digest
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
