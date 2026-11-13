#!/usr/bin/env python3
"""Independently grade one task from durable_task_recovery's run report.

The report contains two separate evidence domains: a receiver context match
and the example fixture's authoritative per-task effect ledger. This grader
does not treat context identity as business truth or a local fixture as a
production authority.
"""

from __future__ import annotations

import json
import datetime
import re
import sys
from pathlib import Path


def fail(message: str) -> None:
    raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def audit(report, task_id, expected_effects):
    if not isinstance(report, dict):
        fail("run report must be an object")
    if report.get("schema_version") != 1 or report.get("scenario") != "durable-task-recovery-example":
        fail("unsupported durable task recovery report")
    handoff = report.get("handoff")
    if not isinstance(handoff, dict):
        fail("handoff must be an object")
    replay_allowed = handoff.get("replay_allowed")
    context_matched = handoff.get("node_full_context_matched")
    if not isinstance(replay_allowed, bool) or not isinstance(context_matched, bool):
        fail("handoff replay/context predicates must be booleans")
    results = report.get("results")
    if not isinstance(results, list):
        fail("results must be an array")
    matches = [row for row in results if isinstance(row, dict) and row.get("task_id") == task_id]
    if len(matches) != 1:
        fail(f"expected exactly one result for task {task_id!r}")
    row = matches[0]
    accepted = row.get("accepted_for_task")
    source = row.get("evidence_source")
    reference = row.get("evidence_reference")
    observed_at = row.get("evidence_observed_at")
    resolution = row.get("host_resolution")
    retry_blocked = row.get("same_id_retry_blocked")
    if isinstance(accepted, bool) or not isinstance(accepted, int) or accepted < 0:
        fail("accepted_for_task must be a non-negative integer")
    if not isinstance(source, str) or not source:
        fail("evidence_source must be a non-empty string")
    if not isinstance(reference, str) or not reference.startswith("/ledger?") or "\n" in reference:
        fail("evidence_reference must identify the fixture ledger read")
    if not isinstance(observed_at, str) or not observed_at:
        fail("evidence_observed_at must be a non-empty timestamp")
    try:
        parsed_observed_at = datetime.datetime.fromisoformat(observed_at.replace("Z", "+00:00"))
        if parsed_observed_at.tzinfo is None:
            fail("evidence_observed_at must include a timezone")
    except ValueError:
        fail("evidence_observed_at must be an RFC3339 timestamp")
    if not isinstance(resolution, str) or not resolution:
        fail("host_resolution must be a non-empty string")
    if not isinstance(retry_blocked, bool):
        fail("same_id_retry_blocked must be boolean")
    if isinstance(expected_effects, bool) or not isinstance(expected_effects, int) or expected_effects < 0:
        fail("expected_effects must be a non-negative integer")

    if accepted == expected_effects:
        oracle_outcome = "verified_complete"
    elif accepted > expected_effects:
        oracle_outcome = "duplicate_or_excess_effects"
    else:
        oracle_outcome = "incomplete_or_unresolved"
    resolution_agrees = resolution == oracle_outcome
    frame_known = handoff.get("node_frame_binding_available")
    if not isinstance(frame_known, bool):
        fail("node_frame_binding_available must be boolean")
    context_status = "matched" if context_matched else ("unavailable" if not frame_known else "mismatched")
    return {
        "valid": True,
        "task_id": task_id,
        "browser_context": context_status,
        "browser_context_matched": context_matched,
        "application_oracle": {
            "source": source,
            "reference": reference,
            "observed_at": observed_at,
            "reported_resolution": resolution,
            "accepted_effects": accepted,
            "expected_effects": expected_effects,
            "outcome": oracle_outcome,
            "reported_resolution_agrees": resolution_agrees,
            "separate_application_fixture_oracle": True,
        },
        "same_id_retry_blocked": retry_blocked,
        "replay_allowed": replay_allowed,
        "safe_to_replay": False,
        "producer_authenticated": False,
        "oracle_authentication_verified": False,
        "limitations": report.get("limitations", []),
    }


def main():
    if len(sys.argv) not in (3, 4):
        print("usage: grade_recovery_run.py RUN_REPORT.json TASK_ID [EXPECTED_EFFECTS]", file=sys.stderr)
        return 2
    expected_effects = 1
    if len(sys.argv) == 4:
        try:
            expected_effects = int(sys.argv[3])
        except ValueError:
            print("EXPECTED_EFFECTS must be a non-negative integer", file=sys.stderr)
            return 2
    if not re.fullmatch(r"[A-Za-z0-9._:-]+", sys.argv[2]):
        print("TASK_ID contains unsupported characters", file=sys.stderr)
        return 2
    try:
        report = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"), object_pairs_hook=unique_object)
        result = audit(report, sys.argv[2], expected_effects)
    except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as exc:
        print(json.dumps({"valid": False, "error": str(exc)}, separators=(",", ":")), file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    passes = (
        result["application_oracle"]["outcome"] == "verified_complete"
        and result["application_oracle"]["reported_resolution_agrees"]
        and result["same_id_retry_blocked"]
        and not result["replay_allowed"]
    )
    return 0 if passes else 3


if __name__ == "__main__":
    raise SystemExit(main())
