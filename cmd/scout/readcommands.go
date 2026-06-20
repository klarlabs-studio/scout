package main

import (
	"fmt"

	"go.klarlabs.de/scout/agent"
)

// readCommand is a one-shot, read-only CLI command: launch a browser, navigate
// to a URL, run a single read/diagnostic action against the shared
// agent.Session, print the result, and exit. These honor scout's deliberate
// one-shot-per-page CLI tier — they take no further interaction and so map
// cleanly onto launch→navigate→read→exit.
type readCommand struct {
	usage string
	// run executes the action and returns a value to print as JSON, or a
	// string/error. Returning a string prints it verbatim (for text payloads).
	run func(s *agent.Session) (any, error)
}

// readCommands is the registry of one-shot read commands the CLI exposes beyond
// the original navigate/observe/markdown set. Each delegates to agent.Session;
// none duplicates CDP. Keeping them in a table makes the surface testable and
// keeps main()'s dispatch flat.
var readCommands = map[string]readCommand{
	"accessibility": {
		usage: "scout accessibility <url>",
		run:   func(s *agent.Session) (any, error) { return s.AccessibilityTree() },
	},
	"readable": {
		usage: "scout readable <url>",
		run:   func(s *agent.Session) (any, error) { return s.ReadableText() },
	},
	"auto-extract": {
		usage: "scout auto-extract <url>",
		run:   func(s *agent.Session) (any, error) { return s.AutoExtract() },
	},
	"table": {
		usage: "scout table <url> <selector>",
		run:   nil, // handled specially: needs a selector argument
	},
	"aria": {
		usage: "scout aria <url>",
		run:   func(s *agent.Session) (any, error) { return s.AriaViolations() },
	},
	"vitals": {
		usage: "scout vitals <url>",
		run:   func(s *agent.Session) (any, error) { return s.WebVitals() },
	},
	"console": {
		usage: "scout console <url>",
		run:   func(s *agent.Session) (any, error) { return s.ConsoleErrors() },
	},
	"dialog": {
		usage: "scout dialog <url>",
		run:   func(s *agent.Session) (any, error) { return s.DetectDialog() },
	},
	"auth-wall": {
		usage: "scout auth-wall <url>",
		run:   func(s *agent.Session) (any, error) { return s.DetectAuthWall() },
	},
	"cookies": {
		usage: "scout cookies <url>",
		run:   func(s *agent.Session) (any, error) { return s.ListCookies() },
	},
	"app-state": {
		usage: "scout app-state <url>",
		run:   func(s *agent.Session) (any, error) { return s.GetAppState() },
	},
}

// runReadCommand dispatches a one-shot read command. Returns false if name is
// not a registered read command so the caller can fall through.
func runReadCommand(name string, args []string) bool {
	cmd, ok := readCommands[name]
	if !ok {
		return false
	}

	// "table" needs a selector positional after the URL.
	if name == "table" {
		requireArgs(args, 2, cmd.usage)
		url, selector := args[1], args[2]
		runOnePage(url, parseFlags(args[3:]), func(s *cliSession) {
			result, err := s.session.ExtractTable(selector)
			exitOnErr(err)
			printJSON(result)
		})
		return true
	}

	requireArgs(args, 1, cmd.usage)
	runOnePage(args[1], parseFlags(args[2:]), func(s *cliSession) {
		result, err := cmd.run(s.session)
		exitOnErr(err)
		// Text payloads (accessibility tree, readable text) print verbatim;
		// structured payloads print as indented JSON.
		if str, ok := result.(string); ok {
			fmt.Println(str)
			return
		}
		printJSON(result)
	})
	return true
}
