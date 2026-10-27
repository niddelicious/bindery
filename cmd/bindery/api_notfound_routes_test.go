package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// errorShapeRouter mirrors the production topology around the API: a root
// router whose "/*" is the SPA fallback, the /api and /api/v1 sub-routers
// that carry the JSON 404/405 handlers, and the sibling mounts (/auth,
// /opds, /bridge) that must keep answering the way they always have.
func errorShapeRouter() chi.Router {
	r := chi.NewRouter()
	ok := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
	}
	r.Route("/api", func(r chi.Router) {
		registerAPIErrorHandlers(r)
		r.Get("/queue", ok("arr queue"))
	})
	r.Route("/api/v1", func(r chi.Router) {
		registerAPIErrorHandlers(r)
		r.Get("/health", ok(`{"status":"ok"}`))
		r.Group(func(r chi.Router) {
			r.Get("/book", ok("books"))
			r.Post("/book", ok("created"))
			r.Put("/book/{id}", ok("updated"))
		})
	})
	r.Get("/auth/status", ok("auth"))
	r.Route("/opds", func(r chi.Router) { r.Get("/", ok("opds root")) })
	r.Route("/bridge/v1", func(r chi.Router) { r.Get("/ping", ok("bridge")) })
	// The SPA fallback, as in main.go: GET only, so it is the root's catch-all.
	r.Get("/*", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>spa</html>"))
	})
	return r
}

func serve(r http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d (body %q)", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body["error"] != msg {
		t.Errorf("error = %q, want %q", body["error"], msg)
	}
}

func TestAPIUnknownRouteIsJSON404(t *testing.T) {
	r := errorShapeRouter()
	for _, path := range []string{"/api/v1/nope", "/api/v1/book/1/nope", "/api/nope", "/api/v1/"} {
		t.Run(path, func(t *testing.T) {
			assertJSONError(t, serve(r, http.MethodGet, path), http.StatusNotFound, "not found")
		})
	}
}

func TestAPIWrongMethodIsJSON405WithAllow(t *testing.T) {
	r := errorShapeRouter()
	cases := []struct {
		method, path, allow string
	}{
		{http.MethodPost, "/api/v1/health", "GET"},
		{http.MethodDelete, "/api/v1/book", "GET, POST"},
		{http.MethodPost, "/api/v1/book/7", "PUT"},
		{http.MethodPost, "/api/queue", "GET"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := serve(r, c.method, c.path)
			assertJSONError(t, rec, http.StatusMethodNotAllowed, "method not allowed")
			got := strings.Split(rec.Header().Get("Allow"), ", ")
			sort.Strings(got)
			if strings.Join(got, ", ") != c.allow {
				t.Errorf("Allow = %q, want %q", rec.Header().Get("Allow"), c.allow)
			}
		})
	}
}

// The handlers are scoped to the API sub-routers. Everything else keeps its
// behaviour: the SPA fallback still serves HTML for client-side routes, and
// the sibling mounts answer as before.
func TestNonAPIRoutesKeepTheirBehaviour(t *testing.T) {
	r := errorShapeRouter()

	for _, path := range []string{"/", "/books/12", "/settings/indexers", "/no/such/page"} {
		rec := serve(r, http.MethodGet, path)
		if rec.Code != http.StatusOK || rec.Body.String() != "<html>spa</html>" ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %s = %d %q (%s), want the SPA", path, rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
		}
	}

	for path, want := range map[string]string{"/auth/status": "auth", "/opds/": "opds root", "/bridge/v1/ping": "bridge"} {
		rec := serve(r, http.MethodGet, path)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}

	// Unknown paths under the sibling mounts are not the API's JSON 404.
	for _, path := range []string{"/opds/nope", "/bridge/v1/nope"} {
		rec := serve(r, http.MethodGet, path)
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("GET %s answered JSON: %q", path, rec.Body.String())
		}
	}

	// A wrong method on the SPA catch-all keeps chi's bare 405.
	rec := serve(r, http.MethodPost, "/books/12")
	if rec.Code != http.StatusMethodNotAllowed || strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("POST /books/12 = %d (%s), want chi's plain 405", rec.Code, rec.Header().Get("Content-Type"))
	}
}
