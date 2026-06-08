// scout is the CLI for AI-powered browser automation.
//
// Usage:
//
//	scout mcp serve                  Start the MCP server (stdio transport)
//	scout navigate <url>             Navigate and print page info as JSON
//	scout observe <url>              Print structured page observation
//	scout markdown <url>             Print page content as markdown
//	scout screenshot <url>           Save screenshot to file
//	scout pdf <url>                  Save PDF to file
//	scout extract <url> <selector>   Extract text from element(s)
//	scout form discover <url>        Discover form fields
//	scout eval <url> <expression>    Evaluate JavaScript on page
//	scout version                    Print version
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Set by GoReleaser ldflags.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "mcp":
		if len(args) < 2 || args[1] != "serve" {
			fmt.Fprintln(os.Stderr, "Usage: scout mcp serve")
			os.Exit(1)
		}
		serveMCP()

	case "ui":
		if len(args) < 2 || args[1] != "serve" {
			fmt.Fprintln(os.Stderr, "Usage: scout ui serve [--provider=claude] [--model=...] [--port=4200]")
			os.Exit(1)
		}
		serveUI(args[2:])

	case "navigate":
		requireArgs(args, 1, "scout navigate <url>")
		runOnePage(args[1], parseFlags(args[2:]), func(s *cliSession) {
			result, err := s.session.Navigate(args[1])
			exitOnErr(err)
			printJSON(result)
		})

	case "observe":
		requireArgs(args, 1, "scout observe <url>")
		runOnePage(args[1], parseFlags(args[2:]), func(s *cliSession) {
			obs, err := s.session.Observe()
			exitOnErr(err)
			printJSON(obs)
		})

	case "markdown":
		requireArgs(args, 1, "scout markdown <url>")
		runOnePage(args[1], parseFlags(args[2:]), func(s *cliSession) {
			md, err := s.session.Markdown()
			exitOnErr(err)
			fmt.Println(md)
		})

	case "screenshot":
		requireArgs(args, 1, "scout screenshot <url> [--output file.png]")
		flags := parseFlags(args[2:])
		output := flags.get("output", "screenshot.png")
		runOnePage(args[1], flags, func(s *cliSession) {
			data, err := s.session.Screenshot()
			exitOnErr(err)
			exitOnErr(os.WriteFile(output, data, 0o600))
			fmt.Printf("Saved screenshot to %s (%d bytes)\n", output, len(data))
		})

	case "pdf":
		requireArgs(args, 1, "scout pdf <url> [--output file.pdf]")
		flags := parseFlags(args[2:])
		output := flags.get("output", "page.pdf")
		runOnePage(args[1], flags, func(s *cliSession) {
			data, err := s.session.PDF()
			exitOnErr(err)
			exitOnErr(os.WriteFile(output, data, 0o600))
			fmt.Printf("Saved PDF to %s (%d bytes)\n", output, len(data))
		})

	case "extract":
		requireArgs(args, 2, "scout extract <url> <selector>")
		runOnePage(args[1], parseFlags(args[3:]), func(s *cliSession) {
			result, err := s.session.ExtractAll(args[2])
			exitOnErr(err)
			if result.Count == 1 {
				fmt.Println(result.Items[0])
			} else {
				printJSON(result)
			}
		})

	case "eval":
		requireArgs(args, 2, "scout eval <url> <expression>")
		runOnePage(args[1], parseFlags(args[3:]), func(s *cliSession) {
			result, err := s.session.Eval(args[2])
			exitOnErr(err)
			if s, ok := result.(string); ok {
				fmt.Println(s)
			} else {
				printJSON(result)
			}
		})

	case "form":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: scout form discover <url> [selector]")
			os.Exit(1)
		}
		switch args[1] {
		case "discover":
			requireArgs(args[1:], 1, "scout form discover <url> [selector]")
			url := args[2]
			selector := ""
			if len(args) > 3 && !strings.HasPrefix(args[3], "--") {
				selector = args[3]
			}
			runOnePage(url, parseFlags(args[3:]), func(s *cliSession) {
				result, err := s.session.DiscoverForm(selector)
				exitOnErr(err)
				printJSON(result)
			})
		default:
			fmt.Fprintf(os.Stderr, "Unknown form command: %s\n", args[1])
			os.Exit(1)
		}

	case "frameworks":
		requireArgs(args, 1, "scout frameworks <url>")
		runOnePage(args[1], parseFlags(args[2:]), func(s *cliSession) {
			frameworks, err := s.session.DetectedFrameworks()
			exitOnErr(err)
			if len(frameworks) == 0 {
				fmt.Println("No frameworks detected")
			} else {
				for _, f := range frameworks {
					fmt.Println(f)
				}
			}
		})

	case "watch":
		requireArgs(args, 1, "scout watch <url> [--interval=5s]")
		runWatch(args[1], parseFlags(args[2:]))

	case "pipe":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: echo urls | scout pipe <command> [selector]")
			os.Exit(1)
		}
		runPipe(args[1:])

	case "record":
		requireArgs(args, 1, "scout record <url> [--output=playbook.json]")
		runRecord(args[1], parseFlags(args[2:]))

	case "version", "--version", "-v":
		fmt.Printf("scout %s (%s)\n", version, commit)

	case "help", "--help", "-h":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`scout — AI-powered browser automation

Usage:
  scout <command> [arguments]

Commands:
  navigate <url>                    Navigate and print page info as JSON
  observe <url>                     Print structured page observation (links, inputs, buttons)
  markdown <url>                    Print page content as compact markdown
  screenshot <url> [--output f]     Save screenshot (default: screenshot.png)
  pdf <url> [--output f]            Save PDF (default: page.pdf)
  extract <url> <selector>          Extract text from element(s)
  eval <url> <expression>           Evaluate JavaScript on page
  form discover <url> [selector]    Discover form fields with labels
  frameworks <url>                  Detect frontend frameworks on page
  watch <url> [--interval=5s]       Live-watch page changes (Ctrl+C to stop)
  pipe <command> [selector]         Process URLs from stdin (one per line)
  record <url> [--output=file]      Interactive recording → playbook JSON
  mcp serve                         Start the MCP server (stdio transport)
  ui serve [flags]                  Start the AG-UI server (browser automation UI)
  version                           Print version information

Global flags:
  --headless                        Run without browser window (for scripts/CI)
  --timeout=30s                     Page operation timeout

MCP Configuration:
  Claude Code:     claude mcp add scout -- scout mcp serve
  Claude Desktop:  {"mcpServers": {"scout": {"command": "scout", "args": ["mcp", "serve"]}}}

`)
}

func requireArgs(args []string, n int, usage string) {
	if len(args) < n+1 {
		fmt.Fprintf(os.Stderr, "Usage: %s\n", usage)
		os.Exit(1)
	}
}

func exitOnErr(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
