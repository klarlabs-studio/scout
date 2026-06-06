package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/scout/agent"
)

func TestMCPErr_NilPassthrough(t *testing.T) {
	if err := mcpErr(nil); err != nil {
		t.Errorf("nil input must return nil, got %v", err)
	}
}

func TestMCPErr_PlainErrorPassthrough(t *testing.T) {
	in := errors.New("boom")
	if err := mcpErr(in); err != in {
		t.Errorf("plain error must be returned as-is, got %v", err)
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
	raw := `{"headless": false, "allow_private_ips": true}`
	var in ConfigureInput
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in.Headless != false || !in.AllowPrivateIPs {
		t.Errorf("ConfigureInput parse mismatch: %+v", in)
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
