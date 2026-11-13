package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ExtractedPattern represents a repeating pattern found on the page (product cards, list items, etc).
type ExtractedPattern struct {
	Pattern    string                      `json:"pattern"` // CSS selector for the repeating container
	TargetID   string                      `json:"target_id,omitempty"`
	FrameID    string                      `json:"frame_id,omitempty"`
	Count      int                         `json:"count"` // number of items found
	Offset     int                         `json:"offset"`
	Limit      int                         `json:"limit"`
	Truncated  bool                        `json:"truncated"`
	Fields     []string                    `json:"fields"` // detected field names
	Items      []map[string]string         `json:"items"`  // extracted structured data
	References []ExtractedPatternReference `json:"references,omitempty"`
}

// ExtractedPatternReference maps one item and its extracted values back to
// CSS selectors and stable node handles in the target and frame reported by
// ExtractedPattern. Selectors describe the DOM at extraction time. Handles
// continue to point to the same attached nodes across reordering and become
// stale when those nodes are replaced or removed.
type ExtractedPatternReference struct {
	ItemSelector   string                               `json:"item_selector"`
	FieldSelectors map[string]string                    `json:"field_selectors,omitempty"`
	ItemHandle     string                               `json:"item_handle,omitempty"`
	FieldHandles   map[string]string                    `json:"field_handles,omitempty"`
	ItemSemantics  ExtractedElementSemantics            `json:"item_semantics"`
	FieldSemantics map[string]ExtractedElementSemantics `json:"field_semantics,omitempty"`
}

// AutoExtract detects repeating patterns on the page and extracts structured data.
// Works without selectors — finds product cards, search results, list items, table rows automatically.
func (s *Session) AutoExtract() (*ExtractedPattern, error) {
	return s.AutoExtractPage(0, 0)
}

// AutoExtractPage returns a bounded page of the best repeating pattern.
// Offset and limit allow callers to retrieve the complete candidate set.
func (s *Session) AutoExtractPage(offset, limit int) (*ExtractedPattern, error) {
	return s.AutoExtractPageContext(context.Background(), offset, limit)
}

// AutoExtractPageContext returns a bounded extraction page while honoring ctx.
func (s *Session) AutoExtractPageContext(ctx context.Context, offset, limit int) (*ExtractedPattern, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	maxItems := s.contentOpts.MaxItems
	if maxItems == 0 {
		maxItems = 50
	}
	if limit > 0 {
		maxItems = min(limit, 100)
	}
	if maxItems < 1 {
		maxItems = 1
	}
	if offset < 0 {
		offset = 0
	}

	js := fmt.Sprintf(`(function() {
		// Find repeating patterns by looking for parent elements with 3+ similar children
		const candidates = [];

		// Strategy 1: Lists (ul/ol with li children)
		for (const list of document.querySelectorAll('ul, ol')) {
			const items = list.querySelectorAll(':scope > li');
			if (items.length >= 3) candidates.push({parent: list, items: Array.from(items), type: 'list'});
		}

		// Strategy 2: Grids/cards (parent with 3+ children sharing same tag+class)
		for (const parent of document.querySelectorAll('main, [role="main"], .content, .results, .products, .items, .cards, .grid, .list, section, div')) {
			const children = parent.children;
			if (children.length < 3) continue;

			// Group children by tag+class signature
			const groups = {};
			for (const child of children) {
				const sig = child.tagName + '.' + (child.className || '').split(' ').sort().join('.');
				if (!groups[sig]) groups[sig] = [];
				groups[sig].push(child);
			}

			for (const [sig, items] of Object.entries(groups)) {
				if (items.length >= 3 && items.length >= children.length * 0.5) {
					candidates.push({parent, items, type: 'card'});
				}
			}
		}

		// Strategy 3: Tables
		for (const table of document.querySelectorAll('table')) {
			const rows = table.querySelectorAll('tbody tr, tr');
			if (rows.length >= 2) candidates.push({parent: table, items: Array.from(rows), type: 'table'});
		}

		if (candidates.length === 0) return null;

		// Pick the best candidate (most items, deepest nesting)
		candidates.sort((a, b) => b.items.length - a.items.length);
		const best = candidates[0];

		// Extract fields from items
		const items = [];
		const references = [];
		const fieldSet = new Set();
		const handlePrefix = 'h_extract_%d_';
		function handleFor(el, suffix) {
			let handle = el.getAttribute('data-scout-handle');
			if (!handle) {
				handle = handlePrefix + suffix;
				el.setAttribute('data-scout-handle', handle);
			}
			return handle;
		}
		function selectorFor(el) {
			const parts = [];
			let node = el;
			while (node && node.nodeType === 1) {
				if (node.id) {
					const idSelector = '#' + CSS.escape(node.id);
					if (document.querySelectorAll(idSelector).length === 1) {
						parts.unshift(idSelector);
						break;
					}
				}
				let part = node.tagName.toLowerCase();
				const parent = node.parentElement;
				if (parent) {
					const siblings = Array.from(parent.children).filter(child => child.tagName === node.tagName);
					if (siblings.length > 1) part += ':nth-of-type(' + (siblings.indexOf(node) + 1) + ')';
				}
				parts.unshift(part);
				node = parent;
			}
			return parts.join(' > ');
		}
		function semanticsFor(el) {
			const tag=el.tagName.toLowerCase();
			let role=(el.getAttribute('role')||'').trim().split(/\\s+/)[0];
			if(!role){
				if(tag==='a'&&el.hasAttribute('href'))role='link';
				else if(tag==='button'||tag==='summary')role='button';
				else if(tag==='textarea'||el.isContentEditable)role='textbox';
				else if(tag==='select')role=el.multiple?'listbox':'combobox';
				else if(tag==='input'){
					const inputRoles={button:'button',image:'button',submit:'button',reset:'button',checkbox:'checkbox',radio:'radio',range:'slider',number:'spinbutton',hidden:''};
					role=Object.prototype.hasOwnProperty.call(inputRoles,el.type)?inputRoles[el.type]:'textbox';
				}
			}
			const interactive=!!(role==='button'||role==='link'||role==='checkbox'||role==='radio'||role==='switch'||role==='menuitem'||role==='option'||role==='slider'||role==='spinbutton'||role==='textbox'||role==='combobox'||role==='listbox'||el.hasAttribute('onclick')||el.hasAttribute('tabindex'));
			return {tag,role,interactive};
		}

		for (const [pageIndex, item] of best.items.slice(%d, %d).entries()) {
			const data = {};
			const fieldSelectors = {};
			const fieldHandles = {};
			const fieldSemantics = {};
			const itemHandle = handleFor(item, 'item_' + (%d + pageIndex));
			function clip(value, max) { return String(value || '').slice(0, max); }

			// Extract text from semantic elements
			const title = item.querySelector('h1,h2,h3,h4,h5,a[href],.title,.name,[class*="title"],[class*="name"]');
			if (title) { data.title = title.textContent.trim().slice(0, 200); fieldSelectors.title = selectorFor(title); fieldHandles.title = handleFor(title, 'field_title_' + (%d + pageIndex)); fieldSemantics.title = semanticsFor(title); fieldSet.add('title'); }

			const price = item.querySelector('.price,[class*="price"],[data-price]');
			if (price) { data.price = clip(price.textContent.trim(), 100); fieldSelectors.price = selectorFor(price); fieldHandles.price = handleFor(price, 'field_price_' + (%d + pageIndex)); fieldSemantics.price = semanticsFor(price); fieldSet.add('price'); }

			const link = item.querySelector('a[href]');
			if (link) { data.link = clip(link.getAttribute('href'), 500); fieldSelectors.link = selectorFor(link); fieldHandles.link = handleFor(link, 'field_link_' + (%d + pageIndex)); fieldSemantics.link = semanticsFor(link); fieldSet.add('link'); }

			const img = item.querySelector('img[src]');
			if (img) { data.image = clip(img.getAttribute('src'), 500); fieldSelectors.image = selectorFor(img); fieldHandles.image = handleFor(img, 'field_image_' + (%d + pageIndex)); fieldSemantics.image = semanticsFor(img); fieldSet.add('image'); }

			const desc = item.querySelector('p,.description,[class*="desc"],[class*="summary"]');
			if (desc) { data.description = desc.textContent.trim().slice(0, 300); fieldSelectors.description = selectorFor(desc); fieldHandles.description = handleFor(desc, 'field_description_' + (%d + pageIndex)); fieldSemantics.description = semanticsFor(desc); fieldSet.add('description'); }

			const rating = item.querySelector('[class*="rating"],[class*="star"],[aria-label*="star"]');
			if (rating) { data.rating = clip(rating.textContent.trim() || rating.getAttribute('aria-label') || '', 100); fieldSelectors.rating = selectorFor(rating); fieldHandles.rating = handleFor(rating, 'field_rating_' + (%d + pageIndex)); fieldSemantics.rating = semanticsFor(rating); fieldSet.add('rating'); }

			// Fallback: if no semantic fields found, use full text
			if (Object.keys(data).length === 0) {
				data.text = item.textContent.trim().slice(0, 300);
				fieldSelectors.text = selectorFor(item);
				fieldHandles.text = itemHandle;
				fieldSemantics.text = semanticsFor(item);
				fieldSet.add('text');
			}

			items.push(data);
			references.push({item_selector: selectorFor(item), field_selectors: fieldSelectors, item_handle: itemHandle, field_handles: fieldHandles, item_semantics: semanticsFor(item), field_semantics: fieldSemantics});
		}

		// Build an escaped, unique selector for the repeating container.
		const selector = selectorFor(best.parent);

		return JSON.stringify({
			pattern: selector,
			count: best.items.length,
		offset: %d,
		limit: %d,
			truncated: best.items.length > Math.min(best.items.length, %d),
			fields: Array.from(fieldSet),
			items: items,
			references: references
		});
	})()`, time.Now().UnixNano(), offset, offset+maxItems, offset, offset, offset, offset, offset, offset, offset, offset, maxItems, offset+maxItems)

	result, err := s.evaluateCurrentContext(ctx, js)
	if err != nil {
		return nil, fmt.Errorf("auto-extract failed: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("no repeating pattern found on page")
	}

	str, _ := result.(string)
	var pattern ExtractedPattern
	if err := json.Unmarshal([]byte(str), &pattern); err != nil {
		return nil, err
	}
	pattern.TargetID, pattern.FrameID = s.page.TargetID(), s.frameID
	return &pattern, nil
}

// ScrollAndCollect auto-scrolls the page and collects items as they lazy-load.
// Scrolls until no new items appear or maxItems is reached.
func (s *Session) ScrollAndCollect(selector string, maxItems int) (*ExtractAllResult, error) {
	return s.ScrollAndCollectPage(selector, 0, maxItems)
}

// ScrollAndCollectPage returns one bounded slice from the loaded matching items.
// Call it again with a higher offset to retrieve subsequent slices.
func (s *Session) ScrollAndCollectPage(selector string, offset, maxItems int) (*ExtractAllResult, error) {
	return s.ScrollAndCollectPageContext(context.Background(), selector, offset, maxItems)
}

// ScrollAndCollectPageContext collects a bounded slice while honoring ctx for
// lock queueing, page setup, scrolling, and page-side waits.
func (s *Session) ScrollAndCollectPageContext(ctx context.Context, selector string, offset, maxItems int) (*ExtractAllResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	if maxItems <= 0 {
		maxItems = 100
	}
	if maxItems > 100 {
		maxItems = 100
	}
	if offset < 0 {
		offset = 0
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(async function() {
		const sel = %s;
		const offset = %d;
		const maxItems = %d;
		let lastCount = 0;
		let stableRounds = 0;
		const allItems = [];
		function selectorFor(el){
			const parts=[];let node=el;const doc=el.ownerDocument;
			while(node&&node.nodeType===1){
				if(node.id){const id='#'+CSS.escape(node.id);if(doc.querySelectorAll(id).length===1){parts.unshift(id);break}}
				let part=node.tagName.toLowerCase();const parent=node.parentElement;
				if(parent){const siblings=Array.from(parent.children).filter(child=>child.tagName===node.tagName);if(siblings.length>1)part+=':nth-of-type('+(siblings.indexOf(node)+1)+')'}
				parts.unshift(part);node=parent;
			}
			return parts.join(' > ');
		}
		function semanticsFor(el){
			const tag=el.tagName.toLowerCase();
			let role=(el.getAttribute('role')||'').trim().split(/\\s+/)[0];
			if(!role){
				if(tag==='a'&&el.hasAttribute('href'))role='link';
				else if(tag==='button'||tag==='summary')role='button';
				else if(tag==='textarea'||el.isContentEditable)role='textbox';
				else if(tag==='select')role=el.multiple?'listbox':'combobox';
				else if(tag==='input'){
					const inputRoles={button:'button',image:'button',submit:'button',reset:'button',checkbox:'checkbox',radio:'radio',range:'slider',number:'spinbutton',hidden:''};
					role=Object.prototype.hasOwnProperty.call(inputRoles,el.type)?inputRoles[el.type]:'textbox';
				}
			}
			const interactive=!!(role==='button'||role==='link'||role==='checkbox'||role==='radio'||role==='switch'||role==='menuitem'||role==='option'||role==='slider'||role==='spinbutton'||role==='textbox'||role==='combobox'||role==='listbox'||el.hasAttribute('onclick')||el.hasAttribute('tabindex'));
			return {tag,role,interactive};
		}

		for (let i = 0; i < 50; i++) {
			const items = document.querySelectorAll(sel);
			const currentCount = items.length;

			// Collect new items
			for (let j = lastCount; j < Math.min(currentCount, offset + maxItems); j++) {
				const el=items[j], semantics=semanticsFor(el);
				allItems.push({text:(el.innerText||el.textContent||'').trim().slice(0,500),selector:selectorFor(el),...semantics});
			}

			if (allItems.length >= offset + maxItems) break;

			if (currentCount === lastCount) {
				stableRounds++;
				if (stableRounds >= 3) break; // no new items after 3 scrolls
			} else {
				stableRounds = 0;
			}
			lastCount = currentCount;

			// Scroll down
			window.scrollBy(0, window.innerHeight);
			await new Promise(r => setTimeout(r, 1000));
		}

		return JSON.stringify({items: allItems.slice(offset, offset + maxItems), total: document.querySelectorAll(sel).length});
	})()`, selectorJSON, offset, maxItems)

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var data struct {
		Items []struct {
			Text        string `json:"text"`
			Selector    string `json:"selector"`
			Tag         string `json:"tag"`
			Role        string `json:"role"`
			Interactive bool   `json:"interactive"`
		} `json:"items"`
		Total int `json:"total"`
	}
	_ = json.Unmarshal([]byte(str), &data)
	items := make([]string, 0, len(data.Items))
	references := make([]ExtractedTextReference, 0, len(data.Items))
	for _, item := range data.Items {
		items = append(items, item.Text)
		references = append(references, ExtractedTextReference{Selector: item.Selector, Tag: item.Tag, Role: item.Role, Interactive: item.Interactive})
	}

	return &ExtractAllResult{
		Selector:     selector,
		ContentTrust: ContentTrustUntrustedPage,
		TargetID:     s.page.TargetID(),
		FrameID:      s.frameID,
		Count:        len(data.Items),
		Total:        data.Total,
		Offset:       offset,
		Limit:        maxItems,
		Truncated:    offset+len(data.Items) < data.Total,
		Items:        items,
		References:   references,
	}, nil
}
