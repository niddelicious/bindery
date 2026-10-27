package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

// rejectedHoldRow stores a live rejected token hold for token under a known
// install secret, as a restart would find it.
func rejectedHoldRow(t *testing.T, settings *db.SettingsRepo, token string) {
	t.Helper()
	ctx := context.Background()
	secret := strings.Repeat("ab", 32)
	if err := settings.Set(ctx, "auth.hardcover_daily_hold_secret", secret); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(secret)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	until := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	if err := settings.Set(ctx, "auth.hardcover_rejected_holds", fmt.Sprintf(`{"%x":%q}`, mac.Sum(nil), until)); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedTokenHoldDoesNotBlockTestConnection(t *testing.T) {
	h, settings, ctx := settingsFixture(t)
	token := "example-only"
	if err := settings.Set(ctx, SettingHardcoverAPIToken, token); err != nil {
		t.Fatal(err)
	}
	rejectedHoldRow(t, settings, token)
	hold := hardcover.NewDailyQuota(settings)
	h.WithDailyQuota(hold)

	// The same held token is refused for ordinary calls without a request.
	var rejected *metadata.TokenRejectedError
	if err := hardcover.New().WithDailyQuota(hold).WithToken(token).CheckQuota(ctx); !errors.As(err, &rejected) {
		t.Fatalf("hold not live: %v", err)
	}
	// The test button must get past the hold. An outbound proxy that refuses
	// every tunnel stands in for Hardcover and counts the attempts, so nothing
	// leaves the machine.
	t.Cleanup(func() { _, _ = httpsec.ConfigureOutboundProxy("", "", true) })
	var attempts atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	if _, err := httpsec.ConfigureOutboundProxy(proxy.URL, "", true); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.TestHardcover(rec, httptest.NewRequest(http.MethodPost, "/api/v1/hardcover/test", nil))
	if body := rec.Body.String(); attempts.Load() == 0 || strings.Contains(body, "rejected the API token") {
		t.Fatalf("test connection was blocked by the rejected hold (attempts %d): %s", attempts.Load(), body)
	}
}

func TestSavingNewTokenLiftsRejectedHold(t *testing.T) {
	_, settings, ctx := settingsFixture(t)
	rejectedHoldRow(t, settings, "old-token")
	if err := settings.Set(ctx, SettingHardcoverAPIToken, "old-token"); err != nil {
		t.Fatal(err)
	}
	// Wired as in cmd/bindery: the token is read per call from settings.
	client := hardcover.New().WithDailyQuota(hardcover.NewDailyQuota(settings)).WithTokenSource(func(ctx context.Context) string {
		return GetHardcoverAPIToken(ctx, settings)
	})
	var rejected *metadata.TokenRejectedError
	if err := client.CheckQuota(ctx); !errors.As(err, &rejected) {
		t.Fatalf("hold not live: %v", err)
	}
	if err := settings.Set(ctx, SettingHardcoverAPIToken, "new-token"); err != nil {
		t.Fatal(err)
	}
	if err := client.CheckQuota(ctx); err != nil {
		t.Fatalf("new token still held: %v", err)
	}
}

func TestRejectedTokenHoldHiddenFromAdminSettings(t *testing.T) {
	h, settings, ctx := settingsFixture(t)
	key := "auth.hardcover_rejected_holds"
	if err := settings.Set(ctx, key, `{"example-fingerprint":"2026-09-19T01:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, adminReq(httptest.NewRequest(http.MethodGet, "/api/v1/setting", nil)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), key) || strings.Contains(rec.Body.String(), "example-fingerprint") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.Get(rec, adminReq(withKey(httptest.NewRequest(http.MethodGet, "/api/v1/setting/"+key, nil), key)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
}
