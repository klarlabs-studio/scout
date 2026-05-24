// Package wait provides context-aware auto-wait utilities for page readiness.
package wait

import (
	"context"
	"fmt"
	"time"
)

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
	js := fmt.Sprintf(`(function(){%s
		return __scoutFind(document, %q) !== null;
	})()`, deepFindJS, selector)
	return poll(ctx, func() bool {
		result, err := eval.Evaluate(js)
		if err != nil {
			return false
		}
		b, ok := result.(bool)
		return ok && b
	}, fmt.Sprintf("selector %q", selector))
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
		if check() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait: timeout waiting for %s", desc)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
