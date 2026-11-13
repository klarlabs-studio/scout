package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlaybook_JSON_Roundtrip(t *testing.T) {
	pb := Playbook{
		Name: "login-flow",
		URL:  "https://example.com/login",
		Actions: []Action{
			{Type: "navigate", Value: "https://example.com/login"},
			{Type: "type", Selector: "#email", Value: "test-user"},
			{Type: "type", Selector: "#password", Value: "secret"},
			{Type: "click", Selector: "#submit"},
			{Type: "fill_form_semantic", Fields: map[string]string{"Email": "ab-user"}},
			{Type: "click_label", Label: 3},
			{Type: "wait", Selector: "#dashboard"},
			{
				Type:     "extract",
				Selector: "#username",
				Value:    "user",
				Expected: &ActionExpect{
					URL:      "https://example.com/dashboard",
					Title:    "Dashboard",
					Selector: "#username",
					Text:     "test-user",
				},
			},
		},
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(pb)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Playbook
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Name != pb.Name || got.URL != pb.URL {
		t.Errorf("basic fields mismatch")
	}
	if len(got.Actions) != len(pb.Actions) {
		t.Fatalf("actions length: got %d, want %d", len(got.Actions), len(pb.Actions))
	}
	if got.Actions[4].Fields["Email"] != "ab-user" {
		t.Error("fill_form_semantic fields not preserved")
	}
	if got.Actions[5].Label != 3 {
		t.Error("click_label label not preserved")
	}
	if got.Actions[7].Expected == nil {
		t.Fatal("expected field should be preserved")
	}
	if got.Actions[7].Expected.URL != "https://example.com/dashboard" {
		t.Error("expected URL not preserved")
	}
}

func TestSaveLoadPlaybook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	pb := &Playbook{
		Name:      "test-playbook",
		URL:       "https://example.com",
		Actions:   []Action{{Type: "navigate", Value: "https://example.com"}},
		CreatedAt: time.Now().Truncate(time.Second),
	}

	if err := SavePlaybook(pb, path); err != nil {
		t.Fatalf("SavePlaybook: %v", err)
	}

	loaded, err := LoadPlaybook(path)
	if err != nil {
		t.Fatalf("LoadPlaybook: %v", err)
	}

	if loaded.Name != pb.Name || loaded.URL != pb.URL {
		t.Errorf("loaded playbook mismatch: %+v", loaded)
	}
	if len(loaded.Actions) != 1 || loaded.Actions[0].Type != "navigate" {
		t.Error("actions not preserved")
	}
}

func TestSavePlaybook_InvalidPath(t *testing.T) {
	pb := &Playbook{Name: "test"}
	err := SavePlaybook(pb, "/nonexistent/dir/deep/test.json")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestLoadPlaybook_NotFound(t *testing.T) {
	_, err := LoadPlaybook("/nonexistent/playbook.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadPlaybook_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte("not json"), 0o600)

	_, err := LoadPlaybook(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestPlaybookResult_JSON(t *testing.T) {
	pr := PlaybookResult{
		Success:    false,
		StepsRun:   3,
		TotalSteps: 5,
		FailedAt:   3,
		FailedAction: &Action{
			Type:     "click",
			Selector: "#missing",
		},
		Error:     "element not found",
		Extracted: map[string]string{"user": "Alice"},
	}
	data, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got PlaybookResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.FailedAt != 3 || got.Error != "element not found" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.Extracted["user"] != "Alice" {
		t.Error("extracted data not preserved")
	}
}

func TestRecordAction(t *testing.T) {
	s := newTestSession()

	s.recordAction(Action{Type: "click", Selector: "#btn"})
	if s.recording != nil {
		t.Error("should not record when no recording is active")
	}

	s.recording = &recording{name: "test", url: "https://example.com"}
	s.recordAction(Action{Type: "navigate", Value: "https://example.com"})
	s.recordAction(Action{Type: "click", Selector: "#btn"})

	if len(s.recording.actions) != 2 {
		t.Errorf("expected 2 recorded actions, got %d", len(s.recording.actions))
	}
	if s.recording.actions[0].Type != "navigate" {
		t.Error("first action should be navigate")
	}
	if s.recording.actions[1].Selector != "#btn" {
		t.Error("second action selector mismatch")
	}
}

func TestExpectRecordedAction(t *testing.T) {
	s := newTestSession()
	if err := s.ExpectRecordedAction(ActionExpect{Text: "done"}); err == nil {
		t.Fatal("expected error without an active recording")
	}
	s.recording = &recording{name: "test"}
	if err := s.ExpectRecordedAction(ActionExpect{Text: "done"}); err == nil {
		t.Fatal("expected error without a recorded action")
	}
	s.recordAction(Action{Type: "click", Selector: "#submit"})
	if err := s.ExpectRecordedAction(ActionExpect{Selector: "#success", Text: "Saved"}); err != nil {
		t.Fatal(err)
	}
	if got := s.recording.actions[0].Expected; got == nil || got.Selector != "#success" || got.Text != "Saved" {
		t.Fatalf("expectation not attached: %+v", got)
	}
	if err := s.ExpectRecordedAction(ActionExpect{}); err == nil {
		t.Fatal("expected error for empty expectation")
	}
}

func TestResumePlaybookRejectsChangedWorkflow(t *testing.T) {
	s := newTestSession()
	pb := &Playbook{Name: "one", Actions: []Action{{Type: "wait", Selector: "#a"}}}
	previous := &PlaybookResult{TotalSteps: 1, FailedAt: 1, StepsRun: 1, PlaybookSHA256: playbookDigest(pb)}
	pb.Actions[0].Selector = "#b"
	if _, err := s.ResumePlaybook(pb, previous); err == nil {
		t.Fatal("expected changed playbook to be rejected")
	}
}

func TestResumePlaybookRejectsAmbiguousAction(t *testing.T) {
	s := newTestSession()
	pb := &Playbook{Name: "ambiguous", Actions: []Action{{Type: "click", Selector: "#submit"}}}
	previous := &PlaybookResult{TotalSteps: 1, FailedAt: 1, StepsRun: 0, PlaybookSHA256: playbookDigest(pb), ResumeSafe: false}
	if _, err := s.ResumePlaybook(pb, previous); err == nil || !strings.Contains(err.Error(), "cannot automatically resume") {
		t.Fatalf("ResumePlaybook error = %v, want ambiguous-action rejection", err)
	}
}

func TestResumePlaybookRequiresSessionCheckpoint(t *testing.T) {
	s := newTestSession()
	s.timeout = time.Second
	pb := &Playbook{Name: "safe", Actions: []Action{{Type: "click", Selector: "#submit"}}}
	previous := &PlaybookResult{TotalSteps: 1, FailedAt: 1, StepsRun: 1, PlaybookSHA256: playbookDigest(pb), ResumeSafe: true}
	_, err := s.ResumePlaybook(pb, previous)
	if err == nil || !strings.Contains(err.Error(), "no session-bound resume token") {
		t.Fatalf("ResumePlaybook error = %v, want missing checkpoint rejection", err)
	}
}

func TestPlaybookResumeCheckpointBindsAndConsumesResult(t *testing.T) {
	s := newTestSession()
	result := &PlaybookResult{
		StepsRun: 1, TotalSteps: 2, PlaybookSHA256: strings.Repeat("a", 64),
		FailedAt: 2, ResumeSafe: true, FailurePhase: "expectation",
	}
	s.mu.Lock()
	if err := s.issuePlaybookCheckpoint(result); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	modified := *result
	modified.StepsRun = 0
	if err := s.verifyPlaybookCheckpoint(&modified); err == nil || !strings.Contains(err.Error(), "changed after replay") {
		s.mu.Unlock()
		t.Fatalf("modified checkpoint error = %v, want integrity rejection", err)
	}
	if err := s.consumePlaybookCheckpoint(result); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	if err := s.consumePlaybookCheckpoint(result); err == nil || !strings.Contains(err.Error(), "already used") {
		s.mu.Unlock()
		t.Fatalf("reused checkpoint error = %v, want one-use rejection", err)
	}
	s.mu.Unlock()
}

func TestPlaybookResumeCheckpointIsSessionBound(t *testing.T) {
	first, second := newTestSession(), newTestSession()
	result := &PlaybookResult{TotalSteps: 1, PlaybookSHA256: strings.Repeat("b", 64), ResumeSafe: true, FailurePhase: "action"}
	first.mu.Lock()
	if err := first.issuePlaybookCheckpoint(result); err != nil {
		first.mu.Unlock()
		t.Fatal(err)
	}
	first.mu.Unlock()
	second.mu.Lock()
	err := second.verifyPlaybookCheckpoint(result)
	second.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("cross-session checkpoint error = %v, want session binding rejection", err)
	}
}

func TestAction_JSON(t *testing.T) {
	tests := []struct {
		name   string
		action Action
	}{
		{"navigate", Action{Type: "navigate", Value: "https://example.com"}},
		{"click", Action{Type: "click", Selector: "#btn"}},
		{"type", Action{Type: "type", Selector: "#input", Value: "hello"}},
		{"click_label", Action{Type: "click_label", Label: 5}},
		{"fill_form", Action{Type: "fill_form_semantic", Fields: map[string]string{"Email": "ab-user"}}},
		{"with expected", Action{
			Type:     "click",
			Selector: "#login",
			Expected: &ActionExpect{URL: "https://example.com/home", Title: "Home"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.action)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got Action
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Type != tt.action.Type {
				t.Errorf("Type: got %q, want %q", got.Type, tt.action.Type)
			}
		})
	}
}
