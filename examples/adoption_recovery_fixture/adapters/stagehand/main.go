package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	stagehand "github.com/browserbase/stagehand/packages/sdk-go/v4"
)

type report struct {
	Status          string `json:"status"`
	VerifiedText    string `json:"verified_text,omitempty"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	TaskID          string `json:"task_id"`
	GoVersion       string `json:"go_version"`
	SDKVersion      string `json:"sdk_version"`
	Dispatch        int    `json:"dispatch_count,omitempty"`
	LedgerCount     int    `json:"ledger_count,omitempty"`
	Initial         string `json:"initial_status,omitempty"`
	Inspected       bool   `json:"ledger_inspected"`
	Retried         bool   `json:"retry_attempted"`
	TaskState       string `json:"task_state,omitempty"`
	TaskRecovered   bool   `json:"task_record_recovered,omitempty"`
	RecoveryOutcome string `json:"recovery_outcome,omitempty"`
	Resolved        bool   `json:"resolution_recorded,omitempty"`
	RecordTaskID    string `json:"record_task_id,omitempty"`
	PlanFingerprint string `json:"plan_fingerprint,omitempty"`
	RecordDispatch  string `json:"record_dispatch,omitempty"`
	Resolution      string `json:"record_resolution,omitempty"`
	EvidenceSource  string `json:"record_evidence_source,omitempty"`
}

// hostTaskJournal is deliberately host-authored glue. Stagehand does not
// provide Scout's durable task-ID reservation and resolution contract.
type hostTaskJournal struct {
	TaskID          string `json:"task_id"`
	PlanFingerprint string `json:"plan_fingerprint"`
	State           string `json:"state"`
	Operation       string `json:"operation"`
	Dispatch        string `json:"dispatch"`
	Resolution      string `json:"resolution,omitempty"`
	EvidenceSource  string `json:"evidence_source,omitempty"`
	EvidenceRef     string `json:"evidence_reference,omitempty"`
	ObservedAt      string `json:"observed_at,omitempty"`
}

func main() {
	stage := flag.String("stage", "first-success", "first-success, control, submit, or reconcile")
	taskID := flag.String("task-id", "", "task identity")
	fixture := flag.String("fixture", "", "fixture base URL or first-success page URL")
	storePath := flag.String("store", "", "host journal path shared by submit and reconcile")
	flag.Parse()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := run(ctx, *stage, *taskID, *fixture, *storePath, started)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func run(ctx context.Context, stage, taskID, fixture, storePath string, started time.Time) (report, error) {
	if taskID == "" || fixture == "" {
		return report{}, fmt.Errorf("--task-id and --fixture are required")
	}
	if stage == "reconcile" {
		if storePath == "" {
			return report{}, fmt.Errorf("--store is required for reconcile")
		}
		return reconcile(ctx, fixture, taskID, storePath, started)
	}
	browser, err := stagehand.LaunchLocalBrowser(ctx, &stagehand.LocalBrowserLaunchOptions{Headless: true})
	if err != nil {
		return report{}, fmt.Errorf("launch local Chrome: %w", err)
	}
	defer browser.Close(ctx)
	client, err := stagehand.Create(ctx, stagehand.CreateOptions{Browser: browser})
	if err != nil {
		return report{}, fmt.Errorf("initialize Stagehand without a model: %w", err)
	}
	defer client.Close(ctx)
	bc, err := client.Browser().Context()
	if err != nil {
		return report{}, err
	}
	page, err := bc.NewPage(ctx)
	if err != nil {
		return report{}, err
	}
	url := fixture
	if stage == "submit" || stage == "control" {
		url = strings.TrimRight(fixture, "/") + "/?task=" + taskID
	}
	if _, err := page.Goto(ctx, url, nil); err != nil {
		return report{}, err
	}
	if stage == "first-success" {
		if err := page.Locator("#complete").Click(ctx, nil); err != nil {
			return report{}, err
		}
		text, err := waitForText(ctx, page.Locator("#status"), "Complete")
		if err != nil {
			return report{}, err
		}
		return report{Status: "verified", VerifiedText: text, TaskID: taskID, GoVersion: runtime.Version(), SDKVersion: "v4.3.0", ElapsedMS: time.Since(started).Milliseconds()}, nil
	}
	if stage == "control" {
		if err := page.Locator("#approve").Click(ctx, nil); err != nil {
			return report{}, fmt.Errorf("click approval: %w", err)
		}
		status := page.Locator("#status")
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			text, err := status.InnerText(ctx)
			if err == nil && strings.TrimSpace(text) == "Accepted 1" {
				ledgerCount, err := fixtureLedgerCount(ctx, fixture, taskID)
				if err != nil {
					return report{}, err
				}
				return report{Status: "verified", VerifiedText: strings.TrimSpace(text), TaskID: taskID, GoVersion: runtime.Version(), SDKVersion: "v4.3.0", Dispatch: 1, LedgerCount: ledgerCount, ElapsedMS: time.Since(started).Milliseconds()}, nil
			}
			select {
			case <-ctx.Done():
				return report{}, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	if stage != "submit" {
		return report{}, fmt.Errorf("unknown --stage %q", stage)
	}
	if storePath == "" {
		return report{}, fmt.Errorf("--store is required for submit")
	}
	if _, err := os.Stat(storePath); err == nil {
		return report{}, fmt.Errorf("host journal already exists at %s; use a fresh task ID and path", storePath)
	} else if !os.IsNotExist(err) {
		return report{}, err
	}
	journal := hostTaskJournal{
		TaskID: taskID, PlanFingerprint: studyPlanFingerprint(taskID, fixture),
		State: "in_flight", Operation: "click:#approve", Dispatch: "may_have_dispatched",
	}
	if err := writeJournal(storePath, journal); err != nil {
		return report{}, fmt.Errorf("persist host task journal before dispatch: %w", err)
	}
	if err := page.Locator("#approve").Click(ctx, nil); err != nil {
		// Keep the possible-dispatch record for the facilitator's forced process
		// loss. A tool error cannot establish that the site did not commit.
		<-ctx.Done()
		return report{}, ctx.Err()
	}
	// Wait until the facilitator terminates this worker after ledger commit.
	<-ctx.Done()
	return report{}, ctx.Err()
}

func waitForText(ctx context.Context, locator *stagehand.PageLocator, expected string) (string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		text, err := locator.InnerText(ctx)
		if err == nil && strings.TrimSpace(text) == expected {
			return strings.TrimSpace(text), nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for text %q: %w", expected, ctx.Err())
		case <-ticker.C:
		}
	}
}

func fixtureLedgerCount(ctx context.Context, fixture, taskID string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture+"/ledger?task="+url.QueryEscape(taskID), nil)
	if err != nil {
		return 0, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fixture ledger returned %s", response.Status)
	}
	var ledger struct {
		TaskID string `json:"task_id"`
		Count  int    `json:"count"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ledger); err != nil {
		return 0, err
	}
	if ledger.TaskID != taskID {
		return 0, fmt.Errorf("fixture ledger task mismatch: %q", ledger.TaskID)
	}
	return ledger.Count, nil
}

func reconcile(ctx context.Context, fixture, taskID, storePath string, started time.Time) (report, error) {
	journal, err := readJournal(storePath)
	if err != nil {
		return report{}, fmt.Errorf("read host task journal: %w", err)
	}
	if journal.TaskID != taskID || journal.PlanFingerprint != studyPlanFingerprint(taskID, fixture) || journal.State != "in_flight" || journal.Dispatch != "may_have_dispatched" || journal.Resolution != "" {
		return report{}, fmt.Errorf("host task journal identity or interrupted state does not match task %q", taskID)
	}
	ledger, err := readLedger(ctx, fixture, taskID)
	if err != nil {
		return report{}, err
	}
	status := "rejected"
	if ledger.Count == 1 {
		status = "verified"
	} else if ledger.Count == 0 {
		status = "unknown"
	}
	resolution := "rejected"
	if ledger.Count == 1 {
		resolution = "verified_complete"
	} else if ledger.Count == 0 {
		resolution = "unresolved"
	}
	journal.State, journal.Resolution = "resolved", resolution
	journal.EvidenceSource, journal.EvidenceRef = "study_fixture_ledger", taskID
	journal.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeJournal(storePath, journal); err != nil {
		return report{}, fmt.Errorf("persist host ledger resolution: %w", err)
	}
	confirmed, err := readJournal(storePath)
	if err != nil || confirmed.Resolution != resolution || confirmed.State != "resolved" || confirmed.EvidenceSource != journal.EvidenceSource || confirmed.EvidenceRef != journal.EvidenceRef {
		return report{}, fmt.Errorf("host ledger resolution was not durably confirmed")
	}
	return report{Status: status, TaskID: taskID, GoVersion: runtime.Version(), SDKVersion: "v4.3.0", Dispatch: ledger.Count, LedgerCount: ledger.Count, Initial: "unknown", Inspected: true, Retried: false, TaskState: "interrupted", TaskRecovered: true, RecoveryOutcome: "unknown", Resolved: true, RecordTaskID: journal.TaskID, PlanFingerprint: journal.PlanFingerprint, RecordDispatch: journal.Dispatch, Resolution: confirmed.Resolution, EvidenceSource: confirmed.EvidenceSource, ElapsedMS: time.Since(started).Milliseconds()}, nil
}

func studyPlanFingerprint(taskID, fixture string) string {
	identity := taskID + "\x00" + strings.TrimRight(fixture, "/") + "\x00POST /submit\x00#approve"
	sum := sha256.Sum256([]byte(identity))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readJournal(path string) (hostTaskJournal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return hostTaskJournal{}, err
	}
	var journal hostTaskJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return hostTaskJournal{}, err
	}
	return journal, nil
}

func writeJournal(path string, journal hostTaskJournal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".stagehand-task-journal-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type ledgerResult struct {
	TaskID string `json:"task_id"`
	Count  int    `json:"count"`
}

func readLedger(ctx context.Context, fixture, taskID string) (ledgerResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(fixture, "/")+"/ledger?task="+url.QueryEscape(taskID), nil)
	if err != nil {
		return ledgerResult{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ledgerResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ledgerResult{}, fmt.Errorf("fixture ledger returned %s", response.Status)
	}
	var ledger ledgerResult
	if err := json.NewDecoder(response.Body).Decode(&ledger); err != nil {
		return ledgerResult{}, err
	}
	if ledger.TaskID != taskID {
		return ledgerResult{}, fmt.Errorf("ledger task mismatch: %q", ledger.TaskID)
	}
	return ledger, nil
}
