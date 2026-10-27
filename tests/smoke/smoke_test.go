// Package smoke boots the real bindery binary and exercises the critical
// golden paths via HTTP. It catches wiring regressions — bad route
// registration, missing migration, broken frontend embed — that per-package
// unit tests can't see.
//
// Requires a pre-built binary. Set BINDERY_BINARY to override the default
// location (repo root `./bindery`). Typically invoked via `make smoke`,
// which builds the binary (including frontend assets) first.
//
// The whole suite ships as a single top-level test so binary boot/teardown
// happens once. Subtests are cheap HTTP calls against the same process.
package smoke_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	apiKey      = "smoke-test-key-0000000000000000"
	bootTimeout = 20 * time.Second
	httpTimeout = 5 * time.Second
)

func TestSmoke(t *testing.T) {
	bin := resolveBinary(t)
	tmp := t.TempDir()

	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := exec.Command(bin)
	// Clean env: drop BINDERY_* from the outer shell so a developer's
	// personal config (e.g. BINDERY_PUID set for their homelab) can't make
	// the smoke run diverge from CI. We explicitly set every variable the
	// binary needs.
	cmd.Env = sanitizedEnv(
		"BINDERY_PORT="+fmt.Sprint(port),
		"BINDERY_DB_PATH="+filepath.Join(tmp, "bindery.db"),
		"BINDERY_DATA_DIR="+tmp,
		"BINDERY_API_KEY="+apiKey,
		"BINDERY_LIBRARY_DIR="+filepath.Join(tmp, "library"),
		"BINDERY_DOWNLOAD_DIR="+filepath.Join(tmp, "downloads"),
		"BINDERY_LOG_LEVEL=error",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("start bindery: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Logf("bindery stdout/stderr:\n%s", out.String())
		}
	})

	if err := waitForHealth(base, bootTimeout); err != nil {
		t.Fatalf("health never green: %v\nbindery output:\n%s", err, out.String())
	}

	t.Run("authors list empty on fresh install", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/author")
		// List endpoints return a Page envelope after Wave 2 / E.
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
		if len(page.Items) != 0 {
			t.Errorf("expected empty authors, got %d", len(page.Items))
		}
	})

	t.Run("books list returns an array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/book")
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("settings list returns an array", func(t *testing.T) {
		body := getJSON(t, base+"/api/v1/setting")
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("decode: %v (body=%s)", err, body)
		}
	})

	t.Run("calibre import 400s when library unconfigured", func(t *testing.T) {
		req := mustReq(t, http.MethodPost, base+"/api/v1/calibre/import", nil)
		resp := do(t, req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 400, got %d (body=%s)", resp.StatusCode, b)
		}
	})

	t.Run("api auth rejects missing key", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/author")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("embedded frontend serves index.html", func(t *testing.T) {
		resp, err := http.Get(base + "/")
		if err != nil {
			t.Fatalf("get /: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(strings.ToLower(string(body)), "<!doctype html") {
			t.Errorf("/ did not return an HTML document — was the binary built without web assets?\ngot: %s", truncate(body, 200))
		}
	})

	// JSON 404 and 405 on the API trees, and the SPA fallback left alone.
	t.Run("unknown API routes answer JSON errors", func(t *testing.T) {
		for _, tc := range []struct {
			method, path string
			status       int
			message      string
			allow        string
		}{
			{http.MethodGet, "/api/v1/nope", http.StatusNotFound, "not found", ""},
			{http.MethodGet, "/api/nope", http.StatusNotFound, "not found", ""},
			{http.MethodPost, "/api/v1/health", http.StatusMethodNotAllowed, "method not allowed", "GET"},
		} {
			resp := do(t, mustReq(t, tc.method, base+tc.path, nil))
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var got map[string]string
			if resp.StatusCode != tc.status || json.Unmarshal(body, &got) != nil || got["error"] != tc.message ||
				!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
				t.Errorf("%s %s = %d %q (%s), want %d JSON error %q", tc.method, tc.path,
					resp.StatusCode, body, resp.Header.Get("Content-Type"), tc.status, tc.message)
			}
			if resp.Header.Get("Allow") != tc.allow {
				t.Errorf("%s %s Allow = %q, want %q", tc.method, tc.path, resp.Header.Get("Allow"), tc.allow)
			}
		}
		// Unauthenticated probes still meet the auth layer first.
		resp, err := http.Get(base + "/api/v1/nope")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauthenticated unknown route = %d, want 401", resp.StatusCode)
		}
		// A client-side route still gets the SPA, not JSON.
		resp, err = http.Get(base + "/books/12")
		if err != nil {
			t.Fatal(err)
		}
		page, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(string(page)), "<!doctype html") {
			t.Errorf("/books/12 = %d %s, want the SPA", resp.StatusCode, truncate(page, 80))
		}
	})

	// The requester allow list can only be proven against the real router,
	// which main() builds inline: sign in as a requester and try routes on
	// both API trees and OPDS.
	t.Run("requester is held to the allow list", func(t *testing.T) {
		smokeRequester(t, base)
	})
}

// smokeRequester creates an admin through first run setup, a requester
// through the admin API, signs the requester in with a cookie, and checks a
// grab, a library read, the Arr queue and OPDS are refused while the
// requester's own routes answer.
func smokeRequester(t *testing.T, base string) {
	t.Helper()
	post := func(url string, body any) *http.Response {
		b, _ := json.Marshal(body)
		req := mustReq(t, http.MethodPost, url, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		return do(t, req)
	}
	resp := post(base+"/api/v1/auth/setup", map[string]string{"username": "smoke-admin", "password": "smoke-admin-password"})
	resp.Body.Close()
	resp = post(base+"/api/v1/auth/users", map[string]string{"username": "smoke-reader", "password": "smoke-reader-password", "role": "requester"})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create requester: %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: httpTimeout}
	send := func(method, url string, body []byte, csrf string) *http.Response {
		req, err := http.NewRequest(method, url, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "bindery-ui")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		return r
	}
	login, _ := json.Marshal(map[string]any{"username": "smoke-reader", "password": "smoke-reader-password"})
	resp = send(http.MethodPost, base+"/api/v1/auth/login", login, "")
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("requester login: %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	resp = send(http.MethodGet, base+"/api/v1/auth/csrf", nil, "")
	var tok struct {
		CSRFToken string `json:"csrfToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if tok.CSRFToken == "" {
		t.Fatal("no CSRF token for the requester session")
	}

	for _, c := range []struct {
		method, path string
		body         []byte
	}{
		{http.MethodPost, "/api/v1/queue/grab", []byte(`{}`)},
		{http.MethodGet, "/api/v1/author", nil},
		{http.MethodGet, "/api/v1/book/1/file", nil},
		{http.MethodGet, "/api/v1/setting", nil},
		{http.MethodGet, "/api/queue", nil},
		{http.MethodGet, "/api/v1//queue", nil},
		{http.MethodGet, "/api/v1/book/1%2Ffile", nil},
	} {
		r := send(c.method, base+c.path, c.body, tok.CSRFToken)
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("requester %s %s: %d %s, want 403", c.method, c.path, r.StatusCode, truncate(b, 200))
		}
	}
	for _, path := range []string{"/api/v1/requests", "/api/v1/requests/library", "/api/v1/auth/status"} {
		r := send(http.MethodGet, base+path, nil, "")
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("requester GET %s: %d, want 200", path, r.StatusCode)
		}
	}

	// OPDS by Basic credentials, which is how a reading app signs in.
	req, _ := http.NewRequest(http.MethodGet, base+"/opds/", nil)
	req.SetBasicAuth("smoke-reader", "smoke-reader-password")
	r, err := (&http.Client{Timeout: httpTimeout}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("requester OPDS: %d, want 403", r.StatusCode)
	}
}

// resolveBinary locates the bindery binary the test will drive. Search order:
//  1. $BINDERY_BINARY if set
//  2. repo-root ./bindery (two levels up from tests/smoke)
//  3. ./bindery relative to cwd
//
// When none of those exist the test is skipped with an actionable hint.
// `make smoke` is responsible for building it first.
func resolveBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if p := os.Getenv("BINDERY_BINARY"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, "../../bindery", "./bindery")
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			return abs
		}
	}
	t.Skip("bindery binary not found — run `make smoke` (builds first) or set BINDERY_BINARY to an existing binary path")
	return ""
}

// sanitizedEnv returns the current environment with all BINDERY_* variables
// removed, then appends the supplied overrides. This prevents a developer's
// personal env (BINDERY_PUID, BINDERY_DB_PATH pointing at their real install)
// from leaking into the smoke-test sandbox.
func sanitizedEnv(overrides ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BINDERY_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, overrides...)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForHealth(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("no 200 from /api/v1/health within %s: %w", timeout, lastErr)
}

func getJSON(t *testing.T, url string) []byte {
	t.Helper()
	req := mustReq(t, http.MethodGet, url, nil)
	resp := do(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d (body=%s)", url, resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

func mustReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Api-Key", apiKey)
	return req
}

func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(req.Context(), httpTimeout)
	t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	return resp
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
