package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/vavallee/bindery/internal/logbuf"
)

// captureRing points the default slog logger at a fresh logbuf ring (the
// buffer the in-app log viewer reads) for the length of the test.
func captureRing(t *testing.T) *logbuf.Ring {
	t.Helper()
	ring := logbuf.New(50)
	prev := slog.Default()
	slog.SetDefault(slog.New(ring))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return ring
}

// served runs h inside RequestID, the order main.go uses.
func served(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	middleware.RequestID(Recoverer(h)).ServeHTTP(rec, req)
	return rec
}

func TestRecoverer_PanicIsJSON500WithRequestID(t *testing.T) {
	ring := captureRing(t)
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom: apikey=SECRETKEY123 at /srv/internal/db.sqlite")
	})
	rec := served(boom, httptest.NewRequest(http.MethodGet, "/api/v1/book?x=1", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["error"] != "internal server error" {
		t.Errorf("error = %q", body["error"])
	}
	rid := body["request_id"]
	if rid == "" {
		t.Fatalf("no request_id in %s", rec.Body.String())
	}
	for _, leak := range []string{"kaboom", "SECRETKEY123", "db.sqlite", "goroutine", ".go:"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("body leaks %q: %s", leak, rec.Body.String())
		}
	}

	// The record lands in the in-app log buffer with the same id and a stack.
	var found *logbuf.Entry
	for _, e := range ring.Snapshot(slog.LevelError, 10) {
		if e.Attrs["request_id"] == rid {
			found = &e
		}
	}
	if found == nil {
		t.Fatalf("no error record with request_id %q in the log ring: %+v", rid, ring.Snapshot(slog.LevelDebug, 10))
	}
	if !strings.Contains(found.Attrs["panic"], "kaboom") {
		t.Errorf("panic value not logged: %v", found.Attrs)
	}
	if !strings.Contains(found.Attrs["stack"], "recoverer_test.go") {
		t.Errorf("stack not logged: %q", found.Attrs["stack"])
	}
	if found.Attrs["path"] != "/api/v1/book" {
		t.Errorf("path = %q, want the path without the query string", found.Attrs["path"])
	}
}

func TestRecoverer_AbortHandlerRepanics(t *testing.T) {
	captureRing(t)
	defer func() {
		if rvr := recover(); rvr != http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, not an error chain
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked", rvr)
		}
	}()
	served(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }),
		httptest.NewRequest(http.MethodGet, "/", nil))
	t.Fatal("ErrAbortHandler was swallowed")
}

func TestRecoverer_KeepsAStartedResponse(t *testing.T) {
	captureRing(t)
	rec := served(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial"))
		panic("late")
	}), httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusAccepted || rec.Body.String() != "partial" {
		t.Fatalf("got %d %q, want the response that had already started", rec.Code, rec.Body.String())
	}
}

func TestRecoverer_UpgradeRequestGetsNoBody(t *testing.T) {
	captureRing(t)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	rec := served(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }), req)
	if rec.Body.Len() != 0 {
		t.Fatalf("upgrade request got a body: %q", rec.Body.String())
	}
}

func TestRecoverer_PassesThroughNormalResponsesAndFlusher(t *testing.T) {
	var flushable bool
	rec := served(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, flushable = w.(http.Flusher)
		_, _ = w.Write([]byte("ok"))
	}), httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if !flushable {
		t.Error("the wrapped writer no longer implements http.Flusher")
	}
}
