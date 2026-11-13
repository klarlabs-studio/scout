package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcpserver "go.klarlabs.de/mcp"
	"go.klarlabs.de/scout/agent"
)

func TestPressKeyIsExposedOnInteractionSurfaces(t *testing.T) {
	for _, surface := range []map[string]struct{}{mcpAllToolNames, mcpToolProfiles["core"], mcpToolProfiles["interact"], mcpCuratedToolNames} {
		if _, ok := surface["press_key"]; !ok {
			t.Error("press_key is missing from an interaction surface")
		}
		for _, name := range []string{"key_down", "key_up"} {
			if _, ok := surface[name]; !ok {
				t.Errorf("%s is missing from an interaction surface", name)
			}
		}
	}
}

func TestMCPErr_NilPassthrough(t *testing.T) {
	if err := mcpErr(nil); err != nil {
		t.Errorf("nil input must return nil, got %v", err)
	}
}

func TestDurableSessionRecoveryToolsAreAdminOnly(t *testing.T) {
	for _, name := range []string{"persist_session", "resume_session", "revoke_session"} {
		if _, ok := mcpAllToolNames[name]; !ok {
			t.Errorf("%s is missing from the complete tool catalog", name)
		}
		if _, ok := mcpToolProfiles["admin"][name]; !ok {
			t.Errorf("%s is missing from the admin profile", name)
		}
		for _, profile := range []string{"core", "read", "interact", "diagnose"} {
			if _, ok := mcpToolProfiles[profile][name]; ok {
				t.Errorf("%s unexpectedly appears in the %s profile", name, profile)
			}
		}
	}
}

func TestTaskRecoveryViewAvailableAcrossProfiles(t *testing.T) {
	if _, ok := mcpAllToolNames["recover_task_run"]; !ok {
		t.Fatal("recover_task_run is missing from the complete tool catalog")
	}
	for _, profile := range []string{"read", "interact", "diagnose", "admin"} {
		if _, ok := mcpToolProfiles[profile]["recover_task_run"]; !ok {
			t.Errorf("recover_task_run is missing from the %s profile", profile)
		}
	}
	for _, name := range []string{"recover_task_run", "resolve_task_run"} {
		if _, ok := mcpToolProfiles["core"][name]; !ok {
			t.Errorf("%s is missing from the core workflow profile", name)
		}
	}
	if err := validateTaskRunToolRequirements(context.Background(), []string{"recover_task_run", "resolve_task_run"}, func(_ context.Context, name string) bool {
		_, ok := mcpToolProfiles["core"][name]
		return ok
	}); err != nil {
		t.Fatalf("core recovery requirements were not satisfied: %v", err)
	}
	if err := validateTaskRunToolRequirements(context.Background(), []string{"recover_task_run", "resolve_task_run"}, func(context.Context, string) bool { return false }); err == nil {
		t.Fatal("restricted profile without recovery tools was accepted")
	}
	if err := validateTaskRunToolRequirements(context.Background(), nil, func(_ context.Context, name string) bool {
		return name == "task_run"
	}); err == nil || !strings.Contains(err.Error(), "SCOUT_TASK_CAPABILITY_MISSING") {
		t.Fatalf("task_run profile without implicit recovery tools error = %v, want capability missing", err)
	}
	if err := validateTaskRunToolRequirements(context.Background(), nil, func(_ context.Context, name string) bool {
		_, ok := mcpToolProfiles["core"][name]
		return ok
	}); err != nil {
		t.Fatalf("core profile should satisfy implicit recovery tools: %v", err)
	}
}

func TestTaskRecoveryViewCLIReadOnly(t *testing.T) {
	_, err := executeSessionRequest(&agent.Session{}, sessionRequest{
		Command: "recover_task_run", TaskID: "task-1", ResolutionStatus: "verified_complete",
		ResolutionEvidence: "ledger confirms one effect",
	})
	if err == nil || !strings.Contains(err.Error(), "is read-only") {
		t.Fatalf("recover_task_run error = %v, want explicit read-only guidance", err)
	}
}

func TestSwitchToFrameDescriptionMatchesSupportedOOPIFBehavior(t *testing.T) {
	for _, expected := range []string{
		"site-isolated out-of-process iframes (OOPIFs)",
		"one unambiguous child target",
		"session URL policy",
		"fail closed",
	} {
		if !strings.Contains(mcpSwitchToFrameDescription, expected) {
			t.Errorf("switch_to_frame description is missing %q", expected)
		}
	}
	if strings.Contains(mcpSwitchToFrameDescription, "not selectable yet") {
		t.Fatal("switch_to_frame description incorrectly says OOPIFs are unsupported")
	}
}

func TestMCPErr_PlainErrorPassthrough(t *testing.T) {
	in := errors.New("boom")
	err := mcpErr(in)
	toolErr, ok := err.(*mcpserver.ToolInputError)
	if !ok {
		t.Fatalf("plain tool error should use MCP tool-execution error type, got %T (%v)", err, err)
	}
	if toolErr.Message != in.Error() {
		t.Errorf("plain tool error message = %q, want %q", toolErr.Message, in.Error())
	}
}

func TestMCPErr_OperationError_WrapsAsEnvelope(t *testing.T) {
	op := &agent.OperationError{
		Phase:         "navigate",
		Cause:         "timeout",
		URL:           "https://example.com",
		StatusCode:    0,
		Detail:        "context deadline",
		OriginalError: "deadline exceeded",
	}
	wrapped := mcpErr(op)
	if wrapped == nil {
		t.Fatal("expected non-nil wrapped error")
	}
	msg := wrapped.Error()
	if !strings.HasPrefix(msg, "SCOUT_ERROR ") {
		t.Errorf("expected SCOUT_ERROR prefix, got %q", msg)
	}
	jsonPart := strings.TrimPrefix(msg, "SCOUT_ERROR ")
	var env MCPErrorEnvelope
	if err := json.Unmarshal([]byte(jsonPart), &env); err != nil {
		t.Fatalf("envelope must be valid JSON: %v", err)
	}
	if env.Code != "SCOUT_OPERATION_ERROR" {
		t.Errorf("Code = %q, want SCOUT_OPERATION_ERROR", env.Code)
	}
	if env.Phase != "navigate" {
		t.Errorf("Phase mismatch: %q", env.Phase)
	}
	if env.Hint == "" {
		t.Error("timeout cause must produce a hint")
	}
}

func TestMCPErr_HintsByCause(t *testing.T) {
	cases := map[string]string{
		"timeout":            "reset",
		"connection_refused": "CDP",
		"http_401":           "Authentication",
		"http_403":           "Authentication",
		"http_404":           "not found",
		"browser_closed":     "reset",
	}
	for cause, mustContain := range cases {
		op := &agent.OperationError{Phase: "p", Cause: cause, OriginalError: "x"}
		wrapped := mcpErr(op)
		jsonPart := strings.TrimPrefix(wrapped.Error(), "SCOUT_ERROR ")
		var env MCPErrorEnvelope
		_ = json.Unmarshal([]byte(jsonPart), &env)
		if !strings.Contains(strings.ToLower(env.Hint), strings.ToLower(mustContain)) {
			t.Errorf("cause %q hint %q must contain %q", cause, env.Hint, mustContain)
		}
	}
}

func TestMCPErr_UnknownCause_NoHint(t *testing.T) {
	op := &agent.OperationError{Phase: "p", Cause: "weird_unknown_cause", OriginalError: "x"}
	wrapped := mcpErr(op)
	jsonPart := strings.TrimPrefix(wrapped.Error(), "SCOUT_ERROR ")
	var env MCPErrorEnvelope
	_ = json.Unmarshal([]byte(jsonPart), &env)
	if env.Hint != "" {
		t.Errorf("unknown cause must yield empty hint, got %q", env.Hint)
	}
}

func TestConfigureInput_JSONUnmarshal(t *testing.T) {
	var in ConfigureInput
	if err := json.Unmarshal([]byte(`{"headless": false, "allow_private_ips": true}`), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Headless == nil || *in.Headless {
		t.Errorf("headless should parse to explicit false, got %v", in.Headless)
	}
	if in.AllowPrivateIPs == nil || !*in.AllowPrivateIPs {
		t.Errorf("allow_private_ips should parse to explicit true, got %v", in.AllowPrivateIPs)
	}

	// Omitted fields decode to nil ("keep current"), not a zero-value false that
	// would silently switch headless off.
	var empty ConfigureInput
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if empty.Headless != nil || empty.AllowPrivateIPs != nil {
		t.Errorf("omitted fields should be nil, got headless=%v allow=%v", empty.Headless, empty.AllowPrivateIPs)
	}
}

func TestParseMCPCallSessionSelector(t *testing.T) {
	tests := []struct {
		name    string
		meta    string
		tool    string
		want    string
		wantErr string
	}{
		{name: "omitted", tool: "navigate"},
		{name: "unrelated metadata", meta: `{"traceparent":"00-abc-def-01"}`, tool: "navigate"},
		{name: "valid browser selector", meta: `{"go.klarlabs.de/scout/session":"checkout"}`, tool: "navigate", want: "checkout"},
		{name: "valid opaque session selector", meta: `{"go.klarlabs.de/scout/session":"scs:AAECAwQFBgcICQoLDA0ODxAREhMUFRYX"}`, tool: "navigate", want: "scs:AAECAwQFBgcICQoLDA0ODxAREhMUFRYX"},
		{name: "status is session addressable", meta: `{"go.klarlabs.de/scout/session":"checkout"}`, tool: "status", want: "checkout"},
		{name: "malformed metadata", meta: `[]`, tool: "navigate", wantErr: "invalid MCP _meta"},
		{name: "selector must be string", meta: `{"go.klarlabs.de/scout/session":7}`, tool: "navigate", wantErr: "expected a session name string"},
		{name: "invalid session name", meta: `{"go.klarlabs.de/scout/session":"../other"}`, tool: "navigate", wantErr: "invalid session name"},
		{name: "malformed opaque session ID", meta: `{"go.klarlabs.de/scout/session":"scs:short"}`, tool: "navigate", wantErr: "malformed opaque session ID"},
		{name: "owner-level operation rejects selector", meta: `{"go.klarlabs.de/scout/session":"checkout"}`, tool: "configure", wantErr: "owner-level tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meta json.RawMessage
			if tt.meta != "" {
				meta = json.RawMessage(tt.meta)
			}
			got, err := parseMCPCallSessionSelector(meta, tt.tool)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMCPCallSessionSelector: %v", err)
			}
			if got != tt.want {
				t.Fatalf("selector = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewMCPOpaqueSessionID(t *testing.T) {
	first, err := newMCPOpaqueSessionID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newMCPOpaqueSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !validMCPOpaqueSessionID(first) || !validMCPOpaqueSessionID(second) {
		t.Fatalf("generated invalid session IDs: %q %q", first, second)
	}
	if first == second {
		t.Fatal("generated duplicate opaque session IDs")
	}
}

func TestResolveMCPSessionSelectorScopesOpaqueIDsToOwner(t *testing.T) {
	id, err := newMCPOpaqueSessionID()
	if err != nil {
		t.Fatal(err)
	}
	registry := map[string]string{id: "http/alice::transport-1/checkout"}
	ownerKey := func(owner, name string) string { return "http/" + owner + "/" + name }
	for _, tc := range []struct {
		name      string
		owner     string
		wantKey   string
		wantError bool
	}{
		{name: "owning transport", owner: "alice::transport-1", wantKey: "http/alice::transport-1/checkout"},
		{name: "different transport", owner: "alice::transport-2", wantError: true},
		{name: "different principal", owner: "mallory::transport-1", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMCPSessionSelector(id, registry,
				func(name string) string { return ownerKey(tc.owner, name) },
				func(key string) bool { return strings.HasPrefix(key, "http/"+tc.owner+"/") })
			if tc.wantError {
				if err == nil || err.Error() != "session does not exist for this client" {
					t.Fatalf("foreign ID error = %v", err)
				}
				return
			}
			if err != nil || got != tc.wantKey {
				t.Fatalf("resolve = %q, %v; want %q", got, err, tc.wantKey)
			}
		})
	}
}

func TestMCPHTTPClientScopeIgnoresTransportSessionInStatelessMode(t *testing.T) {
	for _, transportID := range []string{"", "stale-session", "caller-selected-session"} {
		if got, want := mcpHTTPClientScope("alice", transportID, true), "alice::stateless"; got != want {
			t.Errorf("stateless scope with transport ID %q = %q, want %q", transportID, got, want)
		}
	}
	if got, want := mcpHTTPClientScope("bob", "stale-session", true), "bob::stateless"; got != want {
		t.Errorf("other principal stateless scope = %q, want %q", got, want)
	}
	if got, want := mcpHTTPClientScope("alice", "transport-1", false), "alice::transport-1"; got != want {
		t.Errorf("stateful scope = %q, want %q", got, want)
	}
	if got, want := mcpHTTPClientScope("", "stdio-client", false), "stdio-client"; got != want {
		t.Errorf("non-HTTP scope = %q, want %q", got, want)
	}
}

func TestLocalNamedSessionUsesPrincipalOrProcessProxy(t *testing.T) {
	base := agent.SessionConfig{RemoteCDP: "https://browser.example/cdp", ProxyServer: ""}
	policies := map[string]mcpHTTPPrincipal{
		"alice": {ProxyServer: "http://alice-proxy.example:8080"},
		"bob":   {},
	}

	alice := localNamedSessionConfig(base, "alice", policies, "http://shared-proxy.example:8080")
	if alice.RemoteCDP != "" || alice.ProxyServer != policies["alice"].ProxyServer {
		t.Fatalf("Alice named session config = %+v, want local browser and principal proxy", alice)
	}
	bob := localNamedSessionConfig(base, "bob", policies, "http://shared-proxy.example:8080")
	if bob.RemoteCDP != "" || bob.ProxyServer != "http://shared-proxy.example:8080" {
		t.Fatalf("Bob named session config = %+v, want local browser and process proxy fallback", bob)
	}
	stdio := localNamedSessionConfig(agent.SessionConfig{ProxyServer: "http://stdio-proxy.example:8080"}, "", nil, "http://unused.example:8080")
	if stdio.RemoteCDP != "" || stdio.ProxyServer != "http://stdio-proxy.example:8080" {
		t.Fatalf("stdio named session config = %+v, want inherited process config", stdio)
	}
}

func TestLoadMCPHTTPPrincipalPoliciesValidatesProxyServer(t *testing.T) {
	base := `{"alice":{"token":"secret","remote_cdp":"https://browser.example/cdp","allowed_origins":["https://app.example"]}}`
	for _, proxy := range []string{"http://user:pass@proxy.example:8080", "http://proxy.example:8080/path"} {
		var policy map[string]map[string]any
		if err := json.Unmarshal([]byte(base), &policy); err != nil {
			t.Fatalf("invalid test JSON: %v", err)
		}
		policy["alice"]["proxy_server"] = proxy
		encoded, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadMCPHTTPPrincipalPolicies(string(encoded)); err == nil || !strings.Contains(err.Error(), "invalid proxy_server") {
			t.Fatalf("proxy %q error = %v, want invalid proxy_server", proxy, err)
		}
	}

	valid := `{"alice":{"token":"secret","remote_cdp":"https://browser.example/cdp","allowed_origins":["https://app.example"],"proxy_server":"socks5://proxy.example:1080"}}`
	policies, err := loadMCPHTTPPrincipalPolicies(valid)
	if err != nil {
		t.Fatalf("valid principal proxy rejected: %v", err)
	}
	if got := policies["alice"].ProxyServer; got != "socks5://proxy.example:1080" {
		t.Fatalf("proxy_server = %q, want socks5 proxy", got)
	}
	remoteDefault := agent.SessionConfig{RemoteCDP: policies["alice"].RemoteCDP}
	if remoteDefault.RemoteCDP == "" || remoteDefault.ProxyServer != "" {
		t.Fatalf("principal remote default config = %+v, want remote CDP without an applied local proxy", remoteDefault)
	}
	localNamed := localNamedSessionConfig(remoteDefault, "alice", policies, "")
	if localNamed.RemoteCDP != "" || localNamed.ProxyServer != policies["alice"].ProxyServer {
		t.Fatalf("principal local named config = %+v, want local browser with principal proxy", localNamed)
	}
}

func TestLoadMCPHTTPPrincipalPoliciesRejectsUnknownFields(t *testing.T) {
	base := `{"alice":{"token":"secret","remote_cdp":"https://browser.example/cdp","allowed_origins":["https://app.example"]}}`
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "unknown principal field",
			raw:  strings.Replace(base, `"token"`, `"tokne"`, 1),
			want: "unknown field \"tokne\"",
		},
		{
			name: "misspelled isolation field",
			raw:  strings.Replace(base, `"allowed_origins"`, `"isolate_browser_contex"`, 1),
			want: "unknown field",
		},
		{
			name: "multiple JSON values",
			raw:  base + ` {}`,
			want: "expected one JSON value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadMCPHTTPPrincipalPolicies(tc.raw); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadMCPHTTPPrincipalPolicies() error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadMCPHTTPPrincipalPoliciesDetectsEquivalentRemoteEndpoints(t *testing.T) {
	base := `{"alice":{"token":"alice-secret","remote_cdp":"https://browser.example:443/cdp/","allowed_origins":["https://app.example"]},"bob":{"token":"bob-secret","remote_cdp":"https://BROWSER.EXAMPLE/cdp","allowed_origins":["https://app.example"]}}`
	if _, err := loadMCPHTTPPrincipalPolicies(base); err == nil || !strings.Contains(err.Error(), "share a remote_cdp endpoint") {
		t.Fatalf("equivalent shared endpoint error = %v, want isolation requirement", err)
	}
	isolated := strings.Replace(base, `,"allowed_origins"`, `,"isolate_browser_context":true,"allowed_origins"`, -1)
	policies, err := loadMCPHTTPPrincipalPolicies(isolated)
	if err != nil {
		t.Fatalf("equivalent shared endpoint with both contexts isolated rejected: %v", err)
	}
	if !policies["alice"].IsolateBrowserContext || !policies["bob"].IsolateBrowserContext {
		t.Fatalf("isolated policies = %+v, want both BrowserContexts isolated", policies)
	}
}

func TestMCPArtifactOwnerScopesArtifactsToSession(t *testing.T) {
	if got, want := mcpArtifactOwner("alice::transport-1", "http/alice::transport-1/checkout"), "owner/alice::transport-1/session/http/alice::transport-1/checkout"; got != want {
		t.Fatalf("session artifact owner = %q, want %q", got, want)
	}
	if got, want := mcpArtifactOwner("", "stdio/default"), "owner//session/stdio/default"; got != want {
		t.Fatalf("stdio session artifact owner = %q, want %q", got, want)
	}
}

func TestMCPSessionVisibilityAllowsSameAuthenticatedPrincipalAfterReconnect(t *testing.T) {
	key := "http/acme::transport-old/checkout"
	for _, tc := range []struct {
		name      string
		clientID  string
		principal string
		want      bool
	}{
		{name: "original transport", clientID: "acme::transport-old", principal: "acme", want: true},
		{name: "reconnected transport", clientID: "acme::transport-new", principal: "acme", want: true},
		{name: "different principal", clientID: "other::transport-new", principal: "other", want: false},
		{name: "unauthenticated transport", clientID: "transport-new", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpSessionKeyVisibleToOwner(key, tc.clientID, tc.principal); got != tc.want {
				t.Fatalf("visible = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestMCPSessionOwnerScope(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"default", "stdio_process"},
		{"http/transport-1/default", "transport"},
		{"http/acme::transport-1/default", "authenticated_principal"},
	} {
		if got := mcpSessionOwnerScope(tc.key); got != tc.want {
			t.Errorf("mcpSessionOwnerScope(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestCleanupPrivateArtifactsForOwner(t *testing.T) {
	dir := t.TempDir()
	ownedPath := filepath.Join(dir, "owned.json")
	otherPath := filepath.Join(dir, "other.json")
	if err := os.WriteFile(ownedPath, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	owned, other := mcpArtifactOwner("client", "session-a"), mcpArtifactOwner("client", "session-b")
	registered := map[string][32]byte{ownedPath: {}, otherPath: {}}
	owners := map[string]string{ownedPath: owned, otherPath: other}
	sizes := map[string]int64{ownedPath: 5, otherPath: 5}
	totalBytes := int64(10)
	ownerBytes := map[string]int64{owned: 5, other: 5}
	ownerCounts := map[string]int{owned: 1, other: 1}
	mu := &sync.Mutex{}

	removedBytes, removedCount := cleanupPrivateArtifactsForOwner(mu, registered, owners, sizes, &totalBytes, ownerBytes, ownerCounts, owned)
	if removedBytes != 5 || removedCount != 1 {
		t.Fatalf("removed quota = (%d bytes, %d artifacts), want (5, 1)", removedBytes, removedCount)
	}
	if _, err := os.Stat(ownedPath); !os.IsNotExist(err) {
		t.Fatalf("owned artifact still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Fatalf("other session artifact was removed: %v", err)
	}
	if totalBytes != 5 || ownerBytes[other] != 5 || ownerCounts[other] != 1 {
		t.Fatalf("remaining quota = total %d, other %d/%d; want 5, 5/1", totalBytes, ownerBytes[other], ownerCounts[other])
	}
	if _, ok := registered[ownedPath]; ok {
		t.Fatal("owned artifact remains registered")
	}
	if _, ok := registered[otherPath]; !ok {
		t.Fatal("other session artifact was unregistered")
	}
}

func TestStartScreenRecordingInput_Defaults(t *testing.T) {
	var in StartScreenRecordingInput
	if err := json.Unmarshal([]byte(`{}`), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Width != 0 || in.Height != 0 || in.FPS != 0 {
		t.Errorf("zero-valued fields expected, got %+v", in)
	}
}

func TestStartScreenRecordingInput_FullParse(t *testing.T) {
	raw := `{"width":1920,"height":1080,"fps":24,"quality":75,"format":"mp4","output_dir":"/tmp/x"}`
	var in StartScreenRecordingInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Width != 1920 || in.Height != 1080 || in.FPS != 24 || in.Quality != 75 ||
		in.Format != "mp4" || in.OutputDir != "/tmp/x" {
		t.Errorf("field parse mismatch: %+v", in)
	}
}

func TestResolveViewport(t *testing.T) {
	tests := []struct {
		name                string
		in                  SetViewportInput
		wantW, wantH        int
		wantScale           float64
		wantMobile, wantErr bool
	}{
		{"explicit", SetViewportInput{Width: 390, Height: 844}, 390, 844, 1, false, false},
		{"explicit mobile + scale", SetViewportInput{Width: 360, Height: 800, DeviceScaleFactor: 3, Mobile: true}, 360, 800, 3, true, false},
		{"preset iphone-14", SetViewportInput{Device: "iphone-14"}, 390, 844, 3, true, false},
		{"preset case-insensitive", SetViewportInput{Device: " IPhone-SE "}, 375, 667, 2, true, false},
		{"preset desktop", SetViewportInput{Device: "desktop"}, 1280, 800, 1, false, false},
		{"explicit overrides preset", SetViewportInput{Device: "iphone-14", Width: 320}, 320, 844, 3, true, false},
		{"unknown preset", SetViewportInput{Device: "nope"}, 0, 0, 0, false, true},
		{"missing dims", SetViewportInput{}, 0, 0, 0, false, true},
		{"negative width", SetViewportInput{Width: -1, Height: 800}, 0, 0, 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h, scale, mobile, err := resolveViewport(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if w != tt.wantW || h != tt.wantH || scale != tt.wantScale || mobile != tt.wantMobile {
				t.Errorf("got (%d,%d,%g,%v), want (%d,%d,%g,%v)", w, h, scale, mobile, tt.wantW, tt.wantH, tt.wantScale, tt.wantMobile)
			}
		})
	}
}

// #79: a capture meant for the human has to become a file. base64 renders for
// the model and never reaches the user, so returning it was the whole problem.
func TestWriteCapture_GeneratesPathWhenNoneGiven(t *testing.T) {
	got, err := writeCapture("", "screenshot", "png", []byte("\x89PNG-ish"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(got) })

	if !filepath.IsAbs(got) {
		t.Errorf("returned path should be absolute, got %q", got)
	}
	if filepath.Ext(got) != ".png" {
		t.Errorf("extension should follow the format, got %q", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("file was not written: %v", err)
	}
	data, _ := os.ReadFile(got)
	if string(data) != "\x89PNG-ish" {
		t.Errorf("bytes were altered on the way to disk: %q", data)
	}
}

// A relative path resolves under the temp dir, matching output_dir on
// start_screen_recording — the existing precedent for this in scout.
func TestWriteCapture_RelativeResolvesUnderTemp(t *testing.T) {
	got, err := writeCapture(filepath.Join("scout-test-rel", "shot.png"), "screenshot", "png", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(os.TempDir(), "scout-test-rel")) })

	if !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("relative path should resolve under the temp dir, got %q", got)
	}
}

// An explicit path into a directory that does not exist yet must work; failing
// on a missing parent the caller cannot see would be a poor contract.
func TestWriteCapture_CreatesMissingParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	want := filepath.Join(dir, "shot.png")

	got, err := writeCapture(want, "screenshot", "png", []byte("x"))
	if err != nil {
		t.Fatalf("should create the parent directory: %v", err)
	}
	if got != want {
		t.Errorf("an absolute path must be honoured exactly: got %q want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

// Two captures in the same run must not collide.
func TestWriteCapture_PathsAreUnique(t *testing.T) {
	a, err := writeCapture("", "screenshot", "png", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := writeCapture("", "screenshot", "png", []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(a); _ = os.Remove(b) })

	if a == b {
		t.Fatalf("generated paths collided: %q", a)
	}
}
