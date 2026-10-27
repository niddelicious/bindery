package main

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

// compressUnlessDownload is middleware.Compress(level) minus the book file
// routes. Those serve audio, ebooks and zip archives that are compressed
// already, and a text file (compressible by content type) must not be gzipped
// either: http.ServeContent answers a Range request with the byte range of the
// stored file, so a Content-Encoding on a 206 describes bytes that are not
// gzip, and the wrapper also hides the file from the zero copy path.
func compressUnlessDownload(level int) func(http.Handler) http.Handler {
	compress := middleware.Compress(level)
	return func(next http.Handler) http.Handler {
		compressed := compress(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isBookDownloadPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			compressed.ServeHTTP(w, r)
		})
	}
}

// isBookDownloadPath reports whether p, as the router sees it (the URL base is
// already stripped), is the book file download in either API.
func isBookDownloadPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/api/v1/book/")
	if !ok {
		rest, ok = strings.CutPrefix(p, "/opds/book/")
	}
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(rest, "/file")
	return ok && id != "" && !strings.Contains(id, "/")
}
