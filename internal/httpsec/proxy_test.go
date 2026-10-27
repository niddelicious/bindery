package httpsec

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// resetProxy clears any configured outbound proxy. Registered via t.Cleanup so
// the package-global proxy state never leaks between tests (these tests must
// not run with t.Parallel for the same reason).
func resetProxy(t *testing.T) {
	t.Cleanup(func() {
		if _, err := ConfigureOutboundProxy("", "", true); err != nil {
			t.Fatalf("reset proxy: %v", err)
		}
	})
}

func TestConfigureOutboundProxy_Disabled(t *testing.T) {
	resetProxy(t)

	u, err := ConfigureOutboundProxy("", "", true)
	if err != nil || u != nil {
		t.Fatalf("ConfigureOutboundProxy(\"\") = %v, %v; want nil, nil", u, err)
	}
	if ProxyFunc() != nil {
		t.Error("ProxyFunc() should be nil when no proxy is configured")
	}
	if got := DefaultProxyTransport(); got != http.DefaultTransport {
		t.Errorf("DefaultProxyTransport() = %v; want http.DefaultTransport when unset", got)
	}
}

func TestConfigureOutboundProxy_Valid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // expected resolved proxy URL string
	}{
		{"http", "http://proxy.example:3128", "http://proxy.example:3128"},
		{"http with creds", "http://user:pass@proxy.example:3128", "http://user:pass@proxy.example:3128"},
		{"https", "https://proxy.example:8443", "https://proxy.example:8443"},
		{"socks5", "socks5://proxy.example:1080", "socks5://proxy.example:1080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetProxy(t)

			u, err := ConfigureOutboundProxy(tc.raw, "", true)
			if err != nil {
				t.Fatalf("ConfigureOutboundProxy(%q) error: %v", tc.raw, err)
			}
			if u == nil || u.String() != tc.want {
				t.Fatalf("ConfigureOutboundProxy(%q) = %v; want %s", tc.raw, u, tc.want)
			}

			pf := ProxyFunc()
			if pf == nil {
				t.Fatal("ProxyFunc() is nil after configuring a proxy")
			}
			// A public (non-bypassed) destination resolves to the proxy.
			req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
			got, err := pf(req)
			if err != nil || got == nil || got.String() != tc.want {
				t.Fatalf("ProxyFunc()(public) = %v, %v; want %s", got, err, tc.want)
			}

			if DefaultProxyTransport() == http.DefaultTransport {
				t.Error("DefaultProxyTransport() should be a proxy-applying clone, not http.DefaultTransport")
			}
		})
	}
}

func TestConfigureOutboundProxy_Invalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"unsupported socks4", "socks4://proxy.example:1080"},
		{"unsupported scheme", "ftp://proxy.example:21"},
		{"missing host", "http://"},
		{"malformed escape", "http://%zz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetProxy(t)

			u, err := ConfigureOutboundProxy(tc.raw, "", true)
			if err == nil {
				t.Fatalf("ConfigureOutboundProxy(%q) = %v, nil; want error", tc.raw, u)
			}
			// On error the proxy must stay disabled.
			if ProxyFunc() != nil {
				t.Error("ProxyFunc() should remain nil after a rejected proxy URL")
			}
			if DefaultProxyTransport() != http.DefaultTransport {
				t.Error("DefaultProxyTransport() should remain http.DefaultTransport after a rejected proxy URL")
			}
		})
	}
}

// TestConfigureOutboundProxy_InvalidDoesNotLeakCredentials ensures the error
// returned for a malformed proxy URL never echoes back the cleartext password.
// url.Parse embeds the full raw input in its error string, and this error is
// logged at startup, so wrapping it with %w would leak the credential.
func TestConfigureOutboundProxy_InvalidDoesNotLeakCredentials(t *testing.T) {
	resetProxy(t)

	const secret = "supersecret"
	// Malformed userinfo URL that url.Parse rejects while carrying a password.
	_, err := ConfigureOutboundProxy("http://user:"+secret+"@%zz:3128", "", true)
	if err == nil {
		t.Fatal("ConfigureOutboundProxy returned nil error for malformed url; want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error string leaks proxy password: %q", err.Error())
	}
}

// TestProxyFunc_BypassRules covers both the bypass-local heuristics and the
// explicit no-proxy list: remote hosts are proxied; LAN / loopback / single-label
// hosts and no-proxy matches are dialled direct.
func TestProxyFunc_BypassRules(t *testing.T) {
	resetProxy(t)
	if _, err := ConfigureOutboundProxy("http://proxy.example:3128", "nzbgeek.info, 172.16.0.0/12", true); err != nil {
		t.Fatalf("configure: %v", err)
	}
	pf := ProxyFunc()
	if pf == nil {
		t.Fatal("ProxyFunc() is nil")
	}

	cases := []struct {
		url     string
		proxied bool
	}{
		{"http://example.com/x", true},          // public -> proxied
		{"https://api.openlibrary.org/x", true}, // public -> proxied
		{"http://192.168.1.50:9117", false},     // RFC1918 -> bypass
		{"http://127.0.0.1:8080", false},        // loopback -> bypass
		{"http://172.16.5.4/x", false},          // no-proxy CIDR -> bypass
		{"http://jackett:9117/x", false},        // single-label host -> bypass
		{"http://localhost:8989", false},        // localhost -> bypass
		{"http://prowlarr.local/x", false},      // .local -> bypass
		{"https://nzbgeek.info/x", false},       // no-proxy exact -> bypass
		{"https://api.nzbgeek.info/x", false},   // no-proxy domain suffix -> bypass
	}
	for _, c := range cases {
		req, _ := http.NewRequest(http.MethodGet, c.url, nil)
		got, err := pf(req)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.url, err)
			continue
		}
		if c.proxied && got == nil {
			t.Errorf("%s: expected proxied, got direct", c.url)
		}
		if !c.proxied && got != nil {
			t.Errorf("%s: expected direct, got proxied (%s)", c.url, got)
		}
	}
}

func TestProxyFunc_BypassLocalDisabled(t *testing.T) {
	resetProxy(t)
	// With bypassLocal=false only the explicit no-proxy list is honoured, so a
	// LAN host is proxied.
	if _, err := ConfigureOutboundProxy("http://proxy.example:3128", "", false); err != nil {
		t.Fatalf("configure: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://192.168.1.50:9117", nil)
	got, _ := ProxyFunc()(req)
	if got == nil {
		t.Error("with bypassLocal=false a LAN host should be proxied")
	}
}

// TestDefaultProxyTransport_RoutesThroughProxy verifies that a client built off
// DefaultProxyTransport() actually dials the configured proxy: the request for
// an external host lands on the local proxy server instead.
func TestDefaultProxyTransport_RoutesThroughProxy(t *testing.T) {
	resetProxy(t)

	hit := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	if _, err := ConfigureOutboundProxy(proxy.URL, "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy(%q): %v", proxy.URL, err)
	}

	client := &http.Client{Transport: DefaultProxyTransport()}
	resp, err := client.Get("http://example.invalid/path")
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer resp.Body.Close()

	select {
	case host := <-hit:
		if host != "example.invalid" {
			t.Errorf("proxy saw Host %q; want example.invalid", host)
		}
	default:
		t.Error("request did not reach the configured proxy")
	}
}

// TestBypassProxyForHost_Edges covers the host-classification branches the
// higher-level tests don't reach: empty host, the .lan / .internal suffixes, a
// public IP, and IPv6 ULA / link-local literals.
func TestBypassProxyForHost_Edges(t *testing.T) {
	resetProxy(t)
	if _, err := ConfigureOutboundProxy("http://proxy.example:3128", "10.0.0.0/8", true); err != nil {
		t.Fatalf("configure: %v", err)
	}
	check := func(host string, wantBypass bool) {
		t.Helper()
		if got := bypassProxyForHost(host); got != wantBypass {
			t.Errorf("bypassProxyForHost(%q) = %v; want %v", host, got, wantBypass)
		}
	}
	check("", false)
	check("box.lan", true)
	check("svc.internal", true)
	check("8.8.8.8", false)
	check("fd00::1", true)
	check("fe80::1", true)
}

// TestMatchNoProxyRule exercises the rule matcher directly, including the
// malformed-CIDR and CIDR-vs-hostname paths and dot-bounded suffix matching.
func TestMatchNoProxyRule(t *testing.T) {
	cases := []struct {
		rule string
		host string
		want bool
	}{
		{"10.0.0.0/8", "10.0.0.5", true},
		{"10.0.0.0/8", "8.8.8.8", false},
		{"10.0.0.0/999", "10.0.0.5", false},
		{"10.0.0.0/8", "example.com", false},
		{"example.com", "example.com", true},
		{".example.com", "api.example.com", true},
		{"example.com", "api.example.com", true},
		{"example.com", "notexample.com", false},
	}
	for _, c := range cases {
		if got := matchNoProxyRule(c.rule, c.host); got != c.want {
			t.Errorf("matchNoProxyRule(%q, %q) = %v; want %v", c.rule, c.host, got, c.want)
		}
	}
}

// TestProxyResolver_NoProxyConfigured covers the defensive guard in
// proxyResolver: with no proxy set it returns direct (nil) for any request.
func TestProxyResolver_NoProxyConfigured(t *testing.T) {
	resetProxy(t)
	if _, err := ConfigureOutboundProxy("", "", true); err != nil {
		t.Fatalf("configure: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if u, err := proxyResolver(req); u != nil || err != nil {
		t.Errorf("proxyResolver with no proxy = %v, %v; want nil, nil", u, err)
	}
}

// countingServer is an httptest server that counts the requests it serves.
func countingServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func guardedFetch(t *testing.T, policy Policy, target string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: GuardedTransport(policy), Timeout: 3 * time.Second}
	return client.Do(req)
}

// S3: with an outbound proxy configured, a destination on the bypass list is
// dialled directly, and that direct dial must still go through the policy
// check. Before the fix GuardedTransport returned the bare proxy transport, so
// a bypassed loopback / link-local target was reached with no dial-time check.
func TestGuardedTransport_ProxyBypassedHostIsPolicyChecked(t *testing.T) {
	resetProxy(t)
	proxySrv, proxyHits := countingServer(t)
	if _, err := ConfigureOutboundProxy(proxySrv.URL, "", true); err != nil {
		t.Fatalf("configure: %v", err)
	}
	target, targetHits := countingServer(t) // loopback: bypassed because bypassLocal is on

	resp, err := guardedFetch(t, PolicyStrict, target.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("bypassed loopback target under PolicyStrict was reached; want a policy refusal")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error = %v; want the loopback policy refusal", err)
	}
	if atomic.LoadInt32(targetHits) != 0 || atomic.LoadInt32(proxyHits) != 0 {
		t.Errorf("target hits %d, proxy hits %d; want 0 and 0", *targetHits, *proxyHits)
	}
}

// A no-proxy entry for a metadata address must be refused at dial time under
// every policy, including the permissive PolicyLANLoopback.
func TestGuardedTransport_ProxyNoProxyMetadataHostRefused(t *testing.T) {
	resetProxy(t)
	proxySrv, _ := countingServer(t)
	if _, err := ConfigureOutboundProxy(proxySrv.URL, "169.254.169.254", false); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for _, policy := range []Policy{PolicyStrict, PolicyLAN, PolicyLANLoopback} {
		resp, err := guardedFetch(t, policy, "http://169.254.169.254/latest/meta-data/")
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("policy %d: metadata address reached; want refusal", policy)
		}
		if !strings.Contains(err.Error(), "link-local") {
			t.Errorf("policy %d: error = %v; want the link-local refusal", policy, err)
		}
	}
}

// The dial to the proxy itself is operator configured and must not be checked
// against the policy: a loopback proxy under PolicyStrict still carries a
// proxied request. Without this the fix would break every LAN proxy.
func TestGuardedTransport_ProxyItselfIsNotPolicyChecked(t *testing.T) {
	resetProxy(t)
	proxySrv, proxyHits := countingServer(t)
	if _, err := ConfigureOutboundProxy(proxySrv.URL, "", false); err != nil {
		t.Fatalf("configure: %v", err)
	}
	resp, err := guardedFetch(t, PolicyStrict, "http://some-public-host.example/x")
	if err != nil {
		t.Fatalf("proxied request failed: %v", err)
	}
	_ = resp.Body.Close()
	if atomic.LoadInt32(proxyHits) != 1 {
		t.Errorf("proxy hits = %d; want 1", atomic.LoadInt32(proxyHits))
	}
}

// PolicyLANLoopback (download clients, Prowlarr) still reaches a bypassed LAN
// or loopback target directly.
func TestGuardedTransport_ProxyBypassedLoopbackAllowedUnderLANLoopback(t *testing.T) {
	resetProxy(t)
	proxySrv, proxyHits := countingServer(t)
	if _, err := ConfigureOutboundProxy(proxySrv.URL, "", true); err != nil {
		t.Fatalf("configure: %v", err)
	}
	target, targetHits := countingServer(t)
	resp, err := guardedFetch(t, PolicyLANLoopback, target.URL)
	if err != nil {
		t.Fatalf("bypassed loopback target under PolicyLANLoopback: %v", err)
	}
	_ = resp.Body.Close()
	if atomic.LoadInt32(targetHits) != 1 || atomic.LoadInt32(proxyHits) != 0 {
		t.Errorf("target hits %d, proxy hits %d; want 1 and 0", *targetHits, *proxyHits)
	}
}

func TestIsProxyAddr(t *testing.T) {
	resetProxy(t)
	cases := []struct {
		proxy, addr string
		want        bool
	}{
		{"http://proxy.lan:3128", "proxy.lan:3128", true},
		{"http://Proxy.LAN:3128", "proxy.lan:3128", true},
		{"http://proxy.lan", "proxy.lan:80", true},
		{"https://proxy.lan", "proxy.lan:443", true},
		{"socks5://proxy.lan", "proxy.lan:1080", true},
		{"http://proxy.lan:3128", "proxy.lan:3129", false},
		{"http://proxy.lan:3128", "other.lan:3128", false},
		{"http://[::1]:3128", "[::1]:3128", true},
	}
	for _, c := range cases {
		if _, err := ConfigureOutboundProxy(c.proxy, "", true); err != nil {
			t.Fatalf("configure %q: %v", c.proxy, err)
		}
		if got := isProxyAddr(c.addr); got != c.want {
			t.Errorf("isProxyAddr(%q) with proxy %q = %v; want %v", c.addr, c.proxy, got, c.want)
		}
	}
}

func directFetch(t *testing.T, target string, timeout time.Duration) (*http.Response, error) {
	t.Helper()
	client := &http.Client{
		Transport:     DirectGuardedTransport(PolicyLANLoopback),
		CheckRedirect: RedirectGuard(PolicyLANLoopback),
		Timeout:       timeout,
	}
	return client.Get(target)
}

// Download clients on loopback (127.0.0.1, localhost) and on the LAN are the
// normal case and must keep working under the control-client policy.
func TestDirectGuardedTransport_LoopbackAndLocalhostAllowed(t *testing.T) {
	srv, hits := countingServer(t)
	u := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := strings.Cut(u, ":")
	for _, target := range []string{srv.URL, "http://localhost:" + port} {
		resp, err := directFetch(t, target, 3*time.Second)
		if err != nil {
			t.Fatalf("GET %s: %v", target, err)
		}
		_ = resp.Body.Close()
	}
	if atomic.LoadInt32(hits) != 2 {
		t.Errorf("hits = %d; want 2", atomic.LoadInt32(hits))
	}
}

// RFC1918 and docker-bridge addresses are not refused by the policy: the dial
// proceeds and fails (or times out) on the network, not with a policy error.
func TestDirectGuardedTransport_PrivateLANNotPolicyRefused(t *testing.T) {
	for _, target := range []string{"http://192.168.250.250:9/", "http://172.30.250.250:9/", "http://10.250.250.250:9/"} {
		resp, err := directFetch(t, target, 300*time.Millisecond)
		if err == nil {
			_ = resp.Body.Close()
			continue
		}
		if strings.Contains(err.Error(), "not allowed") {
			t.Errorf("GET %s refused by policy: %v", target, err)
		}
	}
}

// The dial check itself lives in this package, so pin it here too: link-local
// and metadata addresses, in the forms a URL can spell them, are refused at
// connect time even under the permissive control-client policy.
func TestDirectGuardedTransport_LinkLocalAndMetadataRefusedAtDial(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/",
		"http://[::ffff:169.254.169.254]/",
		"http://[fd00:ec2::254]/",
		"http://[fe80::1]/",
		"http://0.0.0.0:9/",
	} {
		resp, err := directFetch(t, target, 2*time.Second)
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("GET %s succeeded; want a policy refusal", target)
			continue
		}
		if !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("GET %s: %v; want a policy refusal, not a network error", target, err)
		}
	}
}

// The control clients are intentionally left off BINDERY_OUTBOUND_PROXY.
func TestDirectGuardedTransport_IgnoresOutboundProxy(t *testing.T) {
	resetProxy(t)
	proxySrv, proxyHits := countingServer(t)
	if _, err := ConfigureOutboundProxy(proxySrv.URL, "", false); err != nil {
		t.Fatalf("configure: %v", err)
	}
	target, targetHits := countingServer(t)
	resp, err := directFetch(t, target.URL, 3*time.Second)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if atomic.LoadInt32(proxyHits) != 0 || atomic.LoadInt32(targetHits) != 1 {
		t.Errorf("proxy hits %d, target hits %d; want 0 and 1", *proxyHits, *targetHits)
	}
}

func TestDirectGuardedTransport_SharedPerPolicy(t *testing.T) {
	if DirectGuardedTransport(PolicyLANLoopback) != DirectGuardedTransport(PolicyLANLoopback) {
		t.Error("one transport (one connection pool) per policy expected")
	}
	if DirectGuardedTransport(PolicyLANLoopback) == DirectGuardedTransport(PolicyStrict) {
		t.Error("different policies must not share a transport")
	}
}

func TestRedirectGuard(t *testing.T) {
	var metadata, loopbackHop int32
	final, finalHits := countingServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-metadata":
			atomic.AddInt32(&metadata, 1)
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/to-loopback":
			atomic.AddInt32(&loopbackHop, 1)
			http.Redirect(w, r, final.URL, http.StatusFound)
		default: // endless loop
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)

	if resp, err := directFetch(t, srv.URL+"/to-metadata", 3*time.Second); err == nil {
		_ = resp.Body.Close()
		t.Error("redirect to the metadata address was followed")
	} else if !strings.Contains(err.Error(), "redirect blocked") || !strings.Contains(err.Error(), "link-local") {
		t.Errorf("error = %v; want redirect blocked with the link-local refusal", err)
	}

	resp, err := directFetch(t, srv.URL+"/to-loopback", 3*time.Second)
	if err != nil {
		t.Fatalf("redirect to a loopback target must be followed: %v", err)
	}
	_ = resp.Body.Close()
	if atomic.LoadInt32(finalHits) != 1 {
		t.Errorf("final hits = %d; want 1", atomic.LoadInt32(finalHits))
	}

	if resp, err := directFetch(t, srv.URL+"/loop", 3*time.Second); err == nil {
		_ = resp.Body.Close()
		t.Error("endless redirect loop was not stopped")
	} else if !strings.Contains(err.Error(), "10 redirects") {
		t.Errorf("error = %v; want the redirect cap", err)
	}
}
