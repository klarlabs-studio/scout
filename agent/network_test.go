package agent

import "testing"

func TestExtractStringMap(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want map[string]string
	}{
		{
			name: "valid headers",
			m:    map[string]any{"headers": map[string]any{"Content-Type": "application/json", "Accept": "text/html"}},
			key:  "headers",
			want: map[string]string{"Content-Type": "application/json", "Accept": "text/html"},
		},
		{
			name: "missing key",
			m:    map[string]any{"other": "value"},
			key:  "headers",
			want: nil,
		},
		{
			name: "non-map value",
			m:    map[string]any{"headers": "not a map"},
			key:  "headers",
			want: nil,
		},
		{
			name: "mixed value types",
			m:    map[string]any{"headers": map[string]any{"good": "value", "bad": 123, "nil": nil}},
			key:  "headers",
			want: map[string]string{"good": "value"},
		},
		{
			name: "empty inner map",
			m:    map[string]any{"headers": map[string]any{}},
			key:  "headers",
			want: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStringMap(tt.m, tt.key)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("length mismatch: got %d, want %d", len(got), len(tt.want))
				return
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestMatchesNetworkPattern(t *testing.T) {
	tests := []struct {
		name    string
		network *networkState
		url     string
		want    bool
	}{
		{
			name:    "nil network",
			network: nil,
			url:     "https://example.com",
			want:    false,
		},
		{
			name:    "disabled",
			network: &networkState{enabled: false},
			url:     "https://example.com",
			want:    false,
		},
		{
			name:    "enabled no patterns matches all",
			network: &networkState{enabled: true, patterns: nil},
			url:     "https://example.com/api",
			want:    true,
		},
		{
			name:    "enabled empty patterns matches all",
			network: &networkState{enabled: true, patterns: []string{}},
			url:     "https://example.com/api",
			want:    true,
		},
		{
			name:    "pattern match",
			network: &networkState{enabled: true, patterns: []string{"/api/"}},
			url:     "https://example.com/api/users",
			want:    true,
		},
		{
			name:    "pattern no match",
			network: &networkState{enabled: true, patterns: []string{"/api/"}},
			url:     "https://example.com/static/img.png",
			want:    false,
		},
		{
			name:    "multiple patterns one match",
			network: &networkState{enabled: true, patterns: []string{"/api/", "/graphql"}},
			url:     "https://example.com/graphql",
			want:    true,
		},
		{
			name:    "multiple patterns no match",
			network: &networkState{enabled: true, patterns: []string{"/api/", "/graphql"}},
			url:     "https://example.com/static/css",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{network: tt.network}
			got := s.matchesNetworkPattern(tt.url)
			if got != tt.want {
				t.Errorf("matchesNetworkPattern(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestDefaultMaxBodySize(t *testing.T) {
	if defaultMaxBodySize != 32*1024 {
		t.Errorf("defaultMaxBodySize: got %d, want %d", defaultMaxBodySize, 32*1024)
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	if !matchesAnyPattern("https://example.com", nil) {
		t.Fatal("expected nil patterns to match")
	}
	if !matchesAnyPattern("https://example.com/api/users", []string{"/api/"}) {
		t.Fatal("expected /api/ to match")
	}
	if matchesAnyPattern("https://example.com/static", []string{"/api/"}) {
		t.Fatal("expected /api/ to not match")
	}
}

func TestDetachNetworkObserversLocked(t *testing.T) {
	called := 0
	s := &Session{
		network: &networkState{
			enabled:            true,
			observersInstalled: true,
			unsub: []func(){
				func() { called++ },
				func() { called++ },
			},
		},
	}

	s.detachNetworkObserversLocked()

	if called != 2 {
		t.Fatalf("unsubscribe calls: got %d, want 2", called)
	}
	if s.network.observersInstalled {
		t.Fatal("expected observersInstalled to be false")
	}
	if len(s.network.unsub) != 0 {
		t.Fatalf("expected unsub callbacks to be cleared, got %d", len(s.network.unsub))
	}
}
