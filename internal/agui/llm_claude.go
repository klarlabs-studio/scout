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

// ClaudeProvider implements LLMProvider using the Anthropic Messages API.
type ClaudeProvider struct {
	APIKey string
	Model  string
	client *http.Client
}

// NewClaudeProvider creates a provider for the Claude Messages API.
func NewClaudeProvider(apiKey, model string) *ClaudeProvider {
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}
	return &ClaudeProvider{
		APIKey: apiKey,
		Model:  model,
		client: &http.Client{},
	}
}

// StreamChat sends a streaming request to the Claude Messages API.
func (c *ClaudeProvider) StreamChat(ctx context.Context, req ChatRequest, cb func(ChatChunk)) error {
	body := c.buildRequest(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("claude: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("claude: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", c.APIKey)
	httpReq.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("claude: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("claude: API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return c.parseStream(resp.Body, cb)
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []claudeMessage `json:"messages"`
	Tools     []claudeTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []claudeContentBlock
}

type claudeContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type claudeTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

func (c *ClaudeProvider) buildRequest(req ChatRequest) claudeRequest {
	var messages []claudeMessage

	for _, m := range req.Messages {
		switch m.Role {
		case "assistant":
			if len(m.ToolCalls) > 0 {
				var blocks []claudeContentBlock
				if m.Content != "" {
					blocks = append(blocks, claudeContentBlock{Type: "text", Text: m.Content})
				}
				for _, tc := range m.ToolCalls {
					var input any
					_ = json.Unmarshal([]byte(tc.Args), &input)
					blocks = append(blocks, claudeContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					})
				}
				messages = append(messages, claudeMessage{Role: "assistant", Content: blocks})
			} else {
				messages = append(messages, claudeMessage{Role: "assistant", Content: m.Content})
			}
		case "tool":
			messages = append(messages, claudeMessage{
				Role: "user",
				Content: []claudeContentBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				}},
			})
		default:
			messages = append(messages, claudeMessage{Role: m.Role, Content: m.Content})
		}
	}

	var tools []claudeTool
	for _, t := range req.Tools {
		tools = append(tools, claudeTool(t))
	}

	return claudeRequest{
		Model:     c.Model,
		MaxTokens: 4096,
		System:    req.System,
		Messages:  messages,
		Tools:     tools,
		Stream:    true,
	}
}

func (c *ClaudeProvider) parseStream(r io.Reader, cb func(ChatChunk)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	inToolUse := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				inToolUse = true
				cb(ChatChunk{
					Type:       "tool_use_start",
					ToolCallID: event.ContentBlock.ID,
					ToolName:   event.ContentBlock.Name,
				})
			} else {
				inToolUse = false
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				cb(ChatChunk{Type: "text", Text: event.Delta.Text})
			case "input_json_delta":
				cb(ChatChunk{Type: "tool_use_delta", ArgsFragment: event.Delta.PartialJSON})
			}
		case "content_block_stop":
			if inToolUse {
				cb(ChatChunk{Type: "tool_use_end"})
				inToolUse = false
			}
		case "message_stop":
			cb(ChatChunk{Type: "stop"})
		case "error":
			cb(ChatChunk{Type: "error", Text: data})
		}
	}

	return scanner.Err()
}
