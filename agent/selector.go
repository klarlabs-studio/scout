package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var textSelectorRe = regexp.MustCompile(`:text\(['"](.+?)['"]\)`)
var hasTextSelectorRe = regexp.MustCompile(`:has-text\(['"](.+?)['"]\)`)

// resolveSelector translates Playwright-style selectors to JS-based element lookup
// when standard CSS selectors won't work. Falls back to the original selector if
// it looks like valid CSS.
func (s *Session) resolveSelector(selector string) (int64, error) {
	// Try standard CSS first
	nodeID, err := s.page.QuerySelector(selector)
	if err == nil {
		return nodeID, nil
	}

	// Standard CSS missed — try piercing through shadow roots before
	// falling through to text / NL search. Recovers Lit / Vue / React
	// custom elements where the actionable <input>, <button>, etc.
	// lives inside a shadow root and is invisible to
	// document.querySelector.
	if pNodeID, pErr := s.page.QuerySelectorPiercing(selector); pErr == nil && pNodeID != 0 {
		return pNodeID, nil
	}

	// Check for Playwright :text('...') syntax
	if matches := textSelectorRe.FindStringSubmatch(selector); len(matches) > 1 {
		text := matches[1]
		tag := strings.TrimSuffix(selector[:textSelectorRe.FindStringIndex(selector)[0]], " ")
		if tag == "" {
			tag = "*"
		}
		return s.findByText(tag, text)
	}

	// Check for :has-text('...')
	if matches := hasTextSelectorRe.FindStringSubmatch(selector); len(matches) > 1 {
		text := matches[1]
		tag := strings.TrimSuffix(selector[:hasTextSelectorRe.FindStringIndex(selector)[0]], " ")
		if tag == "" {
			tag = "*"
		}
		return s.findByText(tag, text)
	}

	// Try natural language selection for prompts that don't look like CSS selectors
	if looksLikeNaturalLanguage(selector) {
		nlResult, nlErr := s.selectByPromptInternal(selector)
		if nlErr == nil && nlResult.Confidence >= 0.4 {
			nodeID, resolveErr := s.page.QuerySelector(nlResult.Selector)
			if resolveErr == nil {
				return nodeID, nil
			}
		}
	}

	// Element not found — try to provide helpful suggestions
	suggestions, sugErr := s.suggestSelectorsInternal(selector)
	if sugErr == nil && len(suggestions) > 0 {
		hint := fmt.Sprintf("element %q not found. Did you mean:", selector)
		for i, sg := range suggestions {
			if i >= 3 {
				break
			}
			hint += fmt.Sprintf(" %s (%s, %q)", sg.Selector, sg.Tag, sg.Text)
			if i < 2 && i < len(suggestions)-1 {
				hint += ","
			}
		}
		return 0, fmt.Errorf("%s", hint)
	}

	return 0, err
}

// suggestSelectorsInternal is the non-locking version of SuggestSelectors.
func (s *Session) suggestSelectorsInternal(failedSelector string) ([]SelectorSuggestion, error) {
	selectorJSON, _ := json.Marshal(failedSelector)
	js := fmt.Sprintf(`(function() {
		const failed = %s;
		const suggestions = [];
		const idMatch = failed.match(/#([\w-]+)/);
		const classMatch = failed.match(/\.([\w-]+)/);
		const tagMatch = failed.match(/^(\w+)/);
		const textMatch = failed.match(/:text\(['"](.+?)['"]\)/);
		const terms = [];
		if (idMatch) terms.push(idMatch[1]);
		if (classMatch) terms.push(classMatch[1]);
		if (textMatch) terms.push(textMatch[1]);
		if (tagMatch && tagMatch[1] !== '*') terms.push(tagMatch[1]);
		for (const el of document.querySelectorAll('a,button,input,textarea,select,[role=button],h1,h2,h3,label')) {
			if (suggestions.length >= 3) break;
			const id = el.id||''; const cls = el.className||''; const text = el.textContent.trim().slice(0,60); const tag = el.tagName.toLowerCase();
			for (const t of terms) {
				const tl = t.toLowerCase();
				if ((id+cls+text+tag).toLowerCase().includes(tl) && el.offsetParent !== null) {
					suggestions.push({selector: id ? '#'+id : (el.name ? tag+'[name="'+el.name+'"]' : tag), tag, text, id, classes: typeof cls === 'string' ? cls.split(' ').slice(0,2).join(' ') : ''});
					break;
				}
			}
		}
		return JSON.stringify(suggestions);
	})()`, selectorJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	str, _ := result.(string)
	var suggestions []SelectorSuggestion
	_ = json.Unmarshal([]byte(str), &suggestions)
	return suggestions, nil
}

// findByText finds an element by tag and text content via JS.
func (s *Session) findByText(tag, text string) (int64, error) {
	tagJSON, _ := json.Marshal(tag)
	textJSON, _ := json.Marshal(text)

	// Use XPath to find by text, then resolve to nodeId via DOM.querySelector workaround
	js := fmt.Sprintf(`(function() {
		const tag = %s;
		const text = %s;
		const elements = document.querySelectorAll(tag === '*' ? 'a,button,span,div,p,li,h1,h2,h3,h4,label,input,td,th' : tag);
		for (const el of elements) {
			if (el.textContent.trim() === text || el.textContent.trim().includes(text)) {
				// Generate a unique selector for this element
				el.setAttribute('data-scout-found', 'true');
				return true;
			}
		}
		return false;
	})()`, tagJSON, textJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return 0, err
	}
	if b, ok := result.(bool); !ok || !b {
		return 0, fmt.Errorf("no element found with text %q", text)
	}

	// Now query the marked element
	nodeID, err := s.page.QuerySelector("[data-scout-found]")
	if err != nil {
		return 0, err
	}

	// Clean up the marker
	_, _ = s.page.Evaluate(`document.querySelector('[data-scout-found]')?.removeAttribute('data-scout-found')`)

	return nodeID, nil
}
