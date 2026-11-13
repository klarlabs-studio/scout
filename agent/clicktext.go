package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ClickTextWithPostconditionContext resolves a visible-text target and clicks it under ctx.
func (s *Session) ClickTextWithPostconditionContext(ctx context.Context, text, role string, condition Postcondition) (*ClickTextResult, error) {
	if condition.count() > 0 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
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
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("click_text: text is required")
	}
	encoded, _ := json.Marshal(text)
	roleJSON, _ := json.Marshal(strings.ToLower(role))
	state, err := s.page.EvaluateContext(ctx, fmt.Sprintf(`(function(){const wanted=%s,role=%s,lower=wanted.toLowerCase(),found=[];function visible(e){const r=e.getBoundingClientRect(),s=getComputedStyle(e);return r.width>=1&&r.height>=1&&s.display!=='none'&&s.visibility!=='hidden'&&s.opacity!=='0'}function add(e,type){if(e&&!found.some(x=>x.el===e))found.push({el:e,type})}for(const e of document.querySelectorAll('[aria-label]'))if(visible(e)&&(e.getAttribute('aria-label')||'').trim().toLowerCase()===lower)add(e,'aria_label');const filter=role==='button'?'button,[role=button],input[type=button],input[type=submit]':role==='link'?'a[href],[role=link]':'button,a[href],[role=button],[role=link],input[type=button],input[type=submit]';for(const e of document.querySelectorAll(filter))if(visible(e)&&(e.textContent||e.value||'').trim().toLowerCase()===lower)add(e,e.tagName.toLowerCase()==='a'?'link_text':'button_text');if(!found.length){const w=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);while(w.nextNode()){const n=w.currentNode;if((n.textContent||'').trim().toLowerCase()!==lower)continue;for(let e=n.parentElement;e;e=e.parentElement)if(e.matches('a[href],button,[role=button],[role=link],input[type=button],input[type=submit],[onclick],[tabindex]')){if(visible(e))add(e,'text_descendant');break}}}const candidates=found.map(({el,type})=>({selector:el.id?'#'+el.id:el.tagName.toLowerCase(),tag:el.tagName.toLowerCase(),text:(el.textContent||el.value||'').trim().slice(0,80),role:el.getAttribute('role')||'',match_type:type}));if(!found.length)return {matched:false,candidates};if(found.length>1)return {matched:true,candidates,match_type:found[0].type};found[0].el.setAttribute('data-scout-context-text','1');return {matched:true,candidates,match_type:found[0].type}})()`, string(encoded), string(roleJSON)))
	if err != nil {
		return nil, fmt.Errorf("click_text evaluate: %w", err)
	}
	cleanupMarker := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, _ = s.page.EvaluateContext(cleanupCtx, `document.querySelectorAll('[data-scout-context-text="1"]').forEach(e=>e.removeAttribute('data-scout-context-text'))`)
	}
	defer cleanupMarker()
	value, _ := state.(map[string]any)
	if value == nil {
		return nil, fmt.Errorf("click_text: target resolution failed")
	}
	matched, _ := value["matched"].(bool)
	raw, _ := json.Marshal(value["candidates"])
	var candidates []ClickTextOption
	_ = json.Unmarshal(raw, &candidates)
	for i := range candidates {
		candidates[i].ContentTrust = ContentTrustUntrustedPage
	}
	matchType, _ := value["match_type"].(string)
	if !matched {
		return nil, &SelectorNotFoundError{Selector: fmt.Sprintf("text=%q", text), Matched: 0, PageTitle: pageTitleSafe(s)}
	}
	if len(candidates) > 1 {
		return &ClickTextResult{ContentTrust: ContentTrustUntrustedPage, Candidates: candidates, MatchType: matchType}, fmt.Errorf("click_text: %d elements match %q (pass role= to disambiguate)", len(candidates), text)
	}
	nodeID, err := s.page.QuerySelectorPiercingContext(ctx, "[data-scout-context-text='1']")
	if err != nil {
		return nil, err
	}
	if _, err = formNodeCallContext(ctx, s.page, nodeID, `function(){this.scrollIntoView({block:'center',inline:'center'});const r=this.getBoundingClientRect(),x=r.left+r.width/2,y=r.top+r.height/2;for(const type of ['mousedown','mouseup','click'])this.dispatchEvent(new MouseEvent(type,{bubbles:true,cancelable:true,clientX:x,clientY:y,view:window}));return true}`, nil); err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("click", "text="+text, "visible-text click was interrupted; the browser may have received it", err, condition)
		}
		return nil, err
	}
	if err = s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		return nil, s.actionOutcomeAfterDispatch("click", "text="+text, "visible-text click was dispatched; page settling was interrupted", err, condition)
	}
	url, title := "", ""
	page, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("click", "text="+text, "visible-text click was dispatched; page state could not be read", err, condition)
	}
	url, title = page.URL, page.Title
	out := &ClickTextResult{ContentTrust: ContentTrustUntrustedPage, URL: url, Title: title, TargetID: s.page.TargetID(), Matched: candidates[0].Selector, MatchType: matchType}
	if condition.count() > 0 {
		verified, e := s.verifyPagePostconditionContextLocked(ctx, "clicked", out.Matched, condition)
		if e != nil {
			return nil, s.actionOutcomeAfterDispatch("click", "text="+text, "visible-text click was dispatched; postcondition could not be verified", e, condition)
		}
		out.OperationOrdinal = verified.OperationOrdinal
		out.Verified, out.Outcome, out.Evidence, out.RecoveryHint = verified.Verified, verified.Outcome, verified.Evidence, verified.RecoveryHint
		out.URL, out.Title = verified.URL, verified.Title
	} else {
		out.OperationOrdinal = s.operationOrdinalForResult("click", out.Matched)
		out.Outcome = "effect_unverified"
		out.Evidence = "visible-text target resolved and click dispatched; page postcondition was not specified"
	}
	var postcondition *Postcondition
	if condition.count() > 0 {
		copy := condition
		postcondition = &copy
	}
	out.Receipt = s.newActionReceipt("click", out.Matched, out.TargetID, out.Outcome, out.Evidence, out.RecoveryHint, out.Verified, out.OperationOrdinal, postcondition, ctx)
	actionReceiptDeadline(ctx, out.Receipt)
	return out, nil
}

// ClickTextResult describes which element ClickText resolved to.
type ClickTextResult struct {
	ContentTrust     string            `json:"content_trust,omitempty"`
	OperationOrdinal int               `json:"operation_ordinal,omitempty"`
	URL              string            `json:"url"`
	Title            string            `json:"title"`
	TargetID         string            `json:"target_id,omitempty"`
	Matched          string            `json:"matched_selector"`
	MatchType        string            `json:"match_type"` // aria_label, button_text, link_text, text_descendant
	Verified         bool              `json:"verified,omitempty"`
	Outcome          string            `json:"outcome,omitempty"`
	Evidence         string            `json:"evidence,omitempty"`
	RecoveryHint     string            `json:"recovery_hint,omitempty"`
	Receipt          *ActionReceipt    `json:"action_receipt,omitempty"`
	Candidates       []ClickTextOption `json:"candidates,omitempty"`
}

// ClickTextOption describes one candidate when ClickText finds multiple matches.
type ClickTextOption struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Selector     string `json:"selector"`
	Tag          string `json:"tag"`
	Text         string `json:"text"`
	Role         string `json:"role,omitempty"`
}

// ClickText clicks an element by its visible text. Resolution order:
//  1. Exact aria-label match
//  2. button/a/[role=button] visible-text match (case-insensitive, exact)
//  3. visible-text match anywhere then climb to closest interactive ancestor
//
// role narrows step 2 to a specific role ("button" or "link"). Pass "" to disable.
// Returns an error with structured candidates when multiple elements share the text.
func (s *Session) ClickText(text, role string) (*ClickTextResult, error) {
	return s.ClickTextWithPostcondition(text, role, Postcondition{})
}

// ClickTextWithPostcondition clicks a uniquely resolved visible-text target
// and optionally verifies the resulting page state.
func (s *Session) ClickTextWithPostcondition(text, role string, condition Postcondition) (*ClickTextResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClickTextWithPostconditionContext(ctx, text, role, condition)
}

func pageTitleSafe(s *Session) string {
	if s.page == nil {
		return ""
	}
	t, err := s.page.Evaluate(`document.title`)
	if err != nil {
		return ""
	}
	str, _ := t.(string)
	return str
}
