import assert from 'node:assert/strict';
import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';

const endpoint = process.env.SCOUT_MCP_COMPAT_URL ?? 'http://127.0.0.1:43127/mcp';
const token = process.env.SCOUT_MCP_COMPAT_TOKEN;
const fixtureURL = process.env.SCOUT_MCP_COMPAT_FIXTURE_URL ?? 'http://127.0.0.1:43128/';
const selectorKey = 'go.klarlabs.de/scout/session';
assert.ok(token, 'SCOUT_MCP_COMPAT_TOKEN must be set');

const observed = [];
const auditedFetch = async (input, init) => {
  const request = new Request(input, init);
  if (request.method === 'POST') {
    const envelope = await request.clone().json();
    assert.equal(request.headers.get('mcp-method'), envelope.method,
      `Mcp-Method must match ${envelope.method}`);
    assert.equal(request.headers.has('mcp-session-id'), false,
      'stateless client must not send a transport session ID');
    observed.push(envelope.method);
  }
  const response = await fetch(request);
  assert.equal(response.headers.has('mcp-session-id'), false,
    'stateless server must not mint a transport session ID');
  return response;
};

const client = new Client(
  { name: 'scout-stateless-compatibility', version: '1.0.0' },
  { versionNegotiation: { mode: { pin: '2026-07-28' } } },
);
const transport = new StreamableHTTPClientTransport(new URL(endpoint), {
  requestInit: { headers: { Authorization: `Bearer ${token}` } },
  fetch: auditedFetch,
});

try {
  await client.connect(transport);
  const { tools } = await client.listTools();
  assert.ok(tools.length > 0, 'server should expose tools');
  assert.ok(tools.some(({ name }) => name === 'list_sessions'),
    'server should expose the owner-level list_sessions tool');

  const sessions = await client.callTool({ name: 'list_sessions', arguments: {} });
  assert.notEqual(sessions.isError, true, 'owner-level list_sessions call should succeed');

  let sessionId;
  try {
    const created = await client.callTool({ name: 'create_session', arguments: { name: 'typescript-compat' } });
    assert.notEqual(created.isError, true, 'create_session should succeed');
    const createdText = created.content.find((item) => item.type === 'text')?.text ?? '';
    sessionId = createdText.match(/session_id:\s*(scs:[A-Za-z0-9_-]+)/)?.[1];
    assert.ok(sessionId, `create_session should return an opaque session_id: ${createdText}`);

    const withSession = (name, args, options) => client.callTool({
      name,
      arguments: args,
      _meta: { [selectorKey]: sessionId },
    }, options);
    const navigation = await withSession('navigate', { url: fixtureURL });
    assert.notEqual(navigation.isError, true, `navigate should succeed: ${JSON.stringify(navigation.content)}`);
    const page = await withSession('readable_text', {});
    assert.notEqual(page.isError, true, `readable_text should succeed: ${JSON.stringify(page.content)}`);
    assert.match(JSON.stringify(page), /scout-sdk-session-bound-read/,
      'readable_text should return content from the fixture in the selected session');

    const controller = new AbortController();
    const pendingRead = withSession('wait_for', { selector: '#never-present' }, { signal: controller.signal });
    setTimeout(() => controller.abort(), 250);
    await assert.rejects(pendingRead, (error) => error.code === 'REQUEST_TIMEOUT' && /AbortError/.test(error.message),
      'aborting an in-flight stateless tool call should surface the SDK cancellation error');
    const afterCancel = await withSession('readable_text', {});
    assert.notEqual(afterCancel.isError, true, 'session should remain usable after request cancellation');
    assert.match(JSON.stringify(afterCancel), /scout-sdk-session-bound-read/,
      'the same session should still read the page after cancellation');
  } finally {
    if (sessionId) {
      const closed = await client.callTool({ name: 'close_session', arguments: { name: sessionId } });
      assert.notEqual(closed.isError, true, 'close_session should clean up the browser session');
    }
  }
  assert.ok(observed.includes('server/discover'), 'client should negotiate the modern protocol');
  assert.ok(observed.includes('tools/list'), 'client should list tools');
  assert.ok(observed.includes('tools/call'), 'client should call a tool');
  console.log(JSON.stringify({
    client: '@modelcontextprotocol/client@2.4.0',
    protocol: '2026-07-28',
    methods: observed,
    toolCount: tools.length,
    listSessionsSucceeded: true,
    browserSessionActionsSucceeded: true,
    requestCancellationSucceeded: true,
    statelessHeadersValid: true,
  }, null, 2));
} finally {
  await client.close();
}
