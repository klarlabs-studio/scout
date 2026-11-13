# Durable task recovery

This example shows how a Go host can preserve a browser operation across a
Scout process interruption, then reconcile the outcome using the host's own
application record. It also demonstrates a signed, short-lived handoff to a
different receiver. The example is a local fixture, not production broker or
database code.

## Run the complete fixture

From the repository root, with Go, Node.js, and a local Chrome/Chromium
installation available:

```sh
go run ./examples/durable_task_recovery
```

The example creates a temporary bbolt task store, starts a local application
fixture and Chrome, and launches fresh Scout child processes. It exercises
interruption after a committed mutation, interruption during result
persistence, completed-result reuse, browser replacement, and a confirmed
zero-effect request followed by a new task ID. It exits non-zero if the
expected replay blocks, handoff checks, or ledger assertions fail. Set
`SCOUT_RECOVERY_REPORT_PATH=/path/to/report.json` to save the machine-readable
run report.

## Host recovery flow

Persist a stable task ID and use the same ID only to retrieve its existing
record. After a worker interruption:

1. Confirm the old worker is stopped. A stored cursor cannot prove that the
   worker is no longer running.
2. Load the durable task record with `Session.GetTaskRunContext` or use
   `Session.RecoverTaskRunContext` for the record and bounded recovery guidance.
3. Query the application's authoritative state using the operation's
   application key or business identifier. Browser text and a successful tool
   response are not a transaction ledger.
4. Record an unresolved observation while evidence is incomplete, or record a
   terminal resolution with `ResolveInterruptedTaskRunWithEvidenceContext`
   once the host has authoritative evidence and provenance.
5. Keep the interrupted task ID reserved. For follow-up work, use a new task
   ID and, where supported, the application's own idempotency key.

Every recovery recommendation has `replay_authorized: false`. A receipt,
matching browser context, or `verified` page postcondition cannot authorize a
retry: the application contract determines whether a retry is safe.

## Portable receipt and handoff

Use `Session.ExportTaskRunRecoveryReceiptContext` to export the redacted
operation envelope, or `NewRecoveryHandoffEnvelope` to sign it with a host
owner scope and broker-issued browser-generation token. Keep the HMAC key
outside the task store, use at least 32 random bytes, and make the handoff
expire within 15 minutes. Receivers should strictly decode and verify the
signature, authenticate the owner, then compare the envelope with independently
observed browser, target, frame, and policy bindings. A binding match only
permits read-only inspection; it never permits replay.

The wire contracts and independent checks are here:

- [Receipt and handoff contract](../../docs/recovery-receipt-contract.md)
- [Recovery receipt JSON Schema](../../docs/schemas/scout-recovery-receipt-v1.schema.json)
- [Signed handoff JSON Schema](../../docs/schemas/scout-recovery-handoff-v1.schema.json)
- [Independent Python receipt auditor](../benchmark/verify_recovery_receipt.py)
- [Independent Node handoff receiver](receiver.mjs)

The Node receiver's command-line interface is exercised by the full Go
fixture, which creates the signed handoff, receiver observation, and
agent-browser target observation. Do not use the fixture's generated keys or
in-process ledger as production trust anchors.

## Evidence limits

The local report proves only that this fixture's assertions passed in one
environment. The fixture ledger is in-process; its owner and browser-generation
claims are modeled by the parent process; and the agent-browser target/URL
observation is unauthenticated and has no frame binding. Production hosts must
connect the same flow to an authenticated broker and an authoritative
application data source. For comparative claims, use the repeated matched
experiments in [the competitive opportunity report](../../docs/competitive-opportunities.md)
and report their scope and limitations.
