//go:build integration

package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/scout/agent"
)

func batchTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Batch Test</title></head>
<body>
  <h1 id="title">Batch Page</h1>
  <button id="reveal" onclick="document.querySelector('#result').textContent='Saved successfully'">Save</button>
  <div id="result"></div>
  <form>
    <label for="name">Name</label>
    <input id="name" name="name" type="text" />
    <label for="email">Email</label>
    <input id="email" name="email" type="email" />
    <button id="submit" type="submit">Submit</button>
  </form>
  <div id="section" style="margin-top: 2000px;">Far away section</div>
</body>
</html>`)
	})
	return httptest.NewServer(mux)
}

func TestExecuteBatchPostconditions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := batchTestServer()
	defer ts.Close()
	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	result, err := s.ExecuteBatch([]agent.BatchAction{
		{Action: "click", Selector: "#reveal", ExpectedText: "Saved successfully"},
		{Action: "type", Selector: "#name", Value: "Alice", ExpectedText: "This text is absent"},
		{Action: "wait", Selector: "#title"},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if result.Succeeded != 2 || result.Failed != 1 || result.Verified != 2 || result.Rejected != 1 || result.Unverified != 0 || result.Unknown != 0 {
		t.Fatalf("unexpected counts: %+v", result)
	}
	if step := result.Results[0]; !step.Success || !step.Verified || step.Outcome != "postcondition_verified" {
		t.Errorf("expected verified step: %+v", step)
	}
	if step := result.Results[1]; step.Success || step.Verified || step.Outcome != "postcondition_failed" {
		t.Errorf("expected failed postcondition: %+v", step)
	}
	if !result.Results[2].Success {
		t.Errorf("batch did not continue after failed postcondition: %+v", result.Results[2])
	}
}

func TestExecuteBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := batchTestServer()
	defer ts.Close()

	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.ExecuteBatch([]agent.BatchAction{
		{Action: "type", Selector: "#name", Value: "Alice"},
		{Action: "type", Selector: "#email", Value: "alice-test-user"},
		{Action: "scroll_to", Selector: "#section"},
		{Action: "click", Selector: "#submit"},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}

	if result.Total != 4 {
		t.Errorf("total: expected 4, got %d", result.Total)
	}
	if result.Succeeded != 4 {
		t.Errorf("succeeded: expected 4, got %d", result.Succeeded)
	}
	if result.Failed != 0 {
		t.Errorf("failed: expected 0, got %d", result.Failed)
	}
	if result.Verified != 0 || result.Rejected != 0 || result.Unverified != 4 || result.Unknown != 0 {
		t.Errorf("unexpected evidence counts: %+v", result)
	}
	for _, step := range result.Results {
		if step.Outcome != "effect_unverified" || step.Verified || step.Evidence == "" || step.RecoveryHint == "" {
			t.Errorf("batch step lacks honest outcome metadata: %+v", step)
		}
	}
	for i, r := range result.Results {
		if !r.Success {
			t.Errorf("action %d (%s): expected success, got error %q", i, r.Action, r.Error)
		}
	}
}

func TestExecuteBatchContinuesOnError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := batchTestServer()
	defer ts.Close()

	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.ExecuteBatch([]agent.BatchAction{
		{Action: "click", Selector: "#nonexistent"},
		{Action: "type", Selector: "#name", Value: "Bob"},
		{Action: "unknown_action"},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}

	if result.Total != 3 {
		t.Errorf("total: expected 3, got %d", result.Total)
	}
	if result.Succeeded != 1 {
		t.Errorf("succeeded: expected 1, got %d", result.Succeeded)
	}
	if result.Failed != 2 {
		t.Errorf("failed: expected 2, got %d", result.Failed)
	}
	if result.Rejected != 2 || result.Verified != 0 || result.Unverified != 1 || result.Unknown != 0 {
		t.Errorf("unexpected evidence counts: %+v", result)
	}
	if result.Results[0].Outcome != "action_failed" || result.Results[0].RecoveryHint == "" {
		t.Errorf("failed batch step lacks recovery outcome: %+v", result.Results[0])
	}

	if result.Results[0].Success {
		t.Error("action 0 (click #nonexistent): expected failure")
	}
	if !result.Results[1].Success {
		t.Errorf("action 1 (type #name): expected success, got error %q", result.Results[1].Error)
	}
	if result.Results[2].Success {
		t.Error("action 2 (unknown_action): expected failure")
	}
	if result.Results[2].Error == "" {
		t.Error("action 2: expected error message")
	}
}

func TestExecuteBatchStopsOnUnknownAtDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body>
<button id="late" onclick="const until=Date.now()+900; while(Date.now()<until){} document.querySelector('#status').textContent='Saved';">Save</button>
<div id="status"></div><input id="later">
</body></html>`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := s.ExecuteBatchContext(ctx, []agent.BatchAction{
		{Action: "click", Selector: "#late", ExpectedText: "Saved"},
		{Action: "type", Selector: "#later", Value: "must not run"},
	})
	if err == nil {
		t.Fatal("ExecuteBatchContext returned nil error after the caller deadline")
	}
	if result == nil || len(result.Results) != 1 {
		t.Fatalf("partial batch = %+v, want only the interrupted click", result)
	}
	step := result.Results[0]
	if step.Outcome != string(agent.OutcomeUnknown) || step.RecoveryRecord == nil {
		t.Fatalf("first step = %+v, want unknown outcome with recovery record", step)
	}
	if result.StoppedOnUnknownAt == nil || *result.StoppedOnUnknownAt != 0 || result.NextActionIndex != 1 || !result.ResumeBlocked {
		t.Fatalf("stop metadata = %+v, want stop at 0 and next action 1 blocked", result)
	}
}

func TestExecuteBatchFillFormSemantic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := batchTestServer()
	defer ts.Close()

	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.ExecuteBatch([]agent.BatchAction{
		{
			Action: "fill_form_semantic",
			Fields: map[string]string{
				"Name":  "Charlie",
				"Email": "charlie-test-user",
			},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}

	if result.Succeeded != 1 {
		t.Errorf("succeeded: expected 1, got %d", result.Succeeded)
	}
	if result.Results[0].Error != "" {
		t.Errorf("fill_form_semantic error: %s", result.Results[0].Error)
	}
}

func TestExecuteBatchWait(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := batchTestServer()
	defer ts.Close()

	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.ExecuteBatch([]agent.BatchAction{
		{Action: "wait", Selector: "#title"},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}

	if result.Succeeded != 1 {
		t.Errorf("succeeded: expected 1, got %d", result.Succeeded)
	}
}

func TestExecuteBatchEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := batchTestServer()
	defer ts.Close()

	s := newSession(t)
	if _, err := s.Navigate(ts.URL); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := s.ExecuteBatch([]agent.BatchAction{})
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}

	if result.Total != 0 {
		t.Errorf("total: expected 0, got %d", result.Total)
	}
	if result.Succeeded != 0 {
		t.Errorf("succeeded: expected 0, got %d", result.Succeeded)
	}
}
