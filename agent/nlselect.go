package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SelectByPrompt finds an element using natural language matching against visible
// text, aria-label, placeholder, title, and other accessibility attributes.
// Returns the best match with a confidence score and up to 3 candidates.
func (s *Session) SelectByPrompt(prompt string) (*PromptSelectResult, error) {
	return s.SelectByPromptContext(context.Background(), prompt)
}

// SelectByPromptContext finds a matching element while honoring ctx for
// session queueing, page setup, and browser evaluation.
func (s *Session) SelectByPromptContext(ctx context.Context, prompt string) (*PromptSelectResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	return s.selectByPromptInternalContext(ctx, prompt)
}

func (s *Session) selectByPromptInternal(prompt string) (*PromptSelectResult, error) {
	return s.selectByPromptInternalContext(context.Background(), prompt)
}

func (s *Session) selectByPromptInternalContext(ctx context.Context, prompt string) (*PromptSelectResult, error) {
	promptJSON, _ := json.Marshal(strings.TrimSpace(prompt))

	js := fmt.Sprintf(`(function() {
		const prompt = %s;
		const keywords = prompt.toLowerCase().split(/\s+/).filter(k => k.length > 0);
		const candidates = [];

		const selectors = 'a[href], button, input, textarea, select, [role="button"], [role="link"], [onclick]';
		for (const el of document.querySelectorAll(selectors)) {
			const rect = el.getBoundingClientRect();
			if (rect.width < 1 || rect.height < 1) continue;
			const style = window.getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') continue;

			const tag = el.tagName.toLowerCase();
			const text = (tag === 'input' || tag === 'textarea')
				? (el.value || '').trim()
				: (el.textContent || '').trim().slice(0, 200);
			const ariaLabel = el.getAttribute('aria-label') || '';
			const placeholder = el.placeholder || '';
			const title = el.title || '';
			const alt = el.getAttribute('alt') || '';
			const role = el.getAttribute('role') || '';

			const textLower = text.toLowerCase();
			const promptLower = prompt.toLowerCase();
			let score = 0;

			if (textLower === promptLower) {
				score = 100;
			} else if (ariaLabel.toLowerCase() === promptLower) {
				score = 95;
			} else if (keywords.length > 0 && keywords.every(k => textLower.includes(k))) {
				score = 80;
			} else if (keywords.length > 0 && keywords.every(k => ariaLabel.toLowerCase().includes(k))) {
				score = 70;
			} else if (keywords.length > 0 && keywords.every(k => placeholder.toLowerCase().includes(k))) {
				score = 60;
			} else if (keywords.length > 0 && keywords.every(k => title.toLowerCase().includes(k))) {
				score = 55;
			} else if (keywords.length > 0 && keywords.every(k => alt.toLowerCase().includes(k))) {
				score = 50;
			} else {
				let matched = 0;
				const haystack = (text + ' ' + ariaLabel + ' ' + placeholder + ' ' + title + ' ' + alt).toLowerCase();
				for (const k of keywords) {
					if (haystack.includes(k)) matched++;
				}
				if (matched > 0) {
					score = Math.round(40 * (matched / keywords.length));
				}
			}

			if (score === 0) continue;

			let selector = tag;
			if (el.id) {
				selector = '#' + CSS.escape(el.id);
			} else if (el.name) {
				selector = tag + '[name="' + el.name + '"]';
			} else {
				const parent = el.parentElement;
				if (parent) {
					const siblings = Array.from(parent.children).filter(c => c.tagName === el.tagName);
					if (siblings.length === 1) {
						let parentSel = parent.tagName.toLowerCase();
						if (parent.id) parentSel = '#' + CSS.escape(parent.id);
						selector = parentSel + ' > ' + tag;
					} else {
						const idx = siblings.indexOf(el) + 1;
						let parentSel = parent.tagName.toLowerCase();
						if (parent.id) parentSel = '#' + CSS.escape(parent.id);
						selector = parentSel + ' > ' + tag + ':nth-of-type(' + idx + ')';
					}
				}
			}

			candidates.push({
				selector: selector,
				text: text.slice(0, 100),
				tag: tag,
				role: role,
				score: score
			});
		}

		candidates.sort((a, b) => b.score - a.score);
		return JSON.stringify(candidates.slice(0, 3));
	})()`, promptJSON)

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, fmt.Errorf("select by prompt failed: %w", err)
	}

	str, _ := result.(string)
	var candidates []struct {
		Selector string  `json:"selector"`
		Text     string  `json:"text"`
		Tag      string  `json:"tag"`
		Role     string  `json:"role"`
		Score    float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(str), &candidates); err != nil {
		return nil, fmt.Errorf("failed to parse candidates: %w", err)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no element found matching prompt %q", prompt)
	}

	best := candidates[0]
	psr := &PromptSelectResult{
		TargetID:     s.page.TargetID(),
		ContentTrust: ContentTrustUntrustedPage,
		Selector:     best.Selector,
		Text:         best.Text,
		Tag:          best.Tag,
		Role:         best.Role,
		Confidence:   best.Score / 100.0,
	}

	for _, c := range candidates {
		psr.Candidates = append(psr.Candidates, PromptCandidate{
			ContentTrust: ContentTrustUntrustedPage,
			Selector:     c.Selector,
			Text:         c.Text,
			Score:        c.Score,
		})
	}

	return psr, nil
}

// looksLikeNaturalLanguage returns true if the string does not look like a CSS
// selector — it contains spaces and none of the typical CSS selector characters.
func looksLikeNaturalLanguage(s string) bool {
	if !strings.Contains(s, " ") {
		return false
	}
	for _, ch := range s {
		switch ch {
		case '#', '.', '[', ']', ':', '>', '+', '~':
			return false
		}
	}
	return true
}
