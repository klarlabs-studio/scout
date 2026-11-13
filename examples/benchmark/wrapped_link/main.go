// Command wrapped_link checks that a click on a multi-line inline element uses
// a rendered fragment instead of the midpoint of its union bounding box.
//
// Run with: go run ./examples/benchmark/wrapped_link
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"time"

	browse "go.klarlabs.de/scout"
)

type report struct {
	Scenario             string               `json:"scenario"`
	ObservedAtUTC        string               `json:"observed_at_utc"`
	GoVersion            string               `json:"go_version"`
	Platform             string               `json:"platform"`
	BrowserUserAgent     string               `json:"browser_user_agent"`
	FragmentCount        int                  `json:"fragment_count"`
	Fragments            []map[string]float64 `json:"fragments,omitempty"`
	UnionCenter          map[string]float64   `json:"union_center,omitempty"`
	UnionCenterHit       string               `json:"union_center_hit,omitempty"`
	UnionCenterHitAnchor bool                 `json:"union_center_hit_anchor"`
	ClickHit             bool                 `json:"click_hit"`
	ResultText           string               `json:"result_text"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (retErr error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><main style="width:180px;font:16px/20px monospace"><p>123456789012 <a id="target" href="#" onclick="document.getElementById('result').textContent='CLICKED';return false">link wraps</a> end.</p><span id="result">WAITING</span></main>`)
	}))
	defer server.Close()

	engine := browse.New(browse.WithHeadless(true), browse.WithTimeout(15*time.Second), browse.WithAllowPrivateIPs(true))
	if err := engine.LaunchContext(ctx); err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := engine.CloseContext(closeCtx); err != nil && retErr == nil {
			retErr = fmt.Errorf("close browser: %w", err)
		}
	}()

	page, err := engine.NewPageContext(ctx)
	if err != nil {
		return fmt.Errorf("create page: %w", err)
	}
	defer page.CloseContext(context.Background())
	if err := page.NavigateContext(ctx, server.URL); err != nil {
		return fmt.Errorf("navigate to fixture: %w", err)
	}

	const inspectJS = `JSON.stringify((() => {
  const anchor = document.querySelector('#target');
  const rects = [...anchor.getClientRects()];
  const union = anchor.getBoundingClientRect();
  const hit = document.elementFromPoint(union.x + union.width / 2, union.y + union.height / 2);
  return {
    fragment_count: rects.length,
    fragments: rects.map(r => ({x:r.x,y:r.y,width:r.width,height:r.height})),
    union_center_hit_anchor: hit === anchor || anchor.contains(hit),
    union_center: {x:union.x + union.width / 2,y:union.y + union.height / 2},
    union_center_hit: hit ? hit.tagName + (hit.id ? '#' + hit.id : '') : null,
    browser_user_agent: navigator.userAgent
  };
})())`
	observed, err := page.EvaluateContext(ctx, inspectJS)
	if err != nil {
		return fmt.Errorf("inspect wrapped link: %w", err)
	}
	raw, ok := observed.(string)
	if !ok {
		return fmt.Errorf("inspect wrapped link returned %T, want JSON string", observed)
	}
	var result report
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return fmt.Errorf("decode wrapped-link observation: %w", err)
	}
	if result.FragmentCount < 2 {
		return fmt.Errorf("fixture did not wrap the link: got %d rendered fragments", result.FragmentCount)
	}
	if result.UnionCenterHitAnchor {
		return fmt.Errorf("fixture does not reproduce the union-box miss: midpoint=%v hit=%s fragments=%v", result.UnionCenter, result.UnionCenterHit, result.Fragments)
	}

	nodeID, err := page.QuerySelectorContext(ctx, "#target")
	if err != nil {
		return fmt.Errorf("find wrapped link: %w", err)
	}
	if err := browse.NewSelection(page, nodeID, "#target").ClickContext(ctx); err != nil {
		return fmt.Errorf("click wrapped link: %w", err)
	}
	clicked, err := page.EvaluateContext(ctx, `document.querySelector('#result').textContent`)
	if err != nil {
		return fmt.Errorf("read click result: %w", err)
	}
	result.ResultText, _ = clicked.(string)
	result.ClickHit = result.ResultText == "CLICKED"
	if !result.ClickHit {
		return fmt.Errorf("click did not activate wrapped link; result=%q", result.ResultText)
	}
	result.Scenario = "wrapped-inline-link-click"
	result.ObservedAtUTC = time.Now().UTC().Format(time.RFC3339)
	result.GoVersion = runtime.Version()
	result.Platform = runtime.GOOS + "/" + runtime.GOARCH
	return json.NewEncoder(os.Stdout).Encode(result)
}
