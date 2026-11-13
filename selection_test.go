package browse

import (
	"errors"
	"runtime"
	"testing"
)

func TestNewSelection(t *testing.T) {
	sel := NewSelection(nil, 42, "div.item")
	if sel.nodeID != 42 {
		t.Errorf("nodeID = %d, want 42", sel.nodeID)
	}
	if sel.selector != "div.item" {
		t.Errorf("selector = %q, want %q", sel.selector, "div.item")
	}
	if sel.Err() != nil {
		t.Errorf("Err() = %v, want nil", sel.Err())
	}
}

func TestSelectionErr(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		sel := &Selection{nodeID: 1}
		if sel.Err() != nil {
			t.Errorf("Err() = %v, want nil", sel.Err())
		}
	})

	t.Run("with error", func(t *testing.T) {
		testErr := errors.New("broken")
		sel := &Selection{err: testErr}
		if sel.Err() != testErr {
			t.Errorf("Err() = %v, want %v", sel.Err(), testErr)
		}
	})
}

func TestSelectionClickWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	if err := sel.Click(); err != testErr {
		t.Errorf("Click() should return pre-existing error, got %v", err)
	}
}

func TestSelectionInputWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	if err := sel.Input("text"); err != testErr {
		t.Errorf("Input() should return pre-existing error, got %v", err)
	}
}

func TestSelectionClearWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	if err := sel.Clear(); err != testErr {
		t.Errorf("Clear() should return pre-existing error, got %v", err)
	}
}

func TestSelectionHoverWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	if err := sel.Hover(); err != testErr {
		t.Errorf("Hover() should return pre-existing error, got %v", err)
	}
}

func TestKeyPressEvents(t *testing.T) {
	events, err := keyPressEvents("Control+A")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want modifier down, key down/up, modifier up", len(events))
	}
	if events[0]["type"] != "keyDown" || events[0]["key"] != "Control" || events[0]["modifiers"] != 0 {
		t.Fatalf("modifier event = %#v", events[0])
	}
	if events[1]["key"] != "a" || events[1]["code"] != "KeyA" || events[1]["modifiers"] != 2 {
		t.Fatalf("key event = %#v", events[1])
	}
	if events[3]["type"] != "keyUp" || events[3]["key"] != "Control" {
		t.Fatalf("release event = %#v", events[3])
	}
	if events[3]["modifiers"] != 2 {
		t.Fatalf("release modifier mask = %#v, want Control still held for keyUp", events[3]["modifiers"])
	}
}

func TestMetaAEditingCommandIsPlatformSpecific(t *testing.T) {
	metaEvents, err := keyPressEvents("Meta+A")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		if got := metaEvents[1]["commands"]; got == nil || got.([]string)[0] != "SelectAll" {
			t.Fatalf("macOS Meta+A command = %#v, want SelectAll", got)
		}
	} else if _, ok := metaEvents[1]["commands"]; ok {
		t.Fatalf("non-macOS Meta+A unexpectedly has platform editing commands: %#v", metaEvents[1])
	}

	controlEvents, err := keyPressEvents("Control+A")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := controlEvents[1]["commands"]; ok {
		t.Fatalf("Control+A unexpectedly has platform editing commands: %#v", controlEvents[1])
	}

	heldMetaEvents, err := keyPressEventsWithModifiers("A", 4)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		if got := heldMetaEvents[0]["commands"]; got == nil || got.([]string)[0] != "SelectAll" {
			t.Fatalf("macOS held-Meta+A command = %#v, want SelectAll", got)
		}
	}
}

func TestKeyPressRejectsUnsupportedInput(t *testing.T) {
	for _, key := range []string{"", "Control+Mystery", "Control+Shift+Alt+Meta+A", "Control+Control+A", "Control++A"} {
		if _, err := keyPressEvents(key); err == nil {
			t.Errorf("key %q unexpectedly accepted", key)
		}
	}
}

func TestKeyPressMapsPunctuationAndFunctionKeys(t *testing.T) {
	for key, wantCode := range map[string]string{"!": "Digit1", "?": "Slash", "F12": "F12", "ArrowLeft": "ArrowLeft"} {
		events, err := keyPressEvents(key)
		if err != nil {
			t.Fatalf("key %q: %v", key, err)
		}
		if events[0]["code"] != wantCode {
			t.Errorf("key %q code = %v, want %s", key, events[0]["code"], wantCode)
		}
	}
	events, err := keyPressEvents("Shift+A")
	if err != nil {
		t.Fatal(err)
	}
	if events[1]["text"] != "A" || events[1]["modifiers"] != 8 {
		t.Fatalf("shifted letter keyDown = %#v", events[1])
	}
	if events[1]["unmodifiedText"] != "a" {
		t.Fatalf("shifted letter unmodifiedText = %#v", events[1])
	}
	events, err = keyPressEvents("!")
	if err != nil {
		t.Fatal(err)
	}
	if events[0]["key"] != "!" || events[0]["code"] != "Digit1" || events[0]["windowsVirtualKeyCode"] != 49 || events[0]["text"] != "!" || events[0]["unmodifiedText"] != "1" {
		t.Fatalf("shifted punctuation keyDown = %#v", events[0])
	}
	events, err = keyPressEventsWithModifiers("a", 8)
	if err != nil {
		t.Fatal(err)
	}
	if events[0]["key"] != "A" || events[0]["text"] != "A" || events[0]["unmodifiedText"] != "a" || events[0]["modifiers"] != 8 {
		t.Fatalf("held-shift lowercase keyDown = %#v", events[0])
	}
	if _, err := keyPressEventsWithModifiers("Shift+a", 8); err == nil {
		t.Fatal("a chord redundantly pressing a held Shift key was accepted")
	}
}

func TestSelectionTextWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	text, err := sel.Text()
	if err != testErr {
		t.Errorf("Text() should return pre-existing error, got %v", err)
	}
	if text != "" {
		t.Errorf("Text() should return empty string on error, got %q", text)
	}
}

func TestSelectionAttrWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	val, err := sel.Attr("href")
	if err != testErr {
		t.Errorf("Attr() should return pre-existing error, got %v", err)
	}
	if val != "" {
		t.Errorf("Attr() should return empty string on error, got %q", val)
	}
}

func TestSelectionVisibleWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	vis, err := sel.Visible()
	if err != testErr {
		t.Errorf("Visible() should return pre-existing error, got %v", err)
	}
	if vis {
		t.Error("Visible() should return false on error")
	}
}

func TestSelectionScreenshotWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	data, err := sel.Screenshot()
	if err != testErr {
		t.Errorf("Screenshot() should return pre-existing error, got %v", err)
	}
	if data != nil {
		t.Error("Screenshot() should return nil data on error")
	}
}

func TestSelectionValueWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	val, err := sel.Value()
	if err != testErr {
		t.Errorf("Value() should return pre-existing error, got %v", err)
	}
	if val != "" {
		t.Errorf("Value() should return empty string on error, got %q", val)
	}
}

func TestSelectionWaitVisibleWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	result := sel.WaitVisible()
	if result != sel {
		t.Error("WaitVisible() should return same selection")
	}
	if result.Err() != testErr {
		t.Errorf("WaitVisible() should preserve error, got %v", result.Err())
	}
}

func TestSelectionWaitStableWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	result := sel.WaitStable()
	if result != sel {
		t.Error("WaitStable() should return same selection")
	}
	if result.Err() != testErr {
		t.Errorf("WaitStable() should preserve error, got %v", result.Err())
	}
}

func TestSelectionWaitEnabledWithPreexistingError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	result := sel.WaitEnabled()
	if result != sel {
		t.Error("WaitEnabled() should return same selection")
	}
	if result.Err() != testErr {
		t.Errorf("WaitEnabled() should preserve error, got %v", result.Err())
	}
}

func TestMustClickPanicsOnError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	defer func() {
		r := recover()
		if r == nil {
			t.Error("MustClick should panic on error")
		}
	}()
	sel.MustClick()
}

func TestMustInputPanicsOnError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	defer func() {
		r := recover()
		if r == nil {
			t.Error("MustInput should panic on error")
		}
	}()
	sel.MustInput("text")
}

func TestMustTextPanicsOnError(t *testing.T) {
	testErr := errors.New("element not found")
	sel := &Selection{err: testErr}

	defer func() {
		r := recover()
		if r == nil {
			t.Error("MustText should panic on error")
		}
	}()
	sel.MustText()
}
