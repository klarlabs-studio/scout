#!/usr/bin/env python3
"""Compare Scout doctor/first read with pinned agent-browser on a local fixture.

This is an operation-level local probe, not an onboarding or user study. It
isolates agent-browser state under a temporary HOME, runs Scout with its normal
workstation runtime environment, and preserves each command transcript.
"""

from __future__ import annotations

import datetime as dt
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import shutil
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "examples/benchmark/results"
VERSION = "0.42.0"


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def run_command(label: str, command: list[str], cwd: Path, env: dict[str, str], timeout: int = 60) -> dict:
    started = time.perf_counter()
    result = subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True, timeout=timeout)
    return {
        "label": label,
        "command": command,
        "exit_code": result.returncode,
        "elapsed_ms": round((time.perf_counter() - started) * 1000, 1),
        "stdout": result.stdout,
        "stderr": result.stderr,
    }


def wait_fixture(url: str, process: subprocess.Popen) -> None:
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"fixture exited early: {process.returncode}")
        try:
            with urllib.request.urlopen(url, timeout=0.5) as response:
                if response.status == 200 and response.read().strip() == b"ok":
                    return
        except Exception:
            time.sleep(0.1)
    raise TimeoutError("fixture readiness timeout")


def main() -> int:
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output = RESULTS / f"first-success-agent-browser-{VERSION}-{stamp}"
    output.mkdir(parents=True, exist_ok=False)
    temp_root = Path(tempfile.mkdtemp(prefix="first-success-compare-", dir="/tmp"))
    home = temp_root / "home"
    for name in ("config", "cache", "state", "data", "tmp"):
        (home / name).mkdir(parents=True, exist_ok=True)

    scout_env = {key: value for key, value in os.environ.items() if not key.startswith("SCOUT_")}
    scout_env["SCOUT_ALLOW_PRIVATE_IPS"] = "true"
    npm_cache = Path.home() / ".npm"
    agent_env = dict(scout_env)
    agent_env.update({"HOME": str(home), "XDG_CONFIG_HOME": str(home / "config"),
                     "XDG_CACHE_HOME": str(home / "cache"), "XDG_STATE_HOME": str(home / "state"),
                     "XDG_DATA_HOME": str(home / "data"), "TMPDIR": str(home / "tmp"),
                     "ANONYMIZED_TELEMETRY": "false", "npm_config_cache": str(npm_cache)})
    # Give each agent-browser first-read control its own browser profile. Its
    # negative read must not inherit the process opened for the positive read.
    agent_positive_home = temp_root / "agent-positive-home"
    agent_negative_home = temp_root / "agent-negative-home"
    for isolated_home in (agent_positive_home, agent_negative_home):
        for name in ("config", "cache", "state", "data", "tmp"):
            (isolated_home / name).mkdir(parents=True, exist_ok=True)
    agent_positive_env = dict(agent_env)
    agent_negative_env = dict(agent_env)
    for isolated_env, isolated_home in ((agent_positive_env, agent_positive_home),
                                         (agent_negative_env, agent_negative_home)):
        isolated_env.update({"HOME": str(isolated_home), "XDG_CONFIG_HOME": str(isolated_home / "config"),
                             "XDG_CACHE_HOME": str(isolated_home / "cache"), "XDG_STATE_HOME": str(isolated_home / "state"),
                             "XDG_DATA_HOME": str(isolated_home / "data"), "TMPDIR": str(isolated_home / "tmp")})
    agent_positive_profile = agent_positive_home / "browser-profile"
    agent_negative_profile = agent_negative_home / "browser-profile"
    for profile in (agent_positive_profile, agent_negative_profile):
        profile.mkdir(parents=True, exist_ok=False)

    def agent_command(profile: Path, *args: str) -> list[str]:
        return [str(agent_cli), *args, f"--user-data-dir={profile}"]
    build_env = dict(scout_env)
    build_env["GOCACHE"] = str(temp_root / "go-build-cache")
    # Pin the module-required toolchain; `local` resolves the wrapper's older
    # base toolchain and can fail even when the required toolchain is installed.
    build_env["GOTOOLCHAIN"] = "go1.27.4"
    scout = temp_root / "scout"
    fixture_bin = temp_root / "fixtureserver"
    subprocess.run(["go", "build", "-o", str(scout), "./cmd/scout"], cwd=ROOT, env=build_env, check=True, timeout=180)
    subprocess.run(["go", "build", "-o", str(fixture_bin), "./examples/benchmark/fixtureserver"], cwd=ROOT, env=build_env, check=True, timeout=180)
    agent_path_result = subprocess.run(
        ["npx", "--yes", f"--package=agent-browser@{VERSION}", "--", "which", "agent-browser"],
        cwd=ROOT, env=agent_env, text=True, capture_output=True, timeout=90,
    )
    # npm may print lifecycle notices; keep the first absolute path it returns.
    agent_cli = next((Path(line.strip()) for line in agent_path_result.stdout.splitlines()
                      if line.strip().startswith("/")), None)
    if agent_path_result.returncode or agent_cli is None:
        raise RuntimeError(f"could not resolve pinned agent-browser executable: {agent_path_result.stdout}\n{agent_path_result.stderr}")

    version = run_command("agent_browser_version", [str(agent_cli), "--version"], ROOT, agent_env)
    if version["exit_code"] or f"agent-browser {VERSION}" not in version["stdout"]:
        raise RuntimeError(f"unexpected agent-browser executable: {version}")

    port = free_port()
    base = f"http://127.0.0.1:{port}"
    page_url = base + "/healthz"
    fixture_log_path = output / "fixture.log"
    fixture_log = fixture_log_path.open("w", encoding="utf-8")
    fixture = subprocess.Popen([str(fixture_bin), "-listen", f"127.0.0.1:{port}"],
                               cwd=ROOT, stdout=fixture_log, stderr=subprocess.STDOUT, text=True)
    records = []
    try:
        wait_fixture(page_url, fixture)
        doctor = run_command("agent_browser_doctor", [str(agent_cli), "doctor", "--json"], ROOT, agent_env, 90)
        try:
            doctor["json"] = json.loads(doctor["stdout"])
        except json.JSONDecodeError:
            doctor["json_parse_error"] = True
        records.append(doctor)

        task_store = temp_root / "tasks.db"
        scout_doctor = run_command("scout_doctor", [str(scout), "doctor", "--json",
            f"--task-store={task_store}", "--live", "--live-timeout=15s",
            f"--probe-url={page_url}", "--probe-text=ok"], ROOT, scout_env, 60)
        try:
            scout_doctor["json"] = json.loads(scout_doctor["stdout"])
        except json.JSONDecodeError:
            scout_doctor["json_parse_error"] = True
        records.append(scout_doctor)

        browser_open = run_command("agent_browser_first_read_open", agent_command(agent_positive_profile, "open", page_url), ROOT, agent_positive_env, 60)
        browser_text = run_command("agent_browser_first_read_text", agent_command(agent_positive_profile, "get", "text", "body"), ROOT, agent_positive_env, 30)
        records.extend([browser_open, browser_text])
        records.append(run_command("agent_browser_positive_close", agent_command(agent_positive_profile, "close", "--all"), ROOT, agent_positive_env, 30))
        scout_read = run_command("scout_first_read", [str(scout), "navigate", page_url,
            "--expected-text=ok", "--headless", "--timeout=15s", "--allow-private-ips"], ROOT, scout_env, 60)
        try:
            scout_read["json"] = json.loads(scout_read["stdout"])
        except json.JSONDecodeError:
            scout_read["json_parse_error"] = True
        records.append(scout_read)

        # Also pair a truthful negative postcondition with each tool's own
        # page read: the browser shows `ok`, while the requested marker is absent.
        browser_negative_open = run_command("agent_browser_negative_postcondition_open", agent_command(agent_negative_profile, "open", page_url), ROOT, agent_negative_env, 60)
        records.append(browser_negative_open)
        browser_negative = run_command("agent_browser_negative_postcondition_read", agent_command(agent_negative_profile, "get", "text", "body"), ROOT, agent_negative_env, 30)
        browser_negative["expected_text"] = "missing-marker"
        browser_negative["matches_expected"] = "missing-marker" in browser_negative["stdout"]
        records.append(browser_negative)
        scout_negative = run_command("scout_negative_postcondition_read", [str(scout), "navigate", page_url,
            "--expected-text=missing-marker", "--headless", "--timeout=15s", "--allow-private-ips"], ROOT, scout_env, 60)
        try:
            scout_negative["json"] = json.loads(scout_negative["stdout"])
        except json.JSONDecodeError:
            scout_negative["json_parse_error"] = True
        records.append(scout_negative)
    finally:
        for isolated_env, profile in ((agent_positive_env, agent_positive_profile),
                                      (agent_negative_env, agent_negative_profile)):
            try:
                subprocess.run(agent_command(profile, "close", "--all"), cwd=ROOT, env=isolated_env,
                               text=True, capture_output=True, timeout=30)
            except Exception:
                pass
        fixture.terminate()
        try:
            fixture.wait(timeout=5)
        except subprocess.TimeoutExpired:
            fixture.kill()
            fixture.wait(timeout=5)
        fixture_log.close()

    by_label = {row["label"]: row for row in records}
    agent_doctor_json = by_label["agent_browser_doctor"].get("json", {})
    scout_doctor_json = by_label["scout_doctor"].get("json", {})
    scout_first_json = by_label["scout_first_read"].get("json", {})
    scout_negative_json = by_label["scout_negative_postcondition_read"].get("json", {})
    predicates = {
        "agent_browser_doctor_passed": agent_doctor_json.get("success") is True,
        "scout_doctor_app_probe_passed": next((check.get("status") == "ok" for check in scout_doctor_json.get("checks", []) if check.get("name") == "app_probe"), False),
        "scout_doctor_cleanup_passed": next((check.get("status") == "ok" for check in scout_doctor_json.get("checks", []) if check.get("name") == "browser_cleanup"), False),
        "scout_doctor_process_succeeded": by_label["scout_doctor"]["exit_code"] == 0,
        "agent_browser_first_read_open_succeeded": by_label["agent_browser_first_read_open"]["exit_code"] == 0,
        "agent_browser_first_read_observed_ok": "ok" in by_label["agent_browser_first_read_text"]["stdout"].strip(),
        "agent_browser_first_read_process_succeeded": by_label["agent_browser_first_read_text"]["exit_code"] == 0,
        "scout_first_read_verified_ok": scout_first_json.get("verified") is True and scout_first_json.get("outcome") == "postcondition_verified",
        "scout_first_read_process_succeeded": by_label["scout_first_read"]["exit_code"] == 0,
        "scout_first_read_phase_timing_present": all(
            isinstance(scout_first_json.get("execution_timing", {}).get(field), int)
            and scout_first_json["execution_timing"][field] >= 0
            for field in ("browser_startup_ms", "page_navigation_verification_ms", "browser_cleanup_ms")
        ),
        "agent_browser_missing_marker_detected_by_harness": by_label["agent_browser_negative_postcondition_read"].get("matches_expected") is False,
        "agent_browser_negative_open_succeeded": by_label["agent_browser_negative_postcondition_open"]["exit_code"] == 0,
        "agent_browser_negative_read_process_succeeded": by_label["agent_browser_negative_postcondition_read"]["exit_code"] == 0,
        "scout_missing_marker_rejected": scout_negative_json.get("outcome") == "postcondition_failed",
        "scout_missing_marker_process_failed": by_label["scout_negative_postcondition_read"]["exit_code"] != 0,
        "scout_negative_read_phase_timing_present": all(
            isinstance(scout_negative_json.get("execution_timing", {}).get(field), int)
            and scout_negative_json["execution_timing"][field] >= 0
            for field in ("browser_startup_ms", "page_navigation_verification_ms", "browser_cleanup_ms")
        ),
    }
    report = {
        "schema_version": 1,
        "benchmark": "scout_vs_agent_browser_doctor_and_first_read_local_probe",
        "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "agent_browser_version": VERSION,
        "scout_version": json.loads(next(row["stdout"] for row in records if row["label"] == "scout_doctor")).get("version"),
        "environment": {"platform": platform.platform(), "go": subprocess.run(["go", "version"], text=True, capture_output=True, check=True).stdout.strip()},
        "fixture": {"route": "/healthz", "body": "ok", "scope": "loopback only"},
        "source": {
            "git_head": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True, capture_output=True, check=True).stdout.strip(),
            "worktree_dirty": bool(subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, text=True, capture_output=True, check=True).stdout.strip()),
        },
        "browser_profile_isolation": "agent-browser positive and negative controls each used a distinct explicit --user-data-dir under a separate temporary HOME/XDG tree and closed their browsers before the next control; the pinned npm package cache is shared from the workstation. Scout one-shot commands each launched and closed their own browser process and used the workstation HOME, XDG, and TMPDIR with SCOUT_* variables cleared except the explicit loopback opt-in",
        "records": records,
        "predicates": predicates,
        "interpretation": "One workstation probe pairs each tool's doctor with a real browser read and a missing-marker postcondition. Agent-browser positive and negative first-read controls use separate temporary profiles and browsers; Scout one-shot commands use separate browser processes. This reflects standalone first-read use, but startup overhead differs because agent-browser exposes persistent-session commands while Scout's measured one-shot command includes browser startup and cleanup. Scout's navigate result records browser startup, page navigation/verification, and browser cleanup durations separately; process wall time remains recorded for each command. It does not test clean-container onboarding, multiple seeded infrastructure failures, diagnosis explanations, participant effort, or reliability rates. Agent-browser's get-text command returns observed text; the harness grades its requested marker separately because the CLI command has no equivalent built-in postcondition flag.",
        "artifacts": ["fixture.log"],
        "temporary_home": "separate isolated temporary HOME/XDG trees for agent-browser positive and negative controls; removed after tool shutdown",
    }
    report_path = output / "summary.json"
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    shutil.rmtree(temp_root, ignore_errors=True)
    print(json.dumps({"report": str(report_path), "records": [
        {"label": row["label"], "exit_code": row["exit_code"], "elapsed_ms": row["elapsed_ms"]}
        for row in records]}, indent=2))
    return 0 if all(predicates.values()) else 1


if __name__ == "__main__":
    raise SystemExit(main())
