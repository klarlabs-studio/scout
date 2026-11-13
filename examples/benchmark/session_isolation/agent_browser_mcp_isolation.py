#!/usr/bin/env python3
"""Reproduce agent-browser's pinned MCP session override behavior on local fixtures."""
import argparse
import json
import os
import pathlib
import signal
import subprocess
import tempfile
import threading
import time


class MCPProcess:
    def __init__(self, version, server_session, socket_dir):
        env = os.environ.copy()
        env["AGENT_BROWSER_SESSION"] = server_session
        env["AGENT_BROWSER_SOCKET_DIR"] = socket_dir
        self.process = subprocess.Popen(
            ["npx", "--yes", f"agent-browser@{version}", "mcp"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
            env=env,
            start_new_session=True,
        )
        self._stderr = []
        self._stderr_thread = threading.Thread(target=self._drain_stderr, daemon=True)
        self._stderr_thread.start()
        self._next_id = 1
        self.transcript = []

    def _drain_stderr(self):
        for line in self.process.stderr:
            self._stderr.append(line)

    def request(self, method, params=None):
        request_id = self._next_id
        self._next_id += 1
        message = {"jsonrpc": "2.0", "id": request_id, "method": method}
        if params is not None:
            message["params"] = params
        self.process.stdin.write(json.dumps(message, separators=(",", ":")) + "\n")
        self.process.stdin.flush()
        self.transcript.append({"direction": "request", "message": message})
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            line = self.process.stdout.readline()
            if not line:
                raise RuntimeError("MCP server exited: " + "".join(self._stderr)[-2000:])
            response = json.loads(line)
            self.transcript.append({"direction": "response", "message": response})
            if response.get("id") == request_id:
                return response
        raise TimeoutError(f"MCP request {method} timed out")

    def initialize(self):
        result = self.request("initialize", {
            "protocolVersion": "2025-11-25",
            "capabilities": {},
            "clientInfo": {"name": "scout-session-isolation-benchmark", "version": "1.0"},
        })
        if "error" in result:
            raise RuntimeError("initialize failed: " + json.dumps(result["error"]))
        self.notify("notifications/initialized")
        return result["result"]

    def notify(self, method, params=None):
        message = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            message["params"] = params
        self.process.stdin.write(json.dumps(message, separators=(",", ":")) + "\n")
        self.process.stdin.flush()
        self.transcript.append({"direction": "request", "message": message})

    def call_tool(self, name, arguments):
        response = self.request("tools/call", {"name": name, "arguments": arguments})
        if "error" in response:
            raise RuntimeError(f"{name} failed: " + json.dumps(response["error"]))
        result = response["result"]
        if result.get("isError"):
            raise RuntimeError(f"{name} returned isError: " + json.dumps(result))
        return result

    def close(self):
        if self.process.poll() is None:
            try:
                os.killpg(self.process.pid, signal.SIGTERM)
                self.process.wait(timeout=5)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(self.process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                self.process.wait(timeout=5)


def first_text(result):
    for item in result.get("content", []):
        if item.get("type") == "text":
            return item.get("text", "")
    structured = result.get("structuredContent")
    if structured is not None:
        return json.dumps(structured, separators=(",", ":"))
    return ""


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--results", required=True)
    args = parser.parse_args()

    results = pathlib.Path(args.results)
    results.mkdir(parents=True, exist_ok=True)
    socket_dir = tempfile.mkdtemp(prefix="scout-agent-browser-mcp-")
    server_a = MCPProcess(args.version, "scout-mcp-a", socket_dir)
    server_b = MCPProcess(args.version, "scout-mcp-b", socket_dir)
    transcript = {"server_a": server_a.transcript, "server_b": server_b.transcript}
    report = {
        "tool_name": "agent-browser",
        "tool_version": args.version,
        "scenario_id": "mcp-server-session-override-isolation",
        "fixture_url": args.base_url + "/session-owner/seed",
        "server_sessions": ["scout-mcp-a", "scout-mcp-b"],
        "tool_session_argument": "shop",
        "success": False,
        "issue_reproduced": False,
        "verification": "Server A seeds primary cookie/localStorage. Server B opens the probe through the same explicit tool session. Server A then reads the browser URL and both servers read the probe page's initial state.",
    }
    try:
        initialized_a = server_a.initialize()
        initialized_b = server_b.initialize()
        tool_listing = server_a.request("tools/list", {})["result"]["tools"]
        relevant = {tool["name"]: tool for tool in tool_listing if tool["name"] in {
            "agent_browser_open", "agent_browser_get_text", "agent_browser_get_url", "agent_browser_close"
        }}
        report["server_info"] = initialized_a.get("serverInfo", {})
        report["server_info_b"] = initialized_b.get("serverInfo", {})
        report["session_schema_available"] = all(
            "session" in relevant.get(name, {}).get("inputSchema", {}).get("properties", {})
            for name in ("agent_browser_open", "agent_browser_get_text", "agent_browser_get_url")
        )
        if not report["session_schema_available"]:
            raise RuntimeError("pinned MCP tool schemas do not expose the session argument")
        session = report["tool_session_argument"]
        server_a.call_tool("agent_browser_open", {"url": args.base_url + "/session-owner/seed", "session": session})
        server_b.call_tool("agent_browser_open", {"url": args.base_url + "/session-owner/probe", "session": session})
        state_from_a = first_text(server_a.call_tool("agent_browser_get_text", {"selector": "#state", "session": session}))
        state_from_b = first_text(server_b.call_tool("agent_browser_get_text", {"selector": "#state", "session": session}))
        url_from_a = first_text(server_a.call_tool("agent_browser_get_url", {"session": session}))
        try:
            state_a = json.loads(state_from_a)
            state_b = json.loads(state_from_b)
        except ValueError:
            state_a, state_b = None, None
        leaked = state_a == ["scout_owner=primary", "primary"] and state_b == ["scout_owner=primary", "primary"] and url_from_a.rstrip("\"'") .endswith("/session-owner/probe")
        isolated = state_b == ["", None]
        report.update({
            "success": True,
            "issue_reproduced": leaked,
            "observed_isolation": isolated,
            "state_read_by_server_a": state_a,
            "state_read_by_server_b": state_b,
            "url_read_by_server_a": url_from_a,
            "interpretation": "Both MCP servers attached to the same browser session despite distinct AGENT_BROWSER_SESSION values." if leaked else "The reported cross-server session override was not reproduced by this pinned release and fixture." if isolated else "The run completed, but state was neither the expected leak nor the isolated-session state.",
        })
    finally:
        try:
            server_a.call_tool("agent_browser_close", {"session": "shop"})
        except Exception:
            pass
        server_a.close()
        server_b.close()
        (results / "agent-browser-mcp-session-override-transcript.json").write_text(json.dumps(transcript, indent=2) + "\n")
        (results / "agent-browser-mcp-session-override.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
