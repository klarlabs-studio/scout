#!/usr/bin/env python3
"""Create one normalized participant record from two facilitator arm reports.

The attestation is separate from machine-generated run reports. This prevents
fixture rehearsals from being silently imported as participant sessions.
"""

import argparse
import hashlib
import json
import math
import re
import sys
from pathlib import Path


def reject_nonstandard_constant(value):
    raise ValueError(f"non-standard JSON numeric constant {value}")


def parse_finite_float(value):
    number = float(value)
    if not math.isfinite(number):
        raise ValueError(f"non-finite JSON number {value}")
    return number


def finite_number(value):
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        return False
    try:
        return math.isfinite(value)
    except OverflowError:
        return False


def read_json(path):
    return json.loads(
        path.read_text(encoding="utf-8"),
        parse_constant=reject_nonstandard_constant,
        parse_float=parse_finite_float,
    )


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_source_manifest(run, trusted_root):
    provenance = run.get("source_provenance")
    if not isinstance(provenance, dict) or provenance.get("manifest_algorithm") != "sha256":
        return {"verified": False, "file_count": 0, "failures": ["missing_sha256_manifest"]}
    files = provenance.get("files")
    if provenance.get("manifest_status") != "captured" or not isinstance(files, list) or not files:
        return {"verified": False, "file_count": 0, "failures": ["manifest_empty_or_not_captured"]}
    workdir = run.get("workdir")
    if not isinstance(workdir, str) or not workdir:
        return {"verified": False, "file_count": len(files), "failures": ["missing_workdir"]}
    root = Path(workdir).resolve()
    trusted_root = trusted_root.resolve()
    if root == trusted_root or trusted_root not in root.parents:
        return {"verified": False, "file_count": len(files), "failures": ["workdir_outside_trusted_root"]}
    failures = []
    seen = set()
    for item in files:
        if not isinstance(item, dict):
            failures.append("invalid_manifest_entry")
            continue
        relative = item.get("path")
        digest = item.get("sha256")
        size = item.get("size_bytes")
        if not isinstance(relative, str) or not relative or Path(relative).is_absolute():
            failures.append("unsafe_manifest_path")
            continue
        rel_path = Path(relative)
        path = (root / rel_path).resolve()
        # Require a canonical relative POSIX path. This avoids platform-specific
        # separator tricks and duplicate spellings of the same checkout file.
        canonical = rel_path.as_posix()
        if (
            root not in path.parents
            or relative in seen
            or canonical != relative
            or "\\" in relative
            or any(part in {"", ".", ".."} for part in rel_path.parts)
        ):
            failures.append("unsafe_or_duplicate_manifest_path")
            continue
        seen.add(canonical)
        if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            failures.append("invalid_sha256_digest")
            continue
        if isinstance(size, bool) or not isinstance(size, int) or size < 0:
            failures.append("invalid_manifest_size")
            continue
        if not path.is_file():
            failures.append("manifest_file_missing")
            continue
        if path.stat().st_size != size or sha256(path) != digest:
            failures.append("manifest_file_changed")
    return {"verified": bool(files) and not failures, "file_count": len(files), "failures": failures}


def participant_arm(run, manual, label, manifest_verification):
    if not isinstance(manual, dict):
        raise ValueError(f"attestation.{label} must be an object")
    if label == "scout" and not isinstance(manual.get("unacceptable_friction"), bool):
        raise ValueError("attestation.scout.unacceptable_friction must be an explicit boolean")
    for field in ("first_verified_seconds", "full_task_seconds", "integration_loc"):
        value = manual.get(field)
        if not finite_number(value) or value < 0:
            raise ValueError(f"attestation.{label}.{field} must be supplied as a non-negative number")

    control = run.get("control", {})
    reconcile = run.get("reconcile", {})
    result = reconcile.get("result", {})
    final_ledger = run.get("final_ledger", {})
    control_pass = control.get("passed")
    if not isinstance(control_pass, bool):
        control_pass = None
    grade = run.get("independent_grade", {})
    independently_graded = grade.get("passed") is True and grade.get("exit_code") == 0
    final_ledger_count = final_ledger.get("count")
    fixture_dispatch_count = run.get("fixture_accepted_dispatch_count", final_ledger.get("accepted_dispatch_count"))
    initial_status = result.get("initial_status", run.get("initial_status"))
    authoritative_state_inspected = result.get("ledger_inspected", run.get("ledger_inspected"))
    retry_attempted = result.get("retry_attempted", run.get("retry_attempted"))
    faulted_classification = result.get("status", run.get("final_classification"))
    checks = run.get("checks", {})
    runner_passed = (
        run.get("runner_passed") is True if "runner_passed" in run
        else bool(checks) and all(checks.values())
    )
    completion_blockers = []
    if run.get("schema_version") != 1:
        completion_blockers.append("unsupported_or_missing_runner_schema")
    if run.get("passed") is not True:
        completion_blockers.append("facilitator_run_not_passed")
    if not runner_passed or not checks or not all(checks.values()):
        completion_blockers.append("runner_checks_not_passed")
    if not independently_graded:
        completion_blockers.append("independent_ledger_grade_not_passed")
    harness = run.get("harness_provenance")
    if not isinstance(harness, list) or not harness:
        completion_blockers.append("fixture_and_runner_provenance_missing")
    if manifest_verification["verified"] is not True:
        completion_blockers.append("participant_source_manifest_not_verified")
    if manual.get("source_reviewed") is not True:
        completion_blockers.append("participant_source_not_reviewed")
    if manual.get("source_checkout_preserved") is not True:
        completion_blockers.append("participant_checkout_not_preserved")
    if not isinstance(faulted_classification, str):
        completion_blockers.append("faulted_classification_missing")
    if not isinstance(initial_status, str):
        completion_blockers.append("initial_faulted_status_missing")
    if not isinstance(authoritative_state_inspected, bool):
        completion_blockers.append("authoritative_state_inspection_missing")
    if not isinstance(retry_attempted, bool):
        completion_blockers.append("retry_observation_missing")
    if not finite_number(final_ledger_count):
        completion_blockers.append("faulted_ledger_count_missing")
    if not finite_number(fixture_dispatch_count):
        completion_blockers.append("faulted_dispatch_count_missing")
    completed = not completion_blockers
    return {
        "completed": completed,
        "first_verified_seconds": manual["first_verified_seconds"],
        "full_task_seconds": manual["full_task_seconds"],
        "integration_loc": manual["integration_loc"],
        "nominal_control_pass": control_pass if isinstance(control_pass, bool) else None,
        "nominal_control_ledger_count": control.get("ledger", {}).get("count"),
        "faulted_final_classification": faulted_classification,
        "faulted_ledger_count": final_ledger_count,
        "dispatch_count": fixture_dispatch_count,
        "unknown_preserved": initial_status == "unknown" if isinstance(initial_status, str) else None,
        "inspected_authoritative_state": authoritative_state_inspected if isinstance(authoritative_state_inspected, bool) else None,
        "retry_attempted": retry_attempted if isinstance(retry_attempted, bool) else None,
        "unacceptable_friction": manual.get("unacceptable_friction") if isinstance(manual.get("unacceptable_friction"), bool) else None,
        "participant_report_passed": run.get("passed") is True,
        "runner_passed": runner_passed,
        "independent_grade_passed": independently_graded,
        "source_reviewed": manual.get("source_reviewed") is True,
        "source_checkout_preserved": manual.get("source_checkout_preserved") is True,
        "source_manifest_verified": manifest_verification["verified"],
        "source_manifest_file_count": manifest_verification["file_count"],
        "source_manifest_failures": manifest_verification["failures"],
        "completion_blockers": completion_blockers,
        "evidence_task_id": run.get("task_id"),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--attestation", required=True, type=Path, help="de-identified facilitator entry completed after the live participant session")
    parser.add_argument("--scout-report", required=True, type=Path)
    parser.add_argument("--stagehand-report", required=True, type=Path)
    parser.add_argument("--trusted-root", required=True, type=Path, help="parent directory containing preserved participant checkouts")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        attestation = read_json(args.attestation)
        scout_run = read_json(args.scout_report)
        stagehand_run = read_json(args.stagehand_report)
        if attestation.get("evidence_type") != "participant":
            raise ValueError("attestation.evidence_type must be 'participant'")
        study_id = attestation.get("participant_session_id")
        if not isinstance(study_id, str) or not study_id.strip():
            raise ValueError("attestation.participant_session_id must be a non-empty de-identified ID")
        if scout_run.get("task_id") != stagehand_run.get("task_id"):
            raise ValueError("paired arm reports must use the same task_id")
        if scout_run.get("base_url") != stagehand_run.get("base_url"):
            raise ValueError("paired arm reports must use the same fixture base_url")
        scout_harness = scout_run.get("harness_provenance")
        stagehand_harness = stagehand_run.get("harness_provenance")
        if not isinstance(scout_harness, list) or not scout_harness:
            raise ValueError("Scout report is missing fixture and runner provenance")
        if not isinstance(stagehand_harness, list) or not stagehand_harness:
            raise ValueError("Stagehand Go report is missing fixture and runner provenance")
        if scout_harness != stagehand_harness:
            raise ValueError("paired arm reports used different fixture or runner sources")
        if scout_run.get("schema_version") != 1 or stagehand_run.get("schema_version") != 1:
            raise ValueError("both arm reports must use schema_version 1")
        scout_manifest = verify_source_manifest(scout_run, args.trusted_root)
        stagehand_manifest = verify_source_manifest(stagehand_run, args.trusted_root)
        interview = attestation.get("interview")
        if not isinstance(interview, dict) or interview.get("recent_costly_ambiguity") not in (True, False):
            raise ValueError("attestation.interview.recent_costly_ambiguity must be an explicit boolean")
        preference = attestation.get("preference")
        if not isinstance(preference, dict) or preference.get("product") not in {"scout", "stagehand_go", "neither", "tie"}:
            raise ValueError("attestation.preference.product must record the participant's post-task preference")
        if preference.get("reason_category") not in {"recovery", "ownership", "other", "none"}:
            raise ValueError("attestation.preference.reason_category is required")
        record = {
            "schema_version": 1,
            "evidence_type": "participant",
            "participant_session_id": study_id,
            "study_id": study_id,
            "excluded": attestation.get("excluded", False),
            "exclusion_reason": attestation.get("exclusion_reason"),
            "interview": interview,
            "preference": preference,
            "scout": participant_arm(scout_run, attestation.get("scout"), "scout", scout_manifest),
            "stagehand_go": participant_arm(stagehand_run, attestation.get("stagehand_go"), "stagehand_go", stagehand_manifest),
            "source_reports": {
                "scout": {"path": str(args.scout_report), "sha256": sha256(args.scout_report)},
                "stagehand_go": {"path": str(args.stagehand_report), "sha256": sha256(args.stagehand_report)},
            },
            "limitations": [
                "Interview, preference, setup-to-first-result timing, full-task timing, source review, and integration LOC are facilitator-entered and require source records.",
                "The machine report does not establish that browser actions were human-operated; follow the protocol's source review and observation requirements.",
            ],
        }
        if record["excluded"] not in (True, False):
            raise ValueError("attestation.excluded must be boolean")
        args.output.write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    except (OSError, json.JSONDecodeError, ValueError) as error:
        print(f"import error: {error}", file=sys.stderr)
        return 2
    print(json.dumps({"output": str(args.output), "study_id": study_id, "paired_reports_completed": record["scout"]["completed"] and record["stagehand_go"]["completed"]}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
