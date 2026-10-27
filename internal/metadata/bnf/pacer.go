package bnf

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// BnF publishes no rate limit for its SRU service, and every Bindery install
// that picks this provider is an independent client of it. So the pace is
// set conservatively here rather than discovered from refusals:
//
//   - requestInterval: at most one request starts per second, with no burst.
//     A search is one request and an author's catalogue a dozen, so a person
//     using the app never waits long, while a background refresh of a whole
//     library stays at the pace of a patient human clicking through the
//     catalogue.
//   - maxConcurrent: at most two requests in flight. Two, not one, so a
//     search typed while an author refresh walks its pages is not stuck
//     behind the whole walk; the start spacing above still applies to both.
//
// One pacer is shared by every client New returns, so the limits hold for
// the process however many clients are built. Retries made by the shared
// provider request loop go through it too, because it sits in the HTTP
// transport.
const (
	requestInterval = time.Second
	maxConcurrent   = 2
)

// errPaced reports that waiting for a request slot would have run past the
// caller's deadline. Failing at once tells the caller BnF was rate limited
// (locally) rather than letting it time out and look like an outage, as the
// Hardcover pacer does.
var errPaced = errors.New("bnf: waiting for the request pacing would pass the caller's deadline")

var defaultPacer = newPacer(requestInterval, maxConcurrent)

// pacer spaces request starts by interval and caps concurrent requests.
type pacer struct {
	interval time.Duration
	slots    chan struct{}

	mu   sync.Mutex
	next time.Time // the earliest start of the next request
}

func newPacer(interval time.Duration, concurrent int) *pacer {
	if concurrent < 1 {
		concurrent = 1
	}
	return &pacer{interval: interval, slots: make(chan struct{}, concurrent)}
}

// acquire waits for a concurrency slot and then for this request's start
// time. It returns the func that frees the slot.
func (p *pacer) acquire(ctx context.Context) (func(), error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-p.slots }) }

	p.mu.Lock()
	start := time.Now()
	if p.next.After(start) {
		start = p.next
	}
	if deadline, ok := ctx.Deadline(); ok && start.After(deadline) {
		p.mu.Unlock()
		release()
		return nil, errPaced
	}
	p.next = start.Add(p.interval)
	p.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	return release, nil
}

// pacedTransport runs every request through a pacer. The concurrency slot is
// held until the response body is closed, not just until the headers arrive,
// so a slow download counts as in flight.
type pacedTransport struct {
	base  http.RoundTripper
	pacer *pacer
}

func (t *pacedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	release, err := t.pacer.acquire(req.Context())
	if err != nil {
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	resp.Body = &releasingBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

type releasingBody struct {
	io.ReadCloser
	release func()
}

func (b *releasingBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}
