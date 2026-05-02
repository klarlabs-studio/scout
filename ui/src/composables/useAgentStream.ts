import { ref, reactive } from "vue";
import type {
  AGUIEvent,
  BrowserState,
  ChatMessage,
  PatchOp,
  ToolAction,
} from "../types/agui";

const defaultBrowserState: BrowserState = {
  url: "",
  title: "",
  screenshot: "",
  elements: [],
  readyScore: 0,
  activeTool: "",
  tabCount: 0,
};

let runCounter = 0;

export function useAgentStream(endpoint = "/api") {
  const messages = ref<ChatMessage[]>([]);
  const actions = ref<ToolAction[]>([]);
  const browserState = reactive<BrowserState>({ ...defaultBrowserState });
  const isRunning = ref(false);
  const isNavigating = ref(false);
  const error = ref<string | null>(null);

  // Persist thread ID across sends so the browser session survives
  const threadId = `scout-${Date.now()}`;

  // Track tool names by ID during streaming
  const toolNameMap = new Map<string, string>();

  function applyPatch(ops: PatchOp[]) {
    for (const op of ops) {
      const key = op.path.split("/")[1] as keyof BrowserState;
      if (key && op.op === "replace" && op.value !== undefined) {
        (browserState as Record<string, unknown>)[key] = op.value;
      }
    }
  }

  let abortController: AbortController | null = null;

  function stop() {
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
  }

  async function send(text: string) {
    if (isRunning.value) return;

    error.value = null;
    isRunning.value = true;
    abortController = new AbortController();

    const userMsg: ChatMessage = {
      id: `user-${Date.now()}`,
      role: "user",
      content: text,
      status: "done",
    };
    messages.value = [...messages.value, userMsg];

    const runId = `run-${++runCounter}`;

    // Build messages payload from full history
    const payload = {
      threadId,
      runId,
      messages: messages.value
        .filter(
          (m) =>
            m.role === "user" ||
            (m.role === "assistant" && m.status === "done")
        )
        .map((m) => ({
          id: m.id,
          role: m.role,
          content: m.content,
        })),
    };

    let currentMsgId = "";
    let currentText = "";

    try {
      const resp = await fetch(endpoint, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
        },
        body: JSON.stringify(payload),
        signal: abortController?.signal,
      });

      if (!resp.ok) {
        const body = await resp.text();
        throw new Error(`Server error ${resp.status}: ${body}`);
      }

      const reader = resp.body?.getReader();
      if (!reader) throw new Error("No response body");

      const decoder = new TextDecoder();
      let buffer = "";

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });

        // SSE frame delimiter is a blank line (\n\n). Splitting on a single \n
        // misparses any frame that uses multi-line `data:` segments or comments.
        const frames = buffer.split("\n\n");
        buffer = frames.pop() ?? "";

        for (const frame of frames) {
          // A frame may contain multiple `data:` lines — concatenate them.
          const dataLines: string[] = [];
          for (const line of frame.split("\n")) {
            if (line.startsWith("data:")) {
              dataLines.push(line.slice(5).replace(/^ /, ""));
            }
          }
          if (dataLines.length === 0) continue;
          const raw = dataLines.join("\n").trim();
          if (!raw) continue;

          let event: AGUIEvent;
          try {
            event = JSON.parse(raw);
          } catch {
            continue;
          }

          handleEvent(event);
        }
      }
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") {
        // User stopped the request — not an error
      } else {
        error.value = e instanceof Error ? e.message : String(e);
      }
    } finally {
      abortController = null;
      if (currentMsgId) {
        messages.value = messages.value.map((m) =>
          m.id === currentMsgId
            ? { ...m, status: "done" as const }
            : m
        );
      }
      isRunning.value = false;
      isNavigating.value = false;
      browserState.activeTool = "";
    }

    function handleEvent(event: AGUIEvent) {
      switch (event.type) {
        case "TEXT_MESSAGE_START": {
          const msgId = event.messageId as string;
          currentMsgId = msgId;
          currentText = "";
          messages.value = [
            ...messages.value,
            {
              id: msgId,
              role: "assistant",
              content: "",
              status: "streaming",
            },
          ];
          break;
        }

        case "TEXT_MESSAGE_CONTENT": {
          const delta = event.delta as string;
          currentText += delta;
          messages.value = messages.value.map((m) =>
            m.id === currentMsgId ? { ...m, content: currentText } : m
          );
          break;
        }

        case "TEXT_MESSAGE_END": {
          messages.value = messages.value.map((m) =>
            m.id === currentMsgId
              ? { ...m, status: "done" as const }
              : m
          );
          currentMsgId = "";
          break;
        }

        case "TOOL_CALL_START": {
          const toolId = event.toolCallId as string;
          const toolName = event.toolCallName as string;
          toolNameMap.set(toolId, toolName);
          browserState.activeTool = toolName;

          // Flag navigation for loading shimmer
          if (toolName === "navigate") {
            isNavigating.value = true;
          }

          actions.value = [
            {
              id: toolId,
              name: toolName,
              args: "",
              timestamp: event.timestamp as number,
              status: "running",
            },
            ...actions.value,
          ];
          break;
        }

        case "TOOL_CALL_ARGS": {
          const toolId = event.toolCallId as string;
          const delta = event.delta as string;
          actions.value = actions.value.map((a) =>
            a.id === toolId ? { ...a, args: a.args + delta } : a
          );
          break;
        }

        case "TOOL_CALL_END": {
          // Tool args are complete, execution starts server-side
          break;
        }

        case "TOOL_CALL_RESULT": {
          const toolId = event.toolCallId as string;
          const content = event.content as string;
          const toolName = toolNameMap.get(toolId) ?? "";

          actions.value = actions.value.map((a) =>
            a.id === toolId
              ? { ...a, result: content, status: "done" as const }
              : a
          );

          browserState.activeTool = "";

          if (toolName === "navigate") {
            isNavigating.value = false;
          }

          // Check if result contains an error
          try {
            const parsed = JSON.parse(content);
            if (parsed.error) {
              actions.value = actions.value.map((a) =>
                a.id === toolId
                  ? { ...a, status: "error" as const }
                  : a
              );
            }
          } catch {
            // not JSON, ignore
          }
          break;
        }

        case "STATE_SNAPSHOT": {
          const state = event.state as BrowserState;
          Object.assign(browserState, state);
          break;
        }

        case "STATE_DELTA": {
          applyPatch(event.operations as PatchOp[]);
          break;
        }

        case "RUN_ERROR": {
          error.value = event.message as string;
          break;
        }
      }
    }
  }

  function clear() {
    messages.value = [];
    actions.value = [];
    toolNameMap.clear();
    Object.assign(browserState, defaultBrowserState);
    error.value = null;
    isNavigating.value = false;
  }

  return {
    messages,
    actions,
    browserState,
    isRunning,
    isNavigating,
    error,
    send,
    stop,
    clear,
  };
}
