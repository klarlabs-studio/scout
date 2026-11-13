#!/usr/bin/env node
// Inspect pinned MCP catalogs for native task identity and recovery tools.
// This is a capability-surface probe; it does not infer runtime failure rates.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const [agentVersion, playwrightVersion, outputPath] = process.argv.slice(2);
if (!agentVersion || !playwrightVersion || !outputPath) {
  throw new Error('usage: node recovery_capability_probe.mjs <agent-browser-version> <playwright-mcp-version> <output.json>');
}

class MCPProcess {
  constructor(command, args) {
    this.child = spawn(command, args, { stdio: ['pipe', 'pipe', 'pipe'], detached: process.platform !== 'win32' });
    this.buffer = Buffer.alloc(0);
    this.pending = new Map();
    this.nextId = 1;
    this.stderr = '';
    this.child.stdout.on('data', chunk => this.consume(chunk));
    this.child.stderr.on('data', chunk => { this.stderr += chunk.toString(); });
    this.child.on('exit', (code, signal) => {
      for (const pending of this.pending.values()) pending.reject(new Error(`MCP exited (${code ?? signal}): ${this.stderr}`));
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
      const message = JSON.parse(raw.toString('utf8'));
      if (message.id === undefined || !this.pending.has(message.id)) continue;
      const pending = this.pending.get(message.id);
      this.pending.delete(message.id);
      message.error ? pending.reject(new Error(JSON.stringify(message.error))) : pending.resolve(message.result);
    }
  }
  request(method, params = {}) {
    const id = this.nextId++;
    const message = { jsonrpc: '2.0', id, method, params };
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`request timeout: ${method}`)); }, 30000);
      this.pending.set(id, {
        resolve: result => { clearTimeout(timer); resolve(result); },
        reject: error => { clearTimeout(timer); reject(error); },
      });
      this.child.stdin.write(`${JSON.stringify(message)}\n`);
    });
  }
  notify(method, params = {}) { this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params })}\n`); }
  async stop() {
    this.child.stdin.end();
    if (this.child.exitCode !== null) return;
    if (process.platform === 'win32') this.child.kill('SIGTERM');
    else {
      try { process.kill(-this.child.pid, 'SIGTERM'); } catch { this.child.kill('SIGTERM'); }
    }
    await Promise.race([
      new Promise(resolve => this.child.once('exit', resolve)),
      new Promise(resolve => setTimeout(resolve, 2000)),
    ]);
    if (this.child.exitCode === null) {
      if (process.platform === 'win32') this.child.kill('SIGKILL');
      else { try { process.kill(-this.child.pid, 'SIGKILL'); } catch { this.child.kill('SIGKILL'); } }
    }
  }
}

const nativeRecoveryName = /task_run|get_task_run|resolve_task_run|export_task_recovery_receipt/i;
const recoverySurface = /task|idempot|recover|reconcil|replay|resume/i;
const summarize = (tool, includeSchema = false) => ({
  name: tool.name,
  description: (tool.description || '').slice(0, 240),
  ...(includeSchema ? { inputSchema: tool.inputSchema || {} } : {}),
});

async function inspect(name, version, command, args) {
  const process = new MCPProcess(command, args);
  try {
    const initialized = await process.request('initialize', {
      protocolVersion: '2025-11-25', capabilities: {},
      clientInfo: { name: 'scout-recovery-capability-probe', version: '1.0.0' },
    });
    process.notify('notifications/initialized');
    let cursor;
    const tools = [];
    do {
      const listed = await process.request('tools/list', cursor ? { cursor } : {});
      tools.push(...(listed.tools || []));
      cursor = listed.nextCursor;
    } while (cursor);
    const relevant = tools.filter(tool => recoverySurface.test(tool.name));
    const taskTools = tools.filter(tool => nativeRecoveryName.test(tool.name));
    const explicitTaskIDFields = tools.flatMap(tool => {
      const properties = tool.inputSchema?.properties || {};
      return Object.keys(properties).filter(key => /^(task_id|taskId|idempotency_key|idempotencyKey)$/.test(key) || (nativeRecoveryName.test(tool.name) && key === 'id'))
        .map(key => ({ tool: tool.name, field: key, schema: properties[key] }));
    });
    return {
      product: name,
      version,
      initializationCapabilities: initialized.capabilities || {},
      toolCount: tools.length,
      recoveryRelatedToolNames: relevant.map(tool => tool.name),
      explicitTaskIdentityFields: explicitTaskIDFields,
      nativeTaskWorkflowTools: taskTools.map(tool => summarize(tool, true)),
      catalogComplete: cursor === undefined,
      interpretation: 'A missing task identity or recovery tool is a capability-surface observation for this pinned MCP catalog; it is not a runtime reliability result.',
    };
  } finally {
    await process.stop();
  }
}

const results = await Promise.all([
  inspect('agent-browser', agentVersion, 'npx', ['--yes', `agent-browser@${agentVersion}`, 'mcp', '--tools', 'all']),
  inspect('playwright-mcp', playwrightVersion, 'npx', ['--yes', `@playwright/mcp@${playwrightVersion}`, '--headless', '--browser', 'chrome', '--no-sandbox', '--isolated']),
  inspect('scout-worktree', 'current-worktree', 'go', ['run', './cmd/scout', 'mcp', 'serve']),
]);
const report = {
  benchmarkVersion: 'scout-browser-reliability-v28',
  comparisonProtocolVersion: 'scout-browser-comparison-v1',
  scenario: 'native-task-recovery-capability-catalog',
  inspectedAt: new Date().toISOString(),
  scope: 'MCP initialize capabilities and complete tools/list catalogs only; no browser action or MCP Tasks protocol behavior was exercised.',
  results,
};
fs.mkdirSync(path.dirname(path.resolve(outputPath)), { recursive: true });
fs.writeFileSync(outputPath, `${JSON.stringify(report, null, 2)}\n`);
console.log(JSON.stringify(report, null, 2));
