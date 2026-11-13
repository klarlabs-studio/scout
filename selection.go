package browse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	cdpclient "go.klarlabs.de/scout/internal/cdp"
)

// Selection wraps a single page element with a fluent, chainable API.
type Selection struct {
	page     *Page
	nodeID   int64
	selector string
	err      error
}

// NewSelection creates a Selection for the given page and node ID.
func NewSelection(page *Page, nodeID int64, selector string) *Selection {
	return &Selection{page: page, nodeID: nodeID, selector: selector}
}

// Err returns the accumulated error, if any.
func (s *Selection) Err() error {
	return s.err
}

// --- Interaction ---

// Click clicks the element.
func (s *Selection) Click() error {
	return s.ClickContext(context.Background())
}

// ClickContext clicks the element using the caller's context for every CDP
// command in the input sequence.
func (s *Selection) ClickContext(ctx context.Context) error {
	return s.ClickContextWithGuard(ctx, nil)
}

// ClickContextWithGuard clicks the element and invokes guard after resolving
// its rendered click point but immediately before the first mouse event. If
// guard returns an error, no input event is dispatched. This narrows the
// interval in which another browser client can replace the page after a
// caller's identity check; it does not provide exclusive control over the
// browser or make the check and input dispatch atomic.
func (s *Selection) ClickContextWithGuard(ctx context.Context, guard func() error) error {
	if s.err != nil {
		return s.err
	}
	x, y, err := s.contentPointContext(ctx)
	if err != nil {
		return fmt.Errorf("browse: click failed to locate content for %q: %w", s.selector, err)
	}
	if guard != nil {
		if err := guard(); err != nil {
			return fmt.Errorf("browse: click pre-dispatch guard failed for %q: %w", s.selector, err)
		}
	}
	dispatched := false
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		_, err := s.page.CallContext(ctx, "Input.dispatchMouseEvent", map[string]any{"type": typ, "x": x, "y": y, "button": "left", "clickCount": 1})
		if err != nil {
			if dispatched {
				return &cdpclient.DispatchOutcomeUnknownError{Method: "Input.dispatchMouseEvent", Err: err}
			}
			var outcomeErr *cdpclient.DispatchOutcomeUnknownError
			if errors.As(err, &outcomeErr) && outcomeErr.Method == "Input.dispatchMouseEvent" {
				return err
			}
			return fmt.Errorf("browse: click %s failed: %w", typ, err)
		}
		dispatched = true
	}
	return nil
}

// MustClick clicks the element and panics on error.
func (s *Selection) MustClick() *Selection {
	if err := s.Click(); err != nil {
		panic(err)
	}
	return s
}

// Input focuses the element, clears it, and types the given text.
func (s *Selection) Input(text string) error {
	return s.InputContext(context.Background(), text)
}

// InputContext clears, focuses, and types into the element using ctx for each
// underlying CDP command.
func (s *Selection) InputContext(ctx context.Context, text string) error {
	return s.InputContextWithGuard(ctx, text, nil)
}

// PressKeyContext dispatches a key press to the currently focused element.
// key must be a supported key name such as Enter, ArrowDown, a, or Control+A.
func (s *Selection) PressKeyContext(ctx context.Context, key string) error {
	return s.PressKeyWithModifiersContext(ctx, key, 0)
}

func (s *Selection) PressKeyWithModifiersContext(ctx context.Context, key string, modifiers int) error {
	if s.err != nil {
		return s.err
	}
	events, err := keyPressEventsWithModifiers(key, modifiers)
	if err != nil {
		return err
	}
	dispatched := false
	var held []map[string]any
	modifierCount := len(strings.Split(key, "+")) - 1
	for index, params := range events {
		_, err := s.page.CallContext(ctx, "Input.dispatchKeyEvent", params)
		if err != nil {
			// If a chord was interrupted after pressing modifiers, best-effort
			// release them without the cancelled caller deadline.
			for i := len(held) - 1; i >= 0; i-- {
				_, _ = s.page.CallContext(context.Background(), "Input.dispatchKeyEvent", held[i])
			}
			if dispatched {
				return &cdpclient.DispatchOutcomeUnknownError{Method: "Input.dispatchKeyEvent", Err: err}
			}
			var outcomeErr *cdpclient.DispatchOutcomeUnknownError
			if errors.As(err, &outcomeErr) && outcomeErr.Method == "Input.dispatchKeyEvent" {
				return err
			}
			return fmt.Errorf("browse: key press %q failed: %w", key, err)
		}
		dispatched = true
		if index < modifierCount {
			held = append(held, map[string]any{"type": "keyUp", "key": params["key"], "code": params["code"], "windowsVirtualKeyCode": params["windowsVirtualKeyCode"], "nativeVirtualKeyCode": params["nativeVirtualKeyCode"], "modifiers": 0})
		}
	}
	return nil
}

// KeyControlContext dispatches one keyDown or keyUp event without releasing
// it. Pair keyDown and keyUp calls to release each held key.
func (s *Selection) KeyControlContext(ctx context.Context, key string, down bool) error {
	return s.KeyControlWithModifiersContext(ctx, key, down, 0)
}

func (s *Selection) KeyControlWithModifiersContext(ctx context.Context, key string, down bool, modifiers int) error {
	if s.err != nil {
		return s.err
	}
	var k keyDefinition
	var err error
	if _, isModifier := map[string]bool{"Control": true, "Shift": true, "Alt": true, "Meta": true}[key]; isModifier {
		k = keyDefinition{key: key, code: key + "Left", vk: map[string]int{"Control": 17, "Shift": 16, "Alt": 18, "Meta": 91}[key]}
	} else {
		k, err = resolveKey(key)
		if err != nil {
			return err
		}
	}
	typ := "keyUp"
	if down {
		typ = "keyDown"
	}
	p := map[string]any{"type": typ, "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk, "modifiers": modifiers}
	if k.text != "" && down && modifiers&7 == 0 {
		unmodified := k.text
		if k.key >= "A" && k.key <= "Z" {
			unmodified = strings.ToLower(k.key)
		}
		if len(k.key) == 1 {
			if base, ok := shiftedKeyText[k.key[0]]; ok {
				unmodified = base
			}
		}
		p["text"], p["unmodifiedText"] = k.text, unmodified
	}
	_, err = s.page.CallContext(ctx, "Input.dispatchKeyEvent", p)
	if err != nil {
		return &cdpclient.DispatchOutcomeUnknownError{Method: "Input.dispatchKeyEvent", Err: err}
	}
	return nil
}

// ResolveKeyForControl validates a single key accepted by KeyControlContext.
func ResolveKeyForControl(key string) error {
	if key == "Control" || key == "Shift" || key == "Alt" || key == "Meta" {
		return nil
	}
	_, err := resolveKey(key)
	return err
}

// InputContextWithGuard checks guard immediately before clearing/focusing the
// element and again before inserting text. The second check matters because
// focus can move when another CDP client navigates the page between commands.
// It narrows that window but cannot exclude a concurrent command after the
// guard returns.
func (s *Selection) InputContextWithGuard(ctx context.Context, text string, guard func() error) error {
	if s.err != nil {
		return s.err
	}
	objectID, err := s.page.ResolveNodeContext(ctx, s.nodeID)
	if err != nil {
		return fmt.Errorf("browse: input resolve failed: %w", err)
	}
	if guard != nil {
		if err := guard(); err != nil {
			return fmt.Errorf("browse: input pre-clear guard failed for %q: %w", s.selector, err)
		}
	}
	_, err = s.callFunctionOnContext(ctx, objectID, `function() { this.value = ''; this.focus(); }`)
	if err != nil {
		var outcomeErr *cdpclient.DispatchOutcomeUnknownError
		if errors.As(err, &outcomeErr) && outcomeErr.Method == "Runtime.callFunctionOn" {
			return err
		}
		return fmt.Errorf("browse: input clear/focus failed: %w", err)
	}
	if guard != nil {
		if err := guard(); err != nil {
			return fmt.Errorf("browse: input pre-insert guard failed for %q: %w", s.selector, err)
		}
	}
	_, err = s.page.CallContext(ctx, "Input.insertText", map[string]any{"text": text})
	if err != nil {
		return &cdpclient.DispatchOutcomeUnknownError{Method: "Input.insertText", Err: fmt.Errorf("browse: input text failed after clearing/focusing the field: %w", err)}
	}
	return nil
}

// MustInput types text and panics on error.
func (s *Selection) MustInput(text string) *Selection {
	if err := s.Input(text); err != nil {
		panic(err)
	}
	return s
}

// Clear clears the element's value.
func (s *Selection) Clear() error {
	if s.err != nil {
		return s.err
	}
	return s.Input("")
}

// Hover moves the mouse over the element.
func (s *Selection) Hover() error {
	return s.HoverContext(context.Background())
}

// HoverContext moves the pointer to the element center using ctx for CDP.
func (s *Selection) HoverContext(ctx context.Context) error {
	if s.err != nil {
		return s.err
	}
	x, y, err := s.contentPointContext(ctx)
	if err != nil {
		return err
	}
	_, err = s.page.CallContext(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y})
	return err
}

// contentPointContext chooses the center of the largest rendered content quad.
// A union box can place its center outside inline text split across lines, so
// prefer per-fragment quads and retain the box model only as a fallback.
func (s *Selection) contentPointContext(ctx context.Context) (float64, float64, error) {
	result, err := s.page.CallContext(ctx, "DOM.getContentQuads", map[string]any{"nodeId": s.nodeID})
	if err != nil {
		return 0, 0, err
	}
	var content struct {
		Quads [][]float64 `json:"quads"`
	}
	if err := json.Unmarshal(result, &content); err != nil {
		return 0, 0, fmt.Errorf("parse content quads: %w", err)
	}
	var best []float64
	var bestArea float64
	for _, quad := range content.Quads {
		if len(quad) < 8 {
			continue
		}
		area := 0.0
		for i := 0; i < 4; i++ {
			j := (i + 1) % 4
			area += quad[i*2]*quad[j*2+1] - quad[j*2]*quad[i*2+1]
		}
		if area < 0 {
			area = -area
		}
		if best == nil || area > bestArea {
			best, bestArea = quad, area
		}
	}
	if best != nil {
		var x, y float64
		for i := 0; i < 4; i++ {
			x += best[i*2] / 4
			y += best[i*2+1] / 4
		}
		return x, y, nil
	}
	result, err = s.page.CallContext(ctx, "DOM.getBoxModel", map[string]any{"nodeId": s.nodeID})
	if err != nil {
		return 0, 0, err
	}
	var box struct {
		Model struct {
			Content []float64 `json:"content"`
		} `json:"model"`
	}
	if err := json.Unmarshal(result, &box); err != nil {
		return 0, 0, fmt.Errorf("parse box model: %w", err)
	}
	if len(box.Model.Content) < 8 {
		return 0, 0, fmt.Errorf("invalid box model for %q", s.selector)
	}
	contentBox := box.Model.Content
	x := (contentBox[0] + contentBox[2] + contentBox[4] + contentBox[6]) / 4
	y := (contentBox[1] + contentBox[3] + contentBox[5] + contentBox[7]) / 4
	return x, y, nil
}

// --- Extraction ---

// Text returns the element's text content.
func (s *Selection) Text() (string, error) {
	if s.err != nil {
		return "", s.err
	}
	objectID, err := s.page.ResolveNode(s.nodeID)
	if err != nil {
		return "", err
	}
	result, err := s.callFunctionOn(objectID, `function() { return this.textContent; }`)
	if err != nil {
		return "", err
	}
	str, _ := result.(string)
	return str, nil
}

// MustText returns the element's text content, panicking on error.
func (s *Selection) MustText() string {
	t, err := s.Text()
	if err != nil {
		panic(err)
	}
	return t
}

// Attr returns the value of the given attribute.
func (s *Selection) Attr(name string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	objectID, err := s.page.ResolveNode(s.nodeID)
	if err != nil {
		return "", err
	}
	result, err := s.callFunctionOn(objectID, fmt.Sprintf(`function() { return this.getAttribute(%q); }`, name))
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	str, _ := result.(string)
	return str, nil
}

// Visible reports whether the element is visible.
func (s *Selection) Visible() (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	objectID, err := s.page.ResolveNode(s.nodeID)
	if err != nil {
		return false, err
	}
	result, err := s.callFunctionOn(objectID, `function() {
		const style = window.getComputedStyle(this);
		return style.display !== 'none' && style.visibility !== 'hidden' && style.opacity !== '0';
	}`)
	if err != nil {
		return false, err
	}
	b, _ := result.(bool)
	return b, nil
}

// Screenshot captures a screenshot of this element only.
func (s *Selection) Screenshot() ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.page.ScreenshotElement(s.nodeID)
}

// Value returns the element's value property (for inputs).
func (s *Selection) Value() (string, error) {
	if s.err != nil {
		return "", s.err
	}
	objectID, err := s.page.ResolveNode(s.nodeID)
	if err != nil {
		return "", err
	}
	result, err := s.callFunctionOn(objectID, `function() { return this.value; }`)
	if err != nil {
		return "", err
	}
	str, _ := result.(string)
	return str, nil
}

// --- Waiting ---

// WaitVisible waits until the element is visible.
func (s *Selection) WaitVisible() *Selection {
	if s.err != nil {
		return s
	}
	objectID, err := s.page.ResolveNode(s.nodeID)
	if err != nil {
		s.err = err
		return s
	}
	timeout := s.page.timeout.Milliseconds()
	_, err = s.callFunctionOn(objectID, fmt.Sprintf(`function() {
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => reject(new Error('timeout waiting for visible')), %d);
			const check = () => {
				const style = window.getComputedStyle(this);
				if (style.display !== 'none' && style.visibility !== 'hidden') {
					clearTimeout(timer);
					resolve(true);
				} else {
					requestAnimationFrame(check);
				}
			};
			check();
		});
	}`, timeout))
	if err != nil {
		s.err = err
	}
	return s
}

// WaitStable waits until the element's position is stable.
func (s *Selection) WaitStable() *Selection {
	if s.err != nil {
		return s
	}
	objectID, err := s.page.ResolveNode(s.nodeID)
	if err != nil {
		s.err = err
		return s
	}
	timeout := s.page.timeout.Milliseconds()
	_, err = s.callFunctionOn(objectID, fmt.Sprintf(`function() {
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => reject(new Error('timeout waiting for stable')), %d);
			let prev = this.getBoundingClientRect();
			const check = () => {
				const curr = this.getBoundingClientRect();
				if (prev.x === curr.x && prev.y === curr.y && prev.width === curr.width && prev.height === curr.height) {
					clearTimeout(timer);
					resolve(true);
				} else {
					prev = curr;
					requestAnimationFrame(check);
				}
			};
			requestAnimationFrame(check);
		});
	}`, timeout))
	if err != nil {
		s.err = err
	}
	return s
}

// WaitEnabled waits until the element is enabled.
func (s *Selection) WaitEnabled() *Selection {
	if s.err != nil {
		return s
	}
	objectID, err := s.page.ResolveNode(s.nodeID)
	if err != nil {
		s.err = err
		return s
	}
	timeout := s.page.timeout.Milliseconds()
	_, err = s.callFunctionOn(objectID, fmt.Sprintf(`function() {
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => reject(new Error('timeout waiting for enabled')), %d);
			const check = () => {
				if (!this.disabled) {
					clearTimeout(timer);
					resolve(true);
				} else {
					requestAnimationFrame(check);
				}
			};
			check();
		});
	}`, timeout))
	if err != nil {
		s.err = err
	}
	return s
}

func (s *Selection) callFunctionOn(objectID, fn string) (any, error) {
	result, err := s.page.call("Runtime.callFunctionOn", map[string]any{
		"functionDeclaration": fn,
		"objectId":            objectID,
		"returnByValue":       true,
		"awaitPromise":        true,
	})
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	if resp.ExceptionDetails != nil {
		return nil, fmt.Errorf("browse: js error: %s", resp.ExceptionDetails.Text)
	}

	var val any
	if len(resp.Result.Value) == 0 {
		return nil, nil // undefined result
	}
	if err := json.Unmarshal(resp.Result.Value, &val); err != nil {
		return nil, nil //nolint:nilerr // unmarshal of undefined/void JS results is expected
	}
	return val, nil
}

func (s *Selection) callFunctionOnContext(ctx context.Context, objectID, fn string) (any, error) {
	result, err := s.page.CallContext(ctx, "Runtime.callFunctionOn", map[string]any{"functionDeclaration": fn, "objectId": objectID, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	if resp.ExceptionDetails != nil {
		return nil, fmt.Errorf("browse: js error: %s", resp.ExceptionDetails.Text)
	}
	var val any
	if len(resp.Result.Value) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(resp.Result.Value, &val); err != nil {
		return nil, fmt.Errorf("browse: decode javascript result: %w", err)
	}
	return val, nil
}
