package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DetectedFrameworks returns which frontend frameworks are active on the current page.
func (s *Session) DetectedFrameworks() ([]string, error) {
	return s.DetectedFrameworksContext(context.Background())
}

// DetectedFrameworksContext detects frontend frameworks while honoring ctx.
func (s *Session) DetectedFrameworksContext(ctx context.Context) ([]string, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	js := `(function() {
		const d = [];
		try {
		// Fast checks (globals only, no DOM scanning)
		if (window.__REACT_DEVTOOLS_GLOBAL_HOOK__) d.push('react');
		if (window.Vue) d.push('vue2');
		if (window.__VUE__ || document.querySelector('[data-v-app]')) d.push('vue3');
		if (window.ng || window.getAllAngularTestabilities || document.querySelector('[ng-version]')) d.push('angular');
		if (window._$HY || window.$SOLID_DEVTOOLS) d.push('solid');
		if (window.preact) d.push('preact');
		if (window.Alpine || document.querySelector('[x-data]')) d.push('alpine');
		if (window.htmx) d.push('htmx');
		if (window.Stimulus || document.querySelector('[data-controller]')) d.push('stimulus');
		if (window.Ember || window.Em) d.push('ember');
		if (window.__QWIK_MANIFEST__) d.push('qwik');
		if (window.__NEXT_DATA__ || document.getElementById('__NEXT_DATA__')) d.push('nextjs');
		if (window.__NUXT__ || document.getElementById('__NUXT_DATA__')) d.push('nuxt');
		if (window.__remixContext || document.getElementById('__remixContext')) d.push('remix');
		if (document.getElementById('__sveltekit_data')) d.push('sveltekit');
		if (document.getElementById('___gatsby') || window.___GATSBY_INTERNAL_PLUGINS) d.push('gatsby');
		if (document.querySelector('[data-astro-island]') || window.__ASTRO__) d.push('astro');
		// Slower checks — scan a sample of elements for framework markers
		const sample = document.querySelectorAll('#root, #app, #__next, [data-reactroot], body > div');
		for (const el of sample) {
			const keys = Object.keys(el);
			if (!d.includes('react') && keys.some(k => k.startsWith('__reactFiber'))) d.push('react');
			if (!d.includes('vue2') && el.__vue__) d.push('vue2');
			if (!d.includes('vue3') && (el.__vueParentComponent || el.__vue_app__)) d.push('vue3');
			if (!d.includes('svelte') && (el.__svelte_meta || el.$$)) d.push('svelte');
			if (!d.includes('preact') && keys.some(k => k.startsWith('__preact'))) d.push('preact');
			if (!d.includes('lit') && el.shadowRoot && el.renderRoot) d.push('lit');
		}
		} catch(e) {}
		return JSON.stringify(d);
	})()`

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, err
	}
	str, _ := result.(string)
	var frameworks []string
	_ = json.Unmarshal([]byte(str), &frameworks)
	return frameworks, nil
}

// ComponentState extracts state/props from a framework component at the given selector.
// Auto-detects the framework (React, Vue 2/3, Svelte, Preact, Angular, Alpine, Lit).
func (s *Session) ComponentState(selector string) (map[string]any, error) {
	return s.ComponentStateContext(context.Background(), selector)
}

// ComponentStateContext extracts framework state while honoring ctx for lock
// queueing, page setup, and browser evaluation.
func (s *Session) ComponentStateContext(ctx context.Context, selector string) (map[string]any, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return null;
		const result = {framework: null};

		// React (fiber)
		const fiberKey = Object.keys(el).find(k => k.startsWith('__reactFiber'));
		if (fiberKey) {
			result.framework = 'react';
			const fiber = el[fiberKey];
			let current = fiber;
			while (current) {
				if (current.memoizedProps || current.memoizedState) {
					result.props = current.memoizedProps;
					const states = [];
					let hook = current.memoizedState;
					while (hook) {
						if (hook.memoizedState !== undefined && typeof hook.memoizedState !== 'function') {
							try { states.push(JSON.parse(JSON.stringify(hook.memoizedState))); } catch(e) {}
						}
						hook = hook.next;
					}
					if (states.length > 0) result.state = states;
					break;
				}
				current = current.return;
			}
			return JSON.stringify(result);
		}

		// Vue 2
		if (el.__vue__) {
			result.framework = 'vue2';
			try { result.data = JSON.parse(JSON.stringify(el.__vue__.$data || {})); } catch(e) {}
			result.props = el.__vue__.$props || {};
			return JSON.stringify(result);
		}

		// Vue 3
		if (el.__vueParentComponent) {
			result.framework = 'vue3';
			const inst = el.__vueParentComponent;
			if (inst.setupState) {
				const data = {};
				for (const k of Object.keys(inst.setupState)) {
					try { if (typeof inst.setupState[k] !== 'function') data[k] = JSON.parse(JSON.stringify(inst.setupState[k])); } catch(e) {}
				}
				result.data = data;
			}
			result.props = inst.props || {};
			return JSON.stringify(result);
		}

		// Svelte
		if (el.$$) {
			result.framework = 'svelte';
			result.ctx = el.$$.ctx;
			return JSON.stringify(result);
		}

		// Preact
		const preactKey = Object.keys(el).find(k => k.startsWith('__preact'));
		if (preactKey) {
			result.framework = 'preact';
			const f = el[preactKey];
			result.props = f.props;
			if (f._component) result.state = f._component.state;
			return JSON.stringify(result);
		}

		// Angular (Ivy)
		if (window.ng && window.ng.getComponent) {
			try {
				const comp = window.ng.getComponent(el);
				if (comp) {
					result.framework = 'angular';
					const props = {};
					for (const k of Object.getOwnPropertyNames(comp)) {
						try { if (typeof comp[k] !== 'function') props[k] = JSON.parse(JSON.stringify(comp[k])); } catch(e) {}
					}
					result.state = props;
					return JSON.stringify(result);
				}
			} catch(e) {}
		}

		// Alpine.js
		if (el._x_dataStack || el.__x) {
			result.framework = 'alpine';
			result.data = el._x_dataStack ? el._x_dataStack[0] : (el.__x ? el.__x.$data : {});
			return JSON.stringify(result);
		}

		// Lit / Web Components
		if (el.shadowRoot && el.constructor.properties) {
			result.framework = 'lit';
			const props = {};
			for (const [k] of el.constructor.properties) {
				try { props[k] = el[k]; } catch(e) {}
			}
			result.properties = props;
			return JSON.stringify(result);
		}

		return null;
	})()`, selectorJSON)

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("no framework component found at %s", selector)
	}
	str, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected result type")
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(str), &state); err != nil {
		return nil, err
	}
	return state, nil
}

// ReactState extracts React component state/props from an element.
func (s *Session) ReactState(selector string) (map[string]any, error) {
	return s.ComponentState(selector)
}

// VueState extracts Vue component data from an element.
func (s *Session) VueState(selector string) (map[string]any, error) {
	return s.ComponentState(selector)
}

// GetAppState extracts global application state from all detected frameworks.
// Checks: Redux, Next.js, Nuxt, Remix, SvelteKit, Gatsby, Alpine stores,
// HTMX config, and common SSR hydration patterns.
func (s *Session) GetAppState() (map[string]any, error) {
	return s.GetAppStateContext(context.Background())
}

// GetAppStateContext extracts global application state while honoring ctx for
// lock queueing, page setup, and browser evaluation.
func (s *Session) GetAppStateContext(ctx context.Context) (map[string]any, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	js := `(function() {
		const state = {};

		// Next.js
		try {
			const nd = document.getElementById('__NEXT_DATA__');
			if (nd) {
				const d = JSON.parse(nd.textContent);
				state.nextjs = {page: d.page, buildId: d.buildId, props: d.props?.pageProps};
			} else if (window.__NEXT_DATA__) {
				state.nextjs = {page: window.__NEXT_DATA__.page, props: window.__NEXT_DATA__.props?.pageProps};
			}
		} catch(e) {}

		// Nuxt
		try {
			if (window.__NUXT__) state.nuxt = window.__NUXT__.data || window.__NUXT__.state || window.__NUXT__;
			const nd = document.getElementById('__NUXT_DATA__');
			if (nd) state.nuxt = JSON.parse(nd.textContent);
		} catch(e) {}

		// Remix
		try {
			if (window.__remixContext) state.remix = window.__remixContext;
			const rc = document.getElementById('__remixContext');
			if (rc) state.remix = JSON.parse(rc.textContent);
		} catch(e) {}

		// SvelteKit
		try {
			const sk = document.getElementById('__sveltekit_data');
			if (sk) state.sveltekit = JSON.parse(sk.textContent);
		} catch(e) {}

		// Gatsby
		try {
			const g = document.querySelector('script[id="gatsby-chunk-mapping"]');
			if (g) state.gatsby = JSON.parse(g.textContent);
		} catch(e) {}

		// Astro islands
		try {
			const islands = document.querySelectorAll('[data-astro-island]');
			if (islands.length > 0) {
				state.astro = Array.from(islands).map(i => ({
					component: i.getAttribute('data-astro-island'),
					props: i.dataset.astroProps ? JSON.parse(decodeURIComponent(i.dataset.astroProps)) : {}
				}));
			}
		} catch(e) {}

		// Alpine stores
		try {
			if (window.Alpine && window.Alpine._stores) {
				state.alpine = {};
				for (const [k, v] of Object.entries(window.Alpine._stores)) {
					try { state.alpine[k] = JSON.parse(JSON.stringify(v)); } catch(e) {}
				}
			}
		} catch(e) {}

		// HTMX
		try {
			if (window.htmx) state.htmx = {version: window.htmx.version, config: window.htmx.config};
		} catch(e) {}

		// Qwik
		try {
			if (window.__QWIK_MANIFEST__) state.qwik = {manifest: true};
		} catch(e) {}

		// Generic SSR hydration state
		const hydrationKeys = ['__INITIAL_STATE__','__APP_STATE__','__PRELOADED_STATE__','__APP_INITIAL_STATE__','__INITIAL_DATA__'];
		for (const k of hydrationKeys) {
			try { if (window[k]) state[k] = window[k]; } catch(e) {}
		}

		// JSON script tags (common hydration pattern)
		const jsonScripts = document.querySelectorAll('script[type="application/json"]');
		if (jsonScripts.length > 0) {
			state._hydrationScripts = Array.from(jsonScripts).slice(0, 5).map(s => {
				try { return {id: s.id, data: JSON.parse(s.textContent)}; } catch(e) { return {id: s.id}; }
			});
		}

		if (Object.keys(state).length === 0) return null;
		return JSON.stringify(state);
	})()`

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	str, ok := result.(string)
	if !ok {
		return nil, nil
	}
	var appState map[string]any
	if err := json.Unmarshal([]byte(str), &appState); err != nil {
		return nil, err
	}
	return appState, nil
}

// WaitForSPA waits for SPA framework hydration/rendering to complete.
// Detects React, Vue, Angular, Svelte, Next.js, Nuxt, and generic content presence.
func (s *Session) WaitForSPA() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.WaitForSPAContext(ctx)
}

func (s *Session) WaitForSPAContext(ctx context.Context) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return err
	}

	js := `new Promise(resolve => {
		function check() {
			const ready =
				(document.getElementById('root') && document.getElementById('root').children.length > 0) ||
				(document.getElementById('app') && document.getElementById('app').children.length > 0) ||
				(document.getElementById('__next') && document.getElementById('__next').children.length > 0) ||
				(document.getElementById('__nuxt') && document.getElementById('__nuxt').children.length > 0) ||
				document.querySelector('[data-v-app]') !== null ||
				document.querySelector('[ng-version]') !== null ||
				document.querySelector('[data-astro-island]') !== null ||
				document.querySelector('[data-sveltekit-router]') !== null ||
				(document.body && document.body.innerText.trim().length > 100);
			if (ready) resolve(true);
			else requestAnimationFrame(check);
		}
		if (document.readyState === 'complete') setTimeout(check, 100);
		else window.addEventListener('load', () => setTimeout(check, 100));
		setTimeout(() => resolve(true), 10000);
	})`

	_, err := s.page.EvaluateContext(ctx, js)
	return err
}

// WaitForSPAIdle waits until the page is hydrated and visually quiet:
//   - document.readyState === "complete"
//   - Astro's "astro:page-load" / "astro:end" event has fired (if Astro detected)
//   - Vue / React mount markers exist (data-v-app populated, root has children)
//   - no pending XHR/fetch (tracked via PerformanceObserver)
//   - no visible spinners / skeletons / aria-busy elements
//   - DOM stable (no mutations) for `quietWindow` milliseconds
//
// This is a stronger signal than WaitForSPA. Use it after a navigation or
// click that triggers hydration before extracting content or asserting state.
func (s *Session) WaitForSPAIdle(quietMS, timeoutMS int) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.WaitForSPAIdleContext(ctx, quietMS, timeoutMS)
}

func (s *Session) WaitForSPAIdleContext(ctx context.Context, quietMS, timeoutMS int) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	if quietMS <= 0 {
		quietMS = 400
	}
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}

	js := fmt.Sprintf(`new Promise(resolve => {
		const QUIET = %d;
		const HARD = %d;
		const start = Date.now();
		const isAstro = !!document.querySelector('[data-astro-island]') || !!window.__ASTRO__;
		let astroDone = !isAstro;
		if (isAstro) {
			document.addEventListener('astro:page-load', () => { astroDone = true; }, {once: true});
			document.addEventListener('astro:end', () => { astroDone = true; }, {once: true});
			// fallback: assume hydration done after 1s if event never fires
			setTimeout(() => { astroDone = true; }, 1000);
		}

		// Track in-flight fetch + XHR. Patch once.
		if (!window.__scoutPending) {
			window.__scoutPending = {n: 0};
			const origFetch = window.fetch;
			window.fetch = function() {
				window.__scoutPending.n++;
				return origFetch.apply(this, arguments).finally(() => { window.__scoutPending.n--; });
			};
			const XHR = window.XMLHttpRequest;
			if (XHR && XHR.prototype) {
				const origSend = XHR.prototype.send;
				XHR.prototype.send = function() {
					window.__scoutPending.n++;
					this.addEventListener('loadend', () => { window.__scoutPending.n--; });
					return origSend.apply(this, arguments);
				};
			}
		}

		let lastMutation = Date.now();
		const obs = new MutationObserver(() => { lastMutation = Date.now(); });
		obs.observe(document.documentElement, {childList: true, subtree: true, attributes: true});

		function ready() {
			if (document.readyState !== 'complete') return false;
			if (!astroDone) return false;
			const pending = (window.__scoutPending && window.__scoutPending.n) || 0;
			if (pending > 0) return false;
			// Visual busy markers
			const busy = document.querySelector('[aria-busy="true"], .spinner, .loading, [class*="skeleton"]:not([class*="hide"])');
			if (busy) {
				const r = busy.getBoundingClientRect();
				const cs = window.getComputedStyle(busy);
				if (r.width > 0 && r.height > 0 && cs.display !== 'none' && cs.visibility !== 'hidden') return false;
			}
			// Mount markers — at least one of these should be populated
			const root = document.getElementById('root') || document.getElementById('app') || document.getElementById('__next') || document.getElementById('__nuxt');
			if (root && root.children.length === 0 && !document.querySelector('[data-v-app]') && !document.querySelector('[ng-version]')) return false;
			// Quiet window since last mutation
			return (Date.now() - lastMutation) >= QUIET;
		}

		function tick() {
			if (ready()) { obs.disconnect(); resolve({ok: true, elapsed: Date.now() - start}); return; }
			if ((Date.now() - start) >= HARD) { obs.disconnect(); resolve({ok: false, elapsed: Date.now() - start, reason: 'timeout'}); return; }
			setTimeout(tick, 100);
		}
		tick();
	})`, quietMS, timeoutMS)

	_, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, err
	}
	return s.pageResultContext(ctx)
}

// DispatchEventContext dispatches a DOM event on an element.
//
// Special cases:
//   - eventType "submit" calls form.requestSubmit() instead of dispatching a
//     bare Event('submit'). requestSubmit fires @submit / @submit.prevent
//     listeners reliably across Vue/React/native forms and runs HTML5
//     validation — the behaviour callers expect when they say "submit this
//     form". If selector points at a non-form element, the closest enclosing
//     <form> is used.
//   - eventType "click" calls .click() so the click triggers the element's
//     default action (e.g. form submission for type=submit).
func (s *Session) DispatchEventContext(ctx context.Context, selector, eventType string, detail map[string]any) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return err
	}
	return s.dispatchEventLockedContext(ctx, selector, eventType, detail)
}

func (s *Session) dispatchEventLockedContext(ctx context.Context, selector, eventType string, detail map[string]any) error {

	selectorJSON, _ := json.Marshal(selector)
	detailJSON, _ := json.Marshal(detail)

	// Shared shadow-DOM-piercing helper: every branch needs the
	// same deep traversal to reach Lit / Stencil / Web Components
	// content. Inlined per call so DispatchEvent stays self-
	// contained (no window pollution that survives between calls).
	const deepFind = `
		function __sd(root, sel) {
			if (!root) return null;
			try { const m = root.querySelector(sel); if (m) return m; } catch(_) {}
			const kids = root.querySelectorAll ? root.querySelectorAll('*') : [];
			for (const el of kids) { if (el.shadowRoot) { const m = __sd(el.shadowRoot, sel); if (m) return m; } }
			return null;
		}`

	var js string
	switch eventType {
	case "submit":
		js = fmt.Sprintf(`(function() {%s
			const el = __sd(document, %s);
			if (!el) return false;
			const form = el.tagName === 'FORM' ? el : el.closest('form');
			if (!form) return false;
			if (typeof form.requestSubmit === 'function') {
				form.requestSubmit();
			} else {
				form.dispatchEvent(new Event('submit', {bubbles: true, cancelable: true}));
			}
			return true;
		})()`, deepFind, selectorJSON)
	case "click":
		js = fmt.Sprintf(`(function() {%s
			const el = __sd(document, %s);
			if (!el) return false;
			el.click();
			return true;
		})()`, deepFind, selectorJSON)
	default:
		js = fmt.Sprintf(`(function() {%s
			const el = __sd(document, %s);
			if (!el) return false;
			el.dispatchEvent(new CustomEvent(%q, {detail: %s, bubbles: true, cancelable: true}));
			return true;
		})()`, deepFind, selectorJSON, eventType, string(detailJSON))
	}

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			action := "dispatch_event"
			switch eventType {
			case "click":
				action = "click"
			case "submit":
				action = "submit_form"
			}
			return s.actionOutcomeAfterDispatch(action, selector, eventType+" event command was interrupted; the browser may have received it", err)
		}
		return err
	}
	if b, ok := result.(bool); !ok || !b {
		return fmt.Errorf("element %s not found", selector)
	}
	return nil
}

func (s *Session) DispatchEventWithPostconditionContext(ctx context.Context, selector, eventType string, detail map[string]any, condition Postcondition) (*PageResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if condition.count() > 1 {
		return nil, fmt.Errorf("postcondition requires at most one condition")
	}
	if condition.count() == 1 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	action := dispatchEventAction(eventType)
	start, before := s.traceBeforeActionContext(ctx)
	if err := s.dispatchEventLockedContext(ctx, selector, eventType, detail); err != nil {
		var outcomeErr *ActionOutcomeError
		if condition.count() == 1 && errors.As(err, &outcomeErr) {
			conditionCopy := condition
			outcomeErr.ExpectedPostcondition = &conditionCopy
		}
		return nil, s.traceActionFailureContext(ctx, start, before, action, selector, "", "", err)
	}
	if condition.count() == 1 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, action, selector, condition)
		if verifyErr != nil {
			outcomeErr := s.actionOutcomeAfterDispatch(action, selector, eventType+" was dispatched; postcondition could not be verified", verifyErr, condition)
			return nil, s.traceActionFailureContext(ctx, start, before, action, selector, "", "", outcomeErr)
		}
		s.traceAfterActionContext(ctx, start, before, action, selector, "", result.URL, nil)
		if s.tracing && s.trace != nil && len(s.trace.events) > 0 {
			result.OperationOrdinal = s.trace.events[len(s.trace.events)-1].OperationOrdinal
		}
		postcondition := condition
		result.Receipt = s.newActionReceipt(action, selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, &postcondition, ctx)
		actionReceiptDeadline(ctx, result.Receipt)
		s.applyTraceResult(action, selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
		return result, nil
	}
	result, err := s.pageResultContext(ctx)
	if err != nil {
		outcomeErr := s.actionOutcomeAfterDispatch(action, selector, eventType+" was dispatched; page state could not be read", err, condition)
		return nil, s.traceActionFailureContext(ctx, start, before, action, selector, "", "", outcomeErr)
	}
	evidence := eventType + " was dispatched to the target; event handlers and application state were not verified"
	recovery := "Supply an expected page condition or inspect the page with observe/assert tools to verify the application effect."
	result.Action, result.Target = action, selector
	result.Outcome, result.Verified, result.Evidence, result.RecoveryHint = string(OutcomeEventObserved), false, evidence, recovery
	s.traceAfterActionContext(ctx, start, before, action, selector, "", result.URL, nil)
	result.OperationOrdinal = s.operationOrdinalForResult(action, selector)
	result.Receipt = s.newActionReceipt(action, selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, false, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult(action, selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, false)
	return result, nil
}

func dispatchEventAction(eventType string) string {
	switch eventType {
	case "click":
		return "click"
	case "submit":
		return "submit_form"
	default:
		return "dispatch_event"
	}
}

func (s *Session) DispatchEventWithPostcondition(selector, eventType string, detail map[string]any, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.DispatchEventWithPostconditionContext(ctx, selector, eventType, detail, condition)
}

func (s *Session) DispatchEvent(selector, eventType string, detail map[string]any) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.DispatchEventContext(ctx, selector, eventType, detail)
}

// SubmitFormResult is returned by SubmitForm.
type SubmitFormResult struct {
	OperationOrdinal    int            `json:"operation_ordinal,omitempty"`
	ContentTrust        string         `json:"content_trust,omitempty"`
	Submitted           bool           `json:"submitted"`
	TargetID            string         `json:"target_id,omitempty"`
	FormSelector        string         `json:"form_selector,omitempty"`
	RequestURL          string         `json:"request_url,omitempty"`
	RequestMethod       string         `json:"request_method,omitempty"`
	StatusCode          int            `json:"status_code,omitempty"`
	URLAfter            string         `json:"url_after,omitempty"`
	Note                string         `json:"note,omitempty"`
	ResponseObserved    bool           `json:"response_observed"`
	ApplicationAccepted bool           `json:"application_accepted"`
	Verified            bool           `json:"verified"`
	Outcome             string         `json:"outcome"`
	Evidence            string         `json:"evidence,omitempty"`
	RecoveryHint        string         `json:"recovery_hint,omitempty"`
	Receipt             *ActionReceipt `json:"action_receipt,omitempty"`
}

func (s *Session) SubmitFormWithPostcondition(selector, matchURL string, timeoutMS int, condition Postcondition) (*SubmitFormResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.submitFormWithConditionContext(ctx, selector, matchURL, timeoutMS, condition, false)
}

func (s *Session) SubmitFormWithAcceptance(selector, matchURL string, timeoutMS int, condition Postcondition) (*SubmitFormResult, error) {
	if condition.URL != "" || condition.URLContains != "" || condition.Title != "" || condition.count() != 1 {
		return nil, fmt.Errorf("acceptance requires exactly one application-state condition (text, selector existence or visibility, value, or count)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.submitFormWithConditionContext(ctx, selector, matchURL, timeoutMS, condition, true)
}

func (s *Session) submitFormWithConditionContext(ctx context.Context, selector, matchURL string, timeoutMS int, condition Postcondition, acceptance bool) (*SubmitFormResult, error) {
	if condition.count() > 0 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
	} else if acceptance {
		return nil, fmt.Errorf("acceptance requires an application-state condition")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	traceFailure := func(err error) (*SubmitFormResult, error) {
		return nil, s.traceActionFailureContext(ctx, start, before, "submit_form", selector, "", "", err)
	}
	out, err := s.submitFormLockedContext(ctx, selector, matchURL, timeoutMS)
	if err != nil {
		var outcomeErr *ActionOutcomeError
		if condition.count() > 0 && errors.As(err, &outcomeErr) && outcomeErr.ExpectedPostcondition == nil {
			postcondition := condition
			outcomeErr.ExpectedPostcondition = &postcondition
		}
		return traceFailure(err)
	}
	out.ResponseObserved = out.StatusCode > 0 || out.URLAfter != ""
	if !out.ResponseObserved {
		if out.Outcome == "" {
			out.Outcome = string(OutcomeResponseUnobserved)
		}
		if out.RecoveryHint == "" {
			out.RecoveryHint = "Check whether submission triggered a request or navigation, then inspect the page before retrying."
		}
		s.recordSubmitTrace(ctx, start, before, selector, out)
		s.decorateSubmitReceipt(ctx, selector, condition, acceptance, out)
		return out, nil
	}
	if out.StatusCode >= 400 {
		out.Outcome = string(OutcomeResponseFailed)
		out.Evidence = fmt.Sprintf("submission response returned HTTP %d", out.StatusCode)
		out.RecoveryHint = "Inspect the response and form validation state before retrying."
		s.recordSubmitTrace(ctx, start, before, selector, out)
		s.decorateSubmitReceipt(ctx, selector, condition, acceptance, out)
		return out, nil
	}
	if condition.count() == 0 {
		s.recordSubmitTrace(ctx, start, before, selector, out)
		s.decorateSubmitReceipt(ctx, selector, condition, acceptance, out)
		return out, nil
	}
	verified, err := s.verifyPagePostconditionContextLocked(ctx, "submitted", selector, condition)
	if err != nil {
		outcomeErr := s.actionOutcomeAfterDispatch("submit_form", selector, "form submission was dispatched; the application postcondition could not be verified", err, condition)
		var unknown *ActionOutcomeError
		if errors.As(outcomeErr, &unknown) {
			if out.ResponseObserved {
				responseURL := out.RequestURL
				if responseURL == "" {
					responseURL = out.URLAfter
				}
				unknown.ResponseEvidence = &ActionResponseEvidence{
					Observed: true, Method: out.RequestMethod,
					URL: redactTraceURL(responseURL), StatusCode: out.StatusCode,
				}
				if unknown.Receipt != nil {
					unknown.Receipt.ResponseEvidence = unknown.ResponseEvidence
				}
			}
		}
		return traceFailure(outcomeErr)
	}
	out.Verified, out.Evidence, out.Outcome, out.RecoveryHint = verified.Verified, verified.Evidence, verified.Outcome, verified.RecoveryHint
	out.ApplicationAccepted = acceptance && out.ResponseObserved && out.Verified && out.StatusCode < 400
	s.recordSubmitTrace(ctx, start, before, selector, out)
	s.decorateSubmitReceipt(ctx, selector, condition, acceptance, out)
	return out, nil
}

func (s *Session) recordSubmitTrace(ctx context.Context, start time.Time, before traceScreenshotReference, selector string, result *SubmitFormResult) {
	s.traceAfterActionContext(ctx, start, before, "submit_form", selector, result.RequestURL, result.URLAfter, nil)
	evidence := result.Evidence
	if evidence == "" {
		evidence = result.Note
	}
	recovery := result.RecoveryHint
	if result.ApplicationAccepted {
		evidence = "HTTP response observed and application acceptance condition verified; " + evidence
	} else if result.ResponseObserved && result.Verified {
		evidence = "HTTP response observed and postcondition verified; " + evidence
	}
	s.applyTraceResult("submit_form", selector, result.TargetID, result.Outcome, evidence, recovery, result.Verified)
}

func (s *Session) SubmitFormWithPostconditionContext(ctx context.Context, selector, matchURL string, timeoutMS int, condition Postcondition) (*SubmitFormResult, error) {
	return s.submitFormWithConditionContext(ctx, selector, matchURL, timeoutMS, condition, false)
}

func (s *Session) SubmitFormWithAcceptanceContext(ctx context.Context, selector, matchURL string, timeoutMS int, condition Postcondition) (*SubmitFormResult, error) {
	if condition.URL != "" || condition.URLContains != "" || condition.Title != "" || condition.count() != 1 {
		return nil, fmt.Errorf("acceptance requires exactly one application-state condition (text, selector existence or visibility, value, or count)")
	}
	return s.submitFormWithConditionContext(ctx, selector, matchURL, timeoutMS, condition, true)
}

// SubmitForm submits a form using form.requestSubmit() — the only reliable
// cross-framework way to trigger @submit / @submit.prevent listeners and
// run HTML5 validation. The selector can be the form itself or any element
// inside it; the closest enclosing <form> is used.
//
// If matchURL is non-empty, the call waits for an XHR/fetch matching that
// substring (default 8s) and returns its response status. Otherwise it waits
// for either a network response or a URL change (whichever comes first)
// before returning.
func (s *Session) SubmitForm(selector, matchURL string, timeoutMS int) (*SubmitFormResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SubmitFormContext(ctx, selector, matchURL, timeoutMS)
}

func (s *Session) SubmitFormContext(ctx context.Context, selector, matchURL string, timeoutMS int) (*SubmitFormResult, error) {
	return s.submitFormWithConditionContext(ctx, selector, matchURL, timeoutMS, Postcondition{}, false)
}

// submitFormLockedContext runs the watcher and returns while the caller still
// owns the session lock, allowing compound submit-and-verify operations to be atomic.
func (s *Session) submitFormLockedContext(ctx context.Context, selector, matchURL string, timeoutMS int) (*SubmitFormResult, error) {

	if timeoutMS <= 0 {
		timeoutMS = 8000
	}
	if err := s.ensureNetworkObserversContextLocked(ctx); err != nil {
		return nil, fmt.Errorf("submit_form: enable response tracking: %w", err)
	}
	s.network.mu.Lock()
	historyStart := len(s.network.history)
	s.network.mu.Unlock()

	urlValue, urlErr := s.page.EvaluateContext(ctx, `location.href`)
	urlBefore, _ := urlValue.(string)
	if urlErr != nil {
		return nil, urlErr
	}

	selectorJSON, _ := json.Marshal(selector)
	matchJSON, _ := json.Marshal(matchURL)
	js := fmt.Sprintf(`new Promise(resolve => {
		const sel = %s;
		const matchURL = %s;
		const HARD = %d;
		const el = document.querySelector(sel);
		if (!el) { resolve({ok: false, reason: 'form_not_found'}); return; }
		const form = el.tagName === 'FORM' ? el : el.closest('form');
		if (!form) { resolve({ok: false, reason: 'no_enclosing_form'}); return; }

		const result = {ok: true, formSelector: (form.id ? '#'+form.id : 'form'), method: (form.method || 'GET').toUpperCase()};
		const urlBefore = window.location.href;

		// Patch fetch + XHR to capture the first matching response after submit.
		let captured = null;
		const origFetch = window.fetch;
		const fetchPatch = function() {
			const args = arguments;
			const url = (typeof args[0] === 'string') ? args[0] : (args[0] && args[0].url) || '';
			return origFetch.apply(this, args).then(res => {
				if (!captured && (!matchURL || url.includes(matchURL))) {
					captured = {url: res.url || url, status: res.status, method: (args[1] && args[1].method) || 'GET'};
				}
				return res;
			});
		};
		window.fetch = fetchPatch;

		const XHR = window.XMLHttpRequest;
		const origOpen = XHR.prototype.open;
		const origSend = XHR.prototype.send;
		XHR.prototype.open = function(method, url) {
			this.__scout = {method: method, url: url};
			return origOpen.apply(this, arguments);
		};
		XHR.prototype.send = function() {
			this.addEventListener('loadend', () => {
				if (!captured && this.__scout && (!matchURL || this.__scout.url.includes(matchURL))) {
					captured = {url: this.__scout.url, status: this.status, method: this.__scout.method};
				}
			});
			return origSend.apply(this, arguments);
		};

		function done(payload) {
			window.fetch = origFetch;
			XHR.prototype.open = origOpen;
			XHR.prototype.send = origSend;
			resolve(Object.assign(result, payload));
		}

		// Trigger submission — requestSubmit fires @submit listeners + validation.
		if (typeof form.requestSubmit === 'function') {
			try { form.requestSubmit(); } catch(e) { form.submit(); }
		} else {
			form.submit();
		}

		const start = Date.now();
		function tick() {
			if (captured) { done({captured: captured}); return; }
			if (window.location.href !== urlBefore) { done({navigated: true, urlAfter: window.location.href}); return; }
			if ((Date.now() - start) >= HARD) { done({timeout: true}); return; }
			setTimeout(tick, 80);
		}
		tick();
	})`, string(selectorJSON), string(matchJSON), timeoutMS)

	raw, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		if ctx.Err() != nil {
			return nil, s.actionOutcomeAfterDispatch("submit_form", selector, "requestSubmit was issued; the response or resulting page state could not be confirmed", ctx.Err())
		}
		capture, waitErr := s.waitForSubmitResponseContext(ctx, historyStart, matchURL, time.Duration(timeoutMS)*time.Millisecond)
		if waitErr != nil {
			return nil, waitErr
		}
		if capture == nil {
			return nil, err
		}
		out := &SubmitFormResult{ContentTrust: ContentTrustUntrustedPage, Submitted: true, TargetID: s.page.TargetID(), FormSelector: selector, RequestURL: capture.URL, RequestMethod: capture.Method, StatusCode: capture.Status, ResponseObserved: true}
		if current, evalErr := s.page.EvaluateContext(ctx, `location.href`); evalErr == nil {
			out.URLAfter, _ = current.(string)
		}
		out.setSubmitResponseOutcome()
		s.recordAction(Action{Type: "submit_form", Selector: selector})
		s.addHistory("submit_form", selector, "", matchURL)
		return out, nil
	}

	out := &SubmitFormResult{ContentTrust: ContentTrustUntrustedPage, TargetID: s.page.TargetID()}
	m, _ := raw.(map[string]any)
	if m == nil {
		return nil, fmt.Errorf("submit_form: unexpected result")
	}
	if ok, _ := m["ok"].(bool); !ok {
		reason, _ := m["reason"].(string)
		switch reason {
		case "form_not_found":
			return nil, fmt.Errorf("submit_form: no element matched %q", selector)
		case "no_enclosing_form":
			return nil, fmt.Errorf("submit_form: %q is not inside a <form>", selector)
		}
		return nil, fmt.Errorf("submit_form failed: %s", reason)
	}
	out.Submitted = true
	out.FormSelector, _ = m["formSelector"].(string)
	out.RequestMethod, _ = m["method"].(string)

	if cap, ok := m["captured"].(map[string]any); ok {
		out.RequestURL, _ = cap["url"].(string)
		if st, ok := cap["status"].(float64); ok {
			out.StatusCode = int(st)
		}
		if meth, ok := cap["method"].(string); ok && meth != "" {
			out.RequestMethod = meth
		}
	} else if nav, _ := m["navigated"].(bool); nav {
		if u, ok := m["urlAfter"].(string); ok {
			out.URLAfter = u
		}
		out.Note = "form submitted; navigated without an observable XHR/fetch — likely a full-page POST"
	} else if to, _ := m["timeout"].(bool); to {
		out.Note = fmt.Sprintf("requestSubmit fired but no response observed within %dms. The handler may have preventDefault'd without making a request, or matched url not seen.", timeoutMS)
	}

	if out.URLAfter == "" {
		if u, err := s.page.EvaluateContext(ctx, `location.href`); err == nil {
			if current, ok := u.(string); ok && current != urlBefore {
				out.URLAfter = current
			}
		}
	}
	if _, ok := m["captured"].(map[string]any); !ok {
		if capture, waitErr := s.waitForSubmitResponseContext(ctx, historyStart, matchURL, 150*time.Millisecond); waitErr != nil {
			return nil, waitErr
		} else if capture != nil {
			out.RequestURL, out.RequestMethod, out.StatusCode = capture.URL, capture.Method, capture.Status
		}
	}
	out.setSubmitResponseOutcome()

	s.recordAction(Action{Type: "submit_form", Selector: selector})
	s.addHistory("submit_form", selector, "", matchURL)
	return out, nil
}

func (s *Session) decorateSubmitReceipt(ctx context.Context, selector string, condition Postcondition, acceptance bool, result *SubmitFormResult) {
	if result == nil {
		return
	}
	result.OperationOrdinal = s.operationOrdinalForResult("submit_form", selector)
	var postcondition *Postcondition
	if acceptance {
		copy := condition
		postcondition = &copy
	}
	result.Receipt = s.newActionReceipt("submit_form", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, postcondition, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
}

func (r *SubmitFormResult) setSubmitResponseOutcome() {
	r.ResponseObserved = r.StatusCode > 0 || r.URLAfter != ""
	switch {
	case r.StatusCode >= 400:
		r.Outcome = string(OutcomeResponseFailed)
		r.Evidence = fmt.Sprintf("submission response returned HTTP %d", r.StatusCode)
		r.RecoveryHint = "Inspect the response and form validation state before retrying."
	case r.ResponseObserved && r.StatusCode > 0:
		r.Outcome = string(OutcomeResponseObserved)
		r.Evidence = fmt.Sprintf("submission response returned HTTP %d", r.StatusCode)
	case r.ResponseObserved:
		r.Outcome = string(OutcomeResponseObserved)
		r.Evidence = "form navigation observed at " + r.URLAfter
	default:
		r.Outcome = string(OutcomeResponseUnobserved)
		r.RecoveryHint = "Check whether submission triggered a request or navigation, then inspect the page before retrying."
	}
}

func (s *Session) waitForSubmitResponseContext(ctx context.Context, start int, matchURL string, timeout time.Duration) (*NetworkCapture, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.network.mu.Lock()
		for i := start; i < len(s.network.history); i++ {
			capture := s.network.history[i]
			if (matchURL == "" || strings.Contains(capture.URL, matchURL)) && capture.Status > 0 {
				s.network.mu.Unlock()
				copy := capture
				return &copy, nil
			}
		}
		s.network.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, nil
		case <-ticker.C:
		}
	}
}

// WaitForRouteChange waits for a SPA client-side route change (pushState/replaceState/hashchange).
func (s *Session) WaitForRouteChange(timeout time.Duration) (*PageResult, error) {
	if timeout == 0 {
		timeout = s.timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.WaitForRouteChangeContext(ctx, timeout)
}

// WaitForRouteChangeContext waits for a client-side route change under the
// caller's deadline. The temporary history hooks are restored on success,
// timeout, and cancellation.
func (s *Session) WaitForRouteChangeContext(ctx context.Context, timeout time.Duration) (*PageResult, error) {
	if timeout <= 0 {
		timeout = s.timeout
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	js := fmt.Sprintf(`new Promise((resolve, reject) => {
		const previousWait = window.__scoutRouteWait;
		const origPush = history.pushState;
		const origReplace = history.replaceState;
		let timer;
		let settled = false;
		let wrappedPush, wrappedReplace;
		function cleanup() {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			window.removeEventListener('popstate', done);
			window.removeEventListener('hashchange', done);
			if (history.pushState === wrappedPush) history.pushState = origPush;
			if (history.replaceState === wrappedReplace) history.replaceState = origReplace;
			if (window.__scoutRouteWait === cleanup) {
				if (previousWait) window.__scoutRouteWait = previousWait;
				else delete window.__scoutRouteWait;
			}
		}
		function done() {
			if (settled) return;
			cleanup();
			resolve(window.location.href);
		}
		wrappedPush = function() { origPush.apply(this, arguments); done(); };
		wrappedReplace = function() { origReplace.apply(this, arguments); done(); };
		window.__scoutRouteWait = cleanup;
		window.addEventListener('popstate', done);
		window.addEventListener('hashchange', done);
		history.pushState = wrappedPush;
		history.replaceState = wrappedReplace;
		timer = setTimeout(() => { cleanup(); reject(new Error('timeout waiting for route change')); }, %d);
	})`, timeout.Milliseconds())

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _ = s.page.EvaluateContext(cleanupCtx, `if(window.__scoutRouteWait)window.__scoutRouteWait()`)
		cancel()
		return nil, err
	}
	urlStr, _ := result.(string)
	title, err := s.page.EvaluateContext(ctx, `document.title`)
	if err != nil {
		return nil, err
	}
	titleStr, _ := title.(string)
	return &PageResult{URL: urlStr, ContentTrust: ContentTrustUntrustedPage, Title: titleStr, TargetID: s.page.TargetID(), FrameID: s.frameID}, nil
}
