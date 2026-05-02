# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

scout is a Gin-like browser automation library for Go using a pure Chrome DevTools Protocol (CDP) implementation over WebSocket. No rod, no chromedp. It has two API layers: a core `browse` package (Engine/Context/Group/HandlerFunc for developers) and an `agent` package (Session-based, structured-output API for AI agents). Includes an MCP server binary at `cmd/scout` and a conversational browser UI at `cmd/scout ui serve`.

## Commands

```bash
# Build & verify
go build ./...
go vet ./...
golangci-lint run --timeout 2m . ./cmd/... ./middleware/... ./internal/...  # excludes examples/

# Tests (Chrome required for integration tests)
go test -short ./...                              # unit tests only, no Chrome
go test ./...                                     # all tests (unit + integration)
go test -run TestIntegrationClick ./...            # single test
go test -run TestIntegration -timeout 120s ./...   # all integration tests
go test -v -race -timeout 300s ./agent/...         # agent package with race detector

# Coverage
make cover-check   # runs tests + coverctl policy enforcement

# Pre-commit hook (gofmt, vet, lint, unit tests, coverctl, nox baseline)
make hooks         # install
bash scripts/pre-commit.sh  # run manually

# AG-UI server (conversational browser UI)
scout ui serve --provider=ollama --model=mistral   # local LLM
scout ui serve --provider=claude                    # Claude (needs ANTHROPIC_API_KEY)
scout ui serve --provider=openai                    # OpenAI (needs OPENAI_API_KEY)
cd ui && npm install && npm run dev                 # Vue frontend at :3000
```

## Architecture

**Three interfaces, one CDP engine:**

The root `browse` package follows Gin's patterns — `Engine` manages browser lifecycle, `Context` carries page state through a `HandlerFunc` middleware chain, `Group` organizes tasks with shared middleware, `Selection`/`SelectionAll` wrap DOM elements. The `agent` package wraps all of this into a single `Session` type with structured JSON-serializable responses, auto-wait, content distillation, and mutex-protected concurrency safety.

The `internal/agui` package adds a third interface: an AG-UI protocol HTTP server (`scout ui serve`) that streams SSE events to a Vue frontend. An LLM (Claude, OpenAI, or Ollama) interprets user messages and calls scout tools via an agentic loop. Browser state (URL, title, screenshot) streams to the frontend as JSON Patch deltas.

**CDP data flow:**

`Page.call(method, params)` → `Conn.CallSessionCtx(ctx, sessionID, method, params)` — every CDP command is scoped to a session ID and carries a `context.Context` for cancellation. Events flow back through `Conn.dispatchEvent` which filters by `sessionID` before invoking handlers. `Page.Close()` cancels its context, removes all session-scoped event handlers, and closes the CDP target.

**Key internal contracts:**

- `Page.getRootNodeID()` caches the DOM document root node ID. It is invalidated when `Navigate()` is called (sets `rootNodeID = 0`). This halves CDP round-trips for `QuerySelector`/`QuerySelectorAll`.
- `Page.Navigate()` validates URLs via `URLValidator` — blocks non-http(s) schemes and private IPs by default. Tests must use `WithAllowPrivateIPs(true)`.
- Resilience middleware (Retry, Timeout, CircuitBreaker, Bulkhead) uses `c.SaveIndex()`/`c.RestoreIndex()` to replay the downstream handler chain. `RestoreIndex` clears `errors` and `aborted` but preserves `keys` — data set by prior handlers survives retries.
- `agent.Session` holds a `sync.Mutex` and locks on every public method. Internal helpers (`ensurePage`, `observeInternal`, `pageResult`, `discoverFormInternal`) are called with the lock held — they must not re-lock.
- The `internal/wait` package provides the polling implementation. `Page.WaitLoad()` and `Page.WaitForSelector()` delegate to `wait.ForLoad()` and `wait.ForSelector()`.
- MCP eval tool is gated behind `SCOUT_ENABLE_EVAL=1` env var due to arbitrary code execution risk.
- MCP server uses lazy session creation — browser starts on first tool use, not at startup. `configure` tool changes settings without restart.
- Playwright-style selectors (`:text('...')`, `:has-text('...')`) are translated to JS text-content lookup via `agent/selector.go`.
- `annotated_screenshot` returns element list only by default (no base64 image). Set `include_image: true` for the image.
- Action replay: `recordAction()` is called inside Navigate/Click/Type when `s.recording != nil`. Playbooks validate expected outcomes.
- Multi-tab: `tabManager` tracks named pages. Default page becomes "default" tab when `OpenTab` is first called.
- DOM diff classification: `classifyDiff()` categorizes mutations as modal_appeared, form_error, notification, loading_complete, etc.
- Action cost: `estimateLinkCost`/`estimateButtonCost` tag elements as high/medium/low in Observe responses.
- Cookie dismiss: `agent/cookies.go` tries 30+ CSS selectors then text-pattern matching on visible buttons.
- Selector suggestions: `resolveSelector` in `agent/selector.go` auto-suggests similar elements on failure via `suggestSelectorsInternal`.
- Page readiness: `agent/readiness.go` scores 0-100 based on readyState, pending images, skeletons, spinners.
- Session history: `agent/history.go` ring buffer of last 20 actions, appended in Navigate/Click/Type via `addHistory`.
- CLI watch/pipe/record in `cmd/scout/watch.go` — watch uses ObserveDiff polling, pipe reuses one session across URLs, record uses StartRecordingPlaybook.
- `agent/nlselect.go`: `SelectByPrompt` uses JS fuzzy text matching against interactive elements. Falls back from `resolveSelector` when input looks like natural language.
- `agent/batch.go`: `ExecuteBatch` acquires mutex once, runs multiple actions sequentially. Uses internal helpers (`batchClick`, `batchType`) to avoid re-locking.
- `agent/vision.go`: `HybridObserve` returns clean screenshot + element bounding boxes. `FindByCoordinates` does point-in-rect hit test.
- `agent/trace.go`: `StartTrace`/`StopTrace` capture before/after screenshots per action, export as zip with `trace.json` + `screenshots/` + `network.json`.
- `agent/screencast.go`: `StartScreenRecording`/`StopScreenRecording` produce a video. Uses **polled `Page.captureScreenshot`** in a goroutine (CDP `Page.startScreencast` events are silently dropped under `--headless=new`). FPS capped at 30 (~15 realistic). The capture loop holds a `*Session`, not a `*browse.Page`, and resolves `s.page` under the session mutex on each tick — that's how the recording survives `Navigate`/`OpenTab`/`SwitchTab` page swaps. On stop, writes ffmpeg concat list with real per-frame timestamps and shells out to `ffmpeg` (libvpx-vp9 for webm, libx264 for mp4). Falls back to frames-only if ffmpeg is missing. Always returns a file path, never base64.
- `agent/iframe.go`: `SwitchToFrame` gets iframe execution context via `Page.createIsolatedWorld`. `SwitchToMainFrame` resets.
- `agent/vitals.go`: `WebVitals` extracts LCP/CLS/INP via PerformanceObserver API.
- Stealth v2 adds canvas/audio fingerprint noise, WebRTC leak prevention, UA rotation.
- `QuerySelectorPiercing` now uses `DOM.getFlattenedDocument` cache with `pierce:true`.

**Screenshot compression:** `ScreenshotWithOptions` with `MaxSize` set progressively re-captures as JPEG with lower quality (80→60→40→20) and smaller scale (1.0→0.75→0.5→0.25) until the image fits under the byte limit. `agent.Session.Screenshot()` defaults to a 5MB limit.

**AG-UI server (`internal/agui/`):**

- `server.go`: HTTP server with CORS, POST `/` handler, health check at `/health`.
- `handler.go`: Agentic loop — LLM → tool calls → `ExecuteTool` → `STATE_DELTA` → repeat until text-only response. Max 10 tool loops per run.
- `events.go`: Self-contained AG-UI SSE encoder (no SDK dependency). 16 event types incl. `RUN_BUDGET_EXHAUSTED` (10-hop ceiling hit) and `RUN_REPEATED_CALL` (3 identical (name, args) tool invocations in a row → loop terminates early — small-model failure mode guard).
- `tools.go`: `CuratedTools()` (20 tools for large models) and `CoreTools()` (6 tools for small/local models like Ollama). `ExecuteTool` maps tool names to `agent.Session` methods. Tool results carrying scraped page text (`observe`, `observe_diff`, `extract`, `extract_table`, `markdown`, `discover_form`) are wrapped via `marshalUntrusted` — `{"_untrusted_page_content": true, "_warning": "...", "data": ...}` — and the system prompt tells the LLM to treat that `data` strictly as data. Defends against page-borne prompt injection.
- `llm_claude.go` / `llm_openai.go`: Streaming LLM providers. OpenAI provider works with Ollama, Groq, Together, etc.
- `sessions.go`: Thread→Session map with 10-minute idle cleanup. One browser per thread.
- `state.go`: `BrowserState` struct + `Diff()` for JSON Patch (RFC 6902) delta generation.
- `ui/`: Vue 3 + Vite frontend. `useAgentStream` composable consumes raw SSE. Zero framework deps beyond Vue.

## Key Dependencies

| Package | Role |
|---------|------|
| `felixgeelhaar/bolt` | Logger/Recovery middleware (zero-alloc structured logging) |
| `felixgeelhaar/fortify` | Retry, Timeout, CircuitBreaker, RateLimit, Bulkhead middleware |
| `felixgeelhaar/statekit` | Task lifecycle state machine (pending→running→success/failed/aborted) |
| `felixgeelhaar/mcp-go` | MCP server framework for `cmd/scout` |
| `gorilla/websocket` | CDP WebSocket transport |

## Lint Configuration

golangci-lint v2 config (`.golangci.yml`): `tests: false` excludes test files from linting. The `examples/` directory is excluded via `exclude-dirs`. Lint targets must be explicit: `. ./cmd/... ./middleware/... ./internal/...` (not `./...` which includes examples).
