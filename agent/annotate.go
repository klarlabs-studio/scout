package agent

import (
	"encoding/json"
	"fmt"

	browse "github.com/felixgeelhaar/scout"
)

// AnnotatedScreenshot captures a screenshot with numbered labels overlaid on
// interactive elements. Returns the image data and a mapping of label numbers
// to element info (selector, text, type). Designed for multimodal LLMs that
// can reference elements by number instead of CSS selectors.
func (s *Session) AnnotatedScreenshot() (*AnnotatedResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	// Inject labels and collect element mapping
	js := `(function() {
		// Remove any previous annotations
		document.querySelectorAll('.__browse_label').forEach(el => el.remove());

		const elements = [];
		let idx = 0;
		const selectors = 'a[href], button, input, textarea, select, [role="button"], [onclick], [tabindex]';

		for (const el of document.querySelectorAll(selectors)) {
			// Skip hidden/tiny elements
			const rect = el.getBoundingClientRect();
			if (rect.width < 5 || rect.height < 5) continue;
			const style = window.getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') continue;

			idx++;
			const tag = el.tagName.toLowerCase();

			// Build selector
			let selector = tag;
			if (el.id) selector = '#' + CSS.escape(el.id);
			else if (el.name) selector = tag + '[name="' + el.name + '"]';

			// Build label
			let text = '';
			if (tag === 'input' || tag === 'textarea') {
				text = el.placeholder || el.value || '';
			} else if (tag === 'select') {
				text = el.options[el.selectedIndex] ? el.options[el.selectedIndex].text : '';
			} else {
				text = el.textContent.trim().slice(0, 60);
			}

			elements.push({
				label: idx,
				selector: selector,
				tag: tag,
				type: el.type || '',
				text: text,
				href: el.getAttribute('href') || '',
				x: Math.round(rect.x),
				y: Math.round(rect.y),
				width: Math.round(rect.width),
				height: Math.round(rect.height)
			});

			// Create visual label overlay
			const marker = document.createElement('div');
			marker.className = '__browse_label';
			marker.textContent = idx;
			marker.style.cssText = 'position:fixed;z-index:999999;pointer-events:none;' +
				'background:#e63946;color:#fff;font:bold 11px/14px monospace;' +
				'padding:0 4px;border-radius:7px;min-width:14px;text-align:center;' +
				'left:' + Math.max(0, rect.x - 2) + 'px;' +
				'top:' + Math.max(0, rect.y - 14) + 'px;' +
				'box-shadow:0 1px 3px rgba(0,0,0,0.4);';
			document.body.appendChild(marker);
		}

		return JSON.stringify(elements);
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("annotation failed: %w", err)
	}

	// Parse element mapping
	var elements []AnnotatedElement
	if str, ok := result.(string); ok {
		_ = json.Unmarshal([]byte(str), &elements)
	}

	// Capture screenshot with labels visible
	maxSize := s.contentOpts.MaxScreenshotBytes
	if maxSize == 0 {
		maxSize = 5 * 1024 * 1024
	}
	screenshot, err := s.page.ScreenshotWithOptions(browse.ScreenshotOptions{
		MaxSize: maxSize,
	})

	// Remove labels after screenshot
	_, _ = s.page.Evaluate(`document.querySelectorAll('.__browse_label').forEach(el => el.remove())`)

	if err != nil {
		return nil, fmt.Errorf("annotated screenshot failed: %w", err)
	}

	return &AnnotatedResult{
		Image:      screenshot,
		Elements:   elements,
		Count:      len(elements),
		LabelScope: "per_call",
	}, nil
}

// ClickLabel clicks the element with the given annotation label number.
// Use after AnnotatedScreenshot to interact by label instead of selector.
func (s *Session) ClickLabel(label int) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	// Find element by re-querying (labels may have been removed)
	js := fmt.Sprintf(`(function() {
		let idx = 0;
		const selectors = 'a[href], button, input, textarea, select, [role="button"], [onclick], [tabindex]';
		for (const el of document.querySelectorAll(selectors)) {
			const rect = el.getBoundingClientRect();
			if (rect.width < 5 || rect.height < 5) continue;
			const style = window.getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') continue;
			idx++;
			if (idx === %d) {
				el.click();
				return true;
			}
		}
		return false;
	})()`, label)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if b, ok := result.(bool); !ok || !b {
		return nil, fmt.Errorf("label %d not found", label)
	}

	_ = s.page.WaitStable(300 * 1e6) // 300ms

	return s.pageResult()
}
