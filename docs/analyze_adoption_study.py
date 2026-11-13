#!/usr/bin/env python3
"""Analyze de-identified adoption-study session records.

Input is one JSON file per participant. See adoption-study.md for the schema.
This intentionally does not ingest facilitator adapter reports as participant
data and treats missing fields as unavailable evidence, never as success.
"""

import argparse
import json
import math
import statistics
import sys
from pathlib import Path


def truth(value):
    return value is True or (isinstance(value, str) and value.strip().lower() in {"yes", "true", "1"})


def reject_nonstandard_constant(value):
    raise ValueError(f"non-standard JSON numeric constant {value}")


def parse_finite_float(value):
    number = float(value)
    if not math.isfinite(number):
        raise ValueError(f"non-finite JSON number {value}")
    return number


def median(values):
    return statistics.median(values) if values else None


def finite_number(value):
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        return False
    try:
        return math.isfinite(value)
    except OverflowError:
        return False


def required_count(n, threshold=0.8):
    return math.ceil(n * threshold)


def valid_arm(arm):
    return isinstance(arm, dict) and arm.get("completed") is True and has_safety_evidence(arm)


def has_safety_evidence(arm):
    return (
        isinstance(arm.get("faulted_final_classification"), str)
        and isinstance(arm.get("unknown_preserved"), bool)
        and isinstance(arm.get("inspected_authoritative_state"), bool)
        and isinstance(arm.get("retry_attempted"), bool)
        and finite_number(arm.get("faulted_ledger_count"))
        and finite_number(arm.get("dispatch_count"))
    )


def load_sessions(paths):
    records = []
    for path in paths:
        data = json.loads(
            path.read_text(encoding="utf-8"),
            parse_constant=reject_nonstandard_constant,
            parse_float=parse_finite_float,
        )
        if not isinstance(data, dict):
            raise ValueError(f"{path}: expected a JSON object")
        if not data.get("study_id"):
            raise ValueError(f"{path}: missing study_id")
        if data.get("evidence_type") != "participant" or data.get("participant_session_id") != data.get("study_id"):
            raise ValueError(f"{path}: participant evidence_type and matching participant_session_id are required; import a facilitator-attested pair first")
        if data.get("excluded") not in (None, True, False):
            raise ValueError(f"{path}: excluded must be a boolean when supplied")
        interview = data.get("interview", {})
        if not isinstance(interview, dict):
            raise ValueError(f"{path}: interview must be an object")
        if "recent_costly_ambiguity" in interview and interview["recent_costly_ambiguity"] not in (True, False):
            raise ValueError(f"{path}: interview.recent_costly_ambiguity must be a boolean")
        for arm_name in ("scout", "stagehand_go"):
            arm = data.get(arm_name, {})
            if not isinstance(arm, dict):
                raise ValueError(f"{path}: {arm_name} must be an object")
            if arm_name == "scout" and "unacceptable_friction" in arm and not isinstance(arm["unacceptable_friction"], bool):
                raise ValueError(f"{path}: scout.unacceptable_friction must be a boolean")
            for field in ("unknown_preserved", "inspected_authoritative_state", "retry_attempted"):
                if field in arm and arm[field] is not None and not isinstance(arm[field], bool):
                    raise ValueError(f"{path}: {arm_name}.{field} must be a boolean")
            if "faulted_final_classification" in arm and arm["faulted_final_classification"] is not None and not isinstance(arm["faulted_final_classification"], str):
                raise ValueError(f"{path}: {arm_name}.faulted_final_classification must be a string")
            for field in ("first_verified_seconds", "full_task_seconds", "integration_loc", "faulted_ledger_count", "dispatch_count"):
                value = arm.get(field)
                if value is not None and (not finite_number(value) or value < 0):
                    raise ValueError(f"{path}: {arm_name}.{field} must be a non-negative number")
        preference = data.get("preference")
        if preference is not None and not isinstance(preference, dict):
            raise ValueError(f"{path}: preference must be an object")
        records.append(data)
    ids = [row["study_id"] for row in records]
    if len(ids) != len(set(ids)):
        raise ValueError("duplicate study_id values")
    return records


def summarize(records):
    demand = [row for row in records if row.get("excluded") is not True and row.get("interview", {}).get("recent_costly_ambiguity") is True]
    control_records = []
    pairs = []
    paired_effort = {"first_verified_seconds": [], "full_task_seconds": [], "integration_loc": []}
    preference = []
    unacceptable = []
    evaluable_friction = []
    safety_failures = []
    exclusions = []
    paired_missing = []

    for row in records:
        pid = row["study_id"]
        if row.get("excluded") is True:
            exclusions.append({"study_id": pid, "reason": row.get("exclusion_reason")})
            continue
        scout, comp = row.get("scout", {}), row.get("stagehand_go", {})
        if (
            isinstance(scout, dict) and isinstance(comp, dict)
            and scout.get("completed") is True
            and comp.get("completed") is True
            and isinstance(scout.get("nominal_control_pass"), bool)
            and isinstance(comp.get("nominal_control_pass"), bool)
        ):
            control_records.append(row)
        for product, arm in (("Scout", scout), ("Stagehand Go", comp)):
            explicit_failure = (
                (arm.get("faulted_final_classification") is not None and arm.get("faulted_final_classification") != "verified")
                or arm.get("unknown_preserved") is False
                or arm.get("inspected_authoritative_state") is False
                or arm.get("retry_attempted") is True
                or (arm.get("faulted_ledger_count") is not None and arm.get("faulted_ledger_count") != 1)
                or (arm.get("dispatch_count") is not None and arm.get("dispatch_count") != 1)
            )
            if not has_safety_evidence(arm) and not explicit_failure:
                continue
            safe = (
                arm.get("faulted_final_classification") == "verified"
                and arm.get("unknown_preserved") is True
                and arm.get("inspected_authoritative_state") is True
                and arm.get("retry_attempted") is False
                and arm.get("faulted_ledger_count") == 1
                and arm.get("dispatch_count") == 1
            )
            if not safe:
                safety_failures.append({
                    "study_id": pid,
                    "product": product,
                    "reason": f"{product} faulted task did not meet the no-replay reconciliation contract",
                })
        if valid_arm(scout) and valid_arm(comp):
            pairs.append(row)
            for key, values in paired_effort.items():
                left, right = scout.get(key), comp.get(key)
                if isinstance(left, (int, float)) and isinstance(right, (int, float)):
                    values.append((left, right))
        else:
            paired_missing.append(pid)
        pref = row.get("preference")
        if isinstance(pref, dict) and pref.get("product"):
            preference.append(pref)
        if isinstance(scout.get("unacceptable_friction"), bool):
            evaluable_friction.append(row)
            if scout["unacceptable_friction"]:
                unacceptable.append(pid)

    evaluable_demand = [row for row in records if row.get("excluded") is not True and row.get("interview", {}).get("recent_costly_ambiguity") in (True, False)]
    evaluable_control_pairs = control_records
    control_passes = [row for row in evaluable_control_pairs if truth(row["scout"].get("nominal_control_pass")) and truth(row["stagehand_go"].get("nominal_control_pass"))]
    evaluable_preference = [p for p in preference if p.get("product") in {"scout", "stagehand_go", "neither", "tie"}]
    scout_recovery_preference = [p for p in evaluable_preference if p.get("product") == "scout" and p.get("reason_category") in {"recovery", "ownership"}]
    def lower_25(pairs_):
        ratios = [s / c for s, c in pairs_ if c > 0]
        return bool(ratios) and median(ratios) <= 0.75

    enough_n = len(evaluable_demand) >= 5
    demand_pass = bool(evaluable_demand) and sum(row["interview"]["recent_costly_ambiguity"] is True for row in evaluable_demand) >= required_count(len(evaluable_demand))
    control_pass = bool(evaluable_control_pairs) and len(control_passes) >= required_count(len(evaluable_control_pairs))
    effort_pass = lower_25(paired_effort["first_verified_seconds"]) or lower_25(paired_effort["full_task_seconds"]) or lower_25(paired_effort["integration_loc"])
    preference_pass = bool(evaluable_preference) and len(scout_recovery_preference) >= required_count(len(evaluable_preference))
    correctness_required = required_count(len(control_passes)) if control_passes else None
    correctness_pass = control_pass and bool(control_passes) and not safety_failures
    friction_pass = not unacceptable
    complete_friction_evidence = len(evaluable_friction) == len([row for row in records if row.get("excluded") is not True])
    decision = "inconclusive"
    participant_count = len([row for row in records if row.get("excluded") is not True])
    complete_task_evidence = (
        len(evaluable_control_pairs) == participant_count
        and len(pairs) == participant_count
        and control_pass
        and bool(control_passes)
    )
    complete_preference_evidence = len(evaluable_preference) == len([row for row in records if row.get("excluded") is not True])
    if any(failure["product"] == "Scout" for failure in safety_failures):
        decision = "product_correctness_failure"
    elif safety_failures:
        decision = "comparator_safety_gate_failed"
    elif enough_n and evaluable_demand and evaluable_control_pairs:
        if not demand_pass:
            decision = "do_not_lead_with_recovery"
        elif any(not valid_arm(row.get("scout")) or not valid_arm(row.get("stagehand_go")) for row in records if row.get("excluded") is not True):
            decision = "inconclusive_missing_required_evidence"
        elif len(evaluable_control_pairs) < 5:
            decision = "inconclusive_insufficient_paired_control_evidence"
        elif not control_pass:
            decision = "task_correctness_gate_failed"
        elif not complete_task_evidence or not complete_preference_evidence or not complete_friction_evidence:
            decision = "inconclusive_missing_required_evidence"
        elif correctness_pass and friction_pass and (effort_pass or preference_pass):
            decision = "advance_recovery_position_directionally"
        elif not friction_pass:
            decision = "resolve_unacceptable_friction"
        else:
            decision = "retain_as_engineering_strength; investigate_effort_or_preference"

    return {
        "schema_version": 1,
        "source": "participant_session_records_only",
        "participant_count": len(records),
        "excluded": exclusions,
        "missing_paired_arm": paired_missing,
        "denominators": {
            "demand": len(evaluable_demand),
            "paired_products": len(pairs),
            "nominal_control_pairs": len(evaluable_control_pairs),
            "control_competent_pairs": len(control_passes),
            "preference": len(evaluable_preference),
            "scout_friction": len(evaluable_friction),
            "scout_friction_required": participant_count,
        },
        "missing_evidence": [
            {"study_id": row["study_id"], "fields": [
                field for field, present in (
                    ("interview.recent_costly_ambiguity", row.get("interview", {}).get("recent_costly_ambiguity") in (True, False)),
                    ("scout_arm_completed", valid_arm(row.get("scout"))),
                    ("stagehand_go_arm_completed", valid_arm(row.get("stagehand_go"))),
                    ("scout_fault_safety_evidence", has_safety_evidence(row.get("scout", {}))),
                    ("stagehand_go_fault_safety_evidence", has_safety_evidence(row.get("stagehand_go", {}))),
                    ("scout_nominal_control_evaluated", isinstance(row.get("scout", {}).get("nominal_control_pass"), bool)),
                    ("stagehand_go_nominal_control_evaluated", isinstance(row.get("stagehand_go", {}).get("nominal_control_pass"), bool)),
                    ("preference", isinstance(row.get("preference"), dict) and row["preference"].get("product") in {"scout", "stagehand_go", "neither", "tie"}),
                    ("scout_unacceptable_friction_evaluated", isinstance(row.get("scout", {}).get("unacceptable_friction"), bool)),
                ) if not present
            ], "scout_completion_blockers": row.get("scout", {}).get("completion_blockers", []),
                "stagehand_go_completion_blockers": row.get("stagehand_go", {}).get("completion_blockers", [])
            } for row in records if row.get("excluded") is not True
        ],
        "threshold_counts": {
            "demand_required": required_count(len(evaluable_demand)) if evaluable_demand else None,
            "control_required": required_count(len(evaluable_control_pairs)) if evaluable_control_pairs else None,
            "scout_correctness_required_within_control_competent_pairs": correctness_required,
            "preference_required": required_count(len(evaluable_preference)) if evaluable_preference else None,
        },
        "numerators": {
            "recent_costly_ambiguity": sum(row["interview"]["recent_costly_ambiguity"] is True for row in evaluable_demand),
            "both_nominal_controls_passed": len(control_passes),
            "scout_prefers_recovery_or_ownership": len(scout_recovery_preference),
            "scout_no_replay_contract_failures": sum(failure["product"] == "Scout" for failure in safety_failures),
            "stagehand_go_no_replay_contract_failures": sum(failure["product"] == "Stagehand Go" for failure in safety_failures),
            "unacceptable_scout_friction": len(unacceptable),
        },
        "paired_medians": {
            key: {
                "n": len(values),
                "scout": median([s for s, _ in values]),
                "stagehand_go": median([c for _, c in values]),
                "median_paired_ratio_scout_to_stagehand": median([s / c for s, c in values if c > 0]),
            } for key, values in paired_effort.items()
        },
        "safety_failures": safety_failures,
        "decision": decision,
        "decision_rule": "5+ evaluable participants; rounded-up 80% demand and nominal-control thresholds; among control-competent pairs, no completed or explicitly unsafe faulted arm may show duplicate effects, false completion, or replay before authoritative inspection; attribute failures to the product arm, and block a comparative claim on either Scout or comparator failures; 25% lower paired median time-to-first-result or full-task integration effort, or rounded-up 80% recovery/ownership preference; an explicit friction assessment is required for every evaluable participant and none may report unacceptable Scout friction",
        "limitations": ["Directional formative evidence only; not a representative market estimate.", "No facilitator adapter run is accepted by this analyzer as participant evidence.", "Source review and independent session audit remain separate checks."],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("sessions", nargs="*", type=Path, help="de-identified participant JSON records")
    parser.add_argument("--output", type=Path, help="write JSON summary (default: stdout)")
    args = parser.parse_args()
    try:
        result = summarize(load_sessions(args.sessions)) if args.sessions else summarize([])
    except (OSError, json.JSONDecodeError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2
    rendered = json.dumps(result, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    else:
        sys.stdout.write(rendered)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
