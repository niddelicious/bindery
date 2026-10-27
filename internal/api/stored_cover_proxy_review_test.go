package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/duplicates"
	"github.com/vavallee/bindery/internal/importer"
	"github.com/vavallee/bindery/internal/models"
)

// fillAll sets every exported field of v to a non zero value, recursively, so a
// field added to a model later is covered by the copy tests below without
// anyone remembering to list it. depth caps the Author <-> Book cycle.
func fillAll(v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Ptr:
		if depth <= 0 {
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fillAll(v.Elem(), depth-1)
	case reflect.Slice:
		if depth <= 0 {
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillAll(v.Index(0), depth-1)
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String && v.Type().Elem().Kind() == reflect.String {
			v.Set(reflect.MakeMap(v.Type()))
			v.SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf("v"))
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fillAll(v.Field(i), depth)
			}
		}
	}
}

// withoutImageURL drops every imageUrl key, at any depth, from decoded JSON.
func withoutImageURL(v any) any {
	switch t := v.(type) {
	case map[string]any:
		delete(t, "imageUrl")
		for k := range t {
			t[k] = withoutImageURL(t[k])
		}
	case []any:
		for i := range t {
			t[i] = withoutImageURL(t[i])
		}
	}
	return v
}

func jsonOf(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sansImages(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return withoutImageURL(v)
}

// The proxied copy of a book, author or series is the original except for its
// image fields, whatever fields the models grow, and writing it leaves the live
// struct (which a background search may still hold) untouched.
func TestProxyWriters_CopyDiffersOnlyInImages(t *testing.T) {
	var book models.Book
	fillAll(reflect.ValueOf(&book).Elem(), 3)
	book.ImageURL = storedCoverRef
	book.Author.ImageURL = "https://img.example/a.jpg"

	var author models.Author
	fillAll(reflect.ValueOf(&author).Elem(), 3)
	author.ImageURL = "https://img.example/a.jpg"
	author.Books[0].ImageURL = storedCoverRef

	var series models.Series
	fillAll(reflect.ValueOf(&series).Elem(), 3)
	series.Books[0].Book.ImageURL = storedCoverRef

	tests := []struct {
		name  string
		live  any
		write func(*httptest.ResponseRecorder)
	}{
		{"book", &book, func(w *httptest.ResponseRecorder) { writeBook(w, http.StatusOK, &book) }},
		{"author", &author, func(w *httptest.ResponseRecorder) { writeAuthor(w, http.StatusOK, &author) }},
		{"series", &series, func(w *httptest.ResponseRecorder) { writeSeries(w, http.StatusOK, &series) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := jsonOf(t, tc.live)
			rec := httptest.NewRecorder()
			tc.write(rec)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			if !reflect.DeepEqual(sansImages(t, before), sansImages(t, rec.Body.Bytes())) {
				t.Errorf("the response differs from the live struct beyond its image fields:\nlive %s\nresp %s", before, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "bindery-cover:") {
				t.Errorf("raw cover reference in the response: %s", rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "/api/v1/images?url=") {
				t.Errorf("no proxied image in the response: %s", rec.Body.String())
			}
			if after := jsonOf(t, tc.live); string(after) != string(before) {
				t.Errorf("writing the response changed the live struct:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func TestProxyWriters_NilAndEmpty(t *testing.T) {
	for name, write := range map[string]func(*httptest.ResponseRecorder){
		"book":   func(w *httptest.ResponseRecorder) { writeBook(w, http.StatusOK, nil) },
		"author": func(w *httptest.ResponseRecorder) { writeAuthor(w, http.StatusOK, nil) },
		"series": func(w *httptest.ResponseRecorder) { writeSeries(w, http.StatusOK, nil) },
	} {
		rec := httptest.NewRecorder()
		write(rec)
		if strings.TrimSpace(rec.Body.String()) != "null" {
			t.Errorf("%s: nil answered %q", name, rec.Body.String())
		}
	}
	if proxiedBook(nil) != nil || proxiedAuthor(nil) != nil || proxiedBooks(nil) != nil {
		t.Error("nil in must stay nil")
	}
	// A series with no books, and one whose link has no joined book.
	rec := httptest.NewRecorder()
	writeSeries(rec, http.StatusOK, &models.Series{ID: 1, Books: []models.SeriesBook{{BookID: 2}}})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "panic") {
		t.Errorf("series with an unjoined book: %d %s", rec.Code, rec.Body.String())
	}
	if got := proxiedAuthor(&models.Author{ImageURL: ""}); got.Books != nil {
		t.Errorf("an author without books gained %v", got.Books)
	}
}

// ProxyImageURL must give the same answer when applied to its own output, on
// any base path, or a cover is wrapped twice and 404s.
func TestProxyImageURL_Idempotent(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 5000) + ".jpg"
	inputs := []string{
		"", "https://example.com/c.jpg", "http://example.com/c.jpg?w=100&h=2#frag",
		storedCoverRef, "data:image/png;base64,AAAA", "relative/cover.jpg", "/already/local.jpg", long,
	}
	for _, base := range []string{"", "/bindery", "/a/b"} {
		prev := imageProxyBase
		SetImageProxyBase(base)
		for _, in := range inputs {
			once := ProxyImageURL(in)
			if twice := ProxyImageURL(once); twice != once {
				t.Errorf("base %q: ProxyImageURL not idempotent for %.60q:\n once  %.80s\n twice %.80s", base, in, once, twice)
			}
			if in != "" && !strings.HasPrefix(in, "/") {
				if !strings.HasPrefix(once, base+"/api/v1/images?url=") {
					t.Errorf("base %q: %.60q proxied to %.80s, want the base path in front", base, in, once)
				}
			}
		}
		SetImageProxyBase(prev)
	}
}

// No handler stores an image URL the client sent, so a proxied URL the web
// client got in a response cannot come back and be persisted (#3146).
func TestClientImageURLIsNeverPersisted(t *testing.T) {
	proxied := ProxyImageURL(storedCoverRef)

	h, books, _, author, ctx := bookFixture(t)
	book := storedCoverBook(t, books, ctx, author.ID, "NOPERSIST1", nil)
	id := strconv.FormatInt(book.ID, 10)
	body := `{"title":"Renamed","imageUrl":` + strconv.Quote(proxied) + `,"image_url":"https://evil.example/x.jpg"}`
	rec := httptest.NewRecorder()
	h.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/book/"+id, strings.NewReader(body)), "id", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /book: %d %s", rec.Code, rec.Body.String())
	}
	stored, err := books.GetByID(ctx, book.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if stored.ImageURL != storedCoverRef || stored.Title != "Renamed" {
		t.Errorf("PUT /book stored imageUrl %q title %q; the client imageUrl must be ignored", stored.ImageURL, stored.Title)
	}

	ah, authors, _, _, actx := authorCoverFixture(t, nil)
	a := &models.Author{ForeignID: "OL-NOPERSIST", Name: "N Author", SortName: "Author, N",
		MetadataProvider: "openlibrary", Monitored: true, ImageURL: "https://img.example/a.jpg"}
	if err := authors.Create(actx, a); err != nil {
		t.Fatal(err)
	}
	aid := strconv.FormatInt(a.ID, 10)
	body = `{"monitored":false,"imageUrl":` + strconv.Quote(ProxyImageURL(a.ImageURL)) + `}`
	rec = httptest.NewRecorder()
	ah.Update(rec, withURLParam(httptest.NewRequest(http.MethodPut, "/api/v1/author/"+aid, strings.NewReader(body)), "id", aid))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /author: %d %s", rec.Code, rec.Body.String())
	}
	got, err := authors.GetByID(actx, a.ID)
	if err != nil || got == nil || got.ImageURL != "https://img.example/a.jpg" {
		t.Errorf("PUT /author stored %+v, err %v; the client imageUrl must be ignored", got, err)
	}
}

// The manual import lookup and the duplicate review embed whole library books,
// so their stored covers go through the proxy too (#3146).
func TestLookupAndDuplicateGroups_ProxyStoredCovers(t *testing.T) {
	groups := []duplicates.Group{{Members: []duplicates.Member{{Book: models.Book{ID: 1, ImageURL: storedCoverRef}}}}}
	proxyDuplicateGroups(groups)
	if got := groups[0].Members[0].ImageURL; got != ProxyImageURL(storedCoverRef) {
		t.Errorf("duplicate member imageUrl = %q", got)
	}
}

// Both duplicate reviews embed whole library books, so each answer proxies the
// stored cover, and the cached scan behind the library review keeps the raw one.
func TestDuplicateReviews_ProxyStoredCovers(t *testing.T) {
	f := newLibDupFixture(t)
	a := f.author(t, "Kristin Hannah", 0)
	b1 := f.addBook(t, a.ID, "Nightingale", "OL7001W", false)
	b2 := f.addBook(t, a.ID, "The Nightingale", "OL7002W", false)
	if _, err := f.database.ExecContext(f.ctx, `UPDATE books SET image_url = ? WHERE id IN (?, ?)`, storedCoverRef, b1.ID, b2.ID); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ { // twice: the second answer comes from the cached scan
		req := httptest.NewRequest(http.MethodGet, "/api/v1/library/duplicate-candidates", nil)
		rec := httptest.NewRecorder()
		f.lib.List(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Nightingale") {
			t.Fatalf("library review = %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "bindery-cover:") || !strings.Contains(rec.Body.String(), "/api/v1/images?url=") {
			t.Errorf("GET /library/duplicate-candidates pass %d: cover not proxied: %s", i, rec.Body.String())
		}
	}

	rec := f.get(t, a.ID, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "bindery-cover:") {
		t.Errorf("GET /author/{id}/duplicate-candidates leaked a raw cover: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/api/v1/images?url=") {
		t.Errorf("GET /author/{id}/duplicate-candidates: no proxied cover: %s", rec.Body.String())
	}
}

func TestManualImportLookup_ProxiesStoredCovers(t *testing.T) {
	t.Parallel()
	h, stub, _, _, _ := manualImportFixture(t)
	matched := models.Book{ID: 1, Title: "Fjellvinden", ImageURL: storedCoverRef}
	stub.lookupResult = importer.LookupResult{Match: "ambiguous", Book: &matched, Candidates: []models.Book{matched}}
	file := filepath.Join(t.TempDir(), "Fjellvinden.epub")
	writeTestFile(t, file)

	rec := httptest.NewRecorder()
	h.Lookup(rec, lookupRequest(file))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got importer.LookupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Book == nil || len(got.Candidates) != 1 {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	requireProxiedCover(t, "lookup book", got.Book.ImageURL, storedCoverRef)
	requireProxiedCover(t, "lookup candidate", got.Candidates[0].ImageURL, storedCoverRef)
	if matched.ImageURL != storedCoverRef {
		t.Error("the scanner's own book was rewritten in place")
	}
}
