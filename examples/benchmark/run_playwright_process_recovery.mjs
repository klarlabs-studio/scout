#!/usr/bin/env node
// Probe Playwright MCP full-process recovery against the authoritative mutation fixture.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const [version, baseURL, resultsDir] = process.argv.slice(2);
if (!version || !baseURL || !resultsDir) {
  throw new Error('usage: node run_playwright_process_recovery.mjs <pinned-version> <base-url> <results-dir>');
}
fs.mkdirSync(resultsDir, { recursive: true });
const transcriptPath = path.join(resultsDir, 'playwright-mcp-process-recovery.jsonl');
fs.writeFileSync(transcriptPath, '');
const record = value => fs.appendFileSync(transcriptPath, `${JSON.stringify(value)}\n`);

class Client {
  constructor(child) {
    this.child = child;
    this.buffer = Buffer.alloc(0);
    this.pending = new Map();
    this.nextId = 1;
    child.stdout.on('data', chunk => this.consume(chunk));
    child.on('exit', (code, signal) => {
      for (const item of this.pending.values()) item.reject(new Error(`MCP exited (${code ?? signal})`));
      this.pending.clear();
    });
  }
  consume(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    for (;;) {
      const end = this.buffer.indexOf('\n');
      if (end < 0) return;
      const raw = this.buffer.subarray(0, end);
      this.buffer = this.buffer.subarray(end + 1);
      if (!raw.length) continue;
      const message = JSON.parse(raw.toString());
      if (message.id !== undefined && this.pending.has(message.id)) {
        const item = this.pending.get(message.id);
        this.pending.delete(message.id);
        message.error ? item.reject(new Error(JSON.stringify(message.error))) : item.resolve(message.result);
      }
      record({ direction: 'server', message });
    }
  }
  request(method, params = {}) {
    const id = this.nextId++;
    const message = { jsonrpc: '2.0', id, method, params };
    record({ direction: 'client', message });
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`timeout: ${method}`)); }, 90000);
      this.pending.set(id, {
        resolve: result => { clearTimeout(timer); resolve(result); },
        reject: error => { clearTimeout(timer); reject(error); },
      });
      this.child.stdin.write(`${JSON.stringify(message)}\n`);
    });
  }
  notify(method, params = {}) { this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params })}\n`); }
  async tool(name, args) {
    const response = await this.request('tools/call', { name, arguments: args });
    if (response.isError) throw new Error(`${name}: ${JSON.stringify(response.content)}`);
    return response;
  }
}

const start = () => {
  const child = spawn('npx', ['--yes', `@playwright/mcp@${version}`, '--headless', '--browser', 'chrome', '--no-sandbox', '--isolated'], {
    stdio: ['pipe', 'pipe', 'pipe'], detached: true,
  });
  const client = new Client(child);
  return { child, client };
};
const initialize = async client => {
  await client.request('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'scout-process-recovery', version: '1' } });
  client.notify('notifications/initialized');
  return client.request('tools/list');
};
const state = async () => {
  const response = await fetch(`${baseURL}/process-recovery-state`);
  if (!response.ok) throw new Error(`fixture state returned ${response.status}`);
  return response.json();
};
const descendants = rootPID => {
  const output = execFileSync('ps', ['-axo', 'pid=,ppid=,command='], { encoding: 'utf8' });
  const rows = output.split('\n').flatMap(line => {
    const match = line.trim().match(/^(\d+)\s+(\d+)\s+(.+)$/);
    return match ? [{ pid: Number(match[1]), ppid: Number(match[2]), command: match[3] }] : [];
  });
  const found = new Set([rootPID]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const row of rows) if (found.has(row.ppid) && !found.has(row.pid)) { found.add(row.pid); changed = true; }
  }
  return rows.filter(row => found.has(row.pid) && row.pid !== rootPID);
};

let first;
let second;
try {
  first = start();
  const listed = await initialize(first.client);
  const evalTool = listed.tools.find(tool => tool.name === 'browser_evaluate');
  const evalProps = evalTool?.inputSchema?.properties || {};
  const evalKey = Object.keys(evalProps).find(key => ['code', 'expression', 'function'].includes(key));
  if (!evalKey) throw new Error('browser_evaluate input schema not recognized');
  await first.client.tool('browser_navigate', { url: `${baseURL}/process-recovery` });
  await first.client.tool('browser_evaluate', { [evalKey]: '() => { localStorage.setItem("scout-recovery-marker", "saved"); return localStorage.getItem("scout-recovery-marker"); }' });
  await first.client.tool('browser_click', { target: '#mutate' });
  const deadline = Date.now() + 10000;
  let before = await state();
  while (before.committed_mutations < 1 && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 10));
    before = await state();
  }
  if (before.committed_mutations !== 1) throw new Error('fixture mutation did not commit before process kill');

  const children = descendants(first.child.pid);
  const chromePIDs = children.filter(row => /Google Chrome|Chromium/.test(row.command)).map(row => row.pid);
  if (!chromePIDs.length) throw new Error(`could not find Chrome descendants for MCP PID ${first.child.pid}`);
  for (const pid of chromePIDs) try { process.kill(pid, 'SIGKILL'); } catch {}
  try { process.kill(-first.child.pid, 'SIGKILL'); } catch { first.child.kill('SIGKILL'); }

  second = start();
  const restartedTools = await initialize(second.client);
  const restartEval = restartedTools.tools.find(tool => tool.name === 'browser_evaluate');
  const restartKey = Object.keys(restartEval?.inputSchema?.properties || {}).find(key => ['code', 'expression', 'function'].includes(key));
  await second.client.tool('browser_navigate', { url: `${baseURL}/process-recovery` });
  const markerResult = await second.client.tool('browser_evaluate', { [restartKey]: '() => localStorage.getItem("scout-recovery-marker")' });
  const markerText = markerResult.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') || '';
  const markerMissing = markerText.includes('null') || markerText.includes('"null"');
  const after = await state();
  await second.client.tool('browser_click', { target: '#mutate' });
  const replayDeadline = Date.now() + 10000;
  let afterReplay = await state();
  while (afterReplay.committed_mutations < 2 && Date.now() < replayDeadline) {
    await new Promise(resolve => setTimeout(resolve, 10));
    afterReplay = await state();
  }
  const report = {
    benchmark_version: 'scout-browser-reliability-v28',
    comparison_protocol_version: 'scout-browser-comparison-v1',
    fixture_version: 'scout-browser-reliability-v28',
    tool_name: 'playwright-mcp',
    tool_version: version,
    scenario_id: 'committed-mutation-hard-browser-kill',
    fixture_url: `${baseURL}/process-recovery`,
    fault_boundary: 'full-browser-and-MCP-process-loss',
    browser_child_pids_killed: chromePIDs,
    mutation_committed_before_kill: before.committed_mutations === 1,
    mutation_count_after_restart: after.committed_mutations,
    mutation_count_after_blind_replay: afterReplay.committed_mutations,
    session_marker_before_kill: 'saved',
    session_marker_restored: !markerMissing,
    marker_observation: markerText,
    reopen_succeeded: true,
    native_committed_action_record_observed: false,
    native_task_id_replay_guard_tested: false,
    blind_same_action_replay_dispatched: true,
    duplicate_side_effect_after_blind_replay: afterReplay.committed_mutations === 2,
    host_must_reconcile_before_retry: true,
    mutation_outcome_preserved: after.committed_mutations === 1,
    success: before.committed_mutations === 1 && after.committed_mutations === 1 && afterReplay.committed_mutations === 2,
    interpretation: 'The first post-restart fixture read shows whether the original side effect remained committed. The runner then deliberately repeats the same UI action on this harmless local fixture; a second count demonstrates duplicate effect from blind replay. This does not test host-supplied idempotency or a native task-ID replay guard.',
    raw_output_file: path.basename(transcriptPath),
  };
  fs.writeFileSync(path.join(resultsDir, 'playwright-mcp-process-recovery-comparison.json'), `${JSON.stringify(report, null, 2)}\n`);
  console.log(JSON.stringify(report, null, 2));
} finally {
  for (const run of [second, first]) if (run) {
    try { process.kill(-run.child.pid, 'SIGKILL'); } catch { try { run.child.kill('SIGKILL'); } catch {} }
  }
}
