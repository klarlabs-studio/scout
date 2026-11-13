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
- A request keeps the observer mode it had when it started. Enabling capture
  does not promote an already-running passive request into a full capture;
  disabling capture prevents an in-flight explicit request from being added
  to history after the boundary. Pending explicit requests are reported as a
  known gap when capture is disabled.
- Capture retains an oldest-to-newest rolling window of at most 200 completed
  requests and at most 200 in-flight requests. `network_requests` pages at most
  3 records at a time; offsets refer to the current retained window and can
  shift as newer requests evict old ones. `dropped_total` is session-wide and
  counts in-flight rejections and completed-history eviction, not just records
  matching the current URL filter.

## Forms

- **`dispatch_event submit` uses `form.requestSubmit()` under the hood.**
  Vue 3's `@submit.prevent` and React's `onSubmit` won't fire on a bare
  `new Event('submit')`; Scout dispatches via `form.requestSubmit()` which
  also runs HTML5 validation.
- **For the canonical fill → submit → wait flow, use `submit_form`.**
  One call replaces click + sleep + check_readiness + network_requests:
  it submits via `requestSubmit`, then waits for either an XHR/fetch
  whose URL contains `match_url` or a URL change, returning the response
  status and the post-submit URL. A successful response or navigation only
  proves that transport activity occurred; pass one `expected_*` condition
  (for example `expected_text`) to verify the application accepted the form.

## Tool-result token budget

- MCP tool results have a soft per-call cap (around 25k tokens depending on
  the host). Scout defaults stay well under this:
  - `screenshot`: JPEG quality 60, `max_width` 1024, 80KB hard cap. The
    image is progressively downscaled before failing. `image_info` reports
    initial/returned dimensions and byte sizes plus compression quality/scale.
    `annotated_screenshot` and `hybrid_observe` include this metadata whenever
    their image is returned or saved.
  - `observe` / `observe_with_budget`: capped at 25 links, 20 inputs,
    15 buttons by default.
  - `extract_all`: 8 items per call, 4,000 characters per item. Continue only
    when `has_more` is true and pass the returned `next_offset` with the same
    `limit`; `items_truncated` and per-item `item_truncated` distinguish clipped
    text from additional matches. The legacy `truncated` flag is true for
    either condition. Each call reads the live DOM, so restart at offset 0 if
    the page changes between requests. `extract_table` returns up to
    25 rows, 10 columns, and 256 characters per cell.
  - `compare_tabs`: up to 40 heading/price/link entries per page and 256
    characters per compared value, with source totals and `truncated` when
    more page content was present.
  - `network_requests`: response bodies truncated to 32KB per request, URL to
    4KB, and request/response headers to at most 64 values of 2KB each.
    Authorization, proxy-authorization, cookie, set-cookie, and credential-like
    header names containing common auth/token/key/secret/CSRF markers are
    redacted at capture time; this heuristic may miss application-specific
    secret headers. URLs and
    request/response bodies may still contain secrets or personal data; treat
    them as sensitive untrusted page content and avoid exporting them to
    shared logs or prompts without review.
- If a call returns truncated content, look for `truncated: true` on the
  result. Re-issue with a tighter filter (pattern, selector, `max_recent`)
  rather than a larger budget.
- Routine structured observations and text snapshots omit values from
  password and file inputs. This applies to `observe`, `observe_scoped`,
  Markdown/accessibility snapshots, and annotated or hybrid element labels;
  explicitly targeted actions and caller-authored page scripts remain able
  to read page state by design.

## Session health

- **Correlate health with the page in use.** `status` returns `active_tab`,
  `active_target_id`, and `current_url` for the page selected by the active
  session. The target ID matches `target_id` on tab and action results and is
  stable only while that browser target remains open. Before the first page is
  attached, the identity fields are omitted; use `list_tabs` after navigating
  to inspect all open targets and their active state. A tab with
  `metadata_stale: true` did not answer its bounded metadata read; its URL and
  title are last-observed values, and its visibility state is unavailable.
  Treat them as hints only and verify the target again before acting on it.

- **Request deadlines for common actions.** `navigate`, `observe`, `observe_scoped`, `observe_diff`, `observe_with_budget`, `markdown`, `readable_text`, `accessibility_tree`, `save_page_snapshot`, `extract`, `extract_all`, `extract_table`, `wait_for`, `wait_spa`, `wait_for_spa_idle`, `wait_for_navigation`, `has_element`,
  `click`, `type`, `fill_form`, `fill_form_semantic`, `select_option`, and `submit_form`
  propagate the MCP request context through session queueing, first-page discovery/attachment, their primary browser operations,
  and verification reads. Semantic filling includes open shadow roots and
  stable-order field results. The submit flow also bounds its in-page
  response/navigation watcher and network event fallback. Natural-language selector matching in click/type and batch now also uses
  caller contexts. Other less common selector fallback paths may still use legacy calls. `hover`, `double_click`, `right_click`, `scroll_to`, `scroll_by`,
  `focus`, `dispatch_event`, `click_label`, `click_handle`, `click_text`, `drag_drop`, `switch_to_frame`, `switch_to_main_frame`, and `batch`
  also propagate the context through session queueing and their browser work.
  `open_tab`, `switch_tab`, `close_tab`, and `list_tabs` now propagate it
  through initial target discovery, creation/closure, page attachment and setup,
  navigation, activation, and page-state reads.
  New page attachment and required CDP domain setup also use the request context;
  failed setup triggers bounded target cleanup for new pages and detaches without closing an existing target.
  Tool follow-up operations and less common mutation paths still use legacy
  calls.

- **Broken pipe / dead socket recovery.** When a CDP write fails with
  `broken pipe`, `connection reset`, or `websocket: close`, the next
  `status` call surfaces `session_dead: true` with a `dead_reason`.
  Recover with `configure { fresh: true }` to spin up a clean browser
  before the next action.
- **`reset` vs `configure { fresh: true }`.** `reset` re-creates a page
  inside the same browser; `configure { fresh: true }` kills the browser
  process and starts a new one. Use `reset` for stuck SPA state, `fresh`
  for dead sockets or accumulated browser state.
