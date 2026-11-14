package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"go.klarlabs.de/mcp"
	"go.klarlabs.de/mcp/protocol"
	mcpserver "go.klarlabs.de/mcp/server"
	mcptransport "go.klarlabs.de/mcp/transport"

	browse "go.klarlabs.de/scout"
	"go.klarlabs.de/scout/agent"
	cdpclient "go.klarlabs.de/scout/internal/cdp"
	waitutil "go.klarlabs.de/scout/internal/wait"
	"go.opentelemetry.io/otel"
)

type MCPErrorEnvelope struct {
	Code                  string                        `json:"code"`
	Message               string                        `json:"message"`
	ContentTrust          string                        `json:"content_trust,omitempty"`
	RunID                 string                        `json:"run_id,omitempty"`
	Step                  int                           `json:"step,omitempty"`
	OperationOrdinal      int                           `json:"operation_ordinal,omitempty"`
	Outcome               string                        `json:"outcome,omitempty"`
	Action                string                        `json:"action,omitempty"`
	Target                string                        `json:"target,omitempty"`
	TargetID              string                        `json:"target_id,omitempty"`
	PolicyID              string                        `json:"policy_id,omitempty"`
	PolicyEvidenceRef     string                        `json:"policy_evidence_ref,omitempty"`
	FrameID               string                        `json:"frame_id,omitempty"`
	TraceID               string                        `json:"trace_id,omitempty"`
	TraceStep             int                           `json:"trace_step,omitempty"`
	TraceActionOrdinal    int                           `json:"trace_action_ordinal,omitempty"`
	DeadlineAt            int64                         `json:"deadline_at_ms,omitempty"`
	ConfiguredTimeoutMS   int64                         `json:"configured_timeout_ms,omitempty"`
	SessionDead           *bool                         `json:"session_dead,omitempty"`
	ConsecutiveTimeouts   *int                          `json:"consecutive_timeouts,omitempty"`
	InFlightCommands      *int                          `json:"inflight_commands,omitempty"`
	URL                   string                        `json:"url,omitempty"`
	Evidence              string                        `json:"evidence,omitempty"`
	RecoveryHint          string                        `json:"recovery_hint,omitempty"`
	ExpectedPostcondition *agent.Postcondition          `json:"expected_postcondition,omitempty"`
	ResponseEvidence      *agent.ActionResponseEvidence `json:"response_evidence,omitempty"`
	ActionReceipt         *agent.ActionReceipt          `json:"action_receipt,omitempty"`
	PartialResult         any                           `json:"partial_result,omitempty"`
	RecoveryRecord        *agent.ActionRecoveryRecord   `json:"recovery_record,omitempty"`
	Phase                 string                        `json:"phase,omitempty"`
	Cause                 string                        `json:"cause,omitempty"`
	StatusCode            int                           `json:"status_code,omitempty"`
	Detail                string                        `json:"detail,omitempty"`
	Hint                  string                        `json:"hint,omitempty"`
}

type mcpHTTPPrincipalKey struct{}

type mcpHTTPPrincipal struct {
	Token                 string   `json:"token"`
	RemoteCDP             string   `json:"remote_cdp"`
	ProxyServer           string   `json:"proxy_server,omitempty"`
	AllowedOrigins        []string `json:"allowed_origins"`
	AllowPrivateIPs       bool     `json:"allow_private_ips,omitempty"`
	IsolateBrowserContext bool     `json:"isolate_browser_context,omitempty"`
}

type mcpHTTPPrincipalSessions struct {
	mu     sync.Mutex
	owners map[string]string
}

func newMCPHTTPPrincipalSessions() *mcpHTTPPrincipalSessions {
	return &mcpHTTPPrincipalSessions{owners: make(map[string]string)}
}

func (s *mcpHTTPPrincipalSessions) authorize(r *http.Request, policies map[string]mcpHTTPPrincipal, stateless bool) (string, error) {
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, "Bearer ") || len(got) <= len("Bearer ") {
		return "", fmt.Errorf("invalid bearer token")
	}
	token := strings.TrimPrefix(got, "Bearer ")
	principal := ""
	for candidate, policy := range policies {
		if len(token) == len(policy.Token) && subtle.ConstantTimeCompare([]byte(token), []byte(policy.Token)) == 1 {
			principal = candidate
		}
	}
	if principal == "" {
		return "", fmt.Errorf("invalid bearer token")
	}
	clientID := ""
	if !stateless {
		clientID = r.Header.Get("Mcp-Session-Id")
	}
	if clientID == "" {
		return principal, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner := s.owners[clientID]; owner != principal {
		return "", fmt.Errorf("MCP session belongs to another principal or does not exist")
	}
	return principal, nil
}

func (s *mcpHTTPPrincipalSessions) bind(clientID, principal string) error {
	if clientID == "" || principal == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner := s.owners[clientID]; owner != "" && owner != principal {
		return fmt.Errorf("MCP session ID is already bound to another principal")
	}
	s.owners[clientID] = principal
	return nil
}

func (s *mcpHTTPPrincipalSessions) retain(clientIDs []string) {
	live := make(map[string]struct{}, len(clientIDs))
	for _, id := range clientIDs {
		live[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.owners {
		if _, ok := live[id]; !ok {
			delete(s.owners, id)
		}
	}
}

func (s *mcpHTTPPrincipalSessions) principalForSession(clientID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	principal := s.owners[clientID]
	delete(s.owners, clientID)
	return principal
}

func mcpHTTPPrincipalFromContext(ctx context.Context) string {
	principal, _ := ctx.Value(mcpHTTPPrincipalKey{}).(string)
	return principal
}

type mcpRequestSessionKey struct{}

// mcpRequestSession is the canonical owner/session binding for one tool call.
// The selector is an address only; authorization has already resolved it
// inside ownerKey's registry before this value is attached to the context.
type mcpRequestSession struct {
	ownerKey   string
	sessionKey string
	sessionID  string
	session    *agent.Session
	explicit   bool
}

func mcpRequestSessionFromContext(ctx context.Context) (mcpRequestSession, bool) {
	routing, ok := ctx.Value(mcpRequestSessionKey{}).(mcpRequestSession)
	return routing, ok
}

type mcpToolCallParams struct {
	Name      string                     `json:"name"`
	Arguments map[string]json.RawMessage `json:"arguments"`
	Meta      json.RawMessage            `json:"_meta"`
}

const mcpSessionSelectorMetaKey = "go.klarlabs.de/scout/session"

func parseMCPCallSessionSelector(meta json.RawMessage, toolName string) (string, error) {
	if len(meta) == 0 || string(meta) == "null" {
		return "", nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(meta, &metadata); err != nil {
		return "", fmt.Errorf("invalid MCP _meta: expected an object")
	}
	raw := metadata[mcpSessionSelectorMetaKey]
	if len(raw) == 0 {
		return "", nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", fmt.Errorf("invalid %s selector: expected a session name string", mcpSessionSelectorMetaKey)
	}
	if !strings.HasPrefix(name, "scs:") {
		validatedName, err := validateSessionName(name)
		if err != nil {
			return "", fmt.Errorf("invalid %s selector: %w", mcpSessionSelectorMetaKey, err)
		}
		name = validatedName
	} else if !validMCPOpaqueSessionID(name) {
		return "", fmt.Errorf("invalid %s selector: malformed opaque session ID", mcpSessionSelectorMetaKey)
	}
	switch toolName {
	case "configure", "list_sessions", "create_session", "switch_session", "close_session", "clone_session":
		return "", fmt.Errorf("%s is not supported on owner-level tool %q", mcpSessionSelectorMetaKey, toolName)
	}
	return name, nil
}

// mcpStatelessToolRequiresSessionSelector identifies calls whose work or result
// belongs to a browser session. Stateless HTTP has no transport ID to provide
// an implicit active-session namespace, so these calls must carry the opaque
// Scout session handle on every request.
func mcpStatelessToolRequiresSessionSelector(toolName string) bool {
	switch toolName {
	case "configure", "help", "list_sessions",
		"persist_session", "resume_session", "revoke_session", "create_session",
		"switch_session", "close_session", "clone_session":
		return false
	default:
		return true
	}
}

func mcpHTTPClientScope(principal, transportID string, stateless bool) string {
	if principal != "" {
		if stateless {
			// Stateless requests do not have a transport-owned namespace. Ignore
			// stale or caller-supplied Mcp-Session-Id values and derive the stable
			// scope only from the authenticated principal.
			return principal + "::stateless"
		}
		return principal + "::" + transportID
	}
	return transportID
}

func newMCPOpaqueSessionID() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return "scs:" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func validMCPOpaqueSessionID(id string) bool {
	if !strings.HasPrefix(id, "scs:") {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "scs:"))
	return err == nil && len(raw) == 24
}

func resolveMCPSessionSelector(selector string, sessionKeysByID map[string]string, keyForName func(string) string, visible func(string) bool) (string, error) {
	if strings.HasPrefix(selector, "scs:") {
		key := sessionKeysByID[selector]
		if key == "" || !visible(key) {
			return "", fmt.Errorf("session does not exist for this client")
		}
		return key, nil
	}
	return keyForName(selector), nil
}

func mcpArtifactOwner(owner, sessionKey string) string {
	if sessionKey == "" {
		return "owner/" + owner
	}
	return "owner/" + owner + "/session/" + sessionKey
}

func mcpSessionKeyVisibleToOwner(key, clientID, principal string) bool {
	if clientID == "" {
		return !strings.HasPrefix(key, "http/")
	}
	if strings.HasPrefix(key, "http/"+clientID+"/") {
		return true
	}
	if principal == "" || !strings.HasPrefix(key, "http/") {
		return false
	}
	owner, _, ok := strings.Cut(strings.TrimPrefix(key, "http/"), "/")
	if !ok {
		return false
	}
	ownerPrincipal, _, hasTransport := strings.Cut(owner, "::")
	return hasTransport && ownerPrincipal == principal
}

func mcpSessionOwnerScope(key string) string {
	if !strings.HasPrefix(key, "http/") {
		return "stdio_process"
	}
	owner, _, ok := strings.Cut(strings.TrimPrefix(key, "http/"), "/")
	if !ok {
		return "transport"
	}
	if strings.Contains(owner, "::") {
		return "authenticated_principal"
	}
	return "transport"
}

func cleanupPrivateArtifactsForOwner(mu *sync.Mutex, registered map[string][32]byte, owners map[string]string, sizes map[string]int64, totalBytes *int64, ownerBytes map[string]int64, ownerCounts map[string]int, owner string) (removedBytes int64, removedCount int) {
	mu.Lock()
	defer mu.Unlock()
	for path, artifactOwner := range owners {
		if artifactOwner != owner {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			continue
		}
		delete(registered, path)
		delete(owners, path)
		if size, hasSize := sizes[path]; hasSize {
			removedBytes += size
			removedCount++
			*totalBytes -= size
			ownerBytes[owner] -= size
			ownerCounts[owner]--
			delete(sizes, path)
		}
	}
	if ownerCounts[owner] <= 0 {
		delete(ownerCounts, owner)
		delete(ownerBytes, owner)
	}
	return removedBytes, removedCount
}

func envBool(key string) bool {
	v := os.Getenv(key)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

const maxStateOutputBytes = 64 * 1024

type stateOutputBudget struct {
	nodes     int
	truncated bool
}

func boundStateOutput(value map[string]any) map[string]any {
	budget := &stateOutputBudget{nodes: 300}
	bounded, _ := boundStateValue(value, 0, budget).(map[string]any)
	if bounded == nil {
		bounded = map[string]any{}
	}
	if budget.truncated {
		bounded["_scout_truncated"] = true
	}
	encoded, err := json.Marshal(bounded)
	if err == nil && len(encoded) <= maxStateOutputBytes {
		return bounded
	}
	return map[string]any{
		"_scout_truncated": true,
		"_scout_error":     "state exceeded the 64 KB MCP output limit; use the Go API for full state",
	}
}

func boundStateValue(value any, depth int, budget *stateOutputBudget) any {
	if budget.nodes <= 0 || depth >= 8 {
		budget.truncated = true
		return "[truncated]"
	}
	budget.nodes--
	switch v := value.(type) {
	case string:
		if len(v) > 256 {
			budget.truncated = true
			return truncateUTF8(v, 256) + "…"
		}
		return v
	case []any:
		limit := min(len(v), 30)
		out := make([]any, 0, limit)
		for _, item := range v[:limit] {
			out = append(out, boundStateValue(item, depth+1, budget))
		}
		if limit < len(v) {
			budget.truncated = true
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		limit := min(len(keys), 30)
		out := make(map[string]any, limit)
		for _, key := range keys[:limit] {
			boundedKey := key
			if len(boundedKey) > 128 {
				boundedKey = truncateUTF8(boundedKey, 128) + "…"
				budget.truncated = true
			}
			out[boundedKey] = boundStateValue(v[key], depth+1, budget)
		}
		if limit < len(keys) {
			budget.truncated = true
		}
		return out
	default:
		return value
	}
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && (value[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return value[:maxBytes]
}

const maxMCPToolResponseBytes = 256 * 1024
const maxMCPBatchResponseBytes = 192 * 1024
const maxMCPResultArtifactBytes = 16 * 1024 * 1024
const maxMCPResultArtifactTotalBytes = 64 * 1024 * 1024
const maxMCPResultArtifactCount = 32
const maxMCPResultArtifactOwnerBytes = 32 * 1024 * 1024
const maxMCPResultArtifactOwnerCount = 4
const maxPageSnapshotChars = 2_000_000
const maxPageSnapshotExcerptChars = 20_000

func artifactQuotaAvailable(totalCount int, totalBytes int64, ownerCount int, ownerBytes int64, size int64) bool {
	return size >= 0 && totalCount < maxMCPResultArtifactCount && totalBytes+size <= maxMCPResultArtifactTotalBytes &&
		ownerCount < maxMCPResultArtifactOwnerCount && ownerBytes+size <= maxMCPResultArtifactOwnerBytes
}

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
var jsonRawMessageType = reflect.TypeOf(json.RawMessage(nil))

// minimumJSONBytes returns a conservative lower bound for ordinary JSON data.
// It deliberately returns unknown for custom marshalers and ambiguous struct
// layouts. A lower bound above the response ceiling proves the result is too
// large without allocating a serialized copy; all other results use the exact
// marshal check below.
func minimumJSONBytes(value any) (uint64, bool) {
	return minimumJSONValueBytes(reflect.ValueOf(value), 0)
}

func minimumJSONValueBytes(value reflect.Value, depth int) (uint64, bool) {
	if depth > 100 || !value.IsValid() {
		return 0, depth <= 100
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, true
		}
		return minimumJSONValueBytes(value.Elem(), depth+1)
	}
	typeOf := value.Type()
	if typeOf == jsonRawMessageType || typeOf.Implements(jsonMarshalerType) || (typeOf.Kind() != reflect.Pointer && reflect.PointerTo(typeOf).Implements(jsonMarshalerType)) {
		return 0, false
	}
	switch value.Kind() {
	case reflect.String:
		// JSON emits every Unicode code point as at least one byte. Counting
		// runes rather than UTF-8 bytes keeps this a lower bound for non-ASCII
		// strings while still allowing early rejection for large values.
		return uint64(utf8.RuneCountInString(value.String())), true
	case reflect.Slice:
		if value.IsNil() {
			return 0, true
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return uint64(value.Len()), true // JSON base64 is never shorter than the source bytes.
		}
		var total uint64
		for i := 0; i < value.Len(); i++ {
			part, ok := minimumJSONValueBytes(value.Index(i), depth+1)
			if !ok {
				return 0, false
			}
			total += part
		}
		return total, true
	case reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return uint64(value.Len()), true
		}
		var total uint64
		for i := 0; i < value.Len(); i++ {
			part, ok := minimumJSONValueBytes(value.Index(i), depth+1)
			if !ok {
				return 0, false
			}
			total += part
		}
		return total, true
	case reflect.Map:
		if value.IsNil() {
			return 0, true
		}
		if value.Type().Key().Kind() != reflect.String {
			return 0, false
		}
		keyType := value.Type().Key()
		if keyType.Implements(reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()) || (keyType.Kind() != reflect.Pointer && reflect.PointerTo(keyType).Implements(reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem())) {
			return 0, false
		}
		var total uint64
		iter := value.MapRange()
		for iter.Next() {
			// Map keys are JSON strings too. Counting raw UTF-8 bytes remains a
			// lower bound even when the encoder escapes them.
			total += uint64(utf8.RuneCountInString(iter.Key().String()))
			part, ok := minimumJSONValueBytes(iter.Value(), depth+1)
			if !ok {
				return 0, false
			}
			total += part
		}
		return total, true
	case reflect.Struct:
		// Anonymous fields and duplicate JSON names can be shadowed by the
		// encoding/json field-selection rules, so decline to estimate them.
		fields := make([]reflect.StructField, 0, value.NumField())
		counts := make(map[string]int, value.NumField())
		for i := 0; i < value.NumField(); i++ {
			field := typeOf.Field(i)
			if field.PkgPath != "" || field.Anonymous {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			counts[name]++
			fields = append(fields, field)
		}
		var total uint64
		for _, field := range fields {
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" {
				name = field.Name
			}
			if counts[name] > 1 {
				return 0, false
			}
			fieldValue := value.FieldByIndex(field.Index)
			if hasJSONOption(options, "omitempty") && isEmptyJSONValue(fieldValue) {
				continue
			}
			if hasJSONOption(options, "omitzero") && fieldValue.IsZero() {
				continue
			}
			part, ok := minimumJSONValueBytes(fieldValue, depth+1)
			if !ok {
				return 0, false
			}
			total += part
		}
		return total, true
	default:
		return 0, true
	}
}

func hasJSONOption(options, wanted string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == wanted {
			return true
		}
	}
	return false
}

func isEmptyJSONValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool:
		return !value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return value.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return value.IsNil()
	}
	return false
}

func boundedToolResponseMiddleware(saveResultArtifact ...func(context.Context, []byte) (*agent.ArtifactReference, error)) mcp.Middleware {
	return func(next mcp.MiddlewareHandlerFunc) mcp.MiddlewareHandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			response, err := next(ctx, req)
			if err != nil || response == nil || req == nil || req.Method != "tools/call" {
				return response, err
			}
			toolName := requestToolName(req.Params)
			var originalResult []byte
			var originalResponse []byte
			if toolName == "batch" || toolName == "task_run" || toolName == "replay_playbook" || toolName == "resume_playbook" {
				if before, marshalErr := json.Marshal(response); marshalErr == nil && len(before) > maxMCPBatchResponseBytes {
					originalResponse = before
					originalResult, _ = json.Marshal(response.Result)
				}
			}
			// Batch results are compacted before the generic output guard. A large
			// batch must retain its per-step outcome and recovery handoffs so an
			// agent can safely continue after an interrupted operation.
			switch toolName {
			case "batch":
				compactBatchToolResponse(response)
			case "task_run":
				compactTaskRunToolResponse(response)
			case "replay_playbook", "resume_playbook":
				compactPlaybookToolResponse(response)
			}
			var preCompactionArtifact *agent.ArtifactReference
			var preCompactionArtifactErr error
			if len(originalResult) > 0 {
				if compacted, marshalErr := json.Marshal(response); marshalErr == nil && !bytes.Equal(compacted, originalResponse) {
					if len(saveResultArtifact) > 0 && saveResultArtifact[0] != nil {
						if len(originalResult) > maxMCPResultArtifactBytes {
							preCompactionArtifactErr = fmt.Errorf("full result exceeds the 16 MiB artifact limit")
						} else if saved, saveErr := saveResultArtifact[0](ctx, originalResult); saveErr == nil {
							preCompactionArtifact = saved
							appendCompactionArtifactHint(response, saved)
						} else {
							preCompactionArtifactErr = saveErr
						}
					} else {
						preCompactionArtifactErr = errors.New("private artifact storage is unavailable")
					}
					if preCompactionArtifact == nil {
						appendCompactionArtifactFailure(response, preCompactionArtifactErr)
					}
				}
			}
			responseBytes := -1
			tooLarge := false
			if response.Error != nil {
				if minimum, ok := minimumJSONBytes(response.Error); ok && minimum > maxMCPToolResponseBytes {
					tooLarge = true
				}
			} else if minimum, ok := minimumJSONBytes(response.Result); ok && minimum > maxMCPToolResponseBytes {
				tooLarge = true
			}
			if !tooLarge {
				encoded, marshalErr := json.Marshal(response)
				if marshalErr == nil && len(encoded) <= maxMCPToolResponseBytes {
					return response, nil
				}
				if marshalErr == nil {
					responseBytes = len(encoded)
				}
			}
			artifact := preCompactionArtifact
			var artifactSaveErr error
			artifactPayload := response.Result
			if response.Error != nil {
				artifactPayload = response.Error
			}
			if artifact == nil && len(saveResultArtifact) > 0 && saveResultArtifact[0] != nil {
				if data, marshalErr := json.Marshal(artifactPayload); marshalErr != nil {
					artifactSaveErr = marshalErr
				} else if len(data) > maxMCPResultArtifactBytes {
					artifactSaveErr = fmt.Errorf("serialized response exceeds the 16 MiB artifact limit")
				} else {
					artifact, artifactSaveErr = saveResultArtifact[0](ctx, data)
				}
			}
			limit := struct {
				Code                    string `json:"code"`
				ContentTrust            string `json:"content_trust"`
				OutputOmitted           bool   `json:"output_omitted"`
				Truncated               bool   `json:"truncated"`
				SerializedResponseBytes int    `json:"serialized_response_bytes,omitempty"`
				MaxResponseBytes        int    `json:"max_response_bytes"`
				Artifact                any    `json:"artifact,omitempty"`
				RecoveryHint            string `json:"recovery_hint"`
			}{
				Code:             "SCOUT_OUTPUT_LIMIT",
				ContentTrust:     agent.ContentTrustUntrustedPage,
				OutputOmitted:    true,
				Truncated:        true,
				MaxResponseBytes: maxMCPToolResponseBytes,
				RecoveryHint:     "Narrow the requested output or use a supported disk-backed capture.",
			}
			if responseBytes >= 0 {
				limit.SerializedResponseBytes = responseBytes
			}
			if artifact != nil {
				limit.Artifact = artifact
				limit.RecoveryHint = "Read or search the saved JSON artifact with read_page_snapshot or search_page_snapshot; delete it with delete_page_snapshot when finished."
			} else if artifactSaveErr != nil {
				limit.RecoveryHint = "Scout could not save the full response artifact: " + truncateUTF8(artifactSaveErr.Error(), 200) + ". Narrow the requested output, delete unused artifacts, or use another supported disk-backed capture."
			}
			if response.Error != nil {
				response.Error = &protocol.Error{
					Code:    response.Error.Code,
					Message: "Scout omitted this tool error because its serialized size exceeds the 256 KB MCP response limit.",
					Data:    limit,
				}
				//nolint:nilerr // This error is represented by the MCP protocol response.
				return response, nil
			}
			limitJSON, _ := json.Marshal(limit)
			response.Result = map[string]any{
				"content": []map[string]string{{
					"type": "text",
					"text": "Scout omitted this tool result from the inline response because it exceeds the 256 KB MCP response limit. " + string(limitJSON),
				}},
				"isError": true,
			}
			return response, nil
		}
	}
}

func appendCompactionArtifactHint(response *protocol.Response, artifact *agent.ArtifactReference) {
	if response == nil || artifact == nil {
		return
	}
	structured, content, isError, ok := batchStructuredResult(response.Result)
	if !ok {
		return
	}
	content = append(content, mcpserver.Content{Type: "text", Text: fmt.Sprintf("The full pre-compaction result is saved as a private artifact at %s (SHA-256 %s). Use read_page_snapshot or search_page_snapshot with this path; delete it with delete_page_snapshot when finished.", artifact.Path, artifact.SHA256)})
	response.Result = mcpserver.StructuredResult{Content: content, StructuredContent: structured, IsError: isError}
}

func appendCompactionArtifactFailure(response *protocol.Response, saveErr error) {
	if response == nil || saveErr == nil {
		return
	}
	structured, content, isError, ok := batchStructuredResult(response.Result)
	if !ok {
		return
	}
	content = append(content, mcpserver.Content{Type: "text", Text: "The full pre-compaction result could not be saved as a private artifact: " + truncateUTF8(saveErr.Error(), 160) + ". Only the compact response is available."})
	response.Result = mcpserver.StructuredResult{Content: content, StructuredContent: structured, IsError: isError}
}

// compactPlaybookToolResponse keeps the resume checkpoint usable when the
// ordered receipt history would exceed the MCP response budget. The session
// retains the authoritative receipt history and restores it from the token on
// resume; the response marks any omitted evidence explicitly.
func compactPlaybookToolResponse(response *protocol.Response) {
	if response == nil || response.Error != nil {
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) <= maxMCPBatchResponseBytes {
		return
	}
	structured, _, isError, ok := batchStructuredResult(response.Result)
	if !ok || structured == nil {
		return
	}
	data, err := json.Marshal(structured)
	if err != nil {
		return
	}
	var result agent.PlaybookResult
	if err := json.Unmarshal(data, &result); err != nil || len(result.StepReceipts) == 0 {
		return
	}
	result.StepReceiptsTruncated = true
	for i := range result.StepReceipts {
		receipt := &result.StepReceipts[i]
		receipt.ActionReceipt.Target = truncateUTF8(receipt.ActionReceipt.Target, 128)
		receipt.ActionReceipt.Evidence = truncateUTF8(receipt.ActionReceipt.Evidence, 160)
		receipt.ActionReceipt.RecoveryHint = truncateUTF8(receipt.ActionReceipt.RecoveryHint, 160)
		if recovery := receipt.RecoveryRecord; recovery != nil {
			recovery.ActionOutcome.Target = truncateUTF8(recovery.ActionOutcome.Target, 128)
			recovery.ActionOutcome.Evidence = truncateUTF8(recovery.ActionOutcome.Evidence, 160)
			recovery.ActionOutcome.RecoveryHint = truncateUTF8(recovery.ActionOutcome.RecoveryHint, 160)
		}
	}
	for {
		data, err = json.Marshal(result)
		if err != nil || json.Unmarshal(data, &structured) != nil {
			return
		}
		content := []mcpserver.Content{{Type: "text", Text: fmt.Sprintf("Playbook %s: %d/%d steps completed. Receipt evidence was compacted to fit the MCP response budget; step_receipts_truncated=true. The session-bound resume token remains valid.", map[bool]string{true: "completed", false: "stopped"}[result.Success], result.StepsRun, result.TotalSteps)}}
		response.Result = mcpserver.StructuredResult{Content: content, StructuredContent: structured, IsError: isError}
		encoded, err = json.Marshal(response)
		if err == nil && len(encoded) <= maxMCPBatchResponseBytes {
			return
		}
		if len(result.StepReceipts) == 0 {
			return
		}
		// Keep the most recent receipt, which carries the interrupted or final
		// step outcome, and discard the oldest entry until the response fits.
		result.StepReceipts = result.StepReceipts[1:]
	}
}

func requestToolName(params json.RawMessage) string {
	var input struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &input)
	return input.Name
}

// compactBatchToolResponse drops verbose, reconstructible batch details when
// the response exceeds its budget, preserving per-step outcomes and recovery
// records. Structured and text content are compacted together.
func compactBatchToolResponse(response *protocol.Response) {
	if response == nil {
		return
	}
	if response.Error != nil {
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return
	}
	structured, _, isError, ok := batchStructuredResult(response.Result)
	if !ok {
		return
	}
	if isError {
		var contentEnvelope struct {
			Content []mcpserver.Content `json:"content"`
		}
		contentJSON, _ := json.Marshal(response.Result)
		if json.Unmarshal(contentJSON, &contentEnvelope) == nil && len(contentEnvelope.Content) > 0 {
			text := contentEnvelope.Content[0].Text
			const prefix = "SCOUT_ERROR "
			if strings.HasPrefix(text, prefix) {
				var envelope MCPErrorEnvelope
				if json.Unmarshal([]byte(strings.TrimPrefix(text, prefix)), &envelope) == nil && len(encoded) > maxMCPBatchResponseBytes {
					partial, marshalErr := json.Marshal(envelope.PartialResult)
					var batch agent.BatchResult
					if marshalErr == nil && json.Unmarshal(partial, &batch) == nil && batch.Total > 0 {
						markBatchCompacted(&batch)
						envelope.PartialResult = batch
						envelope.Detail = truncateUTF8(envelope.Detail, 256)
						errorJSON, _ := json.Marshal(envelope)
						response.Result = mcpserver.StructuredResult{Content: []mcpserver.Content{{Type: "text", Text: prefix + string(errorJSON)}}, IsError: true}
					}
				}
			}
		}
		return
	}
	if len(encoded) <= maxMCPBatchResponseBytes {
		return
	}
	if structured == nil || structured["total"] == nil {
		return
	}
	resultJSON, err := json.Marshal(structured)
	if err != nil {
		return
	}
	var result agent.BatchResult
	if err := json.Unmarshal(resultJSON, &result); err != nil || result.Total == 0 {
		return
	}
	markBatchCompacted(&result)
	resultJSON, err = json.Marshal(result)
	if err != nil {
		return
	}
	if err := json.Unmarshal(resultJSON, &structured); err != nil {
		return
	}
	newContent := []mcpserver.Content{{Type: "text", Text: fmt.Sprintf("Batch completed: %d/%d succeeded, %d failed, %d verified, %d rejected, %d unverified, %d unknown. Verbose evidence was omitted; compact action receipts, per-step outcomes, and recovery records are preserved.", result.Succeeded, result.Total, result.Failed, result.Verified, result.Rejected, result.Unverified, result.Unknown)}}
	response.Result = mcpserver.StructuredResult{Content: newContent, StructuredContent: structured, IsError: isError}
}

// compactTaskRunToolResponse compacts the nested batch while preserving the
// task status, target binding, and resume cursor.
func compactTaskRunToolResponse(response *protocol.Response) {
	if response == nil || response.Error != nil {
		return
	}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) <= maxMCPBatchResponseBytes {
		return
	}
	structured, content, isError, ok := batchStructuredResult(response.Result)
	if !ok || (!isError && structured == nil) {
		return
	}
	compactTask := func(value any) (agent.TaskRunResult, bool) {
		var task agent.TaskRunResult
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil || json.Unmarshal(data, &task) != nil || task.Batch == nil || task.Batch.Total == 0 {
			return task, false
		}
		markBatchCompacted(task.Batch)
		return task, true
	}
	var task agent.TaskRunResult
	if isError {
		if len(content) == 0 || !strings.HasPrefix(content[0].Text, "SCOUT_ERROR ") {
			return
		}
		var envelope MCPErrorEnvelope
		if json.Unmarshal([]byte(strings.TrimPrefix(content[0].Text, "SCOUT_ERROR ")), &envelope) != nil {
			return
		}
		var compacted bool
		task, compacted = compactTask(envelope.PartialResult)
		if !compacted {
			return
		}
		envelope.PartialResult = task
		envelope.Detail = truncateUTF8(envelope.Detail, 256)
		data, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			return
		}
		response.Result = mcpserver.StructuredResult{Content: []mcpserver.Content{{Type: "text", Text: "SCOUT_ERROR " + string(data)}}, IsError: true}
		return
	}
	var compacted bool
	task, compacted = compactTask(structured)
	if !compacted {
		return
	}
	data, err := json.Marshal(task)
	if err != nil || json.Unmarshal(data, &structured) != nil {
		return
	}
	if len(content) > 0 {
		content[0].Text = fmt.Sprintf("Task %s %s: %d/%d actions completed; resume from action %d. Verbose evidence was omitted; compact action receipts, outcomes, and recovery records are preserved.", task.ID, task.Status, task.Batch.Succeeded, task.Batch.Total, task.ResumeFrom)
	}
	response.Result = mcpserver.StructuredResult{Content: content, StructuredContent: structured, IsError: isError}
}

func markBatchCompacted(result *agent.BatchResult) {
	compactBatchResult(result)
	result.Truncated = true
	result.TruncationReason = "verbose step evidence and partial results were omitted to fit the MCP response limit"
}

func compactBatchResult(result *agent.BatchResult) {
	if result == nil {
		return
	}
	for i := range result.Results {
		step := &result.Results[i]
		step.Target = ""
		step.Evidence = ""
		step.PartialResult = nil
		compactActionReceipt(step.Receipt)
		step.Error = truncateUTF8(step.Error, 256)
		step.RecoveryHint = truncateUTF8(step.RecoveryHint, 256)
		if step.RecoveryRecord != nil {
			step.RecoveryRecord.ActionOutcome.Evidence = ""
			step.RecoveryRecord.ActionOutcome.Target = truncateUTF8(step.RecoveryRecord.ActionOutcome.Target, 128)
			step.RecoveryRecord.ActionOutcome.RecoveryHint = truncateUTF8(step.RecoveryRecord.ActionOutcome.RecoveryHint, 256)
		}
	}
	if result.FinalVerification != nil {
		result.FinalVerification.URL = ""
		result.FinalVerification.Title = ""
		result.FinalVerification.Target = ""
		result.FinalVerification.Evidence = ""
		result.FinalVerification.RecoveryHint = truncateUTF8(result.FinalVerification.RecoveryHint, 256)
		compactActionReceipt(result.FinalVerification.Receipt)
	}
	result.Truncated = true
	result.TruncationReason = "verbose step evidence omitted to fit the MCP response limit"
}

// compactActionReceipt preserves the attribution and timing contract while
// dropping page-derived prose and the larger policy snapshot from bounded
// batch responses. Unknown outcomes retain their separate recovery record.
func compactActionReceipt(receipt *agent.ActionReceipt) {
	if receipt == nil {
		return
	}
	receipt.Target = ""
	receipt.Policy = nil
	receipt.Evidence = ""
	receipt.RecoveryHint = ""
}

func batchStructuredResult(value any) (map[string]any, []mcpserver.Content, bool, bool) {
	switch result := value.(type) {
	case mcpserver.StructuredResult:
		return result.StructuredContent, result.Content, result.IsError, true
	case *mcpserver.StructuredResult:
		if result != nil {
			return result.StructuredContent, result.Content, result.IsError, true
		}
	}
	return nil, nil, false, false
}

func mcpErr(err error) error {
	if err == nil {
		return nil
	}
	var policyErr *browse.URLPolicyError
	if errors.As(err, &policyErr) {
		env := MCPErrorEnvelope{
			Code: policyErr.Code, Message: "Request denied by browser URL policy.",
			TargetID: policyErr.TargetID, PolicyID: policyErr.PolicyID,
			PolicyEvidenceRef: policyErr.EvidenceReference,
			RecoveryHint:      "Review the active origin and private-address policy, then retry only if the destination is intended.",
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: "SCOUT_ERROR " + string(b)}
	}
	var taskConflict *agent.TaskRunConflictError
	if errors.As(err, &taskConflict) {
		env := MCPErrorEnvelope{
			Code:    "SCOUT_TASK_ID_CONFLICT",
			Message: taskConflict.Error(),
			Detail:  taskConflict.Reason,
			Hint:    "Reuse the ID only for an identical task plan; after reconciliation, decide whether to retry the uncertain action or continue, then use a new task ID for that chosen plan.",
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: "SCOUT_ERROR " + string(b)}
	}
	var taskCapacity *agent.TaskRunCapacityError
	if errors.As(err, &taskCapacity) {
		env := MCPErrorEnvelope{
			Code:    "SCOUT_TASK_CAPACITY",
			Message: taskCapacity.Error(),
			Detail:  fmt.Sprintf("limit=%d", taskCapacity.Limit),
			Hint:    "No task IDs were evicted. Start a fresh Scout session only after preserving any browser state you need, or use a durable task store.",
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: "SCOUT_ERROR " + string(b)}
	}
	var limitErr sessionLimitReachedError
	if errors.As(err, &limitErr) {
		env := MCPErrorEnvelope{
			Code:         "SCOUT_SESSION_LIMIT",
			Message:      fmt.Sprintf("The configured session limit of %d has been reached.", limitErr.limit),
			RecoveryHint: "Close an unused session or wait for an idle session to expire, then retry.",
			Hint:         "Close an unused session or wait for an idle session to expire, then retry.",
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: "SCOUT_ERROR " + string(b)}
	}
	var outcomeErr *agent.ActionOutcomeError
	if errors.As(err, &outcomeErr) {
		var recoveryRecord *agent.ActionRecoveryRecord
		if record, recordErr := agent.NewActionRecoveryRecord(outcomeErr); recordErr == nil {
			recoveryRecord = &record
		}
		env := MCPErrorEnvelope{
			Code:                  "SCOUT_ACTION_OUTCOME_UNKNOWN",
			Message:               "The action may have completed, but its resulting state could not be verified.",
			ContentTrust:          agent.ContentTrustUntrustedPage,
			Outcome:               string(outcomeErr.Outcome),
			Action:                outcomeErr.Action,
			Target:                outcomeErr.Target,
			TargetID:              outcomeErr.TargetID,
			FrameID:               outcomeErr.FrameID,
			TraceID:               outcomeErr.TraceID,
			TraceStep:             outcomeErr.TraceStep,
			TraceActionOrdinal:    outcomeErr.TraceActionOrdinal,
			RunID:                 outcomeErr.RunID,
			Step:                  outcomeErr.Step,
			OperationOrdinal:      outcomeErr.OperationOrdinal,
			DeadlineAt:            outcomeErr.DeadlineAt,
			ConfiguredTimeoutMS:   outcomeErr.ConfiguredTimeoutMS,
			SessionDead:           boolPointer(outcomeErr.SessionDead),
			ConsecutiveTimeouts:   intPointer(outcomeErr.ConsecutiveTimeouts),
			InFlightCommands:      intPointer(outcomeErr.InFlightCommands),
			URL:                   redactErrorURL(outcomeErr.URL),
			Evidence:              outcomeErr.Evidence,
			RecoveryHint:          outcomeErr.RecoveryHint,
			ExpectedPostcondition: outcomeErr.ExpectedPostcondition,
			ResponseEvidence:      outcomeErr.ResponseEvidence,
			ActionReceipt:         outcomeErr.Receipt,
			PartialResult:         outcomeErr.PartialResult,
			RecoveryRecord:        recoveryRecord,
			Detail:                fmt.Sprint(outcomeErr.Cause),
			Hint:                  outcomeErr.RecoveryHint,
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: fmt.Sprintf("SCOUT_ERROR %s", string(b))}
	}
	var dispatchErr *cdpclient.DispatchOutcomeUnknownError
	if errors.As(err, &dispatchErr) {
		env := MCPErrorEnvelope{
			Code:         "SCOUT_ACTION_OUTCOME_UNKNOWN",
			Message:      "The CDP command may have been applied, but its result was not confirmed.",
			ContentTrust: agent.ContentTrustUntrustedPage,
			Outcome:      string(agent.OutcomeUnknown),
			Action:       "cdp_command",
			Target:       dispatchErr.Method,
			Evidence:     "command may have been dispatched; no authoritative response was received",
			Detail:       dispatchErr.Error(),
			RecoveryHint: "Inspect the current page and session status before retrying.",
			Hint:         "Inspect the current page and session status before retrying.",
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: fmt.Sprintf("SCOUT_ERROR %s", string(b))}
	}
	var navigationErr *browse.NavigationOutcomeUnknownError
	if errors.As(err, &navigationErr) {
		env := MCPErrorEnvelope{
			Code:         "SCOUT_ACTION_OUTCOME_UNKNOWN",
			Message:      "Navigation may have completed, but its resulting page state could not be confirmed.",
			ContentTrust: agent.ContentTrustUntrustedPage,
			Outcome:      string(agent.OutcomeUnknown),
			Action:       "navigate",
			URL:          redactErrorURL(navigationErr.URL),
			Evidence:     "navigation may have been dispatched; no authoritative completion was received",
			Detail:       navigationErr.Error(),
			RecoveryHint: "Inspect the current page URL and session status before retrying; navigation may already have completed.",
			Hint:         "Inspect the current page URL and session status before retrying; navigation may already have completed.",
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: fmt.Sprintf("SCOUT_ERROR %s", string(b))}
	}
	var opErr *agent.OperationError
	if errors.As(err, &opErr) {
		env := MCPErrorEnvelope{
			Code:         "SCOUT_OPERATION_ERROR",
			Message:      opErr.OriginalError,
			ContentTrust: agent.ContentTrustUntrustedPage,
			TargetID:     opErr.TargetID,
			Phase:        opErr.Phase,
			Cause:        opErr.Cause,
			Hint:         opErr.RecoveryHint,
			StatusCode:   opErr.StatusCode,
			URL:          redactErrorURL(opErr.URL),
			Detail:       opErr.Detail,
		}
		switch opErr.Cause {
		case "timeout":
			if env.Hint == "" {
				env.Hint = "Try reset, then retry the action."
			}
		case "connection_refused", "connection_error":
			if env.Hint == "" {
				env.Hint = "Check browser/CDP availability and target URL reachability."
			}
		case "http_401", "http_403":
			if env.Hint == "" {
				env.Hint = "Authentication or authorization required."
			}
		case "http_404":
			if env.Hint == "" {
				env.Hint = "The target URL or resource was not found."
			}
		case "browser_closed":
			if env.Hint == "" {
				env.Hint = "Browser was closed; call reset and retry."
			}
		}
		env.RecoveryHint = env.Hint
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: fmt.Sprintf("SCOUT_ERROR %s", string(b))}
	}
	// Tool handlers use returned errors for browser and application failures.
	// Surface those as MCP tool-execution errors so callers can inspect and
	// recover, rather than reporting a JSON-RPC internal server error.
	return &mcpserver.ToolInputError{Message: err.Error()}
}

func mcpPolicyError(code, message, hint string) error {
	env := MCPErrorEnvelope{
		Code: code, Message: message, Detail: message,
		RecoveryHint: hint, Hint: hint,
	}
	b, _ := json.Marshal(env)
	return &mcpserver.ToolInputError{Message: "SCOUT_ERROR " + string(b)}
}

func mcpErrWithActionTimeout(err error, timeoutMS int) error {
	if timeoutMS > 0 {
		var outcome *agent.ActionOutcomeError
		if errors.As(err, &outcome) {
			outcome.ConfiguredTimeoutMS = int64(timeoutMS)
		}
	}
	return mcpErr(err)
}

func mcpErrWithPartialResult(err error, partial any, timeoutMS int) error {
	if err == nil || partial == nil {
		return mcpErr(err)
	}
	var stopped *agent.BatchStoppedError
	if errors.As(err, &stopped) {
		hint := "The batch stopped because a step was not positively verified. Inspect the partial result and current page before continuing."
		if stopped.Result != nil && stopped.Result.ResumeBlocked {
			hint = "Reconcile the uncertain step before retrying it or continuing with the next_action_index."
		}
		env := MCPErrorEnvelope{
			Code:          "SCOUT_BATCH_STOPPED",
			Message:       err.Error(),
			ContentTrust:  agent.ContentTrustUntrustedPage,
			Detail:        err.Error(),
			PartialResult: partial,
			RecoveryHint:  hint,
			Hint:          hint,
		}
		b, _ := json.Marshal(env)
		return &mcpserver.ToolInputError{Message: fmt.Sprintf("SCOUT_ERROR %s", string(b))}
	}
	var outcome *agent.ActionOutcomeError
	if errors.As(err, &outcome) {
		if timeoutMS > 0 {
			outcome.ConfiguredTimeoutMS = int64(timeoutMS)
		}
		outcome.PartialResult = partial
		return mcpErr(err)
	}
	env := MCPErrorEnvelope{
		Code:          "SCOUT_OPERATION_INTERRUPTED",
		Message:       "The operation stopped before all requested work completed; inspect partial_result before continuing.",
		ContentTrust:  agent.ContentTrustUntrustedPage,
		Detail:        err.Error(),
		PartialResult: partial,
		RecoveryHint:  "Review completed and uncertain step outcomes before retrying unfinished work.",
		Hint:          "Review completed and uncertain step outcomes before retrying unfinished work.",
	}
	b, _ := json.Marshal(env)
	return &mcpserver.ToolInputError{Message: fmt.Sprintf("SCOUT_ERROR %s", string(b))}
}

const scoutErrorMetaKey = "go.klarlabs.de/scout/error"

// attachScoutErrorMetadata makes Scout's documented JSON error envelope
// available to MCP hosts without requiring them to parse the text block. The
// text content remains unchanged for clients that only expose content.
func attachScoutErrorMetadata(response *protocol.Response) {
	if response == nil {
		return
	}
	result, ok := response.Result.(map[string]any)
	if !ok || result["isError"] != true {
		return
	}
	var blocks []any
	switch content := result["content"].(type) {
	case []map[string]any:
		blocks = make([]any, len(content))
		for i := range content {
			blocks[i] = content[i]
		}
	case []map[string]string:
		blocks = make([]any, len(content))
		for i := range content {
			block := make(map[string]any, len(content[i]))
			for key, value := range content[i] {
				block[key] = value
			}
			blocks[i] = block
		}
	case []any:
		blocks = content
	default:
		return
	}
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] != "text" {
			continue
		}
		message, ok := block["text"].(string)
		if !ok || !strings.HasPrefix(message, "SCOUT_ERROR ") {
			continue
		}
		var envelope MCPErrorEnvelope
		if err := json.Unmarshal([]byte(strings.TrimPrefix(message, "SCOUT_ERROR ")), &envelope); err != nil || envelope.Code == "" {
			return
		}
		metadata, _ := result["_meta"].(map[string]any)
		if metadata == nil {
			metadata = make(map[string]any)
		}
		metadata[scoutErrorMetaKey] = envelope
		result["_meta"] = metadata
		return
	}
}

// --- Tool input types ---

type NavigateInput struct {
	URL                     string `json:"url" jsonschema:"required,description=URL to navigate to"`
	TargetID                string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	TimeoutMS               int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. Expiry after navigation dispatch returns outcome_unknown; provide an expected_* condition to reconcile safely."`
	ExpectedText            string `json:"expected_text,omitempty" jsonschema:"description=Optional text expected after navigation"`
	ExpectedURLContains     string `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring expected after redirects"`
	ExpectedVisibleSelector string `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional selector that must be visible after navigation"`
	ExpectedValueSelector   string `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input or contenteditable selector whose value must match expected_value"`
	ExpectedValue           string `json:"expected_value,omitempty" jsonschema:"description=Exact value expected for expected_value_selector, including empty string"`
	ExpectedCountSelector   string `json:"expected_count_selector,omitempty" jsonschema:"description=Optional CSS selector whose match count must equal expected_count"`
	ExpectedCount           *int   `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector"`
}

func (i NavigateInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type ClickInput struct {
	Selector                string `json:"selector" jsonschema:"required,description=CSS selector of element to click"`
	TargetID                string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the click if Scout has switched to another page"`
	TimeoutMS               int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. Expiry after dispatch returns outcome_unknown so the caller can reconcile before retrying."`
	Wait                    bool   `json:"wait,omitempty" jsonschema:"description=If true wait for full page navigation after click"`
	ExpectedText            string `json:"expected_text,omitempty" jsonschema:"description=Optional text that must appear after the click; reports verified only when observed"`
	ExpectedURLContains     string `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring expected after the click"`
	ExpectedVisibleSelector string `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional CSS selector that must be visible after the click"`
	ExpectedValueSelector   string `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input or contenteditable selector whose value must match expected_value"`
	ExpectedValue           string `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector, including empty string"`
	ExpectedCountSelector   string `json:"expected_count_selector,omitempty" jsonschema:"Optional CSS selector whose match count must equal expected_count"`
	ExpectedCount           *int   `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector"`
}

func withMCPActionTimeout(ctx context.Context, timeoutMS int) (context.Context, context.CancelFunc, error) {
	if timeoutMS < 0 {
		return nil, nil, fmt.Errorf("timeout_ms must not be negative")
	}
	if int64(timeoutMS) > int64((24*time.Hour)/time.Millisecond) {
		return nil, nil, fmt.Errorf("timeout_ms must not exceed 86400000")
	}
	if timeoutMS == 0 {
		return ctx, func() {}, nil
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	return bounded, cancel, nil
}

type ReconcileActionInput struct {
	RecoveryRecord        *agent.ActionRecoveryRecord `json:"recovery_record,omitempty" jsonschema:"description=Optional versioned recovery_record returned by Scout; its original target and postcondition are verified without repeating the action"`
	TargetID              string                      `json:"target_id,omitempty" jsonschema:"description=target_id from the unknown-outcome error; prevents inspecting a different page"`
	Action                string                      `json:"action,omitempty" jsonschema:"description=Action name from the unknown-outcome error"`
	Target                string                      `json:"target,omitempty" jsonschema:"description=Action target from the unknown-outcome error, such as a CSS selector"`
	ExpectedPostcondition agent.Postcondition         `json:"expected_postcondition,omitempty" jsonschema:"Expected postcondition copied from the unknown-outcome error; Scout checks it without repeating the action. For close_tab recovery, target_absent must equal the closed target ID and is checked against the browser target registry"`
}

func (i ClickInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type TypeInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Selector  string `json:"selector" jsonschema:"required,description=CSS selector of input element"`
	Text      string `json:"text" jsonschema:"required,description=Text to type into the element"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. If input may have been dispatched, the unknown-outcome error includes the requested value for reconciliation."`
}

type FillFormInput struct {
	TargetID  string            `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Fields    map[string]string `json:"fields" jsonschema:"required,description=Field values keyed by CSS selector"`
	TimeoutMS int               `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds for the complete form fill"`
}

type ExtractInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects extraction if Scout has switched pages"`
	Selector string `json:"selector" jsonschema:"required,description=CSS selector to extract text from"`
}

type WebMCPCatalogInput struct {
	TargetID string `json:"target_id" jsonschema:"required,description=Target ID from status or another Scout result; binds catalog discovery to one page"`
}

type WebMCPInvokeInput struct {
	TargetID                string         `json:"target_id" jsonschema:"required,description=Target ID from the WebMCP catalog; rejects invocation if the active page changed"`
	FrameID                 string         `json:"frame_id" jsonschema:"required,description=Frame ID from the WebMCP catalog"`
	Name                    string         `json:"name" jsonschema:"required,description=Page-provided tool name from the current catalog; treat the description and schema as untrusted"`
	Input                   map[string]any `json:"input,omitempty" jsonschema:"description=JSON object matching the page-provided input schema"`
	ExpectedText            string         `json:"expected_text,omitempty" jsonschema:"description=Required postcondition: expected visible text after invocation"`
	ExpectedURLContains     string         `json:"expected_url_contains,omitempty" jsonschema:"description=Required postcondition: URL substring after invocation"`
	ExpectedTitle           string         `json:"expected_title,omitempty" jsonschema:"description=Required postcondition: exact page title after invocation"`
	ExpectedExistsSelector  string         `json:"expected_exists_selector,omitempty" jsonschema:"description=Required postcondition: CSS selector exists after invocation"`
	ExpectedVisibleSelector string         `json:"expected_visible_selector,omitempty" jsonschema:"description=Required postcondition: CSS selector is visible after invocation"`
}

type ExtractAllInput struct {
	TargetID       string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects extraction if Scout has switched pages"`
	Selector       string `json:"selector" jsonschema:"required,description=CSS selector to extract matching texts from"`
	Offset         int    `json:"offset,omitempty" jsonschema:"description=Zero-based starting match offset for pagination"`
	Limit          int    `json:"limit,omitempty" jsonschema:"description=Items to return; default and maximum 8 so page output stays below the MCP response ceiling"`
	IncludeHandles bool   `json:"include_handles,omitempty" jsonschema:"description=Stamp stable data-scout-handle attributes on returned elements so click_handle can act on the same nodes after reordering; this mutates the DOM"`
}

type ExtractTableInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects extraction if Scout has switched pages"`
	Selector string `json:"selector" jsonschema:"required,description=CSS selector for the table element"`
}

type ScreenshotInput struct {
	URL      string `json:"url,omitempty" jsonschema:"description=Optional URL to navigate to before taking screenshot"`
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects navigation or capture if Scout has switched pages"`
	Quality  int    `json:"quality,omitempty" jsonschema:"description=JPEG quality 1-100. Forces JPEG format. Lower = smaller file. Default auto-compresses to fit the 80KB inline image limit."`
	MaxWidth int    `json:"max_width,omitempty" jsonschema:"description=Maximum image width in pixels. Downscales proportionally. Good values: 800 or 1024."`
	FullPage bool   `json:"full_page,omitempty" jsonschema:"description=Capture the entire scrollable page instead of just the viewport."`
	// OutputPath / SaveToDisk turn the capture into a file the human can be
	// shown, rather than base64 that only ever reaches the model (#79).
	OutputPath string `json:"output_path,omitempty" jsonschema:"description=Write to a new image path and return it instead of base64. Implies save_to_disk. Relative paths resolve under temp; existing and protected paths are rejected."`
	SaveToDisk bool   `json:"save_to_disk,omitempty" jsonschema:"description=Write the image to a generated path and return that path instead of base64 - use when the capture is for the user to see. Disables the size cap, since it exists only to fit base64 into tool-result token limits."`
}

type CaptureResult struct {
	ContentTrust string                   `json:"content_trust,omitempty"`
	DataURL      string                   `json:"data_url,omitempty"`
	Artifact     *agent.ArtifactReference `json:"artifact,omitempty"`
	ImageInfo    *browse.ScreenshotInfo   `json:"image_info,omitempty"`
}

type SetViewportInput struct {
	TargetID          string  `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Width             int     `json:"width,omitempty" jsonschema:"description=Viewport width in CSS pixels. Required unless 'device' is given."`
	Height            int     `json:"height,omitempty" jsonschema:"description=Viewport height in CSS pixels. Required unless 'device' is given."`
	DeviceScaleFactor float64 `json:"device_scale_factor,omitempty" jsonschema:"description=Device pixel ratio (e.g. 2 or 3 for retina/phones). Default 1."`
	Mobile            bool    `json:"mobile,omitempty" jsonschema:"description=Enable mobile emulation (touch + meta-viewport handling). Default false."`
	Device            string  `json:"device,omitempty" jsonschema:"description=Optional preset that fills the other fields: iphone-se, iphone-14, pixel-7, ipad-mini, desktop. Explicit width/height/device_scale_factor/mobile override the preset."`
	TimeoutMS         int     `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds for applying and verifying metrics"`
}

type viewportPreset struct {
	w, h   int
	scale  float64
	mobile bool
}

// viewportPresets are common device metrics for set_viewport's `device` arg.
var viewportPresets = map[string]viewportPreset{
	"iphone-se": {375, 667, 2, true},
	"iphone-14": {390, 844, 3, true},
	"pixel-7":   {412, 915, 2.625, true},
	"ipad-mini": {768, 1024, 2, true},
	"desktop":   {1280, 800, 1, false},
}

// resolveViewport turns a SetViewportInput (explicit dims and/or a device
// preset) into concrete device metrics. Explicit fields override the preset.
func resolveViewport(in SetViewportInput) (w, h int, scale float64, mobile bool, err error) {
	w, h, scale, mobile = in.Width, in.Height, in.DeviceScaleFactor, in.Mobile
	if in.Device != "" {
		preset, ok := viewportPresets[strings.ToLower(strings.TrimSpace(in.Device))]
		if !ok {
			return 0, 0, 0, false, fmt.Errorf("unknown device preset %q (have: iphone-se, iphone-14, pixel-7, ipad-mini, desktop)", in.Device)
		}
		if w == 0 {
			w = preset.w
		}
		if h == 0 {
			h = preset.h
		}
		if scale == 0 {
			scale = preset.scale
		}
		if !in.Mobile {
			mobile = preset.mobile
		}
	}
	if w <= 0 || h <= 0 {
		return 0, 0, 0, false, fmt.Errorf("set_viewport: width and height must be positive (or pass a 'device' preset)")
	}
	if scale == 0 {
		scale = 1
	}
	return w, h, scale, mobile, nil
}

type HasElementInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the check if Scout has switched pages"`
	Selector string `json:"selector" jsonschema:"required,description=CSS selector to check for"`
}

type WaitForInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the wait if Scout has switched pages"`
	Selector string `json:"selector" jsonschema:"required,description=CSS selector to wait for"`
}

type ObserveInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the operation if Scout has switched pages"`
	URL      string `json:"url,omitempty" jsonschema:"description=Optional URL to navigate to before observing"`
}

type SavePageSnapshotInput struct {
	URL        string `json:"url,omitempty" jsonschema:"description=Optional URL to navigate to before capturing"`
	TargetID   string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects navigation or capture if Scout has switched pages"`
	Format     string `json:"format,omitempty" jsonschema:"description=markdown, readable_text, or accessibility_tree; defaults to markdown"`
	MaxChars   int    `json:"max_chars,omitempty" jsonschema:"description=Maximum characters to save; default 200000 and maximum 2000000"`
	OutputPath string `json:"output_path,omitempty" jsonschema:"description=Optional new output path; relative paths resolve under temp and protected paths are rejected"`
}

type ReadPageSnapshotInput struct {
	Path   string `json:"path" jsonschema:"required,description=Registered artifact path returned by save_page_snapshot or an oversized MCP result"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=Zero-based character offset"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Characters to return; default 8000 and maximum 20000"`
}

type SearchPageSnapshotInput struct {
	Path         string `json:"path" jsonschema:"required,description=Registered artifact path returned by save_page_snapshot or an oversized MCP result"`
	Query        string `json:"query" jsonschema:"required,description=Literal text to find, case-insensitive"`
	ContextChars int    `json:"context_chars,omitempty" jsonschema:"description=Characters of context around each match; default 120, maximum 500"`
}

type DeletePageSnapshotInput struct {
	Path string `json:"path" jsonschema:"required,description=Registered artifact path returned by save_page_snapshot or an oversized MCP result"`
}

type SnapshotMatch struct {
	Offset       int    `json:"offset"`
	Text         string `json:"text"`
	ContentTrust string `json:"content_trust"`
}

type SearchPageSnapshotResult struct {
	Path         string          `json:"path"`
	ContentTrust string          `json:"content_trust"`
	Query        string          `json:"query"`
	Matches      []SnapshotMatch `json:"matches"`
	TotalMatches int             `json:"total_matches"`
	TotalChars   int             `json:"total_chars"`
	Truncated    bool            `json:"truncated"`
}

type PageSnapshotResult struct {
	Artifact     *agent.ArtifactReference `json:"artifact"`
	ContentTrust string                   `json:"content_trust"`
	Format       string                   `json:"format"`
	TotalChars   int                      `json:"total_chars"`
	SavedChars   int                      `json:"saved_chars"`
	Truncated    bool                     `json:"truncated"`
}

type PageSnapshotExcerpt struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	ContentTrust string `json:"content_trust"`
	Offset       int    `json:"offset"`
	Length       int    `json:"length"`
	Total        int    `json:"total_chars"`
	Truncated    bool   `json:"truncated"`
}

type ObserveWithBudgetInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the observation if Scout has switched pages"`
	Budget   int    `json:"budget" jsonschema:"required,description=Approximate token budget for the response"`
}

type ObserveScopedInput struct {
	TargetID     string   `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the observation if Scout has switched pages"`
	Sections     []string `json:"sections,omitempty" jsonschema:"description=Landmark roles to limit the observation to: nav, main, header, footer, aside, article, search. Or pass raw CSS selectors (#sidebar, .product-grid). Empty = whole page."`
	AllowEmpty   bool     `json:"allow_empty,omitempty" jsonschema:"description=Return an empty observation when none of the requested scopes match. Default false."`
	LimitChars   int      `json:"limit_chars,omitempty" jsonschema:"description=Maximum length of the returned text blob."`
	LinksLimit   int      `json:"links_limit,omitempty" jsonschema:"description=Cap on returned links (0 = session default)."`
	InputsLimit  int      `json:"inputs_limit,omitempty" jsonschema:"description=Cap on returned inputs (0 = session default)."`
	ButtonsLimit int      `json:"buttons_limit,omitempty" jsonschema:"description=Cap on returned buttons (0 = session default)."`
}

type AutoExtractInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects extraction if Scout has switched pages"`
	Offset   int    `json:"offset,omitempty" jsonschema:"description=Zero-based starting item offset (default 0). Use offset plus limit to retrieve the next page."`
	Limit    int    `json:"limit,omitempty" jsonschema:"description=Maximum items in this page (default session limit; maximum 100)."`
}

type PDFInput struct {
	TargetID   string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects navigation or PDF capture if Scout has switched pages"`
	URL        string `json:"url,omitempty" jsonschema:"description=Optional URL to navigate to before generating PDF"`
	OutputPath string `json:"output_path,omitempty" jsonschema:"description=Write to a new PDF path and return it instead of base64. Relative paths resolve under temp; existing and protected paths are rejected."`
	SaveToDisk bool   `json:"save_to_disk,omitempty" jsonschema:"description=Write the PDF to a generated path and return the path instead of base64, avoiding a large MCP response."`
}

type DiscoverFormInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects inspection if Scout has switched pages"`
	Selector string `json:"selector,omitempty" jsonschema:"description=CSS selector for specific form (empty = all forms)"`
}

type WaitForSPAIdleInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the wait if Scout has switched pages"`
	QuietMS   int    `json:"quiet_ms,omitempty" jsonschema:"description=Required mutation-free quiet window in ms. Default 400."`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Hard cap in ms. Default 15000."`
}

type FillFormSemanticInput struct {
	TargetID  string         `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Fields    map[string]any `json:"fields" jsonschema:"required,description=Field values keyed by human-readable field name. Strings fill text inputs/textareas/selects. Booleans toggle checkboxes (or pick a radio when paired with a string label)."`
	TimeoutMS int            `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds for the complete semantic form fill"`
}

type EnableNetworkInput struct {
	TargetID string   `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the operation if Scout has switched pages"`
	Patterns []string `json:"patterns,omitempty" jsonschema:"description=URL substring patterns to capture (empty = all)"`
}

type NetworkRequestsInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from earlier page evidence; rejects the query if Scout has switched pages since capture was enabled"`
	Pattern   string `json:"pattern,omitempty" jsonschema:"description=URL substring filter"`
	Offset    int    `json:"offset,omitempty" jsonschema:"description=Zero-based offset into matching requests in the retained window, ordered oldest to newest"`
	MaxRecent int    `json:"max_recent,omitempty" jsonschema:"description=Maximum number of requests in this page (default 3; maximum 3)"`
}

type WaitForNavigationInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the wait if Scout has switched pages"`
	Mode      string `json:"mode,omitempty" jsonschema:"description=One of full|spa|any. full = real document load. spa = History API push/replace/popstate. any = first to fire. Default any."`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"description=Maximum wait in milliseconds. Default 5000."`
}

type AriaViolationsInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the scan if Scout has switched pages"`
}

type NetworkRequestsResult struct {
	ContentTrust   string                 `json:"content_trust,omitempty"`
	Requests       []agent.NetworkCapture `json:"requests"`
	Count          int                    `json:"count"`
	Total          int                    `json:"total"`
	DroppedTotal   int                    `json:"dropped_total,omitempty"`
	Offset         int                    `json:"offset"`
	Limit          int                    `json:"limit"`
	Truncated      bool                   `json:"truncated"`
	CaptureEnabled bool                   `json:"capture_enabled"`
	Hint           string                 `json:"hint,omitempty"`
}

// FailedRequestsResult wraps the failed_requests list in a JSON object so the
// tool can advertise a fixed output schema (structuredContent must be an object,
// not a bare array). Each element is a fixed-shape agent.NetworkFailure.
type FailedRequestsResult struct {
	ContentTrust           string                 `json:"content_trust,omitempty"`
	Items                  []agent.NetworkFailure `json:"items"`
	Count                  int                    `json:"count"`
	Total                  int                    `json:"total"`
	Truncated              bool                   `json:"truncated"`
	NetworkCaptureMode     string                 `json:"network_capture_mode"`
	NetworkCaptureCoverage string                 `json:"network_capture_coverage"`
	NetworkCaptureScope    string                 `json:"network_capture_scope"`
}

// ListTabsResult wraps the list_tabs array in a JSON object envelope so the tool
// can advertise a fixed output schema. Each element is a fixed-shape agent.TabInfo.
type ListTabsResult struct {
	ContentTrust string          `json:"content_trust,omitempty"`
	Items        []agent.TabInfo `json:"items"`
	Count        int             `json:"count"`
	Total        int             `json:"total"`
	Offset       int             `json:"offset"`
	Limit        int             `json:"limit"`
	Truncated    bool            `json:"truncated"`
}

type ListTabsInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier result; rejects the listing if Scout has switched active pages"`
	Offset   int    `json:"offset,omitempty" jsonschema:"description=Zero-based tab offset for pagination (default 0)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"description=Maximum tabs to return (default 50, max 50)"`
}

// CookiesListResult wraps the cookies_list array in a JSON object envelope so the
// tool can advertise a fixed output schema. Each element is a fixed-shape
// agent.CookieInfo (values redacted).
type CookiesListResult struct {
	ContentTrust string             `json:"content_trust,omitempty"`
	Items        []agent.CookieInfo `json:"items"`
	Count        int                `json:"count"`
	Total        int                `json:"total"`
	Truncated    bool               `json:"truncated"`
}

type DiscoverFormResult struct {
	ContentTrust string                `json:"content_trust,omitempty"`
	FormSelector string                `json:"form_selector"`
	Action       string                `json:"action,omitempty"`
	Method       string                `json:"method,omitempty"`
	Fields       []agent.FormFieldInfo `json:"fields"`
	Count        int                   `json:"count"`
	Total        int                   `json:"total"`
	Truncated    bool                  `json:"truncated"`
}

type NetworkSummaryInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from earlier page evidence; rejects the query if Scout has switched pages since capture was enabled"`
	Pattern  string `json:"pattern,omitempty" jsonschema:"description=Optional URL substring filter."`
}

type AnnotatedScreenshotInput struct {
	TargetID     string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects capture if Scout has switched pages"`
	IncludeImage bool   `json:"include_image,omitempty" jsonschema:"description=Include base64 image data in response. Default false to avoid large responses. Use screenshot tool separately if you need the image."`
	// Same purpose as on ScreenshotInput: hand the human a file (#79).
	OutputPath string `json:"output_path,omitempty" jsonschema:"description=Write the labelled image here and return the path. Implies include_image and save_to_disk."`
	SaveToDisk bool   `json:"save_to_disk,omitempty" jsonschema:"description=Write the labelled image to a generated path and return that path instead of base64. Implies include_image."`
}

type AnnotatedScreenshotResult struct {
	ContentTrust string                   `json:"content_trust,omitempty"`
	Image        string                   `json:"image,omitempty"`
	ImageInfo    *browse.ScreenshotInfo   `json:"image_info,omitempty"`
	TargetID     string                   `json:"target_id,omitempty"`
	FrameID      string                   `json:"frame_id,omitempty"`
	Elements     []agent.AnnotatedElement `json:"elements"`
	Count        int                      `json:"count"`
	Total        int                      `json:"total"`
	Truncated    bool                     `json:"truncated"`
	LabelScope   string                   `json:"label_scope"`
	// ImagePath is set instead of Image when the capture was written to disk.
	ImagePath string                   `json:"image_path,omitempty"`
	Artifact  *agent.ArtifactReference `json:"artifact,omitempty"`
}

type ClickLabelInput struct {
	TargetID                string      `json:"target_id,omitempty" jsonschema:"description=target_id from annotated_screenshot; rejects the click if another page is active"`
	Label                   json.Number `json:"label" jsonschema:"required,description=Label number from annotated screenshot (e.g. 8)"`
	TimeoutMS               int         `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. If the click may have dispatched, Scout returns outcome_unknown for safe reconciliation."`
	ExpectedText            string      `json:"expected_text,omitempty" jsonschema:"description=Optional visible text postcondition after click"`
	ExpectedURLContains     string      `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring postcondition after click"`
	ExpectedVisibleSelector string      `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional selector that must be visible after click"`
	ExpectedValueSelector   string      `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input or contenteditable selector whose value must match expected_value"`
	ExpectedValue           string      `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector, including empty string"`
	ExpectedCountSelector   string      `json:"expected_count_selector,omitempty" jsonschema:"Optional CSS selector whose match count must equal expected_count"`
	ExpectedCount           *int        `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector"`
}

func (i ClickLabelInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type ClickHandleInput struct {
	Handle                  string `json:"handle" jsonschema:"required,description=Stable node handle from annotated_screenshot or auto_extract. Survives reordering while the same DOM node remains attached; returns stale_handle if detached."`
	TargetID                string `json:"target_id,omitempty" jsonschema:"CDP target_id returned by annotated_screenshot or auto_extract. When supplied, the click is rejected if another tab is active."`
	FrameID                 string `json:"frame_id,omitempty" jsonschema:"Frame ID returned by auto_extract. Supply together with target_id; empty selects the main frame."`
	TimeoutMS               int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. If the click may have dispatched, Scout returns outcome_unknown for safe reconciliation."`
	ExpectedText            string `json:"expected_text,omitempty" jsonschema:"description=Optional visible text postcondition after click"`
	ExpectedURLContains     string `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring postcondition after click"`
	ExpectedVisibleSelector string `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional selector that must be visible after click"`
	ExpectedValueSelector   string `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input or contenteditable selector whose value must match expected_value"`
	ExpectedValue           string `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector, including empty string"`
	ExpectedCountSelector   string `json:"expected_count_selector,omitempty" jsonschema:"Optional CSS selector whose match count must equal expected_count"`
	ExpectedCount           *int   `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector"`
}

func (i ClickHandleInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type ClickTextInput struct {
	TargetID                string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the click if Scout has switched pages"`
	Text                    string `json:"text" jsonschema:"required,description=Visible text to match (exact, case-insensitive)."`
	Role                    string `json:"role,omitempty" jsonschema:"description=Optional role filter: button or link."`
	TimeoutMS               int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. If the click may have dispatched, Scout returns outcome_unknown for safe reconciliation."`
	ExpectedText            string `json:"expected_text,omitempty" jsonschema:"description=Optional visible text postcondition after click"`
	ExpectedURLContains     string `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring postcondition after click"`
	ExpectedVisibleSelector string `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional selector that must be visible after click"`
	ExpectedValueSelector   string `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input or contenteditable selector whose value must match expected_value"`
	ExpectedValue           string `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector, including empty string"`
	ExpectedCountSelector   string `json:"expected_count_selector,omitempty" jsonschema:"Optional CSS selector whose match count must equal expected_count"`
	ExpectedCount           *int   `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector"`
}

func (i ClickTextInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type ComponentStateInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects inspection if Scout has switched pages"`
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of the component root element"`
}

type DispatchEventInput struct {
	TargetID                string         `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Selector                string         `json:"selector" jsonschema:"required,description=CSS selector of the target element"`
	EventType               string         `json:"event_type" jsonschema:"required,description=DOM event type (e.g. click, input, custom-event)"`
	Detail                  map[string]any `json:"detail,omitempty" jsonschema:"description=Event detail/payload data"`
	TimeoutMS               int            `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. If dispatch may have occurred, Scout returns outcome_unknown for safe reconciliation."`
	ExpectedText            string         `json:"expected_text,omitempty" jsonschema:"description=Optional visible text postcondition after dispatch"`
	ExpectedURLContains     string         `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring postcondition after dispatch"`
	ExpectedVisibleSelector string         `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional selector visibility postcondition after dispatch"`
	ExpectedValueSelector   string         `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input or contenteditable selector whose value must match expected_value"`
	ExpectedValue           string         `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector, including empty string"`
	ExpectedCountSelector   string         `json:"expected_count_selector,omitempty" jsonschema:"Optional CSS selector whose match count must equal expected_count"`
	ExpectedCount           *int           `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector"`
}

func (i DispatchEventInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type ConfigureInput struct {
	Headless        *bool `json:"headless,omitempty" jsonschema:"description=Run browser in headless mode (no visible window). Omit to keep the current setting (default true); a missing value no longer silently switches to a visible window."`
	AllowPrivateIPs *bool `json:"allow_private_ips,omitempty" jsonschema:"description=Allow navigation to loopback (127.0.0.1, localhost) and private IPs (10.*, 192.168.*, etc). Required for local-dev workflows. Omit to keep the current setting (default false)."`
	Fresh           bool  `json:"fresh,omitempty" jsonschema:"description=Kill the current page and browser and start a fresh session even if headless/allow_private_ips didn't change. Use after observing session_dead=true in status or when accumulated SPA state needs to be cleared."`
}

type ProfilePathInput struct {
	Path     string `json:"path" jsonschema:"required,description=Local JSON file path for the browser profile. Profile files contain authentication cookies and local storage; keep them private."`
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from earlier page evidence; rejects profile saving if Scout has switched pages"`
}

type LoadProfileInput struct {
	Path      string `json:"path" jsonschema:"required,description=Local JSON file path for the browser profile. Profile files contain authentication cookies and local storage; keep them private."`
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects profile loading if Scout has switched pages"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds for navigation, restore, and reload"`
}

type SessionNameInput struct {
	Name string `json:"name" jsonschema:"required,description=Session name. Use letters, numbers, hyphen, underscore, or period (1-64 characters)."`
}

type CreateSessionInput struct {
	Name           string   `json:"name" jsonschema:"required,description=Session name. Use letters, numbers, hyphen, underscore, or period (1-64 characters)."`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"description=Optional exact HTTP(S) origin allowlist for this session. Must be a subset of the server or principal allowlist; cannot broaden it."`
}

type SessionRecoveryInput struct {
	Handle           string `json:"handle,omitempty" jsonschema:"description=Opaque handle returned by persist_session; required for resume_session and revoke_session."`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty" jsonschema:"description=Optional handle lifetime from 60 seconds to 30 days; defaults to 24 hours."`
}

type SessionRecoveryResult struct {
	Handle      string    `json:"handle,omitempty"`
	SessionName string    `json:"session_name"`
	TargetID    string    `json:"target_id,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	Resumed     bool      `json:"resumed,omitempty"`
	Revoked     bool      `json:"revoked,omitempty"`
}

type CloseSessionResult struct {
	Action            string `json:"action"`
	Outcome           string `json:"outcome"`
	Verified          bool   `json:"verified"`
	RegistryRemoved   bool   `json:"registry_removed"`
	CleanupCompleted  bool   `json:"cleanup_completed"`
	CleanupError      string `json:"cleanup_error,omitempty"`
	ClosedSession     string `json:"closed_session"`
	ActiveSession     string `json:"active_session"`
	RemainingSessions int    `json:"remaining_sessions"`
	Evidence          string `json:"evidence"`
}

type SessionSummary struct {
	ContentTrust           string     `json:"content_trust,omitempty"`
	Name                   string     `json:"name"`
	SessionID              string     `json:"session_id"`
	OwnerScope             string     `json:"owner_scope"`
	Active                 bool       `json:"active"`
	Started                bool       `json:"started"`
	IdleTimeoutMS          int64      `json:"idle_timeout_ms,omitempty"`
	LastActivity           *time.Time `json:"last_activity,omitempty"`
	IdleDeadline           *time.Time `json:"idle_deadline,omitempty"`
	BrowserAlive           bool       `json:"browser_alive"`
	BrowserOwnership       string     `json:"browser_ownership,omitempty"`
	BrowserContextIsolated bool       `json:"browser_context_isolated,omitempty"`
	BrowserEndpoint        string     `json:"browser_endpoint,omitempty"`
	EndpointSource         string     `json:"endpoint_source,omitempty"`
	ProxyConfigured        bool       `json:"proxy_configured"`
	ProxyAppliedByScout    bool       `json:"proxy_applied_by_scout"`
	RemoteHostPolicy       string     `json:"remote_host_policy"`
	SessionDead            bool       `json:"session_dead,omitempty"`
	Recoverable            bool       `json:"recoverable"`
	CurrentURL             string     `json:"current_url,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	SessionLimit           int        `json:"session_limit,omitempty"`
	SessionCount           int        `json:"session_count,omitempty"`
	SessionStarting        int        `json:"session_starting,omitempty"`
	SessionClosing         int        `json:"session_closing,omitempty"`
	InFlightCalls          int        `json:"in_flight_calls"`
	StatusDeferred         bool       `json:"status_deferred,omitempty"`
	CleanupPending         bool       `json:"cleanup_pending,omitempty"`
	CleanupError           string     `json:"cleanup_error,omitempty"`
}

// safeEndpointLabel exposes enough connection identity to distinguish remote
// browser endpoints without returning credentials, paths, query tokens, or
// fragments from a DevTools URL.
func safeEndpointLabel(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return "configured"
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ws", "wss":
		return strings.ToLower(parsed.Scheme) + "://" + parsed.Host
	default:
		return "configured"
	}
}

func validateSessionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) == 0 || len(name) > 64 {
		return "", fmt.Errorf("session name must be 1-64 characters")
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return "", fmt.Errorf("invalid session name %q: use letters, numbers, hyphen, underscore, or period", name)
		}
	}
	return name, nil
}

func boundedRecentRequests(requests []agent.NetworkCapture, offset, requestedLimit int) ([]agent.NetworkCapture, int, int, int, bool) {
	total := len(requests)
	if offset < 0 {
		offset = 0
	}
	limit := requestedLimit
	if limit <= 0 {
		limit = 3
	}
	if limit > 3 {
		limit = 3
	}
	if offset > total {
		offset = total
	}
	end := min(total, offset+limit)
	page := append([]agent.NetworkCapture(nil), requests[offset:end]...)
	return page, total, offset, limit, end < total
}

func captureImageFormat(data []byte) (mime, ext string) {
	if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}) {
		return "image/jpeg", "jpg"
	}
	return "image/png", "png"
}

func truncateText(text string, maxRunes int) (string, int, bool) {
	runes := []rune(text)
	total := len(runes)
	if total <= maxRunes {
		return text, total, false
	}
	return string(runes[:maxRunes]), total, true
}

type ResetInput struct {
	TimeoutMS int `json:"timeout_ms,omitempty" jsonschema:"description=Optional deadline in milliseconds for closing and relaunching the browser session"`
}

type StatusInput struct{}

type HoverInput struct {
	TargetID                string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Selector                string `json:"selector" jsonschema:"required,description=CSS selector of element to hover over"`
	TimeoutMS               int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds; interruption after pointer dispatch returns outcome_unknown."`
	ExpectedText            string `json:"expected_text,omitempty" jsonschema:"Optional text expected after hover"`
	ExpectedURLContains     string `json:"expected_url_contains,omitempty" jsonschema:"Optional URL substring expected after hover"`
	ExpectedVisibleSelector string `json:"expected_visible_selector,omitempty" jsonschema:"Optional CSS selector that must be visible after hover"`
	ExpectedValueSelector   string `json:"expected_value_selector,omitempty" jsonschema:"Optional input selector whose value must match expected_value"`
	ExpectedValue           string `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector"`
	ExpectedCountSelector   string `json:"expected_count_selector,omitempty" jsonschema:"Optional CSS selector whose count must equal expected_count"`
	ExpectedCount           *int   `json:"expected_count,omitempty" jsonschema:"Exact non-negative selector count expected after hover"`
}

type PressKeyInput struct {
	Key                     string `json:"key" jsonschema:"required,description=Key name or chord such as Enter, ArrowDown, or Control+A; sends input to the currently focused element"`
	TargetID                string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	TimeoutMS               int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds; interruption after dispatch returns outcome_unknown"`
	ExpectedText            string `json:"expected_text,omitempty" jsonschema:"Optional text expected after the key press"`
	ExpectedURLContains     string `json:"expected_url_contains,omitempty" jsonschema:"Optional URL substring expected after the key press"`
	ExpectedTitle           string `json:"expected_title,omitempty" jsonschema:"Optional exact page title expected after the key press"`
	ExpectedVisibleSelector string `json:"expected_visible_selector,omitempty" jsonschema:"Optional selector that must be visible after the key press"`
	ExpectedValueSelector   string `json:"expected_value_selector,omitempty" jsonschema:"Optional selector whose value must match expected_value after the key press"`
	ExpectedValue           string `json:"expected_value,omitempty" jsonschema:"Exact expected value for expected_value_selector"`
	ExpectedCountSelector   string `json:"expected_count_selector,omitempty" jsonschema:"Optional selector whose count must equal expected_count after the key press"`
	ExpectedCount           *int   `json:"expected_count,omitempty" jsonschema:"Exact expected count for expected_count_selector"`
}

func (i PressKeyInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, Title: i.ExpectedTitle, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type KeyControlInput struct {
	Key       string `json:"key" jsonschema:"required,description=Single key to hold or release; modifier keys are supported"`
	TargetID  string `json:"target_id,omitempty" jsonschema:"Optional target_id from an earlier Scout result"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"Optional per-action deadline in milliseconds"`
}

func (i HoverInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type SelectOptionInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Selector  string `json:"selector" jsonschema:"required,description=CSS selector of the select element"`
	Option    string `json:"option" jsonschema:"required,description=Option text or value to select"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds; interruption after selection returns outcome_unknown."`
}

type ScrollToInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Selector  string `json:"selector" jsonschema:"required,description=CSS selector of element to scroll into view"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds."`
}

type ScrollByInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	X         int    `json:"x" jsonschema:"description=Horizontal scroll offset in pixels"`
	Y         int    `json:"y" jsonschema:"required,description=Vertical scroll offset in pixels (positive=down)"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds."`
}

type DragDropInput struct {
	TargetID                string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	From                    string `json:"from" jsonschema:"required,description=CSS selector of element to drag"`
	To                      string `json:"to" jsonschema:"required,description=CSS selector of drop target"`
	TimeoutMS               int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. If drag events may have dispatched, Scout returns outcome_unknown for safe reconciliation."`
	ExpectedText            string `json:"expected_text,omitempty" jsonschema:"description=Optional visible text postcondition after drop"`
	ExpectedURLContains     string `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring postcondition after drop"`
	ExpectedVisibleSelector string `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional selector visibility postcondition after drop"`
	ExpectedValueSelector   string `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input or contenteditable selector whose value must match expected_value"`
	ExpectedValue           string `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector, including empty string"`
	ExpectedCountSelector   string `json:"expected_count_selector,omitempty" jsonschema:"Optional CSS selector whose match count must equal expected_count"`
	ExpectedCount           *int   `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector"`
}

func (i DragDropInput) Postcondition() agent.Postcondition {
	return agent.Postcondition{Text: i.ExpectedText, URLContains: i.ExpectedURLContains, VisibleSelector: i.ExpectedVisibleSelector, ValueSelector: i.ExpectedValueSelector, Value: i.ExpectedValue, CountSelector: i.ExpectedCountSelector, Count: i.ExpectedCount}
}

type FocusInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Selector  string `json:"selector" jsonschema:"required,description=CSS selector of element to focus"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds; interruption after focus dispatch returns outcome_unknown."`
}

type StartRecordInput struct {
	Name string `json:"name" jsonschema:"required,description=Name for the playbook being recorded"`
}

type ExpectRecordedActionInput struct {
	URL      string `json:"url,omitempty" jsonschema:"description=Expected URL after the most recently recorded action"`
	Title    string `json:"title,omitempty" jsonschema:"description=Expected page title after the most recently recorded action"`
	Selector string `json:"selector,omitempty" jsonschema:"description=CSS selector that should exist after the most recently recorded action"`
	Text     string `json:"text,omitempty" jsonschema:"description=Text that should be visible after the most recently recorded action"`
}

type SavePlaybookInput struct {
	Path string `json:"path" jsonschema:"required,description=New file path for the playbook JSON; relative paths resolve under the OS temp directory and existing or protected paths are rejected"`
}

type ReplayInput struct {
	Path string `json:"path" jsonschema:"required,description=File path to the playbook JSON file"`
}

type ResumePlaybookInput struct {
	Path   string               `json:"path" jsonschema:"required,description=File path to the playbook JSON file"`
	Result agent.PlaybookResult `json:"previous_result" jsonschema:"required,description=Unmodified failed result from replay_playbook including its one-use resume_token; valid only in the same Scout session"`
}

type StartScreenRecordingInput struct {
	TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects recording if Scout has switched pages"`
	Width     int    `json:"width,omitempty" jsonschema:"description=Capture width in CSS pixels (default 1280)"`
	Height    int    `json:"height,omitempty" jsonschema:"description=Capture height in CSS pixels (default 800)"`
	FPS       int    `json:"fps,omitempty" jsonschema:"description=Target frames per second 1-60 (default 30)"`
	Quality   int    `json:"quality,omitempty" jsonschema:"description=JPEG frame quality 1-100 (default 80)"`
	Format    string `json:"format,omitempty" jsonschema:"description=Output format: webm (default if ffmpeg present), mp4, or frames"`
	OutputDir string `json:"output_dir,omitempty" jsonschema:"description=Parent directory for output. Defaults to OS temp dir; relative paths resolve under temp and protected locations are rejected."`
}

type StopScreenRecordingInput struct{}

type TabInput struct {
	TargetID    string `json:"target_id,omitempty" jsonschema:"description=Optional current active target_id; rejects the operation if Scout has switched pages before applying it"`
	TabTargetID string `json:"tab_target_id,omitempty" jsonschema:"description=Optional target_id from list_tabs; for close_tab, rejects closing the named tab if it now refers to another browser target"`
	Name        string `json:"name,omitempty" jsonschema:"description=Tab name (for open_tab and switch_tab)"`
	URL         string `json:"url,omitempty" jsonschema:"description=URL to open in new tab"`
	TimeoutMS   int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds for opening, switching, or closing a tab"`
}

type HistoryInput struct {
	Count int `json:"count,omitempty" jsonschema:"description=Number of recent actions to return (default 5, max 20)"`
}

type SuggestInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects inspection if Scout has switched pages"`
	Selector string `json:"selector" jsonschema:"required,description=The selector that failed to match"`
}

type SelectByPromptInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects inspection if Scout has switched pages"`
	Prompt   string `json:"prompt" jsonschema:"required,description=Natural language description of the element to find (e.g. 'the login button' or 'search input')"`
}

type SwitchToFrameInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the operation if Scout has switched pages"`
	Selector string `json:"selector" jsonschema:"required,description=CSS selector of the iframe element to switch into"`
}

type CookieSetInput struct {
	TargetID  string  `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
	Name      string  `json:"name" jsonschema:"required,description=Cookie name"`
	Value     string  `json:"value" jsonschema:"required,description=Cookie value"`
	Domain    string  `json:"domain,omitempty" jsonschema:"description=Optional cookie domain"`
	Path      string  `json:"path,omitempty" jsonschema:"description=Cookie path"`
	Expires   float64 `json:"expires,omitempty" jsonschema:"description=Unix expiry time"`
	Secure    bool    `json:"secure,omitempty" jsonschema:"description=Secure cookie flag"`
	HTTPOnly  bool    `json:"http_only,omitempty" jsonschema:"description=HTTP-only cookie flag"`
	SameSite  string  `json:"same_site,omitempty" jsonschema:"description=SameSite policy"`
	TimeoutMS int     `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds"`
}

func (i CookieSetInput) Cookie() agent.CookieInput {
	return agent.CookieInput{Name: i.Name, Value: i.Value, Domain: i.Domain, Path: i.Path, Expires: i.Expires, Secure: i.Secure, HTTPOnly: i.HTTPOnly, SameSite: i.SameSite}
}

type StopTraceInput struct {
	Path string `json:"path" jsonschema:"required,description=New file path for the trace zip; relative paths resolve under the OS temp directory and existing or protected paths are rejected"`
}

type HybridObserveInput struct {
	TargetID     string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects capture if Scout has switched pages"`
	IncludeImage bool   `json:"include_image,omitempty" jsonschema:"description=Include base64 screenshot in response. Default false to keep responses compact."`
	OutputPath   string `json:"output_path,omitempty" jsonschema:"description=Write to a new screenshot path and return it instead of base64. Relative paths resolve under temp; existing and protected paths are rejected."`
	SaveToDisk   bool   `json:"save_to_disk,omitempty" jsonschema:"description=Write the screenshot to a generated file and return its path instead of base64."`
}

type HybridObserveResult struct {
	ContentTrust string                   `json:"content_trust,omitempty"`
	Image        string                   `json:"image,omitempty"`
	ImageInfo    *browse.ScreenshotInfo   `json:"image_info,omitempty"`
	ImagePath    string                   `json:"image_path,omitempty"`
	TargetID     string                   `json:"target_id,omitempty"`
	FrameID      string                   `json:"frame_id,omitempty"`
	Elements     []agent.HybridElement    `json:"elements"`
	Count        int                      `json:"count"`
	Total        int                      `json:"total"`
	Truncated    bool                     `json:"truncated"`
	Width        int                      `json:"viewport_width"`
	Height       int                      `json:"viewport_height"`
	Artifact     *agent.ArtifactReference `json:"artifact,omitempty"`
}

type FindByCoordinatesInput struct {
	TargetID string `json:"target_id,omitempty" jsonschema:"description=target_id from hybrid_observe; rejects lookup if Scout has switched pages"`
	X        int    `json:"x" jsonschema:"required,description=X pixel coordinate in the viewport"`
	Y        int    `json:"y" jsonschema:"required,description=Y pixel coordinate in the viewport"`
}

type BatchInput struct {
	TargetID          string              `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier result; rejects the batch if Scout has switched pages"`
	Actions           []agent.BatchAction `json:"actions" jsonschema:"required,description=Array of actions to execute. Each has action (click/type/fill_form_semantic/wait/scroll_to/click_label/webmcp_invoke). WebMCP actions require webmcp_name and webmcp_frame_id from the current catalog plus exactly one expected postcondition; pass input only when required by the page schema."`
	TimeoutMS         int                 `json:"timeout_ms,omitempty" jsonschema:"description=Optional deadline in milliseconds for the complete batch"`
	ContinueOnUnknown bool                `json:"continue_on_unknown,omitempty" jsonschema:"description=Explicitly allow later actions to run after an uncertain step; default behavior stops and requires reconciliation first"`
	RequireVerified   bool                `json:"require_verified,omitempty" jsonschema:"description=Require positive verification evidence for every step and stop at the first unverified, rejected, or unknown outcome"`
}

type TaskRunInput struct {
	ID                 string               `json:"id" jsonschema:"required,description=Stable caller-provided identifier for this task run"`
	TargetID           string               `json:"target_id,omitempty" jsonschema:"description=Target ID observed before the task; execution is rejected if Scout has switched pages"`
	AllowedOrigins     []string             `json:"allowed_origins,omitempty" jsonschema:"description=Exact HTTP(S) origins; must exactly match the running session policy because task runs cannot broaden or narrow browser policy after launch"`
	RequiredTools      []string             `json:"required_tools,omitempty" jsonschema:"description=Additional MCP tools needed after this task; recover_task_run and resolve_task_run are always required and every named tool is checked before browser actions"`
	FinalPostcondition *agent.Postcondition `json:"final_postcondition,omitempty" jsonschema:"description=Optional page-state condition checked on the bound target after every action completes; if it is not observed, status is needs_reconciliation with failure_reason=verification_failed and same-ID replay remains blocked"`
	Actions            []agent.BatchAction  `json:"actions" jsonschema:"required,description=Sequential verified actions (click/type/fill_form_semantic/wait/scroll_to/click_label/webmcp_invoke); every step needs positive verification evidence, including an expected postcondition for WebMCP"`
	TimeoutMS          int                  `json:"timeout_ms,omitempty" jsonschema:"description=Deadline for the complete task run"`
}

type GetTaskRunInput struct {
	ID string `json:"id" jsonschema:"required,description=Caller-provided task ID to inspect in the active session's task store"`
}

type ExportTaskRecoveryReceiptInput struct {
	ID               string `json:"id" jsonschema:"required,description=Caller-provided task ID from task_run or get_task_run"`
	OperationOrdinal int    `json:"operation_ordinal,omitempty" jsonschema:"description=Saved action receipt ordinal; omit to export the currently started interrupted step or the first saved action receipt"`
}

type GetTaskRunResult struct {
	ContentTrust string               `json:"content_trust,omitempty"`
	Found        bool                 `json:"found"`
	Record       *agent.TaskRunRecord `json:"record,omitempty"`
}

type RecoverTaskRunInput struct {
	ID               string `json:"id" jsonschema:"required,description=Caller-provided task ID to inspect in the active session's task store"`
	OperationOrdinal int    `json:"operation_ordinal,omitempty" jsonschema:"description=Optional saved action ordinal for receipt export; omit to select the interrupted action or first saved action"`
}

type RecoverTaskRunResult struct {
	ContentTrust             string                 `json:"content_trust,omitempty"`
	Found                    bool                   `json:"found"`
	Record                   *agent.TaskRunRecord   `json:"record,omitempty"`
	Receipt                  *agent.RecoveryReceipt `json:"receipt,omitempty"`
	ReceiptUnavailableReason string                 `json:"receipt_unavailable_reason,omitempty"`
	Guidance                 agent.TaskRunGuidance  `json:"guidance"`
}

type ResolveTaskRunInput struct {
	ID                string `json:"id" jsonschema:"required,description=Task ID from recover_task_run or get_task_run for an interrupted reservation"`
	PlanFingerprint   string `json:"plan_fingerprint" jsonschema:"required,description=Exact fingerprint returned by recover_task_run or get_task_run; prevents resolving a different plan"`
	Status            string `json:"status" jsonschema:"required,enum=unresolved|verified_complete|rejected,description=Host reconciliation observation: unresolved, verified_complete, or rejected; verified_complete and rejected require evidence_source, evidence_reference, and observed_at"`
	Evidence          string `json:"evidence" jsonschema:"required,description=Short non-secret description of the authoritative application state used to reach the conclusion"`
	EvidenceSource    string `json:"evidence_source,omitempty" jsonschema:"description=Required for verified_complete or rejected; stable name of the authoritative system inspected, for example orders_api"`
	EvidenceReference string `json:"evidence_reference,omitempty" jsonschema:"description=Required for verified_complete or rejected; opaque non-secret record or event identifier from that source"`
	ObservedAt        string `json:"observed_at,omitempty" jsonschema:"format=date-time,description=Required for verified_complete or rejected; RFC3339 time when the authoritative application state was read"`
}

func validateTaskRunToolRequirements(ctx context.Context, required []string, toolFilter func(context.Context, string) bool) error {
	// A verified task can become an interrupted reservation at any point after
	// dispatch. Always require the read-only inspection and host-resolution
	// tools, even when the caller did not explicitly list follow-up tools.
	// Otherwise the active profile could make the task unrecoverable through
	// MCP after a worker or transport failure.
	required = append(append([]string(nil), required...), "recover_task_run", "resolve_task_run")
	seen := make(map[string]struct{}, len(required))
	missing := make([]string, 0)
	for _, raw := range required {
		name := strings.TrimSpace(raw)
		if name == "" {
			return mcpPolicyError("SCOUT_INVALID_ARGUMENT", "required_tools entries must be non-empty tool names", "Remove empty entries and retry.")
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		if _, known := mcpAllToolNames[name]; !known {
			return mcpPolicyError("SCOUT_INVALID_ARGUMENT", fmt.Sprintf("required_tools references unknown MCP tool %q", name), "Use tool names listed by the help tool.")
		}
		if toolFilter == nil || !toolFilter(ctx, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	env := MCPErrorEnvelope{
		Code:    "SCOUT_TASK_CAPABILITY_MISSING",
		Message: "The active MCP profile does not expose every tool required by this task.",
		Detail:  strings.Join(missing, ", "),
		Hint:    "Restart Scout with a profile that exposes the required tools, or remove additional required_tools that are not available.",
	}
	b, _ := json.Marshal(env)
	return &mcpserver.ToolInputError{Message: "SCOUT_ERROR " + string(b)}
}

type ObserveDiffResult struct {
	ContentTrust string             `json:"content_trust,omitempty"`
	Observation  *agent.Observation `json:"observation"`
	Diff         *agent.DOMDiff     `json:"diff"`
}

var mcpToolProfiles = map[string]map[string]struct{}{
	"read": toolNameSet(
		"status", "list_sessions", "get_task_run", "recover_task_run", "export_task_recovery_receipt", "navigate", "back", "forward", "reload", "observe", "observe_diff", "observe_with_budget", "observe_scoped",
		"submit_outcome", "aria_violations", "extract", "extract_all", "extract_table", "markdown",
		"readable_text", "accessibility_tree", "read_page_snapshot", "search_page_snapshot", "screenshot",
		"webmcp_catalog",
		"annotated_screenshot", "pdf", "network_summary", "network_requests", "detect_frameworks",
		"component_state", "app_state", "detect_dialog", "cookies_list", "check_readiness", "web_vitals",
		"session_history", "console_errors", "failed_requests", "detect_auth_wall", "compare_tabs",
		"assert_text_contains", "has_element", "wait_for", "discover_form", "list_tabs", "expect_recorded_action",
	),
	"interact": toolNameSet(
		"status", "list_sessions", "navigate", "back", "forward", "reload", "observe", "observe_diff",
		"observe_with_budget", "observe_scoped", "submit_outcome", "click", "click_label", "click_handle",
		"click_text", "type", "press_key", "fill_form", "fill_form_semantic", "dispatch_event", "submit_form", "hover",
		"key_down", "key_up",
		"double_click", "right_click", "select_option", "scroll_to", "scroll_by", "focus", "drag_drop",
		"batch", "task_run", "get_task_run", "recover_task_run", "resolve_task_run", "export_task_recovery_receipt", "extract", "extract_all", "extract_table", "markdown", "readable_text", "accessibility_tree",
		"screenshot", "annotated_screenshot", "network_summary", "network_requests", "dismiss_cookies",
		"cookies_list", "cookies_set", "check_readiness", "select_by_prompt", "hybrid_observe",
		"find_by_coordinates", "auto_extract", "scroll_and_collect", "upload_file", "assert_text_contains",
		"has_element", "wait_for", "discover_form", "open_tab", "switch_tab", "close_tab", "list_tabs",
		"switch_to_frame", "switch_to_main_frame",
		"webmcp_catalog", "webmcp_invoke",
	),
	"diagnose": toolNameSet(
		"status", "list_sessions", "get_task_run", "recover_task_run", "export_task_recovery_receipt", "resolve_task_run", "reconcile_action", "observe", "observe_diff", "observe_with_budget",
		"observe_scoped", "submit_outcome", "wait_for_navigation", "aria_violations", "extract", "extract_all",
		"extract_table", "markdown", "readable_text", "accessibility_tree", "read_page_snapshot",
		"search_page_snapshot", "screenshot", "annotated_screenshot", "pdf", "network_summary", "network_requests",
		"wait_spa", "wait_for_spa_idle", "detect_frameworks", "component_state", "app_state", "detect_dialog",
		"cookies_list", "check_readiness", "web_vitals", "suggest_selectors", "hybrid_observe",
		"session_history", "console_errors", "failed_requests", "detect_auth_wall", "compare_tabs",
		"assert_text_contains", "has_element", "wait_for", "discover_form", "list_tabs", "switch_to_frame",
		"switch_to_main_frame", "expect_recorded_action", "start_trace", "stop_trace", "webmcp_catalog",
	),
	"admin": toolNameSet(
		"configure", "save_profile", "load_profile", "set_viewport", "reset", "status", "list_sessions", "get_task_run", "recover_task_run", "export_task_recovery_receipt", "resolve_task_run",
		"create_session", "switch_session", "close_session", "clone_session", "persist_session", "resume_session", "revoke_session", "reconcile_action",
		"webmcp_catalog", "webmcp_invoke",
		"install_submit_tracker", "save_page_snapshot", "delete_page_snapshot", "enable_network_capture",
		"cookies_clear", "cookies_set", "upload_file", "start_recording", "stop_recording",
		"save_playbook", "replay_playbook", "resume_playbook", "start_screen_recording", "stop_screen_recording",
		"start_trace", "stop_trace",
	),
}

func init() {
	// Help is always present so clients can discover the intended workflow even
	// when a selected profile omits specialized tools.
	artifactTools := []string{"read_page_snapshot", "search_page_snapshot", "delete_page_snapshot"}
	for _, profile := range []string{"read", "interact", "diagnose", "admin"} {
		mcpToolProfiles[profile]["help"] = struct{}{}
		for _, name := range artifactTools {
			mcpToolProfiles[profile][name] = struct{}{}
		}
	}
	// Core is the everyday browser workflow surface. Keep diagnostics, network
	// internals, profile/configuration, recording, and recovery controls opt-in.
	mcpToolProfiles["core"] = toolNameSet(
		"help", "status", "list_sessions", "webmcp_catalog", "webmcp_invoke", "navigate", "back", "forward", "reload", "observe", "observe_diff",
		"observe_with_budget", "observe_scoped", "click", "click_label", "click_handle", "click_text", "type", "press_key", "key_down", "key_up",
		"fill_form", "fill_form_semantic", "submit_form", "hover", "double_click", "right_click", "select_option",
		"scroll_to", "scroll_by", "focus", "drag_drop", "batch", "task_run", "get_task_run", "extract", "extract_all", "extract_table",
		"markdown", "readable_text", "accessibility_tree", "screenshot", "annotated_screenshot", "has_element",
		"wait_for", "discover_form", "open_tab", "switch_tab", "close_tab", "list_tabs", "switch_to_frame",
		"switch_to_main_frame", "dismiss_cookies", "cookies_list", "cookies_set", "recover_task_run", "resolve_task_run",
	)
	for _, name := range artifactTools {
		mcpToolProfiles["core"][name] = struct{}{}
	}
}

var mcpAllToolNames = toolNameSet(
	"help", "configure", "save_profile", "load_profile", "set_viewport", "reset", "status", "list_sessions", "webmcp_catalog", "webmcp_invoke", "create_session",
	"switch_session", "close_session", "clone_session", "persist_session", "resume_session", "revoke_session", "navigate", "back", "forward", "reload", "observe",
	"reconcile_action", "observe_diff", "observe_with_budget", "observe_scoped", "submit_outcome", "install_submit_tracker",
	"wait_for_navigation", "aria_violations", "click", "click_label", "click_handle", "click_text", "type", "press_key", "key_down", "key_up", "fill_form",
	"fill_form_semantic", "dispatch_event", "submit_form", "hover", "double_click", "right_click", "select_option",
	"scroll_to", "scroll_by", "focus", "drag_drop", "batch", "extract", "extract_all", "extract_table", "markdown",
	"readable_text", "accessibility_tree", "save_page_snapshot", "read_page_snapshot", "search_page_snapshot",
	"delete_page_snapshot", "screenshot", "annotated_screenshot", "pdf", "enable_network_capture", "network_summary",
	"network_requests", "wait_spa", "wait_for_spa_idle", "detect_frameworks", "component_state", "app_state", "detect_dialog",
	"dismiss_cookies", "cookies_list", "cookies_clear", "cookies_set", "check_readiness", "web_vitals", "select_by_prompt",
	"suggest_selectors", "hybrid_observe", "find_by_coordinates", "session_history", "auto_extract", "scroll_and_collect",
	"console_errors", "failed_requests", "detect_auth_wall", "upload_file", "compare_tabs", "assert_text_contains",
	"has_element", "wait_for", "discover_form", "open_tab", "switch_tab", "close_tab", "list_tabs", "switch_to_frame",
	"switch_to_main_frame", "start_recording", "stop_recording", "expect_recorded_action", "save_playbook", "replay_playbook", "task_run", "get_task_run", "recover_task_run", "export_task_recovery_receipt",
	"resume_playbook", "start_screen_recording", "stop_screen_recording", "start_trace", "stop_trace", "resolve_task_run",
)

func toolNameSet(names ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func sortedToolNames(names map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func mcpToolProfileFilter(profile string) (func(context.Context, string) bool, error) {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		profile = "all"
	}
	if profile == "all" {
		return func(context.Context, string) bool { return true }, nil
	}
	tools, ok := mcpToolProfiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown MCP tool profile %q (choose all, core, read, interact, diagnose, or admin)", profile)
	}
	return func(_ context.Context, name string) bool {
		_, ok := tools[name]
		return ok
	}, nil
}

const mcpSwitchToFrameDescription = "Switch execution context into an iframe selected by CSS in the currently selected document (the main page initially). Supports same-process, cross-origin, nested, and site-isolated out-of-process iframes (OOPIFs) when Chrome exposes one unambiguous child target bound to the selected parent frame and URL. Scout attaches a child-scoped CDP session and applies the session URL policy; missing owners or ambiguous target bindings fail closed. Optionally pass target_id from earlier page evidence to reject the operation if Scout has switched top-level pages. The result includes frame URL/title evidence and outcome=frame_selected when evaluation in the selected frame succeeds. Subsequent actions operate inside that iframe until another frame or switch_to_main_frame is selected."

// mcpCuratedToolNames keeps the common browser workflow available while
// reducing the choice set presented to MCP clients. It is a discovery aid,
// not an authorization boundary; use tool profiles and HTTP principal policy
// for access control.
var mcpCuratedToolNames = toolNameSet(
	"help", "status", "list_sessions", "create_session", "navigate", "back", "forward", "reload",
	"observe", "observe_diff", "observe_with_budget", "observe_scoped", "click", "click_label", "click_handle",
	"click_text", "type", "press_key", "key_down", "key_up", "fill_form", "fill_form_semantic", "submit_form", "hover", "select_option",
	"scroll_to", "scroll_by", "focus", "batch", "task_run", "get_task_run", "recover_task_run",
	"resolve_task_run", "export_task_recovery_receipt", "extract", "extract_all", "extract_table", "markdown",
	"readable_text", "accessibility_tree", "screenshot", "annotated_screenshot", "has_element", "wait_for",
	"discover_form", "open_tab", "switch_tab", "close_tab", "list_tabs", "switch_to_frame", "switch_to_main_frame",
	"dismiss_cookies", "cookies_list", "cookies_set", "webmcp_catalog", "webmcp_invoke",
)

func validateMCPRequiredTools(profile, requiredRaw string, curatedModes ...bool) error {
	curated := len(curatedModes) > 0 && curatedModes[0]
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		profile = "all"
	}
	if _, err := mcpToolProfileFilter(profile); err != nil {
		return err
	}
	if strings.TrimSpace(requiredRaw) == "" {
		return nil
	}

	missing := make([]string, 0)
	seen := make(map[string]struct{})
	for _, raw := range strings.Split(requiredRaw, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			return fmt.Errorf("--require-tools must be a comma-separated list of tool names")
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if _, exists := mcpAllToolNames[name]; !exists {
			return fmt.Errorf("--require-tools references unknown MCP tool %q", name)
		}
		if profile != "all" {
			if _, exposed := mcpToolProfiles[profile][name]; !exposed {
				missing = append(missing, name)
				continue
			}
		}
		if curated {
			if _, exposed := mcpCuratedToolNames[name]; !exposed {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		if curated {
			return fmt.Errorf("MCP profile %q with curated tools does not expose required tools: %s", profile, strings.Join(missing, ", "))
		}
		return fmt.Errorf("MCP profile %q does not expose required tools: %s", profile, strings.Join(missing, ", "))
	}
	return nil
}

func validateMCPToolProfiles(registered []string) error {
	actual := toolNameSet(registered...)
	if len(actual) != len(mcpAllToolNames) {
		return fmt.Errorf("MCP tool profile catalog has %d names; server registers %d", len(mcpAllToolNames), len(actual))
	}
	for name := range actual {
		if _, ok := mcpAllToolNames[name]; !ok {
			return fmt.Errorf("registered MCP tool %q is missing from profile catalog", name)
		}
	}
	for name := range mcpAllToolNames {
		if _, ok := actual[name]; !ok {
			return fmt.Errorf("profile catalog references unregistered MCP tool %q", name)
		}
	}
	for name := range mcpCuratedToolNames {
		if _, ok := actual[name]; !ok {
			return fmt.Errorf("curated MCP catalog references unregistered tool %q", name)
		}
	}
	for profile, tools := range mcpToolProfiles {
		if len(tools) == 0 {
			return fmt.Errorf("MCP tool profile %q is empty", profile)
		}
		for name := range tools {
			if _, ok := actual[name]; !ok {
				return fmt.Errorf("MCP tool profile %q references unregistered tool %q", profile, name)
			}
		}
	}
	return nil
}

func runMCPWithCurated(httpAddr, profile, requiredTools, taskStorePath string, curated bool) (retErr error) {
	idleTimeout, err := resolveSessionIdleTimeout()
	if err != nil {
		return err
	}
	sessionLimit, err := resolveSessionLimit()
	if err != nil {
		return err
	}
	if err := validateMCPRequiredTools(profile, requiredTools, curated); err != nil {
		return err
	}
	toolFilter, err := mcpToolProfileFilter(profile)
	if err != nil {
		return err
	}
	var httpOpts []mcp.HTTPOption
	principalPolicies, err := loadMCPHTTPPrincipalPolicies(os.Getenv("SCOUT_MCP_HTTP_PRINCIPALS_JSON"))
	if err != nil {
		return err
	}
	if proxy := configuredProxyServer(); proxy != "" {
		if err := agent.ValidateProxyServer(proxy); err != nil {
			return err
		}
	}
	if configuredProxyServer() != "" && strings.TrimSpace(os.Getenv("SCOUT_REMOTE_CDP")) != "" && len(principalPolicies) == 0 {
		return fmt.Errorf("SCOUT_PROXY_SERVER only configures locally launched browsers; configure the proxy on the SCOUT_REMOTE_CDP browser host")
	}
	allowedOrigins, allowedOriginsConfigured, err := configuredMCPAllowedOrigins()
	if err != nil {
		return err
	}
	if allowedOriginsConfigured && len(principalPolicies) > 0 {
		return fmt.Errorf("SCOUT_MCP_ALLOWED_ORIGINS cannot be combined with SCOUT_MCP_HTTP_PRINCIPALS_JSON; set allowed_origins on each principal")
	}
	var taskRunStore agent.TaskRunStore
	var sessionRecoveryStore agent.SessionRecoveryStore
	var closeTaskRunStore func() error
	if taskStorePath != "" {
		boltStore, err := agent.OpenBoltTaskRunStore(taskStorePath)
		if err != nil {
			return err
		}
		taskRunStore = boltStore
		sessionRecoveryStore = boltStore
		closeTaskRunStore = boltStore.Close
		defer func() {
			if err := closeTaskRunStore(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("close task run store: %w", err))
			}
		}()
	}
	principalSessions := newMCPHTTPPrincipalSessions()
	if httpAddr != "" {
		statelessHTTP := envBool("SCOUT_MCP_HTTP_STATELESS")
		httpOpts, err = mcpHTTPServeOptionsWithPrincipalSessionsMode(httpAddr, os.Getenv("SCOUT_MCP_HTTP_TOKEN"), principalPolicies, principalSessions, statelessHTTP)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if sessionRecoveryStore != nil {
		go func() {
			claimStore, supportsClaims := sessionRecoveryStore.(agent.ExpiredSessionRecoveryClaimStore)
			batchClaimStore, supportsBatchClaims := sessionRecoveryStore.(agent.ExpiredSessionRecoveryBatchClaimStore)
			claimant, claimIDErr := newMCPOpaqueSessionID()
			cleanupExpired := func() {
				if !supportsClaims || claimIDErr != nil {
					fmt.Fprintln(os.Stderr, "scout: expired session recovery cleanup skipped: store does not support exclusive cleanup claims")
					return
				}
				deleted, cleanupFailed := 0, 0
				attemptedHandles := make([]string, 0)
				for ctx.Err() == nil {
					var record agent.SessionRecoveryRecord
					var claimed bool
					var err error
					if supportsBatchClaims {
						record, claimed, err = batchClaimStore.ClaimExpiredSessionRecoveryRecordExcluding(ctx, time.Now().UTC(), claimant, 30*time.Second, attemptedHandles)
					} else {
						record, claimed, err = claimStore.ClaimExpiredSessionRecoveryRecord(ctx, time.Now().UTC(), claimant, 30*time.Second)
					}
					if err != nil {
						cleanupFailed++
						fmt.Fprintf(os.Stderr, "scout: expired session recovery claim failed: %v\n", err)
						break
					}
					if !claimed {
						break
					}
					releaseClaim := func() {
						releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
						if err := claimStore.ReleaseExpiredSessionRecoveryRecord(releaseCtx, record.Handle, claimant); err != nil {
							fmt.Fprintf(os.Stderr, "scout: expired session recovery claim release failed: %v\n", err)
						}
						releaseCancel()
					}
					retryOtherRecords := func() bool {
						releaseClaim()
						if !supportsBatchClaims {
							return false
						}
						attemptedHandles = append(attemptedHandles, record.Handle)
						return true
					}
					owner := strings.TrimPrefix(record.OwnerScope, "principal/")
					policy, trusted := principalPolicies[owner]
					if strings.HasPrefix(record.OwnerScope, "principal/") && !trusted {
						cleanupFailed++
						fmt.Fprintln(os.Stderr, "scout: expired session cleanup has no current trusted owner policy; retrying after lease expiry")
						if retryOtherRecords() {
							continue
						}
						break
					}
					cfg := agent.SessionConfig{
						RemoteCDP:       os.Getenv("SCOUT_REMOTE_CDP"),
						AllowedOrigins:  allowedOrigins,
						AllowPrivateIPs: envBool("SCOUT_ALLOW_PRIVATE_IPS"),
					}
					if trusted {
						cfg.RemoteCDP = policy.RemoteCDP
						cfg.AllowedOrigins = append([]string(nil), policy.AllowedOrigins...)
						cfg.AllowPrivateIPs = policy.AllowPrivateIPs
						cfg.IsolateRemoteBrowserContext = policy.IsolateBrowserContext
					}
					cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 5*time.Second)
					cleanupErr := agent.DisposeSessionRecoveryContext(cleanupCtx, cfg, record)
					cleanupCancel()
					if cleanupErr != nil {
						cleanupFailed++
						fmt.Fprintf(os.Stderr, "scout: expired session context cleanup failed: %v\n", cleanupErr)
						if retryOtherRecords() {
							continue
						}
						break
					}
					completeCtx, completeCancel := context.WithTimeout(ctx, 2*time.Second)
					removed, deleteErr := claimStore.CompleteExpiredSessionRecoveryRecord(completeCtx, record.Handle, claimant)
					completeCancel()
					if deleteErr != nil || !removed {
						cleanupFailed++
						fmt.Fprintf(os.Stderr, "scout: expired session recovery record cleanup failed (removed=%t): %v\n", removed, deleteErr)
						if retryOtherRecords() {
							continue
						}
						break
					}
					deleted++
				}
				if deleted > 0 || cleanupFailed > 0 {
					fmt.Fprintf(os.Stderr, "scout: expired recovery cleanup removed %d record(s), %d cleanup failure(s)\n", deleted, cleanupFailed)
				}
			}
			cleanupExpired()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					cleanupExpired()
				}
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	stopSignalWatcher := make(chan struct{})
	signalWatcherDone := make(chan struct{})
	var stopSignalWatcherOnce sync.Once
	stopSignals := func() {
		stopSignalWatcherOnce.Do(func() {
			signal.Stop(sigCh)
			close(stopSignalWatcher)
		})
		<-signalWatcherDone
	}
	defer stopSignals()
	go func() {
		defer close(signalWatcherDone)
		select {
		case <-sigCh:
			cancel()
		case <-stopSignalWatcher:
		}
	}()

	// The default session is lazy; named sessions are created explicitly and held
	// until closed or the MCP process exits.
	var (
		sessions                   = make(map[string]*agent.Session)
		sessionRecoveryHandles     = make(map[string]map[string]struct{})
		recoveryHandlesStarting    = make(map[string]bool)
		recoveryPrincipalsStarting = make(map[string]bool)
		sessionIDs                 = make(map[string]string)
		sessionKeysByID            = make(map[string]string)
		snapshotFiles              = make(map[string][32]byte)
		snapshotOwners             = make(map[string]string)
		resultArtifactSizes        = make(map[string]int64)
		resultArtifactBytes        int64
		resultOwnerBytes           = make(map[string]int64)
		resultOwnerCounts          = make(map[string]int)
		activeName                 = "default"
		activeByClient             = make(map[string]string)
		configRevision             uint64
		configByClient             = make(map[string]agent.SessionConfig)
		revisionByClient           = make(map[string]uint64)
		lastActivity               = make(map[string]time.Time)
		inFlight                   = make(map[string]int)
		closingByName              = make(map[string]bool)
		startingSessions           = make(map[string]int)
		startingByName             = make(map[string]chan struct{})
		closingSessions            = make(map[string]int)
		cleanupPending             = make(map[string]bool)
		cleanupErrors              = make(map[string]string)
		shuttingDown               bool
		sessionCfg                 = agent.SessionConfig{
			Headless:        true,
			AllowPrivateIPs: envBool("SCOUT_ALLOW_PRIVATE_IPS"),
			AllowedOrigins:  allowedOrigins,
			RemoteCDP:       os.Getenv("SCOUT_REMOTE_CDP"),
			ProxyServer:     configuredProxyServer(),
			TracerProvider:  otel.GetTracerProvider(),
			TaskRunStore:    taskRunStore,
		}
		sessionMu   sync.Mutex
		snapshotMu  sync.Mutex
		idleClosing sync.WaitGroup
	)
	statelessHTTP := httpAddr != "" && envBool("SCOUT_MCP_HTTP_STATELESS")
	clientIDForContext := func(ctx context.Context) string {
		clientID := mcptransport.HTTPHeadersFromContext(ctx).Get("Mcp-Session-Id")
		return mcpHTTPClientScope(mcpHTTPPrincipalFromContext(ctx), clientID, statelessHTTP)
	}
	configForContextLocked := func(ctx context.Context) agent.SessionConfig {
		if clientID := clientIDForContext(ctx); clientID != "" {
			if cfg, ok := configByClient[clientID]; ok {
				return cfg
			}
		}
		if principal := mcpHTTPPrincipalFromContext(ctx); principal != "" {
			if policy, ok := principalPolicies[principal]; ok {
				cfg := sessionCfg
				cfg.RemoteCDP = policy.RemoteCDP
				// Principal CDP endpoints are already-running browsers; their host
				// owns proxy configuration and cannot inherit this process's proxy.
				cfg.ProxyServer = ""
				cfg.AllowedOrigins = append([]string(nil), policy.AllowedOrigins...)
				cfg.AllowPrivateIPs = policy.AllowPrivateIPs
				cfg.IsolateRemoteBrowserContext = policy.IsolateBrowserContext
				return cfg
			}
		}
		return sessionCfg
	}
	configRevisionForContextLocked := func(ctx context.Context) uint64 {
		if clientID := clientIDForContext(ctx); clientID != "" {
			return revisionByClient[clientID]
		}
		return configRevision
	}
	var sessionVisibleToContext func(context.Context, string) bool
	sessionKeyForContext := func(ctx context.Context, name string) string {
		if clientID := clientIDForContext(ctx); clientID != "" {
			return "http/" + clientID + "/" + name
		}
		return name
	}
	resolveSessionSelectorLocked := func(ctx context.Context, selector string) (string, error) {
		return resolveMCPSessionSelector(selector, sessionKeysByID,
			func(name string) string { return sessionKeyForContext(ctx, name) },
			func(key string) bool { return sessionVisibleToContext(ctx, key) })
	}
	ensureSessionIDLocked := func(key string) (string, error) {
		if id := sessionIDs[key]; id != "" {
			return id, nil
		}
		var id string
		for {
			var err error
			id, err = newMCPOpaqueSessionID()
			if err != nil {
				return "", err
			}
			if sessionKeysByID[id] == "" {
				break
			}
		}
		sessionIDs[key] = id
		sessionKeysByID[id] = key
		return id, nil
	}
	revokeSessionIDLocked := func(key string) {
		if id := sessionIDs[key]; id != "" {
			delete(sessionKeysByID, id)
			delete(sessionIDs, key)
		}
	}
	sessionNameForContext := func(key string) string {
		if strings.HasPrefix(key, "http/") {
			_, name, ok := strings.Cut(strings.TrimPrefix(key, "http/"), "/")
			if ok {
				return name
			}
		}
		return key
	}
	scopeTaskRunStore := func(ctx context.Context, key string, cfg agent.SessionConfig) agent.SessionConfig {
		if cfg.TaskRunStore == nil {
			return cfg
		}
		name := sessionNameForContext(key)
		principal := mcpHTTPPrincipalFromContext(ctx)
		if principal != "" {
			cfg.TaskRunStore = scopedTaskRunStore{store: cfg.TaskRunStore, namespace: "principal/" + principal + "/session/" + name}
		} else if clientID := clientIDForContext(ctx); clientID != "" {
			cfg.TaskRunStore = scopedTaskRunStore{store: cfg.TaskRunStore, namespace: "http-client/" + clientID + "/session/" + name}
		} else {
			cfg.TaskRunStore = scopedTaskRunStore{store: cfg.TaskRunStore, namespace: "stdio/session/" + name}
		}
		return cfg
	}
	recoveryConfigForContext := func(ctx context.Context) (agent.SessionConfig, string, error) {
		principal := mcpHTTPPrincipalFromContext(ctx)
		policy, ok := principalPolicies[principal]
		if principal == "" || !ok {
			return agent.SessionConfig{}, "", fmt.Errorf("durable session recovery requires an authenticated configured HTTP principal")
		}
		if policy.RemoteCDP == "" {
			return agent.SessionConfig{}, "", fmt.Errorf("durable session recovery requires a remote CDP endpoint")
		}
		cfg := sessionCfg
		cfg.RemoteCDP = policy.RemoteCDP
		cfg.AllowedOrigins = append([]string(nil), policy.AllowedOrigins...)
		cfg.AllowPrivateIPs = policy.AllowPrivateIPs
		cfg.IsolateRemoteBrowserContext = policy.IsolateBrowserContext
		cfg = scopeTaskRunStore(ctx, sessionKeyForContext(ctx, "default"), cfg)
		return cfg, "principal/" + principal, nil
	}
	sessionVisibleToContext = func(ctx context.Context, key string) bool {
		return mcpSessionKeyVisibleToOwner(key, clientIDForContext(ctx), mcpHTTPPrincipalFromContext(ctx))
	}
	sessionOwnerForKey := func(key string) string {
		if !strings.HasPrefix(key, "http/") {
			return ""
		}
		owner, _, _ := strings.Cut(strings.TrimPrefix(key, "http/"), "/")
		return owner
	}
	beginSessionCloseLocked := func(key string) {
		delete(cleanupErrors, key)
		cleanupPending[key] = true
		closingSessions[sessionOwnerForKey(key)]++
	}
	// Browser shutdown must have a deadline. Browser.close is best-effort; if
	// Chrome fails to exit after that command, CloseContext force-kills the
	// owned process and removes its temporary profile when the deadline expires.
	closeBrowserSession := func(sess *agent.Session) error {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return sess.CloseContext(closeCtx)
	}
	finishSessionCloseLocked := func(key string, closeErr error) {
		if closeErr != nil {
			delete(cleanupPending, key)
			cleanupErrors[key] = closeErr.Error()
			return // Keep uncertain cleanup counted against the session cap.
		}
		delete(cleanupPending, key)
		delete(cleanupErrors, key)
		owner := sessionOwnerForKey(key)
		closingSessions[owner]--
		if closingSessions[owner] <= 0 {
			delete(closingSessions, owner)
		}
	}
	finishSessionClose := func(key string, sess *agent.Session) {
		go func() {
			closeErr := closeBrowserSession(sess)
			if closeErr != nil {
				fmt.Fprintf(os.Stderr, "scout: session cleanup failed: %v\n", closeErr)
			}
			sessionMu.Lock()
			finishSessionCloseLocked(key, closeErr)
			sessionMu.Unlock()
		}()
	}
	visibleLiveSessionCountLocked := func(ctx context.Context) int {
		count := 0
		for name := range sessions {
			if sessionVisibleToContext(ctx, name) {
				count++
			}
		}
		return count
	}
	visibleSessionCountLocked := func(ctx context.Context) int {
		return visibleLiveSessionCountLocked(ctx) + startingSessions[clientIDForContext(ctx)] + closingSessions[clientIDForContext(ctx)]
	}
	isDefaultSession := func(ctx context.Context, key string) bool {
		return key == sessionKeyForContext(ctx, "default")
	}
	activeNameLocked := func(ctx context.Context) string {
		if routing, ok := mcpRequestSessionFromContext(ctx); ok && routing.sessionKey != "" {
			return routing.sessionKey
		}
		if clientID := clientIDForContext(ctx); clientID != "" {
			if name := activeByClient[clientID]; name != "" {
				return name
			}
			return sessionKeyForContext(ctx, "default")
		}
		return activeName
	}
	setActiveNameLocked := func(ctx context.Context, name string) {
		if clientID := clientIDForContext(ctx); clientID != "" {
			activeByClient[clientID] = name
			return
		}
		activeName = name
	}
	artifactOwnerForContext := func(ctx context.Context) string {
		if routing, ok := mcpRequestSessionFromContext(ctx); ok {
			return mcpArtifactOwner(routing.ownerKey, routing.sessionKey)
		}
		return mcpArtifactOwner(clientIDForContext(ctx), "")
	}
	cleanupSessionArtifacts := func(key string) {
		owner := mcpArtifactOwner(sessionOwnerForKey(key), key)
		cleanupPrivateArtifactsForOwner(&snapshotMu, snapshotFiles, snapshotOwners, resultArtifactSizes, &resultArtifactBytes, resultOwnerBytes, resultOwnerCounts, owner)
	}
	ensureSession := func(ctx context.Context) error {
		for {
			sessionMu.Lock()
			if err := ctx.Err(); err != nil {
				sessionMu.Unlock()
				return err
			}
			name := activeNameLocked(ctx)
			if sessions[name] != nil {
				sessionMu.Unlock()
				return nil
			}
			if pending := startingByName[name]; pending != nil {
				sessionMu.Unlock()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-pending:
					continue
				}
			}
			if sessionLimit > 0 && visibleSessionCountLocked(ctx) >= sessionLimit {
				sessionMu.Unlock()
				return sessionLimitError(sessionLimit)
			}
			pending := make(chan struct{})
			startingByName[name] = pending
			startingSessions[clientIDForContext(ctx)]++
			cfg := configForContextLocked(ctx)
			cfg = scopeTaskRunStore(ctx, name, cfg)
			revision := configRevisionForContextLocked(ctx)
			if !isDefaultSession(ctx, name) {
				cfg.RemoteCDP = "" // Named sessions are isolated local browser processes.
			}
			sessionMu.Unlock()
			s, err := agent.NewSessionContext(ctx, cfg)
			sessionMu.Lock()
			startingSessions[clientIDForContext(ctx)]--
			delete(startingByName, name)
			if err != nil {
				close(pending)
				sessionMu.Unlock()
				return err
			}
			if err := ctx.Err(); err != nil {
				close(pending)
				beginSessionCloseLocked(name)
				sessionMu.Unlock()
				finishSessionClose(name, s)
				return err
			}
			if activeNameLocked(ctx) != name || configRevisionForContextLocked(ctx) != revision {
				close(pending)
				beginSessionCloseLocked(name)
				sessionMu.Unlock()
				finishSessionClose(name, s)
				return fmt.Errorf("active session or browser configuration changed while browser was starting; retry the operation")
			}
			if sessions[name] == nil {
				if _, idErr := ensureSessionIDLocked(name); idErr != nil {
					close(pending)
					sessionMu.Unlock()
					finishSessionClose(name, s)
					return idErr
				}
				sessions[name] = s
				lastActivity[name] = time.Now()
				sessionMu.Unlock()
				close(pending)
				return nil
			}
			sessionMu.Unlock()
			close(pending)
			sessionMu.Lock()
			beginSessionCloseLocked(name)
			sessionMu.Unlock()
			finishSessionClose(name, s)
			return nil
		}
	}

	reconfigure := func(ctx context.Context, update func(*agent.SessionConfig)) agent.SessionConfig {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		name := activeNameLocked(ctx)
		if sessions[name] != nil {
			// Close in goroutine with timeout — don't block if CDP calls are in flight
			old := sessions[name]
			beginSessionCloseLocked(name)
			delete(sessions, name)
			delete(lastActivity, name)
			go func() {
				done := make(chan struct{})
				go func() {
					closeErr := closeBrowserSession(old)
					if closeErr != nil {
						fmt.Fprintf(os.Stderr, "scout: session cleanup failed: %v\n", closeErr)
					}
					sessionMu.Lock()
					finishSessionCloseLocked(name, closeErr)
					sessionMu.Unlock()
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					// Force abandon — the browser process will be cleaned up by OS
				}
			}()
		}
		cfg := configForContextLocked(ctx)
		update(&cfg)
		if clientID := clientIDForContext(ctx); clientID != "" {
			configByClient[clientID] = cfg
			revisionByClient[clientID]++
		} else {
			sessionCfg = cfg
			configRevision++
		}
		return cfg
	}

	// Expire only sessions that have had no MCP tool activity for the configured
	// period and have no tool handler in flight. Closing a launched session stops
	// its browser; Session.Close disconnects from remote CDP without closing the
	// caller-owned browser.
	if idleTimeout > 0 {
		interval := idleTimeout / 4
		if interval > time.Second {
			interval = time.Second
		}
		if interval < 10*time.Millisecond {
			interval = 10 * time.Millisecond
		}
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					var expired []struct {
						key     string
						session *agent.Session
					}
					sessionMu.Lock()
					if shuttingDown {
						sessionMu.Unlock()
						return
					}
					for name, sess := range sessions {
						last, ok := lastActivity[name]
						if !ok || inFlight[name] > 0 || now.Sub(last) < idleTimeout {
							continue
						}
						delete(sessions, name)
						delete(sessionRecoveryHandles, name)
						revokeSessionIDLocked(name)
						delete(lastActivity, name)
						delete(inFlight, name)
						beginSessionCloseLocked(name)
						expired = append(expired, struct {
							key     string
							session *agent.Session
						}{key: name, session: sess})
						idleClosing.Add(1)
						if activeName == name {
							activeName = "default"
						}
						for clientID, active := range activeByClient {
							if active == name {
								activeByClient[clientID] = "http/" + clientID + "/default"
							}
						}
					}
					sessionMu.Unlock()
					for _, item := range expired {
						go func(item struct {
							key     string
							session *agent.Session
						}) {
							defer idleClosing.Done()
							closeErr := closeBrowserSession(item.session)
							if closeErr != nil {
								fmt.Fprintf(os.Stderr, "scout: idle session cleanup failed: %v\n", closeErr)
							}
							cleanupSessionArtifacts(item.key)
							sessionMu.Lock()
							finishSessionCloseLocked(item.key, closeErr)
							sessionMu.Unlock()
						}(item)
					}
				}
			}
		}()
	}

	defer func() {
		stopSignals()
		cancel()
		sessionMu.Lock()
		shuttingDown = true
		sessionMu.Unlock()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		idleClosingDone := make(chan struct{})
		go func() {
			idleClosing.Wait()
			close(idleClosingDone)
		}()
		var closeErrs []error
		select {
		case <-idleClosingDone:
		case <-closeCtx.Done():
			closeErrs = append(closeErrs, fmt.Errorf("waiting for in-progress session cleanup during shutdown: %w", closeCtx.Err()))
		}

		sessionMu.Lock()
		toClose := make([]struct {
			key     string
			session *agent.Session
		}, 0, len(sessions))
		for name, sess := range sessions {
			toClose = append(toClose, struct {
				key     string
				session *agent.Session
			}{key: name, session: sess})
			delete(sessions, name)
			revokeSessionIDLocked(name)
		}
		sessionMu.Unlock()
		for _, item := range toClose {
			if err := item.session.CloseContext(closeCtx); err != nil {
				closeErrs = append(closeErrs, err)
				fmt.Fprintf(os.Stderr, "scout: shutdown session cleanup failed: %v\n", err)
			}
			cleanupSessionArtifacts(item.key)
		}
		retErr = errors.Join(retErr, errors.Join(closeErrs...))
	}()
	cleanupHTTPClient := func(clientID, principal string) {
		if clientID == "" {
			return
		}
		clientScope := clientID
		if principal != "" {
			clientScope = principal + "::" + clientID
		}
		sessionMu.Lock()
		if shuttingDown {
			sessionMu.Unlock()
			return
		}
		idleClosing.Add(1)
		sessionMu.Unlock()
		go func() {
			defer idleClosing.Done()
			ticker := time.NewTicker(25 * time.Millisecond)
			defer ticker.Stop()
			for {
				sessionMu.Lock()
				busy := false
				for name, count := range inFlight {
					if mcpHTTPSessionKeyBelongsToClient(name, clientID, principal) && count > 0 {
						busy = true
						break
					}
				}
				if !busy {
					var closing []struct {
						key     string
						session *agent.Session
					}
					for name, sess := range sessions {
						if mcpHTTPSessionKeyBelongsToClient(name, clientID, principal) {
							beginSessionCloseLocked(name)
							closing = append(closing, struct {
								key     string
								session *agent.Session
							}{key: name, session: sess})
							delete(sessions, name)
							delete(sessionRecoveryHandles, name)
							revokeSessionIDLocked(name)
							delete(lastActivity, name)
							delete(inFlight, name)
						}
					}
					delete(activeByClient, clientScope)
					for scope := range configByClient {
						if scope == clientScope {
							delete(configByClient, scope)
						}
					}
					for scope := range revisionByClient {
						if scope == clientScope {
							delete(revisionByClient, scope)
						}
					}
					sessionMu.Unlock()
					for _, item := range closing {
						closeErr := closeBrowserSession(item.session)
						if closeErr != nil {
							fmt.Fprintf(os.Stderr, "scout: HTTP session cleanup failed: %v\n", closeErr)
						}
						cleanupSessionArtifacts(item.key)
						sessionMu.Lock()
						finishSessionCloseLocked(item.key, closeErr)
						sessionMu.Unlock()
					}
					return
				}
				sessionMu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}

	srv := mcp.NewServer(mcp.ServerInfo{
		Name:    "scout",
		Version: version,
		Capabilities: mcp.Capabilities{
			Tools: true,
		},
	}, mcp.WithInstructions(`Scout provides browser automation tools for navigating websites,
filling forms, extracting data, and taking screenshots. Start with 'navigate' to load a page,
then use 'observe' to see interactive elements, and perform actions with 'click', 'type',
'fill_form_semantic', 'extract', 'extract_table', etc. Use 'observe_diff' after actions to
see only what changed. Use 'annotated_screenshot' for visual element identification.
Use 'configure' to switch between headless and visible browser modes without restarting.
Set SCOUT_REMOTE_CDP to attach to an existing Chrome DevTools WebSocket endpoint; the
first page operation attaches to an existing tab when available. Named sessions can be
created, listed, switched, cloned, and closed. Stateful HTTP clients receive separate
default and named session registries; stdio keeps one registry for its connection.
Browser tool calls can address an existing named session without changing the active
session by setting request metadata _meta.go.klarlabs.de/scout/session to its name.
This selector is scoped to the caller's existing registry and is not an authorization token.
Remote CDP endpoints still refer to shared external browser state. Profile save/load tools persist cookies
and localStorage in a private JSON file. Inactive MCP sessions expire after one hour
by default. Set SCOUT_SESSION_IDLE_TIMEOUT (for example, '30m') to change the timeout,
or set it to 'off' to disable automatic expiration; list_sessions reports last_activity and
idle_deadline. Launched browsers close when their idle session expires. Remote CDP
sessions disconnect without closing the caller-owned browser.

Set SCOUT_MCP_ALLOWED_ORIGINS to a comma-separated list of exact HTTP(S) origins to
restrict MCP navigation and page-target requests. This startup policy cannot be changed
with 'configure'. Principal-mapped HTTP deployments must set allowed_origins on each
principal instead. This page-level policy does not cover service-worker requests or
WebSocket handshakes; use an enforcing proxy for browser-wide network controls.

Use the read-only 'help' tool for task recipes and the available tool names in
the selected MCP profile. Common flows are navigate -> observe -> act -> verify,
and observe -> extract for read-only tasks. 'all' exposes specialized diagnostics,
recovery, and administration tools; core focuses on everyday browser work.

Action outcomes are evidence-based: use target_id to identify the page affected. A
dispatched click, event, or submit is not proof that the application accepted the
change. Check verified/outcome/evidence in the result; without a postcondition, the
effect may be explicitly unverified. Field readback proves the DOM value only. For
application acceptance, request a matching postcondition; form acceptance conditions
also require an observed response. Never retry a non-idempotent action when the prior
result says outcome_unknown until you inspect the page state. Browser tool calls are
pinned to the session and target active when each call begins. Core page action tools
also accept target_id from an earlier result to reject an action if that observed
target has changed.

IMPORTANT: Scout uses standard CSS selectors, NOT Playwright selectors. Do NOT use :text(), :has-text(), >> chaining, or other Playwright-specific syntax. Instead:
- To find by text content: use 'observe' or 'annotated_screenshot' to discover elements, then click by selector or label number
- To find a button by text: use 'fill_form_semantic' for forms, or call 'annotated_screenshot' and use 'click_label' with the label number
- Valid selectors: #id, .class, tag, [attr=value], tag:nth-of-type(n), tag:first-child, etc.

IMPORTANT: fill_form and fill_form_semantic take a JSON OBJECT (not array) for fields:
  fill_form: {"fields": {"#email": "value", "#password": "value"}}
  fill_form_semantic: {"fields": {"Email": "value", "Password": "value"}}
Do NOT send fields as an array of objects.

WORKFLOW: navigate first, then use other tools. Use 'dismiss_cookies' after navigate if a cookie banner appears. Use 'check_readiness' if the page seems to still be loading.`))
	// s returns the current session, lazily creating it on first use.
	// Every handler calls this instead of accessing session directly.
	s := func(ctx context.Context) *agent.Session {
		if routing, ok := mcpRequestSessionFromContext(ctx); ok && routing.session != nil {
			return routing.session
		}
		if err := ensureSession(ctx); err != nil {
			panic(sessionStartFailure{err: err})
		}
		sessionMu.Lock()
		defer sessionMu.Unlock()
		return sessions[activeNameLocked(ctx)]
	}

	// Bind each browser tool call to the selected or active session and page when
	// the request begins. The optional MCP _meta selector is only an address
	// within the authenticated owner's existing registry; it grants no access by
	// itself. Principal-authenticated HTTP owners may reconnect with a new
	// transport and select an existing session by opaque ID. Explicit target_id
	// remains a separate precondition against page drift.
	srv.Use(func(next mcp.MiddlewareHandlerFunc) mcp.MiddlewareHandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			if req == nil || req.Method != protocol.MethodToolsCall {
				return next(ctx, req)
			}
			var call mcpToolCallParams
			if err := json.Unmarshal(req.Params, &call); err != nil || call.Name == "" {
				return next(ctx, req)
			}
			if _, exists := srv.GetTool(call.Name); !exists {
				return next(ctx, req)
			}
			if statelessHTTP && mcpStatelessToolRequiresSessionSelector(call.Name) {
				// Parse here only to distinguish a malformed/missing selector. The
				// normal resolver below performs owner authorization under sessionMu.
				selector, selectorErr := parseMCPCallSessionSelector(call.Meta, call.Name)
				if selectorErr != nil {
					return nil, selectorErr
				}
				if selector == "" || !strings.HasPrefix(selector, "scs:") {
					return nil, fmt.Errorf("stateless HTTP requires an opaque session_id in _meta.%s for tool %q", mcpSessionSelectorMetaKey, call.Name)
				}
			}
			selectedSession, err := parseMCPCallSessionSelector(call.Meta, call.Name)
			if err != nil {
				return nil, err
			}
			sessionMu.Lock()
			if selectedSession != "" {
				selectedKey, resolveErr := resolveSessionSelectorLocked(ctx, selectedSession)
				if resolveErr != nil {
					sessionMu.Unlock()
					return nil, resolveErr
				}
				routing := mcpRequestSession{ownerKey: sessionOwnerForKey(selectedKey), sessionKey: selectedKey, sessionID: sessionIDs[selectedKey], explicit: true}
				ctx = context.WithValue(ctx, mcpRequestSessionKey{}, routing)
			}
			active := activeNameLocked(ctx)
			if selectedSession != "" && sessions[active] == nil &&
				(!isDefaultSession(ctx, active) || closingByName[active] || cleanupErrors[active] != "") {
				sessionMu.Unlock()
				return nil, fmt.Errorf("selected session does not exist for this client")
			}
			trackedNames := map[string]struct{}{active: {}}
			closeKey := ""
			if call.Name == "list_sessions" {
				for name := range sessions {
					if sessionVisibleToContext(ctx, name) {
						trackedNames[name] = struct{}{}
					}
				}
			}
			if call.Name == "switch_session" {
				var name string
				if err := json.Unmarshal(call.Arguments["name"], &name); err == nil {
					target, resolveErr := resolveSessionSelectorLocked(ctx, name)
					if resolveErr != nil {
						sessionMu.Unlock()
						return nil, resolveErr
					}
					if closingByName[target] {
						sessionMu.Unlock()
						return nil, fmt.Errorf("session %q is closing; retry after cleanup completes", name)
					}
				}
			}
			if call.Name == "close_session" {
				var name string
				if err := json.Unmarshal(call.Arguments["name"], &name); err == nil {
					key, resolveErr := resolveSessionSelectorLocked(ctx, name)
					if resolveErr != nil {
						sessionMu.Unlock()
						return nil, resolveErr
					}
					if closingByName[key] {
						sessionMu.Unlock()
						return nil, fmt.Errorf("session %q is already closing", name)
					}
					if sessions[key] == nil {
						sessionMu.Unlock()
						return nil, fmt.Errorf("session %q does not exist", name)
					}
					if principal := mcpHTTPPrincipalFromContext(ctx); principal != "" && isDefaultSession(ctx, key) && recoveryPrincipalsStarting[principal] {
						sessionMu.Unlock()
						return nil, fmt.Errorf("a session recovery for this principal is already starting")
					}
					closingByName[key] = true
					delete(trackedNames, active)
					closeKey = key
					trackedNames[key] = struct{}{}
				}
			}
			if call.Name == "close_session" && closeKey == "" {
				sessionMu.Unlock()
				return nil, fmt.Errorf("close_session requires a valid session name")
			}
			for name := range trackedNames {
				if closingByName[name] && name != closeKey {
					sessionMu.Unlock()
					return nil, fmt.Errorf("session %q is closing; retry after cleanup completes", sessionNameForContext(name))
				}
			}
			countInFlight := true
			for name := range trackedNames {
				if countInFlight {
					inFlight[name]++
				}
				if sessions[name] != nil {
					lastActivity[name] = time.Now()
				}
			}
			sessionMu.Unlock()
			defer func() {
				sessionMu.Lock()
				for name := range trackedNames {
					if countInFlight {
						inFlight[name]--
						if inFlight[name] <= 0 {
							delete(inFlight, name)
						}
					}
					if sessions[name] != nil {
						lastActivity[name] = time.Now()
					}
				}
				if closeKey != "" {
					closingByName[closeKey] = false
				}
				sessionMu.Unlock()
			}()
			if call.Name == "status" || call.Name == "reset" || call.Name == "clone_session" ||
				call.Name == "read_page_snapshot" || call.Name == "search_page_snapshot" || call.Name == "delete_page_snapshot" {
				// These tools are routed through owner-level MCP handlers, but their
				// state belongs to one session. Pin it now so a concurrent switch
				// cannot redirect status/reset/clone work or artifact authorization.
				sessionMu.Lock()
				// `active` was captured under the initial registry lock and is also
				// the session whose in-flight count protects this request from close.
				boundKey := active
				routing := mcpRequestSession{
					ownerKey: sessionOwnerForKey(boundKey), sessionKey: boundKey,
					sessionID: sessionIDs[boundKey], session: sessions[boundKey], explicit: selectedSession != "",
				}
				sessionMu.Unlock()
				ctx = context.WithValue(ctx, mcpRequestSessionKey{}, routing)
			}
			switch call.Name {
			case "configure", "reset", "status", "list_sessions", "create_session", "switch_session", "close_session", "clone_session",
				"read_page_snapshot", "search_page_snapshot", "delete_page_snapshot":
				return next(ctx, req)
			}
			sessionMu.Lock()
			// Route to the session captured and protected by the initial registry
			// lock above. Re-reading activeNameLocked here could observe a concurrent
			// switch_session, while inFlight still protects the earlier session.
			boundKey := active
			bound := sessions[boundKey]
			if selectedSession != "" && closingByName[boundKey] {
				sessionMu.Unlock()
				return nil, fmt.Errorf("selected session is closing; retry after cleanup completes")
			}
			sessionMu.Unlock()
			if bound == nil {
				return next(ctx, req)
			}
			routing := mcpRequestSession{ownerKey: sessionOwnerForKey(boundKey), sessionKey: boundKey, sessionID: sessionIDs[boundKey], session: bound, explicit: selectedSession != ""}
			ctx = context.WithValue(ctx, mcpRequestSessionKey{}, routing)
			var expected string
			if raw := call.Arguments["target_id"]; len(raw) > 0 {
				_ = json.Unmarshal(raw, &expected)
			}
			if expected == "" {
				status, err := bound.StatusContext(ctx)
				if err != nil {
					return nil, fmt.Errorf("could not bind browser tool call to the active target: %w", err)
				}
				expected = status.ActiveTargetID
			}
			ctx = agent.WithExpectedTargetID(ctx, expected)
			return next(ctx, req)
		}
	})

	// Add machine-readable metadata to Scout's tool error results while
	// retaining the existing SCOUT_ERROR text block for backwards compatibility.
	srv.Use(func(next mcp.MiddlewareHandlerFunc) mcp.MiddlewareHandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			response, err := next(ctx, req)
			if err == nil && req != nil && req.Method == protocol.MethodToolsCall {
				attachScoutErrorMetadata(response)
			}
			return response, err
		}
	})

	// maybeNavigate navigates if a URL is provided, otherwise uses the current page.
	maybeNavigate := func(ctx context.Context, url string) error {
		if url != "" {
			_, err := s(ctx).NavigateWithPostconditionContext(ctx, url, agent.Postcondition{})
			return mcpErr(err)
		}
		return nil
	}

	// --- Configuration ---

	srv.Tool("configure").
		ClosedWorld().Idempotent().
		Description("Change browser settings without restarting. Use headless=false to see the browser window. Set allow_private_ips=true for local-dev workflows (localhost, 127.0.0.1, private IPs). Pass fresh=true to force-kill the current page and start clean (use after observing session_dead in status).").
		Handler(func(ctx context.Context, input ConfigureInput) (string, error) {
			if principal := mcpHTTPPrincipalFromContext(ctx); principal != "" && input.AllowPrivateIPs != nil && *input.AllowPrivateIPs && !principalPolicies[principal].AllowPrivateIPs {
				return "", fmt.Errorf("private and loopback destinations are disabled by this principal's server policy")
			}
			cfg := reconfigure(ctx, func(cfg *agent.SessionConfig) {
				if input.Headless != nil {
					cfg.Headless = *input.Headless
				}
				if input.AllowPrivateIPs != nil {
					cfg.AllowPrivateIPs = *input.AllowPrivateIPs
				}
			})
			if input.Fresh {
				// Eagerly create the new session so the next tool call starts clean.
				if err := ensureSession(ctx); err != nil {
					return "", err
				}
			}
			mode := "headless"
			if !cfg.Headless {
				mode = "visible"
			}
			privIPs := "blocked"
			if cfg.AllowPrivateIPs {
				privIPs = "allowed"
			}
			freshNote := ""
			if input.Fresh {
				freshNote = " (fresh browser session started)"
			}
			return fmt.Sprintf("Browser reconfigured: %s mode, private IPs %s%s. Next navigation will use the new settings.", mode, privIPs, freshNote), nil
		})

	srv.Tool("save_profile").
		ClosedWorld().
		Description("Save the current page's URL, cookies, and localStorage to a new local JSON profile file (permissions 0600). Relative paths resolve under the OS temp directory; existing destinations and protected system/credential locations are rejected. This file contains authentication state; store it securely.").
		Handler(func(ctx context.Context, input ProfilePathInput) (string, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			path, err := newMCPOutputPath(input.Path, "profile")
			if err != nil {
				return "", err
			}
			if err := s(ctx).SaveProfileContext(ctx, path); err != nil {
				return "", mcpErr(err)
			}
			return "Browser profile saved to " + path, nil
		})

	srv.Tool("load_profile").
		ClosedWorld().
		Description("Load a local JSON profile. Optionally pass target_id from an earlier result to reject the operation if Scout has switched pages, and timeout_ms to bound navigation, restore, and reload. Profile files contain secrets; use only trusted files.").
		Handler(func(ctx context.Context, input LoadProfileInput) (*agent.ProfileResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			if strings.TrimSpace(input.Path) == "" {
				return nil, fmt.Errorf("profile path is required")
			}
			result, err := s(ctx).LoadProfileWithOutcomeContext(ctx, input.Path)
			return result, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("set_viewport").
		ClosedWorld().Idempotent().
		Description("Resize the live page's viewport / emulate a device so width-based CSS @media breakpoints resolve against the given size. Pass width+height or a device preset; optionally set timeout_ms to bound application and readback. The result includes verified CSS viewport and device pixel ratio.").
		Handler(func(ctx context.Context, input SetViewportInput) (*agent.ViewportResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			w, h, scale, mobile, err := resolveViewport(input)
			if err != nil {
				return nil, err
			}
			result, err := s(ctx).SetViewportWithOutcomeContext(ctx, w, h, scale, mobile)
			return result, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("reset").
		ClosedWorld().
		Description("Force-reset the current browser session and verify the browser engine relaunched. Optionally set timeout_ms to bound shutdown and relaunch. Page creation remains lazy; the result reports whether a page is ready and its target ID when available.").
		Handler(func(ctx context.Context, input ResetInput) (*agent.ResetResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			result, err := s(bounded).ResetWithOutcomeContext(bounded)
			return result, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("help").
		ReadOnly().
		Description("Show task-oriented Scout workflows and tool names available in the selected MCP profile. Use this when unsure which tool to call; each recipe is filtered to the tools this server exposes.").
		Handler(func(ctx context.Context, _ struct{}) (map[string]any, error) {
			available := make(map[string]struct{})
			for name := range mcpAllToolNames {
				if toolFilter(ctx, name) {
					available[name] = struct{}{}
				}
			}
			contains := func(names ...string) []string {
				out := make([]string, 0, len(names))
				for _, name := range names {
					if _, ok := available[name]; ok {
						out = append(out, name)
					}
				}
				return out
			}
			workflows := make([]map[string]any, 0, 9)
			addWorkflow := func(task string, names ...string) {
				steps := contains(names...)
				if len(steps) > 0 {
					workflows = append(workflows, map[string]any{"task": task, "steps": steps})
				}
			}
			addWorkflow("inspect a page", "navigate", "observe", "extract", "readable_text")
			addWorkflow("interact with a page", "navigate", "observe", "click", "type", "fill_form_semantic", "wait_for", "observe_diff")
			addWorkflow("verify a change", "assert_text_contains", "has_element", "wait_for", "submit_outcome", "reconcile_action")
			addWorkflow("run or recover a verified task", "task_run", "recover_task_run", "resolve_task_run")
			addWorkflow("work with tabs", "list_tabs", "open_tab", "switch_tab", "close_tab")
			addWorkflow("manage browser sessions", "list_sessions", "create_session", "switch_session", "clone_session", "close_session")
			addWorkflow("recover a remote browser session across server restart", "persist_session", "resume_session", "revoke_session")
			addWorkflow("configure the browser", "status", "configure", "set_viewport", "reset")
			addWorkflow("save or restore browser state", "save_profile", "load_profile")
			addWorkflow("diagnose a failed action", "status", "observe", "console_errors", "failed_requests", "network_summary", "reconcile_action")
			addWorkflow("record or inspect a trace", "start_trace", "stop_trace", "session_history")
			return map[string]any{
				"workflows": workflows,
				"guidance": []string{
					"Use standard CSS selectors; Scout does not use Playwright selector syntax.",
					"A dispatched action or page load does not prove application acceptance; inspect outcome and verification evidence.",
					"For outcome_unknown, inspect or reconcile before retrying a non-idempotent action.",
				},
				"available_tools": contains(sortedToolNames(available)...),
			}, nil
		})

	srv.Tool("status").
		ReadOnly().
		Description("Get current browser/session health status including browser ownership and remote BrowserContext isolation, proxy configuration/application scope, active tab and CDP target ID, URL, pending requests, timeout streak, and last error. proxy_configured and proxy_applied_by_scout describe configuration only; remote_host_policy is unverified for remote CDP. Configure and verify browser-wide egress on the remote browser host. For a session-bound status read without changing the active session, pass _meta.go.klarlabs.de/scout/session with a name or opaque session_id returned by create_session/list_sessions. Names remain transport-scoped; opaque IDs resolve only for their authenticated owner (the configured principal for authenticated HTTP, otherwise the transport connection) and are not authorization credentials. Use active_target_id to correlate this snapshot with tab and action results.").
		Handler(func(ctx context.Context, input StatusInput) (*agent.SessionStatus, error) {
			return s(ctx).StatusContext(ctx)
		})

	srv.Tool("list_sessions").
		ReadOnly().
		Description("List sessions visible to this client with in-memory opaque session_id handles for per-call selection, owner_scope (stdio_process, transport, or authenticated_principal), active state, browser ownership, remote BrowserContext isolation, proxy configuration/application scope, health, recoverability, current URL, and last error. proxy_configured and proxy_applied_by_scout describe configuration only; remote_host_policy is unverified for remote CDP, whose network egress must be configured and verified on the browser host. These selector IDs are scoped to the server process and authenticated owner: a configured principal can reconnect with a new HTTP transport while the server remains alive, while unauthenticated HTTP and stdio IDs remain tied to their transport/process registry. Explicit close, idle expiry, transport deletion of the owning session, or server restart invalidates an in-memory selector ID. For durable restart recovery, use persist_session/resume_session/revoke_session with an authenticated configured principal, remote CDP, and --task-store; shared and Scout-isolated remote contexts are supported. in_flight_calls counts other browser tool calls currently keeping each session alive; list_sessions does not count itself. Cleanup fields identify pending or failed browser cleanup by session. Stateful HTTP sessions remain separately namespaced by transport; principal-owned opaque IDs permit explicit selection across that principal's transports.").
		Handler(func(ctx context.Context, input StatusInput) ([]SessionSummary, error) {
			sessionMu.Lock()
			activeName := activeNameLocked(ctx)
			names := make([]string, 0, len(sessions)+len(cleanupErrors)+len(cleanupPending)+1)
			for name := range sessions {
				if sessionVisibleToContext(ctx, name) {
					if _, idErr := ensureSessionIDLocked(name); idErr != nil {
						sessionMu.Unlock()
						return nil, idErr
					}
					names = append(names, name)
				}
			}
			for name := range cleanupErrors {
				if sessionVisibleToContext(ctx, name) {
					if _, exists := sessions[name]; !exists {
						names = append(names, name)
					}
				}
			}
			for name := range cleanupPending {
				if sessionVisibleToContext(ctx, name) {
					if _, exists := sessions[name]; !exists {
						if _, failed := cleanupErrors[name]; !failed {
							names = append(names, name)
						}
					}
				}
			}
			if _, exists := sessions[activeName]; !exists {
				if _, idErr := ensureSessionIDLocked(activeName); idErr != nil {
					sessionMu.Unlock()
					return nil, idErr
				}
				names = append(names, activeName)
			}
			visibleCount := visibleLiveSessionCountLocked(ctx)
			startingCount := startingSessions[clientIDForContext(ctx)]
			closingCount := closingSessions[clientIDForContext(ctx)]
			active := activeName
			cfg := configForContextLocked(ctx)
			snapshot := make(map[string]*agent.Session, len(sessions))
			activity := make(map[string]time.Time, len(lastActivity))
			inFlightSnapshot := make(map[string]int, len(inFlight))
			cleanupSnapshot := make(map[string]string, len(cleanupErrors))
			pendingSnapshot := make(map[string]bool, len(cleanupPending))
			for name, cleanupErr := range cleanupErrors {
				if sessionVisibleToContext(ctx, name) {
					cleanupSnapshot[name] = cleanupErr
				}
			}
			for name := range cleanupPending {
				if sessionVisibleToContext(ctx, name) {
					pendingSnapshot[name] = true
				}
			}
			for name, sess := range sessions {
				snapshot[name] = sess
			}
			for name, last := range lastActivity {
				activity[name] = last
			}
			for name, count := range inFlight {
				if sessionVisibleToContext(ctx, name) {
					inFlightSnapshot[name] = count
				}
			}
			sessionMu.Unlock()
			sort.Strings(names)
			out := make([]SessionSummary, 0, len(names))
			for _, name := range names {
				if err := ctx.Err(); err != nil {
					return nil, mcpErr(err)
				}
				inFlightCalls := inFlightSnapshot[name]
				if inFlightCalls > 0 {
					inFlightCalls-- // Exclude this list_sessions request itself.
				}
				sessionMu.Lock()
				sessionID := sessionIDs[name]
				sessionMu.Unlock()
				summary := SessionSummary{ContentTrust: agent.ContentTrustUntrustedPage, Name: sessionNameForContext(name), SessionID: sessionID, OwnerScope: mcpSessionOwnerScope(name), Active: name == active, Started: snapshot[name] != nil, Recoverable: true, SessionLimit: sessionLimit, SessionCount: visibleCount, SessionStarting: startingCount, SessionClosing: closingCount, InFlightCalls: inFlightCalls}
				summary.CleanupPending = pendingSnapshot[name]
				if cleanupErr, failed := cleanupSnapshot[name]; failed {
					summary.CleanupError = cleanupErr
				}
				if isDefaultSession(ctx, name) {
					summary.BrowserContextIsolated = cfg.IsolateRemoteBrowserContext
				}
				if idleTimeout > 0 {
					summary.IdleTimeoutMS = int64((idleTimeout + time.Millisecond - 1) / time.Millisecond)
				}
				if last, ok := activity[name]; ok {
					lastCopy := last
					summary.LastActivity = &lastCopy
					if idleTimeout > 0 {
						deadline := last.Add(idleTimeout)
						summary.IdleDeadline = &deadline
					}
				}
				if isDefaultSession(ctx, name) && cfg.RemoteCDP != "" {
					summary.BrowserEndpoint = safeEndpointLabel(cfg.RemoteCDP)
					summary.EndpointSource = "SCOUT_REMOTE_CDP"
				} else {
					summary.EndpointSource = "scout_launch"
				}
				if isDefaultSession(ctx, name) {
					summary.ProxyConfigured = cfg.ProxyServer != ""
					summary.ProxyAppliedByScout = cfg.ProxyServer != "" && cfg.RemoteCDP == ""
					if cfg.RemoteCDP != "" {
						summary.RemoteHostPolicy = "unverified"
					} else {
						summary.RemoteHostPolicy = "not_applicable"
					}
				}
				if sess := snapshot[name]; sess != nil && summary.InFlightCalls > 0 {
					summary.StatusDeferred = true
				} else if sess != nil {
					status, err := sess.StatusContext(ctx)
					if err != nil {
						return nil, mcpErr(err)
					}
					summary.BrowserAlive = status.BrowserAlive
					summary.BrowserOwnership = status.BrowserOwnership
					summary.BrowserContextIsolated = status.BrowserContextIsolated
					summary.ProxyConfigured = status.ProxyConfigured
					summary.ProxyAppliedByScout = status.ProxyAppliedByScout
					summary.RemoteHostPolicy = status.RemoteHostPolicy
					summary.SessionDead = status.SessionDead
					summary.Recoverable = status.Recoverable
					summary.CurrentURL = status.CurrentURL
					summary.LastError = status.LastError
				} else if isDefaultSession(ctx, name) {
					summary.BrowserOwnership = "remote_cdp"
					if cfg.RemoteCDP == "" {
						summary.BrowserOwnership = "scout_launched"
					}
				}
				out = append(out, summary)
			}
			return out, nil
		})

	srv.Tool("persist_session").
		ClosedWorld().
		Description("Create a durable, owner-scoped recovery handle for the selected default session. Available only to authenticated configured HTTP principals using remote CDP and --task-store. The handle reconnects only to the same live browser context and target under the current trusted policy; it is not an authorization credential.").
		Handler(func(ctx context.Context, input SessionRecoveryInput) (*SessionRecoveryResult, error) {
			if sessionRecoveryStore == nil {
				return nil, fmt.Errorf("persist_session requires --task-store")
			}
			cfg, ownerScope, err := recoveryConfigForContext(ctx)
			if err != nil {
				return nil, err
			}
			key := sessionKeyForContext(ctx, "default")
			sessionMu.Lock()
			active := activeNameLocked(ctx)
			current := sessions[key]
			sessionMu.Unlock()
			if active != key {
				return nil, fmt.Errorf("durable recovery applies only to the principal's default session")
			}
			if current == nil {
				if err := ensureSession(ctx); err != nil {
					return nil, mcpErr(err)
				}
				sessionMu.Lock()
				current = sessions[key]
				sessionMu.Unlock()
			}
			if input.ExpiresInSeconds != 0 && (input.ExpiresInSeconds < 60 || input.ExpiresInSeconds > int((30*24*time.Hour)/time.Second)) {
				return nil, fmt.Errorf("expires_in_seconds must be between 60 and 2592000")
			}
			lifetime := 24 * time.Hour
			if input.ExpiresInSeconds > 0 {
				lifetime = time.Duration(input.ExpiresInSeconds) * time.Second
			}
			digest, err := agent.SessionRecoveryPolicyDigest(cfg)
			if err != nil {
				return nil, err
			}
			handle, err := agent.NewSessionRecoveryHandle()
			if err != nil {
				return nil, err
			}
			record, err := current.PersistRecoveryRecord(ctx, sessionRecoveryStore, handle, ownerScope, "default", digest, time.Now().Add(lifetime))
			if err != nil {
				return nil, err
			}
			sessionMu.Lock()
			if sessionRecoveryHandles[key] == nil {
				sessionRecoveryHandles[key] = make(map[string]struct{})
			}
			sessionRecoveryHandles[key][handle] = struct{}{}
			sessionMu.Unlock()
			return &SessionRecoveryResult{Handle: record.Handle, SessionName: record.SessionName, TargetID: record.TargetID, ExpiresAt: record.ExpiresAt}, nil
		})

	srv.Tool("resume_session").
		ClosedWorld().
		Description("Resume a durable session handle for the authenticated configured HTTP principal. Requires --task-store and reattaches to the exact still-live remote browser context and target under the principal's current trusted policy; never launches a replacement browser. Only the default remote CDP session is supported.").
		Handler(func(ctx context.Context, input SessionRecoveryInput) (*SessionRecoveryResult, error) {
			if input.Handle == "" {
				return nil, fmt.Errorf("resume_session requires handle")
			}
			if sessionRecoveryStore == nil {
				return nil, fmt.Errorf("resume_session requires --task-store")
			}
			cfg, ownerScope, err := recoveryConfigForContext(ctx)
			if err != nil {
				return nil, err
			}
			digest, err := agent.SessionRecoveryPolicyDigest(cfg)
			if err != nil {
				return nil, err
			}
			key := sessionKeyForContext(ctx, "default")
			sessionMu.Lock()
			if routing, ok := mcpRequestSessionFromContext(ctx); ok && routing.sessionKey != "" && routing.sessionKey != key {
				sessionMu.Unlock()
				return nil, fmt.Errorf("resume_session cannot be combined with a non-default session selector")
			}
			if sessions[key] != nil || startingByName[key] != nil || shuttingDown {
				sessionMu.Unlock()
				return nil, fmt.Errorf("default session already exists or is starting")
			}
			if recoveryHandlesStarting[input.Handle] {
				sessionMu.Unlock()
				return nil, fmt.Errorf("session recovery handle is already being resumed")
			}
			principal := mcpHTTPPrincipalFromContext(ctx)
			if recoveryPrincipalsStarting[principal] {
				sessionMu.Unlock()
				return nil, fmt.Errorf("a session recovery for this principal is already starting")
			}
			for closingKey, closing := range closingByName {
				if closing && principal != "" && strings.HasPrefix(closingKey, "http/"+principal+"::") {
					sessionMu.Unlock()
					return nil, fmt.Errorf("a session for this principal is closing")
				}
			}
			for _, handles := range sessionRecoveryHandles {
				if _, exists := handles[input.Handle]; exists {
					sessionMu.Unlock()
					return nil, fmt.Errorf("session recovery handle is already attached")
				}
			}
			for activeKey, handles := range sessionRecoveryHandles {
				if len(handles) > 0 && strings.HasPrefix(activeKey, "http/"+principal+"::") {
					sessionMu.Unlock()
					return nil, fmt.Errorf("a durable session is already attached for this principal")
				}
			}
			for activeKey := range sessions {
				if activeKey != key && strings.HasPrefix(activeKey, "http/"+principal+"::") {
					sessionMu.Unlock()
					return nil, fmt.Errorf("another browser session is already active for this principal")
				}
			}
			if sessionLimit > 0 && visibleSessionCountLocked(ctx) >= sessionLimit {
				sessionMu.Unlock()
				return nil, mcpErr(sessionLimitError(sessionLimit))
			}
			pending := make(chan struct{})
			startingByName[key] = pending
			recoveryHandlesStarting[input.Handle] = true
			recoveryPrincipalsStarting[principal] = true
			startingSessions[clientIDForContext(ctx)]++
			sessionMu.Unlock()

			resumed, record, resumeErr := agent.ResumeSessionContext(ctx, cfg, sessionRecoveryStore, input.Handle, ownerScope, digest)
			sessionMu.Lock()
			startingSessions[clientIDForContext(ctx)]--
			if startingSessions[clientIDForContext(ctx)] <= 0 {
				delete(startingSessions, clientIDForContext(ctx))
			}
			delete(startingByName, key)
			delete(recoveryHandlesStarting, input.Handle)
			delete(recoveryPrincipalsStarting, principal)
			close(pending)
			if resumeErr != nil {
				sessionMu.Unlock()
				return nil, resumeErr
			}
			if ctx.Err() != nil || shuttingDown || sessions[key] != nil {
				sessionMu.Unlock()
				closeErr := resumed.Close()
				return nil, errors.Join(fmt.Errorf("session resume was interrupted; retry with the same handle"), closeErr)
			}
			if _, idErr := ensureSessionIDLocked(key); idErr != nil {
				sessionMu.Unlock()
				closeErr := resumed.Close()
				return nil, errors.Join(idErr, closeErr)
			}
			sessions[key] = resumed
			if sessionRecoveryHandles[key] == nil {
				sessionRecoveryHandles[key] = make(map[string]struct{})
			}
			sessionRecoveryHandles[key][input.Handle] = struct{}{}
			lastActivity[key] = time.Now()
			setActiveNameLocked(ctx, key)
			sessionMu.Unlock()
			return &SessionRecoveryResult{SessionName: record.SessionName, TargetID: record.TargetID, ExpiresAt: record.ExpiresAt, Resumed: true}, nil
		})

	srv.Tool("revoke_session").
		ClosedWorld().
		Description("Revoke an owner-scoped durable session recovery handle. Unknown or foreign handles are indistinguishable and return revoked=false.").
		Handler(func(ctx context.Context, input SessionRecoveryInput) (*SessionRecoveryResult, error) {
			if input.Handle == "" {
				return nil, fmt.Errorf("revoke_session requires handle")
			}
			if sessionRecoveryStore == nil {
				return nil, fmt.Errorf("revoke_session requires --task-store")
			}
			cfg, ownerScope, err := recoveryConfigForContext(ctx)
			if err != nil {
				return nil, err
			}
			sessionMu.Lock()
			if recoveryHandlesStarting[input.Handle] {
				sessionMu.Unlock()
				return nil, fmt.Errorf("session recovery handle is already being resumed or revoked")
			}
			recoveryHandlesStarting[input.Handle] = true
			sessionMu.Unlock()
			defer func() {
				sessionMu.Lock()
				delete(recoveryHandlesStarting, input.Handle)
				sessionMu.Unlock()
			}()
			revoked, err := agent.RevokeSessionRecoveryHandleWithConfig(ctx, cfg, sessionRecoveryStore, input.Handle, ownerScope)
			if err != nil {
				return nil, err
			}
			sessionMu.Lock()
			for key, handles := range sessionRecoveryHandles {
				delete(handles, input.Handle)
				if len(handles) == 0 {
					delete(sessionRecoveryHandles, key)
				}
			}
			sessionMu.Unlock()
			return &SessionRecoveryResult{SessionName: "default", Revoked: revoked}, nil
		})

	srv.Tool("create_session").
		ClosedWorld().
		Description("Create a new isolated local browser session and make it active. The result includes an opaque session_id for explicit per-call selection. Optionally narrow allowed_origins to a subset of the server or principal policy; this immutable session policy cannot broaden that policy. IDs belong to this server process and authenticated owner registry; they are not authorization credentials and do not survive process restart. A configured HTTP principal can use the ID from a replacement transport while this server process remains alive. Named sessions use separate local browser processes, even when the default session attaches to remote CDP. Session names are 1-64 letters, numbers, hyphens, underscores, or periods.").
		Handler(func(ctx context.Context, input CreateSessionInput) (string, error) {
			name, err := validateSessionName(input.Name)
			if err != nil {
				return "", err
			}
			key := sessionKeyForContext(ctx, name)
			sessionMu.Lock()
			if cleanupErrors[key] != "" {
				sessionMu.Unlock()
				return "", fmt.Errorf("session %q has unresolved browser cleanup; restart the Scout server before reusing this name", name)
			}
			if closingByName[key] {
				sessionMu.Unlock()
				return "", fmt.Errorf("session %q is closing", name)
			}
			if sessions[key] != nil {
				sessionMu.Unlock()
				return "", fmt.Errorf("session %q already exists", name)
			}
			if startingByName[key] != nil {
				sessionMu.Unlock()
				return "", fmt.Errorf("session %q is already being created", name)
			}
			if sessionLimit > 0 && visibleSessionCountLocked(ctx) >= sessionLimit {
				sessionMu.Unlock()
				return "", mcpErr(sessionLimitError(sessionLimit))
			}
			pending := make(chan struct{})
			startingByName[key] = pending
			startingSessions[clientIDForContext(ctx)]++
			cfg := configForContextLocked(ctx)
			cfg = scopeTaskRunStore(ctx, key, cfg)
			if len(input.AllowedOrigins) > 0 {
				requested, originErr := parseMCPAllowedOrigins(strings.Join(input.AllowedOrigins, ","))
				if originErr != nil {
					startingSessions[clientIDForContext(ctx)]--
					delete(startingByName, key)
					close(pending)
					sessionMu.Unlock()
					return "", mcpPolicyError(
						"SCOUT_ORIGIN_POLICY_INVALID",
						originErr.Error(),
						"Provide exact HTTP(S) origins without credentials, paths, queries, fragments, or wildcard hosts.",
					)
				}
				if len(cfg.AllowedOrigins) > 0 && !mcpOriginSubset(requested, cfg.AllowedOrigins) {
					startingSessions[clientIDForContext(ctx)]--
					delete(startingByName, key)
					close(pending)
					sessionMu.Unlock()
					return "", mcpPolicyError(
						"SCOUT_ORIGIN_POLICY_REJECTED",
						"session allowed_origins must be a subset of the server or principal allowlist",
						"Request only origins already permitted by the effective server or principal policy.",
					)
				}
				cfg.AllowedOrigins = requested
			}
			revision := configRevisionForContextLocked(ctx)
			cfg = localNamedSessionConfig(cfg, mcpHTTPPrincipalFromContext(ctx), principalPolicies, configuredProxyServer())
			sessionMu.Unlock()
			created, err := agent.NewSessionContext(ctx, cfg)
			sessionMu.Lock()
			startingSessions[clientIDForContext(ctx)]--
			delete(startingByName, key)
			close(pending)
			if err != nil {
				sessionMu.Unlock()
				return "", err
			}
			if configRevisionForContextLocked(ctx) != revision {
				beginSessionCloseLocked(key)
				sessionMu.Unlock()
				finishSessionClose(key, created)
				return "", fmt.Errorf("browser configuration changed while session was starting; retry the operation")
			}
			if sessions[key] != nil {
				beginSessionCloseLocked(key)
				sessionMu.Unlock()
				finishSessionClose(key, created)
				return "", fmt.Errorf("session %q was created concurrently", name)
			}
			sessionID, idErr := ensureSessionIDLocked(key)
			if idErr != nil {
				beginSessionCloseLocked(key)
				sessionMu.Unlock()
				finishSessionClose(key, created)
				return "", idErr
			}
			sessions[key] = created
			lastActivity[key] = time.Now()
			setActiveNameLocked(ctx, key)
			sessionMu.Unlock()
			return "Created and activated session " + name + " (session_id: " + sessionID + ")", nil
		})

	srv.Tool("switch_session").
		ClosedWorld().
		Description("Make one of this client's named browser sessions active using its name or session_id. Subsequent browser tools operate on that session. Opaque IDs are owner-scoped and are not authorization credentials.").
		Handler(func(ctx context.Context, input SessionNameInput) (string, error) {
			name := input.Name
			var err error
			if !validMCPOpaqueSessionID(name) {
				name, err = validateSessionName(name)
			}
			if err != nil {
				return "", err
			}
			sessionMu.Lock()
			key, resolveErr := resolveSessionSelectorLocked(ctx, name)
			if resolveErr != nil {
				sessionMu.Unlock()
				return "", resolveErr
			}
			if closingByName[key] {
				sessionMu.Unlock()
				return "", fmt.Errorf("session %q is closing", name)
			}
			if sessions[key] == nil {
				if name != "default" {
					sessionMu.Unlock()
					return "", mcpErr(fmt.Errorf("session %q does not exist for this client", name))
				}
				setActiveNameLocked(ctx, key)
				lastActivity[key] = time.Now()
				sessionMu.Unlock()
				if err := ensureSession(ctx); err != nil {
					return "", err
				}
				return "Switched to session " + name, nil
			}
			setActiveNameLocked(ctx, key)
			lastActivity[key] = time.Now()
			sessionMu.Unlock()
			return "Switched to session " + name, nil
		})

	srv.Tool("close_session").
		ClosedWorld().
		Description("Close and remove a named browser session by name or session_id after its in-flight tool calls drain. New calls are rejected while closing. Opaque IDs are owner-scoped and are not authorization credentials. The result separately reports registry removal and browser-handle cleanup; a cleanup error does not restore the removed session. Returns the active session selected after removal.").
		Handler(func(ctx context.Context, input SessionNameInput) (*CloseSessionResult, error) {
			if err := ctx.Err(); err != nil {
				return nil, mcpErr(err)
			}
			name := input.Name
			if !validMCPOpaqueSessionID(name) {
				var err error
				name, err = validateSessionName(name)
				if err != nil {
					return nil, err
				}
			}
			sessionMu.Lock()
			key, resolveErr := resolveSessionSelectorLocked(ctx, name)
			if resolveErr != nil {
				sessionMu.Unlock()
				return nil, resolveErr
			}
			name = sessionNameForContext(key)
			sessionMu.Unlock()
			// The close_session request itself is included in middleware's
			// in-flight count. Wait until it is the only call before removing and
			// closing the session, so a concurrent browser operation can finish.
			drainTicker := time.NewTicker(10 * time.Millisecond)
			defer drainTicker.Stop()
			for {
				sessionMu.Lock()
				busy := inFlight[key] > 1
				if !busy {
					sessionMu.Unlock()
					break
				}
				sessionMu.Unlock()
				select {
				case <-ctx.Done():
					return nil, mcpErr(fmt.Errorf("close session %q: waiting for in-flight calls: %w", name, ctx.Err()))
				case <-drainTicker.C:
				}
			}
			sessionMu.Lock()
			if err := ctx.Err(); err != nil {
				sessionMu.Unlock()
				return nil, mcpErr(err)
			}
			closing := sessions[key]
			if closing == nil {
				sessionMu.Unlock()
				return nil, fmt.Errorf("session %q does not exist", name)
			}
			beginSessionCloseLocked(key)
			delete(sessions, key)
			recoveryHandles := sessionRecoveryHandles[key]
			delete(sessionRecoveryHandles, key)
			revokeSessionIDLocked(key)
			delete(lastActivity, key)
			activeName := activeNameLocked(ctx)
			if activeName == key {
				defaultKey := sessionKeyForContext(ctx, "default")
				if sessions[defaultKey] != nil {
					activeName = defaultKey
				} else {
					others := make([]string, 0, len(sessions))
					for n := range sessions {
						if sessionVisibleToContext(ctx, n) {
							others = append(others, n)
						}
					}
					activeName = defaultKey
					others = append(others, activeName)
					sort.Strings(others)
					if len(others) > 0 {
						activeName = others[0]
					}
				}
			}
			setActiveNameLocked(ctx, activeName)
			active := sessionNameForContext(activeName)
			remaining := 0
			for sessionKey := range sessions {
				if sessionVisibleToContext(ctx, sessionKey) {
					remaining++
				}
			}
			sessionMu.Unlock()
			closeErr := closeBrowserSession(closing)
			// The process-local handle index is only a diagnostic aid: durable
			// recovery records can outlive a server restart. Always enumerate the
			// authenticated owner's default-session records on close.
			if sessionRecoveryStore != nil && mcpHTTPPrincipalFromContext(ctx) != "" && isDefaultSession(ctx, key) {
				revokeCtx, revokeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				cfg, ownerScope, configErr := recoveryConfigForContext(ctx)
				deleted := 0
				revokeErr := configErr
				if revokeErr == nil {
					deleted, revokeErr = agent.RevokeSessionRecoveryRecordsForSessionWithConfig(revokeCtx, cfg, sessionRecoveryStore, ownerScope, "default")
				}
				revokeCancel()
				if revokeErr != nil {
					closeErr = errors.Join(closeErr, revokeErr)
				} else if deleted != len(recoveryHandles) {
					fmt.Fprintf(os.Stderr, "scout: closed session revoked %d durable recovery handle(s), process index had %d\n", deleted, len(recoveryHandles))
				}
			}
			cleanupSessionArtifacts(key)
			sessionMu.Lock()
			finishSessionCloseLocked(key, closeErr)
			sessionMu.Unlock()
			result := &CloseSessionResult{
				Action: "close_session", Outcome: string(agent.OutcomePostconditionVerified), Verified: closeErr == nil,
				RegistryRemoved: true, CleanupCompleted: closeErr == nil,
				ClosedSession: name, ActiveSession: active, RemainingSessions: remaining,
				Evidence: "session removed from the server registry; active session selected atomically",
			}
			if closeErr != nil {
				result.Outcome = string(agent.OutcomeActionFailed)
				result.CleanupError = closeErr.Error()
				result.Evidence += "; browser-handle cleanup returned an error"
			} else {
				result.Evidence += "; browser-handle cleanup completed"
			}
			return result, nil
		})

	srv.Tool("clone_session").
		ClosedWorld().
		Description("Clone the active page URL, cookies, and localStorage into a new isolated local browser session and activate it. This starts a separate Chrome process; close it with close_session when finished. Abandoned sessions expire after SCOUT_SESSION_IDLE_TIMEOUT (one hour by default). This copies authentication state, not open tabs or sessionStorage. Use a trusted destination session name.").
		Handler(func(ctx context.Context, input SessionNameInput) (string, error) {
			name, err := validateSessionName(input.Name)
			if err != nil {
				return "", err
			}
			key := sessionKeyForContext(ctx, name)
			sessionMu.Lock()
			if closingByName[key] || sessions[key] != nil || activeNameLocked(ctx) == key || startingByName[key] != nil {
				sessionMu.Unlock()
				return "", fmt.Errorf("session %q already exists or is already active", name)
			}
			cfg := configForContextLocked(ctx)
			cfg = scopeTaskRunStore(ctx, key, cfg)
			revision := configRevisionForContextLocked(ctx)
			cfg.RemoteCDP = "" // The clone is a separate local browser context.
			sessionMu.Unlock()
			profile, err := s(ctx).CaptureProfileContext(ctx)
			if err != nil {
				return "", err
			}
			if profile.URL == "" || profile.URL == "about:blank" {
				return "", fmt.Errorf("navigate the active session to a page before cloning it")
			}
			sessionMu.Lock()
			if sessions[key] != nil || startingByName[key] != nil {
				sessionMu.Unlock()
				return "", fmt.Errorf("session %q was created concurrently", name)
			}
			if sessionLimit > 0 && visibleSessionCountLocked(ctx) >= sessionLimit {
				sessionMu.Unlock()
				return "", mcpErr(sessionLimitError(sessionLimit))
			}
			pending := make(chan struct{})
			startingByName[key] = pending
			startingSessions[clientIDForContext(ctx)]++
			sessionMu.Unlock()
			cloned, err := agent.NewSessionContext(ctx, cfg)
			if err != nil {
				sessionMu.Lock()
				startingSessions[clientIDForContext(ctx)]--
				delete(startingByName, key)
				close(pending)
				sessionMu.Unlock()
				return "", err
			}
			if _, err = cloned.NavigateWithPostconditionContext(ctx, profile.URL, agent.Postcondition{}); err == nil {
				err = cloned.ApplyProfileContext(ctx, profile)
			}
			if err == nil {
				_, err = cloned.ReloadContext(ctx, false)
			}
			// A canceled request can race with the last CDP operation returning
			// successfully. Do not publish a browser the caller was told did not
			// finish creating; the error path below closes its Chrome process and
			// removes its temporary profile.
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				closeErr := closeBrowserSession(cloned)
				sessionMu.Lock()
				startingSessions[clientIDForContext(ctx)]--
				delete(startingByName, key)
				close(pending)
				sessionMu.Unlock()
				return "", errors.Join(fmt.Errorf("failed to clone browser profile: %w", err), closeErr)
			}
			sessionMu.Lock()
			startingSessions[clientIDForContext(ctx)]--
			delete(startingByName, key)
			close(pending)
			if err := ctx.Err(); err != nil {
				beginSessionCloseLocked(key)
				sessionMu.Unlock()
				finishSessionClose(key, cloned)
				return "", err
			}
			if configRevisionForContextLocked(ctx) != revision {
				beginSessionCloseLocked(key)
				sessionMu.Unlock()
				finishSessionClose(key, cloned)
				return "", fmt.Errorf("browser configuration changed while session was cloning; retry the operation")
			}
			if sessions[key] != nil {
				beginSessionCloseLocked(key)
				sessionMu.Unlock()
				finishSessionClose(key, cloned)
				return "", fmt.Errorf("session %q was created concurrently", name)
			}
			sessionID, idErr := ensureSessionIDLocked(key)
			if idErr != nil {
				beginSessionCloseLocked(key)
				sessionMu.Unlock()
				finishSessionClose(key, cloned)
				return "", idErr
			}
			sessions[key] = cloned
			lastActivity[key] = time.Now()
			// Do not let a slow clone completion overwrite a newer explicit
			// switch_session request. The clone still activates itself when the
			// selected source was active at request entry.
			routing, routed := mcpRequestSessionFromContext(ctx)
			if !routed || activeNameLocked(ctx) == routing.sessionKey {
				setActiveNameLocked(ctx, key)
			}
			sessionMu.Unlock()
			return "Cloned authentication state into and activated session " + name + " (session_id: " + sessionID + ")", nil
		})

		// --- Navigation & Observation ---

	srv.Tool("navigate").
		OpenWorld().
		Description("Navigate to a URL and report the final URL after navigation completes. Results include target_id and distinguish navigation completion from postcondition verification. Without a postcondition, page loading alone does not prove application success. Optionally set timeout_ms for a per-action deadline; after an uncertain navigation, provide an expected_* condition so the recovery record can be reconciled. Optionally pass exactly one postcondition: expected_text, expected_url_contains, expected_visible_selector, expected_value_selector with expected_value, or expected_count_selector with expected_count.").
		Handler(func(ctx context.Context, input NavigateInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			progress := mcp.ProgressFromContext(ctx)
			total := 3.0
			_ = progress.ReportWithMessage(1, &total, "Launching browser...")
			condition := input.Postcondition()
			result, err := s(ctx).NavigateWithPostconditionContext(ctx, input.URL, condition)
			if err != nil {
				return nil, mcpErrWithActionTimeout(err, input.TimeoutMS)
			}
			_ = progress.ReportWithMessage(2, &total, "Page loaded")
			_ = progress.ReportWithMessage(3, &total, "Done")
			// Notify client that available tools may have changed based on page content
			if sess := mcp.SessionFromContext(ctx); sess != nil {
				_ = sess.NotifyToolListChanged()
			}
			// Push page info via channel if supported
			if ch := mcp.ChannelFromContext(ctx); ch != nil {
				_ = ch.SendText("scout.navigation", fmt.Sprintf("Navigated to %s — %s", result.URL, result.Title))
			}
			return result, nil
		})

	srv.Tool("back").
		OpenWorld().
		Description("Navigate to the previous page in browser history (like clicking the browser Back button). Optionally pass target_id from an earlier result to reject action if another page is active. Errors if there is no previous entry.").
		Handler(func(ctx context.Context, input struct {
			TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
		}) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).GoBackContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("forward").
		OpenWorld().
		Description("Navigate to the next page in browser history (like clicking the browser Forward button). Optionally pass target_id from an earlier result to reject action if another page is active. Errors if there is no next entry.").
		Handler(func(ctx context.Context, input struct {
			TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
		}) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).GoForwardContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("reload").
		OpenWorld().
		Description("Reload the current page. Optionally pass target_id from an earlier result to reject action if another page is active. Set ignore_cache=true for a hard reload that bypasses the browser cache.").
		Handler(func(ctx context.Context, input struct {
			IgnoreCache bool   `json:"ignore_cache,omitempty" jsonschema:"description=Bypass the browser cache (hard reload)."`
			TargetID    string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
		}) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ReloadContext(ctx, input.IgnoreCache)
			return out, mcpErr(err)
		})

	srv.Tool("observe").
		ReadOnly().
		OutputSchema(agent.Observation{}).
		Description("Get a structured snapshot of the current page. Optionally pass url to navigate first.").
		Handler(func(ctx context.Context, input struct {
			URL      string `json:"url,omitempty" jsonschema:"description=Optional URL to navigate before observing"`
			TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects navigation or observation if Scout has switched pages"`
		}) (*agent.Observation, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			if input.URL != "" {
				if _, err := s(ctx).NavigateWithPostconditionContext(ctx, input.URL, agent.Postcondition{}); err != nil {
					return nil, mcpErr(err)
				}
			}
			out, err := s(ctx).ObserveContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("reconcile_action").
		ReadOnly().
		OutputSchema(agent.PageResult{}).
		Description("Safely reconcile an action reported with outcome_unknown. Pass Scout's versioned recovery_record when available, or copy target_id, action, target, and expected_postcondition from the error. A recovery_record may cross a Scout session/process boundary; Scout retains its run ID as provenance and verifies the saved condition without repeating the action. Page conditions are checked on the exact same live browser target and may check text, exact URL, URL substring, page title, selector existence or visibility, value, or selector count. For close_tab, target_absent checks the original CDP target against browser target inventory; a present target remains unknown. A matching target ID alone does not prove the page contents are unchanged.").
		Handler(func(ctx context.Context, input ReconcileActionInput) (*agent.PageResult, error) {
			if input.RecoveryRecord != nil {
				result, err := s(ctx).ReconcileActionRecoveryRecordContext(ctx, *input.RecoveryRecord)
				return result, mcpErr(err)
			}
			var outcome *agent.ActionOutcomeError
			if input.TargetID == "" || input.Action == "" {
				return nil, mcpErr(fmt.Errorf("provide a recovery_record or target_id, action, and a non-empty expected_postcondition"))
			}
			if err := input.ExpectedPostcondition.Validate(); err != nil {
				return nil, mcpErr(err)
			}
			outcome = &agent.ActionOutcomeError{
				Action: input.Action, Target: input.Target, TargetID: input.TargetID,
				Outcome: agent.OutcomeUnknown, ExpectedPostcondition: &input.ExpectedPostcondition,
			}
			result, err := s(ctx).ReconcileActionOutcomeContext(ctx, outcome)
			return result, mcpErr(err)
		})

	srv.Tool("observe_diff").
		ReadOnly().
		Description("Return only page changes since the last observation. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages.").
		Handler(func(ctx context.Context, input ObserveInput) (*ObserveDiffResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			obs, diff, err := s(ctx).ObserveDiffContext(ctx)
			if err != nil {
				return nil, mcpErr(err)
			}
			return &ObserveDiffResult{ContentTrust: agent.ContentTrustUntrustedPage, Observation: obs, Diff: diff}, nil
		})

	srv.Tool("observe_with_budget").
		ReadOnly().
		Description("Observe the page within an approximate token budget measured from the serialized observation object at roughly 4 JSON bytes per token. The response reports budget, budget_used, and budget_truncated; target identity and completeness counts are retained even when fixed metadata exceeds very small budgets. The separate MCP response-size limit covers protocol framing. Optional target_id from an earlier result rejects the read if Scout has switched pages.").
		Handler(func(ctx context.Context, input ObserveWithBudgetInput) (*agent.Observation, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ObserveWithBudgetContext(ctx, input.Budget)
			return out, mcpErr(err)
		})

	srv.Tool("observe_scoped").
		ReadOnly().
		OutputSchema(agent.Observation{}).
		Description("Observe a subset of the page. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages. Limit by landmark roles (nav/main/footer/...) and/or cap text length and element counts. Reports which selectors matched and which did not; invalid CSS selectors return an error. Set allow_empty when all requested scopes may be absent.").
		Handler(func(ctx context.Context, input ObserveScopedInput) (*agent.Observation, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ObserveScopedContext(ctx, agent.ObserveOptions{
				Sections:     input.Sections,
				AllowEmpty:   input.AllowEmpty,
				LimitChars:   input.LimitChars,
				LinksLimit:   input.LinksLimit,
				InputsLimit:  input.InputsLimit,
				ButtonsLimit: input.ButtonsLimit,
			})
			return out, mcpErr(err)
		})

	srv.Tool("submit_outcome").
		ReadOnly().
		OutputSchema(agent.SubmitOutcome{}).
		Description("Diagnose silent form-submit failures. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages. Returns defaultPrevented for the latest submit, visible [role=alert] messages, aria-invalid field labels, dev-server error overlay text, XHR count since tracker install, and whether navigation committed. Auto-installs the tracker on first call.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.SubmitOutcome, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).LastSubmitOutcomeContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("install_submit_tracker").
		Description("Pre-install the submit tracker so the next submit event records defaultPrevented + XHR count. Call after navigate, before triggering a form submit, if you plan to read submit_outcome afterwards.").
		Handler(func(ctx context.Context, input struct {
			TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the operation if Scout has switched pages"`
		}) (string, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			if err := s(ctx).InstallSubmitTrackerContext(ctx); err != nil {
				return "", mcpErr(err)
			}
			return "submit tracker installed", nil
		})

	srv.Tool("wait_for_navigation").
		ReadOnly().
		OutputSchema(agent.NavigationOutcome{}).
		Description("Wait for the page to navigate. Optionally pass target_id from an earlier result to reject the wait if Scout has switched pages. mode: 'full' (real document load) | 'spa' (History API push/replace/popstate) | 'any' (default, first to fire). Use when an external action — a button outside scout's control, a JS-triggered route change — moves the page; click(wait=true) covers the case where scout is the one triggering.").
		Handler(func(ctx context.Context, input WaitForNavigationInput) (*agent.NavigationOutcome, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).WaitForNavigationContext(ctx, input.Mode, input.TimeoutMs)
			return out, mcpErr(err)
		})

	srv.Tool("aria_violations").
		ReadOnly().
		OutputSchema(agent.AriaViolationReport{}).
		Description("Minimal accessibility scan grouped by impact (critical/serious/moderate/minor). Optionally pass target_id from an earlier result to reject the scan if Scout has switched pages. Returns at most 500 findings with total and truncated metadata. Covers image-alt, button-name, link-name, label, duplicate-id, html-has-lang, landmark-one-main. Zero dependency — for full WCAG coverage run axe-core in your test pipeline.").
		Handler(func(ctx context.Context, input AriaViolationsInput) (*agent.AriaViolationReport, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).AriaViolationsContext(ctx)
			return out, mcpErr(err)
		})

		// --- Interaction ---

	srv.Tool("click").
		Description("Click an element by CSS selector. Pass target_id from an earlier Scout result to bind the click to that page; Scout rejects it if the active page changed. Set wait=true for navigation clicks. Optionally set timeout_ms for a per-action deadline; if the click may have dispatched, Scout returns outcome_unknown with a recovery_record that can be passed to reconcile_action before retrying. Results include target_id (the CDP page target), frame_id when selected, outcome, evidence, and verified. Without a postcondition, click dispatch does not prove the application effect and verified is false. Optionally pass exactly one postcondition: expected_text, expected_url_contains, expected_visible_selector, expected_value_selector with expected_value, or expected_count_selector with expected_count.").
		Handler(func(ctx context.Context, input ClickInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ClickWithPostconditionContext(ctx, input.Selector, input.Postcondition(), input.Wait)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("click_label").
		Description("Click an element by its label number from annotated_screenshot. Optionally set timeout_ms; an interrupted dispatched click returns outcome_unknown for safe reconciliation. Results include target_id and distinguish a dispatched click from a verified postcondition. Without a postcondition, the application effect is unverified. Optionally pass one text, URL, visibility, exact value, or exact selector-count postcondition to verify the resulting page state.").
		Handler(func(ctx context.Context, input ClickLabelInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			label, err := strconv.Atoi(input.Label.String())
			if err != nil {
				return nil, fmt.Errorf("label must be a number (e.g. 8), got %q", input.Label.String())
			}
			out, err := s(ctx).ClickLabelWithPostconditionContext(ctx, label, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("click_handle").
		Description("Click a stable node_handle from annotated_screenshot or auto_extract. Handles continue to identify the same attached DOM node after reordering; they become stale when that node is replaced or removed. Pass the source target_id and frame_id so Scout rejects a tab or frame change instead of resolving the handle in the wrong context. Optionally set timeout_ms; an interrupted dispatched click returns outcome_unknown for safe reconciliation. Results distinguish dispatch from a verified postcondition; without one, the application effect is unverified. Optionally pass one text, URL, visibility, exact value, or exact selector-count postcondition.").
		Handler(func(ctx context.Context, input ClickHandleInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			out, err := s(ctx).ClickByHandleWithIdentityContext(ctx, input.Handle, input.TargetID, input.FrameID, input.Postcondition())
			if err != nil {
				return nil, mcpErrWithActionTimeout(err, input.TimeoutMS)
			}
			return out, nil
		})

	srv.Tool("click_text").
		Description("Click an element by its visible text. Resolution: aria-label exact match, then button/link text match, then closest interactive ancestor of a matching text node. Pass role=\"button\"|\"link\" to disambiguate when text appears in both. Optionally set timeout_ms; an interrupted dispatched click returns outcome_unknown for safe reconciliation. Results include target_id and distinguish dispatch from verified page state; without a postcondition, the application effect is unverified. Optionally pass one text, URL, visibility, exact value, or exact selector-count postcondition.").
		Handler(func(ctx context.Context, input ClickTextInput) (*agent.ClickTextResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ClickTextWithPostconditionContext(ctx, input.Text, input.Role, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("type").
		Description("Type text into an input element. Clears existing value first. Optionally set timeout_ms for a per-action deadline; if input may have been sent, Scout returns outcome_unknown with the requested value for safe reconciliation. The result includes target_id and reports value_verified or value_mismatch based on a live DOM readback. This verifies the field value, not that the application accepted or saved it.").
		Handler(func(ctx context.Context, input TypeInput) (*agent.ElementResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).TypeContext(ctx, input.Selector, input.Text)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("press_key").
		Description("Press a key or modifier chord such as Enter, ArrowDown, Control+A, or Meta+s on the currently focused element. This dispatches browser key events; without a page postcondition the application effect is unverified. Optionally verify expected_text, expected_url_contains, expected_title, expected_visible_selector, expected_value_selector with expected_value, or expected_count_selector with expected_count. Optionally set timeout_ms; interruption after dispatch returns outcome_unknown.").
		Handler(func(ctx context.Context, input PressKeyInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).PressKeyWithPostconditionContext(ctx, input.Key, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	for _, keyTool := range []struct {
		name string
		down bool
	}{{"key_down", true}, {"key_up", false}} {
		keyTool := keyTool
		srv.Tool(keyTool.name).
			Description("Hold or release one key on the currently focused page. Pair key_down with key_up and always release held keys when a workflow exits. Key state changes report dispatch, not application acceptance.").
			Handler(func(ctx context.Context, input KeyControlInput) (*agent.PageResult, error) {
				var cancel context.CancelFunc
				var err error
				ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
				if err != nil {
					return nil, mcpErr(err)
				}
				defer cancel()
				ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
				if keyTool.down {
					return s(ctx).KeyDownContext(ctx, input.Key)
				}
				return s(ctx).KeyUpContext(ctx, input.Key)
			})
	}

	srv.Tool("fill_form").
		Description("Fill multiple form fields at once. Optionally set timeout_ms to bound the complete transaction. Each field reports value_verified, value_mismatch, or action_failed, plus requested and observed values and recovery guidance. Verified field values do not prove that the application accepted or saved the form.").
		Handler(func(ctx context.Context, input FillFormInput) (*agent.FormResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			out, err := s(ctx).FillFormContext(ctx, input.Fields)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("fill_form_semantic").
		Description("Fill form fields by label or name. Optionally set timeout_ms to bound the complete transaction. Strings fill text inputs; booleans toggle checkboxes; for radios pass the label/value as a string. Per-field success requires the requested state to match live readback; mismatches include the observed value, outcome, and recovery guidance. Match-confidence warnings are reported separately.").
		Handler(func(ctx context.Context, input FillFormSemanticInput) (*agent.SemanticFillResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			out, err := s(ctx).FillFormSemanticAnyContext(ctx, input.Fields)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("dispatch_event").
		Description("Dispatch a DOM event on an element. Optionally set timeout_ms; if dispatch may have occurred before interruption, Scout returns outcome_unknown for safe reconciliation. Event receipt alone does not verify the application effect; optionally pass one text, URL, visibility, exact value, or exact selector-count postcondition. Submit uses requestSubmit; click invokes el.click().").
		Handler(func(ctx context.Context, input DispatchEventInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).DispatchEventWithPostconditionContext(ctx, input.Selector, input.EventType, input.Detail, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("submit_form").
		Description("Submit a form using form.requestSubmit() — fires Vue @submit.prevent / React onSubmit / native @submit listeners and runs HTML5 validation. Results include target_id, dispatch and acceptance evidence, and an outcome. Pass the form selector or any selector inside it. If match_url is set, waits for an XHR/fetch whose URL contains that substring and returns its status code. expected_* conditions verify a page predicate; acceptance_* conditions explicitly assert application acceptance and require both an observed response and matching page state. A dispatched submit or successful field readback alone does not prove the server accepted the change.").
		Handler(func(ctx context.Context, input struct {
			Selector                  string `json:"selector" jsonschema:"required,description=CSS selector of the <form> element or any element inside it."`
			TargetID                  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects submission if Scout has switched pages"`
			ActionTimeoutMS           int    `json:"action_timeout_ms,omitempty" jsonschema:"description=Optional deadline for the entire Scout form-submission action. If expiry follows submit dispatch, Scout returns outcome_unknown with the supplied expected_* or acceptance_* page condition for reconciliation."`
			MatchURL                  string `json:"match_url,omitempty" jsonschema:"description=Optional URL substring. When set, waits for an XHR/fetch whose URL contains this and reports its status."`
			TimeoutMS                 int    `json:"timeout_ms,omitempty" jsonschema:"description=Hard cap to wait for response/navigation in ms. Default 8000."`
			ExpectedText              string `json:"expected_text,omitempty" jsonschema:"description=Optional text expected after submission; verifies application acceptance only when observed."`
			ExpectedURLContains       string `json:"expected_url_contains,omitempty" jsonschema:"description=Optional URL substring expected after submission."`
			ExpectedVisibleSelector   string `json:"expected_visible_selector,omitempty" jsonschema:"description=Optional selector that must be visible after submission."`
			ExpectedValueSelector     string `json:"expected_value_selector,omitempty" jsonschema:"description=Optional input/contenteditable selector whose value must match expected_value."`
			ExpectedValue             string `json:"expected_value,omitempty" jsonschema:"Exact value expected for expected_value_selector, including empty string."`
			ExpectedCountSelector     string `json:"expected_count_selector,omitempty" jsonschema:"Optional selector whose match count must equal expected_count."`
			ExpectedCount             *int   `json:"expected_count,omitempty" jsonschema:"Exact non-negative match count for expected_count_selector."`
			AcceptanceText            string `json:"acceptance_text,omitempty" jsonschema:"Optional application acceptance text; requires an observed response and matching page text."`
			AcceptanceVisibleSelector string `json:"acceptance_visible_selector,omitempty" jsonschema:"Optional application acceptance selector; requires an observed response and visible selector."`
			AcceptanceValueSelector   string `json:"acceptance_value_selector,omitempty" jsonschema:"Optional application acceptance input/contenteditable selector."`
			AcceptanceValue           string `json:"acceptance_value,omitempty" jsonschema:"Exact value expected for acceptance_value_selector."`
			AcceptanceCountSelector   string `json:"acceptance_count_selector,omitempty" jsonschema:"Optional selector whose exact count is required for application acceptance."`
			AcceptanceCount           *int   `json:"acceptance_count,omitempty" jsonschema:"Exact non-negative match count required for application acceptance."`
		}) (*agent.SubmitFormResult, error) {
			var cancel context.CancelFunc
			var timeoutErr error
			ctx, cancel, timeoutErr = withMCPActionTimeout(ctx, input.ActionTimeoutMS)
			if timeoutErr != nil {
				return nil, mcpErr(timeoutErr)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			acceptance := agent.Postcondition{Text: input.AcceptanceText, VisibleSelector: input.AcceptanceVisibleSelector, ValueSelector: input.AcceptanceValueSelector, Value: input.AcceptanceValue, CountSelector: input.AcceptanceCountSelector, Count: input.AcceptanceCount}
			acceptanceConditions := 0
			for _, set := range []bool{
				input.AcceptanceText != "",
				input.AcceptanceVisibleSelector != "",
				input.AcceptanceValueSelector != "" || input.AcceptanceValue != "",
				input.AcceptanceCountSelector != "" || input.AcceptanceCount != nil,
			} {
				if set {
					acceptanceConditions++
				}
			}
			expectedConditions := 0
			for _, set := range []bool{
				input.ExpectedText != "",
				input.ExpectedURLContains != "",
				input.ExpectedVisibleSelector != "",
				input.ExpectedValueSelector != "" || input.ExpectedValue != "",
				input.ExpectedCountSelector != "" || input.ExpectedCount != nil,
			} {
				if set {
					expectedConditions++
				}
			}
			if acceptanceConditions > 0 && expectedConditions > 0 {
				return nil, fmt.Errorf("expected_* and acceptance_* conditions cannot be combined")
			}
			if acceptanceConditions > 0 {
				out, err := s(ctx).SubmitFormWithAcceptanceContext(ctx, input.Selector, input.MatchURL, input.TimeoutMS, acceptance)
				return out, mcpErrWithActionTimeout(err, input.ActionTimeoutMS)
			}
			condition := agent.Postcondition{Text: input.ExpectedText, URLContains: input.ExpectedURLContains, VisibleSelector: input.ExpectedVisibleSelector, ValueSelector: input.ExpectedValueSelector, Value: input.ExpectedValue, CountSelector: input.ExpectedCountSelector, Count: input.ExpectedCount}
			out, err := s(ctx).SubmitFormWithPostconditionContext(ctx, input.Selector, input.MatchURL, input.TimeoutMS, condition)
			return out, mcpErrWithActionTimeout(err, input.ActionTimeoutMS)
		})

	srv.Tool("hover").
		Description("Hover over an element to trigger CSS :hover states, tooltips, and dropdown menus. Optionally set timeout_ms to bound the action. Results include target_id and report postcondition_verified only when the browser reports the target inside :hover; otherwise the pointer movement is effect_unverified. Optionally provide one expected_* page condition to verify the application effect and preserve it for safe reconciliation if the hover is interrupted.").
		Handler(func(ctx context.Context, input HoverInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).HoverWithPostconditionContext(ctx, input.Selector, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("double_click").
		Description("Double-click an element. Optionally set timeout_ms; if dispatch may have occurred before interruption, Scout returns outcome_unknown. Results include target_id and distinguish event dispatch from a verified postcondition; without one, the application effect is unverified. Optionally verify resulting page state with text, URL, visibility, exact value, or exact selector count.").
		Handler(func(ctx context.Context, input ClickInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).DoubleClickWithPostconditionContext(ctx, input.Selector, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("right_click").
		Description("Right-click an element to trigger context menus. Optionally set timeout_ms; if dispatch may have occurred before interruption, Scout returns outcome_unknown. Results include target_id and distinguish event dispatch from a verified postcondition; without one, the application effect is unverified. Optionally verify resulting page state with text, URL, visibility, exact value, or exact selector count.").
		Handler(func(ctx context.Context, input ClickInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).RightClickWithPostconditionContext(ctx, input.Selector, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("select_option").
		Description("Select an option from a dropdown/select element by visible text or value. Optionally set timeout_ms; if selection may have changed before interruption, Scout returns outcome_unknown with the exact expected value for safe reconciliation. Results include target_id and report value_verified or value_mismatch after re-reading the live select value, with evidence and recovery guidance. This verifies the field value, not application persistence.").
		Handler(func(ctx context.Context, input SelectOptionInput) (*agent.ElementResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).SelectOptionContext(ctx, input.Selector, input.Option)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("scroll_to").
		Description("Scroll to bring an element into view. Optionally set timeout_ms to bound the action. Results include target_id and verify whether the target bounding box intersects the viewport; if it does not, the result is effect_unverified with evidence and recovery guidance.").
		Handler(func(ctx context.Context, input ScrollToInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ScrollToContext(ctx, input.Selector)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("scroll_by").
		Description("Scroll the page by pixel offset. Optionally set timeout_ms to bound the action. Positive y = scroll down. Results include target_id and verify the observed scrollY change; a page boundary or unchanged position is effect_unverified.").
		Handler(func(ctx context.Context, input ScrollByInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ScrollByContext(ctx, input.X, input.Y)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("focus").
		Description("Set focus on an element, triggering :focus CSS state. Optionally set timeout_ms to bound the action. Results include target_id and report verified success only when document.activeElement matches the requested element; failures after dispatch may return outcome_unknown.").
		Handler(func(ctx context.Context, input FocusInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).FocusContext(ctx, input.Selector)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("drag_drop").
		Description("Drag an element and drop it on another element. Optionally set timeout_ms; if drag events may have dispatched before interruption, Scout returns outcome_unknown for safe reconciliation. Results include target_id and evidence; without a postcondition, only event dispatch is reported and the application effect is unverified. Optionally pass one text, URL, visibility, exact value, or exact selector-count postcondition.").
		Handler(func(ctx context.Context, input DragDropInput) (*agent.PageResult, error) {
			var cancel context.CancelFunc
			var err error
			ctx, cancel, err = withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).DragDropWithPostconditionContext(ctx, input.From, input.To, input.Postcondition())
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

		// --- Batch ---

	srv.Tool("batch").
		Description("Execute up to 100 actions sequentially (96 KiB total JSON input limit). Optionally set timeout_ms to bound the complete batch. By default, Scout stops before later actions if a step may have taken effect but could not be verified; the response includes stopped_on_unknown_at, next_action_index, and the step's recovery_record when available. Reconcile before deciding whether to retry the step or continue from next_action_index. Set continue_on_unknown=true only when later actions are safe despite uncertainty. Set require_verified=true to stop on the first step without positive verification evidence, including actions without a postcondition. This strict contract takes precedence over continue_on_unknown. Known rejected actions continue by default. succeeded counts completed steps; verified, rejected, unverified, and unknown are mutually exclusive evidence counts. Rejected and unknown steps are included in failed. wait and fill_form_semantic report verified observations; other actions without a postcondition are effect_unverified. Each step includes evidence and recovery guidance when useful. Optionally set one text, URL, visibility, exact value, or exact selector-count postcondition on each step. Actions: click, type, fill_form_semantic, wait, scroll_to, click_label, webmcp_invoke. WebMCP invocation requires webmcp_name, webmcp_frame_id, and one expected postcondition; the current catalog and page schema are rechecked at dispatch. Oversized responses omit verbose evidence but preserve step outcomes and recovery records.").
		Handler(func(ctx context.Context, input BatchInput) (*agent.BatchResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			out, err := s(ctx).ExecuteBatchWithOptionsContext(ctx, input.Actions, agent.BatchOptions{ContinueOnUnknown: input.ContinueOnUnknown, RequireVerified: input.RequireVerified})
			if err != nil && out != nil {
				markBatchCompacted(out)
				return out, mcpErrWithPartialResult(err, out, input.TimeoutMS)
			}
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("task_run").
		Description("Execute a verified, target-bound browser task with a stable caller ID. Supported actions include DOM operations and webmcp_invoke; WebMCP actions must name a current catalog tool and frame and provide exactly one expected postcondition. Scout rechecks the page catalog/schema and origin policy at dispatch, ignores the page tool's returned value as proof, and verifies the declared condition. Within the task store's namespace, retrying the same ID and identical plan returns the stored result without replay; a different plan or a concurrent execution using that ID returns SCOUT_TASK_ID_CONFLICT. By default, each session keeps up to 256 IDs in memory. Start with --task-store=PATH to persist records across server restarts; the durable database retains up to 8,192 IDs total across all MCP owner/session namespaces, without automatic pruning. After reconciling an uncertain outcome, decide whether to retry that action or continue, then use a new ID for the chosen plan. Every action must return positive verification evidence; rejected or unverified steps stop the task, and uncertain effects require reconciliation before continuing. The result includes a plan_id, task status, and resume_from cursor. An identical retry returns the saved result with idempotency_replay=true, making clear that this call did not repeat browser actions. Optional final_postcondition checks the completed task's final page state on the bound target; if the final condition is not observed after actions complete, status is needs_reconciliation, failure_reason is verification_failed, and same-ID replay remains blocked until host reconciliation. The active profile must expose recover_task_run and resolve_task_run, checked before browser actions; required_tools can declare additional follow-up tools. Optional allowed_origins must exactly match the session's startup policy; task execution cannot change an existing browser's policy. That page-level policy does not cover service-worker requests or WebSocket handshakes; use an enforcing proxy or host network policy for browser-wide egress.").
		Handler(func(ctx context.Context, input TaskRunInput) (*agent.TaskRunResult, error) {
			if err := validateTaskRunToolRequirements(ctx, input.RequiredTools, toolFilter); err != nil {
				return nil, err
			}
			if input.FinalPostcondition != nil {
				if err := input.FinalPostcondition.Validate(); err != nil || input.FinalPostcondition.TargetAbsent != "" {
					if err == nil {
						err = fmt.Errorf("target_absent is only valid when reconciling an unknown close_tab outcome")
					}
					return nil, mcpPolicyError("SCOUT_INVALID_ARGUMENT", "invalid final_postcondition: "+err.Error(), "Provide exactly one supported page condition; target_absent is reserved for close_tab reconciliation.")
				}
			}
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			out, err := s(bounded).RunTaskContext(bounded, agent.TaskRunSpec{
				ID: input.ID, TargetID: input.TargetID, AllowedOrigins: input.AllowedOrigins,
				FinalPostcondition: input.FinalPostcondition, Timeout: time.Duration(input.TimeoutMS) * time.Millisecond, Actions: input.Actions,
			})
			if err != nil && out != nil {
				return out, mcpErrWithPartialResult(err, out, input.TimeoutMS)
			}
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("get_task_run").
		ReadOnly().
		OutputSchema(GetTaskRunResult{}).
		Description("Inspect a caller-owned task ID in the selected session's task store without changing it. Returns the saved result for a completed task, or the in-flight reservation, bound browser target, redacted non-executable plan, and durable action cursor after interruption. The plan omits action inputs and expected values; it is evidence for another host, not a replay request. Protect the returned task result and record as sensitive browser state: page-derived text, URLs, selectors, evidence, error details, and resolution notes may contain private application data. Store/export the complete record only through host-approved data paths; the separate portable receipt applies its own redactions. After reconnect, select the retained session with its opaque session_id and authenticate as the same configured principal. With --task-store, MCP scopes records by principal and session name, so the task record can be inspected from a replacement transport while the durable store and server session namespace remain available. Without a principal, HTTP transport IDs and stdio namespaces remain connection/process-local. An in-flight record does not prove whether its actions took effect; reconcile application state before deciding what to do next.").
		Handler(func(ctx context.Context, input GetTaskRunInput) (*GetTaskRunResult, error) {
			record, found, err := s(ctx).GetTaskRunContext(ctx, input.ID)
			if err != nil {
				return nil, mcpErr(err)
			}
			result := &GetTaskRunResult{ContentTrust: agent.ContentTrustUntrustedPage, Found: found}
			if found {
				result.Record = &record
			}
			return result, nil
		})

	srv.Tool("recover_task_run").
		ReadOnly().
		OutputSchema(RecoverTaskRunResult{}).
		Description("Inspect a caller-owned task ID and, when available, export its selected portable recovery receipt in one read-only response. The returned task record/result is sensitive browser state and may include page text, URLs, selectors, evidence, error details, and resolution notes; protect it as private application data. The separate portable receipt redacts expected values and page text. For an interrupted reservation, the receipt covers only the currently started step; it never includes action inputs or authorizes replay. A retained needs_reconciliation result identifies a failed final readback or result persistence and remains blocked from same-ID replay. The response includes bounded guidance from the durable cursor and saved result: before any action starts it requires confirming the previous worker stopped before a new task; otherwise it directs the host to authoritative reconciliation. Guidance never declares a business outcome or authorizes replay. The task record is returned even when no receipt can be formed. For recovery across process restarts, configure a durable --task-store. task_run requires this tool and resolve_task_run to be exposed in the active profile before browser work. After this call, inspect authoritative application state and use resolve_task_run separately to record the latest host observation; supply this record's exact fingerprint.").
		Handler(func(ctx context.Context, input RecoverTaskRunInput) (*RecoverTaskRunResult, error) {
			view, err := s(ctx).RecoverTaskRunContext(ctx, input.ID, input.OperationOrdinal)
			if err != nil {
				return nil, mcpErr(err)
			}
			return &RecoverTaskRunResult{ContentTrust: view.ContentTrust, Found: view.Found, Record: view.Record, Receipt: view.Receipt, ReceiptUnavailableReason: view.ReceiptUnavailableReason, Guidance: view.Guidance}, nil
		})

	srv.Tool("export_task_recovery_receipt").
		ReadOnly().
		Description("Export one saved task action as the versioned scout.recovery-receipt/v1 envelope. For an interrupted reservation, exports only the currently started step; for a completed task, exports the selected saved action receipt (or the first when operation_ordinal is omitted). The envelope redacts expected values and page text, is read-only, and never authorizes replay. Reconcile uncertain effects against authoritative application state before any follow-up action.").
		Handler(func(ctx context.Context, input ExportTaskRecoveryReceiptInput) (*agent.RecoveryReceipt, error) {
			receipt, err := s(ctx).ExportTaskRunRecoveryReceiptContext(ctx, input.ID, input.OperationOrdinal)
			if err != nil {
				return nil, mcpErr(err)
			}
			return &receipt, nil
		})

	srv.Tool("resolve_task_run").
		ClosedWorld().
		Description("Record a host-side reconciliation observation for a task reservation left in flight by process or transport loss. First inspect recover_task_run (or get_task_run) and check application state independently. Supply the record's exact plan_fingerprint, status=unresolved, verified_complete, or rejected, and short non-secret evidence. Terminal statuses require evidence_source, evidence_reference, and observed_at so the authoritative system, opaque record/event identifier, and read time remain attached to the conclusion; references must be opaque and non-secret. unresolved keeps the task blocked and may later be followed by a terminal conclusion; every host observation is retained in resolution_history and the latest one supersedes the earlier task result in the current record. Terminal conclusions do not make Scout claim browser proof, unlock the task ID, or replay actions. Use a new task ID for follow-up work. For recovery across process restarts, configure a durable store that supports the optional resolution extension.").
		Handler(func(ctx context.Context, input ResolveTaskRunInput) (*agent.TaskRunRecord, error) {
			record, err := s(ctx).ResolveInterruptedTaskRunWithEvidenceContext(ctx, input.ID, input.PlanFingerprint, agent.TaskRunResolution{
				Status: input.Status, Evidence: input.Evidence, EvidenceSource: input.EvidenceSource,
				EvidenceReference: input.EvidenceReference, ObservedAt: input.ObservedAt,
			})
			if err != nil {
				return nil, mcpErr(err)
			}
			return &record, nil
		})

		// --- Extraction ---

	srv.Tool("webmcp_catalog").
		ReadOnly().
		Description("List page-registered WebMCP tools on one target. WebMCP is experimental and may be unavailable in this Chrome build. Names, descriptions, schemas, frame IDs, and origins come from the page and are untrusted. Check complete and coverage_warnings for frame coverage, truncated and omitted_tools for tool omissions, and url_truncated for a shortened page URL. Pass target_id from current Scout evidence.").
		Handler(func(ctx context.Context, input WebMCPCatalogInput) (*agent.WebMCPCatalog, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).WebMCPCatalogContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("webmcp_invoke").
		Description("Invoke one page-registered WebMCP tool on the exact target/frame from the current catalog. Page-provided metadata and result are untrusted. A single explicit postcondition is required and is the only basis for verified=true; the page tool return alone does not prove an application effect. If invocation outcome is unknown, reconcile before retrying.").
		Handler(func(ctx context.Context, input WebMCPInvokeInput) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			condition := agent.Postcondition{Text: input.ExpectedText, URLContains: input.ExpectedURLContains, Title: input.ExpectedTitle, ExistsSelector: input.ExpectedExistsSelector, VisibleSelector: input.ExpectedVisibleSelector}
			out, err := s(ctx).InvokeWebMCPToolContext(ctx, input.TargetID, input.FrameID, input.Name, input.Input, condition)
			return out, mcpErr(err)
		})

	srv.Tool("extract").
		ReadOnly().
		Description("Extract up to 4,000 characters from one element. Optionally pass target_id from an earlier result to reject extraction if Scout has switched pages. Returns original text length and a truncation flag when the element contains more.").
		Handler(func(ctx context.Context, input ExtractInput) (*agent.ElementResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ExtractContext(ctx, input.Selector)
			if err != nil {
				return nil, mcpErr(err)
			}
			out.Text, out.TextLength, out.TextTruncated = truncateText(out.Text, 4000)
			return out, nil
		})

	srv.Tool("extract_all").
		ReadOnly().
		Description("Extract one page of matching element text and return per-item source references aligned with the values, including selectors, source tags, roles, and interaction hints. Selectors describe the DOM at extraction time. Returns at most 8 items per call and caps each item at 4,000 characters. Use has_more and next_offset to request the next page; items_truncated and item_truncated identify clipped text. The legacy truncated flag is true when either more matches exist or returned text was clipped. Pagination reads the live DOM on each call; if it changes, restart at offset 0 for a fresh view. Set include_handles=true to stamp stable node handles for click_handle; this mutates the DOM.").
		Handler(func(ctx context.Context, input ExtractAllInput) (*agent.ExtractAllResult, error) {
			limit := input.Limit
			if limit <= 0 || limit > 8 {
				limit = 8
			}
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ExtractAllPageWithOptionsContext(ctx, input.Selector, input.Offset, limit, agent.ExtractAllOptions{IncludeHandles: input.IncludeHandles})
			return out, mcpErr(err)
		})

	srv.Tool("extract_table").
		ReadOnly().
		Description("Extract structured table data. Optionally pass target_id from an earlier result to reject extraction if Scout has switched pages. Returns up to 25 rows, 10 columns, and 256 characters per cell with total row/column counts and truncation metadata.").
		Handler(func(ctx context.Context, input ExtractTableInput) (*agent.TableResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ExtractTableContext(ctx, input.Selector)
			if err != nil {
				return nil, mcpErr(err)
			}
			out.TotalCols = out.ColCount
			if len(out.Rows) > 25 {
				out.Rows = out.Rows[:25]
				out.Truncated = true
			}
			if len(out.Headers) > 10 {
				out.Headers = out.Headers[:10]
				out.Truncated = true
			}
			for i := range out.Headers {
				var clipped bool
				out.Headers[i], _, clipped = truncateText(out.Headers[i], 256)
				out.CellTextTruncated = out.CellTextTruncated || clipped
			}
			for i := range out.Rows {
				if len(out.Rows[i]) > 10 {
					out.Rows[i] = out.Rows[i][:10]
					out.Truncated = true
				}
				for j := range out.Rows[i] {
					var clipped bool
					out.Rows[i][j], _, clipped = truncateText(out.Rows[i][j], 256)
					out.CellTextTruncated = out.CellTextTruncated || clipped
				}
			}
			if out.CellTextTruncated {
				out.Truncated = true
			}
			out.RowCount = len(out.Rows)
			out.ColCount = len(out.Headers)
			return out, nil
		})

	srv.Tool("markdown").
		ReadOnly().
		OutputSchema(agent.ContentResult{}).
		Description("Get compact markdown with content length, configured limit, and an explicit truncation flag. Default maximum is 4000 characters. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.ContentResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).MarkdownResultWithLimitContext(ctx, 0)
			return out, mcpErr(err)
		})

	srv.Tool("readable_text").
		ReadOnly().
		OutputSchema(agent.ContentResult{}).
		Description("Extract main readable content with length, configured limit, and an explicit truncation flag. Default maximum is 4000 characters. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.ContentResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ReadableTextResultWithLimitContext(ctx, 0)
			return out, mcpErr(err)
		})

	srv.Tool("accessibility_tree").
		ReadOnly().
		OutputSchema(agent.ContentResult{}).
		Description("Get a compact accessibility tree with length, configured limit, and an explicit truncation flag. Default maximum is 4000 characters. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.ContentResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).AccessibilityTreeResultWithLimitContext(ctx, 0)
			return out, mcpErr(err)
		})

	srv.Tool("save_page_snapshot").
		OpenWorld().
		OutputSchema(PageSnapshotResult{}).
		Description("Save a full bounded page snapshot to a private local text artifact without placing it in the MCP context. Choose markdown, readable_text, or accessibility_tree; default maximum is 200,000 characters, hard maximum 2,000,000. Use read_page_snapshot to search a selected range by offset. An explicit output_path must not already exist; choose a new path to avoid overwriting another file.").
		Handler(func(ctx context.Context, input SavePageSnapshotInput) (*PageSnapshotResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			owner := artifactOwnerForContext(ctx)
			snapshotMu.Lock()
			quotaAvailable := artifactQuotaAvailable(len(resultArtifactSizes), resultArtifactBytes, resultOwnerCounts[owner], resultOwnerBytes[owner], 1)
			snapshotMu.Unlock()
			if !quotaAvailable {
				return nil, fmt.Errorf("MCP artifact storage budget is full; delete an existing artifact before saving another")
			}
			if input.URL != "" {
				if _, err := s(ctx).NavigateWithPostconditionContext(ctx, input.URL, agent.Postcondition{}); err != nil {
					return nil, mcpErr(err)
				}
			}
			format := strings.ToLower(strings.TrimSpace(input.Format))
			if format == "" {
				format = "markdown"
			}
			limit := input.MaxChars
			if limit <= 0 {
				limit = 200_000
			}
			if limit > maxPageSnapshotChars {
				limit = maxPageSnapshotChars
			}
			var content *agent.ContentResult
			var err error
			switch format {
			case "markdown":
				content, err = s(ctx).MarkdownResultWithLimitContext(ctx, limit)
			case "readable_text":
				content, err = s(ctx).ReadableTextResultWithLimitContext(ctx, limit)
			case "accessibility_tree":
				content, err = s(ctx).AccessibilityTreeResultWithLimitContext(ctx, limit)
			default:
				return nil, fmt.Errorf("unsupported snapshot format %q; use markdown, readable_text, or accessibility_tree", format)
			}
			if err != nil {
				return nil, err
			}
			ext := map[string]string{"markdown": "md", "readable_text": "txt", "accessibility_tree": "txt"}[format]
			size := int64(len(content.Content))
			snapshotMu.Lock()
			if !artifactQuotaAvailable(len(resultArtifactSizes), resultArtifactBytes, resultOwnerCounts[owner], resultOwnerBytes[owner], size) {
				snapshotMu.Unlock()
				return nil, fmt.Errorf("MCP artifact storage budget is full; delete an existing artifact before saving another")
			}
			path, err := writePrivateTextArtifact(input.OutputPath, "snapshot", ext, content.Content)
			if err != nil {
				snapshotMu.Unlock()
				return nil, err
			}
			digest := sha256.Sum256([]byte(content.Content))
			snapshotFiles[path] = digest
			snapshotOwners[path] = owner
			resultArtifactSizes[path] = size
			resultArtifactBytes += size
			resultOwnerBytes[owner] += size
			resultOwnerCounts[owner]++
			snapshotMu.Unlock()
			artifact := artifactReference(path, "page_snapshot", ext, []byte(content.Content))
			return &PageSnapshotResult{Artifact: artifact, ContentTrust: agent.ContentTrustUntrustedPage, Format: format, TotalChars: content.TotalLength, SavedChars: content.Length, Truncated: content.Truncated}, nil
		})

	srv.Tool("read_page_snapshot").
		ReadOnly().
		OutputSchema(PageSnapshotExcerpt{}).
		Description("Read a selected range from a registered private text artifact created in this server process, including snapshots and oversized MCP tool results. Access is restricted to the same owner and browser session that created the artifact; stateless HTTP callers must select it with _meta.go.klarlabs.de/scout/session. Use offset and limit (default 8,000 characters, max 20,000); the result reports total length and whether more text remains.").
		Handler(func(ctx context.Context, input ReadPageSnapshotInput) (*PageSnapshotExcerpt, error) {
			return readPrivateSnapshot(&snapshotMu, snapshotFiles, snapshotOwners, artifactOwnerForContext(ctx), input)
		})

	srv.Tool("search_page_snapshot").
		ReadOnly().
		OutputSchema(SearchPageSnapshotResult{}).
		Description("Search a registered private text artifact for case-insensitive literal text and return matching excerpts with character offsets. Access is restricted to the same owner and browser session that created the artifact; stateless HTTP callers must select it with _meta.go.klarlabs.de/scout/session. Results are limited to 50 matches and include truncation metadata.").
		Handler(func(ctx context.Context, input SearchPageSnapshotInput) (*SearchPageSnapshotResult, error) {
			return searchPrivateSnapshot(&snapshotMu, snapshotFiles, snapshotOwners, artifactOwnerForContext(ctx), input)
		})

	srv.Tool("delete_page_snapshot").
		ClosedWorld().
		Description("Delete a registered private text artifact, including page snapshots or oversized MCP tool results. Access is restricted to the same owner and browser session that created the artifact; stateless HTTP callers must select it with _meta.go.klarlabs.de/scout/session.").
		Handler(func(ctx context.Context, input DeletePageSnapshotInput) (string, error) {
			owner := artifactOwnerForContext(ctx)
			if err := deletePrivateSnapshot(&snapshotMu, snapshotFiles, snapshotOwners, resultArtifactSizes, &resultArtifactBytes, resultOwnerBytes, resultOwnerCounts, owner, input.Path); err != nil {
				return "", err
			}
			return "Artifact deleted", nil
		})

	// --- Capture ---

	srv.Tool("screenshot").
		ReadOnly().
		Description("Capture a screenshot. Optionally pass target_id from an earlier result to reject navigation or capture if Scout has switched pages. Returns a data URL by default: quality 60, max_width 1024, and an 80KB hard cap. Scout progressively reduces quality and resolution; image_info reports initial/returned dimensions and byte sizes plus the compression quality/scale. If the smallest capture still exceeds the cap, it returns an error. Set save_to_disk (or output_path) to write a PNG and return artifact metadata with path, MIME type, size, and SHA-256.").
		Handler(func(ctx context.Context, input ScreenshotInput) (*CaptureResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			if err := maybeNavigate(ctx, input.URL); err != nil {
				return nil, err
			}
			toDisk := input.SaveToDisk || input.OutputPath != ""

			maxWidth := input.MaxWidth
			if maxWidth == 0 && !toDisk {
				maxWidth = 1024 // sane default — keeps result under ~80KB without aggressive quality cuts
			}
			opts := browse.ScreenshotOptions{
				MaxSize:  80 * 1024, // 80KB default — fits comfortably under MCP per-result token caps
				FullPage: input.FullPage,
				MaxWidth: maxWidth,
				Format:   "jpeg",
				Quality:  60,
			}
			if toDisk {
				// The cap and the JPEG default exist so base64 fits the
				// tool-result token limit. Once the bytes go to a file that
				// constraint is gone, and applying it anyway silently degrades a
				// tall full_page capture for a reason that no longer holds (#79).
				opts.MaxSize = 0
				opts.Format = "png"
				opts.Quality = 0
			}
			if input.Quality > 0 {
				opts.Quality = input.Quality
				opts.Format = "jpeg" // quality is a JPEG control
			}
			data, imageInfo, err := s(ctx).ScreenshotWithOptionsInfoContext(ctx, opts)
			if err != nil {
				return nil, err
			}
			out := &CaptureResult{ContentTrust: agent.ContentTrustUntrustedPage, ImageInfo: &imageInfo}
			if toDisk {
				_, ext := captureImageFormat(data)
				path, err := writeCapture(input.OutputPath, "screenshot", ext, data)
				if err != nil {
					return nil, err
				}
				out.Artifact = artifactReference(path, "screenshot", ext, data)
				return out, nil
			}
			mime := "image/jpeg"
			if opts.Format == "png" {
				mime = "image/png"
			}
			out.DataURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
			return out, nil
		})

	srv.Tool("annotated_screenshot").
		ReadOnly().
		Description("Label visible interactive elements with numbers. Returns up to 100 element records with target_id and total/truncation metadata. By default returns only the compact element list. Set include_image=true to also get a screenshot capped at 80KB and 1024px; image_info reports compression when the image is returned or saved. Use save_to_disk/output_path for a full-resolution image and artifact metadata. Label numbers are per-call; use node_handle with click_handle for stable identity across reordering, and pass the returned target_id to reject a tab change.").
		Handler(func(ctx context.Context, input AnnotatedScreenshotInput) (*AnnotatedScreenshotResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			toDisk := input.SaveToDisk || input.OutputPath != ""
			maxBytes, maxWidth := 80*1024, 1024
			if toDisk {
				maxBytes, maxWidth = 0, 0
			}
			result, err := s(ctx).AnnotatedScreenshotWithLimitsContext(ctx, maxBytes, maxWidth)
			if err != nil {
				return nil, err
			}
			out := &AnnotatedScreenshotResult{
				ContentTrust: result.ContentTrust,
				TargetID:     result.TargetID,
				FrameID:      result.FrameID,
				Elements:     result.Elements,
				Count:        result.Count,
				Total:        result.Total,
				Truncated:    result.Truncated,
				LabelScope:   result.LabelScope,
			}
			if input.SaveToDisk || input.OutputPath != "" {
				// Writing implies include_image: asking for a file and getting
				// only an element list would be a silent no-op.
				_, ext := captureImageFormat(result.Image)
				path, werr := writeCapture(input.OutputPath, "annotated", ext, result.Image)
				if werr != nil {
					return nil, werr
				}
				out.ImagePath = path
				out.Artifact = artifactReference(path, "annotated_screenshot", ext, result.Image)
				out.ImageInfo = &result.ImageInfo
				return out, nil
			}
			if input.IncludeImage {
				mime, _ := captureImageFormat(result.Image)
				out.Image = mime + ";base64," + base64.StdEncoding.EncodeToString(result.Image)
				out.ImageInfo = &result.ImageInfo
			}
			return out, nil
		})

	srv.Tool("pdf").
		ReadOnly().
		Description("Generate a PDF of the current page. Small files (up to 80KB) are returned as a data URL; larger files require save_to_disk or output_path and return artifact metadata with path, MIME type, size, and SHA-256.").
		Handler(func(ctx context.Context, input PDFInput) (*CaptureResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			if err := maybeNavigate(ctx, input.URL); err != nil {
				return nil, err
			}
			data, err := s(ctx).PDFContext(ctx)
			if err != nil {
				return nil, err
			}
			out := &CaptureResult{ContentTrust: agent.ContentTrustUntrustedPage}
			if input.SaveToDisk || input.OutputPath != "" {
				path, err := writeCapture(input.OutputPath, "page", "pdf", data)
				if err != nil {
					return nil, err
				}
				out.Artifact = artifactReference(path, "pdf", "pdf", data)
				return out, nil
			}
			if len(data) > 80*1024 {
				return nil, fmt.Errorf("PDF is %d bytes; set save_to_disk or output_path to keep it out of the MCP response", len(data))
			}
			out.DataURL = "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(data)
			return out, nil
		})

	// --- Network ---

	srv.Tool("enable_network_capture").
		ClosedWorld().
		Description("Start capturing network XHR/fetch responses. Optionally pass target_id from an earlier result to reject the operation if Scout has switched pages. Patterns filter by URL substring.").
		Handler(func(ctx context.Context, input EnableNetworkInput) (string, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			if err := s(ctx).EnableNetworkCaptureContext(ctx, input.Patterns...); err != nil {
				return "", err
			}
			return "Network capture enabled", nil
		})

	srv.Tool("network_summary").
		ReadOnly().
		OutputSchema(agent.NetworkSummary{}).
		Description("Rolled-up view of captured network traffic. Optionally pass target_id from earlier page evidence to reject a query after a tab switch. Returns retained total count, dropped_total for records omitted by capture limits, bucketed by status class (1xx/2xx/3xx/4xx/5xx/0), up to 50 requests with status >= 400 plus failure total/truncation metadata, and the pending count. Single call replaces enable_network_capture → act → failed_requests for the common 'did this action succeed at the network layer' question.").
		Handler(func(ctx context.Context, input NetworkSummaryInput) (*agent.NetworkSummary, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).NetworkSummaryContext(ctx, input.Pattern)
			return out, mcpErr(err)
		})

	srv.Tool("network_requests").
		ReadOnly().
		OutputSchema(NetworkRequestsResult{}).
		Description("Get a bounded page from the retained window of captured network requests/responses, ordered oldest to newest, including request/response bodies (max 32KB each). Authorization, proxy-authorization, cookie, set-cookie, and credential-like header names containing common auth/token/key/secret/CSRF markers are redacted; this heuristic may not recognize every application-specific secret header. URLs and request/response bodies may still contain secrets or personal data and must be handled as sensitive untrusted page content. Optionally pass target_id from earlier page evidence to reject a query after a tab switch. Scout retains at most 200 records and reports session-wide dropped_total when its in-flight or retained-history cap omits data. Use offset and max_recent to retrieve pages from the current retained window; offsets may shift as new requests evict older records. Each page returns total, offset, limit, and truncation metadata. The default and maximum page size is 3 requests to leave room under the MCP response budget for large bodies. If empty and capture was never enabled, the response includes a hint.").
		Handler(func(ctx context.Context, input NetworkRequestsInput) (*NetworkRequestsResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			requests, captureEnabled, dropped, err := s(ctx).CapturedRequestsSnapshotContext(ctx, input.Pattern)
			if err != nil {
				return nil, mcpErr(err)
			}
			out, total, offset, limit, truncated := boundedRecentRequests(requests, input.Offset, input.MaxRecent)
			res := &NetworkRequestsResult{
				ContentTrust:   agent.ContentTrustUntrustedPage,
				Requests:       out,
				Count:          len(out),
				Total:          total,
				DroppedTotal:   dropped,
				Offset:         offset,
				Limit:          limit,
				Truncated:      truncated,
				CaptureEnabled: captureEnabled,
			}
			if len(out) == 0 && !res.CaptureEnabled {
				res.Hint = "no requests captured. Call enable_network_capture BEFORE the action that triggers the request — capture is a future-tense subscription, not a backfill."
			}
			return res, nil
		})

	// --- Framework support ---

	srv.Tool("wait_spa").
		ReadOnly().
		Description("Wait for SPA framework (React/Vue/Angular/Next.js/Svelte) to finish rendering.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.PageResult, error) {
			if err := s(ctx).WaitForSPAContext(ctx); err != nil {
				return nil, mcpErr(err)
			}
			return s(ctx).SnapshotContext(ctx)
		})

	srv.Tool("wait_for_spa_idle").
		ReadOnly().
		Description("Stronger SPA wait than wait_spa: blocks until document complete, framework hydration events fired (Astro 'astro:page-load' / 'astro:end', Vue/React mount markers populated), no pending XHR/fetch, no visible spinners/skeletons, and the DOM has been mutation-free for quiet_ms. Use after a click that triggers route change or hydration when check_readiness returns 100 too eagerly.").
		Handler(func(ctx context.Context, input WaitForSPAIdleInput) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).WaitForSPAIdleContext(ctx, input.QuietMS, input.TimeoutMS)
			return out, mcpErr(err)
		})

	srv.Tool("detect_frameworks").
		ReadOnly().
		Description("Detect which frontend frameworks are active on the page. Optionally pass target_id from an earlier result to reject detection if Scout has switched pages.").
		Handler(func(ctx context.Context, input ObserveInput) ([]string, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).DetectedFrameworksContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("component_state").
		ReadOnly().
		Description("Extract component state/props from any framework (React, Vue, Svelte, Angular, Alpine, Lit). Optionally pass target_id from an earlier result to reject inspection if Scout has switched pages. MCP output is bounded to 64 KB; _scout_truncated=true means some values were clipped or omitted.").
		Handler(func(ctx context.Context, input ComponentStateInput) (map[string]any, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).ComponentStateContext(ctx, input.Selector)
			if err != nil || out == nil {
				return out, mcpErr(err)
			}
			return boundStateOutput(out), nil
		})

	srv.Tool("app_state").
		ReadOnly().
		Description("Extract global app state (Redux, Next.js, Nuxt, Remix, SvelteKit, Gatsby, Astro, Alpine, HTMX). Optionally pass target_id from an earlier result to reject inspection if Scout has switched pages. MCP output is bounded to 64 KB; _scout_truncated=true means some values were clipped or omitted.").
		Handler(func(ctx context.Context, input ObserveInput) (map[string]any, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).GetAppStateContext(ctx)
			if err != nil || out == nil {
				return out, mcpErr(err)
			}
			return boundStateOutput(out), nil
		})

	// --- Dialog detection ---

	srv.Tool("detect_dialog").
		ReadOnly().
		Description("Check if a modal, dialog, popup, or overlay is currently visible. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages. Returns its title, text, buttons, and inputs.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.DialogInfo, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).DetectDialogContext(ctx)
			return out, mcpErr(err)
		})

	// --- Smart helpers ---

	srv.Tool("dismiss_cookies").
		Description("Find a cookie consent banner and click a supported accept control (common selectors and text such as Accept, Agree, Got it, OK). Pass target_id from an earlier result to reject the action if the active page changed; optionally set timeout_ms to bound the action and verification. The result reports dismissed=true only when the matched banner or accept control is no longer visible after the page settles. If the control remains visible, inspect the page and confirm the site's consent state.").
		Handler(func(ctx context.Context, input struct {
			TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
			TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds"`
		}) (*agent.CookieDismissResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			out, err := s(ctx).DismissCookieBannerContext(ctx)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("cookies_list").
		ReadOnly().
		OutputSchema(CookiesListResult{}).
		Description("List up to 100 cookies for the active page (names + flags only — values are redacted), with total and truncation metadata. Optionally pass target_id from an earlier result to reject the read if Scout has switched pages. Useful for diagnosing stale-session issues after a backend restart.").
		Handler(func(ctx context.Context, input ObserveInput) (*CookiesListResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			cookies, err := s(ctx).ListCookiesContext(ctx)
			if err != nil {
				return nil, mcpErr(err)
			}
			const limit = 100
			total := len(cookies)
			if len(cookies) > limit {
				cookies = cookies[:limit]
			}
			return &CookiesListResult{ContentTrust: agent.ContentTrustUntrustedPage, Items: cookies, Count: len(cookies), Total: total, Truncated: total > len(cookies)}, nil
		})

	srv.Tool("cookies_clear").
		Description("Clear cookies. With no name, drops all cookies via Network.clearBrowserCookies. With a name, deletes only that cookie (optionally scoped by domain/path). Returns the removed count, target identity, and readback evidence. Pass target_id to bind the action to a page and optionally set timeout_ms to bound deletion and verification. Use this when a backend has been restarted and the previous session token is invalid.").
		Handler(func(ctx context.Context, input struct {
			TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the action if Scout has switched pages"`
			Name      string `json:"name,omitempty" jsonschema:"description=Cookie name to delete. Leave empty to clear all cookies."`
			Domain    string `json:"domain,omitempty" jsonschema:"description=Optional domain scope when deleting a single cookie."`
			Path      string `json:"path,omitempty" jsonschema:"description=Optional path scope when deleting a single cookie."`
			TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds"`
		}) (*agent.CookieMutationResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			result, err := s(ctx).ClearCookiesWithOutcomeContext(ctx, input.Name, input.Domain, input.Path)
			if err != nil {
				return nil, mcpErrWithActionTimeout(err, input.TimeoutMS)
			}
			return result, nil
		})

	srv.Tool("cookies_set").
		Description("Set a cookie on the active page and verify its name, scope, and value by readback. Pass target_id from an earlier result to reject the action if the active page changed; optionally set timeout_ms to bound setting and readback. The result reports target identity and outcome evidence without returning the cookie value. Useful for restoring sessions or seeding state.").
		Handler(func(ctx context.Context, input CookieSetInput) (*agent.CookieMutationResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			result, err := s(ctx).SetCookieWithOutcomeContext(ctx, input.Cookie())
			if err != nil {
				return nil, mcpErrWithActionTimeout(err, input.TimeoutMS)
			}
			return result, nil
		})

	srv.Tool("check_readiness").
		ReadOnly().
		OutputSchema(agent.PageReadiness{}).
		Description("Check how ready the page is for interaction. Optionally pass target_id from an earlier result to reject the check if Scout has switched pages. Returns a 0-100 score, pending XHR count, skeleton/spinner presence, and suggestions for what to wait for.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.PageReadiness, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).CheckReadinessContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("web_vitals").
		ReadOnly().
		OutputSchema(agent.WebVitalsResult{}).
		Description("Extract Core Web Vitals (LCP, CLS, INP) and performance timing (TTFB, First Paint, DOM Content Loaded). Optionally pass target_id from an earlier result to reject the read if Scout has switched pages. Each metric is rated good/needs-improvement/poor per Google thresholds.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.WebVitalsResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).WebVitalsContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("select_by_prompt").
		ReadOnly().
		Description("Find an element using natural language (e.g. 'the login button', 'search input'). Returns the best match with confidence score, CSS selector, and target_id. Pass target_id to a follow-up click with the returned selector.").
		Handler(func(ctx context.Context, input SelectByPromptInput) (*agent.PromptSelectResult, error) {
			return s(ctx).SelectByPromptContext(ctx, input.Prompt)
		})

	srv.Tool("suggest_selectors").
		ReadOnly().
		Description("Find elements similar to a selector that failed. Returns up to 5 suggestions with selector, tag, text, and classes.").
		Handler(func(ctx context.Context, input SuggestInput) ([]agent.SelectorSuggestion, error) {
			return s(ctx).SuggestSelectorsContext(ctx, input.Selector)
		})

	srv.Tool("hybrid_observe").
		ReadOnly().
		Description("Vision+DOM hybrid mode: returns a clean screenshot (no labels, capped at 80KB and 1024px) plus up to the configured maximum of interactive-element bounding boxes, with target_id and total/truncation metadata. image_info reports compression when the image is returned or saved. Pass target_id to find_by_coordinates so selection is tied to this observation. Use save_to_disk/output_path for the full-resolution image and artifact metadata.").
		Handler(func(ctx context.Context, input HybridObserveInput) (*HybridObserveResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			toDisk := input.SaveToDisk || input.OutputPath != ""
			maxBytes, maxWidth := 80*1024, 1024
			if toDisk {
				maxBytes, maxWidth = 0, 0
			}
			result, err := s(ctx).HybridObserveWithLimitsContext(ctx, maxBytes, maxWidth)
			if err != nil {
				return nil, mcpErr(err)
			}
			out := &HybridObserveResult{
				ContentTrust: result.ContentTrust,
				TargetID:     result.TargetID,
				FrameID:      result.FrameID,
				Elements:     result.Elements,
				Count:        result.Count,
				Total:        result.Total,
				Truncated:    result.Truncated,
				Width:        result.Width,
				Height:       result.Height,
			}
			if input.IncludeImage {
				mime, _ := captureImageFormat(result.Screenshot)
				out.Image = mime + ";base64," + base64.StdEncoding.EncodeToString(result.Screenshot)
				out.ImageInfo = &result.ImageInfo
			}
			if input.SaveToDisk || input.OutputPath != "" {
				_, ext := captureImageFormat(result.Screenshot)
				path, err := writeCapture(input.OutputPath, "hybrid", ext, result.Screenshot)
				if err != nil {
					return nil, err
				}
				out.Image = ""
				out.ImagePath = path
				out.Artifact = artifactReference(path, "hybrid_screenshot", ext, result.Screenshot)
				out.ImageInfo = &result.ImageInfo
			}
			return out, nil
		})

	srv.Tool("find_by_coordinates").
		ReadOnly().
		Description("Find the interactive element at given pixel coordinates. Use after hybrid_observe and pass its target_id. Returns selector, text, and target_id so a subsequent action can stay bound to the observed page.").
		Handler(func(ctx context.Context, input FindByCoordinatesInput) (*agent.PromptSelectResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			return s(ctx).FindByCoordinatesContext(ctx, input.X, input.Y)
		})

	srv.Tool("session_history").
		ReadOnly().
		Description("Get recent actions performed in this session. Returns up to 20 entries.").
		Handler(func(ctx context.Context, input HistoryInput) ([]agent.HistoryEntry, error) {
			count := input.Count
			if count <= 0 {
				count = 5
			}
			if count > 20 {
				count = 20
			}
			return s(ctx).SessionHistoryContext(ctx, count)
		})

	// --- Smart extraction ---

	srv.Tool("auto_extract").
		ReadOnly().
		Description("Auto-detect repeating patterns (product cards, search results, list items) and extract structured data. No selectors needed. Returns a bounded page with total count, offset, limit, and truncation metadata. Each reference aligns by index with items and includes selectors, stable item/field node handles, and item/field tag, role, and interaction hints. Handles remain attached to the same DOM nodes across reordering and become stale if replaced. To act on an extracted link, pass its field_handles.link value and the response target_id/frame_id to click_handle; first select the matching frame when frame_id is non-empty. Use a postcondition to verify the application effect.").
		Handler(func(ctx context.Context, input AutoExtractInput) (*agent.ExtractedPattern, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			return s(ctx).AutoExtractPageContext(ctx, input.Offset, input.Limit)
		})

	srv.Tool("scroll_and_collect").
		Description("Auto-scroll the page and collect up to 100 items as they lazy-load. Returns per-item references aligned with values, including selectors, source tags, roles, and interaction hints, plus target and frame identity. Optionally pass target_id from an earlier result to reject the operation if Scout has switched pages. For infinite scroll pages. Use offset to retrieve later slices of already loaded results; total and truncation are reported.").
		Handler(func(ctx context.Context, input struct {
			TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the operation if Scout has switched pages"`
			Selector string `json:"selector" jsonschema:"required,description=CSS selector for the repeating items"`
			MaxItems int    `json:"max_items,omitempty" jsonschema:"description=Maximum items to collect (default 100)"`
			Offset   int    `json:"offset,omitempty" jsonschema:"description=Zero-based starting item offset for pagination"`
		}) (*agent.ExtractAllResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			return s(ctx).ScrollAndCollectPageContext(ctx, input.Selector, input.Offset, input.MaxItems)
		})

	// --- Diagnostics ---

	srv.Tool("console_errors").
		ReadOnly().
		OutputSchema(agent.DiagnosticsResult{}).
		Description("Get captured console.error / console.warn messages plus recent network 4xx/5xx failures. Auto-installs lightweight failure-only network observers; the result reports network_capture_mode and network_capture_coverage so unavailable or partial network evidence is explicit.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.DiagnosticsResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).DiagnosticsContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("failed_requests").
		ReadOnly().
		OutputSchema(FailedRequestsResult{}).
		Description("Up to 20 recent network requests with status >= 400 (4xx/5xx). URL + method + status + response body snippet, totals, truncation, and network capture mode/coverage. Use after a form submission that silently failed.").
		Handler(func(ctx context.Context, input ObserveInput) (*FailedRequestsResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			diagnostics, err := s(ctx).DiagnosticsContext(ctx)
			if err != nil {
				return nil, mcpErr(err)
			}
			return &FailedRequestsResult{
				ContentTrust:           agent.ContentTrustUntrustedPage,
				Items:                  diagnostics.NetworkFailures,
				Count:                  len(diagnostics.NetworkFailures),
				Total:                  diagnostics.NetworkFailureTotal,
				Truncated:              diagnostics.NetworkFailuresTruncated,
				NetworkCaptureMode:     diagnostics.NetworkCaptureMode,
				NetworkCaptureCoverage: diagnostics.NetworkCaptureCoverage,
				NetworkCaptureScope:    diagnostics.NetworkCaptureScope,
			}, nil
		})

	srv.Tool("detect_auth_wall").
		ReadOnly().
		Description("Check if the page is a login wall, paywall, or CAPTCHA. Optionally pass target_id from an earlier result to reject detection if Scout has switched pages. Returns type, confidence, and reason.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.AuthWallResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).DetectAuthWallContext(ctx)
			return out, mcpErr(err)
		})

	srv.Tool("upload_file").
		Description("Select a local file on a file input element. Pass target_id from an earlier result to reject the action if the active page changed. Optionally set timeout_ms to bound the operation; if file selection may have dispatched before interruption, Scout returns outcome_unknown with a recovery_record containing the expected input value. The result reports value_verified after reading the filename back from the browser input. This verifies local input selection only; it does not claim that the site accepted or uploaded the file.").
		Handler(func(ctx context.Context, input struct {
			TargetID  string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier result; rejects the action if Scout has switched pages"`
			Selector  string `json:"selector" jsonschema:"required,description=CSS selector of the file input element"`
			FilePath  string `json:"file_path" jsonschema:"required,description=Local path to the file to upload"`
			TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Optional per-action deadline in milliseconds. If expiry follows file selection dispatch, reconcile the returned recovery_record before retrying."`
		}) (*agent.PageResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			out, err := s(ctx).UploadFileResultContext(ctx, input.Selector, input.FilePath)
			return out, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("compare_tabs").
		ReadOnly().
		Description("Compare content between two named tabs. Returns up to 40 heading, price, and link entries per page, clips compared text at 256 characters, and reports source totals and truncation when more content was present.").
		Handler(func(ctx context.Context, input struct {
			Tab1 string `json:"tab1" jsonschema:"required,description=Name of the first tab"`
			Tab2 string `json:"tab2" jsonschema:"required,description=Name of the second tab"`
		}) (*agent.PageDiff, error) {
			return s(ctx).CompareTabsContext(ctx, input.Tab1, input.Tab2)
		})

	// --- Utility ---

	srv.Tool("assert_text_contains").
		ReadOnly().
		Description("Check that the given text appears on the page in one round-trip. Optionally pass target_id from earlier page evidence to reject the assertion if Scout has switched pages. If selector is set, searches only that element's text; otherwise searches body innerText. Returns ok=true with a surrounding snippet on hit, ok=false with a short page-tail snippet on miss. Replaces extract+string-match for post-action verification.").
		Handler(func(ctx context.Context, input struct {
			TargetID        string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from earlier page evidence; rejects the assertion if Scout has switched pages"`
			Text            string `json:"text" jsonschema:"required,description=Text to search for."`
			Selector        string `json:"selector,omitempty" jsonschema:"description=Optional CSS selector to scope the search."`
			CaseInsensitive bool   `json:"case_insensitive,omitempty" jsonschema:"description=Fold case when comparing. Default false."`
		}) (*agent.AssertResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).AssertTextContainsContext(ctx, input.Text, input.Selector, input.CaseInsensitive)
			return out, mcpErr(err)
		})

	srv.Tool("has_element").
		ReadOnly().
		Description("Check if an element exists on the page. Optionally pass target_id from an earlier result to reject the check if Scout has switched pages.").
		Handler(func(ctx context.Context, input HasElementInput) (bool, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			found, err := s(ctx).HasElementContext(ctx, input.Selector)
			return found, mcpErr(err)
		})

	srv.Tool("wait_for").
		ReadOnly().
		Description("Wait for an element to appear in the DOM. Optionally pass target_id from an earlier result to reject the wait if Scout has switched pages.").
		Handler(func(ctx context.Context, input WaitForInput) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).WaitForSnapshotContext(ctx, input.Selector)
			if err != nil {
				var invalid *waitutil.InvalidSelectorError
				if errors.As(err, &invalid) {
					return nil, &mcpserver.ToolInputError{Message: err.Error()}
				}
				return nil, mcpErr(err)
			}
			return out, nil
		})

	srv.Tool("discover_form").
		ReadOnly().
		OutputSchema(DiscoverFormResult{}).
		Description("Discover form fields with their labels, types, and CSS selectors. Returns at most 100 fields and reports the total when more exist.").
		Handler(func(ctx context.Context, input DiscoverFormInput) (*DiscoverFormResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			discovery, err := s(ctx).DiscoverFormContext(ctx, input.Selector)
			if err != nil {
				return nil, mcpErr(err)
			}
			total := len(discovery.Fields)
			fields := discovery.Fields
			if len(fields) > 100 {
				fields = fields[:100]
			}
			return &DiscoverFormResult{
				ContentTrust: discovery.ContentTrust,
				FormSelector: discovery.FormSelector,
				Action:       discovery.Action,
				Method:       discovery.Method,
				Fields:       fields,
				Count:        len(fields),
				Total:        total,
				Truncated:    total > len(fields),
			}, nil
		})

	// --- Tabs ---

	srv.Tool("open_tab").
		OpenWorld().
		Description("Open a new named browser tab and navigate to a URL. Optionally pass the current target_id to reject the operation if Scout has switched pages, and timeout_ms to bound tab creation and navigation. The new tab becomes active.").
		Handler(func(ctx context.Context, input TabInput) (*agent.PageResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			result, err := s(ctx).OpenTabContext(ctx, input.Name, input.URL)
			return result, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("switch_tab").
		ClosedWorld().
		Description("Switch to a named tab. Pass target_id to bind the request to the current active page, and optionally pass tab_target_id from list_tabs to ensure the destination still refers to the target you inspected. timeout_ms bounds activation and state readback.").
		Handler(func(ctx context.Context, input TabInput) (*agent.PageResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			result, err := s(ctx).SwitchTabForTargetContext(ctx, input.Name, input.TabTargetID)
			return result, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("close_tab").
		ClosedWorld().
		Description("Close a named inactive tab. Pass target_id to bind the request to the current active page, and optionally pass tab_target_id from list_tabs to ensure the named tab still refers to the target you inspected. timeout_ms bounds closure. Cannot close the currently active tab.").
		Handler(func(ctx context.Context, input TabInput) (*agent.PageResult, error) {
			bounded, cancel, err := withMCPActionTimeout(ctx, input.TimeoutMS)
			if err != nil {
				return nil, mcpErr(err)
			}
			defer cancel()
			ctx = agent.WithExpectedTargetID(bounded, input.TargetID)
			result, err := s(ctx).CloseTabResultForTargetContext(ctx, input.Name, input.TabTargetID)
			return result, mcpErrWithActionTimeout(err, input.TimeoutMS)
		})

	srv.Tool("list_tabs").
		ReadOnly().
		OutputSchema(ListTabsResult{}).
		Description("List open tabs with names, URLs, and titles. If a renderer is unresponsive, its last-observed URL and title are returned with metadata_stale=true. Optionally pass target_id from earlier evidence to reject a listing after the active page changes. Use offset and limit (maximum 50 per call) to page through the stable name-sorted list; total and truncation metadata are included.").
		Handler(func(ctx context.Context, input ListTabsInput) (*ListTabsResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			limit := input.Limit
			if limit == 0 {
				limit = 50
			}
			if limit < 0 {
				return nil, fmt.Errorf("limit must be non-negative")
			}
			if limit > 50 {
				limit = 50
			}
			tabs, total, err := s(ctx).ListTabsPageContext(ctx, input.Offset, limit)
			if err != nil {
				return nil, mcpErr(err)
			}
			return &ListTabsResult{ContentTrust: agent.ContentTrustUntrustedPage, Items: tabs, Count: len(tabs), Total: total, Offset: input.Offset, Limit: limit, Truncated: input.Offset+len(tabs) < total}, nil
		})

	// --- Frames ---

	srv.Tool("switch_to_frame").
		ClosedWorld().
		Description(mcpSwitchToFrameDescription).
		Handler(func(ctx context.Context, input SwitchToFrameInput) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).SwitchToFrameContext(ctx, input.Selector)
			return out, mcpErr(err)
		})

	srv.Tool("switch_to_main_frame").
		ClosedWorld().
		Description("Switch back to the main page frame after operating inside an iframe. Optionally pass target_id from an earlier result to reject the operation if Scout has switched pages. The result includes main-frame URL/title evidence and outcome=main_frame_selected when evaluation in the main frame succeeds.").
		Handler(func(ctx context.Context, input struct {
			TargetID string `json:"target_id,omitempty" jsonschema:"description=Optional target_id from an earlier Scout result; rejects the operation if Scout has switched pages"`
		}) (*agent.PageResult, error) {
			ctx = agent.WithExpectedTargetID(ctx, input.TargetID)
			out, err := s(ctx).SwitchToMainFrameContext(ctx)
			return out, mcpErr(err)
		})

	// --- Playbook ---

	srv.Tool("start_recording").
		ClosedWorld().
		Description("Start recording browser actions into a replayable playbook. Call stop_recording when done.").
		Handler(func(ctx context.Context, input StartRecordInput) (string, error) {
			if err := s(ctx).StartRecordingPlaybookContext(ctx, input.Name); err != nil {
				return "", err
			}
			return fmt.Sprintf("Recording started: %s", input.Name), nil
		})

	srv.Tool("stop_recording").
		ClosedWorld().
		Description("Stop recording and return the playbook. Save it with save_playbook for later replay.").
		Handler(func(ctx context.Context, input ObserveInput) (*agent.Playbook, error) {
			return s(ctx).StopRecordingPlaybookContext(ctx)
		})

	srv.Tool("expect_recorded_action").
		ClosedWorld().
		Description("Attach an explicit URL, title, selector, or visible-text assertion to the most recently recorded action. Use after inspecting the intended result; replay stops with evidence if the assertion fails.").
		Handler(func(ctx context.Context, input ExpectRecordedActionInput) (string, error) {
			if err := s(ctx).ExpectRecordedActionContext(ctx, agent.ActionExpect{URL: input.URL, Title: input.Title, Selector: input.Selector, Text: input.Text}); err != nil {
				return "", err
			}
			return "Expectation attached to the most recently recorded action", nil
		})

	srv.Tool("save_playbook").
		ClosedWorld().
		Description("Save the last recorded playbook to a new JSON file for deterministic replay. Relative paths resolve under the OS temp directory; existing destinations and protected system/credential locations are rejected.").
		Handler(func(ctx context.Context, input SavePlaybookInput) (string, error) {
			path, err := newMCPOutputPath(input.Path, "playbook")
			if err != nil {
				return "", err
			}
			pb, err := s(ctx).StopRecordingPlaybookContext(ctx)
			if err != nil {
				return "", err
			}
			if err := agent.SavePlaybook(pb, path); err != nil {
				return "", err
			}
			return fmt.Sprintf("Playbook saved to %s (%d actions)", path, len(pb.Actions)), nil
		})

	srv.Tool("replay_playbook").
		OpenWorld().
		Description("Replay a saved playbook deterministically without LLM calls. Returns success/failure and any extracted data.").
		Handler(func(ctx context.Context, input ReplayInput) (*agent.PlaybookResult, error) {
			pb, err := agent.LoadPlaybook(input.Path)
			if err != nil {
				return nil, err
			}
			result, err := s(ctx).ReplayPlaybookContext(ctx, pb)
			return result, mcpErr(err)
		})

	srv.Tool("resume_playbook").
		ClosedWorld().
		Description("Resume a failed playbook after inspecting and repairing the current page. Pass the previous replay result, including its one-use resume_token, and unchanged playbook path. Do not edit progress or safety fields. If step_receipts_truncated=true, pass the compacted result as returned; Scout restores trusted receipt history from the session checkpoint. The token is session-bound. Scout resumes only when resume_safe=true. Completed actions are not repeated. Ambiguous mutations require manual inspection and a fresh replay from a confirmed state.").
		Handler(func(ctx context.Context, input ResumePlaybookInput) (*agent.PlaybookResult, error) {
			pb, err := agent.LoadPlaybook(input.Path)
			if err != nil {
				return nil, err
			}
			result, err := s(ctx).ResumePlaybookContext(ctx, pb, &input.Result)
			return result, mcpErr(err)
		})

	// --- Screen Recording (video) ---

	srv.Tool("start_screen_recording").
		ClosedWorld().
		Description("Start capturing the active page as a screencast video in a private temporary directory. Frames are streamed via CDP and (if ffmpeg is in PATH) encoded to webm/mp4 on stop. Otherwise raw JPEG frames + ffmpeg concat list are returned for offline encoding. A supplied output_dir must be outside protected system/credential locations. Call stop_screen_recording to finish.").
		Handler(func(ctx context.Context, input StartScreenRecordingInput) (string, error) {
			outputDir, err := resolveMCPOutputDirectory(input.OutputDir)
			if err != nil {
				return "", err
			}
			err = s(ctx).StartScreenRecordingContext(ctx, agent.ScreenRecordingOptions{
				Width:     input.Width,
				Height:    input.Height,
				FPS:       input.FPS,
				Quality:   input.Quality,
				Format:    input.Format,
				OutputDir: outputDir,
			})
			if err != nil {
				return "", mcpErr(err)
			}
			return "Screen recording started. Call stop_screen_recording to finish and obtain the video path.", nil
		})

	srv.Tool("stop_screen_recording").
		ClosedWorld().
		Description("Stop the active screen recording. Returns the output path, artifact metadata for the video when available, format, encoder, frame count, and duration.").
		Handler(func(ctx context.Context, input StopScreenRecordingInput) (*agent.ScreenRecordingResult, error) {
			res, err := s(ctx).StopScreenRecordingContext(ctx)
			if err != nil {
				return nil, mcpErr(err)
			}
			return res, nil
		})

	// --- Tracing ---

	srv.Tool("start_trace").
		ClosedWorld().
		Description("Start tracing all browser actions with before/after screenshots. Call stop_trace to export a zip file.").
		Handler(func(ctx context.Context, input ObserveInput) (string, error) {
			if err := s(ctx).StartTraceWithOutputBudgetContext(ctx, maxMCPToolResponseBytes); err != nil {
				return "", err
			}
			return "Trace started. Actions will be recorded with screenshots.", nil
		})

	srv.Tool("stop_trace").
		ClosedWorld().
		Description("Stop tracing and export a zip file containing trace events, screenshots, and network requests. The destination must be a new path outside protected system/credential locations; relative paths resolve under the OS temp directory. Returns its path and artifact metadata (MIME type, size, SHA-256).").
		Handler(func(ctx context.Context, input StopTraceInput) (*agent.TraceResult, error) {
			path, err := newMCPOutputPath(input.Path, "trace")
			if err != nil {
				return nil, err
			}
			result, err := s(ctx).StopTraceContext(ctx, path)
			if err == nil && result != nil && result.Artifact != nil {
				result.Artifact.SHA256 = agentFileSHA256(result.Path)
			}
			return result, err
		})

	registeredTools := srv.Tools()
	registeredToolNames := make([]string, len(registeredTools))
	for i, tool := range registeredTools {
		registeredToolNames[i] = tool.Name
	}
	if err := validateMCPToolProfiles(registeredToolNames); err != nil {
		return err
	}
	if envBool("SCOUT_BENCHMARK_OUTPUT_OVERFLOW") {
		srv.Tool("benchmark_oversized_output").
			Description("Benchmark-only tool that returns an oversized result to exercise the MCP output guard.").
			Handler(func(_ context.Context, _ struct{}) (map[string]any, error) {
				return map[string]any{"payload": strings.Repeat("x", maxMCPToolResponseBytes+1024)}, nil
			})
	}

	// Watchdog: cap every tool call at SCOUT_TOOL_TIMEOUT (default 60s)
	// so a wedged CDP session can't stall the calling agent. On
	// overrun the MCP RPC returns a structured SCOUT_TIMEOUT envelope
	// instead of hanging — see watchdog.go for details and #15.
	serveOpts := []mcp.ServeOption{
		mcp.WithMiddleware(watchdogMiddleware(resolveToolTimeout())),
		mcp.WithMiddleware(boundedToolResponseMiddleware(func(ctx context.Context, data []byte) (*agent.ArtifactReference, error) {
			owner := artifactOwnerForContext(ctx)
			snapshotMu.Lock()
			defer snapshotMu.Unlock()
			if !artifactQuotaAvailable(len(resultArtifactSizes), resultArtifactBytes, resultOwnerCounts[owner], resultOwnerBytes[owner], int64(len(data))) {
				return nil, fmt.Errorf("MCP artifact storage budget is full")
			}
			path, err := writePrivateTextArtifact("", "mcp-tool-result", "json", string(data))
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256(data)
			snapshotFiles[path] = digest
			snapshotOwners[path] = owner
			resultArtifactSizes[path] = int64(len(data))
			resultArtifactBytes += int64(len(data))
			resultOwnerBytes[owner] += int64(len(data))
			resultOwnerCounts[owner]++
			return artifactReference(path, "mcp_tool_response", "json", data), nil
		})),
		mcp.WithToolFilter(func(ctx context.Context, name string) bool {
			if name == "benchmark_oversized_output" {
				return envBool("SCOUT_BENCHMARK_OUTPUT_OVERFLOW")
			}
			if curated {
				if _, ok := mcpCuratedToolNames[name]; !ok {
					return false
				}
			}
			return toolFilter(ctx, name)
		}),
	}
	var serveErr error
	if httpAddr != "" {
		httpOpts = append(httpOpts, mcptransport.WithSessionStore(newMCPHTTPSessionStore(cleanupHTTPClient, principalSessions)))
		if err := mcp.ServeHTTPWithMiddleware(ctx, srv, httpAddr, httpOpts, serveOpts...); err != nil && err != context.Canceled {
			serveErr = err
		}
	} else if err := mcp.ServeStdio(ctx, srv, serveOpts...); err != nil && err != context.Canceled {
		serveErr = err
	}
	if serveErr != nil {
		return serveErr
	}
	return nil
}

type mcpHTTPSessionStore struct {
	*mcptransport.InMemoryStore
	mu       sync.Mutex
	onDelete func(string, string)
	owners   *mcpHTTPPrincipalSessions
}

func newMCPHTTPSessionStore(onDelete func(string, string), owners *mcpHTTPPrincipalSessions) *mcpHTTPSessionStore {
	return &mcpHTTPSessionStore{InMemoryStore: mcptransport.NewInMemoryStore(), onDelete: onDelete, owners: owners}
}

func (s *mcpHTTPSessionStore) StoreSession(ctx context.Context, clientID string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.InMemoryStore.StoreSession(ctx, clientID, data); err != nil {
		return err
	}
	if s.owners != nil {
		if err := s.owners.bind(clientID, mcpHTTPPrincipalFromContext(ctx)); err != nil {
			_ = s.InMemoryStore.DeleteSession(ctx, clientID)
			return err
		}
		ids, err := s.ListSessions(ctx)
		if err == nil {
			s.owners.retain(ids)
		}
	}
	return nil
}

func (s *mcpHTTPSessionStore) DeleteSession(ctx context.Context, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	principal := ""
	if s.owners != nil {
		principal = s.owners.principalForSession(clientID)
	}
	if err := s.InMemoryStore.DeleteSession(ctx, clientID); err != nil {
		return err
	}
	if s.onDelete != nil {
		s.onDelete(clientID, principal)
	}
	return nil
}

func (s *mcpHTTPSessionStore) Delete(ctx context.Context, clientID string) error {
	return s.DeleteSession(ctx, clientID)
}

func mcpHTTPSessionKeyBelongsToClient(key, transportID, principal string) bool {
	if !strings.HasPrefix(key, "http/") {
		return false
	}
	ownerAndName := strings.TrimPrefix(key, "http/")
	owner, _, ok := strings.Cut(ownerAndName, "/")
	if !ok {
		return false
	}
	if principal != "" {
		return owner == principal+"::"+transportID
	}
	return owner == transportID
}

func parseMCPAllowedOrigins(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("origin policy is explicitly empty")
	}
	entries := strings.Split(raw, ",")
	origins := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		origin := strings.TrimSpace(entry)
		if origin == "" {
			return nil, fmt.Errorf("origin policy contains an empty entry")
		}
		u, err := url.Parse(origin)
		if err != nil || u == nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(u.Host, ":") {
			return nil, fmt.Errorf("SCOUT_MCP_ALLOWED_ORIGINS entry %q must be an exact HTTP(S) origin without credentials, path, query, or fragment", origin)
		}
		if strings.Contains(u.Hostname(), "*") {
			return nil, fmt.Errorf("SCOUT_MCP_ALLOWED_ORIGINS entry %q cannot contain wildcard hosts", origin)
		}
		if port := u.Port(); port != "" {
			portNumber, err := strconv.Atoi(port)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return nil, fmt.Errorf("SCOUT_MCP_ALLOWED_ORIGINS entry %q has an invalid port", origin)
			}
		}
		key := strings.ToLower(u.Scheme + "://" + u.Host)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		origins = append(origins, origin)
	}
	return origins, nil
}

// configuredMCPAllowedOrigins preserves the distinction between an unset
// allowlist (no global origin restriction requested) and an explicitly set
// but empty allowlist (a configuration mistake that must fail closed).
func configuredMCPAllowedOrigins() ([]string, bool, error) {
	raw, configured := os.LookupEnv("SCOUT_MCP_ALLOWED_ORIGINS")
	if !configured {
		return nil, false, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, true, fmt.Errorf("SCOUT_MCP_ALLOWED_ORIGINS is set but empty; configure one or more exact HTTP(S) origins or unset the variable")
	}
	origins, err := parseMCPAllowedOrigins(raw)
	return origins, true, err
}

func mcpOriginSubset(requested, allowed []string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		key, err := canonicalMCPOrigin(origin)
		if err != nil {
			return false
		}
		allowedSet[key] = struct{}{}
	}
	for _, origin := range requested {
		key, err := canonicalMCPOrigin(origin)
		if err != nil {
			return false
		}
		if _, ok := allowedSet[key]; !ok {
			return false
		}
	}
	return true
}

func canonicalMCPOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("invalid origin")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if (scheme != "http" && scheme != "https") || host == "" {
		return "", fmt.Errorf("invalid origin")
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if ip := net.ParseIP(host); ip != nil && strings.Contains(host, ":") {
		host = "[" + ip.String() + "]"
	}
	return scheme + "://" + host + ":" + port, nil
}

func loadMCPHTTPPrincipalPolicies(raw string) (map[string]mcpHTTPPrincipal, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var policies map[string]mcpHTTPPrincipal
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policies); err != nil {
		return nil, fmt.Errorf("decode SCOUT_MCP_HTTP_PRINCIPALS_JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode SCOUT_MCP_HTTP_PRINCIPALS_JSON: expected one JSON value")
		}
		return nil, fmt.Errorf("decode SCOUT_MCP_HTTP_PRINCIPALS_JSON: %w", err)
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("SCOUT_MCP_HTTP_PRINCIPALS_JSON must contain at least one principal")
	}
	seenTokens := make(map[string]string, len(policies))
	seenEndpoints := make(map[string]string, len(policies))
	for principal, policy := range policies {
		if len(principal) == 0 || len(principal) > 128 || strings.TrimSpace(principal) != principal {
			return nil, fmt.Errorf("principal IDs must contain 1-128 ASCII letters, numbers, periods, underscores, or hyphens")
		}
		for _, r := range principal {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
				return nil, fmt.Errorf("principal IDs must contain 1-128 ASCII letters, numbers, periods, underscores, or hyphens")
			}
		}
		if strings.TrimSpace(policy.Token) == "" {
			return nil, fmt.Errorf("principal %q must configure a non-empty token", principal)
		}
		if prior, exists := seenTokens[policy.Token]; exists {
			return nil, fmt.Errorf("principals %q and %q use the same token", prior, principal)
		}
		seenTokens[policy.Token] = principal
		if policy.ProxyServer != "" {
			if err := agent.ValidateProxyServer(policy.ProxyServer); err != nil {
				return nil, fmt.Errorf("principal %q has an invalid proxy_server", principal)
			}
		}
		endpoint, err := url.ParseRequestURI(policy.RemoteCDP)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https" && endpoint.Scheme != "ws" && endpoint.Scheme != "wss") {
			return nil, fmt.Errorf("principal %q has an invalid remote_cdp endpoint", principal)
		}
		endpointKey := normalizeMCPHTTPRemoteEndpoint(policy.RemoteCDP)
		if prior, exists := seenEndpoints[endpointKey]; exists && (!policy.IsolateBrowserContext || !policies[prior].IsolateBrowserContext) {
			return nil, fmt.Errorf("principals %q and %q share a remote_cdp endpoint; both must enable isolate_browser_context", prior, principal)
		}
		seenEndpoints[endpointKey] = principal
		if len(policy.AllowedOrigins) == 0 {
			return nil, fmt.Errorf("principal %q must configure at least one allowed_origin", principal)
		}
		validatedOrigins, err := parseMCPAllowedOrigins(strings.Join(policy.AllowedOrigins, ","))
		if err != nil {
			return nil, fmt.Errorf("principal %q has an invalid allowed origin policy: %w", principal, err)
		}
		policy.AllowedOrigins = validatedOrigins
		policies[principal] = policy
	}
	return policies, nil
}

// localNamedSessionConfig converts a principal's remote default configuration
// into a separately launched local browser. Prefer an explicit principal proxy
// and otherwise use the process proxy configured for local browsers.
func localNamedSessionConfig(cfg agent.SessionConfig, principal string, policies map[string]mcpHTTPPrincipal, processProxy string) agent.SessionConfig {
	cfg.RemoteCDP = ""
	if principal != "" {
		cfg.ProxyServer = policies[principal].ProxyServer
		if cfg.ProxyServer == "" {
			cfg.ProxyServer = processProxy
		}
	}
	return cfg
}

func normalizeMCPHTTPRemoteEndpoint(raw string) string {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	// Require isolation for common URL aliases of one browser endpoint, such
	// as host-case changes, an explicit default port, or a trailing slash.
	scheme := strings.ToLower(endpoint.Scheme)
	hostname := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	port := endpoint.Port()
	if (scheme == "http" || scheme == "ws") && port == "80" || (scheme == "https" || scheme == "wss") && port == "443" {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host += ":" + port
	}
	path := endpoint.EscapedPath()
	path = strings.TrimRight(path, "/")
	return scheme + "://" + host + path + "?" + endpoint.RawQuery
}

func mcpHTTPServeOptionsWithPrincipalSessions(addr, token string, principals map[string]mcpHTTPPrincipal, principalSessions *mcpHTTPPrincipalSessions) ([]mcp.HTTPOption, error) {
	return mcpHTTPServeOptionsWithPrincipalSessionsMode(addr, token, principals, principalSessions, false)
}

func mcpHTTPServeOptionsWithPrincipalSessionsMode(addr, token string, principals map[string]mcpHTTPPrincipal, principalSessions *mcpHTTPPrincipalSessions, stateless bool) ([]mcp.HTTPOption, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if len(principals) > 0 && token != "" {
		return nil, fmt.Errorf("configure either SCOUT_MCP_HTTP_TOKEN or SCOUT_MCP_HTTP_PRINCIPALS_JSON, not both")
	}
	if stateless && len(principals) == 0 {
		return nil, fmt.Errorf("stateless MCP HTTP requires SCOUT_MCP_HTTP_PRINCIPALS_JSON so every request has a stable authenticated owner")
	}
	if !loopback && token == "" && len(principals) == 0 {
		return nil, fmt.Errorf("MCP HTTP listener %q is not loopback; set SCOUT_MCP_HTTP_TOKEN or SCOUT_MCP_HTTP_PRINCIPALS_JSON to require bearer authentication", addr)
	}
	streamableMode := mcp.WithStreamableStateful()
	if stateless {
		streamableMode = mcp.WithStreamableStateless()
	}
	opts := []mcp.HTTPOption{streamableMode}
	if len(principals) > 0 {
		principalForRequest := func(r *http.Request) (string, bool) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got == r.Header.Get("Authorization") || got == "" {
				return "", false
			}
			for principal, policy := range principals {
				if len(got) == len(policy.Token) && subtle.ConstantTimeCompare([]byte(got), []byte(policy.Token)) == 1 {
					return principal, true
				}
			}
			return "", false
		}
		opts = append(opts,
			mcptransport.WithAuthorize(func(r *http.Request) error {
				_, err := principalSessions.authorize(r, principals, stateless)
				return err
			}),
			mcptransport.WithRequestContextFn(func(ctx context.Context, r *http.Request) context.Context {
				principal, _ := principalForRequest(r)
				return context.WithValue(ctx, mcpHTTPPrincipalKey{}, principal)
			}),
		)
	} else if token != "" {
		opts = append(opts, mcptransport.WithAuthorize(func(r *http.Request) error {
			want := "Bearer " + token
			got := r.Header.Get("Authorization")
			if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return fmt.Errorf("invalid bearer token")
			}
			return nil
		}))
	}
	return opts, nil
}

// writeCapture persists an image and returns its path, so a capture can be
// handed to the human instead of landing in the model's context as base64 (#79).
//
// A relative or empty path resolves under the OS temp dir, matching
// start_screen_recording's output_dir, which is the precedent for this in scout.
func writeCapture(outputPath, kind, format string, data []byte) (string, error) {
	if format == "" {
		format = "png"
	}
	path := outputPath
	if path != "" && hasPathTraversal(path) {
		return "", fmt.Errorf("screenshot: output path must not contain path traversal")
	}
	if path == "" {
		path = filepath.Join(os.TempDir(),
			fmt.Sprintf("scout-%s-%d.%s", kind, time.Now().UnixNano(), format))
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(os.TempDir(), path)
	}
	path, err := sanitizeCapturePath(path)
	if err != nil {
		return "", err
	}
	// Create the parent so an explicit output_path into a fresh directory works
	// rather than failing on a missing dir the caller cannot see.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("screenshot: create output dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("screenshot: output path already exists; choose a new path: %s", path)
		}
		return "", fmt.Errorf("screenshot: write %s: %w", path, err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		err = chmodErr
	} else {
		var written int
		written, err = file.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
	}
	closeErr := file.Close()
	if err != nil {
		return "", fmt.Errorf("screenshot: write %s: %w", path, err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("screenshot: close %s: %w", path, closeErr)
	}
	complete = true
	return path, nil
}

func newMCPOutputPath(path, kind string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s path is required", kind)
	}
	if hasPathTraversal(path) {
		return "", fmt.Errorf("%s: output path must not contain path traversal", kind)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(os.TempDir(), path)
	}
	path, err := sanitizeCapturePath(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("%s output path already exists; choose a new path: %s", kind, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s: inspect output path: %w", kind, err)
	}
	return path, nil
}

func resolveMCPOutputDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if hasPathTraversal(path) {
		return "", fmt.Errorf("screen recording: output directory must not contain path traversal")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(os.TempDir(), path)
	}
	clean, err := sanitizeCapturePath(filepath.Join(path, ".scout-output-check"))
	if err != nil {
		return "", err
	}
	return filepath.Dir(clean), nil
}

func hasPathTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func sanitizeCapturePath(path string) (string, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	if hasPathTraversal(path) {
		return "", fmt.Errorf("output path must not contain path traversal")
	}
	checkProtected := func(candidate string) error {
		lower := strings.ToLower(filepath.ToSlash(candidate))
		for _, marker := range []string{"/.ssh/", "/.aws/", "/.config/", "/.gnupg/", "/.kube/", "/.docker/", "/.env", "/credentials", "/id_rsa", "/id_ed25519"} {
			if strings.Contains(lower, marker) {
				return fmt.Errorf("output path targets a protected credential location")
			}
		}
		for _, root := range []string{"/etc", "/bin", "/sbin", "/usr", "/System", "/Library/Keychains", "/root"} {
			if candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator)) {
				return fmt.Errorf("output path targets a protected system location")
			}
		}
		return nil
	}
	if err := checkProtected(absPath); err != nil {
		return "", err
	}
	// Resolve symlinks in the nearest existing parent so a safe-looking path
	// cannot redirect a new file into a protected directory.
	parent := filepath.Dir(absPath)
	missing := make([]string, 0, 4)
	for {
		if _, statErr := os.Lstat(parent); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect output path: %w", statErr)
		}
		missing = append(missing, filepath.Base(parent))
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("could not resolve output path")
		}
		parent = next
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve output path: %w", err)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolvedParent = filepath.Join(resolvedParent, missing[i])
	}
	resolved := filepath.Join(resolvedParent, filepath.Base(absPath))
	if err := checkProtected(resolved); err != nil {
		return "", err
	}
	return absPath, nil
}

func artifactReference(path, kind, format string, data []byte) *agent.ArtifactReference {
	mime := "application/octet-stream"
	switch strings.ToLower(format) {
	case "png":
		mime = "image/png"
	case "jpeg", "jpg":
		mime = "image/jpeg"
	case "pdf":
		mime = "application/pdf"
	case "zip":
		mime = "application/zip"
	case "webm":
		mime = "video/webm"
	case "mp4":
		mime = "video/mp4"
	case "md":
		mime = "text/markdown; charset=utf-8"
	case "txt":
		mime = "text/plain; charset=utf-8"
	case "json":
		mime = "application/json"
	}
	digest := sha256.Sum256(data)
	return &agent.ArtifactReference{Path: path, Kind: kind, MIMEType: mime, SizeBytes: int64(len(data)), SHA256: fmt.Sprintf("%x", digest)}
}

func writePrivateTextArtifact(outputPath, kind, ext, content string) (string, error) {
	path := strings.TrimSpace(outputPath)
	if path != "" && hasPathTraversal(path) {
		return "", fmt.Errorf("snapshot: output path must not contain path traversal")
	}
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("scout-%s-%d.%s", kind, time.Now().UnixNano(), ext))
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(os.TempDir(), path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absPath, err = sanitizeCapturePath(absPath)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return "", fmt.Errorf("snapshot: create output directory: %w", err)
	}
	file, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("snapshot: output path already exists; choose a new path: %s", absPath)
		}
		return "", fmt.Errorf("snapshot: create artifact: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(absPath)
		}
	}()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.WriteString(content)
	}
	closeErr := file.Close()
	if err != nil {
		return "", fmt.Errorf("snapshot: write artifact: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("snapshot: close artifact: %w", closeErr)
	}
	complete = true
	return absPath, nil
}

func readPrivateSnapshot(mu *sync.Mutex, registered map[string][32]byte, owners map[string]string, requester string, input ReadPageSnapshotInput) (*PageSnapshotExcerpt, error) {
	path, runes, err := loadPrivateSnapshot(mu, registered, owners, requester, input.Path)
	if err != nil {
		return nil, err
	}
	total := len(runes)
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 8_000
	}
	if limit > maxPageSnapshotExcerptChars {
		limit = maxPageSnapshotExcerptChars
	}
	end := min(total, offset+limit)
	return &PageSnapshotExcerpt{Path: path, Content: string(runes[offset:end]), ContentTrust: agent.ContentTrustUntrustedPage, Offset: offset, Length: end - offset, Total: total, Truncated: end < total}, nil
}

func searchPrivateSnapshot(mu *sync.Mutex, registered map[string][32]byte, owners map[string]string, requester string, input SearchPageSnapshotInput) (*SearchPageSnapshotResult, error) {
	path, runes, err := loadPrivateSnapshot(mu, registered, owners, requester, input.Path)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return nil, fmt.Errorf("snapshot search query must not be empty")
	}
	queryRunes := []rune(query)
	if len(queryRunes) > 500 {
		return nil, fmt.Errorf("snapshot search query is limited to 500 characters")
	}
	contextChars := input.ContextChars
	if contextChars <= 0 {
		contextChars = 120
	}
	if contextChars > 500 {
		contextChars = 500
	}
	lowerContent := make([]rune, len(runes))
	for i, r := range runes {
		lowerContent[i] = unicode.ToLower(r)
	}
	lowerQuery := make([]rune, len(queryRunes))
	for i, r := range queryRunes {
		lowerQuery[i] = unicode.ToLower(r)
	}
	failure := make([]int, len(lowerQuery))
	for i, j := 1, 0; i < len(lowerQuery); i++ {
		for j > 0 && lowerQuery[i] != lowerQuery[j] {
			j = failure[j-1]
		}
		if lowerQuery[i] == lowerQuery[j] {
			j++
		}
		failure[i] = j
	}
	result := &SearchPageSnapshotResult{Path: path, ContentTrust: agent.ContentTrustUntrustedPage, Query: query, TotalChars: len(runes)}
	for i, j := 0, 0; i < len(lowerContent); i++ {
		for j > 0 && lowerContent[i] != lowerQuery[j] {
			j = failure[j-1]
		}
		if lowerContent[i] == lowerQuery[j] {
			j++
		}
		if j == len(lowerQuery) {
			charOffset := i - len(lowerQuery) + 1
			left := max(0, charOffset-contextChars)
			right := min(len(runes), i+1+contextChars)
			result.TotalMatches++
			if len(result.Matches) < 50 {
				result.Matches = append(result.Matches, SnapshotMatch{Offset: charOffset, Text: string(runes[left:right]), ContentTrust: agent.ContentTrustUntrustedPage})
			}
			j = 0
		}
	}
	result.Truncated = result.TotalMatches > len(result.Matches)
	return result, nil
}

func deletePrivateSnapshot(mu *sync.Mutex, registered map[string][32]byte, owners map[string]string, sizes map[string]int64, totalBytes *int64, ownerBytes map[string]int64, ownerCounts map[string]int, requester, requestedPath string) error {
	path, err := filepath.Abs(strings.TrimSpace(requestedPath))
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	_, ok := registered[path]
	owner, hasOwner := owners[path]
	if !ok || !hasOwner || owner != requester {
		return fmt.Errorf("artifact path is not registered for this MCP client")
	}
	info, err := os.Lstat(path)
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		err = fmt.Errorf("snapshot artifact must be a regular file")
	}
	missing := errors.Is(err, os.ErrNotExist)
	if err == nil {
		err = os.Remove(path)
		missing = errors.Is(err, os.ErrNotExist)
	}
	if err == nil || missing {
		delete(registered, path)
		delete(owners, path)
		if size, hasSize := sizes[path]; hasSize {
			*totalBytes -= size
			ownerBytes[requester] -= size
			ownerCounts[requester]--
			delete(sizes, path)
			if ownerCounts[requester] <= 0 {
				delete(ownerCounts, requester)
				delete(ownerBytes, requester)
			}
		}
		return nil
	}
	return fmt.Errorf("delete snapshot artifact: %w", err)
}

func loadPrivateSnapshot(mu *sync.Mutex, registered map[string][32]byte, owners map[string]string, requester, requestedPath string) (string, []rune, error) {
	path, err := filepath.Abs(strings.TrimSpace(requestedPath))
	if err != nil {
		return "", nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	expected, ok := registered[path]
	owner, hasOwner := owners[path]
	if !ok || !hasOwner || owner != requester {
		return "", nil, fmt.Errorf("artifact path is not registered for this MCP client")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("snapshot artifact unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("snapshot artifact must be a regular file")
	}
	file, err := openPrivateArtifact(path)
	if err != nil {
		return "", nil, fmt.Errorf("snapshot artifact unavailable: %w", err)
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("inspect snapshot artifact: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return "", nil, fmt.Errorf("snapshot artifact must be a regular file")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", nil, fmt.Errorf("read snapshot artifact: %w", err)
	}
	digest := sha256.Sum256(data)
	if digest != expected {
		return "", nil, fmt.Errorf("snapshot artifact changed after creation; save a fresh snapshot before reading")
	}
	return path, []rune(string(data)), nil
}

func agentFileSHA256(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func runMCP(httpAddr, profile, requiredTools string, taskStorePaths ...string) error {
	storePath := ""
	if len(taskStorePaths) > 0 {
		storePath = taskStorePaths[0]
	}
	return runMCPWithCurated(httpAddr, profile, requiredTools, storePath, false)
}
