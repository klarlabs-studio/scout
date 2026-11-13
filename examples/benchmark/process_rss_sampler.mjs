#!/usr/bin/env node
// Sample a runner plus command-signature-selected workload processes.
// Usage: node process_rss_sampler.mjs <report.json> -- <command> [args...]
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { setTimeout as delay } from 'node:timers/promises';

const divider = process.argv.indexOf('--');
if (divider < 2 || divider === process.argv.length - 1) {
  throw new Error('usage: node process_rss_sampler.mjs <report.json> -- <command> [args...]');
}
const reportPath = process.argv[2];
const command = process.argv[divider + 1];
const args = process.argv.slice(divider + 2);
const intervalMs = Number(process.env.RSS_SAMPLE_INTERVAL_MS ?? 50);
if (!Number.isInteger(intervalMs) || intervalMs < 10 || intervalMs > 10000) {
  throw new Error('RSS_SAMPLE_INTERVAL_MS must be an integer from 10 to 10000');
}
const workloadPattern = process.env.RSS_WORKLOAD_PATTERN ?? '';
if (!workloadPattern) throw new Error('RSS_WORKLOAD_PATTERN is required to attribute workload processes safely');
const workloadRE = new RegExp(workloadPattern);
const excludePattern = process.env.RSS_EXCLUDE_PATTERN ? new RegExp(process.env.RSS_EXCLUDE_PATTERN) : null;
const selectedPids = new Set((process.env.RSS_PIDS ?? '').split(',').map(value => Number(value.trim())).filter(Number.isInteger));

const child = spawn(command, args, { stdio: 'inherit', env: process.env });
let peakRssBytes = 0;
let peakSampleProcesses = [];
let sampleCount = 0;
let stopped = false;
let samplerError;
const stop = () => { stopped = true; };
process.once('SIGINT', stop);
process.once('SIGTERM', stop);

function processWorkload(rootPid) {
  const rows = spawnSync('ps', ['-axo', 'pid=,ppid=,rss=,command='], { encoding: 'utf8' });
  if (rows.status !== 0) throw new Error(`ps failed: ${rows.stderr}`);
  const processes = rows.stdout.split('\n').flatMap(line => {
    const match = line.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(.*)$/);
    return match ? [{ pid: Number(match[1]), ppid: Number(match[2]), rssKb: Number(match[3]), command: match[4] }] : [];
  });
  const root = processes.find(proc => proc.pid === rootPid);
  if (!root) return [];
  const includeRoot = process.env.RSS_INCLUDE_ROOT !== '0';
  const selected = new Map(includeRoot ? [[root.pid, root]] : []);
  for (const proc of processes) {
    if (proc.pid !== process.pid && !excludePattern?.test(proc.command) && (selectedPids.has(proc.pid) || workloadRE.test(proc.command))) selected.set(proc.pid, proc);
  }
  return [...selected.values()];
}

async function sample() {
  while (!stopped && child.exitCode === null && child.signalCode === null) {
    try {
      const current = processWorkload(child.pid);
      const bytes = current.reduce((total, proc) => total + proc.rssKb * 1024, 0);
      sampleCount += 1;
      if (bytes > peakRssBytes) {
        peakRssBytes = bytes;
        peakSampleProcesses = current.map(({ pid, rssKb, command: cmd }) => ({ pid, rss_kb: rssKb, command: cmd }));
      }
    } catch (error) {
      samplerError = error instanceof Error ? error.message : String(error);
      stopped = true;
    }
    await delay(intervalMs);
  }
}

const startedAt = new Date();
const sampling = sample();
const exitCode = await new Promise((resolve, reject) => {
  child.once('error', reject);
  child.once('close', resolve);
});
stopped = true;
await sampling;

const report = {
  schema_version: 1,
  benchmark: 'process-tree-rss-sample',
  captured_at_utc: new Date().toISOString(),
  started_at_utc: startedAt.toISOString(),
  platform: `${os.platform()} ${os.arch()}`,
  sample_interval_ms: intervalMs,
  sample_count: sampleCount,
  root_pid: child.pid,
  root_command: [command, ...args].join(' '),
  exit_code: exitCode,
  peak_rss_bytes: peakRssBytes,
  peak_rss_kb: Math.round(peakRssBytes / 1024),
  peak_sample_processes: peakSampleProcesses,
  sampling_scope: `runner process plus processes matching RSS_WORKLOAD_PATTERN=${workloadPattern}${excludePattern ? ` excluding RSS_EXCLUDE_PATTERN=${excludePattern}` : ''}`,
  category_peaks_kb: {
    scout_probe_client: Math.max(0, ...peakSampleProcesses.filter(proc => /memory_scout_probe/.test(proc.command)).map(proc => proc.rss_kb)),
    go_toolchain: peakSampleProcesses.filter(proc => /\/go-build\/|go run \.\/cmd\/scout/.test(proc.command)).reduce((sum, proc) => sum + proc.rss_kb, 0),
    playwright_mcp_server: Math.max(0, ...peakSampleProcesses.filter(proc => /playwright-mcp /.test(proc.command)).map(proc => proc.rss_kb)),
    isolated_chrome_session: peakSampleProcesses.filter(proc => /playwright_chromiumdev_profile-/.test(proc.command)).reduce((sum, proc) => sum + proc.rss_kb, 0),
    scout_mcp_server: Math.max(0, ...peakSampleProcesses.filter(proc => /scout mcp serve/.test(proc.command)).map(proc => proc.rss_kb)),
  },
  sampling_method: 'sum of ps RSS for the selected process set at each sample; shared pages may be double counted, and short-lived peaks between samples may be missed',
};
if (samplerError) report.sampler_error = samplerError;
const absoluteReportPath = path.resolve(reportPath);
fs.mkdirSync(path.dirname(absoluteReportPath), { recursive: true });
fs.writeFileSync(absoluteReportPath, `${JSON.stringify(report, null, 2)}\n`);
process.stdout.write(`process_tree_peak_rss=${report.peak_rss_kb} KiB (${report.peak_rss_bytes} bytes)\n`);
process.stdout.write(`rss_samples=${sampleCount} interval_ms=${intervalMs}\n`);
process.stdout.write(`rss_report=${absoluteReportPath}\n`);
if (samplerError) throw new Error(samplerError);
if (exitCode !== 0) process.exitCode = typeof exitCode === 'number' ? exitCode : 1;
