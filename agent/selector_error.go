package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	browse "go.klarlabs.de/scout"
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

// NodeErrorClass categorizes why a node-targeted action failed. Returned to
// callers as part of the decoded error so agents can react accordingly instead
// of pattern-matching raw CDP error strings.
type NodeErrorClass string

const (
	NodeErrFieldMissing NodeErrorClass = "field_missing"
	NodeErrStaleNode    NodeErrorClass = "stale_node"
	NodeErrHidden       NodeErrorClass = "hidden"
	NodeErrDisabled     NodeErrorClass = "disabled"
	NodeErrUnknown      NodeErrorClass = "unknown"
)

// NodeActionError wraps a CDP-level failure on a node-targeted action
// (type, click, fill) with a stable classification.
type NodeActionError struct {
	Selector string         `json:"selector"`
	Class    NodeErrorClass `json:"class"`
	Hint     string         `json:"hint"`
	Cause    error          `json:"-"`
}

func (e *NodeActionError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s on %q: %s", e.Class, e.Selector, e.Hint)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s on %q: %s", e.Class, e.Selector, e.Cause.Error())
	}
	return fmt.Sprintf("%s on %q", e.Class, e.Selector)
}

func (e *NodeActionError) Unwrap() error { return e.Cause }

func (e *NodeActionError) MarshalJSON() ([]byte, error) {
	type alias NodeActionError
	cause := ""
	if e.Cause != nil {
		cause = e.Cause.Error()
	}
	return json.Marshal(struct {
		Error string `json:"error"`
		Cause string `json:"cause,omitempty"`
		*alias
	}{Error: "node_action_failed", Cause: cause, alias: (*alias)(e)})
}

// decodeNodeError converts a raw CDP/Selection failure into a classified
// NodeActionError so callers (e.g. fill_form_semantic) get an actionable class
// instead of "cdp: error -32000". Caller must hold s.mu.
func (s *Session) decodeNodeError(selector string, cause error) error {
	if cause == nil {
		return nil
	}
	// Don't re-wrap already-classified errors.
	var existing *NodeActionError
	if errors.As(cause, &existing) {
		return cause
	}
	var notFound *SelectorNotFoundError
	if errors.As(cause, &notFound) {
		return cause
	}

	class := NodeErrUnknown
	hint := ""
	if browse.IsStaleNodeError(cause) {
		class = NodeErrStaleNode
		hint = "DOM reconciled between resolution and action; retried once and still stale"
	}

	// Inspect live DOM to refine classification.
	if s.page != nil && selector != "" {
		if _, err := s.page.QuerySelector(selector); err != nil {
			class = NodeErrFieldMissing
			hint = "selector does not match any element on the current page"
		} else if vis := elementVisibility(s.page, selector); vis != "" {
			switch vis {
			case "hidden":
				class = NodeErrHidden
				hint = "element exists but is hidden (display:none, visibility:hidden, or aria-hidden)"
			case "disabled":
				class = NodeErrDisabled
				hint = "element exists but is disabled — cannot be interacted with"
			}
		}
	}

	return &NodeActionError{
		Selector: selector,
		Class:    class,
		Hint:     hint,
		Cause:    cause,
	}
}

// elementVisibility returns "hidden" | "disabled" | "" depending on the live
// state of the element. Caller must hold s.mu.
func elementVisibility(page *browse.Page, selector string) string {
	selJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function(){
		const el=document.querySelector(%s);
		if(!el)return '';
		const s=window.getComputedStyle(el);
		if(s.display==='none'||s.visibility==='hidden'||s.opacity==='0')return 'hidden';
		if(el.getAttribute('aria-hidden')==='true')return 'hidden';
		if(el.disabled||el.getAttribute('aria-disabled')==='true')return 'disabled';
		return '';
	})()`, selJSON)
	r, err := page.Evaluate(js)
	if err != nil {
		return ""
	}
	s, _ := r.(string)
	return s
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
