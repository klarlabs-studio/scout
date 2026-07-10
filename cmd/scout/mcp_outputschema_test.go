package main

import (
	"testing"

	"go.klarlabs.de/mcp/schema"
	"go.klarlabs.de/scout/agent"
)

// TestOutputSchemasGenerate guards every type advertised via ToolBuilder.OutputSchema.
//
// OutputSchema runs schema.Generate at registration time; if it errors the
// ToolBuilder silently drops the tool. This test fails loudly instead, so a
// type that becomes unrepresentable (e.g. an added field with no JSON schema
// mapping) is caught in CI rather than silently removing a production tool.
func TestOutputSchemasGenerate(t *testing.T) {
	cases := []struct {
		name string
		val  any
	}{
		// Newly advertised data-tool output types.
		{"network_requests", NetworkRequestsResult{}},
		{"web_vitals", agent.WebVitalsResult{}},
		{"console_errors", agent.DiagnosticsResult{}},
		{"check_readiness", agent.PageReadiness{}},
		{"discover_form", agent.FormDiscoveryResult{}},
		{"failed_requests", FailedRequestsResult{}},
		{"list_tabs", ListTabsResult{}},
		{"cookies_list", CookiesListResult{}},
		// Pre-existing advertised types — kept here so the guard covers the
		// full set of production output schemas.
		{"observe", agent.Observation{}},
		{"submit_outcome", agent.SubmitOutcome{}},
		{"wait_for_navigation", agent.NavigationOutcome{}},
		{"aria_violations", agent.AriaViolationReport{}},
		{"network_summary", agent.NetworkSummary{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := schema.Generate(tc.val)
			if err != nil {
				t.Fatalf("schema.Generate(%T) failed: %v", tc.val, err)
			}
			if s == nil {
				t.Fatalf("schema.Generate(%T) returned nil schema", tc.val)
			}
		})
	}
}
