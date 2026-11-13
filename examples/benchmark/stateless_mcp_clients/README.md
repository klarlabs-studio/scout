# Stateless MCP client compatibility

This smoke starts Scout's opt-in stateless Streamable HTTP endpoint and uses
the independent official TypeScript and Python MCP clients to negotiate the
2026-07-28 protocol, list tools, call `list_sessions`, create a browser
session, navigate it to a local fixture, and read the fixture through that
session's opaque handle, cancel a pending wait, and read again through the same
handle. It also checks each JSON-RPC request's `Mcp-Method` routing header and
confirms neither side creates an `Mcp-Session-Id` transport session.

Run from any directory with Go, Node.js 22 or newer, npm, Python 3.13,
pip, and curl:

```sh
./examples/benchmark/stateless_mcp_clients/run.sh
```

The client packages are pinned to `@modelcontextprotocol/client` 2.4.0 and
`mcp` 2.3.0. Python transitive dependencies are hash-locked in
`requirements.lock`. This proves compatibility for the exercised protocol
negotiation, owner-level tool calls, session-bound browser actions, and
same-process request cancellation; it does not cover OAuth, multi-replica load
balancing, or every client SDK. See the [TypeScript v2 client guide](https://ts.sdk.modelcontextprotocol.io/v2/clients/connect)
and [Python client transport guide](https://py.sdk.modelcontextprotocol.io/client/transports/)
for the independent client APIs used here.

The older `@modelcontextprotocol/sdk` 1.32.2 client was also probed. Its
Streamable HTTP transport does not emit the 2026 `Mcp-Method` routing header,
so it cannot initialize against stateless mode. Keep stateful HTTP as the
compatibility path for clients that do not negotiate 2026-07-28; the stateless
mode remains opt-in.
