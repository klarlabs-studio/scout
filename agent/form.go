package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	browse "github.com/felixgeelhaar/scout"
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

		function buildSelector(el) {
			if (el.id) return '#' + CSS.escape(el.id);
			const t = (el.type || '').toLowerCase();
			if ((t==='radio' || t==='checkbox') && el.name) {
				const v = el.value || '';
				if (v) return 'input[type="'+t+'"][name="'+el.name+'"][value="'+v+'"]';
			}
			if (el.name) return el.tagName.toLowerCase() + '[name="' + el.name + '"]';
			const parent = el.closest('form');
			if (parent) {
				const siblings = parent.querySelectorAll(el.tagName.toLowerCase());
				const idx = Array.from(siblings).indexOf(el);
				if (idx >= 0) return (parent.id ? '#' + CSS.escape(parent.id) + ' ' : 'form ') + el.tagName.toLowerCase() + ':nth-of-type(' + (idx+1) + ')';
			}
			return el.tagName.toLowerCase();
		}

		const fields = [];
		const inputs = root.querySelectorAll('input,textarea,select');
		for (const el of inputs) {
			if (el.type === 'hidden' || el.type === 'submit') continue;
			const field = {
				selector: buildSelector(el),
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
	best := MatchFormField(humanName, candidates)
	if best == nil {
		return SemanticFieldResult{
			HumanName: humanName,
			Error:     fmt.Sprintf("no matching field found for %q", humanName),
		}
	}

	switch strings.ToLower(best.Type) {
	case "checkbox":
		return s.fillCheckbox(humanName, *best, value)
	case "radio":
		return s.fillRadio(humanName, *best, value, candidates)
	default:
		return s.fillTextField(humanName, *best, value)
	}
}

func (s *Session) fillTextField(humanName string, field FormFieldInfo, value any) SemanticFieldResult {
	str := fmt.Sprint(value)
	nodeID, err := s.page.QuerySelector(field.Selector)
	if err != nil {
		return SemanticFieldResult{HumanName: humanName, Selector: field.Selector, Type: field.Type, Error: err.Error()}
	}
	sel := browse.NewSelection(s.page, nodeID, field.Selector)
	if err := sel.Input(str); err != nil {
		return SemanticFieldResult{HumanName: humanName, Selector: field.Selector, Type: field.Type, Error: err.Error()}
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
		function buildSelector(el) {
			if (el.id) return '#'+CSS.escape(el.id);
			const t = (el.type || '').toLowerCase();
			if ((t==='radio' || t==='checkbox') && el.name) {
				const v = el.value || '';
				if (v) return 'input[type="'+t+'"][name="'+el.name+'"][value="'+v+'"]';
			}
			if (el.name) return el.tagName.toLowerCase()+'[name="'+el.name+'"]';
			return el.tagName.toLowerCase();
		}
		const fields = [];
		for (const el of root.querySelectorAll('input,textarea,select')) {
			if (el.type==='hidden'||el.type==='submit') continue;
			const f = {selector:buildSelector(el),label:findLabel(el),type:el.type||el.tagName.toLowerCase(),name:el.name||'',id:el.id||'',placeholder:el.placeholder||'',required:el.required||false};
			if (el.tagName==='SELECT') f.options=Array.from(el.options).slice(0,10).map(o=>o.text.trim());
			fields.push(f);
		}
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

	return best
}
