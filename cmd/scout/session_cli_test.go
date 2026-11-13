package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func TestSessionReconcileRejectsMultipleRecoverySources(t *testing.T) {
	session := &agent.Session{}
	_, err := executeSessionRequest(session, sessionRequest{
		Command: "reconcile",
		Options: map[string]string{
			"recovery_record":        "{}",
			"recovery_record_base64": "e30=",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "only one of") {
		t.Fatalf("reconcile error = %v, want multiple-source rejection", err)
	}
}

func TestConfiguredCLISessionAllowedOrigins(t *testing.T) {
	origins, err := configuredCLISessionAllowedOrigins(cliFlags{flags: map[string]string{"allowed-origins": "https://example.test,https://other.test:8443"}})
	if err != nil || strings.Join(origins, ",") != "https://example.test,https://other.test:8443" {
		t.Fatalf("origins = %v, err = %v", origins, err)
	}
	if origins, err := configuredCLISessionAllowedOrigins(cliFlags{}); err != nil || origins != nil {
		t.Fatalf("unset origins = %v, err = %v", origins, err)
	}
	for _, raw := range []string{"", "https://example.test,"} {
		if _, err := configuredCLISessionAllowedOrigins(cliFlags{flags: map[string]string{"allowed-origins": raw}}); err == nil {
			t.Errorf("allowed origins %q unexpectedly accepted", raw)
		}
	}
}

func TestSessionBatchRequiresActions(t *testing.T) {
	_, err := executeSessionRequest(&agent.Session{}, sessionRequest{Command: "batch"})
	if err == nil || !strings.Contains(err.Error(), "non-empty actions array") {
		t.Fatalf("batch error = %v, want required actions validation", err)
	}
}

func TestSessionBatchEnforcesActionLimit(t *testing.T) {
	_, err := executeSessionRequest(&agent.Session{}, sessionRequest{
		Command: "batch",
		Actions: make([]agent.BatchAction, 101),
	})
	if err == nil || !strings.Contains(err.Error(), "at most 100 actions") {
		t.Fatalf("batch error = %v, want 100 action limit", err)
	}
}

func TestSessionBatchStoppedErrorHasSpecificCode(t *testing.T) {
	index := 2
	result := &agent.BatchResult{StoppedOnUnknownAt: &index, NextActionIndex: 3, ResumeBlocked: true}
	err := &agent.BatchStoppedError{Result: result}
	message, details, record := sessionErrorDetails(err)
	if message == "" || details == nil || details.Code != "SCOUT_BATCH_STOPPED" || record != nil {
		t.Fatalf("batch stop details = (%q, %+v, %v)", message, details, record)
	}
}

func TestSessionJSONLBatchRequestDecodesActions(t *testing.T) {
	var req sessionRequest
	if err := json.Unmarshal([]byte(`{"command":"batch","actions":[{"action":"click","selector":"#save","expected_text":"Saved"}]}`), &req); err != nil {
		t.Fatalf("decode batch request: %v", err)
	}
	if req.Command != "batch" || len(req.Actions) != 1 || req.Actions[0].Action != "click" || req.Actions[0].ExpectedText != "Saved" {
		t.Fatalf("decoded request = %+v", req)
	}
}

func TestSessionJSONLExtractAllRequestDecodesPaginationOptions(t *testing.T) {
	var req sessionRequest
	if err := json.Unmarshal([]byte(`{"command":"extract_all","selector":"article","offset":16,"limit":8,"include_handles":true}`), &req); err != nil {
		t.Fatalf("decode extract_all request: %v", err)
	}
	if req.Command != "extract_all" || req.Selector != "article" || req.Offset != 16 || req.Limit != 8 || !req.IncludeHandles {
		t.Fatalf("decoded request = %+v", req)
	}
}

func TestSessionJSONLPressKeyDecodesPostcondition(t *testing.T) {
	var req sessionRequest
	if err := json.Unmarshal([]byte(`{"command":"press_key","key":"Meta+s","expected_postcondition":{"text":"Saved"}}`), &req); err != nil {
		t.Fatalf("decode press_key request: %v", err)
	}
	if req.Command != "press_key" || req.Key != "Meta+s" || req.ExpectedPostcondition == nil || req.ExpectedPostcondition.Text != "Saved" {
		t.Fatalf("decoded request = %+v", req)
	}
	if err := req.ExpectedPostcondition.Validate(); err != nil {
		t.Fatalf("postcondition validation: %v", err)
	}
}

func TestSessionJSONLKeyControlsRequireKey(t *testing.T) {
	for _, command := range []string{"key_down", "key_up"} {
		_, err := executeSessionRequest(&agent.Session{}, sessionRequest{Command: command})
		if err == nil || !strings.Contains(err.Error(), "requires key") {
			t.Errorf("%s error = %v, want missing-key validation", command, err)
		}
	}
}

func TestSessionExtractAllRequiresSelector(t *testing.T) {
	_, err := executeSessionRequest(&agent.Session{}, sessionRequest{Command: "extract_all"})
	if err == nil || !strings.Contains(err.Error(), "requires selector") {
		t.Fatalf("extract_all error = %v, want missing selector validation", err)
	}
}

func TestRecoverTaskRunRequiresTaskID(t *testing.T) {
	_, err := executeSessionRequest(&agent.Session{}, sessionRequest{Command: "recover_task_run"})
	if err == nil || !strings.Contains(err.Error(), "requires task_id") {
		t.Fatalf("recover_task_run error = %v, want missing task ID", err)
	}
}

func TestRecoverTaskRunMissingRecordIsReadOnlyNotFound(t *testing.T) {
	got, err := executeSessionRequest(&agent.Session{}, sessionRequest{Command: "recover_task_run", TaskID: "missing"})
	if err != nil {
		t.Fatalf("recover_task_run error = %v", err)
	}
	result, ok := got.(agent.TaskRunRecoveryView)
	if !ok || result.Found || result.Record != nil || result.Receipt != nil {
		t.Fatalf("recover_task_run result = %#v, want empty not-found view", got)
	}
}

func TestSessionRecoveryCommandsRequireDurableStore(t *testing.T) {
	session := &agent.Session{}
	input := "{\"id\":1,\"command\":\"persist_session\"}\n{\"id\":2,\"command\":\"resume_session\",\"handle\":\"opaque-handle\"}\n"
	var output bytes.Buffer
	if err := runSessionJSONLWithRecovery(&session, agent.SessionConfig{}, nil, "local-user/test", strings.NewReader(input), &output, func() error { return nil }); err != nil {
		t.Fatalf("runSessionJSONLWithRecovery() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d responses, want 2: %s", len(lines), output.String())
	}
	for i, line := range lines {
		var response sessionResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("decode response %d: %v", i, err)
		}
		if response.OK || !strings.Contains(response.Error, "requires --task-store") {
			t.Errorf("response %d = %+v, want durable-store requirement", i, response)
		}
	}
}

func TestRecoverTaskRunResolutionRequiresFullAttestation(t *testing.T) {
	_, err := executeSessionRequest(&agent.Session{}, sessionRequest{
		Command: "recover_task_run", TaskID: "checkout-42", PlanFingerprint: "fingerprint", ResolutionStatus: "verified_complete",
	})
	if err == nil || !strings.Contains(err.Error(), "is read-only") {
		t.Fatalf("recover_task_run error = %v, want read-only guidance", err)
	}
}

func TestRunSessionJSONLRejectsMultipleRecoverySources(t *testing.T) {
	session := &agent.Session{}
	input := `{"id":1,"command":"reconcile","options":{"recovery_record":"{}","recovery_record_base64":"e30="}}` + "\n"
	var output bytes.Buffer
	err := runSessionJSONL(session, strings.NewReader(input), &output, func() error { return nil })
	if err != nil {
		t.Fatalf("runSessionJSONL() error = %v", err)
	}
	var response sessionResponse
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.OK || !strings.Contains(response.Error, "only one of") {
		t.Fatalf("response = %+v, want multiple-source rejection", response)
	}
}

func TestBoundSessionResponsePreservesRecoveryRecordWhenOutputIsOmitted(t *testing.T) {
	record := &agent.ActionRecoveryRecord{SchemaVersion: agent.ActionRecoverySchemaVersion, ActionOutcome: agent.ActionOutcomeError{Outcome: agent.OutcomeUnknown, TargetID: "target-1"}}
	response := boundSessionResponse(sessionResponse{
		ID:             json.RawMessage(`"call-1"`),
		OK:             true,
		Result:         map[string]any{"payload": strings.Repeat("x", maxSessionResponseBytes)},
		RecoveryRecord: record,
	})
	if response.OK || !response.OutputOmitted || !response.Truncated || response.OriginalBytes <= maxSessionResponseBytes || response.MaxBytes != maxSessionResponseBytes {
		t.Fatalf("bounded response metadata = %+v", response)
	}
	if response.ID == nil || response.RecoveryRecord == nil || response.RecoveryRecord.ActionOutcome.TargetID != "target-1" {
		t.Fatalf("bounded response lost request/recovery identity: %+v", response)
	}
	if response.Result != nil || response.PartialResult != nil {
		t.Fatalf("bounded response retained oversized payload: %+v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > maxSessionResponseBytes {
		t.Fatalf("bounded response size=%d err=%v", len(encoded), err)
	}
}

func TestBoundSessionResponseCanBeReadBackInBoundedChunks(t *testing.T) {
	store, err := newSessionOutputStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	want := sessionResponse{OK: true, Result: map[string]string{"payload": strings.Repeat("z", maxSessionResponseBytes)}}
	bounded := boundSessionResponseWithStore(want, store)
	if !bounded.OutputOmitted || bounded.Artifact == nil {
		t.Fatalf("bounded response = %+v, want retrievable artifact", bounded)
	}
	if info, err := os.Stat(filepath.Join(store.dir, bounded.Artifact.ID+".json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact permissions = %v, err=%v; want 0600", info, err)
	}
	var assembled []byte
	for offset := 0; ; {
		chunk, err := store.read(bounded.Artifact.ID, offset, maxSessionOutputReadBytes)
		if err != nil {
			t.Fatal(err)
		}
		chunkBytes, err := base64.StdEncoding.DecodeString(chunk["data_base64"].(string))
		if err != nil {
			t.Fatal(err)
		}
		assembled = append(assembled, chunkBytes...)
		encodedChunk, err := json.Marshal(chunk)
		if err != nil || len(encodedChunk) > maxSessionResponseBytes {
			t.Fatalf("chunk response size=%d err=%v", len(encodedChunk), err)
		}
		if chunk["truncated"] != true {
			break
		}
		offset += len(chunkBytes)
	}
	digest := sha256.Sum256(assembled)
	if len(assembled) != bounded.Artifact.Bytes || hex.EncodeToString(digest[:]) != bounded.Artifact.SHA256 {
		t.Fatalf("reassembled artifact size=%d digest=%x want size=%d digest=%s", len(assembled), digest, bounded.Artifact.Bytes, bounded.Artifact.SHA256)
	}
	var restored sessionResponse
	if err := json.Unmarshal(assembled, &restored); err != nil || restored.Result == nil {
		t.Fatalf("restored oversized response decode err=%v result=%T", err, restored.Result)
	}
	if _, err := store.read(bounded.Artifact.ID, -1, maxSessionOutputReadBytes); err == nil {
		t.Fatal("negative artifact offset was accepted")
	}
	if _, err := store.read(strings.Repeat("../", 1000), 0, 0); err == nil || strings.Contains(err.Error(), strings.Repeat("../", 100)) {
		t.Fatalf("malformed artifact ID error = %v", err)
	}
}

func TestBoundSessionResponseNeverExceedsLimitWithLargeIdentityAndRecovery(t *testing.T) {
	record := &agent.ActionRecoveryRecord{SchemaVersion: agent.ActionRecoverySchemaVersion, ActionOutcome: agent.ActionOutcomeError{
		Outcome: agent.OutcomeUnknown, TargetID: strings.Repeat("t", maxSessionResponseBytes), Evidence: strings.Repeat("e", maxSessionResponseBytes),
	}}
	response := boundSessionResponseWithStore(sessionResponse{
		ID:     json.RawMessage(`"` + strings.Repeat("i", maxSessionResponseBytes) + `"`),
		Result: map[string]string{"payload": strings.Repeat("x", maxSessionResponseBytes)}, RecoveryRecord: record,
	}, nil)
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > maxSessionResponseBytes {
		t.Fatalf("fallback response size=%d err=%v", len(encoded), err)
	}
	if !response.IDOmitted || !response.RecoveryOmitted || response.RecoveryRecord != nil {
		t.Fatalf("large metadata was not explicitly omitted: %+v", response)
	}
}

func TestBoundSessionResponseLeavesSmallResponseUnchanged(t *testing.T) {
	want := sessionResponse{OK: true, Result: map[string]string{"text": "ready"}}
	got := boundSessionResponse(want)
	if got.OK != want.OK || got.Result == nil || got.OutputOmitted || got.Truncated {
		t.Fatalf("small response changed: %+v", got)
	}
}
