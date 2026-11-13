// Command report aggregates repeated Scout benchmark JSON reports.
//
// Usage: go run ./examples/benchmark/report report-1.json report-2.json ...
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
)

type scenario struct {
	ID             string `json:"id"`
	Success        bool   `json:"success"`
	FalseSuccess   bool   `json:"false_success"`
	DurationMicros int64  `json:"duration_us"`
	OutputBytes    int    `json:"output_bytes"`
	ActionCalls    int    `json:"action_calls"`
}

type inputReport struct {
	Version                   string          `json:"benchmark_version"`
	ComparisonProtocolVersion string          `json:"comparison_protocol_version"`
	FixtureVersion            string          `json:"fixture_version"`
	ScenariosRaw              json.RawMessage `json:"scenarios"`
	ScenarioID                string          `json:"scenario_id"`
	ToolName                  string          `json:"tool_name"`
	ToolVersion               string          `json:"tool_version"`
	BrowserVersion            string          `json:"browser_version"`
	FixtureURL                string          `json:"fixture_url"`
	Predicate                 string          `json:"verification_predicate"`
	SuccessPredicate          string          `json:"success_predicate"`
	Success                   bool            `json:"success"`
	FalseSuccess              bool            `json:"false_success"`
	Repetitions               int             `json:"repetitions"`
	RepetitionsPerScenario    int             `json:"repetitions_per_scenario"`
	SuccessfulRepetitions     int             `json:"successful_repetitions"`
	WorkflowDuration          json.RawMessage `json:"workflow_duration_ms"`
	OutputBytes               int             `json:"output_bytes"`
	OutputBytesDefinition     string          `json:"output_bytes_definition"`
	DurationScope             string          `json:"duration_scope"`
	ActionCalls               int             `json:"action_calls"`
	ActionCallsPerRepetition  int             `json:"action_calls_per_repetition"`
	ToolCalls                 int             `json:"tool_calls"`
	ToolCallsPerRepetition    int             `json:"tool_calls_per_repetition"`
	NativeRecoveryRecord      *bool           `json:"native_recovery_record"`
	RecoveryMethod            string          `json:"recovery_method"`
	ID                        string          `json:"id"`
	Duration                  int64           `json:"duration_us"`
	Completed                 int             `json:"completed"`
	Total                     int             `json:"total"`
}

type metricSummary struct {
	Min    int64 `json:"min"`
	Median int64 `json:"median"`
	P95    int64 `json:"p95"`
	Max    int64 `json:"max"`
}

type scenarioSummary struct {
	ID             string        `json:"id"`
	Runs           int           `json:"runs"`
	ReportCoverage int           `json:"report_coverage"`
	Passed         int           `json:"passed"`
	PassRate       float64       `json:"pass_rate"`
	FalseSuccesses int           `json:"false_successes"`
	DurationUS     metricSummary `json:"duration_us"`
	OutputBytes    metricSummary `json:"output_bytes"`
	ActionCalls    metricSummary `json:"action_calls"`
}

type aggregate struct {
	Version        string            `json:"benchmark_version"`
	InputReports   int               `json:"input_reports"`
	Suite          *suiteSummary     `json:"suite,omitempty"`
	Scenarios      []scenarioSummary `json:"scenarios"`
	ComparisonRows []comparisonRow   `json:"comparison_rows,omitempty"`
}

type comparisonRow struct {
	ScenarioID                string              `json:"scenario_id"`
	ToolName                  string              `json:"tool_name"`
	ToolVersion               string              `json:"tool_version,omitempty"`
	BrowserVersion            string              `json:"browser_version,omitempty"`
	BenchmarkVersion          string              `json:"benchmark_version,omitempty"`
	ComparisonProtocolVersion string              `json:"comparison_protocol_version,omitempty"`
	FixtureVersion            string              `json:"fixture_version,omitempty"`
	FixtureURL                string              `json:"fixture_url,omitempty"`
	VerificationPredicate     string              `json:"verification_predicate,omitempty"`
	Success                   bool                `json:"success"`
	FalseSuccess              bool                `json:"false_success"`
	Repetitions               int                 `json:"repetitions"`
	SuccessfulRepetitions     int                 `json:"successful_repetitions"`
	PassRate                  float64             `json:"pass_rate"`
	MedianDurationMS          *float64            `json:"median_duration_ms,omitempty"`
	P95DurationMS             *float64            `json:"p95_duration_ms,omitempty"`
	DurationScope             string              `json:"duration_scope,omitempty"`
	OutputBytes               int                 `json:"output_bytes,omitempty"`
	OutputBytesDefinition     string              `json:"output_bytes_definition,omitempty"`
	ActionCallsPerRepetition  int                 `json:"action_calls_per_repetition,omitempty"`
	ToolCallsPerRepetition    int                 `json:"tool_calls_per_repetition,omitempty"`
	NativeRecoveryRecord      *bool               `json:"native_recovery_record,omitempty"`
	RecoveryMethod            string              `json:"recovery_method,omitempty"`
	ResultComparable          bool                `json:"result_comparable_with_peers"`
	TimingComparable          bool                `json:"timing_comparable_with_peers"`
	OutputComparable          bool                `json:"output_comparable_with_peers"`
	ComparableWithPeers       bool                `json:"comparable_with_peers"`
	ComparabilityReason       string              `json:"comparability_reason"`
	PeerComparability         []peerComparability `json:"peer_comparability,omitempty"`
}

type peerComparability struct {
	ToolName         string `json:"tool_name"`
	ResultComparable bool   `json:"result_comparable"`
	TimingComparable bool   `json:"timing_comparable"`
	OutputComparable bool   `json:"output_comparable"`
	Reason           string `json:"reason"`
}

type comparisonScenario struct {
	Success                  bool            `json:"success"`
	FalseSuccess             bool            `json:"false_success"`
	Repetitions              int             `json:"repetitions"`
	SuccessfulRepetitions    int             `json:"successful_repetitions"`
	WorkflowDuration         json.RawMessage `json:"workflow_duration_ms"`
	OutputBytes              int             `json:"output_bytes"`
	ActionCalls              int             `json:"action_calls"`
	ActionCallsPerRepetition int             `json:"action_calls_per_repetition"`
	ToolCalls                int             `json:"tool_calls"`
	ToolCallsPerRepetition   int             `json:"tool_calls_per_repetition"`
	NativeRecoveryRecord     *bool           `json:"native_recovery_record"`
	RecoveryMethod           string          `json:"recovery_method"`
	FixtureURL               string          `json:"fixture_url"`
	VerificationPredicate    string          `json:"verification_predicate"`
	DurationScope            string          `json:"duration_scope"`
	OutputBytesDefinition    string          `json:"output_bytes_definition"`
}

type durationStats struct {
	Median   float64   `json:"median"`
	P95      float64   `json:"p95"`
	Includes string    `json:"includes"`
	Samples  []float64 `json:"samples"`
}

type suiteSummary struct {
	Reports  int     `json:"reports"`
	Complete int     `json:"complete_reports"`
	PassRate float64 `json:"pass_rate"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(paths []string, output io.Writer) error {
	if len(paths) == 0 {
		return fmt.Errorf("usage: go run ./examples/benchmark/report report-1.json report-2.json")
	}
	byID := make(map[string][]scenario)
	version := ""
	completedSuites := 0
	var comparisonRows []comparisonRow
	suiteReports := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var report inputReport
		if err := json.Unmarshal(data, &report); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		var suiteScenarios []scenario
		var nestedScenarios map[string]json.RawMessage
		trimmedScenarios := bytes.TrimSpace(report.ScenariosRaw)
		if len(trimmedScenarios) > 0 && trimmedScenarios[0] == '{' {
			if err := json.Unmarshal(trimmedScenarios, &nestedScenarios); err != nil {
				return fmt.Errorf("decode nested scenarios in %s: %w", path, err)
			}
		} else if len(trimmedScenarios) > 0 && trimmedScenarios[0] == '[' {
			if err := json.Unmarshal(trimmedScenarios, &suiteScenarios); err != nil {
				return fmt.Errorf("decode scenario list in %s: %w", path, err)
			}
		}
		if report.Version == "" {
			if report.ToolName == "" && len(suiteScenarios) == 0 && report.ID == "" {
				return fmt.Errorf("%s has no benchmark_version or comparison report metadata", path)
			}
		}
		if report.ToolName != "" {
			if report.Predicate == "" {
				report.Predicate = report.SuccessPredicate
			}
			normalized := normalizeComparison(report, nestedScenarios)
			if len(normalized) == 0 {
				return fmt.Errorf("%s has comparison metadata but no scenario_id or scenarios", path)
			}
			comparisonRows = append(comparisonRows, normalized...)
			continue
		}
		suiteReports++
		if len(suiteScenarios) == 0 && report.ID != "" {
			suiteScenarios = []scenario{{ID: report.ID, Success: report.Success, FalseSuccess: report.FalseSuccess, DurationMicros: report.Duration, OutputBytes: report.OutputBytes, ActionCalls: report.ActionCalls}}
		}
		if report.Total > 0 && report.Completed == report.Total {
			completedSuites++
		}
		if version == "" {
			version = report.Version
		} else if version != report.Version {
			return fmt.Errorf("mixed benchmark versions: %q and %q (%s)", version, report.Version, path)
		}
		seen := make(map[string]bool)
		for _, item := range suiteScenarios {
			if item.ID == "" {
				return fmt.Errorf("%s contains a scenario without id", path)
			}
			if seen[item.ID] {
				return fmt.Errorf("%s contains duplicate scenario %q", path, item.ID)
			}
			seen[item.ID] = true
			byID[item.ID] = append(byID[item.ID], item)
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := aggregate{Version: version, InputReports: len(paths), ComparisonRows: comparisonRows}
	if suiteReports > 0 {
		out.Suite = &suiteSummary{Reports: suiteReports, Complete: completedSuites, PassRate: float64(completedSuites) / float64(suiteReports)}
	}
	for _, id := range ids {
		items := byID[id]
		durations, outputs, calls := make([]int64, 0, len(items)), make([]int64, 0, len(items)), make([]int64, 0, len(items))
		passed, falseSuccesses := 0, 0
		for _, item := range items {
			if item.Success {
				passed++
			}
			if item.FalseSuccess {
				falseSuccesses++
			}
			if item.DurationMicros > 0 {
				durations = append(durations, item.DurationMicros)
			}
			outputs = append(outputs, int64(item.OutputBytes))
			calls = append(calls, int64(item.ActionCalls))
		}
		out.Scenarios = append(out.Scenarios, scenarioSummary{
			ID: id, Runs: len(items), ReportCoverage: len(items), Passed: passed, PassRate: float64(passed) / float64(len(items)), FalseSuccesses: falseSuccesses,
			DurationUS: summarize(durations), OutputBytes: summarize(outputs), ActionCalls: summarize(calls),
		})
	}
	for index := range out.ComparisonRows {
		peers := 0
		allResultComparable, allTimingComparable, allOutputComparable := true, true, true
		var peerRows []peerComparability
		for otherIndex := range out.ComparisonRows {
			if index == otherIndex || out.ComparisonRows[index].ToolName == out.ComparisonRows[otherIndex].ToolName || out.ComparisonRows[index].ScenarioID != out.ComparisonRows[otherIndex].ScenarioID {
				continue
			}
			peers++
			left, right := out.ComparisonRows[index], out.ComparisonRows[otherIndex]
			sameResultConditions := left.FixtureURL != "" && left.FixtureURL == right.FixtureURL && left.VerificationPredicate != "" && left.VerificationPredicate == right.VerificationPredicate && left.BenchmarkVersion != "" && left.BenchmarkVersion == right.BenchmarkVersion && left.ComparisonProtocolVersion != "" && left.ComparisonProtocolVersion == right.ComparisonProtocolVersion && left.FixtureVersion != "" && left.FixtureVersion == right.FixtureVersion && left.BrowserVersion != "" && left.BrowserVersion == right.BrowserVersion
			timingComparable := sameResultConditions && left.DurationScope != "" && left.DurationScope == right.DurationScope
			outputComparable := sameResultConditions && left.OutputBytesDefinition != "" && left.OutputBytesDefinition == right.OutputBytesDefinition
			allResultComparable = allResultComparable && sameResultConditions
			allTimingComparable = allTimingComparable && timingComparable
			allOutputComparable = allOutputComparable && outputComparable
			reason := "all comparison metadata matches"
			switch {
			case !sameResultConditions:
				reason = "result predicates, versions, browser, or fixture differ or are missing"
			case !timingComparable && !outputComparable:
				reason = "timing scope and output-byte definition differ or are missing"
			case !timingComparable:
				reason = "timing scope differs or is missing"
			case !outputComparable:
				reason = "output-byte definition differs or is missing"
			}
			peerRows = append(peerRows, peerComparability{ToolName: right.ToolName, ResultComparable: sameResultConditions, TimingComparable: timingComparable, OutputComparable: outputComparable, Reason: reason})
		}
		if peers == 0 {
			out.ComparisonRows[index].ComparabilityReason = "no other tool report for this scenario"
			continue
		}
		row := &out.ComparisonRows[index]
		row.PeerComparability = peerRows
		row.ResultComparable = allResultComparable
		row.TimingComparable = allTimingComparable
		row.OutputComparable = allOutputComparable
		row.ComparableWithPeers = allResultComparable && allTimingComparable && allOutputComparable
		switch {
		case !allResultComparable:
			row.ComparabilityReason = "result predicates or benchmark, comparison protocol, browser, or fixture versions differ or are missing"
		case !allTimingComparable && !allOutputComparable:
			row.ComparabilityReason = "result conditions match, but timing scope and output-byte definition differ or are missing"
		case !allTimingComparable:
			row.ComparabilityReason = "result conditions match, but timing scope differs or is missing"
		case !allOutputComparable:
			row.ComparabilityReason = "result conditions match, but output-byte definition differs or is missing"
		default:
			row.ComparabilityReason = "result predicates, versions, fixture, timing scope, and output-byte definition match"
		}
	}
	sort.Slice(out.ComparisonRows, func(i, j int) bool {
		if out.ComparisonRows[i].ScenarioID == out.ComparisonRows[j].ScenarioID {
			return out.ComparisonRows[i].ToolName < out.ComparisonRows[j].ToolName
		}
		return out.ComparisonRows[i].ScenarioID < out.ComparisonRows[j].ScenarioID
	})
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

func normalizeComparison(report inputReport, nestedScenarios map[string]json.RawMessage) []comparisonRow {
	tool := report.ToolName
	if tool == "" {
		tool = "unspecified"
	}
	var rows []comparisonRow
	if len(nestedScenarios) > 0 {
		for id, raw := range nestedScenarios {
			var scenario comparisonScenario
			if json.Unmarshal(raw, &scenario) != nil {
				continue
			}
			fixtureURL := scenario.FixtureURL
			if fixtureURL == "" {
				fixtureURL = report.FixtureURL
			}
			row := makeComparisonRow(id, tool, report.ToolVersion, report.BrowserVersion, report.Version, report.ComparisonProtocolVersion, report.FixtureVersion, fixtureURL, scenario.VerificationPredicate, scenario.Success, scenario.FalseSuccess, scenario.Repetitions, scenario.SuccessfulRepetitions, scenario.WorkflowDuration, scenario.OutputBytes, firstNonempty(scenario.OutputBytesDefinition, report.OutputBytesDefinition), firstNonempty(scenario.DurationScope, durationIncludes(report.WorkflowDuration)), perRepetition(scenario.ActionCallsPerRepetition, scenario.ActionCalls, scenario.Repetitions), perRepetition(scenario.ToolCallsPerRepetition, scenario.ToolCalls, scenario.Repetitions))
			row.NativeRecoveryRecord = scenario.NativeRecoveryRecord
			if row.NativeRecoveryRecord == nil {
				row.NativeRecoveryRecord = report.NativeRecoveryRecord
			}
			row.RecoveryMethod = firstNonempty(scenario.RecoveryMethod, report.RecoveryMethod)
			rows = append(rows, row)
		}
		return rows
	}
	if report.ScenarioID == "" {
		return nil
	}
	repetitions := firstNonzero(report.RepetitionsPerScenario, report.Repetitions)
	if repetitions == 0 {
		repetitions = 1
	}
	successes := report.SuccessfulRepetitions
	if successes == 0 && report.Success {
		successes = repetitions
	}
	row := makeComparisonRow(report.ScenarioID, tool, report.ToolVersion, report.BrowserVersion, report.Version, report.ComparisonProtocolVersion, report.FixtureVersion, report.FixtureURL, report.Predicate, report.Success, report.FalseSuccess, repetitions, successes, report.WorkflowDuration, report.OutputBytes, report.OutputBytesDefinition, firstNonempty(report.DurationScope, durationIncludes(report.WorkflowDuration)), perRepetition(report.ActionCallsPerRepetition, report.ActionCalls, repetitions), perRepetition(report.ToolCallsPerRepetition, report.ToolCalls, repetitions))
	row.NativeRecoveryRecord, row.RecoveryMethod = report.NativeRecoveryRecord, report.RecoveryMethod
	rows = append(rows, row)
	return rows
}

func makeComparisonRow(id, tool, toolVersion, browserVersion, benchmarkVersion, comparisonProtocolVersion, fixtureVersion, fixtureURL, predicate string, success, falseSuccess bool, repetitions, successes int, rawDuration json.RawMessage, outputBytes int, outputDefinition, durationScope string, actionCalls, toolCalls int) comparisonRow {
	row := comparisonRow{ScenarioID: id, ToolName: tool, ToolVersion: toolVersion, BrowserVersion: browserVersion, BenchmarkVersion: benchmarkVersion, ComparisonProtocolVersion: comparisonProtocolVersion, FixtureVersion: fixtureVersion, FixtureURL: fixtureURL, VerificationPredicate: predicate, Success: success, FalseSuccess: falseSuccess, Repetitions: repetitions, SuccessfulRepetitions: successes, OutputBytes: outputBytes, OutputBytesDefinition: outputDefinition, DurationScope: durationScope, ActionCallsPerRepetition: actionCalls, ToolCallsPerRepetition: toolCalls}
	if repetitions > 0 {
		row.PassRate = float64(successes) / float64(repetitions)
	}
	if len(rawDuration) > 0 {
		var scalar float64
		if json.Unmarshal(rawDuration, &scalar) == nil {
			row.MedianDurationMS = &scalar
		} else {
			var stats durationStats
			if json.Unmarshal(rawDuration, &stats) == nil {
				if len(stats.Samples) > 0 {
					samples := append([]float64(nil), stats.Samples...)
					sort.Float64s(samples)
					median := samples[len(samples)/2]
					if len(samples)%2 == 0 {
						median = (samples[len(samples)/2-1] + samples[len(samples)/2]) / 2
					}
					p95 := samples[int(math.Ceil(0.95*float64(len(samples))))-1]
					row.MedianDurationMS, row.P95DurationMS = &median, &p95
				} else {
					row.MedianDurationMS, row.P95DurationMS = optionalPositive(stats.Median), optionalPositive(stats.P95)
				}
				row.DurationScope = stats.Includes
			}
		}
	}
	return row
}

func durationIncludes(raw json.RawMessage) string {
	var stats durationStats
	if json.Unmarshal(raw, &stats) != nil {
		return ""
	}
	return stats.Includes
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func optionalPositive(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func firstNonzero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func perRepetition(explicit, total, repetitions int) int {
	if explicit != 0 {
		return explicit
	}
	if total == 0 || repetitions <= 0 {
		return 0
	}
	return int(math.Round(float64(total) / float64(repetitions)))
}

func summarize(values []int64) metricSummary {
	if len(values) == 0 {
		return metricSummary{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	median := values[len(values)/2]
	if len(values)%2 == 0 {
		median = (values[len(values)/2-1] + values[len(values)/2]) / 2
	}
	p95Index := (len(values)*95+99)/100 - 1 // nearest-rank percentile
	return metricSummary{Min: values[0], Median: median, P95: values[p95Index], Max: values[len(values)-1]}
}
