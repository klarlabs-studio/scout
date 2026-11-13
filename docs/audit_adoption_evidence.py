#!/usr/bin/env python3
"""Inventory adoption-study evidence without promoting rehearsals to users."""

import argparse
import json
from pathlib import Path


PARTICIPANT_MARKER = "participant_session_id"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("reports", nargs="*", type=Path, help="run JSON reports to classify")
    args = parser.parse_args()
    included = []
    excluded = []
    invalid = []
    for path in args.reports:
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            invalid.append({"path": str(path), "error": str(error)})
            continue
        if not isinstance(data, dict):
            invalid.append({"path": str(path), "error": "expected JSON object"})
            continue
        participant_id = data.get(PARTICIPANT_MARKER)
        if (
            data.get("evidence_type") == "participant"
            and isinstance(participant_id, str)
            and participant_id.strip()
            and data.get("study_id") == participant_id
        ):
            included.append({"path": str(path), "participant_session_id": participant_id})
        else:
            excluded.append({
                "path": str(path),
                "evidence_type": data.get("evidence_type", "unclassified"),
                "reason": "requires explicit participant evidence_type and matching non-empty participant_session_id and study_id",
            })
    result = {
        "schema_version": 1,
        "included_participant_reports": included,
        "excluded_nonparticipant_reports": excluded,
        "invalid_reports": invalid,
        "participant_report_count": len(included),
        "decision": "participant_evidence_available" if included else "no_participant_evidence",
        "note": "This inventory does not grade task correctness or adoption thresholds; facilitator, adapter, fixture, and benchmark reports are excluded unless explicitly attested as participant sessions.",
    }
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if not invalid else 2


if __name__ == "__main__":
    raise SystemExit(main())
