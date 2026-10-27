package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every write moves the deadline on, so a download that keeps making progress
// is never cut off, while the deadline stays a fixed distance ahead.
func TestStallDeadlineWriter_RollsTheDeadlineForward(t *testing.T) {
	old := downloadStallTimeout
	downloadStallTimeout = 10 * time.Second
	t.Cleanup(func() { downloadStallTimeout = old })

	d := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	w := withStallDeadline(d)
	if w == http.ResponseWriter(d) {
		t.Fatal("a writer that supports deadlines was not wrapped")
	}
	first := d.deadline
	if first.Before(time.Now().Add(5*time.Second)) || first.After(time.Now().Add(11*time.Second)) {
		t.Fatalf("initial deadline %v is not about the stall timeout ahead", first)
	}

	// Writes within a second of the last move leave it alone, a later one moves it.
	_, _ = w.Write([]byte("a"))
	if !d.deadline.Equal(first) {
		t.Fatalf("deadline moved on a write inside the throttle window")
	}
	w.(*stallDeadlineWriter).last = time.Now().Add(-2 * time.Second)
	_, _ = w.Write([]byte("b"))
	if !d.deadline.After(first) {
		t.Fatalf("deadline %v did not move past %v on a later write", d.deadline, first)
	}
	if d.Body.String() != "ab" {
		t.Fatalf("body = %q", d.Body.String())
	}
	if http.NewResponseController(w).Flush() != nil || !d.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
	// A controller built on the wrapper still reaches the server's writer.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil || !d.deadline.IsZero() {
		t.Fatalf("SetWriteDeadline through the wrapper: err=%v deadline=%v", err, d.deadline)
	}
}

// A writer that cannot move its deadline is passed through untouched.
func TestStallDeadlineWriter_UnsupportedWriterPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	if w := withStallDeadline(rec); w != http.ResponseWriter(rec) {
		t.Fatal("a writer without deadline support was wrapped")
	}
}

// The deadline is a stall limit, not a limit on the whole response: a client
// that reads steadily outlasts it, and one that stops reading is dropped.
func TestFileDownload_StalledClientIsDropped(t *testing.T) {
	old := downloadStallTimeout
	downloadStallTimeout = 700 * time.Millisecond
	t.Cleanup(func() { downloadStallTimeout = old })

	h, books, author, _, tmp := fileFixture(t)
	path := filepath.Join(tmp, "book.m4b")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const size = 64 << 20 // more than the kernel buffers can take
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	seedFileBook(t, books, author, "OL-STALL", path)

	done := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		h.Download(w, withURLParam(r, "id", "1"))
	}))
	srv.Config.WriteTimeout = time.Hour // only the stall deadline can end this
	srv.Start()
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1024)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	// Stop reading. The handler must give up on its own.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a client that stopped reading still holds the download after 10 s")
	}
}

// A client that reads steadily keeps a download alive for longer than the stall
// timeout.
func TestFileDownload_SteadyClientOutlastsStallTimeout(t *testing.T) {
	old := downloadStallTimeout
	downloadStallTimeout = 700 * time.Millisecond
	t.Cleanup(func() { downloadStallTimeout = old })

	h, books, author, _, tmp := fileFixture(t)
	path := filepath.Join(tmp, "book.m4b")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const size = 48 << 20
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	seedFileBook(t, books, author, "OL-STEADY", path)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.Download(w, withURLParam(r, "id", "1"))
	}))
	srv.Config.WriteTimeout = 500 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	start := time.Now()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<20)
	var got int64
	for {
		n, err := io.ReadFull(resp.Body, buf)
		got += int64(n)
		if err != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != size {
		t.Fatalf("got %d of %d bytes after %v", got, size, time.Since(start))
	}
	if time.Since(start) < downloadStallTimeout {
		t.Fatalf("finished in %v, too fast to prove anything", time.Since(start))
	}
}
