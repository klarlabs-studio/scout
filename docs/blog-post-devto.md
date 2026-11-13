---
title: "Building a Go Browser Engine for AI Agents and Services"
published: false
description: "Scout is a local-first Go browser engine with a CLI, MCP server, chat UI, and Go library for verified browser actions and deterministic workflows."
tags: go, ai, mcp, automation
cover_image: https://klarlabs-studio.github.io/scout/logo.svg
---

# Building a Go Browser Engine for AI Agents and Services

Browser automation from a Go service or agent worker creates a different set of constraints than browser testing from a TypeScript application. The engine needs to fit an existing Go process, expose useful actions to an MCP client, and make it possible to inspect what happened when a browser mutation returns an uncertain result.

**Scout** is an experiment in that workflow: a Go browser engine with a library, CLI, MCP server, and chat UI over a shared session model. Its product direction is still being evaluated with users; this article describes the implementation and the tradeoffs it makes.

## What Scout Is

Scout is a Go browser execution engine that connects to Chrome through the DevTools Protocol. Install the CLI with Homebrew and register it as an MCP server:

```bash
brew install --cask klarlabs-studio/tap/scout-mcp
claude mcp add scout -- scout mcp serve
```

Or drop the JSON config into any MCP-compatible client:

```json
{
  "mcpServers": {
    "scout": {
      "command": "scout",
      "args": ["mcp", "serve"]
    }
  }
}
```

Local sessions need Chrome or Chromium installed separately; Scout can also attach to a browser managed elsewhere over CDP. The CLI is distributed as a binary, while the Go library can be embedded directly in a Go application.

## MCP workflows

The MCP server exposes actions for common browser workflows, including:

- **Navigate and inspect**: `navigate`, `observe`, `observe_diff`
- **Interact with pages**: `click`, `type`, and `fill_form_semantic`
- **Extract content**: `extract`, `markdown`, and `accessibility_tree`
- **Record and replay**: save and replay deterministic playbooks
- **Recover interrupted work**: inspect and resolve durable task runs

The available catalog depends on the selected MCP profile. For new client setups, use `--profile=core`; run `scout mcp serve --help` to see configuration options. Profiles shape discovery and workflow access, not authorization.

## Task-shaped observations

Agents often need a small, relevant view of a page rather than a full document. Scout provides several representations so callers can choose based on the task.

### DOM Diffing

After the initial `observe`, subsequent calls to `observe_diff` return only what changed. Scout installs a MutationObserver on the page and classifies mutations into categories like `modal_appeared`, `form_error`, `notification`, or `loading_complete`. Instead of re-sending the entire page state after a click, the agent sees a structured summary of the delta.

The diff is a different representation of the page state, not a guaranteed token reduction. Its size depends on the page and the changes being observed.

### Five Levels of Content Distillation

Not every task needs the same level of detail. Scout offers a spectrum:

| Method | Best for |
|--------|----------|
| `observe()` | Deciding what to click or fill |
| `observe_diff()` | Inspecting changes after an action |
| `markdown()` | Reading page content compactly |
| `readable_text()` | Focusing on the main article or body text |
| `accessibility_tree()` | Inspecting semantic elements |

The agent picks the right level for the task. A summarization job uses `readable_text`. A form-filling job uses `observe`. A status check uses `observe_with_budget(200)`.

### Screenshot Auto-Compression

The Go session API defaults to a 5MB screenshot ceiling. MCP screenshots use a tighter 80KB inline limit and a 1024px default width so the image fits comfortably in tool context; Scout progressively lowers JPEG quality and resolution to meet it. Use `save_to_disk` or `output_path` for a full-resolution image intended for a person to view.

## Semantic Form Filling

One of my favorite features. Instead of requiring CSS selectors, `fill_form_semantic` accepts human-readable field names:

```go
session.FillFormSemantic(map[string]string{
    "Email":    "user-example",
    "Password": "hunter2",
})
```

Scout matches labels to inputs using the same heuristics a human would -- `<label>` elements, `aria-label`, `placeholder`, proximity. This eliminates the most common failure mode in form automation: brittle selectors that break when the page changes.

## Visual Grounding with Annotated Screenshots

`annotated_screenshot` overlays numbered labels on every interactive element. The agent sees a screenshot with `[1]`, `[2]`, `[3]` on buttons and links, then calls `click_label(7)` to act. No selector needed. This is especially useful for visually complex pages where CSS selectors are ambiguous or deeply nested.

## Two API Layers

Scout is not just an MCP server. It is also a Go library with two distinct API surfaces.

**The core `browse` package** follows Gin's patterns -- Engine, Context, Group, HandlerFunc. If you are a Go developer writing automation scripts, this is your entry point:

```go
engine := browse.Default(browse.WithHeadless(true))
engine.MustLaunch()
defer engine.Close()

engine.Use(middleware.Stealth())
engine.Use(middleware.Retry(middleware.RetryConfig{MaxAttempts: 3}))
engine.Use(middleware.Timeout(30 * time.Second))

engine.Task("scrape", func(c *browse.Context) {
    c.MustNavigate("https://example.com")
    table, _ := c.ExtractTable("#data")
    c.Set("result", table)
})
engine.Run("scrape")
```

**The `agent` package** wraps everything into a `Session` type with structured JSON output, mutex-protected concurrency safety, and auto-wait. This is what the MCP server uses internally, and it is what you use if you are building your own AI agent orchestrator in Go.

The middleware stack includes Retry, Timeout, CircuitBreaker, RateLimit, Bulkhead, and a Stealth module that applies 10 anti-detection patches (webdriver flag, plugin enumeration, WebGL fingerprint, and more). Retry does not repeat a browser operation after an unknown outcome by default, because the page may have accepted it before its acknowledgment was lost; inspect application state before replaying.

## Playbook Recording

For repetitive workflows, Scout can record agent actions into a playbook:

```go
session.StartRecordingPlaybook("login-flow")
session.Navigate("https://app.example.com/login")
session.FillFormSemantic(map[string]string{"Email": "user-example", "Password": "secret"})
session.Click("#submit")
pb, _ := session.StopRecordingPlaybook()
agent.SavePlaybook(pb, "login.json")
```

Later, `replay_playbook` re-executes the entire flow without involving the LLM at all. This is useful for login flows, setup sequences, or any deterministic prefix to an otherwise exploratory task.

## Built on Purpose-Built Libraries

Scout does not pull in massive framework dependencies. The resilience middleware comes from [fortify](https://github.com/klarlabs-studio/fortify), a standalone library for retry, circuit breaker, and bulkhead patterns. Structured logging uses [bolt](https://github.com/klarlabs-studio/bolt), a zero-allocation logger. The MCP protocol implementation uses [mcp-go](https://github.com/klarlabs-studio/mcp-go). State machine transitions (pending, running, success, failed, aborted) use [statekit](https://github.com/klarlabs-studio/statekit). Each piece is independently testable and reusable.

## Try It

```bash
brew install --cask klarlabs-studio/tap/scout-mcp
scout observe https://news.ycombinator.com
```

That command launches an installed Chrome-compatible browser, navigates to Hacker News, and prints a structured observation. Use `scout doctor --live` to check browser readiness first.

To use it as an MCP server with Claude Code:

```bash
claude mcp add scout -- scout mcp serve
```

The source is at [go.klarlabs.de/scout](https://github.com/klarlabs-studio/scout). MIT licensed. Stars and contributions are welcome -- especially around additional framework detectors, new extraction strategies, and real-world playbook examples.

If your application or agent worker is written in Go and you need browser actions with inspectable outcomes, try Scout and share where the workflow does or does not fit.
