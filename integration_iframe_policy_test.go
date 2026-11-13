//go:build integration

package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/scout/internal/cdp"
)

func TestIntegrationDiscoversSiteIsolatedIframeTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>isolated-child</title><p>ready</p>`)
	}))
	defer child.Close()
	childURL := strings.Replace(child.URL, "127.0.0.1", "localhost", 1)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>oopif-parent</title><iframe src=%q></iframe>`, childURL)
	}))
	defer parent.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()
	page, err := engine.NewPageAt(parent.URL)
	if err != nil {
		t.Fatalf("NewPageAt: %v", err)
	}
	defer page.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		targets, err := engine.conn.GetTargetsCtx(ctx)
		if err != nil {
			t.Fatalf("GetTargets: %v", err)
		}
		for _, target := range targets {
			if target.Type == "iframe" && strings.TrimRight(target.URL, "/") == strings.TrimRight(childURL, "/") {
				if target.ParentFrameID != page.TargetID() {
					t.Fatalf("OOPIF target parentFrameId = %q, want page target %q: %+v", target.ParentFrameID, page.TargetID(), target)
				}
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	targets, err := engine.conn.GetTargetsCtx(ctx)
	if err != nil {
		t.Fatalf("GetTargets after timeout: %v", err)
	}
	t.Fatalf("Chrome did not expose an iframe target for %s; targets: %+v", childURL, targets)
}

func TestIntegrationAttachesToSiteIsolatedIframeTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>isolated-child</title><p id="ready">frame content</p>`)
	}))
	defer child.Close()
	childURL := strings.Replace(child.URL, "127.0.0.1", "localhost", 1)
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>oopif-parent</title><iframe src=%q></iframe>`, childURL)
	}))
	defer parent.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()
	page, err := engine.NewPageAt(parent.URL)
	if err != nil {
		t.Fatalf("NewPageAt: %v", err)
	}
	defer page.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var childTarget *cdp.TargetInfo
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		targets, err := engine.conn.GetTargetsCtx(ctx)
		if err != nil {
			t.Fatalf("GetTargets: %v", err)
		}
		for i := range targets {
			if targets[i].Type == "iframe" && strings.TrimRight(targets[i].URL, "/") == strings.TrimRight(childURL, "/") {
				childTarget = &targets[i]
				break
			}
		}
		if childTarget != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if childTarget == nil {
		t.Fatal("Chrome did not expose the expected site-isolated iframe target")
	}

	sessionID, err := engine.conn.AttachToTargetCtx(ctx, childTarget.TargetID)
	if err != nil {
		t.Fatalf("AttachToTarget: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
		defer cleanupCancel()
		if err := engine.conn.DetachFromTargetCtx(cleanupCtx, sessionID); err != nil {
			t.Errorf("DetachFromTarget: %v", err)
		}
	}()
	for _, domain := range []string{"Page", "DOM", "Runtime"} {
		if _, err := engine.conn.CallSessionCtx(ctx, sessionID, domain+".enable", nil); err != nil {
			t.Fatalf("%s.enable on OOPIF session: %v", domain, err)
		}
	}
	result, err := engine.conn.CallSessionCtx(ctx, sessionID, "Runtime.evaluate", map[string]any{
		"expression":    `({title: document.title, text: document.querySelector('#ready')?.textContent, frameId: location.href})`,
		"returnByValue": true,
	})
	if err != nil {
		t.Fatalf("Runtime.evaluate on OOPIF session: %v", err)
	}
	var evaluated struct {
		Result struct {
			Value struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &evaluated); err != nil {
		t.Fatalf("decode OOPIF evaluation: %v", err)
	}
	if evaluated.Result.Value.Title != "isolated-child" || evaluated.Result.Value.Text != "frame content" {
		t.Fatalf("OOPIF evaluation = %+v, want child document contents", evaluated.Result.Value)
	}
	document, err := engine.conn.CallSessionCtx(ctx, sessionID, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true})
	if err != nil {
		t.Fatalf("DOM.getDocument on OOPIF session: %v", err)
	}
	var domDocument struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(document, &domDocument); err != nil || domDocument.Root.NodeID == 0 {
		t.Fatalf("decode OOPIF DOM root node: node=%d err=%v", domDocument.Root.NodeID, err)
	}
	node, err := engine.conn.CallSessionCtx(ctx, sessionID, "DOM.querySelector", map[string]any{"nodeId": domDocument.Root.NodeID, "selector": "#ready"})
	if err != nil {
		t.Fatalf("DOM.querySelector on OOPIF session: %v", err)
	}
	var selected struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := json.Unmarshal(node, &selected); err != nil || selected.NodeID == 0 {
		t.Fatalf("decode OOPIF selected node: node=%d err=%v", selected.NodeID, err)
	}
	if _, err := engine.conn.CallSessionCtx(ctx, sessionID, "Runtime.evaluate", map[string]any{"expression": `document.querySelector('#ready').textContent = 'updated by frame session'`}); err != nil {
		t.Fatalf("mutate child through OOPIF session: %v", err)
	}
	updated, err := engine.conn.CallSessionCtx(ctx, sessionID, "Runtime.evaluate", map[string]any{"expression": `document.querySelector('#ready').textContent`, "returnByValue": true})
	if err != nil {
		t.Fatalf("verify child mutation: %v", err)
	}
	var updateValue struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(updated, &updateValue); err != nil || updateValue.Result.Value != "updated by frame session" {
		t.Fatalf("child mutation verification = %q, err=%v", updateValue.Result.Value, err)
	}
	parentTitle, err := page.Evaluate(`document.title`)
	if err != nil {
		t.Fatalf("parent after OOPIF attach: %v", err)
	}
	if parentTitle != "oopif-parent" {
		t.Fatalf("parent title after OOPIF attach = %v", parentTitle)
	}
}

// TestIntegrationAllowedOriginsCrossOriginIframes verifies that the page-level
// request policy handles both allowed and denied cross-origin iframe
// navigations without wedging later operations on the parent page.
func TestIntegrationAllowedOriginsCrossOriginIframes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	var allowedHits, deniedHits atomic.Int32
	allowedFrame := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		allowedHits.Add(1)
		fmt.Fprint(w, "allowed-frame")
	}))
	defer allowedFrame.Close()
	deniedFrame := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deniedHits.Add(1)
		fmt.Fprint(w, "denied-frame")
	}))
	defer deniedFrame.Close()

	// localhost and 127.0.0.1 are separate origins and sites in Chrome, while
	// both servers remain reachable through the test listener.
	allowedURL := allowedFrame.URL
	deniedURL := deniedFrame.URL
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>parent-ready</title><iframe src=%q></iframe><iframe src=%q></iframe>`, allowedURL, deniedURL)
	}))
	defer parent.Close()
	parentURL := strings.Replace(parent.URL, "127.0.0.1", "localhost", 1)

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true), WithAllowedOrigins(parentURL, allowedURL))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()
	page, err := engine.NewPageAt(parentURL)
	if err != nil {
		t.Fatalf("NewPageAt: %v", err)
	}
	defer page.Close()

	deadline := time.Now().Add(5 * time.Second)
	for allowedHits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if hits := allowedHits.Load(); hits == 0 {
		t.Fatal("allowed cross-origin iframe did not reach its server")
	}
	if hits := deniedHits.Load(); hits != 0 {
		t.Fatalf("disallowed cross-origin iframe reached its server %d times", hits)
	}
	result, err := page.Evaluate(`document.title`)
	if err != nil {
		t.Fatalf("read parent after iframe policy decisions: %v", err)
	}
	if title, _ := result.(string); title != "parent-ready" {
		t.Fatalf("parent page was not usable after iframe policy decisions: title=%q", title)
	}
}
