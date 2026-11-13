#!/usr/bin/env bash
# Probe agent-browser restore behavior after a hard daemon/browser kill.
# Usage: AGENT_BROWSER_VERSION=0.41.2 ./examples/benchmark/run_agent_browser_process_recovery.sh
set -euo pipefail

version="${AGENT_BROWSER_VERSION:-}"
port="${BENCHMARK_PORT:-41739}"
fault="${BENCHMARK_FAULT_BOUNDARY:-browser}"
if [[ "$fault" != "daemon" && "$fault" != "browser" ]]; then
  echo "BENCHMARK_FAULT_BOUNDARY must be daemon or browser" >&2
  exit 2
fi
if [[ -z "$version" ]]; then
  echo "set AGENT_BROWSER_VERSION to an explicit npm version (for example 0.41.2)" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
results_root="$root/examples/benchmark/results"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-agent-browser-${version}-process-recovery-$$"
results="$results_root/$run_id"
mkdir -p "$results"
server_log="$results/fixture-server.log"
base_url="http://127.0.0.1:$port"
server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT

cd "$root"
if curl -fsS "$base_url/process-recovery-state" >/dev/null 2>&1; then
  echo "fixture port $port is already serving HTTP; choose another BENCHMARK_PORT" >&2
  exit 2
fi
go build -o "$results/fixtureserver" ./examples/benchmark/fixtureserver
"$results/fixtureserver" -listen "127.0.0.1:$port" -process-recovery >"$server_log" 2>&1 &
server_pid=$!
ready=0
for _ in $(seq 1 100); do
  if curl -fsS "$base_url/process-recovery-state" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.1
done
if (( ready == 0 )); then
  cat "$server_log" >&2
  echo "fixture server failed readiness check" >&2
  exit 1
fi

python3 ./examples/benchmark/run_agent_browser.py \
  --version "$version" \
  --base-url "$base_url" \
  --results "$results" \
  --process-recovery-only \
  --process-recovery-fault "$fault"
echo "Benchmark artifacts: $results"
