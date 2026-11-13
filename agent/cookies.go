package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	browse "go.klarlabs.de/scout"
)

func browseCookie(c CookieInput) browse.Cookie {
	return browse.Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Domain:   c.Domain,
		Path:     c.Path,
		Expires:  c.Expires,
		Secure:   c.Secure,
		HTTPOnly: c.HTTPOnly,
		SameSite: c.SameSite,
	}
}

// DismissCookieBanner attempts to find and dismiss common cookie consent banners.
// Tries common selectors and text patterns and verifies that the matched consent
// UI is no longer visible before reporting a verified dismissal.
func (s *Session) DismissCookieBanner() (*CookieDismissResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.DismissCookieBannerContext(ctx)
}

// DismissCookieBannerContext attempts to dismiss a common consent banner
// within the caller's deadline.
func (s *Session) DismissCookieBannerContext(ctx context.Context) (*CookieDismissResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	result, err := s.dismissCookieBannerInternal(ctx)
	var outcomeErr *ActionOutcomeError
	if errors.As(err, &outcomeErr) {
		if result != nil {
			preserveCookieDismissalUnknownOutcome(result, outcomeErr)
			outcomeErr.PartialResult = result
		}
		if outcomeErr.Receipt != nil {
			actionReceiptDeadline(ctx, outcomeErr.Receipt)
		}
		return result, err
	}
	if result != nil {
		result.OperationOrdinal = s.nextOperationOrdinal()
		result.Receipt = s.newActionReceipt(result.Action, result.Target, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, nil, ctx)
		actionReceiptDeadline(ctx, result.Receipt)
	}
	return result, err
}

func preserveCookieDismissalUnknownOutcome(result *CookieDismissResult, outcome *ActionOutcomeError) {
	if result == nil || outcome == nil || outcome.Outcome != OutcomeUnknown {
		return
	}
	result.OperationOrdinal = outcome.OperationOrdinal
	result.Outcome = string(outcome.Outcome)
	result.Verified = false
	result.Dismissed = false
	result.Evidence = outcome.Evidence
	result.RecoveryHint = outcome.RecoveryHint
	result.Receipt = outcome.Receipt
}

func (s *Session) dismissCookieBannerInternal(ctx context.Context) (*CookieDismissResult, error) {

	js := `(function() {
		const bannerSelectors = [
			'#cookie-banner', '#cookie-consent', '#cookie-notice',
			'.cookie-banner', '.cookie-consent', '.cookie-notice',
			'#onetrust-banner-sdk', '#CybotCookiebotDialog',
			'[class*="cookie"]', '[id*="cookie"]',
			'[class*="consent"]', '[id*="consent"]',
			'[class*="gdpr"]',
		];
		const bannerFor = el => {
			for (const sel of bannerSelectors) {
				if (el.closest(sel)) return sel;
			}
			return '';
		};

		// Common accept button selectors (ordered by specificity)
		const selectors = [
			// ID-based
			'#accept-cookies', '#cookie-accept', '#onetrust-accept-btn-handler',
			'#CybotCookiebotDialogBodyLevelButtonLevelOptinAllowAll',
			'#truste-consent-button', '#didomi-notice-agree-button',
			'#cookiescript_accept', '#cookie_action_close_header',
			// Class-based
			'.cookie-accept', '.accept-cookies', '.js-cookie-accept',
			'.cc-accept', '.cc-btn.cc-allow', '.cc-dismiss',
			'.cookie-consent-accept', '.cookie-notice-accept',
			'.gdpr-accept', '.consent-accept',
			// Data attributes
			'[data-cookie-accept]', '[data-consent="accept"]',
			'[data-action="accept"]', '[data-testid="cookie-accept"]',
			// aria labels
			'[aria-label="Accept cookies"]', '[aria-label="Accept all cookies"]',
			'[aria-label="Accept all"]', '[aria-label="Allow all"]',
		];

		// Try direct selectors first
		for (const sel of selectors) {
			const btn = document.querySelector(sel);
			if (btn && btn.offsetParent !== null) {
				const banner = bannerFor(btn);
				btn.click();
				return JSON.stringify({found: true, method: 'selector', selector: sel, banner: banner, text: btn.textContent.trim().slice(0, 50)});
			}
		}

		// Try text-based search on buttons and links
		const textPatterns = [
			/^accept\s*(all)?\s*(cookies)?$/i,
			/^(i\s+)?agree$/i,
			/^allow\s*(all)?\s*(cookies)?$/i,
			/^got\s+it$/i,
			/^ok(ay)?$/i,
			/^consent$/i,
			/^accept\s*&?\s*close$/i,
			/^(i\s+)?understand$/i,
			/^continue$/i,
		];

		const clickables = document.querySelectorAll('button, a, [role="button"], input[type="button"], input[type="submit"]');
		for (const el of clickables) {
			const text = el.textContent.trim();
			if (text.length > 50) continue;
			for (const pattern of textPatterns) {
				if (pattern.test(text) && el.offsetParent !== null) {
					const banner = bannerFor(el);
					el.click();
					return JSON.stringify({found: true, method: 'text', banner: banner, text: text});
				}
			}
		}

		// Check if there's a cookie banner at all
		for (const sel of bannerSelectors) {
			if (document.querySelector(sel)) {
				return JSON.stringify({found: true, method: 'none', banner: sel, text: 'Banner found but no accept button detected'});
			}
		}

		return JSON.stringify({found: false});
	})()`

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("dismiss_cookies", "cookie consent banner", "banner click may have been dispatched but dismissal could not be read back", err)
		}
		return nil, err
	}

	str, _ := result.(string)
	var r CookieDismissResult
	_ = json.Unmarshal([]byte(str), &r)
	r.Action = "dismiss_cookies"
	r.Target = "cookie consent banner"
	r.TargetID = s.page.TargetID()

	if r.Found && r.Method != "none" {
		if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
			return &r, s.actionOutcomeAfterDispatch("dismiss_cookies", "cookie consent banner", "banner click was dispatched but settling was interrupted", err)
		}
		verificationJS, err := cookieBannerVisibilityCheck(r)
		if err != nil {
			return &r, err
		}
		state, err := s.page.EvaluateContext(ctx, verificationJS)
		if err != nil {
			if mutationOutcomeMayBeUnknown(err) {
				return &r, s.actionOutcomeAfterDispatch("dismiss_cookies", "cookie consent banner", "banner click was dispatched but banner visibility could not be read back", err)
			}
			return &r, err
		}
		visible, _ := state.(bool)
		if visible {
			r.Outcome = string(OutcomePostconditionFailed)
			r.Verified = true
			r.Evidence = "an accept control was clicked and the page settled, but the matched consent UI remains visible"
			r.RecoveryHint = "Inspect the banner and confirm the site's consent state before continuing."
			s.applyTraceResult("dismiss_cookies", r.Target, r.TargetID, r.Outcome, r.Evidence, r.RecoveryHint, true)
			return &r, nil
		}
		r.Dismissed = true
		r.Outcome = string(OutcomePostconditionVerified)
		r.Verified = true
		r.Evidence = "the matched consent UI was no longer visible after the accept click"
		s.applyTraceResult("dismiss_cookies", r.Target, r.TargetID, r.Outcome, r.Evidence, "", true)
		return &r, nil
	}
	if r.Found {
		r.Outcome = string(OutcomeActionFailed)
		r.Evidence = "a likely consent banner was found, but no supported accept control was detected"
		r.RecoveryHint = "Inspect the banner and dismiss it with a site-specific selector or action."
		s.applyTraceResult("dismiss_cookies", r.Target, r.TargetID, r.Outcome, r.Evidence, r.RecoveryHint, false)
		return &r, nil
	}
	r.Outcome = string(OutcomePostconditionVerified)
	r.Verified = true
	r.Evidence = "no banner or accept control matching Scout's common consent patterns was found"
	s.applyTraceResult("dismiss_cookies", r.Target, r.TargetID, r.Outcome, r.Evidence, "", true)

	return &r, nil
}

func cookieBannerVisibilityCheck(result CookieDismissResult) (string, error) {
	if result.Banner != "" {
		selector, err := json.Marshal(result.Banner)
		if err != nil {
			return "", err
		}
		return `(function() {
			const visible = el => {
				const style = getComputedStyle(el);
				return el.isConnected && el.getClientRects().length > 0 && style.visibility !== 'hidden' && style.display !== 'none' && Number(style.opacity) !== 0;
			};
			return Array.from(document.querySelectorAll(` + string(selector) + `)).some(visible);
		})()`, nil
	}
	text, err := json.Marshal(result.Text)
	if err != nil {
		return "", err
	}
	return `(function() {
		const visible = el => {
			const style = getComputedStyle(el);
			return el.isConnected && el.getClientRects().length > 0 && style.visibility !== 'hidden' && style.display !== 'none' && Number(style.opacity) !== 0;
		};
		return Array.from(document.querySelectorAll('button, a, [role="button"], input[type="button"], input[type="submit"]'))
			.some(el => el.textContent.trim() === ` + string(text) + ` && visible(el));
	})()`, nil
}

// CookieDismissResult describes the outcome of cookie banner dismissal.
type CookieDismissResult struct {
	OperationOrdinal int            `json:"operation_ordinal,omitempty"`
	Found            bool           `json:"found"`
	Method           string         `json:"method,omitempty"`   // "selector", "text", "none"
	Selector         string         `json:"selector,omitempty"` // which selector matched
	Text             string         `json:"text,omitempty"`     // button text that was clicked
	Banner           string         `json:"banner,omitempty"`   // banner selector if found but not dismissed
	Action           string         `json:"action,omitempty"`
	Target           string         `json:"target,omitempty"`
	TargetID         string         `json:"target_id,omitempty"`
	Outcome          string         `json:"outcome,omitempty"`
	Verified         bool           `json:"verified,omitempty"`
	Dismissed        bool           `json:"dismissed,omitempty"`
	Evidence         string         `json:"evidence,omitempty"`
	RecoveryHint     string         `json:"recovery_hint,omitempty"`
	Receipt          *ActionReceipt `json:"action_receipt,omitempty"`
}

// NavigateAndDismissCookies navigates to a URL and auto-dismisses any cookie banner.
func (s *Session) NavigateAndDismissCookies(url string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.NavigateAndDismissCookiesContext(ctx, url)
}

// NavigateAndDismissCookiesContext navigates and checks for a common consent
// banner under one caller-controlled deadline. The page result preserves both
// navigation evidence and the separate dismissal outcome.
func (s *Session) NavigateAndDismissCookiesContext(ctx context.Context, url string) (*PageResult, error) {
	result, err := s.NavigateWithPostconditionContext(ctx, url, Postcondition{})
	if err != nil {
		return nil, err
	}
	// Navigation is an intentional target transition for locally owned browser
	// sessions. Bind the follow-up dismissal to the target navigation returned,
	// not to the source target supplied by the caller.
	dismissCtx := WithExpectedTargetID(ctx, result.TargetID)
	dismissal, dismissErr := s.DismissCookieBannerContext(dismissCtx)
	result.CookieDismissal = dismissal
	if dismissErr != nil {
		return result, dismissErr
	}
	if dismissal != nil {
		result.Evidence += "; cookie banner check: " + dismissal.Evidence
	}
	return result, nil
}

// CookieInfo is a redacted view of a browser cookie. Values are never returned.
type CookieInfo struct {
	ContentTrust string  `json:"content_trust,omitempty"`
	Name         string  `json:"name"`
	Domain       string  `json:"domain,omitempty"`
	Path         string  `json:"path,omitempty"`
	Expires      float64 `json:"expires,omitempty"`
	Secure       bool    `json:"secure,omitempty"`
	HTTPOnly     bool    `json:"http_only,omitempty"`
	SameSite     string  `json:"same_site,omitempty"`
	HasValue     bool    `json:"has_value"`
}

// ListCookies returns redacted cookie metadata for the active page.
// Values are intentionally omitted to avoid leaking session tokens via tool results.
func (s *Session) ListCookies() ([]CookieInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ListCookiesContext(ctx)
}

func (s *Session) ListCookiesContext(ctx context.Context) ([]CookieInfo, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	cookies, err := s.cookiesContext(ctx)
	if err != nil {
		return nil, err
	}
	return redactCookies(cookies), nil
}

func redactCookies(cookies []browse.Cookie) []CookieInfo {
	out := make([]CookieInfo, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, CookieInfo{ContentTrust: ContentTrustUntrustedPage, Name: c.Name, Domain: c.Domain, Path: c.Path, Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite, HasValue: c.Value != ""})
	}
	return out
}

func (s *Session) cookiesContext(ctx context.Context) ([]browse.Cookie, error) {
	if _, err := s.page.CallContext(ctx, "Network.enable", nil); err != nil {
		return nil, err
	}
	raw, err := s.page.CallContext(ctx, "Network.getCookies", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Cookies []browse.Cookie `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result.Cookies, nil
}

// ClearCookies removes cookies for the active session.
// If name is empty, all cookies are dropped via Network.clearBrowserCookies.
// Otherwise only the named cookie is removed (optionally scoped by domain/path).
func (s *Session) ClearCookies(name, domain, path string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClearCookiesContext(ctx, name, domain, path)
}

func (s *Session) ClearCookiesContext(ctx context.Context, name, domain, path string) (int, error) {
	result, err := s.ClearCookiesWithOutcomeContext(ctx, name, domain, path)
	if err != nil {
		return 0, err
	}
	return result.Removed, nil
}

// ClearCookiesWithOutcomeContext removes cookies and returns readback evidence.
func (s *Session) ClearCookiesWithOutcomeContext(ctx context.Context, name, domain, path string) (*CookieMutationResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	before, err := s.cookiesContext(ctx)
	if err != nil {
		return nil, err
	}
	method := "Network.clearBrowserCookies"
	params := any(nil)
	if name != "" {
		method = "Network.deleteCookies"
		url, err := pageURLContext(ctx, s.page)
		if err != nil {
			return nil, err
		}
		p := map[string]any{"name": name}
		if url != "" {
			p["url"] = url
		}
		if domain != "" {
			p["domain"] = domain
		}
		if path != "" {
			p["path"] = path
		}
		params = p
	}
	_, err = s.page.CallContext(ctx, method, params)
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("clear_cookie", name, "cookie deletion may have been dispatched; cookie state needs inspection", err)
		}
		return nil, err
	}
	after, err := s.cookiesContext(ctx)
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("clear_cookie", name, "cookie deletion was dispatched but cookie state could not be verified", err)
	}
	if name == "" && len(after) > 0 {
		return nil, s.actionOutcomeAfterDispatch("clear_cookie", "all cookies", "clear command completed but cookies remained on readback", fmt.Errorf("%d cookies remain", len(after)))
	}
	removed := len(before) - len(after)
	if removed < 0 {
		removed = 0
	}
	if name != "" {
		for _, c := range after {
			if c.Name == name && cookieDomainMatches(c.Domain, domain) && (path == "" || c.Path == path) {
				return nil, s.actionOutcomeAfterDispatch("clear_cookie", name, "cookie remains after delete command", fmt.Errorf("cookie readback still contains target"))
			}
		}
	}
	target := name
	if target == "" {
		target = "all cookies"
	}
	result := &CookieMutationResult{
		Action:   "clear_cookie",
		Target:   target,
		TargetID: s.page.TargetID(),
		Outcome:  string(OutcomePostconditionVerified),
		Verified: true,
		Removed:  removed,
	}
	if name == "" {
		result.Evidence = "cookie readback confirmed that no cookies remain"
	} else {
		result.Evidence = fmt.Sprintf("cookie readback confirmed that %q is absent in the requested scope", name)
	}
	s.applyTraceResult("clear_cookie", target, result.TargetID, result.Outcome, result.Evidence, "", true)
	s.decorateCookieMutation(ctx, result)
	return result, nil
}

// SetCookie sets a single cookie on the active page.
func (s *Session) SetCookie(c CookieInput) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SetCookieContext(ctx, c)
}

func (s *Session) SetCookieContext(ctx context.Context, c CookieInput) error {
	_, err := s.SetCookieWithOutcomeContext(ctx, c)
	return err
}

// SetCookieWithOutcomeContext sets a cookie and returns readback evidence.
// The cookie value is never included in the result.
func (s *Session) SetCookieWithOutcomeContext(ctx context.Context, c CookieInput) (*CookieMutationResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if _, err := s.page.CallContext(ctx, "Network.enable", nil); err != nil {
		return nil, err
	}
	raw, err := s.page.CallContext(ctx, "Network.setCookie", browseCookie(c))
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("set_cookie", c.Name, "cookie set may have been dispatched; cookie state needs inspection", err)
		}
		return nil, err
	}
	var setResult struct {
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &setResult); err == nil && setResult.Success != nil && !*setResult.Success {
		return nil, fmt.Errorf("browser rejected cookie %q", c.Name)
	}
	cookies, err := s.cookiesContext(ctx)
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("set_cookie", c.Name, "cookie was set but its presence could not be verified", err)
	}
	for _, stored := range cookies {
		if stored.Name == c.Name && cookieDomainMatches(stored.Domain, c.Domain) && (c.Path == "" || stored.Path == c.Path) && stored.Value == c.Value {
			result := &CookieMutationResult{
				Action:   "set_cookie",
				Target:   c.Name,
				TargetID: s.page.TargetID(),
				Outcome:  string(OutcomeValueVerified),
				Verified: true,
				Evidence: "cookie readback matched the requested name, scope, and value",
			}
			s.applyTraceResult("set_cookie", c.Name, result.TargetID, result.Outcome, result.Evidence, "", true)
			s.decorateCookieMutation(ctx, result)
			return result, nil
		}
	}
	return nil, s.actionOutcomeAfterDispatch("set_cookie", c.Name, "set command completed but matching cookie was absent on readback", fmt.Errorf("cookie readback did not match"))
}

// CookieMutationResult reports a cookie change without exposing cookie values.
type CookieMutationResult struct {
	OperationOrdinal int            `json:"operation_ordinal,omitempty"`
	Action           string         `json:"action"`
	Target           string         `json:"target"`
	TargetID         string         `json:"target_id,omitempty"`
	Outcome          string         `json:"outcome"`
	Verified         bool           `json:"verified"`
	Evidence         string         `json:"evidence"`
	Removed          int            `json:"removed"`
	Receipt          *ActionReceipt `json:"action_receipt,omitempty"`
}

func (s *Session) decorateCookieMutation(ctx context.Context, result *CookieMutationResult) {
	if result == nil {
		return
	}
	result.OperationOrdinal = s.nextOperationOrdinal()
	result.Receipt = s.newActionReceipt(result.Action, result.Target, result.TargetID, result.Outcome, result.Evidence, "", result.Verified, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
}

func cookieDomainMatches(actual, requested string) bool {
	if requested == "" {
		return true
	}
	return strings.TrimPrefix(actual, ".") == strings.TrimPrefix(requested, ".")
}

// CookieInput describes a cookie to set. Mirrors the CDP shape minus internal fields.
type CookieInput struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"http_only,omitempty"`
	SameSite string  `json:"same_site,omitempty"`
}

// cookieSummaryInternal returns the CookieSummary view for embedding in Observe.
// Caller must hold s.mu.
func (s *Session) cookieSummaryInternal() *CookieSummary {
	if s.page == nil {
		return nil
	}
	cookies, err := s.page.Cookies()
	if err != nil || len(cookies) == 0 {
		return nil
	}
	names := make([]string, 0, len(cookies))
	for i, c := range cookies {
		if i >= 12 { // cap to keep Observe compact
			break
		}
		names = append(names, c.Name)
	}
	return &CookieSummary{ContentTrust: ContentTrustUntrustedPage, Count: len(cookies), Names: names}
}
