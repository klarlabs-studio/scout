package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

func contentLimit(options ContentOptions, fallback int) int {
	if options.MaxLength <= 0 {
		return fallback
	}
	return options.MaxLength
}

func elementLimit(options ContentOptions) int {
	if options.MaxElements <= 0 {
		return 100
	}
	return options.MaxElements
}

func boundedContentResult(content string, totalLength, maxLength int) *ContentResult {
	if maxLength < 0 {
		maxLength = 0
	}
	length := 0
	cut := len(content)
	for offset := 0; offset < len(content); {
		_, size := utf8.DecodeRuneInString(content[offset:])
		if length == maxLength {
			cut = offset
			break
		}
		length++
		offset += size
	}
	if cut < len(content) {
		content = content[:cut]
	}
	return &ContentResult{Content: content, ContentTrust: ContentTrustUntrustedPage, Length: length, MaxLength: maxLength, TotalLength: totalLength, Truncated: totalLength > length}
}

const maxSnapshotChars = 2_000_000

func requestedContentLimit(requested, configured, fallback int) int {
	limit := contentLimit(ContentOptions{MaxLength: configured}, fallback)
	if requested > 0 {
		limit = requested
	}
	if limit > maxSnapshotChars {
		limit = maxSnapshotChars
	}
	if limit < 1 {
		return fallback
	}
	return limit
}

type boundedContentPayload struct {
	Content     string `json:"content"`
	TotalLength int    `json:"totalLength"`
}

func decodeBoundedContent(raw any, maxLength int, targetID, frameID string) (*ContentResult, error) {
	encoded, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("browser returned unexpected content result %T", raw)
	}
	var payload boundedContentPayload
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		return nil, fmt.Errorf("decode content result: %w", err)
	}
	result := boundedContentResult(payload.Content, payload.TotalLength, maxLength)
	result.TargetID, result.FrameID = targetID, frameID
	return result, nil
}

// ContentOptions controls how page content is extracted and truncated.
type ContentOptions struct {
	// MaxLength is the maximum character length of returned content. Default 4000.
	MaxLength int
	// MaxLinks caps the number of links returned in observations. Default 25.
	MaxLinks int
	// MaxInputs caps the number of inputs returned. Default 20.
	MaxInputs int
	// MaxButtons caps the number of buttons returned. Default 15.
	MaxButtons int
	// MaxItems caps ExtractAll results. Default 50.
	MaxItems int
	// MaxRows caps table rows. Default 100.
	MaxRows int
	// MaxElements caps structured interactive-element results. Default 100.
	MaxElements int
	// MaxScreenshotBytes is the maximum screenshot size in bytes. Default 5MB.
	// Screenshots exceeding this are auto-compressed (JPEG + downscale).
	MaxScreenshotBytes int
}

// DefaultContentOptions returns sensible defaults for LLM context windows.
func DefaultContentOptions() ContentOptions {
	return ContentOptions{
		MaxLength:          4000,
		MaxLinks:           25,
		MaxInputs:          20,
		MaxButtons:         15,
		MaxItems:           50,
		MaxRows:            100,
		MaxElements:        100,
		MaxScreenshotBytes: 5 * 1024 * 1024, // 5MB
	}
}

// SetContentOptions configures content limits for the session.
func (s *Session) SetContentOptions(opts ContentOptions) {
	s.contentOpts = opts
}

// Markdown returns a compact markdown representation of the page content.
// This is much smaller than raw HTML and easier for LLMs to process.
func (s *Session) Markdown() (string, error) {
	result, err := s.MarkdownResult()
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// MarkdownResult returns compact markdown with explicit truncation metadata.
func (s *Session) MarkdownResult() (*ContentResult, error) {
	return s.MarkdownResultWithLimit(0)
}

// MarkdownResultWithLimit returns a bounded Markdown snapshot. Limits above
// two million characters are capped to keep browser-side serialization finite.
func (s *Session) MarkdownResultWithLimit(requested int) (*ContentResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.markdownResultWithEvaluator(requested, s.evaluateInFrame)
}

func (s *Session) MarkdownResultWithLimitContext(ctx context.Context, requested int) (*ContentResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.markdownResultWithEvaluator(requested, func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) })
}

func (s *Session) markdownResultWithEvaluator(requested int, evaluate func(string) (any, error)) (*ContentResult, error) {

	maxLen := requestedContentLimit(requested, s.contentOpts.MaxLength, 4000)

	js := fmt.Sprintf(`(function() {
		function boundedText(text, limit) {
			let totalLength = 0;
			const prefix = [];
			for (const char of text) {
				if (totalLength < limit) prefix.push(char);
				totalLength++;
			}
			return {content: prefix.join(''), totalLength};
		}
		function toMd(el, depth) {
			if (!el) return '';
			const tag = el.tagName ? el.tagName.toLowerCase() : '';
			const skip = ['script','style','noscript','svg','path','meta','link','head'];
			if (skip.includes(tag)) return '';

			if (el.nodeType === 3) return el.textContent;

			let md = '';
			const children = Array.from(el.childNodes).map(c => toMd(c, depth+1)).join('');

			switch(tag) {
				case 'h1': md = '\n# ' + children.trim() + '\n'; break;
				case 'h2': md = '\n## ' + children.trim() + '\n'; break;
				case 'h3': md = '\n### ' + children.trim() + '\n'; break;
				case 'h4': case 'h5': case 'h6':
					md = '\n#### ' + children.trim() + '\n'; break;
				case 'p': md = '\n' + children.trim() + '\n'; break;
				case 'br': md = '\n'; break;
				case 'a':
					const href = el.getAttribute('href') || '';
					const text = children.trim();
					md = text ? '[' + text + '](' + href + ')' : '';
					break;
				case 'img':
					const alt = el.getAttribute('alt') || '';
					md = alt ? '![' + alt + ']' : '';
					break;
				case 'strong': case 'b': md = '**' + children.trim() + '**'; break;
				case 'em': case 'i': md = '*' + children.trim() + '*'; break;
				case 'code': md = '`+"`"+`' + children.trim() + '`+"`"+`'; break;
				case 'pre': md = '\n`+"```"+`\n' + children.trim() + '\n`+"```"+`\n'; break;
				case 'li': md = '\n- ' + children.trim(); break;
				case 'ul': case 'ol': md = '\n' + children + '\n'; break;
				case 'tr': md = '| ' + children; break;
				case 'th': md = children.trim() + ' | '; break;
				case 'td': md = children.trim() + ' | '; break;
				case 'thead':
					md = children + '\n' + '|---'.repeat(el.querySelectorAll('th').length) + '|';
					break;
				case 'table': md = '\n' + children + '\n'; break;
				case 'input':
					const t = el.type || 'text';
					const n = el.name || el.id || '';
					// Do not serialize secrets or local file paths into snapshots.
					const v = t === 'password' || t === 'file' ? '' : (el.value || '');
					const ph = el.placeholder || '';
					md = '[input:' + t + ' name=' + n;
					if (v) md += ' value="' + v + '"';
					if (ph) md += ' placeholder="' + ph + '"';
					md += ']';
					break;
				case 'button':
					md = '[button: ' + children.trim() + ']';
					break;
				case 'select':
					const opts = Array.from(el.options).slice(0, 5).map(o => o.text).join(', ');
					md = '[select name=' + (el.name||el.id||'') + ': ' + opts + ']';
					break;
				case 'textarea':
					md = '[textarea name=' + (el.name||el.id||'') + ']';
					break;
				case 'form':
					md = '\n---form---\n' + children + '\n---/form---\n';
					break;
				case 'nav':
					md = '\n[nav] ' + children + '\n';
					break;
				default: md = children; break;
			}
			return md;
		}
		let result = toMd(document.body, 0);
		// Collapse whitespace
		result = result.replace(/\n{3,}/g, '\n\n').trim();
		return JSON.stringify(boundedText(result, %d));
	})()`, maxLen)

	result, err := evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("markdown conversion failed: %w", err)
	}
	return decodeBoundedContent(result, maxLen, s.page.TargetID(), s.frameID)
}

// ReadableText extracts just the main readable text content, stripping navigation,
// sidebars, and boilerplate. Uses a heuristic based on text density.
func (s *Session) ReadableText() (string, error) {
	result, err := s.ReadableTextResult()
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// ReadableTextResult returns main page text with explicit truncation metadata.
func (s *Session) ReadableTextResult() (*ContentResult, error) {
	return s.ReadableTextResultWithLimit(0)
}

// ReadableTextResultWithLimit returns bounded readable page text.
func (s *Session) ReadableTextResultWithLimit(requested int) (*ContentResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.readableTextResultWithEvaluator(requested, s.evaluateInFrame)
}

func (s *Session) ReadableTextResultWithLimitContext(ctx context.Context, requested int) (*ContentResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.readableTextResultWithEvaluator(requested, func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) })
}

func (s *Session) readableTextResultWithEvaluator(requested int, evaluate func(string) (any, error)) (*ContentResult, error) {

	maxLen := requestedContentLimit(requested, s.contentOpts.MaxLength, 4000)

	js := fmt.Sprintf(`(function() {
		function boundedText(text, limit) {
			let totalLength = 0;
			const prefix = [];
			for (const char of text) {
				if (totalLength < limit) prefix.push(char);
				totalLength++;
			}
			return {content: prefix.join(''), totalLength};
		}
		// Simple readability: find the element with the most text content
		// that isn't a container of the entire page
		const candidates = document.querySelectorAll('article, main, [role="main"], .content, .post, .article, .entry, #content, #main');
		let best = null;
		let bestLen = 0;

		for (const el of candidates) {
			const text = el.innerText || '';
			if (text.length > bestLen) {
				best = el;
				bestLen = text.length;
			}
		}

		// Fallback: use body but skip nav, header, footer, aside
		if (!best || bestLen < 100) {
			const clone = document.body.cloneNode(true);
			for (const tag of ['nav', 'header', 'footer', 'aside', 'script', 'style', 'noscript']) {
				for (const el of clone.querySelectorAll(tag)) el.remove();
			}
			const text = clone.innerText.replace(/\n{3,}/g, '\n\n').trim();
			return JSON.stringify(boundedText(text, %d));
		}

		const text = best.innerText.replace(/\n{3,}/g, '\n\n').trim();
		return JSON.stringify(boundedText(text, %d));
	})()`, maxLen, maxLen)

	result, err := evaluate(js)
	if err != nil {
		return nil, fmt.Errorf("readability extraction failed: %w", err)
	}
	return decodeBoundedContent(result, maxLen, s.page.TargetID(), s.frameID)
}

// AccessibilityTree returns a compact tree representation of the page's
// accessible elements. This is much smaller than HTML and captures the
// semantic structure that matters for interaction.
func (s *Session) AccessibilityTree() (string, error) {
	result, err := s.AccessibilityTreeResult()
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// AccessibilityTreeResult returns the accessibility tree with truncation metadata.
func (s *Session) AccessibilityTreeResult() (*ContentResult, error) {
	return s.AccessibilityTreeResultWithLimit(0)
}

// AccessibilityTreeResultWithLimit returns a bounded accessibility snapshot.
func (s *Session) AccessibilityTreeResultWithLimit(requested int) (*ContentResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.accessibilityTreeResultWithEvaluator(requested, s.evaluateInFrame)
}

func (s *Session) AccessibilityTreeResultWithLimitContext(ctx context.Context, requested int) (*ContentResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.accessibilityTreeResultWithEvaluator(requested, func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) })
}

func (s *Session) accessibilityTreeResultWithEvaluator(requested int, evaluate func(string) (any, error)) (*ContentResult, error) {

	maxLen := requestedContentLimit(requested, s.contentOpts.MaxLength, 8000)

	tree, err := s.accessibilityTreeJSWithEvaluator(maxLen, evaluate)
	if err != nil {
		return nil, err
	}
	return decodeBoundedContent(tree, maxLen, s.page.TargetID(), s.frameID)
}

func (s *Session) accessibilityTreeJSWithEvaluator(maxLen int, evaluate func(string) (any, error)) (string, error) {
	js := fmt.Sprintf(`(function() {
		const lines = [];
		let content = '';
		let totalLength = 0;
		function walk(el, depth) {
			if (!el || !el.tagName) return;
			const tag = el.tagName.toLowerCase();
			const skip = ['script','style','noscript','svg','path','br','hr'];
			if (skip.includes(tag)) return;

			const indent = '  '.repeat(depth);
			const role = el.getAttribute('role') || '';
			const ariaLabel = el.getAttribute('aria-label') || '';
			const id = el.id ? '#' + el.id : '';
			const name = el.name ? '[name=' + el.name + ']' : '';

			// Only emit meaningful nodes
			let line = '';
			switch(tag) {
				case 'a':
					const href = el.getAttribute('href') || '';
					const text = el.textContent.trim().slice(0, 80);
					if (text) line = indent + 'link "' + text + '" -> ' + href;
					break;
				case 'button':
					line = indent + 'button "' + el.textContent.trim().slice(0, 50) + '"' + id;
					break;
				case 'input':
					const t = el.type || 'text';
					const ph = el.placeholder ? ' placeholder="' + el.placeholder + '"' : '';
					// Keep accessibility snapshots from exposing passwords or file paths.
					const val = t === 'password' || t === 'file' || !el.value ? '' : ' value="' + el.value + '"';
					line = indent + 'input[' + t + ']' + id + name + ph + val;
					break;
				case 'textarea':
					line = indent + 'textarea' + id + name;
					break;
				case 'select':
					const opts = Array.from(el.options).slice(0, 3).map(o => o.text).join(', ');
					line = indent + 'select' + id + name + ' (' + opts + ')';
					break;
				case 'img':
					const alt = el.getAttribute('alt');
					if (alt) line = indent + 'img "' + alt + '"';
					break;
				case 'h1': case 'h2': case 'h3': case 'h4':
					line = indent + tag + ' "' + el.textContent.trim().slice(0, 100) + '"';
					break;
				case 'label':
					const forAttr = el.getAttribute('for') || '';
					line = indent + 'label "' + el.textContent.trim().slice(0, 50) + '"' + (forAttr ? ' for=' + forAttr : '');
					break;
				case 'form':
					line = indent + 'form' + id;
					break;
				case 'nav':
					line = indent + 'nav' + (ariaLabel ? ' "' + ariaLabel + '"' : '');
					break;
				case 'main': case 'article': case 'section': case 'aside': case 'header': case 'footer':
					line = indent + tag + (ariaLabel ? ' "' + ariaLabel + '"' : '') + id;
					break;
				default:
					if (role) line = indent + '[role=' + role + ']' + id;
					else if (el.onclick || el.getAttribute('tabindex')) {
						const text = el.textContent.trim().slice(0, 50);
						if (text) line = indent + 'interactive "' + text + '"' + id;
					}
					break;
			}

			if (line) {
				const nextLength = line.length + (totalLength ? 1 : 0);
				if (totalLength + nextLength <= %d) {
					lines.push(line);
					content += (content ? '\\n' : '') + line;
				}
				totalLength += nextLength;
			}

			for (const child of el.children) {
				walk(child, line ? depth + 1 : depth);
			}
		}
		walk(document.body, 0);
		return JSON.stringify({content: content, totalLength: totalLength});
	})()`, maxLen)

	result, err := evaluate(js)
	if err != nil {
		return "", err
	}
	content, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("unexpected content result type %T", result)
	}
	return content, nil
}
