//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
	"go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

func TestIntegrationSessionJSONLExtractAllPaginationOnLivePage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><body>`)
		for i := 0; i < 10; i++ {
			_, _ = fmt.Fprintf(w, `<article>item-%02d</article>`, i)
		}
		_, _ = io.WriteString(w, `<article>`+strings.Repeat("x", 4100)+`</article></body></html>`)
	}))
	defer pageServer.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
		Headless: true, RemoteCDP: browser.WSEndpoint(),
		AllowedOrigins: []string{pageServer.URL}, AllowPrivateIPs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.NavigateWithPostconditionContext(ctx, pageServer.URL, agent.Postcondition{}); err != nil {
		t.Fatal(err)
	}
	firstRaw, err := executeSessionRequest(session, sessionRequest{Command: "extract_all", Selector: "article", Limit: 8, IncludeHandles: true})
	if err != nil {
		t.Fatalf("extract first page: %v", err)
	}
	first := firstRaw.(*agent.ExtractAllResult)
	if first.Count != 8 || first.Total != 11 || !first.HasMore || first.NextOffset != 8 || first.ItemsTruncated || len(first.References) != 8 || first.References[0].Handle == "" {
		t.Fatalf("first page metadata = %+v", first)
	}
	tailRaw, err := executeSessionRequest(session, sessionRequest{Command: "extract_all", Selector: "article", Offset: first.NextOffset, Limit: 8})
	if err != nil {
		t.Fatalf("extract final page: %v", err)
	}
	tail := tailRaw.(*agent.ExtractAllResult)
	if tail.Count != 3 || tail.HasMore || tail.NextOffset != 11 || !tail.ItemsTruncated || len(tail.ItemTruncated) != 3 || !tail.ItemTruncated[2] {
		t.Fatalf("final page metadata = %+v", tail)
	}
}

func TestSessionJSONLRecoverTaskRunInspectsAndRecordsHostResolution(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "task-runs.db")
	store, err := agent.OpenBoltTaskRunStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, created, err := store.ReserveTaskRun(ctx, agent.TaskRunRecord{ID: "recover-1", Fingerprint: "plan-fingerprint", InFlight: true}); err != nil || !created {
		t.Fatalf("ReserveTaskRun() created=%t err=%v", created, err)
	}
	plan := agent.TaskRunPlan{SchemaVersion: 1, TargetID: "target-1", Actions: []agent.TaskRunRecoveryStep{{Action: "click", Selector: "#submit", Postcondition: "text"}}}
	recovery := agent.TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: plan.Actions}
	if err := store.BindTaskRunHandoff(ctx, "recover-1", "plan-fingerprint", "target-1", "browser-1", plan, recovery); err != nil {
		t.Fatal(err)
	}
	recovery.Current = &agent.TaskRunRecoveryCursor{Index: 0, Status: "started"}
	if err := store.UpdateTaskRunRecovery(ctx, "recover-1", "plan-fingerprint", recovery); err != nil {
		t.Fatal(err)
	}
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{TaskRunStore: store})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var output bytes.Buffer
	input := "{\"command\":\"recover_task_run\",\"task_id\":\"recover-1\"}\n" +
		"{\"command\":\"resolve_task_run\",\"task_id\":\"recover-1\",\"plan_fingerprint\":\"plan-fingerprint\",\"resolution_status\":\"verified_complete\",\"resolution_evidence\":\"order API accepted recover-1\",\"resolution_evidence_source\":\"orders_api\",\"resolution_evidence_reference\":\"order-42\",\"resolution_observed_at\":\"2026-11-10T20:00:00Z\"}\n"
	if err := runSessionJSONL(session, strings.NewReader(input), &output, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d responses, want inspection and resolution: %s", len(lines), output.String())
	}
	var inspection sessionResponse
	if err := json.Unmarshal([]byte(lines[0]), &inspection); err != nil {
		t.Fatal(err)
	}
	if !inspection.OK || inspection.Result == nil {
		t.Fatalf("inspection response = %+v", inspection)
	}
	inspectionJSON, _ := json.Marshal(inspection.Result)
	if !strings.Contains(string(inspectionJSON), `"in_flight":true`) || !strings.Contains(string(inspectionJSON), `"fingerprint":"plan-fingerprint"`) {
		t.Fatalf("inspection did not return unresolved record: %s", inspectionJSON)
	}
	var resolution sessionResponse
	if err := json.Unmarshal([]byte(lines[1]), &resolution); err != nil {
		t.Fatal(err)
	}
	if !resolution.OK {
		t.Fatalf("resolution response = %+v", resolution)
	}
	record, found, err := store.GetTaskRun(ctx, "recover-1")
	if err != nil || !found || record.InFlight || record.Resolution == nil || record.Resolution.Status != "verified_complete" {
		t.Fatalf("stored record = %+v found=%t err=%v", record, found, err)
	}
	if _, err := executeSessionRequest(session, sessionRequest{Command: "recover_task_run", TaskID: "recover-1", PlanFingerprint: "plan-fingerprint", ResolutionStatus: "rejected", ResolutionEvidence: "conflicting conclusion"}); err == nil {
		t.Fatal("second conflicting resolution succeeded")
	}
}

func TestSessionJSONLRecoveryPersistsAndResumesExactTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("local Chrome unavailable: %v", err)
	}
	defer browser.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div id="state">cli-recovered</div></body></html>`))
	}))
	defer pageServer.Close()
	storePath := filepath.Join(t.TempDir(), "session-recovery.db")
	store, err := agent.OpenBoltTaskRunStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := agent.SessionConfig{Headless: true, RemoteCDP: browser.WSEndpoint(), AllowedOrigins: []string{pageServer.URL}, AllowPrivateIPs: true, TaskRunStore: store}
	session, err := agent.NewSessionContext(ctx, cfg)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	page, err := session.NavigateWithPostconditionContext(ctx, pageServer.URL, agent.Postcondition{Text: "cli-recovered"})
	if err != nil {
		_ = session.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	owner := cliRecoveryOwnerScope()
	var persistOutput bytes.Buffer
	if err := runSessionJSONLWithRecovery(&session, cfg, store, owner, strings.NewReader("{\"id\":1,\"command\":\"persist_session\"}\n"), &persistOutput, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var persisted sessionResponse
	if err := json.Unmarshal(bytes.TrimSpace(persistOutput.Bytes()), &persisted); err != nil || !persisted.OK {
		t.Fatalf("persist response=%+v decode err=%v", persisted, err)
	}
	persistedJSON, _ := json.Marshal(persisted.Result)
	var persistedResult struct {
		Handle   string `json:"handle"`
		TargetID string `json:"target_id"`
	}
	if err := json.Unmarshal(persistedJSON, &persistedResult); err != nil || persistedResult.Handle == "" || persistedResult.TargetID != page.TargetID {
		t.Fatalf("persist result=%s err=%v", persistedJSON, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = agent.OpenBoltTaskRunStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg.TaskRunStore = store
	session, err = agent.NewSessionContext(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	request, _ := json.Marshal(map[string]any{"id": 2, "command": "resume_session", "handle": persistedResult.Handle})
	var resumeOutput bytes.Buffer
	if err := runSessionJSONLWithRecovery(&session, cfg, store, owner, bytes.NewReader(append(request, '\n')), &resumeOutput, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var resumed sessionResponse
	if err := json.Unmarshal(bytes.TrimSpace(resumeOutput.Bytes()), &resumed); err != nil || !resumed.OK {
		t.Fatalf("resume response=%+v decode err=%v", resumed, err)
	}
	status, err := session.StatusContext(ctx)
	if err != nil || status.ActiveTargetID != page.TargetID {
		t.Fatalf("resumed active target=%q err=%v, want %q", status.ActiveTargetID, err, page.TargetID)
	}
	state, err := session.ExtractContext(ctx, "#state")
	if err != nil || state.Text != "cli-recovered" {
		t.Fatalf("resumed page state=(%q, %v), want cli-recovered", state.Text, err)
	}
}

func TestSessionJSONLOversizedResponseArtifactProtocol(t *testing.T) {
	ctx := context.Background()
	store, err := agent.OpenBoltTaskRunStore(filepath.Join(t.TempDir(), "task-runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, created, err := store.ReserveTaskRun(ctx, agent.TaskRunRecord{ID: "large-plan", Fingerprint: "fingerprint", InFlight: true}); err != nil || !created {
		t.Fatalf("ReserveTaskRun created=%t err=%v", created, err)
	}
	plan := agent.TaskRunPlan{SchemaVersion: 1, TargetID: "target-1", Actions: make([]agent.TaskRunRecoveryStep, 100)}
	for i := range plan.Actions {
		plan.Actions[i] = agent.TaskRunRecoveryStep{Action: "click", Selector: "#" + strings.Repeat("x", 1800)}
	}
	recovery := agent.TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: plan.Actions}
	if err := store.BindTaskRunHandoff(ctx, "large-plan", "fingerprint", "target-1", "browser-1", plan, recovery); err != nil {
		t.Fatal(err)
	}
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{TaskRunStore: store})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- runSessionJSONL(session, requestReader, responseWriter, func() error { return nil })
	}()
	clientOut := bufio.NewReader(responseReader)
	writeRequest := func(value any) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := requestWriter.Write(append(encoded, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	readResponse := func() sessionResponse {
		t.Helper()
		line, err := clientOut.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read JSONL response: %v", err)
		}
		var response sessionResponse
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("decode JSONL response: %v\n%s", err, line)
		}
		if len(line) > maxSessionResponseBytes {
			t.Fatalf("JSONL response is %d bytes, exceeds %d", len(line), maxSessionResponseBytes)
		}
		return response
	}
	writeRequest(map[string]any{"id": "inspect", "command": "recover_task_run", "task_id": "large-plan"})
	inspection := readResponse()
	if inspection.OK || !inspection.OutputOmitted || inspection.Artifact == nil {
		t.Fatalf("oversized response = %+v, want output-limit artifact", inspection)
	}
	var assembled []byte
	for offset := 0; ; {
		writeRequest(map[string]any{"id": "read", "command": "read_output_artifact", "artifact_id": inspection.Artifact.ID, "offset": offset, "limit_bytes": maxSessionOutputReadBytes})
		chunkResponse := readResponse()
		if !chunkResponse.OK {
			t.Fatalf("read_output_artifact response = %+v", chunkResponse)
		}
		chunk := chunkResponse.Result.(map[string]any)
		part, err := base64.StdEncoding.DecodeString(chunk["data_base64"].(string))
		if err != nil {
			t.Fatal(err)
		}
		assembled = append(assembled, part...)
		if chunk["truncated"] != true {
			break
		}
		offset += len(part)
	}
	checksum := sha256.Sum256(assembled)
	if hex.EncodeToString(checksum[:]) != inspection.Artifact.SHA256 {
		t.Fatalf("artifact digest=%x, want %s", checksum, inspection.Artifact.SHA256)
	}
	var original sessionResponse
	if err := json.Unmarshal(assembled, &original); err != nil || original.Result == nil {
		t.Fatalf("artifact did not reconstruct original response: err=%v result=%T", err, original.Result)
	}
	writeRequest(map[string]any{"id": "quit", "command": "quit"})
	if response := readResponse(); !response.OK {
		t.Fatalf("quit response = %+v", response)
	}
	_ = requestWriter.Close()
	_ = responseReader.Close()
	if err := <-done; err != nil && err != io.EOF {
		t.Fatalf("session loop: %v", err)
	}
}

func TestSessionJSONLBatchTimeoutReturnsPartialResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><h1 id="ready">Ready</h1></body></html>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	input := `{"id":"batch-timeout","command":"batch","timeout_ms":400,"actions":[{"action":"wait","selector":"#ready"},{"action":"wait","selector":"#never"}]}` + "\n"
	var output bytes.Buffer
	if err := runSessionJSONL(session, strings.NewReader(input), &output, session.Close); err != nil {
		t.Fatalf("runSessionJSONL: %v", err)
	}

	var response sessionResponse
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, output.String())
	}
	if response.OK || response.Error == "" || response.Result != nil || response.PartialResult == nil {
		t.Fatalf("response does not preserve interrupted batch: %+v", response)
	}
	partial, ok := response.PartialResult.(map[string]any)
	if !ok {
		t.Fatalf("partial_result type = %T, want object", response.PartialResult)
	}
	results, ok := partial["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("partial results = %#v, want completed and interrupted step outcomes", partial["results"])
	}
	step, ok := results[0].(map[string]any)
	if !ok || step["outcome"] != string(agent.OutcomePostconditionVerified) {
		t.Fatalf("first partial step = %#v, want verified wait", results[0])
	}
	interrupted, ok := results[1].(map[string]any)
	if !ok || interrupted["success"] == true || interrupted["error"] == nil {
		t.Fatalf("interrupted partial step = %#v, want failed step evidence", results[1])
	}
}

func TestSessionJSONLBatchMutationTimeoutReturnsRecoveryRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><button id="mutate" onclick="document.querySelector('#status').textContent='Mutation landed'">Mutate</button><div id="status"></div></body></html>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	input := `{"id":"batch-mutation-timeout","command":"batch","timeout_ms":250,"actions":[{"action":"click","selector":"#mutate","expected_text":"Mutation landed"}]}` + "\n"
	var output bytes.Buffer
	if err := runSessionJSONL(session, strings.NewReader(input), &output, session.Close); err != nil {
		t.Fatalf("runSessionJSONL: %v", err)
	}

	var response sessionResponse
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, output.String())
	}
	if response.OK || response.PartialResult == nil {
		t.Fatalf("response = %+v, want interrupted partial batch", response)
	}
	partial, ok := response.PartialResult.(map[string]any)
	if !ok {
		t.Fatalf("partial_result type = %T, want object", response.PartialResult)
	}
	results, ok := partial["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("partial results = %#v, want one uncertain click", partial["results"])
	}
	step, ok := results[0].(map[string]any)
	if !ok || step["outcome"] != string(agent.OutcomeUnknown) {
		t.Fatalf("partial click = %#v, want unknown outcome", results[0])
	}
	record, ok := step["recovery_record"].(map[string]any)
	if !ok {
		t.Fatalf("recovery_record = %#v, want portable recovery object", step["recovery_record"])
	}
	outcome, ok := record["action_outcome"].(map[string]any)
	if !ok || outcome["action"] != "click" || outcome["target"] != "#mutate" || outcome["expected_postcondition"] == nil {
		t.Fatalf("recovery action_outcome = %#v, want target-bound click and postcondition", record["action_outcome"])
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode recovery record: %v", err)
	}
	recovery, err := agent.DecodeActionRecoveryRecord(recordJSON)
	if err != nil {
		t.Fatalf("decode returned recovery record: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close original Scout session: %v", err)
	}
	restarted, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		t.Fatalf("start fresh Scout session: %v", err)
	}
	defer restarted.Close()
	reconciled, err := restarted.ReconcileActionRecoveryRecordContext(ctx, recovery)
	if err != nil {
		t.Fatalf("reconcile returned recovery record: %v", err)
	}
	if !reconciled.Verified || reconciled.Evidence == "" {
		t.Fatalf("reconciliation = %+v, want verified late mutation", reconciled)
	}

	otherBrowser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext for unrelated browser: %v", err)
	}
	defer otherBrowser.Close()
	wrongBrowserSession, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second, RemoteCDP: otherBrowser.WSEndpoint()})
	if err != nil {
		t.Fatalf("start unrelated browser session: %v", err)
	}
	defer wrongBrowserSession.Close()
	if _, err := wrongBrowserSession.Navigate(server.URL); err != nil {
		t.Fatalf("navigate unrelated browser session: %v", err)
	}
	// Deliberately make the recovery target absent in the unrelated browser too.
	// Without the browser-instance guard this would satisfy target_absent and
	// produce a false verified result for a close that may still be pending in
	// the original browser.
	otherTab, err := wrongBrowserSession.OpenTab("recovery-collision", server.URL)
	if err != nil {
		t.Fatalf("open unrelated tab: %v", err)
	}
	if otherTab.TargetID == recovery.ActionOutcome.TargetID {
		t.Fatalf("independent browsers unexpectedly reused target ID %q", otherTab.TargetID)
	}
	if _, err := wrongBrowserSession.SwitchTab("default"); err != nil {
		t.Fatalf("switch away from unrelated tab: %v", err)
	}
	if _, err := wrongBrowserSession.CloseTabResult("recovery-collision"); err != nil {
		t.Fatalf("close unrelated tab: %v", err)
	}
	if _, err := wrongBrowserSession.ReconcileActionRecoveryRecordContext(ctx, recovery); err == nil {
		t.Fatal("reconcile against unrelated browser succeeded; want browser identity mismatch")
	} else if operation, ok := err.(*agent.OperationError); !ok || operation.Cause != "browser_instance_mismatch" {
		t.Fatalf("reconcile against unrelated browser error = %#v, want browser_instance_mismatch", err)
	}
}

func TestPortableCloseRecoveryRemovesLateClosedTabFromRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><html><body><h1>Ready</h1></body></html>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Fatalf("LaunchContext: %v", err)
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, AllowPrivateIPs: true, Timeout: 10 * time.Second, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		t.Fatalf("NewSessionContext: %v", err)
	}
	defer session.Close()
	if _, err := session.Navigate(server.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	opened, err := session.OpenTab("late-close", server.URL)
	if err != nil {
		t.Fatalf("OpenTab: %v", err)
	}
	if _, err := session.SwitchTab("default"); err != nil {
		t.Fatalf("SwitchTab: %v", err)
	}
	unknownClose := &agent.ActionOutcomeError{
		RunID: "portable-late-close", OperationOrdinal: opened.OperationOrdinal,
		Action: "close_tab", Target: "late-close", TargetID: opened.TargetID,
		BrowserInstanceID:     opened.Receipt.BrowserInstanceID,
		Outcome:               agent.OutcomeUnknown,
		ExpectedPostcondition: &agent.Postcondition{TargetAbsent: opened.TargetID},
	}
	recovery, err := agent.NewActionRecoveryRecord(unknownClose)
	if err != nil {
		t.Fatalf("NewActionRecoveryRecord: %v", err)
	}
	encoded, err := json.Marshal(recovery)
	if err != nil {
		t.Fatalf("Marshal recovery: %v", err)
	}
	recovery, err = agent.DecodeActionRecoveryRecord(encoded)
	if err != nil {
		t.Fatalf("DecodeActionRecoveryRecord: %v", err)
	}
	closer, err := cdp.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		t.Fatalf("connect independent CDP closer: %v", err)
	}
	defer closer.Close()
	if err := closer.CloseTargetCtx(ctx, opened.TargetID); err != nil {
		t.Fatalf("close target outside Scout session: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		targets, err := closer.GetTargetsCtx(ctx)
		if err != nil {
			t.Fatalf("read target inventory after late close: %v", err)
		}
		present := false
		for _, target := range targets {
			if target.TargetID == opened.TargetID {
				present = true
				break
			}
		}
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("target %s remained present after close acknowledgement", opened.TargetID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	reconciled, err := session.ReconcileActionRecoveryRecordContext(ctx, recovery)
	if err != nil {
		t.Fatalf("reconcile late close: %v", err)
	}
	if !reconciled.Verified || reconciled.Outcome != string(agent.OutcomePostconditionVerified) {
		t.Fatalf("reconciliation = %+v, want verified target absence", reconciled)
	}
	tabs, err := session.ListTabsContext(ctx)
	if err != nil {
		t.Fatalf("ListTabs after recovery: %v", err)
	}
	for _, tab := range tabs {
		if tab.Name == "late-close" {
			t.Fatalf("late-closed tab remained in registry: %+v", tabs)
		}
	}
}
