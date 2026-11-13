#!/usr/bin/env python3
"""Repeat Scout's durable-task/browser-replacement recovery fixture."""

import argparse
import datetime as dt
import json
import math
from pathlib import Path
import subprocess
import time


ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "examples" / "benchmark" / "results"


def run(command, *, cwd=ROOT, check=True):
    return subprocess.run(
        command, cwd=cwd, text=True, stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT, check=check,
    )


def wilson(successes, total, z=1.959963984540054):
    if total == 0:
        return None
    p = successes / total
    denominator = 1 + z * z / total
    center = (p + z * z / (2 * total)) / denominator
    margin = z * math.sqrt(p * (1 - p) / total + z * z / (4 * total * total)) / denominator
    return [max(0.0, center - margin), min(1.0, center + margin)]


def evaluate(output):
    marker = "Browser process restart after committed mutation;"
    line = next((row for row in output.splitlines() if row.startswith(marker)), "")
    requirements = {
        "replacement_browser": "new_browser=true" in line,
        "retry_blocked": "retry_blocked=true" in line,
        "authoritative_reconciliation": "host_resolution=verified_complete" in line,
        "exactly_four_fixture_mutations": "accepted=4" in line,
    }
    return all(requirements.values()), requirements


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repetitions", type=int, default=30)
    parser.add_argument("--output-dir", type=Path)
    args = parser.parse_args()
    if not 1 <= args.repetitions <= 100:
        parser.error("--repetitions must be between 1 and 100")

    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output_dir = args.output_dir or RESULTS / f"scout-browser-recovery-matrix-{stamp}"
    output_dir = output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=False)
    binary = output_dir / "durable_task_recovery"

    build = run(["go", "build", "-o", str(binary), "./examples/durable_task_recovery"], check=False)
    (output_dir / "build.log").write_text(build.stdout, encoding="utf-8")
    if build.returncode != 0:
        raise SystemExit(f"Scout recovery fixture build failed; see {output_dir / 'build.log'}")

    version = run(["go", "version"], check=False).stdout.strip()
    head = run(["git", "rev-parse", "HEAD"], check=False).stdout.strip()
    dirty = bool(run(["git", "status", "--porcelain"], check=False).stdout.strip())
    runs = []
    for index in range(1, args.repetitions + 1):
        started = time.monotonic()
        result = subprocess.run([str(binary)], cwd=ROOT, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                timeout=120, check=False)
        elapsed_ms = round((time.monotonic() - started) * 1000, 1)
        raw_path = output_dir / f"run-{index:03d}.log"
        raw_path.write_text(result.stdout, encoding="utf-8")
        passed, checks = evaluate(result.stdout)
        passed = passed and result.returncode == 0
        runs.append({
            "run": index,
            "passed": passed,
            "exit_code": result.returncode,
            "elapsed_ms": elapsed_ms,
            "checks": checks,
            "raw_log": raw_path.name,
        })
        print(f"[{index}/{args.repetitions}] {'PASS' if passed else 'FAIL'} ({elapsed_ms:.0f} ms)", flush=True)

    successes = sum(row["passed"] for row in runs)
    report = {
        "schema_version": 1,
        "benchmark": "scout_durable_task_browser_replacement_recovery",
        "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "scout_revision": head,
        "worktree_dirty": dirty,
        "go_version": version,
        "fixture": "examples/durable_task_recovery",
        "scope": "Scout task-process loss after committed mutation, followed by browser replacement, same-ID replay rejection, and host reconciliation from authoritative fixture state",
        "not_measured": ["peer native task-ID guards", "application idempotency", "comparative latency", "browser restoration"],
        "repetitions": args.repetitions,
        "successes": successes,
        "success_rate": successes / args.repetitions,
        "wilson_95_interval": wilson(successes, args.repetitions),
        "runs": runs,
    }
    report_path = output_dir / "summary.json"
    report_path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(f"Summary: {report_path}")
    if successes != args.repetitions:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
