#!/usr/bin/env python3
"""Audit retained late-recovery benchmark artifacts without inferring missing evidence."""
from __future__ import annotations

import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "examples/benchmark/results"
SCOUT = RESULTS / "scout-mcp-late-action-cancellation-10x-20261106.json"
AGENT_DIR = RESULTS / "20261106T054817Z-agent-browser-0.41.2-late-recovery-44622"
PLAYWRIGHT_DIR = RESULTS / "20261106T054942Z-playwright-mcp-0.0.84-late-recovery-46139"
SECRET = "SCOUT-PRIVACY-SENTINEL-DO-NOT-LEAK"


def load(path: Path):
    return json.loads(path.read_text())


def scout_audit():
    rows = load(SCOUT)
    receipts = []
    for row in rows:
        evidence = row.get("evidence", "")
        receipts.append({
            "repetition": row.get("repetition", len(receipts) + 1),
            "success": row.get("success") is True,
            "verified": row.get("verified") is True,
            "unknown_outcome": "unknown_outcome=true" in evidence,
            "same_target_reconciliation": "same_target_reconciled=true" in evidence,
            "recovery_receipt_linked": "recovery_receipt_linked=true" in evidence,
            "replay_prohibited": "replay_count=0" in evidence,
            "serialized_wire_receipt_available": False,
            "privacy_scan_available": False,
        })
    checks = {
        "all_repetitions_succeeded": len(rows) == 10 and all(row["success"] for row in receipts),
        "all_receipts_linked": all(row["recovery_receipt_linked"] and row["same_target_reconciliation"] and row["replay_prohibited"] for row in receipts),
    }
    return {
        "product": "scout-mcp",
        "version": "worktree snapshot 2026-11-06",
        "archive": SCOUT.relative_to(ROOT).as_posix(),
        "summary_output_bytes_available": sum(row.get("output_bytes", 0) for row in rows),
        "summary_output_bytes_definition": "benchmark summary rows; original MCP JSON-RPC result payloads were not retained",
        "repetitions": receipts,
        "checks": checks,
        "missing_evidence": ["raw serialized MCP tool results", "privacy sentinel scan over raw tool results", "normalized receipt completeness from wire payload"],
    }


def agent_audit():
    summary = load(AGENT_DIR / "agent-browser-late-recovery-comparison.json")
    repetitions = load(AGENT_DIR / "agent-browser-late-recovery-repetitions.json")
    files = sorted(AGENT_DIR.glob("agent-browser.late-recovery-*.*"))
    combined = b"".join(path.read_bytes() for path in files)
    outputs = []
    for path in files:
        if path.suffix not in {".stdout", ".stderr"}:
            continue
        text = path.read_text(errors="replace")
        if path.name.endswith(".observe.stdout"):
            outputs.append({"file": path.name, "final_predicate_observed": "Late completion" in text, "bytes": path.stat().st_size})
    checks = {
        "all_repetitions_succeeded": summary.get("successful_repetitions") == summary.get("repetitions") == 10,
        "raw_cli_outputs_present": len(outputs) == 10,
    }
    return {
        "product": "agent-browser",
        "version": summary.get("tool_version"),
        "archive": AGENT_DIR.relative_to(ROOT).as_posix(),
        "retained_cli_output_bytes": sum(path.stat().st_size for path in files if path.suffix in {".stdout", ".stderr"}),
        "output_bytes_definition": summary.get("output_bytes_definition"),
        "inspection_outputs": outputs,
        "native_recovery_receipt": summary.get("native_recovery_record") is True,
        "checks": checks,
        "privacy_sentinel_test": "not_tested",
        "missing_evidence": ["structured native recovery receipt is not part of this adapter output", "raw output predates sentinel-injection privacy test"],
    }


def playwright_audit():
    summary = load(PLAYWRIGHT_DIR / "playwright-mcp-comparison.json")
    repetitions = load(PLAYWRIGHT_DIR / "playwright-mcp-repetitions.json")
    transcript = (PLAYWRIGHT_DIR / "playwright-mcp-transcript.jsonl").read_bytes()
    outputs = []
    for line in transcript.splitlines():
        event = json.loads(line)
        result = event.get("result")
        if not isinstance(result, dict):
            continue
        outputs.append({"bytes": len(json.dumps(result, separators=(",", ":")).encode()), "is_error": result.get("isError") is True, "contains_recovery_record": "recovery_record" in json.dumps(result)})
    checks = {
        "all_repetitions_succeeded": summary.get("successful_repetitions") == summary.get("repetitions_per_scenario") == 10,
        "raw_tool_results_present": bool(outputs),
        "native_recovery_receipt_absent": not any(output["contains_recovery_record"] for output in outputs),
    }
    return {
        "product": "playwright-mcp",
        "version": summary.get("tool_version"),
        "archive": PLAYWRIGHT_DIR.relative_to(ROOT).as_posix(),
        "raw_mcp_result_bytes": sum(output["bytes"] for output in outputs),
        "raw_tool_result_count": len(outputs),
        "native_recovery_receipt": False,
        "checks": checks,
        "privacy_sentinel_test": "not_tested",
        "missing_evidence": ["privacy sentinel was not injected in the historical run"],
    }


def main():
    products = [scout_audit(), agent_audit(), playwright_audit()]
    report = {
        "schema_version": 1,
        "benchmark": "late-recovery-evidence-archive-audit",
        "scenario_id": "late-action-caller-timeout",
        "source_report": "examples/benchmark/results/late-recovery-mcp-head-to-head-10x-2026-11-06.json",
        "audit_method": "recompute only fields supported by the archived raw or summary artifacts; absent source evidence is reported explicitly",
        "privacy_sentinel_design_note": "A sentinel must be injected into a fresh matched run before leakage can be graded; this historical audit does not claim a privacy pass.",
        "products": products,
        "cross_product_comparison": {
            "task_outcome_parity": "historically reported at 10/10 for all three products",
            "serialized_output_bytes_comparable": False,
            "receipt_completeness_comparable": False,
            "privacy_leakage_comparable": False,
            "privacy_leakage_status": "not_tested",
            "reason": "archives contain different evidence forms and Scout's original MCP results were not retained",
        },
        "limitations": [
            "This audit re-reads historical archives; it is not a fresh product execution.",
            "The privacy sentinel was not part of the historical fixture, so absence from archived bytes is not a privacy guarantee.",
            "Summary claims are not promoted to wire-level evidence when raw MCP results are missing.",
        ],
    }
    report["archive_audit_verified"] = all(all(product["checks"].values()) for product in products)
    destination = RESULTS / "late-recovery-evidence-archive-audit-2026-11-12.json"
    destination.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"report": destination.relative_to(ROOT).as_posix(), "archive_audit_verified": report["archive_audit_verified"], "privacy_leakage_status": "not_tested", "products": [{"product": item["product"], "checks": item["checks"], "privacy_sentinel_test": item.get("privacy_sentinel_test", "not_tested")} for item in products]}, indent=2))
    return 0 if report["archive_audit_verified"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
