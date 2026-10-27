package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsBookDownloadPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/v1/book/12/file":        true,
		"/opds/book/12/file":          true,
		"/api/v1/book/12":             false,
		"/api/v1/book/12/files":       false,
		"/api/v1/book//file":          false,
		"/api/v1/book/12/x/file":      false,
		"/api/v1/author/12/file":      false,
		"/api/v1/book":                false,
		"/bindery/api/v1/book/1/file": false,
	} {
		if got := isBookDownloadPath(p); got != want {
			t.Errorf("isBookDownloadPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// The book file routes serve text/plain for a .txt book, which the compressor
// would gzip. A resumed download asks for a byte range of the stored file, and
// a gzip Content-Encoding on that answer corrupts it.
func TestCompressUnlessDownload(t *testing.T) {
	body := strings.Repeat("some compressible text\n", 200)
	h := compressUnlessDownload(5)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	for _, tc := range []struct {
		path     string
		wantGzip bool
	}{
		{"/api/v1/book/1/file", false},
		{"/opds/book/1/file", false},
		{"/api/v1/book/1", true},
		{"/opds/recent", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("Content-Encoding") == "gzip"
			if got != tc.wantGzip {
				t.Fatalf("gzip = %v, want %v", got, tc.wantGzip)
			}
			if !tc.wantGzip && rec.Body.String() != body {
				t.Fatalf("body was altered")
			}
		})
	}
}
