# Scout adoption study

**Purpose:** determine whether Go platform and QA teams have an urgent browser-worker problem that Scout solves better than their current stack. The study tests the current product thesis; it does not assume that Go-native, recovery, or policy controls are valuable differentiators.

## Participants

Recruit 5–8 engineers who have operated browser automation from a Go service, worker, or agent harness in the last year. Include both current users and engineers who evaluated and rejected browser automation. Record role, organization size band, workload, current browser stack, and whether they own production operations. Do not recruit only Scout users or people already familiar with its implementation.

The shareable, neutral [recruitment kit](adoption-recruitment-kit.md) contains
an invitation, eligibility screener, and facilitator checklist. Complete the
date, environment, and compensation details before outreach; keep scheduling
and contact information separate from de-identified study records.

## Interview (25 minutes)

Ask for the most recent real task, not a hypothetical ideal:

1. What browser workflow runs in production or blocks a team today? Who owns it?
2. What happens when the browser disconnects, a call times out, or the worker restarts?
3. How does the system decide whether a form submission or other mutation committed? What evidence is authoritative?
4. How are caller identity, browser/session ownership, allowed destinations, and credentials configured?
5. Where does the current integration consume engineering or on-call time? Ask for a recent example and rough frequency/cost.
6. What did the team evaluate and reject? What made that option unsuitable?
7. Which outcome would justify changing the current system in the next quarter?

Avoid pitching Scout until these questions are answered. Label each statement as observed current practice, a recent example, a desired feature, or a guess. Ask permission before retaining quotes; otherwise keep anonymized notes.

## Matched implementation task (up to 60 minutes per product; 120 minutes total)

Use one versioned, local fixture with three stages, modeled on the repository's
[`examples/ci`](../examples/ci/main.go) and
[`examples/durable_task_recovery`](../examples/durable_task_recovery/main.go)
flows. Stage A is the first-success task: start a Go session, navigate to the
fixture, click its completion button, and verify the explicit page postcondition.
Stage B is the nominal control for the same approval mutation: submit once,
release its response after commit, and verify the rendered success plus one
ledger effect. Stage C repeats the approval implementation with worker loss
after commit and before acknowledgment, then inspects the ledger before deciding
whether work is complete. The expected safe result is one committed effect and
no blind replay; the fixture should return `unknown` until authoritative host
state resolves it. The runner resets the ledger between the control and faulted
approval, using the task ID with a `-control` suffix for the nominal control
and the original task ID for the faulted approval. Use the same original task
ID for the Scout and Stagehand reports, with separate participant checkouts and
product-owned task stores, so the importer can verify both arms ran the same
logical task without sharing cached state. Pin and record the fixture commit,
browser build, and fault injection point.

The participant-facing, neutral instructions and facilitator controls are in
[`adoption-task-card.md`](adoption-task-card.md). The card specifies common
observable output for both products; the fixture runner checks that task ID,
reported count, ledger, and outcome agree. Product-native task records and
host journals are reviewed separately as integration effort. The current Go
fixture source serves the participant page and mutation handler from the same
local server. Per-page tokens and same-origin Fetch metadata reject bare
direct HTTP calls, but do not prove human UI use. Keep the exercise
facilitator-only until a second person has completed the procedure from clean
participant checkouts and reviewed both source manifests. The three-stage fixture is in
[`examples/adoption_recovery_fixture`](../examples/adoption_recovery_fixture/)
and the independent ledger grader is packaged with it. Stage A, the nominal
approval control, and the faulted approval share the same server and runner.
The fixture page reports its rendered
completion status after the click handler updates the DOM. This is fixture
telemetry; participants can inspect the page or issue scripted browser
requests, so source review and observation remain necessary.
Capture sessions with the blank
[`adoption-session-template.md`](adoption-session-template.md), then add one
CSV row per product arm to `adoption-study-metrics.csv`. Record the nominal
approval control separately from the faulted approval. Do not enter
participant results until sessions actually occur.

Each participant implements all three stages once with Scout and once with the
selected official Stagehand Go SDK v4 comparator, using the same machine, Go
version, browser build, fixture, instructions, and a 60-minute limit for each
product arm (120 minutes total). Counterbalance product order.
Provide each product's official documentation and equivalent fixture access.
Permit ordinary coding tools; log help requested and setup failures. Pin the
selected v4 module to its latest stable release available on the study date
and record the exact module path, version, and checksum; do not substitute the
standalone v3 API line. Stagehand
publishes both the official `github.com/browserbase/stagehand-go/v3` line and
the monorepo's `github.com/browserbase/stagehand/packages/sdk-go/v4` line; pin
one explicitly and do not describe v3 as archived.

Keep the **primary task deterministic**: use direct, documented Go page APIs
for the click and postcondition in both products. Stagehand's Go SDK supports
local Chrome and direct page/locator calls. A pinned v4.3.0 local fixture run
confirmed that `Stagehand.Create` initializes the worker with no model and the
direct `Goto`, `Click`, and `InnerText` calls complete without a provider key
or model call. See the runnable adapter at
[`examples/benchmark/stagehand_go_first_success`](../examples/benchmark/stagehand_go_first_success/).
The official quickstart's provider key is for semantic operations. Do not
silently make credentials a prerequisite for this deterministic control arm;
if a future pinned release changes this behavior, report the prerequisite and
do not make a paid model call in the primary task. The selected v4 module's
latest stable tag is v4.3.0 as of 10 November 2026 (checksum
`h1:5ZXuSNhVSk2OjkyTCcTfdSaozZJP1JO8U4l6cY39dZs=`); it requires Go 1.26. The
official quickstart currently shows a `v4.0.0` install command, while the
latest tagged v4 module is v4.3.0. Use the versioned Go module as the release
source of truth and record its checksum. A fresh module query on 10 November
2026 still resolves the v4 path's `@latest` to v4.3.0.
The monorepo currently has a separate open TypeScript release PR for 4.4.1; it
is not evidence of a newer Go SDK. Verify the module again at study time,
because SDK and setup requirements change. See the current [competitive
refresh](competitive-opportunities.md#competitive-refresh-extension-workflows-preserve-more-operator-context-10-november-2026).

If the study includes Stagehand semantic `Act`, `Observe`, or `Extract`, run
that as a separate **AI-enabled arm**. Use the same pinned model, provider,
prompt, and participant-owned credentials in both products where possible;
record model calls, token/cost data if available, network access, and setup
time separately. Never combine its timings or results with the deterministic
primary arm. If the attendee cannot or does not want to provide credentials,
mark the semantic arm unavailable and retain the deterministic pair.

Record the following separately for the deterministic arm and any AI-enabled
arm:

- time from a clean checkout to first independently verified fixture outcome;
- dependency installation, browser acquisition, credential, model, and
  network prerequisites (including whether each is required for the selected
  non-semantic path);
- elapsed time to finish the full task and number of assistance requests;
- lines of host integration code, excluding generated and vendored files;
- explicit code/config needed for owner/session/target binding, policy,
  deadline, postcondition, and recovery; identify whether each is native to the
  pinned product or participant-authored glue. Record owner scope, session
  lifecycle, target binding, origin policy, deadline, postcondition, task
  identity, durable task state, replay guard, and host reconciliation as
  separate fields in the session record; do not merge them into one rating;
- browser dispatch count, fixture ledger count, and whether the worker inspected authoritative state before retrying;
- nominal approval control pass and ledger count for each product, paired with the corresponding faulted task;
- final outcome classification (`verified`, `rejected`, `unknown`) and whether a retry occurred;
- participant confidence, preference, and the reason for that preference.

Capture screen/activity only with consent. Preserve the exact product/package versions, source code, fixture logs, task instructions, and environment metadata. Do not collect production credentials or personal browsing data.

## Analysis and decision rule

Report each participant's paired results and medians/ranges; with 5–8 participants, treat this as directional usability evidence, not a statistically representative market estimate. Separate hard task success from preference and from implementation effort. Publish failures and exclusions.

Advance “host-owned recovery contract” as the lead product position only if:

1. at least 80% of evaluable participants (minimum 4 when N=5) describe a recent, costly ambiguity/recovery problem without being prompted with Scout's terminology;
2. at least 80% of evaluable participants (minimum 4 when N=5) complete the nominal approval control under both products; among those control-competent pairs, Scout produces no duplicate fixture effect and correctly leaves unresolved outcomes as `unknown`;
3. Scout's paired median time-to-first-verified-result or full-task integration effort is at least 25% lower than Stagehand Go, **or** at least 80% of evaluable participants (minimum 4 when N=5) prefer Scout specifically because it makes recovery/ownership materially easier;
4. no participant reports that Scout's controls made a valid workflow impossible without a documented, acceptable workaround.

Use the number of participants with usable evidence for each condition as its denominator; report that numerator and denominator and do not count missing or incomplete evidence as a success. Apply the 80% threshold as a rounded-up count (for example, 4/5, 5/6, 6/7, or 7/8). For paired task comparisons, include only participants who completed both product arms and report the paired sample size. If fewer than five participants provide evaluable evidence, treat the decision rule as inconclusive and recruit more before advancing the positioning.

If demand condition 1 fails, do not lead with recovery. If task correctness passes but effort/preference fails, retain the contract as an engineering strength and prioritize onboarding or the specific friction participants identify. If duplicate effects occur, treat that as a product correctness failure regardless of preference. The thresholds are decision heuristics for this small formative sample, not market proof.

### Reproducible analysis

Keep one de-identified JSON file per participant. The minimum shape consumed by
[`analyze_adoption_study.py`](analyze_adoption_study.py) is:

```json
{
  "evidence_type": "participant",
  "participant_session_id": "P01",
  "study_id": "P01",
  "excluded": false,
  "interview": {"recent_costly_ambiguity": true},
  "scout": {
    "completed": true,
    "first_verified_seconds": 420,
    "full_task_seconds": 1800,
    "integration_loc": 120,
    "nominal_control_pass": true,
    "faulted_final_classification": "verified",
    "faulted_ledger_count": 1,
    "dispatch_count": 1,
    "unknown_preserved": true,
    "inspected_authoritative_state": true,
    "retry_attempted": false,
    "unacceptable_friction": false
  },
  "stagehand_go": {
    "completed": true,
    "first_verified_seconds": 500,
    "full_task_seconds": 2400,
    "integration_loc": 190,
    "nominal_control_pass": true,
    "faulted_final_classification": "verified",
    "faulted_ledger_count": 1,
    "dispatch_count": 1,
    "unknown_preserved": true,
    "inspected_authoritative_state": true,
    "retry_attempted": false
  },
  "preference": {"product": "scout", "reason_category": "recovery"}
}
```

Run `python3 docs/analyze_adoption_study.py docs/study-results/P*.json
--output docs/study-results/summary.json` after reviewing exclusions and
entering each participant's source evidence. The analyzer reports per-condition
denominators, rounded-up thresholds, paired medians, safety failures attributed
to Scout or Stagehand Go, and the decision branch. Explicitly observed unsafe
outcomes remain visible even when an arm is incomplete; missing fields alone
remain missing evidence. A Scout safety failure is a product correctness
failure, while a Stagehand safety failure invalidates the comparison without
being attributed to Scout. These safety outcomes take precedence over sample
size, demand, effort, and preference branches. Missing fields never count as
success; fewer than five
usable interviews or missing paired task/preference evidence remain
inconclusive. Do not add rehearsal reports to the input glob. Verify each
participant's independently graded arm reports and inspect source before
transcribing results. A generated decision is a transparent application of
the protocol, not a replacement for facilitator judgment or the raw records.

Run `python3 -m unittest docs.test_adoption_study docs.test_adoption_importer -v`
to check the decision gate's success and missing/unsafe-evidence branches,
trusted-checkout containment and content hashes, and paired task identity
before changing the study tooling.

Create a normalized record from the two machine reports and facilitator-coded
session evidence with:

```sh
python3 docs/import_adoption_session.py \
  --attestation docs/study-results/P01-attestation.json \
  --scout-report docs/study-results/P01-scout.json \
  --stagehand-report docs/study-results/P01-stagehand-go.json \
  --trusted-root /srv/scout-study/checkouts \
  --output docs/study-results/P01.json
```

The attestation must include `evidence_type: "participant"`, a matching
`participant_session_id`, explicit interview and preference classifications,
and manually recorded setup-to-first-result seconds, full-task seconds,
integration LOC for each product arm, plus an explicit boolean
`unacceptable_friction` assessment for the Scout arm. Both machine reports
must also provide the faulted classification, initial `unknown`, authoritative
ledger inspection, retry result, final ledger count, and accepted dispatch
count. The importer leaves absent values unavailable and adds completion
blockers, rather than turning absence into a safety failure. Missing friction
assessment is unavailable evidence and must
keep the positioning decision inconclusive; it must never be defaulted to
`false`. Reports must share a task ID and have the
current runner schema. An arm counts as complete only when the facilitator
runner and independent ledger grader pass, the facilitator attests that the
participant source was reviewed, and the checkout is preserved for audit. A
missing nominal-control result remains unavailable rather than becoming a
failure or a success; control denominators include only explicit boolean
evaluations, while incomplete pairs keep the decision inconclusive. The
importer stores report hashes and completion blockers so later changes and
missing prerequisites are visible. It also re-hashes every source-manifest
entry against the preserved checkout; a missing, empty, unsafe, or changed
manifest blocks that arm from counting as completed. Preserve each checkout at
its recorded `workdir` until import finishes. Old rehearsals lack the current
grade fields and cannot be promoted into participant evidence. Importer status does
not verify the facilitator's timing, coding, source review, or participant
identity; preserve those source notes separately.

`--trusted-root` must name the parent of the preserved participant checkouts.
The importer rejects a report whose recorded work directory resolves outside
that root before reading manifest files. Keep the root controlled by the study
operator and avoid placing unrelated sensitive files beneath it.

To audit existing fixture/rehearsal artifacts without accidentally treating
them as participants, run
`python3 docs/audit_adoption_evidence.py examples/adoption_recovery_fixture/reports/*.json`.
Reports count as participant evidence only when an authorized facilitator adds
both `"evidence_type": "participant"` and matching non-empty
`participant_session_id` and `study_id` values after the live session. This
matches the analyzer's identity check; a mismatched or incomplete participant
marker is excluded from the inventory. Existing adapter, fixture, and
benchmark reports remain excluded.

The analyzer's synthetic branch checks are software validation only. Until
participant JSON exists, the study remains unrun and no adoption or positioning
decision can be inferred.

## Current status

Protocol only; no participants have been recruited and no user study has been run. The repository's local fixture reports establish technical behavior in controlled runs, not user demand or comparative implementation effort. The Stage A `examples/ci` fixture enables private-IP navigation because its temporary server binds to loopback; production-facing setup guidance should continue to distinguish that test exception from secure default policy. Record study dates, anonymized participant IDs, raw paired metrics, and deviations here only after actual sessions are completed.

**Field readiness:** the protocol, neutral task card, shared local recovery
fixture, operator controls, and independent ledger grader are now packaged in
[`examples/adoption_recovery_fixture`](../examples/adoption_recovery_fixture/).
The fixture can persist its ledger across restarts with `-ledger-file`, refuses
non-loopback binds, and now has a facilitator runner for first success, a
nominal approval control, worker process-group interruption, and fresh-process
reconciliation. The runner captures fixture receive/commit times and process
exit, then checks participant results against the ledger. New run reports also
record the checkout Git HEAD, changed paths when available, executable names,
sizes and SHA-256 digests, plus a SHA-256 manifest for recognized source/config
files referenced by the four commands. The manifest contains paths, sizes, and
digests, not file contents; preserve the checkout separately for source review
and LOC measurement. This is scoped provenance, not a complete dependency or
build attestation. Changed paths are scoped to the supplied participant
checkout, including when it is nested inside a larger repository. A focused
regression verifies this provenance boundary. On 12 November, the final
runner passed all 15 checks and independent ledger grading for both prewritten
Scout and Stagehand adapters on a local Chrome fixture; both source manifests
also verified. These remain facilitator rehearsals and do not count as
participant evidence. Raw reports are in
`paired-audit-2026-11-12` (local audit artifact).
Recheck source provenance with
[`verify_source_manifest.py`](../examples/adoption_recovery_fixture/verify_source_manifest.py).
Common credential flags and credential-like environment assignments are
redacted in saved command metadata, but child output is preserved verbatim;
avoid secrets in commands and review output before sharing reports. The Scout and pinned
Stagehand Go adapters each passed the three-stage facilitator rehearsal: first
success, a normal approval control, then a faulted approval with fresh-process
reconciliation. The 6 November run passed its 22-check contract; the 10
November rerun passed the revised common observable contract and independent
ledger grader for both adapters. The latter confirms one committed effect,
retained `unknown` before ledger inspection, no reported retry, and a final
status consistent with the ledger. See the 10 November Scout report (local audit artifact),
Stagehand report (local audit artifact),
and the 6 November summary (local audit artifact).
These adapters are facilitator rehearsal tooling only: they are not participant
starter code, and their paired runs are not a participant comparison. The Scout
adapter now uses `RunTaskContext`, retains its task in bbolt across process
termination, exports the interrupted action receipt in a fresh process, and
persists a host resolution after reading the fixture ledger. The Stagehand
rehearsal now writes and reopens an fsync-backed, host-authored journal and
persists its ledger resolution. This is integration glue, not a Stagehand SDK
feature. The old Stagehand rehearsal report predates that journal and cannot
count as an equivalent recovery run. The paired adapters can now exercise the
same outcome, but their prewritten code is not a matched user study or a
competitive win. The 10 November grader does not verify source behavior,
native task records, host journals, elapsed time, or attempted browser
dispatches. A tokenized follow-up rejected bare direct HTTP mutation calls
with 403 while both browser adapters still passed; see the tokenized Scout
report (local audit artifact)
and Stagehand report (local audit artifact).
Those controls do not attest to human UI use; source review and observation
remain necessary.

The shared participant output contract was then tightened to require the
requested task ID, `initial_status: "unknown"`, ledger inspection, and a
participant-reported count matching the independent ledger. Both tokenized
adapters passed the expanded 13-check facilitator runner and 12-check
independent grader. See the Scout contract run (local audit artifact)
and Stagehand contract run (local audit artifact).
This closes a harness-schema gap only; participant implementations and the
second-person rehearsal remain outstanding.

**Fresh durable-recovery engineering run (10 November 2026):** the standalone
Scout recovery example passed again with Go 1.26.0. Its four cases cover
dispatch interruption after a committed effect, interruption after final page
verification but before result persistence, completed-result reuse from a new
Scout process, and browser replacement after a committed effect. All three
interrupted tasks rejected same-ID retry and reconciled as `verified_complete`
from the fixture's authoritative ledger with one accepted effect each. The
completed result was reused without another effect. The timestamped raw report
is `recovery-run-20261110T2214Z-go1.26.0.json` (local audit artifact).
This is a fresh engineering reproduction, not a participant session, adoption
signal, or comparative reliability estimate.

The independent fixture ledger now also exposes `accepted_dispatch_count`,
which the grader compares to each reconciler's reported `dispatch_count`.
Fresh Scout and Stagehand facilitator rehearsals passed this expanded grade
with one accepted request and one effect in each faulted task. This verifies
that the shared adapters' dispatch claims match the fixture's accepted-request
count. The ledger also records requests rejected by page-origin/token checks,
so the independent grade can fail a run that reached the fixture through an
invalid request path. It cannot count browser actions that never reach the
fixture or inspect participant-authored source. The report artifacts were
temporary and are not participant rows; relative user effort remains
unmeasured.

The earlier paired rehearsal passed the faulted path before a nominal control
was added; retain it as historical evidence. In the updated run, the
facilitator saw one fixture commit, killed the submit process group before
releasing the response, and a fresh process preserved an `unknown` recovery
result. Each adapter then inspected the independent ledger, persisted a
`verified_complete` host resolution with evidence provenance, and made no
retry; each final faulted-task ledger count was one. Both passed all 22 runner
checks. Scout used its native bbolt task store; Stagehand's adapter
used an fsync-backed host journal, which is integration glue rather than an
SDK feature. The earlier paired summary (local audit artifact)
and raw reports (local audit artifact) are
reproducible fixture evidence, not a participant comparison. The runs were
sequential and facilitator-authored; durations are not comparative evidence.
Participant-authored clean-checkout trials remain necessary to measure setup
effort, usability, and preference.
Participant implementations in clean checkouts, source review, an equivalent
cross-product task contract, and a second-person procedure check remain before
recruitment. The brief records the second-person clean-checkout rehearsal as
the immediate operational gate. Stagehand's pinned SDK
exposes direct page APIs and CDP connect support but no durable task identity or
replay guard, so measure host glue rather than silently supplying a common
recovery library. Keep interview evidence separate from task performance;
benchmark results do not substitute for participant evidence.

**Comparator protocol correction (6 November 2026; refreshed 10 November):**
the primary comparison names the active `browserbase/stagehand` monorepo's Go
SDK v4. This is a deliberate choice of the protocol-first v4 API for the
model-free direct-page task, not a claim that the standalone v3 module is
archived or unsupported. GitHub currently identifies
[`browserbase/stagehand-go`](https://github.com/browserbase/stagehand-go) as
the official Go library and lists v3.21.1 (23 October 2026) as its latest
release, including a local-server security update. Stagehand's monorepo
changelog separately identifies Go SDK v4 as the protocol-first shared-core
line. The v4 quickstart currently shows an install command for v4.0.0,
while the latest tagged module in the selected v4 line is
`github.com/browserbase/stagehand/packages/sdk-go/v4@v4.3.0` as of 10 November
2026 (checksum `h1:5ZXuSNhVSk2OjkyTCcTfdSaozZJP1JO8U4l6cY39dZs=`); it requires
Go 1.26. Record the exact module checksum to avoid silently comparing
different SDKs. The quickstart launches local Chrome and constructs the
Stagehand client with a provider
model/API key for semantic `act`, `extract`, and `observe`. A local fixture run
with the pinned v4.3.0 module confirmed `Stagehand.Create` succeeds without a
model, and direct `Goto`, `Click`, and `InnerText` calls verify the postcondition
without a provider key or model call. The runnable adapter is at
[`examples/benchmark/stagehand_go_first_success`](../examples/benchmark/stagehand_go_first_success/).
Keep semantic operations in a separate AI-enabled arm and measure credential,
cost, and model setup separately. Recheck behavior with the pinned version
before recruitment. Sources: [official Go SDK](https://github.com/browserbase/stagehand/tree/main/packages/sdk-go),
[Go quickstart](https://github.com/browserbase/stagehand/blob/main/packages/docs/v4/first-steps/quickstart.mdx).

## Field kit

### Neutral recruitment note

> I'm studying how teams build and operate browser automation from Go services
> or workers. I'm looking for engineers who have run browser-backed work or
> evaluated a system for it in the past year. The session is about your recent
> workflow and a small implementation exercise, not a product demo. Please
> reserve about 2 hours 45 minutes (and schedule up to 3 hours). We will not use
> production credentials or personal browsing data, and I will only record the
> session with your consent. Would you be available to take part?

Send this only to people who meet the participant criteria above. Do not
preselect participants based on whether they use Go, like Scout, or have
reported a recovery incident. Track invitations and declines separately from
study results; do not retain names in the analysis file.

### Session run sheet

The timed segments total about 158 minutes before transitions. Describe the
session as about 2 hours 45 minutes and schedule a 3-hour calendar block.

1. **Consent and context (3 minutes).** Explain the task, what is recorded, the
   right to stop, and how notes are de-identified. Ask separately before screen
   recording or retaining direct quotes.
2. **Incident interview (25 minutes).** Use the questions above. Ask for the
   latest concrete example and sequence of events; do not introduce Scout,
   idempotency, or recovery terminology until the participant uses it.
3. **Task setup (5 minutes).** Give the same fixture commit, browser build,
   Go toolchain, task card, and time limit for both products. Ask the participant
   to use the supplied clean checkout and do not coach unless requested.
4. **Paired implementation (up to 120 minutes).** Randomly assign product
   order. Allow up to 60 minutes per product arm, reset the fixture and task ID
   between products, and apply the same interruption point. If time expires,
   preserve the incomplete state and record what remains; do not silently omit
   that pair.
5. **Debrief (5 minutes).** Ask which setup they would choose for their recent
   workflow, why, what felt risky or obstructive, and what they would change.
   Record preference only after both implementations.

Use `AB` and `BA` as order values and alternate them across participants. If a
session is interrupted or a product cannot be installed, record the reason and
retain the partial measurements; do not count the missing result as a pass.

### Per-participant record template

Copy this block once per participant. Use a random study ID such as `P01`;
store contact details, if any, separately from these notes and delete them when
recruitment follow-up is complete.

```text
Study ID:
Session date:
Role / team-size band / workload:
Operated browser workers in past year? [yes/no]
Current stack and deployment shape:
Recent concrete incident and observed cost:
Authoritative source used to determine business outcome:
Order: [AB/BA]
Consent: notes [yes/no], recording [yes/no], quote retention [yes/no]

Interview evidence (mark each statement as practice, recent example, desire, or guess):
- Browser workflow and owner:
- Disconnect, timeout, or restart behavior:
- Mutation outcome evidence:
- Caller/session/site policy setup:
- Engineering/on-call cost and frequency:
- Evaluated alternatives and rejection reason:
- Change trigger within next quarter:

Scout task:
- Version / commit:
- Start and first verified outcome times:
- Full task time / assistance requests:
- Host integration LOC / config notes:
- Dispatch count / authoritative fixture count:
- Final classification / duplicate effect? / unknown preserved? :
- Ownership, policy, deadline, postcondition, recovery wiring:
- Blocker or unacceptable control friction:

Comparator task:
- Product and package version / commit:
- Start and first verified outcome times:
- Full task time / assistance requests:
- Host integration LOC / config notes:
- Dispatch count / authoritative fixture count:
- Final classification / duplicate effect? / unknown preserved? :
- Ownership, policy, deadline, postcondition, recovery wiring:
- Blocker or unacceptable control friction:

Preference after both tasks and stated reason:
Protocol deviations / missing data / exclusion decision:
```

Keep raw task timings and counts even when the participant fails. A failed or
incomplete attempt is a result, not missing data. Record any exclusion and the
reason before reviewing aggregate outcomes. Do not convert a preference, a
feature request, or a hypothetical into evidence of a recent production pain.
