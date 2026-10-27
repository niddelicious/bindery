package bnf

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/useragent"
)

func pacedClient(base string, p *pacer, timeout time.Duration) *Client {
	return &Client{
		http: &http.Client{Timeout: timeout, Transport: &pacedTransport{base: http.DefaultTransport, pacer: p}},
		base: base,
	}
}

const emptySRU = `<searchRetrieveResponse><numberOfRecords>0</numberOfRecords><records/></searchRetrieveResponse>`

func TestNewClientsShareOnePacerAndGate(t *testing.T) {
	a, b := New(), New()
	pa, ok := a.http.Transport.(*pacedTransport)
	if !ok {
		t.Fatalf("transport = %T, want the pacer in front of every request", a.http.Transport)
	}
	// Compared with the package defaults, not only with each other: two nil
	// gates are equal, and a nil gate holds nothing while BnF refuses (#3100).
	if pb := b.http.Transport.(*pacedTransport); pa.pacer == nil || pa.pacer != defaultPacer || pb.pacer != defaultPacer {
		t.Error("clients have their own pacers; the limit must hold for the process")
	}
	if a.gate == nil || a.gate != defaultGate || b.gate != defaultGate {
		t.Errorf("gates = %p, %p, want both the shared refusal gate %p", a.gate, b.gate, defaultGate)
	}
	if defaultPacer.interval < time.Second || cap(defaultPacer.slots) > 2 {
		t.Errorf("default pacing = %v, %d concurrent; want at most one start a second and two in flight",
			defaultPacer.interval, cap(defaultPacer.slots))
	}
	if a.http.Timeout <= 0 {
		t.Error("no per request timeout")
	}
}

func TestPacer_SpacesRequestStarts(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		if ua := r.Header.Get("User-Agent"); ua != useragent.Get() || !strings.Contains(strings.ToLower(ua), "bindery") {
			t.Errorf("User-Agent = %q, want Bindery's", ua)
		}
		_, _ = w.Write([]byte(emptySRU))
	}))
	defer srv.Close()

	const interval = 80 * time.Millisecond
	c := pacedClient(srv.URL, newPacer(interval, 2), 5*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.SearchBooks(context.Background(), "x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(starts) != 4 {
		t.Fatalf("requests = %d", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		// Server side arrival can jitter slightly; allow 15 ms.
		if gap := starts[i].Sub(starts[i-1]); gap < interval-15*time.Millisecond {
			t.Errorf("gap %d = %v, want at least %v between request starts", i, gap, interval)
		}
	}
}

func TestPacer_CapsConcurrentRequests(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = w.Write([]byte(emptySRU))
	}))
	defer srv.Close()

	c := pacedClient(srv.URL, newPacer(0, 2), 5*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.SearchBooks(context.Background(), "x")
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > 2 {
		t.Errorf("peak concurrent requests = %d, want at most 2", got)
	}
}

// The concurrency slot is held until the response body is closed, not just
// until the headers arrive: a slow download still counts as in flight.
func TestPacer_SlotHeldUntilBodyClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(emptySRU))
	}))
	defer srv.Close()
	tr := &pacedTransport{base: http.DefaultTransport, pacer: newPacer(0, 1)}
	get := func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		return tr.RoundTrip(req)
	}
	first, err := get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if resp, err := get(ctx); err == nil {
		_ = resp.Body.Close()
		t.Error("second request started while the first body was still open; want it held for the slot")
	}
	_ = first.Body.Close()
	second, err := get(context.Background())
	if err != nil {
		t.Fatalf("after the first body closed: %v", err)
	}
	_ = second.Body.Close()
}

// A request that could only start after its caller's deadline is refused at
// once as rate limited, instead of sleeping into a timeout.
func TestPacer_RefusesPastTheDeadlineAsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(emptySRU))
	}))
	defer srv.Close()
	c := pacedClient(srv.URL, newPacer(time.Hour, 2), 5*time.Second)
	if _, err := c.SearchBooks(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := c.SearchBooks(ctx, "second")
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
	if time.Since(began) > 150*time.Millisecond {
		t.Errorf("took %v, want an immediate refusal", time.Since(began))
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(emptySRU))
	}))
	defer srv.Close()
	c := pacedClient(srv.URL, newPacer(0, 2), 5*time.Second)
	if _, err := c.SearchBooks(context.Background(), "x"); err != nil {
		t.Fatalf("err = %v, want the retry after the wait to succeed", err)
	}
	if len(times) != 2 {
		t.Fatalf("requests = %d, want 2", len(times))
	}
	if gap := times[1].Sub(times[0]); gap < 950*time.Millisecond {
		t.Errorf("retried after %v, want Retry-After's 1 s honoured", gap)
	}
}

func TestPersistent429IsRateLimited(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := pacedClient(srv.URL, newPacer(0, 2), 5*time.Second).SearchBooks(context.Background(), "x")
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("requests = %d, want the first and three retries", got)
	}
}

func TestServerErrorsAreUnavailable(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
		}))
		_, err := pacedClient(srv.URL, newPacer(0, 2), 5*time.Second).SearchBooks(context.Background(), "x")
		srv.Close()
		if !errors.Is(err, providererr.ErrUnavailable) || errors.Is(err, providererr.ErrRateLimited) {
			t.Errorf("HTTP %d: err = %v, want ErrUnavailable", code, err)
		}
		if code == http.StatusInternalServerError && calls.Load() != 1 {
			t.Errorf("HTTP 500 sent %d requests, want no retry", calls.Load())
		}
	}
}

func TestOther4xxIsNotRetriedOrMarked(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := pacedClient(srv.URL, newPacer(0, 2), 5*time.Second).SearchBooks(context.Background(), "x")
	if err == nil || errors.Is(err, providererr.ErrUnavailable) || errors.Is(err, providererr.ErrRateLimited) {
		t.Errorf("err = %v, want a plain error", err)
	}
	if calls.Load() != 1 {
		t.Errorf("requests = %d, want 1", calls.Load())
	}
}

func TestNetworkFailureIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	_, err := pacedClient(url, newPacer(0, 2), 2*time.Second).SearchBooks(context.Background(), "x")
	if !errors.Is(err, providererr.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestHungServerHitsTheTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	began := time.Now()
	_, err := pacedClient(srv.URL, newPacer(0, 2), 150*time.Millisecond).SearchBooks(ctx, "x")
	if err == nil {
		t.Fatal("err = nil, want a timeout")
	}
	if !errors.Is(err, providererr.ErrUnavailable) {
		t.Errorf("err = %v, want it marked unavailable", err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("took %v, want the per request timeout to end each attempt", took)
	}
}

// A body past maxResponseBytes is cut off and fails to decode. The padding is
// an XML comment, so without the cap the document would decode as a valid
// empty result.
func TestOversizedBodyIsCutOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<searchRetrieveResponse><numberOfRecords>0</numberOfRecords><records/><!--`))
		pad := []byte(strings.Repeat("x", 64<<10))
		for written := 0; written < maxResponseBytes+(1<<20); written += len(pad) {
			if _, err := w.Write(pad); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`--></searchRetrieveResponse>`))
	}))
	defer srv.Close()
	_, err := pacedClient(srv.URL, newPacer(0, 2), 5*time.Second).SearchBooks(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("err = %v, want the cut off body to fail to decode", err)
	}
}

// A server whose cursor never ends costs at most maxWorksPages requests for
// the linked catalogue and maxRecentPages for the name query.
func TestPageCapStopsARunawayCursor(t *testing.T) {
	var author2bib, recent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, "aut.recordid"):
			_, _ = w.Write(fixture(t, "authority_carroll.xml"))
			return
		case strings.Contains(q, "bib.author2bib"):
			author2bib.Add(1)
		case strings.Contains(q, "bib.creationdate"):
			recent.Add(1)
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("startRecord"))
		// One new book record per page, and a total that never ends.
		fmt.Fprintf(w, `<searchRetrieveResponse><numberOfRecords>99999999</numberOfRecords><records><record><recordData>`+
			`<record id="ark:/12148/cb%08dx" type="Bibliographic"><leader>     nam  22        450 </leader>`+
			`<datafield tag="200" ind1="1" ind2=" "><subfield code="a">Livre %d</subfield></datafield>`+
			`<datafield tag="700" ind1=" " ind2=" "><subfield code="3">11885918</subfield><subfield code="a">Carroll</subfield><subfield code="b">Lewis</subfield><subfield code="4">070</subfield></datafield>`+
			`</record></recordData></record></records></searchRetrieveResponse>`, start, start)
	}))
	defer srv.Close()
	books, complete, err := pacedClient(srv.URL, newPacer(0, 2), 5*time.Second).GetAuthorWorksSnapshot(context.Background(), "bnf:author:11885918")
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Error("complete = true for a capped walk")
	}
	if got := author2bib.Load(); got != maxWorksPages {
		t.Errorf("author2bib requests = %d, want the cap of %d", got, maxWorksPages)
	}
	if got := recent.Load(); got != maxRecentPages {
		t.Errorf("recent requests = %d, want the cap of %d", got, maxRecentPages)
	}
	if len(books) != maxWorksPages {
		t.Errorf("books = %d, want one per page read", len(books))
	}
}
