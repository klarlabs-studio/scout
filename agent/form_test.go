package agent

import "testing"

func TestMatchFormField(t *testing.T) {
	fields := []FormFieldInfo{
		{Selector: "#email", Label: "Email Address", Type: "email", Name: "email", ID: "email", Placeholder: "you-example"},
		{Selector: "#password", Label: "Password", Type: "password", Name: "password", ID: "password", Placeholder: "Enter password"},
		{Selector: "#first-name", Label: "First Name", Type: "text", Name: "first_name", ID: "first-name", Placeholder: "John"},
		{Selector: "#phone", Label: "Phone Number", Type: "tel", Name: "phone", ID: "phone"},
		{Selector: "#country", Label: "Country", Type: "select", Name: "country", ID: "country", Options: []string{"USA", "UK", "DE"}},
	}

	tests := []struct {
		name      string
		humanName string
		wantSel   string
		wantNil   bool
	}{
		{"exact label match", "Email Address", "#email", false},
		{"exact label case insensitive", "email address", "#email", false},
		{"exact label match password", "Password", "#password", false},
		{"partial label match", "Email", "#email", false},
		{"exact name match", "email", "#email", false},
		{"exact ID match", "first-name", "#first-name", false},
		{"partial name match", "first", "#first-name", false},
		{"placeholder match", "Enter password", "#password", false},
		{"type match", "email", "#email", false},
		{"type match tel", "tel", "#phone", false},
		{"country by label", "Country", "#country", false},
		{"no match", "nonexistent", "", true},
		{"partial ID match", "phone", "#phone", false},
		{"first name natural", "First Name", "#first-name", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchFormField(tt.humanName, fields)
			if tt.wantNil {
				if got != nil {
					t.Errorf("MatchFormField(%q) = %+v, want nil", tt.humanName, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("MatchFormField(%q) = nil, want selector %q", tt.humanName, tt.wantSel)
			}
			if got.Selector != tt.wantSel {
				t.Errorf("MatchFormField(%q).Selector = %q, want %q", tt.humanName, got.Selector, tt.wantSel)
			}
		})
	}
}

func TestMatchFormField_EmptyFields(t *testing.T) {
	got := MatchFormField("Email", nil)
	if got != nil {
		t.Errorf("expected nil for empty fields, got %+v", got)
	}

	got = MatchFormField("Email", []FormFieldInfo{})
	if got != nil {
		t.Errorf("expected nil for empty slice, got %+v", got)
	}
}

func TestMatchFormField_PriorityOrder(t *testing.T) {
	fields := []FormFieldInfo{
		{Selector: "#by-placeholder", Label: "Other", Type: "text", Placeholder: "email here"},
		{Selector: "#by-label", Label: "Email", Type: "text"},
	}
	got := MatchFormField("Email", fields)
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.Selector != "#by-label" {
		t.Errorf("expected exact label match (#by-label), got %q", got.Selector)
	}
}

func TestMatchFormField_NameExactBeatsLabelPartial(t *testing.T) {
	fields := []FormFieldInfo{
		{Selector: "#by-name", Label: "Identifier", Type: "text", Name: "email"},
		{Selector: "#by-label", Label: "Email Notification Settings", Type: "text"},
	}
	got := MatchFormField("email", fields)
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.Selector != "#by-name" {
		t.Errorf("expected exact name match (#by-name), got %q", got.Selector)
	}
}

func TestMatchFormFieldWithScore(t *testing.T) {
	fields := []FormFieldInfo{
		{Selector: "#email", Label: "Email Address", Type: "email", Name: "email", ID: "email", Placeholder: "you@example.com"},
		{Selector: "#password", Label: "Password", Type: "password", Name: "password", ID: "password"},
		{Selector: "#ph-only", Label: "", Type: "text", Placeholder: "looks like fax"},
		{Selector: "#type-only", Label: "", Type: "tel"},
	}

	cases := []struct {
		name      string
		humanName string
		wantSel   string
		minScore  int
		maxScore  int
	}{
		{"exact label", "Email Address", "#email", 100, 100},
		{"name + label exact", "password", "#password", 100, 100},
		{"name exact + partial label", "Email", "#email", 90, 90},
		{"placeholder only (low confidence)", "fax", "#ph-only", 50, 50},
		{"type hint only (low confidence)", "tel", "#type-only", 40, 40},
		{"no match", "nonexistent", "", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, score := MatchFormFieldWithScore(tc.humanName, fields)
			if tc.wantSel == "" {
				if got != nil {
					t.Fatalf("expected nil, got %+v (score %d)", got, score)
				}
				if score != 0 {
					t.Errorf("expected zero score, got %d", score)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected match for %q, got nil", tc.humanName)
			}
			if got.Selector != tc.wantSel {
				t.Errorf("selector = %q, want %q", got.Selector, tc.wantSel)
			}
			if score < tc.minScore || score > tc.maxScore {
				t.Errorf("score = %d, want [%d,%d]", score, tc.minScore, tc.maxScore)
			}
		})
	}
}

func TestMax(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 2},
		{2, 1, 2},
		{0, 0, 0},
		{-1, 1, 1},
		{100, 100, 100},
	}
	for _, tt := range tests {
		got := max(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("max(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
