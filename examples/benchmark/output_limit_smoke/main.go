// Command output_limit_smoke exercises Scout's final MCP response budget with
// real captured network bodies and verifies the structured recovery envelope.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mcp/client"
	"go.klarlabs.de/scout/agent"
)

const maxResponseBytes = 256 * 1024

func main() {
	baseURL := "http://127.0.0.1:41732"
	if len(os.Args) > 1 {
		baseURL = os.Args[1]
	}
	if configured := strings.TrimSpace(os.Getenv("OUTPUT_LIMIT_BASE_URL")); configured != "" {
		baseURL = configured
	}
	mcpBinary := strings.TrimSpace(os.Getenv("SCOUT_MCP_BINARY"))
	command, args := "go", []string{"run", "./cmd/scout", "mcp", "serve"}
	if mcpBinary != "" {
		command, args = mcpBinary, []string{"mcp", "serve"}
	}
	transport, err := client.NewUnsafeStdioTransport(command, args...)
	fatalIf(err)
	defer transport.Close()
	c := client.New(transport)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err = c.Initialize(ctx); err != nil {
		fatalIf(err)
	}
	call := func(name string, args map[string]any) *client.ToolResult {
		result, callErr := c.CallTool(ctx, name, args)
		fatalIf(callErr)
		if result.IsError {
			msg := "unknown error"
			if len(result.Content) > 0 {
				msg = result.Content[0].Text
			}
			if !strings.Contains(msg, "SCOUT_OUTPUT_LIMIT") {
				fatalIf(fmt.Errorf("tool %s failed: %s", name, msg))
			}
		}
		return result
	}
	allowPrivate := true
	_, err = c.CallTool(ctx, "configure", map[string]any{"allow_private_ips": allowPrivate})
	fatalIf(err)
	call("navigate", map[string]any{"url": baseURL + "/text-output-stress"})
	extracted := call("extract_all", map[string]any{"selector": ".large-output"})
	var extractedPage struct {
		Count         int      `json:"count"`
		Total         int      `json:"total"`
		Items         []string `json:"items"`
		ItemLengths   []int    `json:"item_lengths"`
		ItemTruncated []bool   `json:"item_truncated"`
		Truncated     bool     `json:"truncated"`
	}
	if err = json.Unmarshal(toolJSON(extracted), &extractedPage); err != nil {
		fatalIf(err)
	}
	if extracted.IsError || extractedPage.Count != 8 || extractedPage.Total != 12 || len(extractedPage.ItemLengths) != 8 || len(extractedPage.ItemTruncated) != 8 || !extractedPage.Truncated {
		fatalIf(fmt.Errorf("unexpected bounded extraction metadata: %+v", extractedPage))
	}
	for i, item := range extractedPage.Items {
		if len([]rune(item)) != 4000 || extractedPage.ItemLengths[i] <= 4000 || !extractedPage.ItemTruncated[i] {
			fatalIf(fmt.Errorf("item %d was not bounded with accurate original-length metadata: text=%d original=%d truncated=%t", i, len([]rune(item)), extractedPage.ItemLengths[i], extractedPage.ItemTruncated[i]))
		}
	}
	boundedBytes, err := json.Marshal(extracted)
	fatalIf(err)
	if len(boundedBytes) > maxResponseBytes {
		fatalIf(fmt.Errorf("bounded extraction response is %d bytes", len(boundedBytes)))
	}
	overflow := call("benchmark_oversized_output", map[string]any{})
	overflowBytes, err := json.Marshal(overflow)
	fatalIf(err)
	overflowText := ""
	if len(overflow.Content) > 0 {
		overflowText = overflow.Content[0].Text
	}
	if !overflow.IsError || len(overflowBytes) > maxResponseBytes || !strings.Contains(overflowText, "SCOUT_OUTPUT_LIMIT") || !strings.Contains(overflowText, "output_omitted") {
		fatalIf(fmt.Errorf("unexpected live output-limit replacement: is_error=%t bytes=%d text=%q", overflow.IsError, len(overflowBytes), overflowText))
	}
	call("navigate", map[string]any{"url": baseURL + "/large-batch-form"})
	fields := make(map[string]string, 23)
	for i := 0; i < 23; i++ {
		fields[fmt.Sprintf("Bulk field %02d", i)] = strings.Repeat("v", 4200)
	}
	batchResult, batchErr := c.CallTool(ctx, "batch", map[string]any{
		"timeout_ms": 3000,
		"actions": []map[string]any{
			{"action": "fill_form_semantic", "fields": fields},
			{"action": "click", "selector": "#batch-timeout", "expected_text": "Late batch completion"},
		},
	})
	fatalIf(batchErr)
	batchBytes, err := json.Marshal(batchResult)
	fatalIf(err)
	if !batchResult.IsError || len(batchBytes) > maxResponseBytes || len(batchResult.Content) == 0 {
		fatalIf(fmt.Errorf("unexpected oversized batch result: is_error=%t bytes=%d content=%d payload=%s", batchResult.IsError, len(batchBytes), len(batchResult.Content), batchBytes))
	}
	const scoutErrorPrefix = "SCOUT_ERROR "
	batchText := batchResult.Content[0].Text
	if !strings.HasPrefix(batchText, scoutErrorPrefix) {
		fatalIf(fmt.Errorf("oversized batch did not retain structured error envelope: %q", batchText[:min(200, len(batchText))]))
	}
	var batchError struct {
		Code          string `json:"code"`
		PartialResult struct {
			Total     int  `json:"total"`
			Unknown   int  `json:"unknown"`
			Truncated bool `json:"truncated"`
			Results   []struct {
				RecoveryRecord *agent.ActionRecoveryRecord `json:"recovery_record"`
			} `json:"results"`
		} `json:"partial_result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(batchText, scoutErrorPrefix)), &batchError); err != nil {
		fatalIf(fmt.Errorf("decode compacted batch error: %w", err))
	}
	if batchError.Code != "SCOUT_OPERATION_INTERRUPTED" || batchError.PartialResult.Total != 2 || batchError.PartialResult.Unknown != 1 || !batchError.PartialResult.Truncated || len(batchError.PartialResult.Results) == 0 {
		fatalIf(fmt.Errorf("unexpected compacted batch summary: %+v", batchError))
	}
	var recovery *agent.ActionRecoveryRecord
	for _, step := range batchError.PartialResult.Results {
		if step.RecoveryRecord != nil {
			recovery = step.RecoveryRecord
		}
	}
	if recovery == nil {
		fatalIf(fmt.Errorf("compacted batch error lost its recovery record"))
	}
	if err := recovery.Validate(); err != nil {
		fatalIf(fmt.Errorf("compacted recovery record is invalid: %w", err))
	}
	call("navigate", map[string]any{"url": baseURL + "/text-output-stress"})
	snapshot := call("save_page_snapshot", map[string]any{"format": "readable_text", "max_chars": 400000})
	var saved struct {
		Artifact struct {
			Path      string `json:"path"`
			SizeBytes int64  `json:"size_bytes"`
			SHA256    string `json:"sha256"`
		} `json:"artifact"`
		TotalChars int  `json:"total_chars"`
		SavedChars int  `json:"saved_chars"`
		Truncated  bool `json:"truncated"`
	}
	if err = json.Unmarshal(toolJSON(snapshot), &saved); err != nil {
		fatalIf(err)
	}
	if saved.Artifact.Path == "" || saved.Artifact.SizeBytes < 300000 || saved.Artifact.SHA256 == "" || saved.TotalChars != saved.SavedChars || saved.Truncated {
		fatalIf(fmt.Errorf("unexpected saved snapshot metadata: %+v", saved))
	}
	if info, statErr := os.Stat(saved.Artifact.Path); statErr != nil || info.Mode().Perm() != 0o600 {
		fatalIf(fmt.Errorf("snapshot file mode is not private: info=%v error=%v", info, statErr))
	}
	type excerpt struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		Offset    int    `json:"offset"`
		Length    int    `json:"length"`
		Total     int    `json:"total_chars"`
		Truncated bool   `json:"truncated"`
	}
	var excerpts []excerpt
	for _, offset := range []int{0, 20000} {
		result := call("read_page_snapshot", map[string]any{"path": saved.Artifact.Path, "offset": offset, "limit": 20000})
		var part excerpt
		if err = json.Unmarshal(toolJSON(result), &part); err != nil {
			fatalIf(err)
		}
		if part.Path != saved.Artifact.Path || part.Offset != offset || part.Length != 20000 || part.Total != saved.TotalChars || !part.Truncated {
			fatalIf(fmt.Errorf("unexpected snapshot range metadata: %+v", part))
		}
		excerpts = append(excerpts, part)
	}
	if !strings.Contains(excerpts[0].Content, "item-00") || strings.Contains(excerpts[1].Content, "item-00") {
		fatalIf(fmt.Errorf("snapshot range paging returned unexpected content"))
	}
	search := call("search_page_snapshot", map[string]any{"path": saved.Artifact.Path, "query": "ITEM-07", "context_chars": 40})
	var found struct {
		Path    string `json:"path"`
		Query   string `json:"query"`
		Matches []struct {
			Offset int    `json:"offset"`
			Text   string `json:"text"`
		} `json:"matches"`
		TotalMatches int  `json:"total_matches"`
		TotalChars   int  `json:"total_chars"`
		Truncated    bool `json:"truncated"`
	}
	if err = json.Unmarshal(toolJSON(search), &found); err != nil {
		fatalIf(err)
	}
	if found.Path != saved.Artifact.Path || found.Query != "ITEM-07" || found.TotalMatches != 1 || len(found.Matches) != 1 || !strings.Contains(found.Matches[0].Text, "item-07") || found.TotalChars != saved.TotalChars || found.Truncated {
		fatalIf(fmt.Errorf("unexpected snapshot search result: %+v", found))
	}
	_, unsafeErr := c.CallTool(ctx, "read_page_snapshot", map[string]any{"path": "/etc/passwd", "offset": 0, "limit": 100})
	if unsafeErr == nil {
		fatalIf(fmt.Errorf("read_page_snapshot accepted a path outside its registered artifacts"))
	}
	call("delete_page_snapshot", map[string]any{"path": saved.Artifact.Path})
	if _, err = os.Stat(saved.Artifact.Path); !os.IsNotExist(err) {
		fatalIf(fmt.Errorf("snapshot artifact was not deleted: %v", err))
	}
	call("navigate", map[string]any{"url": baseURL + "/network-stress"})
	call("enable_network_capture", map[string]any{})
	call("click", map[string]any{"selector": "#load"})
	deadline := time.Now().Add(15 * time.Second)
	var readiness *client.ToolResult
	for time.Now().Before(deadline) {
		readiness = call("network_requests", map[string]any{"max_recent": 3})
		var state struct {
			Total int `json:"total"`
		}
		_ = json.Unmarshal(toolJSON(readiness), &state)
		if state.Total == 12 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	type request struct {
		URL          string `json:"url"`
		ResponseBody string `json:"response_body"`
	}
	type networkPage struct {
		Requests  []request `json:"requests"`
		Count     int       `json:"count"`
		Total     int       `json:"total"`
		Offset    int       `json:"offset"`
		Truncated bool      `json:"truncated"`
	}
	var allBodies []request
	var pageResults []*client.ToolResult
	for offset := 0; offset < 12; offset += 3 {
		page := call("network_requests", map[string]any{"offset": offset, "max_recent": 3})
		pageResults = append(pageResults, page)
		var decoded networkPage
		if err = json.Unmarshal(toolJSON(page), &decoded); err != nil {
			fatalIf(err)
		}
		expectedCount := 3
		if offset == 9 {
			expectedCount = 3
		}
		if decoded.Count != expectedCount || decoded.Total != 12 || decoded.Offset != offset || decoded.Truncated != (offset+expectedCount < 12) {
			fatalIf(fmt.Errorf("unexpected network page at offset %d: count=%d total=%d offset=%d truncated=%t", offset, decoded.Count, decoded.Total, decoded.Offset, decoded.Truncated))
		}
		allBodies = append(allBodies, decoded.Requests...)
	}
	for i, request := range allBodies {
		if !strings.Contains(request.URL, "/network-body?id=") || len(request.ResponseBody) < 27_000 {
			fatalIf(fmt.Errorf("request %d body was not fully retrievable: url=%s bytes=%d", i, request.URL, len(request.ResponseBody)))
		}
	}
	maxPageBytes := 0
	for i, result := range pageResults {
		encodedResult, marshalErr := json.Marshal(result)
		fatalIf(marshalErr)
		if len(encodedResult) > maxResponseBytes {
			fatalIf(fmt.Errorf("page %d response is %d bytes, over %d", i, len(encodedResult), maxResponseBytes))
		}
		if len(encodedResult) > maxPageBytes {
			maxPageBytes = len(encodedResult)
		}
	}

	fmt.Printf("output_limit_smoke=passed extracted_items=%d extracted_chars_per_item=%d returned_bytes=%d overflow_is_error=%t overflow_replacement_bytes=%d compacted_batch_error_bytes=%d batch_recovery_valid=%t snapshot_chars=%d snapshot_ranges=%d search_matches=%d paged_requests=%d paged_responses=%d max_page_bytes=%d\n", extractedPage.Count, len([]rune(extractedPage.Items[0])), len(boundedBytes), overflow.IsError, len(overflowBytes), len(batchBytes), recovery != nil, saved.SavedChars, len(excerpts), found.TotalMatches, len(allBodies), len(pageResults), maxPageBytes)
}

func toolJSON(result *client.ToolResult) []byte {
	if len(result.StructuredContent) > 0 {
		return result.StructuredContent
	}
	if len(result.Content) > 0 {
		return []byte(result.Content[0].Text)
	}
	return nil
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
