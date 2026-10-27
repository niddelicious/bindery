package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// OPDS Basic auth refusals (429 over the attempt limit, 503 when the client
// left while queued for a hash slot) used the plain text http.Error. They
// answer the standard JSON shape now, like the rest of the API.

func TestOPDS_BasicRefusalsAreJSON(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		h, users, _ := opdsBasicHarness(t) // limiter allows 5 per window
		hash, _ := auth.HashPassword("reader-password-1")
		if _, err := users.Create(context.Background(), "reader", hash); err != nil {
			t.Fatal(err)
		}
		var rec *httptest.ResponseRecorder
		for i := range 12 {
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, basicReq("reader", "wrong-"+strconv.Itoa(i), "203.0.113.141"))
			if rec.Code == http.StatusTooManyRequests {
				break
			}
		}
		wantFixed(t, rec, http.StatusTooManyRequests, "too many attempts")
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Error("WWW-Authenticate was dropped")
		}
	})

	t.Run("abandoned while queued", func(t *testing.T) {
		h, users, v := opdsBasicHarness(t)
		hash, _ := auth.HashPassword("reader-password-1")
		if _, err := users.Create(context.Background(), "reader", hash); err != nil {
			t.Fatal(err)
		}
		v.verify = func(context.Context, string, string) (bool, error) { return false, context.Canceled }
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, basicReq("reader", "guess", "203.0.113.142"))
		wantFixed(t, rec, http.StatusServiceUnavailable, "server busy, try again")
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
	})
}
