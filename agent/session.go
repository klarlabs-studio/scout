// Package agent provides a high-level, agent-optimized API for browser automation.
//
// Unlike the core browse package which follows Gin's middleware/handler pattern for
// human developers, the agent package is designed for AI agents and programmatic callers.
// It provides:
//   - Stateful sessions with automatic page lifecycle management
//   - Structured JSON-serializable results (not plain strings)
//   - Built-in retry and auto-wait on all operations
//   - High-level compound actions (FillForm, ExtractTable, ClickAndWait)
//   - Snapshot-based page state for agent context windows
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	browse "go.klarlabs.de/scout"
	cdpclient "go.klarlabs.de/scout/internal/cdp"
	waitutil "go.klarlabs.de/scout/internal/wait"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type actionSpanLinksContextKey struct{}
type recoveryOperationOrdinalContextKey struct{}
type expectedFrameIDKey struct{}

type frameRouteLevel struct {
	parent        *browse.Page
	child         *browse.Page
	ownerSelector string
	parentFrameID string
	frameID       string
	detached      *atomic.Bool
	unsubscribe   func()
}

func withActionSpanLink(ctx context.Context, sc trace.SpanContext) context.Context {
	if ctx == nil || !sc.IsValid() {
		return ctx
	}
	links, _ := ctx.Value(actionSpanLinksContextKey{}).([]trace.SpanContext)
	links = append(append([]trace.SpanContext(nil), links...), sc)
	return context.WithValue(ctx, actionSpanLinksContextKey{}, links)
}

func (s *Session) startLifecycleSpan(ctx context.Context, operation string) (context.Context, trace.Span, time.Time) {
	if ctx == nil {
		ctx = context.Background()
	}
	startedAt := time.Now()
	if s == nil || s.tracer == nil {
		return ctx, nil, startedAt
	}
	ctx, span := s.tracer.Start(ctx, "scout.browser.session."+operation,
		trace.WithAttributes(
			attribute.String("scout.run_id", s.runID),
			attribute.String("scout.lifecycle.operation", operation),
			attribute.String("scout.browser.ownership", s.browserOwnership),
			attribute.Bool("scout.browser.context_isolated", s.isolateRemoteBrowserContext),
		),
	)
	return ctx, span, startedAt
}

func endLifecycleSpan(span trace.Span, startedAt time.Time, err error) {
	if span == nil {
		return
	}
	outcome := "success"
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			outcome = "deadline_exceeded"
		case errors.Is(err, context.Canceled):
			outcome = "canceled"
		default:
			outcome = "failed"
		}
		span.SetStatus(codes.Error, outcome)
	}
	span.SetAttributes(
		attribute.String("scout.outcome", outcome),
		attribute.Int64("scout.duration_ms", time.Since(startedAt).Milliseconds()),
	)
	span.End()
}

// Session manages a stateful browser automation session for an agent.
// All methods are goroutine-safe via an internal mutex.
type Session struct {
	mu                          sessionMutex
	taskRunMu                   sync.Mutex
	taskRuns                    map[string]taskRunEntry
	taskRunStore                TaskRunStore
	browser                     browse.Browser
	page                        *browse.Page
	mainPage                    *browse.Page
	frameRoute                  *browse.Page
	frameRouteStack             []frameRouteLevel
	orphanedFramePages          []*browse.Page
	frameOwnerSelector          string
	frameParentID               string
	frameDetached               *atomic.Bool
	frameDetachUnsubscribe      func()
	timeout                     time.Duration
	contentOpts                 ContentOptions
	network                     *networkState
	diffInstalled               bool
	closed                      bool
	stealth                     bool
	tabs                        *tabManager
	recording                   *recording
	history                     []HistoryEntry
	tracing                     bool
	trace                       *traceState
	screenRec                   *screenRecording
	frameID                     string
	frameContextID              int64
	headless                    bool
	userAgent                   string
	viewport                    [2]int
	allowPrivateIPs             bool
	allowedOrigins              []string
	proxyServer                 string
	proxyAppliedByScout         bool
	remoteBrowserURL            string
	isolateRemoteBrowserContext bool
	remoteBrowserContextID      string
	browserOwnership            string
	recoverTimeoutN             int
	consecutiveTimeouts         int
	lastError                   string
	lastSuccessAt               time.Time
	lastRecoveryAt              time.Time
	inflightCommands            int
	sessionDead                 bool
	deadReason                  string
	playbookCheckpoints         map[string]playbookCheckpoint
	playbookCheckpointOrder     []string
	playbookCheckpointBytes     int
	runID                       string
	unknownOutcomeSequence      int
	operationOrdinal            int
	tracer                      trace.Tracer
	lastActionName              string
	lastActionStartedAt         time.Time
	lastActionEndedAt           time.Time
	heldKeyModifiers            int
	heldKeys                    []string
}

// nextOperationOrdinal allocates a stable per-session sequence for action
// evidence. Callers hold s.mu while invoking this method.
func (s *Session) nextOperationOrdinal() int {
	if s == nil {
		return 0
	}
	s.operationOrdinal++
	return s.operationOrdinal
}

func (s *Session) newActionReceipt(action, target, targetID, outcome, evidence, recovery string, verified bool, ordinal int, postcondition *Postcondition, parent ...context.Context) *ActionReceipt {
	if s == nil {
		return nil
	}
	if s.runID == "" {
		s.runID = newRunID()
	}
	frameID := ""
	if s.page != nil && targetID != "" && targetID == s.page.TargetID() {
		frameID = s.frameID
	}
	state := verificationStateFor(ActionOutcome(outcome), verified, postcondition)
	var ctx context.Context
	if len(parent) > 0 {
		ctx = parent[0]
	}
	recoveryOrdinal := 0
	if ctx != nil {
		recoveryOrdinal, _ = ctx.Value(recoveryOperationOrdinalContextKey{}).(int)
	}
	policy := s.actionPolicyEvidence()
	policyID := policy.ID
	receipt := &ActionReceipt{
		SchemaVersion: 1, ContentTrust: ContentTrustUntrustedPage, RunID: s.runID,
		OperationOrdinal: ordinal, RecoveryOfOperationOrdinal: recoveryOrdinal, PolicyID: policyID,
		Policy: policy,
		Action: action, BrowserOwnership: s.browserOwnership, Target: target, TargetID: targetID,
		BrowserInstanceID: s.browserInstanceID(), FrameID: frameID, Outcome: ActionOutcome(outcome),
		Verified: verified, Verification: state, Evidence: evidence, RecoveryHint: recovery,
		Postcondition: postcondition,
	}
	endedAt := time.Now()
	startedAt := endedAt
	if s.lastActionName == traceActionForResult(action) && !s.lastActionStartedAt.IsZero() {
		startedAt = s.lastActionStartedAt
		if !s.lastActionEndedAt.IsZero() {
			endedAt = s.lastActionEndedAt
		}
	}
	receipt.StartedAt = startedAt.UnixMilli()
	receipt.DurationMS = endedAt.Sub(startedAt).Milliseconds()
	if s.tracing && s.trace != nil {
		receipt.TraceID = s.trace.run.TraceID
		if receipt.Policy != nil && receipt.TraceID != "" {
			receipt.Policy.EvidenceReference = "trace:" + receipt.TraceID + "#url-policy.json"
			receipt.Policy.DecisionWindow = s.actionPolicyDecisionWindow(targetID, startedAt, endedAt)
		}
		for i := len(s.trace.events) - 1; i >= 0; i-- {
			if s.trace.events[i].OperationOrdinal == ordinal {
				receipt.TraceAction = s.trace.events[i].ActionOrdinal
				receipt.DeadlineAt = s.trace.events[i].DeadlineAt
				break
			}
		}
	}
	if len(parent) > 0 && parent[0] != nil {
		if deadline, ok := parent[0].Deadline(); ok {
			receipt.DeadlineAt = deadline.UnixMilli()
		}
	}
	if s.tracer != nil {
		spanCtx := context.Background()
		if ctx != nil {
			spanCtx = ctx
		}
		var spanOpts []trace.SpanStartOption
		if links, ok := spanCtx.Value(actionSpanLinksContextKey{}).([]trace.SpanContext); ok {
			for _, linked := range links {
				spanOpts = append(spanOpts, trace.WithLinks(trace.Link{SpanContext: linked}))
			}
		}
		spanOpts = append(spanOpts, trace.WithTimestamp(startedAt))
		_, span := s.tracer.Start(spanCtx, "scout.browser.action", spanOpts...)
		if span.SpanContext().IsValid() {
			receipt.OTelTraceID = span.SpanContext().TraceID().String()
			receipt.OTelSpanID = span.SpanContext().SpanID().String()
		}
		attrs := []attribute.KeyValue{
			attribute.String("scout.run_id", receipt.RunID),
			attribute.Int("scout.operation_ordinal", receipt.OperationOrdinal),
			attribute.String("scout.action", receipt.Action),
			attribute.String("scout.policy_id", receipt.PolicyID),
			attribute.String("scout.outcome", string(receipt.Outcome)),
			attribute.Bool("scout.verified", receipt.Verified),
			attribute.Int64("scout.duration_ms", receipt.DurationMS),
		}
		if receipt.RecoveryOfOperationOrdinal > 0 {
			attrs = append(attrs, attribute.Int("scout.recovery_of_operation_ordinal", receipt.RecoveryOfOperationOrdinal))
		}
		if receipt.TargetID != "" {
			attrs = append(attrs, attribute.String("scout.target_id", receipt.TargetID))
		}
		if receipt.FrameID != "" {
			attrs = append(attrs, attribute.String("scout.frame_id", receipt.FrameID))
		}
		if receipt.TraceID != "" {
			attrs = append(attrs, attribute.String("scout.trace_id", receipt.TraceID))
		}
		if receipt.OTelTraceID != "" {
			attrs = append(attrs, attribute.String("scout.otel_trace_id", receipt.OTelTraceID))
		}
		if receipt.OTelSpanID != "" {
			attrs = append(attrs, attribute.String("scout.otel_span_id", receipt.OTelSpanID))
		}
		if receipt.TraceAction > 0 {
			attrs = append(attrs, attribute.Int("scout.trace_action_ordinal", receipt.TraceAction))
		}
		if receipt.DeadlineAt > 0 {
			attrs = append(attrs, attribute.Int64("scout.deadline_at_ms", receipt.DeadlineAt))
		}
		span.SetAttributes(attrs...)
		if receipt.Outcome == OutcomeUnknown || receipt.Outcome == OutcomeActionFailed || receipt.Outcome == OutcomePostconditionFailed || receipt.Outcome == OutcomeValueMismatch {
			span.SetStatus(codes.Error, string(receipt.Outcome))
		}
		span.End(trace.WithTimestamp(endedAt))
	}
	return receipt
}

func (s *Session) actionPolicyDecisionWindow(targetID string, startedAt, endedAt time.Time) *ActionPolicyDecisionWindow {
	if s == nil || !s.tracing || s.trace == nil || targetID == "" || startedAt.IsZero() || endedAt.IsZero() {
		return nil
	}
	window := &ActionPolicyDecisionWindow{
		TargetID: targetID, StartedAtMS: startedAt.UnixMilli(), EndedAtMS: endedAt.UnixMilli(),
		DecisionIndices: []int{},
	}
	for _, decision := range s.trace.policyDecisions {
		if decision.TargetID != targetID || decision.PolicyID != taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs) || decision.Timestamp < window.StartedAtMS || decision.Timestamp > window.EndedAtMS {
			continue
		}
		window.DecisionIndices = append(window.DecisionIndices, decision.Index)
		if decision.Allowed {
			window.Allowed++
		} else {
			window.Blocked++
		}
	}
	return window
}

func (s *Session) actionPolicyEvidence() *ActionPolicyEvidence {
	if s == nil {
		return nil
	}
	origins := append([]string{}, s.allowedOrigins...)
	if normalized, err := normalizeTaskOriginSet(origins); err == nil {
		origins = sortedTaskOrigins(normalized)
	}
	enforcement := "session_page_policy"
	if len(origins) == 0 {
		enforcement = "no_origin_allowlist"
	}
	remoteHostPolicy := "not_applicable"
	if s.browserOwnership == "remote_cdp" {
		remoteHostPolicy = "unverified"
	}
	return &ActionPolicyEvidence{
		ID: taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs), AllowedOrigins: origins,
		AllowPrivateIPs: s.allowPrivateIPs, Enforcement: enforcement,
		ProxyConfigured: s.proxyServer != "", ProxyAppliedByScout: s.proxyAppliedByScout,
		RemoteHostPolicy:     remoteHostPolicy,
		KnownUnobservedPaths: []string{"service_worker_requests", "websocket_handshakes"},
	}
}

func verificationStateFor(outcome ActionOutcome, verified bool, postcondition *Postcondition) VerificationState {
	if outcome == OutcomeUnknown {
		return VerificationUnknown
	}
	if verified {
		return VerificationVerified
	}
	switch outcome {
	case OutcomeActionFailed, OutcomePostconditionFailed, OutcomeValueMismatch, OutcomeResponseFailed:
		return VerificationRejected
	}
	if postcondition != nil {
		return VerificationRejected
	}
	return VerificationUnverified
}

func actionReceiptDeadline(ctx context.Context, receipt *ActionReceipt) {
	if receipt == nil || ctx == nil {
		return
	}
	if deadline, ok := ctx.Deadline(); ok {
		receipt.DeadlineAt = deadline.UnixMilli()
	}
}

type expectedTargetIDKey struct{}
type expectedPageURLKey struct{}

type expectedDocumentKey struct{}

type taskDocumentIdentity struct {
	FrameID  string
	LoaderID string
}

type targetTransitionStartedKey struct{}

// WithExpectedTargetID binds an operation context to the target observed by an
// earlier Scout result. Operations reject the request if Scout has switched
// pages before the action acquires the session lock.
func WithExpectedTargetID(ctx context.Context, targetID string) context.Context {
	if targetID == "" {
		return ctx
	}
	return context.WithValue(ctx, expectedTargetIDKey{}, targetID)
}

func withExpectedPageURL(ctx context.Context, pageURL string) context.Context {
	if pageURL == "" {
		return ctx
	}
	return context.WithValue(ctx, expectedPageURLKey{}, pageURL)
}

func withExpectedTaskDocument(ctx context.Context, identity taskDocumentIdentity) context.Context {
	return context.WithValue(ctx, expectedDocumentKey{}, identity)
}

func (s *Session) taskDocumentIdentityContext(ctx context.Context) (taskDocumentIdentity, error) {
	if s.page == nil {
		return taskDocumentIdentity{}, fmt.Errorf("task document is unavailable")
	}
	raw, err := s.page.CallContext(ctx, "Page.getFrameTree", nil)
	if err != nil {
		return taskDocumentIdentity{}, fmt.Errorf("read task document identity: %w", err)
	}
	var response struct {
		FrameTree frameTreeNode `json:"frameTree"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return taskDocumentIdentity{}, fmt.Errorf("decode task document identity: %w", err)
	}
	wantedFrame := s.frameID
	if wantedFrame == "" {
		wantedFrame = response.FrameTree.Frame.ID
	}
	var find func(frameTreeNode) (taskDocumentIdentity, bool)
	find = func(node frameTreeNode) (taskDocumentIdentity, bool) {
		if node.Frame.ID == wantedFrame {
			return taskDocumentIdentity{FrameID: node.Frame.ID, LoaderID: node.Frame.LoaderID}, true
		}
		for _, child := range node.ChildFrames {
			if identity, ok := find(child); ok {
				return identity, true
			}
		}
		return taskDocumentIdentity{}, false
	}
	identity, ok := find(response.FrameTree)
	if !ok || identity.FrameID == "" || identity.LoaderID == "" {
		return taskDocumentIdentity{}, fmt.Errorf("task document identity is unavailable for frame %q", wantedFrame)
	}
	return identity, nil
}

func (s *Session) checkExpectedTaskDocumentContext(ctx context.Context) error {
	expected, ok := ctx.Value(expectedDocumentKey{}).(taskDocumentIdentity)
	if !ok {
		return nil
	}
	actual, err := s.taskDocumentIdentityContext(ctx)
	if err != nil {
		return &OperationError{
			Phase: "task_binding", Cause: "document_identity_unavailable", TargetID: s.page.TargetID(),
			RecoveryHint:  "Inspect the current page and start a new task after confirming its state.",
			OriginalError: "could not verify the task document identity before dispatch",
		}
	}
	if actual != expected {
		return &OperationError{
			Phase: "task_binding", Cause: "document_changed", TargetID: s.page.TargetID(),
			Detail:        "the selected frame document changed after task identity was captured; no further task actions were dispatched",
			RecoveryHint:  "Inspect the current page and start a new task after confirming its state.",
			OriginalError: "task document changed before dispatch",
		}
	}
	return nil
}

func (s *Session) checkExpectedPageURLContext(ctx context.Context) error {
	expected, ok := ctx.Value(expectedPageURLKey{}).(string)
	if !ok {
		return nil
	}
	actual, err := s.page.URL()
	if err != nil {
		return &OperationError{
			Phase: "task_binding", Cause: "page_state_unavailable", TargetID: s.page.TargetID(),
			RecoveryHint:  "Inspect the current page and start a new task after confirming its state.",
			OriginalError: "could not verify the task page URL before dispatch",
		}
	}
	if actual != expected {
		return &OperationError{
			Phase: "task_binding", Cause: "page_changed", TargetID: s.page.TargetID(),
			RecoveryHint:  "Inspect the current page and start a new task after confirming its state.",
			Detail:        "the page URL changed after task identity was captured; no task actions were dispatched",
			OriginalError: "task page changed before dispatch",
		}
	}
	return nil
}

func (s *Session) checkExpectedFrameIDContext(ctx context.Context) error {
	expected, ok := ctx.Value(expectedFrameIDKey{}).(string)
	if !ok {
		return nil
	}
	if s.frameID != expected {
		return &OperationError{
			Phase: "task_binding", Cause: "frame_changed", TargetID: s.page.TargetID(),
			RecoveryHint:  "Select the intended frame and start a new task after confirming its state.",
			Detail:        "the selected frame changed after task identity was captured; no further task actions were dispatched",
			OriginalError: "task frame changed before dispatch",
		}
	}
	return nil
}

func (s *Session) checkExpectedTargetIDContext(ctx context.Context) error {
	expected, _ := ctx.Value(expectedTargetIDKey{}).(string)
	if expected == "" {
		return nil
	}
	actual := ""
	if s.page != nil {
		actual = s.page.TargetID()
	}
	if actual != expected {
		if transitioning, _ := ctx.Value(targetTransitionStartedKey{}).(bool); transitioning {
			return nil
		}
		return &OperationError{Phase: "target_binding", Cause: "stale_target", Detail: fmt.Sprintf("expected target %s, but active target is %s; inspect the current page before retrying", expected, actual), OriginalError: "target identity mismatch"}
	}
	return nil
}

func beginExpectedTargetTransition(ctx context.Context) context.Context {
	if expected, _ := ctx.Value(expectedTargetIDKey{}).(string); expected == "" {
		return ctx
	}
	return context.WithValue(ctx, targetTransitionStartedKey{}, true)
}

// sessionMutex preserves the ordinary mutex API while allowing deadline-aware
// operations to abandon the queue without leaving a goroutine waiting on Lock.
type sessionMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *sessionMutex) init() {
	m.once.Do(func() {
		m.gate = make(chan struct{}, 1)
		m.gate <- struct{}{}
	})
}

func (m *sessionMutex) Lock() {
	_ = m.LockContext(context.Background())
}

func (m *sessionMutex) LockContext(ctx context.Context) error {
	m.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.gate:
		if err := ctx.Err(); err != nil {
			m.gate <- struct{}{}
			return err
		}
		return nil
	}
}

func (m *sessionMutex) Unlock() {
	m.init()
	m.gate <- struct{}{}
}

// SessionConfig configures a new Session.
type SessionConfig struct {
	Headless        bool
	Timeout         time.Duration
	UserAgent       string
	Viewport        [2]int // [width, height], zero means default
	AllowPrivateIPs bool   // Allow navigation to private/loopback IPs
	// ProxyServer routes a locally launched browser through an HTTP(S) or SOCKS5 proxy.
	// Configure the proxy's own allowlist there for browser-wide egress control;
	// Scout's AllowedOrigins remains a page-level check and is not sent to proxy.
	// It cannot be combined with RemoteCDP; remote browser hosts must configure
	// their proxy independently.
	ProxyServer string
	// AllowedOrigins restricts top-level navigation, redirects, and page-target
	// Fetch-interceptable HTTP(S) requests to exact origins. It does not govern
	// service-worker-owned traffic or WebSocket handshakes; configure an enforcing
	// proxy with ProxyServer for browser-wide egress control. A RemoteCDP host
	// must enforce its own network policy.
	AllowedOrigins []string
	RemoteCDP      string   // Browser WebSocket URL or HTTP(S) DevTools endpoint (skips local launch)
	ChromePath     string   // Executable used for a locally launched browser; overrides SCOUT_CHROME_PATH
	ChromeArgs     []string // Extra args for locally launched Chrome
	// IsolateRemoteBrowserContext creates an ephemeral context on a remote
	// browser and cannot be combined with TargetID attachment. When
	// RemoteBrowserContextID is supplied, attach to that existing context
	// instead; the caller must obtain it from trusted durable state.
	IsolateRemoteBrowserContext bool
	RemoteBrowserContextID      string
	TargetID                    string               // Attach to this existing target instead of selecting/creating a page
	Stealth                     bool                 // Apply anti-detection stealth patches to every new page
	TracerProvider              trace.TracerProvider // Optional caller-owned OpenTelemetry provider
	// TaskRunStore optionally persists task ID reservations and results across
	// sessions or process restarts. Implementations shared across processes must
	// reserve IDs atomically and retain interrupted reservations for manual
	// reconciliation. Implement TaskRunProgressStore to persist per-action
	// dispatch boundaries; without it, an initial cursor does not prove that no
	// browser action was dispatched. See TaskRunStore.
	TaskRunStore TaskRunStore
}

// NewSession creates and launches a new browser session.
func NewSession(cfg SessionConfig) (*Session, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	return NewSessionContext(ctx, cfg)
}

// NewSessionContext creates and launches a browser session within ctx. The
// context bounds startup only; a successfully launched session is independent
// of the lifetime of its creation context.
func NewSessionContext(ctx context.Context, cfg SessionConfig) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.TargetID != "" && cfg.RemoteCDP == "" {
		return nil, fmt.Errorf("target ID attachment requires RemoteCDP")
	}
	if cfg.ProxyServer != "" {
		if cfg.RemoteCDP != "" {
			return nil, fmt.Errorf("ProxyServer cannot configure an already-running remote CDP browser; configure its network proxy on the browser host")
		}
		if err := ValidateProxyServer(cfg.ProxyServer); err != nil {
			return nil, err
		}
	}
	if cfg.IsolateRemoteBrowserContext && cfg.TargetID != "" {
		return nil, fmt.Errorf("target ID attachment cannot be combined with an isolated remote browser context")
	}
	if cfg.RemoteBrowserContextID != "" && (cfg.RemoteCDP == "" || cfg.IsolateRemoteBrowserContext) {
		return nil, fmt.Errorf("existing remote BrowserContext ID requires RemoteCDP and cannot be combined with creating a new isolated context")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	ownership := "scout_launched"
	if cfg.RemoteCDP != "" {
		ownership = "remote_cdp"
	}
	opts := []browse.Option{
		browse.WithHeadless(cfg.Headless),
		browse.WithTimeout(cfg.Timeout),
	}
	if cfg.RemoteCDP == "" && len(cfg.ChromeArgs) > 0 {
		opts = append(opts, browse.WithChromeArgs(cfg.ChromeArgs...))
	}
	if cfg.RemoteCDP == "" && cfg.ChromePath != "" {
		opts = append(opts, browse.WithChromePath(cfg.ChromePath))
	}
	if cfg.UserAgent != "" {
		opts = append(opts, browse.WithUserAgent(cfg.UserAgent))
	}
	if cfg.ProxyServer != "" {
		opts = append(opts, browse.WithProxy(cfg.ProxyServer))
	}
	if cfg.Viewport[0] > 0 && cfg.Viewport[1] > 0 {
		opts = append(opts, browse.WithViewport(cfg.Viewport[0], cfg.Viewport[1]))
	}
	if cfg.AllowPrivateIPs {
		opts = append(opts, browse.WithAllowPrivateIPs(true))
	}
	if len(cfg.AllowedOrigins) > 0 {
		opts = append(opts, browse.WithAllowedOrigins(cfg.AllowedOrigins...))
	}
	opts = append(opts, browse.WithPolicyID(taskOriginPolicyID(cfg.AllowedOrigins, cfg.AllowPrivateIPs)))
	if cfg.RemoteCDP != "" {
		opts = append(opts, browse.WithRemoteCDP(cfg.RemoteCDP))
		if cfg.IsolateRemoteBrowserContext {
			opts = append(opts, browse.WithRemoteBrowserContext())
		} else if cfg.RemoteBrowserContextID != "" {
			opts = append(opts, browse.WithRemoteBrowserContextID(cfg.RemoteBrowserContextID))
		}
	}

	engine := browse.New(opts...)
	if err := engine.LaunchContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	network := &networkState{
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: 200,
	}

	session := &Session{
		browser:                     engine,
		timeout:                     cfg.Timeout,
		contentOpts:                 DefaultContentOptions(),
		stealth:                     cfg.Stealth,
		headless:                    cfg.Headless,
		userAgent:                   cfg.UserAgent,
		proxyServer:                 cfg.ProxyServer,
		proxyAppliedByScout:         cfg.ProxyServer != "" && cfg.RemoteCDP == "",
		browserOwnership:            ownership,
		viewport:                    cfg.Viewport,
		allowPrivateIPs:             cfg.AllowPrivateIPs,
		allowedOrigins:              append([]string(nil), cfg.AllowedOrigins...),
		remoteBrowserURL:            cfg.RemoteCDP,
		isolateRemoteBrowserContext: cfg.IsolateRemoteBrowserContext,
		remoteBrowserContextID:      cfg.RemoteBrowserContextID,
		network:                     network,
		recoverTimeoutN:             3,
		runID:                       newRunID(),
		taskRunStore:                cfg.TaskRunStore,
	}
	if cfg.TracerProvider != nil {
		session.tracer = cfg.TracerProvider.Tracer("go.klarlabs.de/scout/agent")
	}
	if cfg.TargetID != "" {
		attacher, ok := any(engine).(interface {
			PageContext(context.Context, string) (*browse.Page, error)
		})
		if !ok {
			return nil, errors.Join(fmt.Errorf("browser does not support target attachment"), engine.Close())
		}
		page, err := attacher.PageContext(ctx, cfg.TargetID)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("failed to attach to target %s: %w", cfg.TargetID, err), engine.Close())
		}
		if err := session.applyPageURLPolicy(page); err != nil {
			detachCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			detachErr := page.DetachContext(detachCtx)
			cancel()
			return nil, errors.Join(err, detachErr, engine.Close())
		}
		session.page = page
	}
	return session, nil
}

// ValidateProxyServer checks the proxy URL accepted by SessionConfig.ProxyServer.
// Proxy authentication is intentionally unsupported because Chromium receives
// this URL as a process argument.
func ValidateProxyServer(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("invalid ProxyServer: expected an absolute HTTP(S) or SOCKS5 proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
	default:
		return fmt.Errorf("invalid ProxyServer: use http, https, or socks5")
	}
	if parsed.User != nil {
		return fmt.Errorf("invalid ProxyServer: credentials in proxy URLs are unsupported; use a trusted local proxy without URL credentials")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid ProxyServer: proxy URL must not include a path, query, or fragment")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("invalid ProxyServer: port must be between 1 and 65535")
		}
	}
	return nil
}

// NewSessionFromBrowser creates a session from an existing Browser implementation.
// Session.Close calls Close on the supplied browser handle, so cleanup is
// delegated to that handle. If it is connected to remote CDP, set RemoteCDP in
// cfg so the session detaches its page instead of closing the remote target.
func NewSessionFromBrowser(b browse.Browser, cfg SessionConfig) *Session {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	network := &networkState{
		pending:      make(map[string]*NetworkCapture),
		pendingAll:   make(map[string]*NetworkCapture),
		historyLimit: 200,
	}

	session := &Session{
		browser:             b,
		timeout:             cfg.Timeout,
		contentOpts:         DefaultContentOptions(),
		stealth:             cfg.Stealth,
		headless:            cfg.Headless,
		userAgent:           cfg.UserAgent,
		proxyServer:         cfg.ProxyServer,
		proxyAppliedByScout: false,
		browserOwnership:    "injected_browser",
		viewport:            cfg.Viewport,
		allowPrivateIPs:     cfg.AllowPrivateIPs,
		allowedOrigins:      append([]string(nil), cfg.AllowedOrigins...),
		remoteBrowserURL:    cfg.RemoteCDP,
		network:             network,
		recoverTimeoutN:     3,
		runID:               newRunID(),
		taskRunStore:        cfg.TaskRunStore,
	}
	if cfg.TracerProvider != nil {
		session.tracer = cfg.TracerProvider.Tracer("go.klarlabs.de/scout/agent")
	}
	return session
}

// markIfDead inspects err and, if it looks like a dead CDP socket, sets the
// session-dead flag so a follow-up Status call surfaces it. Returns err unchanged.
// Caller must hold s.mu.
func (s *Session) markIfDead(err error) error {
	if err == nil {
		return nil
	}
	if isTargetGoneError(err) {
		return targetGoneOperationError("target_binding", s.page, err)
	}
	if reason := connectionDeadReason(err); reason != "" {
		s.sessionDead = true
		s.deadReason = reason
	}
	return err
}

// connectionDeadReason returns a short non-empty reason when err looks like a
// dead CDP socket (broken pipe, reset, websocket closed). Empty string means
// the connection is still considered usable.
func connectionDeadReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(msg, "connection reset"):
		return "connection_reset"
	case strings.Contains(msg, "use of closed network connection"):
		return "connection_closed"
	case strings.Contains(msg, "websocket: close"):
		return "websocket_closed"
	case strings.Contains(msg, "cdp: connection closed"):
		return "cdp_connection_closed"
	case strings.Contains(msg, "eof") && strings.Contains(msg, "websocket"):
		return "websocket_eof"
	}
	return ""
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(strings.ToLower(s), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

func (s *Session) wrapDetailedError(phase string, err error) error {
	msg := err.Error()
	op := &OperationError{Phase: phase, OriginalError: msg}
	if isTargetGoneError(err) {
		return targetGoneOperationError(phase, s.page, err)
	}
	if u, parseErr := url.Parse(msg); parseErr == nil && u != nil && u.Host != "" {
		op.URL = u.String()
	}
	if opErr, ok := err.(*net.OpError); ok {
		op.Cause = "connection_error"
		op.Detail = opErr.Op
	}
	if strings.Contains(strings.ToLower(msg), "404") {
		op.Cause = "http_404"
		op.StatusCode = 404
	}
	if strings.Contains(strings.ToLower(msg), "403") {
		op.Cause = "http_403"
		op.StatusCode = 403
	}
	if strings.Contains(strings.ToLower(msg), "401") {
		op.Cause = "http_401"
		op.StatusCode = 401
	}
	if strings.Contains(strings.ToLower(msg), "connection refused") {
		op.Cause = "connection_refused"
	}
	if strings.Contains(strings.ToLower(msg), "timeout") || strings.Contains(strings.ToLower(msg), "deadline exceeded") {
		op.Cause = "timeout"
	}
	if strings.Contains(strings.ToLower(msg), "browser") && strings.Contains(strings.ToLower(msg), "closed") {
		op.Cause = "browser_closed"
	}
	if op.Cause == "" {
		op.Cause = "unknown"
	}
	if op.StatusCode == 0 {
		for _, token := range strings.Fields(msg) {
			if len(token) == 3 {
				if n, convErr := strconv.Atoi(token); convErr == nil && n >= 100 && n <= 599 {
					op.StatusCode = n
					break
				}
			}
		}
	}
	return op
}

// isTargetGoneError recognizes CDP failures caused by a page target that has
// been closed or detached outside the current operation. Keep this narrower
// than connectionDeadReason: a gone page target does not necessarily mean the
// browser connection or the rest of the session is dead.
func isTargetGoneError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return containsAny(msg,
		"browser target is gone",
		"target closed",
		"target was closed",
		"target is closed",
		"no session with given id",
		"session with given id not found",
		"session not found",
		"inspected target navigated or closed",
	)
}

func targetGoneOperationError(phase string, page *browse.Page, err error) *OperationError {
	op := &OperationError{
		Phase:         phase,
		Cause:         "target_gone",
		OriginalError: err.Error(),
		RecoveryHint:  "The selected tab was closed or detached. List tabs, then explicitly open or switch to a live tab before retrying.",
	}
	if page != nil {
		op.TargetID = page.TargetID()
		op.Detail = fmt.Sprintf("browser target %s is no longer available", op.TargetID)
	}
	return op
}

// activeTargetGoneAfterFailure confirms a timed-out page command against the
// browser target registry. It is only used on failure paths so healthy actions
// do not pay an extra browser-level CDP round trip.
func (s *Session) activeTargetGoneAfterFailure(ctx context.Context) bool {
	if s.page == nil {
		return false
	}
	checker, ok := s.browser.(interface {
		HasTargetContext(context.Context, string) (bool, error)
	})
	if !ok {
		return false
	}
	parent := context.Background()
	if ctx != nil && ctx.Err() == nil {
		parent = ctx
	}
	checkCtx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()
	present, err := checker.HasTargetContext(checkCtx, s.page.TargetID())
	return err == nil && !present
}

func (s *Session) resetLockedContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.browserOwnership == "injected_browser" {
		return &OperationError{Phase: "reset", Cause: "injected_browser_reset_unsupported", OriginalError: "cannot reset a session backed by an injected browser", RecoveryHint: "Create a new session with a replacement browser handle. Scout left the caller-supplied browser open."}
	}
	s.releaseHeldKeysLocked()
	s.detachNetworkObserversLocked()
	if s.browser != nil {
		if err := s.browser.Close(); err != nil {
			return fmt.Errorf("failed to close browser before reset: %w", err)
		}
		s.browser = nil
	}
	// Closing a locally owned browser tears down its page targets. For RemoteCDP,
	// Close only disconnects Scout; the remote browser and its pages remain caller-owned.
	s.page = nil
	s.mainPage = nil
	s.frameRoute = nil
	s.orphanedFramePages = nil
	for i := range s.frameRouteStack {
		if s.frameRouteStack[i].unsubscribe != nil {
			s.frameRouteStack[i].unsubscribe()
		}
	}
	s.frameRouteStack = nil
	s.frameOwnerSelector = ""
	s.frameParentID = ""
	if s.frameDetachUnsubscribe != nil {
		s.frameDetachUnsubscribe()
		s.frameDetachUnsubscribe = nil
	}
	s.frameDetached = nil
	// Tab entries hold page handles from the old CDP connection. They cannot be
	// reused after reset, even when the remote browser itself remains alive.
	s.tabs = nil
	s.diffInstalled = false
	s.frameID = ""
	s.frameContextID = 0
	if s.network != nil {
		s.network.mu.Lock()
		s.network.enabled = false
		s.network.patterns = nil
		s.network.unsub = nil
		s.network.observedPages = make(map[string]*browse.Page)
		s.network.pending = make(map[string]*NetworkCapture)
		s.network.pendingAll = make(map[string]*NetworkCapture)
		s.network.history = nil
		s.network.droppedRequests = 0
		s.network.captureInterrupted = false
		s.network.observersInstalled = false
		s.network.observerMode = networkObserverNone
		s.network.mu.Unlock()
	}
	opts := []browse.Option{browse.WithHeadless(s.headless), browse.WithTimeout(s.timeout)}
	if s.userAgent != "" {
		opts = append(opts, browse.WithUserAgent(s.userAgent))
	}
	if s.proxyServer != "" {
		opts = append(opts, browse.WithProxy(s.proxyServer))
	}
	if s.viewport[0] > 0 && s.viewport[1] > 0 {
		opts = append(opts, browse.WithViewport(s.viewport[0], s.viewport[1]))
	}
	if s.allowPrivateIPs {
		opts = append(opts, browse.WithAllowPrivateIPs(true))
	}
	if len(s.allowedOrigins) > 0 {
		opts = append(opts, browse.WithAllowedOrigins(s.allowedOrigins...))
	}
	opts = append(opts, browse.WithPolicyID(taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs)))
	if s.remoteBrowserURL != "" {
		opts = append(opts, browse.WithRemoteCDP(s.remoteBrowserURL))
		if s.isolateRemoteBrowserContext {
			opts = append(opts, browse.WithRemoteBrowserContext())
		} else if s.remoteBrowserContextID != "" {
			opts = append(opts, browse.WithRemoteBrowserContextID(s.remoteBrowserContextID))
		}
	}
	engine := browse.New(opts...)
	if err := engine.LaunchContext(ctx); err != nil {
		return fmt.Errorf("failed to reset session: %w", err)
	}
	s.browser = engine
	return nil
}

// Reset force-resets browser and page state. Sessions backed by a browser
// supplied through NewSessionFromBrowser cannot be recreated and return a
// structured unsupported-operation error without closing that browser.
func (s *Session) Reset() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ResetContext(ctx)
}

// ResetContext force-resets browser state, bounding lock wait and browser startup.
// For caller-injected browsers, it returns before touching the browser handle;
// the caller must create a new session with a replacement browser instead.
func (s *Session) ResetContext(ctx context.Context) error {
	_, err := s.ResetWithOutcomeContext(ctx)
	return err
}

// ResetWithOutcomeContext resets the browser engine and reports the verified
// lifecycle state. Page creation remains lazy, as it is for a new session.
func (s *Session) ResetWithOutcomeContext(ctx context.Context) (result *ResetResult, retErr error) {
	ctx, lifecycleSpan, lifecycleStartedAt := s.startLifecycleSpan(ctx, "reset")
	defer func() { endLifecycleSpan(lifecycleSpan, lifecycleStartedAt, retErr) }()
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("session is closed")
	}
	err := s.resetLockedContext(ctx)
	if err != nil {
		var operationErr *OperationError
		if errors.As(err, &operationErr) {
			return nil, operationErr
		}
		s.lastError = err.Error()
		s.sessionDead = true
		s.deadReason = err.Error()
		return nil, s.wrapDetailedError("reset", err)
	}
	s.lastRecoveryAt = time.Now()
	s.consecutiveTimeouts = 0
	s.lastError = ""
	s.sessionDead = false
	s.deadReason = ""
	result = &ResetResult{
		Action: "reset", Outcome: string(OutcomePostconditionVerified),
		Verified: s.browser != nil && !s.sessionDead, BrowserAlive: s.browser != nil && !s.sessionDead,
		PageReady: s.page != nil, Evidence: "browser engine relaunched successfully; page will attach on the next page action",
	}
	result.OperationOrdinal = s.nextOperationOrdinal()
	result.Receipt = s.newActionReceipt(result.Action, "browser session", "", result.Outcome, result.Evidence, "", result.Verified, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	return result, nil
}

// Status returns current session/browser health information.
func (s *Session) Status() *SessionStatus {
	status, _ := s.StatusContext(context.Background())
	return status
}

// StatusContext snapshots session health while honoring ctx during lock
// queueing. The returned data is all-or-error; a canceled caller gets no
// partial health snapshot.
func (s *Session) StatusContext(ctx context.Context) (*SessionStatus, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	st := &SessionStatus{
		ContentTrust:           ContentTrustUntrustedPage,
		BrowserAlive:           s.browser != nil && !s.closed,
		SessionAlive:           !s.closed,
		BrowserOwnership:       s.browserOwnership,
		BrowserInstanceID:      s.browserInstanceID(),
		BrowserContextIsolated: s.isolateRemoteBrowserContext,
		EndpointSource:         sessionEndpointSource(s.browserOwnership),
		AllowedOrigins:         append([]string(nil), s.allowedOrigins...),
		AllowPrivateIPs:        s.allowPrivateIPs,
		PolicyID:               taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs),
		ProxyConfigured:        s.proxyServer != "",
		ProxyAppliedByScout:    s.proxyAppliedByScout,
		RemoteHostPolicy:       "not_applicable",
		Recoverable:            !s.closed && s.browserOwnership != "injected_browser",
		SessionDead:            s.sessionDead,
		DeadReason:             s.deadReason,
		InFlightCommands:       s.inflightCommands,
		ConsecutiveTimeouts:    s.consecutiveTimeouts,
		LastError:              s.lastError,
	}
	if normalizedOrigins, err := normalizeTaskOriginSet(s.allowedOrigins); err == nil {
		st.AllowedOrigins = sortedTaskOrigins(normalizedOrigins)
	}
	st.PolicyEnforcement = "session_page_policy"
	if len(s.allowedOrigins) == 0 {
		st.PolicyEnforcement = "no_origin_allowlist"
	}
	if s.browserOwnership == "remote_cdp" {
		st.BrowserEndpoint = safeBrowserEndpointLabel(s.remoteBrowserURL)
		st.RemoteHostPolicy = "unverified"
	}
	if !s.lastSuccessAt.IsZero() {
		st.LastSuccessAt = s.lastSuccessAt.Format(time.RFC3339)
	}
	if !s.lastRecoveryAt.IsZero() {
		st.LastRecoveryAt = s.lastRecoveryAt.Format(time.RFC3339)
	}
	if s.page != nil {
		st.ActiveTab, st.ActiveTargetID = activePageIdentity(s.page, s.tabs)
		value, err := s.page.EvaluateContext(ctx, "window.location.href")
		if err == nil {
			st.CurrentURL, _ = value.(string)
		} else if reason := connectionDeadReason(err); reason != "" {
			s.sessionDead = true
			s.deadReason = reason
		}
	}
	st.SessionDead = s.sessionDead
	st.DeadReason = s.deadReason
	st.BrowserAlive = s.browser != nil && !s.closed && !s.sessionDead
	switch {
	case s.closed:
		st.Recoverable = false
		st.RecoveryHint = "This session was closed. Create or activate a new session before continuing."
	case s.browserOwnership == "injected_browser":
		st.Recoverable = false
		st.RecoveryHint = "This session uses a caller-supplied browser and cannot be reset by Scout. Supply a replacement browser handle to create a new session."
	case s.sessionDead && s.browserOwnership == "remote_cdp":
		st.Recoverable = true // Reconnection is possible; success is not guaranteed.
		st.RecoveryHint = "The remote CDP connection is unavailable. Reset rediscovers the browser WebSocket from an HTTP(S) DevTools endpoint or reconnects to the configured WebSocket URL; inspect mutations with unknown outcomes before retrying."
	case s.sessionDead:
		st.Recoverable = true
		st.RecoveryHint = "The CDP connection is unavailable. Reset relaunches the locally owned browser; inspect mutations with unknown outcomes before retrying."
	case s.consecutiveTimeouts >= s.recoverTimeoutN && s.recoverTimeoutN > 0:
		st.Recoverable = s.browserOwnership != "injected_browser"
		st.RecoveryHint = "The session has repeated timeouts. Reset it if subsequent read-only operations continue to fail."
	default:
		st.RecoveryHint = "The session is healthy. Reset is available if it becomes unresponsive; inspect the page before retrying any action with an unknown outcome."
	}
	if s.network != nil {
		s.network.mu.Lock()
		st.PendingRequests = len(s.network.pending)
		s.network.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return st, nil
}

func sessionEndpointSource(ownership string) string {
	switch ownership {
	case "remote_cdp":
		return "remote_cdp"
	case "injected_browser":
		return "injected_browser"
	case "scout_launched":
		return "scout_launch"
	default:
		return "unknown"
	}
}

func safeBrowserEndpointLabel(endpoint string) string {
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

func activePageIdentity(page *browse.Page, tabs *tabManager) (tabName, targetID string) {
	if page == nil {
		return "", ""
	}
	return activeTabName(tabs), page.TargetID()
}

func activeTabName(tabs *tabManager) string {
	if tabs == nil || tabs.active == "" {
		return "default"
	}
	return tabs.active
}

// Close releases the session and calls Close on its browser handle. For a
// Scout-launched session this shuts down the local browser. For RemoteCDP it
// disconnects Scout's CDP connection without closing the remote browser. For
// NewSessionFromBrowser it delegates cleanup to the injected Browser's Close
// method, so callers should treat that browser handle as transferred to the
// session for cleanup purposes.
func (s *Session) Close() error {
	return s.CloseContext(context.Background())
}

// CloseContext releases the session within the supplied cleanup deadline.
// It first attempts to close a locally owned page using ctx, then asks a local
// Chrome process to shut down cleanly before force-killing it at the deadline.
// For remote CDP it closes only Scout's connection and leaves the browser and
// its targets open. An isolated context is Scout-owned and disposed.
func (s *Session) CloseContext(ctx context.Context) (retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, lifecycleSpan, lifecycleStartedAt := s.startLifecycleSpan(ctx, "close")
	defer func() { endLifecycleSpan(lifecycleSpan, lifecycleStartedAt, retErr) }()
	// Detach the recorder under the lock, but stop it WITHOUT holding s.mu:
	// cleanup() waits for the capture goroutine, which itself takes s.mu on every
	// tick (captureOne). Waiting under the lock would deadlock — the same reason
	// StopScreenRecording releases the lock before <-rec.done. Setting closed here
	// also makes the next captureOne bail out immediately.
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	s.releaseHeldKeysLocked()
	s.closed = true
	rec := s.screenRec
	s.screenRec = nil
	browser := s.browser
	page := s.mainPage
	framePages := make([]*browse.Page, 0, len(s.frameRouteStack))
	seenFramePages := make(map[*browse.Page]struct{}, len(s.frameRouteStack)+len(s.orphanedFramePages)+1)
	appendFramePage := func(framePage *browse.Page) {
		if framePage == nil {
			return
		}
		if _, exists := seenFramePages[framePage]; exists {
			return
		}
		seenFramePages[framePage] = struct{}{}
		framePages = append(framePages, framePage)
	}
	for i := len(s.frameRouteStack) - 1; i >= 0; i-- {
		level := s.frameRouteStack[i]
		if level.unsubscribe != nil {
			level.unsubscribe()
		}
		appendFramePage(level.child)
	}
	for _, framePage := range s.orphanedFramePages {
		appendFramePage(framePage)
	}
	appendFramePage(s.frameRoute)
	if page == nil {
		page = s.page
	}
	s.browser = nil
	s.page = nil
	s.mainPage = nil
	s.frameRoute = nil
	s.frameRouteStack = nil
	s.orphanedFramePages = nil
	s.frameOwnerSelector = ""
	s.frameParentID = ""
	if s.frameDetachUnsubscribe != nil {
		s.frameDetachUnsubscribe()
		s.frameDetachUnsubscribe = nil
	}
	s.frameDetached = nil
	s.mu.Unlock()

	var cleanupErr error
	if rec != nil {
		cleanupErr = rec.cleanupContext(ctx)
	}
	for _, framePage := range framePages {
		if err := framePage.DetachContext(ctx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	var pageErr error
	if s.remoteBrowserURL != "" && browser != nil {
		if closer, ok := browser.(interface{ CloseContext(context.Context) error }); ok {
			pageErr = closer.CloseContext(ctx)
		} else {
			pageErr = browser.Close()
		}
		browser = nil
	}
	if page != nil && s.remoteBrowserURL == "" {
		pageErr = page.CloseContext(ctx)
	}
	if browser == nil {
		return errors.Join(cleanupErr, pageErr)
	}
	closer, ok := browser.(interface{ CloseContext(context.Context) error })
	var browserErr error
	if ok {
		browserErr = closer.CloseContext(ctx)
	} else {
		browserErr = browser.Close()
	}
	return errors.Join(cleanupErr, pageErr, browserErr)
}

// ensurePage creates a page if none exists.
func (s *Session) ensurePage() error {
	return s.ensurePageContext(context.Background())
}

func (s *Session) ensurePageContext(ctx context.Context) error {
	if s.closed {
		return fmt.Errorf("session is closed")
	}
	if s.page != nil {
		if err := s.ensureFrameRouteContext(ctx); err != nil {
			return err
		}
		if err := s.checkExpectedTargetIDContext(ctx); err != nil {
			return err
		}
		return nil
	}
	// Reuse the initial about:blank page Chrome opened at launch
	var page *browse.Page
	var err error
	created := false
	if contextual, ok := s.browser.(interface {
		ExistingPageContext(context.Context) (*browse.Page, error)
	}); ok {
		page, err = contextual.ExistingPageContext(ctx)
	} else {
		page, err = s.browser.ExistingPage()
	}
	if ctx.Err() != nil {
		if page != nil {
			return errors.Join(ctx.Err(), s.cleanupUninitializedPage(page, created))
		}
		return ctx.Err()
	}
	if err != nil || page == nil {
		if contextual, ok := s.browser.(interface {
			NewPageContext(context.Context) (*browse.Page, error)
		}); ok {
			page, err = contextual.NewPageContext(ctx)
		} else {
			page, err = s.browser.NewPage()
		}
		if err != nil {
			return fmt.Errorf("failed to create page: %w", err)
		}
		created = true
	}
	if s.stealth {
		if err := s.applyStealthPatches(ctx, page); err != nil {
			return errors.Join(err, s.cleanupUninitializedPage(page, created))
		}
	}
	if err := s.applyPageURLPolicy(page); err != nil {
		return errors.Join(err, s.cleanupUninitializedPage(page, created))
	}
	if expected, _ := ctx.Value(expectedTargetIDKey{}).(string); expected != "" && page.TargetID() != expected {
		identityErr := &OperationError{Phase: "target_binding", Cause: "stale_target", Detail: fmt.Sprintf("expected target %s, but active target is %s; inspect the current page before retrying", expected, page.TargetID()), OriginalError: "target identity mismatch"}
		return errors.Join(identityErr, s.cleanupUninitializedPage(page, created))
	}
	s.page = page
	if s.network != nil {
		if created {
			// The page is still at about:blank, so install retained explicit or
			// passive observers before its first navigation.
			if err := s.reattachNetworkObserversLocked(); err != nil {
				s.markNetworkObserverUnavailableLocked()
			}
		} else {
			_ = s.ensureNetworkObserversLocked()
		}
	}
	return nil
}

func (s *Session) cleanupUninitializedPage(page *browse.Page, created bool) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if s.remoteBrowserURL != "" && !created {
		if err := page.DetachContext(cleanupCtx); err != nil {
			return fmt.Errorf("failed to detach uninitialized remote page %q: %w", page.TargetID(), err)
		}
		return nil
	}
	if err := page.CloseContext(cleanupCtx); err != nil {
		return fmt.Errorf("failed to close uninitialized page %q: %w", page.TargetID(), err)
	}
	return nil
}

func (s *Session) applyPageURLPolicy(page *browse.Page) error {
	if err := page.SetURLValidator(browse.URLValidator{
		AllowPrivateIPs: s.allowPrivateIPs,
		PolicyID:        taskOriginPolicyID(s.allowedOrigins, s.allowPrivateIPs),
		AllowedOrigins:  append([]string(nil), s.allowedOrigins...),
	}); err != nil {
		return err
	}
	return nil
}

// Navigate loads a URL and returns structured page info.
func (s *Session) Navigate(url string) (*PageResult, error) {
	return s.NavigateWithPostcondition(url, Postcondition{})
}

// NavigateWithPostcondition navigates and checks the resulting location or
// supplied page condition while holding the session lock throughout.
func (s *Session) NavigateWithPostcondition(url string, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.NavigateWithPostconditionContext(ctx, url, condition)
}

// NavigateWithPostconditionContext navigates under the caller's deadline,
// including lock queueing, CDP navigation, and document-load wait.
func (s *Session) NavigateWithPostconditionContext(ctx context.Context, url string, condition Postcondition) (*PageResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if condition.count() > 0 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
	}
	if expected, _ := ctx.Value(expectedTargetIDKey{}).(string); expected != "" {
		if err := s.ensurePageContext(ctx); err != nil {
			return nil, err
		}
		ctx = beginExpectedTargetTransition(ctx)
	}
	if s.frameRoute != nil {
		if err := s.restoreFrameRouteLocked(ctx); err != nil {
			return nil, fmt.Errorf("restore main page before navigation: %w", err)
		}
	}

	start, before := s.traceBeforeActionContext(ctx)
	// A remote CDP session is attached to a browser the caller owns. Reuse its
	// active page instead of closing that tab and creating a replacement.
	if s.remoteBrowserURL != "" {
		if err := s.ensurePageContext(ctx); err != nil {
			return nil, err
		}
		if err := s.page.NavigateContext(ctx, url); err != nil {
			var unknown *browse.NavigationOutcomeUnknownError
			if errors.As(err, &unknown) {
				outcomeErr := s.actionOutcomeAfterDispatch("navigate", url, "navigation may have been dispatched, but completion could not be verified", err, condition)
				return nil, s.traceActionFailureContext(ctx, start, before, "navigate", "", "", url, outcomeErr)
			}
			s.traceAfterActionContext(ctx, start, before, "navigate", "", "", url, err)
			var policyErr *browse.URLPolicyError
			if errors.As(err, &policyErr) {
				return nil, fmt.Errorf("failed to navigate: %w", err)
			}
			return nil, fmt.Errorf("failed to navigate to %s: %w", url, err)
		}
		_, _ = s.page.EvaluateContext(ctx, scoutSubmitTrackerJS)
		s.traceAfterActionContext(ctx, start, before, "navigate", "", "", url, nil)
		s.recordAction(Action{Type: "navigate", Value: url})
		s.addHistory("navigate", "", url, "")
		if condition.count() == 0 {
			result, err := s.pageResultContext(ctx)
			if err != nil {
				return nil, s.actionOutcomeAfterDispatch("navigate", url, "navigation completed but final page state could not be read", err, condition)
			}
			result.Action, result.Target, result.Outcome, result.Verified = "navigated", url, "navigation_completed", true
			result.OperationOrdinal = s.operationOrdinalForResult("navigate", "")
			result.Evidence = "navigation completed; final URL: " + result.URL
			result.Receipt = s.newActionReceipt("navigate", url, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true, result.OperationOrdinal, nil, ctx)
			actionReceiptDeadline(ctx, result.Receipt)
			s.applyTraceResult("navigate", url, result.TargetID, result.Outcome, result.Evidence, "", true)
			return result, nil
		}
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "navigated", url, condition)
		if verifyErr != nil {
			return nil, s.actionOutcomeAfterDispatch("navigate", url, "navigation completed but its postcondition could not be verified", verifyErr, condition)
		}
		s.applyTraceResult("navigate", url, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
		return result, nil
	}

	page := s.page
	freshTarget := page == nil
	if !freshTarget {
		// Reuse the active target so browser history and origin-scoped page state
		// survive ordinary top-level navigations. Refresh the validator before
		// dispatch; the installed Fetch policy consults this validator on every
		// document request, including redirects.
		s.collectTraceURLPolicyDecisions()
		if err := s.applyPageURLPolicy(page); err != nil {
			return nil, s.traceActionFailureContext(ctx, start, before, "navigate", "", "", url, err)
		}
	} else {
		// Close the initial about:blank tab Chrome opened at launch
		if blank, err := s.browser.ExistingPage(); err == nil && blank != nil {
			_ = blank.Close()
		}
		var err error
		if contextual, ok := s.browser.(interface {
			NewPageContext(context.Context) (*browse.Page, error)
		}); ok {
			page, err = contextual.NewPageContext(ctx)
		} else {
			page, err = s.browser.NewPage()
		}
		if err != nil {
			s.traceAfterActionContext(ctx, start, before, "navigate", "", "", url, err)
			return nil, fmt.Errorf("failed to navigate to %s: %w", url, err)
		}
		if s.stealth {
			if err := s.applyStealthPatches(ctx, page); err != nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				cleanupErr := page.CloseContext(cleanupCtx)
				cancel()
				s.traceAfterActionContext(ctx, start, before, "navigate", "", "", url, err)
				return nil, errors.Join(err, cleanupErr)
			}
		}
		if err := s.applyPageURLPolicy(page); err != nil {
			return nil, errors.Join(err, s.cleanupUninitializedPage(page, true))
		}
	}
	s.page = page
	s.diffInstalled = false
	s.frameID = ""
	s.frameContextID = 0

	// Navigate created a fresh CDP target; the old page's session-scoped
	// network handlers died when it closed. Re-attach BEFORE page.Navigate so
	// the document load and early XHR/fetch calls on the new page are captured.
	// Best-effort: a failure to re-enable must not abort the navigation.
	if freshTarget {
		if err := s.reattachNetworkObserversLocked(); err != nil {
			s.markNetworkObserverUnavailableLocked()
		}
	}

	// Navigate explicitly so we attach event listeners before the load fires
	if err := page.NavigateContext(ctx, url); err != nil {
		var unknown *browse.NavigationOutcomeUnknownError
		if errors.As(err, &unknown) {
			outcomeErr := s.actionOutcomeAfterDispatch("navigate", url, "navigation may have been dispatched, but completion could not be verified", err, condition)
			return nil, s.traceActionFailureContext(ctx, start, before, "navigate", "", "", url, outcomeErr)
		}
		s.traceAfterActionContext(ctx, start, before, "navigate", "", "", url, err)
		var policyErr *browse.URLPolicyError
		if errors.As(err, &policyErr) {
			return nil, fmt.Errorf("failed to navigate: %w", err)
		}
		return nil, fmt.Errorf("failed to navigate to %s: %w", url, err)
	}

	// Pre-install the submit-outcome tracker so the first submit on
	// the new page is captured. Best-effort: failures here shouldn't
	// abort the navigation — the tracker is a diagnostic affordance,
	// not a correctness requirement.
	_, _ = s.page.EvaluateContext(ctx, scoutSubmitTrackerJS)

	s.traceAfterActionContext(ctx, start, before, "navigate", "", "", url, nil)
	s.recordAction(Action{Type: "navigate", Value: url})
	s.addHistory("navigate", "", url, "")
	if condition.count() == 0 {
		result, err := s.pageResultContext(ctx)
		if err != nil {
			return nil, s.actionOutcomeAfterDispatch("navigate", url, "navigation completed but final page state could not be read", err, condition)
		}
		result.Action, result.Target, result.Outcome, result.Verified = "navigated", url, "navigation_completed", true
		result.OperationOrdinal = s.operationOrdinalForResult("navigate", "")
		result.Evidence = "navigation completed; final URL: " + result.URL
		result.Receipt = s.newActionReceipt("navigate", url, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, true, result.OperationOrdinal, nil, ctx)
		actionReceiptDeadline(ctx, result.Receipt)
		s.applyTraceResult("navigate", url, result.TargetID, result.Outcome, result.Evidence, "", true)
		return result, nil
	}
	result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "navigated", url, condition)
	if verifyErr != nil {
		return nil, s.actionOutcomeAfterDispatch("navigate", url, "navigation completed but its postcondition could not be verified", verifyErr, condition)
	}
	s.applyTraceResult("navigate", url, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
	return result, nil
}

// Snapshot returns the current page state without performing any action.
func (s *Session) Snapshot() (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.pageResult()
}

// SnapshotContext returns the current page state under the caller's deadline,
// including time queued for the session lock and page evaluation.
func (s *Session) SnapshotContext(ctx context.Context) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.pageResultContext(ctx)
}

// pageResult returns a (*PageResult, error) pair so the ~18 public action
// methods can `return s.pageResult()` directly; the error is presently always
// nil but the signature keeps those call sites uniform and future-proof.
//
//nolint:unparam // error is intentionally part of the uniform helper contract
func (s *Session) pageResult() (*PageResult, error) {
	url, err := s.evaluateInFrame(`location.href`)
	if err != nil {
		return nil, err
	}
	title, err := s.evaluateInFrame(`document.title`)
	if err != nil {
		return nil, err
	}
	urlStr, _ := url.(string)
	titleStr, _ := title.(string)
	return &PageResult{
		URL: urlStr, ContentTrust: ContentTrustUntrustedPage, Title: titleStr, TargetID: s.page.TargetID(), FrameID: s.frameID,
	}, nil
}

func (s *Session) pageResultContext(ctx context.Context) (*PageResult, error) {
	state, err := s.evaluateCurrentContext(ctx, `({url: location.href, title: document.title})`)
	if err != nil {
		return nil, err
	}
	values, _ := state.(map[string]any)
	url, _ := values["url"].(string)
	title, _ := values["title"].(string)
	return &PageResult{URL: url, ContentTrust: ContentTrustUntrustedPage, Title: title, TargetID: s.page.TargetID(), FrameID: s.frameID}, nil
}

// operationOrdinalForResult reuses the trace event allocated for this action
// when tracing is enabled. Without a trace, it allocates the same session-wide
// sequence directly for the returned result.
func (s *Session) operationOrdinalForResult(action, target string) int {
	if s.tracing && s.trace != nil && len(s.trace.events) > 0 {
		last := s.trace.events[len(s.trace.events)-1]
		if last.Action == action && last.Selector == target && last.OperationOrdinal > 0 {
			return last.OperationOrdinal
		}
	}
	return s.nextOperationOrdinal()
}

// actionPageResult returns page state with concise evidence for a completed action.
// The evidence is limited to browser state directly observed after the action.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) actionPageResult(action, target, evidence string) (*PageResult, error) {
	result, err := s.pageResult()
	if err != nil {
		return nil, err
	}
	result.Action = action
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	result.Target = target
	result.Outcome = "postcondition_verified"
	result.Verified = true
	result.Evidence = evidence
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, evidence, "", true, result.OperationOrdinal, nil)
	s.applyTraceResult(traceActionForResult(action), target, result.TargetID, result.Outcome, evidence, "", true)
	return result, nil
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) actionPageResultUnverified(action, target, evidence string) (*PageResult, error) {
	result, err := s.pageResult()
	if err != nil {
		return nil, err
	}
	result.Action = action
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	result.Target = target
	result.Outcome = "effect_unverified"
	result.Verified = false
	result.Evidence = evidence
	result.RecoveryHint = "Supply an expected page condition or inspect the page with observe/assert tools to verify the effect."
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false, result.OperationOrdinal, nil)
	s.applyTraceResult(traceActionForResult(action), target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false)
	return result, nil
}

func (s *Session) actionPageResultContext(ctx context.Context, action, target, evidence string) (*PageResult, error) {
	result, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, err
	}
	result.Action, result.Target, result.Outcome, result.Verified, result.Evidence = action, target, "postcondition_verified", true, evidence
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, evidence, "", true, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult(traceActionForResult(action), target, result.TargetID, result.Outcome, evidence, "", true)
	return result, nil
}

func (s *Session) actionPageResultUnverifiedContext(ctx context.Context, action, target, evidence string) (*PageResult, error) {
	result, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, err
	}
	result.Action, result.Target, result.Outcome, result.Verified, result.Evidence = action, target, "effect_unverified", false, evidence
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	result.RecoveryHint = "Supply an expected page condition or inspect the page with observe/assert tools to verify the effect."
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult(traceActionForResult(action), target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false)
	return result, nil
}

// actionEventObserved reports browser event delivery without claiming that
// the application completed the requested effect.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) actionEventObserved(action, target, evidence string) (*PageResult, error) {
	result, err := s.pageResult()
	if err != nil {
		return nil, err
	}
	result.Action = action
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	result.Target = target
	result.Outcome = "event_observed"
	result.Verified = false
	result.Evidence = evidence
	result.RecoveryHint = "Supply an expected page condition or inspect the page with observe/assert tools to verify the application effect."
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false, result.OperationOrdinal, nil)
	s.applyTraceResult(traceActionForResult(action), target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false)
	return result, nil
}

func (s *Session) actionEventObservedContext(ctx context.Context, action, target, evidence string) (*PageResult, error) {
	result, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, err
	}
	result.Action, result.Target, result.Outcome, result.Verified, result.Evidence = action, target, "event_observed", false, evidence
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	result.RecoveryHint = "Supply an expected page condition or inspect the page with observe/assert tools to verify the application effect."
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult(traceActionForResult(action), target, result.TargetID, result.Outcome, evidence, result.RecoveryHint, false)
	return result, nil
}

func traceActionForResult(action string) string {
	switch action {
	case "clicked":
		return "click"
	case "typed":
		return "type"
	case "navigated":
		return "navigate"
	case "hovered":
		return "hover"
	case "scrolled_to":
		return "scroll_to"
	case "scrolled_by":
		return "scroll_by"
	case "focused":
		return "focus"
	case "dragged":
		return "drag_drop"
	default:
		return action
	}
}

func elementOutcome(verified bool) string {
	if verified {
		return string(OutcomeValueVerified)
	}
	return string(OutcomeValueMismatch)
}

func elementRecoveryHint(verified bool) string {
	if verified {
		return ""
	}
	return "Inspect the live field value and page validation state, then retry with the value the application accepts."
}

// Click clicks an element and returns the updated page state.
func (s *Session) Click(selector string) (*PageResult, error) {
	return s.ClickExpect(selector, "")
}

// ClickExpect clicks a selector and optionally verifies that expectedText is
// visible on the resulting page. An empty expectedText leaves the effect
// explicitly unverified while still reporting that the click was dispatched.
func (s *Session) ClickExpect(selector, expectedText string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClickWithPostconditionContext(ctx, selector, Postcondition{Text: expectedText}, false)
}

// ClickWithPostcondition clicks a selector and checks the resulting page state.
func (s *Session) ClickWithPostcondition(selector string, condition Postcondition, wait bool) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClickWithPostconditionContext(ctx, selector, condition, wait)
}

// ClickWithPostconditionContext performs the click and its resulting page
// checks under one caller-supplied deadline, including time queued on the
// session lock.
func (s *Session) ClickWithPostconditionContext(ctx context.Context, selector string, condition Postcondition, wait bool) (*PageResult, error) {
	if condition.count() > 0 {
		if err := condition.Validate(); err != nil {
			return nil, err
		}
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	dispatched := false
	dispatchAttempted := false
	if err := s.waitForSelectorContext(ctx, selector); err != nil {
		if s.activeTargetGoneAfterFailure(ctx) {
			err = targetGoneOperationError("click", s.page, err)
			s.traceAfterActionContext(ctx, start, before, "click", selector, "", "", err)
			return nil, err
		}
		if ctx.Err() != nil {
			s.traceAfterActionContext(ctx, start, before, "click", selector, "", "", ctx.Err())
			return nil, ctx.Err()
		}
		if isTargetGoneError(err) {
			err = targetGoneOperationError("click", s.page, err)
			s.traceAfterActionContext(ctx, start, before, "click", selector, "", "", err)
			return nil, err
		}
		var invalid *waitutil.InvalidSelectorError
		if !errors.As(err, &invalid) && !strings.Contains(selector, ":text(") && !strings.Contains(selector, ":has-text(") {
			s.traceAfterActionContext(ctx, start, before, "click", selector, "", "", err)
			return nil, s.enrichSelectorError(selector, err)
		}
		if _, resolveErr := s.resolveSelectorContext(ctx, selector); resolveErr != nil {
			s.traceAfterActionContext(ctx, start, before, "click", selector, "", "", err)
			return nil, s.enrichSelectorError(selector, err)
		}
	}
	click := func() error {
		nodeID, err := s.resolveSelectorContext(ctx, selector)
		if err != nil {
			return s.markIfDead(err)
		}
		dispatchAttempted = true
		if s.frameContextID != 0 && s.frameRoute == nil {
			err = s.clickInSelectedFrameContext(ctx, nodeID)
		} else {
			err = browse.NewSelection(s.page, nodeID, selector).ClickContext(ctx)
		}
		if err == nil || !browse.IsStaleNodeError(err) {
			return s.markIfDead(err)
		}
		nodeID, err = s.resolveSelectorContext(ctx, selector)
		if err != nil {
			return err
		}
		dispatchAttempted = true
		if s.frameContextID != 0 && s.frameRoute == nil {
			return s.markIfDead(s.clickInSelectedFrameContext(ctx, nodeID))
		}
		return s.markIfDead(browse.NewSelection(s.page, nodeID, selector).ClickContext(ctx))
	}
	if err := click(); err != nil {
		if dispatchAttempted && mutationOutcomeMayBeUnknown(err) {
			outcomeErr := s.actionOutcomeAfterDispatch("click", selector, "click command was interrupted; the browser may have received it, but completion was not confirmed", err, condition)
			return nil, s.traceActionFailureContext(ctx, start, before, "click", selector, "", "", outcomeErr)
		}
		s.traceAfterActionContext(ctx, start, before, "click", selector, "", "", err)
		return nil, err
	}
	dispatched = true
	if wait {
		if err := s.page.WaitLoadContext(ctx); err != nil {
			outcomeErr := s.actionOutcomeAfterDispatch("click", selector, "click event dispatched; document load or outcome verification was interrupted", err, condition)
			return nil, s.traceActionFailureContext(ctx, start, before, "click", selector, "", "", outcomeErr)
		}
	} else if err := s.page.WaitStableContext(ctx, 300*time.Millisecond); err != nil {
		outcomeErr := s.actionOutcomeAfterDispatch("click", selector, "click event dispatched; page settling or outcome verification was interrupted", err, condition)
		return nil, s.traceActionFailureContext(ctx, start, before, "click", selector, "", "", outcomeErr)
	}
	if err := ctx.Err(); err != nil {
		if dispatched {
			outcomeErr := s.actionOutcomeAfterDispatch("click", selector, "click event dispatched; caller deadline expired before the resulting state could be verified", err, condition)
			return nil, s.traceActionFailureContext(ctx, start, before, "click", selector, "", "", outcomeErr)
		}
		return nil, err
	}
	s.traceAfterActionContext(ctx, start, before, "click", selector, "", "", nil)
	s.recordAction(Action{Type: "click", Selector: selector})
	s.addHistory("click", selector, "", "")
	if condition.count() > 0 {
		result, verifyErr := s.verifyPagePostconditionContextLocked(ctx, "clicked", selector, condition)
		if verifyErr != nil {
			outcomeErr := s.actionOutcomeAfterDispatch("click", selector, "click event dispatched; postcondition could not be verified", verifyErr, condition)
			return nil, s.traceActionFailureContext(ctx, start, before, "click", selector, "", "", outcomeErr)
		}
		s.applyTraceResult("click", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
		return result, nil
	}
	result, err := s.pageResultContext(ctx)
	if err != nil {
		outcomeErr := s.actionOutcomeAfterDispatch("click", selector, "click event dispatched; page state could not be read", err, condition)
		return nil, s.traceActionFailureContext(ctx, start, before, "click", selector, "", "", outcomeErr)
	}
	result.Action, result.Target, result.Outcome, result.Verified = "clicked", selector, "effect_unverified", false
	result.OperationOrdinal = s.operationOrdinalForResult("click", selector)
	if wait {
		result.Evidence = "target resolved and click dispatched; document load completed"
	} else {
		result.Evidence = "target resolved and click dispatched; page state read after settling"
	}
	result.RecoveryHint = "Supply an expected page condition or inspect the page with observe/assert tools to verify the application effect."
	result.Receipt = s.newActionReceipt("click", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, false, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("click", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, false)
	return result, nil
}

func (s *Session) actionOutcomeAfterDispatch(action, target, evidence string, cause error, expected ...Postcondition) error {
	var page *browse.Page
	if s != nil {
		page = s.page
	}
	return s.actionOutcomeAfterDispatchForPage(page, action, target, evidence, cause, expected...)
}

// actionOutcomeAfterDispatchForPage records which page received a mutation
// whose final outcome could not be confirmed. A non-active page has no
// selected frame in this Session, so its frame identity must remain empty.
func (s *Session) actionOutcomeAfterDispatchForPage(page *browse.Page, action, target, evidence string, cause error, expected ...Postcondition) error {
	outcomeErr := &ActionOutcomeError{
		ContentTrust: ContentTrustUntrustedPage,
		Action:       action,
		Target:       target,
		Outcome:      OutcomeUnknown,
		Evidence:     evidence,
		RecoveryHint: "Inspect the current page and session status before retrying; the action may already have completed.",
		Cause:        cause,
	}
	if len(expected) > 0 && (expected[0].count() > 0 || expected[0].TargetAbsent != "") {
		postcondition := expected[0]
		outcomeErr.ExpectedPostcondition = &postcondition
	}
	if s != nil {
		if s.runID == "" {
			s.runID = newRunID()
		}
		s.unknownOutcomeSequence++
		outcomeErr.RunID = s.runID
		outcomeErr.Step = s.unknownOutcomeSequence
		outcomeErr.OperationOrdinal = s.nextOperationOrdinal()
		outcomeErr.BrowserInstanceID = s.browserInstanceID()
		if page == nil || page == s.page {
			outcomeErr.FrameID = s.frameID
		}
		outcomeErr.ConfiguredTimeoutMS = s.timeout.Milliseconds()
		outcomeErr.SessionDead = s.sessionDead
		outcomeErr.ConsecutiveTimeouts = s.consecutiveTimeouts
		outcomeErr.InFlightCommands = s.inflightCommands
		if page != nil {
			outcomeErr.TargetID = page.TargetID()
		}
		outcomeErr.Receipt = s.newActionReceipt(action, target, outcomeErr.TargetID, string(outcomeErr.Outcome), evidence, outcomeErr.RecoveryHint, false, outcomeErr.OperationOrdinal, outcomeErr.ExpectedPostcondition)
		outcomeErr.Receipt.FrameID = outcomeErr.FrameID
	}
	return outcomeErr
}

func (s *Session) browserInstanceID() string {
	if s == nil || s.browser == nil {
		return ""
	}
	if identified, ok := s.browser.(interface{ BrowserInstanceID() string }); ok {
		return identified.BrowserInstanceID()
	}
	return ""
}

// ReconcileActionOutcomeContext checks the postcondition saved in an unknown
// action error without replaying the mutation. Page conditions require the
// same live target; close_tab target absence is checked against browser target
// inventory. A different live target cannot classify the original action.
func (s *Session) ReconcileActionOutcomeContext(ctx context.Context, outcome *ActionOutcomeError) (*PageResult, error) {
	return s.reconcileActionOutcomeContext(ctx, outcome, false)
}

// ReconcileActionRecoveryOutcomeContext supports a portable record explicitly
// loaded by a host into a fresh session. The record retains its run ID as
// provenance; the exact target ID remains mandatory.
func (s *Session) ReconcileActionRecoveryOutcomeContext(ctx context.Context, outcome *ActionOutcomeError) (*PageResult, error) {
	return s.reconcileActionOutcomeContext(ctx, outcome, true)
}

func (s *Session) reconcileActionOutcomeContext(ctx context.Context, outcome *ActionOutcomeError, portable bool) (*PageResult, error) {
	if outcome == nil {
		return nil, fmt.Errorf("action outcome is required")
	}
	if portable && outcome.OperationOrdinal <= 0 {
		return nil, fmt.Errorf("action outcome is missing a valid operation ordinal")
	}
	if !portable && (outcome.RunID == "" || outcome.RunID != s.runID) {
		return nil, &OperationError{
			Phase: "reconcile_action", Cause: "stale_run",
			Detail:        "unknown action outcome belongs to a different or unspecified Scout run",
			OriginalError: "action outcome run ID does not match the current session",
			RecoveryHint:  "Reconnect to the original Scout run before reconciling; inspect the current page and treat the original outcome as unknown if that run is unavailable.",
		}
	}
	if outcome.ExpectedPostcondition != nil {
		if err := outcome.ExpectedPostcondition.Validate(); err != nil {
			return nil, fmt.Errorf("action outcome has invalid postcondition: %w", err)
		}
		if outcome.ExpectedPostcondition.TargetAbsent != "" {
			if outcome.Action != "close_tab" || outcome.ExpectedPostcondition.TargetAbsent != outcome.TargetID {
				return nil, fmt.Errorf("target_absent postcondition must match the target of a close_tab action")
			}
		}
	}
	if outcome.Receipt != nil {
		traceID, traceErr := trace.TraceIDFromHex(outcome.Receipt.OTelTraceID)
		spanID, spanErr := trace.SpanIDFromHex(outcome.Receipt.OTelSpanID)
		if traceErr == nil && spanErr == nil {
			linked := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, Remote: true})
			ctx = withActionSpanLink(ctx, linked)
		}
	}
	ctx = context.WithValue(ctx, recoveryOperationOrdinalContextKey{}, outcome.OperationOrdinal)
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if portable {
		currentBrowserID := s.browserInstanceID()
		if outcome.BrowserInstanceID == "" || currentBrowserID == "" || outcome.BrowserInstanceID != currentBrowserID {
			return nil, &OperationError{
				Phase: "reconcile_action", Cause: "browser_instance_mismatch",
				Detail:        "recovery record cannot be tied to the currently connected browser instance",
				OriginalError: "portable recovery browser identity is missing or does not match",
				RecoveryHint:  "Reconnect to the original running browser endpoint to reconcile; if it is unavailable or restarted, inspect application state manually and keep the outcome unknown.",
			}
		}
	}
	if condition := outcome.ExpectedPostcondition; condition != nil && condition.TargetAbsent != "" {
		checker, ok := s.browser.(interface {
			HasTargetContext(context.Context, string) (bool, error)
		})
		if !ok {
			return nil, fmt.Errorf("browser does not support target existence checks required to reconcile close_tab")
		}
		present, err := checker.HasTargetContext(ctx, condition.TargetAbsent)
		if err != nil {
			return nil, fmt.Errorf("check closed tab target: %w", err)
		}
		result := &PageResult{
			Action: "reconcile_close_tab", Target: outcome.Target, TargetID: condition.TargetAbsent,
			ContentTrust: ContentTrustUntrustedPage,
			Verified:     !present,
		}
		if present {
			result.Outcome = string(OutcomeUnknown)
			result.Evidence = "the original tab target is still present; close outcome remains unknown"
			result.RecoveryHint = "Inspect the named tab and browser state; do not replay the close until ownership and target identity are confirmed."
		} else {
			result.Outcome = string(OutcomePostconditionVerified)
			result.Evidence = "browser target registry confirms the original page target is absent"
			if s.tabs != nil {
				if tab, ok := s.tabs.tabs[outcome.Target]; ok && tab.page.TargetID() == condition.TargetAbsent && outcome.Target != s.tabs.active {
					delete(s.tabs.tabs, outcome.Target)
				}
			}
		}
		result.OperationOrdinal = s.operationOrdinalForResult("reconcile_close_tab", outcome.Target)
		result.Receipt = s.newActionReceipt(result.Action, result.Target, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, condition, ctx)
		result.Receipt.RecoveryOfOperationOrdinal = outcome.OperationOrdinal
		return result, nil
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	actualTargetID := s.page.TargetID()
	if outcome.TargetID == "" || actualTargetID != outcome.TargetID {
		return nil, &OperationError{
			Phase: "reconcile_action", Cause: "stale_target", TargetID: actualTargetID,
			Detail:        fmt.Sprintf("unknown action was dispatched to target %s, but current target is %s", outcome.TargetID, actualTargetID),
			OriginalError: "action outcome belongs to a different browser target",
			RecoveryHint:  "Select the original live target before reconciling, or inspect the current target and treat the original outcome as unknown.",
		}
	}
	if outcome.ExpectedPostcondition == nil {
		result, err := s.pageResultContext(ctx)
		if err != nil {
			return nil, err
		}
		result.Action, result.Target = "reconcile_"+outcome.Action, outcome.Target
		result.Outcome = string(OutcomeUnknown)
		result.Verified = false
		result.Evidence = "current page observed; the original action had no recorded postcondition, so its effect remains unknown"
		result.RecoveryHint = "Compare the current page with the intended application state before retrying the action."
		result.OperationOrdinal = s.operationOrdinalForResult(result.Action, result.Target)
		result.Receipt = s.newActionReceipt(result.Action, result.Target, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, false, result.OperationOrdinal, nil, ctx)
		result.Receipt.RecoveryOfOperationOrdinal = outcome.OperationOrdinal
		return result, nil
	}
	result, err := s.verifyPagePostconditionContextLocked(ctx, "reconcile_"+outcome.Action, outcome.Target, *outcome.ExpectedPostcondition)
	if err != nil {
		return nil, err
	}
	if result.Receipt != nil {
		result.Receipt.RecoveryOfOperationOrdinal = outcome.OperationOrdinal
	}
	return result, nil
}

// ReconcileActionOutcome is the timeout-bounded form of
// ReconcileActionOutcomeContext.
func (s *Session) ReconcileActionOutcome(outcome *ActionOutcomeError) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ReconcileActionOutcomeContext(ctx, outcome)
}

func mutationOutcomeMayBeUnknown(err error) bool {
	var dispatchErr *cdpclient.DispatchOutcomeUnknownError
	if !errors.As(err, &dispatchErr) {
		return false
	}
	switch dispatchErr.Method {
	case "Input.dispatchMouseEvent", "Input.dispatchKeyEvent", "Input.insertText",
		"Runtime.callFunctionOn", "Runtime.evaluate", "Page.navigate", "Page.reload",
		"Page.navigateToHistoryEntry", "Page.bringToFront", "Page.createIsolatedWorld", "Page.addScriptToEvaluateOnNewDocument",
		"Network.enable", "Network.setUserAgentOverride", "Target.activateTarget", "Target.createTarget",
		"Target.attachToTarget", "Target.detachFromTarget", "Target.closeTarget", "Network.setCookie", "Network.deleteCookies", "Network.clearBrowserCookies",
		"Storage.clearDataForOrigin", "Emulation.setDeviceMetricsOverride", "DOM.setFileInputFiles":
		return true
	default:
		return false
	}
}

func (p Postcondition) count() int {
	n := 0
	if p.Text != "" {
		n++
	}
	if p.URL != "" {
		n++
	}
	if p.URLContains != "" {
		n++
	}
	if p.Title != "" {
		n++
	}
	if p.ExistsSelector != "" {
		n++
	}
	if p.VisibleSelector != "" {
		n++
	}
	if p.ValueSelector != "" || p.Value != "" {
		n++
	}
	if p.CountSelector != "" || p.Count != nil {
		n++
	}
	return n
}

// verifyPagePostcondition checks an explicit URL or visibility condition.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) verifyPagePostcondition(action, target string, condition Postcondition) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.verifyPagePostconditionContextLocked(context.Background(), action, target, condition)
}

// VerifyPostcondition checks a page condition after a caller-composed action.
func (s *Session) VerifyPostcondition(action, target string, condition Postcondition) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.VerifyPostconditionContext(ctx, action, target, condition)
}

// VerifyPostconditionContext checks a caller-composed action's page condition
// under the caller's deadline, including time spent waiting for the session.
func (s *Session) VerifyPostconditionContext(ctx context.Context, action, target string, condition Postcondition) (*PageResult, error) {
	if err := condition.Validate(); err != nil {
		return nil, err
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.verifyPagePostconditionContextLocked(ctx, action, target, condition)
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) verifyPagePostconditionLockedContext(ctx context.Context, action, target string, condition Postcondition) (*PageResult, error) {
	return s.verifyPagePostconditionContextLocked(ctx, action, target, condition)
}

func (s *Session) verifyPagePostconditionContextLocked(ctx context.Context, action, target string, condition Postcondition) (*PageResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if condition.TargetAbsent != "" {
		return nil, fmt.Errorf("target_absent is only valid when reconciling an unknown close_tab outcome")
	}
	if err := condition.Validate(); err != nil {
		return nil, err
	}
	var state any
	var err error
	evidence, failure := "", ""
	switch {
	case condition.Text != "":
		want, _ := json.Marshal(condition.Text)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`(function(){const t=(document.body&&(document.body.innerText||document.body.textContent)||'').trim();const i=t.indexOf(%s);if(i<0)return {ok:false,snippet:t.slice(0,240)};return {ok:true,snippet:t.slice(Math.max(0,i-40),Math.min(t.length,i+%d+40))}})()`, string(want), len([]rune(condition.Text))))
		failure = "expected text was not observed"
	case condition.URL != "":
		want, _ := json.Marshal(condition.URL)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`({ok:location.href === %s,url:location.href})`, string(want)))
		failure = "current URL does not equal expected URL"
	case condition.URLContains != "":
		want, _ := json.Marshal(condition.URLContains)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`({ok:location.href.includes(%s),url:location.href})`, string(want)))
		failure = "current URL does not contain expected substring"
	case condition.Title != "":
		want, _ := json.Marshal(condition.Title)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`({ok:document.title === %s})`, string(want)))
		evidence = "page title checked"
		failure = "page title does not equal expected title"
	case condition.ExistsSelector != "":
		selector, _ := json.Marshal(condition.ExistsSelector)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`({ok:document.querySelector(%s) !== null})`, string(selector)))
		evidence = "existence checked for " + condition.ExistsSelector
		failure = "expected selector was not found"
	case condition.VisibleSelector != "":
		selector, _ := json.Marshal(condition.VisibleSelector)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`(function(){const e=document.querySelector(%s);if(!e)return {ok:false};const r=e.getBoundingClientRect(),s=getComputedStyle(e);return {ok:r.width>0&&r.height>0&&s.visibility!=='hidden'&&s.display!=='none'}})()`, string(selector)))
		evidence = "visibility checked for " + condition.VisibleSelector
		failure = "expected selector is not visible"
	case condition.ValueSelector != "":
		selector, _ := json.Marshal(condition.ValueSelector)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`(function(){const e=document.querySelector(%s);if(!e)return {found:false};return {found:true,actual:('value' in e)?String(e.value):String(e.innerText||e.textContent||'')}})()`, string(selector)))
		failure = fmt.Sprintf("expected value %q was not observed for %s", condition.Value, condition.ValueSelector)
	case condition.CountSelector != "" && condition.Count != nil:
		selector, _ := json.Marshal(condition.CountSelector)
		state, err = s.evaluateInFrameContext(ctx, s.frameContextID, fmt.Sprintf(`document.querySelectorAll(%s).length`, string(selector)))
		failure = fmt.Sprintf("expected %d matches for %s", *condition.Count, condition.CountSelector)
	default:
		return nil, fmt.Errorf("postcondition requires exactly one condition")
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok := false
	switch value := state.(type) {
	case map[string]any:
		ok, _ = value["ok"].(bool)
		if condition.Text != "" || condition.URL != "" || condition.URLContains != "" {
			if snippet, _ := value["snippet"].(string); snippet != "" {
				evidence = snippet
			}
			if current, _ := value["url"].(string); current != "" {
				evidence = "current URL: " + current
			}
		}
		if condition.ValueSelector != "" {
			found, _ := value["found"].(bool)
			actual, _ := value["actual"].(string)
			ok = found && actual == condition.Value
			evidence = fmt.Sprintf("value for %s: %q", condition.ValueSelector, actual)
		}
	case float64:
		actual := int(value)
		ok = actual == *condition.Count
		evidence = fmt.Sprintf("selector %s matched %d elements", condition.CountSelector, actual)
	}
	result, err := s.pageResultContext(ctx)
	if err != nil {
		return nil, err
	}
	result.Action, result.Target, result.Verified, result.Evidence = action, target, ok, evidence
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	if ok {
		result.Outcome = "postcondition_verified"
	} else {
		result.Outcome = "postcondition_failed"
		result.Evidence = failure + "; " + evidence
		result.RecoveryHint = "Inspect the current page and adjust the expected condition or recover the workflow before continuing."
	}
	postcondition := condition
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, &postcondition, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	return result, nil
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) pageResultForAssertion(action, target string, ok bool, evidence, failure string) (*PageResult, error) {
	result, err := s.pageResult()
	if err != nil {
		return nil, err
	}
	result.Action, result.Target, result.Verified, result.Evidence = action, target, ok, evidence
	result.OperationOrdinal = s.operationOrdinalForResult(traceActionForResult(action), target)
	if ok {
		result.Outcome = "postcondition_verified"
	} else {
		result.Outcome = "postcondition_failed"
		result.Evidence = failure + "; " + evidence
		result.RecoveryHint = "Inspect the current page and adjust the expected condition or recover the workflow before continuing."
	}
	result.Receipt = s.newActionReceipt(action, target, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, nil)
	s.applyTraceResult(traceActionForResult(action), target, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
	return result, nil
}

func mustJSON(value string) string { b, _ := json.Marshal(value); return string(b) }

// ClickExpectWait clicks a selector, waits for a full navigation, then checks
// the optional expected text against the resulting document.
func (s *Session) ClickExpectWait(selector, expectedText string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClickWithPostconditionContext(ctx, selector, Postcondition{Text: expectedText}, true)
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) clickExpect(selector, expectedText string, wait bool) (*PageResult, error) {
	return s.clickWithCondition(selector, Postcondition{Text: expectedText}, wait)
}

//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) clickWithCondition(selector string, condition Postcondition, wait bool) (*PageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	start, before := s.traceBeforeAction()

	if err := s.waitAndResolve(selector); err != nil {
		s.traceAfterAction(start, before, selector, err)
		return nil, err
	}

	click := func() error {
		return s.withStaleNodeRetry(selector, func(nodeID int64) error {
			return browse.NewSelection(s.page, nodeID, selector).Click()
		})
	}
	if err := click(); err != nil {
		s.traceAfterAction(start, before, selector, err)
		return nil, err
	}
	if wait {
		if err := s.page.WaitLoad(); err != nil {
			s.traceAfterAction(start, before, selector, err)
			return nil, err
		}
	}

	// Wait for any resulting navigation or DOM update
	if !wait {
		_ = s.page.WaitStable(300 * time.Millisecond)
	}

	s.traceAfterAction(start, before, selector, nil)
	s.recordAction(Action{Type: "click", Selector: selector})
	s.addHistory("click", selector, "", "")
	if condition.count() == 0 {
		if wait {
			return s.actionPageResultUnverified("clicked", selector, "target resolved and click dispatched; document load completed")
		}
		return s.actionPageResultUnverified("clicked", selector, "target resolved and click dispatched; page state read after settling")
	}
	return s.verifyPagePostconditionContextLocked(context.Background(), "clicked", selector, condition)
}

// ClickAndWait clicks an element and waits for a full page navigation.
func (s *Session) ClickAndWait(selector string) (*PageResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ClickWithPostconditionContext(ctx, selector, Postcondition{}, true)
}

// Type types text into an input element.
//
// On Vue v-model / React controlled inputs the framework reactive
// binding can miss a write when the island is still hydrating, or
// when the framework's value setter trap wasn't fired. Type guards
// against this by:
//  1. Briefly waiting for framework hydration on the element.
//  2. Dispatching synthetic input/change/blur events after the CDP
//     keystrokes — belt-and-suspenders for frameworks that listen on
//     dispatched events only.
//  3. Re-reading the value via the prototype getter, so React's
//     internal stateful value tracker doesn't return the stale string.
//
// The result carries both the typed value and the committed value so
// callers can detect races without an extra round-trip.
func (s *Session) Type(selector, text string) (*ElementResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.TypeContext(ctx, selector, text)

}

// TypeContext types and verifies a field under one caller-supplied deadline.
func (s *Session) TypeContext(ctx context.Context, selector, text string) (*ElementResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	if err := s.waitForSelectorContext(ctx, selector); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var invalid *waitutil.InvalidSelectorError
		if !errors.As(err, &invalid) && !strings.Contains(selector, ":text(") && !strings.Contains(selector, ":has-text(") {
			return nil, s.enrichSelectorError(selector, err)
		}
		if _, resolveErr := s.resolveSelectorContext(ctx, selector); resolveErr != nil {
			return nil, s.enrichSelectorError(selector, err)
		}
	}
	nodeID, err := s.resolveSelectorContext(ctx, selector)
	if err != nil {
		s.traceAfterActionContext(ctx, start, before, "type", selector, text, "", err)
		return nil, s.decodeNodeError(selector, err)
	}
	// Hydration is a best-effort enhancement. It may consume at most 500ms and
	// the caller's remaining budget; an expired context stops the input sequence.
	hydrationCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	framework := s.waitForHydrationNodeContext(hydrationCtx, nodeID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sel := browse.NewSelection(s.page, nodeID, selector)
	if err := sel.InputContext(ctx, text); err != nil {
		s.traceAfterActionContext(ctx, start, before, "type", selector, text, "", err)
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("type", selector, "input command was interrupted; the field may have changed, but its value was not verified", err, Postcondition{ValueSelector: selector, Value: text})
		}
		return nil, s.decodeNodeError(selector, err)
	}
	objectID, err := s.page.ResolveNodeContext(ctx, nodeID)
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("type", selector, "input was dispatched; the field value could not be read back", err, Postcondition{ValueSelector: selector, Value: text})
	}
	commitJS := `function(value){const el=this;const proto=el.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;const setter=Object.getOwnPropertyDescriptor(proto,'value');if(setter&&setter.set)setter.set.call(el,value);el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));el.dispatchEvent(new Event('blur',{bubbles:true}));return true}`
	if _, err := s.page.CallContext(ctx, "Runtime.callFunctionOn", map[string]any{"functionDeclaration": commitJS, "objectId": objectID, "arguments": []any{map[string]any{"value": text}}, "returnByValue": true, "awaitPromise": true}); err != nil {
		return nil, s.actionOutcomeAfterDispatch("type", selector, "input was dispatched; framework input events or value verification were interrupted", err, Postcondition{ValueSelector: selector, Value: text})
	}
	if err := s.page.WaitStableContext(ctx, 50*time.Millisecond); err != nil {
		return nil, s.actionOutcomeAfterDispatch("type", selector, "input was dispatched; page settling or value verification was interrupted", err, Postcondition{ValueSelector: selector, Value: text})
	}
	readJS := `function(){const el=this;const proto=el.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;const getter=Object.getOwnPropertyDescriptor(proto,'value');return getter&&getter.get?getter.get.call(el):String(el.innerText||el.textContent||'')}`
	observed, err := s.page.CallContext(ctx, "Runtime.callFunctionOn", map[string]any{"functionDeclaration": readJS, "objectId": objectID, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("type", selector, "input was dispatched; the committed value could not be read", err, Postcondition{ValueSelector: selector, Value: text})
	}
	var readback struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(observed, &readback); err != nil {
		return nil, s.actionOutcomeAfterDispatch("type", selector, "input was dispatched; the committed value could not be decoded", err, Postcondition{ValueSelector: selector, Value: text})
	}
	var committed string
	if len(readback.Result.Value) > 0 {
		_ = json.Unmarshal(readback.Result.Value, &committed)
	}
	verified := committed == text
	s.traceAfterActionContext(ctx, start, before, "type", selector, text, "", nil)
	s.recordAction(Action{Type: "type", Selector: selector, Value: text})
	s.addHistory("type", selector, "", text)
	result := &ElementResult{OperationOrdinal: s.operationOrdinalForResult("type", selector), Selector: selector, TargetID: s.page.TargetID(), Value: text, ValueCommitted: committed, FrameworkReactive: verified, FrameworkDetected: framework, Action: "typed", Verified: verified, Outcome: elementOutcome(verified), Evidence: "input value re-read after framework events", RecoveryHint: elementRecoveryHint(verified)}
	result.Receipt = s.newActionReceipt("type", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified, result.OperationOrdinal, &Postcondition{ValueSelector: selector, Value: text}, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	s.applyTraceResult("type", selector, result.TargetID, result.Outcome, result.Evidence, result.RecoveryHint, result.Verified)
	return result, nil
}

// waitForHydration polls for Vue/React framework hooks on the element
// and returns the detected framework name ("vue", "react", or "none").
// Returns as soon as a hook is seen or budget runs out — never blocks
// longer than budget. Caller must hold s.mu.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) waitForHydration(selector string, budget time.Duration) string {
	selJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function(){
		const el = document.querySelector(%s);
		if (!el) return 'none';
		// Vue 3: __vueParentComponent on the element or any ancestor.
		let cur = el;
		while (cur) {
			if (cur.__vueParentComponent || cur.__vue_app__) return 'vue';
			cur = cur.parentElement;
		}
		// React: __reactProps$<root>/__reactFiber$<root> on the element.
		for (const k of Object.keys(el)) {
			if (k.indexOf('__reactProps$') === 0 || k.indexOf('__reactFiber$') === 0) return 'react';
		}
		return 'none';
	})()`, selJSON)

	deadline := time.Now().Add(budget)
	for {
		result, err := s.page.Evaluate(js)
		if err == nil {
			if str, ok := result.(string); ok && str != "none" {
				return str
			}
		}
		if time.Now().After(deadline) {
			return "none"
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *Session) waitForHydrationNodeContext(ctx context.Context, nodeID int64) string {
	objectID, err := s.page.ResolveNodeContext(ctx, nodeID)
	if err != nil {
		return "none"
	}
	js := `function(){let cur=this;while(cur){if(cur.__vueParentComponent||cur.__vue_app__)return 'vue';cur=cur.parentElement}for(const k of Object.keys(this)){if(k.indexOf('__reactProps$')===0||k.indexOf('__reactFiber$')===0)return 'react'}return 'none'}`
	for {
		if ctx.Err() != nil {
			return "none"
		}
		result, err := s.page.CallContext(ctx, "Runtime.callFunctionOn", map[string]any{"functionDeclaration": js, "objectId": objectID, "returnByValue": true})
		if err == nil {
			var response struct {
				Result struct {
					Value json.RawMessage `json:"value"`
				} `json:"result"`
			}
			if json.Unmarshal(result, &response) == nil {
				var framework string
				_ = json.Unmarshal(response.Result.Value, &framework)
				if framework != "" && framework != "none" {
					return framework
				}
			}
		}
		select {
		case <-ctx.Done():
			return "none"
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// commitInputValue forces a framework-aware value update on the input.
// It uses the prototype value setter (so React's __changeBeforeChange
// detects the write) and dispatches input/change/blur so Vue v-model
// and similar listeners commit the value to reactive state.
// Caller must hold s.mu.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) commitInputValue(selector, text string) {
	selJSON, _ := json.Marshal(selector)
	textJSON, _ := json.Marshal(text)
	js := fmt.Sprintf(`(function(){
		const el = document.querySelector(%s);
		if (!el) return false;
		const proto = el.tagName === 'TEXTAREA'
			? HTMLTextAreaElement.prototype
			: HTMLInputElement.prototype;
		const setter = Object.getOwnPropertyDescriptor(proto, 'value');
		if (setter && setter.set) setter.set.call(el, %s);
		el.dispatchEvent(new Event('input', {bubbles: true}));
		el.dispatchEvent(new Event('change', {bubbles: true}));
		el.dispatchEvent(new Event('blur', {bubbles: true}));
		return true;
	})()`, selJSON, textJSON)
	_, _ = s.page.Evaluate(js)
}

// readCommittedValue reads the input's current value via the prototype
// getter, bypassing any framework-level value-property trap that might
// return the stale internal state instead of the live DOM value.
// Caller must hold s.mu.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) readCommittedValue(selector string) string {
	selJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function(){
		const el = document.querySelector(%s);
		if (!el) return '';
		const proto = el.tagName === 'TEXTAREA'
			? HTMLTextAreaElement.prototype
			: HTMLInputElement.prototype;
		const getter = Object.getOwnPropertyDescriptor(proto, 'value');
		if (getter && getter.get) return String(getter.get.call(el) || '');
		return String(el.value || '');
	})()`, selJSON)
	result, err := s.page.Evaluate(js)
	if err != nil {
		return ""
	}
	if str, ok := result.(string); ok {
		return str
	}
	return ""
}

// FillForm fills multiple form fields and returns their resulting values.
func (s *Session) FillForm(fields map[string]string) (*FormResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.FillFormContext(ctx, fields)

}

// FillFormContext fills fields in stable selector order while holding the
// session lock for the entire transaction. All browser calls and settling
// waits use the caller's context.
func (s *Session) FillFormContext(ctx context.Context, fields map[string]string) (*FormResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	start, before := s.traceBeforeActionContext(ctx)
	selectors := make([]string, 0, len(fields))
	for selector := range fields {
		selectors = append(selectors, selector)
	}
	sort.Strings(selectors)
	result := &FormResult{ContentTrust: ContentTrustUntrustedPage, Fields: make([]FieldResult, 0, len(selectors))}
	traceFailure := func(err error) (*FormResult, error) {
		var outcome *ActionOutcomeError
		if errors.As(err, &outcome) {
			result.Success = false
			result.Outcome = string(OutcomeUnknown)
			result.Evidence = outcome.Evidence
			result.RecoveryHint = "Inspect field-level outcomes and current form values before retrying; some earlier fields may already have changed."
			if outcome.Target != "" && outcome.Action == "fill_form" && !strings.Contains(outcome.Evidence, "after earlier fields changed") && !strings.Contains(outcome.Evidence, "after earlier values") {
				result.Fields = append(result.Fields, FieldResult{
					ContentTrust: ContentTrustUntrustedPage,
					Selector:     outcome.Target, Value: fields[outcome.Target], Outcome: string(OutcomeUnknown),
					Error:        "field mutation may have occurred; value was not verified",
					RecoveryHint: "Read the current field value before retrying this field.",
				})
			}
			outcome.PartialResult = result
		}
		return nil, s.traceActionFailureContext(ctx, start, before, "fill_form", "", "", "", err)
	}
	mutationsDispatched := false
	for _, selector := range selectors {
		if err := ctx.Err(); err != nil {
			if mutationsDispatched {
				return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "form fill was interrupted after earlier fields changed; one or more values may have committed", err))
			}
			return traceFailure(err)
		}
		value := fields[selector]
		nodeID, err := s.resolveSelectorContext(ctx, selector)
		if err != nil {
			if ctx.Err() != nil {
				if mutationsDispatched {
					return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "form fill was interrupted after earlier fields changed; additional values may be incomplete", ctx.Err()))
				}
				return traceFailure(ctx.Err())
			}
			result.Fields = append(result.Fields, FieldResult{ContentTrust: ContentTrustUntrustedPage, Selector: selector, Error: s.decodeNodeError(selector, err).Error(), Outcome: "action_failed", RecoveryHint: "Check that the selector identifies an enabled form field and inspect the page for validation errors."})
			continue
		}
		hydrationCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		framework := s.waitForHydrationNodeContext(hydrationCtx, nodeID)
		cancel()
		if err := ctx.Err(); err != nil {
			if mutationsDispatched {
				return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "form fill was interrupted after earlier fields changed; one or more values may have committed", err))
			}
			return nil, err
		}
		selection := browse.NewSelection(s.page, nodeID, selector)
		inputErr := selection.InputContext(ctx, value)
		if browse.IsStaleNodeError(inputErr) && ctx.Err() == nil {
			// Re-resolve once after framework reconciliation, matching the
			// legacy fill path's stale-node recovery behavior.
			if freshID, resolveErr := s.resolveSelectorContext(ctx, selector); resolveErr == nil {
				nodeID = freshID
				selection = browse.NewSelection(s.page, nodeID, selector)
				inputErr = selection.InputContext(ctx, value)
			}
		}
		if inputErr != nil {
			if mutationOutcomeMayBeUnknown(inputErr) || mutationsDispatched {
				return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "field input command failed after earlier values or a partial field mutation may have occurred", inputErr))
			}
			if ctx.Err() != nil {
				return nil, inputErr
			}
			result.Fields = append(result.Fields, FieldResult{ContentTrust: ContentTrustUntrustedPage, Selector: selector, Error: s.decodeNodeError(selector, inputErr).Error(), Outcome: "action_failed", RecoveryHint: "Check that the selector identifies an enabled form field and inspect the page for validation errors."})
			continue
		}
		mutationsDispatched = true
		objectID, err := s.page.ResolveNodeContext(ctx, nodeID)
		if err != nil {
			return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "field input was dispatched; committed value could not be read", err))
		}
		commit := `function(value){const el=this;const proto=el.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;const setter=Object.getOwnPropertyDescriptor(proto,'value');if(setter&&setter.set)setter.set.call(el,value);el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));el.dispatchEvent(new Event('blur',{bubbles:true}));return true}`
		if _, err := s.page.CallContext(ctx, "Runtime.callFunctionOn", map[string]any{"functionDeclaration": commit, "objectId": objectID, "arguments": []any{map[string]any{"value": value}}, "returnByValue": true, "awaitPromise": true}); err != nil {
			return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "form field was changed; framework state could not be confirmed", err))
		}
		if err := s.page.WaitStableContext(ctx, 50*time.Millisecond); err != nil {
			return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "form field was changed; settling and readback were interrupted", err))
		}
		readback, err := s.page.CallContext(ctx, "Runtime.callFunctionOn", map[string]any{"functionDeclaration": `function(){const el=this;const proto=el.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;const getter=Object.getOwnPropertyDescriptor(proto,'value');return getter&&getter.get?getter.get.call(el):String(el.innerText||el.textContent||'')}`, "objectId": objectID, "returnByValue": true})
		if err != nil {
			return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "form field was changed; committed value could not be read", err))
		}
		var response struct {
			Result struct {
				Value json.RawMessage `json:"value"`
			} `json:"result"`
		}
		if err := json.Unmarshal(readback, &response); err != nil {
			return traceFailure(s.actionOutcomeAfterDispatch("fill_form", selector, "form field was changed; committed value could not be decoded", err))
		}
		var actual string
		if len(response.Result.Value) > 0 {
			_ = json.Unmarshal(response.Result.Value, &actual)
		}
		verified := actual == value
		result.Fields = append(result.Fields, FieldResult{ContentTrust: ContentTrustUntrustedPage, Selector: selector, Value: value, ValueObserved: actual, Success: verified, Verified: verified, Outcome: elementOutcome(verified), Error: fieldValueMismatch(verified), RecoveryHint: elementRecoveryHint(verified), Framework: framework})
	}
	result.Success = true
	for _, field := range result.Fields {
		if !field.Success {
			result.Success = false
			break
		}
	}
	outcome, evidence, recovery := aggregateFillFormOutcome(result)
	s.traceAfterActionContext(ctx, start, before, "fill_form", "", "", "", nil)
	s.applyTraceResult("fill_form", "", s.page.TargetID(), outcome, evidence, recovery, result.Success)
	result.TargetID = s.page.TargetID()
	result.Outcome, result.Evidence, result.RecoveryHint = outcome, evidence, recovery
	result.OperationOrdinal = s.operationOrdinalForResult("fill_form", "")
	result.Receipt = s.newActionReceipt("fill_form", "", result.TargetID, outcome, evidence, recovery, result.Success, result.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, result.Receipt)
	return result, nil
}

func aggregateFillFormOutcome(result *FormResult) (outcome, evidence, recovery string) {
	if result == nil {
		return string(OutcomeActionFailed), "form fill returned no field results", "Inspect the form and retry the failed fill operation."
	}
	if result.Success {
		return string(OutcomePostconditionVerified), "all field values were re-read and matched requested values", ""
	}
	for _, field := range result.Fields {
		if field.Outcome == string(OutcomeActionFailed) {
			return string(OutcomeActionFailed), "one or more fields could not be filled; inspect the field-level results", "Correct missing, disabled, or invalid field targets before submitting."
		}
	}
	return string(OutcomeValueMismatch), "one or more committed field values did not match the requested value", "Inspect field-level outcomes and correct the mismatched values before submitting."
}

func fieldValueMismatch(verified bool) string {
	if verified {
		return ""
	}
	return "value did not match requested value after input"
}

// Extract returns the text content of an element.
func (s *Session) Extract(selector string) (*ElementResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}

	if err := s.waitAndResolve(selector); err != nil {
		return nil, err
	}

	nodeID, err := s.querySelector(selector)
	if err != nil {
		return nil, err
	}
	sel := browse.NewSelection(s.page, nodeID, selector)
	text, err := sel.Text()
	if err != nil {
		return nil, err
	}

	return &ElementResult{
		Selector:     selector,
		ContentTrust: ContentTrustUntrustedPage,
		TargetID:     s.page.TargetID(),
		FrameID:      s.frameID,
		Text:         text,
		Action:       "extracted",
	}, nil
}

// ExtractContext reads one element under the caller's deadline.
func (s *Session) ExtractContext(ctx context.Context, selector string) (*ElementResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if err := s.waitForSelectorContext(ctx, selector); err != nil {
		return nil, err
	}
	nodeID, err := s.resolveSelectorContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	text, err := formNodeCallContext(ctx, s.page, nodeID, `function(){return this.textContent||''}`, nil)
	if err != nil {
		return nil, err
	}
	value, _ := text.(string)
	return &ElementResult{Selector: selector, ContentTrust: ContentTrustUntrustedPage, TargetID: s.page.TargetID(), FrameID: s.frameID, Text: value, Action: "extracted"}, nil
}

// ExtractAll returns text content from all matching elements.
func (s *Session) ExtractAll(selector string) (*ExtractAllResult, error) {
	return s.extractAllPage(selector, 0, 0, 0)
}

// ExtractAllPage returns one bounded slice of matching text. Individual items
// are capped at 4,000 Unicode characters and expose their original lengths.
// HasMore and NextOffset distinguish pagination from item text truncation.
func (s *Session) ExtractAllPage(selector string, offset, limit int) (*ExtractAllResult, error) {
	return s.extractAllPage(selector, offset, limit, 4000)
}

// ExtractAllPageWithOptions returns a bounded page with optional stable node
// handles for callers that need to act on the extracted elements.
func (s *Session) ExtractAllPageWithOptions(selector string, offset, limit int, options ExtractAllOptions) (*ExtractAllResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ExtractAllPageWithOptionsContext(ctx, selector, offset, limit, options)
}

// ExtractAllPageContext extracts a bounded page of matching text under ctx.
func (s *Session) ExtractAllPageContext(ctx context.Context, selector string, offset, limit int) (*ExtractAllResult, error) {
	return s.ExtractAllPageWithOptionsContext(ctx, selector, offset, limit, ExtractAllOptions{})
}

// ExtractAllPageWithOptionsContext extracts a bounded page and can optionally
// stamp stable node handles for callers that need to act on returned elements.
func (s *Session) ExtractAllPageWithOptionsContext(ctx context.Context, selector string, offset, limit int, options ExtractAllOptions) (*ExtractAllResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.extractAllPageWithEvaluatorOptions(selector, offset, limit, 4000, options, func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) })
}

func (s *Session) extractAllPage(selector string, offset, limit, maxTextChars int) (*ExtractAllResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.extractAllPageWithEvaluator(selector, offset, limit, maxTextChars, s.evaluateInFrame)
}

func (s *Session) extractAllPageWithEvaluator(selector string, offset, limit, maxTextChars int, evaluate func(string) (any, error)) (*ExtractAllResult, error) {
	return s.extractAllPageWithEvaluatorOptions(selector, offset, limit, maxTextChars, ExtractAllOptions{}, evaluate)
}

func (s *Session) extractAllPageWithEvaluatorOptions(selector string, offset, limit, maxTextChars int, options ExtractAllOptions, evaluate func(string) (any, error)) (*ExtractAllResult, error) {
	if limit <= 0 {
		limit = s.contentOpts.MaxItems
		if limit <= 0 {
			limit = 50
		}
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	selectorJSON, _ := json.Marshal(selector)
	js := fmt.Sprintf(`(function(){
		const selector=%s, offset=%d, limit=%d, maxChars=%d, includeHandles=%t, handlePrefix='h_extract_all_%d_';
		const elements=Array.from(document.querySelectorAll(selector));
		function selectorFor(el){
			const parts=[];let node=el;const doc=el.ownerDocument;
			while(node&&node.nodeType===1){
				if(node.id){const id='#'+CSS.escape(node.id);if(doc.querySelectorAll(id).length===1){parts.unshift(id);break}}
				let part=node.tagName.toLowerCase();const parent=node.parentElement;
				if(parent){const siblings=Array.from(parent.children).filter(child=>child.tagName===node.tagName);if(siblings.length>1)part+=':nth-of-type('+(siblings.indexOf(node)+1)+')'}
				parts.unshift(part);node=parent;
			}
			return parts.join(' > ');
		}
		function semanticsFor(el){
			const tag=el.tagName.toLowerCase();
			let role=(el.getAttribute('role')||'').trim().split(/\\s+/)[0];
			if(!role){
				if(tag==='a'&&el.hasAttribute('href'))role='link';
				else if(tag==='button')role='button';
				else if(tag==='textarea'||el.isContentEditable)role='textbox';
				else if(tag==='select')role=el.multiple?'listbox':'combobox';
				else if(tag==='input'){
					const inputRoles={button:'button',image:'button',submit:'button',reset:'button',checkbox:'checkbox',radio:'radio',range:'slider',number:'spinbutton',hidden:''};
					role=Object.prototype.hasOwnProperty.call(inputRoles,el.type)?inputRoles[el.type]:'textbox';
				}
				else if(tag==='summary')role='button';
			}
			const interactive=!!(role==='button'||role==='link'||role==='checkbox'||role==='radio'||role==='switch'||role==='menuitem'||role==='option'||role==='slider'||role==='spinbutton'||role==='textbox'||role==='combobox'||role==='listbox'||el.hasAttribute('onclick')||el.hasAttribute('tabindex'));
			return {tag,role,interactive};
		}
		const items=elements.slice(offset,offset+limit).map((el,pageIndex)=>{
			const value=(el.innerText||el.textContent||'').trim();
			const semantics=semanticsFor(el);
			if (!maxChars) {
				const item={text:value,length:Array.from(value).length,truncated:false,selector:selectorFor(el),...semantics};
				if(includeHandles){let handle=el.getAttribute('data-scout-handle');if(!handle){handle=handlePrefix+(offset+pageIndex+1);el.setAttribute('data-scout-handle',handle)}item.handle=handle}
				return item;
			}
			let count=0, parts=[];
			for (const char of value) {
				if (count<maxChars) parts.push(char);
				count++;
			}
			const item={text:parts.join(''),length:count,truncated:count>maxChars,selector:selectorFor(el),...semantics};
			if(includeHandles){let handle=el.getAttribute('data-scout-handle');if(!handle){handle=handlePrefix+(offset+pageIndex+1);el.setAttribute('data-scout-handle',handle)}item.handle=handle}
			return item;
		});
		return JSON.stringify({total:elements.length,items});
	})()`, selectorJSON, offset, limit, maxTextChars, options.IncludeHandles, time.Now().UnixNano())
	result, err := evaluate(js)
	if err != nil {
		return nil, err
	}
	encoded, _ := result.(string)
	var data struct {
		Total int `json:"total"`
		Items []struct {
			Text        string `json:"text"`
			Length      int    `json:"length"`
			Truncated   bool   `json:"truncated"`
			Selector    string `json:"selector"`
			Handle      string `json:"handle"`
			Tag         string `json:"tag"`
			Role        string `json:"role"`
			Interactive bool   `json:"interactive"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(encoded), &data); err != nil {
		return nil, fmt.Errorf("decode extracted items: %w", err)
	}
	items := make([]string, 0, len(data.Items))
	lengths := make([]int, 0, len(data.Items))
	itemTruncated := make([]bool, 0, len(data.Items))
	references := make([]ExtractedTextReference, 0, len(data.Items))
	anyItemTruncated := false
	for _, item := range data.Items {
		items = append(items, item.Text)
		lengths = append(lengths, item.Length)
		itemTruncated = append(itemTruncated, item.Truncated)
		references = append(references, ExtractedTextReference{Selector: item.Selector, Handle: item.Handle, Tag: item.Tag, Role: item.Role, Interactive: item.Interactive})
		anyItemTruncated = anyItemTruncated || item.Truncated
	}
	hasMore, nextOffset, itemsTruncated, truncated := extractionPageState(offset, len(items), data.Total, anyItemTruncated)
	return &ExtractAllResult{
		Selector:       selector,
		ContentTrust:   ContentTrustUntrustedPage,
		TargetID:       s.page.TargetID(),
		FrameID:        s.frameID,
		Count:          len(items),
		Total:          data.Total,
		Offset:         offset,
		Limit:          limit,
		HasMore:        hasMore,
		NextOffset:     nextOffset,
		ItemsTruncated: itemsTruncated,
		Truncated:      truncated,
		Items:          items,
		References:     references,
		ItemLengths:    lengths,
		ItemTruncated:  itemTruncated,
	}, nil
}

func extractionPageState(offset, count, total int, itemsTruncated bool) (hasMore bool, nextOffset int, anyItemsTruncated bool, truncated bool) {
	nextOffset = offset + count
	hasMore = nextOffset < total
	anyItemsTruncated = itemsTruncated
	truncated = hasMore || anyItemsTruncated
	return
}

// ExtractTable extracts structured table data.
func (s *Session) ExtractTable(tableSelector string) (*TableResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.extractTableWithEvaluator(tableSelector, s.evaluateInFrame)
}

// ExtractTableContext reads a bounded table under the caller's deadline.
func (s *Session) ExtractTableContext(ctx context.Context, tableSelector string) (*TableResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.extractTableWithEvaluator(tableSelector, func(js string) (any, error) { return s.evaluateCurrentContext(ctx, js) })
}

func (s *Session) extractTableWithEvaluator(tableSelector string, evaluate func(string) (any, error)) (*TableResult, error) {

	maxRows := s.contentOpts.MaxRows
	if maxRows <= 0 {
		maxRows = 100
	}
	// Bound data in the browser response, while retaining the full row count.
	js := fmt.Sprintf(`(function() {
		const table = document.querySelector(`+jsonQuote(tableSelector)+`);
		if (!table) return null;
		const headers = Array.from(table.querySelectorAll('th')).map(h => h.textContent.trim());
		const allRows = Array.from(table.querySelectorAll('tbody tr, tr')).filter(tr => tr.querySelectorAll('td').length > 0);
		const rows = [];
		for (const tr of allRows.slice(0, %d)) {
			rows.push(Array.from(tr.querySelectorAll('td')).map(c => c.textContent.trim()));
		}
		return {headers: headers, rows: rows, rowCount: allRows.length, colCount: headers.length};
	})()`, maxRows)

	result, err := evaluate(js)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("table %q not found", tableSelector)
	}

	m, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected table result type")
	}

	tr := &TableResult{Selector: tableSelector, ContentTrust: ContentTrustUntrustedPage, TargetID: s.page.TargetID(), FrameID: s.frameID}

	if headers, ok := m["headers"].([]any); ok {
		for _, h := range headers {
			s, _ := h.(string)
			tr.Headers = append(tr.Headers, s)
		}
	}
	if rows, ok := m["rows"].([]any); ok {
		for _, row := range rows {
			if cols, ok := row.([]any); ok {
				r := make([]string, 0, len(cols))
				for _, c := range cols {
					s, _ := c.(string)
					r = append(r, s)
				}
				tr.Rows = append(tr.Rows, r)
			}
		}
	}
	tr.RowCount = len(tr.Rows)
	if count, ok := m["rowCount"].(float64); ok {
		tr.TotalRows = int(count)
	} else {
		tr.TotalRows = tr.RowCount
	}
	tr.ColCount = len(tr.Headers)
	tr.Truncated = tr.TotalRows > tr.RowCount

	return tr, nil
}

// HasElement reports whether an element matching the selector exists right now.
// It uses the same resolver path as Click/Type so the contract is consistent:
// if HasElement returns true, a follow-up action will find the same element
// (modulo intervening DOM mutations, which the stale-node retry handles).
func (s *Session) HasElement(selector string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.page == nil {
		return false
	}
	// Invalidate root cache so we don't return a positive based on a stale tree.
	s.page.InvalidateNodeCache()
	_, err := s.querySelector(selector)
	return err == nil
}

// HasElementContext checks for a matching selector under the caller's deadline.
// Invalid CSS is returned as an error; an ordinary missing match returns false.
func (s *Session) HasElementContext(ctx context.Context, selector string) (bool, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return false, err
	}
	s.page.InvalidateNodeCache()
	_, err := s.resolveSelectorContext(ctx, selector)
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var invalid *waitutil.InvalidSelectorError
	if errors.As(err, &invalid) {
		return false, err
	}
	return false, nil
}

// WaitFor waits until an element matching the selector appears.
func (s *Session) WaitFor(selector string) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.WaitForContext(ctx, selector)
}

// WaitForContext waits for a selector using the caller's deadline, including
// time queued for the session lock and in-flight CDP evaluation.
func (s *Session) WaitForContext(ctx context.Context, selector string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return err
	}
	if err := s.waitForSelectorContext(ctx, selector); err != nil {
		var invalid *waitutil.InvalidSelectorError
		if errors.As(err, &invalid) {
			return err
		}
		return s.enrichSelectorError(selector, err)
	}
	return nil
}

// WaitForSnapshotContext waits for a selector and captures the resulting page
// state while retaining the session lock, so another operation cannot change
// the active page between the wait and its returned evidence.
func (s *Session) WaitForSnapshotContext(ctx context.Context, selector string) (*PageResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if err := s.waitForSelectorContext(ctx, selector); err != nil {
		var invalid *waitutil.InvalidSelectorError
		if errors.As(err, &invalid) {
			return nil, err
		}
		return nil, s.enrichSelectorError(selector, err)
	}
	return s.pageResultContext(ctx)
}

// Eval executes JavaScript and returns the result.
func (s *Session) Eval(js string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensurePage(); err != nil {
		return nil, err
	}
	return s.evaluateInFrame(js)
}

// EvalContext executes JavaScript in the selected frame under ctx.
func (s *Session) EvalContext(ctx context.Context, js string) (any, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.evaluateCurrentContext(ctx, js)
}

// Page returns the underlying Page for advanced operations.
// The caller must not hold the session mutex.
func (s *Session) Page() *browse.Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.page
}

// BrowserTargetsContext lists targets visible to the session's browser.
// The returned frame and worker relationships are diagnostic metadata; callers
// must revalidate a target's parent binding before attaching to it.
func (s *Session) BrowserTargetsContext(ctx context.Context) ([]browse.BrowserTarget, error) {
	if s == nil || ctx == nil {
		return nil, fmt.Errorf("browser target inventory requires a session and context")
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	provider, ok := s.browser.(interface {
		BrowserTargetsContext(context.Context) ([]browse.BrowserTarget, error)
	})
	if !ok {
		return nil, fmt.Errorf("browser target inventory is unavailable")
	}
	return provider.BrowserTargetsContext(ctx)
}

// SetViewport overrides the live page's device metrics so width-based CSS
// media queries resolve against the given size. scale is the device pixel
// ratio (<=0 = 1); mobile toggles mobile emulation. The width/height are
// remembered so a session Reset re-applies them.
func (s *Session) SetViewport(width, height int, scale float64, mobile bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.SetViewportContext(ctx, width, height, scale, mobile)
}

// SetViewportContext applies and verifies device metrics within ctx.
func (s *Session) SetViewportContext(ctx context.Context, width, height int, scale float64, mobile bool) error {
	_, err := s.SetViewportWithOutcomeContext(ctx, width, height, scale, mobile)
	return err
}

// SetViewportWithOutcomeContext applies device metrics and returns their
// verified readback under the caller's deadline.
func (s *Session) SetViewportWithOutcomeContext(ctx context.Context, width, height int, scale float64, mobile bool) (*ViewportResult, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	if scale <= 0 {
		scale = 1
	}
	previous := s.viewport
	s.viewport = [2]int{width, height}
	params := map[string]any{"width": width, "height": height, "deviceScaleFactor": scale, "mobile": mobile}
	if _, err := s.page.CallContext(ctx, "Emulation.setDeviceMetricsOverride", params); err != nil {
		if mutationOutcomeMayBeUnknown(err) {
			return nil, s.actionOutcomeAfterDispatch("set_viewport", fmt.Sprintf("%dx%d", width, height), "device metrics may have changed but the command result is unknown", err)
		}
		s.viewport = previous
		return nil, err
	}
	result, err := s.page.EvaluateContext(ctx, `({width:window.innerWidth,height:window.innerHeight,scale:window.devicePixelRatio})`)
	if err != nil {
		return nil, s.actionOutcomeAfterDispatch("set_viewport", fmt.Sprintf("%dx%d", width, height), "device metrics were sent but viewport state could not be read back", err)
	}
	state, ok := result.(map[string]any)
	if !ok {
		return nil, s.actionOutcomeAfterDispatch("set_viewport", fmt.Sprintf("%dx%d", width, height), "device metrics were sent but viewport readback had an unexpected shape", fmt.Errorf("unexpected viewport readback %T", result))
	}
	actualWidth, _ := state["width"].(float64)
	actualHeight, _ := state["height"].(float64)
	actualScale, _ := state["scale"].(float64)
	if actualWidth != float64(width) || actualHeight != float64(height) || actualScale != scale {
		return nil, s.actionOutcomeAfterDispatch("set_viewport", fmt.Sprintf("%dx%d", width, height), fmt.Sprintf("requested metrics but read back %gx%g at scale %g", actualWidth, actualHeight, actualScale), fmt.Errorf("viewport readback mismatch"))
	}
	out := &ViewportResult{
		Action:   "set_viewport",
		TargetID: s.page.TargetID(),
		Width:    width,
		Height:   height,
		Scale:    scale,
		Mobile:   mobile,
		Outcome:  string(OutcomePostconditionVerified),
		Verified: true,
		Evidence: fmt.Sprintf("read back window.innerWidth=%g, window.innerHeight=%g, window.devicePixelRatio=%g", actualWidth, actualHeight, actualScale),
	}
	out.OperationOrdinal = s.nextOperationOrdinal()
	out.Receipt = s.newActionReceipt(out.Action, fmt.Sprintf("%dx%d", width, height), out.TargetID, out.Outcome, out.Evidence, "", out.Verified, out.OperationOrdinal, nil, ctx)
	actionReceiptDeadline(ctx, out.Receipt)
	s.applyTraceResult("set_viewport", fmt.Sprintf("%dx%d", width, height), out.TargetID, out.Outcome, out.Evidence, "", true)
	return out, nil
}

// Screenshot captures the page as an image.
// Automatically compresses to fit within MaxScreenshotBytes (default 5MB).
func (s *Session) Screenshot() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.ScreenshotContext(ctx)
}

// ScreenshotContext captures the current page under the caller's deadline.
func (s *Session) ScreenshotContext(ctx context.Context) ([]byte, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.page.ScreenshotWithOptionsContext(ctx, browse.ScreenshotOptions{MaxSize: s.contentOpts.MaxScreenshotBytes})
}

// ScreenshotWithOptionsContext captures the current page using the supplied
// screenshot options under the caller's deadline.
func (s *Session) ScreenshotWithOptionsContext(ctx context.Context, opts browse.ScreenshotOptions) ([]byte, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.page.ScreenshotWithOptionsContext(ctx, opts)
}

// ScreenshotWithOptionsInfoContext captures an image and returns compression
// details alongside the bytes for callers that need to explain image fidelity.
func (s *Session) ScreenshotWithOptionsInfoContext(ctx context.Context, opts browse.ScreenshotOptions) ([]byte, browse.ScreenshotInfo, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, browse.ScreenshotInfo{}, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, browse.ScreenshotInfo{}, err
	}
	return s.page.ScreenshotWithOptionsInfoContext(ctx, opts)
}

// PDF generates a PDF of the page.
func (s *Session) PDF() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	return s.PDFContext(ctx)
}

// PDFContext generates a PDF of the current page under the caller's deadline.
func (s *Session) PDFContext(ctx context.Context) ([]byte, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if err := s.ensurePageContext(ctx); err != nil {
		return nil, err
	}
	return s.page.PDFWithOptionsContext(ctx, browse.PDFOptions{})
}

// waitAndResolve waits for an element to appear before interacting.
// Supports both CSS selectors and Playwright-style :text('...') selectors.
func (s *Session) waitAndResolve(selector string) error {
	// Try CSS first
	err := s.waitForSelector(selector)
	if err == nil {
		return nil
	}
	// Try Playwright-style text selector as fallback
	_, resolveErr := s.resolveSelector(selector)
	if resolveErr == nil {
		return nil
	}
	return s.enrichSelectorError(selector, err) // wrap original CSS error with diagnostics
}

// querySelector resolves a selector to a nodeID, supporting Playwright-style syntax.
func (s *Session) querySelector(selector string) (int64, error) {
	return s.resolveSelector(selector)
}

// withStaleNodeRetry runs an action with a freshly-resolved nodeID. If the action
// fails with a CDP "Could not find node" error — typically Vue/React reconciling
// the DOM between resolution and action — the selector is re-resolved against a
// fresh DOM cache and the action is retried once. Caller must hold s.mu.
//
//nolint:unused // Kept for existing tests; golangci-lint excludes test files.
func (s *Session) withStaleNodeRetry(selector string, action func(nodeID int64) error) error {
	nodeID, err := s.querySelector(selector)
	if err != nil {
		return s.markIfDead(err)
	}
	if err = action(nodeID); err == nil {
		return nil
	}
	if !browse.IsStaleNodeError(err) {
		return s.markIfDead(err)
	}
	// Re-resolve from a fresh DOM cache and retry once.
	s.page.InvalidateNodeCache()
	nodeID, resolveErr := s.querySelector(selector)
	if resolveErr != nil {
		return s.markIfDead(resolveErr)
	}
	return s.markIfDead(action(nodeID))
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
