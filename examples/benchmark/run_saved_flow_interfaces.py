#!/usr/bin/env python3
"""Exercise saved-flow recovery through Scout's Go SDK, CLI, and MCP stdio.

The fixture commits a non-idempotent mutation and holds the response. The
runner kills each Scout worker after the commit, leaves Chrome alive over CDP,
then starts a replacement process to inspect and reconcile the durable record.
This is an interface conformance smoke, not a customer reliability estimate.
"""

import argparse
import datetime as dt
import json
import math
import os
from pathlib import Path
import platform
import queue
import shutil
import socket
import subprocess
import threading
import time
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def get_json(url, timeout=2):
    with urllib.request.urlopen(url, timeout=timeout) as response:
        return json.load(response)


class JsonProcess:
    def __init__(self, argv, env, *, mcp=False, output_dir):
        self.argv = argv
        self.mcp = mcp
        self.responses = queue.Queue()
        self.lines = []
        self.proc = subprocess.Popen(
            argv, cwd=ROOT, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, text=True, bufsize=1, start_new_session=True,
        )
        self.log_path = output_dir / (("mcp" if mcp else "cli") + "-" + uuid.uuid4().hex[:8] + ".log")
        self.reader = threading.Thread(target=self._read_output, daemon=True)
        self.reader.start()
        self.next_id = 1
        if mcp:
            response = self._send({
                "jsonrpc": "2.0", "id": self.next_id, "method": "initialize",
                "params": {
                    "protocolVersion": "2025-03-26", "capabilities": {},
                    "clientInfo": {"name": "scout-saved-flow-interface-probe", "version": "1"},
                },
            })
            if "result" not in response:
                raise RuntimeError(f"MCP initialize failed: {response}")
            self.next_id += 1
            self.proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
            self.proc.stdin.flush()

    def _read_output(self):
        assert self.proc.stdout is not None
        for line in self.proc.stdout:
            self.lines.append(line)
            try:
                payload = json.loads(line)
            except json.JSONDecodeError:
                continue
            self.responses.put(payload)
        self.responses.put(None)

    def _send(self, payload, timeout=20):
        assert self.proc.stdin is not None
        self.proc.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
        self.proc.stdin.flush()
        deadline = time.monotonic() + timeout
        wanted = payload.get("id")
        while time.monotonic() < deadline:
            try:
                item = self.responses.get(timeout=max(0.01, deadline - time.monotonic()))
            except queue.Empty as exc:
                raise TimeoutError(f"timed out waiting for response to {payload.get('method') or payload.get('command')}") from exc
            if item is None:
                raise RuntimeError(f"process exited before responding: {''.join(self.lines[-30:])}")
            if item.get("id") == wanted:
                return item
        raise TimeoutError(f"timed out waiting for response id {wanted}")

    def call(self, method, params, timeout=30):
        request_id = self.next_id
        self.next_id += 1
        response = self._send({
            "jsonrpc": "2.0", "id": request_id, "method": method, "params": params,
        }, timeout=timeout)
        if response.get("error"):
            raise RuntimeError(f"{method} failed: {response['error']}")
        return response.get("result", {})

    def cli(self, command, **params):
        request_id = self.next_id
        self.next_id += 1
        response = self._send({"id": request_id, "command": command, **params}, timeout=30)
        if not response.get("ok"):
            raise RuntimeError(f"CLI {command} failed: {response.get('error')}")
        return response.get("result")

    def tool(self, name, arguments, timeout=30):
        response = self.call("tools/call", {"name": name, "arguments": arguments}, timeout=timeout)
        if response.get("isError"):
            raise RuntimeError(f"MCP {name} returned an error: {response}")
        if isinstance(response.get("structuredContent"), dict):
            return response["structuredContent"]
        for content in response.get("content", []):
            if content.get("type") == "text":
                try:
                    value = json.loads(content.get("text", ""))
                except json.JSONDecodeError:
                    continue
                if isinstance(value, dict):
                    return value
        return response

    def stop(self, *, kill=False):
        if self.proc.poll() is None:
            if kill:
                self.proc.kill()
            else:
                self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait(timeout=5)
        if self.proc.stdin:
            self.proc.stdin.close()
        if self.proc.stdout:
            self.proc.stdout.close()
        self.log_path.write_text("".join(self.lines), encoding="utf-8")
        return self.proc.returncode


def wait_for_commit(base, operation_id, timeout=45):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = get_json(f"{base}/saved-flow-state?operation_id={operation_id}")
            if last.get("committed") == 1 and last.get("requests") == 1:
                return last
        except Exception:
            pass
        time.sleep(0.05)
    raise TimeoutError(f"fixture did not record one effect for {operation_id}; last state={last}")


def record_from_recovery(result):
    record = result.get("record") if isinstance(result, dict) else None
    if not isinstance(record, dict):
        raise RuntimeError(f"recovery response did not include a task record: {result}")
    fingerprint = record.get("fingerprint") or record.get("plan_fingerprint")
    if not fingerprint:
        raise RuntimeError(f"recovery record had no plan fingerprint: {record}")
    return record, fingerprint


def verify_receipt(worker_binary, receipt_path, task_id):
    go_validation = subprocess.run([
        str(worker_binary), "verify", f"--receipt={receipt_path}",
    ], cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if go_validation.returncode:
        raise RuntimeError(f"receipt failed strict Go decoder validation: {go_validation.stdout}")
    try:
        go_result = json.loads(go_validation.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Go decoder returned invalid JSON: {go_validation.stdout!r}") from exc
    if go_result.get("valid") is not True or go_result.get("task_id") != task_id:
        raise RuntimeError(f"receipt failed strict Go decoder identity validation: {go_validation.stdout}")

    python_validation = subprocess.run([
        "python3", str(ROOT / "examples/benchmark/verify_recovery_receipt.py"), str(receipt_path),
    ], cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if python_validation.returncode:
        raise RuntimeError(f"receipt failed independent Python verification: {python_validation.stdout}")
    try:
        python_result = json.loads(python_validation.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Python verifier returned invalid JSON: {python_validation.stdout!r}") from exc
    if python_result.get("valid") is not True or python_result.get("task_id") != task_id:
        raise RuntimeError(f"receipt failed independent Python identity validation: {python_validation.stdout}")
    return {"go_decoder_valid": go_result["valid"], "python_verifier_valid": python_result["valid"]}


def completed_actions():
    return [
        {"action": "click", "selector": "#step-one-button", "expected_text": "First step complete"},
        {"action": "click", "selector": "#submit", "expected_text": "Acknowledged"},
    ]


def compare_live_receipts(results, output):
    namespaces = {
        "go": "Go SDK / isolated task store",
        "cli": "stateful CLI / isolated task store",
        "mcp": "MCP stdio / isolated task store",
    }
    cases = []
    for repetition in sorted({item["repetition"] for item in results}):
        by_surface = {item["surface"]: item for item in results if item["repetition"] == repetition}
        interrupted = {surface: str((output / by_surface[surface]["receipt_path"]).resolve()) for surface in ("go", "cli", "mcp")}
        cases.append({
            "name": f"live-interrupted-repetition-{repetition}", "scenario": "interrupted",
            "frame_id_comparison": "presence", "receipts": interrupted, "namespaces": namespaces,
        })
        for ordinal in (1, 2):
            receipts = {}
            for surface in ("go", "cli", "mcp"):
                completed = by_surface[surface]["completed_task"]
                receipt = next(item for item in completed["receipts"] if item["ordinal"] == ordinal)
                receipts[surface] = str((output / receipt["receipt_path"]).resolve())
            cases.append({
                "name": f"live-completed-operation-{ordinal}-repetition-{repetition}", "scenario": "completed",
                "frame_id_comparison": "presence", "receipts": receipts, "namespaces": namespaces,
            })
    manifest_path = output / "live-receipt-parity-manifest.json"
    manifest_path.write_text(json.dumps({"required_scenarios": ["interrupted", "completed"], "cases": cases}, indent=2) + "\n", encoding="utf-8")
    comparison = subprocess.run([
        "go", "run", "./examples/benchmark/recovery_receipt_parity",
        "-manifest", str(manifest_path),
    ], cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    (output / "live-receipt-parity.log").write_text(comparison.stderr, encoding="utf-8")
    try:
        report = json.loads(comparison.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"live receipt parity checker returned invalid JSON: {comparison.stdout!r} {comparison.stderr!r}") from exc
    (output / "live-receipt-parity.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    if comparison.returncode or report.get("passed") is not True:
        raise RuntimeError(f"live receipt parity failed; see {output / 'live-receipt-parity.json'}")
    return report


def export_completed_cli_mcp(surface, process, go_worker_binary, base, output, token, repetition):
    task_id = f"completed-{repetition}-{token}"
    fixture_operation_id = f"completed-{surface}-{repetition}-{token}"
    page_url = f"{base}/saved-flow-task?operation_id={fixture_operation_id}"
    if surface == "cli":
        process.cli("navigate", url=page_url)
        process.cli("switch_frame", frame="#task-frame")
        task_result = process.cli("task_run", task_id=task_id, timeout_ms=300000, actions=completed_actions())
        recovered = process.cli("recover_task_run", task_id=task_id)
    else:
        process.tool("navigate", {"url": page_url})
        process.tool("switch_to_frame", {"selector": "#task-frame"})
        task_result = process.tool("task_run", {"id": task_id, "timeout_ms": 300000, "actions": completed_actions()}, timeout=300)
        recovered = process.tool("recover_task_run", {"id": task_id})
    if not isinstance(task_result, dict) or task_result.get("status") != "completed":
        raise RuntimeError(f"{surface} two-step task did not complete: {task_result}")
    record, fingerprint = record_from_recovery(recovered)
    if record.get("in_flight") or (record.get("result") or {}).get("status") != "completed":
        raise RuntimeError(f"{surface} did not retain the completed two-step task: {record}")
    plan = record.get("plan") or {}
    receipts = []
    for ordinal in (1, 2):
        if surface == "cli":
            receipt = process.cli("export_task_recovery_receipt", task_id=task_id, operation_ordinal=ordinal)
        else:
            receipt = process.tool("export_task_recovery_receipt", {"id": task_id, "operation_ordinal": ordinal})
        operation = receipt.get("operation") or {}
        if (receipt.get("task", {}).get("state") != "completed"
                or receipt.get("task", {}).get("id") != task_id
                or operation.get("ordinal") != ordinal
                or operation.get("outcome") != "verified"
                or operation.get("verification") != "verified"
                or receipt.get("recovery", {}).get("replay_authorized") is not False):
            raise RuntimeError(f"{surface} completed operation {ordinal} receipt is invalid: {receipt}")
        path = output / surface / f"{task_id}.operation-{ordinal}.recovery-receipt.json"
        path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        validation = verify_receipt(go_worker_binary, path, task_id)
        receipts.append({
            "ordinal": ordinal, "action": operation.get("name"), "outcome": operation.get("outcome"),
            "verification": operation.get("verification"), "receipt_path": str(path.relative_to(output)),
            **validation,
        })
    return {
        "task_id": task_id, "fingerprint": fingerprint,
        "policy": {key: plan.get(key) for key in ("policy_id", "policy_enforcement", "allowed_origins", "allow_private_ips")},
        "status": record["result"]["status"], "receipts": receipts,
    }


def run_surface(surface, binary, go_worker_binary, remote_cdp, base, output, token, repetition):
    task_id = f"uncertain-{repetition}-{token}"
    fixture_operation_id = f"uncertain-{surface}-{repetition}-{token}"
    store = output / f"{surface}-{repetition}-task-runs.db"
    env = os.environ.copy()
    env["SCOUT_REMOTE_CDP"] = remote_cdp
    env["SCOUT_MCP_ALLOWED_ORIGINS"] = base
    env["SCOUT_ALLOW_PRIVATE_IPS"] = "true"
    if surface == "cli":
        argv = [str(binary), "session", "--headless", "--allow-private-ips", f"--allowed-origins={base}", f"--task-store={store}"]
    else:
        argv = [str(binary), "mcp", "serve", f"--task-store={store}"]

    first = JsonProcess(argv, env, mcp=surface == "mcp", output_dir=output / surface)
    try:
        page_url = f"{base}/saved-flow-task?operation_id={fixture_operation_id}"
        if surface == "cli":
            first.cli("navigate", url=page_url)
            first.cli("switch_frame", frame="#task-frame")
            def invoke_pending():
                try:
                    first.cli(
                        "task_run", task_id=task_id, timeout_ms=300000,
                        actions=[{"action": "click", "selector": "#submit", "expected_text": "Acknowledged"}],
                    )
                except Exception as exc:
                    pending_error.append(str(exc))
            pending_error = []
            pending = threading.Thread(target=invoke_pending, daemon=True)
        else:
            first.tool("navigate", {"url": page_url})
            first.tool("switch_to_frame", {"selector": "#task-frame"})
            def invoke_pending():
                try:
                    first.tool(
                        "task_run", {"id": task_id, "timeout_ms": 300000,
                                     "actions": [{"action": "click", "selector": "#submit", "expected_text": "Acknowledged"}]},
                        timeout=300,
                    )
                except Exception as exc:
                    pending_error.append(str(exc))
            pending_error = []
            pending = threading.Thread(target=invoke_pending, daemon=True)
        pending.start()
        ledger = wait_for_commit(base, fixture_operation_id)
        # SIGKILL models process loss, so deferred browser/store cleanup does not run.
        first.stop(kill=True)
        pending.join(timeout=5)
        if pending.is_alive() or not pending_error:
            raise RuntimeError(f"{surface} task call was not observed to fail with its worker process")
    except Exception:
        first.stop(kill=True)
        raise

    replacement = JsonProcess(argv, env, mcp=surface == "mcp", output_dir=output / surface)
    try:
        if surface == "cli":
            recovered = replacement.cli("recover_task_run", task_id=task_id)
        else:
            recovered = replacement.tool("recover_task_run", {"id": task_id})
        record, fingerprint = record_from_recovery(recovered)
        plan = record.get("plan") or {}
        plan_policy = {key: plan.get(key) for key in ("policy_id", "policy_enforcement", "allowed_origins", "allow_private_ips")}
        if not record.get("in_flight", False):
            raise RuntimeError(f"recovered task was not retained as in-flight: {record}")

        # Export the real interrupted action receipt from the replacement
        # process. This verifies the durable cursor and interface contract at
        # the same process-loss boundary, rather than exporting a seeded row.
        if surface == "cli":
            receipt = replacement.cli("export_task_recovery_receipt", task_id=task_id, operation_ordinal=1)
        else:
            receipt = replacement.tool("export_task_recovery_receipt", {"id": task_id, "operation_ordinal": 1})
        operation = receipt.get("operation") if isinstance(receipt, dict) else None
        recovery = receipt.get("recovery") if isinstance(receipt, dict) else None
        if not isinstance(operation, dict) or not isinstance(recovery, dict):
            raise RuntimeError(f"{surface} interrupted receipt has an unexpected shape: {receipt}")
        if (receipt.get("task", {}).get("id") != task_id
                or operation.get("ordinal") != 1
                or operation.get("outcome") != "unknown"
                or operation.get("dispatch") != "may_have_dispatched"
                or recovery.get("replay_authorized") is not False):
            raise RuntimeError(f"{surface} interrupted receipt violated identity/uncertainty/replay invariants: {receipt}")
        receipt_path = output / surface / f"{task_id}.recovery-receipt.json"
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        receipt_validation = verify_receipt(go_worker_binary, receipt_path, task_id)

        # A same-ID retry must remain blocked after restart and before host resolution.
        retry_blocked = False
        retry_error = ""
        try:
            if surface == "cli":
                replacement.cli("task_run", task_id=task_id, timeout_ms=300000,
                                actions=[{"action": "click", "selector": "#submit", "expected_text": "Acknowledged"}])
            else:
                replacement.tool("task_run", {"id": task_id, "timeout_ms": 300000,
                                    "actions": [{"action": "click", "selector": "#submit", "expected_text": "Acknowledged"}]}, timeout=10)
        except Exception as exc:
            retry_error = str(exc)
            retry_blocked = "SCOUT_TASK_ID_CONFLICT" in retry_error or "cannot be reused" in retry_error
        if not retry_blocked or "different plan" in retry_error:
            raise RuntimeError(f"{surface} did not prove the same-ID replay guard; error={retry_error!r}")

        observed_at = dt.datetime.now(dt.timezone.utc).isoformat()
        evidence = "fixture ledger reports exactly one committed effect and one request after worker loss"
        if surface == "cli":
            resolution = replacement.cli(
                "resolve_task_run", task_id=task_id, plan_fingerprint=fingerprint,
                resolution_status="verified_complete", resolution_evidence=evidence,
                resolution_evidence_source="saved_flow_fixture_ledger",
                resolution_evidence_reference=task_id, resolution_observed_at=observed_at,
            )
        else:
            resolution = replacement.tool("resolve_task_run", {
                "id": task_id, "plan_fingerprint": fingerprint, "status": "verified_complete",
                "evidence": evidence, "evidence_source": "saved_flow_fixture_ledger",
                "evidence_reference": task_id, "observed_at": observed_at,
            })
        final_state = get_json(f"{base}/saved-flow-state?operation_id={fixture_operation_id}")
        if final_state.get("committed") != 1 or final_state.get("requests") != 1:
            raise RuntimeError(f"effect ledger changed during recovery: {final_state}")
        completed = export_completed_cli_mcp(surface, replacement, go_worker_binary, base, output, token, repetition)
        return {
            "surface": surface,
            "repetition": repetition,
            "task_id": task_id,
            "fixture_operation_id": fixture_operation_id,
            "worker_exit_after_commit": True,
            "ledger_at_interruption": ledger,
            "recovered_in_flight": True,
            "live_interrupted_receipt_exported": True,
            "receipt_task_id": receipt["task"]["id"],
            "receipt_operation_ordinal": operation["ordinal"],
            "receipt_dispatch": operation["dispatch"],
            "receipt_outcome": operation["outcome"],
            "receipt_replay_authorized": recovery["replay_authorized"],
            "receipt_path": str(receipt_path.relative_to(output)),
            **receipt_validation,
            "same_id_retry_blocked": retry_blocked,
            "same_id_retry_error": retry_error,
            "plan_policy": plan_policy,
            "plan_fingerprint": fingerprint,
            "completed_task": completed,
            "resolution": resolution,
            "final_ledger": final_state,
            "success": True,
        }
    finally:
        replacement.stop()


def run_go_surface(worker_binary, remote_cdp, base, output, token, repetition):
    surface = "go"
    task_id = f"uncertain-{repetition}-{token}"
    fixture_operation_id = f"uncertain-{surface}-{repetition}-{token}"
    store = output / f"{surface}-{repetition}-task-runs.db"
    page_url = f"{base}/saved-flow-task?operation_id={fixture_operation_id}"
    worker = subprocess.Popen([
        str(worker_binary), "run", f"--remote-cdp={remote_cdp}", f"--origin={base}",
        f"--url={page_url}", f"--task-id={task_id}", f"--store={store}",
    ], cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    ledger = None
    try:
        ledger = wait_for_commit(base, fixture_operation_id)
        worker.kill()
        worker.wait(timeout=5)
    except Exception:
        if worker.poll() is None:
            worker.kill()
            worker.wait(timeout=5)
        output_text = worker.stdout.read() if worker.stdout else ""
        raise RuntimeError(f"Go SDK worker did not reach the process-loss boundary; output={output_text!r}")
    evidence_path = output / "go" / f"{task_id}.ledger.json"
    evidence_path.write_text(json.dumps(ledger, indent=2) + "\n", encoding="utf-8")
    recovered = subprocess.run([
        str(worker_binary), "recover", f"--store={store}", f"--task-id={task_id}", f"--origin={base}",
        f"--evidence-reference={evidence_path}",
    ], cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
    if recovered.returncode:
        raise RuntimeError(f"Go SDK recovery failed: {recovered.stdout}")
    try:
        result = json.loads(recovered.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Go SDK recovery returned invalid JSON: {recovered.stdout!r}") from exc
    record = result.get("record") or {}
    receipt = result.get("receipt") or {}
    operation = receipt.get("operation") or {}
    recovery = receipt.get("recovery") or {}
    plan = record.get("plan") or {}
    fingerprint = record.get("fingerprint") or record.get("plan_fingerprint")
    plan_policy = {key: plan.get(key) for key in ("policy_id", "policy_enforcement", "allowed_origins", "allow_private_ips")}
    if not record.get("in_flight") and record.get("resolution", {}).get("status") != "verified_complete":
        raise RuntimeError(f"Go SDK recovery did not retain and resolve the task: {record}")
    if (receipt.get("task", {}).get("id") != task_id
            or operation.get("ordinal") != 1
            or operation.get("outcome") != "unknown"
            or operation.get("dispatch") != "may_have_dispatched"
            or recovery.get("replay_authorized") is not False
            or result.get("same_id_retry_blocked") is not True):
        raise RuntimeError(f"Go SDK receipt/replay invariants failed: {result}")
    # Persist the live receipt before invoking the independent verifier, which
    # accepts a file path. Run the strict Go decoder too; the receipt's origin
    # in the Go SDK is not itself evidence that the decoder accepts its export.
    receipt_path = output / "go" / f"{task_id}.recovery-receipt.json"
    receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    receipt_validation = verify_receipt(worker_binary, receipt_path, task_id)
    final_state = get_json(f"{base}/saved-flow-state?operation_id={fixture_operation_id}")
    if final_state.get("committed") != 1 or final_state.get("requests") != 1:
        raise RuntimeError(f"Go SDK fixture ledger changed during recovery: {final_state}")
    completed_id = f"completed-{repetition}-{token}"
    completed_operation_id = f"completed-go-{repetition}-{token}"
    completed_url = f"{base}/saved-flow-task?operation_id={completed_operation_id}"
    completed_run = subprocess.run([
        str(worker_binary), "complete", f"--remote-cdp={remote_cdp}", f"--origin={base}",
        f"--url={completed_url}", f"--task-id={completed_id}", f"--store={store}",
    ], cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=150)
    if completed_run.returncode:
        raise RuntimeError(f"Go SDK completed two-step task failed: {completed_run.stdout}")
    try:
        completed_payload = json.loads(completed_run.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"Go SDK completion returned invalid JSON: {completed_run.stdout!r}") from exc
    completed_record = completed_payload.get("record") or {}
    completed_receipts = completed_payload.get("receipts") or []
    if completed_record.get("in_flight") or (completed_record.get("result") or {}).get("status") != "completed":
        raise RuntimeError(f"Go SDK did not persist the completed task: {completed_record}")
    if len(completed_receipts) != 2:
        raise RuntimeError(f"Go SDK exported {len(completed_receipts)} completed receipts, want two")
    completed_artifacts = []
    for ordinal, receipt in enumerate(completed_receipts, start=1):
        operation = receipt.get("operation") or {}
        if (receipt.get("task", {}).get("id") != completed_id
                or receipt.get("task", {}).get("state") != "completed"
                or operation.get("ordinal") != ordinal
                or operation.get("outcome") != "verified"
                or operation.get("verification") != "verified"
                or receipt.get("recovery", {}).get("replay_authorized") is not False):
            raise RuntimeError(f"Go SDK completed operation {ordinal} receipt is invalid: {receipt}")
        path = output / "go" / f"{completed_id}.operation-{ordinal}.recovery-receipt.json"
        path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        validation = verify_receipt(worker_binary, path, completed_id)
        completed_artifacts.append({
            "ordinal": ordinal, "action": operation.get("name"), "outcome": operation.get("outcome"),
            "verification": operation.get("verification"), "receipt_path": str(path.relative_to(output)),
            **validation,
        })
    completed_plan = completed_record.get("plan") or {}
    completed_task = {
        "task_id": completed_id,
        "fingerprint": completed_record.get("fingerprint"),
        "policy": {key: completed_plan.get(key) for key in ("policy_id", "policy_enforcement", "allowed_origins", "allow_private_ips")},
        "status": completed_record["result"]["status"],
        "receipts": completed_artifacts,
    }
    # The earlier local `receipt` and `operation` names now refer to the
    # completed task's second action. Keep the interrupted evidence explicit.
    interrupted_receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
    interrupted_operation = interrupted_receipt.get("operation") or {}
    interrupted_recovery = interrupted_receipt.get("recovery") or {}
    return {
        "surface": surface, "repetition": repetition, "task_id": task_id,
        "fixture_operation_id": fixture_operation_id,
        "worker_exit_after_commit": True, "ledger_at_interruption": ledger,
        "recovered_in_flight": True, "live_interrupted_receipt_exported": True,
        "receipt_task_id": interrupted_receipt["task"]["id"], "receipt_operation_ordinal": interrupted_operation["ordinal"],
        "receipt_dispatch": interrupted_operation["dispatch"], "receipt_outcome": interrupted_operation["outcome"],
        "receipt_replay_authorized": interrupted_recovery["replay_authorized"],
        "receipt_path": str(receipt_path.relative_to(output)),
        **receipt_validation,
        "same_id_retry_blocked": result["same_id_retry_blocked"],
        "plan_policy": plan_policy, "plan_fingerprint": fingerprint,
        "completed_task": completed_task,
        "resolution": record.get("resolution"), "final_ledger": final_state, "success": True,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--chrome")
    parser.add_argument("--port", type=int)
    parser.add_argument("--repetitions", type=int, default=10, help="independent fixture trials per interface (default: 10)")
    args = parser.parse_args()
    if args.repetitions < 1 or args.repetitions > 100:
        parser.error("--repetitions must be between 1 and 100")
    chrome = args.chrome or os.environ.get("CHROME_BIN") or "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    chrome = shutil.which(chrome) if not Path(chrome).exists() else chrome
    if not chrome:
        parser.error("Chrome not found; pass --chrome or set CHROME_BIN")
    port = args.port or free_port()
    output = (args.output_dir or ROOT / "examples/benchmark/results" / f"saved-flow-interfaces-{dt.datetime.now(dt.timezone.utc):%Y%m%dT%H%M%SZ}").resolve()
    output.mkdir(parents=True, exist_ok=False)
    for surface in ("go", "cli", "mcp"):
        (output / surface).mkdir()
    run_token = uuid.uuid4().hex[:12]
    server_binary = output / "fixtureserver"
    scout_binary = output / "scout"
    go_worker_binary = output / "saved_flow_go_worker"
    for name, argv in (
        ("fixture-build.log", ["go", "build", "-o", str(server_binary), "./examples/benchmark/fixtureserver"]),
        ("scout-build.log", ["go", "build", "-o", str(scout_binary), "./cmd/scout"]),
        ("go-worker-build.log", ["go", "build", "-o", str(go_worker_binary), "./examples/benchmark/saved_flow_go_worker"]),
    ):
        result = subprocess.run(argv, cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        (output / name).write_text(result.stdout, encoding="utf-8")
        if result.returncode:
            raise SystemExit(f"build failed; see {output / name}")

    base = f"http://127.0.0.1:{port}"
    server_log = (output / "fixture-server.log").open("w", encoding="utf-8")
    server = subprocess.Popen([str(server_binary), "-listen", f"127.0.0.1:{port}", "-process-recovery"], cwd=ROOT, stdout=server_log, stderr=subprocess.STDOUT)
    browser = None
    try:
        for _ in range(100):
            if server.poll() is not None:
                raise RuntimeError("fixture server exited before readiness")
            try:
                get_json(base + "/saved-flow-state?operation_id=seed")
                break
            except Exception:
                time.sleep(0.1)
        else:
            raise TimeoutError("fixture server readiness timed out")
        results = []
        chrome_version = None
        for repetition in range(1, args.repetitions + 1):
            chrome_port = free_port()
            version_url = f"http://127.0.0.1:{chrome_port}/json/version"
            browser_log = (output / f"chrome-{repetition}.log").open("w", encoding="utf-8")
            profile = output / f"chrome-profile-{repetition}"
            browser = subprocess.Popen([
                chrome, "--headless=new", "--no-sandbox", "--disable-gpu",
                "--remote-debugging-address=127.0.0.1", f"--remote-debugging-port={chrome_port}",
                "--remote-allow-origins=*", f"--user-data-dir={profile}", "--disable-quic", "about:blank",
            ], stdout=browser_log, stderr=subprocess.STDOUT, start_new_session=True)
            try:
                for _ in range(200):
                    if browser.poll() is not None:
                        raise RuntimeError("Chrome exited before CDP readiness")
                    try:
                        remote_cdp = get_json(version_url).get("webSocketDebuggerUrl")
                        if remote_cdp:
                            chrome_version = get_json(version_url).get("Browser")
                            break
                    except Exception:
                        time.sleep(0.1)
                else:
                    raise TimeoutError("Chrome CDP readiness timed out")
                order = ("cli", "mcp", "go") if repetition % 2 else ("mcp", "cli", "go")
                for surface in order:
                    if surface == "go":
                        results.append(run_go_surface(go_worker_binary, remote_cdp, base, output, run_token, repetition))
                    else:
                        results.append(run_surface(surface, scout_binary, go_worker_binary, remote_cdp, base, output, run_token, repetition))
            finally:
                if browser is not None and browser.poll() is None:
                    browser.terminate()
                    try:
                        browser.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        browser.kill()
                        browser.wait(timeout=5)
                browser = None
                browser_log.close()
        live_receipt_parity = compare_live_receipts(results, output)
        report = {
            "schema_version": 1,
            "benchmark": "scout_saved_flow_cli_mcp_process_recovery_conformance",
            "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "environment": {"platform": platform.platform(), "chrome": chrome_version, "scout_revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(), "worktree_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip())},
            "fixture": "shared non-idempotent saved-flow mutation; worker killed after one ledger effect while Chrome remains live over CDP",
            "surfaces": results,
            "live_receipt_parity": live_receipt_parity,
            "limitations": ["local deterministic fixture trials do not estimate customer reliability", "does not measure integration effort or latency", "MCP and CLI use independent task-store namespaces", "fixture ledger is the host-authoritative oracle", "one Chrome instance is held constant within each paired repetition"],
            "success": all(item["success"] for item in results),
        }
        policy_fields = ("policy_id", "policy_enforcement", "allowed_origins", "allow_private_ips")
        paired = []
        for repetition in range(1, args.repetitions + 1):
            pair = {item["surface"]: item for item in results if item["repetition"] == repetition}
            interfaces = [pair[surface] for surface in ("go", "cli", "mcp")]
            policy_matches = all(all(interfaces[0]["plan_policy"].get(key) == item["plan_policy"].get(key) for key in policy_fields) for item in interfaces[1:])
            fingerprint_matches = all(item["plan_fingerprint"] == interfaces[0]["plan_fingerprint"] for item in interfaces[1:])
            paired.append({"repetition": repetition, "policy_matches": policy_matches, "fingerprint_matches": fingerprint_matches,
                           "go_fingerprint": pair["go"]["plan_fingerprint"], "cli_fingerprint": pair["cli"]["plan_fingerprint"], "mcp_fingerprint": pair["mcp"]["plan_fingerprint"]})
        report["pair_parity"] = paired
        report["policy_parity"] = {"matches": all(item["policy_matches"] for item in paired)}
        report["fingerprint_parity"] = {"matches": all(item["fingerprint_matches"] for item in paired)}
        successes = {surface: sum(1 for item in results if item["surface"] == surface and item["success"]) for surface in ("go", "cli", "mcp")}
        z = 1.959963984540054
        def wilson(success_count):
            n = args.repetitions
            p = success_count / n
            denom = 1 + z * z / n
            center = (p + z * z / (2 * n)) / denom
            margin = z * math.sqrt((p * (1 - p) / n) + (z * z / (4 * n * n))) / denom
            return {"successes": success_count, "trials": n, "rate": p, "wilson_95": [max(0, center - margin), min(1, center + margin)]}
        report["conformance"] = {surface: wilson(count) for surface, count in successes.items()}
        report["live_receipt_conformance"] = {
            "all_exported": all(item["live_interrupted_receipt_exported"] for item in results),
            "all_unknown": all(item["receipt_outcome"] == "unknown" for item in results),
            "all_may_have_dispatched": all(item["receipt_dispatch"] == "may_have_dispatched" for item in results),
            "replay_never_authorized": all(item["receipt_replay_authorized"] is False for item in results),
            "task_identity_matches": all(item["receipt_task_id"] == item["task_id"] for item in results),
            "all_valid_go_decoder": all(item.get("go_decoder_valid", False) for item in results),
            "all_valid_python_verifier": all(item.get("python_verifier_valid", True) for item in results),
        }
        report["success"] = report["success"] and report["policy_parity"]["matches"] and report["fingerprint_parity"]["matches"] and all(value == args.repetitions for value in successes.values()) and all(report["live_receipt_conformance"].values()) and report["live_receipt_parity"].get("passed") is True
        report_path = output / "summary.json"
        report_path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        console_summary = {
            "success": report["success"],
            "repetitions": args.repetitions,
            "conformance": report["conformance"],
            "policy_parity": report["policy_parity"]["matches"],
            "fingerprint_parity": report["fingerprint_parity"]["matches"],
            "live_receipt_conformance": report["live_receipt_conformance"],
            "live_receipt_parity": report["live_receipt_parity"].get("passed"),
        }
        print(json.dumps(console_summary, indent=2))
        print(f"Artifacts: {output}")
        if not report["success"]:
            raise SystemExit(1)
    finally:
        if browser is not None and browser.poll() is None:
            browser.terminate()
            try:
                browser.wait(timeout=10)
            except subprocess.TimeoutExpired:
                browser.kill()
                browser.wait(timeout=5)
        if server.poll() is None:
            server.terminate()
            try:
                server.wait(timeout=10)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait(timeout=5)
        server_log.close()


if __name__ == "__main__":
    main()
