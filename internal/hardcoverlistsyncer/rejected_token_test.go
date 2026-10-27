package hardcoverlistsyncer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

func TestRejectedTokenListSyncStopsBeforeFetch(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	settings := db.NewSettingsRepo(database)
	token := "example-only"
	secret := strings.Repeat("ab", 32)
	if err := settings.Set(ctx, "auth.hardcover_daily_hold_secret", secret); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(secret)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	if err := settings.Set(ctx, "auth.hardcover_rejected_holds", fmt.Sprintf(`{"%x":%q}`, mac.Sum(nil), time.Now().Add(time.Hour).UTC().Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	s := New(db.NewImportListRepo(database), db.NewAuthorRepo(database), db.NewBookRepo(database)).WithDailyQuota(hardcover.NewDailyQuota(settings))
	il := testImportList("Held", "hardcover", true)
	il.APIKey = token
	var rejected *metadata.TokenRejectedError
	if err := s.syncList(ctx, il); !errors.As(err, &rejected) {
		t.Fatalf("factory hold: %v", err)
	}
}
