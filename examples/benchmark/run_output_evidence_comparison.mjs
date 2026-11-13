#!/usr/bin/env node
// Compare bounded extraction of identical page windows, tail recovery, and
// accidental disclosure of a synthetic value stored outside rendered text.
import { spawn, spawnSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { setTimeout as delay } from 'node:timers/promises';
import os from 'node:os';
import path from 'node:path';

const root = process.cwd();
const versions = { scout: 'current worktree', 'agent-browser': '0.42.0', 'playwright-mcp': '0.0.85' };
const repetitions = Number(process.argv[2] ?? '3');
if (!Number.isInteger(repetitions) || repetitions < 1 || repetitions > 10) throw new Error('repetitions must be 1..10');
const sentinel = 'SCOUT_TAIL_EVIDENCE_7f3b9d21';
const privacySentinel = 'SCOUT_PRIVATE_SENTINEL_91c4a6e2';
const pageSize = 8;
const recordCount = 65;
const tailOffset = Math.floor((recordCount - 1) / pageSize) * pageSize;
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('-', '').replace(/\.\d+Z$/, 'Z');
const reportDir = path.join(root, 'examples/benchmark/reports');
const rawDir = path.join(reportDir, `output-evidence-${stamp}`);
await mkdir(rawDir, { recursive: true });
const temp = await mkdtemp(path.join(os.tmpdir(), 'scout-output-evidence-'));
const freePort = await new Promise((resolve, reject) => {
  const server = createServer();
  server.once('error', reject);
  server.listen(0, '127.0.0.1', () => {
    const address = server.address();
    server.close(error => error ? reject(error) : resolve(address.port));
  });
});
const baseURL = `http://127.0.0.1:${freePort}`;
const pageURL = `${baseURL}/output-evidence-stress`;
const scout = path.join(temp, 'scout');
const fixture = path.join(temp, 'fixture');
function build(binary, pkg) {
  const result = spawnSync('go', ['build', '-o', binary, pkg], { cwd: root, encoding: 'utf8' });
  if (result.status !== 0) throw new Error(`go build ${pkg} failed: ${result.stderr}`);
}
build(scout, './cmd/scout');
build(fixture, './examples/benchmark/fixtureserver');
let fixtureStderr = '';
async function waitFixture() {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(pageURL);
      if (response.ok) {
        const body = await response.text();
        if (body.includes(sentinel) && body.includes(privacySentinel)) return;
      }
    } catch { /* wait for fixture startup */ }
    await delay(100);
  }
  throw new Error(`fixture failed to start or lacks tail sentinel: ${fixtureStderr}`);
}

function mcpClient(command, args, env = {}) {
  const child = spawn(command, args, { cwd: root, env: { ...process.env, ...env }, stdio: ['pipe', 'pipe', 'pipe'], detached: process.platform !== 'win32' });
  let buffer = '', stderr = '', nextID = 0;
  const pending = new Map();
  const transcript = [];
  child.stderr.on('data', chunk => stderr += chunk.toString());
  child.stdout.on('data', chunk => {
    buffer += chunk.toString();
    let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
      try {
        const message = JSON.parse(line);
        if (message.id !== undefined && pending.has(message.id)) {
          const entry = pending.get(message.id); pending.delete(message.id); clearTimeout(entry.timer);
          transcript.push({ direction: 'response', ...message }); entry.resolve(message);
        }
      } catch { /* server diagnostics are kept in stderr */ }
    }
  });
  function send(message) {
    transcript.push({ direction: 'request', ...message });
    child.stdin.write(JSON.stringify(message) + '\n');
  }
  function request(method, params = {}, timeoutMS = 45000) {
    const id = ++nextID;
    send({ jsonrpc: '2.0', id, method, params });
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new Error(`timeout: ${method}; stderr=${stderr}`)); }, timeoutMS);
      pending.set(id, { resolve, reject, timer });
    });
  }
  async function initialize(clientName) {
    const initialized = await request('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: clientName, version: '1.0.0' } });
    if (initialized.error) throw new Error(`${clientName} initialize failed: ${JSON.stringify(initialized.error)}`);
    // MCP notifications are fire-and-forget; waiting for a response here hangs.
    send({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} });
  }
  async function listTools() {
    const tools = [];
    let cursor;
    do {
      const response = await request('tools/list', cursor ? { cursor } : {});
      if (response.error) throw new Error(`tools/list failed: ${JSON.stringify(response.error)}`);
      tools.push(...(response.result?.tools ?? [])); cursor = response.result?.nextCursor;
    } while (cursor);
    return tools;
  }
  async function close() {
    const waitForClose = async timeoutMS => {
      if (child.exitCode !== null || child.signalCode !== null) return true;
      return Promise.race([
        new Promise(resolve => child.once('close', () => resolve(true))),
        delay(timeoutMS).then(() => false),
      ]);
    };
    const signalTree = signal => {
      try {
        if (process.platform !== 'win32' && child.pid) process.kill(-child.pid, signal);
        else child.kill(signal);
      } catch (error) {
        if (error.code !== 'ESRCH') throw error;
      }
    };
    signalTree('SIGTERM');
    if (await waitForClose(5000)) return;
    signalTree('SIGKILL');
    if (!(await waitForClose(5000))) throw new Error(`MCP process group ${child.pid} did not exit after SIGKILL`);
  }
  return { child, request, initialize, listTools, transcript, get stderr() { return stderr; }, close };
}

function collectStrings(value, output = []) {
  if (typeof value === 'string') output.push(value);
  else if (Array.isArray(value)) for (const item of value) collectStrings(item, output);
  else if (value && typeof value === 'object') for (const item of Object.values(value)) collectStrings(item, output);
  return output;
}
function hasMarker(value) { return collectStrings(value).some(text => text.includes(sentinel)); }
function hasMarkerValue(value, marker) { return collectStrings(value).some(text => text.includes(marker)); }
function serializedSize(value) { return Buffer.byteLength(JSON.stringify(value)); }
function textOf(result) { return (result?.content ?? []).filter(item => item.type === 'text').map(item => item.text).join('\n'); }
function extractedWindow(result) {
  const structured = result?.result?.structuredContent?.response?.data?.result;
  const candidates = [structured, textOf(result?.result ?? result)];
  for (let candidate of candidates) {
    for (let depth = 0; depth < 3; depth++) {
      if (Array.isArray(candidate) && candidate.every(value => typeof value === 'string')) return candidate;
      if (candidate && typeof candidate === 'object' && Array.isArray(candidate.items)) return candidate.items;
      if (typeof candidate !== 'string') break;
      let text = candidate.trim();
      if (text.startsWith('### Result')) text = text.replace(/^### Result\s*/, '').split(/\n### Ran Playwright code/)[0].trim();
      try { candidate = JSON.parse(text); } catch { break; }
    }
  }
  return [];
}
function expectedArticleText(index) {
  if (index === recordCount - 1) return `Final record\n\n${sentinel}`;
  return `Record ${String(index).padStart(3, '0')}\n\n${`evidence-${String(index).padStart(3, '0')}-`.repeat(250)}`;
}
function matchesWindow(result, offset) {
  const expected = Array.from({ length: Math.min(pageSize, recordCount - offset) }, (_, i) => expectedArticleText(offset + i));
  const observed = extractedWindow(result);
  return { matches: JSON.stringify(observed) === JSON.stringify(expected), count: observed.length };
}
async function saveRaw(name, row) {
  const file = path.join(rawDir, `${name}.json`);
  await writeFile(file, JSON.stringify(row, null, 2) + '\n');
  return { file: path.relative(root, file), bytes_utf8: Buffer.byteLength(await readFile(file)) };
}

async function scoutProbe(repetition) {
  const client = mcpClient(scout, ['mcp', 'serve'], { SCOUT_ALLOW_PRIVATE_IPS: 'true' });
  try {
    await client.initialize('scout-output-evidence-probe');
    const tools = await client.listTools();
    const byName = new Map(tools.map(tool => [tool.name, tool]));
    for (const required of ['navigate', 'extract_all', 'search_page_snapshot']) if (!byName.has(required)) throw new Error(`Scout missing ${required}`);
    await client.request('tools/call', { name: 'navigate', arguments: { url: pageURL } });
    const extracted = await client.request('tools/call', { name: 'extract_all', arguments: { selector: 'article', limit: pageSize, offset: 0 } });
    let markerReturned = hasMarker(extracted);
    let recovery = null;
    if (!markerReturned) {
      const text = textOf(extracted.result ?? extracted);
      const firstPage = text ? JSON.parse(text) : {};
      const total = Number(firstPage.total ?? 0);
      const returnedPageSize = Number(firstPage.limit ?? 0);
      if (firstPage.truncated === true && total > 0 && returnedPageSize > 0) {
        const offset = Math.floor((total - 1) / returnedPageSize) * returnedPageSize;
        recovery = await client.request('tools/call', { name: 'extract_all', arguments: { selector: 'article', limit: returnedPageSize, offset } });
        markerReturned = hasMarker(recovery);
      }
    }
    const initialWindow = matchesWindow(extracted, 0);
    const tailWindow = matchesWindow(recovery, tailOffset);
    return { product: 'scout', repetition, version: versions.scout, interface: 'MCP stdio', extraction: `extract_all(article, offsets=0/${tailOffset}, limit=${pageSize})`, response_bytes: serializedSize(extracted), normalized_window_bytes: serializedSize(extractedWindow(extracted)), inline_tail_marker: hasMarker(extracted), recovered_tail_marker: markerReturned, explicit_truncation: /truncat|output_limit|artifact/i.test(JSON.stringify(extracted)), recovery_response_bytes: recovery ? serializedSize(recovery) : 0, normalized_tail_bytes: serializedSize(extractedWindow(recovery)), initial_window_matches_fixture: initialWindow.matches, initial_window_item_count: initialWindow.count, tail_window_matches_fixture: tailWindow.matches, tail_window_item_count: tailWindow.count, privacy_sentinel_leaked: hasMarkerValue([extracted, recovery], privacySentinel), tool_result: extracted, recovery_result: recovery, stderr: client.stderr, transcript: client.transcript };
  } finally { await client.close(); }
}

async function agentBrowserProbe(repetition) {
  const client = mcpClient('npx', ['--yes', '--package=agent-browser@0.42.0', 'agent-browser', 'mcp'], { AGENT_BROWSER_NO_VERSION_CHECK: '1' });
  try {
    await client.initialize('agent-browser-output-evidence-probe');
    const tools = await client.listTools();
    const open = tools.find(tool => tool.name === 'agent_browser_open');
    const evaluate = tools.find(tool => tool.name === 'agent_browser_eval');
    if (!open || !evaluate) throw new Error('agent-browser open/eval tools missing');
    const session = `scout-output-${stamp}-${repetition}`;
    await client.request('tools/call', { name: open.name, arguments: { url: pageURL, session } });
    const script = offset => `JSON.stringify(Array.from(document.querySelectorAll('article')).slice(${offset}, ${offset + pageSize}).map(element => element.innerText))`;
    const result = await client.request('tools/call', { name: evaluate.name, arguments: { script: script(0), session } });
    const recovery = await client.request('tools/call', { name: evaluate.name, arguments: { script: script(tailOffset), session } });
    const initialWindow = matchesWindow(result, 0);
    const tailWindow = matchesWindow(recovery, tailOffset);
    return { product: 'agent-browser', repetition, version: versions['agent-browser'], interface: 'MCP stdio', extraction: `agent_browser_eval(article.innerText, offsets=0/${tailOffset}, limit=${pageSize})`, response_bytes: serializedSize(result), normalized_window_bytes: serializedSize(extractedWindow(result)), inline_tail_marker: hasMarker(result), recovered_tail_marker: hasMarkerValue([result, recovery], sentinel), explicit_truncation: /truncat|omitted|limit/i.test(JSON.stringify(result)), recovery_response_bytes: serializedSize(recovery), normalized_tail_bytes: serializedSize(extractedWindow(recovery)), initial_window_matches_fixture: initialWindow.matches, initial_window_item_count: initialWindow.count, tail_window_matches_fixture: tailWindow.matches, tail_window_item_count: tailWindow.count, privacy_sentinel_leaked: hasMarkerValue([result, recovery], privacySentinel), tool_result: result, recovery_result: recovery, stderr: client.stderr, transcript: client.transcript };
  } finally { await client.close(); }
}

async function playwrightProbe(repetition) {
  const client = mcpClient('npx', ['--yes', '@playwright/mcp@0.0.85', '--headless', '--browser', 'chrome', '--no-sandbox', '--isolated']);
  try {
    await client.initialize('playwright-output-evidence-probe');
    const tools = await client.listTools();
    const navigate = tools.find(tool => tool.name === 'browser_navigate');
    const evaluate = tools.find(tool => tool.name === 'browser_evaluate');
    const evalArg = Object.keys(evaluate?.inputSchema?.properties ?? {}).find(key => ['code', 'expression', 'function'].includes(key));
    if (!navigate || !evaluate || !evalArg) throw new Error('Playwright MCP navigate/evaluate tools missing');
    await client.request('tools/call', { name: navigate.name, arguments: { url: pageURL } });
    const script = offset => `() => JSON.stringify(Array.from(document.querySelectorAll('article')).slice(${offset}, ${offset + pageSize}).map(element => element.innerText))`;
    const result = await client.request('tools/call', { name: evaluate.name, arguments: { [evalArg]: script(0) } });
    const recovery = await client.request('tools/call', { name: evaluate.name, arguments: { [evalArg]: script(tailOffset) } });
    const initialWindow = matchesWindow(result, 0);
    const tailWindow = matchesWindow(recovery, tailOffset);
    return { product: 'playwright-mcp', repetition, version: versions['playwright-mcp'], interface: 'MCP stdio', extraction: `browser_evaluate(article.innerText, offsets=0/${tailOffset}, limit=${pageSize})`, response_bytes: serializedSize(result), normalized_window_bytes: serializedSize(extractedWindow(result)), inline_tail_marker: hasMarker(result), recovered_tail_marker: hasMarkerValue([result, recovery], sentinel), explicit_truncation: /truncat|omitted|limit/i.test(JSON.stringify(result)), recovery_response_bytes: serializedSize(recovery), normalized_tail_bytes: serializedSize(extractedWindow(recovery)), initial_window_matches_fixture: initialWindow.matches, initial_window_item_count: initialWindow.count, tail_window_matches_fixture: tailWindow.matches, tail_window_item_count: tailWindow.count, privacy_sentinel_leaked: hasMarkerValue([result, recovery], privacySentinel), tool_result: result, recovery_result: recovery, stderr: client.stderr, transcript: client.transcript };
  } finally { await client.close(); }
}

let fixtureProc;
try {
  fixtureProc = spawn(fixture, ['-listen', `127.0.0.1:${freePort}`], { stdio: ['ignore', 'ignore', 'pipe'], detached: process.platform !== 'win32' });
  fixtureProc.stderr.on('data', chunk => fixtureStderr += chunk.toString());
  await waitFixture();
  const probes = [];
  for (let repetition = 1; repetition <= repetitions; repetition++) {
    for (const run of [scoutProbe, agentBrowserProbe, playwrightProbe]) {
      try { probes.push(await run(repetition)); }
      catch (error) { probes.push({ product: run.name.replace('Probe', ''), repetition, error: String(error), stderr: fixtureStderr }); }
    }
  }
  const raw = {};
  for (const [index, row] of probes.entries()) {
    const name = `${row.product ?? `probe-${index + 1}`}-run-${row.repetition ?? index + 1}`;
    raw[name] = await saveRaw(name, row);
  }
  const summaries = probes.map(({ transcript, tool_result, recovery_result, stderr, ...summary }) => summary);
  const report = {
    schema_version: 1,
    scenario: 'matched-window-tail-retrieval-and-private-attribute-sentinel',
    captured_at: new Date().toISOString(),
    repetitions,
    versions,
    fixture: { page_url: pageURL, records: recordCount, page_size: pageSize, offsets: [0, tailOffset], page_tail_marker: sentinel, private_attribute_sentinel: privacySentinel, private_sentinel_source_present: true, source_oracle: 'HTTP fixture response body checked before browser probes' },
    results: summaries,
    raw_evidence: raw,
    interpretation: 'Each product queried article innerText for the same two windows with the same page size; Scout additionally returns structured source references and truncation metadata. Wire bytes therefore include product-specific metadata and wrappers and are descriptive, not a ranking. The private sentinel exists only in an article data attribute outside selected innerText; a leak means it appeared in either returned window.',
    limitations: [
      'This synthetic DOM probe is not an operator study, privacy certification, or production reliability estimate.',
      'The privacy sentinel is a DOM attribute canary, not a credential or real personal data; results do not cover snapshots, screenshots, accessibility trees, logs, or other extraction tools.',
      'Wire-byte counts include API-specific response metadata and do not establish throughput, latency, or an overall product ranking.'
    ]
  };
  const reportPath = path.join(reportDir, `output-evidence-${stamp}.json`);
  await writeFile(reportPath, JSON.stringify(report, null, 2) + '\n');
  process.stdout.write(`${JSON.stringify({ report: path.relative(root, reportPath), results: summaries.map(row => ({ product: row.product, error: row.error, response_bytes: row.response_bytes, inline_tail_marker: row.inline_tail_marker, recovered_tail_marker: row.recovered_tail_marker, explicit_truncation: row.explicit_truncation })) }, null, 2)}\n`);
  if (probes.length !== repetitions * 3 || probes.some(row => row.error || !row.recovered_tail_marker || !row.initial_window_matches_fixture || !row.tail_window_matches_fixture || row.privacy_sentinel_leaked)) process.exitCode = 1;
} finally {
  try {
    if (fixtureProc) {
      const signalFixtureTree = signal => {
        try {
          if (process.platform !== 'win32' && fixtureProc.pid) process.kill(-fixtureProc.pid, signal);
          else fixtureProc.kill(signal);
        } catch (error) {
          if (error.code !== 'ESRCH') throw error;
        }
      };
      signalFixtureTree('SIGTERM');
      const fixtureClosed = await Promise.race([
        new Promise(resolve => fixtureProc.once('close', () => resolve(true))),
        delay(5000).then(() => false),
      ]);
      if (!fixtureClosed) {
        signalFixtureTree('SIGKILL');
        const killedFixtureClosed = await Promise.race([
          new Promise(resolve => fixtureProc.once('close', () => resolve(true))),
          delay(5000).then(() => false),
        ]);
        if (!killedFixtureClosed) throw new Error(`fixture process group ${fixtureProc.pid} did not exit after SIGKILL`);
      }
    }
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
}
