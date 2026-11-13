# Scout opportunity brief

**Working thesis, 10 November 2026**

## Opportunity

Build the browser execution layer for Go services and agent workers that must
know what happened when browser work times out, disconnects, or crosses a
process restart.

Most browser tools make it easy to inspect and operate a page. Scout's more
specific opportunity is to make the caller's execution contract inspectable:
who owns the session, which target and policy a task used, whether dispatch may
have occurred, what was verified, and what the host must reconcile before it
tries again.

This is a product hypothesis, not validated market demand or proven competitive
superiority. The repository contains fixture evidence, but no participant
interviews or matched onboarding study.

## Target user and job

**Initial user:** a Go platform, QA, or automation engineer who operates
browser-backed jobs from a service, worker pool, or agent host and owns the
resulting failure handling.

**Job to be done:** run a browser task inside the host's existing Go system;
when the browser call returns or the process disappears, distinguish a verified
result from a rejected or unknown result, retain task identity, and use the
host's authoritative system to decide whether follow-up work is safe.

**High-value trigger:** a browser mutation may have reached the application,
but the caller did not receive a conclusive result. Examples include a submit
followed by transport loss, a worker restart after dispatch, or a browser
replacement before recovery.

## Why Scout may fit

- One Go engine is available through the library, CLI, MCP server, and chat UI.
  Core deterministic actions do not require a model provider.
- Task IDs, plan fingerprints, target binding, deadlines, typed outcomes, and
  a durable store provide a host-visible execution lifecycle.
- Ambiguous task IDs remain reserved. Recovery exports a redacted plan and
  receipt but does not authorize replay.
- A host can record an authoritative reconciliation conclusion while Scout
  clearly labels it as a host attestation, not browser proof.
- Origin-policy identity and enforcement scope are visible, while page-layer
  checks remain distinct from browser-wide network egress controls.

These are implementation traits supported by code and local fixtures. Their
value to buyers, comparative setup cost, and production recovery performance
are not established yet.

## Positioning to test

> For Go teams running browser work in services or workers, Scout provides one
> composable engine and durable task lifecycle, so a worker can reconcile
> authoritative application state after interruption before deciding what to
> retry.

Test this against two alternatives:

1. “Go-native browser automation with verified actions and deterministic
   replay”
2. “A focused browser toolset for AI agents through MCP”

Do not lead with “safer,” “more reliable,” or “faster” until matched studies
measure those claims against current pinned competitors.

### What current competitor work changes

Session continuity is becoming parity: Browser Use has an open same-session
agent-recovery PR that preserves restrictions and session lifetime, plus a
draft for choosing the active tab on CDP attach. Agent-browser has an open fix
for MCP session arguments escaping server-pinned scope. Recent 10 November
research adds agent-browser v0.42.0 saved flows and one-shot MCP calls,
Playwright MCP v0.0.85 background and tab-group controls, and Browser Use
1.0's agent-centered local/cloud workflow. These raise parity on workflow,
session continuity, and operator context. See the [latest competitive
refreshes](competitive-opportunities.md#competitive-refresh-one-shot-mcp-calls-expose-a-transport-question-10-november-2026)
and the [Browser Use 1.0 assessment](competitive-opportunities.md#competitive-refresh-browser-use-10-changes-the-category-boundary-10-november-2026)
for status and source links.

A public browser-agent leaderboard now compares seven tools on live-site task
success, cost, tokens, duration, and tool calls. The dated 6 November snapshot
reports Stagehand and WebdriverIO near the top of its cohort, but does not measure
post-commit worker loss, duplicate mutations, or the host's recovery code. This
means Scout should complement generic capability scores with a narrowly
matched recovery-conformance study, while tracking normal-path latency and cost
as guardrails. Scout is absent from that leaderboard; these results neither
rank Scout nor show that its recovery workflow is superior. See the dated
[benchmark-coverage refresh](competitive-opportunities.md#competitive-refresh-benchmark-coverage-is-the-opening-6-november-2026).

The comparison boundary has narrowed further. Agent-browser v0.39.1 added
`outcome_unknown` for mutating commands when request bytes may have been sent;
v0.41.2 then improved session restarts when command timeout settings change and
preserves open pages. Playwright MCP v0.0.84 added named isolated contexts and
runtime URL parameter redaction. These releases make honest ambiguity,
continuity, and session isolation necessary parity, not a unique position. The
testable wedge is a durable, cross-process host contract that retains task and
target identity, supports authoritative state reconciliation with provenance,
and works consistently through Go, CLI, and MCP. See the [agent-browser
releases](https://github.com/vercel-labs/agent-browser/releases), [Playwright
MCP v0.0.84](https://github.com/microsoft/playwright-mcp/releases/tag/v0.0.84),
and the dated [competitive refresh](competitive-opportunities.md#competitive-refresh-lost-acknowledgments-are-becoming-explicit-6-november-2026).

The sharper hypothesis is that Scout reduces the Go host's work after an
ambiguous business mutation: bind one operation ID to the task, preserve
`unknown` until an authoritative source is checked, retain that source with the
host's resolution, and prevent same-ID replay. Competitors' browser-session
recovery can preserve where to continue; it does not itself determine whether
the application committed an operation. That distinction is a product angle,
not proof of superiority: application idempotency and reconciliation are host
responsibilities and can be implemented around any browser library. The next
matched study must count glue code and operator steps as well as duplicate
effects and correct outcome classification.

**Implementation evidence update (12 November 2026):** a Chrome-backed local
fixture now exercises this flow through the Go SDK, CLI, and MCP for ten
worker-loss trials per interface. All thirty interrupted receipts remain
`unknown` / `may_have_dispatched`, pass both the Go decoder and independent
Python verifier, and forbid replay. Fixture-ledger reconciliation leaves one
effect per task, and 30 live receipt comparisons pass normalized field parity
with frame binding. This closes the local three-surface conformance gate; the
fixture does not authenticate the producer or independently establish
production browser bindings, and it does not show buyer value. See the [raw
capture and method](../examples/benchmark/evidence/recovery-receipt-parity-live-2026-11-12/live-receipt-parity.json).

Issue-driven research adds a narrower quality gate to that thesis: truthful
click targeting and fail-closed policy configuration. Scout had the same
union-box click-point pattern cited in a current Stagehand Go issue, so the
selection path now uses rendered content quads when available. Scout's global
origin-policy loader also now distinguishes an unset variable from one set to
blank. Local Chrome and fresh-process CLI probes now pass for these cases, with
repeatable runners and raw reports linked in the dated [competitive
refresh](competitive-opportunities.md#competitive-refresh-trustworthy-controls-beat-feature-count-parity-6-november-2026).
They are implementation evidence, not matched comparative proof of an
advantage.

Agent-browser v0.42.0 adds experimental saved flows with direct HTTP, page
request, and recorded-command tiers. Its published flow PRs include explicit
write approval and no resend after an accepted or ambiguous write. That raises
the baseline for reducing repeated UI work and handling lost acknowledgments
inside one flow. Scout's opportunity is therefore not replay safety alone; it
is the host's durable operation identity and evidence-backed business-state
resolution across process loss. This remains a product hypothesis, and the
release does not establish whether hosts can or cannot compose an equivalent
contract around the flow. A 30-pair local run passed the saved-flow and Scout
predicates in 30/30 trials each (Wilson 95% intervals 88.6–100%), with
different HTTP and browser-click dispatch paths. This shows repeatability on
the fixture, not a comparative win or user demand. Keep the direct Go SDK
adoption comparison unchanged. The CLI/MCP process-loss matrix passes 10/10
per surface, with in-flight recovery, same-plan replay blocking, fixture-ledger
resolution, and matching
policy and task fingerprints in every pair (Wilson 95% intervals 72.2–100%).
This is repeated fixture conformance evidence, not customer reliability or a
competitive result. Compare integration effort and run application-level
scenarios before making a product claim. See the [CLI/MCP report](../examples/benchmark/evidence/recovery-receipt-parity-live-2026-11-12/live-receipt-parity.json)
and the [30-pair report](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/report.json).
See the [10 November competitive refresh](competitive-opportunities.md#competitive-refresh-saved-flows-add-a-replayworkflow-baseline-10-november-2026).

Browser Use 1.0.0/1.0.1 also shipped on 10 November: an agent-centered Python
REPL over CDP, with cloud browser/model defaults and selectable local Chromium,
plus CLI/MCP operation. The release includes typed results, file handling,
secret redaction, domain restrictions, and per-run workspace isolation. This
raises the convenience baseline for end-to-end agent buyers and means Scout
cannot claim local execution or model choice as unique. Keep Scout's target
narrower: deterministic execution embedded in Go services, with host-owned
operation identity and business-state reconciliation. Compare against both
Browser Use's local CLI/MCP path and its default cloud-agent path, while
separating tool runtime cost from full agent cost. These are release-note
capabilities; matched quality, effort, and cost evidence is still missing.
See the [Browser Use 1.0 assessment](competitive-opportunities.md#competitive-refresh-browser-use-10-changes-the-category-boundary-10-november-2026).

The portable handoff now has a strict JSON Schema for its signed envelope,
including the embedded recovery receipt, owner scope, browser-generation
claim, key ID, issue/expiry timestamps, and signature shape. Go and the
independent Node receiver both reject a lifetime over 15 minutes; the schema
documents that cross-field rule because JSON Schema does not portably express
the timestamp subtraction. This makes the wire shape reviewable by host
integrators, but does not authenticate a production broker or prove business
outcome. See the [handoff contract](recovery-receipt-contract.md#portability-and-privacy-rules)
and [schema](schemas/scout-recovery-handoff-v1.schema.json).

The pinned `--cli-mcp` path first passed six lost-acknowledgment trials while
the host controller remained alive. A follow-up killed the dispatch worker and
MCP after commit, then used a distinct worker to reload a caller-authored
SQLite checkpoint, start fresh MCP, reconnect to the still-running Chrome page,
and reconcile against the fixture ledger. Ten completed trials passed with one
request/effect and browser-session reconnection in every trial (10/10). One
other attempt failed during fixture compilation before scenario execution and
is documented separately. This demonstrates caller-side composition across
host-worker restart, not native Browser Use task persistence or a Scout win;
Scout has its own native durable-store fixture results. The key open question
is comparative integration effort and operator preference, which needs a
counterbalanced study. [Protocol, artifacts, and limits](competitive-opportunities.md#host-worker-restart-extension).

Playwright MCP v0.0.85 also adds a background-browser option for extension
mode. Existing tab-selection measurements used v0.0.84 in headless Chrome, so
they do not cover attached headed sessions or this new option. Extend the target
ownership benchmark with a headed Chrome 158+ arm before making claims about
background behavior or focus safety; preserve the old results as a separate
baseline. See the [10 November background-mode refresh](competitive-opportunities.md#competitive-refresh-background-mode-changes-the-tab-control-baseline-10-november-2026).

The same v0.0.85 release adds controls for preserving browser tab groups and
restricting locator matches to visible elements. This expands the parity bar
from session selection to the operator's workspace and hidden-element handling.
Add these as separate observable predicates in the headed extension study; do
not fold them into the recovery score. The [follow-up competitor refresh](competitive-opportunities.md#competitive-refresh-extension-workflows-preserve-more-operator-context-10-november-2026)
records the merged upstream PRs and the current Stagehand Go module version.

The finalized sessionless MCP direction makes application handles explicit,
and the official C# SDK now recommends stateless HTTP by default. This creates
a deployment opportunity only if Scout can preserve browser identity and task
recovery state without relying on an MCP transport session. Its current
prototype carries an opaque browser-session handle in `_meta`. Chrome-backed
integration verifies owner and session isolation, durable task/artifact
scoping, idle cleanup, and graceful single-process restart. Official TypeScript
and Python SDK flows navigate and read through the handle, cancel an in-flight
wait, then read again successfully. Multi-replica load balancing, OAuth, and
additional client SDK compatibility remain untested. Keep this as an
integration gate beneath the host-owned outcome thesis, not a standalone
positioning claim. The [latest protocol
research](competitive-opportunities.md#competitive-refresh-stateless-mcp-makes-application-state-explicit-10-november-2026)
records the source status and proposed comparison.

First-success diagnosis is also a live competitive surface. Agent-browser
0.42.0 already offers a broad JSON doctor with a headless launch probe and
explicit repair mode; its older Windows hang and missing-browser reports were
closed after fixes or improved error handling. Stagehand issue #3359 remains
open, with a mock-reproduced false-green doctor report but no physical Chrome
reproduction in the maintainer follow-up. Scout's doctor already separates
configuration, live attach, and an optional application-marker probe. The next
product question is whether those checks explain the actual first command
failure faster: pair every health verdict with a bounded browser read and
measure diagnosis accuracy, phase timings, cleanup, and state changes. Keep
this as a product-quality bet until a matched clean-environment study shows an
adoption advantage. See the [first-success audit](first-success-audit.md).

## Product wedge

The wedge is not another broad action catalog. It is a production-ready path
from first successful task through ambiguous failure and host reconciliation:

1. Configure a browser owner, session, target scope, policy, deadline, and
   durable task store.
2. Execute a deterministic task and receive a task ID plus verified outcome.
3. Interrupt after a fixture mutation commits but before the caller gets the
   result.
4. Restart the worker, inspect the retained redacted task state, and query the
   authoritative fixture ledger.
5. Record the host's conclusion without replaying under the same task ID.

The repository's durable recovery example demonstrates this path against a
local fixture. A fresh Go 1.26.0 run on 10 November 2026 passed four boundaries: a
committed mutation interrupted before acknowledgment, interruption after final
page verification but before result persistence, completed-result reuse from a
new Scout process, and browser replacement after a committed mutation. The two
interrupted tasks and the browser-replacement task blocked same-ID replay and
were reconciled from the fixture ledger; the completed task reused its stored
result without a second effect (raw run (local audit artifact)).
This is one local fixture run, not production reliability or a competitive
comparison. Its independent Node receiver also lacks authenticated frame
identity and correctly rejects full-context matching. The standalone receipt
verifier checks envelope consistency but does not authenticate the producer,
browser, or application outcome.

The adoption fixture has paired live Scout and Stagehand Go facilitator
rehearsals. The updated three-stage runs add a normal approval control before
the faulted approval; each adapter passed all 22 checks, with one effect in
each approval task. Both faulted runs recover task state in a fresh process,
preserve `unknown`, inspect the ledger, record its result with provenance, and
avoid retry. Scout uses its native bbolt task store; Stagehand uses a
host-authored journal. The three-stage summary (local audit artifact)
and raw runs (local audit artifact) document this
fixture result. The earlier paired report (local audit artifact)
predates the control and should be read as historical fault-path evidence.
These adapters are facilitator code, not participant implementations, so they
do not measure relative host glue, usability, or customer demand.

The entry example runs one verified task, repeats the identical task ID and
plan, and confirms that Scout returns the saved result without a second commit
request (idempotent-retry run (local audit artifact)).
It now accepts `-task-store=/path/to/tasks.db` for retained storage; when that
mode is used, it generates a fresh task ID for each new fixture instance. Two
live runs against the same store each committed once, and a separate stateful
CLI process read the first completed result after both example processes had
exited (retained-store reopen report (local audit artifact)).
This proves local bbolt reopen and readback, not recovery from a mutation-time
process crash, production database behavior, or clean-install adoption. Those
failure boundaries remain in the separate [durable recovery example](../examples/durable_task_recovery/main.go).

## Adoption risks and constraints

- The initial target segment may be too narrow or may prefer Playwright's mature
  cross-language ecosystem, Stagehand's agent-oriented workflow, or
  agent-browser's terminal-first session model.
- Durable task storage and owner/session scoping require explicit deployment
  choices; the default remains in-memory in several workflows.
- Browser page verification cannot substitute for a business ledger or
  authoritative API.
- Browser continuity features are converging across competitors. Basic
  persistence, session selection, diagnostics, and tool breadth are not a moat.
- The current evidence is repository-controlled. It does not prove that
  independent teams can integrate Scout with less code or recover more
  correctly in production.

## Decision gates

Use the [adoption study](adoption-study.md) and its field kit before changing
the external lead position. The session script and de-identified record template
are ready; paired metric rows can be stored in
[adoption-study-metrics.csv](adoption-study-metrics.csv). The study should determine:

- whether recent, costly mutation ambiguity appears without prompting;
- whether users can complete the same verified task with less integration
  effort than Stagehand Go;
- whether the recovery flow prevents duplicate effects while preserving
  unresolved outcomes as unknown;
- whether ownership and policy configuration fit existing service boundaries;
- which segment and alternative best describe the real buying decision.

Keep the current lead claim if the study shows demand for a composable Go engine
but not recovery. Reposition around the observed job if recovery pain is rare.
Fix correctness before onboarding if any trial duplicates a committed effect
or reports unknown work as complete.

## Evidence needed next

### Recommended sequence

1. **Close study readiness before recruitment.** Have a second person follow
   the neutral task card from clean participant checkouts. Observe the UI
   interaction, inspect the preserved source manifests and LOC counts, verify
   the nominal and faulted ledger results, and record timing and deviations.
   Fix any ambiguous instruction or grading gap before inviting participants.
2. **Run 5–8 incident interviews.** Recruit engineers who operated Go browser
   workers in the past year, including evaluators who rejected deployment.
   Capture the latest concrete incident, authoritative source, frequency,
   current workaround, and engineering/on-call cost. Do not pitch the thesis
   before hearing the incident.
3. **Run the counterbalanced implementation study.** Compare Scout with the
   pinned Stagehand Go v4 path on the same clean environment. Preserve all
   attempts, including timeouts and install failures. Report nominal control
   separately from faulted recovery, and count host-authored glue, operator
   actions, duplicate effects, false completion, unknown preservation, time,
   and preference.
4. **Apply the decision gates below before extending the benchmark.** If
   participants have no recent costly ambiguity and do not value the recovery
   workflow, narrow the product claim to the job they do report. If an arm
   duplicates a committed effect or reports unresolved work as complete, stop
   positioning work and fix correctness first. If recovery demand exists but
   the task is too difficult to complete, reduce integration effort before
   broadening features.
5. **Then run the separate CLI/MCP experiment.** Compare agent-browser saved
   flows with Scout on recurring task cost and post-commit worker loss. Keep
   this distinct from the Go SDK adoption study; report replay tier, write
   controls, outcome truth, operator steps, setup, and cost.
6. **Publish only after a current tracker recheck.** Refresh issue and PR
   states and pinned versions on the publication date. Publish protocol,
   versions, denominators, raw artifacts, and limitations with the scorecard.

### Decision gates for the first study

- **Continue the recovery thesis** if at least 3 of 5–8 participants can
  describe a recent, concrete ambiguous mutation with a meaningful cost, and
  at least 3 independently identify authoritative reconciliation or duplicate
  prevention as a reason to change their current workflow. Treat this as a
  directional discovery threshold, not a market-size estimate.
- **Prioritize the Go integration wedge** if at least 4 participants finish
  both deterministic arms and the nominal controls pass, so recovery results
  are interpretable. Compare completion time, assistance, and host glue per
  participant; do not declare a win based on preference alone.
- **Require zero safety-critical failures** before any comparative claim:
  zero duplicate committed effects, zero false completion after worker loss,
  and no replay before authoritative inspection in every completed faulted
  task. Any failure triggers investigation and another rehearsal/run before
  promoting the claim.
- **Do not claim lower effort or superiority from 5–8 participants.** Treat
  paired time, LOC, and preference as exploratory. A later, larger study is
  needed for comparative estimates; user interviews establish problem
  relevance, not product-market fit.

The current participant metrics file contains only its header. The second-person
rehearsal and interviews remain the immediate prerequisites; fixture runs and
facilitator adapters do not count toward these gates.

See [competitive opportunities](competitive-opportunities.md),
[adoption study](adoption-study.md), and the [product vision](product-vision.md)
for research sources, protocol, and implementation evidence.
