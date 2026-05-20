# Scout MCP Gotchas

Operational notes for callers driving Scout via MCP. These document observed
edge cases that the tool API can't fully hide.

## Selectors

- **CSS strictness under Chromium + Astro.** Some attribute selectors that
  match in DevTools (e.g. `input[type=text]` on an Astro-hydrated form) can
  return zero matches against the live CDP DOM until hydration finishes.
  Workarounds, in order: drop the attribute (`input` instead of
  `input[type=text]`), use `wait_for_spa_idle` before the action, or use
  `discover_form` + `fill_form_semantic` which match by label/name rather
  than CSS.
- **Stale node IDs after framework re-render.** Scout re-resolves selectors
  at action time and retries once on `cdp: error -32000: Could not find
  node with given id`. If a third attempt is needed, prefer `wait_for_spa_idle`
  → action over manual retries.
- **`annotated_screenshot` labels are per-call only.** Every call recomputes
  the numbering from the current DOM. Pair `click_label` with the most
  recent `annotated_screenshot` in the same turn. For identity stable
  across mutations, use the `selector` field returned in `elements`.

## Network capture

- **`enable_network_capture` is a future-tense subscription, not a backfill.**
  Calling it after an action misses that action's requests. Order is:
  `enable_network_capture` → action that triggers the request →
  `network_requests`. If `network_requests` returns an empty list and
  capture was never enabled, the response carries a hint pointing at
  `enable_network_capture`.

## Forms

- **`dispatch_event submit` uses `form.requestSubmit()` under the hood.**
  Vue 3's `@submit.prevent` and React's `onSubmit` won't fire on a bare
  `new Event('submit')`; Scout dispatches via `form.requestSubmit()` which
  also runs HTML5 validation.
- **For the canonical fill → submit → wait flow, use `submit_form`.**
  One call replaces click + sleep + check_readiness + network_requests:
  it submits via `requestSubmit`, then waits for either an XHR/fetch
  whose URL contains `match_url` or a URL change, returning the response
  status and the post-submit URL.

## Tool-result token budget

- MCP tool results have a soft per-call cap (around 25k tokens depending on
  the host). Scout defaults stay well under this:
  - `screenshot`: JPEG quality 60, `max_width` 1024, 80KB hard cap. The
    image is progressively downscaled before failing.
  - `observe` / `observe_with_budget`: capped at 25 links, 20 inputs,
    15 buttons by default.
  - `extract_all`: 50 items by default; `extract_table`: 100 rows.
  - `network_requests`: response bodies truncated to 32KB per request.
- If a call returns truncated content, look for `truncated: true` on the
  result. Re-issue with a tighter filter (pattern, selector, `max_recent`)
  rather than a larger budget.

## Session health

- **Broken pipe / dead socket recovery.** When a CDP write fails with
  `broken pipe`, `connection reset`, or `websocket: close`, the next
  `status` call surfaces `session_dead: true` with a `dead_reason`.
  Recover with `configure { fresh: true }` to spin up a clean browser
  before the next action.
- **`reset` vs `configure { fresh: true }`.** `reset` re-creates a page
  inside the same browser; `configure { fresh: true }` kills the browser
  process and starts a new one. Use `reset` for stuck SPA state, `fresh`
  for dead sockets or accumulated browser state.
