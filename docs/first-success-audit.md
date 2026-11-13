# First-success and diagnosis audit

This audit measures whether an engineer can get from a clean environment to a
trustworthy browser result and diagnose common setup failures. It complements
the [adoption study](adoption-study.md); its local script checks are harness
preflight only and must not be counted as participant usability evidence.

## Current competitive boundary (10 November 2026)

This is an operation-level comparison, not a checklist comparison. Agent-
browser 0.42.0 provides a broad JSON doctor with a real headless launch check,
offline/quick modes, and explicit optional repair. Stagehand issue #3359 remains
open and reports a false-green doctor result; a maintainer follow-up reproduced
the report with a mocked launch error but did not reproduce a physical Chrome
failure. Agent-browser's older Windows launch-hang and missing-browser
diagnostic reports (#1821 and #1711) were closed after fixes or improved error
handling, so treat them as regression inputs and verify the current release.
See the [dated competitive refresh](competitive-opportunities.md#competitive-refresh-first-success-must-test-the-operation-10-november-2026)
for source links and status details.

Scout's local doctor has three distinct levels: configuration preflight,
explicit live browser launch/attach, and an opt-in temporary-tab app probe
that checks a caller-provided text marker. Compare each verdict to a real first
browser read and record whether the reported phase matches the injected fault.
Do not count a static configuration pass as browser readiness, or an app marker
as proof that a business mutation committed.

## Clean-environment protocol

Run Scout and the pinned comparator in fresh containers or disposable virtual
machines with no Go module cache, browser cache, Scout environment variables,
or pre-existing task store. Pin and record the OS image digest, Go version,
Scout commit/release, comparator version, and browser build. Give each operator
the same task card and network access. Do not preload a browser or model
credential for the deterministic track.

Measure from the first instruction read until the operator independently
verifies a fixture postcondition:

1. Install dependencies and acquire the supported local browser.
2. Start the fixed local fixture and execute its deterministic success path.
3. Capture time, commands, help requests, participant-authored code/config,
   and the verified postcondition.
4. Repeat with one seeded failure at a time: malformed origin policy, invalid
   remote endpoint, unwritable task-store parent, browser launch failure,
   attached but unresponsive page, and live application probe with a missing
   marker. Include one clean first-read control. Run each product's own doctor,
   then the same bounded read against the fixture.
5. Ask the operator to explain the failure and choose the next safe action;
   grade the diagnosis against a fixed answer key, including whether a browser
   mutation could have committed and whether retry is safe.

Use separate fresh environments per product and counterbalance order. Preserve
raw command transcripts and redacted environment metadata. Never collect
credentials, cookies, real browsing data, or unredacted URLs containing
secrets. Keep the deterministic Go SDK comparison separate from MCP and any
model-assisted track.

## Measures and decision rule

Report per participant and product: successful first verified result, elapsed
time, assistance requests, setup failures, authored integration lines,
diagnosis accuracy by seeded fault, false-green/false-red results, time in
each observable startup/attach/read phase, cleanup completion, local-state
changes, and whether the tool recommended an unsafe retry. Treat each
participant as the unit; repeated faults within one participant are not
independent users.

Promote first-success/diagnosis as a product lead only when at least four of
five evaluable participants finish the Scout task and diagnose at least four
of five seeded faults correctly, no participant receives an unsafe retry
recommendation after ambiguous dispatch, and paired results show at least a
25% median time/effort reduction or four of five prefer Scout for a specific
diagnostic reason. Otherwise keep the capability as supporting product quality
and investigate the observed blocker. Report the small sample as directional,
not as a market estimate.

## Current evidence boundary

`examples/benchmark/run_doctor_readiness.py` checks static configuration,
live-browser readiness, a local application probe, and a bounded first CLI read
with both a successful and rejected text postcondition. Doctor JSON reports
stable checks and durations separately for browser session launch/attach,
status, bounded first page read, application probe, and cleanup; skipped phases
are explicit. The app-probe duration includes temporary-tab restoration and
close. `scout navigate` now reports browser startup, page navigation and
verification, and browser cleanup durations alongside the unchanged page
result fields; the readiness runner requires these phases in both positive and
negative first-read controls. The refreshed 11 November workstation report
passed four doctor scenarios and two first-read controls. The verified read
measured 293 ms startup, 109 ms navigation/verification, and 69 ms cleanup; the
missing-marker read measured 320/111/76 ms for those phases. This is one local
run, not an immutable clean-container result, and it does not measure operator
diagnosis or a matched competitor. External process wall time remains recorded
to expose overhead outside the measured browser phases. New readiness reports
use schema 4 and include hashes for Go build inputs and tracked changes,
effective Go toolchain settings, and presence-only display/proxy/CI indicators;
they do not store Go flags or proxy values. The existing 11 November report
predates those fields. No participant results are recorded by this protocol
yet. The pinned agent-browser 0.42.0 workstation
comparison passed all 14 harness predicates; Scout's measured
startup/navigation-verification/cleanup phases were 243/81/74 ms for the
verified read and 286/130/85 ms for the rejected read. The comparison records
its non-isolated Scout environment and does not establish participant effort or
equivalent postcondition behavior. Raw first-success summaries are local audit artifacts; the [curated recovery evidence](../examples/benchmark/evidence/fresh-late-recovery-2026-11-12/report.json) is included with the source.
Run the audit with the adoption-study field kit and append anonymized
participant-level rows only after observed sessions.

**Harness correction and rerun (12 November 2026):** review found the negative
agent-browser read reused the browser process from the positive read, while
Scout launched a fresh browser for each one-shot command. The comparison runner
now gives the two agent-browser controls separate temporary HOME/XDG profiles,
passes a distinct `--user-data-dir` to each, closes the positive browser before
the negative control, and requires each open command to succeed before grading
the read. The corrected
the 12 November raw summary is a local audit artifact
passed all 16 predicates. Both agent-browser reads used distinct browser data
directories; Scout launched a separate browser for each one-shot command. This
is a standalone-command comparison: browser startup/cleanup are included in
Scout's command boundary while agent-browser splits open, read, and close. It
does not support a speed ranking, clean-container onboarding claim, diagnosis
accuracy, participant effort, or reliability estimate. The prior checked-in
comparison remains historical and predates the isolation correction.
