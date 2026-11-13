#!/usr/bin/env node
// Repeated probe: a Playwright MCP tool call remains alive beyond its HTTP idle timeout.
// Usage: node examples/benchmark/compare_playwright_inflight_idle.mjs <base-url> [version] [repetitions] [timeout-ms] [delay-ms]
import { spawn } from 'node:child_process';
import net from 'node:net';
import { setTimeout as delay } from 'node:timers/promises';

const [baseURL, version = '0.0.84', repetitionsText = '5', timeoutText = '5000', delayText = '1800'] = process.argv.slice(2);
if (!baseURL) throw new Error('usage: node compare_playwright_inflight_idle.mjs <base-url> [version] [repetitions] [timeout-ms] [delay-ms]');
const repetitions = Number(repetitionsText), timeoutMs = Number(timeoutText), delayMs = Number(delayText);
if (!Number.isInteger(repetitions) || repetitions < 1) throw new Error('repetitions must be a positive integer');
if (!Number.isInteger(timeoutMs) || timeoutMs < 250) throw new Error('timeout-ms must be an integer >= 250');
if (!Number.isInteger(delayMs) || delayMs < 1 || delayMs > 30000) throw new Error('delay-ms must be an integer from 1 to 30000');

const listener = net.createServer();
await new Promise((resolve, reject) => listener.listen(0, '127.0.0.1', resolve).once('error', reject));
const basePort = listener.address().port;
await new Promise(resolve => listener.close(resolve));

function parseResponse(contentType, raw) {
  if (contentType.includes('application/json')) return JSON.parse(raw);
  const line = raw.split('\n').find(value => value.startsWith('data:'));
  if (!line) throw new Error(`no JSON-RPC data in response: ${raw.slice(0, 500)}`);
  return JSON.parse(line.slice(5).trim());
}

async function request(endpoint, client, method, params = {}) {
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

async function makeClient(endpoint) {
  const client = { nextId: 0, sessionId: '', protocolVersion: '' };
  await request(endpoint, client, 'initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'scout-inflight-idle-probe', version: '1.0.0' } });
  if (!client.sessionId) throw new Error('Playwright MCP did not assign an HTTP session ID');
  await request(endpoint, client, 'notifications/initialized');
  const catalog = await request(endpoint, client, 'tools/list');
  const tools = catalog?.result?.tools ?? [];
  const evaluateTool = tools.find(tool => tool.name === 'browser_evaluate');
  const evaluateArg = Object.keys(evaluateTool?.inputSchema?.properties ?? {}).find(key => ['code', 'expression', 'function'].includes(key));
  const waitTool = tools.find(tool => tool.name === 'browser_wait_for');
  const waitProperties = waitTool?.inputSchema?.properties ?? {};
  const textArg = Object.keys(waitProperties).find(key => key === 'text');
  if (!evaluateArg || !textArg) throw new Error(`unsupported Playwright tools schema: ${JSON.stringify(tools.filter(tool => ['browser_evaluate', 'browser_wait_for'].includes(tool.name)))}`);
  return { ...client, endpoint, evaluateArg, textArg, waitProperties };
}

async function callTool(client, name, args = {}) {
  const response = await request(client.endpoint, client, 'tools/call', { name, arguments: args });
  if (response?.result?.isError) throw new Error(`${name}: ${JSON.stringify(response.result.content)}`);
  return response?.result;
}

function textResult(result) { return result?.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') ?? ''; }
function evaluationValue(result) {
  const match = textResult(result).match(/### Result\n([\s\S]*?)(?:\n### Ran Playwright code|\n### Page|$)/);
  if (!match) return undefined;
  try { return JSON.parse(match[1].trim()); } catch { return undefined; }
}

let browserUserAgent = '';
const rows = [];
try {
  for (let repetition = 1; repetition <= repetitions; repetition++) {
    const listener = net.createServer();
    await new Promise((resolve, reject) => listener.listen(0, '127.0.0.1', resolve).once('error', reject));
    const port = listener.address().port;
    await new Promise(resolve => listener.close(resolve));
    const endpoint = `http://127.0.0.1:${port}/mcp`;
    const child = spawn('npx', ['--yes', `@playwright/mcp@${version}`, '--headless', '--browser', 'chrome', '--no-sandbox', '--isolated', '--port', String(port), '--host', '127.0.0.1', '--idle-timeout', String(timeoutMs)], { stdio: ['ignore', 'ignore', 'pipe'] });
    let stderr = '';
    child.stderr.on('data', chunk => { stderr += chunk.toString(); });
    const row = { repetition, success: false };
    rows.push(row);
    try {
      const client = await (async () => {
        const deadline = Date.now() + 90000;
        while (Date.now() < deadline) {
          if (child.exitCode !== null) throw new Error(`Playwright MCP exited ${child.exitCode}: ${stderr}`);
          try { return await makeClient(endpoint); } catch { await delay(300); }
        }
        throw new Error(`Playwright MCP did not become ready: ${stderr}`);
      })();
      await callTool(client, 'browser_navigate', { url: `${baseURL}/delayed-element?delay_ms=${delayMs}` });
      const before = evaluationValue(await callTool(client, 'browser_evaluate', { [client.evaluateArg]: '() => ({url: location.href, userAgent: navigator.userAgent, ready: Boolean(document.querySelector("#delayed-ready"))})' }));
      if (!before?.url?.includes('/delayed-element?') || before.ready) throw new Error(`fixture did not start in expected pre-ready state: ${JSON.stringify(before)}`);
      if (!browserUserAgent) browserUserAgent = before.userAgent;
      const waitArg = { [client.textArg]: 'Ready' };
      // Some schema versions label the maximum wait as "time", others as "timeout".
      if ('time' in client.waitProperties) waitArg.time = Math.max(10, timeoutMs / 1000 * 2);
      else if ('timeout' in client.waitProperties) waitArg.timeout = Math.max(10000, timeoutMs * 2);
      const waitStarted = Date.now();
      try {
        await callTool(client, 'browser_wait_for', waitArg);
      } catch (error) {
        row.error = error instanceof Error ? error.message : String(error);
        row.wait_duration_ms = Date.now() - waitStarted;
        continue;
      }
      const waitDurationMs = Date.now() - waitStarted;
      let after;
      try {
        after = evaluationValue(await callTool(client, 'browser_evaluate', { [client.evaluateArg]: '() => ({url: location.href, ready: Boolean(document.querySelector("#delayed-ready")), text: document.querySelector("#delayed-ready")?.textContent ?? ""})' }));
      } catch (error) {
        row.error = error instanceof Error ? error.message : String(error);
      }
      const success = after?.url?.includes('/delayed-element?') && after?.ready === true && after?.text === 'Ready';
      Object.assign(row, { success, wait_duration_ms: waitDurationMs, page_after_wait: after });
    } catch (error) {
      row.error = error instanceof Error ? error.message : String(error);
    } finally {
      if (child.exitCode === null) child.kill('SIGTERM');
    }
  }
  const report = {
    tool: 'playwright-mcp', version, interface: 'Streamable HTTP', mode: 'isolated',
    captured_at: new Date().toISOString(), browser_user_agent: browserUserAgent,
    idle_timeout_ms: timeoutMs, fixture_delay_ms: delayMs, wait_predicate: 'browser_wait_for text=Ready', repetitions,
    passed: rows.filter(row => row.success).length, success: rows.length === repetitions && rows.every(row => row.success),
    repetitions_detail: rows,
    evidence_scope: `browser_wait_for waited for the delayed fixture element and retained the same page in ${rows.length} runs; idle cleanup after the call and resource reclamation are not measured`,
  };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  if (!report.success) process.exitCode = 1;
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.stack : error}`);
  process.exitCode = 1;
}
