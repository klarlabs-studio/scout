#!/usr/bin/env python3
"""Compare Browser Use CLI-MCP with durable host state across worker loss.

The supervisor owns the fixture and browser. A first host worker reads a
SQLite checkpoint and dispatches a non-idempotent write. After the application
ledger records the effect, the supervisor kills both that worker and its MCP
server. A second worker reloads the checkpoint, starts a fresh MCP server, and
resolves from the authoritative ledger without replay.
"""

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import platform
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request
import uuid

from run_browser_use_recovery import (
    ROOT, RESULTS, chromium_binary, free_port, get_json, start_mcp,
    stop_harness_daemon, stop_mcp,
)


def persist_checkpoint(db_path, operation_id, task_url, status, state=None):
    with sqlite3.connect(db_path, timeout=10) as db:
        db.execute("PRAGMA synchronous=FULL")
        db.execute("BEGIN IMMEDIATE")
        db.execute(
            "UPDATE operation_checkpoint SET status=?, state_json=?, updated_at=? WHERE operation_id=?",
            (status, json.dumps(state, sort_keys=True) if state is not None else None,
             dt.datetime.now(dt.timezone.utc).isoformat(), operation_id),
        )
        if db.total_changes != 1:
            raise RuntimeError(f"checkpoint not found for {operation_id}")
        db.commit()


def read_checkpoint(db_path, operation_id):
    with sqlite3.connect(db_path, timeout=10) as db:
        db.row_factory = sqlite3.Row
        row = db.execute(
            "SELECT operation_id, task_url, status, state_json, updated_at "
            "FROM operation_checkpoint WHERE operation_id=?", (operation_id,),
        ).fetchone()
    if row is None:
        raise RuntimeError(f"durable host checkpoint missing: {operation_id}")
    result = dict(row)
    result["state"] = json.loads(result.pop("state_json")) if result["state_json"] else None
    return result


def state_url(base, operation_id):
    return f"{base}/saved-flow-state?operation_id={operation_id}"


def worker(stage, args):
    checkpoint = read_checkpoint(args.host_store, args.operation_id)
    if checkpoint["task_url"] != args.task_url:
        raise RuntimeError("durable host checkpoint URL does not match dispatch request")
    mcp = log = client = None
    try:
        transcript = "browser-use-dispatch" if stage == "dispatch" else "browser-use-recovery"
        mcp, log, client = start_mcp(args.output_dir, dict(os.environ), transcript)
        (args.output_dir / f"{stage}-worker.json").write_text(json.dumps({
            "pid": os.getpid(), "mcp_pid": mcp.pid, "operation_id": checkpoint["operation_id"],
            "stage": stage, "loaded_checkpoint_status": checkpoint["status"],
        }, indent=2) + "\n", encoding="utf-8")
        client.initialize()
        tools = client.request("tools/list").get("tools", [])
        if not any(tool.get("name") == "browser_exec" for tool in tools):
            raise RuntimeError("browser_exec missing from fresh MCP server")

        if stage == "dispatch":
            client.tool("browser_exec", {"code": f'new_tab({checkpoint["task_url"]!r})\nprint(page_info())'})
            client.tool("browser_exec", {
                "code": "js(\"document.querySelector('#submit').click(); 'clicked'\")\nprint('dispatched')"
            })
            (args.output_dir / "dispatch-worker-ready.json").write_text(json.dumps({
                "pid": os.getpid(), "mcp_pid": mcp.pid, "operation_id": checkpoint["operation_id"],
                "status": "dispatched_waiting_for_supervisor_fault",
            }, indent=2) + "\n", encoding="utf-8")
            while True:
                time.sleep(1)

        if stage != "reconcile":
            raise ValueError(f"unknown worker stage {stage}")
        ledger = get_json(state_url(args.base_url, checkpoint["operation_id"]))
        browser_probe = client.tool("browser_exec", {"code": "print(page_info())"})
        browser_probe_text = "\n".join(
            item.get("text", "") for item in browser_probe.get("content", [])
            if item.get("type") == "text"
        )
        browser_session_reconnected = checkpoint["task_url"] in browser_probe_text
        if ledger.get("committed") == 1 and ledger.get("requests") == 1:
            resolution = "verified_complete"
        elif ledger.get("committed") == 0 and ledger.get("requests") == 1:
            resolution = "verified_not_committed"
        else:
            resolution = "unresolved"
        persisted_state = {
            "resolution": resolution,
            "evidence_source": "saved_flow_shared_fixture_ledger",
            "evidence_reference": checkpoint["operation_id"],
            "ledger": ledger,
            "observed_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "recovery_worker_pid": os.getpid(),
            "mcp_tool_names": [tool["name"] for tool in tools],
            "browser_session_reconnected": browser_session_reconnected,
            "browser_probe": browser_probe,
        }
        persist_checkpoint(args.host_store, checkpoint["operation_id"], checkpoint["task_url"], resolution, persisted_state)
        reloaded = read_checkpoint(args.host_store, checkpoint["operation_id"])
        if reloaded["status"] != resolution or reloaded["state"] != persisted_state:
            raise RuntimeError("host resolution did not persist across a fresh SQLite read")
        print(json.dumps({"checkpoint_before": checkpoint, "checkpoint_after": reloaded}, sort_keys=True))
        return 0 if resolution == "verified_complete" else 1
    finally:
        if mcp is not None:
            stop_mcp(mcp, log)
            if client is not None:
                client.transcript.close()


def run_worker(stage, argv):
    parser = argparse.ArgumentParser()
    parser.add_argument("--host-store", type=Path, required=True)
    parser.add_argument("--operation-id", required=True)
    parser.add_argument("--task-url", required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args(argv)
    return worker(stage, args)


def kill_group(pid, sig=signal.SIGKILL):
    try:
        os.killpg(pid, sig)
    except ProcessLookupError:
        pass


def main():
    if "--worker-stage" in sys.argv:
        marker = sys.argv.index("--worker-stage")
        return run_worker(sys.argv[marker + 1], sys.argv[marker + 2:])

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=41753)
    parser.add_argument("--output-dir", type=Path)
    args = parser.parse_args()

    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output = (args.output_dir or RESULTS / f"browser-use-1.0.1-host-restart-{stamp}").resolve()
    output.mkdir(parents=True, exist_ok=False)
    base = f"http://127.0.0.1:{args.port}"
    token = uuid.uuid4().hex[:12]
    operation_id = f"uncertain-browser-use-host-{token}"
    task_url = f"{base}/saved-flow-task?operation_id={operation_id}"
    host_store = output / "host-operation.sqlite3"

    version = subprocess.run(
        ["uvx", "--from", "browser-use==1.0.1", "browser-use", "--version"],
        cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        timeout=90, check=False,
    )
    (output / "browser-use-version.log").write_text(version.stdout, encoding="utf-8")
    if version.returncode or "browser-use 1.0.1 (browser-harness 0.1.13)" not in version.stdout:
        raise SystemExit(f"unexpected Browser Use runtime; see {output / 'browser-use-version.log'}")

    fixture_binary = output / "fixtureserver"
    build = subprocess.run(
        ["go", "build", "-o", str(fixture_binary), "./examples/benchmark/fixtureserver"],
        cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        timeout=90, check=False,
    )
    (output / "fixture-build.log").write_text(build.stdout, encoding="utf-8")
    if build.returncode:
        raise SystemExit(f"fixture build failed; see {output / 'fixture-build.log'}")

    fixture_log = (output / "fixture-server.log").open("w", encoding="utf-8")
    fixture = subprocess.Popen(
        [str(fixture_binary), "-listen", f"127.0.0.1:{args.port}", "-process-recovery"],
        cwd=ROOT, stdout=fixture_log, stderr=subprocess.STDOUT, text=True,
    )
    dispatch = recovery = None
    dispatch_log = recovery_log = None
    chrome = chrome_log = profile = None
    harness_runtime = harness_tmp = None
    try:
        for _ in range(100):
            if fixture.poll() is not None:
                raise RuntimeError(f"fixture server exited; see {output / 'fixture-server.log'}")
            try:
                get_json(state_url(base, "readiness"))
                break
            except Exception:
                time.sleep(0.1)
        else:
            raise TimeoutError("fixture server readiness timed out")

        env = {key: os.environ[key] for key in (
            "PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "USER", "LOGNAME",
        ) if key in os.environ}
        env.update({
            "ANONYMIZED_TELEMETRY": "false", "BH_TELEMETRY": "0",
            "PLAYWRIGHT_BROWSERS_PATH": "/tmp/scout-browser-use-browsers",
            "UV_CACHE_DIR": "/tmp/scout-browser-use-uv-cache",
            "BU_NAME": "scout-recovery-" + token,
        })
        harness_runtime = Path(tempfile.mkdtemp(prefix="bu-runtime-", dir="/tmp"))
        harness_tmp = Path(tempfile.mkdtemp(prefix="bu-tmp-", dir="/tmp"))
        env.update({
            "BH_RUNTIME_DIR": str(harness_runtime), "BH_TMP_DIR": str(harness_tmp),
            "BH_RUNTIME_DIR_SHARED": "0", "BH_TMP_DIR_SHARED": "0",
            "BH_HOME": str(harness_tmp / "home"), "BH_CONFIG_DIR": str(harness_tmp / "config"),
            "BH_AGENT_WORKSPACE": str(harness_tmp / "workspace"),
        })
        chrome_bin = chromium_binary(Path(env["PLAYWRIGHT_BROWSERS_PATH"]))
        chrome_port = free_port()
        profile = tempfile.TemporaryDirectory(prefix="scout-browser-use-host-chrome-")
        chrome_log = (output / "chromium.log").open("w", encoding="utf-8")
        chrome = subprocess.Popen(
            [str(chrome_bin), "--headless=new", "--no-sandbox", "--disable-gpu",
             "--remote-debugging-address=127.0.0.1", f"--remote-debugging-port={chrome_port}",
             f"--user-data-dir={profile.name}", "about:blank"],
            cwd=output, env=env, stdin=subprocess.DEVNULL, stdout=chrome_log,
            stderr=subprocess.STDOUT, text=True, start_new_session=True,
        )
        cdp_url = f"http://127.0.0.1:{chrome_port}"
        for _ in range(100):
            if chrome.poll() is not None:
                raise RuntimeError(f"temporary Chromium exited; see {output / 'chromium.log'}")
            try:
                get_json(cdp_url + "/json/version")
                break
            except Exception:
                time.sleep(0.1)
        else:
            raise TimeoutError("temporary Chromium DevTools endpoint did not become ready")
        env["BU_CDP_URL"] = cdp_url

        with sqlite3.connect(host_store) as db:
            db.execute("PRAGMA journal_mode=DELETE")
            db.execute("PRAGMA synchronous=FULL")
            db.execute("CREATE TABLE operation_checkpoint (operation_id TEXT PRIMARY KEY, task_url TEXT NOT NULL, status TEXT NOT NULL, state_json TEXT, updated_at TEXT NOT NULL)")
            db.execute("INSERT INTO operation_checkpoint VALUES (?, ?, 'prepared', NULL, ?)",
                       (operation_id, task_url, dt.datetime.now(dt.timezone.utc).isoformat()))
            db.commit()
        with host_store.open("rb") as db_file:
            os.fsync(db_file.fileno())
        checkpoint_before = read_checkpoint(host_store, operation_id)

        worker_args = ["--host-store", str(host_store), "--operation-id", operation_id,
                       "--task-url", task_url, "--base-url", base, "--output-dir", str(output)]
        dispatch_log = (output / "dispatch-worker.log").open("w", encoding="utf-8")
        dispatch = subprocess.Popen(
            [sys.executable, str(Path(__file__).resolve()), "--worker-stage", "dispatch", *worker_args],
            cwd=ROOT, env=env, stdin=subprocess.DEVNULL,
            stdout=dispatch_log, stderr=subprocess.STDOUT, text=True, start_new_session=True,
        )
        mcp_pid_file = output / "dispatch-worker.json"
        deadline = time.monotonic() + 90
        committed = get_json(state_url(base, operation_id))
        while committed.get("committed") != 1 and time.monotonic() < deadline:
            if dispatch.poll() is not None:
                raise RuntimeError(f"dispatch worker exited before commit; see {output / 'dispatch-worker.log'}")
            time.sleep(0.025)
            committed = get_json(state_url(base, operation_id))
        if committed.get("committed") != 1:
            raise TimeoutError("fixture mutation did not commit before worker-loss boundary")
        for _ in range(100):
            if mcp_pid_file.is_file():
                break
            time.sleep(0.01)
        else:
            raise TimeoutError("dispatch worker did not publish its MCP process ID")
        worker_state = json.loads(mcp_pid_file.read_text(encoding="utf-8"))
        kill_group(worker_state["mcp_pid"])
        kill_group(dispatch.pid)
        dispatch.wait(timeout=10)
        dispatch_log.close()
        post_kill = get_json(state_url(base, operation_id))

        recovery_log = (output / "recovery-worker.log").open("w", encoding="utf-8")
        recovery = subprocess.Popen(
            [sys.executable, str(Path(__file__).resolve()), "--worker-stage", "reconcile", *worker_args],
            cwd=ROOT, env=env, stdin=subprocess.DEVNULL,
            stdout=recovery_log, stderr=subprocess.STDOUT, text=True, start_new_session=True,
        )
        recovery_code = recovery.wait(timeout=120)
        recovery_log.close()
        if recovery_code != 0:
            raise RuntimeError(f"recovery worker failed ({recovery_code}); see {output / 'recovery-worker.log'}")

        checkpoint_after = read_checkpoint(host_store, operation_id)
        final_ledger = get_json(state_url(base, operation_id))
        dispatch_state = json.loads((output / "dispatch-worker.json").read_text(encoding="utf-8"))
        recovery_state = json.loads((output / "reconcile-worker.json").read_text(encoding="utf-8"))
        # The recovery worker's MCP identity and checkpoint load are written by
        # the parent from its durable SQLite record after it has exited.
        result = {
            "schema_version": 1,
            "benchmark": "browser_use_cli_mcp_host_process_restart_recovery",
            "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "environment": {"platform": platform.platform(), "python": platform.python_version()},
            "browser_use_version": version.stdout.strip(),
            "server_mode": "browser-use 1.0.1 --cli-mcp; isolated local Chromium via loopback BU_CDP_URL",
            "fixture": "examples/benchmark/fixture.HandlerWithProcessRecovery /saved-flow-task and /saved-flow-state",
            "fault_boundary": "dispatch host worker and its MCP server killed after application commit and before acknowledgment; a distinct recovery host worker reloads SQLite state",
            "operation_id": operation_id,
            "host_store": host_store.name,
            "checkpoint_before_dispatch": checkpoint_before,
            "checkpoint_after_recovery": checkpoint_after,
            "dispatch_worker_pid": dispatch.pid,
            "dispatch_worker_exit_code": dispatch.returncode,
            "dispatch_worker_killed": dispatch.returncode == -signal.SIGKILL,
            "dispatch_mcp_pid": worker_state["mcp_pid"],
            "recovery_worker_pid": recovery.pid,
            "recovery_worker_distinct": recovery.pid != dispatch.pid,
            "recovery_mcp_pid": recovery_state["mcp_pid"],
            "recovery_worker_exit_code": recovery_code,
            "fixture_after_dispatch_before_kill": committed,
            "fixture_after_worker_and_mcp_kill": post_kill,
            "fixture_after_host_recovery": final_ledger,
            "resolution": checkpoint_after["state"],
            "browser_session_reconnected_after_restart": checkpoint_after["state"]["browser_session_reconnected"],
            "no_duplicate_effect": final_ledger.get("committed") == 1 and final_ledger.get("requests") == 1,
            "success": checkpoint_before["status"] == "prepared" and checkpoint_after["status"] == "verified_complete" and dispatch.returncode == -signal.SIGKILL and recovery_code == 0 and final_ledger.get("committed") == 1 and final_ledger.get("requests") == 1,
            "interpretation": "A durable host-owned SQLite checkpoint survived dispatch-worker loss. A different process reloaded the operation ID, initialized a fresh Browser Use MCP server, queried application truth, and persisted verified completion without replay. This is a caller-authored recovery adapter; it does not establish native Browser Use task persistence or compare integration effort against Scout.",
            "artifacts": ["host-operation.sqlite3", "dispatch-worker.json", "recovery-worker.log", "browser-use-dispatch.jsonl", "browser-use-recovery.jsonl"],
        }
        (output / "summary.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(result, indent=2))
        print(f"Artifacts: {output}")
        return 0 if result["success"] else 1
    finally:
        if dispatch is not None and dispatch.poll() is None:
            kill_group(dispatch.pid)
            dispatch.wait(timeout=10)
        if recovery is not None and recovery.poll() is None:
            kill_group(recovery.pid)
            recovery.wait(timeout=10)
        for handle in [dispatch_log, recovery_log]:
            if handle is not None and not handle.closed:
                handle.close()
        if harness_runtime is not None:
            stop_harness_daemon(harness_runtime)
        if chrome is not None and chrome.poll() is None:
            try:
                kill_group(chrome.pid, signal.SIGTERM)
                chrome.wait(timeout=10)
            except subprocess.TimeoutExpired:
                kill_group(chrome.pid)
                chrome.wait(timeout=10)
        if chrome_log is not None:
            chrome_log.close()
        if profile is not None:
            profile.cleanup()
        if harness_runtime is not None:
            import shutil
            shutil.rmtree(harness_runtime, ignore_errors=True)
        if harness_tmp is not None:
            import shutil
            shutil.rmtree(harness_tmp, ignore_errors=True)
        if fixture.poll() is None:
            fixture.terminate()
            try:
                fixture.wait(timeout=10)
            except subprocess.TimeoutExpired:
                fixture.kill()
                fixture.wait(timeout=10)
        fixture_log.close()


if __name__ == "__main__":
    raise SystemExit(main())
