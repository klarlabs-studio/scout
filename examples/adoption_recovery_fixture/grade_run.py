#!/usr/bin/env python3
"""Independently grades an adoption-study task record against fixture ledger."""
import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request


def fetch_ledger(base_url, task_id):
    query = urllib.parse.urlencode({"task": task_id})
    request = urllib.request.Request(
        f"{base_url.rstrip('/')}/ledger?{query}", headers={"Accept": "application/json"}
    )
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("base_url")
    parser.add_argument("task_id")
    parser.add_argument("run_json", help="facilitator run JSON with reconciliation outcome fields")
    args = parser.parse_args()
    with open(args.run_json, encoding="utf-8") as handle:
        run = json.load(handle)
    ledger = fetch_ledger(args.base_url, args.task_id)
    reconciliation = run.get("reconcile", {}).get("result", run)
    initial_status = reconciliation.get("initial_status", run.get("initial_status"))
    ledger_inspected = reconciliation.get("ledger_inspected", run.get("ledger_inspected"))
    final_classification = reconciliation.get("status", run.get("final_classification"))
    retry_attempted = reconciliation.get("retry_attempted", run.get("retry_attempted"))
    reported_dispatch_count = reconciliation.get("dispatch_count")
    accepted_dispatch_count = ledger.get("accepted_dispatch_count")
    attempts = ledger.get("attempts", [])
    checks = {
        "ledger_task_matches": ledger.get("task_id") == args.task_id,
        "participant_task_identity_matches": reconciliation.get("task_id") == args.task_id,
        "exactly_one_authoritative_effect": ledger.get("count") == 1,
        "no_blind_retry": retry_attempted is False,
        "single_committed_effect": ledger.get("count") == 1,
        "uncertainty_preserved_before_reconciliation": initial_status == "unknown",
        "authoritative_ledger_inspected": ledger_inspected is True,
        "reported_ledger_count_matches": reconciliation.get("ledger_count") == ledger.get("count"),
        "reported_dispatch_count_matches_fixture": reported_dispatch_count == accepted_dispatch_count,
        "accepted_dispatches_match_committed_effects": accepted_dispatch_count == ledger.get("count"),
        "no_rejected_mutation_attempts": all(item.get("accepted") is True for item in attempts),
        "attempt_count_matches_fixture_events": ledger.get("attempt_count") == len(attempts),
        "final_classification_matches_ledger": final_classification == ("verified" if ledger.get("count") == 1 else "rejected"),
    }
    first_success = run.get("first_success", {})
    first_observation = first_success.get("fixture_observation", {})
    first_report = first_success.get("result", {})
    checks.update({
        "first_success_rendered_postcondition_observed": first_observation.get("status") == "Complete",
        "first_success_completion_observation_count_one": first_observation.get("complete_click_count") == 1,
        "first_success_report_matches_observation": (
            first_report.get("status") == "verified"
            and first_report.get("verified_text") == "Complete"
        ),
    })
    report = {
        "schema_version": 1,
        "task_id": args.task_id,
        "ledger": ledger,
        "checks": checks,
        "passed": all(checks.values()),
        "scope": "independent fixture-ledger grading of common observable behavior, accepted dispatches, and rejected mutation attempts; does not verify participant statements about source code, native records, host journals, elapsed time, or browser interactions that never reached the fixture",
    }
    print(json.dumps(report, indent=2))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, urllib.error.URLError, json.JSONDecodeError) as error:
        print(f"grader error: {error}", file=sys.stderr)
        raise SystemExit(2)
