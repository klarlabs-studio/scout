import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


RUNNER = Path(__file__).with_name("run_arm.py")
spec = importlib.util.spec_from_file_location("adoption_run_arm", RUNNER)
run_arm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(run_arm)


def git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


class GitWorktreeChangesTest(unittest.TestCase):
    def test_changes_are_scoped_to_nested_participant_checkout(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp) / "repo"
            checkout = repo / "participant"
            checkout.mkdir(parents=True)
            git(repo, "init", "-q")
            git(repo, "config", "user.email", "test@example.invalid")
            git(repo, "config", "user.name", "Test")
            (repo / "repository.txt").write_text("root\n", encoding="utf-8")
            (checkout / "source.go").write_text("package participant\n", encoding="utf-8")
            git(repo, "add", ".")
            git(repo, "commit", "-qm", "initial")

            (repo / "repository.txt").write_text("root changed\n", encoding="utf-8")
            (checkout / "source.go").write_text("package participant // changed\n", encoding="utf-8")
            (checkout / "notes.txt").write_text("participant change\n", encoding="utf-8")

            self.assertEqual(
                run_arm.git_worktree_changes(checkout),
                ["participant/notes.txt", "participant/source.go"],
            )
            self.assertEqual(run_arm.git_revision(checkout), git(repo, "rev-parse", "HEAD"))


class CommandPreflightTest(unittest.TestCase):
    def test_malformed_argv_writes_report_before_fixture_mutation(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "evidence.json"
            result = subprocess.run(
                [
                    sys.executable, str(RUNNER), "http://127.0.0.1:1", "preflight-test",
                    "--first-success-command", "'unterminated",
                    "--control-command", "worker control",
                    "--submit-command", "worker submit",
                    "--reconcile-command", "worker reconcile",
                    "--output", str(output),
                ],
                check=False, capture_output=True, text=True,
            )
            self.assertEqual(result.returncode, 2)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertFalse(report["passed"])
            self.assertEqual(report["events"], [])
            self.assertIn("not valid quoted argv", report["preflight_error"])


if __name__ == "__main__":
    unittest.main()
