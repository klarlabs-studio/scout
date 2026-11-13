# Scout product roadmap

This document tracks work that remains open. It replaces the original feature
wishlist, much of which shipped while its issue descriptions stayed marked as
pending. “Implemented” means the code is present in this worktree; check the
linked evidence and current release before making a shipped-product claim.
Competitor comparisons and buyer evidence are tracked separately in the
[opportunity brief](opportunity-brief.md), [competitive research](competitive-opportunities.md),
and [adoption study](adoption-study.md).

## Current product direction

Make Scout a dependable browser execution runtime for Go services. The host
owns orchestration, identity, persistence, and business truth. Scout should
preserve a task's operation identity and browser evidence across interruption,
report uncertainty without replaying mutations, and make it straightforward
for the host to reconcile against its authoritative application state.

This remains a product hypothesis. Repository behavior and fixture runs do not
establish demand, lower integration effort, or superiority over competitors.

## Open product and evidence work

### P0 — Cross-interface recovery receipt parity (acceptance met)

Export equivalent task records through the Go API, stateful CLI, and MCP, then
normalize and compare the results with both the Go decoder and independent
Python verifier. Cover interrupted and completed tasks, non-first task steps,
target/frame identity, policy digest, and replay prohibition. Keep MCP owner
namespaces explicit; do not weaken isolation just to share a physical record.

**Acceptance:** one reproducible harness reports per-interface field parity,
validator outcomes, and any namespace-specific differences. See
[`recovery-receipt-contract.md`](recovery-receipt-contract.md) and the
[portability proof gate](opportunity-brief.md#recovery-contract-portability-next-proof-gate).

The checker now takes a multi-case manifest, requires interrupted, completed,
and non-first-step cases, runs both the Go decoder and independent Python
verifier for each Go/CLI/MCP export, checks policy-digest and frame-ID fixtures,
and reports field mismatches and namespace labels. Its manifest template is at
[`examples/benchmark/recovery_receipt_parity/manifest.example.json`](../examples/benchmark/recovery_receipt_parity/manifest.example.json).
The capture utility now seeds interrupted and completed records, including a
two-operation result, then exports through all three public surfaces. Interrupted,
completed, and non-first-step (operation 2) cases have matching normalized fields
across Go, CLI, and MCP; every receipt passes the Go decoder and independent
Python verifier, with replay prohibited. Reproduce with the instructions at
[`recovery_receipt_parity/README.md`](../examples/benchmark/recovery_receipt_parity/README.md).
Checked-in receipts and the audit report are at
[`manifest.live.json`](../examples/benchmark/recovery_receipt_parity/manifest.live.json)
and [`seeded receipts`](../examples/benchmark/recovery_receipt_parity/captures/).
The comparer now binds the independent verifier's task ID, operation ID,
outcome, and replay flag back to each decoded receipt; only passing supported
cases count toward required scenario coverage. Unit regressions cover mismatched
identity, outcome, replay, and failed or unsupported coverage, and the seeded
three-case report passes again.
The non-first-step scenario now also requires a completed task, verified
operation, and replay prohibition; an interrupted or unverified step 2 cannot
stand in for completed multi-step evidence. Regression cases cover each rejected
state, and the checked-in live manifest still passes all three required
scenarios.
These are seeded interface captures; no browser actions ran, so they prove
export/schema consistency rather than end-to-end task behavior or application
outcomes.

A fresh capture from the current source on 12 November generated all nine Go,
stateful CLI, and MCP exports, then ran the comparer against those outputs.
Interrupted, completed, and completed non-first-step cases passed both receipt
validators and all normalized field comparisons, with no missing required
scenario. Compact manifests and reports are checked in under
[`benchmark/evidence/recovery-receipt-parity-live-2026-11-12`](../examples/benchmark/evidence/recovery-receipt-parity-live-2026-11-12/). The full raw export archive remains local because it includes redundant captures.
The capture uses synthetic seeded stores and no browser actions; producer
authentication, independent browser binding, and application outcome remain
outside this acceptance gate. CI continues to audit the checked-in manifest.

A separate Chrome-backed process-loss matrix exercises stateful CLI and MCP
after worker termination: ten trials per surface pass, all ten paired task
fingerprints and origin policies match, and each fixture ledger remains at one
effect/request. The runner alternates order and isolates each pair in a fresh
Chrome process after a single-browser ten-pair attempt stalled. This does not
replace the receipt export/parity acceptance above; it adds runtime recovery
evidence at the two public interfaces. See
[`live-receipt-parity.json`](../examples/benchmark/evidence/recovery-receipt-parity-live-2026-11-12/live-receipt-parity.json).

The runner exports the actual interrupted operation receipt from the
replacement process before host reconciliation. The earlier live capture at
`scout-three-surface-process-loss-2026-11-11` (local audit artifact)
reported the Go decoder as valid without invoking it for the Go SDK receipt;
its decoder claim is therefore not verified and should be treated as
historical. The runner now invokes the strict decoder for that receipt and
checks its task identity before recording success.

A fresh Chrome-backed capture ran ten trials per surface after that correction.
Go SDK, CLI, and MCP each committed one fixture effect per trial, lost the
worker, and retained `unknown` / `may_have_dispatched` with replay unauthorized.
Every interrupted receipt passed both the Go decoder and independent Python
verifier; plan fingerprints and policy match, and same-ID replay remains
blocked. All three surfaces also completed a live two-action task in every
trial, exported operations 1 and 2, and passed both validators for each
receipt. The runner gives each interface the same task ID in its isolated
store and compares the interrupted and two completed-operation receipts for
each repetition. The initial 11 November capture used main-page tasks. After
persisting the selected iframe in each task recovery plan, a 12 November
Chrome-backed capture ran the same 10-trial protocol with all task actions
inside a fixture iframe. All 30 interrupted/completed receipt comparisons
passed, both receipt validators passed all 90 exports, and every receipt was
frame-bound. The comparer requires frame presence and normalizes independent
CDP frame IDs to `<frame-bound>`; it still compares task/operation identity,
target, policy, outcome, and replay state exactly. The capture and manifest are
at
[`scout-three-surface-live-receipt-parity-2026-11-12`](../examples/benchmark/evidence/recovery-receipt-parity-live-2026-11-12/).
Each interface passed 10/10 local trials (Wilson 95% interval 72.2–100%);
this is not a customer reliability estimate or competitive result.

The checked-in seeded parity manifest is now run in the integration workflow,
so CI revalidates the three public export shapes with the Go decoder and
independent Python verifier. This protects schema/validator parity; the
separate Chrome-backed process-loss matrix remains necessary for runtime task
and application-effect evidence.

A fresh audit found the live manifest explicitly required only interrupted and
completed scenarios even though the checker supports the required
`non_first_step` case. The manifest now requires all three and adds the
frame-bound completed operation-2 receipt triplet. Re-running the current Go
decoder, independent Python verifier, and comparer passed all 31 cases and
reports all three required scenarios. The original 90-receipt capture remains
unchanged; this adds a missing acceptance assertion using its existing exports.

### P0 — Task identity drift under concurrent browser changes

Exercise an in-flight mutation while another client switches, closes, or
replaces tabs and while a distinct owner uses the same caller-selected session
label. Bind continuation to owner, browser generation, target, frame, and
policy. `RunTaskContext` binds target and page URL before durable handoff. The
batch rechecks URL after its per-action `started` checkpoint and before
dispatch. A Chrome-backed race regression navigates the same target from a
competing Scout call while handoff persistence is paused and confirms failure
before batch creation; the replacement page's matching click endpoint receives
no request. A second
regression opens and selects a replacement tab in the same window and confirms
the task fails before creating a batch or clicking the new target. A third
closes the captured page while durable handoff persistence is paused and also
confirms rejection before batch creation or click dispatch. A fourth
Chrome-backed regression attaches an independent CDP client, navigates the
captured target while durable handoff is paused, and confirms the task rejects
the changed page before batch creation or click dispatch. A fifth Chrome-backed
regression queues `ResetContext` during durable handoff and confirms the task
refuses dispatch after reset replaces its active target; the reset then
completes successfully. Session recovery checks the endpoint and
BrowserContext fingerprint before reattaching to a persisted target, so a new
endpoint or context fails closed. A recovery integration test holds the
configured HTTP discovery URL stable while `/json/version` switches to a
different live Chrome WebSocket endpoint; resume rejects the changed browser
generation even though the policy digest is unchanged. Direct WebSocket
proxies that hide browser replacement behind an unchanged endpoint and the
irreducible race between the final URL check and an external client's CDP
command remain open. Task handoff now captures the selected frame's CDP loader
identity and rechecks it at batch entry and after each durable action-start
checkpoint. A live Chrome regression reloads the same URL through an
independent CDP client while persistence is paused; the loader changes while
target, frame, and URL remain constant, and Scout rejects the task before
action 0. This detects same-URL document replacement but cannot make the final
check and browser command atomic against an external CDP client. The CDP
task click path now repeats URL, frame, and loader-identity checks after the
click point is resolved and immediately before its first mouse event. A
selection-level CDP regression verifies the guard runs after geometry lookup
and that a rejected guard dispatches no input event. Batch type, scroll, and
label-click paths also recheck task binding before their mutating CDP
evaluation. Semantic form filling rechecks before each field mutation and
between text-field focus and `Input.insertText`, where a navigation could
otherwise move focus to a replacement page. A CDP regression rejects the
second guard and verifies no text insertion follows. These checks narrow the
mutation window but do not close the external-client race after the final
check.
An additional Chrome-backed regression now mirrors the scale of the open
agent-browser #1867 long-session report: 28 page navigations and 28 verified
viewport changes, with an independent CDP client activating a competing tab
between operations. Every cycle retains the original target ID and independently
reads back the expected URL, page marker, and viewport; the test also closes
and recreates the competing tab mid-run. This passes on the local Chrome
build and proves only this Scout/Chrome fixture, not Edge compatibility or a
comparative failure rate.
The existing integration fixture blocks the durable per-action
`started` checkpoint, navigates the same target from a second client while
Scout holds its session lock, then confirms the task stops before action 0 and
the replacement page's effect endpoint receives no request. The authenticated MCP HTTP recovery integration now creates
the same completed task ID and identical plan fingerprint for two principals
on separate Chrome endpoints: the second principal cannot read the first
record and executes its own task without an idempotency replay. This covers
principal-level task-store isolation through the public MCP handlers. The same
live HTTP integration now has Alice and Bob each create a named session called
`work`; each sees only its own opaque selector, their browser targets differ,
both can switch to their own session, and Bob's attempt to select Alice's
opaque ID is rejected. This verifies same-name isolation for separate
authenticated principals through public MCP handlers.

The selected frame ID is now carried into task execution and checked again at
batch entry and after each durable action-start checkpoint. A Chrome-backed
regression changes selection from the recorded iframe to the main frame while
handoff persistence is paused; task execution fails with `frame_changed` and
the iframe mutation endpoint receives no request. This protects Scout-mediated
frame changes; direct external CDP clients can still race the final frame check
with a browser command. Batch `type`, `scroll_to`, and `click_label` now evaluate
in the selected frame context. Semantic form discovery and field lookup also
use that context and traverse its open shadow roots. A live task regression
types and verifies a value, semantically fills a labeled field, and fills a
field inside an open shadow root within its selected iframe (3/3 repetitions).
Nested frame selection resolves selectors from the active document and
matches only its direct child frames. A Chrome integration regression selects
a nested iframe, evaluates within it, navigates that child document, then clicks
and reads in the replacement execution context. This also repairs stale
isolated execution-context IDs after a selected frame navigates; the selected
frame is rebound only when CDP reports that its prior context is missing or
destroyed. The focused nested-frame test and agent unit suite pass.
The existing cross-origin nested-frame regression covers frames represented in
the page target's frame tree. Chrome exposes a cross-site `localhost` iframe as
a separate OOPIF target, which is absent from that tree. `SwitchToFrame` now
binds to one browser `iframe` target with the selected parent frame and matching
URL, rechecks the binding after attachment, enables Page/DOM/Runtime on a
child-scoped session, and applies the configured URL policy there. Standard
evaluation, inspection, selector lookup, and click actions use the child
session, while results retain the top-level target ID and child frame ID.
Returning to the main frame, changing tabs, top-level navigation, and closing
the session detach the frame session without closing the child target.

Chrome integration tests cover child DOM reads and clicks, navigation to a new
document within the same OOPIF session, a denied-origin child navigation,
main-frame restoration, tab switching, and ambiguous duplicate-URL owner
elements. Duplicate bindings fail closed. Nested site-isolated frames now use
a parent-to-child route stack; selection and recovery preserve each owner page
and frame binding, and returning to the main frame detaches the stack in reverse
order. A Chrome regression alternates `127.0.0.1` and `localhost` across two
site boundaries, reads from the grandchild, and verifies a full unwind to the
top-level page. Request-time
reattachment after target detachment now re-resolves the selected iframe owner,
requires a unique owner URL and exactly one matching child target, verifies the
new frame identity and document URL, then swaps the CDP session. A Chrome
regression removes and recreates the selected iframe from the owner page,
asserts Chrome's `Target.detachedFromTarget` event invalidates the route before
the next read, and confirms reads resume through a replacement OOPIF with a new
frame ID. Missing owners, duplicate URLs, and
ambiguous target matches fail closed. Chrome coverage now replaces an outer
owner while a nested OOPIF is selected, changes the inner target URL, and
confirms both route levels rebind before the nested read resumes. External CDP
races between an owner change and the next Scout call remain open. Browser
target-inventory errors during selection or rebinding now propagate as
inspection failures instead of becoming a false “frame not found”; unit
coverage verifies both error preservation and unique target matching. The
direct CDP attachment probe
separately confirms target session attach/read/detach behavior.

The stateful MCP request middleware now routes a browser call to the active
session snapshot captured when its in-flight count is incremented. It no
longer rereads the mutable active-session pointer after unlocking, which could
route the call to a session that was not protected from concurrent close.
Authenticated HTTP same-name isolation is covered by the live principal test;
the narrow interleaving between active-session capture and route setup still
lacks a deterministic race fixture.

**Acceptance:** no wrong-target mutation or cross-owner read; ambiguous or
changed bindings fail closed; browser restoration never counts as business
outcome verification. Record per-run target and effect ledger evidence in the
[runtime conformance matrix](../examples/benchmark/OPPORTUNITY_MATRIX.md).

### P1 — Policy and evidence conformance fixtures

Run the same malformed/empty-policy, newest-evidence-under-output-budget, and
unknown/deprecated-input cases against Scout and pinned competitors. Verify
the effective policy and source-side effects independently, and preserve
truncation/completeness metadata. Scout now rejects explicit empty origin
entries in `create_session`, preventing a blank requested policy from clearing
an inherited allowlist; tests distinguish an unset server policy from an empty
one. `ObserveWithBudget` truncates at a valid UTF-8 boundary, with a focused
regression covering byte limits inside and around multibyte runes. A focused
MCP response fixture forces playbook receipt compaction and verifies the newest
receipt survives with explicit truncation metadata under the response cap.
`ObserveWithBudget` now preserves a calculated zero interactive allowance;
previously the generic observation path interpreted zero as a request for
default limits and could exceed tiny budgets. A boundary regression verifies
zero and one-element allocations. Budgeted observations now estimate actual
serialized JSON size after extraction, trim lower-priority metadata and page
content until the estimate fits, and report `budget`, `budget_used`, and
`budget_truncated`. Fixed target identity and omission counts remain present if
they alone exceed a tiny budget. Focused unit coverage verifies serialized
accounting, trimming priority, convergence, and the irreducible tiny-budget
overage case; `budget_used` includes its own serialized field. The budget
estimates the observation object; the separate MCP response limit covers
protocol framing. `budget_truncated` distinguishes budget-driven omissions
from the observation's normal configured caps. The live Chrome budget
regression passes.
The live MCP output-limit runner was rerun on Go 1.27.4 after the dependency
upgrade. It verified the serialized 256 KiB ceiling on bounded extraction,
oversized success replacement, compacted batch recovery evidence, private
snapshot paging/search, and network-body paging; no response exceeded the cap.
The raw contract predicates and scope limits are captured in
`scout-mcp-output-budget-live-2026-11-12.json` (local audit artifact).
The runner is now part of the browser integration workflow, so the same live
wire-level overflow and retrieval contract will gate CI on Linux and macOS.
The WebMCP page-tool catalog now has a separate 192 KiB serialized-content
budget in addition to the per-tool and tool-count limits. When page-provided
metadata is omitted, the response exposes `truncated` and `omitted_tools` so
the caller can distinguish a partial catalog from an empty complete catalog.
Focused tests cover a complete catalog, aggregate overflow, and an individually
oversized tool. Catalog coverage is tracked separately: direct site-isolated
iframes are queried through the parent page's `getTools({fromOrigins})` API, so
only child tools explicitly exposed to that parent enter the catalog. Scout then
maps those names to the child frame's CDP tool events. Invocation attaches to
the matching child target only after root-catalog validation and verifies the
declared postcondition inside that frame, returning a frame-bound action
receipt. A Chrome fixture confirms the exposed tool can be invoked and an
unexposed sibling tool remains absent. If exposure
cannot be checked or target setup fails, the response sets `complete=false`
with a coverage warning. The MCP tool description directs clients to both
coverage and output-budget metadata. Page URLs are
also bounded at 8 KiB with an explicit `url_truncated` flag, so an unusually
long URL cannot consume the entire result.

A three-run tail-evidence comparison now uses the real MCP interfaces with a
65-record page and a unique marker in the final record. All 9 probes passed.
Scout returned the initial 8 records with `truncated: true` and accurate
total/limit metadata; the harness followed the offset to the final record and
recovered the marker in all 3 runs. Agent-browser 0.42.0 and Playwright MCP
0.0.85 also recovered the marker. The original probe used different read
methods, so its byte counts were not directly comparable. Raw protocol
responses and tool schemas are retained under
`output-evidence-20261112T064746Z` (local audit artifact),
with the compact summary at
`output-evidence-20261112T064746Z.json` (local audit artifact).
The revised probe queries the same `article.innerText` windows (offsets 0 and
64, page size 8) from all three products. In 3 runs per product, both windows
in all 9 product runs exactly matched the fixture oracle and recovered the tail
marker, and none returned a synthetic private sentinel stored only in an
article attribute. Normalized text was 26,137 bytes for each first window and
48 bytes for each tail window. Serialized MCP results for the first window
were 27,384 bytes for Scout, 52,917 for agent-browser, and 26,505 for
Playwright MCP; those totals include product-specific metadata and wrappers.
The three-run raw captures and summary are in
`output-evidence-20261112T065719Z` (local audit artifact)
and `output-evidence-20261112T065719Z.json` (local audit artifact).
Reproduce with `node examples/benchmark/run_output_evidence_comparison.mjs 3`.
This is one synthetic extraction shape, not a privacy certification or a broad
budget curve. Other selectors and output surfaces (snapshots, screenshots,
accessibility trees, logs) remain unmeasured; byte totals are descriptive, not
a product quality ranking.

The `extract_all` response now separates pagination from clipped item text:
`has_more` and `next_offset` provide the continuation contract, while
`items_truncated` aggregates the existing per-item `item_truncated` flags. The
legacy `truncated` field remains true for either condition. A live MCP capture
verified the first page returned `has_more=true`, `next_offset=8`, and
`items_truncated=false`; the final one-item page returned `has_more=false`,
`next_offset=65`, and `items_truncated=false`. The captured response is in
`output-evidence-20261112T070143Z` (local audit artifact)
and `output-evidence-20261112T070143Z.json` (local audit artifact).
README and MCP gotchas now show callers how to use the fields and note that
pagination reads the live DOM, so callers should restart if the page changes.
The stateful CLI now exposes the same bounded `extract_all` operation with
`selector`, `offset`, `limit`, and optional `include_handles`; its page size is
also capped at 8 to keep extraction behavior aligned with MCP. JSONL decoding
and missing-selector validation have focused regression coverage. A tagged
headless Chrome integration test extracts 8 of 11 articles, verifies the
continuation metadata and returned handles, then reads the final three and
confirms only the oversized item's text is marked clipped. Its
`TestIntegration` name includes it in the integration workflow's focused Go
test command. Reproduce with
`go test -tags=integration ./cmd/scout -run '^TestIntegrationSessionJSONLExtractAllPaginationOnLivePage$' -count=1`.
The comparison runner now launches each MCP server and the fixture in an
isolated process group, escalates from `SIGTERM` to `SIGKILL` with bounded
shutdown waits, and removes temporary binaries even when fixture cleanup
fails. A fresh one-run-per-product check completed on 12 November; all three
recovered the tail marker and the runner left no matching processes or
temporary build directories. Its raw capture and summary are
`output-evidence-20261112T065140Z` (local audit artifact)
and `output-evidence-20261112T065140Z.json` (local audit artifact).
This validates runner cleanup and the original retrieval behavior. The later
matched-window probe above provides separate, bounded evidence for the
synthetic attribute sentinel and exact selected text windows.
The one-shot `navigate --expected-*` CLI now exits nonzero on a rejected page
predicate while retaining the observed result on stdout and a typed failure
envelope on stderr with `--json-errors`; a live loopback run confirms the
process exit behavior.
The matched origin request-blocking fixture now covers Scout, agent-browser
0.42.0, and Playwright MCP 0.0.85. In five restricted runs per product, all
reached the fixture and the independent denied-origin counter remained zero;
one unrestricted positive control per product reached the fixture and produced
exactly one request. Raw MCP/CLI outputs and per-run records are retained in
`origin-policy-matched-20261112T062042Z.json` (local audit artifact).
This compares only an ordinary fetch. Native policy scopes differ, and the
fixture does not cover redirects, service workers, WebSockets, EventSource,
WebRTC, DNS rebinding, or remote-host egress. Malformed/empty policy behavior
against competitors, cross-product output paths, and unknown/deprecated-field
behavior remain outstanding. Existing Scout checks for those cases are
implementation evidence, not cross-product comparisons. The optional
mitmproxy allowlist now preserves explicit port `0` instead of treating it as
the scheme default; a unit regression verifies that `:0` and `:443` remain
distinct authorities.

Fresh issue research found agent-browser issue
[#2574](https://github.com/vercel-labs/agent-browser/issues/2574), an open
report that `--allowed-domains` can stall on cross-origin iframe targets when
automatic network controls time out in `Runtime.evaluate`. On 12 November, the
Scout's local fixture now mirrors the report's `localhost` parent and
`127.0.0.1` child: the denied destination receives zero requests and the parent
remains readable after the block. The separate site-isolated routing/action
fixture also passes. This is local Scout coverage, not a
matched failure-rate comparison or a reproduction of the reported Stripe
Elements case. Add the same cross-origin iframe shape to the pinned
cross-product policy suite and verify the page remains usable after a block.

The Go recovery receipt and handoff decoders now recursively reject duplicate
JSON object keys, matching the independent Python verifier. Previously Go's
struct decoder silently kept the later duplicate value, so the validators did
not enforce the same malformed-input contract. Regressions cover nested
duplicates and each public receipt, handoff, and observation decoder. This
closes the local parser-parity gap; malformed and deprecated inputs remain
unmatched across pinned competitors.

Principal-map startup now decodes the complete JSON document with unknown
field rejection. A misspelled `isolate_browser_context` or policy key fails
closed instead of being silently ignored, and trailing JSON values are
rejected. Focused regressions cover both cases. This closes the Scout-side
configuration behavior only; malformed-policy parity against pinned
competitors remains outstanding.

The shared-endpoint isolation guard now canonicalizes host case, trailing DNS
dots, default ports, and trailing path slashes before comparing principal CDP
URLs. A policy regression verifies that explicit `:443`, host-case, and slash
aliases cannot avoid the isolation requirement. DNS aliases and reverse-proxy
routes still need deployment owners to mark every principal reaching the same
Chrome with `isolate_browser_context`.

The open agent-browser report [#1902](https://github.com/vercel-labs/agent-browser/issues/1902)
describes an explicitly empty allowlist becoming unrestricted. A local fixture
reproduces the same behavior on pinned `agent-browser@0.42.0`: unset and valid
allowlist controls behave as expected, while blank and comma-only
`AGENT_BROWSER_ALLOWED_DOMAINS` values both reach the denied-origin listener
once. Scout's corresponding configuration probe rejects blank, whitespace,
and comma-only values during `doctor` and MCP startup. Raw evidence and the
repeatable runner are
`agent-browser-empty-policy-2026-11-12.json` (local audit artifact)
and [`run_agent_browser_empty_policy.py`](../examples/benchmark/run_agent_browser_empty_policy.py).
This is one local ordinary-fetch reproduction, not an upstream prevalence or
security-boundary claim; equivalent malformed-configuration probes for other
pinned competitors remain outstanding.

A fresh matched rerun on 12 November records source revision and platform,
then checks the same local listener for agent-browser 0.42.0 and Scout. The
agent-browser unset control is unrestricted, its valid `localhost` policy
blocks the denied request, and configured blank and comma-only values also
reach the denied listener once. Scout's unset policy reaches it once; explicit
empty and trailing-empty `--allowed-origins` values exit with a validation
error before the browser starts; the valid exact-origin policy reaches the
fixture while the denied listener remains at zero. All five report predicates
pass. See
`agent-browser-empty-policy-2026-11-12-matched.json` (local audit artifact).
This compares Scout's CLI syntax validation and agent-browser's browser-level
host allowlist as separate contracts. It is a single local run, not an
exploit-prevalence estimate or proof of remote egress enforcement.

A fresh one-run matched capture on 12 November again passed the unrestricted
positive controls and denied-origin check for Scout, agent-browser 0.42.0, and
Playwright MCP 0.0.85. The run confirmed the benchmark's current predicates,
not a broader enforcement claim. Auditing its cleanup found that the runner
left its temporary build directory behind; it now waits for fixture shutdown
and removes the generated binaries on both success and failure. The rerun
created no new temp directory. Raw evidence is
`origin-policy-matched-20261112T074954Z.json` (local audit artifact).

An additional matched capture on 12 November passed one unrestricted control
and one restricted request-blocking run for each product. Scout's CLI,
agent-browser 0.42.0, and Playwright MCP 0.0.85 each reached the allowed
fixture; the independent denied-origin listener recorded zero hits in every
restricted run. The browser was HeadlessChrome 157. Raw output is
`origin-policy-matched-20261112T092420Z.json` (local audit artifact).
This confirms the runner's existing ordinary-fetch predicate on one local
browser build. It does not rank the products or establish redirect, worker,
WebSocket, WebRTC, DNS-rebinding, or remote-network enforcement parity.

A later matched run added empty and malformed allowlist variants to the pinned
Playwright MCP 0.0.85 probe. An empty CLI argument and empty environment value
both allowed the denied request once. Semicolon-only and whitespace environment
values instead blocked the allowed fixture page before its fetch; the denied
counter stayed at zero. The run's ordinary policy controls passed for Scout,
agent-browser, and Playwright MCP. Playwright's documentation describes
allow-all as its default, so the empty-value result is recorded as behavior
rather than a bug claim. The capture is
`origin-policy-matched-20261112T090236Z.json` (local audit artifact).
These are one-run local results; config-file parsing and other browser paths
remain untested.

The Chrome-backed `TestIntegrationAllowedOriginsCrossOriginIframes` also
confirms one allowed and one denied cross-origin iframe, followed by a usable
parent-page read. Ten local repetitions pass on this Chrome setup; they do not
establish behavior through a remote browser's network boundary. The OOPIF
attach path also now has a protocol fault fixture for a child `Target.attachToTarget`
response delayed past the caller deadline: Scout returns at that deadline,
observes the response for a bounded 250 ms, detaches the late session, and
retries a retained child-session detach during parent cleanup after an injected
failure. This is narrower than the competitor's
auto-attach/navigation failure report; cross-browser policy conformance and
remote-network boundary evidence remain open.

**Acceptance:** raw inputs and outputs, effective policy, omitted-evidence
counts, and independent verdict are retained for every repetition. See the
[11 November competitive refresh](competitive-opportunities.md#competitive-refresh-fail-closed-policy-and-fresh-evidence-under-limits-11-november-2026).

The local first-success doctor now reports launch/attach, status, bounded
active-page read, application probe, and cleanup as separate timed checks, with
explicit skipped states. A Chrome-backed run passed all four doctor scenarios
and both first-read postcondition controls; its workstation report is
`scout-doctor-readiness-2026-11-11.json` (local audit artifact).
This is diagnostic implementation evidence only. The clean-container,
participant, and pinned-competitor comparison remains open.

### P1 — Network observer continuity across navigation and tab changes

GitHub issue [#42](https://github.com/klarlabs-studio/scout/issues/42) is
closed as completed. Its reported case enabled capture before navigation to a
localhost SPA and then observed zero requests. The observer lifecycle now binds to the current
page before navigation, retains explicit and passive intent across failed
attachments, and reports detached observers as unavailable in live summaries
and trace manifests. Local Chrome 157 reproductions recorded the initial
document and SPA `/api` request on the new target, and requests from two
sequentially opened tabs under their own target IDs. A switch-away/switch-back
run now captures the selected tab's post-switch `/api` request under the right
target. Observers remain attached to each live tab, and pending records use
target-scoped request IDs. Reattachment without an active page now fails
explicitly, preserves passive/explicit observer intent, and clears stale
page-scoped pending requests. A failed attach now retains unsubscribe callbacks
for observers still attached to older tabs, so later disable/close can clean
them up; a unit regression covers this partial-attach state. Injected CDP attach
failure now has both a focused state regression and a Chrome-backed fixture:
the fixture records the initial document and SPA API request, injects one
`Network.enable` failure during tab creation, then confirms observers recover
while summary coverage remains `known_gaps`. This proves the state transition
for this local scenario, not a reliability estimate. Each request now retains
whether it began under explicit capture or passive diagnostics: an in-flight
passive success cannot appear as an explicitly captured request after capture
is enabled, and a request begun during explicit capture is not added to history
after capture is disabled. Focused regressions cover both transitions.
An additional code audit found that Chromium reuses a request ID across
redirect hops and reports the completed response on the next
`requestWillBeSent`; the observer now finalizes that prior URL/status/header
record before registering the redirected request. A dedicated redirect-chain
regression now feeds the exact event order and proves each hop retains its own
URL, status, MIME type, and response headers, including when only the
destination URL matches the capture filter. The focused regression and the
Chrome-backed navigation fixture now records both redirect hops with their
separate status and destination headers. The focused regression and live Chrome
redirect/capture checks pass. Redirect-chain coverage is now closed for the
tested Chrome build; other Chromium versions remain covered by CI rather than
a local compatibility claim.

**Acceptance:** repeat the issue's order (configure, enable capture, navigate,
wait, inspect summary and requests) against the reported localhost SPA shape;
assert known document/API request IDs, correct target IDs, and honest coverage
when an observer attach is forced to fail. Exercise the same lifecycle through
open/switch tab, ordinary navigation, and a redirect chain. The closed issue's exact external
environment is not independently available; retain these local fixtures as
regression coverage and avoid describing the issue as open.

### P1 — Page initialization cleanup reporting

When first-page setup fails during stealth, URL-policy, target-binding, or tab
frame-route preparation, cleanup now uses a bounded context and joins detach or
close failures with the original setup error. This makes a potentially
orphaned page visible to Go callers while preserving `errors.Is` and
`errors.As` for the primary failure. Direct target attachment joins browser
close errors after unsupported or failed attachment, and joins bounded page
detach plus browser-close errors when URL policy setup fails. Temporary
site-isolated iframe targets join bounded detach errors on failed setup and
retain failed handles for retry. Route restoration also retains failed detach
handles, retries them later, and includes outstanding handles in session close.
Close deduplicates frame handles across active routes and orphans, while reset
drops stale handles only after the old browser connection closes successfully.
Successful reattachment reports failure to detach the superseded target after
committing the new route state.

Attaching an existing page now also preserves a failed detach after page-domain
or URL-policy initialization fails. A focused fixture forces both `Fetch.enable`
and `Target.detachFromTarget` to fail and asserts both errors remain visible.
This verifies CDP error reporting but does not exercise a real Chrome detach
failure.

Deterministic CDP regressions now reject target attachment followed by
created-target close, URL-policy installation followed by detach of a
caller-owned remote page, and OOPIF `Page.enable` followed by detach of its
attached frame session. Frame-attach errors now retain the failed session
handle in a typed error; Scout queues it for retry during route restoration or
session close. The CDP fixture rejects the first detach and accepts a retry,
and an agent unit regression checks retained-handle deduplication. A
route-restoration fixture also forces the first detach to fail, confirms the
active route is cleared while its handle is retained, then confirms a later
restore retries successfully and removes the handle. Each fixture asserts that
setup and cleanup failures remain visible together; the remote fixture also
asserts cleanup never sends `Target.closeTarget` for the caller's page. The
selected and nested OOPIF owner-replacement tests also exposed Chrome returning
`No session with given id` when Scout detached a session Chrome had already
removed. Cleanup now treats that response as an already-completed detach while
retaining other detach errors; both live regressions pass. The full agent
integration suite passes after this change.
focused regressions, relevant agent page/target checks, and `git diff --check`
pass. A Chrome-backed CDP-proxy regression now injects policy setup failure and
reports a detach error after forwarding detach to the live browser; both errors
remain visible, no target-close command is sent, and the caller page remains
present. This verifies Scout's reporting and ownership behavior with a live
target, but not Chrome refusing the detach command itself.

### P1 — First-success diagnostic matrix

Compare diagnostics with the first real bounded browser read on clean pinned
environments. Include missing browser, unreachable endpoint, launch/attach
failure, responsive browser with unresponsive page, missing application marker,
and successful probe. Separate static checks, live browser reachability, and
application verification; keep repair actions opt-in.

**Acceptance:** phase timing, stable failure category, first-command result,
cleanup, and local state changes are captured for every case. A single
workstation smoke is not a diagnosis-accuracy or onboarding result.

Focused CLI doctor tests now assert that malformed origin credentials are
redacted from its JSON report and that an application probe cannot run without
explicit `--live`; both checks retain stable codes and remediation. Live doctor
now requires one bounded active-page observation in addition to session status,
uses a separate bounded cleanup deadline, and reports requested probes as
`not_checked` when launch or attach fails. Fault regressions cover missing
browser discovery, unreachable remote endpoint, attached session with a timed
out page read, cleanup after that read failure, and successful plus missing
application markers on local Chrome. A CDP integration fixture verifies a real
browser endpoint remains reachable while its page renderer is blocked, and that
doctor rejects the bounded page read while closing its remote connection
cleanly. The Chrome application-probe fixture verifies its marker result and
reports tab cleanup; it does not independently inspect the target list. The
full Go suite and focused browser fault case pass. This matrix establishes local
classification and cleanup-path behavior, not clean-environment onboarding,
cross-product diagnosis accuracy, or a reliability estimate.
The CLI `navigate` result now includes `execution_timing` with browser
startup, page navigation/verification, and browser cleanup durations while
preserving the existing page-result fields. Cleanup errors now affect the
command exit status instead of being silently discarded; a joined cleanup
failure preserves the page postcondition classification. Focused tests cover
the result shape and error classification, and the readiness runner requires
phase timings for both positive and negative first-read controls. A fresh
workstation run passed with positive first-read phases of 293/109/69 ms
(startup/navigation+verification/cleanup) and missing-marker phases of
320/111/76 ms. The updated raw run is retained at
`scout-doctor-readiness-2026-11-11.json` (local audit artifact).
The integration workflow now runs the same source-built readiness matrix on
Linux and macOS, checks configuration-only, live browser,
successful/missing application marker, positive/negative first-read exit and
classification, and first-read phase timing. The runner also executes and
records five deterministic fault regressions: missing browser, unreachable
CDP attach, a timed-out active-page read, timeout cleanup, and failed
application-probe tab cleanup. The report lists the expected
status/code/remediation contracts beside each Go test result; the tests assert
the relevant values against actual doctor output. The runner records test
pass/fail and the Go tests validate these claims, but the runner does not parse
the Go test process to independently extract each in-memory report value. Live
rejected-tab-close behavior remains a separate Chrome/CDP integration fixture,
now explicitly selected by CI because its test name does not match the broad
`TestIntegration` selector. The runner does not compare diagnosis against
another tool. The validated report is uploaded
as a per-OS, 14-day CI artifact alongside the browser egress destination-oracle
report.
The pinned agent-browser 0.42.0 first-success comparison asserts process
status alongside observed predicates and phase-timing fields. On the corrected
12 November rerun, the verified Scout read exited 0, the missing-marker read
exited 1, and all sixteen runner predicates passed. Agent-browser positive
and negative controls now use separate HOME/XDG trees and explicit browser
user-data directories; npm's package cache remains shared. The raw report is
the 12 November first-success raw summary (local audit artifact).
Agent-browser's corresponding commands exited 0 for both marker cases; the
harness observed the absent text and recorded that its command has no
postcondition failure contract. Scout's one-shot command includes startup and
cleanup, while agent-browser uses separate `open`, `get text`, and `close`
commands, so these timings do not support a speed ranking. The earlier raw
records remain available at
the 12 November first-success raw summary (local audit artifact) and
the 11 November first-success raw summary (local audit artifact).
Agent-browser's `get text body` exposes observed text for the harness to grade,
so this remains an interface check, not an equivalent built-in postcondition
comparison.

Machine-readable doctor remediation should tell the operator what to do next
for each stable failure code. Missing local Chrome now returns
`install_chrome_or_chromium_or_set_remote_cdp`; keep the human detail and JSON
hint aligned, and add assertions whenever a new failure mode or browser
provider is introduced. The existing missing-browser fault test covers this
contract. A documentation audit found the README still described doctor JSON
schema version 2 and used the former missing-browser remediation code; both
were corrected to match the current CLI contract. The remediation reference
now includes the probe, browser-session, and proxy hints emitted by the current
implementation. The audit also found that attach, status, unresponsive-page,
cleanup, and invalid remote-endpoint failures fell through to the generic
`inspect_check_detail` hint. These now return distinct operator actions while
keeping the failure codes and observed details separate.

A stub regression injects failure while restoring the original target and
closing the temporary application-probe tab. A live Chrome/CDP proxy test now
rejects the probe tab's `Target.closeTarget` command, then checks the browser's
target list through an independent CDP connection. The doctor reports
`SCOUT_DOCTOR_APP_PROBE_CLEANUP_FAILED` with
`inspect_browser_tabs_and_restore_original_session`; the orphan target remains
visible, while the separate Scout session disconnect reports success. The
focused doctor suite and the live fault case pass. This verifies diagnosis and
observable cleanup failure for one Chrome/CDP injection; it does not retry
orphan removal automatically or establish equivalent behavior across browser
providers.

An additional unreachable-CDP run pairs Scout's seeded failure behavior with
the pinned agent-browser 0.42.0 ordinary doctor result. Scout returned
`SCOUT_DOCTOR_BROWSER_LIVE_ERROR`, its first read surfaced connection refusal,
and the live doctor did not create the configured task-store file.
Agent-browser's doctor passed its local launch test and wrote one small
diagnostic target marker in its isolated home. Since agent-browser doctor has
no endpoint injection in this harness, this is not a matched verdict under
failure. Stagehand's corresponding check remains merged-source-only until a
released CLI can be run. See the raw run (local audit artifact)
and the [competitive research update](competitive-opportunities.md#competitive-refresh-stagehand-turns-a-false-all-clear-into-a-regression-case-12-november-2026).

### P1 — Tab recovery when a renderer stops responding

Switching tabs now probes renderer responsiveness with a short deadline and
uses browser-level target activation to recover a discarded page before
committing Scout's active-tab state. Closing a background tab attempts
successor recovery under a separate bounded context and preserves successful
close semantics if the successor cannot be read. If browser activation
succeeds but page responsiveness remains unknown, Scout records the selected
target and returns an unknown switch outcome. Activation is surfaced in action
evidence when a follow-up probe succeeds. Both activation and renderer probes
now have their own deadlines, even when a caller supplies a context without
one, so recovery cannot hold the session lock indefinitely. CDP also marks a
timed-out `Target.activateTarget` as an unknown dispatch; Scout retains the
requested tab as its logical target and reports uncertain outcome instead of
labeling activation a definite failure or claiming successful recovery.

**Acceptance:** use a deterministic CDP fixture for discarded renderers and
JavaScript-dialog-paused pages. Cover switch and close, verify whether the
target was reloaded or focus changed, ensure failed switches preserve prior
refs/frame state, ensure committed closes stay successful, and prove the next
queued command completes. Unit tests cover bounded probe decisions, activation,
confirmation, and activation failure. A Chrome 157 integration now opens a
JavaScript alert on an inactive target, verifies switch returns an unknown
outcome with the requested target identity, dismisses the alert, and proves the
next switch and page read succeed. It also closes a second dialog-paused target,
checks the target is absent through browser-level CDP, and proves the next
switch and read work. The existing closed-target fixture verifies fast failure
and queue recovery. A Chrome 157 discard fixture now forces urgent discard on an
inactive, detached tab, observes Chromium replace its target ID, and verifies
Scout activates and reattaches to the unique replacement before confirming the
page title. Replacement matching excludes targets already assigned to another
Scout tab and fails closed when multiple unassigned targets share the cached
URL and title; the fixture verifies the active page is unchanged on that
ambiguous result, then succeeds after the duplicate is removed. These fixtures
verify successful recovery on this Chrome build. A focused Chrome regression
also selects an iframe, attempts to switch to a missing tab, and verifies the
selected frame ID, URL, and in-frame read remain unchanged. This covers a
pre-activation rejection. A deterministic WebSocket CDP fault fixture now also
stalls a background renderer, rejects `Target.activateTarget`, and verifies the
previously selected iframe URL and title remain readable after the failure.
The fixture closes the stalled target after the assertion because tab listing
now bounds each renderer metadata read and returns the tab's last-observed URL
and title with `metadata_stale: true` when that renderer does not respond.
Together these cover pre-activation and activation failures for the tested
Chrome build; they do not characterize every Chromium version or races during
concurrent target replacement. See the competitor evidence in the
[11 November refresh](competitive-opportunities.md#11-november-refresh-readiness-and-discarded-renderers).

### P1 — Buyer adoption study

Run the prepared paired task with 5–8 Go platform or QA engineers. Capture
participant-authored setup and integration code, task success, diagnosis of
seeded faults, time, help requested, confidence, and preference. Publish
exclusions and failures; do not substitute facilitator-authored adapters for
participant implementation evidence.

**Acceptance:** evaluate the predeclared decision gates in
[`adoption-study.md`](adoption-study.md) and report participant-level results.
Until then, demand and integration-cost advantage remain unknown.

The participant importer now requires a facilitator-attested boolean for
unacceptable Scout friction, and the analyzer reports the evaluated and
required counts. Missing friction evidence remains inconclusive instead of
being interpreted as a clean result. This closes an evidence-pipeline gap; no
participant data has been collected, so the adoption gate remains open. A
further audit found the comparative safety gate checked Scout's faulted arm
but not Stagehand Go's. The analyzer now applies the same completed-run
contract to both products: final verification, preserved `unknown`, an
authoritative ledger read, no retry, and exactly one dispatch/effect. A
completed unsafe comparator run therefore blocks a clean comparative result
too. Explicitly observed unsafe outcomes are surfaced even when an arm is
incomplete or its nominal control failed; missing safety fields alone remain
in the missing-evidence path. The importer now requires those faulted-run
fields before marking an arm complete, and the analyzer requires them in the
documented participant record shape. This prevents missing comparator data
from being mislabeled as a safety failure or counted as a complete pair.
The decision analyzer now has a small regression suite covering a fully
qualifying paired result, missing Scout friction evidence, an explicitly unsafe
Stagehand retry, and missing comparator safety data. All four branches pass;
the live evidence audit still finds zero participant reports, so no market or
adoption conclusion is available. Importer regressions now also cover participant
checkout containment, changed source content, path traversal, and mismatched
task IDs across product arms.

### P2 — Stateless MCP acceptance

Complete the opt-in stateless MCP prototype's owner isolation, durable task and
artifact scoping, error cleanup, and client compatibility matrix. One-shot
tool invocation is not by itself evidence that users need stateless transport.

A Chrome-backed HTTP integration now uses the MCP 2026-07-28 client discovery
flow. It confirms browser calls require an opaque per-call handle, the same
principal can reconnect with a stale `Mcp-Session-Id` header and continue using
its handle, and a different principal receives the same not-found response as
an unknown handle. The same integration also saves a private page snapshot,
confirms it remains readable after reconnect, denies another principal access,
and confirms explicit session close removes the artifact and revokes reads.
It now also reuses the identical caller task ID and plan in Alice's second
named session: that session executes on its own target without replay, while a
retry from the original session returns only its own cached result. This
verifies stateless task-run namespacing across two named sessions for one
principal.
The same integration gives Alice and Bob one durable task store, then confirms
they can independently run and inspect the same caller-selected task ID: Bob
cannot see Alice's record, and his task executes instead of replaying her
result. Stale transport IDs are ignored by both HTTP principal authorization
and owner-scope resolution in stateless mode; stateful mode still binds
transport IDs to principals. Same-principal task isolation is now verified
between default and named sessions in both stateless and stateful HTTP. A
stateful reconnect test also confirms durable task results are shared by
principal plus logical session name even though a new transport creates a
separate browser target; the replay is marked and does not dispatch actions
into that target. Clients should retain the same logical name to inspect/replay
outcomes, and choose a new name for independent task execution. The stateless
scenario also overlaps four `status` calls through two clients for Alice's
default and named sessions. The transport race test passed three consecutive
runs; every observed request carried a matching `Mcp-Method`, and no response
minted a transport session ID. A stale caller-supplied `Mcp-Session-Id` is
deliberately tolerated by Scout and cannot override the explicit handle. CI now
runs this browser-backed test independently of the broader `TestIntegration*`
selector. This original same-process capture has since been extended with a
graceful single-process restart check and official TypeScript/Python client
flows, including cancellation and a successful read afterward. Multi-replica
load balancing, OAuth, and additional client SDKs remain untested.

Snapshot read/search/delete calls now bind artifact authorization to the
selected or active session, matching the ownership key used when snapshots and
oversized results are saved. This addresses a stateful HTTP routing mismatch
found during the audit. Authenticated stateful HTTP regressions now demonstrate
that a second named session cannot read or delete the artifact, switching back
restores access, and reconnecting with the same captured `Mcp-Session-Id`
preserves read/delete access. Reconnecting with a fresh transport starts a
separate artifact namespace; clients must retain the transport session ID when
they need stateful artifact continuity.

The snapshot audit found that the advertised per-owner/process artifact caps
were enforced only for oversized tool responses; `save_page_snapshot` could
fill the registry outside those quotas. Snapshot and response artifacts now
share one count/byte budget, and delete releases quota atomically. Registered
artifact reads hold the registry lock through file access and use no-follow
opens on Unix to avoid a registry-check/delete or symlink-swap race. Focused
tests now cover combined quota boundaries, owner denial, quota release, digest
integrity, symlink rejection, and zero-byte quota cleanup. The Chrome-backed
stateless HTTP scenario now also verifies same-principal cross-session artifact
denial, reconnect access from the creating session, four successful snapshot
saves followed by quota rejection, deletion restoring quota for another save,
foreign-principal denial, and cleanup on session close. Host and Windows
builds plus focused tests pass. A separate authenticated stateful HTTP recovery
scenario now creates an artifact in the default session, denies access from a
second named session, restores access after switching back, and verifies delete
removes the file. This exercises switching through the actual MCP HTTP handlers;
an additional authenticated HTTP regression reconnects with the same captured
`Mcp-Session-Id`, reads the artifact, deletes it, and confirms the file is gone.
Stateful artifact continuity therefore depends on retaining the transport
session ID; reconnecting with a fresh transport starts a new artifact namespace.
Stateless clients address artifacts by the selected Scout session handle.

A separate Chrome-backed HTTP regression now exercises idle cleanup with a
300 ms timeout and fixture responses held open beyond that threshold. While
navigation is blocked, `list_sessions` reports the browser as started with an
in-flight call; navigation then completes successfully, and a later idle
listing reports the session stopped. A second held navigation is canceled by
the MCP client; the tool call returns and a later idle listing again reports
the stopped session. This covers graceful HTTP request cancellation in the
same process, but not server restart, load balancing, or external client
compatibility.

Independent compatibility smokes run the official TypeScript MCP client
2.4.0 and Python MCP SDK 2.3.0 against the opt-in stateless HTTP server. Both
negotiate 2026-07-28, list tools, call `list_sessions`, create a browser
session, navigate to a local fixture, and read its text using the opaque
per-call session handle; both also cancel an in-flight wait, read successfully
again through the same handle, and close the created session. They verify
`Mcp-Method` on each request and confirm no transport session ID is sent or
returned. CI runs both pinned v2 clients. The probe also found that the
older TypeScript SDK 1.32.2 does not emit the new routing header and cannot
initialize against stateless mode; stateful mode remains the compatibility
path for that client. This adds two independent client rows, not broad
compatibility proof: the Chrome-backed integration now restarts the stateless
server, confirms a pre-restart handle is rejected, then creates a fresh handle
and successfully navigates with it while reopening the same durable task store.
The focused race-enabled run passed. Both external SDK smokes also cancel an
in-flight wait, then verify that the same session can read the page again.
This defines single-process restart and same-process cancellation behavior;
multi-replica load balancing, OAuth and additional client SDKs remain
untested, so stateless mode stays opt-in.

### P2 — AG-UI session lifecycle

The AG-UI session manager now reserves per-thread startup slots and launches
browsers outside its global registry lock, so one slow Chrome startup does not
block unrelated thread lookups. Idle cleanup skips pending startups; shutdown
rejects new sessions, waits for pending startup and cleanup work, closes
sessions outside the registry lock, and returns cleanup errors. The server logs
shutdown failures. Idle cleanup now tracks in-flight closes and aggregates
their errors under the lifecycle lock before shutdown returns. Deterministic
regressions prove concurrent same-thread lookups share one startup, shutdown
waits for a pending startup, a delayed startup does not block an unrelated
thread, cleanup errors remain discoverable from `Close`, and new sessions are
rejected once shutdown begins. The focused lifecycle tests pass under the race
detector. A Chrome-backed integration now starts the manager's browser,
navigates to a local fixture, verifies the page title, closes the session, and
checks that post-shutdown creation is rejected. The successful cleanup path is
verified against local Chrome; the error aggregation branch is exercised with
an injected cleanup failure.

The AG-UI HTTP endpoint previously returned `Access-Control-Allow-Origin: *`
for every run request, while its session and run serialization keys were
caller-controlled thread IDs. The server now accepts same-origin browser
requests and non-browser requests without an `Origin` header; cross-origin
browser calls require an exact configured origin via `--allowed-origins` or
`SCOUT_UI_ALLOWED_ORIGINS`. Empty, wildcard, credentialed, path-bearing, and
otherwise non-origin values fail validation. Health checks do not receive CORS
grants. Tests cover denied POST/preflight requests, allowed origins, default
port normalization, malformed values, and launch configuration errors. The
full Go suite and race-enabled AG-UI/CLI packages pass. This constrains browser
cross-origin access; AG-UI still has no user authentication or logical-owner
identity, so deployments must place it behind an authenticated boundary when
callers are not equally trusted.
The backend binds only to `127.0.0.1`; the exact-origin setting is CORS, not
authentication. README guidance states that remote use requires an
authenticated proxy. No remote-listen flag is exposed while the endpoint has
no user identity model.

**Acceptance:** a delayed startup for one thread does not block an unrelated
lookup; concurrent same-thread lookups share one startup; close is idempotent,
rejects new sessions, waits for startups, and reports every browser cleanup
failure.

**Acceptance:** concurrent logical owners cannot read, mutate, or delete one
another's state; reconnect and abandoned-request behavior are explicit; run
protocol conformance against supported clients before changing defaults.

### P2 — Browser-wide egress evidence

Measure where page Fetch interception ends and whether the configured proxy is
actually used for service workers, WebSockets, redirects, iframes, new tabs,
direct sockets, and remote CDP routing. Scout records page-target policy
decisions, while proxy metadata alone cannot attest enforcement.

**Acceptance:** each tested path has an independent destination-side oracle and
an attributable allow/block record. Keep unsupported or unobserved channels
explicit; do not claim browser-wide enforcement from configuration alone.

The reference addon now emits a path-free startup status and origin count.
Invalid or empty origin policy, or an invalid private-IP opt-in, emits
`reason=invalid_policy` and aborts addon loading; it never installs a
permissive fallback or echoes the configured origin in the startup traceback.
Sixteen Python unit checks pass, including audit sink failure returning 503,
DNS answer validation, missing destinations, and resolution failures. The real
mitmproxy 12.3.1 startup runner verifies valid policy
listening and empty/malformed policy exit before listening; its report is
`scout-egress-proxy-startup-2026-11-11.json` (local audit artifact).
The Chrome 157 / mitmproxy 12.3.1 live fixture also passed its six denied-path
checks with 29 attributable proxy decisions and zero destination hits at each
denied destination; evidence
is `scout-egress-proxy-policy-live-2026-11-11.json` (local audit artifact).
The 12 November refresh adds a positive WSS control: the allowlisted WebSocket
handshake reached its independent destination once, while each of the six
denied destinations, including the denied WSS endpoint, remained at zero hits
with attributable denial decisions. The raw report contains 32 proxy decisions
and is
`scout-egress-proxy-policy-live-2026-11-12.json` (local audit artifact).
The integration workflow now reruns that Chrome-backed fixture as a focused
step, retains its structured report in the runner workspace, and independently
checks the allowed WebSocket hit, zero hits for each denied destination, and
at least one allowed and denied CONNECT decision. The step fails on a skipped
fixture so a missing browser or mitmproxy cannot count as enforcement evidence.
The checked report is uploaded as a per-OS, 14-day CI artifact.
The proxy now resolves upstream names before connecting, rejects any non-public
answer by default, and pins a vetted public answer to prevent a second lookup.
A Chrome fixture allowlisting `allowed.localhost` verified a
`private_peer_denied` decision and zero destination hits. This closes the DNS
rebinding case only for traffic reaching the reference proxy. Locally launched
Chromium now disables non-proxied WebRTC UDP when a proxy is configured; this
uses Chromium's `disable_non_proxied_udp` behavior (UDP SOCKS proxying or TCP
fallback). The Chrome 157 destination-side integration fixture sends an ICE
gathering request toward an independent local UDP sink and observed zero
datagrams with the policy active. It caught and corrected use of Chromium's
weaker `--force-webrtc-ip-handling-policy` preference switch, which did not
enforce the restriction in the live fixture. In the same run, an unavailable
HTTP proxy caused navigation to fail and the independent destination server
received zero requests. A TCP TURN fixture was added with a direct Chrome
positive control: the control reached the independent TCP listener, while the
proxy-configured Chrome produced zero TCP destination hits. This provides
evidence for the tested TCP TURN setup; it does not cover other WebRTC servers,
browser versions, arbitrary browser socket APIs, custom browser wrappers, or
remote-CDP routing, which still need separate live destination-side
experiments and network-level controls. Locally launched
Chromium also maps host lookups to `NOTFOUND` except for the configured proxy
host. A Chrome-backed fixture loaded an `.invalid` destination through a local
proxy and confirmed the proxy received the original hostname, so the browser
does not need local destination resolution for the proxied request. The proxy's
own hostname still uses the host resolver.

The focused Chrome 157 test `TestIntegrationProxyLaunchBlocksDirectEgressOnProxyFailure`
was rerun locally on 12 November: it passed with zero HTTP destination hits,
zero direct STUN UDP datagrams, and zero direct TCP TURN connections while the
configured proxy was unavailable. Before the TCP assertion, an unproxied Chrome
positive control reached the independent TCP listener; the proxied case then
produced zero hits. This validates those locally launched Chromium paths for
the tested build. The reference-proxy test separately exercises traffic routed
through mitmproxy; the focused egress-proxy test could not be rerun here because
`mitmdump` is unavailable. Remote CDP still requires host-side proxy and
firewall configuration and independent destination-side verification.

The 12 November Chrome 157 / mitmproxy 12.3.1 live rerun exercised the proxy's
actual upstream address pinning and passed all six denied destination checks,
the allowed WSS positive control, and the loopback peer denial. The allowed
TLS fixture now listens on dual-stack loopback; the first run exposed that
pinning a resolver's `::1` answer cannot reach an IPv4-only fixture. The
dual-stack fixture now verifies successful connection to the pinned IPv6
answer without masking the address selected by policy. Its report is
`scout-egress-proxy-policy-live-2026-11-12-ipv6-pin.json` (local audit artifact).

The proxy addon's DNS failure, empty-answer, and malformed-address denial paths
now require a successful audit write before returning their policy result. A
regression test forces the audit sink to fail during DNS denial and verifies
that the connection still fails closed with an audit-specific error; malformed
resolver output is separately denied and attributed. The focused Python suite
passes 18 checks. The root README was also corrected to match the addon's
current resolve-and-pin behavior and to scope that guarantee to connections
that traverse the proxy. The private-IP development opt-in now changes only
whether private answers are allowed; the selected answer is pinned in both
modes, so a second DNS lookup cannot change the peer after policy evaluation.
Regression coverage verifies a loopback answer remains allowed only with the
opt-in and that the actual address plus hostname SNI are pinned. The proxy
suite now passes 18 checks.

`SCOUT_PROXY_SERVER` now routes locally launched browsers through the configured
HTTP(S) or SOCKS5 proxy across one-shot and stateful CLI, MCP, and chat UI
sessions. It does not infer an allowlist from `AllowedOrigins`, and `doctor`
labels proxy enforcement unverified. Remote CDP still requires host-side
configuration; the remaining acceptance work is to prove routing and policy
decisions across bypass channels with independent destination oracles.

An audit found `WithChromeArgs` was appended after Scout's proxy controls, so
duplicate flags could silently replace proxy routing, resolver restrictions,
or the WebRTC non-proxied UDP policy. Local launch now rejects conflicting
proxy, PAC, bypass, resolver, and WebRTC flags before browser discovery. Unit
regressions verify both rejection and harmless browser tuning flags. This
closes a local configuration override path; arbitrary Chrome code/extension
behavior and remote-browser host policy remain separate controls.

Runtime `status` and `list_sessions` now report `proxy_configured`,
`proxy_applied_by_scout`, and `remote_host_policy`. For remote CDP they identify
the proxy as not applied by Scout and remote enforcement as unverified; MCP
tool descriptions direct operators to configure and verify egress on the
browser host. A focused agent status regression and the MCP output-schema
generation test pass. This makes the deployment boundary visible at runtime,
but does not prove remote routing or close the destination-oracle acceptance
for remote CDP or browser bypass channels.

Principal-map documentation now clarifies that `proxy_server` applies to that
principal's locally launched named sessions; its default `remote_cdp` session
still uses the remote host's network policy. Per-session status fields show
which case is active.

### P2 — Evidence and observability utility

Measure whether action receipts, trace correlation, and bounded evidence help
operators diagnose the delayed-mutation and wrong-target fixtures. Capture
privacy leakage, output cost, trace overhead, and time to identify the failure
against a pinned peer baseline.

The `url-policy.json` trace artifact now distinguishes the policy verdict from
the CDP disposition result: each request records `resolution=resolved` or
`resolution=failed`, with a redacted `resolution_error` on failure. Trace export
removes URL credentials, query strings, and fragments from policy URLs, reasons,
and resolution errors. The versioned contract is documented in
[`scout-url-policy-trace-v1.schema.json`](schemas/scout-url-policy-trace-v1.schema.json)
with a valid example; a trace regression checks failure evidence and URL
redaction. This makes request-level evidence more honest and machine-readable,
but does not itself establish browser-wide policy coverage, comparative output
quality, privacy completeness, or faster diagnosis.

**Acceptance:** publish raw runs and distinguish evidence completeness,
diagnosis time, output size, and overhead. Do not collapse them into one score.

**Fresh evidence (12 November 2026):** a matched 10-run Scout MCP / Playwright
MCP 0.0.85 late-click probe now retains each adapter's serialized tool results
and scans a hidden fixture sentinel. Both reached the final page predicate in
10/10 runs and neither serialized result set leaked the sentinel. Scout retained
the typed unknown outcome, expected postcondition, and linked read-only recovery
receipt in 10/10; Playwright retained its in-flight call and inspected after
completion. The serialized result totals (96,479 vs 7,850 bytes across 30
responses) are descriptive only: Scout's rows serialize normalized Go client
results while Playwright's are raw JSON-RPC result objects. The probe does not
measure diagnosis time or trace overhead. See the [fresh evidence report](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/report.json)
and its [Scout repetitions](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/scout-repetitions.json),
[Playwright repetitions](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/playwright-mcp-repetitions.json),
and [Playwright repetitions](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/playwright-mcp-repetitions.json).

**Current evidence (12 November 2026):** the existing
`late-recovery-mcp-head-to-head-10x-2026-11-06.json` (local audit artifact)
supports a narrower receipt-parity claim: Scout returned a typed unknown
outcome before delayed completion in 10/10 runs, then reconciled read-only on
the same target with a linked recovery receipt and no replay in 10/10. The
agent-browser and Playwright MCP adapters reached the same page predicate in
10/10 runs using caller-side waiting and inspection, without an equivalent
native receipt contract in this probe. Timing and output bytes are explicitly
not comparable, and this fixture does not measure diagnosis time, privacy
leakage, trace overhead, or production reliability. Keep those acceptance
measures open; the next probe should capture normalized serialized tool output
and receipt completeness from both arms, then use blinded operators to record
diagnosis time and any sensitive values exposed.

The historical archive audit is now reproducible with
`python3 examples/benchmark/audit_late_recovery_evidence.py`; its report is
[`late-recovery-evidence-archive-audit-2026-11-12.json`](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/report.json).
It rechecks the 10/10 outcomes and Scout receipt-link summary, agent-browser's
retained CLI output, and Playwright MCP's four retained MCP results. It also
confirms why the comparison is still incomplete: Scout's archived run contains
summary rows rather than serialized MCP results, the adapters retain different
output forms, and the historical runs did not inject a privacy sentinel. The
audit therefore leaves cross-product wire-byte, normalized receipt, and
privacy-leakage comparisons unverified; it does not convert historical absence
of a sentinel into a privacy claim.

Network capture now redacts `Authorization`, `Proxy-Authorization`, `Cookie`,
`Set-Cookie`, and common API/auth/access/refresh/CSRF token headers when CDP
records request or response metadata. The same rule applies to explicit
captures and passive failure diagnostics, so Go, CLI, and MCP consumers receive
redacted header values from the shared capture model. Header matching handles
hyphenated and camel-case credential names, including acronym forms such as
`XAPIKey`; extraction-level regressions check those values are redacted while
preserving ordinary `Content-Type` metadata. URLs, request bodies, and response
bodies remain bounded but unredacted because they can be application evidence; callers must
still treat them as sensitive untrusted page content. This is a targeted
credential-header safeguard, not a privacy certification or a guarantee that
all application-specific secret headers are recognized.

## Implemented features to maintain

The 12 November integration audit also corrected several existing browser
regressions: navigation now reuses its active target while refreshing the URL
validator before each top-level request (preserving browser history and
origin-scoped storage); history actions wait for the selected URL instead of
accepting a stale `about:blank` ready state; text selectors bypass CSS polling;
empty saved localStorage restores as an empty object; option results retain the
selected label; and screenshot budgets can downscale below quarter scale. The
agent integration suite passes with these changes (`go test -tags=integration
./agent -count=1`, 129 seconds), as do `go test ./...` and `go vet ./...`. The
serial full integration command (`go test -tags=integration ./... -p 1 -count=1`)
also passes across root, agent, MCP/CLI, examples, and internal packages. A
parallel full run timed out in the MCP recovery case while browser-heavy
packages competed for resources; use the serial command as the reliable local
acceptance run. OOPIF support limits remain as described above.

Same-site cross-origin in-process iframe clicks now activate the resolved DOM
element through hit-tested pointer and mouse events in that frame's execution
context. A Chrome integration regression checks the child mutation, pointer
handler, overlay blocking, and link navigation; neighboring regular and
site-isolated frame-action checks pass. Top-level mouse dispatch produced a
false-success click in this topology. The in-process route uses DOM event
simulation, so OS-level input and less common pointer edge cases remain outside
the tested contract.

The following former wishlist items are present in the current code and should
be handled as maintenance or regression coverage, not new product work:

- MCP elicitation, tool annotations, structured output schemas, and streaming
  progress: `cmd/scout/mcp.go` and the MCP server integration.
- DOM observation/diff, active tab/navigation context, selector suggestions,
  click-by-text, semantic checkbox/radio filling, and post-mutation readback:
  `agent/observe.go`, `agent/selector.go`, `agent/clicktext.go`, and
  `agent/form.go`.
- Cookie management, bounded network diagnostics, failed-request reporting,
  session status/reset, and resilience controls: `agent/diagnostics.go` and
  registered tools in `cmd/scout/mcp.go`.
- Playbooks, tabs, profiles, trace exports, task runs, and recovery receipts:
  the corresponding `agent/` packages, CLI, MCP handlers, and schema artifacts.
- Chromium/CDP browser support, remote CDP, security policy, and distribution
  workflows. WebDriver BiDi remains a separately scoped cross-browser project;
  do not describe it as complete based on browser-family launcher support.

The legacy GitHub issue list (#5–#12) is historical context. Check the current
issue state and implementation before reopening any of those requests.

## Release and maintenance gates

- Keep Go and UI dependency versions compatible with their declared toolchain
  and peer constraints; document any intentionally deferred major upgrade.
  The Go and UI dependency upgrades in this worktree pass `go test ./...` and
  `npm run build`. On 12 November, `nox fix --outdated` found and upgraded the
  pinned `chromedp/headless-shell` image digest; a fresh `nox fix --outdated
  --dry-run` still reports all direct dependencies current, and the registry's
  current `stable` tag resolves to the same pinned manifest digest. The UI
  production build passes. `npm outdated --json` reports TypeScript 7.0.2 as
  the only newer UI dependency; its active `@volar/typescript` peer constraint
  remains `<7`, so keep TypeScript 6 until Vue's language tooling supports the
  newer compiler. A direct TypeScript 7.0.2 trial reproduced npm's peer warning
  and failed `vue-tsc -b` with `ERR_PACKAGE_PATH_NOT_EXPORTED` for
  `typescript/lib/tsc`; restoring 6.0.3 returned the production build to green.
  `go list -m -u
  all` still reports newer releases in upstream module graphs, but a fresh
  comparison against `go list -deps -test ./...` found zero upgrade candidates
  selected by Scout's test package set.
  A fresh 12 November pass also advanced `@vitejs/plugin-vue` to 6.0.11 and
  removed the now-unused `brace-expansion` override (the package is absent from
  the resolved tree). `npm run build` passes,
  `npm audit --json` reports zero vulnerabilities, and `npm outdated --json`
  lists only TypeScript 7.0.2, which remains outside the active Volar peer
  range. The full Go test package set resolves 28 external modules and has
  zero compatible upgrade candidates; updates reported for other modules in
  dependency-owned graphs are not selected by Scout's package set.
  A fresh Nox inventory still flags the browser base image's original zlib
  version, while a rebuilt Linux/arm64 Scout image reports the fixed
  `1:1.3.dfsg+really1.3.1-1+deb13u1` package installed by the Dockerfile and
passes `scout doctor --live --json` through Chrome startup, bounded read, and
cleanup. The inventory appears to report the upstream base layer without
applying the Dockerfile's later apt upgrade; retain the image-level runtime
check and do not describe the advisory as fixed in Nox until its final-image
analysis confirms the upgrade.
  A fresh dependency-only Nox scan on 12 November repeated the same base-layer
  zlib advisory. Separately, this checkout's Linux container build completed
  from the pinned digest, and `dpkg-query` inside the resulting image reported
  `1:1.3.dfsg+really1.3.1-1+deb13u1`. This verifies the final package version
  on the local build. A Linux/arm64 rebuild using a target-platform Scout
  binary also passed `scout doctor --live --json` through browser startup,
  status, bounded active-page read, and cleanup. The scanner's final-image
  coverage remains unresolved.
- Keep generated frontend output and dependency caches out of source security
  scans while continuing to scan source and manifests.
- Recheck security findings against current scanner output before claiming the
  vulnerability backlog is clear.
- Keep the CLI, MCP, Go API, schemas, and examples aligned when changing a
  public result or recovery contract.

**Security and dependency recheck (12 November 2026):** `govulncheck ./...`
reported no known Go vulnerabilities, `npm audit --audit-level=low` reported
zero UI vulnerabilities across the lockfile, and `npm outdated --json` found
no outdated UI packages. `go list -u -m all` shows no newer direct Scout
modules; it lists newer versions in upstream transitive graphs, while the
checked `go list -deps -test ./...` package set has no selected upgrade
candidates. This records scanner and registry state for this checkout and
date; it does not certify transitive modules outside Scout's build graph or
the built container image.

The MCP SDK now enforces `additionalProperties: false` at runtime, including
nested closed objects, before tool handlers run. The matched unknown-input
probe confirms Scout rejects the retired navigation field with zero fixture
requests and accepts the valid control with one. The fix is in the adjacent
`mcp-go` checkout consumed by Scout; Scout’s dependency manifest remains at
v1.30.1. Pinned agent-browser 0.42.0 also rejects it (as JSON-RPC `InvalidParams`); pinned Playwright MCP 0.0.85
advertises a closed schema but accepts the field and navigates, producing one
request. This is one navigation-field comparison, not a survey of every input
or tool. See the timestamped report under
`examples/benchmark/reports` (local audit artifact).

The MCP schema validator also now applies JSON Schema 2020-12 sibling
constraints alongside `$ref`; a regression confirms a closed-object sibling
rejects undeclared fields while the referenced valid input still passes. The
full `mcp-go` test suite passes after this change. A fresh matched run against
the current `mcp-go` checkout reproduces the side-effect result: Scout rejects
the unknown navigation field before the fixture request, agent-browser rejects
it at JSON-RPC validation, and Playwright MCP 0.0.85 accepts it and navigates.
See `unknown-input-20261112T063721Z.json` (local audit artifact).

**Current-tree recheck (12 November 2026):** focused-key actions now have an
optional page postcondition across the Go API, CLI, JSONL, and MCP; held-key
controls are available through those interfaces, and reset/close best-effort
releases held ordinary keys as well as modifiers. Chrome integration verifies
Meta+s, successful and rejected postconditions, held Shift behavior, and a
paired key-up for an ordinary key. `Control+A` is checked to send Ctrl+A
without implicitly adding Shift. Session close also emits a key-up for an
ordinary key left held. The macOS Chrome run passed; Windows and Linux
integration test binaries cross-compile, and the focused Windows runner is
configured in CI but still needs an actual Windows browser run.

`go get -u ./...` and `go mod tidy` upgraded the Go dependencies used by
Scout's packages. `go test ./...`, `go test -short -race -count=1 ./...`,
`go vet ./...`, `go mod verify`, and `govulncheck` with Go 1.27.4 pass. Direct
requirements have no newer compatible release; `go list -u -m all` still
reports newer versions for some transitive modules outside Scout's imported
package set. Forcing those unused graph entries into `go.mod` is removed by
`go mod tidy`. The UI production build, `npm audit --audit-level=low`, and 12
adoption tooling unit tests pass. `npm outdated` identifies TypeScript 7.0.2;
the installed Vue type checker requires TypeScript 6, so 6.0.3 remains the
latest compatible version.

Nox core runs taint analysis without its retired plugin. The standard scan's
three zlib advisories refer to the pinned Chromium base layer; the Dockerfile
installs Debian's fixed zlib package in a later layer, confirmed by local
final-image package inspection, which Nox does not account for. Optional
dependency-confusion, freshness, and exposed-endpoint checks were attempted.
Dependency-confusion checks completed and reported the absence of a private npm
registry pin for public scoped packages; there is no private package scope in
this tree, so no dependency-confusion exposure was confirmed. Freshness did not
run because its configured plugin binary is unavailable on this machine.
Exposed-endpoint checks flagged three `/health` or `/healthz` handlers: one UI
health handler bound only to 127.0.0.1 and two local fixture handlers, so these
are not production administrative endpoints. No live attack or running-system
probe was run.

**Current-tree recheck (13 November 2026):** `go test ./...`,
`go test -short -race -count=1 ./...`, `go vet ./...`, `go mod verify`, and
`govulncheck ./...` pass. `npm audit --audit-level=low` reports zero
vulnerabilities and `npm run build` passes. `npm outdated --json` reports only
TypeScript 7.0.2; TypeScript 6.0.3 remains the latest version compatible with
the installed Vue language tooling. `nox scan .` reports three zlib findings
for the pinned Chromium base-image layer, while the Dockerfile installs the
fixed Debian package in a later layer. The local Docker daemon was unavailable
for a new final-image inspection, so final-image scanner coverage and current
installed-package state were not reverified in this pass. Dependency freshness
is still unavailable because its optional plugin is not installed. No new
source-level vulnerability or dependency upgrade was confirmed.

The first live keyboard dogfood on `intel.klarlabs.de/signup` exposed a
macOS-specific Meta+A defect: CDP reported key dispatch and an unchanged-value
postcondition passed, but only the caret-position character was removed by the
subsequent Backspace. Chromium's synthetic key event does not invoke AppKit's
native Select All key equivalent, so Darwin Meta+A now includes CDP's
`SelectAll` editing command. A regression checks selection bounds before
Backspace, and the focused macOS Chrome integration passes. A second run of
the live field verified held Meta+A and Backspace cleared the entire disposable
value; the signup form was not submitted.
