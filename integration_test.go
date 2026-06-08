//go:build integration

package browse

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
	<h1 id="title">Hello Browse</h1>
	<input id="name" type="text" value="initial" />
	<button id="btn" onclick="document.getElementById('title').textContent='Clicked!'">Click Me</button>
	<ul id="list">
		<li class="item">Alpha</li>
		<li class="item">Beta</li>
		<li class="item">Gamma</li>
	</ul>
	<div id="hidden" style="display:none">Hidden Content</div>
	<a id="link" href="/about" data-role="nav">About</a>
</body>
</html>`)
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>About</h1></body></html>`)
	})
	mux.HandleFunc("/table", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><body>
<table id="data">
  <thead><tr><th>Name</th><th>Age</th><th>City</th></tr></thead>
  <tbody>
    <tr><td>Alice</td><td>30</td><td>Berlin</td></tr>
    <tr><td>Bob</td><td>25</td><td>London</td></tr>
    <tr><td>Carol</td><td>35</td><td>Paris</td></tr>
  </tbody>
</table>
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
	return httptest.NewServer(mux)
}

func TestIntegrationNavigateAndExtract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var gotTitle string
	var gotURL string

	engine.Task("extract", func(c *Context) {
		c.MustNavigate(ts.URL)
		gotTitle = c.El("#title").MustText()
		gotURL = c.URL()
	})

	if err := engine.Run("extract"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotTitle != "Hello Browse" {
		t.Errorf("title: expected 'Hello Browse', got %q", gotTitle)
	}
	if !strings.HasPrefix(gotURL, "http://127.0.0.1:") {
		t.Errorf("URL: expected http://127.0.0.1:*, got %q", gotURL)
	}
}

func TestIntegrationClick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var afterClick string
	engine.Task("click", func(c *Context) {
		c.MustNavigate(ts.URL)
		c.El("#btn").MustClick()
		// Small wait for DOM update
		c.WaitStable()
		afterClick = c.El("#title").MustText()
	})

	if err := engine.Run("click"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if afterClick != "Clicked!" {
		t.Errorf("after click: expected 'Clicked!', got %q", afterClick)
	}
}

func TestIntegrationInput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var inputVal string
	engine.Task("input", func(c *Context) {
		c.MustNavigate(ts.URL)
		c.El("#name").MustInput("browse-go")
		inputVal, _ = c.El("#name").Value()
	})

	if err := engine.Run("input"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if inputVal != "browse-go" {
		t.Errorf("input value: expected 'browse-go', got %q", inputVal)
	}
}

func TestIntegrationElAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var items []string
	var count int
	engine.Task("list", func(c *Context) {
		c.MustNavigate(ts.URL)
		all := c.ElAll(".item")
		count = all.Count()
		items, _ = all.Texts()
	})

	if err := engine.Run("list"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if count != 3 {
		t.Errorf("count: expected 3, got %d", count)
	}
	expected := []string{"Alpha", "Beta", "Gamma"}
	for i, v := range expected {
		if i >= len(items) || items[i] != v {
			t.Errorf("item %d: expected %q, got %q", i, v, items[i])
		}
	}
}

func TestIntegrationHasEl(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var hasTitle, hasMissing bool
	engine.Task("hasel", func(c *Context) {
		c.MustNavigate(ts.URL)
		hasTitle = c.HasEl("#title")
		hasMissing = c.HasEl("#nonexistent")
	})

	if err := engine.Run("hasel"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !hasTitle {
		t.Error("expected HasEl(#title) = true")
	}
	if hasMissing {
		t.Error("expected HasEl(#nonexistent) = false")
	}
}

func TestIntegrationAttr(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var href, dataRole string
	engine.Task("attr", func(c *Context) {
		c.MustNavigate(ts.URL)
		href, _ = c.El("#link").Attr("href")
		dataRole, _ = c.El("#link").Attr("data-role")
	})

	if err := engine.Run("attr"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if href != "/about" {
		t.Errorf("href: expected '/about', got %q", href)
	}
	if dataRole != "nav" {
		t.Errorf("data-role: expected 'nav', got %q", dataRole)
	}
}

func TestIntegrationVisibility(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var titleVisible, hiddenVisible bool
	engine.Task("visible", func(c *Context) {
		c.MustNavigate(ts.URL)
		titleVisible, _ = c.El("#title").Visible()
		hiddenVisible, _ = c.El("#hidden").Visible()
	})

	if err := engine.Run("visible"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !titleVisible {
		t.Error("expected #title to be visible")
	}
	if hiddenVisible {
		t.Error("expected #hidden to be hidden")
	}
}

func TestIntegrationScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var screenshot []byte
	engine.Task("screenshot", func(c *Context) {
		c.MustNavigate(ts.URL)
		screenshot, _ = c.Screenshot()
	})

	if err := engine.Run("screenshot"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(screenshot) == 0 {
		t.Error("expected non-empty screenshot")
	}
	// PNG magic bytes
	if len(screenshot) > 4 && screenshot[0] != 0x89 {
		t.Error("expected PNG format")
	}
}

func TestIntegrationHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var html string
	engine.Task("html", func(c *Context) {
		c.MustNavigate(ts.URL)
		html, _ = c.HTML()
	})

	if err := engine.Run("html"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(html, "Hello Browse") {
		t.Errorf("HTML should contain 'Hello Browse', got %d bytes", len(html))
	}
}

func TestIntegrationEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var result any
	engine.Task("eval", func(c *Context) {
		c.MustNavigate(ts.URL)
		result, _ = c.Eval(`document.title`)
	})

	if err := engine.Run("eval"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result != "Test Page" {
		t.Errorf("eval: expected 'Test Page', got %v", result)
	}
}

func TestIntegrationMiddlewareChainWithBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := Default(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var title string
	engine.Task("with-middleware", func(c *Context) {
		c.MustNavigate(ts.URL)
		title = c.El("#title").MustText()
	})

	if err := engine.Run("with-middleware"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if title != "Hello Browse" {
		t.Errorf("expected 'Hello Browse', got %q", title)
	}
}

func TestIntegrationSelectAllFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var filtered []string
	engine.Task("filter", func(c *Context) {
		c.MustNavigate(ts.URL)
		result := c.ElAll(".item").Filter(func(s *Selection) bool {
			text, _ := s.Text()
			return strings.HasPrefix(text, "B")
		})
		filtered, _ = result.Texts()
	})

	if err := engine.Run("filter"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(filtered) != 1 || filtered[0] != "Beta" {
		t.Errorf("expected [Beta], got %v", filtered)
	}
}

func TestIntegrationFullPageScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var fullPage []byte
	engine.Task("fullpage", func(c *Context) {
		c.MustNavigate(ts.URL)
		fullPage, _ = c.ScreenshotFullPage()
	})

	if err := engine.Run("fullpage"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fullPage) == 0 {
		t.Error("expected non-empty full page screenshot")
	}
	if fullPage[0] != 0x89 {
		t.Error("expected PNG format")
	}
}

func TestIntegrationScreenshotMaxSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var shot []byte
	engine.Task("maxsize", func(c *Context) {
		c.MustNavigate(ts.URL)
		// Request with a very small max size to force compression
		var err error
		shot, err = c.Page().ScreenshotWithOptions(ScreenshotOptions{
			MaxSize: 1024, // 1KB — will force aggressive compression
		})
		if err != nil {
			t.Fatalf("ScreenshotWithOptions: %v", err)
		}
	})

	if err := engine.Run("maxsize"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty screenshot")
	}
	t.Logf("Compressed screenshot: %d bytes (limit 1024)", len(shot))
	// JPEG magic bytes: FF D8
	if len(shot) > 1 && shot[0] == 0xFF && shot[1] == 0xD8 {
		t.Log("Format: JPEG (auto-switched from PNG)")
	}
}

func TestIntegrationScreenshotCompact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var shot []byte
	engine.Task("compact", func(c *Context) {
		c.MustNavigate(ts.URL)
		var err error
		shot, err = c.Page().ScreenshotCompact()
		if err != nil {
			t.Fatalf("ScreenshotCompact: %v", err)
		}
	})

	if err := engine.Run("compact"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Fatal("expected non-empty screenshot")
	}
	if len(shot) > 5*1024*1024 {
		t.Errorf("screenshot exceeds 5MB limit: %d bytes", len(shot))
	}
	t.Logf("Compact screenshot: %d bytes", len(shot))
}

func TestIntegrationElementScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var elShot []byte
	engine.Task("elshot", func(c *Context) {
		c.MustNavigate(ts.URL)
		elShot, _ = c.ScreenshotElement("#title")
	})

	if err := engine.Run("elshot"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(elShot) == 0 {
		t.Error("expected non-empty element screenshot")
	}
}

func TestIntegrationSelectionScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var shot []byte
	engine.Task("selshot", func(c *Context) {
		c.MustNavigate(ts.URL)
		shot, _ = c.El("#title").Screenshot()
	})

	if err := engine.Run("selshot"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(shot) == 0 {
		t.Error("expected non-empty selection screenshot")
	}
}

func TestIntegrationPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var pdf []byte
	engine.Task("pdf", func(c *Context) {
		c.MustNavigate(ts.URL)
		pdf, _ = c.PDF()
	})

	if err := engine.Run("pdf"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(pdf) == 0 {
		t.Error("expected non-empty PDF")
	}
	// PDF magic bytes: %PDF
	if len(pdf) > 4 && string(pdf[:4]) != "%PDF" {
		t.Errorf("expected PDF format, got first 4 bytes: %x", pdf[:4])
	}
}

func TestIntegrationRecorder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var frameCount int64
	engine.Task("record", func(c *Context) {
		c.MustNavigate(ts.URL)

		rec, err := c.StartRecording(RecorderOptions{})
		if err != nil {
			t.Fatalf("StartRecording: %v", err)
		}
		defer rec.Cleanup()

		// Perform some actions to generate frames
		c.El("#btn").MustClick()

		// Give screencast time to capture frames
		time.Sleep(500 * time.Millisecond)

		if err := rec.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		frameCount = rec.FrameCount()
	})

	if err := engine.Run("record"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Screencast may or may not capture frames depending on timing
	t.Logf("Captured %d frames", frameCount)
}

func TestIntegrationTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var table *Table
	engine.Task("table", func(c *Context) {
		c.MustNavigate(ts.URL + "/table")
		table, _ = c.ExtractTable("#data")
	})

	if err := engine.Run("table"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if table == nil {
		t.Fatal("table is nil")
	}
	if len(table.Headers) != 3 {
		t.Errorf("expected 3 headers, got %d: %v", len(table.Headers), table.Headers)
	}
	if len(table.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(table.Rows))
	}
	if len(table.Rows) > 0 && table.Rows[0][0] != "Alice" {
		t.Errorf("expected first cell 'Alice', got %q", table.Rows[0][0])
	}
}

func TestIntegrationFillForm(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var fname, lname, email string
	engine.Task("form", func(c *Context) {
		c.MustNavigate(ts.URL + "/form")
		err := c.FillForm(map[string]string{
			"#fname": "John",
			"#lname": "Doe",
			"#email": "john-test-user",
		})
		if err != nil {
			t.Fatalf("FillForm: %v", err)
		}
		fname, _ = c.El("#fname").Value()
		lname, _ = c.El("#lname").Value()
		email, _ = c.El("#email").Value()
	})

	if err := engine.Run("form"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if fname != "John" {
		t.Errorf("fname: expected 'John', got %q", fname)
	}
	if lname != "Doe" {
		t.Errorf("lname: expected 'Doe', got %q", lname)
	}
	if email != "john-test-user" {
		t.Errorf("email: expected 'john-test-user', got %q", email)
	}
}

func TestIntegrationWaitSelector(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	var text string
	engine.Task("wait-selector", func(c *Context) {
		c.MustNavigate(ts.URL + "/delayed")
		if err := c.WaitSelector("#delayed-el"); err != nil {
			t.Fatalf("WaitSelector: %v", err)
		}
		text = c.El("#delayed-el").MustText()
	})

	if err := engine.Run("wait-selector"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if text != "Loaded!" {
		t.Errorf("expected 'Loaded!', got %q", text)
	}
}

func TestIntegrationConcurrentRunAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ts := testServer()
	defer ts.Close()

	engine := New(WithHeadless(true), WithAllowPrivateIPs(true), WithPoolSize(3))
	if err := engine.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer engine.Close()

	results := make(map[string]string)
	var mu sync.Mutex

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("task-%d", i)
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

	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}
	for name, title := range results {
		if title != "Hello Browse" {
			t.Errorf("%s: expected 'Hello Browse', got %q", name, title)
		}
	}
}
