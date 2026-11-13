# Recovery receipt interface parity

`compare_recovery_receipts` checks receipt exports from Scout's Go API, stateful JSONL CLI, and MCP stdio server. It validates each receipt with the strict Go decoder and the independent Python verifier, then compares normalized fields across interfaces.

The report counts a required scenario as covered only when its case passes
receipt decoding, independent-verifier identity checks, scenario assertions, and
cross-interface field comparison. A manifest may set `required_scenarios` to a
subset for a narrowly scoped live capture; if omitted, interrupted, completed,
and non-first-step cases are all required. A non-first-step case requires a
non-empty frame ID. A case may set `frame_id_comparison` to `presence` when
independent browser sessions produce distinct frame IDs; this requires every
receipt to have a frame ID and normalizes the value to `<frame-bound>` for the
cross-interface comparison. Omit it or set it to `exact` to compare IDs
literally.

The comparer omits absolute evidence timestamps and the operation deadline from
normalized parity fields because independent captures necessarily happen at
different times. It lists those dynamic fields in the report; receipt validators
still inspect the original values.

## Reproduce seeded interface captures

From the repository root:

```sh
GOTOOLCHAIN=go1.27.4 go run ./examples/benchmark/recovery_receipt_parity/capture --out /tmp/scout-recovery-parity
```

The capture command creates temporary task stores, builds Scout if `--scout` is omitted, and performs exports through all three public interfaces. It executes no browser actions. The completed fixtures contain two saved action receipts so the non-first-step case can export operation 2. Use a new `--out` directory for each run.

To audit those generated captures, point a manifest at the files or update `manifest.example.json` paths. The checked-in `manifest.live.json` and receipts are one deterministic capture from these interfaces.

```sh
GOTOOLCHAIN=go1.27.4 go run ./examples/benchmark/recovery_receipt_parity -manifest examples/benchmark/recovery_receipt_parity/manifest.live.json
```

The parity result establishes export/schema consistency only. It does not authenticate the producer, independently observe browser binding, or prove application outcomes. Fixtures and identifiers are synthetic; they do not represent a live browser task.
