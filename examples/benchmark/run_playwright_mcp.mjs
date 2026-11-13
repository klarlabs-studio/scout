#!/usr/bin/env node
// Repeated, pinned Playwright MCP comparison against Scout's local fixtures.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { performance } from 'node:perf_hooks';

const [version, repetitionsText, baseURL, resultsDir] = process.argv.slice(2);
if (!version || !repetitionsText || !baseURL || !resultsDir) {
  throw new Error('usage: node run_playwright_mcp.mjs <pinned-version> <repetitions>=10 <base-url> <results-dir>');
}
const repetitions = Number(repetitionsText);
if (!Number.isInteger(repetitions) || repetitions < 10) throw new Error('repetitions must be an integer >= 10');
const verifiedActionOnly = process.env.BENCHMARK_SCENARIO === 'verified-action';
const lateRecoveryOnly = process.env.BENCHMARK_SCENARIO === 'late-recovery';
const backgroundTabOnly = process.env.BENCHMARK_SCENARIO === 'background-tab';
const frameCloseOnly = process.env.BENCHMARK_SCENARIO === 'frame-close';
fs.mkdirSync(resultsDir, { recursive: true });

class MCPClient {
  constructor(child) {
    this.child = child;
    this.buffer = Buffer.alloc(0);
    this.pending = new Map();
    this.nextId = 1;
    this.transcript = [];
    child.stdout.on('data', chunk => this.consume(chunk));
    child.on('exit', (code, signal) => {
      for (const { reject } of this.pending.values()) reject(new Error(`Playwright MCP exited (${code ?? signal})`));
      this.pending.clear();
    });
  }
  consume(chunk) {
    this.buffer = Buffer.concat([this.buffer, chunk]);
    while (true) {
      const boundary = this.buffer.indexOf('\n');
      if (boundary < 0) return;
      const bytes = this.buffer.subarray(0, boundary);
      this.buffer = this.buffer.subarray(boundary + 1);
      if (!bytes.length) continue;
      const message = JSON.parse(bytes.toString('utf8'));
      this.transcript.push({ direction: 'server', message });
      if (message.id !== undefined && this.pending.has(message.id)) {
        const p = this.pending.get(message.id);
        this.pending.delete(message.id);
        message.error ? p.reject(new Error(JSON.stringify(message.error))) : p.resolve(message.result);
      }
    }
  }
  request(method, params = {}) {
    const id = this.nextId++;
    const message = { jsonrpc: '2.0', id, method, params };
    const body = Buffer.from(JSON.stringify(message));
    this.transcript.push({ direction: 'client', message });
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.pending.delete(id); reject(new Error(`MCP request timed out: ${method}`)); }, 90000);
      this.pending.set(id, {
        resolve: result => { clearTimeout(timer); resolve(result); },
        reject: error => { clearTimeout(timer); reject(error); },
      });
      this.child.stdin.write(body);
      this.child.stdin.write('\n');
    });
  }
  notification(method, params = {}) {
    const message = { jsonrpc: '2.0', method, params };
    this.transcript.push({ direction: 'client', message });
    const body = Buffer.from(JSON.stringify(message));
    this.child.stdin.write(body);
    this.child.stdin.write('\n');
  }
  async callTool(name, args) {
    const response = await this.request('tools/call', { name, arguments: args });
    if (response.isError) throw new Error(`${name}: ${JSON.stringify(response.content)}`);
    return response;
  }
  async stop() {
    try { await this.request('tools/call', { name: 'browser_close', arguments: {} }); } catch {}
    this.child.stdin.end();
    this.child.kill('SIGTERM');
  }
}

const child = spawn('npx', ['--yes', `@playwright/mcp@${version}`, '--headless', '--browser', 'chrome', '--no-sandbox', '--isolated'], { stdio: ['pipe', 'pipe', 'pipe'] });
let stderr = '';
child.stderr.on('data', chunk => { stderr += chunk.toString(); });
const client = new MCPClient(child);
const recordPath = path.join(resultsDir, 'playwright-mcp-transcript.jsonl');
fs.writeFileSync(recordPath, '');
const record = event => fs.appendFileSync(recordPath, `${JSON.stringify(event)}\n`);
const resultText = result => result.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') || '';
const evaluationValue = result => {
  const text = resultText(result);
  const match = text.match(/### Result\n([\s\S]*?)(?:\n### Ran Playwright code|\n### Page|$)/);
  if (!match) return undefined;
  try { return JSON.parse(match[1].trim()); } catch { return undefined; }
};
const regexEscape = value => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const currentTabMatches = (tabText, title, url) =>
  new RegExp(`- \\d+: \\(current\\) \\[${regexEscape(title)}\\]\\(${regexEscape(url)}\\)`).test(tabText);
const lateActionState = async () => {
  const response = await fetch(`${baseURL}/late-action-state`);
  if (!response.ok) throw new Error(`late-action state endpoint returned ${response.status}`);
  return response.json();
};
const privacySentinel = 'SCOUT_PRIVATE_SENTINEL_6b29f0a1';
try {
  const initialized = await client.request('initialize', {
    protocolVersion: '2025-03-26',
    capabilities: {},
    clientInfo: { name: 'scout-competitive-benchmark', version: '1.0.0' },
  });
  record({ type: 'initialize', result: initialized });
  client.notification('notifications/initialized');
  const tools = await client.request('tools/list');
  record({ type: 'tools/list', result: tools });
  const names = new Set(tools.tools.map(tool => tool.name));
  for (const required of ['browser_navigate', 'browser_snapshot', 'browser_click', 'browser_evaluate', 'browser_tabs', 'browser_close']) {
    if (!names.has(required)) throw new Error(`pinned Playwright MCP lacks required tool ${required}`);
  }
  const evaluate = tools.tools.find(tool => tool.name === 'browser_evaluate');
  const evalArg = evaluate.inputSchema.properties;
  const evalKey = Object.keys(evalArg).find(key => ['code', 'expression', 'function'].includes(key));
  if (!evalKey) throw new Error(`unrecognized browser_evaluate schema: ${JSON.stringify(evaluate.inputSchema)}`);
  const metadata = await client.callTool('browser_navigate', { url: `${baseURL}/action` });
  record({ type: 'metadata-navigate', result: metadata });
  const ua = await client.callTool('browser_evaluate', { [evalKey]: '() => navigator.userAgent' });
  record({ type: 'browser-user-agent', result: ua });
  const uaText = ua.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') || JSON.stringify(ua);
  const browserVersion = uaText.match(/HeadlessChrome\/(\d+(?:\.\d+)+)/)?.[1] || uaText.match(/Chrome\/(\d+(?:\.\d+)+)/)?.[1] || 'unknown';

  const rows = [];
  if (lateRecoveryOnly) {
    const deadlineMs = 300;
    for (let repetition = 1; repetition <= repetitions; repetition++) {
      const started = performance.now();
      const before = await lateActionState();
      const nav = await client.callTool('browser_navigate', { url: `${baseURL}/late-action` });
      const clickPromise = client.callTool('browser_click', { target: '#late-action' });
      const clickSettled = clickPromise.then(result => ({ result }), error => ({ error: String(error) }));
      const dispatchDeadline = performance.now() + 10000;
      let dispatchState;
      while (true) {
        dispatchState = await lateActionState();
        if (dispatchState.started_count > before.started_count) break;
        if (performance.now() >= dispatchDeadline) throw new Error(`no confirmed click dispatch in repetition ${repetition}`);
        await new Promise(resolve => setTimeout(resolve, 10));
      }
      const deadlineResult = await Promise.race([
        clickSettled.then(value => ({ completed: true, value })),
        new Promise(resolve => setTimeout(() => resolve({ completed: false }), deadlineMs)),
      ]);
      let timedOut = !deadlineResult.completed;
      let clickResult = deadlineResult.value?.result;
      const completionDeadline = performance.now() + 10000;
      let completedState;
      while (true) {
        completedState = await lateActionState();
        if (completedState.completed_count > before.completed_count) break;
        if (performance.now() >= completionDeadline) throw new Error(`late page mutation did not complete in repetition ${repetition}`);
        await new Promise(resolve => setTimeout(resolve, 10));
      }
      const settledAfterCompletion = await clickSettled;
      if (!clickResult && settledAfterCompletion.result) clickResult = settledAfterCompletion.result;
      const observed = await client.callTool('browser_evaluate', { [evalKey]: '() => document.querySelector("#late-status")?.textContent' });
      const observedValue = evaluationValue(observed);
      const serializedResults = { navigate: nav, click: clickResult, inspect: observed };
      const privacySentinelLeaked = JSON.stringify(serializedResults).includes(privacySentinel);
      const elapsed = performance.now() - started;
      const resultBytes = Object.values(serializedResults).filter(Boolean).reduce((sum, item) => sum + Buffer.byteLength(JSON.stringify(item)), 0);
      const row = {
        scenario: 'late-action-caller-timeout', repetition,
        success: timedOut && observedValue === 'Late completion',
        caller_wait_ended_by_deadline: timedOut,
        browser_dispatch_confirmed: true,
        completion_confirmed: true,
        native_recovery_record: false,
        observed_text: observedValue,
        workflow_duration_ms: elapsed,
        tool_calls: 3,
        response_payload_bytes: resultBytes,
        serialized_tool_results: serializedResults,
        serialized_tool_results_kind: 'raw MCP JSON-RPC tool result objects',
        privacy_sentinel_leaked: privacySentinelLeaked,
      };
      rows.push(row);
      record({ type: 'late-action-caller-timeout', repetition, nav, caller_deadline_ms_after_dispatch: deadlineMs, deadline_result: deadlineResult, eventual_click_result: settledAfterCompletion, observed, dispatch_state: dispatchState, completion_state: completedState });
    }
  } else if (verifiedActionOnly || (!backgroundTabOnly && !lateRecoveryOnly && !frameCloseOnly)) for (let repetition = 1; repetition <= repetitions; repetition++) {
    const started = performance.now();
    const nav = await client.callTool('browser_navigate', { url: `${baseURL}/action` });
    const snapshot = await client.callTool('browser_snapshot', {});
    const click = await client.callTool('browser_click', { target: '#complete' });
    const observed = await client.callTool('browser_evaluate', { [evalKey]: '() => document.querySelector("#status")?.textContent' });
    const elapsed = performance.now() - started;
    const observedText = resultText(observed);
    const observedValue = evaluationValue(observed);
    const success = observedValue === 'Task complete';
    const responseBytes = [nav, snapshot, click, observed].reduce((sum, item) => sum + Buffer.byteLength(JSON.stringify(item)), 0);
    const row = { scenario: 'verified-action', repetition, success, observed_text: observedValue ?? observedText, workflow_duration_ms: elapsed, tool_calls: 4, response_payload_bytes: responseBytes };
    rows.push(row);
    record({ type: 'verified-action', repetition, nav, snapshot, click, observed });
  }

  const runScenario = async (scenario, repetition, operation) => {
    const started = performance.now();
    const evidence = await operation();
    const elapsed = performance.now() - started;
    const calls = evidence.results;
    const bytes = calls.reduce((sum, item) => sum + Buffer.byteLength(JSON.stringify(item)), 0);
    const row = { scenario, repetition, success: evidence.success, observed: evidence.observed, workflow_duration_ms: elapsed, tool_calls: calls.length, response_payload_bytes: bytes };
    rows.push(row);
    record({ type: scenario, repetition, ...evidence });
    return row;
  };
  const actionRows = rows.slice();
  const actionMedian = values => {
    const sortedValues = values.map(row => row.workflow_duration_ms).sort((a, b) => a - b);
    return { median: sortedValues[Math.floor(sortedValues.length / 2)], p95: sortedValues[Math.ceil(sortedValues.length * 0.95) - 1], samples: values.map(row => row.workflow_duration_ms) };
  };

  const runBackgroundTab = async repetition => runScenario('background-tab-action', repetition, async () => {
    const runURL = `${baseURL}/background-tab?run=${repetition}`;
    const guardURL = `${baseURL}/foreground-tab?run=${repetition}`;
    const nav = await client.callTool('browser_navigate', { url: runURL });
    const foreground = await client.callTool('browser_tabs', { action: 'new', url: guardURL });
    const tabsBefore = await client.callTool('browser_tabs', { action: 'list' });
    const selectTask = await client.callTool('browser_tabs', { action: 'select', index: 0 });
    const tabsSelected = await client.callTool('browser_tabs', { action: 'list' });
    const beforeClick = await client.callTool('browser_evaluate', { [evalKey]: '() => ({url: location.href, title: document.title, visibility: document.visibilityState, transitions: window.__visibilityTransitions, status: document.querySelector("#status")?.textContent})' });
    const click = await client.callTool('browser_click', { target: '#background-action' });
    const observed = await client.callTool('browser_evaluate', { [evalKey]: '() => ({url: location.href, title: document.title, visibility: document.visibilityState, transitions: window.__visibilityTransitions, status: document.querySelector("#status")?.textContent})' });
    const tabsAfter = await client.callTool('browser_tabs', { action: 'list' });
    const beforeValue = evaluationValue(beforeClick) || {};
    const value = evaluationValue(observed) || {};
    const transitions = Array.isArray(value.transitions) ? value.transitions : [];
    const beforeTabsText = resultText(tabsBefore);
    const selectedTabsText = resultText(tabsSelected);
    const afterTabsText = resultText(tabsAfter);
    const expectedTaskURL = runURL;
    const taskWasListed = beforeTabsText.includes(expectedTaskURL) && beforeTabsText.includes(guardURL);
    const taskSelected = currentTabMatches(selectedTabsText, 'Background tab task', expectedTaskURL);
    const taskStillSelected = currentTabMatches(afterTabsText, 'Background tab task', expectedTaskURL);
    const foregroundTabPreserved = afterTabsText.includes(guardURL);
    const targetIdentityVerified = taskWasListed && taskSelected && taskStillSelected && foregroundTabPreserved && beforeValue.url === expectedTaskURL && value.url === expectedTaskURL && beforeValue.title === 'Background tab task' && value.title === 'Background tab task';
    const success = targetIdentityVerified && value.status === 'Background task complete';
    const closeGuard = await client.callTool('browser_tabs', { action: 'close', index: 1 });
    return {
      results: [nav, foreground, tabsBefore, selectTask, tabsSelected, beforeClick, click, observed, tabsAfter, closeGuard],
      success,
      observed: {
        target_identity_verified: targetIdentityVerified,
        task_was_listed: taskWasListed,
        task_selected_after_switch: taskSelected,
        task_still_selected_after_action: taskStillSelected,
        expected_task_url: expectedTaskURL,
        task_url_before_action: beforeValue.url,
        task_url_after_action: value.url,
        visibility_transitions: transitions,
        hidden_observed: transitions.includes('hidden'),
        visible_after_switch: transitions.at(-1) === 'visible' && value.visibility === 'visible',
        visibility_contract_diagnostic: 'headless Chrome visibility transitions are recorded but are not a target-identity success gate',
        status_text: value.status,
        foreground_tab_preserved: foregroundTabPreserved,
      },
    };
  });

  const runFrameClose = async repetition => runScenario('frame-scope-after-unrelated-tab-close', repetition, async () => {
    const nav = await client.callTool('browser_navigate', { url: `${baseURL}/iframe?run=${repetition}` });
    const snapshot = await client.callTool('browser_snapshot', {});
    const ref = findSnapshotRef(resultText(snapshot), 'Complete frame task');
    if (!ref) return { results: [nav, snapshot], success: false, observed: { ref_found: false, reason: 'frame button ref missing from snapshot' } };
    const background = await client.callTool('browser_tabs', { action: 'new', url: `${baseURL}/foreground-tab?run=${repetition}` });
    const tabsBefore = await client.callTool('browser_tabs', { action: 'list' });
    const beforeText = resultText(tabsBefore);
    const taskTabIndex = Number(beforeText.match(/- (\d+): \[Frame fixture\]\(/)?.[1]);
    const taskSelected = Number.isInteger(taskTabIndex) ? await client.callTool('browser_tabs', { action: 'select', index: taskTabIndex }) : undefined;
    const tabsSelected = await client.callTool('browser_tabs', { action: 'list' });
    const taskWasCurrent = currentTabMatches(resultText(tabsSelected), 'Frame fixture', `${baseURL}/iframe?run=${repetition}`);
    const closeUnrelated = await client.callTool('browser_tabs', { action: 'close', index: 1 });
    const click = await client.callTool('browser_click', { target: ref });
    const observed = await client.callTool('browser_evaluate', { [evalKey]: '() => ({url: location.href, frameText: document.querySelector("#task-frame")?.contentDocument?.querySelector("#frame-result")?.textContent, parentText: document.querySelector("#frame-result")?.textContent})' });
    const tabsAfter = await client.callTool('browser_tabs', { action: 'list' });
    const value = evaluationValue(observed) || {};
    const afterText = resultText(tabsAfter);
    const frameCompleted = value.frameText === 'Frame task complete';
    const taskStillCurrent = value.url === `${baseURL}/iframe?run=${repetition}` && currentTabMatches(afterText, 'Frame fixture', value.url);
    const unrelatedTabWasOpened = resultText(background).includes('foreground-tab') || beforeText.includes(`${baseURL}/foreground-tab?run=${repetition}`);
    const unrelatedTabWasClosed = !afterText.includes(`${baseURL}/foreground-tab?run=${repetition}`);
    return {
      results: [nav, snapshot, background, tabsBefore, taskSelected, tabsSelected, closeUnrelated, click, observed, tabsAfter],
      success: frameCompleted && taskWasCurrent && taskStillCurrent && unrelatedTabWasOpened && unrelatedTabWasClosed,
      observed: { ref, task_tab_index: taskTabIndex, frame_text: value.frameText, parent_text: value.parentText, task_url: value.url, frame_completed: frameCompleted, task_was_current: taskWasCurrent, task_still_current: taskStillCurrent, unrelated_tab_opened: unrelatedTabWasOpened, unrelated_tab_closed: unrelatedTabWasClosed, target_id_exposed: false },
    };
  });

  const findSnapshotRef = (snapshotText, accessibleName) => {
    const escaped = accessibleName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const match = snapshotText.match(new RegExp(`button \\"${escaped}\\" \\[ref=([^\\]]+)\\]`));
    return match?.[1] || '';
  };

  if (!verifiedActionOnly && !lateRecoveryOnly && !backgroundTabOnly && !frameCloseOnly) for (let repetition = 1; repetition <= repetitions; repetition++) {
    await runScenario('scoped-observation', repetition, async () => {
      const nav = await client.callTool('browser_navigate', { url: `${baseURL}/large` });
      const snapshot = await client.callTool('browser_snapshot', { target: 'main' });
      const snapshotText = resultText(snapshot);
      return { results: [nav, snapshot], success: snapshotText.includes('Target record 042') && !snapshotText.includes('Decoy record 042') && !snapshotText.includes('Decoy-only target'), observed: snapshotText.includes('Target record 042') ? 'target included; decoys excluded' : snapshotText.slice(0, 300) };
    });

    await runScenario('shadow-root-action', repetition, async () => {
      const nav = await client.callTool('browser_navigate', { url: `${baseURL}/shadow` });
      const snapshot = await client.callTool('browser_snapshot', {});
      const ref = findSnapshotRef(resultText(snapshot), 'Complete shadow task');
      const click = ref ? await client.callTool('browser_click', { target: ref }) : { content: [{ type: 'text', text: 'shadow action ref not found in accessibility snapshot' }] };
      const observed = await client.callTool('browser_evaluate', { [evalKey]: '() => document.querySelector("#shadow-result")?.textContent' });
      const observedText = resultText(observed);
      const observedValue = evaluationValue(observed);
      return { results: [nav, snapshot, click, observed], success: Boolean(ref) && observedValue === 'Shadow task complete', observed: { ref, result: observedValue ?? observedText } };
    });

    await runScenario('iframe-action', repetition, async () => {
      const nav = await client.callTool('browser_navigate', { url: `${baseURL}/iframe` });
      const snapshot = await client.callTool('browser_snapshot', {});
      const ref = findSnapshotRef(resultText(snapshot), 'Complete frame task');
      const click = ref ? await client.callTool('browser_click', { target: ref }) : { content: [{ type: 'text', text: 'frame action ref not found in accessibility snapshot' }] };
      const observed = await client.callTool('browser_evaluate', { [evalKey]: '() => document.querySelector("#task-frame")?.contentDocument?.querySelector("#frame-result")?.textContent' });
      const observedText = resultText(observed);
      const observedValue = evaluationValue(observed);
      return { results: [nav, snapshot, click, observed], success: Boolean(ref) && observedValue === 'Frame task complete', observed: { ref, result: observedValue ?? observedText } };
    });

    for (const formCase of [{ name: 'form-acceptance', path: '/form', endpoint: '/submit', expectedStatus: 200, expectedText: 'Order accepted' }, { name: 'form-rejection', path: '/reject-form', endpoint: '/reject', expectedStatus: 422, expectedText: 'Order accepted' }]) {
      await runScenario(formCase.name, repetition, async () => {
        const nav = await client.callTool('browser_navigate', { url: `${baseURL}${formCase.path}` });
        const snapshot = await client.callTool('browser_snapshot', {});
        const click = await client.callTool('browser_click', { target: '#checkout button' });
        const wait = await client.callTool('browser_wait_for', { text: formCase.expectedText, time: 3 });
        const status = await client.callTool('browser_network_requests', { filter: formCase.endpoint });
        const networkText = resultText(status);
        const observedStatus = Number(networkText.match(/\b(200|422)\b/)?.[1] || 0);
        const pageText = resultText(wait);
        const pageSaysAccepted = pageText.includes('Order accepted');
        const success = observedStatus === formCase.expectedStatus && pageSaysAccepted;
        return { results: [nav, snapshot, click, wait, status], success, observed: { http_status: observedStatus, page_says_accepted: pageSaysAccepted, ui_false_success_signal: formCase.name === 'form-rejection' && pageSaysAccepted && observedStatus >= 400, network_excerpt: networkText.slice(0, 500) } };
      });
    }

    await runScenario('caller-orchestrated-recovery', repetition, async () => {
      const nav = await client.callTool('browser_navigate', { url: `${baseURL}/replay` });
      const click = await client.callTool('browser_click', { target: '#advance' });
      const first = await client.callTool('browser_evaluate', { [evalKey]: '() => document.querySelector("#status")?.textContent' });
      const changed = await client.callTool('browser_evaluate', { [evalKey]: '() => { document.querySelector("#status").textContent = "Changed state"; return document.querySelector("#status").textContent }' });
      const repair = await client.callTool('browser_click', { target: '#repair' });
      const late = await client.callTool('browser_evaluate', { [evalKey]: '() => document.querySelector("#late")?.textContent' });
      const retry = await client.callTool('browser_click', { target: '#advance' });
      const final = await client.callTool('browser_evaluate', { [evalKey]: '() => document.querySelector("#status")?.textContent' });
      const firstValue = evaluationValue(first), changedValue = evaluationValue(changed), lateValue = evaluationValue(late), finalValue = evaluationValue(final);
      return { results: [nav, click, first, changed, repair, late, retry, final], success: firstValue === 'Step complete' && changedValue === 'Changed state' && lateValue === 'Ready for retry' && finalValue === 'Step complete', observed: { initial: firstValue, deliberate_change: changedValue, repair_target: lateValue, retry: finalValue, recovery_is_caller_orchestrated: true } };
    });

    await runBackgroundTab(repetition);
  }

  if (backgroundTabOnly) for (let repetition = 1; repetition <= repetitions; repetition++) await runBackgroundTab(repetition);
  if (frameCloseOnly) for (let repetition = 1; repetition <= repetitions; repetition++) await runFrameClose(repetition);

  fs.writeFileSync(path.join(resultsDir, 'playwright-mcp-repetitions.json'), `${JSON.stringify(rows, null, 2)}\n`);
  const scenarioSummaries = {};
  const scenarioContracts = {
    'late-action-caller-timeout': { path: '/late-action', predicate: "confirmed click dispatch; caller wait ends at 300 ms before mutation completion; after late completion, #late-status text equals 'Late completion'" },
    'verified-action': { path: '/action', predicate: "#status text exactly equals 'Task complete'" },
    'scoped-observation': { path: '/large', predicate: 'main-scoped accessibility snapshot includes Target record 042 and excludes Decoy record 042 and Decoy-only target' },
    'shadow-root-action': { path: '/shadow', predicate: "#shadow-result text exactly equals 'Shadow task complete' after clicking the snapshot ref" },
    'iframe-action': { path: '/iframe', predicate: "same-origin #frame-result text exactly equals 'Frame task complete' after clicking the snapshot ref" },
    'form-acceptance': { path: '/form', predicate: 'matching POST returns HTTP 200 and page text is Order accepted' },
    'form-rejection': { path: '/reject-form', predicate: 'matching POST returns HTTP 422 while the fixture still shows Order accepted, exposing the misleading UI signal' },
    'caller-orchestrated-recovery': { path: '/replay', predicate: 'initial action, changed expectation, repair, and retry states all match the recorded expected text' },
    'background-tab-action': { path: '/background-tab?run=<repetition>', predicate: 'unique task URL nonce remains selected before and after the action; guard tab remains present through action; status text is Background task complete; visibility transitions are diagnostic only' },
    'frame-scope-after-unrelated-tab-close': { path: '/iframe?run=<repetition>', predicate: 'capture the frame button ref, open then close an unrelated tab, click the captured ref, and independently verify frame result text is Frame task complete on the original URL' },
  };
  const durationScope = 'wall time from first browser_navigate call through final observation; excludes MCP launch, metadata probes, and close';
  const outputBytesDefinition = 'sum of UTF-8 JSON-serialized MCP result objects for all scenario calls; excludes JSON-RPC framing, metadata, and transcript envelope';
  for (const scenario of [...new Set(rows.map(row => row.scenario))]) {
    const scenarioRows = rows.filter(row => row.scenario === scenario);
    const contract = scenarioContracts[scenario];
    if (!contract) throw new Error(`missing scenario contract for ${scenario}`);
    scenarioSummaries[scenario] = {
      success: scenarioRows.every(row => row.success),
      successful_repetitions: scenarioRows.filter(row => row.success).length,
      repetitions: scenarioRows.length,
      workflow_duration_ms: actionMedian(scenarioRows),
      tool_calls: scenarioRows.reduce((sum, row) => sum + row.tool_calls, 0),
      tool_calls_per_repetition: scenarioRows[0].tool_calls,
      action_calls_per_repetition: scenarioRows[0].action_calls,
      output_bytes: scenarioRows.reduce((sum, row) => sum + row.response_payload_bytes, 0),
      fixture_url: lateRecoveryOnly ? 'local deterministic HandlerWithLateActionSignals /late-action' : `${baseURL}${contract.path}`,
      verification_predicate: contract.predicate,
      duration_scope: durationScope,
      output_bytes_definition: outputBytesDefinition,
      naive_false_success_signals: scenarioRows.filter(row => row.observed?.ui_false_success_signal).length,
    };
  }
  const actionDurations = actionMedian(actionRows);
  const summary = {
    benchmark_version: 'scout-browser-reliability-v28',
    comparison_protocol_version: 'scout-browser-comparison-v1',
    fixture_version: 'scout-browser-reliability-v28',
    tool_name: 'playwright-mcp',
    tool_version: version,
    browser_version: `HeadlessChrome ${browserVersion}`,
    browser_user_agent: uaText,
    scenario_id: lateRecoveryOnly ? 'late-action-caller-timeout' : (verifiedActionOnly ? 'verified-action' : (backgroundTabOnly ? 'background-tab-action' : (frameCloseOnly ? 'frame-scope-after-unrelated-tab-close' : 'multi-scenario'))),
    fixture_url: lateRecoveryOnly ? 'local deterministic HandlerWithLateActionSignals /late-action' : baseURL,
    operation: lateRecoveryOnly ? 'confirmed click dispatch, caller-side 300 ms deadline, then page-state inspection after late completion' : (backgroundTabOnly ? 'open a task and guard tab, select task tab, verify its unique URL before/after click, read status, and close guard tab' : 'verified action, scoped observation, accepted/rejected forms, caller-orchestrated recovery, and hidden-tab action; see scenarios for exact calls'),
    verification_predicate: lateRecoveryOnly ? scenarioContracts['late-action-caller-timeout'].predicate : 'per-scenario external predicates are recorded in repetition results and transcript',
    success: Object.values(scenarioSummaries).every(scenario => scenario.success),
    false_success: false,
    naive_false_success_signals: scenarioSummaries['form-rejection']?.naive_false_success_signals ?? 0,
    repetitions_per_scenario: repetitions,
    successful_repetitions: rows.filter(row => row.success).length,
    workflow_duration_ms: { ...actionDurations, includes: lateRecoveryOnly ? 'from navigation through deadline observation, late completion, and page inspection; excludes server startup and metadata probes' : backgroundTabOnly ? 'background-tab scenario: navigation, tab creation/selection, target URL checks, click and page-state observation; excludes server startup, metadata probes, and close' : frameCloseOnly ? 'frame-close scenario: navigation, snapshot, unrelated tab creation/closure, captured-ref action, and independent frame/page verification; excludes server startup, metadata probes, and close' : 'verified-action only: four MCP tool calls per repetition; excludes server startup, metadata probes, and close' },
    action_calls: rows.filter(row => row.scenario === 'verified-action').length,
    tool_calls: rows.reduce((sum, row) => sum + row.tool_calls, 0),
    output_bytes: rows.reduce((sum, row) => sum + row.response_payload_bytes, 0),
    scenarios: scenarioSummaries,
    output_bytes_definition: `${outputBytesDefinition}; per-scenario totals are reported separately`,
    raw_output_file: 'playwright-mcp-transcript.jsonl',
    raw_results_file: 'playwright-mcp-repetitions.json',
    native_recovery_record: false,
    recovery_method: lateRecoveryOnly ? 'client-side Promise.race ends caller waiting at the deadline but does not cancel the MCP request; the adapter waits for the fixture completion signal, then inspects the page without replay' : undefined,
    adapter_note: 'Direct MCP JSON-RPC stdio client; pinned package and browser=chrome; browser version recorded through navigator.userAgent.',
  };
  const scoutPath = path.join(resultsDir, 'scout-benchmark.json');
  if (fs.existsSync(scoutPath)) {
    const scout = JSON.parse(fs.readFileSync(scoutPath, 'utf8'));
    summary.benchmark_version = scout.benchmark_version;
    summary.fixture_version = scout.benchmark_version;
    const scoutScenarios = Array.isArray(scout.scenarios)
      ? scout.scenarios
      : (scout.scenario_id ? [{
          id: scout.scenario_id,
          success: scout.success,
          verified: scout.verification_state === 'verified',
          outcome: scout.outcome,
          action_calls: scout.action_calls_per_repetition,
          output_bytes: scout.output_bytes,
          verification_state: scout.verification_state,
        }] : []);
    const action = scoutScenarios.find(row => row.id === 'verified-action');
    const counterparts = {
      'verified-action': 'verified-action',
      'scoped-observation': 'scoped-large-page',
      'shadow-root-action': 'shadow-root-action',
      'iframe-action': 'iframe-action',
      'form-acceptance': 'form-acceptance',
      'form-rejection': 'form-rejection',
      'background-tab-action': 'background-tab-action',
    };
    summary.scout_comparison = { repetitions: 1, scenarios: {} };
    for (const [mcpName, scoutName] of Object.entries(counterparts)) {
      const action = scoutScenarios.find(row => row.id === scoutName);
      if (action) summary.scout_comparison.scenarios[mcpName] = {
        scout_scenario_id: action.id,
        success: action.success,
        verified: action.verified,
        outcome: action.outcome,
        verification_state: action.verification_state,
        action_calls: action.action_calls,
        tool_calls: 1,
        output_bytes: action.output_bytes,
      };
    }
    const rejection = scoutScenarios.find(row => row.id === 'form-rejection');
    if (rejection) summary.scout_comparison.scenarios['form-rejection'] = {
      scout_scenario_id: rejection.id,
      success: rejection.success,
      verified: rejection.verified,
      outcome: rejection.outcome,
      application_accepted: rejection.application_accepted,
      action_calls: rejection.action_calls,
      tool_calls: 1,
      response_observed: rejection.response_observed,
      status_code: rejection.status_code,
    };
  }
  fs.writeFileSync(path.join(resultsDir, 'playwright-mcp-comparison.json'), `${JSON.stringify(summary, null, 2)}\n`);
  console.log(JSON.stringify(summary, null, 2));
  if (!summary.success) process.exitCode = 1;
} catch (error) {
  fs.writeFileSync(path.join(resultsDir, 'playwright-mcp-error.txt'), `${error.stack || error}\n${stderr}`);
  throw error;
} finally {
  fs.appendFileSync(recordPath, `${JSON.stringify({ type: 'transcript-final', events: client.transcript.length })}\n`);
  await client.stop();
}
