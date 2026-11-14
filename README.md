<p align="center">
  <img src="docs/logo-400.png" alt="Scout" width="160">
</p>

<h1 align="center">Scout</h1>

<p align="center"><strong>Browser automation from one Go binary.</strong> A local-first browser execution engine for Go apps and AI agents. Verify actions, bound outputs, and replay known workflows through the Go API, CLI, MCP server, or chat UI.</p>

<p align="center">
  <a href="https://github.com/klarlabs-studio/scout/releases"><img src="https://img.shields.io/github/v/release/klarlabs-studio/scout?style=flat-square&color=3b82f6" alt="Release"></a>
  <a href="https://github.com/klarlabs-studio/scout/blob/main/LICENSE"><img src="https://img.shields.io/github/license/klarlabs-studio/scout?style=flat-square" alt="License"></a>
  <a href="https://github.com/klarlabs-studio/scout/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/klarlabs-studio/scout/ci.yml?style=flat-square&label=CI" alt="CI"></a>
  <a href="https://pkg.go.dev/go.klarlabs.de/scout"><img src="https://img.shields.io/badge/go.dev-reference-007d9c?style=flat-square" alt="Go Reference"></a>
  <img src="https://img.shields.io/badge/coverage-80%25-brightgreen?style=flat-square" alt="Coverage">
  <a href="https://github.com/klarlabs-studio/scout/security/code-scanning"><img src="https://img.shields.io/badge/security-nox-22c55e?style=flat-square" alt="Security (nox)"></a>
</p>

A single statically-linked `scout` binary gives you a CLI, an MCP server with browser automation tools (so any MCP-aware agent — Claude Desktop, Cursor, Cline, custom — has a browser), a conversational chat UI, and a Go library with Gin-like middleware composition. Same engine, four access points.

```bash
brew install --cask klarlabs-studio/tap/scout-mcp
```

## Where Scout fits

Scout is a Go browser execution engine with a built-in MCP server. Its CLI and MCP server use the same Go engine, and its core browser actions do not require an inference provider. A local run still needs Chrome or Chromium; Scout can also attach to a separately managed browser over CDP. The published container image uses `headless-shell` as its browser base.

Choose the tool around the workflow you need:

| If you need… | Consider… |
|---|---|
| An embeddable Go engine, a built-in MCP server, explicit action evidence, recorded deterministic playbooks, or a one-shot CLI action with a postcondition | **Scout** |
| A Go worker must reconcile a browser mutation after a timeout or process restart | **Scout task runs** with a durable task store; inspect the saved task, check your authoritative application state, and record the host's conclusion before using a new task ID |
| Cross-browser automation and a mature testing ecosystem in TypeScript, Python, Java, or .NET, or Playwright's agent-oriented CLI/MCP workflows | [Playwright](https://playwright.dev/) and [Playwright MCP](https://playwright.dev/mcp). Playwright MCP supports scoped and depth-limited snapshots, search, and saving snapshots to files. |
| A terminal-first browser CLI with persistent sessions, agent skills, provider integrations, and experimental saved flows that try HTTP, page, then recorded-command replay | [agent-browser](https://github.com/vercel-labs/agent-browser) |
| Natural-language browser actions and extraction, self-healing, or a path to hosted Browserbase browsers | [Stagehand](https://www.stagehand.dev/), which also lists Go among its SDK clients. |

Scout's product hypothesis is that Go services and agent workers need a durable record of browser work when a mutation may have committed but its response was lost. A host can inspect the interrupted task, check its own authoritative application state, and record a resolution; Scout preserves the uncertainty and does not replay the task automatically. This is a workflow to evaluate, not a proven competitive advantage or validated buyer demand. Try the local end-to-end path with `go run ./examples/durable_task_recovery`; see the [opportunity brief](docs/opportunity-brief.md) and [competitive benchmark protocol](examples/benchmark/COMPARISON.md) for current evidence and its limits.

## Quick Start

### Go API: first verified action

Install Chrome or Chromium, then run the smallest verified Go workflow. It
starts its own local fixture, clicks one button, checks the resulting page
text, and prints a JSON receipt. It needs no account, API key, or model
provider:

```sh
go run ./examples/hello
```

If you want a non-invasive configuration check first, run
`go run ./cmd/scout doctor --json`; this does not start Chrome or contact a
remote browser, and its overall status remains `warning` until browser
readiness is checked. To verify that Scout can actually start or attach to the
browser and complete a bounded page read, run
`go run ./cmd/scout doctor --live`. Live mode can start a local browser or
connect to `SCOUT_REMOTE_CDP`; it exits nonzero when that check fails and
prints phase-specific remediation guidance. Neither doctor command contacts an
application URL unless you explicitly add `--probe-url` (and `--probe-text`,
if needed) to the live command.

The fixture binds to loopback, so the example explicitly enables
`AllowPrivateIPs` for that local URL; production sessions continue to block
private and loopback destinations by default. If browser startup fails, Scout
reports that Chrome or Chromium is required and suggests installing it or
attaching to a trusted browser through `SCOUT_REMOTE_CDP`. The published
container image is the Scout MCP server image; it is not a Go build environment
for running repository examples.

### Choose the next step

- **CI and deterministic replay:** `go run ./examples/ci` records and replays a
  playbook.
- **Durable task IDs:** `go run ./examples/task_run` verifies one task, retries
  the same ID and plan, and confirms the fixture was committed only once. It
  removes its temporary task database on exit. Pass
  `-task-store=/path/to/tasks.db` to retain records; use a fresh task ID for
  each new fixture. A separate CLI process read a saved result after two
  example processes exited (reopen report (local audit artifact)).
- **Process-loss recovery:** `go run ./examples/durable_task_recovery`
  reconciles against an authoritative fixture ledger.

See [`examples/hello/main.go`](examples/hello/main.go) for the minimal Go API
calls and [`docs/go-service-integration.md`](docs/go-service-integration.md)
for worker, recovery, and deployment patterns.

### CLI, MCP, and UI

Local sessions launch an installed Chrome-compatible browser. Scout does not bundle a browser in its Go binary. Chrome and Chromium are detected from standard install locations or `PATH`; `headless-shell` and `chrome-headless-shell` are also detected for container deployments. Set `SCOUT_CHROME_PATH` to select a process-wide executable, or use `browse.WithChromePath` / `agent.SessionConfig.ChromePath` for per-engine or per-session selection. An invalid configured path fails instead of silently selecting another browser. To use a browser managed separately, set `SCOUT_REMOTE_CDP` to its DevTools WebSocket URL or stable HTTP(S) DevTools endpoint. HTTP(S) endpoints are resolved through `/json/version` each time Scout launches or resets the session, so a restarted browser may publish a new WebSocket URL.

```bash
# CLI — one-shot commands; visible by default
scout observe https://example.com          # structured page snapshot
scout markdown https://news.ycombinator.com # page as compact markdown
scout navigate https://example.com --expected-text='Example Domain' --headless # verify a page after navigation
scout click https://example.com '#save' --expected-text='Saved' --headless # verified one-shot action
scout type https://example.com '#search' 'browser automation' --headless # type and verify committed value
scout press-key https://example.com 'Control+A' --expected-text='Selected' --headless # dispatch and verify a focused-key chord
scout key-down https://example.com Shift --headless # hold a key; pair with key-up in the workflow
scout key-up https://example.com Shift --headless # release a held key
scout submit https://example.com '#checkout' --match-url='/orders' --acceptance-text='Order placed' --headless # require response and accepted page state
scout screenshot https://github.com         # save screenshot
scout extract https://example.com h1        # extract element text
scout frameworks https://react.dev          # detect React, Vue, etc.
```

When `scout navigate` receives an `--expected-*` predicate and the page does
not satisfy it, the command exits nonzero after printing the observed result.
Add `--json-errors` to receive a `SCOUT_POSTCONDITION_FAILED` envelope on
stderr for shell automation. The JSON result includes `execution_timing` for
browser startup, page navigation and verification, and browser cleanup; a
cleanup failure also makes the command exit nonzero.

```bash
# Stateful CLI — one browser process, newline-delimited JSON requests
scout session --headless --task-store=./scout-task-runs.db
# Then send one JSON object per line, for example:
# {"command":"navigate","url":"https://example.com"}
# {"command":"observe"}
# {"command":"extract_all","selector":"article","offset":0,"limit":8}
# {"command":"batch","timeout_ms":10000,"actions":[{"action":"click","selector":"#save","expected_text":"Saved"}]}
# {"command":"quit"}
```

With `SCOUT_REMOTE_CDP` configured, the stateful JSONL session can persist and
resume an owner-bound handle to the same live browser target across Scout
process restarts. Send `{"command":"persist_session"}` to create a handle
(valid for 24 hours by default; optionally provide an RFC3339 `expires_at`),
then resume it in a later process with
`{"command":"resume_session","handle":"HANDLE"}`. Both commands require
`--task-store`; the handle is scoped to the local OS user and is not an
authorization token. The browser and exact target must remain alive. This
cannot restore a local browser, isolated context, or a browser/target that was
restarted. Revoke a handle with
`{"command":"revoke_session","handle":"HANDLE"}`.

```bash
# MCP Server — give AI agents browser superpowers
claude mcp add scout -- scout mcp serve --profile=core --task-store=/var/lib/scout/task-runs.db

# MCP server with a smaller task-specific tool catalog
claude mcp add scout-read -- scout mcp serve --profile=read

# MCP server with the everyday catalog, filtered to common browser workflows
claude mcp add scout-curated -- scout mcp serve --curated

# Browser UI — conversational browser automation
scout ui serve --provider=ollama --model=mistral
cd ui && npm ci && npm run dev  # frontend at http://localhost:3000; /api proxies to Go backend :4200
```

## Choose an interface

| Use case | Interface | Start here |
|---|---|---|
| One-off inspection, extraction, or capture from a shell | CLI | `scout observe URL`, `scout markdown URL`, `scout screenshot URL` |
| Navigate and confirm the resulting page state from a shell | CLI | `scout navigate URL --expected-text='Ready' --headless` |
| Run one click and check its outcome from a shell | CLI | `scout click URL '#save' --expected-text='Saved' --headless` |
| Type into one field and verify its committed value | CLI | `scout type URL '#search' 'browser automation' --headless` |
| Send and verify a focused key or shortcut | CLI | `scout press-key URL 'Enter' --expected-text='Submitted' --headless` |
| Submit a form and distinguish an HTTP response from application acceptance | CLI | `scout submit URL '#checkout' --match-url='/orders' --acceptance-text='Order placed' --headless` |
| An AI client needs the everyday browser tool set | MCP server profile | `scout mcp serve --profile=core` |
| An AI client needs the everyday tools with a smaller choice set | MCP server | `scout mcp serve --curated` |
| An AI client needs the full browser tool catalog | MCP server | `scout mcp serve --profile=all`; configure it in Claude Code, Cursor, or another MCP client |
| An AI client needs a narrower task-specific catalog | MCP server profile | `scout mcp serve --profile=read` (also `interact`, `diagnose`, `admin`); the compatibility default is `all` |
| A person wants to direct an agent and watch the browser | Chat UI | `scout ui serve` plus the Vue frontend |
| Embed browser automation in a Go program | Go library | `agent.NewSession(...)` or the lower-level `browse` engine |

The CLI runs one operation per command and closes its local browser afterward (or disconnects cleanly from a remote browser). MCP and the chat UI keep a browser session alive across actions. Use MCP for tool-driven agent workflows, and use the CLI when you want a small, scriptable command with a direct result.

MCP profiles reduce the tools returned by `tools/list` and reject calls to tools outside the selected profile. `core` provides the everyday navigation, inspection, interaction, extraction, wait, and tab workflows; use it for new MCP client setups. A 6 November catalog capture recorded 48 tools and 57,523 schema bytes for `core`, 50.4% fewer schema bytes than `all` in that capture. `read` includes navigation and page inspection; `interact` focuses on page actions; `diagnose` includes diagnostics, tracing, and recovery; `admin` includes configuration, session lifecycle, persistence, and recording controls. The compatibility default remains `all`; pass `--profile=all` explicitly when the full catalog is needed. Call the read-only `help` tool for task recipes filtered to the active profile; it omits unavailable workflows and explains outcome verification and safe recovery. Profiles are discovery and workflow controls, not authorization: use a trusted transport boundary and OS/network controls when a tool must be inaccessible. Catalog size changes with the registered tool set and schemas; inspect `tools/list` for the active build rather than relying on a fixed count.

Add `--curated` to any profile to hide advanced and infrequently used tools while keeping the common browser workflow, including task recovery. The curated catalog composes with `--profile`; `--require-tools` is checked against both filters and startup fails if a required tool is hidden. This is a model-choice aid, not an access-control boundary.

For a deployment that depends on specific actions, declare them with `--require-tools`. Scout checks the names against the selected profile before starting the MCP server and exits with the missing tool names if the profile cannot satisfy the requirement. For example, `scout mcp serve --profile=read --require-tools=navigate,extract` starts, while `scout mcp serve --profile=read --require-tools=click` fails immediately because `read` does not expose `click`. This is a startup compatibility check, not an authorization boundary or a guarantee that a task will succeed.

For a Go program that attaches to an already running Chrome instance, see [`examples/remote_cdp`](examples/remote_cdp). It reads `SCOUT_REMOTE_CDP` and `SCOUT_URL` from the environment. Both the CI and remote-CDP examples use a two-minute caller context that is canceled by SIGINT/SIGTERM, pass it through session startup and browser operations, and defer session cleanup. Closing the remote example disconnects Scout without stopping the externally owned Chrome process.

## Reuse a browser session

Scout's CLI and MCP server can attach to an existing Chrome DevTools Protocol (CDP) browser. Set the WebSocket endpoint before starting Scout:

```bash
# In one terminal, using the Chrome/Chromium executable installed on your system:
google-chrome --headless --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 \
  --user-data-dir=/tmp/scout-cdp-profile about:blank

# In another terminal, read Chrome's browser-level WebSocket endpoint:
export SCOUT_REMOTE_CDP="$(curl -fsS http://127.0.0.1:9222/json/version \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["webSocketDebuggerUrl"])')"
scout mcp serve --profile=core # or: scout observe https://example.com
```

Replace `google-chrome` with the executable name or full path for your Chrome/Chromium installation. The first page operation attaches to an existing page when one is available. Navigation reuses that tab. Scout disconnects from a remote browser when its session closes or resets; it leaves the remote browser process and attached tab open. Keep the debugging endpoint bound to loopback and use a trusted endpoint; it grants browser control to any process that can reach it.

MCP sessions are named and isolated. `create_session` starts a separate browser and activates it; `switch_session` returns to another live session; `clone_session` copies the active page URL, cookies, and localStorage into a new session; `close_session` releases one. Names allow 1-64 letters, numbers, hyphens, underscores, and periods. `list_sessions` shows the active session. Idle sessions expire after one hour by default; active tool calls finish before cleanup. Set `SCOUT_SESSION_IDLE_TIMEOUT` to a Go duration such as `30m`, or to `off` to keep sessions until explicitly closed or the MCP server exits.

The `save_profile` and `load_profile` tools persist the current page URL, browser cookies, and that page origin's localStorage to a JSON file with owner-only permissions. Profile files contain authentication secrets. Loading a profile opens its saved URL, restores cookies and localStorage, and reloads the page. Existing profile JSON files without a saved URL can still be applied to the current page. Profiles do not include sessionStorage, open tabs, browser extensions, or other origins' localStorage. Use profiles to restore a session after restarting Scout; cloning does not copy open tabs or sessionStorage.

The Go library has the same workflow in [`examples/profile`](examples/profile). Run `go run ./examples/profile` to save a profile, close its browser, start a fresh session, restore the profile, and verify the restored cookie and localStorage against a local fixture. By default, it uses a private temporary directory and removes it after the run. Pass `-profile /path/to/profile.json` to retain the profile at a chosen path. The example is self-contained; it does not use real account credentials.

Reset behavior depends on who owns Chrome. A local session closes its page and browser and launches a fresh browser. A remote CDP session detaches and reconnects to the same externally owned browser; its tabs and browser state can remain. Closing a remote session disconnects Scout and leaves the remote Chrome process running.

Screenshots returned as MCP tool output are JPEG, with an 80 KB hard limit and 1024 px max width by default. Scout progressively reduces quality and resolution; `image_info` reports the initial and returned byte sizes and dimensions, plus whether compression was applied and its quality/scale. `annotated_screenshot` and `hybrid_observe` return the same metadata when their image is included or saved. If the smallest inline capture still exceeds the limit, Scout returns an error. For a full-resolution capture intended for a person to view, set `save_to_disk: true` or pass `output_path`; Scout returns a file path and does not put image bytes in the model context. Annotated screenshots return element data only unless `include_image` or a disk output is requested.

## Install

Building Scout from source or using its Go library requires Go 1.26 or later.

```bash
# Homebrew
brew install --cask klarlabs-studio/tap/scout-mcp

# Direct binary
curl -fsSL https://raw.githubusercontent.com/klarlabs-studio/scout/main/install.sh | bash

# Go
go install go.klarlabs.de/scout/cmd/scout@latest

# As a library
go get go.klarlabs.de/scout
```

The Linux/macOS installer checks the downloaded release archive against the
SHA-256 checksum published with that release before extraction.

## MCP Server

Run `scout mcp serve` and any MCP-aware agent has a browser. No second project to install, no Node runtime, no Python interpreter — the binary is the server. First install Scout using one of the methods above and confirm `scout version` works. Then add the stdio server to your MCP client and restart or reload that client:

```bash
claude mcp add scout -- scout mcp serve --profile=core --task-store=/var/lib/scout/task-runs.db # Claude Code
```

For clients configured with JSON, use this entry in their MCP server configuration:

```json
{
  "mcpServers": {
    "scout": {
      "command": "scout",
      "args": ["mcp", "serve", "--profile=core", "--task-store=/var/lib/scout/task-runs.db"],
      "env": {
        "SCOUT_REMOTE_CDP": "ws://127.0.0.1:9222/devtools/browser/<browser-id>"
      }
    }
  }
}
```

If the client cannot find `scout`, set `command` to the absolute path printed by `which scout`. To attach Scout to an existing Chrome instance, add `SCOUT_REMOTE_CDP` to the server's `env` object. Set `SCOUT_MCP_ALLOWED_ORIGINS` to a comma-separated list of exact origins (for example, `https://shop.example,https://login.example`) to keep MCP navigation and page-target requests within those origins. If the variable is set, it must contain at least one valid origin; unset it to run without a global origin allowlist. This startup policy cannot be loosened with `configure`; it does not cover service-worker requests or WebSocket handshakes. For a trusted local development browser, `SCOUT_ALLOW_PRIVATE_IPS` can be set to `1`. The server uses stdio for MCP messages, so do not start it in a separate terminal or redirect its stdout. Once connected, call `navigate` with a URL, then `observe` to inspect the page. MCP sessions live until the server process exits; use `save_profile` and `load_profile` when browser state must survive a restart.

For clients that connect over HTTP, start the stateful Streamable HTTP server:

```bash
scout mcp serve-http                         # http://127.0.0.1:4201/mcp
SCOUT_MCP_HTTP_TOKEN='<secret>' scout mcp serve-http --listen=0.0.0.0:4201 --task-store=/var/lib/scout/task-runs.db
```

Page-derived fields in observations, text reads, extraction results, semantic
selection, and snapshot artifacts include
`content_trust: "untrusted_page_content"`. Treat page text, labels, URLs, and
values as data rather than instructions or authorization policy. Scout keeps
the evidence intact; this provenance marker does not sanitize prompt injection
or replace application-specific confirmation and authorization checks.

The HTTP endpoint serves initialize-based, stateful MCP by default and has an
opt-in stateless Streamable HTTP mode. It does not negotiate both modes on one
listener. See the [HTTP protocol compatibility note](docs/go-service-integration.md#mcp-http-protocol-compatibility).

The listener defaults to loopback. Non-loopback addresses require bearer authentication. Use `SCOUT_MCP_HTTP_TOKEN` for one shared credential, or use `SCOUT_MCP_HTTP_PRINCIPALS_JSON` to map distinct bearer tokens to stable principal IDs, remote browser endpoints, and required exact allowed origins. Principals sharing one Chrome endpoint must each set `isolate_browser_context` to `true`; this creates an ephemeral isolated context per MCP session. Principal policies default to blocking private/loopback destinations; set `allow_private_ips` to `true` only for a principal that needs them. For example:

For loopback or shared-token HTTP deployments, `SCOUT_MCP_ALLOWED_ORIGINS` can set one startup allowlist for every MCP session. If set, it must contain one or more exact origins; unset it to disable the global allowlist. Do not combine it with principal mapping; configure each principal's `allowed_origins` instead.

```sh
SCOUT_MCP_HTTP_PRINCIPALS_JSON='{"tenant-a":{"token":"secret-a","remote_cdp":"http://127.0.0.1:9222","allowed_origins":["https://app-a.example"]},"tenant-b":{"token":"secret-b","remote_cdp":"http://127.0.0.1:9222","allowed_origins":["https://app-b.example"],"isolate_browser_context":true}}' scout mcp serve-http --listen=0.0.0.0:4201
```

With principal mapping enabled, Scout derives identity from the authenticated bearer token, scopes session/configuration state by principal and MCP transport session in stateful mode, and routes each principal's default session to its configured endpoint. In stateless mode the configured principal is the stable owner across requests. Principal policy JSON rejects unknown fields and trailing values, so misspelled isolation or policy settings fail startup. Scout rejects principal mapping alongside `SCOUT_MCP_HTTP_TOKEN`; do not send principal IDs in caller-controlled headers. Duplicate normalized `remote_cdp` URLs are accepted only when both principals enable `isolate_browser_context`; aliases and proxy routes require the same setting for every principal that reaches the shared Chrome. BrowserContext support is required from the remote CDP provider. Named sessions continue to launch separate local browsers. A principal's `proxy_server` configures those locally launched named sessions only; its default session connected to `remote_cdp` uses the remote browser host's network policy. `status` and `list_sessions` report whether Scout applied a proxy to each session. A stateful HTTP client's `DELETE` closes only sessions in that transport session's principal scope after tracked tool calls finish; stateless sessions use explicit close or idle expiry. Stdio keeps its existing process-local session registry. Without principal mapping, `SCOUT_REMOTE_CDP` remains process-wide and all HTTP clients attach to that same external browser. The principal map is read at startup; rotate/revoke credentials or change endpoint assignments by updating the deployment secret and restarting Scout. Store the JSON in your deployment secret manager rather than shell history, logs, or a checked-in file.

Treat each configured principal as one browser-data trust boundary. Stateless
MCP sessions are shared across transports authenticated as that
principal so a replacement transport can reconnect. Durable task records use
the principal and logical session name as their namespace in both HTTP modes.
In stateful mode, a fresh transport can create a separate browser session with
the same name and still receive a completed task result from the previous
transport; `idempotency_replay: true` means the actions were not sent to the
fresh browser. Reuse the logical name to recover prior task outcomes, and use a
different name when the task ID should be independently executable. If independent agents
must not see or control one another's browser sessions, map them to distinct
principal IDs and bearer tokens; using different tokens that map to the same
principal does not create isolation. Opaque session IDs select a session inside
the authenticated principal's scope and are not credentials.

Chrome-backed stateless and stateful HTTP integrations verify that the same
task ID and plan can run independently in two sessions for one principal,
while a retry in the original session returns only its own cached result.
Stateful HTTP also verifies that a retry from a replacement transport using
the same principal and session name reuses the durable result without acting on
its newly created browser target. Clustered-server and abandoned-request
behavior should be validated separately for the deployment topology you use.

Set `SCOUT_MCP_HTTP_STATELESS=true` to opt into modern stateless Streamable
HTTP. This mode requires `SCOUT_MCP_HTTP_PRINCIPALS_JSON`; shared-token and
unauthenticated listeners are rejected because they do not provide a stable
owner. Clients can call `server/discover` without `initialize`; subsequent
calls must carry `MCP-Protocol-Version: 2026-07-28`, `Mcp-Method`, and `_meta`
fields for protocol version, client info, and capabilities.

`list_sessions` returns owner-visible opaque `session_id` handles. Every
browser/session-scoped tool call must include one at
`_meta.go.klarlabs.de/scout/session`; names and missing selectors fail closed.
Session lifecycle tools use their session-name/handle argument and remain
principal-scoped. There is no transport `DELETE` cleanup in this mode, so
explicitly close sessions or configure `SCOUT_SESSION_IDLE_TIMEOUT` for idle
cleanup. The session registry is still in memory and handles stop working
after a server restart.

Chrome-backed integrations verify owner isolation, task and artifact scoping,
quota cleanup, in-flight idle protection, and request cancellation within one
process. Independent TypeScript MCP client 2.4.0 and Python MCP SDK 2.3.0
smokes verify modern discovery, owner-level calls, and a browser-session
create/navigate/read/close flow using the opaque per-call handle; the older
TypeScript SDK 1.32.2 does not send the required routing header. A focused
integration verifies that old handles fail closed and fresh handles work after
a graceful single-process restart. Both SDK smokes also cancel an in-flight
wait and then read again through the same handle. Keep stateless mode opt-in
until multi-replica load balancing and additional target clients are covered.
Run the [client
compatibility harness](examples/benchmark/stateless_mcp_clients/README.md)
against the pinned SDKs.

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
| **Sessions** | `create_session`, `list_sessions`, `switch_session`, `clone_session`, `close_session`, `persist_session`, `resume_session`, `revoke_session`, `save_profile`, `load_profile` |
| **Frameworks** | `wait_spa`, `detect_frameworks`, `component_state`, `app_state` |
| **Playback** | `start_recording`, `expect_recorded_action`, `stop_recording`, `save_playbook`, `replay_playbook`, `resume_playbook` |
| **Video** | `start_screen_recording`, `stop_screen_recording` |
| **Smart Helpers** | `check_readiness`, `suggest_selectors`, `session_history` |
| **Vision** | `hybrid_observe`, `find_by_coordinates` |
| **Batch** | `execute_batch` |
| **Verified task run** | MCP `task_run`, `get_task_run`, `recover_task_run`, `resolve_task_run` (core/interact/diagnose/admin profiles); stateful CLI exposes the same lifecycle |
| **Iframe** | `switch_to_frame`, `switch_to_main_frame` |
| **Trace** | `start_trace`, `stop_trace` |
| **Cookies** | `cookies_list`, `cookies_clear`, `cookies_set`, `dismiss_cookies` |
| **Diagnostics** | `detect_dialog`, `detect_auth_wall`, `console_errors` (incl. network 4xx/5xx), `failed_requests`, `compare_tabs`, `upload_file` |

`fill_form_semantic` marks a field successful only when live browser readback matches the requested value or checked state. A mismatch sets `success: false`, reports `value_mismatch`, and includes the observed value and recovery guidance; low-confidence label matching is reported as a separate warning.

The MCP `admin` profile exposes durable `persist_session`, `resume_session`,
and `revoke_session` tools. They require `--task-store` plus
`SCOUT_MCP_HTTP_PRINCIPALS_JSON` authentication, and support the principal's
default remote CDP session, including an isolated BrowserContext. The returned 192-bit handle is owner-scoped and expires after
24 hours by default (configurable up to 30 days). A new authenticated
transport or Scout server process can resume it only while the same browser
and exact target remain live under the principal's current endpoint and
origin policy. Transport deletion and idle expiry clear the live session but
leave its durable recovery handle usable while the browser target remains
available. Explicit `close_session` revokes every recovery handle for that
principal's default session, including handles created before a reconnect.
In-memory `session_id` selectors remain process-scoped; they are not
the durable recovery handle.

After `switch_to_frame`, core inspection and selector actions operate in that frame until another frame, `switch_to_main_frame`, or an active-tab change is selected. Call `switch_to_frame` again with a selector in the current frame to enter a nested iframe. Opening or switching tabs resets execution to that tab's main frame so a frame context cannot leak across browser targets. If a selected frame navigates to a new document, Scout recreates its isolated execution context before the next operation. `observe` and page action results include `frame_id` when a child frame is selected, alongside that frame's URL and title. Specialized diagnostics and browser-wide operations may still describe the top-level page.

Site-isolated out-of-process iframes (OOPIFs) are selectable when Chrome exposes one unambiguous iframe target bound to the selected parent frame and URL. Scout attaches a dedicated CDP session, routes inspection and selector actions through it, preserves the top-level target ID in results, applies the configured URL policy, and detaches the frame session when returning to the main frame or switching tabs. Nested site-isolated frames are supported through a parent-to-child route stack; selecting the main frame unwinds the stack. Duplicate iframe URLs and ambiguous target bindings fail closed. Same-process and cross-origin frames present in the page frame tree remain supported.


Large structured results are bounded too: `discover_form` and visual element lists return at most 100 entries with total and truncation metadata; `network_requests` returns pages of up to 3 requests from a session-retained window of 200, with a session-wide `dropped_total` when the in-flight or history cap omits data. Each captured request includes its CDP `target_id`, the time Scout observed the request, and its finish time when available. Since new requests can evict older ones, offsets apply to the current retained window. `network_summary` includes at most 50 failure details and reports `capture_mode`, `coverage_scope`, `coverage`, whether capture was ever enabled or interrupted, active URL filters, and the history limit. Its scope is only the session network-capture window: `unavailable`, `not_started`, `diagnostics_only`, `not_enabled`, `known_gaps`, and `disabled_at_read` distinguish missing or partial capture from a clean empty result; `active_no_known_loss` means Scout observed no known loss, not that the browser's entire network activity was captured. `console_errors` and `failed_requests` also report capture mode and coverage, including when observers are unavailable. `failed_requests` returns at most 20 with totals. Console diagnostics retain the latest 100 messages in the browser, return up to 20 per call, and cap each message at 2,000 characters. Tab and cookie listings return at most 50 and 100 entries respectively. MCP `extract` returns at most 4,000 characters with original length and truncation metadata. Its result includes `target_id` and `frame_id`, keeping text bound to the selected browser target and execution frame; an empty frame ID denotes the main frame. Table extraction returns at most 25 rows, 10 columns, and 256 characters per cell. Screenshot, annotated screenshot, and hybrid image results are capped at 80 KB; annotated and hybrid images default to 1024 px. PDFs over 80 KB require `save_to_disk` or `output_path`. Use disk output for full-resolution images and documents.

`observe` and `observe_diff` include total counts and a `truncated` flag; observation text, links, inputs, buttons, metadata, and long labels/URLs have fixed per-field and item limits. Component and application state responses are bounded to 64 KB and mark clipped values with `_scout_truncated`. Large MCP batch, task, and playbook results compact verbose evidence when the serialized response exceeds 192 KB. They preserve task status, resume position, per-step outcomes, unknown-outcome recovery records, and compact action receipts with run/operation IDs, policy and target identity, verification, and timing; page-derived prose and large policy snapshots are omitted. Scout saves the full pre-compaction result as a private JSON artifact when it is at most 16 MiB and artifact storage is available, and includes the retrieval path in the response. If saving fails or exceeds the artifact limit, the response says that only the compact result is available. Every MCP `tools/call` response also has a 256 KB serialized-size ceiling. When a result or JSON-RPC error still exceeds it, Scout returns a compact `SCOUT_OUTPUT_LIMIT` notice and saves the payload as a private JSON artifact when possible. Use `read_page_snapshot` or `search_page_snapshot` with the returned artifact path; `delete_page_snapshot` removes it. Artifact access is bound to the requesting MCP principal/session. The combined snapshot and response-artifact registry is capped at 4 artifacts/32 MiB per owner and 32 artifacts/64 MiB per server process. Delete an artifact when finished to release quota. This is a final serialized-response guard. Tool-specific bounds and paging are applied earlier for supported large outputs; the guard does not cap peak memory used while a tool or the MCP framework builds its result.

Network diagnostics distinguish `capture_mode`: `not_started` means no network observer has run, `diagnostics_only` means Scout retained failure-only network evidence, `full_capture` means explicit request capture is active, `unavailable` means observation was requested but its CDP observer is not attached, and `disabled` means explicit capture has stopped. Coverage includes `unavailable` when observers could not be installed, `diagnostics_only` when only failures are retained, and `not_enabled`, `known_gaps`, `disabled_at_read`, or `active_no_known_loss` for explicit capture. An active state with no known event loss does not claim complete visibility into browser-wide traffic.

Stateful `scout session` JSONL also caps each response at 256 KiB. Oversized serialized responses are saved to a private temporary artifact when within the 16 MiB per-artifact and 64 MiB/32-artifact session limits; the response includes its opaque ID, size, and SHA-256. While the same session process is alive, use `{"command":"read_output_artifact","artifact_id":"ID","offset":0,"limit_bytes":131072}` and advance the offset until complete. Each response returns base64 data and the full artifact digest. Artifacts are mode `0600` inside a mode `0700` directory and are deleted when the session process exits. If the fallback envelope itself cannot retain an unusually large request ID or recovery record, it marks that field omitted; the original envelope remains available through the artifact. This wire-size cap does not bound peak memory while Scout builds the result.

For a bounded action result that preserves recovery data, pass `timeout_ms` to MCP `click`, `navigate`, or `type`, or `action_timeout_ms` to `submit_form`, and keep the MCP request itself alive long enough to receive the response. The form action deadline is separate from `timeout_ms`, which caps response/navigation observation. If a mutation may have dispatched before its deadline, Scout returns `outcome_unknown` with a versioned `recovery_record` and the expected page or field condition when supplied; form submissions also retain observed HTTP method, sanitized response URL, and status when available. Pass the record to `reconcile_action` to inspect the original target without replaying the action. `batch` stops by default after an uncertain step; Go callers receive a `BatchStoppedError`, MCP returns `SCOUT_BATCH_STOPPED` with partial results, and stateful CLI returns the same error code with `partial_result`. The result identifies `stopped_on_unknown_at` and the first unattempted `next_action_index`. Set `require_verified=true` (MCP or stateful CLI) or `BatchOptions.RequireVerified` (Go) to stop at the first step without positive verification evidence; this also stops on rejected or unknown outcomes and takes precedence over `continue_on_unknown`. The result identifies `stopped_on_contract_at`; an unknown outcome still requires reconciliation before retry or continuation. Without strict mode, reconcile an uncertain step before deciding to retry it or continue from its index. Set `continue_on_unknown=true` only when later actions remain safe even if the uncertain action took effect. Canceling the outer MCP request can stop the caller from receiving the action record. Recovery records can include selectors, page evidence, and expected text or values; protect them like authentication or browser state. CLI recovery files are created with mode `0600` and are not overwritten. Current portable recovery records use schema version 3 and bind reconciliation to a non-reversible fingerprint of the live DevTools endpoint and BrowserContext; the original browser/context must remain running across the Scout process restart.

For a task-level result, MCP `task_run`, stateful JSONL `task_run`, and Go `Session.RunTaskContext` bind a stable caller ID and target to a deadline-bounded sequence. Every action must have positive verification evidence; the result reports `completed`, `rejected`, `needs_reconciliation`, or `incomplete`, along with `resume_from` and the underlying batch receipts. A failed final postcondition returns `needs_reconciliation` with `failure_reason: "verification_failed"`; the task ID remains reserved until host reconciliation. An optional `final_postcondition` checks the completed task's final page state. Within the task store's namespace, retrying the same ID and identical plan returns its stored result without replaying actions; the returned `idempotency_replay: true` field identifies that cache hit. A different plan or concurrent run with that ID returns `SCOUT_TASK_ID_CONFLICT`. The default session-local registry retains at most 256 task IDs and fails closed with `SCOUT_TASK_CAPACITY` rather than evicting retry history. Set `--task-store=/path/to/task-runs.db` on `scout session`, `scout mcp serve`, or `scout mcp serve-http` to use the durable bbolt store, which retains up to 8,192 IDs total across all owner/session namespaces in that database, with no automatic pruning. The database is created with owner-only file permissions; its parent directory must exist, and the database contains task results and browser evidence. MCP scopes records to the authenticated principal and named session when principal mapping is configured; otherwise it scopes HTTP records to the transport session, which may change after reconnect. For Go services, configure `SessionConfig.TaskRunStore`; clustered services need a shared transactional store. Before MCP `task_run` starts browser work, Scout verifies that the active profile exposes `recover_task_run` and `resolve_task_run`; `required_tools` can add further profile requirements. After reconnect or process restart, use the read-only MCP `recover_task_run` or stateful CLI `recover_task_run` with only `task_id` to inspect a retained record when the durable store and its namespace are available. The response includes bounded `guidance` with a stable recommendation, rationale, prerequisites, and `replay_authorized: false`: an initial cursor with no action-start checkpoint requires both worker-stop confirmation and authoritative application reconciliation, since custom stores without per-action progress may retain that cursor after dispatch. A started cursor means that step may have been dispatched. Completed saved results direct the host to inspect their verification evidence; missing records direct it to verify store and namespace scope. Guidance never claims business success from browser state. An interrupted record includes a redacted, non-executable `plan` (action names, selectors, postcondition kinds, target, and policy metadata) and a `recovery` cursor. The built-in bbolt store retains per-action cursor state: `current.status: "started"` means that step may have been dispatched; `finished` means Scout recorded its result; `next_action` is the first step not yet recorded finished. The portable plan omits action inputs and expected values/text, so it helps a different host understand the interrupted work but cannot replay it. Built-in bbolt stores the browser binding, plan, and initial cursor atomically before dispatch. This narrows inspection after a crash but does not establish application state; reconcile authoritative state before deciding what to do. Custom Go stores can opt into per-step persistence with `agent.TaskRunProgressStore`, redacted plan persistence with `agent.TaskRunPlanStore`, and atomic pre-dispatch handoff with `agent.TaskRunHandoffStore`; stores used with final postconditions should implement `agent.TaskRunUncertainResultStore` to retain a failed final readback without releasing the replay guard. Legacy `TaskRunStore` implementations remain compatible for tasks that do not need this extension but do not retain these details. For principal-mapped HTTP, reconnect with the same authenticated principal and named session; ordinary HTTP transport-session IDs may change on reconnect, and stdio namespaces are process-local. After authoritative reconciliation, Go `Session.ResolveInterruptedTaskRunWithEvidenceContext`, MCP `resolve_task_run` (diagnose/admin profiles), or stateful CLI `resolve_task_run` with the record fingerprint can record the host's conclusion. Terminal `verified_complete` and `rejected` resolutions require `evidence_source`, an opaque non-secret `evidence_reference`, and RFC3339 `observed_at` in addition to non-secret `evidence`; unresolved observations may omit provenance while the host gathers evidence. The CLI command records the attestation and never executes browser actions. `ResolveInterruptedTaskRunContext` remains suitable for unresolved observations. Use `rejected` when the application confirms rejection. This is an auditable host attestation, not Scout browser proof; the task ID remains permanently unavailable for execution, so use a new ID for follow-up work. Evidence is limited to 4,096 bytes and must not contain secrets. Go stores can implement the optional `agent.TaskRunResolutionStore`; the built-in bbolt and MCP scoped stores support it. See the [Go service integration guide](docs/go-service-integration.md) for interrupted-run reconciliation and the runnable [durable task recovery example](examples/durable_task_recovery/main.go), which demonstrates completed-result reuse in a second Scout process while Chrome and the browser target remain alive. Task results and `status` include `policy_id`, a versioned digest of the normalized origin and private-IP policy, plus `allowed_origins`, `allow_private_ips` and `policy_enforcement` so host logs can correlate the configured scope. `task_run.allowed_origins` must match the running session's policy. To scope access more narrowly, create a named MCP session with `allowed_origins`; it is checked against the server or principal allowlist and becomes immutable for that session. Invalid or broader requests return `SCOUT_ORIGIN_POLICY_INVALID` or `SCOUT_ORIGIN_POLICY_REJECTED` with a recovery hint and do not create a session. Page-level checks do not cover service workers or WebSockets; use an enforcing proxy or host network policy for browser-wide egress. Example JSONL request: `{"command":"task_run","task_id":"checkout-42","target_id":"TARGET_FROM_OBSERVE","timeout_ms":15000,"actions":[{"action":"click","selector":"#submit","expected_text":"Order accepted"}]}`. In MCP, pass the task ID as `id` and the sequence as `actions`; use `get_task_run` and `resolve_task_run` for crash recovery when the active profile exposes them.
The durable `recovery.phase` identifies whether the run was executing actions, checking its optional final postcondition, or persisting the result. The manifest includes selector targets for selector-based action and final postconditions, but records only condition types for text and URL assertions. It never stores expected values or text. A crash in the final verification or persistence phase still requires authoritative application-state reconciliation. `webmcp_invoke` is also available as a verified `task_run` or strict `batch` action: supply the current catalog tool name and frame ID, page-schema-validated input, and one host-declared postcondition. Scout rechecks the target, frame catalog, input schema, and origin policy before dispatch, and treats the page tool result as untrusted; task idempotency and recovery receipts therefore cover page-native calls as well as DOM actions.

MCP `extract_all`, `auto_extract`, and `scroll_and_collect` accept `offset` and `limit`/`max_items` for pagination; stateful CLI JSONL `extract_all` accepts `selector`, `offset`, `limit`, and optional `include_handles`. For `extract_all`, continue only when `has_more` is true and pass the returned `next_offset` with the same `limit`; `items_truncated` and per-item `item_truncated` describe clipped text. The legacy `truncated` flag is true when either more matches remain or item text was clipped. `extract_all.references` and `scroll_and_collect.references` align by index with `items` and include a CSS selector, source tag, inferred or explicit ARIA role, and an `interactive` hint for each element. `scroll_and_collect` also returns the source `target_id` and `frame_id`. The hint reflects common semantic controls or interaction attributes; it does not prove that an element is visible or currently actionable. Set `include_handles=true` when the result will drive a later `click_handle`; Scout will stamp stable node handles into the page DOM, so this option is mutating. Go callers can use `ExtractAllPageWithOptions` or `ExtractAllPageWithOptionsContext` with `ExtractAllOptions{IncludeHandles: true}` for the same behavior. `auto_extract.references` aligns by index with `items` and includes CSS selectors, stable item and field node handles, and tag, role, and interaction hints for each source, with the matching `target_id` and `frame_id`. Pass a field handle such as `field_handles.link` and both identity fields to `click_handle` to bind the action to the extracted target and frame. Handles survive reordering while their DOM node remains attached; they become stale after node replacement or removal. If DOM cloning duplicates a handle, `click_handle` rejects it as ambiguous instead of choosing one match. CSS selectors describe the DOM at extraction time and should be rechecked after page changes. MCP `extract_all` returns at most 8 items per call and clips each item to 4,000 characters, reporting original lengths and per-item truncation; use `has_more` and `next_offset` to continue and `items_truncated` to distinguish clipped text. The Go `ExtractAll` method retains full item text; Go callers can use `ExtractAllPage` for bounded pages. Batch execution accepts at most 100 actions and 96 KiB of serialized action input. When a batch response exceeds its compact-response budget, Scout omits verbose per-step evidence and field partial results, marks the result `truncated`, and preserves step identity, outcome counts, and recovery records. Disk-backed captures, traces, and encoded recordings return an `artifact` object with `path`, `kind`, `mime_type`, `size_bytes`, and `sha256` when the artifact is a single file; frame-directory recordings return path and type metadata. Inline screenshot/PDF payloads use `data_url` in the same capture result shape.

Trace ZIP artifacts contain `trace.json` action records, `url-policy.json` per-request origin policy decisions, `network.json` captures, `network-manifest.json` network coverage metadata, `evidence-manifest.json` trace-event and screenshot coverage, referenced screenshots, and `run.json` execution metadata. Each event records the status of its before/after screenshot capture. The evidence manifest reports expected captures, assets written to the ZIP, status/counter mismatches, and `unattached_capture_attempts` for before-captures abandoned before an event was emitted; coverage distinguishes no trace events, no screenshot capture, complete before/after screenshot capture, and known screenshot gaps. The network manifest reports whether capture was ever enabled and is enabled at export, whether a URL filter was active, retained/pending/dropped request counts, the history limit, and capture interruptions. Clearing captured requests starts a new evidence window and discards in-flight records from the prior window. Its `no_known_loss_at_export` state describes Scout's session capture window; it does not prove browser-wide completeness. Each action record includes its browser ownership mode (`scout_launched` or `remote_cdp`) alongside its target and operation ordinals, so it can be correlated with action receipts without disclosing a browser endpoint. Portable action values are omitted, and URLs embedded in evidence, errors, and recovery hints have credentials, query strings, and fragments removed; trace evidence can still contain other page-derived text, so handle the archive as sensitive data. The request log records intercepted HTTP(S) URL policy verdicts and whether CDP successfully resolved each paused request; a verdict does not imply the browser applied it successfully. It is capped at 10,000 decisions per page and includes a dropped count. Exported decision URLs and URLs in reason or resolution error text have credentials, query strings, and fragments removed. The [`url-policy.json` schema](docs/schemas/scout-url-policy-trace-v1.schema.json) documents the archive format. The run manifest records configured session policy and timeout, plus the 256 KiB serialized response ceiling for traces started through MCP; each action records its caller deadline and remaining budget when the caller supplied one. It also includes `egress_coverage`: count of page targets seen by traced actions, how many had the page-target URL policy interceptor active, the number of recorded policy decisions, known unobserved service-worker and WebSocket paths, and proxy enforcement status (`unverified`). Pages with no traced actions are not included. This remains unverified even when Scout configured a local proxy because the trace cannot attest to external proxy decisions. Proxy metadata indicates that a proxy was configured and does not establish that it enforced the recorded origin policy. Service worker requests and WebSocket handshakes remain outside page Fetch interception.

For a full page that is too large to keep in the agent context, use `save_page_snapshot` (available in the `all`, `diagnose`, and `admin` profiles). The `read_page_snapshot`, `search_page_snapshot`, and `delete_page_snapshot` retrieval tools are available in every profile, including `core`, so oversized MCP responses can always be recovered when the server saves an artifact. Save the bounded snapshot, then search or read selected ranges through MCP instead of requesting the entire page inline:

```json
{"method":"tools/call","params":{"name":"save_page_snapshot","arguments":{"format":"readable_text","max_chars":200000}}}
{"method":"tools/call","params":{"name":"search_page_snapshot","arguments":{"path":"<artifact.path>","query":"invoice total"}}}
{"method":"tools/call","params":{"name":"read_page_snapshot","arguments":{"path":"<artifact.path>","offset":0,"limit":8000}}}
{"method":"tools/call","params":{"name":"delete_page_snapshot","arguments":{"path":"<artifact.path>"}}}
```

Use the `path` returned inside `artifact` for the later calls. Artifact reads, searches, and deletes are authorized against the selected session, or the active session when no selector is supplied; if you switch sessions, select the session that owns the artifact. Stateless HTTP calls always require the opaque session handle in `_meta.go.klarlabs.de/scout/session`. Saved snapshots and overflow artifacts remain registered only for the lifetime of that MCP server process; retrieve any needed ranges before deleting them or restarting the server. The overflow notice includes its artifact metadata under `artifact`. Other paged tools return `total`, `offset`, `limit`, and `truncated` metadata so callers can request the next slice.
| **Utility** | `has_element`, `wait_for`, `configure`, `set_viewport`, `web_vitals`, `select_by_prompt` |

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

The one-shot CLI, `watch`, `pipe`, `record`, `reconcile`, and `session` commands
block loopback and private IP destinations by default. Add `--allow-private-ips`
only when those commands need to reach a trusted local-development target.

MCP sessions expire after one idle hour by default; override this with `SCOUT_SESSION_IDLE_TIMEOUT` set to a Go duration such as `30m`, or set it to `off` to retain sessions until explicit close or server shutdown. A configured duration must be positive; malformed, zero, or negative values fail startup. `list_sessions` reports `idle_timeout_ms`, `last_activity`, and `idle_deadline`. Set `SCOUT_MAX_SESSIONS` to a positive integer to cap sessions per MCP HTTP client (or across the stdio server); it is disabled by default. A configured value must be a positive integer or startup fails. `list_sessions` reports the live count, in-progress startups, pending or failed cleanup by session, and configured cap. A full cap returns `SCOUT_SESSION_LIMIT` with guidance to close a session or wait for idle expiration. The cap includes concurrent startups and browser cleanup in progress. If cleanup fails, Scout keeps that slot reserved because the browser's final state is uncertain. Cleanup is not retryable in-process; restart the Scout server to release the reservation, and inspect `cleanup_error` to diagnose the failure. An idle Scout-launched browser is closed and its in-memory page state is lost; a remote CDP session is disconnected without closing the browser. Save cookies and localStorage with `save_profile` first if they need to be restored later. In-flight tool calls are allowed to finish before the idle session is closed.

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
scout ui serve --provider=ollama --model=mistral    # local, no API key; backend at 127.0.0.1:4200
scout ui serve --provider=claude                     # needs ANTHROPIC_API_KEY
scout ui serve --provider=openai --model=gpt-4o     # needs OPENAI_API_KEY
scout ui serve --provider=groq --base-url=https://api.groq.com/openai --model=llama-3.3-70b-versatile
# For a frontend on another origin, allow its exact HTTP(S) origin:
scout ui serve --provider=ollama --allowed-origins=http://localhost:3000

# Start the Vue frontend
cd ui && npm ci && npm run dev                       # frontend at :3000, proxies /api to backend :4200
```

The AG-UI backend binds to `127.0.0.1` because it has no authentication or
per-user session identity. The origin allowlist controls browser CORS only; it
does not grant safe remote access. Keep remote deployments behind an
authenticated proxy that provides an appropriate user boundary.

The UI streams AG-UI protocol events over SSE:
- **Chat panel** with markdown rendering and quick-action pills
- **Live browser viewport** with screenshot streaming and URL bar
- **Activity timeline** showing tool calls in real-time
- **Stop button** to cancel mid-stream

The Go server handles the agentic loop: LLM decides which scout tools to call, executes them, streams browser state deltas back to the frontend. Supports any OpenAI-compatible endpoint via `--base-url`.
Cross-origin browser requests are denied unless their exact origin is listed with `--allowed-origins` or `SCOUT_UI_ALLOWED_ORIGINS` (comma-separated). Same-origin browser requests and server-to-server requests without an `Origin` header remain supported. The readiness endpoint does not grant CORS access. This is an origin check, not user authentication; deploy the UI behind an authenticated boundary when users are not equally trusted.

## Agent Package (Go)

High-level Go API for callers that want to embed scout in a program. Structured output, auto-wait, goroutine-safe. Most users reach scout through the CLI or MCP server above — this section is for the Go-library path.

Examples are runnable from a fresh checkout: `go run ./examples/scrape -url https://news.ycombinator.com`, `go run ./examples/login -url https://example.com/login -user USER -pass PASS`, `go run ./examples/human_handoff -url https://example.com/login -expect 'Account overview'` (headed browser; requires a graphical session), `go run ./examples/ci`, `go run ./examples/ci -playbook ./ci-playbook.json`, `go run ./examples/worker_pool -workers 2 -jobs 4`, `go run ./examples/profile`, `go run ./examples/benchmark`, and `go run ./examples/remote_cdp` with its environment configured. The CI example verifies the live click's expected text, records the action with an explicit replay assertion, saves and reloads the playbook JSON, replays it without a model call, emits both action evidence and the replay digest/result with ordered per-step receipts, and exits nonzero on failure. By default the playbook is temporary; `-playbook` keeps it at the supplied path. The worker-pool example gives each bounded worker its own browser session, restricts page requests to the fixture origin, processes deterministic local jobs, rejects any click without a verified postcondition receipt, and cancels/cleans up on SIGINT, SIGTERM, or its three-minute deadline. Caller-supplied private fixtures require the explicit `-allow-private-ips` opt-in. See the [Go service integration guide](docs/go-service-integration.md) for request-scoped contexts, browser ownership, egress policy, OpenTelemetry, and runtime deployment. The profile example roundtrips cookies and localStorage between fresh sessions. The task-run example demonstrates a single verified task with an exact fixture-origin policy and durable task store. The durable recovery example demonstrates process-loss reconciliation against an authoritative fixture ledger. The benchmark example runs versioned local fixtures for scope containment, false-success controls, verified clicks and typed values, form acceptance/rejection, deterministic replay, changed-expectation evidence, resume after page repair, and Go/MCP tab lifecycle parity. The baseline compares Go, CLI, and real MCP stdio results for clicks, committed input values, and accepted or rejected form submissions. A focused frame-scope case now also exercises the stateful CLI's tab/frame identity behavior. Its rejected-form case checks an HTTP 422 response against misleading acceptance text. It emits machine-readable JSON baseline metrics. The integration workflow runs the CI, profile, and benchmark examples with Chrome installed.

Mutating action results include the browser `target_id`, action and target, outcome, direct evidence, and a recovery hint when the page effect is uncertain. `target_id` identifies the CDP page target that produced the result; `frame_id` identifies the selected frame when the operation ran inside one. Use these fields to correlate evidence with a tab, especially after switching tabs. `verified` is reserved for a checked postcondition; event receipt is reported as `event_observed` and does not imply that the application completed the intended effect. Click callers can provide `expected_text` to verify a resulting page condition.

### OpenTelemetry

The Go agent package accepts an application-owned OpenTelemetry tracer provider. Scout does not install an SDK or exporter; configure those in the host application and pass its provider into the session:

```go
session, err := agent.NewSession(agent.SessionConfig{
    Headless:       true,
    TracerProvider: otel.GetTracerProvider(),
})
```

Receipt-covered operations emit `scout.browser.action` spans. Context-aware methods parent those spans to the supplied context. The span includes action outcome, verification, timing, deadline, and browser target identifiers; it omits URLs, selectors, form values, evidence, and captured page content. When the provider supplies a valid span context, its trace and span IDs are returned as `otel_trace_id` and `otel_span_id` in the action receipt, separate from Scout's own trace archive ID. Reconciling an outcome with that receipt links the reconciliation span to the original action span, including when the caller uses another trace. See [OpenTelemetry's Go library guidance](https://opentelemetry.io/docs/languages/go/libraries/) for configuring the SDK in an application.

Session reset and close also emit `scout.browser.session.reset` and `scout.browser.session.close` lifecycle spans when a tracer provider is configured. These spans record operation, browser ownership, remote context isolation, outcome, and duration. Reset's action receipt is a child span; close records bounded cleanup and its final error state. Lifecycle spans omit page content and endpoint details.

For a service-oriented setup guide covering worker pools, cancellation, browser ownership, egress policy, telemetry, and runtime deployment, see [Embedding Scout in a Go service](docs/go-service-integration.md).

```go
session, _ := agent.NewSession(agent.SessionConfig{Headless: true})
defer session.Close()

// Navigate and observe
session.Navigate("https://example.com")
obs, _ := session.Observe()               // links, inputs, buttons, text + action costs

// DOM diff — inspect what changed after an action
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
// Later: session.ReplayPlaybook(pb) // deterministic; inspect failure evidence
// After repairing the current page: session.ResumePlaybook(pb, failedResult)

// Persistent profiles
session.SaveProfile("session.json")   // cookies + localStorage
session.LoadProfile("session.json")

// Content distillation (5 levels)
session.Markdown()          // ~2-8KB compact markdown
session.ReadableText()      // ~1-4KB main content only
session.AccessibilityTree() // ~1-4KB semantic tree
session.ObserveWithBudget(500) // fit in ~500 tokens
```

When replay fails, `PlaybookResult.FailedAt` is the one-based action index (zero means setup or starting-URL navigation failed), and `StepsRun` counts actions that completed. Inspect `Evidence` and `RecoveryHint` before continuing. A result with `ResumeSafe: true` includes a one-use, session-bound `ResumeToken`; pass the unmodified result and same playbook to `ResumePlaybook`. Resume verifies the playbook digest, result integrity, and progress, then continues after completed actions. If an action may have been dispatched but could not be verified, `ResumeSafe` is false and automatic resume is rejected to prevent an accidental duplicate. The MCP `resume_playbook` tool requires the unchanged result, including its `resume_token`, and the unchanged playbook path in the same Scout session. Replay again after editing the playbook or starting a new session.

`SavePlaybook` writes JSON through a temporary file in the destination directory, then atomically replaces the destination. The resulting file has mode `0600`, including when replacing an existing file, because recordings can contain entered values.

Recordings capture actions, but do not guess which page changes are important. After inspecting a result during recording, attach an explicit replay expectation to the latest action with `Session.ExpectRecordedAction(agent.ActionExpect{Text: "Saved"})`, or use MCP `expect_recorded_action` with an expected URL, title, selector, or text. Replay checks every populated expectation and reports the failed step with page evidence.

For unattended flows, optionally restrict top-level pages and redirects to exact origins:

```go
session, err := agent.NewSession(agent.SessionConfig{
	Headless:       true,
	AllowedOrigins: []string{"https://example.com", "https://login.example.com"},
})
```

The scheme and port are part of each origin. Paths are ignored; subdomains and alternate ports require their own entries. Redirects to an origin outside the allowlist are blocked. Scout also applies the policy through its page-target CDP Fetch interceptor to HTTP(S) requests, including subresources, `fetch`/XHR, and downloads. A controlled service-worker test and a WebSocket handshake test demonstrate that service-worker-owned fetches and WebSockets bypass this page-level check. `AllowedOrigins` is not a complete network sandbox.

For browser-wide enforcement, route locally launched Chrome through a proxy that enforces the desired destination policy:

```go
session, err := agent.NewSession(agent.SessionConfig{
    Headless: true,
    ProxyServer: "http://127.0.0.1:8080",
    AllowedOrigins: []string{"https://example.com"},
})
```

`ProxyServer` only routes traffic; the proxy must enforce its own allowlist for HTTP requests and CONNECT destinations. Scout does not translate `AllowedOrigins` into proxy rules. With `RemoteCDP`, the remote browser host must configure and enforce its own proxy policy. Scout's page-layer private-IP and DNS checks do not prevent DNS rebinding; the reference proxy resolves and pins public addresses for connections that traverse it. Use network-level destination controls for channels or processes outside that proxy.

For local launches with `ProxyServer`, Scout also isolates destination DNS
resolution and disables non-proxied WebRTC UDP. `WithChromeArgs` cannot override
these proxy, resolver, or WebRTC egress controls; conflicting flags fail the
launch so the reported proxy configuration cannot silently disagree with
Chrome's routing.

CLI, stateful CLI, MCP, and chat UI users can route locally launched browsers
through a proxy with `SCOUT_PROXY_SERVER`, for example
`SCOUT_PROXY_SERVER=http://127.0.0.1:8080 scout mcp serve --profile=core`.
Scout passes browser traffic through that proxy but does not derive or enforce
an allowlist there; configure the proxy policy separately. `scout doctor`
reports a configured proxy as unverified. This variable cannot configure a
browser attached through `SCOUT_REMOTE_CDP`; configure that browser host's
network policy instead. Proxy URLs with embedded credentials are rejected to
avoid placing secrets in the browser process arguments.

The [reference mitmproxy addon](examples/egress-proxy/allowlist.py) enforces exact HTTP origins for cleartext requests and exact HTTPS host/port authorities for CONNECT tunnels (which also covers WSS handshakes). Before opening each upstream connection, it resolves the destination, rejects any non-public answer by default, and pins a selected public address while retaining the hostname for TLS SNI. This guards against DNS rebinding for connections that reach this proxy. It does not decrypt TLS or constrain traffic that bypasses the proxy; use network-level controls for other browser channels and processes. Start it with:

```sh
python3 -m pip install 'mitmproxy==12.3.1'
SCOUT_ALLOWED_ORIGINS='https://example.com,https://login.example.com' \
  mitmdump --mode regular --listen-host 127.0.0.1 --listen-port 8080 \
  --set 'ignore_hosts=.*' \
  -s examples/egress-proxy/allowlist.py
```

Then set `ProxyServer: "http://127.0.0.1:8080"` in `SessionConfig`. Keep `AllowedOrigins` as well: it gives Scout an early page-level check and clearer action errors, while the proxy independently enforces its configured network policy. The proxy allowlist is deliberately configured separately; Scout does not silently claim that its page policy is enforced by a proxy.

With Chrome and `mitmdump` installed, run the integration proof from the repository root:

```sh
go test -tags=integration . -run '^TestIntegrationEgressProxyBlocksServiceWorkerAndWebSocket$' -count=1
```

The test forces Chrome loopback traffic through the proxy and verifies that the allowlisted page and service worker load, while a disallowed service-worker fetch and WSS tunnel are rejected before their destination server sees them.

By default, Scout also blocks private and loopback destinations at its page-level checks. It fails closed when its DNS lookup cannot verify a hostname, because Chrome uses a separate resolver. This is an early guard; the reference proxy's destination resolution and pinning protect only upstream connections that traverse that proxy.

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

`Retry` does not automatically repeat a browser operation when Scout reports
that its outcome is unknown: the command may have reached the page even though
its acknowledgment was lost. Inspect authoritative application state before
replaying that operation. `RetryConfig.IsRetryable` can override this behavior
for workflows whose application contract makes replay safe.

## CLI

The CLI supports both **one-shot commands** and `scout session`, a long-running
JSONL interface that owns one browser session until `quit`, EOF, or process
shutdown. Session mode supports `navigate`, `observe`, `annotate`, `click`, `batch`,
`click_handle`, `type`, `press_key`, `key_down`, `key_up`, `eval`, `switch_frame`, `main_frame`, `open_tab`,
`switch_tab`, `close_tab`, `list_tabs`, `reconcile`, and `shutdown` (close the
browser session and exit). `quit` also exits. Each line is a JSON
object with a `command` field; the response is one JSON object with the same
`id` when supplied plus `ok`, `result`, or `error`. Mutating and observation
requests accept `target_id`; handle clicks also accept `frame_id` and refuse
stale target or frame identities. `reconcile` accepts a versioned recovery
record in `options.recovery_record` or its base64-encoded JSON form in
`options.recovery_record_base64`. Requests may set `timeout_ms` to bound one
operation; `press_key` accepts an optional `expected_postcondition`, and held
keys should be paired with `key_up` (Scout releases any held keys on reset or
shutdown). If a dispatched mutation times out, preserve its returned recovery
record and reconcile it before retrying. A `batch` request supplies the normal
`execute_batch` fields in an `actions` array; it accepts up to 100 actions and
returns the same evidence counts and per-step receipts. If a batch deadline
interrupts execution, the response includes completed steps under
`partial_result`; uncertain steps include a portable `recovery_record` that can
be passed to `reconcile` from a fresh Scout session if the same browser target
is still available. Use `shutdown` for an acknowledged
browser close, `quit` to exit, or EOF to close the session.

CLI defaults to visible browser (`--headless` to hide):

```bash
scout doctor --json                  # validate configuration without launching a browser
scout doctor --live --live-timeout=5s # also launch/attach and check browser session health
scout doctor --live --probe-url=https://fixture.example/health --probe-text="ready" # opt-in isolated-tab application probe
scout doctor --http-listen=0.0.0.0:4201 --profile=core --require-tools=task_run,get_task_run
scout navigate <url> [--expected-text=TEXT] # navigate and optionally verify the resulting page
scout type <url> <selector> <text>    # type and report the committed value
scout press-key <url> <key-or-chord> [--expected-text=TEXT] # press and optionally verify the page
scout key-down <url> <key>             # hold a key until key-up
scout key-up <url> <key>               # release a held key
scout submit <url> <selector> [--match-url=URL] [--expected-text=TEXT] # submit and report response/verification
scout observe <url>                   # structured observation
scout markdown <url>                  # compact markdown
scout readable <url>                  # main readable content (boilerplate stripped)
scout accessibility <url>             # semantic accessibility tree
scout screenshot <url> [--output f]   # save screenshot
scout pdf <url> [--output f]          # save PDF
scout extract <url> <selector>        # extract text
scout table <url> <selector>          # extract a table as structured rows
scout auto-extract <url>              # auto-detect dominant data pattern
scout eval <url> <expression>         # run JavaScript
scout form discover <url>             # discover form fields
scout frameworks <url>                # detect frameworks
scout app-state <url>                 # global app/store state (JSON)
scout aria <url>                      # ARIA violation report (JSON)
scout vitals <url>                    # Core Web Vitals (JSON)
scout console <url>                   # console errors + network 4xx/5xx (JSON)
scout dialog <url>                    # detect visible modal/dialog (JSON)
scout auth-wall <url>                 # detect login/auth wall (JSON)
scout cookies <url>                   # list cookies (values redacted)
scout watch <url> [--interval=5s]     # live-watch page changes
scout pipe <command> [selector]       # batch process URLs from stdin
scout record <url> [--output f]       # interactive recording → playbook
scout mcp serve                       # start MCP server
scout ui serve [flags]                # start chat UI
scout session [--url=URL] [--headless] [--task-store=PATH] # persistent JSONL browser session
scout version                         # print version
```

`scout doctor` checks private-IP policy settings, MCP profile and required-tool
configuration, origin/principal policies, the optional durable task-store path,
whether the local browser executable is discoverable when local launch is
selected, and whether a remote CDP endpoint is configured. Pass `--http-listen` to
validate listener binding and authentication requirements. By default it does
not launch a browser or connect to CDP; JSON checks for live browser readiness
are marked `not_checked`. Add `--live` to launch a local browser or attach to
`SCOUT_REMOTE_CDP`, then check session status. `--live-timeout` bounds startup
and is limited to 30 seconds. Live mode may create a local browser process and
connect to a remote endpoint, so use it only when those actions are appropriate.
For an opt-in application check, add `--probe-url` and optionally `--probe-text`.
Scout opens a separate temporary tab, restricts it to the probe URL's origin,
checks that navigation completed and (when provided) that the marker appears,
then restores the prior target and closes the temporary tab. Use a dedicated
health fixture: the requested page is contacted and its script runs. The URL and
page text are omitted from doctor output. `--probe-text` requires `--probe-url`,
and a probe requires `--live`.
JSON output uses schema version 3. A configuration-only run reports top-level
`warning` because browser readiness was not checked; it still exits zero. The
command exits nonzero for invalid configuration, an unusable store path, or a
failed live check. Other warnings (such as no durable store) also keep exit
status zero. Each check includes a stable `remediation` code
for host automation; use `status` and `remediation`, not `detail` text, for
machine decisions:

| Remediation | Meaning |
|---|---|
| `none` | This check passed. |
| `run_live_check` | This check was not performed; rerun with `--live` where appropriate. |
| `repair_configuration_before_live_check` | Correct browser endpoint configuration before live probing. |
| `repair_policy_or_profile_configuration` | Correct the named policy or MCP profile. |
| `repair_listener_or_auth_configuration` | Correct the listener address or authentication settings. |
| `configure_or_repair_task_store` | Configure durable storage or repair its path and permissions. |
| `initialize_task_store_on_service_start` | The store does not exist; Scout will initialize it when the service starts. |
| `verify_store_owner_or_retry` | Another process holds the store lock; determine whether it is the active Scout service. |
| `install_chrome_or_chromium_or_set_remote_cdp` | Install local Chrome/Chromium or set a remote CDP endpoint. |
| `repair_remote_cdp_endpoint` | Correct the remote CDP endpoint URL and confirm the browser host is reachable. |
| `check_browser_endpoint_and_connection` | Check browser availability and endpoint connectivity. |
| `reconnect_browser_session` | Check that the browser is running, then reconnect and rerun the read-only status check. |
| `inspect_unresponsive_target` | Inspect the active page or renderer before deciding whether to switch targets or recover the page. |
| `inspect_browser_processes_and_targets` | Inspect browser processes and targets after the doctor session failed to close cleanly. |
| `verify_proxy_health_and_decisions_or_configure_remote_browser_egress` | Check the configured proxy and its policy decisions, or configure egress on the remote browser host. |
| `repair_probe_url_or_application` | Check the health fixture, its origin policy, and the expected marker. |
| `check_probe_url_and_browser_policy` | Check the probe URL and its browser policy. |
| `verify_health_marker_and_application_readiness` | Confirm the expected marker and application readiness. |
| `inspect_browser_tabs_and_restore_original_session` | Inspect browser tabs and restore the original session after probe cleanup failed. |
| `repair_browser_attach_before_app_probe` | Restore browser launch or attachment before running the application probe. |
| `repair_browser_status_before_read` | Restore a live browser status before attempting the bounded page read. |
| `repair_browser_session_before_status` | Restore browser launch or attachment before checking session status. |
| `repair_browser_session_before_read` | Restore browser launch or attachment before attempting the bounded page read. |
| `repair_browser_status_before_app_probe` | Restore a live browser status before running the application probe. |
| `repair_browser_read_before_app_probe` | Restore a bounded active-page read before running the application probe. |
| `inspect_check_detail` | Inspect the check detail and choose an operator action. |

These hints never authorize retrying a browser mutation.
Default preflight does not open the task store. In `--live` mode, an existing
store is opened read-only to check database structure and lock availability;
this does not test write capacity or modify task records. A locked store is a
warning because the lock may belong to a healthy running service. A missing
store remains unchecked and is not created by doctor.

For scripts, prefix a command with `--json-errors` to print one JSON error
envelope to stderr while keeping successful results on stdout. Unknown mutation
outcomes use `SCOUT_ACTION_OUTCOME_UNKNOWN` with `outcome`, `action`, `target`,
`target_id`, `expected_postcondition`, `evidence`, `detail`, and `hint`; inspect the current page before retrying.
An interrupted `fill_form` or `fill_form_semantic` may also include `partial_result` with per-field outcomes collected before the interruption, including any field whose mutation could not be verified. A timed-out `batch` returns `SCOUT_OPERATION_INTERRUPTED` with completed and uncertain step outcomes in `partial_result`. Batch results keep `succeeded` as the count of steps that completed, and report mutually exclusive `verified`, `rejected`, `unverified`, and `unknown` evidence counts. Rejected and unknown steps are included in `failed`; together, the evidence counts account for every returned step. Successful `wait` and semantic form-fill steps report observed selectors or field readbacks as verified; other actions need a postcondition for application-effect verification. Failed semantic form fills include per-field outcomes so callers can inspect earlier changes before retrying.
Other failures use `SCOUT_OPERATION_ERROR` when Scout has a classified
operation error, or `SCOUT_ERROR` for an unclassified error.
MCP responses keep this text block and also expose parsed Scout envelopes in
`_meta["go.klarlabs.de/scout/error"]` for host code that needs typed access.

```bash
scout --json-errors --recovery-file=action-recovery.json click \
  https://example.com '#save' --headless --expected-text='Saved'
```

If the browser is managed outside Scout and remains available after the command
exits, an unknown click can be reconciled from a fresh CLI process. Set
`SCOUT_REMOTE_CDP` to that browser's endpoint and copy the action, target ID,
and expected condition from the error envelope. `reconcile` can load the saved
artifact directly, attaches to that exact live page, checks the condition, and
never repeats the click. Recovery files use schema version 3 and owner-only
permissions. Schema version 1 flat artifacts remain readable. Go callers can create
the same portable record with `agent.NewActionRecoveryRecord`, encode it with
`json.Marshal`, and restore it with `agent.DecodeActionRecoveryRecord`. The Go
`Session.ReconcileActionRecoveryRecordContext` helper validates and checks that
record against its original live target. MCP errors include `recovery_record`
when a typed outcome contains enough target identity; pass that object to
`reconcile_action` to verify without replaying:

```bash
SCOUT_REMOTE_CDP=http://127.0.0.1:9222 \
  scout reconcile --recovery-file=action-recovery.json
```

For manual recovery, the condition flags are `--expected-text`, `--expected-url-contains`,
`--expected-visible-selector`, `--expected-value-selector` with
`--expected-value`, or `--expected-count-selector` with `--expected-count`.
The target must still exist in the same browser process; Scout refuses to infer
the result from another tab or a replacement target.

For `scout submit`, `--expected-*` checks a page condition, while `--acceptance-*` requires both an observed response and a matching page condition before `application_accepted` is true. The result keeps the HTTP status separate from application acceptance; a successful HTTP status alone does not mean the operation succeeded.

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
├── cmd/scout/                         # CLI + MCP server with profile-based tool access
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
