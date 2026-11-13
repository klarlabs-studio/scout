#!/usr/bin/env python3
"""Run repeated verified-click trials using a pinned agent-browser CLI."""
import argparse
import json
import math
import os
import pathlib
import re
import secrets
import signal
import statistics
import subprocess
import sys
import time
import urllib.request


def run_cli(version, session, args):
    command = ["npx", "--yes", f"agent-browser@{version}", "--session", session, *args]
    return subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def save_output(root, stem, result):
    (root / f"{stem}.stdout").write_bytes(result.stdout)
    (root / f"{stem}.stderr").write_bytes(result.stderr)


def fixture_late_state(base_url):
    with urllib.request.urlopen(base_url + "/late-action-state", timeout=2) as response:
        return json.loads(response.read())


def fixture_process_recovery_state(base_url):
    with urllib.request.urlopen(base_url + "/process-recovery-state", timeout=2) as response:
        return json.loads(response.read())


def run_caller_idempotency_probe(version, base_url, root):
    """Measure application idempotency on repeated browser UI submissions."""
    session = "idem-benchmark"
    namespace = session

    def cli(args, label, check=True):
        command = ["npx", "--yes", f"agent-browser@{version}", "--session", session,
                   "--namespace", namespace, *args]
        proc = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        save_output(root, f"agent-browser.idempotency.{label}", proc)
        if check and proc.returncode:
            raise RuntimeError(f"{label} failed: {proc.stderr.decode(errors='replace')}")
        return proc

    def state():
        with urllib.request.urlopen(base_url + "/idempotent-mutation-state", timeout=3) as response:
            return json.loads(response.read())

    cli(["open", base_url + "/idempotency-task"], "open")
    before = state()
    first = cli(["click", "#submit"], "first-submit")
    first_result = first.stdout.decode(errors="replace").strip()
    deadline = time.monotonic() + 5
    after_first = state()
    while after_first["committed_mutations"] == before["committed_mutations"] and time.monotonic() < deadline:
        time.sleep(0.01)
        after_first = state()
    if after_first["committed_mutations"] == before["committed_mutations"]:
        raise RuntimeError("first UI submission did not reach the authoritative fixture")
    second = cli(["click", "#submit"], "same-task-retry")
    second_result = second.stdout.decode(errors="replace").strip()
    deadline = time.monotonic() + 5
    after_second = state()
    while after_second["requests"] < after_first["requests"] + 1 and time.monotonic() < deadline:
        time.sleep(0.01)
        after_second = state()
    if after_second["requests"] < after_first["requests"] + 1:
        raise RuntimeError("same-task UI retry did not reach the authoritative fixture")
    cli(["close"], "close", check=False)
    delta_first = after_first["committed_mutations"] - before["committed_mutations"]
    delta_second = after_second["committed_mutations"] - after_first["committed_mutations"]
    success = delta_first == 1 and delta_second == 0 and after_second["keys"] == before["keys"] + 1
    report = {
        "benchmark_version": "scout-browser-reliability-v28",
        "comparison_protocol_version": "scout-browser-comparison-v1",
        "browser_state_restore_enabled": True,
        "browser_state_restore_flags": ["--restore", "--restore-save", "always"],
        "browser_state_restore_validation": "restore-status-and-localStorage-marker-reported-independently",
        "fixture_version": "scout-browser-reliability-v28",
        "tool_name": "agent-browser",
        "tool_version": version,
        "scenario_id": "application-idempotency-after-ui-replay",
        "fixture_isolation": "fresh fixture server process per tool run; adapter-local application key store",
        "mechanism": "repeat the same browser UI submission; fixture page supplies a stable application Idempotency-Key",
        "browser_action_first_call": first.returncode == 0,
        "browser_action_same_key_retry": second.returncode == 0,
        "first_call_output": first_result,
        "same_key_retry_output": second_result,
        "first_call_effect_delta": delta_first,
        "same_key_retry_effect_delta": delta_second,
        "idempotency_key_count_delta": after_second["keys"] - before["keys"],
        "tool_native_task_id_guard_tested": False,
        "host_supplied_idempotency_tested": False,
        "application_idempotency_tested": True,
        "success": success,
        "interpretation": "Tests that an application-provided idempotency key suppresses duplicate effects when the same UI action is repeated through separate agent-browser CLI processes. It does not test native task-ID replay guards, outcome classification, or browser restart recovery.",
    }
    (root / "agent-browser-idempotency-comparison.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    return 0 if success else 1


def run_process_recovery_probe(version, base_url, root, fault="daemon"):
    """Probe restore persistence and committed-action visibility across a hard browser kill."""
    run_key = secrets.token_hex(4)
    session = f"pr-{run_key}"
    namespace = f"pr-{run_key}"

    def cli(args, label, check=True):
        command = ["npx", "--yes", f"agent-browser@{version}", "--session", session,
                   "--namespace", namespace, *args]
        proc = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        save_output(root, f"agent-browser.process-recovery.{label}", proc)
        if check and proc.returncode:
            raise RuntimeError(f"{label} failed: {proc.stderr.decode(errors='replace')}")
        return proc

    def parse_info(label):
        proc = cli(["session", "info", "--json"], label)
        try:
            return json.loads(proc.stdout)["data"]
        except (ValueError, KeyError, TypeError) as exc:
            raise RuntimeError(f"could not parse session info for {label}") from exc

    cli(["--restore", "--restore-save", "always", "open", base_url + "/process-recovery"], "open")
    cli(["click", "#seed-marker"], "seed-marker")
    cli(["get", "text", "#marker"], "marker-before")
    cli(["--restore", "--restore-save", "always", "session", "info", "--json"], "info-before-mutation")
    cli(["click", "#mutate"], "click-mutation")

    deadline = time.monotonic() + 10
    before_kill = fixture_process_recovery_state(base_url)
    while before_kill["committed_mutations"] < 1 and time.monotonic() < deadline:
        time.sleep(0.01)
        before_kill = fixture_process_recovery_state(base_url)
    if before_kill["committed_mutations"] != 1:
        raise RuntimeError("fixture mutation did not commit before the hard kill")

    initial_info = parse_info("info-before-kill")
    pid = initial_info.get("pid")
    if not isinstance(pid, int) or pid <= 1:
        raise RuntimeError(f"missing isolated agent-browser daemon PID: {pid!r}")
    browser_pids = []
    if fault == "browser":
        ps = subprocess.run(["ps", "-axo", "pid=,ppid=,command="], check=True, text=True, capture_output=True)
        rows = []
        for line in ps.stdout.splitlines():
            parts = line.strip().split(None, 2)
            if len(parts) == 3 and parts[0].isdigit() and parts[1].isdigit():
                rows.append((int(parts[0]), int(parts[1]), parts[2]))
        descendants = {pid}
        changed = True
        while changed:
            changed = False
            for child, parent, command in rows:
                if parent in descendants and child not in descendants:
                    descendants.add(child)
                    changed = True
                    if "Google Chrome" in command or "Chromium" in command:
                        browser_pids.append(child)
        if not browser_pids:
            raise RuntimeError("could not find Chrome child processes of the isolated agent-browser daemon")
        for browser_pid in browser_pids:
            os.kill(browser_pid, signal.SIGKILL)
    os.kill(pid, signal.SIGKILL)

    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            current = fixture_process_recovery_state(base_url)
            if current["committed_mutations"] == 1:
                break
        except Exception:
            pass
        time.sleep(0.02)
    restored_info = cli(["--restore", "--restore-save", "always", "session", "info", "--json"], "info-after-kill", check=False)
    try:
        info_data = json.loads(restored_info.stdout).get("data", {})
    except ValueError:
        info_data = {}

    # An explicit URL makes restore validation deterministic while preserving
    # the namespaced browser storage that --restore is designed to recover.
    reopened = cli(["--restore", "--restore-save", "always", "open", base_url + "/process-recovery"], "reopen-after-kill", check=False)
    observed = cli(["get", "text", "#marker"], "marker-after-kill", check=False)
    marker = observed.stdout.decode(errors="replace").strip() if observed.returncode == 0 else ""
    after = fixture_process_recovery_state(base_url)
    cli(["click", "#mutate"], "blind-replay", check=False)
    replay_deadline = time.monotonic() + 10
    after_replay = fixture_process_recovery_state(base_url)
    while after_replay["committed_mutations"] < 2 and time.monotonic() < replay_deadline:
        time.sleep(0.01)
        after_replay = fixture_process_recovery_state(base_url)
    cli(["close"], "close", check=False)

    report = {
        "benchmark_version": "scout-browser-reliability-v28",
        "comparison_protocol_version": "scout-browser-comparison-v1",
        "fixture_version": "scout-browser-reliability-v28",
        "tool_name": "agent-browser",
        "tool_version": version,
        "scenario_id": "committed-mutation-hard-browser-kill",
        "fixture_url": base_url + "/process-recovery",
        "fault_injection": (
            "SIGKILL the unique agent-browser daemon PID after server commit while its response is held open"
            if fault == "daemon" else
            "SIGKILL Chrome child processes discovered beneath the unique agent-browser daemon, then SIGKILL the daemon, after server commit while its response is held open"
        ),
        "fault_boundary": fault,
        "browser_child_pids_killed": browser_pids,
        "mutation_committed_before_kill": before_kill["committed_mutations"] == 1,
        "mutation_count_after_restart": after["committed_mutations"],
        "mutation_count_after_blind_replay": after_replay["committed_mutations"],
        "session_marker_restored": marker == "saved",
        "session_marker_before_kill": "saved",
        "restore_status_after_restart": (
            info_data.get("runtime") or {}
        ).get("restoreStatus", (
            (info_data.get("runtime") or {}).get("lifecycle") or {}
        ).get("restoreStatus")),
        "reopen_succeeded": reopened.returncode == 0,
        "native_committed_action_record_observed": False,
        "native_task_id_replay_guard_tested": False,
        "blind_same_action_replay_dispatched": True,
        "duplicate_side_effect_after_blind_replay": after_replay["committed_mutations"] == 2,
        "host_must_reconcile_before_retry": True,
        "mutation_outcome_preserved": before_kill["committed_mutations"] == 1 and after["committed_mutations"] == 1,
        "success": before_kill["committed_mutations"] == 1 and after["committed_mutations"] == 1,
        "interpretation": "The first post-restart fixture read shows whether the original side effect remained committed. Browser-state restoration is explicitly enabled and measured separately. The runner then deliberately repeats the same UI action on this harmless local fixture; a second count demonstrates duplicate effect from blind replay. This does not test host-supplied idempotency or a native task-ID replay guard.",
        "raw_output_files": sorted(path.name for path in root.glob("agent-browser.process-recovery.*")),
    }
    report_path = root / f"agent-browser-process-recovery-{fault}-comparison.json"
    report_path.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    return 0 if report["success"] else 1


def run_late_recovery_scenario(version, repetitions, base_url, root, browser_version):
    rows = []
    deadline_ms = 300
    for number in range(1, repetitions + 1):
        session = f"scout-late-recovery-{number}"
        opened = run_cli(version, session, ["open", base_url + "/late-action"])
        save_output(root, f"agent-browser.late-recovery-{number:02d}.open", opened)
        if opened.returncode:
            raise RuntimeError(f"open failed in late recovery run {number}: {opened.stderr.decode(errors='replace')}")
        before = fixture_late_state(base_url)
        command = ["npx", "--yes", f"agent-browser@{version}", "--session", session, "click", "#late-action"]
        started = time.perf_counter()
        proc = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        dispatch_deadline = time.monotonic() + 10
        while fixture_late_state(base_url)["started_count"] <= before["started_count"]:
            if proc.poll() is not None:
                stdout, stderr = proc.communicate()
                raise RuntimeError(f"click exited before confirmed dispatch in run {number}: {stderr.decode(errors='replace')}")
            if time.monotonic() >= dispatch_deadline:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.communicate()
                raise RuntimeError(f"no browser dispatch signal in run {number}")
            time.sleep(0.01)
        dispatch_at = time.monotonic()
        time.sleep(deadline_ms / 1000)
        timed_out = proc.poll() is None
        if timed_out:
            os.killpg(proc.pid, signal.SIGKILL)
        stdout, stderr = proc.communicate(timeout=5)
        (root / f"agent-browser.late-recovery-{number:02d}.click.stdout").write_bytes(stdout)
        (root / f"agent-browser.late-recovery-{number:02d}.click.stderr").write_bytes(stderr)
        state_deadline = time.monotonic() + 10
        while fixture_late_state(base_url)["completed_count"] <= before["completed_count"]:
            if time.monotonic() >= state_deadline:
                raise RuntimeError(f"late mutation did not complete in run {number}")
            time.sleep(0.01)
        observed = run_cli(version, session, ["get", "text", "#late-status"])
        save_output(root, f"agent-browser.late-recovery-{number:02d}.observe", observed)
        if observed.returncode:
            raise RuntimeError(f"inspection failed in late recovery run {number}: {observed.stderr.decode(errors='replace')}")
        final_text = observed.stdout.decode(errors="replace").strip()
        elapsed_ms = (time.monotonic() - dispatch_at) * 1000
        rows.append({
            "repetition": number,
            "dispatch_observed": True,
            "caller_wait_ended_by_deadline": timed_out,
            "completion_observed": True,
            "final_text": final_text,
            "success": timed_out and final_text == "Late completion",
            "elapsed_from_dispatch_to_inspection_ms": elapsed_ms,
        })
        closed = run_cli(version, session, ["close"])
        save_output(root, f"agent-browser.late-recovery-{number:02d}.close", closed)
        if closed.returncode:
            raise RuntimeError(f"close failed in late recovery run {number}")

    durations = sorted(row["elapsed_from_dispatch_to_inspection_ms"] for row in rows)
    report = {
        "benchmark_version": "scout-browser-reliability-v28",
        "comparison_protocol_version": "scout-browser-comparison-v1",
        "fixture_version": "scout-browser-reliability-v28",
        "tool_name": "agent-browser",
        "tool_version": version,
        "browser_version": browser_version,
        "scenario_id": "late-action-caller-timeout",
        "fixture_url": "local deterministic HandlerWithLateActionSignals /late-action",
        "caller_deadline_ms_after_confirmed_dispatch": deadline_ms,
        "verification_predicate": "confirmed click dispatch; caller wait ends at 300 ms before mutation completion; after late completion, #late-status text equals 'Late completion'",
        "success": all(row["success"] for row in rows),
        "false_success": False,
        "repetitions": len(rows),
        "successful_repetitions": sum(row["success"] for row in rows),
        "workflow_duration_ms": {
            "median": statistics.median(durations),
            "p95": durations[math.ceil(0.95 * len(durations)) - 1],
            "samples": durations,
            "includes": "from confirmed dispatch through caller interruption, late completion, and CLI state inspection; excludes browser startup/navigation",
        },
        "duration_scope": "from confirmed dispatch through caller interruption, late completion, and CLI state inspection; excludes browser startup/navigation",
        "tool_calls_per_repetition": 4,
        "output_bytes": sum(p.stat().st_size for p in root.glob("agent-browser.late-recovery-*.*")),
        "output_bytes_definition": "combined stdout/stderr for open, click, inspection, and close CLI calls across all runs; excludes external dispatch state probes and JSON reports",
        "native_recovery_record": False,
        "recovery_method": "caller terminates the CLI process at its deadline, waits for the fixture completion signal, then starts a separate CLI inspection command; no action replay",
        "raw_results_file": "agent-browser-late-recovery-repetitions.json",
    }
    (root / "agent-browser-late-recovery-repetitions.json").write_text(json.dumps(rows, indent=2) + "\n")
    (root / "agent-browser-late-recovery-comparison.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    return 0 if report["success"] else 1


def run_background_tab_scenario(version, repetitions, base_url, root, browser_version, scout_report):
    rows = []
    for number in range(1, repetitions + 1):
        session = f"scout-background-tab-{number}"
        task_url = base_url + f"/background-tab?run={number}"
        steps = [
            ("open", ["open", "about:blank"]),
            ("task-tab", ["tab", "new", "--label", "background-task", task_url]),
            ("foreground-tab", ["tab", "new", "--label", "foreground-guard", f"{base_url}/foreground-tab?run={number}"]),
            ("targets-before", ["--json", "tab", "list"]),
            ("select-task", ["tab", "background-task"]),
            ("targets-selected", ["--json", "tab", "list"]),
            ("click", ["click", "#background-action"]),
            ("targets-after", ["--json", "tab", "list"]),
            ("visibility", ["eval", "JSON.stringify(window.__visibilityTransitions)"]),
            ("status", ["get", "text", "#status"]),
        ]
        started = time.perf_counter_ns()
        outputs = {}
        errors = {}
        for label, command_args in steps:
            proc = run_cli(version, session, command_args)
            save_output(root, f"agent-browser.background-only-{number:02d}.{label}", proc)
            if proc.returncode:
                raise RuntimeError(f"background tab {label} failed in run {number}: {proc.stderr.decode(errors='replace')}")
            outputs[label] = proc.stdout.decode(errors="replace").strip()
        try:
            transitions = json.loads(outputs["visibility"])
            if isinstance(transitions, str):
                transitions = json.loads(transitions)
        except (ValueError, TypeError):
            transitions = []
        try:
            tab_lists = {
                label: json.loads(outputs[label])
                for label in ("targets-before", "targets-selected", "targets-after")
            }
            def labeled_task(snapshot):
                return next(
                    tab for tab in snapshot["data"]["tabs"]
                    if tab.get("label") == "background-task"
                )
            task_before = labeled_task(tab_lists["targets-before"])
            task_selected = labeled_task(tab_lists["targets-selected"])
            task_after = labeled_task(tab_lists["targets-after"])
            guard_after = next(tab for tab in tab_lists["targets-after"]["data"]["tabs"] if tab.get("label") == "foreground-guard")
            target_identity_ok = (
                bool(task_before.get("targetId"))
                and task_before.get("targetId") == task_selected.get("targetId") == task_after.get("targetId")
                and task_selected.get("active") is True
                and task_after.get("active") is True
                and task_after.get("url") == task_url
                and guard_after.get("url") == f"{base_url}/foreground-tab?run={number}"
            )
            target_id = task_after.get("targetId", "")
        except (ValueError, TypeError, KeyError, StopIteration):
            target_identity_ok = False
            target_id = ""
        elapsed = (time.perf_counter_ns() - started) / 1_000_000
        rows.append({
            "repetition": number,
            "success": outputs["status"] == "Background task complete" and target_identity_ok,
            "target_identity_verified": target_identity_ok,
            "target_id": target_id,
            "visibility_transitions": transitions,
            "visibility_contract_diagnostic": "headless Chrome visibility transitions are recorded but are not a target-identity success gate",
            "status_text": outputs["status"],
            "workflow_duration_ms": elapsed,
            "tool_calls": len(steps),
        })
        close = run_cli(version, session, ["close"])
        save_output(root, f"agent-browser.background-only-{number:02d}.close", close)
        if close.returncode:
            raise RuntimeError(f"background tab close failed in run {number}")
    (root / "agent-browser-background-tab-repetitions.json").write_text(json.dumps(rows, indent=2) + "\n")
    durations = sorted(row["workflow_duration_ms"] for row in rows)
    scout = json.loads(pathlib.Path(scout_report).read_text())
    scout_rows = {row["id"]: row for row in scout["scenarios"]}
    raw_output_files = sorted(
        path.name for path in root.glob("agent-browser.background-only-*.*")
        if path.name.endswith((".stdout", ".stderr"))
    )
    output_bytes = sum((root / name).stat().st_size for name in raw_output_files)
    report = {
        "benchmark_version": scout["benchmark_version"],
        "tool_name": "agent-browser",
        "tool_version": version,
        "browser_version": browser_version,
        "scenario_id": "background-tab-action",
        "fixture_url": base_url + "/background-tab",
        "success": all(row["success"] for row in rows),
        "successful_repetitions": sum(row["success"] for row in rows),
        "repetitions": repetitions,
        "false_success": False,
        "operation": "open uniquely addressed task and guard tabs, select the task tab, click action, and read target, visibility, and status evidence",
        "verification_predicate": "targetId remains stable and active on the unique expected URL; status is Background task complete; visibility transitions are diagnostic",
        "success_predicate": "targetId stays constant and selected/active on the unique expected URL; status text is Background task complete",
        "workflow_duration_ms": {
            "median": statistics.median(durations),
            "p95": durations[max(0, math.ceil(0.95 * len(durations)) - 1)],
            "samples": durations,
            "includes": "ten agent-browser CLI subprocesses per repetition; excludes close and version/browser metadata probes",
        },
        "action_calls_per_repetition": 1,
        "tool_calls_per_repetition": 10,
        "output_bytes": output_bytes,
        "output_bytes_definition": "combined stdout and stderr bytes for all background-tab subprocesses across repetitions, including close commands; excludes version/browser metadata probes and JSON reports",
        "raw_output_file": raw_output_files,
        "raw_results_file": "agent-browser-background-tab-repetitions.json",
        "scout_comparison": scout_rows["background-tab-action"],
    }
    (root / "agent-browser-background-tab-comparison.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    return 0 if report["success"] else 1


def run_frame_scope_after_tab_close(version, repetitions, base_url, root, browser_version):
    """Reproduce agent-browser issue #2407 using a pre-captured iframe ref."""
    rows = []
    steps = [
        ("open-task", ["open", base_url + "/iframe"]),
        ("create-background-tab", ["tab", "new", base_url + "/foreground-tab", "--label", "bg"]),
        ("select-task", ["tab", "t1"]),
        ("select-frame", ["frame", "#task-frame"]),
        ("snapshot", ["snapshot", "-i"]),
        ("close-unrelated", ["tab", "close", "bg"]),
        ("click-original-ref", ["click", "@e1"]),
        ("status", ["get", "text", "#frame-result"]),
    ]
    for number in range(1, repetitions + 1):
        session = f"scout-frame-close-{number}"
        started = time.perf_counter_ns()
        outputs = {}
        errors = {}
        failed_step = ""
        for label, command_args in steps:
            proc = run_cli(version, session, command_args)
            save_output(root, f"agent-browser.frame-close-{number:02d}.{label}", proc)
            outputs[label] = proc.stdout.decode(errors="replace").strip()
            errors[label] = proc.stderr.decode(errors="replace").strip()
            if proc.returncode:
                failed_step = label
                break
        duration = (time.perf_counter_ns() - started) / 1_000_000
        closed = failed_step in ("click-original-ref", "status")
        status = ""
        if not failed_step:
            status = outputs.get("status", "")
        click_ok = not failed_step and status == "Frame task complete"
        rows.append({
            "repetition": number,
            "success": click_ok,
            "failure_step": failed_step or None,
            "failure_text": errors.get(failed_step, "") if failed_step else "",
            "unknown_ref_failure": failed_step == "click-original-ref" and "Unknown ref" in errors.get("click-original-ref", ""),
            "status_text": status,
            "workflow_duration_ms": duration,
            "tool_calls": len(steps) if not failed_step else next(i + 1 for i, (label, _) in enumerate(steps) if label == failed_step),
        })
        (root / f"agent-browser.frame-close-{number:02d}.json").write_text(json.dumps(rows[-1], indent=2) + "\n")
        close = run_cli(version, session, ["close"])
        save_output(root, f"agent-browser.frame-close-{number:02d}.close", close)
    report = {
        "scenario_id": "frame-scope-after-unrelated-tab-close",
        "tool_name": "agent-browser",
        "tool_version": version,
        "browser_version": browser_version,
        "fixture_url": base_url + "/iframe",
        "success": all(row["success"] for row in rows),
        "successful_repetitions": sum(row["success"] for row in rows),
        "repetitions": repetitions,
        "unknown_ref_failures": sum(row["unknown_ref_failure"] for row in rows),
        "workflow_duration_ms": {
            "median": statistics.median(row["workflow_duration_ms"] for row in rows),
            "p95": sorted(row["workflow_duration_ms"] for row in rows)[max(0, math.ceil(0.95 * len(rows)) - 1)],
        },
        "operation": "open the iframe task, create and select an unrelated tab, reselect the task, capture @e1, close the unrelated tab, click @e1, and inspect resulting page text",
        "step_order": [label for label, _ in steps],
        "repetitions_detail": rows,
        "raw_output_glob": "agent-browser.frame-close-*.*.stdout|stderr",
        "interpretation": "A version-specific regression reproduction on a local deterministic fixture, not a product-wide reliability score. Re-run after upstream PR #2408 is released.",
    }
    (root / "agent-browser-frame-close-comparison.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    return 0 if report["success"] else 1


def run_session_isolation_scenario(version, repetitions, base_url, root, browser_version, scout_report):
    rows = []
    for number in range(1, repetitions + 1):
        primary = f"scout-isolation-primary-{number}"
        isolated = f"scout-isolation-secondary-{number}"
        steps = [
            ("primary-seed", primary, ["open", base_url + "/session-owner/seed"]),
            ("secondary-probe", isolated, ["open", base_url + "/session-owner/probe"]),
            ("secondary-initial-state", isolated, ["get", "text", "#state"]),
            ("secondary-check-page", isolated, ["open", base_url + "/session-owner/check"]),
            ("secondary-final-state", isolated, ["get", "text", "#state"]),
            ("primary-check-page", primary, ["open", base_url + "/session-owner/check"]),
            ("primary-final-state", primary, ["get", "text", "#state"]),
        ]
        started = time.perf_counter_ns()
        outputs = {}
        elapsed = 0
        try:
            for label, session, command_args in steps:
                proc = run_cli(version, session, command_args)
                save_output(root, f"agent-browser.isolation-{number:02d}.{label}", proc)
                if proc.returncode:
                    raise RuntimeError(f"agent-browser {label} failed in repetition {number}: {proc.stderr.decode(errors='replace')}")
                outputs[label] = proc.stdout.decode(errors="replace").strip()
            elapsed = (time.perf_counter_ns() - started) / 1_000_000
        finally:
            close_errors = []
            for label, session in (("primary-close", primary), ("secondary-close", isolated)):
                close = run_cli(version, session, ["close"])
                save_output(root, f"agent-browser.isolation-{number:02d}.{label}", close)
                if close.returncode:
                    close_errors.append(f"{session}: {close.stderr.decode(errors='replace')}")
            if close_errors:
                raise RuntimeError("failed to close agent-browser sessions: " + "; ".join(close_errors))
        try:
            initial_state = json.loads(outputs["secondary-initial-state"])
            secondary_state = json.loads(outputs["secondary-final-state"])
            primary_state = json.loads(outputs["primary-final-state"])
        except (KeyError, ValueError, TypeError):
            initial_state, secondary_state, primary_state = None, None, None
        rows.append({
            "repetition": number,
            "success": initial_state == ["", None] and secondary_state == ["scout_owner=secondary", "secondary"] and primary_state == ["scout_owner=primary", "primary"],
            "secondary_initial_state": initial_state,
            "secondary_final_state": secondary_state,
            "primary_final_state": primary_state,
            "workflow_duration_ms": elapsed,
            "tool_calls_per_repetition": len(steps),
            "verification": "caller reads same-origin cookie and localStorage from both named sessions; second session begins empty, writes secondary values, and primary values remain unchanged",
        })
    raw_file = "agent-browser-session-isolation-repetitions.json"
    (root / raw_file).write_text(json.dumps(rows, indent=2) + "\n")
    durations = sorted(row["workflow_duration_ms"] for row in rows)
    scout = json.loads(pathlib.Path(scout_report).read_text())
    scout_row = next((row for row in scout["scenarios"] if row["id"] == "mcp-process-session-isolation"), None)
    raw_files = sorted(root.glob("agent-browser.isolation-*.*"))
    report = {
        "benchmark_version": scout["benchmark_version"],
        "tool_name": "agent-browser",
        "tool_version": version,
        "browser_version": browser_version,
        "scenario_id": "mcp-process-session-isolation",
        "fixture_url": base_url + "/session-owner/seed",
        "success": len(rows) == repetitions and all(row["success"] for row in rows),
        "successful_repetitions": sum(row["success"] for row in rows),
        "repetitions": repetitions,
        "false_success": False,
        "native_session_isolation": True,
        "verification_location": "adapter reads page state from each named session; no page-side sharing or fixture-specific tool is used",
        "workflow_duration_ms": {
            "median": statistics.median(durations),
            "p95": durations[max(0, math.ceil(0.95 * len(durations)) - 1)],
            "samples": durations,
            "includes": "seven agent-browser CLI subprocesses per repetition, including both session opens and caller-side state reads",
            "excludes": "version/browser probe and session close",
        },
        "tool_calls_per_repetition": len(steps),
        "raw_results_file": raw_file,
        "raw_evidence_files": [path.name for path in raw_files],
        "scout_comparison": {
            "success": scout_row["success"] if scout_row else False,
            "verified": scout_row["verified"] if scout_row else False,
            "outcome": scout_row.get("outcome") if scout_row else "missing_scenario",
            "evidence": scout_row.get("evidence") if scout_row else "",
            "repetitions": 1,
            "interpretation": "Scout is one local MCP-process run; agent-browser repetitions are independent named-session runs. Both use the same fixture state predicate, but the execution interfaces and repetition counts differ.",
        },
    }
    (root / "agent-browser-session-isolation-comparison.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    return 0 if report["success"] and report["scout_comparison"]["success"] else 1


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--repetitions", type=int, default=10)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--results", required=True)
    parser.add_argument("--scout-report")
    parser.add_argument("--background-only", action="store_true")
    parser.add_argument("--session-isolation-only", action="store_true")
    parser.add_argument("--frame-close-only", action="store_true")
    parser.add_argument("--verified-action-only", action="store_true")
    parser.add_argument("--late-recovery-only", action="store_true")
    parser.add_argument("--process-recovery-only", action="store_true")
    parser.add_argument("--process-recovery-fault", choices=("daemon", "browser"), default="daemon")
    parser.add_argument("--caller-idempotency-only", action="store_true")
    args = parser.parse_args()
    if args.repetitions < 10 and not args.process_recovery_only:
        parser.error("--repetitions must be at least 10")

    root = pathlib.Path(args.results)
    version = subprocess.run(
        ["npx", "--yes", f"agent-browser@{args.version}", "--version"],
        check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    (root / "agent-browser.version.txt").write_bytes(version.stdout)

    if args.process_recovery_only:
        return run_process_recovery_probe(args.version, args.base_url, root, args.process_recovery_fault)
    if args.caller_idempotency_only:
        return run_caller_idempotency_probe(args.version, args.base_url, root)

    if not args.scout_report:
        parser.error("--scout-report is required outside process-recovery mode")

    browser_session = "scout-bench-meta"
    browser = run_cli(args.version, browser_session, ["open", args.base_url + "/action"])
    save_output(root, "agent-browser.browser-open", browser)
    if browser.returncode:
        raise RuntimeError(browser.stderr.decode(errors="replace"))
    ua = run_cli(args.version, browser_session, ["eval", "navigator.userAgent"])
    save_output(root, "agent-browser.user-agent", ua)
    if ua.returncode:
        raise RuntimeError(ua.stderr.decode(errors="replace"))
    run_cli(args.version, browser_session, ["close"])
    browser_user_agent = ua.stdout.decode(errors="replace").strip()
    chrome_version = re.search(r"(?:Headless)?Chrome/(\d+(?:\.\d+)+)", browser_user_agent)
    browser_version = f"HeadlessChrome {chrome_version.group(1)}" if chrome_version else "unknown"

    if args.late_recovery_only:
        return run_late_recovery_scenario(args.version, args.repetitions, args.base_url, root, browser_version)
    if args.background_only:
        return run_background_tab_scenario(args.version, args.repetitions, args.base_url, root, browser_version, args.scout_report)
    if args.session_isolation_only:
        return run_session_isolation_scenario(args.version, args.repetitions, args.base_url, root, browser_version, args.scout_report)
    if args.frame_close_only:
        return run_frame_scope_after_tab_close(args.version, args.repetitions, args.base_url, root, browser_version)
    if args.verified_action_only:
        rows = []
        transcripts = []
        for number in range(1, args.repetitions + 1):
            session = f"scout-verified-{number}"
            steps = [
                ("open", ["open", args.base_url + "/action"]),
                ("snapshot", ["snapshot", "-i"]),
                ("click", ["click", "#complete"]),
                ("status", ["get", "text", "#status"]),
            ]
            started = time.perf_counter_ns()
            outputs = {}
            for label, command in steps:
                proc = run_cli(args.version, session, command)
                filename = f"agent-browser.verified-action-{number:02d}.{label}"
                save_output(root, filename, proc)
                transcripts.extend([filename + ".stdout", filename + ".stderr"])
                if proc.returncode:
                    raise RuntimeError(f"{label} failed in repetition {number}: {proc.stderr.decode(errors='replace')}")
                outputs[label] = proc.stdout.decode(errors="replace").strip()
            elapsed = (time.perf_counter_ns() - started) / 1_000_000
            close = run_cli(args.version, session, ["close"])
            filename = f"agent-browser.verified-action-{number:02d}.close"
            save_output(root, filename, close)
            transcripts.extend([filename + ".stdout", filename + ".stderr"])
            if close.returncode:
                raise RuntimeError(f"close failed in repetition {number}")
            rows.append({"repetition": number, "success": outputs["status"] == "Task complete", "status_text": outputs["status"], "workflow_duration_ms": elapsed, "tool_calls": 4})
        durations = sorted(row["workflow_duration_ms"] for row in rows)
        report = {
            "benchmark_version": "scout-browser-reliability-v28",
            "comparison_protocol_version": "scout-browser-comparison-v1",
            "fixture_version": "scout-browser-reliability-v28",
            "tool_name": "agent-browser",
            "tool_version": args.version,
            "browser_version": browser_version,
            "browser_user_agent": browser_user_agent,
            "scenario_id": "verified-action",
            "fixture_url": args.base_url + "/action",
            "verification_predicate": "#status text exactly equals 'Task complete'",
            "success": all(row["success"] for row in rows),
            "false_success": False,
            "repetitions": len(rows),
            "successful_repetitions": sum(row["success"] for row in rows),
            "workflow_duration_ms": {"median": statistics.median(durations), "p95": durations[math.ceil(0.95 * len(durations)) - 1], "samples": durations, "includes": "four CLI subprocesses per repetition: open, snapshot, click, get text; excludes browser metadata probes and close"},
            "duration_scope": "four CLI subprocesses per repetition: open, snapshot, click, get text; excludes browser metadata probes and close",
            "action_calls_per_repetition": 1,
            "tool_calls_per_repetition": 4,
            "output_bytes": sum((root / name).stat().st_size for name in transcripts),
            "output_bytes_definition": "combined stdout and stderr bytes for open, snapshot, click, get text, and close across repetitions; excludes version and browser metadata probes and JSON reports",
            "raw_results_file": "agent-browser-verified-action-repetitions.json",
        }
        (root / "agent-browser-verified-action-repetitions.json").write_text(json.dumps(rows, indent=2) + "\n")
        (root / "agent-browser-verified-action-comparison.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
        return 0 if report["success"] else 1

    rows = []
    form_rows = []
    replay_rows = []
    scope_rows = []
    background_tab_rows = []
    scope_output_bytes = 0
    bounded_output_bytes = 0
    for number in range(1, args.repetitions + 1):
        session = f"scout-bench-{number}"
        steps = [
            ("open", ["open", args.base_url + "/action"]),
            ("snapshot", ["snapshot", "-i"]),
            ("click", ["click", "#complete"]),
            ("status", ["get", "text", "#status"]),
        ]
        started = time.perf_counter_ns()
        output = {}
        for label, command_args in steps:
            proc = run_cli(args.version, session, command_args)
            save_output(root, f"agent-browser.run-{number:02d}.{label}", proc)
            if proc.returncode:
                raise RuntimeError(f"{label} failed in run {number}: {proc.stderr.decode(errors='replace')}")
            output[label] = proc.stdout.decode(errors="replace").strip()
        elapsed_ms = (time.perf_counter_ns() - started) / 1_000_000
        row = {
            "repetition": number,
            "success": output["status"] == "Task complete",
            "status_text": output["status"],
            "workflow_duration_ms": elapsed_ms,
        }
        rows.append(row)

        replay_session = f"scout-replay-{number}"
        replay_steps = [
            ("open", ["open", args.base_url + "/replay"]),
            ("snapshot", ["snapshot", "-i"]),
            ("click", ["click", "#advance"]),
            ("first-state", ["get", "text", "#status"]),
        ]
        replay_started = time.perf_counter_ns()
        replay_output = {}
        for label, command_args in replay_steps:
            proc = run_cli(args.version, replay_session, command_args)
            save_output(root, f"agent-browser.run-{number:02d}.replay-{label}", proc)
            if proc.returncode:
                raise RuntimeError(f"replay {label} failed in run {number}: {proc.stderr.decode(errors='replace')}")
            replay_output[label] = proc.stdout.decode(errors="replace").strip()
        first_success = replay_output["first-state"] == "Step complete"
        repair_steps = [
            ("controlled-change", ["eval", "document.querySelector('#status').textContent='Different state'"]),
            ("changed-state", ["get", "text", "#status"]),
            ("repair", ["click", "#repair"]),
            ("resume-action", ["click", "#advance"]),
            ("resumed-state", ["get", "text", "#status"]),
        ]
        for label, command_args in repair_steps:
            proc = run_cli(args.version, replay_session, command_args)
            save_output(root, f"agent-browser.run-{number:02d}.replay-{label}", proc)
            if proc.returncode:
                raise RuntimeError(f"replay recovery {label} failed in run {number}: {proc.stderr.decode(errors='replace')}")
            replay_output[label] = proc.stdout.decode(errors="replace").strip()
        replay_elapsed = (time.perf_counter_ns() - replay_started) / 1_000_000
        replay_row = {
            "repetition": number,
            "successful_action_assertion": first_success,
            "expected_text": "Step complete",
            "observed_first_state": replay_output["first-state"],
            "changed_state_observed": replay_output["changed-state"] == "Different state",
            "changed_expectation_detected": replay_output["changed-state"] != "Step complete",
            "recovery_action_dispatched": replay_output["repair"] == "✓ Done",
            "resumed_state": replay_output["resumed-state"],
            "recovery_succeeded": replay_output["resumed-state"] == "Step complete",
            "failure_evidence": replay_output["changed-state"],
            "failure_detection": "caller-side text comparison; CLI has no saved playbook or native resume result",
            "workflow_duration_ms": replay_elapsed,
            "tool_calls": len(replay_steps) + len(repair_steps),
        }
        replay_row["success"] = first_success and replay_row["changed_state_observed"] and replay_row["changed_expectation_detected"] and replay_row["recovery_action_dispatched"] and replay_row["recovery_succeeded"]
        replay_rows.append(replay_row)
        close_replay = run_cli(args.version, replay_session, ["close"])
        save_output(root, f"agent-browser.run-{number:02d}.replay-close", close_replay)
        if close_replay.returncode:
            raise RuntimeError(f"replay close failed in run {number}")

        for form_case, path, expected_status, expected_ui in (
            ("acceptance", "/form", 200, "Order accepted"),
            ("rejection", "/reject-form", 422, "Order accepted"),
        ):
            form_session = f"scout-form-{number}-{form_case}"
            form_steps = [
                ("open", ["open", args.base_url + path]),
                ("click", ["click", "#checkout button"]),
                ("wait", ["wait", "--text", expected_ui]),
                ("network", ["--json", "network", "requests", "--type", "xhr,fetch", "--method", "POST"]),
            ]
            form_started = time.perf_counter_ns()
            form_output = {}
            for label, command_args in form_steps:
                proc = run_cli(args.version, form_session, command_args)
                save_output(root, f"agent-browser.run-{number:02d}.form-{form_case}.{label}", proc)
                if proc.returncode:
                    raise RuntimeError(f"form {form_case} {label} failed in run {number}: {proc.stderr.decode(errors='replace')}")
                form_output[label] = proc.stdout.decode(errors="replace").strip()
            form_elapsed = (time.perf_counter_ns() - form_started) / 1_000_000
            try:
                network = json.loads(form_output["network"])
                requests = network["data"]["requests"]
                matching = [request for request in requests if request.get("url", "").endswith("/submit" if expected_status == 200 else "/reject")]
                actual_status = matching[-1]["status"] if matching else None
            except (ValueError, KeyError, TypeError, IndexError):
                actual_status = None
            form_row = {
                "repetition": number,
                "case": form_case,
                "expected_http_status": expected_status,
                "observed_http_status": actual_status,
                "ui_status_text": expected_ui,
                "ui_claim_matches_acceptance": expected_ui == "Order accepted",
                # Report whether the CLI exposes the transport outcome. The
                # fixture's rejection UI is intentionally misleading and its
                # false-success signal is recorded independently.
                "success": actual_status == expected_status,
                "naive_false_success_signal": form_case == "rejection" and actual_status == 422 and expected_ui == "Order accepted",
                "workflow_duration_ms": form_elapsed,
                "tool_calls": len(form_steps),
            }
            form_rows.append(form_row)
            close = run_cli(args.version, form_session, ["close"])
            save_output(root, f"agent-browser.run-{number:02d}.form-{form_case}.close", close)
            if close.returncode:
                raise RuntimeError(f"form {form_case} close failed in run {number}")
        large_page = run_cli(args.version, session, ["open", args.base_url + "/large"])
        save_output(root, f"agent-browser.run-{number:02d}.large-open", large_page)
        scoped = run_cli(args.version, session, ["snapshot", "-s", "main"])
        save_output(root, f"agent-browser.run-{number:02d}.scope", scoped)
        bounded = run_cli(args.version, session, ["--max-output", "2000", "snapshot"])
        save_output(root, f"agent-browser.run-{number:02d}.bounded", bounded)
        if large_page.returncode or scoped.returncode or bounded.returncode:
            raise RuntimeError(f"scope/output check failed in run {number}")
        scoped_text = scoped.stdout.decode(errors="replace")
        bounded_text = bounded.stdout.decode(errors="replace")
        scope_bytes = len(scoped.stdout)
        bounded_bytes = len(bounded.stdout)
        scope_output_bytes += scope_bytes
        bounded_output_bytes += bounded_bytes
        truncation = re.search(r"\[truncated: showing (\d+) of (\d+) chars", bounded_text)
        bounded_respected = bool(truncation and int(truncation.group(1)) == 2000)
        scope_rows.append({
            "repetition": number,
            "success": "Target record 042" in scoped_text and "Decoy-only target" not in scoped_text,
            "target_in_scope": "Target record 042" in scoped_text,
            "outside_scope_target_excluded": "Decoy-only target" not in scoped_text,
            "scoped_output_bytes": scope_bytes,
            "bounded_output_response_characters": len(bounded_text),
            "bounded_output_content_limit": 2000,
            "bounded_output_truncation_notice": truncation.group(0) if truncation else None,
            "bounded_output_respected": bounded_respected,
        })
        close = run_cli(args.version, session, ["close"])
        save_output(root, f"agent-browser.run-{number:02d}.close", close)
        if close.returncode:
            raise RuntimeError(f"close failed in run {number}")

        tab_session = f"scout-background-tab-{number}"
        tab_steps = [
            ("open", ["open", "about:blank"]),
            ("task-tab", ["tab", "new", "--label", "background-task", f"{args.base_url}/background-tab?run={number}"]),
            ("foreground-tab", ["tab", "new", "--label", "foreground-guard", f"{args.base_url}/foreground-tab?run={number}"]),
            ("select-task", ["tab", "background-task"]),
            ("click", ["click", "#background-action"]),
            ("visibility", ["eval", "JSON.stringify(window.__visibilityTransitions)"]),
            ("status", ["get", "text", "#status"]),
        ]
        tab_started = time.perf_counter_ns()
        tab_output = {}
        for label, command_args in tab_steps:
            proc = run_cli(args.version, tab_session, command_args)
            save_output(root, f"agent-browser.run-{number:02d}.background-tab-{label}", proc)
            if proc.returncode:
                raise RuntimeError(f"background tab {label} failed in run {number}: {proc.stderr.decode(errors='replace')}")
            tab_output[label] = proc.stdout.decode(errors="replace").strip()
        try:
            transitions_value = json.loads(tab_output["visibility"])
            if isinstance(transitions_value, str):
                transitions_value = json.loads(transitions_value)
        except (ValueError, TypeError):
            transitions_value = []
        transitioned = "hidden" in transitions_value and transitions_value[-1:] == ["visible"]
        tab_elapsed = (time.perf_counter_ns() - tab_started) / 1_000_000
        tabs_proc = run_cli(args.version, tab_session, ["tab", "list", "--json"])
        save_output(root, f"agent-browser.run-{number:02d}.background-tab-targets", tabs_proc)
        if tabs_proc.returncode:
            raise RuntimeError(f"background tab target inspection failed in run {number}: {tabs_proc.stderr.decode(errors='replace')}")
        tabs_value = json.loads(tabs_proc.stdout.decode(errors="replace"))
        expected_url = f"{args.base_url}/background-tab?run={number}"
        target_rows = tabs_value if isinstance(tabs_value, list) else tabs_value.get("tabs", [])
        task_rows = [tab for tab in target_rows if tab.get("url") == expected_url]
        selected_target = bool(task_rows) and bool(task_rows[0].get("active", task_rows[0].get("selected", False)))
        target_verified = len(task_rows) == 1 and selected_target and any(tab.get("url") == f"{args.base_url}/foreground-tab?run={number}" for tab in target_rows)
        background_tab_rows.append({
            "repetition": number,
            "success": target_verified and tab_output["status"] == "Background task complete",
            "target_identity_verified": target_verified,
            "expected_task_url": expected_url,
            "visibility_transitions": transitions_value,
            "hidden_observed": "hidden" in transitions_value,
            "visible_after_switch": transitions_value[-1:] == ["visible"],
            "status_text": tab_output["status"],
            "workflow_duration_ms": tab_elapsed,
            "tool_calls": len(tab_steps),
            "operation": "open uniquely addressed task and guard tabs, select task, click action, inspect active target URL and postcondition; visibility transitions are diagnostic",
        })
        close_tab = run_cli(args.version, tab_session, ["close"])
        save_output(root, f"agent-browser.run-{number:02d}.background-tab-close", close_tab)
        if close_tab.returncode:
            raise RuntimeError(f"background tab close failed in run {number}")

    (root / "agent-browser-repetitions.json").write_text(json.dumps(rows, indent=2) + "\n")
    (root / "agent-browser-form-repetitions.json").write_text(json.dumps(form_rows, indent=2) + "\n")
    (root / "agent-browser-replay-repetitions.json").write_text(json.dumps(replay_rows, indent=2) + "\n")
    (root / "agent-browser-forms.json").write_text(json.dumps({
        "repetitions": len(form_rows) // 2,
        "successful_status_observations": sum(row["success"] for row in form_rows),
        "naive_false_success_signals": sum(row["naive_false_success_signal"] for row in form_rows),
        "expected_http_status_predicate": "acceptance=200 and rejection=422 from matching POST request",
        "acceptance_predicate": "observed HTTP 200 and UI says Order accepted",
        "rejection_predicate": "observed HTTP 422 and UI does not claim Order accepted",
        "raw_results_file": "agent-browser-form-repetitions.json",
    }, indent=2) + "\n")
    (root / "agent-browser-scope-repetitions.json").write_text(json.dumps(scope_rows, indent=2) + "\n")
    (root / "agent-browser-background-tab-repetitions.json").write_text(json.dumps(background_tab_rows, indent=2) + "\n")
    durations = sorted(row["workflow_duration_ms"] for row in rows)
    p95 = durations[max(0, math.ceil(0.95 * len(durations)) - 1)]
    transcripts = sorted(
        path.name for path in root.glob("agent-browser.run-*.*")
        if path.name.endswith((".stdout", ".stderr"))
    )
    output_bytes = sum((root / name).stat().st_size for name in transcripts)
    scout = json.loads(pathlib.Path(args.scout_report).read_text())
    scout_rows = {row["id"]: row for row in scout["scenarios"]}
    success_count = sum(row["success"] for row in rows)
    report = {
        "benchmark_version": scout["benchmark_version"],
        "comparison_protocol_version": "scout-browser-comparison-v1",
        "fixture_version": scout["benchmark_version"],
        "tool_name": "agent-browser",
        "tool_version": args.version,
        "browser_version": browser_version,
        "browser_user_agent": browser_user_agent,
        "scenario_id": "verified-action",
        "fixture_url": args.base_url + "/action",
        "operation": "open; snapshot -i; click #complete; get text #status",
        "verification_predicate": "#status text exactly equals 'Task complete'",
        "success": success_count == len(rows),
        "false_success": False,
        "repetitions": len(rows),
        "successful_repetitions": success_count,
        "workflow_duration_ms": {
            "median": statistics.median(durations),
            "p95": p95,
            "samples": durations,
            "includes": "four CLI subprocesses per repetition: open, snapshot, click, get text",
            "excludes": "tool installation, version probe, browser user-agent probe, and close",
        },
        "action_calls_per_repetition": 1,
        "tool_calls_per_repetition": 4,
        "output_bytes": output_bytes,
        "output_bytes_definition": "combined stdout and stderr bytes for all click, form, scope/output, and close commands across all repetitions; excludes version and browser metadata probes and JSON reports",
        "raw_output_file": transcripts,
        "scout_comparison": {
            "success": scout_rows["verified-action"]["success"],
            "verified": scout_rows["verified-action"]["verified"],
            "outcome": scout_rows["verified-action"]["outcome"],
            "action_calls": scout_rows["verified-action"]["action_calls"],
            "tool_calls": 1,
            "output_bytes": scout_rows["verified-action"]["output_bytes"],
            "repetitions": 1,
        },
        "notes": "CLI output and Scout serialized-result bytes have different measurement boundaries. Timing includes per-command CLI startup and is an interface-level measurement, not isolated browser-action latency.",
        "scope_and_output": {
            "repetitions": len(scope_rows),
            "successful_repetitions": sum(row["success"] and row["bounded_output_respected"] for row in scope_rows),
            "target_predicate": "main-scoped snapshot includes Target record 042",
            "scope_exclusion_predicate": "main-scoped snapshot excludes footer text Decoy-only target",
            "output_limit_content_characters": 2000,
            "truncation_notice_overhead_included_in_response": True,
            "bounded_output_respected_repetitions": sum(row["bounded_output_respected"] for row in scope_rows),
            "scoped_snapshot_output_bytes": scope_output_bytes,
            "bounded_full_page_output_bytes": bounded_output_bytes,
            "raw_results_file": "agent-browser-scope-repetitions.json",
            "scout_comparison": {
                "scoped-large-page": {
                    "success": scout_rows["scoped-large-page"]["success"],
                    "serialized_output_bytes": scout_rows["scoped-large-page"]["output_bytes"],
                    "output_limit_bytes": scout_rows["scoped-large-page"]["expected_output_bytes_max"],
                },
                "scope-excludes-decoy": {
                    "success": scout_rows["scope-excludes-decoy"]["success"],
                    "serialized_output_bytes": scout_rows["scope-excludes-decoy"]["output_bytes"],
                },
            },
        },
        "forms": {
            "repetitions": len(form_rows),
            "successful_repetitions": sum(row["success"] for row in form_rows),
            "naive_false_success_signals": sum(row["naive_false_success_signal"] for row in form_rows),
            "expected_http_status_predicate": "acceptance=200 and rejection=422 from matching POST request",
            "acceptance_predicate": "observed HTTP 200 and UI says Order accepted",
            "rejection_predicate": "observed HTTP 422 and UI does not claim Order accepted",
            "raw_results_file": "agent-browser-form-repetitions.json",
            "scout_comparison": {
                scenario_id: {
                    "success": scout_rows[scenario_id]["success"],
                    "verified": scout_rows[scenario_id]["verified"],
                    "outcome": scout_rows[scenario_id]["outcome"],
                    "false_success": scout_rows[scenario_id]["false_success"],
                }
                for scenario_id in ("form-acceptance", "form-rejection")
            },
        },
        "playbook_replay": {
            "repetitions": len(replay_rows),
            "successful_repetitions": sum(row["success"] for row in replay_rows),
            "replay_successes": sum(row["successful_action_assertion"] for row in replay_rows),
            "changed_expectation_detections": sum(row["changed_expectation_detected"] for row in replay_rows),
            "recovery_successes": sum(row["recovery_succeeded"] for row in replay_rows),
            "workflow_duration_ms": {
                "median": statistics.median(row["workflow_duration_ms"] for row in replay_rows),
                "p95": sorted(row["workflow_duration_ms"] for row in replay_rows)[max(0, math.ceil(0.95 * len(replay_rows)) - 1)],
                "samples": [row["workflow_duration_ms"] for row in replay_rows],
                "includes": "nine CLI subprocesses per repetition, including open, snapshot, initial action/state check, controlled change/state read, repair, retry/state check",
                "excludes": "version probe and close",
            },
            "native_playbook_replay": False,
            "adapter_semantics": "direct CLI action plus caller-side expected-text comparison; controlled page change, repair click, then action retry",
            "raw_results_file": "agent-browser-replay-repetitions.json",
            "scout_comparison": {
                scenario_id: {
                    "success": scout_rows[scenario_id]["success"],
                    "verified": scout_rows[scenario_id]["verified"],
                    "outcome": scout_rows[scenario_id]["outcome"],
                    "false_success": scout_rows[scenario_id]["false_success"],
                }
                for scenario_id in ("playbook-replay", "playbook-change-detection", "playbook-resume-after-repair")
            },
        },
        "background_tab_action": {
            "repetitions": len(background_tab_rows),
            "successful_repetitions": sum(row["success"] for row in background_tab_rows),
            "success_predicate": "agent-browser tab list shows the unique expected task URL selected while guard URL remains present; status text is Background task complete; visibility transitions are diagnostic",
            "workflow_duration_ms": {
                "median": statistics.median(row["workflow_duration_ms"] for row in background_tab_rows),
                "p95": sorted(row["workflow_duration_ms"] for row in background_tab_rows)[max(0, math.ceil(0.95 * len(background_tab_rows)) - 1)],
                "samples": [row["workflow_duration_ms"] for row in background_tab_rows],
            "includes": "ten agent-browser CLI subprocesses per repetition: open blank, create task and guard tabs, inspect targets before/after selection/action, click, and read visibility trace/status",
                "excludes": "version probe and close",
            },
            "tool_calls_per_repetition": len(tab_steps),
            "raw_results_file": "agent-browser-background-tab-repetitions.json",
            "scout_comparison": {
                "success": scout_rows["background-tab-action"]["success"],
                "verified": scout_rows["background-tab-action"]["verified"],
                "evidence": scout_rows["background-tab-action"].get("evidence"),
            },
        },
    }
    (root / "agent-browser-comparison.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    if success_count != len(rows) or report["scope_and_output"]["successful_repetitions"] != len(scope_rows) or report["forms"]["successful_repetitions"] != len(form_rows) or report["forms"]["naive_false_success_signals"] != len(rows) or report["playbook_replay"]["successful_repetitions"] != len(replay_rows) or report["background_tab_action"]["successful_repetitions"] != len(background_tab_rows):
        return 1
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        print(error, file=sys.stderr)
        raise SystemExit(1)
