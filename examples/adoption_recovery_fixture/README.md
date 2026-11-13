# Shared adoption recovery fixture

This local fixture gives Scout and Stagehand Go the same browser page, delayed
mutation, and independent application ledger. The `/submit` handler records
the effect before it holds the response. The operator releases that response
after stopping the worker. The optional JSON ledger file is atomically updated
with mode 0600 so committed effects and accepted/rejected mutation-attempt
records survive a fixture process restart. It reads the original commit-array
format for existing fixture files. Without
`-ledger-file`, state stays in memory. The fixture is not for production use.

The server refuses non-loopback listen addresses because operator controls are
intended only for a local facilitator. The participant mutation endpoints
require a per-page random token and same-origin Fetch metadata, which rejects a
bare direct HTTP POST; this is a study guardrail, not a security boundary
against a participant who can inspect the page or run scripted browser code.

## Start and reset

```sh
go run ./examples/adoption_recovery_fixture -listen 127.0.0.1:41990 \
  -ledger-file /tmp/adoption-study-ledger.json
curl -X POST http://127.0.0.1:41990/operator/reset
```

Open `http://127.0.0.1:41990/?task=study-approval-1`. The approval button
submits exactly one browser request per click. Do not click twice. The
participant must stop their worker after the facilitator confirms that the
ledger committed the effect and before releasing the held response.

## Facilitator sequence

Use the controller to run each participant implementation. It resets the
ledger, runs first success, completes a nominal approval control, checks its
single ledger effect and page postcondition, releases its response, then resets
before running the faulted approval. For the faulted task it waits until the
ledger commits exactly one effect, kills the submit process group, releases the
held response, and launches the fresh reconciliation process. This captures
process and fixture timestamps without prescribing browser library calls.

```sh
python3 examples/adoption_recovery_fixture/run_arm.py \
  http://127.0.0.1:41990 study-approval-1 \
  --workdir /path/to/participant/checkout \
  --first-success-command 'go run ./cmd/study-worker first-success' \
  --control-command 'go run ./cmd/study-worker control' \
  --submit-command 'go run ./cmd/study-worker submit' \
  --reconcile-command 'go run ./cmd/study-worker reconcile' \
  --output /path/to/evidence/P01-scout.json
```

The participant commands receive `--task-id` and `--fixture` arguments. The
control command receives the task ID with `-control` appended; it must make the
approval normally and emit the common control JSON fields in the task card. The runner
releases the response only after observing exactly one effect, checks the
rendered postcondition and report, then resets the ledger before the faulted
task. The first-success command uses the `/first-success?task=...` page. After the button
handler updates `#status`, the page sends fixture telemetry containing the
rendered status. The runner checks that telemetry and the command's
independently read DOM postcondition. The token rejects a bare telemetry POST;
source review is still needed because scripted browser requests can reproduce
the page's same-origin request.
Submit and reconcile use the fixture root. Before the worker run, the facilitator gives the participant
the task card and allows them to build these commands in a clean checkout.

Each `--*-command` value is parsed as quoted argv with Python `shlex.split`
and launched directly; it is **not** evaluated by a shell. Use one executable
plus arguments. Shell pipelines, redirects, command substitution, and inline
environment assignments are not supported; if an environment variable is
needed, set it in the facilitator process before launching the runner. Invalid
quoting or an empty command writes a failed preflight JSON report and exits
before resetting or contacting the fixture. The runner never invokes a shell
for participant commands.
Repeat with Stagehand Go after resetting to the same browser build and task ID.
The runner sends SIGKILL to the submit process group once the ledger commit is
visible, so detached descendants are outside its guarantee.

Each run report records the checkout Git HEAD when available and a SHA-256
manifest of recognized source/config files referenced by the participant's
four command definitions. The manifest stores relative paths, sizes, and
digests, not file contents. It is not a complete dependency or build
attestation; preserve the checkout separately for source review and LOC
measurement. It also records resolved command executable names, sizes, and
SHA-256 digests, which can identify a prebuilt binary without copying it.
Known credential and private-key filenames are excluded from the source
manifest.
Verify a preserved source checkout with:

```sh
python3 examples/adoption_recovery_fixture/verify_source_manifest.py \
  /path/to/evidence/P01-scout.json --workdir /path/to/participant/checkout
```

When a command uses a prebuilt binary or source paths are not command
arguments, pass the participant implementation explicitly with repeated
`--source-path` values, for example `--source-path ./cmd/study-worker`. To hash
all recognized source/config files in the participant checkout, use
`--source-path .`; excluded build/dependency directories and known credential
and key files remain omitted. An empty manifest is recorded explicitly and
cannot be verified; resolve that before treating the run as source-audited
evidence.

Common credential flags and credential-like environment assignments are
redacted in saved command metadata. Avoid secrets in command strings and review
child output for sensitive values before sharing reports.

The reconciliation command emits the common observable fields specified on
the task card. The runner saves process timestamps, independent ledger
entries, exit codes, and child output, then grades the reported outcome
against the live ledger. The grader checks participant task identity and
reported effect and dispatch counts against the requested task and authoritative
ledger, including requests rejected by fixture origin/token checks. Product-native
task records and host journals are reviewed separately from this common machine
grade; the fixture cannot observe browser actions that never reach its endpoint.

```json
{
  "task_id": "study-approval-1",
  "ledger_count": 1,
  "initial_status": "unknown",
  "ledger_inspected": true,
  "final_classification": "verified",
  "retry_attempted": false
}
```

```sh
python3 examples/adoption_recovery_fixture/grade_run.py \
  http://127.0.0.1:41990 study-approval-1 run.json
```

The grader independently fetches the ledger and checks the exact effect count,
participant task ID and reported count, recorded classification, and runner-captured fields. The fixture reports `attempt_count` and `accepted_dispatch_count` separately
from its effect count. Attempts rejected by page-request validation appear
with `accepted: false` and a rejection reason; accepted requests commit one
effect atomically. The grader checks that a clean participant run has no
rejected mutation attempts and compares the adapter's `dispatch_count` to the
server-side accepted count. This detects bare or wrongly scoped requests that
reach the fixture, but cannot observe browser actions that never reach it.
Review participant source to confirm that it used the intended browser action
and did not call the fixture endpoint directly.

On a completed facilitator run, `run_arm.py` invokes this grader against the
saved evidence and the live fixture ledger. Its final `passed` value is true
only when both the runner's checks and the independent grader pass; the saved
JSON includes the grader result under `independent_grade`. The standalone
command above remains useful for rechecking preserved evidence while the
fixture ledger is still available.

## Verification completed

The fixture was built and exercised locally. The ledger showed one committed
effect before response release; the grader accepted the single-effect record
and rejected a second effect. A bare POST probe returned 403 and appeared in
the ledger as one rejected attempt, zero accepted dispatches, and zero effects.
A configured JSON ledger survived a fixture
process restart with count one, its file mode was 0600, and a `0.0.0.0` bind
was rejected. The new attempt record also survived a process restart: one bare
POST remained recorded as rejected, with zero accepted dispatches and effects.
The facilitator runner then completed its sequence with temporary
mock child commands, including process-group kill and independent ledger
grading ([runner smoke report](reports/facilitator-runner-harness-smoke-2026-11-06.json)).
The mock first-success command posted the fixture completion endpoint directly,
so this validates harness wiring only, not browser control or either product.
These are local fixture checks, not participant-study results.

The expanded independent dispatch-count grade was then exercised with fresh
Scout and Stagehand Go adapter rehearsals. Each run reported one accepted
mutation request, matching one committed effect and the adapter's reported
dispatch count; both preserved `unknown`, inspected the ledger, and made no
retry. These runs validate the harness measurement on these adapters only.
A separate bare-POST probe was rejected by origin/token checks and appeared in
the ledger as one rejected attempt with zero accepted dispatches and effects.
The adapter runs and probe validate the harness only. They do not verify
participant source, measure comparative implementation effort, or establish
adoption demand.

A separate Chrome fixture smoke clicked the first-success button and verified
the DOM transition and one server event, then clicked approval once and checked
that the effect was in the ledger while the request remained pending. Releasing
the operator gate returned `Accepted 1`; the ledger stayed at one effect
([browser smoke report](reports/browser-fixture-smoke-2026-11-06.json)). This
validates the fixture UI and delayed-response behavior only. It did not run a
Scout or Stagehand implementation, inspect participant code, or establish user
demand.

## Facilitator adapter rehearsal

Runnable direct-API adapters for Scout and Stagehand Go are in `adapters/`.
They let a facilitator rehearse both browser paths through the same runner:

```sh
cd examples/adoption_recovery_fixture/adapters
go build -o /tmp/adoption-scout ./scout
go build -o /tmp/adoption-stagehand ./stagehand
```

Run each with `run_arm.py`, selecting the adapter's `--stage first-success`,
`--stage control`, `--stage submit`, and `--stage reconcile` commands. The Stagehand adapter pins
SDK v4.3.0. The Scout adapter runs the mutation through `RunTaskContext` with a
persistent bbolt store. Pass the same unique `--store` path to its `submit` and
`reconcile` commands. The fresh reconciler opens that store, requires the
interrupted task record, exports and checks its unknown recovery receipt, then
reads the fixture ledger and persists a host resolution with evidence source
and observation time. Its `dispatch_count` field is adapter-reported rehearsal
data, not an independently observed count of browser requests. The paired
Stagehand adapter writes a small fsync-backed host journal before the click,
reopens and validates that journal after process loss, then persists the
ledger resolution. This journal is participant-authored host glue, not
Stagehand functionality. The adapters demonstrate the same recovery outcome
through different integration surfaces, but the prewritten code cannot measure
participant LOC or usability. They remain facilitator tools, not participant
solutions or comparative user-study evidence.

Example Scout runner invocation for one arm (use a fresh task ID and store
path for each repetition):

```sh
python3 ../run_arm.py http://127.0.0.1:41990 study-approval-1 \
  --workdir . \
  --first-success-command '/tmp/adoption-scout --stage first-success' \
  --control-command '/tmp/adoption-scout --stage control --store /tmp/scout-study-approval-1-control.db' \
  --submit-command '/tmp/adoption-scout --stage submit --store /tmp/scout-study-approval-1.db' \
  --reconcile-command '/tmp/adoption-scout --stage reconcile --store /tmp/scout-study-approval-1.db' \
  --source-path ./scout --source-path go.mod --source-path go.sum \
  --output /tmp/adoption-scout-study-approval-1.json
```

For the Stagehand arm, use the same runner and reset, replacing the binary with
`/tmp/adoption-stagehand`. Its control command needs no store; pass
`--store /tmp/stagehand-study-approval-1.json` to both submit and reconcile so
the fresh process can reopen the adapter's host journal. Also pass
`--source-path ./stagehand --source-path go.mod --source-path go.sum` so its
report can be tied to the adapter source and pinned module graph.

The original adapter rehearsal passed for both arms on 6 November 2026; see
the [summary](reports/adapter-rehearsal-2026-11-06.json), [Scout raw run](reports/adapter-rehearsal-scout-2026-11-06.json), and [Stagehand raw run](reports/adapter-rehearsal-stagehand-2026-11-06.json).
Those original reports predate the Scout adapter's durable task-record path
and verify the shared fixture sequence only. The updated Scout adapter then
passed a real browser fixture run: the facilitator observed one ledger commit,
killed the worker process group before releasing the held response, and the
fresh process loaded an in-flight record with an `unknown` receipt. It checked
the ledger, recorded and read back `verified_complete` with evidence
provenance, and reported no retry. The final independent ledger count was one.
See the [raw run](reports/scout-durable-task-rehearsal-2026-11-06.json). This
validates Scout's local recovery path against this fixture; it is not a
Stagehand comparison or participant-study result.

The updated paired rehearsal then ran both adapter paths with equivalent
interruption and ledger checks. Each passed all 21 checks, preserved `unknown`
after interruption, recorded one fixture effect, reconciled to `verified`, and
made no retry. Scout used its native bbolt task record; Stagehand used the
adapter's fsync-backed host journal. See the [paired summary](reports/paired-rehearsal-2026-11-06.json),
[Scout raw run](reports/scout-paired-rehearsal-2026-11-06.json), and
[Stagehand raw run](reports/stagehand-paired-rehearsal-2026-11-06.json).
These were sequential facilitator rehearsals. Their timings are descriptive
only, and the result does not measure participant effort, usability, demand,
or comparative performance.

## Current boundary

This fixture is suitable for a facilitator-led prototype, not yet a complete
recruitment bundle. The runner records the server's received/commit times and
the worker process-group kill, but does not independently inspect detached
descendants or participant code. A per-page token and browser Fetch metadata
reject accidental or bare direct HTTP calls to mutation endpoints; they do
not attest that a human clicked the UI because a participant can inspect the
page or issue scripted browser requests. The unauthenticated loopback operator
controls also assume a facilitator-controlled machine. Provide clean
participant checkouts and have a second person rehearse the procedure before
sessions. Review source and observe the browser interaction. Do not treat the
harness as independent proof of UI use or secure operator isolation.

Fresh run reports include SHA-256 hashes for the fixture server, facilitator
runner, and independent grader. The participant-record importer requires both
arms to carry identical harness hashes and the same fixture base URL; reports
from older rehearsals without this provenance remain useful for fixture
history but cannot complete a participant arm in the normalized study data.

The runner records `git_changed_paths` relative to the supplied participant
checkout, even when that checkout is nested inside a larger repository. It
still records the containing repository's HEAD. A focused standard-library
regression protects this scope:

```sh
python3 -m unittest examples.adoption_recovery_fixture.test_run_arm -v
```

The same focused test covers malformed command quoting and verifies that a
preflight failure emits an evidence report without contacting or resetting the
fixture.

On 12 November 2026, both adapter arms were rerun through the final runner on
the local Chrome fixture. Scout and Stagehand each passed all 15 shared checks
and the independent ledger grader; each produced one accepted dispatch and
one effect, preserved `unknown` before fresh-process reconciliation, and did
not retry. Their source manifests verified against the adapter checkout. The
machine reports are in
[`reports/paired-audit-2026-11-12`](reports/paired-audit-2026-11-12/).
These are facilitator rehearsals using prewritten adapters; they verify the
field kit and the harness provenance boundary, not participant behavior,
implementation effort, or product preference.

The tokenized fixture was rehearsed on 10 November with Scout and Stagehand Go.
Both three-stage runs passed the common facilitator contract and independent
ledger grader; bare `/submit` POSTs returned HTTP 403. These are updated
facilitator rehearsals only. See the [Scout run](reports/scout-three-stage-rehearsal-token-2026-11-10.json)
and [Stagehand run](reports/stagehand-three-stage-rehearsal-token-2026-11-10.json).
