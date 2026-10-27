package httpsec

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The clone must be a clone: the shared default transport, and everything
// built off it, keeps its own idle limit, and what the base carries survives.
func TestMetadataTransport_ClonesBaseAndRaisesIdleConns(t *testing.T) {
	resetProxy(t)
	dt := http.DefaultTransport.(*http.Transport)
	defaultIdle := dt.MaxIdleConnsPerHost

	got, ok := MetadataTransport().(*http.Transport)
	if !ok {
		t.Fatalf("MetadataTransport() is %T, want *http.Transport", MetadataTransport())
	}
	if got == dt {
		t.Fatal("MetadataTransport() returned the shared default transport, not a clone")
	}
	if got.MaxIdleConnsPerHost != 16 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 16", got.MaxIdleConnsPerHost)
	}
	if dt.MaxIdleConnsPerHost != defaultIdle {
		t.Fatalf("default transport MaxIdleConnsPerHost changed %d -> %d", defaultIdle, dt.MaxIdleConnsPerHost)
	}
	if got != MetadataTransport() {
		t.Fatal("two calls returned different transports; a provider built per request would get its own idle pool")
	}
	if got.IdleConnTimeout != dt.IdleConnTimeout || got.TLSHandshakeTimeout != dt.TLSHandshakeTimeout ||
		got.MaxIdleConns != dt.MaxIdleConns || got.ForceAttemptHTTP2 != dt.ForceAttemptHTTP2 || got.Proxy == nil {
		t.Fatalf("clone dropped base fields: %+v", got)
	}
}

// Installing a proxy after the first call must not leave callers on a clone of
// the old, proxy-less base.
func TestMetadataTransport_FollowsTheBaseWhenItChanges(t *testing.T) {
	resetProxy(t)
	before := MetadataTransport().(*http.Transport)
	if _, err := ConfigureOutboundProxy("http://proxy.example:3128", "", true); err != nil {
		t.Fatal(err)
	}
	after := MetadataTransport().(*http.Transport)
	if after == before {
		t.Fatal("MetadataTransport kept the clone of the previous base")
	}
	if u, err := after.Proxy(httptest.NewRequest(http.MethodGet, "http://example.org/", nil)); err != nil || u == nil {
		t.Fatalf("new clone has no proxy: %v %v", u, err)
	}
	if again := MetadataTransport(); again != http.RoundTripper(after) {
		t.Fatal("repeat call did not reuse the clone")
	}
}

func TestMetadataTransport_KeepsProxyAndDialHook(t *testing.T) {
	resetProxy(t)
	if _, err := ConfigureOutboundProxy("http://proxy.example:3128", "", true); err != nil {
		t.Fatal(err)
	}
	// Give the proxied base a dial hook, as a guarded base would carry.
	base := outboundProxyTransport.(*http.Transport)
	var dialed atomic.Int32
	base.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dialed.Add(1)
		return nil, errors.New("blocked by test hook")
	}

	got := MetadataTransport().(*http.Transport)
	if got.DialContext == nil {
		t.Fatal("clone dropped the base DialContext")
	}
	if _, err := got.DialContext(context.Background(), "tcp", "x:1"); err == nil || dialed.Load() != 1 {
		t.Fatalf("clone does not use the base dial hook (err=%v dialed=%d)", err, dialed.Load())
	}
	u, err := got.Proxy(httptest.NewRequest(http.MethodGet, "http://example.org/", nil))
	if err != nil || u == nil || u.Host != "proxy.example:3128" {
		t.Fatalf("clone lost the outbound proxy: %v %v", u, err)
	}
}

// The guarded constructors are not touched by the idle pool change and still
// refuse a link-local address at dial time.
func TestMetadataTransport_DoesNotWeakenGuardedTransports(t *testing.T) {
	resetProxy(t)
	_ = MetadataTransport()
	for _, rt := range []http.RoundTripper{
		GuardedTransport(PolicyStrict),
		DirectGuardedTransport(PolicyLANLoopback),
	} {
		tr := rt.(*http.Transport)
		if tr.DialContext == nil {
			t.Fatalf("%T lost its dial guard", rt)
		}
		if tr.MaxIdleConnsPerHost == metadataIdleConnsPerHost {
			t.Fatal("guarded transport picked up the metadata idle setting")
		}
	}
	c := &http.Client{Transport: GuardedTransport(PolicyStrict), Timeout: 3 * time.Second}
	if resp, err := c.Get("http://169.254.169.254/latest/meta-data/"); err == nil {
		resp.Body.Close()
		t.Fatal("GuardedTransport(PolicyStrict) reached the link-local metadata address")
	}
}

// With the default idle limit of 2, a second burst of 8 finds two idle
// connections and dials six more; with 16 it reuses all eight.
func TestMetadataTransport_ReusesConnectionsAcrossBursts(t *testing.T) {
	resetProxy(t)
	const n = 8

	var mu sync.Mutex
	var inflight int
	release := make(chan struct{})
	reset := func() {
		mu.Lock()
		inflight = 0
		release = make(chan struct{})
		mu.Unlock()
	}
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Hold every request until all n are in flight so the burst really
		// needs n connections.
		mu.Lock()
		inflight++
		ch := release
		if inflight == n {
			close(ch)
		}
		mu.Unlock()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
		}
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	burst := func(c *http.Client) {
		reset()
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, err := c.Get(srv.URL)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}()
		}
		wg.Wait()
	}
	run := func(rt *http.Transport) int32 {
		conns.Store(0)
		c := &http.Client{Transport: rt}
		burst(c)
		time.Sleep(50 * time.Millisecond) // let the finished connections go idle
		burst(c)
		rt.CloseIdleConnections()
		return conns.Load()
	}

	if got := run(http.DefaultTransport.(*http.Transport).Clone()); got <= n {
		t.Fatalf("control (default idle limit) dialled %d connections, want more than %d; the premise no longer holds", got, n)
	}
	if got := run(MetadataTransport().(*http.Transport)); got != n {
		t.Fatalf("MetadataTransport dialled %d connections over two bursts of %d, want %d", got, n, n)
	}
}
