package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// A cover Bindery stores itself is a bindery-cover: reference that only the
// image proxy can serve, so every endpoint that returns books must hand it
// out as a proxy URL. Wanted and the series endpoints returned it raw, and
// the page showed a broken image (#3146).

var storedCoverRef = "bindery-cover:" + strings.Repeat("ab", 32) + ".jpg"

func TestListWanted_ProxiesStoredCovers(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	b := &models.Book{ForeignID: "SC1", AuthorID: author.ID, Title: "Fjellvinden", SortTitle: "fjellvinden",
		Status: models.BookStatusWanted, Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true, ImageURL: storedCoverRef}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ListWanted(rec, httptest.NewRequest(http.MethodGet, "/api/v1/wanted/missing", nil))
	var got []models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if len(got) != 1 || got[0].ImageURL != ProxyImageURL(storedCoverRef) {
		t.Fatalf("wanted imageUrl = %+v, want %q", got, ProxyImageURL(storedCoverRef))
	}
}

func TestSeries_ProxiesStoredCovers(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL1A", Name: "Kari Nordmann", SortName: "Nordmann, Kari"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := &models.Book{ForeignID: "OL1B", AuthorID: author.ID, Title: "Fjellvinden", Status: models.BookStatusWanted, ImageURL: storedCoverRef}
	if err := bookRepo.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	s := &models.Series{ForeignID: "OLSER1", Title: "Fjell"}
	if err := seriesRepo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	want := ProxyImageURL(storedCoverRef)
	firstCover := func(series []models.Series) string {
		if len(series) != 1 || len(series[0].Books) != 1 || series[0].Books[0].Book == nil {
			t.Fatalf("unexpected series %+v", series)
		}
		return series[0].Books[0].Book.ImageURL
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series", nil))
	var list []models.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if got := firstCover(list); got != want {
		t.Errorf("list imageUrl = %q, want %q", got, want)
	}

	rec = httptest.NewRecorder()
	h.List(rec, httptest.NewRequest(http.MethodGet, "/api/v1/series?limit=10", nil))
	var page seriesListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if got := firstCover(page.Items); got != want {
		t.Errorf("paged list imageUrl = %q, want %q", got, want)
	}

	id := strconv.FormatInt(s.ID, 10)
	rec = httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/series/"+id, nil), "id", id))
	var one models.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if got := firstCover([]models.Series{one}); got != want {
		t.Errorf("get imageUrl = %q, want %q", got, want)
	}

	// The Hardcover comparison lists the series' own books the same way.
	if got := localDiffBook(models.SeriesBook{Book: book}).ImageURL; got != want {
		t.Errorf("diff local imageUrl = %q, want %q", got, want)
	}
}
