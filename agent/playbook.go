package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	browse "github.com/felixgeelhaar/scout"
)

func newSelectionFromPage(page *browse.Page, nodeID int64, selector string) *browse.Selection {
	return browse.NewSelection(page, nodeID, selector)
}

// Action represents a single recorded browser action.
type Action struct {
	Type     string            `json:"type"`               // navigate, click, type, select, scroll, wait, extract
	Selector string            `json:"selector,omitempty"` // CSS selector or :text() selector
	Value    string            `json:"value,omitempty"`    // URL for navigate, text for type, option for select
	Label    int               `json:"label,omitempty"`    // label number for click_label
	Fields   map[string]string `json:"fields,omitempty"`   // for fill_form_semantic
	Expected *ActionExpect     `json:"expected,omitempty"` // expected outcome for replay validation
}

// ActionExpect describes the expected outcome of an action for replay validation.
type ActionExpect struct {
	URL      string `json:"url,omitempty"`      // expected URL after action
	Title    string `json:"title,omitempty"`    // expected page title
	Selector string `json:"selector,omitempty"` // element that should exist after action
	Text     string `json:"text,omitempty"`     // expected text content
}

// Playbook is a recorded sequence of browser actions that can be replayed.
type Playbook struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"` // starting URL
	Actions   []Action  `json:"actions"`
	CreatedAt time.Time `json:"created_at"`
}

// PlaybookResult is the outcome of replaying a playbook.
type PlaybookResult struct {
	Success      bool              `json:"success"`
	StepsRun     int               `json:"steps_run"`
	TotalSteps   int               `json:"total_steps"`
	FailedAt     int               `json:"failed_at,omitempty"`
	FailedAction *Action           `json:"failed_action,omitempty"`
	Error        string            `json:"error,omitempty"`
	Extracted    map[string]string `json:"extracted,omitempty"` // data from extract actions
}

// recording holds state while recording actions.
type recording struct {
	name    string
	url     string
	actions []Action
}

// StartRecordingPlaybook begins recording all agent actions into a playbook.
func (s *Session) StartRecordingPlaybook(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	url := ""
	if s.page != nil {
		url, _ = s.page.URL()
	}

	s.recording = &recording{
		name: name,
		url:  url,
	}
}

// StopRecordingPlaybook stops recording and returns the playbook.
func (s *Session) StopRecordingPlaybook() (*Playbook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recording == nil {
		return nil, fmt.Errorf("no recording in progress")
	}

	pb := &Playbook{
		Name:      s.recording.name,
		URL:       s.recording.url,
		Actions:   s.recording.actions,
		CreatedAt: time.Now(),
	}
	s.recording = nil
	return pb, nil
}

// recordAction appends an action to the current recording (if active).
// Caller must hold s.mu.
func (s *Session) recordAction(a Action) {
	if s.recording != nil {
		s.recording.actions = append(s.recording.actions, a)
	}
}

// SavePlaybook saves a playbook to a JSON file.
func SavePlaybook(pb *Playbook, path string) error {
	data, err := json.MarshalIndent(pb, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// LoadPlaybook loads a playbook from a JSON file.
func LoadPlaybook(path string) (*Playbook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pb Playbook
	if err := json.Unmarshal(data, &pb); err != nil {
		return nil, err
	}
	return &pb, nil
}

// ReplayPlaybook executes a recorded playbook deterministically.
// Returns the result including any extracted data and where it failed (if applicable).
func (s *Session) ReplayPlaybook(pb *Playbook) (*PlaybookResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	result := &PlaybookResult{
		TotalSteps: len(pb.Actions),
		Extracted:  make(map[string]string),
	}

	// Navigate to starting URL. Use the same "close blank tab, open fresh"
	// pattern as Session.Navigate — directly navigating the initial about:blank
	// tab is unreliable (Chrome can hang on the load event when redirecting
	// the very first page). We are already holding s.mu so we cannot call
	// s.Navigate; inline the equivalent sequence.
	if pb.URL != "" {
		if s.page != nil {
			_ = s.page.Close()
		}
		page, err := s.browser.NewPage()
		if err != nil {
			result.Error = fmt.Sprintf("failed to open page for %s: %v", pb.URL, err)
			return result, nil
		}
		if s.stealth {
			s.applyStealthPatches(page)
		}
		s.page = page
		s.diffInstalled = false
		s.frameID = ""
		s.frameContextID = 0
		if err := page.Navigate(pb.URL); err != nil {
			result.Error = fmt.Sprintf("failed to navigate to starting URL %s: %v", pb.URL, err)
			return result, nil
		}
	}

	for i, action := range pb.Actions {
		result.StepsRun = i + 1
		err := s.executeAction(action, result)
		if err != nil {
			result.FailedAt = i + 1
			result.FailedAction = &pb.Actions[i]
			result.Error = err.Error()
			return result, nil
		}

		// Validate expected outcome
		if action.Expected != nil {
			if err := s.validateExpect(action.Expected); err != nil {
				result.FailedAt = i + 1
				result.FailedAction = &pb.Actions[i]
				result.Error = fmt.Sprintf("expectation failed: %v", err)
				return result, nil
			}
		}
	}

	result.Success = true
	return result, nil
}

func (s *Session) executeAction(a Action, result *PlaybookResult) error {
	switch a.Type {
	case "navigate":
		return s.page.Navigate(a.Value)

	case "click":
		nodeID, err := s.resolveSelector(a.Selector)
		if err != nil {
			return fmt.Errorf("click: element %q not found: %w", a.Selector, err)
		}
		sel := newSelectionFromPage(s.page, nodeID, a.Selector)
		if err := sel.Click(); err != nil {
			return fmt.Errorf("click failed: %w", err)
		}
		_ = s.page.WaitStable(300 * time.Millisecond)
		return nil

	case "type":
		nodeID, err := s.resolveSelector(a.Selector)
		if err != nil {
			return fmt.Errorf("type: element %q not found: %w", a.Selector, err)
		}
		sel := newSelectionFromPage(s.page, nodeID, a.Selector)
		return sel.Input(a.Value)

	case "select":
		selectorJSON, _ := json.Marshal(a.Selector)
		optionJSON, _ := json.Marshal(a.Value)
		js := fmt.Sprintf(`(function() {
			const sel = document.querySelector(%s);
			if (!sel) return false;
			for (const opt of sel.options) {
				if (opt.text.trim() === %s || opt.value === %s) {
					const nativeSetter = Object.getOwnPropertyDescriptor(
						HTMLSelectElement.prototype, 'value'
					).set;
					nativeSetter.call(sel, opt.value);
					sel.dispatchEvent(new Event('input', {bubbles: true}));
					sel.dispatchEvent(new Event('change', {bubbles: true}));
					return true;
				}
			}
			return false;
		})()`, selectorJSON, optionJSON, optionJSON)
		r, err := s.page.Evaluate(js)
		if err != nil {
			return err
		}
		if b, ok := r.(bool); !ok || !b {
			return fmt.Errorf("select: option %q not found in %s", a.Value, a.Selector)
		}
		return nil

	case "scroll":
		selectorJSON, _ := json.Marshal(a.Selector)
		js := fmt.Sprintf(`(function() {
			const el = document.querySelector(%s);
			if (!el) return false;
			el.scrollIntoView({behavior:'instant', block:'center'});
			return true;
		})()`, selectorJSON)
		_, err := s.page.Evaluate(js)
		return err

	case "wait":
		return s.page.WaitForSelector(a.Selector)

	case "fill_form":
		if a.Fields == nil {
			return fmt.Errorf("fill_form: no fields specified")
		}
		for selector, value := range a.Fields {
			nodeID, err := s.resolveSelector(selector)
			if err != nil {
				return fmt.Errorf("fill_form: field %q not found: %w", selector, err)
			}
			sel := newSelectionFromPage(s.page, nodeID, selector)
			if err := sel.Input(value); err != nil {
				return fmt.Errorf("fill_form: failed to fill %q: %w", selector, err)
			}
		}
		return nil

	case "extract":
		nodeID, err := s.resolveSelector(a.Selector)
		if err != nil {
			return fmt.Errorf("extract: element %q not found: %w", a.Selector, err)
		}
		sel := newSelectionFromPage(s.page, nodeID, a.Selector)
		text, err := sel.Text()
		if err != nil {
			return err
		}
		key := a.Selector
		if a.Value != "" {
			key = a.Value // use value as the result key name
		}
		result.Extracted[key] = text
		return nil

	default:
		return fmt.Errorf("unknown action type: %s", a.Type)
	}
}

func (s *Session) validateExpect(expect *ActionExpect) error {
	if expect.URL != "" {
		url, _ := s.page.URL()
		if url != expect.URL {
			return fmt.Errorf("expected URL %q, got %q", expect.URL, url)
		}
	}
	if expect.Title != "" {
		title, _ := s.page.Evaluate(`document.title`)
		titleStr, _ := title.(string)
		if titleStr != expect.Title {
			return fmt.Errorf("expected title %q, got %q", expect.Title, titleStr)
		}
	}
	if expect.Selector != "" {
		_, err := s.resolveSelector(expect.Selector)
		if err != nil {
			return fmt.Errorf("expected element %q not found", expect.Selector)
		}
	}
	return nil
}
