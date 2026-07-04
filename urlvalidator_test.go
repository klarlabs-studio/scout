package browse

import (
	"net"
	"strings"
	"testing"
)

// withResolver swaps the package resolver for a hermetic fake for the duration
// of a test, so DNS-dependent validation is deterministic and offline.
func withResolver(t *testing.T, fn func(host string) ([]net.IP, error)) {
	t.Helper()
	orig := lookupIP
	lookupIP = fn
	t.Cleanup(func() { lookupIP = orig })
}

func TestURLValidator_ValidURLs(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}

	tests := []struct {
		name string
		url  string
	}{
		{"http scheme", "http://example.com"},
		{"https scheme", "https://example.com"},
		{"https with path", "https://example.com/page"},
		{"https with query", "https://example.com/search?q=go"},
		{"https with fragment", "https://example.com/page#section"},
		{"https with port", "https://example.com:8080/api"},
		{"http subdomain", "http://sub.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.Validate(tt.url); err != nil {
				t.Errorf("Validate(%q) returned error: %v", tt.url, err)
			}
		})
	}
}

func TestURLValidator_BlockedSchemes(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}

	tests := []struct {
		name string
		url  string
	}{
		{"file scheme", "file:///etc/passwd"},
		{"javascript scheme", "javascript:alert(1)"},
		{"data scheme", "data:text/html,<h1>hi</h1>"},
		{"ftp scheme", "ftp://example.com/file"},
		{"chrome scheme", "chrome://settings"},
		{"about scheme", "about:blank"},
		{"empty scheme", "://no-scheme"}, // fails at URL parse, not scheme check
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(tt.url)
			if err == nil {
				t.Errorf("Validate(%q) should have returned error", tt.url)
			}
		})
	}
}

func TestURLValidator_PrivateIPsBlocked(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}

	tests := []struct {
		name string
		url  string
	}{
		{"loopback IPv4", "http://127.0.0.1"},
		{"loopback IPv4 with port", "http://127.0.0.1:8080"},
		{"private 10.x", "http://10.0.0.1"},
		{"private 172.16.x", "http://172.16.0.1"},
		{"private 192.168.x", "http://192.168.1.1"},
		{"loopback IPv6", "http://[::1]"},
		{"link-local IPv4", "http://169.254.1.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(tt.url)
			if err == nil {
				t.Errorf("Validate(%q) should block private IP", tt.url)
			}
			if err != nil && !strings.Contains(err.Error(), "blocked navigation to private") {
				t.Errorf("Validate(%q) error should mention private IP, got: %v", tt.url, err)
			}
		})
	}
}

func TestURLValidator_PrivateIPsAllowed(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: true}

	tests := []struct {
		name string
		url  string
	}{
		{"loopback", "http://127.0.0.1"},
		{"loopback with port", "http://127.0.0.1:3000"},
		{"private 10.x", "http://10.0.0.1"},
		{"private 192.168.x", "http://192.168.1.1"},
		{"loopback IPv6", "http://[::1]"},
		{"localhost by IP", "http://127.0.0.1:8080/api"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.Validate(tt.url); err != nil {
				t.Errorf("Validate(%q) with AllowPrivateIPs=true returned error: %v", tt.url, err)
			}
		})
	}
}

func TestURLValidator_PublicIPsAlwaysAllowed(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}

	tests := []struct {
		name string
		url  string
	}{
		{"public IP", "http://8.8.8.8"},
		{"public IP with path", "https://1.1.1.1/dns-query"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.Validate(tt.url); err != nil {
				t.Errorf("Validate(%q) should allow public IP, got: %v", tt.url, err)
			}
		})
	}
}

func TestURLValidator_LocalhostBlocked(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}

	// "localhost" names the loopback interface and must be blocked without
	// depending on the resolver.
	for _, u := range []string{
		"http://localhost",
		"http://localhost:3000",
		"http://LOCALHOST",
		"http://foo.localhost",
		"http://localhost.", // trailing dot
	} {
		t.Run(u, func(t *testing.T) {
			if err := v.Validate(u); err == nil {
				t.Errorf("Validate(%q) should block the loopback host", u)
			}
		})
	}
}

// TestURLValidator_NumericEncodingsBlocked covers browser-accepted numeric host
// encodings that net.ParseIP rejects: an SSRF actor uses these to reach loopback
// past a naive IP-literal check. No DNS is involved.
func TestURLValidator_NumericEncodingsBlocked(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}

	for _, tt := range []struct{ name, url string }{
		{"decimal loopback", "http://2130706433/"},      // 127.0.0.1
		{"hex loopback", "http://0x7f000001/"},          // 127.0.0.1
		{"octal dotted loopback", "http://0177.0.0.1/"}, // 127.0.0.1
		{"decimal metadata", "http://2852039166/"},      // 169.254.169.254
		{"short-form private", "http://10.1/"},          // 10.0.0.1
		{"unspecified 0.0.0.0", "http://0.0.0.0:8080/"}, // IsUnspecified
		{"unspecified single 0", "http://0/"},           // 0.0.0.0
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.Validate(tt.url); err == nil {
				t.Errorf("Validate(%q) should block the encoded internal address", tt.url)
			}
		})
	}
}

// TestURLValidator_NumericEncodingsPublicAllowed confirms the numeric parser
// doesn't over-block public addresses expressed numerically.
func TestURLValidator_NumericEncodingsPublicAllowed(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}
	for _, u := range []string{
		"http://134744072/",  // 8.8.8.8
		"http://0x08080808/", // 8.8.8.8
	} {
		t.Run(u, func(t *testing.T) {
			if err := v.Validate(u); err != nil {
				t.Errorf("Validate(%q) should allow the public address, got: %v", u, err)
			}
		})
	}
}

// TestURLValidator_DNSResolutionBlocked exercises the resolver path: a public
// hostname whose A record points at an internal address (static-DNS / *.nip.io
// style bypass) must be blocked.
func TestURLValidator_DNSResolutionBlocked(t *testing.T) {
	v := URLValidator{AllowPrivateIPs: false}
	withResolver(t, func(host string) ([]net.IP, error) {
		switch host {
		case "rebind.attacker.example":
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		case "metadata.internal.example":
			return []net.IP{net.ParseIP("169.254.169.254")}, nil
		case "good.example":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		default:
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	})

	if err := v.Validate("http://rebind.attacker.example/"); err == nil {
		t.Error("host resolving to 127.0.0.1 should be blocked")
	}
	if err := v.Validate("http://metadata.internal.example/"); err == nil {
		t.Error("host resolving to link-local metadata IP should be blocked")
	}
	if err := v.Validate("http://good.example/"); err != nil {
		t.Errorf("host resolving to a public IP should be allowed, got: %v", err)
	}
	// A name that fails to resolve is not a policy violation (navigation fails
	// on its own); it must not error the validator.
	if err := v.Validate("http://nonexistent.example/"); err != nil {
		t.Errorf("unresolvable host should not error, got: %v", err)
	}
}

func TestDefaultURLValidator(t *testing.T) {
	if DefaultURLValidator.AllowPrivateIPs {
		t.Error("DefaultURLValidator should have AllowPrivateIPs=false")
	}
	if err := DefaultURLValidator.Validate("https://example.com"); err != nil {
		t.Errorf("DefaultURLValidator should allow public URLs: %v", err)
	}
	if err := DefaultURLValidator.Validate("http://127.0.0.1"); err == nil {
		t.Error("DefaultURLValidator should block private IPs")
	}
}
