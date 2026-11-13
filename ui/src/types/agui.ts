// AG-UI protocol event types consumed from the Go SSE stream.

export type EventType =
  | "RUN_STARTED"
  | "RUN_FINISHED"
  | "RUN_ERROR"
  | "TEXT_MESSAGE_START"
  | "TEXT_MESSAGE_CONTENT"
  | "TEXT_MESSAGE_END"
  | "TOOL_CALL_START"
  | "TOOL_CALL_ARGS"
  | "TOOL_CALL_END"
  | "TOOL_CALL_RESULT"
  | "STATE_SNAPSHOT"
  | "STATE_DELTA";

export interface AGUIEvent {
  type: EventType;
  [key: string]: unknown;
}

export interface TextMessageContent {
  type: "TEXT_MESSAGE_CONTENT";
  messageId: string;
  delta: string;
}

export interface ToolCallStart {
  type: "TOOL_CALL_START";
  timestamp: number;
  toolCallId: string;
  toolCallName: string;
}

export interface ToolCallResult {
  type: "TOOL_CALL_RESULT";
  timestamp: number;
  toolCallId: string;
  content: string;
}

export interface StateSnapshot {
  type: "STATE_SNAPSHOT";
  state: BrowserState;
}

export interface StateDelta {
  type: "STATE_DELTA";
  operations: PatchOp[];
}

export interface PatchOp {
  op: "replace" | "add" | "remove";
  path: string;
  value?: unknown;
}

export interface BrowserState {
  url: string;
  title: string;
  screenshot: string;
  elements: ElementInfo[];
  readyScore: number;
  activeTool: string;
  tabCount: number;
}

export interface ElementInfo {
  tag: string;
  text?: string;
  selector?: string;
  type?: string;
}

export interface ChatMessage {
  id: string;
  role: "user" | "assistant" | "tool";
  content: string;
  toolName?: string;
  toolCallId?: string;
  toolCalls?: Array<{ id: string; name: string; arguments: string }>;
  status?: "streaming" | "done" | "error";
}

export interface ToolAction {
  id: string;
  name: string;
  args: string;
  result?: string;
  timestamp: number;
  status: "running" | "done" | "error" | "unknown";
}
