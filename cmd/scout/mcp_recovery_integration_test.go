//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.klarlabs.de/mcp/client"
	"go.klarlabs.de/mcp/protocol"
	"go.klarlabs.de/scout/agent"
	"go.klarlabs.de/scout/internal/launcher"
)

type mcpSelectorRoundTripper struct {
	base     http.RoundTripper
	selector string
}

func (rt *mcpSelectorRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || rt.selector == "" {
		return rt.base.RoundTrip(request)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var method string
	_ = json.Unmarshal(envelope["method"], &method)
	if method == "tools/call" {
		var params map[string]json.RawMessage
		if err := json.Unmarshal(envelope["params"], &params); err != nil {
			return nil, err
		}
		var meta map[string]json.RawMessage
		if len(params["_meta"]) > 0 {
			if err := json.Unmarshal(params["_meta"], &meta); err != nil {
				return nil, err
			}
		}
		if meta == nil {
			meta = make(map[string]json.RawMessage)
		}
		selector, _ := json.Marshal(rt.selector)
		meta[mcpSessionSelectorMetaKey] = selector
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		params["_meta"] = metaJSON
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		envelope["params"] = paramsJSON
		body, err = json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
	}
	clone := request.Clone(request.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return rt.base.RoundTrip(clone)
}

type mcpSessionIDCapture struct {
	base http.RoundTripper
	mu   sync.Mutex
	id   string
}

// mcpStatelessWireAudit checks the actual HTTP envelope used by the upstream
// client. Stateless MCP requests carry the JSON-RPC method routing header,
// omit transport session IDs, and receive no server-created transport ID.
type mcpStatelessWireAudit struct {
	base http.RoundTripper
	mu   sync.Mutex
	seen map[string]int
	bad  []string
}

func (a *mcpStatelessWireAudit) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.seen == nil {
		a.seen = make(map[string]int)
	}
	a.seen[envelope.Method]++
	if got := request.Header.Get("Mcp-Method"); got != envelope.Method {
		a.bad = append(a.bad, fmt.Sprintf("%s request carried Mcp-Method %q", envelope.Method, got))
	}
	a.mu.Unlock()

	clone := request.Clone(request.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	response, err := a.base.RoundTrip(clone)
	if err == nil && response.Header.Get("Mcp-Session-Id") != "" {
		a.mu.Lock()
		a.bad = append(a.bad, fmt.Sprintf("stateless %s response minted Mcp-Session-Id %q", envelope.Method, response.Header.Get("Mcp-Session-Id")))
		a.mu.Unlock()
	}
	return response, err
}

func (a *mcpStatelessWireAudit) assertClean(t *testing.T) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.bad) != 0 {
		t.Fatalf("stateless MCP wire contract violations: %s", strings.Join(a.bad, "; "))
	}
	if a.seen["tools/call"] == 0 || a.seen["server/discover"] == 0 {
		t.Fatalf("stateless MCP client did not exercise discovery and tools/call: methods=%v", a.seen)
	}
}

func (c *mcpSessionIDCapture) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := c.base.RoundTrip(request)
	if err == nil {
		if id := response.Header.Get("Mcp-Session-Id"); id != "" {
			c.mu.Lock()
			c.id = id
			c.mu.Unlock()
		}
	}
	return response, err
}

func (c *mcpSessionIDCapture) sessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

const (
	recoveryServerHelperEnv = "SCOUT_MCP_RECOVERY_SERVER_HELPER"
	recoveryServerAddrEnv   = "SCOUT_MCP_RECOVERY_SERVER_ADDR"
	recoveryServerStoreEnv  = "SCOUT_MCP_RECOVERY_SERVER_STORE"
)

func TestMCPRecoveryServerProcessHelper(t *testing.T) {
	if os.Getenv(recoveryServerHelperEnv) != "1" {
		return
	}
	if err := runMCP(os.Getenv(recoveryServerAddrEnv), "all", "", os.Getenv(recoveryServerStoreEnv)); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestMCPStatelessHTTPPrincipalHandleSurvivesStaleTransportHeader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<title>Stateless fixture</title><main>principal-bound session</main><button id="owner-task" onclick="document.body.dataset.task='completed'; document.body.innerText='task completed'">Complete</button>`)
	}))
	defer fixture.Close()
	chrome, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("headless Chrome unavailable: %v", err)
	}
	defer chrome.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	policies, err := json.Marshal(map[string]mcpHTTPPrincipal{
		"alice": {Token: "alice-stateless-token", RemoteCDP: chrome.WSEndpoint(), IsolateBrowserContext: true, AllowedOrigins: []string{fixture.URL}, AllowPrivateIPs: true},
		"bob":   {Token: "bob-stateless-token", RemoteCDP: chrome.WSEndpoint(), IsolateBrowserContext: true, AllowedOrigins: []string{fixture.URL}, AllowPrivateIPs: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPRecoveryServerProcessHelper$")
	taskStorePath := filepath.Join(t.TempDir(), "stateless-task-runs.db")
	env := append(os.Environ(),
		"SCOUT_MCP_HTTP_PRINCIPALS_JSON="+string(policies),
		"SCOUT_MCP_HTTP_STATELESS=true",
		recoveryServerHelperEnv+"=1",
		recoveryServerAddrEnv+"="+addr,
		recoveryServerStoreEnv+"="+taskStorePath,
	)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stateless MCP server: %v", err)
	}
	stopServer := func() {
		if cmd == nil || cmd.Process == nil {
			return
		}
		stopping := cmd
		cmd = nil
		_ = stopping.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = stopping.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			_ = stopping.Process.Kill()
			<-done
		}
	}
	defer func() { stopServer() }()

	wireAudit := &mcpStatelessWireAudit{base: http.DefaultTransport}
	newClient := func(token, staleTransportID string, selector string) *client.Client {
		t.Helper()
		base := &mcpSelectorRoundTripper{base: wireAudit, selector: selector}
		httpClient := &http.Client{Transport: base}
		options := []client.HTTPTransportOption{
			client.WithHTTPHeader("Authorization", "Bearer "+token),
			client.WithHTTPClient(httpClient),
		}
		if staleTransportID != "" {
			options = append(options, client.WithHTTPHeader("Mcp-Session-Id", staleTransportID))
		}
		transport, err := client.NewHTTPTransport("http://"+addr, options...)
		if err != nil {
			t.Fatal(err)
		}
		return client.New(transport)
	}
	connect := func(c *client.Client) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			info, connectErr := c.Connect(ctx)
			if connectErr == nil {
				if info.ProtocolVersion != protocol.ModernVersion {
					t.Fatalf("connected protocol = %q, want stateless %q", info.ProtocolVersion, protocol.ModernVersion)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("connect stateless MCP client: %v", connectErr)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	aliceFirst := newClient("alice-stateless-token", "", "")
	connect(aliceFirst) // Uses server/discover, without initialize or Mcp-Session-Id.
	listed, err := aliceFirst.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || listed.IsError {
		t.Fatalf("Alice list_sessions: result=%+v err=%v", listed, err)
	}
	var aliceSessions []SessionSummary
	sessionJSON := listed.StructuredContent
	if len(sessionJSON) == 0 && len(listed.Content) > 0 {
		sessionJSON = json.RawMessage(listed.Content[0].Text)
	}
	if err := json.Unmarshal(sessionJSON, &aliceSessions); err != nil {
		t.Fatalf("decode Alice sessions: %v; content=%+v", err, listed.Content)
	}
	if len(aliceSessions) != 1 || aliceSessions[0].SessionID == "" {
		t.Fatalf("Alice session handles = %+v, want one opaque default handle", aliceSessions)
	}
	aliceHandle := aliceSessions[0].SessionID

	// A new stateless transport carries a stale session header. The explicit
	// Scout handle must still resolve under Alice's stable authenticated scope.
	aliceReconnect := newClient("alice-stateless-token", "stale-stateful-session", aliceHandle)
	connect(aliceReconnect)
	navigated, err := aliceReconnect.CallTool(ctx, "navigate", map[string]any{"url": fixture.URL})
	if err != nil || navigated.IsError {
		t.Fatalf("Alice navigate after reconnect: result=%+v err=%v", navigated, err)
	}
	var page agent.PageResult
	pageJSON := navigated.StructuredContent
	if len(pageJSON) == 0 && len(navigated.Content) > 0 {
		pageJSON = json.RawMessage(navigated.Content[0].Text)
	}
	if err := json.Unmarshal(pageJSON, &page); err != nil {
		t.Fatalf("decode navigation result: %v; content=%+v", err, navigated.Content)
	}
	if page.TargetID == "" || strings.TrimRight(page.URL, "/") != strings.TrimRight(fixture.URL, "/") {
		t.Fatalf("navigation did not use retained Alice session: %+v", page)
	}

	aliceTaskResult, err := aliceReconnect.CallTool(ctx, "task_run", map[string]any{
		"id": "shared-stateless-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	})
	if err != nil || aliceTaskResult.IsError {
		t.Fatalf("Alice task_run: result=%+v err=%v", aliceTaskResult, err)
	}
	var aliceTask agent.TaskRunResult
	aliceTaskJSON := aliceTaskResult.StructuredContent
	if len(aliceTaskJSON) == 0 && len(aliceTaskResult.Content) > 0 {
		aliceTaskJSON = json.RawMessage(aliceTaskResult.Content[0].Text)
	}
	if err := json.Unmarshal(aliceTaskJSON, &aliceTask); err != nil || aliceTask.Status != "completed" {
		t.Fatalf("decode Alice task result: task=%+v err=%v; content=%+v", aliceTask, err, aliceTaskResult.Content)
	}

	snapshotResult, err := aliceReconnect.CallTool(ctx, "save_page_snapshot", map[string]any{"format": "readable_text"})
	if err != nil || snapshotResult.IsError {
		t.Fatalf("Alice save_page_snapshot: result=%+v err=%v", snapshotResult, err)
	}
	var snapshot PageSnapshotResult
	snapshotJSON := snapshotResult.StructuredContent
	if len(snapshotJSON) == 0 && len(snapshotResult.Content) > 0 {
		snapshotJSON = json.RawMessage(snapshotResult.Content[0].Text)
	}
	if err := json.Unmarshal(snapshotJSON, &snapshot); err != nil || snapshot.Artifact == nil || snapshot.Artifact.Path == "" {
		t.Fatalf("decode Alice snapshot: snapshot=%+v err=%v; content=%+v", snapshot, err, snapshotResult.Content)
	}
	artifactPath := snapshot.Artifact.Path
	defer os.Remove(artifactPath)
	artifactPaths := []string{artifactPath}
	defer func() {
		for _, path := range artifactPaths {
			_ = os.Remove(path)
		}
	}()

	// The artifact remains readable after another Alice reconnect, while Bob's
	// own opaque handle must not grant access to Alice's private snapshot.
	aliceArtifactReconnect := newClient("alice-stateless-token", "stale-stateful-session", aliceHandle)
	connect(aliceArtifactReconnect)
	readAliceSnapshot, err := aliceArtifactReconnect.CallTool(ctx, "read_page_snapshot", map[string]any{"path": artifactPath})
	if err != nil || readAliceSnapshot.IsError {
		t.Fatalf("Alice read snapshot after reconnect: result=%+v err=%v", readAliceSnapshot, err)
	}

	// Artifacts are scoped to a browser session, not just a principal. A second
	// Alice session must not read the default session's artifact.
	aliceControl := newClient("alice-stateless-token", "stale-stateful-session", "")
	connect(aliceControl)
	created, err := aliceControl.CallTool(ctx, "create_session", map[string]any{"name": "artifact-isolation"})
	if err != nil || created.IsError {
		t.Fatalf("Alice create second session: result=%+v err=%v", created, err)
	}
	listAfterCreate, err := aliceControl.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || listAfterCreate.IsError {
		t.Fatalf("Alice list sessions after create: result=%+v err=%v", listAfterCreate, err)
	}
	var aliceSessionsAfterCreate []SessionSummary
	listJSON := listAfterCreate.StructuredContent
	if len(listJSON) == 0 && len(listAfterCreate.Content) > 0 {
		listJSON = json.RawMessage(listAfterCreate.Content[0].Text)
	}
	if err := json.Unmarshal(listJSON, &aliceSessionsAfterCreate); err != nil || len(aliceSessionsAfterCreate) != 2 {
		t.Fatalf("decode Alice sessions after create: sessions=%+v err=%v", aliceSessionsAfterCreate, err)
	}
	var secondAliceHandle string
	for _, item := range aliceSessionsAfterCreate {
		if item.Name == "artifact-isolation" {
			secondAliceHandle = item.SessionID
		}
	}
	if secondAliceHandle == "" {
		t.Fatalf("second Alice session handle missing: %+v", aliceSessionsAfterCreate)
	}
	aliceSecondSession := newClient("alice-stateless-token", "stale-stateful-session", secondAliceHandle)
	connect(aliceSecondSession)
	if result, readErr := aliceSecondSession.CallTool(ctx, "read_page_snapshot", map[string]any{"path": artifactPath}); readErr == nil && (result == nil || !result.IsError) {
		t.Fatalf("same principal's second session read first session artifact: %+v", result)
	}
	secondSessionNavigation, err := aliceSecondSession.CallTool(ctx, "navigate", map[string]any{"url": fixture.URL})
	if err != nil || secondSessionNavigation.IsError {
		t.Fatalf("Alice second-session navigate: result=%+v err=%v", secondSessionNavigation, err)
	}
	var secondSessionPage agent.PageResult
	secondPageJSON := secondSessionNavigation.StructuredContent
	if len(secondPageJSON) == 0 && len(secondSessionNavigation.Content) > 0 {
		secondPageJSON = json.RawMessage(secondSessionNavigation.Content[0].Text)
	}
	if err := json.Unmarshal(secondPageJSON, &secondSessionPage); err != nil || secondSessionPage.TargetID == "" {
		t.Fatalf("decode Alice second-session page: page=%+v err=%v; content=%+v", secondSessionPage, err, secondSessionNavigation.Content)
	}
	secondSessionTaskResult, err := aliceSecondSession.CallTool(ctx, "task_run", map[string]any{
		"id": "shared-stateless-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	})
	if err != nil || secondSessionTaskResult.IsError {
		t.Fatalf("Alice second-session task_run with reused ID: result=%+v err=%v", secondSessionTaskResult, err)
	}
	var secondSessionTask agent.TaskRunResult
	secondTaskJSON := secondSessionTaskResult.StructuredContent
	if len(secondTaskJSON) == 0 && len(secondSessionTaskResult.Content) > 0 {
		secondTaskJSON = json.RawMessage(secondSessionTaskResult.Content[0].Text)
	}
	if err := json.Unmarshal(secondTaskJSON, &secondSessionTask); err != nil || secondSessionTask.Status != "completed" || secondSessionTask.IdempotencyReplay {
		t.Fatalf("decode Alice second-session task: task=%+v err=%v; content=%+v", secondSessionTask, err, secondSessionTaskResult.Content)
	}
	if secondSessionTask.TargetID != secondSessionPage.TargetID {
		t.Fatalf("second-session task target=%q, want its own page target %q", secondSessionTask.TargetID, secondSessionPage.TargetID)
	}
	// Interleave calls from two transports for the same principal. Each request
	// carries a different explicit handle, so overlapping dispatch must not
	// redirect a read or mutation to the other session.
	type concurrentSessionCall struct {
		name   string
		client *client.Client
	}
	calls := []concurrentSessionCall{{name: "default", client: aliceArtifactReconnect}, {name: "named", client: aliceSecondSession}}
	var concurrentWG sync.WaitGroup
	concurrentErrors := make(chan error, len(calls)*4)
	for _, call := range calls {
		call := call
		concurrentWG.Add(1)
		go func() {
			defer concurrentWG.Done()
			for i := 0; i < 4; i++ {
				result, callErr := call.client.CallTool(ctx, "status", map[string]any{})
				if callErr != nil {
					concurrentErrors <- fmt.Errorf("%s session status call %d: %w", call.name, i, callErr)
					return
				}
				if result == nil || result.IsError {
					concurrentErrors <- fmt.Errorf("%s session status call %d returned error result: %+v", call.name, i, result)
					return
				}
			}
		}()
	}
	concurrentWG.Wait()
	close(concurrentErrors)
	for callErr := range concurrentErrors {
		t.Error(callErr)
	}
	defaultReplay, err := aliceArtifactReconnect.CallTool(ctx, "task_run", map[string]any{
		"id": "shared-stateless-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	})
	if err != nil || defaultReplay.IsError {
		t.Fatalf("Alice default-session task replay: result=%+v err=%v", defaultReplay, err)
	}
	var defaultReplayTask agent.TaskRunResult
	defaultReplayJSON := defaultReplay.StructuredContent
	if len(defaultReplayJSON) == 0 && len(defaultReplay.Content) > 0 {
		defaultReplayJSON = json.RawMessage(defaultReplay.Content[0].Text)
	}
	if err := json.Unmarshal(defaultReplayJSON, &defaultReplayTask); err != nil || !defaultReplayTask.IdempotencyReplay || defaultReplayTask.TargetID != page.TargetID {
		t.Fatalf("default-session replay crossed task namespaces: task=%+v err=%v; content=%+v", defaultReplayTask, err, defaultReplay.Content)
	}
	// Return to the original handle after the owner-level create/list calls.
	if result, readErr := aliceArtifactReconnect.CallTool(ctx, "read_page_snapshot", map[string]any{"path": artifactPath}); readErr != nil || result.IsError {
		t.Fatalf("default session lost its artifact after another session was created: result=%+v err=%v", result, readErr)
	}

	// Snapshots and oversized-response artifacts share one owner budget. Four
	// saves fit; the fifth is rejected without registering another file.
	for i := 0; i < maxMCPResultArtifactOwnerCount-1; i++ {
		result, saveErr := aliceArtifactReconnect.CallTool(ctx, "save_page_snapshot", map[string]any{"format": "readable_text"})
		if saveErr != nil || result.IsError {
			t.Fatalf("Alice snapshot %d within quota: result=%+v err=%v", i+2, result, saveErr)
		}
		var extra PageSnapshotResult
		extraJSON := result.StructuredContent
		if len(extraJSON) == 0 && len(result.Content) > 0 {
			extraJSON = json.RawMessage(result.Content[0].Text)
		}
		if err := json.Unmarshal(extraJSON, &extra); err != nil || extra.Artifact == nil {
			t.Fatalf("decode Alice snapshot %d: artifact=%+v err=%v", i+2, extra.Artifact, err)
		}
		artifactPaths = append(artifactPaths, extra.Artifact.Path)
	}
	fifth, fifthErr := aliceArtifactReconnect.CallTool(ctx, "save_page_snapshot", map[string]any{"format": "readable_text"})
	if fifthErr == nil && (fifth == nil || !fifth.IsError) {
		t.Fatalf("fifth snapshot exceeded owner quota but succeeded: %+v", fifth)
	}
	deletedPath := artifactPaths[1]
	deleted, deleteErr := aliceArtifactReconnect.CallTool(ctx, "delete_page_snapshot", map[string]any{"path": deletedPath})
	if deleteErr != nil || deleted.IsError {
		t.Fatalf("delete snapshot to release quota: result=%+v err=%v", deleted, deleteErr)
	}
	if _, statErr := os.Stat(deletedPath); !os.IsNotExist(statErr) {
		t.Fatalf("deleted snapshot remains on disk: %v", statErr)
	}
	afterDelete, afterDeleteErr := aliceArtifactReconnect.CallTool(ctx, "save_page_snapshot", map[string]any{"format": "readable_text"})
	if afterDeleteErr != nil || afterDelete.IsError {
		t.Fatalf("snapshot after deleting one artifact should fit quota: result=%+v err=%v", afterDelete, afterDeleteErr)
	}
	var afterDeleteSnapshot PageSnapshotResult
	afterDeleteJSON := afterDelete.StructuredContent
	if len(afterDeleteJSON) == 0 && len(afterDelete.Content) > 0 {
		afterDeleteJSON = json.RawMessage(afterDelete.Content[0].Text)
	}
	if err := json.Unmarshal(afterDeleteJSON, &afterDeleteSnapshot); err != nil || afterDeleteSnapshot.Artifact == nil {
		t.Fatalf("decode snapshot after deletion: artifact=%+v err=%v", afterDeleteSnapshot.Artifact, err)
	}
	artifactPaths = append(artifactPaths, afterDeleteSnapshot.Artifact.Path)
	if result, readErr := aliceArtifactReconnect.CallTool(ctx, "read_page_snapshot", map[string]any{"path": deletedPath}); readErr == nil && (result == nil || !result.IsError) {
		t.Fatalf("deleted artifact remained readable: %+v", result)
	}

	bob := newClient("bob-stateless-token", "", "")
	connect(bob)
	bobListed, err := bob.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || bobListed.IsError {
		t.Fatalf("Bob list_sessions: result=%+v err=%v", bobListed, err)
	}
	var bobSessions []SessionSummary
	bobJSON := bobListed.StructuredContent
	if len(bobJSON) == 0 && len(bobListed.Content) > 0 {
		bobJSON = json.RawMessage(bobListed.Content[0].Text)
	}
	if err := json.Unmarshal(bobJSON, &bobSessions); err != nil || len(bobSessions) != 1 || bobSessions[0].SessionID == "" {
		t.Fatalf("decode Bob sessions: sessions=%+v err=%v; content=%+v", bobSessions, err, bobListed.Content)
	}
	bobOwnSession := newClient("bob-stateless-token", "", bobSessions[0].SessionID)
	connect(bobOwnSession)
	bobPageResult, err := bobOwnSession.CallTool(ctx, "navigate", map[string]any{"url": fixture.URL})
	if err != nil || bobPageResult.IsError {
		t.Fatalf("Bob navigate: result=%+v err=%v", bobPageResult, err)
	}
	foreignTaskResult, err := bobOwnSession.CallTool(ctx, "get_task_run", map[string]any{"id": "shared-stateless-task-id"})
	if err != nil || foreignTaskResult.IsError {
		t.Fatalf("Bob get_task_run: result=%+v err=%v", foreignTaskResult, err)
	}
	var foreignTask GetTaskRunResult
	foreignTaskJSON := foreignTaskResult.StructuredContent
	if len(foreignTaskJSON) == 0 && len(foreignTaskResult.Content) > 0 {
		foreignTaskJSON = json.RawMessage(foreignTaskResult.Content[0].Text)
	}
	if err := json.Unmarshal(foreignTaskJSON, &foreignTask); err != nil || foreignTask.Found {
		t.Fatalf("Bob observed Alice's task record: found=%v err=%v; content=%+v", foreignTask.Found, err, foreignTaskResult.Content)
	}
	bobTaskResult, err := bobOwnSession.CallTool(ctx, "task_run", map[string]any{
		"id": "shared-stateless-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	})
	if err != nil || bobTaskResult.IsError {
		t.Fatalf("Bob task_run with independently reused ID: result=%+v err=%v", bobTaskResult, err)
	}
	var bobTask agent.TaskRunResult
	bobTaskJSON := bobTaskResult.StructuredContent
	if len(bobTaskJSON) == 0 && len(bobTaskResult.Content) > 0 {
		bobTaskJSON = json.RawMessage(bobTaskResult.Content[0].Text)
	}
	if err := json.Unmarshal(bobTaskJSON, &bobTask); err != nil || bobTask.Status != "completed" || bobTask.IdempotencyReplay {
		t.Fatalf("decode Bob task result: task=%+v err=%v; content=%+v", bobTask, err, bobTaskResult.Content)
	}
	if bobTask.PlanID != aliceTask.PlanID {
		t.Fatalf("same caller task ID produced different plan fingerprints: Alice=%q Bob=%q", aliceTask.PlanID, bobTask.PlanID)
	}
	if result, readErr := bobOwnSession.CallTool(ctx, "read_page_snapshot", map[string]any{"path": artifactPath}); readErr == nil && (result == nil || !result.IsError) {
		t.Fatalf("Bob read Alice snapshot unexpectedly succeeded: %+v", result)
	}

	// Closing Alice's session removes its owned artifact from disk and the
	// registry, even though the client transport itself is stateless.
	closed, err := aliceControl.CallTool(ctx, "close_session", map[string]any{"name": aliceHandle})
	if err != nil || closed.IsError {
		t.Fatalf("Alice close_session: result=%+v err=%v", closed, err)
	}
	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Fatalf("Alice session close left private snapshot on disk: stat err=%v", err)
	}
	if result, readErr := aliceArtifactReconnect.CallTool(ctx, "read_page_snapshot", map[string]any{"path": artifactPath}); readErr == nil && (result == nil || !result.IsError) {
		t.Fatalf("read after owner session close unexpectedly succeeded: %+v", result)
	}

	if _, err := bob.CallTool(ctx, "status", map[string]any{}); err == nil || !strings.Contains(err.Error(), "requires an opaque session_id") {
		t.Fatalf("selectorless stateless status error = %v, want explicit handle requirement", err)
	}
	bobWithForeignHandle := newClient("bob-stateless-token", "", aliceHandle)
	connect(bobWithForeignHandle)
	if _, err := bobWithForeignHandle.CallTool(ctx, "status", map[string]any{}); err == nil || !strings.Contains(err.Error(), "session does not exist for this client") {
		t.Fatalf("foreign principal handle error = %v, want owner-scoped not-found", err)
	}
	wireAudit.assertClean(t)
	_ = aliceFirst.Close()
	_ = aliceReconnect.Close()
	_ = aliceArtifactReconnect.Close()
	_ = aliceControl.Close()
	_ = aliceSecondSession.Close()
	_ = bob.Close()
	_ = bobOwnSession.Close()
	_ = bobWithForeignHandle.Close()

	// Stateless handles are intentionally process-local. A graceful server
	// restart must invalidate the old handle without preventing a fresh owner
	// from creating and using a new session against the same durable task store.
	stopServer()
	cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPRecoveryServerProcessHelper$")
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("restart stateless MCP server: %v", err)
	}
	staleAfterRestart := newClient("alice-stateless-token", "", aliceHandle)
	connect(staleAfterRestart)
	if _, err := staleAfterRestart.CallTool(ctx, "status", map[string]any{}); err == nil || !strings.Contains(err.Error(), "session does not exist for this client") {
		t.Fatalf("pre-restart handle error = %v, want owner-scoped not-found", err)
	}
	_ = staleAfterRestart.Close()

	postRestartOwner := newClient("alice-stateless-token", "", "")
	connect(postRestartOwner)
	createdAfterRestart, err := postRestartOwner.CallTool(ctx, "create_session", map[string]any{"name": "after-restart"})
	if err != nil || createdAfterRestart.IsError {
		t.Fatalf("create session after server restart: result=%+v err=%v", createdAfterRestart, err)
	}
	postRestartList, err := postRestartOwner.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || postRestartList.IsError {
		t.Fatalf("list sessions after server restart: result=%+v err=%v", postRestartList, err)
	}
	var postRestartSessions []SessionSummary
	postRestartJSON := postRestartList.StructuredContent
	if len(postRestartJSON) == 0 && len(postRestartList.Content) > 0 {
		postRestartJSON = json.RawMessage(postRestartList.Content[0].Text)
	}
	if err := json.Unmarshal(postRestartJSON, &postRestartSessions); err != nil || len(postRestartSessions) != 1 || postRestartSessions[0].SessionID == "" || postRestartSessions[0].SessionID == aliceHandle {
		t.Fatalf("post-restart sessions = %+v, err=%v; want one fresh handle", postRestartSessions, err)
	}
	postRestartClient := newClient("alice-stateless-token", "", postRestartSessions[0].SessionID)
	connect(postRestartClient)
	postRestartNavigation, err := postRestartClient.CallTool(ctx, "navigate", map[string]any{"url": fixture.URL})
	if err != nil || postRestartNavigation.IsError {
		t.Fatalf("navigate with fresh post-restart handle: result=%+v err=%v", postRestartNavigation, err)
	}
	closedAfterRestart, err := postRestartOwner.CallTool(ctx, "close_session", map[string]any{"name": postRestartSessions[0].SessionID})
	if err != nil || closedAfterRestart.IsError {
		t.Fatalf("close post-restart session: result=%+v err=%v", closedAfterRestart, err)
	}
	_ = postRestartOwner.Close()
	_ = postRestartClient.Close()
	wireAudit.assertClean(t)
}

func TestMCPInFlightCallSurvivesIdleTimeoutThenExpires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	requestStarted := make(chan string, 2)
	releaseResponse := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hold" && r.URL.Path != "/cancel" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		requestStarted <- r.URL.Path
		if r.URL.Path == "/cancel" {
			<-r.Context().Done()
			return
		}
		select {
		case <-releaseResponse:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<title>Idle lifecycle fixture</title><main>ready</main>`)
	}))
	defer fixture.Close()
	chrome, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("headless Chrome unavailable: %v", err)
	}
	defer chrome.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	policies, err := json.Marshal(map[string]mcpHTTPPrincipal{
		"idle-test": {Token: "idle-test-token", RemoteCDP: chrome.WSEndpoint(), IsolateBrowserContext: true, AllowedOrigins: []string{fixture.URL}, AllowPrivateIPs: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPRecoveryServerProcessHelper$")
	cmd.Env = append(os.Environ(),
		"SCOUT_MCP_HTTP_PRINCIPALS_JSON="+string(policies),
		"SCOUT_MCP_HTTP_STATELESS=true",
		"SCOUT_SESSION_IDLE_TIMEOUT=300ms",
		recoveryServerHelperEnv+"=1",
		recoveryServerAddrEnv+"="+addr,
		recoveryServerStoreEnv+"="+filepath.Join(t.TempDir(), "idle-task-runs.db"),
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start MCP server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	newClient := func(selector string) *client.Client {
		t.Helper()
		rt := &mcpSelectorRoundTripper{base: http.DefaultTransport, selector: selector}
		transport, transportErr := client.NewHTTPTransport("http://"+addr,
			client.WithHTTPHeader("Authorization", "Bearer idle-test-token"),
			client.WithHTTPClient(&http.Client{Transport: rt}),
		)
		if transportErr != nil {
			t.Fatal(transportErr)
		}
		return client.New(transport)
	}
	connect := func(c *client.Client) {
		t.Helper()
		connectDeadline := time.Now().Add(15 * time.Second)
		for {
			_, connectErr := c.Connect(ctx)
			if connectErr == nil {
				return
			}
			if time.Now().After(connectDeadline) {
				t.Fatalf("connect MCP client: %v", connectErr)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	bootstrap := newClient("")
	connect(bootstrap)
	decodeSessions := func(result *client.ToolResult) ([]SessionSummary, error) {
		raw := result.StructuredContent
		if len(raw) == 0 && len(result.Content) > 0 {
			raw = json.RawMessage(result.Content[0].Text)
		}
		var decoded []SessionSummary
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	listed, err := bootstrap.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || listed.IsError {
		t.Fatalf("list sessions: result=%+v err=%v", listed, err)
	}
	sessions, err := decodeSessions(listed)
	if err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID == "" {
		t.Fatalf("initial sessions = %+v, want one session handle", sessions)
	}
	mcpClient := newClient(sessions[0].SessionID)
	connect(mcpClient)
	navigateResult := make(chan error, 1)
	go func() {
		result, callErr := mcpClient.CallTool(ctx, "navigate", map[string]any{"url": fixture.URL + "/hold"})
		if callErr == nil && result.IsError {
			callErr = fmt.Errorf("navigate returned isError")
		}
		navigateResult <- callErr
	}()
	select {
	case path := <-requestStarted:
		if path != "/hold" {
			t.Fatalf("first navigation reached %q, want /hold", path)
		}
	case <-ctx.Done():
		t.Fatal("navigation never reached the delayed fixture")
	case <-time.After(10 * time.Second):
		t.Fatal("navigation never reached the delayed fixture")
	}
	// Cross the idle threshold while navigation remains in flight. The cleanup
	// worker must keep the browser alive, and list_sessions must report that
	// active call rather than silently presenting an idle session.
	time.Sleep(450 * time.Millisecond)
	active, err := bootstrap.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || active.IsError {
		t.Fatalf("list sessions during navigation: result=%+v err=%v", active, err)
	}
	sessions, err = decodeSessions(active)
	if err != nil {
		t.Fatalf("decode active sessions: %v", err)
	}
	if len(sessions) != 1 || !sessions[0].Started || sessions[0].InFlightCalls < 1 {
		t.Fatalf("in-flight session state = %+v, want started with an active call", sessions)
	}
	close(releaseResponse)
	select {
	case callErr := <-navigateResult:
		if callErr != nil {
			t.Fatalf("navigate during idle interval: %v", callErr)
		}
	case <-ctx.Done():
		t.Fatalf("navigation did not finish: %v", ctx.Err())
	}
	time.Sleep(600 * time.Millisecond)
	expired, err := bootstrap.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || expired.IsError {
		t.Fatalf("list sessions after idle expiry: result=%+v err=%v", expired, err)
	}
	sessions, err = decodeSessions(expired)
	if err != nil {
		t.Fatalf("decode expired sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Started {
		t.Fatalf("session after idle expiry = %+v, want registered but stopped session", sessions)
	}

	// A client that abandons an active HTTP request must also release the MCP
	// tool call so that idle cleanup can close its browser session.
	cancelClient := newClient(sessions[0].SessionID)
	connect(cancelClient)
	callCtx, cancelCall := context.WithCancel(ctx)
	canceledCall := make(chan error, 1)
	go func() {
		_, callErr := cancelClient.CallTool(callCtx, "navigate", map[string]any{"url": fixture.URL + "/cancel"})
		canceledCall <- callErr
	}()
	select {
	case path := <-requestStarted:
		if path != "/cancel" {
			t.Fatalf("canceled navigation reached %q, want /cancel", path)
		}
	case <-ctx.Done():
		t.Fatal("canceled navigation never reached the fixture")
	case <-time.After(10 * time.Second):
		t.Fatal("canceled navigation never reached the fixture")
	}
	cancelCall()
	select {
	case callErr := <-canceledCall:
		if callErr == nil {
			t.Fatal("canceled navigation unexpectedly returned successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP tool call did not return after its HTTP request was canceled")
	}
	time.Sleep(600 * time.Millisecond)
	postCancel, err := bootstrap.CallTool(ctx, "list_sessions", map[string]any{})
	if err != nil || postCancel.IsError {
		t.Fatalf("list sessions after canceled call: result=%+v err=%v", postCancel, err)
	}
	sessions, err = decodeSessions(postCancel)
	if err != nil {
		t.Fatalf("decode post-cancel sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Started {
		t.Fatalf("session after canceled call = %+v, want stopped after idle cleanup", sessions)
	}
}

func TestMCPHTTPRecoveryRestartsAndResumesExactTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<title>Recovery fixture</title><main>persistent target</main><button id="owner-task" onclick="document.body.dataset.task='completed'; document.body.innerText='task completed'">Complete</button>`)
	}))
	defer fixture.Close()

	browser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("headless Chrome unavailable: %v", err)
	}
	defer browser.Close()
	bobBrowser, err := launcher.LaunchContext(ctx, launcher.Options{Headless: true})
	if err != nil {
		t.Skipf("second headless Chrome unavailable: %v", err)
	}
	defer bobBrowser.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	storePath := filepath.Join(t.TempDir(), "task-runs.db")
	policies, err := json.Marshal(map[string]mcpHTTPPrincipal{
		"alice": {Token: "alice-token", RemoteCDP: browser.WSEndpoint(), AllowedOrigins: []string{fixture.URL}, AllowPrivateIPs: true},
		"bob":   {Token: "bob-token", RemoteCDP: bobBrowser.WSEndpoint(), AllowedOrigins: []string{fixture.URL}, AllowPrivateIPs: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseEnv := append(os.Environ(), "SCOUT_MCP_HTTP_PRINCIPALS_JSON="+string(policies))
	startServer := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPRecoveryServerProcessHelper$")
		cmd.Env = append(baseEnv,
			recoveryServerHelperEnv+"=1",
			recoveryServerAddrEnv+"="+addr,
			recoveryServerStoreEnv+"="+storePath,
		)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start MCP helper: %v", err)
		}
		return cmd
	}
	stopServer := func(cmd *exec.Cmd) {
		t.Helper()
		if cmd == nil || cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil && !strings.Contains(err.Error(), "signal: terminated") {
				t.Logf("MCP server exit: %v", err)
			}
		case <-time.After(8 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	newClient := func(token string) (*client.Client, *mcpSessionIDCapture) {
		t.Helper()
		capture := &mcpSessionIDCapture{base: http.DefaultTransport}
		httpClient := &http.Client{Transport: capture}
		transport, err := client.NewHTTPTransport("http://"+addr,
			client.WithHTTPHeader("Authorization", "Bearer "+token),
			client.WithHTTPClient(httpClient),
		)
		if err != nil {
			t.Fatal(err)
		}
		c := client.New(transport)
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := c.Initialize(ctx); err == nil {
				return c, capture
			}
			if time.Now().After(deadline) {
				_ = c.Close()
				t.Fatalf("initialize MCP client at %s: %v", addr, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	newClientForTransportSession := func(token, transportSessionID string) *client.Client {
		t.Helper()
		transport, err := client.NewHTTPTransport("http://"+addr,
			client.WithHTTPHeader("Authorization", "Bearer "+token),
			client.WithHTTPHeader("Mcp-Session-Id", transportSessionID),
		)
		if err != nil {
			t.Fatal(err)
		}
		// Do not initialize again: initialize always creates a new stateful
		// transport session. The first tool call proves that the captured ID
		// remains valid for a client reconnecting to the existing session.
		return client.New(transport)
	}
	call := func(c *client.Client, tool string, args any) *client.ToolResult {
		t.Helper()
		result, err := c.CallTool(ctx, tool, args)
		if err != nil {
			t.Fatalf("call %s: %v", tool, err)
		}
		return result
	}
	decode := func(result *client.ToolResult, into any) {
		t.Helper()
		if result.IsError {
			t.Fatalf("unexpected tool error: %+v", result.Content)
		}
		if len(result.StructuredContent) > 0 {
			if err := json.Unmarshal(result.StructuredContent, into); err != nil {
				t.Fatalf("decode structured content: %v", err)
			}
			return
		}
		for _, item := range result.Content {
			if item.Type == "text" {
				if err := json.Unmarshal([]byte(item.Text), into); err != nil {
					t.Fatalf("decode tool content: %v (%s)", err, item.Text)
				}
				return
			}
		}
		t.Fatalf("tool returned no decodable content")
	}

	server := startServer()
	defer func() { stopServer(server) }()

	first, firstSession := newClient("alice-token")
	page := agent.PageResult{}
	decode(call(first, "navigate", map[string]any{"url": fixture.URL}), &page)
	if page.TargetID == "" {
		t.Fatal("navigate returned no target ID")
	}
	if page.ContentTrust != agent.ContentTrustUntrustedPage {
		t.Fatalf("navigate content_trust = %q, want %q", page.ContentTrust, agent.ContentTrustUntrustedPage)
	}
	var statefulSnapshot PageSnapshotResult
	decode(call(first, "save_page_snapshot", map[string]any{"format": "readable_text"}), &statefulSnapshot)
	if statefulSnapshot.Artifact == nil || statefulSnapshot.Artifact.Path == "" {
		t.Fatalf("stateful snapshot has no artifact: %+v", statefulSnapshot)
	}
	statefulArtifactPath := statefulSnapshot.Artifact.Path
	defer os.Remove(statefulArtifactPath)
	if created := call(first, "create_session", map[string]any{"name": "artifact-other"}); created.IsError {
		t.Fatalf("create second stateful HTTP session: %+v", created.Content)
	}
	if denied, readErr := first.CallTool(ctx, "read_page_snapshot", map[string]any{"path": statefulArtifactPath}); readErr == nil && (denied == nil || !denied.IsError) {
		t.Fatalf("stateful named session read default-session artifact: %+v", denied)
	}
	if switched := call(first, "switch_session", map[string]any{"name": "default"}); switched.IsError {
		t.Fatalf("switch to artifact owner session: %+v", switched.Content)
	}
	if readable := call(first, "read_page_snapshot", map[string]any{"path": statefulArtifactPath}); readable.IsError {
		t.Fatalf("default stateful session could not read its artifact after switching back: %+v", readable.Content)
	}
	reconnected := newClientForTransportSession("alice-token", firstSession.sessionID())
	if reconnectRead := call(reconnected, "read_page_snapshot", map[string]any{"path": statefulArtifactPath}); reconnectRead.IsError {
		t.Fatalf("stateful artifact was unavailable after reconnecting with the same transport session: %+v", reconnectRead.Content)
	}
	if deleted := call(reconnected, "delete_page_snapshot", map[string]any{"path": statefulArtifactPath}); deleted.IsError {
		t.Fatalf("reconnected stateful client could not delete snapshot: %+v", deleted.Content)
	}
	if _, statErr := os.Stat(statefulArtifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("deleted stateful artifact remains on disk: %v", statErr)
	}
	var handleOne SessionRecoveryResult
	decode(call(first, "persist_session", map[string]any{}), &handleOne)
	if handleOne.Handle == "" || handleOne.TargetID != page.TargetID {
		t.Fatalf("persisted handle does not identify navigated target: %+v, page=%+v", handleOne, page)
	}
	deleteRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://"+addr+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest.Header.Set("Authorization", "Bearer alice-token")
	deleteRequest.Header.Set("Mcp-Session-Id", firstSession.sessionID())
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatalf("delete first MCP transport: %v", err)
	}
	_ = deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("MCP transport DELETE status = %d, want %d", deleteResponse.StatusCode, http.StatusNoContent)
	}
	_ = first.Close()

	second, _ := newClient("alice-token")
	var resumedAfterDelete SessionRecoveryResult
	decode(call(second, "resume_session", map[string]any{"handle": handleOne.Handle}), &resumedAfterDelete)
	if !resumedAfterDelete.Resumed || resumedAfterDelete.TargetID != page.TargetID {
		t.Fatalf("transport DELETE recovery changed target: %+v, want %s", resumedAfterDelete, page.TargetID)
	}
	var statusAfterDelete agent.SessionStatus
	decode(call(second, "status", map[string]any{}), &statusAfterDelete)
	if statusAfterDelete.ActiveTargetID != page.TargetID || strings.TrimSuffix(statusAfterDelete.CurrentURL, "/") != fixture.URL {
		t.Fatalf("status after transport DELETE recovery differs from original target: %+v", statusAfterDelete)
	}
	_ = second.Close()
	bobPersistClient, _ := newClient("bob-token")
	var bobPage agent.PageResult
	decode(call(bobPersistClient, "navigate", map[string]any{"url": fixture.URL}), &bobPage)
	var bobPreRestartStatus agent.SessionStatus
	decode(call(bobPersistClient, "status", map[string]any{}), &bobPreRestartStatus)
	if bobPreRestartStatus.BrowserOwnership != "remote_cdp" || bobPreRestartStatus.ActiveTargetID != bobPage.TargetID {
		t.Fatalf("Bob pre-restart status = %+v, want a live remote target", bobPreRestartStatus)
	}
	var bobRecoveryHandle SessionRecoveryResult
	decode(call(bobPersistClient, "persist_session", map[string]any{}), &bobRecoveryHandle)
	if bobRecoveryHandle.Handle == "" {
		t.Fatal("Bob persist_session returned no handle for restart cleanup check")
	}
	_ = bobPersistClient.Close()

	stopServer(server)
	server = nil

	server = startServer()
	bobAfterRestart, _ := newClient("bob-token")
	var bobFreshStatus agent.SessionStatus
	decode(call(bobAfterRestart, "status", map[string]any{}), &bobFreshStatus)
	bobCloseAfterRestart := call(bobAfterRestart, "close_session", map[string]any{"name": "default"})
	if bobCloseAfterRestart.IsError {
		t.Fatalf("close Bob default session after restart: %+v", bobCloseAfterRestart.Content)
	}
	var revokedAfterRestartClose SessionRecoveryResult
	decode(call(bobAfterRestart, "revoke_session", map[string]any{"handle": bobRecoveryHandle.Handle}), &revokedAfterRestartClose)
	if revokedAfterRestartClose.Revoked {
		t.Fatal("close after restart left Bob's durable recovery handle in the store")
	}
	_ = bobAfterRestart.Close()
	third, _ := newClient("alice-token")
	fourth, _ := newClient("alice-token")
	type resumeAttempt struct {
		client *client.Client
		result *client.ToolResult
		err    error
	}
	start := make(chan struct{})
	attempts := make(chan resumeAttempt, 2)
	for _, candidate := range []*client.Client{third, fourth} {
		go func(c *client.Client) {
			<-start
			result, err := c.CallTool(ctx, "resume_session", map[string]any{"handle": handleOne.Handle})
			attempts <- resumeAttempt{client: c, result: result, err: err}
		}(candidate)
	}
	close(start)
	firstAttempt, secondAttempt := <-attempts, <-attempts
	winners := 0
	var resumedClient *client.Client
	var resumedResult *client.ToolResult
	for _, attempt := range []resumeAttempt{firstAttempt, secondAttempt} {
		if attempt.err == nil && attempt.result != nil && !attempt.result.IsError {
			winners++
			resumedClient = attempt.client
			resumedResult = attempt.result
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent resume successes = %d; attempts=%+v %+v, want exactly one", winners, firstAttempt, secondAttempt)
	}
	var resumedAfterRestart SessionRecoveryResult
	decode(resumedResult, &resumedAfterRestart)
	if !resumedAfterRestart.Resumed || resumedAfterRestart.TargetID != page.TargetID {
		t.Fatalf("restart resume changed target: %+v, want %s", resumedAfterRestart, page.TargetID)
	}
	var restartedStatus agent.SessionStatus
	decode(call(resumedClient, "status", map[string]any{}), &restartedStatus)
	if restartedStatus.ActiveTargetID != page.TargetID || strings.TrimSuffix(restartedStatus.CurrentURL, "/") != fixture.URL {
		t.Fatalf("restart status does not match original target: %+v", restartedStatus)
	}
	otherClient := third
	if otherClient == resumedClient {
		otherClient = fourth
	}
	type lifecycleAttempt struct {
		name   string
		result *client.ToolResult
		err    error
	}
	lifecycleStart := make(chan struct{})
	lifecycleResults := make(chan lifecycleAttempt, 2)
	go func() {
		<-lifecycleStart
		result, err := resumedClient.CallTool(ctx, "close_session", map[string]any{"name": "default"})
		lifecycleResults <- lifecycleAttempt{name: "close", result: result, err: err}
	}()
	go func() {
		<-lifecycleStart
		result, err := otherClient.CallTool(ctx, "resume_session", map[string]any{"handle": handleOne.Handle})
		lifecycleResults <- lifecycleAttempt{name: "resume", result: result, err: err}
	}()
	close(lifecycleStart)
	firstLifecycle, secondLifecycle := <-lifecycleResults, <-lifecycleResults
	var closeAttempt, racingResume lifecycleAttempt
	for _, attempt := range []lifecycleAttempt{firstLifecycle, secondLifecycle} {
		if attempt.name == "close" {
			closeAttempt = attempt
		} else {
			racingResume = attempt
		}
	}
	closeSucceeded := closeAttempt.err == nil && closeAttempt.result != nil && !closeAttempt.result.IsError
	resumeSucceeded := racingResume.err == nil && racingResume.result != nil && !racingResume.result.IsError
	if closeSucceeded == resumeSucceeded {
		t.Fatalf("close/resume race must have exactly one winner: close=%+v resume=%+v", closeAttempt, racingResume)
	}
	if resumeSucceeded {
		sequentialClose := call(otherClient, "close_session", map[string]any{"name": "default"})
		if sequentialClose.IsError {
			t.Fatalf("close session after resume won race: %+v", sequentialClose.Content)
		}
	}
	var revoked SessionRecoveryResult
	decode(call(resumedClient, "revoke_session", map[string]any{"handle": handleOne.Handle}), &revoked)
	if revoked.Revoked {
		t.Fatal("close_session left the durable recovery handle active")
	}
	if err := third.Close(); err != nil {
		t.Fatalf("close first recovery HTTP transport: %v", err)
	}
	if err := fourth.Close(); err != nil {
		t.Fatalf("close second recovery HTTP transport: %v", err)
	}

	bob, _ := newClient("bob-token")
	aliceTaskClient, _ := newClient("alice-token")
	createSession := func(c *client.Client) string {
		t.Helper()
		result := call(c, "create_session", map[string]any{"name": "work"})
		for _, item := range result.Content {
			if item.Type == "text" {
				const marker = "(session_id: "
				if start := strings.Index(item.Text, marker); start >= 0 {
					value := item.Text[start+len(marker):]
					if end := strings.IndexByte(value, ')'); end >= 0 {
						return value[:end]
					}
				}
			}
		}
		t.Fatalf("create_session returned no opaque selector: %+v", result.Content)
		return ""
	}
	aliceCreated := createSession(aliceTaskClient)
	bobCreated := createSession(bob)
	if aliceCreated == "" || bobCreated == "" || aliceCreated == bobCreated {
		t.Fatalf("same-name sessions did not receive distinct opaque selectors: Alice=%q Bob=%q", aliceCreated, bobCreated)
	}
	aliceListing := call(aliceTaskClient, "list_sessions", map[string]any{})
	bobListing := call(bob, "list_sessions", map[string]any{})
	var aliceSessions, bobSessions []SessionSummary
	decode(aliceListing, &aliceSessions)
	decode(bobListing, &bobSessions)
	findWork := func(sessions []SessionSummary) (SessionSummary, bool) {
		for _, session := range sessions {
			if session.Name == "work" {
				return session, true
			}
		}
		return SessionSummary{}, false
	}
	aliceWork, ok := findWork(aliceSessions)
	if !ok || aliceWork.SessionID != aliceCreated || aliceWork.OwnerScope != "authenticated_principal" {
		t.Fatalf("Alice work session listing = %+v, want her own principal-scoped session %q", aliceSessions, aliceCreated)
	}
	bobWork, ok := findWork(bobSessions)
	if !ok || bobWork.SessionID != bobCreated || bobWork.OwnerScope != "authenticated_principal" {
		t.Fatalf("Bob work session listing = %+v, want his own principal-scoped session %q", bobSessions, bobCreated)
	}
	if result, err := bob.CallTool(ctx, "switch_session", map[string]any{"name": aliceCreated}); err == nil && !result.IsError {
		t.Fatalf("Bob selected Alice's opaque session ID: %+v", result)
	}
	var aliceWorkPage, bobWorkPage agent.PageResult
	decode(call(aliceTaskClient, "navigate", map[string]any{"url": fixture.URL}), &aliceWorkPage)
	decode(call(bob, "navigate", map[string]any{"url": fixture.URL}), &bobWorkPage)
	if aliceWorkPage.TargetID == "" || bobWorkPage.TargetID == "" || aliceWorkPage.TargetID == bobWorkPage.TargetID {
		t.Fatalf("same-name principal sessions did not retain distinct browser targets: Alice=%q Bob=%q", aliceWorkPage.TargetID, bobWorkPage.TargetID)
	}
	if result, err := bob.CallTool(ctx, "switch_session", map[string]any{"name": "work"}); err != nil || result.IsError {
		t.Fatalf("Bob switched to his same-named session: result=%+v err=%v", result, err)
	}
	var bobWorkStatus agent.SessionStatus
	decode(call(bob, "status", map[string]any{}), &bobWorkStatus)
	if bobWorkStatus.ActiveTargetID != bobWorkPage.TargetID {
		t.Fatalf("Bob's same-name session points at target %q, want %q", bobWorkStatus.ActiveTargetID, bobWorkPage.TargetID)
	}
	if result, err := aliceTaskClient.CallTool(ctx, "switch_session", map[string]any{"name": "work"}); err != nil || result.IsError {
		t.Fatalf("Alice switched to her same-named session: result=%+v err=%v", result, err)
	}
	var aliceWorkStatus agent.SessionStatus
	decode(call(aliceTaskClient, "status", map[string]any{}), &aliceWorkStatus)
	if aliceWorkStatus.ActiveTargetID != aliceWorkPage.TargetID {
		t.Fatalf("Alice's same-name session points at target %q, want %q", aliceWorkStatus.ActiveTargetID, aliceWorkPage.TargetID)
	}
	decode(call(aliceTaskClient, "navigate", map[string]any{"url": fixture.URL}), &page)
	var aliceTask agent.TaskRunResult
	decode(call(aliceTaskClient, "task_run", map[string]any{
		"id": "same-owner-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	}), &aliceTask)
	if aliceTask.Status != "completed" {
		t.Fatalf("Alice task status = %q, want completed: %+v", aliceTask.Status, aliceTask)
	}
	if result, err := aliceTaskClient.CallTool(ctx, "switch_session", map[string]any{"name": "default"}); err != nil || result.IsError {
		t.Fatalf("Alice switched to her default session: result=%+v err=%v", result, err)
	}
	var aliceDefaultPage agent.PageResult
	decode(call(aliceTaskClient, "navigate", map[string]any{"url": fixture.URL}), &aliceDefaultPage)
	var aliceDefaultTask agent.TaskRunResult
	decode(call(aliceTaskClient, "task_run", map[string]any{
		"id": "same-owner-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	}), &aliceDefaultTask)
	if aliceDefaultTask.Status != "completed" || aliceDefaultTask.IdempotencyReplay {
		t.Fatalf("Alice's default session did not execute the same task ID independently: %+v", aliceDefaultTask)
	}
	if aliceDefaultTask.TargetID == "" || aliceDefaultTask.TargetID == aliceTask.TargetID || aliceDefaultTask.TargetID != aliceDefaultPage.TargetID {
		t.Fatalf("Alice default-session task target=%q, work target=%q, navigated target=%q", aliceDefaultTask.TargetID, aliceTask.TargetID, aliceDefaultPage.TargetID)
	}
	if result, err := aliceTaskClient.CallTool(ctx, "switch_session", map[string]any{"name": "work"}); err != nil || result.IsError {
		t.Fatalf("Alice switched back to work: result=%+v err=%v", result, err)
	}
	var aliceWorkReplay agent.TaskRunResult
	decode(call(aliceTaskClient, "task_run", map[string]any{
		"id": "same-owner-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	}), &aliceWorkReplay)
	if !aliceWorkReplay.IdempotencyReplay || aliceWorkReplay.TargetID != aliceTask.TargetID {
		t.Fatalf("Alice's work retry crossed session task namespaces: %+v", aliceWorkReplay)
	}
	// Stateful browser sessions are transport-scoped, but durable task records
	// are scoped by principal and logical session name. A reconnecting client
	// that creates the same name therefore sees the saved result without
	// dispatching the plan against its new browser target.
	aliceReconnect, _ := newClient("alice-token")
	defer aliceReconnect.Close()
	createSession(aliceReconnect)
	var aliceReconnectPage agent.PageResult
	decode(call(aliceReconnect, "navigate", map[string]any{"url": fixture.URL}), &aliceReconnectPage)
	if aliceReconnectPage.TargetID == "" || aliceReconnectPage.TargetID == aliceWorkPage.TargetID {
		t.Fatalf("Alice reconnect did not create an independent browser target: reconnect=%q work=%q", aliceReconnectPage.TargetID, aliceWorkPage.TargetID)
	}
	var aliceReconnectReplay agent.TaskRunResult
	decode(call(aliceReconnect, "task_run", map[string]any{
		"id": "same-owner-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	}), &aliceReconnectReplay)
	if !aliceReconnectReplay.IdempotencyReplay || aliceReconnectReplay.TargetID != aliceTask.TargetID {
		t.Fatalf("Alice reconnect did not replay the logical work-session record: %+v", aliceReconnectReplay)
	}
	var reconnectUntouched bool
	decode(call(aliceReconnect, "has_element", map[string]any{"selector": "body:not([data-task='completed'])"}), &reconnectUntouched)
	if !reconnectUntouched {
		t.Fatal("Alice reconnect replay unexpectedly dispatched the task against its independent browser target")
	}
	var foreignTask GetTaskRunResult
	decode(call(bob, "get_task_run", map[string]any{"id": "same-owner-task-id"}), &foreignTask)
	if foreignTask.Found {
		t.Fatalf("Bob read Alice's task record: %+v", foreignTask)
	}
	decode(call(bob, "navigate", map[string]any{"url": fixture.URL}), &page)
	var bobTask agent.TaskRunResult
	decode(call(bob, "task_run", map[string]any{
		"id": "same-owner-task-id", "timeout_ms": 15000,
		"actions": []map[string]any{{"action": "click", "selector": "#owner-task", "expected_text": "task completed"}},
	}), &bobTask)
	if bobTask.Status != "completed" {
		t.Fatalf("Bob could not independently use Alice's caller-selected task ID: %+v", bobTask)
	}
	if bobTask.PlanID != aliceTask.PlanID {
		t.Fatalf("same caller-selected task plan fingerprint differs across owners: Alice=%q Bob=%q", aliceTask.PlanID, bobTask.PlanID)
	}
	if bobTask.IdempotencyReplay {
		t.Fatal("Bob's same-ID task reused Alice's saved result instead of executing in Bob's namespace")
	}
	_ = aliceTaskClient.Close()
	foreign, foreignErr := bob.CallTool(ctx, "resume_session", map[string]any{"handle": handleOne.Handle})
	if foreignErr == nil && !foreign.IsError {
		t.Fatal("different principal resumed Alice's durable handle")
	}
	unknown, unknownErr := bob.CallTool(ctx, "resume_session", map[string]any{"handle": strings.Repeat("x", 32)})
	if unknownErr == nil && unknown != nil && !unknown.IsError {
		t.Fatal("unknown recovery handle unexpectedly resumed")
	}
	if foreignErr == nil || unknownErr == nil || foreignErr.Error() != unknownErr.Error() {
		t.Fatalf("foreign and unknown handles are distinguishable: foreign=%v unknown=%v", foreignErr, unknownErr)
	}
	if err := bob.Close(); err != nil {
		t.Fatalf("close Bob HTTP transport: %v", err)
	}
}
