#!/usr/bin/env python3
"""Compare pinned agent-browser and Scout diagnostics for an unreachable CDP endpoint.

This is a one-workstation regression probe. It records the commands, outputs,
timings and isolated state before and after; it is not a release quality or
reliability estimate.
"""
from __future__ import annotations

import datetime as dt
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "examples/benchmark/results"
AGENT_VERSION = "0.42.0"


def reserve_closed_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def run(label: str, command: list[str], env: dict[str, str], timeout: int = 45) -> dict:
    started = time.perf_counter()
    try:
        result = subprocess.run(command, cwd=ROOT, env=env, text=True,
                                capture_output=True, timeout=timeout)
        return {"label": label, "command": command, "exit_code": result.returncode,
                "elapsed_ms": round((time.perf_counter() - started) * 1000, 1),
                "stdout": result.stdout, "stderr": result.stderr, "timed_out": False}
    except subprocess.TimeoutExpired as exc:
        return {"label": label, "command": command, "exit_code": None,
                "elapsed_ms": round((time.perf_counter() - started) * 1000, 1),
                "stdout": exc.stdout or "", "stderr": exc.stderr or "", "timed_out": True}


def state_snapshot(root: Path) -> dict:
    files = sorted(str(p.relative_to(root)) for p in (root / ".agent-browser").rglob("*")
                   if p.is_file())
    return {"persistent_files": files,
            "persistent_file_count": len(files),
            "persistent_bytes": sum((root / name).stat().st_size for name in files),
            "runtime_compile_cache_files": sum(1 for p in (root / "tmp").rglob("*") if p.is_file())}


def parse_json(record: dict) -> dict:
    try:
        return json.loads(record["stdout"])
    except (KeyError, json.JSONDecodeError):
        return {}


def main() -> int:
    RESULTS.mkdir(parents=True, exist_ok=True)
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    out = RESULTS / f"unreachable-cdp-{AGENT_VERSION}-{stamp}"
    out.mkdir()
    root = Path(tempfile.mkdtemp(prefix="scout-unreachable-cdp-", dir="/tmp"))
    home = root / "home"
    for name in ("config", "cache", "state", "data", "tmp"):
        (home / name).mkdir(parents=True, exist_ok=True)

    clean_env = {k: v for k, v in os.environ.items() if not k.startswith("SCOUT_")}
    agent_env = dict(clean_env, HOME=str(home), XDG_CONFIG_HOME=str(home / "config"),
                     XDG_CACHE_HOME=str(home / "cache"), XDG_STATE_HOME=str(home / "state"),
                     XDG_DATA_HOME=str(home / "data"), TMPDIR=str(home / "tmp"),
                     ANONYMIZED_TELEMETRY="false")
    agent_env["npm_config_cache"] = str(Path.home() / ".npm")
    scout_env = dict(clean_env, SCOUT_ALLOW_PRIVATE_IPS="true")
    build_env = dict(clean_env, GOTOOLCHAIN="go1.27.4", GOCACHE=str(root / "go-cache"))
    scout_bin = root / "scout"
    subprocess.run(["go", "build", "-o", str(scout_bin), "./cmd/scout"],
                   cwd=ROOT, env=build_env, check=True, timeout=180)

    resolved = subprocess.run(["npx", "--yes", f"--package=agent-browser@{AGENT_VERSION}",
                               "--", "which", "agent-browser"], cwd=ROOT, env=agent_env,
                              text=True, capture_output=True, check=True, timeout=90)
    cli = next((Path(line.strip()) for line in resolved.stdout.splitlines()
                if line.strip().startswith("/")), None)
    if cli is None:
        raise RuntimeError("pinned agent-browser executable path was not returned")
    version = run("agent_browser_version", [str(cli), "--version"], agent_env)
    if version["exit_code"] != 0 or f"agent-browser {AGENT_VERSION}" not in version["stdout"]:
        raise RuntimeError(f"pinned agent-browser version check failed: {version}")

    port = reserve_closed_port()
    endpoint = f"http://127.0.0.1:{port}"
    records = [version]
    before = state_snapshot(home)
    # The upstream merged-source PR validates this condition with a closed CDP
    # port. agent-browser doctor has no endpoint override, so preserve its own
    # ordinary doctor result and mark that condition as not injectable there.
    records.append(run("agent_browser_doctor", [str(cli), "doctor", "--json"], agent_env, 90))
    scout_failure_env = dict(scout_env, SCOUT_REMOTE_CDP=endpoint)
    task_store = root / "scout-tasks.db"
    records.append(run("scout_doctor_unreachable_cdp",
                       [str(scout_bin), "doctor", "--json", "--task-store=" + str(task_store),
                        "--live", "--live-timeout=8s"], scout_failure_env, 25))
    records.append(run("scout_first_read_unreachable_cdp",
                       [str(scout_bin), "navigate", "http://example.invalid/", "--headless",
                        "--timeout=8s", "--allow-private-ips"], scout_failure_env, 20))
    after = state_snapshot(home)

    doctor = parse_json(next(r for r in records if r["label"] == "scout_doctor_unreachable_cdp"))
    checks = {item.get("name"): item for item in doctor.get("checks", [])}
    agent_doctor = next(r for r in records if r["label"] == "agent_browser_doctor")
    scout_read = next(r for r in records if r["label"] == "scout_first_read_unreachable_cdp")
    predicates = {
        "agent_browser_version_pinned": version["exit_code"] == 0,
        "agent_browser_doctor_returned": agent_doctor.get("exit_code") == 0,
        "agent_browser_doctor_launch_test_passed": any(
            check.get("id") == "launch.elapsed" and check.get("status") == "pass"
            for check in parse_json(agent_doctor).get("checks", [])),
        "agent_browser_doctor_created_scoped_diagnostic_target": any(
            Path(name).name.startswith("doctor-") and name.endswith(".target")
            for name in after["persistent_files"]),
        "scout_doctor_reports_live_failure": checks.get("browser_live", {}).get("status") == "error",
        "scout_doctor_has_timing_for_live_failure": isinstance(checks.get("browser_live", {}).get("duration_ms"), int),
        "scout_does_not_create_task_store_on_failed_attach": not task_store.exists(),
        "scout_first_read_reports_connection_refused": scout_read["exit_code"] != 0 and "connection refused" in scout_read.get("stderr", "").lower(),
    }
    report = {
        "schema_version": 1,
        "benchmark": "unreachable_cdp_diagnostic_comparison",
        "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "agent_browser_version": AGENT_VERSION,
        "environment": {"platform": platform.platform(),
                        "go": subprocess.run(["go", "version"], text=True, capture_output=True,
                                              check=True).stdout.strip()},
        "scenario": {"kind": "unreachable loopback CDP endpoint", "endpoint": "redacted loopback endpoint",
                     "endpoint_injection": {"scout": "SCOUT_REMOTE_CDP", "agent_browser": "not available in doctor invocation"}},
        "evidence_boundary": "The Stagehand mainline PR test uses a closed CDP endpoint, but this local comparison does not run Stagehand browse doctor: only the source change is merged and no browse CLI is installed here. Agent-browser doctor is run without endpoint injection and is not scored on the seeded failure. Scout's live failure classification and unchanged isolated agent-browser state are directly observed.",
        "source": {"git_head": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT,
                                                text=True, capture_output=True, check=True).stdout.strip(),
                   "worktree_dirty": bool(subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                                                         text=True, capture_output=True, check=True).stdout.strip())},
        "records": records,
        "scout_diagnostic": checks.get("browser_live"),
        "predicates": predicates,
        "agent_browser_home_effect": {"before": before, "after": after,
                                       "delta_files": sorted(set(after["persistent_files"]) - set(before["persistent_files"]))},
        "artifacts": [],
    }
    (out / "summary.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    shutil.rmtree(root, ignore_errors=True)
    print(json.dumps({"report": str(out / "summary.json"), "predicates": predicates}, indent=2))
    return 0 if all(predicates.values()) else 1


if __name__ == "__main__":
    raise SystemExit(main())
