#!/usr/bin/env node
// Run one WebMCP fixture task through a pinned Playwright MCP package.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const [version, baseURL, outputPath] = process.argv.slice(2);
if (!version || !baseURL || !outputPath) {
  throw new Error('usage: node run_playwright_mcp_smoke.mjs <version> <base-url> <output.json>');
}
const chrome = process.env.CHROME_EXECUTABLE || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
if (!fs.existsSync(chrome)) throw new Error(`Chrome executable not found: ${chrome}`);
const configPath = path.join(os.tmpdir(), `scout-webmcp-playwright-${process.pid}.json`);
fs.writeFileSync(configPath, JSON.stringify({
  browser: {
    browserName: 'chromium',
    isolated: true,
    launchOptions: { headless: true, executablePath: chrome, args: ['--enable-features=WebMCP'] },
  },
}));

class MCPClient {
  constructor(child) {
    this.child = child;
    this.buffer = '';
    this.nextId = 1;
    this.pending = new Map();
    this.stderr = '';
    child.stdout.on('data', chunk => this.consume(chunk));
    child.stderr.on('data', chunk => { this.stderr += chunk.toString(); });
    child.on('exit', code => {
      for (const pending of this.pending.values()) pending.reject(new Error(`Playwright MCP exited (${code})`));
      this.pending.clear();
    });
  }
  consume(chunk) {
    this.buffer += chunk.toString();
    while (true) {
      const boundary = this.buffer.indexOf('\n');
      if (boundary < 0) return;
      const line = this.buffer.slice(0, boundary);
      this.buffer = this.buffer.slice(boundary + 1);
      if (!line) continue;
      const message = JSON.parse(line);
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
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`MCP request timed out: ${method}`));
      }, 60_000);
      this.pending.set(id, {
        resolve: value => { clearTimeout(timer); resolve(value); },
        reject: error => { clearTimeout(timer); reject(error); },
      });
      this.child.stdin.write(`${JSON.stringify(message)}\n`);
    });
  }
  notify(method, params = {}) {
    this.child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params })}\n`);
  }
  async callTool(name, args = {}) {
    const response = await this.request('tools/call', { name, arguments: args });
    if (response.isError) throw new Error(`${name} failed: ${JSON.stringify(response.content)}`);
    return response;
  }
  async close() {
    try { await this.callTool('browser_close'); } catch {}
    this.child.stdin.end();
    this.child.kill('SIGTERM');
  }
}

const child = spawn('npx', [
  '--yes', `@playwright/mcp@${version}`, '--config', configPath,
  '--init-script', path.join(path.dirname(new URL(import.meta.url).pathname), 'init.js'),
  '--headless', '--no-sandbox',
], { stdio: ['pipe', 'pipe', 'pipe'] });
const client = new MCPClient(child);
const textOf = result => result.content?.filter(part => part.type === 'text').map(part => part.text).join('\n') ?? '';
try {
  const initialized = await client.request('initialize', {
    protocolVersion: '2025-03-26', capabilities: {},
    clientInfo: { name: 'scout-webmcp-smoke', version: '1.0.0' },
  });
  client.notify('notifications/initialized');
  const initialTools = await client.request('tools/list');
  const available = new Set(initialTools.tools.map(tool => tool.name));
  for (const tool of ['browser_navigate', 'browser_snapshot', 'browser_evaluate', 'browser_close']) {
    if (!available.has(tool)) throw new Error(`Playwright MCP ${version} lacks ${tool}`);
  }
  await client.callTool('browser_navigate', { url: `${baseURL}/action` });
  const snapshot = await client.callTool('browser_snapshot');
  const list = await client.request('tools/list');
  const pageTool = list.tools.find(tool => tool.name === 'webmcp_complete_task');
  if (!pageTool) throw new Error(`WebMCP tool not exposed; snapshot: ${textOf(snapshot)}`);
  const invocation = await client.callTool(pageTool.name);
  const verify = await client.callTool('browser_evaluate', {
    function: '() => ({status: document.querySelector("#status")?.textContent, userAgent: navigator.userAgent})',
  });
  const verificationText = textOf(verify);
  if (!verificationText.includes('Task complete')) throw new Error(`postcondition failed: ${verificationText}`);
  const report = {
    classification: 'single-run interface smoke; not comparative reliability evidence',
    playwright_mcp_version: version,
    fixture_url: `${baseURL}/action`,
    chrome_user_agent: verificationText.match(/(?:Headless)?Chrome\/[^\s"\\]+/)?.[0] ?? 'captured in verify response',
    webmcp_tool: pageTool.name,
    tool_description: pageTool.description,
    tool_annotations: pageTool.annotations ?? null,
    input_schema: pageTool.inputSchema,
    snapshot_exposed_page_tool: textOf(snapshot).includes('complete_task'),
    invocation_text: textOf(invocation),
    independent_verification_text: verificationText,
    passed: true,
    limitations: [
      'The page tool is injected by the harness.',
      'One run does not estimate reliability.',
      'No agent-level selection, hostile metadata, delayed invocation, or UI fallback comparison is included.',
    ],
  };
  fs.mkdirSync(path.dirname(path.resolve(outputPath)), { recursive: true });
  fs.writeFileSync(outputPath, `${JSON.stringify(report, null, 2)}\n`);
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  await client.close();
} catch (error) {
  process.stderr.write(`${error}\n${client.stderr.slice(-3000)}`);
  process.exitCode = 1;
  await client.close();
} finally {
  fs.rmSync(configPath, { force: true });
}
