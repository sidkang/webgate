package fetch

import (
	"context"
	"net"
	"net/url"
	"strings"
)

type UrlGuardReason string

const (
	ReasonInvalidURL          UrlGuardReason = "invalid_url"
	ReasonUnsupportedProtocol UrlGuardReason = "unsupported_protocol"
	ReasonMissingHostname     UrlGuardReason = "missing_hostname"
	ReasonBlockedHostname     UrlGuardReason = "blocked_hostname"
	ReasonBlockedAddress      UrlGuardReason = "blocked_address"
)

type LookupAddress struct {
	Address string
	Family  int
}

// Lookup resolves a hostname. Injectable for tests.
type Lookup func(ctx context.Context, hostname string) ([]LookupAddress, error)

var blockedHostnames = map[string]struct{}{
	"localhost":                {},
	"metadata.google.internal": {},
	"metadata":                 {},
}

func stripIPv6ZoneID(address string) string {
	normalized := strings.ToLower(address)
	if i := strings.IndexByte(normalized, '%'); i >= 0 {
		return normalized[:i]
	}
	return normalized
}

func isIPv6LinkLocal(address string) bool {
	normalized := stripIPv6ZoneID(address)
	if strings.HasPrefix(normalized, "::") {
		return false
	}
	first := normalized
	if i := strings.IndexByte(normalized, ':'); i >= 0 {
		first = normalized[:i]
	}
	if len(first) < 1 || len(first) > 4 {
		return false
	}
	n := 0
	for _, c := range first {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n |= int(c - '0')
		case c >= 'a' && c <= 'f':
			n |= int(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			n |= int(c - 'A' + 10)
		default:
			return false
		}
	}
	return n&0xffc0 == 0xfe80
}

func isPrivateOrSpecialIPv4(v4 net.IP) bool {
	if len(v4) != net.IPv4len {
		return true
	}
	a, b, c := int(v4[0]), int(v4[1]), int(v4[2])
	if a == 0 || a == 10 || a == 127 {
		return true
	}
	if a == 100 && b >= 64 && b <= 127 {
		return true
	}
	if a == 169 && b == 254 {
		return true
	}
	if a == 172 && b >= 16 && b <= 31 {
		return true
	}
	if a == 192 && b == 168 {
		return true
	}
	if a == 192 && b == 0 && c == 0 {
		return true
	}
	if a >= 224 {
		return true
	}
	return false
}

func isPrivateOrSpecialIPv6(address string) bool {
	normalized := stripIPv6ZoneID(address)
	if normalized == "::" || normalized == "::1" {
		return true
	}
	if strings.HasPrefix(normalized, "fc") || strings.HasPrefix(normalized, "fd") {
		return true
	}
	if isIPv6LinkLocal(normalized) {
		return true
	}
	if strings.HasPrefix(normalized, "ff") {
		return true
	}
	return false
}

// IsBlockedIPAddress reports whether an IP string is local/private/metadata/special.
// 198.18.0.0/15 is not specially allowed or denied (treated like other public-looking space).
func IsBlockedIPAddress(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return isPrivateOrSpecialIPv4(v4)
	}
	return isPrivateOrSpecialIPv6(address)
}

// IsBlockedHostname reports whether a hostname is blocked before DNS.
func IsBlockedHostname(hostname string) bool {
	normalized := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if normalized == "" {
		return true
	}
	if _, ok := blockedHostnames[normalized]; ok {
		return true
	}
	if strings.HasSuffix(normalized, ".localhost") {
		return true
	}
	if strings.HasSuffix(normalized, ".local") {
		return true
	}
	bare := normalized
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") {
		bare = bare[1 : len(bare)-1]
	}
	if net.ParseIP(bare) != nil {
		return IsBlockedIPAddress(bare)
	}
	return false
}

// ParseFetchURL validates scheme/host without DNS.
func ParseFetchURL(input string) (*url.URL, UrlGuardReason, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, ReasonInvalidURL, "URL must be a non-empty http(s) URL."
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" {
		return nil, ReasonInvalidURL, "URL must be a valid http(s) URL."
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ReasonUnsupportedProtocol, "Only http and https URLs can be fetched."
	}
	if u.Hostname() == "" {
		return nil, ReasonMissingHostname, "URL must include a hostname."
	}
	if IsBlockedHostname(u.Hostname()) {
		return nil, ReasonBlockedHostname, "URL hostname is blocked for local, private, or metadata destinations."
	}
	return u, "", ""
}

// AssertFetchURLAllowed parses and optionally DNS-checks a fetch URL.
// Lookup failure allows the URL (Cloak DNS is authoritative).
func AssertFetchURLAllowed(ctx context.Context, input string, lookup Lookup) (*url.URL, error) {
	u, reason, msg := ParseFetchURL(input)
	if reason != "" {
		return nil, NewError(CodeInvalidInput, msg)
	}
	if err := ctx.Err(); err != nil {
		return nil, mapCtxErr(err)
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		if IsBlockedIPAddress(host) {
			return nil, NewError(CodeInvalidInput, "URL address is blocked for local, private, or metadata destinations.")
		}
		return u, nil
	}
	if lookup == nil {
		lookup = defaultLookup
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, mapCtxErr(ctx.Err())
		}
		return u, nil
	}
	for _, item := range addrs {
		if IsBlockedIPAddress(item.Address) {
			return nil, NewError(CodeInvalidInput, "URL hostname resolved to a blocked local, private, or metadata address.")
		}
	}
	return u, nil
}

func defaultLookup(ctx context.Context, hostname string) ([]LookupAddress, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, err
	}
	out := make([]LookupAddress, 0, len(ips))
	for _, ip := range ips {
		family := 6
		if ip.IP.To4() != nil {
			family = 4
		}
		out = append(out, LookupAddress{Address: ip.IP.String(), Family: family})
	}
	return out, nil
}

func mapCtxErr(err error) error {
	if err == context.Canceled {
		return NewError(CodeAborted, "fetch was aborted")
	}
	if err == context.DeadlineExceeded {
		return NewError(CodeTimeout, "fetch timed out")
	}
	return NewError(CodeBackendError, "fetch failed")
}
