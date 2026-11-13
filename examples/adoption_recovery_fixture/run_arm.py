#!/usr/bin/env python3
"""Run one participant implementation through the controlled loss boundary."""
import argparse
import datetime as dt
import hashlib
import json
import os
import shlex
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request


def utc_now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def request(base_url, path, method="GET", timeout=5):
    req = urllib.request.Request(base_url.rstrip("/") + path, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as response:
        raw = response.read()
        return json.loads(raw) if raw else None


def command(args, name):
    try:
        result = shlex.split(args)
    except ValueError as error:
        raise ValueError(f"{name} is not valid quoted argv: {error}") from error
    if not result:
        raise ValueError(f"{name} must contain an executable")
    return result


def report_command(argv):
    """Keep useful run provenance while hiding common credential arguments."""
    secret_flags = {"--token", "--password", "--secret", "--api-key", "--apikey", "--authorization", "--cookie"}
    redacted = []
    hide_next = False
    for token in argv:
        if hide_next:
            redacted.append("<redacted>")
            hide_next = False
            continue
        flag, separator, value = token.partition("=")
        env_name, env_separator, env_value = token.partition("=")
        if (flag.lower() in secret_flags or
                (env_separator and any(word in env_name.lower() for word in ("token", "password", "secret", "api_key", "apikey", "authorization", "cookie")))):
            redacted.append(env_name + "=<redacted>" if env_separator else flag + ("=<redacted>" if separator else ""))
            hide_next = not (env_separator or separator)
        else:
            redacted.append(token)
    return redacted


def source_manifest(workdir, output_path, commands, source_paths):
    """Hash command-referenced source paths and any source files they name."""
    root = Path(workdir).resolve()
    output = Path(output_path).resolve()
    excluded = {".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", "target", "coverage"}
    source_suffixes = {".go", ".py", ".js", ".ts", ".mod", ".sum", ".json", ".yaml", ".yml", ".toml"}
    sensitive_names = {".env", ".env.local", "credentials.json", "secrets.json", "secrets.yaml", "secrets.yml", "id_rsa", "id_ed25519"}
    candidates = set()
    for token in source_paths:
        candidate = Path(token)
        try:
            candidate = candidate.resolve().relative_to(root) if candidate.is_absolute() else candidate
        except ValueError:
            continue
        path = (root / candidate).resolve()
        if (path != root and root not in path.parents) or any(part in excluded for part in candidate.parts):
            continue
        if path.is_file() and path.suffix.lower() in source_suffixes:
            candidates.add(candidate)
        elif path.is_dir():
            candidates.update(p.relative_to(root) for p in path.rglob("*") if p.is_file() and p.suffix.lower() in source_suffixes and not any(part in excluded for part in p.relative_to(root).parts))
    for argv in commands:
        for token in argv:
            if token.startswith("-") or token in {"go", "run", "build", "test", "python", "python3", "node", "npx", "env", "sh", "bash"}:
                continue
            candidate = Path(token)
            try:
                candidate = candidate.resolve().relative_to(root) if candidate.is_absolute() else candidate
            except ValueError:
                continue
            path = (root / candidate).resolve()
            if root not in path.parents or any(part in excluded for part in candidate.parts):
                continue
            if path.is_file() and path.suffix.lower() in source_suffixes:
                candidates.add(candidate)
            elif path.is_dir() and token.startswith("."):
                candidates.update(p.relative_to(root) for p in path.rglob("*") if p.is_file() and p.suffix.lower() in source_suffixes and not any(part in excluded for part in p.relative_to(root).parts))
    for candidate in list(candidates):
        path = (root / candidate).resolve()
        if path.is_file() and path.suffix.lower() in source_suffixes:
            try:
                for line in path.read_text(encoding="utf-8").splitlines():
                    stripped = line.strip().strip("\"'` ;(){}[]")
                    for suffix in source_suffixes:
                        if stripped.endswith(suffix):
                            dependency = Path(stripped.split()[-1])
                            if not dependency.is_absolute():
                                candidates.add(candidate.parent / dependency)
            except (OSError, UnicodeDecodeError):
                continue
    files = []
    for candidate in sorted(candidates, key=lambda item: item.as_posix()):
        path = (root / candidate).resolve()
        relative = path.relative_to(root) if root in path.parents else None
        if (relative is None or not path.is_file() or any(part in excluded for part in relative.parts)
                or path.name.lower() in sensitive_names or path.suffix.lower() in {".pem", ".key", ".p12", ".pfx"}):
            continue
        if path == output or path.suffix.lower() in {".db", ".sqlite", ".log", ".png", ".jpg", ".jpeg", ".webm", ".mp4"}:
            continue
        rel = path.relative_to(root).as_posix()
        digest = hashlib.sha256()
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                digest.update(chunk)
        files.append({"path": rel, "sha256": digest.hexdigest(), "size_bytes": path.stat().st_size})
    return files


def harness_manifest():
    """Pin the fixture and grading code that define this run's fault boundary."""
    root = Path(__file__).resolve().parent
    paths = ("main.go", "run_arm.py", "grade_run.py")
    manifest = []
    for relative in paths:
        path = root / relative
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        manifest.append({"path": relative, "sha256": digest, "size_bytes": path.stat().st_size})
    return manifest


def git_revision(workdir):
    try:
        root = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"], cwd=workdir,
            check=True, capture_output=True, text=True, timeout=5,
        ).stdout.strip()
        return subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=root,
            check=True, capture_output=True, text=True, timeout=5,
        ).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return None


def git_worktree_changes(workdir):
    try:
        root = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"], cwd=workdir,
            check=True, capture_output=True, text=True, timeout=5,
        ).stdout.strip()
        worktree = subprocess.run(
            ["git", "rev-parse", "--show-prefix"], cwd=workdir,
            check=True, capture_output=True, text=True, timeout=5,
        ).stdout.strip().rstrip("/")
        relative = worktree or "."
        pathspec = ["--", "."] if relative == "." else ["--", relative]
        result = subprocess.run(
            ["git", "status", "--porcelain", "--untracked-files=all", *pathspec], cwd=root,
            check=True, capture_output=True, text=True, timeout=5,
        )
        return sorted(line[3:] for line in result.stdout.splitlines() if len(line) > 3)
    except (OSError, subprocess.SubprocessError):
        return None


def executable_provenance(commands, workdir):
    """Record executable identity without copying the binary."""
    records = []
    seen = set()
    for argv in commands:
        if not argv:
            continue
        executable = shutil.which(argv[0], path=os.environ.get("PATH"))
        if executable is None:
            candidate = Path(argv[0])
            if not candidate.is_absolute():
                candidate = Path(workdir) / candidate
            if candidate.is_file() and os.access(candidate, os.X_OK):
                executable = str(candidate)
        if executable is None:
            records.append({"command_name": Path(argv[0]).name, "status": "not_resolved"})
            continue
        path = Path(executable).resolve()
        if str(path) in seen:
            continue
        seen.add(str(path))
        digest = hashlib.sha256()
        try:
            with path.open("rb") as handle:
                for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                    digest.update(chunk)
        except OSError as error:
            records.append({"command_name": path.name, "status": "unreadable", "error": str(error)})
            continue
        records.append({"command_name": path.name, "sha256": digest.hexdigest(), "size_bytes": path.stat().st_size})
    return records


def kill_process_group(child, sig=signal.SIGKILL):
    """Kill a worker's process group, tolerating exit races."""
    try:
        os.killpg(child.pid, sig)
        return True
    except ProcessLookupError:
        return False


def stop_and_collect(child, timeout=10):
    """Bound cleanup and pipe draining, including inherited pipes in descendants."""
    kill_process_group(child)
    try:
        return child.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        # A descendant may have escaped the original group or kept stdout open.
        # Retry the group kill once and keep the facilitator from hanging forever.
        kill_process_group(child)
        return child.communicate(timeout=timeout)


def run_bounded(command_args, cwd, timeout):
    child = subprocess.Popen(
        command_args,
        cwd=cwd,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    try:
        stdout, _ = child.communicate(timeout=timeout)
        return child.returncode, stdout, False
    except subprocess.TimeoutExpired:
        stdout, _ = stop_and_collect(child)
        return child.returncode, stdout, True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base_url")
    parser.add_argument("task_id")
    parser.add_argument("--first-success-command", required=True, help="participant command that verifies the non-mutating first-success page")
    parser.add_argument("--control-command", required=True, help="participant command that completes a normal approval for the paired no-fault control")
    parser.add_argument("--submit-command", required=True, help="participant command that performs one browser mutation")
    parser.add_argument("--reconcile-command", required=True, help="fresh-process command that inspects the task and authoritative ledger")
    parser.add_argument("--workdir", default=".")
    parser.add_argument("--commit-timeout", type=float, default=120)
    parser.add_argument("--reconcile-timeout", type=float, default=120)
    parser.add_argument("--output", required=True, help="path to save JSON evidence")
    parser.add_argument("--source-path", action="append", default=[], help="additional participant source file or directory to include in the provenance manifest; repeat as needed")
    args = parser.parse_args()
    output_path = os.path.abspath(args.output)
    try:
        parsed_commands = {
            "first_success": command(args.first_success_command, "--first-success-command"),
            "control": command(args.control_command, "--control-command"),
            "submit": command(args.submit_command, "--submit-command"),
            "reconcile": command(args.reconcile_command, "--reconcile-command"),
        }
    except ValueError as error:
        os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
        with open(output_path, "w", encoding="utf-8") as handle:
            json.dump({
                "schema_version": 1,
                "task_id": args.task_id,
                "base_url": args.base_url.rstrip("/"),
                "passed": False,
                "preflight_error": str(error),
                "events": [],
            }, handle, indent=2)
            handle.write("\n")
        print(json.dumps({"evidence": output_path, "passed": False, "preflight_error": str(error)}, indent=2))
        return 2
    base = args.base_url.rstrip("/")
    task_query = urllib.parse.urlencode({"task": args.task_id})
    participant_commands = [
        parsed_commands["first_success"], parsed_commands["control"],
        parsed_commands["submit"], parsed_commands["reconcile"],
    ]
    source_files = source_manifest(args.workdir, args.output, participant_commands, args.source_path)
    report = {
        "schema_version": 1,
        "task_id": args.task_id,
        "base_url": base,
        "harness_provenance": harness_manifest(),
        "submit_command": report_command(parsed_commands["submit"]),
        "control_command": report_command(parsed_commands["control"]),
        "first_success_command": report_command(parsed_commands["first_success"]),
        "reconcile_command": report_command(parsed_commands["reconcile"]),
        "workdir": os.path.abspath(args.workdir),
        "source_provenance": {
            "git_head": git_revision(args.workdir),
            "git_changed_paths": git_worktree_changes(args.workdir),
            "executables": executable_provenance(participant_commands, args.workdir),
            "captured_at": utc_now(),
            "manifest_algorithm": "sha256",
            "manifest_status": "captured" if source_files else "empty_source_set",
            "manifest_scope": "recognized source/config files and source directories referenced by participant commands or --source-path, plus relative source paths from included files; known credential/key filenames and contents are not copied",
            "files": source_files,
        },
        "events": [],
        "limitations": [
            "Process group termination covers descendants that remain in the worker's process group; detached descendants require separate OS-level audit.",
        "The fixture ledger independently records mutation attempts after page-request validation; accepted dispatches commit an effect atomically, while rejected attempts carry a reason and no effect.",
        ],
    }
    def persist_report():
        os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
        with open(output_path, "w", encoding="utf-8") as handle:
            json.dump(report, handle, indent=2)
            handle.write("\n")

    request(base, "/operator/reset", method="POST")
    report["events"].append({"event": "fixture_reset", "at": utc_now()})
    first_url = base + "/first-success?" + task_query
    first_cmd = parsed_commands["first_success"] + ["--task-id", args.task_id, "--fixture", first_url]
    first_started = time.monotonic()
    first_exit, first_stdout, first_timed_out = run_bounded(first_cmd, args.workdir, args.reconcile_timeout)
    try:
        first_json = json.loads(first_stdout.strip().splitlines()[-1])
    except (IndexError, json.JSONDecodeError):
        first_json = {}
    first_event = request(base, "/first-success/observe?" + task_query)
    report["first_success"] = {
        "command": report_command(first_cmd),
        "finished_at": utc_now(),
        "elapsed_ms": round((time.monotonic() - first_started) * 1000, 2),
        "exit_code": first_exit,
        "timed_out": first_timed_out,
        "output": first_stdout,
        "result": first_json,
        "fixture_observation": first_event,
    }
    first_ok = (
        first_event.get("event_count") == 1
        and first_event.get("status") == "Complete"
        and first_event.get("complete_click_count") == 1
        and not first_timed_out and first_exit == 0
        and first_json.get("status") == "verified"
        and first_json.get("verified_text") == "Complete"
    )
    if not first_ok:
        report["checks"] = {
            "first_success_reported_verified": first_json.get("status") == "verified",
            "first_success_fixture_event_observed": first_event.get("event_count") == 1,
            "first_success_rendered_postcondition_observed": first_event.get("status") == "Complete",
        }
        persist_report()
        print(json.dumps({"evidence": output_path, "passed": False, "checks": report["checks"]}, indent=2))
        return 1
    report["events"].append({"event": "first_success_verified", "at": utc_now(), "participant_elapsed_ms": first_json.get("elapsed_ms")})

    # Run the same approval flow once without worker loss. A separate operation
    # ID and fixture reset keep this competence control independent of the
    # faulted task and any product-side idempotency state.
    control_task_id = args.task_id + "-control"
    control_query = urllib.parse.urlencode({"task": control_task_id})
    control_cmd = parsed_commands["control"] + ["--task-id", control_task_id, "--fixture", base]
    control_started_at = utc_now()
    control_started = time.monotonic()
    control_child = subprocess.Popen(
        control_cmd,
        cwd=args.workdir,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    control_deadline = control_started + args.commit_timeout
    control_ledger = None
    while time.monotonic() < control_deadline:
        if control_child.poll() is not None:
            output, _ = stop_and_collect(control_child)
            report["control"] = {"command": report_command(control_cmd), "exit_code_before_commit": control_child.returncode, "output": output}
            persist_report()
            raise RuntimeError("no-fault control worker exited before fixture commit")
        try:
            control_ledger = request(base, "/ledger?" + control_query, timeout=2)
        except (OSError, urllib.error.URLError, json.JSONDecodeError):
            time.sleep(0.1)
            continue
        if control_ledger.get("count", 0) >= 1:
            break
        time.sleep(0.05)

    if not control_ledger or control_ledger.get("count") != 1:
        output, _ = stop_and_collect(control_child)
        request(base, "/operator/release?" + control_query, method="POST")
        report["control"] = {"command": report_command(control_cmd), "exit_code": control_child.returncode, "output": output, "ledger": control_ledger}
        persist_report()
        raise TimeoutError("no-fault control did not commit exactly one effect before deadline")

    request(base, "/operator/release?" + control_query, method="POST")
    try:
        control_stdout, _ = control_child.communicate(timeout=args.reconcile_timeout)
        control_timed_out = False
    except subprocess.TimeoutExpired:
        control_stdout, _ = stop_and_collect(control_child)
        control_timed_out = True
    try:
        control_result = json.loads(control_stdout.strip().splitlines()[-1])
    except (IndexError, json.JSONDecodeError):
        control_result = {}
    report["control"] = {
        "task_id": control_task_id,
        "command": report_command(control_cmd),
        "started_at": control_started_at,
        "elapsed_ms": round((time.monotonic() - control_started) * 1000, 2),
        "exit_code": control_child.returncode,
        "timed_out": control_timed_out,
        "output": control_stdout,
        "result": control_result,
        "ledger": request(base, "/ledger?" + control_query),
    }
    report["control"]["checks"] = {
        "worker_completed": control_child.returncode == 0 and not control_timed_out,
        "exactly_one_effect": report["control"]["ledger"].get("count") == 1,
        "exactly_one_accepted_dispatch": report["control"]["ledger"].get("accepted_dispatch_count") == 1,
        "no_rejected_mutation_attempts": (
            report["control"]["ledger"].get("attempt_count") == 1
            and all(item.get("accepted") is True for item in report["control"]["ledger"].get("attempts", []))
        ),
        "participant_reported_verified": control_result.get("status") == "verified",
        "participant_task_identity_matches": control_result.get("task_id") == control_task_id,
        "participant_reported_ledger_count_one": control_result.get("ledger_count") == 1,
        "participant_verified_page_postcondition": control_result.get("verified_text") == "Accepted 1",
        "participant_reported_no_retry": control_result.get("retry_attempted") is False,
    }
    report["control"]["passed"] = all(report["control"]["checks"].values())
    if not report["control"]["passed"]:
        persist_report()
        print(json.dumps({"evidence": output_path, "passed": False, "control_checks": report["control"]["checks"]}, indent=2))
        return 1
    report["events"].append({"event": "nominal_approval_control_verified", "at": utc_now(), "task_id": control_task_id})
    request(base, "/operator/reset", method="POST")
    report["events"].append({"event": "fixture_reset_before_faulted_approval", "at": utc_now()})

    submit_cmd = parsed_commands["submit"] + ["--task-id", args.task_id, "--fixture", base]
    submit_started = utc_now()
    child = subprocess.Popen(
        submit_cmd,
        cwd=args.workdir,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        start_new_session=True,
    )
    report["submit"] = {"pid": child.pid, "started_at": submit_started, "command": report_command(submit_cmd)}
    deadline = time.monotonic() + args.commit_timeout
    ledger = None
    while time.monotonic() < deadline:
        if child.poll() is not None:
            output, _ = stop_and_collect(child)
            report["submit"].update({"exit_code_before_commit": child.returncode, "output": output})
            persist_report()
            raise RuntimeError("submit worker exited before fixture commit")
        try:
            ledger = request(base, "/ledger?" + task_query, timeout=2)
        except (OSError, urllib.error.URLError, json.JSONDecodeError):
            time.sleep(0.1)
            continue
        if ledger.get("count", 0) >= 1:
            break
        time.sleep(0.05)
    if not ledger or ledger.get("count") != 1:
        output, _ = stop_and_collect(child)
        report["submit"].update({"exit_code": child.returncode, "output": output})
        persist_report()
        raise TimeoutError("fixture did not observe exactly one committed effect before deadline")

    committed = ledger["entries"][0]
    report["events"].append({"event": "fixture_received_and_committed_mutation", "received_at": committed.get("received_at"), "committed_at": committed.get("committed_at"), "sequence": committed.get("sequence")})
    killed_at = utc_now()
    kill_process_group(child)
    try:
        submit_output, _ = child.communicate(timeout=10)
    except subprocess.TimeoutExpired:
        kill_process_group(child)
        try:
            submit_output, _ = child.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            report["submit"].update({
                "killed_at": killed_at,
                "termination_signal": "SIGKILL",
                "exit_code": child.poll(),
                "output_collection_timed_out": True,
            })
            persist_report()
            raise RuntimeError(
                "submit worker or inherited output pipe did not close within 20 seconds"
            )
    report["submit"].update({"killed_at": killed_at, "termination_signal": "SIGKILL", "exit_code": child.returncode, "output": submit_output})
    report["events"].append({"event": "submit_worker_process_group_killed", "at": killed_at, "exit_code": child.returncode})

    request(base, "/operator/release?" + task_query, method="POST")
    report["events"].append({"event": "held_response_released_after_kill", "at": utc_now()})

    reconcile_cmd = parsed_commands["reconcile"] + ["--task-id", args.task_id, "--fixture", base]
    reconcile_started = utc_now()
    reconcile_exit, reconcile_stdout, reconcile_timed_out = run_bounded(reconcile_cmd, args.workdir, args.reconcile_timeout)
    report["reconcile"] = {
        "command": report_command(reconcile_cmd),
        "started_at": reconcile_started,
        "finished_at": utc_now(),
        "exit_code": reconcile_exit,
        "timed_out": reconcile_timed_out,
        "output": reconcile_stdout,
    }
    try:
        report["reconcile"]["result"] = json.loads(reconcile_stdout.strip().splitlines()[-1])
    except (IndexError, json.JSONDecodeError):
        report["reconcile"]["result_parse_error"] = "last stdout line was not JSON"
    report["final_ledger"] = request(base, "/ledger?" + task_query)
    report["fixture_accepted_dispatch_count"] = report["final_ledger"].get("accepted_dispatch_count")
    result = report["reconcile"].get("result", {})
    report["participant_reported_dispatch_count"] = result.get("dispatch_count")
    report["initial_status"] = result.get("initial_status")
    report["ledger_inspected"] = result.get("ledger_inspected")
    report["final_classification"] = result.get("status")
    report["retry_attempted"] = result.get("retry_attempted")
    report["checks"] = {
        "nominal_control_passed": report["control"]["passed"],
        "first_success_reported_verified": first_json.get("status") == "verified",
        "first_success_fixture_event_observed": first_event.get("event_count") == 1,
        "first_success_rendered_postcondition_observed": first_event.get("status") == "Complete",
        "one_effect_committed_before_interruption": ledger.get("count") == 1,
        "worker_terminated_by_controller": report["submit"].get("exit_code") == -signal.SIGKILL,
        "final_effect_count_is_one": report["final_ledger"].get("count") == 1,
        "reconciler_preserved_unknown": result.get("initial_status") == "unknown",
        "reconciler_read_authoritative_ledger": result.get("ledger_inspected") is True,
        "reconciler_task_identity_matches": result.get("task_id") == args.task_id,
        "reconciler_reported_verified": result.get("status") == "verified",
        "reconciler_reported_ledger_count_one": result.get("ledger_count") == 1,
        "reconciler_dispatch_count_matches_fixture": result.get("dispatch_count") == report["fixture_accepted_dispatch_count"],
        "no_rejected_mutation_attempts": all(item.get("accepted") is True for item in report["final_ledger"].get("attempts", [])),
        "reconciler_reported_no_retry": result.get("retry_attempted") is False,
        # Product-specific recovery records and host journals are facilitator
        # source-review items, not a shared participant JSON contract. Keep the
        # machine grade on observable behavior common to both products.
    }
    report["runner_passed"] = all(report["checks"].values())
    persist_report()
    grader_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "grade_run.py")
    try:
        grade_process = subprocess.run(
            [sys.executable, grader_path, base, args.task_id, output_path],
            check=False,
            capture_output=True,
            text=True,
            timeout=15,
        )
        try:
            report["independent_grade"] = json.loads(grade_process.stdout)
        except json.JSONDecodeError:
            report["independent_grade"] = {
                "passed": False,
                "error": "grader output was not valid JSON",
                "stdout": grade_process.stdout,
                "stderr": grade_process.stderr,
                "exit_code": grade_process.returncode,
            }
    except (OSError, subprocess.TimeoutExpired) as error:
        report["independent_grade"] = {"passed": False, "error": str(error)}
    report["independent_grade"]["exit_code"] = grade_process.returncode if "grade_process" in locals() else None
    report["passed"] = report["runner_passed"] and report["independent_grade"].get("passed") is True and report["independent_grade"]["exit_code"] == 0
    persist_report()
    print(json.dumps({"evidence": output_path, "passed": report["passed"], "checks": report["checks"], "independent_grade_passed": report["independent_grade"].get("passed", False)}, indent=2))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, urllib.error.URLError, ValueError, RuntimeError, TimeoutError) as error:
        print(f"facilitator error: {error}", file=sys.stderr)
        raise SystemExit(2)
