#!/usr/bin/env python3
"""Compare agent-browser saved-flow write recovery with Scout host reconciliation.

This repeated local protocol probe records a successful flow, changes its
operation ID, and makes each write commit while its response is held open. It
grades both arms against the authoritative fixture ledger; its intervals do
not estimate customer reliability or integration cost.
"""

import argparse
import datetime as dt
import json
import math
import os
from pathlib import Path
import platform
import re
import signal
import subprocess
import time
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "examples" / "benchmark" / "results"


def command(argv, *, env=None, timeout=60):
    return subprocess.run(
        argv, cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT, timeout=timeout, check=False,
    )


def get_json(url):
    with urllib.request.urlopen(url, timeout=3) as response:
        return json.load(response)


def fixture_state(base, operation_id):
    return get_json(base + "/saved-flow-state?operation_id=" + operation_id)


def run_scout(binary, stage, task_id, base, store_path, output_dir, timeout=90):
    result = command(
        [str(binary), "--stage", stage, "--task-id", task_id, "--base-url", base, "--store", str(store_path)],
        timeout=timeout,
    )
    (output_dir / f"scout-{stage}-{task_id}.log").write_text(result.stdout, encoding="utf-8")
    payload = {}
    if result.stdout.strip():
        try:
            payload = json.loads(result.stdout.strip().splitlines()[-1])
        except json.JSONDecodeError:
            pass
    return result, payload


def wilson(successes, total, z=1.959963984540054):
    if total == 0:
        return None
    proportion = successes / total
    denominator = 1 + z * z / total
    center = (proportion + z * z / (2 * total)) / denominator
    margin = z * math.sqrt(proportion * (1 - proportion) / total + z * z / (4 * total * total)) / denominator
    return [max(0.0, center - margin), min(1.0, center + margin)]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", default="0.42.0", help="pinned agent-browser npm version")
    parser.add_argument("--port", type=int, default=41742)
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--timeout-ms", type=int, default=2500)
    parser.add_argument("--repetitions", type=int, default=10)
    args = parser.parse_args()
    if not re.fullmatch(
        r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?",
        args.version,
    ):
        parser.error("--version must be an exact semantic version (for example 0.42.0)")
    if not 500 <= args.timeout_ms <= 30000:
        parser.error("--timeout-ms must be between 500 and 30000")
    if not 1 <= args.repetitions <= 30:
        parser.error("--repetitions must be between 1 and 30")

    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output = args.output_dir or RESULTS / f"agent-browser-{args.version}-saved-flow-recovery-{stamp}"
    output = output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    base = f"http://127.0.0.1:{args.port}"
    run_token = uuid.uuid4().hex[:12]
    flow_name = "scout_saved_flow_" + run_token
    namespace = "scout-flow-" + run_token
    session = "flow-" + run_token
    browser = ["npx", "--yes", f"agent-browser@{args.version}", "--namespace", namespace, "--session", session]
    server_binary = output / "fixtureserver"
    build = command(["go", "build", "-o", str(server_binary), "./examples/benchmark/fixtureserver"])
    (output / "fixture-build.log").write_text(build.stdout, encoding="utf-8")
    if build.returncode:
        raise SystemExit(f"fixture server build failed; see {output / 'fixture-build.log'}")
    scout_binary = output / "saved_flow_scout"
    scout_build = command(["go", "build", "-o", str(scout_binary), "./examples/benchmark/saved_flow_scout"])
    (output / "scout-build.log").write_text(scout_build.stdout, encoding="utf-8")
    if scout_build.returncode:
        raise SystemExit(f"Scout shared-fixture adapter build failed; see {output / 'scout-build.log'}")

    server_log = (output / "fixture-server.log").open("w", encoding="utf-8")
    server = subprocess.Popen(
        [str(server_binary), "-listen", f"127.0.0.1:{args.port}", "-process-recovery"],
        cwd=ROOT, stdout=server_log, stderr=subprocess.STDOUT, text=True,
    )
    scout_submit = None
    scout_submit_log = None
    try:
        for _ in range(100):
            if server.poll() is not None:
                raise SystemExit(f"fixture server exited; see {output / 'fixture-server.log'}")
            try:
                get_json(base + "/saved-flow-state?operation_id=seed")
                break
            except Exception:
                time.sleep(0.1)
        else:
            raise SystemExit("fixture server readiness timed out")

        seed_id = "flow-seed-" + run_token
        capture_url = base + "/saved-flow-task?operation_id=" + seed_id
        steps = command(browser + ["--capture", "flow", "open", capture_url])
        (output / "capture-open.log").write_text(steps.stdout, encoding="utf-8")
        if steps.returncode:
            raise SystemExit(f"agent-browser open failed; see {output / 'capture-open.log'}")
        # The shared fixture deliberately places the task in an iframe. Select
        # it explicitly so flow capture does not depend on implicit frame
        # traversal behavior in the CLI.
        frame = command(browser + ["frame", "#task-frame"])
        (output / "capture-frame.log").write_text(frame.stdout, encoding="utf-8")
        if frame.returncode:
            raise SystemExit(f"agent-browser frame selection failed; see {output / 'capture-frame.log'}")
        ready = command(browser + ["wait", "#submit"])
        (output / "capture-ready.log").write_text(ready.stdout, encoding="utf-8")
        if ready.returncode:
            raise SystemExit(f"agent-browser fixture readiness failed; see {output / 'capture-ready.log'}")
        click = command(browser + ["click", "#submit"])
        (output / "capture-click.log").write_text(click.stdout, encoding="utf-8")
        if click.returncode:
            raise SystemExit(f"agent-browser capture click failed; see {output / 'capture-click.log'}")
        save = command(browser + ["flow", "save", flow_name, "--param", "operation_id=" + seed_id])
        (output / "flow-save.log").write_text(save.stdout, encoding="utf-8")
        if save.returncode:
            raise SystemExit(f"agent-browser flow save failed; see {output / 'flow-save.log'}")

        flow_runs = []
        for index in range(1, args.repetitions + 1):
            operation_id = f"uncertain-agent-{run_token}-{index:03d}"
            started = time.monotonic()
            replay = command(
                browser + ["flow", "run", flow_name, "--param", f"operation_id={operation_id}",
                           "--allow-write", "all", "--timeout", str(args.timeout_ms)],
                timeout=max(15, (args.timeout_ms / 1000) + 12),
            )
            replay_ms = round((time.monotonic() - started) * 1000, 1)
            (output / f"flow-run-{index:03d}.log").write_text(replay.stdout, encoding="utf-8")
            ledger = fixture_state(base, operation_id)
            flow_unknown = "outcome_unknown" in replay.stdout
            flow_runs.append({
                "run": index,
                "operation_id": operation_id,
                "exit_code": replay.returncode,
                "elapsed_ms": replay_ms,
                "outcome_unknown": flow_unknown,
                "ledger": ledger,
                "no_resend_within_run": flow_unknown and ledger.get("committed") == 1 and ledger.get("requests") == 1,
                "output": replay.stdout.strip(),
            })
        # Closing the session releases any response that the flow left pending.
        close = command(browser + ["close", "--all"])
        (output / "browser-close.log").write_text(close.stdout, encoding="utf-8")

        # Both Scout and the saved flow use the same page and mutation/ledger
        # implementation. The normal Scout control checks the task page and
        # response postcondition before we introduce the fault boundary.
        store_path = output / "task-runs.db"
        scout_arm_started = time.monotonic()
        scout_control, scout_control_json = run_scout(
            scout_binary, "control", "scout-control-" + run_token, base, store_path, output,
        )
        if scout_control.returncode or scout_control_json.get("status") != "completed" or scout_control_json.get("ledger_count") != 1 or scout_control_json.get("ledger_requests") != 1:
            raise SystemExit("Scout no-fault control failed; see scout-control log")

        scout_runs = []
        for index in range(1, args.repetitions + 1):
            scout_task_id = f"uncertain-scout-{run_token}-{index:03d}"
            scout_started = time.monotonic()
            scout_submit_log = (output / f"scout-submit-{index:03d}.log").open("w", encoding="utf-8")
            scout_submit = subprocess.Popen(
                [str(scout_binary), "--stage", "submit", "--task-id", scout_task_id,
                 "--base-url", base, "--store", str(store_path)],
                cwd=ROOT, stdin=subprocess.DEVNULL, stdout=scout_submit_log,
                stderr=subprocess.STDOUT, text=True, start_new_session=True,
            )
            scout_deadline = time.monotonic() + 60
            while time.monotonic() < scout_deadline:
                if scout_submit.poll() is not None:
                    raise SystemExit(f"Scout submit worker {index} exited before the fixture commit")
                scout_state = fixture_state(base, scout_task_id)
                if scout_state.get("committed") == 1:
                    break
                time.sleep(0.05)
            else:
                os.killpg(scout_submit.pid, signal.SIGKILL)
                scout_submit.wait(timeout=10)
                raise SystemExit(f"Scout submit worker {index} did not commit before timeout")
            os.killpg(scout_submit.pid, signal.SIGKILL)
            scout_submit.wait(timeout=10)
            scout_submit_log.close()
            scout_interrupted_state = fixture_state(base, scout_task_id)
            if scout_interrupted_state.get("committed") != 1 or scout_interrupted_state.get("requests") != 1:
                raise SystemExit(f"Scout run {index} ledger did not show exactly one effect from one request")
            retry_check, retry_json = run_scout(
                scout_binary, "retry-check", scout_task_id, base, store_path, output,
            )
            if retry_check.returncode or retry_json.get("retry_blocked") is not True:
                raise SystemExit(f"Scout same-ID retry guard failed on run {index}")
            reconcile, reconcile_json = run_scout(
                scout_binary, "reconcile", scout_task_id, base, store_path, output,
            )
            if reconcile.returncode or reconcile_json.get("resolution") != "verified_complete" or reconcile_json.get("ledger_count") != 1:
                raise SystemExit(f"Scout host reconciliation failed on run {index}")
            scout_runs.append({
                "run": index,
                "operation_id": scout_task_id,
                "elapsed_ms": round((time.monotonic() - scout_started) * 1000, 1),
                "ledger": scout_interrupted_state,
                "same_id_retry": retry_json,
                "reconciliation": reconcile_json,
                "recovery_succeeded": retry_json.get("retry_blocked") is True and reconcile_json.get("resolution") == "verified_complete",
            })
        scout_ms = round((time.monotonic() - scout_arm_started) * 1000, 1)
        scout_revision = command(["git", "rev-parse", "HEAD"]).stdout.strip()
        worktree_dirty = bool(command(["git", "status", "--porcelain"]).stdout.strip())
        go_version = command(["go", "version"]).stdout.strip()
        node_version = command(["node", "--version"]).stdout.strip()

        report = {
            "schema_version": 1,
            "benchmark": "agent_browser_saved_flow_write_recovery_vs_scout_host_reconciliation",
            "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "environment": {
                "platform": platform.platform(),
                "scout_revision": scout_revision,
                "worktree_dirty": worktree_dirty,
                "go_version": go_version,
                "node_version": node_version,
            },
            "agent_browser_version": args.version,
            "flow_name": flow_name,
            "fixture": "examples/benchmark/fixture.HandlerWithProcessRecovery saved-flow routes",
            "shared_semantics": "Both arms use the same local fixture and non-idempotent mutation route. agent-browser records a UI click and uses the saved flow's direct HTTP tier for the faulted request; Scout dispatches a browser click. Both outcomes are graded against the same authoritative ledger implementation.",
            "agent_browser": {
                "flow_save_exit_code": save.returncode,
                "repetitions": len(flow_runs),
                "unknown_without_resend_successes": sum(row["no_resend_within_run"] for row in flow_runs),
                "success_rate": sum(row["no_resend_within_run"] for row in flow_runs) / len(flow_runs),
                "wilson_95_interval": wilson(sum(row["no_resend_within_run"] for row in flow_runs), len(flow_runs)),
                "runs": flow_runs,
            },
            "scout": {
                "exit_code": 0,
                "elapsed_ms": scout_ms,
                "no_fault_control": scout_control_json,
                "repetitions": len(scout_runs),
                "recovery_successes": sum(row["recovery_succeeded"] for row in scout_runs),
                "success_rate": sum(row["recovery_succeeded"] for row in scout_runs) / len(scout_runs),
                "wilson_95_interval": wilson(sum(row["recovery_succeeded"] for row in scout_runs), len(scout_runs)),
                "runs": scout_runs,
            },
            "paired_runs": [
                {"run": index + 1, "agent_browser_operation_id": flow_runs[index]["operation_id"],
                 "scout_operation_id": scout_runs[index]["operation_id"],
                 "agent_browser_pass": flow_runs[index]["no_resend_within_run"],
                 "scout_pass": scout_runs[index]["recovery_succeeded"]}
                for index in range(min(len(flow_runs), len(scout_runs)))
            ],
            "interpretation": "Both products use the same local fixture, non-idempotent mutation endpoint, and authoritative ledger. agent-browser reports the ambiguous write outcome without resending inside that flow run; its faulted write used the HTTP tier. Scout used a browser click, then demonstrated a durable same-ID retry guard and host resolution. Repeated runs estimate only this fixture's behavior, not customer reliability or full integration cost.",
        }
        (output / "summary.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2))
        print(f"Artifacts: {output}")
        if report["agent_browser"]["unknown_without_resend_successes"] != args.repetitions or report["scout"]["recovery_successes"] != args.repetitions:
            raise SystemExit(1)
    finally:
        if scout_submit is not None and scout_submit.poll() is None:
            os.killpg(scout_submit.pid, signal.SIGKILL)
            scout_submit.wait(timeout=10)
        if scout_submit_log is not None and not scout_submit_log.closed:
            scout_submit_log.close()
        if server.poll() is None:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait(timeout=10)
        server_log.close()
        if scout_submit_log is not None and not scout_submit_log.closed:
            scout_submit_log.close()
        server.terminate()
        try:
            server.wait(timeout=5)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait()
        server_log.close()


if __name__ == "__main__":
    main()
