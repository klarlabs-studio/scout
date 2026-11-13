"""Regression tests for participant evidence inventory classification."""

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("audit_adoption_evidence.py")
SPEC = importlib.util.spec_from_file_location("audit_adoption_evidence", SCRIPT)
AUDIT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(AUDIT)


class AdoptionEvidenceAuditTests(unittest.TestCase):
    def run_audit(self, reports):
        with mock.patch("sys.argv", [str(SCRIPT), *map(str, reports)]):
            output = []
            with mock.patch("builtins.print", side_effect=lambda value: output.append(value)):
                code = AUDIT.main()
        return code, json.loads(output[0])

    def test_only_matching_attested_participant_records_are_included(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            valid = root / "valid.json"
            valid.write_text(json.dumps({
                "evidence_type": "participant",
                "participant_session_id": "P01",
                "study_id": "P01",
            }), encoding="utf-8")
            mismatched = root / "mismatched.json"
            mismatched.write_text(json.dumps({
                "evidence_type": "participant",
                "participant_session_id": "P02",
                "study_id": "P03",
            }), encoding="utf-8")
            rehearsal = root / "rehearsal.json"
            rehearsal.write_text(json.dumps({"study_id": "fixture"}), encoding="utf-8")

            code, result = self.run_audit([valid, mismatched, rehearsal])

        self.assertEqual(code, 0)
        self.assertEqual(result["participant_report_count"], 1)
        self.assertEqual(result["included_participant_reports"][0]["participant_session_id"], "P01")
        self.assertEqual(len(result["excluded_nonparticipant_reports"]), 2)

    def test_invalid_json_is_reported_and_returns_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            invalid = Path(tmp) / "invalid.json"
            invalid.write_text("{", encoding="utf-8")
            code, result = self.run_audit([invalid])

        self.assertEqual(code, 2)
        self.assertEqual(len(result["invalid_reports"]), 1)
        self.assertEqual(result["participant_report_count"], 0)


if __name__ == "__main__":
    unittest.main()
