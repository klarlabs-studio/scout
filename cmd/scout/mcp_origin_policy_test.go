package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseMCPAllowedOriginsRejectsExplicitEmptyEntries(t *testing.T) {
	for _, raw := range []string{"", "   ", "https://allowed.example,", ",https://allowed.example", "https://allowed.example,  ,https://other.example"} {
		t.Run(strings.ReplaceAll(raw, "/", "_"), func(t *testing.T) {
			if origins, err := parseMCPAllowedOrigins(raw); err == nil {
				t.Fatalf("parseMCPAllowedOrigins(%q) = %#v, nil error; want explicit empty entry rejected", raw, origins)
			}
		})
	}
}

func TestConfiguredMCPAllowedOriginsDistinguishesUnsetFromEmpty(t *testing.T) {
	old, wasSet := os.LookupEnv("SCOUT_MCP_ALLOWED_ORIGINS")
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", old)
		} else {
			_ = os.Unsetenv("SCOUT_MCP_ALLOWED_ORIGINS")
		}
	})
	_ = os.Unsetenv("SCOUT_MCP_ALLOWED_ORIGINS")
	if origins, configured, err := configuredMCPAllowedOrigins(); err != nil || configured || origins != nil {
		t.Fatalf("unset policy = (%#v, %t, %v), want (nil, false, nil)", origins, configured, err)
	}
	_ = os.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", "")
	if origins, configured, err := configuredMCPAllowedOrigins(); err == nil || !configured || origins != nil {
		t.Fatalf("empty configured policy = (%#v, %t, %v), want (nil, true, error)", origins, configured, err)
	}

	_ = os.Setenv("SCOUT_MCP_ALLOWED_ORIGINS", "https://allowed.example")
	origins, configured, err := configuredMCPAllowedOrigins()
	if err != nil || !configured || len(origins) != 1 || origins[0] != "https://allowed.example" {
		t.Fatalf("valid configured policy = (%#v, %t, %v), want the exact configured origin", origins, configured, err)
	}
}
