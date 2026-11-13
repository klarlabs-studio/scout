# Scout worker pool

This example runs a fixed number of independent browser workers. Each worker
owns one Scout session, and every submitted job emits exactly one JSON result.
Completed results include the verified action receipt. Failures include a
stable `phase` and `error_code`; jobs not started after cancellation have
`status: "not_started"` and `error_code: "SCOUT_JOB_NOT_STARTED"`.

## Run locally

```sh
go run ./examples/worker_pool -workers 2 -jobs 4
```

The default fixture is created by the example and binds to loopback. For a
separately managed fixture, pass `-fixture-url`. Private and loopback addresses
are blocked unless you also pass `-allow-private-ips`; use that flag only for a
fixture you control. In both cases, each worker's `AllowedOrigins` is restricted
to the supplied fixture URL.

Each worker has its own browser session. A canceled or ambiguous dispatched
action is emitted as `needs_reconciliation` with a recovery record, and that
worker stops taking jobs. The host must inspect authoritative application state
before recording a resolution or assigning a new task ID. An ordinary job
failure does not cancel unrelated work. The process exits nonzero if any job
fails or needs reconciliation.

Consume stdout as newline-delimited JSON and key results by `job_id`; output
order follows completion order, not submission order. Treat error messages as
diagnostic text and use `status`, `phase`, and `error_code` for routing and
metrics. The result queue is bounded by worker count, so a slow stdout consumer
backpressures workers instead of buffering a full batch. A nonzero exit can
follow complete per-job output when individual jobs failed.

On SIGINT/SIGTERM, active jobs publish their final observed result before the
pool exits. If navigation may have been dispatched, the result is
`needs_reconciliation` with a recovery record. Jobs still waiting in the queue
are `not_started`; the process exits nonzero so the host can account for the
interrupted batch. The sample does not persist the job queue or retry IDs, so
production hosts must retain those in their own durable job store.
Session-close and local fixture-server shutdown failures are included in the
process error, including when cancellation or result-output failure also
occurred. Hosts can inspect joined causes with `errors.Is` when calling `run`
in-process; the executable reports the combined diagnostic on stderr.

## Remote CDP

For a clean Linux container run, start the pinned headless-shell browser and
attach the workers over CDP:

```sh
docker compose -f examples/worker_pool/compose.yaml up --abort-on-container-exit --exit-code-from worker
docker compose -f examples/worker_pool/compose.yaml down
```

Compose shares the browser service's network namespace with the worker because
Chrome advertises its CDP WebSocket on loopback. The worker and browser remain
separate containers, and the browser's CDP port is not published to the host.
The browser is externally owned; Scout disconnects its sessions on exit.

When `SCOUT_REMOTE_CDP` is set and more than one worker is requested, the
example requires `-isolate-remote-contexts`. Each worker then receives a
separate ephemeral browser context. Remote contexts do not enforce browser-wide
network egress. Configure that on the remote browser host and verify it there.

The 12 November 2026 Compose run verified four of four fixture jobs across two
remote targets and confirmed fixture-origin policy in every receipt. Its report
and raw log are in [`reports`](reports/compose-origin-policy-2026-11-12.json).
This is a single local recipe run; it does not measure performance or verify
remote-host egress.

A fresh, forced-recreation run at 10:16 UTC on 12 November also completed all
four jobs. All four receipts were verified, fixture-origin scoped, and bound
to two isolated remote browser contexts. The captured summary and raw worker
log are [`compose-recovery-live-2026-11-12.json`](reports/compose-recovery-live-2026-11-12.json)
and [`compose-recovery-live-2026-11-12.log`](reports/compose-recovery-live-2026-11-12.log).
This confirms the documented container path on Go 1.27.4 and the pinned browser
image; it is one local run, not a reliability or performance estimate.
