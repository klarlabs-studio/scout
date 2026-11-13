#!/usr/bin/env node
// Compare repeated UI submissions against an application idempotency fixture.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const [version, baseURL, resultsDir] = process.argv.slice(2);
if (!version || !baseURL || !resultsDir) throw new Error('usage: node run_playwright_idempotency.mjs <pinned-version> <base-url> <results-dir>');
fs.mkdirSync(resultsDir, { recursive: true });
const transcript = path.join(resultsDir, 'playwright-mcp-idempotency.jsonl');
fs.writeFileSync(transcript, '');
const record = value => fs.appendFileSync(transcript, `${JSON.stringify(value)}\n`);

class Client {
  constructor(child) {
    this.child = child; this.buffer = Buffer.alloc(0); this.pending = new Map(); this.nextId = 1;
    child.stdout.on('data', chunk => this.consume(chunk));
    child.on('exit', (code, signal) => { for (const p of this.pending.values()) p.reject(new Error(`MCP exited (${code ?? signal})`)); this.pending.clear(); });
  }
  consume(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    for (;;) {
      const end = this.buffer.indexOf('\n'); if (end < 0) return;
      const raw = this.buffer.subarray(0, end); this.buffer = this.buffer.subarray(end + 1); if (!raw.length) continue;
      const msg = JSON.parse(raw.toString());
      if (msg.id !== undefined && this.pending.has(msg.id)) { const p = this.pending.get(msg.id); this.pending.delete(msg.id); msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result); }
      record({ direction: 'server', message: msg });
    }
  }
  request(method, params = {}) {
    const id = this.nextId++, message = { jsonrpc: '2.0', id, method, params }; record({ direction: 'client', message });
    return new Promise((resolve, reject) => { const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`timeout: ${method}`)); }, 90000); this.pending.set(id, { resolve: value => { clearTimeout(timer); resolve(value); }, reject: err => { clearTimeout(timer); reject(err); } }); this.child.stdin.write(`${JSON.stringify(message)}\n`); });
  }
  notify(method, params = {}) { this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params })}\n`); }
  async tool(name, args) { const result = await this.request('tools/call', { name, arguments: args }); if (result.isError) throw new Error(`${name}: ${JSON.stringify(result.content)}`); return result; }
}

const child = spawn('npx', ['--yes', `@playwright/mcp@${version}`, '--headless', '--browser', 'chrome', '--no-sandbox', '--isolated', '--proxy-bypass', '127.0.0.1,localhost'], { stdio: ['pipe', 'pipe', 'pipe'], detached: true });
const client = new Client(child);
const state = async () => { const r = await fetch(`${baseURL}/idempotent-mutation-state`); if (!r.ok) throw new Error(`fixture state returned ${r.status}`); return r.json(); };
let report = {
  benchmark_version: 'scout-browser-reliability-v28', comparison_protocol_version: 'scout-browser-comparison-v1',
  fixture_version: 'scout-browser-reliability-v28', tool_name: 'playwright-mcp', tool_version: version,
  scenario_id: 'application-idempotency-after-ui-replay', fixture_url: `${baseURL}/idempotency-task`,
  fixture_isolation: 'fresh fixture server process per tool run; adapter-local application key store',
  mechanism: 'repeat the same browser UI submission; fixture page supplies a stable application Idempotency-Key',
  tool_native_task_id_guard_tested: false, host_supplied_idempotency_tested: false, application_idempotency_tested: true,
  success: false, raw_output_file: path.basename(transcript),
};
const saveReport = () => fs.writeFileSync(path.join(resultsDir, 'playwright-mcp-idempotency-comparison.json'), `${JSON.stringify(report, null, 2)}\n`);
try {
  await client.request('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'scout-idempotency-comparison', version: '1' } });
  client.notify('notifications/initialized');
  const listed = await client.request('tools/list');
  const evalTool = listed.tools.find(tool => tool.name === 'browser_evaluate');
  const key = Object.keys(evalTool?.inputSchema?.properties || {}).find(k => ['code', 'expression', 'function'].includes(k));
  if (!key) throw new Error('browser_evaluate input schema not recognized');
  const evaluate = async expression => {
    const result = await client.tool('browser_evaluate', { [key]: expression });
    return result.content?.filter(part => part.type === 'text').map(part => part.text).join('\n') ?? '';
  };
  await client.tool('browser_navigate', { url: `${baseURL}/idempotency-task` });
  const before = await state();
  await client.tool('browser_click', { target: '#submit' });
  const waitFor = async predicate => {
    const deadline = Date.now() + 5000;
    let value = await state();
    while (!predicate(value) && Date.now() < deadline) { await new Promise(resolve => setTimeout(resolve, 10)); value = await state(); }
    if (!predicate(value)) throw new Error('UI action did not reach the authoritative fixture');
    return value;
  };
  let afterFirst;
  try {
    afterFirst = await waitFor(value => value.committed_mutations === before.committed_mutations + 1);
  } catch (error) {
    report.first_page_status = await evaluate('() => document.querySelector("#status")?.textContent');
    try {
      const consoleResult = await client.tool('browser_console_messages', { level: 'error' });
      report.page_errors = consoleResult.content?.filter(part => part.type === 'text').map(part => part.text).join('\n') ?? '';
    } catch (consoleError) {
      report.console_capture_error = String(consoleError);
    }
    throw new Error(`${error}; page status: ${report.first_page_status}; page errors: ${report.page_errors ?? 'unavailable'}`);
  }
  await client.tool('browser_click', { target: '#submit' });
  const afterSecond = await waitFor(value => value.requests === afterFirst.requests + 1);
  report.first_call_effect_delta = afterFirst.committed_mutations - before.committed_mutations;
  report.same_task_retry_effect_delta = afterSecond.committed_mutations - afterFirst.committed_mutations;
  report.idempotency_key_count_delta = afterSecond.keys - before.keys;
  report.success = report.first_call_effect_delta === 1 && report.same_task_retry_effect_delta === 0 && report.idempotency_key_count_delta === 1;
  report.interpretation = 'Tests application-level deduplication from repeated UI submits; it does not test native task-ID replay guards, outcome classification, or browser restart recovery.';
  saveReport();
  console.log(JSON.stringify(report, null, 2));
  if (!report.success) process.exitCode = 1;
} catch (error) {
  report.failure = String(error);
  report.interpretation = 'The adapter did not complete the matched fixture request; this is a harness/configuration failure, not a competitive idempotency result.';
  saveReport();
  throw error;
} finally { try { process.kill(-child.pid, 'SIGKILL'); } catch { try { child.kill('SIGKILL'); } catch {} } }
