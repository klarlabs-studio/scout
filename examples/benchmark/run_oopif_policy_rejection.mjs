#!/usr/bin/env node
// Bounded reproduction harness for agent-browser issue #2574's localhost parent
// and 127.0.0.1 child iframe shape. Usage: node .../run_oopif_policy_rejection.mjs [runs]
import { spawn, spawnSync } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';

const runs = Number(process.argv[2] ?? '3');
if (!Number.isInteger(runs) || runs < 1 || runs > 10) throw new Error('runs must be 1..10');
const root = process.cwd();
const stamp = new Date().toISOString().replaceAll(':', '').replaceAll('-', '').replace(/\.\d+Z$/, 'Z');
const reportDir = path.join(root, 'examples/benchmark/reports');
await mkdir(reportDir, { recursive: true });
const temp = await mkdtemp(path.join(os.tmpdir(), 'scout-oopif-policy-'));
const fixture = path.join(temp, 'fixture');
const scout = path.join(temp, 'scout');
const build = (binary, pkg) => {
  const result = spawnSync('go', ['build', '-o', binary, pkg], { cwd: root, encoding: 'utf8' });
  if (result.status !== 0) throw new Error(`go build ${pkg}: ${result.stderr}`);
};
build(fixture, './examples/benchmark/origin_policy_fixture');
build(scout, './cmd/scout');
const fixtureProc = spawn(fixture, ['-allowed-listen', '127.0.0.1:41745', '-denied-listen', '127.0.0.1:41746'], { stdio: ['ignore', 'ignore', 'pipe'] });
let fixtureStderr = '';
fixtureProc.stderr.on('data', chunk => fixtureStderr += chunk.toString());
const parentOrigin = 'http://localhost:41745';
const childOrigin = 'http://127.0.0.1:41746';
const pageURL = `${parentOrigin}/parent.html?child=${encodeURIComponent(childOrigin)}`;
const countURL = 'http://127.0.0.1:41745/count';
const resetURL = 'http://127.0.0.1:41745/reset';
async function count() { return (await (await fetch(countURL)).json()).denied_origin_hits; }
async function waitFixture() {
  for (let i = 0; i < 100; i++) { try { await count(); return; } catch { await delay(100); } }
  throw new Error(`fixture failed to start: ${fixtureStderr}`);
}
async function reset() { await fetch(resetURL); }
async function run(label, command, args, env, timeoutMs = 40000) {
  await reset();
  const proc = spawn(command, args, { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'], detached: process.platform !== 'win32' });
  let stdout = '', stderr = '', timedOut = false;
  proc.stdout.on('data', chunk => stdout += chunk.toString());
  proc.stderr.on('data', chunk => stderr += chunk.toString());
  const exit = await new Promise(resolve => {
    const timer = setTimeout(() => {
      timedOut = true;
      if (proc.pid) {
        try { if (process.platform === 'win32') proc.kill('SIGKILL'); else process.kill(-proc.pid, 'SIGKILL'); }
        catch { proc.kill('SIGKILL'); }
      }
    }, timeoutMs);
    proc.once('close', (code, signal) => {
      clearTimeout(timer);
      resolve({ code, signal });
    });
  });
  await delay(500);
  const row = { label, exit_code: exit.code, signal: exit.signal, timed_out: timedOut, denied_child_requests: await count(), page_readable: /OOPIF policy fixture|Parent ready/.test(stdout), stdout, stderr };
  return row;
}

const results = { scout: [], 'agent-browser': [] };
try {
  await waitFixture();
  const env = { ...process.env, AGENT_BROWSER_NO_VERSION_CHECK: '1', AGENT_BROWSER_ALLOWED_DOMAINS: 'localhost' };
  for (let i = 1; i <= runs; i++) {
    results.scout.push(await run(`scout-${i}`, scout, ['session', '--headless', '--allow-private-ips', `--allowed-origins=${parentOrigin}`, `--url=${pageURL}`], process.env));
    const session = `scout-oopif-${stamp}-${i}`;
    const peerEnv = { ...env, AGENT_BROWSER_ALLOWED_DOMAINS: 'localhost' };
    const row = await run(`agent-browser-${i}`, 'npx', ['--yes', '--package=agent-browser@0.42.0', 'agent-browser', '--no-auto-connect', '--session', session, 'open', pageURL], peerEnv);
    const cleanup = spawnSync('npx', ['--yes', '--package=agent-browser@0.42.0', 'agent-browser', '--no-auto-connect', '--session', session, 'close'], { cwd: root, env: peerEnv, encoding: 'utf8', timeout: 10000 });
    row.cleanup_exit_code = cleanup.status;
    results['agent-browser'].push(row);
  }
  const report = { schema_version: 1, scenario: 'cross-origin-site-isolated-child-iframe-policy-rejection', captured_at: new Date().toISOString(), source: { git_revision: spawnSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).stdout.trim(), dirty_worktree: true }, versions: { scout: spawnSync(scout, ['version'], { encoding: 'utf8' }).stdout.trim(), agent_browser: '0.42.0', chrome: 'local default Chrome' }, fixture: { page_url: pageURL, parent_origin: parentOrigin, denied_child_origin: childOrigin, independent_oracle: 'HTTP hit count at child fixture listener' }, policy: 'Scout --allowed-origins exact parent origin; agent-browser AGENT_BROWSER_ALLOWED_DOMAINS=localhost', runs, results, limitations: ['This is the reported local iframe shape, not the reported Stripe Elements page.', 'A timeout is a bounded hang observation, not proof of a general product defect.', 'A child hit is counted only after the child document reaches the fixture listener.', 'This workstation and browser version may differ from the issue reporter environment.'] };
  const output = path.join(reportDir, `oopif-policy-rejection-${stamp}.json`);
  await writeFile(output, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify({ report: path.relative(root, output), scout_readable: results.scout.filter(row => row.page_readable).length, scout_denied_requests: results.scout.map(row => row.denied_child_requests), agent_browser_timeouts: results['agent-browser'].filter(row => row.timed_out).length, agent_browser_denied_requests: results['agent-browser'].map(row => row.denied_child_requests) }, null, 2));
} finally {
  fixtureProc.kill('SIGTERM');
  await rm(temp, { recursive: true, force: true });
}
