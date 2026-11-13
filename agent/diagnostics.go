package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// ConsoleMessage represents a captured browser console message.
type ConsoleMessage struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Level        string `json:"level"` // log, warn, error, info
	Text         string `json:"text"`
	Source       string `json:"source,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
}

// NetworkFailure summarizes a failed network response (status >= 400) for diagnostics.
type NetworkFailure struct {
	ContentTrust          string `json:"content_trust,omitempty"`
	URL                   string `json:"url"`
	Method                string `json:"method"`
	Status                int    `json:"status"`
	MimeType              string `json:"mime_type,omitempty"`
	ResponseBodySnippet   string `json:"response_body_snippet,omitempty"`
	ResponseBodyTruncated bool   `json:"response_body_truncated,omitempty"`
}

func networkFailureSnippet(body string) (string, bool) {
	if len(body) <= 500 {
		return body, false
	}
	return truncateUTF8Bytes(body, 500), true
}

// DiagnosticsResult bundles console messages and recent network failures.
// Used by the console_errors MCP tool to give a single-call view of what's broken.
type DiagnosticsResult struct {
	ContentTrust             string           `json:"content_trust,omitempty"`
	Messages                 []ConsoleMessage `json:"messages"`
	NetworkCaptureMode       string           `json:"network_capture_mode"`
	NetworkCaptureCoverage   string           `json:"network_capture_coverage"`
	NetworkCaptureScope      string           `json:"network_capture_scope"`
	NetworkFailures          []NetworkFailure `json:"network_failures,omitempty"`
	NetworkFailureTotal      int              `json:"network_failure_total"`
	NetworkFailuresTruncated bool             `json:"network_failures_truncated"`
}

// Diagnostics returns console messages + recent 4xx/5xx network failures.
// Auto-installs lightweight network observers so failures recorded after this
// call surface in subsequent calls without an explicit enable_network_capture.
func (s *Session) Diagnostics() (*DiagnosticsResult, error) {
	return s.DiagnosticsContext(context.Background())
}

// DiagnosticsContext returns recent console messages and network failures while
// honoring the caller's deadline for session queueing and browser evaluation.
func (s *Session) DiagnosticsContext(ctx context.Context) (*DiagnosticsResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	messages, err := s.consoleErrorsLockedContext(ctx)
	if err != nil {
		return nil, err
	}
	out := &DiagnosticsResult{ContentTrust: ContentTrustUntrustedPage, Messages: messages}

	if s.network == nil {
		s.network = &networkState{
			pending:      make(map[string]*NetworkCapture),
			pendingAll:   make(map[string]*NetworkCapture),
			historyLimit: 200,
		}
	}
	var observerErr error
	// Preserve passive observer intent even when the first CDP attach fails.
	// Page switches can then retry diagnostics without requiring another call.
	s.network.mu.Lock()
	if !s.network.enabled && !s.network.observersInstalled {
		s.network.observerMode = networkObserverDiagnostics
	}
	s.network.mu.Unlock()
	if !s.network.observersInstalled || (s.network.enabled && s.network.observerMode != networkObserverCapture) {
		observerErr = s.ensureNetworkObserversContextLocked(ctx)
		if observerErr != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	s.network.mu.Lock()
	out.NetworkCaptureScope = "session_network_capture_window"
	out.NetworkCaptureMode = networkCaptureMode(s.network)
	switch {
	case observerErr != nil || !s.network.observersInstalled:
		out.NetworkCaptureMode = "unavailable"
		out.NetworkCaptureCoverage = "unavailable"
	case s.network.captureInterrupted || s.network.droppedRequests > 0 || (s.network.enabled && len(s.network.pending) > 0):
		out.NetworkCaptureCoverage = "known_gaps"
	case s.network.observerMode == networkObserverDiagnostics:
		out.NetworkCaptureCoverage = "diagnostics_only"
	case !s.network.captureEverEnabled:
		out.NetworkCaptureCoverage = "not_enabled"
	case !s.network.enabled:
		out.NetworkCaptureCoverage = "disabled_at_read"
	default:
		out.NetworkCaptureCoverage = "active_no_known_loss"
	}
	for _, c := range s.network.history {
		if ctx.Err() != nil {
			s.network.mu.Unlock()
			return nil, ctx.Err()
		}
		if c.Status >= 400 {
			snippet, snippetTruncated := networkFailureSnippet(c.ResponseBody)
			out.NetworkFailureTotal++
			url := c.URL
			if len(url) > 2048 {
				url = url[:2048]
			}
			method := c.Method
			if len(method) > 32 {
				method = method[:32]
			}
			mime := c.MimeType
			if len(mime) > 128 {
				mime = mime[:128]
			}
			out.NetworkFailures = append(out.NetworkFailures, NetworkFailure{
				ContentTrust:          ContentTrustUntrustedPage,
				URL:                   url,
				Method:                method,
				Status:                c.Status,
				MimeType:              mime,
				ResponseBodySnippet:   snippet,
				ResponseBodyTruncated: c.ResponseBodyTruncated || snippetTruncated,
			})
		}
	}
	s.network.mu.Unlock()
	if len(out.NetworkFailures) > 20 {
		out.NetworkFailuresTruncated = true
		out.NetworkFailures = out.NetworkFailures[len(out.NetworkFailures)-20:]
	}

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
	return s.ConsoleErrorsContext(context.Background())
}

// ConsoleErrorsContext returns captured console errors and warnings under ctx.
func (s *Session) ConsoleErrorsContext(ctx context.Context) ([]ConsoleMessage, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.consoleErrorsLockedContext(ctx)
}

func (s *Session) consoleErrorsLockedContext(ctx context.Context) ([]ConsoleMessage, error) {
	js := `(function() {
		if (!window.__scoutConsole) {
			window.__scoutConsole = [];
			const orig = {error: console.error, warn: console.warn};
			function record(level, args) {
				const full = Array.from(args).map(String).join(' ');
				window.__scoutConsole.push({level: level, text: full.slice(0, 2000), truncated: full.length > 2000});
				if (window.__scoutConsole.length > 100) window.__scoutConsole = window.__scoutConsole.slice(-100);
			}
			console.error = function() {
				record('error', arguments);
				orig.error.apply(console, arguments);
			};
			console.warn = function() {
				record('warn', arguments);
				orig.warn.apply(console, arguments);
			};
		}
		const msgs = window.__scoutConsole.slice(-20);
		window.__scoutConsole = [];
		return JSON.stringify(msgs);
	})()`

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var messages []ConsoleMessage
	_ = json.Unmarshal([]byte(str), &messages)
	for i := range messages {
		messages[i].ContentTrust = ContentTrustUntrustedPage
	}
	return messages, nil
}

// AuthWallResult describes whether the page appears to be behind an auth wall.
type AuthWallResult struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Detected     bool   `json:"detected"`
	Type         string `json:"type,omitempty"` // login, paywall, captcha, age_gate, none
	Confidence   int    `json:"confidence"`     // 0-100
	Reason       string `json:"reason,omitempty"`
	LoginURL     string `json:"login_url,omitempty"`
}

// DetectAuthWall checks if the current page is an authentication wall, paywall, or CAPTCHA.
func (s *Session) DetectAuthWall() (*AuthWallResult, error) {
	return s.DetectAuthWallContext(context.Background())
}

// DetectAuthWallContext checks for common authentication walls under ctx.
func (s *Session) DetectAuthWallContext(ctx context.Context) (*AuthWallResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
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

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var authResult AuthWallResult
	_ = json.Unmarshal([]byte(str), &authResult)
	authResult.ContentTrust = ContentTrustUntrustedPage
	return &authResult, nil
}

// UploadFile triggers a file upload on a file input element. The path is
// validated first so an untrusted (page-injected) instruction can't exfiltrate
// local secrets via the upload — see validateUploadPath.
func (s *Session) UploadFile(selector, filePath string) error {
	_, err := s.UploadFileResult(selector, filePath)
	return err
}

// UploadFileResult uploads one file and reports whether the browser exposes the
// selected file on the input. It does not claim that the site accepted it.
func (s *Session) UploadFileResult(selector, filePath string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.UploadFileResultContext(ctx, selector, filePath)
}

// UploadFileResultContext selects a file and verifies its name on the input
// without claiming that the site accepted or uploaded it.
func (s *Session) UploadFileResultContext(ctx context.Context, selector, filePath string) (*PageResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := validateUploadPath(filePath)
	if err != nil {
		return nil, err
	}

	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.checkExpectedTargetIDContext(ctx); err != nil {
		return nil, err
	}

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	nodeID, err := s.page.QuerySelectorContext(ctx, selector)
	if err != nil {
		return nil, fmt.Errorf("file input %q not found: %w", selector, err)
	}

	// Use DOM.setFileInputFiles CDP command
	name := filepath.Base(resolved)
	condition := Postcondition{ValueSelector: selector, Value: `C:\fakepath\` + name}
	objectID, err := s.page.ResolveNodeContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	// Get the backend node ID for the file input
	_, err = s.page.CallContext(ctx, "DOM.setFileInputFiles", map[string]any{
		"files":    []string{resolved},
		"objectId": objectID,
	})
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("upload_file", selector, "file selection may have been dispatched but could not be confirmed", err, condition)
		}
		return nil, fmt.Errorf("file upload failed: %w", err)
	}
	selectorJSON, _ := json.Marshal(selector)
	nameJSON, _ := json.Marshal(name)
	readback, err := s.page.EvaluateContext(ctx, fmt.Sprintf(`(()=>{const el=document.querySelector(%s);return !!el&&!!el.files&&Array.from(el.files).some(f=>f.name===%s)})()`, selectorJSON, nameJSON))
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("upload_file", selector, "file selection was dispatched but the input could not be read back", err, condition)
	}
	if selected, _ := readback.(bool); !selected {
		return nil, s.actionOutcomeAfterDispatch("upload_file", selector, "file selection command completed but the requested file was absent on readback", fmt.Errorf("selected file was not present"), condition)
	}
	evidence := "browser file input reports " + name + "; site-side upload acceptance was not asserted"
	result, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("upload_file", selector, "file selection was verified but result state could not be read", err, condition)
	}
	result.Action = "file_selected"
	result.Target = selector
	result.Outcome = string(OutcomeValueVerified)
	result.Verified = true
	result.Evidence = evidence
	result.RecoveryHint = "The browser input contains the selected file; verify the page or server response to confirm the site accepted the upload."
	result.OperationOrdinal = s.operationOrdinalForResult("upload_file", selector)
	result.Receipt = s.newActionReceipt("upload_file", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true, result.OperationOrdinal, &condition, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("upload_file", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true)
	return result, err
}

// PageDiff compares two pages and returns the differences.
type PageDiff struct {
	URL1           string               `json:"url1"`
	URL2           string               `json:"url2"`
	Title1         string               `json:"title1"`
	Title2         string               `json:"title2"`
	OnlyIn1        []string             `json:"only_in_1,omitempty"`
	OnlyIn2        []string             `json:"only_in_2,omitempty"`
	Different      map[string][2]string `json:"different,omitempty"` // field -> [value1, value2]
	OnlyIn1Total   int                  `json:"only_in_1_total"`
	OnlyIn2Total   int                  `json:"only_in_2_total"`
	DifferentTotal int                  `json:"different_total"`
	TotalItems1    int                  `json:"total_items_1"`
	TotalItems2    int                  `json:"total_items_2"`
	Truncated      bool                 `json:"truncated"`
}

// CompareTabs compares the content of two named tabs.
func (s *Session) CompareTabs(tab1, tab2 string) (*PageDiff, error) {
	return s.CompareTabsContext(context.Background(), tab1, tab2)
}

// CompareTabsContext compares two named tabs while honoring ctx for lock
// queueing and both browser evaluations.
func (s *Session) CompareTabsContext(ctx context.Context, tab1, tab2 string) (*PageDiff, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
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
		const maxItems = 40, maxText = 256;
		const data = {title: document.title.slice(0, 500), url: window.location.href.slice(0, 2048), items: {}, total: 0, truncated: false};
		function add(key, value) {
			data.total++;
			const clippedKey = Array.from(key).slice(0, 100).join('');
			const chars = Array.from(value);
			if (chars.length > maxText || clippedKey !== key) data.truncated = true;
			if (Object.keys(data.items).length >= maxItems) { data.truncated = true; return; }
			data.items[clippedKey] = chars.slice(0, maxText).join('');
		}
		// Extract headings
		for (const h of document.querySelectorAll('h1,h2,h3')) {
			const text = h.textContent.trim();
			const key = h.tagName + ':' + text.slice(0, 50);
			add(key, text);
		}
		// Extract prices/values
		for (const el of document.querySelectorAll('.price,[class*="price"],[data-price]')) {
			const key = 'price:' + (el.closest('[class*="title"],[class*="name"],h2,h3')?.textContent.trim().slice(0,30) || el.parentElement?.textContent.trim().slice(0,30) || 'unknown');
			add(key, el.textContent.trim());
		}
		// Extract links
		for (const a of document.querySelectorAll('a[href]')) {
			const text = a.textContent.trim();
			if (text) add('link:'+text.slice(0,40), a.getAttribute('href') || '');
		}
		if (document.title.length > 500 || window.location.href.length > 2048) data.truncated = true;
		return JSON.stringify(data);
	})()`

	r1, err := t1.page.EvaluateContext(ctx, extractJS)
	if err != nil {
		return nil, err
	}
	r2, err := t2.page.EvaluateContext(ctx, extractJS)
	if err != nil {
		return nil, err
	}

	var d1, d2 struct {
		Title     string            `json:"title"`
		URL       string            `json:"url"`
		Items     map[string]string `json:"items"`
		Total     int               `json:"total"`
		Truncated bool              `json:"truncated"`
	}
	s1, _ := r1.(string)
	s2, _ := r2.(string)
	_ = json.Unmarshal([]byte(s1), &d1)
	_ = json.Unmarshal([]byte(s2), &d2)

	diff := &PageDiff{
		URL1:        d1.URL,
		URL2:        d2.URL,
		Title1:      d1.Title,
		Title2:      d2.Title,
		Different:   make(map[string][2]string),
		TotalItems1: d1.Total,
		TotalItems2: d2.Total,
		Truncated:   d1.Truncated || d2.Truncated,
	}

	for k, v1 := range d1.Items {
		if v2, ok := d2.Items[k]; ok {
			if v1 != v2 {
				diff.Different[k] = [2]string{v1, v2}
				diff.DifferentTotal++
			}
		} else {
			diff.OnlyIn1 = append(diff.OnlyIn1, k+"="+v1)
			diff.OnlyIn1Total++
		}
	}
	for k, v2 := range d2.Items {
		if _, ok := d1.Items[k]; !ok {
			diff.OnlyIn2 = append(diff.OnlyIn2, k+"="+v2)
			diff.OnlyIn2Total++
		}
	}

	return diff, nil
}
