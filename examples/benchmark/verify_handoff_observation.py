#!/usr/bin/env python3
"""Independently grade receiver context observations for recovery handoffs.

This checker validates the observation contract and compares it with the
expected owner/browser/target/policy binding supplied by a trusted caller. It
does not authenticate the observation source or establish application outcome.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path


FIELDS = {
    "owner_scope",
    "browser_generation",
    "target_id",
    "frame_id",
    "frame_binding_known",
    "policy_id",
    "policy_digest",
}
REQUIRED = FIELDS - {"frame_id"}
EXPECTED_REQUIRED = {"owner_scope", "browser_generation", "target_id", "policy_id", "policy_digest"}
EXPECTED_FIELDS = EXPECTED_REQUIRED | {"frame_id"}


def fail(message: str) -> None:
    raise ValueError(message)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            fail(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def nonempty(value, field):
    if not isinstance(value, str) or not value.strip():
        fail(f"{field} must be a non-empty string")


def audit(observation, expected):
    if not isinstance(observation, dict):
        fail("observation must be an object")
    unknown = set(observation) - FIELDS
    missing = REQUIRED - set(observation)
    if unknown:
        fail(f"observation has unknown fields: {', '.join(sorted(unknown))}")
    if missing:
        fail(f"observation is missing fields: {', '.join(sorted(missing))}")

    for field in ("owner_scope", "browser_generation", "target_id", "policy_id"):
        nonempty(observation[field], field)
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", observation["policy_digest"]):
        fail("policy_digest must be a lowercase SHA-256 digest")
    if not isinstance(observation["frame_binding_known"], bool):
        fail("frame_binding_known must be boolean")
    if "frame_id" in observation and not isinstance(observation["frame_id"], str):
        fail("frame_id must be a string")
    if observation["frame_binding_known"] and "frame_id" not in observation:
        fail("frame_id is required when frame_binding_known is true")
    if not observation["frame_binding_known"] and observation.get("frame_id", ""):
        fail("frame_id must be empty when frame_binding_known is false")

    if not isinstance(expected, dict):
        fail("expected binding must be an object")
    unknown_expected = set(expected) - EXPECTED_FIELDS
    missing_expected = EXPECTED_REQUIRED - set(expected)
    if unknown_expected:
        fail(f"expected binding has unknown fields: {', '.join(sorted(unknown_expected))}")
    if missing_expected:
        fail(f"expected binding is missing fields: {', '.join(sorted(missing_expected))}")
    for field in EXPECTED_FIELDS - {"frame_id"}:
        nonempty(expected[field], f"expected.{field}")
    if "frame_id" in expected and not isinstance(expected["frame_id"], str):
        fail("expected.frame_id must be a string")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", expected["policy_digest"]):
        fail("expected.policy_digest must be a lowercase SHA-256 digest")

    fields = sorted(EXPECTED_FIELDS - {"frame_id"})
    mismatches = [field for field in fields if observation[field] != expected[field]]
    frame_match = (
        observation["frame_binding_known"]
        and "frame_id" in observation
        and "frame_id" in expected
        and observation["frame_id"] == expected["frame_id"]
    )
    return {
        "valid": True,
        "context_matched": not mismatches and frame_match,
        "mismatched_fields": mismatches,
        "frame_binding_known": observation["frame_binding_known"],
        "frame_matched": frame_match,
        "producer_authenticated": False,
        "application_outcome_included": False,
        "replay_allowed": False,
        "note": "Identity fields match structurally; authenticate the observation source and check application state independently before recovery.",
    }


def audit_application_oracle(oracle, expected_task_id):
    fields = {"task_id", "source", "observed_at", "accepted_effects", "expected_effects", "authoritative"}
    required = fields
    if not isinstance(oracle, dict):
        fail("application oracle must be an object")
    unknown = set(oracle) - fields
    missing = required - set(oracle)
    if unknown:
        fail(f"application oracle has unknown fields: {', '.join(sorted(unknown))}")
    if missing:
        fail(f"application oracle is missing fields: {', '.join(sorted(missing))}")
    for field in ("task_id", "source", "observed_at"):
        nonempty(oracle[field], f"oracle.{field}")
    if oracle["task_id"] != expected_task_id:
        fail("application oracle task_id does not match expected task")
    if oracle["authoritative"] is not True:
        fail("application oracle must be explicitly marked authoritative")
    for field in ("accepted_effects", "expected_effects"):
        value = oracle[field]
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            fail(f"oracle.{field} must be a non-negative integer")
    if oracle["accepted_effects"] == oracle["expected_effects"]:
        outcome = "verified_complete"
    elif oracle["accepted_effects"] > oracle["expected_effects"]:
        outcome = "duplicate_or_excess_effects"
    else:
        outcome = "incomplete_or_unresolved"
    return {
        "oracle_valid": True,
        "oracle_outcome": outcome,
        "task_id": oracle["task_id"],
        "evidence_source": oracle["source"],
        "observed_at": oracle["observed_at"],
        "accepted_effects": oracle["accepted_effects"],
        "expected_effects": oracle["expected_effects"],
        "application_outcome_independently_observed": True,
        "note": "This conclusion is only as trustworthy as the authoritative oracle and its transport/authentication.",
    }


def main():
    if len(sys.argv) not in (3, 5):
        print("usage: verify_handoff_observation.py OBSERVATION.json EXPECTED_BINDING.json [ORACLE.json EXPECTED_TASK_ID]", file=sys.stderr)
        return 2
    try:
        observation = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"), object_pairs_hook=unique_object)
        expected = json.loads(Path(sys.argv[2]).read_text(encoding="utf-8"), object_pairs_hook=unique_object)
        result = audit(observation, expected)
        if len(sys.argv) == 5:
            oracle = json.loads(Path(sys.argv[3]).read_text(encoding="utf-8"), object_pairs_hook=unique_object)
            result["application_oracle"] = audit_application_oracle(oracle, sys.argv[4])
            result["application_outcome_included"] = True
    except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as exc:
        print(json.dumps({"valid": False, "error": str(exc)}, separators=(",", ":")), file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0 if result["context_matched"] else 3


if __name__ == "__main__":
    raise SystemExit(main())
