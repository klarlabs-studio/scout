package browse

import (
	"errors"
	"fmt"
	"testing"

	"go.klarlabs.de/scout/internal/cdp"
)

func TestTimeoutError(t *testing.T) {
	e := &TimeoutError{Operation: "click", Selector: "#btn"}
	s := e.Error()
	if s != `browse: timeout waiting for click on "#btn"` {
		t.Errorf("unexpected: %s", s)
	}

	e2 := &TimeoutError{Operation: "page load"}
	s2 := e2.Error()
	if s2 != "browse: timeout during page load" {
		t.Errorf("unexpected: %s", s2)
	}
}

func TestElementNotFoundError(t *testing.T) {
	e := &ElementNotFoundError{Selector: "#missing"}
	s := e.Error()
	if s != `browse: element not found: "#missing"` {
		t.Errorf("unexpected: %s", s)
	}
}

func TestNavigationError(t *testing.T) {
	inner := errors.New("net error")
	e := &NavigationError{URL: "http://bad", Err: inner}
	s := e.Error()
	if s != `browse: navigation to "http://bad" failed: net error` {
		t.Errorf("unexpected: %s", s)
	}
	if !errors.Is(e, inner) {
		t.Error("Unwrap should return inner error")
	}
}

func TestNavigationErrorNilInner(t *testing.T) {
	e := &NavigationError{URL: "http://test", Err: nil}
	s := e.Error()
	if s != `browse: navigation to "http://test" failed: <nil>` {
		t.Errorf("unexpected: %s", s)
	}
	if e.Unwrap() != nil {
		t.Error("Unwrap of nil Err should be nil")
	}
}

func TestRateLimitError(t *testing.T) {
	e := &RateLimitError{TaskName: "scrape"}
	s := e.Error()
	if s != `browse: rate limit exceeded for task "scrape"` {
		t.Errorf("unexpected: %s", s)
	}
}

func TestCircuitOpenError(t *testing.T) {
	e := &CircuitOpenError{TaskName: "api-call"}
	s := e.Error()
	if s != `browse: circuit open, task "api-call" rejected` {
		t.Errorf("unexpected: %s", s)
	}
}

func TestBulkheadFullError(t *testing.T) {
	e := &BulkheadFullError{TaskName: "batch"}
	s := e.Error()
	if s != `browse: bulkhead full, task "batch" rejected` {
		t.Errorf("unexpected: %s", s)
	}
}

func TestTimeoutErrorVariants(t *testing.T) {
	tests := []struct {
		name     string
		err      *TimeoutError
		expected string
	}{
		{
			"with selector",
			&TimeoutError{Operation: "wait for selector", Selector: "div.loading"},
			`browse: timeout waiting for wait for selector on "div.loading"`,
		},
		{
			"without selector",
			&TimeoutError{Operation: "navigation"},
			"browse: timeout during navigation",
		},
		{
			"empty operation",
			&TimeoutError{},
			"browse: timeout during ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestElementNotFoundErrorVariants(t *testing.T) {
	tests := []struct {
		selector string
		expected string
	}{
		{"#id", `browse: element not found: "#id"`},
		{".class", `browse: element not found: ".class"`},
		{"", `browse: element not found: ""`},
	}
	for _, tt := range tests {
		e := &ElementNotFoundError{Selector: tt.selector}
		if got := e.Error(); got != tt.expected {
			t.Errorf("got %q, want %q", got, tt.expected)
		}
	}
}

func TestIsOutcomeUnknownRecognizesWrappedBrowserDispatchErrors(t *testing.T) {
	err := fmt.Errorf("click: %w", &cdp.DispatchOutcomeUnknownError{
		Method: "Input.dispatchMouseEvent",
		Err:    errors.New("connection closed after write"),
	})
	if !IsOutcomeUnknown(err) {
		t.Fatalf("IsOutcomeUnknown(%T) = false, want true", err)
	}
}

func TestIsOutcomeUnknownRejectsDefinitiveErrors(t *testing.T) {
	if IsOutcomeUnknown(&NavigationError{URL: "https://example.test", Err: errors.New("connection refused")}) {
		t.Fatal("definitive navigation failure classified as an unknown outcome")
	}
	if IsOutcomeUnknown(nil) {
		t.Fatal("nil error classified as an unknown outcome")
	}
}
