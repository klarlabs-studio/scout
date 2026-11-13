// Command durable_task_recovery demonstrates a signed recovery handoff across
// Scout processes, action-dispatch and result-persistence crash boundaries,
// completed-result retry, and browser restart. The parent owns the broker token,
// Chrome, and application fixture; handoff matching never authorizes replay.
//
// Run with: go run ./examples/durable_task_recovery
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
	"go.klarlabs.de/scout/internal/launcher"
)

const taskID = "submit-once-001"

type childResult struct {
	TargetID             string `json:"target_id"`
	BrowserInstanceID    string `json:"browser_instance_id,omitempty"`
	PolicyID             string `json:"policy_id,omitempty"`
	Status               string `json:"status"`
	PlanID               string `json:"plan_id"`
	IdempotencyReplay    bool   `json:"idempotency_replay,omitempty"`
	InFlight             bool   `json:"in_flight,omitempty"`
	RecoveryPhase        string `json:"recovery_phase,omitempty"`
	CursorStatus         string `json:"cursor_status,omitempty"`
	NextAction           int    `json:"next_action,omitempty"`
	ResolutionStatus     string `json:"resolution_status,omitempty"`
	ResolutionSource     string `json:"resolution_source,omitempty"`
	ResolutionReference  string `json:"resolution_reference,omitempty"`
	ResolutionObservedAt string `json:"resolution_observed_at,omitempty"`
	AuthoritativeCount   int    `json:"authoritative_count,omitempty"`
	RetryBlocked         bool   `json:"retry_blocked,omitempty"`
	HandoffMatched       bool   `json:"handoff_matched,omitempty"`
	HandoffReplaySafe    bool   `json:"handoff_replay_safe,omitempty"`
}

type scenarioResult struct {
	TaskID                         string `json:"task_id"`
	FollowUpTaskID                 string `json:"follow_up_task_id,omitempty"`
	DuplicateTaskID                string `json:"duplicate_task_id,omitempty"`
	ApplicationKey                 string `json:"application_idempotency_key,omitempty"`
	ApplicationDuplicateSuppressed bool   `json:"application_duplicate_suppressed,omitempty"`
	Boundary                       string `json:"boundary"`
	RecoveryPhase                  string `json:"recovery_phase,omitempty"`
	CursorStatus                   string `json:"cursor_status,omitempty"`
	SameIDRetryBlocked             bool   `json:"same_id_retry_blocked"`
	HostResolution                 string `json:"host_resolution,omitempty"`
	EvidenceSource                 string `json:"evidence_source,omitempty"`
	EvidenceReference              string `json:"evidence_reference,omitempty"`
	EvidenceObservedAt             string `json:"evidence_observed_at,omitempty"`
	AcceptedForTask                int    `json:"accepted_for_task"`
	TotalAccepted                  int    `json:"total_accepted"`
	CompletedResultReused          bool   `json:"completed_result_reused,omitempty"`
}

type handoffResult struct {
	NodeOwnerMatched                 bool `json:"node_owner_matched"`
	NodeBrowserMatched               bool `json:"node_browser_matched"`
	NodeTargetMatched                bool `json:"node_target_matched"`
	NodePolicyMatched                bool `json:"node_policy_matched"`
	NodeFrameBindingAvailable        bool `json:"node_frame_binding_available"`
	NodeFullContextMatched           bool `json:"node_full_context_matched"`
	TamperedEnvelopeRejected         bool `json:"tampered_envelope_rejected"`
	SeparateScoutContextMatched      bool `json:"separate_scout_context_matched"`
	ReplacementBrowserMatched        bool `json:"replacement_browser_matched"`
	GenerationDerivedFromLiveProcess bool `json:"generation_derived_from_live_process"`
	PolicyReadFromLiveSession        bool `json:"policy_read_from_live_session"`
	ReplayAllowed                    bool `json:"replay_allowed"`
}

type runReport struct {
	SchemaVersion int              `json:"schema_version"`
	Scenario      string           `json:"scenario"`
	ObservedAt    string           `json:"observed_at_utc"`
	GoVersion     string           `json:"go_version"`
	Platform      string           `json:"platform"`
	Handoff       handoffResult    `json:"handoff"`
	Results       []scenarioResult `json:"results"`
	Limitations   []string         `json:"limitations"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var err error
	if os.Getenv("SCOUT_RECOVERY_PHASE") != "" {
		err = runChild(ctx)
	} else {
		err = run(ctx)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func localBrowserGeneration(processID int, browserFingerprint string) (string, error) {
	if processID <= 0 || strings.TrimSpace(browserFingerprint) == "" {
		return "", fmt.Errorf("trusted local broker needs a live Chrome process ID and Scout browser fingerprint")
	}
	canonical := fmt.Sprintf("scout-local-browser-generation-v1\x00%d\x00%s", processID, browserFingerprint)
	digest := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func run(ctx context.Context) error {
	report := runReport{
		SchemaVersion: 1,
		Scenario:      "durable-task-recovery-example",
		ObservedAt:    time.Now().UTC().Format(time.RFC3339),
		GoVersion:     runtime.Version(),
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		Results:       make([]scenarioResult, 0, 4),
		Limitations: []string{
			"The fixture ledger is an in-process demonstration authority, not a production application database.",
			"Owner scope is host configured; the local parent process models a trusted broker and derives browser generation from Chrome process identity plus Scout's live browser fingerprint.",
			"The handoff policy binding is checked against live Scout session status; this local demonstration does not use an independently deployed broker.",
			"The agent-browser target and URL observation is unauthenticated and does not include frame identity.",
			"One local run is not a reliability estimate or comparative competitor result.",
		},
	}
	var mu sync.Mutex
	accepted := 0
	acceptedByTask := make(map[string]int)
	acceptedByKey := make(map[string]int)
	effectCountByKey := make(map[string]int)
	keyAttemptCount := make(map[string]int)
	mutationStarted := make(chan struct{}, 1)
	zeroEffectRequestStarted := make(chan struct{}, 1)
	releaseZeroEffect := make(chan struct{})
	browserRestartMutationStarted := make(chan struct{}, 1)
	checkpointStarted := make(chan struct{}, 1)
	releaseMutation := make(chan struct{})
	releaseCheckpoint := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, `<!doctype html><title>Task recovery</title>
<button id="submit" onclick="fetch('/submit?task='+encodeURIComponent(new URLSearchParams(location.search).get('task')||'')+'&key='+encodeURIComponent(new URLSearchParams(location.search).get('key')||''),{method:'POST'}).then(r=>r.text()).then(n=>document.querySelector('#status').textContent='Accepted: '+n)">Submit once</button>
<p id="status">Ready</p>`)
		case "/submit":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			taskOperationID := strings.TrimSpace(r.URL.Query().Get("task"))
			if taskOperationID == "" {
				http.Error(w, "task operation ID is required", http.StatusBadRequest)
				return
			}
			applicationKey := strings.TrimSpace(r.URL.Query().Get("key"))
			if applicationKey == "" {
				applicationKey = taskOperationID
			}
			mu.Lock()
			keyAttemptCount[applicationKey]++
			if applicationKey == "zero-effect-application-key" && keyAttemptCount[applicationKey] == 1 {
				mu.Unlock()
				select {
				case zeroEffectRequestStarted <- struct{}{}:
				default:
				}
				select {
				case <-releaseZeroEffect:
				case <-r.Context().Done():
				}
				http.Error(w, "transient failure before application commit", http.StatusServiceUnavailable)
				return
			}
			if existing := acceptedByKey[applicationKey]; existing > 0 {
				mu.Unlock()
				fmt.Fprint(w, existing)
				return
			}
			accepted++
			count := accepted
			acceptedByTask[taskOperationID]++
			acceptedByKey[applicationKey] = count
			effectCountByKey[applicationKey]++
			mu.Unlock()
			select {
			case mutationStarted <- struct{}{}:
			default:
			}
			if count == 4 {
				select {
				case browserRestartMutationStarted <- struct{}{}:
				default:
				}
			}
			if count == 1 {
				<-releaseMutation
			}
			fmt.Fprint(w, count)
		case "/ledger":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			mu.Lock()
			taskOperationID := strings.TrimSpace(r.URL.Query().Get("task"))
			applicationKey := strings.TrimSpace(r.URL.Query().Get("key"))
			if applicationKey == "" {
				applicationKey = taskOperationID
			}
			count := effectCountByKey[applicationKey]
			taskCount := acceptedByTask[taskOperationID]
			total := accepted
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Accepted       int    `json:"accepted"`
				TaskAccepted   int    `json:"task_accepted"`
				Total          int    `json:"total"`
				TaskID         string `json:"task_id"`
				ApplicationKey string `json:"application_key"`
			}{Accepted: count, TaskAccepted: taskCount, Total: total, TaskID: taskOperationID, ApplicationKey: applicationKey})
		case "/checkpoint":
			checkpointStarted <- struct{}{}
			<-releaseCheckpoint
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	defer close(releaseMutation)
	defer close(releaseCheckpoint)
	defer close(releaseZeroEffect)

	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return fmt.Errorf("launch Chrome: %w", err)
	}
	defer browser.Close()

	tempDir, err := os.MkdirTemp("", "scout-task-recovery-*")
	if err != nil {
		return fmt.Errorf("create task store directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	storePath := filepath.Join(tempDir, "task-runs.db")
	devToolsURL := browser.DevToolsURL()
	prepared, err := runChildProcess(ctx, "prepare", browser.WSEndpoint(), fixture.URL, storePath, "")
	if err != nil {
		return fmt.Errorf("prepare browser target: %w", err)
	}
	if prepared.TargetID == "" || prepared.BrowserInstanceID == "" || prepared.PolicyID == "" {
		return fmt.Errorf("prepare process returned incomplete live browser or policy state: %+v", prepared)
	}
	interrupted, err := startInterruptedChild(ctx, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID, mutationStarted)
	if err != nil {
		return err
	}
	if !interrupted.InFlight || interrupted.RecoveryPhase != "actions" || interrupted.CursorStatus != "started" {
		return fmt.Errorf("interrupted task record has unexpected recovery cursor: %+v", interrupted)
	}
	// Sign a short-lived handoff in the parent and verify it in a fresh child
	// process. The host-issued browser generation is intentionally modeled as a
	// broker token, separate from Scout's local browser fingerprint.
	handoffStore, err := agent.OpenBoltTaskRunStore(storePath)
	if err != nil {
		return fmt.Errorf("open task store for handoff export: %w", err)
	}
	record, found, err := handoffStore.GetTaskRun(ctx, taskID)
	if err != nil || !found {
		_ = handoffStore.Close()
		return fmt.Errorf("load interrupted task for handoff: found=%t err=%v", found, err)
	}
	receipt, err := agent.ExportTaskRunRecoveryReceipt(record, 1)
	if closeErr := handoffStore.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("export interrupted action receipt: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("create handoff signing key: %w", err)
	}
	now := time.Now().UTC()
	ownerScope := "demo-tenant/task-recovery" // host configured principal scope
	browserGeneration, err := localBrowserGeneration(browser.ProcessID(), prepared.BrowserInstanceID)
	if err != nil {
		return err
	}
	policyDigest, err := agent.RecoveryReceiptPolicyDigest(prepared.PolicyID)
	if err != nil {
		return err
	}
	if receipt.Policy == nil || receipt.Policy.ID != prepared.PolicyID || receipt.Policy.Digest != policyDigest {
		return fmt.Errorf("live Scout policy does not match the interrupted task receipt")
	}
	envelope, err := agent.NewRecoveryHandoffEnvelope(receipt, ownerScope, browserGeneration, "demo-ephemeral-v1", key, now, now.Add(time.Minute))
	if err != nil {
		return fmt.Errorf("sign recovery handoff: %w", err)
	}
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode recovery handoff: %w", err)
	}
	handoffPath := filepath.Join(tempDir, "handoff.json")
	keyPath := filepath.Join(tempDir, "handoff.key")
	if err := os.WriteFile(handoffPath, envelopeBytes, 0600); err != nil {
		return fmt.Errorf("write recovery handoff: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return fmt.Errorf("write handoff test key: %w", err)
	}
	peerObservation, err := observeWithAgentBrowser(ctx, devToolsURL, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("observe live target with agent-browser: %w", err)
	}
	peerObservationPath := filepath.Join(tempDir, "peer-observation.json")
	if err := os.WriteFile(peerObservationPath, peerObservation, 0600); err != nil {
		return fmt.Errorf("write peer observation: %w", err)
	}
	// A separate Node.js process verifies the published JSON/HMAC contract. It
	// reads target ID and URL directly from agent-browser; the trusted local
	// broker signs owner, live process/browser generation, and live policy state.
	initialBinding := agent.RecoveryHandoffObservedBinding{
		OwnerScope: ownerScope, BrowserGeneration: browserGeneration,
		TargetID: prepared.TargetID, FrameBindingKnown: false,
		PolicyID: prepared.PolicyID, PolicyDigest: policyDigest,
	}
	observationPublicKey, observationPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("create receiver observation key: %w", err)
	}
	observationNow := time.Now().UTC()
	observationEnvelope, err := agent.NewRecoveryHandoffObservation(initialBinding, "demo-observation-v1", observationPrivateKey, observationNow, observationNow.Add(time.Minute))
	if err != nil {
		return fmt.Errorf("sign receiver observation: %w", err)
	}
	observationBytes, err := json.Marshal(observationEnvelope)
	if err != nil {
		return fmt.Errorf("encode signed receiver observation: %w", err)
	}
	initialBindingPath := filepath.Join(tempDir, "receiver-observation.json")
	if err := os.WriteFile(initialBindingPath, observationBytes, 0600); err != nil {
		return fmt.Errorf("write signed receiver observation: %w", err)
	}
	observationPublicDER, err := x509.MarshalPKIXPublicKey(observationPublicKey)
	if err != nil {
		return fmt.Errorf("encode receiver observation public key: %w", err)
	}
	observationPublicKeyPath := filepath.Join(tempDir, "receiver-observation.pub")
	if err := os.WriteFile(observationPublicKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: observationPublicDER}), 0600); err != nil {
		return fmt.Errorf("write receiver observation public key: %w", err)
	}
	nodeResult, err := runNodeHandoffReceiver(ctx, handoffPath, keyPath, ownerScope, initialBindingPath, observationPublicKeyPath, peerObservationPath, fixtureTaskURL(fixture.URL, taskID))
	if err != nil {
		return fmt.Errorf("verify handoff in independent Node.js receiver: %w", err)
	}
	if !nodeResult.SameOwner || !nodeResult.SameBrowser || !nodeResult.SameTarget || !nodeResult.PeerTargetMatches || !nodeResult.PeerURLMatches || !nodeResult.PeerObservationMatches || nodeResult.SameFrame || !nodeResult.SamePolicy || nodeResult.SameContext || nodeResult.ReplayAllowed {
		return fmt.Errorf("independent receiver did not fail closed on unavailable frame identity: %+v", nodeResult)
	}
	if err := verifyNodeReceiverRejectsTamperedEnvelope(ctx, keyPath, ownerScope, initialBindingPath, observationPublicKeyPath, peerObservationPath, fixtureTaskURL(fixture.URL, taskID), envelope, tempDir); err != nil {
		return fmt.Errorf("independent receiver accepted a modified handoff: %w", err)
	}
	report.Handoff.NodeOwnerMatched = nodeResult.SameOwner
	report.Handoff.NodeBrowserMatched = nodeResult.SameBrowser
	report.Handoff.NodeTargetMatched = nodeResult.SameTarget && nodeResult.PeerObservationMatches
	report.Handoff.NodePolicyMatched = nodeResult.SamePolicy
	report.Handoff.NodeFrameBindingAvailable = nodeResult.SameFrame
	report.Handoff.NodeFullContextMatched = nodeResult.SameContext
	report.Handoff.TamperedEnvelopeRejected = true
	report.Handoff.ReplayAllowed = nodeResult.ReplayAllowed
	report.Handoff.GenerationDerivedFromLiveProcess = true
	report.Handoff.PolicyReadFromLiveSession = true
	fmt.Fprintf(os.Stderr, "Signed recovery handoff verified by independent Node.js receiver; owner/browser/target/policy matched; frame_unknown=true; same_context=%t; replay_allowed=%t\n", nodeResult.SameContext, nodeResult.ReplayAllowed)
	handoffCheck, err := runChildProcessWithEnv(ctx, "handoff-check", taskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID,
		"SCOUT_RECOVERY_HANDOFF="+handoffPath, "SCOUT_RECOVERY_HANDOFF_KEY="+keyPath, "SCOUT_RECOVERY_HANDOFF_OWNER="+ownerScope,
		"SCOUT_RECOVERY_PEER_OBSERVATION="+peerObservationPath, fmt.Sprintf("SCOUT_RECOVERY_BROWSER_PID=%d", browser.ProcessID()))
	if err != nil {
		return fmt.Errorf("verify handoff in receiving process: %w", err)
	}
	if !handoffCheck.HandoffMatched || !handoffCheck.HandoffReplaySafe {
		return fmt.Errorf("receiver did not bind handoff safely: %+v", handoffCheck)
	}
	report.Handoff.SeparateScoutContextMatched = handoffCheck.HandoffMatched
	fmt.Fprintf(os.Stderr, "Signed recovery handoff verified in a separate Scout process using agent-browser target observation; same_context=%t; replay_allowed=%t; host reconciliation still required\n", handoffCheck.HandoffMatched, !handoffCheck.HandoffReplaySafe)
	retryCheck, err := runChildProcess(ctx, "retry-check", browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("check interrupted-task retry guard: %w", err)
	}
	if !retryCheck.RetryBlocked || !retryCheck.InFlight {
		return fmt.Errorf("interrupted task was not blocked from replay: %+v", retryCheck)
	}
	mu.Lock()
	count := accepted
	mu.Unlock()
	if count != 1 {
		return fmt.Errorf("retry guard caused %d server mutations; want exactly 1", count)
	}
	resolved, err := runChildProcess(ctx, "resolve", browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("reconcile interrupted task: %w", err)
	}
	if resolved.ResolutionStatus != "verified_complete" || resolved.ResolutionSource != "durable_task_recovery_fixture_ledger" || resolved.AuthoritativeCount != 1 || resolved.InFlight {
		return fmt.Errorf("interrupted task resolution was not retained: %+v", resolved)
	}
	mu.Lock()
	count = accepted
	mu.Unlock()
	if count != 1 {
		return fmt.Errorf("interrupted task side effect ran %d times; want exactly once", count)
	}
	report.Results = append(report.Results, scenarioResult{
		TaskID: taskID, Boundary: "mutation committed; response held; Scout child interrupted",
		RecoveryPhase: interrupted.RecoveryPhase, CursorStatus: interrupted.CursorStatus,
		SameIDRetryBlocked: retryCheck.RetryBlocked, HostResolution: resolved.ResolutionStatus,
		EvidenceSource: resolved.ResolutionSource, AcceptedForTask: resolved.AuthoritativeCount, TotalAccepted: count,
		EvidenceReference: resolved.ResolutionReference, EvidenceObservedAt: resolved.ResolutionObservedAt,
	})
	fmt.Fprintf(os.Stderr, "Interrupted task %q after server accepted its mutation; durable cursor=%s/%s; retry_blocked=%t; host resolution=%s; task_ledger_count=%d; total_accepted=%d\n", taskID, interrupted.RecoveryPhase, interrupted.CursorStatus, retryCheck.RetryBlocked, resolved.ResolutionStatus, resolved.AuthoritativeCount, count)

	// The fixture checkpoint runs after the final page postcondition is verified
	// and result_persisting is durable, but before the store transitions the task
	// to a completed result. Killing here covers the last Scout process boundary.
	resultTaskID := taskID + "-result-persisting"
	resultInterrupted, err := startChildKilledAtSignal(ctx, "result-persisting", resultTaskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID, fixture.URL+"/checkpoint", checkpointStarted)
	if err != nil {
		return fmt.Errorf("interrupt result persistence: %w", err)
	}
	if resultInterrupted.RecoveryPhase != "result_persisting" || resultInterrupted.CursorStatus != "finished" || resultInterrupted.NextAction != 1 || !resultInterrupted.InFlight {
		return fmt.Errorf("result-persistence crash has unexpected recovery record: %+v", resultInterrupted)
	}
	resultRetry, err := runChildProcessForTask(ctx, "retry-check", resultTaskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("check result-persistence retry guard: %w", err)
	}
	if !resultRetry.RetryBlocked || !resultRetry.InFlight {
		return fmt.Errorf("result-persistence task was not blocked from replay: %+v", resultRetry)
	}
	mu.Lock()
	count = accepted
	mu.Unlock()
	if count != 2 {
		return fmt.Errorf("result-persistence retry guard caused %d mutations; want exactly 2", count)
	}
	resultResolved, err := runChildProcessForTask(ctx, "resolve", resultTaskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("reconcile result-persistence task: %w", err)
	}
	if resultResolved.ResolutionStatus != "verified_complete" || resultResolved.ResolutionSource != "durable_task_recovery_fixture_ledger" || resultResolved.AuthoritativeCount != 1 {
		return fmt.Errorf("result-persistence resolution was not retained: %+v", resultResolved)
	}
	report.Results = append(report.Results, scenarioResult{
		TaskID: resultTaskID, Boundary: "final page verification complete; result persistence interrupted",
		RecoveryPhase: resultInterrupted.RecoveryPhase, CursorStatus: resultInterrupted.CursorStatus,
		SameIDRetryBlocked: resultRetry.RetryBlocked, HostResolution: resultResolved.ResolutionStatus,
		EvidenceSource: resultResolved.ResolutionSource, AcceptedForTask: resultResolved.AuthoritativeCount, TotalAccepted: count,
		EvidenceReference: resultResolved.ResolutionReference, EvidenceObservedAt: resultResolved.ResolutionObservedAt,
	})
	fmt.Fprintf(os.Stderr, "Interrupted task %q after final page verification and before result commit at durable cursor=%s/%s; retry_blocked=%t; host resolution=%s; task_ledger_count=%d; total_accepted=%d\n", resultTaskID, resultInterrupted.RecoveryPhase, resultInterrupted.CursorStatus, resultRetry.RetryBlocked, resultResolved.ResolutionStatus, resultResolved.AuthoritativeCount, count)

	// Use an independent task ID to demonstrate the normal completed-result
	// retry contract without reusing the deliberately interrupted reservation.
	first, err := runChildProcess(ctx, "first", browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("first completed Scout process: %w", err)
	}
	if first.TargetID == "" || first.Status != "completed" || first.IdempotencyReplay {
		return fmt.Errorf("first Scout process returned an invalid result: %+v", first)
	}
	retried, err := runChildProcess(ctx, "retry", browser.WSEndpoint(), fixture.URL, storePath, first.TargetID)
	if err != nil {
		return fmt.Errorf("restarted Scout process: %w", err)
	}
	mu.Lock()
	count = accepted
	mu.Unlock()
	if count != 3 {
		return fmt.Errorf("fixture observed %d total mutations; want two interrupted + one completed = 3", count)
	}
	if first.PlanID != retried.PlanID || first.Status != retried.Status || first.TargetID != retried.TargetID || !retried.IdempotencyReplay {
		return fmt.Errorf("retry returned a different result: first=%+v retry=%+v", first, retried)
	}
	report.Results = append(report.Results, scenarioResult{
		TaskID: taskID + "-completed", Boundary: "completed result reused by a fresh Scout process",
		SameIDRetryBlocked: false, AcceptedForTask: 1, TotalAccepted: count,
		CompletedResultReused: retried.IdempotencyReplay,
	})
	fmt.Fprintf(os.Stderr, "Completed task retry after Scout process restart; accepted=%d status=%s replay=%t target=%s plan=%s\n", count, retried.Status, retried.IdempotencyReplay, retried.TargetID, retried.PlanID)

	// A browser crash is a distinct boundary from a Scout worker crash. Keep the
	// durable store and application fixture alive, but replace Chrome entirely.
	browserRestartTaskID := taskID + "-browser-restart"
	browserRestartInterrupted, err := startChildKilledAtSignal(ctx, "interrupted-browser-restart", browserRestartTaskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID, "", browserRestartMutationStarted)
	if err != nil {
		return fmt.Errorf("interrupt task before browser restart: %w", err)
	}
	if !browserRestartInterrupted.InFlight || browserRestartInterrupted.RecoveryPhase != "actions" || browserRestartInterrupted.CursorStatus != "started" {
		return fmt.Errorf("browser-restart task has unexpected recovery cursor: %+v", browserRestartInterrupted)
	}
	// Close deliberately kills Chrome; os/exec reports the expected signal exit.
	_ = browser.Close()
	browser, err = launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return fmt.Errorf("launch replacement browser: %w", err)
	}
	defer browser.Close()
	replacementHandoff, err := runChildProcessWithEnv(ctx, "handoff-check-replacement", taskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID,
		"SCOUT_RECOVERY_HANDOFF="+handoffPath, "SCOUT_RECOVERY_HANDOFF_KEY="+keyPath, "SCOUT_RECOVERY_HANDOFF_OWNER="+ownerScope,
		fmt.Sprintf("SCOUT_RECOVERY_BROWSER_PID=%d", browser.ProcessID()))
	if err != nil {
		return fmt.Errorf("check handoff against replacement browser: %w", err)
	}
	if replacementHandoff.HandoffMatched || !replacementHandoff.HandoffReplaySafe {
		return fmt.Errorf("replacement browser incorrectly matched old handoff or allowed replay: %+v", replacementHandoff)
	}
	report.Handoff.ReplacementBrowserMatched = replacementHandoff.HandoffMatched
	fmt.Fprintf(os.Stderr, "Replacement browser rejected old page binding; same_context=%t; replay_allowed=%t; host ledger reconciliation remains available\n", replacementHandoff.HandoffMatched, !replacementHandoff.HandoffReplaySafe)
	restartedRetry, err := runChildProcessForTask(ctx, "retry-check-after-browser-restart", browserRestartTaskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("check replay guard after browser restart: %w", err)
	}
	if !restartedRetry.RetryBlocked || !restartedRetry.InFlight {
		return fmt.Errorf("browser-restart task was not blocked from replay: %+v", restartedRetry)
	}
	mu.Lock()
	count = accepted
	mu.Unlock()
	if count != 4 {
		return fmt.Errorf("browser restart retry guard caused %d mutations; want exactly 4", count)
	}
	restartedResolved, err := runChildProcessForTask(ctx, "resolve-after-browser-restart", browserRestartTaskID, browser.WSEndpoint(), fixture.URL, storePath, prepared.TargetID)
	if err != nil {
		return fmt.Errorf("reconcile task after browser restart: %w", err)
	}
	if restartedResolved.ResolutionStatus != "verified_complete" || restartedResolved.ResolutionSource != "durable_task_recovery_fixture_ledger" || restartedResolved.AuthoritativeCount != 1 || restartedResolved.InFlight {
		return fmt.Errorf("browser-restart task resolution was not retained: %+v", restartedResolved)
	}
	report.Results = append(report.Results, scenarioResult{
		TaskID: browserRestartTaskID, Boundary: "mutation committed; browser replaced",
		RecoveryPhase: browserRestartInterrupted.RecoveryPhase, CursorStatus: browserRestartInterrupted.CursorStatus,
		SameIDRetryBlocked: restartedRetry.RetryBlocked, HostResolution: restartedResolved.ResolutionStatus,
		EvidenceSource: restartedResolved.ResolutionSource, AcceptedForTask: restartedResolved.AuthoritativeCount, TotalAccepted: count,
		EvidenceReference: restartedResolved.ResolutionReference, EvidenceObservedAt: restartedResolved.ResolutionObservedAt,
	})
	fmt.Fprintf(os.Stderr, "Browser process restart after committed mutation; old_target=%s new_browser=%t; retry_blocked=%t; host_resolution=%s; task_ledger_count=%d; total_accepted=%d\n", prepared.TargetID, true, restartedRetry.RetryBlocked, restartedResolved.ResolutionStatus, restartedResolved.AuthoritativeCount, count)

	// Exercise the other host-recovery branch: the first request reaches the
	// application boundary but commits no effect. A fresh process confirms zero
	// in the authoritative ledger, resolves the original Scout task as rejected,
	// then executes a new Scout task with the same application idempotency key.
	zeroEffectTaskID := taskID + "-zero-effect-first"
	zeroEffectAppKey := "zero-effect-application-key"
	zeroEffectInterrupted, err := startChildKilledAtSignal(ctx, "zero-effect-interrupted", zeroEffectTaskID, browser.WSEndpoint(), fixture.URL, storePath, "", "", zeroEffectRequestStarted,
		"SCOUT_RECOVERY_APP_KEY="+zeroEffectAppKey)
	if err != nil {
		return fmt.Errorf("interrupt zero-effect task: %w", err)
	}
	if !zeroEffectInterrupted.InFlight || zeroEffectInterrupted.CursorStatus != "started" {
		return fmt.Errorf("zero-effect task did not retain an interrupted dispatch: %+v", zeroEffectInterrupted)
	}
	zeroLedger, err := readFixtureLedgerForKey(ctx, fixture.URL, zeroEffectTaskID, zeroEffectAppKey)
	if err != nil || zeroLedger.Accepted != 0 {
		return fmt.Errorf("authoritative ledger before retry = %+v, err=%v; want zero committed effects", zeroLedger, err)
	}
	zeroResolved, err := runChildProcessWithEnv(ctx, "resolve-zero-effect", zeroEffectTaskID, browser.WSEndpoint(), fixture.URL, storePath, "",
		"SCOUT_RECOVERY_APP_KEY="+zeroEffectAppKey)
	if err != nil {
		return fmt.Errorf("reconcile zero-effect task: %w", err)
	}
	if zeroResolved.ResolutionStatus != "rejected" || zeroResolved.AuthoritativeCount != 0 || zeroResolved.InFlight {
		return fmt.Errorf("zero-effect resolution was not retained: %+v", zeroResolved)
	}
	zeroSameIDRetry, err := runChildProcessForTask(ctx, "retry-check", zeroEffectTaskID, browser.WSEndpoint(), fixture.URL, storePath, "")
	if err != nil || !zeroSameIDRetry.RetryBlocked {
		return fmt.Errorf("resolved zero-effect Scout task ID was not kept reserved: %+v err=%v", zeroSameIDRetry, err)
	}
	zeroFollowupTaskID := taskID + "-zero-effect-followup"
	zeroFollowup, err := runChildProcessWithEnv(ctx, "retry-zero-effect", zeroFollowupTaskID, browser.WSEndpoint(), fixture.URL, storePath, "",
		"SCOUT_RECOVERY_APP_KEY="+zeroEffectAppKey, "SCOUT_RECOVERY_EXPECTED_TEXT=Accepted: 5")
	if err != nil {
		return fmt.Errorf("run host-authorized follow-up task: %w", err)
	}
	if zeroFollowup.Status != "completed" || zeroFollowup.PlanID == "" {
		return fmt.Errorf("host-authorized follow-up did not complete with a task identity: %+v", zeroFollowup)
	}
	zeroDuplicateTaskID := taskID + "-zero-effect-duplicate"
	zeroDuplicate, err := runChildProcessWithEnv(ctx, "retry-zero-effect", zeroDuplicateTaskID, browser.WSEndpoint(), fixture.URL, storePath, "",
		"SCOUT_RECOVERY_APP_KEY="+zeroEffectAppKey, "SCOUT_RECOVERY_EXPECTED_TEXT=Accepted: 5")
	if err != nil {
		return fmt.Errorf("verify application-key duplicate suppression: %w", err)
	}
	if zeroDuplicate.Status != "completed" || zeroDuplicate.IdempotencyReplay {
		return fmt.Errorf("duplicate-key delivery did not complete as a separate Scout task: %+v", zeroDuplicate)
	}
	zeroFinalLedger, err := readFixtureLedgerForKey(ctx, fixture.URL, zeroFollowupTaskID, zeroEffectAppKey)
	if err != nil || zeroFinalLedger.Accepted != 1 || zeroFinalLedger.TaskAccepted != 1 || zeroFinalLedger.Total != 5 {
		return fmt.Errorf("application idempotency ledger after follow-up = %+v, err=%v; want one effect under one new task", zeroFinalLedger, err)
	}
	report.Results = append(report.Results, scenarioResult{
		TaskID: zeroEffectTaskID, FollowUpTaskID: zeroFollowupTaskID, DuplicateTaskID: zeroDuplicateTaskID, ApplicationKey: zeroEffectAppKey,
		ApplicationDuplicateSuppressed: true,
		Boundary:                       "request reached application; zero-effect ledger confirmed; new Scout task reused application idempotency key",
		RecoveryPhase:                  zeroEffectInterrupted.RecoveryPhase, CursorStatus: zeroEffectInterrupted.CursorStatus,
		SameIDRetryBlocked: zeroSameIDRetry.RetryBlocked, HostResolution: zeroResolved.ResolutionStatus,
		EvidenceSource: zeroResolved.ResolutionSource, EvidenceReference: zeroResolved.ResolutionReference,
		EvidenceObservedAt: zeroResolved.ResolutionObservedAt, AcceptedForTask: zeroFinalLedger.Accepted, TotalAccepted: zeroFinalLedger.Total,
	})
	fmt.Fprintf(os.Stderr, "Zero-effect recovery confirmed by ledger; original_task=%s blocked=%t; followup_task=%s duplicate_task=%s application_key=%s effects=%d total_effects=%d\n", zeroEffectTaskID, zeroSameIDRetry.RetryBlocked, zeroFollowupTaskID, zeroDuplicateTaskID, zeroEffectAppKey, zeroFinalLedger.Accepted, zeroFinalLedger.Total)
	reportBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode recovery report: %w", err)
	}
	reportBytes = append(reportBytes, '\n')
	if _, err := os.Stdout.Write(reportBytes); err != nil {
		return fmt.Errorf("write recovery report: %w", err)
	}
	if reportPath := os.Getenv("SCOUT_RECOVERY_REPORT_PATH"); reportPath != "" {
		if err := os.MkdirAll(filepath.Dir(reportPath), 0755); err != nil {
			return fmt.Errorf("create report directory: %w", err)
		}
		file, err := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return fmt.Errorf("create report %q: %w", reportPath, err)
		}
		if _, err := io.Copy(file, bytes.NewReader(reportBytes)); err != nil {
			_ = file.Close()
			return fmt.Errorf("write report %q: %w", reportPath, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close report %q: %w", reportPath, err)
		}
	}
	return nil
}

func runChildProcess(ctx context.Context, phase, cdpURL, origin, storePath, targetID string) (childResult, error) {
	return runChildProcessForTask(ctx, phase, taskID, cdpURL, origin, storePath, targetID)
}

func runChildProcessForTask(ctx context.Context, phase, childTaskID, cdpURL, origin, storePath, targetID string) (childResult, error) {
	return runChildProcessWithEnv(ctx, phase, childTaskID, cdpURL, origin, storePath, targetID)
}

func runChildProcessWithEnv(ctx context.Context, phase, childTaskID, cdpURL, origin, storePath, targetID string, extraEnv ...string) (childResult, error) {
	executable, err := os.Executable()
	if err != nil {
		return childResult{}, fmt.Errorf("find example executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = append(os.Environ(),
		"SCOUT_RECOVERY_PHASE="+phase,
		"SCOUT_RECOVERY_CDP="+cdpURL,
		"SCOUT_RECOVERY_ORIGIN="+origin,
		"SCOUT_RECOVERY_STORE="+storePath,
		"SCOUT_RECOVERY_TARGET="+targetID,
		"SCOUT_RECOVERY_TASK_ID="+childTaskID,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return childResult{}, fmt.Errorf("%w: %s", err, output)
	}
	var result childResult
	if err := json.Unmarshal(output, &result); err != nil {
		return childResult{}, fmt.Errorf("decode child result: %w: %s", err, output)
	}
	return result, nil
}

type agentBrowserTab struct {
	TargetID string `json:"targetId"`
	URL      string `json:"url"`
	Type     string `json:"type"`
}

type agentBrowserTabList struct {
	Success bool `json:"success"`
	Data    struct {
		Tabs []agentBrowserTab `json:"tabs"`
	} `json:"data"`
	Error json.RawMessage `json:"error"`
}

type nodeHandoffResult struct {
	SameOwner              bool `json:"same_owner"`
	SameBrowser            bool `json:"same_browser"`
	SameTarget             bool `json:"same_target"`
	PeerTargetMatches      bool `json:"peer_target_matches"`
	PeerURLMatches         bool `json:"peer_url_matches"`
	PeerObservationMatches bool `json:"peer_observation_matches"`
	SameFrame              bool `json:"same_frame"`
	SamePolicy             bool `json:"same_policy"`
	SameContext            bool `json:"same_context"`
	ReplayAllowed          bool `json:"replay_allowed"`
}

func runNodeHandoffReceiver(ctx context.Context, envelopePath, keyPath, owner, observationPath, observationPublicKeyPath, peerObservationPath, expectedURL string) (nodeHandoffResult, error) {
	command := exec.CommandContext(ctx, "node", filepath.Join("examples", "durable_task_recovery", "receiver.mjs"), envelopePath, keyPath, owner, observationPath, observationPublicKeyPath, peerObservationPath, expectedURL)
	command.Dir = "."
	output, err := command.CombinedOutput()
	if err != nil {
		return nodeHandoffResult{}, fmt.Errorf("node receiver: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var result nodeHandoffResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nodeHandoffResult{}, fmt.Errorf("decode node receiver result: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return result, nil
}

func verifyNodeReceiverRejectsTamperedEnvelope(ctx context.Context, keyPath, owner, observationPath, observationPublicKeyPath, peerObservationPath, expectedURL string, envelope agent.RecoveryHandoffEnvelope, tempDir string) error {
	envelope.Receipt.Operation.Name = "tampered-operation"
	tampered, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	tamperedPath := filepath.Join(tempDir, "tampered-handoff.json")
	if err := os.WriteFile(tamperedPath, tampered, 0600); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "node", filepath.Join("examples", "durable_task_recovery", "receiver.mjs"), tamperedPath, keyPath, owner, observationPath, observationPublicKeyPath, peerObservationPath, expectedURL)
	command.Dir = "."
	if output, err := command.CombinedOutput(); err == nil {
		return fmt.Errorf("tampered envelope verified: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func observeWithAgentBrowser(ctx context.Context, devToolsURL, targetID string) ([]byte, error) {
	bin := os.Getenv("SCOUT_AGENT_BROWSER_BIN")
	if bin == "" {
		bin = "npx"
	}
	version := os.Getenv("SCOUT_AGENT_BROWSER_VERSION")
	if version == "" {
		version = "0.41.2"
	}
	args := []string{"--yes", "agent-browser@" + version, "--cdp", devToolsURL, "--pin-tab", "--json", "tab", "list"}
	if bin != "npx" {
		args = []string{"--cdp", devToolsURL, "--pin-tab", "--json", "tab", "list"}
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // configurable automation CLI path
	output, err := cmd.Output()
	if err != nil {
		if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
			return nil, fmt.Errorf("agent-browser tab list failed: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	var result agentBrowserTabList
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("decode agent-browser tab list: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if !result.Success || len(result.Data.Tabs) == 0 {
		return nil, fmt.Errorf("agent-browser returned no tab observations: %s", strings.TrimSpace(string(output)))
	}
	for _, tab := range result.Data.Tabs {
		if tab.TargetID == targetID && tab.Type == "page" {
			if !strings.HasPrefix(tab.URL, "http://") && !strings.HasPrefix(tab.URL, "https://") {
				return nil, fmt.Errorf("agent-browser target URL is not a web page")
			}
			return json.Marshal(agentBrowserObservation{TargetID: tab.TargetID, URL: tab.URL})
		}
	}
	return nil, fmt.Errorf("agent-browser did not observe expected page target %q", targetID)
}

type agentBrowserObservation struct {
	TargetID string `json:"target_id"`
	URL      string `json:"url"`
}

func runChild(ctx context.Context) (retErr error) {
	phase := os.Getenv("SCOUT_RECOVERY_PHASE")
	childTaskID := os.Getenv("SCOUT_RECOVERY_TASK_ID")
	if childTaskID == "" {
		childTaskID = taskID
	}
	cdpURL := os.Getenv("SCOUT_RECOVERY_CDP")
	origin := os.Getenv("SCOUT_RECOVERY_ORIGIN")
	storePath := os.Getenv("SCOUT_RECOVERY_STORE")
	targetID := os.Getenv("SCOUT_RECOVERY_TARGET")
	if cdpURL == "" || origin == "" || storePath == "" {
		return fmt.Errorf("recovery child environment is incomplete")
	}
	store, err := agent.OpenBoltTaskRunStore(storePath)
	if err != nil {
		return fmt.Errorf("open task store: %w", err)
	}
	var taskStore agent.TaskRunStore = store
	if phase == "result-persisting" {
		taskStore = checkpointTaskRunStore{TaskRunStore: store, progress: store, checkpointURL: os.Getenv("SCOUT_RECOVERY_CHECKPOINT_URL")}
	}
	defer func() {
		if err := store.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close task store: %w", err)
		}
	}()

	configuredTargetID := targetID
	if phase == "retry-check-after-browser-restart" || phase == "resolve-after-browser-restart" || phase == "handoff-check-replacement" {
		configuredTargetID = ""
	}
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		RemoteCDP:       cdpURL,
		TargetID:        configuredTargetID,
		Timeout:         20 * time.Second,
		AllowPrivateIPs: true,
		AllowedOrigins:  []string{origin},
		TaskRunStore:    taskStore,
	})
	if err != nil {
		return fmt.Errorf("connect Scout session: %w", err)
	}
	defer func() {
		if err := session.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close Scout session: %w", err)
		}
	}()

	if phase == "first" || phase == "prepare" || phase == "interrupted" || phase == "result-persisting" || phase == "interrupted-browser-restart" || phase == "zero-effect-interrupted" || phase == "retry-zero-effect" {
		pageTaskID := childTaskID
		if phase == "first" {
			pageTaskID = taskID + "-completed"
		}
		pageURL := fixtureTaskURL(origin, pageTaskID)
		if applicationKey := os.Getenv("SCOUT_RECOVERY_APP_KEY"); applicationKey != "" {
			pageURL = fixtureTaskURLWithKey(origin, pageTaskID, applicationKey)
		}
		if _, err := session.NavigateWithPostconditionContext(ctx, pageURL, agent.Postcondition{Text: "Ready"}); err != nil {
			return fmt.Errorf("open fixture page: %w", err)
		}
		page, err := session.ObserveContext(ctx)
		if err != nil {
			return fmt.Errorf("identify browser target: %w", err)
		}
		targetID = page.TargetID
	}
	if phase == "prepare" {
		status, err := session.StatusContext(ctx)
		if err != nil {
			return fmt.Errorf("read live browser and policy state: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(childResult{TargetID: targetID, BrowserInstanceID: status.BrowserInstanceID, PolicyID: status.PolicyID})
	}
	if phase == "handoff-check" || phase == "handoff-check-replacement" {
		envelopeBytes, err := os.ReadFile(os.Getenv("SCOUT_RECOVERY_HANDOFF"))
		if err != nil {
			return fmt.Errorf("read recovery handoff: %w", err)
		}
		envelope, err := agent.DecodeRecoveryHandoffEnvelope(envelopeBytes)
		if err != nil {
			return err
		}
		key, err := os.ReadFile(os.Getenv("SCOUT_RECOVERY_HANDOFF_KEY"))
		if err != nil {
			return fmt.Errorf("read handoff verification key: %w", err)
		}
		status, err := session.StatusContext(ctx)
		if err != nil {
			return fmt.Errorf("observe receiving session status: %w", err)
		}
		observation, err := session.ObserveContext(ctx)
		if err != nil {
			return fmt.Errorf("observe receiving page: %w", err)
		}
		policyDigest, err := agent.RecoveryReceiptPolicyDigest(status.PolicyID)
		if err != nil {
			return err
		}
		pid, err := strconv.Atoi(os.Getenv("SCOUT_RECOVERY_BROWSER_PID"))
		if err != nil {
			return fmt.Errorf("trusted local broker process ID is missing: %w", err)
		}
		brokerGeneration, err := localBrowserGeneration(pid, status.BrowserInstanceID)
		if err != nil {
			return err
		}
		if phase == "handoff-check" && (brokerGeneration != envelope.BrowserGeneration || status.BrowserInstanceID != envelope.Receipt.Browser.InstanceID) {
			return fmt.Errorf("demo broker could not bind generation to the original browser context")
		}
		if phase == "handoff-check-replacement" && (brokerGeneration == envelope.BrowserGeneration || status.BrowserInstanceID == envelope.Receipt.Browser.InstanceID) {
			return fmt.Errorf("replacement browser unexpectedly reused the old generation or fingerprint")
		}
		if phase == "handoff-check" && (status.PolicyID != envelope.Receipt.Policy.ID || policyDigest != envelope.Receipt.Policy.Digest) {
			return fmt.Errorf("live Scout policy does not match the handoff receipt")
		}
		observed := agent.RecoveryHandoffObservedBinding{
			OwnerScope: os.Getenv("SCOUT_RECOVERY_HANDOFF_OWNER"), BrowserGeneration: brokerGeneration,
			TargetID: observation.TargetID, FrameID: observation.FrameID, FrameBindingKnown: true,
			PolicyID: status.PolicyID, PolicyDigest: policyDigest,
		}
		observedJSON, err := json.Marshal(observed)
		if err != nil {
			return fmt.Errorf("encode receiver binding observation: %w", err)
		}
		observed, err = agent.DecodeRecoveryHandoffObservedBinding(observedJSON)
		if err != nil {
			return err
		}
		if status.BrowserInstanceID == "" || status.ActiveTargetID != "" && status.ActiveTargetID != observation.TargetID {
			return fmt.Errorf("receiving session has no consistent live browser/target binding")
		}
		if peerObservationPath := os.Getenv("SCOUT_RECOVERY_PEER_OBSERVATION"); peerObservationPath != "" {
			peerBytes, err := os.ReadFile(peerObservationPath)
			if err != nil {
				return fmt.Errorf("read independent runner observation: %w", err)
			}
			var peer agentBrowserObservation
			if err := json.Unmarshal(peerBytes, &peer); err != nil {
				return fmt.Errorf("decode independent runner observation: %w", err)
			}
			if peer.TargetID != observation.TargetID || peer.URL != observation.URL {
				return fmt.Errorf("independent runner observation does not match Scout's live target and URL")
			}
		}
		comparison, err := agent.CompareRecoveryHandoffBinding(envelope, map[string][]byte{envelope.KeyID: key}, os.Getenv("SCOUT_RECOVERY_HANDOFF_OWNER"), time.Now(), observed)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(childResult{TargetID: observation.TargetID, HandoffMatched: comparison.SameContext, HandoffReplaySafe: !comparison.ReplayAllowed})
	}
	if phase == "inspect" || phase == "resolve" || phase == "resolve-zero-effect" || phase == "retry-check" || phase == "retry-check-after-browser-restart" || phase == "resolve-after-browser-restart" {
		record, found, err := session.GetTaskRunContext(ctx, childTaskID)
		if err != nil || !found {
			return fmt.Errorf("inspect interrupted task: found=%t err=%v", found, err)
		}
		if phase == "retry-check" || phase == "retry-check-after-browser-restart" {
			expectedText := "Accepted: 1"
			if phase == "retry-check-after-browser-restart" {
				expectedText = "Accepted: 4"
			}
			_, err := session.RunTaskContext(ctx, agent.TaskRunSpec{
				ID:             childTaskID,
				TargetID:       targetID,
				AllowedOrigins: []string{origin},
				Actions: []agent.BatchAction{{
					Action:       "click",
					Selector:     "#submit",
					ExpectedText: expectedText,
				}},
				FinalPostcondition: &agent.Postcondition{Text: expectedText},
			})
			var conflict *agent.TaskRunConflictError
			if !errors.As(err, &conflict) {
				return fmt.Errorf("retry returned %v; want a task conflict before browser dispatch", err)
			}
		} else if phase == "resolve" || phase == "resolve-after-browser-restart" || phase == "resolve-zero-effect" {
			expectedCount := 1
			appKey := os.Getenv("SCOUT_RECOVERY_APP_KEY")
			if appKey == "" {
				appKey = childTaskID
			}
			ledger, err := readFixtureLedgerForKey(ctx, origin, childTaskID, appKey)
			if err != nil {
				return fmt.Errorf("read authoritative fixture ledger: %w", err)
			}
			resolutionStatus := "verified_complete"
			if phase == "resolve-zero-effect" {
				expectedCount = 0
				resolutionStatus = "rejected"
			}
			if ledger.Accepted != expectedCount || ledger.TaskID != childTaskID || ledger.ApplicationKey != appKey {
				return fmt.Errorf("authoritative fixture ledger reports %d accepted effects for task %q and application key %q; want %d", ledger.Accepted, ledger.TaskID, ledger.ApplicationKey, expectedCount)
			}
			evidence := fmt.Sprintf("authoritative fixture ledger reports %d accepted effects for application operation %s", ledger.Accepted, appKey)
			recordedAt := time.Now().UTC().Format(time.RFC3339Nano)
			record, err = session.ResolveInterruptedTaskRunWithEvidenceContext(ctx, childTaskID, record.Fingerprint, agent.TaskRunResolution{
				Status: resolutionStatus, Evidence: evidence, EvidenceSource: "durable_task_recovery_fixture_ledger",
				EvidenceReference: fmt.Sprintf("/ledger?task=%s&key=%s&accepted=%d", url.QueryEscape(childTaskID), url.QueryEscape(appKey), ledger.Accepted), ObservedAt: recordedAt,
			})
			if err != nil {
				return fmt.Errorf("resolve interrupted task: %w", err)
			}
		}
		result := childResult{TargetID: record.TargetID, InFlight: record.InFlight}
		if record.Resolution != nil && (phase == "resolve" || phase == "resolve-after-browser-restart" || phase == "resolve-zero-effect") {
			ledger, err := readFixtureLedgerForKey(ctx, origin, childTaskID, os.Getenv("SCOUT_RECOVERY_APP_KEY"))
			if err != nil {
				return fmt.Errorf("confirm authoritative fixture ledger after resolution: %w", err)
			}
			result.AuthoritativeCount = ledger.Accepted
		}
		if record.Recovery != nil {
			result.RecoveryPhase = record.Recovery.Phase
			result.NextAction = record.Recovery.NextAction
			if record.Recovery.Current != nil {
				result.CursorStatus = record.Recovery.Current.Status
			}
		}
		if record.Resolution != nil {
			result.ResolutionStatus = record.Resolution.Status
			result.ResolutionSource = record.Resolution.EvidenceSource
			result.ResolutionReference = record.Resolution.EvidenceReference
			result.ResolutionObservedAt = record.Resolution.ObservedAt
		}
		result.RetryBlocked = phase == "retry-check" || phase == "retry-check-after-browser-restart"
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if phase != "first" && phase != "retry" && phase != "interrupted" && phase != "result-persisting" && phase != "interrupted-browser-restart" && phase != "zero-effect-interrupted" && phase != "retry-zero-effect" {
		return fmt.Errorf("unknown recovery phase %q", phase)
	}
	currentTaskID := childTaskID
	expectedText := "Accepted: 1"
	if phase == "first" || phase == "retry" {
		currentTaskID = taskID + "-completed"
		expectedText = "Accepted: 3"
	} else if phase == "result-persisting" {
		expectedText = "Accepted: 2"
	} else if phase == "interrupted-browser-restart" {
		expectedText = "Accepted: 4"
	} else if phase == "retry-zero-effect" {
		expectedText = os.Getenv("SCOUT_RECOVERY_EXPECTED_TEXT")
		if expectedText == "" {
			return fmt.Errorf("host-authorized follow-up requires an expected fixture result")
		}
	}
	var finalPostcondition *agent.Postcondition
	if phase != "result-persisting" {
		finalPostcondition = &agent.Postcondition{Text: expectedText}
	}
	task := agent.TaskRunSpec{
		ID:             currentTaskID,
		TargetID:       targetID,
		AllowedOrigins: []string{origin},
		Actions: []agent.BatchAction{{
			Action:       "click",
			Selector:     "#submit",
			ExpectedText: expectedText,
		}},
		FinalPostcondition: finalPostcondition,
	}
	result, err := session.RunTaskContext(ctx, task)
	if err != nil {
		return fmt.Errorf("run task: %w", err)
	}
	if result.TargetID != targetID {
		return fmt.Errorf("task result target %q differs from expected %q", result.TargetID, targetID)
	}
	return json.NewEncoder(os.Stdout).Encode(childResult{TargetID: result.TargetID, Status: result.Status, PlanID: result.PlanID, IdempotencyReplay: result.IdempotencyReplay})
}

type fixtureLedger struct {
	Accepted       int    `json:"accepted"`
	TaskAccepted   int    `json:"task_accepted"`
	Total          int    `json:"total"`
	TaskID         string `json:"task_id"`
	ApplicationKey string `json:"application_key"`
}

func fixtureTaskURL(origin, taskID string) string {
	return strings.TrimRight(origin, "/") + "/?task=" + url.QueryEscape(taskID)
}

func fixtureTaskURLWithKey(origin, taskID, applicationKey string) string {
	return fixtureTaskURL(origin, taskID) + "&key=" + url.QueryEscape(applicationKey)
}

func readFixtureLedgerForKey(ctx context.Context, origin, taskID, applicationKey string) (fixtureLedger, error) {
	ledgerURL := strings.TrimRight(origin, "/") + "/ledger?task=" + url.QueryEscape(taskID)
	if applicationKey != "" {
		ledgerURL += "&key=" + url.QueryEscape(applicationKey)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ledgerURL, nil)
	if err != nil {
		return fixtureLedger{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fixtureLedger{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fixtureLedger{}, fmt.Errorf("ledger returned HTTP %d", response.StatusCode)
	}
	var ledger fixtureLedger
	if err := json.NewDecoder(response.Body).Decode(&ledger); err != nil {
		return fixtureLedger{}, fmt.Errorf("decode ledger response: %w", err)
	}
	if ledger.TaskID != taskID || ledger.ApplicationKey != applicationKey && applicationKey != "" {
		return fixtureLedger{}, fmt.Errorf("ledger returned task ID %q; want %q", ledger.TaskID, taskID)
	}
	return ledger, nil
}

func startInterruptedChild(ctx context.Context, cdpURL, origin, storePath, targetID string, mutationStarted <-chan struct{}) (childResult, error) {
	return startChildKilledAtSignal(ctx, "interrupted", taskID, cdpURL, origin, storePath, targetID, "", mutationStarted)
}

func startChildKilledAtSignal(ctx context.Context, phase, childTaskID, cdpURL, origin, storePath, targetID, checkpointURL string, signal <-chan struct{}, extraEnv ...string) (childResult, error) {
	executable, err := os.Executable()
	if err != nil {
		return childResult{}, fmt.Errorf("find example executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = append(os.Environ(),
		"SCOUT_RECOVERY_PHASE="+phase,
		"SCOUT_RECOVERY_CDP="+cdpURL,
		"SCOUT_RECOVERY_ORIGIN="+origin,
		"SCOUT_RECOVERY_STORE="+storePath,
		"SCOUT_RECOVERY_TARGET="+targetID,
		"SCOUT_RECOVERY_TASK_ID="+childTaskID,
		"SCOUT_RECOVERY_CHECKPOINT_URL="+checkpointURL,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	if err := cmd.Start(); err != nil {
		return childResult{}, fmt.Errorf("start interrupted Scout process: %w", err)
	}
	select {
	case <-signal:
		if err := cmd.Process.Kill(); err != nil {
			return childResult{}, fmt.Errorf("kill interrupted Scout process: %w", err)
		}
		_ = cmd.Wait()
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return childResult{}, fmt.Errorf("wait for fixture mutation before process kill: %w", ctx.Err())
	}
	return runChildProcessForTask(ctx, "inspect", childTaskID, cdpURL, origin, storePath, targetID)
}

type checkpointTaskRunStore struct {
	agent.TaskRunStore
	progress      agent.TaskRunProgressStore
	checkpointURL string
}

func (s checkpointTaskRunStore) UpdateTaskRunRecovery(ctx context.Context, id, fingerprint string, recovery agent.TaskRunRecovery) error {
	if err := s.progress.UpdateTaskRunRecovery(ctx, id, fingerprint, recovery); err != nil {
		return err
	}
	if recovery.Phase != "result_persisting" {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.checkpointURL, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("await result-persistence checkpoint: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("result-persistence checkpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (s checkpointTaskRunStore) ResolveTaskRun(ctx context.Context, id, fingerprint string, resolution agent.TaskRunResolution) error {
	store, ok := s.TaskRunStore.(agent.TaskRunResolutionStore)
	if !ok {
		return fmt.Errorf("underlying task store cannot resolve interrupted runs")
	}
	return store.ResolveTaskRun(ctx, id, fingerprint, resolution)
}
