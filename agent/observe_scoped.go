package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// ObserveOptions tunes which parts of the page Observe walks and how
// much of the result to return. Empty fields use the session defaults
// (matching plain Observe).
//
// Sections filters the scan to one or more landmark roles ("nav",
// "main", "header", "footer", "aside") plus a handful of common
// IDs/classes (#main, #content, .content, [role=main]). When empty
// scout walks the whole page as before.
type ObserveOptions struct {
	Sections     []string `json:"sections,omitempty"`
	AllowEmpty   bool     `json:"allow_empty,omitempty"`
	LimitChars   int      `json:"limit_chars,omitempty"`
	LinksLimit   int      `json:"links_limit,omitempty"`
	InputsLimit  int      `json:"inputs_limit,omitempty"`
	ButtonsLimit int      `json:"buttons_limit,omitempty"`
}

// ObserveScoped is Observe with per-call section + cap overrides.
// Useful on listing pages where the unscoped observation eats more
// tokens than the agent needs.
func (s *Session) ObserveScoped(opts ObserveOptions) (*Observation, error) {
	return s.observeScoped(opts, nil)
}

// ObserveScopedContext applies the same scoped observation with the caller's deadline.
func (s *Session) ObserveScopedContext(ctx context.Context, opts ObserveOptions) (*Observation, error) {
	return s.observeScoped(opts, ctx)
}

func (s *Session) observeScoped(opts ObserveOptions, ctx context.Context) (*Observation, error) {
	if opts.LimitChars > 8000 {
		opts.LimitChars = 8000
	}
	if opts.LinksLimit > 100 {
		opts.LinksLimit = 100
	}
	if opts.InputsLimit > 100 {
		opts.InputsLimit = 100
	}
	if opts.ButtonsLimit > 100 {
		opts.ButtonsLimit = 100
	}
	if ctx != nil {
		if err := s.mu.LockContext(ctx); err != nil {
			return nil, err
		}
	} else {
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	if ctx != nil {
		if err := s.ensurePageContext(ctx); err != nil {
			return nil, err
		}
	} else if err := s.ensurePage(); err != nil {
		return nil, err
	}
	evaluate := s.evaluateInFrame
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		evaluate = func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) }
	}
	saved := s.contentOpts
	defer func() { s.contentOpts = saved }()

	if opts.LimitChars > 0 {
		s.contentOpts.MaxLength = opts.LimitChars
	}
	if opts.LinksLimit > 0 {
		s.contentOpts.MaxLinks = opts.LinksLimit
	}
	if opts.InputsLimit > 0 {
		s.contentOpts.MaxInputs = opts.InputsLimit
	}
	if opts.ButtonsLimit > 0 {
		s.contentOpts.MaxButtons = opts.ButtonsLimit
	}

	if len(opts.Sections) == 0 {
		return s.observeInternalWithEvaluator(evaluate)
	}
	if err := s.validateScopeSelectorsWithEvaluator(opts.Sections, evaluate); err != nil {
		return nil, err
	}
	return s.observeWithinSectionsWithEvaluator(opts.Sections, opts.AllowEmpty, evaluate)
}

func (s *Session) validateScopeSelectorsWithEvaluator(sections []string, evaluate func(string) (any, error)) error {
	selectorsJSON := sectionRootJS(sections)
	result, err := evaluate(fmt.Sprintf(`(function(){
		const selectors=%s;
		for(const sel of selectors){document.querySelectorAll(sel)}
		return true;
	})()`, selectorsJSON))
	if err != nil {
		return fmt.Errorf("invalid observation scope selector in %v: %w", sections, err)
	}
	_ = result
	return nil
}

// sectionRootJS builds a JS expression that resolves the section
// roots requested by the caller. Each name maps to a CSS selector
// covering landmark elements plus common conventions.
func sectionRootJS(sections []string) string {
	sels := make([]string, 0, len(sections)*4)
	for _, name := range sections {
		switch name {
		case "nav":
			sels = append(sels, "nav", "[role=\"navigation\"]")
		case "main":
			sels = append(sels, "main", "[role=\"main\"]", "#main", "#content", ".content")
		case "header":
			sels = append(sels, "header", "[role=\"banner\"]")
		case "footer":
			sels = append(sels, "footer", "[role=\"contentinfo\"]")
		case "aside":
			sels = append(sels, "aside", "[role=\"complementary\"]")
		case "article":
			sels = append(sels, "article", "[role=\"article\"]")
		case "search":
			sels = append(sels, "[role=\"search\"]", "form[role=\"search\"]")
		default:
			// Treat as raw CSS selector. Trust the caller — agents may
			// pass "#sidebar" or ".product-grid" deliberately.
			sels = append(sels, name)
		}
	}
	joined, _ := json.Marshal(sels)
	return string(joined)
}

func (s *Session) observeWithinSectionsWithEvaluator(sections []string, allowEmpty bool, evaluate func(string) (any, error)) (*Observation, error) {
	rootSelsJSON := sectionRootJS(sections)
	maxText := s.contentOpts.MaxLength
	if maxText == 0 {
		maxText = 4000
	}

	maxLinks := s.contentOpts.MaxLinks
	if maxLinks <= 0 {
		maxLinks = 25
	}
	maxInputs := s.contentOpts.MaxInputs
	if maxInputs <= 0 {
		maxInputs = 20
	}
	maxButtons := s.contentOpts.MaxButtons
	if maxButtons <= 0 {
		maxButtons = 15
	}
	js := fmt.Sprintf(`(function() {
		const selectors = %s;
		const roots = [];
		const matched = [];
		const misses = [];
		const seen = new Set();
		for (const sel of selectors) {
			try {
				const found = Array.from(document.querySelectorAll(sel));
				if (found.length) matched.push(sel); else misses.push(sel);
				for (const el of found) {
					if (!seen.has(el)) { seen.add(el); roots.push(el); }
				}
			} catch (e) { return {scope_error: sel + ': ' + e.message}; }
		}
		if (roots.length === 0) return {scope_matched: 0, scope_selectors: matched, scope_misses: misses};

		const obs = {
			url: window.location.href.slice(0, 2048),
			url_length: window.location.href.length,
			title: document.title.slice(0, 500),
			title_length: document.title.length,
			text: '',
			links: [],
			links_total: 0,
			inputs: [],
			inputs_total: 0,
			buttons: [],
			buttons_total: 0,
			meta: {},
			scope_matched: roots.length,
			scope_selectors: matched,
			scope_misses: misses
		};

		// Concatenate the visible text of every matched root, capped.
		let buf = '';
		let totalTextLength = 0;
		for (const r of roots) {
			const text = (r.innerText || '') + '\n';
			totalTextLength += text.length;
			if (buf.length < %d) buf += text.slice(0, %d - buf.length);
		}
		obs.text = buf.slice(0, %d);
		obs.text_length = totalTextLength;
		obs.text_truncated = totalTextLength > %d;

		function linkText(a) {
			const direct = a.textContent.trim();
			if (direct) return direct;
			const aria = a.getAttribute('aria-label');
			if (aria && aria.trim()) return aria.trim();
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
			try {
				const url = new URL(a.href, window.location.href);
				const segs = url.pathname.split('/').filter(Boolean);
				if (segs.length > 0) {
					return decodeURIComponent(segs[segs.length - 1]).replace(/[-_]+/g, ' ').trim();
				}
			} catch (_) {}
			return '';
		}

		function inputLabel(el, root) {
			if (el.id) {
				const labels = [];
				if (root.matches && root.matches('label[for]')) labels.push(root);
				if (root.querySelectorAll) labels.push(...root.querySelectorAll('label[for]'));
				const l = labels.find(label => label.htmlFor === el.id);
				if (l) return l.textContent.trim().slice(0, 80);
			}
			const ariaLabel = el.getAttribute('aria-label');
			if (ariaLabel) return ariaLabel.slice(0, 80);
			const wrapping = el.closest('label');
			if (wrapping && (root === wrapping || root.contains(wrapping))) {
				const c = wrapping.cloneNode(true);
				c.querySelectorAll('input,textarea,select').forEach(i => i.remove());
				return c.textContent.trim().slice(0, 80);
			}
			return '';
		}

		const seenLinks = new Set();
		const seenInputs = new Set();
		const seenButtons = new Set();
		function scopedQuery(root, selector) {
			const found = Array.from(root.querySelectorAll(selector));
			if (root.matches && root.matches(selector)) found.unshift(root);
			return found;
		}

		for (const r of roots) {
			for (const a of scopedQuery(r, 'a[href]')) {
				if (seenLinks.has(a)) continue;
				seenLinks.add(a);
				obs.links_total++;
				if (obs.links.length >= %d) continue;
				const text = linkText(a);
				if (text || a.href) obs.links.push({text: text.slice(0, 100), href: a.getAttribute('href')});
			}
			for (const input of scopedQuery(r, 'input, textarea, select')) {
				if (seenInputs.has(input)) continue;
				seenInputs.add(input);
				obs.inputs_total++;
				if (obs.inputs.length >= %d) continue;
				const t = (input.type || input.tagName.toLowerCase()).toLowerCase();
				const item = {
					id: input.id || '',
					name: input.name || '',
					type: t,
					value: t === 'password' || t === 'file' ? '' : input.value || '',
					placeholder: input.placeholder || ''
				};
				if (t === 'checkbox' || t === 'radio') {
					item.checked = !!input.checked;
					const lbl = inputLabel(input, r);
					if (lbl) item.label = lbl;
				}
				obs.inputs.push(item);
			}
			for (const btn of scopedQuery(r, 'button, input[type=submit], input[type=button], [role=button]')) {
				if (seenButtons.has(btn)) continue;
				seenButtons.add(btn);
				obs.buttons_total++;
				if (obs.buttons.length >= %d) continue;
				obs.buttons.push({
					text: (btn.textContent || btn.value || '').trim().slice(0, 100),
					id: btn.id || '',
					type: btn.type || ''
				});
			}
		}

		obs.interactive = obs.links_total + obs.inputs_total + obs.buttons_total;
		return obs;
	})()`, rootSelsJSON, maxText, maxText, maxText, maxText, maxLinks, maxInputs, maxButtons)

	result, err := evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("failed scoped observation: %w", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected scoped observation result")
	}
	scopeMatched, _ := m["scope_matched"].(float64)
	if scopeMatched == 0 && !allowEmpty {
		return nil, fmt.Errorf("no observation scopes matched: %v; use allow_empty=true to accept an empty scope", sections)
	}
	if scopeMatched == 0 {
		pageState, stateErr := evaluate(`({url:location.href,title:document.title})`)
		if stateErr != nil {
			return nil, stateErr
		}
		state, _ := pageState.(map[string]any)
		url := strVal(state, "url")
		title := strVal(state, "title")
		misses := scopeStringSlice(m["scope_misses"])
		return &Observation{URL: url, ContentTrust: ContentTrustUntrustedPage, TargetID: s.page.TargetID(), FrameID: s.frameID, URLLength: len(url), Title: title, TitleLength: len(title), ScopeMatched: 0, ScopeSelectors: append([]string(nil), sections...), ScopeMisses: misses}, nil
	}

	obs := &Observation{ContentTrust: ContentTrustUntrustedPage}
	obs.TargetID = s.page.TargetID()
	obs.FrameID = s.frameID
	obs.ScopeMatched = int(scopeMatched)
	obs.ScopeSelectors = append([]string(nil), sections...)
	obs.ScopeMisses = scopeStringSlice(m["scope_misses"])
	obs.URL, _ = m["url"].(string)
	obs.Title, _ = m["title"].(string)
	obs.Text, _ = m["text"].(string)
	obs.TextLength = observationInt(m["text_length"])
	obs.TextTruncated, _ = m["text_truncated"].(bool)
	obs.LinksTotal = observationInt(m["links_total"])
	obs.InputsTotal = observationInt(m["inputs_total"])
	obs.ButtonsTotal = observationInt(m["buttons_total"])
	obs.URLLength = observationInt(m["url_length"])
	obs.TitleLength = observationInt(m["title_length"])
	obs.Truncated = obs.TextTruncated || obs.URLLength > len(obs.URL) || obs.TitleLength > len(obs.Title)
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
				obs.Links = append(obs.Links, LinkInfo{
					ContentTrust: ContentTrustUntrustedPage,
					Text:         text,
					Href:         href,
					Cost:         estimateLinkCost(href),
				})
			}
		}
	}
	obs.Truncated = obs.Truncated || obs.LinksTotal > len(obs.Links)

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
				obs.Buttons = append(obs.Buttons, ButtonInfo{
					ContentTrust: ContentTrustUntrustedPage,
					Text:         strVal(bm, "text"),
					ID:           strVal(bm, "id"),
					Type:         btnType,
					Cost:         estimateButtonCost(btnType, strVal(bm, "text")),
				})
			}
		}
	}
	obs.Truncated = obs.Truncated || obs.ButtonsTotal > len(obs.Buttons)

	if cs := s.cookieSummaryInternal(); cs != nil {
		obs.Cookies = cs
	}
	return obs, nil
}

func scopeStringSlice(value any) []string {
	values, _ := value.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if s, ok := value.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
