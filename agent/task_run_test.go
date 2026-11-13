package agent

import "testing"

func TestNormalizeTaskPageOrigin(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want string
		fail bool
	}{
		{name: "path query and fragment", url: "https://EXAMPLE.com/checkout/42?step=pay#confirm", want: "https://example.com:443"},
		{name: "explicit port", url: "http://localhost:8080/a", want: "http://localhost:8080"},
		{name: "userinfo rejected", url: "https://user:pass@example.com/path", fail: true},
		{name: "non HTTP scheme rejected", url: "file:///tmp/page.html", fail: true},
		{name: "empty URL rejected", url: "", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTaskPageOrigin(tc.url)
			if tc.fail {
				if err == nil {
					t.Fatalf("normalizeTaskPageOrigin(%q) = %q, want error", tc.url, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("normalizeTaskPageOrigin(%q) = %q, %v; want %q", tc.url, got, err, tc.want)
			}
		})
	}
}
