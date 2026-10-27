package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/vavallee/bindery/internal/auth"
)

// ResolveOIDCRedirectBase returns the base URL to use as the prefix for OIDC
// redirect_uri construction. Precedence:
//
//  1. configured (BINDERY_OIDC_REDIRECT_BASE_URL) — explicit operator override.
//     Required for path-prefix deploys and any case where forwarded headers
//     don't reflect the public URL the IdP will see.
//  2. X-Forwarded-Proto + X-Forwarded-Host on the request, if the request is
//     coming from a trusted proxy peer (BINDERY_TRUSTED_PROXY). Honors the
//     same trust boundary as proxy-auth mode.
//  3. r.Host with scheme inferred from r.TLS. Used when Bindery is reached
//     directly without a proxy.
//
// X-Forwarded-Proto is NEVER honoured outside the trusted-proxy branch — when
// Bindery is reachable directly, an attacker can set the header from a
// browser fetch and would otherwise be able to influence the redirect_uri
// scheme (e.g. forcing an https → http downgrade for the OIDC callback).
// r.TLS is the only safe signal in that case.
//
// Returns the empty string only when the request has no Host header at all
// (which would already have been rejected upstream). Trailing slashes are
// stripped; the caller appends the path.
func ResolveOIDCRedirectBase(r *http.Request, configured string, trusted []*net.IPNet) string {
	if configured != "" {
		return strings.TrimRight(configured, "/")
	}

	if len(trusted) > 0 && requestFromTrustedProxy(r, trusted) {
		xfh := strings.TrimRight(r.Header.Get("X-Forwarded-Host"), "/")
		xfp := strings.ToLower(r.Header.Get("X-Forwarded-Proto"))
		// Both values are validated, not just trusted because the peer is: a
		// proxy that forwards a client supplied header (#1640) would otherwise
		// let it pick the scheme and host of the redirect_uri. A value that
		// does not validate is ignored and the Host fallback below applies.
		if (xfp == "http" || xfp == "https") && validForwardedHost(xfh) {
			return xfp + "://" + xfh
		}
	}

	// Direct-reach path: only trust r.TLS for scheme. XFP from an untrusted
	// peer is attacker-controlled (see CVE-style scheme-downgrade scenario in
	// the function doc). trustedProxyMiddleware also strips XFP from untrusted
	// peers when wired in cmd/bindery, but this function is callable in
	// isolation (and tested in isolation) so it must stay safe on its own.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if r.Host == "" {
		return ""
	}
	return scheme + "://" + strings.TrimRight(r.Host, "/")
}

// validForwardedHost reports whether h is a bare host[:port]: a DNS name,
// IPv4 address or bracketed IPv6 address, with an optional numeric port in
// 1-65535. No scheme, userinfo, path, query, fragment, whitespace, control
// characters or comma lists, so it can be placed after "scheme://" and stay a
// plain authority.
func validForwardedHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}
	host := h
	if strings.HasPrefix(h, "[") {
		end := strings.Index(h, "]")
		if end < 0 {
			return false
		}
		ip := net.ParseIP(h[1:end])
		if ip == nil || ip.To4() != nil {
			return false
		}
		rest := h[end+1:]
		return rest == "" || validForwardedPort(rest)
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		if !validForwardedPort(h[i:]) {
			return false
		}
		host = h[:i]
	}
	if host == "" {
		return false
	}
	for _, c := range host {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
		default:
			return false
		}
	}
	return true
}

// validForwardedPort accepts ":" followed by a decimal port in 1-65535.
func validForwardedPort(s string) bool {
	if len(s) < 2 || len(s) > 6 || s[0] != ':' {
		return false
	}
	n := 0
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 65535
}

// requestFromTrustedProxy reports whether the request's immediate peer (the
// last hop, captured before real-IP rewriting) is in the configured proxy CIDR
// list.
func requestFromTrustedProxy(r *http.Request, trusted []*net.IPNet) bool {
	ip := net.ParseIP(auth.RealPeerHost(r))
	if ip == nil {
		return false
	}
	for _, c := range trusted {
		if c.Contains(ip) {
			return true
		}
	}
	return false
}
