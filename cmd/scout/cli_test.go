package main

import (
	"testing"
	"time"
)

func TestParseFlags_KeyValue(t *testing.T) {
	f := parseFlags([]string{"--foo=bar", "--baz=qux", "positional"})
	if got := f.get("foo", ""); got != "bar" {
		t.Errorf("foo = %q, want bar", got)
	}
	if got := f.get("baz", ""); got != "qux" {
		t.Errorf("baz = %q, want qux", got)
	}
}

func TestParseFlags_BareFlagEmptyValue(t *testing.T) {
	f := parseFlags([]string{"--headless"})
	if got := f.get("headless", "X"); got != "" {
		t.Errorf("bare flag value = %q, want empty", got)
	}
	if !f.getBool("headless", false) {
		t.Error("bare flag must read as bool true")
	}
}

func TestParseFlags_PositionalIgnored(t *testing.T) {
	f := parseFlags([]string{"https://example.com", "--timeout=5s"})
	if got := f.getDuration("timeout", 0); got != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", got)
	}
}

func TestCliFlags_GetDefault(t *testing.T) {
	f := parseFlags(nil)
	if got := f.get("missing", "fallback"); got != "fallback" {
		t.Errorf("default fallback failed: %q", got)
	}
}

func TestCliFlags_GetBoolValues(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"", true}, // bare presence
		{"false", false},
		{"0", false},
		{"junk", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			args := []string{"--flag=" + tc.raw}
			if tc.raw == "" {
				args = []string{"--flag"}
			}
			f := parseFlags(args)
			if got := f.getBool("flag", false); got != tc.want {
				t.Errorf("getBool(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCliFlags_GetDurationFallback(t *testing.T) {
	f := parseFlags([]string{"--timeout=not-a-duration"})
	if got := f.getDuration("timeout", 7*time.Second); got != 7*time.Second {
		t.Errorf("invalid duration must fall back to default, got %v", got)
	}
}

func TestCliFlags_GetDurationMissing(t *testing.T) {
	f := parseFlags(nil)
	if got := f.getDuration("missing", 3*time.Second); got != 3*time.Second {
		t.Errorf("missing duration must use default, got %v", got)
	}
}
