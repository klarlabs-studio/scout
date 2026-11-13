// Command profiles measures MCP tool-catalog size for Scout's task profiles.
// Run from the repository root with: go run ./examples/benchmark/profiles
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"time"
)

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type toolCatalog struct {
	Tools      []json.RawMessage `json:"tools"`
	NextCursor string            `json:"nextCursor"`
}

type profileReport struct {
	Profile              string   `json:"profile"`
	ToolCount            int      `json:"tool_count"`
	CatalogBytes         int      `json:"catalog_bytes"`
	ToolSchemaBytes      int      `json:"tool_schema_bytes"`
	EstimatedTokens      int      `json:"estimated_tokens_div_4"`
	ToolNames            []string `json:"tool_names"`
	HelpCallable         bool     `json:"help_callable"`
	HelpWorkflowCount    int      `json:"help_workflow_count"`
	HelpRecipeToolCount  int      `json:"help_recipe_tool_count"`
	HelpCatalogMatches   bool     `json:"help_catalog_matches"`
	HelpRecipesAvailable bool     `json:"help_recipes_available"`
}

type report struct {
	FormatVersion       string          `json:"format_version"`
	CapturedAt          string          `json:"captured_at"`
	Measurement         string          `json:"measurement"`
	TokenEstimate       string          `json:"token_estimate"`
	AllMutationCallable bool            `json:"all_mutation_callable"`
	CoreMutationBlocked bool            `json:"core_mutation_blocked"`
	ReadMutationBlocked bool            `json:"read_mutation_blocked"`
	Profiles            []profileReport `json:"profiles"`
}

func main() {
	buildDir, err := os.MkdirTemp("", "scout-mcp-profile-benchmark-*")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(buildDir)
	scoutPath := buildDir + "/scout"
	build := exec.Command("go", "build", "-o", scoutPath, "./cmd/scout")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fatal(fmt.Errorf("build Scout: %w", err))
	}
	profiles := []string{"all", "core", "read", "interact", "diagnose", "admin"}
	result := report{
		FormatVersion: "scout-mcp-profile-catalog-v2",
		CapturedAt:    time.Now().UTC().Format(time.RFC3339),
		Measurement:   "UTF-8 bytes in the compact JSON encoding of every complete tools/list tool entry, including descriptions, input schemas, and output schemas; catalog_bytes includes JSON array delimiters.",
		TokenEstimate: "estimated_tokens_div_4 is catalog_bytes divided by four and rounded up; it is a rough comparison only, not a tokenizer measurement.",
	}
	for _, name := range profiles {
		catalog, entries, err := queryProfile(scoutPath, name)
		if err != nil {
			fatal(err)
		}
		row := profileReport{Profile: name, ToolCount: len(entries), CatalogBytes: len(catalog), ToolSchemaBytes: len(bytes.Join(entries, []byte{})), EstimatedTokens: (len(catalog) + 3) / 4}
		for _, raw := range entries {
			var item struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &item); err != nil {
				fatal(fmt.Errorf("decode %s tool entry: %w", name, err))
			}
			row.ToolNames = append(row.ToolNames, item.Name)
		}
		sort.Strings(row.ToolNames)
		help, err := queryHelp(scoutPath, name)
		if err != nil {
			fatal(err)
		}
		row.HelpCallable = true
		row.HelpWorkflowCount = len(help.Workflows)
		recipeTools := make(map[string]struct{})
		for _, workflow := range help.Workflows {
			for _, tool := range workflow.Steps {
				recipeTools[tool] = struct{}{}
			}
		}
		row.HelpRecipeToolCount = len(recipeTools)
		row.HelpCatalogMatches = sameToolNames(row.ToolNames, help.AvailableTools)
		row.HelpRecipesAvailable = toolsAreSubset(recipeTools, row.ToolNames)
		if !row.HelpCatalogMatches || !row.HelpRecipesAvailable {
			fatal(fmt.Errorf("%s help output does not match its exposed tool profile", name))
		}
		result.Profiles = append(result.Profiles, row)
	}
	result.AllMutationCallable = callTool(scoutPath, "all", "configure") == nil
	result.CoreMutationBlocked = callTool(scoutPath, "core", "configure") != nil
	result.ReadMutationBlocked = callTool(scoutPath, "read", "configure") != nil
	if !result.AllMutationCallable || !result.CoreMutationBlocked || !result.ReadMutationBlocked {
		fatal(errors.New("profile call-gating check failed"))
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal(err)
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}

type helpPayload struct {
	AvailableTools []string `json:"available_tools"`
	Workflows      []struct {
		Steps []string `json:"steps"`
	} `json:"workflows"`
}

func queryHelp(scoutPath, profile string) (helpPayload, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, scoutPath, "mcp", "serve", "--profile="+profile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return helpPayload{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return helpPayload{}, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return helpPayload{}, err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	rpc := newRPC(stdin, bufio.NewReader(stdout))
	if _, err := rpc.call("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "scout-profile-benchmark", "version": "1.0.0"},
	}); err != nil {
		return helpPayload{}, fmt.Errorf("initialize %s help probe: %w", profile, err)
	}
	if err := rpc.notify("notifications/initialized"); err != nil {
		return helpPayload{}, err
	}
	response, err := rpc.call("tools/call", map[string]any{"name": "help", "arguments": map[string]any{}})
	if err != nil {
		return helpPayload{}, fmt.Errorf("call help in %s profile: %w", profile, err)
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return helpPayload{}, err
	}
	for _, block := range envelope.Content {
		if block.Type == "text" {
			var payload helpPayload
			if err := json.Unmarshal([]byte(block.Text), &payload); err == nil && payload.AvailableTools != nil && payload.Workflows != nil {
				return payload, nil
			}
		}
	}
	return helpPayload{}, fmt.Errorf("%s help response did not contain a structured workflow payload", profile)
}

func sameToolNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, name := range left {
		set[name] = struct{}{}
	}
	for _, name := range right {
		if _, ok := set[name]; !ok {
			return false
		}
	}
	return true
}

func toolsAreSubset(candidate map[string]struct{}, allowed []string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	for name := range candidate {
		if _, ok := set[name]; !ok {
			return false
		}
	}
	return true
}

func queryProfile(scoutPath, profile string) ([]byte, [][]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, scoutPath, "mcp", "serve", "--profile="+profile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	reader := bufio.NewReader(stdout)
	rpc := newRPC(stdin, reader)
	if _, err := rpc.call("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "scout-profile-benchmark", "version": "1.0.0"},
	}); err != nil {
		return nil, nil, fmt.Errorf("initialize %s profile: %w", profile, err)
	}
	if err := rpc.notify("notifications/initialized"); err != nil {
		return nil, nil, err
	}
	var allEntries [][]byte
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		response, err := rpc.call("tools/list", params)
		if err != nil {
			return nil, nil, fmt.Errorf("list %s profile: %w", profile, err)
		}
		var page toolCatalog
		if err := json.Unmarshal(response, &page); err != nil {
			return nil, nil, fmt.Errorf("decode %s catalog page: %w", profile, err)
		}
		for _, entry := range page.Tools {
			compact, err := compactJSON(entry)
			if err != nil {
				return nil, nil, err
			}
			allEntries = append(allEntries, compact)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(allEntries, func(i, j int) bool {
		return entryName(allEntries[i]) < entryName(allEntries[j])
	})
	return []byte("[" + joinJSON(allEntries) + "]"), allEntries, nil
}

type rpc struct {
	stdin  io.Writer
	stdout *bufio.Reader
	nextID int
}

func newRPC(stdin io.Writer, stdout *bufio.Reader) *rpc { return &rpc{stdin: stdin, stdout: stdout} }

func (r *rpc) call(method string, params map[string]any) (json.RawMessage, error) {
	r.nextID++
	request := rpcRequest{JSONRPC: "2.0", ID: r.nextID, Method: method, Params: params}
	if err := r.write(request); err != nil {
		return nil, err
	}
	line, err := r.stdout.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var response rpcResponse
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", method, err)
	}
	if response.Error != nil {
		return nil, fmt.Errorf("%s error %d: %s", method, response.Error.Code, response.Error.Message)
	}
	return response.Result, nil
}

func (r *rpc) notify(method string) error {
	return r.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}{JSONRPC: "2.0", Method: method})
}

func (r *rpc) write(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = r.stdin.Write(encoded)
	return err
}

func callTool(scoutPath, profile, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, scoutPath, "mcp", "serve", "--profile="+profile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	rpc := newRPC(stdin, bufio.NewReader(stdout))
	if _, err := rpc.call("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "scout-profile-benchmark", "version": "1.0.0"},
	}); err != nil {
		return err
	}
	if err := rpc.notify("notifications/initialized"); err != nil {
		return err
	}
	_, err = rpc.call("tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
	return err
}

func compactJSON(raw json.RawMessage) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func entryName(raw []byte) string {
	var entry struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &entry)
	return entry.Name
}

func joinJSON(entries [][]byte) string {
	var out bytes.Buffer
	for i, entry := range entries {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(entry)
	}
	return out.String()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "profile benchmark:", err)
	os.Exit(1)
}
