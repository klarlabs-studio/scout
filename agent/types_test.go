package agent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestPageContentResultsExposeUntrustedProvenance(t *testing.T) {
	results := []any{
		Observation{ContentTrust: ContentTrustUntrustedPage, Text: "page text"},
		PageResult{ContentTrust: ContentTrustUntrustedPage, Title: "page title"},
		ContentResult{ContentTrust: ContentTrustUntrustedPage, Content: "page body"},
		ElementResult{ContentTrust: ContentTrustUntrustedPage, Text: "element text"},
		ExtractAllResult{ContentTrust: ContentTrustUntrustedPage, Items: []string{"item"}},
		TableResult{ContentTrust: ContentTrustUntrustedPage, Rows: [][]string{{"cell"}}},
		PromptSelectResult{ContentTrust: ContentTrustUntrustedPage, Text: "matched page text"},
		NetworkCapture{ContentTrust: ContentTrustUntrustedPage, ResponseBody: "page response"},
		TraceResult{ContentTrust: ContentTrustUntrustedPage, TraceID: "trace-1"},
		AnnotatedResult{ContentTrust: ContentTrustUntrustedPage, Elements: []AnnotatedElement{{Text: "page button"}}},
		HybridResult{ContentTrust: ContentTrustUntrustedPage, Elements: []HybridElement{{Text: "page control"}}},
		ActionReceipt{ContentTrust: ContentTrustUntrustedPage, Evidence: "page changed"},
		ActionOutcomeError{ContentTrust: ContentTrustUntrustedPage, Evidence: "page outcome unknown"},
		ActionRecoveryRecord{ContentTrust: ContentTrustUntrustedPage},
		BatchResult{ContentTrust: ContentTrustUntrustedPage, Results: []BatchActionResult{{Receipt: &ActionReceipt{ContentTrust: ContentTrustUntrustedPage}}}},
		TaskRunResult{ContentTrust: ContentTrustUntrustedPage, Batch: &BatchResult{ContentTrust: ContentTrustUntrustedPage}},
		PlaybookResult{ContentTrust: ContentTrustUntrustedPage, StepReceipts: []PlaybookStepReceipt{{ActionReceipt: ActionReceipt{ContentTrust: ContentTrustUntrustedPage}}}},
		AssertResult{ContentTrust: ContentTrustUntrustedPage, Snippet: "page instruction"},
		FormDiscoveryResult{ContentTrust: ContentTrustUntrustedPage, Fields: []FormFieldInfo{{Label: "page label"}}},
		FormResult{ContentTrust: ContentTrustUntrustedPage, Fields: []FieldResult{{ValueObserved: "page value"}}},
		SemanticFillResult{ContentTrust: ContentTrustUntrustedPage, Fields: []SemanticFieldResult{{HumanName: "page label"}}},
		DOMDiff{ContentTrust: ContentTrustUntrustedPage, Added: []DOMElement{{Text: "page text"}}},
		NavigationOutcome{ContentTrust: ContentTrustUntrustedPage, URL: "https://example.test"},
		SubmitOutcome{ContentTrust: ContentTrustUntrustedPage, AlertsVisible: []string{"page alert"}},
		SubmitFormResult{ContentTrust: ContentTrustUntrustedPage, Evidence: "page response"},
		NetworkSummary{ContentTrust: ContentTrustUntrustedPage, Failures: []NetworkCapture{{ContentTrust: ContentTrustUntrustedPage}}},
		DiagnosticsResult{ContentTrust: ContentTrustUntrustedPage, Messages: []ConsoleMessage{{ContentTrust: ContentTrustUntrustedPage}}},
		HistoryEntry{ContentTrust: ContentTrustUntrustedPage, Result: "page result"},
		DialogInfo{ContentTrust: ContentTrustUntrustedPage, Text: "page dialog"},
		CookieInfo{ContentTrust: ContentTrustUntrustedPage, Name: "session"},
		LinkInfo{ContentTrust: ContentTrustUntrustedPage, Text: "page link"},
		InputInfo{ContentTrust: ContentTrustUntrustedPage, Label: "page input"},
		ButtonInfo{ContentTrust: ContentTrustUntrustedPage, Text: "page button"},
		CookieSummary{ContentTrust: ContentTrustUntrustedPage, Names: []string{"session"}},
		FormFieldInfo{ContentTrust: ContentTrustUntrustedPage, Label: "page field"},
		FieldResult{ContentTrust: ContentTrustUntrustedPage, ValueObserved: "page value"},
		SemanticFieldResult{ContentTrust: ContentTrustUntrustedPage, HumanName: "page field"},
		PromptCandidate{ContentTrust: ContentTrustUntrustedPage, Text: "page candidate"},
		AnnotatedElement{ContentTrust: ContentTrustUntrustedPage, Text: "page element"},
		ClickTextOption{ContentTrust: ContentTrustUntrustedPage, Text: "page click candidate"},
		ClickTextResult{ContentTrust: ContentTrustUntrustedPage, URL: "https://example.test", Title: "page title"},
		Profile{ContentTrust: ContentTrustUntrustedPage, LocalStorage: map[string]string{"key": "page value"}},
		AuthWallResult{ContentTrust: ContentTrustUntrustedPage, Reason: "page signal"},
		AriaViolationReport{ContentTrust: ContentTrustUntrustedPage},
		PageReadiness{ContentTrust: ContentTrustUntrustedPage},
		WebVitalsResult{ContentTrust: ContentTrustUntrustedPage},
		TabInfo{ContentTrust: ContentTrustUntrustedPage, URL: "https://example.test"},
		SelectorSuggestion{ContentTrust: ContentTrustUntrustedPage, Text: "page text"},
	}
	for _, result := range results {
		t.Run(fmt.Sprintf("%T", result), func(t *testing.T) {
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got["content_trust"] != ContentTrustUntrustedPage {
				t.Fatalf("content_trust = %v, want %q", got["content_trust"], ContentTrustUntrustedPage)
			}
		})
	}
}

func TestVerificationStateFor(t *testing.T) {
	condition := &Postcondition{Text: "saved"}
	tests := []struct {
		name      string
		outcome   ActionOutcome
		verified  bool
		condition *Postcondition
		want      VerificationState
	}{
		{"verified", OutcomePostconditionVerified, true, condition, VerificationVerified},
		{"rejected", OutcomePostconditionFailed, false, condition, VerificationRejected},
		{"unknown", OutcomeUnknown, false, condition, VerificationUnknown},
		{"without check", OutcomeEventObserved, false, nil, VerificationUnverified},
		{"failed action", OutcomeActionFailed, false, nil, VerificationRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verificationStateFor(tt.outcome, tt.verified, tt.condition); got != tt.want {
				t.Fatalf("verificationStateFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActionReceipt_JSONIncludesVerificationState(t *testing.T) {
	receipt := ActionReceipt{SchemaVersion: 1, Action: "click", BrowserOwnership: "remote_cdp", Outcome: OutcomeUnknown, Verification: VerificationUnknown}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["verification_state"] != string(VerificationUnknown) {
		t.Fatalf("verification_state = %v, want %q", got["verification_state"], VerificationUnknown)
	}
	if got["browser_ownership"] != "remote_cdp" {
		t.Fatalf("browser_ownership = %v, want remote_cdp", got["browser_ownership"])
	}
}

func TestActionReceiptCapturesBrowserOwnership(t *testing.T) {
	for _, ownership := range []string{"scout_launched", "remote_cdp"} {
		t.Run(ownership, func(t *testing.T) {
			s := &Session{browserOwnership: ownership, runID: "run-owner"}
			receipt := s.newActionReceipt("navigate", "https://example.test", "target-1", string(OutcomePostconditionVerified), "URL matched", "", true, 1, nil)
			if receipt.BrowserOwnership != ownership {
				t.Fatalf("browser_ownership = %q, want %q", receipt.BrowserOwnership, ownership)
			}
		})
	}
}

func TestActionReceiptCarriesScopedPolicyEvidence(t *testing.T) {
	s := &Session{
		browserOwnership:    "remote_cdp",
		allowedOrigins:      []string{"https://example.test:443", "https://api.example.test"},
		allowPrivateIPs:     false,
		proxyServer:         "http://proxy.internal:8080",
		proxyAppliedByScout: false,
	}
	receipt := s.newActionReceipt("click", "#save", "target-1", string(OutcomePostconditionVerified), "clicked", "", true, 1, nil)
	if receipt.Policy == nil {
		t.Fatal("action receipt has no policy snapshot")
	}
	if receipt.Policy.ID != receipt.PolicyID || receipt.Policy.Enforcement != "session_page_policy" || receipt.Policy.RemoteHostPolicy != "unverified" {
		t.Fatalf("unexpected policy identity/scope: %+v", receipt.Policy)
	}
	if receipt.Policy.ProxyAppliedByScout || !receipt.Policy.ProxyConfigured {
		t.Fatalf("remote proxy configuration was misrepresented: %+v", receipt.Policy)
	}
	if len(receipt.Policy.AllowedOrigins) != 2 || receipt.Policy.AllowedOrigins[0] != "https://api.example.test:443" || receipt.Policy.AllowedOrigins[1] != "https://example.test:443" {
		t.Fatalf("origins were not normalized and sorted: %#v", receipt.Policy.AllowedOrigins)
	}
	if len(receipt.Policy.KnownUnobservedPaths) != 2 || receipt.Policy.KnownUnobservedPaths[0] != "service_worker_requests" || receipt.Policy.KnownUnobservedPaths[1] != "websocket_handshakes" {
		t.Fatalf("known unobserved paths = %#v", receipt.Policy.KnownUnobservedPaths)
	}
}

func TestActionReceiptSummarizesTargetPolicyDecisionsInActionWindow(t *testing.T) {
	start := time.UnixMilli(1_700_000_000_000)
	end := start.Add(time.Second)
	s := &Session{
		browserOwnership: "scout_launched", runID: "run-window", allowedOrigins: []string{"https://example.test"},
		tracing: true, lastActionName: "click", lastActionStartedAt: start, lastActionEndedAt: end,
		trace: &traceState{run: TraceRunMetadata{TraceID: "trace-window"}, policyDecisions: []TraceURLPolicyDecision{
			{Index: 0, TargetID: "target-1", Timestamp: start.Add(100 * time.Millisecond).UnixMilli(), Allowed: true, PolicyID: taskOriginPolicyID([]string{"https://example.test"}, false)},
			{Index: 1, TargetID: "target-1", Timestamp: start.Add(200 * time.Millisecond).UnixMilli(), Allowed: false, PolicyID: taskOriginPolicyID([]string{"https://example.test"}, false)},
			{Index: 2, TargetID: "target-other", Timestamp: start.Add(300 * time.Millisecond).UnixMilli(), Allowed: false, PolicyID: taskOriginPolicyID([]string{"https://example.test"}, false)},
			{Index: 3, TargetID: "target-1", Timestamp: end.Add(time.Millisecond).UnixMilli(), Allowed: true, PolicyID: taskOriginPolicyID([]string{"https://example.test"}, false)},
		}},
	}
	receipt := s.newActionReceipt("click", "#save", "target-1", string(OutcomePostconditionVerified), "saved", "", true, 1, nil)
	if receipt.Policy == nil || receipt.Policy.DecisionWindow == nil {
		t.Fatal("traced action receipt has no policy decision window")
	}
	window := receipt.Policy.DecisionWindow
	if receipt.Policy.EvidenceReference != "trace:trace-window#url-policy.json" || window.TargetID != "target-1" || window.Allowed != 1 || window.Blocked != 1 || len(window.DecisionIndices) != 2 || window.DecisionIndices[0] != 0 || window.DecisionIndices[1] != 1 {
		t.Fatalf("unexpected policy evidence window: reference=%q window=%+v", receipt.Policy.EvidenceReference, window)
	}
}

func TestElementResult_JSONIncludesFrameIdentity(t *testing.T) {
	want := ElementResult{Selector: "#message", TargetID: "target-7", FrameID: "frame-3", Text: "approved", ContentTrust: ContentTrustUntrustedPage}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ElementResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.TargetID != want.TargetID || got.FrameID != want.FrameID || got.Text != want.Text {
		t.Fatalf("element result lost evidence identity: got %+v, want %+v", got, want)
	}
}

func TestPageResult_JSON(t *testing.T) {
	tests := []struct {
		name string
		pr   PageResult
	}{
		{"basic", PageResult{URL: "https://example.com", Title: "Example"}},
		{"verified action", PageResult{URL: "https://example.com", Title: "Example", Action: "clicked", Target: "#submit", Verified: true, Evidence: "target resolved"}},
		{"unverified action", PageResult{URL: "https://example.com", Title: "Example", Action: "clicked", Target: "#submit", Evidence: "click dispatched"}},
		{"empty", PageResult{}},
		{"unicode", PageResult{URL: "https://example.com/日本語", Title: "日本語ページ"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.pr)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got PageResult
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got != tt.pr {
				t.Errorf("roundtrip: got %+v, want %+v", got, tt.pr)
			}
		})
	}
}

func TestClickTextResult_JSONIncludesOperationOrdinal(t *testing.T) {
	want := ClickTextResult{OperationOrdinal: 14, URL: "https://example.test/", Title: "Example", Matched: "#continue", MatchType: "button_text"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ClickTextResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.OperationOrdinal != want.OperationOrdinal || got.URL != want.URL || got.Title != want.Title || got.Matched != want.Matched || got.MatchType != want.MatchType {
		t.Fatalf("ClickTextResult roundtrip = %+v, want %+v", got, want)
	}
}

func TestPostconditionValidate(t *testing.T) {
	wantCount := 2
	wantValue := "saved"
	tests := []struct {
		name string
		pc   Postcondition
		want bool
	}{
		{"text", Postcondition{Text: "Saved"}, true},
		{"empty input value", Postcondition{ValueSelector: "#name", Value: ""}, true},
		{"exact value", Postcondition{ValueSelector: "#status", Value: wantValue}, true},
		{"exact count", Postcondition{CountSelector: ".row", Count: &wantCount}, true},
		{"negative count", Postcondition{CountSelector: ".row", Count: func() *int { n := -1; return &n }()}, false},
		{"missing count", Postcondition{CountSelector: ".row"}, false},
		{"multiple conditions", Postcondition{Text: "Saved", CountSelector: ".row", Count: &wantCount}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.pc.Validate() == nil
			if got != tt.want {
				t.Errorf("Validate() success = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestElementResult_JSON(t *testing.T) {
	tests := []struct {
		name string
		er   ElementResult
	}{
		{"with text", ElementResult{Selector: "#foo", Text: "hello", Action: "extracted"}},
		{"with value", ElementResult{Selector: "input", Value: "bar", ValueCommitted: "bar", Action: "typed", Verified: true, Evidence: "value reread"}},
		{"omitempty text", ElementResult{Selector: "p", Action: "extracted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.er)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got ElementResult
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got != tt.er {
				t.Errorf("roundtrip: got %+v, want %+v", got, tt.er)
			}
		})
	}
}

func TestExtractAllResult_JSON(t *testing.T) {
	r := ExtractAllResult{
		Selector:  "li",
		Count:     3,
		Total:     5,
		Truncated: true,
		Items:     []string{"a", "b", "c"},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ExtractAllResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Selector != r.Selector || got.Count != r.Count || got.Total != r.Total || got.Truncated != r.Truncated {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}
	if len(got.Items) != 3 {
		t.Errorf("items length: got %d, want 3", len(got.Items))
	}
}

func TestBoundedContentResult(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		limit     int
		total     int
		want      string
		truncated bool
	}{
		{"within limit", "hello", 5, 5, "hello", false},
		{"at limit with omitted source", "hello", 5, 6, "hello", true},
		{"over limit", "hello", 5, 11, "hello", true},
		{"unicode boundary", "a日", 2, 4, "a日", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := boundedContentResult(tt.input, tt.total, tt.limit)
			if got.Content != tt.want || got.Truncated != tt.truncated || got.MaxLength != tt.limit {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestTableResult_JSON(t *testing.T) {
	r := TableResult{
		Selector:  "table",
		Headers:   []string{"Name", "Age"},
		Rows:      [][]string{{"Alice", "30"}, {"Bob", "25"}},
		RowCount:  2,
		TotalRows: 2,
		ColCount:  2,
		Truncated: false,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got TableResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.RowCount != 2 || got.TotalRows != 2 || got.ColCount != 2 {
		t.Errorf("table counts: got %d/%d, want 2/2", got.RowCount, got.ColCount)
	}
	if len(got.Rows) != 2 || len(got.Headers) != 2 {
		t.Errorf("table sizes: rows=%d headers=%d", len(got.Rows), len(got.Headers))
	}
}

func TestFormResult_JSON(t *testing.T) {
	r := FormResult{
		Fields: []FieldResult{
			{Selector: "#email", Value: "test-user", Success: true},
			{Selector: "#missing", Error: "not found"},
		},
		Success: false,
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got FormResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Success != false {
		t.Error("expected Success=false")
	}
	if len(got.Fields) != 2 {
		t.Fatalf("fields: got %d, want 2", len(got.Fields))
	}
	if got.Fields[0].Success != true || got.Fields[1].Error != "not found" {
		t.Errorf("field roundtrip mismatch")
	}
}

func TestSubmitFormResult_JSONPreservesOutcomeEvidence(t *testing.T) {
	r := SubmitFormResult{
		Submitted:           true,
		Verified:            false,
		ResponseObserved:    true,
		ApplicationAccepted: false,
		Outcome:             "response_failed",
		StatusCode:          422,
		Evidence:            "observed POST response for /reject",
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["response_observed"] != true || got["application_accepted"] != false || got["outcome"] != "response_failed" || got["status_code"] != float64(422) {
		t.Fatalf("serialized submit result lost outcome evidence: %s", data)
	}
}

func TestFieldValueMismatch(t *testing.T) {
	if got := fieldValueMismatch(true); got != "" {
		t.Fatalf("verified value mismatch error = %q, want empty", got)
	}
	if got := fieldValueMismatch(false); got == "" {
		t.Fatal("unverified value should explain the mismatch")
	}
}

func TestObservation_JSON(t *testing.T) {
	obs := Observation{
		URL:         "https://example.com",
		Title:       "Example",
		Text:        "Hello World",
		Links:       []LinkInfo{{Text: "About", Href: "/about", Cost: "high"}},
		Inputs:      []InputInfo{{ID: "q", Type: "text", Placeholder: "Search"}},
		Buttons:     []ButtonInfo{{Text: "Submit", Type: "submit", Cost: "high"}},
		Interactive: 3,
		Meta:        map[string]string{"description": "test"},
		HasDialog:   true,
		DialogType:  "modal",
		DialogText:  "Confirm?",
	}
	data, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Observation
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.URL != obs.URL || got.Title != obs.Title || got.Interactive != 3 {
		t.Errorf("basic fields mismatch")
	}
	if !got.HasDialog || got.DialogType != "modal" || got.DialogText != "Confirm?" {
		t.Errorf("dialog fields mismatch")
	}
	if len(got.Links) != 1 || got.Links[0].Cost != "high" {
		t.Errorf("links mismatch")
	}
}

func TestDOMDiff_JSON(t *testing.T) {
	diff := DOMDiff{
		Added:          []DOMElement{{Tag: "div", ID: "new", Classes: "modal", Text: "Hello"}},
		Removed:        []DOMElement{{Tag: "span"}},
		Modified:       []DOMChange{{Tag: "input", Attribute: "class", OldValue: "a", NewValue: "b", ChangeType: "attribute"}},
		HasDiff:        true,
		Classification: "modal_appeared",
		Summary:        "Modal/dialog appeared: Hello",
	}
	data, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got DOMDiff
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.HasDiff || got.Classification != "modal_appeared" {
		t.Errorf("diff roundtrip mismatch")
	}
	if len(got.Added) != 1 || len(got.Removed) != 1 || len(got.Modified) != 1 {
		t.Errorf("diff element counts mismatch")
	}
}

func TestNetworkCapture_JSON(t *testing.T) {
	nc := NetworkCapture{
		URL:                   "https://api.example.com/data",
		Method:                "POST",
		Status:                201,
		MimeType:              "application/json",
		RequestHeaders:        map[string]string{"Content-Type": "application/json"},
		ResponseHeaders:       map[string]string{"X-Request-Id": "abc"},
		RequestBody:           `{"key":"value"}`,
		ResponseBody:          `{"id":1}`,
		RequestBodyTruncated:  false,
		ResponseBodyTruncated: true,
	}
	data, err := json.Marshal(nc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got NetworkCapture
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.URL != nc.URL || got.Method != nc.Method || got.Status != nc.Status {
		t.Errorf("basic fields mismatch")
	}
	if !got.ResponseBodyTruncated || got.RequestBodyTruncated {
		t.Errorf("truncated flags mismatch")
	}
}

func TestFormFieldInfo_JSON(t *testing.T) {
	f := FormFieldInfo{
		Selector:    "#email",
		Label:       "Email Address",
		Type:        "email",
		Name:        "email",
		ID:          "email",
		Placeholder: "you-example",
		Required:    true,
		Options:     []string{"opt1", "opt2"},
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got FormFieldInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Label != f.Label || got.Required != true || len(got.Options) != 2 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestBatchAction_JSON(t *testing.T) {
	ba := BatchAction{
		Action:   "fill_form_semantic",
		Selector: "form",
		Value:    "test",
		Fields:   map[string]string{"Email": "ab-user"},
	}
	data, err := json.Marshal(ba)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got BatchAction
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Action != "fill_form_semantic" || got.Fields["Email"] != "ab-user" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestBatchResult_JSON(t *testing.T) {
	br := BatchResult{
		Total:     3,
		Succeeded: 2,
		Failed:    1,
		Results: []BatchActionResult{
			{Index: 0, Action: "click", Success: true},
			{Index: 1, Action: "type", Success: true},
			{Index: 2, Action: "wait", Success: false, Error: "timeout"},
		},
	}
	data, err := json.Marshal(br)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got BatchResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Total != 3 || got.Succeeded != 2 || got.Failed != 1 {
		t.Errorf("counts mismatch")
	}
	if len(got.Results) != 3 || got.Results[2].Error != "timeout" {
		t.Errorf("results mismatch")
	}
}

func TestWebVitalsResult_JSON(t *testing.T) {
	wv := WebVitalsResult{
		LCP:              2500,
		CLS:              0.1,
		INP:              200,
		TTFB:             800,
		DOMContentLoaded: 1500,
		FirstPaint:       300,
		LCPRating:        "good",
		CLSRating:        "good",
		INPRating:        "good",
		OverallRating:    "good",
	}
	data, err := json.Marshal(wv)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got WebVitalsResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.LCPRating != "good" || got.OverallRating != "good" {
		t.Errorf("ratings mismatch")
	}
	// Verify JSON field names
	m := make(map[string]any)
	json.Unmarshal(data, &m)
	if _, ok := m["lcp_ms"]; !ok {
		t.Error("expected JSON key 'lcp_ms'")
	}
	if _, ok := m["cls"]; !ok {
		t.Error("expected JSON key 'cls'")
	}
}

func TestAnnotatedElement_JSON(t *testing.T) {
	ae := AnnotatedElement{
		Label:    1,
		Selector: "#btn",
		Tag:      "button",
		Type:     "submit",
		Text:     "Go",
		Href:     "",
		X:        10,
		Y:        20,
		Width:    100,
		Height:   30,
	}
	data, err := json.Marshal(ae)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got AnnotatedElement
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Label != 1 || got.X != 10 || got.Width != 100 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestAnnotatedResult_ImageOmitted(t *testing.T) {
	ar := AnnotatedResult{
		Image:    []byte("png data"),
		Elements: []AnnotatedElement{{Label: 1, Tag: "a"}},
		Count:    1,
	}
	data, err := json.Marshal(ar)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	m := make(map[string]any)
	json.Unmarshal(data, &m)
	if _, ok := m["image"]; ok {
		t.Error("Image field should be omitted from JSON (json:\"-\")")
	}
}

func TestTraceEvent_JSON(t *testing.T) {
	te := TraceEvent{
		Index:             0,
		ActionOrdinal:     3,
		Action:            "navigate",
		TargetID:          "page-1",
		URL:               "https://example.com",
		Outcome:           string(OutcomeNavigationCompleted),
		Verified:          true,
		VerificationKnown: true,
		Evidence:          "location matched requested URL",
		Timestamp:         1234567890,
		Duration:          500,
	}
	data, err := json.Marshal(te)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got TraceEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Action != "navigate" || got.ActionOrdinal != 3 || got.Timestamp != 1234567890 || got.TargetID != "page-1" || got.Outcome != string(OutcomeNavigationCompleted) || !got.Verified || !got.VerificationKnown || got.Evidence != "location matched requested URL" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestPromptSelectResult_JSON(t *testing.T) {
	psr := PromptSelectResult{
		Selector:   "#login",
		Text:       "Log In",
		Tag:        "button",
		Role:       "button",
		Confidence: 0.95,
		Candidates: []PromptCandidate{
			{Selector: "#login", Text: "Log In", Score: 95},
			{Selector: "#signup", Text: "Sign Up", Score: 60},
		},
	}
	data, err := json.Marshal(psr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got PromptSelectResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Confidence != 0.95 || len(got.Candidates) != 2 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestHybridResult_JSON(t *testing.T) {
	hr := HybridResult{
		Elements: []HybridElement{
			{Index: 0, Tag: "button", Text: "Go", Selector: "#go", X: 10, Y: 20, Width: 50, Height: 30},
		},
		Width:  1920,
		Height: 1080,
	}
	data, err := json.Marshal(hr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got HybridResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Width != 1920 || got.Height != 1080 || len(got.Elements) != 1 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestCookieDismissResult_JSON(t *testing.T) {
	cdr := CookieDismissResult{
		Found:    true,
		Method:   "selector",
		Selector: "#accept-cookies",
		Text:     "Accept All",
	}
	data, err := json.Marshal(cdr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got CookieDismissResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.Found || got.Method != "selector" || got.Text != "Accept All" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestCookieDismissResultPreservedOnUnknownOutcome(t *testing.T) {
	result := &CookieDismissResult{
		Found: true, Method: "selector", Selector: "#accept-cookies",
		Action: "dismiss_cookies", Target: "cookie consent banner", TargetID: "target-7", OperationOrdinal: 2,
	}
	receipt := &ActionReceipt{SchemaVersion: 1, Action: "dismiss_cookies", TargetID: "target-7", OperationOrdinal: 1, Outcome: OutcomeUnknown}
	outcomeErr := &ActionOutcomeError{
		Action: "dismiss_cookies", Target: result.Target, TargetID: result.TargetID,
		OperationOrdinal: 1, Outcome: OutcomeUnknown,
		Evidence:     "banner click was dispatched but settling was interrupted",
		RecoveryHint: "Inspect before retrying", Receipt: receipt,
	}
	preserveCookieDismissalUnknownOutcome(result, outcomeErr)
	outcomeErr.PartialResult = result

	partial, ok := outcomeErr.PartialResult.(*CookieDismissResult)
	if !ok {
		t.Fatalf("PartialResult type = %T, want *CookieDismissResult", outcomeErr.PartialResult)
	}
	if !partial.Found || partial.Method != "selector" || partial.Outcome != string(OutcomeUnknown) || partial.Verified || partial.Dismissed {
		t.Fatalf("partial dismissal result lost uncertainty evidence: %+v", partial)
	}
	if partial.OperationOrdinal != outcomeErr.OperationOrdinal || partial.Evidence != outcomeErr.Evidence || partial.RecoveryHint != outcomeErr.RecoveryHint {
		t.Fatalf("partial dismissal result does not match unknown outcome: result=%+v error=%+v", partial, outcomeErr)
	}
	if partial.Receipt != outcomeErr.Receipt {
		t.Fatal("partial dismissal result does not carry the authoritative unknown-outcome receipt")
	}
}

func TestPageReadiness_JSON(t *testing.T) {
	pr := PageReadiness{
		Score:         80,
		State:         "complete",
		PendingXHR:    0,
		PendingImages: 2,
		HasSkeleton:   false,
		HasSpinner:    true,
		Suggestions:   []string{"Wait for 2 images to load"},
	}
	data, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got PageReadiness
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Score != 80 || got.State != "complete" || !got.HasSpinner {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Suggestions) != 1 {
		t.Errorf("suggestions: got %d, want 1", len(got.Suggestions))
	}
}

func TestSemanticFillResult_JSON(t *testing.T) {
	sfr := SemanticFillResult{
		Fields: []SemanticFieldResult{
			{HumanName: "Email", Selector: "#email", Value: "ab-user", Success: true},
			{HumanName: "Phone", Error: "no matching field found"},
		},
		Success: false,
	}
	data, err := json.Marshal(sfr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got SemanticFillResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Success || len(got.Fields) != 2 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestSelectorSuggestion_JSON(t *testing.T) {
	ss := SelectorSuggestion{
		Selector: "#login-btn",
		Tag:      "button",
		Text:     "Log In",
		ID:       "login-btn",
		Classes:  "btn primary",
	}
	data, err := json.Marshal(ss)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got SelectorSuggestion
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Selector != "#login-btn" || got.Tag != "button" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}
