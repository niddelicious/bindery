package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
)

func TestHardcoverHoldStatus(t *testing.T) {
	reset := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		err       error
		wantUntil string
		wantRej   bool
	}{
		{"no hold", nil, "", false},
		{"daily quota", &metadata.DailyQuotaError{ResetAt: reset}, "2026-10-12T00:00:00Z", false},
		{"rejected token", &metadata.TokenRejectedError{Provider: "Hardcover"}, "", true},
		{"rejected token joined with a write failure", errors.Join(&metadata.TokenRejectedError{Provider: "Hardcover"}, errors.New("disk full")), "", true},
		{"wrapped daily quota", fmt.Errorf("check: %w", &metadata.DailyQuotaError{ResetAt: reset}), "2026-10-12T00:00:00Z", false},
		{"unrelated error", errors.New("settings unreadable"), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			until, rejected := hardcoverHoldStatus(tc.err)
			if until != tc.wantUntil || rejected != tc.wantRej {
				t.Fatalf("got (%q, %v), want (%q, %v)", until, rejected, tc.wantUntil, tc.wantRej)
			}
		})
	}
}
