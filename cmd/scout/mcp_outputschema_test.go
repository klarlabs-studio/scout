package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mcp/protocol"
	"go.klarlabs.de/mcp/schema"
	mcpserver "go.klarlabs.de/mcp/server"
	"go.klarlabs.de/scout/agent"
)

func TestBoundStateOutput(t *testing.T) {
	items := make([]any, 500)
	for i := range items {
		items[i] = strings.Repeat("x", 1000)
	}
	result := boundStateOutput(map[string]any{"items": items})
	if result["_scout_truncated"] != true {
		t.Fatal("expected state output to report truncation")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal bounded state: %v", err)
	}
	if len(encoded) > maxStateOutputBytes {
		t.Fatalf("bounded state is %d bytes, limit is %d", len(encoded), maxStateOutputBytes)
	}
}

func TestArtifactReferenceIncludesDigestAndMIME(t *testing.T) {
	data := []byte("capture bytes")
	artifact := artifactReference("/tmp/capture.png", "screenshot", "png", data)
	if artifact.Path != "/tmp/capture.png" || artifact.Kind != "screenshot" || artifact.MIMEType != "image/png" || artifact.SizeBytes != int64(len(data)) {
		t.Fatalf("unexpected artifact metadata: %+v", artifact)
	}
	if artifact.SHA256 != "e8873af2f1f29756578867d0f90bd3dc73e653f39f7cda5603d3c012d11b4ecb" {
		t.Fatalf("unexpected SHA-256: %s", artifact.SHA256)
	}
}

func TestActionInputsBuildValueAndCountPostconditions(t *testing.T) {
	wantCount := 3
	tests := []struct {
		name string
		got  agent.Postcondition
	}{
		{"navigate", (NavigateInput{ExpectedValueSelector: "#status", ExpectedValue: "saved"}).Postcondition()},
		{"click", (ClickInput{ExpectedCountSelector: ".row", ExpectedCount: &wantCount}).Postcondition()},
		{"dispatch_event", (DispatchEventInput{ExpectedValueSelector: "#status", ExpectedValue: "saved"}).Postcondition()},
		{"drag_drop", (DragDropInput{ExpectedCountSelector: ".row", ExpectedCount: &wantCount}).Postcondition()},
		{"click_label", (ClickLabelInput{ExpectedValueSelector: "#status", ExpectedValue: "saved"}).Postcondition()},
		{"click_handle", (ClickHandleInput{ExpectedCountSelector: ".row", ExpectedCount: &wantCount}).Postcondition()},
		{"click_text", (ClickTextInput{ExpectedValueSelector: "#status", ExpectedValue: "saved"}).Postcondition()},
		{"press_key", (PressKeyInput{ExpectedText: "Saved"}).Postcondition()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.got.Validate(); err != nil {
				t.Fatalf("mapped postcondition is invalid: %v", err)
			}
		})
	}
}

func TestMutationInputsExposePerActionTimeout(t *testing.T) {
	inputs := []any{
		ClickInput{TimeoutMS: 250},
		ClickLabelInput{TimeoutMS: 250},
		ClickHandleInput{TimeoutMS: 250},
		ClickTextInput{TimeoutMS: 250},
		DispatchEventInput{TimeoutMS: 250},
		DragDropInput{TimeoutMS: 250},
		HoverInput{TimeoutMS: 250},
		SelectOptionInput{TimeoutMS: 250},
		ScrollToInput{TimeoutMS: 250},
		ScrollByInput{TimeoutMS: 250},
		FocusInput{TimeoutMS: 250},
	}
	for _, input := range inputs {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("marshal %T: %v", input, err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatalf("decode %T: %v", input, err)
		}
		if fields["timeout_ms"] != float64(250) {
			t.Errorf("%T timeout_ms = %v, want 250", input, fields["timeout_ms"])
		}
	}
}

func TestMCPActionTimeoutBoundsAndValidation(t *testing.T) {
	for _, timeoutMS := range []int{-1, 86_400_001} {
		if _, cancel, err := withMCPActionTimeout(context.Background(), timeoutMS); err == nil {
			cancel()
			t.Errorf("timeout_ms %d was accepted", timeoutMS)
		}
	}
	ctx, cancel, err := withMCPActionTimeout(context.Background(), 250)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 250*time.Millisecond || time.Until(deadline) <= 0 {
		t.Fatalf("per-action deadline was not applied: deadline=%v present=%v", deadline, ok)
	}
}

func TestBoundedToolResponseMiddleware(t *testing.T) {
	artifact := agent.ArtifactReference{Path: "/tmp/capture.png", Kind: "screenshot", MIMEType: "image/png", SizeBytes: int64(maxMCPToolResponseBytes)}
	tests := []struct {
		name     string
		response *protocol.Response
	}{
		{
			name: "oversized text result",
			response: &protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: map[string]any{
				"content": []map[string]string{{"type": "text", "text": strings.Repeat("x", maxMCPToolResponseBytes)}},
			}},
		},
		{
			name: "oversized image with artifact metadata",
			response: &protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("2"), Result: map[string]any{
				"content":           []map[string]string{{"type": "image", "mimeType": "image/png", "data": strings.Repeat("a", maxMCPToolResponseBytes)}},
				"structuredContent": map[string]any{"artifact": artifact},
			}},
		},
		{
			name: "oversized protocol error",
			response: &protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("3"), Error: &protocol.Error{
				Code: protocol.CodeInternalError, Message: "operation failed", Data: map[string]any{"detail": strings.Repeat("x", maxMCPToolResponseBytes)},
			}},
		},
		{
			name: "oversized nested multimodal result",
			response: &protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("4"), Result: map[string]any{
				"content": []map[string]any{{
					"type":     "resource",
					"resource": map[string]any{"uri": "file:///capture", "text": strings.Repeat("x", maxMCPToolResponseBytes)},
				}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := boundedToolResponseMiddleware()(func(context.Context, *protocol.Request) (*protocol.Response, error) {
				return tt.response, nil
			})
			response, err := handler(context.Background(), &protocol.Request{Method: "tools/call"})
			if err != nil {
				t.Fatalf("middleware returned error: %v", err)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal middleware response: %v", err)
			}
			if len(encoded) > maxMCPToolResponseBytes {
				t.Fatalf("response is %d bytes, limit is %d", len(encoded), maxMCPToolResponseBytes)
			}
			if !strings.Contains(string(encoded), "exceeds the 256 KB MCP response limit") || !strings.Contains(string(encoded), "SCOUT_OUTPUT_LIMIT") {
				t.Fatalf("expected bounded-output guidance, got %s", encoded)
			}
			if tt.name == "oversized protocol error" {
				if response.Error == nil || response.Error.Code != protocol.CodeInternalError {
					t.Fatalf("expected original protocol error code to be preserved, got %+v", response.Error)
				}
				data, _ := json.Marshal(response.Error.Data)
				if !strings.Contains(string(data), `"output_omitted":true`) {
					t.Fatalf("expected machine-readable omission metadata, got %s", data)
				}
			} else if response.Error != nil {
				t.Fatalf("result response unexpectedly became protocol error: %+v", response.Error)
			}
		})
	}
}

func TestMinimumJSONBytesIsConservativeForMCPContent(t *testing.T) {
	values := []any{
		map[string]any{"text": strings.Repeat("\u0001", maxMCPToolResponseBytes)},
		map[string]any{"text": strings.Repeat("é", maxMCPToolResponseBytes)},
		map[string]any{"resource": map[string]any{"text": strings.Repeat("x", maxMCPToolResponseBytes)}},
	}
	for i, value := range values {
		minimum, ok := minimumJSONBytes(value)
		if !ok || minimum <= maxMCPToolResponseBytes {
			t.Fatalf("case %d was not proven oversized: minimum=%d ok=%t", i, minimum, ok)
		}
	}

	// For values below the cap, the estimate must never exceed actual JSON
	// output; that invariant is what makes it safe to use for early rejection.
	for _, value := range []any{
		map[string]any{"text": "é\n\"value\""},
		map[string]any{"content": []any{map[string]any{"resource": map[string]any{"text": "small"}}}},
	} {
		minimum, ok := minimumJSONBytes(value)
		encoded, err := json.Marshal(value)
		if err != nil || !ok || minimum > uint64(len(encoded)) {
			t.Fatalf("estimate is not conservative: minimum=%d ok=%t encoded=%q err=%v", minimum, ok, encoded, err)
		}
	}
}

func TestBatchResponseCompactionPreservesRecoveryRecords(t *testing.T) {
	record, err := agent.NewActionRecoveryRecord(&agent.ActionOutcomeError{
		OperationOrdinal: 7, Action: "click", TargetID: "tab-1", BrowserInstanceID: "browser-fingerprint", Outcome: agent.OutcomeUnknown,
		ExpectedPostcondition: &agent.Postcondition{Text: strings.Repeat("needle", 120)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := agent.BatchResult{Total: 1, Failed: 1, Unknown: 1, Results: []agent.BatchActionResult{{
		Index: 1, Action: "click", Outcome: string(agent.OutcomeUnknown), RecoveryRecord: &record,
		Evidence: strings.Repeat("verbose evidence ", 20_000), Target: strings.Repeat("target ", 4_000),
	}}}
	structured, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(structured, &payload); err != nil {
		t.Fatal(err)
	}
	response := &protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: mcpserver.StructuredResult{
		Content:           []mcpserver.Content{{Type: "text", Text: strings.Repeat("verbose mirror ", 20_000)}},
		StructuredContent: payload,
	}}
	handler := boundedToolResponseMiddleware()(func(context.Context, *protocol.Request) (*protocol.Response, error) { return response, nil })
	response, err = handler(context.Background(), &protocol.Request{Method: "tools/call", Params: json.RawMessage(`{"name":"batch"}`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxMCPToolResponseBytes {
		t.Fatalf("compacted response exceeds cap: %d", len(encoded))
	}
	var got mcpserver.StructuredResult
	encodedResult, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedResult, &got); err != nil {
		t.Fatal(err)
	}
	var gotBatch agent.BatchResult
	gotBytes, _ := json.Marshal(got.StructuredContent)
	if err := json.Unmarshal(gotBytes, &gotBatch); err != nil {
		t.Fatal(err)
	}
	if !gotBatch.Truncated || len(gotBatch.Results) != 1 || gotBatch.Results[0].RecoveryRecord == nil {
		t.Fatalf("compaction lost batch state or recovery: %+v", gotBatch)
	}
	if err := gotBatch.Results[0].RecoveryRecord.Validate(); err != nil {
		t.Fatalf("recovery record invalid after compaction: %v", err)
	}
}

func TestPlaybookResponseCompactionKeepsNewestReceipt(t *testing.T) {
	receipts := make([]agent.PlaybookStepReceipt, 600)
	for i := range receipts {
		receipts[i] = agent.PlaybookStepReceipt{
			Step: i + 1,
			ActionReceipt: agent.ActionReceipt{
				Target:   "target",
				Evidence: strings.Repeat("verbose evidence ", 200),
			},
		}
	}
	result := agent.PlaybookResult{
		StepsRun: len(receipts), TotalSteps: len(receipts), StepReceipts: receipts,
		PlaybookSHA256: strings.Repeat("a", 64), ResumeSafe: true,
	}
	structured, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(structured, &payload); err != nil {
		t.Fatal(err)
	}
	response := &protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: mcpserver.StructuredResult{
		Content:           []mcpserver.Content{{Type: "text", Text: "playbook complete"}},
		StructuredContent: payload,
	}}
	compactPlaybookToolResponse(response)
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxMCPBatchResponseBytes {
		t.Fatalf("compacted response exceeds cap: %d", len(encoded))
	}
	var gotStructured mcpserver.StructuredResult
	resultBytes, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resultBytes, &gotStructured); err != nil {
		t.Fatal(err)
	}
	gotBytes, err := json.Marshal(gotStructured.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got agent.PlaybookResult
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	if !got.StepReceiptsTruncated || len(got.StepReceipts) == 0 {
		t.Fatalf("expected explicit truncation and retained receipts, got %+v", got)
	}
	if got.StepReceipts[len(got.StepReceipts)-1].Step != len(receipts) {
		t.Fatalf("newest receipt was not retained: last step=%d, want %d", got.StepReceipts[len(got.StepReceipts)-1].Step, len(receipts))
	}
}

func TestBatchErrorCompactionPreservesPartialRecoveryRecords(t *testing.T) {
	record, err := agent.NewActionRecoveryRecord(&agent.ActionOutcomeError{
		OperationOrdinal: 9, Action: "click", TargetID: "tab-2", BrowserInstanceID: "browser-fingerprint", Outcome: agent.OutcomeUnknown,
		ExpectedPostcondition: &agent.Postcondition{URLContains: "/saved"},
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := agent.BatchResult{Total: 100, Failed: 1, Unknown: 1, Results: []agent.BatchActionResult{{
		Index: 1, Action: "click", Outcome: string(agent.OutcomeUnknown), RecoveryRecord: &record,
		Evidence: strings.Repeat("verbose ", 40_000),
	}}}
	envelope := MCPErrorEnvelope{Code: "SCOUT_OPERATION_INTERRUPTED", Message: "stopped", PartialResult: batch}
	envelopeJSON, _ := json.Marshal(envelope)
	response := &protocol.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: mcpserver.StructuredResult{
		Content: []mcpserver.Content{{Type: "text", Text: "SCOUT_ERROR " + string(envelopeJSON)}}, IsError: true,
	}}
	handler := boundedToolResponseMiddleware()(func(context.Context, *protocol.Request) (*protocol.Response, error) { return response, nil })
	response, err = handler(context.Background(), &protocol.Request{Method: "tools/call", Params: json.RawMessage(`{"name":"batch"}`)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxMCPToolResponseBytes {
		t.Fatalf("compacted error exceeds cap: %d", len(encoded))
	}
	var result mcpserver.StructuredResult
	encodedResult, _ := json.Marshal(response.Result)
	if err := json.Unmarshal(encodedResult, &result); err != nil {
		t.Fatal(err)
	}
	const prefix = "SCOUT_ERROR "
	if !strings.HasPrefix(result.Content[0].Text, prefix) {
		t.Fatalf("unexpected compact error: %q", result.Content[0].Text)
	}
	var compacted MCPErrorEnvelope
	if err := json.Unmarshal([]byte(strings.TrimPrefix(result.Content[0].Text, prefix)), &compacted); err != nil {
		t.Fatal(err)
	}
	partialJSON, _ := json.Marshal(compacted.PartialResult)
	var got agent.BatchResult
	if err := json.Unmarshal(partialJSON, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Results) != 1 || got.Results[0].RecoveryRecord == nil {
		t.Fatalf("lost partial recovery: %+v", got)
	}
	if err := got.Results[0].RecoveryRecord.Validate(); err != nil {
		t.Fatalf("invalid preserved record: %v", err)
	}
}

func TestBoundedRecentRequests(t *testing.T) {
	requests := make([]agent.NetworkCapture, 750)
	for i := range requests {
		requests[i].URL = string(rune(i + 1))
	}
	out, total, offset, limit, truncated := boundedRecentRequests(requests, 500, 900)
	if len(out) != 3 || total != 750 || offset != 500 || limit != 3 || !truncated || out[0].URL != requests[500].URL || out[len(out)-1].URL != requests[502].URL {
		t.Fatalf("unexpected bounded recent requests result: len=%d total=%d offset=%d limit=%d truncated=%t", len(out), total, offset, limit, truncated)
	}
	lastPage, _, lastOffset, _, lastTruncated := boundedRecentRequests(requests, 749, 20)
	if len(lastPage) != 1 || lastOffset != 749 || lastTruncated || lastPage[0].URL != requests[749].URL {
		t.Fatalf("unexpected last page: len=%d offset=%d truncated=%t", len(lastPage), lastOffset, lastTruncated)
	}
}

// TestOutputSchemasGenerate guards every type advertised via ToolBuilder.OutputSchema.
//
// OutputSchema runs schema.Generate at registration time; if it errors the
// ToolBuilder silently drops the tool. This test fails loudly instead, so a
// type that becomes unrepresentable (e.g. an added field with no JSON schema
// mapping) is caught in CI rather than silently removing a production tool.
func TestOutputSchemasGenerate(t *testing.T) {
	cases := []struct {
		name string
		val  any
	}{
		// Newly advertised data-tool output types.
		{"network_requests", NetworkRequestsResult{}},
		{"web_vitals", agent.WebVitalsResult{}},
		{"console_errors", agent.DiagnosticsResult{}},
		{"check_readiness", agent.PageReadiness{}},
		{"discover_form", agent.FormDiscoveryResult{}},
		{"failed_requests", FailedRequestsResult{}},
		{"list_tabs", ListTabsResult{}},
		{"tab_info", agent.TabInfo{}},
		{"list_sessions", SessionSummary{}},
		{"status", agent.SessionStatus{}},
		{"cookies_list", CookiesListResult{}},
		// Pre-existing advertised types — kept here so the guard covers the
		// full set of production output schemas.
		{"observe", agent.Observation{}},
		{"content_result", agent.ContentResult{}},
		{"discover_form", DiscoverFormResult{}},
		{"annotated_screenshot", AnnotatedScreenshotResult{}},
		{"hybrid_observe", HybridObserveResult{}},
		{"capture", CaptureResult{}},
		{"artifact_reference", agent.ArtifactReference{}},
		{"screen_recording", agent.ScreenRecordingResult{}},
		{"trace", agent.TraceResult{}},
		{"submit_outcome", agent.SubmitOutcome{}},
		{"submit_form_result", agent.SubmitFormResult{}},
		{"wait_for_navigation", agent.NavigationOutcome{}},
		{"aria_violations", agent.AriaViolationReport{}},
		{"network_summary", agent.NetworkSummary{}},
		{"navigate_input", NavigateInput{}},
		{"click_input", ClickInput{}},
		{"dispatch_event_input", DispatchEventInput{}},
		{"drag_drop_input", DragDropInput{}},
		{"session_recovery_input", SessionRecoveryInput{}},
		{"session_recovery_result", SessionRecoveryResult{}},
		{"batch_input", BatchInput{}},
		{"click_label_input", ClickLabelInput{}},
		{"click_handle_input", ClickHandleInput{}},
		{"click_text_input", ClickTextInput{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := schema.Generate(tc.val)
			if err != nil {
				t.Fatalf("schema.Generate(%T) failed: %v", tc.val, err)
			}
			if s == nil {
				t.Fatalf("schema.Generate(%T) returned nil schema", tc.val)
			}
		})
	}
}
