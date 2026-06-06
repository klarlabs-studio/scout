// Example: scrape demonstrates extracting structured data from a web page.
//
// Usage:
//
//	go run ./examples/scrape -url https://news.ycombinator.com
package main

import (
	"flag"
	"fmt"
	"os"

	browse "go.klarlabs.de/scout"
)

func main() {
	url := flag.String("url", "https://news.ycombinator.com", "URL to scrape")
	selector := flag.String("selector", ".titleline > a", "CSS selector for items")
	headless := flag.Bool("headless", true, "Run in headless mode")
	flag.Parse()

	engine := browse.Default(browse.WithHeadless(*headless))
	engine.MustLaunch()
	defer engine.Close()

	engine.Task("scrape", func(c *browse.Context) {
		c.MustNavigate(*url)

		items := c.ElAll(*selector)
		fmt.Printf("Found %d items:\n\n", items.Count())

		items.Each(func(i int, el *browse.Selection) {
			title, _ := el.Text()
			href, _ := el.Attr("href")
			fmt.Printf("%3d. %s\n     %s\n\n", i+1, title, href)
		})
	})

	if err := engine.Run("scrape"); err != nil {
		fmt.Fprintf(os.Stderr, "Scrape failed: %v\n", err)
		os.Exit(1)
	}
}
