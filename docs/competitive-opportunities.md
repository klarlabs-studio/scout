# Scout competitive opportunities

**Research date:** 12 November 2026. Feature status and public issue trackers change quickly; verify current releases before using this as external positioning.

### Keyboard-control parity gap (12 November 2026)

The current agent-browser surface includes a `press` action, while Scout had
text insertion but no public focused-key or shortcut action. Scout now exposes
`PressKey` through the Go API, one-shot CLI (`press-key`), JSONL session
(`press_key`), and MCP (`press_key` in core/interact/curated catalogs). It
dispatches CDP key events for common navigation/edit keys, F1–F12, ASCII
letters/digits, punctuation, and `Control`, `Shift`, `Alt`, or `Meta` chords.
The Chrome integration fixture checks the key, code, and modifier flags seen by
page handlers. This closes a concrete interaction-surface parity gap; it does
not imply superiority, prove a site's shortcut took effect, cover arbitrary
Unicode key layouts, or establish parity for every key understood by Chrome.
Callers should verify application effects with a separate page observation.

Key dispatch remains `effect_unverified` unless the caller supplies a page
postcondition. `press_key` now supports expected text, URL substring, title,
visible element, exact value, or selector count checks through Go, CLI, JSONL,
and MCP. `key_down`/`key_up` controls are available through the same surfaces.
The Chrome integration fixture verifies Meta+s produces `metaKey=true`, checks
an application-level result, and exercises held Shift with a later key-up.
Linux and macOS use the existing integration matrix; a focused Windows runner
now checks the same Meta behavior. These page-level tests validate Chromium's
event mapping, not native browser shortcuts or every platform-specific app
behavior.

### Competitive refresh: recovery is converging; host reconciliation is the wedge (12 November 2026)

The current competitor surface rules out “persistent browser sessions” as a
durable differentiator. Playwright MCP documents persistent user profiles and
an isolated mode, while agent-browser documents session management and
restoration ([Playwright profile modes](https://github.com/microsoft/playwright.dev/blob/main/mcp/configuration/user-profile.mdx),
[agent-browser session management](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md)).
Recovery failures also appear across the public trackers: Playwright MCP
[#1599](https://github.com/microsoft/playwright-mcp/issues/1599) described a
deadlocked singleton after a crashed tab and was closed through a fix;
Playwright [#41617](https://github.com/microsoft/playwright/issues/41617)
reported a disconnect that could strand the session, but was also closed;
agent-browser [#1118](https://github.com/vercel-labs/agent-browser/issues/1118)
was closed with follow-up fix #1136 for stale daemon/socket state. These closed
reports are valuable fault-injection cases, not current competitor defects.

Scout's opportunity is one layer above reconnecting the browser: retain a
stable operation identity across process loss, report whether the action may
have been dispatched, bind available evidence to browser/target/frame/policy,
and let the application host check its own authoritative state before a retry.
Scout already has durable task reservations, redacted receipts, explicit
unknown outcomes, and host reconciliation APIs. The current parity capture is
synthetic fixture evidence: it does not authenticate a producer, independently
observe browser binding, or query a real application's outcome ledger. So this
is a product direction supported by implementation and contract shape, not yet
a proven competitive win or validated buyer benefit.

**Product evidence update:** the runnable
[`durable_task_recovery` example](../examples/durable_task_recovery/main.go)
now covers both authoritative branches. Its saved
zero-effect report (local audit artifact)
records a worker killed after request receipt but before application commit,
an authoritative zero-effect check, a terminal `rejected` resolution, a blocked
same-ID attempt, then a new Scout task with the same application idempotency
key and exactly one final effect; a second distinct Scout task sending that key
receives the prior application result without adding an effect. The fixture
enforces the key atomically. This is one local end-to-end run against a
demonstration ledger, not a production database adapter, peer comparison,
reliability estimate, or buyer validation.

**Next product gate:** adapt this flow to a real service-owned transactional
ledger and run repeated crash-boundary trials against pinned peer releases.
Measure duplicate effects, false completion, and operator recovery steps.
Keep browser reconnection tests separate from business-outcome reconciliation.

**Fresh matched recovery evidence (12 November 2026):** Scout MCP and pinned
Playwright MCP 0.0.85 both reached the late-action page predicate in 10/10
repetitions. Scout returned an unknown outcome with a target-bound recovery
record, then reconciled without replay; Playwright kept its tool call alive and
inspected the page after the mutation. A hidden sentinel did not appear in
either adapter's retained serialized tool results. See the
[probe report](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/report.json)
and the summarized [Playwright MCP repetitions](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/playwright-mcp-repetitions.json).
The response-byte totals are not a size ranking because Scout serializes
normalized Go client results while Playwright records raw MCP result objects.
This one fixture supports a recovery-evidence distinction, not a broad
reliability or privacy guarantee.

### Tracker status recheck (12 November 2026)

The agent-browser session-isolation pair cited below is still open: issue
[#2422](https://github.com/vercel-labs/agent-browser/issues/2422) reports that
an MCP tool-supplied `session` can override the server-pinned session, and PR
[#2430](https://github.com/vercel-labs/agent-browser/pull/2430) remains open.
The PR's latest change also rejects `--session` smuggled through positional
tool arguments, in addition to scoping ordinary tool-selected session names
under the server-pinned name. Its author reports regression coverage for both
paths; GitHub shows the PR has not been deployed. The pinned v0.42.0 release
does not list this fix, and the local two-server collision probe reproduced on
that release. Keep this as a version-pinned regression case and rerun after a
release containing the change. ([PR details](https://github.com/vercel-labs/agent-browser/pull/2430))
Do not confuse that PR with agent-browser PR
[#2530](https://github.com/vercel-labs/agent-browser/pull/2530), which is an
unrelated proposal to reject malformed encrypted-profile nonce lengths before
they panic. The latter is a useful input-validation test idea, not evidence
about session isolation.

Agent-browser issue [#2574](https://github.com/vercel-labs/agent-browser/issues/2574)
is also open. Its reporter says agent-browser v0.42.0 wedges on macOS with
`--allowed-domains` when a page creates a cross-origin iframe: an auto-attached
target's `Runtime.evaluate` times out, `open` can take 30 seconds and leave the
tab at `about:blank`, and a Stripe Elements case reportedly wedges on a later
wait. This is a reporter's reproduction on Chrome for Testing 157.0.8103.50,
not an independently confirmed defect or a general claim about agent-browser.
Scout's local Chrome fixtures passed on 12 November for site-isolated frame
routing/actions and a denied child iframe. The blocked-child fixture now mirrors
the report's hostname orientation (`localhost` parent, `127.0.0.1` child); the
parent remains readable and the denied destination receives zero requests
([`TestIntegrationSwitchToSiteIsolatedFrameRoutesActions`](../agent/integration_test.go),
[`TestIntegrationURLPolicyBlocksSiteIsolatedIframe`](../agent/integration_test.go)).
The fixtures cover Scout's OOPIF attach, child inspection/action, and fail-closed
policy path; they do not reproduce Stripe Elements. The bounded, three-run
comparison of the `localhost` parent / `127.0.0.1` child shape also completed on
this macOS workstation: Scout and agent-browser 0.42.0 each returned without a
timeout in all three runs, reported the parent readable, and left the denied
child counter at zero; agent-browser cleanup succeeded in all three runs
(raw capture (local audit artifact),
[runner](../examples/benchmark/run_oopif_policy_rejection.mjs)). This does not
reproduce the reporter's wedge and does not establish a comparative reliability
advantage. Repeat on Chrome for Testing 157.0.8103.50 and include the Stripe
Elements flow before drawing a broader conclusion.

Stagehand issue [#3431](https://github.com/browserbase/stagehand/issues/3431)
remains open and its proposed `Fix:` guidance in
[#3434](https://github.com/browserbase/stagehand/pull/3434) remains draft.
The proposed paths distinguish remote auth errors, missing local Chrome,
refused CDP, and a generic forced session restart. Keep it in the planned
feature column until a release contains it.

Playwright MCP still lists [v0.0.85](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.85)
as latest. Its separate isolated-session idle leak report
[#1795](https://github.com/microsoft/playwright-mcp/issues/1795) is closed
through Playwright issue #43527; it should no longer be described as an open
gap. Keep long-call timeout behavior (#42693) as its own pinned regression
fixture: it concerns an active call outlasting the idle timeout, not the
closed leak caused by a long-lived MCP connection.

**Product consequence:** keep session owner pinning and browser lifecycle
identity in Scout's matched conformance suite. Run explicit cross-owner
attempts where a tool argument names another session, then verify both the
selected browser identity and effect destination. For timeout comparisons,
separate active-call expiry from idle-session cleanup and record whether the
browser survives each case. Scout's HTTP recovery integration already tests
same-name `work` sessions for two authenticated principals, verifies distinct
opaque selectors and browser targets, and rejects Bob selecting Alice's
selector. This covers the cross-owner name-collision case for Scout's current
HTTP principal boundary; it does not prove same-principal multi-agent isolation
or stdio multi-tenant isolation. See the public-handler case in
[`mcp_recovery_integration_test.go`](../cmd/scout/mcp_recovery_integration_test.go).

### Competitive refresh: Stagehand turns a false all-clear into a regression case (12 November 2026)

Stagehand issue [#3359](https://github.com/browserbase/stagehand/issues/3359)
remains open. Its proposed diagnostic fix, PR [#3423](https://github.com/browserbase/stagehand/pull/3423), was approved and mirrored as [#3439](https://github.com/browserbase/stagehand/pull/3439), which merged into `main` on 10 November. It exposes a cached browser-start error in session status and makes `browse doctor` fail on the relevant browser, Browserbase, or CDP row; the check remains read-only and does not relaunch Chrome. The PR explicitly addresses the false all-clear, not the reporter's underlying local `ECONNREFUSED`, which its author had not reproduced with a managed browser. The implementation also has mocked manager/doctor tests and an end-to-end closed-CDP check, according to the PR description. This is merged-source evidence, not yet evidence about a released package or a fix for the reported physical Chrome failure.

The follow-up [#3431](https://github.com/browserbase/stagehand/issues/3431) requests actionable `Fix:` guidance for those cached failures; its linked [#3434](https://github.com/browserbase/stagehand/pull/3434) remains draft at the 12 November recheck. This moves Stagehand's diagnostic coverage forward: Scout should not treat a missing cached-start-error signal as a differentiator. The useful comparison is whether each product's first bounded real operation agrees with its diagnostic verdict and returns a trustworthy, correctly scoped explanation.

Agent-browser issue [#1671](https://github.com/vercel-labs/agent-browser/issues/1671), a Linux ARM64 daemon hang report against v0.26.0, is closed as not planned. It should remain a historical stress-test idea, not evidence of a current v0.42.0 limitation; the issue itself describes a specific host and old version.

**Product consequence:** add cached launch failure to the pinned-version first-success comparison and record release status separately from merged source. For Stagehand, test both the failed-start diagnostic path (closed CDP) and the unresolved physical local-browser reproduction; for agent-browser, do not score #1671 as an open defect. Scout's remaining proof is the matched clean-environment matrix and independent verification of outcome provenance, not a claim that competitors lack doctor coverage.

The pinned agent-browser 0.42.0 first-success rerun (local audit artifact) passed all fourteen runner predicates on this workstation. Both tools' doctors and valid fixture reads succeeded. Scout's CLI returned exit 0 for the present marker and exit 1 with `postcondition_failed` for the missing marker. Agent-browser's text command returned exit 0 in both cases; the harness independently observed the text and graded the absent marker. This remains a one-run interface comparison: Scout uses one navigate command with a built-in expected-text assertion, while agent-browser uses `open` followed by `get text`, and only Scout emits separate startup/navigation/cleanup timings. The measured wall times are useful for rerunning the same procedure, not a fair speed or product-quality ranking. The unreachable-CDP run (local audit artifact) seeds failure only for Scout because agent-browser doctor has no matching endpoint injection; its healthy launch result must not be scored as a failed-case miss.

**Harness correction (12 November 2026):** the runner's negative
agent-browser read reused the browser process from the positive read, while
Scout launched a fresh browser for each one-shot command. The runner now
separates agent-browser's temporary HOME/XDG profiles, passes a distinct
explicit `--user-data-dir` to each control, and closes the positive browser
before the negative control. It also requires each `open` command to succeed
before grading the following text read. The corrected run at
local first-success audit artifact
passed all 16 predicates: both tools passed their doctor/positive read, both
negative controls observed the missing marker, Scout returned
`postcondition_failed` with exit 1, and agent-browser's read returned exit 0
for the harness to grade. This is one workstation run; Scout's one-shot
commands include browser startup and cleanup while agent-browser splits open,
read, and close. Do not rank these times. The npm package cache and Scout's
workstation HOME/XDG remain shared; only agent-browser's browser data is
isolated by the explicit profiles.

### Competitive refresh: saved flows and current tracker state (11 November 2026)

The latest [agent-browser v0.42.0 release](https://github.com/vercel-labs/agent-browser/releases/tag/v0.42.0)
ships experimental saved flows: record a task, then attempt direct HTTP, the
same request from the page, and finally replay of recorded commands. Saving
waits for in-flight requests and follows redirects to completion. The release
also narrows tool schemas and tells agents to prefer snapshots over screenshots.
This makes task recording and ordinary replay poor differentiators by
themselves. Scout's stronger hypothesis remains the host-facing contract after
an ambiguous mutation: preserve operation identity, return an explicit
unresolved outcome, and let the application host reconcile before any retry.
The release calls saved flows experimental; compare behavior on pinned versions
before making parity or quality claims.

The agent-browser MCP session-isolation report [#2422](https://github.com/vercel-labs/agent-browser/issues/2422)
and proposed fix [#2430](https://github.com/vercel-labs/agent-browser/pull/2430)
were both still open in the current tracker view. The report describes a
tool-supplied session overriding the server-pinned session. This remains a
useful isolation conformance case, not a durable Scout lead: the PR may change
the behavior before a released-version comparison.

The current Browser Use issues [#6218](https://github.com/browser-use/browser-use/issues/6218),
[#6363](https://github.com/browser-use/browser-use/issues/6363), and
[#6379](https://github.com/browser-use/browser-use/issues/6379), plus proposed
fixes [#6364](https://github.com/browser-use/browser-use/pull/6364) and
[#6380](https://github.com/browser-use/browser-use/pull/6380), were open in
the tracker view. They report malformed security configuration falling back
to permissive defaults, pruning a same-response screenshot while retaining
older images, and forwarding additional ignored legacy fields. These reports
and fixes are not merged-release evidence. They sharpen Scout's conformance
suite: corrupt and empty policy must fail closed; bounded evidence must retain
the newest decision-relevant item with explicit omissions; ignored fields must
not silently reach downstream consumers.

Stagehand issue [#3359](https://github.com/browserbase/stagehand/issues/3359)
still records a local CLI connection-refused case where `browse doctor` reports
green. A Stagehand PR [#3423](https://github.com/browserbase/stagehand/pull/3423)
added a cached browser-start failure diagnostic and its mirrored internal PR
was reported merged on 10 November. The issue's exact physical Chrome failure
was not reproduced in its investigation, so treat the change as improved
failure reporting, not proof that the original launch failure is fixed. A
separate request [#3431](https://github.com/browserbase/stagehand/issues/3431)
for actionable `Fix:` hints still has a draft external PR [#3434](https://github.com/browserbase/stagehand/pull/3434)
awaiting maintainer approval. Diagnostic guidance is becoming table stakes;
Scout should keep its bounded first-read check and clearly report whether the
application predicate itself was actually observed.

**Product consequence:** keep saved flows, snapshots, and health hints in the
parity column. Invest next in matched conformance runs for policy, output
provenance, and post-commit recovery, then use participant-authored Go
integrations to determine whether the host-owned outcome contract solves a
costly job. Repository fixtures and competitor tracker reports cannot establish
buyer demand or superiority.

### Competitive refresh: current lifecycle and diagnostic issues (12 November 2026)

The latest Playwright MCP release page still marks [v0.0.85](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.85) as latest (released 9 November). The focused rerun recorded below therefore remains current for the release visible today. Upstream [issue #42693](https://github.com/microsoft/playwright/issues/42693) remains closed as not planned; its report identifies a regression where the idle clock runs through a long active call. Keep the matched 5/5 result as a pinned-release regression fixture, not a claim about all Playwright versions or production failure rates.

The agent-browser report [#2079](https://github.com/vercel-labs/agent-browser/issues/2079) remains open. It describes a macOS session silently replaced with a fresh `about:blank` context after repeated `press` commands in v0.33.2 and v0.37.1, while the CLI still exits successfully; its reporter did not reproduce on Linux 0.37.1. This is a strong cross-platform continuity test case, but the versions are older than our current v0.42.0 benchmark and the issue alone does not establish a current defect. Add it to the lifecycle matrix as an OS-specific regression scenario and require the harness to assert URL, cookies, and browser/session identity after every action.

Stagehand [#3431](https://github.com/browserbase/stagehand/issues/3431) remains open and has a draft fix in [#3434](https://github.com/browserbase/stagehand/pull/3434): generate actionable doctor guidance from the cached launch error. In combination with [#3359](https://github.com/browserbase/stagehand/issues/3359), it shows that diagnostics should be judged on three separate outcomes: detecting a failed start, explaining a corrective action, and confirming an actual application read. Scout's first-success runner currently exercises the first and third layers; add guidance assertions for stable error codes so the operator advice stays specific and does not expose secrets.

**Research implication:** prioritize the shared lifecycle matrix over adding another browser command. Its next cases should cover idle time during a long active operation, silent browser replacement during repeated keyboard input, and diagnostic advice paired with a real first read. Pin OS and competitor release for each run, retain raw outputs, and label unresolved tracker reports as test prompts rather than evidence of a release-level weakness.

### Competitive refresh: fail-closed policy and fresh evidence under limits (11 November 2026)

Recent issue activity points to three practical reliability checks: preserve
security policy when configuration is malformed, keep the most useful recent
evidence when output is capped, and avoid forwarding ignored or deprecated
controls as if they were honored. These are test opportunities, not evidence
of competitor-wide failure rates.

Browser Use issue [#6218](https://github.com/browser-use/browser-use/issues/6218)
is open with a reporter's account that malformed persisted configuration can
fall back to allow-all and overwrite the original restrictions; the issue does
not identify a fix PR. Browser Use also has open issue [#6363](https://github.com/browser-use/browser-use/issues/6363)
and open PR [#6364](https://github.com/browser-use/browser-use/pull/6364),
which describe retaining older rather than newer screenshots when pruning a
response, and open issue [#6379](https://github.com/browser-use/browser-use/issues/6379)
with open PR [#6380](https://github.com/browser-use/browser-use/pull/6380),
which address deprecated action-result fields continuing to reach the model.
The issue reporters and PR authors provide the reproduction and proposed
regression cases; these remain tracker reports and unmerged proposals at this
snapshot. Stagehand issue [#3431](https://github.com/browserbase/stagehand/issues/3431)
requests actionable recovery guidance for browser startup failures; draft PR
[#3434](https://github.com/browserbase/stagehand/pull/3434) proposes structured
error codes and remediation hints. Agent-browser [v0.42.0](https://github.com/vercel-labs/agent-browser/releases/tag/v0.42.0)
remains the latest observed release, with experimental saved flows and session
reuse/relaunch fixes.

**12 November tracker recheck:** Browser Use #6218, #6363, and #6379 remain
open; proposed fixes #6364 and #6380 also remain open. The #6379 report now
links additional follow-up #6383, but the public issue still describes the
extra-field leak as unresolved. Playwright MCP issue #42693 remains closed as
not planned. These are tracker states only: the next useful comparison still
needs to run the fixtures against pinned release artifacts and capture the
actual policy, newest retained screenshot, and serialized tool result.

Scout's MCP origin parser distinguishes an unset policy from a configured
empty value, and `create_session` now rejects explicitly empty origin entries
so they cannot clear an inherited allowlist. Focused regression tests cover
empty, whitespace, and comma-created empty entries. A direct `doctor --json` probe with
`SCOUT_MCP_ALLOWED_ORIGINS=''` returned an error and remediation guidance,
which is evidence for this specific configuration path, not a proof that all
policy boundaries are fail-closed. Scout already applies a global serialized
response ceiling and exposes disk-backed artifact retrieval for oversized
results; the product gap to measure is whether the compact result preserves
the newest, most decision-relevant evidence and precisely identifies omitted
inputs.

**Next comparison:** add cross-tool conformance fixtures for malformed policy
state, explicit empty policy, newest-evidence retention under an output budget,
and unknown/deprecated input handling. Capture raw output, effective policy,
truncation metadata, and whether the tool silently changes or discards state.
Do not claim superiority until the same fixtures run against pinned versions
with independently checked results.

### Competitive refresh: first success must test the operation (10 November 2026)

The first-success opportunity is narrower than “add a doctor command.”
Agent-browser 0.42.0 already has a substantial doctor surface: it checks the
environment, Chrome, daemons, configuration, security, providers, and network;
it runs a headless launch check and supports JSON, offline/quick modes, plus
opt-in repairs. Its `--fix` mode can install Chrome, close version-mismatched
daemons, and prune expired state, so repair must stay a distinct, explicit
operator choice. ([0.42.0 changelog](https://github.com/vercel-labs/agent-browser/blob/main/CHANGELOG.md), [doctor reference](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/commands.md))

Two earlier agent-browser diagnostics reports now have materially different
status. Issue [#1821](https://github.com/vercel-labs/agent-browser/issues/1821)
reported `doctor` passing while Windows `open` hung on 0.37.0; the maintainer
identified fix #2160, shipped in 0.39.3, and closed the report. Issue
[#1711](https://github.com/vercel-labs/agent-browser/issues/1711) reported a
missing Chrome download misdiagnosed as a target-host timeout; on 3 November,
the maintainer reported that main/0.41.1 gave an actionable “Chrome not found”
message and fixed download-host attribution. Treat both as historical cases
and rerun their conditions on the pinned 0.42.0 release before making any
current comparison claim.

The current open Stagehand issue [#3359](https://github.com/browserbase/stagehand/issues/3359)
reports local page commands failing with `ECONNREFUSED` while `browse doctor`
returns all green. A maintainer follow-up documents a provider-free mock that
reproduced the false `ok` verdict after a cached browser-launch failure, but
explicitly says the physical Chrome startup failure has not been reproduced
and no production fix was proposed in that comment. Keep that distinction in
the scorecard: the report supports a targeted regression test, not a claim
that Stagehand generally misreports health.

Scout already separates static configuration, live browser reachability, and
an opt-in temporary-tab application probe with a caller-specified text marker.
Its readiness smoke covers configuration-only, live attach, marker success,
and missing-marker failure, but it is one Scout-only run. The concrete product
opportunity is to make each result auditable at the operation level: record
phase timings and stable failure codes for launch/attach, target selection,
navigation, and marker verification; preserve the non-mutating default; and
keep any repair command separate from diagnostics. A green configuration
check must never imply that an application workflow is ready.

**Next comparison:** on clean pinned environments, pair each tool's doctor
result with the first real, bounded browser read that its users would run.
Inject missing browser, unreachable endpoint, launch failure, responsive
browser with unresponsive page, application marker missing, and a valid path.
Record the doctor verdict, first-command result, diagnosis category, elapsed
time per phase, cleanup, and whether the tool changed local state. This
measures diagnosis accuracy and time-to-first-trustworthy-result without
confusing historical issue resolution with current product capability.

### Opportunity refresh: own the handoff between browser evidence and business truth (6 November 2026)

This refresh compares the execution surfaces and failure boundaries that matter
to Scout's Go host workflow. It is based on current official product material
and public tracker reports; issue reports identify test cases, not fleet-wide
failure rates.

**What competitors now cover well:** Stagehand v4 moves target management,
state, and CDP dispatch into a browser extension and says it queries live
browser state rather than relying on stale client-side models. Browserbase
offers hosted browser sessions, persistent contexts, session replay, and
readable agent traces. Steel similarly offers cloud sessions, session replay,
human takeover, and event timelines. Playwright already has mature action-level
traces with DOM snapshots, screenshots, console, network, and source context.
So persistent sessions, visual replay, broad traces, and basic reconnect are
not defensible Scout differentiators. ([Stagehand v4](https://www.browserbase.com/blog/stagehand-v4),
[Browserbase replay API](https://www.browserbase.com/blog/session-replay),
[Steel agent traces](https://docs.steel.dev/overview/agent-traces/overview),
[Playwright Trace Viewer](https://playwright.dev/docs/trace-viewer))

**The unresolved boundary is application outcome reconciliation.** Browser
traces can establish which actions and browser states were observed; they do
not independently establish whether the business system committed a charge,
order, or account change after the worker lost its response. Session restore
can preserve a browser context, but the host still needs an operation identity,
an authoritative application oracle, and a rule for when retry is safe. This is
the most credible opening for Scout because its durable task record already
models an unresolved outcome and its resolution can record host-supplied
evidence. The source is the product interfaces and documented capabilities;
the claim that this boundary is a buyer pain remains a hypothesis requiring
interviews and matched adoption trials.

Public issues reinforce several concrete conformance cases. Browser Use has an
open request to let a retry continue the same browser session (#6295), and a
separate request for active-tab connection management in MCP (#6296). A
Playwright MCP report says the first operation after a CDP disconnect fails
even though a subsequent retry recovers, which can prompt an agent to abandon
an authenticated context (#41617). Stagehand has recent reports involving
active-page selection, health checks that pass while local commands fail, and
clicks on wrapped links landing on a surrounding element (#3371, #3359, #3394).
These are good tests for explicit target identity, truthful health checks, and
bounded recovery instructions; they do not show prevalence or comparative
failure rates. ([Browser Use issues](https://github.com/browser-use/browser-use/issues/6295),
[Browser Use MCP tab issue](https://github.com/browser-use/browser-use/issues/6296),
[Playwright disconnect report](https://github.com/microsoft/playwright/issues/41617),
[Stagehand issue tracker](https://github.com/browserbase/stagehand/issues))

| Opportunity | Product move | Evidence that would justify investment |
|---|---|---|
| Business-outcome handoff | Make `unknown` a stable task result; expose a redacted task/operation receipt; accept host reconciliation with source and timestamp; never equate browser evidence with ledger truth. | Matched post-commit process-loss trials measure false completion, duplicate effects, correct unresolved status, and host actions across Scout and Stagehand Go. |
| Evidence provenance | Give every receipt field a source class: browser-observed, runtime-derived, or host-attested; record evidence gaps distinctly from empty evidence. | Independent verifier detects missing events, changed browser generation, wrong target, unavailable network evidence, and inconsistent host attestations. |
| Recovery instructions | `recover_task_run` now returns bounded guidance from the durable cursor across Go, stateful CLI, and MCP. An initial cursor requires confirming the old worker stopped and reconciling authoritative application state because custom stores without per-action progress may retain that cursor after dispatch. It never says retry is safe. | Cross-surface injected failures verify phase mapping, worker-stop prerequisite, target/browser replacement, stale or unavailable store state, and no increase in duplicate effects or false completion; operator trials measure whether the guidance reduces recovery time. |
| First-success path for Go hosts | Ship one small service/worker quickstart that wires durable task storage, owner scope, deadline, policy, and recovery inspection with minimal code. | Counterbalanced onboarding study measures setup time, integration LOC, first verified task, recovery time, and preference against Stagehand Go. |
| Runtime conformance | Publish pinned adapters and a machine-readable scorecard complementary to generic task-success leaderboards. Keep latency and normal-path success as guardrails. | Repeatable runs with a second verifier and raw artifacts; report denominators and confidence intervals rather than isolated pass rates. |

**Priority:** finish the independent verifier and matched post-commit recovery
study before building a hosted dashboard or expanding the action catalog. The
first conservative cursor-based recovery guidance is implemented; validate its
phase mapping across injected failures before adding more recommendations.
Keep receipts portable and provider-neutral so Scout can wrap local Chromium
or remote CDP; the advantage should come from the host contract, not requiring
Scout-owned browser infrastructure.

**Current position:** “verifiable browser execution for Go application
workers” is the strongest hypothesis to test. A narrower and more concrete
lead is: “when a browser task may have committed but its worker lost the
response, Scout preserves enough identity and evidence for the host to
reconcile before retrying.” This describes intended behavior, not a proven
competitive win. The proof still needs independent adoption data and repeated,
matched recovery measurements.

### Competitive refresh: benchmark coverage is the opening (6 November 2026)

The current public WebdriverIO browser-agent leaderboard compares seven tools
on a fixed sample of 100 live-site Online-Mind2Web tasks with a fixed harness
and independent judging. Its displayed rates pool tool runs across two models,
so the per-tool denominators vary and exceed 100. The current leaderboard
reports Stagehand 60%
(151/252), WebdriverIO session 62% (103/165), WebdriverIO MCP 59% (98/166),
Playwright MCP 52% (133/256), and agent-browser 48% (128/265). The denominators
differ because the displayed results aggregate runs; the leaderboard describes
them as the newest version of each tool that ran every task. It reports cost,
tokens, duration, and calls, and cautions that the 100-task sample yields
intervals around ±10 percentage points, so close rates do not establish a
reliable difference. Scout is not included. These results are useful evidence
that generic task success and efficiency are
already being measured, but they say nothing about host-authored recovery code,
authoritative mutation outcomes, or duplicate effects. ([leaderboard and
methodology](https://benchmark.webdriver.io/))

This narrows Scout's opportunity. Do not try to win a generic browser-agent
success leaderboard with a repository-specific recovery fixture. Build a
complementary conformance study around the failure contract the public benchmark
does not report: inject worker loss after a non-idempotent commit, then measure
false completion, duplicate effects, unresolved-outcome handling, integration
LOC, operator decisions, and time to a correct host decision. Include ordinary
success, cost, and latency as guardrails so recovery controls are not rewarded
if they make the common path unusably expensive. Run Scout and pinned peers
against the same fixture and publish per-run outcomes, version hashes,
confidence intervals, and adapter code. This is an experiment design, not a
claim that Scout currently beats these tools.

The leaderboard also makes the Stagehand comparator choice clearer: Stagehand
is competitive on generic task completion, while its current Go SDK can be
measured on the deterministic path separately from model-backed actions. Keep
the proposed Stagehand Go adoption study, but include the host glue and recovery
decision as explicit outcomes; task success by itself cannot validate Scout's
position. See the [adoption protocol](adoption-study.md) and
[opportunity brief](opportunity-brief.md).

### Capability map and positioning consequence (6 November 2026)

This is a product-surface comparison from official docs, release notes, and
the pinned interfaces already inspected in the local reports. It is not a
quality ranking; “available” means documented or implemented, not that every
deployment path was benchmarked.

| Tool | Current strengths Scout must treat as parity | Remaining opening for Scout |
|---|---|---|
| Playwright MCP | Structured accessibility snapshots and bounded `browser_find`; tabs, navigation, interaction, screenshots, network tools, optional PDF/vision/DevTools/testing capabilities; persistent profiles, isolated sessions, extension attachment, and named isolated contexts. The v0.0.84 release added separately named isolated contexts and bounded find results. | Show that one host can carry task identity, deadline, policy, verified outcome, and post-interruption reconciliation through Go, CLI, and MCP with less integration effort. Do not claim Playwright lacks session isolation or output controls. ([capabilities](https://playwright.dev/mcp/capabilities), [v0.0.84](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.84)) |
| agent-browser | Terminal-first CLI and MCP, named sessions, pinned tabs, persistent storage, batching, broad network/diagnostic surface, plugin integrations, and sandbox helpers. It is shipping features quickly. The exact MCP pinned-session override issue #2422 remains open; PR #2430 proposes server-name scoping and includes regression cases, but is not merged at this snapshot. | Make owner/session configuration and task outcome explicit at the runtime boundary, then publish repeated matched runs. Do not make the open issue itself the product thesis: if #2430 merges, that failure case becomes parity. ([changelog](https://github.com/vercel-labs/agent-browser/blob/main/CHANGELOG.md), [issue #2422](https://github.com/vercel-labs/agent-browser/issues/2422), [open PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430)) |
| Stagehand Go | Maintained SDK in the active Stagehand monorepo, with semantic `act`/`observe`/`extract`, direct page/locator access, local Chrome, and Browserbase-backed execution. Go compatibility and model-assisted interaction are not unique to Scout. | Compare the complete host workflow, especially deterministic task IDs, ambiguous mutation handling, authoritative-state reconciliation, deployment choices, and measured setup effort. ([official Go SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go), [Go quickstart](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/quickstart.mdx)) |
| Scout | One Go engine exposed through library, CLI, and MCP; target-bound verified actions, durable task records, explicit `unknown` outcomes, owner-scoped session recovery, a portable no-replay receipt, bounded output retrieval, and operator diagnostics. Current repo fixtures cover parts of this integrated path. | Turn the pieces into one independently audited task contract and show a buyer-facing advantage with repeated matched recovery trials and a paired onboarding study. Current technical fixtures do not prove demand or lower implementation effort. ([product vision](product-vision.md), [adoption study](adoption-study.md)) |

**Positioning consequence:** browser features are converging faster than a
feature checklist can differentiate Scout. Lead with the host's business
decision after an interrupted mutation: what was dispatched, what the browser
verified, what remains unknown, which authoritative source resolved it, and
why replay is or is not allowed. Keep session ownership, target identity,
policy, deadlines, and output bounds as supporting requirements in the same
execution record. The next proof should compare that entire workflow across
Scout and Stagehand Go, with identical fixture semantics and explicit adapter
costs; a Scout-only recovery pass or a competitor's open issue is insufficient.

### Live tracker recheck: session selection and click evidence (6 November 2026)

The dated snapshot was rechecked against GitHub on 6 November. Agent-browser
issue [#2422](https://github.com/vercel-labs/agent-browser/issues/2422) remains
open and describes a caller-supplied MCP `session` overriding the server's
`AGENT_BROWSER_SESSION`; proposed PR
[#2430](https://github.com/vercel-labs/agent-browser/pull/2430) remains open
and unmerged. Browser Use issue
[#6296](https://github.com/browser-use/browser-use/issues/6296) remains open
for attaching MCP tools to an already active browser session instead of
creating a tab. Its related draft PR
[#6305](https://github.com/browser-use/browser-use/pull/6305) is open and
unmerged; it proposes opt-in selection of a uniquely focused existing tab,
with an error on ambiguity and no fallback tab. Stagehand issue
[#3394](https://github.com/browserbase/stagehand/issues/3394) remains open and
reports that wrapped-link clicks can return success while landing on the
surrounding element; the report names Go SDK v4.1.0 and v4.3.0. These are
individual reports and proposed fixes, not prevalence or release-wide failure
evidence.

**Product consequence:** “attach to the user's current browser” is not a
complete contract. Scout should require or return an explicit target identity,
refuse ambiguous initial selection, bind each result to the actual target and
browser generation, and verify a caller-declared postcondition after dispatch.
That result should distinguish `dispatch attempted`, `postcondition verified`,
and `unknown`; an API success or click event alone is insufficient. Scout
already has target-bound operations, expected-target checks, and explicit
postcondition outcomes in parts of its Go/MCP surfaces, but this recheck does
not demonstrate one consistent contract across every entry point or compare
its false-success rate with current competitor releases. Keep that as a
conformance work item, not a superiority claim.

### Research update: compete on runtime conformance, not agent leaderboard scores (6 November 2026)

The latest tracker and release scan strengthens the earlier execution-contract thesis and changes how Scout should prove it. Playwright MCP v0.0.84 now documents named isolated contexts, URL parameter redaction, bounded find results, retained network history across navigations, and per-client idle timers. These are concrete capabilities Scout must compare against when defining its default operator contract; the release also shows how quickly a once-distinctive capability becomes baseline. ([Playwright MCP v0.0.84](https://github.com/microsoft/playwright-mcp/releases))

Recent issue reports cluster around **runtime conformance**, not missing action verbs: agent-browser lists session-argument precedence, slow attach with discarded tabs, missing direct-page event capture, and HAR reporting success after a browser restart; Stagehand lists active-page selection, a `doctor` command that reports healthy while local commands fail, and extraction without element provenance. Each is an individual, version-sensitive report. The useful signal is that operators need to know whether a result is bound to the right page and whether evidence collection actually happened. ([agent-browser issues](https://github.com/vercel-labs/agent-browser/issues), [Stagehand issues](https://github.com/browserbase/stagehand/issues))

At the same time, public WebArena leaderboards now track many agent submissions, including scores above 70%, and require a public source and setup notes for submitted results. That measures agent-and-model task completion, not browser-runtime guarantees. Scout should not optimize its positioning around a model leaderboard where outcomes are confounded by prompts, models, memory, and harnesses. Instead it can publish an independently reproducible **runtime conformance scorecard** that holds the model or deterministic script constant and measures wrong-target prevention, unknown-outcome handling, replay prevention, evidence completeness, deadline behavior, owner isolation, resource cleanup, and time to recover. ([Steel browser-agent leaderboard](https://leaderboard.steel.dev/leaderboards/webarena/), [leaderboard evidence requirements](https://github.com/steel-dev/leaderboard#contributing))

**Sharper product opportunity:** give every browser task a compact, machine-readable execution receipt with stable caller/session/target/generation identity, policy digest, deadline, outcome, evidence availability, and recovery cursor; make the scorecard verify the receipt against browser events and an application-side oracle. This connects Scout's Go embedding and durable recovery work into a product surface an operator can inspect and compare. It also addresses an adoption gap: a buyer can try Scout without buying a model provider or managed browser, then run the same contract against local Chromium and remote CDP.

The differentiator needs a harder boundary than “we have receipts.” Receipts are only valuable if their fields are derived from the runtime that owns them, distinguish observed facts from host assertions, and stay stable across Go, CLI, and MCP. For example, “network evidence unavailable” must differ from “zero requests”; “browser generation changed” must differ from “target not found”; and an ambiguous dispatched mutation must remain `unknown` until the host's business-state oracle resolves it. A signed field whose source is still the caller is provenance metadata, not independent evidence.

| Investment | Why it can beat the current feature race | Proof required |
|---|---|---|
| Runtime conformance suite | Competitor tool catalogs are expanding; matched behavioral guarantees are harder to imitate with a release note. | Pinned versions, identical fixtures, repeated trials, raw machine-readable output, and a second verifier independent of Scout. |
| Evidence provenance | Recent event-capture and extraction reports show that empty or unattributed evidence can look like success. | Inject missing event sources, navigation, browser restart, and extraction from duplicate values; assert completeness/source identity in receipts. |
| Ownership and lifecycle clarity | Isolation and session controls are now table stakes, but mismatched identity and cleanup behavior remain operationally costly. | Two-client/two-context races, idle calls, disconnects, target churn, and explicit local-versus-caller-owned cleanup checks. |
| Integration speed | A Go-native runtime can win when it shortens the path from service code to a trustworthy result. | Fresh-repository study against a Go-compatible alternative, measuring first verified action, glue code, recovery work, and developer preference. |

**Recommended execution order:** (1) finish a single cross-surface receipt contract and document which fields are observed versus asserted; (2) build the independent verifier and matched fault-injection fixtures; (3) run release-pinned comparisons with Playwright MCP, agent-browser, and Stagehand; (4) use scorecard results and 5–8 platform-team interviews to choose the product message. Only then decide whether to invest in another integration, dashboard, hosted control plane, or richer agent-facing APIs. Existing local demos support feasibility of pieces of this approach; they do not establish comparative advantage or customer demand.

**Decision:** make “verifiable browser execution for application teams” the product hypothesis, with safe resolution of uncertain mutations as its lead proof point. Keep broad action coverage and WebMCP on the parity track. Promote the hypothesis to positioning only if the independent scorecard shows a repeatable advantage and interviews confirm that teams will adopt/pay for the contract. Tracker signals and public leaderboard results do not establish either claim.

### Further research: win on operator confidence and first success (6 November 2026)

The newest GitHub reports strengthen the case for competing on the execution
contract, but they also expose two adoption-level opportunities: an operator
must be able to diagnose a clean installation, and browser attachment must stay
predictable under realistic browser state.

* **Session ownership can fail silently.** Agent-browser issue
  [#2422](https://github.com/vercel-labs/agent-browser/issues/2422) reports
  that a model-supplied MCP `session` can override the server's pinned session,
  causing separately configured servers to share one browser. Its proposed
  fix, [PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430),
  scopes names under the pinned server session and is still open at this
  snapshot. The repository's pinned 0.41.2 reproducer confirms the issue:
  two servers configured with different `AGENT_BROWSER_SESSION` values both
  reach the same `shop` browser, share its cookie and localStorage, and observe
  the other server's navigation (raw report (local audit artifact),
  MCP transcript (local audit artifact),
  [reproducer](../examples/benchmark/session_isolation/agent_browser_mcp_isolation.py)).
  The PR is still a proposal, not released behavior; its author reports
  regression cases for pinned-name scoping, pass-through, unpinned operation,
  `extraArgs --session`, and invalid pinned names. Scout's corresponding local
  evidence previously used different selector mechanisms. A new matched
  Scout probe sends the same explicit `default` session selector in each of
  two authenticated HTTP principals on the same remote CDP endpoint. All five
  repetitions resolved distinct targets and isolated storage; cross-principal
  transport and opaque-ID reuse were denied, while same-principal reconnect
  and owner survival passed (five-run report (local audit artifact),
  five-run principal baseline (local audit artifact)).
  This directly exercises Scout's per-call selector boundary, but it uses
  `_meta.go.klarlabs.de/scout/session`, not agent-browser's `session` tool
  argument, and one successful run does not establish broader security or
  comparative reliability.
* **Attach time is part of perceived reliability.** Agent-browser issue
  [#2431](https://github.com/vercel-labs/agent-browser/issues/2431) reports
  that discarded tabs can add serial 3-second renderer probes, taking attach
  time from about 0.2 seconds to 6.3 seconds in the reporter's five-tab
  scenario. The proposed concurrent probe lowers the two-discard case to
  roughly 3.2 seconds while preserving tab selection. Scout should benchmark
  cold attach, warm attach, and browser recovery separately with live,
  discarded, and crashed tabs; optimize only where measurements show an
  advantage.
* **Configuration failure handling is a trust boundary.** Browser Use issue
  [#6218](https://github.com/browser-use/browser-use/issues/6218) reports a
  reproducible fail-open path in version 0.13.13: malformed configuration is
  replaced by defaults, which clears domain restrictions and IP blocking. The
  report is still open in the tracker. Treat this as a specific release finding,
  not as a broad safety judgment. Scout's product bar should be explicit:
  missing config may select documented first-run defaults; malformed or
  unreadable configured policy must fail startup, preserve the original, and
  never make browser work more permissive.
* **Health checks must match their documentation.** Browser Use issue
  [#6309](https://github.com/browser-use/browser-use/issues/6309), opened
  November 4, reports that advertised `doctor --json` forms fail argument
  validation on the published 0.13.14 package before reaching the health
  reporter. This points to a practical opening: make Scout's doctor/status
  command machine-readable, safe to run without launching a browser, and verify
  the installed release artifact in a fresh environment.

**Opportunity update:** prioritize a compact operator-ready surface alongside
the durable task contract: `doctor --json` for configuration, browser endpoint,
policy and store health; an explicit session/target identity in every result;
and typed diagnostics that say whether retry, reconnect, or host reconciliation
is safe. A clean-install integration recipe should reach one verified browser
action without a model provider, then show how to enable MCP and durable
recovery. This can differentiate Scout on time-to-first-trustworthy-result,
which is more concrete than tool-count claims.

**Validation:** use a clean container or temporary user profile and a pinned
release artifact. Measure install-to-first-verified-action time, diagnostic
accuracy, cold/warm/recovered attach latency, wrong-session selection, and
recovery success after process and browser loss. Include malformed policy and
unavailable store cases, requiring clear startup failure and zero browser
dispatch. Compare the same scenarios against current pinned versions of
agent-browser, Browser Use, Playwright MCP, and Stagehand. Public issues are
individual failure reports and feature requests; they identify adversarial test
cases, not prevalence or comparative product performance.

### Competitive update: lifecycle contracts are converging (6 November 2026)

Recent upstream work makes basic session continuity and doctor commands less
distinctive. Agent-browser documents strict `--pin-tab` behavior, stable target
IDs and restart reattachment in its [session management guide](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md).
Browser Use has open requests for same-session recovery and active-tab MCP
connection management ([#6295](https://github.com/browser-use/browser-use/issues/6295),
[#6296](https://github.com/browser-use/browser-use/issues/6296)); the recovery
change remains a proposal, not shipped parity. Treat pinning and same-session
continuation as a baseline to match and test, not a differentiator.

The remaining opening is a **host-owned execution contract** that makes a
browser task's owner, session, target, policy, deadline, postcondition and
outcome visible together. When dispatch may have happened but the caller times
out, the runtime should return `unknown`, retain task identity, prevent blind
replay, and let the host reconcile against its authoritative business state.
This may matter to Go service and worker teams more than another action
catalog, but it remains a positioning hypothesis until buyer interviews and a
matched setup study confirm the pain and show lower integration effort.

Operational reports point to an adjacent resource-lifecycle opportunity.
Playwright MCP issue [#42693](https://github.com/microsoft/playwright/issues/42693)
reports idle cleanup interrupting a call that outlasts the timeout; a separate
renderer-crash report was closed through upstream Playwright work
([#1599](https://github.com/microsoft/playwright-mcp/issues/1599)).
Agent-browser issue [#2431](https://github.com/vercel-labs/agent-browser/issues/2431)
reports slow CDP attachment with discarded tabs. These are individual reports,
not prevalence data. A useful product response is a lifecycle scorecard for
in-flight work, idle cleanup, crash/disconnect recovery, attach latency, and
ownership, without claims that competitors are broadly unreliable.

**Next research gate:** interview 5–8 Go platform or QA engineers who operate
browser workers, then have them implement the same verified task using Scout
and Stagehand Go. Measure deterministic and agent-driven tracks separately.
Capture time-to-first-verified-result, integration code, policy and identity
wiring, fault recovery steps, and preference. Keep runtime trials pinned by
version and recheck issue/PR status at test time. If buyers do not value
host-side reconciliation or the setup study shows no meaningful advantage,
keep the durable contract as a technical strength rather than the lead product
position.

**Scout implementation update:** `scout doctor --json` now checks local
private-IP policy configuration, MCP profile and required-tool compatibility,
origin and principal policy syntax, conflicting HTTP policy modes, optional
HTTP listener authentication, remote CDP endpoint syntax, and task-store path
permissions without launching a browser or opening the store. It returns
versioned JSON, uses nonzero exit status for invalid configuration or an
unusable path, and omits secret values from validation failures. This lowers
diagnosis friction. Default preflight does not prove endpoint reachability,
store integrity, browser attach latency, or time to first verified action. The
opt-in `scout doctor --live` mode now launches local Chrome or attaches to
`SCOUT_REMOTE_CDP`, then reads Scout session status under a bounded timeout.
Default preflight marks browser readiness `not_checked`; live launch/attach
failure returns an error status. Each JSON check includes a stable `code` for
machine classification and a stable `remediation` hint for host automation,
separate from operator-facing detail text. Live `browser_live` and `app_probe`
checks include elapsed milliseconds. Browser startup/attach plus session health
is measured as one phase; app-probe timing includes navigation, marker
verification, and cleanup. Marker-missing and cleanup-failed probe results have
distinct codes. These hints cover configuration, browser connection and
task-store repair; they do not authorize retrying a mutation. This is a reachability/session
check by default, not a page navigation or application functionality check.
The opt-in `--probe-url` opens a temporary tab constrained to the supplied
origin; `--probe-text` can require a known health marker. It restores the prior
target and closes the probe tab. Because the site is contacted and its scripts
run, this should use a dedicated health fixture. This gives a truthful
application-path signal without navigating the operator's current page. The
doctor JSON schema is version 3 for the stable check code and live phase timing
fields. With `--live --task-store=PATH`, it also opens an existing bbolt
store read-only, verifies the required bucket structure, and reports lock
contention separately from corruption. It never creates a missing store or
modifies task records; write capacity remains unverified. A busy store may
simply be owned by the active service and is therefore a warning, not a failure.

**Application-probe smoke (6 November 2026):** a live local Chrome run reached a
loopback fixture in a temporary tab, confirmed the expected marker, restored
the initial target, and closed the probe tab. A second run with a missing marker
returned a nonzero doctor status while reporting successful tab cleanup; neither
the probe URL nor expected text appeared in JSON output. The raw result is in
`scout-doctor-app-probe-2026-11-06.json` (local audit artifact).
An additional run configured a global allowlist that excluded the probe origin;
doctor rejected the probe before navigation. This verifies the local opt-in path
and policy boundary only. Remote CDP ownership, concurrent
clients, repeated reliability, and equivalent competitor probes remain open.

### Further research: compete on truthful health and continuity (6 November 2026)

The newest public reports sharpen the product opportunity from “recovery” to
**a health signal that predicts whether the next operation can succeed, with
clear resource and retry guidance**. The current Stagehand issue #3359 describes
local page commands failing with `ECONNREFUSED` while `browse doctor` reports
`[ok]`; it includes a qualification reproducer whose expected failure is that
the diagnostic verdict stays green after a cached browser initialization
failure. This is a user report and reproducer, not independently verified
behavior across Stagehand releases. Scout now keeps safe configuration
preflight separate from explicit live readiness. The new `--live` mode launches
or attaches under a bounded timeout and reports the live session check as `ok`,
`error`, or `not_checked`. It does not navigate to an external page, so it
cannot establish that a target application works; a stronger end-to-end probe
should use an operator-supplied fixture URL and remain opt-in.
([Stagehand #3359](https://github.com/browserbase/stagehand/issues/3359))

Lifecycle defects surface across products: a Playwright MCP issue reports a
renderer crash leaving a stale tab reference that blocks later calls until
restart, while Stagehand reports a CLI daemon aborting operations after 35
seconds despite longer requested waits. Playwright's report was closed through
an upstream Playwright issue, so treat it as a regression scenario, not a
confirmed current defect. These point to tests of health transitions, not just
startup: in-flight call versus idle expiry, renderer crash, daemon timeout,
browser disconnect, and recovery to a new target generation. Scout should
expose which resource failed and what can safely be retried; mutation ambiguity
still requires application reconciliation. ([Playwright MCP #1599](https://github.com/microsoft/playwright-mcp/issues/1599), [Stagehand #3369](https://github.com/browserbase/stagehand/issues/3369))

Concurrency and ownership are increasingly core capabilities. Playwright MCP
v0.0.84 offers named isolated contexts, per-client idle expiry, URL parameter
redaction, and page-provided WebMCP tools. Scout should treat these as parity
requirements and test tenant isolation and untrusted page-supplied schemas.
Stagehand's open #3371 reports docs selecting the oldest tab via `activePage()`
instead of the active tab; agent-browser #2422 reports a model-provided MCP
session overriding a server-pinned session, with PR #2430 proposing scoped
session names. Pair those with Scout's authenticated principal routing and
owner-bound handles in a two-client, two-tab matrix. A correct result should
name the principal, session, target and browser generation, and reject an
ambiguous active target rather than guessing. ([Playwright MCP v0.0.84](https://github.com/microsoft/playwright-mcp/releases), [Stagehand #3371](https://github.com/browserbase/stagehand/issues/3371), [agent-browser #2422](https://github.com/vercel-labs/agent-browser/issues/2422), [agent-browser PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430))

**Where Scout can win:** competitors already offer broad action catalogs,
AI-directed observe/act/extract, browser hosting or state restore, named
contexts, recordings, and MCP integrations. Tool breadth is a weak lead. Scout
can stand out for Go services that need a small, provider-neutral execution
contract: caller-owned browser, explicit identity and policy, durable
no-replay task reservation, typed `verified`/`rejected`/`unknown` outcome, and
host reconciliation against application state. The durable recovery example
now demonstrates a per-task authoritative ledger lookup across Scout process
loss and demonstrates rejection when the browser generation changes. That is
a credible product thesis and a local fixture proof, not a competitive
benchmark or production provider validation.

| Opportunity | Scout advantage to build | Evidence required before claiming leadership |
|---|---|---|
| Truthful health and repair guidance | Separate safe preflight from live readiness; report browser/store/transport state and whether retry, reconnect, or host reconciliation is appropriate. | Inject launch failure, stale endpoint, renderer crash, store lock/write failure, and slow in-flight calls; ensure no false-green result. |
| Safe mutation continuity | Keep task IDs reserved after ambiguous dispatch; provide a redacted, portable receipt and host adapter contract for authoritative reconciliation. | Repeat process/browser-loss boundary runs and compare duplicate effects, false completion, unresolved rates, and operator steps on identical fixtures. |
| Tenant and target ownership | Make principal, session, target, and browser generation visible in every result; refuse ambiguous selection. | Two clients with colliding names, switching/clone races, stale handles, isolated contexts, and wrong-target attempts. |
| First successful integration | Offer a clean Go service recipe with local Chrome, remote CDP, and MCP configuration, with one verified action and troubleshooting path. | Timed clean-environment study against Stagehand Go, Playwright MCP, and agent-browser: install time, first verified outcome, integration code, and recovery work. |

**Priority:** first close the gap between static diagnostics and live readiness,
then publish a reproducible lifecycle and recovery scorecard. Continue to treat
policy correctness and untrusted page data as trust requirements, but do not
position either as a moat without broader boundary testing. Current tracker
issues identify useful test cases; they do not establish competitor prevalence
or Scout's comparative performance.

### Further research: make the caller contract the advantage (6 November 2026)

Current competitor signals point to a sharper opportunity than expanding Scout's browser API. Browser Use issue [#6295](https://github.com/browser-use/browser-use/issues/6295) requests that an agent recovery tool reuse the active browser session; PR [#6298](https://github.com/browser-use/browser-use/pull/6298) proposes that behavior. Its issue list also includes [#6218](https://github.com/browser-use/browser-use/issues/6218), reporting that malformed security configuration resets the MCP server to allow-all. Treat the latter as a security regression signal to verify against the current branch, not as a claim about released defaults. Agent-browser's [#2422](https://github.com/vercel-labs/agent-browser/issues/2422) reports that an MCP-provided session can override a server-pinned session, with [PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430) proposing scoped naming. These are examples of caller identity, session ownership, and fail-closed configuration becoming difficult at integration boundaries.

The product opening is to make those boundaries explicit and composable: each request carries a trusted caller identity, a fixed browser/session/target binding, a policy digest, a deadline, and a typed result (`verified`, `rejected`, or `unknown`). Recovery should reattach to that same owned state, preserve replay protection, and distinguish a Scout observation from a host's authoritative application-state attestation. Scout already has significant pieces across Go, MCP and CLI, but the integration path still needs to prove that a service can configure them without bespoke glue and can inspect the same contract after restart.

| Competitive signal | Product opportunity for Scout | Proof to collect |
|---|---|---|
| Recovery must reuse the active browser session (Browser Use #6295/#6298). | One stable run/session/target identity through action, timeout, and recovery. | Recover from a fresh host process against the same browser; verify the mutation is not replayed and policy is unchanged. |
| Server-pinned session can be overridden by MCP arguments (agent-browser #2422/#2430). | Treat caller, transport session, Scout session, and browser target as separate identities; show the binding in each receipt. | Adversarial two-client test with colliding names and attempts to select another session. |
| Invalid security configuration may fall back to allow-all (Browser Use #6218). | Parse and validate policy once at startup; any invalid or unavailable policy produces a typed startup failure, never an implicit permissive mode. | Malformed-file, missing-file, and reload-failure probes that assert no browser action or network request reaches the fixture. |
| Managed-browser products bundle persistence, recordings, concurrency, and deployment. | Integrate with caller-owned or hosted browsers while keeping execution receipts portable and provider-neutral. | Same outcome/recovery contract across local Chromium and at least one remote CDP provider, with ownership/cleanup verified. |

**Recommended investment:** ship a minimal caller-facing `ExecutionContext`/receipt bundle (identity, policy digest, target, deadline, outcome, evidence references, recovery cursor) consistently across Go, JSONL CLI and MCP, then publish the matched boundary tests. Keep provider integrations and policy authoring behind the existing host interfaces until interviews establish a buying blocker. This is a hypothesis about product leverage, not a proven market advantage. The most valuable next research is 5–10 interviews with Go platform teams about the incidents and glue code they have today, followed by a timed integration study using a fresh service repository.

**Competitive guardrail:** issue reports are case studies, not prevalence data; PRs may be incomplete or merged after this snapshot. Verify each issue, PR and release before external use. Compare product behavior on reproducible fixtures, not tracker counts. Do not claim Scout is safer or more reliable until equivalent fault-injection runs produce repeated, independently reviewable results.

### Live tracker recheck: continuity converges, policy evidence stands out (6 November 2026)

The latest tracker state reinforces that session continuity is rapidly becoming
table stakes. Agent-browser PR [#2430](https://github.com/vercel-labs/agent-browser/pull/2430)
is still open and proposes scoping MCP tool-provided session names under the
server-pinned session after issue [#2422](https://github.com/vercel-labs/agent-browser/issues/2422)
reported cross-server session confusion. The background-tab regression
[#2407](https://github.com/vercel-labs/agent-browser/issues/2407) remains in
the open issue list, so keep the existing pinned-version reproduction as a
fixture rather than a product claim. Browser Use issue
[#6296](https://github.com/browser-use/browser-use/issues/6296) remains open;
PR [#6305](https://github.com/browser-use/browser-use/pull/6305) attempts
active-tab selection on initial connection, but its discussion records a
headed-mode run where the environment exposed no uniquely focused page. That
is useful evidence of a hard ambiguity boundary: a good API must fail clearly
when active-tab identity cannot be proved.

Playwright MCP v0.0.84 is listed as the latest release in its tracker and adds
named isolated contexts through `browser_tabs` plus per-client idle expiry for
isolated HTTP clients, alongside fixes for graceful client disconnect and
route-header/origin-filter interaction ([release notes](https://github.com/microsoft/playwright-mcp/releases)).
That narrows any Scout claim around isolation or lifecycle cleanup. Compare
identity isolation, exact-target reattachment, and mutation outcome recovery
as separate capabilities; a named context does not prove that a prior browser
operation committed.

The feature surface is converging in other ways too. Playwright MCP v0.0.84
supports opt-in redaction of selected query and fragment parameter values in
tool responses. Agent-browser v0.40.0 adds Vercel Sandbox MCP hosting and
compact batch output; its documented restore workflow includes isolated
namespaces and validation. These make URL hygiene, session restore, and lower
response volume parity requirements. **Correction from a fresh official-source
check (6 November 2026; corrected 10 November):** Stagehand has two official
Go module lines. The standalone `stagehand-go/v3` repository remains active
and released v3.21.1 on 23 October 2026; the Stagehand monorepo separately
ships Go SDK v4 at `packages/sdk-go/v4`, with a Go SDK 4.3.0 changelog entry
and a protocol-first shared core across TypeScript, Python and Go. Its
documented Go path supports local browser launch and Browserbase,
familiar page methods, AI-guided `Act`/`Observe`/`Extract`, WebMCP and
OpenTelemetry. Go support and local execution are direct competitive parity.
The remaining distinction to test is whether Scout gives a service a more
complete caller-owned execution contract—deadlines, policy, outcome
classification, durable replay guards and host reconciliation—without
requiring Stagehand's agent or Browserbase layers. The SDK capability list
does not establish whether its native task recovery matches Scout's; compare
that directly before claiming an advantage ([Stagehand Go SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go),
[official changelog](https://github.com/browserbase/stagehand/blob/main/CHANGELOG.md),
[v4 Go quickstart](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/quickstart.mdx),
[Playwright v0.0.84](https://github.com/microsoft/playwright-mcp/releases),
[agent-browser v0.40.0](https://github.com/vercel-labs/agent-browser/releases/tag/v0.40.0),
[agent-browser restore workflow](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md)).

**Opportunity update:** the honest three-state decision contract is implemented
in the task-run recovery flow. Go, stateful CLI and MCP can record
`verified_complete`, `rejected`, or `unresolved`; unresolved observations keep
the task ID reserved against replay, append to resolution history, and can later
be followed by a terminal conclusion. The built-in bbolt store persists this
state across process restarts. Custom `TaskRunStore` implementations must also
implement the optional `TaskRunResolutionStore` extension for durable host
attestations; otherwise resolution fails explicitly. The remaining product
opportunity is proof at the application boundary: build a fixture whose
authoritative ledger can answer “committed / not committed / unknown,” and
measure whether a fresh host resolves the task without inspecting Scout
internals or repeating the browser mutation. Report false-success,
duplicate-effect, unresolved-case and host-integration-time rates separately;
keep native peer behavior apart from adapters.

**Policy opportunity update:** the diagnostics contract is implemented across
Go, CLI and MCP: denials carry a stable reason, policy identity and safe target
evidence reference. The remaining problem is enforcement coverage and
operator confidence. Three local runs with Chrome 157 and mitmproxy 12.3.1
observed proxy decisions and zero destination hits for service-worker HTTP,
redirect, subresource, iframe, new-tab and WebSocket cases. A separate one-run
launcher check confirmed HTTP and HTTPS loopback requests reached a deny proxy
and did not reach the origin. These results validate those exact fixtures;
they do not prove all traffic uses the proxy, that DNS/IP rebinding is blocked,
or that behavior holds on remote-CDP and request-recording failure paths. A new
real-process startup probe verifies that valid configuration starts mitmproxy,
while empty and malformed policies exit before opening a listener; malformed
credentials are absent from the startup output. The runner is part of the
integration workflow. Startup evidence is recorded at
`scout-egress-proxy-startup-2026-11-11.json` (local audit artifact).
The next experiment should cover DNS rebinding, direct socket attempts and
remote-browser traffic, then verify both the decision record and destination-
side outcome. Browser Use issue
[#6233](https://github.com/browser-use/browser-use/issues/6233) is a feature
request for destructive-action/egress guard and signed audit records, not proof
of customer demand. Keep browser-origin checks distinct from network egress
enforcement, and validate configuration and incident-diagnosis value with
operators.

**Next experiments:** (1) run the same ambiguity fixture through Scout,
agent-browser, Playwright MCP and Browser Use, with process loss before
dispatch, after dispatch and after verification; (2) attempt exact active-tab
selection with zero, one and multiple focused/visible targets; (3) test policy
denials and proxy failures across navigation, redirect, service worker,
WebSocket, DNS/IP changes and remote-CDP traffic; (4) interview Go platform and
security operators about incident cost and required evidence; (5) require
repeated runs and publish versions, configuration, artifacts and adapter code.
These distinguish outcomes buyers care about from feature-list parity.

### Competitive convergence and the next differentiation test (6 November 2026)

Recent competitor work narrows the defensible claim. Agent-browser now documents
session-to-target binding that survives daemon restarts, with opt-in strict
`--pin-tab` behavior that returns `tab_gone` instead of silently switching to a
neighboring tab. Stable tab IDs and target binding make “we pin the target” a
parity capability, not a unique Scout moat ([current session docs](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md),
[target binding implementation](https://github.com/vercel-labs/agent-browser/blob/main/cli/src/native/browser.rs)).
The open [#1867 report](https://github.com/vercel-labs/agent-browser/issues/1867)
is useful as a pinned-version CDP drift regression, but does not establish a
current general failure.

The remaining #2407 issue reports background-tab closure invalidating active
references and frame scope. Our local reproduction against agent-browser
0.41.2 failed 10/10 after the adapter sequence was corrected to close a truly
unrelated tab; Scout's Go API passed 10/10 and Playwright MCP 0.0.84 passed
10/10 on the same frame postcondition. Scout additionally records stable target
and frame IDs; Playwright's MCP result did not expose target IDs. The outcome
predicate is comparable, while instrumentation and timings are not. PRs and
release changes must be checked before presenting this as a current advantage.
Keep it as a versioned regression fixture, and rerun after a fix lands ([issue #2407](https://github.com/vercel-labs/agent-browser/issues/2407),
[current issue list](https://github.com/vercel-labs/agent-browser/issues)).

Playwright MCP is also actively closing lifecycle and isolation gaps: its
recent release notes include per-client idle timers and multiple attach,
disconnect, and browser cleanup fixes ([release history](https://github.com/microsoft/playwright-mcp/releases)).
Older reports such as [#1343](https://github.com/microsoft/playwright-mcp/issues/1343)
about visible-tab selection should be treated as versioned semantic test cases,
not current defects. Browser Use issue [#5252](https://github.com/browser-use/browser-use/issues/5252)
is likewise an older protocol-error report; later release notes describe
unknown MCP tool calls being returned as application errors. Re-test current
versions before using it in comparisons.

**Native task-recovery catalog check (6 November 2026):** a live, fully
paginated MCP catalog probe inspected agent-browser 0.41.2 (156 tools),
Playwright MCP 0.0.84 (25 tools), and the current Scout worktree (112 tools).
The two peer catalogs expose no explicit task-run/recovery workflow or caller
task-ID field. Scout exposes `task_run`, `get_task_run`, `resolve_task_run`,
and `export_task_recovery_receipt`, each addressed by a stable task ID. The
probe did not issue browser actions and does not establish that host code cannot
compose recovery with either peer. The result narrows the experiment: compare
Scout's native reservation/receipt against explicit caller-orchestrated
reconciliation in the peer versions, and label each adapter layer. See the
catalog report (local audit artifact)
and [probe source](../examples/benchmark/recovery_capability_probe.mjs).
Scout's application-level `task_run` is distinct from MCP's asynchronous Tasks
protocol; this catalog probe does not exercise that protocol in any product.

The durable recovery fixture now also attaches the independent `agent-browser`
0.41.2 runner to Scout-launched Chrome through the launcher's local DevTools
HTTP discovery URL. Before accepting the signed handoff, the fresh Scout process
requires the peer's live `targetId` and page URL to match its own browser
observation. The independent Node receiver also consumes the signed envelope
and checks the target ID and canonical URL directly against agent-browser's
CLI observation, then refuses a full-context match because frame identity is
unavailable. The fresh run is recorded in
`worktree-run-20261106T1936Z.json` (local audit artifact).
Run `SCOUT_AGENT_BROWSER_VERSION=0.41.2 go run
./examples/durable_task_recovery` (or set `SCOUT_AGENT_BROWSER_BIN` to an
installed CLI). This verifies cross-runner target agreement in the example;
the broker/host still supplies owner, browser-generation and policy bindings,
and the peer CLI observation is not authenticated, so this is not an
authenticated third-party proof protocol. The older 30-run
browser-replacement matrix validates Scout's durable task-ID guard and
authoritative reconciliation only; it predates this peer observation and is
not a comparative benchmark. Promote a run to a tracked raw report before
using it as external evidence.

**Differentiation hypothesis:** compete on a runtime-neutral, durable outcome
receipt and safe recovery handoff, not target pinning. A receipt should bind a
caller task/action ID to browser and target generation, frame, policy digest,
dispatch state, observed postcondition, evidence timestamps, and a classified
result (`verified`, `rejected`, or `unknown`). It must survive process loss,
redact secrets, and be consumable by another host/runtime without implying
that the action is replayable. The receiving host reconciles against its
authoritative application state and supplies any idempotency key; the browser
runtime reports evidence and uncertainty. Scout has a durable, non-replayable
task plan and task-ID reservation machinery, but portability across runtimes
and a stable public receipt schema remain hypotheses to validate.

**Next experiment:** run the same delayed mutation across Scout, agent-browser,
Playwright MCP, and Browser Use. Kill the tool process at pre-dispatch,
post-dispatch/pre-response, and post-verification/pre-commit boundaries;
restart with a fresh process and, separately, a replaced browser. Grade each
tool on duplicate effects, false success, correct `unknown` classification,
receipt portability/redaction, host reconciliation steps, and recovery time.
Keep native facilities and adapter-built behavior in separate columns. Require
at least 30 runs per boundary before comparing rates; a small smoke test only
validates the harness. This turns the opportunity into a falsifiable buyer
benefit: safe continuation after interruption with less bespoke recovery code.

### Competitor recovery semantics recheck (6 November 2026)

The current Playwright MCP guide says its default persistent profile preserves
login state between server sessions; isolated sessions start fresh and can be
seeded with `--storage-state`. Its idle policy closes server-owned headless
browsers after an hour by default, allows an explicit timeout for other modes,
and says attached CDP browsers are left open by default. The per-client idle
timer for isolated HTTP clients is now documented separately from the shared
context timer. ([Playwright MCP profile and lifecycle docs](https://github.com/microsoft/playwright/blob/main/docs/src/getting-started-mcp.md))

Playwright MCP's opt-in `browser_resume` is specifically for resuming paused
script execution; it is not a durable browser-session resume handle. The
current release notes mention a path-parsing fix for that debugger capability,
not browser-target recovery. ([Playwright MCP capability docs](https://playwright.dev/mcp/introduction#developer-tools), [release notes](https://github.com/microsoft/playwright-mcp/releases))
Playwright issue [#41617](https://github.com/microsoft/playwright/issues/41617)
describes the first-call failure after a remote CDP disconnect and an agent
losing its logged-in context; the reporter later clarified a retry succeeds,
and the issue was closed as completed. Treat that as a recovery-UX regression
fixture, not an assertion of a current defect.

The current agent-browser issue queue identifies separate competitive
pressures: CDP attach can take about six seconds with multiple discarded tabs
([#2431](https://github.com/vercel-labs/agent-browser/issues/2431)); a tool
argument may override the server-pinned MCP session ([#2422](https://github.com/vercel-labs/agent-browser/issues/2422), with a proposed, unmerged
[PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430)); and direct
page connections may miss console/network/HAR/trace events ([#2293](https://github.com/vercel-labs/agent-browser/issues/2293)). Its current
release history also shows broad growth in provider plugins, auth/state
management, WebMCP, profiles, and dashboard features. This means Scout's
advantage must be measured as lower attach and recovery cost plus stronger
owner/target attribution, not raw tool count.

**Updated product implication:** compare four distinct outcomes in the scorecard:
(1) login-state persistence across a server restart, (2) reconnect to the exact
live target without losing page state, (3) recovery after the browser itself
restarts, and (4) safe diagnosis after a disconnect. A persistent profile may
restore cookies but does not prove exact-target/page recovery; Scout's target
reattachment design must not claim browser-restart recovery unless a live
target and matching browser fingerprint are verified. Run paired tests with
the same login fixture and measure recovery rate, setup steps, attach latency,
and the first useful action after restart.

### MCP stateless transport compatibility (6 November 2026)

The current MCP revision (2026-07-28) removes transport session IDs and the initialize/ping lifecycle in favor of per-request `_meta`; `server/discover` replaces initialization when clients want server discovery, and Streamable HTTP also requires the `Mcp-Method` routing header. The spec says a lost response stream is not cancellation, so a disconnected caller cannot safely imply that browser work should be torn down ([2026-07-28 transport specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports), [2025-11-25 transport specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)).

At the initial assessment, Scout used stateful Streamable HTTP exclusively and relied on `Mcp-Session-Id` for named browser sessions, snapshots, result artifacts, and principal-scoped limits. The pinned `mcp-go` v1.30.1 supplies no transport client ID in stateless mode, so changing the option alone would merge those owner scopes. The library rejects `server/discover` in stateful mode when the session store is enabled, because every non-initialize request needs a prior session ID. Modern `clientInfo` metadata identifies a client implementation, not an authenticated caller, so it cannot safely replace that key. MCP requires application state spanning requests to use an explicit identifier the client supplies on each request; `server/discover` is optional in the protocol, though Scout's dependency implements it ([statelessness rules](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/index.mdx#statelessness)).

The dependency selects stateful versus stateless behavior once on the HTTP transport (`WithStreamableStateful` / `WithStreamableStateless`); its request-context hook runs within the selected path and cannot negotiate per request. It exposes a public stateless transport option, so a single stateless HTTP mode is possible without a custom router. A hybrid stateful/stateless endpoint would still need a transport-level mode resolver (initialize-era request → legacy session path; modern request → stateless path) plus a Scout owner key carried explicitly on each modern call. Scout now has an opt-in stateless prototype with principal authentication and an opaque handle in per-call `_meta`; this addresses the initial ownership gap for a single stateless mode, while hybrid mode and production acceptance remain open.

**Stateful session-selection progress:** browser tool calls can now select an existing named session using `_meta.go.klarlabs.de/scout/session`. The lookup is scoped to the authenticated client's existing registry, checks that the named session exists and is not closing, and binds both the session and active target for the duration of the call; `status` supports the same selector without changing the active session. Registry/configuration tools reject the selector. This improves stateful multi-session ergonomics; the later stateless prototype uses an opaque owner-scoped session handle, while the shared-remote-browser boundary remains.

**Opaque handle progress (6 November 2026):** `create_session` and `list_sessions` now return a random 192-bit `session_id`; `_meta.go.klarlabs.de/scout/session` accepts either that handle or a legacy name, and switch/close accept handles as well. Resolution stays inside the authenticated HTTP transport/principal registry (or stdio process); foreign IDs return the same not-found result as unknown IDs. Close, idle expiry, and HTTP DELETE revoke the handle. At this stage the registry remained in memory, so process restart invalidated it; reconnectable durable ownership and stateless request support were unimplemented. The `mcp-explicit-session-routing` live benchmark exercises concurrent same-transport routing by ID, isolated cookie/storage, and selectorless default compatibility. See the later [stateless implementation update](#mcp-stateless-transport-compatibility-6-november-2026) for current prototype status.

**Acceptance evidence (6 November 2026):** the `mcp-explicit-session-routing` benchmark sends two concurrent selector-bearing navigation calls over one stdio transport. They return distinct targets; extraction confirms the seeded primary cookie/localStorage appears only in the left context while the right starts empty; a selectorless status call confirms the implicit default session remains unstarted. This verifies the stateful selector’s same-transport routing and mixed explicit/implicit behavior. It does not establish reconnect-by-ID, authenticated cross-principal rejection for opaque IDs, or stateless lifecycle coverage.

The protocol matrix to close is concrete: (1) stateful initialize-era client continues to create, use, and delete an isolated owner scope; (2) modern discovery, when requested, returns without a transport session; (3) modern calls require valid per-request `_meta` plus `Mcp-Method`; (4) calls from two authenticated logical clients cannot see each other's named sessions or private artifacts, even when they share a principal; (5) stream disconnect leaves a dispatched mutation classified as uncertain until reconciliation; (6) explicit cancellation and idle cleanup remain distinct. Exercise both a single bearer-token configuration and principal-based auth. Do not mark modern support complete from a successful `server/discover` response alone.

**Tracker signal:** MCP C# SDK issue [#1777](https://github.com/modelcontextprotocol/csharp-sdk/issues/1777) requested a hybrid endpoint for stateful initialize clients and sessionless 2026-07-28 clients; it was closed as completed through merged PR [#1796](https://github.com/modelcontextprotocol/csharp-sdk/pull/1796), which introduced `StatefulForInitializeClients`. That demonstrates demand for progressive migration and a shipped competitor implementation. Protocol PR [#2822](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2822) proposed client-owned `Mcp-Client-Session-Id` correlation across HTTP and `_meta`, but was closed without merging after maintainers said it runs against the protocol's sessionless direction. It is not a standardized field. **Opportunity:** validate Scout's opt-in authenticated logical-context handle and assess whether target clients need a hybrid endpoint. Bind every handle to authenticated identity and preserve per-context isolation; do not treat a caller-selected ID as authorization by itself.

**Inference:** modern stateless support may be a near-term adoption opportunity for some MCP clients, but buyer demand is unknown. Scout now defines a stable authenticated owner for its opt-in prototype. Chrome-backed HTTP integration verifies snapshot isolation across principals, reconnect continuity, explicit-close cleanup, and durable task-ID isolation across principals using the same caller ID. Repeated concurrency, same-principal session isolation, restart recovery, idle cleanup, and target-client compatibility remain unproven. Immediate disconnect cleanup would violate the protocol's ambiguity model; prefer explicit cancellation where supported and bounded idle cleanup after abandoned state.

### Competitive refresh: stateless MCP makes application state explicit (10 November 2026)

The 2026-07-28 MCP revision and the finalized sessionless SEP move state
continuity out of transport sessions: clients carry protocol metadata on each
request, while stateful applications pass an explicit server-minted handle
through later calls. The SEP calls out two practical risks: a model may lose or
misstate an opaque ID, and a handle that appears in chat history must still be
authorized against the authenticated caller. Its example carries state handles
as ordinary tool arguments. The SEP also says the protocol does not currently
type or formally link a handle returned by one tool to an input accepted by
another; clients infer the relationship from tool names and descriptions.
([2026-07-28 sessionless SEP](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/seps/2567-sessionless-mcp.md), [statelessness rules](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/index.mdx#statelessness))

Adoption is uneven across adjacent clients and SDKs. The official MCP C# SDK
documentation recommends stateless HTTP by default and says the 2026-07-28
revision removes both `Mcp-Session-Id` and the initialize handshake. By
contrast, Microsoft Agent Framework's Python client tracks an open feature
issue for supporting that revision alongside older servers; the issue
describes its current implementation as handshake-era. This is evidence of a
transition and compatibility burden, not evidence that browser competitors
lack support or that users have adopted the new mode. ([C# stateless guidance](https://github.com/modelcontextprotocol/csharp-sdk/blob/main/docs/concepts/stateless/stateless.md), [Agent Framework issue #8245](https://github.com/microsoft/agent-framework/issues/8245))

**Product opportunity:** make Scout's application-level handles survive the
transport change cleanly. A session selector must be caller-carried, bound to
authenticated identity, discoverable in tool schemas and examples, and safe to
retain across context compaction or worker replacement. Durable task identity
must remain distinct from browser-session identity, and neither handle should
authorize replay. This makes explicit state management an integration
contract rather than a metadata convention. The current stateless prototype
uses an opaque Scout session ID in `_meta`, rather than as an ordinary
tool-argument field. It therefore does not yet follow the handle-passing shape
described by the SEP or prove that target clients preserve the selector. It
also does not prove that task/artifact namespaces remain isolated or that
handles can be resumed safely after server restart or load balancing.
Stateless owner scope now ignores any `Mcp-Session-Id` header and is derived
solely from the authenticated principal; a stale transport header cannot split
the principal's session registry or make an otherwise valid opaque handle
unresolvable.

**Next comparison:** on pinned modern and initialize-era clients, measure
whether each client can discover tools, carry the returned session handle,
survive a context reset with a compact recovery record, and reconnect after a
worker or server replacement. Include same-principal logical clients, foreign
principal denial, task-result reuse, artifact isolation, and idle cleanup.
Compare against a stateful MCP server and a pure stateless tool server; record
client support friction separately from browser correctness. Do not make
stateless support the lead message until target-client demand and handle
continuity are observed. First prototype an additive explicit `session_id`
argument on browser-scoped calls and measure schema clarity and compatibility;
retain `_meta` as a transport-level compatibility path only if clients require
it, with conflicting argument and metadata selectors rejected. Implementation
inspection narrows this work: Scout's pinned MCP library generates closed
JSON Schemas for struct inputs and validates arguments before handler dispatch.
A middleware-only alias would neither advertise the field nor pass validation.
The field must be added to each browser-scoped input schema, or Scout must
design a separate scoped-call surface; do not weaken schema validation globally
to make the selector fit.

**Implementation progress — portable recovery handoff (6 November 2026):** interrupted `get_task_run` records now include a versioned, redacted `plan` describing action names, selectors, postcondition kinds, target, deadline, and policy identity without action inputs or expected values/text. The built-in bbolt store atomically binds browser/target identity, this plan, and the initial not-yet-dispatched cursor before any browser action. The plan is explicitly descriptive and cannot be replayed. A focused store check verifies it survives close/reopen, while a redaction check confirms secret input and expected text are absent. This makes task context portable to another host for reconciliation; host-owned authoritative application evidence and task-ID reservation still remain necessary to safely resolve the outcome.

**6 November tracker recheck:** agent-browser issue [#2422](https://github.com/vercel-labs/agent-browser/issues/2422) remains open, while proposed [PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430) is also open and contains a fix that scopes tool-provided sessions under the server's pinned `AGENT_BROWSER_SESSION`. Its latest commit added a guard for a `--session` flag smuggled through command arguments. The PR has not merged into `vercel-labs/agent-browser/main` and is not evidence about a released version. Keep the existing 0.41.2 cross-server leak as a pinned-version regression fixture; do not treat session override isolation as a durable Scout lead.

**Target-drift research refresh (6 November 2026):** agent-browser issue [#1867](https://github.com/vercel-labs/agent-browser/issues/1867) reports silent drift in a long-lived CDP-attached Edge session: viewport changes may return success without changing the measured viewport, and later reads/actions may run against an unrelated tab. The report is open and version/environment specific, not a rate or proof that the current release has the behavior. It sharpens the opportunity from stale-reference rejection to end-to-end target attribution: verify the target and read back state for every operation that can silently succeed. Scout already reads back viewport metrics and accepts target IDs on page-scoped MCP operations; benchmark these together under external tab activation/closure, and assert target ID, URL, viewport, and resulting page state before accepting success. The same tracker lists CDP attach delays when tabs are discarded (#2431) and notes a guard against server-pinned session override in proposed PR #2430; these point to startup cost and ownership as separate benchmark dimensions ([current issue list](https://github.com/vercel-labs/agent-browser/issues)). Stagehand's open [#3371](https://github.com/browserbase/stagehand/issues/3371) reports `activePage()` docs selecting the oldest tab rather than the active tab, reinforcing the need to distinguish browser-active state from a tool's selected target. **Inference:** Scout's strongest near-term proof is a wrong-target/false-success benchmark, not a claim that its target features are unique.

**Latest competitive signal recheck (6 November 2026):** Playwright MCP v0.0.84 adds named isolated contexts in one browser, bounded `browser_find`, URL value redaction, and retained network history; this release also fixes isolated-client idle cleanup and shared-browser disconnect behavior ([v0.0.84 notes](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.84)). Agent-browser documents persistent restore keyed by a stable session ID and strict tab pinning for shared CDP, but session IDs alone do not persist state ([session management](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md)). Browser Use's latest issues request active-tab connection management (#6296) and same-session agent retry (#6295) ([issue tracker](https://github.com/browser-use/browser-use/issues)); these are workflow requests, not estimates of prevalence. Stagehand's active v4 monorepo includes a first-party Go SDK, local browser support, direct page methods and optional model-backed agent actions ([Go SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go), [quickstart](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/quickstart.mdx)). **Inference:** session persistence, isolation, context-bounded discovery, and Go support are converging capabilities. Scout's sharper opportunity is to make *application outcome state and safe next action* portable across interfaces and process restarts, then prove lower duplicate-side-effect and false-success rates on a matched fault-injection benchmark.

**Recovery feature recheck (6 November 2026):** agent-browser's current session docs describe `--restore` as auto-saving/restoring cookies and localStorage under a stable key, including periodic saves and validation checks that can prevent a bad restore from overwriting good state ([session management](https://agent-browser.dev/sessions), [restore reference](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md)). This is meaningful state continuity and must be included in any comparison; it does not by itself establish whether a particular submitted operation committed. Playwright MCP issue [#1599](https://github.com/microsoft/playwright-mcp/issues/1599), about a stale renderer-crashed page reference, is closed through [microsoft/playwright#40617](https://github.com/microsoft/playwright/pull/40617), so it should no longer be presented as a current known gap without a released-version reproduction. The separate CDP disconnect report [#41617](https://github.com/microsoft/playwright/issues/41617) was also closed after the reporter found a subsequent tool call could recover; the report still describes a failed first call and possible loss of page/session context, but does not establish a current defect. **Inference:** compare three independent outcomes—browser/session restoration, application-side mutation classification, and safe continuation. Scout's task record addresses the latter two; competitor adapters should receive equivalent access to the fixture's authoritative server record and may use their documented restore/reconnect behavior.

### Competitive research refresh: target attribution and host integration (6 November 2026)

The newest issue scan reinforces two buyer problems worth testing. Agent-browser [#1867](https://github.com/vercel-labs/agent-browser/issues/1867) is an open report of silent target drift in a long-lived CDP-attached Edge workflow: commands returned success while viewport readback and later page reads indicated a different target. Browser Use [#6296](https://github.com/browser-use/browser-use/issues/6296) asks MCP tools to attach to an already active browser session rather than create a new tab on first use. These are individual reports, not frequency or current-version defect evidence. Playwright MCP [#1795](https://github.com/microsoft/playwright-mcp/issues/1795) is a useful counterexample: its reported idle-process leak was addressed by merged [PR #43527](https://github.com/microsoft/playwright/pull/43527), so it should now be treated as a lifecycle regression scenario, not an incumbent weakness. Browser Use [#6278](https://github.com/browser-use/browser-use/issues/6278) reports an MCP client crash on valid JSON Schema type arrays, a concrete interoperability edge case to include in schema compatibility checks.

**Product inference:** the opportunity is not “Scout has tabs” or “Scout has Go.” Competing products already offer broad tab/session control, restore, profiles, and Go access. Scout can be better for service teams if each browser call is attributable to an owner and exact target, reads back the relevant state before claiming success, and carries a typed recovery path across process boundaries. Scout already has request-scoped target routing, viewport readback, target-bound action receipts, principal-scoped contexts, and owner-bound durable handles for a live remote target. These are implementation ingredients, not proof of superiority. The next matched experiment should deliberately switch/close tabs during delayed calls and compare the intended target ID, URL, viewport, mutation count, receipt, and recovery decision across current pinned releases. Separately compare MCP schema compatibility using nullable unions and nested/array types, and measure whether a first-use workflow can attach to an existing session without manual tab repair.

| Signal | Evidence status | Scout opportunity | Fair validation |
|---|---|---|---|
| Silent target drift | Open agent-browser user report, one environment | Extend target attribution/readback contract to every success result | Induce active-tab changes and unrelated-tab closure during concurrent and delayed operations; independently grade target and page state |
| Existing-session MCP attachment | Open Browser Use feature request | Make attach-vs-launch explicit and preserve caller-owned browser state | Fresh browser with authenticated existing tab; compare setup steps, exact target retention, and side effects |
| Isolated-session idle cleanup | Playwright report fixed by merged PR #43527 | Keep Scout's lifecycle contract measurable and guard against regression | Keep HTTP stream alive without calls, include an in-flight call, then assert cleanup timing and process ownership |
| JSON Schema interoperability | Open Browser Use report for `type: ["string", "null"]` | Ensure Scout's tool schemas remain consumable by strict and permissive MCP clients | Round-trip union, nullable, nested object, and array schemas through at least two MCP SDKs |

Avoid turning issue counts into claims. Retest each scenario against the release being compared and publish raw outcomes; only then promote a Scout advantage.

### Competitive opportunity refresh: policy integrity and truthful outcomes (6 November 2026)

A fresh issue and PR scan suggests the most useful next research track is
**policy integrity under alternate dispatch paths**, paired with **truthful
failure classification**. This is a sharper security claim than counting
policy controls or successful browser calls.

* Agent-browser issue [#2172](https://github.com/vercel-labs/agent-browser/issues/2172)
  is an open, reproducible report against its daemon JSON-line boundary: a
  `javascript:` navigation executes script while the `evaluate` action is
  denied. The reporter's workaround is to deny navigation too, which blocks
  the demonstrated request. The report explicitly does not show that the same
  spelling works through the CLI. Treat this as a versioned authorization
  regression case, not evidence that every interface or release is vulnerable.
* Browser Use issue [#6233](https://github.com/browser-use/browser-use/issues/6233)
  proposes an invariant action guard for credential-exfiltration destinations,
  destructive DOM actions, secret scrubbing and signed audit records. It is an
  open feature request with a third-party implementation link, not a merged
  Browser Use capability or evidence of customer prevalence.
* Browser Use issue [#6278](https://github.com/browser-use/browser-use/issues/6278)
  reports that a valid JSON Schema union such as `type: ["string", "null"]`
  crashes MCP tool registration in the reported version. This is a concrete
  interoperability probe; check its status and released fix before ranking
  current support.
* Playwright issue [#42693](https://github.com/microsoft/playwright/issues/42693)
  recorded an idle timer closing a browser during a long-running tool call. It
  was closed as not planned, so preserve it only as a regression test idea and
  check current releases. In contrast, the isolated-session leak in
  [#1795](https://github.com/microsoft/playwright-mcp/issues/1795) was fixed by
  merged [PR #43527](https://github.com/microsoft/playwright/pull/43527); do
  not market that issue as an active weakness.
* Browser Use [#6296](https://github.com/browser-use/browser-use/issues/6296)
  requests attaching MCP tools to an already active browser session. Its
  discussion reports an environment where no unique focused or visible page
  could be identified. The valuable product behavior is therefore explicit
  attach ownership with a clear ambiguity error, not guessing which tab is
  “active.”

**Opportunity for Scout:** turn policy into an executable, auditable contract
that composes across transports. Feed semantically equivalent requests through
Go, CLI, MCP stdio and HTTP, plus any lower-level daemon/extension route; include
`javascript:` URLs with casing/whitespace variants, redirects, new tabs,
iframes, and policy reload/failure. Record the selected target, effective
policy ID/digest, decision reason, whether dispatch occurred, independent page
and destination side effects, and the final classified outcome. A deny is a
pass only when the side effect did not occur; an error after a side effect must
not be reported as “not executed.” Pair this with the existing proxy and
recovery fixtures so network egress policy and browser action authorization
remain separate layers.

Scout already has policy diagnostics and target-bound recovery evidence, and
the existing egress experiments cover selected browser traffic paths. Those
implementation facts are not proof that all entry points enforce one policy
semantics. The CLI's one-shot actions, stateful JSONL session, `watch`, `pipe`,
`record`, and `reconcile` now block private and loopback addresses by default;
`--allow-private-ips` is an explicit opt-in for trusted local-development
targets. MCP already defaults to the same block unless configured otherwise.
This closes the unconditional private-address bypass in local CLI entry points,
but does not prove parity across every action or egress channel. The next
release-grade experiment is a cross-interface policy
conformance matrix with hostile inputs, a deliberately failing policy source,
and a delayed mutation at each dispatch boundary. Require zero denied side
effects, no false success, no policy fail-open on parse/reload errors, and a
portable decision record that another host can inspect. Repeat on pinned
competitor releases and separate native support from harness adapters.

**Priority adjustment (inference):** move cross-interface policy conformance
ahead of expanding browser verbs or session ergonomics. The market already has
tab pinning, restore, Go SDKs and named contexts converging toward parity. A
demonstrable guarantee that a denied effect cannot slip through a less obvious
transport—and that uncertain outcomes remain honestly uncertain—would better
fit Scout's service-operations position. Validate operator value with security
and platform engineers before elevating this to a commercial moat.

### Strategic opportunity refresh: sell dependable execution for services (6 November 2026)

The current market makes several former differentiators poor bets. Playwright MCP v0.0.84 has named isolated contexts, URL parameter redaction, WebMCP integration, recording, multiple browser engines, and extensive optional capabilities ([release notes](https://github.com/microsoft/playwright-mcp/releases), [configuration docs](https://github.com/microsoft/playwright-mcp)). Agent-browser's recent releases add sandbox helpers, a growing plugin system, persistent state restore, strict tab binding, and capability profiles ([release history](https://github.com/vercel-labs/agent-browser/releases), [changelog](https://github.com/vercel-labs/agent-browser/blob/main/CHANGELOG.md)). Stagehand's official Go SDK combines direct page methods with AI-assisted `act`/`observe`/`extract`, local and Browserbase browser paths, and a shared cross-language core ([Go SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go)). Go support, MCP, session persistence, tool profiles, and broad browser verbs are therefore parity areas, not a Scout moat.

#### Stagehand Go comparison boundary (6 November 2026)

The official Stagehand v4 quickstart provides two valid comparisons that must
not be blended. Its local-browser `act`/`observe`/`extract` flow asks the host
to provide a model and API key, while the Go SDK also exposes direct browser
page methods. The deterministic track should compare Scout's Go API with
Stagehand Go's direct page methods on the same local fixture, with no model
calls in either path. The agentic track should use the same model/provider,
prompt, browser, and acceptance predicate, and report model cost separately.
For both tracks, interrupt after a server-side mutation commits and before the
caller receives a verified result; then compare status classification,
target-bound evidence, replay protection, host reconciliation steps, and
duplicate effects. Keep setup steps, compilation/runtime dependencies, and
cleanup as separate integration outcomes. This will test whether Scout's
host-owned task/recovery contract is valuable over Stagehand's richer
agent-oriented surface without treating Go support, local browser control, or
AI use as an advantage by themselves ([Stagehand Go quickstart](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/quickstart.mdx),
[SDK source](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go),
[Go SDK changelog](https://github.com/browserbase/stagehand/blob/main/CHANGELOG.md)).

Public issue trackers expose a more useful set of buyer problems, with important limits: they are individual reports and feature requests, not prevalence data. Browser Use has open requests for attaching to an already-active browser (#6296) and letting its retry agent continue an existing MCP browser session (#6295), plus open reports about JSON Schema type-array registration (#6278) and a security profile reverting to allow-all when configuration parsing fails (#6218) ([issue tracker](https://github.com/browser-use/browser-use/issues)). Stagehand has an open report that `activePage()` documentation selects the oldest tab rather than the active tab (#3371), and a report that domain policy defaults to `null` and allows traffic until configured (#3002) ([issue tracker](https://github.com/browserbase/stagehand/issues)). The tracker also shows ongoing fixes and old reports; none of these should be described as current product-wide defects without release-level reproduction. The useful demand signal is for explicit ownership, fail-closed policy, honest outcomes, and recovery that stays in the same browser context.

**Recommended product position:** make Scout the browser execution runtime a service can trust and embed. Its promise should be: *every mutation is tied to a caller, task, browser target, policy, deadline, and observed postcondition; when the result cannot be proved, Scout preserves uncertainty and helps the host reconcile without replaying the side effect.* This uses Scout's Go embedding, no-inference deterministic path, CLI/MCP parity, bounded output, durable task reservations, append-only resolution history, and portable recovery receipt as one coherent story. The value is less bespoke glue and fewer duplicate or falsely reported business actions—not a longer tool catalog. This remains a hypothesis until the matched fault-injection benchmark measures the benefit against native competitor workflows.

| Investment | Why it can win | Evidence to collect before claiming leadership |
|---|---|---|
| **Outcome handoff** — stable, redacted receipt and reconciliation contract across process restarts and host runtimes | Browser state restore is increasingly common; it does not answer whether a submitted business mutation committed. Competing retry and recovery workflows often sit beside the current session rather than continuing through one task record. | Run the same authoritative ledger fixture through Scout and native competitor workflows at pre-dispatch, post-dispatch, and post-verification crashes. Report false success, duplicate effects, `unknown` accuracy, recovery time, host adapter code, and terminal/append-only history. |
| **Ownership and policy that fail closed** — caller-scoped contexts, stable denial reasons, and auditable policy decisions | Recent issue reports point to confusion around active target ownership, sessions, and policy defaults. Scout now has five fresh HTTP runs showing two authenticated principals isolated on one shared endpoint, and three proxy fixture runs with explicit destination decisions across service-worker HTTP, redirects, subresources, iframes, new tabs, and WebSockets. These are specific local contract checks, not coverage of every browser egress path or deployment. | Extend the principal matrix to credential rotation/revocation, aliased endpoints and concurrent target churn. Extend egress testing to proxy failure, DNS/IP rebinding, OS traffic and remote CDP. Verify destination-side outcomes as well as reason-coded decisions. |
| **Predictable Go service integration** — local deterministic execution, composable middleware, bounded resource behavior, and provider-independent core | Stagehand v4 has an official Go SDK and direct page APIs, so Go support is parity. Its local AI `act`/`observe`/`extract` quickstart requires a model and API key; a deterministic page-method comparison should exclude model use in both products, while an agentic comparison must pin the same provider/model and report its cost. | Compare clean setup, glue code, warm action latency, cancellation/deadline propagation, process/browser cleanup, CPU and peak memory, and failure diagnosis. Run separate deterministic Go API and agentic model-backed tracks. Include Playwright Go/CLI only as a separately labeled alternative. |
| **Proof-led distribution** — benchmark artifacts that explain exactly what passed and what was measured | Competitors are shipping rapidly; release-number feature claims age quickly. A reproducible scorecard gives technical buyers a way to trust Scout's narrower promise. | Keep pinned versions, fixture hashes, traces, scripts, raw repetitions, and explicit measurement scope. Use at least 30 repetitions for failure-rate comparisons and distinguish native behavior from adapters. |

**Do next:** prioritize the public recovery receipt and a native cross-runner fixture; publish the benchmark protocol with raw artifacts; then test Go service integration time and operational cost with real host applications. Do not spend the next product cycle matching more generic browser verbs, another session profile, or a model-driven agent surface until buyer interviews or task-level evals show that these limit adoption. The current evidence is stronger than a single-run proof for selected contracts: five fresh HTTP principal-isolation runs, three proxy policy runs, and repeated Scout-only recovery probes exist. Still missing are a 30-run comparative ambiguity study, independent customer integration study, and scale/cost benchmark. Keep the position as a testable strategy rather than a superiority claim.

**Agent discovery and evaluation update (6 November 2026):** agent-browser's optional `@agent-browser/eve` adapter advertises 42 namespaced tools and says its names, descriptions, and schemas are sourced from the matching CLI catalog. The repository's EVE docs describe a 28-tool always-loaded core with 14 tools deferred behind search; treat this as a separate adapter surface, not the standard MCP default. Its eval suite measures context footprint and agent behavior across skill loading/selection, command use, and reference routing, with model/provider selection and optional LLM judging ([EVE adapter](https://github.com/vercel-labs/agent-browser/blob/main/packages/%40agent-browser/eve/README.md), [eval suite](https://github.com/vercel-labs/agent-browser/blob/main/evals/README.md)). Scout's profile catalog and filtered `help` recipes are mechanically consistent, but no model-driven tool selection or task-success comparison has been run. The differentiator remains unproven until Scout measures task completion, actual prompt/tool tokens, wrong tool selection, retries, and latency on pinned tasks and models.

**Fresh competitor scan (6 November 2026):** agent-browser's MCP now defaults to a `core` tool profile and groups extra tools into `network`, `state`, `debug`, `tabs`, `react`, and `mobile`; this makes curated MCP surfaces a direct parity feature, not a unique Scout claim ([profile docs](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/commands.md)). Playwright MCP offers browser and capability selection, CDP attach, allowed/blocked origins, isolated or persistent profiles, and output-file retention controls ([configuration](https://github.com/microsoft/playwright-mcp)). Browser Use now explicitly offers a two-tool CLI-oriented MCP for persistent direct browser control, with a configurable 100,000-character output cap; its full server separately offers session lifecycle tools ([MCP docs](https://github.com/browser-use/browser-use/blob/main/docs/MCP_SERVER.md)).

### Opportunity research: compose agent workflows around Scout-owned evidence (6 November 2026)

Recent Browser Use MCP requests describe two workflow gaps: its MCP tool flow may create a new tab rather than attach to an already active browser session ([#6296](https://github.com/browser-use/browser-use/issues/6296)), and its autonomous retry helper cannot continue the same session already used by structured MCP tools ([#6295](https://github.com/browser-use/browser-use/issues/6295)). A draft [PR #6305](https://github.com/browser-use/browser-use/pull/6305) adds opt-in active-tab selection on initial CDP connection, with ambiguity errors and no fallback tab; contributors reported headless integration runs passing, while headed active-tab discovery remained unresolved in their environment. These are active implementation signals, not evidence of demand prevalence. Scout already attaches to an externally owned browser via `SCOUT_REMOTE_CDP`, reuses its active page on the default remote session, and exposes target/session identity. That makes session continuity a parity check to validate, not a new roadmap bet.

**Recovery tracker recheck (12 November 2026):** issue [#6295](https://github.com/browser-use/browser-use/issues/6295) remains open. Its linked [PR #6298](https://github.com/browser-use/browser-use/pull/6298) is also open and GitHub reports it as dirty/not mergeable. The proposal adds `same_tab` recovery, reusing the active browser session while preserving domain policy and serializing retries. The PR description reports unit and workflow checks, while its local real-browser integration attempt did not complete because Chromium failed to start. Treat this as active product work, not a released Browser Use capability. It reinforces that same-session recovery is converging; Scout's opportunity is the durable operation record and evidence-based outcome resolution that can cross process restarts. Benchmark both behaviors separately: browser-context continuity, then whether a host can classify an uncertain mutation without replaying it.

Agent-browser is ahead of Scout on persistent browser-session ergonomics: `--restore` periodically saves browser state, while `--pin-tab` persists CDP target binding across daemon restarts and reports `tab_gone` instead of silently switching tabs ([session management docs](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md)). Scout should not position target pinning or session restore as unique. Its differentiating layer is a durable *application task outcome*: action-level dispatch cursor, a no-replay interrupted reservation, and an explicit host reconciliation record. Playwright MCP's renderer-crash report (#1599) was fixed by merged [PR #40617](https://github.com/microsoft/playwright/pull/40617), which replaces the crashed tab on the next call and surfaces the event. Treat it as a regression case, not an active competitor defect. **Inference:** the defensible comparison is now browser-context recovery versus business-operation recovery, measured by whether a host can determine if a mutation committed without duplicating it.

The more promising opportunity is **composition with clear ownership and handoff**: allow a host application to use deterministic Scout actions, preserve a durable task record, and then hand the same target plus bounded evidence to a different agent/tool for recovery without losing the authoritative state or accidentally repeating a mutation. Scout's `task_run` and durable store address same-plan replay across Scout restarts, but the completed-task recovery example keeps Chrome and the target alive. The open product question is whether task identity, action receipts, policy identity, and reconciliation hints can be exported/imported or consumed by another runner while maintaining a single owner for browser mutations.

Current security reports reinforce the need for explicit ownership and fail-closed configuration. Browser Use issue [#6218](https://github.com/browser-use/browser-use/issues/6218) reports that malformed `config.json` may be replaced with defaults that allow all; Stagehand issue [#3002](https://github.com/browserbase/stagehand/issues/3002) reports a null domain policy that continues requests until policy is configured; Stagehand issue [#2945](https://github.com/browserbase/stagehand/issues/2945) reports service-worker and WebSocket paths outside its domain-policy interception. These reports are version/commit-specific and should be reproduced before comparison. Scout currently fails startup on invalid principal/origin policy and documents that page-target origin checks do not cover service-worker requests or WebSocket handshakes; the differentiator is making enforcement scope visible in each receipt and supporting an external network boundary where needed.

**Research questions to answer before building more:**

1. Do Go teams need a portable task/recovery record across Scout process restart only, or across different browser tools too? Interview 5–10 teams and observe how they handle unknown mutation outcomes.
2. Can Scout's record be a stable interop format without exposing secrets or page data? Prototype export/import with schema version, policy digest, browser/context fingerprint, target ID, operation state, postcondition, and artifact references; reject mismatched identity and never replay a mutation automatically.
3. Can one common fault-injection harness compare Scout, Playwright MCP/CLI, Stagehand Go, agent-browser, and Browser Use on the same target and side effect? Pin versions and separately report native capabilities, adapters, and unsupported cases.
4. Does explicit ownership and recovery reduce duplicate side effects and operator recovery time enough to justify the added setup? Measure before positioning this as the lead differentiator.

**Immediate next step:** extend the recovery pilot to at least 30 repetitions per tool and crash boundary, add repeated Scout full-browser-loss trials, and test peer native task-ID guards and outcome classification. The pilot now deliberately repeats the same harmless fixture click after restart: agent-browser and Playwright MCP both dispatch a second side effect, showing that UI-level blind retry duplicates work. This does not test either host's own idempotency facility because no task key was supplied. Scout's separate durable task store blocks a same-ID retry after Chrome replacement and allows reconciliation from authoritative application evidence. A corrected isolated application-key UI probe passed for both agent-browser 0.41.2 and Playwright MCP 0.0.84: each produced one effect, then suppressed a repeated submission under the same page-provided key. The earlier Playwright 0-request result came from sharing the fixture's key store across adapters; the second tool correctly reused the first tool's key. The runner now restarts the fixture between adapters, and the current result establishes application-idempotency parity only (artifacts (local audit artifact)). Preserve raw runs and report session restore, blind replay effect, native guards, and classification separately.

**Adjacent contract signal:** the current agent-desktop JSON output contract uses explicit delivery states (`not_delivered`, `delivery_uncertain`, `delivered_unverified`, `delivered_verified`) and derives a machine-readable `safe` / `unsafe` retry disposition; it also returns partial snapshots with explicit completeness markers ([contract](https://github.com/vercel-labs/agent-desktop/blob/main/docs/json-output.md)). This is a desktop automation project, not a direct browser competitor, so it is a design signal rather than browser parity evidence. Scout's portable recovery receipt separates `dispatch`, `outcome`, and `verification`, and hard-codes `replay_authorized: false`. Because even a verified browser predicate does not prove that the host's business ledger committed, v1 now consistently points to authoritative application reconciliation for every operation. This is deliberately not a `safe_to_retry` claim: only the host can make that decision from application semantics and evidence. Continue to distinguish confirmed pre-dispatch denial from possible dispatch without turning a UI state or error label into retry authorization. Never infer safe retry from an unchanged page alone.

**Implementation update: principal origin policy validation.** The principal-map loader previously checked each `allowed_origins` entry with a looser validator than `SCOUT_MCP_ALLOWED_ORIGINS`. It could accept a wildcard hostname or other URL components that the global parser rejects, allowing policy configuration to be accepted at startup and fail later or be interpreted inconsistently. The loader now calls the shared exact-origin parser, rejects invalid/wildcard entries during startup, deduplicates equivalent entries, and stores the validated values. `go build ./...` and `git diff --check` pass; no tests were run.

**Implementation update: durable per-step task recovery.** The built-in Bolt store now retains a redacted action manifest and per-step cursor with each in-flight task. The manifest records action, action selector, postcondition type, and (when the predicate is selector-based) its target selector, while excluding form values and expected text. `current.status: started` marks the step that may have dispatched before a crash; `finished` means Scout recorded its result, and `next_action` points to the first step not durably finished. An optional `TaskRunProgressStore` preserves compatibility for custom stores. This narrows host reconciliation after restart but does not prove application state; host confirmation still uses `resolve_task_run` with authoritative evidence, and an interrupted ID is never replayed.

**Implementation update: final-verification crash boundary.** The durable recovery phase now distinguishes action execution, final-postcondition verification, and result persistence. It records only the type of the final condition, not its expected value. A crash after every action is finished therefore does not falsely imply that the final page check or completed result was recorded. Hosts still inspect authoritative application state before resolving the reservation.

**Fresh tracker signals (6 November 2026):** Browser Use issue [#5747](https://github.com/browser-use/browser-use/issues/5747) remains open and describes performance degradation on long-running tasks from repeated DOM serialization and growing message history. Browser Use issue [#5067](https://github.com/browser-use/browser-use/issues/5067) remains open but links follow-up #5799; a maintainer commented that it was fixed, so treat it as a regression scenario rather than a current weakness. Playwright issue [#42693](https://github.com/microsoft/playwright/issues/42693) reproduces idle cleanup closing a browser during an in-flight call on a 1.64.0-next commit; it is closed as not planned, not confirmed fixed. Agent-browser issue [#2245](https://github.com/vercel-labs/agent-browser/issues/2245) remains open and reports output bounds not applying to one MCP JSON path in v0.40.0/v0.41.0, based on source inspection with no reproduction run. These status and version details matter: the reports are scenario leads, not prevalence estimates or claims about current released behavior. Scout already has bounded batch inputs/results, durable task cursors, and artifact retrieval for oversized responses. The remaining opportunity is fault-injection evidence across long tasks, renderer crashes, in-flight idle cleanup, and every result encoding path, with matched pinned tool versions.

The durable process-kill fixture now covers two Scout crash boundaries. First, it kills a child after the server accepts a mutation but while its response remains blocked; a fresh process sees `actions/started`. Second, a benchmark-only progress-store wrapper pauses after the final page postcondition succeeds and `result_persisting` is durably written; the action cursor is `finished`, the fixture has accepted the mutation, and the completed result has not been committed. After each kill, a fresh child attempts the same task ID and plan; Scout rejects it before dispatch, and the authoritative mutation count remains unchanged. The host records reconciliation from that server-side evidence. A separate completed task demonstrates cross-process idempotent result reuse. Full-browser-loss probes now have 10 independent runs per tool for agent-browser 0.41.2 and Playwright MCP 0.0.84. Both preserved the fixture mutation and reopened in 10/10 runs; agent-browser restored the localStorage marker in 1/10 and Playwright MCP in 0/10. In the updated probe, both adapters then blindly repeated the harmless UI click and duplicated the fixture effect in 10/10 runs (Wilson 95% CI 72.2–100%). This measures unsafe UI replay, not native task-ID guards or host-supplied idempotency. The same 10/10 observed preservation and reopen rates have Wilson intervals of 72.2–100%; the 1/10 and 0/10 storage-marker restore intervals are also wide. Scout separately replaces Chrome after a confirmed mutation, blocks the same-ID retry, and records host reconciliation from authoritative fixture evidence in a fresh Scout process. The manifest excludes expected values/text; selectors remain retained to identify the verification target.

**Further competitive research (6 November 2026):** The independent WebdriverIO benchmark reports same-harness Online-Mind2Web runs across Playwright MCP, agent-browser, Stagehand, WebdriverIO, and Playwright CLI, measuring task success, tokens, cost, time, and tool calls ([benchmark and raw-run links](https://github.com/webdriverio/benchmark)). Its published results are single-run per task and model, so they are useful as an evaluation template, not a stable ranking. Scout's next scorecard should use repeated trials and include false-success rate, verified outcome, wrong-target actions, recovery success, and host integration effort in addition to efficiency metrics. A strong Scout advantage may be fewer unsafe false positives even if a general web task benchmark shows no raw completion-rate lead.

**Durable recovery proof (10 November 2026):** [`examples/durable_task_recovery`](../examples/durable_task_recovery/main.go) exercises the public Go API with a live local fixture. A fresh run is recorded in `recovery-run-20261110.json` (local audit artifact). It kills one Scout child after a fixture mutation commits but before its response returns, then reopens bbolt and verifies the `actions/started` cursor. It kills another after the final page postcondition succeeds and `result_persisting` is durably recorded, but before the task result is committed; the recovered action cursor is `finished`. After both interruptions, same-ID retries are rejected before browser dispatch, and the fixture ledger remains at one accepted mutation per task. Fresh processes reconcile against the fixture's independent in-process ledger, and a separate completed task demonstrates saved-result reuse. The run also replaces Chrome after a committed mutation, rejects the prior page binding, blocks replay, and reconciles from the ledger. The parent intentionally keeps Chrome and the app fixture alive for the Scout-process interruption cases. This verifies the example's local contract on one run; the ledger is not a production database, and the result is not comparative or reliability evidence. Independent participant studies and matched comparative recovery runs remain open.

The example also attaches the independent `agent-browser` 0.41.2 runner to Chrome through its local CDP discovery URL. Its live tab list must report the same CDP `targetId` and page URL Scout observes before the handoff is accepted. This closes a concrete interoperability proof point for target selection; the host still owns broker generation, tenant scope, policy identity, and authentication of the peer observation.

**Cross-process handoff proof (6 November 2026):** the same runnable fixture exports the interrupted action receipt, signs it with an ephemeral host-held key and tenant scope, then verifies it in a standalone Node.js receiver and a new Scout process. Node validates owner and expiry, verifies the HMAC-SHA256 bytes, and compares supplied browser generation, target, frame, and policy. A fresh run matched owner/browser/target/policy but failed closed on frame because agent-browser's tab-list CLI does not expose frame identity; the Scout process separately confirmed the full binding. Mutating a signed operation field was rejected by Node. The Node binding record is supplied by this fixture rather than an authenticated production broker feed. The fixture's generation token remains modeled. This validates cross-language contract consumption and negative signature handling, not full cross-vendor browser portability or production broker identity; those require adapters that attest a stable generation token and map it to the live browser/context.

**Receiver evidence refinement (6 November 2026):** the standalone Node receiver reads the JSON observation emitted by the independent agent-browser CLI and checks its target ID against the signed receipt and its URL against the fixture's expected URL. It also independently checks task-local operation identity, outcome/verification agreement, unknown-dispatch consistency, no-replay state, and the policy digest when canonical inputs are present. A fresh full-example run after these checks passed and rejected a tampered signed envelope; see the focused Node receiver run record (local audit artifact). The CLI output remains unauthenticated and lacks frame identity, while the demo broker-generation token is not bound to a production browser context. This is cross-language contract-consumption evidence, not proof of production cross-vendor continuity.

**Current competitor baseline recheck (6 November 2026):** agent-browser's current session-management docs describe periodic auth-state restore and strict `--pin-tab` target binding, with `tab_gone` instead of silent fallback; Playwright MCP's recovery issue records that a second call can recover after the first post-disconnect error, though an agent may abandon the lost page state ([agent-browser session management](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md), [Playwright disconnect report](https://github.com/microsoft/playwright/issues/41617)). These features raise the bar for continuity and target safety. They do not settle whether a non-idempotent business mutation committed. The product opportunity is therefore evidence provenance in the host's decision: Scout terminal resolution records require the source system, opaque record reference, and observation time alongside the host's status, while remaining explicit that Scout does not verify that source. Validate whether this structured trail reduces audit and integration work in a host-service study.

**Opportunity update (6 November 2026):** Current public issue traffic suggests the sharpest gap is *recoverable operations under lifecycle failure*, not basic browser coverage. Recent Playwright reports describe idle cleanup interrupting an in-flight call (#42693), orphaned isolated browser processes when a keepalive connection never sends `DELETE` (#1795), and a CDP disconnect case (#41617). In that latter report, the reporter clarified that a subsequent call could recover; the first call still failed and could leave an agent without its working page context. Agent-browser reports include tab-reference loss after closing a background tab (#2407), slow CDP attach with discarded tabs (#2431), and clickables missing inside shadow roots (#2246). Browser Use has an open request for active-tab connection management (#6296) and an open report of security policy falling back to allow-all when configuration parsing fails (#6215). These reports are individual, version-specific signals; several Playwright reports are closed through fixes in underlying Playwright, so test current releases before drawing conclusions ([Playwright idle timeout](https://github.com/microsoft/playwright/issues/42693), [Playwright isolated-session cleanup](https://github.com/microsoft/playwright-mcp/issues/1795), [Playwright CDP recovery](https://github.com/microsoft/playwright/issues/41617), [agent-browser current issues](https://github.com/vercel-labs/agent-browser/issues), [Browser Use current issues](https://github.com/browser-use/browser-use/issues)).

The resulting opportunity is to sell a *predictable failure boundary*: every call has a bounded deadline; a timeout distinguishes “not applied,” “applied and verified,” and “outcome unknown”; unknown mutations are reconciled before retry; session cleanup has a bounded contract; and target references never silently move to a neighboring tab. This is more concrete than a general “reliable automation” claim. Scout already has work in these areas, but the product advantage remains to be demonstrated with matched, repeated fixtures and current competitor versions. Turn the reports above into regression scenarios, not marketing evidence.

### Competitive refresh: same-session recovery and isolation are converging (6 November 2026)

Fresh GitHub state narrows the Scout thesis further. Browser Use has an open
[#6298 PR](https://github.com/browser-use/browser-use/pull/6298) to run its
agentic retry in the MCP tool's existing session, preserve domain restrictions
and `keep_alive`, serialize retries, and protect the session from idle/close
cleanup. Its author reports 20 unit cases and six real-browser cases in CI;
that is promising PR evidence, not a released capability. Browser Use's
[#6305 draft](https://github.com/browser-use/browser-use/pull/6305) proposes
choosing the already focused tab on initial CDP attach, with an explicit
ambiguity error. It depends on separate startup-recovery work in
[#5851](https://github.com/browser-use/browser-use/pull/5851), which remains
open. This makes same-session continuation, restriction preservation, and
active-tab attachment an imminent parity bar.

Agent-browser issue
[#2422](https://github.com/vercel-labs/agent-browser/issues/2422) describes a
more serious isolation failure: an MCP tool's `session` argument can override
the server-pinned session and route two agents into the same browser. Its open
[#2430 PR](https://github.com/vercel-labs/agent-browser/pull/2430) proposes
namespacing tool sessions under the server session. Measure isolation and
misrouting directly; do not assume a session name is an authorization
boundary. Meanwhile, Playwright MCP issue
[#1599](https://github.com/microsoft/playwright-mcp/issues/1599) about a stale
crashed tab was closed through an upstream Playwright fix, an example of why
issue reports are test leads rather than durable competitor deficits.

**Positioning implication:** the strongest defensible opening is host-owned
operation resolution. Session continuity says where a worker can continue;
Scout's opportunity is to make it easier for a Go host to decide whether a
business operation committed, preserve that conclusion with its evidence
source, and keep the same operation ID from triggering duplicate work. The
current task-run API, recovery receipt, and resolution record implement parts
of this contract. This is still a product hypothesis: a host can build similar
guards around any browser library, and no buyer study yet shows Scout reduces
that integration burden.

**Next competitive proof:** use Browser Use's `same_tab` path once it is
released, plus current agent-browser and Playwright MCP. Start from an existing
authenticated browser session; dispatch one delayed, non-idempotent fixture
mutation; interrupt the initiating worker; hand control to a second runner;
then query the fixture's authoritative operation ledger before any retry. Record
session and restriction continuity, target/frame agreement, exact effect count,
typed outcome, integration code/steps, and the source attached to the host's
resolution. Keep native tool guarantees separate from host-built safeguards.
The [competitive opportunity matrix](../examples/benchmark/OPPORTUNITY_MATRIX.md)
already specifies repeat counts and claim gates for this comparison.

**Differentiation bets, ranked by evidence and potential:**

1. **Safe recovery contract:** return a typed task result with verified / rejected / unknown states, evidence, target identity, and a resume point. Competitors have capable browser primitives; the opportunity is making recovery semantics explicit and usable by a host service.
2. **Go service fit:** provide lifecycle, context cancellation, worker ownership, graceful shutdown, and structured receipts in an idiomatic Go library. Compete on integration effort for Go teams, not on language breadth.
3. **Fail-closed policy with proof:** reject malformed policies at startup and connect policy decisions to attributable destinations. Pair in-browser request controls with an egress proxy where service workers, WebSockets, redirects, or remote-browser infrastructure are in scope.
4. **Target continuity:** make actions address stable target IDs and surface target loss instead of implicitly adopting whichever tab is active. Validate shared-CDP behavior under simultaneous clients and tab churn.
5. **Evidence-efficient control:** keep observation bounded and paginated while exposing truncation and retrieval paths. Measure task success under equal token budgets before claiming smaller context is better.
6. **WebMCP as an adapter:** expose page-registered tools only behind explicit origin/target scope, schema validation, and independent postcondition checks. Chrome's preview is page-local and flag-gated, so it is an experiment rather than a near-term moat.

**What to build / measure next:** prioritize the safe recovery contract as the product surface; use the delayed submit/type/click fixtures and target-churn matrix to test it. Then recruit Go service teams to compare time-to-first-verified-task and effort to handle an interrupted mutation. In parallel, keep a small current-version regression lane for lifecycle, policy parsing, CDP reconnect, and shadow-DOM discovery. Publish exact versions, native-vs-adapter behavior, raw per-run outcomes, and unresolved limits. Do not convert issue volume into failure-rate claims.

**Opportunity recheck (6 November 2026):** The October 9 WebdriverIO run reports 48% success for Playwright MCP, 50% for agent-browser, and 56% for Stagehand on its 100-task DeepSeek V4.1 Flash sample. Agent-browser used 735k tokens/task, compared with 652k for Playwright MCP and 354k for Stagehand; these are harness/model-specific figures from one run, not a product ranking. The same benchmark has another model run with a different ordering, reinforcing that Scout needs pinned, repeated experiments before claiming an efficiency or success lead ([run protocol and results](https://github.com/webdriverio/benchmark)). The competitive gap is therefore better framed as *operational risk and integration cost*: task-specific origin scope, stable target identity, typed verification, recovery handoff, and a native Go embedding path.

Issue and PR traffic adds two useful cautions. Browser Use has open reports requesting active-tab continuity in MCP (#6296) and describing an allow-all fallback after malformed security configuration (#6218); agent-browser's current issue list includes CDP attach latency after discarded tabs (#2431), shadow-root action discovery (#2246), and output-limit coverage on a JSON response path (#2245) ([Browser Use issues](https://github.com/browser-use/browser-use/issues), [agent-browser issues](https://github.com/vercel-labs/agent-browser/issues)). Agent-browser also documents persisted target binding, strict tab pinning, and session state restore, so session continuity and persistence are active parity areas rather than a defensible Scout moat by themselves ([session management](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md)). The 0.42.0 release is dated 10 November 2026; check these issue reports against that version before treating them as current gaps ([release history](https://github.com/vercel-labs/agent-browser/releases)). Treat reports as signals, inspect the matching commits/releases before comparison, and do not infer prevalence from issue counts.

### WebMCP matched-smoke result (6 November 2026)

I exercised agent-browser 0.41.2 and Playwright MCP 0.0.84 against Scout's
shared `/action` fixture with Chrome 157.0.8103.38. Both invoked the injected
`complete_task` tool and passed an independent DOM postcondition in one run.
Agent-browser surfaced a compact tool summary on page load, targeted schema
retrieval, and JSON provenance `untrusted: true`; Playwright exposed a dynamic
`webmcp_complete_task` MCP tool after `browser_snapshot` and wrapped its page
description and invocation output with untrusted-content text. These are
different context surfaces for the same feature; no token or reliability
advantage is measured. The reproducible harness and observations are in
[`examples/benchmark/webmcp`](../examples/benchmark/webmcp/README.md).

The setup itself is version-sensitive: agent-browser's default managed launch
did not expose `document.modelContext` here because its `WebMCPTesting` launch
feature alone was insufficient for this Chrome build; adding
`--enable-features=WebMCP` made it available. That is an integration finding,
not a general competitor defect. Scout now has an experimental CDP bridge exposed
through `webmcp_catalog` and `webmcp_invoke`. It binds invocation to the current
target and registered frame, validates a bounded JSON Schema subset, marks
page-controlled metadata/results untrusted, and requires an independent
postcondition. This is parity work, not agent-level reliability evidence. The
live Scout bridge smoke now passes once on Chrome 157.0.8103.38: the MCP
catalog found the injected tool, invocation returned `postcondition_verified`,
and a separate CDP read confirmed the fixture state. The report is recorded at
[`scout-webmcp-live-smoke.json`](../examples/benchmark/webmcp/scout-webmcp-live-smoke.json).
This remains one integration smoke, not reliability evidence. The next gate is a
same-page WebMCP-versus-accessibility task with delayed completion, tab/frame
drift, malformed schemas, hostile metadata, independent grading, and repeated
actual-agent runs. Chrome describes
WebMCP as experimental and flag-gated for local development
([Chrome WebMCP docs](https://developer.chrome.com/docs/ai/webmcp)).

**Recommended next opportunities:**

1. Finish session-scoped origin policy construction and make its enforced surface explicit. MCP named sessions now accept a requested allowlist only when it is a subset of the effective server/principal policy. Go status and task receipts now carry a versioned policy digest and enforcement label; verify that each transport preserves these fields and keep proxy enforcement documented for service workers, WebSockets, and remote-browser egress.
2. Make the Go integration path the adoption wedge: document a worker/service lifecycle with bounded concurrency, graceful shutdown, context cancellation, structured recovery, and injectable browser ownership. Compare this against Playwright's broader language/testing ecosystem and Browser Use's hosted-browser path instead of claiming general automation superiority.
3. Build a small deterministic evaluation before expanding features. Use local fixtures with gated answers and exact target/policy checks, run repeated trials, and report false success, wrong-target action, reconciliation rate, and setup/integration time alongside token and latency numbers.
4. Track discovery quality and browser compatibility as explicit coverage gaps: shadow DOM, frames, discarded tabs, Electron/CDP attach, redirects, and worker/network policy. Convert each into a fixture where behavior is deterministic and report unsupported cases directly.

Open GitHub reports expose two concrete unmet needs to investigate: Browser Use users request active-tab/session continuity for MCP clients ([#6296](https://github.com/browser-use/browser-use/issues/6296)); another report describes a malformed config silently resetting origin/IP policy to allow-all ([#6218](https://github.com/browser-use/browser-use/issues/6218)). Agent-browser's current issue list includes a CDP connection delay with multiple discarded tabs ([#2431](https://github.com/vercel-labs/agent-browser/issues/2431)) and a snapshot scan missing click targets inside shadow roots ([#2246](https://github.com/vercel-labs/agent-browser/issues/2246)). These are individual reports, not prevalence data. They reinforce product requirements Scout can make explicit: target identity must survive tab churn, policy parse failure must stop startup, and action discovery must state its coverage limits. Confirm fixes and release status before using any report as a comparison claim.

Current GitHub issues reinforce where reliability work still matters, but should be read as signals rather than proof of product-wide failure: agent-browser has reports about session override precedence ([#2422](https://github.com/vercel-labs/agent-browser/issues/2422)), multi-discarded-tab CDP connection latency ([#2431](https://github.com/vercel-labs/agent-browser/issues/2431)), and output bounds missed on one JSON response path ([#2245](https://github.com/vercel-labs/agent-browser/issues/2245)). Browser Use has open requests for MCP active-tab connection management ([#6296](https://github.com/browser-use/browser-use/issues/6296)) and a report about a malformed security config falling back to allow-all ([#6218](https://github.com/browser-use/browser-use/issues/6218)). Recheck issue and PR state against the specific release being compared.

### Competitive refresh: revise the Go wedge and focus the advantage (6 November 2026)

**Go support is parity, not a moat.** Stagehand's active v4 monorepo ships a Go SDK alongside TypeScript and Python, with local browser and Browserbase connections, typed extraction, AI-guided actions, and familiar page APIs ([Stagehand Go SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go), [quickstart](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/quickstart.mdx)). Scout can still win Go teams, but the claim must be about the resulting operational contract and deployment shape: composable APIs, task-scoped policy, verified outcomes, and recovery evidence. “No model” is a valid distinction from Stagehand's local agentic workflow, which requires a provider key, but not from its direct page methods or other deterministic libraries. Compare setup time and runtime requirements honestly; do not describe Scout as the only Go option.

**Competitors are also improving recovery and observability.** Playwright MCP documents idle cleanup that preserves in-flight calls, configurable timeout behavior, trace timelines with DOM snapshots/screenshots/network/console evidence, and WebMCP list/call support in recent releases ([MCP lifecycle docs](https://playwright.dev/docs/getting-started-mcp#idle-timeout), [tracing](https://playwright.dev/mcp/tools/tracing), [release notes](https://github.com/microsoft/playwright-mcp/releases)). Its issue history includes a CDP disconnect report that was closed after retry behavior worked, showing that the original report must not be treated as an enduring gap ([#41617](https://github.com/microsoft/playwright/issues/41617)). Browser Use Cloud V4 explicitly warns not to blindly retry an ambiguous run creation because duplicate paid work may result, and says to validate the result string in the application ([V4 API guide](https://github.com/browser-use/browser-use/blob/main/skills/cloud/references/api-v4.md)). That validates the *job* of ambiguity-aware recovery, while proving Scout must offer a better integrated outcome contract than a warning in operational docs.

Browser Use MCP issue traffic adds two current workflow signals: requests for explicit active-tab connection management (#6296) and for `retry_with_browser_use_agent` to continue the current browser session (#6295) ([issue tracker](https://github.com/browser-use/browser-use/issues)). These indicate a gap between autonomous task tools and low-level session control. They do not establish prevalence or imply that Scout's own target/session handling is already superior; compare target continuity under tab changes, caller cancellation, browser restart, and same-session follow-up.

#### Revised competitive position

Scout's strongest opportunity is **verifiable, recoverable browser work embedded in an application**. The differentiating unit is not an individual click API or Go SDK; it is a caller-visible task result that joins:

- declared origin, session, target, deadline, and required evidence;
- action-by-action state (`verified`, `rejected`, or `unknown`) with evidence and a resume cursor;
- explicit safe-retry guidance, including reconciliation before repeating a mutation;
- local execution and caller-owned model/infrastructure, with no hosted service required;
- bounded evidence plus trace/artifact retrieval that does not depend on one MCP transport.

The claims need to be tested against both code-first SDKs and agent-oriented MCPs. Stagehand is the closest Go-SDK comparison; Playwright is the strongest tracing/tooling baseline; agent-browser is the CLI/session and context-footprint comparison; Browser Use is the autonomous and hosted-run comparison. Scout should not claim it beats an autonomous agent at open-ended task completion unless a matched task set shows it.

#### Experiment: interrupted task, same target

Add or extend a deterministic benchmark where a mutation commits after the caller deadline (for example, a delayed form submission with a one-time side effect). Run each tool through its native supported interface and pin exact versions. Include cases for: timeout before dispatch; timeout after dispatch but before response; browser disconnect after commit; target closed/replaced; and accepted versus rejected server response with optimistic UI text. For each repetition, record whether the tool reports success, rejection, or uncertainty; whether it identifies the correct target; whether the caller can inspect state before retry; duplicate side effects after recovery; cleanup time/processes; evidence bytes; calls/tokens; and operator steps needed to continue.

The primary score should be **false-success and duplicate-side-effect rate**, followed by time and operator effort to reach a verified outcome. Raw task success, latency, token count, tool count, and trace quality are supporting measures. Use at least 30 randomized repetitions per condition before publishing rates, preserve raw logs, and include a documented Scout failure case. First run this on Scout and agent-browser / Playwright MCP, then add Stagehand Go and Browser Use interfaces where their public APIs allow a fair same-target setup. A hosted Browser Use run and a local Scout run have different infrastructure; report that as a separate deployment condition.

#### Product implications

1. Keep Go-first onboarding, but position it as an integration advantage for Go service teams, not exclusive language support. Add a side-by-side integration example with Stagehand Go and measure lines/configuration, required services, first verified result, and graceful shutdown.
2. Make retry semantics part of the API result: say whether dispatch began, whether the mutation may have committed, what evidence is available, and which read-only reconciliation can establish the state. A generic `retryable` boolean is insufficient for non-idempotent actions.
3. Make task-run receipts durable or exportable if the process exits; in-memory idempotency alone cannot protect a service retry after restart. Persist enough task identity and result state to reject a conflicting reuse of a caller ID and return the original completed outcome. For a crash during dispatch, recover as `unknown` until an explicit reconciliation predicate passes.
4. Close the observability gap with one exportable task bundle (ordered actions, target identities, verification evidence, timings, bounded DOM/network excerpts, and artifact references). Playwright already sets a high bar for trace UX, so Scout should make traces actionable for Go services rather than only viewable in a GUI.
5. Treat WebMCP as a compatibility adapter opportunity, not a unique capability. Wrap page-provided calls with the same target/origin policy, schema checks, and independent postcondition verification as ordinary UI actions.

Until the experiment is run, the defensible message is a product hypothesis: **Scout is being built to make browser task outcomes safe for software to act on.** Avoid claims of lower failure rates, stronger security, or easier Go integration without matched evidence.

### New conclusion: move from “profiles” to a verifiable task contract

Profile switches are converging across Scout and agent-browser, and small MCP catalogs are also available in Browser Use. Agent-browser's EVE adapter additionally explores deferred discovery; its eval suite makes context footprint and agent performance explicit measurements. Scout should keep profiles because they reduce available surface, but lead with the guarantee that a profile cannot silently remove the actions required by a declared task. Add machine-readable task capabilities (required tool/action families and optional evidence predicates), validate a requested profile at startup, and fail early with the exact missing capability. Then evaluate profiles and recipes on fixed tasks using pinned model versions, schema-token counts, outcome predicates, latency, and recovery cost. A deterministic tool-list script alone measures catalog size; it cannot show that a model chooses the right tools or completes a task.

The highest-value near-term product opportunity is a **task run contract** layered on the existing action receipts and recovery records: each task declares allowed origins, session/target scope, required evidence, deadline, and retry policy; Scout returns a compact trace with completed/uncertain steps and a resume point. This joins several parity features (profiles, batching, traces) around a user-visible result: a Go service can determine whether it may safely continue after a browser action fails. The security issues above also suggest one concrete acceptance rule: malformed or unavailable policy must fail closed, never broaden access.

Priority order:

1. Complete receipt/recovery coverage for every mutating tool and publish a cross-interface contract test.
2. Add task-level required capabilities and fail-fast profile validation; keep profiles as operational presets.
3. Complete fail-closed policy loading and deployment-ready egress enforcement. MCP now supports a fixed exact-origin allowlist for stdio/shared-token deployments; principal mapping uses per-principal allowlists. Validate the startup contract and keep an enforcing proxy example for service-worker, WebSocket, and remote-browser paths.
4. Run repeated, pinned-model tasks against Scout, Playwright MCP, and agent-browser. Record success predicates, wrong-target actions, tool/schema tokens, wall time, retries, and output bytes; publish raw runs and scripts.
5. Interview Go service teams before investing in agent loops, plugins, or hosted browser management. Those layers already have strong incumbents.

**Next competitive experiment:** build a fixed local task set that covers page inspection, form completion with an explicit accepted/rejected result, tab selection under churn, and recovery after an uncertain mutation. Run the same model/version and task prompt against Scout `core` and `all`, with and without calling `help`, plus agent-browser's standard MCP and EVE adapter where setup permits. Randomize task order, pin browser builds, and record exact success predicates, schema/prompt/completion tokens, tool choice and arguments, calls, retries, wall time, and false success. Start with 10 repetitions per task/condition to find protocol problems; use at least 30 before publishing a comparative rate. Keep adapter differences visible and report confidence intervals. EVE's published eval design shows a measurement pattern, not evidence that it outperforms Scout.

**Opportunity refinement:** Scout's most defensible wedge is an *operational contract for browser work in Go services*: task-scoped permissions, stable session and target attribution, explicit verified/rejected/unknown states, bounded evidence, and safe reconciliation after uncertain mutations. This is stronger when combined into one host-facing task result than when marketed as separate receipts, profiles, or lifecycle controls. Build the next slice as a task run envelope that composes existing batch, origin, target, deadline, and recovery primitives; then use the scorecard above to learn whether it reduces false success and recovery effort. Defer a general autonomous agent loop, hosted browser fleet, and arbitrary plugin system until interviews show those are adoption blockers; those areas have established competitors and would expand the trust surface.

**Implementation progress (6 November 2026):** `Session.RunTaskContext`, MCP `task_run`, and stateful JSONL `task_run` now provide one caller-ID/target/deadline/evidence envelope over verified batches, including a task status and resume cursor. MCP named sessions can be created with a narrower exact-origin allowlist, checked as a subset of the applicable server or principal policy and fixed at session launch. `task_run` declarations must still match that session policy; page-level policy does not enforce all browser egress channels. Durable run persistence is available through the opt-in bbolt store and Go `TaskRunStore` interface; the runnable recovery example demonstrates both a killed-process dispatch boundary with host reconciliation and completed-task reuse after reopening the store. Final-verification/result-persistence crash cases and repeated competitor task-success evaluation remain open.

**Implementation update:** MCP stdio and Streamable HTTP now accept `--require-tools=tool_a,tool_b`. Startup rejects unknown tool names or a selected profile that omits any required tool, and reports all missing requirements together. Batch execution now stops by default before dispatching later actions when a step has an uncertain outcome. Go returns a typed `BatchStoppedError`; MCP and stateful CLI expose `SCOUT_BATCH_STOPPED` and retain the partial result plus recovery record. Callers can explicitly set `continue_on_unknown=true` when later actions remain safe despite uncertainty. These provide catalog and limited retry-policy checks, not a complete task contract: task predicates, origin constraints, and caller-level recovery orchestration remain future work. The runner prototype explored for measuring task quality was removed after it hung and produced no valid measurement. Rebuild that evaluation around an isolated, verified browser fixture and pinned model runs before publishing task-success claims.

**Egress evidence update (6 November 2026):** The reference mitmproxy allowlist now emits structured, path-free decisions for HTTP and CONNECT events and fails closed if the logger rejects a decision. Its Chrome fixture uses separate denied destinations for service-worker fetch, redirect, subresource, iframe, new-tab navigation, and WebSocket handshake; three consecutive runs verified all six deny records plus an allowed-origin record and confirmed none of the denied requests reached its destination. Seven addon unit tests and the live Chrome/mitmproxy integration pass on Chrome 157.0.8103.38, mitmproxy 12.3.1, macOS arm64; three per-run decision sets are saved at `scout-egress-proxy-policy-2026-11-06.json` (local audit artifact). This is evidence for the Scout-plus-proxy deployment pattern, not proof that arbitrary browser launchers or remote environments route every channel through the proxy.

**Policy diagnostic contract (6 November 2026):** Scout's page-level URL denials now have stable reason codes and carry the effective policy digest plus target evidence reference through Go, CLI, MCP, and trace decisions. This improves audit correlation and host recovery diagnosis; it is implementation progress, not comparative evidence or full browser egress enforcement.

**Additional live issue scan (6 November 2026):** Playwright MCP issue [#1795](https://github.com/microsoft/playwright-mcp/issues/1795) described isolated browser processes persisting when a proxy kept an HTTP/SSE stream alive without MCP activity; it was closed as completed through microsoft/playwright#43527 on 19 October. Playwright issue [#41539](https://github.com/microsoft/playwright/issues/41539) described an isolated HTTP context surviving one client's disconnect while another remained connected; it was fixed in #41560. These are lifecycle regression cases, not current product defects. Current agent-browser open issues include [#1713](https://github.com/vercel-labs/agent-browser/issues/1713) (no per-command deadline can leave a daemon command blocked), [#2290](https://github.com/vercel-labs/agent-browser/issues/2290) (HAR stop after browser restart reports zero requests), [#2293](https://github.com/vercel-labs/agent-browser/issues/2293) (direct-page event attribution), and #2246 (output bounds skipped on one JSON path). Stagehand's current issue tracker shows reports around active-page selection (#3371), already-idle `waitForLoadState` timing out (#3370), CLI daemon deadlines (#3369), and iframe auto-attach after `act()` (#3321). Tracker state and fixes are release-specific; rerun this scan before any external comparison.

## Executive view

Scout competes in a crowded browser automation stack, but the products solve different layers:

| Layer | Representative products | Their strength | Scout's opening |
|---|---|---|---|
| Browser control primitives | Playwright MCP, Vercel agent-browser | Mature browser operations, rich snapshots, CLI workflows, broad integrations | Keep Go embedding and one-binary deployment simple; make operation identity and result evidence consistent across Go, CLI, and MCP |
| AI automation SDKs | Stagehand | Natural-language `act`/`observe`/`extract`, deterministic code path, multi-language SDKs | Offer a lower-dependency runtime with caller-owned control and explicit handling of uncertain outcomes |
| Full browser agents and hosted execution | Browser Use, Skyvern, Browserbase | Agent loop, cloud browsers, scaling and anti-bot infrastructure | Integrate with these environments rather than recreate their hosted fleet; differentiate at the execution contract |

The opportunity is not “more browser verbs,” “Go support,” or generic reliability. Those are increasingly parity features. Scout can aim to be the **auditable browser runtime for agent-driven work**: each operation states which session and target it used, what it tried to do, what evidence it observed, whether the result is known, and what safe next action is available. The key product test is whether this contract reduces wrong actions and makes failures cheaper to diagnose in a real Go service, while keeping context and runtime costs bounded.

**Operator recovery workflow update:** durable task recovery exposes a redacted plan, target binding, dispatch cursor, and host attestation history through Go, CLI, and MCP. The stateful CLI combines lookup and optional host attestation with `recover_task_run`; MCP exposes `get_task_run` and `resolve_task_run`. The built-in bbolt store retains unresolved and terminal attestations across process restarts and keeps the task ID unavailable after either. This does not automate application-state reconciliation or establish Scout-side proof. The workflow is documented in [the Go service integration guide](go-service-integration.md).

## What “better than the competition” should mean

Scout should not try to win by matching the largest surface area. Agent-browser is rapidly adding plugins, provider support, skills, flows and CLI ergonomics; Playwright MCP has mature Playwright semantics and capability switches; Stagehand sells natural-language interaction with hosted execution; Browser Use bundles an agent loop and cloud. Trying to reproduce all of those would dilute Scout's advantage. The viable wedge is a smaller runtime with stronger guarantees at the boundary between an agent and a real browser.

| Buyer need | Competitive baseline | Scout can win if it proves | Evidence / next proof |
|---|---|---|---|
| Avoid a false “done” after a timeout or stale target | Competitors expose errors, traces, agent retries or caller-side checks; native recovery semantics vary | Every mutation returns verified, rejected, or unknown with target-bound evidence and a non-replaying reconcile path | Extend recovery to all mutations; matched late-effect, target-loss and reconnect cases across Go, CLI and MCP |
| Keep browser work attributable across concurrency | Playwright MCP isolates HTTP clients; agent-browser supports named sessions and per-command overrides; Stagehand exposes contexts/pages | Every call carries an unambiguous client/session/target identity, and mixed concurrent calls cannot redirect or contaminate results | Five fresh Scout HTTP runs passed simultaneous shared-endpoint navigation, context isolation, reset and sibling cleanup; next cover provider diversity and larger concurrent load (raw report (local audit artifact)) |
| Keep agent context and execution cost predictable | Playwright has snapshot scope/capabilities; agent-browser has output limits and batching; Stagehand caches/batches; Browser Use issue reports long-task state growth | Uniform output limits and task-sized tool profiles preserve needed evidence at a predictable context and runtime cost | Measure schema tokens, output bytes, tool selection, task completion, memory and p95 latency on identical tasks |
| Embed in an existing service | Most leading stacks center TypeScript/Python, a CLI, or hosted platform | A Go caller can embed Scout without adopting another agent framework or browser provider | Ship working worker-pool, graceful-shutdown, container and remote-CDP examples; validate with Go teams |
| Feed existing observability stacks | Stagehand advertises built-in OpenTelemetry; other stacks rely on traces, logs, or provider dashboards | Let Go hosts correlate browser actions with their existing traces while keeping page data private by default | Go API supports caller-owned providers; CLI/MCP has opt-in OTLP/HTTP export using standard `OTEL_EXPORTER_OTLP_*` configuration. Local stdio and Streamable HTTP runs confirmed receipt/span ID and duration parity, default content redaction, and close-span flush (stdio capture (local audit artifact), HTTP capture (local audit artifact)); a delayed-effect recovery run confirmed cross-trace span linking, recovery-ordinal attributes, and selector/predicate redaction (recovery capture (local audit artifact)). Each transport check is one local run; repeated reliability, external collectors, and no-op versus sampled overhead remain unmeasured. |
| Operate safely in production | Hosted products bring managed infrastructure; local tools differ in policy and lifecycle controls | Explicit browser ownership, URL/egress policy, authenticated HTTP boundary, cancellation semantics and cleanup are easy to understand | Threat-model shared HTTP service, add owner-bound sessions before claiming multi-tenant readiness, exercise disconnect/crash cleanup |

This wedge is strongest for teams already operating Go workers and browsers. If research shows the target buyers primarily want a turnkey autonomous agent or managed anti-bot access, Scout should integrate with those systems instead of competing for that layer.

## Opportunity research refresh (6 November 2026)

### Strategic implications from the current feature set (6 November 2026)

The category is converging on broad capability parity. Stagehand now documents Go alongside TypeScript and Python, combines `act`/`observe`/typed `extract` with deterministic page methods, and reports per-method model metrics ([Stagehand introduction](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/introduction.mdx)). Therefore Go language availability alone is not a moat. Scout's narrower claim should be that a Go service can own browser lifecycle and policy directly, with no agent framework requirement, and receive a stable, target-bound outcome/recovery contract.

The control layer is also expanding: Playwright MCP offers persistent, isolated, and extension-connected browser modes plus configurable snapshots and network controls ([Playwright MCP](https://github.com/microsoft/playwright-mcp)); Chrome DevTools MCP exposes category switches for performance, network, debugging, memory, WebAuthn, PWA, and experimental WebMCP tools ([configuration](https://github.com/ChromeDevTools/chrome-devtools-mcp/blob/main/docs/configuration.md)). Browser Use and Browserbase bundle hosted sessions, persistent profiles, and live viewing. Matching each capability would make Scout a weaker copy of several products. Instead, integrate at CDP/provider boundaries and make outcomes, ownership, policy, and evidence portable across them.

An emerging adjacent opportunity is WebMCP: Chrome describes it as an early-preview, browser-native way for sites to publish structured client-side tools, requiring an origin-trial or local browser flag today ([Chrome WebMCP guide](https://developer.chrome.com/docs/modern-web-guidance/guides/webmcp/webmcp)). Scout could detect and invoke these site-provided tools as an optional first path, then fall back to DOM/CDP interaction when absent. This is an experiment, not a roadmap commitment: support is preview-stage, site coverage is unknown, and high-impact actions still need Scout's own verification and policy checks.

Recent issue trackers point to a practical product gap shared across the category: session continuity and security policy must survive retries and failure. Stagehand's open tracker includes reports on active-page selection, idle wait semantics, iframe attachment, and domain-policy bypass through service-worker/WebSocket traffic ([open issue tracker](https://github.com/browserbase/stagehand/issues)); Browser Use's tracker includes session-follow-up requests and a reported fail-open configuration path ([open issue tracker](https://github.com/browser-use/browser-use/issues)). These are individual reports, not defect rates or proof that Scout is safer. They motivate matched tests for Scout: policy must cover all egress paths, malformed config must fail closed, and an uncertain operation must retain its session/target identity through recovery.

**Updated strategic bet:** sell a *verifiable execution layer for browser work* to teams that already have an agent or service. The defensible proof is not a larger API or a language checkbox; it is a reproducible reduction in false success, unsafe retries, wrong-target actions, and time-to-diagnosis across Go, CLI, MCP, and remote-browser deployments. The next investment should be a matched fault-injection scorecard and user validation of portable recovery receipts. Treat WebMCP support and trace UX as small integration experiments; defer a general autonomous agent, hosted fleet, and capability-count race.

### Matched delayed-mutation recovery result

The delayed `/late-action` fixture now has a matched ten-run comparison across Scout's Go API, agent-browser 0.41.2, and Playwright MCP 0.0.84 on HeadlessChrome 157. All three reached the same post-mutation page state in 10/10 repetitions after the caller wait ended at 300 ms; none replayed the click. Scout exposed a native `outcome_unknown` record containing the expected predicate and target, reconciled the late effect on that target, and refused to reconcile after the active target changed. The peer adapters used caller-side interruption or wait orchestration followed by inspection and did not expose a recovery record in this trial. Raw reports and a normalized result-only matrix are documented in [the benchmark comparison](../examples/benchmark/COMPARISON.md).

This strengthens the opportunity hypothesis around a stable, target-bound recovery receipt and safe resolution in the caller's workflow. It does not establish that competitors lack late-effect handling generally, nor does it support a speed claim: the three timing windows differ. The focused CLI recovery probe now also persists the private record, reconciles from a fresh process on the original target, closes that target, and confirms a later reconcile is refused; this extends the safety check across a process boundary. Next evidence should repeat the same evidence-quality scorecard through Go, CLI, and MCP, then test whether platform teams find the portable target/predicate useful when orchestrating non-idempotent browser work.

Recent competitor product changes and public issue reports reinforce the execution-contract opportunity. They also suggest where Scout should invest beyond its current recovery work. Issue reports below are examples of concrete failure modes, not estimates of how often they occur.

**Verified-action head-to-head refresh:** Scout Go API, agent-browser 0.41.2, and Playwright MCP 0.0.84 each satisfied the same `#status == "Task complete"` predicate in 10/10 runs on HeadlessChrome 157. This establishes outcome parity for a simple click fixture, not a Scout advantage. The normalized report correctly excludes timing and response-size ranking because the paths measure different operations and output boundaries. Raw artifacts and scope notes are in [`examples/benchmark/COMPARISON.md`](../examples/benchmark/COMPARISON.md#repeated-verified-action-outcome-parity). The priority should shift to uncertain late mutations, target ownership under concurrency, and egress coverage, where Scout's vision makes stronger promises.

**Matched late-mutation recovery contract (6 November 2026):** Scout MCP stdio, agent-browser 0.41.2, and Playwright MCP 0.0.84 each reached the same delayed-click page predicate in 10/10 runs on HeadlessChrome 157. This is completion parity. The fixture independently confirmed that Scout returned its typed `outcome_unknown` before the delayed mutation completed in all 10 runs; Scout then reconciled on the same target and linked the recovery receipt without replay. Both peer adapters used caller-side interruption/wait and inspection in this probe. The normalized report keeps timing windows and output sizes non-comparable: `late-recovery-mcp-head-to-head-10x-2026-11-06.json` (local audit artifact), with raw Scout rows in `scout-mcp-late-action-cancellation-10x-20261106.json` (local audit artifact). This supports a native, host-readable recovery-contract hypothesis for this fixture, not a release-wide reliability lead or proof that peers lack native recovery elsewhere. The older Scout Go API and adapter details remain in [`examples/benchmark/COMPARISON.md`](../examples/benchmark/COMPARISON.md#matched-delayed-mutation-caller-timeout-comparison).

**Recovery input integrity follow-up:** Portable recovery JSON now rejects unknown fields and trailing JSON/content instead of silently accepting a partial or concatenated artifact. The legacy v1 CLI recovery envelope remains supported under the same strict decoding rules. Stateful CLI reconciliation rejects requests that provide both raw and base64 recovery records. Focused tests cover valid v1 compatibility, malformed suffixes, and ambiguous-source rejection; cross-interface restart and late-effect benchmarks remain the proof for end-to-end recovery behavior.

**Target-destroying recovery follow-up:** `close_tab` can now save a `target_absent` predicate bound to the exact closed CDP target. Reconciliation checks the live browser target inventory even if Scout's active page is a different target: absence verifies the close, presence stays `outcome_unknown`, and unsupported inventory checks or errors never count as success. Validation only permits this predicate for `close_tab` when its ID equals the unknown outcome's target. Unit coverage verifies both inventory outcomes and rejects mismatched or cross-action predicates. The browser lifecycle integration now also round-trips a portable close recovery record and verifies the closed target is absent from live Chrome inventory. This exercises recovery serialization and real target inventory after a confirmed close; it does not inject an uncertain close or prove late-close behavior. A delayed close/disconnect fixture and repeated cross-provider results remain open before making a broader reliability claim.

**Concurrent session close follow-up:** `close_session` now marks the named session as closing at MCP request admission, rejects new work targeting that session, and waits for already admitted calls to drain before removing the browser session. The wait is bounded by the MCP request context; if it expires, the session remains registered and the admission marker is cleared. `SCOUT_BENCHMARK_FOCUS=mcp-http-close-drain go run ./examples/benchmark` runs a delayed page wait concurrently with `close_session` and checks that the wait finishes before registry removal and browser cleanup. One live HTTP run passed: the wait completed, a new status call was rejected during the drain, and close completed after 1.745 s with registry removal and cleanup confirmed. This is a single local run, not repeated reliability evidence.

| Priority | Opportunity | Competitive signal | Scout move | Proof required |
|---|---|---|---|---|
| P0 | Make uncertainty and recovery consistent for every mutation | Stagehand's issue tracker includes failures around cache behavior, iframe attachment, active-page selection, and CLI timeout expectations. Playwright MCP reports include shared-context tab ownership and wait calls racing idle cleanup. | Finish the verified/rejected/unknown receipt contract across all mutating APIs and interfaces. Preserve target identity and expected postcondition; offer reconciliation that never silently replays a mutation. | Same fault fixtures through Go, one-shot CLI, stateful CLI, MCP stdio and HTTP; inject late effects, target closure, cancellation, and restart. Track false-success and unsafe-retry rates. |
| P0 | Own session and target isolation under concurrency | Playwright MCP issue [#1631](https://github.com/microsoft/playwright-mcp/issues/1631) describes cross-client page events leaking into snapshots; Playwright issue [#42961](https://github.com/microsoft/playwright/issues/42961) proposes ownership semantics for shared persistent profiles. | Make session ownership explicit in every request/result and test concurrent clients, popups, reconnects, disconnect cleanup and target closure. Scout now has opt-in isolated remote BrowserContexts per principal/session; shared authenticated-profile ownership remains a separate product choice. | Five fresh Scout HTTP runs passed shared-endpoint context isolation, reset and sibling cleanup. Cleanup after caller deadline now gets a bounded 500 ms disposal attempt with errors reported; `disposeOnDetach` is the protocol fallback. Provider diversity and larger concurrency remain open (raw report (local audit artifact)). |
| P1 | Reduce context cost without losing actionable evidence | Playwright MCP provides snapshot scoping and file output; agent-browser has output limits and batching; Stagehand offers caching/batching. Yet Stagehand issue [#1767](https://github.com/browserbase/stagehand/issues/1767) reports repeated cache misses for documented operations. | Extend Scout's bounded observations with stable compact references, targeted refresh, and explicit truncation/completeness metadata. Keep limits predictable across MCP tools and error payloads. | Compare task success, tokens/bytes, tool calls and p95 latency on the same long-page and multi-step tasks; ensure truncation cannot look like complete evidence. |
| P1 | Make observability useful at the run level | Stagehand advertises OpenTelemetry; Playwright MCP tracing records action timelines, DOM snapshots, screenshots, network and console details. Scout has compact action spans through a caller-owned Go provider and opt-in CLI/MCP OTLP/HTTP export. | Correlate action receipts, reconciliation, target changes and session lifecycle into useful traces while keeping selectors, URLs, form values and page content out by default. Keep SDK/exporter ownership explicit and flush on shutdown. | One-run MCP stdio and Streamable HTTP captures confirm action receipt/span ID and duration parity, redaction, and session-close export; the stdio recovery capture confirms cross-trace reconciliation links and ordinal attributes. Repeated reliability, sampled/no-op overhead, representative trace inspection, external collectors, and provider behavior under canceled sessions remain open. |
| P1 | Turn local integration into production operations | Hosted competitors package persistent sessions, live view, proxy/browser fleet operations and task status. Scout's Go runtime can instead fit into an existing worker or service. | Provide production examples for worker pools, graceful shutdown, remote CDP, persistence boundaries, health checks and failure recovery. Define the HTTP MCP trust boundary and session ownership before encouraging shared deployment. | Run a documented sample under concurrent load and process restart; report memory, startup, latency and cleanup behavior. |
| P2 | Offer a useful human handoff primitive | Hosted browser products make live viewing or takeover part of login/2FA workflows; this is a meaningful operational convenience rather than another low-level browser verb. | Go now exposes `HandoffToHumanContext`: the host supplies its own viewer/approval callback, receives the current target and redacted URL, and Scout rechecks target identity plus an optional postcondition after resumption; callback errors, target drift, or failed readback return `outcome_unknown`. No viewing transport or credential fields are added to Scout. | The API compiles and the integration guide describes the flow. A live login/2FA handoff, target-drift behavior, and credential-log audit remain unverified. |
| P1 | Make browser lifecycle health visible and bounded | Playwright MCP reports have covered idle streams masking abandoned sessions, per-client context cleanup, and profile lock collisions; agent-browser reports cover renderer stalls and restart-during-recording gaps. Some were fixed upstream, which makes them good regression scenarios. | Expose per-session last protocol activity, idle deadline, ownership, isolation policy, cleanup result, and bounded shutdown outcome; support an operator cap on active sessions and a health endpoint/hook for host schedulers. `status` and `list_sessions` now expose whether a session is configured for an isolated remote BrowserContext; idle deadlines, activity, session cap, and starting/closing counts are also available to MCP hosts. | Keep a stream open with no calls, disconnect one of multiple clients, restart a browser during capture, and interrupt a long mutation; assert cleanup and surviving-client state. Startup/close counts are surfaced, but lifecycle fault-injection and cleanup-result aggregation still need evidence. |
| P1 | Treat diagnostics as a completeness contract | Competitor issues show success-shaped empty traces/HARs, renderer disruptions, iframe attachment gaps, and output-bound bypasses can leave agents with incomplete evidence. | Return explicit completeness/coverage metadata for traces, network logs, snapshots, and bounded outputs, with a reason when capture is partial or restarted. Trace archives now add `network-manifest.json` with the same coverage vocabulary as live summaries plus export-specific capture state, enabled/ever-enabled state, filter presence, retained/pending/dropped counts, retention limit and interruption status, plus `evidence-manifest.json` with per-event screenshot statuses, expected/written counts, status/counter mismatches, and unattached capture attempts when no event was emitted. Live `network_summary` now returns the same session-window distinctions: never enabled, known gaps, disabled at read, or active with no known loss; it also reports filtering and retention limit. Existing `network.json` and `trace.json` formats remain intact. | Inject renderer loss, browser restart, inaccessible iframe, and oversized JSON/text outputs; verify the caller can distinguish complete evidence from partial evidence. The network manifest and summary report the session capture window only; the evidence manifest reports screenshot capture gaps, not page-wide diagnostic completeness. `active_no_known_loss` is not a claim of complete browser-wide visibility. |

### Product choice

Prioritize **recoverable, isolated browser operations** as the differentiator. Context efficiency and run-level observability should support that promise. Defer building a hosted browser fleet, CAPTCHA solving, a general autonomous agent, or provider-specific anti-bot features: those would put Scout into direct infrastructure competition with Browserbase, Browser Use and Skyvern. Integrate with those layers when customers need them.

The next competitive proof should be a public, reproducible benchmark pack with matched tasks, fault injection, repetitions, pinned tool/browser versions, and raw outputs. Keep outcome correctness separate from latency and output-size comparisons when adapters measure different boundaries. Include at least: late mutation after timeout, multiple concurrent sessions, long-page targeted extraction, idle cleanup during in-flight work, and recovery after process/browser reconnect. Shared-remote-browser ownership now has a focused Scout contract probe: two HTTP clients attached to one external CDP browser read and overwrite the same cookie/localStorage state and selected the same target ID in a 6 November run (raw result (local audit artifact)). That is evidence of the current trust boundary, not an isolation feature or general reliability advantage. A single local run is a contract check, not evidence of a general reliability advantage.

### Issue and product sources

- [Playwright MCP #1631: shared BrowserContext cross-client page bleed](https://github.com/microsoft/playwright-mcp/issues/1631)
- [Playwright #42961: per-client ownership for shared persistent contexts](https://github.com/microsoft/playwright/issues/42961)
- [Stagehand #1767: documented server caching reported as repeated misses](https://github.com/browserbase/stagehand/issues/1767)
- [Stagehand open issue tracker](https://github.com/browserbase/stagehand/issues), which includes recent reports on iframe attachment, active-page selection and timeout behavior; validate status against the pinned release before drawing conclusions.
- [agent-browser #1713: command deadlines and recovery on wedged sessions](https://github.com/vercel-labs/agent-browser/issues/1713) and [#2290: HAR result after browser restart](https://github.com/vercel-labs/agent-browser/issues/2290); these are open issue reports and motivate Scout's deadline and diagnostic-completeness contracts, not a general product comparison.
- [Playwright MCP tracing](https://playwright.dev/mcp/tools/tracing) and [snapshot controls](https://playwright.dev/mcp/snapshots)
- [Browserbase Live View](https://docs.browserbase.com/features/live-view) for human inspection and takeover of hosted sessions.

## Capability comparison

| Capability | Scout | Playwright MCP | Vercel agent-browser | Stagehand | Browser Use |
|---|---|---|---|---|---|
| Main interface | Go library, CLI, MCP, chat UI | MCP over Playwright | CLI/daemon, MCP, skills | TS/Python/Go SDK and hosted service | Python agent library and hosted cloud |
| Deterministic browser control | Direct CDP and typed Go API | Playwright APIs/tool calls | CLI commands and semantic refs | Playwright-style APIs plus AI helpers | Browser automation inside autonomous loop |
| AI-assisted interaction | Observation, semantic forms, coordinate grounding, chat loop | Snapshot/vision tools for an external agent | Snapshot/refs and agent workflows | `act`, `observe`, `extract`, agent mode | Built-in task agent, vision and DOM reasoning |
| Output/context controls | Bounded observations, diffs, distilled content | Snapshot subtree/depth/search/file options; capability groups | Snapshot scoping, output limits, batching | Hybrid snapshots, caching and batching | Agent-managed state and cloud task execution |
| Session/state | Profiles, tabs, playbooks; caller-supplied target assertions on selected page-scoped MCP tools | Isolated contexts and browser configuration | Persistent sessions, tab pinning, restoration and recovery | Browser contexts and hosted sessions | Local or cloud browser state |
| Failure evidence and telemetry | Typed action outcomes, health snapshot, Go reconcile API, CLI recovery file and MCP reconcile tool; Go caller-owned OTel provider plus opt-in CLI/MCP OTLP/HTTP export | MCP tool errors, Playwright traces and diagnostics | Structured `tab_gone`, session recovery and diagnostics | SDK errors, logs and advertised OpenTelemetry support | Agent/task-level status and traces |
| Deployment | Single Go binary; browser supplied separately | Node ecosystem or packaged MCP runner | Installable CLI and browser | SDK runtime; optional Browserbase | Python runtime; optional hosted service |

Sources: [Scout repository](https://github.com/klarlabs-studio/scout), [Playwright MCP capabilities](https://playwright.dev/mcp/capabilities), [Playwright snapshots](https://playwright.dev/mcp/snapshots), [agent-browser session management](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md), [Stagehand](https://stagehand.dev/), [Browser Use](https://github.com/browser-use/browser-use).

This is a product-level comparison, not a claim that each feature was tested against every release. Scout's CLI cross-process v2 recovery and MCP record/reconcile paths now have focused local browser benchmark scenarios, including delayed upload and final-step batch effects after a timeout. Those are contract checks, not comparative or repeated reliability results. See [benchmark comparison notes](../examples/benchmark/COMPARISON.md) and the focused recovery report (local audit artifact).

The benchmark report utility now normalizes archived Scout, agent-browser, and Playwright MCP comparison summaries while retaining version, fixture URL, verification predicate, repeat count, timing scope, output-byte definition, and per-repetition calls. A fresh focused 6 November 2026 run now compares Scout Go API, agent-browser 0.41.2, and Playwright MCP 0.0.84 on the same `verified-action` fixture, Chrome 157, and exact postcondition; all three passed 10/10. The normalized matrix marks their outcomes comparable and timing/output incomparable because the adapters measure a Go postcondition call, four CLI subprocesses, and four MCP calls respectively, with different byte boundaries. Scout returns a native `postcondition_verified` result; the competitor adapters establish the same external page predicate through their supported interfaces. This supports outcome parity and the value of first-class postcondition evidence, not a speed ranking or broad reliability claim. The historical archived full-suite reports remain unsuitable as a matched comparison. See the [focused protocol and raw report paths](../examples/benchmark/COMPARISON.md#competitive-comparison-protocol).

**Receipt verification follow-up (6 November 2026):** the focused comparison now fails if Scout's Go, one-shot CLI, and MCP results omit or disagree on the receipt's `verification_state`. Scout Go API, agent-browser 0.41.2, and Playwright MCP 0.0.84 each passed the same exact-text postcondition in 10/10 fresh runs on HeadlessChrome 157. The scorecard (local audit artifact) records outcome parity and explicitly marks timing and output comparisons incomparable. Only Scout emits the normalized action receipt in this adapter set; the competitor adapters validate the resulting DOM predicate externally. This demonstrates consistency of Scout's receipt across three Scout interfaces plus matched outcome parity with these pinned competitors for one task. It does not establish receipt-field parity, reliability leadership, or performance superiority.

## What recent GitHub reports suggest

Issues and PRs are useful sources for adversarial test cases. They are not representative samples and should not be quoted as bug rates.

- [Playwright issue #41617](https://github.com/microsoft/playwright/issues/41617) discussed the first failed call after a remote CDP browser disconnect and the risk that an agent abandons authenticated state. It was closed after maintainers clarified a later call can recover. This is a session continuity problem, not evidence that Playwright cannot reconnect.
- Agent-browser issue #2422 (opened 5 November 2026) reports that a per-tool `session` can override the MCP server's configured session. The 6 November tracker recheck found proposed PR #2430 still open with a fix and argument-bypass guard; treat this as a regression lead for v0.41.2, not a durable competitor weakness. Issue [#2293](https://github.com/vercel-labs/agent-browser/issues/2293) (opened 1 November; open when checked 6 November) reports missing console, network, HAR, and trace evidence for direct-page connections because session-less CDP events are filtered out. These are issue reports, not prevalence estimates. Scout already namespaces its local HTTP session registry per MCP transport session and binds page calls to the session/target captured at request start. Its CDP client attaches to browser targets through `Target.attachToTarget`; direct page-WebSocket provider mode is not evident in the current code. Treat direct-page event attribution as a future compatibility requirement if Scout adds that mode, and measure target isolation for multiple Scout sessions attached to one shared remote browser separately.
- Playwright issue [#42423](https://github.com/microsoft/playwright/issues/42423) documented trace archives becoming metadata-only after a renderer disruption, with subsequent MCP calls still appearing successful. It was closed as completed in [#42425](https://github.com/microsoft/playwright/pull/42425), so treat it as an upstream regression test rather than a current competitor weakness. The product lesson remains useful: an operation receipt should identify page/context replacement and trace health, and tracing should report whether the exported artifact contains usable action evidence instead of treating file creation as proof of a valid trace.
- [Playwright MCP issue #1599](https://github.com/microsoft/playwright-mcp/issues/1599) reported a stale crashed-page reference leaving later operations unusable. It was closed with upstream work. Keep renderer crash and stale target as regression scenarios.
- Playwright MCP issue [#1631](https://github.com/microsoft/playwright-mcp/issues/1631) reported page events leaking across clients in a shared browser configuration and was closed with guidance to use isolated contexts or separate user-data directories. A newer Playwright issue, [#42961](https://github.com/microsoft/playwright/issues/42961), raises the opposite need: preserve a shared authenticated profile while assigning tabs and cleanup to individual clients. The report was archived/closed without an upstream implementation commitment. This is a real product trade-off—isolated storage vs. shared login state with page ownership—not a simple “more isolation is better” requirement. Scout's HTTP clients currently receive separate Scout session registries, while clients attached to the same external remote CDP browser still share browser state. Validate this use case with Go teams before adding shared-profile ownership semantics.
- Playwright MCP's [isolated-context disconnect issue #41539](https://github.com/microsoft/playwright/issues/41539) documented leaked per-client contexts while another HTTP client stayed connected. Separately, [issue #1795](https://github.com/microsoft/playwright-mcp/issues/1795) reported idle browser processes surviving proxy-held streams; it was closed by [Playwright change #43527](https://github.com/microsoft/playwright/pull/43527), and MCP v0.0.84 release notes now list per-client idle timers. Current docs say each isolated client has its own timer and the timer does not fire during a tool call ([idle-timeout contract](https://playwright.dev/docs/getting-started-mcp#idle-timeout)); [issue #42693](https://github.com/microsoft/playwright/issues/42693) reports a regression where an in-flight call was closed, but it was closed as not planned. Treat lifecycle behavior as a parity bar, not an active competitor weakness. Scout's HTTP session DELETE removes that client's browser/config registry after in-flight tools drain. The process-level fixture passed 4/4 fresh runs, returning HTTP 204 and observing the Scout-launched Chrome process exit. A second fixture held open SSE streams for two clients: over 3/3 runs, the inactive client's browser exited while the active client's browser stayed alive, and the inactive client could recreate an isolated session. See the [benchmark notes](../examples/benchmark/COMPARISON.md). These are Scout contract checks, not matched competitor results. Silent disconnect without protocol DELETE and DELETE concurrent with a long mutation remain follow-ups.
- [agent-browser issue #1713](https://github.com/vercel-labs/agent-browser/issues/1713) reports commands hanging without per-command deadlines and process cleanup. Follow-on [PR #1851](https://github.com/vercel-labs/agent-browser/pull/1851) is still open and GitHub shows no deployment; its proposed 30-second CDP handshake deadline would address the silent-WebSocket-upgrade case, but it is not yet evidence of released behavior. Separately, [PR #2396](https://github.com/vercel-labs/agent-browser/pull/2396) merged on 5 November 2026 and centralizes session-setting reuse/relaunch decisions. It reports setting changes and browser relaunches explicitly, addressing an earlier class of silent relaunch/session-loss concerns. Recheck the released version before comparison; do not present issue #2122 or PR #1851 as an enduring product gap.
- Current [agent-browser issues](https://github.com/vercel-labs/agent-browser/issues) list background-tab closure clearing active refs/frame scope (#2407) and an MCP `session` argument overriding `AGENT_BROWSER_SESSION` (#2422). The 6 November 2026 snapshot shows both open with proposed fixes (#2408 and #2430); PR #2430 now includes an argument-bypass guard but is not merged. Against release v0.41.2, the corrected focused CLI scenario reproduced `Unknown ref: e1` in 10/10 repetitions after the task tab was reselected, the iframe ref captured, and a separate tab closed. Median end-to-end time to failure was 9.32 s and p95 10.39 s, including separate CLI startup for each step; this timing is not comparable with MCP or Go API workflows. In the same fixture, Scout's Go API and Playwright MCP 0.0.84 both completed the frame postcondition in 10/10 runs. Scout recorded target/frame IDs; Playwright's adapter could not expose target IDs. This supports a version-specific regression and instrumentation distinction, not a broad reliability ranking. Rerun when an upstream fix ships. Scout's multi-client CLI parity is implemented, so the older claim that its CLI is one-shot is stale. See the [matched protocol and raw-result paths](../examples/benchmark/COMPARISON.md#frame-scope-after-closing-an-unrelated-tab). Agent-browser's release page still listed v0.41.2 as latest in this research snapshot; recheck before external positioning. Its output-budget issue #2246 remains a hypothesis for the distinct JSON/tool-text route, not proof that every MCP response is unbounded.
- Current [Stagehand open issues](https://github.com/browserbase/stagehand/issues) include `activePage()` selecting the oldest page instead of the active page ([#3371](https://github.com/browserbase/stagehand/issues/3371)), CLI wait deadlines not respected ([#3369](https://github.com/browserbase/stagehand/issues/3369)), cross-site iframes loaded after natural-language `act()` missing from snapshot/extract ([#3321](https://github.com/browserbase/stagehand/issues/3321)), and locator latency on large lists. These suggest parity fixtures for tab/frame identity, deadline propagation, and scaling. They are reports, not verified current-release failures.
- [Browser Use issue #5067](https://github.com/browser-use/browser-use/issues/5067) described renderer crashes being surfaced as retries/timeouts instead of a clear crash outcome. The issue reports a fix in discussion; treat it as a crash-detection fixture, not an enduring product weakness.
- More recent Browser Use reports include [#5747](https://github.com/browser-use/browser-use/issues/5747) on long-task degradation from repeated DOM serialization/state fetching and growing message history. Open [PR #6304](https://github.com/browser-use/browser-use/pull/6304) proposes attaching its MCP server to an existing CDP session and [PR #6298](https://github.com/browser-use/browser-use/pull/6298) targets recovery-session reuse. These changes narrow Scout's integration and recovery whitespace; recheck merge/release status before making claims.

**Research update (5 November 2026):** The current agent-browser issue list adds a useful interop case: [#2293](https://github.com/vercel-labs/agent-browser/issues/2293) reports direct-page CDP connections where console/network/HAR events are missed and trace/profiler stop can time out because events lack a `sessionId`. It is open with no linked PR at this snapshot. Scout's current `RemoteCDP` path attaches to browser-level endpoints and does not claim page-level direct-WebSocket support; benchmark those endpoint modes separately before promising provider compatibility. The broader opportunity is a diagnostic receipt that says which event sources were actually observed, so “no console errors” cannot be confused with “console capture unavailable.” Another new report, [#2431](https://github.com/vercel-labs/agent-browser/issues/2431), describes roughly six-second `--cdp` connection latency with multiple discarded tabs; treat it as a responsiveness probe, not a general latency result. Meanwhile, agent-browser's changelog says unresponsive tabs now surface `page_wedged` and can be replaced after a second timeout, and `connect` waits up to two seconds for a debugging port ([changelog](https://github.com/vercel-labs/agent-browser/blob/main/CHANGELOG.md)). Those shipped behaviors raise the parity bar and reduce the value of claiming generic recovery as unique.

**Research update (6 November 2026 — MCP lifecycle):** Two recent Playwright reports make session cleanup a more specific benchmark target. [playwright-mcp #1795](https://github.com/microsoft/playwright-mcp/issues/1795) reported abandoned isolated browsers when an HTTP/SSE connection stayed open without MCP tool calls; the issue was closed as completed via [playwright #43527](https://github.com/microsoft/playwright/issues/43527) on 19 October. [Playwright #42693](https://github.com/microsoft/playwright/issues/42693), by contrast, reported a regression where the idle timer closed a browser during a tool call longer than the timeout; it is closed as not planned. These reports are version- and implementation-specific, not evidence of current general failure. Together they define the lifecycle invariant buyers need: protocol keepalives must not count as useful work, while a tracked in-flight tool call must prevent idle cleanup.

Scout's current HTTP MCP implementation appears to satisfy both sides at the local code level: its reaper expires sessions only when `inFlight[name] == 0` and the last tool activity exceeds the configured idle timeout; middleware increments the in-flight count at tool start and refreshes activity at start and completion; `SCOUT_MAX_SESSIONS` bounds new session creation. Existing probes cover held-SSE idle expiry across two HTTP clients and long calls over stdio and Streamable HTTP. The matched 6 November comparison used the same `/delayed-element?delay_ms=1800` fixture and 1 s idle timeout in both products. Scout survived the 1.8 s wait and expired normally after the call in 5/5 runs; Playwright MCP 0.0.84 closed its context during `browser_wait_for` in 5/5 fresh-process runs at 1.012–1.029 s. This directly reproduces [Playwright issue #42693](https://github.com/microsoft/playwright/issues/42693), though npm's latest package that day was still 0.0.84. At a 5 s timeout Playwright passed 5/5, so the opportunity is a resilience contract for configured low idle thresholds and long operations. The result is version-specific, does not establish population-wide superiority, and measures neither memory nor Playwright post-call cleanup. Follow up with a contract regression suite on later releases and cleanup-latency/resource telemetry. Also exercise remote-CDP disconnect separately: closing a Scout session must release Scout resources without closing the caller-owned browser. This is a higher-value opportunity than adding more browser verbs because it addresses a concrete reported failure mode and matters directly to hosted MCP gateways.

Browser Use issue [#5252](https://github.com/browser-use/browser-use/issues/5252) remains listed open at the checked snapshot, although its activity references fixes for reporting exceptions with `isError=true`. Before presenting it as an active gap, verify the exact released package and protocol behavior. The durable lesson for Scout is to retain two independent dimensions: MCP invocation status (`isError`) and browser-side effect outcome (`verified`, `not observed`, or `unknown`). A protocol failure after dispatch does not prove that no side effect occurred.

Interpretation: upstream issue queues are most useful as test generators. The recurring themes across projects are target selection, deadlines, session recovery, large-page/context cost, direct-CDP event completeness, and output consistency. Scout should convert those into fixed adversarial fixtures and compete on measured results, rather than market a competitor's unresolved issue as an advantage.

## Ranked opportunities

### 1. Make uncertain outcomes safe to resolve

Scout already has the seed of a differentiator: an action timeout can be reported as `outcome_unknown`, preserve a postcondition and target, and be reconciled without replaying the mutation. The CLI stores a versioned recovery record, while Go and MCP have reconciliation entry points. MCP `click`, `navigate`, and `type` accept per-action `timeout_ms`; `submit_form` accepts `action_timeout_ms` separately from its response-observation `timeout_ms`. These let the action return its typed outcome and recovery record before the global MCP watchdog deadline. Uncertain navigation, typing, and form submission preserve the caller's page or field predicate when supplied. The focused local MCP fixture exercises late click, type, and submit effects and reconciles each record. **Additional coverage added:** `click_label`, `click_handle`, `click_text`, `dispatch_event`, `drag_drop`, `double_click`, `right_click`, `hover`, `select_option`, `scroll_to`, `scroll_by`, and `focus` accept bounded per-action `timeout_ms`; `hover` now also accepts a page postcondition for tooltip/menu state. `click_text` results also expose the session operation ordinal. The live MCP late-effect fixture covers `click_label`, `click_handle`, `click_text`, `dispatch_event`, `drag_drop`, `select_option`, and `hover`: all seven returned `outcome_unknown`, retained the expected predicate and target, then reconciled to `postcondition_verified` on the same target in a 6 November 2026 run (captured report (local audit artifact)). For `select_option`, Scout resolves the option's exact value before dispatch; for `hover`, callers can assert delayed tooltip/menu content. A late `change` or pointer handler can therefore be reconciled against the intended page condition. The same run also covered `upload_file`: the browser selected a fixture file, its `change` handler blocked past the 300 ms action deadline, and Scout returned a recovery record whose target and expected filename reconciled to `postcondition_verified` without replay. The Go upload path now also checks caller-supplied `target_id` before dispatch, matching the MCP tool's advertised target-binding contract. This verifies local input selection and recovery, not site-side upload acceptance or repeated reliability. Other tab-changing actions, cookie mutations, profile/session changes, and semantic form fills still need controlled late-effect fixtures. Calls must allow enough time after the per-action deadline to deliver the recovery record.

Measure: false-success rate, late mutation rate after timeout, reconciliation correctness, time to safe resolution, and whether recovery ever replays a non-idempotent action. Include click, form submit, navigation, and file upload.

### 2. Attribute every result to its actual browser target

Bind each operation to caller/session, target, frame, and action identity. Return the binding alongside observations and errors. Make stale references fail with an explicit, recoverable result. **Progress:** page-scoped tools accept and validate `target_id`; the MCP request middleware pins each operation to the session and target captured at request start. Scout exposes Streamable HTTP as well as stdio. The HTTP registry now namespaces default and named session keys by the stateful MCP session ID; stdio retains its original process-local names. Five fresh two-client runs passed 5/5: the target race refused the stale click, client two could create the same public session name as client one without collision, and it could not switch to a session name created only by client one. Both clients retained distinct target IDs. This verifies local HTTP session isolation. Remote CDP endpoints still point at shared external browser state, and the bearer token is a shared secret rather than user identity. A Go API frame-scope fixture passed 5/5 after closing an unrelated tab; the stateful JSONL CLI now also runs a multi-step frame-close workflow with target/frame checks. The CLI focused case has one successful run, so repeat it before claiming three-surface reliability parity. Evidence is local, not competitor evidence.

**HTTP deployment boundary:** `scout mcp serve-http` listens on loopback by default. Binding a non-loopback interface requires bearer authentication through either `SCOUT_MCP_HTTP_TOKEN` or `SCOUT_MCP_HTTP_PRINCIPALS_JSON`. The latter maps distinct tokens to stable principals and configured remote CDP endpoints; session registries, configuration, and DELETE cleanup are scoped to the principal plus MCP transport session. It rejects identical normalized CDP URLs assigned to multiple principals, preventing the most direct shared-endpoint misconfiguration. URL aliases and reverse-proxy routes can still lead to the same Chrome instance, so this is a startup guard rather than browser-context isolation. Dedicated-endpoint routing, origin enforcement, cross-principal session binding and cleanup passed the five-run local matrix in the principal isolation report (local audit artifact). Token rotation, alias detection, shared-browser target ownership, and external egress enforcement remain open.

**Opportunity: ownership for shared remote browsers.** Scout's default `SCOUT_REMOTE_CDP` remains a process-wide shared endpoint, and a focused local probe confirmed that two HTTP clients on it select the same target and read/write the same cookie and localStorage state (raw result (local audit artifact)). Scout now offers `SCOUT_MCP_HTTP_PRINCIPALS_JSON` to map authenticated bearer credentials to per-principal remote endpoints and to bind transport-session use and cleanup to the authenticated principal. The map rejects identical normalized URLs for multiple principals, but cannot detect aliases or proxy routes to one Chrome. It does not provide browser-context isolation: callers that reach the same browser still share tabs and state. The Go [`principal_browser_gateway`](../examples/principal_browser_gateway/main.go) remains useful when the host needs application-native authentication, origin policy, or persistent session management. Never treat `Mcp-Session-Id` as the principal or let callers choose arbitrary endpoints. Dedicated endpoint routing and principal/session cleanup now have five fresh-run local evidence; token rotation/revocation, alias detection, target ownership inside one shared authenticated profile, and matched competitor comparisons remain open. See [the integration guide](go-service-integration.md#browser-ownership) and the [shared-state probe](../examples/benchmark/COMPARISON.md#mcp-http-shared-remote-ownership).

**HTTP lifecycle:** MCP `DELETE` tears down the transport session and asynchronously closes that client's Scout browser sessions after tracked tool calls finish; it also discards client-scoped config. Process-level fixtures passed 4/4 explicit DELETE runs and 3/3 held-SSE idle runs, including active-client survival and isolated session restart. Silent disconnect without DELETE, server crash, and DELETE during a long mutation remain unmeasured. Playwright v0.0.84 documents per-client idle timers and protection for in-flight calls, so Scout still needs matched tests before claiming an advantage.

Measure: wrong-target action rate and evidence contamination under concurrency. Compare matched cases against current pinned Playwright MCP and agent-browser releases.

### 3. Make bounded evidence consistent and easy to discover

Scout already implements many of the primitives: a 256 KiB MCP response ceiling, bounded page reads and extraction, truncation metadata, capped screenshots and network pages, and private saved snapshots with offset reads/search. The MCP server offers `core`, `read`, `interact`, `diagnose`, and `admin` tool profiles; `all` remains the compatibility default. A 6 November paginated stdio capture records 103 tools and 115,873 catalog bytes for `all`; the recommended `core` has 48 tools and 57,523 bytes (50.4% fewer), `read` 44 and 46,081 (60.2% fewer), `interact` 60 and 72,575 (37.4% fewer), `diagnose` 50 and 57,752 (50.2% fewer), and `admin` 29 and 30,665 (73.5% fewer). The v2 capture verifies that `configure` is callable in `all` and unavailable in `core` and `read`; it also calls the read-only `help` tool in all six profiles, verifies its advertised tools match each complete catalog, and checks that every recipe step is exposed and that empty recipes are omitted. It returns only supported recipes (6–9 per profile, with 14–34 distinct recipe tools) and communicates verification and retry guidance, but has not been evaluated for model task success. See the raw catalog report (local audit artifact) and the [reproduction protocol](../examples/benchmark/COMPARISON.md#mcp-profile-catalog-baseline). Byte/4 token figures are only rough estimates; no model-driven task selection or success evaluation has been run. Profiles are discovery and workflow controls, not authorization.

Measure: output bytes/tokens at p50/p95, truncation detection, evidence retrieval success, task success under equal budgets, and discrepancies between text and JSON/MCP paths. Use agent-browser #2246 as a regression-inspired case; verify its status before publishing comparison claims.

### 4. Publish a browser execution scorecard

The benchmark should reward correctness and efficiency together, not raw feature count. Scout already has local fixtures for outcome checks, scoping, forms, deadlines, target loss, and recovery. Turn these into pinned, repeatable adapters with raw artifacts.

Report task success, false-success rate, p50/p95 latency, deadline overshoot, action count, result bytes/tokens, cleanup time, and recovery rate. Keep model-driven benchmark results separate from deterministic browser-layer tests.

### 5. Be the easiest embedded runtime for Go services

Scout's one-binary CLI/MCP plus Go API is a coherent deployment story for Go teams. **Adoption material shipped:** the worker-pool example gives each worker an independent browser, validates the receipt schema, run ID, operation ordinal, target ID, outcome, and verification state before emitting success, and cancels/cleans up on shutdown. The new [Go service integration guide](go-service-integration.md) covers request/job contexts, remote-CDP ownership, egress enforcement boundaries, OpenTelemetry, and runtime sizing. A live local browser run with two workers and four jobs emitted four target-bound `postcondition_verified` receipts and exited cleanly. The worker-pool example is also listed among the integration workflow's executable examples.

**6 November shutdown check:** the worker-pool example treats SIGTERM/SIGINT cancellation as an orderly stop. A live run completed 4/4 verified jobs across two workers; a signal during active browser work exited 0 with no stderr. `Session.CloseContext` bounds waiting for the session lock, screen-recorder shutdown, and page close/detach. For a local browser, it sends CDP `Browser.close`, closes Scout's connection, waits to the cleanup deadline, and force-kills Chrome if needed; remote CDP only disconnects Scout. Two process-scoped macOS probes now found no remaining Chrome processes for either temporary profile after normal completion or SIGTERM, closing the helper-process leak observed in the initial audit for this Chrome build and shutdown path. It cannot take a session handle if an operation holds the session lock past the cleanup deadline, and injected browsers must bound their own legacy `Close` implementation. Preserve unknown mutation outcomes in the host's job state before acknowledging or retrying interrupted work. See the [service integration guide](go-service-integration.md) for the close contract and limits.

The next proof is adoption, not another sample: validate this with 5–10 Go/platform teams. Ask about deployment friction, browser ownership, authentication, reliability incidents, and whether portable recovery data would justify adoption or budget. Docker scratch/distroless recipes, structured logger hooks, and production resource measurements remain open.

### 6. Measure task-sized tool surfaces

Playwright MCP capability groups and agent-browser's default `core` profile show that a large surface should not be the starting point for every agent. Scout now has a curated 48-tool `core` profile that cuts catalog bytes by 50.4% from `all`; new MCP examples recommend it, while the full catalog remains available for compatibility. The capture verifies catalog transfer, call gating, and recipe/catalog consistency. It does not yet show fewer measured model tokens, better tool choice, or equal task success. Compare the profiles on matched representative tasks with a pinned model and actual token accounting before making those claims.

Measure schema tokens, tool selection success, calls-to-completion, and output size on a fixed task set. Do not assume an MCP capability profile is beneficial until an agent evaluation confirms it.

### 7. Integrate with hosted browser providers without becoming one

Browser Use, Browserbase, and other hosted products compete on managed browsers, scaling, and site access. Scout can keep browser ownership flexible by making remote CDP/provider support first-class, and provide adapters without tying its core value to one vendor. Prioritize only after interviews show this is a buying blocker.

## Recommended sequence

1. Close recovery contract gaps: enumerate every mutating tool, add explicit deadlines and postconditions where missing, and verify Go record round-trip plus late-effect reconciliation.
2. Finish the same-client concurrency/lifecycle matrix, including remote CDP, HTTP DELETE during a slow mutation, silent disconnect, and server crash; compare with pinned Playwright MCP and agent-browser on identical predicates.
3. Finish the bounded-evidence inventory and verify output limits and retrieval paths across adapters; evaluate the implemented profiles on matched agent tasks with equal models and budgets, recording actual tokens, tool-selection errors, calls to completion, and task success.
4. Repeat Playwright MCP isolated/shared idle probes with a fresh server per repetition. Existing artifacts are one run per mode and support contract observations only; reusing one server stalled before a repeated report.
5. Publish the scorecard with exact versions, browser builds, adapters, raw output, repetitions, confidence limitations and unsupported cases.
6. Validate the Go runtime and recovery propositions with prospective users; prioritize based on actual adoption blockers before expanding provider integrations.

## Positioning to test

> Scout is a small, embeddable browser runtime for teams that need agents to show what happened and recover safely when a browser action times out or the target changes.

Treat this as a hypothesis. The proof needed is a matched benchmark and customer interviews demonstrating that attribution and safe recovery solve a costly problem. Avoid claims such as “most reliable,” “lowest token cost,” or “best MCP browser” until the comparative evidence exists.

## Implementation update: interrupted semantic form fills

When `fill_form_semantic` or selector-based `fill_form` becomes uncertain after one or more field mutations, the shared error envelope carries `partial_result`: completed field outcomes, requested/observed values, and the in-flight field's unknown outcome when available. A live late-effect batch fixture found and fixed two gaps: when the deadline expired on the final batch action, `ExecuteBatchContext` returned no error, so MCP emitted an ordinary result; and the unknown step's action receipt omitted the caller's postcondition. Batch now returns the deadline error after recording the final partial step and attaches the validated predicate to both the typed outcome and receipt. The raw MCP envelope was verified as `SCOUT_OPERATION_INTERRUPTED`; the first action was complete, the last remained unknown with its target and expected text preserved, and `reconcile_action` verified the late effect on that same target without replay. The fixture covers batch timeout behavior; semantic form-fill late effects remain unverified. Portable recovery records omit form partial results because values may be sensitive, while the immediate error can carry them. See the live recovery report (local audit artifact).

## Implementation update: machine-readable MCP errors

Scout's MCP server preserves the existing `SCOUT_ERROR {json}` text block and adds the parsed envelope under `_meta["go.klarlabs.de/scout/error"]`. A live stdio recovery fixture now confirms the wire result sets `isError=true`, carries the machine-readable metadata for `SCOUT_ACTION_OUTCOME_UNKNOWN`, and still reconciles the same target after late click, type, and submit effects. The open-source Go SDK convenience `ToolResult` currently retains `IsError` and content but drops result `_meta`; raw `protocol.Response` is required to inspect the metadata in Go. A middleware regression check confirms an oversized tool error is replaced by a bounded `SCOUT_OUTPUT_LIMIT` result while preserving `isError=true`. These checks do not yet generate an oversized `partial_result` through a live browser workflow, nor measure peak memory while the middleware serializes a large intermediate result. See the live stdio report (local audit artifact).

### Competitive refresh (6 November 2026)

The current official documentation sharpens the competitive bar. Playwright MCP exposes optional capability groups to control the tool catalog size, and its devtools capability can record traces live with before/after DOM snapshots, screenshots, network activity, console messages, and timings ([capabilities](https://playwright.dev/mcp/capabilities), [tracing](https://playwright.dev/mcp/tools/tracing)). Scout already has MCP profiles and trace archives; the gap is making evidence equally immediate and navigable inside a host session, then measuring its bytes and decision usefulness against Playwright's trace viewer.

Agent-browser's changelog describes an out-of-process plugin protocol with provider plugins, launch mutators, custom command capabilities, and capability-scoped policy gates, as well as CA-bundle configuration that retains certificate verification ([changelog](https://agent-browser.dev/changelog)). That raises the bar from a single browser driver to a controllable extension surface. Scout should first publish explicit extension seams for browser providers and policy hooks, while keeping the core contract small and testable; do not add arbitrary plugin execution until trust boundaries, permissions, and lifecycle isolation are specified.

Stagehand positions its cross-language SDK around `act`, `observe`, and `extract`, and now advertises TypeScript, Python, and Go support ([Stagehand](https://www.stagehand.dev/)). Natural-language convenience and language reach are weak differentiators for Scout. The stronger opening is a Go-native, deterministic systems layer that can sit underneath agent frameworks and return a stable verified/unknown/rejected receipt, deadline, and recovery predicate. A fair comparison should measure identical task completion and safe-recovery outcomes, plus integration effort for a Go service, rather than compare API names.

These opportunities are conditional on current docs and must be rechecked before launch claims. Immediate research actions: run a matched trace task against Playwright; ask several Go-service users to integrate Scout's receipt/recovery API without bespoke glue; and prototype provider/policy interfaces behind an explicit trust model. The recommendation is to lead with **safe resolution of uncertain browser mutations**, supported by portable evidence, while treating trace UX, extension breadth, and natural-language features as parity or integration work.

### Lifecycle and evidence integrity refresh (6 November 2026)

The current issue scan strengthens two follow-on bets. First, lifecycle must be measured in terms of browser work, not merely an open transport: a keepalive stream can outlive useful activity, and per-client cleanup must not depend on the last client leaving. Scout exposes last activity, idle deadlines, in-flight/start/close counts, and an operator-configured active-session cap. MCP process shutdown now applies one shared 10-second budget to waiting for idle/client cleanup and closing registered sessions, logs close failures, and returns joined cleanup errors to the caller. Fault-injection for idle cleanup, long-running calls, and shutdown remains needed to verify the lifecycle contract under failure. Second, evidence needs an explicit completeness state. An empty HAR, missing trace archive, skipped iframe, or output path that bypasses bounds can look like a valid but uninformative result. Scout already reports truncation on several read surfaces and has typed outcome receipts; extend the same idea to diagnostic artifacts and make the metadata available to hosts.

This scan also narrows the positioning: the competitive win is not that upstream tools have lifecycle bugs (several cited examples are already fixed), but that Scout can make resource ownership, cleanup, and evidence completeness part of its stable caller-facing contract. Prioritize a small operational scorecard—abandoned-session cleanup, per-client disconnect, browser restart, and partial-evidence detection—alongside the mutation-recovery scorecard. Track exact upstream versions and issue status in the fixture metadata so stale competitor regressions cannot accidentally become marketing claims.

**Implementation progress:** the focused `mcp-http-principal-isolation` scenario passed five fresh server/browser runs (5/5). Two authenticated clients were routed to their configured dedicated CDP endpoints; cross-origin navigation and cross-principal MCP session-ID reuse were rejected; principal A retained access after the rejected attempt; and principal B remained usable after principal A's explicit MCP `DELETE`. Local fixtures enable private-IP access to reach loopback test servers. Scenario durations (excluding the temporary CLI build) had a 1.31 s median and a 1.21–1.50 s range; raw results are in the five-run report (local audit artifact). Startup also rejected duplicate normalized CDP URLs across principals. This is contract evidence for dedicated endpoints and one local server build, not evidence for shared-browser ownership, token rotation, egress enforcement, or competitor superiority.

## Implementation update: failed tab-open cleanup

`open_tab` previously ignored failures while closing a newly created target after URL-policy setup, navigation, or readback failed. The MCP error adapter would then report only the primary error, even when the browser target might remain open but absent from Scout's tab registry. These paths now return `ActionOutcomeError` with `outcome_unknown`, the target ID, the cleanup failure, and inspect-before-retry guidance when target closure cannot be confirmed. Successful cleanup still returns the original operation error. `go build ./...` and `git diff --check -- agent/tabs.go` pass. The timeout/cleanup-failure branch still needs a controlled browser fixture that proves the target ID and subsequent tab inventory behavior; it does not yet resolve orphan targets automatically.

## Implementation update: bind tab closure to the inspected target

`close_tab` and `switch_tab` now accept `tab_target_id` separately from `target_id`. The latter remains the precondition for the active page; the former, when supplied from `list_tabs`, checks that the named tab still maps to the inspected browser target before closing or activating it. The Go API exposes `CloseTabResultForTargetContext` and `SwitchTabForTargetContext`; MCP and the stateful JSONL CLI route through them. This closes a target-attribution gap when tab names are reused or state changes between inspection and the tab operation. `go build ./...` and focused `git diff --check` pass. Target-name reuse and concurrent lifecycle races still need live multi-client fixtures; the field is optional for backward compatibility.

`list_tabs` now sorts named tabs before reading their state and accepts bounded `offset`/`limit` pagination (maximum 50 per MCP call), with total, offset, limit, and truncation metadata. Go callers can request a stable page through `ListTabsPageContext`; existing `ListTabsContext` retains its all-items behavior. This makes repeated responses stable and lets MCP clients retrieve beyond the first 50 tabs. The change is build-checked; pagination boundaries and schema interoperability still need focused verification.

The paged Go method and MCP tool also accept the active-page `target_id` precondition. A stale caller is rejected before the tab snapshot is read, keeping discovery results tied to the page context the caller observed.

## Implementation update: partial localStorage profile restore

Profile restore clears `localStorage` before writing saved keys. The operation now marks the mutation boundary before dispatching that script and reports `outcome_unknown` for any subsequent evaluation error, because a script exception can occur after storage has already been cleared or partly repopulated (including profiles with no cookies). This closes a false-no-effect path in the shared Go/MCP outcome contract. The build is the current verification; a deterministic browser-side exception after `clear()` still needs a focused fixture.

## Implementation update: combined navigation and consent dismissal

`NavigateAndDismissCookies` used to ignore all errors and discard the dismissal result after navigation. It now has a context-aware variant that uses one deadline for both steps, retains the cookie dismissal result on the returned `PageResult`, and returns dismissal errors alongside the successful navigation result so callers can distinguish page load from consent handling. If no supported control is found, the structured `cookie_dismissal` field reports that outcome without claiming dismissal. Build-check this path and add coverage for no banner, unsupported banner, verified dismissal, and uncertain click outcomes before treating the composite flow as fully verified.

The follow-up dismissal is rebound to the `target_id` returned by navigation, since local navigation can replace the active CDP target. The caller's original expected target still guards the navigation step; it no longer incorrectly rejects the intentional destination transition.

### Competitive opportunity refresh: continuity, attribution, and evidence

Recent upstream reports point to a valuable product promise for Scout: an agent should continue a task against the exact browser state it inspected, and every result should say what state it actually touched. Browser Use issue [#6295](https://github.com/browser-use/browser-use/issues/6295), opened 3 November 2026, asks its retry tool to continue in the same browser session instead of creating a separate one. Stagehand reports include [activePage() examples choosing the oldest tab](https://github.com/browserbase/stagehand/issues/3371), [CLI requests cut off by a 35-second socket deadline](https://github.com/browserbase/stagehand/issues/3369), and a request for [extract() to return the source element for each value](https://github.com/browserbase/stagehand/issues/3406). These are individual open reports, not prevalence evidence or proof of current released behavior.

Scout can make this a coherent capability instead of a collection of browser commands: keep a stable session and target identity across inspect, act, verify, and recovery; bind mutations to the target the caller inspected; preserve one caller deadline end-to-end; and return evidence that can be attributed to that same target. Scout already has named sessions, target-bound tab actions, deadlines, verification receipts, and portable recovery predicates. The opportunity is to make continuity the default workflow and demonstrate the complete contract from an MCP host, including safe continuation after a timeout and explicit handoff when state cannot be proven.

| Opportunity | Competitor signal | Scout basis | Evidence needed |
|---|---|---|---|
| Same-session recovery | Browser Use #6295 asks retries to reuse the active session. | Named session lifecycle plus same-target reconciliation. | Show a host switching from structured actions to recovery without losing target, cookies, or policy. |
| Active-target correctness | Stagehand #3371 describes examples selecting the oldest tab. | `target_id` preconditions and target-aware tab operations. | Run concurrent tab churn and prove stale requests reject rather than affect another tab. |
| Honest deadlines | Stagehand #3369 reports a client transport deadline shorter than requested page waits. | Context-aware task and action deadlines. | Publish requested-versus-observed deadline bounds across CLI, MCP stdio, HTTP, and Go. |
| Attributable extraction | Stagehand #3406 requests mapping extracted values to source elements. | Target IDs, action receipts, and bounded page evidence. | Add source locator/evidence references to extraction outputs and measure whether they remain valid after page changes. |

**Recommended positioning:** “Scout is the browser runtime that keeps an agent's action, verification, and recovery attached to the same browser target.” This is more specific and supportable than a general claim of better browser automation. Treat extraction provenance and end-to-end deadline behavior as product opportunities; do not claim parity until their host-facing contracts are implemented and measured.

### Further research: where Scout can beat the field (6 November 2026)

The latest issue scan confirms that competitors keep improving their basics: Playwright MCP now supports bounded `browser_find` results and subtree/depth/file snapshots, while agent-browser has active reports on discarded-tab latency, shadow-root discovery, and output limits in JSON responses ([Playwright MCP releases](https://github.com/microsoft/playwright-mcp/releases), [agent-browser issues](https://github.com/vercel-labs/agent-browser/issues)). Browser Use's tracker still surfaces a request for retry tools to reuse the active session and a report about a malformed security configuration falling back to allow-all ([Browser Use issues](https://github.com/browser-use/browser-use/issues)). These are test prompts tied to current repository state, not evidence of prevalence; Playwright's context overload issue (#1329) is older and its snapshot-to-file route offers a workaround ([#1329](https://github.com/microsoft/playwright-mcp/issues/1329)).

This changes the opportunity ranking. Output search, profiles, tab continuity, and broad capture are becoming table stakes. Scout should win on a specific cross-surface guarantee: a Go host can bind work to the observed page, declare a deadline and postcondition, receive a typed verified/rejected/unknown result, and reconcile an uncertain mutation without replaying it. The recent MCP audit found several read tools that accepted `target_id` but did not apply it as a precondition; those handlers now bind their context before reading. A live stdio probe confirmed stale IDs yield structured `stale_target` errors for extraction, markdown, element existence, and waits. The same guard was added to `hybrid_observe`; it still needs a live probe. This is a real implementation correction, not comparative proof.

To establish an advantage, compare Scout against pinned competitors on the same delayed-effect and tab-churn fixtures. Record wrong-target mutation rate, false-success rate, recovery without replay, deadline overshoot, evidence usefulness, serialized bytes/tokens, and Go integration time. Include repeated trials and raw artifacts. Keep output search and plugin surfaces on the parity roadmap, but prioritize reliability contracts and make unsupported browser coverage explicit. Avoid using individual issues as product claims.

**Target-binding audit follow-up (6 November 2026).** The source audit found three target-bearing handler exceptions that use specialized identity semantics (`reconcile_action`, `click_handle`, and `task_run`); the other audited MCP registrations apply the expected target context before page work. A live stdio probe navigated between distinct targets and exercised 28 target-aware read, diagnostic, wait, and capture tools; every call returned `isError=true` with a structured `SCOUT_OPERATION_ERROR` / `stale_target` envelope, including `hybrid_observe`. The probe exposed twelve handlers that had returned the raw target error, which the MCP SDK surfaced as a JSON-RPC internal error rather than a tool error; those are now normalized through Scout's MCP error adapter. Tool descriptions now explain the target precondition across observation, assertions, diagnostics, and waits. `go build ./...` and `git diff --check` pass. This is broad stale-target coverage over the exercised surfaces, not proof of concurrent tab churn or every mutation path.

**Task contract update (6 November 2026).** `task_run` accepts optional `required_tools` for follow-up inspection or recovery. Before `RunTaskContext` can execute actions, Scout verifies that every requested name exists and is exposed by the active MCP profile; unknown names produce `SCOUT_INVALID_ARGUMENT`, while profile-hidden names return a machine-readable `SCOUT_TASK_CAPABILITY_MISSING` error listing the missing tools. It also accepts `final_postcondition`, validated before browser work and evaluated on the original target only after all actions complete. The task result preserves verification evidence; a failed final readback returns `needs_reconciliation` with `failure_reason: verification_failed` and keeps same-ID replay blocked until host reconciliation. Incomplete or uncertain batches are not reclassified by the final predicate. Task IDs are bound to plan fingerprints and completed results: an identical retry returns the stored result without replay, while a concurrent duplicate or changed plan returns `SCOUT_TASK_ID_CONFLICT`. The default in-memory registry retains 256 IDs per session and fails closed with `SCOUT_TASK_CAPACITY` instead of evicting a replay guard. MCP and stateful CLI can opt into durable bbolt storage with `--task-store=PATH`; the Go API accepts a caller-owned transactional store. The recovery example demonstrates completed-result reuse across reopened sessions, while killed-process restart remains unverified. Startup `--require-tools` remains useful for server-wide requirements. These checks improve caller/task fit but do not certify broader task success or make MCP profiles authorization boundaries. `go build ./...` and `git diff --check` pass; no automated tests were run.

**Next evaluation:** use a small fixed workflow with multiple tabs, a delayed mutation, and a late-arriving effect. Compare Scout with pinned Browser Use, Stagehand, and Playwright MCP versions on target-switch errors, recovery success without replay, false-success rate, deadline overshoot, and evidence provenance. Repeat runs; record exact dependency versions and raw artifacts. Upstream issues should inspire cases, not serve as the only proof of Scout superiority.

**Implementation progress — extraction provenance:** MCP `extract_all` and `scroll_and_collect` return a per-item `references` array aligned with `items`, containing a CSS selector, tag, inferred or explicit ARIA role, and interaction hint for each element. `scroll_and_collect` also returns target and frame identity. `auto_extract` carries the same semantics for each repeated item and each extracted field, alongside its selectors and stable handles. The interaction hint reflects common semantic controls or interaction attributes; it does not prove visibility or current actionability. Callers may set `include_handles=true` to stamp stable `data-scout-handle` attributes and receive a handle per item for `click_handle`; default extraction does not mutate the DOM. Go callers have matching option methods. The tool also applies the supplied `target_id` precondition. Selector references are snapshots of current DOM structure and may stop resolving after page changes; handles identify the same attached node through reordering, and become stale when the node is replaced or removed. A direct Go API probe verified references on unbounded `ExtractAll`, opt-in handles, and stale-target rejection. A live MCP stdio run against a local fixture returned two aligned selectors for “Alpha” and “Beta”; the optional-handle call returned distinct handles and preserved the same target ID. `go build ./...` and `git diff --check` passed. No automated test suite was run.

**Handle ambiguity hardening:** `click_handle` now searches the selected document and its open shadow roots for all matches and rejects a handle that resolves to more than one node before dispatch. A live browser probe cloned a handle-stamped button; Scout returned the structured `ambiguous_handle` cause and both buttons remained unchanged. This protects extracted references from the common DOM-cloning behavior that copies attributes. Closed shadow roots remain outside discovery and action coverage.

### Competitive update: isolation and safe operation are converging (6 November 2026)

The latest release evidence raises the parity bar. Playwright MCP v0.0.84 now advertises separately named isolated contexts within one browser, per-client idle timers for isolated HTTP clients, graceful disconnect cleanup, URL-parameter redaction, and fixes that keep shared-client recordings separate and enforce file-root checks through symlinks ([release notes](https://github.com/microsoft/playwright-mcp/releases)). These changes make session lifecycle and basic evidence hygiene less defensible as standalone differentiators. They also show that the important unit is an authenticated caller's context and evidence stream, not simply an MCP session or browser process.

Open competitor reports still supply useful fixtures, not prevalence claims. Browser Use issue [#6218](https://github.com/browser-use/browser-use/issues/6218) reports malformed security configuration falling back to allow-all; issue [#5252](https://github.com/browser-use/browser-use/issues/5252) reports a failed tool call returned with `isError=false`. Agent-browser issues [#2245](https://github.com/vercel-labs/agent-browser/issues/2245) and [#2246](https://github.com/vercel-labs/agent-browser/issues/2246) report output bounds skipped on a JSON path and interactive discovery missing open shadow-root controls. Meanwhile, Playwright MCP's release notes document fixes for idle cleanup and shared recording bleed, demonstrating why Scout should recheck issue state before claiming a gap.

**Updated opportunity ranking:**

1. **Prove principal-to-browser ownership.** Scout's HTTP principal routing and per-client MCP session IDs are a foundation, but a shared bearer token is not caller identity, and remote CDP clients can share one browser's state. Make the owner/context boundary explicit, reject ambiguous shared ownership for mutations, and measure cross-client state leakage and disconnect behavior. Compare on a fixture with two principals and both dedicated and shared CDP endpoints.
2. **Make every result truthful and bounded.** Preserve `isError`, typed outcome, verification state, truncation/completeness, target identity, and recovery receipt through every transport and output encoding path. Add adversarial fixtures for malformed policy, partial JSON output, and mid-recording browser restart. These checks can become a reproducible acceptance suite rather than positioning based on open issue counts.
3. **Measure evidence utility and cost.** Compare Scout, Playwright MCP, and agent-browser on the same task, including whether a host can decide completed/rejected/unknown from the returned artifact alone, plus wrong-target mutation rate, recovery without replay, bytes/tokens, and integration time. Use repeated runs and publish pinned versions.
4. **Probe WebMCP as an optional discovery path.** Agent-browser's current project surface includes WebMCP list/invoke/result/cancel tools ([CLI repository](https://github.com/vercel-labs/agent-browser)); Scout's advantage would need to be policy-checked invocation plus the same verification and recovery receipt as DOM/CDP actions. Keep this an experiment until browser/site support and security semantics are tested.

The strongest current product hypothesis is narrower than “reliable browser automation”: Scout can provide a host-verifiable ownership and outcome contract across Go, CLI, and MCP. The acceptance bar is that independent host code can identify which principal/session/target performed an operation, whether the intended state was observed, whether the artifact is complete, and what safe recovery is available. The current repository has many of these pieces, but durable idempotency across restarts, shared remote-browser ownership, full response-path completeness, and repeated head-to-head results remain unproven.

### Opportunity refresh: compete on operational guarantees (6 November 2026)

I rechecked current competitor issue trackers, documentation, and the public browser-agent benchmark. The new reports reinforce the earlier thesis, but also show why issue-driven positioning is fragile: an open issue is a lead for a fixture, not evidence of frequency or a released competitor defect. For example, agent-browser currently has reports about discarded-tab attach latency (#2431), worker bootstrap under CSP (#2403), shadow-root target discovery (#2246), and an MCP JSON path that may skip output bounds (#2245) ([issue tracker](https://github.com/vercel-labs/agent-browser/issues)). Browser Use has requests for active-tab/session continuity (#6296, #6295), a report of malformed policy falling back to allow-all (#6218), and a report that an agent can alter a disabled control and still report success (#6068) ([issue tracker](https://github.com/browser-use/browser-use/issues)). These are individual reports; verify state against a pinned release before presenting any as a comparison claim.

The architectural direction is clear: Browser Use offers a two-tool persistent CLI MCP mode with an explicit output cap; Playwright MCP has isolated/shared client contexts, per-client idle cleanup, storage-state options, extension attach, and output retention controls ([Browser Use MCP guide](https://github.com/browser-use/browser-use/blob/main/docs/MCP_SERVER.md), [Playwright MCP configuration](https://github.com/microsoft/playwright-mcp)). Agent-browser is extending its plugin/provider surface and browser lifecycle support. Tool count, named sessions, and a small catalog are becoming table stakes. Scout should keep these capabilities competitive, but make its headline the contract an application can depend on.

The strongest public comparison asset I found is WebdriverIO's benchmark: it swaps browser tools while holding model, harness, and prompt constant, publishes every run and judgment, and uses a fixed sample from Online-Mind2Web. Its current page reports 100 tasks, 2 models, and thousands of agent runs, while clearly cautioning that confidence intervals overlap and newer tool versions may not have completed all tasks ([benchmark method and results](https://benchmark.webdriver.io/), [source and raw data](https://github.com/webdriverio/benchmark)). Its results vary by model and run; Scout should borrow the protocol rather than claim a win from a single leaderboard snapshot. This also exposes a useful category gap: broad task success does not by itself measure wrong-target actions, a false success response, safe recovery after a timeout, or whether a service host can act on the result without parsing prose.

**Where Scout can win, in priority order:**

1. **Make the outcome contract the product.** Every side-effecting operation should end in an attributable `verified`, `rejected`, or `unknown` state bound to the caller, session, target, deadline, and evidence. Unknown must include a safe next step and support reconciliation without replay. Complete this through every public surface, including playbook replay/resume, HTTP transport, output compaction, and shutdown paths. Keep secrets out of portable receipts.
2. **Turn policy into a fail-closed execution plan.** Resolve caller, browser owner, origin rules, required capabilities, and deadline before dispatch. Return the effective policy identity and explain denials in machine-readable form. Make malformed configuration prevent startup. This gives Go platform teams a practical integration point beyond browser automation wrappers; verify network-level enforcement boundaries separately.
3. **Own continuity across tabs and retries.** Use stable target identity for reads and mutations; make task IDs idempotent within a documented lifetime; carry recovery state to a follow-up worker or process. Explicitly distinguish session continuity, browser ownership, and state isolation. Scout now has a bbolt-backed durable store for a single-process owner; shared transactional storage remains the host integration path for clustered deployments.
4. **Make evidence complete and retrievable under bounds.** A capped response must preserve completion state, target, resume/recovery token, and retrieval pointer. Signal truncation for every evidence type and provide cursor/file retrieval for large snapshots, traces, and receipts. This is a chance to beat output-bound bugs with a coherent contract rather than a larger default output limit.
5. **Prove the advantage with a published scorecard.** Add repeated trials on deterministic delayed-effect and tab-churn fixtures, then a small live-web track using a fixed public dataset. Compare pinned Scout, Playwright MCP, agent-browser, and Browser Use releases with equal models/prompts. Report verified success, false success, wrong-target mutations, recovery-without-replay, deadline overshoot, output bytes/tokens, calls, latency, cleanup, and Go integration time. Publish raw artifacts and confidence intervals.

**Product focus:** market Scout first to Go services that need auditable browser side effects and safe recovery, with MCP as an adapter to that same runtime. The simple install and broad tool catalog help adoption, but are easier to copy than a stable execution contract backed by reproducible evidence. Prioritize the smallest work that closes that proof loop: complete receipts and recovery through compaction/resume; publish an operator-ready fail-closed policy and ownership guide; then run the matched scorecard and interview Go platform teams. Defer a general plugin system, more natural-language browser APIs, and hosted-browser features until interviews show they block adoption.

**Claims to avoid for now:** “most reliable,” “safer than competitors,” “lowest token use,” and “best browser agent.” The available evidence supports a focused product hypothesis and test plan, not comparative superiority. The Playwright issue [#42693](https://github.com/microsoft/playwright/issues/42693), for example, describes an idle timer closing a browser during a running call but is marked “closed as not planned”; current docs say the timer should not fire during a call. Treat it as a version-specific regression fixture, not a present-day product weakness.

### Opportunity refresh: task utility per context cost (6 November 2026)

The context-efficiency question is now directly represented in competitor evaluation work. Agent-browser's official eval suite separates deterministic context-footprint accounting (catalog, initialization, and skill bytes with approximate token conversion) from model-driven selection and command-use cases, with provider/model selection and optional model judging ([eval methodology](https://github.com/vercel-labs/agent-browser/blob/main/evals/README.md)). Its current MCP guide also makes a curated `core` profile the default and defers other capability groups ([MCP command reference](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/commands.md)). A community comparison, [web-agent-bench](https://github.com/nsosio/web-agent-bench), reports near-tied completion on its small task set while showing larger differences in context tokens. Treat that as a useful hypothesis and methodology example, not a representative ranking.

Scout already measures serialized MCP catalog size, but bytes divided by four are only a rough estimate, and catalog size alone says nothing about whether a model chooses the right operation. The next benchmark should report **verified task utility per context cost**: for each pinned task/model/profile, capture actual provider-reported input and output tokens, verified task completion, wrong-target or disallowed actions, tool-selection errors, calls/retries, latency, and recovery outcome. Add the full catalog and a curated profile as explicit conditions; keep prompts, model settings, task state, timeouts, and success predicates fixed. Separate deterministic footprint (tool schemas, instructions, and loaded reference text) from live model outcomes, and publish raw transcripts with secrets removed. A profile only wins if it reduces context cost without lowering verified completion or increasing unsafe/incorrect actions.

**Opportunity update:** small, curated MCP surfaces are converging toward parity. Scout can differentiate by demonstrating that its profile and task contracts reduce the work and context needed to reach a verified outcome, including the safe next step after an uncertain mutation. The benchmark needs a provider adapter that reads actual usage counters; do not convert bytes to tokens and label that result model usage.

### Competitive shift: page-native WebMCP tools

Playwright MCP 0.0.81 added `browser_webmcp_list` and `browser_webmcp_call`. Its current documentation says a page can register tools through the experimental WebMCP API and Playwright exposes the current tab's tools as MCP tools named `webmcp_<tool>`. The page supplies each tool's name, description, schema, and result, which Playwright explicitly says to treat as untrusted input ([v0.0.81 release notes](https://github.com/microsoft/playwright-mcp/releases), [WebMCP documentation](https://github.com/microsoft/playwright/blob/main/docs/src/getting-started-mcp.md#webmcp-tools)). Agent-browser also released experimental WebMCP support: its command surface lists tools with frame identity, invokes them, and supports detached result retrieval and cancellation; its 0.36.0 notes say managed Chrome enables it by default, with an opt-out ([changelog](https://github.com/vercel-labs/agent-browser/blob/main/CHANGELOG.md), [command reference](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/commands.md#webmcp-page-tools)). Its trust guidance explicitly treats page schemas, annotations, and results as untrusted and says these markers do not provide a security boundary ([trust boundaries](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/trust-boundaries.md)). Chrome's guidance classifies WebMCP as an early preview for Chromium 146+, activated through the `enable-webmcp-testing` flag or an origin trial; it runs inside the page, is limited to tools, and requires a secure context. It is not a backend MCP transport ([Chrome WebMCP guide](https://developer.chrome.com/docs/modern-web-guidance/guides/webmcp/webmcp)). **The bridge is now a parity feature, not a distinct Scout opportunity.** It can bypass brittle UI discovery on supported sites, but adoption depends on site support and the page controls the tool contract.

The Scout opportunity is to preserve one **verified execution contract across interaction modes**. A bridge by itself is not differentiation: both major MCP/CLI competitors now offer page-native invocation, and agent-browser has already documented hostile tool metadata and host confirmation boundaries. Scout's testable hypothesis is that a Go service can use either DOM/CDP actions or page-provided tools while keeping the same target and policy binding, untrusted-content provenance, caller-declared postcondition, and durable recovery receipt. This could reduce UI brittleness without asking a host to adopt the competitor's orchestration model. It remains a hypothesis until matched behavior and buyer interviews validate it.

Before expanding the experimental bridge, use the competitor adapters and Scout on a matched localhost fixture in Chromium 157 and current pinned releases. Register one read-only tool, one mutation tool, a delayed operation, and a same-named tool in another tab. Compare DOM and WebMCP paths for verified task outcome, wrong-target calls, schema validation, hostile metadata handling, output provenance, timeout reconciliation, call count, and host-visible recovery data. Record unsupported browser/site paths explicitly; don't count shipping parity as competitive progress.

### Competitive refresh: WebMCP and browser-native snapshots (6 November 2026)

The latest scan changes the previous WebMCP recommendation. Agent-browser's 0.36.0 changelog already describes experimental discovery/invocation, frame-aware selection, detached calls, cancellation, and bounded metadata/output. The current CLI contract says it sends short name/description/origin/frame summaries, requires fetching the selected schema on demand, and invalidates the catalog when discovery returns empty or unavailable. Its trust guide marks page metadata as untrusted and says content boundaries are only provenance cues; host policy and confirmation remain authoritative. Playwright MCP separately exposes current-tab page tools and likewise documents page metadata/results as untrusted. Thus “add WebMCP” alone is feature parity work, and prior text calling the gap uniquely Scout's is stale.

Agent-browser also has draft PR [#2434](https://github.com/vercel-labs/agent-browser/pull/2434) proposing an experimental snapshot source from Chrome Annotated Page Content (APC). The patch reports rendered-only content, optional viewport filtering, same-origin frame refs, and a fallback to accessibility snapshots when decoding fails. The PR itself calls out that APC is an internal Chrome schema pinned to verified versions and that in-frame results remain incomplete. This is a main-branch proposal, not a released baseline. It suggests that observation quality and visible/actionable-node alignment are being actively explored; Scout should compare APC only on a pinned browser build and preserve the accessibility path as a compatibility baseline.

The competitive bar is also moving from surface inspection toward actual-agent evaluation. Agent-browser's WebMCP context eval runs a real Codex agent against a local shopping fixture, records CLI calls and page events, requires relevant tool invocations after discovery, independently checks the wishlist, and includes ordinary and hostile-metadata controls. The project calls these smoke evaluations and does not claim reliability across models or sites; its reported output bytes are not tokenizer-specific token counts ([eval methodology](https://github.com/vercel-labs/agent-browser/blob/main/evals/README.md)). Scout's benchmark suite has strong deterministic contract probes, but this research suggests a missing layer: whether a real model preserves task success and safety when choosing between UI and native site tools, across profiles and hostile page content.

**Implementation follow-up (6 November 2026):** `task_run` and strict batches now accept a catalogued WebMCP action under the same stable task ID, target binding, current schema/origin checks, host-declared postcondition, idempotent retry, and recovery export used for ordinary UI actions. The live Chrome 157 fixture passed through MCP: Scout invoked the mutation once, verified the page state independently, returned the stored result on an identical task retry, and exported a no-replay receipt bound to the page tool's frame. The durable plan retains the tool/frame identity but omits its input. This is one integration smoke, not a reliability estimate or proof of delayed-call recovery. It establishes shared-contract parity; it does not make WebMCP a unique Scout capability. Cross-origin frame catalog visibility, hostile metadata, delayed completion, frame drift, cancellation, and real-agent selection remain unverified.

**Revised direction:** focus on the common outcome contract across UI and WebMCP, and evaluate it with both deterministic fixtures and actual-agent trials. A successful page-tool return does not prove a committed application mutation, just as a successful click dispatch does not. The comparison should verify whether Scout's target binding, host postconditions, typed unknown outcomes, durable task IDs, and authoritative reconciliation remain usable with a page-native call. Include a hostile description/schema/result and verify that untrusted provenance reaches the host. Report behavior by path (native feature, adapter code, unsupported) rather than collapse it into completion rate alone. For model trials, pin model/version/prompt/profile, capture provider token counts where available, grade outcome from fixture state, and keep smoke outcomes separate from repeated reliability estimates.

**Next decision gate:** reproduce the WebMCP fixture through Playwright MCP and agent-browser first. If their native calls already preserve target identity, bounded output, cancellation, and enough result evidence for the same host reconciliation, then Scout's opportunity is integration ergonomics and portability, not a safety claim. If they omit stable action identity or a durable, target-bound unknown-outcome record, prototype the smallest Scout bridge that feeds the existing task receipt instead of adding a separate recovery subsystem. The exact acceptance comparison belongs in the updated [opportunity matrix](../examples/benchmark/OPPORTUNITY_MATRIX.md).

**PR status matters:** on 6 November, agent-browser had open proposed fixes for the output-bound path ([#2419](https://github.com/vercel-labs/agent-browser/pull/2419)), open-shadow-root cursor discovery ([#2413](https://github.com/vercel-labs/agent-browser/pull/2413)), discarded-tab connection probing ([#2438](https://github.com/vercel-labs/agent-browser/pull/2438)), and pinned MCP session scoping ([#2430](https://github.com/vercel-labs/agent-browser/pull/2430)). PR #2430 specifically scopes user-selected sessions beneath a server-pinned session and also guards a `--session` passed via extra arguments. Browser Use's disabled-control click report led to [PR #5965](https://github.com/browser-use/browser-use/pull/5965), which fixes misleading success when a normal click is suppressed; a later [report and reproducible runs in #6031](https://github.com/browser-use/browser-use/issues/6031) show a separate agent-level bypass by changing the DOM with `evaluate`, so a click fix alone does not solve policy-compliant task completion. The practical lesson for Scout is to benchmark policy adherence and final-state evidence, not just whether the browser dispatched an input event. These PRs/issues were inspected as repository activity and must be rechecked before release comparisons.

### Further research: long-lived operation and browser resource contracts (6 November 2026)

The current issue scan adds a more operational opportunity than another browser API. Playwright MCP has recent reports about a long `browser_wait_for` being interrupted by idle cleanup ([#42693](https://github.com/microsoft/playwright/issues/42693)), an isolated browser process tree remaining alive when an HTTP/SSE connection stays open without tool calls ([#1795](https://github.com/microsoft/playwright-mcp/issues/1795), closed with upstream work), and first-call startup stalling after a browser close ([#1798](https://github.com/microsoft/playwright-mcp/issues/1798)). Agent-browser has fresh reports about CDP attach latency when tabs are discarded ([#2431](https://github.com/vercel-labs/agent-browser/issues/2431)) and a session argument overriding the server's configured session ([#2422](https://github.com/vercel-labs/agent-browser/issues/2422)). Some are open, some have been addressed upstream; none establish a general failure rate. They identify distinct dimensions that should not be collapsed into a single “reliability” number: in-flight call survival, eventual process cleanup, attach latency under browser pressure, and server-enforced session ownership.

Scout's existing local evidence is strongest on in-flight idle behavior and remote-browser preservation. The next product opportunity is an operator-facing **resource and ownership contract** that is measurable across transports: every session should report its owner and browser ownership mode, last activity and idle deadline, in-flight work, cleanup state, and whether a tool call is extending lifetime. Then publish matched checks for a tool call longer than idle timeout, an open-but-idle HTTP stream, disconnect without DELETE, attach with discarded tabs, and an unauthorized session override. This is valuable to Go services that run browser workers continuously, and it complements task recovery: a host needs to know both whether work is safe to resume and whether the resource still exists.

**Implementation progress:** `list_sessions` now reports `in_flight_calls` per visible session and does not count its own request. If another browser call is active, it returns promptly with `status_deferred=true` and omits browser-backed status fields rather than queuing behind that call. This fills an operator visibility gap: `last_activity` and `idle_deadline` explain when idle cleanup is expected, while the in-flight count explains why cleanup is being deferred. The value is an instantaneous server-side count of browser tool calls; it does not measure browser process memory or guarantee the remote provider released resources.

The same listing now reports `owner_scope` as `stdio_process`, `transport`, or `authenticated_principal`. This makes the registry boundary visible without returning principal names, transport IDs, or credentials. It describes authorization scope, not browser-process ownership; use `browser_ownership` for that separate dimension.

**Live HTTP evidence (6 November 2026):** the benchmark now sends `list_sessions` concurrently during a 1.8-second `wait_for` with a 500 ms idle timeout. Five fresh server/browser runs passed 5/5: session listing returned in 0–1 ms, reported one in-flight call, and deferred browser-backed status; the call completed, then idle cleanup expired the session. Raw results are in `scout-mcp-http-inflight-observability-5x-2026-11-06.json` (local audit artifact). This verifies Scout's fixture behavior only; no cross-tool comparison or abrupt-disconnect cleanup claim follows from it.

**Same-process reconnect and live recovery evidence (6 November 2026):** the principal-isolation fixture now holds a delayed task in its `actions/started` cursor while a fresh Streamable HTTP transport, authenticated as the same principal, reads the redacted plan and in-flight reservation using only the existing opaque session ID. The task then completes, the saved result is readable, and `status` through that transport still matches the original target and URL. A different principal is denied both transport-session reuse and the opaque ID. Three fresh runs passed 3/3, alongside isolated browser storage, reset, origin enforcement, and DELETE isolation. Run with `SCOUT_BENCHMARK_FOCUS=mcp-http-principal-isolation SCOUT_BENCHMARK_REPEATS=3 go run ./examples/benchmark`. This demonstrates live same-process task visibility during another transport's active browser call; it does not simulate Scout process loss, server restart, idle expiry, or browser loss. The separate durable recovery example covers process-loss cursor persistence.

| Opportunity | Competitor signal | Scout basis | Next evidence |
|---|---|---|---|
| In-flight lifetime | Playwright #42693 reports idle eviction during a long call; Scout's local 10-run comparison already exercised that case. | MCP middleware tracks in-flight calls and idle cleanup. | Repeat against a current Playwright release and add slow navigation plus cancellation cases. |
| Idle connection cleanup | Playwright MCP #1795 describes a held stream preventing idle cleanup; upstream marked it completed. | Scout has lifecycle probes for held SSE and idle remote CDP. | Test abrupt disconnect, a proxy-held stream, process tree exit, and memory after repeated create/close cycles. |
| Attach under tab pressure | agent-browser #2431 reports serial renderer probes adding seconds when discarded tabs exist. | Scout attaches to existing targets and exposes lifecycle status. | Build a fixture with 0/10/50 discarded tabs; report attach latency and which tab became active. |
| Server-owned session identity | agent-browser #2422 reports a tool `session` argument overriding configured server scope. | Scout namespaces MCP state by transport session/principal and rejects cross-owner named-session access. | Test every session-selection path, including conflicting server defaults and caller arguments, on stdio and HTTP. |

Do not position these reports as competitor defects after fixes land. The defensible opportunity is to make resource ownership observable and to ship repeatable lifecycle tests as a deployment guarantee. This research favors an operator dashboard/health contract and a transport matrix over expanding the action catalog.

**Implementation progress (6 November 2026):** `SessionConfig.TaskRunStore` now provides a host-database extension point for task idempotency beyond one session. Scout atomically reserves through the host interface, binds the observed browser instance and target before dispatch, persists completed results, and leaves interrupted post-dispatch reservations in-flight so a retry fails closed. Pre-dispatch reservations can be released; completion and cleanup use bounded detached contexts so caller cancellation does not silently erase the outcome. `OpenBoltTaskRunStore` supplies durable local persistence for a single-process owner, with a global 8,192-ID cap and no automatic pruning. `GetTaskRun` lets a restarted host inspect an interrupted reservation and resolve it after reconciliation. Focused checks close and reopen the bbolt file, verify completed results remain reusable, keep interrupted reservations bound and unrecreated, reject conflicting plans, and verify MCP owner namespaces isolate stored results. The runnable recovery example now executes a completed task and identical retry in separate Scout processes against the same durable store and live browser target; one application mutation is observed. Stateful CLI and MCP expose `--task-store=PATH`; MCP scopes records by principal/session or, without a principal, by HTTP transport/session so durable evidence does not cross connected client boundaries. Killed-process CLI/MCP execution, reconnect semantics without principal mapping, and recovery after a crash during dispatch remain unverified. Clustered deployments still need a shared transactional implementation.

### Competitive refresh: isolation and explicit state (6 November 2026)

Recent Playwright reports show that multi-client state isolation is a live product concern, not a solved checkbox: a shared BrowserContext report described snapshot data from a sibling client ([playwright-mcp #1631](https://github.com/microsoft/playwright-mcp/issues/1631)); a separate report found isolated context cleanup waited until the last client disconnected ([Playwright #41539](https://github.com/microsoft/playwright/issues/41539), fixed by [#41560](https://github.com/microsoft/playwright/pull/41560)). Playwright's own guidance distinguishes persistent, isolated, and shared-context ownership, and notes that its persistent profile can only be used by one browser instance at a time ([MCP setup](https://playwright.dev/mcp/getting-started)). Those reports are version-specific; use them to derive fixtures, not as claims about current releases.

At this 6 November refresh, Scout's state contract made ownership explicit: stateful HTTP clients receive separate registries, while stdio uses one registry for the connection. HTTP session keys are scoped by transport client ID and optional principal. Tool calls are bound at request start to the active session and target; MCP `DELETE` waits for in-flight work before cleanup. Scout accepted a per-call named-session selector in `_meta.go.klarlabs.de/scout/session`; omitted selectors retained the implicit active-session behavior. The name was not an opaque reconnect handle, and the registry still relied on MCP transport identity. The modern MCP spec says stateless requests should carry metadata needed to reconstruct state, and cross-request state needs explicit identifiers on every request ([MCP statelessness](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/index.mdx#statelessness)). See the 10 November [stateless implementation update](#mcp-stateless-transport-compatibility-6-november-2026) for current status.

The stateful MCP path now implements the first part of this improvement: a namespaced `_meta` selector resolves an existing owner-scoped session before target binding. The next increment is to return a server-issued opaque handle in session creation/list results and add equivalent Go/CLI selection. Keep the implicit active session as a backward-compatible default for stateful clients. Do not use client-supplied names as authorization: bind handles to the authenticated principal or transport owner, make them unguessable where they cross trust boundaries, and reject unknown/foreign handles before browser access. The same pattern could later enable MCP sessionless transports and parallel agent workers without shared mutable “active session” state.

The 6 November implementation inspection confirmed that the dependency exposes `WithStreamableStateless()` while Scout selected `WithStreamableStateful()`. Stateless mode removes `Mcp-Session-Id`; principal authorization was then bound to that transport ID, so flipping the option alone would lose ownership isolation and leak registry state. The 10 November prototype added configured principal authentication and opaque per-call `_meta` session handles. Chrome-backed HTTP integration now covers task and artifact isolation, reconnect, quota and close cleanup, idle behavior, cancellation, and graceful single-process restart; official TypeScript and Python client smokes cover session-bound navigation, reading, cancellation of an in-flight wait, and a successful subsequent read. Multi-replica load balancing and additional client SDKs remain open; see the [current stateless implementation update](#mcp-stateless-transport-compatibility-6-november-2026).

Before shipping, map all session-affecting operations and durable task namespaces, then prove: (1) two concurrent calls with different explicit session handles cannot cross-route, (2) a reconnect can resume using only its handle, (3) a foreign principal cannot enumerate or select another owner's handle, and (4) omitted selectors preserve current behavior. Include a stateless client fixture that sends the handle on every call; principal auth must be checked on every request, and explicit session lifetime needs an owner policy independent of transport DELETE. The product promise to test is **parallel browser work with explicit, auditable ownership**. Scout already has per-client HTTP isolation and in-flight lifecycle evidence; explicit per-call identity is the remaining architectural step toward truly stateless operation.

Other research signals align with Scout's evidence-first wedge. Playwright MCP now exposes a `browser_verify_value` helper, showing that post-action checks are becoming a competitive feature ([tool reference](https://github.com/microsoft/playwright-mcp)); Stagehand contributors recommend treating natural-language action results as execution evidence and gating workflow completion on a separate assertion ([discussion #2176](https://github.com/browserbase/stagehand/discussions/2176)). Browser Use issue #5747 reports long-task degradation from growing message history and repeated DOM serialization ([issue #5747](https://github.com/browser-use/browser-use/issues/5747)). These reinforce the opportunity to make concise, durable verification receipts a stable runtime contract, with bounded snapshots and explicit completeness, rather than adding a second autonomous agent loop.

### Implementation update: owner-scoped MCP restart recovery (6 November 2026)

The `admin` profile now has `persist_session`, `resume_session`, and
`revoke_session`. This path requires a configured authenticated HTTP principal,
the durable `--task-store`, and a shared remote CDP endpoint without an
isolated BrowserContext. It persists a separate 192-bit handle bound to that
principal, the current policy digest, browser fingerprint, and exact target;
the endpoint and policy are reloaded from the current principal configuration
on resume. A process-local in-flight guard prevents two requests on one server
from attaching the same handle simultaneously. Transport deletion and idle
expiry clear stale process indexes while leaving the durable record resumable;
explicit `close_session` revokes all recovery handles for the principal's
default session, including handles created before reconnect. The MCP process
expires old records hourly. The CLI offers the same lifecycle using the local
OS user as owner scope.

This closes the basic cross-interface recovery gap for a browser that remains
alive, but it is narrower than the full opportunity: it cannot restore a
restarted browser, isolated contexts, local sessions, or bearer-only HTTP
deployments without a stable principal. The current integration proof exercises
the Go/CLI storage and exact-target path; MCP handler behavior still needs a
full HTTP restart test covering principal A success, principal B denial,
changed endpoint/policy, duplicate concurrent resume, explicit close, expiry,
and transport deletion followed by a new transport. Do not call MCP restart
recovery release-proven until that matrix passes.

### Further research: trust boundaries and session continuity (6 November 2026)

The newest product documentation changes the competitive read in two ways.

First, persistent login state is already a default in Playwright MCP: its
workspace-scoped profile preserves cookies and local storage across sessions,
and its docs also cover explicit storage save/restore. Scout's durable handle
is a different contract: it resumes the same still-live browser target after a
Scout process restart, under a currently authenticated owner and unchanged
policy. This can be valuable for service workers holding live browser state,
but should be described as live-target continuity, not general-purpose browser
profile persistence. Scout should document a deliberate distinction between
reconnecting to a live remote browser and restoring user data into a new one.
([Playwright MCP profile and state](https://playwright.dev/mcp/configuration/user-profile),
[Playwright storage tools](https://playwright.dev/mcp/tools/storage))

Second, untrusted page content is a product security boundary, not just a
prompting concern. Playwright MCP issue #1479 describes indirect prompt
injection carried by accessibility snapshots. Agent-browser now documents
content boundary markers, output caps, and domain restrictions as runtime
options. These are signals that agent consumers need a visible distinction
between tool instructions and page-provided text, plus bounded result size.
Scout already bounds several responses and records evidence. Go results now
mark their page-derived fields with `content_trust: "untrusted_page_content"`
for observations, text reads, extraction, semantic selection, forms,
navigation/assertion outcomes, diagnostics, readiness, vitals, tabs, and
selector suggestions. MCP wrappers propagate it through capture, network,
cookies, forms, screenshots, hybrid observation, and DOM diffs. Snapshot
save/read/search results carry the same marker. Trace exports mark each event
and network record alongside the run, evidence, and network manifests. Task,
playbook, action, and recovery receipts also mark the boundary. The field
preserves raw evidence while distinguishing it from Scout instructions and
authorization policy. This is metadata for consuming agents, not a sanitizer
or prompt-injection defense; adversarial end-to-end client evaluation and
some visual/OCR pathways still need explicit coverage.
([Playwright MCP #1479](https://github.com/microsoft/playwright-mcp/issues/1479),
[agent-browser safeguards](https://github.com/vercel-labs/agent-browser))

Issue reports also point to concrete regression tests instead of broad quality
claims: Stagehand #3370 reports a `networkidle` wait timing out on an already
idle page, #3359 reports a doctor check that passes while browser commands
cannot attach, and #3371 reports active-page selection choosing the oldest
tab. Agent-browser's current docs expose `--pin-tab`, `--max-output`,
`--allowed-domains`, and `--content-boundaries`, showing that tab pinning,
bounded context, and content trust markers are now explicit workflow features.
Scout should compare these scenarios directly: idle waits must return promptly,
health checks must perform a bounded real attach/read, and tab actions must
retain target identity through churn. These reports are case generators, not
release-wide failure-rate evidence.
([Stagehand issue tracker](https://github.com/browserbase/stagehand/issues),
[agent-browser current feature set](https://github.com/vercel-labs/agent-browser))

#### Revised opportunity order

1. Complete the lifecycle conformance matrix: transport DELETE, replacement
   transport, process restart, in-flight tool during deletion, idle expiry,
   close/revoke, and foreign-owner attempts. The new HTTP fixture now proves
   same-target resume after an actual MCP DELETE and after process restart.
2. Complete adversarial coverage of the untrusted-content field, especially
   visual/OCR pathways and downstream clients; publish examples that preserve
   evidence while keeping page text distinct from trusted policy and metadata.
3. Add a real attach/read probe to `doctor` and return phase-specific timings
   (configuration, endpoint reachability, target discovery, and read).
4. Run the fixed task scorecard with equal browser fixtures and pinned
   versions. Include session continuity, target drift, prompt-injection
   boundary handling, output bytes/tokens, and truthful task outcomes.

This sharpens the defensible edge: a browser runtime that carries identity,
trust provenance, and outcome certainty through an interrupted workflow. The
edge is only credible when these contracts are easy to inspect and verified
across Go, CLI, and MCP interfaces.

#### What the leading tools already cover

Feature breadth is no longer a useful moat by itself. Playwright MCP offers a
large tool catalog, accessibility snapshots, cross-browser support, persistent
and isolated profiles, extension attachment, network controls, WebMCP, and an
unsafe direct-code escape hatch. Its persistent profile keeps login state, but
the documented profile is single-instance; isolated sessions lose in-memory
state at close unless seeded from storage state. ([Playwright MCP feature and
profile docs](https://github.com/microsoft/playwright/blob/main/docs/src/getting-started-mcp.md))

Agent-browser is a fast native CLI with compact snapshots, durable named
sessions, authentication/state helpers, skills, flows, providers, and a live
dashboard. It documents separate storage when it owns each browser, but only
tab separation when several sessions share one CDP browser. ([agent-browser
session management](https://github.com/vercel-labs/agent-browser/blob/main/skill-data/core/references/session-management.md),
[agent-browser skill](https://github.com/vercel-labs/agent-browser/blob/main/skills/agent-browser/SKILL.md))

Stagehand offers a useful middle path between deterministic code and autonomy:
`observe` can produce actions for later replay, `act` performs one semantic
action, `extract` returns structured data, and an agent loop is available when
needed. Its Browserbase mode also caches repeated AI calls server-side.
([Stagehand primitives](https://github.com/browserbase/skills/blob/main/skills/browser-use-to-stagehand/references/guide.md),
[Stagehand caching](https://github.com/browserbase/stagehand/blob/main/packages/docs/v3/references/stagehand.mdx))

Browser Use competes at the higher-level autonomous-agent loop, while its issue
tracker surfaces demand for same-session retry, outcome verification, and
governance before real-world actions. That evidence suggests a distinct buyer
need for a reliable execution layer beneath agent frameworks, rather than one
more all-in-one agent loop. ([same-session retry #6295](https://github.com/browser-use/browser-use/issues/6295),
[outcome verification #5957](https://github.com/browser-use/browser-use/issues/5957))

#### What fresh GitHub activity says

The newest issues are useful as test-case generators, not proof that a
competitor is broadly unreliable:

| Signal | Tracker evidence | Product opening for Scout |
|---|---|---|
| Transport cleanup is still subtle | Playwright MCP #1837 reports POST-only sessions that miss heartbeat cleanup; #1795 reports a held stream defeating idle expiry. | Publish a lifecycle contract covering request activity, idle expiry, disconnect, cancellation, in-flight calls, and cleanup ownership. Make the checks runnable against a real server. |
| MCP schemas are part of product quality | Playwright MCP #1828 reports defaults marked required, inaccurate read-only/destructive annotations, and weak numeric validation. | Treat generated JSON schemas and annotations as a public compatibility surface. Add schema contract fixtures and validate bounds before touching the browser. |
| Browser attachment cost matters | agent-browser #2431 reports serial probes adding about six seconds with multiple discarded tabs. | Benchmark attach latency with 0/10/50 discarded tabs, and expose attach phase/timing so operators can see whether delay came from discovery, target selection, or navigation. |
| Server ownership must constrain tool input | agent-browser #2422 reports a per-call session override against the pinned server session; PR #2430 proposes a guard and was still open in the tracker snapshot. | Make session selection server-authorized and owner-bound at every interface; test conflicting selectors and defaults. Don't claim the competitor issue remains if the PR ships. |
| A healthy diagnostic must test a real operation | Stagehand #3359 reports `doctor` returning all green while local browser commands fail to attach. | Make `doctor` perform a bounded launch/attach/read check and return actionable root causes, not just configuration checks. |
| Recovery should preserve the active browser | Browser Use #6295 asks its retry agent to reuse the already active session. | Offer an explicit same-target handoff/retry path with a durable owner-bound handle and a verified target, rather than silently opening a blank browser. |
| Long workflows need outcome proof | Browser Use #5957 asks for guidance on verifying real-world results after actions. | Return compact evidence receipts and distinguish “tool call succeeded,” “browser postcondition verified,” and “external outcome uncertain.” Never infer a purchase/submission completed from a click alone. |

Sources: [Playwright MCP #1837](https://github.com/microsoft/playwright-mcp/issues/1837),
[Playwright MCP #1795](https://github.com/microsoft/playwright-mcp/issues/1795),
[Playwright MCP #1828](https://github.com/microsoft/playwright-mcp/issues/1828),
[agent-browser #2431](https://github.com/vercel-labs/agent-browser/issues/2431),
[agent-browser #2422 and PR #2430](https://github.com/vercel-labs/agent-browser/issues/2422),
[Stagehand #3359](https://github.com/browserbase/stagehand/issues/3359).
Tracker states are volatile and should be rechecked before public comparisons.

#### Recommended position

**Scout should be the Go-native, evidence-first browser runtime for agents that
must operate safely in production.** Its wedge is not having the most actions or
the most autonomous model loop. It is giving application developers explicit
control over who owns a browser, which target an action reached, what evidence
supports the result, and how a failed or interrupted task is reconciled.

That position builds on Scout's current strengths: Go embedding, CDP attach,
owner-scoped sessions, target IDs, bounded action/task state, structured
postconditions, durable task records, and human handoff. It also fits the
capabilities competitors are adding: Playwright's `browser_verify_value` and
Stagehand's semantic primitives show that verification and semantic actions
are becoming table stakes. The durable distinction should be the *contract*
around execution and recovery, not an isolated verify tool.

#### Priority order

1. **Close and publish the recovery proof.** Complete the MCP restart matrix
   already listed above: same principal succeeds; another principal, changed
   policy/endpoint, expired/revoked handle, and duplicate concurrent resume
   fail closed; a new transport succeeds; closing the browser session revokes
   attached handles. Document the precise promise: exact live-target recovery,
   not restoration after browser loss.
2. **Ship a verification receipt contract.** Each state-changing operation
   should identify the target, action outcome, checked postcondition, evidence
   source, and uncertainty. For external side effects, expose an explicit
   reconciliation step instead of automatic replay. This addresses the gap
   between tool success and real-world success raised in Browser Use #5957.
3. **Make ownership and health independently testable.** Extend the existing
   benchmark suite into a small conformance pack for isolation, target drift,
   idle cleanup, disconnect, long calls, and attach latency. Emit JSON results
   with versions and environment details so teams can compare releases and
   providers themselves.
4. **Reduce adoption cost before expanding the action catalog.** Improve
   first-run `doctor`, MCP schema quality, examples, and operator guidance.
   Add per-call timeout overrides, compact evidence output, and safe defaults
   where the API makes an otherwise routine task require extra round trips.
5. **Add breadth only where the runtime contract survives.** Explore WebDriver
   BiDi/Firefox and more providers only with parity for target identity,
   ownership, postcondition evidence, and recovery semantics. Avoid racing
   competitors on AI action count or an embedded autonomous agent loop.

#### Proof points that would make the position credible

Publish matched runs, with the same fixture and browser, for: correct-target
action rate under external tab changes; state isolation across two principals;
recovery success and time after transport/server restart while the browser
remains alive; duplicate-side-effect rate after interrupted mutations; p50/p95
attach latency with discarded tabs; idle cleanup under open streams and
in-flight calls; and token/call count for a completed verified task. Include
failures, not only aggregate pass rates. Until those comparative runs exist,
the strategy above is an evidence-backed opportunity hypothesis rather than a
claim that Scout already outperforms competitors.

### Further research: where Scout can win (6 November 2026)

A capability scan of current official docs and recent issue trackers points to a useful product position: Scout should compete as the **reliable browser runtime for agent builders**, where browser identity, task outcome, evidence quality, and policy can be inspected by the host application. Feature-count parity is a weak strategy: competitors already expose very broad browser command surfaces.

| Product | Strong product shape | What its current surface emphasizes |
|---|---|---|
| Playwright MCP | Familiar Playwright control and accessibility-tree interaction | A compact default MCP tool set with opt-in network, storage, testing, vision, PDF, and DevTools capabilities; refs, snapshots, and direct browser code for complex steps ([capabilities](https://playwright.dev/mcp/capabilities), [snapshots](https://playwright.dev/mcp/snapshots)). |
| agent-browser | Fast CLI plus a large typed MCP surface | Profiles that expose core actions or opt-in network, state, debug, tabs, React, and mobile groups; CLI parity and session-scoped browser state ([commands](https://agent-browser.dev/commands)). |
| Stagehand / Browserbase | Natural-language browser actions paired with optional managed remote infrastructure | Stagehand provides `act`, schema-validated `extract`, `observe`, and an autonomous `agent` loop; it works locally and can use Browserbase for cloud browsers, replay, and managed execution ([Stagehand](https://stagehand.dev/), [Browserbase integration](https://docs.browserbase.com/introduction/stagehand)). |
| Scout | Go-native embeddable runtime with persistent sessions, explicit policies, durable task receipts, recovery, and human-handoff hooks | A host can own orchestration and persistence while Scout provides browser execution and attributable evidence. The current opportunity is making this contract obvious and easy to adopt. |

**Implementation update (6 November 2026):** Scout action receipts, portable task recovery receipts, human-handoff uncertainty receipts, trace run metadata, and action spans now carry the same effective page-policy digest. This closes a useful attribution join for hosts: each outcome can be tied to the policy active for its session. It does not expand enforcement coverage or establish a competitor advantage; matched end-to-end workflow evidence remains the decision gate.

### Session identity and live-context access recheck (6 November 2026)

Recent research narrows the session-continuity claim. Agent-browser's official
session docs describe isolated sessions, stable worktree IDs, and persistent
session-to-tab pinning across daemon restarts; its 0.41.2 changelog (3 November)
also lists fixes for session restarts when timeout settings change and for
launch settings being dropped on bare `open`. Stable target binding and basic
restart persistence are parity dimensions to measure, not Scout's position by
themselves ([sessions](https://agent-browser.dev/sessions),
[changelog](https://agent-browser.dev/changelog)).

The newest open issue reports are more specific. Agent-browser #2422 reports
that an MCP call's `session` argument can override the server's
`AGENT_BROWSER_SESSION`, allowing two server instances to reach the same named
browser; its proposed #2430 fix should be checked in the first release that
contains it. Issue #2421 reports per-command environment/config CDP targets
overriding a successful explicit `connect` on a later command. These are
versioned reports, not general failure-rate evidence ([#2422](https://github.com/vercel-labs/agent-browser/issues/2422),
[#2430](https://github.com/vercel-labs/agent-browser/pull/2430),
[#2421](https://github.com/vercel-labs/agent-browser/issues/2421)). Browser
Use #6296 and #6295 separately signal demand for MCP tools to join an already
active browser session and for agentic recovery to share the structured tools'
session, rather than starting elsewhere ([#6296](https://github.com/browser-use/browser-use/issues/6296),
[#6295](https://github.com/browser-use/browser-use/issues/6295)).

**Product opportunity:** make browser/session identity explicit across an
entire tool call and across adapter boundaries. Scout already has authenticated
owner-scoped opaque selectors and a canonical request routing record. The
latest middleware review also found that `status`, `reset`, and `clone_session`
were missing request-entry session binding; the implementation now pins their
session. A focused live stdio `clone_session` race verified that the clone used
the source active at request entry while a later concurrent switch remained
active at completion. Clone still activates its destination when no newer
switch intervenes. The scenario is available as
`SCOUT_BENCHMARK_FOCUS=mcp-session-owner-race`; status/switch and reset/switch
races remain to be measured. Extend the regression matrix to cover simultaneous
agents with identical caller-picked session names,
attempted override of server-owned session scope, explicit `connect` followed
by commands carrying conflicting environment defaults, parallel
`clone_session` and `switch_session`, and an agentic handoff that must retain
the original target. Report same-owner selection, cross-owner denial, target
continuity, and page-state isolation as separate predicates. A useful future
UX is a compact routing receipt on each result with authenticated owner scope,
canonical session ID, target ID, and whether selection was explicit, without
returning credentials or relying on a mutable “active session.”

The source pages above describe features, not comparative performance. The competitive issue scan is more useful as a source of failure modes to test than as a quality ranking. Recent agent-browser issues cover conflicting server and per-call session identity ([#2422](https://github.com/vercel-labs/agent-browser/issues/2422)), slow CDP attach with discarded tabs ([#2431](https://github.com/vercel-labs/agent-browser/issues/2431)), missing direct-page network events ([#2293](https://github.com/vercel-labs/agent-browser/issues/2293)), empty HAR success after browser restart ([#2290](https://github.com/vercel-labs/agent-browser/issues/2290)), and MCP output limits/boundaries skipped on a JSON path ([#2245](https://github.com/vercel-labs/agent-browser/issues/2245)). Recent Stagehand reports include active-page selection, already-idle `networkidle` waits, and a CLI timeout shorter than the requested wait ([#3371](https://github.com/browserbase/stagehand/issues/3371), [#3370](https://github.com/browserbase/stagehand/issues/3370), [#3369](https://github.com/browserbase/stagehand/issues/3369)). A Playwright MCP report describes a CDP session remaining unusable after browser disconnect ([#41617](https://github.com/microsoft/playwright/issues/41617)). These are individual reports and may be fixed or version-specific; validate the same scenarios on pinned current versions before making external claims.

### Competitive refresh: restored browser state versus task continuity (6 November 2026)

Agent-browser has moved session persistence beyond keeping a daemon alive. Its
merged [session-hardening PR #1486](https://github.com/vercel-labs/agent-browser/pull/1486)
adds stable worktree-scoped session IDs, namespace isolation, restore validation,
and automatic save/restore of cookies and localStorage across browser restarts.
The [current session guide](https://agent-browser.dev/sessions) also documents
periodic autosave and strict tab pinning across daemon restarts. Its current
MCP/EVE surface includes short-lived per-tool processes and an individual MCP
call mode for runtimes that cannot keep a process open, as described in the
[current changelog](https://github.com/vercel-labs/agent-browser/blob/main/CHANGELOG.md).
This makes stable session identity, state restore, and ephemeral execution
parity dimensions rather than unique Scout claims.

The security and correctness boundary is useful: browser-state restoration is
not task recovery. Saved cookies and localStorage are sensitive credentials;
the competitor docs explicitly warn that state files contain plaintext tokens.
The changelog records a fixed cross-origin redirect issue where restored state
could be written to the wrong origin ([PR #2212](https://github.com/vercel-labs/agent-browser/pull/2212)).
Do not describe that fixed report as a current defect. More generally, a
restored authenticated tab proves that a browser context was reconstructed; it
does not prove whether an interrupted checkout, form submission, or other
mutation committed.

Scout's current durable session handle intentionally reconnects only to the
same live browser context and exact target under the current owner and policy.
After a browser restart, the old handle fails closed. Scout's task receipt can
still preserve the interrupted operation and prohibit automatic replay, but
the host must reconcile through its authoritative application state before
starting a new task. That is a narrower persistence story than automatic auth
restore, and a stronger foundation for truthful mutation recovery. The
opportunity is to compose both needs without conflating them: optionally
reconstruct an owner-scoped browser session from host-managed state, label the
new browser generation, and keep old task outcomes `unknown` until application
evidence resolves them. Never treat restored cookies or a matching URL as
continuity proof for the old page or mutation.

### Competitive refresh: session handoff is converging (6 November 2026)

Browser Use PR [#6298](https://github.com/browser-use/browser-use/pull/6298)
is open and proposes `same_tab=true` for agentic recovery after MCP tools have
prepared a page. The proposal explicitly preserves domain restrictions,
serializes concurrent retries, and protects the reused session from idle or
explicit-close cleanup while the retry agent is running. Its author reports
12 GitHub Actions checks including six real-browser cases; the local live run
failed before Chromium exposed CDP, so that evidence is CI-backed proposal
coverage, not a merged release. Browser Use draft PR
[#6305](https://github.com/browser-use/browser-use/pull/6305) separately
proposes opt-in selection of the currently focused existing tab on initial
CDP attach, failing when selection is ambiguous. Its author reports tests with
headless Chrome and calls out headed and cross-platform gaps. Both PRs remain
unmerged. Treat same-session fallback and active-tab attach as converging
parity dimensions, then recheck pinned releases after merge.

**Product implication:** Scout should not claim those workflows as a moat.
The defensible experiment is whether Scout's host-owned task ID, redacted
receipt, authoritative-state reconciliation, and replay guard reduce duplicate
effects and false completion when a worker and browser fail at different
boundaries. A new `scout.recovery-observation/v1` API lets a browser broker
sign owner/generation/target/frame/policy evidence with Ed25519; a receiver
can validate that public-key signature and its two-minute freshness before
comparing it with the sender's HMAC receipt. The local recovery demo now
generates this signed observation and verifies it in an independent Node.js
receiver; the recorded rerun (local audit artifact)
still uses fixture-supplied owner/generation/policy claims and unauthenticated
agent-browser target output. Benchmark evidence still needs matched
repetitions and independent host-ledger grading.

**Decision experiment:** use a fixture with an authenticated session and a
delayed non-idempotent mutation. Kill the browser and worker at pre-dispatch,
post-dispatch/pre-response, and post-verification/pre-result-commit boundaries.
Compare Scout's explicit fail-closed reattach/reconciliation flow with
agent-browser's restore flow and a host adapter that combines restore with an
authoritative ledger lookup. Grade restored principal, target-generation
change, credential exposure, duplicate side effects, outcome classification,
and human/operator steps separately. Include a cross-origin redirect and
failed restore validation. If users value both auth continuity and safe task
reconciliation, build these as separate, composable contracts; do not promise
transparent browser recovery for an ambiguous mutation.

### Competitive refresh: request retention is parity; capture health is an opening (6 November 2026)

Playwright MCP's current release notes document retained request history
across navigation, capped at 200 earlier requests, with stable request indexes
([release notes](https://github.com/microsoft/playwright-mcp/releases)). Its
older issue about losing the initial navigation and pre-navigation requests
was closed by PR #377, so this is a resolved gap rather than a current
competitor defect ([issue #376 and fix](https://github.com/microsoft/playwright-mcp/issues/376)).
Scout's 200-record rolling window is therefore useful parity. A direct
comparison should verify whether initial document requests, redirects, and
requests before navigation are retained in the same scenarios.

The stronger opportunity is telling callers when event capture stopped being
trustworthy. The agent-browser issue list currently includes reports that
direct-page CDP connections miss console, request, HAR, trace, and profiler
events ([#2293](https://github.com/vercel-labs/agent-browser/issues/2293)) and
that `network har stop` can report success with zero requests after a browser
restart ([#2290](https://github.com/vercel-labs/agent-browser/issues/2290)).
A Playwright report described metadata-only traces after renderer disruption;
it was fixed upstream, demonstrating why Scout should turn the failure mode
into a regression fixture rather than advertise it as a current competitor
weakness ([#42423 and fix #42425](https://github.com/microsoft/playwright/issues/42423)).
These are individual reports and not prevalence evidence.

**Product implication:** live `network_summary` now distinguishes `capture_mode`
(`not_started`, `diagnostics_only`, `full_capture`, `unavailable`, `disabled`) from coverage
(`unavailable`, `not_enabled`, `diagnostics_only`, `known_gaps`, `disabled_at_read`, or
`active_no_known_loss`). `console_errors` and `failed_requests` expose the same
mode and coverage. It also exposes interruption, filtering, dropped records,
pending requests, and retention limit. This makes an empty response
more interpretable, while explicitly not claiming browser-wide completeness.
The next proof should inject browser restart, target replacement, and direct
CDP attach during capture on Scout and pinned competitor releases; compare
whether callers receive a warning, a partial artifact, a clean empty result,
or an explicit unknown state. This tests evidence integrity as a product
contract instead of assuming request-history retention is unique.

The direct-page report gives a precise fixture dimension: the provider sends
events without `sessionId`, while the runtime associates its page with an
empty session ID; the reporter found event filtering treated those identities
as different and returned success-shaped empty logs. Scout's conformance
protocol now records CDP connection mode and calls for both target-attached
and direct-page arms, with an independent event ledger and an explicit
unavailable result when a mode is unsupported. The built-in Scout
`RemoteCDP` path currently connects to a browser endpoint and attaches page
targets with `Target.attachToTarget`; it does not provide a direct-page
WebSocket mode. Its direct-page arm must therefore be reported unavailable
until a connector supports that transport. This should be run against actual
supported providers before making an interoperability claim.

#### Opportunity ranking

1. **Outcome and evidence contract (highest leverage).** Make every action produce a portable receipt that distinguishes “command accepted,” “page changed,” and “application outcome verified.” Include target identity, policy identity, evidence timestamps, and known capture gaps. Keep unknown mutation outcomes unknown and support explicit reconciliation. Scout already has task receipts, policy IDs, playbook step receipts, network manifests, trace screenshot manifests, and durable recovery; close the gaps and publish one concise schema and examples. This is more defensible than adding more selectors or wrappers.
2. **Recoverable, inspectable browser sessions.** Differentiate on reconnect to the exact live browser target, owner-scoped handles, safe in-flight lifecycle, and operator-readable resource state. Complete the lifecycle matrix (disconnect, server restart, browser restart, idle eviction, slow call, remote context disposal), then offer an easy health/status endpoint and metrics. The recent competitor reports show why the dimensions matter, while the current Scout fixtures demonstrate only selected paths.
3. **Trust boundaries by construction.** Mark all page-originated text, URLs, network bodies, screenshots/OCR, and nested task receipts as untrusted evidence; separate those from host instructions and policy decisions. The marker now covers core page results, network/trace/annotated/hybrid outputs, and task/playbook/action/recovery receipt boundaries. It is metadata, not sanitization or a prompt-injection defense. Add adversarial fixtures and verify how downstream clients preserve and heed it. Playwright MCP's community report calls out accessibility-tree text as a prompt-injection carrier ([#1479](https://github.com/microsoft/playwright-mcp/issues/1479)); treat that as a threat-model prompt, not a confirmed vulnerability.
4. **Prove useful workflows, not tool count.** Ship a small set of production recipes for authenticated browser workers, supervised handoff, deterministic extraction with postconditions, and crash-safe retry. Each should have a runnable example, typed Go API, MCP/CLI parity statement, and benchmark report with setup and limitations. This can make Scout simpler to adopt than a broad tool catalog while still covering real jobs.
5. **Adoption and integration ergonomics.** Reduce first-success time with a quickstart that chooses local Chrome, remote CDP, or managed browser; show MCP and Go paths; include policy configuration and troubleshooting. Add a compatibility matrix for MCP clients and popular browser providers. Avoid a hosted-browser product bet until evidence shows users need Scout to supply infrastructure instead of integrating cleanly with theirs.

#### Next research and validation gates

| Gate | Experiment | Decision it informs |
|---|---|---|
| Competitive workflow scorecard | Run the same 10 workflows on Scout, Playwright MCP, agent-browser, and Stagehand using pinned versions, identical browser/site fixtures, and measured success, retries, latency, tokens, and evidence completeness. Include a browser crash and one hostile page. | Which Scout advantages are real and which roadmap items are table stakes. |
| Lifecycle contract | Exercise every transport and ownership mode through slow calls, abrupt client loss, DELETE, idle expiry, browser restart, and process restart. Record whether browser/process resources, task state, and ownership all match the documented contract. | Whether session recovery is release-grade and which metrics/controls hosts need. |
| Integration fit interviews | Ask 5–8 Go teams that run browser workers where they persist state, authorize sites, reconcile ambiguous mutations, and hand off to people. Test the Go integration example with them. | Whether Go-native runtime plus recovery solves an urgent problem or needs a narrower target segment. |
| Trust propagation | Put prompt injection in accessible names, visible text, iframe content, network responses, OCR text, and task receipts; inspect raw protocol results and behavior in supported clients. | Whether provenance reaches the agent boundary and whether additional isolation/confirmation primitives are needed. |

**Near-term recommendation:** finish provenance coverage and adversarial fixtures, then publish the lifecycle matrix and comparative scorecard. Keep reliability, recovery, and policy as the core position. Pause further tool-surface expansion until the scorecard shows a missing workflow is costing real success. The evidence today supports a strong thesis, not yet a proven competitive lead.

### Research update: budget-aware verification is a product opportunity (6 November 2026)

The market is publishing controlled comparisons of browser-agent tools.
WebdriverIO's open benchmark holds the harness and task sample constant, pins
tool versions, and publishes judgments. Its current leaderboard pools runs
across two models on 100 live Online-Mind2Web tasks; it reports Stagehand 60%,
WebdriverIO session 62%, WebdriverIO MCP 59%, Playwright MCP 52%, and
agent-browser 48%. These are benchmark-specific rates with differing run
denominators, not a universal ranking; its methodology says the sample yields
confidence intervals around ±10 percentage points. The results support
measuring success, tokens, cost, duration, and tool calls together, but do not
score interruption recovery or host-authored reconciliation. See the fuller
[benchmark-coverage refresh](#competitive-refresh-benchmark-coverage-is-the-opening-6-november-2026)
and [source leaderboard](https://benchmark.webdriver.io/).

A separate community benchmark isolates more complex interaction patterns—dynamic waits, decoys, overlays, scrolling and pixel-only content—and points to token volume as a distinct cost. It also shows the comparison problem: the tools live at different layers and agent harness overhead can dominate the same CLI. Treat these as design cues, not externally validated Scout claims. ([web-agent-bench methodology](https://github.com/nsosio/web-agent-bench))

Fresh Stagehand issue reports point to the remaining product friction after broad feature parity: extraction values without source elements (#3406), false-green doctor output when local commands cannot connect (#3359), ambiguous active page behavior (#3371), and interaction edge cases like wrapped links (#3394). They are individual reports. Together they suggest that users need bounded, source-linked observations and truthful health/readiness signals, with reliable fallback when semantic interaction cannot resolve a target. ([Stagehand issue tracker](https://github.com/browserbase/stagehand/issues))

**Updated opportunity:** Scout can become the *verification and operations layer* for browser work. Keep the Go runtime and caller-owned browser integration, while making three things cheap and standard:

1. **Evidence-first results:** every extracted or changed value links to a source element or response, target identity, timestamp, and verification step; distinguish command success from application success.
2. **Context-budget controls:** support compact, bounded observations with stable references, field-level extraction, diffs, and explicit truncation; retain screenshots or full traces as optional evidence instead of repeating entire page trees.
3. **Operational truth:** `doctor` distinguishes static configuration validity from live browser readiness and gives actionable reconnect/retry/reconcile guidance. Attach and recovery latency should be measured at p50/p95 across realistic live, discarded, and crashed targets.

**What to build next:** add a single verified-extraction workflow that returns value + source locator + evidence reference + truncation state, then demonstrate it over Go, CLI and MCP from one receipt contract. Pair it with an independent scorecard covering task success, false verification, evidence attribution, tokens, latency, setup time and recovery. Use a deterministic fixture suite for confidence and a smaller pinned live-site run for transfer; publish raw outcomes and confidence intervals. The existing product note's outcome/evidence contract remains the highest priority; this update makes efficiency and first-use diagnostics measurable parts of that contract.

**Positioning guardrail:** do not claim Scout is more accurate, cheaper, or safer from feature inspection or issue counts. The benchmark evidence currently establishes that competitive performance depends on setup and task distribution. Scout's differentiator is a testable hypothesis: application teams can get a compact, attributable, recoverable result with less custom integration. Validate that against repeated matched runs and developer interviews before turning it into a market claim.

### Competitive refresh: lifecycle controls need two independent clocks (6 November 2026)

The latest upstream lifecycle work makes simple “idle cleanup” claims weak. Playwright MCP issue [#1795](https://github.com/microsoft/playwright-mcp/issues/1795) reported isolated browser contexts that stayed alive when an HTTP/SSE connection remained open without tool calls. Its linked [Playwright PR #43527](https://github.com/microsoft/playwright/pull/43527) merged on October 19, giving each isolated client its own timer and closing a shared browser once no client context remains. A different report, [Playwright issue #42693](https://github.com/microsoft/playwright/issues/42693), described the inverse failure in an affected release: the idle timer expired during an in-flight tool call and closed its browser. That issue is closed as “not planned,” so the report is a version-specific regression fixture rather than an upstream fix commitment. Together the two reports establish a useful conformance target: transport keepalive must not count as work, and active work must suspend idle eviction.

Scout already has separate request activity and in-flight accounting. Its HTTP lifecycle probe verifies that an SSE connection alone does not keep an idle client's browser alive, while the 1-second-idle/1.8-second-wait comparison passed 10/10 Scout repetitions and failed 5/5 against Playwright MCP 0.0.84. The result is narrow and version-pinned; it is not evidence about newer releases or the broader market. Keep the two properties as separate scorecard rows, retain raw runs, and rerun after competitor updates. ([Scout lifecycle protocol and reports](../examples/benchmark/README.md#the-streamable-http-lifecycle-probes-can-be-run-independently-without-an-external-fixture-server), [matched comparison details](../examples/benchmark/COMPARISON.md))

The same research suggests two adjacent product opportunities:

* **Lifecycle explainability:** show the caller why a session is alive or scheduled for expiry: active request count, last protocol activity, owner, browser ownership, and exact expiry action. Keep a separate hard operation deadline so protecting an in-flight call from idle cleanup cannot permit an indefinitely stuck operation. Document what happens on transport disconnect, explicit delete, process exit, and caller-owned remote browsers.
* **Attach-time predictability:** agent-browser issue [#2431](https://github.com/vercel-labs/agent-browser/issues/2431) reports slow CDP attachment with discarded tabs; measure cold and warm attach separately for live, discarded, and crashed targets. Preserve strict target identity if optimizing attach, and report the target recovery mode rather than silently adopting a different active tab.

Agent-browser's open [session-argument ownership report #2422](https://github.com/vercel-labs/agent-browser/issues/2422) is a high-value isolation fixture: the report says an MCP tool argument can override the server's configured session. Its linked [PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430) is also still open in this tracker snapshot. Test this as a wrong-owner attempt across Scout's HTTP principals and named sessions, asserting that a caller-controlled name cannot escape the authenticated owner namespace. Recheck both statuses before using them as competitor claims.

**Research implication:** lifecycle confidence is a stronger near-term competitive wedge than persistent sessions alone. Keep three questions distinct in the product and scorecard: (1) is the browser still reachable and attached to the exact target; (2) is an operation currently in flight and protected from idle cleanup; (3) has the application-side postcondition been verified after an ambiguous mutation. A green health check answers only the first question. The Stagehand report [#3359](https://github.com/browserbase/stagehand/issues/3359) specifically alleges a false-green local doctor result while page commands fail; Scout's default static doctor and opt-in live/probe modes already separate configuration validity from tested browser and application reachability. Continue to label each level explicitly rather than collapsing them into one “healthy” bit.

**Next experiment:** extend the existing lifecycle scorecard with open-stream idle expiry, in-flight-call protection, a call that exceeds its hard operation deadline, abrupt client disconnect, and per-owner isolation. For each run, capture protocol activity, in-flight count, idle deadline, operation deadline, browser process/context state, target ID, result classification, and cleanup latency. Run the same fixture against pinned Scout, Playwright MCP, and agent-browser versions; publish the raw JSON and mark unsupported transport/ownership combinations unavailable. This turns scattered issue reports into reusable falsifiable cases and tests whether Scout's implementation and diagnostics stay correct as upstream products converge.

### Implementation follow-through: frame-bound single-element extraction (6 November 2026)

Scout's `extract_all` and table workflows already return source references or
frame identity, but single-element `extract` returned the target ID without the
selected frame ID. That left consumers unable to bind standalone extracted text
to the same target/frame contract used by observations and action receipts.
`ElementResult` now includes additive `frame_id` in both regular and
context-aware extraction paths. MCP clients receive the field through the
existing result shape; the README documents that an empty ID means the main
frame. This is runtime identity metadata, not a durable DOM locator: callers
must still re-check selectors after page mutation.
# Competitive refresh: continuity is parity; integration evidence is the test (6 November 2026)

Current upstream releases narrow the opportunity further:

- **Stagehand has two official Go module lines.** The active monorepo's v4 SDK
  release notes include per-call locator and snapshot timeouts, bounded batch
  and RPC deadlines, disconnect diagnostics, and Browserbase lifecycle fixes.
  The separate official `stagehand-go/v3` repository is also active; v3.21.1
  shipped on 23 October 2026 with a local-server security update. Compare the
  selected v4 line for protocol-first APIs, and do not describe v3 as archived
  ([v3 repository](https://github.com/browserbase/stagehand-go),
  [v3 releases](https://github.com/browserbase/stagehand-go/releases),
  [v4 SDK source](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go),
  [shared changelog](https://github.com/browserbase/stagehand/blob/main/CHANGELOG.md)).
- **Agent-browser has a strong restore workflow.** Its release notes describe
  worktree-scoped session IDs, namespace isolation, restore validation, and
  automatic state saving. Recent changes also address unsafe storage restore
  across redirects and domain containment across workers, popups, and reused
  daemon sessions ([release history](https://github.com/vercel-labs/agent-browser/blob/main/CHANGELOG.md)).
- **Playwright MCP has added practical session and output controls.** v0.0.84
  includes named isolated contexts, runtime URL parameter redaction, and
  bounded `browser_find` results; its docs distinguish persistent profiles
  from isolated contexts and warn that persistent profiles cannot be shared by
  concurrent browser instances ([release](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.84),
  [profile documentation](https://github.com/microsoft/playwright-mcp#user-profile)).

These features make “Go support,” browser restore, named sessions, output
limits, and health commands poor standalone differentiators. They are baseline
expectations to match and measure. The opening left for Scout is a more specific
integration outcome: a Go host can attach its own task ID, policy, deadline,
verified page condition, and durable reservation to a browser operation, then
reconcile ambiguous dispatch against its own business system without granting
the browser tool replay authority. This is an inference from the current
feature sets, not proof that buyers value the contract or that Scout requires
less integration work.

The next competitive experiment should therefore compare the same deterministic
task and interrupted mutation in Scout and Stagehand Go, including host code
that reads an independent ledger. A separate MCP workflow should compare
Playwright MCP and agent-browser on tool setup and session ownership, but should
not be used to infer Go embedding effort. Report setup time, lines of host glue,
dispatch count, authoritative outcome, duplicate effects, and recovery steps.
Do not combine these tracks into one score.

### Competitive refresh: trustworthy controls beat feature-count parity (6 November 2026)

Recent open reports expose two concrete quality gates. Stagehand issue
[#3394](https://github.com/browserbase/stagehand/issues/3394), reproduced on
Go SDK v4.1.0 and v4.3.0, reports clicks on wrapped inline links returning
success while the event target misses the link. The reported cause is using the
center of a union box instead of a per-fragment content quad. Scout used the
same `DOM.getBoxModel` center calculation in `Selection.ClickContext` and
`HoverContext`; the selection path now prefers the center of the largest
`DOM.getContentQuads` fragment, with the box model as a fallback. The local
Chrome fixture [`wrapped_link/main.go`](../examples/benchmark/wrapped_link/main.go)
confirms two rendered fragments, an old union-box midpoint landing on the
surrounding paragraph, and Scout's click changing page state to `CLICKED`.
The captured run is
`wrapped-link-click-2026-11-06.json` (local audit artifact).
This is one local runtime reproduction, not a matched competitor result or a
release-wide reliability claim.

Agent-browser issues [#1902](https://github.com/vercel-labs/agent-browser/issues/1902)
and [#1997](https://github.com/vercel-labs/agent-browser/issues/1997) are open
reports from v0.37.1/v0.38.1: an explicitly empty allowed-domain list is treated
as unrestricted, and MCP string values beginning with `-` may be parsed as
command flags. They point to a broader product bar: policy configuration must
distinguish “unset” from “configured but empty,” while structured tool input
must stay data all the way to dispatch. Scout's MCP handlers receive typed
JSON and do not construct a shell command, but `SCOUT_MCP_ALLOWED_ORIGINS` had
the same unset/blank ambiguity. Startup parsing and `doctor` now reject a set,
blank value and advise either supplying origins or unsetting it. The
repeatable [`run_origin_policy_probe.py`](../examples/benchmark/run_origin_policy_probe.py)
probe confirms unset and valid-origin settings pass the policy check, while
blank, whitespace, and comma-only settings fail both `doctor` and fresh MCP
startup. Its raw report is
`origin-policy-empty-config-2026-11-06.json` (local audit artifact);
`doctor` still reports unrelated readiness warnings in the passing cases.
On 12 November, the reported behavior was reproduced against pinned
`agent-browser@0.42.0` with a destination-side HTTP counter: blank and
comma-only environment values each allowed the denied request, while the
unset control was unrestricted and a valid `localhost` allowlist blocked it.
The runner and capture are
[`run_agent_browser_empty_policy.py`](../examples/benchmark/run_agent_browser_empty_policy.py)
and `agent-browser-empty-policy-2026-11-12.json` (local audit artifact).
This confirms the specific ordinary-fetch behavior in one local setup, not its
prevalence or a general comparative security advantage. The Scout `doctor`
diagnostic now explicitly says empty values are invalid and that the variable
must be unset or contain exact origins. A separate one-run edge case against
pinned Playwright MCP 0.0.85 passed `--allowed-origins=`; its independent
destination counter recorded the denied request. Empty environment input had
the same result; whitespace and separator-only environment values instead
blocked the allowed fixture page before its script ran. Since Playwright
documents allow-all as the default, the empty value is recorded as behavior
rather than a defect. The comparison report keeps CLI, environment, and
configuration-file semantics distinct; configuration-file behavior is still
untested.

These issue reports are adversarial test cases, not evidence of how often users
hit the failures. These probes establish that Scout's current local
implementation meets those predicates; they do not show how often upstream
users encounter the reported bugs or that Scout is safer overall. The
competitive opportunity is to make *truthful execution*
observable: a dispatched click should be checked against a caller-declared
postcondition, an empty configured boundary should not become allow-all, and
the diagnostic should name the condition it actually checked. Add the wrapped
link case in the matched geometry suite and policy parser/startup cases in the
clean-install scorecard. Do not turn isolated upstream bugs into blanket
claims that Scout is safer or more reliable.

The Stagehand comparison should use the selected Go SDK v4 in the active
`browserbase/stagehand` repository. The separately maintained standalone v3
line remains official, so the study must pin the API generation rather than
calling v3 archived. Stagehand advertises Go SDK v4 as a supported route, while issue
[#2744](https://github.com/browserbase/stagehand/issues/2744) reports measured
API gaps blocking one user's full Playwright exit. Treat the issue as a useful
task-selection source: include a representative PDF, network-event, or
emulation need only if Scout and the selected Stagehand version both expose a
comparable path. The primary recovery study remains about integration effort
and outcome handling, not maximizing API surface.

### Comparator setup correction: active Stagehand Go SDK (6 November 2026)

The adoption-study draft previously referred generically to “Stagehand Go” and
could be read as switching between API generations. The primary comparator is
the Go v4 implementation in the active Stagehand monorepo at
`packages/sdk-go`. The official Go quickstart still shows
`github.com/browserbase/stagehand/packages/sdk-go/v4@v4.0.0`, but the latest
stable tagged v4 module found on 6 November 2026 was v4.3.0
(`h1:5ZXuSNhVSk2OjkyTCcTfdSaozZJP1JO8U4l6cY39dZs=`), which requires Go 1.26.
The quickstart starts local Chrome and calls `Stagehand.Create` with a provider
API key for semantic
`act`, `extract`, and `observe` ([SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go),
[official Go quickstart](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/quickstart.mdx)).
The Go SDK also exposes direct page/locator APIs. Therefore the fairest
deterministic control is a local, model-free browser task through Scout and
Stagehand's direct page APIs, with task outcome and host-authored recovery code
counted separately. Measure Stagehand's model key and semantic-action setup in
a separate AI-enabled arm; record key setup as an adoption prerequisite, but
do not charge model-call cost or timing to the deterministic page API, and do
not mix the AI arm into the primary pair. Recheck the module version at
recruitment because the monorepo's SDK changes faster than this study
document. No participant comparison has run.

### Competitive refresh: attach delay and recovery guidance (6 November 2026)

Two current issue reports refine the lifecycle scorecard. Agent-browser issue
[#2431](https://github.com/vercel-labs/agent-browser/issues/2431) is open and
reports that v0.41.2 CDP connection can take about 6.3 seconds when multiple
tabs are discarded: its described implementation probes the first target, then
starts a second round for remaining targets. The reporter proposes probing in
one concurrent round while preserving lowest-live-index selection. This is a
specific, version-pinned report with a proposed patch in the issue; it is not a
general attach-latency distribution or evidence every user sees the delay. The
follow-up [PR #2438](https://github.com/vercel-labs/agent-browser/pull/2438) is
open and proposes probing discarded targets in one concurrent round. Its
author reports the targeted test falling from about 6.02 to 3.03 seconds. Rerun
the attach benchmark against a release containing the change before treating
the reported delay as a current comparison point.

Playwright MCP issue
[#1588](https://github.com/microsoft/playwright-mcp/issues/1588) describes a
remote CDP disconnect that left later tool calls failing until the caller ran
`browser_close` and navigated again. The recovery fix merged in upstream
[Playwright PR #40652](https://github.com/microsoft/playwright/pull/40652).
This is historical evidence that reconnection instructions and automatic
recovery behavior matter; verify the fix in the pinned Playwright MCP release
under test. Stagehand issue
[#3359](https://github.com/browserbase/stagehand/issues/3359) remains open and
reports local page commands failing while its doctor reports all checks green.
A maintainer-side follow-up reports that a provider-free mock reproduced an
`ok` doctor verdict after the manager's launch failed with `ECONNREFUSED`; the
physical Chrome startup failure remains unreproduced. Scout marks skipped live
browser checks `not_checked`, bounds explicit live attach checks, and offers an
opt-in isolated-tab application probe. Scout now also raises the top-level
status to `warning` when browser readiness was not checked, while keeping
configuration-only preflight successful. This makes its summary status clearer
to callers that do not inspect individual checks; no matched competitor doctor
trial has run. The repeatable local probe now records configuration-only,
live attach, a valid application marker, and a missing-marker failure in
`scout-doctor-readiness-2026-11-06.json` (local audit artifact).
The run passed all four expected classifications; it is a single Scout smoke,
not a repeated reliability estimate or peer comparison.

**Product implication:** keep recovery correctness as the product thesis, and
add attach/readiness latency as an operational guardrail. The next scorecard
should include (a) fresh attach with all-live tabs, one discarded first target,
and multiple discarded targets; (b) a remote CDP drop followed by the documented
recovery path; and (c) doctor verdicts for unused, launch-failed, attached-but
unresponsive, and application-probe-failed states. Capture p50/p95 attach time,
first verified read time, recovery steps, truthful status classification, and
whether any mutation occurred before identity was re-established. Keep
configuration preflight, browser reachability, page responsiveness, and
application health as separate reported states. The issue pages identify
adversarial cases; they do not establish market prevalence or Scout's relative
performance.

### Study fixture rehearsal result (6 November 2026)

The shared three-stage fixture has direct-API facilitator adapters for Scout
and the active Stagehand Go SDK v4.3.0. Each sequential rehearsal completed
first success, a nominal approval control, then a delayed approval followed by
process-group interruption and fresh-process ledger reconciliation. Both
passed all 22 runner checks. The nominal and faulted approval each committed
one effect; after interruption both products retained `unknown`, inspected the
authoritative ledger, recorded its result, and did not retry. Scout used its
native bbolt task store; Stagehand used a host-authored fsync-backed journal.
The three-stage summary (local audit artifact)
and raw runs (local audit artifact) document this
fixture result. The earlier 21-check paired reports predate the nominal control
and remain historical evidence. This closes a fixture-plumbing gap only.
Facilitator-written adapters do not provide comparative evidence on participant
effort, usability, or buyer demand. The next meaningful evidence remains
clean-checkout participant implementations with source review and
counterbalanced order.

### Competitive refresh: lost acknowledgments are becoming explicit (6 November 2026)

The recovery thesis needs a sharper boundary. Agent-browser v0.39.1 added a
guard against duplicate daemon command execution after a lost response: for
mutating and launch commands, it stops retrying once request bytes may have
been sent and reports `outcome_unknown` in CLI JSON and MCP. The change was
merged in [PR #2085](https://github.com/vercel-labs/agent-browser/pull/2085)
and released in v0.39.1. This is meaningful
parity for a single command's lost acknowledgment. Scout should not position
the word `unknown`, or “never blindly retry,” by itself as unique. The test
that remains is whether the host can retain a stable operation identity and
recover across process restart, inspect a durable task record, attach an
authoritative application resolution with provenance, and make a safe next
decision across its Go, CLI, and MCP surfaces. Agent-browser's later release
v0.41.2 also fixes session restarts that lost pages when timeout settings
changed, another reminder that browser continuity and task outcome are separate
contracts. ([agent-browser v0.39.1](https://github.com/vercel-labs/agent-browser/releases/tag/v0.39.1),
[v0.41.2](https://github.com/vercel-labs/agent-browser/releases/tag/v0.41.2))

Independent recovery research supports evaluating that boundary, while also
warning against overclaiming from Scout's narrow fixture. UndoBench reports a
large gap between nominal competence and conditional recovery on its evaluated
agent workflows, and reports that recovery depends on where the failure lands;
after-commit verification and server-side idempotency helped in its experiments,
while simple local journaling did not solve composite partial-mutation cases.
Its task suite uses mocked enterprise APIs and a limited number of independent
workflow clusters, so use its results as motivation and method guidance, not as
a browser-tool market estimate. Its paired control/fault design suggests a
useful improvement to Scout's study: record a nominal successful control for
each task arm and compare it to the faulted run, so recovery is separated from
basic product/setup competence. ([UndoBench paper](https://arxiv.org/abs/2610.05622),
[code and benchmark methodology](https://github.com/tradertanmay/undobench))

Playwright MCP v0.0.84 adds named isolated contexts, runtime URL-parameter
redaction, and first-class page-defined WebMCP tools; those capabilities
strengthen the parity baseline for isolation, secret handling, and direct
application-tool interaction. They also sharpen an opportunity question:
where a site exposes WebMCP, does an execution lifecycle need to span a
page-provided business tool as well as browser UI actions? Scout should test
identity, provenance, and replay semantics around such tools before claiming
the browser is the only execution boundary. ([Playwright MCP v0.0.84 release](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.84))

**Revised opportunity:** prove that a Go host can carry a durable operation
contract across UI and page tools, process loss, browser replacement, and
authoritative reconciliation with less bespoke glue and no duplicate effects.
The current paired rehearsal verifies the post-commit lost-response fixture for
facilitator-written adapters only. It does not test before-mutation or partial
mutation failures, browser-process loss, concurrent tasks, WebMCP dispatch,
server-side idempotency, or participant effort. Add those as distinct fault
phases and controls in the next benchmark version; do not combine them into a
single recovery score.

### Competitive refresh: saved flows add a replay/workflow baseline (10 November 2026)

Agent-browser released v0.42.0 on 10 November with experimental saved flows.
The feature records a task's successful request and command path, then tries
direct HTTP, page requests, and recorded commands in tiers, retaining the
successful tier. Its merged PRs describe site-bound flows, live-cookie use,
write approval, and a rule against resending a write after acceptance or an
ambiguous response. An unanswered write is reported as `outcome_unknown`;
authentication failures, rate limits, challenges, and redirects receive
specific handling. This substantially improves the baseline for reducing
repeated UI work and handling ambiguous writes within a saved flow. ([v0.42.0
release](https://github.com/vercel-labs/agent-browser/releases), [flow
recording PR #2236](https://github.com/vercel-labs/agent-browser/pull/2236),
[tiered replay PR #2397](https://github.com/vercel-labs/agent-browser/pull/2397))

**Product implication:** “safe replay” and “do not resend an ambiguous write”
are not Scout's wedge by themselves. Saved flows may cut repeated task cost
while retaining explicit write controls. The product hypothesis that remains
is narrower: can an independently owned host carry a durable operation ID
through process loss, inspect authoritative business state, attach that
resolution with provenance, and safely decide what to do next? The release
notes and PRs describe flow-level request/command recording and fallback; they
do not describe a durable Go host task reservation or application-ledger
reconciliation contract. That is a reading of the documented scope, not proof
the two cannot be composed. Verify behavior against the pinned release before
making any comparative claim.

Keep this CLI/MCP flow test separate from the deterministic Go SDK adoption
study. A useful comparative fixture should run the same recurring,
non-idempotent operation through a saved flow and Scout, with a plain host plus
Playwright as an optional control:

1. Record and save a successful nominal flow; measure the initial setup cost.
2. Repeat normally; record latency, requests, UI commands, approvals, output,
   and model/token use where applicable.
3. Kill the worker after the fixture commits but before its acknowledgment
   reaches the caller; restart from a fresh process and retain the same logical
   operation ID.
4. Inspect the independent fixture ledger, report whether the outcome remains
   unknown until checked, and attempt the same logical task again.
5. Add shape drift, auth expiry, 5xx/408 responses, and two POST operations on
   one path to test tier fallback and ambiguous matching.

Grade duplicate commits, false completion, unresolved-outcome retention,
cross-process ID continuity, authoritative reconciliation and provenance,
operator steps, write prompts, recovery operations, successful tier, and normal
path cost separately. In particular, do not assume saved-flow fallback causes
duplicate effects: the published PRs explicitly describe no-resend behavior
for uncertain writes. Treat narrower matching cases and flow-specific limits
as test hypotheses from those PRs, not as demonstrated failures. The protocol
now has a reproducible paired probe in
[`run_agent_browser_saved_flow_recovery.py`](../examples/benchmark/run_agent_browser_saved_flow_recovery.py).
On the pinned v0.42.0 repeated run, both arms used the same local fixture,
non-idempotent mutation route, and authoritative ledger. The saved flow used
its direct HTTP tier; Scout dispatched a browser click. The flow returned
`outcome_unknown` after one request had committed; Scout's fresh-process same-task retry was
blocked before dispatch and host reconciliation resolved `verified_complete`
(10-pair summary (local audit artifact),
expanded 30-pair summary (local audit artifact)).
Both arms passed 30/30; the Wilson 95% interval for each rate is 88.6–100%.
This validates repeated behavior in one matched fault protocol and confirms
the published no-resend behavior on this fixture. Dispatch paths differ, and
the result does not establish comparative reliability, integration cost, or
latency. Measure clean-repository setup and participant implementation effort
before the next opportunity gate, without attributing demand to the CLI/MCP
capability alone.

### Competitive refresh: background mode changes the tab-control baseline (10 November 2026)

Playwright MCP v0.0.85, released 9 November, adds a `--background` option for
extension mode. It leaves the connected tab inactive and the browser window
unfocused, and opens client-created tabs in the background. Its release notes
specify Chrome 158.0.8127.2 or later for opening the token-protected connect
page in the background. This is a newer, narrower capability than the v0.0.84
headless tab-selection comparison: it applies to an attached extension session
and does not establish correct target ownership under external tab churn.
([v0.0.85 release](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.85),
[upstream issue #42973](https://github.com/microsoft/playwright/issues/42973))

**Benchmark consequence:** keep the existing HeadlessChrome 157 measurements
as historical target-selection evidence. Add a separate headed Chrome 158+
scenario using a live extension connection and the background option. Measure
window focus, active tab, new-tab placement, target identity, and action result
independently; then inject external activation, closure, and popup creation
while a delayed action is in flight. A test that only checks window focus would
miss whether the task remains bound to its original page. Do not describe the
older 0.0.84 result as covering this mode.


### Competitive refresh: extension workflows preserve more operator context (10 November 2026)

The Playwright MCP v0.0.85 release also ships two controls that the earlier
background-mode note did not cover: `--no-tab-group` leaves controlled tabs in
their existing browser groups, and `browser.contextOptions.locateOnlyVisible`
prevents element tools and unsafe locator code from matching hidden elements.
The corresponding upstream PRs are merged: [#43814](https://github.com/microsoft/playwright/pull/43814)
(tab-group control) and [#43903](https://github.com/microsoft/playwright/pull/43903)
(visible-only locators), alongside [#43864](https://github.com/microsoft/playwright/pull/43864)
(background mode). These are shipped Playwright capabilities in v0.0.85, not
open proposals.

**Product implication:** session ownership should include operator-visible
browser behavior, not only target IDs. Background execution, tab grouping, and
visible-only targeting address focus disruption, workspace organization, and
hidden duplicate matches. They raise the parity bar for Scout's connected
browser use. The opportunity is to combine such session controls with a
host-owned task and outcome contract, while preserving user intent across
concurrent operations. Scout should benchmark focus changes, tab-group
membership, active target, and hidden-element disambiguation independently;
recovery results do not substitute for these interaction guarantees. These
features do not establish correct owner routing under tab churn or authoritative
business completion.

**Study version check (10 November 2026):** the selected active Stagehand v4 Go
module still resolves to `github.com/browserbase/stagehand/packages/sdk-go/v4@v4.3.0`
(`go list -m ...@latest`), even though the monorepo's open release PR #3438
stages TypeScript Stagehand 4.4.1 packages. Do not infer a Go SDK update from
the monorepo's TypeScript release train. Keep v4.3.0 pinned for the deterministic
adoption arm unless the module itself publishes a newer Go tag; record the Go
module checksum at recruitment. The distinction is a current package-version
fact, not a product-quality comparison.

### Competitive refresh: Stagehand publishes two active Go lines (10 November 2026)

A current GitHub check corrects the earlier claim that the standalone Go
repository was archived. `browserbase/stagehand-go` describes itself as the
official Go library and lists v3.21.1 as its latest release on 23 October
2026. That release updates the default local-server download to v3.7.7,
including Node.js 24.22.1 and Undici 7.30.1 with October security fixes. The
active monorepo separately ships the protocol-first Go SDK v4; its changelog
lists v4.3.0 and the v4 quickstart documents local Chrome and direct page APIs
([v3 repository](https://github.com/browserbase/stagehand-go), [v3 releases](https://github.com/browserbase/stagehand-go/releases), [v4 SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go), [shared changelog](https://github.com/browserbase/stagehand/blob/main/CHANGELOG.md)).

The adoption study keeps v4.3.0 as its primary comparator because that is the
protocol-first API line already rehearsed for model-free direct-page work. This
is a scope choice, not evidence that v3 is unsupported or inferior. A separate
v3 run would answer whether the prior official Go integration has materially
different setup, local-browser, or recovery characteristics; do not merge its
timings with the v4 arm. Refresh both module versions and checksums immediately
before recruitment.

### Competitive refresh: silent browser replacement is an identity test (10 November 2026)

Agent-browser v0.42.0 is the current release checked for this note. Its release
fixes session reuse and relaunch by keeping a connection target only after
attach succeeds, and persisting security settings across daemon restarts
([release notes](https://github.com/vercel-labs/agent-browser/releases/tag/v0.42.0)).
An open report, issue [#2079](https://github.com/vercel-labs/agent-browser/issues/2079),
describes successful `press` commands silently landing in a replacement
`about:blank` browser, but its reproduced versions are 0.33.2 and 0.37.1; the
report does not establish that v0.42.0 is affected. Treat it as a fault
hypothesis to rerun, not a current-version defect.

The newer ownership report is more direct: issue
[#2422](https://github.com/vercel-labs/agent-browser/issues/2422) says a
per-call MCP session name can override the server-pinned session and route two
servers into the same browser. Its proposed fix,
[PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430), scopes the
tool session under the pinned server identity and remained open in the
10 November tracker check. Recheck the PR and release before making a shipped
competitor claim.

**Product opportunity:** make browser identity drift observable at the task
boundary. For every task, retain the owner, session, browser generation, target,
and policy binding; before reporting success or continuing after reconnect,
verify they still match. A changed browser must stop the old task and preserve
its prior dispatch uncertainty for host reconciliation. Add the same fault
matrix to Scout and pinned competitor runs: change the caller-selected session,
replace the browser during a task, preserve the process but relaunch the
browser, and induce a successful-looking command on a blank replacement. Grade
wrong-owner access, identity-change detection, false completion, task replay,
and operator recovery separately. The local Scout recovery example proves
rejection of a prior signed handoff after browser replacement; it does not yet
measure repeated in-task identity drift or competitor behavior on v0.42.0.

**Pinned-release recheck (12 November 2026):** I ran 40 consecutive
`ArrowDown` commands against agent-browser v0.42.0 on macOS arm64, checking the
CDP target ID, URL, title, cookie, and `window.name` after every press. All five
signals remained stable. The older issue's reported browser replacement did
not reproduce in this run. This is one process/session on one OS, so it does
not close the lifecycle scenario or establish cross-platform behavior. The
per-command observations and summary are retained in
`agent-browser-0.42.0-press-identity` (local audit artifact).

**Pinned MCP ownership recheck (12 November 2026):** two agent-browser v0.42.0
MCP servers used distinct `AGENT_BROWSER_SESSION` values but received the same
tool-level `session: "shop"` argument. In this one local fixture run, server B
read server A's seeded cookie and `localStorage`, and server A observed server
B's probe navigation. The cross-server collision in issue [#2422](https://github.com/vercel-labs/agent-browser/issues/2422)
therefore reproduced on the pinned v0.42.0 release. Proposed [PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430)
was still open in the live tracker check; [v0.42.0's release notes](https://github.com/vercel-labs/agent-browser/releases/tag/v0.42.0)
do not list that change. Preserve the fixture as a release regression case,
and rerun after an upstream release that includes the fix. The full report and
transcript are in
`agent-browser-0.42.0-mcp-session-override-20261112T0421Z` (local audit artifact).

Scout's ownership boundary is different and must be described precisely:
stateful HTTP scopes a session registry by authenticated principal and MCP
transport session, while opt-in stateless HTTP uses the authenticated
principal as its stable owner. Thus distinct principals are isolated, but
stateless clients intentionally sharing one principal share that principal's
session namespace. This supports reconnects; it is not a same-principal
multi-agent isolation guarantee. The product opportunity is to let an operator
derive narrower agent or workspace owners under a credential without weakening
the stable identity needed for reconnect and recovery. Before implementing
that, define lifecycle and credential rotation semantics, then add a repeated
multi-client collision benchmark covering both Scout HTTP modes and the
competitor regression fixture.

### Competitive refresh: one-shot MCP calls expose a transport question (10 November 2026)

Agent-browser v0.42.0 adds `agent-browser mcp call <tool> '<json>'`, which
invokes one MCP tool without keeping a stdio server process open. The same
release still offers its long-lived MCP server and experimental saved flows
([v0.42.0 release](https://github.com/vercel-labs/agent-browser/releases/tag/v0.42.0)).
This makes process-per-call operation a current competitor baseline; it does
not establish stateless Streamable HTTP support, browser-session persistence,
or durable task identity across invocations.

Scout's `mcp serve-http` endpoint remains initialize-based and stateful by
default. Opt-in stateless mode supports modern discovery for authenticated
principals with an opaque per-call session handle. Chrome-backed integration
covers session routing, task and artifact scoping, quota cleanup, idle
protection, and cancellation. Official TypeScript and Python SDK smokes now
exercise create/navigate/read/close, cancel an in-flight wait, and read again
through the same handle. This is local compatibility evidence, not a broad
production claim: a focused integration confirms stale handles fail after a
single-process restart and fresh handles work afterward, while multi-replica
load balancing and additional client SDKs still need validation. See the
implementation inventory in
[`go-service-integration.md`](go-service-integration.md#mcp-http-protocol-compatibility).

**Opportunity and evidence gate:** validate whether stateless compatibility
is useful to target clients before making it a product lead. Compare the same
deterministic browser task through (1) one-shot MCP call, (2) persistent stdio
MCP, (3) Scout's stateful HTTP, (4) Scout's opt-in stateless prototype, and
(5) an initialize-era MCP client.
Record setup-to-first-call time, per-call startup/attach latency, context reuse,
owner/session continuity, and recovery after a lost response or process exit.
Require the same browser context and task ID for like-for-like repeated calls;
label fresh-context calls separately. Check which target clients actually
require `server/discover` and whether their workload benefits from stateless
transport. The prototype still needs multi-replica load-balancing behavior and
additional client SDKs before a broader
production compatibility claim.

**Implementation gate update (10 November 2026):** the pinned `go.klarlabs.de/mcp`
v1.30.1 source confirms that `WithStreamableStateless()` removes transport
session creation and the `Mcp-Session-Id` requirement, while requiring the
`Mcp-Method` routing header. The server's `SessionStore` remains transport
protocol state; it does not own Scout browser sessions. An opt-in prototype now
uses configured principal authentication as the stable registry owner and reads
the opaque Scout session handle from decoded per-call `_meta`; browser/session-
scoped calls fail closed without a valid handle. Owner-level configuration and
lifecycle operations bind to the authenticated principal, while explicit close
or configured idle expiry replace transport `DELETE` as cleanup paths. A live
local Chrome fixture confirms `server/discover`, no transport session ID, handle
issuance from `list_sessions`, navigation and same-handle target continuity
across independent requests, and parallel navigation through two handles to
distinct targets. Missing/named, unknown, unauthenticated, and foreign-principal
handles are rejected before browser access. The close/revocation path still needs
live validation; this is one local run and does not prove repeated concurrency,
task/artifact namespace isolation, or cleanup/idempotency under disconnect or
browser failure. The prototype uses `SCOUT_MCP_HTTP_STATELESS=true`; stateful
transport remains the default.

### Competitive status recheck: saved flows ship, ownership fix remains proposed (10 November 2026)

The agent-browser v0.42.0 release shipped experimental saved flows that try
direct HTTP, page requests, and recorded-command replay, and a separate fix for
session reuse/relaunch after failed attachment. The release notes do not list
the MCP pinned-session override fix. As of this check, issue
[#2422](https://github.com/vercel-labs/agent-browser/issues/2422) and PR
[#2430](https://github.com/vercel-labs/agent-browser/pull/2430) remain open.
The PR adds regression coverage for a pinned server session, explicit matching
session, unpinned server, `extraArgs` override, and invalid pinned name. It
reports 1,866 Rust unit tests passing with unrelated sandbox failures, plus
54 documentation tests and 439 route tests passing. These are author-reported
checks on the proposed branch; they are not evidence that v0.42.0 ships the fix
or that the issue affects every deployment. Recheck before publishing any
current capability claim.

Stagehand issue [#3359](https://github.com/browserbase/stagehand/issues/3359)
also remains open in this tracker check. It reports that local page commands
fail to connect while `browse doctor` returns all green. The report itself is
not a reproduced release-wide defect; retain it as a diagnostic-conformance
case and rerun against the selected current version. Browser Use issue
[#6295](https://github.com/browser-use/browser-use/issues/6295) remains open,
with PR [#6298](https://github.com/browser-use/browser-use/pull/6298) linked as
the proposed same-session recovery implementation. Treat both as upstream
activity, not a shipped competitor capability, until merge and release status
are verified.

#### Upstream status recheck (12 November 2026)

The issue remains open. PR #6298 is also open and unmerged; GitHub currently
reports `mergeable=false` and `mergeable_state=dirty`, and its last update was
5 November 2026. Its description reports regression and real-browser checks,
and two automated review summaries say earlier findings were addressed, but
those are proposed-branch reports. They do not establish a clean merge or a
released same-session recovery feature. Recheck after upstream updates before
changing the release-level comparison.

**Product implication:** do not position Scout around session pinning or saved
workflow replay. Both are active competitive work, and saved flows raise the
recurring-work baseline. The researchable distinction remains a durable host
operation identity that survives worker loss, links the outcome decision to
authoritative application evidence, and carries owner/target/policy provenance.
Test that distinction with the direct Go study separately from the CLI/MCP
saved-flow comparison; current fixtures establish feasibility but not lower
effort or buyer preference.

### Competitive refresh: Browser Use 1.0 changes the category boundary (10 November 2026)

Browser Use released 1.0.0 on 10 November and published 1.0.1 later that same
day to fix initial release issues. Version 1.0 replaces most of its prior Python agent engine
with a persistent Python REPL operating directly on Chrome DevTools Protocol;
its release describes a 136,866-line reduction and makes a hosted Browser Use
browser and gateway model the defaults. Local Chromium remains selectable, and
the release also documents typed output, file transfer, redacted sensitive
values, domain restrictions, and an MCP/CLI surface. Version 1.0.1 adds a
server/container path that installs its own Chromium, isolates each run's
workspace, and fixes a cloud-browser allowed-domain bypass. These details come
from the project's release notes, not an independent evaluation. ([Browser Use
1.0.0 and 1.0.1 releases](https://github.com/browser-use/browser-use/releases))

The release's own benchmark reports results for its selected model/browser
configuration; those vendor figures are not a comparable Scout result. The
meaningful product shift is that browser-use is now an opinionated agent,
runtime, cloud-browser, and hosted-model package rather than only a local
automation library. Its default model/cloud path raises its convenience bar
for teams that want an end-to-end agent. Its ability to choose local Chromium
and external models also means Scout cannot claim model optionality or local
execution as unique capabilities.

**Opportunity implication:** focus Scout on infrastructure already owned by a
Go service: deterministic browser execution without a provider key, typed
task-level results, host-supplied idempotency and transactional persistence,
and a durable reconciliation record that stays separate from browser/session
restoration. Measure the integration cost and operational controls against
Browser Use 1.0.1's CLI/MCP local-browser mode as a distinct comparator, and
against its default cloud/agent mode as a separate end-to-end choice. Do not
compare tool-only latency to an agent run, or assume the stated benchmark
scores transfer to Scout's deterministic task distribution. Validate exact
dependency constraints, browser ownership, secret handling, workspace cleanup,
origin enforcement, worker-loss classification, and host-ledger reconciliation
on the pinned 1.0.1 release before revising the position. Until that matched
work and participant study exists, this is a competitor-scope update and
research hypothesis, not a win claim.

#### Reproducible next comparison

#### First-success diagnostic comparison (10 November 2026)

A one-run workstation probe compared Scout doctor plus a real bounded read
against pinned agent-browser 0.42.0 doctor plus a real read on the same
loopback `/healthz` fixture. Both doctors passed. Scout's application probe
and browser cleanup passed, Scout verified the expected `ok` text and rejected
a missing marker, and agent-browser read the page text; its missing-marker
case was graded by the harness because `get text body` has no equivalent
postcondition flag. Scout's command wall times were 1.72 s for doctor, 1.30 s
for the positive read, and 1.95 s for the missing-marker read. Agent-browser's
doctor was 2.68 s, its `open` command was 2.55 s, and its separate text read
was 0.24 s. These are single CLI invocation timings on one workstation, not
matched end-to-end task latency or a reliability result. The full transcripts
and predicates are in the local first-success audit artifact
and the [reproduction script](../examples/benchmark/run_first_success_comparison.py).

The initial probe run gave Scout a temporary HOME, XDG directories, and
TMPDIR. It passed browser launch but timed out on its page-navigation CDP call;
subsequent runs using Scout's normal workstation runtime paths passed. Treat
the initial timeout as a harness-induced environment artifact, not a Scout
defect. This corrected run is still a local diagnostic only: it does not test
clean-container onboarding, multiple failure modes, explanation quality,
participant effort, or user preference.

The pinned executable is invoked as `uvx --from browser-use==1.0.1 browser-use
--cli-mcp`; its version check reports Browser Use 1.0.1 with browser-harness
0.1.13. The first package-resolution check established only those versions;
the subsequent local recovery runs are recorded below. No cross-tool win is
claimed.

Use the existing `/saved-flow-task` fixture and its application ledger for a
matched local MCP study. Persist one operation ID and a pre-action checkpoint
in the host process; call Browser Use's `browser_exec` to inspect the page and
dispatch the same POST-backed task; then terminate the MCP process after the
fixture confirms commit but before acknowledgment. Start a fresh pinned MCP
process and have the host query the authoritative ledger before it decides
whether to retry. The primary outcome is exactly one committed effect and a
truthful resolved/uncertain host result. Keep browser/session restoration as a
separate measure. Run the same host ledger and fault boundary against Scout;
report successful completions, duplicate effects, unresolved outcomes, setup
steps, host glue, recovery calls, and wall time separately. Preserve raw MCP
transcripts and fixture state for each repetition. This evaluates the local
MCP integration path; it does not compare Browser Use's hosted agent defaults
or establish a broad reliability rate.

The first two runs used Browser Use 1.0.1's `--mcp` compatibility alias and
passed the predicate on its isolated Chromium. They are retained as an initial
integration check, separate from the actual CLI-MCP arm: first run (local audit artifact),
second run (local audit artifact).

The direct `--cli-mcp` arm used a fresh Chrome for Testing 158 profile and a
loopback-only `BU_CDP_URL`, with Browser Use 1.0.1 and browser-harness 0.1.13.
Across six consecutive runs, each fresh MCP server navigated to the fixture,
committed exactly one effect, was killed before page acknowledgment, then
restarted; the host read the application ledger and did not replay (6/6).
The median from just after MCP process spawn through reconciliation was 2.49 s
(range 0.84–14.62 s). This includes fixture navigation and two MCP sessions,
but is not comparable with other tools' timing. The aggregate and per-run
transcripts (local audit artifact)
preserve every result. Three earlier adapter attempts timed out during
navigation at Browser Use's five-second daemon IPC boundary; after adding
isolated runtime directories and limiting the spawned process environment,
the six-run matrix passed. Those early adapter iterations are not mixed into
the six-run result.

This establishes caller-side ledger reconciliation around Browser Use MCP
process loss on one local setup. The host controller remained alive, so this
does not exercise durable host checkpoint reload or host-process restart. It
also does not show a native Browser Use task record or a Scout advantage.

#### Host-worker restart extension

A second adapter added a caller-authored SQLite operation checkpoint. For each
completed trial, the supervisor killed both the dispatch worker and its MCP
server after the fixture committed. A distinct worker then reloaded the
checkpoint, started fresh Browser Use MCP, queried the fixture ledger, and
persisted `verified_complete` without replay. It also called `browser_exec`
after restart and confirmed that the current page was still reachable. Ten
completed trials passed all predicates (10/10), including browser-session
reconnection and exactly one request/effect per task. One additional attempt
failed before scenario execution when local `go build` intermittently reported
standard-library packages missing; the rerun and three further checks passed.
That setup failure is retained separately in its raw artifact and excluded
from the ten completed trials. Aggregate and run artifacts (local audit artifact).

This is evidence that a host can compose durable task identity and business
outcome reconciliation around Browser Use MCP, even after the host worker
restarts. It does not demonstrate native Browser Use task persistence: the
SQLite checkpoint and reconciliation adapter were caller-authored. The
supervisor, fixture, and Chrome process remained alive, so it does not cover
browser-process loss. Scout has also passed this fixture with its native
durable task store, but these runs do not measure comparative glue, setup
effort, or adoption. The supported product hypothesis is that Scout can make
this host contract easier to adopt in Go; that requires a counterbalanced
implementation study and operator evidence.

**Schema implementation note (10 November 2026):** a proposed ordinary `session_id` argument cannot be added only in Scout middleware. In pinned `go.klarlabs.de/mcp` v1.30.1, typed inputs generate closed JSON schemas and `Tool.Execute` validates arguments before calling the handler. `Server.Use` wraps request dispatch but cannot rewrite `tools/list` responses. Until the library offers a supported input-schema extension or Scout registers explicit schemas, keep the stateless prototype's opaque handle in `_meta`; do not advertise an undiscoverable argument. A future argument must be removed before typed-input validation, conflict-checked against `_meta`, and resolved only within the authenticated owner's registry.

### 11 November issue refresh: domain controls and cross-origin frames

Agent Browser issue [#2574](https://github.com/vercel-labs/agent-browser/issues/2574)
was opened against v0.42.0 on 11 November. Its author reports that `--allowed-domains`
can leave a cross-origin iframe paused after an auto-attached target's
`Runtime.evaluate` times out; the parent navigation then wedges and may be
replaced by `about:blank`. The report includes allowed and denied iframe hosts
and a Stripe Elements case. This is a single reporter's reproduction, not a
frequency estimate or yet a confirmed root cause.
At the 11 November 22:45 UTC tracker check, the issue had no comments and no
matching open pull request was found.

It sharpens Scout's egress conformance question: domain denial should block the
out-of-policy frame while leaving the top-level page and later browser calls
usable, and allowed cross-origin frames should complete normally. Scout's
reference-proxy fixture already has destination-side iframe checks, but the
checked-in report covers a local proxy policy flow, not Scout's own browser
startup and page-target policy path under OOPIF attach failure. Add that paired
case before claiming iframe-policy resilience; retain page-level interception
and proxy behavior as distinct coverage scopes.

To repeat this local arm, install the pinned browser under the temporary path
and run the [benchmark adapter](../examples/benchmark/run_browser_use_recovery.py):

```sh
env UV_CACHE_DIR=/tmp/scout-browser-use-uv-cache \
  PLAYWRIGHT_BROWSERS_PATH=/tmp/scout-browser-use-browsers \
  uvx --from browser-use==1.0.1 browser-use install
env UV_CACHE_DIR=/tmp/scout-browser-use-uv-cache \
  PLAYWRIGHT_BROWSERS_PATH=/tmp/scout-browser-use-browsers \
  python3 examples/benchmark/run_browser_use_recovery.py
```

### 11 November refresh: readiness and discarded renderers

Recent GitHub reports reinforce that “browser connected” is not a sufficient
readiness signal. Playwright issue [#44052](https://github.com/microsoft/playwright/issues/44052),
opened 11 November 2026, reports `connectOverCDP` hanging when a page renderer
is busy. Stagehand issue [#3359](https://github.com/browserbase/stagehand/issues/3359)
reports local page commands failing while `browse doctor` says all checks are
OK; issue [#3431](https://github.com/browserbase/stagehand/issues/3431) asks for
a repair hint after a browser start failure. These are individual reports,
not estimates of how often users encounter the failures.

The same class of problem can occur after connection. Agent Browser issue
[#1036](https://github.com/vercel-labs/agent-browser/issues/1036) documented
startup hanging on a discarded tab. The project later closed it with a
live-target selection fix. Follow-up [#1528](https://github.com/vercel-labs/agent-browser/issues/1528)
reported tab switching could still hang and stall queued commands. Merged
[PR #1532](https://github.com/vercel-labs/agent-browser/pull/1532) added bounded
renderer probing, discarded-tab revival, transactional switch state, dialog
handling, and best-effort successor recovery on close. This suggests a sharper resilience gate for
Scout: exercise attach, observe, switch, and close separately against both
discarded tabs and a busy renderer, and assert each operation returns within a
bound while preserving the selected target and avoiding unintended focus
changes.

Scout's doctor now requires a bounded page read before reporting a live CDP
session healthy, bounds cleanup, and explicitly labels skipped probes. A
Chrome integration fixture with a renderer blocked in JavaScript confirms the
doctor reports a page-read failure and still completes cleanup. That covers
diagnostic truthfulness, not operational tab switching or recovery. A matched
fault study should next pin competitor versions and run the same frozen-tab
and busy-renderer cases through attach, switch, observe, and close, preserving
raw transcripts and measuring latency, queue recovery, focus changes, and
cleanup. Until that study is run, this is a product opportunity and a test
gate, not a competitive win claim.

### 12 November implementation: curated MCP catalog

Scout now accepts `--curated` on stdio and Streamable HTTP MCP servers. It
intersects with the selected profile and keeps common navigation, observation,
interaction, extraction, and task recovery tools. The opt-in mode preserves the
existing full-catalog default for compatibility. Startup validates
`--require-tools` against both filters, and the live MCP handshake returned 53
tools with task recovery available. This addresses tool-choice overhead as a
configuration option; it is not a differentiator by itself. Compare it with
`core` and full catalog in the pinned model benchmark using actual provider
usage counters and verified task outcomes before making efficiency claims.

### 12 November implementation: proxy routing across agent interfaces

`SCOUT_PROXY_SERVER` now configures locally launched browsers from one-shot and
stateful CLI, MCP, and chat UI entry points. A live CLI navigation and an MCP
`navigate` call both reached a loopback fixture through the configured proxy;
`scout doctor` reports the proxy as unverified and rejects credential-bearing
proxy URLs. The proxy still owns its allowlist and destination rules. This
closes an interface-access gap for the existing proxy mitigation, not the
browser-wide egress evidence gate: remote CDP and traffic that bypasses the
configured proxy still require host/network controls and independent tests.

### 12 November refresh: cross-origin frame resilience and MCP ownership

The [agent-browser #2574 report](https://github.com/vercel-labs/agent-browser/issues/2574)
remained open in the 12 November tracker view. Its author reports that
`--allowed-domains` can wedge navigation or a later wait when network controls
encounter a cross-origin iframe; the report is scoped to v0.42.0 on macOS and
Chrome for Testing 157. No matching fix PR appeared in the tracker search at
this check. Treat it as a useful fault-injection case, not a general statement
about agent-browser reliability.

Scout now has a Chrome-backed page-policy fixture that puts one allowed and
one denied cross-origin iframe under an allowed parent, then reads the parent
again. `go test -tags=integration . -run
'^TestIntegrationAllowedOriginsCrossOriginIframes$' -count=10` passed all ten
local repetitions.
This shows the ordinary page-target path can make the allow/deny decisions and
remain usable in that fixture. It does not reproduce an OOPIF attach timeout,
repeat the case across browser versions, compare with agent-browser, or prove
proxy enforcement. Keep the cross-origin test in the conformance harness and
add a separate induced attach-stall arm before claiming the failure mode is
covered.

Scout's OOPIF route uses explicit `Target.attachToTarget` with the caller's
deadline, then validates the owner/target binding before switching. A delayed
attach response exposed a cleanup gap: Chrome may create the target session
after the caller deadline, while the generic CDP call discards the late
response. Frame attachment returns at the caller's deadline and retains a
bounded 250 ms listener for the late response; when Chrome reports a created
session, Scout detaches it. A protocol fixture verifies the caller returns
promptly, the late child attachment is detached, and parent cleanup retries a
retained child session after an injected detach failure. This covers resource
cleanup for the narrow attach-timeout case; it does not claim the competitor's
auto-attach navigation failure is reproduced or that the full browser-policy
behavior is equivalent.

The agent-browser [same-site iframe interaction report #2472](https://github.com/vercel-labs/agent-browser/issues/2472)
remained open in the same tracker check. It reports a case where an in-process,
same-site cross-origin frame can be inspected while selector clicks do not
reach its controls; the author contrasts it with the site-isolated frame path.
Scout's new Chrome integration regression reproduces that topology with two
`localhost` origins and verifies both a child DOM mutation and link navigation
after `SwitchToFrame`. This uncovered a local false-success click: dispatch
returned nil while the child state remained unchanged. The fix resolves the
selected child DOM node, checks the element at its center with
`elementFromPoint`, and dispatches pointer and mouse events in its owning
execution context. The regression verifies a pointer handler runs, a covering
overlay rejects the click as obscured, and a child link navigates. Neighboring
same-site and site-isolated frame action checks pass. This remains DOM event
simulation rather than OS-level pointer input; it does not compare
agent-browser behavior or cover every browser version and transformed/nested
frame geometry.

The agent-browser [session ownership issue #2422](https://github.com/vercel-labs/agent-browser/issues/2422)
and proposed [PR #2430](https://github.com/vercel-labs/agent-browser/pull/2430)
also remained open. The issue describes tool-selected MCP sessions escaping a
server-pinned session; the PR proposes scoping those names. Scout's existing
authenticated MCP tests already prove that same labels map to separate opaque
sessions for separate principals, but the next comparison should include two
MCP servers for the same principal and deliberately colliding caller labels.
This distinguishes owner isolation from session-name collision handling.
Scout deliberately treats the authenticated principal as its trust boundary:
stateless transports for one principal share its sessions to support
reconnection. The README now tells operators to map mutually isolated agents
to distinct principal IDs and tokens. This is an explicit deployment choice,
not a same-principal isolation guarantee; do not compare it with a
server-pinned browser session as if the boundaries were identical.

Chrome-backed stateless and stateful HTTP integrations now reuse the same task
ID and plan across two sessions for one principal: each session executes
against its own target, and retrying in the original session replays only its
own result. This verifies the principal/session-name task-store namespace in
one Scout process; collisions between separate Scout servers remain
unmeasured.

A stateful reconnect regression now distinguishes transport identity from the
logical task namespace: a replacement transport creates a new browser target,
but a retry with the same principal, session name, task ID, and plan returns
the prior result with `idempotency_replay: true` and does not act on the new
target. This supports safe outcome recovery while making the logical session
name part of the idempotency contract. Client UX should surface that scope
before a caller expects a same-ID retry to execute in a fresh browser.

Finally, [Playwright MCP v0.0.85](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.85)
adds extension-mode options to leave the active browser in the background and
preserve existing tab groups. These are operator-context controls, not a
recovery advantage. Scout's headed-browser study should separately record
focus changes, selected-tab ownership, and group preservation rather than
folding them into task correctness.

### 12 November recheck: in-flight idle eviction remains reproducible

Playwright MCP v0.0.85 was released on 9 November. The release carries forward
the per-client isolated HTTP idle-timer fix from merged
[Playwright PR #43527](https://github.com/microsoft/playwright/pull/43527),
which closed [playwright-mcp issue #1795](https://github.com/microsoft/playwright-mcp/issues/1795).
That fix concerns idle clients sharing a browser; it does not establish
protection for a single tool call that runs longer than its idle timeout.

I reran the latter case against the v0.0.85 package using the same local
`/delayed-element?delay_ms=1800` page, visible `Ready` predicate, 1,000 ms idle
timeout, and HeadlessChrome 157. Scout passed 5/5 calls (1.809–1.826 s) and
expired the session after each call. Playwright MCP failed 5/5 at 1.007–1.013 s
with the browser closed during `browser_wait_for`. This matches
[Playwright issue #42693](https://github.com/microsoft/playwright/issues/42693),
which remains closed as not planned, and is a version-pinned fixture result,
not a production failure-rate or general superiority claim. Raw captures and
scope limits are retained in
`playwright-inflight-idle-current-2026-11-12` (local audit artifact).

This confirms one narrow lifecycle regression signal still exists in the
current release. Keep the investment focused on two-clock lifecycle semantics
and operator-visible recovery. Before claiming a durable competitive lead,
repeat on a later Playwright release and measure resource cleanup, abrupt
disconnect, and active-call hard-deadline behavior in the same scorecard.

#### Upstream status recheck (12 November 2026)

GitHub still marks [Playwright issue #42693](https://github.com/microsoft/playwright/issues/42693)
closed as “not planned.” Its reproduction and cause describe the idle timer
running through an active tool call. The latest tagged MCP release remains
[v0.0.85](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.85),
and the local 5/5 failure result above is explicitly pinned to that package.
The repository's current [README on `main`](https://github.com/microsoft/playwright-mcp/blob/main/README.md)
has 10 commits since that tag, so main-branch documentation or code must not be
treated as behavior shipped in v0.0.85. Re-run the fixture against the next
published package before updating the release-level comparison.

Scout's MCP cleanup loop checks the session's in-flight call count before
removing an idle session (`cmd/scout/mcp.go`). Preserve the paired lifecycle
contract in regression coverage: active work survives the idle threshold;
once it completes, an idle session can expire; and a separate hard deadline,
if configured, is reported as a distinct interruption. This is the concrete
product behavior to protect, while the competitive claim stays limited to the
local fixture and tagged versions.

### 12 November refresh: saved flows and first-party MCP narrow the gap

The latest primary-source release notes change the comparison in two places.
Agent-browser v0.42.0 (10 November) now has experimental saved flows, including
direct HTTP, page-request, and recorded-command replay tiers, plus a one-shot
MCP call for environments that cannot keep a stdio process open. Its release
also describes consolidated session reuse/relaunch and security-setting
preservation across daemon restarts. These features overlap with Scout's
recurring-flow and short-lived-call use cases, so “persistent Go SDK vs
stateless CLI” is no longer a sufficient positioning distinction. The release
does not establish equivalent durable mutation receipts, host-authoritative
reconciliation, or a no-replay-after-uncertain-dispatch contract; those need a
matched fixture before claiming a difference. See the [v0.42.0 release
notes](https://github.com/vercel-labs/agent-browser/releases).

Browser Use 1.0.2 (11 November) now provides a first-party CLI/MCP route with
its own Chromium when remote debugging is disabled, longer CDP waits, and a
clearer error for dialog-paused pages. Its 1.0 line also advertises domain
restrictions across browser modes, typed results, uploads/downloads, and
explicit secret redaction. This means Scout should not claim broad MCP
availability, domain policy, or structured output as a unique advantage.
Browser Use is an agent-oriented, Python/model workflow; Scout's testable
position is the host-owned Go execution contract and recovery evidence. The
release's own benchmark reports model task scores and cloud-browser cost; those
figures are not comparable to Scout's deterministic local fixture. See the
[1.0.2 release](https://github.com/browser-use/browser-use/releases) and the
[1.0 feature and security notes](https://github.com/browser-use/browser-use/releases).

Playwright's official Trace Viewer remains a strong baseline for action
timelines, before/after DOM snapshots, screenshots, logs, and local inspection
of CI traces. Scout's evidence work should complement that debugging workflow
with durable operation identity, explicit uncertainty, and reconciliation
hooks, while measuring privacy and output cost. It should not position action
receipts as a replacement for a mature trace viewer. See the [Trace Viewer
documentation](https://playwright.dev/docs/trace-viewer).

**Product consequence:** make the recovery contract the differentiator to
test, not the mere existence of sessions, MCP tools, saved flows, policy
controls, or structured results. The 12 November Chrome-backed run now covers
Go, CLI, and MCP worker loss for ten repetitions each, with live receipt
parity, strict decoding, same-ID retry blocking, and one-effect fixture-ledger
reconciliation. The adjacent 10-pair agent-browser v0.42.0 comparison confirms
both paths avoid duplicate effects in the tested boundary, while only Scout's
flow exercises the host task reservation and reconciliation contract; the
dispatch methods differ, so this is not an advantage claim. The next
competitive experiment should add a same-browser second-runner recovery arm
with authenticated owner, browser-generation, target/frame, and policy
bindings. The next product proof should replace the demonstration ledger with
a transactional host adapter and test it with target Go service teams. Keep
setup cost, model use, and task completion as separate measures.

**Matched fixture follow-up (12 November 2026):** the v0.42.0 saved-flow
runner was repeated for ten paired writes on macOS 27.0.1 arm64. Both arms
passed 10/10: each operation committed once from one request. Agent-browser's
recorded flow used its HTTP replay tier and surfaced `outcome_unknown` after
the response was held; Scout used a browser click, blocked the interrupted
task's same-ID retry, and accepted host reconciliation from the shared ledger
as `verified_complete`. This shows complementary outcomes in this fixture,
not a general reliability advantage: both avoided an in-run resend, and the
dispatch paths differ. The 95% Wilson intervals are 72.2–100% with only ten
runs. The full summary and per-run logs are retained in
`agent-browser-0.42.0-saved-flow-recovery-20261112T024500Z` (local audit artifact).

### 12 November status check: tab pinning is a contract question

The open [agent-browser issue #1867](https://github.com/vercel-labs/agent-browser/issues/1867)
reports that an unpinned, long-lived CDP-attached Edge session can fall back
to an unrelated tab after the bound tab is closed or becomes unresponsive;
the reporter also describes viewport operations that return success without
matching readback. The issue page still has no linked branch or pull request.
Its reproduction is pinned to 0.38.0 on Edge 153 and explicitly distinguishes
the documented `AGENT_BROWSER_PIN_TAB=1` fail-closed behavior. This is a useful
contract case, not evidence that the current 0.42.0 release has the same
failure or that default fallback is universally wrong.

Keep this separate from [issue #1530](https://github.com/vercel-labs/agent-browser/issues/1530):
that earlier shared-Chrome session/target binding report is closed and linked
to [PR #1589](https://github.com/vercel-labs/agent-browser/pull/1589), whose
fix restores a persisted target on reattach. The current v0.42.0 release notes
also describe consolidated session reuse and relaunch behavior. Do not present
the resolved attach-binding issue as an incumbent defect.

Scout's matching proof should run a long-lived attached-browser scenario that
interleaves navigation, viewport changes, external tab activation and closure,
and a stalled renderer. Before accepting success, independently assert target
ID, URL, viewport, and a page-specific effect. Existing Scout target-bound
task checks and viewport readback cover parts of this contract; the matched
long-run Edge scenario is still unmeasured. The recent pre-dispatch identity
guards strengthen one mutation boundary but do not replace that end-to-end
target attribution test. This keeps the product opportunity focused on
detectable false success and explicit recovery, rather than feature-count
claims.

Scout's first matched-shape regression now performs 28 navigation and
viewport cycles against a live local Chrome target while a second CDP client
activates, closes, and recreates a competing tab. All cycles retained the
bound target, URL, page marker, and verified viewport. This is a Scout-only
local Chrome result; the current Edge/agent-browser reproduction remains
unverified, so it is a fixture capability check rather than comparative
evidence.
