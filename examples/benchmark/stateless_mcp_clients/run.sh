#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
compat_dir="$root/examples/benchmark/stateless_mcp_clients"
listen="${SCOUT_MCP_COMPAT_LISTEN:-127.0.0.1:43127}"
fixture_listen="${SCOUT_MCP_COMPAT_FIXTURE_LISTEN:-127.0.0.1:43128}"
host="${listen%:*}"
port="${listen##*:}"
fixture_host="${fixture_listen%:*}"
fixture_port="${fixture_listen##*:}"
tmp="$(mktemp -d)"
server_pid=""
fixture_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  if [[ -n "$fixture_pid" ]]; then
    kill "$fixture_pid" 2>/dev/null || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

(cd "$root" && go build -o "$tmp/scout" ./cmd/scout)
token="scout-compat-$(python3 -c 'import secrets; print(secrets.token_urlsafe(24))')"
principal_policy="$(python3 - "$token" "http://$fixture_host:$fixture_port" <<'PY'
import json
import sys

print(json.dumps({"compat": {
    "token": sys.argv[1],
    "remote_cdp": "ws://127.0.0.1:9222/devtools/browser/compatibility-probe",
    "allowed_origins": [sys.argv[2]],
    "allow_private_ips": True,
}}))
PY
)"

(cd "$compat_dir" && npm ci --ignore-scripts --no-audit --no-fund)
python3 -m pip install --disable-pip-version-check --require-hashes \
  --requirement "$compat_dir/requirements.lock"
python3 -m http.server "$fixture_port" --bind "$fixture_host" \
  --directory "$compat_dir/fixture" >"$tmp/fixture.log" 2>&1 &
fixture_pid=$!
fixture_ready=false
for _ in $(seq 1 100); do
  code="$(curl --silent --output /dev/null --write-out '%{http_code}' "http://$fixture_host:$fixture_port/" || true)"
  if [[ "$code" == "200" ]]; then
    fixture_ready=true
    break
  fi
  sleep 0.1
done
if [[ "$fixture_ready" != true ]]; then
  cat "$tmp/fixture.log" >&2
  echo "Compatibility fixture did not become ready at $fixture_listen" >&2
  exit 1
fi
SCOUT_MCP_HTTP_STATELESS=true \
SCOUT_MCP_HTTP_PRINCIPALS_JSON="$principal_policy" \
  "$tmp/scout" mcp serve-http --listen="$listen" --profile=all \
  >"$tmp/server.log" 2>&1 &
server_pid=$!

ready=false
for _ in $(seq 1 100); do
  code="$(curl --silent --output /dev/null --write-out '%{http_code}' "http://$host:$port/mcp" || true)"
  if [[ "$code" != "000" ]]; then
    ready=true
    break
  fi
  sleep 0.1
done
if [[ "$ready" != true ]]; then
  cat "$tmp/server.log" >&2
  echo "Scout MCP server did not become ready at $listen" >&2
  exit 1
fi

SCOUT_MCP_COMPAT_URL="http://$host:$port/mcp" \
SCOUT_MCP_COMPAT_TOKEN="$token" \
SCOUT_MCP_COMPAT_FIXTURE_URL="http://$fixture_host:$fixture_port/" \
  npm --prefix "$compat_dir" test

SCOUT_MCP_COMPAT_URL="http://$host:$port/mcp" \
SCOUT_MCP_COMPAT_TOKEN="$token" \
SCOUT_MCP_COMPAT_FIXTURE_URL="http://$fixture_host:$fixture_port/" \
  python3 "$compat_dir/compatibility.py"
