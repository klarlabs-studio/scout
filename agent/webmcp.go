package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	browse "go.klarlabs.de/scout"
)

const (
	maxWebMCPTools        = 128
	maxWebMCPToolBytes    = 64 << 10
	maxWebMCPCatalogBytes = 192 << 10
	maxWebMCPURLBytes     = 8 << 10
	maxWebMCPResultBytes  = 256 << 10
)

// WebMCPTool is page-provided metadata. Treat its name, description, schema,
// annotations, and frame as untrusted content.
type WebMCPTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	FrameID      string         `json:"frame_id"`
	Origin       string         `json:"origin"`
	ContentTrust string         `json:"content_trust"`
}

// WebMCPCatalog reports the currently registered page tools for one target.
type WebMCPCatalog struct {
	TargetID         string       `json:"target_id"`
	URL              string       `json:"url"`
	ContentTrust     string       `json:"content_trust"`
	Tools            []WebMCPTool `json:"tools"`
	Complete         bool         `json:"complete"`
	CoverageWarnings []string     `json:"coverage_warnings,omitempty"`
	URLTruncated     bool         `json:"url_truncated,omitempty"`
	Truncated        bool         `json:"truncated,omitempty"`
	OmittedTools     int          `json:"omitted_tools,omitempty"`
}

type webMCPToolWire struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
	FrameID     string         `json:"frameId"`
}

func webMCPDropTool(tools []webMCPToolWire, candidate webMCPToolWire) []webMCPToolWire {
	filtered := tools[:0]
	for _, tool := range tools {
		if tool.Name != candidate.Name || tool.FrameID != candidate.FrameID {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func webMCPUpsertTool(tools []webMCPToolWire, candidate webMCPToolWire) []webMCPToolWire {
	for i := range tools {
		if tools[i].Name == candidate.Name && tools[i].FrameID == candidate.FrameID {
			tools[i] = candidate
			return tools
		}
	}
	return append(tools, candidate)
}

type webMCPInvocationResponse struct {
	InvocationID string `json:"invocationId"`
}

type webMCPResponseEvent struct {
	InvocationID string          `json:"invocationId"`
	Status       string          `json:"status"`
	Output       json.RawMessage `json:"output"`
	ErrorText    string          `json:"errorText"`
	Oversized    bool            `json:"-"`
	ResultBytes  int             `json:"-"`
}

type webMCPFrameTree struct {
	Frame struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"frame"`
	ChildFrames []webMCPFrameTree `json:"childFrames"`
}

func webMCPFrameOrigins(tree webMCPFrameTree, origins map[string]string, inheritedOrigin string) {
	origin := inheritedOrigin
	if tree.Frame.ID != "" {
		if parsedOrigin, err := browseURLOrigin(tree.Frame.URL); err == nil {
			origin = parsedOrigin
		} else if strings.HasPrefix(tree.Frame.URL, "blob:") {
			if parsedOrigin, parseErr := browseURLOrigin(strings.TrimPrefix(tree.Frame.URL, "blob:")); parseErr == nil {
				origin = parsedOrigin
			}
		} else if tree.Frame.URL != "about:blank" && tree.Frame.URL != "about:srcdoc" {
			origin = ""
		}
		if origin != "" {
			origins[tree.Frame.ID] = origin
		}
	}
	for _, child := range tree.ChildFrames {
		webMCPFrameOrigins(child, origins, origin)
	}
}

func webMCPFrameIDs(tree webMCPFrameTree, frameIDs map[string]struct{}) {
	if tree.Frame.ID != "" {
		frameIDs[tree.Frame.ID] = struct{}{}
	}
	for _, child := range tree.ChildFrames {
		webMCPFrameIDs(child, frameIDs)
	}
}

// WebMCPCatalogContext reads the current tool catalog from the experimental
// CDP WebMCP domain. It does not expose page-provided tools as Scout tools.
func (s *Session) WebMCPCatalogContext(ctx context.Context) (*WebMCPCatalog, error) {
	if s == nil || ctx == nil {
		return nil, fmt.Errorf("WebMCP catalog requires a session and context")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.webmcpCatalogLockedContext(ctx, s.page)
}

func (s *Session) webmcpCatalogLockedContext(ctx context.Context, page *browse.Page) (*WebMCPCatalog, error) {
	type catalogEvent struct {
		Tools []webMCPToolWire `json:"tools"`
	}
	events := make(chan catalogEvent, 8)
	removed := make(chan json.RawMessage, 8)
	removeAdded := page.OnRawSession("WebMCP.toolsAdded", func(raw json.RawMessage) {
		var event catalogEvent
		if json.Unmarshal(raw, &event) == nil {
			select {
			case events <- event:
			default:
			}
		}
	})
	defer removeAdded()
	removeRemoved := page.OnRawSession("WebMCP.toolsRemoved", func(raw json.RawMessage) {
		select {
		case removed <- raw:
		default:
		}
	})
	defer removeRemoved()
	if _, err := page.CallSessionContext(ctx, "WebMCP.enable", map[string]any{}); err != nil {
		return nil, err
	}
	tools := make([]webMCPToolWire, 0)
	idle := time.NewTimer(50 * time.Millisecond)
	defer idle.Stop()
	for {
		select {
		case event := <-events:
			for _, candidate := range event.Tools {
				tools = webMCPUpsertTool(tools, candidate)
			}
			if len(tools) > maxWebMCPTools {
				return nil, fmt.Errorf("WebMCP catalog exceeds %d tools", maxWebMCPTools)
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(50 * time.Millisecond)
		case <-idle.C:
			goto catalogReady
		case <-ctx.Done():
			return nil, ctx.Err()
		case raw := <-removed:
			var event catalogEvent
			if json.Unmarshal(raw, &event) == nil {
				for _, candidate := range event.Tools {
					tools = webMCPDropTool(tools, candidate)
				}
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(50 * time.Millisecond)
		}
	}
catalogReady:
	if len(tools) > maxWebMCPTools {
		return nil, fmt.Errorf("WebMCP catalog exceeds %d tools", maxWebMCPTools)
	}
	var url string
	urlValue, _ := page.EvaluateContext(ctx, `location.href`)
	url, _ = urlValue.(string)
	frameOrigins := make(map[string]string)
	knownFrameIDs := make(map[string]struct{})
	coverageWarnings := make([]string, 0)
	rootFrameID := ""
	if rawTree, err := page.CallSessionContext(ctx, "Page.getFrameTree", map[string]any{}); err == nil {
		var tree struct {
			FrameTree webMCPFrameTree `json:"frameTree"`
		}
		if json.Unmarshal(rawTree, &tree) == nil {
			rootFrameID = tree.FrameTree.Frame.ID
			webMCPFrameOrigins(tree.FrameTree, frameOrigins, "")
			webMCPFrameIDs(tree.FrameTree, knownFrameIDs)
		} else {
			coverageWarnings = append(coverageWarnings, "frame_tree_unavailable")
		}
	} else {
		coverageWarnings = append(coverageWarnings, "frame_tree_unavailable")
	}
	provider, ok := s.browser.(interface {
		BrowserTargetsContext(context.Context) ([]browse.BrowserTarget, error)
	})
	if !ok || rootFrameID == "" {
		coverageWarnings = append(coverageWarnings, "frame_target_coverage_unverified")
	} else if targets, err := provider.BrowserTargetsContext(ctx); err != nil {
		coverageWarnings = append(coverageWarnings, "frame_target_coverage_unverified")
	} else {
		validator := browse.URLValidator{AllowPrivateIPs: s.allowPrivateIPs, PolicyID: taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs), AllowedOrigins: append([]string(nil), s.allowedOrigins...)}
		ownedFrameTargets := make(map[string]struct{})
		for _, target := range targets {
			if target.Type == "iframe" {
				if _, direct := knownFrameIDs[target.ParentFrameID]; direct {
					ownedFrameTargets[target.TargetID] = struct{}{}
				}
			}
		}
		for changed := true; changed; {
			changed = false
			for _, target := range targets {
				if target.Type != "iframe" {
					continue
				}
				if _, alreadyOwned := ownedFrameTargets[target.TargetID]; alreadyOwned {
					continue
				}
				if _, parentOwned := ownedFrameTargets[target.ParentID]; parentOwned {
					ownedFrameTargets[target.TargetID] = struct{}{}
					changed = true
				}
			}
		}
		for _, target := range targets {
			if target.Type != "iframe" {
				continue
			}
			if _, owned := ownedFrameTargets[target.TargetID]; !owned {
				continue
			}
			if _, parentKnown := knownFrameIDs[target.ParentFrameID]; !parentKnown {
				coverageWarnings = append(coverageWarnings, "nested_site_isolated_iframe_tools_not_enumerated")
				continue
			}
			if err := validator.Validate(target.URL); err != nil {
				coverageWarnings = append(coverageWarnings, "site_isolated_iframe_origin_denied")
				continue
			}
			origin, originErr := browseURLOrigin(target.URL)
			if originErr != nil {
				coverageWarnings = append(coverageWarnings, "site_isolated_iframe_origin_unverified")
				continue
			}
			exposedTools, exposureErr := webMCPExposedFrameTools(ctx, page, origin)
			if exposureErr != nil {
				coverageWarnings = append(coverageWarnings, "site_isolated_iframe_exposure_unverified")
				continue
			}
			if len(exposedTools) == 0 {
				continue
			}
			framePage, attachErr := page.AttachFrameTargetContext(ctx, target.TargetID)
			if attachErr != nil {
				coverageWarnings = append(coverageWarnings, "site_isolated_iframe_attach_failed")
				continue
			}
			frameTools, frameID, enumErr := webMCPEnumerateFrameTarget(ctx, framePage)
			detachErr := framePage.DetachContext(context.Background())
			if enumErr != nil || detachErr != nil {
				coverageWarnings = append(coverageWarnings, "site_isolated_iframe_tools_not_enumerated")
				continue
			}
			exposedNames := make(map[string]struct{}, len(exposedTools))
			for _, tool := range exposedTools {
				exposedNames[tool.Name] = struct{}{}
			}
			for _, tool := range frameTools {
				if tool.FrameID == "" || frameID != tool.FrameID {
					continue
				}
				if _, exposed := exposedNames[tool.Name]; !exposed {
					continue
				}
				frameOrigins[tool.FrameID] = origin
				tools = webMCPUpsertTool(tools, tool)
			}
		}
	}
	return webMCPBuildCatalog(page.TargetID(), url, tools, frameOrigins, coverageWarnings), nil
}

func webMCPEnumerateFrameTarget(ctx context.Context, page *browse.Page) ([]webMCPToolWire, string, error) {
	type event struct {
		Tools []webMCPToolWire `json:"tools"`
	}
	events := make(chan event, 8)
	remove := page.OnRawSession("WebMCP.toolsAdded", func(raw json.RawMessage) {
		var parsed event
		if json.Unmarshal(raw, &parsed) == nil {
			select {
			case events <- parsed:
			default:
			}
		}
	})
	defer remove()
	if _, err := page.CallSessionContext(ctx, "WebMCP.enable", map[string]any{}); err != nil {
		return nil, "", err
	}
	frameID, err := webMCPRootFrameID(ctx, page)
	if err != nil {
		return nil, "", err
	}
	tools := make([]webMCPToolWire, 0)
	idle := time.NewTimer(50 * time.Millisecond)
	defer idle.Stop()
	for {
		select {
		case incoming := <-events:
			for _, tool := range incoming.Tools {
				tools = webMCPUpsertTool(tools, tool)
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(50 * time.Millisecond)
		case <-idle.C:
			return tools, frameID, nil
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
}

func webMCPExposedFrameTools(ctx context.Context, page *browse.Page, origin string) ([]webMCPToolWire, error) {
	originJSON, _ := json.Marshal(origin)
	expression := fmt.Sprintf(`(async()=>{if(!document.modelContext?.getTools)throw new Error('WebMCP getTools is unavailable');const tools=await document.modelContext.getTools({fromOrigins:[%s]});return (tools||[]).map(t=>({name:t.name,description:t.description,inputSchema:t.inputSchema,annotations:t.annotations,frameId:t.frameId}))})()`, originJSON)
	raw, err := page.CallSessionContext(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true})
	if err != nil {
		return nil, err
	}
	var response struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	var tools []webMCPToolWire
	if len(response.Result.Value) == 0 || string(response.Result.Value) == "null" {
		return nil, fmt.Errorf("parent WebMCP getTools did not return serializable tools")
	}
	if err := json.Unmarshal(response.Result.Value, &tools); err != nil {
		return nil, err
	}
	return tools, nil
}

func webMCPBuildCatalog(targetID, pageURL string, tools []webMCPToolWire, frameOrigins map[string]string, coverageWarnings []string) *WebMCPCatalog {
	urlTruncated := len(pageURL) > maxWebMCPURLBytes
	if urlTruncated {
		pageURL = truncateUTF8Bytes(pageURL, maxWebMCPURLBytes)
	}
	catalog := &WebMCPCatalog{
		TargetID: targetID, URL: pageURL, ContentTrust: ContentTrustUntrustedPage,
		Tools: make([]WebMCPTool, 0, len(tools)), Complete: len(coverageWarnings) == 0,
		CoverageWarnings: append([]string(nil), coverageWarnings...), URLTruncated: urlTruncated,
	}
	// Reserve room for the catalog envelope and omission fields. Individual
	// schemas have their own cap, but many valid schemas can still exceed the
	// MCP response budget when returned together.
	baseBytes, _ := json.Marshal(&WebMCPCatalog{
		TargetID: catalog.TargetID, URL: catalog.URL, ContentTrust: catalog.ContentTrust,
		Tools: []WebMCPTool{}, Complete: catalog.Complete, CoverageWarnings: catalog.CoverageWarnings,
		URLTruncated: catalog.URLTruncated, Truncated: true, OmittedTools: maxWebMCPTools,
	})
	usedBytes := len(baseBytes)
	seen := make(map[string]bool)
	for _, tool := range tools {
		if tool.Name == "" || len(tool.Name) > 128 {
			catalog.OmittedTools++
			continue
		}
		if seen[tool.FrameID+"\x00"+tool.Name] {
			continue
		}
		seen[tool.FrameID+"\x00"+tool.Name] = true
		encoded, err := json.Marshal(tool)
		if err != nil || len(encoded) > maxWebMCPToolBytes {
			catalog.OmittedTools++
			continue
		}
		candidate := WebMCPTool{Name: tool.Name, Description: webMCPTruncate(tool.Description, 1000), InputSchema: tool.InputSchema, Annotations: tool.Annotations, FrameID: tool.FrameID, Origin: frameOrigins[tool.FrameID], ContentTrust: ContentTrustUntrustedPage}
		candidateJSON, err := json.Marshal(candidate)
		separatorBytes := 0
		if len(catalog.Tools) > 0 {
			separatorBytes = 1
		}
		if err != nil || usedBytes+len(candidateJSON)+separatorBytes > maxWebMCPCatalogBytes {
			catalog.OmittedTools++
			continue
		}
		usedBytes += len(candidateJSON) + separatorBytes
		catalog.Tools = append(catalog.Tools, candidate)
	}
	catalog.Truncated = catalog.OmittedTools > 0
	return catalog
}

// InvokeWebMCPToolContext invokes one page-provided WebMCP tool in its
// registered frame. A postcondition is mandatory: page tool output is never
// treated as proof that an application effect completed.
func (s *Session) InvokeWebMCPToolContext(ctx context.Context, targetID, frameID, name string, input map[string]any, condition Postcondition) (*PageResult, error) {
	if s == nil || ctx == nil {
		return nil, fmt.Errorf("WebMCP invocation requires a session and context")
	}
	if strings.TrimSpace(targetID) == "" || strings.TrimSpace(frameID) == "" || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("target_id, frame_id, and tool name are required")
	}
	if err := condition.Validate(); err != nil {
		return nil, fmt.Errorf("WebMCP invocation requires one postcondition: %w", err)
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if s.page.TargetID() != targetID {
		return nil, &OperationError{Phase: "target_binding", Cause: "stale_target", Detail: "active browser target changed; inspect the current page before retrying", OriginalError: "target identity mismatch"}
	}
	return s.invokeWebMCPToolLockedContext(ctx, s.page, targetID, frameID, name, input, condition)
}

// invokeWebMCPToolLockedContext invokes and verifies a page tool while the
// caller holds the session lock. Keeping this path shared lets task_run and
// verified batches use the same policy, schema, target, and outcome contract.
func (s *Session) invokeWebMCPToolLockedContext(ctx context.Context, page *browse.Page, targetID, frameID, name string, input map[string]any, condition Postcondition) (*PageResult, error) {
	if page == nil || page.TargetID() != targetID {
		return nil, &OperationError{Phase: "target_binding", Cause: "stale_target", Detail: "active browser target changed; inspect the current page before retrying", OriginalError: "target identity mismatch"}
	}
	if err := condition.Validate(); err != nil {
		return nil, fmt.Errorf("WebMCP invocation requires one postcondition: %w", err)
	}
	catalog, err := s.webmcpCatalogLockedContext(ctx, page)
	if err != nil {
		return nil, err
	}
	allowedCatalogTool := false
	for _, candidate := range catalog.Tools {
		if candidate.Name == name && candidate.FrameID == frameID {
			allowedCatalogTool = true
			break
		}
	}
	if !allowedCatalogTool {
		return nil, fmt.Errorf("WebMCP tool is not present in the current target/frame catalog")
	}
	if rootFrameID, err := webMCPRootFrameID(ctx, page); err == nil && rootFrameID != frameID {
		if provider, ok := s.browser.(interface {
			BrowserTargetsContext(context.Context) ([]browse.BrowserTarget, error)
		}); ok {
			targets, listErr := provider.BrowserTargetsContext(ctx)
			if listErr != nil {
				return nil, listErr
			}
			validator := browse.URLValidator{AllowPrivateIPs: s.allowPrivateIPs, PolicyID: taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs), AllowedOrigins: append([]string(nil), s.allowedOrigins...)}
			for _, target := range targets {
				if target.Type != "iframe" {
					continue
				}
				if target.ParentFrameID != rootFrameID {
					continue
				}
				if err := validator.Validate(target.URL); err != nil {
					continue
				}
				framePage, attachErr := page.AttachFrameTargetContext(ctx, target.TargetID)
				if attachErr != nil {
					continue
				}
				attachedFrameID, frameErr := webMCPRootFrameID(ctx, framePage)
				if frameErr == nil && attachedFrameID == frameID {
					result, invokeErr := s.invokeWebMCPToolLockedContext(ctx, framePage, targetID, frameID, name, input, condition)
					return result, errors.Join(invokeErr, framePage.DetachContext(context.Background()))
				}
				_ = framePage.DetachContext(context.Background())
			}
		}
	}
	allowed := false
	for _, tool := range catalog.Tools {
		if tool.Name == name && tool.FrameID == frameID {
			allowed = true
			if tool.Origin == "" {
				return nil, fmt.Errorf("WebMCP tool frame origin could not be established; refusing invocation")
			}
			validator := browse.URLValidator{AllowPrivateIPs: s.allowPrivateIPs, PolicyID: taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs), AllowedOrigins: append([]string(nil), s.allowedOrigins...)}
			if err := validator.Validate(tool.Origin); err != nil {
				return nil, fmt.Errorf("WebMCP tool frame origin is outside the session policy: %w", err)
			}
			if err := validateWebMCPInput(tool.InputSchema, input); err != nil {
				return nil, err
			}
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("WebMCP tool is not present in the current target/frame catalog")
	}
	inputJSON, err := json.Marshal(input)
	if err != nil || len(inputJSON) > maxWebMCPToolBytes {
		return nil, fmt.Errorf("WebMCP input is invalid or exceeds the size limit")
	}
	responses := make(chan webMCPResponseEvent, 1)
	remove := page.OnRawSession("WebMCP.toolResponded", func(raw json.RawMessage) {
		var header struct {
			InvocationID string `json:"invocationId"`
			Status       string `json:"status"`
			ErrorText    string `json:"errorText"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return
		}
		var event webMCPResponseEvent
		switch {
		case len(raw) > maxWebMCPResultBytes+(4<<10):
			event = webMCPResponseEvent{InvocationID: header.InvocationID, Status: header.Status, ErrorText: header.ErrorText, Oversized: true}
		case json.Unmarshal(raw, &event) != nil:
			return
		default:
			event.ResultBytes = len(event.Output)
		}
		select {
		case responses <- event:
		default:
		}
	})
	defer remove()
	var invocation webMCPInvocationResponse
	result, err := page.CallSessionContext(ctx, "WebMCP.invokeTool", map[string]any{"frameId": frameID, "toolName": name, "input": input})
	if err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, webMCPFrameOutcome(s.actionOutcomeAfterDispatchForPage(page, "webmcp_"+name, name, "WebMCP invocation may have been dispatched; reconcile the page before retrying", err, condition), frameID)
		}
		return nil, err
	}
	if err := json.Unmarshal(result, &invocation); err != nil || invocation.InvocationID == "" {
		return nil, fmt.Errorf("WebMCP invocation did not return an invocation ID")
	}
	var response webMCPResponseEvent
	select {
	case response = <-responses:
		if response.InvocationID != invocation.InvocationID {
			return nil, webMCPFrameOutcome(s.actionOutcomeAfterDispatchForPage(page, "webmcp_"+name, name, "WebMCP invocation response could not be correlated; reconcile before retrying", errors.New("invocation id mismatch"), condition), frameID)
		}
	case <-ctx.Done():
		return nil, webMCPFrameOutcome(s.actionOutcomeAfterDispatchForPage(page, "webmcp_"+name, name, "WebMCP invocation may still be running; reconcile before retrying", ctx.Err(), condition), frameID)
	}
	var verified *PageResult
	if page == s.page {
		verified, err = s.verifyPagePostconditionContextLocked(ctx, "webmcp_"+name, name, condition)
	} else {
		verified, err = s.verifyWebMCPPostconditionOnTarget(ctx, page, "webmcp_"+name, name, condition)
	}
	if err != nil {
		reason := "WebMCP returned a result but the declared postcondition could not be verified"
		cause := err
		if response.Status != "Completed" {
			reason = "WebMCP reported failure after dispatch and the declared postcondition could not be verified"
			cause = errors.Join(fmt.Errorf("WebMCP invocation status %q: %s", response.Status, webMCPTruncate(response.ErrorText, 500)), err)
		}
		return nil, webMCPFrameOutcome(s.actionOutcomeAfterDispatchForPage(page, "webmcp_"+name, name, reason, cause, condition), frameID)
	}
	if !verified.Verified {
		cause := fmt.Errorf("declared postcondition was not observed after WebMCP invocation (status %q): %s", response.Status, verified.Evidence)
		return nil, webMCPFrameOutcome(s.actionOutcomeAfterDispatchForPage(page, "webmcp_"+name, name, "WebMCP invocation did not establish the declared postcondition; reconcile before retrying", cause, condition), frameID)
	}
	verified.FrameID = frameID
	if verified.Receipt != nil {
		verified.Receipt.FrameID = frameID
	}
	verified.Evidence = "declared page postcondition verified; WebMCP result is untrusted and omitted from verification"
	if response.Status != "Completed" {
		verified.Evidence = "declared page postcondition verified despite WebMCP status " + fmt.Sprintf("%q", response.Status) + "; page result is untrusted and omitted from verification"
	}
	if verified.Receipt != nil {
		verified.Receipt.ContentTrust = ContentTrustUntrustedPage
	}
	if response.Oversized {
		verified.Evidence += "; untrusted WebMCP result exceeded the " + fmt.Sprint(maxWebMCPResultBytes) + " byte capture limit"
	} else {
		verified.Evidence += "; untrusted WebMCP result bytes: " + fmt.Sprint(response.ResultBytes)
	}
	if verified.Receipt != nil {
		verified.Receipt.Evidence = verified.Evidence
	}
	return verified, nil
}

func (s *Session) verifyWebMCPPostconditionOnTarget(ctx context.Context, page *browse.Page, action, target string, condition Postcondition) (*PageResult, error) {
	var expression string
	failure := "declared postcondition was not observed in the registered frame"
	switch {
	case condition.Text != "":
		want, _ := json.Marshal(condition.Text)
		expression = fmt.Sprintf(`(document.body?.innerText||document.body?.textContent||'').includes(%s)`, want)
	case condition.URL != "":
		want, _ := json.Marshal(condition.URL)
		expression = fmt.Sprintf(`location.href === %s`, want)
	case condition.URLContains != "":
		want, _ := json.Marshal(condition.URLContains)
		expression = fmt.Sprintf(`location.href.includes(%s)`, want)
	case condition.Title != "":
		want, _ := json.Marshal(condition.Title)
		expression = fmt.Sprintf(`document.title === %s`, want)
	case condition.ExistsSelector != "":
		selector, _ := json.Marshal(condition.ExistsSelector)
		expression = fmt.Sprintf(`document.querySelector(%s) !== null`, selector)
	case condition.VisibleSelector != "":
		selector, _ := json.Marshal(condition.VisibleSelector)
		expression = fmt.Sprintf(`(()=>{const e=document.querySelector(%s);if(!e)return false;const r=e.getBoundingClientRect(),s=getComputedStyle(e);return r.width>0&&r.height>0&&s.visibility!=='hidden'&&s.display!=='none'})()`, selector)
	case condition.ValueSelector != "":
		selector, value := webMCPJSON(condition.ValueSelector), webMCPJSON(condition.Value)
		expression = fmt.Sprintf(`(()=>{const e=document.querySelector(%s);return !!e&&String(('value' in e)?e.value:(e.innerText||e.textContent||''))===%s})()`, selector, value)
	case condition.CountSelector != "" && condition.Count != nil:
		selector := webMCPJSON(condition.CountSelector)
		expression = fmt.Sprintf(`document.querySelectorAll(%s).length === %d`, selector, *condition.Count)
	default:
		return nil, fmt.Errorf("postcondition requires exactly one condition")
	}
	state, err := page.EvaluateContext(ctx, expression)
	if err != nil {
		return nil, err
	}
	verified, ok := state.(bool)
	if !ok {
		return nil, fmt.Errorf("frame postcondition returned an invalid result")
	}
	result, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, err
	}
	result.Action, result.Target, result.Verified, result.Evidence = action, target, verified, "declared postcondition checked in the registered WebMCP frame"
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	if verified {
		result.Outcome = "postcondition_verified"
	} else {
		result.Outcome = "postcondition_failed"
		result.Evidence = failure
		result.RecoveryHint = "Inspect the registered frame and reconcile the workflow before continuing."
	}
	postcondition := condition
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, &postcondition, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	return result, nil
}

func webMCPJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func webMCPRootFrameID(ctx context.Context, page *browse.Page) (string, error) {
	raw, err := page.CallSessionContext(ctx, "Page.getFrameTree", map[string]any{})
	if err != nil {
		return "", err
	}
	var tree struct {
		FrameTree webMCPFrameTree `json:"frameTree"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil {
		return "", err
	}
	if tree.FrameTree.Frame.ID == "" {
		return "", fmt.Errorf("frame tree did not contain a root frame")
	}
	return tree.FrameTree.Frame.ID, nil
}

func webMCPFrameOutcome(err error, frameID string) error {
	var outcome *ActionOutcomeError
	if errors.As(err, &outcome) {
		outcome.FrameID = frameID
		if outcome.Receipt != nil {
			outcome.Receipt.FrameID = frameID
		}
	}
	return err
}

func validateWebMCPInput(schema map[string]any, input map[string]any) error {
	if schema == nil {
		return fmt.Errorf("WebMCP tool has no input schema; refusing invocation")
	}
	return validateWebMCPSchema(schema, input, "$", 0)
}

// validateWebMCPSchema implements a deliberately bounded JSON Schema subset.
// Unknown schema keywords fail closed so page-provided constraints are not
// silently ignored before dispatch.
func validateWebMCPSchema(schema map[string]any, value any, path string, depth int) error {
	if depth > 16 {
		return fmt.Errorf("WebMCP input schema exceeds maximum nesting")
	}
	allowed := map[string]bool{"$schema": true, "$comment": true, "type": true, "properties": true, "required": true, "additionalProperties": true, "items": true, "enum": true, "minimum": true, "maximum": true, "minLength": true, "maxLength": true, "minItems": true, "maxItems": true, "minProperties": true, "maxProperties": true, "description": true, "title": true}
	for key := range schema {
		if !allowed[key] {
			return fmt.Errorf("WebMCP input schema keyword %q is unsupported", key)
		}
	}
	if choices, ok := schema["enum"].([]any); ok {
		matched := false
		encoded, _ := json.Marshal(value)
		for _, choice := range choices {
			candidate, _ := json.Marshal(choice)
			if string(encoded) == string(candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("WebMCP input %s is not an allowed enum value", path)
		}
	}
	typ, _ := schema["type"].(string)
	if typ == "" {
		return fmt.Errorf("WebMCP input schema at %s must declare a type", path)
	}
	switch typ {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("WebMCP input %s must be an object", path)
		}
		properties, hasProperties := schema["properties"].(map[string]any)
		if _, exists := schema["properties"]; exists && !hasProperties {
			return fmt.Errorf("WebMCP input schema properties are invalid")
		}
		required := []any{}
		if rawRequired, exists := schema["required"]; exists {
			var ok bool
			required, ok = rawRequired.([]any)
			if !ok {
				return fmt.Errorf("WebMCP input schema required fields are invalid")
			}
		}
		for _, raw := range required {
			key, ok := raw.(string)
			if !ok {
				return fmt.Errorf("WebMCP input schema has invalid required field")
			}
			if _, exists := obj[key]; !exists {
				return fmt.Errorf("WebMCP input is missing required field %q", key)
			}
		}
		if !withinCount(len(obj), schema["minProperties"], schema["maxProperties"]) {
			return fmt.Errorf("WebMCP input %s has invalid property count", path)
		}
		additional, hasAdditional := schema["additionalProperties"].(bool)
		additionalSchema, hasAdditionalSchema := schema["additionalProperties"].(map[string]any)
		if _, exists := schema["additionalProperties"]; exists && !hasAdditional && !hasAdditionalSchema {
			return fmt.Errorf("WebMCP input schema additionalProperties is invalid")
		}
		for key, item := range obj {
			rawProp, exists := properties[key]
			if !exists {
				if hasAdditional && !additional {
					return fmt.Errorf("WebMCP input contains unknown field %q", key)
				}
				if hasAdditionalSchema {
					if err := validateWebMCPSchema(additionalSchema, item, path+"."+key, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			prop, ok := rawProp.(map[string]any)
			if !ok {
				return fmt.Errorf("WebMCP input schema property %q is invalid", key)
			}
			if err := validateWebMCPSchema(prop, item, path+"."+key, depth+1); err != nil {
				return err
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("WebMCP input %s must be an array", path)
		}
		if !withinCount(len(arr), schema["minItems"], schema["maxItems"]) {
			return fmt.Errorf("WebMCP input %s has invalid item count", path)
		}
		if rawItems, exists := schema["items"]; exists {
			items, ok := rawItems.(map[string]any)
			if !ok {
				return fmt.Errorf("WebMCP input schema items are invalid")
			}
			for i, item := range arr {
				if err := validateWebMCPSchema(items, item, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
	case "string":
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("WebMCP input %s must be a string", path)
		}
		if !withinCount(len([]rune(str)), schema["minLength"], schema["maxLength"]) {
			return fmt.Errorf("WebMCP input %s has invalid string length", path)
		}
	case "number", "integer":
		n, ok := webMCPNumber(value)
		if !ok || typ == "integer" && n != float64(int64(n)) {
			return fmt.Errorf("WebMCP input %s must be a %s", path, typ)
		}
		if min, ok := schema["minimum"].(float64); ok && n < min {
			return fmt.Errorf("WebMCP input %s is below minimum", path)
		}
		if max, ok := schema["maximum"].(float64); ok && n > max {
			return fmt.Errorf("WebMCP input %s is above maximum", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("WebMCP input %s must be a boolean", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("WebMCP input %s must be null", path)
		}
	default:
		return fmt.Errorf("WebMCP input schema type %q is unsupported", typ)
	}
	return nil
}

func withinCount(count int, minValue, maxValue any) bool {
	if min, ok := webMCPNumber(minValue); ok && float64(count) < min {
		return false
	}
	if max, ok := webMCPNumber(maxValue); ok && float64(count) > max {
		return false
	}
	return true
}
func webMCPNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func browseURLOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid page URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

func webMCPTruncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
