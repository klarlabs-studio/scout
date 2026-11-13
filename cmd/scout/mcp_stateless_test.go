package main

import (
	"net/http/httptest"
	"testing"
)

func TestMCPHTTPPrincipalAuthorizationIgnoresTransportSessionInStatelessMode(t *testing.T) {
	policies := map[string]mcpHTTPPrincipal{"alice": {Token: "alice-token"}}
	sessions := newMCPHTTPPrincipalSessions()
	request := httptest.NewRequest("POST", "http://scout.test/mcp", nil)
	request.Header.Set("Authorization", "Bearer alice-token")
	request.Header.Set("Mcp-Session-Id", "stale-stateful-session")

	principal, err := sessions.authorize(request, policies, true)
	if err != nil || principal != "alice" {
		t.Fatalf("stateless authorization = (%q, %v), want (alice, nil)", principal, err)
	}
	if _, bound := sessions.owners["stale-stateful-session"]; bound {
		t.Fatal("stateless transport header was unexpectedly bound as a stateful session")
	}

	if _, err := sessions.authorize(request, policies, false); err == nil {
		t.Fatal("stateful authorization accepted an unbound transport session ID")
	}
}
