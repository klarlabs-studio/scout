package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxTaskRunRecordsPerSession bounds retained idempotency results. The store
// fails closed at this limit instead of evicting IDs that could still be
// retried by a caller.
const MaxTaskRunRecordsPerSession = 256

type taskRunEntry struct {
	Fingerprint       string
	InFlight          bool
	Plan              *TaskRunPlan
	Timeout           time.Duration
	AllowedOrigins    []string
	AllowPrivateIPs   bool
	PolicyID          string
	PolicyEnforcement string
	TargetID          string
	BrowserInstanceID string
	Result            *TaskRunResult
	Resolution        *TaskRunResolution
	ResolutionHistory []TaskRunResolution
	Recovery          *TaskRunRecovery
	Error             string
}

// TaskRunRecord is the durable idempotency record for one task ID. A store
// should retain reserved records across process restarts: an interrupted
// reservation is ambiguous and must not be automatically replayed.
type TaskRunRecord struct {
	ID           string `json:"id"`
	Fingerprint  string `json:"fingerprint"`
	ContentTrust string `json:"content_trust,omitempty"`
	// Plan is the portable, redacted task contract. It preserves operation
	// shape and postcondition types for a recovery host without retaining input
	// values, expected text, or other user-supplied secrets.
	Plan              *TaskRunPlan        `json:"plan,omitempty"`
	InFlight          bool                `json:"in_flight"`
	TargetID          string              `json:"target_id,omitempty"`
	BrowserInstanceID string              `json:"browser_instance_id,omitempty"`
	Result            *TaskRunResult      `json:"result,omitempty"`
	Resolution        *TaskRunResolution  `json:"resolution,omitempty"`
	ResolutionHistory []TaskRunResolution `json:"resolution_history,omitempty"`
	Recovery          *TaskRunRecovery    `json:"recovery,omitempty"`
	Error             string              `json:"error,omitempty"`
}

// TaskRunPlan is safe to export as a recovery handoff. It is descriptive only:
// it omits action arguments and cannot be submitted to execute or replay a task.
type TaskRunPlan struct {
	SchemaVersion      int                   `json:"schema_version"`
	TargetID           string                `json:"target_id,omitempty"`
	AllowedOrigins     []string              `json:"allowed_origins,omitempty"`
	AllowPrivateIPs    bool                  `json:"allow_private_ips"`
	PolicyID           string                `json:"policy_id"`
	PolicyEnforcement  string                `json:"policy_enforcement"`
	TimeoutMS          int64                 `json:"timeout_ms,omitempty"`
	Actions            []TaskRunRecoveryStep `json:"actions"`
	FinalPostcondition string                `json:"final_postcondition,omitempty"`
	FinalTarget        string                `json:"final_target,omitempty"`
}

// TaskRunRecovery stores a redacted manifest and dispatch cursor. The cursor
// is durable only when the configured store implements TaskRunProgressStore.
// Input values and expected text are intentionally excluded.
type TaskRunRecovery struct {
	SchemaVersion      int                    `json:"schema_version"`
	Phase              string                 `json:"phase"`
	FinalPostcondition string                 `json:"final_postcondition,omitempty"`
	FinalTarget        string                 `json:"final_target,omitempty"`
	Steps              []TaskRunRecoveryStep  `json:"steps"`
	Current            *TaskRunRecoveryCursor `json:"current,omitempty"`
	NextAction         int                    `json:"next_action"`
}

type TaskRunRecoveryStep struct {
	Action              string `json:"action"`
	Selector            string `json:"selector,omitempty"`
	FrameID             string `json:"frame_id,omitempty"`
	WebMCPName          string `json:"webmcp_name,omitempty"`
	WebMCPFrameID       string `json:"webmcp_frame_id,omitempty"`
	Postcondition       string `json:"postcondition,omitempty"`
	PostconditionTarget string `json:"postcondition_target,omitempty"`
}

type TaskRunRecoveryCursor struct {
	Index  int    `json:"index"`
	Status string `json:"status"`
}

// TaskRunProgressStore is an optional extension for per-action recovery state.
// Without it, the store retains only the initial cursor, which must not be
// interpreted as proof that no action was dispatched.
type TaskRunProgressStore interface {
	UpdateTaskRunRecovery(ctx context.Context, id, fingerprint string, recovery TaskRunRecovery) error
}

// TaskRunPlanStore is an optional store extension for persisting the redacted
// task plan before action dispatch. It remains optional for compatibility with
// caller-owned stores implementing the original TaskRunStore contract.
type TaskRunPlanStore interface {
	UpdateTaskRunPlan(ctx context.Context, id, fingerprint string, plan TaskRunPlan) error
}

// TaskRunHandoffStore atomically binds browser identity with the redacted plan
// and initial cursor before any action can dispatch.
type TaskRunHandoffStore interface {
	BindTaskRunHandoff(ctx context.Context, id, fingerprint, targetID, browserInstanceID string, plan TaskRunPlan, recovery TaskRunRecovery) error
}

// TaskRunResolution is a caller attestation recorded after inspecting the
// application state for a task interrupted by process or transport loss. It
// records what the host concluded; Scout does not treat it as browser proof.
type TaskRunResolution struct {
	Status            string `json:"status"`
	Evidence          string `json:"evidence"`
	EvidenceSource    string `json:"evidence_source,omitempty"`
	EvidenceReference string `json:"evidence_reference,omitempty"`
	ObservedAt        string `json:"observed_at,omitempty"`
	ResolvedAt        string `json:"resolved_at"`
}

// TaskRunStore persists task ID reservations and results. Reserve
// must atomically create a reservation or return the existing record, echoing
// the proposed identity for a newly-created reservation. BindTaskRunTarget
// must durably attach browser and target identity before dispatch. Complete
// must transition only the matching reservation to a retained result;
// Release must remove only a matching reservation, never a completed record.
// A store shared by multiple Scout sessions/processes must provide its own
// cross-process transaction or lock. It must never evict reservations or
// completed records that a caller may retry.
//
// Existing in-flight records are returned with created=false; Scout rejects
// them as conflicts and does not replay their actions. A host can reconcile
// application state and use TaskRunResolutionStore to retain its conclusion.
// Implement TaskRunProgressStore to persist per-action dispatch boundaries;
// without it, the initial recovery cursor may remain after an action starts.
// Stores that may execute tasks with a final postcondition should also
// implement TaskRunUncertainResultStore so a readback failure is retained
// without releasing the same-ID replay guard.
type TaskRunStore interface {
	ReserveTaskRun(ctx context.Context, proposed TaskRunRecord) (existing TaskRunRecord, created bool, err error)
	BindTaskRunTarget(ctx context.Context, id, fingerprint, targetID, browserInstanceID string) error
	CompleteTaskRun(ctx context.Context, completed TaskRunRecord) error
	ReleaseTaskRun(ctx context.Context, id, fingerprint string) error
	GetTaskRun(ctx context.Context, id string) (record TaskRunRecord, found bool, err error)
}

// TaskRunResolutionStore is an optional extension for stores that can
// durably annotate interrupted reservations after host-side reconciliation.
// It is separate from TaskRunStore to preserve compatibility with existing
// caller-owned store implementations.
type TaskRunResolutionStore interface {
	ResolveTaskRun(ctx context.Context, id, fingerprint string, resolution TaskRunResolution) error
}

// TaskRunUncertainResultStore retains an observed result without releasing
// the reservation. The host must reconcile authoritative application state
// before it resolves the task or starts follow-up work.
type TaskRunUncertainResultStore interface {
	RetainUncertainTaskRun(ctx context.Context, id, fingerprint string, result TaskRunResult, failure string) error
}

// GetTaskRunContext returns the retained task record without changing it.
// Hosts can use this after a restart to inspect a completed result or identify
// the browser target associated with an interrupted reservation.
func (s *Session) GetTaskRunContext(ctx context.Context, id string) (TaskRunRecord, bool, error) {
	if s == nil {
		return TaskRunRecord{}, false, fmt.Errorf("task run lookup requires a session")
	}
	if ctx == nil {
		return TaskRunRecord{}, false, fmt.Errorf("task run lookup requires a context")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return TaskRunRecord{}, false, fmt.Errorf("task run lookup requires a non-empty id")
	}
	if err := ctx.Err(); err != nil {
		return TaskRunRecord{}, false, err
	}
	if s.taskRunStore != nil {
		record, found, err := s.taskRunStore.GetTaskRun(ctx, id)
		if err != nil || !found {
			return record, found, err
		}
		return normalizeTaskRunRecord(record), true, nil
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, found := s.taskRuns[id]
	if !found {
		return TaskRunRecord{}, false, nil
	}
	return normalizeTaskRunRecord(TaskRunRecord{
		ID: id, Fingerprint: entry.Fingerprint, InFlight: entry.InFlight,
		TargetID: entry.TargetID, BrowserInstanceID: entry.BrowserInstanceID,
		Result: cloneTaskRunResult(entry.Result), Resolution: cloneTaskRunResolution(entry.Resolution), ResolutionHistory: cloneTaskRunResolutionHistory(entry.ResolutionHistory), Recovery: cloneTaskRunRecovery(entry.Recovery), Error: entry.Error,
		Plan: cloneTaskRunPlan(entry.Plan),
	}), true, nil
}

func normalizeTaskRunRecord(record TaskRunRecord) TaskRunRecord {
	// A task record can embed page-derived results and plan metadata. Preserve
	// their provenance at the outer record boundary for every caller surface.
	record.ContentTrust = ContentTrustUntrustedPage
	if record.Plan == nil && record.Recovery != nil {
		record.Plan = taskRunPlanFromRecovery(record.Recovery, record.TargetID, nil, false, "", "", 0)
	}
	if record.Plan != nil {
		plan := cloneTaskRunPlan(record.Plan)
		if record.Result != nil {
			plan.TargetID = record.Result.TargetID
			plan.AllowedOrigins = append([]string(nil), record.Result.AllowedOrigins...)
			plan.AllowPrivateIPs = record.Result.AllowPrivateIPs
			plan.PolicyID = record.Result.PolicyID
			plan.PolicyEnforcement = record.Result.PolicyEnforcement
		}
		record.Plan = plan
	}
	return record
}

// ResolveInterruptedTaskRunContext records a host reconciliation observation
// for a durable in-flight reservation. "unresolved" preserves the replay
// guard and can later be followed by a terminal conclusion. Terminal outcomes
// require structured evidence provenance; use
// ResolveInterruptedTaskRunWithEvidenceContext for those conclusions.
//
// Deprecated: use ResolveInterruptedTaskRunWithEvidenceContext. This helper
// remains suitable for unresolved observations, but terminal conclusions need
// an evidence source, reference, and observation timestamp.
func (s *Session) ResolveInterruptedTaskRunContext(ctx context.Context, id, fingerprint, status, evidence string) (TaskRunRecord, error) {
	return s.ResolveInterruptedTaskRunWithEvidenceContext(ctx, id, fingerprint, TaskRunResolution{Status: status, Evidence: evidence})
}

// ResolveInterruptedTaskRunWithEvidenceContext records a host reconciliation
// observation and structured provenance for the authoritative read. Terminal
// statuses require EvidenceSource, EvidenceReference, and ObservedAt; unresolved
// observations may omit them while the host is still gathering evidence.
// References should be opaque, non-secret identifiers, not credentialed URLs.
func (s *Session) ResolveInterruptedTaskRunWithEvidenceContext(ctx context.Context, id, fingerprint string, resolution TaskRunResolution) (TaskRunRecord, error) {
	if s == nil || ctx == nil {
		return TaskRunRecord{}, fmt.Errorf("task resolution requires a session and context")
	}
	id = strings.TrimSpace(id)
	fingerprint = strings.TrimSpace(fingerprint)
	resolution.Status = strings.TrimSpace(resolution.Status)
	resolution.Evidence = strings.TrimSpace(resolution.Evidence)
	resolution.EvidenceSource = strings.TrimSpace(resolution.EvidenceSource)
	resolution.EvidenceReference = strings.TrimSpace(resolution.EvidenceReference)
	resolution.ObservedAt = strings.TrimSpace(resolution.ObservedAt)
	if id == "" || fingerprint == "" {
		return TaskRunRecord{}, fmt.Errorf("task resolution requires task ID and plan fingerprint")
	}
	if err := ctx.Err(); err != nil {
		return TaskRunRecord{}, err
	}
	resolution.ResolvedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := validateTaskRunResolution(resolution); err != nil {
		return TaskRunRecord{}, err
	}
	if s.taskRunStore != nil {
		store, ok := s.taskRunStore.(TaskRunResolutionStore)
		if !ok {
			return TaskRunRecord{}, fmt.Errorf("configured task run store does not support interrupted-task resolution")
		}
		if err := store.ResolveTaskRun(ctx, id, fingerprint, resolution); err != nil {
			return TaskRunRecord{}, err
		}
		record, found, err := s.GetTaskRunContext(ctx, id)
		if err != nil {
			return TaskRunRecord{}, err
		}
		if !found {
			return TaskRunRecord{}, fmt.Errorf("resolved task record disappeared")
		}
		return record, nil
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, ok := s.taskRuns[id]
	if !ok || entry.Fingerprint != fingerprint {
		return TaskRunRecord{}, &TaskRunConflictError{TaskID: id, Reason: "task reservation does not exist or has a different plan"}
	}
	if !entry.InFlight || entry.TargetID == "" || entry.BrowserInstanceID == "" || entry.Resolution != nil && entry.Resolution.Status != "unresolved" {
		return TaskRunRecord{}, &TaskRunConflictError{TaskID: id, Reason: "only a bound interrupted reservation can be resolved"}
	}
	entry.InFlight = resolution.Status == "unresolved"
	entry.Resolution = &resolution
	entry.Result = nil
	entry.ResolutionHistory = append(entry.ResolutionHistory, resolution)
	s.taskRuns[id] = entry
	return normalizeTaskRunRecord(TaskRunRecord{ID: id, Fingerprint: entry.Fingerprint, InFlight: entry.InFlight, TargetID: entry.TargetID, BrowserInstanceID: entry.BrowserInstanceID, Result: cloneTaskRunResult(entry.Result), Resolution: cloneTaskRunResolution(entry.Resolution), ResolutionHistory: cloneTaskRunResolutionHistory(entry.ResolutionHistory), Recovery: cloneTaskRunRecovery(entry.Recovery), Plan: cloneTaskRunPlan(entry.Plan), Error: entry.Error}), nil
}

// TaskRunConflictError indicates that a task ID is already in use by another
// execution or by a different plan in this live session.
type TaskRunConflictError struct {
	TaskID string
	Reason string
}

// TaskRunCapacityError indicates that the session has retained the maximum
// number of task IDs and cannot safely accept another without evicting history.
type TaskRunCapacityError struct {
	Limit int
}

func (e *TaskRunCapacityError) Error() string {
	if e == nil {
		return "task run capacity reached"
	}
	return fmt.Sprintf("task run capacity reached: this session retains up to %d task IDs", e.Limit)
}

func (e *TaskRunConflictError) Error() string {
	if e == nil {
		return "task ID conflict"
	}
	return fmt.Sprintf("task %q cannot be reused: %s", e.TaskID, e.Reason)
}

// TaskRunSpec declares the execution contract for a bounded browser task.
// AllowedOrigins, when provided, must exactly match the session's configured
// page-level allowlist. Browser-wide egress still requires an enforcing proxy
// or host network policy.
type TaskRunSpec struct {
	ID                 string         `json:"id"`
	TargetID           string         `json:"target_id,omitempty"`
	AllowedOrigins     []string       `json:"allowed_origins,omitempty"`
	FinalPostcondition *Postcondition `json:"final_postcondition,omitempty"`
	Timeout            time.Duration  `json:"-"`
	Actions            []BatchAction  `json:"actions"`
}

// TaskRunResult is a task-level view over batch receipts. Status is one of
// completed, rejected, needs_reconciliation, or incomplete. FailureReason
// carries diagnoses such as verification_failed without making uncertainty a
// terminal outcome. ResumeFrom is the
// first action not attempted; callers must reconcile any unknown outcome
// before deciding whether to resume there.
type TaskRunResult struct {
	ID                string       `json:"id"`
	ContentTrust      string       `json:"content_trust,omitempty"`
	Status            string       `json:"status"`
	FailureReason     string       `json:"failure_reason,omitempty"`
	IdempotencyReplay bool         `json:"idempotency_replay,omitempty"`
	TargetID          string       `json:"target_id"`
	BrowserInstanceID string       `json:"browser_instance_id"`
	AllowedOrigins    []string     `json:"allowed_origins,omitempty"`
	AllowPrivateIPs   bool         `json:"allow_private_ips"`
	PolicyID          string       `json:"policy_id"`
	PolicyEnforcement string       `json:"policy_enforcement"`
	ResumeFrom        int          `json:"resume_from"`
	PlanID            string       `json:"plan_id"`
	FinalVerification *PageResult  `json:"final_verification,omitempty"`
	Batch             *BatchResult `json:"batch"`
}

// RunTaskContext executes a task under a verified, target-bound batch
// contract. The task's requested origin set, if provided, must exactly match
// the allowlist configured when this Session was created; a live browser
// policy cannot be narrowed by a later task request. Session calls are
// serialized, and Scout rechecks the captured page URL immediately before
// dispatch. This cannot exclude direct CDP or other external browser clients
// from changing the target concurrently. Task actions recheck the captured URL
// after each durable action-start checkpoint, but external CDP clients can
// still race that check with the browser command itself; callers should ensure
// exclusive target control for the duration of a task.
func (s *Session) RunTaskContext(ctx context.Context, spec TaskRunSpec) (*TaskRunResult, error) {
	if s == nil {
		return nil, fmt.Errorf("task run requires a session")
	}
	if strings.TrimSpace(spec.ID) == "" {
		return nil, fmt.Errorf("task run requires a non-empty id")
	}
	if len(spec.Actions) == 0 {
		return nil, fmt.Errorf("task run requires at least one action")
	}
	if spec.FinalPostcondition != nil {
		if err := spec.FinalPostcondition.Validate(); err != nil {
			return nil, fmt.Errorf("task final_postcondition is invalid: %w", err)
		}
		if spec.FinalPostcondition.TargetAbsent != "" {
			return nil, fmt.Errorf("task final_postcondition cannot use target_absent")
		}
	}
	if spec.Timeout < 0 {
		return nil, fmt.Errorf("task timeout cannot be negative")
	}
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	configuredOrigins := append([]string(nil), s.allowedOrigins...)
	configured, err := normalizeTaskOriginSet(configuredOrigins)
	if err != nil {
		return nil, fmt.Errorf("session origin policy is invalid: %w", err)
	}
	if len(spec.AllowedOrigins) > 0 {
		if len(configuredOrigins) == 0 {
			return nil, fmt.Errorf("task allowed_origins requires a session-level AllowedOrigins policy")
		}
		requested, err := normalizeTaskOriginSet(spec.AllowedOrigins)
		if err != nil {
			return nil, fmt.Errorf("task origin policy is invalid: %w", err)
		}
		for origin := range requested {
			if _, ok := configured[origin]; !ok {
				return nil, fmt.Errorf("task origin %q is not covered by the session origin policy", origin)
			}
		}
		if len(requested) != len(configured) {
			return nil, fmt.Errorf("task origin policy cannot narrow an already-running session; configure the exact task origins when creating the session")
		}
		configuredOrigins = sortedTaskOrigins(requested)
	} else if len(configured) > 0 {
		configuredOrigins = sortedTaskOrigins(configured)
	}

	fingerprint, err := taskRunFingerprint(spec, configuredOrigins, s.allowPrivateIPs)
	if err != nil {
		return nil, fmt.Errorf("encode task plan: %w", err)
	}
	if cached, cachedErr, reused, reserveErr := s.reserveTaskRun(ctx, spec.ID, fingerprint); reserveErr != nil {
		return nil, reserveErr
	} else if reused {
		cached.IdempotencyReplay = true
		if cachedErr != "" {
			return cached, fmt.Errorf("task run previously ended: %s", cachedErr)
		}
		return cached, nil
	}
	dispatchStarted := false
	defer func() {
		if !dispatchStarted {
			s.releaseTaskRunReservation(ctx, spec.ID, fingerprint)
		}
	}()

	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	page := s.page
	targetID := page.TargetID()
	frameID := s.frameID
	currentURL, err := page.URL()
	var documentIdentity taskDocumentIdentity
	var documentErr error
	if err == nil {
		documentIdentity, documentErr = s.taskDocumentIdentityContext(ctx)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("read current page URL for task policy: %w", err)
	}
	if documentErr != nil {
		return nil, fmt.Errorf("capture task document identity: %w", documentErr)
	}

	if spec.TargetID != "" && spec.TargetID != targetID {
		return nil, fmt.Errorf("task target precondition failed: expected %q, current target is %q", spec.TargetID, targetID)
	}
	if spec.TargetID == "" {
		spec.TargetID = targetID
	}
	if len(configuredOrigins) > 0 {
		currentOrigin, err := normalizeTaskPageOrigin(currentURL)
		if err != nil {
			return nil, fmt.Errorf("cannot establish current page origin for task policy: %w", err)
		}
		if _, ok := configured[currentOrigin]; !ok {
			return nil, fmt.Errorf("current page origin %q is outside the task origin policy", currentOrigin)
		}
	}
	instanceID := s.browserInstanceID()
	recovery := taskRunRecoveryManifest(spec.Actions, spec.FinalPostcondition, frameID)
	policyID := taskOriginPolicyID(configuredOrigins, s.allowPrivateIPs)
	enforcement := "session_page_policy"
	if len(configuredOrigins) == 0 {
		enforcement = "no_origin_allowlist"
	}
	plan := taskRunPlanFromRecovery(&recovery, spec.TargetID, configuredOrigins, s.allowPrivateIPs, policyID, enforcement, spec.Timeout)
	if err := s.bindTaskRunHandoff(ctx, spec.ID, fingerprint, targetID, instanceID, plan, recovery); err != nil {
		return nil, fmt.Errorf("persist task recovery handoff: %w", err)
	}
	ctx = WithExpectedTargetID(ctx, spec.TargetID)
	ctx = withExpectedPageURL(ctx, currentURL)
	ctx = withExpectedTaskDocument(ctx, documentIdentity)
	ctx = context.WithValue(ctx, expectedFrameIDKey{}, frameID)
	dispatchStarted = true
	batch, err := s.executeTaskRunBatch(ctx, spec.ID, fingerprint, spec.Actions, spec.FinalPostcondition, &recovery)
	result := &TaskRunResult{
		ID: spec.ID, ContentTrust: ContentTrustUntrustedPage, Status: "incomplete", TargetID: spec.TargetID, BrowserInstanceID: instanceID, AllowedOrigins: configuredOrigins,
		AllowPrivateIPs: s.allowPrivateIPs,
		PolicyID:        policyID, PlanID: fingerprint, PolicyEnforcement: enforcement,
		Batch: batch,
	}
	if batch != nil {
		result.ResumeFrom = batch.NextActionIndex
		result.Status = taskRunStatus(batch)
		result.FinalVerification = batch.FinalVerification
	}
	if spec.FinalPostcondition != nil && result.Status == "completed" && (result.FinalVerification == nil || !result.FinalVerification.Verified) {
		// A missing or negative final readback is uncertain even if the verifier
		// did not return an error. Keep the same-ID replay guard until the host
		// checks authoritative application state.
		result.Status = "needs_reconciliation"
		result.FailureReason = "verification_failed"
		err = errors.New("task final postcondition was not verified")
	}
	if spec.FinalPostcondition != nil && batch != nil && batch.NextActionIndex == len(spec.Actions) && batch.StopReason == "" && err != nil {
		// All mutations completed, but the final readback failed. Preserve the
		// diagnosis while keeping the reservation nonterminal. Actions may have
		// committed, so only authoritative host reconciliation can release the
		// same-ID replay guard.
		result.Status = "needs_reconciliation"
		result.FailureReason = "verification_failed"
	}
	if err != nil {
		if result.Status == "needs_reconciliation" {
			if persistErr := s.finishUncertainTaskRun(ctx, spec.ID, fingerprint, result, err); persistErr != nil {
				err = errors.Join(err, fmt.Errorf("persist uncertain task result: %w", persistErr))
			}
		} else if persistErr := s.finishTaskRun(ctx, spec.ID, fingerprint, result, err); persistErr != nil {
			err = errors.Join(err, fmt.Errorf("persist task run result: %w", persistErr))
		}
		return result, err
	}
	recovery.Phase = "result_persisting"
	if checkpointErr := s.updateTaskRunRecovery(ctx, spec.ID, fingerprint, recovery); checkpointErr != nil {
		result.Status = "needs_reconciliation"
		result.FailureReason = "result_persistence_failed"
		err = fmt.Errorf("persist task result phase: %w", checkpointErr)
		if persistErr := s.finishUncertainTaskRun(ctx, spec.ID, fingerprint, result, err); persistErr != nil {
			err = errors.Join(err, fmt.Errorf("persist uncertain task result: %w", persistErr))
		}
		return result, err
	}
	recovery.Phase = "result_persisting"
	if checkpointErr := s.updateTaskRunRecovery(ctx, spec.ID, fingerprint, recovery); checkpointErr != nil {
		err = errors.Join(err, fmt.Errorf("persist task result phase: %w", checkpointErr))
	}
	if persistErr := s.finishTaskRun(ctx, spec.ID, fingerprint, result, err); persistErr != nil {
		result.Status = "needs_reconciliation"
		result.FailureReason = "result_persistence_failed"
		err = errors.Join(err, fmt.Errorf("persist task run result: %w", persistErr))
		if uncertainErr := s.finishUncertainTaskRun(ctx, spec.ID, fingerprint, result, err); uncertainErr != nil {
			err = errors.Join(err, fmt.Errorf("persist uncertain task result: %w", uncertainErr))
		}
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func taskRunFingerprint(spec TaskRunSpec, configuredOrigins []string, allowPrivateIPs bool) (string, error) {
	plan := struct {
		TargetID           string         `json:"target_id,omitempty"`
		AllowedOrigins     []string       `json:"allowed_origins,omitempty"`
		AllowPrivateIPs    bool           `json:"allow_private_ips"`
		FinalPostcondition *Postcondition `json:"final_postcondition,omitempty"`
		TimeoutNanos       int64          `json:"timeout_nanos"`
		Actions            []BatchAction  `json:"actions"`
	}{spec.TargetID, configuredOrigins, allowPrivateIPs, spec.FinalPostcondition, int64(spec.Timeout), spec.Actions}
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "scout-task-v1:" + hex.EncodeToString(digest[:]), nil
}

func taskRunRecoveryManifest(actions []BatchAction, final *Postcondition, frameID string) TaskRunRecovery {
	r := TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: make([]TaskRunRecoveryStep, len(actions))}
	if final != nil {
		r.FinalPostcondition = postconditionKind(*final)
		r.FinalTarget = postconditionTarget(*final)
	}
	for i, action := range actions {
		step := TaskRunRecoveryStep{Action: action.Action, Selector: action.Selector, FrameID: frameID, WebMCPName: action.WebMCPName, WebMCPFrameID: action.WebMCPFrameID}
		condition := Postcondition{
			Text: action.ExpectedText, URLContains: action.ExpectedURLContains,
			VisibleSelector: action.ExpectedVisibleTarget, ValueSelector: action.ExpectedValueSelector,
			Value: action.ExpectedValue, CountSelector: action.ExpectedCountSelector, Count: action.ExpectedCount,
		}
		step.Postcondition = postconditionKind(condition)
		step.PostconditionTarget = postconditionTarget(condition)
		r.Steps[i] = step
	}
	return r
}

func taskRunPlanFromRecovery(recovery *TaskRunRecovery, targetID string, origins []string, allowPrivateIPs bool, policyID, enforcement string, timeout time.Duration) *TaskRunPlan {
	if recovery == nil {
		return nil
	}
	return &TaskRunPlan{
		SchemaVersion: 1, TargetID: targetID, AllowedOrigins: append([]string(nil), origins...),
		AllowPrivateIPs: allowPrivateIPs, PolicyID: policyID, PolicyEnforcement: enforcement,
		TimeoutMS: timeout.Milliseconds(), Actions: append([]TaskRunRecoveryStep(nil), recovery.Steps...),
		FinalPostcondition: recovery.FinalPostcondition, FinalTarget: recovery.FinalTarget,
	}
}

// ValidateTaskRunPlan checks the structural integrity of a redacted handoff.
// It is evidence for an operator, never an executable task specification.
func ValidateTaskRunPlan(plan TaskRunPlan) error {
	if plan.SchemaVersion != 1 {
		return fmt.Errorf("unsupported task plan schema version %d", plan.SchemaVersion)
	}
	if len(plan.Actions) == 0 || len(plan.Actions) > 100 {
		return fmt.Errorf("task plan must contain between 1 and 100 actions")
	}
	if plan.TimeoutMS < 0 {
		return fmt.Errorf("task plan timeout cannot be negative")
	}
	for i, step := range plan.Actions {
		if strings.TrimSpace(step.Action) == "" {
			return fmt.Errorf("task plan action %d has no action name", i)
		}
		if step.Postcondition != "" && !taskRunHasValue(step.Postcondition, "text", "url_contains", "visible_selector", "value", "count") {
			return fmt.Errorf("task plan action %d has unsupported postcondition kind %q", i, step.Postcondition)
		}
	}
	if plan.FinalPostcondition != "" && !taskRunHasValue(plan.FinalPostcondition, "text", "url", "url_contains", "title", "exists_selector", "visible_selector", "value", "count") {
		return fmt.Errorf("task plan has unsupported final postcondition kind %q", plan.FinalPostcondition)
	}
	return nil
}

func taskRunHasValue(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func postconditionTarget(p Postcondition) string {
	switch {
	case p.ExistsSelector != "":
		return p.ExistsSelector
	case p.VisibleSelector != "":
		return p.VisibleSelector
	case p.ValueSelector != "":
		return p.ValueSelector
	case p.CountSelector != "":
		return p.CountSelector
	case p.TargetAbsent != "":
		return p.TargetAbsent
	default:
		return ""
	}
}

func postconditionKind(p Postcondition) string {
	switch {
	case p.Text != "":
		return "text"
	case p.URL != "":
		return "url"
	case p.URLContains != "":
		return "url_contains"
	case p.Title != "":
		return "title"
	case p.ExistsSelector != "":
		return "exists_selector"
	case p.VisibleSelector != "":
		return "visible_selector"
	case p.ValueSelector != "" || p.Value != "":
		return "value"
	case p.CountSelector != "" || p.Count != nil:
		return "count"
	case p.TargetAbsent != "":
		return "target_absent"
	default:
		return ""
	}
}

func (s *Session) updateTaskRunRecovery(ctx context.Context, id, fingerprint string, recovery TaskRunRecovery) error {
	if s.taskRunStore != nil {
		store, ok := s.taskRunStore.(TaskRunProgressStore)
		if !ok {
			return nil
		}
		persistCtx, cancel := taskRunPersistenceContext(ctx)
		defer cancel()
		return store.UpdateTaskRunRecovery(persistCtx, id, fingerprint, recovery)
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, ok := s.taskRuns[id]
	if !ok || entry.Fingerprint != fingerprint || !entry.InFlight {
		return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
	}
	entry.Recovery = cloneTaskRunRecovery(&recovery)
	s.taskRuns[id] = entry
	return nil
}

func (s *Session) updateTaskRunPlan(ctx context.Context, id, fingerprint string, plan *TaskRunPlan) error {
	if plan == nil {
		return fmt.Errorf("task recovery plan is nil")
	}
	if s.taskRunStore != nil {
		store, ok := s.taskRunStore.(TaskRunPlanStore)
		if !ok {
			// Older host stores still protect task-ID reservations, but cannot
			// retain the portable plan metadata used by recovery inspection.
			return nil
		}
		persistCtx, cancel := taskRunPersistenceContext(ctx)
		defer cancel()
		return store.UpdateTaskRunPlan(persistCtx, id, fingerprint, *plan)
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, ok := s.taskRuns[id]
	if !ok || entry.Fingerprint != fingerprint || !entry.InFlight {
		return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
	}
	entry.Plan = cloneTaskRunPlan(plan)
	entry.Timeout = time.Duration(plan.TimeoutMS) * time.Millisecond
	entry.AllowedOrigins = append([]string(nil), plan.AllowedOrigins...)
	entry.AllowPrivateIPs = plan.AllowPrivateIPs
	entry.PolicyID = plan.PolicyID
	entry.PolicyEnforcement = plan.PolicyEnforcement
	s.taskRuns[id] = entry
	return nil
}

func (s *Session) bindTaskRunHandoff(ctx context.Context, id, fingerprint, targetID, browserInstanceID string, plan *TaskRunPlan, recovery TaskRunRecovery) error {
	if plan == nil {
		return fmt.Errorf("task recovery plan is nil")
	}
	if s.taskRunStore != nil {
		if store, ok := s.taskRunStore.(TaskRunHandoffStore); ok {
			persistCtx, cancel := taskRunPersistenceContext(ctx)
			defer cancel()
			return store.BindTaskRunHandoff(persistCtx, id, fingerprint, targetID, browserInstanceID, *plan, recovery)
		}
		if err := s.bindTaskRunTarget(ctx, id, fingerprint, targetID, browserInstanceID); err != nil {
			return err
		}
		if err := s.updateTaskRunPlan(ctx, id, fingerprint, plan); err != nil {
			return err
		}
		return s.updateTaskRunRecovery(ctx, id, fingerprint, recovery)
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, ok := s.taskRuns[id]
	if !ok || entry.Fingerprint != fingerprint || !entry.InFlight {
		return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
	}
	entry.TargetID = targetID
	entry.BrowserInstanceID = browserInstanceID
	entry.Plan = cloneTaskRunPlan(plan)
	entry.Timeout = time.Duration(plan.TimeoutMS) * time.Millisecond
	entry.AllowedOrigins = append([]string(nil), plan.AllowedOrigins...)
	entry.AllowPrivateIPs = plan.AllowPrivateIPs
	entry.PolicyID = plan.PolicyID
	entry.PolicyEnforcement = plan.PolicyEnforcement
	entry.Recovery = cloneTaskRunRecovery(&recovery)
	s.taskRuns[id] = entry
	return nil
}

func (s *Session) executeTaskRunBatch(ctx context.Context, id, fingerprint string, actions []BatchAction, final *Postcondition, recovery *TaskRunRecovery) (*BatchResult, error) {
	return s.ExecuteBatchWithOptionsContext(ctx, actions, BatchOptions{RequireVerified: true, FinalPostcondition: final, beforeFinalVerification: func() error {
		recovery.Phase = "final_verification"
		return s.updateTaskRunRecovery(ctx, id, fingerprint, *recovery)
	}, OnActionProgress: func(index int, status string) error {
		recovery.Current = &TaskRunRecoveryCursor{Index: index, Status: status}
		if status == "finished" {
			recovery.NextAction = index + 1
		}
		return s.updateTaskRunRecovery(ctx, id, fingerprint, *recovery)
	}})
}

func (s *Session) reserveTaskRun(ctx context.Context, id, fingerprint string) (*TaskRunResult, string, bool, error) {
	if s.taskRunStore != nil {
		existing, created, err := s.taskRunStore.ReserveTaskRun(ctx, TaskRunRecord{ID: id, Fingerprint: fingerprint, InFlight: true})
		if err != nil {
			return nil, "", false, err
		}
		if existing.ID != id || existing.Fingerprint != fingerprint {
			if existing.ID == id && existing.Fingerprint != fingerprint {
				return nil, "", false, &TaskRunConflictError{TaskID: id, Reason: "the ID is bound to a different plan"}
			}
			return nil, "", false, fmt.Errorf("task run store returned a record with mismatched ID")
		}
		if created {
			return nil, "", false, nil
		}
		if existing.Resolution != nil {
			return nil, "", false, &TaskRunConflictError{TaskID: id, Reason: "the interrupted task was resolved by its host; use a new task ID for follow-up work"}
		}
		if existing.InFlight {
			return nil, "", false, &TaskRunConflictError{TaskID: id, Reason: "a previous execution may still be in progress; reconcile before reuse"}
		}
		if existing.Result == nil || existing.Result.ID != id || existing.Result.PlanID != fingerprint {
			return nil, "", false, fmt.Errorf("task run store returned an incomplete completed record")
		}
		return cloneTaskRunResult(existing.Result), existing.Error, true, nil
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	if s.taskRuns == nil {
		s.taskRuns = make(map[string]taskRunEntry)
	}
	if existing, ok := s.taskRuns[id]; ok {
		if existing.Fingerprint != fingerprint {
			return nil, "", false, &TaskRunConflictError{TaskID: id, Reason: "the ID is bound to a different plan"}
		}
		if existing.InFlight {
			return nil, "", false, &TaskRunConflictError{TaskID: id, Reason: "an execution with this ID is still in progress"}
		}
		if existing.Resolution != nil {
			return nil, "", false, &TaskRunConflictError{TaskID: id, Reason: "the interrupted task was resolved by its host; use a new task ID for follow-up work"}
		}
		return cloneTaskRunResult(existing.Result), existing.Error, true, nil
	}
	if len(s.taskRuns) >= MaxTaskRunRecordsPerSession {
		return nil, "", false, &TaskRunCapacityError{Limit: MaxTaskRunRecordsPerSession}
	}
	s.taskRuns[id] = taskRunEntry{Fingerprint: fingerprint, InFlight: true}
	return nil, "", false, nil
}

func cloneTaskRunResolution(resolution *TaskRunResolution) *TaskRunResolution {
	if resolution == nil {
		return nil
	}
	copy := *resolution
	return &copy
}

func cloneTaskRunResolutionHistory(history []TaskRunResolution) []TaskRunResolution {
	return append([]TaskRunResolution(nil), history...)
}

func cloneTaskRunRecovery(recovery *TaskRunRecovery) *TaskRunRecovery {
	if recovery == nil {
		return nil
	}
	copy := *recovery
	copy.Steps = append([]TaskRunRecoveryStep(nil), recovery.Steps...)
	if recovery.Current != nil {
		cursor := *recovery.Current
		copy.Current = &cursor
	}
	return &copy
}

func cloneTaskRunPlan(plan *TaskRunPlan) *TaskRunPlan {
	if plan == nil {
		return nil
	}
	copy := *plan
	copy.AllowedOrigins = append([]string(nil), plan.AllowedOrigins...)
	copy.Actions = append([]TaskRunRecoveryStep(nil), plan.Actions...)
	return &copy
}

func validateTaskRunResolution(resolution TaskRunResolution) error {
	if resolution.Status != "verified_complete" && resolution.Status != "rejected" && resolution.Status != "unresolved" {
		return fmt.Errorf("task resolution status must be verified_complete, rejected, or unresolved")
	}
	if strings.TrimSpace(resolution.Evidence) == "" || len(resolution.Evidence) > 4096 {
		return fmt.Errorf("task resolution requires 1-4096 bytes of evidence")
	}
	if _, err := time.Parse(time.RFC3339Nano, resolution.ResolvedAt); err != nil {
		return fmt.Errorf("task resolution has invalid timestamp: %w", err)
	}
	if (resolution.EvidenceSource == "") != (resolution.EvidenceReference == "") {
		return fmt.Errorf("task resolution evidence source and reference must be supplied together")
	}
	if resolution.Status != "unresolved" && (resolution.EvidenceSource == "" || resolution.ObservedAt == "") {
		return fmt.Errorf("terminal task resolution requires evidence_source, evidence_reference, and observed_at")
	}
	if resolution.ObservedAt != "" && resolution.EvidenceSource == "" {
		return fmt.Errorf("task resolution observation time requires evidence source and reference")
	}
	if len(resolution.EvidenceSource) > 128 || len(resolution.EvidenceReference) > 256 {
		return fmt.Errorf("task resolution evidence provenance exceeds its size limit")
	}
	if resolution.EvidenceSource != "" && strings.ContainsAny(resolution.EvidenceSource+resolution.EvidenceReference, "\r\n\x00") {
		return fmt.Errorf("task resolution evidence provenance contains invalid characters")
	}
	if resolution.ObservedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, resolution.ObservedAt); err != nil {
			return fmt.Errorf("task resolution has invalid observation timestamp: %w", err)
		}
	}
	return nil
}

func (s *Session) releaseTaskRunReservation(ctx context.Context, id, fingerprint string) {
	if s.taskRunStore != nil {
		storeCtx, cancel := taskRunPersistenceContext(ctx)
		defer cancel()
		_ = s.taskRunStore.ReleaseTaskRun(storeCtx, id, fingerprint)
		return
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	if entry, ok := s.taskRuns[id]; ok && entry.Fingerprint == fingerprint && entry.InFlight {
		delete(s.taskRuns, id)
	}
}

func (s *Session) bindTaskRunTarget(ctx context.Context, id, fingerprint, targetID, browserInstanceID string) error {
	if s.taskRunStore != nil {
		return s.taskRunStore.BindTaskRunTarget(ctx, id, fingerprint, targetID, browserInstanceID)
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, ok := s.taskRuns[id]
	if !ok || entry.Fingerprint != fingerprint || !entry.InFlight {
		return &TaskRunConflictError{TaskID: id, Reason: "task reservation is no longer active"}
	}
	entry.TargetID = targetID
	entry.BrowserInstanceID = browserInstanceID
	s.taskRuns[id] = entry
	return nil
}

func (s *Session) finishTaskRun(ctx context.Context, id, fingerprint string, result *TaskRunResult, err error) error {
	if s.taskRunStore != nil {
		record := TaskRunRecord{ID: id, Fingerprint: fingerprint, Result: cloneTaskRunResult(result)}
		if result != nil {
			record.TargetID = result.TargetID
			record.BrowserInstanceID = result.BrowserInstanceID
		}
		if err != nil {
			record.Error = err.Error()
		}
		storeCtx, cancel := taskRunPersistenceContext(ctx)
		defer cancel()
		if existing, found, getErr := s.taskRunStore.GetTaskRun(storeCtx, id); getErr == nil && found {
			record.Plan = cloneTaskRunPlan(existing.Plan)
			record.Recovery = cloneTaskRunRecovery(existing.Recovery)
			if record.Plan == nil && existing.Recovery != nil {
				record.Plan = taskRunPlanFromRecovery(existing.Recovery, existing.TargetID, nil, false, "", "", 0)
			}
		}
		if persistErr := s.taskRunStore.CompleteTaskRun(storeCtx, record); persistErr != nil {
			return persistErr
		}
		return nil
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, ok := s.taskRuns[id]
	if !ok || entry.Fingerprint != fingerprint {
		return nil
	}
	entry.InFlight = false
	entry.Result = cloneTaskRunResult(result)
	if entry.Plan != nil && result != nil {
		entry.Plan.TargetID = result.TargetID
		entry.Plan.AllowedOrigins = append([]string(nil), result.AllowedOrigins...)
		entry.Plan.AllowPrivateIPs = result.AllowPrivateIPs
		entry.Plan.PolicyID = result.PolicyID
		entry.Plan.PolicyEnforcement = result.PolicyEnforcement
	}
	if err != nil {
		entry.Error = err.Error()
	}
	s.taskRuns[id] = entry
	return nil
}

func (s *Session) finishUncertainTaskRun(ctx context.Context, id, fingerprint string, result *TaskRunResult, err error) error {
	if s.taskRunStore != nil {
		if store, ok := s.taskRunStore.(TaskRunUncertainResultStore); ok {
			storeCtx, cancel := taskRunPersistenceContext(ctx)
			defer cancel()
			return store.RetainUncertainTaskRun(storeCtx, id, fingerprint, *cloneTaskRunResult(result), err.Error())
		}
		// Legacy stores keep the in-flight reservation as the replay guard. The
		// durable cursor remains available through their existing lookup path.
		return nil
	}
	s.taskRunMu.Lock()
	defer s.taskRunMu.Unlock()
	entry, ok := s.taskRuns[id]
	if !ok || entry.Fingerprint != fingerprint || !entry.InFlight {
		return &TaskRunConflictError{TaskID: id, Reason: "uncertain task reservation is no longer active"}
	}
	entry.Result = cloneTaskRunResult(result)
	entry.Error = err.Error()
	s.taskRuns[id] = entry
	return nil
}

func taskRunPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func cloneTaskRunResult(result *TaskRunResult) *TaskRunResult {
	if result == nil {
		return nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		copy := *result
		return &copy
	}
	var cloned TaskRunResult
	if err := json.Unmarshal(data, &cloned); err != nil {
		copy := *result
		return &copy
	}
	return &cloned
}

func taskRunStatus(batch *BatchResult) string {
	if batch == nil {
		return "incomplete"
	}
	if batch.Unknown > 0 || batch.ResumeBlocked {
		return "needs_reconciliation"
	}
	if batch.Rejected > 0 || batch.StopReason == "verification_required" {
		return "rejected"
	}
	if batch.NextActionIndex < batch.Total {
		return "incomplete"
	}
	return "completed"
}

func normalizeTaskOriginSet(origins []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		origin, err := normalizeTaskOrigin(raw)
		if err != nil {
			return nil, err
		}
		set[origin] = struct{}{}
	}
	return set, nil
}

func normalizeTaskOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%q is not an exact HTTP(S) origin", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" || u.Host == "" || (scheme != "http" && scheme != "https") || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("%q is not an exact HTTP(S) origin", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.Contains(host, "*") {
		return "", fmt.Errorf("%q is not an exact HTTP(S) origin", raw)
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return "", fmt.Errorf("%q has an empty port", raw)
		}
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("%q has an invalid port", raw)
	}
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		host = "[" + ip.String() + "]"
	}
	return scheme + "://" + host + ":" + port, nil
}

func normalizeTaskPageOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%q does not have a valid HTTP(S) origin", raw)
	}
	return normalizeTaskOrigin(strings.ToLower(u.Scheme) + "://" + u.Host)
}

func sortedTaskOrigins(set map[string]struct{}) []string {
	origins := make([]string, 0, len(set))
	for origin := range set {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	return origins
}

// taskOriginPolicyID identifies the normalized origin policy and its semantics
// without depending on the caller's input ordering or spelling.
func taskOriginPolicyID(origins []string, allowPrivateIPs bool) string {
	normalized, err := normalizeTaskOriginSet(origins)
	canonical := sortedTaskOrigins(normalized)
	if err != nil {
		canonical = append([]string{"invalid"}, origins...)
		sort.Strings(canonical[1:])
	}
	scope := "scout-origin-policy-v1\npage-target-http-requests\n"
	if allowPrivateIPs {
		scope += "private-ip-access=allowed\n"
	} else {
		scope += "private-ip-access=blocked\n"
	}
	if len(canonical) == 0 {
		scope += "unrestricted\n"
	} else {
		scope += "exact-allowlist\n"
	}
	digest := sha256.Sum256([]byte(scope + strings.Join(canonical, "\n")))
	return "scout-origin-v1:" + hex.EncodeToString(digest[:])
}
