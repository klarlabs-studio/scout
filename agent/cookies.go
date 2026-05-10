package agent

import (
	"encoding/json"
	"fmt"
	"time"

	browse "github.com/felixgeelhaar/scout"
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
// Tries common selectors and text patterns. Returns whether a banner was found and dismissed.
func (s *Session) DismissCookieBanner() (*CookieDismissResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	js := `(function() {
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
				btn.click();
				return JSON.stringify({found: true, method: 'selector', selector: sel, text: btn.textContent.trim().slice(0, 50)});
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
					el.click();
					return JSON.stringify({found: true, method: 'text', text: text});
				}
			}
		}

		// Check if there's a cookie banner at all
		const bannerSelectors = [
			'#cookie-banner', '#cookie-consent', '#cookie-notice',
			'.cookie-banner', '.cookie-consent', '.cookie-notice',
			'#onetrust-banner-sdk', '#CybotCookiebotDialog',
			'[class*="cookie"]', '[id*="cookie"]',
			'[class*="consent"]', '[id*="consent"]',
			'[class*="gdpr"]',
		];
		for (const sel of bannerSelectors) {
			if (document.querySelector(sel)) {
				return JSON.stringify({found: true, method: 'none', banner: sel, text: 'Banner found but no accept button detected'});
			}
		}

		return JSON.stringify({found: false});
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var r CookieDismissResult
	_ = json.Unmarshal([]byte(str), &r)

	if r.Found && r.Method != "none" {
		time.Sleep(300 * time.Millisecond) // wait for banner animation
	}

	return &r, nil
}

// CookieDismissResult describes the outcome of cookie banner dismissal.
type CookieDismissResult struct {
	Found    bool   `json:"found"`
	Method   string `json:"method,omitempty"`   // "selector", "text", "none"
	Selector string `json:"selector,omitempty"` // which selector matched
	Text     string `json:"text,omitempty"`     // button text that was clicked
	Banner   string `json:"banner,omitempty"`   // banner selector if found but not dismissed
}

// NavigateAndDismissCookies navigates to a URL and auto-dismisses any cookie banner.
func (s *Session) NavigateAndDismissCookies(url string) (*PageResult, error) {
	result, err := s.Navigate(url)
	if err != nil {
		return nil, err
	}
	_, _ = s.DismissCookieBanner()
	return result, nil
}

// AutoDismissCookies wraps Navigate to always dismiss cookie banners.
func (s *Session) autoNavigate(url string) (*PageResult, error) {
	result, err := s.Navigate(url)
	if err != nil {
		return nil, err
	}

	// Best-effort cookie dismissal
	dismissResult, _ := s.DismissCookieBanner()
	if dismissResult != nil && dismissResult.Found {
		_ = fmt.Sprintf("Cookie banner dismissed: %s", dismissResult.Text)
	}

	return result, nil
}

// CookieInfo is a redacted view of a browser cookie. Values are never returned.
type CookieInfo struct {
	Name     string  `json:"name"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"http_only,omitempty"`
	SameSite string  `json:"same_site,omitempty"`
	HasValue bool    `json:"has_value"`
}

// ListCookies returns redacted cookie metadata for the active page.
// Values are intentionally omitted to avoid leaking session tokens via tool results.
func (s *Session) ListCookies() ([]CookieInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	cookies, err := s.page.Cookies()
	if err != nil {
		return nil, err
	}
	out := make([]CookieInfo, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, CookieInfo{
			Name:     c.Name,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  c.Expires,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
			HasValue: c.Value != "",
		})
	}
	return out, nil
}

// ClearCookies removes cookies for the active session.
// If name is empty, all cookies are dropped via Network.clearBrowserCookies.
// Otherwise only the named cookie is removed (optionally scoped by domain/path).
func (s *Session) ClearCookies(name, domain, path string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return 0, err
	}
	before, _ := s.page.Cookies()
	if name == "" {
		if err := s.page.ClearBrowserCookies(); err != nil {
			return 0, err
		}
		return len(before), nil
	}
	url, _ := s.page.URL()
	if err := s.page.DeleteCookie(name, url, domain, path); err != nil {
		return 0, err
	}
	return 1, nil
}

// SetCookie sets a single cookie on the active page.
func (s *Session) SetCookie(c CookieInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return err
	}
	return s.page.SetCookie(browseCookie(c))
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
	return &CookieSummary{Count: len(cookies), Names: names}
}
