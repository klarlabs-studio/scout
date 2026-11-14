// Command benchmark runs Scout's local, deterministic browser reliability
// fixtures and writes one JSON report to stdout.
//
// Run with: go run ./examples/benchmark
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/mcp/client"
	"go.klarlabs.de/mcp/protocol"
	"go.klarlabs.de/scout/agent"
	benchmarkfixture "go.klarlabs.de/scout/examples/benchmark/fixture"
	cdpclient "go.klarlabs.de/scout/internal/cdp"
	"go.klarlabs.de/scout/internal/launcher"
)

const benchmarkVersion = "scout-browser-reliability-v28"

//go:embed fixtures/v28.json
var fixtureJSON []byte

type fixtureSet struct {
	Version   string            `json:"version"`
	Scenarios []fixtureScenario `json:"scenarios"`
}

type fixtureScenario struct {
	ID              string `json:"id"`
	RequiredScope   string `json:"required_scope"`
	ExpectedText    string `json:"expected_text"`
	ExpectedValue   string `json:"expected_value"`
	ExpectedInputID string `json:"expected_input_id"`
	ForbiddenLabel  string `json:"forbidden_label"`
	ForbiddenText   string `json:"forbidden_text"`
	MaxOutputBytes  int    `json:"max_output_bytes"`
	Selector        string `json:"selector"`
	FormSelector    string `json:"form_selector"`
	MatchURL        string `json:"match_url"`
	ExpectedStatus  int    `json:"expected_status"`
	ExpectedMiss    bool   `json:"expected_miss"`
	ExpectedFailure string `json:"expected_failure_outcome"`
	StartingPath    string `json:"starting_path"`
	ActionSelector  string `json:"action_selector"`
	ChangedText     string `json:"changed_expected_text"`
	RepairSelector  string `json:"repair_selector"`
	FrameSelector   string `json:"frame_selector"`
}

type scenarioResult struct {
	ID                    string                     `json:"id"`
	Success               bool                       `json:"success"`
	Verified              bool                       `json:"verified"`
	Outcome               string                     `json:"outcome,omitempty"`
	ApplicationAccepted   *bool                      `json:"application_accepted,omitempty"`
	Target                string                     `json:"target,omitempty"`
	ActionCalls           int                        `json:"action_calls"`
	OutputBytes           int                        `json:"output_bytes"`
	ExpectedBytesMax      int                        `json:"expected_output_bytes_max,omitempty"`
	FalseSuccess          bool                       `json:"false_success"`
	NaiveFalseSuccess     bool                       `json:"naive_false_success,omitempty"`
	Classification        string                     `json:"classification,omitempty"`
	Error                 string                     `json:"error,omitempty"`
	Evidence              string                     `json:"evidence,omitempty"`
	RecoveryHint          string                     `json:"recovery_hint,omitempty"`
	VerificationState     string                     `json:"verification_state,omitempty"`
	Value                 string                     `json:"value,omitempty"`
	ValueCommitted        string                     `json:"value_committed,omitempty"`
	ResponseObserved      *bool                      `json:"response_observed,omitempty"`
	StatusCode            int                        `json:"status_code,omitempty"`
	DurationMicros        int64                      `json:"duration_us,omitempty"`
	SerializedResults     map[string]json.RawMessage `json:"serialized_tool_results,omitempty"`
	SerializedResultsKind string                     `json:"serialized_tool_results_kind,omitempty"`
	PrivacySentinelLeaked bool                       `json:"privacy_sentinel_leaked,omitempty"`
}

type report struct {
	Version           string           `json:"benchmark_version"`
	Scenarios         []scenarioResult `json:"scenarios"`
	Completed         int              `json:"completed"`
	Total             int              `json:"total"`
	FalseSuccess      int              `json:"false_successes"`
	NaiveFalseSuccess int              `json:"naive_false_successes"`
}

type benchmarkSessionSummary struct {
	Name             string `json:"name"`
	SessionID        string `json:"session_id"`
	Started          bool   `json:"started"`
	BrowserAlive     bool   `json:"browser_alive"`
	BrowserOwnership string `json:"browser_ownership"`
	BrowserEndpoint  string `json:"browser_endpoint"`
	EndpointSource   string `json:"endpoint_source"`
	SessionDead      bool   `json:"session_dead"`
	CurrentURL       string `json:"current_url"`
	Active           bool   `json:"active"`
	IdleTimeoutMS    int64  `json:"idle_timeout_ms"`
	LastActivity     string `json:"last_activity"`
	IdleDeadline     string `json:"idle_deadline"`
	InFlightCalls    int    `json:"in_flight_calls"`
	StatusDeferred   bool   `json:"status_deferred"`
}

type benchmarkSessionCaptureTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	id   string
}

func (t *benchmarkSessionCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			t.mu.Lock()
			t.id = id
			t.mu.Unlock()
		}
	}
	return resp, err
}

func (t *benchmarkSessionCaptureTransport) sessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id
}

func buildBenchmarkScoutCLI(ctx context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp("", "scout-benchmark-cli-*")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "scout")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, "./cmd/scout")
	if output, buildErr := cmd.CombinedOutput(); buildErr != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("compile ./cmd/scout: %w: %s", buildErr, strings.TrimSpace(string(output)))
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runSubmitParity(id, url string, fixture fixtureScenario, baseline *agent.SubmitFormResult) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "submit", url, fixture.FormSelector,
		"--match-url="+fixture.MatchURL, "--acceptance-text="+fixture.ExpectedText, "--timeout-ms=3000", "--headless")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	var cliResult agent.SubmitFormResult
	parseErr := json.Unmarshal(stdout, &cliResult)
	matches := parseErr == nil && cliResult.TargetID != "" && cliResult.Submitted == baseline.Submitted && cliResult.Verified == baseline.Verified && cliResult.ResponseObserved == baseline.ResponseObserved && cliResult.ApplicationAccepted == baseline.ApplicationAccepted && cliResult.Outcome == baseline.Outcome && cliResult.FormSelector == baseline.FormSelector && cliResult.RequestMethod == baseline.RequestMethod && cliResult.StatusCode == baseline.StatusCode && cliResult.Evidence == baseline.Evidence
	observed, accepted := cliResult.ResponseObserved, cliResult.ApplicationAccepted
	result := scenarioResult{ID: id, Success: matches, Verified: matches, Outcome: cliResult.Outcome, ApplicationAccepted: &accepted, ResponseObserved: &observed, StatusCode: cliResult.StatusCode, Target: cliResult.FormSelector, ActionCalls: 1, OutputBytes: len(stdout), Evidence: cliResult.Evidence, RecoveryHint: cliResult.RecoveryHint}
	if runErr != nil {
		result.Error = runErr.Error()
		if ctx.Err() != nil {
			result.Error = ctx.Err().Error()
		}
		if stderr.Len() > 0 {
			result.Error += ": " + strings.TrimSpace(stderr.String())
		}
	} else if parseErr != nil {
		result.Error = fmt.Sprintf("decode CLI form result: %v", parseErr)
	} else if !matches {
		result.Error = "CLI form result fields differ from Go baseline"
	}
	return result
}

func mcpCall(ctx context.Context, c *client.Client, name string, args map[string]any) (*client.ToolResult, error) {
	result, err := c.CallTool(ctx, name, args)
	if err != nil {
		return nil, err
	}
	if result.IsError {
		message := "MCP tool returned an error"
		if len(result.Content) > 0 {
			message = result.Content[0].Text
		}
		return result, fmt.Errorf("%s: %s", name, message)
	}
	return result, nil
}

func mcpRawCall(ctx context.Context, transport client.Transport, id int64, name string, args map[string]any) (*protocol.Response, error) {
	return mcpRawCallWithMeta(ctx, transport, id, name, args, nil)
}

func mcpRawCallWithMeta(ctx context.Context, transport client.Transport, id int64, name string, args map[string]any, meta map[string]any) (*protocol.Response, error) {
	call := map[string]any{"name": name, "arguments": args}
	if meta != nil {
		call["_meta"] = meta
	}
	params, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}
	requestID, _ := json.Marshal(id)
	return transport.Send(ctx, &protocol.Request{JSONRPC: "2.0", ID: requestID, Method: "tools/call", Params: params})
}

func mcpRawResultJSON(response *protocol.Response) ([]byte, error) {
	if response == nil || response.Error != nil {
		if response == nil {
			return nil, errors.New("nil MCP response")
		}
		return nil, response.Error
	}
	result, ok := response.Result.(map[string]any)
	if !ok || result["isError"] == true {
		return nil, fmt.Errorf("MCP tool returned an error: %#v", response.Result)
	}
	if structured, ok := result["structuredContent"]; ok {
		return json.Marshal(structured)
	}
	if contents, ok := result["content"].([]any); ok && len(contents) > 0 {
		if content, ok := contents[0].(map[string]any); ok {
			if text, ok := content["text"].(string); ok {
				return []byte(text), nil
			}
		}
	}
	return nil, errors.New("MCP tool response omitted structured result")
}

func inspectMCPErrorResponse(response *protocol.Response) (struct {
	Code string `json:"code"`
}, error) {
	var envelope struct {
		Code string `json:"code"`
	}
	result, ok := response.Result.(map[string]any)
	if !ok || result["isError"] != true {
		return envelope, fmt.Errorf("wire result did not set isError=true")
	}
	meta, ok := result["_meta"].(map[string]any)
	if !ok {
		return envelope, fmt.Errorf("wire result omitted _meta: %#v", result)
	}
	raw, ok := meta["go.klarlabs.de/scout/error"]
	if !ok {
		return envelope, fmt.Errorf("wire result omitted Scout error metadata")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return envelope, err
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func mcpToolHasInputProperty(tools []client.ToolInfo, toolName, property string) bool {
	for _, tool := range tools {
		if tool.Name != toolName {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			return false
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return false
		}
		_, ok = properties[property]
		return ok
	}
	return false
}

func mcpResultJSON(result *client.ToolResult) []byte {
	if result == nil {
		return nil
	}
	if len(result.StructuredContent) > 0 {
		return result.StructuredContent
	}
	if len(result.Content) > 0 {
		return []byte(result.Content[0].Text)
	}
	return nil
}

func runMCPActionParity(ctx context.Context, c *client.Client, id, url, action string, fixture fixtureScenario, baseline any) scenarioResult {
	nav, err := mcpCall(ctx, c, "navigate", map[string]any{"url": url})
	if err != nil {
		return scenarioResult{ID: id, ActionCalls: 1, Error: err.Error()}
	}
	var navResult agent.PageResult
	if decodeErr := json.Unmarshal(mcpResultJSON(nav), &navResult); decodeErr != nil || navResult.TargetID == "" {
		return scenarioResult{ID: id, ActionCalls: 1, Error: fmt.Sprintf("MCP navigation omitted target_id or could not be decoded: %v", decodeErr)}
	}
	var args map[string]any
	toolName := action
	switch action {
	case "click":
		args = map[string]any{"selector": fixture.Selector, "expected_text": fixture.ExpectedText}
	case "type":
		args = map[string]any{"selector": fixture.Selector, "text": fixture.ExpectedValue}
	case "submit":
		toolName = "submit_form"
		args = map[string]any{"selector": fixture.FormSelector, "match_url": fixture.MatchURL, "timeout_ms": 3000, "acceptance_text": fixture.ExpectedText}
	default:
		return scenarioResult{ID: id, ActionCalls: 2, Error: "unsupported MCP parity action " + action}
	}
	toolResult, err := mcpCall(ctx, c, toolName, args)
	if err != nil {
		return scenarioResult{ID: id, ActionCalls: 2, Error: err.Error()}
	}
	var matches bool
	var value, valueCommitted, evidence, outcome, targetID string
	verificationState := ""
	var accepted, observed *bool
	var statusCode int
	switch baselineResult := baseline.(type) {
	case *agent.PageResult:
		var got agent.PageResult
		decodeErr := json.Unmarshal(mcpResultJSON(toolResult), &got)
		targetID = got.TargetID
		matches = decodeErr == nil && got.TargetID == navResult.TargetID && got.Action == baselineResult.Action && got.Target == baselineResult.Target && got.Outcome == baselineResult.Outcome && got.Verified == baselineResult.Verified && got.Evidence == baselineResult.Evidence && got.Receipt != nil && baselineResult.Receipt != nil && got.Receipt.Verification == baselineResult.Receipt.Verification
		if decodeErr != nil {
			return scenarioResult{ID: id, ActionCalls: 2, Error: "decode MCP click result: " + decodeErr.Error()}
		}
		value, outcome, evidence = "", got.Outcome, got.Evidence
		if got.Receipt != nil {
			verificationState = string(got.Receipt.Verification)
		}
	case *agent.ElementResult:
		var got agent.ElementResult
		decodeErr := json.Unmarshal(mcpResultJSON(toolResult), &got)
		targetID = got.TargetID
		matches = decodeErr == nil && got.TargetID == navResult.TargetID && got.Selector == baselineResult.Selector && got.Action == baselineResult.Action && got.Value == baselineResult.Value && got.ValueCommitted == baselineResult.ValueCommitted && got.FrameworkReactive == baselineResult.FrameworkReactive && got.Verified == baselineResult.Verified && got.Outcome == baselineResult.Outcome && got.Evidence == baselineResult.Evidence && got.Receipt != nil && baselineResult.Receipt != nil && got.Receipt.Verification == baselineResult.Receipt.Verification
		if decodeErr != nil {
			return scenarioResult{ID: id, ActionCalls: 2, Error: "decode MCP type result: " + decodeErr.Error()}
		}
		value, valueCommitted, outcome, evidence = got.Value, got.ValueCommitted, got.Outcome, got.Evidence
		if got.Receipt != nil {
			verificationState = string(got.Receipt.Verification)
		}
	case *agent.SubmitFormResult:
		var got agent.SubmitFormResult
		decodeErr := json.Unmarshal(mcpResultJSON(toolResult), &got)
		targetID = got.TargetID
		matches = decodeErr == nil && got.TargetID == navResult.TargetID && got.Submitted == baselineResult.Submitted && got.Verified == baselineResult.Verified && got.ResponseObserved == baselineResult.ResponseObserved && got.ApplicationAccepted == baselineResult.ApplicationAccepted && got.Outcome == baselineResult.Outcome && got.FormSelector == baselineResult.FormSelector && got.RequestMethod == baselineResult.RequestMethod && got.StatusCode == baselineResult.StatusCode && got.Evidence == baselineResult.Evidence
		if decodeErr != nil {
			return scenarioResult{ID: id, ActionCalls: 2, Error: "decode MCP submit result: " + decodeErr.Error()}
		}
		value, outcome, evidence, statusCode = "", got.Outcome, got.Evidence, got.StatusCode
		if got.Receipt != nil {
			verificationState = string(got.Receipt.Verification)
		}
		accepted, observed = &got.ApplicationAccepted, &got.ResponseObserved
	default:
		return scenarioResult{ID: id, ActionCalls: 2, Error: "unsupported MCP parity baseline type"}
	}
	encoded, _ := json.Marshal(toolResult)
	if len(nav.Content) > 0 {
		encodedNav, _ := json.Marshal(nav)
		encoded = append(encoded, encodedNav...)
	}
	result := scenarioResult{ID: id, Success: matches, Verified: matches, Outcome: outcome, VerificationState: verificationState, Target: targetID, ActionCalls: 2, OutputBytes: len(encoded), Evidence: evidence, Value: value, ValueCommitted: valueCommitted, ApplicationAccepted: accepted, ResponseObserved: observed, StatusCode: statusCode}
	if !matches {
		result.Error = "MCP result fields differ from Go baseline"
	}
	return result
}

func runMCPExplicitSessionRouting(fixture fixtureScenario) scenarioResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	baseURL := server.URL
	previousPrivate, hadPrivate := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true"); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "allow local benchmark fixture: " + err.Error()}
	}
	transport, startErr := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	restoreEnvironmentValue("SCOUT_ALLOW_PRIVATE_IPS", previousPrivate, hadPrivate)
	if startErr != nil {
		return scenarioResult{ID: fixture.ID, Error: "start MCP transport: " + startErr.Error()}
	}
	defer transport.Close()
	mcp := client.New(transport)
	if _, err := mcp.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize MCP client: " + err.Error()}
	}
	calls, outputBytes := 1, 0
	call := func(name string, args map[string]any) (*client.ToolResult, error) {
		calls++
		result, err := mcpCall(ctx, mcp, name, args)
		if err == nil {
			outputBytes += len(mcpResultJSON(result))
		}
		return result, err
	}
	if _, err := call("create_session", map[string]any{"name": "left"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "create left session: " + err.Error()}
	}
	if _, err := call("create_session", map[string]any{"name": "right"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "create right session: " + err.Error()}
	}
	listed, err := call("list_sessions", map[string]any{})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "list session IDs: " + err.Error()}
	}
	var sessionSummaries []benchmarkSessionSummary
	if err := json.Unmarshal(mcpResultJSON(listed), &sessionSummaries); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "decode session IDs: " + err.Error()}
	}
	sessionIDs := map[string]string{}
	for _, summary := range sessionSummaries {
		sessionIDs[summary.Name] = summary.SessionID
	}
	if !strings.HasPrefix(sessionIDs["left"], "scs:") || len(sessionIDs["left"]) != 36 || !strings.HasPrefix(sessionIDs["right"], "scs:") || len(sessionIDs["right"]) != 36 {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: fmt.Sprintf("list_sessions omitted opaque IDs: left=%q right=%q", sessionIDs["left"], sessionIDs["right"])}
	}
	if _, err := call("switch_session", map[string]any{"name": "default"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "restore implicit default session: " + err.Error()}
	}
	rawCall := func(id int64, tool string, args map[string]any, session string) (*protocol.Response, error) {
		calls++
		response, err := mcpRawCallWithMeta(ctx, transport, id, tool, args, map[string]any{"go.klarlabs.de/scout/session": session})
		if err == nil && response != nil && response.Error == nil {
			if raw, marshalErr := json.Marshal(response.Result); marshalErr == nil {
				outputBytes += len(raw)
			}
		}
		return response, err
	}
	var leftNav, rightNav *protocol.Response
	var leftErr, rightErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		leftNav, leftErr = rawCall(101, "navigate", map[string]any{"url": baseURL + "/session-owner/seed"}, sessionIDs["left"])
	}()
	go func() {
		defer wg.Done()
		rightNav, rightErr = rawCall(102, "navigate", map[string]any{"url": baseURL + "/session-owner/probe"}, sessionIDs["right"])
	}()
	wg.Wait()
	if leftErr != nil || rightErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("concurrent explicit navigation failed: left=%v right=%v", leftErr, rightErr)}
	}
	decodeRaw := func(response *protocol.Response, destination any) error {
		if response == nil {
			return errors.New("empty MCP response")
		}
		if response.Error != nil {
			return fmt.Errorf("MCP error %d: %s", response.Error.Code, response.Error.Message)
		}
		toolResult, ok := response.Result.(map[string]any)
		if !ok {
			return fmt.Errorf("unexpected MCP tool result %T", response.Result)
		}
		if toolResult["isError"] == true {
			return fmt.Errorf("MCP tool returned error: %#v", toolResult["content"])
		}
		if structured, ok := toolResult["structuredContent"]; ok {
			return remarshalJSON(structured, destination)
		}
		content, ok := toolResult["content"].([]any)
		if !ok || len(content) == 0 {
			return errors.New("MCP tool result has no content")
		}
		first, ok := content[0].(map[string]any)
		if !ok {
			return errors.New("MCP tool content is malformed")
		}
		payload, ok := first["text"].(string)
		if !ok {
			return errors.New("MCP tool content has no text payload")
		}
		return json.Unmarshal([]byte(payload), destination)
	}
	var leftPage, rightPage agent.PageResult
	if err := decodeRaw(leftNav, &leftPage); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "decode left navigation: " + err.Error()}
	}
	if err := decodeRaw(rightNav, &rightPage); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "decode right navigation: " + err.Error()}
	}
	if leftPage.TargetID == "" || rightPage.TargetID == "" || leftPage.TargetID == rightPage.TargetID {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: fmt.Sprintf("explicit sessions did not return distinct targets: left=%q right=%q", leftPage.TargetID, rightPage.TargetID)}
	}
	readState := func(id int64, session string) (agent.ElementResult, error) {
		response, err := rawCall(id, "extract", map[string]any{"selector": "#state"}, session)
		if err != nil {
			return agent.ElementResult{}, err
		}
		var state agent.ElementResult
		if err := decodeRaw(response, &state); err != nil {
			return state, err
		}
		return state, nil
	}
	leftState, leftStateErr := readState(103, sessionIDs["left"])
	rightState, rightStateErr := readState(104, sessionIDs["right"])
	if leftStateErr != nil || rightStateErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("read explicit session state: left=%v right=%v", leftStateErr, rightStateErr)}
	}
	implicitRaw, implicitErr := call("status", map[string]any{})
	var implicit struct {
		ActiveTargetID string `json:"active_target_id"`
	}
	if implicitErr == nil {
		implicitErr = json.Unmarshal(mcpResultJSON(implicitRaw), &implicit)
	}
	passed := implicitErr == nil && leftState.TargetID == leftPage.TargetID && leftState.Text == `["scout_owner=primary","primary"]` && rightState.TargetID == rightPage.TargetID && rightState.Text == `["",null]` && implicit.ActiveTargetID == ""
	evidence := fmt.Sprintf("concurrent_selector_calls=true; distinct_targets=%t; left_primary_state_retained=%t; right_context_started_empty=%t; implicit_default_unstarted=%t", leftPage.TargetID != rightPage.TargetID, leftState.Text == `["scout_owner=primary","primary"]`, rightState.Text == `["",null]`, implicit.ActiveTargetID == "")
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "explicit_session_routing_isolated", Target: leftPage.TargetID + "/" + rightPage.TargetID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: time.Since(started).Microseconds(), Evidence: evidence}
	if !passed {
		result.Error = fmt.Sprintf("selector routing contract failed: left=%+v right=%+v implicit=%+v status_err=%v", leftState, rightState, implicit, implicitErr)
	}
	return result
}

func runRemoteDisconnectRecovery(serverURL string, dispatchObserved <-chan struct{}, fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve DevTools port: " + err.Error()}
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true, Port: port})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote fixture browser: " + err.Error()}
	}
	defer func() {
		if browser != nil {
			_ = browser.Close()
		}
	}()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, Timeout: 15 * time.Second, AllowPrivateIPs: true, RemoteCDP: endpoint})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "attach through HTTP DevTools endpoint: " + err.Error()}
	}
	defer func() { _ = session.Close() }()
	pageURL := serverURL + fixture.StartingPath
	if _, err := session.NavigateWithPostconditionContext(ctx, pageURL, agent.Postcondition{}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "navigate remote fixture: " + err.Error()}
	}
	type clickResult struct {
		result *agent.PageResult
		err    error
	}
	clickDone := make(chan clickResult, 1)
	go func() {
		result, clickErr := session.ClickWithPostconditionContext(ctx, fixture.Selector, agent.Postcondition{Text: fixture.ExpectedText}, false)
		clickDone <- clickResult{result: result, err: clickErr}
	}()
	select {
	case <-dispatchObserved:
	case <-ctx.Done():
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "page never confirmed action dispatch before deadline"}
	}
	closeErr := browser.Close()
	browser = nil
	// Closing a fixture browser deliberately kills its child process. Some
	// platforms report that expected termination as signal: killed.
	if closeErr != nil && !strings.Contains(closeErr.Error(), "signal: killed") {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "stop remote fixture browser: " + closeErr.Error()}
	}
	var clickErr error
	select {
	case got := <-clickDone:
		clickErr = got.err
	case <-ctx.Done():
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "action call did not return after browser disconnect"}
	}
	var unknown *agent.ActionOutcomeError
	unknownConfirmed := errors.As(clickErr, &unknown) && unknown.Outcome == agent.OutcomeUnknown && unknown.TargetID != ""
	status, statusErr := session.StatusContext(ctx)
	deadReported := statusErr == nil && status.SessionDead && status.Recoverable && status.BrowserOwnership == "remote_cdp"
	if statusErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Outcome: outcomeName(unknown), Error: "status after disconnect: " + statusErr.Error()}
	}
	browser, err = launcher.LaunchContext(ctx, launcher.Options{Headless: true, Port: port})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Outcome: outcomeName(unknown), Error: "restart remote fixture browser: " + err.Error()}
	}
	resetStarted := time.Now()
	if err := session.ResetContext(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Outcome: outcomeName(unknown), DurationMicros: time.Since(resetStarted).Microseconds(), RecoveryHint: status.RecoveryHint, Error: "reset after remote browser restart: " + err.Error()}
	}
	if _, err := session.NavigateWithPostconditionContext(ctx, pageURL, agent.Postcondition{}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 5, Outcome: outcomeName(unknown), DurationMicros: time.Since(resetStarted).Microseconds(), RecoveryHint: status.RecoveryHint, Error: "navigate after remote reset: " + err.Error()}
	}
	verified, err := session.ClickWithPostconditionContext(ctx, fixture.Selector, agent.Postcondition{Text: fixture.ExpectedText}, false)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 6, Outcome: outcomeName(unknown), DurationMicros: time.Since(resetStarted).Microseconds(), RecoveryHint: status.RecoveryHint, Error: "verified action after reset: " + err.Error()}
	}
	passed := unknownConfirmed && deadReported && verified.Verified && verified.Outcome == string(agent.OutcomePostconditionVerified) && verified.URL == pageURL
	unknownTarget := ""
	if unknown != nil {
		unknownTarget = unknown.TargetID
	}
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "unknown_then_remote_reconnected", Target: unknownTarget, ActionCalls: 6, OutputBytes: len(fmt.Sprint(clickErr)) + len(fmt.Sprint(status)) + len(fmt.Sprint(verified)), DurationMicros: time.Since(resetStarted).Microseconds(), Evidence: fmt.Sprintf("dispatch_signal=true; unknown_outcome=%t; session_dead=%t; reset_reconnected=true; post_reset_action=%s", unknownConfirmed, status.SessionDead, verified.Outcome), RecoveryHint: status.RecoveryHint}
	if !passed {
		result.Error = fmt.Sprintf("recovery predicates failed: unknown=%t dead=%t verified=%t outcome=%s url=%s", unknownConfirmed, deadReported, verified.Verified, verified.Outcome, verified.URL)
	}
	return result
}

func runLateActionCancellation(fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	handler, started, completed := benchmarkfixture.HandlerWithLateActionSignals()
	server := httptest.NewServer(handler)
	defer server.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, Timeout: 10 * time.Second, AllowPrivateIPs: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch browser: " + err.Error()}
	}
	defer func() { _ = session.Close() }()
	pageURL := server.URL + "/late-action"
	if _, err := session.NavigateWithPostconditionContext(ctx, pageURL, agent.Postcondition{}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "navigate to fixture: " + err.Error()}
	}
	clickCtx, clickCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer clickCancel()
	type clickResult struct {
		page *agent.PageResult
		err  error
	}
	clickDone := make(chan clickResult, 1)
	startedAt := time.Now()
	go func() {
		page, clickErr := session.ClickWithPostconditionContext(clickCtx, "#late-action", agent.Postcondition{Text: "Late completion"}, false)
		clickDone <- clickResult{page: page, err: clickErr}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "page did not confirm dispatch: " + ctx.Err().Error()}
	}
	var got clickResult
	select {
	case got = <-clickDone:
	case <-ctx.Done():
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "click call did not return: " + ctx.Err().Error()}
	}
	returnedAt := time.Now()
	var unknown *agent.ActionOutcomeError
	unknownOutcome := errors.As(got.err, &unknown) && unknown.Outcome == agent.OutcomeUnknown
	expectationRetained := unknownOutcome && unknown.ExpectedPostcondition != nil && unknown.ExpectedPostcondition.Text == "Late completion"
	if !unknownOutcome || !expectationRetained {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, DurationMicros: returnedAt.Sub(startedAt).Microseconds(), Error: fmt.Sprintf("expected outcome_unknown after deadline, got %T: %v", got.err, got.err)}
	}
	completedBeforeReturn := false
	select {
	case <-completed:
		completedBeforeReturn = true
	default:
	}
	select {
	case <-completed:
	case <-ctx.Done():
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Outcome: string(unknown.Outcome), Error: "page did not complete bounded task: " + ctx.Err().Error()}
	}
	reconciled, reconcileErr := session.ReconcileActionOutcomeContext(ctx, unknown)
	if reconcileErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Outcome: string(unknown.Outcome), Error: "reconcile unknown action: " + reconcileErr.Error()}
	}
	text, err := session.ReadableText()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 5, Outcome: string(unknown.Outcome), Error: "read page after late completion: " + err.Error()}
	}
	otherTab, tabErr := session.OpenTabContext(ctx, "reconcile-guard", server.URL+"/action")
	if tabErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 5, Outcome: string(unknown.Outcome), Error: "open target for reconciliation guard: " + tabErr.Error()}
	}
	_, staleErr := session.ReconcileActionOutcomeContext(ctx, unknown)
	var staleTarget *agent.OperationError
	staleRefused := errors.As(staleErr, &staleTarget) && staleTarget.Cause == "stale_target" && otherTab.TargetID != unknown.TargetID
	returnLatency := returnedAt.Sub(startedAt)
	deadlineGrace := 50 * time.Millisecond
	recoveryLinked := unknown.OperationOrdinal > 0 && reconciled.Receipt != nil && reconciled.Receipt.RecoveryOfOperationOrdinal == unknown.OperationOrdinal
	passed := !completedBeforeReturn && strings.Contains(text, "Late completion") && reconciled.Verified && reconciled.Outcome == string(agent.OutcomePostconditionVerified) && recoveryLinked && staleRefused && clickCtx.Err() == context.DeadlineExceeded && returnLatency <= 300*time.Millisecond+deadlineGrace
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: string(unknown.Outcome), ActionCalls: 7, OutputBytes: len(text), DurationMicros: returnLatency.Microseconds(), Evidence: fmt.Sprintf("caller_deadline_ms=300; deadline_grace_ms=%d; call_returned_ms=%d; completion_observed_after_return=true; expected_condition_retained=%t; reconciliation=%s; recovery_receipt_linked=%t; stale_target_refused=%t; final_dom_contains_late_completion=%t", deadlineGrace.Milliseconds(), returnLatency.Milliseconds(), expectationRetained, reconciled.Outcome, recoveryLinked, staleRefused, strings.Contains(text, "Late completion"))}
	if !passed {
		result.Error = fmt.Sprintf("late outcome predicates failed: completion_before_return=%t deadline=%v return_latency_ms=%d deadline_grace_ms=%d reconciled=%t/%s stale_refused=%t final_text=%t", completedBeforeReturn, clickCtx.Err(), returnLatency.Milliseconds(), deadlineGrace.Milliseconds(), reconciled.Verified, reconciled.Outcome, staleRefused, strings.Contains(text, "Late completion"))
	}
	return result
}

type lateFixtureSignalState struct {
	StartedCount   uint64 `json:"started_count"`
	CompletedCount uint64 `json:"completed_count"`
}

func readLateFixtureSignalState(ctx context.Context, endpoint string) (lateFixtureSignalState, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return lateFixtureSignalState{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return lateFixtureSignalState{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return lateFixtureSignalState{}, fmt.Errorf("unexpected fixture signal status %d", response.StatusCode)
	}
	var state lateFixtureSignalState
	if err := json.NewDecoder(response.Body).Decode(&state); err != nil {
		return lateFixtureSignalState{}, err
	}
	return state, nil
}

func waitForLateFixtureSignal(ctx context.Context, endpoint string, prior uint64, completed bool) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := readLateFixtureSignalState(ctx, endpoint)
		if err != nil {
			return err
		}
		count := state.StartedCount
		if completed {
			count = state.CompletedCount
		}
		if count > prior {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runSilentCDPHandshakeDeadline(fixture fixtureScenario) scenarioResult {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve silent CDP endpoint: " + err.Error()}
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	stopAccept := make(chan struct{})
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			select {
			case accepted <- conn:
			case <-stopAccept:
				_ = conn.Close()
				return
			}
			select {
			case <-stopAccept:
				_ = conn.Close()
				return
			default:
			}
		}
	}()
	defer close(stopAccept)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, dialErr := cdpclient.DialContext(ctx, "ws://"+listener.Addr().String()+"/devtools/browser/silent")
	duration := time.Since(started)
	acceptedPeer := false
	select {
	case conn := <-accepted:
		acceptedPeer = true
		_ = conn.Close()
	default:
	}
	bounded := dialErr != nil && duration < time.Second
	result := scenarioResult{ID: fixture.ID, Success: bounded && acceptedPeer, Verified: bounded && acceptedPeer, Outcome: "handshake_deadline", ActionCalls: 1, OutputBytes: len(fmt.Sprint(dialErr)), DurationMicros: duration.Microseconds(), Evidence: fmt.Sprintf("endpoint_accepted_tcp=%t; returned_before_one_second=%t; caller_deadline_enforced=%t", acceptedPeer, duration < time.Second, bounded)}
	if !result.Success {
		result.Error = fmt.Sprintf("bounded handshake mismatch: accepted=%t err=%v duration=%s", acceptedPeer, dialErr, duration)
	}
	return result
}

func runCLIHandshakeDeadline(fixture fixtureScenario) scenarioResult {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve silent CLI CDP endpoint: " + err.Error()}
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	acceptedAt := make(chan time.Time, 1)
	stopAccept := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		acceptedAt <- time.Now()
		select {
		case accepted <- conn:
		case <-stopAccept:
			_ = conn.Close()
			return
		}
		<-stopAccept
		_ = conn.Close()
	}()
	defer close(stopAccept)

	tmpDir, err := os.MkdirTemp("", "scout-cli-deadline-")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create CLI benchmark directory: " + err.Error()}
	}
	defer os.RemoveAll(tmpDir)
	binary := tmpDir + "/scout"
	build := exec.Command("go", "build", "-o", binary, "./cmd/scout")
	if buildOutput, buildErr := build.CombinedOutput(); buildErr != nil {
		return scenarioResult{ID: fixture.ID, Error: fmt.Sprintf("build CLI for deadline probe: %v: %s", buildErr, buildOutput)}
	}

	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "SCOUT_REMOTE_CDP=") {
			env = append(env, item)
		}
	}
	env = append(env, "SCOUT_REMOTE_CDP=ws://"+listener.Addr().String()+"/devtools/browser/silent")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "navigate", "https://example.com/", "--headless", "--timeout=300ms")
	cmd.Env = env
	started := time.Now()
	output, runErr := cmd.CombinedOutput()
	processDuration := time.Since(started)
	acceptedPeer := false
	var peerConn net.Conn
	var connectionStarted time.Time
	select {
	case peerConn = <-accepted:
		acceptedPeer = true
		connectionStarted = <-acceptedAt
	default:
	}
	operationDuration := processDuration
	if acceptedPeer {
		operationDuration = time.Since(connectionStarted)
	}
	peerClosed := false
	if peerConn != nil {
		_ = peerConn.SetReadDeadline(time.Now().Add(750 * time.Millisecond))
		reader := bufio.NewReader(peerConn)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				break
			}
			if line == "\r\n" {
				var oneByte [1]byte
				_, readErr = reader.Read(oneByte[:])
				peerClosed = errors.Is(readErr, io.EOF)
				break
			}
		}
	}
	processExited := cmd.ProcessState != nil && cmd.ProcessState.Exited()
	outputText := strings.ToLower(string(output))
	timeoutReported := strings.Contains(outputText, "deadline") || strings.Contains(outputText, "i/o timeout")
	bounded := runErr != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) && operationDuration < 750*time.Millisecond && timeoutReported
	passed := acceptedPeer && peerClosed && processExited && bounded
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "cli_startup_deadline", ActionCalls: 1, OutputBytes: len(output), DurationMicros: operationDuration.Microseconds(), Evidence: fmt.Sprintf("endpoint_accepted_tcp=%t; cli_timeout_reported=%t; client_socket_closed=%t; cli_process_exited=%t; process_duration_ms=%d; operation_returned_before_750ms=%t", acceptedPeer, timeoutReported, peerClosed, processExited, processDuration.Milliseconds(), operationDuration < 750*time.Millisecond)}
	if !passed {
		result.Error = fmt.Sprintf("CLI deadline mismatch: accepted=%t bounded=%t socket_closed=%t process_exited=%t operation_duration=%s process_duration=%s err=%v output=%s", acceptedPeer, bounded, peerClosed, processExited, operationDuration, processDuration, runErr, strings.TrimSpace(string(output)))
	}
	return result
}

func runMCPHandshakeWatchdog(fixture fixtureScenario) scenarioResult {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve silent MCP CDP endpoint: " + err.Error()}
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	stopAccept := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		select {
		case accepted <- conn:
		case <-stopAccept:
			_ = conn.Close()
			return
		}
		<-stopAccept
		_ = conn.Close()
	}()
	defer close(stopAccept)

	oldTimeout, hadTimeout := os.LookupEnv("SCOUT_TOOL_TIMEOUT")
	oldRemote, hadRemote := os.LookupEnv("SCOUT_REMOTE_CDP")
	_ = os.Setenv("SCOUT_TOOL_TIMEOUT", "300ms")
	_ = os.Setenv("SCOUT_REMOTE_CDP", "ws://"+listener.Addr().String()+"/devtools/browser/silent")
	transport, err := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	if hadTimeout {
		_ = os.Setenv("SCOUT_TOOL_TIMEOUT", oldTimeout)
	} else {
		_ = os.Unsetenv("SCOUT_TOOL_TIMEOUT")
	}
	if hadRemote {
		_ = os.Setenv("SCOUT_REMOTE_CDP", oldRemote)
	} else {
		_ = os.Unsetenv("SCOUT_REMOTE_CDP")
	}
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start focused MCP server: " + err.Error()}
	}
	defer transport.Close()
	mcpClient := client.New(transport)
	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer initCancel()
	if _, err := mcpClient.Initialize(initCtx); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "initialize focused MCP server: " + err.Error()}
	}

	callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
	started := time.Now()
	_, callErr := mcpCall(callCtx, mcpClient, "navigate", map[string]any{"url": "https://example.com/"})
	duration := time.Since(started)
	callCancel()
	acceptedPeer := false
	var peerConn net.Conn
	select {
	case peerConn = <-accepted:
		acceptedPeer = true
	default:
	}
	watchdogTimeout := callErr != nil && strings.Contains(callErr.Error(), "SCOUT_TIMEOUT") && strings.Contains(callErr.Error(), "watchdog")
	peerClosed := false
	if peerConn != nil {
		_ = peerConn.SetReadDeadline(time.Now().Add(750 * time.Millisecond))
		reader := bufio.NewReader(peerConn)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				break
			}
			if line == "\r\n" {
				var oneByte [1]byte
				_, readErr = reader.Read(oneByte[:])
				peerClosed = errors.Is(readErr, io.EOF)
				break
			}
		}
	}

	healthCtx, healthCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, healthErr := mcpCall(healthCtx, mcpClient, "list_sessions", map[string]any{})
	healthCancel()
	responsive := healthErr == nil
	passed := acceptedPeer && watchdogTimeout && duration < time.Second && peerClosed && responsive
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "mcp_watchdog_timeout", ActionCalls: 2, OutputBytes: len(fmt.Sprint(callErr)), DurationMicros: duration.Microseconds(), Evidence: fmt.Sprintf("endpoint_accepted_tcp=%t; watchdog_error=%t; returned_before_one_second=%t; canceled_handshake_socket_closed=%t; server_responsive_after_timeout=%t", acceptedPeer, watchdogTimeout, duration < time.Second, peerClosed, responsive)}
	if !passed {
		result.Error = fmt.Sprintf("MCP watchdog mismatch: accepted=%t watchdog=%t socket_closed=%t responsive=%t duration=%s err=%v health_err=%v", acceptedPeer, watchdogTimeout, peerClosed, responsive, duration, callErr, healthErr)
	}
	return result
}

func runExternalTargetClose(serverURL string, fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch external browser: " + err.Error()}
	}
	defer browser.Close()
	session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Headless: true, Timeout: 10 * time.Second, AllowPrivateIPs: true, RemoteCDP: browser.WSEndpoint()})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "attach Scout to external browser: " + err.Error()}
	}
	defer session.Close()
	pageURL := serverURL + fixture.StartingPath
	page, err := session.NavigateWithPostconditionContext(ctx, pageURL, agent.Postcondition{})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "navigate external target: " + err.Error()}
	}
	external, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "connect external CDP actor: " + err.Error()}
	}
	defer external.Close()
	if err := external.CloseTargetCtx(ctx, page.TargetID); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "externally close target: " + err.Error()}
	}
	_, actionErr := session.ClickWithPostconditionContext(ctx, fixture.Selector, agent.Postcondition{Text: fixture.ExpectedText}, false)
	var opErr *agent.OperationError
	structured := errors.As(actionErr, &opErr) && opErr.Cause == "target_gone" && opErr.TargetID == page.TargetID && opErr.RecoveryHint != ""
	result := scenarioResult{ID: fixture.ID, Success: structured, Verified: structured, Outcome: "target_gone", Target: page.TargetID, ActionCalls: 3, OutputBytes: len(fmt.Sprint(actionErr)), Evidence: fmt.Sprintf("external_close=true; structured_target_gone=%t; target_id_matches=%t; recovery_hint=%t", structured, opErr != nil && opErr.TargetID == page.TargetID, opErr != nil && opErr.RecoveryHint != "")}
	if !structured {
		result.Error = fmt.Sprintf("expected structured target_gone for %s, got %T: %v", page.TargetID, actionErr, actionErr)
		result.FalseSuccess = actionErr == nil
	} else {
		result.RecoveryHint = opErr.RecoveryHint
	}
	return result
}

func runMCPExternalTargetClose(serverURL string, fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch external browser: " + err.Error()}
	}
	defer browser.Close()
	transport, err := startMCPWithRemoteEndpoint(browser.WSEndpoint())
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start MCP server attached to external browser: " + err.Error()}
	}
	defer transport.Close()
	mcp := client.New(transport)
	if _, err := mcp.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "initialize remote MCP server: " + err.Error()}
	}
	nav, err := mcpCall(ctx, mcp, "navigate", map[string]any{"url": serverURL + fixture.StartingPath})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "navigate through MCP: " + err.Error()}
	}
	var page agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(nav), &page); err != nil || page.TargetID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: fmt.Sprintf("MCP navigation omitted target identity: %v", err)}
	}
	external, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "connect external CDP actor: " + err.Error()}
	}
	defer external.Close()
	if err := external.CloseTargetCtx(ctx, page.TargetID); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "externally close MCP target: " + err.Error()}
	}
	_, actionErr := mcpCall(ctx, mcp, "click", map[string]any{"selector": fixture.Selector, "target_id": page.TargetID, "expected_text": fixture.ExpectedText})
	const marker = "SCOUT_ERROR "
	message := actionErrString(actionErr)
	idx := strings.Index(message, marker)
	var envelope struct {
		Code         string `json:"code"`
		Cause        string `json:"cause"`
		TargetID     string `json:"target_id"`
		RecoveryHint string `json:"recovery_hint"`
	}
	decodeErr := errors.New("missing Scout MCP error envelope")
	if idx >= 0 {
		decodeErr = json.Unmarshal([]byte(message[idx+len(marker):]), &envelope)
	}
	passed := decodeErr == nil && actionErr != nil && envelope.Code == "SCOUT_OPERATION_ERROR" && envelope.Cause == "target_gone" && envelope.TargetID == page.TargetID && envelope.RecoveryHint != ""
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: envelope.Cause, Target: page.TargetID, ActionCalls: 3, OutputBytes: len(message), Evidence: fmt.Sprintf("external_close=true; mcp_error_code=%s; cause=%s; target_id_matches=%t; recovery_hint=%t", envelope.Code, envelope.Cause, envelope.TargetID == page.TargetID, envelope.RecoveryHint != ""), RecoveryHint: envelope.RecoveryHint}
	if !passed {
		result.Error = fmt.Sprintf("MCP target-loss contract failed: error=%v; envelope=%+v; decode=%v", actionErr, envelope, decodeErr)
		result.FalseSuccess = actionErr == nil
	}
	return result
}

func actionErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func runMCPRemoteEndpointStickiness(serverURL string, fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve DevTools port: " + err.Error()}
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true, Port: port})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote fixture browser: " + err.Error()}
	}
	defer func() {
		if browser != nil {
			_ = browser.Close()
		}
	}()
	transport, err := startMCPWithRemoteEndpoint(endpoint)
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start remote-configured MCP server: " + err.Error()}
	}
	defer transport.Close()
	mcpClient := client.New(transport)
	if _, err := mcpClient.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize remote-configured MCP server: " + err.Error()}
	}
	calls, outputBytes := 1, 0
	listSessions := func() ([]benchmarkSessionSummary, error) {
		result, callErr := mcpCall(ctx, mcpClient, "list_sessions", map[string]any{})
		calls++
		if callErr != nil {
			return nil, callErr
		}
		outputBytes += len(mcpResultJSON(result))
		var sessions []benchmarkSessionSummary
		if decodeErr := json.Unmarshal(mcpResultJSON(result), &sessions); decodeErr != nil {
			return nil, decodeErr
		}
		return sessions, nil
	}
	defaultSummary := func(sessions []benchmarkSessionSummary) *benchmarkSessionSummary {
		for i := range sessions {
			if sessions[i].Name == "default" {
				return &sessions[i]
			}
		}
		return nil
	}
	initial, err := listSessions()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "read initial endpoint configuration: " + err.Error()}
	}
	initialDefault := defaultSummary(initial)
	if initialDefault == nil || initialDefault.Started || initialDefault.BrowserOwnership != "remote_cdp" || initialDefault.EndpointSource != "SCOUT_REMOTE_CDP" || initialDefault.BrowserEndpoint != endpoint {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("initial endpoint provenance mismatch: %+v", initialDefault)}
	}
	if _, err := mcpCall(ctx, mcpClient, "configure", map[string]any{"allow_private_ips": true}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls + 1, OutputBytes: outputBytes, Error: "configure local fixture access: " + err.Error()}
	}
	calls++
	pageURL := serverURL + fixture.StartingPath
	nav, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": pageURL})
	calls++
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "navigate through configured endpoint: " + err.Error()}
	}
	outputBytes += len(mcpResultJSON(nav))
	var firstPage agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(nav), &firstPage); err != nil || firstPage.TargetID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("initial navigation omitted target identity: %v", err)}
	}
	if err := browser.Close(); err != nil && !strings.Contains(err.Error(), "signal: killed") {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "stop remote fixture browser: " + err.Error()}
	}
	browser = nil
	_, failedResetErr := mcpCall(ctx, mcpClient, "reset", map[string]any{})
	calls++
	if failedResetErr == nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "reset unexpectedly succeeded while configured remote browser was unavailable"}
	}
	browser, err = launcher.LaunchContext(ctx, launcher.Options{Headless: true, Port: port})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "restart remote fixture browser: " + err.Error()}
	}
	reset, err := mcpCall(ctx, mcpClient, "reset", map[string]any{})
	calls++
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "reset after remote restart: " + err.Error()}
	}
	outputBytes += len(mcpResultJSON(reset))
	var resetResult agent.ResetResult
	if err := json.Unmarshal(mcpResultJSON(reset), &resetResult); err != nil || !resetResult.BrowserAlive || resetResult.Outcome != string(agent.OutcomePostconditionVerified) {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("remote reset result mismatch: %+v decode=%v", resetResult, err)}
	}
	nav, err = mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": pageURL})
	calls++
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "navigate after remote reset: " + err.Error()}
	}
	outputBytes += len(mcpResultJSON(nav))
	var secondPage agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(nav), &secondPage); err != nil || secondPage.TargetID == "" || secondPage.URL != pageURL {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("post-reset navigation mismatch: %+v decode=%v", secondPage, err)}
	}
	finalSessions, err := listSessions()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "read post-reset endpoint provenance: " + err.Error()}
	}
	finalDefault := defaultSummary(finalSessions)
	passed := finalDefault != nil && finalDefault.Started && finalDefault.BrowserAlive && !finalDefault.SessionDead && finalDefault.BrowserOwnership == "remote_cdp" && finalDefault.EndpointSource == "SCOUT_REMOTE_CDP" && finalDefault.BrowserEndpoint == endpoint && finalDefault.CurrentURL == pageURL && firstPage.TargetID != secondPage.TargetID
	evidence := fmt.Sprintf("initial_source=%s; endpoint=%s; remote_unavailable_reset_failed=%t; reset_reconnected=true; post_reset_target_changed=%t; final_owner=%s; final_source=%s; final_url=%s", initialDefault.EndpointSource, initialDefault.BrowserEndpoint, failedResetErr != nil, firstPage.TargetID != secondPage.TargetID, valueOrEmpty(finalDefault, func(s *benchmarkSessionSummary) string { return s.BrowserOwnership }), valueOrEmpty(finalDefault, func(s *benchmarkSessionSummary) string { return s.EndpointSource }), valueOrEmpty(finalDefault, func(s *benchmarkSessionSummary) string { return s.CurrentURL }))
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "remote_endpoint_sticky_after_reset", Target: firstPage.TargetID + "/" + secondPage.TargetID, ActionCalls: calls, OutputBytes: outputBytes, Evidence: evidence}
	if !passed {
		result.Error = fmt.Sprintf("post-reset endpoint ownership mismatch: %+v", finalDefault)
	}
	return result
}

func startMCPWithRemoteEndpoint(endpoint string) (*client.StdioTransport, error) {
	previousEndpoint, hadEndpoint := os.LookupEnv("SCOUT_REMOTE_CDP")
	previousPrivateIPs, hadPrivateIPs := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_REMOTE_CDP", endpoint); err != nil {
		return nil, err
	}
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true"); err != nil {
		restoreEnvironmentValue("SCOUT_REMOTE_CDP", previousEndpoint, hadEndpoint)
		return nil, err
	}
	transport, startErr := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	restoreEnvironmentValue("SCOUT_REMOTE_CDP", previousEndpoint, hadEndpoint)
	restoreEnvironmentValue("SCOUT_ALLOW_PRIVATE_IPS", previousPrivateIPs, hadPrivateIPs)
	return transport, startErr
}

func runMCPSessionIdleExpiration(serverURL string, fixture fixtureScenario) scenarioResult {
	const idleTimeout = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	previousIdleTimeout, hadIdleTimeout := os.LookupEnv("SCOUT_SESSION_IDLE_TIMEOUT")
	previousPrivateIPs, hadPrivateIPs := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_SESSION_IDLE_TIMEOUT", idleTimeout.String()); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "set idle timeout: " + err.Error()}
	}
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true"); err != nil {
		restoreEnvironmentValue("SCOUT_SESSION_IDLE_TIMEOUT", previousIdleTimeout, hadIdleTimeout)
		return scenarioResult{ID: fixture.ID, Error: "allow fixture access: " + err.Error()}
	}
	transport, startErr := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	restoreEnvironmentValue("SCOUT_SESSION_IDLE_TIMEOUT", previousIdleTimeout, hadIdleTimeout)
	restoreEnvironmentValue("SCOUT_ALLOW_PRIVATE_IPS", previousPrivateIPs, hadPrivateIPs)
	if startErr != nil {
		return scenarioResult{ID: fixture.ID, Error: "start idle-configured MCP server: " + startErr.Error()}
	}
	defer transport.Close()
	mcpClient := client.New(transport)
	if _, err := mcpClient.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize idle-configured MCP server: " + err.Error()}
	}
	calls, outputBytes := 1, 0
	readSessions := func() ([]benchmarkSessionSummary, error) {
		result, callErr := mcpCall(ctx, mcpClient, "list_sessions", map[string]any{})
		calls++
		if callErr != nil {
			return nil, callErr
		}
		payload := mcpResultJSON(result)
		outputBytes += len(payload)
		var sessions []benchmarkSessionSummary
		if decodeErr := json.Unmarshal(payload, &sessions); decodeErr != nil {
			return nil, decodeErr
		}
		return sessions, nil
	}
	defaultSession := func(sessions []benchmarkSessionSummary) *benchmarkSessionSummary {
		for i := range sessions {
			if sessions[i].Name == "default" {
				return &sessions[i]
			}
		}
		return nil
	}
	if _, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": serverURL + fixture.StartingPath}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls + 1, OutputBytes: outputBytes, Error: "start browser session: " + err.Error()}
	}
	calls++
	started, err := readSessions()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "read started session: " + err.Error()}
	}
	initial := defaultSession(started)
	if initial == nil || !initial.Started || initial.IdleTimeoutMS != idleTimeout.Milliseconds() || initial.LastActivity == "" || initial.IdleDeadline == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("started session omitted idle metadata: %+v", initial)}
	}
	// Do not call MCP tools during the idle window: every tool call counts as activity.
	time.Sleep(2*idleTimeout + 150*time.Millisecond)
	expired, err := readSessions()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "read expired session: " + err.Error()}
	}
	final := defaultSession(expired)
	passed := final != nil && !final.Started && final.IdleTimeoutMS == idleTimeout.Milliseconds()
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "idle_session_expired", ActionCalls: calls, OutputBytes: outputBytes, Evidence: fmt.Sprintf("timeout_ms=%d; started_deadline=%s; expired_started=%t", initial.IdleTimeoutMS, initial.IdleDeadline, final != nil && final.Started)}
	if !passed {
		result.Error = fmt.Sprintf("session did not expire as configured: %+v", final)
	}
	return result
}

// runMCPSessionIdleInFlight verifies that activity timeout never evicts a
// session while a tool handler is still running, and that idle cleanup resumes
// after the handler completes.
func runMCPSessionIdleInFlight(serverURL string, fixture fixtureScenario) scenarioResult {
	const idleTimeout = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	previousIdleTimeout, hadIdleTimeout := os.LookupEnv("SCOUT_SESSION_IDLE_TIMEOUT")
	previousPrivateIPs, hadPrivateIPs := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_SESSION_IDLE_TIMEOUT", idleTimeout.String()); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "set idle timeout: " + err.Error()}
	}
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true"); err != nil {
		restoreEnvironmentValue("SCOUT_SESSION_IDLE_TIMEOUT", previousIdleTimeout, hadIdleTimeout)
		return scenarioResult{ID: fixture.ID, Error: "allow fixture access: " + err.Error()}
	}
	transport, startErr := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	restoreEnvironmentValue("SCOUT_SESSION_IDLE_TIMEOUT", previousIdleTimeout, hadIdleTimeout)
	restoreEnvironmentValue("SCOUT_ALLOW_PRIVATE_IPS", previousPrivateIPs, hadPrivateIPs)
	if startErr != nil {
		return scenarioResult{ID: fixture.ID, Error: "start idle-configured MCP server: " + startErr.Error()}
	}
	defer transport.Close()
	mcpClient := client.New(transport)
	if _, err := mcpClient.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize idle-configured MCP server: " + err.Error()}
	}
	calls, outputBytes := 1, 0
	call := func(callCtx context.Context, name string, args map[string]any) (*client.ToolResult, error) {
		result, err := mcpCall(callCtx, mcpClient, name, args)
		calls++
		if err == nil {
			outputBytes += len(mcpResultJSON(result))
		}
		return result, err
	}
	readDefault := func() (*benchmarkSessionSummary, error) {
		result, err := call(ctx, "list_sessions", map[string]any{})
		if err != nil {
			return nil, err
		}
		var sessions []benchmarkSessionSummary
		if err := json.Unmarshal(mcpResultJSON(result), &sessions); err != nil {
			return nil, err
		}
		for i := range sessions {
			if sessions[i].Name == "default" {
				return &sessions[i], nil
			}
		}
		return nil, errors.New("default session missing from list_sessions")
	}
	if _, err := call(ctx, "navigate", map[string]any{"url": serverURL + fixture.StartingPath}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "start delayed-element session: " + err.Error()}
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer waitCancel()
	waitStarted := time.Now()
	waitDone := make(chan error, 1)
	go func() {
		_, err := call(waitCtx, "wait_for", map[string]any{"selector": fixture.Selector})
		waitDone <- err
	}()
	// The element appears after 1.8 seconds, well beyond the 500 ms idle
	// timeout. Queue a registry read during the wait; the stdio transport serves
	// it after the active tool returns, so successful wait completion plus a
	// still-started session proves the idle reaper did not close it underneath.
	time.Sleep(900 * time.Millisecond)
	statusStarted := time.Now()
	afterWait, err := readDefault()
	statusLatency := time.Since(statusStarted)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "list during in-flight wait: " + err.Error()}
	}
	waitErr := <-waitDone
	waitDuration := time.Since(waitStarted)
	if waitErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: "delayed wait did not complete: " + waitErr.Error()}
	}
	if afterWait == nil || !afterWait.Started || waitDuration <= idleTimeout {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: fmt.Sprintf("session was not protected across the in-flight call: started_after_wait=%t status_latency=%s wait_duration=%s", afterWait != nil && afterWait.Started, statusLatency, waitDuration)}
	}
	time.Sleep(2*idleTimeout + 150*time.Millisecond)
	after, err := readDefault()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: "list after idle window: " + err.Error()}
	}
	passed := after != nil && !after.Started
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "inflight_protected_then_idle_expired", ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Evidence: fmt.Sprintf("idle_timeout_ms=%d; wait_duration_ms=%d; session_started_after_wait=%t; queued_list_sessions_latency_ms=%d; expired_after_return=%t", idleTimeout.Milliseconds(), waitDuration.Milliseconds(), afterWait.Started, statusLatency.Milliseconds(), !after.Started)}
	if !passed {
		result.Error = fmt.Sprintf("session did not expire after in-flight call completed: %+v", after)
	}
	return result
}

// runMCPHTTPIdleInFlight verifies an HTTP tool call longer than the idle
// timeout keeps its browser alive while an SSE stream remains open, and that
// ordinary idle cleanup resumes after the call completes.
func runMCPHTTPIdleInFlight(fixture fixtureScenario, scoutBinary string) scenarioResult {
	idleTimeout := 500 * time.Millisecond
	if raw := os.Getenv("SCOUT_BENCHMARK_IDLE_TIMEOUT"); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil || parsed < 100*time.Millisecond {
			return scenarioResult{ID: fixture.ID, Error: "SCOUT_BENCHMARK_IDLE_TIMEOUT must be a duration of at least 100ms"}
		}
		idleTimeout = parsed
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve HTTP port: " + err.Error()}
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	taskStoreDir, err := os.MkdirTemp("", "scout-principal-reconnect-task-store-*")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create task store directory: " + err.Error()}
	}
	defer os.RemoveAll(taskStoreDir)
	cmd := exec.CommandContext(ctx, scoutBinary, "mcp", "serve-http", "--listen="+addr, "--task-store="+filepath.Join(taskStoreDir, "task-runs.db"))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Env = envWithout(os.Environ(), "SCOUT_REMOTE_CDP")
	cmd.Env = envWith(cmd.Env, "SCOUT_ALLOW_PRIVATE_IPS", "1")
	cmd.Env = envWith(cmd.Env, "SCOUT_SESSION_IDLE_TIMEOUT", idleTimeout.String())
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start idle HTTP MCP server: " + err.Error()}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	capture := &mcpSessionIDCapture{base: http.DefaultTransport}
	httpClient := &http.Client{Transport: capture}
	transport, err := client.NewHTTPTransport("http://"+addr, client.WithHTTPClient(httpClient))
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create HTTP transport: " + err.Error()}
	}
	mcpClient := client.New(transport)
	defer mcpClient.Close()
	for attempt := 0; attempt < 80; attempt++ {
		if _, err := mcpClient.Initialize(ctx); err == nil {
			break
		}
		if attempt == 79 {
			return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "HTTP MCP server did not become ready"}
		}
		time.Sleep(100 * time.Millisecond)
	}
	calls, outputBytes := 1, 0
	var callMu sync.Mutex
	call := func(callCtx context.Context, name string, args map[string]any) (*client.ToolResult, error) {
		result, callErr := mcpCall(callCtx, mcpClient, name, args)
		callMu.Lock()
		defer callMu.Unlock()
		calls++
		if callErr == nil {
			outputBytes += len(mcpResultJSON(result))
		}
		return result, callErr
	}
	if capture.sessionID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "HTTP transport did not expose the minted MCP session ID"}
	}
	pageURL := server.URL + fixture.StartingPath
	if raw := os.Getenv("SCOUT_BENCHMARK_WAIT_DELAY_MS"); raw != "" {
		if delayMS, parseErr := strconv.Atoi(raw); parseErr == nil && delayMS > 0 && delayMS <= 30000 {
			pageURL += "?delay_ms=" + strconv.Itoa(delayMS)
		}
	}
	if _, err := call(ctx, "navigate", map[string]any{"url": pageURL}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "start browser session: " + err.Error()}
	}
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	streamReq, err := http.NewRequestWithContext(streamCtx, http.MethodGet, "http://"+addr+"/mcp", nil)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "build SSE request: " + err.Error()}
	}
	streamReq.Header.Set("Mcp-Session-Id", capture.sessionID)
	streamReq.Header.Set("Accept", "text/event-stream")
	stream, err := httpClient.Do(streamReq)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "open HTTP event stream: " + err.Error()}
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("HTTP event stream returned status %d", stream.StatusCode)}
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer waitCancel()
	waitStarted := time.Now()
	waitDone := make(chan error, 1)
	go func() {
		_, waitErr := call(waitCtx, "wait_for", map[string]any{"selector": fixture.Selector})
		waitDone <- waitErr
	}()
	// Query session state concurrently while wait_for owns the browser session.
	// list_sessions must report the active call without blocking on browser status.
	time.Sleep(900 * time.Millisecond)
	statusStarted := time.Now()
	type sessionRead struct {
		sessions []benchmarkSessionSummary
		err      error
	}
	statusDone := make(chan sessionRead, 1)
	go func() {
		result, callErr := call(ctx, "list_sessions", map[string]any{})
		if callErr != nil {
			statusDone <- sessionRead{err: callErr}
			return
		}
		var sessions []benchmarkSessionSummary
		if decodeErr := json.Unmarshal(mcpResultJSON(result), &sessions); decodeErr != nil {
			statusDone <- sessionRead{err: decodeErr}
			return
		}
		statusDone <- sessionRead{sessions: sessions}
	}()
	var concurrentSummary *benchmarkSessionSummary
	var statusLatency time.Duration
	select {
	case read := <-statusDone:
		statusLatency = time.Since(statusStarted)
		if read.err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "list sessions during in-flight wait: " + read.err.Error()}
		}
		for i := range read.sessions {
			if read.sessions[i].Name == "default" {
				concurrentSummary = &read.sessions[i]
			}
		}
	case <-ctx.Done():
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "list_sessions blocked behind in-flight browser call"}
	}
	waitErr := <-waitDone
	waitDuration := time.Since(waitStarted)
	if waitErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: "HTTP wait did not survive the idle timeout: " + waitErr.Error()}
	}
	if waitDuration <= idleTimeout {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: fmt.Sprintf("HTTP wait duration %s did not exceed idle timeout %s", waitDuration, idleTimeout)}
	}

	if concurrentSummary == nil || !concurrentSummary.Started || concurrentSummary.InFlightCalls != 1 || !concurrentSummary.StatusDeferred || statusLatency >= idleTimeout {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: fmt.Sprintf("concurrent session report did not expose the active wait promptly: summary=%+v latency=%s", concurrentSummary, statusLatency)}
	}

	// The list call above is the last protocol activity. Once the handler is
	// finished, the same idle policy must still reclaim the browser.
	time.Sleep(2*idleTimeout + 150*time.Millisecond)
	afterResult, err := call(ctx, "list_sessions", map[string]any{})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: "read HTTP session after idle window: " + err.Error()}
	}
	var sessions []benchmarkSessionSummary
	if err := json.Unmarshal(mcpResultJSON(afterResult), &sessions); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: waitDuration.Microseconds(), Error: "decode HTTP session after idle window: " + err.Error()}
	}
	var expiredAfterCall bool
	for _, summary := range sessions {
		if summary.Name == "default" {
			expiredAfterCall = !summary.Started
		}
	}
	passed := expiredAfterCall
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "http_inflight_protected_then_idle_expired", ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: time.Since(started).Microseconds(), Evidence: fmt.Sprintf("idle_timeout_ms=%d; sse_stream_open=true; wait_duration_ms=%d; concurrent_list_sessions_ms=%d; reported_in_flight=%d; status_deferred=%t; started_during_call=%t; expired_after_idle=%t", idleTimeout.Milliseconds(), waitDuration.Milliseconds(), statusLatency.Milliseconds(), concurrentSummary.InFlightCalls, concurrentSummary.StatusDeferred, concurrentSummary.Started, expiredAfterCall)}
	if !passed {
		result.Error = "HTTP session remained started after its idle timeout elapsed following the completed call"
	}
	return result
}

func runMCPRemoteIdleExpiration(serverURL string, fixture fixtureScenario) scenarioResult {
	const idleTimeout = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve remote DevTools port: " + err.Error()}
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true, Port: port})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote fixture browser: " + err.Error()}
	}
	defer func() {
		if browser != nil {
			_ = browser.Close()
		}
	}()
	previousIdleTimeout, hadIdleTimeout := os.LookupEnv("SCOUT_SESSION_IDLE_TIMEOUT")
	if err := os.Setenv("SCOUT_SESSION_IDLE_TIMEOUT", idleTimeout.String()); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "set remote idle timeout: " + err.Error()}
	}
	transport, startErr := startMCPWithRemoteEndpoint(endpoint)
	restoreEnvironmentValue("SCOUT_SESSION_IDLE_TIMEOUT", previousIdleTimeout, hadIdleTimeout)
	if startErr != nil {
		return scenarioResult{ID: fixture.ID, Error: "start remote idle-configured MCP server: " + startErr.Error()}
	}
	defer transport.Close()
	mcpClient := client.New(transport)
	if _, err := mcpClient.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize remote idle server: " + err.Error()}
	}
	calls, outputBytes := 1, 0
	readDefault := func() (*benchmarkSessionSummary, error) {
		result, callErr := mcpCall(ctx, mcpClient, "list_sessions", map[string]any{})
		calls++
		if callErr != nil {
			return nil, callErr
		}
		payload := mcpResultJSON(result)
		outputBytes += len(payload)
		var sessions []benchmarkSessionSummary
		if decodeErr := json.Unmarshal(payload, &sessions); decodeErr != nil {
			return nil, decodeErr
		}
		for i := range sessions {
			if sessions[i].Name == "default" {
				return &sessions[i], nil
			}
		}
		return nil, fmt.Errorf("default session missing")
	}
	navigate := func() (*agent.PageResult, error) {
		result, callErr := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": serverURL + fixture.StartingPath})
		calls++
		if callErr != nil {
			return nil, callErr
		}
		payload := mcpResultJSON(result)
		outputBytes += len(payload)
		var page agent.PageResult
		if decodeErr := json.Unmarshal(payload, &page); decodeErr != nil {
			return nil, decodeErr
		}
		return &page, nil
	}
	firstPage, err := navigate()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "navigate remote browser: " + err.Error()}
	}
	initial, err := readDefault()
	if err != nil || initial == nil || !initial.Started || initial.BrowserOwnership != "remote_cdp" || initial.BrowserEndpoint != endpoint {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("initial remote ownership mismatch: summary=%+v err=%v", initial, err)}
	}
	// No MCP calls during this interval: status/listing calls count as activity.
	time.Sleep(2*idleTimeout + 250*time.Millisecond)
	expired, err := readDefault()
	if err != nil || expired == nil || expired.Started || expired.IdleTimeoutMS != idleTimeout.Milliseconds() {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("remote session did not expire: summary=%+v err=%v", expired, err)}
	}
	response, err := http.Get(endpoint + "/json/version")
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "remote browser did not survive session eviction: " + err.Error()}
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("remote DevTools endpoint returned %d after eviction", response.StatusCode)}
	}
	secondPage, err := navigate()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "reconnect to surviving remote browser: " + err.Error()}
	}
	reconnected, err := readDefault()
	passed := err == nil && reconnected != nil && reconnected.Started && reconnected.BrowserOwnership == "remote_cdp" && reconnected.BrowserEndpoint == endpoint && secondPage.TargetID != "" && firstPage.TargetID != ""
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "remote_browser_survived_idle_disconnect", Target: firstPage.TargetID + "/" + secondPage.TargetID, ActionCalls: calls, OutputBytes: outputBytes, Evidence: fmt.Sprintf("evicted_started=%t; remote_http_status=%d; reconnected_started=%t; owner=%s", expired.Started, response.StatusCode, reconnected != nil && reconnected.Started, valueOrEmpty(reconnected, func(s *benchmarkSessionSummary) string { return s.BrowserOwnership }))}
	if !passed {
		result.Error = fmt.Sprintf("remote session did not reconnect with preserved ownership: summary=%+v err=%v", reconnected, err)
	}
	return result
}

// runMCPHTTPRemoteIdleExpiration verifies that an HTTP client's idle eviction
// disconnects from, but never shuts down, the caller-owned remote browser.
func runMCPHTTPRemoteIdleExpiration(serverURL string, fixture fixtureScenario) scenarioResult {
	const idleTimeout = 600 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	devtoolsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve remote DevTools port: " + err.Error()}
	}
	devtoolsPort := devtoolsListener.Addr().(*net.TCPAddr).Port
	_ = devtoolsListener.Close()
	remoteEndpoint := fmt.Sprintf("http://127.0.0.1:%d", devtoolsPort)
	remoteBrowser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true, Port: devtoolsPort})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote fixture browser: " + err.Error()}
	}
	defer func() {
		if remoteBrowser != nil {
			_ = remoteBrowser.Close()
		}
	}()
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve HTTP MCP port: " + err.Error()}
	}
	httpAddr := httpListener.Addr().String()
	_ = httpListener.Close()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "mcp", "serve-http", "--listen="+httpAddr)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Env = envWithout(os.Environ(), "SCOUT_REMOTE_CDP")
	cmd.Env = envWith(cmd.Env, "SCOUT_REMOTE_CDP", remoteEndpoint)
	cmd.Env = envWith(cmd.Env, "SCOUT_ALLOW_PRIVATE_IPS", "true")
	cmd.Env = envWith(cmd.Env, "SCOUT_SESSION_IDLE_TIMEOUT", idleTimeout.String())
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start remote HTTP MCP server: " + err.Error()}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	capture := &mcpSessionIDCapture{base: http.DefaultTransport}
	httpClient := &http.Client{Transport: capture}
	transport, err := client.NewHTTPTransport("http://"+httpAddr, client.WithHTTPClient(httpClient))
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create HTTP MCP transport: " + err.Error()}
	}
	mcpClient := client.New(transport)
	defer mcpClient.Close()
	for attempt := 0; attempt < 100; attempt++ {
		if _, err := mcpClient.Initialize(ctx); err == nil {
			break
		}
		if attempt == 99 {
			return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "remote HTTP MCP server did not become ready"}
		}
		time.Sleep(100 * time.Millisecond)
	}
	calls, outputBytes := 1, 0
	call := func(name string, args map[string]any) ([]byte, error) {
		result, callErr := mcpCall(ctx, mcpClient, name, args)
		calls++
		if callErr != nil {
			return nil, callErr
		}
		payload := mcpResultJSON(result)
		outputBytes += len(payload)
		return payload, nil
	}
	readDefault := func() (*benchmarkSessionSummary, error) {
		payload, callErr := call("list_sessions", map[string]any{})
		if callErr != nil {
			return nil, callErr
		}
		var sessions []benchmarkSessionSummary
		if decodeErr := json.Unmarshal(payload, &sessions); decodeErr != nil {
			return nil, decodeErr
		}
		for i := range sessions {
			if sessions[i].Name == "default" {
				return &sessions[i], nil
			}
		}
		return nil, errors.New("default session missing")
	}
	// A second independent HTTP client can observe session state without
	// resetting the target client's idle timer.
	observerTransport, err := client.NewHTTPTransport("http://" + httpAddr)
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create idle observer transport: " + err.Error()}
	}
	observer := client.New(observerTransport)
	defer observer.Close()
	if _, err := observer.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "initialize idle observer: " + err.Error()}
	}
	observeDefault := func() (*benchmarkSessionSummary, error) {
		result, callErr := mcpCall(ctx, observer, "list_sessions", map[string]any{})
		if callErr != nil {
			return nil, callErr
		}
		var sessions []benchmarkSessionSummary
		if decodeErr := json.Unmarshal(mcpResultJSON(result), &sessions); decodeErr != nil {
			return nil, decodeErr
		}
		for i := range sessions {
			if sessions[i].Name == "default" {
				return &sessions[i], nil
			}
		}
		return nil, errors.New("default session missing")
	}
	if capture.sessionID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "HTTP transport did not expose its MCP session ID"}
	}
	initial, err := readDefault()
	if err != nil || initial == nil || initial.Started || initial.BrowserOwnership != "remote_cdp" || initial.BrowserEndpoint != remoteEndpoint {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("initial remote HTTP ownership mismatch: summary=%+v err=%v", initial, err)}
	}
	pageURL := serverURL + fixture.StartingPath
	pagePayload, err := call("navigate", map[string]any{"url": pageURL})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "navigate attached remote browser: " + err.Error()}
	}
	var firstPage agent.PageResult
	if err := json.Unmarshal(pagePayload, &firstPage); err != nil || firstPage.TargetID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("remote HTTP navigation omitted target identity: %+v decode=%v", firstPage, err)}
	}
	idleState, err := readDefault()
	if err != nil || idleState == nil || idleState.IdleDeadline == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("read remote idle deadline: summary=%+v err=%v", idleState, err)}
	}
	deadline, err := time.Parse(time.RFC3339Nano, idleState.IdleDeadline)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "parse remote idle deadline: " + err.Error()}
	}
	cleanupStarted := time.Now()
	if wait := time.Until(deadline); wait > 0 {
		time.Sleep(wait)
	}
	var expired *benchmarkSessionSummary
	for time.Now().Before(deadline.Add(3 * time.Second)) {
		expired, err = observeDefault()
		if err != nil || expired == nil || !expired.Started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cleanupLatency := time.Since(deadline)
	if err != nil || expired == nil || expired.Started || expired.IdleTimeoutMS != idleTimeout.Milliseconds() {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: time.Since(cleanupStarted).Microseconds(), Error: fmt.Sprintf("remote HTTP session did not expire: summary=%+v err=%v", expired, err)}
	}
	response, err := http.Get(remoteEndpoint + "/json/version")
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "caller-owned remote browser closed during idle eviction: " + err.Error()}
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("remote browser endpoint returned HTTP %d after idle eviction", response.StatusCode)}
	}
	secondPayload, err := call("navigate", map[string]any{"url": pageURL})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "reconnect to caller-owned browser after idle eviction: " + err.Error()}
	}
	var secondPage agent.PageResult
	if err := json.Unmarshal(secondPayload, &secondPage); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "decode navigation after remote reconnect: " + err.Error()}
	}
	reconnected, err := readDefault()
	passed := err == nil && response.StatusCode == http.StatusOK && secondPage.TargetID != "" && secondPage.TargetID == firstPage.TargetID && reconnected != nil && reconnected.Started && reconnected.BrowserAlive && reconnected.BrowserOwnership == "remote_cdp" && reconnected.BrowserEndpoint == remoteEndpoint
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "http_remote_idle_detached_and_reconnected", Target: firstPage.TargetID + "/" + secondPage.TargetID, ActionCalls: calls, OutputBytes: outputBytes, DurationMicros: time.Since(cleanupStarted).Microseconds(), Evidence: fmt.Sprintf("idle_timeout_ms=%d; cleanup_after_deadline_ms=%.1f; expired_started=%t; remote_http_status=%d; target_stable=%t; reconnected_owner=%s", idleTimeout.Milliseconds(), float64(cleanupLatency.Microseconds())/1000, expired.Started, response.StatusCode, secondPage.TargetID == firstPage.TargetID, valueOrEmpty(reconnected, func(s *benchmarkSessionSummary) string { return s.BrowserOwnership }))}
	if !passed {
		result.Error = fmt.Sprintf("remote HTTP idle reconnect contract failed: summary=%+v err=%v targets=%s/%s", reconnected, err, firstPage.TargetID, secondPage.TargetID)
	}
	return result
}

func runMCPSessionIdleIsolation(serverURL string, fixture fixtureScenario) scenarioResult {
	const idleTimeout = 600 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	previousIdleTimeout, hadIdleTimeout := os.LookupEnv("SCOUT_SESSION_IDLE_TIMEOUT")
	previousPrivateIPs, hadPrivateIPs := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_SESSION_IDLE_TIMEOUT", idleTimeout.String()); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "set sibling idle timeout: " + err.Error()}
	}
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true"); err != nil {
		restoreEnvironmentValue("SCOUT_SESSION_IDLE_TIMEOUT", previousIdleTimeout, hadIdleTimeout)
		return scenarioResult{ID: fixture.ID, Error: "allow fixture access: " + err.Error()}
	}
	transport, startErr := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	restoreEnvironmentValue("SCOUT_SESSION_IDLE_TIMEOUT", previousIdleTimeout, hadIdleTimeout)
	restoreEnvironmentValue("SCOUT_ALLOW_PRIVATE_IPS", previousPrivateIPs, hadPrivateIPs)
	if startErr != nil {
		return scenarioResult{ID: fixture.ID, Error: "start sibling idle MCP server: " + startErr.Error()}
	}
	defer transport.Close()
	mcpClient := client.New(transport)
	if _, err := mcpClient.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize sibling idle server: " + err.Error()}
	}
	calls, outputBytes := 1, 0
	call := func(name string, args map[string]any) ([]byte, error) {
		result, err := mcpCall(ctx, mcpClient, name, args)
		calls++
		if err != nil {
			return nil, err
		}
		payload := mcpResultJSON(result)
		outputBytes += len(payload)
		return payload, nil
	}
	navigate := func() (*agent.PageResult, error) {
		payload, err := call("navigate", map[string]any{"url": serverURL + fixture.StartingPath})
		if err != nil {
			return nil, err
		}
		var page agent.PageResult
		if err := json.Unmarshal(payload, &page); err != nil {
			return nil, err
		}
		return &page, nil
	}
	if _, err := call("create_session", map[string]any{"name": "idle_sibling"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "create idle sibling: " + err.Error()}
	}
	idlePage, err := navigate()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "start idle sibling: " + err.Error()}
	}
	if _, err := call("create_session", map[string]any{"name": "active_sibling"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "create active sibling: " + err.Error()}
	}
	activePage, err := navigate()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "start active sibling: " + err.Error()}
	}
	if idlePage.TargetID == "" || activePage.TargetID == "" || idlePage.TargetID == activePage.TargetID {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: fmt.Sprintf("named sessions did not start distinct targets: idle=%s active=%s", idlePage.TargetID, activePage.TargetID)}
	}
	// Keep one named session active while the other receives no calls.
	activeUntil := time.Now().Add(2 * idleTimeout)
	for time.Now().Before(activeUntil) {
		if _, err := call("status", map[string]any{}); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "active sibling failed during idle window: " + err.Error()}
		}
		time.Sleep(idleTimeout / 4)
	}
	payload, err := call("list_sessions", map[string]any{})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "read sibling lifecycle state: " + err.Error()}
	}
	var sessions []benchmarkSessionSummary
	if err := json.Unmarshal(payload, &sessions); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, OutputBytes: outputBytes, Error: "decode sibling lifecycle state: " + err.Error()}
	}
	var idleSummary, activeSummary *benchmarkSessionSummary
	for i := range sessions {
		switch sessions[i].Name {
		case "idle_sibling":
			idleSummary = &sessions[i]
		case "active_sibling":
			activeSummary = &sessions[i]
		}
	}
	passed := (idleSummary == nil || !idleSummary.Started) && activeSummary != nil && activeSummary.Started && activeSummary.BrowserAlive
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "idle_session_evicted_active_sibling_alive", Target: idlePage.TargetID + "/" + activePage.TargetID, ActionCalls: calls, OutputBytes: outputBytes, Evidence: fmt.Sprintf("idle_session_absent=%t; idle_started=%t; active_started=%t; active_browser_alive=%t; active_target_distinct=%t", idleSummary == nil, idleSummary != nil && idleSummary.Started, activeSummary != nil && activeSummary.Started, activeSummary != nil && activeSummary.BrowserAlive, idlePage.TargetID != activePage.TargetID)}
	if !passed {
		result.Error = fmt.Sprintf("sibling lifecycle state mismatch: idle=%+v active=%+v", idleSummary, activeSummary)
	}
	return result
}

func restoreEnvironmentValue(name, value string, existed bool) {
	if existed {
		_ = os.Setenv(name, value)
		return
	}
	_ = os.Unsetenv(name)
}

func valueOrEmpty(summary *benchmarkSessionSummary, get func(*benchmarkSessionSummary) string) string {
	if summary == nil {
		return ""
	}
	return get(summary)
}

func outcomeName(err error) string {
	var outcomeErr *agent.ActionOutcomeError
	if errors.As(err, &outcomeErr) {
		return string(outcomeErr.Outcome)
	}
	return ""
}

func mcpErrorEnvelope(err error) (code, cause, message string) {
	if err == nil {
		return "", "", ""
	}
	const marker = "SCOUT_ERROR "
	text := err.Error()
	index := strings.Index(text, marker)
	if index < 0 {
		return "", "", text
	}
	var envelope struct {
		Code    string `json:"code"`
		Cause   string `json:"cause"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(text[index+len(marker):]), &envelope) != nil {
		return "", "", text
	}
	return envelope.Code, envelope.Cause, envelope.Message
}

func run() (retErr error) {
	var fixtures fixtureSet
	if err := json.Unmarshal(fixtureJSON, &fixtures); err != nil {
		return fmt.Errorf("decode benchmark fixtures: %w", err)
	}
	if fixtures.Version != benchmarkVersion {
		return fmt.Errorf("fixture version %q does not match runner version %q", fixtures.Version, benchmarkVersion)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "verified-action" {
		return runVerifiedActionFocus(fixtures)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "background-tab-action" {
		return runBackgroundTabFocus(fixtures)
	}
	if len(fixtures.Scenarios) != 44 {
		return fmt.Errorf("fixture version %s declares %d scenarios; expected 44", fixtures.Version, len(fixtures.Scenarios))
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "late-action-cancellation" || focus == "silent-handshake" || focus == "mcp-handshake-watchdog" || focus == "cli-handshake-deadline" {
		var result scenarioResult
		if focus == "late-action-cancellation" {
			result = runLateActionCancellation(fixtureScenario{ID: "late-action-cancellation"})
		} else if focus == "silent-handshake" {
			result = runSilentCDPHandshakeDeadline(fixtureScenario{ID: "silent-cdp-handshake-deadline"})
		} else if focus == "mcp-handshake-watchdog" {
			result = runMCPHandshakeWatchdog(fixtureScenario{ID: "mcp-handshake-watchdog"})
		} else {
			result = runCLIHandshakeDeadline(fixtureScenario{ID: "cli-handshake-deadline"})
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "cli-recovery-file" {
		result := runCLIRecoveryFile(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "cli-frame-close" {
		result := runCLIFrameClose(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "cli-session-parity" {
		repeats := 5
		if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
			}
			repeats = parsed
		}
		result := runCLISessionParityRepeated(fixtureScenario{ID: focus}, benchmarkBaseURL(), repeats)
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-recovery-record" {
		result := runMCPRecoveryRecord(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-late-action-cancellation" {
		repeats := 10
		if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
			}
			repeats = parsed
		}
		result := runMCPLateActionCancellationRepeated(fixtureScenario{ID: focus}, benchmarkBaseURL(), repeats)
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-explicit-session-routing" {
		result := runMCPExplicitSessionRouting(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-concurrent-target-race" {
		result := runMCPConcurrentTargetRace(fixtureScenario{ID: focus}, false)
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-concurrent-target-race" {
		result := runMCPConcurrentTargetRace(fixtureScenario{ID: focus}, true)
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-session-owner-race" {
		repeats := 1
		if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
			}
			repeats = parsed
		}
		rows := make([]scenarioResult, 0, repeats)
		for repetition := 1; repetition <= repeats; repetition++ {
			rows = append(rows, runMCPSessionOwnerRace(fixtureScenario{ID: fmt.Sprintf("%s-%02d", focus, repetition)}))
		}
		passed := 0
		for _, row := range rows {
			if row.Success {
				passed++
			}
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			ID          string           `json:"id"`
			Success     bool             `json:"success"`
			Repetitions int              `json:"repetitions"`
			Passed      int              `json:"passed"`
			Results     []scenarioResult `json:"results"`
		}{focus, passed == repeats, repeats, passed, rows})
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-shared-remote-ownership" {
		result := runMCPHTTPSharedRemoteOwnership(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-principal-isolation" {
		repeats := 1
		if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
			}
			repeats = parsed
		}
		rows := make([]scenarioResult, 0, repeats)
		for repetition := 1; repetition <= repeats; repetition++ {
			result := runMCPHTTPPrincipalIsolation(fixtureScenario{ID: fmt.Sprintf("%s-%02d", focus, repetition)})
			rows = append(rows, result)
		}
		passed := 0
		for _, row := range rows {
			if row.Success {
				passed++
			}
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			ID          string           `json:"id"`
			Success     bool             `json:"success"`
			Repetitions int              `json:"repetitions"`
			Passed      int              `json:"passed"`
			Results     []scenarioResult `json:"results"`
		}{focus, passed == repeats, repeats, passed, rows})
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-delete-cleanup" {
		result := runMCPHTTPDeleteCleanup(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-close-drain" {
		result := runMCPHTTPCloseDrain(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-idle-stream" {
		result := runMCPHTTPIdleStream(fixtureScenario{ID: focus})
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-remote-idle" {
		var remoteFixture fixtureScenario
		for _, candidate := range fixtures.Scenarios {
			if candidate.ID == "mcp-remote-session-idle-disconnect" {
				remoteFixture = candidate
				break
			}
		}
		if remoteFixture.ID == "" {
			return fmt.Errorf("missing mcp-remote-session-idle-disconnect fixture")
		}
		repeats := 1
		if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
			}
			repeats = parsed
		}
		fixtureServer := httptest.NewServer(benchmarkfixture.Handler())
		defer fixtureServer.Close()
		rows := make([]scenarioResult, 0, repeats)
		for repetition := 1; repetition <= repeats; repetition++ {
			result := runMCPHTTPRemoteIdleExpiration(fixtureServer.URL, remoteFixture)
			result.ID = fmt.Sprintf("%s-%02d", focus, repetition)
			rows = append(rows, result)
		}
		passed := 0
		for _, row := range rows {
			if row.Success {
				passed++
			}
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			ID            string           `json:"id"`
			Success       bool             `json:"success"`
			Repetitions   int              `json:"repetitions"`
			Passed        int              `json:"passed"`
			IdleTimeoutMS int              `json:"idle_timeout_ms"`
			Results       []scenarioResult `json:"results"`
		}{focus, passed == repeats, repeats, passed, 600, rows})
	}
	if focus := os.Getenv("SCOUT_BENCHMARK_FOCUS"); focus == "mcp-http-idle-inflight" {
		buildCtx, buildCancel := context.WithTimeout(context.Background(), 60*time.Second)
		binaryPath, cleanupBinary, buildErr := buildBenchmarkScoutCLI(buildCtx)
		buildCancel()
		if buildErr != nil {
			return fmt.Errorf("build Scout CLI for HTTP lifecycle probe: %w", buildErr)
		}
		defer cleanupBinary()
		repeats := 1
		if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
			}
			repeats = parsed
		}
		rows := make([]scenarioResult, 0, repeats)
		for repetition := 1; repetition <= repeats; repetition++ {
			result := runMCPHTTPIdleInFlight(fixtureScenario{ID: focus, StartingPath: "/delayed-element", Selector: "#delayed-ready"}, binaryPath)
			result.ID = fmt.Sprintf("%s-%02d", focus, repetition)
			rows = append(rows, result)
		}
		passed := 0
		for _, row := range rows {
			if row.Success {
				passed++
			}
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			ID          string           `json:"id"`
			Success     bool             `json:"success"`
			Repetitions int              `json:"repetitions"`
			Passed      int              `json:"passed"`
			IdleTimeout string           `json:"idle_timeout"`
			DelayMS     string           `json:"delay_ms"`
			Results     []scenarioResult `json:"results"`
		}{focus, passed == repeats, repeats, passed, os.Getenv("SCOUT_BENCHMARK_IDLE_TIMEOUT"), os.Getenv("SCOUT_BENCHMARK_WAIT_DELAY_MS"), rows})
	}
	fixture := func(id string) (fixtureScenario, error) {
		for _, scenario := range fixtures.Scenarios {
			if scenario.ID == id {
				return scenario, nil
			}
		}
		return fixtureScenario{}, fmt.Errorf("scenario %q missing from fixture version %s", id, fixtures.Version)
	}
	fixtureHandler, dispatchObserved := benchmarkfixture.HandlerWithDispatchSignal()
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	mcpTransport, err := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	if err != nil {
		return fmt.Errorf("start MCP transport: %w", err)
	}
	defer mcpTransport.Close()
	mcpClient := client.New(mcpTransport)
	mcpCtx, mcpCancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer mcpCancel()
	if _, err := mcpClient.Initialize(mcpCtx); err != nil {
		return fmt.Errorf("initialize MCP client: %w", err)
	}
	isolatedTransport, err := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	if err != nil {
		return fmt.Errorf("start isolated MCP transport: %w", err)
	}
	defer isolatedTransport.Close()
	isolatedClient := client.New(isolatedTransport)
	if _, err := isolatedClient.Initialize(mcpCtx); err != nil {
		return fmt.Errorf("initialize isolated MCP client: %w", err)
	}
	preStartSessions, err := mcpCall(mcpCtx, mcpClient, "list_sessions", map[string]any{})
	if err != nil {
		return fmt.Errorf("list lazy session: %w", err)
	}
	var lazySessions []struct {
		Name             string `json:"name"`
		Active           bool   `json:"active"`
		Started          bool   `json:"started"`
		BrowserOwnership string `json:"browser_ownership"`
		Recoverable      bool   `json:"recoverable"`
	}
	if err := json.Unmarshal(mcpResultJSON(preStartSessions), &lazySessions); err != nil || len(lazySessions) != 1 || lazySessions[0].Name != "default" || !lazySessions[0].Active || lazySessions[0].Started || lazySessions[0].BrowserOwnership != "scout_launched" || !lazySessions[0].Recoverable {
		return fmt.Errorf("lazy session ownership/status contract mismatch: result=%s decode=%v", mcpResultJSON(preStartSessions), err)
	}
	if _, err := mcpCall(mcpCtx, mcpClient, "configure", map[string]any{"allow_private_ips": true}); err != nil {
		return fmt.Errorf("configure MCP fixture access: %w", err)
	}
	if _, err := mcpCall(mcpCtx, isolatedClient, "configure", map[string]any{"allow_private_ips": true}); err != nil {
		return fmt.Errorf("configure isolated MCP fixture access: %w", err)
	}

	session, err := agent.NewSession(agent.SessionConfig{Headless: true, Timeout: 15 * time.Second, AllowPrivateIPs: true})
	if err != nil {
		return fmt.Errorf("start browser: %w", err)
	}
	defer func() {
		if err := session.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close browser: %w", err)
		}
	}()

	out := report{Version: fixtures.Version}
	add := func(result scenarioResult) {
		out.Scenarios = append(out.Scenarios, result)
		if result.Success {
			out.Completed++
		}
		if result.FalseSuccess {
			out.FalseSuccess++
		}
		if result.NaiveFalseSuccess {
			out.NaiveFalseSuccess++
		}
	}
	silentHandshakeFixture, _ := fixture("silent-cdp-handshake-deadline")
	add(runSilentCDPHandshakeDeadline(silentHandshakeFixture))
	mcpHandshakeFixture, _ := fixture("mcp-handshake-watchdog")
	add(runMCPHandshakeWatchdog(mcpHandshakeFixture))
	cliHandshakeFixture, _ := fixture("cli-handshake-deadline")
	add(runCLIHandshakeDeadline(cliHandshakeFixture))

	disconnectFixture, _ := fixture("remote-disconnect-recovery")
	add(runRemoteDisconnectRecovery(server.URL, dispatchObserved, disconnectFixture))
	externalCloseFixture, _ := fixture("target-gone-after-external-close")
	add(runExternalTargetClose(server.URL, externalCloseFixture))
	externalCloseMCPFixture, _ := fixture("target-gone-mcp-external-close")
	add(runMCPExternalTargetClose(server.URL, externalCloseMCPFixture))
	endpointFixture, _ := fixture("mcp-remote-endpoint-sticky")
	add(runMCPRemoteEndpointStickiness(server.URL, endpointFixture))
	idleFixture, _ := fixture("mcp-session-idle-expiration")
	add(runMCPSessionIdleExpiration(server.URL, idleFixture))
	inFlightIdleFixture, _ := fixture("mcp-session-idle-inflight-protection")
	add(runMCPSessionIdleInFlight(server.URL, inFlightIdleFixture))
	remoteIdleFixture, _ := fixture("mcp-remote-session-idle-disconnect")
	add(runMCPRemoteIdleExpiration(server.URL, remoteIdleFixture))
	siblingIdleFixture, _ := fixture("mcp-session-idle-isolation")
	add(runMCPSessionIdleIsolation(server.URL, siblingIdleFixture))

	if _, err := session.Navigate(server.URL + "/large"); err != nil {
		return fmt.Errorf("navigate large fixture: %w", err)
	}
	invalidWait, _ := fixture("invalid-selector-fast-fail")
	waitStarted := time.Now()
	waitErr := session.WaitFor(invalidWait.Selector)
	goWaitDuration := time.Since(waitStarted)
	goWaitFastFail := waitErr != nil && strings.Contains(strings.ToLower(waitErr.Error()), "invalid selector") && goWaitDuration < 2*time.Second
	_, mcpNavigateErr := mcpCall(mcpCtx, mcpClient, "navigate", map[string]any{"url": server.URL + "/large"})
	var mcpWaitErr error
	mcpWaitDuration := time.Duration(0)
	if mcpNavigateErr == nil {
		waitStarted = time.Now()
		_, mcpWaitErr = mcpCall(mcpCtx, mcpClient, "wait_for", map[string]any{"selector": invalidWait.Selector})
		mcpWaitDuration = time.Since(waitStarted)
	}
	mcpWaitFastFail := mcpWaitErr != nil && strings.Contains(strings.ToLower(mcpWaitErr.Error()), "invalid selector") && mcpWaitDuration < 2*time.Second
	waitError := ""
	if !goWaitFastFail {
		waitError = fmt.Sprintf("Go wait did not fail fast: err=%v duration=%s", waitErr, goWaitDuration)
	} else if mcpNavigateErr != nil {
		waitError = "MCP navigation failed: " + mcpNavigateErr.Error()
	} else if !mcpWaitFastFail {
		waitError = fmt.Sprintf("MCP wait did not fail fast: err=%v duration=%s", mcpWaitErr, mcpWaitDuration)
	}
	add(scenarioResult{ID: invalidWait.ID, Success: goWaitFastFail && mcpWaitFastFail, Verified: goWaitFastFail && mcpWaitFastFail, Outcome: "invalid_selector_fast_fail", ActionCalls: 2, OutputBytes: len(fmt.Sprint(waitErr)) + len(fmt.Sprint(mcpWaitErr)), DurationMicros: max(goWaitDuration, mcpWaitDuration).Microseconds(), Error: waitError})

	canceledWait, _ := fixture("wait-context-cancellation")
	cancelCtx, cancelWait := context.WithCancel(context.Background())
	cancelWait() // Prove cancellation is honored before acquiring the session lock.
	waitStarted = time.Now()
	canceledGoErr := session.WaitForContext(cancelCtx, canceledWait.Selector)
	cancelGoDuration := time.Since(waitStarted)
	cancelGoOK := errors.Is(canceledGoErr, context.Canceled) && cancelGoDuration < time.Second
	cancelWaitError := ""
	if !cancelGoOK {
		cancelWaitError = fmt.Sprintf("Go wait cancellation mismatch: err=%v duration=%s", canceledGoErr, cancelGoDuration)
	}
	add(scenarioResult{ID: canceledWait.ID, Success: cancelGoOK, Verified: cancelGoOK, Outcome: "go_wait_canceled", ActionCalls: 1, OutputBytes: len(fmt.Sprint(canceledGoErr)), DurationMicros: cancelGoDuration.Microseconds(), Error: cancelWaitError})

	rootControl, _ := fixture("scope-root-control")
	if _, err := session.Navigate(server.URL + rootControl.StartingPath); err != nil {
		return fmt.Errorf("navigate scoped control fixture: %w", err)
	}
	rootObs, rootErr := session.ObserveScoped(agent.ObserveOptions{Sections: []string{rootControl.RequiredScope}})
	if rootErr != nil {
		add(scenarioResult{ID: rootControl.ID, ActionCalls: 1, Error: rootErr.Error()})
	} else {
		encoded, _ := json.Marshal(rootObs)
		inputFound := false
		labelLeaked := false
		for _, input := range rootObs.Inputs {
			if input.ID == rootControl.ExpectedInputID {
				inputFound = true
				labelLeaked = strings.Contains(input.Label, rootControl.ForbiddenLabel)
			}
		}
		outsideControlFound := len(rootObs.Buttons) > 0
		passed := rootObs.TargetID != "" && rootObs.ScopeMatched == 1 && inputFound && !labelLeaked && !outsideControlFound
		add(scenarioResult{ID: rootControl.ID, Success: passed, Verified: inputFound && !labelLeaked && !outsideControlFound, Outcome: "scope_root_control", FalseSuccess: rootObs.ScopeMatched == 0 && inputFound, ActionCalls: 1, OutputBytes: len(encoded)})
	}
	if _, err := session.Navigate(server.URL + "/large"); err != nil {
		return fmt.Errorf("navigate large fixture: %w", err)
	}
	scopeMiss, _ := fixture("scope-excludes-decoy")
	miss, missErr := session.ObserveScoped(agent.ObserveOptions{Sections: []string{scopeMiss.RequiredScope}, LimitChars: 8000})
	if missErr != nil {
		add(scenarioResult{ID: "scope-excludes-decoy", ActionCalls: 1, Error: missErr.Error()})
	} else {
		encoded, _ := json.Marshal(miss)
		missed := miss.ScopeMatched > 0 && !strings.Contains(miss.Text, scopeMiss.ExpectedText)
		falseSuccess := scopeMiss.ExpectedMiss && !missed
		passed := missed == scopeMiss.ExpectedMiss && miss.TargetID != ""
		outcome := "scope_miss"
		if !missed {
			outcome = "scope_leak"
		}
		add(scenarioResult{ID: scopeMiss.ID, Success: passed, Verified: missed, Outcome: outcome, FalseSuccess: falseSuccess, ActionCalls: 1, OutputBytes: len(encoded)})
	}
	large, _ := fixture("scoped-large-page")
	obs, err := session.ObserveScoped(agent.ObserveOptions{Sections: []string{large.RequiredScope}, LimitChars: 8000})
	if err != nil {
		add(scenarioResult{ID: "scoped-large-page", ActionCalls: 1, Error: err.Error()})
	} else {
		encoded, _ := json.Marshal(obs)
		hasTarget := strings.Contains(obs.Text, large.ExpectedText)
		excludesDecoy := !strings.Contains(obs.Text, large.ForbiddenText)
		withinBudget := len(encoded) <= large.MaxOutputBytes
		identityPresent := obs.TargetID != ""
		add(scenarioResult{ID: large.ID, Success: identityPresent && hasTarget && excludesDecoy && withinBudget && obs.ScopeMatched > 0, Verified: identityPresent && hasTarget && excludesDecoy && obs.ScopeMatched > 0, Outcome: "scoped_observation", Target: obs.TargetID, ActionCalls: 1, OutputBytes: len(encoded), ExpectedBytesMax: large.MaxOutputBytes})
	}

	if _, err := session.Navigate(server.URL + "/action"); err != nil {
		return fmt.Errorf("navigate action fixture: %w", err)
	}
	health, _ := fixture("session-health-ownership")
	if _, err := mcpCall(mcpCtx, mcpClient, "navigate", map[string]any{"url": server.URL + health.StartingPath}); err != nil {
		add(scenarioResult{ID: health.ID, ActionCalls: 1, Error: "MCP health fixture navigation: " + err.Error()})
	} else {
		statusResult, statusErr := mcpCall(mcpCtx, mcpClient, "status", map[string]any{})
		listResult, listErr := mcpCall(mcpCtx, mcpClient, "list_sessions", map[string]any{})
		var status agent.SessionStatus
		var summaries []struct {
			Name             string `json:"name"`
			Active           bool   `json:"active"`
			Started          bool   `json:"started"`
			BrowserAlive     bool   `json:"browser_alive"`
			BrowserOwnership string `json:"browser_ownership"`
			SessionDead      bool   `json:"session_dead"`
			Recoverable      bool   `json:"recoverable"`
			CurrentURL       string `json:"current_url"`
		}
		if statusErr == nil {
			statusErr = json.Unmarshal(mcpResultJSON(statusResult), &status)
		}
		if listErr == nil {
			listErr = json.Unmarshal(mcpResultJSON(listResult), &summaries)
		}
		var listed *struct {
			Name             string `json:"name"`
			Active           bool   `json:"active"`
			Started          bool   `json:"started"`
			BrowserAlive     bool   `json:"browser_alive"`
			BrowserOwnership string `json:"browser_ownership"`
			SessionDead      bool   `json:"session_dead"`
			Recoverable      bool   `json:"recoverable"`
			CurrentURL       string `json:"current_url"`
		}
		for i := range summaries {
			if summaries[i].Name == "default" {
				listed = &summaries[i]
			}
		}
		wantURL := server.URL + health.StartingPath
		passed := statusErr == nil && listErr == nil && listed != nil && status.SessionAlive && status.BrowserAlive && !status.SessionDead && status.Recoverable && status.BrowserOwnership == "scout_launched" && status.CurrentURL == wantURL && status.ActiveTargetID != "" && listed.Active && listed.Started && listed.BrowserAlive && listed.Recoverable && listed.BrowserOwnership == status.BrowserOwnership && listed.CurrentURL == status.CurrentURL && listed.SessionDead == status.SessionDead
		encodedStatus, _ := json.Marshal(status)
		encodedList, _ := json.Marshal(summaries)
		failure := ""
		if statusErr != nil {
			failure = "status: " + statusErr.Error()
		} else if listErr != nil {
			failure = "list_sessions: " + listErr.Error()
		} else if !passed {
			failure = fmt.Sprintf("session status/list mismatch: status=%s sessions=%s", encodedStatus, encodedList)
		}
		add(scenarioResult{ID: health.ID, Success: passed, Verified: passed, Outcome: "session_health_reported", Target: status.ActiveTargetID, ActionCalls: 3, OutputBytes: len(encodedStatus) + len(encodedList), Evidence: fmt.Sprintf("ownership=%s URL=%s browser_alive=%t recoverable=%t", status.BrowserOwnership, status.CurrentURL, status.BrowserAlive, status.Recoverable), Error: failure})
	}
	isolation, _ := fixture("mcp-process-session-isolation")
	var isolationCalls, isolationBytes int
	var primaryTarget, isolatedTarget string
	var isolationEvidence []string
	isolationError := ""
	navigateMCP := func(c *client.Client, path string) (agent.PageResult, error) {
		result, callErr := mcpCall(mcpCtx, c, "navigate", map[string]any{"url": server.URL + path})
		isolationCalls++
		if callErr != nil {
			return agent.PageResult{}, callErr
		}
		isolationBytes += len(mcpResultJSON(result))
		var page agent.PageResult
		if decodeErr := json.Unmarshal(mcpResultJSON(result), &page); decodeErr != nil {
			return page, decodeErr
		}
		if page.TargetID == "" {
			return page, errors.New("navigation omitted target_id")
		}
		return page, nil
	}
	extractState := func(c *client.Client) (agent.ElementResult, error) {
		result, callErr := mcpCall(mcpCtx, c, "extract", map[string]any{"selector": "#state"})
		isolationCalls++
		if callErr != nil {
			return agent.ElementResult{}, callErr
		}
		isolationBytes += len(mcpResultJSON(result))
		var extracted agent.ElementResult
		if decodeErr := json.Unmarshal(mcpResultJSON(result), &extracted); decodeErr != nil {
			return extracted, decodeErr
		}
		return extracted, nil
	}
	if page, err := navigateMCP(mcpClient, "/session-owner/seed"); err != nil {
		isolationError = "seed primary MCP process: " + err.Error()
	} else {
		primaryTarget = page.TargetID
		isolationEvidence = append(isolationEvidence, "primary seeded cookie and localStorage")
		if page, err := navigateMCP(isolatedClient, "/session-owner/probe"); err != nil {
			isolationError = "probe isolated MCP process: " + err.Error()
		} else {
			isolatedTarget = page.TargetID
			state, err := extractState(isolatedClient)
			if err != nil {
				isolationError = "read isolated process initial state: " + err.Error()
			} else if state.TargetID != isolatedTarget || state.Text != `["",null]` {
				isolationError = fmt.Sprintf("isolated process saw unexpected state %q on target %s", state.Text, state.TargetID)
			} else if page, err := navigateMCP(isolatedClient, "/session-owner/check"); err != nil {
				isolationError = "check isolated process state: " + err.Error()
			} else {
				state, err = extractState(isolatedClient)
				if err != nil {
					isolationError = "read isolated process state: " + err.Error()
				} else if state.TargetID != page.TargetID || state.Text != `["scout_owner=secondary","secondary"]` {
					isolationError = fmt.Sprintf("isolated process state mismatch: navigation_target=%s extract_target=%s state=%q", page.TargetID, state.TargetID, state.Text)
				} else if page, err := navigateMCP(mcpClient, "/session-owner/check"); err != nil {
					isolationError = "check primary process state: " + err.Error()
				} else {
					isolatedTarget = state.TargetID
					primaryTarget = page.TargetID
					state, err = extractState(mcpClient)
					if err != nil {
						isolationError = "read primary process state: " + err.Error()
					} else if primaryTarget == isolatedTarget || state.TargetID != primaryTarget || state.Text != `["scout_owner=primary","primary"]` {
						isolationError = fmt.Sprintf("primary process state mismatch or target sharing: navigation_target=%s extract_target=%s state=%q", page.TargetID, state.TargetID, state.Text)
					} else {
						isolationEvidence = append(isolationEvidence, "isolated process began empty and retained secondary values", "primary process retained primary values", "target IDs remained distinct")
					}
				}
			}
		}
	}
	passed := isolationError == ""
	add(scenarioResult{ID: isolation.ID, Success: passed, Verified: passed, Outcome: "mcp_process_state_isolated", Target: primaryTarget + "/" + isolatedTarget, ActionCalls: isolationCalls, OutputBytes: isolationBytes, Evidence: strings.Join(isolationEvidence, "; "), Error: isolationError})
	// Identical client-supplied logical labels are scoped to their MCP
	// process. Compare the independent default browser sessions, and verify
	// that a named session can be created separately in each process.
	sessionOverride, _ := fixture("mcp-session-override-isolation")
	var overrideCalls, overrideBytes int
	var overrideEvidence []string
	var overrideError string
	callOverride := func(c *client.Client, tool string, args map[string]any) (*client.ToolResult, error) {
		result, callErr := mcpCall(mcpCtx, c, tool, args)
		overrideCalls++
		if callErr == nil {
			overrideBytes += len(mcpResultJSON(result))
		}
		return result, callErr
	}
	const nameA = "shop"
	const nameB = "shop"
	var ownerA, ownerB agent.PageResult
	if nav, err := callOverride(mcpClient, "navigate", map[string]any{"url": server.URL + "/session-owner/seed"}); err != nil {
		overrideError = "seed first MCP process: " + err.Error()
	} else if err := json.Unmarshal(mcpResultJSON(nav), &ownerA); err != nil || ownerA.TargetID == "" {
		overrideError = fmt.Sprintf("decode first process seed: %v", err)
	} else if nav, err := callOverride(isolatedClient, "navigate", map[string]any{"url": server.URL + "/session-owner/seed"}); err != nil {
		overrideError = "seed second MCP process with same logical session label: " + err.Error()
	} else if err := json.Unmarshal(mcpResultJSON(nav), &ownerB); err != nil || ownerB.TargetID == "" || ownerA.TargetID == ownerB.TargetID {
		overrideError = fmt.Sprintf("same-named MCP processes shared target identity: first=%s second=%s decode=%v", ownerA.TargetID, ownerB.TargetID, err)
	} else if state, err := callOverride(mcpClient, "extract", map[string]any{"selector": "#state"}); err != nil {
		overrideError = "read first process state: " + err.Error()
	} else if got := func() agent.ElementResult {
		var v agent.ElementResult
		_ = json.Unmarshal(mcpResultJSON(state), &v)
		return v
	}(); got.TargetID != ownerA.TargetID || got.Text != `["scout_owner=primary","primary"]` {
		overrideError = fmt.Sprintf("first process state mismatch: target=%s state=%q", got.TargetID, got.Text)
	} else if state, err := callOverride(isolatedClient, "extract", map[string]any{"selector": "#state"}); err != nil {
		overrideError = "read second process state: " + err.Error()
	} else if got := func() agent.ElementResult {
		var v agent.ElementResult
		_ = json.Unmarshal(mcpResultJSON(state), &v)
		return v
	}(); got.TargetID != ownerB.TargetID || got.Text != `["scout_owner=primary","primary"]` {
		overrideError = fmt.Sprintf("second process state mismatch: target=%s state=%q", got.TargetID, got.Text)
	}
	if overrideError == "" {
		if _, err := callOverride(mcpClient, "create_session", map[string]any{"name": nameA}); err != nil {
			overrideError = "create named session in first process: " + err.Error()
		} else if _, err := callOverride(isolatedClient, "create_session", map[string]any{"name": nameB}); err != nil {
			overrideError = "create same-named session in second process: " + err.Error()
		} else if sessions, err := callOverride(mcpClient, "list_sessions", map[string]any{}); err != nil {
			overrideError = "list first process sessions: " + err.Error()
		} else if !strings.Contains(string(mcpResultJSON(sessions)), `"name":"shop"`) {
			overrideError = "first process registry omitted its shop session"
		} else if sessions, err := callOverride(isolatedClient, "list_sessions", map[string]any{}); err != nil {
			overrideError = "list second process sessions: " + err.Error()
		} else if !strings.Contains(string(mcpResultJSON(sessions)), `"name":"shop"`) {
			overrideError = "second process registry omitted its shop session"
		}
	}
	passed = overrideError == ""
	if passed {
		overrideEvidence = append(overrideEvidence, "same logical session name shop was accepted by both independent process registries", "each MCP process retained its own target and cookie/localStorage state", "named session listings were independently scoped to each server")
	}
	add(scenarioResult{ID: sessionOverride.ID, Success: passed, Verified: passed, Outcome: "mcp_session_override_isolated", Target: ownerA.TargetID + "/" + ownerB.TargetID, ActionCalls: overrideCalls, OutputBytes: overrideBytes, Evidence: strings.Join(overrideEvidence, "; "), Error: overrideError})
	action, _ := fixture("verified-action")
	clicked, clickErr := session.ClickWithPostcondition(action.Selector, agent.Postcondition{Text: action.ExpectedText}, false)
	if clickErr != nil {
		add(scenarioResult{ID: "verified-action", ActionCalls: 1, Error: clickErr.Error()})
	} else {
		encoded, _ := json.Marshal(clicked)
		add(scenarioResult{ID: action.ID, Success: clicked.Verified && clicked.Outcome == "postcondition_verified", Verified: clicked.Verified, Outcome: clicked.Outcome, VerificationState: string(clicked.Receipt.Verification), Target: clicked.Target, ActionCalls: 1, OutputBytes: len(encoded), Evidence: clicked.Evidence, RecoveryHint: clicked.RecoveryHint})
	}
	cliParity, _ := fixture("cli-verified-action-parity")
	if clickErr != nil {
		add(scenarioResult{ID: cliParity.ID, ActionCalls: 1, Error: "Go baseline click failed; CLI parity cannot be compared"})
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "click", server.URL+"/action", cliParity.Selector, "--expected-text="+cliParity.ExpectedText, "--headless")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		var cliResult agent.PageResult
		parseErr := json.Unmarshal(stdout, &cliResult)
		matches := parseErr == nil && cliResult.TargetID != "" && cliResult.Action == clicked.Action && cliResult.Target == clicked.Target && cliResult.Outcome == clicked.Outcome && cliResult.Verified == clicked.Verified && cliResult.Evidence == clicked.Evidence && cliResult.Receipt != nil && cliResult.Receipt.Verification == clicked.Receipt.Verification
		verificationState := ""
		if cliResult.Receipt != nil {
			verificationState = string(cliResult.Receipt.Verification)
		}
		result := scenarioResult{ID: cliParity.ID, Success: matches, Verified: matches, Outcome: cliResult.Outcome, VerificationState: verificationState, Target: cliResult.Target, ActionCalls: 1, OutputBytes: len(stdout), Evidence: cliResult.Evidence}
		if runErr != nil {
			result.Success = false
			result.Verified = false
			result.Error = runErr.Error()
			if ctx.Err() != nil {
				result.Error = ctx.Err().Error()
			}
			if stderr.Len() > 0 {
				result.Error += ": " + strings.TrimSpace(stderr.String())
			}
		} else if parseErr != nil {
			result.Error = fmt.Sprintf("decode CLI result: %v", parseErr)
		} else if !matches {
			result.Error = "CLI result fields differ from Go baseline"
		}
		add(result)
	}
	mcpClickParity, _ := fixture("mcp-verified-action-parity")
	if clickErr != nil {
		add(scenarioResult{ID: mcpClickParity.ID, ActionCalls: 1, Error: "Go baseline click failed; MCP parity cannot be compared"})
	} else {
		add(runMCPActionParity(mcpCtx, mcpClient, mcpClickParity.ID, server.URL+"/action", "click", mcpClickParity, clicked))
	}
	typed, _ := fixture("verified-type")
	if _, err := session.Navigate(server.URL + typed.StartingPath); err != nil {
		return fmt.Errorf("navigate type fixture: %w", err)
	}
	typeResult, typeErr := session.Type(typed.Selector, typed.ExpectedValue)
	if typeErr != nil {
		add(scenarioResult{ID: typed.ID, ActionCalls: 1, Error: typeErr.Error()})
	} else {
		encoded, _ := json.Marshal(typeResult)
		falseSuccess := typeResult.Verified && typeResult.ValueCommitted != typed.ExpectedValue
		passed := typeResult.Verified && typeResult.ValueCommitted == typed.ExpectedValue && typeResult.Outcome == "value_verified"
		add(scenarioResult{ID: typed.ID, Success: passed, Verified: typeResult.Verified, Outcome: typeResult.Outcome, Target: typeResult.Selector, FalseSuccess: falseSuccess, ActionCalls: 1, OutputBytes: len(encoded), Evidence: typeResult.Evidence, RecoveryHint: typeResult.RecoveryHint, Value: typeResult.Value, ValueCommitted: typeResult.ValueCommitted})
	}
	typeParity, _ := fixture("cli-verified-type-parity")
	if typeErr != nil {
		add(scenarioResult{ID: typeParity.ID, ActionCalls: 1, Error: "Go baseline type failed; CLI parity cannot be compared"})
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "type", server.URL+typeParity.StartingPath, typeParity.Selector, typeParity.ExpectedValue, "--headless")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		var cliResult agent.ElementResult
		parseErr := json.Unmarshal(stdout, &cliResult)
		matches := parseErr == nil && cliResult.TargetID != "" && cliResult.Selector == typeResult.Selector && cliResult.Action == typeResult.Action && cliResult.Value == typeResult.Value && cliResult.ValueCommitted == typeResult.ValueCommitted && cliResult.FrameworkReactive == typeResult.FrameworkReactive && cliResult.Verified == typeResult.Verified && cliResult.Outcome == typeResult.Outcome && cliResult.Evidence == typeResult.Evidence
		result := scenarioResult{ID: typeParity.ID, Success: matches, Verified: matches, Outcome: cliResult.Outcome, Target: cliResult.Selector, ActionCalls: 1, OutputBytes: len(stdout), Evidence: cliResult.Evidence, Value: cliResult.Value, ValueCommitted: cliResult.ValueCommitted}
		if runErr != nil {
			result.Error = runErr.Error()
			if ctx.Err() != nil {
				result.Error = ctx.Err().Error()
			}
			if stderr.Len() > 0 {
				result.Error += ": " + strings.TrimSpace(stderr.String())
			}
		} else if parseErr != nil {
			result.Error = fmt.Sprintf("decode CLI result: %v", parseErr)
		} else if !matches {
			result.Error = "CLI result fields differ from Go baseline"
		}
		add(result)
	}
	mcpTypeParity, _ := fixture("mcp-verified-type-parity")
	if typeErr != nil {
		add(scenarioResult{ID: mcpTypeParity.ID, ActionCalls: 1, Error: "Go baseline type failed; MCP parity cannot be compared"})
	} else {
		add(runMCPActionParity(mcpCtx, mcpClient, mcpTypeParity.ID, server.URL+typeParity.StartingPath, "type", mcpTypeParity, typeResult))
	}

	var rejectionBaseline, acceptanceBaseline *agent.SubmitFormResult
	rejection, _ := fixture("form-rejection")
	if _, err := session.Navigate(server.URL + "/reject-form"); err != nil {
		return fmt.Errorf("navigate rejected form fixture: %w", err)
	}
	rejected, rejectErr := session.SubmitFormWithAcceptance(rejection.FormSelector, rejection.MatchURL, 3000, agent.Postcondition{Text: "Order accepted"})
	if rejectErr != nil {
		add(scenarioResult{ID: rejection.ID, ActionCalls: 1, Error: rejectErr.Error()})
	} else {
		rejectionBaseline = rejected
		encoded, _ := json.Marshal(rejected)
		falseSuccess := rejected.Verified || rejected.ApplicationAccepted || rejected.Outcome != rejection.ExpectedFailure || rejected.StatusCode != rejection.ExpectedStatus
		accepted := rejected.ApplicationAccepted
		observed := rejected.ResponseObserved
		add(scenarioResult{ID: rejection.ID, Success: !falseSuccess, Verified: rejected.Verified, ApplicationAccepted: &accepted, ResponseObserved: &observed, StatusCode: rejected.StatusCode, Outcome: rejected.Outcome, FalseSuccess: falseSuccess, ActionCalls: 1, OutputBytes: len(encoded), Evidence: rejected.Evidence, RecoveryHint: rejected.RecoveryHint})
		trap, _ := fixture("optimistic-ui-false-success")
		status, statusErr := session.Extract("#status")
		naiveFalseSuccess := statusErr == nil && strings.TrimSpace(status.Text) == trap.ExpectedText && rejected.StatusCode >= 400
		structuredCorrect := naiveFalseSuccess && rejected.ResponseObserved && !rejected.ApplicationAccepted && rejected.Outcome == trap.ExpectedFailure && rejected.StatusCode == trap.ExpectedStatus
		trapResult := scenarioResult{
			ID: trap.ID, Success: structuredCorrect, Verified: rejected.Verified,
			ApplicationAccepted: &accepted, ResponseObserved: &observed,
			StatusCode: rejected.StatusCode, Outcome: rejected.Outcome,
			FalseSuccess:      naiveFalseSuccess && rejected.ApplicationAccepted,
			NaiveFalseSuccess: naiveFalseSuccess,
			Classification:    "text_only=success; response_backed=reject",
			ActionCalls:       1, OutputBytes: len(encoded), Evidence: rejected.Evidence,
		}
		if statusErr != nil {
			trapResult.Error = "read optimistic status text: " + statusErr.Error()
		} else if !structuredCorrect {
			trapResult.Error = fmt.Sprintf("false-success trap did not hold: text=%q status=%d accepted=%t outcome=%s", strings.TrimSpace(status.Text), rejected.StatusCode, rejected.ApplicationAccepted, rejected.Outcome)
		}
		add(trapResult)
		rejectionParity, _ := fixture("cli-rejected-form-parity")
		add(runSubmitParity(rejectionParity.ID, server.URL+rejectionParity.StartingPath, rejectionParity, rejected))
		rejectionMCPParity, _ := fixture("mcp-rejected-form-parity")
		add(runMCPActionParity(mcpCtx, mcpClient, rejectionMCPParity.ID, server.URL+rejectionMCPParity.StartingPath, "submit", rejectionMCPParity, rejected))
	}
	accepted, _ := fixture("form-acceptance")
	if _, err := session.Navigate(server.URL + "/form"); err != nil {
		return fmt.Errorf("navigate accepted form fixture: %w", err)
	}
	submitted, submitErr := session.SubmitFormWithAcceptance(accepted.FormSelector, accepted.MatchURL, 3000, agent.Postcondition{Text: accepted.ExpectedText})
	if submitErr != nil {
		add(scenarioResult{ID: accepted.ID, ActionCalls: 1, Error: submitErr.Error()})
	} else {
		acceptanceBaseline = submitted
		encoded, _ := json.Marshal(submitted)
		applicationAccepted := submitted.ApplicationAccepted
		passed := submitted.Verified && submitted.ApplicationAccepted && submitted.Outcome == "postcondition_verified"
		falseSuccess := submitted.ApplicationAccepted && (submitted.StatusCode < 200 || submitted.StatusCode >= 400)
		observed := submitted.ResponseObserved
		add(scenarioResult{ID: accepted.ID, Success: passed, Verified: submitted.Verified, ApplicationAccepted: &applicationAccepted, ResponseObserved: &observed, StatusCode: submitted.StatusCode, Outcome: submitted.Outcome, FalseSuccess: falseSuccess, ActionCalls: 1, OutputBytes: len(encoded), Evidence: submitted.Evidence, RecoveryHint: submitted.RecoveryHint})
		acceptanceParity, _ := fixture("cli-accepted-form-parity")
		add(runSubmitParity(acceptanceParity.ID, server.URL+acceptanceParity.StartingPath, acceptanceParity, submitted))
		acceptanceMCPParity, _ := fixture("mcp-accepted-form-parity")
		add(runMCPActionParity(mcpCtx, mcpClient, acceptanceMCPParity.ID, server.URL+acceptanceMCPParity.StartingPath, "submit", acceptanceMCPParity, submitted))
	}
	add(runCLISessionParityAtBase(fixtureScenario{ID: "cli-session-parity"}, server.URL, typeResult, rejectionBaseline, acceptanceBaseline, typeErr))

	replay, _ := fixture("playbook-replay")
	playbook := &agent.Playbook{Name: "benchmark", URL: server.URL + replay.StartingPath, Actions: []agent.Action{{Type: "click", Selector: replay.ActionSelector, Expected: &agent.ActionExpect{Text: replay.ExpectedText}}}}
	replayed, replayErr := session.ReplayPlaybook(playbook)
	if replayErr != nil {
		add(scenarioResult{ID: replay.ID, ActionCalls: 1, Error: replayErr.Error()})
	} else {
		encoded, _ := json.Marshal(replayed)
		valid := replayed.Success && replayed.StepsRun == 1 && replayed.PlaybookSHA256 != ""
		add(scenarioResult{ID: replay.ID, Success: valid, Verified: valid, Outcome: "playbook_replayed", ActionCalls: 1, OutputBytes: len(encoded)})
	}
	changed, _ := fixture("playbook-change-detection")
	changedPlaybook := &agent.Playbook{Name: "benchmark-changed-expectation", URL: server.URL + changed.StartingPath, Actions: []agent.Action{{Type: "click", Selector: changed.ActionSelector, Expected: &agent.ActionExpect{Text: changed.ChangedText}}}}
	failed, failureErr := session.ReplayPlaybook(changedPlaybook)
	if failureErr != nil {
		add(scenarioResult{ID: changed.ID, ActionCalls: 1, Error: failureErr.Error()})
	} else {
		encoded, _ := json.Marshal(failed)
		falseSuccess := failed.Success || failed.FailurePhase != "expectation" || failed.FailedAt != 1 || failed.PlaybookSHA256 == "" || failed.Evidence == "" || failed.RecoveryHint == ""
		add(scenarioResult{ID: changed.ID, Success: !falseSuccess, Verified: !failed.Success, Outcome: failed.FailurePhase, FalseSuccess: falseSuccess, ActionCalls: 1, OutputBytes: len(encoded)})
	}
	resume, _ := fixture("playbook-resume-after-repair")
	resumePlaybook := &agent.Playbook{Name: "benchmark-resume", URL: server.URL + resume.StartingPath, Actions: []agent.Action{
		{Type: "click", Selector: resume.ActionSelector, Expected: &agent.ActionExpect{Text: resume.ExpectedText}},
		{Type: "wait", Selector: "#late"},
	}}
	interrupted, interruptErr := session.ReplayPlaybook(resumePlaybook)
	if interruptErr != nil {
		add(scenarioResult{ID: resume.ID, ActionCalls: 1, Error: interruptErr.Error()})
	} else if interrupted.Success || interrupted.FailedAt != 2 || interrupted.StepsRun != 1 || interrupted.FailurePhase != "action" {
		add(scenarioResult{ID: resume.ID, ActionCalls: 1, Error: fmt.Sprintf("replay did not stop at the missing second-step target: success=%t failed_at=%d steps_run=%d phase=%q", interrupted.Success, interrupted.FailedAt, interrupted.StepsRun, interrupted.FailurePhase)})
	} else if _, err := session.Click(resume.RepairSelector); err != nil {
		add(scenarioResult{ID: resume.ID, ActionCalls: 2, Error: fmt.Sprintf("repair fixture: %v", err)})
	} else {
		resumed, resumeErr := session.ResumePlaybook(resumePlaybook, interrupted)
		if resumeErr != nil {
			add(scenarioResult{ID: resume.ID, ActionCalls: 3, Error: resumeErr.Error()})
		} else {
			encoded, _ := json.Marshal(resumed)
			valid := resumed.Success && resumed.FailedAt == 0 && resumed.StepsRun == 2 && resumed.PlaybookSHA256 == interrupted.PlaybookSHA256
			add(scenarioResult{ID: resume.ID, Success: valid, Verified: valid, Outcome: "playbook_resumed_after_repair", ActionCalls: 3, OutputBytes: len(encoded)})
		}
	}

	shadow, _ := fixture("shadow-root-action")
	if _, err := session.Navigate(server.URL + shadow.StartingPath); err != nil {
		return fmt.Errorf("navigate shadow fixture: %w", err)
	}
	shadowResult, shadowErr := session.ClickWithPostcondition(shadow.ActionSelector, agent.Postcondition{Text: shadow.ExpectedText}, false)
	if shadowErr != nil {
		add(scenarioResult{ID: shadow.ID, ActionCalls: 1, Error: shadowErr.Error()})
	} else {
		encoded, _ := json.Marshal(shadowResult)
		add(scenarioResult{ID: shadow.ID, Success: shadowResult.Verified && shadowResult.Outcome == "postcondition_verified", Verified: shadowResult.Verified, Outcome: shadowResult.Outcome, ActionCalls: 1, OutputBytes: len(encoded)})
	}

	frame, _ := fixture("iframe-action")
	if _, err := session.Navigate(server.URL + frame.StartingPath); err != nil {
		return fmt.Errorf("navigate iframe fixture: %w", err)
	}
	if _, err := session.SwitchToFrame(frame.FrameSelector); err != nil {
		add(scenarioResult{ID: frame.ID, ActionCalls: 1, Error: err.Error()})
	} else {
		frameResult, frameErr := session.ClickWithPostcondition(frame.ActionSelector, agent.Postcondition{Text: frame.ExpectedText}, false)
		_, mainFrameErr := session.SwitchToMainFrame()
		if frameErr != nil || mainFrameErr != nil {
			cause := frameErr
			if cause == nil {
				cause = mainFrameErr
			}
			add(scenarioResult{ID: frame.ID, ActionCalls: 1, Error: cause.Error()})
		} else {
			encoded, _ := json.Marshal(frameResult)
			identityPresent := frameResult.TargetID != "" && frameResult.FrameID != ""
			add(scenarioResult{ID: frame.ID, Success: identityPresent && frameResult.Verified && frameResult.Outcome == "postcondition_verified", Verified: identityPresent && frameResult.Verified, Outcome: frameResult.Outcome, Target: frameResult.TargetID, ActionCalls: 1, OutputBytes: len(encoded), Evidence: frameResult.Evidence, RecoveryHint: frameResult.RecoveryHint})
		}
	}

	tabParity, _ := fixture("tab-lifecycle-parity")
	tabURL := server.URL + "/action"
	goOpen, goOpenErr := session.OpenTab("parity-tab", tabURL)
	if goOpenErr != nil {
		add(scenarioResult{ID: tabParity.ID, ActionCalls: 1, Error: fmt.Sprintf("Go open tab: %v", goOpenErr)})
	} else {
		goSwitch, goSwitchErr := session.SwitchTab("default")
		goClose, goCloseErr := session.CloseTabResult("parity-tab")
		goTabs, goTabsErr := session.ListTabs()
		goTabAbsent, goDefaultActive := true, false
		for _, tab := range goTabs {
			if tab.Name == "parity-tab" {
				goTabAbsent = false
			}
			goDefaultActive = goDefaultActive || (tab.Name == "default" && tab.Active)
		}
		goTabOK := goSwitchErr == nil && goCloseErr == nil && goTabsErr == nil &&
			goOpen.Action == "opened_tab" && goOpen.Target == "parity-tab" && goOpen.TargetID != "" && goOpen.URL == tabURL && goOpen.Verified && goOpen.Outcome == string(agent.OutcomePostconditionVerified) && goOpen.Evidence != "" &&
			goSwitch.Action == "switched_tab" && goSwitch.Target == "default" && goSwitch.TargetID != "" && goSwitch.Verified && goSwitch.Outcome == string(agent.OutcomePostconditionVerified) && goSwitch.Evidence != "" &&
			goClose.Action == "closed_tab" && goClose.Target == "parity-tab" && goClose.TargetID == goOpen.TargetID && goClose.Verified && goClose.Outcome == string(agent.OutcomePostconditionVerified) && goClose.Evidence != "" && goTabAbsent && goDefaultActive
		goOpenJSON, _ := json.Marshal(goOpen)
		var goSwitchJSON, goCloseJSON []byte
		if goSwitch != nil {
			goSwitchJSON, _ = json.Marshal(goSwitch)
		}
		if goClose != nil {
			goCloseJSON, _ = json.Marshal(goClose)
		}
		goTabsJSON, _ := json.Marshal(goTabs)
		goOutputBytes := len(goOpenJSON) + len(goSwitchJSON) + len(goCloseJSON) + len(goTabsJSON)

		var mcpFailure string
		mcpOpen, err := mcpCall(mcpCtx, mcpClient, "open_tab", map[string]any{"name": "parity-tab", "url": tabURL})
		var mcpOpenResult agent.PageResult
		if err != nil {
			mcpFailure = err.Error()
		} else if err = json.Unmarshal(mcpResultJSON(mcpOpen), &mcpOpenResult); err != nil {
			mcpFailure = "decode MCP open_tab result: " + err.Error()
		}
		var mcpSwitchResult agent.PageResult
		var mcpCloseResult agent.PageResult
		var mcpTabList struct {
			Items []agent.TabInfo `json:"items"`
		}
		mcpOutputBytes := 0
		if mcpFailure == "" {
			mcpOutputBytes += len(mcpResultJSON(mcpOpen))
			mcpSwitch, callErr := mcpCall(mcpCtx, mcpClient, "switch_tab", map[string]any{"name": "default"})
			if callErr != nil {
				mcpFailure = callErr.Error()
			} else {
				mcpOutputBytes += len(mcpResultJSON(mcpSwitch))
				if err := json.Unmarshal(mcpResultJSON(mcpSwitch), &mcpSwitchResult); err != nil {
					mcpFailure = "decode MCP switch_tab result: " + err.Error()
				}
			}
		}
		if mcpFailure == "" {
			mcpClose, callErr := mcpCall(mcpCtx, mcpClient, "close_tab", map[string]any{"name": "parity-tab"})
			if callErr != nil {
				mcpFailure = callErr.Error()
			} else {
				mcpOutputBytes += len(mcpResultJSON(mcpClose))
				if err := json.Unmarshal(mcpResultJSON(mcpClose), &mcpCloseResult); err != nil {
					mcpFailure = "decode MCP close_tab result: " + err.Error()
				}
			}
		}
		if mcpFailure == "" {
			mcpTabs, callErr := mcpCall(mcpCtx, mcpClient, "list_tabs", map[string]any{})
			if callErr != nil {
				mcpFailure = callErr.Error()
			} else {
				mcpOutputBytes += len(mcpResultJSON(mcpTabs))
				if err := json.Unmarshal(mcpResultJSON(mcpTabs), &mcpTabList); err != nil {
					mcpFailure = "decode MCP list_tabs result: " + err.Error()
				}
			}
		}
		mcpTabAbsent, mcpDefaultActive := true, false
		for _, tab := range mcpTabList.Items {
			if tab.Name == "parity-tab" {
				mcpTabAbsent = false
			}
			mcpDefaultActive = mcpDefaultActive || (tab.Name == "default" && tab.Active)
		}
		mcpTabOK := mcpFailure == "" &&
			mcpOpenResult.Action == goOpen.Action && mcpOpenResult.Target == goOpen.Target && mcpOpenResult.TargetID != "" && mcpOpenResult.URL == tabURL && mcpOpenResult.Verified == goOpen.Verified && mcpOpenResult.Outcome == goOpen.Outcome && mcpOpenResult.Evidence != "" &&
			mcpSwitchResult.Action == goSwitch.Action && mcpSwitchResult.Target == goSwitch.Target && mcpSwitchResult.TargetID != "" && mcpSwitchResult.Verified == goSwitch.Verified && mcpSwitchResult.Outcome == goSwitch.Outcome && mcpSwitchResult.Evidence != "" &&
			mcpCloseResult.Action == goClose.Action && mcpCloseResult.Target == goClose.Target && mcpCloseResult.TargetID == mcpOpenResult.TargetID && mcpCloseResult.Verified == goClose.Verified && mcpCloseResult.Outcome == goClose.Outcome && mcpCloseResult.Evidence != "" && mcpTabAbsent && mcpDefaultActive
		success := goTabOK && mcpTabOK
		detail := fmt.Sprintf("Go open/switch/close=%t; MCP open/switch/close=%t; Go closed target=%s; MCP closed target=%s", goTabOK, mcpTabOK, goClose.TargetID, mcpCloseResult.TargetID)
		if mcpFailure != "" {
			detail += "; MCP error: " + mcpFailure
		}
		add(scenarioResult{ID: tabParity.ID, Success: success, Verified: success, Outcome: "tab_lifecycle_parity", Target: mcpCloseResult.TargetID, ActionCalls: 8, OutputBytes: goOutputBytes + mcpOutputBytes, FalseSuccess: goTabOK != mcpTabOK, Evidence: detail})
	}

	background, _ := fixture("background-tab-action")
	if _, err := session.OpenTab("background-task", server.URL+background.StartingPath); err != nil {
		add(scenarioResult{ID: background.ID, ActionCalls: 1, Error: fmt.Sprintf("open background task: %v", err)})
	} else if _, err := session.OpenTab("foreground-guard", server.URL+"/foreground-tab"); err != nil {
		add(scenarioResult{ID: background.ID, ActionCalls: 2, Error: fmt.Sprintf("open foreground guard: %v", err)})
	} else {
		before, beforeErr := session.ListTabs()
		wasHidden := false
		backgroundTargetBefore := ""
		for _, tab := range before {
			if tab.Name == "background-task" {
				wasHidden = tab.VisibilityState == "hidden"
				backgroundTargetBefore = tab.TargetID
			}
		}
		if beforeErr != nil {
			add(scenarioResult{ID: background.ID, ActionCalls: 3, Error: fmt.Sprintf("inspect background tab: %v", beforeErr)})
		} else if switched, err := session.SwitchTab("background-task"); err != nil {
			add(scenarioResult{ID: background.ID, ActionCalls: 4, Error: fmt.Sprintf("activate background task: %v", err)})
		} else {
			clicked, clickErr := session.ClickWithPostcondition(background.ActionSelector, agent.Postcondition{Text: background.ExpectedText}, false)
			if clickErr != nil {
				add(scenarioResult{ID: background.ID, ActionCalls: 5, Error: clickErr.Error()})
			} else {
				encoded, _ := json.Marshal(clicked)
				tabs, tabsErr := session.ListTabs()
				activeTask := false
				visibleTask := false
				foregroundPresent := false
				backgroundTargetAfter := ""
				backgroundURLAfter := ""
				for _, tab := range tabs {
					if tab.Name == "background-task" {
						activeTask = tab.Active
						visibleTask = tab.VisibilityState == "visible"
						backgroundTargetAfter = tab.TargetID
						backgroundURLAfter = tab.URL
					}
					foregroundPresent = foregroundPresent || (tab.Name == "foreground-guard" && tab.Title == "Foreground tab guard")
				}
				wantURL := server.URL + background.StartingPath
				switchTargetMatches := switched.Target == "background-task" && switched.URL == wantURL
				identityPreserved := backgroundTargetBefore != "" && backgroundTargetAfter == backgroundTargetBefore
				actionTargetMatches := clicked.TargetID != "" && clicked.TargetID == backgroundTargetAfter
				urlMatches := backgroundURLAfter == wantURL && clicked.URL == wantURL
				passed := wasHidden && switchTargetMatches && actionTargetMatches && clicked.Verified && clicked.Outcome == "postcondition_verified" && tabsErr == nil && activeTask && visibleTask && foregroundPresent && identityPreserved && urlMatches
				result := scenarioResult{ID: background.ID, Success: passed, Verified: clicked.Verified, Outcome: clicked.Outcome, Target: clicked.TargetID, ActionCalls: 5, OutputBytes: len(encoded), Evidence: fmt.Sprintf("visibility hidden -> visible; action target %s matched active target %s; URL %s; foreground tab preserved\n%s", clicked.TargetID, backgroundTargetAfter, backgroundURLAfter, clicked.Evidence)}
				if tabsErr != nil {
					result.Error = tabsErr.Error()
				} else if !wasHidden || !switchTargetMatches || !actionTargetMatches || !activeTask || !visibleTask || !foregroundPresent || !identityPreserved || !urlMatches {
					result.Error = fmt.Sprintf("target transition invalid: hidden_before=%t switch_target_matches=%t action_target_matches=%t active_after=%t visible_after=%t foreground_preserved=%t identity_preserved=%t url_matches=%t before_target=%q action_target=%q after_target=%q url=%q", wasHidden, switchTargetMatches, actionTargetMatches, activeTask, visibleTask, foregroundPresent, identityPreserved, urlMatches, backgroundTargetBefore, clicked.TargetID, backgroundTargetAfter, backgroundURLAfter)
				}
				add(result)
			}
		}
	}

	staleTarget, _ := fixture("stale-target-after-tab-switch")
	first, navErr := mcpCall(mcpCtx, mcpClient, "navigate", map[string]any{"url": server.URL + "/background-tab"})
	var firstPage agent.PageResult
	if navErr == nil {
		navErr = json.Unmarshal(mcpResultJSON(first), &firstPage)
		if navErr == nil && firstPage.TargetID == "" {
			navErr = errors.New("initial MCP navigation omitted target_id")
		}
	}
	if navErr != nil {
		add(scenarioResult{ID: staleTarget.ID, ActionCalls: 1, Error: fmt.Sprintf("navigate initial target: %v", navErr)})
	} else {
		guardRaw, openErr := mcpCall(mcpCtx, mcpClient, "open_tab", map[string]any{"name": "target-guard", "url": server.URL + "/foreground-tab"})
		var guardPage agent.PageResult
		if openErr == nil {
			openErr = json.Unmarshal(mcpResultJSON(guardRaw), &guardPage)
			if openErr == nil && guardPage.TargetID == "" {
				openErr = errors.New("MCP open_tab omitted guard target_id")
			}
		}
		if openErr != nil {
			add(scenarioResult{ID: staleTarget.ID, ActionCalls: 2, Error: fmt.Sprintf("open guard tab: %v", openErr)})
		} else {
			_, clickErr := mcpCall(mcpCtx, mcpClient, "click", map[string]any{"selector": staleTarget.ActionSelector, "target_id": firstPage.TargetID, "expected_text": staleTarget.ExpectedText})
			code, cause, message := mcpErrorEnvelope(clickErr)
			rejected := code == "SCOUT_OPERATION_ERROR" && cause == "stale_target" && strings.Contains(strings.ToLower(message), "target identity mismatch")
			activeRaw, tabsErr := mcpCall(mcpCtx, mcpClient, "status", map[string]any{})
			var activeStatus agent.SessionStatus
			if tabsErr == nil {
				tabsErr = json.Unmarshal(mcpResultJSON(activeRaw), &activeStatus)
			}
			guardActive := activeStatus.ActiveTargetID == guardPage.TargetID
			originalActive := activeStatus.ActiveTargetID == firstPage.TargetID
			statusRaw, statusErr := mcpCall(mcpCtx, mcpClient, "observe", map[string]any{"target_id": guardPage.TargetID})
			unchanged := statusErr == nil && strings.Contains(string(mcpResultJSON(statusRaw)), "Foreground untouched")
			passed := rejected && tabsErr == nil && guardActive && !originalActive && unchanged
			result := scenarioResult{ID: staleTarget.ID, Success: passed, Verified: passed, Outcome: "stale_target_rejected", Target: firstPage.TargetID, ActionCalls: 5, OutputBytes: len(fmt.Sprint(clickErr)) + len(mcpResultJSON(activeRaw)) + len(mcpResultJSON(statusRaw)), Evidence: fmt.Sprintf("old_target=%s; guard_target=%s; mismatch_rejected=%t; guard_active=%t; original_active=%t; guard_state_unchanged=%t", firstPage.TargetID, guardPage.TargetID, rejected, guardActive, originalActive, unchanged)}
			if !passed {
				result.Error = fmt.Sprintf("stale identity guard failed: click_error=%v; tabs_error=%v; guard_active=%t; original_active=%t; status_error=%v", clickErr, tabsErr, guardActive, originalActive, statusErr)
				result.FalseSuccess = clickErr == nil
			}
			add(result)
		}
	}

	reopened, _ := fixture("stale-target-after-close-reopen")
	oldRaw, oldErr := mcpCall(mcpCtx, mcpClient, "open_tab", map[string]any{"name": "close-reopen-original", "url": server.URL + "/background-tab"})
	var oldPage agent.PageResult
	if oldErr == nil {
		oldErr = json.Unmarshal(mcpResultJSON(oldRaw), &oldPage)
		if oldErr == nil && oldPage.TargetID == "" {
			oldErr = errors.New("initial MCP navigation omitted target_id")
		}
	}
	if oldErr != nil {
		add(scenarioResult{ID: reopened.ID, ActionCalls: 1, Error: fmt.Sprintf("open original page: %v", oldErr)})
	} else {
		guardRaw, guardErr := mcpCall(mcpCtx, mcpClient, "open_tab", map[string]any{"name": "close-reopen-guard", "url": server.URL + "/foreground-tab"})
		var guardPage agent.PageResult
		if guardErr == nil {
			guardErr = json.Unmarshal(mcpResultJSON(guardRaw), &guardPage)
			if guardErr == nil && guardPage.TargetID == "" {
				guardErr = errors.New("MCP open_tab omitted guard target_id")
			}
		}
		if guardErr == nil {
			var closedRaw *client.ToolResult
			closedRaw, guardErr = mcpCall(mcpCtx, mcpClient, "close_tab", map[string]any{"name": "close-reopen-original", "target_id": guardPage.TargetID})
			if guardErr == nil {
				var closedPage agent.PageResult
				guardErr = json.Unmarshal(mcpResultJSON(closedRaw), &closedPage)
				if guardErr == nil && (closedPage.TargetID != oldPage.TargetID || !closedPage.Verified || closedPage.Outcome != "postcondition_verified") {
					guardErr = fmt.Errorf("close_tab did not verify original target closure: %+v", closedPage)
				}
			}
		}
		var replacementPage agent.PageResult
		var replacementRaw *client.ToolResult
		if guardErr == nil {
			replacementRaw, guardErr = mcpCall(mcpCtx, mcpClient, "open_tab", map[string]any{"name": "same-url-replacement", "url": server.URL + "/background-tab", "target_id": guardPage.TargetID})
			if guardErr == nil {
				guardErr = json.Unmarshal(mcpResultJSON(replacementRaw), &replacementPage)
				if guardErr == nil && replacementPage.TargetID == "" {
					guardErr = errors.New("MCP replacement open_tab omitted target_id")
				}
			}
		}
		if guardErr != nil {
			add(scenarioResult{ID: reopened.ID, ActionCalls: 4, Error: fmt.Sprintf("replace closed page: %v", guardErr)})
		} else {
			_, clickErr := mcpCall(mcpCtx, mcpClient, "click", map[string]any{"selector": reopened.ActionSelector, "target_id": oldPage.TargetID, "expected_text": reopened.ExpectedText})
			code, cause, message := mcpErrorEnvelope(clickErr)
			rejected := code == "SCOUT_OPERATION_ERROR" && cause == "stale_target" && strings.Contains(strings.ToLower(message), "target identity mismatch")
			activeRaw, activeErr := mcpCall(mcpCtx, mcpClient, "status", map[string]any{})
			var activeStatus agent.SessionStatus
			if activeErr == nil {
				activeErr = json.Unmarshal(mcpResultJSON(activeRaw), &activeStatus)
			}
			activeReplacement := activeStatus.ActiveTargetID == replacementPage.TargetID
			pageRaw, pageErr := mcpCall(mcpCtx, mcpClient, "observe", map[string]any{"target_id": replacementPage.TargetID})
			pageText := string(mcpResultJSON(pageRaw))
			unchanged := pageErr == nil && strings.Contains(pageText, "Waiting") && !strings.Contains(pageText, reopened.ExpectedText)
			sameURL := oldPage.URL == replacementPage.URL && oldPage.URL == server.URL+"/background-tab"
			newIdentity := replacementPage.TargetID != "" && replacementPage.TargetID != oldPage.TargetID
			passed := rejected && activeErr == nil && activeReplacement && pageErr == nil && unchanged && sameURL && newIdentity
			result := scenarioResult{ID: reopened.ID, Success: passed, Verified: passed, Outcome: "closed_target_replacement_rejected", Target: oldPage.TargetID, ActionCalls: 7, OutputBytes: len(fmt.Sprint(clickErr)) + len(mcpResultJSON(activeRaw)) + len(mcpResultJSON(pageRaw)), Evidence: fmt.Sprintf("old_target=%s; replacement_target=%s; same_url=%t; new_identity=%t; stale_target_rejected=%t; replacement_active=%t; replacement_unchanged=%t", oldPage.TargetID, replacementPage.TargetID, sameURL, newIdentity, rejected, activeReplacement, unchanged)}
			if !passed {
				result.Error = fmt.Sprintf("closed target replacement guard failed: click_error=%v; active_error=%v; page_error=%v; same_url=%t; new_identity=%t; active_replacement=%t; unchanged=%t", clickErr, activeErr, pageErr, sameURL, newIdentity, activeReplacement, unchanged)
				result.FalseSuccess = clickErr == nil
			}
			add(result)
		}
	}

	stable, _ := fixture("stable-handle-after-reorder")
	if _, err := session.Navigate(server.URL + stable.StartingPath); err != nil {
		return fmt.Errorf("navigate stable-handle fixture: %w", err)
	}
	annotated, annotateErr := session.AnnotatedScreenshot()
	var stableHandle string
	if annotateErr == nil {
		for _, element := range annotated.Elements {
			if element.Selector == stable.Selector {
				stableHandle = element.NodeHandle
				break
			}
		}
	}
	if annotateErr != nil || stableHandle == "" {
		cause := annotateErr
		if cause == nil {
			cause = fmt.Errorf("target handle not found in annotation")
		}
		add(scenarioResult{ID: stable.ID, ActionCalls: 1, Error: cause.Error()})
	} else if _, err := session.Click("#mutate"); err != nil {
		add(scenarioResult{ID: stable.ID, ActionCalls: 2, Error: fmt.Sprintf("mutate target order: %v", err)})
	} else {
		clicked, clickErr := session.ClickByHandleWithPostcondition(stableHandle, agent.Postcondition{Text: stable.ExpectedText})
		if clickErr != nil {
			add(scenarioResult{ID: stable.ID, ActionCalls: 3, Error: clickErr.Error()})
		} else {
			encoded, _ := json.Marshal(clicked)
			add(scenarioResult{ID: stable.ID, Success: clicked.Verified && clicked.Outcome == "postcondition_verified", Verified: clicked.Verified, Outcome: clicked.Outcome, Target: stableHandle, ActionCalls: 3, OutputBytes: len(encoded), Evidence: clicked.Evidence})
		}
	}

	stale, _ := fixture("stale-handle-after-removal")
	if _, err := session.Navigate(server.URL + stale.StartingPath); err != nil {
		return fmt.Errorf("navigate stale-handle fixture: %w", err)
	}
	annotated, annotateErr = session.AnnotatedScreenshot()
	stableHandle = ""
	if annotateErr == nil {
		for _, element := range annotated.Elements {
			if element.Selector == stale.Selector {
				stableHandle = element.NodeHandle
				break
			}
		}
	}
	if annotateErr != nil || stableHandle == "" {
		cause := annotateErr
		if cause == nil {
			cause = fmt.Errorf("target handle not found in annotation")
		}
		add(scenarioResult{ID: stale.ID, ActionCalls: 1, Error: cause.Error()})
	} else if _, err := session.Click("#remove"); err != nil {
		add(scenarioResult{ID: stale.ID, ActionCalls: 2, Error: fmt.Sprintf("remove target: %v", err)})
	} else {
		_, clickErr := session.ClickByHandle(stableHandle)
		var operationErr *agent.OperationError
		valid := errors.As(clickErr, &operationErr) && operationErr.Cause == stale.ExpectedFailure
		result := scenarioResult{ID: stale.ID, Success: valid, Verified: valid, Outcome: stale.ExpectedFailure, Target: stableHandle, ActionCalls: 3}
		if clickErr != nil {
			result.Evidence = clickErr.Error()
		} else {
			result.FalseSuccess = true
			result.Error = "detached target handle unexpectedly dispatched a click"
		}
		add(result)
	}

	out.Total = len(out.Scenarios)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		return fmt.Errorf("write benchmark report: %w", err)
	}
	if out.Completed != out.Total {
		return fmt.Errorf("benchmark completed %d/%d scenarios", out.Completed, out.Total)
	}
	return nil
}

func runVerifiedActionFocus(fixtures fixtureSet) error {
	var action fixtureScenario
	for _, item := range fixtures.Scenarios {
		if item.ID == "verified-action" {
			action = item
			break
		}
	}
	if action.ID == "" {
		return fmt.Errorf("verified-action fixture is missing")
	}
	baseURL := benchmarkBaseURL()
	repetitions := 10
	if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
		}
		repetitions = parsed
	}
	durations := make([]float64, 0, repetitions)
	passed, outputBytes, targetID := 0, 0, ""
	browserVersion, predicate := "", "#status text exactly equals 'Task complete'"
	for index := 0; index < repetitions; index++ {
		session, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
		if err != nil {
			return err
		}
		if _, err := session.Navigate(baseURL + "/action"); err != nil {
			_ = session.Close()
			return err
		}
		if browserVersion == "" {
			ua, err := session.Page().Evaluate("navigator.userAgent")
			if err != nil {
				_ = session.Close()
				return err
			}
			uaText, _ := ua.(string)
			versionMatch := regexp.MustCompile(`(?:Headless)?Chrome/(\d+(?:\.\d+)+)`).FindStringSubmatch(uaText)
			if len(versionMatch) == 2 {
				browserVersion = "HeadlessChrome " + versionMatch[1]
			} else {
				browserVersion = "unknown"
			}
		}
		started := time.Now()
		result, err := session.ClickWithPostcondition(action.Selector, agent.Postcondition{Text: action.ExpectedText}, false)
		durations = append(durations, float64(time.Since(started).Microseconds())/1000)
		if err != nil {
			_ = session.Close()
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			_ = session.Close()
			return err
		}
		outputBytes += len(encoded)
		if result.Verified && result.Outcome == "postcondition_verified" {
			passed++
		}
		targetID = result.Target
		if result.Receipt == nil || result.Receipt.Verification != agent.VerificationVerified {
			_ = session.Close()
			return fmt.Errorf("verified-action repetition %d omitted verified action receipt", index+1)
		}
		out := map[string]any{"repetition": index + 1, "outcome": result.Outcome, "verification_state": result.Receipt.Verification, "verified": result.Verified, "target": result.Target, "evidence": result.Evidence}
		if index == 0 {
			receiptJSON, _ := json.MarshalIndent(out, "", "  ")
			_ = os.WriteFile(filepath.Join("examples", "benchmark", "results", "scout-go-api-verified-action-receipt.json"), append(receiptJSON, '\n'), 0o600)
		}
		if err := session.Close(); err != nil {
			return err
		}
	}
	sort.Float64s(durations)
	median := durations[len(durations)/2]
	if len(durations)%2 == 0 {
		median = (durations[len(durations)/2-1] + durations[len(durations)/2]) / 2
	}
	p95 := durations[int(math.Ceil(0.95*float64(len(durations))))-1]
	samples := make([]float64, len(durations))
	copy(samples, durations)
	out := map[string]any{
		"benchmark_version": benchmarkVersion, "comparison_protocol_version": "scout-browser-comparison-v1", "fixture_version": benchmarkVersion,
		"tool_name": "scout-go-api", "tool_version": "workspace", "browser_version": browserVersion,
		"scenario_id": action.ID, "fixture_url": baseURL + "/action", "verification_predicate": predicate,
		"success": passed == repetitions, "false_success": false, "repetitions": repetitions, "successful_repetitions": passed,
		"workflow_duration_ms":        map[string]any{"median": median, "p95": p95, "samples": samples, "includes": "single Go API click with native postcondition; excludes browser launch, navigation, and cleanup"},
		"duration_scope":              "single Go API click with native postcondition; excludes browser launch, navigation, and cleanup",
		"action_calls_per_repetition": 1, "tool_calls_per_repetition": 1, "output_bytes": outputBytes,
		"output_bytes_definition": "UTF-8 JSON bytes of each serialized Go click result summed across repetitions; excludes navigation, browser metadata, and cleanup",
		"target_id":               targetID, "native_postcondition": true,
		"verification_state":     "verified",
		"receipt_schema_version": 1,
		"receipt_report_file":    "scout-go-api-verified-action-receipt.json",
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func runBackgroundTabFocus(fixtures fixtureSet) error {
	var action fixtureScenario
	for _, item := range fixtures.Scenarios {
		if item.ID == "background-tab-action" {
			action = item
			break
		}
	}
	if action.ID == "" {
		return fmt.Errorf("background-tab-action fixture is missing")
	}
	baseURL := benchmarkBaseURL()
	repetitions := 10
	if raw := os.Getenv("SCOUT_BENCHMARK_REPEATS"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			return fmt.Errorf("SCOUT_BENCHMARK_REPEATS must be between 1 and 100")
		}
		repetitions = parsed
	}
	rows := make([]map[string]any, 0, repetitions)
	durations := make([]float64, 0, repetitions)
	passed, outputBytes := 0, 0
	browserVersion := "unknown"
	for index := 0; index < repetitions; index++ {
		taskURL := fmt.Sprintf("%s%s?run=%d", baseURL, action.StartingPath, index+1)
		session, err := agent.NewSession(agent.SessionConfig{Headless: true, AllowPrivateIPs: true})
		if err != nil {
			return fmt.Errorf("start Scout browser for repetition %d: %w", index+1, err)
		}
		if _, err := session.OpenTab("background-task", taskURL); err != nil {
			_ = session.Close()
			return fmt.Errorf("open task tab for repetition %d: %w", index+1, err)
		}
		if _, err := session.OpenTab("foreground-guard", baseURL+"/foreground-tab"); err != nil {
			_ = session.Close()
			return fmt.Errorf("open foreground guard for repetition %d: %w", index+1, err)
		}
		if browserVersion == "unknown" {
			ua, err := session.Page().Evaluate("navigator.userAgent")
			if err != nil {
				_ = session.Close()
				return fmt.Errorf("read browser version: %w", err)
			}
			if uaText, ok := ua.(string); ok {
				if match := regexp.MustCompile(`(?:Headless)?Chrome/(\d+(?:\.\d+)+)`).FindStringSubmatch(uaText); len(match) == 2 {
					browserVersion = "HeadlessChrome " + match[1]
				}
			}
		}
		before, err := session.ListTabs()
		if err != nil {
			_ = session.Close()
			return fmt.Errorf("list tabs before switching: %w", err)
		}
		wasHidden, taskTargetBefore := false, ""
		for _, tab := range before {
			if tab.Name == "background-task" {
				wasHidden, taskTargetBefore = tab.VisibilityState == "hidden", tab.TargetID
			}
		}
		started := time.Now()
		switched, switchErr := session.SwitchTab("background-task")
		if switchErr != nil {
			_ = session.Close()
			return fmt.Errorf("select task target for repetition %d: %w", index+1, switchErr)
		}
		clicked, clickErr := session.ClickWithPostcondition(action.ActionSelector, agent.Postcondition{Text: action.ExpectedText}, false)
		if clickErr != nil {
			_ = session.Close()
			return fmt.Errorf("click task target for repetition %d: %w", index+1, clickErr)
		}
		after, listErr := session.ListTabs()
		duration := float64(time.Since(started).Microseconds()) / 1000
		durations = append(durations, duration)
		if listErr != nil {
			_ = session.Close()
			return fmt.Errorf("list tabs after action: %w", listErr)
		}
		activeTask, visibleTask, foregroundPresent := false, false, false
		taskTargetAfter, taskURLAfter := "", ""
		for _, tab := range after {
			if tab.Name == "background-task" {
				activeTask, visibleTask = tab.Active, tab.VisibilityState == "visible"
				taskTargetAfter, taskURLAfter = tab.TargetID, tab.URL
			}
			if tab.Name == "foreground-guard" && tab.Title == "Foreground tab guard" {
				foregroundPresent = true
			}
		}
		encoded, _ := json.Marshal(clicked)
		outputBytes += len(encoded)
		passedThisRun := wasHidden && taskTargetBefore != "" && switched.TargetID == taskTargetBefore && clicked.TargetID == taskTargetBefore && taskTargetAfter == taskTargetBefore && activeTask && visibleTask && foregroundPresent && taskURLAfter == taskURL && clicked.URL == taskURLAfter && clicked.Verified && clicked.Outcome == string(agent.OutcomePostconditionVerified)
		if passedThisRun {
			passed++
		}
		rows = append(rows, map[string]any{
			"repetition": index + 1, "success": passedThisRun, "verified": clicked.Verified, "outcome": clicked.Outcome,
			"target_id_before": taskTargetBefore, "switch_target_id": switched.TargetID, "action_target_id": clicked.TargetID, "target_id_after": taskTargetAfter,
			"url_after": taskURLAfter, "hidden_before": wasHidden, "active_after": activeTask, "visible_after": visibleTask,
			"foreground_tab_preserved": foregroundPresent, "workflow_duration_ms": duration,
		})
		if err := session.Close(); err != nil {
			return fmt.Errorf("close Scout browser for repetition %d: %w", index+1, err)
		}
	}
	sort.Float64s(durations)
	median := durations[len(durations)/2]
	if len(durations)%2 == 0 {
		median = (durations[len(durations)/2-1] + durations[len(durations)/2]) / 2
	}
	p95 := durations[int(math.Ceil(0.95*float64(len(durations))))-1]
	out := map[string]any{
		"benchmark_version": benchmarkVersion, "comparison_protocol_version": "scout-browser-comparison-v1", "fixture_version": benchmarkVersion,
		"tool_name": "scout-go-api", "tool_version": "workspace", "browser_version": browserVersion,
		"scenario_id": action.ID, "fixture_url": baseURL + action.StartingPath + "?run=<repetition>",
		"verification_predicate": "selected task target matches before/switch/action/after IDs; unique URL nonce is unchanged; target is active and visible; foreground guard remains; declared text postcondition verifies",
		"success":                passed == repetitions, "false_success": false, "repetitions": repetitions, "successful_repetitions": passed,
		"workflow_duration_ms":        map[string]any{"median": median, "p95": p95, "samples": durations, "includes": "tab selection, verified click, and post-action tab/target inspection; excludes browser launch, tab setup, and cleanup"},
		"action_calls_per_repetition": 3, "tool_calls_per_repetition": 3, "output_bytes": outputBytes,
		"output_bytes_definition": "UTF-8 JSON bytes of each serialized Go click result summed across repetitions; excludes tab setup, browser metadata, and cleanup",
		"native_postcondition":    true, "target_id": rows[len(rows)-1]["action_target_id"], "verification_state": "verified",
		"runs": rows,
		"scenarios": []scenarioResult{{
			ID: action.ID, Success: passed == repetitions, Verified: passed == repetitions,
			Outcome: string(agent.OutcomePostconditionVerified), Target: fmt.Sprint(rows[len(rows)-1]["action_target_id"]),
			ActionCalls: 3 * repetitions, OutputBytes: outputBytes, FalseSuccess: false,
			Evidence: fmt.Sprintf("target_identity_and_postcondition_passed=%d/%d; foreground_tab_preserved=%d/%d", passed, repetitions, passed, repetitions),
		}},
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func runCLIRecoveryFile(fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote browser: " + err.Error()}
	}
	defer browser.Close()
	tmpDir, err := os.MkdirTemp("", "scout-cli-recovery-")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create recovery temp directory: " + err.Error()}
	}
	defer os.RemoveAll(tmpDir)
	binary := tmpDir + "/scout"
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/scout")
	if output, err := build.CombinedOutput(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: fmt.Sprintf("build CLI: %v: %s", err, strings.TrimSpace(string(output)))}
	}
	recoveryPath := tmpDir + "/recovery.json"
	env := append(os.Environ(), "SCOUT_REMOTE_CDP="+browser.WSEndpoint())
	click := exec.CommandContext(ctx, binary, "--json-errors", "--recovery-file="+recoveryPath, "click", server.URL+"/late-action", "#late-action", "--expected-text=Late completion", "--headless", "--timeout=300ms")
	click.Env = env
	started := time.Now()
	clickOutput, clickErr := click.CombinedOutput()
	if clickErr == nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, OutputBytes: len(clickOutput), Error: "expected click command to report an unknown outcome"}
	}
	var clickEnvelope struct {
		Code                string `json:"code"`
		Outcome             string `json:"outcome"`
		RunID               string `json:"run_id"`
		Step                int    `json:"step"`
		DeadlineAt          int64  `json:"deadline_at_ms"`
		ConfiguredTimeoutMS int64  `json:"configured_timeout_ms"`
		SessionDead         *bool  `json:"session_dead"`
		ConsecutiveTimeouts *int   `json:"consecutive_timeouts"`
		InFlightCommands    *int   `json:"inflight_commands"`
		TargetID            string `json:"target_id"`
	}
	if err := json.Unmarshal(clickOutput, &clickEnvelope); err != nil || clickEnvelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || clickEnvelope.Outcome != string(agent.OutcomeUnknown) || clickEnvelope.TargetID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, OutputBytes: len(clickOutput), Error: fmt.Sprintf("click did not return structured unknown outcome: decode=%v output=%s", err, clickOutput)}
	}
	recoveryBytes, err := os.ReadFile(recoveryPath)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, OutputBytes: len(clickOutput), Error: "read recovery artifact: " + err.Error()}
	}
	recovery, err := agent.DecodeActionRecoveryRecord(recoveryBytes)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, OutputBytes: len(clickOutput) + len(recoveryBytes), Error: "decode recovery artifact: " + err.Error()}
	}
	info, err := os.Stat(recoveryPath)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "stat recovery artifact: " + err.Error()}
	}
	private := info.Mode().Perm() == 0o600
	unknown := recovery.ActionOutcome
	healthPresent := clickEnvelope.SessionDead != nil && clickEnvelope.ConsecutiveTimeouts != nil && clickEnvelope.InFlightCommands != nil
	healthRetained := healthPresent && unknown.SessionDead == *clickEnvelope.SessionDead && unknown.ConsecutiveTimeouts == *clickEnvelope.ConsecutiveTimeouts && unknown.InFlightCommands == *clickEnvelope.InFlightCommands
	retained := recovery.SchemaVersion == agent.ActionRecoverySchemaVersion && unknown.RunID != "" && unknown.Step > 0 && unknown.DeadlineAt > 0 && unknown.ConfiguredTimeoutMS == 300 && unknown.TargetID == clickEnvelope.TargetID && unknown.ExpectedPostcondition != nil && unknown.ExpectedPostcondition.Text == "Late completion" && healthRetained
	time.Sleep(1400 * time.Millisecond)
	reconcile := exec.CommandContext(ctx, binary, "reconcile", "--recovery-file="+recoveryPath, "--timeout=10s")
	reconcile.Env = env
	reconcileOutput, err := reconcile.CombinedOutput()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, OutputBytes: len(clickOutput) + len(recoveryBytes) + len(reconcileOutput), Error: fmt.Sprintf("reconcile process: %v: %s", err, strings.TrimSpace(string(reconcileOutput)))}
	}
	var result agent.PageResult
	if err := json.Unmarshal(reconcileOutput, &result); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, OutputBytes: len(clickOutput) + len(recoveryBytes) + len(reconcileOutput), Error: "decode reconciled result: " + err.Error()}
	}
	if result.TargetID != clickEnvelope.TargetID || result.Outcome != string(agent.OutcomePostconditionVerified) || !result.Verified {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, OutputBytes: len(clickOutput) + len(recoveryBytes) + len(reconcileOutput), Error: "late mutation did not reconcile as verified on its original target"}
	}
	external, err := cdpclient.DialContext(ctx, browser.WSEndpoint())
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, OutputBytes: len(clickOutput) + len(recoveryBytes) + len(reconcileOutput), Error: "connect replacement target control: " + err.Error()}
	}
	defer external.Close()
	if err := external.CloseTargetCtx(ctx, clickEnvelope.TargetID); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 5, OutputBytes: len(clickOutput) + len(recoveryBytes) + len(reconcileOutput), Error: "close original target before stale retry: " + err.Error()}
	}
	staleRetry := exec.CommandContext(ctx, binary, "reconcile", "--recovery-file="+recoveryPath, "--timeout=2s")
	staleRetry.Env = env
	staleOutput, staleErr := staleRetry.CombinedOutput()
	staleRefused := staleErr != nil && strings.Contains(string(staleOutput), clickEnvelope.TargetID) && strings.Contains(string(staleOutput), "does not exist")
	recoveryLinked := unknown.OperationOrdinal > 0 && result.Receipt != nil && result.Receipt.RecoveryOfOperationOrdinal == unknown.OperationOrdinal
	passed := private && retained && recoveryLinked && staleRefused
	evidence := fmt.Sprintf("unknown_cli_outcome=true; schema_version=%d; run_id=%t; unknown_outcome_sequence=%d; deadline_recorded=%t; health_fields_present=%t; health_retained=%t; target_retained=%t; postcondition_retained=%t; recovery_mode=%04o; same_target_reconcile=%s; recovery_receipt_linked=%t; original_target_closed=%t; stale_record_refused=%t", recovery.SchemaVersion, unknown.RunID != "", unknown.Step, unknown.DeadlineAt > 0, healthPresent, healthRetained, unknown.TargetID == clickEnvelope.TargetID, retained, info.Mode().Perm(), result.Outcome, recoveryLinked, staleRefused, staleRefused)
	out := scenarioResult{ID: fixture.ID, Success: passed, Verified: result.Verified, Outcome: result.Outcome, Target: result.TargetID, ActionCalls: 7, OutputBytes: len(clickOutput) + len(recoveryBytes) + len(reconcileOutput) + len(staleOutput), DurationMicros: time.Since(started).Microseconds(), Evidence: evidence}
	if !passed {
		out.Error = "recovery artifact did not preserve a private, same-target verifiable postcondition"
	}
	return out
}

func runCLIFrameClose(fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	baseURL := os.Getenv("SCOUT_BENCHMARK_BASE_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:41731"
	}
	serverCheck, err := http.Get(baseURL + "/healthz")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "check fixture server: " + err.Error()}
	}
	_ = serverCheck.Body.Close()
	if serverCheck.StatusCode != http.StatusOK {
		return scenarioResult{ID: fixture.ID, Error: "fixture server health check returned " + serverCheck.Status}
	}
	tmpDir, err := os.MkdirTemp("", "scout-cli-frame-close-")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create CLI work directory: " + err.Error()}
	}
	defer os.RemoveAll(tmpDir)
	binary := filepath.Join(tmpDir, "scout")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/scout")
	if output, err := build.CombinedOutput(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: fmt.Sprintf("build CLI: %v: %s", err, strings.TrimSpace(string(output)))}
	}
	cmd := exec.CommandContext(ctx, binary, "session", "--headless", "--allow-private-ips")
	cmd.Env = append(os.Environ(), "SCOUT_REMOTE_CDP=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "open CLI stdin: " + err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "open CLI stdout: " + err.Error()}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start CLI session: " + err.Error()}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	reader := bufio.NewReader(stdout)
	call := 0
	var frameOutputBytes int
	type cliSessionRequest struct {
		ID           json.RawMessage   `json:"id,omitempty"`
		Command      string            `json:"command"`
		URL          string            `json:"url,omitempty"`
		Selector     string            `json:"selector,omitempty"`
		Text         string            `json:"text,omitempty"`
		Handle       string            `json:"handle,omitempty"`
		Frame        string            `json:"frame,omitempty"`
		Tab          string            `json:"tab,omitempty"`
		Name         string            `json:"name,omitempty"`
		TargetID     string            `json:"target_id,omitempty"`
		FrameID      string            `json:"frame_id,omitempty"`
		ExpectedText string            `json:"expected_text,omitempty"`
		Wait         bool              `json:"wait,omitempty"`
		Options      map[string]string `json:"options,omitempty"`
		TimeoutMS    int               `json:"timeout_ms,omitempty"`
	}
	type cliSessionResponse struct {
		ID     json.RawMessage `json:"id,omitempty"`
		OK     bool            `json:"ok"`
		Result any             `json:"result,omitempty"`
		Error  string          `json:"error,omitempty"`
	}
	request := func(req cliSessionRequest) (cliSessionResponse, []byte, error) {
		call++
		line, err := json.Marshal(req)
		if err != nil {
			return cliSessionResponse{}, nil, err
		}
		if _, err := stdin.Write(append(line, '\n')); err != nil {
			return cliSessionResponse{}, nil, fmt.Errorf("write request %d: %w", call, err)
		}
		responseLine, err := reader.ReadBytes('\n')
		if err != nil {
			return cliSessionResponse{}, responseLine, fmt.Errorf("read response %d: %w; stderr=%s", call, err, stderr.String())
		}
		frameOutputBytes += len(responseLine)
		var response cliSessionResponse
		if err := json.Unmarshal(responseLine, &response); err != nil {
			return response, responseLine, fmt.Errorf("decode response %d: %w: %s", call, err, responseLine)
		}
		if !response.OK {
			if req.TargetID != "" && strings.Contains(strings.ToLower(response.Error), "target") {
				return response, responseLine, nil // Expected stale-target rejection under test.
			}
			return response, responseLine, fmt.Errorf("request %d %s failed: %s", call, req.Command, response.Error)
		}
		return response, responseLine, nil
	}
	_, _, err = request(cliSessionRequest{ID: json.RawMessage(`1`), Command: "open_tab", Name: "unrelated", URL: baseURL + "/foreground-tab"})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: err.Error()}
	}
	frameResp, _, err := request(cliSessionRequest{ID: json.RawMessage(`2`), Command: "open_tab", Name: "task", URL: baseURL + "/iframe"})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: err.Error()}
	}
	var opened agent.PageResult
	if err := remarshalJSON(frameResp.Result, &opened); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: "decode opened page: " + err.Error()}
	}
	selectedResp, _, err := request(cliSessionRequest{ID: json.RawMessage(`3`), Command: "switch_frame", Frame: "#task-frame", TargetID: opened.TargetID})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: err.Error()}
	}
	var selected agent.PageResult
	if err := remarshalJSON(selectedResp.Result, &selected); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: "decode selected frame: " + err.Error()}
	}
	annotatedResp, _, err := request(cliSessionRequest{ID: json.RawMessage(`4`), Command: "annotate", TargetID: selected.TargetID})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: err.Error()}
	}
	var annotated agent.AnnotatedResult
	if err := remarshalJSON(annotatedResp.Result, &annotated); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: "decode annotation: " + err.Error()}
	}
	if len(annotated.Elements) == 0 {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: fmt.Sprintf("annotation returned no frame elements: %#v", annotatedResp.Result)}
	}
	var foundHandle bool
	var frameHandle string
	for _, element := range annotated.Elements {
		if element.Selector == "#frame-action" || strings.Contains(element.Selector, "frame-action") {
			foundHandle = true
			frameHandle = element.NodeHandle
		}
	}
	if !foundHandle || frameHandle == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: fmt.Sprintf("annotated frame did not return a stable handle for #frame-action: %+v", annotated.Elements)}
	}
	_, _, err = request(cliSessionRequest{ID: json.RawMessage(`5`), Command: "close_tab", Tab: "unrelated", TargetID: selected.TargetID})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: err.Error()}
	}
	wrongTarget, _, wrongErr := request(cliSessionRequest{ID: json.RawMessage(`55`), Command: "observe", TargetID: "stale-target-id"})
	if wrongErr != nil || wrongTarget.OK || !strings.Contains(strings.ToLower(wrongTarget.Error), "target") {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: fmt.Sprintf("stale target was not safely rejected: response=%+v err=%v", wrongTarget, wrongErr)}
	}
	clickedResp, _, err := request(cliSessionRequest{ID: json.RawMessage(`6`), Command: "click_handle", Handle: frameHandle, TargetID: selected.TargetID, FrameID: selected.FrameID, ExpectedText: "Frame task complete"})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: err.Error()}
	}
	var clicked agent.PageResult
	if err := remarshalJSON(clickedResp.Result, &clicked); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: call, Error: "decode handle click: " + err.Error()}
	}
	shutdown, _, shutdownErr := request(cliSessionRequest{ID: json.RawMessage(`7`), Command: "shutdown"})
	_ = stdin.Close()
	waitErr := cmd.Wait()
	passed := opened.TargetID != "" && selected.TargetID == opened.TargetID && selected.FrameID != "" && clicked.TargetID == selected.TargetID && clicked.FrameID == selected.FrameID && clicked.Verified && clicked.Outcome == string(agent.OutcomePostconditionVerified) && wrongErr == nil && !wrongTarget.OK && strings.Contains(strings.ToLower(wrongTarget.Error), "target") && shutdownErr == nil && waitErr == nil && shutdown.OK
	evidence := fmt.Sprintf("target_retained=%t; frame_retained=%t; stale_target_rejected=%t; handle=%q; outcome=%s; verified=%t; session_closed=%t", clicked.TargetID == selected.TargetID, clicked.FrameID == selected.FrameID, !wrongTarget.OK, "frame-save", clicked.Outcome, clicked.Verified, shutdown.OK && waitErr == nil)
	outputBytes := frameOutputBytes
	out := scenarioResult{ID: fixture.ID, Success: passed, Verified: clicked.Verified, Outcome: clicked.Outcome, Target: clicked.TargetID, ActionCalls: call, OutputBytes: outputBytes, DurationMicros: time.Since(started).Microseconds(), Evidence: evidence}
	if !passed {
		out.Error = fmt.Sprintf("identity contract failed: opened_target=%q selected_target=%q selected_frame=%q clicked_target=%q clicked_frame=%q outcome=%q verified=%t stale_rejected=%t shutdown=%+v wait_err=%v stderr=%s", opened.TargetID, selected.TargetID, selected.FrameID, clicked.TargetID, clicked.FrameID, clicked.Outcome, clicked.Verified, !wrongTarget.OK, shutdown, waitErr, stderr.String())
	}
	return out
}

func benchmarkBaseURL() string {
	if baseURL := os.Getenv("SCOUT_BENCHMARK_BASE_URL"); baseURL != "" {
		return baseURL
	}
	return "http://127.0.0.1:41731"
}

func runCLISessionParityRepeated(fixture fixtureScenario, baseURL string, repeats int) scenarioResult {
	started := time.Now()
	var actionCalls, outputBytes int
	for i := 0; i < repeats; i++ {
		result := runCLISessionParityAtBase(fixture, baseURL, nil, nil, nil, nil)
		actionCalls += result.ActionCalls
		outputBytes += result.OutputBytes
		if !result.Success {
			result.ID = fixture.ID
			result.Success = false
			result.Verified = false
			result.ActionCalls = actionCalls
			result.OutputBytes = outputBytes
			result.Evidence = fmt.Sprintf("repetitions_passed=%d/%d; failed_repetition=%d; %s", i, repeats, i+1, result.Evidence)
			return result
		}
	}
	return scenarioResult{ID: fixture.ID, Success: true, Verified: true, Outcome: "response_backed_form_parity", Target: "#task", ActionCalls: actionCalls, OutputBytes: outputBytes, DurationMicros: time.Since(started).Microseconds(), Evidence: fmt.Sprintf("repetitions_passed=%d/%d; per_run_calls=7; committed_type=true; rejected_status=422; rejected_application_accepted=false; rejected_outcome=response_failed; accepted_status=200; accepted_application_accepted=true; accepted_postcondition=true; session_closed=true", repeats, repeats)}
}

func runCLISessionParityAtBase(fixture fixtureScenario, baseURL string, typeBaseline *agent.ElementResult, rejectionBaseline, acceptanceBaseline *agent.SubmitFormResult, typeBaselineErr error) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "check fixture server: " + err.Error()}
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return scenarioResult{ID: fixture.ID, Error: "fixture server health check returned " + resp.Status}
	}
	tmpDir, err := os.MkdirTemp("", "scout-cli-session-parity-")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create CLI work directory: " + err.Error()}
	}
	defer os.RemoveAll(tmpDir)
	binary := filepath.Join(tmpDir, "scout")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/scout")
	if output, err := build.CombinedOutput(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: fmt.Sprintf("build CLI: %v: %s", err, strings.TrimSpace(string(output)))}
	}
	cmd := exec.CommandContext(ctx, binary, "session", "--headless", "--allow-private-ips")
	cmd.Env = append(os.Environ(), "SCOUT_REMOTE_CDP=")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: err.Error()}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: err.Error()}
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	reader := bufio.NewReader(stdout)
	type request struct {
		ID           int    `json:"id"`
		Command      string `json:"command"`
		URL          string `json:"url,omitempty"`
		Selector     string `json:"selector,omitempty"`
		Text         string `json:"text,omitempty"`
		MatchURL     string `json:"match_url,omitempty"`
		ExpectedText string `json:"expected_text,omitempty"`
		Acceptance   bool   `json:"acceptance,omitempty"`
		TimeoutMS    int    `json:"timeout_ms,omitempty"`
	}
	type response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	var outputBytes int
	call := func(req request, allowError bool) (response, error) {
		line, err := json.Marshal(req)
		if err != nil {
			return response{}, err
		}
		if _, err := stdin.Write(append(line, '\n')); err != nil {
			return response{}, err
		}
		line, err = reader.ReadBytes('\n')
		outputBytes += len(line)
		if err != nil {
			return response{}, fmt.Errorf("read session response: %w; stderr=%s", err, stderr.String())
		}
		var got response
		if err := json.Unmarshal(line, &got); err != nil {
			return got, err
		}
		if !got.OK && !allowError {
			return got, fmt.Errorf("%s: %s", req.Command, got.Error)
		}
		return got, nil
	}
	call(request{ID: 1, Command: "navigate", URL: baseURL + "/type"}, false)
	typed, err := call(request{ID: 2, Command: "type", Selector: "#task", Text: "benchmark task"}, false)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, OutputBytes: outputBytes, Error: err.Error()}
	}
	var typeResult agent.ElementResult
	if err := json.Unmarshal(typed.Result, &typeResult); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "decode type result: " + err.Error()}
	}
	if !typeResult.Verified || typeResult.ValueCommitted != "benchmark task" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, OutputBytes: outputBytes, Error: "stateful CLI did not verify committed input value"}
	}
	if typeBaselineErr == nil && typeBaseline != nil && (typeResult.Selector != typeBaseline.Selector || typeResult.Value != typeBaseline.Value || typeResult.ValueCommitted != typeBaseline.ValueCommitted || typeResult.FrameworkReactive != typeBaseline.FrameworkReactive || typeResult.Verified != typeBaseline.Verified || typeResult.Outcome != typeBaseline.Outcome || typeResult.Evidence != typeBaseline.Evidence) {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, OutputBytes: outputBytes, Error: "stateful CLI type result differs from Go API baseline"}
	}
	checks := []struct {
		path, selector, match, text string
		accepted                    bool
		status                      int
	}{
		{"/reject-form", "#checkout", "/reject", "Order accepted", false, 422},
		{"/form", "#checkout", "/submit", "Order accepted", true, 200},
	}
	for i, check := range checks {
		if _, err := call(request{ID: 3 + i*2, Command: "navigate", URL: baseURL + check.path}, false); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 2 + i*2, OutputBytes: outputBytes, Error: err.Error()}
		}
		got, err := call(request{ID: 4 + i*2, Command: "submit", Selector: check.selector, MatchURL: check.match, ExpectedText: check.text, Acceptance: true, TimeoutMS: 3000}, true)
		if err != nil {
			return scenarioResult{ID: fixture.ID, OutputBytes: outputBytes, Error: err.Error()}
		}
		var result agent.SubmitFormResult
		if err := json.Unmarshal(got.Result, &result); err != nil {
			return scenarioResult{ID: fixture.ID, Error: "decode submit result: " + err.Error()}
		}
		if !got.OK || result.StatusCode != check.status || result.ApplicationAccepted != check.accepted || !result.ResponseObserved || result.Verified != check.accepted {
			return scenarioResult{ID: fixture.ID, ActionCalls: 2 + (i+1)*2, OutputBytes: outputBytes, Error: fmt.Sprintf("form %s mismatch: ok=%t status=%d accepted=%t response=%t verified=%t stderr=%s", check.path, got.OK, result.StatusCode, result.ApplicationAccepted, result.ResponseObserved, result.Verified, stderr.String())}
		}
		baseline := rejectionBaseline
		if check.accepted {
			baseline = acceptanceBaseline
		}
		if baseline != nil && (result.Submitted != baseline.Submitted || result.Verified != baseline.Verified || result.ResponseObserved != baseline.ResponseObserved || result.ApplicationAccepted != baseline.ApplicationAccepted || result.Outcome != baseline.Outcome || result.FormSelector != baseline.FormSelector || result.RequestMethod != baseline.RequestMethod || result.StatusCode != baseline.StatusCode || result.Evidence != baseline.Evidence) {
			return scenarioResult{ID: fixture.ID, ActionCalls: 2 + (i+1)*2, OutputBytes: outputBytes, Error: fmt.Sprintf("stateful CLI form result %s differs from Go API baseline", check.path)}
		}
	}
	if _, err := call(request{ID: 7, Command: "shutdown"}, false); err != nil {
		return scenarioResult{ID: fixture.ID, OutputBytes: outputBytes, Error: err.Error()}
	}
	return scenarioResult{ID: fixture.ID, Success: true, Verified: true, Outcome: "response_backed_form_parity", Target: typeResult.Selector, ActionCalls: 7, OutputBytes: outputBytes, Evidence: "committed_type=true; rejected_status=422; rejected_application_accepted=false; rejected_outcome=response_failed; accepted_status=200; accepted_application_accepted=true; accepted_postcondition=true; session_closed=true"}
}

func remarshalJSON(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

type mcpSessionIDCapture struct {
	base      http.RoundTripper
	sessionID string
}

func (t *mcpSessionIDCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp.Header.Get("Mcp-Session-Id") != "" {
		t.sessionID = resp.Header.Get("Mcp-Session-Id")
	}
	return resp, err
}

func scoutChromePIDs() (map[int]string, error) {
	cmd := exec.Command("ps", "-axo", "pid=,command=")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	pids := make(map[int]string)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(line, "--remote-debugging-port=") || strings.Contains(line, "--type=") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil {
			pids[pid] = line
		}
	}
	return pids, nil
}

// runMCPHTTPSharedRemoteOwnership characterizes the boundary between isolated
// Scout HTTP sessions and a browser process that both sessions attach to.
func runMCPHTTPSharedRemoteOwnership(fixture fixtureScenario) scenarioResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote browser: " + err.Error()}
	}
	defer browser.Close()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve HTTP port: " + err.Error()}
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "mcp", "serve-http", "--listen="+addr)
	cmd.Env = envWith(os.Environ(), "SCOUT_REMOTE_CDP", browser.WSEndpoint())
	cmd.Env = envWith(cmd.Env, "SCOUT_ALLOW_PRIVATE_IPS", "1")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start HTTP MCP server: " + err.Error()}
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	newClient := func() (*client.Client, error) {
		transport, err := client.NewHTTPTransport("http://" + addr)
		if err != nil {
			return nil, err
		}
		return client.New(transport), nil
	}
	first, err := newClient()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create first HTTP client: " + err.Error()}
	}
	second, err := newClient()
	if err != nil {
		_ = first.Close()
		return scenarioResult{ID: fixture.ID, Error: "create second HTTP client: " + err.Error()}
	}
	defer first.Close()
	defer second.Close()
	initialize := func(c *client.Client) error {
		for attempt := 0; attempt < 100; attempt++ {
			if _, err := c.Initialize(ctx); err == nil {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("HTTP MCP server did not become ready")
	}
	if err := initialize(first); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize first client: " + err.Error()}
	}
	if err := initialize(second); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize second client: " + err.Error()}
	}

	calls, outputBytes := 0, 0
	navigate := func(c *client.Client, path string) (agent.PageResult, error) {
		result, err := mcpCall(ctx, c, "navigate", map[string]any{"url": server.URL + path})
		calls++
		if err != nil {
			return agent.PageResult{}, err
		}
		outputBytes += len(mcpResultJSON(result))
		var page agent.PageResult
		if err := json.Unmarshal(mcpResultJSON(result), &page); err != nil {
			return page, err
		}
		if page.TargetID == "" {
			return page, errors.New("navigation omitted target_id")
		}
		return page, nil
	}
	extract := func(c *client.Client) (agent.ElementResult, error) {
		result, err := mcpCall(ctx, c, "extract", map[string]any{"selector": "#state"})
		calls++
		if err != nil {
			return agent.ElementResult{}, err
		}
		outputBytes += len(mcpResultJSON(result))
		var state agent.ElementResult
		if err := json.Unmarshal(mcpResultJSON(result), &state); err != nil {
			return state, err
		}
		return state, nil
	}

	var firstTarget, secondTarget string
	var observedSeed, observedAfter string
	var failure string
	if page, err := navigate(first, "/session-owner/seed"); err != nil {
		failure = "seed through first client: " + err.Error()
	} else {
		firstTarget = page.TargetID
		if page, err := navigate(second, "/session-owner/probe"); err != nil {
			failure = "probe through second client: " + err.Error()
		} else {
			secondTarget = page.TargetID
			state, err := extract(second)
			if err != nil {
				failure = "read second client probe state: " + err.Error()
			} else {
				observedSeed = state.Text
				if state.TargetID != secondTarget || state.Text != `["scout_owner=primary","primary"]` {
					failure = fmt.Sprintf("second client did not observe first client's seed: target=%s expected_target=%s state=%q", state.TargetID, secondTarget, state.Text)
				} else if page, err := navigate(first, "/session-owner/check"); err != nil {
					failure = "check through first client: " + err.Error()
				} else {
					firstTarget = page.TargetID
					state, err = extract(first)
					if err != nil {
						failure = "read first client check state: " + err.Error()
					} else {
						observedAfter = state.Text
						if state.TargetID != firstTarget || state.Text != `["scout_owner=secondary","secondary"]` {
							failure = fmt.Sprintf("first client did not observe second client's write: target=%s expected_target=%s state=%q", state.TargetID, firstTarget, state.Text)
						}
					}
				}
			}
		}
	}
	passed := failure == ""
	outcome := "shared_remote_browser_state"
	if !passed {
		outcome = "shared_remote_browser_state_unconfirmed"
	}
	return scenarioResult{
		ID: fixture.ID, Success: passed, Verified: passed, Outcome: outcome,
		Target: firstTarget + "/" + secondTarget, ActionCalls: calls, OutputBytes: outputBytes,
		DurationMicros: time.Since(started).Microseconds(),
		Evidence:       fmt.Sprintf("two_http_clients=true; same_scout_server=true; remote_cdp=%s; first_write_visible_to_second=%t; second_write_visible_to_first=%t; first_target=%s; second_target=%s; second_observed=%q; first_observed_after=%q", browser.WSEndpoint(), observedSeed == `["scout_owner=primary","primary"]`, observedAfter == `["scout_owner=secondary","secondary"]`, firstTarget, secondTarget, observedSeed, observedAfter),
		Error:          failure,
	}
}

// runMCPHTTPPrincipalIsolation verifies that authenticated principals receive
// their configured remote browser and origin policy, and that deleting one
// transport session leaves the other principal operational.
func runMCPHTTPPrincipalIsolation(fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	scoutBinary, cleanupBinary, err := buildBenchmarkScoutCLI(ctx)
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "build Scout server: " + err.Error()}
	}
	defer cleanupBinary()
	started := time.Now()
	allowedA := httptest.NewServer(benchmarkfixture.Handler())
	defer allowedA.Close()
	browserA, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch principal A browser: " + err.Error()}
	}
	defer browserA.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve HTTP port: " + err.Error()}
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	taskStoreDir, err := os.MkdirTemp("", "scout-principal-reconnect-task-store-*")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create task store directory: " + err.Error()}
	}
	defer os.RemoveAll(taskStoreDir)
	policies, err := json.Marshal(map[string]any{
		"principal-a": map[string]any{"token": "benchmark-token-a", "remote_cdp": browserA.WSEndpoint(), "allowed_origins": []string{allowedA.URL}, "allow_private_ips": true, "isolate_browser_context": true},
		"principal-b": map[string]any{"token": "benchmark-token-b", "remote_cdp": browserA.WSEndpoint(), "allowed_origins": []string{allowedA.URL}, "allow_private_ips": true, "isolate_browser_context": true},
	})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "encode principal policies: " + err.Error()}
	}
	cmd := exec.CommandContext(ctx, scoutBinary, "mcp", "serve-http", "--listen="+addr, "--task-store="+filepath.Join(taskStoreDir, "task-runs.db"))
	cmd.Env = envWith(os.Environ(), "SCOUT_MCP_HTTP_PRINCIPALS_JSON", string(policies))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start HTTP MCP server: " + err.Error()}
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	newClient := func(token string) (*client.Client, client.Transport, *benchmarkSessionCaptureTransport, error) {
		capture := &benchmarkSessionCaptureTransport{base: http.DefaultTransport}
		httpClient := &http.Client{Transport: capture}
		transport, err := client.NewHTTPTransport("http://"+addr, client.WithHTTPClient(httpClient), client.WithHTTPHeader("Authorization", "Bearer "+token))
		if err != nil {
			return nil, nil, nil, err
		}
		return client.New(transport), transport, capture, nil
	}
	first, transportA, captureA, err := newClient("benchmark-token-a")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create principal A client: " + err.Error()}
	}
	second, transportB, _, err := newClient("benchmark-token-b")
	if err != nil {
		_ = first.Close()
		return scenarioResult{ID: fixture.ID, Error: "create principal B client: " + err.Error()}
	}
	firstOpen := true
	defer func() {
		if firstOpen {
			_ = first.Close()
		}
		_ = second.Close()
	}()
	initialize := func(c *client.Client) error {
		for attempt := 0; attempt < 100; attempt++ {
			if _, err := c.Initialize(ctx); err == nil {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("HTTP MCP server did not become ready")
	}
	if err := initialize(first); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize principal A: " + err.Error()}
	}
	if err := initialize(second); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize principal B: " + err.Error()}
	}
	calls := 0
	for _, c := range []*client.Client{first, second} {
		if _, err := mcpCall(ctx, c, "navigate", map[string]any{"url": allowedA.URL + "/"}); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: calls + 1, Error: "initialize default session for explicit selector: " + err.Error()}
		}
		calls++
	}
	selectorName := "default"
	navigate := func(c *client.Client, url string) (agent.PageResult, error) {
		result, err := mcpCall(ctx, c, "navigate", map[string]any{"url": url})
		calls++
		if err != nil {
			return agent.PageResult{}, err
		}
		var page agent.PageResult
		if err := json.Unmarshal(mcpResultJSON(result), &page); err != nil {
			return page, err
		}
		return page, nil
	}
	listEndpoints := func(c *client.Client) (string, error) {
		result, err := mcpCall(ctx, c, "list_sessions", map[string]any{})
		calls++
		if err != nil {
			return "", err
		}
		var sessions []benchmarkSessionSummary
		if err := json.Unmarshal(mcpResultJSON(result), &sessions); err != nil {
			return "", err
		}
		for _, session := range sessions {
			if session.Name == "default" {
				return session.BrowserEndpoint, nil
			}
		}
		return "", errors.New("list_sessions omitted the default session")
	}
	var failure string
	sameNameSelectorScoped := false
	originDenied, sessionReuseDenied, foreignOpaqueIDDenied, ownerSessionRetained, deleteIsolation, storageIsolated, resetIsolation, reconnectByOpaqueID, taskLookupByOpaqueID := false, false, false, false, false, false, false, false, false
	var pageA, pageB agent.PageResult
	var navAErr, navBErr error
	var navGroup sync.WaitGroup
	calls += 2
	navGroup.Add(2)
	go func() {
		defer navGroup.Done()
		response, callErr := mcpRawCallWithMeta(ctx, transportA, 801, "navigate", map[string]any{"url": allowedA.URL + "/session-owner/seed"}, map[string]any{"go.klarlabs.de/scout/session": selectorName})
		if callErr == nil {
			var raw []byte
			raw, callErr = mcpRawResultJSON(response)
			if callErr == nil {
				callErr = json.Unmarshal(raw, &pageA)
			}
		}
		navAErr = callErr
	}()
	go func() {
		defer navGroup.Done()
		response, callErr := mcpRawCallWithMeta(ctx, transportB, 802, "navigate", map[string]any{"url": allowedA.URL + "/session-owner/probe"}, map[string]any{"go.klarlabs.de/scout/session": selectorName})
		if callErr == nil {
			var raw []byte
			raw, callErr = mcpRawResultJSON(response)
			if callErr == nil {
				callErr = json.Unmarshal(raw, &pageB)
			}
		}
		navBErr = callErr
	}()
	navGroup.Wait()
	if navAErr != nil {
		failure = "navigate through principal A: " + navAErr.Error()
	}
	if failure == "" && navBErr != nil {
		failure = "navigate through principal B: " + navBErr.Error()
	}
	if failure == "" {
		sameNameSelectorScoped = pageA.TargetID != "" && pageB.TargetID != "" && pageA.TargetID != pageB.TargetID
		if !sameNameSelectorScoped {
			failure = fmt.Sprintf("same-name session selector crossed principal scope: A=%s B=%s", pageA.TargetID, pageB.TargetID)
		}
	}
	if failure == "" {
		delayedURL := allowedA.URL + "/delayed-state?delay_ms=1500"
		if delayedPage, err := navigate(first, delayedURL); err != nil {
			failure = "navigate principal A to delayed task page: " + err.Error()
		} else {
			pageA = delayedPage
			taskDone := make(chan error, 1)
			go func() {
				_, taskErr := mcpCall(ctx, first, "task_run", map[string]any{
					"id":         "reconnect-task-probe",
					"target_id":  pageA.TargetID,
					"actions":    []any{map[string]any{"action": "wait", "selector": "#task-ready"}},
					"timeout_ms": 5000,
				})
				taskDone <- taskErr
			}()
			if taskErr := <-taskDone; taskErr != nil && failure == "" {
				failure = "finish delayed recovery task: " + taskErr.Error()
			}
			if failure == "" {
				resultResponse, resultErr := mcpCall(ctx, first, "get_task_run", map[string]any{"id": "reconnect-task-probe"})
				var lookup struct {
					Found  bool                 `json:"found"`
					Record *agent.TaskRunRecord `json:"record"`
				}
				if resultErr == nil {
					resultErr = json.Unmarshal(mcpResultJSON(resultResponse), &lookup)
				}
				if resultErr != nil || !lookup.Found || lookup.Record == nil || lookup.Record.InFlight || lookup.Record.Result == nil || lookup.Record.Result.Status != "completed" {
					failure = fmt.Sprintf("read completed task record: %v record=%#v", resultErr, lookup.Record)
				} else {
					taskLookupByOpaqueID = true
				}
			}
		}
	}
	endpointA, errA := listEndpoints(first)
	endpointB, errB := listEndpoints(second)
	if failure == "" && errA != nil {
		failure = "read principal A endpoint: " + errA.Error()
	}
	if failure == "" && errB != nil {
		failure = "read principal B endpoint: " + errB.Error()
	}
	if failure == "" && (endpointA != benchmarkEndpointLabel(browserA.WSEndpoint()) || endpointB != endpointA) {
		failure = fmt.Sprintf("principal endpoint routing mismatch: A=%q B=%q", endpointA, endpointB)
	}
	if failure == "" {
		var state agent.ElementResult
		for attempt := 0; attempt < 10; attempt++ {
			probe, probeErr := mcpCall(ctx, second, "extract", map[string]any{"selector": "#state"})
			if probeErr != nil {
				failure = "read principal B isolated storage: " + probeErr.Error()
				break
			}
			if err := json.Unmarshal(mcpResultJSON(probe), &state); err != nil {
				failure = "decode isolated storage: " + err.Error()
				break
			}
			if state.Text != "pending" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if failure == "" && state.Text != `["",null]` {
			failure = fmt.Sprintf("principal B observed unexpected or shared storage: %q", state.Text)
		}
		if failure == "" {
			storageIsolated = true
		}
	}
	if failure == "" {
		if _, err := navigate(first, "http://example.com/"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "origin") {
			failure = fmt.Sprintf("principal A did not reject principal B origin: %v", err)
		} else {
			originDenied = true
		}
	}
	if failure == "" {
		beforeResetTarget := pageB.TargetID
		if _, err := mcpCall(ctx, second, "reset", map[string]any{}); err != nil {
			failure = "reset principal B context: " + err.Error()
		} else {
			afterReset, err := navigate(second, allowedA.URL+"/session-owner/probe")
			if err != nil {
				failure = "navigate after principal B reset: " + err.Error()
			} else if afterReset.TargetID == beforeResetTarget {
				failure = "principal B reset reused its prior target"
			} else {
				var state agent.ElementResult
				probe, err := mcpCall(ctx, second, "extract", map[string]any{"selector": "#state"})
				if err != nil {
					failure = "read principal B post-reset storage: " + err.Error()
				} else if err := json.Unmarshal(mcpResultJSON(probe), &state); err != nil {
					failure = "decode principal B post-reset storage: " + err.Error()
				} else if state.Text != `["",null]` {
					failure = fmt.Sprintf("principal B post-reset storage was not empty: %q", state.Text)
				} else {
					resetIsolation = true
				}
			}
		}
	}
	if failure == "" {
		sessionID := captureA.sessionID()
		if sessionID == "" {
			failure = "principal A initialize response omitted MCP session ID"
		} else {
			probeReq, reqErr := http.NewRequestWithContext(ctx, http.MethodDelete, "http://"+addr+"/mcp", nil)
			if reqErr == nil {
				probeReq.Header.Set("Authorization", "Bearer benchmark-token-b")
				probeReq.Header.Set("Mcp-Session-Id", sessionID)
				var probeResp *http.Response
				probeResp, reqErr = http.DefaultClient.Do(probeReq)
				if reqErr == nil {
					_ = probeResp.Body.Close()
					sessionReuseDenied = probeResp.StatusCode == http.StatusUnauthorized || probeResp.StatusCode == http.StatusForbidden
				}
			}
			if reqErr != nil || !sessionReuseDenied {
				failure = fmt.Sprintf("principal B reused principal A MCP session ID: %v", reqErr)
			} else if _, err := mcpCall(ctx, first, "status", map[string]any{}); err != nil {
				failure = "principal A session was not usable after cross-principal reuse attempt: " + err.Error()
			} else {
				calls++
				ownerSessionRetained = true
				listed, listErr := mcpCall(ctx, first, "list_sessions", map[string]any{})
				calls++
				var summaries []benchmarkSessionSummary
				if listErr == nil {
					listErr = json.Unmarshal(mcpResultJSON(listed), &summaries)
				}
				var opaqueID string
				for _, summary := range summaries {
					if summary.Name == "default" {
						opaqueID = summary.SessionID
						break
					}
				}
				if listErr != nil || opaqueID == "" {
					failure = fmt.Sprintf("read principal A opaque session ID: %v", listErr)
				} else {
					reconnectCapture := &benchmarkSessionCaptureTransport{base: http.DefaultTransport}
					reconnectHTTP := &http.Client{Transport: reconnectCapture}
					reconnectTransport, reconnectErr := client.NewHTTPTransport("http://"+addr, client.WithHTTPClient(reconnectHTTP), client.WithHTTPHeader("Authorization", "Bearer benchmark-token-a"))
					if reconnectErr == nil {
						reconnectClient := client.New(reconnectTransport)
						if reconnectErr = initialize(reconnectClient); reconnectErr == nil {
							taskResponse, taskCallErr := mcpRawCallWithMeta(ctx, reconnectTransport, 990, "get_task_run", map[string]any{"id": "reconnect-task-probe"}, map[string]any{"go.klarlabs.de/scout/session": opaqueID})
							if taskCallErr != nil {
								reconnectErr = taskCallErr
							} else if rawTask, decodeErr := mcpRawResultJSON(taskResponse); decodeErr != nil {
								reconnectErr = fmt.Errorf("get_task_run by opaque ID: %w", decodeErr)
							} else {
								var lookup struct {
									Found  bool                 `json:"found"`
									Record *agent.TaskRunRecord `json:"record"`
								}
								if decodeErr := json.Unmarshal(rawTask, &lookup); decodeErr != nil {
									reconnectErr = fmt.Errorf("decode task lookup: %w", decodeErr)
								} else if !lookup.Found || lookup.Record == nil || lookup.Record.ID != "reconnect-task-probe" || lookup.Record.Result == nil || lookup.Record.Result.Status != "completed" {
									reconnectErr = fmt.Errorf("reconnected task lookup omitted the original record: %#v", lookup)
								} else {
									taskLookupByOpaqueID = true
								}
							}
							calls++
						}
						if reconnectErr == nil {
							response, callErr := mcpRawCallWithMeta(ctx, reconnectTransport, 991, "status", map[string]any{}, map[string]any{"go.klarlabs.de/scout/session": opaqueID})
							if callErr != nil {
								reconnectErr = callErr
							} else if response.Error != nil {
								reconnectErr = fmt.Errorf("status by opaque ID returned JSON-RPC error: %v", response.Error)
							} else if result, ok := response.Result.(map[string]any); !ok || result["isError"] == true {
								reconnectErr = fmt.Errorf("status by opaque ID returned an MCP error: %#v", response.Result)
							} else {
								structured, exists := result["structuredContent"]
								if !exists {
									if contents, ok := result["content"].([]any); ok && len(contents) > 0 {
										if content, ok := contents[0].(map[string]any); ok {
											if text, ok := content["text"].(string); ok {
												structured = json.RawMessage(text)
												exists = true
											}
										}
									}
								}
								encoded, encodeErr := json.Marshal(structured)
								var resumed agent.SessionStatus
								if !exists || encodeErr != nil {
									reconnectErr = fmt.Errorf("status by opaque ID omitted structured status: %v", encodeErr)
								} else if decodeErr := json.Unmarshal(encoded, &resumed); decodeErr != nil {
									reconnectErr = fmt.Errorf("decode resumed status: %w", decodeErr)
								} else if resumed.ActiveTargetID != pageA.TargetID || resumed.CurrentURL != pageA.URL {
									reconnectErr = fmt.Errorf("resumed status mismatched original session: target=%q URL=%q", resumed.ActiveTargetID, resumed.CurrentURL)
								} else {
									reconnectByOpaqueID = true
								}
							}
						}
						_ = reconnectClient.Close()
					}
					calls++
					if reconnectErr != nil {
						failure = "same-principal reconnect by opaque session ID failed: " + reconnectErr.Error()
					}
				}
				if failure == "" {
					foreignTransport, foreignErr := client.NewHTTPTransport("http://"+addr, client.WithHTTPHeader("Authorization", "Bearer benchmark-token-b"))
					if foreignErr == nil {
						foreignClient := client.New(foreignTransport)
						if foreignErr = initialize(foreignClient); foreignErr == nil {
							response, callErr := mcpRawCallWithMeta(ctx, foreignTransport, 992, "status", map[string]any{}, map[string]any{"go.klarlabs.de/scout/session": opaqueID})
							if callErr != nil {
								foreignErr = callErr
							} else if response.Error == nil && func() bool { result, ok := response.Result.(map[string]any); return !ok || result["isError"] != true }() {
								foreignErr = fmt.Errorf("foreign principal status unexpectedly succeeded: %#v", response.Result)
							} else {
								foreignOpaqueIDDenied = true
							}
						}
						_ = foreignClient.Close()
					}
					calls++
					if foreignErr != nil {
						failure = "foreign principal opaque session ID check failed: " + foreignErr.Error()
					}
				}
			}
		}
		if failure == "" {
			sessionID := captureA.sessionID()
			deleteReq, reqErr := http.NewRequestWithContext(ctx, http.MethodDelete, "http://"+addr+"/mcp", nil)
			if reqErr == nil {
				deleteReq.Header.Set("Authorization", "Bearer benchmark-token-a")
				deleteReq.Header.Set("Mcp-Session-Id", sessionID)
				var deleteResp *http.Response
				deleteResp, reqErr = (&http.Client{Transport: captureA}).Do(deleteReq)
				if reqErr == nil {
					_ = deleteResp.Body.Close()
					if deleteResp.StatusCode < 200 || deleteResp.StatusCode >= 300 {
						reqErr = fmt.Errorf("DELETE returned HTTP %d", deleteResp.StatusCode)
					}
				}
			}
			if reqErr != nil {
				failure = "delete principal A MCP session: " + reqErr.Error()
			} else {
				firstOpen = false
				if _, err := mcpCall(ctx, second, "status", map[string]any{}); err != nil {
					failure = "principal B did not survive principal A DELETE: " + err.Error()
				} else {
					calls++
					deleteIsolation = true
				}
			}
		}
	}
	_ = first.Close()
	passed := failure == "" && storageIsolated && resetIsolation && reconnectByOpaqueID && taskLookupByOpaqueID && foreignOpaqueIDDenied
	return scenarioResult{
		ID: fixture.ID, Success: passed && storageIsolated, Verified: passed && storageIsolated, Outcome: "principal_scoped_http_browser_context",
		Target: pageA.TargetID + "/" + pageB.TargetID, ActionCalls: calls,
		DurationMicros: time.Since(started).Microseconds(),
		Evidence:       fmt.Sprintf("distinct_tokens=true; shared_endpoint=%t; concurrent_navigation=true; same_name_selector=%s; same_name_selector_scoped=%t; endpoint_a=%s; endpoint_b=%s; distinct_targets=%t; storage_isolated=%t; reset_recreated_isolated_context=%t; origin_denial=%t; cross_principal_transport_id_denied=%t; cross_principal_opaque_id_denied=%t; owner_session_retained=%t; same_principal_reconnect_by_opaque_id=%t; task_lookup_by_opaque_id=%t; principal_a_delete_preserved_b=%t", endpointA == endpointB, selectorName, sameNameSelectorScoped, endpointA, endpointB, pageA.TargetID != "" && pageB.TargetID != "" && pageA.TargetID != pageB.TargetID, storageIsolated, resetIsolation, originDenied, sessionReuseDenied, foreignOpaqueIDDenied, ownerSessionRetained, reconnectByOpaqueID, taskLookupByOpaqueID, deleteIsolation),
		Error:          failure,
	}
}

func benchmarkEndpointLabel(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func runMCPHTTPDeleteCleanup(fixture fixtureScenario) scenarioResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	before, err := scoutChromePIDs()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "inspect baseline browser processes: " + err.Error()}
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve HTTP port: " + err.Error()}
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "mcp", "serve-http", "--listen="+addr)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Env = envWithout(os.Environ(), "SCOUT_REMOTE_CDP")
	cmd.Env = envWith(cmd.Env, "SCOUT_ALLOW_PRIVATE_IPS", "1")
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start HTTP MCP server: " + err.Error()}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	capture := &mcpSessionIDCapture{base: http.DefaultTransport}
	httpClient := &http.Client{Transport: capture}
	transport, err := client.NewHTTPTransport("http://"+addr, client.WithHTTPClient(httpClient))
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create HTTP transport: " + err.Error()}
	}
	mcpClient := client.New(transport)
	defer mcpClient.Close()
	for attempt := 0; attempt < 80; attempt++ {
		if _, err := mcpClient.Initialize(ctx); err == nil {
			break
		}
		if attempt == 79 {
			return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "HTTP MCP server did not become ready"}
		}
		time.Sleep(100 * time.Millisecond)
	}
	nav, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/concurrency-guard"})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "start owned browser session: " + err.Error()}
	}
	startedPIDs, err := scoutChromePIDs()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "inspect started browser process: " + err.Error()}
	}
	ownedPIDs := make(map[int]string)
	for pid, process := range startedPIDs {
		if _, existed := before[pid]; !existed {
			ownedPIDs[pid] = process
		}
	}
	if len(ownedPIDs) == 0 {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "navigation succeeded but no newly launched Chrome process was observable"}
	}
	if capture.sessionID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "HTTP transport did not expose the minted MCP session ID"}
	}
	deleteReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://"+addr+"/mcp", nil)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "build session DELETE request: " + err.Error()}
	}
	deleteReq.Header.Set("Mcp-Session-Id", capture.sessionID)
	deleteResp, err := httpClient.Do(deleteReq)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "delete MCP HTTP session: " + err.Error()}
	}
	_ = deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusNoContent {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("session DELETE returned HTTP %d", deleteResp.StatusCode)}
	}
	deadline := time.Now().Add(8 * time.Second)
	remaining := len(ownedPIDs)
	for time.Now().Before(deadline) {
		current, err := scoutChromePIDs()
		if err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "inspect browser cleanup: " + err.Error()}
		}
		remaining = 0
		for pid := range ownedPIDs {
			if _, alive := current[pid]; alive {
				remaining++
			}
		}
		if remaining == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	passed := remaining == 0
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "http_session_delete_cleanup_verified", ActionCalls: 3, OutputBytes: len(mcpResultJSON(nav)), DurationMicros: time.Since(started).Microseconds(), Evidence: fmt.Sprintf("http_delete_status=%d; owned_chrome_processes=%d; remaining_after_delete=%d", deleteResp.StatusCode, len(ownedPIDs), remaining)}
	if !passed {
		result.Error = "HTTP session DELETE returned but Scout-launched Chrome processes remained alive"
	}
	return result
}

func runMCPHTTPCloseDrain(fixture fixtureScenario) scenarioResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pageServer := httptest.NewServer(benchmarkfixture.Handler())
	defer pageServer.Close()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve HTTP port: " + err.Error()}
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	serverCmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "mcp", "serve-http", "--listen="+addr)
	serverCmd.Stdout, serverCmd.Stderr = io.Discard, io.Discard
	serverCmd.Env = envWith(envWithout(os.Environ(), "SCOUT_REMOTE_CDP"), "SCOUT_ALLOW_PRIVATE_IPS", "1")
	if err := serverCmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start HTTP MCP server: " + err.Error()}
	}
	defer func() {
		_ = serverCmd.Process.Kill()
		_, _ = serverCmd.Process.Wait()
	}()
	newClient := func() (*client.Client, error) {
		transport, err := client.NewHTTPTransport("http://"+addr, client.WithHTTPClient(&http.Client{Transport: http.DefaultTransport}))
		if err != nil {
			return nil, err
		}
		mcpClient := client.New(transport)
		if _, err := mcpClient.Initialize(ctx); err != nil {
			mcpClient.Close()
			return nil, err
		}
		return mcpClient, nil
	}
	var first *client.Client
	for attempt := 0; attempt < 80; attempt++ {
		first, err = newClient()
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize first MCP client: " + err.Error()}
	}
	defer first.Close()
	if _, err := mcpCall(ctx, first, "navigate", map[string]any{"url": pageServer.URL + "/delayed-element?delay=1200"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "navigate default session: " + err.Error()}
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := mcpCall(ctx, first, "wait_for", map[string]any{"selector": "#delayed-ready", "timeout_ms": 5000})
		waitDone <- err
	}()
	time.Sleep(150 * time.Millisecond)
	closeStarted := time.Now()
	type closeResponse struct {
		result *client.ToolResult
		err    error
		spent  time.Duration
	}
	closeDone := make(chan closeResponse, 1)
	go func() {
		result, err := mcpCall(ctx, first, "close_session", map[string]any{"name": "default"})
		closeDone <- closeResponse{result: result, err: err, spent: time.Since(closeStarted)}
	}()
	time.Sleep(100 * time.Millisecond)
	_, admissionErr := mcpCall(ctx, first, "status", map[string]any{})
	if admissionErr == nil || !strings.Contains(strings.ToLower(admissionErr.Error()), "closing") {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Error: fmt.Sprintf("new status call was not rejected while close drained: %v", admissionErr)}
	}
	closedResponse := <-closeDone
	closeResult, closeErr, closeDuration := closedResponse.result, closedResponse.err, closedResponse.spent
	waitErr := <-waitDone
	if waitErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "in-flight wait was interrupted by close_session: " + waitErr.Error()}
	}
	if closeErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "close_session failed: " + closeErr.Error()}
	}
	if closeDuration < 700*time.Millisecond {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("close_session returned before delayed wait drained (%s)", closeDuration)}
	}
	var closed struct {
		RegistryRemoved  bool `json:"registry_removed"`
		CleanupCompleted bool `json:"cleanup_completed"`
	}
	if err := json.Unmarshal(mcpResultJSON(closeResult), &closed); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "decode close result: " + err.Error()}
	}
	if !closed.RegistryRemoved || !closed.CleanupCompleted {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("close result did not confirm registry removal and cleanup: %+v", closed)}
	}
	result := scenarioResult{ID: fixture.ID, Success: true, Verified: true, Outcome: "inflight_session_call_drained_before_close", ActionCalls: 4, DurationMicros: time.Since(started).Microseconds(), Evidence: fmt.Sprintf("wait_completed=true; new_status_rejected_while_closing=true; close_duration_ms=%d; registry_removed=true; cleanup_completed=true", closeDuration.Milliseconds())}
	if encoded, err := json.Marshal(closeResult); err == nil {
		result.OutputBytes = len(encoded)
	}
	return result
}

func runMCPHTTPIdleStream(fixture fixtureScenario) scenarioResult {
	const idleTimeout = 1600 * time.Millisecond
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	before, err := scoutChromePIDs()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "inspect baseline browser processes: " + err.Error()}
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "reserve HTTP port: " + err.Error()}
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "mcp", "serve-http", "--listen="+addr)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Env = envWithout(os.Environ(), "SCOUT_REMOTE_CDP")
	cmd.Env = envWith(cmd.Env, "SCOUT_ALLOW_PRIVATE_IPS", "1")
	cmd.Env = envWith(cmd.Env, "SCOUT_SESSION_IDLE_TIMEOUT", idleTimeout.String())
	if err := cmd.Start(); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start idle HTTP MCP server: " + err.Error()}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	newClient := func() (*client.Client, *mcpSessionIDCapture, *http.Client, error) {
		capture := &mcpSessionIDCapture{base: http.DefaultTransport}
		httpClient := &http.Client{Transport: capture}
		transport, err := client.NewHTTPTransport("http://"+addr, client.WithHTTPClient(httpClient))
		if err != nil {
			return nil, nil, nil, err
		}
		return client.New(transport), capture, httpClient, nil
	}
	idleClient, idleCapture, idleHTTP, err := newClient()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create idle HTTP client: " + err.Error()}
	}
	activeClient, activeCapture, activeHTTP, err := newClient()
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "create active HTTP client: " + err.Error()}
	}
	defer idleClient.Close()
	defer activeClient.Close()
	initialize := func(c *client.Client) error {
		for attempt := 0; attempt < 80; attempt++ {
			if _, err := c.Initialize(ctx); err == nil {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("HTTP MCP server did not become ready")
	}
	if err := initialize(idleClient); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize idle client: " + err.Error()}
	}
	if err := initialize(activeClient); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize active client: " + err.Error()}
	}
	if _, err := mcpCall(ctx, idleClient, "navigate", map[string]any{"url": server.URL + "/concurrency-source"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "start idle client's browser: " + err.Error()}
	}
	firstPIDs, err := scoutChromePIDs()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "inspect idle client's browser: " + err.Error()}
	}
	idlePIDs := newChromePIDs(before, firstPIDs)
	if len(idlePIDs) == 0 {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "idle client's locally owned browser process was not observable"}
	}
	if _, err := mcpCall(ctx, activeClient, "navigate", map[string]any{"url": server.URL + "/concurrency-guard"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Error: "start active client's browser: " + err.Error()}
	}
	secondPIDs, err := scoutChromePIDs()
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Error: "inspect active client's browser: " + err.Error()}
	}
	activePIDs := newChromePIDs(firstPIDs, secondPIDs)
	if len(activePIDs) == 0 {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Error: "active client's locally owned browser process was not observable"}
	}
	openStream := func(capture *mcpSessionIDCapture, httpClient *http.Client) (*http.Response, context.CancelFunc, error) {
		streamCtx, streamCancel := context.WithCancel(ctx)
		req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, "http://"+addr+"/mcp", nil)
		if err != nil {
			streamCancel()
			return nil, nil, err
		}
		req.Header.Set("Mcp-Session-Id", capture.sessionID)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := httpClient.Do(req)
		if err != nil {
			streamCancel()
			return nil, nil, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			streamCancel()
			return nil, nil, fmt.Errorf("SSE stream returned HTTP %d", resp.StatusCode)
		}
		return resp, streamCancel, nil
	}
	idleStream, closeIdleStream, err := openStream(idleCapture, idleHTTP)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Error: "hold idle client's HTTP stream: " + err.Error()}
	}
	defer func() { closeIdleStream(); _ = idleStream.Body.Close() }()
	activeStream, closeActiveStream, err := openStream(activeCapture, activeHTTP)
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Error: "hold active client's HTTP stream: " + err.Error()}
	}
	defer func() { closeActiveStream(); _ = activeStream.Body.Close() }()
	deadline := time.Now().Add(2 * idleTimeout)
	activeCalls := 0
	for time.Now().Before(deadline) {
		if _, err := mcpCall(ctx, activeClient, "status", map[string]any{}); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 5 + activeCalls, Error: "active HTTP client failed during idle window: " + err.Error()}
		}
		activeCalls++
		time.Sleep(idleTimeout / 4)
	}
	idleSessionsRaw, err := mcpCall(ctx, idleClient, "list_sessions", map[string]any{})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 5 + activeCalls, Error: "read expired HTTP session: " + err.Error()}
	}
	var idleSessions []benchmarkSessionSummary
	if err := json.Unmarshal(mcpResultJSON(idleSessionsRaw), &idleSessions); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 6 + activeCalls, Error: "decode expired HTTP session: " + err.Error()}
	}
	var idleStarted bool
	idleFound := false
	for _, summary := range idleSessions {
		if summary.Name == "default" {
			idleFound = true
			idleStarted = summary.Started
		}
	}
	restartedRaw, err := mcpCall(ctx, idleClient, "navigate", map[string]any{"url": server.URL + "/concurrency-source"})
	var restartedPage agent.PageResult
	if err == nil {
		err = json.Unmarshal(mcpResultJSON(restartedRaw), &restartedPage)
	}
	restartErr := err
	activeStatusRaw, statusErr := mcpCall(ctx, activeClient, "status", map[string]any{})
	var activeStatus agent.SessionStatus
	if statusErr == nil {
		statusErr = json.Unmarshal(mcpResultJSON(activeStatusRaw), &activeStatus)
	}
	currentPIDs, processErr := scoutChromePIDs()
	if processErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 7 + activeCalls, Error: "inspect post-idle browser processes: " + processErr.Error()}
	}
	remainingIdle, remainingActive := liveChromePIDs(idlePIDs, currentPIDs), liveChromePIDs(activePIDs, currentPIDs)
	passed := restartErr == nil && statusErr == nil && idleFound && !idleStarted && restartedPage.TargetID != "" && activeStatus.BrowserAlive && restartedPage.TargetID != activeStatus.ActiveTargetID && remainingIdle == 0 && remainingActive > 0
	result := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "held_http_stream_idle_client_expired_active_client_alive", ActionCalls: 8 + activeCalls, OutputBytes: len(mcpResultJSON(idleSessionsRaw)) + len(mcpResultJSON(restartedRaw)) + len(mcpResultJSON(activeStatusRaw)), DurationMicros: time.Since(started).Microseconds(), Evidence: fmt.Sprintf("idle_timeout_ms=%d; idle_sse_stream_open=true; active_sse_stream_open=true; active_status_calls=%d; idle_session_started_after_timeout=%t; idle_session_restarted=%t; restarted_target_isolated=%t; idle_browser_processes_remaining=%d; active_browser_alive=%t; active_browser_processes_remaining=%d", idleTimeout.Milliseconds(), activeCalls, idleStarted, restartedPage.TargetID != "", restartedPage.TargetID != activeStatus.ActiveTargetID, remainingIdle, activeStatus.BrowserAlive, remainingActive)}
	if !passed {
		result.Error = fmt.Sprintf("HTTP per-client idle cleanup mismatch: idle_found=%t idle_started=%t restarted_target=%s active_target=%s active_alive=%t idle_processes=%d active_processes=%d restart_error=%v status_error=%v", idleFound, idleStarted, restartedPage.TargetID, activeStatus.ActiveTargetID, activeStatus.BrowserAlive, remainingIdle, remainingActive, restartErr, statusErr)
	}
	return result
}

func newChromePIDs(before, after map[int]string) map[int]string {
	newPIDs := make(map[int]string)
	for pid, process := range after {
		if _, existed := before[pid]; !existed {
			newPIDs[pid] = process
		}
	}
	return newPIDs
}

func liveChromePIDs(wanted, current map[int]string) int {
	count := 0
	for pid := range wanted {
		if _, alive := current[pid]; alive {
			count++
		}
	}
	return count
}

func envWithout(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func envWith(environment []string, name, value string) []string {
	updated := envWithout(environment, name)
	return append(updated, name+"="+value)
}

func runMCPLateActionCancellationRepeated(fixture fixtureScenario, baseURL string, repetitions int) scenarioResult {
	if repetitions < 1 || repetitions > 100 {
		return scenarioResult{ID: fixture.ID, Error: "repetitions must be between 1 and 100"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(repetitions)*8*time.Second+30*time.Second)
	defer cancel()
	rows := make([]scenarioResult, 0, repetitions)
	var totalBytes, totalCalls int
	for i := 0; i < repetitions; i++ {
		row, calls, err := runMCPLateActionCancellationOnce(ctx, i+1)
		totalCalls += calls
		totalBytes += row.OutputBytes
		rows = append(rows, row)
		if err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: totalCalls, OutputBytes: totalBytes, Error: fmt.Sprintf("repetition %d: %v", i+1, err)}
		}
	}
	callerReturn := make([]int64, 0, len(rows))
	fullRecovery := make([]int64, 0, len(rows))
	passed := len(rows) == repetitions
	for _, row := range rows {
		passed = passed && row.Success
		var callerUS, fullUS int64
		_, _ = fmt.Sscanf(row.Evidence, "caller_return_us=%d; full_recovery_us=%d", &callerUS, &fullUS)
		callerReturn = append(callerReturn, callerUS)
		fullRecovery = append(fullRecovery, fullUS)
	}
	sort.Slice(callerReturn, func(i, j int) bool { return callerReturn[i] < callerReturn[j] })
	sort.Slice(fullRecovery, func(i, j int) bool { return fullRecovery[i] < fullRecovery[j] })
	median := func(values []int64) int64 {
		middle := len(values) / 2
		if len(values)%2 == 1 {
			return values[middle]
		}
		return (values[middle-1] + values[middle]) / 2
	}
	if outputPath := os.Getenv("SCOUT_BENCHMARK_REPETITIONS_FILE"); outputPath != "" {
		if err := writeJSONFile(outputPath, rows); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: totalCalls, OutputBytes: totalBytes, Error: "write repeated MCP recovery report: " + err.Error()}
		}
	}
	return scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "unknown_then_reconciled", ActionCalls: totalCalls, OutputBytes: totalBytes, DurationMicros: median(fullRecovery), Evidence: fmt.Sprintf("repetitions=%d; successful_repetitions=%d; caller_return_median_ms=%.3f; full_recovery_median_ms=%.3f; full_recovery_min_ms=%.3f; full_recovery_max_ms=%.3f; native_recovery_record=true; same_target_reconciliation=true; replay_count=0", repetitions, countScenarioSuccess(rows), float64(median(callerReturn))/1000, float64(median(fullRecovery))/1000, float64(fullRecovery[0])/1000, float64(fullRecovery[len(fullRecovery)-1])/1000)}
}

func countScenarioSuccess(rows []scenarioResult) int {
	count := 0
	for _, row := range rows {
		if row.Success {
			count++
		}
	}
	return count
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func runMCPLateActionCancellationOnce(parent context.Context, repetition int) (scenarioResult, int, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	// Keep the signal counters in the same process as the harness so each run
	// starts from a clean fixture and does not depend on an external server mode.
	handler, _, _ := benchmarkfixture.HandlerWithLateActionSignals()
	server := httptest.NewServer(handler)
	defer server.Close()
	baseURL := server.URL
	previousPrivate, hadPrivate := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true"); err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 0, err
	}
	transport, err := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	restoreEnvironmentValue("SCOUT_ALLOW_PRIVATE_IPS", previousPrivate, hadPrivate)
	if err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 0, err
	}
	mcpClient := client.New(transport)
	defer mcpClient.Close()
	if _, err := mcpClient.Initialize(ctx); err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 1, err
	}
	nav, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": strings.TrimRight(baseURL, "/") + "/late-action"})
	if err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 2, fmt.Errorf("navigate: %w", err)
	}
	var page agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(nav), &page); err != nil || page.TargetID == "" {
		return scenarioResult{ID: "mcp-late-action-cancellation", OutputBytes: len(mcpResultJSON(nav))}, 2, fmt.Errorf("decode target: %v", err)
	}
	stateURL := strings.TrimRight(baseURL, "/") + "/late-action-state"
	before, err := readLateFixtureSignalState(ctx, stateURL)
	if err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 2, fmt.Errorf("read initial fixture signals: %w", err)
	}
	startedAt := time.Now()
	// Give MCP transport delivery enough time to return Scout's typed outcome;
	// the per-action timeout_ms is the deadline being exercised here.
	actionCtx, actionCancel := context.WithTimeout(ctx, 5*time.Second)
	actionDone := make(chan struct {
		result *client.ToolResult
		err    error
	}, 1)
	go func() {
		result, err := mcpClient.CallTool(actionCtx, "click", map[string]any{"selector": "#late-action", "target_id": page.TargetID, "expected_text": "Late completion", "timeout_ms": 300})
		actionDone <- struct {
			result *client.ToolResult
			err    error
		}{result, err}
	}()
	var action struct {
		result *client.ToolResult
		err    error
	}
	select {
	case action = <-actionDone:
	case <-ctx.Done():
		actionCancel()
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 3, ctx.Err()
	}
	returnedAt := time.Now()
	actionCancel()
	if action.err != nil || action.result == nil || !action.result.IsError || len(action.result.Content) == 0 {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 3, fmt.Errorf("click lacked unknown outcome: err=%v result=%v", action.err, action.result)
	}
	if err := waitForLateFixtureSignal(ctx, stateURL, before.StartedCount, false); err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 3, fmt.Errorf("click dispatch was not confirmed: %w", err)
	}
	afterReturn, err := readLateFixtureSignalState(ctx, stateURL)
	if err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 3, fmt.Errorf("read fixture state at outcome return: %w", err)
	}
	completedBeforeReturn := afterReturn.CompletedCount > before.CompletedCount
	var envelope struct {
		Code           string                      `json:"code"`
		Outcome        string                      `json:"outcome"`
		TargetID       string                      `json:"target_id"`
		RecoveryRecord *agent.ActionRecoveryRecord `json:"recovery_record"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(action.result.Content[0].Text, "SCOUT_ERROR ")), &envelope); err != nil || envelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || envelope.Outcome != string(agent.OutcomeUnknown) || envelope.TargetID != page.TargetID || envelope.RecoveryRecord == nil {
		return scenarioResult{ID: "mcp-late-action-cancellation", OutputBytes: len(mcpResultJSON(action.result))}, 3, fmt.Errorf("unknown outcome envelope invalid: %v", err)
	}
	unknown := envelope.RecoveryRecord.ActionOutcome
	if err := envelope.RecoveryRecord.Validate(); err != nil || unknown.ExpectedPostcondition == nil || unknown.ExpectedPostcondition.Text != "Late completion" || unknown.OperationOrdinal == 0 {
		return scenarioResult{ID: "mcp-late-action-cancellation", OutputBytes: len(mcpResultJSON(action.result))}, 3, fmt.Errorf("recovery record invalid: %v", err)
	}
	if err := waitForLateFixtureSignal(ctx, stateURL, before.CompletedCount, true); err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 3, fmt.Errorf("late mutation completion was not confirmed: %w", err)
	}
	reconciled, err := mcpCall(ctx, mcpClient, "reconcile_action", map[string]any{"recovery_record": envelope.RecoveryRecord})
	if err != nil {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 4, err
	}
	var verified agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(reconciled), &verified); err != nil || !verified.Verified || verified.Outcome != string(agent.OutcomePostconditionVerified) || verified.TargetID != page.TargetID || verified.Receipt == nil || verified.Receipt.RecoveryOfOperationOrdinal != unknown.OperationOrdinal {
		return scenarioResult{ID: "mcp-late-action-cancellation"}, 4, fmt.Errorf("reconciliation failed: %v", err)
	}
	callerUS := returnedAt.Sub(startedAt).Microseconds()
	fullUS := time.Since(startedAt).Microseconds()
	actionJSON := serializedToolResult(action.result)
	reconcileJSON := serializedToolResult(reconciled)
	navigateJSON := serializedToolResult(nav)
	serialized := map[string]json.RawMessage{"navigate": navigateJSON, "click": actionJSON, "reconcile_action": reconcileJSON}
	joinedResults := string(navigateJSON) + string(actionJSON) + string(reconcileJSON)
	sentinelLeaked := strings.Contains(joinedResults, "SCOUT_PRIVATE_SENTINEL_6b29f0a1")
	row := scenarioResult{ID: "mcp-late-action-cancellation", Success: callerUS <= 350000 && fullUS > callerUS && !completedBeforeReturn, Verified: verified.Verified, Outcome: verified.Outcome, Target: verified.TargetID, ActionCalls: 3, OutputBytes: len(navigateJSON) + len(actionJSON) + len(reconcileJSON), DurationMicros: callerUS, Evidence: fmt.Sprintf("caller_return_us=%d; full_recovery_us=%d; repetition=%d; dispatch_signal_confirmed=true; completion_signal_confirmed=true; completion_before_outcome_return=%t; unknown_outcome=true; expected_postcondition_retained=true; same_target_reconciled=true; recovery_receipt_linked=true; replay_count=0; privacy_sentinel_leaked=%t", callerUS, fullUS, repetition, completedBeforeReturn, sentinelLeaked), SerializedResults: serialized, SerializedResultsKind: "normalized_go_mcp_client_tool_result_objects_excluding_jsonrpc_framing", PrivacySentinelLeaked: sentinelLeaked}
	return row, 3, nil
}

func serializedToolResult(result *client.ToolResult) json.RawMessage {
	if result == nil {
		return json.RawMessage("null")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}

func runMCPConcurrentTargetRace(fixture fixtureScenario, useHTTP bool) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote browser: " + err.Error()}
	}
	defer browser.Close()
	previous, hadPrevious := os.LookupEnv("SCOUT_REMOTE_CDP")
	previousPrivate, hadPrivate := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_REMOTE_CDP", browser.WSEndpoint()); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "configure remote browser: " + err.Error()}
	}
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "1"); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "allow local fixture: " + err.Error()}
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("SCOUT_REMOTE_CDP", previous)
		} else {
			_ = os.Unsetenv("SCOUT_REMOTE_CDP")
		}
		if hadPrivate {
			_ = os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", previousPrivate)
		} else {
			_ = os.Unsetenv("SCOUT_ALLOW_PRIVATE_IPS")
		}
	}()
	var mcpClient, secondClient *client.Client
	if useHTTP {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return scenarioResult{ID: fixture.ID, Error: "reserve HTTP port: " + err.Error()}
		}
		addr := probe.Addr().String()
		_ = probe.Close()
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/scout", "mcp", "serve-http", "--listen="+addr)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			return scenarioResult{ID: fixture.ID, Error: "start HTTP MCP server: " + err.Error()}
		}
		defer func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}()
		newClient := func() (*client.Client, error) {
			transport, err := client.NewHTTPTransport("http://" + addr)
			if err != nil {
				return nil, err
			}
			c := client.New(transport)
			return c, nil
		}
		mcpClient, err = newClient()
		if err == nil {
			secondClient, err = newClient()
		}
		if err != nil {
			return scenarioResult{ID: fixture.ID, Error: "create HTTP MCP client: " + err.Error()}
		}
		defer mcpClient.Close()
		defer secondClient.Close()
		initialize := func(c *client.Client) error {
			for attempt := 0; attempt < 80; attempt++ {
				if _, err := c.Initialize(ctx); err == nil {
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return fmt.Errorf("HTTP MCP server did not become ready")
		}
		if err := initialize(mcpClient); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize HTTP MCP: " + err.Error()}
		}
		if err := initialize(secondClient); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize second HTTP MCP client: " + err.Error()}
		}
	} else {
		transport, err := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
		if err != nil {
			return scenarioResult{ID: fixture.ID, Error: "start MCP transport: " + err.Error()}
		}
		defer transport.Close()
		mcpClient = client.New(transport)
		if _, err := mcpClient.Initialize(ctx); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize MCP: " + err.Error()}
		}
		secondClient = mcpClient
	}
	var source agent.PageResult
	first, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/concurrency-source"})
	if err == nil {
		err = json.Unmarshal(mcpResultJSON(first), &source)
	}
	if err != nil || source.TargetID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: fmt.Sprintf("decode source target: %v", err)}
	}
	guardRaw, err := mcpCall(ctx, mcpClient, "open_tab", map[string]any{"name": "race-guard", "url": server.URL + "/concurrency-guard", "target_id": source.TargetID})
	var guard agent.PageResult
	if err == nil {
		err = json.Unmarshal(mcpResultJSON(guardRaw), &guard)
	}
	if err != nil || guard.TargetID == "" || guard.TargetID == source.TargetID {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("open guard target: %v", err)}
	}
	if _, err := mcpCall(ctx, mcpClient, "switch_tab", map[string]any{"name": "default", "target_id": guard.TargetID}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 4, Error: "activate source target: " + err.Error()}
	}
	started := time.Now()
	type callOutcome struct {
		result *client.ToolResult
		err    error
	}
	waitDone := make(chan callOutcome, 1)
	go func() {
		r, e := mcpClient.CallTool(ctx, "wait_for", map[string]any{"selector": "#race-ready", "target_id": source.TargetID})
		waitDone <- callOutcome{r, e}
	}()
	// The source page keeps this operation inside the session lock for about
	// 1.8s, leaving time to queue a target switch and an old-target mutation.
	time.Sleep(120 * time.Millisecond)
	switchDone := make(chan callOutcome, 1)
	go func() {
		r, e := mcpClient.CallTool(ctx, "switch_tab", map[string]any{"name": "race-guard", "target_id": source.TargetID})
		switchDone <- callOutcome{r, e}
	}()
	time.Sleep(80 * time.Millisecond)
	clickDone := make(chan callOutcome, 1)
	go func() {
		r, e := mcpClient.CallTool(ctx, "click", map[string]any{"selector": "#race-click", "target_id": source.TargetID, "expected_text": "Source clicked", "timeout_ms": 5000})
		clickDone <- callOutcome{r, e}
	}()
	waitResult, switchResult, clickResult := <-waitDone, <-switchDone, <-clickDone
	if waitResult.err != nil || waitResult.result == nil || waitResult.result.IsError {
		return scenarioResult{ID: fixture.ID, ActionCalls: 7, Error: fmt.Sprintf("source wait failed: err=%v result=%v", waitResult.err, waitResult.result)}
	}
	var waited agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(waitResult.result), &waited); err != nil || waited.TargetID != source.TargetID {
		return scenarioResult{ID: fixture.ID, ActionCalls: 7, Error: fmt.Sprintf("wait returned wrong target %q: %v", waited.TargetID, err)}
	}
	if switchResult.err != nil || switchResult.result == nil || switchResult.result.IsError {
		return scenarioResult{ID: fixture.ID, ActionCalls: 7, Error: fmt.Sprintf("concurrent target switch failed: err=%v result=%v", switchResult.err, switchResult.result)}
	}
	var switched agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(switchResult.result), &switched); err != nil || switched.TargetID != guard.TargetID {
		return scenarioResult{ID: fixture.ID, ActionCalls: 7, Error: fmt.Sprintf("switch returned wrong target %q: %v", switched.TargetID, err)}
	}
	clickRejectedStale, clickOnSource := false, false
	clickDescription := ""
	if clickResult.err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 7, Error: "concurrent click transport failed: " + clickResult.err.Error()}
	}
	if clickResult.result != nil && clickResult.result.IsError && len(clickResult.result.Content) > 0 {
		code, cause, _ := mcpErrorEnvelope(fmt.Errorf("%s", clickResult.result.Content[0].Text))
		clickRejectedStale = code == "SCOUT_OPERATION_ERROR" && cause == "stale_target"
		clickDescription = fmt.Sprintf("error=%s cause=%s", code, cause)
	} else if clickResult.result != nil {
		var clicked agent.PageResult
		if err := json.Unmarshal(mcpResultJSON(clickResult.result), &clicked); err == nil && clicked.TargetID == source.TargetID && clicked.Verified && clicked.Outcome == string(agent.OutcomePostconditionVerified) {
			clickOnSource = true
			clickDescription = fmt.Sprintf("source_target=%s outcome=%s", clicked.TargetID, clicked.Outcome)
		}
	}
	guardCheck, guardErr := mcpCall(ctx, mcpClient, "assert_text_contains", map[string]any{"text": "Guard untouched", "target_id": guard.TargetID})
	var guardState struct {
		OK bool `json:"ok"`
	}
	if guardErr == nil {
		guardErr = json.Unmarshal(mcpResultJSON(guardCheck), &guardState)
	}
	guardUnchanged := guardErr == nil && guardState.OK
	clientSelectionIsolated := true
	clientSelectionChecked := false
	selectionEvidence := "single_client"
	var clientTwoTarget string
	selectionBytes := 0
	if useHTTP && guardErr == nil {
		clientSelectionChecked = true
		created, err := mcpCall(ctx, secondClient, "create_session", map[string]any{"name": "http-client-two"})
		selectionBytes += len(mcpResultJSON(created))
		if err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 9, Error: "second client could not create named session: " + err.Error()}
		}
		nav, err := mcpCall(ctx, secondClient, "navigate", map[string]any{"url": server.URL + "/concurrency-guard"})
		selectionBytes += len(mcpResultJSON(nav))
		var secondPage agent.PageResult
		if err == nil {
			err = json.Unmarshal(mcpResultJSON(nav), &secondPage)
		}
		if err != nil || secondPage.TargetID == "" {
			return scenarioResult{ID: fixture.ID, ActionCalls: 10, Error: fmt.Sprintf("second client named-session navigation failed: %v", err)}
		}
		clientTwoTarget = secondPage.TargetID
		firstStatusRaw, firstErr := mcpCall(ctx, mcpClient, "status", map[string]any{})
		selectionBytes += len(mcpResultJSON(firstStatusRaw))
		var firstStatus agent.SessionStatus
		if firstErr == nil {
			firstErr = json.Unmarshal(mcpResultJSON(firstStatusRaw), &firstStatus)
		}
		firstStayedSelected := firstErr == nil && firstStatus.ActiveTargetID == guard.TargetID
		secondConfigIsolated := false
		createdForFirst, err := mcpCall(ctx, mcpClient, "create_session", map[string]any{"name": "http-client-two"})
		selectionBytes += len(mcpResultJSON(createdForFirst))
		if err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 11, Error: "first client could not create same-named private session: " + err.Error()}
		}
		firstPageRaw, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/concurrency-source"})
		selectionBytes += len(mcpResultJSON(firstPageRaw))
		var firstPage agent.PageResult
		if err == nil {
			err = json.Unmarshal(mcpResultJSON(firstPageRaw), &firstPage)
		}
		if err != nil || firstPage.TargetID == "" {
			return scenarioResult{ID: fixture.ID, ActionCalls: 12, Error: fmt.Sprintf("first client same-named session navigation failed: %v", err)}
		}
		firstLocalNavigationSucceeded := true
		privateCreated, err := mcpCall(ctx, mcpClient, "create_session", map[string]any{"name": "private-to-one"})
		selectionBytes += len(mcpResultJSON(privateCreated))
		if err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: 13, Error: "first client could not create private-only session: " + err.Error()}
		}
		foreignSwitch, foreignSwitchErr := secondClient.CallTool(ctx, "switch_session", map[string]any{"name": "private-to-one"})
		foreignSwitchRejected := foreignSwitchErr == nil && foreignSwitch != nil && foreignSwitch.IsError
		if foreignSwitchErr != nil && strings.Contains(foreignSwitchErr.Error(), "private-to-one") {
			foreignSwitchRejected = true
		}
		if foreignSwitch != nil {
			selectionBytes += len(mcpResultJSON(foreignSwitch))
		}
		secondStatusRaw, secondErr := mcpCall(ctx, secondClient, "status", map[string]any{})
		selectionBytes += len(mcpResultJSON(secondStatusRaw))
		var secondStatus agent.SessionStatus
		if secondErr == nil {
			secondErr = json.Unmarshal(mcpResultJSON(secondStatusRaw), &secondStatus)
		}
		configResult, configErr := mcpCall(ctx, secondClient, "configure", map[string]any{"allow_private_ips": false})
		selectionBytes += len(mcpResultJSON(configResult))
		secondConfigIsolated = configErr == nil && strings.Contains(string(mcpResultJSON(configResult)), "private IPs blocked")
		clientSelectionIsolated = firstStayedSelected && secondConfigIsolated && foreignSwitchRejected && secondErr == nil && secondStatus.ActiveTargetID == clientTwoTarget && firstPage.TargetID != secondStatus.ActiveTargetID
		selectionEvidence = fmt.Sprintf("client_one_kept_target=%s; client_one_same_name_target=%s; client_two_same_name_target=%s; client_two_private_ips_blocked=%t; client_one_private_fixture_navigation_succeeded=%t; foreign_session_switch_rejected=%t; foreign_switch_error=%v", firstStatus.ActiveTargetID, firstPage.TargetID, secondStatus.ActiveTargetID, secondConfigIsolated, firstLocalNavigationSucceeded, foreignSwitchRejected, foreignSwitchErr)
	}
	passed := clickRejectedStale || clickOnSource
	passed = passed && guardUnchanged && clientSelectionIsolated
	transportName := "stdio_single_client"
	if useHTTP {
		transportName = "streamable_http_two_clients"
	}
	callCount := 8
	if useHTTP {
		callCount += 8
	}
	out := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "concurrent_target_binding_verified", Target: guard.TargetID, ActionCalls: callCount, OutputBytes: len(mcpResultJSON(waitResult.result)) + len(mcpResultJSON(switchResult.result)) + len(mcpResultJSON(clickResult.result)) + len(mcpResultJSON(guardCheck)) + selectionBytes, DurationMicros: time.Since(started).Microseconds(), Evidence: fmt.Sprintf("transport=%s; parallel_requests=wait+switch+click; wait_target=%s; switched_target=%s; click=%s; click_pinned_to_source_or_refused=%t; guard_unchanged=%t; client_session_selection_checked=%t; client_session_selection_isolated=%t; %s", transportName, waited.TargetID, switched.TargetID, clickDescription, passed && (clickRejectedStale || clickOnSource), guardUnchanged, clientSelectionChecked, clientSelectionIsolated, selectionEvidence)}
	if !passed {
		out.Error = fmt.Sprintf("concurrent target safety or session selection failed: stale_rejected=%t click_on_source=%t guard_unchanged=%t client_selection_isolated=%t guard_error=%v", clickRejectedStale, clickOnSource, guardUnchanged, clientSelectionIsolated, guardErr)
	}
	return out
}

func runMCPSessionOwnerRace(fixture fixtureScenario) scenarioResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server := httptest.NewServer(benchmarkfixture.Handler())
	defer server.Close()
	previousPrivate, hadPrivate := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "true"); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "allow local benchmark fixture: " + err.Error()}
	}
	transport, err := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	restoreEnvironmentValue("SCOUT_ALLOW_PRIVATE_IPS", previousPrivate, hadPrivate)
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start MCP transport: " + err.Error()}
	}
	defer transport.Close()
	mcp := client.New(transport)
	if _, err := mcp.Initialize(ctx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize MCP client: " + err.Error()}
	}
	calls := 1
	call := func(name string, args map[string]any) (*client.ToolResult, error) {
		calls++
		return mcpCall(ctx, mcp, name, args)
	}
	for _, name := range []string{"left", "right"} {
		if _, err := call("create_session", map[string]any{"name": name}); err != nil {
			return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "create " + name + " session: " + err.Error()}
		}
	}
	if _, err := call("switch_session", map[string]any{"name": "right"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "select right session for seed: " + err.Error()}
	}
	if _, err := call("navigate", map[string]any{"url": server.URL + "/session-owner/check"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "navigate right seed session: " + err.Error()}
	}
	if _, err := call("switch_session", map[string]any{"name": "left"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "select source session: " + err.Error()}
	}
	if _, err := call("navigate", map[string]any{"url": server.URL + "/session-owner-delayed/primary"}); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "navigate source session: " + err.Error()}
	}
	// CaptureProfile waits for the delayed DOM read, leaving a deterministic window
	// in which another request can change the owner's active session.
	type outcome struct {
		result *client.ToolResult
		err    error
	}
	cloneDone := make(chan outcome, 1)
	go func() {
		cloneDone <- func() outcome {
			r, e := mcp.CallTool(ctx, "clone_session", map[string]any{"name": "snapshot"})
			return outcome{r, e}
		}()
	}()
	time.Sleep(200 * time.Millisecond)
	switchDone := make(chan outcome, 1)
	go func() {
		switchDone <- func() outcome {
			r, e := mcp.CallTool(ctx, "switch_session", map[string]any{"name": "right"})
			return outcome{r, e}
		}()
	}()
	cloneResult, switchResult := <-cloneDone, <-switchDone
	if cloneResult.err != nil || cloneResult.result == nil || cloneResult.result.IsError {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls + 2, Error: fmt.Sprintf("clone during concurrent switch failed: %v", cloneResult.err)}
	}
	if switchResult.err != nil || switchResult.result == nil || switchResult.result.IsError {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls + 2, Error: fmt.Sprintf("concurrent session switch failed: %v", switchResult.err)}
	}
	statusRaw, err := call("status", map[string]any{})
	var status agent.SessionStatus
	if err == nil {
		err = json.Unmarshal(mcpResultJSON(statusRaw), &status)
	}
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "read active session status: " + err.Error()}
	}
	listRaw, err := call("list_sessions", map[string]any{})
	var sessions []benchmarkSessionSummary
	if err == nil {
		err = json.Unmarshal(mcpResultJSON(listRaw), &sessions)
	}
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: calls, Error: "list cloned session: " + err.Error()}
	}
	var snapshot *benchmarkSessionSummary
	for i := range sessions {
		if sessions[i].Name == "snapshot" {
			snapshot = &sessions[i]
			break
		}
	}
	passed := snapshot != nil && snapshot.CurrentURL == server.URL+"/session-owner-delayed/primary" && !snapshot.Active
	var rightActive bool
	for _, summary := range sessions {
		if summary.Name == "right" {
			rightActive = summary.Active
		}
	}
	passed = passed && rightActive
	evidence := fmt.Sprintf("concurrent_switch_to_right_succeeded=true; clone_source_url=%s; cloned_session_url=%s; newer_switch_preserved=%t", server.URL+"/session-owner-delayed/primary", valueOrEmpty(snapshot, func(s *benchmarkSessionSummary) string { return s.CurrentURL }), rightActive && snapshot != nil && !snapshot.Active)
	out := scenarioResult{ID: fixture.ID, Success: passed, Verified: passed, Outcome: "owner_tool_route_pinned_during_switch", Target: valueOrEmpty(snapshot, func(s *benchmarkSessionSummary) string { return s.SessionID }), ActionCalls: calls, OutputBytes: len(mcpResultJSON(cloneResult.result)) + len(mcpResultJSON(switchResult.result)) + len(mcpResultJSON(statusRaw)) + len(mcpResultJSON(listRaw)), DurationMicros: time.Since(started).Microseconds(), Evidence: evidence}
	if !passed {
		out.Error = fmt.Sprintf("owner-level clone routing contract failed: clone=%+v active_status=%+v sessions=%+v", cloneResult.result, status, sessions)
	}
	if !passed {
		return out
	}

	// Hold the left session lock in wait_for, queue an owner-level status call,
	// then switch to right before the delayed selector appears. Status must still
	// describe the request-entry session (left).
	if _, err := call("switch_session", map[string]any{"name": "left"}); err != nil {
		out.Success = false
		out.Error = "select left for status race: " + err.Error()
		return out
	}
	if _, err := call("navigate", map[string]any{"url": server.URL + "/session-owner-delayed/status-race"}); err != nil {
		out.Success = false
		out.Error = "navigate status-race session: " + err.Error()
		return out
	}
	leftStatusRaw, err := call("status", map[string]any{})
	var leftStatus agent.SessionStatus
	if err == nil {
		err = json.Unmarshal(mcpResultJSON(leftStatusRaw), &leftStatus)
	}
	if err != nil || leftStatus.ActiveTargetID == "" {
		out.Success = false
		out.Error = fmt.Sprintf("capture left status target: %v status=%+v", err, leftStatus)
		return out
	}
	type ownerOutcome struct {
		result *client.ToolResult
		err    error
	}
	waitDone := make(chan ownerOutcome, 1)
	go func() {
		r, e := mcp.CallTool(ctx, "wait_for", map[string]any{"selector": "#ready"})
		waitDone <- ownerOutcome{r, e}
	}()
	time.Sleep(100 * time.Millisecond)
	statusDone := make(chan ownerOutcome, 1)
	go func() {
		r, e := mcp.CallTool(ctx, "status", map[string]any{})
		statusDone <- ownerOutcome{r, e}
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := call("switch_session", map[string]any{"name": "right"}); err != nil {
		out.Success = false
		out.Error = "switch during queued status: " + err.Error()
		return out
	}
	waitResult, statusResult := <-waitDone, <-statusDone
	var racedStatus agent.SessionStatus
	if statusResult.err == nil && statusResult.result != nil {
		statusResult.err = json.Unmarshal(mcpResultJSON(statusResult.result), &racedStatus)
	}
	statusRacePassed := waitResult.err == nil && waitResult.result != nil && !waitResult.result.IsError && statusResult.err == nil && statusResult.result != nil && !statusResult.result.IsError && racedStatus.ActiveTargetID == leftStatus.ActiveTargetID
	if !statusRacePassed {
		out.Success = false
		out.Error = fmt.Sprintf("status/switch did not preserve request-entry session: wait_err=%v status_err=%v expected_target=%s actual=%s", waitResult.err, statusResult.err, leftStatus.ActiveTargetID, racedStatus.ActiveTargetID)
		return out
	}

	// Repeat the lock/switch boundary with reset. Reset must affect the captured
	// left session while the newer switch stays on right; right's target remains.
	rightStatusRaw, err := call("status", map[string]any{})
	var rightStatusBefore agent.SessionStatus
	if err == nil {
		err = json.Unmarshal(mcpResultJSON(rightStatusRaw), &rightStatusBefore)
	}
	if err != nil || rightStatusBefore.ActiveTargetID == "" {
		out.Success = false
		out.Error = fmt.Sprintf("capture right target before reset race: %v status=%+v", err, rightStatusBefore)
		return out
	}
	if _, err := call("switch_session", map[string]any{"name": "left"}); err != nil {
		out.Success = false
		out.Error = "select left for reset race: " + err.Error()
		return out
	}
	if _, err := call("navigate", map[string]any{"url": server.URL + "/session-owner-delayed/reset-race"}); err != nil {
		out.Success = false
		out.Error = "navigate reset-race session: " + err.Error()
		return out
	}
	waitDone = make(chan ownerOutcome, 1)
	go func() {
		r, e := mcp.CallTool(ctx, "wait_for", map[string]any{"selector": "#ready"})
		waitDone <- ownerOutcome{r, e}
	}()
	time.Sleep(100 * time.Millisecond)
	resetDone := make(chan ownerOutcome, 1)
	go func() {
		r, e := mcp.CallTool(ctx, "reset", map[string]any{})
		resetDone <- ownerOutcome{r, e}
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := call("switch_session", map[string]any{"name": "right"}); err != nil {
		out.Success = false
		out.Error = "switch during queued reset: " + err.Error()
		return out
	}
	waitResult, resetResult := <-waitDone, <-resetDone
	var reset agent.ResetResult
	if resetResult.err == nil && resetResult.result != nil {
		resetResult.err = json.Unmarshal(mcpResultJSON(resetResult.result), &reset)
	}
	postRightRaw, rightErr := call("status", map[string]any{})
	var rightStatusAfter agent.SessionStatus
	if rightErr == nil {
		rightErr = json.Unmarshal(mcpResultJSON(postRightRaw), &rightStatusAfter)
	}
	resetRacePassed := waitResult.err == nil && waitResult.result != nil && !waitResult.result.IsError && resetResult.err == nil && resetResult.result != nil && !resetResult.result.IsError && reset.Verified && reset.BrowserAlive && rightErr == nil && rightStatusAfter.ActiveTargetID == rightStatusBefore.ActiveTargetID
	if !resetRacePassed {
		out.Success = false
		out.Error = fmt.Sprintf("reset/switch did not preserve owner routing: wait_err=%v reset_err=%v reset=%+v right_before=%s right_after=%s status_err=%v", waitResult.err, resetResult.err, reset, rightStatusBefore.ActiveTargetID, rightStatusAfter.ActiveTargetID, rightErr)
		return out
	}
	out.ActionCalls = calls + 10
	out.OutputBytes += len(mcpResultJSON(waitResult.result)) + len(mcpResultJSON(statusResult.result)) + len(mcpResultJSON(resetResult.result)) + len(mcpResultJSON(postRightRaw))
	out.DurationMicros = time.Since(started).Microseconds()
	out.Evidence += fmt.Sprintf("; status_switch_pinned_left=%t; reset_switch_pinned_left=%t; right_target_preserved=%t", statusRacePassed, resetRacePassed, rightStatusAfter.ActiveTargetID == rightStatusBefore.ActiveTargetID)
	out.Outcome = "clone_status_reset_owner_routes_pinned_during_switch"
	return out
}

func runMCPLateMutationCase(ctx context.Context, mcpClient *client.Client, baseURL, action, path string) (int, error) {
	calls := 1
	nav, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": baseURL + path})
	if err != nil {
		return 1, fmt.Errorf("navigate %s: %w", path, err)
	}
	var page agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(nav), &page); err != nil || page.TargetID == "" {
		return 1, fmt.Errorf("decode %s target: %v", path, err)
	}
	args := map[string]any{"target_id": page.TargetID, "timeout_ms": 300}
	expectedText := "Late completion"
	switch action {
	case "click_label", "click_handle":
		calls++
		annotated, annotateErr := mcpCall(ctx, mcpClient, "annotated_screenshot", map[string]any{"target_id": page.TargetID})
		if annotateErr != nil {
			return 2, fmt.Errorf("annotate %s: %w", path, annotateErr)
		}
		var observation struct {
			Elements []agent.AnnotatedElement `json:"elements"`
		}
		if err := json.Unmarshal(mcpResultJSON(annotated), &observation); err != nil {
			return 2, fmt.Errorf("decode %s annotation: %w", path, err)
		}
		var target *agent.AnnotatedElement
		for i := range observation.Elements {
			if observation.Elements[i].Selector == "#late-action" {
				target = &observation.Elements[i]
				break
			}
		}
		if target == nil {
			return 2, fmt.Errorf("%s annotation omitted #late-action", path)
		}
		if action == "click_label" {
			args["label"] = strconv.Itoa(target.Label)
		} else {
			args["handle"] = target.NodeHandle
			args["frame_id"] = ""
		}
	case "click_text":
		args["text"], args["role"] = "Start task", "button"
	case "dispatch_event":
		args["selector"], args["event_type"] = "#late-event", "late-task"
		expectedText = "Late event completion"
	case "drag_drop":
		args["from"], args["to"] = "#late-source", "#late-target"
		expectedText = "Late drop completion"
	case "select_option":
		args["selector"], args["option"] = "#late-select", "Ready"
	case "hover":
		args["selector"] = "#late-hover"
		expectedText = "Hover menu opened"
	default:
		return 1, fmt.Errorf("unsupported late mutation tool %q", action)
	}
	if action != "select_option" {
		args["expected_text"] = expectedText
	}
	callCtx, callCancel := context.WithTimeout(ctx, 10*time.Second)
	result, callErr := mcpClient.CallTool(callCtx, action, args)
	callCancel()
	calls++
	if callErr != nil || result == nil || !result.IsError || len(result.Content) == 0 {
		return 2, fmt.Errorf("%s did not return unknown outcome: result=%v error=%v", action, result, callErr)
	}
	var envelope struct {
		Code           string                      `json:"code"`
		Outcome        string                      `json:"outcome"`
		TargetID       string                      `json:"target_id"`
		ConfiguredMS   int64                       `json:"configured_timeout_ms"`
		RecoveryRecord *agent.ActionRecoveryRecord `json:"recovery_record"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(result.Content[0].Text, "SCOUT_ERROR ")), &envelope); err != nil {
		return 2, fmt.Errorf("decode %s outcome: %w (%s)", action, err, result.Content[0].Text)
	}
	if envelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || envelope.Outcome != string(agent.OutcomeUnknown) || envelope.TargetID != page.TargetID || envelope.ConfiguredMS != 300 || envelope.RecoveryRecord == nil {
		return 2, fmt.Errorf("%s returned incomplete unknown outcome: %+v", action, envelope)
	}
	if err := envelope.RecoveryRecord.Validate(); err != nil {
		return 2, fmt.Errorf("%s recovery record invalid: %w", action, err)
	}
	unknown := envelope.RecoveryRecord.ActionOutcome
	postconditionMatches := unknown.ExpectedPostcondition != nil && unknown.ExpectedPostcondition.Text == expectedText
	if action == "select_option" {
		postconditionMatches = unknown.ExpectedPostcondition != nil && unknown.ExpectedPostcondition.ValueSelector == "#late-select" && unknown.ExpectedPostcondition.Value == "ready"
	}
	if !postconditionMatches || unknown.OperationOrdinal == 0 {
		return 2, fmt.Errorf("%s recovery record lost postcondition or operation identity: %+v", action, unknown)
	}
	time.Sleep(1350 * time.Millisecond)
	reconciled, reconcileErr := mcpCall(ctx, mcpClient, "reconcile_action", map[string]any{"recovery_record": envelope.RecoveryRecord})
	calls++
	if reconcileErr != nil {
		return 3, fmt.Errorf("%s reconcile: %w", action, reconcileErr)
	}
	var resultPage agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(reconciled), &resultPage); err != nil {
		return 3, fmt.Errorf("decode %s reconciliation: %w", action, err)
	}
	if resultPage.TargetID != page.TargetID || !resultPage.Verified || resultPage.Outcome != string(agent.OutcomePostconditionVerified) {
		return 3, fmt.Errorf("%s late effect did not reconcile on the original target: %+v", action, resultPage)
	}
	return calls, nil
}

func runMCPRecoveryRecord(fixture fixtureScenario) scenarioResult {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	handler, _, completed := benchmarkfixture.HandlerWithLateActionSignals()
	server := httptest.NewServer(handler)
	defer server.Close()
	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "launch remote browser: " + err.Error()}
	}
	defer browser.Close()
	previous, hadPrevious := os.LookupEnv("SCOUT_REMOTE_CDP")
	if err := os.Setenv("SCOUT_REMOTE_CDP", browser.WSEndpoint()); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "configure remote browser: " + err.Error()}
	}
	previousPrivate, hadPrivate := os.LookupEnv("SCOUT_ALLOW_PRIVATE_IPS")
	if err := os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", "1"); err != nil {
		return scenarioResult{ID: fixture.ID, Error: "allow local benchmark fixture: " + err.Error()}
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("SCOUT_REMOTE_CDP", previous)
		} else {
			_ = os.Unsetenv("SCOUT_REMOTE_CDP")
		}
		if hadPrivate {
			_ = os.Setenv("SCOUT_ALLOW_PRIVATE_IPS", previousPrivate)
		} else {
			_ = os.Unsetenv("SCOUT_ALLOW_PRIVATE_IPS")
		}
	}()
	transport, err := client.NewUnsafeStdioTransport("go", "run", "./cmd/scout", "mcp", "serve")
	if err != nil {
		return scenarioResult{ID: fixture.ID, Error: "start MCP transport: " + err.Error()}
	}
	defer transport.Close()
	mcpClient := client.New(transport)
	initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
	defer initCancel()
	if _, err := mcpClient.Initialize(initCtx); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "initialize MCP client: " + err.Error()}
	}
	tools, listToolsErr := mcpClient.ListTools(initCtx)
	if listToolsErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "list MCP tools: " + listToolsErr.Error()}
	}
	for _, toolName := range []string{"assert_text_contains", "component_state", "network_summary", "network_requests", "save_profile"} {
		if !mcpToolHasInputProperty(tools, toolName, "target_id") {
			return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "MCP tool schema omitted target_id: " + toolName}
		}
	}
	timeoutTools := []string{"click_label", "click_handle", "click_text", "dispatch_event", "drag_drop", "double_click", "right_click", "hover", "select_option", "scroll_to", "scroll_by", "focus"}
	for _, toolName := range timeoutTools {
		if !mcpToolHasInputProperty(tools, toolName, "timeout_ms") {
			return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "MCP tool schema omitted timeout_ms: " + toolName}
		}
	}
	started := time.Now()
	if _, traceErr := mcpCall(ctx, mcpClient, "start_trace", map[string]any{}); traceErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "start MCP trace for ordinal contract: " + traceErr.Error()}
	}
	nav, err := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/late-action"})
	if err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: "navigate to fixture: " + err.Error()}
	}
	var navResult agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(nav), &navResult); err != nil || navResult.TargetID == "" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 1, Error: fmt.Sprintf("decode navigation target: %v", err)}
	}
	clickCtx, clickCancel := context.WithTimeout(ctx, 10*time.Second)
	clickRaw, clickRawErr := mcpRawCall(clickCtx, transport, 3, "click", map[string]any{
		"selector": "#late-action", "target_id": navResult.TargetID, "expected_text": "Late completion", "timeout_ms": 300,
	})
	clickCancel()
	if clickRawErr != nil || clickRaw.Result == nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("read raw MCP error response: %v", clickRawErr)}
	}
	clickResult, ok := clickRaw.Result.(map[string]any)
	if !ok {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("raw MCP response has unexpected result type %T", clickRaw.Result)}
	}
	clickBlocks, ok := clickResult["content"].([]any)
	if clickResult["isError"] != true || !ok || len(clickBlocks) == 0 {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("expected MCP isError result with content: %#v", clickResult)}
	}
	clickBlock, ok := clickBlocks[0].(map[string]any)
	if !ok {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "MCP error content block was malformed"}
	}
	clickText, _ := clickBlock["text"].(string)
	wireError, wireErr := inspectMCPErrorResponse(clickRaw)
	if wireErr != nil || wireError.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: fmt.Sprintf("raw MCP response omitted machine-readable error metadata: %v", wireErr)}
	}
	var envelope struct {
		Code           string                      `json:"code"`
		Outcome        string                      `json:"outcome"`
		TargetID       string                      `json:"target_id"`
		RecoveryRecord *agent.ActionRecoveryRecord `json:"recovery_record"`
	}
	clickErrorText := strings.TrimPrefix(clickText, "SCOUT_ERROR ")
	if err := json.Unmarshal([]byte(clickErrorText), &envelope); err != nil || envelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || envelope.Outcome != string(agent.OutcomeUnknown) || envelope.RecoveryRecord == nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, OutputBytes: len(clickText), Error: fmt.Sprintf("MCP error omitted a valid recovery record: decode=%v content=%s", err, clickText)}
	}
	record := *envelope.RecoveryRecord
	if err := record.Validate(); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "invalid MCP recovery record: " + err.Error()}
	}
	unknown := record.ActionOutcome
	if record.SchemaVersion != agent.ActionRecoverySchemaVersion || unknown.RunID == "" || unknown.Step == 0 || unknown.ConfiguredTimeoutMS != 300 || unknown.TargetID != navResult.TargetID || unknown.ExpectedPostcondition == nil || unknown.ExpectedPostcondition.Text != "Late completion" {
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "MCP recovery record did not preserve run, sequence, configured timeout, target, and postcondition"}
	}
	select {
	case <-completed:
	case <-ctx.Done():
		return scenarioResult{ID: fixture.ID, ActionCalls: 2, Error: "late action did not complete: " + ctx.Err().Error()}
	}
	reconcile, reconcileErr := mcpCall(ctx, mcpClient, "reconcile_action", map[string]any{"recovery_record": record})
	if reconcileErr != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "MCP recovery reconciliation: " + reconcileErr.Error()}
	}
	var result agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(reconcile), &result); err != nil {
		return scenarioResult{ID: fixture.ID, ActionCalls: 3, Error: "decode reconciled result: " + err.Error()}
	}
	recoveryLinked := unknown.OperationOrdinal > 0 && result.Receipt != nil && result.Receipt.RecoveryOfOperationOrdinal == unknown.OperationOrdinal
	passed := result.TargetID == navResult.TargetID && result.Outcome == string(agent.OutcomePostconditionVerified) && result.Verified && recoveryLinked
	evidence := fmt.Sprintf("schema_version=%d; run_id=%t; unknown_outcome_sequence=%d; configured_timeout_ms=%d; same_target=%t; postcondition_retained=%t; reconcile=%s; recovery_receipt_linked=%t", record.SchemaVersion, unknown.RunID != "", unknown.Step, unknown.ConfiguredTimeoutMS, result.TargetID == navResult.TargetID, unknown.ExpectedPostcondition != nil, result.Outcome, recoveryLinked)
	out := scenarioResult{ID: fixture.ID, Success: passed, Verified: result.Verified, Outcome: result.Outcome, Target: result.TargetID, ActionCalls: 4, OutputBytes: len(clickText) + len(mcpResultJSON(reconcile)), DurationMicros: time.Since(started).Microseconds(), Evidence: evidence + "; raw_is_error=true; raw_result_meta_code=" + wireError.Code}
	if !passed {
		out.Error = "MCP recovery record did not reconcile the original target's completed postcondition"
	}
	if !passed {
		return out
	}
	typePage, typeNavErr := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/late-type"})
	if typeNavErr != nil {
		out.Success = false
		out.Error = "navigate to late type fixture: " + typeNavErr.Error()
		return out
	}
	var typeNav agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(typePage), &typeNav); err != nil || typeNav.TargetID != navResult.TargetID {
		out.Success = false
		out.Error = fmt.Sprintf("type fixture navigation changed target or failed: %v", err)
		return out
	}
	typeStateURL := server.URL + "/late-type-state"
	typeBefore, typeStateErr := readLateFixtureSignalState(ctx, typeStateURL)
	if typeStateErr != nil {
		out.Success = false
		out.Error = "read late type signal baseline: " + typeStateErr.Error()
		return out
	}
	typeCtx, typeCancel := context.WithTimeout(ctx, 10*time.Second)
	typed, typeErr := mcpClient.CallTool(typeCtx, "type", map[string]any{"selector": "#late-input", "text": "late value", "target_id": typeNav.TargetID, "timeout_ms": 900})
	typeCancel()
	if typeErr != nil || typed == nil || !typed.IsError || len(typed.Content) == 0 {
		out.Success = false
		out.Error = fmt.Sprintf("expected structured MCP type unknown-outcome error: call=%v result=%v", typeErr, typed)
		return out
	}
	typeErrorText := strings.TrimPrefix(typed.Content[0].Text, "SCOUT_ERROR ")
	var typeEnvelope struct {
		Code           string                      `json:"code"`
		Outcome        string                      `json:"outcome"`
		TargetID       string                      `json:"target_id"`
		RecoveryRecord *agent.ActionRecoveryRecord `json:"recovery_record"`
	}
	if err := json.Unmarshal([]byte(typeErrorText), &typeEnvelope); err != nil || typeEnvelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || typeEnvelope.Outcome != string(agent.OutcomeUnknown) || typeEnvelope.RecoveryRecord == nil {
		out.Success = false
		out.Error = fmt.Sprintf("MCP type error omitted a valid recovery record: decode=%v content=%s", err, typed.Content[0].Text)
		return out
	}
	typeRecord := *typeEnvelope.RecoveryRecord
	if err := typeRecord.Validate(); err != nil {
		out.Success = false
		out.Error = "invalid MCP type recovery record: " + err.Error()
		return out
	}
	typeUnknown := typeRecord.ActionOutcome
	if typeUnknown.ConfiguredTimeoutMS != 900 || typeUnknown.TargetID != typeNav.TargetID || typeUnknown.ExpectedPostcondition == nil || typeUnknown.ExpectedPostcondition.ValueSelector != "#late-input" || typeUnknown.ExpectedPostcondition.Value != "late value" {
		out.Success = false
		out.Error = "MCP type recovery record did not preserve timeout, target, and requested field value"
		return out
	}
	if err := waitForLateFixtureSignal(ctx, typeStateURL, typeBefore.StartedCount, false); err != nil {
		out.Success = false
		out.Error = "late type dispatch was not confirmed: " + err.Error()
		return out
	}
	if err := waitForLateFixtureSignal(ctx, typeStateURL, typeBefore.CompletedCount, true); err != nil {
		out.Success = false
		out.Error = "late type effect did not complete: " + err.Error()
		return out
	}
	typeReconcile, typeReconcileErr := mcpCall(ctx, mcpClient, "reconcile_action", map[string]any{"recovery_record": typeRecord})
	if typeReconcileErr != nil {
		out.Success = false
		out.Error = "MCP type recovery reconciliation: " + typeReconcileErr.Error()
		return out
	}
	var typeResult agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(typeReconcile), &typeResult); err != nil {
		out.Success = false
		out.Error = "decode reconciled type result: " + err.Error()
		return out
	}
	typePassed := typeResult.TargetID == typeNav.TargetID && typeResult.Outcome == string(agent.OutcomePostconditionVerified) && typeResult.Verified
	out.Success = passed && typePassed
	out.Verified = out.Success
	out.Outcome = typeResult.Outcome
	out.ActionCalls = 6
	out.OutputBytes += len(typePage.Content[0].Text) + len(typed.Content[0].Text) + len(mcpResultJSON(typeReconcile))
	out.DurationMicros = time.Since(started).Microseconds()
	out.Evidence += fmt.Sprintf("; type_timeout_ms=%d; type_target_retained=%t; type_value_postcondition_retained=%t; type_reconcile=%s", typeUnknown.ConfiguredTimeoutMS, typeResult.TargetID == typeNav.TargetID, typeUnknown.ExpectedPostcondition != nil && typeUnknown.ExpectedPostcondition.Value == "late value", typeResult.Outcome)
	if !typePassed {
		out.Error = "MCP type recovery record did not reconcile the original target's completed field value"
		return out
	}
	submitPage, submitNavErr := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/late-submit"})
	if submitNavErr != nil {
		out.Success = false
		out.Error = "navigate to late submit fixture: " + submitNavErr.Error()
		return out
	}
	var submitNav agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(submitPage), &submitNav); err != nil || submitNav.TargetID != navResult.TargetID {
		out.Success = false
		out.Error = fmt.Sprintf("submit fixture navigation changed target or failed: %v", err)
		return out
	}
	submitStateURL := server.URL + "/late-submit-state"
	submitBefore, submitStateErr := readLateFixtureSignalState(ctx, submitStateURL)
	if submitStateErr != nil {
		out.Success = false
		out.Error = "read late submit signal baseline: " + submitStateErr.Error()
		return out
	}
	submitCtx, submitCancel := context.WithTimeout(ctx, 10*time.Second)
	submitted, submitErr := mcpClient.CallTool(submitCtx, "submit_form", map[string]any{"selector": "#late-form", "target_id": submitNav.TargetID, "action_timeout_ms": 300, "expected_text": "Submission complete"})
	submitCancel()
	if submitErr != nil || submitted == nil || !submitted.IsError || len(submitted.Content) == 0 {
		out.Success = false
		out.Error = fmt.Sprintf("expected structured MCP submit unknown-outcome error: call=%v result=%v", submitErr, submitted)
		return out
	}
	submitErrorText := strings.TrimPrefix(submitted.Content[0].Text, "SCOUT_ERROR ")
	var submitEnvelope struct {
		Code               string                      `json:"code"`
		Outcome            string                      `json:"outcome"`
		TargetID           string                      `json:"target_id"`
		TraceID            string                      `json:"trace_id"`
		TraceStep          int                         `json:"trace_step"`
		TraceActionOrdinal int                         `json:"trace_action_ordinal"`
		RecoveryRecord     *agent.ActionRecoveryRecord `json:"recovery_record"`
	}
	if err := json.Unmarshal([]byte(submitErrorText), &submitEnvelope); err != nil || submitEnvelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || submitEnvelope.Outcome != string(agent.OutcomeUnknown) || submitEnvelope.RecoveryRecord == nil {
		out.Success = false
		out.Error = fmt.Sprintf("MCP submit error omitted a valid recovery record: decode=%v content=%s", err, submitted.Content[0].Text)
		return out
	}
	submitRecord := *submitEnvelope.RecoveryRecord
	if err := submitRecord.Validate(); err != nil {
		out.Success = false
		out.Error = "invalid MCP submit recovery record: " + err.Error()
		return out
	}
	submitUnknown := submitRecord.ActionOutcome
	if submitUnknown.ConfiguredTimeoutMS != 300 || submitUnknown.TargetID != submitNav.TargetID || submitUnknown.ExpectedPostcondition == nil || submitUnknown.ExpectedPostcondition.Text != "Submission complete" || submitEnvelope.TraceID == "" || submitEnvelope.TraceStep <= 0 || submitEnvelope.TraceActionOrdinal <= 0 || submitUnknown.TraceActionOrdinal != submitEnvelope.TraceActionOrdinal {
		out.Success = false
		out.Error = "MCP submit recovery record did not preserve timeout, target, postcondition, and trace action ordinal"
		return out
	}
	tracePath := filepath.Join(os.TempDir(), fmt.Sprintf("scout-trace-ordinal-%d.zip", time.Now().UnixNano()))
	traceRaw, traceErr := mcpCall(ctx, mcpClient, "stop_trace", map[string]any{"path": tracePath})
	var traceResult agent.TraceResult
	if traceErr == nil {
		traceErr = json.Unmarshal(mcpResultJSON(traceRaw), &traceResult)
	}
	if traceErr != nil {
		out.Success = false
		out.Error = "stop MCP trace for ordinal contract: " + traceErr.Error()
		return out
	}
	traceArchive, traceErr := zip.OpenReader(tracePath)
	if traceErr != nil {
		out.Success = false
		out.Error = "open MCP trace artifact: " + traceErr.Error()
		return out
	}
	var traceOrdinalMatch bool
	for _, file := range traceArchive.File {
		if file.Name != "trace.json" {
			continue
		}
		reader, openErr := file.Open()
		if openErr != nil {
			traceErr = openErr
			break
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			traceErr = readErr
			break
		}
		if closeErr != nil {
			traceErr = closeErr
			break
		}
		var events []agent.TraceEvent
		if decodeErr := json.Unmarshal(data, &events); decodeErr != nil {
			traceErr = decodeErr
			break
		}
		for _, event := range events {
			if event.Action == "submit_form" && event.TargetID == submitNav.TargetID && event.ActionOrdinal == submitEnvelope.TraceActionOrdinal && event.Index+1 == submitEnvelope.TraceStep {
				traceOrdinalMatch = true
			}
		}
	}
	_ = traceArchive.Close()
	_ = os.Remove(tracePath)
	if traceErr != nil || !traceOrdinalMatch {
		out.Success = false
		out.Error = fmt.Sprintf("MCP trace event did not match recovery ordinal: matched=%t error=%v", traceOrdinalMatch, traceErr)
		return out
	}
	if err := waitForLateFixtureSignal(ctx, submitStateURL, submitBefore.StartedCount, false); err != nil {
		out.Success = false
		out.Error = "late submit dispatch was not confirmed: " + err.Error()
		return out
	}
	if err := waitForLateFixtureSignal(ctx, submitStateURL, submitBefore.CompletedCount, true); err != nil {
		out.Success = false
		out.Error = "late submit effect did not complete: " + err.Error()
		return out
	}
	submitReconcile, submitReconcileErr := mcpCall(ctx, mcpClient, "reconcile_action", map[string]any{"recovery_record": submitRecord})
	if submitReconcileErr != nil {
		out.Success = false
		out.Error = "MCP submit recovery reconciliation: " + submitReconcileErr.Error()
		return out
	}
	var submitResult agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(submitReconcile), &submitResult); err != nil {
		out.Success = false
		out.Error = "decode reconciled submit result: " + err.Error()
		return out
	}
	submitPassed := submitResult.TargetID == submitNav.TargetID && submitResult.Outcome == string(agent.OutcomePostconditionVerified) && submitResult.Verified
	if !submitPassed {
		out.Success = false
		out.Verified = false
		out.Error = "MCP submit recovery record did not reconcile the original target's completed page condition"
		return out
	}
	filePath := filepath.Join(os.TempDir(), fmt.Sprintf("scout-late-upload-%d.txt", time.Now().UnixNano()))
	if err := os.WriteFile(filePath, []byte("scout upload recovery fixture\n"), 0o600); err != nil {
		out.Success = false
		out.Error = "create upload fixture file: " + err.Error()
		return out
	}
	defer os.Remove(filePath)
	uploadPage, uploadNavErr := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/late-upload"})
	if uploadNavErr != nil {
		out.Success = false
		out.Error = "navigate to late upload fixture: " + uploadNavErr.Error()
		return out
	}
	var uploadNav agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(uploadPage), &uploadNav); err != nil || uploadNav.TargetID != navResult.TargetID {
		out.Success = false
		out.Error = fmt.Sprintf("upload fixture navigation changed target or failed: %v", err)
		return out
	}
	uploadCtx, uploadCancel := context.WithTimeout(ctx, 10*time.Second)
	uploaded, uploadErr := mcpClient.CallTool(uploadCtx, "upload_file", map[string]any{
		"selector": "#late-file", "file_path": filePath, "target_id": uploadNav.TargetID, "timeout_ms": 300,
	})
	uploadCancel()
	if uploadErr != nil || uploaded == nil || !uploaded.IsError || len(uploaded.Content) == 0 {
		out.Success = false
		out.Error = fmt.Sprintf("expected upload_file unknown-outcome error: call=%v result=%v", uploadErr, uploaded)
		return out
	}
	var uploadEnvelope struct {
		Code           string                      `json:"code"`
		Outcome        string                      `json:"outcome"`
		RecoveryRecord *agent.ActionRecoveryRecord `json:"recovery_record"`
	}
	uploadText := strings.TrimPrefix(uploaded.Content[0].Text, "SCOUT_ERROR ")
	if err := json.Unmarshal([]byte(uploadText), &uploadEnvelope); err != nil || uploadEnvelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || uploadEnvelope.Outcome != string(agent.OutcomeUnknown) || uploadEnvelope.RecoveryRecord == nil {
		out.Success = false
		out.Error = fmt.Sprintf("upload_file error omitted recovery record: decode=%v content=%s", err, uploaded.Content[0].Text)
		return out
	}
	uploadRecord := *uploadEnvelope.RecoveryRecord
	if err := uploadRecord.Validate(); err != nil {
		out.Success = false
		out.Error = "invalid upload_file recovery record: " + err.Error()
		return out
	}
	if uploadRecord.ActionOutcome.Action != "upload_file" || uploadRecord.ActionOutcome.OperationOrdinal == 0 || uploadRecord.ActionOutcome.TargetID != uploadNav.TargetID || uploadRecord.ActionOutcome.ExpectedPostcondition == nil || uploadRecord.ActionOutcome.ExpectedPostcondition.ValueSelector != "#late-file" || uploadRecord.ActionOutcome.ExpectedPostcondition.Value != `C:\fakepath\`+filepath.Base(filePath) {
		out.Success = false
		out.Error = "upload_file recovery record omitted expected file value, target, or operation identity"
		return out
	}
	time.Sleep(1400 * time.Millisecond)
	uploadReconciled, uploadReconcileErr := mcpCall(ctx, mcpClient, "reconcile_action", map[string]any{"recovery_record": uploadRecord})
	if uploadReconcileErr != nil {
		out.Success = false
		out.Error = "upload_file recovery reconciliation: " + uploadReconcileErr.Error()
		return out
	}
	var uploadResult agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(uploadReconciled), &uploadResult); err != nil || uploadResult.TargetID != uploadNav.TargetID || uploadResult.Outcome != string(agent.OutcomePostconditionVerified) || !uploadResult.Verified {
		out.Success = false
		out.Error = fmt.Sprintf("upload_file late selection did not reconcile on original target: decode=%v result=%s", err, mcpResultJSON(uploadReconciled))
		return out
	}
	out.Evidence += fmt.Sprintf("; upload_timeout_ms=300; upload_target_retained=true; upload_value=%t; upload_reconcile=%s", uploadRecord.ActionOutcome.ExpectedPostcondition.Value == `C:\fakepath\`+filepath.Base(filePath), uploadResult.Outcome)
	out.ActionCalls += 4
	out.OutputBytes += len(uploaded.Content[0].Text) + len(mcpResultJSON(uploadReconciled)) + len(mcpResultJSON(uploadPage))
	out.DurationMicros = time.Since(started).Microseconds()
	batchPage, batchNavErr := mcpCall(ctx, mcpClient, "navigate", map[string]any{"url": server.URL + "/late-batch"})
	if batchNavErr != nil {
		out.Success = false
		out.Error = "navigate to late batch fixture: " + batchNavErr.Error()
		return out
	}
	var batchNav agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(batchPage), &batchNav); err != nil || batchNav.TargetID != navResult.TargetID {
		out.Success = false
		out.Error = fmt.Sprintf("batch fixture navigation changed target or failed: %v", err)
		return out
	}
	batchCallCtx, batchCallCancel := context.WithTimeout(ctx, 10*time.Second)
	batchResult, batchCallErr := mcpClient.CallTool(batchCallCtx, "batch", map[string]any{
		"target_id": batchNav.TargetID, "timeout_ms": 600,
		"actions": []agent.BatchAction{
			{Action: "click", Selector: "#batch-first", ExpectedText: "First completed"},
			{Action: "click", Selector: "#batch-late", ExpectedText: "Batch late completion"},
		},
	})
	batchCallCancel()
	if batchCallErr != nil || batchResult == nil || !batchResult.IsError || len(batchResult.Content) == 0 {
		out.Success = false
		out.Error = fmt.Sprintf("expected interrupted batch with partial results: call=%v result=%v", batchCallErr, batchResult)
		return out
	}
	var batchEnvelope struct {
		Code          string `json:"code"`
		PartialResult struct {
			Total   int                       `json:"total"`
			Results []agent.BatchActionResult `json:"results"`
		} `json:"partial_result"`
	}
	batchText := strings.TrimPrefix(batchResult.Content[0].Text, "SCOUT_ERROR ")
	if err := json.Unmarshal([]byte(batchText), &batchEnvelope); err != nil || batchEnvelope.Code != "SCOUT_OPERATION_INTERRUPTED" || batchEnvelope.PartialResult.Total != 2 || len(batchEnvelope.PartialResult.Results) != 2 {
		out.Success = false
		out.Error = fmt.Sprintf("batch interruption omitted partial actions: decode=%v content=%s", err, batchResult.Content[0].Text)
		return out
	}
	lateBatchStep := batchEnvelope.PartialResult.Results[1]
	var lateBatchOutcome *agent.ActionOutcomeError
	if lateBatchStep.RecoveryRecord != nil {
		lateBatchOutcome = &lateBatchStep.RecoveryRecord.ActionOutcome
	} else if lateBatchStep.Receipt != nil {
		lateBatchOutcome = &agent.ActionOutcomeError{
			RunID: lateBatchStep.Receipt.RunID, OperationOrdinal: lateBatchStep.Receipt.OperationOrdinal,
			Action: lateBatchStep.Receipt.Action, Target: lateBatchStep.Receipt.Target, TargetID: lateBatchStep.Receipt.TargetID,
			FrameID: lateBatchStep.Receipt.FrameID, Outcome: agent.OutcomeUnknown,
			ExpectedPostcondition: lateBatchStep.Receipt.Postcondition, Receipt: lateBatchStep.Receipt,
		}
	}
	if lateBatchStep.Outcome != string(agent.OutcomeUnknown) || lateBatchStep.TargetID != batchNav.TargetID || lateBatchOutcome == nil || lateBatchOutcome.ExpectedPostcondition == nil || lateBatchOutcome.ExpectedPostcondition.Text != "Batch late completion" {
		out.Success = false
		out.Error = fmt.Sprintf("uncertain batch step omitted its target or postcondition: %+v", lateBatchStep)
		return out
	}
	var batchRecovery agent.ActionRecoveryRecord
	if lateBatchStep.RecoveryRecord != nil {
		batchRecovery = *lateBatchStep.RecoveryRecord
	} else {
		var recordErr error
		batchRecovery, recordErr = agent.NewActionRecoveryRecord(lateBatchOutcome)
		if recordErr != nil {
			out.Success = false
			out.Error = "build recovery record from partial batch receipt: " + recordErr.Error()
			return out
		}
	}
	time.Sleep(1400 * time.Millisecond)
	batchReconciled, batchReconcileErr := mcpCall(ctx, mcpClient, "reconcile_action", map[string]any{"recovery_record": batchRecovery})
	if batchReconcileErr != nil {
		out.Success = false
		out.Error = "partial batch recovery reconciliation: " + batchReconcileErr.Error()
		return out
	}
	var batchRecoveryResult agent.PageResult
	if err := json.Unmarshal(mcpResultJSON(batchReconciled), &batchRecoveryResult); err != nil || batchRecoveryResult.TargetID != batchNav.TargetID || batchRecoveryResult.Outcome != string(agent.OutcomePostconditionVerified) || !batchRecoveryResult.Verified {
		out.Success = false
		out.Error = fmt.Sprintf("batch late effect did not reconcile on original target: decode=%v result=%s", err, mcpResultJSON(batchReconciled))
		return out
	}
	out.Evidence += fmt.Sprintf("; batch_partial_steps=%d; batch_uncertain_target_retained=true; batch_postcondition_retained=true; batch_reconcile=%s", len(batchEnvelope.PartialResult.Results), batchRecoveryResult.Outcome)
	out.ActionCalls += 4
	out.OutputBytes += len(batchResult.Content[0].Text) + len(mcpResultJSON(batchReconciled)) + len(mcpResultJSON(batchPage))
	out.DurationMicros = time.Since(started).Microseconds()
	lateCases := []struct {
		action string
		path   string
	}{
		{"click_label", "/late-label"},
		{"click_handle", "/late-handle"},
		{"click_text", "/late-text"},
		{"dispatch_event", "/late-event"},
		{"drag_drop", "/late-drag"},
		{"select_option", "/late-select"},
		{"hover", "/late-hover"},
	}
	lateActionCalls := 0
	lateEvidence := make([]string, 0, len(lateCases))
	for _, testCase := range lateCases {
		calls, lateErr := runMCPLateMutationCase(ctx, mcpClient, server.URL, testCase.action, testCase.path)
		lateActionCalls += calls
		if lateErr != nil {
			out.Success = false
			out.Verified = false
			out.Error = fmt.Sprintf("%s late-effect recovery: %v", testCase.action, lateErr)
			return out
		}
		lateEvidence = append(lateEvidence, testCase.action+"=outcome_unknown→postcondition_verified")
	}
	guardRaw, guardErr := mcpCall(ctx, mcpClient, "open_tab", map[string]any{"name": "stale-read-guard", "url": server.URL + "/foreground-tab", "target_id": submitNav.TargetID})
	var guardPage agent.PageResult
	if guardErr == nil {
		guardErr = json.Unmarshal(mcpResultJSON(guardRaw), &guardPage)
	}
	if guardErr != nil || guardPage.TargetID == "" || guardPage.TargetID == submitNav.TargetID {
		out.Success = false
		out.Error = fmt.Sprintf("open stale-read guard target: result=%s error=%v", mcpResultJSON(guardRaw), guardErr)
		return out
	}
	staleCalls := []struct {
		name string
		args map[string]any
	}{
		{"assert_text_contains", map[string]any{"text": "Foreground untouched", "target_id": submitNav.TargetID}},
		{"component_state", map[string]any{"selector": "body", "target_id": submitNav.TargetID}},
		{"network_summary", map[string]any{"target_id": submitNav.TargetID}},
		{"network_requests", map[string]any{"target_id": submitNav.TargetID}},
		{"save_profile", map[string]any{"path": filepath.Join(os.TempDir(), fmt.Sprintf("scout-stale-profile-%d.json", time.Now().UnixNano())), "target_id": submitNav.TargetID}},
	}
	staleRejected := 0
	staleOutputBytes := 0
	for _, call := range staleCalls {
		_, staleErr := mcpCall(ctx, mcpClient, call.name, call.args)
		code, cause, _ := mcpErrorEnvelope(staleErr)
		if code == "SCOUT_OPERATION_ERROR" && cause == "stale_target" {
			staleRejected++
		}
		staleOutputBytes += len(fmt.Sprint(staleErr))
	}
	activeRaw, activeErr := mcpCall(ctx, mcpClient, "status", map[string]any{})
	var activeStatus agent.SessionStatus
	if activeErr == nil {
		activeErr = json.Unmarshal(mcpResultJSON(activeRaw), &activeStatus)
	}
	guardRemainedActive := activeErr == nil && activeStatus.ActiveTargetID == guardPage.TargetID
	out.Success = passed && typePassed && submitPassed && staleRejected == len(staleCalls) && guardRemainedActive && len(lateEvidence) == len(lateCases)
	out.Verified = out.Success
	out.Outcome = submitResult.Outcome
	out.ActionCalls = 18 + lateActionCalls
	out.OutputBytes += len(submitPage.Content[0].Text) + len(submitted.Content[0].Text) + len(mcpResultJSON(submitReconcile)) + len(mcpResultJSON(guardRaw)) + staleOutputBytes + len(mcpResultJSON(activeRaw))
	out.DurationMicros = time.Since(started).Microseconds()
	out.Evidence += fmt.Sprintf("; submit_action_timeout_ms=%d; submit_target_retained=%t; submit_postcondition_retained=%t; submit_reconcile=%s; trace_id=%s; trace_step=%d; trace_action_ordinal=%d; trace_event_match=%t; target_id_schema_fields=5; per_action_timeout_schema_fields=%d/%d; late_effect_recoveries=%s; stale_read_calls_rejected=%d/%d; guard_target_remained_active=%t", submitUnknown.ConfiguredTimeoutMS, submitResult.TargetID == submitNav.TargetID, submitUnknown.ExpectedPostcondition != nil && submitUnknown.ExpectedPostcondition.Text == "Submission complete", submitResult.Outcome, submitEnvelope.TraceID, submitEnvelope.TraceStep, submitEnvelope.TraceActionOrdinal, traceOrdinalMatch, len(timeoutTools), len(timeoutTools), strings.Join(lateEvidence, ","), staleRejected, len(staleCalls), guardRemainedActive)
	if !submitPassed {
		out.Error = "MCP submit recovery record did not reconcile the original target's completed page condition"
	} else if staleRejected != len(staleCalls) || !guardRemainedActive {
		out.Error = fmt.Sprintf("stale target guards failed: rejected=%d/%d; guard remained active=%t; error=%v", staleRejected, len(staleCalls), guardRemainedActive, activeErr)
	}
	return out
}
