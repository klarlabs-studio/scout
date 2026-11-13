package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDiagnosticsResponseSnippetTruncatesAtUTF8Boundary(t *testing.T) {
	body := strings.Repeat("a", 499) + "€tail"
	snippet, truncated := networkFailureSnippet(body)
	if len(snippet) > 500 || !utf8.ValidString(snippet) {
		t.Fatalf("snippet is not valid UTF-8 within byte cap: bytes=%d text=%q", len(snippet), snippet)
	}
	if !truncated || len(snippet) != 499 {
		t.Fatalf("truncation metadata or boundary incorrect: bytes=%d truncated=%t", len(snippet), truncated)
	}
}

func TestConsoleMessage_JSON(t *testing.T) {
	cm := ConsoleMessage{
		Level:  "error",
		Text:   "Uncaught TypeError: null is not an object",
		Source: "https://example.com/app.js",
	}
	data, err := json.Marshal(cm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ConsoleMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Level != "error" || got.Text != cm.Text || got.Source != cm.Source {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestConsoleMessage_OmitEmptySource(t *testing.T) {
	cm := ConsoleMessage{Level: "warn", Text: "deprecated API"}
	data, err := json.Marshal(cm)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	m := make(map[string]any)
	json.Unmarshal(data, &m)
	if _, ok := m["source"]; ok {
		t.Error("source should be omitted when empty")
	}
}

func TestAuthWallResult_JSON(t *testing.T) {
	aw := AuthWallResult{
		Detected:   true,
		Type:       "login",
		Confidence: 75,
		Reason:     "Password field found",
		LoginURL:   "/auth/login",
	}
	data, err := json.Marshal(aw)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got AuthWallResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.Detected || got.Type != "login" || got.Confidence != 75 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestPageDiff_JSON(t *testing.T) {
	pd := PageDiff{
		URL1:      "https://example.com/a",
		URL2:      "https://example.com/b",
		Title1:    "Page A",
		Title2:    "Page B",
		OnlyIn1:   []string{"H1:Title A=Title A"},
		OnlyIn2:   []string{"H1:Title B=Title B"},
		Different: map[string][2]string{"price:item": {"$10", "$20"}},
	}
	data, err := json.Marshal(pd)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got PageDiff
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.URL1 != pd.URL1 || got.Title2 != "Page B" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Different) != 1 || got.Different["price:item"][0] != "$10" {
		t.Errorf("different mismatch: %+v", got.Different)
	}
}

func TestDialogInfo_JSON(t *testing.T) {
	di := DialogInfo{
		Found:    true,
		Type:     "modal",
		Title:    "Confirm Delete",
		Text:     "Are you sure?",
		Buttons:  []string{"Cancel", "Delete"},
		Inputs:   []InputInfo{{ID: "reason", Type: "text", Placeholder: "Reason"}},
		Selector: "#confirm-dialog",
	}
	data, err := json.Marshal(di)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got DialogInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.Found || got.Type != "modal" || len(got.Buttons) != 2 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Inputs) != 1 || got.Inputs[0].Placeholder != "Reason" {
		t.Errorf("inputs mismatch: %+v", got.Inputs)
	}
}

func TestDialogInfo_NotFound(t *testing.T) {
	di := DialogInfo{Found: false}
	data, err := json.Marshal(di)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got DialogInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Found {
		t.Error("expected Found=false")
	}
}

func TestExtractedPattern_JSON(t *testing.T) {
	ep := ExtractedPattern{
		Pattern: ".product-card",
		Count:   5,
		Fields:  []string{"title", "price", "link"},
		Items: []map[string]string{
			{"title": "Product A", "price": "$10", "link": "/a"},
			{"title": "Product B", "price": "$20", "link": "/b"},
		},
	}
	data, err := json.Marshal(ep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ExtractedPattern
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Pattern != ".product-card" || got.Count != 5 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Items) != 2 || got.Items[0]["title"] != "Product A" {
		t.Errorf("items mismatch")
	}
}

func TestFrameInfo_JSON(t *testing.T) {
	fi := FrameInfo{
		FrameID:  "frame-123",
		URL:      "https://example.com/widget",
		Name:     "widget",
		Selector: "iframe.widget",
	}
	data, err := json.Marshal(fi)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got FrameInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.FrameID != "frame-123" || got.Name != "widget" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestProfile_JSON(t *testing.T) {
	p := Profile{
		LocalStorage: map[string]string{"token": "abc123", "theme": "dark"},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Profile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.LocalStorage["token"] != "abc123" {
		t.Errorf("localStorage roundtrip mismatch: %+v", got.LocalStorage)
	}
}
