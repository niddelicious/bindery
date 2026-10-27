package sabnzbd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// S4: the control client must not follow a redirect to a link-local or cloud
// metadata address, and must not dial one even when the configured host is one.
// Link-local is blocked under every policy, including the permissive
// PolicyLANLoopback the control client uses, while loopback and LAN targets (the
// normal case, and what every other test in this package relies on) still work.

func redirectToMetadataServer(t *testing.T) (string, int, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), port, &hits
}

func TestClient_RedirectToMetadataRefused(t *testing.T) {
	host, port, hits := redirectToMetadataServer(t)
	c := New(host, port, "key", "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Test(ctx)
	if err == nil {
		t.Fatal("Test followed a redirect to the metadata address without error")
	}
	if atomic.LoadInt32(hits) == 0 {
		t.Fatalf("the loopback test server was never reached (the loopback target must stay allowed): %v", err)
	}
	if !strings.Contains(err.Error(), "link-local") {
		t.Errorf("error = %v; want the link-local refusal", err)
	}
	// The redirect hop itself is refused (CheckRedirect), not just the later connect.
	if !strings.Contains(err.Error(), "redirect blocked") {
		t.Errorf("error = %v; want the redirect refused at the hop", err)
	}
}

func TestClient_LinkLocalHostRefusedAtDial(t *testing.T) {
	c := New("169.254.169.254", 8080, "key", "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Test(ctx)
	if err == nil {
		t.Fatal("Test reached a link-local host without error")
	}
	if !strings.Contains(err.Error(), "link-local") {
		t.Errorf("error = %v; want the link-local refusal", err)
	}
}
