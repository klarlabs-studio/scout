package agent

import (
	"fmt"

	browse "go.klarlabs.de/scout"
)

// ContentTrustUntrustedPage marks text, labels, URLs, and values read from a
// web page. Callers must treat these values as untrusted input, never as
// instructions or authorization policy.
const ContentTrustUntrustedPage = "untrusted_page_content"

// ActionOutcome is the stable vocabulary used by structured action results.
// Keep the JSON representation as a string so existing Go and MCP clients remain compatible.
type ActionOutcome string

const (
	OutcomeActionFailed          ActionOutcome = "action_failed"
	OutcomeNavigationCompleted   ActionOutcome = "navigation_completed"
	OutcomePostconditionVerified ActionOutcome = "postcondition_verified"
	OutcomePostconditionFailed   ActionOutcome = "postcondition_failed"
	OutcomeEffectUnverified      ActionOutcome = "effect_unverified"
	OutcomeUnknown               ActionOutcome = "outcome_unknown"
	OutcomeEventObserved         ActionOutcome = "event_observed"
	OutcomeValueVerified         ActionOutcome = "value_verified"
	OutcomeValueMismatch         ActionOutcome = "value_mismatch"
	OutcomeResponseObserved      ActionOutcome = "response_observed"
	OutcomeResponseUnobserved    ActionOutcome = "response_unobserved"
	OutcomeResponseFailed        ActionOutcome = "response_failed"
	OutcomeFrameSelected         ActionOutcome = "frame_selected"
	OutcomeMainFrameSelected     ActionOutcome = "main_frame_selected"
)

// IsKnown reports whether the value belongs to the documented stable vocabulary.
func (o ActionOutcome) IsKnown() bool {
	switch o {
	case OutcomeActionFailed, OutcomeNavigationCompleted, OutcomePostconditionVerified,
		OutcomePostconditionFailed, OutcomeEffectUnverified, OutcomeUnknown,
		OutcomeEventObserved, OutcomeValueVerified, OutcomeValueMismatch,
		OutcomeResponseObserved, OutcomeResponseUnobserved, OutcomeResponseFailed,
		OutcomeFrameSelected, OutcomeMainFrameSelected:
		return true
	default:
		return false
	}
}

// IsKnownActionOutcome validates the stable outcome vocabulary without
// requiring callers to change existing string-typed result handling.
func IsKnownActionOutcome(value string) bool { return ActionOutcome(value).IsKnown() }

// PageResult is the structured response after a navigation or page-level action.
// Verified is true only when the requested postcondition was directly checked.
// For actions without a postcondition, Evidence describes what was observed
// and Verified remains false to distinguish dispatch from outcome.
type PageResult struct {
	OperationOrdinal  int                  `json:"operation_ordinal,omitempty"`
	URL               string               `json:"url"`
	ContentTrust      string               `json:"content_trust,omitempty"`
	Title             string               `json:"title"`
	TargetID          string               `json:"target_id,omitempty"`
	FrameID           string               `json:"frame_id,omitempty"`
	Action            string               `json:"action,omitempty"`
	Target            string               `json:"target,omitempty"`
	Outcome           string               `json:"outcome,omitempty"`
	Verified          bool                 `json:"verified,omitempty"`
	VerificationKnown bool                 `json:"verification_known,omitempty"`
	Evidence          string               `json:"evidence,omitempty"`
	RecoveryHint      string               `json:"recovery_hint,omitempty"`
	Receipt           *ActionReceipt       `json:"action_receipt,omitempty"`
	CookieDismissal   *CookieDismissResult `json:"cookie_dismissal,omitempty"`
}

// ActionReceipt is the versioned, portable evidence record for one browser
// operation. New Scout-generated receipts include a policy snapshot. Target
// descriptions, evidence, and identifiers can contain sensitive data and
// should be redacted by the caller before external export.
type ActionReceipt struct {
	SchemaVersion              int                     `json:"schema_version"`
	ContentTrust               string                  `json:"content_trust,omitempty"`
	RunID                      string                  `json:"run_id,omitempty"`
	OperationOrdinal           int                     `json:"operation_ordinal"`
	RecoveryOfOperationOrdinal int                     `json:"recovery_of_operation_ordinal,omitempty"`
	PolicyID                   string                  `json:"policy_id,omitempty"`
	Policy                     *ActionPolicyEvidence   `json:"policy,omitempty"`
	Action                     string                  `json:"action"`
	BrowserOwnership           string                  `json:"browser_ownership,omitempty"`
	Target                     string                  `json:"target,omitempty"`
	TargetID                   string                  `json:"target_id,omitempty"`
	BrowserInstanceID          string                  `json:"browser_instance_id,omitempty"`
	FrameID                    string                  `json:"frame_id,omitempty"`
	Outcome                    ActionOutcome           `json:"outcome"`
	Verified                   bool                    `json:"verified"`
	Verification               VerificationState       `json:"verification_state"`
	Evidence                   string                  `json:"evidence,omitempty"`
	RecoveryHint               string                  `json:"recovery_hint,omitempty"`
	DeadlineAt                 int64                   `json:"deadline_at_ms,omitempty"`
	StartedAt                  int64                   `json:"started_at_ms,omitempty"`
	DurationMS                 int64                   `json:"duration_ms,omitempty"`
	TraceID                    string                  `json:"trace_id,omitempty"`
	OTelTraceID                string                  `json:"otel_trace_id,omitempty"`
	OTelSpanID                 string                  `json:"otel_span_id,omitempty"`
	TraceAction                int                     `json:"trace_action_ordinal,omitempty"`
	Postcondition              *Postcondition          `json:"expected_postcondition,omitempty"`
	ResponseEvidence           *ActionResponseEvidence `json:"response_evidence,omitempty"`
}

// ActionPolicyEvidence snapshots the effective session policy inputs and
// enforcement boundary at action time. It describes Scout's configured page
// policy and proxy setup; it does not claim browser-wide egress enforcement.
type ActionPolicyEvidence struct {
	ID                   string                      `json:"id"`
	AllowedOrigins       []string                    `json:"allowed_origins"`
	AllowPrivateIPs      bool                        `json:"allow_private_ips"`
	Enforcement          string                      `json:"enforcement"`
	ProxyConfigured      bool                        `json:"proxy_configured"`
	ProxyAppliedByScout  bool                        `json:"proxy_applied_by_scout"`
	RemoteHostPolicy     string                      `json:"remote_host_policy"`
	KnownUnobservedPaths []string                    `json:"known_unobserved_paths"`
	EvidenceReference    string                      `json:"evidence_reference,omitempty"`
	DecisionWindow       *ActionPolicyDecisionWindow `json:"decision_window,omitempty"`
}

// ActionPolicyDecisionWindow summarizes URL-policy decisions observed for the
// action's target during its execution interval. It does not assert causality.
type ActionPolicyDecisionWindow struct {
	TargetID        string `json:"target_id"`
	StartedAtMS     int64  `json:"started_at_ms"`
	EndedAtMS       int64  `json:"ended_at_ms"`
	DecisionIndices []int  `json:"decision_indices"`
	Allowed         int    `json:"allowed"`
	Blocked         int    `json:"blocked"`
}

// ActionResponseEvidence preserves response facts already observed when a
// mutation's application-level result remains unknown. URL is sanitized before
// the evidence enters a portable recovery record or error envelope.
type ActionResponseEvidence struct {
	Observed   bool   `json:"observed"`
	Method     string `json:"method,omitempty"`
	URL        string `json:"url,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
}

// VerificationState distinguishes a checked positive result, a checked
// negative result, an ambiguous operation, and an operation that did not
// request or perform a postcondition check.
type VerificationState string

const (
	VerificationVerified   VerificationState = "verified"
	VerificationRejected   VerificationState = "rejected"
	VerificationUnknown    VerificationState = "unknown"
	VerificationUnverified VerificationState = "unverified"
)

// ViewportResult reports the device metrics Scout requested and read back.
// Mobile records the requested emulation mode; verification covers the CSS
// viewport dimensions and device pixel ratio.
type ViewportResult struct {
	OperationOrdinal int            `json:"operation_ordinal,omitempty"`
	Action           string         `json:"action"`
	TargetID         string         `json:"target_id,omitempty"`
	Width            int            `json:"width"`
	Height           int            `json:"height"`
	Scale            float64        `json:"device_scale_factor"`
	Mobile           bool           `json:"mobile"`
	Outcome          string         `json:"outcome"`
	Verified         bool           `json:"verified"`
	Evidence         string         `json:"evidence"`
	Receipt          *ActionReceipt `json:"action_receipt,omitempty"`
}

// ResetResult describes the browser engine postcondition after a reset. Page
// creation stays lazy, so TargetID is populated only after a later page action.
type ResetResult struct {
	OperationOrdinal int            `json:"operation_ordinal,omitempty"`
	Action           string         `json:"action"`
	Outcome          string         `json:"outcome"`
	Verified         bool           `json:"verified"`
	BrowserAlive     bool           `json:"browser_alive"`
	PageReady        bool           `json:"page_ready"`
	TargetID         string         `json:"target_id,omitempty"`
	Evidence         string         `json:"evidence"`
	Receipt          *ActionReceipt `json:"action_receipt,omitempty"`
}

// Postcondition describes a page state that must be observed after an action.
// At most one field should be set. Empty postconditions leave the effect
// explicitly unverified.
type Postcondition struct {
	Text            string `json:"text,omitempty"`
	URL             string `json:"url,omitempty"`
	URLContains     string `json:"url_contains,omitempty"`
	Title           string `json:"title,omitempty"`
	ExistsSelector  string `json:"exists_selector,omitempty"`
	VisibleSelector string `json:"visible_selector,omitempty"`
	ValueSelector   string `json:"value_selector,omitempty"`
	Value           string `json:"value,omitempty"`
	CountSelector   string `json:"count_selector,omitempty"`
	Count           *int   `json:"count,omitempty"`
	TargetAbsent    string `json:"target_absent,omitempty"`
}

// Validate returns an error unless exactly one supported condition is set.
func (p Postcondition) Validate() error {
	count := 0
	if p.Text != "" {
		count++
	}
	if p.URL != "" {
		count++
	}
	if p.URLContains != "" {
		count++
	}
	if p.Title != "" {
		count++
	}
	if p.ExistsSelector != "" {
		count++
	}
	if p.VisibleSelector != "" {
		count++
	}
	if p.ValueSelector != "" || p.Value != "" {
		count++
	}
	if p.CountSelector != "" || p.Count != nil {
		count++
	}
	if p.TargetAbsent != "" {
		count++
	}
	if count != 1 {
		return fmt.Errorf("postcondition requires exactly one condition: text, url, url_contains, title, exists_selector, visible_selector, value_selector/value, count_selector/count, or target_absent")
	}
	if p.ValueSelector == "" && p.Value != "" {
		return fmt.Errorf("value postcondition requires value_selector")
	}
	if (p.CountSelector != "") != (p.Count != nil) {
		return fmt.Errorf("count postcondition requires both count_selector and count")
	}
	if p.Count != nil && *p.Count < 0 {
		return fmt.Errorf("count postcondition must be non-negative")
	}
	return nil
}

// ElementResult is the structured response for single-element operations.
//
// For Type, `Value` is what the caller asked us to type. `ValueCommitted`
// is the value re-read from the DOM after dispatching input/change/blur
// — these diverge under Vue v-model / React controlled inputs when the
// framework either wasn't hydrated yet (race) or transformed the input
// (trim, mask). `FrameworkReactive` is true when both match.
type ElementResult struct {
	OperationOrdinal  int            `json:"operation_ordinal,omitempty"`
	Selector          string         `json:"selector"`
	TargetID          string         `json:"target_id,omitempty"`
	FrameID           string         `json:"frame_id,omitempty"`
	Text              string         `json:"text,omitempty"`
	ContentTrust      string         `json:"content_trust,omitempty"`
	TextLength        int            `json:"text_length,omitempty"`
	TextTruncated     bool           `json:"text_truncated,omitempty"`
	Value             string         `json:"value,omitempty"`
	ValueCommitted    string         `json:"value_committed,omitempty"`
	FrameworkReactive bool           `json:"framework_reactive,omitempty"`
	FrameworkDetected string         `json:"framework_detected,omitempty"` // "vue", "react", "none"
	Action            string         `json:"action"`
	Verified          bool           `json:"verified,omitempty"`
	Outcome           string         `json:"outcome,omitempty"`
	Evidence          string         `json:"evidence,omitempty"`
	RecoveryHint      string         `json:"recovery_hint,omitempty"`
	Receipt           *ActionReceipt `json:"action_receipt,omitempty"`
}

// ExtractAllResult is the structured response for multi-element extraction.
type ExtractAllResult struct {
	Selector       string                   `json:"selector"`
	TargetID       string                   `json:"target_id,omitempty"`
	FrameID        string                   `json:"frame_id,omitempty"`
	Count          int                      `json:"count"`
	Total          int                      `json:"total"`
	Offset         int                      `json:"offset,omitempty"`
	Limit          int                      `json:"limit,omitempty"`
	HasMore        bool                     `json:"has_more"`
	NextOffset     int                      `json:"next_offset"`
	ItemsTruncated bool                     `json:"items_truncated"`
	Truncated      bool                     `json:"truncated,omitempty"`
	Items          []string                 `json:"items"`
	ContentTrust   string                   `json:"content_trust,omitempty"`
	References     []ExtractedTextReference `json:"references,omitempty"`
	ItemLengths    []int                    `json:"item_lengths,omitempty"`
	ItemTruncated  []bool                   `json:"item_truncated,omitempty"`
}

// ExtractedTextReference maps an extracted value back to its element at the
// time of extraction. Selectors are useful for inspection; handles preserve
// node identity across reordering while the element remains attached.
type ExtractedTextReference struct {
	Selector    string `json:"selector"`
	Handle      string `json:"handle,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Role        string `json:"role,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
}

// ExtractedElementSemantics summarizes the source element's DOM tag, role,
// and common interaction affordances. Interactive is a hint, not proof that
// the element is visible or currently actionable.
type ExtractedElementSemantics struct {
	Tag         string `json:"tag,omitempty"`
	Role        string `json:"role,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
}

// ExtractAllOptions controls optional identity stamping for extracted nodes.
type ExtractAllOptions struct {
	IncludeHandles bool
}

// ContentResult describes bounded textual page content returned by MCP.
type ContentResult struct {
	Content      string `json:"content"`
	ContentTrust string `json:"content_trust,omitempty"`
	TargetID     string `json:"target_id,omitempty"`
	FrameID      string `json:"frame_id,omitempty"`
	Length       int    `json:"length"`
	MaxLength    int    `json:"max_length"`
	TotalLength  int    `json:"total_length"`
	Truncated    bool   `json:"truncated"`
}

// TableResult is the structured response for table extraction.
type TableResult struct {
	Selector          string     `json:"selector"`
	TargetID          string     `json:"target_id,omitempty"`
	FrameID           string     `json:"frame_id,omitempty"`
	Headers           []string   `json:"headers"`
	Rows              [][]string `json:"rows"`
	ContentTrust      string     `json:"content_trust,omitempty"`
	RowCount          int        `json:"row_count"`
	TotalRows         int        `json:"total_rows"`
	ColCount          int        `json:"col_count"`
	TotalCols         int        `json:"total_cols,omitempty"`
	CellTextTruncated bool       `json:"cell_text_truncated,omitempty"`
	Truncated         bool       `json:"truncated,omitempty"`
}

// FormResult is the structured response for form filling.
type FormResult struct {
	OperationOrdinal int            `json:"operation_ordinal,omitempty"`
	ContentTrust     string         `json:"content_trust,omitempty"`
	TargetID         string         `json:"target_id,omitempty"`
	Outcome          string         `json:"outcome,omitempty"`
	Evidence         string         `json:"evidence,omitempty"`
	RecoveryHint     string         `json:"recovery_hint,omitempty"`
	Receipt          *ActionReceipt `json:"action_receipt,omitempty"`
	Fields           []FieldResult  `json:"fields"`
	Success          bool           `json:"success"`
}

// FieldResult describes the outcome of filling a single field.
type FieldResult struct {
	ContentTrust  string `json:"content_trust,omitempty"`
	Selector      string `json:"selector"`
	Value         string `json:"value,omitempty"`
	ValueObserved string `json:"value_observed,omitempty"`
	Success       bool   `json:"success"`
	Verified      bool   `json:"verified,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Error         string `json:"error,omitempty"`
	RecoveryHint  string `json:"recovery_hint,omitempty"`
	Framework     string `json:"framework_detected,omitempty"`
}

// Observation is a structured snapshot of the visible page for agent context.
type Observation struct {
	URL              string            `json:"url"`
	ContentTrust     string            `json:"content_trust,omitempty"`
	TargetID         string            `json:"target_id,omitempty"`
	FrameID          string            `json:"frame_id,omitempty"`
	URLLength        int               `json:"url_length"`
	Title            string            `json:"title"`
	TitleLength      int               `json:"title_length"`
	Text             string            `json:"text"`
	TextLength       int               `json:"text_length"`
	TextTruncated    bool              `json:"text_truncated"`
	Budget           int               `json:"budget,omitempty"`
	BudgetUsed       int               `json:"budget_used,omitempty"`
	BudgetTruncated  bool              `json:"budget_truncated,omitempty"`
	Links            []LinkInfo        `json:"links,omitempty"`
	LinksTotal       int               `json:"links_total"`
	Inputs           []InputInfo       `json:"inputs,omitempty"`
	InputsTotal      int               `json:"inputs_total"`
	Buttons          []ButtonInfo      `json:"buttons,omitempty"`
	ButtonsTotal     int               `json:"buttons_total"`
	Interactive      int               `json:"interactive_elements"`
	Meta             map[string]string `json:"meta,omitempty"`
	MetaTotal        int               `json:"meta_total"`
	Truncated        bool              `json:"truncated"`
	HasDialog        bool              `json:"has_dialog,omitempty"`
	DialogType       string            `json:"dialog_type,omitempty"` // dialog, modal, overlay
	DialogText       string            `json:"dialog_text,omitempty"`
	ActiveTab        string            `json:"active_tab,omitempty"`        // text of [role=tab][aria-selected=true]
	ActiveTabID      string            `json:"active_tab_id,omitempty"`     // id/data-tab-id of selected tab
	ActiveNavigation []string          `json:"active_navigation,omitempty"` // breadcrumb of [aria-current=page] / .active links + page H1
	Cookies          *CookieSummary    `json:"cookies,omitempty"`
	ScopeMatched     int               `json:"scope_matched,omitempty"`
	ScopeSelectors   []string          `json:"scope_selectors,omitempty"`
	ScopeMisses      []string          `json:"scope_misses,omitempty"`
}

// LinkInfo describes a link on the page.
type LinkInfo struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Text         string `json:"text"`
	Href         string `json:"href"`
	Cost         string `json:"cost,omitempty"` // "high" (navigation), "medium" (ajax), "low" (anchor)
}

// CookieSummary is a compact view of cookies for the current page (no values).
type CookieSummary struct {
	ContentTrust string   `json:"content_trust,omitempty"`
	Count        int      `json:"count"`
	Names        []string `json:"names,omitempty"`
}

// InputInfo describes an input element.
type InputInfo struct {
	ContentTrust string `json:"content_trust,omitempty"`
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Type         string `json:"type"`
	Value        string `json:"value,omitempty"`
	Placeholder  string `json:"placeholder,omitempty"`
	Checked      *bool  `json:"checked,omitempty"` // populated for checkbox/radio inputs only
	Label        string `json:"label,omitempty"`   // resolved label/aria-label, helpful for checkbox/radio identification
}

// ButtonInfo describes a button element.
type ButtonInfo struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Text         string `json:"text"`
	ID           string `json:"id,omitempty"`
	Type         string `json:"type,omitempty"`
	Cost         string `json:"cost,omitempty"` // "high" (submit/navigation), "medium" (action), "low" (toggle)
}

// --- DOM Diff types ---

// DOMDiff represents changes between two Observe() calls.
type DOMDiff struct {
	ContentTrust   string       `json:"content_trust,omitempty"`
	Added          []DOMElement `json:"added,omitempty"`
	AddedTotal     int          `json:"added_total"`
	Removed        []DOMElement `json:"removed,omitempty"`
	RemovedTotal   int          `json:"removed_total"`
	Modified       []DOMChange  `json:"modified,omitempty"`
	ModifiedTotal  int          `json:"modified_total"`
	Truncated      bool         `json:"truncated"`
	HasDiff        bool         `json:"has_diff"`
	Classification string       `json:"classification,omitempty"` // navigation, content_loaded, modal_appeared, form_error, notification, loading_complete, element_state_changed, minor_update
	Summary        string       `json:"summary,omitempty"`        // human-readable one-line summary
}

// DOMElement describes an element that was added or removed.
type DOMElement struct {
	Tag     string `json:"tag"`
	ID      string `json:"id,omitempty"`
	Classes string `json:"classes,omitempty"`
	Text    string `json:"text,omitempty"`
}

// DOMChange describes a modification to an existing element.
type DOMChange struct {
	Tag        string `json:"tag"`
	ID         string `json:"id,omitempty"`
	Attribute  string `json:"attribute,omitempty"`
	OldValue   string `json:"old_value,omitempty"`
	NewValue   string `json:"new_value,omitempty"`
	ChangeType string `json:"change_type"` // "attribute", "text", "children"
}

// --- Network Capture types ---

// NetworkCapture holds a captured network request/response pair.
type NetworkCapture struct {
	captureID              uint64            `json:"-"`
	explicitCaptureAtStart bool              `json:"-"`
	diagnosticsAtStart     bool              `json:"-"`
	TargetID               string            `json:"target_id,omitempty"`
	ObservedAtUnixMS       int64             `json:"observed_at_unix_ms,omitempty"`
	FinishedAtUnixMS       int64             `json:"finished_at_unix_ms,omitempty"`
	URL                    string            `json:"url"`
	ContentTrust           string            `json:"content_trust,omitempty"`
	Method                 string            `json:"method"`
	Status                 int               `json:"status"`
	MimeType               string            `json:"mime_type,omitempty"`
	RequestHeaders         map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders        map[string]string `json:"response_headers,omitempty"`
	RequestBody            string            `json:"request_body,omitempty"`
	ResponseBody           string            `json:"response_body,omitempty"`
	RequestBodyTruncated   bool              `json:"request_body_truncated,omitempty"`
	ResponseBodyTruncated  bool              `json:"response_body_truncated,omitempty"`
	FromHistory            bool              `json:"from_history,omitempty"`
}

// SessionStatus provides health and diagnostics for the active browser session.
type SessionStatus struct {
	ContentTrust string `json:"content_trust,omitempty"`
	BrowserAlive bool   `json:"browser_alive"`
	SessionAlive bool   `json:"session_alive"`
	// BrowserOwnership describes whether Scout launched, attached to, or was
	// given the browser handle. It helps operators understand Close semantics.
	BrowserOwnership string `json:"browser_ownership,omitempty"`
	// BrowserInstanceID is Scout's vendor-local DevTools/context fingerprint.
	// Cross-runner continuity must use a host/broker-issued generation token.
	BrowserInstanceID string `json:"browser_instance_id,omitempty"`
	// BrowserContextIsolated reports whether this session is configured to use
	// a separate remote BrowserContext. It never exposes the provider context ID.
	BrowserContextIsolated bool `json:"browser_context_isolated,omitempty"`
	// BrowserEndpoint is a credential-safe scheme/host label for remote CDP.
	BrowserEndpoint string `json:"browser_endpoint,omitempty"`
	// EndpointSource identifies the configuration source for the browser endpoint.
	EndpointSource string `json:"endpoint_source,omitempty"`
	// AllowedOrigins and PolicyID identify Scout's exact page-level origin
	// policy. They do not claim service-worker or WebSocket egress enforcement.
	AllowedOrigins    []string `json:"allowed_origins,omitempty"`
	AllowPrivateIPs   bool     `json:"allow_private_ips"`
	PolicyID          string   `json:"policy_id"`
	PolicyEnforcement string   `json:"policy_enforcement"`
	// ProxyConfigured and ProxyAppliedByScout describe configuration only; they
	// do not attest that browser traffic used an enforcing proxy. RemoteHostPolicy
	// is unverified for remote CDP because its network is managed externally.
	ProxyConfigured     bool   `json:"proxy_configured"`
	ProxyAppliedByScout bool   `json:"proxy_applied_by_scout"`
	RemoteHostPolicy    string `json:"remote_host_policy"`
	// Recoverable reports whether ResetContext can recreate/reconnect this
	// session. It is false for closed sessions and caller-injected browsers,
	// which require the caller to provide a replacement handle.
	Recoverable  bool   `json:"recoverable"`
	RecoveryHint string `json:"recovery_hint,omitempty"`
	// SessionDead is set when the most recent CDP call returned a connection
	// failure (broken pipe, connection reset, websocket closed). Callers should
	// reset the session via configure { fresh: true } before retrying.
	SessionDead         bool   `json:"session_dead,omitempty"`
	DeadReason          string `json:"dead_reason,omitempty"`
	ActiveTab           string `json:"active_tab,omitempty"`
	ActiveTargetID      string `json:"active_target_id,omitempty"`
	CurrentURL          string `json:"current_url,omitempty"`
	PendingRequests     int    `json:"pending_requests"`
	InFlightCommands    int    `json:"inflight_command_count"`
	ConsecutiveTimeouts int    `json:"consecutive_timeouts"`
	LastError           string `json:"last_error,omitempty"`
	LastSuccessAt       string `json:"last_success_at,omitempty"`
	LastRecoveryAt      string `json:"last_recovery_at,omitempty"`
}

// OperationError captures structured context for action failures.
type OperationError struct {
	Phase         string `json:"phase"`
	Cause         string `json:"cause"`
	TargetID      string `json:"target_id,omitempty"`
	RecoveryHint  string `json:"recovery_hint,omitempty"`
	URL           string `json:"url,omitempty"`
	StatusCode    int    `json:"status_code,omitempty"`
	Detail        string `json:"detail,omitempty"`
	OriginalError string `json:"original_error"`
}

// ActionOutcomeError reports when a mutation may have been dispatched but its
// resulting state could not be verified. Step is the one-based sequence of
// unknown outcomes within the session, not a run-wide action ordinal. Callers
// should inspect current state before retrying because the original action may
// have completed.
type ActionOutcomeError struct {
	ContentTrust          string                  `json:"content_trust,omitempty"`
	RunID                 string                  `json:"run_id,omitempty"`
	Step                  int                     `json:"step,omitempty"`
	OperationOrdinal      int                     `json:"operation_ordinal,omitempty"`
	Action                string                  `json:"action"`
	Target                string                  `json:"target,omitempty"`
	TargetID              string                  `json:"target_id,omitempty"`
	BrowserInstanceID     string                  `json:"browser_instance_id,omitempty"`
	Outcome               ActionOutcome           `json:"outcome"`
	Evidence              string                  `json:"evidence,omitempty"`
	RecoveryHint          string                  `json:"recovery_hint,omitempty"`
	ExpectedPostcondition *Postcondition          `json:"expected_postcondition,omitempty"`
	ResponseEvidence      *ActionResponseEvidence `json:"response_evidence,omitempty"`
	URL                   string                  `json:"url,omitempty"`
	FrameID               string                  `json:"frame_id,omitempty"`
	TraceID               string                  `json:"trace_id,omitempty"`
	TraceStep             int                     `json:"trace_step,omitempty"`
	TraceActionOrdinal    int                     `json:"trace_action_ordinal,omitempty"`
	DeadlineAt            int64                   `json:"deadline_at_ms,omitempty"`
	ConfiguredTimeoutMS   int64                   `json:"configured_timeout_ms,omitempty"`
	SessionDead           bool                    `json:"session_dead"`
	ConsecutiveTimeouts   int                     `json:"consecutive_timeouts"`
	InFlightCommands      int                     `json:"inflight_commands"`
	Cause                 error                   `json:"-"`
	Receipt               *ActionReceipt          `json:"action_receipt,omitempty"`
	// PartialResult carries field-level results collected before a multi-step
	// operation became uncertain. It is intentionally opaque here so each
	// action can preserve its normal typed result without changing recovery.
	PartialResult any `json:"partial_result,omitempty"`
}

func (e *ActionOutcomeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s %s: %s: %v", e.Action, e.Target, e.Outcome, e.Cause)
	}
	return fmt.Sprintf("%s %s: %s", e.Action, e.Target, e.Outcome)
}

func (e *ActionOutcomeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *OperationError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Phase + " failed"
	if e.Cause != "" {
		msg += ": " + e.Cause
	}
	if e.URL != "" {
		msg += " url=" + e.URL
	}
	if e.Detail != "" {
		msg += " detail=" + e.Detail
	}
	msg += " err=" + e.OriginalError
	return msg
}

// --- Semantic Form types ---

// FormFieldInfo describes a discovered form field with its label.
type FormFieldInfo struct {
	ContentTrust string   `json:"content_trust,omitempty"`
	Selector     string   `json:"selector"`
	Label        string   `json:"label"`
	Type         string   `json:"type"`
	Name         string   `json:"name,omitempty"`
	ID           string   `json:"id,omitempty"`
	Placeholder  string   `json:"placeholder,omitempty"`
	Required     bool     `json:"required,omitempty"`
	Options      []string `json:"options,omitempty"`
}

// FormDiscoveryResult is the structured response from form field discovery.
type FormDiscoveryResult struct {
	ContentTrust string          `json:"content_trust,omitempty"`
	FormSelector string          `json:"form_selector"`
	Action       string          `json:"action,omitempty"`
	Method       string          `json:"method,omitempty"`
	Fields       []FormFieldInfo `json:"fields"`
}

// SemanticFillResult is the structured response from semantic form filling.
type SemanticFillResult struct {
	OperationOrdinal int                   `json:"operation_ordinal,omitempty"`
	ContentTrust     string                `json:"content_trust,omitempty"`
	TargetID         string                `json:"target_id,omitempty"`
	Outcome          string                `json:"outcome,omitempty"`
	Evidence         string                `json:"evidence,omitempty"`
	RecoveryHint     string                `json:"recovery_hint,omitempty"`
	Receipt          *ActionReceipt        `json:"action_receipt,omitempty"`
	Fields           []SemanticFieldResult `json:"fields"`
	Success          bool                  `json:"success"`
}

// --- Visual Grounding types ---

// AnnotatedResult holds an annotated screenshot with element-label mapping.
//
// IMPORTANT: label numbers are scoped to a single annotated_screenshot call.
// They are recomputed every call by traversal order of visible interactive
// elements. If the DOM mutates between calls — Vue/React re-render, modal
// open, route change — label N may refer to a different element. Always
// pair click_label with the most recent annotated_screenshot in the same
// turn. For stable identity across mutations, prefer the selector field.
type AnnotatedResult struct {
	Image        []byte                `json:"-"` // PNG/JPEG image data
	ImageInfo    browse.ScreenshotInfo `json:"image_info"`
	ContentTrust string                `json:"content_trust,omitempty"`
	TargetID     string                `json:"target_id,omitempty"`
	FrameID      string                `json:"frame_id,omitempty"`
	Elements     []AnnotatedElement    `json:"elements"`
	Count        int                   `json:"count"`
	Total        int                   `json:"total"`
	Truncated    bool                  `json:"truncated"`
	// TargetID identifies the page target used to create handles. FrameID is
	// empty for the main-document annotation performed by this method.
	// LabelScope is always "per_call" — included so consumers can serialize
	// the contract explicitly instead of inferring it from documentation.
	LabelScope string `json:"label_scope"`
}

// AnnotatedElement maps a numbered label to an interactive element.
//
// NodeHandle is a stable identifier Scout stamps onto the DOM element
// as `data-scout-handle`. Unlike Label (recomputed per call), the
// handle survives reordering while the same node remains attached.
// Resolving a handle whose element was replaced or removed returns a
// structured stale-handle error.
type AnnotatedElement struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Label        int    `json:"label"`
	NodeHandle   string `json:"node_handle,omitempty"`
	Selector     string `json:"selector"`
	Tag          string `json:"tag"`
	Type         string `json:"type,omitempty"`
	Text         string `json:"text,omitempty"`
	Href         string `json:"href,omitempty"`
	X            int    `json:"x"`
	Y            int    `json:"y"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// SemanticFieldResult describes the outcome of filling one semantically-matched field.
//
// MatchConfidence exposes the matcher's score (0–100) so callers can flag
// low-confidence resolutions and decide whether to proceed. Anything below
// ~50 means the matcher fell back to a placeholder/type hint — verify
// before treating the fill as authoritative.
type SemanticFieldResult struct {
	ContentTrust      string `json:"content_trust,omitempty"`
	HumanName         string `json:"human_name"`
	Selector          string `json:"selector,omitempty"`
	Type              string `json:"type,omitempty"`
	Value             string `json:"value,omitempty"`              // value the caller asked us to set (string form)
	ValueObserved     string `json:"value_observed,omitempty"`     // value re-read after the mutation
	FrameworkReactive bool   `json:"framework_reactive,omitempty"` // true when set==observed (Vue/React state followed)
	MatchConfidence   int    `json:"match_confidence,omitempty"`   // 0–100, how confidently humanName mapped to this field
	Success           bool   `json:"success"`
	Outcome           string `json:"outcome,omitempty"`
	RecoveryHint      string `json:"recovery_hint,omitempty"`
	Warning           string `json:"warning,omitempty"`
	Error             string `json:"error,omitempty"`
}

// --- Natural Language Selection types ---

// PromptSelectResult is the structured response from SelectByPrompt.
type PromptSelectResult struct {
	TargetID     string            `json:"target_id,omitempty"`
	ContentTrust string            `json:"content_trust,omitempty"`
	Selector     string            `json:"selector"`
	Text         string            `json:"text"`
	Tag          string            `json:"tag"`
	Role         string            `json:"role,omitempty"`
	Confidence   float64           `json:"confidence"`
	Candidates   []PromptCandidate `json:"candidates,omitempty"`
}

// PromptCandidate describes one candidate element from natural language matching.
type PromptCandidate struct {
	ContentTrust string  `json:"content_trust,omitempty"`
	Selector     string  `json:"selector"`
	Text         string  `json:"text"`
	Score        float64 `json:"score"`
}

// BatchAction describes a single action within a batch operation.
type BatchAction struct {
	Action                string            `json:"action"`
	Selector              string            `json:"selector,omitempty"`
	Value                 string            `json:"value,omitempty"`
	Label                 int               `json:"label,omitempty"`
	Fields                map[string]string `json:"fields,omitempty"`
	WebMCPName            string            `json:"webmcp_name,omitempty"`
	WebMCPFrameID         string            `json:"webmcp_frame_id,omitempty"`
	WebMCPInput           map[string]any    `json:"webmcp_input,omitempty"`
	ExpectedText          string            `json:"expected_text,omitempty"`
	ExpectedURLContains   string            `json:"expected_url_contains,omitempty"`
	ExpectedVisibleTarget string            `json:"expected_visible_selector,omitempty"`
	ExpectedValueSelector string            `json:"expected_value_selector,omitempty"`
	ExpectedValue         string            `json:"expected_value,omitempty"`
	ExpectedCountSelector string            `json:"expected_count_selector,omitempty"`
	ExpectedCount         *int              `json:"expected_count,omitempty"`
}

// BatchActionResult describes the outcome of a single action within a batch.
type BatchActionResult struct {
	Index          int                   `json:"index"`
	TargetID       string                `json:"target_id,omitempty"`
	Action         string                `json:"action"`
	Target         string                `json:"target,omitempty"`
	Success        bool                  `json:"success"`
	Verified       bool                  `json:"verified,omitempty"`
	Outcome        string                `json:"outcome,omitempty"`
	Evidence       string                `json:"evidence,omitempty"`
	RecoveryHint   string                `json:"recovery_hint,omitempty"`
	Error          string                `json:"error,omitempty"`
	PartialResult  any                   `json:"partial_result,omitempty"`
	RecoveryRecord *ActionRecoveryRecord `json:"recovery_record,omitempty"`
	Receipt        *ActionReceipt        `json:"action_receipt,omitempty"`
}

// BatchResult is the structured response from ExecuteBatch.
// Succeeded counts completed steps, including steps whose application effect
// is explicitly unverified. Verified, Rejected, Unverified, and Unknown are
// mutually exclusive evidence states; rejected and unknown outcomes are
// subsets of Failed.
type BatchResult struct {
	ContentTrust string `json:"content_trust,omitempty"`
	Total        int    `json:"total"`
	Succeeded    int    `json:"succeeded"`
	Failed       int    `json:"failed"`
	Verified     int    `json:"verified"`
	Rejected     int    `json:"rejected"`
	Unverified   int    `json:"unverified"`
	Unknown      int    `json:"unknown"`
	// NextActionIndex is the first action that was not attempted. If the
	// preceding step is unknown, reconcile it before using this index.
	NextActionIndex     int    `json:"next_action_index"`
	StoppedOnUnknownAt  *int   `json:"stopped_on_unknown_at,omitempty"`
	StoppedOnContractAt *int   `json:"stopped_on_contract_at,omitempty"`
	StopReason          string `json:"stop_reason,omitempty"`
	// ResumeBlocked indicates that an unknown action must be reconciled before
	// the caller decides whether to retry it or continue with later actions.
	ResumeBlocked     bool                `json:"resume_blocked,omitempty"`
	Truncated         bool                `json:"truncated,omitempty"`
	TruncationReason  string              `json:"truncation_reason,omitempty"`
	Results           []BatchActionResult `json:"results"`
	FinalVerification *PageResult         `json:"final_verification,omitempty"`
}

// WebVitalsResult holds Core Web Vitals and related performance metrics.
type WebVitalsResult struct {
	ContentTrust     string  `json:"content_trust,omitempty"`
	LCP              float64 `json:"lcp_ms"`
	CLS              float64 `json:"cls"`
	INP              float64 `json:"inp_ms"`
	TTFB             float64 `json:"ttfb_ms"`
	DOMContentLoaded float64 `json:"dom_content_loaded_ms"`
	FirstPaint       float64 `json:"first_paint_ms"`
	LCPRating        string  `json:"lcp_rating"`
	CLSRating        string  `json:"cls_rating"`
	INPRating        string  `json:"inp_rating"`
	OverallRating    string  `json:"overall_rating"`
}

// --- Vision + DOM Hybrid types ---

// HybridResult holds a clean screenshot alongside bounding-box data for all
// interactive elements, enabling coordinate-based element selection.
type HybridResult struct {
	Screenshot   []byte                `json:"screenshot,omitempty"`
	ImageInfo    browse.ScreenshotInfo `json:"image_info"`
	ContentTrust string                `json:"content_trust,omitempty"`
	TargetID     string                `json:"target_id,omitempty"`
	FrameID      string                `json:"frame_id,omitempty"`
	Elements     []HybridElement       `json:"elements"`
	Count        int                   `json:"count"`
	Total        int                   `json:"total"`
	Truncated    bool                  `json:"truncated"`
	Width        int                   `json:"viewport_width"`
	Height       int                   `json:"viewport_height"`
}

// HybridElement describes an interactive element with its bounding box.
type HybridElement struct {
	Index    int     `json:"index"`
	Tag      string  `json:"tag"`
	Text     string  `json:"text"`
	Selector string  `json:"selector"`
	Role     string  `json:"role,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
}

// --- Trace types ---

// TraceEvent records a single action during a trace session.
type TraceEvent struct {
	Index                  int    `json:"index"`
	ContentTrust           string `json:"content_trust,omitempty"`
	ActionOrdinal          int    `json:"action_ordinal"`
	OperationOrdinal       int    `json:"operation_ordinal"`
	Action                 string `json:"action"`
	BrowserOwnership       string `json:"browser_ownership,omitempty"`
	TargetID               string `json:"target_id,omitempty"`
	Selector               string `json:"selector,omitempty"`
	Value                  string `json:"value,omitempty"`
	ActionValueRedacted    bool   `json:"action_value_redacted,omitempty"`
	URL                    string `json:"url,omitempty"`
	Outcome                string `json:"outcome,omitempty"`
	Verified               bool   `json:"verified,omitempty"`
	VerificationKnown      bool   `json:"verification_known,omitempty"`
	Evidence               string `json:"evidence,omitempty"`
	RecoveryHint           string `json:"recovery_hint,omitempty"`
	Timestamp              int64  `json:"timestamp_ms"`
	Duration               int64  `json:"duration_ms"`
	DeadlineAt             int64  `json:"deadline_at_ms,omitempty"`
	BudgetRemainingMS      int64  `json:"budget_remaining_ms,omitempty"`
	Error                  string `json:"error,omitempty"`
	BeforeImg              string `json:"before_screenshot,omitempty"`
	AfterImg               string `json:"after_screenshot,omitempty"`
	BeforeScreenshotStatus string `json:"before_screenshot_status,omitempty"`
	AfterScreenshotStatus  string `json:"after_screenshot_status,omitempty"`
}

// TraceURLPolicyDecision records a request-level URL policy verdict.
type TraceURLPolicyDecision struct {
	Index           int    `json:"index"`
	TargetID        string `json:"target_id,omitempty"`
	Timestamp       int64  `json:"timestamp_ms"`
	URL             string `json:"url"`
	Method          string `json:"method,omitempty"`
	ResourceType    string `json:"resource_type,omitempty"`
	Allowed         bool   `json:"allowed"`
	ReasonCode      string `json:"reason_code,omitempty"`
	PolicyID        string `json:"policy_id,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	ResolutionError string `json:"resolution_error,omitempty"`
}

// TraceRunMetadata describes configured execution constraints for a trace.
// ProxyConfigured records configuration only; it does not prove proxy enforcement.
type TraceRunMetadata struct {
	TraceID             string              `json:"trace_id"`
	ContentTrust        string              `json:"content_trust,omitempty"`
	SchemaVersion       int                 `json:"schema_version"`
	StartedAt           int64               `json:"started_at_ms"`
	ConfiguredTimeoutMS int64               `json:"configured_timeout_ms,omitempty"`
	OutputBudgetBytes   int64               `json:"output_budget_bytes,omitempty"`
	AllowedOrigins      []string            `json:"allowed_origins,omitempty"`
	AllowPrivateIPs     bool                `json:"allow_private_ips"`
	PolicyID            string              `json:"policy_id"`
	ProxyConfigured     bool                `json:"proxy_configured"`
	ProxyAppliedByScout bool                `json:"proxy_applied_by_scout"`
	RemoteBrowser       bool                `json:"remote_browser"`
	BrowserOwnership    string              `json:"browser_ownership,omitempty"`
	NetworkRedacted     bool                `json:"network_redacted"`
	EgressCoverage      TraceEgressCoverage `json:"egress_coverage"`
}

// TraceEgressCoverage describes which browser egress paths Scout observes and
// which enforcement claims are supported by the trace. It deliberately does
// not claim that an externally configured proxy enforced a policy.
type TraceEgressCoverage struct {
	PageTargetsCount        int      `json:"page_targets_count"`
	PageTargetsPolicyActive int      `json:"page_targets_policy_active"`
	PagePolicyDecisions     int      `json:"page_policy_decisions"`
	KnownUnobservedPaths    []string `json:"known_unobserved_paths"`
	ProxyEnforcement        string   `json:"proxy_enforcement"`
}

// ArtifactReference identifies a generated file for callers to open or transfer.
type ArtifactReference struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	MIMEType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
}

// TraceResult is the structured response after stopping a trace and writing the zip.
type TraceResult struct {
	TraceID      string             `json:"trace_id"`
	ContentTrust string             `json:"content_trust,omitempty"`
	Path         string             `json:"path"`
	EventCount   int                `json:"event_count"`
	Duration     int64              `json:"total_duration_ms"`
	Size         int64              `json:"file_size_bytes"`
	Artifact     *ArtifactReference `json:"artifact,omitempty"`
}
