#!/usr/bin/env python3
"""Verify mitmproxy addon startup is observable and fails closed."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parents[2]
ADDON = Path(__file__).with_name("allowlist.py")


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def wait_until(predicate, process, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        if process.poll() is not None:
            return False
        time.sleep(0.05)
    return predicate()


def run_case(binary: str, label: str, policy: str, should_start: bool) -> dict:
    port = free_port()
    env = os.environ.copy()
    env["SCOUT_ALLOWED_ORIGINS"] = policy
    with tempfile.TemporaryDirectory(prefix="scout-egress-startup-") as tmp:
        log = Path(tmp) / "mitmdump.log"
        with log.open("w+", encoding="utf-8") as output:
            process = subprocess.Popen(
                [binary, "--mode", "regular", "--listen-host", "127.0.0.1", "--listen-port", str(port), "-s", str(ADDON)],
                cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT,
            )
            try:
                if should_start:
                    started = wait_until(lambda: _tcp_open(port), process)
                    if not started:
                        raise RuntimeError(f"{label}: valid policy did not start listening")
                    if not wait_until(lambda: _startup_record(_read_log(log)) is not None, process):
                        raise RuntimeError(f"{label}: startup audit record did not appear: {_read_log(log)}")
                    text = log.read_text(encoding="utf-8", errors="replace")
                    record = _startup_record(text)
                    if not record or record.get("status") != "ok" or record.get("origin_count") != 1:
                        raise RuntimeError(f"{label}: startup success record missing or invalid: {record}")
                    process.terminate()
                    process.wait(timeout=5)
                    return {"case": label, "started": True, "exit_code": process.returncode, "startup": record}

                exited = wait_until(lambda: process.poll() is not None, process, timeout=10)
                if not exited:
                    raise RuntimeError(f"{label}: invalid policy unexpectedly left mitmdump running")
                output.flush()
                text = log.read_text(encoding="utf-8", errors="replace")
                record = _startup_record(text)
                if not record or record.get("status") != "error" or record.get("reason") != "invalid_policy":
                    raise RuntimeError(f"{label}: startup failure record missing or invalid: {record}; output={text}")
                if "secret" in text or "user:" in text:
                    raise RuntimeError(f"{label}: startup log disclosed policy credentials")
                if _tcp_open(port):
                    raise RuntimeError(f"{label}: invalid policy process accepted connections")
                return {"case": label, "started": False, "exit_code": process.returncode, "startup": record}
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)


def _startup_record(text: str):
    for line in text.splitlines():
        marker = "SCOUT_EGRESS_STARTUP "
        index = line.find(marker)
        if index >= 0:
            try:
                return json.loads(line[index + len(marker):])
            except json.JSONDecodeError:
                return None
    return None


def _tcp_open(port: int) -> bool:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.1):
            return True
    except OSError:
        return False


def _read_log(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mitmdump", default=shutil.which("mitmdump"))
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if not args.mitmdump:
        parser.error("mitmdump is required (install the pinned mitmproxy version)")
    cases = [
        run_case(args.mitmdump, "valid_policy", "https://allowed.example", True),
        run_case(args.mitmdump, "empty_policy", "", False),
        run_case(args.mitmdump, "malformed_policy", "https://user:secret@example.com/path", False),
    ]
    report = {"schema_version": 1, "mitmdump": args.mitmdump, "cases": cases, "success": True}
    encoded = json.dumps(report, indent=2) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded, encoding="utf-8")
    print(encoded, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
