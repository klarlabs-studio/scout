package agent

import (
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
	LimitChars   int      `json:"limit_chars,omitempty"`
	LinksLimit   int      `json:"links_limit,omitempty"`
	InputsLimit  int      `json:"inputs_limit,omitempty"`
	ButtonsLimit int      `json:"buttons_limit,omitempty"`
}

// ObserveScoped is Observe with per-call section + cap overrides.
// Useful on listing pages where the unscoped observation eats more
// tokens than the agent needs.
func (s *Session) ObserveScoped(opts ObserveOptions) (*Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
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
		return s.observeInternal()
	}
	return s.observeWithinSections(opts.Sections)
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

// observeWithinSections runs an observation scoped to the union of
// section roots, then trims by the (already-narrowed) contentOpts.
// Caller must hold s.mu.
func (s *Session) observeWithinSections(sections []string) (*Observation, error) {
	rootSelsJSON := sectionRootJS(sections)
	maxText := s.contentOpts.MaxLength
	if maxText == 0 {
		maxText = 4000
	}

	js := fmt.Sprintf(`(function() {
		const selectors = %s;
		const roots = [];
		const seen = new Set();
		for (const sel of selectors) {
			try {
				for (const el of document.querySelectorAll(sel)) {
					if (!seen.has(el)) { seen.add(el); roots.push(el); }
				}
			} catch (_) {}
		}
		if (roots.length === 0) return null;

		const obs = {
			url: window.location.href,
			title: document.title,
			text: '',
			links: [],
			inputs: [],
			buttons: [],
			meta: {},
			scope_matched: roots.length
		};

		// Concatenate the visible text of every matched root, capped.
		let buf = '';
		for (const r of roots) {
			buf += (r.innerText || '') + '\n';
			if (buf.length >= %d) break;
		}
		obs.text = buf.slice(0, %d);

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

		const seenLinks = new Set();
		const seenInputs = new Set();
		const seenButtons = new Set();

		for (const r of roots) {
			for (const a of r.querySelectorAll('a[href]')) {
				if (seenLinks.has(a)) continue;
				seenLinks.add(a);
				const text = linkText(a);
				if (text || a.href) obs.links.push({text: text.slice(0, 100), href: a.getAttribute('href')});
			}
			for (const input of r.querySelectorAll('input, textarea, select')) {
				if (seenInputs.has(input)) continue;
				seenInputs.add(input);
				const t = (input.type || input.tagName.toLowerCase()).toLowerCase();
				const item = {
					id: input.id || '',
					name: input.name || '',
					type: t,
					value: input.value || '',
					placeholder: input.placeholder || ''
				};
				if (t === 'checkbox' || t === 'radio') {
					item.checked = !!input.checked;
					const lbl = inputLabel(input);
					if (lbl) item.label = lbl;
				}
				obs.inputs.push(item);
			}
			for (const btn of r.querySelectorAll('button, input[type=submit], input[type=button], [role=button]')) {
				if (seenButtons.has(btn)) continue;
				seenButtons.add(btn);
				obs.buttons.push({
					text: (btn.textContent || btn.value || '').trim().slice(0, 100),
					id: btn.id || '',
					type: btn.type || ''
				});
			}
		}

		obs.interactive = obs.links.length + obs.inputs.length + obs.buttons.length;
		return obs;
	})()`, rootSelsJSON, maxText, maxText)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("failed scoped observation: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("no sections matched %v", sections)
	}
	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected scoped observation result")
	}

	maxLinks := s.contentOpts.MaxLinks
	if maxLinks == 0 {
		maxLinks = 25
	}
	maxInputs := s.contentOpts.MaxInputs
	if maxInputs == 0 {
		maxInputs = 20
	}
	maxButtons := s.contentOpts.MaxButtons
	if maxButtons == 0 {
		maxButtons = 15
	}

	obs := &Observation{}
	obs.URL, _ = m["url"].(string)
	obs.Title, _ = m["title"].(string)
	obs.Text, _ = m["text"].(string)
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
					Text: text,
					Href: href,
					Cost: estimateLinkCost(href),
				})
			}
		}
	}

	if inputs, ok := m["inputs"].([]any); ok {
		for i, inp := range inputs {
			if i >= maxInputs {
				break
			}
			if im, ok := inp.(map[string]any); ok {
				info := InputInfo{
					ID:          strVal(im, "id"),
					Name:        strVal(im, "name"),
					Type:        strVal(im, "type"),
					Value:       strVal(im, "value"),
					Placeholder: strVal(im, "placeholder"),
					Label:       strVal(im, "label"),
				}
				if c, ok := im["checked"].(bool); ok {
					info.Checked = &c
				}
				obs.Inputs = append(obs.Inputs, info)
			}
		}
	}

	if buttons, ok := m["buttons"].([]any); ok {
		for i, btn := range buttons {
			if i >= maxButtons {
				break
			}
			if bm, ok := btn.(map[string]any); ok {
				btnType := strVal(bm, "type")
				obs.Buttons = append(obs.Buttons, ButtonInfo{
					Text: strVal(bm, "text"),
					ID:   strVal(bm, "id"),
					Type: btnType,
					Cost: estimateButtonCost(btnType, strVal(bm, "text")),
				})
			}
		}
	}

	if cs := s.cookieSummaryInternal(); cs != nil {
		obs.Cookies = cs
	}
	return obs, nil
}
