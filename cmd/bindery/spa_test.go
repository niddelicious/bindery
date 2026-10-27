package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
)

const testManifest = `{"name":"Bindery","start_url":".","scope":"."}`

// spaTestServer wires spaHandler the way main() does: behind the security
// headers and, when urlBase is set, under the URL base mount.
func spaTestServer(urlBase string) http.Handler {
	dist := fstest.MapFS{
		"index.html":            {Data: []byte(`<!doctype html><html><head></head><body></body></html>`)},
		"manifest.webmanifest":  {Data: []byte(testManifest)},
		"apple-touch-icon.png":  {Data: []byte("\x89PNG\r\n\x1a\n")},
		"icon-192.png":          {Data: []byte("\x89PNG\r\n\x1a\n")},
		"icon-maskable-512.png": {Data: []byte("\x89PNG\r\n\x1a\n")},
	}
	r := chi.NewRouter()
	r.Use(api.SecurityHeaders(""))
	r.Get("/*", spaHandler(dist, []byte(injectBaseHTML(`<html><head></head></html>`, urlBase))))
	return mountUnderURLBase(r, urlBase)
}

func TestSPAHandler_ServesManifest(t *testing.T) {
	for _, base := range []string{"", "/bindery"} {
		t.Run("base="+base, func(t *testing.T) {
			rec := httptest.NewRecorder()
			spaTestServer(base).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+"/manifest.webmanifest", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			// Go's mime table has no .webmanifest entry, so without the override
			// this sniffs as text/plain and nosniff pins it there.
			if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
				t.Fatalf("Content-Type = %q, want application/manifest+json", ct)
			}
			if rec.Body.String() != testManifest {
				t.Fatalf("body = %q, want the manifest, not the SPA fallback", rec.Body.String())
			}
			// manifest-src falls back to default-src 'self', which allows a
			// same-origin manifest. A manifest-src directive added later must
			// not lock it out.
			csp := rec.Header().Get("Content-Security-Policy")
			if !strings.Contains(csp, "default-src 'self'") {
				t.Fatalf("CSP lost default-src 'self': %q", csp)
			}
			if strings.Contains(csp, "manifest-src") && !strings.Contains(csp, "manifest-src 'self'") {
				t.Fatalf("CSP manifest-src does not allow 'self': %q", csp)
			}
		})
	}
}

func TestSPAHandler_ServesIcons(t *testing.T) {
	for _, name := range []string{"apple-touch-icon.png", "icon-192.png", "icon-maskable-512.png"} {
		rec := httptest.NewRecorder()
		spaTestServer("/bindery").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bindery/"+name, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", name, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Fatalf("%s: Content-Type = %q, want image/png", name, ct)
		}
	}
}

func TestSPAHandler_FallsBackToIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	spaTestServer("/bindery").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/bindery/author/10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), `<base href="/bindery/">`) {
		t.Fatalf("fallback did not serve the injected index: %q", rec.Body.String())
	}
}

// A tab still running the previous build asks for chunk names the new build
// no longer has. Answering with the app shell (200 text/html) made iOS
// WebKit reject the import as "'text/html' is not a valid JavaScript MIME
// type." instead of reloading; a missing asset must 404.
func TestSPAHandler_MissingAssetIs404(t *testing.T) {
	for _, base := range []string{"", "/bindery"} {
		for _, p := range []string{
			"/assets/BooksPage-abc123.js",
			"/assets/index-abc123.css",
			"/assets/nested/missing.png",
			"/assets/",
			"/stale-chunk-abc123.js",
			"/workbox-abc123.mjs",
			"/assets/BooksPage-abc123.js.map",
		} {
			t.Run("base="+base+p, func(t *testing.T) {
				rec := httptest.NewRecorder()
				spaTestServer(base).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+p, nil))
				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want 404", rec.Code)
				}
				if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
					t.Fatalf("Content-Type = %q, want anything but the HTML app shell", ct)
				}
				if strings.Contains(rec.Body.String(), "<html") {
					t.Fatalf("body is the app shell: %q", rec.Body.String())
				}
			})
		}
	}
}

func TestSPAHandler_RoutesStillFallBackToIndex(t *testing.T) {
	for _, base := range []string{"", "/bindery"} {
		for _, p := range []string{"/", "/books", "/author/10", "/settings/calibre", "/assetsmanager", "/book/12/assets"} {
			t.Run("base="+base+p, func(t *testing.T) {
				rec := httptest.NewRecorder()
				spaTestServer(base).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+p, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200", rec.Code)
				}
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
					t.Fatalf("Content-Type = %q, want text/html", ct)
				}
			})
		}
	}
}

func TestSPAHandler_ExistingAssetIsServed(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":                 {Data: []byte(`<!doctype html>`)},
		"assets/BooksPage-abc123.js": {Data: []byte("export default 1\n")},
	}
	rec := httptest.NewRecorder()
	spaHandler(dist, []byte("<html></html>")).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/BooksPage-abc123.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, want a JavaScript type", ct)
	}
}

const (
	immutableCache = "public, max-age=31536000, immutable"
	noStoreCache   = "no-cache, no-store, must-revalidate"
)

// Vite names every file under assets/ name-<hash>.ext, so the URL changes
// whenever the content does and the browser may keep it for good. Anything
// else, including files at the root and an unhashed file that happens to sit
// under assets/, must stay revalidated or an upgrade would not reach users.
func TestSPAHandler_CacheControl(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":                  {Data: []byte(`<!doctype html>`)},
		"assets/index-BAKpjB06.js":    {Data: []byte("export default 1\n")},
		"assets/index-Cx9_-Qz1.css":   {Data: []byte("a{}\n")},
		"assets/nb-BHWsVc0a.js":       {Data: []byte("export default {}\n")},
		"assets/sub/page-BAKpjB06.js": {Data: []byte("x\n")},
		"assets/logo.png":             {Data: []byte("\x89PNG\r\n\x1a\n")},
		"assets/notes.js":             {Data: []byte("x\n")},
		"favicon.png":                 {Data: []byte("\x89PNG\r\n\x1a\n")},
		"manifest.webmanifest":        {Data: []byte(testManifest)},
		"theme-bootstrap.js":          {Data: []byte("x\n")},
		"decoy-BAKpjB06.js":           {Data: []byte("x\n")},
	}
	h := spaHandler(dist, []byte("<html></html>"))
	cases := []struct {
		path string
		want string
	}{
		{"/assets/index-BAKpjB06.js", immutableCache},
		{"/assets/index-Cx9_-Qz1.css", immutableCache},
		{"/assets/nb-BHWsVc0a.js", immutableCache},
		{"/assets/sub/page-BAKpjB06.js", immutableCache},
		{"/assets/logo.png", ""},
		{"/assets/notes.js", ""},
		{"/favicon.png", ""},
		{"/manifest.webmanifest", ""},
		{"/theme-bootstrap.js", ""},
		{"/decoy-BAKpjB06.js", ""},
		{"/", noStoreCache},
		{"/index.html", noStoreCache},
		{"/books", noStoreCache},
		{"/assets/gone-BAKpjB06.js", noStoreCache},
	}
	for _, base := range []string{"", "/bindery"} {
		var handler http.Handler = h
		if base != "" {
			handler = http.StripPrefix(base, h)
		}
		for _, tc := range cases {
			t.Run("base="+base+tc.path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+tc.path, nil))
				if got := rec.Header().Get("Cache-Control"); got != tc.want {
					t.Fatalf("Cache-Control = %q, want %q (status %d)", got, tc.want, rec.Code)
				}
			})
		}
	}
}
