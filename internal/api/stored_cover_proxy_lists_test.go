package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// The adoption list shows the library books it suggests, and a book can hold
// a stored cover, so the reference travels there as a proxy URL too.
func TestToAdoptionItem_ProxiesStoredCover(t *testing.T) {
	refs := map[int64]db.UnmatchedBookRef{
		7: {ID: 7, Title: "Fjellvinden", ImageURL: storedCoverRef},
		8: {ID: 8, Title: "Another", ImageURL: storedCoverRef},
	}
	u := &db.UnmatchedUnit{ID: 1, BookID: 7, Candidates: []db.UnmatchedCandidate{{BookID: 8, Score: 0.9}}}
	it := toAdoptionItem(u, refs)
	if it.Book == nil {
		t.Fatal("no book on the item")
	}
	requireProxiedCover(t, "adoption item book", it.Book.ImageURL, storedCoverRef)
	if len(it.Candidates) != 1 {
		t.Fatalf("candidates = %+v", it.Candidates)
	}
	requireProxiedCover(t, "adoption candidate", it.Candidates[0].Book.ImageURL, storedCoverRef)
	if refs[7].ImageURL != storedCoverRef {
		t.Error("the shared ref map was rewritten in place")
	}
}

// The manual import scan matches files against the library, and the books it
// names can hold a stored cover.
func TestManualImportScan_ProxiesStoredCovers(t *testing.T) {
	t.Parallel()
	h, stub, _, _, _ := manualImportFixture(t)
	matched := models.Book{ID: 1, Title: "Fjellvinden", ImageURL: storedCoverRef}
	other := models.Book{ID: 2, Title: "Fjellvind", ImageURL: storedCoverRef}
	stub.lookupResult = importer.LookupResult{
		Match: "ambiguous", ParsedTitle: "Fjellvinden", Book: &matched, Candidates: []models.Book{matched, other},
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Fjellvinden.epub"))

	rec := httptest.NewRecorder()
	h.Scan(rec, scanRequest(root))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp ScanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Book == nil || len(resp.Items[0].Candidates) != 2 {
		t.Fatalf("unexpected items %+v", resp.Items)
	}
	requireProxiedCover(t, "scan book", resp.Items[0].Book.ImageURL, storedCoverRef)
	for _, c := range resp.Items[0].Candidates {
		requireProxiedCover(t, "scan candidate", c.ImageURL, storedCoverRef)
	}
	if matched.ImageURL != storedCoverRef {
		t.Error("the scanner's own book was rewritten in place")
	}
}
