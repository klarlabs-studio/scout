#!/usr/bin/env python3
"""Summarize process RSS reports from process_rss_sampler.mjs."""
import json
import pathlib
import statistics
import sys

if len(sys.argv) < 3:
    raise SystemExit("usage: memory_summary.py <tool> <report.json> [report.json ...]")

tool = sys.argv[1]
paths = [pathlib.Path(value) for value in sys.argv[2:]]
reports = [json.loads(path.read_text()) for path in paths]
values = sorted(report["peak_rss_kb"] for report in reports)
summary = {
    "tool": tool,
    "repetitions": len(reports),
    "successful_runs": sum(report.get("exit_code") == 0 for report in reports),
    "sample_interval_ms": sorted({report["sample_interval_ms"] for report in reports}),
    "sampling_scope": sorted({report["sampling_scope"] for report in reports}),
    "peak_rss_kb": {
        "min": values[0],
        "median": statistics.median(values),
        "max": values[-1],
        "runs": values,
    },
    "category_peak_rss_kb": {
        key: {
            "min": min(report.get("category_peaks_kb", {}).get(key, 0) for report in reports),
            "median": statistics.median(report.get("category_peaks_kb", {}).get(key, 0) for report in reports),
            "max": max(report.get("category_peaks_kb", {}).get(key, 0) for report in reports),
            "runs": [report.get("category_peaks_kb", {}).get(key, 0) for report in reports],
        }
        for key in sorted({key for report in reports for key in report.get("category_peaks_kb", {})})
    },
    "source_reports": [str(path) for path in paths],
    "reported_by_sampler": "runner plus selected workload commands; see each raw report for exact command signatures",
    "caveat": "ps RSS sums process resident sizes and can double-count shared pages; 50 ms samples can miss short-lived peaks",
}
print(json.dumps(summary, indent=2))
