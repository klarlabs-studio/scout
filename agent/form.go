package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	browse "go.klarlabs.de/scout"
)

// DiscoverForm analyzes form fields on the page and returns their labels and selectors.
// If formSelector is empty, discovers all forms on the page.
func (s *Session) DiscoverForm(formSelector string) (*FormDiscoveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	sel := "document.body"
	if formSelector != "" {
		selJSON, _ := json.Marshal(formSelector)
		sel = fmt.Sprintf("document.querySelector(%s)", selJSON)
	}

	js := fmt.Sprintf(`(function() {
		const root = %s;
		if (!root) return null;

		function findLabel(el) {
			if (el.id) {
				const label = document.querySelector('label[for="' + CSS.escape(el.id) + '"]');
				if (label) return label.textContent.trim();
			}
			const ariaLabel = el.getAttribute('aria-label');
			if (ariaLabel) return ariaLabel;
			const labelledBy = el.getAttribute('aria-labelledby');
			if (labelledBy) {
				const ref = document.getElementById(labelledBy);
				if (ref) return ref.textContent.trim();
			}
			const parent = el.closest('label');
			if (parent) {
				const clone = parent.cloneNode(true);
				const inputs = clone.querySelectorAll('input,textarea,select');
				inputs.forEach(i => i.remove());
				return clone.textContent.trim();
			}
			if (el.placeholder) return el.placeholder;
			const prev = el.previousElementSibling;
			if (prev && ['LABEL','SPAN','DIV','P','TD','TH'].includes(prev.tagName)) {
				return prev.textContent.trim();
			}
			return el.name || el.id || '';
		}

		// uniquePath walks parents up to the nearest stable anchor (id or
		// document root) and emits a CSS path with :nth-of-type so the
		// resulting selector matches exactly one element. Required for
		// Vue v-model / React forms where inputs lack id and name.
		function uniquePath(el) {
			const segments = [];
			let cur = el;
			while (cur && cur.nodeType === 1 && cur !== document.documentElement) {
				if (cur.id) {
					segments.unshift('#' + CSS.escape(cur.id));
					break;
				}
				const tag = cur.tagName.toLowerCase();
				let idx = 1;
				let sib = cur.previousElementSibling;
				while (sib) {
					if (sib.tagName === cur.tagName) idx++;
					sib = sib.previousElementSibling;
				}
				segments.unshift(tag + ':nth-of-type(' + idx + ')');
				cur = cur.parentElement;
			}
			return segments.join(' > ');
		}

		function buildSelector(el) {
			if (el.id) return '#' + CSS.escape(el.id);
			const t = (el.type || '').toLowerCase();
			if ((t==='radio' || t==='checkbox') && el.name) {
				const v = el.value || '';
				if (v) return 'input[type="'+t+'"][name="'+el.name+'"][value="'+v+'"]';
			}
			if (el.name) {
				const candidate = el.tagName.toLowerCase() + '[name="' + el.name + '"]';
				if (document.querySelectorAll(candidate).length === 1) return candidate;
			}
			return uniquePath(el);
		}

		const fields = [];

		// Deep query: collects matching elements from "start" AND every
		// shadow root reachable below it. Lets us pull inputs out of
		// Lit / Vue / React custom elements whose internals are
		// invisible to querySelectorAll alone. The walker also descends
		// into slot assignments implicitly via getRootNode chains.
		function deepQueryAll(start, selectorList) {
			const out = [];
			const seen = new Set();
			function walk(node) {
				if (!node || seen.has(node)) return;
				seen.add(node);
				try {
					node.querySelectorAll(selectorList).forEach(el => out.push(el));
				} catch (_) { /* DocumentFragments without querySelectorAll */ }
				const candidates = node.querySelectorAll ? node.querySelectorAll('*') : [];
				for (const el of candidates) {
					if (el.shadowRoot) walk(el.shadowRoot);
				}
			}
			walk(start);
			return out;
		}

		// pierceSelector lets the fill stage find the same element on a
		// later DOM tick even if it's behind a shadow boundary. We tag
		// each discovered input with a stable data-scout-id and use
		// the attribute selector [data-scout-id="..."]. The Go side
		// runs QuerySelectorPiercing which walks the flattened DOM
		// tree to resolve it across shadow boundaries.
		let scoutCounter = (window.__scoutFieldCounter || 0);
		function pierceSelector(el) {
			let id = el.getAttribute('data-scout-id');
			if (!id) {
				id = 'f' + (++scoutCounter);
				el.setAttribute('data-scout-id', id);
			}
			return '[data-scout-id="' + id + '"]';
		}
		window.__scoutFieldCounter = scoutCounter;

		const inputs = deepQueryAll(root, 'input,textarea,select');
		for (const el of inputs) {
			if (el.type === 'hidden' || el.type === 'submit') continue;
			// Prefer the standard heuristic when the element is in the
			// document scope (cheap, no DOM mutation). Fall back to the
			// pierce-tag for elements behind a shadow boundary so the
			// fill path can locate them.
			let sel;
			const inMainDoc = el.getRootNode() === document;
			if (inMainDoc) {
				sel = buildSelector(el);
				if (document.querySelectorAll(sel).length !== 1) {
					sel = uniquePath(el);
				}
				if (document.querySelectorAll(sel).length !== 1) {
					sel = pierceSelector(el);
				}
			} else {
				sel = pierceSelector(el);
			}
			const field = {
				selector: sel,
				label: findLabel(el),
				type: el.type || el.tagName.toLowerCase(),
				name: el.name || '',
				id: el.id || '',
				placeholder: el.placeholder || '',
				required: el.required || false
			};
			if (el.tagName === 'SELECT') {
				field.options = Array.from(el.options).slice(0, 10).map(o => o.text.trim());
			}
			fields.push(field);
		}

		const form = root.tagName === 'FORM' ? root : root.querySelector('form');
		return JSON.stringify({
			formSelector: form ? buildSelector(form) : '',
			action: form ? form.action : '',
			method: form ? form.method : '',
			fields: fields
		});
	})()`, sel)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("form discovery failed: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("no form found")
	}

	str, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected form discovery result")
	}

	var discovery FormDiscoveryResult
	if err := json.Unmarshal([]byte(str), &discovery); err != nil {
		return nil, fmt.Errorf("failed to parse form discovery: %w", err)
	}

	return &discovery, nil
}

// FillFormSemantic fills form fields using human-readable names instead of CSS selectors.
// Keys are names like "Email", "Password", "First Name".
// The method auto-discovers form fields and matches by label, name, placeholder, and id.
func (s *Session) FillFormSemantic(fields map[string]string) (*SemanticFillResult, error) {
	any := make(map[string]any, len(fields))
	for k, v := range fields {
		any[k] = v
	}
	return s.FillFormSemanticAny(any)
}

// FillFormSemanticAny is like FillFormSemantic but accepts any value type per field.
// Booleans toggle checkboxes (or set radios when paired with a string value matching a label).
// Strings fill text inputs, textareas, and select options.
//
// For every field the result includes the value that was set, the value re-read from
// the DOM after dispatching input/change events, and a warning when the framework's
// reactive binding (Vue v-model / React onChange) didn't pick up the change.
func (s *Session) FillFormSemanticAny(fields map[string]any) (*SemanticFillResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	discovery, err := s.discoverFormInternal("")
	if err != nil {
		return nil, err
	}

	result := &SemanticFillResult{
		Fields:  make([]SemanticFieldResult, 0, len(fields)),
		Success: true,
	}

	for humanName, value := range fields {
		fr := s.fillSemanticField(humanName, value, discovery.Fields)
		if !fr.Success {
			result.Success = false
		}
		result.Fields = append(result.Fields, fr)
	}

	return result, nil
}

// fillSemanticField fills one field and returns a structured outcome.
// Caller must hold s.mu.
func (s *Session) fillSemanticField(humanName string, value any, candidates []FormFieldInfo) SemanticFieldResult {
	best, score := MatchFormFieldWithScore(humanName, candidates)
	if best == nil {
		return SemanticFieldResult{
			HumanName: humanName,
			Error:     fmt.Sprintf("no matching field found for %q", humanName),
		}
	}

	var res SemanticFieldResult
	switch strings.ToLower(best.Type) {
	case "checkbox":
		res = s.fillCheckbox(humanName, *best, value)
	case "radio":
		res = s.fillRadio(humanName, *best, value, candidates)
	default:
		res = s.fillTextField(humanName, *best, value)
	}
	res.MatchConfidence = score
	// Low-confidence matches (placeholder/type hint fallback only) are
	// almost always a sign that the form lacks distinguishing labels —
	// surface a warning so callers don't act on a guess. Doesn't fail
	// the fill (the value did land somewhere), but flags it loudly.
	if score > 0 && score < 50 && res.Warning == "" && res.Error == "" {
		res.Warning = fmt.Sprintf("low-confidence field match (score %d/100) — verify before submitting", score)
	}
	return res
}

func (s *Session) fillTextField(humanName string, field FormFieldInfo, value any) SemanticFieldResult {
	str := fmt.Sprint(value)
	var sel *browse.Selection
	if err := s.withStaleNodeRetry(field.Selector, func(nodeID int64) error {
		sel = browse.NewSelection(s.page, nodeID, field.Selector)
		return sel.Input(str)
	}); err != nil {
		return SemanticFieldResult{HumanName: humanName, Selector: field.Selector, Type: field.Type, Error: s.decodeNodeError(field.Selector, err).Error()}
	}
	// Dispatch input + change so frameworks update their bound state.
	dispatchEvents(s.page, field.Selector, "input", "change")
	observed, _ := sel.Value()
	reactive := observed == str
	res := SemanticFieldResult{
		HumanName:         humanName,
		Selector:          field.Selector,
		Type:              field.Type,
		Value:             str,
		ValueObserved:     observed,
		FrameworkReactive: reactive,
		Success:           true,
	}
	if !reactive {
		res.Warning = "DOM value matched but observed value differs — framework binding may not have updated"
	}
	return res
}

func (s *Session) fillCheckbox(humanName string, field FormFieldInfo, value any) SemanticFieldResult {
	want, ok := toBool(value)
	if !ok {
		return SemanticFieldResult{
			HumanName: humanName,
			Selector:  field.Selector,
			Type:      field.Type,
			Error:     fmt.Sprintf("checkbox %q expects bool, got %T", humanName, value),
		}
	}

	checked, err := readChecked(s.page, field.Selector)
	if err != nil {
		return SemanticFieldResult{HumanName: humanName, Selector: field.Selector, Type: field.Type, Error: err.Error()}
	}
	wantStr := boolStr(want)

	if checked == want {
		return SemanticFieldResult{
			HumanName: humanName, Selector: field.Selector, Type: field.Type,
			Value: wantStr, ValueObserved: wantStr, FrameworkReactive: true, Success: true,
		}
	}

	if err := clickCheckbox(s.page, field.Selector); err != nil {
		return SemanticFieldResult{HumanName: humanName, Selector: field.Selector, Type: field.Type, Error: err.Error()}
	}
	dispatchEvents(s.page, field.Selector, "input", "change")

	observed, _ := readChecked(s.page, field.Selector)
	reactive := observed == want
	res := SemanticFieldResult{
		HumanName:         humanName,
		Selector:          field.Selector,
		Type:              field.Type,
		Value:             wantStr,
		ValueObserved:     boolStr(observed),
		FrameworkReactive: reactive,
		Success:           true,
	}
	if !reactive {
		res.Warning = "checkbox click fired but observed checked state didn't change — framework binding may not have updated"
		res.Success = false
	}
	return res
}

func (s *Session) fillRadio(humanName string, field FormFieldInfo, value any, candidates []FormFieldInfo) SemanticFieldResult {
	str := fmt.Sprint(value)
	// Radio set: pick the radio whose label matches str within the same name group.
	target := field
	for _, c := range candidates {
		if strings.EqualFold(c.Type, "radio") && c.Name == field.Name {
			if strings.EqualFold(c.Label, str) || strings.EqualFold(c.ID, str) {
				target = c
				break
			}
		}
	}
	if err := clickCheckbox(s.page, target.Selector); err != nil {
		return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Error: err.Error()}
	}
	dispatchEvents(s.page, target.Selector, "input", "change")

	observed, _ := readChecked(s.page, target.Selector)
	res := SemanticFieldResult{
		HumanName:         humanName,
		Selector:          target.Selector,
		Type:              target.Type,
		Value:             str,
		ValueObserved:     boolStr(observed),
		FrameworkReactive: observed,
		Success:           observed,
	}
	if !observed {
		res.Warning = "radio click fired but observed checked state is false — framework binding may not have updated"
	}
	return res
}

func toBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "on", "1", "checked":
			return true, true
		case "false", "no", "off", "0", "unchecked", "":
			return false, true
		}
	case float64:
		return x != 0, true
	case int:
		return x != 0, true
	}
	return false, false
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func dispatchEvents(p *browse.Page, selector string, events ...string) {
	selJSON, _ := json.Marshal(selector)
	for _, ev := range events {
		evJSON, _ := json.Marshal(ev)
		js := fmt.Sprintf(`(function(){const el=document.querySelector(%s);if(!el)return false;el.dispatchEvent(new Event(%s,{bubbles:true,cancelable:true}));return true;})()`, selJSON, evJSON)
		_, _ = p.Evaluate(js)
	}
}

func readChecked(p *browse.Page, selector string) (bool, error) {
	selJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function(){const el=document.querySelector(%s);return el?!!el.checked:false;})()`, selJSON)
	r, err := p.Evaluate(js)
	if err != nil {
		return false, err
	}
	if b, ok := r.(bool); ok {
		return b, nil
	}
	return false, nil
}

func clickCheckbox(p *browse.Page, selector string) error {
	selJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function(){const el=document.querySelector(%s);if(!el)return false;el.scrollIntoView({block:'center'});el.click();return true;})()`, selJSON)
	r, err := p.Evaluate(js)
	if err != nil {
		return err
	}
	if b, ok := r.(bool); ok && !b {
		return fmt.Errorf("element not found for click")
	}
	return nil
}

// discoverFormInternal is the non-locking version of DiscoverForm.
func (s *Session) discoverFormInternal(formSelector string) (*FormDiscoveryResult, error) {
	sel := "document.body"
	if formSelector != "" {
		selJSON, _ := json.Marshal(formSelector)
		sel = fmt.Sprintf("document.querySelector(%s)", selJSON)
	}

	// Same JS as DiscoverForm but called without taking the lock
	js := fmt.Sprintf(`(function() {
		const root = %s;
		if (!root) return null;
		function findLabel(el) {
			if (el.id) { const l = document.querySelector('label[for="'+CSS.escape(el.id)+'"]'); if (l) return l.textContent.trim(); }
			if (el.getAttribute('aria-label')) return el.getAttribute('aria-label');
			const lb = el.getAttribute('aria-labelledby'); if (lb) { const r = document.getElementById(lb); if (r) return r.textContent.trim(); }
			const p = el.closest('label'); if (p) { const c = p.cloneNode(true); c.querySelectorAll('input,textarea,select').forEach(i => i.remove()); return c.textContent.trim(); }
			if (el.placeholder) return el.placeholder;
			const prev = el.previousElementSibling; if (prev && ['LABEL','SPAN','DIV','P','TD','TH'].includes(prev.tagName)) return prev.textContent.trim();
			return el.name || el.id || '';
		}
		function uniquePath(el) {
			const segs = [];
			let cur = el;
			while (cur && cur.nodeType === 1 && cur !== document.documentElement) {
				if (cur.id) { segs.unshift('#' + CSS.escape(cur.id)); break; }
				const tag = cur.tagName.toLowerCase();
				let idx = 1;
				let sib = cur.previousElementSibling;
				while (sib) { if (sib.tagName === cur.tagName) idx++; sib = sib.previousElementSibling; }
				segs.unshift(tag + ':nth-of-type(' + idx + ')');
				cur = cur.parentElement;
			}
			return segs.join(' > ');
		}
		function buildSelector(el) {
			if (el.id) return '#'+CSS.escape(el.id);
			const t = (el.type || '').toLowerCase();
			if ((t==='radio' || t==='checkbox') && el.name) {
				const v = el.value || '';
				if (v) return 'input[type="'+t+'"][name="'+el.name+'"][value="'+v+'"]';
			}
			if (el.name) {
				const cand = el.tagName.toLowerCase()+'[name="'+el.name+'"]';
				if (document.querySelectorAll(cand).length === 1) return cand;
			}
			return uniquePath(el);
		}
		// Walk shadow roots (mirror of DiscoverForm).
		function deepQueryAll(start, sel) {
			const out = [], seen = new Set();
			(function walk(n) {
				if (!n || seen.has(n)) return;
				seen.add(n);
				try { n.querySelectorAll(sel).forEach(e => out.push(e)); } catch(_){}
				const all = n.querySelectorAll ? n.querySelectorAll('*') : [];
				for (const el of all) if (el.shadowRoot) walk(el.shadowRoot);
			})(start);
			return out;
		}
		let _c = (window.__scoutFieldCounter || 0);
		function pierceSel(el) {
			let id = el.getAttribute('data-scout-id');
			if (!id) { id = 'f' + (++_c); el.setAttribute('data-scout-id', id); }
			return '[data-scout-id="' + id + '"]';
		}
		const fields = [];
		for (const el of deepQueryAll(root, 'input,textarea,select')) {
			if (el.type==='hidden'||el.type==='submit') continue;
			let s;
			if (el.getRootNode() === document) {
				s = buildSelector(el);
				if (document.querySelectorAll(s).length !== 1) s = uniquePath(el);
				if (document.querySelectorAll(s).length !== 1) s = pierceSel(el);
			} else {
				s = pierceSel(el);
			}
			const f = {selector:s,label:findLabel(el),type:el.type||el.tagName.toLowerCase(),name:el.name||'',id:el.id||'',placeholder:el.placeholder||'',required:el.required||false};
			if (el.tagName==='SELECT') f.options=Array.from(el.options).slice(0,10).map(o=>o.text.trim());
			fields.push(f);
		}
		window.__scoutFieldCounter = _c;
		const form = root.tagName==='FORM'?root:root.querySelector('form');
		return JSON.stringify({formSelector:form?buildSelector(form):'',action:form?form.action:'',method:form?form.method:'',fields:fields});
	})()`, sel)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("no form found")
	}
	str, _ := result.(string)
	var discovery FormDiscoveryResult
	if err := json.Unmarshal([]byte(str), &discovery); err != nil {
		return nil, err
	}
	return &discovery, nil
}

// MatchFormField finds the best matching field for a human-readable name using
// weighted fuzzy matching on label, name, id, placeholder, and type.
// Returns nil if no match is found. Exported for direct testing and reuse.
func MatchFormField(humanName string, fields []FormFieldInfo) *FormFieldInfo {
	best, _ := MatchFormFieldWithScore(humanName, fields)
	return best
}

// MatchFormFieldWithScore is like MatchFormField but also returns the
// match score (0–100). Callers can use this to detect low-confidence
// resolutions and ask the user for confirmation before acting on them.
func MatchFormFieldWithScore(humanName string, fields []FormFieldInfo) (*FormFieldInfo, int) {
	humanLower := strings.ToLower(humanName)
	var best *FormFieldInfo
	bestScore := 0

	for i := range fields {
		f := &fields[i]
		score := 0

		// Exact label match (highest priority)
		if strings.EqualFold(f.Label, humanName) {
			score = 100
		} else if strings.Contains(strings.ToLower(f.Label), humanLower) {
			score = 80
		}

		// Name/ID match
		if strings.EqualFold(f.Name, humanName) || strings.EqualFold(f.ID, humanName) {
			score = max(score, 90)
		} else if strings.Contains(strings.ToLower(f.Name), humanLower) {
			score = max(score, 70)
		} else if strings.Contains(strings.ToLower(f.ID), humanLower) {
			score = max(score, 60)
		}

		// Placeholder match
		if strings.Contains(strings.ToLower(f.Placeholder), humanLower) {
			score = max(score, 50)
		}

		// Type hint match (e.g., "email" matches type="email")
		if strings.EqualFold(f.Type, humanName) {
			score = max(score, 40)
		}

		if score > bestScore {
			bestScore = score
			best = f
		}
	}

	return best, bestScore
}
