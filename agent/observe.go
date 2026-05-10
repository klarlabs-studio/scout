package agent

import (
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

// observeInternal is the non-locking implementation of Observe.
// Caller must hold s.mu.
func (s *Session) observeInternal() (*Observation, error) {

	js := `(function() {
		const obs = {
			url: window.location.href,
			title: document.title,
			text: document.body ? document.body.innerText.slice(0, 2000) : '',
			links: [],
			inputs: [],
			buttons: [],
			meta: {}
		};

		// Collect links
		for (const a of document.querySelectorAll('a[href]')) {
			const text = a.textContent.trim();
			if (text || a.href) {
				obs.links.push({text: text.slice(0, 100), href: a.getAttribute('href')});
			}
		}

		// Collect inputs
		for (const input of document.querySelectorAll('input, textarea, select')) {
			obs.inputs.push({
				id: input.id || '',
				name: input.name || '',
				type: input.type || input.tagName.toLowerCase(),
				value: input.value || '',
				placeholder: input.placeholder || ''
			});
		}

		// Collect buttons
		for (const btn of document.querySelectorAll('button, input[type=submit], input[type=button], [role=button]')) {
			obs.buttons.push({
				text: (btn.textContent || btn.value || '').trim().slice(0, 100),
				id: btn.id || '',
				type: btn.type || ''
			});
		}

		// Collect meta tags
		for (const meta of document.querySelectorAll('meta[name], meta[property]')) {
			const key = meta.getAttribute('name') || meta.getAttribute('property');
			const val = meta.getAttribute('content');
			if (key && val) obs.meta[key] = val.slice(0, 200);
		}

		// Active tab — [role=tab][aria-selected=true]
		const selectedTab = document.querySelector('[role="tab"][aria-selected="true"]');
		if (selectedTab) {
			obs.active_tab = (selectedTab.textContent || '').trim().slice(0, 80);
			obs.active_tab_id = selectedTab.id || selectedTab.getAttribute('data-tab-id') || selectedTab.getAttribute('aria-controls') || '';
		}

		// Active navigation breadcrumb — [aria-current=page] links + page H1
		const nav = [];
		for (const el of document.querySelectorAll('[aria-current="page"]')) {
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

		obs.interactive = obs.links.length + obs.inputs.length + obs.buttons.length;
		return obs;
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("failed to observe page: %w", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected observation result type")
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
				cost := estimateLinkCost(href)
				obs.Links = append(obs.Links, LinkInfo{Text: text, Href: href, Cost: cost})
			}
		}
	}

	if inputs, ok := m["inputs"].([]any); ok {
		for i, inp := range inputs {
			if i >= maxInputs {
				break
			}
			if im, ok := inp.(map[string]any); ok {
				obs.Inputs = append(obs.Inputs, InputInfo{
					ID:          strVal(im, "id"),
					Name:        strVal(im, "name"),
					Type:        strVal(im, "type"),
					Value:       strVal(im, "value"),
					Placeholder: strVal(im, "placeholder"),
				})
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
				cost := estimateButtonCost(btnType, strVal(bm, "text"))
				obs.Buttons = append(obs.Buttons, ButtonInfo{
					Text: strVal(bm, "text"),
					ID:   strVal(bm, "id"),
					Type: btnType,
					Cost: cost,
				})
			}
		}
	}

	if meta, ok := m["meta"].(map[string]any); ok {
		obs.Meta = make(map[string]string)
		for k, v := range meta {
			if s, ok := v.(string); ok {
				obs.Meta[k] = s
			}
		}
	}

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
	dialogResult, err := s.page.Evaluate(dialogJS)
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
