package api

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// seriesLinkFixture stands up the shape #2328 is about: an author whose books
// are all already in the library (the ABS or calibre import case) and whose
// monitoring refuses newly discovered works, so a refresh may update rows and
// may not create them.
type seriesLinkFixture struct {
	db      *sql.DB
	authors *db.AuthorRepo
	books   *db.BookRepo
	series  *db.SeriesRepo
	profile *db.MetadataProfileRepo
	author  *models.Author
}

func newSeriesLinkFixture(t *testing.T, acceptsNewBooks bool) *seriesLinkFixture {
	t.Helper()
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &seriesLinkFixture{
		db:      database,
		authors: db.NewAuthorRepo(database),
		books:   db.NewBookRepo(database),
		series:  db.NewSeriesRepo(database),
		profile: db.NewMetadataProfileRepo(database),
	}
	f.author = &models.Author{
		ForeignID: "OL2236A", Name: "Ann Leckie", SortName: "Leckie, Ann",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if !acceptsNewBooks {
		f.author.MonitorNewItems = models.AuthorMonitorNewItemsNone
	}
	if err := f.authors.Create(context.Background(), f.author); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *seriesLinkFixture) handler(stub *stubMetaProvider) *AuthorHandler {
	return NewAuthorHandler(f.authors, nil, f.books, f.series,
		metadata.NewAggregator(stub), nil, f.profile, nil)
}

// addImportedBook stores a book the way an import leaves it: a real row with
// no series membership of any kind.
func (f *seriesLinkFixture) addImportedBook(t *testing.T, foreignID, title string) *models.Book {
	t.Helper()
	b := &models.Book{
		ForeignID: foreignID, AuthorID: f.author.ID, Title: title, SortTitle: title,
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := f.books.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

// seriesWork is a provider work that names the series it belongs to, which is
// what every real provider returns and what the create path already reads.
func seriesWork(foreignID, title, position string) models.Book {
	b := models.Book{
		ForeignID: foreignID, Title: title, SortTitle: title,
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary",
		SeriesRefs: []models.SeriesRef{{
			ForeignID: "OL-S-IMPERIAL-RADCH", Title: "Imperial Radch", Position: position, Primary: true,
		}},
	}
	return b
}

type storedLink struct {
	seriesTitle string
	position    string
	primary     bool
}

// linksForBook reads series_books directly. The point of these tests is the
// rows, including how many of them there are, so they do not go through a
// repo helper that could hide a duplicate.
func linksForBook(t *testing.T, f *seriesLinkFixture, bookID int64) []storedLink {
	t.Helper()
	rows, err := f.db.QueryContext(context.Background(), `
		SELECT s.title, sb.position_in_series, sb.primary_series
		FROM series_books sb JOIN series s ON s.id = sb.series_id
		WHERE sb.book_id = ? ORDER BY s.title`, bookID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedLink
	for rows.Next() {
		var l storedLink
		var primary int
		if err := rows.Scan(&l.seriesTitle, &l.position, &primary); err != nil {
			t.Fatal(err)
		}
		l.primary = primary != 0
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func countRows(t *testing.T, f *seriesLinkFixture, query string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// warnRecorder captures WARN and above from the default logger for the life of
// a test. The mid loop budget guard is only observable through what it stops
// the loop from attempting, and a failed attempt announces itself as a WARN.
type warnRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (w *warnRecorder) Handle(_ context.Context, r slog.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, r.Message)
	return nil
}

func (w *warnRecorder) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnRecorder) WithGroup(string) slog.Handler      { return w }

func (w *warnRecorder) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.msgs)
}

func (w *warnRecorder) messages() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

func captureWarnings(t *testing.T) *warnRecorder {
	t.Helper()
	rec := &warnRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

func refreshCatalogue(t *testing.T, h *AuthorHandler, author *models.Author) {
	t.Helper()
	if _, err := h.runCatalogueSync(context.Background(), author, catalogueSyncOptions{
		mediaType: models.MediaTypeEbook, discovery: true,
	}); err != nil {
		t.Fatalf("catalogue sync: %v", err)
	}
}

// The headline case (#2328). Every book of an imported library already exists,
// none is in a series, and the author refuses new books. Before the fix the
// refresh updated ratings and covers and linked nothing, because series
// membership was only ever written for books the sync created.
func TestCatalogueSync_LinksSeriesOntoBooksThatAlreadyExist(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	justice := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	sword := f.addImportedBook(t, "OL2236W2", "Ancillary Sword")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
		seriesWork("OL2236W2", "Ancillary Sword", "2"),
	}})

	refreshCatalogue(t, h, f.author)

	for _, tc := range []struct {
		book     *models.Book
		position string
	}{{justice, "1"}, {sword, "2"}} {
		links := linksForBook(t, f, tc.book.ID)
		if len(links) != 1 {
			t.Fatalf("%s: got %d series links, want 1: %+v", tc.book.Title, len(links), links)
		}
		if links[0].seriesTitle != "Imperial Radch" || links[0].position != tc.position {
			t.Fatalf("%s: got %+v, want Imperial Radch at %q", tc.book.Title, links[0], tc.position)
		}
		if !links[0].primary {
			t.Fatalf("%s: the only series a book is in should be its primary one", tc.book.Title)
		}
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM books"); n != 2 {
		t.Fatalf("the refresh created books it was not allowed to create: %d rows, want 2", n)
	}
}

// The other existing-row branch: the work resolves by title, not by id. A
// calibre stub carries "calibre:" ids that no provider work will ever match,
// so this is the branch an imported library lands in most often.
func TestCatalogueSync_LinksSeriesOntoATitleMatchedRow(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	stub := f.addImportedBook(t, "calibre:77", "Ancillary Justice")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
	}})

	refreshCatalogue(t, h, f.author)

	links := linksForBook(t, f, stub.ID)
	if len(links) != 1 || links[0].seriesTitle != "Imperial Radch" || links[0].position != "1" {
		t.Fatalf("title matched row: got %+v, want one Imperial Radch link at position 1", links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM books"); n != 1 {
		t.Fatalf("the title match minted a second row: %d books, want 1", n)
	}
}

// A link that is already there is left exactly as it is, including a position
// that disagrees with the provider. Position is user editable and the renamer
// reads it; a refresh rewriting it would undo hand corrections silently.
func TestCatalogueSync_LeavesAnExistingSeriesLinkAlone(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	s := &models.Series{ForeignID: "OL-S-IMPERIAL-RADCH", Title: "Imperial Radch"}
	if err := f.series.CreateOrGet(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.LinkBookIfMissing(ctx, s.ID, book.ID, "1.5", false); err != nil {
		t.Fatal(err)
	}
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
	}})

	refreshCatalogue(t, h, f.author)

	links := linksForBook(t, f, book.ID)
	if len(links) != 1 {
		t.Fatalf("got %d links, want the one that was already there: %+v", len(links), links)
	}
	if links[0].position != "1.5" {
		t.Fatalf("stored position = %q, want it untouched at 1.5", links[0].position)
	}
	if links[0].primary {
		t.Fatal("the refresh promoted a link the user had demoted")
	}
}

// The two providers mint series ids in different namespaces, so the same
// series arrives under an id the stored link does not carry. Linking it would
// file one book under two series rows. The stored row wins.
func TestCatalogueSync_DoesNotRelinkTheSameSeriesUnderAnotherProviderID(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	stored := &models.Series{ForeignID: "ol-series:imperial-radch", Title: "Imperial Radch"}
	if err := f.series.CreateOrGet(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.LinkBookIfMissing(ctx, stored.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}
	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.SeriesRefs[0].ForeignID = "hc-series:1026"
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	links := linksForBook(t, f, book.ID)
	if len(links) != 1 {
		t.Fatalf("got %d links, want the one already stored: %+v", len(links), links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM series"); n != 1 {
		t.Fatalf("got %d series rows, want the one already stored", n)
	}
}

// Two refreshes in a row must leave one row, not two. series_books is keyed on
// (series_id, book_id), so this also pins the series upsert: a second series
// row with the same foreign id would have produced a second link.
func TestCatalogueSync_RepeatedRefreshDoesNotDuplicateSeriesRows(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
	}})

	refreshCatalogue(t, h, f.author)
	refreshCatalogue(t, h, f.author)
	refreshCatalogue(t, h, f.author)

	if links := linksForBook(t, f, book.ID); len(links) != 1 {
		t.Fatalf("got %d series links after three refreshes, want 1: %+v", len(links), links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM series"); n != 1 {
		t.Fatalf("got %d series rows, want 1", n)
	}
}

// A provider that offers no series data changes nothing. This is also the
// no-cost path: with no ref to act on the linker never takes its one read.
func TestCatalogueSync_NoSeriesDataFromTheProviderChangesNothing(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.SeriesRefs = nil
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	if links := linksForBook(t, f, book.ID); len(links) != 0 {
		t.Fatalf("a refresh with no provider series data invented links: %+v", links)
	}
	if n := countRows(t, f, "SELECT COUNT(*) FROM series"); n != 0 {
		t.Fatalf("got %d series rows, want none", n)
	}
}

// A ref with no foreign id is dropped rather than upserted: CreateOrGet
// refuses an empty foreign id (#1645) because every such caller would
// otherwise collapse onto one shared series row.
func TestCatalogueSync_SkipsASeriesRefWithNoForeignID(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.SeriesRefs[0].ForeignID = ""
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	if links := linksForBook(t, f, book.ID); len(links) != 0 {
		t.Fatalf("an unidentified series ref was linked anyway: %+v", links)
	}
}

// The unattended discovery job calls the same sync path on a schedule. It must
// still create the work it found AND link the series of the books already
// there, and it must stop when its per author budget cancels the context.
func TestDiscoverAuthorBooks_LinksSeriesForExistingBooksAndStillCreates(t *testing.T) {
	f := newSeriesLinkFixture(t, true)
	existing := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	h := f.handler(&stubMetaProvider{works: []models.Book{
		seriesWork("OL2236W1", "Ancillary Justice", "1"),
		seriesWork("OL2236W2", "Ancillary Sword", "2"),
	}})

	created, err := h.DiscoverAuthorBooks(context.Background(), f.author)
	if err != nil {
		t.Fatalf("DiscoverAuthorBooks: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want the one work the library did not have", created)
	}
	if links := linksForBook(t, f, existing.ID); len(links) != 1 || links[0].position != "1" {
		t.Fatalf("discovery did not link the series of the book that already existed: %+v", links)
	}
	fresh, err := f.books.GetByForeignID(context.Background(), "OL2236W2")
	if err != nil || fresh == nil {
		t.Fatalf("discovery did not create the new work: %v", err)
	}
	if links := linksForBook(t, f, fresh.ID); len(links) != 1 {
		t.Fatalf("the created book lost its series link: %+v", links)
	}
}

// The in loop budget guard, pinned by what it prevents rather than by the
// absence of a write.
//
// The first version of this test cancelled before the first call, so all it
// proved was that load() fails on a dead context: replacing the in loop
// ctx.Err() check with `if false` still passed it. Here the linker is primed
// first, so load() and ensureResolved() are both satisfied from memory and the
// loop is genuinely reached. Without the guard the first ref runs CreateOrGet
// against the cancelled context, which fails and logs a WARN; with it the loop
// returns silently. Zero WARN records is therefore the discriminator.
func TestExistingBookSeriesLinker_StopsMidLoopWhenTheBudgetExpires(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	linker := newExistingBookSeriesLinker(f.series, f.author.ID,
		map[int64]struct{}{book.ID: {}})

	// Prime: one good call populates the snapshot and the resolved set.
	linker.link(context.Background(), book, seriesWork("OL2236W1", "Ancillary Justice", "1").SeriesRefs)
	if linker.linked != 1 {
		t.Fatalf("priming call linked = %d, want 1", linker.linked)
	}

	warnings := captureWarnings(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	linker.link(ctx, book, []models.SeriesRef{
		{ForeignID: "hc-series:900", Title: "Imperial Radch Chronology", Position: "1", Primary: true},
		{ForeignID: "hc-series:901", Title: "Some Other Series", Position: "2", Primary: true},
	})

	if n := warnings.count(); n != 0 {
		t.Fatalf("got %d WARN records, want none: the loop kept working on a cancelled context: %v", n, warnings.messages())
	}
	if linker.linked != 1 {
		t.Fatalf("linked = %d, want nothing written after the budget ran out", linker.linked)
	}
	if links := linksForBook(t, f, book.ID); len(links) != 1 {
		t.Fatalf("the cancelled call wrote links anyway: %+v", links)
	}
}

// Review item 1. The id resolved branch matches on a globally UNIQUE foreign
// id, so its row can belong to another author, and reparentMisattachedBook
// deliberately leaves a genuinely co-authored row where it is. That book is
// invisible to an author scoped snapshot, and the first version of this change
// read it as "in no series" and stamped a second primary_series=1 row onto a
// book that already had one.
func TestCatalogueSync_CoAuthoredRowDoesNotGainASecondPrimarySeries(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()

	// The book lives under the co-author, credited on the work, so the synced
	// author may not take it.
	coAuthor := &models.Author{
		ForeignID: "OL9999A", Name: "Co Author", SortName: "Author, Co",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := f.authors.Create(ctx, coAuthor); err != nil {
		t.Fatal(err)
	}
	shared := &models.Book{
		ForeignID: "OL2236W1", AuthorID: coAuthor.ID, Title: "Ancillary Justice", SortTitle: "Ancillary Justice",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := f.books.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	stored := &models.Series{ForeignID: "ol-series:imperial-radch", Title: "Imperial Radch"}
	if err := f.series.CreateOrGet(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.LinkBookIfMissing(ctx, stored.ID, shared.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.CreditedAuthorForeignIDs = []string{coAuthor.ForeignID, f.author.ForeignID}
	work.SeriesRefs = []models.SeriesRef{
		{ForeignID: "hc-series:1026", Title: "Imperial Radch Chronology", Position: "1", Primary: true},
	}
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	after, err := f.books.GetByForeignID(ctx, "OL2236W1")
	if err != nil || after == nil {
		t.Fatalf("book vanished: %v", err)
	}
	if after.AuthorID != coAuthor.ID {
		t.Fatalf("the co-authored row was stolen from its author: author_id = %d, want %d", after.AuthorID, coAuthor.ID)
	}
	links := linksForBook(t, f, shared.ID)
	var primaries []storedLink
	for _, l := range links {
		if l.primary {
			primaries = append(primaries, l)
		}
	}
	if len(primaries) != 1 {
		t.Fatalf("got %d primary series rows, want exactly 1: %+v", len(primaries), links)
	}
	if primaries[0].seriesTitle != "Imperial Radch" {
		t.Fatalf("primary series = %q, want the one that was already stored", primaries[0].seriesTitle)
	}
}

// Review item 1, the other half: a book outside the snapshot must not be
// linked to a series it is already in. Without the per book fallback read the
// linker sees nothing stored and writes a row for a membership that exists.
func TestCatalogueSync_CoAuthoredRowKeepsOneRowForASeriesItIsAlreadyIn(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	coAuthor := &models.Author{
		ForeignID: "OL9999A", Name: "Co Author", SortName: "Author, Co",
		MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := f.authors.Create(ctx, coAuthor); err != nil {
		t.Fatal(err)
	}
	shared := &models.Book{
		ForeignID: "OL2236W1", AuthorID: coAuthor.ID, Title: "Ancillary Justice", SortTitle: "Ancillary Justice",
		Language: "eng", MediaType: models.MediaTypeEbook, Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "openlibrary", Monitored: true,
	}
	if err := f.books.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	stored := &models.Series{ForeignID: "ol-series:imperial-radch", Title: "Imperial Radch"}
	if err := f.series.CreateOrGet(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.LinkBookIfMissing(ctx, stored.ID, shared.ID, "3", true); err != nil {
		t.Fatal(err)
	}

	// The provider names the same series under the OTHER namespace's id.
	work := seriesWork("OL2236W1", "Ancillary Justice", "1")
	work.CreditedAuthorForeignIDs = []string{coAuthor.ForeignID, f.author.ForeignID}
	work.SeriesRefs = []models.SeriesRef{
		{ForeignID: "hc-series:1026", Title: "Imperial Radch", Position: "1", Primary: true},
	}
	h := f.handler(&stubMetaProvider{works: []models.Book{work}})

	refreshCatalogue(t, h, f.author)

	links := linksForBook(t, f, shared.ID)
	if len(links) != 1 {
		t.Fatalf("got %d links, want the one already stored: %+v", len(links), links)
	}
	if links[0].position != "3" {
		t.Fatalf("stored position = %q, want it untouched at 3", links[0].position)
	}
}

// Review item 2, round 2 item 1. The create loop adds each book it makes to
// seenTitles as it goes, so a later work with the same normalised title
// reaches the title branch holding a row created after the snapshot was taken.
//
// Two writers must not compete for that row's primary series, AND the second
// work's series must not be thrown away: the create path iterates the created
// rows, not the works, so it never sees the second work's refs at all. The
// assertion is therefore on the whole set of links, not only on how many are
// primary, which is what let the dropped row through the first time.
func TestCatalogueSync_ABookCreatedThisRunKeepsBothWorksSeries(t *testing.T) {
	f := newSeriesLinkFixture(t, true)
	first := seriesWork("OL2236W1", "Ancillary Justice", "1")
	// Same canonical title, different work id, different series.
	second := seriesWork("OL2236W2", "Ancillary Justice", "4")
	second.SeriesRefs = []models.SeriesRef{
		{ForeignID: "hc-series:777", Title: "Radch Chronology", Position: "4", Primary: true},
	}
	h := f.handler(&stubMetaProvider{works: []models.Book{first, second}})

	refreshCatalogue(t, h, f.author)

	book, err := f.books.GetByForeignID(context.Background(), "OL2236W1")
	if err != nil || book == nil {
		t.Fatalf("the first work was not created: %v", err)
	}
	links := linksForBook(t, f, book.ID)
	got := make(map[string]string, len(links))
	var primaries int
	for _, l := range links {
		got[l.seriesTitle] = l.position
		if l.primary {
			primaries++
		}
	}
	want := map[string]string{"Imperial Radch": "1", "Radch Chronology": "4"}
	if len(got) != len(want) {
		t.Fatalf("got %d series links, want both works' series %v: %+v", len(got), want, links)
	}
	for title, position := range want {
		if pos, ok := got[title]; !ok {
			t.Fatalf("series %q was dropped: %+v", title, links)
		} else if pos != position {
			t.Fatalf("series %q at position %q, want %q", title, pos, position)
		}
	}
	if primaries != 1 {
		t.Fatalf("got %d primary series rows on a book created this run, want exactly 1: %+v", primaries, links)
	}
}

// Review item 3. A series fill or a manual link can write a primary series
// between the snapshot and the insert, because the per author lock does not
// cover them. Resolving the flag at write time rather than from the snapshot
// is what makes that safe, so this stores a primary membership AFTER the
// linker has loaded and checks the next link is not primary too.
func TestExistingBookSeriesLinker_ResolvesPrimaryAtWriteTime(t *testing.T) {
	f := newSeriesLinkFixture(t, false)
	ctx := context.Background()
	book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
	linker := newExistingBookSeriesLinker(f.series, f.author.ID,
		map[int64]struct{}{book.ID: {}})

	// Load the snapshot while the book is in no series at all.
	if !linker.load(ctx) {
		t.Fatal("snapshot load failed")
	}
	// Something else files the book, the way series fill does.
	meanwhile := &models.Series{ForeignID: "ol-series:imperial-radch", Title: "Imperial Radch"}
	if err := f.series.CreateOrGet(ctx, meanwhile); err != nil {
		t.Fatal(err)
	}
	if _, err := f.series.LinkBookIfMissing(ctx, meanwhile.ID, book.ID, "1", true); err != nil {
		t.Fatal(err)
	}

	linker.link(ctx, book, []models.SeriesRef{
		{ForeignID: "hc-series:1026", Title: "Radch Chronology", Position: "1", Primary: true},
	})

	links := linksForBook(t, f, book.ID)
	var primaries int
	for _, l := range links {
		if l.primary {
			primaries++
		}
	}
	if len(links) != 2 {
		t.Fatalf("got %d links, want both series: %+v", len(links), links)
	}
	if primaries != 1 {
		t.Fatalf("got %d primary series rows, want exactly 1: %+v", primaries, links)
	}
}

// Review item 4. The cross provider title skip normalises through the shared
// book title normaliser, so a doubled space, a case difference, an apostrophe
// and a parenthetical suffix all count as the same series, and so does an
// inverted article ("Expanse, The"), which was a known gap until the shared
// normaliser learned the filing form (#3122).
func TestExistingBookSeriesLinker_TitleSkipNormalisation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stored, offer string
		wantSkipped   bool
	}{
		{"identical", "The Expanse", "The Expanse", true},
		{"case", "The Expanse", "the expanse", true},
		{"doubled space", "The Expanse", "The  Expanse", true},
		{"apostrophe", "Dragon's Egg", "Dragons Egg", true},
		{"parenthetical suffix", "The Expanse", "The Expanse (Publication Order)", true},
		{"inverted article", "The Expanse", "Expanse, The", true},
		{"genuinely different", "The Expanse", "Imperial Radch", false},
		{"sub-series is not the parent", "Discworld", "Discworld: Witches", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSeriesLinkFixture(t, false)
			ctx := context.Background()
			book := f.addImportedBook(t, "OL2236W1", "Ancillary Justice")
			stored := &models.Series{ForeignID: "ol-series:stored", Title: tc.stored}
			if err := f.series.CreateOrGet(ctx, stored); err != nil {
				t.Fatal(err)
			}
			if _, err := f.series.LinkBookIfMissing(ctx, stored.ID, book.ID, "1", true); err != nil {
				t.Fatal(err)
			}
			linker := newExistingBookSeriesLinker(f.series, f.author.ID,
				map[int64]struct{}{book.ID: {}})
			linker.link(ctx, book, []models.SeriesRef{
				{ForeignID: "hc-series:1026", Title: tc.offer, Position: "1", Primary: true},
			})

			links := linksForBook(t, f, book.ID)
			want := 2
			if tc.wantSkipped {
				want = 1
			}
			if len(links) != want {
				t.Fatalf("stored %q, provider %q: got %d links, want %d: %+v",
					tc.stored, tc.offer, len(links), want, links)
			}
		})
	}
}
