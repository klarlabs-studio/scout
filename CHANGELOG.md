# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.7.0] - 2026-05-20

### Added
- `submit_form` MCP tool — uses `form.requestSubmit()` so Vue `@submit.prevent`,
  React `onSubmit`, and native `@submit` listeners actually fire. Captures the
  resulting XHR/fetch (or URL change) and returns status + post-submit URL in
  one call, replacing click + sleep + check_readiness + network_requests.
- `wait_for_spa_idle` MCP tool — stronger SPA wait than `wait_spa`. Blocks
  until document complete + Astro `astro:page-load`/`astro:end` fired + Vue/
  React mount markers populated + no pending XHR/fetch + no visible spinners
  + the DOM has been mutation-free for `quiet_ms`. Closes the gap where
  `check_readiness` returns 100 too eagerly on hydrated Vue/Astro islands.
- `assert_text_contains` MCP tool — one-shot text-presence check with
  surrounding snippet on hit, page-tail snippet on miss. Replaces
  extract → string-match in caller code for post-action verification.
- `configure { fresh: true }` — kills the current browser process and starts
  a clean session. Recovery path for `session_dead: true` from `status`.
- `NodeActionError` classification — fill/click/type failures now carry a
  stable `class` (`field_missing` | `stale_node` | `hidden` | `disabled` |
  `unknown`) so callers can react without grepping CDP error strings.
- `SessionStatus.SessionDead` + `dead_reason` — surfaces broken pipe,
  connection reset, websocket closed so callers know to `configure { fresh: true }`
  rather than retrying into a dead socket.
- `IsStaleNodeError` + `Page.InvalidateNodeCache` exports on the `browse`
  package for callers wrapping their own Selection actions.
- `AnnotatedResult.LabelScope = "per_call"` — explicit contract that
  annotated-screenshot label numbers are recomputed every call.
- `docs/mcp-gotchas.md` — operational notes for selectors, network capture
  ordering, form submission, token budget, dead-socket recovery.

### Fixed
- Stale CDP node IDs after Vue/React reconciliation. `Page.QuerySelector`
  now invalidates the cached root nodeId and retries once on
  `cdp: error -32000: Could not find node with given id`; `Click`, `Type`,
  `Hover`, `FillForm`, and `fillTextField` re-resolve the selector and
  retry the action once on the same error. Removes the bulk of observed
  flake when driving SPAs.
- `has_element` vs action mismatch — both now go through the same
  resolver path; `has_element` invalidates the DOM cache first so it
  reflects current state, not a stale tree.
- `dispatch_event` with `event_type: "submit"` now calls
  `form.requestSubmit()` instead of dispatching a bare `Event('submit')`,
  so Vue `@submit.prevent` and React `onSubmit` listeners actually run
  and HTML5 validation fires.
- `dispatch_event` with `event_type: "click"` now calls `el.click()` so
  the default action (e.g. form submission for `type=submit`) runs.
- `screenshot` defaults to `max_width: 1024` when unset, keeping result
  comfortably under the MCP tool-result token cap without aggressive
  quality cuts.
- `network_requests` returns a structured result with `capture_enabled`
  and an explicit hint when the list is empty and capture was never
  enabled — instead of a silent empty array.

### Changed
- Tool count: 74 → 77 (added `submit_form`, `wait_for_spa_idle`,
  `assert_text_contains`).

## [1.0.5] - 2026-03-22

### Added
- Dialog/modal detection: `detect_dialog` tool finds `dialog[open]`, `aria-modal`, role=dialog, overlay modals
- `observe` now includes `has_dialog`/`dialog_type`/`dialog_text` fields
- Auto-pattern extraction: `auto_extract` detects repeating elements without selectors
- Infinite scroll collection: `scroll_and_collect` auto-scrolls and collects items
- Console error capture: `console_errors` surfaces JS errors/warnings
- Auth wall detection: `detect_auth_wall` identifies login walls, paywalls, CAPTCHAs
- File upload: `upload_file` via CDP DOM.setFileInputFiles
- Two-page comparison: `compare_tabs` diffs content between tabs
- Auto-detect Edge, Brave, Arc, Opera, Vivaldi browsers

### Fixed
- MCP screenshots capped at 200KB (~2.5k tokens) to avoid context overflow
- `click_label` accepts both string "8" and number 8
- Stripped internal "agent:" prefix from all error messages
- WaitStable hard timeout (3s max) prevents SPA hangs
- `observe`/`screenshot` accept optional `url` parameter
- `configure` no longer hangs with concurrent agents
- CI: fixed golangci-lint v2 config, Go version matrix, removed codecov

### Changed
- CLI defaults to visible browser (`--headless` to hide)

## [0.9.0] - 2026-03-22

### Added
- Cookie consent auto-dismiss: `dismiss_cookies` tries 30+ selectors and text patterns
- Page readiness signals: `check_readiness` returns 0-100 score with pending XHR/images/skeleton/spinner detection
- Selector suggestions: when a selector fails, returns 3 closest matching elements automatically
- Session history: `session_history` returns last N actions for conversation-aware context
- `scout watch <url>`: live-watch page changes with classified DOM diffs at configurable intervals
- `scout pipe <command>`: batch process URLs from stdin (extract, observe, markdown, screenshot, frameworks)
- `scout record <url>`: interactive recording — opens visible browser, saves actions as playbook JSON

## [0.8.0] - 2026-03-21

### Added
- MCP structured content: `OutputSchema` on observe tool for typed responses
- MCP channels: navigate pushes page info to `scout.navigation` channel
- MCP elicitation: available via `ElicitFromContext` for interactive prompts
- MCP dynamic tool registration: `NotifyToolListChanged` after navigate
- MCP progress reporting: navigate reports launch/load/done steps
- MCP tool annotations: `ReadOnly`/`OpenWorld`/`ClosedWorld`/`Idempotent` on all 46 tools
- Interaction tools: hover, double_click, right_click, select_option, scroll_to, scroll_by, focus, drag_drop
- Multi-tab coordination: open_tab, switch_tab, close_tab, list_tabs
- DOM diff classification: modal_appeared, form_error, notification, loading_complete, content_loaded
- Shadow DOM traversal: `QuerySelectorPiercing` crosses shadow boundaries
- Action cost estimation: links/buttons tagged high/medium/low in observe responses
- Action replay: start_recording, stop_recording, save_playbook, replay_playbook
- Playwright :text() selector support (translates to JS text-content lookup)
- Runtime configure tool (switch headless/visible without restart)
- Lazy session creation (browser starts on first tool use)

### Fixed
- Annotated screenshot no longer returns 147KB base64 by default (element list only)

### Changed
- CLI defaults to visible browser (--headless to hide)
- Upgraded mcp-go from v1.7.0 to v1.9.0

## [0.1.0] - 2026-03-21

### Added
- Gin-like browser automation API: Engine, Context, Group, HandlerFunc, Selection
- Pure CDP implementation over WebSocket (no rod/chromedp dependency)
- Agent package with structured JSON output for AI agents
- 29 MCP tools via `scout mcp serve`
- Full CLI: `scout navigate`, `observe`, `markdown`, `screenshot`, `pdf`, `extract`, `eval`, `form discover`, `frameworks`
- Middleware: Stealth, Retry, Timeout, CircuitBreaker, RateLimit, Bulkhead, Auth, Network
- Content distillation: Markdown, ReadableText, AccessibilityTree
- DOM diff tracking between observations
- Network request/response capture
- Semantic form filling (auto-matches labels to inputs)
- Token-budget-aware extraction
- Visual grounding (annotated screenshots with numbered labels)
- Persistent browser profiles (cookie/localStorage serialization)
- Screenshot auto-compression for LLM contexts (MaxSize enforcement)
- Video recording (screencast → MP4/GIF via ffmpeg)
- PDF generation
- Remote CDP connection support (Browserbase, Steel, self-hosted)
- Framework detection and state extraction (React, Vue, Angular, Svelte, Next.js, Nuxt, Remix, SvelteKit, Gatsby, Alpine, HTMX, Stimulus, Lit, Preact, Ember, Qwik, Astro, SolidJS)
- URL validation (SSRF protection)
- Task lifecycle state machine via statekit
- Structured logging via bolt
- GoReleaser + Homebrew distribution
