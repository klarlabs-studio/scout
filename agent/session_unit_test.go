package agent

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestJsonQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", `"hello"`},
		{"", `""`},
		{`with "quotes"`, `"with \"quotes\""`},
		{"with\nnewline", `"with\nnewline"`},
		{"with\ttab", `"with\ttab"`},
		{`back\slash`, `"back\\slash"`},
		{"unicode: 日本語", `"unicode: 日本語"`},
		{`<script>alert('xss')</script>`, `"\u003cscript\u003ealert('xss')\u003c/script\u003e"`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := jsonQuote(tt.input)
			if got != tt.want {
				t.Errorf("jsonQuote(%q) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultContentOptions_Values(t *testing.T) {
	opts := DefaultContentOptions()
	if opts.MaxLength != 4000 {
		t.Errorf("MaxLength: got %d", opts.MaxLength)
	}
	if opts.MaxLinks != 25 {
		t.Errorf("MaxLinks: got %d", opts.MaxLinks)
	}
	if opts.MaxInputs != 20 {
		t.Errorf("MaxInputs: got %d", opts.MaxInputs)
	}
	if opts.MaxButtons != 15 {
		t.Errorf("MaxButtons: got %d", opts.MaxButtons)
	}
	if opts.MaxItems != 50 {
		t.Errorf("MaxItems: got %d", opts.MaxItems)
	}
	if opts.MaxRows != 100 {
		t.Errorf("MaxRows: got %d", opts.MaxRows)
	}
	if opts.MaxScreenshotBytes != 5*1024*1024 {
		t.Errorf("MaxScreenshotBytes: got %d", opts.MaxScreenshotBytes)
	}
}

func TestSetContentOptions(t *testing.T) {
	s := newTestSession()
	custom := ContentOptions{
		MaxLength:  1000,
		MaxLinks:   5,
		MaxInputs:  3,
		MaxButtons: 2,
		MaxItems:   10,
		MaxRows:    20,
	}
	s.SetContentOptions(custom)
	if s.contentOpts.MaxLength != 1000 {
		t.Errorf("MaxLength: got %d", s.contentOpts.MaxLength)
	}
	if s.contentOpts.MaxLinks != 5 {
		t.Errorf("MaxLinks: got %d", s.contentOpts.MaxLinks)
	}
}

func TestTraceBeforeAction_NotTracing(t *testing.T) {
	s := newTestSession()
	start, before := s.traceBeforeAction()
	if !start.IsZero() {
		t.Error("start should be zero when not tracing")
	}
	if before != nil {
		t.Error("before should be nil when not tracing")
	}
}

func TestTraceAfterAction_NotTracing(t *testing.T) {
	s := newTestSession()
	s.traceAfterAction(time.Now(), nil, "click", "#btn", "", "", nil)
	if s.trace != nil {
		t.Error("trace should remain nil when not tracing")
	}
}

func TestTraceAfterAction_Tracing(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start := time.Now()
	s.traceAfterAction(start, nil, "navigate", "", "", "https://example.com", nil)

	if len(s.trace.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(s.trace.events))
	}
	ev := s.trace.events[0]
	if ev.Action != "navigate" {
		t.Errorf("action: got %q", ev.Action)
	}
	if ev.URL != "https://example.com" {
		t.Errorf("url: got %q", ev.URL)
	}
	if ev.Error != "" {
		t.Errorf("error should be empty, got %q", ev.Error)
	}
}

func TestTraceAfterAction_WithError(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start := time.Now()
	s.traceAfterAction(start, nil, "click", "#btn", "", "", fmt.Errorf("element not found"))

	if len(s.trace.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(s.trace.events))
	}
	if s.trace.events[0].Error != "element not found" {
		t.Errorf("error: got %q", s.trace.events[0].Error)
	}
}

func TestTraceAfterAction_WithScreenshots(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start := time.Now()
	before := []byte("before-screenshot-data")
	s.traceAfterAction(start, before, "click", "#btn", "", "", nil)

	if len(s.trace.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(s.trace.events))
	}
	if s.trace.events[0].BeforeImg != "before-screenshot-data" {
		t.Errorf("BeforeImg: got %q", s.trace.events[0].BeforeImg)
	}
}

func TestTraceBeforeAction_Tracing(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	start, _ := s.traceBeforeAction()
	if start.IsZero() {
		t.Error("start should not be zero when tracing")
	}
}

func TestCaptureTraceScreenshot_NilPage(t *testing.T) {
	s := newTestSession()
	result := s.captureTraceScreenshot()
	if result != nil {
		t.Error("expected nil for nil page")
	}
}

func TestEnsurePage_ClosedSession(t *testing.T) {
	s := newTestSession()
	s.closed = true
	err := s.ensurePage()
	if err == nil {
		t.Error("expected error for closed session")
	}
	if err.Error() != "session is closed" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSessionConfig_Defaults(t *testing.T) {
	cfg := SessionConfig{}
	if cfg.Timeout != 0 {
		t.Errorf("default Timeout should be 0 (set in constructor)")
	}
	if cfg.Headless {
		t.Error("default Headless should be false")
	}
}

func TestNewSessionFromBrowser_DefaultTimeout(t *testing.T) {
	cfg := SessionConfig{}
	s := NewSessionFromBrowser(nil, cfg)
	if s.timeout != 30*time.Second {
		t.Errorf("default timeout: got %v, want 30s", s.timeout)
	}
}

func TestNewSessionFromBrowser_CustomTimeout(t *testing.T) {
	cfg := SessionConfig{Timeout: 10 * time.Second}
	s := NewSessionFromBrowser(nil, cfg)
	if s.timeout != 10*time.Second {
		t.Errorf("timeout: got %v, want 10s", s.timeout)
	}
}

func TestNewSessionFromBrowser_ContentOpts(t *testing.T) {
	cfg := SessionConfig{}
	s := NewSessionFromBrowser(nil, cfg)
	if s.contentOpts.MaxLength != 4000 {
		t.Errorf("MaxLength: got %d, want 4000", s.contentOpts.MaxLength)
	}
}

func TestNewSessionFromBrowser_Stealth(t *testing.T) {
	cfg := SessionConfig{Stealth: true}
	s := NewSessionFromBrowser(nil, cfg)
	if !s.stealth {
		t.Error("stealth should be true")
	}
}

func TestTraceAfterAction_NilTrace(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = nil
	s.traceAfterAction(time.Now(), nil, "click", "", "", "", nil)
}

func TestTraceAfterAction_MultipleEvents(t *testing.T) {
	s := newTestSession()
	s.tracing = true
	s.trace = &traceState{startTime: time.Now()}

	for i := 0; i < 5; i++ {
		start := time.Now()
		s.traceAfterAction(start, nil, "click", "#btn", "", "", nil)
	}

	if len(s.trace.events) != 5 {
		t.Errorf("expected 5 events, got %d", len(s.trace.events))
	}
	for i, ev := range s.trace.events {
		if ev.Index != i {
			t.Errorf("event[%d].Index = %d", i, ev.Index)
		}
	}
}

func TestStatus_BasicFields(t *testing.T) {
	s := newTestSession()
	s.lastError = "boom"
	s.consecutiveTimeouts = 2
	s.inflightCommands = 1
	st := s.Status()
	if st == nil {
		t.Fatal("expected status")
	}
	if !st.SessionAlive {
		t.Fatal("expected session_alive=true")
	}
	if st.BrowserAlive {
		t.Fatal("expected browser_alive=false for test session without browser")
	}
	if st.LastError != "boom" {
		t.Fatalf("last_error: got %q", st.LastError)
	}
	if st.ConsecutiveTimeouts != 2 {
		t.Fatalf("consecutive_timeouts: got %d", st.ConsecutiveTimeouts)
	}
	if st.InFlightCommands != 1 {
		t.Fatalf("inflight_command_count: got %d", st.InFlightCommands)
	}
}

func TestWrapDetailedError_Timeout(t *testing.T) {
	s := newTestSession()
	err := s.wrapDetailedError("navigate", fmt.Errorf("context deadline exceeded while loading"))
	op, ok := err.(*OperationError)
	if !ok {
		t.Fatalf("expected *OperationError, got %T", err)
	}
	if op.Cause != "timeout" {
		t.Fatalf("cause: got %q", op.Cause)
	}
	if op.Phase != "navigate" {
		t.Fatalf("phase: got %q", op.Phase)
	}
}

func TestWrapDetailedError_NetOp(t *testing.T) {
	s := newTestSession()
	netErr := &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")}
	err := s.wrapDetailedError("navigate", netErr)
	op, ok := err.(*OperationError)
	if !ok {
		t.Fatalf("expected *OperationError, got %T", err)
	}
	if op.Cause != "connection_refused" {
		t.Fatalf("cause: got %q", op.Cause)
	}
	if op.Detail != "dial" {
		t.Fatalf("detail: got %q", op.Detail)
	}
}
