package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// PageReadiness describes how ready a page is for interaction.
type PageReadiness struct {
	ContentTrust  string   `json:"content_trust,omitempty"`
	Score         int      `json:"score"`                  // 0-100
	State         string   `json:"state"`                  // loading, interactive, complete, spa_ready
	PendingXHR    int      `json:"pending_xhr"`            // in-flight XHR/fetch requests
	PendingImages int      `json:"pending_images"`         // images still loading
	HasSkeleton   bool     `json:"has_skeleton,omitempty"` // skeleton/placeholder elements present
	HasSpinner    bool     `json:"has_spinner,omitempty"`  // loading spinner present
	Suggestions   []string `json:"suggestions,omitempty"`  // what to wait for
}

// CheckReadiness returns the current page readiness state.
func (s *Session) CheckReadiness() (*PageReadiness, error) {
	return s.CheckReadinessContext(context.Background())
}

// CheckReadinessContext reads page readiness while honoring ctx for queueing
// and browser work.
func (s *Session) CheckReadinessContext(ctx context.Context) (*PageReadiness, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	js := `(function() {
		const r = {score: 0, state: document.readyState, pendingXHR: 0, pendingImages: 0};
		const suggestions = [];

		// Base score from readyState
		if (r.state === 'complete') r.score = 60;
		else if (r.state === 'interactive') r.score = 40;
		else r.score = 20;

		// Check pending images
		const imgs = document.querySelectorAll('img');
		for (const img of imgs) {
			if (!img.complete && img.src) r.pendingImages++;
		}
		if (r.pendingImages === 0) r.score += 10;
		else suggestions.push('Wait for ' + r.pendingImages + ' images to load');

		// Check for skeleton/placeholder elements
		const skeletons = document.querySelectorAll('[class*="skeleton"], [class*="placeholder"], [class*="shimmer"], [class*="loading"]');
		r.hasSkeleton = skeletons.length > 0;
		if (r.hasSkeleton) suggestions.push('Skeleton/placeholder elements still visible');
		else r.score += 10;

		// Check for spinners
		const spinners = document.querySelectorAll('[class*="spinner"], [class*="loading"], [role="progressbar"], .loader');
		r.hasSpinner = spinners.length > 0;
		if (r.hasSpinner) suggestions.push('Loading spinner still visible');
		else r.score += 10;

		// Check for SPA framework readiness
		const hasContent = document.body && document.body.innerText.trim().length > 100;
		if (hasContent) r.score += 10;
		else suggestions.push('Page has minimal content — SPA may still be hydrating');

		r.score = Math.min(r.score, 100);
		r.suggestions = suggestions;
		return JSON.stringify(r);
	})()`

	result, err := s.evaluateCurrentContext(ctx, js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var readiness PageReadiness
	_ = json.Unmarshal([]byte(str), &readiness)
	readiness.ContentTrust = ContentTrustUntrustedPage
	return &readiness, nil
}

// SelectorSuggestion describes a similar element when a selector fails.
type SelectorSuggestion struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Selector     string `json:"selector"`
	Tag          string `json:"tag"`
	Text         string `json:"text,omitempty"`
	ID           string `json:"id,omitempty"`
	Classes      string `json:"classes,omitempty"`
}

// SuggestSelectors finds elements similar to a failed selector.
// Called automatically when querySelector fails.
func (s *Session) SuggestSelectors(failedSelector string) ([]SelectorSuggestion, error) {
	return s.SuggestSelectorsContext(context.Background(), failedSelector)
}

// SuggestSelectorsContext finds likely replacement selectors while honoring
// ctx for session queueing, page setup, and browser evaluation.
func (s *Session) SuggestSelectorsContext(ctx context.Context, failedSelector string) ([]SelectorSuggestion, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	selectorJSON, _ := json.Marshal(failedSelector)
	js := fmt.Sprintf(`(function() {
		const failed = %s;
		const suggestions = [];

		// Extract key parts from the failed selector
		const idMatch = failed.match(/#([\w-]+)/);
		const classMatch = failed.match(/\.([\w-]+)/);
		const tagMatch = failed.match(/^(\w+)/);
		const textMatch = failed.match(/:text\(['"](.+?)['"]\)/);

		const searchTerms = [];
		if (idMatch) searchTerms.push(idMatch[1]);
		if (classMatch) searchTerms.push(classMatch[1]);
		if (textMatch) searchTerms.push(textMatch[1]);
		if (tagMatch && tagMatch[1] !== '*') searchTerms.push(tagMatch[1]);

		// Search for similar elements
		const allElements = document.querySelectorAll('a, button, input, textarea, select, [role="button"], [onclick], h1, h2, h3, label, span, div, p');

		for (const el of allElements) {
			if (suggestions.length >= 5) break;

			const id = el.id || '';
			const classes = el.className || '';
			const text = el.textContent.trim().slice(0, 60);
			const tag = el.tagName.toLowerCase();

			let match = false;
			for (const term of searchTerms) {
				const termLower = term.toLowerCase();
				if (id.toLowerCase().includes(termLower) ||
					classes.toLowerCase().includes(termLower) ||
					text.toLowerCase().includes(termLower) ||
					tag === termLower) {
					match = true;
					break;
				}
			}

			if (match && el.offsetParent !== null) {
				let selector = tag;
				if (id) selector = '#' + id;
				else if (el.name) selector = tag + '[name="' + el.name + '"]';

				suggestions.push({
					selector: selector,
					tag: tag,
					text: text,
					id: id,
					classes: typeof classes === 'string' ? classes.split(' ').slice(0, 3).join(' ') : ''
				});
			}
		}

		return JSON.stringify(suggestions);
	})()`, selectorJSON)

	result, err := s.evaluateCurrentContext(ctx, js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var suggestions []SelectorSuggestion
	_ = json.Unmarshal([]byte(str), &suggestions)
	for i := range suggestions {
		suggestions[i].ContentTrust = ContentTrustUntrustedPage
	}
	return suggestions, nil
}
