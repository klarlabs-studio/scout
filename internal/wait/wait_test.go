package wait

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mockEvaluator implements the Evaluator interface for testing.
type mockEvaluator struct {
	results []any
	errs    []error
	calls   atomic.Int64
}

func (m *mockEvaluator) Evaluate(_ string) (any, error) {
	idx := int(m.calls.Add(1)) - 1
	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx < len(m.results) {
		return m.results[idx], nil
	}
	// Default: return last result if calls exceed configured results.
	if len(m.results) > 0 {
		return m.results[len(m.results)-1], nil
	}
	return nil, errors.New("no results configured")
}

// expressionCapture records the JS expression passed to Evaluate.
type expressionCapture struct {
	expression string
	result     any
	err        error
}

func (e *expressionCapture) Evaluate(expr string) (any, error) {
	e.expression = expr
	return e.result, e.err
}

func TestForLoad(t *testing.T) {
	tests := []struct {
		name    string
		results []any
		errs    []error
		wantErr bool
	}{
		{
			name:    "immediate complete",
			results: []any{"complete"},
		},
		{
			name:    "loading then complete",
			results: []any{"loading", "interactive", "complete"},
		},
		{
			name:    "error then complete",
			results: []any{nil, "complete"},
			errs:    []error{errors.New("eval error"), nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			eval := &mockEvaluator{results: tt.results, errs: tt.errs}
			err := ForLoad(ctx, eval)

			if tt.wantErr {
				if err == nil {
					t.Error("ForLoad() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ForLoad() unexpected error: %v", err)
			}
		})
	}
}

func TestForLoadTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{results: []any{"loading"}}
	err := ForLoad(ctx, eval)

	if err == nil {
		t.Fatal("ForLoad() expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("ForLoad() error = %q, want timeout message", err.Error())
	}
	if !strings.Contains(err.Error(), "page load") {
		t.Errorf("ForLoad() error = %q, want 'page load' description", err.Error())
	}
}

func TestForLoadContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	eval := &mockEvaluator{results: []any{"loading"}}

	done := make(chan error, 1)
	go func() {
		done <- ForLoad(ctx, eval)
	}()

	// Cancel after a short delay.
	time.Sleep(80 * time.Millisecond)
	cancel()

	err := <-done
	if err == nil {
		t.Fatal("ForLoad() expected cancellation error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("ForLoad() error = %q, want timeout/cancellation message", err.Error())
	}
}

func TestForSelector(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		results  []any
		wantErr  bool
	}{
		{
			name:     "element found immediately",
			selector: "#main",
			results:  []any{true},
		},
		{
			name:     "element found after retries",
			selector: ".loading",
			results:  []any{false, false, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			eval := &mockEvaluator{results: tt.results}
			err := ForSelector(ctx, eval, tt.selector)

			if tt.wantErr {
				if err == nil {
					t.Error("ForSelector() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ForSelector() unexpected error: %v", err)
			}
		})
	}
}

func TestForSelectorTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{results: []any{false}}
	err := ForSelector(ctx, eval, "#never-found")

	if err == nil {
		t.Fatal("ForSelector() expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), `selector "#never-found"`) {
		t.Errorf("ForSelector() error = %q, want selector description", err.Error())
	}
}

func TestForSelectorJSExpression(t *testing.T) {
	// Verify the JS embeds the selector inside the shadow-piercing
	// find call. We don't snapshot the full multi-line walker; the
	// substring checks pin the surface downstream code depends on.
	eval := &expressionCapture{result: true}
	ctx := context.Background()

	_ = ForSelector(ctx, eval, "div.container > p")
	if !strings.Contains(eval.expression, `__scoutFind(document, "div.container > p")`) {
		t.Errorf("ForSelector() JS missing piercing call for selector: %q", eval.expression)
	}
	if !strings.Contains(eval.expression, "!== null") {
		t.Errorf("ForSelector() JS missing presence check: %q", eval.expression)
	}
}

func TestForVisible(t *testing.T) {
	tests := []struct {
		name    string
		results []any
		wantErr bool
	}{
		{
			name:    "visible immediately",
			results: []any{true},
		},
		{
			name:    "hidden then visible",
			results: []any{false, false, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			eval := &mockEvaluator{results: tt.results}
			err := ForVisible(ctx, eval, "#modal")

			if tt.wantErr {
				if err == nil {
					t.Error("ForVisible() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ForVisible() unexpected error: %v", err)
			}
		})
	}
}

func TestForVisibleTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{results: []any{false}}
	err := ForVisible(ctx, eval, "#hidden")

	if err == nil {
		t.Fatal("ForVisible() expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), `"#hidden" visible`) {
		t.Errorf("ForVisible() error = %q, want visible description", err.Error())
	}
}

func TestForHidden(t *testing.T) {
	tests := []struct {
		name    string
		results []any
		wantErr bool
	}{
		{
			name:    "hidden immediately",
			results: []any{true},
		},
		{
			name:    "visible then hidden",
			results: []any{false, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			eval := &mockEvaluator{results: tt.results}
			err := ForHidden(ctx, eval, "#spinner")

			if tt.wantErr {
				if err == nil {
					t.Error("ForHidden() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ForHidden() unexpected error: %v", err)
			}
		})
	}
}

func TestForHiddenTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{results: []any{false}}
	err := ForHidden(ctx, eval, "#visible-forever")

	if err == nil {
		t.Fatal("ForHidden() expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), `"#visible-forever" hidden`) {
		t.Errorf("ForHidden() error = %q, want hidden description", err.Error())
	}
}

func TestForFunction(t *testing.T) {
	tests := []struct {
		name    string
		js      string
		results []any
		wantErr bool
	}{
		{
			name:    "true immediately",
			js:      "window.ready === true",
			results: []any{true},
		},
		{
			name:    "false then true",
			js:      "document.title !== ''",
			results: []any{false, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			eval := &mockEvaluator{results: tt.results}
			err := ForFunction(ctx, eval, tt.js)

			if tt.wantErr {
				if err == nil {
					t.Error("ForFunction() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ForFunction() unexpected error: %v", err)
			}
		})
	}
}

func TestForFunctionTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{results: []any{false}}
	err := ForFunction(ctx, eval, "false")

	if err == nil {
		t.Fatal("ForFunction() expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "condition") {
		t.Errorf("ForFunction() error = %q, want 'condition' description", err.Error())
	}
}

func TestForFunctionJSPassthrough(t *testing.T) {
	eval := &expressionCapture{result: true}
	ctx := context.Background()

	customJS := "window.myApp && window.myApp.loaded"
	_ = ForFunction(ctx, eval, customJS)
	if eval.expression != customJS {
		t.Errorf("ForFunction() JS = %q, want %q", eval.expression, customJS)
	}
}

func TestPollEvaluatorError(t *testing.T) {
	// When Evaluate returns an error, poll should keep retrying, not succeed.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{
		errs: []error{
			errors.New("err1"),
			errors.New("err2"),
			errors.New("err3"),
		},
		results: []any{nil, nil, nil},
	}
	err := ForLoad(ctx, eval)
	if err == nil {
		t.Error("expected timeout error when Evaluate always fails")
	}
}

func TestPollWrongType(t *testing.T) {
	// ForSelector expects bool; give it a string. Should keep polling until timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{results: []any{"not a bool"}}
	err := ForSelector(ctx, eval, "#test")
	if err == nil {
		t.Error("expected timeout when Evaluate returns wrong type")
	}
}

func TestForLoadWrongType(t *testing.T) {
	// ForLoad expects string; give it an int. Should keep polling until timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	eval := &mockEvaluator{results: []any{42}}
	err := ForLoad(ctx, eval)
	if err == nil {
		t.Error("expected timeout when readyState is not a string")
	}
}

func TestPollCallCount(t *testing.T) {
	// Verify poll calls check repeatedly.
	eval := &mockEvaluator{results: []any{false, false, false, true}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := ForSelector(ctx, eval, "#el")
	if err != nil {
		t.Fatalf("ForSelector() unexpected error: %v", err)
	}

	calls := eval.calls.Load()
	if calls < 4 {
		t.Errorf("expected at least 4 Evaluate calls, got %d", calls)
	}
}

func TestForSelectorSpecialChars(t *testing.T) {
	// Verify selectors with special characters survive the quote
	// round-trip inside the shadow-piercing find call.
	eval := &expressionCapture{result: true}
	ctx := context.Background()

	selector := `input[name="email"]`
	_ = ForSelector(ctx, eval, selector)

	want := fmt.Sprintf("__scoutFind(document, %q)", selector)
	if !strings.Contains(eval.expression, want) {
		t.Errorf("ForSelector() JS missing %q in: %q", want, eval.expression)
	}
}
