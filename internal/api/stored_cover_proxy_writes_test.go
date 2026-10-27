package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// The web client swaps the object a mutating endpoint returns straight into
// its state (setBook(updated), setBooks(books.map(...))), so a write that
// answers with the raw bindery-cover: reference turns a stored cover into
// "No cover" until the next reload (#3146). Every endpoint that hands back a
// book, author or series the library holds has to proxy it, not only the
// reads.

func requireProxiedCover(t *testing.T, label, got, stored string) {
	t.Helper()
	if stored == "" {
		t.Fatalf("%s: fixture has no stored image", label)
	}
	if want := ProxyImageURL(stored); got != want {
		t.Errorf("%s: imageUrl = %q, want %q", label, got, want)
	}
	if strings.HasPrefix(got, "bindery-cover:") {
		t.Errorf("%s: raw stored cover reference leaked: %q", label, got)
	}
}

func decodeBookBody(t *testing.T, label string, rec *httptest.ResponseRecorder, wantStatus int) models.Book {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("%s: status = %d, want %d: %s", label, rec.Code, wantStatus, rec.Body.String())
	}
	var b models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("%s: %v: %s", label, err, rec.Body.String())
	}
	return b
}

func storedCoverBook(t *testing.T, books *db.BookRepo, ctx context.Context, authorID int64, foreignID string, mutate func(*models.Book)) *models.Book {
	t.Helper()
	b := &models.Book{ForeignID: foreignID, AuthorID: authorID, Title: "Fjellvinden", SortTitle: "fjellvinden",
		Status: models.BookStatusWanted, MediaType: models.MediaTypeEbook, Genres: []string{},
		MetadataProvider: "openlibrary", Monitored: true, ImageURL: storedCoverRef}
	if mutate != nil {
		mutate(b)
	}
	if err := books.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBookUpdate_ProxiesStoredCover(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := storedCoverBook(t, books, ctx, author.ID, "SCU1", nil)
	id := strconv.FormatInt(book.ID, 10)

	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+id, strings.NewReader(`{"mediaType":"audiobook"}`)), "id", id)
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	got := decodeBookBody(t, "PUT /book/{id}", rec, http.StatusOK)
	requireProxiedCover(t, "PUT /book/{id}", got.ImageURL, storedCoverRef)
	if got.MediaType != models.MediaTypeAudiobook {
		t.Errorf("mediaType = %q, the update itself did not apply", got.MediaType)
	}

	// The proxying is on the response only: the row keeps the reference.
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil || stored == nil || stored.ImageURL != storedCoverRef {
		t.Fatalf("stored imageUrl = %+v, err %v; want the untouched reference", stored, err)
	}
}

func TestBookToggleExcluded_ProxiesStoredCover(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := storedCoverBook(t, books, ctx, author.ID, "SCU2", nil)
	id := strconv.FormatInt(book.ID, 10)

	rec := httptest.NewRecorder()
	h.ToggleExcluded(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+id+"/exclude", nil), "id", id))
	got := decodeBookBody(t, "PUT /book/{id}/exclude", rec, http.StatusOK)
	requireProxiedCover(t, "PUT /book/{id}/exclude", got.ImageURL, storedCoverRef)
	if !got.Excluded {
		t.Error("the book was not excluded")
	}
}

func TestBookDeleteFile_ProxiesStoredCover(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	path := filepath.Join(t.TempDir(), "Book.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := storedCoverBook(t, books, ctx, author.ID, "SCU3", func(b *models.Book) { b.Status = models.BookStatusImported })
	if err := books.SetFilePath(ctx, book.ID, path); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(book.ID, 10)

	// No format: the legacy single path.
	rec := httptest.NewRecorder()
	h.DeleteFile(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+id+"/file", nil), "id", id))
	got := decodeBookBody(t, "DELETE /book/{id}/file", rec, http.StatusOK)
	requireProxiedCover(t, "DELETE /book/{id}/file", got.ImageURL, storedCoverRef)
}

func TestBookDeleteFile_TrackedFormats_ProxyStoredCover(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "Book.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	book := storedCoverBook(t, books, ctx, author.ID, "SCU4", func(b *models.Book) { b.Status = models.BookStatusImported })
	if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, path); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(book.ID, 10)

	rec := httptest.NewRecorder()
	h.DeleteFile(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+id+"/file?format=ebook", nil), "id", id))
	got := decodeBookBody(t, "DELETE /book/{id}/file?format=ebook", rec, http.StatusOK)
	requireProxiedCover(t, "DELETE /book/{id}/file?format=ebook", got.ImageURL, storedCoverRef)
}

func TestEnrichAudiobook_ProxiesStoredCover(t *testing.T) {
	client := &editingAudnexClient{}
	h, books, book, ctx := enrichAudiobookFixture(t, client)
	if err := books.SetImageURL(ctx, book.ID, storedCoverRef); err != nil {
		t.Fatal(err)
	}

	rec := postEnrichAudiobook(h, book.ID)
	got := decodeBookBody(t, "POST /book/{id}/enrich-audiobook", rec, http.StatusOK)
	// Whatever enrichment left in the row, the response carries its proxy URL.
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	requireProxiedCover(t, "POST /book/{id}/enrich-audiobook", got.ImageURL, stored.ImageURL)
}

func TestRecommendationAdd_ProxiesBookCover(t *testing.T) {
	f := covSrNewRecFixture(t)
	ctx := context.Background()
	if err := f.authors.Create(ctx, &models.Author{ForeignID: "OL-RECA", Name: "Rec Author", SortName: "Author, Rec", MetadataProvider: "openlibrary", Monitored: true}); err != nil {
		t.Fatal(err)
	}
	f.h.WithEditionFetcher(func(context.Context, string) ([]models.Edition, error) { return nil, nil })
	rows := f.seed(t, 1, models.RecommendationCandidate{
		ForeignID: "OL-RECB", RecType: models.RecTypeAuthorNew, Title: "Recommended",
		AuthorName: "Rec Author", MediaType: models.MediaTypeEbook, Score: 1, ImageURL: storedCoverRef,
	})
	id := strconv.FormatInt(rows[0].ID, 10)

	rec := httptest.NewRecorder()
	f.h.Add(rec, covSrRecRequest(http.MethodPost, "/", "", 1, "", map[string]string{"id": id}))
	got := decodeBookBody(t, "POST /recommendations/{id}/add", rec, http.StatusCreated)
	requireProxiedCover(t, "POST /recommendations/{id}/add", got.ImageURL, storedCoverRef)
}

func authorCoverFixture(t *testing.T, provider metadata.Provider) (*AuthorHandler, *db.AuthorRepo, *db.BookRepo, *db.SeriesRepo, context.Context) {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	authors := db.NewAuthorRepo(database)
	books := db.NewBookRepo(database)
	series := db.NewSeriesRepo(database)
	profiles := db.NewMetadataProfileRepo(database)
	var agg *metadata.Aggregator
	if provider != nil {
		agg = metadata.NewAggregator(provider)
	}
	return NewAuthorHandler(authors, nil, books, series, agg, nil, profiles, nil), authors, books, series, context.Background()
}

func TestAuthorCreate_ProxiesPhoto(t *testing.T) {
	provider := &fixedAuthorProvider{result: &models.Author{
		ForeignID: "OL-PHOTO-A", Name: "Photo Author", SortName: "Author, Photo",
		MetadataProvider: "openlibrary", ImageURL: storedCoverRef,
	}}
	h, _, _, _, _ := authorCoverFixture(t, provider)
	body, _ := json.Marshal(map[string]any{"foreignAuthorId": "OL-PHOTO-A", "authorName": "Photo Author", "monitored": false})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	requireProxiedCover(t, "POST /author", got.ImageURL, storedCoverRef)
}

func TestAuthorUpdate_ProxiesPhoto(t *testing.T) {
	h, authors, _, _, ctx := authorCoverFixture(t, nil)
	a := &models.Author{ForeignID: "OL-UPD-A", Name: "Upd Author", SortName: "Author, Upd",
		MetadataProvider: "openlibrary", Monitored: true, ImageURL: storedCoverRef}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(a.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/author/"+id, strings.NewReader(`{"monitored":false}`)), "id", id)
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	requireProxiedCover(t, "PUT /author/{id}", got.ImageURL, storedCoverRef)
	for _, b := range got.Books {
		requireProxiedCover(t, "PUT /author/{id} embedded book", b.ImageURL, storedCoverRef)
	}
}

func TestAuthorAddBook_ProxiesCover(t *testing.T) {
	stub := addBookBackCatalogueStub(false)
	stub.works[0].ImageURL = storedCoverRef
	stub.getBookByID["OL27482W"].ImageURL = storedCoverRef
	f := newCoreFixture(t, stub)

	body, _ := json.Marshal(map[string]any{
		"foreignBookId":   wellsAddBookParams.ForeignBookID,
		"foreignAuthorId": wellsAddBookParams.ForeignAuthorID,
		"authorName":      wellsAddBookParams.AuthorName,
	})
	rec := httptest.NewRecorder()
	f.h.AddBook(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)))
	got := decodeBookBody(t, "POST /author/book", rec, http.StatusCreated)
	f.drain(t)
	stored, err := f.books.GetByForeignID(context.Background(), "OL27482W")
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	requireProxiedCover(t, "POST /author/book", got.ImageURL, stored.ImageURL)

	// The 409 for a second add names the library row, cover included.
	rec = httptest.NewRecorder()
	f.h.AddBook(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author/book", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second add status = %d: %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		ExistingBook models.Book `json:"existingBook"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	requireProxiedCover(t, "POST /author/book 409 existingBook", conflict.ExistingBook.ImageURL, stored.ImageURL)
}

func TestAuthorListSeries_ProxiesEmbeddedBookCovers(t *testing.T) {
	h, authors, books, series, ctx := authorCoverFixture(t, nil)
	a := &models.Author{ForeignID: "OL-LSC-A", Name: "LS Author", SortName: "Author, LS", MetadataProvider: "openlibrary", Monitored: true}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	book := storedCoverBook(t, books, ctx, a.ID, "OL-LSC-B", nil)
	s := &models.Series{ForeignID: "ol-series:lsc", Title: "Fjell"}
	if err := series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := series.LinkBook(ctx, s.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(a.ID, 10)

	rec := httptest.NewRecorder()
	h.ListSeries(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/author/"+id+"/series", nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got []models.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Books) != 1 || got[0].Books[0].Book == nil {
		t.Fatalf("unexpected series %+v", got)
	}
	requireProxiedCover(t, "GET /author/{id}/series", got[0].Books[0].Book.ImageURL, storedCoverRef)
}

// The series endpoints that answer with the series after a change embed the
// same books as the reads do.
func TestSeriesWrites_ProxyEmbeddedBookCovers(t *testing.T) {
	h, seriesRepo, authorRepo, bookRepo := seriesFixture(t)
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-SW-A", Name: "SW Author", SortName: "Author, SW"}
	if err := authorRepo.Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	book := storedCoverBook(t, bookRepo, ctx, author.ID, "OL-SW-B", nil)
	s := &models.Series{ForeignID: "OLSW1", Title: "Fjell"}
	if err := seriesRepo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := seriesRepo.LinkBook(ctx, s.ID, book.ID, "1", false); err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(s.ID, 10)
	bid := strconv.FormatInt(book.ID, 10)

	check := func(label string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", label, rec.Code, rec.Body.String())
		}
		var got models.Series
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Books) != 1 || got.Books[0].Book == nil {
			t.Fatalf("%s: unexpected series %+v", label, got)
		}
		requireProxiedCover(t, label, got.Books[0].Book.ImageURL, storedCoverRef)
	}

	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/series/"+sid, strings.NewReader(`{"title":"Fjell II"}`)), "id", sid))
	check("PUT /series/{id}", rec)

	rec = httptest.NewRecorder()
	h.AddBook(rec, withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/series/"+sid+"/books", strings.NewReader(`{"bookId":`+bid+`,"positionInSeries":"2"}`)), "id", sid))
	check("POST /series/{id}/books", rec)

	rec = httptest.NewRecorder()
	h.SetPrimaryBook(rec, withURLParams(httptest.NewRequest(http.MethodPut, "/api/v1/series/"+sid+"/books/"+bid+"/primary", nil),
		map[string]string{"id": sid, "bookId": bid}))
	check("PUT /series/{id}/books/{bookId}/primary", rec)
}
