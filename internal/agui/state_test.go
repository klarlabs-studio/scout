package agui

import (
	"testing"
)

func TestDiff_NoChange(t *testing.T) {
	a := &BrowserState{URL: "x", Title: "y", ReadyScore: 50}
	b := &BrowserState{URL: "x", Title: "y", ReadyScore: 50}
	if ops := Diff(a, b); len(ops) != 0 {
		t.Errorf("expected 0 ops for identical states, got %d: %+v", len(ops), ops)
	}
}

func TestDiff_URLChange(t *testing.T) {
	a := &BrowserState{URL: "old"}
	b := &BrowserState{URL: "new"}
	ops := Diff(a, b)
	if len(ops) != 1 || ops[0].Path != "/url" || ops[0].Value != "new" {
		t.Errorf("expected single /url replace, got %+v", ops)
	}
}

func TestDiff_ScreenshotEmptyNotPatched(t *testing.T) {
	a := &BrowserState{Screenshot: "abc"}
	b := &BrowserState{Screenshot: ""}
	ops := Diff(a, b)
	for _, op := range ops {
		if op.Path == "/screenshot" {
			t.Errorf("empty screenshot must not emit patch: %+v", ops)
		}
	}
}

func TestDiff_ElementsChange(t *testing.T) {
	a := &BrowserState{Elements: []ElementInfo{{Tag: "a"}}}
	b := &BrowserState{Elements: []ElementInfo{{Tag: "b"}}}
	found := false
	for _, op := range Diff(a, b) {
		if op.Path == "/elements" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /elements patch when element list differs")
	}
}

func TestDiff_AllFields(t *testing.T) {
	a := &BrowserState{}
	b := &BrowserState{URL: "u", Title: "t", ReadyScore: 80, ActiveTool: "click", TabCount: 2}
	got := Diff(a, b)
	if len(got) != 5 {
		t.Errorf("expected 5 ops, got %d: %+v", len(got), got)
	}
}
