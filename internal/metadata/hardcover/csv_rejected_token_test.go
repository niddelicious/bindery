package hardcover_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/migrate"
)

type countingTransport func(*http.Request) (*http.Response, error)

func (f countingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestCSVImportWithRejectedTokenAsksHardcoverOnce is the #3101 symptom: a
// bulk import with a rejected token used to send one doomed request per row.
func TestCSVImportWithRejectedTokenAsksHardcoverOnce(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	settings := db.NewSettingsRepo(database)
	var requests atomic.Int32
	client := hardcover.New().
		WithDailyQuota(hardcover.NewDailyQuota(settings)).
		WithToken("example-only-token").
		WithTransportForTest(countingTransport(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_token","error_description":"Invalid or expired token"}`)),
			}, nil
		}))

	var rows strings.Builder
	rows.WriteString("name\n")
	for _, name := range []string{"One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten"} {
		rows.WriteString(name + " Author\n")
	}
	res, err := migrate.ImportCSVAuthors(context.Background(), strings.NewReader(rows.String()),
		db.NewAuthorRepo(database), settings, metadata.NewAggregator(client), nil)

	var rejected *metadata.TokenRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("import error = %v, want the rejected token to stop it", err)
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("Hardcover saw %d requests for a 10 row import, want 1", n)
	}
	if res == nil || res.Added != 0 {
		t.Fatalf("result = %+v", res)
	}
}
