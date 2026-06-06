// Example: login demonstrates authenticating to a web application.
//
// Usage:
//
//	go run ./examples/login -url https://example.com/login -user admin -pass secret
package main

import (
	"flag"
	"fmt"
	"os"

	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/middleware"
)

func main() {
	url := flag.String("url", "", "Login page URL")
	user := flag.String("user", "", "Username")
	pass := flag.String("pass", "", "Password")
	userSel := flag.String("user-selector", "#username", "CSS selector for username input")
	passSel := flag.String("pass-selector", "#password", "CSS selector for password input")
	submitSel := flag.String("submit-selector", "button[type=submit]", "CSS selector for submit button")
	headless := flag.Bool("headless", true, "Run in headless mode")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "Usage: login -url <URL> -user <USER> -pass <PASS>")
		os.Exit(1)
	}

	engine := browse.Default(
		browse.WithHeadless(*headless),
		browse.WithViewport(1280, 720),
	)
	engine.MustLaunch()
	defer engine.Close()

	engine.Task("login", middleware.ScreenshotOnError("./"), func(c *browse.Context) {
		c.MustNavigate(*url)

		c.El(*userSel).MustInput(*user)
		c.El(*passSel).MustInput(*pass)
		c.El(*submitSel).MustClick()

		c.WaitStable()

		fmt.Printf("Logged in. Current URL: %s\n", c.URL())

		if title, err := c.Eval(`document.title`); err == nil {
			fmt.Printf("Page title: %v\n", title)
		}
	})

	if err := engine.Run("login"); err != nil {
		fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
		os.Exit(1)
	}
}
