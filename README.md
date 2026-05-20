<p align="center">
  <img src="docs/logo-400.png" alt="Scout" width="160">
</p>

<h1 align="center">Scout</h1>

<p align="center"><strong>Browser automation, one binary.</strong> The simpler alternative to Playwright — no Node, no Python, no runtime. Drive a real browser from Go, any shell, any AI agent (built-in MCP server), or a chat UI.</p>

<p align="center">
  <a href="https://github.com/felixgeelhaar/scout/releases"><img src="https://img.shields.io/github/v/release/felixgeelhaar/scout?style=flat-square&color=3b82f6" alt="Release"></a>
  <a href="https://github.com/felixgeelhaar/scout/blob/main/LICENSE"><img src="https://img.shields.io/github/license/felixgeelhaar/scout?style=flat-square" alt="License"></a>
  <a href="https://github.com/felixgeelhaar/scout/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/felixgeelhaar/scout/ci.yml?style=flat-square&label=CI" alt="CI"></a>
  <a href="https://pkg.go.dev/github.com/felixgeelhaar/scout"><img src="https://img.shields.io/badge/go.dev-reference-007d9c?style=flat-square" alt="Go Reference"></a>
  <img src="https://img.shields.io/badge/coverage-80%25-brightgreen?style=flat-square" alt="Coverage">
  <img src="docs/nox-badge.svg?v=2" alt="Security">
</p>

A single statically-linked `scout` binary gives you a CLI, a 77-tool MCP server (so any MCP-aware agent — Claude Desktop, Cursor, Cline, custom — has a browser), a conversational chat UI, and a Go library with Gin-like middleware composition. Same engine, four access points.

```bash
brew install felixgeelhaar/tap/scout
```

## vs. Playwright

| | Scout | Playwright |
|---|---|---|
| Install | one ~15 MB binary | npm + ~600 MB browser cache |
| Runtime dep | **none** (static) | Node.js always; Python/Java/.NET as wrappers |
| Drive from | Go, any shell, MCP, chat UI | TS/JS first-class; others lag |
| AI-agent native | **built-in** `scout mcp serve` | separate `playwright-mcp` project |
| Token-aware extraction | DOM diff, distillation, observation budgets (50–80% fewer tokens) | not provided |
| Action playbooks | record & replay deterministic JSON | codegen produces a script you maintain |
| Container deploy | drop into `scratch` or `distroless` | carry Node + browser binaries |
| CDP access | direct WebSocket, zero abstraction | internal protocol over CDP |

## Quick Start

```bash
# CLI — visible browser, one-shot commands
scout observe https://example.com          # structured page snapshot
scout markdown https://news.ycombinator.com # page as compact markdown
scout screenshot https://github.com         # save screenshot
scout extract https://example.com h1        # extract element text
scout frameworks https://react.dev          # detect React, Vue, etc.

# MCP Server — give AI agents browser superpowers
claude mcp add scout -- scout mcp serve

# Browser UI — conversational browser automation
scout ui serve --provider=ollama --model=mistral
cd ui && npm install && npm run dev  # open http://localhost:3000
```

## Install

```bash
# Homebrew
brew install felixgeelhaar/tap/scout

# Direct binary
curl -fsSL https://raw.githubusercontent.com/felixgeelhaar/scout/main/install.sh | bash

# Go
go install github.com/felixgeelhaar/scout/cmd/scout@latest

# As a library
go get github.com/felixgeelhaar/scout
```

## MCP Server — 77 Tools

Run `scout mcp serve` and any MCP-aware agent has a browser. No second project to install, no Node runtime, no Python interpreter — the binary is the server. Configure in any MCP client:

```bash
claude mcp add scout -- scout mcp serve           # Claude Code
```

```json
{"mcpServers": {"scout": {"command": "scout", "args": ["mcp", "serve"]}}}
```

### Tool Categories

| Category | Tools |
|----------|-------|
| **Navigation** | `navigate`, `observe`, `observe_diff`, `observe_with_budget` |
| **Interaction** | `click`, `click_label`, `click_text`, `type`, `hover`, `double_click`, `right_click`, `select_option`, `scroll_to`, `scroll_by`, `focus`, `drag_drop`, `dispatch_event` |
| **Forms** | `fill_form`, `fill_form_semantic` (checkbox/radio + state echo), `discover_form` |
| **Extraction** | `extract`, `extract_all`, `extract_table`, `auto_extract`, `scroll_and_collect`, `markdown`, `readable_text`, `accessibility_tree` |
| **Capture** | `screenshot`, `annotated_screenshot`, `pdf` |
| **Network** | `enable_network_capture`, `network_requests` |
| **Tabs** | `open_tab`, `switch_tab`, `close_tab`, `list_tabs` |
| **Frameworks** | `wait_spa`, `detect_frameworks`, `component_state`, `app_state` |
| **Playback** | `start_recording`, `stop_recording`, `save_playbook`, `replay_playbook` |
| **Video** | `start_screen_recording`, `stop_screen_recording` |
| **Smart Helpers** | `check_readiness`, `suggest_selectors`, `session_history` |
| **Vision** | `hybrid_observe`, `find_by_coordinates` |
| **Batch** | `execute_batch` |
| **Iframe** | `switch_to_frame`, `switch_to_main_frame` |
| **Trace** | `start_trace`, `stop_trace` |
| **Cookies** | `cookies_list`, `cookies_clear`, `cookies_set`, `dismiss_cookies` |
| **Diagnostics** | `detect_dialog`, `detect_auth_wall`, `console_errors` (incl. network 4xx/5xx), `failed_requests`, `compare_tabs`, `upload_file` |
| **Utility** | `has_element`, `wait_for`, `configure`, `web_vitals`, `select_by_prompt` |

All tools have MCP annotations (`ReadOnly`, `OpenWorld`, `ClosedWorld`, `Idempotent`) for smart auto-approval. Read-only tools like `observe`, `extract`, and `screenshot` run without permission prompts.

### Runtime Configuration

Switch between headless and visible browser without restarting, and opt into local-dev workflows (loopback, private IPs):

```
Agent: configure(headless: false)                        → browser window appears
Agent: navigate("https://...")                           → watch it work
Agent: configure(headless: true)                         → back to headless
Agent: configure(allow_private_ips: true)                → unlock localhost / 192.168.* / 10.*
Agent: navigate("http://127.0.0.1:4173/")                → drive your local dev server
```

The MCP server also reads `SCOUT_ALLOW_PRIVATE_IPS=1` at startup as a one-shot toggle for trusted environments.

### Screen Recording (video)

Record the active page as a video. Pure CDP — works in headless, no Playwright needed. Recording survives `navigate`, `open_tab`, and `switch_tab` calls in between, so a multi-page demo lands as one continuous clip:

```
Agent: start_screen_recording({ width: 1280, height: 800, fps: 15, format: "webm" })
Agent: navigate("https://example.com")
Agent: click("#cta")
Agent: navigate("https://example.com/dashboard")   # recording continues across pages
Agent: stop_screen_recording()
       → { path: "/tmp/scout-rec-XXX.webm", format: "webm", encoder: "ffmpeg",
           frame_count: N, duration_ms: N }
```

If `ffmpeg` is on PATH, the result is encoded to WebM (libvpx-vp9) or MP4 (libx264). If not, scout returns the raw JPEG frames directory plus an ffmpeg concat list so you can encode offline. The result is always a file path, never base64 — never enters your LLM token budget.

Realistic FPS: ~10–15 on typical pages, capped at 30. Implementation polls `Page.captureScreenshot` (CDP `Page.startScreencast` events are silently dropped under `--headless=new` Chrome).

## Browser UI

A conversational browser automation interface. Type natural language, watch the browser respond in real-time.

```bash
# Start the AG-UI server (Go backend)
scout ui serve --provider=ollama --model=mistral    # local, no API key
scout ui serve --provider=claude                     # needs ANTHROPIC_API_KEY
scout ui serve --provider=openai --model=gpt-4o     # needs OPENAI_API_KEY
scout ui serve --provider=groq --base-url=https://api.groq.com/openai --model=llama-3.3-70b-versatile

# Start the Vue frontend
cd ui && npm install && npm run dev                  # http://localhost:3000
```

The UI streams AG-UI protocol events over SSE:
- **Chat panel** with markdown rendering and quick-action pills
- **Live browser viewport** with screenshot streaming and URL bar
- **Activity timeline** showing tool calls in real-time
- **Stop button** to cancel mid-stream

The Go server handles the agentic loop: LLM decides which scout tools to call, executes them, streams browser state deltas back to the frontend. Supports any OpenAI-compatible endpoint via `--base-url`.

## Agent Package (Go)

High-level Go API for callers that want to embed scout in a program. Structured output, auto-wait, goroutine-safe. Most users reach scout through the CLI or MCP server above — this section is for the Go-library path.

```go
session, _ := agent.NewSession(agent.SessionConfig{Headless: true})
defer session.Close()

// Navigate and observe
session.Navigate("https://example.com")
obs, _ := session.Observe()               // links, inputs, buttons, text + action costs

// DOM diff — only what changed (saves 50-80% tokens)
session.Click("#submit")
_, diff, _ := session.ObserveDiff()
// diff.Classification: "modal_appeared"
// diff.Summary: "Modal/dialog appeared: Login required"

// Semantic form filling — no CSS selectors
session.FillFormSemantic(map[string]string{
    "Email": "user-example", "Password": "secret",
})

// Visual grounding — click by number, not selector
result, _ := session.AnnotatedScreenshot()  // numbered labels on elements
session.ClickLabel(7)                        // click element [7]

// Multi-tab coordination
session.OpenTab("pricing", "https://example.com/pricing")
session.SwitchTab("default")

// Framework detection (19 frameworks)
frameworks, _ := session.DetectedFrameworks() // ["react", "nextjs"]
state, _ := session.ComponentState("#app")    // read React/Vue state

// Network capture — read API responses directly
session.EnableNetworkCapture("/api/")
captured := session.CapturedRequests("/api/users")

// Action replay — record once, replay without LLM
session.StartRecordingPlaybook("login-flow")
// ... do stuff ...
pb, _ := session.StopRecordingPlaybook()
agent.SavePlaybook(pb, "login.json")
// Later: session.ReplayPlaybook(pb)  // 100x cheaper

// Persistent profiles
session.SaveProfile("session.json")   // cookies + localStorage
session.LoadProfile("session.json")

// Content distillation (5 levels)
session.Markdown()          // ~2-8KB compact markdown
session.ReadableText()      // ~1-4KB main content only
session.AccessibilityTree() // ~1-4KB semantic tree
session.ObserveWithBudget(500) // fit in ~500 tokens
```

## Core Library (Go)

Gin-like Engine/Context/Group/HandlerFunc with middleware composition. The lowest-level Go API — use it when you want full control of task lifecycle, named groups, and middleware chains:

```go
engine := browse.Default(browse.WithHeadless(true))
engine.MustLaunch()
defer engine.Close()

engine.Use(middleware.Stealth())
engine.Use(middleware.Retry(middleware.RetryConfig{MaxAttempts: 3}))
engine.Use(middleware.Timeout(30 * time.Second))

admin := engine.Group("admin", middleware.BasicAuth("admin", "secret"))
admin.Task("export", func(c *browse.Context) {
    c.MustNavigate("https://app.example.com/admin")
    table, _ := c.ExtractTable("#users")
    c.Set("data", table)
})

engine.RunGroup("admin")
```

### Middleware

| Category | Middleware |
|----------|-----------|
| **Resilience** | `Retry`, `Timeout`, `CircuitBreaker`, `RateLimit`, `Bulkhead` |
| **Auth** | `BearerAuth`, `BasicAuth`, `CookieAuth`, `HeaderAuth` |
| **Anti-detection** | `Stealth` (10 patches: webdriver, plugins, WebGL, etc.) |
| **Network** | `BlockResources`, `WaitNetworkIdle` |
| **Utilities** | `ScreenshotOnError`, `SlowMotion`, `Viewport` |

## CLI

CLI defaults to visible browser (`--headless` to hide):

```bash
scout navigate <url>                  # page info as JSON
scout observe <url>                   # structured observation
scout markdown <url>                  # compact markdown
scout screenshot <url> [--output f]   # save screenshot
scout pdf <url> [--output f]          # save PDF
scout extract <url> <selector>        # extract text
scout eval <url> <expression>         # run JavaScript
scout form discover <url>             # discover form fields
scout frameworks <url>                # detect frameworks
scout watch <url> [--interval=5s]     # live-watch page changes
scout pipe <command> [selector]       # batch process URLs from stdin
scout record <url> [--output f]       # interactive recording → playbook
scout mcp serve                       # start MCP server
scout version                         # print version
```

## Architecture

```
scout/
├── browse.go, engine.go, context.go   # Gin-like API
├── page.go, selection.go              # CDP page & element interaction
├── recorder.go                        # Action playbook recording (Navigate/Click/Type → JSON)
├── middleware/                        # stealth, resilience, auth, network
├── agent/                             # AI agent API (50+ methods)
│   ├── session.go                     # Session lifecycle, Navigate, Click, Type
│   ├── observe.go, diff.go            # Observe, ObserveDiff, cost estimation
│   ├── content.go                     # Markdown, ReadableText, AccessibilityTree
│   ├── form.go                        # DiscoverForm, FillFormSemantic, MatchFormField
│   ├── annotate.go                    # AnnotatedScreenshot, ClickLabel
│   ├── network.go                     # EnableNetworkCapture, CapturedRequests
│   ├── spa.go                         # DetectedFrameworks, ComponentState, GetAppState
│   ├── tabs.go                        # OpenTab, SwitchTab, CloseTab, ListTabs
│   ├── playbook.go                    # StartRecording, ReplayPlaybook, SavePlaybook
│   ├── interact.go                    # Hover, DragDrop, SelectOption, ScrollTo
│   ├── profile.go                     # CaptureProfile, ApplyProfile, SaveProfile
│   ├── selector.go                    # Playwright :text() selector translation
│   ├── budget.go                      # ObserveWithBudget, EstimateTokens
│   ├── nlselect.go                    # SelectByPrompt, fuzzy NL element matching
│   ├── batch.go                       # ExecuteBatch, sequential multi-action
│   ├── vision.go                      # HybridObserve, FindByCoordinates
│   ├── trace.go                       # StartTrace, StopTrace, action tracing
│   ├── screencast.go                  # StartScreenRecording / StopScreenRecording — video via captureScreenshot polling + ffmpeg encode
│   ├── iframe.go                      # SwitchToFrame, SwitchToMainFrame
│   └── vitals.go                      # WebVitals (LCP/CLS/INP)
├── internal/cdp/                      # WebSocket CDP client (context-aware)
├── internal/launcher/                 # Chrome process management
├── cmd/scout/                         # CLI + MCP server (77 tools)
└── docs/                              # Landing page (GitHub Pages)
```

## Security

Vulnerability scanning runs on every push and PR via [`nox`](https://github.com/nox-hq/nox). Findings are uploaded to GitHub code scanning, annotated inline on PRs, and gated against `.nox/baseline.json` so regressions block merges. The status badge in the header reflects the latest main-branch scan.

`nox` also drives dependency remediation in place of Dependabot — the [Nox Remediate](.github/workflows/nox-remediate.yml) workflow runs weekly (Monday 06:00 UTC) and on demand, executing `nox fix` against fresh OSV.dev findings and opening a single PR with the verified upgrades.

```bash
# Local scan
nox scan -severity-threshold high .

# Local fix
nox fix -input findings.json
```

## License

MIT
