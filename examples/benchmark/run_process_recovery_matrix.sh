#!/usr/bin/env bash
# Run isolated full-browser-loss trials for agent-browser and Playwright MCP.
# Usage: BENCHMARK_REPETITIONS=10 AGENT_BROWSER_VERSION=0.41.2 PLAYWRIGHT_MCP_VERSION=0.0.84 ./examples/benchmark/run_process_recovery_matrix.sh
set -euo pipefail

repetitions="${BENCHMARK_REPETITIONS:-10}"
agent_version="${AGENT_BROWSER_VERSION:-}"
playwright_version="${PLAYWRIGHT_MCP_VERSION:-}"
base_port="${BENCHMARK_PORT:-41800}"
if ! [[ "$repetitions" =~ ^[0-9]+$ ]] || (( repetitions < 30 )); then
  echo "BENCHMARK_REPETITIONS must be an integer of at least 30 for a conformance report" >&2
  exit 2
fi
if [[ -z "$agent_version" || -z "$playwright_version" ]]; then
  echo "set AGENT_BROWSER_VERSION and PLAYWRIGHT_MCP_VERSION to pinned versions" >&2
  exit 2
fi
if (( base_port + repetitions * 2 >= 65535 )); then
  echo "BENCHMARK_PORT range exceeds valid TCP ports" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
results="$root/examples/benchmark/results/$(date -u +%Y%m%dT%H%M%SZ)-process-recovery-matrix-$$"
mkdir -p "$results"
agent_dirs="$results/agent-browser-runs.txt"
playwright_dirs="$results/playwright-mcp-runs.txt"
: >"$agent_dirs"
: >"$playwright_dirs"

run_and_capture() {
  local output="$1"
  shift
  if ! "$@" >"$output" 2>&1; then
    cat "$output" >&2
    return 1
  fi
  cat "$output" >&2
  sed -n 's/^Benchmark artifacts: //p' "$output" | tail -1
}

for ((run = 1; run <= repetitions; run++)); do
  agent_port=$((base_port + run * 2))
  playwright_port=$((agent_port + 1))
  agent_dir="$(run_and_capture "$results/agent-browser-$run.log" env \
    AGENT_BROWSER_VERSION="$agent_version" BENCHMARK_PORT="$agent_port" \
    BENCHMARK_FAULT_BOUNDARY=daemon \
    "$root/examples/benchmark/run_agent_browser_process_recovery.sh")"
  [[ -n "$agent_dir" ]] || { echo "could not locate agent-browser run artifacts" >&2; exit 1; }
  printf '%s\n' "$agent_dir" >>"$agent_dirs"

  playwright_dir="$(run_and_capture "$results/playwright-mcp-$run.log" env \
    PLAYWRIGHT_MCP_VERSION="$playwright_version" BENCHMARK_PORT="$playwright_port" \
    "$root/examples/benchmark/run_playwright_process_recovery.sh")"
  [[ -n "$playwright_dir" ]] || { echo "could not locate Playwright MCP run artifacts" >&2; exit 1; }
  printf '%s\n' "$playwright_dir" >>"$playwright_dirs"
  echo "Completed process-recovery repetition $run/$repetitions"
done

python3 "$root/examples/benchmark/aggregate_process_recovery.py" \
  --output "$results/process-recovery-matrix.json" \
  --agent-runs "$agent_dirs" \
  --playwright-runs "$playwright_dirs" \
  --agent-version "$agent_version" \
  --playwright-version "$playwright_version"
echo "Aggregate: $results/process-recovery-matrix.json"
