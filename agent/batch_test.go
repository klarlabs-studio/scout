package agent_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
