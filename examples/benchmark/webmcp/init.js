// Benchmark-only adapter for the shared action fixture. This script models a
// page that exposes one workflow through Chrome's experimental WebMCP API.
function registerFixtureTool() {
  if (!window.__scoutWebMCPClickCounter) {
    window.__scoutWebMCPClickCounter = true;
    window.__scoutWebMCPClicks = 0;
    document.addEventListener('click', event => {
      if (event.target?.closest?.('#complete')) window.__scoutWebMCPClicks++;
    }, true);
  }
  if (!document.modelContext?.registerTool) return false;
  if (window.__scoutFixtureWebMCPRegistered) return true;
  window.__scoutFixtureWebMCPRegistered = true;
  document.modelContext.registerTool({
    name: 'complete_task',
    description: 'Complete the visible benchmark task by activating its button.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    annotations: { readOnlyHint: false, consequentialHint: false },
    execute: async () => {
      const button = document.querySelector('#complete');
      if (!button) throw new Error('Task button is unavailable');
      button.click();
      return document.querySelector('#status')?.textContent ?? 'missing status';
    },
  });
  return true;
}

if (!registerFixtureTool()) {
  const retry = setInterval(() => {
    if (registerFixtureTool()) clearInterval(retry);
  }, 25);
  setTimeout(() => clearInterval(retry), 5000);
}
