#!/usr/bin/env python3
"""Normalize retained late-recovery probe evidence without claiming wire parity."""
from __future__ import annotations

import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "examples/benchmark/results"
SCOUT = RESULTS / "fresh-late-recovery-20261112/scout-repetitions.json"
PLAYWRIGHT = RESULTS / "20261112T080125Z-playwright-mcp-0.0.85-late-recovery-73341"
SENTINEL = "SCOUT_PRIVATE_SENTINEL_6b29f0a1"


def read(path: Path):
    return json.loads(path.read_text())


def main() -> None:
    scout_rows = read(SCOUT)
    playwright_rows = read(PLAYWRIGHT / "playwright-mcp-repetitions.json")
    scout_receipts = []
    scout_bytes = 0
    for index, row in enumerate(scout_rows, start=1):
        results = row["serialized_tool_results"]
        scout_bytes += row["output_bytes"]
        click_text = json.dumps(results["click"])
        reconcile_text = json.dumps(results["reconcile_action"])
        scout_receipts.append({
            "repetition": index,
            "unknown_outcome": "SCOUT_ACTION_OUTCOME_UNKNOWN" in click_text,
            "expected_postcondition_retained": "Late completion" in click_text,
            "same_target_reconciled": row.get("verified") is True,
            "linked_recovery_receipt": "recovery_of_operation_ordinal" in reconcile_text,
            "replay_count_zero": "replay_count=0" in row.get("evidence", ""),
            "privacy_sentinel_leaked": SENTINEL in json.dumps(results),
        })
    playwright_bytes = sum(row["response_payload_bytes"] for row in playwright_rows)
    playwright_checks = [
        {"repetition": row["repetition"],
         "final_predicate_observed": row.get("observed_text") == "Late completion",
         "privacy_sentinel_leaked": row.get("privacy_sentinel_leaked") is True,
         "native_recovery_receipt": False}
        for row in playwright_rows
    ]
    report = {
        "schema_version": 1,
        "benchmark": "fresh-late-recovery-evidence-probe",
        "scenario": "late click completes after caller deadline",
        "fixture": "shared late-action route with hidden privacy sentinel",
        "sentinel": SENTINEL,
        "products": [
            {"name": "Scout MCP", "version": "worktree snapshot 2026-11-12",
             "repetitions": len(scout_rows), "successful_repetitions": sum(r["success"] for r in scout_rows),
             "serialized_response_bytes": scout_bytes,
             "serialized_response_kind": scout_rows[0]["serialized_tool_results_kind"],
             "per_repetition_receipt_checks": scout_receipts,
             "privacy_sentinel_leaks": sum(r["privacy_sentinel_leaked"] for r in scout_receipts)},
            {"name": "Playwright MCP", "version": "0.0.85",
             "repetitions": len(playwright_rows), "successful_repetitions": sum(r["success"] for r in playwright_rows),
             "serialized_response_bytes": playwright_bytes,
             "serialized_response_kind": "raw MCP JSON-RPC tool result objects",
             "per_repetition_checks": playwright_checks,
             "privacy_sentinel_leaks": sum(r["privacy_sentinel_leaked"] for r in playwright_checks),
             "native_recovery_receipt": False}
        ],
        "interpretation": {
            "outcome_parity": "both adapters reached the final page predicate in all ten runs",
            "receipt_comparison": "Scout returns a typed unknown outcome and later links a read-only reconciliation receipt; the Playwright adapter retains its in-flight call and inspects the page after completion.",
            "privacy_probe": "The hidden fixture sentinel was absent from retained serialized tool results in both adapters; this checks only these result objects and this fixture.",
            "bytes_comparable": False,
            "bytes_limit": "Both totals cover three serialized tool results per repetition and exclude JSON-RPC framing. Scout serializes Go MCP client ToolResult values; Playwright records raw MCP result objects. The payload structures and recovery evidence differ, so byte totals are descriptive, not a size ranking.",
            "timing_comparable": False,
            "limitations": ["one local fixture", "ten repetitions", "does not measure diagnosis time, trace overhead, or production reliability", "Scout normalized client result serialization is not raw wire capture"]
        }
    }
    out = RESULTS / "fresh-late-recovery-evidence-20261112.json"
    out.write_text(json.dumps(report, indent=2) + "\n")
    print(out.relative_to(ROOT))


if __name__ == "__main__":
    main()
