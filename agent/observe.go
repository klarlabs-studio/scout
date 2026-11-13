package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Observe returns a structured snapshot of the page's current state,
// including all interactive elements (links, inputs, buttons).
// This is designed to fit into an agent's context window as a concise
// representation of what actions are available on the page.
func (s *Session) Observe() (*Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.observeInternal()
}

// ObserveContext captures page state using the caller's deadline for both
// session queueing and browser evaluations.
func (s *Session) ObserveContext(ctx context.Context) (*Observation, error) {
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
	return s.observeInternalWithEvaluator(func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) })
}

// observeInternal is the non-locking implementation of Observe.
// Caller must hold s.mu.
func (s *Session) observeInternal() (*Observation, error) {
	return s.observeInternalWithEvaluator(s.evaluateInFrame)
}

func (s *Session) observeInternalWithEvaluator(evaluate func(string) (any, error)) (*Observation, error) {
	return s.observeInternalWithEvaluatorAndLimits(evaluate, nil)
}

type observationLimits struct {
	links   int
	inputs  int
	buttons int
}

func (s *Session) observeInternalWithEvaluatorAndLimits(evaluate func(string) (any, error), limits *observationLimits) (*Observation, error) {

	js := `(function() {
		const bodyText = document.body ? document.body.innerText : '';
		const obs = {
			url: window.location.href.slice(0, 2048),
			url_length: window.location.href.length,
			title: document.title.slice(0, 500),
			title_length: document.title.length,
			text: bodyText.slice(0, 2000),
			text_length: bodyText.length,
			text_truncated: bodyText.length > 2000,
			links: [],
			links_total: document.querySelectorAll('a[href]').length,
			inputs: [],
			inputs_total: document.querySelectorAll('input, textarea, select').length,
			buttons: [],
			buttons_total: document.querySelectorAll('button, input[type=submit], input[type=button], [role=button]').length,
			meta: {},
			meta_total: document.querySelectorAll('meta[name], meta[property]').length
		};

		// Collect links. Accessible name fallback so card-style anchors
		// (anchor wraps image + heading block, no direct text node) still
		// give the agent something to disambiguate. Order matches the
		// ARIA accessible-name computation, then a URL-slug last resort.
		function linkText(a) {
			const direct = a.textContent.trim();
			if (direct) return direct;
			const aria = a.getAttribute('aria-label');
			if (aria && aria.trim()) return aria.trim();
			const labelledBy = a.getAttribute('aria-labelledby');
			if (labelledBy) {
				const ref = document.getElementById(labelledBy);
				if (ref) {
					const t = (ref.textContent || '').trim();
					if (t) return t;
				}
			}
			const heading = a.querySelector('h1,h2,h3,h4,h5,h6');
			if (heading) {
				const t = (heading.textContent || '').trim();
				if (t) return t;
			}
			const img = a.querySelector('img[alt]');
			if (img) {
				const alt = (img.getAttribute('alt') || '').trim();
				if (alt) return alt;
			}
			const title = a.getAttribute('title');
			if (title && title.trim()) return title.trim();
			// Last resort: human-ify the URL slug.
			try {
				const url = new URL(a.href, window.location.href);
				const segs = url.pathname.split('/').filter(Boolean);
				if (segs.length > 0) {
					const last = decodeURIComponent(segs[segs.length - 1]);
					return last.replace(/[-_]+/g, ' ').trim();
				}
			} catch (_) {}
			return '';
		}
		for (const a of Array.from(document.querySelectorAll('a[href]')).slice(0, 100)) {
			const text = linkText(a);
			if (text || a.href) {
				obs.links.push({text: text.slice(0, 100), href: (a.getAttribute('href') || '').slice(0, 500)});
			}
		}

		// Collect inputs
		function inputLabel(el) {
			if (el.id) {
				const l = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
				if (l) return l.textContent.trim().slice(0, 80);
			}
			const ariaLabel = el.getAttribute('aria-label');
			if (ariaLabel) return ariaLabel.slice(0, 80);
			const wrapping = el.closest('label');
			if (wrapping) {
				const c = wrapping.cloneNode(true);
				c.querySelectorAll('input,textarea,select').forEach(i => i.remove());
				return c.textContent.trim().slice(0, 80);
			}
			return '';
		}
		for (const input of Array.from(document.querySelectorAll('input, textarea, select')).slice(0, 100)) {
			const t = (input.type || input.tagName.toLowerCase()).toLowerCase();
			const item = {
				id: (input.id || '').slice(0, 200),
				name: (input.name || '').slice(0, 200),
				type: t,
				value: t === 'password' || t === 'file' ? '' : (input.value || '').slice(0, 200),
				placeholder: (input.placeholder || '').slice(0, 200)
			};
			if (t === 'checkbox' || t === 'radio') {
				item.checked = !!input.checked;
				const lbl = inputLabel(input);
				if (lbl) item.label = lbl;
			}
			obs.inputs.push(item);
		}

		// Collect buttons
		for (const btn of Array.from(document.querySelectorAll('button, input[type=submit], input[type=button], [role=button]')).slice(0, 100)) {
			obs.buttons.push({
				text: (btn.textContent || btn.value || '').trim().slice(0, 100),
				id: (btn.id || '').slice(0, 200),
				type: btn.type || ''
			});
		}

		// Collect meta tags
		for (const meta of Array.from(document.querySelectorAll('meta[name], meta[property]')).slice(0, 50)) {
			const key = meta.getAttribute('name') || meta.getAttribute('property');
			const val = meta.getAttribute('content');
			if (key && val && Object.keys(obs.meta).length < 50) obs.meta[key.slice(0, 100)] = val.slice(0, 200);
		}

		// Active tab — [role=tab][aria-selected=true]
		const selectedTab = document.querySelector('[role="tab"][aria-selected="true"]');
		if (selectedTab) {
			obs.active_tab = (selectedTab.textContent || '').trim().slice(0, 80);
			obs.active_tab_id = (selectedTab.id || selectedTab.getAttribute('data-tab-id') || selectedTab.getAttribute('aria-controls') || '').slice(0, 200);
		}

		// Active navigation breadcrumb — [aria-current=page] links + page H1
		const nav = [];
		for (const el of Array.from(document.querySelectorAll('[aria-current="page"]')).slice(0, 10)) {
			const t = (el.textContent || '').trim();
			if (t) nav.push(t.slice(0, 60));
		}
		if (nav.length === 0) {
			for (const el of document.querySelectorAll('nav a.active, nav a.is-active, nav a[aria-selected="true"]')) {
				const t = (el.textContent || '').trim();
				if (t) nav.push(t.slice(0, 60));
				if (nav.length >= 4) break;
			}
		}
		const h1 = document.querySelector('h1');
		if (h1) {
			const ht = (h1.textContent || '').trim().slice(0, 80);
			if (ht && (nav.length === 0 || nav[nav.length-1] !== ht)) nav.push(ht);
		}
		if (nav.length > 0) obs.active_navigation = nav;

		obs.interactive = obs.links_total + obs.inputs_total + obs.buttons_total;
		return obs;
	})()`

	result, err := evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("failed to observe page: %w", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected observation result type")
	}

	maxLinks, maxInputs, maxButtons := s.contentOpts.MaxLinks, s.contentOpts.MaxInputs, s.contentOpts.MaxButtons
	if limits != nil {
		maxLinks, maxInputs, maxButtons = limits.links, limits.inputs, limits.buttons
	} else {
		if maxLinks == 0 {
			maxLinks = 25
		}
		if maxInputs == 0 {
			maxInputs = 20
		}
		if maxButtons == 0 {
			maxButtons = 15
		}
	}

	obs := &Observation{ContentTrust: ContentTrustUntrustedPage}
	obs.TargetID = s.page.TargetID()
	obs.FrameID = s.frameID
	obs.URL, _ = m["url"].(string)
	obs.Title, _ = m["title"].(string)
	obs.URLLength = observationInt(m["url_length"])
	obs.TitleLength = observationInt(m["title_length"])
	obs.Text, _ = m["text"].(string)
	obs.TextLength = observationInt(m["text_length"])
	obs.TextTruncated, _ = m["text_truncated"].(bool)
	obs.LinksTotal = observationInt(m["links_total"])
	obs.InputsTotal = observationInt(m["inputs_total"])
	obs.ButtonsTotal = observationInt(m["buttons_total"])
	obs.MetaTotal = observationInt(m["meta_total"])
	if v, ok := m["interactive"].(float64); ok {
		obs.Interactive = int(v)
	}

	if links, ok := m["links"].([]any); ok {
		for i, l := range links {
			if i >= maxLinks {
				break
			}
			if lm, ok := l.(map[string]any); ok {
				text, _ := lm["text"].(string)
				href, _ := lm["href"].(string)
				cost := estimateLinkCost(href)
				obs.Links = append(obs.Links, LinkInfo{ContentTrust: ContentTrustUntrustedPage, Text: text, Href: href, Cost: cost})
			}
		}
	}
	obs.Truncated = obs.LinksTotal > len(obs.Links) || obs.TextTruncated ||
		obs.URLLength > len(obs.URL) || obs.TitleLength > len(obs.Title)

	if inputs, ok := m["inputs"].([]any); ok {
		for i, inp := range inputs {
			if i >= maxInputs {
				break
			}
			if im, ok := inp.(map[string]any); ok {
				info := InputInfo{
					ContentTrust: ContentTrustUntrustedPage,
					ID:           strVal(im, "id"),
					Name:         strVal(im, "name"),
					Type:         strVal(im, "type"),
					Value:        strVal(im, "value"),
					Placeholder:  strVal(im, "placeholder"),
					Label:        strVal(im, "label"),
				}
				if c, ok := im["checked"].(bool); ok {
					info.Checked = &c
				}
				obs.Inputs = append(obs.Inputs, info)
			}
		}
	}
	obs.Truncated = obs.Truncated || obs.InputsTotal > len(obs.Inputs)

	if buttons, ok := m["buttons"].([]any); ok {
		for i, btn := range buttons {
			if i >= maxButtons {
				break
			}
			if bm, ok := btn.(map[string]any); ok {
				btnType := strVal(bm, "type")
				cost := estimateButtonCost(btnType, strVal(bm, "text"))
				obs.Buttons = append(obs.Buttons, ButtonInfo{
					ContentTrust: ContentTrustUntrustedPage,
					Text:         strVal(bm, "text"),
					ID:           strVal(bm, "id"),
					Type:         btnType,
					Cost:         cost,
				})
			}
		}
	}
	obs.Truncated = obs.Truncated || obs.ButtonsTotal > len(obs.Buttons)

	if meta, ok := m["meta"].(map[string]any); ok {
		obs.Meta = make(map[string]string)
		for k, v := range meta {
			if s, ok := v.(string); ok {
				obs.Meta[k] = s
			}
		}
	}
	obs.Truncated = obs.Truncated || obs.MetaTotal > len(obs.Meta)

	if v, ok := m["active_tab"].(string); ok {
		obs.ActiveTab = v
	}
	if v, ok := m["active_tab_id"].(string); ok {
		obs.ActiveTabID = v
	}
	if nav, ok := m["active_navigation"].([]any); ok {
		for _, n := range nav {
			if str, ok := n.(string); ok {
				obs.ActiveNavigation = append(obs.ActiveNavigation, str)
			}
		}
	}

	if cs := s.cookieSummaryInternal(); cs != nil {
		obs.Cookies = cs
	}

	// Check for active dialogs/modals
	dialogJS := `(function() {
		for (const d of document.querySelectorAll('dialog[open],[aria-modal="true"],[role="dialog"],[role="alertdialog"]')) {
			const s = window.getComputedStyle(d);
			if (s.display !== 'none' && s.visibility !== 'hidden') {
				return JSON.stringify({found:true, type: d.tagName==='DIALOG'?'dialog':'modal', text: d.textContent.trim().slice(0,200)});
			}
		}
		for (const sel of ['[class*="modal"]','[class*="dialog"]','[class*="overlay"]']) {
			for (const el of document.querySelectorAll(sel)) {
				const s = window.getComputedStyle(el);
				if (s.display!=='none' && s.visibility!=='hidden' && s.opacity!=='0' && (s.position==='fixed'||s.position==='absolute') && (parseInt(s.zIndex)||0)>=100) {
					return JSON.stringify({found:true, type:'overlay', text: el.textContent.trim().slice(0,200)});
				}
			}
		}
		return JSON.stringify({found:false});
	})()`
	dialogResult, err := evaluate(dialogJS)
	if err == nil {
		if dStr, ok := dialogResult.(string); ok {
			var dInfo struct {
				Found bool   `json:"found"`
				Type  string `json:"type"`
				Text  string `json:"text"`
			}
			if json.Unmarshal([]byte(dStr), &dInfo) == nil && dInfo.Found {
				obs.HasDialog = true
				obs.DialogType = dInfo.Type
				obs.DialogText = dInfo.Text
			}
		}
	}

	return obs, nil
}

func observationInt(v any) int {
	if n, ok := v.(float64); ok {
		return int(n)
	}
	return 0
}

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// estimateLinkCost classifies the cost of clicking a link.
func estimateLinkCost(href string) string {
	if href == "" || href == "#" {
		return "low" // in-page anchor or no-op
	}
	if strings.HasPrefix(href, "#") {
		return "low" // anchor scroll
	}
	if strings.HasPrefix(href, "javascript:") {
		return "medium" // JS action
	}
	return "high" // page navigation
}

// estimateButtonCost classifies the cost of clicking a button.
func estimateButtonCost(btnType, text string) string {
	textLower := strings.ToLower(text)
	if btnType == "submit" {
		return "high" // form submission, likely navigation
	}
	if strings.Contains(textLower, "submit") || strings.Contains(textLower, "sign") ||
		strings.Contains(textLower, "login") || strings.Contains(textLower, "register") ||
		strings.Contains(textLower, "create") || strings.Contains(textLower, "delete") ||
		strings.Contains(textLower, "save") || strings.Contains(textLower, "checkout") {
		return "high"
	}
	if strings.Contains(textLower, "next") || strings.Contains(textLower, "continue") ||
		strings.Contains(textLower, "load more") || strings.Contains(textLower, "search") {
		return "medium"
	}
	return "low" // toggle, close, expand, etc.
}
