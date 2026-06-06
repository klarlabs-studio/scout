// Example: demo showcases browse-go's middleware, groups, and task composition.
//
// It starts a local test server and demonstrates the Gin-like API.
//
// Usage:
//
//	go run ./examples/demo
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/middleware"
)

func main() {
	// Start a local test server with multiple pages
	ts := startTestServer()
	defer ts.Close()
	fmt.Printf("Test server running at %s\n\n", ts.URL)

	engine := browse.Default(
		browse.WithHeadless(true),
		browse.WithViewport(1024, 768),
	)
	engine.MustLaunch()
	defer engine.Close()

	// Global middleware: slow motion for demo visibility
	engine.Use(middleware.SlowMotion(200 * time.Millisecond))

	// Group: homepage tasks
	home := engine.Group("home")

	home.Task("extract-title", func(c *browse.Context) {
		c.MustNavigate(ts.URL)
		title := c.El("h1").MustText()
		fmt.Printf("[home/extract-title] Title: %s\n", title)
	})

	home.Task("count-links", func(c *browse.Context) {
		c.MustNavigate(ts.URL)
		links := c.ElAll("a")
		fmt.Printf("[home/count-links] Found %d links\n", links.Count())
	})

	// Group: form tasks with retry middleware
	forms := engine.Group("forms", middleware.Retry(middleware.RetryConfig{
		MaxAttempts:  2,
		InitialDelay: 100 * time.Millisecond,
	}))

	forms.Task("fill-search", func(c *browse.Context) {
		c.MustNavigate(ts.URL + "/search")
		c.El("#query").MustInput("browse-go")
		c.El("button").MustClick()
		c.WaitStable()

		result := c.El("#result").MustText()
		fmt.Printf("[forms/fill-search] Search result: %s\n", result)
	})

	// Standalone task: screenshot
	engine.Task("screenshot", func(c *browse.Context) {
		c.MustNavigate(ts.URL)
		if err := c.ScreenshotTo("demo-screenshot.png"); err != nil {
			fmt.Printf("[screenshot] Error: %v\n", err)
			return
		}
		fmt.Println("[screenshot] Saved to demo-screenshot.png")
	})

	// Run all tasks
	fmt.Println("--- Running all tasks ---")
	if err := engine.RunAll(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n--- All tasks completed ---")

	// Cleanup screenshot
	os.Remove("demo-screenshot.png")
}

func startTestServer() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Browse-Go Demo</title></head>
<body>
  <h1>Welcome to Browse-Go</h1>
  <nav>
    <a href="/about">About</a>
    <a href="/search">Search</a>
    <a href="/contact">Contact</a>
  </nav>
  <p>A Gin-like browser automation library for Go.</p>
</body>
</html>`)
	})

	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Search</title></head>
<body>
  <h1>Search</h1>
  <form onsubmit="event.preventDefault(); document.getElementById('result').textContent = 'Results for: ' + document.getElementById('query').value;">
    <input id="query" type="text" placeholder="Search..." />
    <button type="submit">Search</button>
  </form>
  <div id="result"></div>
</body>
</html>`)
	})

	return httptest.NewServer(mux)
}
