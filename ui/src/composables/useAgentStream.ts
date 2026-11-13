import { ref, reactive } from "vue";
import { watch } from "vue";
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

const conversationStorageKey = "scout.conversation.v3";
// The AG-UI server expires idle browser sessions after ten minutes. Keep the
// client-side recovery window aligned so stale transcripts do not masquerade
// as a live browser session after a longer absence.
const conversationMaxAgeMs = 10 * 60 * 1000;
const conversationMaxMessages = 40;
const requestMaxMessages = 80;
const requestMaxBodyBytes = 768 * 1024;
const conversationMaxContentChars = 4000;
const conversationMaxActions = 40;
const conversationMaxActionChars = 1000;

interface PersistedConversation {
  savedAt: number;
  threadId: string;
  messages: ChatMessage[];
  actions: ToolAction[];
}

function newThreadId() {
  return `scout-${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`;
}

function loadConversation(): PersistedConversation | null {
  try {
    const raw = globalThis.localStorage?.getItem(conversationStorageKey);
    if (!raw) return null;
    const value = JSON.parse(raw) as Partial<PersistedConversation>;
    if (
      typeof value.savedAt !== "number" ||
      Date.now() - value.savedAt > conversationMaxAgeMs ||
      value.savedAt > Date.now() + 60_000 ||
      typeof value.threadId !== "string" ||
      !Array.isArray(value.messages) ||
      !value.messages.every((message) =>
        message && typeof message === "object" &&
        typeof message.id === "string" &&
        ["user", "assistant", "tool"].includes(message.role) &&
        typeof message.content === "string"
      ) ||
      !Array.isArray(value.actions) ||
      !value.actions.every((action) =>
        action && typeof action === "object" &&
        typeof action.id === "string" &&
        typeof action.name === "string" &&
        typeof action.args === "string" &&
        ["running", "done", "error", "unknown"].includes(action.status)
      )
    ) {
      globalThis.localStorage?.removeItem(conversationStorageKey);
      return null;
    }
    return value as PersistedConversation;
  } catch {
    // Storage may be disabled, full, or contain an older/invalid payload.
    return null;
  }
}

function boundedText(value: string, maxChars: number) {
  return value.length <= maxChars ? value : `${value.slice(0, maxChars)}… [truncated]`;
}

function completeHistoryGroups(messages: ChatMessage[]) {
  const eligible = messages.filter((message) =>
    message.role === "user" ||
    (message.role === "assistant" && message.status === "done") ||
    message.role === "tool"
  );
  const groups: ChatMessage[][] = [];
  for (const message of eligible) {
    if (message.role === "user" || groups.length === 0) groups.push([]);
    groups[groups.length - 1].push(message);
  }
  return groups;
}

function historyTail(messages: ChatMessage[], maxMessages: number) {
  const groups = completeHistoryGroups(messages);
  const selected: ChatMessage[][] = [];
  let count = 0;
  for (let index = groups.length - 1; index >= 0; index--) {
    const group = groups[index];
    if (selected.length > 0 && count + group.length > maxMessages) break;
    selected.unshift(group);
    count += group.length;
  }
  return selected.flat();
}

function requestHistory(messages: ChatMessage[], threadId: string, runId: string) {
  const toInput = (message: ChatMessage) => ({
    id: message.id,
    role: message.role,
    content: message.content,
    toolCallId: message.toolCallId,
    toolCalls: message.toolCalls?.map((call) => ({
      id: call.id,
      name: call.name,
      arguments: call.arguments,
    })),
  });

  const groups = completeHistoryGroups(messages);
  let selected: ChatMessage[][] = [];
  let count = 0;
  for (let index = groups.length - 1; index >= 0; index--) {
    const group = groups[index];
    if (selected.length > 0 && count + group.length > requestMaxMessages) break;
    selected.unshift(group);
    count += group.length;
    const candidate = {
      threadId,
      runId,
      messages: selected.flat().map(toInput),
    };
    if (new TextEncoder().encode(JSON.stringify(candidate)).byteLength > requestMaxBodyBytes) {
      selected.shift();
      count -= group.length;
      break;
    }
  }
  if (selected.length === 0 && groups.length > 0) {
    const current = groups[groups.length - 1].map(toInput);
    const size = new TextEncoder().encode(JSON.stringify({ threadId, runId, messages: current })).byteLength;
    if (size > requestMaxBodyBytes || current.length > requestMaxMessages) {
      throw new Error("This turn is too large to send. Shorten the prompt or clear the conversation before trying again.");
    }
    return current;
  }
  return selected.flat().map(toInput);
}

let runCounter = 0;

export function useAgentStream(endpoint = "/api") {
  const restored = loadConversation();
  const messages = ref<ChatMessage[]>(restored?.messages ?? []);
  const actions = ref<ToolAction[]>(
    restored?.actions.map((action) => action.status === "running"
      ? {
          ...action,
          status: "unknown" as const,
          result: "The page was reloaded before a result arrived. This action may have completed; inspect the page and application state before retrying.",
        }
      : action) ?? [],
  );
  // Browser state is live session state, so never restore it from a stale
  // client snapshot. The backend sends an authoritative snapshot on each run.
  const browserState = reactive<BrowserState>({ ...defaultBrowserState });
  const isRunning = ref(false);
  const isClearing = ref(false);
  const isNavigating = ref(false);
  const error = ref<string | null>(null);

  // Reuse the same backend session after reload so visible history and browser
  // state remain attached to the thread the user is continuing.
  let threadId = restored?.threadId ?? newThreadId();

  function persistConversation() {
    try {
      const safeMessages = historyTail(messages.value, conversationMaxMessages).map((message) => ({
        ...message,
        content: boundedText(message.content, conversationMaxContentChars),
        toolCalls: message.toolCalls?.map((call) => ({
          ...call,
          arguments: boundedText(call.arguments, conversationMaxActionChars),
        })),
      }));
      const safeActions = actions.value.slice(0, conversationMaxActions).map((action) => ({
        ...action,
        args: boundedText(action.args, conversationMaxActionChars),
        result: action.result ? boundedText(action.result, conversationMaxActionChars) : undefined,
      }));
      globalThis.localStorage?.setItem(conversationStorageKey, JSON.stringify({
        savedAt: Date.now(),
        threadId,
        messages: safeMessages,
        actions: safeActions,
      } satisfies PersistedConversation));
    } catch {
      // The conversation remains usable for this page lifetime when storage is unavailable.
    }
  }

  watch([messages, actions], persistConversation, { deep: true });

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
  let currentToolMessageId = "";
  // Every stream owns a generation. Clearing the conversation invalidates the
  // current generation so late SSE events and its finally block cannot write
  // into a cleared conversation or reset a newer run's state.
  let runGeneration = 0;

  function stop() {
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
  }

  async function send(text: string) {
    if (isRunning.value || isClearing.value) return;

    const userMsg: ChatMessage = {
      id: `user-${Date.now()}`,
      role: "user",
      content: text,
      status: "done",
    };
    const runId = `run-${++runCounter}`;
    let requestMessages: ReturnType<typeof requestHistory>;
    try {
      requestMessages = requestHistory([...messages.value, userMsg], threadId, runId);
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
      return;
    }

    const generation = ++runGeneration;
    error.value = null;
    isRunning.value = true;
    abortController = new AbortController();

    messages.value = [...messages.value, userMsg];
    persistConversation();

    // Keep requests below the server's 1 MiB limit and avoid sending an
    // unbounded transcript. Tool-call/result pairs remain grouped at the head.
    const payload = {
      threadId,
      runId,
      messages: requestMessages,
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

          if (generation === runGeneration) handleEvent(event);
        }
      }
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") {
        // User stopped the request — not an error
      } else if (
        e instanceof TypeError &&
        /fetch|network|failed/i.test(e.message)
      ) {
        // A streaming request cannot resume after transport loss. The server may
        // have dispatched a browser mutation before the connection failed, so
        // do not invite blind replay; require an application-side state check.
        error.value =
          "Connection to Scout server lost. The last browser action may have completed. Inspect the page and application state before starting another run.";
      } else {
        error.value = e instanceof Error ? e.message : String(e);
      }
    } finally {
      if (generation !== runGeneration) return;
      // The stream may stop after the server dispatched a browser action but
      // before its result reached the client. Keep that uncertainty visible;
      // callers should inspect browser state before retrying mutations.
      actions.value = actions.value.map((action) =>
        action.status === "running"
          ? {
              ...action,
              status: "unknown" as const,
              result:
                "No result received. The action may have completed; inspect the page and application state before retrying.",
            }
          : action,
      );
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
      persistConversation();
    }

    function handleEvent(event: AGUIEvent) {
      switch (event.type) {
        case "TEXT_MESSAGE_START": {
          const msgId = event.messageId as string;
          currentMsgId = msgId;
          currentText = "";
          currentToolMessageId = "";
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
          if (!currentToolMessageId) {
            currentToolMessageId = `assistant-tools-${toolId}`;
            messages.value = [...messages.value, {
              id: currentToolMessageId,
              role: "assistant",
              content: "",
              status: "done",
              toolCalls: [],
            }];
          }

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
          const assistantCall = messages.value.find((m) => m.id === currentToolMessageId);
          if (assistantCall) {
            const calls = [...(assistantCall.toolCalls ?? [])];
            calls.push({ id: toolId, name: toolName, arguments: "" });
            messages.value = messages.value.map((m) =>
              m.id === assistantCall.id ? { ...m, toolCalls: calls } : m
            );
          }
          break;
        }

        case "TOOL_CALL_ARGS": {
          const toolId = event.toolCallId as string;
          const delta = event.delta as string;
          actions.value = actions.value.map((a) =>
            a.id === toolId ? { ...a, args: a.args + delta } : a
          );
          for (const msg of messages.value) {
            if (msg.role !== "assistant" || !msg.toolCalls?.some((call) => call.id === toolId)) continue;
            messages.value = messages.value.map((m) => m.id === msg.id ? {
              ...m,
              toolCalls: m.toolCalls?.map((call) => call.id === toolId
                ? { ...call, arguments: call.arguments + delta }
                : call),
            } : m);
            break;
          }
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
          messages.value = [...messages.value, {
            id: event.messageId as string,
            role: "tool",
            toolCallId: toolId,
            toolName,
            content,
            status: "done",
          }];

          // Keep the live browser panel synchronized after each captured image.
          try {
            const parsed = JSON.parse(content);
            if (parsed.image) browserState.screenshot = parsed.image;
          } catch {
            // Tool output may be plain text.
          }

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

  async function clear() {
    if (isRunning.value || isClearing.value) return;
    isClearing.value = true;
    error.value = null;
    try {
      const response = await fetch(`${endpoint.replace(/\/$/, "")}/session?threadId=${encodeURIComponent(threadId)}`, {
        method: "DELETE",
      });
      if (!response.ok) {
        throw new Error(`Server returned ${response.status}`);
      }
    } catch (err) {
      error.value = `Could not close the browser session, so the conversation was kept: ${err instanceof Error ? err.message : "request failed"}`;
      return;
    } finally {
      isClearing.value = false;
    }
    runGeneration++;
    messages.value = [];
    actions.value = [];
    toolNameMap.clear();
    Object.assign(browserState, defaultBrowserState);
    error.value = null;
    isRunning.value = false;
    isNavigating.value = false;
    browserState.activeTool = "";
    threadId = newThreadId();
    persistConversation();
  }

  return {
    messages,
    actions,
    browserState,
    isRunning,
    isClearing,
    isNavigating,
    error,
    send,
    stop,
    clear,
  };
}
