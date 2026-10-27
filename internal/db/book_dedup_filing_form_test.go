package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

type bookSnapshot struct {
	title string
	key   sql.NullString
	owner sql.NullInt64
	autho int64
}

func snapshotBooks(t *testing.T, database *sql.DB) map[int64]bookSnapshot {
	t.Helper()
	rows, err := database.Query("SELECT id, title, dedup_key, owner_user_id, author_id FROM books")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]bookSnapshot{}
	for rows.Next() {
		var id int64
		var s bookSnapshot
		if err := rows.Scan(&id, &s.title, &s.key, &s.owner, &s.autho); err != nil {
			t.Fatal(err)
		}
		out[id] = s
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFilingFormKeyUpgrade is the #3122 upgrade path. Keys are derived from
// books.title by Go code SQLite cannot run, and nothing in the schema makes
// dedup_key unique (idx_books_author_dedup_key is a plain index), so the repair
// is the revision-gated startup backfill rather than a SQL migration: the boot
// after CanonicalDedupKeyRev moves rewrites every stale key.
//
// The database starts as the previous release left it: marker at revision 2,
// keys computed the old way. The matrix covers rows with no collision, a pair
// that now shares a key, a three way group, two authors with the same pair, and
// empty and NULL keys. No row is deleted, merged or retitled, owners are
// untouched, a second open changes nothing, and rows that now share a key are
// still separate rows that the ABS importer's ambiguity check can see.
func TestFilingFormKeyUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bindery.db")

	database := openFileDB(t, path)
	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	authorA := mkAuthor(t, authors, ctx, "OL-3122-A")
	authorB := mkAuthor(t, authors, ctx, "OL-3122-B")

	type seed struct {
		author   *models.Author
		fid      string
		title    string
		storedAs any // what the previous release left in dedup_key
	}
	seeds := []seed{
		// No collision: a filing form alone, and a plain title.
		{authorA, "f-hobbit", "Hobbit, The", "hobbit the"},
		{authorA, "f-plain", "Mistborn", "mistborn"},
		// One collision pair.
		{authorA, "f-trace-1", "Trace of Death, A", "trace of death a"},
		{authorA, "f-trace-2", "A Trace of Death", "a trace of death"},
		// Three way collision, one carrying a bracketed qualifier.
		{authorA, "f-ember-1", "Ember in the Ashes, An", "ember in the ashes an"},
		{authorA, "f-ember-2", "An Ember in the Ashes", "an ember in the ashes"},
		{authorA, "f-ember-3", "Ember in the Ashes, An [Unabridged]", "ember in the ashes an"},
		// The same pair under a second author: keys are per author, so these
		// must not interact with authorA's.
		{authorB, "g-trace-1", "Trace of Death, A", "trace of death a"},
		{authorB, "g-trace-2", "A Trace of Death", "a trace of death"},
		// Empty and NULL keys, one of them a filing form.
		{authorB, "g-empty", "Boot, Das", ""},
		{authorB, "g-null", "Aleph, El", nil},
		// A comma that is not a filing form.
		{authorB, "g-comma", "Love, Actually", "love actually"},
	}
	ids := make([]int64, len(seeds))
	for i, s := range seeds {
		b := mkBook(t, books, ctx, s.author.ID, s.fid, s.title, models.BookStatusWanted)
		ids[i] = b.ID
		if _, err := database.Exec("UPDATE books SET dedup_key = ? WHERE id = ?", s.storedAs, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	// The previous release stamped revision 2.
	if _, err := database.Exec("UPDATE settings SET value = '2' WHERE key = ?", backfillRevKeyDedup); err != nil {
		t.Fatal(err)
	}
	before := snapshotBooks(t, database)
	database.Close()

	database = openFileDB(t, path)
	defer database.Close()
	after := snapshotBooks(t, database)

	if len(after) != len(before) {
		t.Fatalf("row count %d -> %d: the upgrade must never delete or add rows", len(before), len(after))
	}
	for id, b := range before {
		a, ok := after[id]
		if !ok {
			t.Fatalf("book %d vanished", id)
		}
		if a.title != b.title || a.owner != b.owner || a.autho != b.autho {
			t.Errorf("book %d: title/owner/author changed: %+v -> %+v", id, b, a)
		}
		if want := indexer.CanonicalDedupKey(b.title); !a.key.Valid || a.key.String != want {
			t.Errorf("book %d %q: dedup_key = %q, want %q", id, b.title, a.key.String, want)
		}
	}
	if got, ok := settingValue(t, database, backfillRevKeyDedup); !ok || got != strconv.Itoa(indexer.CanonicalDedupKeyRev) {
		t.Errorf("marker after upgrade = %q (present %v), want %d", got, ok, indexer.CanonicalDedupKeyRev)
	}

	// Concrete values, so the rule is pinned independently of the function that
	// produced it.
	wantKey := map[string]string{
		"f-hobbit": "the hobbit", "f-plain": "mistborn",
		"f-trace-1": "a trace of death", "f-trace-2": "a trace of death",
		"f-ember-1": "an ember in the ashes", "f-ember-2": "an ember in the ashes", "f-ember-3": "an ember in the ashes",
		"g-trace-1": "a trace of death", "g-trace-2": "a trace of death",
		"g-empty": "das boot", "g-null": "el aleph", "g-comma": "love actually",
	}
	for i, s := range seeds {
		if got := after[ids[i]].key.String; got != wantKey[s.fid] {
			t.Errorf("%s (%q): dedup_key = %q, want %q", s.fid, s.title, got, wantKey[s.fid])
		}
	}

	// Colliding rows stay separate rows, and the importer's lookup reports them
	// as ambiguous (more than one match) instead of binding to either. That is
	// how they reach review: Review duplicates reads titles, never this column.
	repo := NewBookRepo(database)
	pair, err := repo.FindAllByAuthorAndDedupKey(ctx, authorA.ID, "A Trace of Death")
	if err != nil {
		t.Fatal(err)
	}
	if len(pair) != 2 {
		t.Errorf("author A: lookup of the collided pair returned %d rows, want 2 (ambiguous)", len(pair))
	}
	three, err := repo.FindAllByAuthorAndDedupKey(ctx, authorA.ID, "Ember in the Ashes, An")
	if err != nil {
		t.Fatal(err)
	}
	if len(three) != 3 {
		t.Errorf("author A: lookup of the three way group returned %d rows, want 3", len(three))
	}
	other, err := repo.FindAllByAuthorAndDedupKey(ctx, authorB.ID, "Trace of Death, A")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 2 {
		t.Errorf("author B: lookup returned %d rows, want 2 and no rows from author A", len(other))
	}
	hobbit, err := repo.FindByAuthorAndDedupKey(ctx, authorA.ID, "The Hobbit")
	if err != nil || hobbit == nil || hobbit.ID != ids[0] {
		t.Errorf("natural order lookup of a stored filing form = %+v, %v; want book %d", hobbit, err, ids[0])
	}

	// Second open: marker current, nothing runs, nothing changes. Then force the
	// backfill itself again, which must be a no-op on converged rows.
	database.Close()
	database = openFileDB(t, path)
	if again := snapshotBooks(t, database); !sameSnapshots(after, again) {
		t.Errorf("second open changed rows: %+v -> %+v", after, again)
	}
	if err := backfillBookDedupKeys(database); err != nil {
		t.Fatal(err)
	}
	if again := snapshotBooks(t, database); !sameSnapshots(after, again) {
		t.Errorf("re-running the backfill changed rows: %+v -> %+v", after, again)
	}
}

func sameSnapshots(a, b map[int64]bookSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for id, x := range a {
		if y, ok := b[id]; !ok || x != y {
			return false
		}
	}
	return true
}
