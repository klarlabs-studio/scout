package agui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIProvider implements LLMProvider using the OpenAI Chat Completions API.
// Compatible with OpenAI, Ollama, Groq, Together, Azure OpenAI, and any
// provider that exposes an OpenAI-compatible /v1/chat/completions endpoint.
type OpenAIProvider struct {
	APIKey  string
	Model   string
	BaseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a provider for OpenAI-compatible APIs.
func NewOpenAIProvider(apiKey, model, baseURL string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		model = "gpt-4o"
	}
	return &OpenAIProvider{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
		client:  &http.Client{},
	}
}

func (o *OpenAIProvider) StreamChat(ctx context.Context, req ChatRequest, cb func(ChatChunk)) error {
	body := o.buildRequest(req)
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("openai: failed to marshal request: %w", err)
	}

	url := o.BaseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("openai: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("openai: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("openai: API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return o.parseStream(resp.Body, cb)
}

type openaiRequest struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	Tools     []openaiTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiToolFunction `json:"function"`
}

type openaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiTool struct {
	Type     string            `json:"type"`
	Function openaiToolFuncDef `json:"function"`
}

type openaiToolFuncDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

func (o *OpenAIProvider) buildRequest(req ChatRequest) openaiRequest {
	var messages []openaiMessage

	// System message
	if req.System != "" {
		messages = append(messages, openaiMessage{Role: "system", Content: req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			msg := openaiMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, openaiToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: openaiToolFunction{
						Name:      tc.Name,
						Arguments: tc.Args,
					},
				})
			}
			messages = append(messages, msg)
		case "tool":
			messages = append(messages, openaiMessage{
				Role:       "tool",
				Content:    m.Content,
				ToolCallID: m.ToolCallID,
			})
		default:
			messages = append(messages, openaiMessage{Role: m.Role, Content: m.Content})
		}
	}

	var tools []openaiTool
	for _, t := range req.Tools {
		tools = append(tools, openaiTool{
			Type: "function",
			Function: openaiToolFuncDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return openaiRequest{
		Model:     o.Model,
		Messages:  messages,
		Tools:     tools,
		Stream:    true,
		MaxTokens: 4096,
	}
}

func (o *OpenAIProvider) parseStream(r io.Reader, cb func(ChatChunk)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	// Track tool call state across deltas
	type toolState struct {
		id   string
		name string
	}
	toolCalls := make(map[int]*toolState)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			cb(ChatChunk{Type: "stop"})
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		// Text content
		if delta.Content != "" {
			cb(ChatChunk{Type: "text", Text: delta.Content})
		}

		// Tool calls
		for _, tc := range delta.ToolCalls {
			state, exists := toolCalls[tc.Index]

			if !exists {
				// New tool call
				state = &toolState{id: tc.ID, name: tc.Function.Name}
				toolCalls[tc.Index] = state
				cb(ChatChunk{
					Type:       "tool_use_start",
					ToolCallID: tc.ID,
					ToolName:   tc.Function.Name,
				})
			}

			// Argument fragments
			if tc.Function.Arguments != "" {
				cb(ChatChunk{
					Type:         "tool_use_delta",
					ToolCallID:   state.id,
					ArgsFragment: tc.Function.Arguments,
				})
			}
		}

		// Finish reason = "tool_calls" means all tool calls are complete
		if fr := chunk.Choices[0].FinishReason; fr != nil && *fr == "tool_calls" {
			for _, state := range toolCalls {
				cb(ChatChunk{Type: "tool_use_end", ToolCallID: state.id})
			}
			toolCalls = make(map[int]*toolState)
		}
	}

	return scanner.Err()
}
