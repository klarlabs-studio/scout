#!/usr/bin/env python3
"""Verify the source files recorded by a facilitator run report."""
import argparse
import hashlib
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", help="run_arm.py JSON evidence report")
    parser.add_argument("--workdir", help="checkout containing the captured files; defaults to report workdir")
    args = parser.parse_args()
    report_path = Path(args.report).resolve()
    report = json.loads(report_path.read_text(encoding="utf-8"))
    provenance = report.get("source_provenance")
    if not isinstance(provenance, dict) or provenance.get("manifest_algorithm") != "sha256":
        raise SystemExit("report has no supported source manifest")
    root = Path(args.workdir or report.get("workdir", ".")).resolve()
    checks = []
    for item in provenance.get("files", []):
        rel = Path(item["path"])
        path = (root / rel).resolve()
        if root not in path.parents or not path.is_file():
            checks.append({"path": item["path"], "status": "missing_or_unsafe"})
            continue
        hasher = hashlib.sha256()
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                hasher.update(chunk)
        digest = hasher.hexdigest()
        checks.append({
            "path": item["path"],
            "status": "match" if digest == item.get("sha256") and path.stat().st_size == item.get("size_bytes") else "changed",
        })
    result = {
        "report": str(report_path),
        "workdir": str(root),
        "manifest_files": len(checks),
        "verified": bool(checks) and all(item["status"] == "match" for item in checks),
        "failure_reason": "manifest is empty" if not checks else None,
        "changed_paths": [item["path"] for item in checks if item["status"] != "match"],
    }
    print(json.dumps(result, indent=2))
    return 0 if result["verified"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
