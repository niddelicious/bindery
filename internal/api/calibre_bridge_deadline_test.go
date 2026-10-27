package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The Calibre bridge serves the same large files, so it gets the same rolling
// write deadline, and only once the delivery row and path have been accepted.
func TestBridgeFile_ExtendsWriteDeadline(t *testing.T) {
	f := newBridgeFixture(t)
	good := filepath.Join(f.root, "Jane Austen", "Emma (1815).epub")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.outside, "secret.epub")
	_, ids := f.book("Emma", good)
	_, outsideIDs := f.book("Outside", outside)

	serve := func(id int64) *deadlineWriter {
		req := httptest.NewRequest(http.MethodGet, "/bridge/v1/deliveries/"+strconv.FormatInt(id, 10)+"/file", nil)
		req.Header.Set("Authorization", "Bearer "+testBridgeKey)
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		f.router.ServeHTTP(w, req)
		return w
	}
	w := serve(ids[0])
	if w.Code != http.StatusOK || w.calls == 0 || !w.deadline.After(time.Now().Add(downloadStallTimeout-time.Minute)) {
		t.Fatalf("served file: status %d, deadline calls %d value %v", w.Code, w.calls, w.deadline)
	}
	if w := serve(outsideIDs[0]); w.Code != http.StatusForbidden || w.calls != 0 {
		t.Fatalf("refused path: status %d, deadline calls %d (an error response keeps the server deadline)", w.Code, w.calls)
	}
}
