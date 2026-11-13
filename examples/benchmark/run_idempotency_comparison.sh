#!/usr/bin/env bash
# Compare repeated UI submission behavior when the application supplies an idempotency key.
# Usage: AGENT_BROWSER_VERSION=0.41.2 PLAYWRIGHT_MCP_VERSION=0.0.84 ./examples/benchmark/run_idempotency_comparison.sh
set -euo pipefail

agent_version="${AGENT_BROWSER_VERSION:-}"
playwright_version="${PLAYWRIGHT_MCP_VERSION:-}"
port="${BENCHMARK_PORT:-41890}"
if [[ -z "$agent_version" || -z "$playwright_version" ]]; then
  echo "set AGENT_BROWSER_VERSION and PLAYWRIGHT_MCP_VERSION to pinned versions" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
results="$root/examples/benchmark/results/$(date -u +%Y%m%dT%H%M%SZ)-application-idempotency-$$"
mkdir -p "$results/agent-browser" "$results/playwright-mcp"
base_url="http://127.0.0.1:$port"
server_pid=""
cleanup() { if [[ -n "$server_pid" ]]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi; }
trap cleanup EXIT

stop_server() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
    server_pid=""
  fi
}

start_server() {
  "$results/fixtureserver" -listen "127.0.0.1:$port" -idempotency-only >"$results/fixture-server.log" 2>&1 &
  server_pid=$!
  for _ in $(seq 1 100); do
    if curl -fsS "$base_url/idempotent-mutation-state" >/dev/null 2>&1; then return 0; fi
    sleep 0.1
  done
  cat "$results/fixture-server.log" >&2
  echo "fixture server failed readiness check" >&2
  return 1
}

cd "$root"
if curl -fsS "$base_url/idempotent-mutation-state" >/dev/null 2>&1; then
  echo "fixture port $port is already serving HTTP; choose another BENCHMARK_PORT" >&2
  exit 2
fi
go build -o "$results/fixtureserver" ./examples/benchmark/fixtureserver
start_server

python3 "$root/examples/benchmark/run_agent_browser.py" --version "$agent_version" --base-url "$base_url" --results "$results/agent-browser" --caller-idempotency-only
stop_server
start_server
npx --yes "@playwright/mcp@$playwright_version" --version >"$results/playwright-mcp/playwright-mcp.version.txt"
node "$root/examples/benchmark/run_playwright_idempotency.mjs" "$playwright_version" "$base_url" "$results/playwright-mcp"
echo "Benchmark artifacts: $results"
