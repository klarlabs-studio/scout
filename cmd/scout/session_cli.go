package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/scout/agent"
)

// sessionRequest and sessionResponse define the stateful CLI's JSONL protocol.
// One process owns one Scout browser session; every request is routed against
// the active page selected by the preceding commands.
type sessionRequest struct {
	ID                          json.RawMessage      `json:"id,omitempty"`
	Command                     string               `json:"command"`
	URL                         string               `json:"url,omitempty"`
	Selector                    string               `json:"selector,omitempty"`
	Limit                       int                  `json:"limit,omitempty"`
	IncludeHandles              bool                 `json:"include_handles,omitempty"`
	Text                        string               `json:"text,omitempty"`
	Key                         string               `json:"key,omitempty"`
	Handle                      string               `json:"handle,omitempty"`
	ExpiresAt                   string               `json:"expires_at,omitempty"`
	Frame                       string               `json:"frame,omitempty"`
	Tab                         string               `json:"tab,omitempty"`
	Name                        string               `json:"name,omitempty"`
	TargetID                    string               `json:"target_id,omitempty"`
	TaskID                      string               `json:"task_id,omitempty"`
	OperationOrdinal            int                  `json:"operation_ordinal,omitempty"`
	ArtifactID                  string               `json:"artifact_id,omitempty"`
	Offset                      int                  `json:"offset,omitempty"`
	LimitBytes                  int                  `json:"limit_bytes,omitempty"`
	PlanFingerprint             string               `json:"plan_fingerprint,omitempty"`
	ResolutionStatus            string               `json:"resolution_status,omitempty"`
	ResolutionEvidence          string               `json:"resolution_evidence,omitempty"`
	ResolutionEvidenceSource    string               `json:"resolution_evidence_source,omitempty"`
	ResolutionEvidenceReference string               `json:"resolution_evidence_reference,omitempty"`
	ResolutionObservedAt        string               `json:"resolution_observed_at,omitempty"`
	AllowedOrigins              []string             `json:"allowed_origins,omitempty"`
	FinalPostcondition          *agent.Postcondition `json:"final_postcondition,omitempty"`
	TabTargetID                 string               `json:"tab_target_id,omitempty"`
	FrameID                     string               `json:"frame_id,omitempty"`
	ExpectedText                string               `json:"expected_text,omitempty"`
	ExpectedPostcondition       *agent.Postcondition `json:"expected_postcondition,omitempty"`
	MatchURL                    string               `json:"match_url,omitempty"`
	Acceptance                  bool                 `json:"acceptance,omitempty"`
	Wait                        bool                 `json:"wait,omitempty"`
	Options                     map[string]string    `json:"options,omitempty"`
	TimeoutMS                   int                  `json:"timeout_ms,omitempty"`
	ContinueOnUnknown           bool                 `json:"continue_on_unknown,omitempty"`
	RequireVerified             bool                 `json:"require_verified,omitempty"`
	Actions                     []agent.BatchAction  `json:"actions,omitempty"`
}

type sessionResponse struct {
	ID              json.RawMessage             `json:"id,omitempty"`
	OK              bool                        `json:"ok"`
	ContentTrust    string                      `json:"content_trust,omitempty"`
	Result          any                         `json:"result,omitempty"`
	Error           string                      `json:"error,omitempty"`
	ErrorDetails    *cliErrorEnvelope           `json:"error_details,omitempty"`
	RecoveryRecord  *agent.ActionRecoveryRecord `json:"recovery_record,omitempty"`
	PartialResult   any                         `json:"partial_result,omitempty"`
	OutputOmitted   bool                        `json:"output_omitted,omitempty"`
	Truncated       bool                        `json:"truncated,omitempty"`
	OriginalBytes   int                         `json:"original_bytes,omitempty"`
	MaxBytes        int                         `json:"max_bytes,omitempty"`
	Artifact        *sessionOutputArtifact      `json:"artifact,omitempty"`
	RecoveryHint    string                      `json:"recovery_hint,omitempty"`
	IDOmitted       bool                        `json:"id_omitted,omitempty"`
	RecoveryOmitted bool                        `json:"recovery_record_omitted,omitempty"`
}

type sessionOutputArtifact struct {
	ID        string `json:"id"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
	ReadLimit int    `json:"read_limit_bytes"`
}

type sessionOutputStore struct {
	dir     string
	entries map[string]sessionOutputArtifact
	total   int
}

const maxSessionResponseBytes = 256 * 1024
const maxSessionOutputArtifactBytes = 16 * 1024 * 1024
const maxSessionOutputArtifactTotalBytes = 64 * 1024 * 1024
const maxSessionOutputArtifactCount = 32
const maxSessionOutputReadBytes = 128 * 1024

func newSessionOutputStore() (*sessionOutputStore, error) {
	dir, err := os.MkdirTemp("", "scout-session-output-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	return &sessionOutputStore{dir: dir, entries: make(map[string]sessionOutputArtifact)}, nil
}

func (s *sessionOutputStore) close() error {
	if s == nil || s.dir == "" {
		return nil
	}
	return os.RemoveAll(s.dir)
}

func (s *sessionOutputStore) save(data []byte) (*sessionOutputArtifact, error) {
	if s == nil {
		return nil, fmt.Errorf("output artifact storage is unavailable")
	}
	if len(data) > maxSessionOutputArtifactBytes {
		return nil, fmt.Errorf("response exceeds the 16 MiB artifact limit")
	}
	if len(s.entries) >= maxSessionOutputArtifactCount || s.total+len(data) > maxSessionOutputArtifactTotalBytes {
		return nil, fmt.Errorf("session output artifact capacity is exhausted")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(random[:])
	path := filepath.Join(s.dir, id+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, errors.Join(err, os.Remove(path))
	}
	digest := sha256.Sum256(data)
	artifact := sessionOutputArtifact{ID: id, Bytes: len(data), SHA256: hex.EncodeToString(digest[:]), ReadLimit: maxSessionOutputReadBytes}
	s.entries[id] = artifact
	s.total += len(data)
	return &artifact, nil
}

func (s *sessionOutputStore) read(id string, offset, limit int) (map[string]any, error) {
	if s == nil {
		return nil, fmt.Errorf("output artifact storage is unavailable")
	}
	if len(id) != 32 {
		return nil, fmt.Errorf("output artifact ID is invalid")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return nil, fmt.Errorf("output artifact ID is invalid")
	}
	artifact, ok := s.entries[id]
	if !ok {
		return nil, fmt.Errorf("output artifact %q was not found", id)
	}
	if offset < 0 || offset > artifact.Bytes {
		return nil, fmt.Errorf("offset must be between 0 and %d", artifact.Bytes)
	}
	if limit <= 0 || limit > maxSessionOutputReadBytes {
		limit = maxSessionOutputReadBytes
	}
	file, err := os.Open(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data := make([]byte, min(limit, artifact.Bytes-offset))
	n, err := file.ReadAt(data, int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n != len(data) {
		return nil, fmt.Errorf("output artifact is incomplete: read %d of %d requested bytes", n, len(data))
	}
	return map[string]any{
		"artifact_id": id, "offset": offset, "total_bytes": artifact.Bytes,
		"data_base64": base64.StdEncoding.EncodeToString(data), "truncated": offset+len(data) < artifact.Bytes,
		"sha256": artifact.SHA256,
	}, nil
}

func boundSessionResponseWithStore(response sessionResponse, store *sessionOutputStore) sessionResponse {
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) <= maxSessionResponseBytes {
		return response
	}
	bounded := sessionResponse{
		ID:            response.ID,
		OK:            false,
		ContentTrust:  agent.ContentTrustUntrustedPage,
		Error:         "Scout omitted this JSONL response because its serialized size exceeds the 256 KB session response limit.",
		OutputOmitted: true,
		Truncated:     true,
		OriginalBytes: len(encoded),
		MaxBytes:      maxSessionResponseBytes,
		RecoveryHint:  "Narrow the request output or read the saved artifact in bounded chunks with read_output_artifact.",
	}
	if artifact, saveErr := store.save(encoded); saveErr == nil {
		bounded.Artifact = artifact
		bounded.RecoveryHint = "Read the saved response with read_output_artifact while this session process remains running."
	} else {
		bounded.RecoveryHint = "The full response was not saved: " + saveErr.Error() + ". Narrow the request output or use a bounded/disk-backed capture command."
	}
	if response.ErrorDetails != nil {
		bounded.ErrorDetails = &cliErrorEnvelope{Code: response.ErrorDetails.Code, Message: bounded.Error, ContentTrust: response.ErrorDetails.ContentTrust, Outcome: response.ErrorDetails.Outcome}
	}
	if response.RecoveryRecord != nil {
		bounded.RecoveryRecord = response.RecoveryRecord
	}
	if len(bounded.ID) > 4096 {
		bounded.ID = nil
		bounded.IDOmitted = true
	}
	if compact, marshalErr := json.Marshal(bounded); marshalErr != nil || len(compact) > maxSessionResponseBytes {
		bounded.RecoveryRecord = nil
		bounded.RecoveryOmitted = response.RecoveryRecord != nil
	}
	if compact, marshalErr := json.Marshal(bounded); marshalErr != nil || len(compact) > maxSessionResponseBytes {
		bounded.ErrorDetails = nil
	}
	if compact, marshalErr := json.Marshal(bounded); marshalErr != nil || len(compact) > maxSessionResponseBytes {
		bounded.ID = nil
		bounded.IDOmitted = true
	}
	return bounded
}

func serveCLIActionSession(flags cliFlags) {
	log.SetOutput(os.Stderr)
	headless, err := flags.getBool("headless", true)
	if err != nil {
		exitOnErr(err)
	}
	timeout, err := flags.getDuration("timeout", 30*time.Second)
	if err != nil {
		exitOnErr(err)
	}
	allowPrivateIPs, err := flags.getBool("allow-private-ips", false)
	if err != nil {
		exitOnErr(err)
	}
	allowedOrigins, err := configuredCLISessionAllowedOrigins(flags)
	if err != nil {
		exitOnErr(err)
	}
	var taskRunStore agent.TaskRunStore
	var recoveryStore agent.SessionRecoveryStore
	var closeTaskRunStore func() error
	if path := flags.get("task-store", ""); path != "" {
		store, err := agent.OpenBoltTaskRunStore(path)
		if err != nil {
			exitOnErr(err)
		}
		taskRunStore = store
		recoveryStore, _ = any(store).(agent.SessionRecoveryStore)
		closeTaskRunStore = store.Close
	}
	sessionConfig := agent.SessionConfig{
		Headless:        headless,
		Timeout:         timeout,
		AllowPrivateIPs: allowPrivateIPs,
		AllowedOrigins:  allowedOrigins,
		RemoteCDP:       os.Getenv("SCOUT_REMOTE_CDP"),
		ProxyServer:     configuredProxyServer(),
		TaskRunStore:    taskRunStore,
	}
	session, err := agent.NewSessionContext(context.Background(), sessionConfig)
	if err != nil {
		if closeTaskRunStore != nil {
			err = errors.Join(err, closeTaskRunStore())
		}
		exitOnErr(err)
	}
	var closeOnce sync.Once
	var closeErr error
	closeSession := func() error {
		closeOnce.Do(func() {
			closeErr = session.Close()
			if closeTaskRunStore != nil {
				closeErr = errors.Join(closeErr, closeTaskRunStore())
			}
		})
		return closeErr
	}
	if url := flags.get("url", ""); url != "" {
		result, err := session.Navigate(url)
		if err != nil {
			exitOnErr(errors.Join(err, closeSession()))
		}
		if err := writeJSONLine(os.Stdout, sessionResponse{OK: true, Result: result}); err != nil {
			exitOnErr(errors.Join(err, closeSession()))
		}
	}
	runErr := runSessionJSONLWithRecovery(&session, sessionConfig, recoveryStore, cliRecoveryOwnerScope(), os.Stdin, os.Stdout, closeSession)
	if closeErr := closeSession(); closeErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("close session and task store: %w", closeErr))
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "scout session:", runErr)
		os.Exit(1)
	}
}

func configuredCLISessionAllowedOrigins(flags cliFlags) ([]string, error) {
	raw, configured := flags.flags["allowed-origins"]
	if !configured {
		return nil, nil
	}
	origins, err := parseMCPAllowedOrigins(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid --allowed-origins: %w", err)
	}
	return origins, nil
}

func runSessionJSONLWithRecovery(session **agent.Session, sessionConfig agent.SessionConfig, recoveryStore agent.SessionRecoveryStore, ownerScope string, input io.Reader, output io.Writer, closeSession func() error) (retErr error) {
	var outputStore *sessionOutputStore
	defer func() {
		if err := outputStore.close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove session output artifacts: %w", err))
		}
	}()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req sessionRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if writeErr := writeJSONLine(output, sessionResponse{OK: false, Error: "invalid JSON request: " + err.Error()}); writeErr != nil {
				return writeErr
			}
			continue
		}
		if req.Command == "quit" || req.Command == "close" {
			return closeSessionAndRespond(req.ID, output, closeSession)
		}
		if req.Command == "shutdown" {
			return closeSessionAndRespond(req.ID, output, closeSession)
		}
		var result any
		var err error
		switch req.Command {
		case "read_output_artifact":
			if req.ArtifactID == "" {
				err = fmt.Errorf("read_output_artifact requires artifact_id")
			} else {
				if outputStore == nil {
					err = fmt.Errorf("output artifact %q was not found", req.ArtifactID)
				} else {
					result, err = outputStore.read(req.ArtifactID, req.Offset, req.LimitBytes)
				}
			}
		case "persist_session":
			//nolint:gocritic // This branch layout is clearer for this multi-condition check.
			if recoveryStore == nil {
				err = fmt.Errorf("persist_session requires --task-store with durable session recovery support")
			} else if *session == nil {
				err = fmt.Errorf("browser session is unavailable")
			} else {
				var handle string
				handle = req.Handle
				if handle == "" {
					handle, err = agent.NewSessionRecoveryHandle()
				}
				var digest string
				if err == nil {
					digest, err = agent.SessionRecoveryPolicyDigest(sessionConfig)
				}
				expires := time.Now().Add(24 * time.Hour)
				if req.ExpiresAt != "" {
					expires, err = time.Parse(time.RFC3339, req.ExpiresAt)
				}
				name := req.Name
				if name == "" {
					name = "default"
				}
				if err == nil {
					var record agent.SessionRecoveryRecord
					record, err = (*session).PersistRecoveryRecord(context.Background(), recoveryStore, handle, ownerScope, name, digest, expires)
					if err == nil {
						result = map[string]any{"handle": record.Handle, "session_name": record.SessionName, "expires_at": record.ExpiresAt, "target_id": record.TargetID}
					}
				}
			}
		case "resume_session":
			//nolint:gocritic // This branch layout is clearer for this multi-condition check.
			if recoveryStore == nil {
				err = fmt.Errorf("resume_session requires --task-store with durable session recovery support")
			} else if req.Handle == "" {
				err = fmt.Errorf("resume_session requires handle")
			} else {
				var digest string
				digest, err = agent.SessionRecoveryPolicyDigest(sessionConfig)
				if err == nil {
					var resumed *agent.Session
					var record agent.SessionRecoveryRecord
					resumed, record, err = agent.ResumeSessionContext(context.Background(), sessionConfig, recoveryStore, req.Handle, ownerScope, digest)
					if err == nil {
						previousCloseErr := (*session).Close()
						*session = resumed
						resumeResult := map[string]any{"resumed": true, "session_name": record.SessionName, "target_id": record.TargetID}
						if previousCloseErr != nil {
							resumeResult["previous_session_close_error"] = previousCloseErr.Error()
							err = fmt.Errorf("session resumed but closing the previous session failed: %w", previousCloseErr)
						}
						result = resumeResult
					}
				}
			}
		case "revoke_session":
			//nolint:gocritic // This branch layout is clearer for this multi-condition check.
			if recoveryStore == nil {
				err = fmt.Errorf("revoke_session requires --task-store with durable session recovery support")
			} else if req.Handle == "" {
				err = fmt.Errorf("revoke_session requires handle")
			} else {
				var revoked bool
				revoked, err = agent.RevokeSessionRecoveryHandleWithConfig(context.Background(), sessionConfig, recoveryStore, req.Handle, ownerScope)
				if err == nil {
					result = map[string]bool{"revoked": revoked}
				}
			}
		default:
			result, err = executeSessionRequest(*session, req)
		}
		response := sessionResponse{ID: req.ID, OK: err == nil, Result: result}
		if err != nil {
			response.Error, response.ErrorDetails, response.RecoveryRecord = sessionErrorDetails(err)
			if batch, ok := result.(*agent.BatchResult); ok {
				response.PartialResult = batch
				response.Result = nil
			}
			if task, ok := result.(*agent.TaskRunResult); ok {
				response.PartialResult = task
				response.Result = nil
			}
		}
		encoded, _ := json.Marshal(response)
		if len(encoded) > maxSessionResponseBytes && outputStore == nil {
			outputStore, _ = newSessionOutputStore()
		}
		response = boundSessionResponseWithStore(response, outputStore)
		if err := writeJSONLine(output, response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func cliRecoveryOwnerScope() string {
	current, err := user.Current()
	if err != nil || current.Uid == "" {
		return "local-user/unknown"
	}
	digest := sha256.Sum256([]byte(current.Uid))
	return "local-user/" + hex.EncodeToString(digest[:])
}

func sessionErrorDetails(err error) (string, *cliErrorEnvelope, *agent.ActionRecoveryRecord) {
	if err == nil {
		return "", nil, nil
	}
	var stopped *agent.BatchStoppedError
	if errors.As(err, &stopped) {
		hint := "Inspect the partial result and current page; the required verification contract was not met."
		if stopped.Result != nil && stopped.Result.ResumeBlocked {
			hint = "Reconcile the uncertain step before retrying it or continuing with the next_action_index."
		}
		return err.Error(), &cliErrorEnvelope{
			Code:         "SCOUT_BATCH_STOPPED",
			Message:      err.Error(),
			ContentTrust: agent.ContentTrustUntrustedPage,
			RecoveryHint: hint,
		}, nil
	}
	var details cliErrorEnvelope
	if encoded := cliErrorJSON(err); json.Unmarshal([]byte(encoded), &details) != nil {
		details = cliErrorEnvelope{Code: "SCOUT_ERROR", Message: err.Error()}
	}
	var outcome *agent.ActionOutcomeError
	if errors.As(err, &outcome) && outcome.Outcome == agent.OutcomeUnknown && outcome.TargetID != "" {
		if record, recordErr := agent.NewActionRecoveryRecord(outcome); recordErr == nil {
			return details.Message, &details, &record
		}
	}
	return details.Message, &details, nil
}

func closeSessionAndRespond(id json.RawMessage, output io.Writer, closeSession func() error) error {
	closeErr := closeSession()
	response := sessionResponse{ID: id, OK: closeErr == nil, Result: map[string]bool{"closed": closeErr == nil}}
	if closeErr != nil {
		response.Error = closeErr.Error()
	}
	return writeJSONLine(output, response)
}

func writeJSONLine(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func executeSessionRequest(session *agent.Session, req sessionRequest) (any, error) {
	ctx := context.Background()
	if req.TimeoutMS > 0 {
		if uint64(req.TimeoutMS) > uint64((1<<63-1)/int64(time.Millisecond)) {
			return nil, fmt.Errorf("timeout_ms exceeds the supported duration")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	if req.TargetID != "" {
		ctx = agent.WithExpectedTargetID(ctx, req.TargetID)
	}
	condition := agent.Postcondition{Text: req.ExpectedText}
	switch req.Command {
	case "extract_all":
		if strings.TrimSpace(req.Selector) == "" {
			return nil, fmt.Errorf("extract_all requires selector")
		}
		limit := req.Limit
		if limit <= 0 || limit > 8 {
			limit = 8
		}
		return session.ExtractAllPageWithOptionsContext(ctx, req.Selector, req.Offset, limit, agent.ExtractAllOptions{IncludeHandles: req.IncludeHandles})
	case "batch":
		if len(req.Actions) == 0 {
			return nil, fmt.Errorf("batch requires a non-empty actions array")
		}
		return session.ExecuteBatchWithOptionsContext(ctx, req.Actions, agent.BatchOptions{ContinueOnUnknown: req.ContinueOnUnknown, RequireVerified: req.RequireVerified})
	case "task_run":
		if req.TaskID == "" {
			return nil, fmt.Errorf("task_run requires task_id")
		}
		if len(req.Actions) == 0 {
			return nil, fmt.Errorf("task_run requires a non-empty actions array")
		}
		return session.RunTaskContext(ctx, agent.TaskRunSpec{
			ID: req.TaskID, TargetID: req.TargetID, AllowedOrigins: req.AllowedOrigins,
			FinalPostcondition: req.FinalPostcondition, Timeout: time.Duration(req.TimeoutMS) * time.Millisecond, Actions: req.Actions,
		})
	case "get_task_run":
		if req.TaskID == "" {
			return nil, fmt.Errorf("get_task_run requires task_id")
		}
		record, found, err := session.GetTaskRunContext(ctx, req.TaskID)
		if err != nil {
			return nil, err
		}
		result := GetTaskRunResult{ContentTrust: agent.ContentTrustUntrustedPage, Found: found}
		if found {
			result.Record = &record
		}
		return result, nil
	case "recover_task_run":
		if req.TaskID == "" {
			return nil, fmt.Errorf("recover_task_run requires task_id")
		}
		if req.ResolutionStatus != "" || req.ResolutionEvidence != "" || req.ResolutionEvidenceSource != "" || req.ResolutionEvidenceReference != "" || req.ResolutionObservedAt != "" {
			return nil, fmt.Errorf("recover_task_run is read-only; use resolve_task_run for host reconciliation")
		}
		return session.RecoverTaskRunContext(ctx, req.TaskID, req.OperationOrdinal)
	case "export_task_recovery_receipt":
		if req.TaskID == "" {
			return nil, fmt.Errorf("export_task_recovery_receipt requires task_id")
		}
		return session.ExportTaskRunRecoveryReceiptContext(ctx, req.TaskID, req.OperationOrdinal)
	case "resolve_task_run":
		if req.TaskID == "" || req.PlanFingerprint == "" {
			return nil, fmt.Errorf("resolve_task_run requires task_id and plan_fingerprint from get_task_run")
		}
		return session.ResolveInterruptedTaskRunWithEvidenceContext(ctx, req.TaskID, req.PlanFingerprint, agent.TaskRunResolution{
			Status: req.ResolutionStatus, Evidence: req.ResolutionEvidence, EvidenceSource: req.ResolutionEvidenceSource,
			EvidenceReference: req.ResolutionEvidenceReference, ObservedAt: req.ResolutionObservedAt,
		})
	case "navigate":
		if req.URL == "" {
			return nil, fmt.Errorf("navigate requires url")
		}
		if err := validateSessionPostcondition(req.ExpectedText); err != nil {
			return nil, err
		}
		return session.NavigateWithPostconditionContext(ctx, req.URL, condition)
	case "observe":
		return session.ObserveContext(ctx)
	case "annotate":
		result, err := session.AnnotatedScreenshotWithLimitsContext(ctx, 80*1024, 1024)
		if err != nil {
			return nil, err
		}
		return struct {
			TargetID  string                   `json:"target_id,omitempty"`
			FrameID   string                   `json:"frame_id,omitempty"`
			Elements  []agent.AnnotatedElement `json:"elements"`
			Count     int                      `json:"count"`
			Total     int                      `json:"total"`
			Truncated bool                     `json:"truncated"`
			Image     string                   `json:"image,omitempty"`
		}{TargetID: result.TargetID, FrameID: result.FrameID, Elements: result.Elements, Count: result.Count, Total: result.Total, Truncated: result.Truncated, Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(result.Image)}, nil
	case "click":
		if req.Selector == "" {
			return nil, fmt.Errorf("click requires selector")
		}
		if err := validateSessionPostcondition(req.ExpectedText); err != nil {
			return nil, err
		}
		return session.ClickWithPostconditionContext(ctx, req.Selector, condition, req.Wait)
	case "click_handle":
		if req.Handle == "" {
			return nil, fmt.Errorf("click_handle requires handle")
		}
		if err := validateSessionPostcondition(req.ExpectedText); err != nil {
			return nil, err
		}
		return session.ClickByHandleWithIdentityContext(ctx, req.Handle, req.TargetID, req.FrameID, condition)
	case "type":
		if req.Selector == "" {
			return nil, fmt.Errorf("type requires selector")
		}
		return session.TypeContext(ctx, req.Selector, req.Text)
	case "press_key":
		if req.Key == "" {
			return nil, fmt.Errorf("press_key requires key")
		}
		condition := agent.Postcondition{}
		if req.ExpectedPostcondition != nil {
			condition = *req.ExpectedPostcondition
			if err := condition.Validate(); err != nil {
				return nil, err
			}
		}
		return session.PressKeyWithPostconditionContext(ctx, req.Key, condition)
	case "key_down":
		if req.Key == "" {
			return nil, fmt.Errorf("key_down requires key")
		}
		return session.KeyDownContext(ctx, req.Key)
	case "key_up":
		if req.Key == "" {
			return nil, fmt.Errorf("key_up requires key")
		}
		return session.KeyUpContext(ctx, req.Key)
	case "submit":
		if req.Selector == "" {
			return nil, fmt.Errorf("submit requires selector")
		}
		if req.MatchURL == "" {
			return nil, fmt.Errorf("submit requires match_url")
		}
		if err := validateSessionPostcondition(req.ExpectedText); err != nil {
			return nil, err
		}
		condition := agent.Postcondition{Text: req.ExpectedText}
		if req.Acceptance {
			return session.SubmitFormWithAcceptanceContext(ctx, req.Selector, req.MatchURL, req.TimeoutMS, condition)
		}
		return session.SubmitFormWithPostconditionContext(ctx, req.Selector, req.MatchURL, req.TimeoutMS, condition)
	case "eval":
		if req.Text == "" {
			return nil, fmt.Errorf("eval requires text containing the JavaScript expression")
		}
		return session.EvalContext(ctx, req.Text)
	case "switch_frame":
		if req.Frame == "" {
			return nil, fmt.Errorf("switch_frame requires frame selector")
		}
		return session.SwitchToFrameContext(ctx, req.Frame)
	case "main_frame":
		return session.SwitchToMainFrameContext(ctx)
	case "open_tab":
		if req.Name == "" || req.URL == "" {
			return nil, fmt.Errorf("open_tab requires name and url")
		}
		return session.OpenTabContext(ctx, req.Name, req.URL)
	case "switch_tab":
		if req.Tab == "" {
			return nil, fmt.Errorf("switch_tab requires tab")
		}
		return session.SwitchTabForTargetContext(ctx, req.Tab, req.TabTargetID)
	case "close_tab":
		if req.Tab == "" {
			return nil, fmt.Errorf("close_tab requires tab")
		}
		return session.CloseTabResultForTargetContext(ctx, req.Tab, req.TabTargetID)
	case "list_tabs":
		return session.ListTabsContext(ctx)
	case "reconcile":
		var record agent.ActionRecoveryRecord
		rawRecord := req.Options["recovery_record"]
		rawBase64 := req.Options["recovery_record_base64"]
		if rawRecord != "" && rawBase64 != "" {
			return nil, fmt.Errorf("reconcile accepts only one of options.recovery_record or recovery_record_base64")
		}
		if raw := rawRecord; raw != "" {
			decoded, err := agent.DecodeActionRecoveryRecord([]byte(raw))
			if err != nil {
				return nil, fmt.Errorf("decode recovery_record: %w", err)
			}
			record = decoded
		} else if raw := req.Options["recovery_record_base64"]; raw != "" {
			data, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				return nil, fmt.Errorf("decode recovery_record_base64: %w", err)
			}
			decoded, err := agent.DecodeActionRecoveryRecord(data)
			if err != nil {
				return nil, fmt.Errorf("decode recovery_record_base64 payload: %w", err)
			}
			record = decoded
		} else {
			return nil, fmt.Errorf("reconcile requires options.recovery_record or recovery_record_base64")
		}
		return session.ReconcileActionRecoveryRecordContext(ctx, record)
	default:
		return nil, fmt.Errorf("unknown session command %q", req.Command)
	}
}

func validateSessionPostcondition(expectedText string) error {
	if strings.TrimSpace(expectedText) == "" {
		return nil
	}
	return (agent.Postcondition{Text: expectedText}).Validate()
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func boundSessionResponse(response sessionResponse) sessionResponse {
	return boundSessionResponseWithStore(response, nil)
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func runSessionJSONL(session *agent.Session, input io.Reader, output io.Writer, closeSession func() error) error {
	return runSessionJSONLWithRecovery(&session, agent.SessionConfig{}, nil, "", input, output, closeSession)
}
