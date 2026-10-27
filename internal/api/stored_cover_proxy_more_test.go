package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

func TestBookDeleteFile_IndividualPaths_ProxyStoredCover(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query func(path string) string
	}{
		{"delete one tracked file", func(p string) string { return "path=" + url.QueryEscape(p) + "&delete=true" }},
		{"deregister one tracked file", func(p string) string { return "path=" + url.QueryEscape(p) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, books, _, author, ctx := bookFixture(t)
			tmp := t.TempDir()
			first := filepath.Join(tmp, "first.epub")
			second := filepath.Join(tmp, "second.epub")
			for _, p := range []string{first, second} {
				if err := os.WriteFile(p, []byte(p), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			book := storedCoverBook(t, books, ctx, author.ID, "SCU-IND", func(b *models.Book) { b.Status = models.BookStatusImported })
			for _, p := range []string{first, second} {
				if err := books.AddBookFile(ctx, book.ID, models.MediaTypeEbook, p); err != nil {
					t.Fatal(err)
				}
			}
			id := strconv.FormatInt(book.ID, 10)
			rec := httptest.NewRecorder()
			h.DeleteFile(rec, withURLParam(httptest.NewRequest(http.MethodDelete, "/api/v1/book/"+id+"/file?"+tc.query(first), nil), "id", id))
			got := decodeBookBody(t, tc.name, rec, http.StatusOK)
			requireProxiedCover(t, tc.name, got.ImageURL, storedCoverRef)
		})
	}
}

func TestBookGet_ProxiesStoredCover(t *testing.T) {
	h, books, _, author, ctx := bookFixture(t)
	book := storedCoverBook(t, books, ctx, author.ID, "SCU-GET", nil)
	id := strconv.FormatInt(book.ID, 10)
	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/book/"+id, nil), "id", id))
	got := decodeBookBody(t, "GET /book/{id}", rec, http.StatusOK)
	requireProxiedCover(t, "GET /book/{id}", got.ImageURL, storedCoverRef)
}

func TestBookRebind_ProxiesStoredCover(t *testing.T) {
	h, books, _, _, author, book, ctx := rebindFixture(t)
	if err := books.SetImageURL(ctx, book.ID, storedCoverRef); err != nil {
		t.Fatal(err)
	}
	h.WithMetaLookup(&stubLookup{book: &models.Book{
		Title: "Correct Book", SortTitle: "Correct Book", Genres: []string{},
		Author: &models.Author{ForeignID: author.ForeignID, Name: author.Name},
	}})
	rec := httptest.NewRecorder()
	h.Rebind(rec, rebindRequest(book.ID, map[string]any{"provider": "openlibrary", "foreign_id": "OL2W"}))
	got := decodeBookBody(t, "POST /book/{id}/rebind", rec, http.StatusOK)
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	requireProxiedCover(t, "POST /book/{id}/rebind", got.ImageURL, stored.ImageURL)
}

func TestAuthorGet_ProxiesPhoto(t *testing.T) {
	h, authors, _, _, ctx := authorCoverFixture(t, nil)
	a := &models.Author{ForeignID: "OL-GET-A", Name: "Get Author", SortName: "Author, Get",
		MetadataProvider: "openlibrary", Monitored: true, ImageURL: storedCoverRef}
	if err := authors.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(a.ID, 10)
	rec := httptest.NewRecorder()
	h.Get(rec, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/author/"+id, nil), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	requireProxiedCover(t, "GET /author/{id}", got.ImageURL, storedCoverRef)
}

// Adding an author that is already there answers 200 with the existing row
// relinked upstream, and 409 with the library row when it cannot.
func TestAuthorCreate_ExistingAuthor_ProxiesPhoto(t *testing.T) {
	provider := &fixedAuthorProvider{result: &models.Author{
		ForeignID: "OL26320A", Name: "J.R.R. Tolkien", SortName: "Tolkien, J.R.R.",
		MetadataProvider: "openlibrary", ImageURL: storedCoverRef,
	}}
	h, authors, _, _, ctx := authorCoverFixture(t, provider)
	existing := &models.Author{ForeignID: "abs:author:lib:tolkien", Name: "J. R. R. Tolkien", SortName: "Tolkien, J. R. R.",
		MetadataProvider: "audiobookshelf", Monitored: true}
	if err := authors.Create(ctx, existing); err != nil {
		t.Fatal(err)
	}
	post := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"foreignAuthorId": "OL26320A", "authorName": "J.R.R. Tolkien", "monitored": true})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		return rec
	}

	rec := post()
	if rec.Code != http.StatusOK {
		t.Fatalf("relink status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	stored, err := authors.GetByID(ctx, existing.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	requireProxiedCover(t, "POST /author (existing, 200)", got.ImageURL, stored.ImageURL)

	// Second add: now a plain duplicate, 409 with the canonical row.
	rec = post()
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d: %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		CanonicalAuthor models.Author `json:"canonicalAuthor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	requireProxiedCover(t, "POST /author 409 canonicalAuthor", conflict.CanonicalAuthor.ImageURL, stored.ImageURL)
}

func TestAuthorRelinkUpstream_ProxiesPhoto(t *testing.T) {
	f := newRelinkUpstreamFixture(t, &searchableAuthorProvider{
		authors: map[string]*models.Author{
			"OL26320A": {ForeignID: "OL26320A", Name: "J.R.R. Tolkien", SortName: "Tolkien, J.R.R.",
				MetadataProvider: "openlibrary", ImageURL: storedCoverRef},
		},
	})
	existing := f.createAuthor(t, &models.Author{ForeignID: "abs:author:lib:tolkien", Name: "J. R. R. Tolkien",
		SortName: "Tolkien, J. R. R.", MetadataProvider: "audiobookshelf", Monitored: true})

	rec := f.relinkToCandidate(t, existing.ID, "OL26320A", "J.R.R. Tolkien")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got models.Author
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	stored, err := f.authors.GetByID(f.ctx, existing.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload: %v", err)
	}
	requireProxiedCover(t, "POST /author/{id}/relink-upstream", got.ImageURL, stored.ImageURL)
}
