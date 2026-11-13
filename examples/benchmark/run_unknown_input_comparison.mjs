#!/usr/bin/env node
// Verify unknown MCP fields fail before browser side effects on pinned peers.
import { spawn, spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';
import os from 'node:os';
import path from 'node:path';

const root = process.cwd();
const versions = { scout: 'current worktree', 'agent-browser': '0.42.0', 'playwright-mcp': '0.0.85' };
const mcpGoSource = path.resolve(root, '../mcp-go');
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('-', '').replace(/\.\d+Z$/, 'Z');
const reportDir = path.join(root, 'examples/benchmark/reports');
const rawDir = path.join(reportDir, `unknown-input-${stamp}`);
await mkdir(rawDir, { recursive: true });
const temp = await mkdtemp(path.join(os.tmpdir(), 'scout-unknown-input-'));
const overlayMod = path.join(temp, 'go.mod');
const baseMod = await (await import('node:fs/promises')).readFile(path.join(root, 'go.mod'), 'utf8');
await writeFile(overlayMod, `${baseMod}\nreplace go.klarlabs.de/mcp => ${mcpGoSource}\n`);
await (await import('node:fs/promises')).copyFile(path.join(root, 'go.sum'), path.join(temp, 'go.sum'));
function build(binary, pkg) {
  const r = spawnSync('go', ['build', '-modfile', overlayMod, '-o', binary, pkg], { cwd: root, encoding: 'utf8', env: process.env });
  if (r.status !== 0) throw new Error(`build ${pkg} failed: ${r.stderr}`);
}
const scout = path.join(temp, 'scout');
const fixture = path.join(temp, 'fixture');
build(scout, './cmd/scout');
build(fixture, './examples/benchmark/origin_policy_fixture');
const fixtureProc = spawn(fixture, ['-allowed-listen', '127.0.0.1:41745', '-denied-listen', '127.0.0.1:41746'], { stdio: ['ignore', 'ignore', 'pipe'] });
let fixtureStderr = '';
fixtureProc.stderr.on('data', chunk => fixtureStderr += chunk.toString());
const baseURL = 'http://localhost:41745';
const pageURL = `${baseURL}/page?denied=${encodeURIComponent('http://127.0.0.1:41746')}`;
async function waitFixture() {
  const end = Date.now() + 10000;
  while (Date.now() < end) { try { await fetch(`${baseURL}/count`); return; } catch { await delay(100); } }
  throw new Error(`fixture did not start: ${fixtureStderr}`);
}
async function reset() { await fetch(`${baseURL}/reset`); }
async function count() { return (await (await fetch(`${baseURL}/count`)).json()).denied_origin_hits; }

async function probe(product, command, args, env = {}) {
  const child = spawn(command, args, { cwd: root, env: { ...process.env, ...env }, stdio: ['pipe', 'pipe', 'pipe'] });
  let stderr = '', buffer = '', id = 0;
  const pending = new Map(), transcript = [];
  child.stderr.on('data', chunk => stderr += chunk.toString());
  child.stdout.on('data', chunk => {
    buffer += chunk.toString(); let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
      try { const msg = JSON.parse(line); if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); } }
      catch { /* preserve protocol transcript only */ }
    }
  });
  function request(method, params = {}, notification = false) {
    const reqId = notification ? undefined : ++id;
    const message = { jsonrpc: '2.0', ...(reqId ? { id: reqId } : {}), method, params };
    transcript.push({ direction: 'request', ...message });
    child.stdin.write(JSON.stringify(message) + '\n');
    if (notification) return Promise.resolve(undefined);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(reqId); reject(new Error(`timeout: ${method}`)); }, 30000);
      pending.set(reqId, response => { clearTimeout(timer); transcript.push({ direction: 'response', ...response }); resolve(response); });
    });
  }
  try {
    const initialized = await request('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'scout-unknown-input-probe', version: '1.0.0' } });
    if (initialized.error) throw new Error(JSON.stringify(initialized.error));
    await request('notifications/initialized', {}, true);
    let tools = [], cursor;
    do {
      const response = await request('tools/list', cursor ? { cursor } : {});
      tools.push(...(response.result?.tools ?? [])); cursor = response.result?.nextCursor;
    } while (cursor);
    const tool = tools.find(item => product === 'scout' ? item.name === 'navigate' : product === 'playwright-mcp' ? item.name === 'browser_navigate' : item.name === 'agent_browser_open');
    if (!tool) throw new Error(`navigation tool missing for ${product}; found ${tools.length} tools`);
    const schema = tool.inputSchema ?? {};
    const schemaClosed = schema.additionalProperties === false;
    const unknownField = 'retired_wait_for_navigation';
    await reset();
    const invalid = await request('tools/call', { name: tool.name, arguments: { url: pageURL, [unknownField]: true } });
    const invalidResult = invalid.result ?? {};
    const invalidRejected = invalid.error?.code === -32602 || invalidResult.isError === true;
    await delay(300);
    const invalidHits = await count();
    await reset();
    const valid = await request('tools/call', { name: tool.name, arguments: { url: pageURL } });
    await delay(700);
    const validHits = await count();
    const validResult = valid.result ?? {};
    return {
      product, version: versions[product], interface: 'MCP stdio', tool: tool.name,
      schema_additional_properties_false: schemaClosed,
      unknown_field: unknownField,
      invalid_call_is_error: invalidRejected,
      invalid_call_protocol_error: invalid.error ?? null,
      invalid_call_content: invalidResult.content ?? invalid.error,
      invalid_call_denied_origin_hits: invalidHits,
      valid_control_is_error: validResult.isError === true,
      valid_control_denied_origin_hits: validHits,
      predicate_passed: invalidRejected && invalidHits === 0 && validResult.isError !== true && validHits === 1,
      stderr, transcript
    };
  } finally {
    child.kill('SIGTERM');
    await new Promise(resolve => child.once('close', resolve));
  }
}

try {
  await waitFixture();
  const rows = [];
  rows.push(await probe('scout', scout, ['mcp', 'serve'], { SCOUT_ALLOW_PRIVATE_IPS: 'true' }));
  rows.push(await probe('agent-browser', 'npx', ['--yes', '--package=agent-browser@0.42.0', 'agent-browser', 'mcp']));
  rows.push(await probe('playwright-mcp', 'npx', ['--yes', '@playwright/mcp@0.0.85', '--headless', '--browser', 'chrome', '--no-sandbox', '--isolated']));
  const raw = {};
  for (const row of rows) {
    const file = path.join(rawDir, `${row.product}.json`);
    const encoded = JSON.stringify(row, null, 2) + '\n';
    await writeFile(file, encoded);
    raw[row.product] = { file: path.relative(root, file), bytes_utf8: Buffer.byteLength(encoded) };
  }
  const report = {
    schema_version: 1,
    scenario: 'unknown-mcp-input-rejection-and-side-effect-comparison',
    captured_at: new Date().toISOString(),
    versions,
    fixture: { page_url: pageURL, mcp_go_source_override: '../mcp-go', side_effect_oracle: 'denied-origin /hit counter; invalid calls must cause zero requests and valid calls exactly one' },
    results: rows,
    raw_evidence: raw,
    limitations: [
      'One unknown optional field is tested against each navigation tool; this does not enumerate deprecated fields or every tool schema.',
      'Closed-schema advertisement is reported independently from runtime behavior because implementations may ignore additional fields.',
      'MCP tool errors (isError) and JSON-RPC InvalidParams errors (-32602) both count as rejection when no fixture request occurs.',
      'A single valid navigation follows each invalid call as a positive control; each product uses its documented MCP stdio server.',
      'Local pinned versions and Chrome establish this case only, not broad compatibility or reliability.'
    ]
  };
  const reportPath = path.join(reportDir, `unknown-input-${stamp}.json`);
  await writeFile(reportPath, JSON.stringify(report, null, 2) + '\n');
  process.stdout.write(`${JSON.stringify({ report: path.relative(root, reportPath), results: rows.map(row => ({ product: row.product, passed: row.predicate_passed, invalid_hits: row.invalid_call_denied_origin_hits, valid_hits: row.valid_control_denied_origin_hits })) }, null, 2)}\n`);
  if (rows.some(row => !row.predicate_passed)) process.exitCode = 1;
} finally {
  fixtureProc.kill('SIGTERM');
}
