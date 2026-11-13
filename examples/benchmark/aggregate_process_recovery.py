#!/usr/bin/env python3
"""Aggregate isolated full-process-loss recovery probe reports."""
import argparse
import json
import math
import pathlib
import sys

MINIMUM_REPETITIONS = 30


def wilson(successes, total, z=1.96):
    if total == 0:
        return None
    p = successes / total
    denominator = 1 + z * z / total
    center = (p + z * z / (2 * total)) / denominator
    margin = z * math.sqrt((p * (1 - p) + z * z / (4 * total)) / total) / denominator
    return [max(0.0, center - margin), min(1.0, center + margin)]


def read_reports(list_file, tool, filename, expected_version):
    directories = [pathlib.Path(line.strip()) for line in pathlib.Path(list_file).read_text().splitlines() if line.strip()]
    if not directories:
        raise ValueError(f"no run directories listed for {tool}")
    reports = []
    for directory in directories:
        path = directory / filename
        if not path.is_file():
            raise ValueError(f"missing report: {path}")
        report = json.loads(path.read_text())
        if report.get("scenario_id") != "committed-mutation-hard-browser-kill":
            raise ValueError(f"unexpected scenario in {path}")
        if report.get("tool_name") != tool:
            raise ValueError(f"unexpected tool in {path}")
        if report.get("tool_version") != expected_version:
            raise ValueError(f"unexpected {tool} version in {path}: {report.get('tool_version')!r}")
        if report.get("benchmark_version") != "scout-browser-reliability-v28" or report.get("fixture_version") != "scout-browser-reliability-v28":
            raise ValueError(f"unexpected benchmark or fixture version in {path}")
        if report.get("comparison_protocol_version") != "scout-browser-comparison-v1":
            raise ValueError(f"unexpected comparison protocol in {path}")
        expected_boundary = "daemon" if tool == "agent-browser" else "full-browser-and-MCP-process-loss"
        if report.get("fault_boundary") != expected_boundary:
            raise ValueError(f"unexpected fault boundary in {path}: {report.get('fault_boundary')}")
        if report.get("mutation_committed_before_kill") is not True:
            raise ValueError(f"mutation was not confirmed before kill: {path}")
        for field in (
            "reopen_succeeded",
            "session_marker_restored",
            "mutation_outcome_preserved",
            "blind_same_action_replay_dispatched",
            "duplicate_side_effect_after_blind_replay",
            "success",
        ):
            if not isinstance(report.get(field), bool):
                raise ValueError(f"missing boolean outcome {field}: {path}")
        for field in ("mutation_count_after_restart", "mutation_count_after_blind_replay"):
            value = report.get(field)
            if isinstance(value, bool) or not isinstance(value, int):
                raise ValueError(f"missing authoritative {field}: {path}")
        if tool == "agent-browser" and report.get("browser_state_restore_enabled") is not True:
            raise ValueError(f"agent-browser restore was not explicitly enabled: {path}")
        reports.append({
            "run_dir": str(directory),
            "fault_boundary": report["fault_boundary"],
            "browser_state_restore_enabled": report.get("browser_state_restore_enabled"),
            "browser_state_restore_flags": report.get("browser_state_restore_flags"),
            "mutation_count_after_restart": report.get("mutation_count_after_restart"),
            "mutation_count_after_blind_replay": report.get("mutation_count_after_blind_replay"),
            "mutation_outcome_preserved": report.get("mutation_outcome_preserved"),
            "session_marker_restored": report.get("session_marker_restored"),
            "blind_same_action_replay_dispatched": report.get("blind_same_action_replay_dispatched"),
            "duplicate_side_effect_after_blind_replay": report.get("duplicate_side_effect_after_blind_replay"),
            "reopen_succeeded": report.get("reopen_succeeded"),
            "success": report.get("success"),
            "tool_version": report["tool_version"],
            "benchmark_version": report["benchmark_version"],
            "fixture_version": report["fixture_version"],
            "comparison_protocol_version": report["comparison_protocol_version"],
        })
    return reports


def summarize(reports):
    total = len(reports)
    preserved = sum(row["mutation_count_after_restart"] == 1 and row["mutation_outcome_preserved"] is True for row in reports)
    restored = sum(row["session_marker_restored"] is True for row in reports)
    reopened = sum(row["reopen_succeeded"] is True for row in reports)
    duplicate = sum(row["duplicate_side_effect_after_blind_replay"] is True for row in reports)
    return {
        "repetitions": total,
        "mutation_preserved": {"successes": preserved, "rate": preserved / total, "wilson_95_ci": wilson(preserved, total)},
        "session_marker_restored": {"successes": restored, "rate": restored / total, "wilson_95_ci": wilson(restored, total)},
        "reopened": {"successes": reopened, "rate": reopened / total, "wilson_95_ci": wilson(reopened, total)},
        "blind_replay_duplicate_side_effect": {"successes": duplicate, "rate": duplicate / total, "wilson_95_ci": wilson(duplicate, total)},
        "runs": reports,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    parser.add_argument("--agent-runs", required=True)
    parser.add_argument("--playwright-runs", required=True)
    parser.add_argument("--agent-version", required=True)
    parser.add_argument("--playwright-version", required=True)
    parser.add_argument("--minimum-repetitions", type=int, default=MINIMUM_REPETITIONS)
    args = parser.parse_args()
    if args.minimum_repetitions < 1:
        parser.error("--minimum-repetitions must be positive")
    try:
        agent = read_reports(args.agent_runs, "agent-browser", "agent-browser-process-recovery-browser-comparison.json", args.agent_version)
        playwright = read_reports(args.playwright_runs, "playwright-mcp", "playwright-mcp-process-recovery-comparison.json", args.playwright_version)
        if len(agent) < args.minimum_repetitions or len(playwright) < args.minimum_repetitions:
            raise ValueError(f"each arm requires at least {args.minimum_repetitions} complete runs; got agent-browser={len(agent)}, playwright-mcp={len(playwright)}")
    except (OSError, ValueError, json.JSONDecodeError) as error:
        parser.error(str(error))
    report = {
        "benchmark_version": "scout-browser-reliability-v28",
        "comparison_protocol_version": "scout-browser-comparison-v1",
        "fixture_version": "scout-browser-reliability-v28",
        "scenario_id": "committed-mutation-hard-browser-kill",
        "minimum_repetitions": args.minimum_repetitions,
        "pinned_versions": {"agent-browser": args.agent_version, "playwright-mcp": args.playwright_version},
        "fault_boundary": "full browser and tool process loss",
        "shared_predicate": "server-side mutation confirmed before kill; fresh process reopens; authoritative fixture count remains exactly one; storage-marker restore reported independently",
        "tool_results": {"agent-browser": summarize(agent), "playwright-mcp": summarize(playwright)},
        "unmeasured": ["native committed/rejected/unknown action classification", "native task-ID replay guard", "host-supplied idempotency", "latency parity"],
        "scout_task_store_browser_replacement": {"same_task_retry_blocked_before_dispatch": True, "host_reconciliation": "verified_complete", "source_output": "examples/benchmark/results/scout-browser-recovery-2026-11-06/durable-task-recovery.txt"},
        "comparability_note": "Agent-browser explicitly enables --restore and --restore-save always; its storage marker is measured separately from mutation outcome. Playwright MCP uses its documented persistent-profile lifecycle. Peer probes deliberately repeat a harmless UI click after restart and measure whether it duplicates the fixture side effect. Scout separately demonstrates a durable host task-ID reservation across Chrome replacement; this is complementary architecture evidence, not a matched native-tool comparison.",
        "interpretation": "Repeated probes compare fixture outcome persistence, browser-state restore, and the effect of a deliberate blind UI replay. They do not measure peer native task-ID guards or host-supplied idempotency.",
    }
    destination = pathlib.Path(args.output)
    destination.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
