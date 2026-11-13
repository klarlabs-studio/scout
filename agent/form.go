package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	browse "go.klarlabs.de/scout"
)

// DiscoverForm analyzes form fields on the page and returns their labels and selectors.
// If formSelector is empty, discovers all forms on the page.
func (s *Session) DiscoverForm(formSelector string) (*FormDiscoveryResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.DiscoverFormContext(ctx, formSelector)
}

// DiscoverFormContext discovers form fields under the caller's deadline.
func (s *Session) DiscoverFormContext(ctx context.Context, formSelector string) (*FormDiscoveryResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.discoverFormInternalContext(ctx, formSelector)
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
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.FillFormSemanticAnyContext(ctx, fields)
}

// FillFormSemanticAnyContext performs semantic discovery, matching, field
// actions, and readback under one caller deadline and one session lock.
func (s *Session) FillFormSemanticAnyContext(ctx context.Context, fields map[string]any) (*SemanticFillResult, error) {
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
	start, before := s.traceBeforeActionContext(ctx)
	discovery, err := s.discoverFormInternalContext(ctx, "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	result := &SemanticFillResult{ContentTrust: ContentTrustUntrustedPage, TargetID: s.page.TargetID(), Fields: make([]SemanticFieldResult, 0, len(fields)), Success: true}
	mutationsDispatched := false
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			if mutationsDispatched {
				return nil, s.semanticFillOutcomeAfterDispatch(result, name, "semantic form fill was interrupted after earlier fields changed", err)
			}
			return nil, err
		}
		fieldResult := s.fillSemanticFieldContext(ctx, name, fields[name], discovery.Fields, nil)
		fieldResult.ContentTrust = ContentTrustUntrustedPage
		if fieldResult.Outcome == string(OutcomeUnknown) {
			cause := ctx.Err()
			if cause == nil {
				cause = errors.New(fieldResult.Error)
			}
			fieldResult.Success = false
			result.Success = false
			result.Fields = append(result.Fields, fieldResult)
			return nil, s.semanticFillOutcomeAfterDispatch(result, fieldResult.Selector, fieldResult.Error, cause)
		}
		if fieldResult.Outcome == "value_verified" || fieldResult.Outcome == "value_mismatch" {
			mutationsDispatched = true
		}
		if err := ctx.Err(); err != nil {
			if mutationsDispatched {
				if fieldResult.HumanName != "" {
					result.Fields = append(result.Fields, fieldResult)
				}
				return nil, s.semanticFillOutcomeAfterDispatch(result, fieldResult.Selector, "semantic form fill was interrupted after a field mutation", err)
			}
			return nil, err
		}
		if !fieldResult.Success {
			result.Success = false
		}
		result.Fields = append(result.Fields, fieldResult)
	}
	result.Outcome = string(OutcomePostconditionVerified)
	result.Evidence = "all semantic form fields were processed and their outcomes are included"
	if !result.Success {
		result.Outcome = string(OutcomeValueMismatch)
		result.RecoveryHint = "Inspect field-level outcomes and correct missing, invalid, or mismatched controls before continuing."
		for _, field := range result.Fields {
			if field.Outcome == string(OutcomeActionFailed) {
				result.Outcome = string(OutcomeActionFailed)
				break
			}
		}
	}
	s.traceAfterActionContext(ctx, start, before, "fill_form_semantic", "", "", "", nil)
	result.OperationOrdinal = s.operationOrdinalForResult("fill_form_semantic", "")
	result.Receipt = s.newActionReceipt("fill_form_semantic", "", result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Success, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	return result, nil
}

func (s *Session) semanticFillOutcomeAfterDispatch(result *SemanticFillResult, target, evidence string, cause error) error {
	if result != nil {
		result.Success = false
		result.Outcome = string(OutcomeUnknown)
		result.Evidence = evidence
		result.RecoveryHint = "Inspect each field outcome and current form values before retrying; some earlier fields may already have changed."
	}
	err := s.actionOutcomeAfterDispatch("fill_form_semantic", target, evidence, cause)
	if outcome, ok := err.(*ActionOutcomeError); ok {
		outcome.PartialResult = result
	}
	return err
}

func (s *Session) fillSemanticFieldContext(ctx context.Context, humanName string, value any, candidates []FormFieldInfo, beforeMutation func() error) SemanticFieldResult {
	best, score := MatchFormFieldWithScore(humanName, candidates)
	if best == nil {
		return SemanticFieldResult{HumanName: humanName, Outcome: "action_failed", RecoveryHint: "Inspect the form labels and choose a field name that maps to an available control.", Error: fmt.Sprintf("no matching field found for %q", humanName)}
	}
	target := *best
	typeName := strings.ToLower(best.Type)
	want := fmt.Sprint(value)
	if typeName == "radio" {
		matched := radioMatches(target, want)
		for _, candidate := range candidates {
			if strings.EqualFold(candidate.Type, "radio") && candidate.Name == best.Name && radioMatches(candidate, want) {
				target, matched = candidate, true
				break
			}
		}
		if !matched {
			return SemanticFieldResult{HumanName: humanName, Selector: best.Selector, Type: best.Type, Value: want, MatchConfidence: score, Outcome: "action_failed", Error: fmt.Sprintf("no radio option in group %q matches %q", best.Name, want)}
		}
	}
	nodeID, err := s.querySelectorForCurrentContext(ctx, target.Selector)
	if err != nil {
		return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, MatchConfidence: score, Outcome: "action_failed", Error: s.decodeNodeError(target.Selector, err).Error(), RecoveryHint: "Inspect the form and confirm the selected control is still present."}
	}
	selection := browse.NewSelection(s.page, nodeID, target.Selector)
	observed, reactive, err := "", false, error(nil)
	selectedValue, selectedText := "", ""
	mutationAttempted := false
	switch typeName {
	case "checkbox", "radio":
		checkedValue, callErr := formNodeCallContext(ctx, s.page, nodeID, `function(){return !!this.checked}`, nil)
		if callErr != nil {
			err = callErr
			break
		}
		checked, _ := checkedValue.(bool)
		wantChecked := typeName == "radio"
		if typeName == "checkbox" {
			wantChecked, _ = toBool(value)
		}
		if typeName == "checkbox" {
			if _, ok := toBool(value); !ok {
				return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, MatchConfidence: score, Outcome: "action_failed", Error: fmt.Sprintf("checkbox %q expects bool, got %T", humanName, value)}
			}
		}
		if checked != wantChecked {
			mutationAttempted = true
			if beforeMutation != nil {
				if err := beforeMutation(); err != nil {
					return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: string(OutcomeActionFailed), Error: err.Error()}
				}
			}
			_, err = formNodeCallContext(ctx, s.page, nodeID, `function(){this.click();this.dispatchEvent(new Event('input',{bubbles:true,cancelable:true}));this.dispatchEvent(new Event('change',{bubbles:true,cancelable:true}));return !!this.checked}`, nil)
			if err != nil {
				break
			}
		}
		read, callErr := formNodeCallContext(ctx, s.page, nodeID, `function(){return !!this.checked}`, nil)
		if callErr != nil {
			err = callErr
			break
		}
		checked, _ = read.(bool)
		observed, reactive = boolStr(checked), checked == wantChecked
	case "select", "select-one", "select-multiple":
		mutationAttempted = true
		if beforeMutation != nil {
			if err := beforeMutation(); err != nil {
				return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: string(OutcomeActionFailed), Error: err.Error()}
			}
		}
		selected, callErr := formNodeCallContext(ctx, s.page, nodeID, `function(want){if(this.tagName!=='SELECT')return null;for(const option of this.options){if(option.text.trim()===want||option.value===want){const setter=Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype,'value').set;setter.call(this,option.value);this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}));return {value:String(this.value),text:option.text.trim()}}}return null}`, []any{want})
		if callErr != nil {
			err = callErr
			break
		}
		if selected == nil {
			mutationAttempted = false
			err = fmt.Errorf("option %q not found in %s", want, target.Selector)
			break
		}
		selectedValues, _ := selected.(map[string]any)
		selectedValue, _ = selectedValues["value"].(string)
		selectedText, _ = selectedValues["text"].(string)
	default:
		mutationAttempted = true
		if err = selection.InputContextWithGuard(ctx, want, beforeMutation); err != nil {
			break
		}
		if beforeMutation != nil {
			if err = beforeMutation(); err != nil {
				break
			}
		}
		_, err = formNodeCallContext(ctx, s.page, nodeID, `function(){this.dispatchEvent(new Event('input',{bubbles:true,cancelable:true}));this.dispatchEvent(new Event('change',{bubbles:true,cancelable:true}));this.dispatchEvent(new Event('blur',{bubbles:true,cancelable:true}));const proto=this.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;const getter=Object.getOwnPropertyDescriptor(proto,'value');return getter&&getter.get?String(getter.get.call(this)):String(this.value||'')}`, nil)
		if err == nil {
			actual, e := formNodeCallContext(ctx, s.page, nodeID, `function(){return String(this.value||'')}`, nil)
			if e != nil {
				err = e
			} else {
				observed, _ = actual.(string)
				reactive = observed == want
			}
		}
	}
	if err == nil {
		err = s.page.WaitStableContext(ctx, 50*time.Millisecond)
	}
	if ctx.Err() != nil {
		if mutationAttempted {
			return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: string(OutcomeUnknown), Error: "field mutation may have been dispatched, but its value could not be verified", RecoveryHint: "Inspect this field and the page before retrying; the requested value may already be present."}
		}
		return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: "action_failed", Error: ctx.Err().Error()}
	}
	if err != nil {
		if mutationAttempted {
			return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: string(OutcomeUnknown), Error: err.Error(), RecoveryHint: "Inspect this field and the page before retrying; the requested value may already be present."}
		}
		return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: "action_failed", Error: err.Error(), RecoveryHint: "Inspect the field and application state, then retry after the form is ready."}
	}
	if refreshed, refreshErr := s.querySelectorForCurrentContext(ctx, target.Selector); refreshErr == nil {
		nodeID = refreshed
	}
	switch typeName {
	case "checkbox", "radio":
		read, readErr := formNodeCallContext(ctx, s.page, nodeID, `function(){return !!this.checked}`, nil)
		if readErr != nil {
			err = readErr
		} else {
			checked, _ := read.(bool)
			observed = boolStr(checked)
			expected := typeName == "radio"
			if typeName == "checkbox" {
				expected, _ = toBool(value)
			}
			reactive = checked == expected
		}
	case "select":
		read, readErr := formNodeCallContext(ctx, s.page, nodeID, `function(){return String(this.value)}`, nil)
		if readErr != nil {
			err = readErr
		} else {
			observed, _ = read.(string)
			reactive = observed == selectedValue && (selectedText == want || selectedValue == want)
		}
	default:
		read, readErr := formNodeCallContext(ctx, s.page, nodeID, `function(){const proto=this.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;const getter=Object.getOwnPropertyDescriptor(proto,'value');return getter&&getter.get?String(getter.get.call(this)):String(this.value||'')}`, nil)
		if readErr != nil {
			err = readErr
		} else {
			observed, _ = read.(string)
			reactive = observed == want
		}
	}
	if err != nil {
		if mutationAttempted {
			return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: string(OutcomeUnknown), Error: err.Error(), RecoveryHint: "Inspect this field and the page before retrying; the requested value may already be present."}
		}
		return SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, MatchConfidence: score, Outcome: "action_failed", Error: err.Error()}
	}
	result := SemanticFieldResult{HumanName: humanName, Selector: target.Selector, Type: target.Type, Value: want, ValueObserved: observed, FrameworkReactive: reactive, Success: reactive, MatchConfidence: score}
	if reactive {
		result.Outcome = "value_verified"
	} else {
		result.Outcome = "value_mismatch"
		result.Error = fmt.Sprintf("requested value %q but observed %q", want, observed)
		result.RecoveryHint = "Inspect the field and application state; retry the fill after the form is ready."
		result.Warning = "observed field value differs from the requested value"
	}
	if score > 0 && score < 50 && result.Warning == "" && result.Error == "" {
		result.Warning = fmt.Sprintf("low-confidence field match (score %d/100) — verify before submitting", score)
	}
	return result
}

func formNodeCallContext(ctx context.Context, page *browse.Page, nodeID int64, declaration string, args []any) (any, error) {
	objectID, err := page.ResolveNodeContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"functionDeclaration": declaration, "objectId": objectID, "returnByValue": true, "awaitPromise": true}
	if len(args) > 0 {
		cdpArgs := make([]map[string]any, 0, len(args))
		for _, arg := range args {
			cdpArgs = append(cdpArgs, map[string]any{"value": arg})
		}
		params["arguments"] = cdpArgs
	}
	raw, err := page.CallContext(ctx, "Runtime.callFunctionOn", params)
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.ExceptionDetails != nil {
		return nil, fmt.Errorf("browser evaluation: %s", response.ExceptionDetails.Text)
	}
	if len(response.Result.Value) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(response.Result.Value, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// radioMatches reports whether a radio option is the one identified by value,
// by its visible label or element id (case-insensitively) — the same criteria
// the group scan uses.
func radioMatches(f FormFieldInfo, value string) bool {
	return strings.EqualFold(f.Label, value) || strings.EqualFold(f.ID, value)
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

func (s *Session) discoverFormInternalContext(ctx context.Context, formSelector string) (*FormDiscoveryResult, error) {
	return s.discoverFormInternalWithEvaluator(formSelector, func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) })
}

func (s *Session) discoverFormInternalWithEvaluator(formSelector string, evaluate func(string) (any, error)) (*FormDiscoveryResult, error) {
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

	result, err := evaluate(js)
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
	discovery.ContentTrust = ContentTrustUntrustedPage
	for i := range discovery.Fields {
		discovery.Fields[i].ContentTrust = ContentTrustUntrustedPage
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
	humanLower := strings.ToLower(strings.TrimSpace(humanName))
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
		switch {
		case strings.EqualFold(f.Name, humanName) || strings.EqualFold(f.ID, humanName):
			score = max(score, 90)
		case strings.Contains(strings.ToLower(f.Name), humanLower):
			score = max(score, 70)
		case strings.Contains(strings.ToLower(f.ID), humanLower):
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
