// Command compare_recovery_receipts audits receipt exports from Scout's Go SDK,
// stateful JSONL CLI, and MCP surfaces. It applies both the public Go decoder
// and the independent Python verifier, then reports normalized field parity
// and owner/session namespace differences for every supplied scenario.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"go.klarlabs.de/scout/agent"
)

var surfaces = []string{"go", "cli", "mcp"}

type manifest struct {
	RequiredScenarios []string    `json:"required_scenarios,omitempty"`
	Cases             []caseInput `json:"cases"`
}

type caseInput struct {
	Name              string            `json:"name"`
	Scenario          string            `json:"scenario"`
	FrameIDComparison string            `json:"frame_id_comparison,omitempty"`
	Receipts          map[string]string `json:"receipts"`
	Namespaces        map[string]string `json:"namespaces"`
}

type normalizedReceipt struct {
	Schema        string                              `json:"schema"`
	Task          agent.RecoveryReceiptTask           `json:"task"`
	Runtime       agent.RecoveryReceiptRuntime        `json:"runtime"`
	Browser       agent.RecoveryReceiptBrowser        `json:"browser"`
	Target        agent.RecoveryReceiptTarget         `json:"target"`
	Policy        *agent.RecoveryReceiptPolicy        `json:"policy,omitempty"`
	Operation     agent.RecoveryReceiptOperation      `json:"operation"`
	Postcondition *agent.RecoveryReceiptPostcondition `json:"postcondition,omitempty"`
	EvidenceTrust string                              `json:"evidence_trust"`
	EvidenceRefs  []string                            `json:"evidence_references"`
	ReplayAllowed bool                                `json:"replay_authorized"`
	NextStep      string                              `json:"next_step"`
}

type surfaceResult struct {
	Namespace string             `json:"namespace,omitempty"`
	GoValid   bool               `json:"go_decoder_valid"`
	Python    map[string]any     `json:"python_verifier,omitempty"`
	Error     string             `json:"error,omitempty"`
	Receipt   *normalizedReceipt `json:"normalized_receipt,omitempty"`
}

type caseResult struct {
	Name                 string                   `json:"name"`
	Scenario             string                   `json:"scenario"`
	FrameIDComparison    string                   `json:"frame_id_comparison,omitempty"`
	Passed               bool                     `json:"passed"`
	TaskID               string                   `json:"task_id,omitempty"`
	OperationOrdinal     int                      `json:"operation_ordinal,omitempty"`
	FieldsCompared       []string                 `json:"fields_compared,omitempty"`
	FieldParity          map[string]bool          `json:"field_parity"`
	MismatchedFields     map[string][]string      `json:"mismatched_fields,omitempty"`
	Namespaces           map[string]string        `json:"namespaces"`
	NamespaceDifferences []string                 `json:"namespace_differences,omitempty"`
	Surfaces             map[string]surfaceResult `json:"surfaces"`
}

func main() {
	manifestPath := flag.String("manifest", "", "JSON manifest with one or more Go/CLI/MCP receipt triplets")
	pythonPath := flag.String("python-verifier", "", "path to verify_recovery_receipt.py (defaults to the repository verifier)")
	flag.Parse()
	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "usage: compare_recovery_receipts -manifest parity.json [-python-verifier verify_recovery_receipt.py]")
		os.Exit(2)
	}
	if *pythonPath == "" {
		_, source, _, ok := runtime.Caller(0)
		if !ok {
			fail("cannot locate the independent Python verifier; pass -python-verifier")
		}
		*pythonPath = filepath.Join(filepath.Dir(source), "..", "verify_recovery_receipt.py")
	}

	data, err := os.ReadFile(*manifestPath)
	if err != nil {
		fail("read manifest: %v", err)
	}
	var input manifest
	if err := json.Unmarshal(data, &input); err != nil {
		fail("decode manifest: %v", err)
	}
	if len(input.Cases) == 0 {
		fail("manifest must contain at least one case")
	}

	results := make([]caseResult, 0, len(input.Cases))
	allPassed := true
	for _, item := range input.Cases {
		result := auditCase(item, *pythonPath)
		results = append(results, result)
		allPassed = allPassed && result.Passed
	}
	covered := passedScenarios(results)
	requiredScenarios := input.RequiredScenarios
	if len(requiredScenarios) == 0 {
		requiredScenarios = []string{"interrupted", "completed", "non_first_step"}
	}
	missing := make([]string, 0)
	for _, scenario := range requiredScenarios {
		if !covered[scenario] {
			missing = append(missing, scenario)
		}
	}
	if len(missing) > 0 {
		allPassed = false
	}
	report := map[string]any{
		"schema":                                 "scout.recovery-receipt-parity/v1",
		"passed":                                 allPassed,
		"required_scenarios":                     requiredScenarios,
		"covered_scenarios":                      sortedKeys(covered),
		"missing_scenarios":                      missing,
		"cases":                                  results,
		"producer_authenticated":                 false,
		"browser_binding_independently_observed": false,
		"application_outcome_independently_observed": false,
		"normalized_dynamic_fields":                  []string{"evidence.created_at", "evidence.observed_at", "operation.deadline_at_ms"},
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if !allPassed {
		os.Exit(1)
	}
}

func passedScenarios(results []caseResult) map[string]bool {
	covered := map[string]bool{}
	required := map[string]bool{"interrupted": true, "completed": true, "non_first_step": true}
	for _, result := range results {
		if result.Passed && required[result.Scenario] {
			covered[result.Scenario] = true
		}
	}
	return covered
}

func auditCase(input caseInput, pythonPath string) caseResult {
	result := caseResult{
		Name: input.Name, Scenario: input.Scenario, FrameIDComparison: input.FrameIDComparison, Passed: true,
		FieldParity: make(map[string]bool), Namespaces: make(map[string]string),
		Surfaces: make(map[string]surfaceResult),
	}
	if result.Name == "" {
		result.Name = input.Scenario
	}
	if result.Scenario != "interrupted" && result.Scenario != "completed" && result.Scenario != "non_first_step" {
		result.Passed = false
		result.MismatchedFields = map[string][]string{"scenario": {"must be interrupted, completed, or non_first_step"}}
	}
	if input.FrameIDComparison != "" && input.FrameIDComparison != "exact" && input.FrameIDComparison != "presence" {
		result.Passed = false
		result.MismatchedFields = appendField(result.MismatchedFields, "frame_id_comparison", "must be exact or presence")
	}
	var receipts = make(map[string]normalizedReceipt, len(surfaces))
	for _, surface := range surfaces {
		path := input.Receipts[surface]
		sr := surfaceResult{Namespace: input.Namespaces[surface]}
		if path == "" {
			sr.Error = "receipt path is required"
			result.Passed = false
			result.Surfaces[surface] = sr
			continue
		}
		if sr.Namespace == "" {
			sr.Error = "namespace label is required to document record scope"
			result.Passed = false
		}
		result.Namespaces[surface] = sr.Namespace
		data, err := os.ReadFile(path)
		if err == nil {
			var receipt agent.RecoveryReceipt
			receipt, err = agent.DecodeRecoveryReceipt(data)
			if err == nil {
				sr.GoValid = true
				normalized := normalize(receipt)
				sr.Receipt = &normalized
				receipts[surface] = normalized
			}
		}
		if err != nil {
			sr.Error = appendError(sr.Error, "Go decoder: "+err.Error())
			result.Passed = false
		} else {
			py, pyErr := runPythonVerifier(pythonPath, path)
			if pyErr != nil {
				sr.Error = appendError(sr.Error, "Python verifier: "+pyErr.Error())
				result.Passed = false
			} else {
				sr.Python = py
				if sr.Receipt != nil {
					if identityErr := validatePythonResult(py, *sr.Receipt); identityErr != nil {
						sr.Error = appendError(sr.Error, "Python verifier: "+identityErr.Error())
						result.Passed = false
					}
				}
			}
			if sr.Receipt != nil {
				if scenarioErr := validateScenario(input, *sr.Receipt); scenarioErr != nil {
					sr.Error = appendError(sr.Error, scenarioErr.Error())
					result.Passed = false
				}
			}
		}
		result.Surfaces[surface] = sr
	}
	for i, surface := range surfaces {
		for _, other := range surfaces[i+1:] {
			if result.Namespaces[surface] != result.Namespaces[other] {
				result.NamespaceDifferences = append(result.NamespaceDifferences, fmt.Sprintf("%s=%q; %s=%q", surface, result.Namespaces[surface], other, result.Namespaces[other]))
			}
		}
	}
	if len(receipts) != len(surfaces) {
		result.Passed = false
		return result
	}
	if input.FrameIDComparison == "presence" {
		for surface, receipt := range receipts {
			if receipt.Target.FrameID == "" {
				result.Passed = false
				result.MismatchedFields = appendField(result.MismatchedFields, surface, "target.frame_id is required when comparing frame presence")
			} else {
				receipt.Target.FrameID = "<frame-bound>"
				receipts[surface] = receipt
				surfaceResult := result.Surfaces[surface]
				surfaceResult.Receipt = &receipt
				result.Surfaces[surface] = surfaceResult
			}
		}
	}
	base := receipts["go"]
	result.TaskID = base.Task.ID
	result.OperationOrdinal = base.Operation.Ordinal
	result.FieldsCompared = normalizedFieldNames(base)
	baseJSON, _ := json.Marshal(base)
	for _, surface := range surfaces {
		candidateJSON, _ := json.Marshal(receipts[surface])
		result.FieldParity[surface] = bytes.Equal(baseJSON, candidateJSON)
		if !result.FieldParity[surface] {
			result.Passed = false
			for field := range differingFields(base, receipts[surface]) {
				result.MismatchedFields = appendField(result.MismatchedFields, surface, field)
			}
		}
	}
	if base.ReplayAllowed {
		result.Passed = false
		result.MismatchedFields = appendField(result.MismatchedFields, "replay_authorized", "must be false")
	}
	if result.Surfaces["go"].Python == nil || result.Surfaces["cli"].Python == nil || result.Surfaces["mcp"].Python == nil {
		result.Passed = false
	}
	return result
}

func validatePythonResult(result map[string]any, receipt normalizedReceipt) error {
	checks := []struct {
		field string
		want  string
	}{
		{field: "schema", want: receipt.Schema},
		{field: "task_id", want: receipt.Task.ID},
		{field: "operation_id", want: receipt.Operation.ID},
		{field: "outcome", want: receipt.Operation.Outcome},
	}
	for _, check := range checks {
		got, _ := result[check.field].(string)
		if got != check.want {
			return fmt.Errorf("reported %s %q does not match receipt %q", check.field, got, check.want)
		}
	}
	if replayAuthorized, ok := result["replay_authorized"].(bool); !ok || replayAuthorized != receipt.ReplayAllowed {
		return fmt.Errorf("reported replay_authorized does not match receipt (got %v, want %v)", result["replay_authorized"], receipt.ReplayAllowed)
	}
	return nil
}

func validateScenario(input caseInput, receipt normalizedReceipt) error {
	switch input.Scenario {
	case "interrupted":
		if receipt.Task.State != "interrupted" || receipt.Operation.Outcome != "unknown" || receipt.ReplayAllowed {
			return fmt.Errorf("interrupted scenario requires task.state=interrupted, operation.outcome=unknown, and replay_authorized=false")
		}
	case "completed":
		if receipt.Task.State != "completed" || receipt.Operation.Verification != "verified" || receipt.ReplayAllowed {
			return fmt.Errorf("completed scenario requires task.state=completed, operation.verification=verified, and replay_authorized=false")
		}
	case "non_first_step":
		if receipt.Task.State != "completed" || receipt.Operation.Verification != "verified" || receipt.ReplayAllowed {
			return fmt.Errorf("non_first_step scenario requires a completed, verified task and replay_authorized=false")
		}
		if receipt.Operation.Ordinal <= 1 {
			return fmt.Errorf("non_first_step scenario requires operation.ordinal greater than 1")
		}
		if receipt.Target.FrameID == "" {
			return fmt.Errorf("non_first_step scenario requires a non-empty target.frame_id")
		}
	default:
		return fmt.Errorf("scenario must be interrupted, completed, or non_first_step")
	}
	if receipt.Policy == nil || receipt.Policy.Digest == "" {
		return fmt.Errorf("scenario requires policy.digest")
	}
	return nil
}

func normalizedFieldNames(receipt normalizedReceipt) []string {
	data, _ := json.Marshal(receipt)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runPythonVerifier(verifier, receiptPath string) (map[string]any, error) {
	command := exec.Command("python3", verifier, receiptPath)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("invalid verifier JSON: %w", err)
	}
	valid, _ := result["valid"].(bool)
	if !valid {
		return result, fmt.Errorf("verifier returned valid=false")
	}
	return result, nil
}

func differingFields(a, b normalizedReceipt) map[string][]string {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	var lmap, rmap map[string]json.RawMessage
	_ = json.Unmarshal(left, &lmap)
	_ = json.Unmarshal(right, &rmap)
	fields := map[string][]string{}
	for key, lvalue := range lmap {
		if !bytes.Equal(lvalue, rmap[key]) {
			fields[key] = []string{"differs from Go baseline"}
		}
	}
	for key := range rmap {
		if _, exists := lmap[key]; !exists {
			fields[key] = []string{"present only outside Go baseline"}
		}
	}
	return fields
}

func appendField(fields map[string][]string, key, value string) map[string][]string {
	if fields == nil {
		fields = map[string][]string{}
	}
	fields[key] = append(fields[key], value)
	return fields
}

func appendError(current, next string) string {
	if current == "" {
		return next
	}
	return current + "; " + next
}

func normalize(r agent.RecoveryReceipt) normalizedReceipt {
	refs := append([]string{}, r.Evidence.References...)
	// The task deadline is an absolute epoch timestamp. Separate surface runs
	// necessarily start at different times, so compare the durable operation
	// contract while reporting the excluded timing metadata explicitly.
	r.Operation.DeadlineAt = 0
	return normalizedReceipt{
		Schema: r.Schema, Task: r.Task, Runtime: r.Runtime, Browser: r.Browser,
		Target: r.Target, Policy: r.Policy, Operation: r.Operation,
		Postcondition: r.Postcondition, EvidenceTrust: r.Evidence.Trust,
		EvidenceRefs: refs, ReplayAllowed: r.Recovery.ReplayAuthorized,
		NextStep: r.Recovery.NextStep,
	}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
