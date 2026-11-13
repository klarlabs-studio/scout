// Command memory_scout_probe runs the delayed-element MCP wait used by the
// cross-tool memory comparison. Each process owns one stdio MCP server.
package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"time"

	"go.klarlabs.de/mcp/client"
	"go.klarlabs.de/scout/examples/benchmark/fixture"
)

func main() {
	server := httptest.NewServer(fixture.Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mcpBinary := os.Getenv("SCOUT_MCP_BINARY")
	if mcpBinary == "" {
		mcpBinary = "go"
	}
	args := []string{"run", "./cmd/scout", "mcp", "serve"}
	if mcpBinary != "go" {
		args = []string{"mcp", "serve"}
	}
	transport, err := client.NewUnsafeStdioTransport(mcpBinary, args...)
	must(err)
	defer transport.Close()
	mcpClient := client.New(transport)
	if _, err := mcpClient.Initialize(ctx); err != nil {
		must(err)
	}
	if _, err := mcpClient.CallTool(ctx, "configure", map[string]any{"allow_private_ips": true}); err != nil {
		must(err)
	}
	if _, err := mcpClient.CallTool(ctx, "navigate", map[string]any{"url": server.URL + "/delayed-element?delay_ms=1800"}); err != nil {
		must(err)
	}
	before, err := mcpClient.CallTool(ctx, "has_element", map[string]any{"selector": "#delayed-ready"})
	if err != nil {
		must(err)
	}
	if before.IsError || string(before.StructuredContent) == "true" {
		must(fmt.Errorf("delayed element was ready before the wait began"))
	}
	started := time.Now()
	wait, err := mcpClient.CallTool(ctx, "wait_for", map[string]any{"selector": "#delayed-ready"})
	if err != nil {
		must(err)
	}
	waitMS := time.Since(started).Milliseconds()
	if wait.IsError {
		must(fmt.Errorf("wait_for returned an error"))
	}
	check, err := mcpClient.CallTool(ctx, "assert_text_contains", map[string]any{"text": "Ready", "selector": "#delayed-ready"})
	if err != nil {
		must(err)
	}
	if check.IsError {
		must(fmt.Errorf("wait completed without the expected text"))
	}
	fmt.Printf("{\"success\":true,\"precondition\":\"not_ready\",\"postcondition\":\"Ready\",\"wait_duration_ms\":%d}\n", waitMS)
}

func must(err error) {
	if err != nil {
		fmt.Println(err)
		panic(err)
	}
}
