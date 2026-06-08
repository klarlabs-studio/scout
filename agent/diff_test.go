package agent

import (
	"strings"
	"testing"
)

func TestClassifyDiff_ModalAppeared(t *testing.T) {
	tests := []struct {
		name          string
		diff          *DOMDiff
		wantClass     string
		wantSubstring string
	}{
		{
			name: "dialog tag added",
			diff: &DOMDiff{
				Added: []DOMElement{{Tag: "dialog", Text: "Confirm action"}},
			},
			wantClass:     "modal_appeared",
			wantSubstring: "Confirm action",
		},
		{
			name: "modal class added",
			diff: &DOMDiff{
				Added: []DOMElement{{Tag: "div", Classes: "modal-overlay", Text: "Login Required"}},
			},
			wantClass:     "modal_appeared",
			wantSubstring: "Login Required",
		},
		{
			name: "dialog class added",
			diff: &DOMDiff{
				Added: []DOMElement{{Tag: "div", Classes: "dialog-wrapper", Text: "Prompt"}},
			},
			wantClass:     "modal_appeared",
			wantSubstring: "Prompt",
		},
		{
			name: "overlay class added",
			diff: &DOMDiff{
				Added: []DOMElement{{Tag: "div", Classes: "overlay-container"}},
			},
			wantClass: "modal_appeared",
		},
		{
			name: "popup class added",
			diff: &DOMDiff{
				Added: []DOMElement{{Tag: "div", Classes: "popup-message", Text: "Welcome"}},
			},
			wantClass: "modal_appeared",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, summary := classifyDiff(tt.diff)
			if cls != tt.wantClass {
				t.Errorf("classification: got %q, want %q", cls, tt.wantClass)
			}
			if tt.wantSubstring != "" && !contains(summary, tt.wantSubstring) {
				t.Errorf("summary %q should contain %q", summary, tt.wantSubstring)
			}
		})
	}
}

func TestClassifyDiff_FormError(t *testing.T) {
	tests := []struct {
		name string
		diff *DOMDiff
	}{
		{
			name: "error class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "span", Classes: "error-message", Text: "Invalid email"}}},
		},
		{
			name: "alert-danger class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Classes: "alert-danger", Text: "Something went wrong"}}},
		},
		{
			name: "invalid class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "p", Classes: "field-invalid", Text: "Required"}}},
		},
		{
			name: "error in text",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Text: "Error: bad input"}}},
		},
		{
			name: "invalid in text",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Text: "invalid password"}}},
		},
		{
			name: "failed in text",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Text: "Login failed"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, _ := classifyDiff(tt.diff)
			if cls != "form_error" {
				t.Errorf("classification: got %q, want %q", cls, "form_error")
			}
		})
	}
}

func TestClassifyDiff_Notification(t *testing.T) {
	tests := []struct {
		name string
		diff *DOMDiff
		want string
	}{
		{
			name: "success class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Classes: "success", Text: "Saved!"}}},
			want: "notification",
		},
		{
			name: "alert-success class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Classes: "alert-success", Text: "Created"}}},
			want: "notification",
		},
		{
			name: "success in text",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "p", Text: "Operation success"}}},
			want: "notification",
		},
		{
			name: "saved in text",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "p", Text: "Changes saved"}}},
			want: "notification",
		},
		{
			name: "created in text",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "p", Text: "Account created"}}},
			want: "notification",
		},
		{
			name: "updated in text",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "span", Text: "Profile updated"}}},
			want: "notification",
		},
		{
			name: "toast class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Classes: "toast-notification", Text: "Done"}}},
			want: "notification",
		},
		{
			name: "snackbar class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Classes: "snackbar", Text: "Item added"}}},
			want: "notification",
		},
		{
			name: "notification class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Classes: "notification-bar", Text: "New message"}}},
			want: "notification",
		},
		{
			name: "alert class",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "div", Classes: "alert-info", Text: "FYI"}}},
			want: "notification",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, _ := classifyDiff(tt.diff)
			if cls != tt.want {
				t.Errorf("classification: got %q, want %q", cls, tt.want)
			}
		})
	}
}

func TestClassifyDiff_LoadingComplete(t *testing.T) {
	tests := []struct {
		name string
		diff *DOMDiff
	}{
		{
			name: "spinner removed",
			diff: &DOMDiff{Removed: []DOMElement{{Tag: "div", Classes: "spinner"}}},
		},
		{
			name: "loading removed",
			diff: &DOMDiff{Removed: []DOMElement{{Tag: "div", Classes: "loading-indicator"}}},
		},
		{
			name: "skeleton removed",
			diff: &DOMDiff{Removed: []DOMElement{{Tag: "div", Classes: "skeleton-card"}}},
		},
		{
			name: "placeholder removed",
			diff: &DOMDiff{Removed: []DOMElement{{Tag: "div", Classes: "placeholder-line"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, summary := classifyDiff(tt.diff)
			if cls != "loading_complete" {
				t.Errorf("classification: got %q, want %q", cls, "loading_complete")
			}
			if summary != "Loading indicator removed" {
				t.Errorf("summary: got %q", summary)
			}
		})
	}
}

func TestClassifyDiff_ContentLoaded(t *testing.T) {
	added := make([]DOMElement, 6)
	for i := range added {
		added[i] = DOMElement{Tag: "div", Text: "content"}
	}
	diff := &DOMDiff{Added: added}
	cls, summary := classifyDiff(diff)
	if cls != "content_loaded" {
		t.Errorf("classification: got %q, want %q", cls, "content_loaded")
	}
	if !contains(summary, "6 elements added") {
		t.Errorf("summary: got %q", summary)
	}
}

func TestClassifyDiff_ElementStateChanged(t *testing.T) {
	tests := []struct {
		name string
		diff *DOMDiff
	}{
		{
			name: "disabled attribute",
			diff: &DOMDiff{Modified: []DOMChange{{Tag: "button", Attribute: "disabled", ChangeType: "attribute"}}},
		},
		{
			name: "aria-disabled attribute",
			diff: &DOMDiff{Modified: []DOMChange{{Tag: "input", Attribute: "aria-disabled", ChangeType: "attribute"}}},
		},
		{
			name: "class attribute",
			diff: &DOMDiff{Modified: []DOMChange{{Tag: "div", Attribute: "class", OldValue: "a", NewValue: "b", ChangeType: "attribute"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, _ := classifyDiff(tt.diff)
			if cls != "element_state_changed" {
				t.Errorf("classification: got %q, want %q", cls, "element_state_changed")
			}
		})
	}
}

func TestClassifyDiff_MinorUpdate(t *testing.T) {
	tests := []struct {
		name string
		diff *DOMDiff
	}{
		{
			name: "few elements added",
			diff: &DOMDiff{Added: []DOMElement{{Tag: "span", Text: "new"}, {Tag: "span", Text: "text"}}},
		},
		{
			name: "only modifications with non-special attrs",
			diff: &DOMDiff{Modified: []DOMChange{{Tag: "div", Attribute: "data-count", ChangeType: "attribute"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cls, _ := classifyDiff(tt.diff)
			if cls != "minor_update" {
				t.Errorf("classification: got %q, want %q", cls, "minor_update")
			}
		})
	}
}

func TestClassifyDiff_EmptyDiff(t *testing.T) {
	diff := &DOMDiff{}
	cls, summary := classifyDiff(diff)
	if cls != "minor_update" {
		t.Errorf("classification: got %q, want %q", cls, "minor_update")
	}
	if summary != "0 modifications" {
		t.Errorf("summary: got %q", summary)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"short", "hello", "hello"},
		{"exactly limit", strings.Repeat("a", truncateLen), strings.Repeat("a", truncateLen)},
		{"over limit", strings.Repeat("a", truncateLen+5), strings.Repeat("a", truncateLen) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input)
			if got != tt.want {
				t.Errorf("truncate(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseDOMElement(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		want DOMElement
	}{
		{
			name: "all fields",
			m:    map[string]any{"tag": "div", "id": "foo", "classes": "bar baz", "text": "hello"},
			want: DOMElement{Tag: "div", ID: "foo", Classes: "bar baz", Text: "hello"},
		},
		{
			name: "missing fields",
			m:    map[string]any{"tag": "span"},
			want: DOMElement{Tag: "span"},
		},
		{
			name: "empty map",
			m:    map[string]any{},
			want: DOMElement{},
		},
		{
			name: "wrong types ignored",
			m:    map[string]any{"tag": 123, "id": true},
			want: DOMElement{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDOMElement(tt.m)
			if got != tt.want {
				t.Errorf("parseDOMElement: got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && containsImpl(s, sub)))
}

func containsImpl(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
