# Scout recovery receipt contract

**Status:** proposed public contract, derived from the current Go implementation and competitive research (6 November 2026).

Scout already has versioned `ActionReceipt`, `ActionRecoveryRecord`, and
`TaskRunRecord` structures. This document defines the stable portable envelope
that a host should store and hand to another process when browser work may
have been interrupted. It does not authorize browser actions or retries.

Receipt v1's `evidence.trust` is always `runtime_metadata`: it describes what
Scout observed while executing the operation. Host attestations belong in the
separately signed recovery-handoff envelope, and page-derived content is never
made trustworthy by copying it into a receipt. The Go decoder, independent
Python auditor, and JSON Schema enforce this same boundary.
The Go receipt and handoff decoders also reject duplicate JSON object keys
recursively, matching the Python auditor instead of silently keeping the last
value.

## Intended guarantees

- A receipt identifies the task and operation, browser instance, target, and
  frame involved in the operation.
- Dispatch and verification are separate facts. An action can be dispatched
  while its application effect remains unknown.
- A postcondition is evidence about browser-visible state. It is not proof of
  a backend transaction unless the host supplies independent application
  evidence.
- An interrupted operation remains reserved against automatic replay. A host
  inspects authoritative application state and records an `unresolved`
  observation or a terminal resolution. Unresolved observations keep the
  reservation active for further inspection; follow-up plans use a new task ID.
- Exported records omit secret action inputs and expected values. Page-derived
  text, selectors, URLs, and evidence remain untrusted and may need additional
  host redaction.
- `TaskRunRecord` lookups return the complete retained record, including a
  saved `result`, `error`, and host resolution notes when present. Those fields
  can contain private page content or application details and must be protected
  as sensitive browser state. Redaction of the plan or portable receipt does
  not sanitize the full task record.
- `recover_task_run` includes bounded guidance from the durable task cursor.
  It distinguishes an initial cursor from a possibly dispatched action, but
  custom stores without per-action progress may retain the initial cursor
  after dispatch. The cursor alone does not prove whether work occurred. The
  guidance never authorizes replay or infers business state.

Export binds to the retained task plan. If the saved plan has a policy ID, a
non-empty action policy ID must match it. An interrupted WebMCP action carries
the frame ID recorded in that plan, so the receipt identifies the frame
selected when the action was prepared even if the live browser context is no
longer available. Receivers must still compare that frame with an independently
observed frame before treating the receipt as same-context evidence.

## Recovery guidance

The Go recovery view, stateful JSONL CLI, and MCP `recover_task_run` return the
same `guidance` object with `recommendation`, `rationale`, `prerequisites`, and
`replay_authorized` fields. The recommendation is operator guidance, not an
executable continuation request. Its standalone Draft 2020-12 schema is
[`scout-task-run-guidance-v1.schema.json`](schemas/scout-task-run-guidance-v1.schema.json):

| Durable record state | Recommendation | Required operator action |
|---|---|---|
| No matching record | `verify_task_store_scope` | Confirm task ID, configured store, owner, and session namespace; absence does not prove that no action occurred. |
| Saved completed result | `inspect_saved_task_result` | Review the retained result and verification evidence. |
| Saved non-completed result | `reconcile_with_authoritative_application_state` | Inspect partial effects in the saved result, reconcile uncertain effects against the application, and use a new task ID. |
| Terminal host resolution | `review_host_resolution_then_use_new_task_id` | Review evidence and provenance, then use a new task ID for follow-up work. |
| Initial `actions` phase, no current cursor, `next_action=0` | `reconcile_with_authoritative_application_state` | Confirm the previous worker cannot continue, recheck target and policy, inspect authoritative application state for possible effects, then record unresolved or terminal resolution before using a new task ID. |
| Any later or incomplete state | `reconcile_with_authoritative_application_state` | Inspect the authoritative application system and record an unresolved or terminal host resolution with provenance. |

Every recommendation sets `replay_authorized` to false. The built-in bbolt
store records the initial cursor before the first action dispatch, but custom
stores may omit per-action progress and retain that cursor after dispatch. The
initial cursor therefore cannot prove whether browser work occurred or whether
the worker is still running. A `started` cursor is intentionally treated as
possibly dispatched, even if interruption occurred before the browser call.
The host must reconcile uncertainty against authoritative application state
rather than infer dispatch success or failure from timing.

## Proposed envelope

```json
{
  "schema": "scout.recovery-receipt/v1",
  "task": {
    "id": "checkout-42",
    "plan_fingerprint": "sha256:...",
    "state": "interrupted"
  },
  "runtime": {
    "name": "scout",
    "version": "...",
    "receipt_schema": 1
  },
  "browser": {
    "instance_id": "...",
    "generation": "broker-epoch-7",
    "ownership": "shared"
  },
  "target": {
    "id": "...",
    "frame_id": "..."
  },
  "policy": {
    "id": "...",
    "digest": "sha256:...",
    "origins": ["https://example.com"],
    "allow_private_ips": false,
    "inputs_complete": true,
    "enforcement": "session_page_policy"
  },
  "operation": {
    "id": "scout-op-v1.Y2hlY2tvdXQtNDI.1",
    "ordinal": 1,
    "name": "click",
    "dispatch": "may_have_dispatched",
    "outcome": "unknown",
    "verification": "unknown",
    "deadline_at_ms": 1793966400000
  },
  "postcondition": {
    "kind": "text_present",
    "captured": false
  },
  "evidence": {
    "created_at": "2026-11-06T12:00:00Z",
    "observed_at": null,
    "trust": "runtime_metadata",
    "references": []
  },
  "recovery": {
    "replay_authorized": false,
    "next_step": "reconcile_with_authoritative_application_state"
  }
}
```

The envelope is versioned as a separate export format; existing Scout JSON
fields remain unchanged. The Go API can compose a `RecoveryReceipt` from one
retained task record and an action receipt, or export directly from a saved
task record. MCP and stateful JSONL CLI expose this through
`export_task_recovery_receipt`. An interrupted task exports only its currently
started step; a completed task selects a saved action receipt, while the
exported ordinal and ID are normalized to the one-based step within that task.
It exports only the postcondition kind and trace reference, not expected text,
field values, or page evidence. Origin policy IDs carry a versioned SHA-256
digest, which the envelope exposes in the standard `sha256:` form alongside
normalized origins and the private-IP setting so a receiver can recompute it
when `inputs_complete` is true. These two input fields are serialized even when
the origin list is empty or private-IP access is false; the JSON schema requires
them in every policy object. Older records without complete inputs retain an
ID/digest consistency check and set the flag false. `browser.instance_id` is
Scout's non-reversible fingerprint of the DevTools endpoint and selected
BrowserContext. It is useful for another Scout process but is vendor-local.
Scout omits `browser.generation` because the Engine does not independently
observe a broker-issued lifecycle token. If a trusted host adds that optional
field, it must authenticate the value and bind it to the actual browser
instance; it must not copy the Scout fingerprint into the generation field.
The operation's optional `deadline_at_ms` is the absolute Unix deadline
captured from the action context. It is omitted when no deadline was observed;
the task's configured timeout is not substituted for an absolute deadline.

`agent.CompareRecoveryReceiptBinding` is a read-only receiver helper. It
compares the receipt's browser fingerprint, target, frame, and policy ID/digest
with values independently observed from the receiving runtime. Set
`FrameBindingKnown` only when the receiver positively identifies the selected
frame; an empty frame ID means top-level only when that flag is true. `SameContext`
is true only when every binding matches. A match is only a prerequisite for
considering page inspection: it does not authorize a mutation, replay, or
business conclusion. The host must authenticate the owner before exposing
browser state and must reconcile the operation against its authoritative
application source. Receipt v1 intentionally contains no authenticated owner
identity, so a receiver cannot use it alone to establish cross-principal
authorization. A changed browser generation, target, frame, or policy fails
the same-context comparison and requires application-level reconciliation
without relying on the old page.

The benchmark directory includes a standalone Python audit at
[`verify_recovery_receipt.py`](../examples/benchmark/verify_recovery_receipt.py).
It independently checks the exported envelope's strict v1 shape, operation
state consistency, task-local operation ID, non-replay rule, evidence metadata,
and canonical origin-policy digest. It does not trust the receipt as proof of
its own producer, browser binding, or application outcome; its output keeps
those three facts explicitly false until a trusted receiver supplies them.
Run it against exported JSON as a second implementation of the portable
contract, alongside the Go decoder and JSON Schema validation.

The Go canonicalizer and Python audit share edge-case vectors in
[`scout-origin-policy-v1-vectors.json`](schemas/scout-origin-policy-v1-vectors.json).
They cover an unrestricted policy, case folding and default-port expansion,
duplicate origins and ordering, IPv6 formatting, explicit ports, and private-IP
semantics. The focused Go check is
`go test ./agent -run 'TestOriginPolicySharedVectors|TestRecoveryReceipt' -count=1`;
the Python audit checks the same vectors before accepting a receipt. This
verifies the two local implementations against shared expected values; it is
not a full Go/CLI/MCP live round-trip or participant study.

The v1 browser fingerprint is Scout-specific: it hashes Scout's view of the
DevTools endpoint and selected BrowserContext. A different browser runner
cannot be expected to derive the same value, even when it is attached to the
same Chrome target. Do not compare vendor-local browser IDs across runtimes.
Cross-runner handoff uses a separate `scout.recovery-handoff/v1` envelope
around the unchanged receipt. `agent.NewRecoveryHandoffEnvelope` signs the
receipt together with a host owner scope and a host-issued browser-generation
token using HMAC-SHA256. The host must issue that token from its browser broker
and bind it to the actual browser/context; an arbitrary caller-supplied string
does not establish continuity. Keep the signing key outside the task store,
use at least 32 random bytes, set an expiry no more than 15 minutes after
issuance, and select it by `key_id` so receivers can rotate keys. Both the Go
verifier and the independent Node receiver reject longer lifetimes. The
envelope shape is specified in
[`docs/schemas/scout-recovery-handoff-v1.schema.json`](schemas/scout-recovery-handoff-v1.schema.json);
the schema documents the timestamp relationship, while Go and Node enforce
the 15-minute maximum at runtime. The envelope signature authenticates the
handoff claims, not the live browser or application outcome.

Receivers strictly decode with `agent.DecodeRecoveryHandoffEnvelope`, then use
`agent.CompareRecoveryHandoffBinding` with trusted keys, the authenticated
owner scope, current time, and independently observed broker/browser/policy
values. A match is only a gate for considering read-only page inspection. A
different browser vendor can compare the shared generation token even though
it cannot reproduce Scout's internal browser fingerprint. The receiving host
still has to verify that its broker maps the token to the current browser,
target, and frame, and query its authoritative application source. The
comparison always reports replay as disallowed. Scout's simpler
`CompareRecoveryReceiptBinding` remains useful when the receiver can observe
Scout's internal fingerprint directly, such as a second Scout process.
For handoff comparisons, the receiver must set `FrameBindingKnown` only when
its adapter positively identifies the selected frame. An empty frame ID is
Scout's top-level-frame representation; it must not stand in for an
unobserved frame value. Non-Scout adapters can serialize their independent
observation using [the v1 JSON schema](schemas/scout-recovery-handoff-observation-v1.schema.json)
and [top-level-frame example](schemas/examples/scout-recovery-handoff-observation-top-level.json),
then decode it with `agent.DecodeRecoveryHandoffObservedBinding`. The host must
authenticate the adapter/broker source before decoding and comparing; the
schema validates shape, not trust.

The standalone Node receiver in `examples/durable_task_recovery/receiver.mjs`
independently verifies the signed handoff, rejects duplicate JSON fields, and
checks the embedded receipt's task-local operation ID/ordinal, outcome and
verification agreement, conservative unknown-dispatch state, no-replay next
step, and recomputable origin-policy digest when policy inputs are marked
complete. These checks make the example receiver useful outside Scout's Go
runtime; they do not authenticate the receipt producer or turn fixture-supplied
broker observations into production evidence. The latest local run is recorded
in the receiver smoke report (local audit artifact).

When the host records its reconciliation, terminal `verified_complete` and
`rejected` `TaskRunResolution` values require `evidence_source`, an opaque
`evidence_reference`, and RFC3339 `observed_at`, in addition to a short
non-secret `evidence` note. Unresolved observations may omit provenance while
the host gathers evidence. The source and reference must be supplied together;
keep the reference non-secret and avoid embedding credentials or sensitive
query data. These fields identify the host's evidence trail but do not let
Scout verify its authority or contents. The older
`ResolveInterruptedTaskRunContext` helper remains suitable for unresolved
observations only; use `ResolveInterruptedTaskRunWithEvidenceContext` for
terminal conclusions.

## Status vocabulary

| State | Meaning | Safe host behavior |
|---|---|---|
| `verified` | The declared postcondition was positively observed. | Continue only if the application contract says that predicate is sufficient. |
| `rejected` | Scout recorded negative action or verification evidence. This can mean an action failed, a response failed, or a postcondition/value check did not match. | Inspect application state; this outcome alone does not prove whether dispatch occurred or a mutation was rolled back. |
| `unknown` | Dispatch or observation was interrupted or ambiguous. | Reconcile against authoritative state before any retry. |
| `unverified` | No postcondition was requested or checked. | Treat the action as an attempted operation, not task completion. |

Protocol delivery success is not one of these application outcomes. A tool
error, timeout, or successful response must not be used alone to infer whether
the site committed a mutation.

`dispatch` is a separate, conservative statement. The exporter uses
`may_have_dispatched` for unknown outcomes and action/response failures because
those outcomes do not establish whether the browser received the mutation. A
`dispatched` value means the recorded action path completed far enough for
Scout to produce a non-failure receipt; it still does not prove site delivery
or business effect. Consumers must not derive `safe_to_retry` from either
field: `replay_authorized` remains false for every receipt. The v1 `next_step`
is always `reconcile_with_authoritative_application_state`, even when the
browser postcondition was verified. The host's business ledger, not the browser
predicate, determines whether a retry is safe.

## Portability and privacy rules

1. Preserve task ID, plan fingerprint, task-local operation ordinal, browser/target/frame
   identity, dispatch state, outcome, verification state, and timestamps when
   transferring a receipt.
2. Never export secret action inputs, credentials, arbitrary expected values,
   cookies, page storage, or raw page text by default.
3. Mark page-originated strings and URLs as untrusted. Sanitize URLs and let
   the host decide whether identifiers are sensitive in its environment.
4. A receiving runtime may reconcile only if it can prove that the referenced
   browser and target still match. Otherwise it reports unavailable evidence;
   it does not silently bind the receipt to a neighboring tab.
5. Receipt import is read-only. It cannot trigger an action, unlock a task ID,
   or authorize replay. Resolution is a separate host attestation.
6. Keep native runtime capability separate from adapter-created evidence in
   comparative benchmarks.

## Compatibility and acceptance gates

- Maintain a JSON schema and golden examples for each published major version.
- Reject unsupported schema versions and malformed or contradictory identity
fields; preserve unknown extension fields only in a future explicitly
negotiated envelope version. Validate a provided policy digest as lowercase
SHA-256 and recompute it from the receipt's canonical origin and private-IP
inputs when `inputs_complete` is true. Otherwise check that the policy ID and
digest agree, and treat the digest as an identifier only.
- Round-trip receipts through Go, MCP, stateful CLI, and a fresh process. Verify
  redaction, stable identity, exact outcome state, and no replay side effect.
- Exercise process loss before dispatch, after possible dispatch, and after
  positive verification but before durable result commit. Repeat with a
  replaced browser and a different host process.
- Compare Scout and competitors using the same fixture, showing native and
  adapter behavior separately. Require at least 30 runs per failure boundary
  before publishing comparative outcome rates.
- Validate the contract with Go service/QA users; measure recovery code and
  time saved before claiming buyer advantage.

## Current implementation gaps

Scout currently persists a redacted `TaskRunPlan`, cursor, browser/target
identity, idempotency reservation, and optional host resolution. Action
receipts are versioned and carry operation ordinal, target/browser/frame
identity, outcome, verification, evidence, postcondition, and an embedded
policy snapshot. That snapshot includes the canonical exact-origin allowlist
(with normalized explicit ports), private-IP setting, policy ID, page-policy
scope, proxy configuration/application status, and known unobserved service
worker and WebSocket paths. For remote CDP, `remote_host_policy` is
`unverified`; a configured proxy is not described as applied by Scout. The
snapshot reports configuration and known coverage boundaries, not proof that
a browser provider or external proxy enforced a rule. The recovery
record is versioned and non-replayable. The Go envelope exports Scout's Engine
fingerprint in `instance_id` and omits `generation` because Scout does not
independently observe a broker-issued browser lifecycle token. An operation
deadline is included only when present on the saved action receipt. The
separate action recovery
record may include selectors or postcondition text; use the redacted task
envelope for portable handoff until that older record is separately hardened.
The envelope exposes the origin policy's versioned digest and uses a
delimiter-safe task-local operation ID. The format is
`scout-op-v1.<base64url-utf8-task-id>.<one-based-step>`; base64url is unpadded
and canonical. Receivers decode the task component and require it to match
`task.id`, then compare the positive decimal step with `operation.ordinal`.
This preserves arbitrary caller task IDs, including IDs containing dots or
slashes, without ambiguity.
The standalone [action receipt schema](schemas/scout-action-receipt-v1.schema.json)
and [remote-CDP example](schemas/examples/scout-action-receipt-remote-cdp-v1.json)
describe the additive policy object. The schema keeps that object optional for
historical v1 receipts; new Scout-generated receipts include it.
