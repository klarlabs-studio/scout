---
title: "How I Replaced Playwright MCP with a Single Go Binary"
published: false
description: "Scout is a 66-tool MCP server that gives AI agents a real browser — pure CDP, zero runtime dependencies, built for token efficiency."
tags: go, ai, mcp, automation
cover_image: https://klarlabs-studio.github.io/scout/logo.svg
---

# How I Replaced Playwright MCP with a Single Go Binary

Every MCP server that gives AI agents a browser has the same problem: it ships an entire runtime. Playwright MCP needs Node.js. Python-based alternatives need a Python interpreter, virtualenv, and pip packages. By the time you have a working setup, you have pulled in hundreds of megabytes of dependencies for what is conceptually a simple job -- let the LLM see and interact with a web page.

Then there is the token problem. Most browser MCP tools return raw HTML or massive accessibility snapshots. A single `observe` call can blow through 10,000 tokens of context window before the agent has done anything useful. Multiply that by every step in a multi-page workflow and you are burning money on DOM noise.

I built **Scout** to fix both problems.

## What Scout Is

Scout is a single Go binary that implements the Chrome DevTools Protocol directly over WebSocket. No rod, no chromedp, no Node.js, no Python. You install it with Homebrew and register it as an MCP server in one line:

```bash
brew install klarlabs-studio/tap/scout
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

That is the entire setup. The binary is around 15MB. Chrome is the only external dependency, and you already have it.

## 66 Tools, Organized by What Agents Actually Need

Scout exposes 66 MCP tools grouped into categories that match real agent workflows:

- **Navigation**: `navigate`, `observe`, `observe_diff`, `observe_with_budget`
- **Interaction**: `click`, `click_label`, `type`, `hover`, `drag_drop`, `select_option`, `scroll_to`, and more
- **Forms**: `fill_form`, `fill_form_semantic`, `discover_form`
- **Extraction**: `extract`, `extract_table`, `auto_extract`, `markdown`, `readable_text`, `accessibility_tree`
- **Capture**: `screenshot`, `annotated_screenshot`, `pdf`
- **Network**: `enable_network_capture`, `network_requests`
- **Tabs**: `open_tab`, `switch_tab`, `close_tab`, `list_tabs`
- **Frameworks**: `detect_frameworks` (19 frameworks), `component_state`, `app_state`, `wait_spa`
- **Playback**: `start_recording`, `stop_recording`, `save_playbook`, `replay_playbook`
- **Diagnostics**: `detect_dialog`, `detect_auth_wall`, `console_errors`, `compare_tabs`

All tools carry MCP annotations like `ReadOnly` and `Idempotent` so clients can auto-approve safe operations without prompting.

## The Token Problem, Solved

This is where Scout diverges most from existing browser MCP servers. Every feature is designed around one question: how do we give the agent maximum information in minimum tokens?

### DOM Diffing

After the initial `observe`, subsequent calls to `observe_diff` return only what changed. Scout installs a MutationObserver on the page and classifies mutations into categories like `modal_appeared`, `form_error`, `notification`, or `loading_complete`. Instead of re-sending the entire page state after a click, the agent sees a structured summary of the delta.

In practice, this saves 50-80% of tokens per step.

### Five Levels of Content Distillation

Not every task needs the same level of detail. Scout offers a spectrum:

| Method | Output size | Best for |
|--------|------------|----------|
| `observe()` | ~2-5 KB | Deciding what to click or fill |
| `observe_diff()` | ~0.5-2 KB | Seeing only what changed |
| `markdown()` | ~2-8 KB | Reading page content compactly |
| `readable_text()` | ~1-4 KB | Main article text only |
| `accessibility_tree()` | ~1-4 KB | Semantic element tree |

The agent picks the right level for the task. A summarization job uses `readable_text`. A form-filling job uses `observe`. A status check uses `observe_with_budget(200)`.

### Screenshot Auto-Compression

Screenshots default to a 5MB ceiling. When the raw capture exceeds that, Scout progressively re-captures as JPEG with lower quality (80, 60, 40, 20) and smaller viewport scale (1.0, 0.75, 0.5, 0.25) until it fits. No manual tuning required.

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

The middleware stack includes Retry, Timeout, CircuitBreaker, RateLimit, Bulkhead, and a Stealth module that applies 10 anti-detection patches (webdriver flag, plugin enumeration, WebGL fingerprint, and more).

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
brew install klarlabs-studio/tap/scout
scout observe https://news.ycombinator.com
```

That one command launches Chrome, navigates to Hacker News, extracts a structured snapshot of every link, button, and input on the page, and prints it as JSON. No config files, no setup, no runtime.

To use it as an MCP server with Claude Code:

```bash
claude mcp add scout -- scout mcp serve
```

The source is at [go.klarlabs.de/scout](https://github.com/klarlabs-studio/scout). MIT licensed. Stars and contributions are welcome -- especially around additional framework detectors, new extraction strategies, and real-world playbook examples.

If you have been frustrated by the weight and token cost of existing browser MCP tools, give Scout a try. It is one binary, and it just works.
