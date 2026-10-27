package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// TestQueueList_ShowsEverythingButOldImports pins what the queue endpoint
// returns. The page has Retry, Retry import, Match to book and Remove on
// failed, importFailed and importBlocked rows, so those must be listed however
// old they are; only an import older than queueImportedVisibleFor drops out.
func TestQueueList_ShowsEverythingButOldImports(t *testing.T) {
	h, database, downloads, _, _, ctx := queueFixture(t)
	now := time.Now().UTC()
	ancient := now.Add(-300 * 24 * time.Hour)

	add := func(guid string, status models.DownloadState, at time.Time) {
		t.Helper()
		d := &models.Download{GUID: guid, Title: guid, Status: models.StateGrabbed, Protocol: "usenet"}
		if err := downloads.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`UPDATE downloads SET status=?, added_at=?, imported_at=? WHERE id=?`, status, at, at, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range models.AllStates() {
		add("ancient-"+string(s), s, ancient)
	}
	add("imported-yesterday", models.StateImported, now.Add(-24*time.Hour))
	add("imported-8-days", models.StateImported, now.Add(-8*24*time.Hour))

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp queueListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range resp.Items {
		got[it.GUID] = true
	}

	var missing []string
	for _, s := range models.AllStates() {
		if s == models.StateImported {
			if got["ancient-"+string(s)] {
				t.Errorf("an import from 300 days ago is still on the queue page")
			}
			continue
		}
		if !got["ancient-"+string(s)] {
			missing = append(missing, string(s))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("states hidden from the queue by age, which hides actions the user has not taken: %v", missing)
	}
	if !got["imported-yesterday"] {
		t.Error("an import from yesterday must be listed")
	}
	if got["imported-8-days"] {
		t.Error("an import from 8 days ago is past the 7 day window and must not be listed")
	}
}
