package browse

import (
	"errors"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		url, origin string
		want        bool
	}{
		{"https://a.com/x", "https://a.com", true},
		{"https://a.com/x?q=1#f", "https://a.com", true},
		{"https://a.com:8080/x", "https://a.com", false}, // port differs
		{"http://a.com", "https://a.com", false},         // scheme differs
		{"https://b.com", "https://a.com", false},        // host differs
		{"https://a.com", "", false},                     // no top-level origin
	}
	for _, c := range cases {
		if got := sameOrigin(c.url, c.origin); got != c.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", c.url, c.origin, got, c.want)
		}
	}
}

func TestOriginOf(t *testing.T) {
	cases := []struct{ url, want string }{
		{"https://a.com/x?y=1", "https://a.com"},
		{"https://a.com:8080/x", "https://a.com:8080"},
		{"http://a.com", "http://a.com"},
		{"not-a-url", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := originOf(c.url); got != c.want {
			t.Errorf("originOf(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestFetchInterceptorPatterns(t *testing.T) {
	// A union of typed rules yields one pattern per distinct resource type.
	fi := &fetchInterceptor{rules: []RequestRule{
		{Name: "a", ResourceTypes: []string{"Document"}},
		{Name: "b", ResourceTypes: []string{"Image", "Document"}},
	}}
	pats := fi.patternsLocked()
	if len(pats) != 2 {
		t.Fatalf("expected 2 patterns (Document, Image), got %d: %+v", len(pats), pats)
	}
	got := map[string]bool{}
	for _, p := range pats {
		got[p["resourceType"].(string)] = true
	}
	if !got["Document"] || !got["Image"] {
		t.Errorf("patterns missing a type: %+v", pats)
	}

	// Any rule wanting all types collapses to a single catch-all pattern.
	fiAll := &fetchInterceptor{rules: []RequestRule{
		{Name: "typed", ResourceTypes: []string{"Document"}},
		{Name: "all"}, // no ResourceTypes = all
	}}
	all := fiAll.patternsLocked()
	if len(all) != 1 || len(all[0]) != 0 {
		t.Errorf("a catch-all rule should produce one empty pattern, got %+v", all)
	}
}

func TestRequestVerdictSameOriginAsTop(t *testing.T) {
	r := InterceptedRequest{URL: "https://api.example.com/v1", TopLevelOrigin: "https://api.example.com"}
	if !r.SameOriginAsTop() {
		t.Error("same-origin request should match the top-level origin")
	}
	cross := InterceptedRequest{URL: "https://evil.example.com/steal", TopLevelOrigin: "https://api.example.com"}
	if cross.SameOriginAsTop() {
		t.Error("cross-origin request must not match the top-level origin")
	}
}

func TestURLPolicyDecisionLogIsBoundedAndReturnsSnapshot(t *testing.T) {
	p := &Page{}
	for i := 0; i < maxURLPolicyDecisions+1; i++ {
		p.recordURLPolicyDecision(URLPolicyDecision{URL: "https://example.test", Allowed: true})
	}
	decisions, dropped := p.URLPolicyDecisions()
	if len(decisions) != maxURLPolicyDecisions || dropped != 1 {
		t.Fatalf("URL policy log length/dropped = %d/%d, want %d/1", len(decisions), dropped, maxURLPolicyDecisions)
	}
	decisions[0].URL = "changed"
	copyOfLog, _ := p.URLPolicyDecisions()
	if copyOfLog[0].URL != "https://example.test" {
		t.Fatal("URLPolicyDecisions returned a mutable view of the internal log")
	}
}

func TestURLPolicyDecisionResolution(t *testing.T) {
	p := &Page{}
	decision := URLPolicyDecision{URL: "https://example.test", Allowed: true}
	p.finishURLPolicyDecision(&decision, nil)
	decisions, _ := p.URLPolicyDecisions()
	if decisions[0].Resolution != "resolved" {
		t.Fatalf("resolution = %q, want resolved", decisions[0].Resolution)
	}
	failed := URLPolicyDecision{URL: "https://blocked.example", Allowed: false}
	p.finishURLPolicyDecision(&failed, errors.New("CDP disposition failed"))
	decisions, _ = p.URLPolicyDecisions()
	last := decisions[len(decisions)-1]
	if last.Resolution != "failed" || last.ResolutionError != "CDP disposition failed" {
		t.Fatalf("failed resolution = %q / %q", last.Resolution, last.ResolutionError)
	}
}

func TestURLPolicyInstalledReportsInterceptorState(t *testing.T) {
	p := &Page{}
	if p.URLPolicyInstalled() {
		t.Fatal("new page unexpectedly reports URL policy installed")
	}
	p.setURLPolicyInstalled(true)
	if !p.URLPolicyInstalled() {
		t.Fatal("page did not report installed URL policy")
	}
}
