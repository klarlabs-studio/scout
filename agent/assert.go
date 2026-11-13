package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AssertResult is returned from text/visibility assertions.
type AssertResult struct {
	ContentTrust string `json:"content_trust,omitempty"`
	OK           bool   `json:"ok"`
	TargetID     string `json:"target_id,omitempty"`
	Selector     string `json:"selector,omitempty"`
	Expected     string `json:"expected,omitempty"`
	Found        string `json:"found,omitempty"`
	Snippet      string `json:"snippet,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// AssertTextContains checks that the given text appears on the page.
// If selector is empty, searches the full body innerText. If selector is set,
// only that element's textContent is searched. Case-sensitive by default; pass
// caseInsensitive=true to fold case. Returns OK plus a short surrounding
// snippet so the caller can confirm the match landed where expected.
//
// One round-trip instead of extract → string-match in caller code.
func (s *Session) AssertTextContains(text, selector string, caseInsensitive bool) (*AssertResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.AssertTextContainsContext(ctx, text, selector, caseInsensitive)
}

// AssertTextContainsContext checks the current frame under the caller's deadline.
func (s *Session) AssertTextContainsContext(ctx context.Context, text, selector string, caseInsensitive bool) (*AssertResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	return s.assertTextContainsLockedContext(ctx, text, selector, caseInsensitive)
}

// assertTextContainsLocked performs the assertion while the caller holds s.mu.

func (s *Session) assertTextContainsLockedContext(ctx context.Context, text, selector string, caseInsensitive bool) (*AssertResult, error) {
	if text == "" {
		return nil, fmt.Errorf("assert_text_contains: text is required")
	}

	selJSON, _ := json.Marshal(selector)
	wantJSON, _ := json.Marshal(text)
	js := fmt.Sprintf(`(function(){
		const sel = %s;
		const want = %s;
		const ci = %t;
		const root = sel ? document.querySelector(sel) : document.body;
		if (!root) return JSON.stringify({ok:false, reason:'selector_not_found'});
		const txt = (root.innerText || root.textContent || '').trim();
		const hay = ci ? txt.toLowerCase() : txt;
		const needle = ci ? want.toLowerCase() : want;
		const idx = hay.indexOf(needle);
		if (idx < 0) {
			// Send back a small slice of the haystack for debugging.
			const tail = txt.slice(0, 240);
			return JSON.stringify({ok:false, reason:'not_found', snippet: tail});
		}
		const start = Math.max(0, idx - 40);
		const end = Math.min(txt.length, idx + want.length + 40);
		return JSON.stringify({ok:true, snippet: txt.slice(start, end), found: txt.slice(idx, idx+want.length)});
	})()`, string(selJSON), string(wantJSON), caseInsensitive)

	// Assertions follow the active execution context just like clicks and
	// reads. Otherwise a verified click inside an iframe is checked against
	// the parent document and is incorrectly reported as a failure.
	raw, err := s.evaluateInFrameContext(ctx, s.frameContextID, js)
	if err != nil {
		return nil, err
	}
	str, _ := raw.(string)
	var parsed struct {
		OK      bool   `json:"ok"`
		Reason  string `json:"reason"`
		Snippet string `json:"snippet"`
		Found   string `json:"found"`
	}
	_ = json.Unmarshal([]byte(str), &parsed)

	out := &AssertResult{
		ContentTrust: ContentTrustUntrustedPage,
		OK:           parsed.OK,
		TargetID:     s.page.TargetID(),
		Selector:     selector,
		Expected:     text,
		Snippet:      strings.TrimSpace(parsed.Snippet),
		Found:        parsed.Found,
		Reason:       parsed.Reason,
	}
	if !out.OK && out.Reason == "" {
		out.Reason = "not_found"
	}
	return out, nil
}

// assertTextContainsLocked performs the assertion while the caller holds s.mu.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) assertTextContainsLocked(text, selector string, caseInsensitive bool) (*AssertResult, error) {
	return s.assertTextContainsLockedContext(context.Background(), text, selector, caseInsensitive)
}
