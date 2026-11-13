#!/usr/bin/env bash
# Probe Playwright MCP process-loss recovery against the shared mutation fixture.
set -euo pipefail

version="${PLAYWRIGHT_MCP_VERSION:-}"
port="${BENCHMARK_PORT:-41741}"
if [[ -z "$version" ]]; then
  echo "set PLAYWRIGHT_MCP_VERSION to an explicit npm version (for example 0.0.84)" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-playwright-mcp-${version}-process-recovery-$$"
results="$root/examples/benchmark/results/$run_id"
mkdir -p "$results"
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
"$results/fixtureserver" -listen "127.0.0.1:$port" -process-recovery >"$results/fixture-server.log" 2>&1 &
server_pid=$!
ready=0
for _ in $(seq 1 100); do
  if curl -fsS "$base_url/process-recovery-state" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.1
done
if (( ready == 0 )); then
  cat "$results/fixture-server.log" >&2
  echo "fixture server failed readiness check" >&2
  exit 1
fi

npx --yes "@playwright/mcp@$version" --version >"$results/playwright-mcp.version.txt"
node ./examples/benchmark/run_playwright_process_recovery.mjs "$version" "$base_url" "$results"
echo "Benchmark artifacts: $results"
