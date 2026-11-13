#!/usr/bin/env python3
"""Probe Browser Use 1.0.1 local MCP after a committed, unacknowledged write.

This is an integration probe, not a product reliability estimate. It uses the
same non-idempotent fixture route and authoritative ledger as Scout and the
agent-browser recovery comparisons.
"""

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import platform
import select
import signal
import socket
import subprocess
import tempfile
import time
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "examples" / "benchmark" / "results"


def get_json(url):
    with urllib.request.urlopen(url, timeout=3) as response:
        return json.load(response)


class MCP:
    def __init__(self, child, transcript):
        self.child = child
        self.transcript = transcript
        self.next_id = 1

    def request(self, method, params=None, timeout=90):
        request_id = self.next_id
        self.next_id += 1
        message = {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params or {}}
        self.transcript.write(json.dumps({"direction": "client", "message": message}) + "\n")
        self.transcript.flush()
        self.child.stdin.write(json.dumps(message) + "\n")
        self.child.stdin.flush()
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            ready, _, _ = select.select([self.child.stdout], [], [], max(0, deadline - time.monotonic()))
            if not ready:
                raise TimeoutError(f"timed out waiting for {method}")
            line = self.child.stdout.readline()
            if not line:
                raise RuntimeError("Browser Use MCP exited before replying")
            response = json.loads(line)
            self.transcript.write(json.dumps({"direction": "server", "message": response}) + "\n")
            self.transcript.flush()
            if response.get("id") == request_id:
                if "error" in response:
                    raise RuntimeError(json.dumps(response["error"]))
                return response["result"]
        raise TimeoutError(f"timed out waiting for {method}")

    def initialize(self):
        result = self.request("initialize", {
            "protocolVersion": "2025-03-26", "capabilities": {},
            "clientInfo": {"name": "scout-browser-use-recovery", "version": "1"},
        })
        self.child.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
        self.child.stdin.flush()
        return result

    def tool(self, name, arguments, timeout=90):
        result = self.request("tools/call", {"name": name, "arguments": arguments}, timeout)
        if result.get("isError"):
            raise RuntimeError(f"{name}: {result.get('content')}")
        return result


def start_mcp(output, env, transcript_name):
    log = (output / (transcript_name + ".stderr.log")).open("w", encoding="utf-8")
    child = subprocess.Popen(
        ["uvx", "--from", "browser-use==1.0.1", "browser-use", "--cli-mcp"],
        cwd=output, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=log, text=True, bufsize=1, start_new_session=True,
    )
    return child, log, MCP(child, (output / (transcript_name + ".jsonl")).open("w", encoding="utf-8"))


def stop_mcp(child, log):
    if child.poll() is None:
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        child.wait(timeout=10)
    log.close()


def stop_harness_daemon(runtime_dir):
    for pid_file in runtime_dir.glob("bu*.pid"):
        try:
            pid = int(pid_file.read_text(encoding="utf-8").strip())
        except (OSError, ValueError):
            continue
        command = subprocess.run(
            ["ps", "-p", str(pid), "-o", "command="], text=True,
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False,
        ).stdout.strip()
        if "browser_harness.daemon" not in command:
            continue
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            continue
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if subprocess.run(["ps", "-p", str(pid), "-o", "pid="],
                              stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                              text=True, check=False).stdout.strip() == "":
                break
            time.sleep(0.05)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def chromium_binary(browsers_path):
    candidates = [
        *browsers_path.glob("chromium-*/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"),
        *browsers_path.glob("chromium-*/chrome-mac/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"),
        *browsers_path.glob("chromium-*/chrome-linux/chrome"),
        *browsers_path.glob("chromium-*/chrome-linux64/chrome"),
        *browsers_path.glob("chromium-*/chrome-win/chrome.exe"),
    ]
    for candidate in candidates:
        if candidate.is_file():
            return candidate
    raise FileNotFoundError(f"no Playwright Chromium found under {browsers_path}; run browser-use install")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=41752)
    parser.add_argument("--output-dir", type=Path)
    args = parser.parse_args()

    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    output = (args.output_dir or RESULTS / f"browser-use-1.0.1-local-mcp-recovery-{stamp}").resolve()
    output.mkdir(parents=True, exist_ok=False)
    base = f"http://127.0.0.1:{args.port}"
    token = uuid.uuid4().hex[:12]
    operation_id = f"uncertain-browser-use-{token}"
    task_url = f"{base}/saved-flow-task?operation_id={operation_id}"

    version_check = subprocess.run(
        ["uvx", "--from", "browser-use==1.0.1", "browser-use", "--version"],
        cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        timeout=90, check=False,
    )
    (output / "browser-use-version.log").write_text(version_check.stdout, encoding="utf-8")
    if version_check.returncode or "browser-use 1.0.1 (browser-harness 0.1.13)" not in version_check.stdout:
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
    child = log = None
    restarted = restart_log = None
    chrome = chrome_log = None
    profile = None
    harness_runtime = None
    harness_tmp = None
    phase = "setup"
    try:
        for _ in range(100):
            if fixture.poll() is not None:
                raise RuntimeError(f"fixture server exited; see {output / 'fixture-server.log'}")
            try:
                get_json(base + "/saved-flow-state?operation_id=readiness")
                break
            except Exception:
                time.sleep(0.1)
        else:
            raise TimeoutError("fixture server readiness timed out")

        env = {key: os.environ[key] for key in (
            "PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "USER", "LOGNAME",
        ) if key in os.environ}
        env.update({
            "ANONYMIZED_TELEMETRY": "false",
            "PLAYWRIGHT_BROWSERS_PATH": "/tmp/scout-browser-use-browsers",
            "UV_CACHE_DIR": "/tmp/scout-browser-use-uv-cache",
            "BU_NAME": "scout-recovery-" + token,
            "BH_TELEMETRY": "0",
        })
        harness_runtime = Path(tempfile.mkdtemp(prefix="bu-runtime-", dir="/tmp"))
        harness_tmp = Path(tempfile.mkdtemp(prefix="bu-tmp-", dir="/tmp"))
        env.update({
            "BH_RUNTIME_DIR": str(harness_runtime),
            "BH_TMP_DIR": str(harness_tmp),
            "BH_RUNTIME_DIR_SHARED": "0",
            "BH_TMP_DIR_SHARED": "0",
            "BH_HOME": str(harness_tmp / "home"),
            "BH_CONFIG_DIR": str(harness_tmp / "config"),
            "BH_AGENT_WORKSPACE": str(harness_tmp / "workspace"),
        })
        browser_path = Path(env["PLAYWRIGHT_BROWSERS_PATH"])
        chrome_bin = chromium_binary(browser_path)
        chrome_port = free_port()
        profile = tempfile.TemporaryDirectory(prefix="scout-browser-use-chrome-")
        chrome_log = (output / "chromium.log").open("w", encoding="utf-8")
        chrome = subprocess.Popen(
            [str(chrome_bin), "--headless=new", "--no-sandbox", "--disable-gpu",
             "--remote-debugging-address=127.0.0.1", f"--remote-debugging-port={chrome_port}",
             f"--user-data-dir={profile.name}", "about:blank"],
            cwd=ROOT, env=env, stdin=subprocess.DEVNULL, stdout=chrome_log,
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
        child, log, client = start_mcp(output, env, "browser-use-first")
        started = time.monotonic()
        phase = "mcp_initialize"
        init = client.initialize()
        phase = "list_tools"
        toolset = client.request("tools/list").get("tools", [])
        tool_names = [tool["name"] for tool in toolset]
        if "browser_exec" not in tool_names:
            raise RuntimeError(f"browser_exec missing from tool list: {tool_names}")
        phase = "navigate_to_fixture"
        nav = client.tool("browser_exec", {"code": f'new_tab({task_url!r})\nprint(page_info())'})
        phase = "dispatch_mutation"
        dispatch = client.tool("browser_exec", {
            "code": "js(\"document.querySelector('#submit').click(); 'clicked'\")\nprint('dispatched')"
        })

        deadline = time.monotonic() + 30
        committed = get_json(base + "/saved-flow-state?operation_id=" + operation_id)
        while committed.get("committed") != 1 and time.monotonic() < deadline:
            time.sleep(0.025)
            committed = get_json(base + "/saved-flow-state?operation_id=" + operation_id)
        if committed.get("committed") != 1:
            raise TimeoutError("fixture mutation did not commit")

        # Simulate losing the browser-control process after the application
        # committed but before the page received its HTTP response.
        stop_mcp(child, log)
        child = log = None
        after_kill = get_json(base + "/saved-flow-state?operation_id=" + operation_id)
        restarted, restart_log, client2 = start_mcp(output, env, "browser-use-restarted")
        restart_init = client2.initialize()
        restart_toolset = client2.request("tools/list").get("tools", [])
        # The host reconciles using application-owned truth. It does not
        # dispatch a second browser action after the uncertain acknowledgment.
        resolution = get_json(base + "/saved-flow-state?operation_id=" + operation_id)
        result = {
            "schema_version": 1,
            "benchmark": "browser_use_local_mcp_committed_unacknowledged_recovery",
            "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "environment": {"platform": platform.platform(), "python": platform.python_version()},
            "browser_use_version_output": version_check.stdout.strip(),
            "browser_use_version": "1.0.1",
            "browser_harness_version": "0.1.13",
            "server_mode": "browser-use --cli-mcp with explicit loopback BU_CDP_URL to isolated local Chromium",
            "browser_binary": str(chrome_bin),
            "fixture": "examples/benchmark/fixture.HandlerWithProcessRecovery /saved-flow-task and /saved-flow-state",
            "operation_id": operation_id,
            "tool_names": tool_names,
            "restart_tool_names": [tool["name"] for tool in restart_toolset],
            "initialization_server_name": init.get("serverInfo", {}).get("name"),
            "restart_initialization_server_name": restart_init.get("serverInfo", {}).get("name"),
            "tool_calls_before_fault": 2,
            "navigation_result": nav,
            "dispatch_result": dispatch,
            "fixture_after_commit_before_mcp_kill": committed,
            "fixture_after_mcp_kill": after_kill,
            "host_resolution_after_mcp_restart": resolution,
            "mcp_process_restarted": restarted.poll() is None,
            "no_replay_after_restart": resolution.get("requests") == 1 and resolution.get("committed") == 1,
            "elapsed_ms_through_resolution": round((time.monotonic() - started) * 1000, 1),
            "success": committed.get("committed") == 1 and after_kill.get("requests") == 1 and resolution.get("requests") == 1 and resolution.get("committed") == 1,
            "interpretation": "The MCP process was killed after the fixture committed its non-idempotent write and before acknowledgment. A fresh Browser Use MCP process initialized successfully; the host read the authoritative fixture ledger and did not replay. This does not establish native Browser Use task persistence, host-process restart recovery, browser restoration, or a reliability rate.",
            "raw_transcripts": ["browser-use-first.jsonl", "browser-use-restarted.jsonl"],
        }
        (output / "summary.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(result, indent=2))
        print(f"Artifacts: {output}")
        if not result["success"]:
            raise SystemExit(1)
    except Exception as error:
        failure = {
            "schema_version": 1,
            "benchmark": "browser_use_local_mcp_committed_unacknowledged_recovery",
            "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "browser_use_version": version_check.stdout.strip() if "version_check" in locals() else "unknown",
            "server_mode": "browser-use --cli-mcp with explicit loopback BU_CDP_URL to isolated local Chromium",
            "operation_id": operation_id,
            "phase": phase,
            "error": f"{type(error).__name__}: {error}",
            "diagnostics": ["browser-use-first.jsonl", "browser-use-first.stderr.log", "chromium.log"],
            "success": False,
        }
        (output / "failure.json").write_text(json.dumps(failure, indent=2) + "\n", encoding="utf-8")
        raise
    finally:
        if child is not None:
            stop_mcp(child, log)
        if restarted is not None:
            stop_mcp(restarted, restart_log)
        if harness_runtime is not None:
            stop_harness_daemon(harness_runtime)
        if chrome is not None and chrome.poll() is None:
            try:
                os.killpg(chrome.pid, signal.SIGTERM)
                chrome.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(chrome.pid, signal.SIGKILL)
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
    main()
