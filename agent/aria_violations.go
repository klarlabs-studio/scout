package agent

// AriaViolation is one finding from a minimal a11y scan.
// scout intentionally keeps this lightweight — no axe-core bundle,
// no third-party dependency — so it ships with every binary and
// surfaces the common, high-confidence issues without false-positives.
type AriaViolation struct {
	Rule     string `json:"rule"`
	Impact   string `json:"impact"` // "critical" | "serious" | "moderate" | "minor"
	Selector string `json:"selector,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Detail   string `json:"detail"`
}

// AriaViolationReport groups violations by impact.
type AriaViolationReport struct {
	Critical []AriaViolation `json:"critical,omitempty"`
	Serious  []AriaViolation `json:"serious,omitempty"`
	Moderate []AriaViolation `json:"moderate,omitempty"`
	Minor    []AriaViolation `json:"minor,omitempty"`
	Count    int             `json:"count"`
}

// AriaViolations runs a minimal accessibility scan and returns
// findings grouped by impact. The scan covers the most common
// (and most actionable) failures — images without alt, buttons
// and links without accessible names, form fields without labels,
// duplicate ids, missing document language, and missing main
// landmark.
//
// This is a deliberate small surface: zero dependencies, runs in
// the browser via one Evaluate call. For full WCAG coverage,
// bundle axe-core in your test pipeline.
func (s *Session) AriaViolations() (*AriaViolationReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	js := `(function() {
		const findings = [];
		function add(rule, impact, el, detail) {
			let selector = '';
			let tag = '';
			if (el) {
				tag = el.tagName ? el.tagName.toLowerCase() : '';
				if (el.id) selector = '#' + el.id;
				else if (el.name) selector = tag + '[name="' + el.name + '"]';
				else selector = tag;
			}
			findings.push({rule:rule, impact:impact, selector:selector, tag:tag, detail:detail});
		}

		function accessibleName(el) {
			const aria = el.getAttribute('aria-label');
			if (aria && aria.trim()) return aria.trim();
			const labelledBy = el.getAttribute('aria-labelledby');
			if (labelledBy) {
				const ref = document.getElementById(labelledBy);
				if (ref) {
					const t = (ref.textContent || '').trim();
					if (t) return t;
				}
			}
			const text = (el.textContent || '').trim();
			if (text) return text;
			const value = el.value || '';
			if (value) return value;
			const title = el.getAttribute('title');
			if (title && title.trim()) return title.trim();
			return '';
		}

		// 1. Images without alt (critical for icon images, serious otherwise)
		for (const img of document.querySelectorAll('img')) {
			if (!img.hasAttribute('alt')) {
				add('image-alt', 'serious', img, 'img missing alt attribute (use alt="" for decorative images)');
			}
		}

		// 2. Buttons with no accessible name
		for (const btn of document.querySelectorAll('button, [role="button"]')) {
			if (!accessibleName(btn)) {
				add('button-name', 'critical', btn, 'button has no accessible name');
			}
		}

		// 3. Links with no accessible name
		for (const a of document.querySelectorAll('a[href]')) {
			if (!accessibleName(a)) {
				add('link-name', 'serious', a, 'link has no accessible name');
			}
		}

		// 4. Form fields with no label / aria-label
		for (const input of document.querySelectorAll('input, textarea, select')) {
			const t = (input.type || '').toLowerCase();
			if (t === 'hidden' || t === 'submit' || t === 'button' || t === 'reset') continue;
			let hasLabel = false;
			if (input.id) {
				if (document.querySelector('label[for="' + CSS.escape(input.id) + '"]')) hasLabel = true;
			}
			if (!hasLabel && input.closest('label')) hasLabel = true;
			if (!hasLabel && input.getAttribute('aria-label')) hasLabel = true;
			if (!hasLabel && input.getAttribute('aria-labelledby')) hasLabel = true;
			if (!hasLabel && input.getAttribute('title')) hasLabel = true;
			if (!hasLabel) {
				add('label', 'critical', input, 'form field has no label');
			}
		}

		// 5. Duplicate ids
		const seen = {};
		for (const el of document.querySelectorAll('[id]')) {
			const id = el.id;
			if (seen[id]) {
				add('duplicate-id', 'moderate', el, 'duplicate id "' + id + '"');
			} else {
				seen[id] = true;
			}
		}

		// 6. Missing document language
		if (!document.documentElement.lang) {
			add('html-has-lang', 'serious', document.documentElement, '<html> missing lang attribute');
		}

		// 7. Missing <main> landmark (skip very short docs)
		if (document.body && document.body.innerText && document.body.innerText.length > 200) {
			if (!document.querySelector('main, [role="main"]')) {
				add('landmark-one-main', 'moderate', null, 'page has no <main> or [role=main] landmark');
			}
		}

		return findings;
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	report := &AriaViolationReport{}
	arr, ok := result.([]any)
	if !ok {
		return report, nil
	}
	for _, raw := range arr {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		v := AriaViolation{}
		v.Rule, _ = m["rule"].(string)
		v.Impact, _ = m["impact"].(string)
		v.Selector, _ = m["selector"].(string)
		v.Tag, _ = m["tag"].(string)
		v.Detail, _ = m["detail"].(string)
		report.Count++
		switch v.Impact {
		case "critical":
			report.Critical = append(report.Critical, v)
		case "serious":
			report.Serious = append(report.Serious, v)
		case "moderate":
			report.Moderate = append(report.Moderate, v)
		default:
			report.Minor = append(report.Minor, v)
		}
	}
	return report, nil
}
