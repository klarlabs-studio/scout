// scout is the CLI for AI-powered browser automation.
//
// Usage:
//
//	scout mcp serve [--profile=NAME]  Start the MCP server (stdio transport)
//	scout mcp serve-http [flags]      Start the MCP server over Streamable HTTP
//	scout navigate <url>             Navigate and optionally verify a page condition
//	scout click <url> <selector>     Click once and report the observed outcome
//	scout type <url> <selector> <text> Type into a field and verify its committed value
//	scout submit <url> <selector>      Submit a form and report response and acceptance evidence
//	scout observe <url>              Print structured page observation
//	scout markdown <url>             Print page content as markdown
//	scout screenshot <url>           Save screenshot to file
//	scout pdf <url>                  Save PDF to file
//	scout extract <url> <selector>   Extract text from element(s)
//	scout form discover <url>        Discover form fields
//	scout press-key <url> <key>      Dispatch a key/chord and optionally verify a page condition
//	scout key-down|key-up <url> <key> Hold or release a key
//	scout eval <url> <expression>    Evaluate JavaScript on page
//	scout version                    Print version
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/agent"
	"go.klarlabs.de/scout/internal/cdp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Set by GoReleaser ldflags.
var (
	version      = "dev"
	commit       = "none"
	jsonErrors   bool
	recoveryFile string
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}
	args, jsonErrors = extractJSONErrorsFlag(args)
	args, recoveryFile = extractRecoveryFileFlag(args)
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}
	if recoveryFile != "" && args[0] != "reconcile" {
		if _, err := os.Lstat(recoveryFile); err == nil {
			exitOnErr(fmt.Errorf("recovery file %q already exists; reconcile it or choose another path", recoveryFile))
		} else if !os.IsNotExist(err) {
			exitOnErr(fmt.Errorf("check recovery file path: %w", err))
		}
	}

	switch args[0] {
	case "mcp":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: scout mcp serve | scout mcp serve-http [--listen=127.0.0.1:4201]")
			os.Exit(1)
		}
		switch args[1] {
		case "serve":
			flags := parseFlags(args[2:])
			for key := range flags.flags {
				if key != "profile" && key != "require-tools" && key != "otel" && key != "task-store" && key != "curated" {
					exitOnErr(fmt.Errorf("unknown mcp serve flag --%s", key))
				}
			}
			otelValue := flags.get("otel", "false")
			if otelValue == "" {
				otelValue = "true"
			}
			curatedValue := flags.get("curated", "false")
			if curatedValue == "" {
				curatedValue = "true"
			}
			startMCPWithTelemetry("", flags.get("profile", "all"), flags.get("require-tools", ""), otelValue, flags.get("task-store", ""), curatedValue)
		case "serve-http":
			flags := parseFlags(args[2:])
			for key := range flags.flags {
				if key != "listen" && key != "profile" && key != "require-tools" && key != "otel" && key != "task-store" && key != "curated" {
					exitOnErr(fmt.Errorf("unknown mcp serve-http flag --%s", key))
				}
			}
			addr := flags.get("listen", "127.0.0.1:4201")
			otelValue := flags.get("otel", "false")
			if otelValue == "" {
				otelValue = "true"
			}
			curatedValue := flags.get("curated", "false")
			if curatedValue == "" {
				curatedValue = "true"
			}
			startMCPWithTelemetry(addr, flags.get("profile", "all"), flags.get("require-tools", ""), otelValue, flags.get("task-store", ""), curatedValue)
		default:
			fmt.Fprintln(os.Stderr, "Usage: scout mcp serve | scout mcp serve-http [--listen=127.0.0.1:4201]")
			os.Exit(1)
		}

	case "ui":
		if len(args) < 2 || args[1] != "serve" {
			fmt.Fprintln(os.Stderr, "Usage: scout ui serve [--provider=claude] [--model=...] [--port=4200] [--allowed-origins=LIST]")
			os.Exit(1)
		}
		serveUI(args[2:])

	case "session":
		flags := parseFlags(args[1:])
		for key := range flags.flags {
			if key != "url" && key != "headless" && key != "timeout" && key != "task-store" && key != "allow-private-ips" && key != "allowed-origins" {
				exitOnErr(fmt.Errorf("unknown session flag --%s", key))
			}
		}
		serveCLIActionSession(flags)

	case "doctor":
		flags := parseFlags(args[1:])
		for key := range flags.flags {
			if key != "json" && key != "task-store" && key != "profile" && key != "require-tools" && key != "http-listen" && key != "live" && key != "live-timeout" && key != "probe-url" && key != "probe-text" {
				exitOnErr(fmt.Errorf("unknown doctor flag --%s", key))
			}
		}
		jsonOutput, err := flags.getBool("json", false)
		exitOnErr(err)
		live, err := flags.getBool("live", false)
		exitOnErr(err)
		liveTimeout := 5 * time.Second
		if raw := flags.get("live-timeout", ""); raw != "" {
			liveTimeout, err = time.ParseDuration(raw)
			exitOnErr(err)
			if liveTimeout <= 0 || liveTimeout > 30*time.Second {
				exitOnErr(fmt.Errorf("--live-timeout must be greater than zero and at most 30s"))
			}
		}
		probeURL := flags.get("probe-url", "")
		probeText := flags.get("probe-text", "")
		report := runDoctorWithProbeOptions(flags.get("task-store", ""), flags.get("profile", "all"), flags.get("require-tools", ""), flags.get("http-listen", ""), live, liveTimeout, probeURL, probeText)
		if jsonOutput {
			exitOnErr(printDoctorJSON(report))
		} else {
			printDoctor(report)
		}
		if report.Status == "error" {
			os.Exit(1)
		}

	case "navigate":
		requireArgs(args, 1, "scout navigate <url> [--target-id=ID] [--expected-text=TEXT] [--expected-url-contains=URL] [--expected-visible-selector=CSS] [--expected-value-selector=CSS --expected-value=VALUE] [--expected-count-selector=CSS --expected-count=N]")
		flags := parseFlags(args[2:])
		condition, err := navigatePostcondition(flags)
		if err != nil {
			exitOnErr(err)
		}
		var navigated *cliSession
		err = runOnePageWithPostcondition(args[1], flags, condition, func(s *cliSession) error {
			navigated = s
			return navigationPostconditionError(s.initialNavigation)
		})
		if navigated != nil {
			printJSON(cliNavigationOutput{
				PageResult: navigated.initialNavigation,
				ExecutionTiming: cliExecutionTiming{
					BrowserStartupMS:             navigated.browserStartupMS,
					PageNavigationVerificationMS: navigated.navigationMS,
					BrowserCleanupMS:             navigated.cleanupMS,
				},
			})
		}
		exitOnErr(err)

	case "click":
		requireArgs(args, 2, "scout click <url> <selector> [--target-id=ID] [--wait] [--expected-text=TEXT] [--expected-url-contains=URL] [--expected-visible-selector=CSS] [--expected-value-selector=CSS --expected-value=VALUE] [--expected-count-selector=CSS --expected-count=N]")
		flags := parseFlags(args[3:])
		condition, err := clickPostcondition(flags)
		if err != nil {
			exitOnErr(err)
		}
		wait, err := flags.getBool("wait", false)
		if err != nil {
			exitOnErr(err)
		}
		var clickErr error
		runOnePage(args[1], flags, func(s *cliSession) {
			result, err := s.session.ClickWithPostcondition(args[2], condition, wait)
			if err != nil {
				clickErr = err
				return
			}
			printJSON(result)
		})
		exitOnErr(clickErr)

	case "reconcile":
		requireArgs(args, 0, "scout reconcile --target-id=ID --action=ACTION --target=SELECTOR [--expected-text=TEXT | --expected-url-contains=URL | --expected-visible-selector=CSS | --expected-value-selector=CSS --expected-value=VALUE | --expected-count-selector=CSS --expected-count=N]")
		flags := parseFlags(args[1:])
		if os.Getenv("SCOUT_REMOTE_CDP") == "" {
			exitOnErr(fmt.Errorf("reconcile requires SCOUT_REMOTE_CDP to point to the still-running browser"))
		}
		allowed := map[string]struct{}{
			"target-id": {}, "action": {}, "target": {}, "timeout": {}, "headless": {}, "recovery-file": {}, "allow-private-ips": {},
			"expected-text": {}, "expected-url-contains": {}, "expected-visible-selector": {},
			"expected-value-selector": {}, "expected-value": {}, "expected-count-selector": {}, "expected-count": {},
		}
		for key := range flags.flags {
			if _, ok := allowed[key]; !ok {
				exitOnErr(fmt.Errorf("unknown reconcile flag --%s", key))
			}
		}
		outcome := &agent.ActionOutcomeError{Outcome: agent.OutcomeUnknown}
		if recoveryFile != "" {
			if flags.get("target-id", "") != "" || flags.get("action", "") != "" || flags.get("target", "") != "" {
				exitOnErr(fmt.Errorf("use either --recovery-file or explicit --target-id/--action/--target flags"))
			}
			if err := readRecoveryFile(recoveryFile, outcome); err != nil {
				exitOnErr(err)
			}
		} else {
			outcome.TargetID, outcome.Action, outcome.Target = flags.get("target-id", ""), flags.get("action", ""), flags.get("target", "")
			if outcome.TargetID == "" || outcome.Action == "" || outcome.Target == "" {
				exitOnErr(fmt.Errorf("reconcile requires --recovery-file or --target-id, --action, and --target"))
			}
		}
		conditionFlags := make(map[string]string)
		for _, key := range []string{"expected-text", "expected-url-contains", "expected-visible-selector", "expected-value-selector", "expected-value", "expected-count-selector", "expected-count"} {
			if value, ok := flags.flags[key]; ok {
				conditionFlags[key] = value
			}
		}
		if len(conditionFlags) > 0 {
			if recoveryFile != "" {
				exitOnErr(fmt.Errorf("postcondition is already stored in the recovery file"))
			}
			condition, err := parsePostcondition(cliFlags{flags: conditionFlags}, "reconcile")
			if err != nil {
				exitOnErr(err)
			}
			outcome.ExpectedPostcondition = &condition
		}
		timeout, err := flags.getDuration("timeout", 30*time.Second)
		if err != nil {
			exitOnErr(err)
		}
		allowPrivateIPs, err := flags.getBool("allow-private-ips", false)
		if err != nil {
			exitOnErr(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		session, err := agent.NewSessionContext(ctx, agent.SessionConfig{Timeout: timeout, AllowPrivateIPs: allowPrivateIPs, RemoteCDP: os.Getenv("SCOUT_REMOTE_CDP"), ProxyServer: configuredProxyServer(), TargetID: outcome.TargetID})
		cancel()
		exitOnErr(err)
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		result, reconcileErr := session.ReconcileActionRecoveryOutcomeContext(ctx, outcome)
		cancel()
		closeErr := session.Close()
		if reconcileErr != nil {
			exitOnErr(errors.Join(reconcileErr, closeErr))
		}
		exitOnErr(closeErr)
		printJSON(result)

	case "type":
		requireArgs(args, 3, "scout type <url> <selector> <text>")
		runOnePage(args[1], parseFlags(args[4:]), func(s *cliSession) {
			result, err := s.session.Type(args[2], args[3])
			exitOnErr(err)
			printJSON(result)
		})

	case "press-key":
		requireArgs(args, 2, "scout press-key <url> <key-or-chord> [--expected-text=TEXT | --expected-url-contains=URL | --expected-visible-selector=CSS | --expected-value-selector=CSS --expected-value=VALUE]")
		flags := parseFlags(args[3:])
		condition, err := parsePostcondition(flags, "press-key")
		if err != nil {
			exitOnErr(err)
		}
		runOnePage(args[1], flags, func(s *cliSession) {
			result, err := s.session.PressKeyWithPostcondition(args[2], condition)
			exitOnErr(err)
			printJSON(result)
		})

	case "key-down", "key-up":
		requireArgs(args, 2, "scout key-down|key-up <url> <key>")
		runOnePage(args[1], parseFlags(args[3:]), func(s *cliSession) {
			var result *agent.PageResult
			var err error
			if args[0] == "key-down" {
				result, err = s.session.KeyDown(args[2])
			} else {
				result, err = s.session.KeyUp(args[2])
			}
			exitOnErr(err)
			printJSON(result)
		})

	case "submit":
		requireArgs(args, 2, "scout submit <url> <form-selector> [--match-url=URL] [--expected-text=TEXT | --acceptance-text=TEXT]")
		flags := parseFlags(args[3:])
		options, err := parseSubmitOptions(flags)
		if err != nil {
			exitOnErr(err)
		}
		runOnePage(args[1], flags, func(s *cliSession) {
			var result *agent.SubmitFormResult
			var err error
			if options.hasAccept {
				result, err = s.session.SubmitFormWithAcceptance(args[2], options.matchURL, options.timeoutMS, options.acceptance)
			} else {
				result, err = s.session.SubmitFormWithPostcondition(args[2], options.matchURL, options.timeoutMS, options.expected)
			}
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
			exitOnErr(browse.WritePrivateFile(output, data))
			fmt.Printf("Saved screenshot to %s (%d bytes)\n", output, len(data))
		})

	case "pdf":
		requireArgs(args, 1, "scout pdf <url> [--output file.pdf]")
		flags := parseFlags(args[2:])
		output := flags.get("output", "page.pdf")
		runOnePage(args[1], flags, func(s *cliSession) {
			data, err := s.session.PDF()
			exitOnErr(err)
			exitOnErr(browse.WritePrivateFile(output, data))
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
			writeCLIError(fmt.Errorf("usage: scout form discover <url> [selector]"), "")
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
		requireArgs(args, 1, "scout watch <url> [--interval=5s] [--target-id=ID]")
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
		// One-shot read/diagnostic commands (accessibility, readable, table,
		// aria, vitals, console, dialog, auth-wall, cookies, app-state).
		if runReadCommand(args[0], args) {
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

// startMCPWithTelemetry optionally configures an OTLP/HTTP trace exporter for
// the lifetime of the MCP server. OTEL_EXPORTER_OTLP_* variables configure the
// exporter; the exporter is absent unless --otel=true is passed.
func startMCPWithTelemetry(httpAddr, profile, requiredTools, enabledRaw string, taskStorePaths ...string) {
	storePath := ""
	curated := false
	if len(taskStorePaths) > 0 {
		storePath = taskStorePaths[0]
	}
	if len(taskStorePaths) > 1 {
		var err error
		curated, err = strconv.ParseBool(taskStorePaths[1])
		if err != nil {
			exitOnErr(fmt.Errorf("invalid --curated value %q (use true or false)", taskStorePaths[1]))
		}
	}
	enabled, err := strconv.ParseBool(enabledRaw)
	if err != nil {
		exitOnErr(fmt.Errorf("invalid --otel value %q (use true or false)", enabledRaw))
	}
	if !enabled {
		if err := runMCPWithCurated(httpAddr, profile, requiredTools, storePath, curated); err != nil {
			writeCLIError(err, "")
			os.Exit(1)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exporter, err := otlptracehttp.New(ctx)
	exitOnErr(err)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName("scout"),
			semconv.ServiceVersion(version),
			attribute.String("service.instance.id", commit),
		)),
	)
	otel.SetTracerProvider(provider)
	serveErr := runMCPWithCurated(httpAddr, profile, requiredTools, storePath, curated)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	shutdownErr := provider.Shutdown(shutdownCtx)
	if serveErr != nil || shutdownErr != nil {
		exitOnErr(errors.Join(serveErr, func() error {
			if shutdownErr != nil {
				return fmt.Errorf("flush OpenTelemetry traces: %w", shutdownErr)
			}
			return nil
		}()))
	}
}

func printUsage() {
	fmt.Print(`scout — AI-powered browser automation

Usage:
  scout <command> [arguments]

One-shot commands open a browser, perform one action, and exit. Use
scout session for a long-running JSONL interface that preserves a browser
session across navigation, observation, frame and tab selection, clicks,
typing, and recovery. Private and loopback IP navigation is blocked by default;
pass --allow-private-ips only for trusted local-development targets.

Commands:
  navigate <url>                    Navigate and print page info as JSON
  click <url> <selector>            Click once; optionally verify one postcondition
  reconcile                         Recheck a prior unknown action on its live remote target
  type <url> <selector> <text>      Type into a field and verify its committed value
  press-key <url> <key-or-chord>    Press a key; optionally verify a page condition
  key-down <url> <key>              Hold a key until key-up or session cleanup
  key-up <url> <key>                Release a held key
  submit <url> <selector>           Submit a form; distinguish response from acceptance
  observe <url>                     Print structured page observation (links, inputs, buttons)
  markdown <url>                    Print page content as compact markdown
  readable <url>                    Print main readable content (nav/boilerplate stripped)
  accessibility <url>               Print the semantic accessibility tree
  screenshot <url> [--output f]     Save screenshot (default: screenshot.png)
  pdf <url> [--output f]            Save PDF (default: page.pdf)
  extract <url> <selector>          Extract text from element(s)
  table <url> <selector>            Extract a table as structured rows (JSON)
  auto-extract <url>                Auto-detect and extract the dominant data pattern
  eval <url> <expression>           Evaluate JavaScript on page
  form discover <url> [selector]    Discover form fields with labels
  frameworks <url>                  Detect frontend frameworks on page
  app-state <url>                   Extract global app/store state (JSON)
  aria <url>                        Accessibility (ARIA) violation report (JSON)
  vitals <url>                      Core Web Vitals report (JSON)
  console <url>                     Console errors/warnings + network 4xx/5xx (JSON)
  dialog <url>                      Detect a visible modal/dialog/overlay (JSON)
  auth-wall <url>                   Detect a login/auth wall (JSON)
  cookies <url>                     List cookies (names + flags, values redacted)
  watch <url> [--interval=5s] [--target-id=ID] [--allow-private-ips]  Live-watch page changes (Ctrl+C to stop)
  pipe <command> [selector] [--allow-private-ips]  Process URLs from stdin (one per line)
  record <url> [--output=file] [--allow-private-ips]  Interactive recording → playbook JSON
  mcp serve [--profile=NAME] [--require-tools=LIST] [--curated] [--otel]  Start MCP stdio (opt-in OTLP/HTTP traces)
  mcp serve-http [--listen=ADDR] [--profile=NAME] [--require-tools=LIST] [--curated] [--otel]  Start Streamable HTTP; uses standard OTEL_EXPORTER_OTLP_* variables
  ui serve [--allowed-origins=LIST] Start AG-UI; allow exact cross-origin UI origins
  session [--url=URL] [--headless] [--allow-private-ips] [--allowed-origins=LIST]  Keep one browser session for JSONL commands on stdin
  doctor [--json] [--live] [--live-timeout=5s] [--probe-url=URL] [--probe-text=TEXT] [--task-store=PATH] [--profile=NAME] [--require-tools=LIST] [--http-listen=ADDR]  Check setup; --live checks browser reachability
  version                           Print version information

Global flags:
  --headless                        Run without browser window (for scripts/CI)
  --timeout=30s                     Page operation timeout
  --allow-private-ips               Allow loopback/private IP targets (trusted local development only)
  --json-errors                     Print structured error envelopes to stderr
  --recovery-file=PATH              Save unknown action outcomes for later reconciliation

MCP Configuration:
  Claude Code:     claude mcp add scout -- scout mcp serve
  Claude Desktop:  {"mcpServers": {"scout": {"command": "scout", "args": ["mcp", "serve"]}}}
  HTTP clients:    http://127.0.0.1:4201/mcp (start with scout mcp serve-http)
  MCP profiles:    all (default), core, read, interact, diagnose, admin
  Require tools:   comma-separated MCP tool names that the selected profile must expose

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
		if recoveryFile != "" {
			if writeErr := persistRecoveryFile(recoveryFile, err); writeErr != nil {
				fmt.Fprintf(os.Stderr, "Could not write recovery file: %v\n", writeErr)
			}
		}
		writeCLIError(err, "")
		os.Exit(1)
	}
}

func writeCLIError(err error, url string) {
	if jsonErrors {
		fmt.Fprintln(os.Stderr, cliErrorJSONWithContext(err, url))
		return
	}
	if url != "" {
		fmt.Fprintf(os.Stderr, "%s: %v\n", url, err)
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

type cliErrorEnvelope struct {
	SchemaVersion         int                           `json:"schema_version,omitempty"`
	CreatedAt             string                        `json:"created_at,omitempty"`
	RunID                 string                        `json:"run_id,omitempty"`
	Step                  int                           `json:"step,omitempty"`
	OperationOrdinal      int                           `json:"operation_ordinal,omitempty"`
	Code                  string                        `json:"code"`
	Message               string                        `json:"message"`
	ContentTrust          string                        `json:"content_trust,omitempty"`
	Outcome               string                        `json:"outcome,omitempty"`
	Action                string                        `json:"action,omitempty"`
	Target                string                        `json:"target,omitempty"`
	TargetID              string                        `json:"target_id,omitempty"`
	PolicyID              string                        `json:"policy_id,omitempty"`
	PolicyEvidenceRef     string                        `json:"policy_evidence_ref,omitempty"`
	FrameID               string                        `json:"frame_id,omitempty"`
	TraceID               string                        `json:"trace_id,omitempty"`
	TraceStep             int                           `json:"trace_step,omitempty"`
	TraceActionOrdinal    int                           `json:"trace_action_ordinal,omitempty"`
	DeadlineAt            int64                         `json:"deadline_at_ms,omitempty"`
	ConfiguredTimeoutMS   int64                         `json:"configured_timeout_ms,omitempty"`
	SessionDead           *bool                         `json:"session_dead,omitempty"`
	ConsecutiveTimeouts   *int                          `json:"consecutive_timeouts,omitempty"`
	InFlightCommands      *int                          `json:"inflight_commands,omitempty"`
	URL                   string                        `json:"url,omitempty"`
	Evidence              string                        `json:"evidence,omitempty"`
	RecoveryHint          string                        `json:"recovery_hint,omitempty"`
	ExpectedPostcondition *agent.Postcondition          `json:"expected_postcondition,omitempty"`
	ResponseEvidence      *agent.ActionResponseEvidence `json:"response_evidence,omitempty"`
	ActionReceipt         *agent.ActionReceipt          `json:"action_receipt,omitempty"`
	Hint                  string                        `json:"hint,omitempty"`
	Phase                 string                        `json:"phase,omitempty"`
	Cause                 string                        `json:"cause,omitempty"`
	StatusCode            int                           `json:"status_code,omitempty"`
	Detail                string                        `json:"detail,omitempty"`
}

func boolPointer(value bool) *bool { return &value }
func intPointer(value int) *int    { return &value }

func boolValue(value *bool) bool {
	return value != nil && *value
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func extractRecoveryFileFlag(args []string) ([]string, string) {
	filtered := make([]string, 0, len(args))
	path := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--recovery-file=") {
			path = strings.TrimPrefix(arg, "--recovery-file=")
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, path
}

func persistRecoveryFile(path string, err error) error {
	var outcome *agent.ActionOutcomeError
	if !errors.As(err, &outcome) || outcome == nil || outcome.Outcome != agent.OutcomeUnknown || outcome.TargetID == "" {
		return nil
	}
	record, err := agent.NewActionRecoveryRecord(outcome)
	if err != nil {
		return fmt.Errorf("create action recovery record: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".scout-recovery-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp, path); err != nil {
		return err
	}
	return nil
}

func readRecoveryFile(path string, outcome *agent.ActionOutcomeError) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read recovery file: %w", err)
	}
	if record, recordErr := agent.DecodeActionRecoveryRecord(data); recordErr == nil {
		*outcome = *record.Outcome()
		return nil
	}
	// Accept schema v1 CLI artifacts for backwards compatibility.
	var envelope cliErrorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("decode recovery file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode recovery file: trailing JSON value")
		}
		return fmt.Errorf("decode recovery file: trailing content: %w", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Code != "SCOUT_ACTION_OUTCOME_UNKNOWN" || envelope.Outcome != string(agent.OutcomeUnknown) || envelope.TargetID == "" || envelope.Action == "" {
		return fmt.Errorf("recovery file is incomplete or uses an unsupported schema")
	}
	*outcome = agent.ActionOutcomeError{
		Outcome: agent.OutcomeUnknown, Action: envelope.Action, Target: envelope.Target,
		TargetID: envelope.TargetID, ExpectedPostcondition: envelope.ExpectedPostcondition,
		URL: envelope.URL, FrameID: envelope.FrameID, TraceID: envelope.TraceID, TraceStep: envelope.TraceStep,
		Evidence: envelope.Evidence, RecoveryHint: envelope.RecoveryHint,
		RunID: envelope.RunID, Step: envelope.Step, OperationOrdinal: envelope.OperationOrdinal, TraceActionOrdinal: envelope.TraceActionOrdinal, DeadlineAt: envelope.DeadlineAt,
		ConfiguredTimeoutMS: envelope.ConfiguredTimeoutMS,
		ConsecutiveTimeouts: intValue(envelope.ConsecutiveTimeouts), InFlightCommands: intValue(envelope.InFlightCommands),
		SessionDead: boolValue(envelope.SessionDead),
	}
	return nil
}

func cliErrorJSON(err error) string {
	return cliErrorJSONWithContext(err, "")
}

func redactErrorURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func cliErrorJSONWithContext(err error, url string) string {
	env := cliErrorEnvelope{Code: "SCOUT_ERROR", Message: err.Error()}
	var postconditionErr *pagePostconditionError
	if errors.As(err, &postconditionErr) && postconditionErr.result != nil {
		result := postconditionErr.result
		env.ContentTrust = result.ContentTrust
		env.Code = "SCOUT_POSTCONDITION_FAILED"
		env.Outcome = result.Outcome
		env.Action = result.Action
		env.Target = result.Target
		env.TargetID = result.TargetID
		env.FrameID = result.FrameID
		env.URL = redactErrorURL(result.URL)
		env.Evidence = result.Evidence
		env.RecoveryHint = result.RecoveryHint
		if result.Receipt != nil {
			env.ExpectedPostcondition = result.Receipt.Postcondition
		}
		env.ActionReceipt = result.Receipt
		data, marshalErr := json.Marshal(env)
		if marshalErr != nil {
			return `{"code":"SCOUT_ERROR","message":"failed to encode error"}`
		}
		return string(data)
	}
	var policyErr *browse.URLPolicyError
	if errors.As(err, &policyErr) {
		env.Code = policyErr.Code
		env.Message = "Request denied by browser URL policy."
		env.PolicyID = policyErr.PolicyID
		env.TargetID = policyErr.TargetID
		env.PolicyEvidenceRef = policyErr.EvidenceReference
		env.Hint = "Review the active origin and private-address policy, then retry only if the destination is intended."
		data, marshalErr := json.Marshal(env)
		if marshalErr != nil {
			return `{"code":"SCOUT_ERROR","message":"failed to encode error"}`
		}
		return string(data)
	}
	var taskConflict *agent.TaskRunConflictError
	if errors.As(err, &taskConflict) {
		env.Code = "SCOUT_TASK_ID_CONFLICT"
		env.Detail = taskConflict.Reason
		env.Hint = "Reuse the ID only for an identical plan; after reconciliation, decide whether to retry the uncertain action or continue, then use a new task ID for that chosen plan."
	}
	var taskCapacity *agent.TaskRunCapacityError
	if errors.As(err, &taskCapacity) {
		env.Code = "SCOUT_TASK_CAPACITY"
		env.Detail = fmt.Sprintf("limit=%d", taskCapacity.Limit)
		env.Hint = "No task IDs were evicted; start a fresh Scout session only after preserving any browser state you need, or use a durable task store."
	}
	var outcomeErr *agent.ActionOutcomeError
	if errors.As(err, &outcomeErr) {
		env.ContentTrust = agent.ContentTrustUntrustedPage
		env.Code = "SCOUT_ACTION_OUTCOME_UNKNOWN"
		env.Outcome = string(outcomeErr.Outcome)
		env.Action = outcomeErr.Action
		env.Target = outcomeErr.Target
		env.TargetID = outcomeErr.TargetID
		env.FrameID = outcomeErr.FrameID
		env.TraceID = outcomeErr.TraceID
		env.TraceStep = outcomeErr.TraceStep
		env.TraceActionOrdinal = outcomeErr.TraceActionOrdinal
		env.RunID = outcomeErr.RunID
		env.Step = outcomeErr.Step
		env.OperationOrdinal = outcomeErr.OperationOrdinal
		env.DeadlineAt = outcomeErr.DeadlineAt
		env.ConfiguredTimeoutMS = outcomeErr.ConfiguredTimeoutMS
		env.SessionDead = boolPointer(outcomeErr.SessionDead)
		env.ConsecutiveTimeouts = intPointer(outcomeErr.ConsecutiveTimeouts)
		env.InFlightCommands = intPointer(outcomeErr.InFlightCommands)
		env.URL = redactErrorURL(outcomeErr.URL)
		env.Evidence = outcomeErr.Evidence
		env.RecoveryHint = outcomeErr.RecoveryHint
		env.ExpectedPostcondition = outcomeErr.ExpectedPostcondition
		env.ResponseEvidence = outcomeErr.ResponseEvidence
		env.ActionReceipt = outcomeErr.Receipt
		env.Hint = outcomeErr.RecoveryHint
		env.Detail = fmt.Sprint(outcomeErr.Cause)
	} else {
		var operationErr *agent.OperationError
		if errors.As(err, &operationErr) {
			env.ContentTrust = agent.ContentTrustUntrustedPage
			env.Code = "SCOUT_OPERATION_ERROR"
			env.Hint = operationErr.RecoveryHint
			env.Phase = operationErr.Phase
			env.Cause = operationErr.Cause
			env.StatusCode = operationErr.StatusCode
			env.URL = redactErrorURL(operationErr.URL)
			env.Detail = operationErr.Detail
			env.Message = operationErr.OriginalError
			env.RecoveryHint = operationErr.RecoveryHint
		} else {
			var dispatchErr *cdp.DispatchOutcomeUnknownError
			if errors.As(err, &dispatchErr) {
				env.ContentTrust = agent.ContentTrustUntrustedPage
				env.Code = "SCOUT_ACTION_OUTCOME_UNKNOWN"
				env.Outcome = string(agent.OutcomeUnknown)
				env.Action = "cdp_command"
				env.Target = dispatchErr.Method
				env.Evidence = "command may have been dispatched; no authoritative response was received"
				env.RecoveryHint = "Inspect the current page and session state before retrying."
				env.Hint = env.RecoveryHint
				env.Detail = dispatchErr.Error()
			} else {
				var navigationErr *browse.NavigationOutcomeUnknownError
				if errors.As(err, &navigationErr) {
					env.ContentTrust = agent.ContentTrustUntrustedPage
					env.Code = "SCOUT_ACTION_OUTCOME_UNKNOWN"
					env.Outcome = string(agent.OutcomeUnknown)
					env.Action = "navigate"
					env.URL = redactErrorURL(navigationErr.URL)
					env.Evidence = "navigation may have been dispatched; no authoritative completion was received"
					env.RecoveryHint = "Inspect the current page URL and session state before retrying; navigation may already have completed."
					env.Hint = env.RecoveryHint
					env.Detail = navigationErr.Error()
				}
			}
		}
	}
	if env.URL == "" {
		env.URL = redactErrorURL(url)
	}
	if env.RecoveryHint == "" {
		env.RecoveryHint = env.Hint
	}
	data, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		return `{"code":"SCOUT_ERROR","message":"failed to encode error"}`
	}
	return string(data)
}

func extractJSONErrorsFlag(args []string) ([]string, bool) {
	filtered := make([]string, 0, len(args))
	enabled := false
	for _, arg := range args {
		if arg == "--json-errors" {
			enabled = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, enabled
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
