package agent

import (
	"encoding/json"
	"fmt"
	"time"
)

// NavigationOutcome reports the result of a WaitForNavigation call.
type NavigationOutcome struct {
	Mode            string `json:"mode"`              // "full" | "spa" | "any"
	URL             string `json:"url"`               // final URL
	Title           string `json:"title"`             // final title
	URLChanged      bool   `json:"url_changed"`       // location.href moved
	FullNavigation  bool   `json:"full_navigation"`   // document was replaced
	SPANavigation   bool   `json:"spa_navigation"`    // History API mutation observed
	IdleAfterChange bool   `json:"idle_after_change"` // network settled following the change
	TimedOut        bool   `json:"timed_out"`
	ElapsedMillis   int64  `json:"elapsed_ms"`
}

// WaitForNavigation returns when the page has navigated, either via a
// full document load or a History API mutation (SPA route change).
//
// mode:
//   - "full" — only full document navigations count
//   - "spa"  — only SPA route changes count (History API push/replace/popstate)
//   - "any"  — first of either wins (default when empty)
//
// timeoutMs caps the wait; zero means default 5000ms.
//
// Click({wait:true}) is the right tool when you're about to do the
// click yourself; this is for the case where some other action
// (button outside scout's control, JS-triggered route change) is
// what moves the page.
func (s *Session) WaitForNavigation(mode string, timeoutMs int) (*NavigationOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	if mode == "" {
		mode = "any"
	}
	switch mode {
	case "full", "spa", "any":
	default:
		return nil, fmt.Errorf("wait_for_navigation: mode must be full|spa|any, got %q", mode)
	}
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}
	modeJSON, _ := json.Marshal(mode)
	js := fmt.Sprintf(`(function() {
		const mode = %s;
		const startURL = location.href;
		const startTime = performance.now();
		return new Promise(function(resolve) {
			let resolved = false;
			let timer = null;
			let fullObserved = false;
			let spaObserved = false;

			function finish(reason, url) {
				if (resolved) return;
				resolved = true;
				if (timer) clearTimeout(timer);
				cleanup();
				resolve({
					reason: reason,
					url: url || location.href,
					full: fullObserved,
					spa: spaObserved,
					elapsed: Math.round(performance.now() - startTime),
				});
			}

			const origPush = history.pushState;
			const origReplace = history.replaceState;
			function onSPA() {
				spaObserved = true;
				if (mode === 'spa' || mode === 'any') {
					// brief idle wait so the framework can mount the new route
					setTimeout(function() { finish('spa', location.href); }, 50);
				}
			}
			function onPopState() { onSPA(); }
			history.pushState = function() {
				const r = origPush.apply(this, arguments);
				onSPA();
				return r;
			};
			history.replaceState = function() {
				const r = origReplace.apply(this, arguments);
				onSPA();
				return r;
			};
			window.addEventListener('popstate', onPopState, true);

			function onFullLoad() {
				fullObserved = true;
				if (mode === 'full' || mode === 'any') finish('full', location.href);
			}
			window.addEventListener('load', onFullLoad, { once: true, capture: true });

			function cleanup() {
				history.pushState = origPush;
				history.replaceState = origReplace;
				window.removeEventListener('popstate', onPopState, true);
				window.removeEventListener('load', onFullLoad, true);
			}

			timer = setTimeout(function() { finish('timeout', location.href); }, %d);
		});
	})()`, modeJSON, timeoutMs)

	start := time.Now()
	// Evaluate awaits the returned Promise on the CDP side, so the
	// in-page setTimeout is what enforces the budget.
	result, err := s.page.Evaluate(js)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return nil, fmt.Errorf("wait_for_navigation: %w", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("wait_for_navigation: unexpected result type %T", result)
	}

	reason, _ := m["reason"].(string)
	url, _ := m["url"].(string)
	out := &NavigationOutcome{
		Mode:          mode,
		URL:           url,
		URLChanged:    url != "",
		ElapsedMillis: elapsed,
	}
	if v, ok := m["full"].(bool); ok {
		out.FullNavigation = v
	}
	if v, ok := m["spa"].(bool); ok {
		out.SPANavigation = v
	}
	if reason == "timeout" {
		out.TimedOut = true
	} else {
		// Pull the latest title via a quick eval — saves the caller a
		// second hop and confirms the new page has actually rendered.
		if titleResult, err := s.page.Evaluate(`document.title`); err == nil {
			if t, ok := titleResult.(string); ok {
				out.Title = t
			}
		}
		// Best-effort idle wait so a follow-up observe doesn't race
		// the framework's mount path. Caller-visible via IdleAfterChange.
		if err := s.page.WaitStable(400 * time.Millisecond); err == nil {
			out.IdleAfterChange = true
		}
	}
	return out, nil
}
