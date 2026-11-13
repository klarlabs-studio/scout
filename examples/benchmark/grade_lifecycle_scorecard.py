#!/usr/bin/env python3
"""Grade version-pinned browser lifecycle measurements without hiding gaps.

Input is a JSON object with a `runs` array. Each row names a product/version,
the target-pressure scenario, observed attach/first-read latency, recovery
steps, and doctor/readiness classifications. Missing measurements stay
unmeasured; this utility does not run browsers or infer product behavior.
"""
from __future__ import annotations

import argparse
import json
import math
import statistics
import sys
from collections import defaultdict
from pathlib import Path
from typing import Optional

STATES = ("launch_failed", "browser_unreachable", "page_unresponsive", "app_probe_failed")
EXPECTED_READINESS = {
    "launch_failed": {"browser_live": "error"},
    "browser_unreachable": {"browser_live": "error"},
    "page_unresponsive": {"browser_live": "ok", "page_responsive": "not_checked"},
    "app_probe_failed": {"browser_live": "ok", "app_probe": "error"},
}
PRESSURE = ("live", "discarded", "crashed")
MIN_RUNS_PER_SCENARIO = 30
MIN_RUNS_PER_READINESS_STATE = 30
MIN_CDP_RECOVERY_RUNS = 30


def percentile(values: list[float], p: float) -> Optional[float]:
    if not values:
        return None
    ordered = sorted(values)
    index = (len(ordered) - 1) * p
    lower = math.floor(index)
    upper = math.ceil(index)
    if lower == upper:
        return ordered[lower]
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (index - lower)


def load(path: Path) -> dict:
    data = json.loads(path.read_text())
    if not isinstance(data, dict) or not isinstance(data.get("runs"), list):
        raise ValueError("input must be an object with a runs array")
    return data


def grade(data: dict) -> dict:
    rows = data["runs"]
    groups: dict[tuple[str, str, str, int], list[dict]] = defaultdict(list)
    for i, row in enumerate(rows):
        if not isinstance(row, dict):
            raise ValueError(f"runs[{i}] must be an object")
        for key in ("product", "version", "target_state"):
            if not isinstance(row.get(key), str) or not row[key].strip():
                raise ValueError(f"runs[{i}].{key} must be a non-empty string")
        if row["target_state"] not in PRESSURE:
            raise ValueError(f"runs[{i}].target_state must be one of {PRESSURE}")
        target_count = row.get("target_count")
        if not isinstance(target_count, int) or target_count < 0:
            raise ValueError(f"runs[{i}].target_count must be a non-negative integer")
        for key in ("attach_ms", "first_verified_read_ms"):
            value = row.get(key)
            if value is not None and (not isinstance(value, (int, float)) or value < 0):
                raise ValueError(f"runs[{i}].{key} must be a non-negative number or null")
        groups[(row["product"], row["version"], row["target_state"], target_count)].append(row)

    latency = []
    for (product, version, state, count), group in sorted(groups.items()):
        for metric in ("attach_ms", "first_verified_read_ms"):
            values = [float(row[metric]) for row in group if row.get(metric) is not None]
            latency.append({
                "product": product, "version": version, "target_state": state,
                "target_count": count, "metric": metric,
                "n": len(values), "missing": len(group) - len(values),
                "p50_ms": percentile(values, .50), "p95_ms": percentile(values, .95),
                "mean_ms": statistics.fmean(values) if values else None,
            })

    readiness = []
    readiness_complete = True
    for product, version in sorted({(r["product"], r["version"]) for r in rows}):
        product_rows = [r for r in rows if (r["product"], r["version"]) == (product, version)]
        statuses = {state: {check: {"expected": 0, "observed": 0, "correct": 0} for check in expected} for state, expected in EXPECTED_READINESS.items()}
        for row in product_rows:
            state = row.get("readiness_state")
            if state in statuses:
                checks = row.get("readiness_checks")
                if not isinstance(checks, dict):
                    continue
                for check, expected_verdict in EXPECTED_READINESS[state].items():
                    counters = statuses[state][check]
                    counters["expected"] += 1
                    verdict = checks.get(check)
                    if verdict in ("ok", "warning", "error", "not_checked"):
                        counters["observed"] += 1
                        if verdict == expected_verdict:
                            counters["correct"] += 1
        under_sampled = [f"{state}.{check}" for state, checks in statuses.items() for check, values in checks.items() if values["observed"] < MIN_RUNS_PER_READINESS_STATE]
        readiness_complete = readiness_complete and not under_sampled
        readiness.append({
            "product": product, "version": version, "minimum_runs_per_state": MIN_RUNS_PER_READINESS_STATE,
            "states": statuses, "under_sampled_states": under_sampled,
        })

    recovery = []
    for row in rows:
        if row.get("fault") == "cdp_disconnect":
            steps = row.get("recovery_steps")
            recovery.append({
                "product": row["product"], "version": row["version"],
                "recovered": row.get("recovered"),
                "recovery_steps": steps if isinstance(steps, list) else None,
                "first_verified_read_after_recovery_ms": row.get("first_verified_read_ms"),
                "status": "measured" if isinstance(row.get("recovered"), bool) and isinstance(steps, list) else "unmeasured",
            })
    recovery_by_product = []
    for product, version in sorted({(r["product"], r["version"]) for r in rows}):
        cases = [r for r in rows if (r["product"], r["version"]) == (product, version) and r.get("fault") == "cdp_disconnect"]
        measured = sum(1 for r in cases if isinstance(r.get("recovered"), bool) and isinstance(r.get("recovery_steps"), list))
        recovery_by_product.append({
            "product": product, "version": version, "runs": len(cases), "measured": measured,
            "minimum_runs": MIN_CDP_RECOVERY_RUNS,
            "status": "complete" if measured >= MIN_CDP_RECOVERY_RUNS else "under_sampled",
        })

    required = {(state, count) for state in PRESSURE for count in (0, 10, 50)}
    coverage = []
    versions = sorted({(r["product"], r["version"]) for r in rows})
    for product, version in versions:
        product_rows = [r for r in rows if (r["product"], r["version"]) == (product, version)]
        counts = {(state, count): sum(1 for r in product_rows if (r["target_state"], r["target_count"]) == (state, count)) for state, count in required}
        coverage.append({
            "product": product, "version": version,
            "scenarios_expected": len(required),
            "scenarios_observed": sum(1 for n in counts.values() if n > 0),
            "minimum_runs_per_scenario": MIN_RUNS_PER_SCENARIO,
            "missing_or_under_sampled": [
                {"target_state": state, "target_count": count, "runs": n}
                for (state, count), n in sorted(counts.items()) if n < MIN_RUNS_PER_SCENARIO
            ],
        })

    return {
        "schema_version": 1,
        "captured_at": data.get("captured_at"),
        "environment": data.get("environment", {}),
        "evidence_scope": "Summarizes submitted observations only. It does not run browsers, authenticate reports, or infer missing outcomes.",
        "scenario_coverage": coverage,
        "latency": latency,
        "readiness": readiness,
        "readiness_coverage_complete": readiness_complete,
        "cdp_recovery": recovery,
        "cdp_recovery_coverage": recovery_by_product,
        "overall_status": "complete" if coverage and all(not item["missing_or_under_sampled"] for item in coverage) and readiness_complete and recovery_by_product and all(item["status"] == "complete" for item in recovery_by_product) else "incomplete",
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path, help="raw lifecycle run JSON")
    parser.add_argument("-o", "--output", type=Path, help="write graded JSON here")
    args = parser.parse_args()
    try:
        report = grade(load(args.input))
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        print(f"lifecycle scorecard: {exc}", file=sys.stderr)
        return 2
    rendered = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered)
    else:
        sys.stdout.write(rendered)
    return 0 if report["overall_status"] == "complete" else 3


if __name__ == "__main__":
    raise SystemExit(main())
