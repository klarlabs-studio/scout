package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.klarlabs.de/scout/agent"
)

// runWatch re-runs observe on a URL at intervals and prints diffs.
func runWatch(url string, flags cliFlags) {
	interval := flags.getDuration("interval", 5*time.Second)
	headless := flags.getBool("headless", false)

	session, err := agent.NewSession(agent.SessionConfig{
		Headless:        headless,
		AllowPrivateIPs: true,
	})
	exitOnErr(err)
	defer func() { _ = session.Close() }()

	_, err = session.Navigate(url)
	exitOnErr(err)

	// Set up Ctrl+C handler
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Printf("Watching %s every %s (Ctrl+C to stop)\n\n", url, interval)

	// First observation (installs MutationObserver)
	_, _, _ = session.ObserveDiff()

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		select {
		case <-sigCh:
			fmt.Println("\nStopped watching.")
			return
		case <-tick.C:
			obs, diff, err := session.ObserveDiff()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				continue
			}

			if diff.HasDiff {
				ts := time.Now().Format("15:04:05")
				fmt.Printf("[%s] %s — %s\n", ts, diff.Classification, diff.Summary)
				if len(diff.Added) > 0 {
					fmt.Printf("  + %d elements added\n", len(diff.Added))
					for _, el := range diff.Added {
						if el.Text != "" {
							fmt.Printf("    + <%s> %s\n", el.Tag, truncateStr(el.Text, 60))
						}
					}
				}
				if len(diff.Removed) > 0 {
					fmt.Printf("  - %d elements removed\n", len(diff.Removed))
				}
				if len(diff.Modified) > 0 {
					fmt.Printf("  ~ %d elements modified\n", len(diff.Modified))
				}
			} else {
				_ = obs // keep reference
			}
		}
	}
}

// runPipe reads URLs from stdin and runs a command on each.
func runPipe(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: echo urls | scout pipe <command> [selector]")
		os.Exit(1)
	}

	command := args[0]
	selector := ""
	if len(args) > 1 {
		selector = args[1]
	}

	// Validate the command (and its arguments) up front, before launching a
	// browser, so these usage exits don't skip the cleanup defer below.
	switch command {
	case "extract", "observe", "markdown", "screenshot", "frameworks":
	default:
		fmt.Fprintf(os.Stderr, "Unknown pipe command: %s (supported: extract, observe, markdown, screenshot, frameworks)\n", command)
		os.Exit(1)
	}
	if command == "extract" && selector == "" {
		fmt.Fprintln(os.Stderr, "extract requires a selector: scout pipe extract <selector>")
		os.Exit(1)
	}

	session, err := agent.NewSession(agent.SessionConfig{
		Headless:        true,
		AllowPrivateIPs: true,
	})
	exitOnErr(err)
	defer func() { _ = session.Close() }()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		url := strings.TrimSpace(scanner.Text())
		if url == "" || strings.HasPrefix(url, "#") {
			continue
		}

		_, err := session.Navigate(url)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error navigating %s: %v\n", url, err)
			continue
		}

		switch command {
		case "extract":
			result, err := session.Extract(selector)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
			} else {
				fmt.Printf("%s\t%s\n", url, result.Text)
			}
		case "observe":
			obs, err := session.Observe()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
			} else {
				data, _ := json.Marshal(obs)
				fmt.Println(string(data))
			}
		case "markdown":
			md, err := session.Markdown()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
			} else {
				fmt.Printf("--- %s ---\n%s\n\n", url, md)
			}
		case "screenshot":
			data, err := session.Screenshot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
			} else {
				filename := sanitizeFilename(url) + ".png"
				_ = os.WriteFile(filename, data, 0o600)
				fmt.Printf("%s → %s (%d bytes)\n", url, filename, len(data))
			}
		case "frameworks":
			fw, err := session.DetectedFrameworks()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
			} else {
				fmt.Printf("%s\t%s\n", url, strings.Join(fw, ","))
			}
		}
	}
}

// runRecord opens a visible browser for interactive recording.
func runRecord(url string, flags cliFlags) {
	output := flags.get("output", "playbook.json")

	session, err := agent.NewSession(agent.SessionConfig{
		Headless:        false, // always visible for recording
		AllowPrivateIPs: true,
	})
	exitOnErr(err)
	defer func() { _ = session.Close() }()

	session.StartRecordingPlaybook("recorded")

	_, err = session.Navigate(url)
	exitOnErr(err)

	fmt.Printf("Recording started. Browser is open at %s\n", url)
	fmt.Println("Interact with the page in the browser window.")
	fmt.Println("Press Enter here when done to save the playbook.")
	fmt.Println()

	// Wait for Enter
	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n')

	pb, err := session.StopRecordingPlaybook()
	exitOnErr(err)

	err = agent.SavePlaybook(pb, output)
	exitOnErr(err)

	fmt.Printf("Playbook saved to %s (%d actions)\n", output, len(pb.Actions))
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sanitizeFilename(url string) string {
	r := strings.NewReplacer("https://", "", "http://", "", "/", "_", ":", "_", "?", "_", "&", "_")
	s := r.Replace(url)
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}
