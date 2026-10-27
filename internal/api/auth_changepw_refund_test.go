package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// A request abandoned while queued for a KDF slot verified nothing, so it must
// not count toward the lockout (the reservation is refunded), and a run of wrong
// current passwords must not touch the login limiter: locking the password
// change form must never lock the same person out of signing in.
func TestChangePassword_AbandonedRefundedAndLoginUnaffected(t *testing.T) {
	h, users, _, ctx := newAuthFixture(t)
	hash, _ := auth.HashPassword("old-password")
	u, err := users.Create(ctx, "only", hash)
	if err != nil {
		t.Fatal(err)
	}
	change := func(current string) int {
		req := httptest.NewRequest(http.MethodPost, "/auth/password",
			jsonBody(t, changePasswordRequest{CurrentPassword: current, NewPassword: "new-password-2026"}))
		req = req.WithContext(auth.WithUserID(req.Context(), u.ID))
		rec := httptest.NewRecorder()
		h.ChangePassword(rec, req)
		return rec.Code
	}

	real := h.verifyPassword
	h.verifyPassword = func(context.Context, string, string) (bool, error) { return false, context.Canceled }
	for i := 0; i < 20; i++ {
		if got := change("WRONG"); got != http.StatusServiceUnavailable {
			t.Fatalf("abandoned request %d: status = %d, want 503", i, got)
		}
	}
	h.verifyPassword = real

	// Twenty abandoned requests left no trace: five real failures still answer 401.
	for i := 1; i <= 5; i++ {
		if got := change("WRONG"); got != http.StatusUnauthorized {
			t.Fatalf("wrong attempt %d: status = %d, want 401 (abandoned requests were counted)", i, got)
		}
	}
	if got := change("WRONG"); got != http.StatusTooManyRequests {
		t.Fatalf("attempt 6: status = %d, want 429", got)
	}

	// Locked out of changing the password, not of logging in.
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		jsonBody(t, loginRequest{Username: "only", Password: "old-password"}))
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after password-change lockout: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
