package agent

// PageResult is the structured response after a navigation or page-level action.
type PageResult struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// ElementResult is the structured response for single-element operations.
type ElementResult struct {
	Selector string `json:"selector"`
	Text     string `json:"text,omitempty"`
	Value    string `json:"value,omitempty"`
	Action   string `json:"action"`
}

// ExtractAllResult is the structured response for multi-element extraction.
type ExtractAllResult struct {
	Selector  string   `json:"selector"`
	Count     int      `json:"count"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated,omitempty"`
	Items     []string `json:"items"`
}

// TableResult is the structured response for table extraction.
type TableResult struct {
	Selector  string     `json:"selector"`
	Headers   []string   `json:"headers"`
	Rows      [][]string `json:"rows"`
	RowCount  int        `json:"row_count"`
	ColCount  int        `json:"col_count"`
	Truncated bool       `json:"truncated,omitempty"`
}

// FormResult is the structured response for form filling.
type FormResult struct {
	Fields  []FieldResult `json:"fields"`
	Success bool          `json:"success"`
}

// FieldResult describes the outcome of filling a single field.
type FieldResult struct {
	Selector string `json:"selector"`
	Value    string `json:"value,omitempty"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
}

// Observation is a structured snapshot of the visible page for agent context.
type Observation struct {
	URL              string            `json:"url"`
	Title            string            `json:"title"`
	Text             string            `json:"text"`
	Links            []LinkInfo        `json:"links,omitempty"`
	Inputs           []InputInfo       `json:"inputs,omitempty"`
	Buttons          []ButtonInfo      `json:"buttons,omitempty"`
	Interactive      int               `json:"interactive_elements"`
	Meta             map[string]string `json:"meta,omitempty"`
	HasDialog        bool              `json:"has_dialog,omitempty"`
	DialogType       string            `json:"dialog_type,omitempty"` // dialog, modal, overlay
	DialogText       string            `json:"dialog_text,omitempty"`
	ActiveTab        string            `json:"active_tab,omitempty"`        // text of [role=tab][aria-selected=true]
	ActiveTabID      string            `json:"active_tab_id,omitempty"`     // id/data-tab-id of selected tab
	ActiveNavigation []string          `json:"active_navigation,omitempty"` // breadcrumb of [aria-current=page] / .active links + page H1
	Cookies          *CookieSummary    `json:"cookies,omitempty"`
}

// LinkInfo describes a link on the page.
type LinkInfo struct {
	Text string `json:"text"`
	Href string `json:"href"`
	Cost string `json:"cost,omitempty"` // "high" (navigation), "medium" (ajax), "low" (anchor)
}

// CookieSummary is a compact view of cookies for the current page (no values).
type CookieSummary struct {
	Count int      `json:"count"`
	Names []string `json:"names,omitempty"`
}

// InputInfo describes an input element.
type InputInfo struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Type        string `json:"type"`
	Value       string `json:"value,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Checked     *bool  `json:"checked,omitempty"` // populated for checkbox/radio inputs only
	Label       string `json:"label,omitempty"`   // resolved label/aria-label, helpful for checkbox/radio identification
}

// ButtonInfo describes a button element.
type ButtonInfo struct {
	Text string `json:"text"`
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
	Cost string `json:"cost,omitempty"` // "high" (submit/navigation), "medium" (action), "low" (toggle)
}

// --- DOM Diff types ---

// DOMDiff represents changes between two Observe() calls.
type DOMDiff struct {
	Added          []DOMElement `json:"added,omitempty"`
	Removed        []DOMElement `json:"removed,omitempty"`
	Modified       []DOMChange  `json:"modified,omitempty"`
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
	URL                   string            `json:"url"`
	Method                string            `json:"method"`
	Status                int               `json:"status"`
	MimeType              string            `json:"mime_type,omitempty"`
	RequestHeaders        map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders       map[string]string `json:"response_headers,omitempty"`
	RequestBody           string            `json:"request_body,omitempty"`
	ResponseBody          string            `json:"response_body,omitempty"`
	RequestBodyTruncated  bool              `json:"request_body_truncated,omitempty"`
	ResponseBodyTruncated bool              `json:"response_body_truncated,omitempty"`
	FromHistory           bool              `json:"from_history,omitempty"`
}

// SessionStatus provides health and diagnostics for the active browser session.
type SessionStatus struct {
	BrowserAlive        bool   `json:"browser_alive"`
	SessionAlive        bool   `json:"session_alive"`
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
	URL           string `json:"url,omitempty"`
	StatusCode    int    `json:"status_code,omitempty"`
	Detail        string `json:"detail,omitempty"`
	OriginalError string `json:"original_error"`
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
	Selector    string   `json:"selector"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Name        string   `json:"name,omitempty"`
	ID          string   `json:"id,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// FormDiscoveryResult is the structured response from form field discovery.
type FormDiscoveryResult struct {
	FormSelector string          `json:"form_selector"`
	Action       string          `json:"action,omitempty"`
	Method       string          `json:"method,omitempty"`
	Fields       []FormFieldInfo `json:"fields"`
}

// SemanticFillResult is the structured response from semantic form filling.
type SemanticFillResult struct {
	Fields  []SemanticFieldResult `json:"fields"`
	Success bool                  `json:"success"`
}

// --- Visual Grounding types ---

// AnnotatedResult holds an annotated screenshot with element-label mapping.
type AnnotatedResult struct {
	Image    []byte             `json:"-"` // PNG/JPEG image data
	Elements []AnnotatedElement `json:"elements"`
	Count    int                `json:"count"`
}

// AnnotatedElement maps a numbered label to an interactive element.
type AnnotatedElement struct {
	Label    int    `json:"label"`
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	Type     string `json:"type,omitempty"`
	Text     string `json:"text,omitempty"`
	Href     string `json:"href,omitempty"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// SemanticFieldResult describes the outcome of filling one semantically-matched field.
type SemanticFieldResult struct {
	HumanName         string `json:"human_name"`
	Selector          string `json:"selector,omitempty"`
	Type              string `json:"type,omitempty"`
	Value             string `json:"value,omitempty"`              // value the caller asked us to set (string form)
	ValueObserved     string `json:"value_observed,omitempty"`     // value re-read after the mutation
	FrameworkReactive bool   `json:"framework_reactive,omitempty"` // true when set==observed (Vue/React state followed)
	Success           bool   `json:"success"`
	Warning           string `json:"warning,omitempty"`
	Error             string `json:"error,omitempty"`
}

// --- Natural Language Selection types ---

// PromptSelectResult is the structured response from SelectByPrompt.
type PromptSelectResult struct {
	Selector   string            `json:"selector"`
	Text       string            `json:"text"`
	Tag        string            `json:"tag"`
	Role       string            `json:"role,omitempty"`
	Confidence float64           `json:"confidence"`
	Candidates []PromptCandidate `json:"candidates,omitempty"`
}

// PromptCandidate describes one candidate element from natural language matching.
type PromptCandidate struct {
	Selector string  `json:"selector"`
	Text     string  `json:"text"`
	Score    float64 `json:"score"`
}

// BatchAction describes a single action within a batch operation.
type BatchAction struct {
	Action   string            `json:"action"`
	Selector string            `json:"selector,omitempty"`
	Value    string            `json:"value,omitempty"`
	Label    int               `json:"label,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}

// BatchActionResult describes the outcome of a single action within a batch.
type BatchActionResult struct {
	Index   int    `json:"index"`
	Action  string `json:"action"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// BatchResult is the structured response from ExecuteBatch.
type BatchResult struct {
	Total     int                 `json:"total"`
	Succeeded int                 `json:"succeeded"`
	Failed    int                 `json:"failed"`
	Results   []BatchActionResult `json:"results"`
}

// WebVitalsResult holds Core Web Vitals and related performance metrics.
type WebVitalsResult struct {
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
	Screenshot []byte          `json:"screenshot,omitempty"`
	Elements   []HybridElement `json:"elements"`
	Width      int             `json:"viewport_width"`
	Height     int             `json:"viewport_height"`
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
	Index     int    `json:"index"`
	Action    string `json:"action"`
	Selector  string `json:"selector,omitempty"`
	Value     string `json:"value,omitempty"`
	URL       string `json:"url,omitempty"`
	Timestamp int64  `json:"timestamp_ms"`
	Duration  int64  `json:"duration_ms"`
	Error     string `json:"error,omitempty"`
	BeforeImg string `json:"before_screenshot,omitempty"`
	AfterImg  string `json:"after_screenshot,omitempty"`
}

// TraceResult is the structured response after stopping a trace and writing the zip.
type TraceResult struct {
	Path       string `json:"path"`
	EventCount int    `json:"event_count"`
	Duration   int64  `json:"total_duration_ms"`
	Size       int64  `json:"file_size_bytes"`
}
