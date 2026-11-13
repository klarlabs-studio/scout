# Embedding Scout in a Go service

The Scout Go module requires Go 1.26 or later. The examples below use the
repository's selected dependency graph; pin Scout and its toolchain together
in service builds.

For a minimal task-level starting point, run [`../examples/task_run`](../examples/task_run/main.go). It shows a bounded Go context, a temporary local fixture, an explicit final postcondition, and a durable task store in one small program. It then repeats the same task ID and plan and confirms that Scout returns the stored result without a second fixture commit (recorded smoke (local audit artifact)). By default the example uses a temporary store. Pass `-task-store=/path/to/tasks.db` to retain records; each invocation uses a fresh task ID because it starts a new fixture. A separate stateful CLI process successfully read a retained completed task after two example processes exited (reopen smoke (local audit artifact)). Production services must retain their store for the full retry window. For process-loss recovery and host-side reconciliation, see the [durable recovery start guide](../examples/durable_task_recovery/README.md) and its [runnable fixture](../examples/durable_task_recovery/main.go). That example writes one JSON run summary to stdout and human-readable progress to stderr (recorded run (local audit artifact)).

Scout's Go package fits applications that already own their job queue,
authentication, tracing, and deployment policy. Keep one browser session scoped
to one worker or request, pass the caller's context through startup and every
operation, and close the session when that unit of work ends.

The low-level `Page` API has context variants for navigation, waiting,
evaluation, HTML/URL reads, screenshots/PDFs, and viewport changes. Prefer
these variants when several browser calls belong to one request or job so its
deadline covers the whole unit of work. Every `Page.CallContext` and
`Page.EvaluateContext` call is also bounded by the page lifetime: after the
page is closed or detached, a still-live caller context cannot issue another
CDP command through that page. Cookie reads and mutations, user-agent changes,
and element screenshots also provide context variants. Cookie mutations stop
if the Network domain cannot be enabled, rather than sending a follow-up
mutation after setup failed.

## OpenTelemetry

For an embedded Go service, configure and own the SDK in the host, then pass its
provider through `agent.SessionConfig.TracerProvider`. Scout records compact
action and session lifecycle spans and omits page text, URLs, selectors, form
values, and evidence from span attributes by default.

The standalone MCP server can export the same spans with the opt-in `--otel`
flag:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 \\
  scout mcp serve-http --otel
```

This enables the OTLP/HTTP trace exporter; standard `OTEL_EXPORTER_OTLP_*`
variables configure its endpoint, headers, compression, and timeout. Scout
flushes the provider when the MCP server returns. Without `--otel`, the CLI
does not create an exporter or send telemetry.

## Worker pool

[`../examples/worker_pool/main.go`](../examples/worker_pool/main.go) is a
runnable example with a bounded worker count, one independent browser session
per worker, a shared cancellation context, and signal-aware shutdown. It checks
each action receipt's schema version, run ID, operation ordinal, target ID,
policy ID, outcome, and verification state before reporting the job complete:

```sh
go run ./examples/worker_pool -workers 2 -jobs 4
```

The example writes exactly one JSONL result per submitted job, keyed by
`job_id`. Stable `status`, `phase`, and `error_code` fields support routing and
metrics; diagnostic messages remain human-readable. Jobs stopped before
dispatch are emitted as `not_started`, and the process exits nonzero if any job
fails. See the [worker pool guide](../examples/worker_pool/README.md) for its
failure and output contract.

For a clean Linux container setup, the same example has a Compose recipe that
starts the pinned `headless-shell` browser image and attaches two workers over
CDP, with an isolated browser context per worker. Compose places the worker and
browser in a shared network namespace because Chrome's DevTools discovery URL
advertises a loopback WebSocket address; the worker and browser still run in
separate containers and the browser service remains separately owned:

```sh
docker compose -f examples/worker_pool/compose.yaml up --abort-on-container-exit --exit-code-from worker
```

The browser is a separate service; Scout disconnects its worker sessions on
exit and does not own or stop the browser container. The browser's CDP port is
not published to the host. Remove the containers with `docker compose
-f examples/worker_pool/compose.yaml down`.

Each worker's page policy is restricted to the fixture origin. The built-in
loopback fixture enables private-address access because it is created by the
example itself. A caller-supplied `-fixture-url` does not enable private IPs by
default; pass `-allow-private-ips` only when that URL points to a trusted,
controlled fixture on a private network. Do not use that opt-in for arbitrary
URLs or page content.

When `SCOUT_REMOTE_CDP` is set and more than one worker is requested, the
example refuses to start unless `-isolate-remote-contexts` is also supplied.
Each worker gets an ephemeral remote BrowserContext; closing the worker
disconnects Scout and disposes that context while leaving the browser process
owned by Compose.

The first clean Compose run completed all four verified jobs across two distinct
targets. The pinned versions, predicates, limitations, and raw output are in
`examples/worker_pool/reports` (local audit artifact).
This is a recipe smoke result, not a timing, resource, or adoption study.
The 12 November rerun against Go 1.27.4 verified all four jobs and confirmed
that each remote-CDP receipt carries only the fixture origin; its raw log and
machine-readable summary are
`compose-origin-policy-2026-11-12` (local audit artifact).
This remains a single local run and does not verify remote-host egress policy.

The example emits one JSON result per processed job. `completed` includes the
verified action receipt; ordinary failures are reported as `failed` and do not
cancel unrelated workers. An ambiguous dispatched mutation is reported as
`needs_reconciliation` with a portable recovery record. That worker stops
accepting new jobs because its page may have changed; the host can pass the
record to `ReconcileActionRecoveryRecordContext` while the same browser and
target remain available. The sample returns a nonzero process status if any
job failed or needs reconciliation. The complete run contract, safety flags,
remote CDP recipe, and evidence boundary are in the
[`worker_pool` README](../examples/worker_pool/README.md).

Use the same pattern in a service worker. Bound the worker count and the
request/job deadline separately: the first caps concurrent browser processes;
the second limits how long a caller can consume one. If a request is canceled
after an action was dispatched, treat an unknown outcome as a reconciliation
case. Inspect the returned target-bound recovery evidence before deciding
whether to retry a non-idempotent action.

Every Scout-created `ActionReceipt` now includes the same versioned `policy_id`
that appears in session status and task-run results. Hosts can correlate a
single mutation with the exact origin/private-address policy active when Scout
created its receipt. A policy digest identifies configuration; it does not
prove that every browser egress channel was enforced.

`Session.ReconcileActionOutcomeContext` accepts an outcome only in the Scout
run that created it. If the process or session was restarted, persist the
versioned `ActionRecoveryRecord` and pass it to
`Session.ReconcileActionRecoveryRecordContext`; that explicit portable path
retains the original run ID as provenance and still requires the exact original
browser target to be live in the same underlying browser instance. It cannot
reconcile across a browser reset or restart that replaced the target. The CLI's
`reconcile --recovery-file` and stateful CLI `reconcile` command use this
portable path. A target ID match does not prove the same page contents or
application state, so only a saved postcondition can verify completion; without
one, reconciliation reports the state as unknown for caller inspection.

### Resuming a live remote session after process restart

For a service that must reconnect to a browser after its Scout worker restarts,
use an authenticated owner scope and a durable `agent.SessionRecoveryStore`.
`BoltTaskRunStore` implements both task-run persistence and session recovery in
one database. The owner scope must come from the service's authenticated
identity mapping (a keyed hash is preferable); never accept it directly from
an untrusted browser-tool request. Keep the current remote CDP endpoint and
origin policy in trusted service configuration.

```go
store, err := agent.OpenBoltTaskRunStore("/var/lib/my-service/scout-state.db")
if err != nil {
	return err
}
defer store.Close()

policyDigest, err := agent.SessionRecoveryPolicyDigest(sessionConfig)
if err != nil {
	return err
}
handle, err := agent.NewSessionRecoveryHandle()
if err != nil {
	return err
}
record, err := session.PersistRecoveryRecord(
	ctx, store, handle, authenticatedOwnerScope, "checkout-worker",
	policyDigest, time.Now().Add(24*time.Hour),
)
if err != nil {
	return err
}
_ = record // return the opaque handle to the same authenticated owner

resumed, record, err := agent.ResumeSessionContext(
	ctx, sessionConfig, store, handle, authenticatedOwnerScope, policyDigest,
)
```

The handle is an identifier, not a credential. Resume requires the caller's
authenticated owner scope and the current trusted policy digest. Scout attaches
to the recorded target and checks the remote browser fingerprint before
returning it; missing, foreign, expired, closed-target, or policy-mismatched
handles fail closed. `ResumeSessionContext` validates the returned handle,
owner, record shape, expiry, and policy digest before opening a browser, so a
custom store cannot make an expired or mismatched record resumable by returning
it from a lookup. The browser must remain running with that exact context and
target. Shared and Scout-isolated remote BrowserContexts are supported;
local browsers and Scout-managed proxy sessions are not. An isolated context is
preserved when the recovery record is created and is disposed when its recovery handle or owning default session is explicitly
revoked through the host's current trusted endpoint. Session close enumerates
owner-scoped records, disposes each distinct context, then removes the handles;
the bulk revoke path validates every listed record's owner, session, and shape
before disposing any context. Failed cleanup retains the affected records for
retry. If persisting an isolated context fails during ownership transfer, Scout
attempts record rollback with a bounded detached deadline and reports rollback
failure instead of silently leaving a possibly resumable handle. The durable
record contains the opaque CDP BrowserContext ID; protect
the bbolt file and backups as sensitive state. The built-in store uses
owner-only filesystem permissions but does not encrypt data at rest. For multi-replica cleanup, implement `ExpiredSessionRecoveryClaimStore`: claim
one expired handle with a lease, dispose any referenced isolated context
through the current trusted endpoint with `DisposeSessionRecoveryContext`
(it verifies the browser fingerprint first), then atomically complete the claim.
Release failed claims for a later sweep and report cleanup failures to operators.
`ListExpiredSessionRecoveryRecords` is an inspection API;
`DeleteExpiredSessionRecoveryRecords` is only suitable when no external browser
resources need cleanup.

### Persisting task idempotency

`SessionConfig.TaskRunStore` lets a service persist task-ID reservations and
completed `TaskRunResult` values in its database. For a single-process Go
service, Scout includes `OpenBoltTaskRunStore`; clustered services can
implement the interface with their transactional database. For example:

```go
taskRuns, err := agent.OpenBoltTaskRunStore("/var/lib/my-service/scout-tasks.db")
if err != nil {
	return err
}
defer taskRuns.Close()

session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
	Headless: true,
	TaskRunStore: taskRuns,
})
```

The store contains task results and browser evidence, so protect and back it
up as application data. `BoltTaskRunStore` is single-process-owner; use a
shared database implementation for multiple service processes. The store must make
`ReserveTaskRun` atomic across every process that can execute the same task
IDs, retain completed records for the full caller retry window, and never
silently evict an in-flight reservation. Scout reserves the ID before browser
work, then persists the browser-instance and target IDs before dispatch. A
matching completed ID returns its stored result; a different plan using that
ID is rejected. If the process exits while a reservation is in-flight, a later
attempt is rejected and the host can inspect the bound target and reconcile
application state before resolving that reservation. Scout does not infer
that the action was safe to replay.

Use `session.GetTaskRunContext(ctx, taskID)` to read the retained record without
changing it. MCP exposes the same lookup as `get_task_run`; the stateful CLI
accepts `{"command":"get_task_run","task_id":"checkout-42"}`. A missing
record returns `found: false`. An `in_flight` record identifies the reservation
and bound browser target, but does not establish whether the browser action took
effect. Reconcile application state through the host's workflow before choosing
a new task ID or plan.

Treat the returned `TaskRunRecord` as sensitive browser state. Its saved result,
error, and host resolution notes can include page-derived text, URLs, selectors,
evidence, or private application details. The plan is redacted and
non-executable, but that does not redact the sibling `result` field. Apply the
host's retention and access controls before storing, logging, or exporting the
full record. The portable recovery receipt is a separate artifact with its own
redaction rules; it is not a sanitized serialization of the complete task
record.

For the stateful CLI, `recover_task_run` performs read-only inspection. Send
`{"command":"recover_task_run","task_id":"checkout-42"}` to receive the
same sensitive task record, including its redacted plan, durable cursor,
fingerprint, browser, and target bindings, plus a portable receipt when one is
available. The receipt's redactions do not apply to the full `record` object.
For host
reconciliation, first inspect authoritative application state, then send a
separate `resolve_task_run` request with the plan fingerprint and outcome. Use
`unresolved` when state is not yet available; this appends an observation,
keeps the reservation in flight, and permits a later observation. Resolution
history is retained. Neither command executes browser actions. A terminal
conclusion keeps the task ID reserved; use a fresh task ID for follow-up work.

For a host-confirmed `rejected` outcome, keep two identities separate. The
Scout task ID permanently guards the interrupted browser plan and must not be
reused, even when the authoritative application ledger shows no effect. If the
application supports idempotency keys, the host may create a new Scout task ID
while preserving the same application idempotency key for the intended
business operation. The application must enforce that key atomically; Scout's
task store does not know or enforce business idempotency. Recheck the
authoritative ledger after the follow-up and treat any mismatch as unresolved.
`examples/durable_task_recovery` demonstrates both branches. Its recorded
zero-effect run (local audit artifact)
kills a worker after the fixture receives the first request but before it
commits an effect, confirms zero in the authoritative ledger, resolves the
original Scout task as `rejected`, verifies same-ID reuse remains blocked, then
uses a new Scout task ID with the same application key. A second distinct Scout
task submits that key again; the fixture returns the prior application result
without increasing its effect count. This validates the local contract path;
it is not a production database integration or a reliability estimate.

The recovery response also carries `guidance` with a stable recommendation,
rationale, prerequisites, and `replay_authorized: false`. A missing record asks
you to verify store and namespace scope; an initial cursor asks you to confirm
the prior worker stopped and reconcile authoritative application state because
a custom store without per-action progress may retain that cursor after
dispatch. A possibly dispatched or incomplete task also asks for authoritative
reconciliation. A saved completed
result asks you to inspect its verification evidence. A retained uncertain result
asks you to inspect the result and authoritative application state. Once the host
records an observation, that latest observation supersedes the prior task result
in the current record while append-only `resolution_history` preserves earlier
observations. The field is the same in Go, stateful CLI, and MCP and never claims
business success from browser state.

The same read-only `recover_task_run` view is available in MCP and through the
Go API `Session.RecoverTaskRunContext`. For an interrupted task, the receipt
covers only the currently started step; a missing receipt does not hide the
task record. MCP and CLI accept an optional `operation_ordinal` to select a
saved action. The view never resolves the task or authorizes replay. MCP and
CLI `resolve_task_run` remain separate host attestations after authoritative
application reconciliation.

Stateful CLI JSONL responses are capped at 256 KiB, including error and partial
result envelopes. If an encoded response exceeds the cap, Scout emits a small
`SCOUT_OUTPUT_LIMIT` error response with `output_omitted`, `truncated`,
`original_bytes`, and `max_bytes`; it preserves the request ID and any action
`recovery_record`, but omits the oversized result/partial result. When the
serialized envelope is at most 16 MiB and artifact capacity is available, it
also returns an opaque artifact ID, byte count, and SHA-256. Read it while the
same `scout session` process is running with
`{"command":"read_output_artifact","artifact_id":"ID","offset":0,"limit_bytes":131072}`.
The response contains a base64 chunk, offset, total bytes, truncation flag, and
digest; advance the offset until complete and verify the assembled bytes against
the digest. Artifacts use mode `0600`, live in a mode `0700` temporary directory,
and are deleted when the session process exits. The limits are 32 artifacts and
64 MiB total per session. This caps emitted output, but does not make an already
materialized large browser result cheap in memory. If the request ID or recovery
record alone would overflow the compact notice, Scout marks that field omitted;
the complete original envelope remains in the artifact when it is within the
artifact limits.

`CompleteTaskRun` and pre-dispatch reservation cleanup use a detached context
with a five-second persistence deadline so caller cancellation does not
prevent recording the final browser outcome. A persistence error is returned together with the
available task result; the host should treat the durable state as uncertain
until it reads its store. Supply a store only when this failure mode and
retention policy are implemented. The default remains session-local memory,
which does not protect retries after session or process loss.

After restart, `GetTaskRun` reads a durable record without changing it. If it
is still in flight, inspect the recorded browser instance and target through
the host's browser/application controls. Once reconciled, call
`Session.ResolveInterruptedTaskRunWithEvidenceContext` with the record's exact
fingerprint and a short non-secret description of the authoritative
application evidence. Terminal `verified_complete` and `rejected` conclusions
must also include the source system, an opaque source record/event reference,
and the RFC3339 timestamp when that source was read. This keeps a claim tied
to where and when the host checked it. `unresolved` records
that the host cannot yet determine the business outcome, keeps the reservation
in flight, and allows a later observation to be appended. A manual bbolt
close/reopen probe verified that unresolved state and its history persist.
Terminal conclusions do not claim Scout verified the page, and the ID remains
unavailable for execution. Use a new ID for follow-up work. The record retains
the latest resolution and append-only `resolution_history`.
`Session.ResolveInterruptedTaskRunContext` remains available for unresolved
observations; terminal outcomes require the evidence-aware method. MCP
`resolve_task_run` accepts `evidence_source`,
`evidence_reference`, and `observed_at`; stateful CLI recovery accepts the
corresponding `resolution_evidence_source`, `resolution_evidence_reference`,
and `resolution_observed_at` fields. References must be opaque and non-secret.
These are host-provided provenance labels, not independently verified evidence.

### Recovery contract across Go, CLI, and MCP

The three entry points share one recovery model and `TaskRunRecord` shape. The
Go host calls `GetTaskRunContext`, `RecoverTaskRunContext`, and
`ResolveInterruptedTaskRunWithEvidenceContext`. The stateful CLI exposes those
operations as `get_task_run`, `recover_task_run`, and `resolve_task_run`; MCP
uses the same tool names. Resolution fields map directly to the Go
`TaskRunResolution` fields:

| Go field | MCP input | Stateful CLI JSONL input |
|---|---|---|
| `Status` | `status` | `resolution_status` |
| `Evidence` | `evidence` | `resolution_evidence` |
| `EvidenceSource` | `evidence_source` | `resolution_evidence_source` |
| `EvidenceReference` | `evidence_reference` | `resolution_evidence_reference` |
| `ObservedAt` | `observed_at` | `resolution_observed_at` |

All three require the exact task ID and plan fingerprint. Terminal resolutions
also require source, reference, and observation time; unresolved observations
may omit those fields while the host is still gathering evidence. They retain
unresolved observations and keep terminally resolved IDs reserved. Recovery is read-only
and never replays browser work. MCP wraps the shared result in its normal tool
envelope; the stateful CLI returns the view as its JSONL result. Profile
availability is the only deployment-level difference: MCP requires the active
profile to expose both `recover_task_run` and `resolve_task_run` before starting
a task.

The optional `recovery` field on a built-in Bolt record identifies the action
manifest and the last durable cursor. Its `phase` distinguishes action
execution, final-postcondition verification, and result persistence; selector
based postconditions include their target selector. The manifest records
condition types but never expected values or text.
If the process stopped during final verification or result persistence, check
the authoritative application state before resolving the reservation.
MCP exposes this operation as `resolve_task_run` in the `core`, `interact`,
`diagnose`, and `admin` profiles; the stateful CLI accepts a
`resolve_task_run` command. MCP checks both recovery tools before every
`task_run`, even when `required_tools` is omitted. That field can declare
additional follow-up tools. A restricted profile missing either recovery tool
rejects the task before browser dispatch. Custom stores used with final postconditions should implement the optional
`TaskRunUncertainResultStore` to preserve the uncertain result and keep the
replay guard active. Stores that need host reconciliation should also implement
`TaskRunResolutionStore`; without it, `resolve_task_run` returns an explicit
unsupported-store error. The Bolt adapter
retains up to 8,192 IDs and never prunes automatically; provision a new database
only after the application's retry window for the old database has expired.

[`examples/durable_task_recovery`](../examples/durable_task_recovery/main.go)
demonstrates the host boundary with separate Scout processes. Its receiving
process reads an independent fixture `/ledger` endpoint keyed by the task ID
and refuses to record `verified_complete` unless exactly one accepted mutation
matches that operation key. It also presents the original signed receipt to a receiver
attached to a replacement Chrome generation; the receiver rejects same-page
inspection (`same_context: false`) and keeps replay disabled before the host
reconciles from the ledger. The fixture is deliberately local and deterministic;
production hosts must query their own authoritative business system and supply
trustworthy evidence provenance.

The stateful CLI and MCP server can opt into the same local store with
`scout session --task-store=/var/lib/scout/task-runs.db`,
`scout mcp serve --task-store=...`, or
`scout mcp serve-http --task-store=...`. The parent directory must already
exist. MCP prefixes task IDs internally using the principal and named session
when principal mapping is enabled. Without a principal, HTTP records are scoped
to the transport session and may not deduplicate after the client reconnects
with a new transport session ID. The file contains task results and browser
evidence; protect the database and its parent directory as application data.

## Browser ownership

`agent.NewSessionContext` can launch a local Chrome browser or attach through
`SessionConfig.RemoteCDP`. Scout owns and closes a browser it launches. For an
attached remote browser, set `RemoteCDP`; `Session.Close` disconnects Scout and
leaves the caller-owned browser running. See
[`../examples/remote_cdp/main.go`](../examples/remote_cdp/main.go) and
[remote CDP setup](../README.md#remote-browser-connection).

`Session.StatusContext` reports `browser_ownership`, whether the session is
configured for an isolated remote BrowserContext (`browser_context_isolated`), `endpoint_source`,
the effective `allowed_origins`, `allow_private_ips`, `policy_enforcement`, a versioned `policy_id`,
and a safe `browser_endpoint` label for remote CDP. The ID is a digest of the
normalized page-origin and private-IP policy, stable across ordering and
default-port spelling;
it identifies configured policy and is not proof that every browser network
channel is covered. The label contains only scheme,
host, and port; it omits credentials, DevTools paths, query tokens, and
fragments. Treat it as an operator diagnostic, not as an authorization identity
or proof that two endpoints have isolated browser state.

An injected browser passed to `NewSessionFromBrowser` is owned by the session's
close path. If that handle refers to a remote browser that must stay running,
configure the session as remote CDP and verify the adapter's disconnect
semantics. Multiple Scout clients attached to the same external browser share
state unless each session opts into an isolated remote BrowserContext. A task
run serializes calls made through its `Session` and rechecks the captured page
URL immediately before dispatch, but it cannot lock out a direct CDP client or
another process attached to the same target. The host must provide exclusive
target control for the duration of a task; when that cannot be guaranteed,
separate browser contexts and restrict access to the DevTools endpoint.

## Human handoff

Use `HandoffToHumanContext` when a person must complete sign-in, 2FA, consent,
or another gated step. The callback belongs to the host, so the host can open
its own live viewer or approval flow without Scout owning that transport. Scout
passes the run ID, current CDP target ID, a URL with credentials/query/fragment
removed, and an optional reason. It releases the session lock while the callback
runs, then confirms the active target is still the one handed off and can check
one application postcondition before returning a target-bound receipt.

```go
result, err := session.HandoffToHumanContext(ctx, "Complete sign-in", func(
	ctx context.Context, request agent.HumanHandoffRequest,
) error {
		return viewer.WaitForApproval(ctx, request.TargetID)
	}, &agent.Postcondition{Text: "Account overview"})
if err != nil {
	return err
}
if !result.Verified {
	return fmt.Errorf("human step completed but the expected page state was not verified: %s", result.Evidence)
}
```

The callback must honor its context. Without a postcondition, callback
completion is reported as unverified. Callback errors, inability to reacquire
the browser, and target changes return `outcome_unknown` with the original
target ID and inspect-before-retry guidance. Credential values are not
parameters in this API or its receipt; other Scout artifacts retain their
documented page-data handling rules.

[`../examples/human_handoff/main.go`](../examples/human_handoff/main.go) shows
the local headed-browser flow. Run it with a URL and expected post-login text:

```sh
go run ./examples/human_handoff -url https://example.com/login -expect 'Account overview'
```

The example requires a graphical session and is intended for interactive
development, not a headless server deployment.

### Sharing one authenticated remote browser

`SCOUT_MCP_HTTP_PRINCIPALS_JSON` can route authenticated principals to dedicated
remote endpoints or create one isolated BrowserContext per principal on a
shared Chrome endpoint. Set `isolate_browser_context` to `true` for every
principal sharing that endpoint. Scout creates a context per session, restricts
target discovery and creation to it, recreates it on reset, and disposes it when
that session closes. Do not use the MCP session ID as an application identity;
it identifies transport lifecycle and is bound to the principal authenticated
when the session is created.

For example, a deployment secret can contain this JSON and the process can
listen on a private service interface:

```json
{
  "tenant-a": {
    "token": "<unique-secret-a>",
    "remote_cdp": "https://browser-gateway.internal/tenant-a",
    "allowed_origins": ["https://app-a.example"],
    "proxy_server": "http://tenant-a-proxy.internal:8080"
  },
  "tenant-b": {
    "token": "<unique-secret-b>",
    "remote_cdp": "https://browser-gateway.internal/tenant-b",
    "allowed_origins": ["https://app-b.example"]
  }
}
```

```sh
# Inject SCOUT_MCP_HTTP_PRINCIPALS_JSON from your deployment secret manager.
scout mcp serve-http --listen=0.0.0.0:4201 --profile=core
```

The JSON field names are `token`, `remote_cdp`, `allowed_origins`,
`allow_private_ips`, `isolate_browser_context`, and the optional
`proxy_server`. Every principal requires a unique non-empty token, a remote
CDP endpoint, and at least one exact HTTP(S) origin. `proxy_server` accepts an
absolute HTTP(S) or SOCKS5 proxy URL without credentials or a path. For that
principal's separately launched local named sessions, it overrides
`SCOUT_PROXY_SERVER`; when omitted, named sessions use `SCOUT_PROXY_SERVER` if
configured. The authenticated principal's default session still uses its
remote CDP browser: Scout does not apply this proxy to that browser, so the
remote browser host must enforce and verify its own egress policy. The HTTP(S)
listener requires this principal map or
`SCOUT_MCP_HTTP_TOKEN` when bound beyond loopback; do not configure both.
Use distinct origins and dedicated browser endpoints when possible. If
endpoints can alias the same Chrome, set `isolate_browser_context: true` for
every principal that may reach it. Keep tokens in a secret manager and rotate
them by updating the secret and restarting the server; policy is loaded only
at startup. Put TLS and network access controls at the service boundary.

For a multi-tenant deployment, either assign a dedicated endpoint/profile to
each principal or enable `isolate_browser_context` for every principal sharing
one endpoint. The contexts isolate cookies, localStorage, and tabs while the
Chrome process remains caller-owned. They do not share login state across
principals. Target ID attachment (`SessionConfig.TargetID`) is not available
together with isolated contexts because target IDs are scoped to the owner
context. Keep credentials out of URLs and logs, and ensure cleanup only closes
resources Scout owns.

The principal map rejects duplicate normalized `remote_cdp` URLs unless both
principals enable `isolate_browser_context`. This catches direct assignments,
including case and trailing-slash differences. It cannot detect aliases,
reverse-proxy routes, or distinct URLs that ultimately attach to one Chrome;
every principal on that shared endpoint must enable context isolation. Remote
CDP providers must support Chromium's `Target.createBrowserContext` and
`Target.disposeBrowserContext` commands.

For independently trusted agents or workers, configure a distinct principal
ID and bearer token for each agent. Stateless requests using the same token
resolve to the same stable principal owner; an opaque `session_id` selects a
session inside that owner and does not create another authorization boundary.
When those principals share a Chrome endpoint, enable
`isolate_browser_context: true` for every principal. This keeps each agent's
cookies, localStorage, and tabs separate while preserving that agent's session
across stateless requests. Do not share one stateless principal token among
agents that require isolation from each other.

The focused fixture `SCOUT_BENCHMARK_FOCUS=mcp-http-shared-remote-ownership`
starts one Scout HTTP server and one externally owned Chrome, then attaches two
HTTP clients to the same remote CDP endpoint. A run on 6 November 2026
confirmed that both clients selected the same target ID: client B read the
cookie and localStorage seeded by client A, and client A then read B's update.
The captured one-run report is
`scout-mcp-http-shared-remote-ownership-2026-11-06.json` (local audit artifact).
That fixture captures the legacy shared-context behavior. The focused
`mcp-http-principal-isolation` scenario now assigns two principals to the same
Chrome endpoint with isolated contexts and verifies distinct targets, empty
cross-principal cookies/localStorage, origin enforcement, transport ownership,
session cleanup, and isolated context recreation after reset. Repeat against
each provider and browser version before deployment because context-command
support varies.

[`../examples/principal_browser_gateway/main.go`](../examples/principal_browser_gateway/main.go)
shows the server-side routing shape for a Go host: trusted authentication
middleware places a verified principal in request context, and a fixed
server-owned map selects that principal's CDP endpoint. The sample configuration
is supplied as `SCOUT_BROWSER_ENDPOINTS_JSON`, for example
`{"tenant-a":{"remote_cdp":"http://127.0.0.1:9222","allowed_origins":["https://app-a.example"]},"tenant-b":{"remote_cdp":"http://127.0.0.1:9223","allowed_origins":["https://app-b.example"]}}`.
It intentionally does not read an identity header itself, and it listens on
loopback only. The example keeps one Scout session per configured principal,
serializes that principal's browser requests, disconnects sessions after 30
minutes without use, and exposes `Close` for application shutdown. Closing a
remote session only disconnects Scout from that principal's browser. The pool
is bounded by the trusted principal policy map; applications with a dynamic
principal set should add an explicit capacity and eviction policy. The configured
`AllowedOrigins` constrains Scout navigation, redirects, and intercepted page
requests, but it does not replace remote-host network egress policy. Add the
host's real auth middleware before exposing any handler to a caller.

`go test ./examples/principal_browser_gateway -run TestPrincipalPoliciesIsolateRemoteBrowserState`
is an executable isolation check: it starts two disposable Chrome processes,
routes two test principals to their separate CDP endpoints, and verifies that
same-origin cookie/localStorage writes remain separate and target IDs differ.
The test explicitly enables private-IP navigation only for its local fixture;
production policy should leave `allow_private_ips` false unless private network
access is deliberately required.

### MCP HTTP protocol compatibility

`scout mcp serve-http` uses initialize-based stateful Streamable HTTP by
default. This lets Scout bind browser sessions and private artifacts to the
transport session. An opt-in prototype supports the 2026-07-28 stateless
`server/discover` flow when `SCOUT_MCP_HTTP_STATELESS=true` and configured
principal authentication is enabled. Stateless calls use an opaque Scout
`session_id` in per-call `_meta`; a transport option alone would remove
`Mcp-Session-Id` without providing an equivalent owner-scoped selector.

The stateless path has a Chrome-backed HTTP integration for modern discovery,
required per-call opaque handles, same-principal reconnect with a stale
`Mcp-Session-Id` header, and foreign-principal denial. It also confirms private
page snapshots remain readable after reconnect, are inaccessible to another
principal, and are removed when their owning session closes. It also confirms
Alice and Bob can run the same caller-selected task ID in one durable store
without seeing or replaying one another's records. Repeated concurrency,
same-principal session isolation, cleanup under disconnect or browser failure,
restart/load-balancer behavior, and target-client compatibility still need
validation. Keep stateful mode as the default and
benchmark the prototype with target clients before relying on it for production
workflows. See the
[stateless implementation notes](product-vision.md#mcp-protocol-direction).

For a shared Streamable HTTP service, Scout also supports
`SCOUT_MCP_HTTP_PRINCIPALS_JSON`, which maps each bearer token to a stable
principal ID and a server-configured `remote_cdp` endpoint. Scout binds each
MCP transport session ID to the first authenticated principal that uses it,
rejects attempts by another principal to reuse it, scopes registries and
configuration by both values, and limits `DELETE` cleanup to that principal's
session. Set `isolate_browser_context` for every principal assigned to a shared
remote endpoint. Each principal must also configure exact `allowed_origins`; private
and loopback destinations are blocked unless that principal explicitly enables
`allow_private_ips`. The `configure` tool cannot widen that server-side private
network policy. These origin checks cover Scout's navigation and intercepted
page requests, not every browser network channel or the remote host's egress.
Do not infer identity from `Mcp-Session-Id` or let callers choose arbitrary
endpoint URLs. Configure a dedicated remote browser endpoint/profile per
principal or set `isolate_browser_context` for every principal sharing one
endpoint. Principal contexts are ephemeral and do not share an authenticated
profile with one another.
`SCOUT_MCP_HTTP_TOKEN` remains available for a single
shared identity and cannot route callers to different browser endpoints.
The principal map is loaded at startup. Revoke a token or change an endpoint by
updating the deployment secret/configuration and restarting Scout; the current
process does not hot-reload principal policy. Coordinate the restart with active
work, since server shutdown releases Scout sessions and disconnects from remote
browsers without stopping those externally owned browsers.

The opt-in stateless prototype uses a client-carried Scout session handle
bound to the authenticated principal. It does not provide one hybrid endpoint
for both protocol eras; keep the stateful endpoint as the default. See the
[competitive opportunities research](competitive-opportunities.md#mcp-stateless-transport-compatibility-6-november-2026)
for the protocol matrix and remaining validation gates.

## Contexts and shutdown

Use context-aware APIs for service work:

```go
ctx, cancel := context.WithTimeout(request.Context(), 45*time.Second)
defer cancel()

session, err := agent.NewSessionContext(ctx, agent.SessionConfig{
	Headless: true,
	Timeout:  15 * time.Second, // browser operation/startup ceiling
})
if err != nil {
	return err
}
defer session.Close()

if _, err := session.NavigateWithPostconditionContext(ctx, targetURL,
	agent.Postcondition{URLContains: "/dashboard"}); err != nil {
	return err
}
```

The caller context bounds startup and context-aware work; a session that starts
successfully is independent of the startup context lifetime. Keep it alive for
the operations that need it. On shutdown, stop accepting work, cancel worker
contexts, wait for workers to return, and close sessions with a separate
cleanup deadline. `Session.Close()` preserves the unbounded compatibility
behavior; `Session.CloseContext(ctx)` bounds waiting for the session lock,
screen-recorder shutdown, and CDP page close/detach. For Scout-launched Chrome,
it requests `Browser.close`, closes Scout's CDP socket, waits for the process
until the deadline, and then force-kills the browser process as a fallback. For
remote CDP, it disposes a context created by that connection before
disconnecting Scout. A context transferred to durable recovery or reattached
from a recovery record is detached without disposal; revoke its recovery handle
through the trusted host configuration to dispose it. If owned-context cleanup
hits an expired caller deadline, disposal gets a separate 500 ms best-effort
window and returns any failure. A custom
browser supplied through `NewSessionFromBrowser` must implement a bounded
`Close` itself because the legacy `browse.Browser` interface has no
context-aware close method. If cleanup expires while another operation still
holds the session lock, `CloseContext` returns the deadline error without
taking the browser handle; retain a process-level termination deadline as a
final fallback. The worker-pool example uses a two-second close context per
session.

## Policy and egress

`AllowedOrigins` checks exact origins for navigation, redirects, and
page-target requests Scout can intercept. It does not cover service-worker
traffic, WebSocket handshakes, traffic that bypasses the browser proxy, or
requests made by a remote browser host. For browser-wide HTTP/HTTPS/WSS host
and port enforcement, configure an egress proxy in the browser's network
environment. The optional
[`../examples/egress-proxy/README.md`](../examples/egress-proxy/README.md)
documents a mitmproxy allowlist and its limits. Use network policy or a
firewall where destination-IP enforcement is required. For locally launched
browsers, `ProxyServer` disables Chromium's implicit loopback bypass, applies
`disable_non_proxied_udp` to WebRTC, and prevents local browser DNS lookups
except for resolving the proxy host. The proxy host's own resolution and all
other host processes remain the deployment's responsibility. Remote CDP
browsers require these controls on their host.

URL denials return `*browse.URLPolicyError`, which callers can inspect through
`errors.As`. Its stable `Code` values distinguish invalid URLs, blocked
schemes, denied origins, blocked private addresses, and unverifiable addresses.
`PolicyID` correlates the denial with the effective session policy; page-target
navigation also fills `TargetID` and a safe `EvidenceReference` such as
`target:<id>`. CLI and MCP error envelopes expose the same code, `policy_id`,
`target_id`, and `policy_evidence_ref` fields. Messages sent through those
envelopes are generic and do not include the attempted URL. Trace policy
decisions include reason code and policy ID; exported URLs are redacted. These
fields attribute Scout's page-level decision and do not imply full browser
egress coverage. Go navigation error text also omits the attempted URL when
the failure is a policy denial; use the typed error fields for attribution.

## OpenTelemetry

Pass the application's `trace.TracerProvider` in `SessionConfig`. Scout emits
`scout.browser.action` spans for receipt-covered operations and keeps page
content, URLs, selectors, and form values out of span attributes. The host
application owns SDK setup, exporters, sampling, and resource attributes. When
a valid span context is available, the action receipt returns `otel_trace_id`
and `otel_span_id` separately from Scout's trace archive ID. Passing that
receipt to reconciliation adds a span link to the original action, including
when reconciliation runs under another caller trace. See the
[OpenTelemetry section in the README](../README.md#opentelemetry).

## Runtime and deployment

The Scout Go binary does not bundle Chrome. Install a compatible browser in
the runtime image and make its executable available to Scout. For local
sessions, budget memory and process limits for each concurrent browser; the
worker count is also a browser-process limit. Remote CDP moves browser
resources and egress policy to the remote host, but adds endpoint availability
and ownership requirements. Scout does not provision or monitor that host.

Before production rollout, run the examples and benchmark fixtures in the same
container image and browser build used by the service. Measure process count,
memory, startup latency, action p95, timeout and reconciliation rates, and
shutdown duration under the intended concurrency. The deterministic examples
verify API contracts; they do not predict site-specific reliability or resource
consumption.

### Cancellable SPA route waits

Use `Session.WaitForRouteChangeContext(ctx, timeout)` when the host must propagate its request deadline into a client-side route wait. The legacy `WaitForRouteChange(timeout)` wrapper keeps its configured timeout behavior. Temporary history hooks are removed on success, timeout, and context cancellation.
