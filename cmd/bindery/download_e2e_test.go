package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/models"
)

// downloadServer serves the book file route behind the real router middleware
// (useCoreMiddleware, the stack main.go installs) on a real http.Server whose
// WriteTimeout is one second. The book's path is whatever the caller made.
func downloadServer(t *testing.T, mk func(root string) string) *httptest.Server {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	books := db.NewBookRepo(database)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "A", SortName: "A"}
	if err := db.NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	book := &models.Book{ForeignID: "OL1W", AuthorID: author.ID, Title: "T"}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if err := books.SetFilePath(ctx, book.ID, mk(root)); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	useCoreMiddleware(r, "")
	r.Get("/api/v1/book/{id}/file", api.NewFileHandler(books, root).Download)

	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = time.Second
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// sparseFile makes a size byte file that costs no disk.
func sparseFile(t *testing.T, p string, size int64) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// slowRead drains body in chunks, pausing between them, and returns what it got.
func slowRead(t *testing.T, body io.Reader) []byte {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, 512<<10)
	for {
		n, err := io.ReadFull(body, buf)
		out.Write(buf[:n])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return out.Bytes()
		}
		if err != nil {
			t.Fatalf("read after %d bytes: %v", out.Len(), err)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

const e2eSize = 32 << 20

// A response longer than the server WriteTimeout used to be cut off at it. The
// whole path counts: the file has to be big enough that the kernel buffers
// cannot swallow it, the client slow enough that the transfer outlasts the
// timeout, and every wrapper between the server and the handler has to hand
// http.NewResponseController through to the server's own writer.
func TestDownloadOutlastsServerWriteTimeout(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		srv := downloadServer(t, func(root string) string {
			p := filepath.Join(root, "book.m4b")
			sparseFile(t, p, e2eSize)
			return p
		})
		start := time.Now()
		resp, err := http.Get(srv.URL + "/api/v1/book/1/file")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got := slowRead(t, resp.Body)
		if len(got) != e2eSize {
			t.Fatalf("got %d of %d bytes", len(got), e2eSize)
		}
		if time.Since(start) < 1500*time.Millisecond {
			t.Fatalf("transfer took %v, too quick to prove anything about a 1 s WriteTimeout", time.Since(start))
		}
	})

	t.Run("zip", func(t *testing.T) {
		srv := downloadServer(t, func(root string) string {
			dir := filepath.Join(root, "Título")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			sparseFile(t, filepath.Join(dir, "capítulo 1.mp3"), e2eSize)
			if err := os.WriteFile(filepath.Join(dir, "info.txt"), []byte("hello"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		})
		resp, err := http.Get(srv.URL + "/api/v1/book/1/file")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.ContentLength != -1 {
			t.Fatalf("zip Content-Length = %d, want unknown (chunked)", resp.ContentLength)
		}
		got := slowRead(t, resp.Body)
		zr, err := zip.NewReader(bytes.NewReader(got), int64(len(got)))
		if err != nil {
			t.Fatalf("zip of %d bytes: %v", len(got), err)
		}
		if len(zr.File) != 2 {
			t.Fatalf("zip has %d entries, want 2", len(zr.File))
		}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			n, err := io.Copy(io.Discard, rc) // checks the CRC
			_ = rc.Close()
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if f.Name == "capítulo 1.mp3" && (n != e2eSize || f.Method != zip.Store) {
				t.Errorf("%s: %d bytes, method %d", f.Name, n, f.Method)
			}
		}
	})
}

// Resume requests go through the same stack as the full download.
func TestDownloadRangeThroughRealStack(t *testing.T) {
	srv := downloadServer(t, func(root string) string {
		p := filepath.Join(root, "book.txt") // text/plain: the compressor would take it
		if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	})
	do := func(method string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+"/api/v1/book/1/file", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		tr := &http.Transport{DisableCompression: true}
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := do(http.MethodGet, map[string]string{"Range": "bytes=3-6"})
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "3456" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 3-6/10" {
		t.Fatalf("Content-Range = %q", cr)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Fatalf("Content-Encoding = %q on a ranged download", ce)
	}

	// If-Range with a validator that does not match answers the whole file.
	resp = do(http.MethodGet, map[string]string{"Range": "bytes=3-6", "If-Range": `"nope"`})
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "0123456789" {
		t.Fatalf("stale If-Range: %d %q", resp.StatusCode, body)
	}
}
