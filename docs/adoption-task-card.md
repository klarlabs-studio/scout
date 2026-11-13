# Browser worker study task card

Give this card to participants after the incident interview. Use the exact same
copy, fixture reset, machine, Go version, and Chrome build for both products.
Allow up to 60 minutes per product arm (120 minutes total) and counterbalance
product order. Do not explain either product's recovery features before the
participant starts.

## Scenario

You are implementing a small Go worker that submits one approval in a browser.
The application may commit the approval while the browser response is delayed
and the worker process is interrupted. The application ledger is authoritative.
Your worker must not create a duplicate approval when it cannot tell whether the
first submission completed.

## Required behavior

1. Start from the supplied clean checkout and use the supplied Go and Chrome
   versions. You may use either product's documented Go APIs and ordinary
   coding tools. Do not use an AI model or provider key in this task.
   Implement four commands named `first-success`, `control`, `submit`, and
   `reconcile`. The facilitator starts them through the shared runner; each
   command receives `--task-id <id>` and `--fixture <url>`. The
   `first-success` command gets the original task ID and a fixture URL already
   set to `/first-success?task=<id>`. The `control` command gets `<id>-control`
   and the fixture base URL; `submit` and `reconcile` get the original task ID
   and fixture base URL. Use the fixture base URL as the origin for the
   approval page and read-only `/ledger` endpoint. The facilitator provides
   the exact URL and runner commands; do not start or reset the fixture
   yourself.
2. **Stage A, first success:** open the fixture's `/first-success` page, click
   `#complete`, verify `#status` reads `Complete`, and emit one JSON line with
   `status: "verified"`, `verified_text: "Complete"`, `task_id`, and
   `elapsed_ms`. The timer starts when your command starts and ends after the
   independent page postcondition read.
3. **Stage B, normal approval control:** navigate to the approval page with
   task ID `study-approval-1-control`, submit once, wait for the page to show
   `Accepted 1`, and report `verified`. The facilitator releases the normal
   response after the ledger records one effect. The runner independently
   checks the participant result and ledger, then resets the fixture.
4. **Stage C, uncertain approval:** repeat the same implementation with task
   ID `study-approval-1`. The facilitator runner observes the ledger commit,
   then stops the submit worker process group before releasing the delayed
   response.
5. In a fresh process, inspect your persisted task state or host recovery
   journal and the fixture's authoritative ledger. Decide whether the approval
   committed. If committed, record verified
   completion without another submit. If absent, explain what evidence would
   make a retry safe; do not invent evidence or retry during the measured task.
6. After the ledger read, emit one JSON line from the control command with
   `status`, `task_id`, `verified_text`, `ledger_count`, and
   `retry_attempted`. Emit one JSON line from reconciliation with
   `status`, `task_id`, `ledger_count`, `initial_status`, `ledger_inspected`,
   and `retry_attempted`. The expected faulted result is `verified`, the
   requested task ID, `1`, `unknown`, `true`, and `false`; do not report
   verified before inspecting the authoritative ledger. Preserve enough state
   in your implementation to explain how the fresh process knew which logical
   task it was reconciling.
   Show the code/config that binds the task ID, browser target, policy, and
   decision to the ledger result. Identify each ownership, task-state,
   recovery, and reconciliation control as native product behavior or
   participant-authored host glue. The facilitator separately inspects
   product-native records and any host journal; Scout-specific record fields
   are not required from the Stagehand SDK.

Use the same original task ID for both product arms. Reset the fixture and use
separate participant checkouts and product-owned task stores between arms; do
not carry one arm's persisted state into the other. The runner suffixes the
control task ID and resets the ledger before the faulted task.

For the fresh reconciliation process, reopen the same participant-owned
durable state used by `submit`. The runner starts a new process and supplies
the original task ID and fixture origin; it does not pass the prior process's
memory to `reconcile`. Preserve an initial `unknown` state before reading the
ledger, then report the ledger-backed decision in the required JSON line.

The task is successful only when the nominal control and faulted task each
have exactly one approval for their own task ID, the participant does not
blindly replay the mutation, and the final faulted-task decision is supported
by the ledger response. A browser page message or
SDK call result alone is not authoritative.

## Facilitator procedure

Before each product arm, reset the fixture ledger and task ID, preserve the
same browser build, and restore the same starting page. Use the fixture's
operator control to release the held response only after the worker has been
interrupted. The facilitator records process IDs and confirms the child process
has exited. After the participant completes or time expires, record ledger
counts and preserve source and logs. Do not help unless asked; record each
request and the exact help supplied.

The first-success fixture page posts its rendered status to fixture telemetry
after its click handler updates the DOM. Mutation endpoints require a per-page
token and same-origin Fetch metadata, which rejects a bare direct HTTP POST.
That does not prove which browser API the participant used: a participant can
inspect page state or issue scripted browser requests. Review source and
observe the run.

The fixture must expose a separate operator-only interruption/release control
and a read-only ledger endpoint. Keep these controls out of the participant's
browser page. The faulted task must not be called verified until after the
ledger check matches task ID and count. Never count an SDK's success return as
proof of application state. Scout's native task record should retain its
interrupted/unknown state before host reconciliation; Stagehand is graded on
the equivalent state retained by participant-authored host glue. Record this
difference as integration effort, not as a missing SDK feature that
invalidates the task.

The local facilitator fixture and independent ledger grader are documented in
[`examples/adoption_recovery_fixture`](../examples/adoption_recovery_fixture/README.md).
Use its operator procedure for reset, commit confirmation, worker interruption,
response release, and ledger grading. The runner records fixture receive/commit
times and worker process-group termination. Its independent ledger records
requests rejected by fixture origin/token checks separately from accepted
dispatches and committed effects; the grader checks the reported dispatch
count against that ledger. It cannot count browser actions that never reach the
fixture or inspect product-native task records and host journals. Review
participant source and observe the browser interaction because the fixture
cannot prove that the participant used a human UI action.

## Measurement notes

Record time to first verified outcome, total task time, assistance requests,
participant-authored integration LOC, dispatch count, final ledger count,
whether `unknown` was preserved before reconciliation, and whether a retry was
attempted. Mark each owner/session/target/policy/deadline/postcondition/recovery
control as native, configured, or participant-authored glue. Keep setup time,
browser installation, dependency download, and task execution boundaries
separate. Do not compare one product's wall-clock time to another product's
without matching what the timer includes.

This card defines the intended task, not a completed product comparison. The
Scout and Stagehand adapter commands under
`examples/adoption_recovery_fixture/adapters` are facilitator rehearsals, not
participant solutions. Scout uses its native durable task store; the Stagehand
adapter uses a host-authored journal to rehearse an equivalent state and
resolution workflow. That journal is study adapter code, not an SDK feature,
and must not be reused as participant starter code.
The current tokenized Scout and Stagehand facilitator rehearsals pass the
nominal control and faulted path, but use prewritten adapters. Participant
implementations, clean checkouts, and a second-person procedure rehearsal are
still required before recruitment.
