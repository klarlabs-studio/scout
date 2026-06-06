package agent

import (
	"encoding/json"
	"fmt"
	"time"

	browse "go.klarlabs.de/scout"
)

func (s *Session) ExecuteBatch(actions []BatchAction) (*BatchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	result := &BatchResult{
		Total:   len(actions),
		Results: make([]BatchActionResult, 0, len(actions)),
	}

	for i, action := range actions {
		ar := BatchActionResult{
			Index:  i,
			Action: action.Action,
		}

		err := s.executeSingleAction(action)
		if err != nil {
			ar.Error = err.Error()
			result.Failed++
		} else {
			ar.Success = true
			result.Succeeded++
		}

		result.Results = append(result.Results, ar)
	}

	return result, nil
}

func (s *Session) executeSingleAction(action BatchAction) error {
	switch action.Action {
	case "click":
		return s.batchClick(action.Selector)
	case "type":
		return s.batchType(action.Selector, action.Value)
	case "fill_form_semantic":
		return s.batchFillFormSemantic(action.Fields)
	case "wait":
		return s.page.WaitForSelector(action.Selector)
	case "scroll_to":
		return s.batchScrollTo(action.Selector)
	case "click_label":
		return s.batchClickLabel(action.Label)
	default:
		return fmt.Errorf("unknown batch action %q", action.Action)
	}
}

func (s *Session) batchClick(selector string) error {
	if selector == "" {
		return fmt.Errorf("click action requires a selector")
	}

	nodeID, err := s.querySelector(selector)
	if err != nil {
		return err
	}
	sel := browse.NewSelection(s.page, nodeID, selector)
	if err := sel.Click(); err != nil {
		return err
	}

	_ = s.page.WaitStable(300 * time.Millisecond)
	s.recordAction(Action{Type: "click", Selector: selector})
	s.addHistory("click", selector, "", "")
	return nil
}

func (s *Session) batchType(selector, text string) error {
	if selector == "" {
		return fmt.Errorf("type action requires a selector")
	}

	selectorJSON, _ := json.Marshal(selector)
	textJSON, _ := json.Marshal(text)
	js := fmt.Sprintf(`(function() {
		const el = document.querySelector(%s);
		if (!el) return false;
		el.focus();
		// Use native setter to trigger React/Vue/Angular synthetic event systems.
		const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype
			: el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype
			: HTMLInputElement.prototype;
		const nativeSetter = Object.getOwnPropertyDescriptor(proto, 'value');
		if (nativeSetter && nativeSetter.set) {
			nativeSetter.set.call(el, %s);
		} else {
			el.value = %s;
		}
		el.dispatchEvent(new Event('input', {bubbles: true}));
		el.dispatchEvent(new Event('change', {bubbles: true}));
		return true;
	})()`, selectorJSON, textJSON, textJSON)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return err
	}
	if b, ok := result.(bool); !ok || !b {
		return fmt.Errorf("element %s not found", selector)
	}

	s.recordAction(Action{Type: "type", Selector: selector, Value: text})
	s.addHistory("type", selector, "", text)
	return nil
}

func (s *Session) batchFillFormSemantic(fields map[string]string) error {
	if len(fields) == 0 {
		return fmt.Errorf("fill_form_semantic action requires fields")
	}

	discovery, err := s.discoverFormInternal("")
	if err != nil {
		return err
	}

	var errs []string
	for humanName, value := range fields {
		best := MatchFormField(humanName, discovery.Fields)
		if best == nil {
			errs = append(errs, fmt.Sprintf("no matching field for %q", humanName))
			continue
		}

		nodeID, err := s.page.QuerySelector(best.Selector)
		if err != nil {
			errs = append(errs, fmt.Sprintf("field %q (%s): %v", humanName, best.Selector, err))
			continue
		}

		sel := browse.NewSelection(s.page, nodeID, best.Selector)
		if err := sel.Input(value); err != nil {
			errs = append(errs, fmt.Sprintf("field %q (%s): %v", humanName, best.Selector, err))
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("some fields failed: %s", errs[0])
	}
	return nil
}

func (s *Session) batchScrollTo(selector string) error {
	if selector == "" {
		return fmt.Errorf("scroll_to action requires a selector")
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
		return err
	}
	if b, ok := result.(bool); !ok || !b {
		return fmt.Errorf("element %s not found", selector)
	}

	time.Sleep(300 * time.Millisecond)
	return nil
}

func (s *Session) batchClickLabel(label int) error {
	if label <= 0 {
		return fmt.Errorf("click_label action requires a positive label number")
	}

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
		return err
	}
	if b, ok := result.(bool); !ok || !b {
		return fmt.Errorf("label %d not found", label)
	}

	_ = s.page.WaitStable(300 * time.Millisecond)
	s.addHistory("click_label", "", "", fmt.Sprintf("%d", label))
	return nil
}
