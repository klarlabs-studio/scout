#!/usr/bin/env node
// Matched loopback probe for Scout, agent-browser, and Playwright MCP.
// Usage: node examples/benchmark/run_origin_policy_comparison.mjs [repetitions]
import { spawn, spawnSync } from 'node:child_process';
import { createInterface } from 'node:readline';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';

const repetitions = Number(process.argv[2] ?? '3');
if (!Number.isInteger(repetitions) || repetitions < 1 || repetitions > 20) throw new Error('repetitions must be 1..20');
const root = process.cwd();
const reportDir = path.join(root, 'examples/benchmark/reports');
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('-', '').replace(/\.\d+Z$/, 'Z');
const rawDir = path.join(reportDir, `origin-policy-${stamp}`);
await mkdir(rawDir, { recursive: true });
const sourceRevision = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).stdout.trim();
const sourceStatus = spawnSync('git', ['status', '--short'], { cwd: root, encoding: 'utf8' }).stdout.trim();

function build(binary, pkg) {
  const result = spawnSync('go', ['build', '-o', binary, pkg], { cwd: root, encoding: 'utf8' });
  if (result.status !== 0) throw new Error(`go build ${pkg} failed: ${result.stderr}`);
}
const temp = await import('node:fs/promises').then(fs => fs.mkdtemp(path.join(os.tmpdir(), 'scout-policy-')));
const scout = path.join(temp, 'scout');
const fixture = path.join(temp, 'origin-policy-fixture');
build(scout, './cmd/scout');
build(fixture, './examples/benchmark/origin_policy_fixture');
const fixtureProc = spawn(fixture, ['-allowed-listen', '127.0.0.1:41745', '-denied-listen', '127.0.0.1:41746'], { stdio: ['ignore', 'ignore', 'pipe'] });
let fixtureStderr = '';
fixtureProc.stderr.on('data', chunk => fixtureStderr += chunk.toString());
const allowedOrigin = 'http://localhost:41745';
const deniedOrigin = 'http://127.0.0.1:41746';
const pageURL = `${allowedOrigin}/page?denied=${encodeURIComponent(deniedOrigin)}`;
const countURL = 'http://127.0.0.1:41745/count';
const resetURL = 'http://127.0.0.1:41745/reset';
async function waitFixture() {
  const end = Date.now() + 10000;
  while (Date.now() < end) {
    try { await fetch(countURL); return; } catch { await delay(100); }
  }
  throw new Error(`fixture failed to start: ${fixtureStderr}`);
}
async function count() { return (await (await fetch(countURL)).json()).denied_origin_hits; }
async function reset() { await fetch(resetURL); }

const raw = {};
const outcomes = { scout: [], 'agent-browser': [], 'playwright-mcp': [] };
const controls = { scout: [], 'agent-browser': [], 'playwright-mcp': [] };
const edgeCases = {};
async function save(name, value) {
  const encoded = JSON.stringify(value, null, 2) + '\n';
  const file = path.join(rawDir, `${name}.json`);
  await writeFile(file, encoded);
  raw[name] = { file: path.relative(root, file), bytes_utf8: Buffer.byteLength(encoded) };
}

async function runScout(repetition, restricted = true) {
  await reset();
  const args = ['session', '--headless', '--allow-private-ips', ...(restricted ? [`--allowed-origins=${allowedOrigin}`] : []), `--url=${pageURL}`];
  const proc = spawn(scout, args, { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  proc.stdout.on('data', chunk => stdout += chunk.toString());
  proc.stderr.on('data', chunk => stderr += chunk.toString());
  const code = await new Promise(resolve => proc.once('close', resolve));
  await delay(700);
  const hits = await count();
  let page;
  try { page = JSON.parse(stdout.trim().split('\n')[0]).result; } catch { page = undefined; }
  const fixtureReady = code === 0 && page?.title === 'Origin policy fixture' && page?.url?.startsWith(allowedOrigin + '/page?');
  const row = { repetition, mode: restricted ? 'restricted' : 'unrestricted-control', exit_code: code, fixture_ready: fixtureReady, denied_origin_hits: hits, blocked: restricted && fixtureReady && hits === 0, control_passed: !restricted && fixtureReady && hits === 1, stdout, stderr };
  (restricted ? outcomes.scout : controls.scout).push(row);
  await save(`scout-${restricted ? repetition : 'baseline'}`, row);
}

async function runAgentBrowser(repetition, restricted = true) {
  await reset();
  const env = { ...process.env, AGENT_BROWSER_NO_VERSION_CHECK: '1' };
  if (restricted) env.AGENT_BROWSER_ALLOWED_DOMAINS = 'localhost';
  else delete env.AGENT_BROWSER_ALLOWED_DOMAINS;
  const args = ['--no-auto-connect', '--session', `scout-policy-${repetition}`, 'open', pageURL];
  const proc = spawn('npx', ['--yes', '--package=agent-browser@0.42.0', 'agent-browser', ...args], { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '', stderr = '';
  proc.stdout.on('data', chunk => stdout += chunk.toString());
  proc.stderr.on('data', chunk => stderr += chunk.toString());
  const code = await new Promise(resolve => proc.once('close', resolve));
  await delay(700);
  const hits = await count();
  const fixtureReady = code === 0 && stdout.includes('Origin policy fixture') && stdout.includes('/page?');
  const row = { repetition, mode: restricted ? 'restricted' : 'unrestricted-control', exit_code: code, fixture_ready: fixtureReady, denied_origin_hits: hits, blocked: restricted && fixtureReady && hits === 0, control_passed: !restricted && fixtureReady && hits === 1, stdout, stderr };
  const close = spawnSync('npx', ['--yes', '--package=agent-browser@0.42.0', 'agent-browser', '--no-auto-connect', '--session', `scout-policy-${repetition}`, 'close'], { cwd: root, env, encoding: 'utf8', timeout: 30000 });
  row.cleanup_exit_code = close.status;
  (restricted ? outcomes['agent-browser'] : controls['agent-browser']).push(row);
  await save(`agent-browser-${restricted ? repetition : 'baseline'}`, row);
}

let nextId = 0;
async function runPlaywright(repetition, policyMode = 'blocked') {
  await reset();
  const restricted = policyMode === 'blocked';
  const edgeConfig = {
    'empty-allowlist': { arg: '--allowed-origins=', env: undefined },
    'empty-allowlist-env': { env: '' },
    'semicolon-only-env': { env: ';' },
    'whitespace-env': { env: '   ' },
  }[policyMode];
  const args = ['--yes', '@playwright/mcp@0.0.85', '--headless', '--browser', 'chrome', '--no-sandbox', ...(restricted ? ['--blocked-origins', deniedOrigin] : []), ...(edgeConfig?.arg ? [edgeConfig.arg] : []), '--isolated'];
  const env = { ...process.env };
  if (edgeConfig && 'env' in edgeConfig) env.PLAYWRIGHT_MCP_ALLOWED_ORIGINS = edgeConfig.env;
  const proc = spawn('npx', args, { cwd: root, env, stdio: ['pipe', 'pipe', 'pipe'] });
  let stderr = '', lineBuffer = '';
  const pending = new Map();
  proc.stderr.on('data', chunk => stderr += chunk.toString());
  proc.stdout.on('data', chunk => {
    lineBuffer += chunk.toString();
    let index;
    while ((index = lineBuffer.indexOf('\n')) >= 0) {
      const line = lineBuffer.slice(0, index); lineBuffer = lineBuffer.slice(index + 1);
      try {
        const message = JSON.parse(line);
        if (message.id && pending.has(message.id)) { pending.get(message.id)(message); pending.delete(message.id); }
      } catch { /* MCP servers may write non-protocol startup lines to stdout. */ }
    }
  });
  const transcript = [];
  async function request(method, params = {}, notification = false) {
    const id = notification ? undefined : ++nextId;
    const message = { jsonrpc: '2.0', ...(id ? { id } : {}), method, params };
    transcript.push({ direction: 'request', ...message });
    proc.stdin.write(JSON.stringify(message) + '\n');
    if (notification) return undefined;
    return await new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new Error(`timeout: ${method}`)); }, 30000);
      pending.set(id, result => { clearTimeout(timer); transcript.push({ direction: 'response', ...result }); resolve(result); });
    });
  }
  let row;
  try {
    const initialized = await request('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'scout-origin-policy-probe', version: '1.0.0' } });
    if (initialized.error) throw new Error(JSON.stringify(initialized.error));
    await request('notifications/initialized', {}, true);
    const listed = await request('tools/list');
    const tools = listed.result?.tools ?? [];
    const navigate = tools.find(tool => tool.name === 'browser_navigate');
    const evaluate = tools.find(tool => tool.name === 'browser_evaluate');
    const evalArg = Object.keys(evaluate?.inputSchema?.properties ?? {}).find(key => ['code', 'expression', 'function'].includes(key));
    if (!navigate || !evaluate || !evalArg) throw new Error('required Playwright MCP tools missing');
    const navigated = await request('tools/call', { name: 'browser_navigate', arguments: { url: pageURL } });
    await delay(900);
    const evaluated = await request('tools/call', { name: 'browser_evaluate', arguments: { [evalArg]: '() => ({url: location.href, ready: document.querySelector("#ready")?.textContent, userAgent: navigator.userAgent})' } });
    const hits = await count();
    const text = evaluated.result?.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') ?? '';
    const fixtureReady = text.includes('"ready": "Ready"') && text.includes(allowedOrigin + '/page?');
    row = { repetition, mode: policyMode, exit_code: 0, fixture_ready: fixtureReady, denied_origin_hits: hits, blocked: restricted && fixtureReady && hits === 0, control_passed: policyMode === 'unrestricted-control' && fixtureReady && hits === 1, navigation_result: navigated.result, browser_result: evaluated.result };
  } catch (error) {
    row = { repetition, mode: policyMode, exit_code: 1, fixture_ready: false, denied_origin_hits: await count(), blocked: false, control_passed: false, navigation_result: undefined, error: String(error), stderr };
  } finally {
    proc.kill('SIGTERM');
    await new Promise(resolve => proc.once('close', resolve));
  }
  row.transcript = transcript;
  row.stderr = stderr;
  if (edgeConfig) edgeCases[policyMode] = row;
  else (restricted ? outcomes['playwright-mcp'] : controls['playwright-mcp']).push(row);
  await save(`playwright-mcp-${policyMode}-${repetition}`, row);
}

try {
  await waitFixture();
  await runScout(1, false);
  for (let i = 1; i <= repetitions; i++) await runScout(i);
  await runAgentBrowser(1, false);
  for (let i = 1; i <= repetitions; i++) await runAgentBrowser(i);
  await runPlaywright(1, 'unrestricted-control');
  for (let i = 1; i <= repetitions; i++) await runPlaywright(i, 'blocked');
  await runPlaywright(1, 'empty-allowlist');
  await runPlaywright(1, 'empty-allowlist-env');
  await runPlaywright(1, 'semicolon-only-env');
  await runPlaywright(1, 'whitespace-env');
  const report = {
    schema_version: 1,
    scenario: 'matched-explicit-origin-request-blocking',
    captured_at: new Date().toISOString(),
    source: { description: 'current worktree adapters against pinned npm packages', git_revision: sourceRevision, worktree_dirty: sourceStatus.length > 0, dirty_paths: sourceStatus.split('\n').filter(Boolean) },
    versions: { scout: scoutVersion(scout), 'agent-browser': '0.42.0', 'playwright-mcp': '0.0.85', browser_user_agent: Object.values(outcomes['playwright-mcp']).flatMap(rows => rows).map(row => row.browser_result?.content?.find(item => item.type === 'text')?.text ?? '').find(text => text.includes('userAgent'))?.match(/"userAgent":\s*"([^"]+)"/)?.[1] ?? 'unavailable' },
    fixture: { page_url: pageURL, denied_origin: deniedOrigin, independent_oracle: 'atomic HTTP hit count on the denied-origin fixture listener' },
    policies: {
      scout: { setting: `--allowed-origins=${allowedOrigin}`, semantics: 'session page policy; receipt identifies known unobserved service-worker and WebSocket paths' },
      'agent-browser': { setting: 'AGENT_BROWSER_ALLOWED_DOMAINS=localhost', semantics: 'browser-level host containment with documented worker/WebRTC restrictions; host matching is broader than exact origin matching' },
      'playwright-mcp': { setting: `--blocked-origins ${deniedOrigin}`, semantics: 'exact origin blocklist; documented as not a security boundary and not covering redirects' }
    },
    repetitions,
    results: Object.fromEntries(Object.entries(outcomes).map(([name, rows]) => [name, { baseline_control: controls[name][0], baseline_control_passed: controls[name].length === 1 && controls[name][0].control_passed, fixture_ready: rows.filter(row => row.fixture_ready).length, blocked: rows.filter(row => row.blocked).length, runs: rows.length, false_successes: rows.filter(row => row.fixture_ready && row.denied_origin_hits > 0).length, repetitions: rows }])),
    edge_cases: { playwright: Object.fromEntries(Object.entries(edgeCases).map(([name, result]) => {
      const navigationText = result.navigation_result?.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') ?? '';
      const behavior = result.fixture_ready ? (result.denied_origin_hits === 0 ? 'denied_request_blocked' : 'denied_request_allowed') : navigationText.includes('ERR_BLOCKED_BY_CLIENT') ? 'configured_policy_blocked_allowed_fixture' : result.exit_code !== 0 ? 'server_or_tool_error' : 'fixture_not_reached';
      return [name, { behavior, result }];
    })) },
    raw_evidence: raw,
    limitations: [
      'This checks one ordinary fetch() to one denied local HTTP origin after loading the allowed page.',
      'It does not test redirects, service workers, WebSockets, EventSource, WebRTC, subresource classes, DNS rebinding, or remote-host egress.',
      'The native policy controls have different scopes and are not feature-equivalent; hit counts compare only this shared observable predicate.',
      'A local fixture run does not establish release reliability or production security boundaries.'
    ]
  };
  const reportPath = path.join(reportDir, `origin-policy-matched-${stamp}.json`);
  await writeFile(reportPath, JSON.stringify(report, null, 2) + '\n');
  process.stdout.write(`${JSON.stringify({ report: path.relative(root, reportPath), results: Object.fromEntries(Object.entries(report.results).map(([name, result]) => [name, `${result.blocked}/${result.runs} blocked`])) }, null, 2)}\n`);
  if (Object.values(report.results).some(result => !result.baseline_control_passed || result.blocked !== result.runs)) process.exitCode = 1;
} finally {
  fixtureProc.kill('SIGTERM');
  if (fixtureProc.exitCode === null && fixtureProc.signalCode === null) {
    await new Promise(resolve => {
      const timer = setTimeout(() => fixtureProc.kill('SIGKILL'), 2000);
      fixtureProc.once('close', () => { clearTimeout(timer); resolve(); });
    });
  }
  await rm(temp, { recursive: true, force: true });
}

function scoutVersion(binary) {
  const result = spawnSync(binary, ['version'], { encoding: 'utf8' });
  return result.stdout.trim() || result.stderr.trim() || 'current worktree binary';
}
