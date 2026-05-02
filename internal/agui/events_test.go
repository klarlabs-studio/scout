package agui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEWriter_NonFlusherFails(t *testing.T) {
	rw := &nonFlusher{}
	if _, err := NewSSEWriter(rw); err == nil {
		t.Error("expected error when ResponseWriter does not implement Flusher")
	}
}

func TestSSEWriter_HeadersSet(t *testing.T) {
	rec := httptest.NewRecorder()
	if _, err := NewSSEWriter(rec); err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

func TestSSEWriter_WriteEvent_FrameFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewSSEWriter(rec)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := w.WriteEvent(map[string]any{"k": "v"}); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("expected SSE data prefix, got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("expected SSE frame to end with \\n\\n, got %q", body)
	}
	if !strings.Contains(body, `"k":"v"`) {
		t.Errorf("expected JSON payload in body, got %q", body)
	}
}

func TestSSEWriter_NewlinesEscaped(t *testing.T) {
	rec := httptest.NewRecorder()
	w, err := NewSSEWriter(rec)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	if err := w.WriteEvent(map[string]any{"v": "a\nb"}); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	body := rec.Body.String()
	// Embedded raw \n would break SSE framing; must be escaped to \\n.
	if strings.Contains(body, "a\nb") {
		t.Errorf("raw newline must be escaped, got %q", body)
	}
}

func TestNow_PositiveUnixMillis(t *testing.T) {
	if Now() <= 0 {
		t.Error("Now() must return positive ms timestamp")
	}
}

// nonFlusher is an http.ResponseWriter without Flusher to exercise the SSE error path.
type nonFlusher struct {
	headers http.Header
}

func (n *nonFlusher) Header() http.Header {
	if n.headers == nil {
		n.headers = make(http.Header)
	}
	return n.headers
}
func (n *nonFlusher) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusher) WriteHeader(int)             {}
