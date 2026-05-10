package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SelectorNotFoundError is returned when a selector cannot be resolved.
// It carries diagnostic context (similar elements, page title, h1) so
// callers don't need a follow-up observe call to figure out what went wrong.
type SelectorNotFoundError struct {
	Selector  string               `json:"selector"`
	Matched   int                  `json:"matched"`
	Similar   []SelectorSuggestion `json:"similar,omitempty"`
	PageTitle string               `json:"page_title,omitempty"`
	PageH1    string               `json:"page_h1,omitempty"`
	Cause     error                `json:"-"`
}

func (e *SelectorNotFoundError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "selector %q not found (matched=%d)", e.Selector, e.Matched)
	if e.PageTitle != "" {
		fmt.Fprintf(&b, " on %q", e.PageTitle)
	}
	if e.PageH1 != "" {
		fmt.Fprintf(&b, " [h1=%q]", e.PageH1)
	}
	if len(e.Similar) > 0 {
		b.WriteString(". Similar:")
		for i, sg := range e.Similar {
			if i >= 3 {
				break
			}
			fmt.Fprintf(&b, " %s", sg.Selector)
			if sg.Text != "" {
				fmt.Fprintf(&b, " (%q)", sg.Text)
			}
		}
	}
	return b.String()
}

func (e *SelectorNotFoundError) Unwrap() error { return e.Cause }

// MarshalJSON exposes the structured fields plus the rendered message,
// which is what surfaces through MCP error paths.
func (e *SelectorNotFoundError) MarshalJSON() ([]byte, error) {
	type alias SelectorNotFoundError
	return json.Marshal(struct {
		Error string `json:"error"`
		*alias
	}{Error: "selector_not_found", alias: (*alias)(e)})
}

// enrichSelectorError augments a raw selector failure with diagnostic context.
// Caller must hold s.mu.
func (s *Session) enrichSelectorError(selector string, cause error) error {
	if cause == nil {
		return nil
	}
	var existing *SelectorNotFoundError
	if errors.As(cause, &existing) {
		return cause
	}

	enriched := &SelectorNotFoundError{
		Selector: selector,
		Matched:  0,
		Cause:    cause,
	}

	if s.page != nil {
		if suggestions, err := s.suggestSelectorsInternal(selector); err == nil {
			enriched.Similar = suggestions
		}
		if t, err := s.page.Evaluate(`document.title`); err == nil {
			if str, ok := t.(string); ok {
				enriched.PageTitle = str
			}
		}
		if h, err := s.page.Evaluate(`(function(){const h=document.querySelector('h1');return h?h.textContent.trim().slice(0,120):'';})()`); err == nil {
			if str, ok := h.(string); ok {
				enriched.PageH1 = str
			}
		}
	}
	return enriched
}
