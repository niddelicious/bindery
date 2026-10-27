package bnf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
)

// The testdata responses are real BnF SRU answers retrieved on 2026-10-11
// (see testdata/SOURCE.md), with third party summaries replaced.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixtureServer answers each SRU query with the fixture route picks, and
// records the queries it was sent.
type fixtureServer struct {
	*httptest.Server
	mu      sync.Mutex
	queries []string
}

func newFixtureServer(t *testing.T, route func(v url.Values) string) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		fs.mu.Lock()
		fs.queries = append(fs.queries, q)
		fs.mu.Unlock()
		name := route(r.URL.Query())
		if name == "" {
			name = "empty.xml"
		}
		w.Header().Set("Content-Type", "text/xml;charset=UTF-8")
		_, _ = w.Write(fixture(t, name))
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fixtureServer) sent() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.queries...)
}

func testClient(base string) *Client {
	return &Client{http: &http.Client{}, base: base}
}

// carrollRoute answers like BnF would for the Lewis Carroll fixtures. Only
// the first page of a result has records.
func carrollRoute(v url.Values) string {
	q := strings.ToLower(v.Get("query"))
	if s := v.Get("startRecord"); s != "" && s != "1" {
		return "empty.xml"
	}
	switch {
	case strings.Contains(q, "aut.accesspoint"):
		if strings.Contains(q, "carroll") {
			return "authority_search_carroll.xml"
		}
		return "empty.xml"
	case strings.Contains(q, "aut.recordid"):
		if strings.Contains(q, `"11885918"`) {
			return "authority_carroll.xml"
		}
		return "empty.xml"
	case strings.Contains(q, "bib.creationdate"):
		return "recent_carroll.xml"
	case strings.Contains(q, "bib.author2bib") && strings.Contains(q, "bib.title"):
		return "isbn_miroir.xml"
	case strings.Contains(q, "bib.author2bib"):
		return "author_works_carroll.xml"
	case strings.Contains(q, "bib.fuzzyisbn"):
		return "isbn_miroir.xml"
	case strings.Contains(q, "bib.persistentid"):
		return "record_miroir.xml"
	case strings.Contains(q, "outsider"):
		return "search_outsider.xml"
	}
	return "search_alice.xml"
}

func TestSearchBooks_FrenchEditionTitleTranslatorIsNotAuthor(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	books, err := testClient(fs.URL).SearchBooks(context.Background(), "alice pays merveilles carroll")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) == 0 {
		t.Fatal("no books")
	}
	got := books[0]
	if got.Title != "Alice au pays des merveilles" {
		t.Errorf("title = %q, want the French edition title", got.Title)
	}
	if got.Language != "fre" {
		t.Errorf("language = %q, want fre", got.Language)
	}
	if got.Author == nil || got.Author.Name != "Lewis Carroll" || got.Author.ForeignID != "bnf:author:11885918" {
		t.Errorf("author = %+v, want Lewis Carroll linked to bnf:author:11885918", got.Author)
	}
	if got.ImageURL != "" {
		t.Errorf("ImageURL = %q, want none: BnF covers are never used", got.ImageURL)
	}
	if !strings.HasPrefix(got.ForeignID, "bnf:cb") {
		t.Errorf("ForeignID = %q, want the record's ARK", got.ForeignID)
	}
	for _, b := range books {
		if b.Author != nil && strings.Contains(b.Author.Name, "Bué") {
			t.Errorf("translator became the author of %q", b.Title)
		}
	}
	q := fs.sent()[0]
	if !strings.HasPrefix(q, `bib.anywhere all "alice pays merveilles carroll"`) || !strings.HasSuffix(q, ` and bib.doctype any "a"`) {
		t.Errorf("query = %q, want an anywhere search restricted to printed text", q)
	}
}

// The 2026 converted record (no authority links, roles as text) joins the
// work of the linked records with the same title instead of becoming a
// second, authorless book.
func TestSearchBooks_UnlinkedRecordJoinsItsWork(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	books, err := testClient(fs.URL).SearchBooks(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	var alice []string
	for _, b := range books {
		if b.Title == "Alice au pays des merveilles" {
			alice = append(alice, b.ForeignID)
			if b.Author == nil || b.Author.ForeignID != "bnf:author:11885918" {
				t.Errorf("%s author = %+v, want the linked Lewis Carroll", b.ForeignID, b.Author)
			}
		}
	}
	if len(alice) != 1 {
		t.Errorf("works titled Alice au pays des merveilles = %v, want one", alice)
	}
}

func TestSearchBooks_OriginalTitleAndElision(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	books, err := testClient(fs.URL).SearchBooks(context.Background(), "L'Outsider")
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %d, want the editions folded into one work", len(books))
	}
	b := books[0]
	if b.Title != "L'outsider" || b.OriginalTitle != "The outsider" {
		t.Errorf("title = %q original = %q, want L'outsider / The outsider", b.Title, b.OriginalTitle)
	}
	if b.Author == nil || b.Author.Name != "Stephen King" {
		t.Errorf("author = %+v, want Stephen King (Jean Esch is the translator)", b.Author)
	}
	if len(b.Editions) != 3 {
		t.Errorf("editions = %d, want 3", len(b.Editions))
	}
	// The earliest entered French record represents the work.
	if b.ForeignID != "bnf:cb456640410" {
		t.Errorf("ForeignID = %q, want bnf:cb456640410 (entered 2019-02-06)", b.ForeignID)
	}
	if b.Description == "" {
		t.Error("publisher summary dropped, want it kept as the description")
	}
	if q := fs.sent()[0]; !strings.Contains(q, `"L'Outsider"`) {
		t.Errorf("query = %q, want the elided article kept", q)
	}
}

func TestGetBookByISBN(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	b, err := testClient(fs.URL).GetBookByISBN(context.Background(), "2-07-062897-3")
	if err != nil {
		t.Fatal(err)
	}
	if b == nil || b.Title != "Ce qu'Alice trouva de l'autre côté du miroir" {
		t.Fatalf("book = %+v", b)
	}
	ed := b.Editions[0]
	if ed.ISBN13 == nil || *ed.ISBN13 != "9782070628971" || ed.Publisher != "Gallimard-Jeunesse" || ed.NumPages == nil || *ed.NumPages != 185 {
		t.Errorf("edition = %+v", ed)
	}
	if b.ReleaseDate == nil || b.ReleaseDate.Year() != 2009 {
		t.Errorf("release date = %v, want 2009", b.ReleaseDate)
	}
	// Electre's summary is a third party's text, not BnF data.
	if b.Description != "" {
		t.Errorf("description = %q, want the Electre summary left out", b.Description)
	}
	if q := fs.sent()[0]; q != `bib.fuzzyIsbn all "2070628973" and bib.doctype any "a"` {
		t.Errorf("query = %q", q)
	}
}

func TestSearchBooks_ISBNQueryUsesISBNLookup(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	books, err := testClient(fs.URL).SearchBooks(context.Background(), "isbn:9782070628971")
	if err != nil || len(books) != 1 {
		t.Fatalf("books = %d err = %v", len(books), err)
	}
	if q := fs.sent()[0]; !strings.HasPrefix(q, "bib.fuzzyIsbn") {
		t.Errorf("query = %q, want an ISBN lookup", q)
	}
}

func TestSearchAuthors_AuthorityHeadingsOnly(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	authors, err := testClient(fs.URL).SearchAuthors(context.Background(), "carroll lewis")
	if err != nil {
		t.Fatal(err)
	}
	// Johnny Carroll's record matches the query only through his variant
	// "Carroll, Johnny Lewis", so he is not offered.
	if len(authors) != 1 || authors[0].ForeignID != "bnf:author:11885918" || authors[0].Name != "Lewis Carroll" || authors[0].SortName != "Carroll, Lewis" {
		t.Fatalf("authors = %+v, want only Lewis Carroll", authors)
	}
	if q := fs.sent()[0]; q != `aut.accesspoint all "carroll lewis" and aut.type any "pep"` {
		t.Errorf("query = %q, want a person authority search", q)
	}
}

func TestGetAuthor(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	c := testClient(fs.URL)
	a, err := c.GetAuthor(context.Background(), "bnf:author:11885918")
	if err != nil || a == nil {
		t.Fatalf("author = %+v err = %v", a, err)
	}
	if a.Name != "Lewis Carroll" || a.ForeignID != "bnf:author:11885918" || a.MetadataProvider != "bnf" {
		t.Errorf("author = %+v", a)
	}
	found := false
	for _, n := range a.AlternateNames {
		if n == "Charles Lutwidge Dodgson" {
			found = true
		}
	}
	if !found {
		t.Errorf("alternate names = %v, want the authority record's variants", a.AlternateNames)
	}
	if q := fs.sent()[0]; q != `aut.recordid all "11885918" and aut.type any "pep"` {
		t.Errorf("query = %q, want a person authority lookup", q)
	}

	missing, err := c.GetAuthor(context.Background(), "bnf:author:99999999")
	if err != nil || missing != nil {
		t.Errorf("missing author = %+v, %v; want nil, nil", missing, err)
	}
	for _, bad := range []string{"bnf:author:1\" or aut.type any \"dew", "nb:author:11885918", "bnf:author:"} {
		if _, err := c.GetAuthor(context.Background(), bad); err == nil {
			t.Errorf("GetAuthor(%q) accepted a malformed id", bad)
		}
	}
}

func TestGetAuthorWorksSnapshot(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	books, complete, err := testClient(fs.URL).GetAuthorWorksSnapshot(context.Background(), "bnf:author:11885918")
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Error("complete = true; BnF's unlinked 2026 records mean the catalogue is never complete")
	}
	titles := map[string]bool{}
	for _, b := range books {
		titles[b.Title] = true
		if b.Author == nil || b.Author.ForeignID != "bnf:author:11885918" {
			t.Errorf("%q author = %+v", b.Title, b.Author)
		}
	}
	// Magazine issues (461 $x) are not books; Disney's "d'après Lewis
	// Carroll" credits him with relator 100, not as author.
	for _, unwanted := range []string{"Mille et une histoires", "J'aime lire"} {
		if titles[unwanted] {
			t.Errorf("%q kept: an issue of a periodical is not a book", unwanted)
		}
	}
	if !titles["Ce qu'Alice trouva de l'autre côté du miroir"] {
		t.Errorf("titles = %v, want the authored translation", titles)
	}
	// The unlinked 2026 record found by name joins the catalogue.
	if !titles["Alice au pays des merveilles"] {
		t.Errorf("titles = %v, want the 2026 Hatier edition adopted by name", titles)
	}
	sent := fs.sent()
	// The fixture claims 704 records but the fake server's second page is
	// empty, which ends the walk. The recent record is adopted only after one
	// lookup shows no namesake shares the heading.
	if len(sent) != 5 || !strings.HasPrefix(sent[4], `aut.accesspoint all "Carroll Lewis"`) {
		t.Errorf("requests = %v, want authority, two author2bib pages, the recent name query and the namesake check", sent)
	}
}

func TestGetAuthorWorksSnapshot_MissingAuthorityIsNotAnEmptyCatalogue(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	books, complete, err := testClient(fs.URL).GetAuthorWorksSnapshot(context.Background(), "bnf:author:99999999")
	if err != nil || books != nil || complete {
		t.Errorf("= %v, %v, %v; want nil, false, nil", books, complete, err)
	}
	if len(fs.sent()) != 1 {
		t.Errorf("requests = %v, want only the authority lookup", fs.sent())
	}
}

func TestGetBook(t *testing.T) {
	fs := newFixtureServer(t, carrollRoute)
	c := testClient(fs.URL)
	b, err := c.GetBook(context.Background(), "bnf:cb434747912")
	if err != nil || b == nil {
		t.Fatalf("book = %+v err = %v", b, err)
	}
	if b.ForeignID != "bnf:cb434747912" || b.Title != "Ce qu'Alice trouva de l'autre côté du miroir" {
		t.Errorf("book = %+v", b)
	}
	sent := fs.sent()
	if len(sent) == 0 || sent[0] != `bib.persistentid all "ark:/12148/cb434747912"` {
		t.Errorf("queries = %v", sent)
	}
	for _, bad := range []string{"bnf:cb4347479\" or \"x", "bnf:", "dnb:123", "bnf:../x"} {
		if _, err := c.GetBook(context.Background(), bad); err == nil {
			t.Errorf("GetBook(%q) accepted a malformed id", bad)
		}
	}
}

// GetBook returns the whole work: the requested record plus the other
// editions found by author and title, under the ID that was asked for.
func TestGetBook_AddsTheWorksOtherEditions(t *testing.T) {
	rec := func(ark, entered, isbn string) string {
		return `<record><recordData><record id="ark:/12148/` + ark + `" type="Bibliographic"><leader>     nam  22        450 </leader>` +
			`<datafield tag="010" ind1=" " ind2=" "><subfield code="a">` + isbn + `</subfield></datafield>` +
			`<datafield tag="100" ind1=" " ind2=" "><subfield code="a">` + entered + `d2000    m  y0frey50      ba</subfield></datafield>` +
			`<datafield tag="101" ind1="0" ind2=" "><subfield code="a">fre</subfield></datafield>` +
			`<datafield tag="200" ind1="1" ind2=" "><subfield code="a">Alice au pays des merveilles</subfield></datafield>` +
			`<datafield tag="700" ind1=" " ind2=" "><subfield code="3">11885918</subfield><subfield code="a">Carroll</subfield><subfield code="b">Lewis</subfield><subfield code="4">070</subfield></datafield>` +
			`</record></recordData></record>`
	}
	wrap := func(records string) string {
		return `<searchRetrieveResponse><numberOfRecords>2</numberOfRecords><records>` + records + `</records></searchRetrieveResponse>`
	}
	fs := &fixtureServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		fs.mu.Lock()
		fs.queries = append(fs.queries, q)
		fs.mu.Unlock()
		switch {
		case strings.Contains(q, "bib.persistentid"):
			_, _ = w.Write([]byte(wrap(rec("cb20000002b", "20050101", "978-2-07-000001-0"))))
		case strings.Contains(q, "bib.author2bib"):
			_, _ = w.Write([]byte(wrap(rec("cb20000001c", "19900101", "978-2-07-000002-7") + rec("cb20000002b", "20050101", "978-2-07-000001-0"))))
		default:
			_, _ = w.Write([]byte(emptySRU))
		}
	}))
	defer fs.Close()
	b, err := testClient(fs.URL).GetBook(context.Background(), "bnf:cb20000002b")
	if err != nil || b == nil {
		t.Fatalf("book = %+v err = %v", b, err)
	}
	if b.ForeignID != "bnf:cb20000002b" {
		t.Errorf("ForeignID = %q, want the requested record's", b.ForeignID)
	}
	if len(b.Editions) != 2 {
		t.Errorf("editions = %d, want the requested record and the other edition of the work", len(b.Editions))
	}
	sent := fs.sent()
	if len(sent) != 2 || sent[1] != `bib.author2bib any "11885918" and bib.title all "Alice au pays des merveilles" and bib.doctype any "a"` {
		t.Errorf("queries = %v, want the record, then its work by author and title", sent)
	}
}

func TestEmptyResultIsNotAnError(t *testing.T) {
	fs := newFixtureServer(t, func(url.Values) string { return "empty.xml" })
	c := testClient(fs.URL)
	books, err := c.SearchBooks(context.Background(), "zzqxbinderynothing")
	if err != nil || len(books) != 0 {
		t.Errorf("SearchBooks = %v, %v; want empty, nil", books, err)
	}
	b, err := c.GetBookByISBN(context.Background(), "9782070628971")
	if err != nil || b != nil {
		t.Errorf("GetBookByISBN = %v, %v; want nil, nil", b, err)
	}
}

func TestDiagnosticIsAnError(t *testing.T) {
	fs := newFixtureServer(t, func(url.Values) string { return "diagnostic.xml" })
	_, err := testClient(fs.URL).SearchBooks(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "Index non supporté") {
		t.Errorf("err = %v, want the SRU diagnostic", err)
	}
}

func TestZeroValueClientIsNotConfigured(t *testing.T) {
	var c Client
	if _, err := c.SearchBooks(context.Background(), "x"); !errors.Is(err, metadata.ErrProviderNotConfigured) {
		t.Errorf("err = %v", err)
	}
}

// A malformed or truncated body is a decode error, never a panic or a
// silently short result.
func TestGarbageBodyIsAnError(t *testing.T) {
	for _, body := range []string{"", "<not xml", "<searchRetrieveResponse><numberOfRecords>x</numberOfRecords><records><record><recordData><record type=\"Bibliographic\"><datafield tag=\"200\">"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := testClient(srv.URL).SearchBooks(context.Background(), "x")
		srv.Close()
		if err == nil {
			t.Errorf("body %q: err = nil, want a decode error", body)
		}
	}
}

// A body of nested elements is refused once it passes maxXMLDepth, before
// the decoder's element stack grows: a 4 MiB nest of <a> cost about 260 MiB
// of allocation without the limit.
func TestDeeplyNestedBodyIsRefused(t *testing.T) {
	body := "<searchRetrieveResponse>" + strings.Repeat("<a>", (maxResponseBytes-64)/3)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var out sruResponse
	err := decodeXML(strings.NewReader(body), &out)
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "nest") {
		t.Errorf("err = %v, want the nesting refused", err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 32<<20 {
		t.Errorf("decoding allocated %d MiB, want the nest refused before it costs more than 32 MiB", alloc>>20)
	}
	// Real responses nest about eight deep and decode as before.
	if err := decodeXML(strings.NewReader(string(fixture(t, "search_alice.xml"))), &out); err != nil || len(out.Records) == 0 {
		t.Errorf("real response: %d records, err = %v", len(out.Records), err)
	}
}

// encoding/xml skips a leading byte order mark itself (BnF's Explain
// response starts with one), so decodeXML has no code of its own for it.
func TestByteOrderMarkIsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(append([]byte{0xEF, 0xBB, 0xBF}, fixture(t, "isbn_miroir.xml")...))
	}))
	defer srv.Close()
	b, err := testClient(srv.URL).GetBookByISBN(context.Background(), "9782070628971")
	if err != nil || b == nil {
		t.Errorf("= %v, %v; want the record despite the BOM", b, err)
	}
}
