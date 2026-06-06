package agent

import (
	"encoding/json"
	"fmt"

	browse "go.klarlabs.de/scout"
)

func (s *Session) HybridObserve() (*HybridResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	js := `(function() {
		const elements = [];
		let idx = 0;
		const selectors = 'a[href], button, input, textarea, select, [role="button"], [onclick], [tabindex]';

		for (const el of document.querySelectorAll(selectors)) {
			const rect = el.getBoundingClientRect();
			if (rect.width < 5 || rect.height < 5) continue;
			const style = window.getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') continue;

			idx++;
			const tag = el.tagName.toLowerCase();

			let selector = tag;
			if (el.id) selector = '#' + CSS.escape(el.id);
			else if (el.name) selector = tag + '[name="' + el.name + '"]';

			let text = '';
			if (tag === 'input' || tag === 'textarea') {
				text = el.placeholder || el.value || '';
			} else if (tag === 'select') {
				text = el.options[el.selectedIndex] ? el.options[el.selectedIndex].text : '';
			} else {
				text = el.textContent.trim().slice(0, 60);
			}

			const role = el.getAttribute('role') || '';

			elements.push({
				index: idx,
				tag: tag,
				text: text,
				selector: selector,
				role: role,
				x: rect.x,
				y: rect.y,
				width: rect.width,
				height: rect.height
			});
		}

		return JSON.stringify({
			elements: elements,
			width: window.innerWidth,
			height: window.innerHeight
		});
	})()`

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("hybrid observe failed: %w", err)
	}

	str, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("unexpected hybrid observe result type")
	}

	var parsed struct {
		Elements []HybridElement `json:"elements"`
		Width    int             `json:"width"`
		Height   int             `json:"height"`
	}
	if err := json.Unmarshal([]byte(str), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse hybrid observe result: %w", err)
	}

	maxSize := s.contentOpts.MaxScreenshotBytes
	if maxSize == 0 {
		maxSize = 5 * 1024 * 1024
	}
	screenshot, err := s.page.ScreenshotWithOptions(browse.ScreenshotOptions{
		MaxSize: maxSize,
	})
	if err != nil {
		return nil, fmt.Errorf("hybrid observe screenshot failed: %w", err)
	}

	return &HybridResult{
		Screenshot: screenshot,
		Elements:   parsed.Elements,
		Width:      parsed.Width,
		Height:     parsed.Height,
	}, nil
}

func (s *Session) FindByCoordinates(x, y int) (*PromptSelectResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	js := fmt.Sprintf(`(function() {
		const px = %d;
		const py = %d;
		const candidates = [];
		const selectors = 'a[href], button, input, textarea, select, [role="button"], [onclick], [tabindex]';

		for (const el of document.querySelectorAll(selectors)) {
			const rect = el.getBoundingClientRect();
			if (rect.width < 5 || rect.height < 5) continue;
			const style = window.getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') continue;

			if (px < rect.x || px > rect.x + rect.width) continue;
			if (py < rect.y || py > rect.y + rect.height) continue;

			const tag = el.tagName.toLowerCase();

			let selector = tag;
			if (el.id) selector = '#' + CSS.escape(el.id);
			else if (el.name) selector = tag + '[name="' + el.name + '"]';
			else {
				const parent = el.parentElement;
				if (parent) {
					const siblings = Array.from(parent.children).filter(c => c.tagName === el.tagName);
					if (siblings.length === 1) {
						let parentSel = parent.tagName.toLowerCase();
						if (parent.id) parentSel = '#' + CSS.escape(parent.id);
						selector = parentSel + ' > ' + tag;
					} else {
						const idx = siblings.indexOf(el) + 1;
						let parentSel = parent.tagName.toLowerCase();
						if (parent.id) parentSel = '#' + CSS.escape(parent.id);
						selector = parentSel + ' > ' + tag + ':nth-of-type(' + idx + ')';
					}
				}
			}

			let text = '';
			if (tag === 'input' || tag === 'textarea') {
				text = (el.value || '').trim();
			} else {
				text = (el.textContent || '').trim().slice(0, 100);
			}

			const role = el.getAttribute('role') || '';
			const area = rect.width * rect.height;

			candidates.push({
				selector: selector,
				text: text,
				tag: tag,
				role: role,
				area: area
			});
		}

		candidates.sort((a, b) => a.area - b.area);
		return JSON.stringify(candidates.slice(0, 3));
	})()`, x, y)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("find by coordinates failed: %w", err)
	}

	str, _ := result.(string)
	var candidates []struct {
		Selector string  `json:"selector"`
		Text     string  `json:"text"`
		Tag      string  `json:"tag"`
		Role     string  `json:"role"`
		Area     float64 `json:"area"`
	}
	if err := json.Unmarshal([]byte(str), &candidates); err != nil {
		return nil, fmt.Errorf("failed to parse coordinate candidates: %w", err)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no interactive element found at coordinates (%d, %d)", x, y)
	}

	best := candidates[0]
	psr := &PromptSelectResult{
		Selector:   best.Selector,
		Text:       best.Text,
		Tag:        best.Tag,
		Role:       best.Role,
		Confidence: 1.0,
	}

	for _, c := range candidates {
		psr.Candidates = append(psr.Candidates, PromptCandidate{
			Selector: c.Selector,
			Text:     c.Text,
			Score:    100.0,
		})
	}

	return psr, nil
}
