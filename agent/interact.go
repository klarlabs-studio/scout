package agent

import (
	"encoding/json"
	"fmt"
	"time"

	browse "go.klarlabs.de/scout"
)

// Hover moves the mouse over an element, triggering CSS :hover states and tooltips.
func (s *Session) Hover(selector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	if err := s.waitAndResolve(selector); err != nil {
		return nil, err
	}

	if err := s.withStaleNodeRetry(selector, func(nodeID int64) error {
		return browse.NewSelection(s.page, nodeID, selector).Hover()
	}); err != nil {
		return nil, err
	}

	// Brief pause for hover effects to render
	time.Sleep(100 * time.Millisecond)
	return s.pageResult()
}

// DoubleClick double-clicks an element.
func (s *Session) DoubleClick(selector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	if err := s.waitAndResolve(selector); err != nil {
		return nil, err
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return false;
		el.dispatchEvent(new MouseEvent('dblclick', {bubbles: true, cancelable: true}));
		return true;
	})()`, selectorJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if b, ok := result.(bool); !ok || !b {
		return nil, fmt.Errorf("element %s not found", selector)
	}

	_ = s.page.WaitStable(300 * time.Millisecond)
	return s.pageResult()
}

// RightClick right-clicks an element (opens context menu).
func (s *Session) RightClick(selector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	if err := s.waitAndResolve(selector); err != nil {
		return nil, err
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return false;
		el.dispatchEvent(new MouseEvent('contextmenu', {bubbles: true, cancelable: true}));
		return true;
	})()`, selectorJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if b, ok := result.(bool); !ok || !b {
		return nil, fmt.Errorf("element %s not found", selector)
	}

	return s.pageResult()
}

// SelectOption selects an option from a <select> element by visible text.
func (s *Session) SelectOption(selector, optionText string) (*ElementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	selectorJSON, _ := json.Marshal(selector)
	optionJSON, _ := json.Marshal(optionText)
	js := fmt.Sprintf(`(function() {
		const sel = document.querySelector(%s);
		if (!sel || sel.tagName !== 'SELECT') return null;
		for (const opt of sel.options) {
			if (opt.text.trim() === %s || opt.value === %s) {
				// Use native setter to trigger React's synthetic event system.
				// React overrides value properties; using the prototype setter
				// ensures React detects the change and fires onChange.
				const nativeSetter = Object.getOwnPropertyDescriptor(
					HTMLSelectElement.prototype, 'value'
				).set;
				nativeSetter.call(sel, opt.value);
				sel.dispatchEvent(new Event('input', {bubbles: true}));
				sel.dispatchEvent(new Event('change', {bubbles: true}));
				return JSON.stringify({value: opt.value, text: opt.text.trim()});
			}
		}
		return null;
	})()`, selectorJSON, optionJSON, optionJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("option %q not found in %s", optionText, selector)
	}

	str, _ := result.(string)
	var selected struct {
		Value string `json:"value"`
		Text  string `json:"text"`
	}
	_ = json.Unmarshal([]byte(str), &selected)

	return &ElementResult{
		Selector: selector,
		Value:    selected.Value,
		Text:     selected.Text,
		Action:   "selected",
	}, nil
}

// ScrollTo scrolls to bring an element into view.
func (s *Session) ScrollTo(selector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return false;
		el.scrollIntoView({behavior: 'smooth', block: 'center'});
		return true;
	})()`, selectorJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if b, ok := result.(bool); !ok || !b {
		return nil, fmt.Errorf("element %s not found", selector)
	}

	time.Sleep(300 * time.Millisecond)
	return s.pageResult()
}

// ScrollBy scrolls the page by a pixel offset.
func (s *Session) ScrollBy(x, y int) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	js := fmt.Sprintf(`window.scrollBy(%d, %d); true`, x, y)
	_, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}

	time.Sleep(200 * time.Millisecond)
	return s.pageResult()
}

// Focus sets focus on an element (triggers :focus CSS state).
func (s *Session) Focus(selector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return false;
		el.focus();
		return true;
	})()`, selectorJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if b, ok := result.(bool); !ok || !b {
		return nil, fmt.Errorf("element %s not found", selector)
	}
	return s.pageResult()
}

// DragDrop drags an element from one selector to another.
func (s *Session) DragDrop(fromSelector, toSelector string) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	fromJSON, _ := json.Marshal(fromSelector)
	toJSON, _ := json.Marshal(toSelector)
	js := fmt.Sprintf(`(function() {
		const from = document.querySelector(%s);
		const to = document.querySelector(%s);
		if (!from || !to) return false;

		const fromRect = from.getBoundingClientRect();
		const toRect = to.getBoundingClientRect();
		const fromX = fromRect.x + fromRect.width/2;
		const fromY = fromRect.y + fromRect.height/2;
		const toX = toRect.x + toRect.width/2;
		const toY = toRect.y + toRect.height/2;

		from.dispatchEvent(new DragEvent('dragstart', {bubbles: true, clientX: fromX, clientY: fromY}));
		to.dispatchEvent(new DragEvent('dragenter', {bubbles: true, clientX: toX, clientY: toY}));
		to.dispatchEvent(new DragEvent('dragover', {bubbles: true, clientX: toX, clientY: toY}));
		to.dispatchEvent(new DragEvent('drop', {bubbles: true, clientX: toX, clientY: toY}));
		from.dispatchEvent(new DragEvent('dragend', {bubbles: true, clientX: toX, clientY: toY}));
		return true;
	})()`, fromJSON, toJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}
	if b, ok := result.(bool); !ok || !b {
		return nil, fmt.Errorf("drag from %s to %s failed", fromSelector, toSelector)
	}

	_ = s.page.WaitStable(300 * time.Millisecond)
	return s.pageResult()
}
