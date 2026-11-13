package browse

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// allowedSchemes for navigation. Blocks file://, javascript:, data:, chrome:// etc.
var allowedSchemes = map[string]bool{"http": true, "https": true}

// lookupIP resolves a hostname to IP addresses. It is a package variable so
// tests can substitute a hermetic resolver.
var lookupIP = net.LookupIP

// URLValidator controls URL validation for navigation.
// Set AllowPrivateIPs to true to permit loopback/private IP navigation (e.g., for testing).
type URLValidator struct {
	AllowPrivateIPs bool
	// PolicyID is an opaque, stable identifier supplied by the embedding
	// application so policy denials can be correlated across interfaces.
	PolicyID string
	// AllowedOrigins restricts top-level document navigations and redirects to
	// exact origins. Scheme and non-default port are part of the origin; default
	// ports are normalized, paths are ignored, and malformed entries return an
	// error. The policy is applied to top-level navigations, redirects, and
	// Fetch-interceptable HTTP(S) requests, including subresources, fetch/XHR, and
	// downloads. WebSocket handshakes are outside CDP Fetch interception.
	AllowedOrigins []string
}

// DefaultURLValidator blocks private IPs and non-http(s) schemes.
var DefaultURLValidator = URLValidator{AllowPrivateIPs: false}

// Validate checks that a URL is safe for navigation.
//
// It blocks non-http(s) schemes and, unless AllowPrivateIPs is set, any
// destination that names, encodes, or resolves to a private/loopback/link-local/
// unspecified address. The private-IP block resists common SSRF bypasses: it
// blocks the "localhost" name, normalizes browser-style numeric host encodings
// (decimal, hex, and octal inet_aton forms) before checking, and resolves DNS
// hostnames and blocks any answer in an internal range.
//
// Redirects are also checked by each Page's request interceptor. It does not
// defend against DNS rebinding (a host that resolves public here but private
// when Chrome navigates); that requires enforcement at the network layer.
func (v URLValidator) Validate(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &URLPolicyError{Code: "SCOUT_POLICY_INVALID_URL", PolicyID: v.PolicyID, Err: fmt.Errorf("browse: invalid URL: %w", err)}
	}
	scheme := strings.ToLower(u.Scheme)
	if !allowedSchemes[scheme] {
		return &URLPolicyError{Code: "SCOUT_POLICY_SCHEME_BLOCKED", PolicyID: v.PolicyID, Err: fmt.Errorf("browse: blocked URL scheme %q (only http/https allowed)", u.Scheme)}
	}
	if len(v.AllowedOrigins) > 0 {
		origin := normalizedOrigin(u)
		allowed := false
		for _, candidate := range v.AllowedOrigins {
			parsed, parseErr := url.Parse(candidate)
			if parseErr != nil {
				return fmt.Errorf("browse: invalid allowed origin %q: %w", candidate, parseErr)
			}
			candidateScheme := strings.ToLower(parsed.Scheme)
			if parsed.User != nil {
				return fmt.Errorf("browse: invalid allowed origin %q: credentials are not allowed", candidate)
			}
			if parsed.Host == "" || (candidateScheme != "http" && candidateScheme != "https") {
				return fmt.Errorf("browse: invalid allowed origin %q: expected an http(s) origin", candidate)
			}
			if normalizedOrigin(parsed) == origin {
				allowed = true
			}
		}
		if !allowed {
			return &URLPolicyError{Code: "SCOUT_POLICY_ORIGIN_DENIED", PolicyID: v.PolicyID, Err: fmt.Errorf("browse: blocked request to origin %q; origin is not in the allowlist", origin)}
		}
	}
	if v.AllowPrivateIPs {
		return nil
	}
	if err := checkHost(u.Hostname()); err != nil {
		var policyErr *URLPolicyError
		if errors.As(err, &policyErr) {
			copy := *policyErr
			copy.PolicyID = v.PolicyID
			return &copy
		}
		return err
	}
	return nil
}

// normalizedOrigin returns the URL origin tuple with scheme/host casing
// normalized and default ports omitted. URL paths are intentionally ignored
// because existing allowlist configuration accepts origin URLs with paths.
func normalizedOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	hostname := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// checkHost blocks a host that names, encodes, or resolves to a private,
// loopback, link-local, or unspecified address.
func checkHost(host string) error {
	if host == "" {
		return &URLPolicyError{Code: "SCOUT_POLICY_INVALID_URL", Err: fmt.Errorf("browse: blocked navigation to URL with no host")}
	}

	// "localhost" and any *.localhost always name the loopback interface,
	// regardless of what a resolver returns.
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return &URLPolicyError{Code: "SCOUT_POLICY_PRIVATE_ADDRESS_BLOCKED", Err: fmt.Errorf("browse: blocked navigation to loopback host %q", host)}
	}

	// An IP literal (dotted-quad or IPv6) — check directly.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return &URLPolicyError{Code: "SCOUT_POLICY_PRIVATE_ADDRESS_BLOCKED", Err: fmt.Errorf("browse: blocked navigation to private/loopback IP %s", ip)}
		}
		return nil
	}

	// A browser-style numeric host (decimal 2130706433, hex 0x7f000001, octal
	// 0177.0.0.1) that net.ParseIP rejects but Chrome resolves to an IP.
	if ip, ok := parseIPv4Number(host); ok {
		if isBlockedIP(ip) {
			return &URLPolicyError{Code: "SCOUT_POLICY_PRIVATE_ADDRESS_BLOCKED", Err: fmt.Errorf("browse: blocked navigation to private/loopback IP %s (encoded as %q)", ip, host)}
		}
		return nil
	}

	// A DNS name — resolve and block if any answer is internal. Fail closed on
	// resolution errors: Chrome has a separate resolver and could otherwise reach
	// a host that the policy check could not classify.
	ips, err := lookupIP(host)
	if err != nil {
		return &URLPolicyError{Code: "SCOUT_POLICY_ADDRESS_UNVERIFIED", Err: fmt.Errorf("browse: blocked navigation to %q because its address could not be verified: %w", host, err)}
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return &URLPolicyError{Code: "SCOUT_POLICY_PRIVATE_ADDRESS_BLOCKED", Err: fmt.Errorf("browse: blocked navigation to %q which resolves to private/loopback IP %s", host, ip)}
		}
	}
	return nil
}

// isBlockedIP reports whether ip is in a range Scout must not reach by default:
// loopback, RFC1918/ULA private, link-local (incl. cloud metadata
// 169.254.169.254), or the unspecified address (0.0.0.0 / ::).
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// parseIPv4Number parses the browser/inet_aton numeric host forms that
// net.ParseIP rejects but Chrome accepts: a 1-4 part address where each part may
// be decimal, hex (0x…), or octal (leading 0), and a short form's final part
// fills the remaining low-order bytes. Returns (ip, true) only when host is such
// a numeric form.
func parseIPv4Number(host string) (net.IP, bool) {
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return nil, false
	}
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return nil, false
	}
	nums := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := parseIPv4Part(p)
		if !ok {
			return nil, false
		}
		// Every part but the last addresses exactly one byte.
		if i < len(parts)-1 && n > 0xff {
			return nil, false
		}
		nums[i] = n
	}
	// The final part fills the remaining low-order bytes.
	last := nums[len(parts)-1]
	if remaining := uint(8 * (5 - len(parts))); remaining < 32 && last >= (uint64(1)<<remaining) {
		return nil, false
	}
	addr := last
	for i := 0; i < len(parts)-1; i++ {
		addr += nums[i] << uint(8*(3-i))
	}
	if addr > 0xffffffff {
		return nil, false
	}
	return net.IPv4(byte(addr>>24), byte(addr>>16), byte(addr>>8), byte(addr)), true
}

// parseIPv4Part parses one host segment as decimal, hex (0x…), or octal (0…),
// matching how a browser interprets a numeric IPv4 host.
func parseIPv4Part(p string) (uint64, bool) {
	if p == "" {
		return 0, false
	}
	base := 10
	switch {
	case len(p) >= 2 && (p[:2] == "0x" || p[:2] == "0X"):
		base, p = 16, p[2:]
		if p == "" {
			return 0, false
		}
	case len(p) >= 2 && p[0] == '0':
		base, p = 8, p[1:]
	}
	n, err := strconv.ParseUint(p, base, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
