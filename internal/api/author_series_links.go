package api

import (
	"context"
	"log/slog"
	"strings"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// existingBookSeriesLinkSampleLimit caps the titles carried in the run's
// summary log line, the same way the sync's skip samples are capped.
const existingBookSeriesLinkSampleLimit = 5

// existingBookSeriesLinker writes provider series refs onto books the library
// ALREADY has, during a catalogue sync.
//
// Until #2328 series membership was written in exactly one place,
// handleNewWantedBook, which runs only over the books a sync creates. Both
// branches that recognise an existing row return to the top of the loop before
// reaching it, so on an imported library, where every work resolves to a row
// that is already there and the author's monitoring refuses new ones, a
// refresh updated ratings, covers and genres and could never link a series.
// That left no way at all to repair series membership, which is most of what
// "relink this author to a better record and refresh" is for.
//
// # What the snapshot can and cannot answer
//
// The linker holds one author-scoped snapshot of current memberships so the
// common case costs one query for the whole sync. That snapshot describes the
// books that were under this author when it was taken, and NOTHING else, which
// is the trap the first version of this fell into: both call sites can hand it
// a book the snapshot has never heard of, and an absent book reads as "this
// book is in no series at all". Two such books exist:
//
//   - the id-resolved branch matches globally, so its row can still belong to
//     another author. reparentMisattachedBook deliberately leaves a genuinely
//     co-authored row where it is, so this is a permanent state, not a race.
//   - the title branch sees books created by this very run, because the create
//     loop adds them to seenTitles as it goes.
//
// So: refs aimed at a book this run created are held and written by
// linkDeferred once the create path has finished with it, because those two
// writers would otherwise fight over which series is primary. Any other book
// the snapshot does not cover gets one membership query of its own, cached for
// the rest of the sync.
//
// The primary flag is never decided from the snapshot at all. It is decided at
// write time by LinkBookPreservingPrimary, which re-reads HasPrimarySeries, so
// a stale or absent snapshot entry cannot produce the second primary_series=1
// row that #2525 exists to prevent. The residual window is the two statements
// inside that call: a concurrent series fill can still slip a primary in
// between them. Closing it needs a partial unique index on the table, which is
// a migration and not this change. No duplicate membership is possible at any
// time, concurrently or not, because series_books is keyed on
// (series_id, book_id) and every insert here is INSERT OR IGNORE.
//
// # Cost
//
// It runs for every book of every author on every refresh, including the
// unattended discovery pass, so:
//   - no provider call, ever. The refs come from the works fetch the sync has
//     already made, off the same models.Book the create path reads them from.
//   - one SELECT per sync, taken lazily on the first existing book that
//     carries a series ref, plus one per book the snapshot cannot cover (in
//     practice zero: it takes a co-authored work to produce one).
//   - per link actually written: the HasPrimarySeries read inside
//     LinkBookPreservingPrimary, the insert, and the series upsert when the
//     series is new to this sync. A library already in the right shape writes
//     nothing and reads only the snapshot.
//
// It is not safe for concurrent use; a catalogue sync holds the author's lock
// and walks its works serially.
type existingBookSeriesLinker struct {
	series   *db.SeriesRepo
	authorID int64

	// covered is the ids of the books that were under this author before the
	// run started. For those, and only those, an absent snapshot entry really
	// does mean "in no series".
	covered map[int64]struct{}
	// created is the books this run made. They belong to the create path.
	created map[int64]struct{}
	// resolved is the books outside covered whose memberships have since been
	// fetched one at a time.
	resolved map[int64]struct{}
	// deferred holds refs aimed at a book this run created, until the create
	// path has written that book's own series.
	deferred []deferredLink

	loaded bool
	failed bool

	// linkedForeignIDs is book id → series foreign id → stored position.
	linkedForeignIDs map[int64]map[string]string
	// linkedTitles is book id → set of canonical series title keys the book is
	// already in, whatever id those rows carry. It is what stops the
	// cross-provider duplicate: the two providers mint series ids in different
	// namespaces, so "The Expanse" from Hardcover and "The Expanse" from
	// OpenLibrary are two rows in `series`, and linking by id alone would file
	// one book under both.
	linkedTitles map[int64]map[string]struct{}
	seriesIDs    map[string]int64

	linked         int
	conflicts      int
	conflictSample []string
}

// newExistingBookSeriesLinker takes the ids of the author's books as they were
// read at the top of the sync, before anything was created or re-parented.
func newExistingBookSeriesLinker(series *db.SeriesRepo, authorID int64, covered map[int64]struct{}) *existingBookSeriesLinker {
	return &existingBookSeriesLinker{
		series:   series,
		authorID: authorID,
		covered:  covered,
		created:  make(map[int64]struct{}),
		resolved: make(map[int64]struct{}),
	}
}

// markCreated records a book this run created. Its series come from the create
// path, which runs later, so linking it now would race that writer over which
// series is primary: handleNewWantedBook passes the provider's own flag to
// LinkBook unconditionally, and both rows would land primary.
func (l *existingBookSeriesLinker) markCreated(bookID int64) {
	if l == nil || bookID == 0 {
		return
	}
	l.created[bookID] = struct{}{}
}

// deferredLink is a set of refs aimed at a book this run created, held until
// the create path has finished with it.
type deferredLink struct {
	book models.Book
	refs []models.SeriesRef
}

// linkDeferred writes the refs held back from books this run created. It runs
// after the created-books pass, so handleNewWantedBook has already written
// each book's own series and the membership read below sees it.
//
// This exists because "the create path links the same refs a moment later" is
// only true of the work that created the row. When two provider works share a
// normalised title, the second one reaches the title branch holding the first
// one's row, and the create path never sees the second work's refs at all: it
// iterates createdBooks, which holds one entry per created row. Dropping those
// refs silently lost a real series the provider had reported.
//
// Ordering is the point. Running here rather than in the loop means the book's
// own series is already stored, so LinkBookPreservingPrimary files these as
// additional memberships instead of competing for the primary slot.
func (l *existingBookSeriesLinker) linkDeferred(ctx context.Context) {
	if l == nil || len(l.deferred) == 0 {
		return
	}
	pending := l.deferred
	l.deferred = nil
	for i := range pending {
		if ctx.Err() != nil {
			return
		}
		// Release the claim: the create path is done with this row.
		delete(l.created, pending[i].book.ID)
		l.link(ctx, &pending[i].book, pending[i].refs)
	}
}

// seriesTitleKey normalises a series title for the "already in this series
// under the other provider's id" comparison.
//
// indexer.CanonicalDedupKey, the normaliser TitleIndex is built on, but not
// TitleIndex.Lookup itself. The index's second tier treats a title and the
// same title plus a subtitle as one work, which is right for books and wrong
// for series: "Discworld" and "Discworld: Witches" are two real series, and
// matching them would mean the second one could never be linked. Verified
// against the normaliser: it folds case, collapses a doubled internal space,
// ignores an apostrophe difference and drops a parenthetical suffix, so
// "The Expanse", "The  Expanse", "the expanse" and
// "The Expanse (Publication Order)" all agree. So does an inverted article
// ("Expanse, The" against "The Expanse"), since the shared normaliser folds the
// library filing form (#3122); that used to be a known gap here.
func seriesTitleKey(title string) string {
	return indexer.CanonicalDedupKey(title)
}

// link records the provider's series refs for a book that is already in the
// library. Best effort throughout: a series that cannot be upserted or a link
// that cannot be written is logged and skipped, never fatal to the sync, which
// is how the create path treats the same failures.
func (l *existingBookSeriesLinker) link(ctx context.Context, book *models.Book, refs []models.SeriesRef) {
	if l == nil || l.series == nil || book == nil || book.ID == 0 || len(refs) == 0 {
		return
	}
	if _, isNew := l.created[book.ID]; isNew {
		// Held, not dropped: see linkDeferred. The create path owns this row
		// until it has written the series of the work that created it.
		l.deferred = append(l.deferred, deferredLink{book: *book, refs: refs})
		return
	}
	if !l.load(ctx) {
		return
	}
	if !l.ensureResolved(ctx, book.ID) {
		return
	}
	for _, ref := range refs {
		// The discovery job bounds each author with a context deadline, so a
		// long author stops here rather than running the rest of its works
		// against a cancelled context and logging a warning per ref.
		if ctx.Err() != nil {
			return
		}
		foreignID := strings.TrimSpace(ref.ForeignID)
		title := strings.TrimSpace(ref.Title)
		// CreateOrGet refuses an empty foreign id outright (#1645): without
		// one every caller would collapse onto a single shared row.
		if foreignID == "" || title == "" {
			continue
		}
		if have, ok := l.linkedForeignIDs[book.ID][foreignID]; ok {
			// The book is in this series already. Leave the stored row
			// exactly as it is, including a position that disagrees with the
			// provider: position is user editable, a renamer reads it, and a
			// refresh silently rewriting it would undo hand corrections with
			// no record. Counted and reported instead.
			//
			// Skipping the write is a cost decision, not a correctness one:
			// the insert below ignores an exact duplicate, so letting it run
			// would change nothing but the query count.
			if pos := strings.TrimSpace(ref.Position); pos != "" && pos != have {
				l.recordConflict(book, title, have, pos)
			}
			continue
		}
		// Same series under the other provider's id. Adding it would file one
		// volume under two series rows, with the renamer and the series page
		// each free to pick one. The stored row wins: it is the one the rest
		// of the library already points at.
		//
		// Accepted consequence: a genuinely different series that happens to
		// share a title with one the book is in is skipped, every time, with
		// only this DEBUG line to say so. It is the rarer error of the two,
		// and the alternative is a duplicate series in everyone's library.
		if _, sameTitle := l.linkedTitles[book.ID][seriesTitleKey(title)]; sameTitle {
			slog.Debug("a series with this title is already linked under another provider id; leaving it",
				"book", book.Title, "bookId", book.ID, "series", title, "foreignId", foreignID)
			continue
		}
		seriesID, ok := l.resolveSeries(ctx, foreignID, title)
		if !ok {
			continue
		}
		// #2525: a book already filed under a primary series keeps it. The
		// flag is resolved by the repo at write time and never from the
		// snapshot, because a book the snapshot does not describe would
		// otherwise read as having no primary and be stamped with a second
		// one. The create path can still pass its own flag through, since a
		// book it just made has no other membership.
		created, err := l.series.LinkBookPreservingPrimary(ctx, seriesID, book.ID, ref.Position)
		if err != nil {
			slog.Warn("failed to link an existing book to its series", "book", book.Title, "series", title, "error", err)
			continue
		}
		l.remember(book.ID, foreignID, title, ref.Position)
		if !created {
			// The row was there but not in what we read, which a concurrent
			// series fill or manual link can produce. INSERT OR IGNORE means
			// nothing was duplicated; nothing to count.
			continue
		}
		l.linked++
		slog.Debug("linked an existing book to a series found on refresh",
			"book", book.Title, "bookId", book.ID, "series", title, "position", ref.Position)
		// Mirrors the create path (#2245): a ref that names the series by a
		// Hardcover id is worth recording as a Hardcover link, or the series
		// shows "(no Hardcover link)" and Fill does nothing. Only on a link
		// this run actually created, so a settled library writes nothing.
		if linked, err := l.series.EnsureHardcoverLinkFromForeignID(ctx, seriesID, foreignID, title); err != nil {
			slog.Warn("failed to record hardcover series link", "series", title, "error", err)
		} else if linked {
			slog.Debug("linked series to hardcover from provider series ref", "series", title, "foreignId", foreignID)
		}
	}
}

// load takes the author's current memberships once per sync, on first use.
// Returns false when there is nothing to work from: a failed read must not be
// treated as "no memberships", or the linker would try to write a link for
// every book and rely on INSERT OR IGNORE to sort it out.
func (l *existingBookSeriesLinker) load(ctx context.Context) bool {
	if l.failed {
		return false
	}
	if l.loaded {
		return true
	}
	memberships, err := l.series.ListBookSeriesMembershipsByAuthor(ctx, l.authorID)
	if err != nil {
		slog.Warn("could not read existing series memberships; skipping series links for this refresh",
			"authorId", l.authorID, "error", err)
		l.failed = true
		return false
	}
	l.linkedForeignIDs = make(map[int64]map[string]string, len(memberships))
	l.linkedTitles = make(map[int64]map[string]struct{}, len(memberships))
	l.seriesIDs = make(map[string]int64)
	for bookID, rows := range memberships {
		l.absorb(bookID, rows)
	}
	l.loaded = true
	return true
}

// ensureResolved guarantees the maps describe this book. A book the snapshot
// covers needs nothing; any other book is read on its own, once. Returns false
// when the read failed, so a book whose memberships are unknown is left alone
// rather than linked blind.
func (l *existingBookSeriesLinker) ensureResolved(ctx context.Context, bookID int64) bool {
	if _, ok := l.covered[bookID]; ok {
		return true
	}
	if _, ok := l.resolved[bookID]; ok {
		return true
	}
	rows, err := l.series.ListBookSeriesMembershipsForBook(ctx, bookID)
	if err != nil {
		slog.Warn("could not read the series a book is already in; leaving its series links alone",
			"bookId", bookID, "error", err)
		return false
	}
	l.absorb(bookID, rows)
	l.resolved[bookID] = struct{}{}
	return true
}

func (l *existingBookSeriesLinker) absorb(bookID int64, rows []db.BookSeriesMembership) {
	for _, m := range rows {
		if key := seriesTitleKey(m.SeriesTitle); key != "" {
			if l.linkedTitles[bookID] == nil {
				l.linkedTitles[bookID] = make(map[string]struct{}, len(rows))
			}
			l.linkedTitles[bookID][key] = struct{}{}
		}
		if m.SeriesForeignID == "" {
			continue
		}
		if l.linkedForeignIDs[bookID] == nil {
			l.linkedForeignIDs[bookID] = make(map[string]string, len(rows))
		}
		l.linkedForeignIDs[bookID][m.SeriesForeignID] = strings.TrimSpace(m.Position)
		l.seriesIDs[m.SeriesForeignID] = m.SeriesID
	}
}

// resolveSeries maps a provider series foreign id to a local series row,
// creating it if this library has never seen it. Resolved ids are cached for
// the rest of the sync: an author's works commonly share a handful of series,
// and without the cache each one would re-run the upsert.
func (l *existingBookSeriesLinker) resolveSeries(ctx context.Context, foreignID, title string) (int64, bool) {
	if id, ok := l.seriesIDs[foreignID]; ok {
		return id, true
	}
	s := &models.Series{ForeignID: foreignID, Title: title}
	if err := l.series.CreateOrGet(ctx, s); err != nil {
		slog.Warn("failed to upsert series", "series", title, "error", err)
		return 0, false
	}
	l.seriesIDs[foreignID] = s.ID
	return s.ID, true
}

func (l *existingBookSeriesLinker) remember(bookID int64, foreignID, title, position string) {
	if l.linkedForeignIDs[bookID] == nil {
		l.linkedForeignIDs[bookID] = make(map[string]string, 1)
	}
	l.linkedForeignIDs[bookID][foreignID] = strings.TrimSpace(position)
	if key := seriesTitleKey(title); key != "" {
		if l.linkedTitles[bookID] == nil {
			l.linkedTitles[bookID] = make(map[string]struct{}, 1)
		}
		l.linkedTitles[bookID][key] = struct{}{}
	}
}

func (l *existingBookSeriesLinker) recordConflict(book *models.Book, series, stored, offered string) {
	l.conflicts++
	if len(l.conflictSample) < existingBookSeriesLinkSampleLimit {
		l.conflictSample = append(l.conflictSample, book.Title+" in "+series)
	}
	slog.Debug("keeping the stored position for a book already in this series",
		"book", book.Title, "bookId", book.ID, "series", series, "stored", stored, "provider", offered)
}

// logSummary reports the run's series work, and says nothing when there was
// none. A refresh over a settled library is the common case and it should not
// add a line to the log.
func (l *existingBookSeriesLinker) logSummary(authorName string) {
	if l == nil || (l.linked == 0 && l.conflicts == 0) {
		return
	}
	slog.Info("linked series onto books the library already had",
		"author", authorName, "linked", l.linked,
		"positionDisagreements", l.conflicts, "sample", l.conflictSample)
}
