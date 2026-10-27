package api

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse cidr %q: %v", cidr, err)
	}
	return n
}

func TestResolveOIDCRedirectBase_ConfiguredWins(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://internal.cluster.svc/api/v1/auth/oidc/x/login", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "bindery.example.com")
	r.RemoteAddr = "10.0.0.5:54321"

	got := ResolveOIDCRedirectBase(r, "https://override.example.com", []*net.IPNet{mustCIDR(t, "10.0.0.0/8")})
	if got != "https://override.example.com" {
		t.Fatalf("configured value should win, got %q", got)
	}
}

func TestResolveOIDCRedirectBase_TrustedProxyForwarded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
	r.Host = "internal.cluster.svc"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "bindery.example.com")
	r.RemoteAddr = "10.0.0.5:54321"

	got := ResolveOIDCRedirectBase(r, "", []*net.IPNet{mustCIDR(t, "10.0.0.0/8")})
	if got != "https://bindery.example.com" {
		t.Fatalf("trusted-proxy forwarded headers should be used, got %q", got)
	}
}

func TestResolveOIDCRedirectBase_UntrustedPeerIgnoresForwarded(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
	r.Host = "internal.cluster.svc"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "evil.example.com")
	r.RemoteAddr = "203.0.113.7:443"

	// Untrusted peer must not be able to influence host OR scheme. r.TLS is
	// nil here, so the safe fallback is plain http. Honouring XF-Proto=https
	// from an untrusted peer would be the scheme-spoofing bug the audit
	// flagged (Bundle A finding #3).
	got := ResolveOIDCRedirectBase(r, "", []*net.IPNet{mustCIDR(t, "10.0.0.0/8")})
	if got != "http://internal.cluster.svc" {
		t.Fatalf("forwarded headers from untrusted peer must be ignored (host AND scheme); got %q, want http://internal.cluster.svc", got)
	}
}

func TestResolveOIDCRedirectBase_NoForwardedHeadersFallsBackToHost(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
	r.Host = "bindery.example.com"
	r.RemoteAddr = "10.0.0.5:54321"

	got := ResolveOIDCRedirectBase(r, "", []*net.IPNet{mustCIDR(t, "10.0.0.0/8")})
	if got != "http://bindery.example.com" {
		t.Fatalf("trusted peer without XF headers should fall back to Host, got %q", got)
	}
}

func TestResolveOIDCRedirectBase_HTTPSDetection(t *testing.T) {
	cases := []struct {
		name string
		set  func(*http.Request)
		want string
	}{
		{
			name: "TLS connection",
			set: func(r *http.Request) {
				r.Host = "bindery.example.com"
				r.TLS = &tls.ConnectionState{}
			},
			want: "https://bindery.example.com",
		},
		{
			// SECURITY: X-Forwarded-Proto from an untrusted peer is attacker-
			// controlled (browser fetch / curl can set it). Honouring it here
			// would let an attacker downgrade the OIDC callback URI from
			// https to http when Bindery is reached directly. Only r.TLS
			// is a safe signal in the direct-reach path; this case must
			// fall back to plain http even with XFP=https set.
			name: "X-Forwarded-Proto=https from untrusted peer is ignored",
			set: func(r *http.Request) {
				r.Host = "bindery.example.com"
				r.Header.Set("X-Forwarded-Proto", "https")
				r.RemoteAddr = "203.0.113.7:443"
			},
			want: "http://bindery.example.com",
		},
		{
			name: "plain HTTP",
			set:  func(r *http.Request) { r.Host = "bindery.example.com" },
			want: "http://bindery.example.com",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			c.set(r)
			if got := ResolveOIDCRedirectBase(r, "", nil); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestOIDCRedirect_HonorsXForwardedProtoOnlyForTrustedProxies pins down the
// scheme-downgrade fix: an untrusted peer that sends X-Forwarded-Proto: http
// must not be able to force the OIDC callback off https, and a trusted peer
// must still be able to surface the real public scheme. Without the trusted-
// proxy gate, a developer instance reachable directly would issue a callback
// like http://bindery.example.com/... after an attacker pings the login URL
// with the spoofed header, opening a downgrade-and-intercept window on the
// OIDC code exchange.
func TestOIDCRedirect_HonorsXForwardedProtoOnlyForTrustedProxies(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}

	t.Run("untrusted peer cannot downgrade scheme with X-Forwarded-Proto", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
		r.Host = "bindery.example.com"
		r.TLS = &tls.ConnectionState{} // real connection is TLS
		r.RemoteAddr = "192.168.1.5:1234"
		r.Header.Set("X-Forwarded-Proto", "http")

		got := ResolveOIDCRedirectBase(r, "", trusted)
		if got != "https://bindery.example.com" {
			t.Fatalf("untrusted XF-Proto=http must not downgrade an https connection; got %q, want https://bindery.example.com", got)
		}
	})

	t.Run("untrusted peer cannot upgrade scheme with X-Forwarded-Proto", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
		r.Host = "bindery.example.com"
		// No r.TLS — plain HTTP connection.
		r.RemoteAddr = "203.0.113.7:443"
		r.Header.Set("X-Forwarded-Proto", "https")

		got := ResolveOIDCRedirectBase(r, "", trusted)
		if got != "http://bindery.example.com" {
			t.Fatalf("untrusted XF-Proto=https must not pretend a plain http connection is https; got %q, want http://bindery.example.com", got)
		}
	})

	t.Run("trusted peer is honoured", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
		r.Host = "internal.cluster.svc"
		r.RemoteAddr = "10.0.0.5:54321"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "bindery.example.com")

		got := ResolveOIDCRedirectBase(r, "", trusted)
		if got != "https://bindery.example.com" {
			t.Fatalf("trusted proxy XF headers must be honoured; got %q", got)
		}
	})
}

func TestResolveOIDCRedirectBase_StripsTrailingSlashes(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "bindery.example.com"

	got := ResolveOIDCRedirectBase(r, "https://override.example.com/", nil)
	if got != "https://override.example.com" {
		t.Fatalf("trailing slash on configured value should be stripped, got %q", got)
	}

	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "bindery.example.com/")
	r.RemoteAddr = "10.0.0.5:54321"
	got = ResolveOIDCRedirectBase(r, "", []*net.IPNet{mustCIDR(t, "10.0.0.0/8")})
	if got != "https://bindery.example.com" {
		t.Fatalf("trailing slash on X-Forwarded-Host should be stripped, got %q", got)
	}
}

// #1640: a trusted proxy that forwards a client supplied X-Forwarded-Proto or
// X-Forwarded-Host (a common nginx footgun) must not let the value shape the
// OIDC redirect_uri. Anything that is not an http or https scheme plus a bare
// host[:port] is ignored and the base falls back to the request's own Host.
func TestResolveOIDCRedirectBase_ForwardedValuesValidated(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}
	cases := []struct {
		name, proto, host, want string
	}{
		{"valid https host", "https", "bindery.example.com", "https://bindery.example.com"},
		{"valid http host", "http", "bindery.example.com", "http://bindery.example.com"},
		{"scheme is case folded", "HTTPS", "bindery.example.com", "https://bindery.example.com"},
		{"host with port", "https", "bindery.example.com:8443", "https://bindery.example.com:8443"},
		{"ipv4 with port", "http", "192.168.1.50:8787", "http://192.168.1.50:8787"},
		{"bracketed ipv6 with port", "https", "[2001:db8::1]:8443", "https://[2001:db8::1]:8443"},
		{"single label docker name", "http", "bindery", "http://bindery"},
		{"trailing slash is stripped", "https", "bindery.example.com/", "https://bindery.example.com"},

		{"javascript scheme", "javascript", "bindery.example.com", "http://internal.cluster.svc"},
		{"ftp scheme", "ftp", "bindery.example.com", "http://internal.cluster.svc"},
		{"scheme with separator", "https://", "bindery.example.com", "http://internal.cluster.svc"},
		{"comma list scheme", "https,http", "bindery.example.com", "http://internal.cluster.svc"},
		{"host with path", "https", "evil.example/phish", "http://internal.cluster.svc"},
		{"host with userinfo", "https", "user@evil.example", "http://internal.cluster.svc"},
		{"host with scheme", "https", "https://evil.example", "http://internal.cluster.svc"},
		{"host with query", "https", "evil.example?x=1", "http://internal.cluster.svc"},
		{"host with fragment", "https", "evil.example#x", "http://internal.cluster.svc"},
		{"host with space", "https", "evil.example evil2.example", "http://internal.cluster.svc"},
		{"host with backslash", "https", `evil.example\@good.example`, "http://internal.cluster.svc"},
		{"comma list host", "https", "a.example,b.example", "http://internal.cluster.svc"},
		{"host with control char", "https", "evil.example\r\nX-Injected: 1", "http://internal.cluster.svc"},
		{"non numeric port", "https", "bindery.example.com:abc", "http://internal.cluster.svc"},
		{"port out of range", "https", "bindery.example.com:99999", "http://internal.cluster.svc"},
		{"empty port", "https", "bindery.example.com:", "http://internal.cluster.svc"},
		{"only a slash", "https", "/", "http://internal.cluster.svc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/x/login", nil)
			r.Host = "internal.cluster.svc"
			r.RemoteAddr = "10.0.0.5:54321"
			r.Header.Set("X-Forwarded-Proto", c.proto)
			r.Header.Set("X-Forwarded-Host", c.host)
			if got := ResolveOIDCRedirectBase(r, "", trusted); got != c.want {
				t.Errorf("proto=%q host=%q: base = %q, want %q", c.proto, c.host, got, c.want)
			}
		})
	}
}
