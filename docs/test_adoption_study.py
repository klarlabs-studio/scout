"""Regression tests for the adoption-study evidence decision gate."""

import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("analyze_adoption_study.py")
SPEC = importlib.util.spec_from_file_location("analyze_adoption_study", SCRIPT)
ANALYZER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ANALYZER)


def arm(*, friction=None, retry=False, completed=True):
    return {
        "completed": completed,
        "first_verified_seconds": 100,
        "full_task_seconds": 300,
        "integration_loc": 70,
        "nominal_control_pass": True,
        "faulted_final_classification": "verified",
        "faulted_ledger_count": 1,
        "dispatch_count": 1,
        "unknown_preserved": True,
        "inspected_authoritative_state": True,
        "retry_attempted": retry,
        **({"unacceptable_friction": friction} if friction is not None else {}),
    }


def participant(index, *, scout_friction=False, comparator_retry=False, missing_comparator=False):
    comparator = arm(retry=comparator_retry, completed=not missing_comparator)
    if missing_comparator:
        comparator.pop("faulted_ledger_count")
    return {
        "evidence_type": "participant",
        "participant_session_id": f"P{index}",
        "study_id": f"P{index}",
        "excluded": False,
        "interview": {"recent_costly_ambiguity": True},
        "scout": arm(friction=scout_friction),
        "stagehand_go": comparator,
        "preference": {"product": "scout", "reason_category": "recovery"},
    }


class AdoptionStudyDecisionTests(unittest.TestCase):
    def records(self):
        return [participant(i) for i in range(1, 6)]

    def test_complete_safe_pairs_can_advance_directionally(self):
        result = ANALYZER.summarize(self.records())
        self.assertEqual(result["decision"], "advance_recovery_position_directionally")
        self.assertEqual(result["denominators"]["paired_products"], 5)

    def test_missing_friction_assessment_is_inconclusive(self):
        records = self.records()
        records[0]["scout"].pop("unacceptable_friction")
        result = ANALYZER.summarize(records)
        self.assertEqual(result["decision"], "inconclusive_missing_required_evidence")
        self.assertEqual(result["denominators"]["scout_friction"], 4)

    def test_explicit_comparator_replay_blocks_comparison(self):
        records = self.records()
        records[0] = participant(1, comparator_retry=True)
        result = ANALYZER.summarize(records)
        self.assertEqual(result["decision"], "comparator_safety_gate_failed")
        self.assertEqual(result["numerators"]["stagehand_go_no_replay_contract_failures"], 1)

    def test_missing_comparator_safety_field_is_not_mislabeled_as_failure(self):
        records = self.records()
        records[0] = participant(1, missing_comparator=True)
        result = ANALYZER.summarize(records)
        self.assertEqual(result["decision"], "inconclusive_missing_required_evidence")
        self.assertEqual(result["safety_failures"], [])

    def test_incomplete_arm_does_not_enter_nominal_control_denominator(self):
        records = self.records()
        records[0]["scout"]["completed"] = False
        result = ANALYZER.summarize(records)
        self.assertEqual(result["denominators"]["nominal_control_pairs"], 4)
        self.assertEqual(result["decision"], "inconclusive_missing_required_evidence")


if __name__ == "__main__":
    unittest.main()
