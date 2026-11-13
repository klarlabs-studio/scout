#!/usr/bin/env python3
"""Capture Scout doctor readiness states against the local benchmark fixture."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BUILD_INPUT_SUFFIXES = {".go"}
BUILD_INPUT_NAMES = {"go.mod", "go.sum", "go.work", "go.work.sum"}
BUILD_EMBEDDED_INPUTS = {"examples/benchmark/fixtures/v28.json"}


def source_fingerprint() -> dict[str, str | int]:
    tracked = subprocess.run(
        ["git", "ls-files", "-z"], cwd=ROOT, capture_output=True, check=True
    ).stdout.split(b"\0")
    untracked = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard", "-z"],
        cwd=ROOT, capture_output=True, check=True,
    ).stdout.split(b"\0")
    paths = sorted({os.fsdecode(item) for item in tracked + untracked if item})
    manifest = hashlib.sha256()
    included = 0
    for relative in paths:
        path = Path(relative)
        is_build_input = (
            path.suffix in BUILD_INPUT_SUFFIXES
            or path.name in BUILD_INPUT_NAMES
            or relative in BUILD_EMBEDDED_INPUTS
        )
        if not is_build_input or not (ROOT / path).is_file():
            continue
        content_digest = hashlib.sha256((ROOT / path).read_bytes()).digest()
        manifest.update(relative.encode("utf-8", errors="surrogateescape"))
        manifest.update(b"\0")
        manifest.update(content_digest)
        included += 1
    tracked_diff_hash = hashlib.sha256()
    diff = subprocess.Popen(
        ["git", "diff", "HEAD", "--binary"], cwd=ROOT,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    assert diff.stdout is not None
    for chunk in iter(lambda: diff.stdout.read(1024 * 1024), b""):
        tracked_diff_hash.update(chunk)
    diff_error = diff.stderr.read() if diff.stderr is not None else b""
    if diff.wait() != 0:
        raise RuntimeError(f"git diff failed: {os.fsdecode(diff_error)}")
    go_env = subprocess.run(
        [
            "go", "env", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED",
            "GOTOOLCHAIN", "GOWORK", "GOFLAGS",
        ],
        cwd=ROOT, text=True, capture_output=True, check=True,
    ).stdout.splitlines()
    effective_gowork = go_env[5] if len(go_env) > 5 else ""
    effective_goflags = go_env[6] if len(go_env) > 6 else ""
    gowork_digest = hashlib.sha256(effective_gowork.encode()).hexdigest()
    gowork_path = Path(effective_gowork)
    if not gowork_path.is_absolute():
        gowork_path = ROOT / gowork_path
    if effective_gowork and effective_gowork != "off" and gowork_path.is_file():
        gowork_digest = hashlib.sha256(gowork_path.read_bytes()).hexdigest()
    return {
        "build_input_files": included,
        "build_input_sha256": manifest.hexdigest(),
        "tracked_diff_sha256": tracked_diff_hash.hexdigest(),
        "go_version": go_env[0] if len(go_env) > 0 else "unknown",
        "goos": go_env[1] if len(go_env) > 1 else "unknown",
        "goarch": go_env[2] if len(go_env) > 2 else "unknown",
        "cgo_enabled": go_env[3] if len(go_env) > 3 else "unknown",
        "gotoolchain": go_env[4] if len(go_env) > 4 else "unknown",
        "goflags_configured": bool(effective_goflags),
        "goflags_sha256": hashlib.sha256(effective_goflags.encode()).hexdigest(),
        "gowork_configured": bool(effective_gowork and effective_gowork != "off"),
        "gowork_sha256": gowork_digest,
    }


def host_environment_fingerprint() -> dict[str, bool | str]:
    path = os.environ.get("PATH", "")
    return {
        "path_sha256": hashlib.sha256(path.encode()).hexdigest(),
        "display_available": bool(os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY")),
        "http_proxy_configured": bool(os.environ.get("HTTP_PROXY") or os.environ.get("http_proxy")),
        "https_proxy_configured": bool(os.environ.get("HTTPS_PROXY") or os.environ.get("https_proxy")),
        "no_proxy_configured": bool(os.environ.get("NO_PROXY") or os.environ.get("no_proxy")),
        "ci_environment": bool(os.environ.get("CI")),
    }


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def clean_environment() -> dict[str, str]:
    env = {key: value for key, value in os.environ.items() if not key.startswith("SCOUT_")}
    return env


def request_json(binary: Path, label: str, args: list[str], env: dict[str, str]) -> dict:
    started = time.perf_counter()
    command = [str(binary), "doctor", "--json", *args]
    result = subprocess.run(command, cwd=ROOT, env=env, text=True, capture_output=True, timeout=40)
    elapsed_ms = round((time.perf_counter() - started) * 1000, 1)
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"{label}: doctor did not return JSON (exit {result.returncode}): {result.stdout}\n{result.stderr}") from exc
    checks = {check["name"]: check["status"] for check in payload.get("checks", [])}
    return {
        "case": label,
        "command": ["scout", "doctor", "--json", *["--task-store=<temporary-task-store>" if arg.startswith("--task-store=") else arg for arg in args]],
        "exit_code": result.returncode,
        "elapsed_ms": elapsed_ms,
        "status": payload.get("status"),
        "checks": checks,
        "stdout": payload,
        "stderr": result.stderr,
    }


def first_read(binary: Path, label: str, url: str, expected_text: str, env: dict[str, str]) -> dict:
    command = [
        str(binary), "navigate", url,
        f"--expected-text={expected_text}", "--headless", "--timeout=15s", "--allow-private-ips",
    ]
    started = time.perf_counter()
    result = subprocess.run(command, cwd=ROOT, env=env, text=True, capture_output=True, timeout=25)
    elapsed_ms = round((time.perf_counter() - started) * 1000, 1)
    try:
        payload = json.loads(result.stdout)
        output_kind = "json"
    except json.JSONDecodeError:
        payload = None
        output_kind = "non_json"

    verified = bool(payload and payload.get("verified"))
    outcome = payload.get("outcome") if payload else None
    execution_timing = payload.get("execution_timing") if payload else None
    timing_fields = (
        "browser_startup_ms",
        "page_navigation_verification_ms",
        "browser_cleanup_ms",
    )
    phase_timing_complete = (
        isinstance(execution_timing, dict)
        and all(isinstance(execution_timing.get(field), int) and execution_timing[field] >= 0 for field in timing_fields)
    )
    if verified and outcome == "postcondition_verified":
        classification = "verified"
    elif outcome == "postcondition_failed":
        classification = "postcondition_failed"
    elif result.returncode != 0:
        classification = "operation_error"
    else:
        classification = "unverified_or_invalid_output"

    return {
        "case": label,
        "command": ["scout", "navigate", "<loopback-fixture-url>", f"--expected-text={expected_text}", "--headless", "--timeout=15s", "--allow-private-ips"],
        "exit_code": result.returncode,
        "elapsed_ms": elapsed_ms,
        "classification": classification,
        "matches_expected": (classification == "verified" and result.returncode == 0) if label == "first_read_verified" else (classification == "postcondition_failed"),
        "output_kind": output_kind,
        "execution_timing": execution_timing,
        "phase_timing_complete": phase_timing_complete,
        "stdout": payload if payload is not None else result.stdout,
        "stderr": result.stderr,
    }


def doctor_fault_tests() -> list[dict]:
    names = {
        "TestDoctorReportsMissingBrowserAndSkipsLiveCheck": {
            "local_browser": ("error", "SCOUT_DOCTOR_LOCAL_BROWSER_ERROR", "install_chrome_or_chromium_or_set_remote_cdp"),
            "browser_live": ("not_checked", "SCOUT_DOCTOR_BROWSER_LIVE_NOT_CHECKED", "repair_configuration_before_live_check"),
        },
        "TestDoctorLiveAttachFailureHasStableCodeAndNoSecrets": {
            "browser_live": ("error", "SCOUT_DOCTOR_BROWSER_LIVE_ERROR", "check_browser_endpoint_and_connection"),
            "browser_cleanup": ("not_checked", "SCOUT_DOCTOR_BROWSER_CLEANUP_NOT_CHECKED", "none"),
        },
        "TestDoctorLiveReadFailureIsNotGreenAndClosesSession": {
            "browser_live": ("error", "SCOUT_DOCTOR_BROWSER_LIVE_PAGE_READ_FAILED", "check_browser_endpoint_and_connection"),
            "browser_cleanup": ("ok", "SCOUT_DOCTOR_BROWSER_CLEANUP_OK", "none"),
        },
        "TestDoctorTimeoutDuringFirstReadStillClosesSession": {
            "browser_first_read": ("error", "SCOUT_DOCTOR_BROWSER_FIRST_READ_ERROR", "inspect_unresponsive_target"),
            "browser_cleanup": ("ok", "SCOUT_DOCTOR_BROWSER_CLEANUP_OK", "none"),
        },
        "TestDoctorProbeCleanupFailureHasStableRecoveryGuidance": {
            "app_probe": ("error", "SCOUT_DOCTOR_APP_PROBE_CLEANUP_FAILED", "inspect_browser_tabs_and_restore_original_session"),
        },
    }
    result = subprocess.run(
        ["go", "test", "./cmd/scout", "-run", "^(" + "|".join(names) + ")$", "-count=1", "-json"],
        cwd=ROOT,
        env=clean_environment(),
        text=True,
        capture_output=True,
        timeout=180,
    )
    events = []
    for line in result.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    tests = []
    for name, expected_checks in names.items():
        passed = any(event.get("Action") == "pass" and event.get("Test") == name for event in events)
        failed = any(event.get("Action") == "fail" and event.get("Test") == name for event in events)
        test = {
            "case": name,
            "status": "failed" if failed or result.returncode != 0 and not passed else "passed" if passed else "missing",
            "matches_expected": passed and not failed and result.returncode == 0,
            "expected_checks": {
                check_name: {"status": values[0], "code": values[1], "remediation": values[2]}
                for check_name, values in expected_checks.items()
            },
        }
        test["expected_check_assertions"] = len(expected_checks)
        tests.append(test)
    if result.returncode != 0 and not any(test["status"] == "failed" for test in tests):
        raise RuntimeError(f"doctor fault tests failed outside the expected test cases: {result.stdout}\n{result.stderr}")
    return tests


def wait_fixture(url: str, process: subprocess.Popen[str]) -> None:
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"fixture server exited early with status {process.returncode}")
        try:
            with urllib.request.urlopen(url, timeout=0.5) as response:
                if response.status == 200 and response.read().strip() == b"ok":
                    return
        except Exception:
            time.sleep(0.1)
    raise RuntimeError("fixture server did not become ready")


def run(output: Path) -> dict:
    with tempfile.TemporaryDirectory(prefix="scout-doctor-readiness-") as tmp:
        tmpdir = Path(tmp)
        scout = tmpdir / "scout"
        fixture = tmpdir / "fixtureserver"
        subprocess.run(["go", "build", "-o", str(scout), "./cmd/scout"], cwd=ROOT, check=True, timeout=180)
        subprocess.run(["go", "build", "-o", str(fixture), "./examples/benchmark/fixtureserver"], cwd=ROOT, check=True, timeout=180)
        port = free_port()
        fixture_url = f"http://127.0.0.1:{port}/healthz"
        server = subprocess.Popen([str(fixture), "-listen", f"127.0.0.1:{port}"], cwd=ROOT, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        try:
            wait_fixture(fixture_url, server)
            base_env = clean_environment()
            base_args = [f"--task-store={tmpdir / 'tasks.db'}"]
            cases = [
                request_json(scout, "configuration_only", base_args, base_env),
                request_json(scout, "live_browser", base_args + ["--live", "--live-timeout=15s"], base_env),
            ]
            probe_env = dict(base_env)
            probe_env["SCOUT_ALLOW_PRIVATE_IPS"] = "true"
            common = base_args + ["--live", "--live-timeout=15s", f"--probe-url={fixture_url}"]
            cases.append(request_json(scout, "application_probe_pass", common + ["--probe-text=ok"], probe_env))
            cases.append(request_json(scout, "application_probe_missing_marker", common + ["--probe-text=missing-marker"], probe_env))
            first_reads = [
                first_read(scout, "first_read_verified", fixture_url, "ok", base_env),
                first_read(scout, "first_read_missing_marker", fixture_url, "missing-marker", base_env),
            ]
            fault_tests = doctor_fault_tests()
        finally:
            server.terminate()
            try:
                server.wait(timeout=5)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait(timeout=5)

    expectations = {
        "configuration_only": ("warning", 0, "not_checked", None, None),
        "live_browser": ("ok", 0, "ok", None, "ok"),
        "application_probe_pass": ("warning", 0, "ok", "ok", "ok"),
        "application_probe_missing_marker": ("error", 1, "ok", "error", "ok"),
    }
    for case in cases:
        status, exit_code, browser_live, app_probe, browser_cleanup = expectations[case["case"]]
        case["matches_expected"] = (
            case["status"] == status
            and case["exit_code"] == exit_code
            and case["checks"].get("browser_live") == browser_live
            and (app_probe is None or case["checks"].get("app_probe") == app_probe)
            and (browser_cleanup is None or case["checks"].get("browser_cleanup") == browser_cleanup)
        )
    version = cases[0]["stdout"].get("version", "unknown")
    report = {
        "schema_version": 4,
        "captured_at": datetime.now(timezone.utc).isoformat(),
        "tool": "scout doctor",
        "tool_version": version,
        "source": {
            "git_head": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True, capture_output=True, check=True).stdout.strip(),
            "worktree_dirty": bool(subprocess.run(["git", "status", "--porcelain"], cwd=ROOT, text=True, capture_output=True, check=True).stdout.strip()),
            "fingerprint": source_fingerprint(),
        },
        "environment": {
            "os": platform.platform(),
            "python": platform.python_version(),
            "go": subprocess.run(["go", "version"], text=True, capture_output=True, check=True).stdout.strip(),
            "host_context": host_environment_fingerprint(),
        },
        "fixture": {"route": "/healthz", "body": "ok", "scope": "local loopback only"},
        "cases": cases,
        "first_reads": first_reads,
        "fault_tests": fault_tests,
        "timing_scope": "External process wall-clock duration for each CLI invocation. Doctor reports browser launch/attach/status duration, application probe duration including temporary-tab cleanup, and browser session cleanup duration separately. Navigate CLI results additionally report browser startup, page navigation/verification, and browser cleanup durations.",
        "all_cases_match_expected": all(case["matches_expected"] for case in cases) and all(case["matches_expected"] and case["phase_timing_complete"] for case in first_reads) and all(test["matches_expected"] and test["expected_check_assertions"] > 0 for test in fault_tests),
        "evidence_scope": "Single local run on this worktree. It verifies doctor classifications, real bounded first-read outcomes, and CLI exit behavior, not competitor behavior, population reliability, browser-process cleanup, or independent tab-cleanup observation.",
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="report path (default: a unique UTC timestamp under examples/benchmark/reports)")
    args = parser.parse_args()
    output = args.output
    if output is None:
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
        output = ROOT / "examples/benchmark/reports" / f"scout-doctor-readiness-{stamp}.json"
    try:
        report = run(output)
    except (OSError, subprocess.SubprocessError, RuntimeError) as exc:
        print(f"doctor readiness run failed: {exc}", file=sys.stderr)
        return 2
    print(json.dumps({
        "output": str(output),
        "all_cases_match_expected": report["all_cases_match_expected"],
        "cases": [{"case": item["case"], "status": item["status"], "exit_code": item["exit_code"], "elapsed_ms": item["elapsed_ms"], "checks": item["checks"]} for item in report["cases"]],
        "first_reads": [{"case": item["case"], "classification": item["classification"], "exit_code": item["exit_code"], "elapsed_ms": item["elapsed_ms"], "phase_timing_complete": item["phase_timing_complete"], "execution_timing": item["execution_timing"], "matches_expected": item["matches_expected"]} for item in report["first_reads"]],
        "fault_tests": report["fault_tests"],
    }, indent=2))
    return 0 if report["all_cases_match_expected"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
