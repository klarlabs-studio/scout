package browse

import (
	"fmt"
	"runtime"
	"strings"
)

type keyDefinition struct {
	key, code string
	vk        int
	text      string
	shift     bool
}

var namedKeys = map[string]keyDefinition{
	"Enter": {"Enter", "Enter", 13, "\r", false}, "Tab": {"Tab", "Tab", 9, "\t", false},
	"Escape": {"Escape", "Escape", 27, "", false}, "Backspace": {"Backspace", "Backspace", 8, "", false},
	"Delete": {"Delete", "Delete", 46, "", false}, "Space": {" ", "Space", 32, " ", false},
	"ArrowUp": {"ArrowUp", "ArrowUp", 38, "", false}, "ArrowDown": {"ArrowDown", "ArrowDown", 40, "", false},
	"ArrowLeft": {"ArrowLeft", "ArrowLeft", 37, "", false}, "ArrowRight": {"ArrowRight", "ArrowRight", 39, "", false},
	"Home": {"Home", "Home", 36, "", false}, "End": {"End", "End", 35, "", false},
	"PageUp": {"PageUp", "PageUp", 33, "", false}, "PageDown": {"PageDown", "PageDown", 34, "", false},
	"Insert": {"Insert", "Insert", 45, "", false},
}

var shiftedKeyText = map[byte]string{'!': "1", '@': "2", '#': "3", '$': "4", '%': "5", '^': "6", '&': "7", '*': "8", '(': "9", ')': "0", '_': "-", '+': "=", '{': "[", '}': "]", '|': "\\", ':': ";", '"': "'", '<': ",", '>': ".", '?': "/", '~': "`"}

func resolveKey(name string) (keyDefinition, error) {
	if key, ok := namedKeys[name]; ok {
		return key, nil
	}
	if len(name) == 1 {
		c := name[0]
		if c >= 'a' && c <= 'z' {
			return keyDefinition{name, "Key" + strings.ToUpper(name), int(c - 'a' + 65), name, false}, nil
		}
		if c >= 'A' && c <= 'Z' {
			return keyDefinition{name, "Key" + strings.ToUpper(name), int(c), name, true}, nil
		}
		if c >= '0' && c <= '9' {
			return keyDefinition{name, "Digit" + name, int(c), name, false}, nil
		}
		if unshifted, ok := shiftedKeyText[c]; ok {
			base, _ := resolveKey(unshifted)
			return keyDefinition{name, base.code, base.vk, name, true}, nil
		}
		codes := map[byte]string{'-': "Minus", '=': "Equal", '[': "BracketLeft", ']': "BracketRight", '\\': "Backslash", ';': "Semicolon", '\'': "Quote", ',': "Comma", '.': "Period", '/': "Slash", '`': "Backquote"}
		if code, ok := codes[c]; ok {
			return keyDefinition{name, code, int(c), name, false}, nil
		}
	}
	if strings.HasPrefix(name, "F") {
		var n int
		if _, err := fmt.Sscanf(name, "F%d", &n); err == nil && n >= 1 && n <= 12 {
			return keyDefinition{name, name, 111 + n, "", false}, nil
		}
	}
	return keyDefinition{}, fmt.Errorf("unsupported key %q", name)
}

// keyPressEvents translates a key name or modifier chord into CDP key events.

func keyPressEventsWithModifiers(chord string, initialModifiers int) ([]map[string]any, error) {
	parts := strings.Split(chord, "+")
	if len(parts) == 0 || len(parts) > 4 {
		return nil, fmt.Errorf("invalid key chord %q", chord)
	}
	mods := map[string]keyDefinition{"Control": {"Control", "ControlLeft", 17, "", false}, "Shift": {"Shift", "ShiftLeft", 16, "", false}, "Alt": {"Alt", "AltLeft", 18, "", false}, "Meta": {"Meta", "MetaLeft", 91, "", false}}
	var down []keyDefinition
	modifierMask := initialModifiers
	seen := map[string]bool{}
	for _, part := range parts[:len(parts)-1] {
		mod, ok := mods[part]
		if !ok {
			return nil, fmt.Errorf("unsupported modifier %q in key chord", part)
		}
		if seen[part] {
			return nil, fmt.Errorf("duplicate modifier %q in key chord", part)
		}
		bit := map[string]int{"Alt": 1, "Control": 2, "Meta": 4, "Shift": 8}[part]
		if modifierMask&bit != 0 {
			return nil, fmt.Errorf("modifier %q is already held", part)
		}
		seen[part] = true
		down = append(down, mod)
		switch part {
		case "Alt":
			modifierMask |= 1
		case "Control":
			modifierMask |= 2
		case "Meta":
			modifierMask |= 4
		case "Shift":
			modifierMask |= 8
		}
	}
	key, err := resolveKey(parts[len(parts)-1])
	if err != nil {
		return nil, err
	}
	// Uppercase letters conventionally name the physical letter in a Ctrl/Meta
	// chord (Ctrl+A means select all). Make the shifted meaning explicit with
	// Shift+A so uppercase chord spelling does not silently add Shift.
	if key.shift && modifierMask&8 == 0 && modifierMask&7 != 0 && len(key.key) == 1 && key.key[0] >= 'A' && key.key[0] <= 'Z' {
		key.key = strings.ToLower(key.key)
		key.text = key.key
		key.shift = false
	}
	params := func(t string, k keyDefinition, modifier int) map[string]any {
		if modifier&8 != 0 && !k.shift && len(k.key) == 1 {
			if k.key[0] >= 'a' && k.key[0] <= 'z' {
				k.key = strings.ToUpper(k.key)
				k.vk = int(k.key[0])
				k.text = k.key
				k.shift = true
			} else {
				for shifted, base := range shiftedKeyText {
					if base == k.key {
						k.key, k.text, k.shift = string(shifted), string(shifted), true
						break
					}
				}
			}
		}
		p := map[string]any{"type": t, "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk, "modifiers": modifier}
		// Synthetic Meta+A reaches the page as a KeyboardEvent on macOS, but
		// Chromium does not route it through AppKit's native key-equivalent
		// handling. Supply the equivalent editing command so the browser applies
		// the standard Select All behavior to editable content.
		if t == "keyDown" && runtime.GOOS == "darwin" && modifier == 4 && k.code == "KeyA" {
			p["commands"] = []string{"SelectAll"}
		}
		if t == "keyDown" && k.text != "" && modifier&7 == 0 {
			p["text"] = k.text
			p["unmodifiedText"] = k.text
		}
		if t == "keyDown" && k.shift && modifier&8 == 0 {
			p["modifiers"] = modifier | 8
		}
		if t == "keyDown" && k.shift && k.text != "" {
			p["text"] = k.text
			p["unmodifiedText"] = k.text
			if len(k.key) == 1 && k.key >= "A" && k.key <= "Z" {
				p["unmodifiedText"] = strings.ToLower(k.key)
			} else if len(k.key) == 1 {
				base, ok := shiftedKeyText[k.key[0]]
				if ok {
					p["unmodifiedText"] = base
				}
			}
		}
		return p
	}
	var events []map[string]any
	currentMask := initialModifiers
	for _, mod := range down {
		events = append(events, params("keyDown", mod, currentMask))
		switch mod.key {
		case "Alt":
			currentMask |= 1
		case "Control":
			currentMask |= 2
		case "Meta":
			currentMask |= 4
		case "Shift":
			currentMask |= 8
		}
	}
	events = append(events, params("keyDown", key, currentMask), params("keyUp", key, currentMask))
	for i := len(down) - 1; i >= 0; i-- {
		events = append(events, params("keyUp", down[i], currentMask))
		switch down[i].key {
		case "Alt":
			currentMask &^= 1
		case "Control":
			currentMask &^= 2
		case "Meta":
			currentMask &^= 4
		case "Shift":
			currentMask &^= 8
		}
	}
	return events, nil
}

// keyPressEvents translates a key name or modifier chord into CDP key events.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func keyPressEvents(chord string) ([]map[string]any, error) {
	return keyPressEventsWithModifiers(chord, 0)
}
