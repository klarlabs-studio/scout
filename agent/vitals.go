package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

func rateMetric(value, goodThresh, poorThresh float64) string {
	if value <= goodThresh {
		return "good"
	}
	if value >= poorThresh {
		return "poor"
	}
	return "needs-improvement"
}

func overallRating(ratings ...string) string {
	worst := "good"
	for _, r := range ratings {
		if r == "poor" {
			return "poor"
		}
		if r == "needs-improvement" {
			worst = "needs-improvement"
		}
	}
	return worst
}

func (s *Session) WebVitals() (*WebVitalsResult, error) {
	return s.WebVitalsContext(context.Background())
}

// WebVitalsContext extracts performance metrics while honoring ctx for
// session queueing and browser evaluation.
func (s *Session) WebVitalsContext(ctx context.Context) (*WebVitalsResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}

	js := `(function() {
		const r = {lcp: 0, cls: 0, inp: 0, ttfb: 0, domContentLoaded: 0, firstPaint: 0};

		// LCP — last entry wins
		try {
			const lcpEntries = performance.getEntriesByType('largest-contentful-paint');
			if (lcpEntries.length > 0) {
				r.lcp = lcpEntries[lcpEntries.length - 1].startTime;
			}
		} catch(e) {}

		// CLS — sum layout shifts without recent input
		try {
			const clsEntries = performance.getEntriesByType('layout-shift');
			for (const entry of clsEntries) {
				if (!entry.hadRecentInput) {
					r.cls += entry.value;
				}
			}
		} catch(e) {}

		// INP — max event duration
		try {
			const eventEntries = performance.getEntriesByType('event');
			for (const entry of eventEntries) {
				if (entry.duration > r.inp) {
					r.inp = entry.duration;
				}
			}
		} catch(e) {}

		// Navigation Timing
		try {
			const nav = performance.getEntriesByType('navigation');
			if (nav.length > 0) {
				const n = nav[0];
				r.ttfb = n.responseStart - n.startTime;
				r.domContentLoaded = n.domContentLoadedEventEnd - n.startTime;
			} else if (performance.timing) {
				const t = performance.timing;
				r.ttfb = t.responseStart - t.navigationStart;
				r.domContentLoaded = t.domContentLoadedEventEnd - t.navigationStart;
			}
		} catch(e) {}

		// First Paint
		try {
			const paintEntries = performance.getEntriesByType('paint');
			for (const entry of paintEntries) {
				if (entry.name === 'first-paint') {
					r.firstPaint = entry.startTime;
					break;
				}
			}
		} catch(e) {}

		return JSON.stringify(r);
	})()`

	result, err := s.page.EvaluateContext(ctx, js)
	if err != nil {
		return nil, fmt.Errorf("web vitals extraction failed: %w", err)
	}

	str, ok := result.(string)
	if !ok {
		return nil, fmt.Errorf("web vitals returned unexpected type")
	}

	var raw struct {
		LCP              float64 `json:"lcp"`
		CLS              float64 `json:"cls"`
		INP              float64 `json:"inp"`
		TTFB             float64 `json:"ttfb"`
		DOMContentLoaded float64 `json:"domContentLoaded"`
		FirstPaint       float64 `json:"firstPaint"`
	}
	if err := json.Unmarshal([]byte(str), &raw); err != nil {
		return nil, fmt.Errorf("web vitals parse failed: %w", err)
	}

	lcpRating := rateMetric(raw.LCP, 2500, 4000)
	clsRating := rateMetric(raw.CLS, 0.1, 0.25)
	inpRating := rateMetric(raw.INP, 200, 500)

	return &WebVitalsResult{
		ContentTrust:     ContentTrustUntrustedPage,
		LCP:              raw.LCP,
		CLS:              raw.CLS,
		INP:              raw.INP,
		TTFB:             raw.TTFB,
		DOMContentLoaded: raw.DOMContentLoaded,
		FirstPaint:       raw.FirstPaint,
		LCPRating:        lcpRating,
		CLSRating:        clsRating,
		INPRating:        inpRating,
		OverallRating:    overallRating(lcpRating, clsRating, inpRating),
	}, nil
}
