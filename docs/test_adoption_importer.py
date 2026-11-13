"""Regression tests for participant evidence import provenance gates."""

import contextlib
import hashlib
import importlib.util
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("import_adoption_session.py")
SPEC = importlib.util.spec_from_file_location("import_adoption_session", SCRIPT)
IMPORTER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(IMPORTER)


class AdoptionImporterProvenanceTests(unittest.TestCase):
    def test_manifest_requires_checkout_under_trusted_root_and_unchanged_content(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "trusted"
            checkout = root / "participant"
            checkout.mkdir(parents=True)
            source = checkout / "main.go"
            source.write_text("package main\n", encoding="utf-8")
            payload = source.read_bytes()
            report = {
                "workdir": str(checkout),
                "source_provenance": {
                    "manifest_algorithm": "sha256",
                    "manifest_status": "captured",
                    "files": [{
                        "path": "main.go",
                        "sha256": hashlib.sha256(payload).hexdigest(),
                        "size_bytes": len(payload),
                    }],
                },
            }
            self.assertTrue(IMPORTER.verify_source_manifest(report, root)["verified"])

            source.write_text("package main // changed\n", encoding="utf-8")
            changed = IMPORTER.verify_source_manifest(report, root)
            self.assertFalse(changed["verified"])
            self.assertIn("manifest_file_changed", changed["failures"])

            report["workdir"] = str(Path(tmp) / "outside")
            outside = IMPORTER.verify_source_manifest(report, root)
            self.assertFalse(outside["verified"])
            self.assertIn("workdir_outside_trusted_root", outside["failures"])

    def test_manifest_rejects_path_escape(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "trusted"
            checkout = root / "participant"
            checkout.mkdir(parents=True)
            outside = root / "outside.go"
            outside.write_text("package outside\n", encoding="utf-8")
            report = {
                "workdir": str(checkout),
                "source_provenance": {
                    "manifest_algorithm": "sha256",
                    "manifest_status": "captured",
                    "files": [{
                        "path": "../outside.go",
                        "sha256": hashlib.sha256(outside.read_bytes()).hexdigest(),
                        "size_bytes": outside.stat().st_size,
                    }],
                },
            }
            result = IMPORTER.verify_source_manifest(report, root)
            self.assertFalse(result["verified"])
            self.assertIn("unsafe_or_duplicate_manifest_path", result["failures"])

    def test_manifest_rejects_noncanonical_path_spellings(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "trusted"
            checkout = root / "participant"
            checkout.mkdir(parents=True)
            source = checkout / "main.go"
            source.write_text("package main\\n", encoding="utf-8")
            payload = source.read_bytes()
            for relative in ("./main.go", "nested/../main.go", "nested\\..\\main.go"):
                report = {
                    "workdir": str(checkout),
                    "source_provenance": {
                        "manifest_algorithm": "sha256",
                        "manifest_status": "captured",
                        "files": [{
                            "path": relative,
                            "sha256": hashlib.sha256(payload).hexdigest(),
                            "size_bytes": len(payload),
                        }],
                    },
                }
                result = IMPORTER.verify_source_manifest(report, root)
                self.assertFalse(result["verified"], relative)
                self.assertIn("unsafe_or_duplicate_manifest_path", result["failures"])

    def test_pair_with_different_task_ids_is_rejected_before_import(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            reports = []
            for name, task_id in (("scout", "task-a"), ("stagehand", "task-b")):
                path = tmp_path / f"{name}.json"
                path.write_text(json.dumps({"task_id": task_id}), encoding="utf-8")
                reports.append(path)
            attestation = tmp_path / "attestation.json"
            attestation.write_text(json.dumps({
                "evidence_type": "participant",
                "participant_session_id": "P01",
            }), encoding="utf-8")
            output = tmp_path / "normalized.json"
            argv = [
                str(SCRIPT), "--attestation", str(attestation),
                "--scout-report", str(reports[0]),
                "--stagehand-report", str(reports[1]),
                "--trusted-root", str(tmp_path), "--output", str(output),
            ]
            stderr = io.StringIO()
            with mock.patch("sys.argv", argv), contextlib.redirect_stderr(stderr):
                code = IMPORTER.main()
            self.assertEqual(code, 2)
            self.assertIn("paired arm reports must use the same task_id", stderr.getvalue())
            self.assertFalse(output.exists())

    def test_pair_with_same_task_id_and_harness_is_importable(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = Path(tmp)
            harness = [{"path": "main.go", "sha256": "a" * 64, "size_bytes": 10}]
            reports = []
            for name in ("scout", "stagehand"):
                path = tmp_path / f"{name}.json"
                path.write_text(json.dumps({
                    "schema_version": 1,
                    "task_id": "P01-approval",
                    "base_url": "http://127.0.0.1:41990",
                    "harness_provenance": harness,
                }), encoding="utf-8")
                reports.append(path)
            attestation = tmp_path / "attestation.json"
            attestation.write_text(json.dumps({
                "evidence_type": "participant",
                "participant_session_id": "P01",
                "interview": {"recent_costly_ambiguity": False},
                "preference": {"product": "tie", "reason_category": "none"},
                "scout": {
                    "first_verified_seconds": 1,
                    "full_task_seconds": 2,
                    "integration_loc": 3,
                    "unacceptable_friction": False,
                },
                "stagehand_go": {
                    "first_verified_seconds": 1,
                    "full_task_seconds": 2,
                    "integration_loc": 3,
                },
            }), encoding="utf-8")
            output = tmp_path / "normalized.json"
            argv = [
                str(SCRIPT), "--attestation", str(attestation),
                "--scout-report", str(reports[0]),
                "--stagehand-report", str(reports[1]),
                "--trusted-root", str(tmp_path), "--output", str(output),
            ]
            stderr = io.StringIO()
            with mock.patch("sys.argv", argv), contextlib.redirect_stderr(stderr):
                code = IMPORTER.main()
            self.assertEqual(code, 0, stderr.getvalue())
            record = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(record["scout"]["evidence_task_id"], "P01-approval")
            self.assertEqual(record["stagehand_go"]["evidence_task_id"], "P01-approval")


if __name__ == "__main__":
    unittest.main()
