package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWebMCPBuildCatalogReportsCompleteCatalog(t *testing.T) {
	got := webMCPBuildCatalog("target", "https://example.test/", []webMCPToolWire{{
		Name: "lookup", Description: "Read record", FrameID: "frame",
		InputSchema: map[string]any{"type": "object"},
	}}, map[string]string{"frame": "https://example.test"}, nil)

	if !got.Complete || got.Truncated || got.OmittedTools != 0 || len(got.Tools) != 1 {
		t.Fatalf("catalog completeness = complete:%v truncated:%v omitted:%d tools:%d", got.Complete, got.Truncated, got.OmittedTools, len(got.Tools))
	}
	if got.Tools[0].Origin != "https://example.test" || got.Tools[0].ContentTrust != ContentTrustUntrustedPage {
		t.Fatalf("catalog tool provenance = %+v", got.Tools[0])
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxWebMCPCatalogBytes {
		t.Fatalf("catalog size = %d, limit = %d", len(encoded), maxWebMCPCatalogBytes)
	}
}

func TestWebMCPBuildCatalogBoundsAggregateMetadata(t *testing.T) {
	largeValue := strings.Repeat("x", 54<<10)
	tools := make([]webMCPToolWire, 4)
	for i := range tools {
		tools[i] = webMCPToolWire{
			Name:        "tool_" + string(rune('a'+i)),
			Description: "Fixture tool",
			FrameID:     "frame",
			InputSchema: map[string]any{"type": "object", "description": largeValue},
		}
	}

	got := webMCPBuildCatalog("target", "https://example.test/", tools, map[string]string{"frame": "https://example.test"}, nil)
	if !got.Truncated || got.OmittedTools != 1 {
		t.Fatalf("catalog omission metadata = truncated:%v omitted:%d", got.Truncated, got.OmittedTools)
	}
	if len(got.Tools) != 3 {
		t.Fatalf("retained tools = %d, want 3", len(got.Tools))
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxWebMCPCatalogBytes {
		t.Fatalf("catalog size = %d, limit = %d", len(encoded), maxWebMCPCatalogBytes)
	}
}

func TestWebMCPBuildCatalogCountsOversizedToolAsOmitted(t *testing.T) {
	got := webMCPBuildCatalog("target", "https://example.test/", []webMCPToolWire{{
		Name: "oversized", FrameID: "frame",
		InputSchema: map[string]any{"description": strings.Repeat("x", maxWebMCPToolBytes)},
	}}, nil, nil)
	if !got.Truncated || got.OmittedTools != 1 || len(got.Tools) != 0 {
		t.Fatalf("oversized tool handling = truncated:%v omitted:%d tools:%d", got.Truncated, got.OmittedTools, len(got.Tools))
	}
}

func TestWebMCPBuildCatalogSeparatesCoverageFromOutputTruncation(t *testing.T) {
	got := webMCPBuildCatalog("target", "https://example.test/", nil, nil, []string{"site_isolated_iframe_tools_not_enumerated"})
	if got.Complete || got.Truncated || got.OmittedTools != 0 || len(got.CoverageWarnings) != 1 {
		t.Fatalf("coverage metadata = complete:%v truncated:%v omitted:%d warnings:%v", got.Complete, got.Truncated, got.OmittedTools, got.CoverageWarnings)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxWebMCPCatalogBytes {
		t.Fatalf("catalog size = %d, limit = %d", len(encoded), maxWebMCPCatalogBytes)
	}
}

func TestWebMCPBuildCatalogBoundsAndMarksPageURL(t *testing.T) {
	longURL := "https://example.test/" + strings.Repeat("界", maxWebMCPURLBytes)
	got := webMCPBuildCatalog("target", longURL, nil, nil, nil)
	if !got.URLTruncated || len(got.URL) > maxWebMCPURLBytes {
		t.Fatalf("URL truncation = marked:%v bytes:%d", got.URLTruncated, len(got.URL))
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxWebMCPCatalogBytes {
		t.Fatalf("catalog size = %d, limit = %d", len(encoded), maxWebMCPCatalogBytes)
	}
}
