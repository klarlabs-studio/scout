# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.10.0] - 2026-05-24

### Added

- **Shadow DOM piercing across every action surface.** v1.9.0 landed
  piercing in form discovery + `resolveSelector`; v1.10.0 finishes
  the job by extending the deep-find walker to every remaining
  selector lookup so any Lit / Stencil / Web Components UI is fully
  driveable.
- **`wait.ForSelector` / `ForVisible` / `ForHidden`** poll through
  a `__scoutFind` walker that descends into open shadow roots.
  Fixes `click`, `type`, and every higher-level action that called
  `Page.WaitForSelector` first — they used to time out for 60s on
  shadow-rooted nodes because the wait predicate evaluated against
  the document scope only.
- **`Session.DispatchEvent`** routes `submit`, `click`, and the
  generic custom-event branch through the same piercing find.
  Lit form submission via `dispatchEvent("form", "submit")` now
  works end to end.
- **`Session.AnnotatedScreenshot` + `ClickLabel`** enumerate
  interactive elements via a shadow-DOM walker so the annotated
  label list includes buttons / inputs / anchors inside custom
  elements. `click_label` re-queries with the same walker.

### Tests

- `agent/shadow_dom_test.go` grows two new integration cases:
  - `TestShadowDOM_DispatchEvent_PiercesShadowRoots` — clicks a
    shadow-rooted button twice via DispatchEvent and asserts the
    counter wired inside the shadow root updates.
  - `TestShadowDOM_AnnotatedScreenshot_EnumeratesShadowChildren`
    — annotates a page whose only interactive element lives in a
    shadow root and asserts it shows up in the element list.
- `internal/wait/wait_test.go` snapshot tests updated to assert
  the new substring-based contract (`__scoutFind(document, ...)`)
  rather than the old single-line `document.querySelector`.

## [1.9.0] - 2026-05-24

### Added

- **Shadow DOM piercing in form discovery + fill.** `DiscoverForm`,
  `FillFormSemantic`, and every interaction tool that routes through
  `resolveSelector` (`type`, `click`, batch actions) now walk into
  open shadow roots. Inputs inside Lit / Stencil / Vue / React custom
  elements are discovered and fillable without changing call shape.
  Discovered inputs are tagged with a stable `data-scout-id` so
  subsequent fills round-trip across shadow boundaries.
- **Attribute selectors in `QuerySelectorPiercing`.** The flat-node
  matcher now handles `[attr="value"]` and `tag[attr="value"]` in
  addition to `#id`, `.class`, and bare tags. Lets piercing resolve
  the `data-scout-id` selectors emitted by form discovery without
  another evaluate round-trip.
- **`resolveSelector` falls back to piercing** before invoking the
  text / natural-language paths. Any selector that fails standard
  `document.querySelector` automatically retries against the flat
  DOM tree before the slower fallbacks run.

### Tests

- New `agent/shadow_dom_test.go` exercises the full discover → fill
  → click → submit loop against a custom element that wraps its form
  in an open shadow root.

## [1.8.0] - 2026-05-22

### Added
- `observe_scoped` MCP tool — limit observation to landmark roles
  (`nav`/`main`/`footer`/`header`/`aside`/`article`/`search`) or raw CSS
  selectors, with `limit_chars` / `links_limit` / `inputs_limit` /
  `buttons_limit` caps. Cuts token usage on listing pages.
- `submit_outcome` + `install_submit_tracker` MCP tools — diagnose silent
  form-submit failures. Returns `defaultPrevented`, the submitted form id,
  visible `[role=alert]` text, aria-invalid field labels, dev-server error
  overlay text (Vite/Next/Webpack), XHR count, and `navigation_committed`.
  Tracker auto-installs at every `Navigate` so the first submit is caught.
- `network_summary` MCP tool — rolled-up view of captured traffic with
  `total`, `by_status` (1xx/2xx/3xx/4xx/5xx/0), inline `failures`, `pending`
  count, and `capture_enabled` flag with hint. Replaces enable-capture +
  failed-requests + console-errors round-trip.
- `click_handle` MCP tool + `AnnotatedElement.NodeHandle` — `annotated_screenshot`
  now stamps a `data-scout-handle` attribute on every annotated element.
  Handles survive DOM mutations that would shuffle label numbers; clicking
  a stale handle returns a structured `stale_handle` `OperationError`.
- `wait_for_navigation` MCP tool — wait for the next full document load,
  SPA route change (History API push/replace/popstate), or either (mode
  `full`/`spa`/`any`). Use when an external action moves the page.
- `aria_violations` MCP tool — zero-dependency a11y scan grouped by impact.
  Catches `image-alt`, `button-name`, `link-name`, `label`, `duplicate-id`,
  `html-has-lang`, `landmark-one-main`. Run axe-core in your test pipeline
  for full WCAG coverage.
- Watchdog middleware in `cmd/scout` (`SCOUT_TOOL_TIMEOUT`, default 60s) —
  every MCP tool call runs in a goroutine with a per-call deadline. On
  timeout the RPC returns a structured `SCOUT_TIMEOUT` envelope instead
  of stalling the calling agent indefinitely.
- `MatchFormFieldWithScore` + `SemanticFieldResult.MatchConfidence` —
  matcher's 0–100 score exposed so callers can flag low-confidence
  resolutions. `FillFormSemantic` warns when the score < 50.
- `ElementResult.ValueCommitted` / `FrameworkReactive` / `FrameworkDetected`
  on `Type` — re-reads the input via the prototype getter after a microtask
  flush, so React's stateful value tracker doesn't return stale strings,
  and surfaces whether the framework actually committed the write.

### Fixed
- `fill_form_semantic` mapped values to the wrong input when two fields
  shared the same generic selector (Vue v-model / React controlled inputs
  with no `id`/`name`). `DiscoverForm` now produces a unique CSS path via
  `:nth-of-type` from the nearest stable ancestor, with a defensive
  re-check that any heuristic-built selector resolves to exactly one node.
- Vue/React hydration race: `Type` lost its input on `client:visible`
  islands when the framework's listeners weren't attached yet. `Type` now
  polls for Vue (`__vueParentComponent`/`__vue_app__`) and React
  (`__reactProps$*`/`__reactFiber$*`) hooks before typing, dispatches
  synthetic `input` + `change` + `blur` after the CDP keystrokes, and
  re-reads via the prototype getter.
- `observe` returned `text: ""` for card-style anchors (anchor wraps image
  + heading block, no direct text node). New accessible-name fallback:
  direct text → `aria-label` → `aria-labelledby` → first nested heading
  → first `<img alt>` → `title` → URL slug.
- `NewPageAt` could return before Chrome committed the first navigation,
  so callers reading `URL()` immediately saw `about:blank`. Now waits for
  load (best-effort; failures still return the page).

### Changed
- `cmd/scout` MCP server wires the watchdog middleware via
  `mcp.ServeStdio(ctx, srv, mcp.WithMiddleware(watchdogMiddleware(...)))`.
- CI nox version bumped to `0.10.0` (drops a 0.9.5 false-positive
  `Secrets:1` finding on `cmd/scout/mcp.go`).

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
