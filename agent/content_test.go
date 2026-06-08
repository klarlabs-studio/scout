//go:build integration

package agent_test

import (
	"strings"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func TestMarkdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	md, err := s.Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	if len(md) == 0 {
		t.Fatal("empty markdown")
	}
	if !strings.Contains(md, "Hello Agent") {
		t.Error("markdown should contain page heading")
	}
	// Should be much shorter than raw HTML
	if len(md) > 10000 {
		t.Errorf("markdown too large: %d chars", len(md))
	}

	t.Logf("Markdown (%d chars):\n%s", len(md), md[:min(500, len(md))])
}

func TestReadableText(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	text, err := s.ReadableText()
	if err != nil {
		t.Fatalf("ReadableText: %v", err)
	}

	if len(text) == 0 {
		t.Fatal("empty readable text")
	}
	if !strings.Contains(text, "Hello Agent") {
		t.Error("should contain main content")
	}

	t.Logf("ReadableText (%d chars): %s", len(text), text[:min(300, len(text))])
}

func TestAccessibilityTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.Navigate(ts.URL)

	tree, err := s.AccessibilityTree()
	if err != nil {
		t.Fatalf("AccessibilityTree: %v", err)
	}

	if len(tree) == 0 {
		t.Fatal("empty accessibility tree")
	}
	if !strings.Contains(tree, "link") {
		t.Error("should contain link elements")
	}
	if !strings.Contains(tree, "input") {
		t.Error("should contain input elements")
	}
	if !strings.Contains(tree, "button") {
		t.Error("should contain button elements")
	}

	t.Logf("A11y tree (%d chars):\n%s", len(tree), tree[:min(800, len(tree))])
}

func TestContentLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping agent test in short mode")
	}
	ts := testServer()
	defer ts.Close()

	s := newSession(t)
	s.SetContentOptions(agent.ContentOptions{
		MaxLength:  500,
		MaxLinks:   1,
		MaxInputs:  1,
		MaxButtons: 1,
		MaxItems:   2,
		MaxRows:    1,
	})

	s.Navigate(ts.URL)

	obs, err := s.Observe()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if len(obs.Links) > 1 {
		t.Errorf("expected max 1 link, got %d", len(obs.Links))
	}
	if len(obs.Inputs) > 1 {
		t.Errorf("expected max 1 input, got %d", len(obs.Inputs))
	}
	if len(obs.Buttons) > 1 {
		t.Errorf("expected max 1 button, got %d", len(obs.Buttons))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
