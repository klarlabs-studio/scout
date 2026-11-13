#!/usr/bin/env node
// Exercise Scout's WebMCP MCP tools against the local shared action fixture.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const [scoutBinary, cdpURL, baseURL, outputPath] = process.argv.slice(2);
if (!scoutBinary || !cdpURL || !baseURL || !outputPath) {
  throw new Error('usage: node run_scout_webmcp_smoke.mjs <scout-binary> <cdp-url> <fixture-base-url> <output.json>');
}
const fixtureDir = path.dirname(fileURLToPath(import.meta.url));
const initScript = fs.readFileSync(path.join(fixtureDir, 'init.js'), 'utf8');

class CDPClient {
  constructor(ws) {
    this.ws = ws;
    this.nextId = 1;
    this.pending = new Map();
    ws.addEventListener('message', event => {
      const message = JSON.parse(event.data);
      if (!this.pending.has(message.id)) return;
      const pending = this.pending.get(message.id);
      this.pending.delete(message.id);
      message.error ? pending.reject(new Error(JSON.stringify(message.error))) : pending.resolve(message.result);
    });
  }
  call(method, params = {}) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); }, 15_000);
      this.pending.set(id, {
        resolve: value => { clearTimeout(timer); resolve(value); },
        reject: error => { clearTimeout(timer); reject(error); },
      });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
  close() { this.ws.close(); }
}

class MCPClient {
  constructor(child) {
    this.child = child;
    this.buffer = '';
    this.nextId = 1;
    this.pending = new Map();
    this.stderr = '';
    child.stderr.on('data', chunk => { this.stderr += chunk.toString(); });
    child.stdout.on('data', chunk => {
      this.buffer += chunk.toString();
      for (;;) {
        const boundary = this.buffer.indexOf('\n');
        if (boundary < 0) return;
        const line = this.buffer.slice(0, boundary);
        this.buffer = this.buffer.slice(boundary + 1);
        if (!line) continue;
        const message = JSON.parse(line);
        if (!this.pending.has(message.id)) continue;
        const pending = this.pending.get(message.id);
        this.pending.delete(message.id);
        message.error ? pending.reject(new Error(JSON.stringify(message.error))) : pending.resolve(message.result);
      }
    });
    child.on('exit', code => {
      for (const pending of this.pending.values()) pending.reject(new Error(`Scout exited (${code}): ${this.stderr}`));
      this.pending.clear();
    });
  }
  request(method, params = {}) {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`MCP timeout: ${method}; ${this.stderr}`)); }, 60_000);
      this.pending.set(id, {
        resolve: value => { clearTimeout(timer); resolve(value); },
        reject: error => { clearTimeout(timer); reject(error); },
      });
      this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`);
    });
  }
  notify(method) { this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params: {} })}\n`); }
  async callTool(name, args) {
    const result = await this.request('tools/call', { name, arguments: args });
    if (result.isError) throw new Error(`${name}: ${JSON.stringify(result.content)}`);
    return result;
  }
}

const textOf = result => result.content?.filter(part => part.type === 'text').map(part => part.text).join('\n') ?? '';
let cdp;
let child;
try {
  const browserInfo = await (await fetch(`${cdpURL.replace(/\/$/, '')}/json/version`)).json();
  const targets = await (await fetch(`${cdpURL.replace(/\/$/, '')}/json/list`)).json();
  const target = targets.find(item => item.type === 'page');
  if (!target) throw new Error('Chrome has no page target');
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { ws.addEventListener('open', resolve, { once: true }); ws.addEventListener('error', reject, { once: true }); });
  cdp = new CDPClient(ws);
  await cdp.call('Page.enable');
  await cdp.call('Page.addScriptToEvaluateOnNewDocument', { source: initScript });
  await cdp.call('Page.navigate', { url: `${baseURL.replace(/\/$/, '')}/action` });
  let registered = false;
  for (let attempt = 0; attempt < 100; attempt++) {
    const value = await cdp.call('Runtime.evaluate', {
      expression: '(async()=>({tools:(await document.modelContext?.getTools?.())?.map(tool=>tool.name)??[]}))()',
      returnByValue: true,
      awaitPromise: true,
    });
    if (value.result?.value?.tools?.includes('complete_task')) { registered = true; break; }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  if (!registered) throw new Error('fixture WebMCP tool did not register');

  child = spawn(scoutBinary, ['mcp', 'serve', '--profile=all'], {
    env: { ...process.env, SCOUT_REMOTE_CDP: cdpURL, SCOUT_ALLOW_PRIVATE_IPS: '1' },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const client = new MCPClient(child);
  await client.request('initialize', {
    protocolVersion: '2025-03-26', capabilities: {},
    clientInfo: { name: 'scout-webmcp-live-smoke', version: '1' },
  });
  client.notify('notifications/initialized');
  const tools = [];
  let cursor;
  do {
    const page = await client.request('tools/list', cursor ? { cursor } : {});
    tools.push(...page.tools);
    cursor = page.nextCursor;
  } while (cursor);
  for (const name of ['webmcp_catalog', 'webmcp_invoke', 'task_run', 'get_task_run', 'export_task_recovery_receipt']) {
    if (!tools.some(tool => tool.name === name)) throw new Error(`MCP profile all lacks ${name}`);
  }

  const catalogResult = await client.callTool('webmcp_catalog', { target_id: target.id });
  const catalog = JSON.parse(textOf(catalogResult));
  const pageTool = catalog.tools.find(tool => tool.name === 'complete_task');
  if (!pageTool) throw new Error('catalog omitted complete_task');
  const taskInput = {
    id: 'webmcp-complete-task-smoke',
    target_id: target.id,
    required_tools: ['export_task_recovery_receipt'],
    actions: [{
      action: 'webmcp_invoke',
      webmcp_name: pageTool.name,
      webmcp_frame_id: pageTool.frame_id,
      webmcp_input: {},
      expected_text: 'Task complete',
    }],
  };
  const invokeResult = await client.callTool('task_run', taskInput);
  const task = JSON.parse(textOf(invokeResult));
  const retryResult = await client.callTool('task_run', taskInput);
  const retryTask = JSON.parse(textOf(retryResult));
  const storedTaskResult = await client.callTool('get_task_run', { id: taskInput.id });
  const storedTask = JSON.parse(textOf(storedTaskResult));
  const receiptResult = await client.callTool('export_task_recovery_receipt', { id: taskInput.id });
  const recoveryReceipt = JSON.parse(textOf(receiptResult));
  const independentValue = await cdp.call('Runtime.evaluate', {
    expression: 'JSON.stringify({status:document.querySelector("#status")?.textContent,completed:document.querySelector("#status")?.textContent==="Task complete",click_count:window.__scoutWebMCPClicks})',
    returnByValue: true,
  });
  const independentVerification = JSON.parse(independentValue.result.value);
  const storedRecord = storedTask.record ?? storedTask;
  const recoverySteps = storedRecord.recovery?.steps ?? storedRecord.result?.batch?.recovery?.steps ?? storedRecord.result?.recovery?.steps ?? storedRecord.plan?.actions ?? [];
  const retainedWebMCPStep = recoverySteps.some(step => step.webmcp_name === pageTool.name && step.webmcp_frame_id === pageTool.frame_id && !Object.hasOwn(step, 'webmcp_input'));
  const receiptFrame = recoveryReceipt.target?.frame_id;
  const report = {
    classification: 'single-run live Scout WebMCP bridge smoke; not a reliability estimate',
    date: new Date().toISOString().slice(0, 10),
    browser: browserInfo.Browser,
    fixture_url: `${baseURL.replace(/\/$/, '')}/action`,
    mcp_profile: 'all',
    mcp_tool_count: tools.length,
    webmcp_tools: tools.filter(tool => tool.name.startsWith('webmcp_')).map(tool => tool.name),
    catalog,
    task,
    same_id_retry: retryTask,
    stored_task: storedTask,
    recovery_receipt: recoveryReceipt,
    checks: { retained_webmcp_step_without_input: retainedWebMCPStep, receipt_frame_id: receiptFrame },
    independent_verification: independentVerification,
    passed: task.status === 'completed'
      && task.batch?.verified === 1
      && retryTask.idempotency_replay === true
      && recoveryReceipt.recovery?.replay_authorized === false
      && retainedWebMCPStep
      && receiptFrame === pageTool.frame_id
      && independentVerification.completed === true
      && independentVerification.click_count === 1,
    limitations: [
      'The page tool is injected by the local fixture.',
      'One run does not estimate reliability; repeated same-ID invocation checks only that Scout does not re-dispatch this completed task.',
      'No delayed completion, hostile metadata, schema rejection, frame drift, cancellation, or actual-agent selection is covered.',
    ],
  };
  fs.mkdirSync(path.dirname(path.resolve(outputPath)), { recursive: true });
  fs.writeFileSync(outputPath, `${JSON.stringify(report, null, 2)}\n`);
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  if (!report.passed) process.exitCode = 1;
} catch (error) {
  process.stderr.write(`${error}\n`);
  process.exitCode = 1;
} finally {
  if (child) { child.stdin.end(); child.kill('SIGTERM'); }
  cdp?.close();
}
