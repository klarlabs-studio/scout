import asyncio
import json
import os

import httpx2
from mcp import Client
from mcp.client.streamable_http import streamable_http_client


endpoint = os.environ.get("SCOUT_MCP_COMPAT_URL", "http://127.0.0.1:43127/mcp")
token = os.environ.get("SCOUT_MCP_COMPAT_TOKEN")
fixture_url = os.environ.get("SCOUT_MCP_COMPAT_FIXTURE_URL", "http://127.0.0.1:43128/")
selector_key = "go.klarlabs.de/scout/session"
if not token:
    raise SystemExit("SCOUT_MCP_COMPAT_TOKEN must be set")

observed = []


async def audit_request(request):
    if request.method != "POST":
        return
    envelope = json.loads(await request.aread())
    method = envelope["method"]
    assert request.headers.get("mcp-method") == method, (
        f"Mcp-Method must match {method}"
    )
    assert "mcp-session-id" not in request.headers, (
        "stateless client must not send a transport session ID"
    )
    observed.append(method)


async def audit_response(response):
    assert "mcp-session-id" not in response.headers, (
        "stateless server must not mint a transport session ID"
    )


async def main():
    async with httpx2.AsyncClient(
        headers={"Authorization": f"Bearer {token}"},
        timeout=httpx2.Timeout(30, read=300),
        event_hooks={"request": [audit_request], "response": [audit_response]},
    ) as http:
        async with Client(
            streamable_http_client(endpoint, http_client=http)
        ) as client:
            tools = await client.list_tools()
            assert any(tool.name == "list_sessions" for tool in tools.tools)
            sessions = await client.call_tool("list_sessions", {})
            assert not sessions.is_error, "owner-level list_sessions call failed"

            created = await client.call_tool("create_session", {"name": "python-compat"})
            assert not created.is_error, "create_session failed"
            created_text = "\n".join(
                item.text for item in created.content if hasattr(item, "text")
            )
            import re

            match = re.search(r"session_id:\s*(scs:[A-Za-z0-9_-]+)", created_text)
            assert match, f"create_session should return an opaque session_id: {created_text}"
            session_id = match.group(1)
            try:
                meta = {selector_key: session_id}
                navigation = await client.call_tool(
                    "navigate", {"url": fixture_url}, meta=meta
                )
                assert not navigation.is_error, f"navigate failed: {navigation.content}"
                page = await client.call_tool("readable_text", {}, meta=meta)
                assert not page.is_error, f"readable_text failed: {page.content}"
                assert "scout-sdk-session-bound-read" in str(page), (
                    "readable_text should return content from the fixture in the selected session"
                )
                pending_read = asyncio.create_task(
                    client.call_tool("wait_for", {"selector": "#never-present"}, meta=meta)
                )
                await asyncio.sleep(0.25)
                pending_read.cancel()
                try:
                    await pending_read
                except asyncio.CancelledError:
                    pass
                else:
                    raise AssertionError("cancelled tool call should raise asyncio.CancelledError")
                after_cancel = await client.call_tool("readable_text", {}, meta=meta)
                assert not after_cancel.is_error, (
                    "session should remain usable after request cancellation"
                )
                assert "scout-sdk-session-bound-read" in str(after_cancel), (
                    "the same session should still read the page after cancellation"
                )
            finally:
                closed = await client.call_tool(
                    "close_session", {"name": session_id}
                )
                assert not closed.is_error, "close_session should clean up the browser session"
            assert "server/discover" in observed, "modern protocol was not negotiated"
            assert "tools/list" in observed
            assert "tools/call" in observed
            print(
                json.dumps(
                    {
                        "client": "mcp==2.3.0",
                        "protocol": client.protocol_version,
                        "methods": observed,
                        "tool_count": len(tools.tools),
                        "list_sessions_succeeded": True,
                        "browser_session_actions_succeeded": True,
                        "request_cancellation_succeeded": True,
                        "stateless_headers_valid": True,
                    },
                    indent=2,
                )
            )


asyncio.run(main())
