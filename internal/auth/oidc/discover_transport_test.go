package oidc

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/httpsec"
)

// A caller with no DiscoverPolicy is the documented no-guardrails mode (an
// in-process test pointed at httptest.NewServer). It must keep getting the
// default transport, which http.Client reads a nil RoundTripper as.
func TestDiscoverTransport_NilPolicyKeepsDefault(t *testing.T) {
	if got := discoverTransport(nil); got != nil {
		t.Errorf("discoverTransport(nil) = %#v, want nil (http.Client's default transport)", got)
	}
}

// TestDiscoverTransport_DialGuardWhenDirect covers #2353. Discover validated
// the issuer up front and re-validated every redirect hop, but dialled without
// re-checking, so a name answering with a public address for the lookups and a
// private one for the connect got through both.
func TestDiscoverTransport_DialGuardWhenDirect(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	if _, err := httpsec.ConfigureOutboundProxy("", "", true); err != nil {
		t.Fatalf("ConfigureOutboundProxy reset: %v", err)
	}

	policy := httpsec.PolicyLAN
	got := discoverTransport(&policy)
	tr, ok := got.(*http.Transport)
	if !ok {
		t.Fatalf("discoverTransport = %T, want *http.Transport on the direct path", got)
	}
	if tr.DialContext == nil {
		t.Error("direct-path transport must install a DialContext (the rebind re-check)")
	}
	if got == httpsec.DefaultProxyTransport() {
		t.Error("direct-path transport must be a clone, not the shared default transport")
	}
}

// With an outbound proxy configured the dial targets the proxy rather than the
// IdP; that dial is exempt from the policy so a loopback or LAN proxy is not
// rejected by the guard meant for the IdP, and the request still arrives.
func TestDiscoverTransport_LoopbackProxyStillWorks(t *testing.T) {
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	var hits int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer proxy.Close()
	if _, err := httpsec.ConfigureOutboundProxy(proxy.URL, "", false); err != nil {
		t.Fatalf("ConfigureOutboundProxy: %v", err)
	}

	policy := httpsec.PolicyLAN
	client := &http.Client{Transport: discoverTransport(&policy)}
	resp, err := client.Get("http://idp.example.test/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("request through the loopback proxy: %v", err)
	}
	_ = resp.Body.Close()
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("proxy hits = %d; want 1", atomic.LoadInt32(&hits))
	}
}
