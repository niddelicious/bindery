package hardcover

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
)

const rejectedBody = `{"error":"invalid_token","error_description":"Invalid or expired token"}`

// rejectedClient answers every request with status/body and counts them.
func rejectedClient(t *testing.T, q *DailyQuota, token string, status int, body string) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	th, _ := newFakeThrottle()
	c := New().WithDailyQuota(q).WithToken(token)
	c.throttle = th
	c.http = &http.Client{Transport: dailyTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return c, &calls
}

func TestRejectedTokenIsAskedOnce(t *testing.T) {
	q, _ := dailyFixture(t)
	c, calls := rejectedClient(t, q, "example-only-token", 401, rejectedBody)
	var out any
	for i := range 20 {
		err := c.query(context.Background(), "query", nil, &out)
		var rejected *metadata.TokenRejectedError
		var daily *metadata.DailyQuotaError
		if !errors.As(err, &rejected) || errors.As(err, &daily) {
			t.Fatalf("call %d: %v", i, err)
		}
		if strings.Contains(err.Error(), "example-only-token") {
			t.Fatalf("error leaks the token: %v", err)
		}
		if i == 0 && !strings.Contains(err.Error(), "HTTP 401") {
			t.Fatalf("first answer lost the upstream reason: %v", err)
		}
		if !strings.Contains(err.Error(), "Hardcover") || !strings.Contains(err.Error(), "changed in Settings") {
			t.Fatalf("wording: %v", err)
		}
		if errors.Is(err, metadata.ErrRateLimited) || metadata.IsProviderUnavailable(err) {
			t.Fatalf("a rejected token is neither a throttle nor an outage: %v", err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if err := c.CheckQuota(context.Background()); !metadata.IsProviderHold(err) {
		t.Fatalf("CheckQuota = %v", err)
	}
}

func TestRejectedTokenHoldSurvivesRestartAndKeysByToken(t *testing.T) {
	ctx := context.Background()
	q, settings := dailyFixture(t)
	c, calls := rejectedClient(t, q, "bad-token", 401, rejectedBody)
	var out any
	_ = c.query(ctx, "query", nil, &out)
	if row, err := settings.Get(ctx, rejectedHoldSetting); err != nil || row == nil || strings.Contains(row.Value, "bad-token") {
		t.Fatalf("row = %+v err %v", row, err)
	}
	restarted := NewDailyQuota(settings)
	c2, calls2 := rejectedClient(t, restarted, "Bearer bad-token", 401, rejectedBody)
	for range 5 {
		if err := c2.query(ctx, "query", nil, &out); !metadata.IsProviderHold(err) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || calls2.Load() != 0 {
		t.Fatalf("requests before restart %d, after restart %d", calls.Load(), calls2.Load())
	}
	// Saving a different token is the whole "resume when the token changes".
	c3, calls3 := rejectedClient(t, restarted, "good-token", 200, `{"data":{}}`)
	if err := c3.query(ctx, "query", nil, &out); err != nil || calls3.Load() != 1 {
		t.Fatalf("new token: %v calls %d", err, calls3.Load())
	}
	if err := restarted.Check(ctx, "bad-token"); !metadata.IsProviderHold(err) {
		t.Fatalf("old token hold lost: %v", err)
	}
}

func TestRejectedTokenOnlyForAuthenticationFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		hold   bool
	}{
		{"401", 401, ``, true},
		{"401 JSON invalid_token", 401, rejectedBody, true},
		{"401 proxy html page", 401, `<html><body>401 Authorization Required</body></html>`, false},
		{"401 plain text from a gateway", 401, `Unauthorized`, false},
		{"403 cloudflare challenge page", 403, `<!DOCTYPE html><title>Just a moment...</title>`, false},
		{"403 invalid_token", 403, rejectedBody, true},
		{"403 unable to verify", 403, `{"error":"Unable to verify token"}`, true},
		{"403 refused operator", 403, `{"error":"ilike and related operations are not permitted on this server."}`, false},
		{"403 insufficient scope", 403, `{"error":"insufficient_scope"}`, false},
		{"403 token bucket", 403, `{"error":"Token bucket exhausted, retry after 30s"}`, false},
		{"500", 500, `{"error":"boom"}`, false},
		{"500 mentions token", 500, `{"error":"invalid token cache"}`, false},
		{"401 from auth server outage as 503", 503, rejectedBody, false},
		{"graphql auth error in a 200", 200, `{"errors":[{"message":"Unable to verify token"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, _ := dailyFixture(t)
			c, calls := rejectedClient(t, q, "tok", tc.status, tc.body)
			var out any
			for range 3 {
				_ = c.query(context.Background(), "query", nil, &out)
			}
			if err := c.CheckQuota(context.Background()); metadata.IsProviderHold(err) != tc.hold {
				t.Fatalf("hold = %v, want %v (%v)", !tc.hold, tc.hold, err)
			}
			if tc.hold && calls.Load() != 1 {
				t.Fatalf("requests = %d, want 1", calls.Load())
			}
		})
	}
}

func TestRejectedTokenProbeAndRecovery(t *testing.T) {
	ctx := context.Background()
	q, settings := dailyFixture(t)
	now := time.Now().UTC()
	q.now = func() time.Time { return now }
	var status atomic.Int32
	status.Store(401)
	var calls atomic.Int32
	th, _ := newFakeThrottle()
	c := New().WithDailyQuota(q).WithToken("tok")
	c.throttle = th
	c.http = &http.Client{Transport: dailyTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		body := rejectedBody
		if status.Load() == 200 {
			body = `{"data":{}}`
		}
		return &http.Response{StatusCode: int(status.Load()), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	var out any
	_ = c.query(ctx, "query", nil, &out)
	_ = c.query(ctx, "query", nil, &out)
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	// A deliberate retest reaches Hardcover while the hold is live, and a
	// rejection renews the hold without adding a second request for others.
	if err := c.query(WithRejectedTokenProbe(ctx), "query", nil, &out); !metadata.IsProviderHold(err) || calls.Load() != 2 {
		t.Fatalf("probe: %v calls %d", err, calls.Load())
	}
	if err := c.query(ctx, "query", nil, &out); !metadata.IsProviderHold(err) || calls.Load() != 2 {
		t.Fatalf("after probe: %v calls %d", err, calls.Load())
	}
	// Token fixed on Hardcover's side: the probe succeeds and lifts the hold.
	status.Store(200)
	if err := c.query(WithRejectedTokenProbe(ctx), "query", nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := c.query(ctx, "query", nil, &out); err != nil {
		t.Fatalf("hold survived a successful probe: %v", err)
	}
	if err := NewDailyQuota(settings).Check(ctx, "tok"); err != nil {
		t.Fatalf("cleared hold persisted as live: %v", err)
	}
	// Without any retest the hold lapses on its own after the cap.
	status.Store(401)
	calls.Store(0)
	_ = c.query(ctx, "query", nil, &out)
	now = now.Add(time.Hour - time.Minute)
	_ = c.query(ctx, "query", nil, &out)
	if calls.Load() != 1 {
		t.Fatalf("early probe: calls %d", calls.Load())
	}
	now = now.Add(2 * time.Minute)
	_ = c.query(ctx, "query", nil, &out)
	if calls.Load() != 2 {
		t.Fatalf("no probe after the cap: calls %d", calls.Load())
	}
}

func TestRejectedTokenAppliesToEveryClientCall(t *testing.T) {
	q, _ := dailyFixture(t)
	c, calls := rejectedClient(t, q, "tok", 401, rejectedBody)
	ctx := context.Background()
	if _, err := c.SearchAuthors(ctx, "Example"); !metadata.IsProviderHold(err) {
		t.Fatal(err)
	}
	calls.Store(0)
	checks := map[string]func() error{
		"SearchBooks":      func() error { _, err := c.SearchBooks(ctx, "Example"); return err },
		"GetAuthor":        func() error { _, err := c.GetAuthor(ctx, "hc:1"); return err },
		"GetAuthorWorks":   func() error { _, err := c.GetAuthorWorks(ctx, "hc:1"); return err },
		"GetBook":          func() error { _, err := c.GetBook(ctx, "hc:1"); return err },
		"GetEditions":      func() error { _, err := c.GetEditions(ctx, "hc:1"); return err },
		"GetBookByISBN":    func() error { _, err := c.GetBookByISBN(ctx, "9780441172719"); return err },
		"SearchSeries":     func() error { _, err := c.SearchSeries(ctx, "Dune", 3); return err },
		"GetSeriesCatalog": func() error { _, err := c.GetSeriesCatalog(ctx, "1"); return err },
		"GetUserLists":     func() error { _, err := c.GetUserLists(ctx); return err },
		"GetListBooks":     func() error { _, err := c.GetListBooks(ctx, 1); return err },
		"GetUserWishlist":  func() error { _, err := c.GetUserWishlist(ctx, 10); return err },
	}
	for name, call := range checks {
		if err := call(); !metadata.IsProviderHold(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("held client sent %d requests", calls.Load())
	}
}

func TestRejectedTokenAsPrimary(t *testing.T) {
	ctx := context.Background()
	q, _ := dailyFixture(t)
	c, calls := rejectedClient(t, q, "tok", 401, rejectedBody)
	_, _ = c.GetBookByISBN(ctx, "9780441172719")
	calls.Store(0)
	// Behind the aggregator as primary, nothing is sent. The enricher role
	// is the same client methods, covered by TestRejectedTokenAppliesToEveryClientCall.
	primary := metadata.NewAggregator(c)
	if err := primary.CheckQuota(ctx); !metadata.IsProviderHold(err) {
		t.Fatalf("primary CheckQuota = %v", err)
	}
	if _, _, err := primary.GetBookByISBNWithOutcome(ctx, "9780441172719"); !metadata.IsProviderHold(err) {
		t.Fatalf("primary isbn: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("held primary sent %d requests", calls.Load())
	}
}

func TestRejectedTokenConcurrentWorkers(t *testing.T) {
	ctx := context.Background()
	q, _ := dailyFixture(t)
	var calls atomic.Int32
	c := New().WithDailyQuota(q).WithToken("tok")
	c.throttle = &throttle{now: time.Now, sleep: func(context.Context, time.Duration) error { return nil }}
	gate := make(chan struct{})
	c.http = &http.Client{Transport: dailyTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		<-gate // every worker is admitted before the first answer lands
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(rejectedBody))}, nil
	})}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out any
			errs <- c.query(ctx, "query", nil, &out)
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !metadata.IsProviderHold(err) {
			t.Fatal(err)
		}
	}
	var out any
	before := calls.Load()
	if err := c.query(ctx, "query", nil, &out); !metadata.IsProviderHold(err) || calls.Load() != before {
		t.Fatalf("late caller: %v", err)
	}
	if before > 32 {
		t.Fatalf("calls %d", before)
	}
}

func TestRejectedTokenCorruptRowAndFailedWrite(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settings := db.NewSettingsRepo(database)
	q := NewDailyQuota(settings)
	if err := settings.Set(ctx, rejectedHoldSetting, `not json`); err != nil {
		t.Fatal(err)
	}
	if err := q.Check(ctx, "tok"); err != nil {
		t.Fatalf("corrupt row blocked admission: %v", err)
	}
	if _, err := database.Exec(`CREATE TRIGGER fail_rejected_write BEFORE INSERT ON settings BEGIN SELECT RAISE(FAIL, 'temporary write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM settings WHERE key = ?`, rejectedHoldSetting); err != nil {
		t.Fatal(err)
	}
	err = q.rejectToken(ctx, "tok", errors.New("token rejected (HTTP 401)"))
	var rejected *metadata.TokenRejectedError
	if !errors.As(err, &rejected) || !strings.Contains(err.Error(), "temporary write failure") {
		t.Fatalf("write failure hidden or hold lost: %v", err)
	}
	if err := q.Check(ctx, "other"); err == nil || !strings.Contains(err.Error(), "temporary write failure") {
		t.Fatalf("pending write was not retried before admitting: %v", err)
	}
	if _, err := database.Exec(`DROP TRIGGER fail_rejected_write`); err != nil {
		t.Fatal(err)
	}
	if err := q.Check(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := NewDailyQuota(settings).Check(ctx, "tok"); !errors.As(err, &rejected) {
		t.Fatalf("retried hold not durable: %v", err)
	}
}

// A token that is both over its daily quota and rejected reports the rejection:
// waiting for the reset cannot help, so the answer must not promise one.
func TestRejectedTokenOutranksDailyQuotaHold(t *testing.T) {
	ctx := context.Background()
	q, _ := dailyFixture(t)
	if err := q.Check(ctx, "tok"); err != nil {
		t.Fatal(err)
	}
	q.mu.Lock()
	q.holds[q.tokenKey("tok")] = q.now().Add(2 * time.Hour)
	q.version++
	q.mu.Unlock()
	var daily *metadata.DailyQuotaError
	if err := q.Check(ctx, "tok"); !errors.As(err, &daily) {
		t.Fatalf("daily hold alone: %v", err)
	}
	_ = q.rejectToken(ctx, "tok", errors.New("token rejected (HTTP 401)"))
	var rejected *metadata.TokenRejectedError
	err := q.Check(ctx, "tok")
	if !errors.As(err, &rejected) || errors.As(err, &daily) {
		t.Fatalf("both holds: %v", err)
	}
}
