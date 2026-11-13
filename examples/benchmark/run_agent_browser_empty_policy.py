#!/usr/bin/env python3
"""Probe empty AGENT_BROWSER_ALLOWED_DOMAINS against an independent local oracle."""

from __future__ import annotations

import json
import os
import pathlib
import platform
import subprocess
import tempfile
import time
import urllib.request
import argparse
import platform
from datetime import datetime, timezone


ROOT = pathlib.Path(__file__).resolve().parents[2]
ALLOWED = "http://localhost:41745"
DENIED = "http://127.0.0.1:41746"
PAGE = f"{ALLOWED}/page?denied={DENIED}"


def get_json(url: str) -> dict:
    with urllib.request.urlopen(url, timeout=2) as response:
        return json.load(response)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path, help="write report here (defaults to stdout)")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="scout-agent-browser-policy-") as directory:
        fixture = pathlib.Path(directory) / "origin-policy-fixture"
        subprocess.run(
            ["go", "build", "-o", str(fixture), "./examples/benchmark/origin_policy_fixture"],
            cwd=ROOT,
            check=True,
        )
        server = subprocess.Popen(
            [str(fixture), "-allowed-listen", "127.0.0.1:41745", "-denied-listen", "127.0.0.1:41746"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
        )
        try:
            for _ in range(100):
                try:
                    get_json(f"{ALLOWED}/count")
                    break
                except Exception:
                    if server.poll() is not None:
                        raise RuntimeError(f"fixture exited: {server.stderr.read()}")
                    time.sleep(0.1)
            else:
                raise TimeoutError("fixture did not start")

            rows = []
            for label, configured in (("unset", None), ("blank", ""), ("comma_only", ","), ("valid", "localhost")):
                with urllib.request.urlopen(f"{ALLOWED}/reset", timeout=2):
                    pass
                env = os.environ.copy()
                env["AGENT_BROWSER_NO_VERSION_CHECK"] = "1"
                if configured is None:
                    env.pop("AGENT_BROWSER_ALLOWED_DOMAINS", None)
                else:
                    env["AGENT_BROWSER_ALLOWED_DOMAINS"] = configured
                session = f"scout-empty-policy-{label}-{os.getpid()}"
                run = subprocess.run(
                    ["npx", "--yes", "--package=agent-browser@0.42.0", "agent-browser",
                     "--no-auto-connect", "--session", session, "open", PAGE],
                    cwd=ROOT,
                    env=env,
                    capture_output=True,
                    text=True,
                    timeout=90,
                )
                time.sleep(0.8)
                hits = get_json(f"{ALLOWED}/count")["denied_origin_hits"]
                close = subprocess.run(
                    ["npx", "--yes", "--package=agent-browser@0.42.0", "agent-browser",
                     "--no-auto-connect", "--session", session, "close"],
                    cwd=ROOT,
                    env=env,
                    capture_output=True,
                    text=True,
                    timeout=30,
                )
                ready = run.returncode == 0 and "Origin policy fixture" in run.stdout
                rows.append({
                    "case": label,
                    "configured_value": configured,
                    "exit_code": run.returncode,
                    "fixture_ready": ready,
                    "denied_origin_hits": hits,
                    "containment_bypassed": configured is not None and hits > 0,
                    "stdout": run.stdout,
                    "stderr": run.stderr,
                    "close_exit_code": close.returncode,
                })

            # Compare configuration handling separately from request enforcement:
            # Scout's CLI requires an explicit policy value to be syntactically
            # valid and rejects empty/malformed values before starting Chrome.
            scout = pathlib.Path(directory) / "scout"
            subprocess.run(["go", "build", "-o", str(scout), "./cmd/scout"], cwd=ROOT, check=True)
            scout_version = subprocess.run([str(scout), "version"], capture_output=True, text=True, check=False).stdout.strip()
            scout_rows = []
            for label, value in (("unset", None), ("empty", ""), ("trailing_empty", f"{ALLOWED},"), ("valid", ALLOWED)):
                with urllib.request.urlopen(f"{ALLOWED}/reset", timeout=2):
                    pass
                command = [str(scout), "session", "--headless", "--allow-private-ips"]
                if value is not None:
                    command.append(f"--allowed-origins={value}")
                run = subprocess.run(command, cwd=ROOT, input=json.dumps({"id": 1, "command": "navigate", "url": PAGE}) + "\n", capture_output=True, text=True, timeout=30)
                time.sleep(0.8)
                hits = get_json(f"{ALLOWED}/count")["denied_origin_hits"]
                try:
                    response = json.loads(run.stdout.strip().splitlines()[0])
                except (IndexError, json.JSONDecodeError):
                    response = {}
                scout_rows.append({
                    "case": label,
                    "configured_value": value,
                    "exit_code": run.returncode,
                    "fixture_ready": run.returncode == 0 and response.get("ok") is True and "Origin policy fixture" in json.dumps(response.get("result", {})),
                    "denied_origin_hits": hits,
                    "rejected_before_browser": run.returncode != 0 and "invalid --allowed-origins" in (run.stderr + run.stdout),
                    "stdout": run.stdout,
                    "stderr": run.stderr,
                })

        finally:
            server.terminate()
            try:
                server.wait(timeout=3)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait(timeout=3)

    report = {
        "schema_version": 1,
        "scenario": "agent-browser-empty-allowed-domains",
        "captured_at_utc": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "platform": f"{platform.system().lower()}/{platform.machine()}",
        "source": {
            "git_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip(),
            "worktree_dirty": bool(subprocess.run(["git", "status", "--short"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()),
        },
        "competitor": {"name": "agent-browser", "version": "0.42.0"},
        "scout": {"version": scout_version, "cases": scout_rows},
        "fixture": {"allowed_page": PAGE, "denied_origin": DENIED,
                    "independent_oracle": "atomic hit count on the denied-origin HTTP listener"},
        "cases": rows,
        "predicates": {
            "unset_is_unrestricted": rows[0]["fixture_ready"] and rows[0]["denied_origin_hits"] == 1,
            "valid_allowlist_blocks_denied_origin": rows[3]["fixture_ready"] and rows[3]["denied_origin_hits"] == 0,
            "empty_configured_values_bypass_containment": all(
                row["fixture_ready"] and row["denied_origin_hits"] == 1 and row["containment_bypassed"]
                for row in rows[1:3]
            ),
            "scout_rejects_empty_and_malformed_before_browser": all(
                scout_rows[index]["exit_code"] != 0 and scout_rows[index]["rejected_before_browser"] and scout_rows[index]["denied_origin_hits"] == 0
                for index in (1, 2)
            ),
            "scout_valid_origin_blocks_denied_origin": scout_rows[3]["fixture_ready"] and scout_rows[3]["denied_origin_hits"] == 0,
        },
        "limitations": [
            "One local run against pinned agent-browser 0.42.0; not a release-wide reliability claim.",
            "Scout CLI syntax rejection is a startup contract and is reported separately from browser request enforcement.",
            "Tests a page-initiated ordinary fetch only, not redirects, other resource types, or remote egress.",
            "The fixture verifies source-side effects independently; it does not establish exploit prevalence.",
        ],
    }
    encoded = json.dumps(report, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded, encoding="utf-8")
    else:
        print(encoded, end="")
    if not all(report["predicates"].values()):
        raise SystemExit(1)


if __name__ == "__main__":
    main()
