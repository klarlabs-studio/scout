# Scout benchmark

## Doctor and first-read readiness probe

Capture configuration-only, live-browser, healthy application-probe,
missing-marker, and bounded first-read states against a loopback fixture:

```sh
python3 examples/benchmark/run_doctor_readiness.py
```

The integration workflow verifies four doctor scenarios (including
configuration-only), both first-read
outcomes and timings, and five deterministic fault regressions for missing
browser, failed CDP attach, active-page timeout, timeout cleanup, and
application-probe tab cleanup. It checks the stable status, code, and
remediation contracts through the corresponding Go assertions. It separately
runs the Chrome-backed unresponsive-renderer and rejected-tab-close cases,
which have nonstandard test names and would be missed by the broad integration
selector.

The runner builds the current worktree, starts and stops its own local fixture,
and writes a report to a unique UTC-timestamped file under `reports/` by
default. Pass `--output /path/to/report.json` to choose a path explicitly.
The checked-in [11 November report](reports/scout-doctor-readiness-2026-11-11.json)
is one historical run.
It expects configuration-only status `warning` with exit 0, successful live
attach/probe checks, a missing probe marker to return `error` with exit 1, a
verified first read, a rejected first-read postcondition, and successful
browser-session cleanup after every live check. The report measures external
process wall time. Doctor JSON separately reports browser launch/attach,
browser status, bounded active-page read, application-probe (including
temporary-tab cleanup), and browser-session cleanup durations; first-read CLI
phases remain combined. Output payloads
containing dynamic targets are omitted from the checked-in report. This is local Scout
evidence, not a competitor comparison, clean-container onboarding study, or
repeated-run reliability estimate. It records the source revision and whether
the worktree was dirty; schema 4 reports also include fingerprints for Go
build inputs and tracked changes, the effective Go toolchain settings, and
presence-only host context signals for display, proxies, and CI. Environment
values such as proxy URLs and Go flags are not written to the report. The
temporary task-store path is redacted from the saved command.

### Unreachable CDP diagnostic probe

Run a seeded loopback CDP attach failure against Scout's live doctor and first
read while also capturing the pinned agent-browser doctor's ordinary local
health report:

```sh
python3 examples/benchmark/run_unreachable_cdp_comparison.py
```

The 12 November run is in
[`unreachable-cdp-0.42.0-20261112T002143Z`](results/unreachable-cdp-0.42.0-20261112T002143Z/summary.json).
On this macOS/Chrome workstation, Scout's doctor returned a typed
`SCOUT_DOCTOR_BROWSER_LIVE_ERROR`, and the first read surfaced connection
refusal; the configured task-store file was not created. Agent-browser 0.42.0
doctor passed its local launch test, and its isolated home gained one small
`.agent-browser/doctor-*.target` diagnostic marker (82 bytes in this run).
Its doctor does not accept the seeded endpoint, so this is not a matched
failure-verdict comparison. It reports scenario injection separately and must
not be used to infer that agent-browser missed this failure. The Stagehand
merged-source closed-CDP check is documented in
[`competitive-opportunities.md`](../../docs/competitive-opportunities.md),
but the Stagehand CLI is not installed here and no released Stagehand package
was tested.

The paired positive/negative first-read harness can be rerun with:

```sh
python3 examples/benchmark/run_first_success_comparison.py
```

The refreshed 12 November report is
[`first-success-agent-browser-0.42.0-20261112T002232Z`](results/first-success-agent-browser-0.42.0-20261112T002232Z/summary.json).
All fourteen runner predicates passed. Scout returned a nonzero status for the
missing marker; agent-browser's `get text body` returned success and exposed
its text for separate harness grading. The commands and assertion contracts
differ, so these records do not establish which product is faster or more
reliable.

## Lifecycle scorecard for competitive runs

Use [`lifecycle-scorecard-template.json`](lifecycle-scorecard-template.json) to
record raw measurements for Scout and pinned competitor releases. Duplicate a
row per run and record the exact browser/tool versions in `environment` and
`product`/`version`. `target_state` is `live`, `discarded`, or `crashed`; run
the 0, 10, and 50 target-pressure cases for each state. Record both attach and
first independently verified read latency. For CDP disconnect rows, include
whether recovery succeeded and the operator/tool steps required.

Grade the rows with:

```sh
python3 examples/benchmark/grade_lifecycle_scorecard.py \
  examples/benchmark/lifecycle-scorecard-template.json \
  --output /tmp/lifecycle-scorecard.json
```

The template is intentionally incomplete and the grader exits `3` until all
nine target-pressure scenarios have at least 30 runs each, all four readiness
failure states have at least 30 classified runs, and CDP disconnect recovery
has at least 30 measured runs for every product/version. Missing latency
remains null; p50/p95 use only observed runs. Record the relevant check results
in `readiness_checks`: browser launch/reachability failures should fail
`browser_live`; an attached but unresponsive page should not count as page
responsive when that check is unperformed; and an explicit failed application
probe should fail `app_probe`. This report summarizes submitted
evidence only; it does not reproduce a competitor issue or verify its reported
behavior.

## Independent recovery-receipt audit

Audit an exported `scout.recovery-receipt/v1` JSON file with the standalone
Python checker:

```sh
python3 examples/benchmark/verify_recovery_receipt.py /path/to/receipt.json
```

The checker uses only Python's standard library. It rejects duplicate/unknown
fields, contradictory operation states, replay authorization, malformed
task-local operation IDs, non-canonical or inconsistent origin-policy digests,
and invalid evidence metadata. Its JSON result explicitly leaves producer
authentication, live browser binding, and application-outcome verification
false: those claims require independent trusted host or application evidence.
This is a structural/semantic audit, not a cryptographic signature verifier or
proof that a site committed a transaction.

The checked-in [`exported-recovery-receipt-v1.json`](receipts/exported-recovery-receipt-v1.json)
is a current-format fixture for exercising the structural checker; it is not a
live export or producer-authentication evidence.

To compare Go SDK, stateful CLI, and MCP receipt exports, capture equivalent
task records from each surface and list them in a manifest. Include interrupted,
completed, and non-first-step cases. Namespace labels record where each copy was
seeded and are reported explicitly; they do not make isolated stores shared.

```sh
go run ./examples/benchmark/recovery_receipt_parity \
  -manifest /tmp/recovery-receipt-parity.json
```

Each case has `name`, `scenario` (`interrupted`, `completed`, or
`non_first_step`), `receipts` paths for `go`, `cli`, and `mcp`, and `namespaces`
labels for the corresponding store/owner/session scope. The checker runs
Scout's strict Go decoder and the independent Python verifier on every file,
then reports validator outcomes, per-surface field parity, mismatched fields,
and namespace differences. It permits timestamps and JSON key order to differ.
Its process exit status is nonzero if a case or validator fails, or if any of
the three required scenario classes is absent. Every case must carry a policy
digest; the non-first-step case must also carry a non-empty frame ID. The
checker audits captured
exports; it does not seed records or authenticate producers, browser binding,
or application outcomes. Keep raw exports in a private temporary directory and
do not weaken MCP owner/session isolation to share one physical record.
Use [`recovery_receipt_parity/manifest.example.json`](recovery_receipt_parity/manifest.example.json)
as the capture-path and namespace template.

To grade a receiver's independently collected browser-context observation
against the host's expected owner, generation, target, frame, and policy, use
the separate handoff checker:

```sh
python3 examples/benchmark/verify_handoff_observation.py \
  examples/benchmark/receipts/matching-handoff-observation-v1.json \
  examples/benchmark/receipts/expected-handoff-binding-v1.json \
  examples/benchmark/receipts/authoritative-ledger-complete-v1.json \
  checkout-42
```

It exits `0` for a structurally valid full context match, `3` for a valid but
mismatched or unknown frame context, and `1` for malformed input. Its output
always leaves producer authentication and application-outcome observation
false unless a separate oracle record is supplied. That record must identify
the expected task, oracle source/time, expected and observed effect counts, and
an explicit authoritative flag. Excess effects are reported as duplicates;
short counts remain incomplete/unresolved. The script cannot authenticate the
oracle or observation source, and never authorizes replay.

For a real report emitted by the durable recovery example, grade the
receiver-context and per-task fixture-ledger fields together while keeping
those evidence domains separate:

```sh
go run ./examples/durable_task_recovery \
  > /tmp/scout-durable-recovery.json
python3 examples/benchmark/grade_recovery_run.py \
  /tmp/scout-durable-recovery.json submit-once-001
```

The default expected mutation count is one; pass an explicit fourth argument
for another fixture contract. The report grader rejects duplicate task rows,
reports unavailable context when the full binding did not match, and never
marks replay safe. The fixture is local demonstration evidence, and the
report's limitations remain part of its output.
The latest joined grade is preserved at
[`reports/durable-recovery-joined-grade-2026-11-06.json`](reports/durable-recovery-joined-grade-2026-11-06.json): the local ledger reports one accepted effect while the independent Node receiver lacks frame identity, so application completion is known in the fixture but browser context is unavailable for page-based recovery.

To verify that a configured proxy sees loopback destinations from Scout-launched
Chrome, run the focused deny-proxy scenario. It checks `127.0.0.1`, `localhost`,
`::1`, and an HTTPS `CONNECT`; each origin must record zero requests while the
proxy observes the request and returns 403:

```sh
go run ./examples/benchmark/loopback_proxy
```

The runner uses a minimal local HTTP proxy and does not replace the mitmproxy
integration coverage for service-worker and WebSocket paths.

The versioned fixtures in `fixtures/v28.json` exercise deterministic browser
contracts locally. The full runner emits one JSON report and exits nonzero if
the fixture version is wrong or any scenario fails:

```sh
go run ./examples/benchmark > /tmp/scout-benchmark.json
```

Some workflows run against a caller-provided fixture server or remote browser.
Choose a supported focus with `SCOUT_BENCHMARK_FOCUS`; repeated workflows also
accept `SCOUT_BENCHMARK_REPEATS` (1–100):

```sh
SCOUT_BENCHMARK_FOCUS=cli-session-parity \
SCOUT_BENCHMARK_REPEATS=5 \
go run ./examples/benchmark > /tmp/scout-cli-parity.json
```

To verify owner-tool routing while a session switch changes the active pointer,
run the deterministic race matrix. It overlaps `clone_session`, `status`, and
`reset` with a delayed page operation, switches from the captured `left` session
to `right`, and checks the clone source, returned status target, reset outcome,
and preservation of the right target. Repeat in fresh MCP processes with
`SCOUT_BENCHMARK_REPEATS` (1–100); the JSON summary reports total passes and
per-run evidence:

```sh
SCOUT_BENCHMARK_FOCUS=mcp-session-owner-race \
SCOUT_BENCHMARK_REPEATS=10 \
go run ./examples/benchmark
```

The 10 November 2026 current-worktree run passed 10/10, including all three
owner operations in every run. This is a local stdio race fixture; authenticated
multi-client HTTP routing and external tab churn remain separate coverage.

The authenticated-principal isolation scenario also sends the same explicit
`_meta.go.klarlabs.de/scout/session=default` selector from two HTTP principals
to one shared remote CDP endpoint. It checks distinct target IDs and storage,
then retains the scenario's reconnect, foreign-handle denial, reset, and
transport-delete predicates. Repeat it to gather selector-specific evidence:

```sh
SCOUT_BENCHMARK_FOCUS=mcp-http-principal-isolation \
SCOUT_BENCHMARK_REPEATS=5 \
go run ./examples/benchmark > /tmp/scout-principal-selector.json
```

This exercises Scout's MCP metadata selector. It is not a wire-compatible
reproduction of agent-browser's tool-level `session` argument; compare those
flows only through an adapter that preserves the same caller-supplied label.

For a focused three-way verified-action outcome comparison, the pinned
agent-browser and Playwright MCP wrappers accept `BENCHMARK_SCENARIO=verified-action`.
They run only that scenario for the requested repetitions (minimum 10), start
the same local fixture server, and save a Scout Go API report alongside each
adapter report:

```sh
BENCHMARK_SCENARIO=verified-action BENCHMARK_REPETITIONS=10 \
  AGENT_BROWSER_VERSION=0.41.2 ./examples/benchmark/run_agent_browser.sh
BENCHMARK_SCENARIO=verified-action BENCHMARK_REPETITIONS=10 \
  PLAYWRIGHT_MCP_VERSION=0.0.84 ./examples/benchmark/run_playwright_mcp.sh
go run ./examples/benchmark/report \
  examples/benchmark/results/<agent-browser-run>/agent-browser-verified-action-comparison.json \
  examples/benchmark/results/<playwright-mcp-run>/playwright-mcp-comparison.json \
  examples/benchmark/results/<scout-run>/scout-go-api-verified-action-comparison.json \
  > /tmp/verified-action-comparison-matrix.json
```

To inspect whether pinned MCP servers expose native task identity or recovery
tools, query their complete catalogs without opening a browser page:

```sh
node examples/benchmark/recovery_capability_probe.mjs 0.41.2 0.0.84 \
  examples/benchmark/reports/native-task-recovery-capability-2026-11-06.json
```

The report records initialize capabilities, tool counts, task-ID fields, and
task/recovery tool names. Catalog absence is version-specific capability
evidence; it is not a runtime reliability result.

To compare target/frame continuity after closing an unrelated tab, run the
version-pinned `frame-close` scenario. It captures an iframe action reference,
opens and closes a separate tab, then clicks the captured reference and grades
the frame result on the original page. Scout and agent-browser are run in
separate fresh sessions; the Playwright MCP adapter uses an isolated browser
and reports the target evidence it can expose. This reproduces agent-browser's
reported frame-scope case without treating one project's issue as a general
failure claim:

```sh
BENCHMARK_SCENARIO=frame-close BENCHMARK_REPETITIONS=10 \
  AGENT_BROWSER_VERSION=0.41.2 ./examples/benchmark/run_agent_browser.sh
BENCHMARK_SCENARIO=frame-close BENCHMARK_REPETITIONS=10 \
  PLAYWRIGHT_MCP_VERSION=0.0.84 ./examples/benchmark/run_playwright_mcp.sh
```

For a focused background-tab target-selection comparison, use the same wrappers
with `BENCHMARK_SCENARIO=background-tab`. This runs ten repetitions by default,
uses a unique task URL per repetition, preserves a foreground guard tab, and
checks the task postcondition plus the strongest target-selection evidence each
interface exposes. Scout and agent-browser record stable target IDs; Playwright
MCP's tab interface is graded by selected unique URL/title and page outcome.
Visibility events are diagnostic because headless Chrome may not emit a
consistent hidden-to-visible transition. Timing includes different interface
overhead and is not a browser-engine speed comparison.

The 6 November 2026 matched run passed 10/10 for all three tools on HeadlessChrome
157. See [`the raw reports and repetitions`](results/20261106T140200Z-background-tab-focus-matched/)
for exact predicates, timing scopes, target evidence, and transcripts.

```sh
BENCHMARK_SCENARIO=background-tab BENCHMARK_REPETITIONS=10 \
  AGENT_BROWSER_VERSION=0.41.2 ./examples/benchmark/run_agent_browser.sh
BENCHMARK_SCENARIO=background-tab BENCHMARK_REPETITIONS=10 \
  PLAYWRIGHT_MCP_VERSION=0.0.84 ./examples/benchmark/run_playwright_mcp.sh
```

Each wrapper now writes to a unique UTC-stamped directory under
`examples/benchmark/results/`, including the Scout report, peer transcript,
raw command output, and summary. The wrapper prints the directory path at the
end. Existing runs are preserved. Use the priority order and evidence gates in
[`OPPORTUNITY_MATRIX.md`](OPPORTUNITY_MATRIX.md) to choose the next matched
experiment; the matrix explicitly separates buyer value, current evidence,
and requirements for a defensible claim.

The runtime conformance protocol is [`runtime-conformance-v1.json`](runtime-conformance-v1.json).
It defines independent application oracles, required identity metadata, six
fault scenarios, and reporting rules. Its status is protocol-only: existing
reports cover selected scenarios, and no aggregate conformance result or
release-wide advantage is claimed. Record missing telemetry as unavailable,
and do not combine outcomes, timing, output size, and resource use into one
score.

## Competitive trust-boundary probes

Run the wrapped inline-link geometry fixture against local Chrome:

```sh
go run ./examples/benchmark/wrapped_link
```

It verifies the anchor wraps to multiple rendered fragments, checks that the
old union-box midpoint hits a paragraph, then clicks through Scout and checks
the page-state postcondition. A captured run is in
[`reports/wrapped-link-click-2026-11-06.json`](reports/wrapped-link-click-2026-11-06.json).
This is a local single-run probe, not a matched competitor comparison.

Check global MCP allowed-origin configuration in fresh CLI processes:

```sh
python3 examples/benchmark/run_origin_policy_probe.py
```

The probe checks unset and valid values plus blank, whitespace, and comma-only
values against both `doctor` and MCP startup where applicable. It retains raw
configuration, stdout/stderr, exact UTF-8 byte counts, and separate predicates.
Pass `--output reports/my-run.json` to keep a new capture. The 12 November
current-worktree capture is [`reports/origin-policy-empty-config-2026-11-12.json`](reports/origin-policy-empty-config-2026-11-12.json); the 6 November report is historical.
Passing policy checks can coexist with unrelated `doctor` readiness warnings.

Run the matched source-effect probe against Scout, agent-browser 0.42.0, and
Playwright MCP 0.0.85 with local Chrome:

```sh
node examples/benchmark/run_origin_policy_comparison.mjs 5
```

The fixture loads a page from `localhost` that attempts an ordinary `fetch()`
to a second origin at `127.0.0.1`. The second listener counts requests
independently of browser output. Scout uses its session `--allowed-origins`,
agent-browser uses `AGENT_BROWSER_ALLOWED_DOMAINS`, and Playwright MCP uses
`--blocked-origins`. Their native policy scopes differ, so compare only the
shared request-count predicate. Each repetition's stdout/stderr or MCP
transcript and the aggregate report are saved under `reports/`. Redirects,
service workers, WebSockets, EventSource, WebRTC, DNS rebinding, and remote-host
egress are outside this fixture. The 12 November repeated capture includes five restricted runs and one
unrestricted positive control per product: each unrestricted browser produced
one counted request, and each restricted browser produced zero in all five
runs. See [`reports/origin-policy-matched-20261112T062042Z.json`](reports/origin-policy-matched-20261112T062042Z.json) and the individual raw captures in its sibling directory.

Reproduce the cross-origin OOPIF policy shape from agent-browser issue #2574
against Scout and pinned agent-browser 0.42.0:

```sh
node examples/benchmark/run_oopif_policy_rejection.mjs 3
```

The fixture serves a `localhost` parent containing a `127.0.0.1` child iframe,
counts child document requests independently, bounds each CLI run to 40 seconds,
and records raw output under `reports/`. A timeout is an observation on this
machine, not proof of a general product defect; the reported Stripe Elements
case is not covered.

To probe agent-browser's saved cookie/localStorage recovery across a hard
daemon kill while a server-side mutation is committed but its response remains
open, run:

```sh
AGENT_BROWSER_VERSION=0.41.2 ./examples/benchmark/run_agent_browser_process_recovery.sh
```

The runner uses a unique agent-browser namespace, explicitly enables
`--restore --restore-save always`, kills that run's daemon after the fixture
reports the committed mutation, then opens a fresh CLI process and records
session restore diagnostics and a localStorage marker. The default
`browser` fault boundary kills that namespace's Chrome child processes before
killing the daemon; set `BENCHMARK_FAULT_BOUNDARY=daemon` to isolate daemon
loss. Both reports keep marker restoration separate from the authoritative
mutation count. The one-run probe never replays the mutation. Matrix runs
deliberately repeat the harmless mutation after recording recovery to measure
blind-replay effects. The marker indicates only whether browser state returned;
it does not classify the pending operation or establish that agent-browser
lacks application-level idempotency when a host supplies it. Keep older
single-run results without explicit restore flags separate. Use a free
`BENCHMARK_PORT` if 41739 is occupied.

To probe agent-browser v0.42.0's experimental saved-flow write semantics
against Scout's durable host reconciliation example, run:

```sh
python3 examples/benchmark/run_agent_browser_saved_flow_recovery.py --version 0.42.0
```

The runner records a parameterized flow from the fixture page, explicitly
selecting `#task-frame` before capture. Its faulted
operation uses the saved flow's direct HTTP tier; Scout uses a browser click.
Both dispatch the same non-idempotent mutation route, and the server commits
one effect then holds the response. The flow returns `outcome_unknown` with one
request and one effect; the Scout worker is killed at the same commit boundary,
a fresh worker's same-ID retry is blocked, and the host records
`verified_complete` from the shared ledger. The runner defaults to ten pairs;
use `--repetitions 30` for the expanded run. The initial 10-pair summary is
[`agent-browser-saved-flow-recovery-2026-11-10.json`](reports/agent-browser-saved-flow-recovery-2026-11-10.json),
and the expanded 30-pair summary is
[`agent-browser-saved-flow-recovery-30x-2026-11-10.json`](reports/agent-browser-saved-flow-recovery-30x-2026-11-10.json).
Both arms passed 30/30 in the expanded run (100%; Wilson 95% interval
88.6–100%). This validates the local fault protocol only; it does not estimate
customer reliability, integration cost, or latency. The saved flow used its
HTTP tier while Scout used a browser click, so the repeated outcome is fixture
parity across different dispatch paths. Saved flows remain experimental.
The 12 November 2026 rerun passed 10/10 for each arm on macOS 27.0.1 arm64
with agent-browser 0.42.0 and Go 1.27.4: each ledger showed one committed
effect from one request; agent-browser reported `outcome_unknown`, while
Scout blocked same-ID retry and reconciled the host's `verified_complete`
finding. Raw logs and the full summary are in
[`agent-browser-0.42.0-saved-flow-recovery-20261112T024500Z`](results/agent-browser-0.42.0-saved-flow-recovery-20261112T024500Z/summary.json).
Choose a free `--port` if 41742 is occupied.

The Go SDK, stateful CLI, and MCP stdio surfaces can be checked against the
same process-loss fixture with:

```sh
GOTOOLCHAIN=go1.26.1 python3 examples/benchmark/run_saved_flow_interfaces.py --repetitions 10
```

This probe keeps Chrome alive over CDP, kills each worker after one committed
mutation, restarts it on the same durable task store, checks that an identical
task ID and plan remain blocked, and resolves from the fixture ledger. It also
asserts all three interfaces record identical origin policy and task fingerprints.
The replacement process now exports the interrupted action receipt before
reconciliation and checks task identity, unknown outcome, possible dispatch,
and replay prohibition. The runner now gives each interface the same task ID
inside its own isolated store, so portable task and operation IDs can be
compared directly. It invokes the receipt parity checker on the live
interrupted receipt and both completed-operation receipts from all three
interfaces. The comparison normalizes absolute evidence/deadline timestamps
and lists those fields in the report; it still compares browser, target,
policy, task, operation outcome, and replay state. The CLI exposes
`session --allowed-origins=LIST` for this purpose.

The runner alternates CLI/MCP order and starts a fresh Chrome instance per pair
after an earlier single-browser matrix stalled during navigation. The
11 November 10-pair CLI/MCP run is in
[`scout-cli-mcp-process-recovery-10x-2026-11-11.json`](reports/scout-cli-mcp-process-recovery-10x-2026-11-11.json):
both surfaces passed 10/10 fixture trials, and all ten pairs matched policy and
task fingerprints. Each success rate has a Wilson 95% interval of 72.2–100%.
This measures local conformance only; it is not a customer reliability estimate
or competitive benchmark. Each surface uses a separate task store and task ID.

A three-interface extension completes live interrupted and two-action tasks
through the Go SDK, CLI, and MCP. Its parity checker audits the interrupted
receipt and both completed operations for each repetition. The 11 November
capture used main-page tasks. The 12 November capture runs the same ten-trial
protocol with task controls inside an iframe and verifies all 30 receipts are
frame-bound; the comparer checks frame presence and normalizes per-session
CDP frame IDs while keeping the other receipt fields exact. All 90 archived
receipts passed both validators, and each interface passed 10/10 trials
(Wilson 95% interval 72.2–100%). Keep trial counts and namespaces visible:
this is deterministic local fixture evidence, not a customer reliability
estimate or competitive benchmark. The capture is in
[`scout-three-surface-live-receipt-parity-2026-11-12`](reports/scout-three-surface-live-receipt-parity-2026-11-12/).

The equivalent Playwright MCP probe runs one full browser and MCP process loss
against the same fixture:

```sh
PLAYWRIGHT_MCP_VERSION=0.0.84 ./examples/benchmark/run_playwright_process_recovery.sh
```

Both tools have one current full-process probe in `results/`. Those reports
show fixture mutation count and browser-state restoration; neither adapter
records Scout's portable committed-action recovery envelope. They are
single-run evidence, not comparative rates. Scout's durable task recovery
example exercises its own process-kill boundaries and host reconciliation,
documented in `docs/product-vision.md`.

Repeat Scout's browser-replacement boundary with fresh fixture processes and
aggregate the replay-blocking and authoritative-reconciliation checks:

```sh
python3 examples/benchmark/run_scout_browser_recovery_matrix.py --repetitions 30
```

The runner builds the fixture once, starts a fresh browser/task workflow per
trial, preserves each raw log, and writes a JSON summary with a Wilson 95%
interval. The 6 November 2026 current-worktree rerun passed 30/30 (100%, 95%
interval 88.6–100%) on commit `adcf15d`; the captured report marks the worktree
dirty.
This is Scout-only fixture evidence for browser replacement after committed
mutation; it does not compare peer recovery, application idempotency, browser
state restoration, or latency. See
[`summary.json`](results/scout-browser-recovery-matrix-20261106T144416Z/summary.json)
for the aggregate and per-run evidence.

For repeated independent trials with an aggregate rate and Wilson 95% intervals
for mutation preservation, state restoration, and reopening, run:

```sh
BENCHMARK_REPETITIONS=30 \
  AGENT_BROWSER_VERSION=0.41.2 \
  PLAYWRIGHT_MCP_VERSION=0.0.84 \
  ./examples/benchmark/run_process_recovery_matrix.sh
```

The matrix requires at least 30 repetitions per tool, checks the requested
tool versions and fixture/protocol pins, and rejects missing outcome fields
before calculating rates. Failed reopen and replay outcomes remain in the
denominator when their observations are present. Use `--minimum-repetitions`
on the aggregator only when producing an explicitly exploratory summary. Each
repetition starts a new authoritative fixture and browser process for each
tool. The agent-browser arm kills its daemon and Chrome; the Playwright MCP arm
kills its server process and Chrome. After recording post-restart state, each
runner deliberately repeats the
same harmless fixture click and records whether it duplicates the side effect.
This measures blind UI replay only; native task-ID guards and host-supplied
idempotency are not tested. The aggregate keeps the raw run directories.

The retained 30-run process-loss matrix also contains a no-replay observation:
the authoritative count is read after restart and before the deliberate second
click. The normalized comparison with Scout's separate 30-run task-ID guard and
host-reconciliation fixture is documented in
[`matched-recovery-no-replay-2026-11-06.json`](reports/matched-recovery-no-replay-2026-11-06.json).
These are distinct protocols, so their 30/30 rates are reported as separate
predicates rather than a head-to-head success-rate claim.

An exploratory application-idempotency fixture repeats the same UI submit with
a stable page-provided key against agent-browser and Playwright MCP. The runner
restarts the fixture between adapters so each starts with an empty key store;
otherwise the second adapter would correctly deduplicate the first adapter's
key and invalidate the per-tool first-call measurement:

```sh
AGENT_BROWSER_VERSION=0.41.2 PLAYWRIGHT_MCP_VERSION=0.0.84 \
  ./examples/benchmark/run_idempotency_comparison.sh
```

This measures application-side deduplication, not browser-tool recovery. A
corrected 6 November 2026 run passed for both agent-browser 0.41.2 and
Playwright MCP 0.0.84: each reached its isolated fixture, committed one effect,
then suppressed the repeated effect for the same key. This is parity evidence
for application-provided idempotency, not native task replay protection or
comparative recovery. The raw reports and MCP transcript are preserved under
[`results/20261106T171754Z-application-idempotency-41401`](results/20261106T171754Z-application-idempotency-41401).

This focus also checks that Scout's Go, one-shot CLI, and MCP results carry the
same receipt verification state. Each wrapper writes a Scout-only receipt
sample; after both competitor reports exist, build the compact scorecard with
`jq -n` inputs from the three comparison JSON files (see
`results/verified-action-receipt-scorecard-2026-11-06.json` for the captured
shape). Competitor rows report the externally observed page predicate because
these adapters do not emit Scout's normalized receipt. Treat result parity as
the comparable dimension; timing and byte totals have different scopes.

The shared fixture URL is `http://127.0.0.1:41731/action`; use another
`BENCHMARK_PORT` consistently if that port is occupied. The reporter can
normalize the three saved JSON reports. Outcome parity is separate from
latency and output size, which remain incomparable unless their measurement
boundaries match. The focused Scout path accepts `SCOUT_BENCHMARK_REPEATS` and
defaults to ten repetitions.

For delayed-mutation caller-timeout comparison, run both adapters with
`BENCHMARK_SCENARIO=late-recovery` and at least ten repetitions. Choose a free
port for each wrapper. They save a normalized Scout report and peer report in
the run directory; pass `scout-late-action-cancellation-comparison.json`,
`agent-browser-late-recovery-comparison.json`, and `playwright-mcp-comparison.json`
to the aggregator to produce a matrix. The result predicate is shared, while
timing and output remain incomparable because the measured operations and serialization
boundaries differ. See `COMPARISON.md` for the 6 November 2026 results and
their limits.

To repeat the same late-mutation contract through Scout's real MCP stdio
server, first start the shared fixture server, then run:

```sh
SCOUT_BENCHMARK_FOCUS=mcp-late-action-cancellation \
SCOUT_BENCHMARK_BASE_URL=http://127.0.0.1:41731 \
SCOUT_BENCHMARK_REPEATS=10 \
SCOUT_BENCHMARK_REPETITIONS_FILE=examples/benchmark/results/scout-mcp-late-action-cancellation-10x.json \
go run ./examples/benchmark
```

The aggregate includes caller return and full reconciliation medians; the
optional mode-0600 JSON file retains per-run evidence. The comparison report
keeps timing scopes separate from the caller-side peer adapter workflows.

To compare repetitions, save each run as a separate JSON file and aggregate
them:

```sh
go run ./examples/benchmark/report /tmp/run-01.json /tmp/run-02.json
```

The `mcp-http-idle-inflight` focus issues `list_sessions` concurrently with a
long `wait_for`. It requires the listing to return before the idle timeout,
report the concurrent call in `in_flight_calls`, set `status_deferred`, and
still let idle cleanup expire the session after work finishes. Five fresh
server/browser repetitions on 6 November 2026 passed; the captured run is
[`scout-mcp-http-inflight-observability-5x-2026-11-06.json`](results/scout-mcp-http-inflight-observability-5x-2026-11-06.json).
This validates Scout's local HTTP contract, not cleanup after abrupt client
disconnect or remote-provider resource release.

The report includes suite completion rate, scenario pass rate and coverage,
false-success count, and min/median/nearest-rank p95/max for duration, output
bytes, and action calls. Suite reports with different benchmark versions or
duplicate scenario IDs are rejected. Comparison reports are normalized
separately into `comparison_rows`; the aggregator retains each tool/browser
version, repetition count, pass rate, timing scope, output definition, and
per-repetition call counts. It reports result, timing, and output comparability
separately. Result comparisons require a shared benchmark version, comparison
protocol version, fixture version, browser version, fixture URL, and exact
verification predicate; timing and output
comparisons also require matching measurement boundaries. Missing metadata
makes that comparison dimension explicitly non-comparable. Nested
per-scenario summaries need scenario-specific predicates to qualify for direct
comparison. Missing durations are excluded from suite statistics; missing
scenarios remain visible through `report_coverage`.

Focused reports are contract probes, not full-suite results. Cross-tool claims
also require matched versions, identical fixture/task conditions, repeated
runs, and retained raw reports. Historical Scout and competitor reports are
in `results/`; check `COMPARISON.md` for each report's scope and limitations.

## MCP output limits and artifact retrieval

Run the live smoke workflow for bounded extraction, a large private page
snapshot, snapshot range/search retrieval, and paged network response bodies:

```sh
./examples/benchmark/output_limit_smoke/run.sh
```

The runner builds standalone binaries, starts a local MCP server and fixture,
checks tool result sizes against the 256 KiB serialized response ceiling,
verifies snapshot file mode and cleanup, and exits nonzero on a contract
mismatch. It samples the Scout MCP server process RSS every 50 ms and writes a
machine-readable report to
`results/mcp-output-limit-memory.json` (override with
`OUTPUT_LIMIT_REPORT`). The report excludes Chrome, the fixture, and the smoke
client; short-lived RSS peaks between samples may be missed. This is a single
workload measurement, not a general memory ceiling. It also writes the
wire-contract predicates, raw smoke line, cap, and limitations to
`reports/scout-mcp-output-budget-live-YYYY-MM-DD.json` (override with
`OUTPUT_LIMIT_CONTRACT_REPORT`). That report is evidence for this Scout run,
not a cross-tool comparison. With
`SCOUT_BENCHMARK_OUTPUT_OVERFLOW=1` in the child server environment, the smoke
also invokes an opt-in benchmark-only tool that produces an oversized result.
It verifies the final middleware returns `isError`, the `SCOUT_OUTPUT_LIMIT`
and `output_omitted` metadata, and a replacement result below the 256 KiB
ceiling. The tool is absent from normal server profiles. The recorded run is a
live protocol-client call through stdio MCP; it checks the decoded MCP result
size rather than measuring raw JSON-RPC frame bytes.

### Matched MCP memory probe

The delayed-element memory probe compares a standalone Scout MCP server with
Playwright MCP 0.0.84 on the same 1.8 s fixture, `Ready` predicate, headless
Chrome 157, and 5 s idle timeout. The Scout probe uses stdio MCP; Playwright
uses Streamable HTTP, so these are matched task observations, not fully matched
transport costs. The runner samples process RSS every 50 ms and reports Scout
server, Playwright MCP server, and isolated Playwright Chrome categories
separately. Reproduce five runs with:

```sh
go build -o /tmp/scout-mcp ./cmd/scout
go build -o /tmp/scout-memory-probe ./examples/benchmark/memory_scout_probe
for i in 1 2 3 4 5; do
  SCOUT_MCP_BINARY=/tmp/scout-mcp RSS_SAMPLE_INTERVAL_MS=50 \
  RSS_INCLUDE_ROOT=0 RSS_WORKLOAD_PATTERN='scout-mcp mcp serve' \
  RSS_EXCLUDE_PATTERN='zsh -c|process_rss_sampler' \
    node examples/benchmark/process_rss_sampler.mjs \
      "examples/benchmark/results/scout-mcp-memory-5x-run-0${i}.json" \
      -- /tmp/scout-memory-probe
  RSS_SAMPLE_INTERVAL_MS=50 RSS_INCLUDE_ROOT=0 \
  RSS_WORKLOAD_PATTERN='node .*/playwright-mcp |playwright_chromiumdev_profile-' \
    node examples/benchmark/process_rss_sampler.mjs \
      "examples/benchmark/results/playwright-mcp-memory-5x-run-0${i}.json" \
      -- node examples/benchmark/compare_playwright_inflight_idle.mjs \
        http://127.0.0.1:41731 0.0.84 1 5000 1800
done
```

The raw runs and summary are in `results/matched-mcp-memory-5x-2026-11-06.json`.
RSS sums per-process resident sizes and may double-count shared pages; 50 ms
sampling can miss shorter peaks. The server categories exclude launchers and
browser; Chrome is reported separately. Scout's stdio and Playwright's HTTP
transport differ, so use this as an operational-cost signal, not a claim of
end-to-end memory superiority. The 1 s Playwright case is intentionally
excluded from the cost comparison because it fails when the idle timer closes
the page before the fixture completes; see `COMPARISON.md` for that separate
reliability result.

The Streamable HTTP lifecycle probes can be run independently without an
external fixture server. `mcp-http-idle-stream` holds an SSE stream open while
one client's browser idles and a second client's browser stays active;
`mcp-http-idle-inflight` keeps an SSE stream open during a `wait_for` longer
than the configured idle timeout, then checks that normal cleanup resumes
after the call returns:

```sh
SCOUT_BENCHMARK_FOCUS=mcp-http-idle-stream go run ./examples/benchmark
SCOUT_BENCHMARK_FOCUS=mcp-http-idle-inflight go run ./examples/benchmark
SCOUT_BENCHMARK_FOCUS=mcp-http-remote-idle SCOUT_BENCHMARK_REPEATS=5 go run ./examples/benchmark
```

The remote-idle focus starts its own local fixture, caller-owned Chrome, and
HTTP MCP server for each repetition. A separate MCP client observes session
expiry without refreshing the target client's idle timer; the report measures
time from the server-reported idle deadline to the first lazy-session
observation.

To repeat the matched in-flight idle-timeout probe against Playwright MCP
0.0.84, run Scout's 10 repetitions, start the fixture server on port 41743,
then run Playwright with the same 1 s idle limit and 1.8 s wait:

```sh
SCOUT_BENCHMARK_FOCUS=mcp-http-idle-inflight \
SCOUT_BENCHMARK_REPEATS=10 \
SCOUT_BENCHMARK_IDLE_TIMEOUT=1s \
SCOUT_BENCHMARK_WAIT_DELAY_MS=1800 \
  go run ./examples/benchmark \
  > examples/benchmark/results/scout-playwright-idle-inflight-10x-2026-11-06.json
go build -o /tmp/scout-benchmark-fixtureserver ./examples/benchmark/fixtureserver
/tmp/scout-benchmark-fixtureserver -listen 127.0.0.1:41743 \
  > /tmp/scout-fixture-41743.log 2>&1 &
fixture_pid=$!
node examples/benchmark/compare_playwright_inflight_idle.mjs \
  http://127.0.0.1:41743 0.0.84 10 1000 1800 \
  > examples/benchmark/results/playwright-mcp-idle-inflight-10x-2026-11-06.json
kill "$fixture_pid"
wait "$fixture_pid" 2>/dev/null || true
```

The server/browser launch is isolated per repetition. The Scout probe builds a
temporary `scout` executable and executes it directly so teardown closes the
actual MCP child. See `COMPARISON.md` for outcome counts, timing ranges, and
limits on the comparison.

`mcp-recovery-record` inspects a raw stdio JSON-RPC tool response to check
that action failures carry `isError=true` and Scout's machine-readable error
metadata under `_meta`. It also forces a delayed file-input `change` handler to
outlive the 300 ms `upload_file` deadline, then checks recovery of the expected
filename on the same target without replaying the file selection. A second
scenario times out a batch on its final click, checks that the partial result
retains the uncertain target and expected text, then reconciles the late effect
without replaying the click. It also delays a `<select>` change handler beyond
the action deadline and verifies that Scout retains the resolved option value
for reconciliation on the original target. A final case delays a hover handler
and reconciles its expected tooltip text on the original target. The Go SDK's
convenience `ToolResult` currently drops result metadata, so the probe reads
`protocol.Response` directly:

```sh
SCOUT_BENCHMARK_FOCUS=mcp-recovery-record go run ./examples/benchmark
```

For the pinned Playwright MCP in-flight comparison, start the fixture server,
then run the adapter with an HTTP idle timeout above the fixture's observed
wait duration. The probe records the exact tool and browser versions and
repeats the same delayed-element predicate:

```sh
go run ./examples/benchmark/fixtureserver -listen 127.0.0.1:41731
node examples/benchmark/compare_playwright_inflight_idle.mjs http://127.0.0.1:41731 0.0.84 5 5000
```

Use the same `delay-ms` and timeout values for Scout's focused probe. For
example, both products can be run with a 1.8 s wait and 1 s idle timeout:

```sh
SCOUT_BENCHMARK_FOCUS=mcp-http-idle-inflight \
SCOUT_BENCHMARK_IDLE_TIMEOUT=1s SCOUT_BENCHMARK_WAIT_DELAY_MS=1800 \
go run ./examples/benchmark
node examples/benchmark/compare_playwright_inflight_idle.mjs http://127.0.0.1:41731 0.0.84 5 1000 1800
```

The Playwright adapter starts a fresh MCP server and browser for every
repetition, so one expired context cannot affect later attempts.

The matched capture on 6 November 2026 passed Scout's 1 s / 1.8 s case 5/5,
including idle expiry after the call; Playwright MCP 0.0.84 failed all five
waits when the configured idle timeout elapsed before the target appeared.
See `COMPARISON.md` for scope and raw result paths. Recheck this against newer
releases before treating it as a maintained compatibility guarantee.

That recheck was repeated on 12 November against Playwright MCP v0.0.85: with
the same 1,000 ms timeout and 1,800 ms fixture delay, Scout passed 5/5 and
Playwright failed 5/5 at about one second. The raw reports and explicit scope
limits are retained in
[`reports/playwright-inflight-idle-current-2026-11-12/`](reports/playwright-inflight-idle-current-2026-11-12/).
Reproduce the Playwright arm with a fixture server on port 41743:

```sh
go run ./examples/benchmark/fixtureserver -listen 127.0.0.1:41743
node examples/benchmark/compare_playwright_inflight_idle.mjs \
  http://127.0.0.1:41743 0.0.85 5 1000 1800
```

Playwright MCP v0.0.84 fixed a separate per-client idle-timer issue reported
in [#1795](https://github.com/microsoft/playwright-mcp/issues/1795), via
[PR #43527](https://github.com/microsoft/playwright/pull/43527). This test
targets [#42693](https://github.com/microsoft/playwright/issues/42693), which
remains a version-specific in-flight timeout regression case.
