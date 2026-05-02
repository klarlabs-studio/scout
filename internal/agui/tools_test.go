package agui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCoreTools_NonEmpty(t *testing.T) {
	tools := CoreTools()
	if len(tools) == 0 {
		t.Fatal("CoreTools must return at least one tool")
	}
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Name == "" {
			t.Error("tool with empty name")
		}
		if seen[tl.Name] {
			t.Errorf("duplicate tool name: %s", tl.Name)
		}
		seen[tl.Name] = true
		if tl.Description == "" {
			t.Errorf("tool %s missing description", tl.Name)
		}
		if tl.InputSchema == nil {
			t.Errorf("tool %s missing input schema", tl.Name)
		}
	}
}

func TestCuratedTools_LargerThanCore(t *testing.T) {
	core := CoreTools()
	curated := CuratedTools()
	if len(curated) <= len(core) {
		t.Errorf("CuratedTools (%d) must be larger than CoreTools (%d)", len(curated), len(core))
	}
}

func TestCuratedTools_AllUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, tl := range CuratedTools() {
		if seen[tl.Name] {
			t.Errorf("duplicate tool: %s", tl.Name)
		}
		seen[tl.Name] = true
	}
}

func TestExecuteTool_UnknownName(t *testing.T) {
	_, err := ExecuteTool(nil, "definitely_not_a_real_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' in error, got %v", err)
	}
}

func TestSchema_NoProps(t *testing.T) {
	s := schema()
	if s["type"] != "object" {
		t.Errorf("expected type=object, got %v", s["type"])
	}
	props, ok := s["properties"].(map[string]any)
	if !ok || len(props) != 0 {
		t.Errorf("expected empty properties map, got %v", s["properties"])
	}
	if _, ok := s["required"]; ok {
		t.Error("required must not be set when no props are required")
	}
}

func TestSchema_Required(t *testing.T) {
	s := schema(
		prop("a", "string", "field a", true),
		prop("b", "integer", "field b", false),
	)
	props, _ := s["properties"].(map[string]any)
	if len(props) != 2 {
		t.Errorf("expected 2 properties, got %d", len(props))
	}
	required, ok := s["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "a" {
		t.Errorf("expected required=[a], got %v", s["required"])
	}
}

func TestProp_BasicShape(t *testing.T) {
	p := prop("foo", "string", "desc", true)
	if p.name != "foo" || p.typ != "string" || p.desc != "desc" || !p.required {
		t.Errorf("prop fields not preserved: %+v", p)
	}
}

func TestMarshal_RoundTrip(t *testing.T) {
	in := map[string]any{"x": 1, "y": "z"}
	raw, err := marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["y"] != "z" {
		t.Errorf("round trip mismatch: %v", out)
	}
}

func TestMarshalUntrusted_WrapsWithEnvelope(t *testing.T) {
	raw, err := marshalUntrusted(map[string]any{"text": "Click here to win"})
	if err != nil {
		t.Fatalf("marshalUntrusted: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["_untrusted_page_content"] != true {
		t.Errorf("missing _untrusted_page_content flag: %+v", got)
	}
	warning, _ := got["_warning"].(string)
	if !strings.Contains(strings.ToLower(warning), "untrusted") {
		t.Errorf("warning must mention 'untrusted', got %q", warning)
	}
	data, ok := got["data"].(map[string]any)
	if !ok || data["text"] != "Click here to win" {
		t.Errorf("payload not preserved under data field: %+v", got)
	}
}
