package agent

import (
	"encoding/json"
	"fmt"
)

// ConsoleMessage represents a captured browser console message.
type ConsoleMessage struct {
	Level  string `json:"level"` // log, warn, error, info
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
}

// NetworkFailure summarizes a failed network response (status >= 400) for diagnostics.
type NetworkFailure struct {
	URL                   string `json:"url"`
	Method                string `json:"method"`
	Status                int    `json:"status"`
	MimeType              string `json:"mime_type,omitempty"`
	ResponseBodySnippet   string `json:"response_body_snippet,omitempty"`
	ResponseBodyTruncated bool   `json:"response_body_truncated,omitempty"`
}

// DiagnosticsResult bundles console messages and recent network failures.
// Used by the console_errors MCP tool to give a single-call view of what's broken.
type DiagnosticsResult struct {
	Messages        []ConsoleMessage `json:"messages"`
	NetworkFailures []NetworkFailure `json:"network_failures,omitempty"`
}

// Diagnostics returns console messages + recent 4xx/5xx network failures.
// Auto-installs lightweight network observers so failures recorded after this
// call surface in subsequent calls without an explicit enable_network_capture.
func (s *Session) Diagnostics() (*DiagnosticsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	messages, err := s.consoleErrorsLocked()
	if err != nil {
		return nil, err
	}
	out := &DiagnosticsResult{Messages: messages}

	if s.network == nil {
		s.network = &networkState{
			pending:      make(map[string]*NetworkCapture),
			pendingAll:   make(map[string]*NetworkCapture),
			historyLimit: 200,
		}
	}
	if !s.network.observersInstalled {
		_ = s.ensureNetworkObserversLocked()
	}

	s.network.mu.Lock()
	for _, c := range s.network.history {
		if c.Status >= 400 {
			snippet := c.ResponseBody
			if len(snippet) > 500 {
				snippet = snippet[:500]
			}
			out.NetworkFailures = append(out.NetworkFailures, NetworkFailure{
				URL:                   c.URL,
				Method:                c.Method,
				Status:                c.Status,
				MimeType:              c.MimeType,
				ResponseBodySnippet:   snippet,
				ResponseBodyTruncated: c.ResponseBodyTruncated || len(c.ResponseBody) > 500,
			})
		}
	}
	s.network.mu.Unlock()

	return out, nil
}

// FailedRequests returns recent 4xx/5xx network responses since observers were installed.
// Auto-installs observers if not yet enabled — first call surfaces no history.
func (s *Session) FailedRequests() ([]NetworkFailure, error) {
	d, err := s.Diagnostics()
	if err != nil {
		return nil, err
	}
	return d.NetworkFailures, nil
}

// ConsoleErrors returns captured console.error and console.warn messages from the page.
func (s *Session) ConsoleErrors() ([]ConsoleMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.consoleErrorsLocked()
}

// consoleErrorsLocked is the non-locking implementation. Caller must hold s.mu.
func (s *Session) consoleErrorsLocked() ([]ConsoleMessage, error) {
	js := `(function() {
		if (!window.__scoutConsole) {
			window.__scoutConsole = [];
			const orig = {error: console.error, warn: console.warn};
			console.error = function() {
				window.__scoutConsole.push({level:'error', text: Array.from(arguments).map(String).join(' ')});
				orig.error.apply(console, arguments);
			};
			console.warn = function() {
				window.__scoutConsole.push({level:'warn', text: Array.from(arguments).map(String).join(' ')});
				orig.warn.apply(console, arguments);
			};
		}
		const msgs = window.__scoutConsole.slice(-20);
		window.__scoutConsole = [];
		return JSON.stringify(msgs);
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var messages []ConsoleMessage
	_ = json.Unmarshal([]byte(str), &messages)
	return messages, nil
}

// AuthWallResult describes whether the page appears to be behind an auth wall.
type AuthWallResult struct {
	Detected   bool   `json:"detected"`
	Type       string `json:"type,omitempty"` // login, paywall, captcha, age_gate, none
	Confidence int    `json:"confidence"`     // 0-100
	Reason     string `json:"reason,omitempty"`
	LoginURL   string `json:"login_url,omitempty"`
}

// DetectAuthWall checks if the current page is an authentication wall, paywall, or CAPTCHA.
func (s *Session) DetectAuthWall() (*AuthWallResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	js := `(function() {
		const result = {detected: false, type: 'none', confidence: 0, reason: ''};
		const text = document.body ? document.body.innerText.toLowerCase() : '';
		const html = document.body ? document.body.innerHTML.toLowerCase() : '';

		// Login detection
		const loginSignals = [
			{test: () => document.querySelector('input[type="password"]'), type: 'login', weight: 40, reason: 'Password field found'},
			{test: () => document.querySelector('form[action*="login"], form[action*="signin"], form[action*="auth"]'), type: 'login', weight: 30, reason: 'Login form detected'},
			{test: () => text.includes('sign in') || text.includes('log in') || text.includes('login'), type: 'login', weight: 20, reason: 'Login text found'},
			{test: () => text.includes('forgot password') || text.includes('reset password'), type: 'login', weight: 15, reason: 'Password reset link found'},
			{test: () => document.querySelector('[class*="login"], [id*="login"], [class*="signin"], [id*="signin"]'), type: 'login', weight: 20, reason: 'Login container found'},
		];

		// Paywall detection
		const paywallSignals = [
			{test: () => text.includes('subscribe') && text.includes('unlock'), type: 'paywall', weight: 40, reason: 'Subscribe/unlock text'},
			{test: () => document.querySelector('[class*="paywall"], [id*="paywall"], [class*="subscribe-wall"]'), type: 'paywall', weight: 50, reason: 'Paywall container found'},
			{test: () => text.includes('premium content') || text.includes('members only'), type: 'paywall', weight: 35, reason: 'Premium content text'},
			{test: () => html.includes('blur(') && text.includes('subscri'), type: 'paywall', weight: 45, reason: 'Blurred content with subscribe prompt'},
		];

		// CAPTCHA detection
		const captchaSignals = [
			{test: () => document.querySelector('iframe[src*="recaptcha"], iframe[src*="hcaptcha"], iframe[src*="turnstile"]'), type: 'captcha', weight: 80, reason: 'CAPTCHA iframe detected'},
			{test: () => document.querySelector('[class*="captcha"], [id*="captcha"]'), type: 'captcha', weight: 60, reason: 'CAPTCHA element found'},
			{test: () => text.includes('verify you are human') || text.includes('are you a robot'), type: 'captcha', weight: 50, reason: 'CAPTCHA text found'},
		];

		const allSignals = [...loginSignals, ...paywallSignals, ...captchaSignals];
		let maxScore = 0;
		let bestType = 'none';
		let bestReason = '';
		let totalScore = 0;

		for (const signal of allSignals) {
			try {
				if (signal.test()) {
					totalScore += signal.weight;
					if (signal.weight > maxScore) {
						maxScore = signal.weight;
						bestType = signal.type;
						bestReason = signal.reason;
					}
				}
			} catch(e) {}
		}

		result.confidence = Math.min(totalScore, 100);
		if (totalScore >= 30) {
			result.detected = true;
			result.type = bestType;
			result.reason = bestReason;
		}

		// Find login URL
		const loginLink = document.querySelector('a[href*="login"], a[href*="signin"], a[href*="auth"]');
		if (loginLink) result.loginURL = loginLink.getAttribute('href');

		return JSON.stringify(result);
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var authResult AuthWallResult
	_ = json.Unmarshal([]byte(str), &authResult)
	return &authResult, nil
}

// UploadFile triggers a file upload on a file input element.
func (s *Session) UploadFile(selector, filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return err
	}

	nodeID, err := s.querySelector(selector)
	if err != nil {
		return fmt.Errorf("file input %q not found: %w", selector, err)
	}

	// Use DOM.setFileInputFiles CDP command
	objectID, err := s.page.ResolveNode(nodeID)
	if err != nil {
		return err
	}

	// Get the backend node ID for the file input
	_, err = s.page.Call("DOM.setFileInputFiles", map[string]any{
		"files":    []string{filePath},
		"objectId": objectID,
	})
	if err != nil {
		return fmt.Errorf("file upload failed: %w", err)
	}

	return nil
}

// PageDiff compares two pages and returns the differences.
type PageDiff struct {
	URL1      string               `json:"url1"`
	URL2      string               `json:"url2"`
	Title1    string               `json:"title1"`
	Title2    string               `json:"title2"`
	OnlyIn1   []string             `json:"only_in_1,omitempty"`
	OnlyIn2   []string             `json:"only_in_2,omitempty"`
	Different map[string][2]string `json:"different,omitempty"` // field -> [value1, value2]
}

// CompareTabs compares the content of two named tabs.
func (s *Session) CompareTabs(tab1, tab2 string) (*PageDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tabs == nil {
		return nil, fmt.Errorf("no tabs open")
	}

	t1, ok1 := s.tabs.tabs[tab1]
	t2, ok2 := s.tabs.tabs[tab2]
	if !ok1 {
		return nil, fmt.Errorf("tab %q not found", tab1)
	}
	if !ok2 {
		return nil, fmt.Errorf("tab %q not found", tab2)
	}

	// Extract key content from both pages
	extractJS := `(function() {
		const data = {title: document.title, url: window.location.href, items: {}};
		// Extract headings
		for (const h of document.querySelectorAll('h1,h2,h3')) {
			const key = h.tagName + ':' + h.textContent.trim().slice(0, 50);
			data.items[key] = h.textContent.trim();
		}
		// Extract prices/values
		for (const el of document.querySelectorAll('.price,[class*="price"],[data-price]')) {
			const key = 'price:' + (el.closest('[class*="title"],[class*="name"],h2,h3')?.textContent.trim().slice(0,30) || el.parentElement?.textContent.trim().slice(0,30) || 'unknown');
			data.items[key] = el.textContent.trim();
		}
		// Extract links
		for (const a of document.querySelectorAll('a[href]')) {
			const text = a.textContent.trim();
			if (text) data.items['link:'+text.slice(0,40)] = a.getAttribute('href');
		}
		return JSON.stringify(data);
	})()`

	r1, err := t1.page.Evaluate(extractJS)
	if err != nil {
		return nil, err
	}
	r2, err := t2.page.Evaluate(extractJS)
	if err != nil {
		return nil, err
	}

	var d1, d2 struct {
		Title string            `json:"title"`
		URL   string            `json:"url"`
		Items map[string]string `json:"items"`
	}
	s1, _ := r1.(string)
	s2, _ := r2.(string)
	_ = json.Unmarshal([]byte(s1), &d1)
	_ = json.Unmarshal([]byte(s2), &d2)

	diff := &PageDiff{
		URL1:      d1.URL,
		URL2:      d2.URL,
		Title1:    d1.Title,
		Title2:    d2.Title,
		Different: make(map[string][2]string),
	}

	for k, v1 := range d1.Items {
		if v2, ok := d2.Items[k]; ok {
			if v1 != v2 {
				diff.Different[k] = [2]string{v1, v2}
			}
		} else {
			diff.OnlyIn1 = append(diff.OnlyIn1, k+"="+v1)
		}
	}
	for k, v2 := range d2.Items {
		if _, ok := d1.Items[k]; !ok {
			diff.OnlyIn2 = append(diff.OnlyIn2, k+"="+v2)
		}
	}

	return diff, nil
}
