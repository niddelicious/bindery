package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// seriesFillExcludedFixture files the Way of Kings under a Hardcover linked
// series, takes it out the way the book page does, and returns what a test
// needs to fill the series again (#2554).
func seriesFillExcludedFixture(t *testing.T) (*SeriesHandler, *db.SeriesRepo, *models.Series, *models.Book) {
	t.Helper()
	catalog := stormlightCatalog()
	h, seriesRepo, authorRepo, bookRepo := seriesFixtureWithProvider(t, &stubSeriesProvider{
		catalogs: map[string]*metadata.SeriesCatalog{catalog.ForeignID: catalog},
	}, newMockBookSearcher())
	ctx := context.Background()
	series := &models.Series{ForeignID: "ol-series:stormlight", Title: "The Stormlight Archive"}
	if err := seriesRepo.Create(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.UpsertHardcoverLink(ctx, &models.SeriesHardcoverLink{
		SeriesID: series.ID, HardcoverSeriesID: catalog.ForeignID, HardcoverProviderID: catalog.ProviderID,
		HardcoverTitle: catalog.Title, HardcoverAuthorName: catalog.AuthorName, HardcoverBookCount: catalog.BookCount,
		Confidence: 1, LinkedBy: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	author := &models.Author{ForeignID: "hc:brandon-sanderson", Name: "Brandon Sanderson", SortName: "Sanderson, Brandon", MetadataProvider: "hardcover"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{
		ForeignID: "hc:the-way-of-kings", AuthorID: author.ID, Title: "The Way of Kings", SortTitle: "The Way of Kings",
		Status: models.BookStatusImported, Monitored: true, Genres: []string{}, MetadataProvider: "hardcover",
	}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	if _, err := seriesRepo.LinkBookIfMissing(ctx, series.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	if removed, err := seriesRepo.RemoveBookFromSeries(ctx, series.ID, book.ID); err != nil || !removed {
		t.Fatalf("remove from series: removed=%v err=%v", removed, err)
	}
	return h, seriesRepo, series, book
}

func seriesFillMembers(t *testing.T, repo *db.SeriesRepo, seriesID int64) int {
	t.Helper()
	books, err := repo.ListBooksInSeries(context.Background(), seriesID)
	if err != nil {
		t.Fatal(err)
	}
	return len(books)
}

// An explicit Add of one book the user took out of the series is the user
// asking for it back, so it rejoins the series and the exclusion is gone.
// The bulk fill must keep honouring the exclusion (#2554).
func TestSeriesFillSingleBookAddRejoinsExcludedBook(t *testing.T) {
	h, seriesRepo, series, book := seriesFillExcludedFixture(t)
	id := strconv.FormatInt(series.ID, 10)

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", bytes.NewBufferString(`{"foreignBookId":"hc:the-way-of-kings"}`)), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := seriesFillMembers(t, seriesRepo, series.ID); got != 1 {
		t.Fatalf("explicit Add left the book out of the series (members=%d, response %s)", got, rec.Body.String())
	}
	if left, err := seriesRepo.ListBookSeriesExclusions(context.Background(), book.ID); err != nil || len(left) != 0 {
		t.Fatalf("exclusion survived an explicit Add: %+v err=%v", left, err)
	}
}

func TestSeriesFillBulkKeepsExcludedBookOut(t *testing.T) {
	h, seriesRepo, series, book := seriesFillExcludedFixture(t)
	id := strconv.FormatInt(series.ID, 10)

	rec := httptest.NewRecorder()
	h.Fill(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/1/fill", nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := seriesFillMembers(t, seriesRepo, series.ID); got != 0 {
		t.Fatalf("bulk fill put an excluded book back (members=%d)", got)
	}
	if left, err := seriesRepo.ListBookSeriesExclusions(context.Background(), book.ID); err != nil || len(left) != 1 {
		t.Fatalf("bulk fill cleared the exclusion: %+v err=%v", left, err)
	}
}
