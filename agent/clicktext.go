package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ClickTextResult describes which element ClickText resolved to.
type ClickTextResult struct {
	URL        string            `json:"url"`
	Title      string            `json:"title"`
	Matched    string            `json:"matched_selector"`
	MatchType  string            `json:"match_type"` // aria_label, button_text, link_text, text_descendant
	Candidates []ClickTextOption `json:"candidates,omitempty"`
}

// ClickTextOption describes one candidate when ClickText finds multiple matches.
type ClickTextOption struct {
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Role     string `json:"role,omitempty"`
}

// ClickText clicks an element by its visible text. Resolution order:
//  1. Exact aria-label match
//  2. button/a/[role=button] visible-text match (case-insensitive, exact)
//  3. visible-text match anywhere then climb to closest interactive ancestor
//
// role narrows step 2 to a specific role ("button" or "link"). Pass "" to disable.
// Returns an error with structured candidates when multiple elements share the text.
func (s *Session) ClickText(text, role string) (*ClickTextResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("click_text: text is required")
	}

	textJSON, _ := json.Marshal(text)
	roleJSON, _ := json.Marshal(strings.ToLower(role))

	js := fmt.Sprintf(`(function() {
		const wanted = %s;
		const wantedRole = %s;
		const wantedLower = wanted.toLowerCase();
		const seen = new Set();   // dedupe by element identity, never by weak selector
		const matches = [];       // {el, type} in match order
		function add(el, type) { if (el && !seen.has(el)) { seen.add(el); matches.push({el: el, type: type}); } }

		function visible(el) {
			if (!el) return false;
			const r = el.getBoundingClientRect();
			if (r.width < 1 || r.height < 1) return false;
			const s = window.getComputedStyle(el);
			return s.display !== 'none' && s.visibility !== 'hidden' && s.opacity !== '0';
		}
		function describe(el, type) {
			let sel = el.tagName.toLowerCase();
			if (el.id) sel = '#' + el.id;
			else if (el.name) sel = sel + '[name="' + el.name + '"]';
			return {
				selector: sel,
				tag: el.tagName.toLowerCase(),
				text: (el.textContent || el.value || '').trim().slice(0, 80),
				role: el.getAttribute('role') || '',
				match_type: type
			};
		}

		// 1. exact aria-label match
		for (const el of document.querySelectorAll('[aria-label]')) {
			if (!visible(el)) continue;
			if ((el.getAttribute('aria-label') || '').trim().toLowerCase() === wantedLower) {
				add(el, 'aria_label');
			}
		}

		// 2. button/a/[role=button] visible text exact match
		const tagFilter = wantedRole === 'button' ? 'button,[role=button],input[type=button],input[type=submit]'
			: wantedRole === 'link' ? 'a[href],[role=link]'
			: 'button,a[href],[role=button],[role=link],input[type=button],input[type=submit]';
		for (const el of document.querySelectorAll(tagFilter)) {
			if (!visible(el)) continue;
			const t = (el.textContent || el.value || '').trim();
			if (t.toLowerCase() === wantedLower) {
				add(el, el.tagName.toLowerCase() === 'a' ? 'link_text' : 'button_text');
			}
		}

		// 3. text node match → closest interactive ancestor
		if (matches.length === 0) {
			const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, null);
			while (walker.nextNode()) {
				const node = walker.currentNode;
				const t = (node.textContent || '').trim();
				if (t.toLowerCase() !== wantedLower) continue;
				let el = node.parentElement;
				while (el) {
					if (el.matches('a[href],button,[role=button],[role=link],input[type=button],input[type=submit],[onclick],[tabindex]')) {
						if (visible(el)) add(el, 'text_descendant');
						break;
					}
					el = el.parentElement;
				}
			}
		}

		if (matches.length === 0) return JSON.stringify({matched: false, candidates: []});

		// One descriptor per DISTINCT matched element (identity-deduped above).
		const out = matches.map(m => describe(m.el, m.type));

		// More than one distinct element shares the text — ambiguous; do not click.
		if (matches.length > 1) {
			return JSON.stringify({matched: true, candidates: out, match_type: matches[0].type, selector: out[0].selector});
		}

		// Exactly one match — mark that element directly (no fragile selector re-query).
		matches[0].el.setAttribute('data-scout-clicktext', '1');
		return JSON.stringify({matched: true, candidates: out, match_type: matches[0].type, selector: out[0].selector});
	})()`, textJSON, roleJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("click_text evaluate: %w", err)
	}
	str, _ := result.(string)
	var resp struct {
		Matched    bool              `json:"matched"`
		Candidates []ClickTextOption `json:"candidates"`
		MatchType  string            `json:"match_type"`
		Selector   string            `json:"selector"`
	}
	if err := json.Unmarshal([]byte(str), &resp); err != nil {
		return nil, fmt.Errorf("click_text parse: %w", err)
	}
	if !resp.Matched {
		return nil, &SelectorNotFoundError{
			Selector:  fmt.Sprintf("text=%q", text),
			Matched:   0,
			PageTitle: pageTitleSafe(s),
		}
	}
	if len(resp.Candidates) > 1 {
		return &ClickTextResult{
			Matched:    resp.Selector,
			MatchType:  resp.MatchType,
			Candidates: resp.Candidates,
		}, fmt.Errorf("click_text: %d elements match %q (pass role= to disambiguate)", len(resp.Candidates), text)
	}

	// Click the marked element via real DOM event sequence to fire framework handlers.
	clickJS := `(function() {
		const el = document.querySelector('[data-scout-clicktext]');
		if (!el) return false;
		el.removeAttribute('data-scout-clicktext');
		el.scrollIntoView({block: 'center', inline: 'center'});
		const rect = el.getBoundingClientRect();
		const cx = rect.left + rect.width / 2;
		const cy = rect.top + rect.height / 2;
		for (const type of ['mousedown', 'mouseup', 'click']) {
			el.dispatchEvent(new MouseEvent(type, {bubbles: true, cancelable: true, clientX: cx, clientY: cy, view: window}));
		}
		return true;
	})()`
	if _, err := s.page.Evaluate(clickJS); err != nil {
		return nil, fmt.Errorf("click_text dispatch: %w", err)
	}
	_ = s.page.WaitStable(300 * time.Millisecond)

	url, _ := s.page.URL()
	titleAny, _ := s.page.Evaluate(`document.title`)
	title, _ := titleAny.(string)
	s.recordAction(Action{Type: "click", Selector: "text=" + text})
	s.addHistory("click_text", "text="+text, "", "")

	return &ClickTextResult{
		URL:       url,
		Title:     title,
		Matched:   resp.Selector,
		MatchType: resp.MatchType,
	}, nil
}

func pageTitleSafe(s *Session) string {
	if s.page == nil {
		return ""
	}
	t, err := s.page.Evaluate(`document.title`)
	if err != nil {
		return ""
	}
	str, _ := t.(string)
	return str
}
