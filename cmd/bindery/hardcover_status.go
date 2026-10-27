package main

import (
	"errors"
	"log/slog"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
)

// hardcoverHoldStatus turns the result of Hardcover's CheckQuota into the two
// fields GET /api/v1/system/status reports: the end of a daily quota pause (an
// RFC 3339 time, empty when there is none) and whether the saved token is
// currently refused. A rejected token outranks a quota pause, as it does in
// CheckQuota itself, so the Settings banner never promises a reset that will
// not help (#3101).
func hardcoverHoldStatus(err error) (pausedUntil string, tokenRejected bool) {
	var rejected *metadata.TokenRejectedError
	var exhausted *metadata.DailyQuotaError
	switch {
	case errors.As(err, &rejected):
		return "", true
	case errors.As(err, &exhausted):
		return exhausted.ResetAt.UTC().Format(time.RFC3339), false
	case err != nil:
		slog.Warn("read Hardcover pause status", "error", err)
	}
	return "", false
}
