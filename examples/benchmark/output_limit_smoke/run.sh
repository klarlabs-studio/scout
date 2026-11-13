#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
port="${OUTPUT_LIMIT_PORT:-41732}"
sample_interval_ms="${OUTPUT_LIMIT_SAMPLE_INTERVAL_MS:-50}"
report="${OUTPUT_LIMIT_REPORT:-$root/examples/benchmark/results/mcp-output-limit-memory.json}"
contract_report="${OUTPUT_LIMIT_CONTRACT_REPORT:-$root/examples/benchmark/reports/scout-mcp-output-budget-live-$(date -u +%Y-%m-%d).json}"
tmp_dir="$(mktemp -d)"
fixture_pid=""
client_pid=""

cleanup() {
  if [[ -n "$client_pid" ]]; then
    kill "$client_pid" 2>/dev/null || true
    wait "$client_pid" 2>/dev/null || true
  fi
  if [[ -n "$fixture_pid" ]]; then
    kill "$fixture_pid" 2>/dev/null || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

if ! [[ "$sample_interval_ms" =~ ^[1-9][0-9]*$ ]]; then
  echo "OUTPUT_LIMIT_SAMPLE_INTERVAL_MS must be a positive integer" >&2
  exit 2
fi

mkdir -p "$(dirname "$report")"
cd "$root"

go build -o "$tmp_dir/scout" ./cmd/scout
go build -o "$tmp_dir/fixture" ./examples/benchmark/fixtureserver
go build -o "$tmp_dir/smoke" ./examples/benchmark/output_limit_smoke

"$tmp_dir/fixture" -listen "127.0.0.1:$port" >"$tmp_dir/fixture.log" 2>&1 &
fixture_pid=$!
for _ in $(seq 1 100); do
  if curl --silent --fail "http://127.0.0.1:$port/healthz" >/dev/null; then
    break
  fi
  sleep 0.05
done
if ! curl --silent --fail "http://127.0.0.1:$port/healthz" >/dev/null; then
  cat "$tmp_dir/fixture.log" >&2
  echo "fixture did not become healthy on port $port" >&2
  exit 1
fi

SCOUT_MCP_BINARY="$tmp_dir/scout" SCOUT_BENCHMARK_OUTPUT_OVERFLOW=1 OUTPUT_LIMIT_BASE_URL="http://127.0.0.1:$port" \
  "$tmp_dir/smoke" >"$tmp_dir/smoke.log" 2>&1 &
client_pid=$!
peak_rss_kb=0
while kill -0 "$client_pid" 2>/dev/null; do
  sampled_rss_kb="$(ps -axo rss=,command= | awk -v binary="$tmp_dir/scout" 'index($0, binary) && / mcp serve/ { if ($1 > peak) peak = $1 } END { print peak + 0 }')"
  if (( sampled_rss_kb > peak_rss_kb )); then
    peak_rss_kb=$sampled_rss_kb
  fi
  sleep "$(python3 -c 'import sys; print(int(sys.argv[1]) / 1000)' "$sample_interval_ms")"
done
if ! wait "$client_pid"; then
  cat "$tmp_dir/smoke.log" >&2
  exit 1
fi
client_pid=""
cat "$tmp_dir/smoke.log"

if (( peak_rss_kb <= 0 )); then
  echo "could not observe the Scout MCP server process in ps output" >&2
  exit 1
fi

platform="$(uname -s)"
architecture="$(uname -m)"
go_version="$(go version)"
python3 - "$report" "$contract_report" "$peak_rss_kb" "$sample_interval_ms" "$platform" "$architecture" "$go_version" "$tmp_dir/smoke.log" <<'PY'
import datetime
import json
import pathlib
import sys

report_path, contract_report_path, rss_kb, interval_ms, platform, architecture, go_version, output_path = sys.argv[1:]
output = pathlib.Path(output_path).read_text().strip()
payload = {
    "schema_version": 1,
    "benchmark": "mcp-output-limit-smoke",
    "captured_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "platform": platform,
    "architecture": architecture,
    "go_version": go_version,
    "sample_interval_ms": int(interval_ms),
    "mcp_server_peak_rss_bytes": int(rss_kb) * 1024,
    "mcp_server_peak_rss_kb": int(rss_kb),
    "memory_scope": "standalone Scout MCP server process only; excludes Chrome, fixture server, and smoke client",
    "sampling_method": "ps RSS sampled while the smoke client was active; peaks shorter than the interval may be missed",
    "smoke_output": output,
}
path = pathlib.Path(report_path)
path.write_text(json.dumps(payload, indent=2) + "\n")
print(f"mcp_server_peak_rss={payload['mcp_server_peak_rss_kb']} KiB ({payload['mcp_server_peak_rss_bytes']} bytes)")
print(f"memory_report={path}")

smoke = output.removeprefix("output_limit_smoke=passed ")
measurements = {}
for field in smoke.split():
    key, value = field.split("=", 1)
    if value in {"true", "false"}:
        measurements[key] = value == "true"
    else:
        measurements[key] = int(value)
contract = {
    "schema_version": 1,
    "benchmark": "scout-mcp-output-budget-live",
    "captured_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "source": "current worktree",
    "platform": f"{platform.lower()}/{architecture}",
    "go_version": go_version,
    "mcp_response_limit_bytes": 256 * 1024,
    "verified": True,
    "raw_smoke_output": output,
    "measurements": measurements,
    "checks": {
        "bounded_extract_has_omission_metadata": measurements["extracted_items"] == 8 and measurements["extracted_chars_per_item"] == 4000,
        "oversized_success_replaced_by_error": measurements["overflow_is_error"] and measurements["overflow_replacement_bytes"] <= 256 * 1024,
        "compacted_batch_retains_valid_recovery_record": measurements["batch_recovery_valid"],
        "snapshot_range_and_search_retrieval": measurements["snapshot_ranges"] == 2 and measurements["search_matches"] == 1,
        "network_pages_stay_under_response_limit": measurements["paged_requests"] == 12 and measurements["paged_responses"] == 4 and measurements["max_page_bytes"] <= 256 * 1024,
    },
    "limitations": [
        "Single live Scout run; no pinned competitor adapter was run.",
        "Does not test unknown or deprecated input handling across products.",
        "RSS sampling can miss peaks shorter than the sample interval.",
    ],
    "memory_report": path.relative_to(pathlib.Path.cwd()).as_posix(),
}
contract["verified"] = all(contract["checks"].values())
contract_path = pathlib.Path(contract_report_path)
contract_path.parent.mkdir(parents=True, exist_ok=True)
contract_path.write_text(json.dumps(contract, indent=2) + "\n")
print(f"contract_report={contract_path}")
if not contract["verified"]:
    raise SystemExit("output budget contract report contains a failed predicate")
PY
