package agent

import (
	"encoding/json"
	"fmt"
)

// ExtractedPattern represents a repeating pattern found on the page (product cards, list items, etc).
type ExtractedPattern struct {
	Pattern string              `json:"pattern"` // CSS selector for the repeating container
	Count   int                 `json:"count"`   // number of items found
	Fields  []string            `json:"fields"`  // detected field names
	Items   []map[string]string `json:"items"`   // extracted structured data
}

// AutoExtract detects repeating patterns on the page and extracts structured data.
// Works without selectors — finds product cards, search results, list items, table rows automatically.
func (s *Session) AutoExtract() (*ExtractedPattern, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	maxItems := s.contentOpts.MaxItems
	if maxItems == 0 {
		maxItems = 50
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
		const fieldSet = new Set();

		for (const item of best.items.slice(0, %d)) {
			const data = {};

			// Extract text from semantic elements
			const title = item.querySelector('h1,h2,h3,h4,h5,a[href],.title,.name,[class*="title"],[class*="name"]');
			if (title) { data.title = title.textContent.trim().slice(0, 200); fieldSet.add('title'); }

			const price = item.querySelector('.price,[class*="price"],[data-price]');
			if (price) { data.price = price.textContent.trim(); fieldSet.add('price'); }

			const link = item.querySelector('a[href]');
			if (link) { data.link = link.getAttribute('href'); fieldSet.add('link'); }

			const img = item.querySelector('img[src]');
			if (img) { data.image = img.getAttribute('src'); fieldSet.add('image'); }

			const desc = item.querySelector('p,.description,[class*="desc"],[class*="summary"]');
			if (desc) { data.description = desc.textContent.trim().slice(0, 300); fieldSet.add('description'); }

			const rating = item.querySelector('[class*="rating"],[class*="star"],[aria-label*="star"]');
			if (rating) { data.rating = rating.textContent.trim() || rating.getAttribute('aria-label') || ''; fieldSet.add('rating'); }

			// Fallback: if no semantic fields found, use full text
			if (Object.keys(data).length === 0) {
				data.text = item.textContent.trim().slice(0, 300);
				fieldSet.add('text');
			}

			items.push(data);
		}

		// Build selector for the pattern
		let selector = best.parent.tagName.toLowerCase();
		if (best.parent.id) selector = '#' + best.parent.id;
		else if (best.parent.className) selector += '.' + best.parent.className.split(' ')[0];

		return JSON.stringify({
			pattern: selector,
			count: best.items.length,
			fields: Array.from(fieldSet),
			items: items
		});
	})()`, maxItems)

	result, err := s.page.Evaluate(js)
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
	return &pattern, nil
}

// ScrollAndCollect auto-scrolls the page and collects items as they lazy-load.
// Scrolls until no new items appear or maxItems is reached.
func (s *Session) ScrollAndCollect(selector string, maxItems int) (*ExtractAllResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	if maxItems <= 0 {
		maxItems = 100
	}

	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(async function() {
		const sel = %s;
		const maxItems = %d;
		let lastCount = 0;
		let stableRounds = 0;
		const allTexts = [];

		for (let i = 0; i < 50; i++) {
			const items = document.querySelectorAll(sel);
			const currentCount = items.length;

			// Collect new items
			for (let j = lastCount; j < Math.min(currentCount, maxItems); j++) {
				allTexts.push(items[j].textContent.trim().slice(0, 500));
			}

			if (allTexts.length >= maxItems) break;

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

		return JSON.stringify({items: allTexts, total: document.querySelectorAll(sel).length});
	})()`, selectorJSON, maxItems)

	result, err := s.page.Evaluate(js)
	if err != nil {
		return nil, err
	}

	str, _ := result.(string)
	var data struct {
		Items []string `json:"items"`
		Total int      `json:"total"`
	}
	_ = json.Unmarshal([]byte(str), &data)

	return &ExtractAllResult{
		Selector:  selector,
		Count:     len(data.Items),
		Total:     data.Total,
		Truncated: len(data.Items) < data.Total,
		Items:     data.Items,
	}, nil
}
