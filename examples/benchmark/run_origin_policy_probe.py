#!/usr/bin/env python3
"""Build Scout and probe unset, empty, malformed, and valid origin policy."""

from __future__ import annotations

import json
import os
import platform
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path
import argparse


ROOT = Path(__file__).resolve().parents[2]


def run(binary: Path, args: list[str], configured: str | None) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    if configured is None:
        env.pop("SCOUT_MCP_ALLOWED_ORIGINS", None)
    else:
        env["SCOUT_MCP_ALLOWED_ORIGINS"] = configured
    return subprocess.run(
        [str(binary), *args], cwd=ROOT, env=env, capture_output=True, text=True, timeout=15
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--output",
        type=Path,
        help="write the report here (defaults to stdout)",
    )
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="scout-origin-policy-") as temp_dir:
        binary = Path(temp_dir) / "scout"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/scout"], cwd=ROOT, check=True)
        cases = [
            ("unset", None, "ok"),
            ("blank", "", "error"),
            ("whitespace", "   ", "error"),
            ("comma_only", ",", "error"),
            ("valid", "https://example.com", "ok"),
        ]
        observations = []
        for label, value, expected_policy_status in cases:
            doctor = run(binary, ["doctor", "--json"], value)
            report = json.loads(doctor.stdout)
            policy = next(check for check in report["checks"] if check["name"] == "origin_policy")
            if policy["status"] != expected_policy_status:
                raise RuntimeError(f"doctor {label}: expected {expected_policy_status}, got {policy['status']}")
            expected_exit = 1 if expected_policy_status == "error" else 0
            if doctor.returncode != expected_exit:
                raise RuntimeError(f"doctor {label}: expected exit {expected_exit}, got {doctor.returncode}")
            observation = {
                "case": label,
                "raw_environment_value": value,
                "expected_policy_status": expected_policy_status,
                "doctor_exit_code": doctor.returncode,
                "doctor_overall_status": report["status"],
                "origin_policy_status": policy["status"],
                "origin_policy_detail": policy["detail"],
                "doctor_stdout": doctor.stdout,
                "doctor_stderr": doctor.stderr,
                "doctor_stdout_bytes_utf8": len(doctor.stdout.encode("utf-8")),
            }
            if label in {"blank", "whitespace", "comma_only"}:
                startup = run(binary, ["mcp", "serve"], value)
                if startup.returncode == 0:
                    raise RuntimeError(f"MCP startup {label} unexpectedly succeeded")
                observation["startup_exit_code"] = startup.returncode
                observation["startup_error"] = startup.stderr.strip()
                observation["startup_stdout"] = startup.stdout
                observation["startup_stderr"] = startup.stderr
                observation["startup_stdout_bytes_utf8"] = len(startup.stdout.encode("utf-8"))
                observation["startup_stderr_bytes_utf8"] = len(startup.stderr.encode("utf-8"))
            observations.append(observation)

        report = {
            "schema_version": 1,
            "scenario": "mcp-origin-policy-empty-configuration",
            "observed_at_utc": datetime.now(timezone.utc).isoformat(timespec="seconds"),
            "go_version": subprocess.run(["go", "version"], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip(),
            "platform": f"{platform.system().lower()}/{platform.machine()}",
            "source": "current worktree",
            "output_byte_definition": "UTF-8 bytes of captured stdout/stderr, excluding process framing",
            "predicates": {
                "unset_and_valid_values_accepted": all(
                    row["origin_policy_status"] == "ok"
                    for row in observations
                    if row["case"] in {"unset", "valid"}
                ),
                "explicit_empty_values_rejected": all(
                    row["origin_policy_status"] == "error"
                    and row["doctor_exit_code"] == 1
                    and row["startup_exit_code"] != 0
                    for row in observations
                    if row["case"] in {"blank", "whitespace", "comma_only"}
                ),
            },
            "results": observations,
            "limitations": [
                "CLI doctor and startup configuration only; no browser navigation was attempted.",
                "A valid origin is checked by doctor but is not used to start a persistent MCP server.",
                "One local run does not establish release-artifact behavior or comparative reliability.",
            ],
        }
        encoded = json.dumps(report, indent=2) + "\n"
        if args.output:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(encoded, encoding="utf-8")
        else:
            print(encoded, end="")


if __name__ == "__main__":
    main()
