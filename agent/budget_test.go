package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"empty string", "", 0},
		{"single char", "a", 1},
		{"four chars", "abcd", 1},
		{"five chars", "abcde", 2},
		{"eight chars", "abcdefgh", 2},
		{"twelve chars", "abcdefghijkl", 3},
		{"short sentence", "Hello, World!", 4},
		{"longer text", "The quick brown fox jumps over the lazy dog", 11},
		{"one byte", "x", 1},
		{"three bytes", "xyz", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.input)
			if got != tt.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d (len=%d)", tt.input, got, tt.want, len(tt.input))
			}
		})
	}
}

func TestMin(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 0, 0},
		{-1, 1, -1},
		{100, 100, 100},
		{-5, -3, -5},
	}
	for _, tt := range tests {
		got := min(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("min(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestBudgetedObservationLimitsPreserveZeroAllowance(t *testing.T) {
	opts := DefaultContentOptions()
	got := budgetedObservationLimits(0, opts)
	if got.links != 0 || got.inputs != 0 || got.buttons != 0 {
		t.Fatalf("zero interactive budget returned default limits: %+v", got)
	}

	got = budgetedObservationLimits(30, opts)
	if got.links != 1 || got.inputs != 0 || got.buttons != 0 {
		t.Fatalf("unexpected limits for one element allowance: %+v", got)
	}
}

func TestObservationTokenEstimateUsesSerializedPayload(t *testing.T) {
	obs := &Observation{URL: "https://example.test", Title: "Products", Text: "hello", Meta: map[string]string{"description": "A useful page"}}
	encoded, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	want := (len(encoded) + 3) / 4
	if got := observationTokenEstimate(obs); got != want {
		t.Fatalf("observationTokenEstimate() = %d, want %d", got, want)
	}
}

func TestTrimObservationForBudgetRemovesVerboseMetadataFirst(t *testing.T) {
	obs := &Observation{URL: "https://example.test", Title: "Products", Text: "useful page content", Meta: map[string]string{"description": "large metadata value"}, Links: []LinkInfo{{Text: "Next", Href: "/next"}}}
	if !trimObservationForBudget(obs) {
		t.Fatal("trimObservationForBudget() = false, want true")
	}
	if obs.Meta != nil || len(obs.Links) != 1 || obs.Text != "useful page content" {
		t.Fatalf("trim removed wrong content: %+v", obs)
	}
}

func TestBudgetedObservationTrimmingConvergesOrReportsFixedOverage(t *testing.T) {
	obs := &Observation{
		URL: "https://example.test", TargetID: "target-1", FrameID: "frame-1",
		Title: strings.Repeat("title", 40), Text: strings.Repeat("content", 200),
		Meta:       map[string]string{"description": strings.Repeat("metadata", 100)},
		Links:      []LinkInfo{{Text: "Next", Href: "/next"}, {Text: "Previous", Href: "/previous"}},
		LinksTotal: 2, Truncated: true,
	}
	obs.Budget = 200
	obs.BudgetTruncated = true
	obs.BudgetUsed = stableObservationTokenEstimate(obs)
	for obs.BudgetUsed > obs.Budget && trimObservationForBudget(obs) {
		obs.BudgetTruncated = true
		obs.Truncated = true
		obs.BudgetUsed = stableObservationTokenEstimate(obs)
	}
	if obs.BudgetUsed != observationTokenEstimate(obs) {
		t.Fatalf("budget_used = %d, serialized estimate = %d", obs.BudgetUsed, observationTokenEstimate(obs))
	}
	if obs.BudgetUsed > obs.Budget {
		t.Fatalf("fixture should fit after trimming: budget_used=%d budget=%d", obs.BudgetUsed, obs.Budget)
	}
	if !obs.BudgetTruncated || !obs.Truncated || obs.TargetID != "target-1" || obs.LinksTotal != 2 {
		t.Fatalf("trimming lost truncation or identity/count evidence: %+v", obs)
	}

	tiny := &Observation{URL: "https://example.test", TargetID: "target-1", LinksTotal: 10, Truncated: true, Budget: 1}
	tiny.BudgetTruncated = true
	tiny.BudgetUsed = stableObservationTokenEstimate(tiny)
	if tiny.BudgetUsed <= tiny.Budget {
		t.Fatal("fixed identity/count metadata should exceed the one-token fixture budget")
	}
	tiny.BudgetTruncated = false
	tiny.BudgetUsed = stableObservationTokenEstimate(tiny)
	for tiny.BudgetUsed > tiny.Budget {
		tiny.BudgetTruncated = true
		tiny.Truncated = true
		tiny.BudgetUsed = stableObservationTokenEstimate(tiny)
		if tiny.BudgetUsed <= tiny.Budget || !trimObservationForBudget(tiny) {
			break
		}
		tiny.BudgetUsed = stableObservationTokenEstimate(tiny)
	}
	if tiny.BudgetUsed != observationTokenEstimate(tiny) || tiny.BudgetUsed <= tiny.Budget || !tiny.BudgetTruncated {
		t.Fatalf("irreducible overage not reported consistently: used=%d actual=%d budget=%d truncated=%t", tiny.BudgetUsed, observationTokenEstimate(tiny), tiny.Budget, tiny.BudgetTruncated)
	}
}

func TestObservationBudgetTruncationDistinguishesConfiguredCaps(t *testing.T) {
	configured := DefaultContentOptions()
	obs := &Observation{LinksTotal: 30, InputsTotal: 5, ButtonsTotal: 3, Links: make([]LinkInfo, 25), Inputs: make([]InputInfo, 5), Buttons: make([]ButtonInfo, 3)}
	defaultLimits := observationLimits{links: 25, inputs: 20, buttons: 15}
	if observationBudgetLimitsOmitted(obs, defaultLimits, configured) {
		t.Fatal("normal configured link cap was incorrectly reported as budget truncation")
	}
	budgetLimits := observationLimits{links: 2, inputs: 2, buttons: 1}
	obs.Links = obs.Links[:2]
	obs.Inputs = obs.Inputs[:2]
	obs.Buttons = obs.Buttons[:1]
	if !observationBudgetLimitsOmitted(obs, budgetLimits, configured) {
		t.Fatal("budget-reduced element limits were not reported as budget truncation")
	}
}

func TestDefaultContentOptions(t *testing.T) {
	opts := DefaultContentOptions()
	if opts.MaxLength != 4000 {
		t.Errorf("MaxLength: got %d, want 4000", opts.MaxLength)
	}
	if opts.MaxLinks != 25 {
		t.Errorf("MaxLinks: got %d, want 25", opts.MaxLinks)
	}
	if opts.MaxInputs != 20 {
		t.Errorf("MaxInputs: got %d, want 20", opts.MaxInputs)
	}
	if opts.MaxButtons != 15 {
		t.Errorf("MaxButtons: got %d, want 15", opts.MaxButtons)
	}
	if opts.MaxItems != 50 {
		t.Errorf("MaxItems: got %d, want 50", opts.MaxItems)
	}
	if opts.MaxRows != 100 {
		t.Errorf("MaxRows: got %d, want 100", opts.MaxRows)
	}
	if opts.MaxScreenshotBytes != 5*1024*1024 {
		t.Errorf("MaxScreenshotBytes: got %d, want %d", opts.MaxScreenshotBytes, 5*1024*1024)
	}
}

func TestTruncateUTF8BytesPreservesValidUTF8(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		max  int
		want string
	}{
		{name: "ascii", text: "abcdef", max: 3, want: "abc"},
		{name: "inside multibyte rune", text: "a€b", max: 2, want: "a"},
		{name: "after multibyte rune", text: "a€b", max: 4, want: "a€"},
		{name: "zero limit", text: "a€b", max: 0, want: ""},
		{name: "already within limit", text: "a€b", max: 5, want: "a€b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateUTF8Bytes(tc.text, tc.max)
			if got != tc.want {
				t.Fatalf("truncateUTF8Bytes(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) || len(got) > tc.max {
				t.Fatalf("result is invalid or exceeds byte limit: %q (%d bytes)", got, len(got))
			}
		})
	}

	text := strings.Repeat("€", 20)
	got := truncateUTF8Bytes(text, 10)
	if got != strings.Repeat("€", 3) || !utf8.ValidString(got) {
		t.Fatalf("unexpected multibyte truncation: %q", got)
	}
}
