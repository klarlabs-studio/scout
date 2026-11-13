// Command capture creates deterministic recovery receipt fixtures and exports
// them through Scout's Go, stateful CLI, and MCP stdio surfaces.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.klarlabs.de/mcp/client"
	"go.klarlabs.de/scout/agent"
)

const (
	taskID  = "parity-fixture-task"
	target  = "parity-fixture-target"
	browser = "parity-fixture-browser"
)

const fixturePolicyID = "scout-origin-v1:e40b288ff0dc289fc18ac3da6b83f289831f3b2b77e2df256a3bdf3e160c187f"

func main() {
	out := flag.String("out", "", "capture output directory")
	scoutBin := flag.String("scout", "", "Scout binary (builds one when omitted)")
	flag.Parse()
	if *out == "" {
		fatal("-out is required")
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		fatal("create output: %v", err)
	}
	bin := *scoutBin
	if bin == "" {
		bin = filepath.Join(*out, "scout")
		root := repoRoot()
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/scout")
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			fatal("build scout: %v: %s", err, output)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, scenario := range []string{"interrupted", "completed", "non_first_step"} {
		id := taskID + "-" + scenario
		record := fixture(id)
		goPath := filepath.Join(*out, scenario+".go.json")
		cliPath := filepath.Join(*out, scenario+".cli.json")
		mcpPath := filepath.Join(*out, scenario+".mcp.json")
		if err := seed(filepath.Join(*out, scenario+".go.db"), record); err != nil {
			fatal("seed Go store: %v", err)
		}
		storePath := filepath.Join(*out, scenario+".cli.db")
		if err := seed(storePath, record); err != nil {
			fatal("seed CLI store: %v", err)
		}
		mcpStorePath := filepath.Join(*out, scenario+".mcp.db")
		if err := seedMCP(mcpStorePath, record); err != nil {
			fatal("seed MCP store: %v", err)
		}
		ordinal := 1
		if scenario == "non_first_step" {
			ordinal = 2
		}
		if err := exportGo(ctx, filepath.Join(*out, scenario+".go.db"), id, ordinal, goPath); err != nil {
			fatal("Go export: %v", err)
		}
		if err := exportCLI(ctx, bin, storePath, id, ordinal, cliPath); err != nil {
			fatal("CLI export: %v", err)
		}
		if err := exportMCP(ctx, bin, mcpStorePath, id, ordinal, mcpPath); err != nil {
			fatal("MCP export: %v", err)
		}
		fmt.Printf("captured %s receipts to %s\n", scenario, *out)
	}
}

func fixture(id string) agent.TaskRunRecord {
	plan := &agent.TaskRunPlan{SchemaVersion: 1, TargetID: target, PolicyID: fixturePolicyID, PolicyEnforcement: "session_page_policy", Actions: []agent.TaskRunRecoveryStep{{Action: "click", Selector: "#first"}, {Action: "click", Selector: "#second"}}}
	first := receipt(id, 1, "#first", "trace-first")
	second := receipt(id, 2, "#second", "trace-second")
	record := agent.TaskRunRecord{
		ID: id, Fingerprint: hash("fixture-plan:" + id), InFlight: false, TargetID: target, BrowserInstanceID: browser, Plan: plan,
		Result:   &agent.TaskRunResult{ID: id, Status: "completed", TargetID: target, BrowserInstanceID: browser, PolicyID: fixturePolicyID, PolicyEnforcement: "session_page_policy", PlanID: hash("fixture-plan:" + id), Batch: &agent.BatchResult{Total: 2, Succeeded: 2, Verified: 2, NextActionIndex: 2, Results: []agent.BatchActionResult{{Index: 0, Action: "click", Success: true, Verified: true, Outcome: string(agent.OutcomePostconditionVerified), Receipt: &first}, {Index: 1, Action: "click", Success: true, Verified: true, Outcome: string(agent.OutcomePostconditionVerified), Receipt: &second}}}},
		Recovery: &agent.TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: plan.Actions, NextAction: 2},
	}
	if strings.Contains(id, "interrupted") {
		record.InFlight = true
		record.Result = nil
		record.Recovery = &agent.TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: plan.Actions, Current: &agent.TaskRunRecoveryCursor{Index: 0, Status: "started"}, NextAction: 0}
	}
	return record
}

func receipt(id string, ordinal int, selector, trace string) agent.ActionReceipt {
	frame := ""
	if ordinal > 1 {
		frame = "frame-fixture"
	}
	return agent.ActionReceipt{SchemaVersion: 1, RunID: "fixture-run", OperationOrdinal: ordinal, PolicyID: fixturePolicyID, Action: "click", BrowserOwnership: "owned", Target: "https://example.test/", TargetID: target, BrowserInstanceID: browser, FrameID: frame, Outcome: agent.OutcomePostconditionVerified, Verified: true, Verification: agent.VerificationVerified, Evidence: "fixture parity evidence", StartedAt: 1794420000000, DurationMS: 12, TraceID: trace, Postcondition: &agent.Postcondition{VisibleSelector: selector}}
}

func seed(path string, record agent.TaskRunRecord) error {
	store, err := agent.OpenBoltTaskRunStore(path)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err = store.ReserveTaskRun(ctx, agent.TaskRunRecord{ID: record.ID, Fingerprint: record.Fingerprint, InFlight: true}); err != nil {
		return err
	}
	if err = store.BindTaskRunHandoff(ctx, record.ID, record.Fingerprint, record.TargetID, record.BrowserInstanceID, *record.Plan, agent.TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: record.Plan.Actions, NextAction: 0}); err != nil {
		return err
	}
	if record.InFlight {
		return store.UpdateTaskRunRecovery(ctx, record.ID, record.Fingerprint, *record.Recovery)
	}
	return store.CompleteTaskRun(ctx, record)
}

func seedMCP(path string, record agent.TaskRunRecord) error {
	store, err := agent.OpenBoltTaskRunStore(path)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	namespace := "stdio/session/default"
	scoped := scope{store: store, namespace: namespace}
	storageID := scoped.storageID(record.ID)
	if _, _, err = store.ReserveTaskRun(ctx, agent.TaskRunRecord{ID: storageID, Fingerprint: record.Fingerprint, InFlight: true}); err != nil {
		return err
	}
	if err = store.BindTaskRunHandoff(ctx, storageID, record.Fingerprint, record.TargetID, record.BrowserInstanceID, *record.Plan, agent.TaskRunRecovery{SchemaVersion: 1, Phase: "actions", Steps: record.Plan.Actions, NextAction: 0}); err != nil {
		return err
	}
	record.ID = storageID
	if record.Result != nil {
		record.Result.ID = storageID
		record.Result.Batch.Results[0].Receipt.RunID = "fixture-run"
		record.Result.Batch.Results[1].Receipt.RunID = "fixture-run"
	}
	if record.InFlight {
		return store.UpdateTaskRunRecovery(ctx, storageID, record.Fingerprint, *record.Recovery)
	}
	return store.CompleteTaskRun(ctx, record)
}

type scope struct {
	store     *agent.BoltTaskRunStore
	namespace string
}

func (s scope) storageID(id string) string {
	sum := sha256.Sum256([]byte(s.namespace))
	return "scope:" + hex.EncodeToString(sum[:]) + ":" + id
}

func exportGo(ctx context.Context, path, id string, ordinal int, out string) error {
	store, err := agent.OpenBoltTaskRunStore(path)
	if err != nil {
		return err
	}
	defer store.Close()
	s := agent.NewSessionFromBrowser(nil, agent.SessionConfig{TaskRunStore: store})
	r, err := s.ExportTaskRunRecoveryReceiptContext(ctx, id, ordinal)
	if err != nil {
		return err
	}
	return write(out, r)
}
func exportCLI(ctx context.Context, bin, store, id string, ordinal int, out string) error {
	cmd := exec.CommandContext(ctx, bin, "session", "--task-store="+store)
	cmd.Stdin = bytes.NewReader([]byte(fmt.Sprintf("{\"command\":\"export_task_recovery_receipt\",\"task_id\":%q,\"operation_ordinal\":%d}\n", id, ordinal)))
	var stdout, stderr captureBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	var response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("CLI export failed: %s", response.Error)
	}
	return writeRaw(out, response.Result)
}
func exportMCP(ctx context.Context, bin, store, id string, ordinal int, out string) error {
	transport, err := client.NewStdioTransport(bin, "mcp", "serve", "--task-store="+store, "--otel=false")
	if err != nil {
		return err
	}
	c := client.New(transport)
	defer c.Close()
	if _, err = c.Initialize(ctx); err != nil {
		return err
	}
	result, err := c.CallTool(ctx, "export_task_recovery_receipt", map[string]any{"id": id, "operation_ordinal": ordinal})
	if err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("MCP returned tool error: %+v", result.Content)
	}
	if len(result.StructuredContent) > 0 {
		return writeRaw(out, result.StructuredContent)
	}
	for _, item := range result.Content {
		if item.Type == "text" {
			return writeRaw(out, []byte(item.Text))
		}
	}
	return fmt.Errorf("MCP response had no receipt")
}

func write(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
func writeRaw(path string, data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	return write(path, v)
}
func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../"))
}
func fatal(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }

type captureBuffer struct{ data []byte }

func (b *captureBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *captureBuffer) Bytes() []byte  { return b.data }
func (b *captureBuffer) String() string { return string(b.data) }
