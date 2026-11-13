#!/usr/bin/env bash
# Compare Scout and agent-browser on the shared verified-click fixture.
# Usage: AGENT_BROWSER_VERSION=0.41.2 ./examples/benchmark/run_agent_browser.sh
set -euo pipefail

version="${AGENT_BROWSER_VERSION:-}"
repetitions="${BENCHMARK_REPETITIONS:-10}"
scenario="${BENCHMARK_SCENARIO:-full}"
port="${BENCHMARK_PORT:-41731}"
if [[ -z "$version" ]]; then
  echo "set AGENT_BROWSER_VERSION to an explicit npm version (for example 0.41.2)" >&2
  exit 2
fi
if ! [[ "$repetitions" =~ ^[0-9]+$ ]] || (( repetitions < 10 )); then
  echo "BENCHMARK_REPETITIONS must be an integer of at least 10" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
results="$root/examples/benchmark/results"
run_id="$(date -u +%Y%m%dT%H%M%SZ)-agent-browser-${version}-${scenario}-$$"
results="$results/$run_id"
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
if curl -fsS "$base_url/healthz" >/dev/null 2>&1; then
  echo "fixture port $port is already serving HTTP; choose another BENCHMARK_PORT" >&2
  exit 2
fi
go build -o "$results/fixtureserver" ./examples/benchmark/fixtureserver
"$results/fixtureserver" -listen "127.0.0.1:$port" >"$server_log" 2>&1 &
server_pid=$!
ready=0
for _ in $(seq 1 100); do
  if curl -fsS "$base_url/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.1
done
if (( ready == 0 )); then
  cat "$server_log" >&2
  echo "fixture server failed readiness check" >&2
  exit 1
fi

if [[ "$scenario" == "verified-action" ]]; then
  SCOUT_BENCHMARK_FOCUS=verified-action SCOUT_BENCHMARK_BASE_URL="$base_url" go run ./examples/benchmark >"$results/scout-benchmark.json"
  cp "$results/scout-benchmark.json" "$results/scout-go-api-verified-action-comparison.json"
elif [[ "$scenario" == "background-tab" ]]; then
  SCOUT_BENCHMARK_FOCUS=background-tab-action SCOUT_BENCHMARK_REPEATS="$repetitions" SCOUT_BENCHMARK_BASE_URL="$base_url" go run ./examples/benchmark >"$results/scout-benchmark.json"
  cp "$results/scout-benchmark.json" "$results/scout-background-tab-comparison.json"
  python3 ./examples/benchmark/run_agent_browser.py --version "$version" --repetitions "$repetitions" --base-url "$base_url" --results "$results" --scout-report "$results/scout-background-tab-comparison.json" --background-only
elif [[ "$scenario" == "frame-close" ]]; then
  SCOUT_BENCHMARK_BASE_URL="$base_url" go run ./examples/benchmark/frame_close >"$results/scout-frame-close-comparison.json"
  python3 ./examples/benchmark/run_agent_browser.py --version "$version" --repetitions "$repetitions" --base-url "$base_url" --results "$results" --scout-report "$results/scout-frame-close-comparison.json" --frame-close-only
elif [[ "$scenario" == "late-recovery" ]]; then
  SCOUT_BENCHMARK_FOCUS=late-action-cancellation go run ./examples/benchmark >"$results/scout-late-action-cancellation.json"
  python3 - "$results/scout-late-action-cancellation.json" "$results/scout-late-action-cancellation-comparison.json" <<'PY'
import json, sys
source, destination = sys.argv[1:]
report = json.load(open(source))
report.update({
    "scenario_id": "late-action-caller-timeout",
    "fixture_url": "local deterministic HandlerWithLateActionSignals /late-action",
    "caller_deadline_ms_after_confirmed_dispatch": 300,
    "verification_predicate": "confirmed click dispatch; caller wait ends at 300 ms before mutation completion; after late completion, #late-status text equals 'Late completion'",
    "native_recovery_record": True,
    "recovery_method": "Scout returns a target-bound ActionOutcomeError with the expected postcondition; it reconciles the late effect on the original target without replay and refuses reconciliation after target change",
})
with open(destination, "w") as output:
    json.dump(report, output, indent=2)
    output.write("\n")
PY
  cp "$results/scout-late-action-cancellation-comparison.json" "$results/scout-benchmark.json"
else
  go run ./examples/benchmark >"$results/scout-benchmark.json"
fi
runner_args=()
if [[ "$scenario" == "verified-action" ]]; then runner_args+=(--verified-action-only); fi
if [[ "$scenario" == "late-recovery" ]]; then runner_args+=(--late-recovery-only); fi
if [[ "$scenario" == "background-tab" ]]; then runner_args+=(--background-only); fi
if [[ "$scenario" == "frame-close" ]]; then runner_args+=(--frame-close-only); fi
python3 ./examples/benchmark/run_agent_browser.py \
  --version "$version" \
  --repetitions "$repetitions" \
  --base-url "$base_url" \
  --results "$results" \
  --scout-report "$results/scout-benchmark.json" \
  "${runner_args[@]}"
echo "Benchmark artifacts: $results"
