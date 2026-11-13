package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	browse "go.klarlabs.de/scout"
)

func newSelectionFromPage(page *browse.Page, nodeID int64, selector string) *browse.Selection {
	return browse.NewSelection(page, nodeID, selector)
}

// Action represents a single recorded browser action.
type Action struct {
	Type     string            `json:"type"`               // navigate, click, type, select, scroll, wait, extract
	Selector string            `json:"selector,omitempty"` // CSS selector or :text() selector
	Value    string            `json:"value,omitempty"`    // URL for navigate, text for type, option for select
	Label    int               `json:"label,omitempty"`    // label number for click_label
	Fields   map[string]string `json:"fields,omitempty"`   // for fill_form_semantic
	Expected *ActionExpect     `json:"expected,omitempty"` // expected outcome for replay validation
}

// ActionExpect describes the expected outcome of an action for replay validation.
type ActionExpect struct {
	URL      string `json:"url,omitempty"`      // expected URL after action
	Title    string `json:"title,omitempty"`    // expected page title
	Selector string `json:"selector,omitempty"` // element that should exist after action
	Text     string `json:"text,omitempty"`     // expected text content
}

// Playbook is a recorded sequence of browser actions that can be replayed.
type Playbook struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"` // starting URL
	Actions   []Action  `json:"actions"`
	CreatedAt time.Time `json:"created_at"`
}

// PlaybookResult is the outcome of replaying a playbook.
type PlaybookResult struct {
	Success               bool                  `json:"success"`
	ContentTrust          string                `json:"content_trust,omitempty"`
	StepsRun              int                   `json:"steps_run"`
	TotalSteps            int                   `json:"total_steps"`
	StepReceipts          []PlaybookStepReceipt `json:"step_receipts,omitempty"`
	StepReceiptsTruncated bool                  `json:"step_receipts_truncated,omitempty"`
	PlaybookSHA256        string                `json:"playbook_sha256"`
	ResumeSafe            bool                  `json:"resume_safe"`
	// ResumeToken is a one-use bearer token valid only in the session that
	// produced this result. Progress and safety fields are authenticated; receipt
	// details are restored from the session's trusted checkpoint copy.
	ResumeToken  string            `json:"resume_token,omitempty"`
	FailedAt     int               `json:"failed_at,omitempty"`
	FailedAction *Action           `json:"failed_action,omitempty"`
	Error        string            `json:"error,omitempty"`
	FailurePhase string            `json:"failure_phase,omitempty"`
	CurrentURL   string            `json:"current_url,omitempty"`
	CurrentTitle string            `json:"current_title,omitempty"`
	Evidence     string            `json:"evidence,omitempty"`
	RecoveryHint string            `json:"recovery_hint,omitempty"`
	Extracted    map[string]string `json:"extracted,omitempty"` // data from extract actions
}

// PlaybookStepReceipt links playbook steps and retry attempts to their
// portable browser-operation evidence. Step zero is the initial URL
// navigation; declared playbook actions use one-based indexes. The operation
// ordinal is unique within the owning Scout session.
type PlaybookStepReceipt struct {
	Step           int                   `json:"step"`
	Attempt        int                   `json:"attempt"`
	ActionReceipt  ActionReceipt         `json:"action_receipt"`
	RecoveryRecord *ActionRecoveryRecord `json:"recovery_record,omitempty"`
}

// PlaybookInitialNavigationStep identifies navigation to Playbook.URL. Action
// receipts use one-based indexes; zero is reserved for this setup operation.
const PlaybookInitialNavigationStep = 0

type playbookCheckpoint struct {
	digest       [32]byte
	receiptBytes []byte
}

const maxPlaybookCheckpointBytes = 16 * 1024 * 1024

// ResumePlaybook continues a failed playbook from its first incomplete step.
// The caller is responsible for repairing the current page before resuming.
// A result is resumable only when it includes an unmodified session checkpoint
// token and ResumeSafe is true. Starting-page navigation failures use
// FailedAt == 0 and are retried only when their checkpoint allows it.
func (s *Session) ResumePlaybook(pb *Playbook, previous *PlaybookResult) (*PlaybookResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ResumePlaybookContext(ctx, pb, previous)
}

// ResumePlaybookContext resumes a failed workflow within the caller's
// deadline, including lock wait, browser actions, and expectation checks.
func (s *Session) ResumePlaybookContext(ctx context.Context, pb *Playbook, previous *PlaybookResult) (*PlaybookResult, error) {
	if pb == nil {
		return nil, fmt.Errorf("playbook is nil")
	}
	if previous == nil {
		return nil, fmt.Errorf("previous playbook result is nil")
	}
	if previous.Success {
		return nil, fmt.Errorf("cannot resume a successful playbook")
	}
	if len(previous.PlaybookSHA256) != 64 || previous.PlaybookSHA256 != playbookDigest(pb) {
		return nil, fmt.Errorf("previous result does not identify this playbook; replay it again before resuming")
	}
	if previous.TotalSteps != len(pb.Actions) || previous.StepsRun < 0 || previous.StepsRun > len(pb.Actions) {
		return nil, fmt.Errorf("previous result progress does not match this playbook")
	}
	if previous.FailedAt < 0 || previous.FailedAt > len(pb.Actions) {
		return nil, fmt.Errorf("failed step %d is outside playbook action range", previous.FailedAt)
	}
	if !previous.ResumeSafe {
		return nil, fmt.Errorf("cannot automatically resume this playbook result: the failed action may already have been applied; inspect the current page and restart from a confirmed step")
	}

	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.verifyPlaybookCheckpoint(previous); err != nil {
		return nil, err
	}
	if err := s.restorePlaybookReceipts(previous); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if err := s.consumePlaybookCheckpoint(previous); err != nil {
		return nil, err
	}

	result := &PlaybookResult{
		ContentTrust:   ContentTrustUntrustedPage,
		TotalSteps:     len(pb.Actions),
		PlaybookSHA256: playbookDigest(pb),
		StepsRun:       previous.StepsRun,
		Extracted:      make(map[string]string, len(previous.Extracted)),
		StepReceipts:   append([]PlaybookStepReceipt(nil), previous.StepReceipts...),
	}
	defer func() {
		if !result.Success && result.FailurePhase != "" {
			_ = s.issuePlaybookCheckpoint(result)
		}
	}()
	for k, v := range previous.Extracted {
		result.Extracted[k] = v
	}
	if previous.FailedAt == 0 {
		if pb.URL != "" {
			if err := s.page.NavigateContext(ctx, pb.URL); err != nil {
				var navigationUnknown *browse.NavigationOutcomeUnknownError
				if errors.As(err, &navigationUnknown) || mutationOutcomeMayBeUnknown(err) {
					outcomeErr := s.actionOutcomeAfterDispatchForPage(s.page, "navigate", pb.URL, "playbook starting navigation may have been dispatched but completion was not confirmed", err)
					s.appendPlaybookFailureReceipt(result, PlaybookInitialNavigationStep, Action{Type: "navigate", Value: pb.URL}, outcomeErr)
				}
				s.recordReplayFailureContext(ctx, result, "navigation", fmt.Sprintf("failed to navigate to starting URL %s: %v", pb.URL, err))
				result.ResumeSafe = !errors.As(err, &navigationUnknown) && !mutationOutcomeMayBeUnknown(err)
				return result, nil
			}
			s.appendPlaybookActionReceiptForPage(result, PlaybookInitialNavigationStep, s.page, Action{Type: "navigate", Value: pb.URL}, OutcomeNavigationCompleted, true, "playbook starting navigation completed")
		}
		result.StepsRun = 0
	}

	start := previous.StepsRun
	if previous.FailedAt == 0 {
		start = 0
	}
	for i := start; i < len(pb.Actions); i++ {
		action := pb.Actions[i]
		if err := s.executeActionContext(ctx, action, result); err != nil {
			s.appendPlaybookFailureReceipt(result, i+1, action, err)
			result.StepsRun = i
			result.FailedAt = i + 1
			result.FailedAction = &pb.Actions[i]
			var outcomeErr *ActionOutcomeError
			result.ResumeSafe = !errors.As(err, &outcomeErr)
			s.recordReplayFailureContext(ctx, result, "action", err.Error())
			return result, nil
		}
		result.StepsRun = i + 1
		if action.Expected != nil && hasPlaybookExpectation(*action.Expected) {
			if err := s.validateExpectContext(ctx, action.Expected); err != nil {
				s.appendPlaybookActionReceipt(result, i+1, action, OutcomePostconditionFailed, false, "playbook action completed, but its declared expectation did not match: "+err.Error())
				result.FailedAt = i + 1
				result.FailedAction = &pb.Actions[i]
				s.recordReplayFailureContext(ctx, result, "expectation", fmt.Sprintf("expectation failed: %v", err))
				result.ResumeSafe = true
				return result, nil
			}
			s.appendPlaybookActionReceipt(result, i+1, action, OutcomePostconditionVerified, true, "playbook action completed and all declared expectations matched")
		} else {
			outcome, verified, evidence := playbookStepOutcome(action)
			s.appendPlaybookActionReceipt(result, i+1, action, outcome, verified, evidence)
		}
	}
	result.Success = true
	return result, nil
}

func playbookDigest(pb *Playbook) string {
	data, err := json.Marshal(pb)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// issuePlaybookCheckpoint creates a session-bound, one-use bearer token for a
// failed replay result. The token authenticates progress and safety fields;
// authoritative receipts are retained separately for bounded MCP responses.
func (s *Session) issuePlaybookCheckpoint(result *PlaybookResult) error {
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return fmt.Errorf("create playbook resume token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes[:])
	digest, err := playbookCheckpointDigest(result)
	if err != nil {
		return err
	}
	if s.playbookCheckpoints == nil {
		s.playbookCheckpoints = make(map[string]playbookCheckpoint)
	}
	receiptBytes, err := json.Marshal(result.StepReceipts)
	if err != nil {
		return fmt.Errorf("serialize playbook receipts for resume: %w", err)
	}
	if len(receiptBytes) > maxPlaybookCheckpointBytes {
		result.ResumeSafe = false
		result.RecoveryHint = "The replay result is too large to retain a bounded session checkpoint; inspect the current page and start a fresh replay from a confirmed state."
		return nil
	}
	s.playbookCheckpoints[token] = playbookCheckpoint{digest: digest, receiptBytes: receiptBytes}
	s.playbookCheckpointBytes += len(receiptBytes)
	s.playbookCheckpointOrder = append(s.playbookCheckpointOrder, token)
	for len(s.playbookCheckpointOrder) > 256 || s.playbookCheckpointBytes > maxPlaybookCheckpointBytes {
		oldest := s.playbookCheckpointOrder[0]
		s.playbookCheckpointOrder = s.playbookCheckpointOrder[1:]
		s.playbookCheckpointBytes -= len(s.playbookCheckpoints[oldest].receiptBytes)
		delete(s.playbookCheckpoints, oldest)
	}
	result.ResumeToken = token
	return nil
}

func (s *Session) consumePlaybookCheckpoint(result *PlaybookResult) error {
	if err := s.verifyPlaybookCheckpoint(result); err != nil {
		return err
	}
	checkpoint := s.playbookCheckpoints[result.ResumeToken]
	s.playbookCheckpointBytes -= len(checkpoint.receiptBytes)
	delete(s.playbookCheckpoints, result.ResumeToken)
	for i, token := range s.playbookCheckpointOrder {
		if token == result.ResumeToken {
			s.playbookCheckpointOrder = append(s.playbookCheckpointOrder[:i], s.playbookCheckpointOrder[i+1:]...)
			break
		}
	}
	return nil
}

func (s *Session) verifyPlaybookCheckpoint(result *PlaybookResult) error {
	if result.ResumeToken == "" {
		return fmt.Errorf("playbook result has no session-bound resume token; replay it again in this session")
	}
	checkpoint, ok := s.playbookCheckpoints[result.ResumeToken]
	if !ok {
		return fmt.Errorf("playbook resume token is unknown, expired, or already used")
	}
	digest, err := playbookCheckpointDigest(result)
	if err != nil {
		return err
	}
	if digest != checkpoint.digest {
		return fmt.Errorf("playbook result changed after replay; use the original result or replay the workflow again")
	}
	return nil
}

func (s *Session) restorePlaybookReceipts(result *PlaybookResult) error {
	checkpoint, ok := s.playbookCheckpoints[result.ResumeToken]
	if !ok {
		return fmt.Errorf("playbook resume token is unknown, expired, or already used")
	}
	var receipts []PlaybookStepReceipt
	if err := json.Unmarshal(checkpoint.receiptBytes, &receipts); err != nil {
		return fmt.Errorf("restore trusted playbook receipts: %w", err)
	}
	result.StepReceipts = receipts
	result.StepReceiptsTruncated = false
	return nil
}

func playbookCheckpointDigest(result *PlaybookResult) ([32]byte, error) {
	copy := *result
	copy.ResumeToken = ""
	// Receipts are retained in the session checkpoint and restored from that
	// trusted copy when resuming. This lets bounded MCP responses omit receipt
	// detail without making the resume token unusable or trusting edited data.
	copy.StepReceipts = nil
	copy.StepReceiptsTruncated = false
	data, err := json.Marshal(copy)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

// recording holds state while recording actions.
type recording struct {
	name    string
	url     string
	actions []Action
}

// StartRecordingPlaybook begins recording all agent actions into a playbook.
func (s *Session) StartRecordingPlaybook(name string) {
	_ = s.StartRecordingPlaybookContext(context.Background(), name)
}

// StartRecordingPlaybookContext begins recording under ctx's lock deadline.
func (s *Session) StartRecordingPlaybookContext(ctx context.Context, name string) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()

	url := ""
	if s.page != nil {
		url, _ = s.page.URL()
	}

	s.recording = &recording{
		name: name,
		url:  url,
	}
	return ctx.Err()
}

// StopRecordingPlaybook stops recording and returns the playbook.
func (s *Session) StopRecordingPlaybook() (*Playbook, error) {
	return s.StopRecordingPlaybookContext(context.Background())
}

// StopRecordingPlaybookContext stops recording under ctx's lock deadline.
func (s *Session) StopRecordingPlaybookContext(ctx context.Context) (*Playbook, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if s.recording == nil {
		return nil, fmt.Errorf("no recording in progress")
	}

	pb := &Playbook{
		Name:      s.recording.name,
		URL:       s.recording.url,
		Actions:   s.recording.actions,
		CreatedAt: time.Now(),
	}
	s.recording = nil
	return pb, ctx.Err()
}

// ExpectRecordedAction attaches an explicit replay assertion to the most
// recently recorded action. This avoids guessing which transient DOM changes
// should be replay invariants.
func (s *Session) ExpectRecordedAction(expect ActionExpect) error {
	return s.ExpectRecordedActionContext(context.Background(), expect)
}

// ExpectRecordedActionContext updates the last expectation under ctx's lock deadline.
func (s *Session) ExpectRecordedActionContext(ctx context.Context, expect ActionExpect) error {
	if expect.URL == "" && expect.Title == "" && expect.Selector == "" && expect.Text == "" {
		return fmt.Errorf("at least one expected URL, title, selector, or text is required")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.recording == nil {
		return fmt.Errorf("no recording in progress")
	}
	if len(s.recording.actions) == 0 {
		return fmt.Errorf("no recorded action to attach expectation to")
	}
	copy := expect
	s.recording.actions[len(s.recording.actions)-1].Expected = &copy
	return nil
}

// recordAction appends an action to the current recording (if active).
// Caller must hold s.mu.
func (s *Session) recordAction(a Action) {
	if s.recording != nil {
		s.recording.actions = append(s.recording.actions, a)
	}
}

// SavePlaybook saves a playbook to a private JSON file. It writes a complete
// temporary file beside the destination and atomically replaces the path so
// readers do not observe a partially written playbook. Replacing an existing
// path also resets its permissions to 0600.
func SavePlaybook(pb *Playbook, path string) error {
	if pb == nil {
		return fmt.Errorf("playbook is nil")
	}
	data, err := json.MarshalIndent(pb, "", "  ")
	if err != nil {
		return fmt.Errorf("encode playbook: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary playbook file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set playbook permissions: %w", err)
	}
	n, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write playbook: %w", err)
	}
	if n != len(data) {
		_ = tmp.Close()
		return fmt.Errorf("write playbook: %w", io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync playbook: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close playbook: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace playbook: %w", err)
	}
	committed = true
	return nil
}

// LoadPlaybook loads a playbook from a JSON file.
func LoadPlaybook(path string) (*Playbook, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: caller-controlled playbook path is intentional
	if err != nil {
		return nil, err
	}
	var pb Playbook
	if err := json.Unmarshal(data, &pb); err != nil {
		return nil, err
	}
	return &pb, nil
}

// ReplayPlaybook executes a recorded playbook deterministically.
// Returns the result including any extracted data and where it failed (if applicable).
func (s *Session) ReplayPlaybook(pb *Playbook) (*PlaybookResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ReplayPlaybookContext(ctx, pb)
}

// ReplayPlaybookContext executes a deterministic playbook within the caller's
// deadline, including setup, each action, and its assertions.
func (s *Session) ReplayPlaybookContext(ctx context.Context, pb *Playbook) (*PlaybookResult, error) {
	if pb == nil {
		return nil, fmt.Errorf("playbook is nil")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	result := &PlaybookResult{
		ContentTrust:   ContentTrustUntrustedPage,
		TotalSteps:     len(pb.Actions),
		PlaybookSHA256: playbookDigest(pb),
		Extracted:      make(map[string]string),
	}
	defer func() {
		if !result.Success && result.FailurePhase != "" {
			_ = s.issuePlaybookCheckpoint(result)
		}
	}()

	// Navigate to starting URL. Remote tabs are owned by the caller, so reuse
	// the attached page; local sessions replace their initial blank tab.
	if pb.URL != "" {
		if err := s.restoreFrameRouteLocked(ctx); err != nil {
			s.recordReplayFailureContext(ctx, result, "navigation", fmt.Sprintf("failed to detach selected iframe before starting navigation: %v", err))
			result.ResumeSafe = true
			return result, nil
		}
		if s.remoteBrowserURL == "" {
			page, err := s.newPageContext(ctx)
			if err != nil {
				s.recordReplayFailureContext(ctx, result, "navigation", fmt.Sprintf("failed to open page for %s: %v", pb.URL, err))
				result.ResumeSafe = true
				return result, nil
			}
			if s.stealth {
				if err := s.applyStealthPatches(ctx, page); err != nil {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
					cleanupErr := page.CloseContext(cleanupCtx)
					cancel()
					s.recordReplayFailureContext(ctx, result, "stealth_setup", fmt.Sprintf("failed to apply stealth configuration: %v", errors.Join(err, cleanupErr)))
					result.ResumeSafe = true
					return result, nil
				}
			}
			if err := s.applyPageURLPolicy(page); err != nil {
				cleanupErr := s.cleanupUninitializedPage(page, true)
				s.recordReplayFailureContext(ctx, result, "navigation", fmt.Sprintf("failed to apply page URL policy: %v", errors.Join(err, cleanupErr)))
				result.ResumeSafe = true
				return result, nil
			}
			if err := page.NavigateContext(ctx, pb.URL); err != nil {
				var navigationUnknown *browse.NavigationOutcomeUnknownError
				if errors.As(err, &navigationUnknown) || mutationOutcomeMayBeUnknown(err) {
					oldPage := s.page
					s.page = page
					s.diffInstalled = false
					s.frameID = ""
					s.frameContextID = 0
					if oldPage != nil {
						cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
						_ = oldPage.CloseContext(cleanupCtx)
						cancel()
					}
					outcomeErr := s.actionOutcomeAfterDispatchForPage(page, "navigate", pb.URL, "playbook starting navigation may have been dispatched but completion was not confirmed", err)
					s.appendPlaybookFailureReceipt(result, PlaybookInitialNavigationStep, Action{Type: "navigate", Value: pb.URL}, outcomeErr)
				} else {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
					_ = page.CloseContext(cleanupCtx)
					cancel()
				}
				s.recordReplayFailureContext(ctx, result, "navigation", fmt.Sprintf("failed to navigate to starting URL %s: %v", pb.URL, err))
				result.ResumeSafe = !errors.As(err, &navigationUnknown) && !mutationOutcomeMayBeUnknown(err)
				return result, nil
			}
			s.appendPlaybookActionReceiptForPage(result, PlaybookInitialNavigationStep, page, Action{Type: "navigate", Value: pb.URL}, OutcomeNavigationCompleted, true, "playbook starting navigation completed")
			oldPage := s.page
			s.page = page
			s.diffInstalled = false
			s.frameID = ""
			s.frameContextID = 0
			if oldPage != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = oldPage.CloseContext(cleanupCtx)
				cancel()
			}
		} else if err := s.page.NavigateContext(ctx, pb.URL); err != nil {
			var navigationUnknown *browse.NavigationOutcomeUnknownError
			if errors.As(err, &navigationUnknown) || mutationOutcomeMayBeUnknown(err) {
				outcomeErr := s.actionOutcomeAfterDispatchForPage(s.page, "navigate", pb.URL, "playbook starting navigation may have been dispatched but completion was not confirmed", err)
				s.appendPlaybookFailureReceipt(result, PlaybookInitialNavigationStep, Action{Type: "navigate", Value: pb.URL}, outcomeErr)
			}
			s.recordReplayFailureContext(ctx, result, "navigation", fmt.Sprintf("failed to navigate to starting URL %s: %v", pb.URL, err))
			result.ResumeSafe = !errors.As(err, &navigationUnknown) && !mutationOutcomeMayBeUnknown(err)
			return result, nil
		}
		s.appendPlaybookActionReceiptForPage(result, PlaybookInitialNavigationStep, s.page, Action{Type: "navigate", Value: pb.URL}, OutcomeNavigationCompleted, true, "playbook starting navigation completed")
	}

	for i, action := range pb.Actions {
		err := s.executeActionContext(ctx, action, result)
		if err != nil {
			s.appendPlaybookFailureReceipt(result, i+1, action, err)
			result.StepsRun = i
			result.FailedAt = i + 1
			result.FailedAction = &pb.Actions[i]
			var outcomeErr *ActionOutcomeError
			result.ResumeSafe = !errors.As(err, &outcomeErr)
			s.recordReplayFailureContext(ctx, result, "action", err.Error())
			return result, nil
		}
		result.StepsRun = i + 1

		// Validate expected outcome
		if action.Expected != nil && hasPlaybookExpectation(*action.Expected) {
			if err := s.validateExpectContext(ctx, action.Expected); err != nil {
				s.appendPlaybookActionReceipt(result, i+1, action, OutcomePostconditionFailed, false, "playbook action completed, but its declared expectation did not match: "+err.Error())
				result.FailedAt = i + 1
				result.FailedAction = &pb.Actions[i]
				s.recordReplayFailureContext(ctx, result, "expectation", fmt.Sprintf("expectation failed: %v", err))
				result.ResumeSafe = true
				return result, nil
			}
			s.appendPlaybookActionReceipt(result, i+1, action, OutcomePostconditionVerified, true, "playbook action completed and all declared expectations matched")
		} else {
			outcome, verified, evidence := playbookStepOutcome(action)
			s.appendPlaybookActionReceipt(result, i+1, action, outcome, verified, evidence)
		}
	}

	result.Success = true
	return result, nil
}

func playbookStepOutcome(action Action) (ActionOutcome, bool, string) {
	switch action.Type {
	case "navigate":
		return OutcomeNavigationCompleted, true, "playbook navigation completed"
	case "wait":
		return OutcomePostconditionVerified, true, "the requested selector was observed"
	case "extract":
		return OutcomeValueVerified, true, "text was read from the requested element"
	default:
		return OutcomeEffectUnverified, false, "playbook action completed; resulting application state was not asserted"
	}
}

func hasPlaybookExpectation(expect ActionExpect) bool {
	return expect.URL != "" || expect.Title != "" || expect.Selector != "" || expect.Text != ""
}

func playbookExpectedPostcondition(expect *ActionExpect) *Postcondition {
	if expect == nil || !hasPlaybookExpectation(*expect) {
		return nil
	}
	// A portable recovery predicate supports one condition. Prefer the most
	// specific declared condition in the same order replay validates them;
	// full replay still checks every expectation before reporting success.
	switch {
	case expect.URL != "":
		return &Postcondition{URL: expect.URL}
	case expect.Title != "":
		return &Postcondition{Title: expect.Title}
	case expect.Selector != "":
		return &Postcondition{ExistsSelector: expect.Selector}
	default:
		return &Postcondition{Text: expect.Text}
	}
}

func (s *Session) appendPlaybookActionReceipt(result *PlaybookResult, step int, action Action, outcome ActionOutcome, verified bool, evidence string) {
	s.appendPlaybookActionReceiptForPage(result, step, s.page, action, outcome, verified, evidence)
}

func (s *Session) appendPlaybookActionReceiptForPage(result *PlaybookResult, step int, page *browse.Page, action Action, outcome ActionOutcome, verified bool, evidence string) {
	if s == nil || result == nil || page == nil {
		return
	}
	target := action.Selector
	if action.Type == "navigate" {
		target = action.Value
	}
	ordinal := s.nextOperationOrdinal()
	postcondition := playbookExpectedPostcondition(action.Expected)
	receipt := s.newActionReceipt(action.Type, target, page.TargetID(), string(outcome), evidence, "", verified, ordinal, postcondition)
	result.StepReceipts = append(result.StepReceipts, PlaybookStepReceipt{
		Step: step, Attempt: playbookReceiptAttempt(result.StepReceipts, step), ActionReceipt: *receipt,
	})
}

func (s *Session) appendPlaybookFailureReceipt(result *PlaybookResult, step int, action Action, err error) {
	var outcome *ActionOutcomeError
	if errors.As(err, &outcome) && outcome != nil {
		receipt := outcome.Receipt
		if receipt == nil {
			targetID := outcome.TargetID
			if targetID == "" && s.page != nil {
				targetID = s.page.TargetID()
			}
			receipt = s.newActionReceipt(outcome.Action, outcome.Target, targetID, string(outcome.Outcome), outcome.Evidence, outcome.RecoveryHint, false, outcome.OperationOrdinal, outcome.ExpectedPostcondition)
		}
		stepReceipt := PlaybookStepReceipt{
			Step: step, Attempt: playbookReceiptAttempt(result.StepReceipts, step), ActionReceipt: *receipt,
		}
		if outcome.Outcome == OutcomeUnknown {
			if record, recordErr := NewActionRecoveryRecord(outcome); recordErr == nil {
				stepReceipt.RecoveryRecord = &record
			}
		}
		result.StepReceipts = append(result.StepReceipts, stepReceipt)
		return
	}
	// A failure before dispatch still belongs to the step history. It is
	// explicitly rejected so a host can distinguish it from an uncertain
	// mutation and decide whether a repaired replay is safe.
	s.appendPlaybookActionReceipt(result, step, action, OutcomeActionFailed, false, err.Error())
	if len(result.StepReceipts) > 0 {
		result.StepReceipts[len(result.StepReceipts)-1].ActionReceipt.RecoveryHint = "Correct the reported target or input, then resume after confirming the current page state."
	}
}

func playbookReceiptAttempt(receipts []PlaybookStepReceipt, step int) int {
	attempt := 1
	for _, receipt := range receipts {
		if receipt.Step == step {
			attempt++
		}
	}
	return attempt
}

func (s *Session) recordReplayFailureContext(ctx context.Context, result *PlaybookResult, phase, message string) {
	result.Error = message
	result.FailurePhase = phase
	if s.page != nil && ctx.Err() == nil {
		result.CurrentURL, _ = pageURLContext(ctx, s.page)
		if title, err := s.page.EvaluateContext(ctx, `document.title`); err == nil {
			result.CurrentTitle, _ = title.(string)
		}
		if evidence, err := s.page.EvaluateContext(ctx, `document.body ? document.body.innerText : ''`); err == nil {
			if text, ok := evidence.(string); ok {
				result.Evidence = strings.TrimSpace(text)
				if len(result.Evidence) > 500 {
					result.Evidence = result.Evidence[:500]
				}
			}
		}
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "not found") || strings.Contains(lower, "no node"):
		result.RecoveryHint = "Re-observe the current page and update the failed selector or recorded action."
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out"):
		result.RecoveryHint = "Check whether the page is still loading or the target appears after an SPA update, then add an appropriate wait."
	case phase == "expectation":
		result.RecoveryHint = "The action ran but its expected result did not match. Inspect the page, then resume; Scout will continue after this executed action without repeating it."
	case phase == "action" && !result.ResumeSafe:
		result.RecoveryHint = "The action may have been applied but could not be verified. Inspect the current page before retrying; automatic resume is disabled to avoid repeating a possibly completed mutation."
	case phase == "navigation":
		result.RecoveryHint = "Check the starting URL and browser connectivity, repair the session if needed, then resume to retry starting-page navigation."
	default:
		result.RecoveryHint = "Inspect the current page evidence and revise or retry the failed playbook step."
	}
}

func pageURLContext(ctx context.Context, page *browse.Page) (string, error) {
	value, err := page.EvaluateContext(ctx, `window.location.href`)
	if err != nil {
		return "", err
	}
	url, _ := value.(string)
	return url, nil
}

func (s *Session) newPageContext(ctx context.Context) (*browse.Page, error) {
	creator, ok := s.browser.(interface {
		NewPageContext(context.Context) (*browse.Page, error)
	})
	if !ok {
		return nil, fmt.Errorf("browser does not support context-aware page creation")
	}
	return creator.NewPageContext(ctx)
}

func (s *Session) executeActionContext(ctx context.Context, a Action, result *PlaybookResult) error {
	outcome := func(action, target, evidence string, err error) error {
		if err == nil {
			return nil
		}
		var navigationUnknown *browse.NavigationOutcomeUnknownError
		if !mutationOutcomeMayBeUnknown(err) && !errors.As(err, &navigationUnknown) {
			return err
		}
		if condition := playbookExpectedPostcondition(a.Expected); condition != nil {
			return s.actionOutcomeAfterDispatch(action, target, evidence, err, *condition)
		}
		return s.actionOutcomeAfterDispatch(action, target, evidence, err)
	}
	switch a.Type {
	case "navigate":
		err := s.page.NavigateContext(ctx, a.Value)
		return outcome("navigate", a.Value, "playbook navigation may have been dispatched but completion was not confirmed", err)
	case "click":
		nodeID, err := s.resolveSelectorContext(ctx, a.Selector)
		if err != nil {
			return fmt.Errorf("click: element %q not found: %w", a.Selector, err)
		}
		if err := newSelectionFromPage(s.page, nodeID, a.Selector).ClickContext(ctx); err != nil {
			return outcome("click", a.Selector, "playbook click may have been dispatched but completion was not confirmed", fmt.Errorf("click failed: %w", err))
		}
		err = s.page.WaitStableContext(ctx, 300*time.Millisecond)
		if err != nil {
			if condition := playbookExpectedPostcondition(a.Expected); condition != nil {
				return s.actionOutcomeAfterDispatch("click", a.Selector, "playbook click was dispatched but page settling was not confirmed", err, *condition)
			}
			return s.actionOutcomeAfterDispatch("click", a.Selector, "playbook click was dispatched but page settling was not confirmed", err)
		}
		return nil
	case "type":
		nodeID, err := s.resolveSelectorContext(ctx, a.Selector)
		if err != nil {
			return fmt.Errorf("type: element %q not found: %w", a.Selector, err)
		}
		err = newSelectionFromPage(s.page, nodeID, a.Selector).InputContext(ctx, a.Value)
		return outcome("type", a.Selector, "playbook input may have been dispatched but the field value was not confirmed", err)
	case "select":
		nodeID, err := s.resolveSelectorContext(ctx, a.Selector)
		if err != nil {
			return fmt.Errorf("select: element %q not found: %w", a.Selector, err)
		}
		selected, err := formNodeCallContext(ctx, s.page, nodeID, `function(want){if(this.tagName!=='SELECT')return false;for(const opt of this.options){if(opt.text.trim()===want||opt.value===want){const setter=Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype,'value').set;setter.call(this,opt.value);this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}));return true}}return false}`, []any{a.Value})
		if err != nil {
			return outcome("select_option", a.Selector, "playbook selection may have been dispatched but the selected value was not confirmed", err)
		}
		if ok, _ := selected.(bool); !ok {
			return fmt.Errorf("select: option %q not found in %s", a.Value, a.Selector)
		}
		return nil
	case "scroll":
		nodeID, err := s.resolveSelectorContext(ctx, a.Selector)
		if err != nil {
			return fmt.Errorf("scroll: element %q not found: %w", a.Selector, err)
		}
		_, err = formNodeCallContext(ctx, s.page, nodeID, `function(){this.scrollIntoView({behavior:'instant',block:'center'});return true}`, nil)
		return outcome("scroll_to", a.Selector, "playbook scroll may have been dispatched but the resulting viewport was not confirmed", err)
	case "wait":
		return s.waitForSelectorContext(ctx, a.Selector)
	case "fill_form":
		if a.Fields == nil {
			return fmt.Errorf("fill_form: no fields specified")
		}
		changedFields := 0
		for selector, value := range a.Fields {
			nodeID, err := s.resolveSelectorContext(ctx, selector)
			if err != nil {
				if changedFields > 0 {
					if condition := playbookExpectedPostcondition(a.Expected); condition != nil {
						return s.actionOutcomeAfterDispatch("fill_form", selector, "playbook form fill stopped after an earlier field mutation", err, *condition)
					}
					return s.actionOutcomeAfterDispatch("fill_form", selector, "playbook form fill stopped after an earlier field mutation", err)
				}
				return fmt.Errorf("fill_form: field %q not found: %w", selector, err)
			}
			if err := newSelectionFromPage(s.page, nodeID, selector).InputContext(ctx, value); err != nil {
				wrapped := fmt.Errorf("fill_form: failed to fill %q: %w", selector, err)
				if condition := playbookExpectedPostcondition(a.Expected); condition != nil {
					return s.actionOutcomeAfterDispatch("fill_form", selector, "playbook form fill may be partial; inspect fields before retrying", wrapped, *condition)
				}
				return s.actionOutcomeAfterDispatch("fill_form", selector, "playbook form fill may be partial; inspect fields before retrying", wrapped)
			}
			changedFields++
		}
		return nil
	case "extract":
		nodeID, err := s.resolveSelectorContext(ctx, a.Selector)
		if err != nil {
			return fmt.Errorf("extract: element %q not found: %w", a.Selector, err)
		}
		value, err := formNodeCallContext(ctx, s.page, nodeID, `function(){return this.textContent||''}`, nil)
		if err != nil {
			return err
		}
		key := a.Selector
		if a.Value != "" {
			key = a.Value
		}
		result.Extracted[key], _ = value.(string)
		return nil
	default:
		return fmt.Errorf("unknown action type: %s", a.Type)
	}
}

func (s *Session) validateExpectContext(ctx context.Context, expect *ActionExpect) error {
	if expect.URL != "" {
		url, err := pageURLContext(ctx, s.page)
		if err != nil {
			return err
		}
		if url != expect.URL {
			return fmt.Errorf("expected URL %q, got %q", expect.URL, url)
		}
	}
	if expect.Title != "" {
		title, err := s.page.EvaluateContext(ctx, `document.title`)
		if err != nil {
			return err
		}
		if titleString, _ := title.(string); titleString != expect.Title {
			return fmt.Errorf("expected title %q, got %q", expect.Title, titleString)
		}
	}
	if expect.Selector != "" {
		if _, err := s.resolveSelectorContext(ctx, expect.Selector); err != nil {
			return fmt.Errorf("expected element %q not found: %w", expect.Selector, err)
		}
	}
	if expect.Text != "" {
		textJSON, _ := json.Marshal(expect.Text)
		value, err := s.page.EvaluateContext(ctx, fmt.Sprintf(`document.body ? document.body.innerText.includes(%s) : false`, textJSON))
		if err != nil {
			return err
		}
		if matched, _ := value.(bool); !matched {
			return fmt.Errorf("expected text %q not found", expect.Text)
		}
	}
	return nil
}
