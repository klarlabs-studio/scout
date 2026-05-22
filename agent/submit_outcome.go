package agent

import "fmt"

// SubmitOutcome captures the structured aftermath of the most recent
// form submission or submit-button click on the page. Designed for
// the diagnostic gap in issue #21: when a form's @submit.prevent
// runs and the client-side validation surfaces a [role=alert] toast,
// every other scout signal (failed_requests, console_errors, observe)
// comes back empty — the agent has no way to tell whether the submit
// fired but was rejected, fired and 4xx'd, or never fired at all.
//
// Fields are populated lazily: alerts and invalid fields are scanned
// at read time, DefaultPrevented + XHRCount come from a page-side
// tracker installed on first read.
type SubmitOutcome struct {
	// DefaultPrevented is true when the most recent submit event had
	// preventDefault() called on it (i.e. client validation blocked
	// the form). Requires the tracker to have been installed before
	// the submit; nil-zero when no submit was observed.
	DefaultPrevented bool `json:"default_prevented"`

	// SubmittedForm identifies the form whose submit was observed:
	// id, name, or `<form>` as a last resort. Empty when no submit
	// has fired.
	SubmittedForm string `json:"submitted_form,omitempty"`

	// AlertsVisible lists the visible `[role=alert]` text content
	// currently on the page (each entry capped at 200 chars). Most
	// frameworks render inline validation errors into these slots.
	AlertsVisible []string `json:"alerts_visible,omitempty"`

	// AriaInvalidFields lists labels of inputs currently carrying
	// aria-invalid="true" — another signal that validation failed.
	AriaInvalidFields []string `json:"aria_invalid_fields,omitempty"`

	// FrameworkErrorOverlay captures the dev-server error overlay
	// text if one is visible (Vite, Next.js, Webpack).
	FrameworkErrorOverlay string `json:"framework_error_overlay,omitempty"`

	// XHRCount is the count of fetch/XMLHttpRequest calls observed
	// since the tracker was installed. Useful to disambiguate
	// "submit fired and hit the network" vs "submit fired but no
	// request went out".
	XHRCount int `json:"xhr_count"`

	// NavigationCommitted is true when the current URL differs from
	// the URL at the time the tracker was installed. Catches submits
	// that triggered a full nav.
	NavigationCommitted bool `json:"navigation_committed"`

	// CurrentURL is the URL at the moment of reading.
	CurrentURL string `json:"current_url,omitempty"`

	// TrackerInstalled is false when no submit has been observed yet
	// because the tracker just installed itself. Caller should retry
	// after the next submit-firing action.
	TrackerInstalled bool `json:"tracker_installed"`
}

// scoutSubmitTrackerJS is injected once per page. It installs:
//   - a capture-phase submit listener that records defaultPrevented
//     for the latest submit (microtask-deferred so we see the value
//     after handlers ran);
//   - fetch + XHR wrappers that bump an XHR counter;
//   - the initial URL so SubmitOutcome.NavigationCommitted can be
//     evaluated against the moment the tracker booted.
//
// Idempotent — re-running is a no-op when window.__scoutSubmit exists.
const scoutSubmitTrackerJS = `(function() {
	if (window.__scoutSubmit) return;
	const state = {
		initial_url: location.href,
		xhr_count: 0,
		last_submit: null,
	};
	window.__scoutSubmit = state;
	document.addEventListener('submit', function(e) {
		const form = e.target;
		const name = (form && (form.id || form.name || (form.action || '<form>'))) || '<form>';
		// Defer one task so listeners chained after this one get to
		// call preventDefault before we snapshot.
		setTimeout(function() {
			state.last_submit = {
				default_prevented: !!e.defaultPrevented,
				form: name,
				ts: Date.now(),
			};
		}, 0);
	}, true);
	if (window.fetch) {
		const origFetch = window.fetch.bind(window);
		window.fetch = function() {
			state.xhr_count++;
			return origFetch.apply(window, arguments);
		};
	}
	if (window.XMLHttpRequest && window.XMLHttpRequest.prototype) {
		const origSend = window.XMLHttpRequest.prototype.send;
		window.XMLHttpRequest.prototype.send = function() {
			state.xhr_count++;
			return origSend.apply(this, arguments);
		};
	}
})()`

// InstallSubmitTracker injects the page-side tracker so subsequent
// LastSubmitOutcome reads can return defaultPrevented + XHRCount.
// Idempotent — safe to call from auto-init paths.
func (s *Session) InstallSubmitTracker() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return err
	}
	_, err := s.page.Evaluate(scoutSubmitTrackerJS)
	return err
}

// LastSubmitOutcome returns the structured aftermath of the most
// recent submit/click on the page. Auto-installs the tracker if it
// isn't already loaded — on the first call after a submit fired,
// TrackerInstalled will be true but the submit data will be missing
// because the tracker booted too late. The caller should re-fire
// the action and read again.
func (s *Session) LastSubmitOutcome() (*SubmitOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	js := scoutSubmitTrackerJS + `;(function() {
		const state = window.__scoutSubmit || {};
		const alerts = [];
		for (const el of document.querySelectorAll('[role="alert"]')) {
			const cs = window.getComputedStyle(el);
			if (cs.display === 'none' || cs.visibility === 'hidden') continue;
			const t = (el.textContent || '').trim();
			if (t) alerts.push(t.slice(0, 200));
		}
		const invalid = [];
		for (const el of document.querySelectorAll('[aria-invalid="true"]')) {
			let label = '';
			if (el.id) {
				const l = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
				if (l) label = l.textContent.trim().slice(0, 80);
			}
			if (!label) {
				const al = el.getAttribute('aria-label');
				if (al) label = al.slice(0, 80);
			}
			if (!label) {
				const wrap = el.closest('label');
				if (wrap) {
					const c = wrap.cloneNode(true);
					c.querySelectorAll('input,textarea,select').forEach(i => i.remove());
					label = c.textContent.trim().slice(0, 80);
				}
			}
			if (!label) label = el.name || el.id || el.placeholder || '<unnamed>';
			invalid.push(label);
		}
		// Dev-server error overlays — Vite, Next, Webpack all use vite-error-overlay /
		// nextjs-portal / __WebpackOverlay class names.
		let overlay = '';
		for (const sel of ['vite-error-overlay', '#__next-build-watcher', '[data-nextjs-toast]', 'iframe#webpack-dev-server-client-overlay']) {
			const el = document.querySelector(sel);
			if (el) {
				const text = (el.textContent || '').trim();
				if (text) { overlay = text.slice(0, 400); break; }
			}
		}
		return {
			default_prevented: state.last_submit ? !!state.last_submit.default_prevented : false,
			submitted_form: state.last_submit ? state.last_submit.form : '',
			alerts_visible: alerts,
			aria_invalid_fields: invalid,
			framework_error_overlay: overlay,
			xhr_count: state.xhr_count || 0,
			navigation_committed: !!(state.initial_url && state.initial_url !== location.href),
			current_url: location.href,
			tracker_installed: !!state.initial_url,
		};
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("submit-outcome read failed: %w", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected submit-outcome result")
	}

	out := &SubmitOutcome{}
	out.DefaultPrevented, _ = m["default_prevented"].(bool)
	out.SubmittedForm, _ = m["submitted_form"].(string)
	out.FrameworkErrorOverlay, _ = m["framework_error_overlay"].(string)
	out.NavigationCommitted, _ = m["navigation_committed"].(bool)
	out.CurrentURL, _ = m["current_url"].(string)
	out.TrackerInstalled, _ = m["tracker_installed"].(bool)
	if v, ok := m["xhr_count"].(float64); ok {
		out.XHRCount = int(v)
	}
	if arr, ok := m["alerts_visible"].([]any); ok {
		for _, a := range arr {
			if str, ok := a.(string); ok {
				out.AlertsVisible = append(out.AlertsVisible, str)
			}
		}
	}
	if arr, ok := m["aria_invalid_fields"].([]any); ok {
		for _, a := range arr {
			if str, ok := a.(string); ok {
				out.AriaInvalidFields = append(out.AriaInvalidFields, str)
			}
		}
	}
	return out, nil
}
