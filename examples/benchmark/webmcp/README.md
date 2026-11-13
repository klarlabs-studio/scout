# WebMCP smoke fixture

This local smoke probe checks that a page-registered tool can be discovered,
invoked, and independently verified through agent-browser. It uses the shared
`/action` benchmark page and injects a deterministic `complete_task` WebMCP
tool with [`init.js`](init.js).

On 6 November 2026, the installed versions were agent-browser 0.41.2 and
Chrome 157.0.8103.38. A default managed launch did not expose
`document.modelContext` with the pinned agent-browser build's
`WebMCPTesting` launch feature. Passing the documented Chrome flag explicitly
made the API available. The tool then appeared in `webmcp list` with
`untrusted: true`, invocation returned `completed`, and an independent DOM
read returned `Task complete`.

Playwright MCP 0.0.84 completed the same one-run fixture task on Chrome
157.0.8103.38. After `browser_snapshot`, it exposed a dynamic
`webmcp_complete_task` MCP tool. The MCP description and result text marked
page-provided metadata/output as untrusted, and the independent DOM check
confirmed the same `Task complete` state. Replay the Playwright path with:

```sh
go build -o /tmp/scout-webmcp-fixtureserver ./examples/benchmark/fixtureserver
/tmp/scout-webmcp-fixtureserver -listen 127.0.0.1:41891 &
fixture_pid=$!
node examples/benchmark/webmcp/run_playwright_mcp_smoke.mjs \
  0.0.84 http://127.0.0.1:41891 \
  examples/benchmark/webmcp/playwright-mcp-0.0.84-smoke.json
kill "$fixture_pid"
wait "$fixture_pid" 2>/dev/null || true
```

Reproduce from the repository root (uses port 41891):

```sh
go build -o /tmp/scout-webmcp-fixtureserver ./examples/benchmark/fixtureserver
/tmp/scout-webmcp-fixtureserver -listen 127.0.0.1:41891 &
fixture_pid=$!
agent-browser close --all
agent-browser --args "--enable-features=WebMCP" \
  --session scout-webmcp-smoke \
  --init-script "$PWD/examples/benchmark/webmcp/init.js" \
  open http://127.0.0.1:41891/action
agent-browser --session scout-webmcp-smoke webmcp list --json
agent-browser --session scout-webmcp-smoke \
  webmcp invoke complete_task --params '{}'
agent-browser --session scout-webmcp-smoke \
  eval "document.querySelector('#status').textContent"
agent-browser --session scout-webmcp-smoke close
kill "$fixture_pid"
wait "$fixture_pid" 2>/dev/null || true
```

This is one deterministic interface smoke run, not a matched performance or
reliability comparison. The tool is injected by the harness, and the browser
flag is experimental. The next comparison should use ordinary accessibility-
tree fallback as a control, then add delayed invocation and malicious metadata
with independent grading and repeated actual-agent runs. Structured observations
for both competitor paths are saved in [`observed-result.json`](observed-result.json)
and [`playwright-mcp-0.0.84-smoke.json`](playwright-mcp-0.0.84-smoke.json).

## Scout live bridge smoke

Scout's bridge passed one live Chrome 157 run through `task_run`. The MCP
catalog discovered the injected tool, the verified task invoked it with a
host-declared postcondition, a same-ID retry returned the retained result
without a second page mutation, and the exported recovery receipt kept the
catalogued frame identity while denying replay. The stored recovery manifest
retained the tool/frame names and excluded its input. The runner independently
read the fixture state through CDP. The compact result is
[`scout-webmcp-live-smoke.json`](scout-webmcp-live-smoke.json). This is an
integration smoke, not a reliability estimate; it establishes parity through
Scout's task/recovery contract rather than a unique WebMCP capability.

With Chrome and Node installed, run the local fixture and a dedicated headless
Chrome instance in separate terminals:

```sh
go build -o /tmp/scout-webmcp-fixtureserver ./examples/benchmark/fixtureserver
go build -o /tmp/scout-webmcp ./cmd/scout
```

```sh
/tmp/scout-webmcp-fixtureserver -listen 127.0.0.1:41891
```

```sh
/Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome \
  --headless=new --no-sandbox --remote-debugging-port=49222 \
  --user-data-dir=/tmp/scout-webmcp-chrome \
  --enable-features=WebMCP about:blank
```

Then run the MCP smoke adapter; it installs the fixture tool in the current
page, paginates `tools/list`, invokes Scout's catalog and invoke tools, and
independently grades the page through CDP:

```sh
node examples/benchmark/webmcp/run_scout_webmcp_smoke.mjs \
  /tmp/scout-webmcp http://127.0.0.1:49222 http://127.0.0.1:41891 \
  examples/benchmark/webmcp/scout-webmcp-live-smoke.json
```

The runner sets `SCOUT_REMOTE_CDP` and enables loopback for this local fixture.
Use a fresh temporary Chrome profile for a clean run. Chrome's WebMCP domain is
experimental and the fixture injects its page tool; this smoke does not test
site adoption, origin-denied iframe behavior, delayed calls, hostile metadata,
or real-agent tool selection.
