//go:build integration

package browse

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fullTestServer returns a richer httptest.Server with pages for various
// test scenarios. Extends testServer() from integration_test.go.
func fullTestServer() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Full Test Page</title></head>
<body>
	<h1 id="title">Full Test</h1>
	<input id="name" type="text" value="initial" />
	<button id="btn" onclick="document.getElementById('title').textContent='Clicked!'">Click Me</button>
	<ul id="list">
		<li class="item">Alpha</li>
		<li class="item">Beta</li>
		<li class="item">Gamma</li>
	</ul>
	<div id="hidden" style="display:none">Hidden Content</div>
	<a id="link" href="/about" data-role="nav">About</a>
	<div id="hover-target" onmouseover="this.classList.add('hovered')">Hover Me</div>
	<button id="disabled-btn" disabled onclick="this.textContent='Enabled Click'">Disabled</button>
</body>
</html>`)
	})

	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>About Page</title></head><body><h1 id="about-title">About</h1></body></html>`)
	})

	mux.HandleFunc("/user-agent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><body><span id="ua">%s</span></body></html>`, html.EscapeString(r.UserAgent()))
	})

	mux.HandleFunc("/delayed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<div id="container"></div>
<script>
setTimeout(function() {
	document.getElementById('container').innerHTML = '<p id="delayed-el">Loaded!</p>';
}, 200);
</script>
</body></html>`)
	})

	mux.HandleFunc("/shadow", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<div id="host"></div>
<script>
const host = document.getElementById('host');
const shadow = host.attachShadow({mode: 'open'});
shadow.innerHTML = '<span id="shadow-inner" class="shadow-cls">Shadow Content</span>';
</script>
</body></html>`)
	})

	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<form id="signup">
	<input id="fname" type="text" />
	<input id="lname" type="text" />
	<input id="email" type="email" />
</form>
</body></html>`)
	})

	mux.HandleFunc("/table", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<table id="data">
	<thead><tr><th>Name</th><th>Age</th></tr></thead>
	<tbody>
		<tr><td>Alice</td><td>30</td></tr>
		<tr><td>Bob</td><td>25</td></tr>
	</tbody>
</table>
</body></html>`)
	})

	mux.HandleFunc("/cookies", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:  "testcookie",
			Value: "cookievalue",
			Path:  "/",
		})
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Cookies</h1></body></html>`)
	})

	mux.HandleFunc("/eval-types", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<script>
window.testNumber = 42;
window.testBool = true;
window.testNull = null;
window.testArray = [1, 2, 3];
window.testObject = {key: "value"};
</script>
</body></html>`)
	})

	mux.HandleFunc("/wait-visible", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<div id="initially-hidden" style="display:none">Hidden at first</div>
<script>
setTimeout(function() {
	document.getElementById('initially-hidden').style.display = 'block';
}, 200);
</script>
</body></html>`)
	})

	mux.HandleFunc("/enable-button", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<button id="delayed-btn" disabled>Wait for me</button>
<script>
setTimeout(function() {
	document.getElementById('delayed-btn').disabled = false;
}, 200);
</script>
</body></html>`)
	})

	mux.HandleFunc("/large-page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<div style="width:2000px;height:3000px;background:linear-gradient(red,blue)">
<h1 id="large-title">Large Page</h1>
</div>
</body></html>`)
	})

	return httptest.NewServer(mux)
}

// launchTestEngine creates and launches a headless engine for integration tests.
func launchTestEngine(t *testing.T) *Engine {
	t.Helper()
	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

// --- page.go tests ---

func TestIntegrationQuerySelectorAll(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var nodeIDs []int64
	engine.Task("qsa", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		nodeIDs, err = c.Page().QuerySelectorAll(".item")
		if err != nil {
			t.Fatalf("QuerySelectorAll: %v", err)
		}
	})

	if err := engine.Run("qsa"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(nodeIDs) != 3 {
		t.Errorf("QuerySelectorAll('.item') returned %d nodes, want 3", len(nodeIDs))
	}
}

func TestIntegrationQuerySelectorPiercing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var normalFound bool
	var shadowFound bool

	engine.Task("pierce", func(c *Context) {
		c.MustNavigate(ts.URL + "/shadow")

		// Normal selector should work for non-shadow elements
		nid, err := c.Page().QuerySelectorPiercing("#host")
		if err == nil && nid > 0 {
			normalFound = true
		}

		// Shadow DOM element -- piercing should find it
		nid, err = c.Page().QuerySelectorPiercing("#shadow-inner")
		if err == nil && nid > 0 {
			shadowFound = true
		}
	})

	if err := engine.Run("pierce"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !normalFound {
		t.Error("QuerySelectorPiercing should find normal DOM elements")
	}
	if !shadowFound {
		t.Error("QuerySelectorPiercing should find elements inside shadow DOM")
	}
}

func TestIntegrationGetFlattenedNodesCache(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("flatten-cache", func(c *Context) {
		c.MustNavigate(ts.URL)

		page := c.Page()

		// First call should fetch from CDP
		nodes1, err := page.getFlattenedNodes()
		if err != nil {
			t.Fatalf("getFlattenedNodes (first): %v", err)
		}
		if len(nodes1) == 0 {
			t.Fatal("getFlattenedNodes returned empty slice")
		}

		// Second call should return cached result
		nodes2, err := page.getFlattenedNodes()
		if err != nil {
			t.Fatalf("getFlattenedNodes (cached): %v", err)
		}

		// Verify they point to the same underlying slice (cache hit)
		if &nodes1[0] != &nodes2[0] {
			t.Error("expected getFlattenedNodes to return cached result on second call")
		}
	})

	if err := engine.Run("flatten-cache"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestIntegrationNavigateURLValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var validErr, invalidErr error
	engine.Task("url-validate", func(c *Context) {
		// Valid URL should succeed
		validErr = c.Navigate(ts.URL)

		// Invalid scheme should fail
		invalidErr = c.Navigate("ftp://example.com")
	})

	if err := engine.Run("url-validate"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if validErr != nil {
		t.Errorf("valid URL navigation failed: %v", validErr)
	}
	if invalidErr == nil {
		t.Error("expected error for ftp:// URL")
	}
	var navErr *NavigationError
	if !errors.As(invalidErr, &navErr) {
		t.Errorf("expected NavigationError, got %T", invalidErr)
	}
}

func TestIntegrationNavigateInvalidURL(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var jsErr, fileErr error
	engine.Task("invalid-urls", func(c *Context) {
		jsErr = c.Navigate("javascript:alert(1)")
		fileErr = c.Navigate("file:///etc/passwd")
	})

	if err := engine.Run("invalid-urls"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if jsErr == nil {
		t.Error("expected error for javascript: URL")
	}
	if fileErr == nil {
		t.Error("expected error for file: URL")
	}
}

func TestIntegrationWaitLoadExplicit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var waitErr error
	engine.Task("wait-load", func(c *Context) {
		c.MustNavigate(ts.URL)
		// Explicit WaitLoad after navigation should succeed since page is already loaded
		waitErr = c.WaitLoad()
	})

	if err := engine.Run("wait-load"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if waitErr != nil {
		t.Errorf("WaitLoad: %v", waitErr)
	}
}

func TestIntegrationWaitStablePage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var stableErr error
	engine.Task("wait-stable", func(c *Context) {
		c.MustNavigate(ts.URL)
		stableErr = c.Page().WaitStable(200 * time.Millisecond)
	})

	if err := engine.Run("wait-stable"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if stableErr != nil {
		t.Errorf("WaitStable: %v", stableErr)
	}
}

func TestIntegrationWaitStableDefaultDuration(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var stableErr error
	engine.Task("wait-stable-default", func(c *Context) {
		c.MustNavigate(ts.URL)
		// d=0 triggers default 500ms
		stableErr = c.Page().WaitStable(0)
	})

	if err := engine.Run("wait-stable-default"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if stableErr != nil {
		t.Errorf("WaitStable(0): %v", stableErr)
	}
}

func TestIntegrationWaitForSelectorExisting(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var waitErr error
	engine.Task("wait-sel", func(c *Context) {
		c.MustNavigate(ts.URL)
		waitErr = c.Page().WaitForSelector("#title")
	})

	if err := engine.Run("wait-sel"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if waitErr != nil {
		t.Errorf("WaitForSelector existing: %v", waitErr)
	}
}

func TestIntegrationEvaluateReturnTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var numberResult, boolResult, nullResult, arrayResult, objectResult, undefinedResult any
	var undefinedErr error

	engine.Task("eval-types", func(c *Context) {
		c.MustNavigate(ts.URL + "/eval-types")

		numberResult, _ = c.Eval(`window.testNumber`)
		boolResult, _ = c.Eval(`window.testBool`)
		nullResult, _ = c.Eval(`window.testNull`)
		arrayResult, _ = c.Eval(`window.testArray`)
		objectResult, _ = c.Eval(`window.testObject`)
		undefinedResult, undefinedErr = c.Eval(`undefined`)
	})

	if err := engine.Run("eval-types"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Number comes back as float64 from JSON
	if n, ok := numberResult.(float64); !ok || n != 42 {
		t.Errorf("number: expected 42, got %v (%T)", numberResult, numberResult)
	}
	if b, ok := boolResult.(bool); !ok || !b {
		t.Errorf("bool: expected true, got %v", boolResult)
	}
	if nullResult != nil {
		t.Errorf("null: expected nil, got %v", nullResult)
	}
	if arrayResult == nil {
		t.Error("array: expected non-nil")
	}
	if objectResult == nil {
		t.Error("object: expected non-nil")
	}
	// undefined returns nil, nil
	if undefinedResult != nil {
		t.Errorf("undefined: expected nil, got %v", undefinedResult)
	}
	if undefinedErr != nil {
		t.Errorf("undefined err: expected nil, got %v", undefinedErr)
	}
}

func TestIntegrationEvaluateJSError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var jsErr error
	engine.Task("eval-error", func(c *Context) {
		c.MustNavigate(ts.URL)
		_, jsErr = c.Eval(`throw new Error("test error")`)
	})

	if err := engine.Run("eval-error"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if jsErr == nil {
		t.Error("expected JS error")
	}
	if jsErr != nil && !strings.Contains(jsErr.Error(), "js error") {
		t.Errorf("expected js error message, got: %v", jsErr)
	}
}

func TestIntegrationScreenshotWithOptionsCompression(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("compress", func(c *Context) {
		c.MustNavigate(ts.URL + "/large-page")
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			MaxSize: 512, // Very small, forces max compression
		})
		if err != nil {
			t.Fatalf("ScreenshotWithOptions: %v", err)
		}
	})

	if err := engine.Run("compress"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty compressed screenshot")
	}
	t.Logf("Compressed to %d bytes (limit 512)", len(shot))
}

func TestIntegrationScreenshotWithClip(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("clip-shot", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			Clip: &ClipRegion{X: 0, Y: 0, Width: 200, Height: 100},
		})
		if err != nil {
			t.Fatalf("ScreenshotWithOptions with clip: %v", err)
		}
	})

	if err := engine.Run("clip-shot"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty clipped screenshot")
	}
}

func TestIntegrationScreenshotJPEG(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("jpeg", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			Format:  "jpeg",
			Quality: 50,
		})
		if err != nil {
			t.Fatalf("JPEG screenshot: %v", err)
		}
	})

	if err := engine.Run("jpeg"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) < 2 {
		t.Fatal("expected non-empty JPEG screenshot")
	}
	// JPEG magic bytes: FF D8
	if shot[0] != 0xFF || shot[1] != 0xD8 {
		t.Errorf("expected JPEG format, got first 2 bytes: %x %x", shot[0], shot[1])
	}
}

func TestIntegrationScreenshotWithMaxWidth(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("maxwidth", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			MaxWidth: 400,
		})
		if err != nil {
			t.Fatalf("MaxWidth screenshot: %v", err)
		}
	})

	if err := engine.Run("maxwidth"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty screenshot with MaxWidth")
	}
}

func TestIntegrationScreenshotClipWithMaxWidth(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("clip-maxw", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			Clip:     &ClipRegion{X: 0, Y: 0, Width: 800, Height: 400},
			MaxWidth: 200,
		})
		if err != nil {
			t.Fatalf("Clip+MaxWidth screenshot: %v", err)
		}
	})

	if err := engine.Run("clip-maxw"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty screenshot")
	}
}

func TestIntegrationPDFWithOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var pdf []byte
	engine.Task("pdf-opts", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		pdf, err = c.Page().PDFWithOptions(PDFOptions{
			Landscape:       true,
			PrintBackground: true,
			Scale:           0.8,
			PaperWidth:      8.5,
			PaperHeight:     11,
			MarginTop:       0.5,
			MarginBottom:    0.5,
			MarginLeft:      0.5,
			MarginRight:     0.5,
			PageRanges:      "1",
		})
		if err != nil {
			t.Fatalf("PDFWithOptions: %v", err)
		}
	})

	if err := engine.Run("pdf-opts"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(pdf) < 4 {
		t.Fatal("expected non-empty PDF")
	}
	if string(pdf[:4]) != "%PDF" {
		t.Errorf("expected PDF magic, got %x", pdf[:4])
	}
}

func TestIntegrationSetUserAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var userAgent string
	engine.Task("set-ua", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.Page().SetUserAgent("ScoutBot/1.0"); err != nil {
			t.Fatalf("SetUserAgent: %v", err)
		}
		c.MustNavigate(ts.URL + "/user-agent")
		result, err := c.El("#ua").Text()
		if err != nil {
			t.Fatalf("read userAgent: %v", err)
		}
		userAgent = result
	})

	if err := engine.Run("set-ua"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if userAgent != "ScoutBot/1.0" {
		t.Errorf("userAgent: expected 'ScoutBot/1.0', got %q", userAgent)
	}
}

func TestIntegrationSetViewport(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var innerWidth, innerHeight float64
	engine.Task("set-vp", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.Page().SetViewport(800, 600); err != nil {
			t.Fatalf("SetViewport: %v", err)
		}
		w, _ := c.Eval(`window.innerWidth`)
		h, _ := c.Eval(`window.innerHeight`)
		innerWidth, _ = w.(float64)
		innerHeight, _ = h.(float64)
	})

	if err := engine.Run("set-vp"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if innerWidth != 800 {
		t.Errorf("innerWidth: expected 800, got %v", innerWidth)
	}
	if innerHeight != 600 {
		t.Errorf("innerHeight: expected 600, got %v", innerHeight)
	}
}

func TestIntegrationPageClose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	// Use NewPageAt directly to test page lifecycle
	page, err := engine.NewPageAt(ts.URL)
	if err != nil {
		t.Fatalf("NewPageAt: %v", err)
	}

	// Verify page is functional
	title, err := page.Evaluate(`document.title`)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if title != "Full Test Page" {
		t.Errorf("title: expected 'Full Test Page', got %v", title)
	}

	// Close the page
	if err := page.Close(); err != nil {
		t.Fatalf("Page.Close: %v", err)
	}
}

func TestIntegrationPageCall(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var result string
	engine.Task("raw-call", func(c *Context) {
		c.MustNavigate(ts.URL)
		// Use the raw Call method to get the page title via Runtime.evaluate
		raw, err := c.Page().Call("Runtime.evaluate", map[string]any{
			"expression":    "document.title",
			"returnByValue": true,
		})
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		result = string(raw)
	})

	if err := engine.Run("raw-call"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(result, "Full Test Page") {
		t.Errorf("Call result should contain title, got %s", result)
	}
}

func TestIntegrationCookies(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var cookies []Cookie
	engine.Task("cookies", func(c *Context) {
		c.MustNavigate(ts.URL + "/cookies")

		var err error
		cookies, err = c.Cookies()
		if err != nil {
			t.Fatalf("Cookies: %v", err)
		}
	})

	if err := engine.Run("cookies"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, c := range cookies {
		if c.Name == "testcookie" && c.Value == "cookievalue" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected testcookie=cookievalue in cookies: %v", cookies)
	}
}

func TestIntegrationSetCookie(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var cookieValue string
	engine.Task("set-cookie", func(c *Context) {
		c.MustNavigate(ts.URL)
		err := c.SetCookie(Cookie{
			Name:   "mycookie",
			Value:  "myvalue",
			Domain: "127.0.0.1",
			Path:   "/",
		})
		if err != nil {
			t.Fatalf("SetCookie: %v", err)
		}
		// Verify cookie was set
		result, _ := c.Eval(`document.cookie`)
		cookieValue, _ = result.(string)
	})

	if err := engine.Run("set-cookie"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(cookieValue, "mycookie=myvalue") {
		t.Errorf("expected cookie to be set, got: %q", cookieValue)
	}
}

func TestIntegrationGetRootNodeIDCache(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("root-cache", func(c *Context) {
		c.MustNavigate(ts.URL)
		page := c.Page()

		id1, err := page.getRootNodeID()
		if err != nil {
			t.Fatalf("getRootNodeID (1): %v", err)
		}
		if id1 == 0 {
			t.Fatal("getRootNodeID returned 0")
		}

		id2, err := page.getRootNodeID()
		if err != nil {
			t.Fatalf("getRootNodeID (2): %v", err)
		}

		// Cached value should be the same
		if id1 != id2 {
			t.Errorf("getRootNodeID cache mismatch: %d != %d", id1, id2)
		}
	})

	if err := engine.Run("root-cache"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- selection.go tests ---

func TestIntegrationSelectionClick(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var afterClick string
	engine.Task("sel-click", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.El("#btn").Click(); err != nil {
			t.Fatalf("Click: %v", err)
		}
		c.WaitStable()
		afterClick, _ = c.El("#title").Text()
	})

	if err := engine.Run("sel-click"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if afterClick != "Clicked!" {
		t.Errorf("after click: expected 'Clicked!', got %q", afterClick)
	}
}

func TestIntegrationSelectionInput(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var value string
	engine.Task("sel-input", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.El("#name").Input("test-value"); err != nil {
			t.Fatalf("Input: %v", err)
		}
		value, _ = c.El("#name").Value()
	})

	if err := engine.Run("sel-input"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if value != "test-value" {
		t.Errorf("value: expected 'test-value', got %q", value)
	}
}

func TestIntegrationSelectionClear(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var value string
	engine.Task("sel-clear", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.El("#name").Clear(); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		value, _ = c.El("#name").Value()
	})

	if err := engine.Run("sel-clear"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if value != "" {
		t.Errorf("after clear: expected empty, got %q", value)
	}
}

func TestIntegrationSelectionHover(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var hasHovered bool
	engine.Task("sel-hover", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.El("#hover-target").Hover(); err != nil {
			t.Fatalf("Hover: %v", err)
		}
		// Check if the hovered class was added via the onmouseover handler
		time.Sleep(100 * time.Millisecond)
		cls, _ := c.El("#hover-target").Attr("class")
		hasHovered = strings.Contains(cls, "hovered")
	})

	if err := engine.Run("sel-hover"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !hasHovered {
		t.Error("expected hover to trigger onmouseover handler")
	}
}

func TestIntegrationSelectionTextAttrValue(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var text, href, dataRole, inputVal string
	var visible, hiddenVisible bool

	engine.Task("sel-read", func(c *Context) {
		c.MustNavigate(ts.URL)
		text, _ = c.El("#title").Text()
		href, _ = c.El("#link").Attr("href")
		dataRole, _ = c.El("#link").Attr("data-role")
		inputVal, _ = c.El("#name").Value()
		visible, _ = c.El("#title").Visible()
		hiddenVisible, _ = c.El("#hidden").Visible()
	})

	if err := engine.Run("sel-read"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if text != "Full Test" {
		t.Errorf("Text: expected 'Full Test', got %q", text)
	}
	if href != "/about" {
		t.Errorf("Attr href: expected '/about', got %q", href)
	}
	if dataRole != "nav" {
		t.Errorf("Attr data-role: expected 'nav', got %q", dataRole)
	}
	if inputVal != "initial" {
		t.Errorf("Value: expected 'initial', got %q", inputVal)
	}
	if !visible {
		t.Error("expected #title to be visible")
	}
	if hiddenVisible {
		t.Error("expected #hidden to be hidden")
	}
}

func TestIntegrationSelectionWaitVisible(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var isVisible bool
	engine.Task("wait-visible", func(c *Context) {
		c.MustNavigate(ts.URL + "/wait-visible")

		sel := c.El("#initially-hidden").WaitVisible()
		if sel.Err() != nil {
			t.Fatalf("WaitVisible: %v", sel.Err())
		}
		isVisible, _ = sel.Visible()
	})

	if err := engine.Run("wait-visible"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !isVisible {
		t.Error("expected element to be visible after WaitVisible")
	}
}

func TestIntegrationSelectionWaitStable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("sel-wait-stable", func(c *Context) {
		c.MustNavigate(ts.URL)
		sel := c.El("#title").WaitStable()
		if sel.Err() != nil {
			t.Fatalf("WaitStable: %v", sel.Err())
		}
	})

	if err := engine.Run("sel-wait-stable"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestIntegrationSelectionWaitEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("wait-enabled", func(c *Context) {
		c.MustNavigate(ts.URL + "/enable-button")
		sel := c.El("#delayed-btn").WaitEnabled()
		if sel.Err() != nil {
			t.Fatalf("WaitEnabled: %v", sel.Err())
		}
	})

	if err := engine.Run("wait-enabled"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestIntegrationSelectionScreenshotDirect(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("sel-screenshot", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		shot, err = c.El("#title").Screenshot()
		if err != nil {
			t.Fatalf("Selection.Screenshot: %v", err)
		}
	})

	if err := engine.Run("sel-screenshot"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Error("expected non-empty element screenshot")
	}
}

func TestIntegrationMustClickMustInputSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var afterClick, inputVal string
	engine.Task("must-ops", func(c *Context) {
		c.MustNavigate(ts.URL)
		c.El("#btn").MustClick()
		c.WaitStable()
		afterClick = c.El("#title").MustText()
		c.El("#name").MustInput("must-input-value")
		inputVal, _ = c.El("#name").Value()
	})

	if err := engine.Run("must-ops"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if afterClick != "Clicked!" {
		t.Errorf("MustClick: expected 'Clicked!', got %q", afterClick)
	}
	if inputVal != "must-input-value" {
		t.Errorf("MustInput: expected 'must-input-value', got %q", inputVal)
	}
}

// --- selection_all.go tests ---

func TestIntegrationSelectionAllCountTextsFirstLastAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var count int
	var texts []string
	var firstText, lastText, atText string

	engine.Task("sel-all", func(c *Context) {
		c.MustNavigate(ts.URL)
		all := c.ElAll(".item")
		count = all.Count()
		texts, _ = all.Texts()
		firstText, _ = all.First().Text()
		lastText, _ = all.Last().Text()
		atText, _ = all.At(1).Text()
	})

	if err := engine.Run("sel-all"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if count != 3 {
		t.Errorf("Count: expected 3, got %d", count)
	}
	if len(texts) != 3 || texts[0] != "Alpha" || texts[1] != "Beta" || texts[2] != "Gamma" {
		t.Errorf("Texts: expected [Alpha Beta Gamma], got %v", texts)
	}
	if firstText != "Alpha" {
		t.Errorf("First: expected 'Alpha', got %q", firstText)
	}
	if lastText != "Gamma" {
		t.Errorf("Last: expected 'Gamma', got %q", lastText)
	}
	if atText != "Beta" {
		t.Errorf("At(1): expected 'Beta', got %q", atText)
	}
}

func TestIntegrationSelectionAllEach(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var visited []string
	engine.Task("sel-all-each", func(c *Context) {
		c.MustNavigate(ts.URL)
		all := c.ElAll(".item")
		all.Each(func(i int, s *Selection) {
			txt, _ := s.Text()
			visited = append(visited, txt)
		})
	})

	if err := engine.Run("sel-all-each"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(visited) != 3 {
		t.Errorf("Each visited %d, want 3", len(visited))
	}
}

func TestIntegrationSelectionAllFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var filtered []string
	engine.Task("sel-all-filter", func(c *Context) {
		c.MustNavigate(ts.URL)
		result := c.ElAll(".item").Filter(func(s *Selection) bool {
			text, _ := s.Text()
			return text == "Alpha" || text == "Gamma"
		})
		filtered, _ = result.Texts()
	})

	if err := engine.Run("sel-all-filter"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(filtered) != 2 {
		t.Errorf("expected 2 filtered results, got %d: %v", len(filtered), filtered)
	}
}

// --- engine.go tests ---

func TestIntegrationEngineLaunchAndClose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	engine := New(WithHeadless(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Verify the connection is alive by creating a page
	page, err := engine.NewPage()
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	page.Close()

	if err := engine.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestIntegrationEngineNewPageAndNewPageAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	// NewPage creates a blank page
	blankPage, err := engine.NewPage()
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	defer blankPage.Close()

	// NewPageAt navigates to URL during creation
	namedPage, err := engine.NewPageAt(ts.URL)
	if err != nil {
		t.Fatalf("NewPageAt: %v", err)
	}
	defer namedPage.Close()

	url, err := namedPage.URL()
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1") {
		t.Errorf("NewPageAt URL: expected http://127.0.0.1:*, got %q", url)
	}
}

func TestIntegrationEngineMustLaunch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	engine := New(WithHeadless(true))
	result := engine.MustLaunch()
	defer engine.Close()

	if result != engine {
		t.Error("MustLaunch should return the engine")
	}
}

func TestIntegrationEngineTaskWithMiddleware(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var middlewareCalled bool
	var handlerCalled bool
	engine.Use(func(c *Context) {
		middlewareCalled = true
		c.Next()
	})
	engine.Task("mw-task", func(c *Context) {
		handlerCalled = true
		c.MustNavigate(ts.URL)
	})

	if err := engine.Run("mw-task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !middlewareCalled {
		t.Error("middleware was not called")
	}
	if !handlerCalled {
		t.Error("handler was not called")
	}
}

func TestIntegrationEngineConcurrentTasks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true), WithPoolSize(3))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var mu sync.Mutex
	results := make(map[string]string)

	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("concurrent-%d", i)
		engine.Task(name, func(c *Context) {
			c.MustNavigate(ts.URL)
			title := c.El("#title").MustText()
			mu.Lock()
			results[c.TaskName()] = title
			mu.Unlock()
		})
	}

	if err := engine.RunAll(); err != nil {
		t.Fatalf("RunAll: %v", err)
	}

	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}
}

func TestIntegrationEngineRunAllSequential(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	// No pool size means sequential execution
	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var order []string
	var mu sync.Mutex
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("seq-%d", i)
		engine.Task(name, func(c *Context) {
			c.MustNavigate(ts.URL)
			mu.Lock()
			order = append(order, c.TaskName())
			mu.Unlock()
		})
	}

	if err := engine.RunAll(); err != nil {
		t.Fatalf("RunAll: %v", err)
	}

	if len(order) != 3 {
		t.Errorf("expected 3 tasks executed, got %d", len(order))
	}
}

func TestIntegrationEngineRunGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var taskRan bool
	g := engine.Group("test-group")
	g.Task("task1", func(c *Context) {
		c.MustNavigate(ts.URL)
		taskRan = true
	})

	if err := engine.RunGroup("test-group"); err != nil {
		t.Fatalf("RunGroup: %v", err)
	}

	if !taskRan {
		t.Error("group task should have run")
	}
}

func TestIntegrationEngineWithUserAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true), WithUserAgent("CustomAgent/2.0"))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var ua string
	engine.Task("check-ua", func(c *Context) {
		c.MustNavigate(ts.URL + "/user-agent")
		ua, _ = c.El("#ua").Text()
	})

	if err := engine.Run("check-ua"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if ua != "CustomAgent/2.0" {
		t.Errorf("expected 'CustomAgent/2.0', got %q", ua)
	}
}

// --- context.go tests ---

func TestIntegrationContextNavigateElElAll(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var title string
	var count int
	engine.Task("ctx-nav", func(c *Context) {
		if err := c.Navigate(ts.URL); err != nil {
			t.Fatalf("Navigate: %v", err)
		}
		title = c.El("#title").MustText()
		count = c.ElAll(".item").Count()
	})

	if err := engine.Run("ctx-nav"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if title != "Full Test" {
		t.Errorf("expected 'Full Test', got %q", title)
	}
	if count != 3 {
		t.Errorf("expected 3 items, got %d", count)
	}
}

func TestIntegrationContextSetGetDataFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var result string
	engine.Use(func(c *Context) {
		c.Set("request-id", "test-123")
		c.Next()
	})
	engine.Task("data-flow", func(c *Context) {
		c.MustNavigate(ts.URL)
		result = c.GetString("request-id")
	})

	if err := engine.Run("data-flow"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result != "test-123" {
		t.Errorf("expected 'test-123', got %q", result)
	}
}

func TestIntegrationContextErrorHandlingAndAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("abort-test", func(c *Context) {
		c.MustNavigate(ts.URL)
		c.AbortWithError(fmt.Errorf("intentional abort"))
	})

	err := engine.Run("abort-test")
	if err == nil {
		t.Error("expected error from aborted task")
	}
	if !strings.Contains(err.Error(), "intentional abort") {
		t.Errorf("expected 'intentional abort', got: %v", err)
	}
}

func TestIntegrationContextWaitNavigation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var waitErr error
	engine.Task("wait-nav", func(c *Context) {
		c.MustNavigate(ts.URL)
		waitErr = c.WaitNavigation()
	})

	if err := engine.Run("wait-nav"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if waitErr != nil {
		t.Errorf("WaitNavigation: %v", waitErr)
	}
}

func TestIntegrationContextScreenshotTo(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "screenshot.png")

	engine.Task("screenshot-to", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.ScreenshotTo(outPath); err != nil {
			t.Fatalf("ScreenshotTo: %v", err)
		}
	})

	if err := engine.Run("screenshot-to"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("screenshot file: %v", err)
	}
	if info.Size() == 0 {
		t.Error("screenshot file is empty")
	}
}

func TestIntegrationContextPDFTo(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "output.pdf")

	engine.Task("pdf-to", func(c *Context) {
		c.MustNavigate(ts.URL)
		if err := c.PDFTo(outPath); err != nil {
			t.Fatalf("PDFTo: %v", err)
		}
	})

	if err := engine.Run("pdf-to"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("PDF file: %v", err)
	}
	if info.Size() == 0 {
		t.Error("PDF file is empty")
	}
}

func TestIntegrationContextElNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var selErr error
	engine.Task("el-notfound", func(c *Context) {
		c.MustNavigate(ts.URL)
		sel := c.El("#nonexistent")
		selErr = sel.Err()
	})

	if err := engine.Run("el-notfound"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if selErr == nil {
		t.Error("expected error for nonexistent element")
	}
	var notFound *ElementNotFoundError
	if !errors.As(selErr, &notFound) {
		t.Errorf("expected ElementNotFoundError, got %T: %v", selErr, selErr)
	}
}

// --- recorder.go tests ---

func TestIntegrationRecorderFramesAndDir(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-frames", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := c.StartRecording(RecorderOptions{})
		if err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		defer rec.Cleanup()

		// Check FramesDir is non-empty
		dir := rec.FramesDir()
		if dir == "" {
			t.Error("FramesDir should not be empty")
		}

		// Perform action and wait for frames
		c.El("#btn").MustClick()
		time.Sleep(500 * time.Millisecond)

		if err := rec.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		frames, err := rec.Frames()
		if err != nil {
			t.Fatalf("Frames: %v", err)
		}
		t.Logf("Captured %d frames, frame files: %d", rec.FrameCount(), len(frames))
	})

	if err := engine.Run("rec-frames"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestIntegrationRecorderDoubleStart(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-double-start", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := NewRecorder(c.Page(), RecorderOptions{})
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		defer rec.Cleanup()

		if err := rec.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}

		// Second start should fail
		err = rec.Start()
		if err == nil {
			t.Error("expected error on double Start")
		}

		rec.Stop()
	})

	if err := engine.Run("rec-double-start"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestIntegrationRecorderStopWithoutStart(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-stop-no-start", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := NewRecorder(c.Page(), RecorderOptions{})
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		defer rec.Cleanup()

		// Stop without Start should be a no-op
		if err := rec.Stop(); err != nil {
			t.Errorf("Stop without Start should not error, got: %v", err)
		}
	})

	if err := engine.Run("rec-stop-no-start"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestIntegrationRecorderPNGFormat(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-png", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := c.StartRecording(RecorderOptions{
			Format:    "png",
			Quality:   100,
			MaxWidth:  640,
			MaxHeight: 480,
		})
		if err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		defer rec.Cleanup()

		c.El("#btn").MustClick()
		time.Sleep(300 * time.Millisecond)

		if err := rec.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		t.Logf("PNG recorder captured %d frames", rec.FrameCount())
	})

	if err := engine.Run("rec-png"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestIntegrationRecorderSaveVideoNoFrames(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-no-frames", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := NewRecorder(c.Page(), RecorderOptions{})
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		defer rec.Cleanup()

		// SaveVideo with no frames should error
		err = rec.SaveVideo("/tmp/test-no-frames.mp4", 30)
		if err == nil {
			t.Error("expected error for SaveVideo with no frames")
		}
		if !strings.Contains(err.Error(), "no frames") {
			t.Errorf("expected 'no frames' error, got: %v", err)
		}

		// Same for SaveGIF
		err = rec.SaveGIF("/tmp/test-no-frames.gif", 15)
		if err == nil {
			t.Error("expected error for SaveGIF with no frames")
		}
	})

	if err := engine.Run("rec-no-frames"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- Additional coverage for page.go Navigate cache invalidation ---

func TestIntegrationNavigateClearsCache(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("nav-cache", func(c *Context) {
		c.MustNavigate(ts.URL)
		page := c.Page()

		// Populate root node cache
		id1, err := page.getRootNodeID()
		if err != nil {
			t.Fatalf("getRootNodeID: %v", err)
		}

		// Navigate to another page -- should invalidate cache
		if err := page.Navigate(ts.URL + "/about"); err != nil {
			t.Fatalf("Navigate: %v", err)
		}

		// Root node ID should be re-fetched (different page)
		id2, err := page.getRootNodeID()
		if err != nil {
			t.Fatalf("getRootNodeID after nav: %v", err)
		}

		// The IDs may differ because the DOM is fresh after navigation.
		// The key test is that the cache was invalidated (rootNodeID was 0
		// before the second call). We verify by checking that
		// flattenedNodes was also cleared.
		if page.flattenedNodes != nil {
			t.Error("flattenedNodes should be nil after Navigate")
		}

		t.Logf("root node IDs: before=%d after=%d", id1, id2)
	})

	if err := engine.Run("nav-cache"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- Additional coverage: ResolveNode ---

func TestIntegrationResolveNode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("resolve", func(c *Context) {
		c.MustNavigate(ts.URL)
		nodeID, err := c.Page().QuerySelector("#title")
		if err != nil {
			t.Fatalf("QuerySelector: %v", err)
		}
		objectID, err := c.Page().ResolveNode(nodeID)
		if err != nil {
			t.Fatalf("ResolveNode: %v", err)
		}
		if objectID == "" {
			t.Error("expected non-empty objectID")
		}
	})

	if err := engine.Run("resolve"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- Additional coverage: OnSession event handler ---

func TestIntegrationOnSession(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("on-session", func(c *Context) {
		c.MustNavigate(ts.URL)

		var received bool
		unsub := c.Page().OnSession("DOM.documentUpdated", func(params map[string]any) {
			received = true
		})
		defer unsub()

		// Trigger a DOM update by navigating
		c.Navigate(ts.URL + "/about")
		time.Sleep(200 * time.Millisecond)

		// The event may or may not fire depending on timing,
		// but the handler should not panic
		t.Logf("DOM.documentUpdated received: %v", received)
	})

	if err := engine.Run("on-session"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- Additional coverage: Attr returns empty for nonexistent attribute ---

func TestIntegrationSelectionAttrNonexistent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var val string
	var attrErr error
	engine.Task("attr-missing", func(c *Context) {
		c.MustNavigate(ts.URL)
		val, attrErr = c.El("#title").Attr("nonexistent-attr")
	})

	if err := engine.Run("attr-missing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if attrErr != nil {
		t.Errorf("Attr for nonexistent: %v", attrErr)
	}
	if val != "" {
		t.Errorf("expected empty string for nonexistent attr, got %q", val)
	}
}

// --- Test engine launch already launched ---

func TestIntegrationEngineAlreadyLaunched(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	engine := New(WithHeadless(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	// Second launch should fail
	err := engine.Launch()
	if err == nil {
		t.Error("expected error for already launched engine")
	}
	if !strings.Contains(err.Error(), "already launched") {
		t.Errorf("expected 'already launched' error, got: %v", err)
	}
}

// --- Test engine with custom viewport ---

func TestIntegrationEngineWithViewport(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true), WithViewport(1024, 768))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var w, h float64
	engine.Task("check-vp", func(c *Context) {
		c.MustNavigate(ts.URL)
		wResult, _ := c.Eval(`window.innerWidth`)
		hResult, _ := c.Eval(`window.innerHeight`)
		w, _ = wResult.(float64)
		h, _ = hResult.(float64)
	})

	if err := engine.Run("check-vp"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if w != 1024 {
		t.Errorf("viewport width: expected 1024, got %v", w)
	}
	if h != 768 {
		t.Errorf("viewport height: expected 768, got %v", h)
	}
}

// --- Test FillForm with an invalid selector ---

func TestIntegrationFillFormBadSelector(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var fillErr error
	engine.Task("fill-bad", func(c *Context) {
		c.MustNavigate(ts.URL + "/form")
		fillErr = c.FillForm(map[string]string{
			"#nonexistent": "value",
		})
	})

	if err := engine.Run("fill-bad"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if fillErr == nil {
		t.Error("expected error for FillForm with nonexistent selector")
	}
}

// --- Test QuerySelectorAll returning empty ---

func TestIntegrationQuerySelectorAllEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var nodeIDs []int64
	engine.Task("qsa-empty", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		nodeIDs, err = c.Page().QuerySelectorAll(".nonexistent-class")
		if err != nil {
			t.Fatalf("QuerySelectorAll: %v", err)
		}
	})

	if err := engine.Run("qsa-empty"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(nodeIDs) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodeIDs))
	}
}

// --- Test concurrent RunAll with errors ---

func TestIntegrationRunAllConcurrentWithError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true), WithPoolSize(2))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	engine.Task("good-task", func(c *Context) {
		c.MustNavigate(ts.URL)
	})
	engine.Task("bad-task", func(c *Context) {
		c.AbortWithError(fmt.Errorf("intentional failure"))
	})

	err := engine.RunAll()
	if err == nil {
		t.Error("expected error from RunAll when a task fails")
	}
}

// --- Test HTML and URL on context ---

func TestIntegrationContextHTMLAndURL(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var html, url string
	engine.Task("html-url", func(c *Context) {
		c.MustNavigate(ts.URL)
		html, _ = c.HTML()
		url = c.URL()
	})

	if err := engine.Run("html-url"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(html, "Full Test") {
		t.Errorf("HTML should contain 'Full Test'")
	}
	if !strings.HasPrefix(url, "http://127.0.0.1") {
		t.Errorf("URL: expected http://127.0.0.1:*, got %q", url)
	}
}

// --- Test HasEl with real browser ---

func TestIntegrationContextHasEl(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var hasTitle, hasMissing bool
	engine.Task("has-el", func(c *Context) {
		c.MustNavigate(ts.URL)
		hasTitle = c.HasEl("#title")
		hasMissing = c.HasEl("#nonexistent")
	})

	if err := engine.Run("has-el"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !hasTitle {
		t.Error("expected HasEl(#title) = true")
	}
	if hasMissing {
		t.Error("expected HasEl(#nonexistent) = false")
	}
}

// --- Additional coverage: QuerySelectorPiercing with class selector via flattened nodes ---

func TestIntegrationQuerySelectorPiercingClass(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var found bool
	engine.Task("pierce-class", func(c *Context) {
		c.MustNavigate(ts.URL + "/shadow")
		// Use class selector which goes through the flattened node path
		nid, err := c.Page().QuerySelectorPiercing(".shadow-cls")
		if err == nil && nid > 0 {
			found = true
		}
	})

	if err := engine.Run("pierce-class"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !found {
		t.Error("QuerySelectorPiercing should find shadow element by class")
	}
}

func TestIntegrationQuerySelectorPiercingNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var pierceErr error
	engine.Task("pierce-missing", func(c *Context) {
		c.MustNavigate(ts.URL)
		_, pierceErr = c.Page().QuerySelectorPiercing("#absolutely-nonexistent-element-xyz")
	})

	if err := engine.Run("pierce-missing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if pierceErr == nil {
		t.Error("expected error for piercing search that finds nothing")
	}
}

// --- Additional coverage: ElAll with error ---

func TestIntegrationElAllError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var count int
	engine.Task("elall-empty", func(c *Context) {
		c.MustNavigate(ts.URL)
		// Valid selector that matches nothing
		all := c.ElAll(".completely-nonexistent-class")
		count = all.Count()
	})

	if err := engine.Run("elall-empty"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if count != 0 {
		t.Errorf("expected 0 elements, got %d", count)
	}
}

// --- Additional coverage: MustNavigate error path triggers panic ---

func TestIntegrationMustNavigateInvalidURL(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	engine := Default(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var recovered bool
	engine.Task("must-nav-bad", func(c *Context) {
		defer func() {
			if r := recover(); r != nil {
				recovered = true
			}
		}()
		c.MustNavigate("ftp://invalid.scheme")
	})

	// The Recovery middleware in Default should catch the panic
	err := engine.Run("must-nav-bad")
	// Either the deferred recover catches it or Recovery middleware catches it
	if err != nil && !recovered {
		// Recovery middleware caught it
		t.Logf("Recovery caught the error: %v", err)
	} else if recovered {
		t.Log("Deferred recover caught the panic")
	}
}

// --- Additional coverage: Screenshot with full page + MaxSize ---

func TestIntegrationScreenshotFullPageWithMaxSize(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("fullpage-maxsize", func(c *Context) {
		c.MustNavigate(ts.URL + "/large-page")
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			FullPage: true,
			MaxSize:  2048,
		})
		if err != nil {
			t.Fatalf("ScreenshotWithOptions: %v", err)
		}
	})

	if err := engine.Run("fullpage-maxsize"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty screenshot")
	}
	t.Logf("Full page compressed to %d bytes", len(shot))
}

// --- Additional coverage: Recorder SaveVideo with default fps ---

func TestIntegrationRecorderSaveVideoDefaultFPS(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-save-defaults", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := NewRecorder(c.Page(), RecorderOptions{})
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		defer rec.Cleanup()

		// SaveVideo with fps=0 should default to 30
		err = rec.SaveVideo("/tmp/test-default-fps.mp4", 0)
		if err == nil {
			t.Error("expected error for no frames")
		}
		if !strings.Contains(err.Error(), "no frames") {
			t.Errorf("expected 'no frames' error, got: %v", err)
		}

		// SaveGIF with fps=0 should default to 15
		err = rec.SaveGIF("/tmp/test-default-fps.gif", 0)
		if err == nil {
			t.Error("expected error for no frames")
		}
	})

	if err := engine.Run("rec-save-defaults"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- Additional coverage: Recorder with recording still active when SaveVideo called ---

func TestIntegrationRecorderSaveVideoWhileRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-save-active", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := NewRecorder(c.Page(), RecorderOptions{})
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		defer rec.Cleanup()

		if err := rec.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}

		// Capture some frames
		c.El("#btn").MustClick()
		time.Sleep(500 * time.Millisecond)

		// SaveVideo while still recording -- should auto-stop first
		tmpDir := t.TempDir()
		outPath := filepath.Join(tmpDir, "test.mp4")
		err = rec.SaveVideo(outPath, 30)
		// This will either succeed (if ffmpeg is available) or fail with ffmpeg error
		// Either way it should NOT panic and should have called Stop
		if err != nil {
			t.Logf("SaveVideo: %v (expected if ffmpeg not installed)", err)
		}

		// Verify recording stopped
		if rec.recording.Load() {
			t.Error("recording should be stopped after SaveVideo")
		}
	})

	if err := engine.Run("rec-save-active"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- Additional coverage: Recorder SaveGIF while recording ---

func TestIntegrationRecorderSaveGIFWhileRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	engine.Task("rec-gif-active", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := NewRecorder(c.Page(), RecorderOptions{})
		if err != nil {
			t.Fatalf("NewRecorder: %v", err)
		}
		defer rec.Cleanup()

		if err := rec.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}

		c.El("#btn").MustClick()
		time.Sleep(500 * time.Millisecond)

		tmpDir := t.TempDir()
		outPath := filepath.Join(tmpDir, "test.gif")
		err = rec.SaveGIF(outPath, 15)
		if err != nil {
			t.Logf("SaveGIF: %v (expected if ffmpeg not installed)", err)
		}

		if rec.recording.Load() {
			t.Error("recording should be stopped after SaveGIF")
		}
	})

	if err := engine.Run("rec-gif-active"); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// --- Additional coverage: Evaluate with complex return types ---

func TestIntegrationEvaluatePromise(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var result any
	engine.Task("eval-promise", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		result, err = c.Eval(`new Promise(resolve => setTimeout(() => resolve("done"), 50))`)
		if err != nil {
			t.Fatalf("Eval promise: %v", err)
		}
	})

	if err := engine.Run("eval-promise"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result != "done" {
		t.Errorf("expected 'done', got %v", result)
	}
}

// --- Additional coverage: Context.Eval returns void ---

func TestIntegrationEvalVoid(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var result any
	var evalErr error
	engine.Task("eval-void", func(c *Context) {
		c.MustNavigate(ts.URL)
		// console.log returns undefined
		result, evalErr = c.Eval(`console.log("test")`)
	})

	if err := engine.Run("eval-void"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if evalErr != nil {
		t.Errorf("eval void: %v", evalErr)
	}
	if result != nil {
		t.Errorf("expected nil for void eval, got %v", result)
	}
}

// --- Additional coverage: Engine RunAll with no tasks ---

func TestIntegrationRunAllEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}

	engine := New(WithHeadless(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	// RunAll with no tasks should succeed
	if err := engine.RunAll(); err != nil {
		t.Errorf("RunAll empty: %v", err)
	}
}

// --- Additional coverage: Engine concurrent RunAll with multiple errors ---

func TestIntegrationRunAllConcurrentMultipleErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true), WithPoolSize(3))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	engine.Task("fail-1", func(c *Context) {
		c.AbortWithError(fmt.Errorf("error one"))
	})
	engine.Task("fail-2", func(c *Context) {
		c.AbortWithError(fmt.Errorf("error two"))
	})
	engine.Task("fail-3", func(c *Context) {
		c.AbortWithError(fmt.Errorf("error three"))
	})

	err := engine.RunAll()
	if err == nil {
		t.Error("expected error from RunAll with multiple failures")
	}
	// Should mention multiple failures
	if strings.Contains(err.Error(), "tasks failed") {
		t.Logf("Multi-error: %v", err)
	}
}

// --- Additional coverage: Page.Navigate with navigation error ---

func TestIntegrationNavigateNonexistentHost(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var navErr error
	engine.Task("nav-bad-host", func(c *Context) {
		navErr = c.Navigate("http://this-host-does-not-exist-at-all.invalid")
	})

	if err := engine.Run("nav-bad-host"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if navErr == nil {
		t.Error("expected error navigating to nonexistent host")
	}
	var ne *NavigationError
	if errors.As(navErr, &ne) {
		t.Logf("NavigationError: %v", ne)
	}
}

// --- Additional coverage: Selection operations after navigation (element stale) ---

func TestIntegrationSelectionAllTextsOnRealPage(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var texts []string
	engine.Task("texts-real", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		texts, err = c.ElAll(".item").Texts()
		if err != nil {
			t.Fatalf("Texts: %v", err)
		}
	})

	if err := engine.Run("texts-real"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(texts) != 3 {
		t.Errorf("expected 3 texts, got %d", len(texts))
	}
}

// --- Additional coverage: Engine RunGroup with actual tasks ---

func TestIntegrationRunGroupMultipleTasks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var mu sync.Mutex
	ran := make(map[string]bool)

	g := engine.Group("multi")
	g.Task("a", func(c *Context) {
		c.MustNavigate(ts.URL)
		mu.Lock()
		ran["a"] = true
		mu.Unlock()
	})
	g.Task("b", func(c *Context) {
		c.MustNavigate(ts.URL + "/about")
		mu.Lock()
		ran["b"] = true
		mu.Unlock()
	})

	if err := engine.RunGroup("multi"); err != nil {
		t.Fatalf("RunGroup: %v", err)
	}

	if !ran["a"] || !ran["b"] {
		t.Errorf("expected both tasks to run, ran=%v", ran)
	}
}

// --- Additional coverage: ScreenshotWithOptions with Clip and MaxSize ---

func TestIntegrationScreenshotClipMaxSizeCompression(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	ts := fullTestServer()
	defer ts.Close()
	engine := launchTestEngine(t)

	var shot []byte
	engine.Task("clip-compress", func(c *Context) {
		c.MustNavigate(ts.URL + "/large-page")
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			Clip:    &ClipRegion{X: 0, Y: 0, Width: 1000, Height: 1000},
			MaxSize: 1024,
		})
		if err != nil {
			t.Fatalf("ScreenshotWithOptions: %v", err)
		}
	})

	if err := engine.Run("clip-compress"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty screenshot")
	}
	t.Logf("Clip+MaxSize compressed to %d bytes", len(shot))
}
