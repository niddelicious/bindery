package main

import (
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

// staticContentTypes covers files in the embedded frontend whose extension
// Go's mime table does not know. Without an entry http.FileServer sniffs the
// body, and a web app manifest comes back as text/plain, which the
// X-Content-Type-Options: nosniff header then pins.
var staticContentTypes = map[string]string{
	".webmanifest": "application/manifest+json",
}

// staticAssetExts are extensions only a build asset carries. A client side
// route never ends in one, so a miss on such a path is a missing file.
var staticAssetExts = map[string]bool{
	".js":   true,
	".mjs":  true,
	".css":  true,
	".map":  true,
	".wasm": true,
}

// isStaticAssetPath reports whether p (relative to the frontend root, no
// leading slash) names a build asset rather than a client side route. A miss
// on one must 404: answering with index.html makes a tab still running the
// previous build import an HTML page as a script chunk, which WebKit rejects
// as "'text/html' is not a valid JavaScript MIME type." instead of the
// failed fetch the reload guard in lazyWithReload.ts looks for.
func isStaticAssetPath(p string) bool {
	return strings.HasPrefix(p, "assets/") || staticAssetExts[strings.ToLower(path.Ext(p))]
}

// hashedAssetName matches the file names Vite emits for everything under
// assets/ (name-<hash>.ext, the hash being 8 base64url characters). Chunks,
// the CSS bundle, the lazily loaded locale files and imported images all come
// out this way, so the URL changes whenever the content does.
var hashedAssetName = regexp.MustCompile(`^.+-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]+$`)

// immutableCacheControl lets the browser keep a content hashed file for a year
// without asking again.
const immutableCacheControl = "public, max-age=31536000, immutable"

// isHashedAssetPath reports whether p (relative to the frontend root, no
// leading slash) is a build output whose name carries a content hash, which
// is the only kind of file that is safe to cache forever. Files copied from
// web/public (favicon, manifest, theme-bootstrap.js) keep their name across
// builds, and index.html is the one file that names the current hashes.
func isHashedAssetPath(p string) bool {
	return strings.HasPrefix(p, "assets/") && hashedAssetName.MatchString(path.Base(p))
}

// spaHandler serves the embedded frontend: index.html (with the <base> tag
// already injected) for the root and for any path that is not a real file,
// so client side routes survive a reload, and the file itself otherwise.
// Missing build assets get a 404 instead of the app shell.
func spaHandler(distFS fs.FS, indexHTML []byte) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(distFS))
	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		_, _ = w.Write(indexHTML)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path[1:]
		if p == "" || p == "index.html" {
			serveIndex(w)
			return
		}
		if _, err := fs.Stat(distFS, p); err == nil {
			if ct, ok := staticContentTypes[path.Ext(p)]; ok {
				// http.FileServer keeps a Content-Type that is already set.
				w.Header().Set("Content-Type", ct)
			}
			if isHashedAssetPath(p) {
				w.Header().Set("Cache-Control", immutableCacheControl)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if isStaticAssetPath(p) {
			// The fallback below would hand a stale chunk import an HTML
			// page with a 200; a plain 404 fails the import cleanly.
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			http.NotFound(w, r)
			return
		}
		// SPA fallback: unknown paths render the app shell.
		serveIndex(w)
	}
}
