// Package wait provides context-aware auto-wait utilities for page readiness.
package wait

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// InvalidSelectorError reports CSS syntax rejected before polling begins.
type InvalidSelectorError struct {
	Selector string
	Reason   string
}

// TargetGoneError reports that the browser target backing an evaluator no
// longer exists. Unlike ordinary evaluation errors during navigation, this
// condition cannot recover by polling the same page again.
type TargetGoneError struct{ Err error }

func (e *TargetGoneError) Error() string {
	if e == nil || e.Err == nil {
		return "wait: browser target is gone"
	}
	return "wait: browser target is gone: " + e.Err.Error()
}

func (e *TargetGoneError) Unwrap() error { return e.Err }

func (e *InvalidSelectorError) Error() string {
	return fmt.Sprintf("invalid selector %q: %s", e.Selector, e.Reason)
}

// Evaluator can execute JavaScript expressions (matches Page.Evaluate).
type Evaluator interface {
	Evaluate(expression string) (any, error)
}

// deepFindJS is a shared prelude that walks through open shadow roots
// to find an element by CSS selector. Every wait/visibility helper
// composes this in so polling matches Lit / Stencil / Web Components
// content the same way userland code does.
const deepFindJS = `
function __scoutFind(root, sel) {
  if (!root) return null;
  try { const m = root.querySelector(sel); if (m) return m; } catch(_) {}
  const kids = root.querySelectorAll ? root.querySelectorAll('*') : [];
  for (const el of kids) {
    if (el.shadowRoot) {
      const m = __scoutFind(el.shadowRoot, sel);
      if (m) return m;
    }
  }
  return null;
}`

// ForLoad waits until document.readyState is "complete".
func ForLoad(ctx context.Context, eval Evaluator) error {
	return poll(ctx, func() bool {
		result, err := eval.Evaluate(`document.readyState`)
		if err != nil {
			return false
		}
		s, ok := result.(string)
		return ok && s == "complete"
	}, "page load")
}

// ForSelector waits until at least one element matches the CSS
// selector. Pierces open shadow roots so Lit / Stencil components
// match against their internal nodes.
func ForSelector(ctx context.Context, eval Evaluator, selector string) error {
	// These agent selectors are resolved by the higher-level Session API after
	// the CSS wait path; preserve that fallback rather than treating them as CSS.
	if !strings.Contains(selector, ":text(") && !strings.Contains(selector, ":has-text(") {
		validationJS := fmt.Sprintf(`(function(){
			try { document.querySelector(%q); return ''; }
			catch (e) { return e.name + ': ' + e.message; }
		})()`, selector)
		validation, err := eval.Evaluate(validationJS)
		if err != nil {
			return err
		}
		if reason, ok := validation.(string); ok && reason != "" {
			return &InvalidSelectorError{Selector: selector, Reason: reason}
		}
	}

	js := fmt.Sprintf(`(function(){%s
		return __scoutFind(document, %q) !== null;
	})()`, deepFindJS, selector)
	return pollWithError(ctx, func() (bool, error) {
		result, err := eval.Evaluate(js)
		if err != nil {
			if targetGone(err) {
				return false, &TargetGoneError{Err: err}
			}
			return false, nil
		}
		b, ok := result.(bool)
		return ok && b, nil
	}, fmt.Sprintf("selector %q", selector))
}

func targetGone(err error) bool {
	var gone *TargetGoneError
	if errors.As(err, &gone) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "target closed") || strings.Contains(msg, "target was closed") || strings.Contains(msg, "session with given id not found") || strings.Contains(msg, "session not found") || strings.Contains(msg, "inspected target navigated or closed")
}

func pollWithError(ctx context.Context, check func() (bool, error), desc string) error {
	for {
		if err := ctx.Err(); err != nil {
			return waitContextError(err, desc)
		}
		ok, err := check()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return waitContextError(ctx.Err(), desc)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func waitContextError(err error, desc string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("wait: timeout while waiting for %s: %w", desc, err)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("wait: timeout or cancellation while waiting for %s: %w", desc, err)
	}
	return fmt.Errorf("wait: %s while waiting for %s", err, desc)
}

// ForVisible waits until the element matching selector is visible.
// Pierces open shadow roots.
func ForVisible(ctx context.Context, eval Evaluator, selector string) error {
	js := fmt.Sprintf(`(function() {%s
		const el = __scoutFind(document, %q);
		if (!el) return false;
		const style = window.getComputedStyle(el);
		return style.display !== 'none' && style.visibility !== 'hidden' && style.opacity !== '0';
	})()`, deepFindJS, selector)

	return poll(ctx, func() bool {
		result, err := eval.Evaluate(js)
		if err != nil {
			return false
		}
		b, ok := result.(bool)
		return ok && b
	}, fmt.Sprintf("%q visible", selector))
}

// ForHidden waits until the element matching selector is hidden or
// absent. Pierces open shadow roots.
func ForHidden(ctx context.Context, eval Evaluator, selector string) error {
	js := fmt.Sprintf(`(function() {%s
		const el = __scoutFind(document, %q);
		if (!el) return true;
		const style = window.getComputedStyle(el);
		return style.display === 'none' || style.visibility === 'hidden';
	})()`, deepFindJS, selector)

	return poll(ctx, func() bool {
		result, err := eval.Evaluate(js)
		if err != nil {
			return false
		}
		b, ok := result.(bool)
		return ok && b
	}, fmt.Sprintf("%q hidden", selector))
}

// ForFunction waits until a JavaScript expression returns true.
func ForFunction(ctx context.Context, eval Evaluator, js string) error {
	return poll(ctx, func() bool {
		result, err := eval.Evaluate(js)
		if err != nil {
			return false
		}
		b, ok := result.(bool)
		return ok && b
	}, "condition")
}

func poll(ctx context.Context, check func() bool, desc string) error {
	for {
		if err := ctx.Err(); err != nil {
			return waitContextError(err, desc)
		}
		if check() {
			return nil
		}
		select {
		case <-ctx.Done():
			return waitContextError(ctx.Err(), desc)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
