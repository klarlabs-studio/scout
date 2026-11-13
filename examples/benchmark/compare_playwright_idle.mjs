#!/usr/bin/env node
// Repeated probe for Playwright MCP HTTP-client idle timer scope.
// Usage: node examples/benchmark/compare_playwright_idle.mjs <base-url> [version] [isolated|shared] [repetitions] [timeout-ms]
import { spawn } from 'node:child_process';
import net from 'node:net';
import { setTimeout as delay } from 'node:timers/promises';

const [baseURL, version = '0.0.84', mode = 'isolated', repetitionsText = '10', timeoutText = '5000'] = process.argv.slice(2);
if (!baseURL) throw new Error('usage: node compare_playwright_idle.mjs <base-url> [version] [isolated|shared] [repetitions] [timeout-ms]');
if (!['isolated', 'shared'].includes(mode)) throw new Error(`unsupported mode: ${mode}`);
const repetitions = Number(repetitionsText);
const timeoutMs = Number(timeoutText);
if (!Number.isInteger(repetitions) || repetitions < 1) throw new Error('repetitions must be a positive integer');
if (!Number.isInteger(timeoutMs) || timeoutMs < 1000) throw new Error('timeout-ms must be an integer >= 1000');
const listener = net.createServer();
await new Promise((resolve, reject) => listener.listen(0, '127.0.0.1', resolve).once('error', reject));
const port = listener.address().port;
await new Promise(resolve => listener.close(resolve));
const endpoint = `http://127.0.0.1:${port}/mcp`;
const serverArgs = ['--yes', `@playwright/mcp@${version}`, '--headless', '--browser', 'chrome', '--no-sandbox'];
serverArgs.push(mode === 'isolated' ? '--isolated' : '--shared-browser-context');
serverArgs.push('--port', String(port), '--host', '127.0.0.1', '--idle-timeout', String(timeoutMs));
const child = spawn('npx', serverArgs, { stdio: ['ignore', 'ignore', 'pipe'] });
let stderr = '';
child.stderr.on('data', chunk => { stderr += chunk.toString(); });

function parseResponse(contentType, raw) {
  if (contentType.includes('application/json')) return JSON.parse(raw);
  const line = raw.split('\n').find(value => value.startsWith('data:'));
  if (!line) throw new Error(`no JSON-RPC data in response: ${raw.slice(0, 500)}`);
  return JSON.parse(line.slice(5).trim());
}

async function request(client, method, params = {}) {
  const headers = { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' };
  if (client.sessionId) headers['Mcp-Session-Id'] = client.sessionId;
  if (client.protocolVersion) headers['Mcp-Protocol-Version'] = client.protocolVersion;
  const response = await fetch(endpoint, { method: 'POST', headers, body: JSON.stringify({ jsonrpc: '2.0', id: ++client.nextId, method, params }) });
  if (!response.ok) throw new Error(`HTTP ${response.status} for ${method}: ${await response.text()}`);
  const sessionId = response.headers.get('mcp-session-id');
  if (sessionId) client.sessionId = sessionId;
  if (method === 'initialize') client.protocolVersion = '2025-03-26';
  const raw = await response.text();
  return raw ? parseResponse(response.headers.get('content-type') ?? '', raw) : undefined;
}

async function makeClient(name) {
  const client = { name, nextId: 0, sessionId: '', protocolVersion: '' };
  const initialized = await request(client, 'initialize', {
    protocolVersion: '2025-03-26', capabilities: {},
    clientInfo: { name: `scout-idle-probe-${name}`, version: '1.0.0' },
  });
  if (!client.sessionId) throw new Error(`Playwright MCP did not assign an HTTP session to ${name}`);
  await request(client, 'notifications/initialized');
  const catalog = await request(client, 'tools/list');
  const evaluateTool = catalog?.result?.tools?.find(tool => tool.name === 'browser_evaluate');
  const evaluateArg = Object.keys(evaluateTool?.inputSchema?.properties ?? {}).find(key => ['code', 'expression', 'function'].includes(key));
  if (!evaluateArg) throw new Error(`browser_evaluate schema is unsupported: ${JSON.stringify(evaluateTool?.inputSchema)}`);
  client.evaluateArg = evaluateArg;
  return { client, initialized };
}

async function callTool(client, name, args = {}) {
  const result = await request(client, 'tools/call', { name, arguments: args });
  if (result?.result?.isError) throw new Error(`${name}: ${JSON.stringify(result.result.content)}`);
  return result?.result;
}

function textResult(result) {
  return result?.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') ?? '';
}

function evaluationValue(result) {
  const match = textResult(result).match(/### Result\n([\s\S]*?)(?:\n### Ran Playwright code|\n### Page|$)/);
  if (!match) return undefined;
  try { return JSON.parse(match[1].trim()); } catch { return undefined; }
}

async function evaluate(client, code) {
  return callTool(client, 'browser_evaluate', { [client.evaluateArg]: code });
}

let serverErrors = '';
try {
  const rows = [];
  let browserUserAgent = '';
  for (let repetition = 1; repetition <= repetitions; repetition++) {
    let a, b;
    const startupDeadline = Date.now() + 90000;
    while (!a && Date.now() < startupDeadline) {
      if (child.exitCode !== null) throw new Error(`Playwright MCP exited ${child.exitCode}: ${stderr}`);
      try { a = await makeClient(`idle-${repetition}`); } catch { await delay(300); }
    }
    if (!a) throw new Error(`Playwright MCP did not become ready: ${stderr}`);
    b = await makeClient(`active-${repetition}`);
    if (a.client.sessionId === b.client.sessionId) throw new Error('HTTP clients unexpectedly share one MCP session ID');

    const keyA = `idle_probe_a_${repetition}`;
    const keyB = `idle_probe_b_${repetition}`;
    await callTool(a.client, 'browser_navigate', { url: `${baseURL}/session-owner/seed` });
    const seedA = await evaluate(a.client, `() => { document.cookie = '${keyA}=a; path=/'; localStorage.setItem('${keyA}', 'a'); return 'seeded-a'; }`);
    await callTool(b.client, 'browser_navigate', { url: `${baseURL}/session-owner/seed` });
    const seedB = await evaluate(b.client, `() => { document.cookie = '${keyB}=b; path=/'; localStorage.setItem('${keyB}', 'b'); return 'seeded-b'; }`);
    if (evaluationValue(seedA) !== 'seeded-a' || evaluationValue(seedB) !== 'seeded-b') throw new Error(`could not seed ${mode} contexts in repetition ${repetition}`);
    if (!browserUserAgent) browserUserAgent = evaluationValue(await evaluate(b.client, '() => navigator.userAgent'));
    const crossClientState = evaluationValue(await evaluate(b.client, `() => ({cookie: document.cookie.includes('${keyA}=a'), storage: localStorage.getItem('${keyA}')})`));
    const crossClientExpected = mode === 'shared' ? crossClientState?.cookie === true && crossClientState?.storage === 'a' : crossClientState?.cookie === false && crossClientState?.storage === null;
    if (!crossClientExpected) throw new Error(`unexpected cross-client context state in ${mode} mode: ${JSON.stringify(crossClientState)}`);

    const keepAliveUntil = Date.now() + timeoutMs * 2;
    let activeChecks = 0;
    while (Date.now() < keepAliveUntil) {
      await evaluate(b.client, `() => localStorage.getItem('${keyB}')`);
      activeChecks++;
      await delay(timeoutMs / 4);
    }

    await callTool(a.client, 'browser_navigate', { url: `${baseURL}/session-owner/seed` });
    const idleState = evaluationValue(await evaluate(a.client, `() => ({cookie: document.cookie.includes('${keyA}=a'), storage: localStorage.getItem('${keyA}')})`));
    const activeState = evaluationValue(await evaluate(b.client, `() => ({cookie: document.cookie.includes('${keyB}=b'), storage: localStorage.getItem('${keyB}')})`));
    const expectedIdleRetained = mode === 'shared';
    const success = idleState?.cookie === expectedIdleRetained && idleState?.storage === (expectedIdleRetained ? 'a' : null) && activeState?.cookie === true && activeState?.storage === 'b';
    rows.push({ repetition, success, cross_client_state: crossClientState, idle_client_state_after_window: idleState, active_client_state_after_window: activeState, active_client_keepalive_calls: activeChecks });
  }
  const report = {
    tool: 'playwright-mcp', version, interface: 'Streamable HTTP', mode,
    captured_at: new Date().toISOString(), browser_user_agent: browserUserAgent,
    idle_timeout_ms: timeoutMs, repetitions, passed: rows.filter(row => row.success).length,
    success: rows.length === repetitions && rows.every(row => row.success), repetitions_detail: rows,
    evidence_scope: `storage-lifecycle contract across ${repetitions} runs; browser/process memory and cleanup latency are not measured`,
  };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  if (!report.success) process.exitCode = 1;
} catch (error) {
  serverErrors = stderr;
  process.stderr.write(`${error instanceof Error ? error.stack : error}\n${serverErrors}`);
  process.exitCode = 1;
} finally {
  if (child.exitCode === null) child.kill('SIGTERM');
}
