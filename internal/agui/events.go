// Package agui implements an AG-UI protocol server for scout browser automation.
//
// The AG-UI protocol uses Server-Sent Events (SSE) over HTTP to stream
// structured events from an agent backend to a CopilotKit frontend.
package agui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// EventType identifies an AG-UI protocol event.
type EventType string

const (
	EventRunStarted         EventType = "RUN_STARTED"
	EventRunFinished        EventType = "RUN_FINISHED"
	EventRunError           EventType = "RUN_ERROR"
	EventTextMessageStart   EventType = "TEXT_MESSAGE_START"
	EventTextMessageContent EventType = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd     EventType = "TEXT_MESSAGE_END"
	EventToolCallStart      EventType = "TOOL_CALL_START"
	EventToolCallArgs       EventType = "TOOL_CALL_ARGS"
	EventToolCallEnd        EventType = "TOOL_CALL_END"
	EventToolCallResult     EventType = "TOOL_CALL_RESULT"
	EventStateSnapshot      EventType = "STATE_SNAPSHOT"
	EventStateDelta         EventType = "STATE_DELTA"
	EventStepStarted        EventType = "STEP_STARTED"
	EventStepFinished       EventType = "STEP_FINISHED"
	EventRunBudgetExhausted EventType = "RUN_BUDGET_EXHAUSTED"
	EventRunRepeatedCall    EventType = "RUN_REPEATED_CALL"
)

// RunStarted signals the beginning of an agent run.
type RunStarted struct {
	Type     EventType `json:"type"`
	ThreadID string    `json:"threadId"`
	RunID    string    `json:"runId"`
}

// RunFinished signals successful completion of an agent run.
type RunFinished struct {
	Type     EventType `json:"type"`
	ThreadID string    `json:"threadId"`
	RunID    string    `json:"runId"`
}

// RunError signals a failed agent run.
type RunError struct {
	Type    EventType `json:"type"`
	Message string    `json:"message"`
	Code    string    `json:"code,omitempty"`
}

// RunBudgetExhausted signals that the agentic loop hit its tool-call ceiling
// before producing a final text-only response. Distinct from RunFinished so
// callers can show a "stopped early — task incomplete" state.
type RunBudgetExhausted struct {
	Type      EventType `json:"type"`
	ThreadID  string    `json:"threadId"`
	RunID     string    `json:"runId"`
	HopsUsed  int       `json:"hopsUsed"`
	HopsLimit int       `json:"hopsLimit"`
}

// RunRepeatedCall signals that the same tool was invoked with identical
// arguments more than the allowed window. Often a small-model failure mode.
type RunRepeatedCall struct {
	Type     EventType `json:"type"`
	ThreadID string    `json:"threadId"`
	RunID    string    `json:"runId"`
	ToolName string    `json:"toolName"`
	Repeats  int       `json:"repeats"`
}

// TextMessageStart initiates a streaming text message.
type TextMessageStart struct {
	Type      EventType `json:"type"`
	MessageID string    `json:"messageId"`
	Role      string    `json:"role"`
}

// TextMessageContent streams a text chunk.
type TextMessageContent struct {
	Type      EventType `json:"type"`
	MessageID string    `json:"messageId"`
	Delta     string    `json:"delta"`
}

// TextMessageEnd signals a text message is complete.
type TextMessageEnd struct {
	Type      EventType `json:"type"`
	MessageID string    `json:"messageId"`
}

// ToolCallStart initiates a tool invocation.
type ToolCallStart struct {
	Type         EventType `json:"type"`
	Timestamp    int64     `json:"timestamp"`
	ToolCallID   string    `json:"toolCallId"`
	ToolCallName string    `json:"toolCallName"`
}

// ToolCallArgs streams tool argument JSON fragments.
type ToolCallArgs struct {
	Type       EventType `json:"type"`
	Timestamp  int64     `json:"timestamp"`
	ToolCallID string    `json:"toolCallId"`
	Delta      string    `json:"delta"`
}

// ToolCallEnd signals tool arguments are complete.
type ToolCallEnd struct {
	Type       EventType `json:"type"`
	Timestamp  int64     `json:"timestamp"`
	ToolCallID string    `json:"toolCallId"`
}

// ToolCallResult returns the tool execution output.
type ToolCallResult struct {
	Type       EventType `json:"type"`
	Timestamp  int64     `json:"timestamp"`
	MessageID  string    `json:"messageId"`
	ToolCallID string    `json:"toolCallId"`
	Content    string    `json:"content"`
	Role       string    `json:"role"`
}

// StateSnapshot delivers complete shared state.
type StateSnapshot struct {
	Type  EventType `json:"type"`
	State any       `json:"state"`
}

// StateDelta delivers incremental state updates via JSON Patch (RFC 6902).
type StateDelta struct {
	Type       EventType `json:"type"`
	Operations []PatchOp `json:"operations"`
}

// PatchOp is a single JSON Patch operation.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// StepStarted signals the beginning of a sub-task.
type StepStarted struct {
	Type        EventType `json:"type"`
	StepID      string    `json:"stepId"`
	Description string    `json:"description,omitempty"`
}

// StepFinished signals completion of a sub-task.
type StepFinished struct {
	Type   EventType `json:"type"`
	StepID string    `json:"stepId"`
}

// SSEWriter writes AG-UI events as Server-Sent Events to an http.ResponseWriter.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter creates an SSEWriter and sets the required SSE response headers.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("agui: response writer does not support flushing")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &SSEWriter{w: w, flusher: flusher}, nil
}

// WriteEvent serializes an event as JSON and writes it as an SSE data frame.
func (s *SSEWriter) WriteEvent(event any) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("agui: failed to marshal event: %w", err)
	}

	// Escape newlines within JSON to preserve SSE frame integrity
	escaped := strings.ReplaceAll(string(data), "\n", "\\n")
	escaped = strings.ReplaceAll(escaped, "\r", "\\r")

	_, err = fmt.Fprintf(s.w, "data: %s\n\n", escaped)
	if err != nil {
		return fmt.Errorf("agui: failed to write SSE frame: %w", err)
	}
	s.flusher.Flush()
	return nil
}

// Now returns the current time in milliseconds for AG-UI timestamps.
func Now() int64 {
	return time.Now().UnixMilli()
}
