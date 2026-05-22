package agent

import "testing"

func TestStatusBucket(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{0, "0"},
		{100, "1xx"},
		{200, "2xx"},
		{204, "2xx"},
		{301, "3xx"},
		{399, "3xx"},
		{400, "4xx"},
		{404, "4xx"},
		{499, "4xx"},
		{500, "5xx"},
		{599, "5xx"},
		{999, "5xx"},
	}
	for _, c := range cases {
		if got := statusBucket(c.status); got != c.want {
			t.Errorf("statusBucket(%d) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestNetworkSummary_emptyNoCapture(t *testing.T) {
	s := &Session{}
	out := s.NetworkSummary("")
	if out.Total != 0 {
		t.Errorf("total = %d, want 0", out.Total)
	}
	if out.CaptureEnabled {
		t.Error("expected capture_enabled=false on fresh session")
	}
	if out.Hint == "" {
		t.Error("expected hint on empty no-capture session")
	}
}

func TestNetworkSummary_buckets(t *testing.T) {
	s := &Session{
		network: &networkState{
			enabled: true,
			requests: []NetworkCapture{
				{URL: "/a", Status: 200},
				{URL: "/b", Status: 204},
				{URL: "/c", Status: 301},
				{URL: "/d", Status: 404},
				{URL: "/e", Status: 500},
				{URL: "/f", Status: 0},
			},
			pending: map[string]*NetworkCapture{
				"req-1": {URL: "/in-flight"},
			},
		},
	}
	out := s.NetworkSummary("")
	if out.Total != 6 {
		t.Errorf("total = %d, want 6", out.Total)
	}
	if out.Pending != 1 {
		t.Errorf("pending = %d, want 1", out.Pending)
	}
	if !out.CaptureEnabled {
		t.Error("capture_enabled = false, want true")
	}
	wantBuckets := map[string]int{"2xx": 2, "3xx": 1, "4xx": 1, "5xx": 1, "0": 1}
	for k, want := range wantBuckets {
		if out.ByStatus[k] != want {
			t.Errorf("by_status[%q] = %d, want %d", k, out.ByStatus[k], want)
		}
	}
	if len(out.Failures) != 2 {
		t.Errorf("failures = %d, want 2 (one 4xx + one 5xx)", len(out.Failures))
	}
}

func TestNetworkSummary_patternFilter(t *testing.T) {
	s := &Session{
		network: &networkState{
			enabled: true,
			requests: []NetworkCapture{
				{URL: "/api/auth/signin", Status: 401},
				{URL: "/static/icon.png", Status: 200},
				{URL: "/api/auth/signup", Status: 201},
			},
		},
	}
	out := s.NetworkSummary("/api/auth")
	if out.Total != 2 {
		t.Errorf("filtered total = %d, want 2", out.Total)
	}
	if len(out.Failures) != 1 {
		t.Errorf("filtered failures = %d, want 1", len(out.Failures))
	}
	if out.Failures[0].URL != "/api/auth/signin" {
		t.Errorf("failure URL = %q, want /api/auth/signin", out.Failures[0].URL)
	}
}
