package metadata

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DailyQuotaError is a long-lived provider pause, distinct from short throttling.
// Callers should stop bulk work and surface ResetAt rather than retry each row.
type DailyQuotaError struct{ ResetAt time.Time }

func (e *DailyQuotaError) Error() string {
	return fmt.Sprintf("Hardcover daily quota exhausted; paused until %s; retry the job after this time", e.ResetAt.UTC().Format(time.RFC3339))
}

// TokenRejectedError means the provider answered an authentication failure for
// the configured API token. It is a long-lived pause like DailyQuotaError, but
// it ends when the token changes, not at a clock time, so it is its own type:
// a caller must not tell the user to wait for a reset that will never come.
// It never carries the token. Cause is the provider's own sanitised answer
// (for example "token rejected (HTTP 401: Invalid or expired token)") on the
// call that met the rejection, and nil for a hold served without a request.
type TokenRejectedError struct {
	Provider string
	Cause    error
}

func (e *TokenRejectedError) Error() string {
	provider := e.Provider
	if provider == "" {
		provider = "provider"
	}
	msg := provider + " rejected the API token"
	if e.Cause != nil {
		msg = e.Cause.Error()
	}
	return msg + "; " + provider + " requests are paused until the token is changed in Settings"
}

func (e *TokenRejectedError) Unwrap() error { return e.Cause }

// ProviderHold returns err's DailyQuotaError or TokenRejectedError, or nil when
// err is neither. Every stop point for bulk work asks this instead of matching
// one type, so a new kind of hold cannot be wired into one consumer and missed
// by its sibling.
func ProviderHold(err error) error {
	var daily *DailyQuotaError
	if errors.As(err, &daily) {
		return daily
	}
	var rejected *TokenRejectedError
	if errors.As(err, &rejected) {
		return rejected
	}
	return nil
}

// IsProviderHold reports whether err is a long-lived provider pause that bulk
// work must stop on rather than retry for every row.
func IsProviderHold(err error) bool { return ProviderHold(err) != nil }

// CheckQuota lets bulk jobs stop while the primary provider is held. Enrichers
// are not checked: bulk work does not need them, and their held calls already
// fail fast without a request. No provider requests are made by this check.
func (a *aggregatorCore) CheckQuota(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if quota, ok := a.primary.(interface{ CheckQuota(context.Context) error }); ok {
		return quota.CheckQuota(ctx)
	}
	return nil
}
