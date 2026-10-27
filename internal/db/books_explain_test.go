package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// explainPlan returns the EXPLAIN QUERY PLAN detail lines for query.
func explainPlan(t *testing.T, database *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := database.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// A single-book lookup must touch only that book's file rows. The first_ebook
// and first_audiobook CTEs it used to carry were built over the whole
// book_files table (a MATERIALIZE plus a format-only search), which cost about
// 50 ms per GetByID on a 30,000 file library.
func TestBookQueries_PlanBindsFileLookupToBook(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	queries := map[string]string{
		"GetByID": bookCTE + " SELECT " + bookColumns + " FROM books " + bookJoins + " WHERE books.id = ?",
		"List":    bookCTE + " SELECT " + bookColumns + " FROM books " + bookJoins + " ORDER BY books.sort_key, sort_title LIMIT 50",
	}
	for name, q := range queries {
		var args []any
		if strings.Contains(q, "books.id = ?") {
			args = append(args, 1)
		}
		plan := strings.Join(explainPlan(t, database, q, args...), "\n")
		if strings.Contains(plan, "MATERIALIZE") {
			t.Errorf("%s materialises a table for the file paths:\n%s", name, plan)
		}
		if strings.Contains(plan, "SCAN book_files") || strings.Contains(plan, "SCAN bf") {
			t.Errorf("%s scans book_files:\n%s", name, plan)
		}
		if !strings.Contains(plan, "idx_book_files_format_book (format=? AND book_id=?)") {
			t.Errorf("%s does not look files up by (format, book_id):\n%s", name, plan)
		}
	}
}

// The per-book lookup must keep the CTE's answers: the lowest id file of each
// format, ignoring the other format, and the stored column still wins.
func TestBookQueries_FirstFilePerFormat(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()

	authors := NewAuthorRepo(database)
	books := NewBookRepo(database)
	author := seedAuthor(t, authors, "OL1A", "Author")

	both := seedBook(t, books, author.ID, "OL1W", "Both formats")
	none := seedBook(t, books, author.ID, "OL2W", "No files")
	stored := seedBook(t, books, author.ID, "OL3W", "Stored column")
	// z-first.epub is inserted first and so has the lowest id, although it
	// sorts after a-second.epub by path: "first" has to mean lowest id.
	for _, f := range []struct {
		book         int64
		format, path string
	}{
		{both.ID, "ebook", "/lib/b/z-first.epub"},
		{both.ID, "ebook", "/lib/b/a-second.epub"},
		{both.ID, "audiobook", "/lib/b/book.m4b"},
		{stored.ID, "ebook", "/lib/c/fallback.epub"},
	} {
		mustExec(t, database, `INSERT INTO book_files (book_id, format, path) VALUES (?, ?, ?)`, f.book, f.format, f.path)
	}
	mustExec(t, database, `UPDATE books SET ebook_file_path = '/lib/c/stored.epub' WHERE id = ?`, stored.ID)
	mustExec(t, database, `UPDATE books SET ebook_file_path = '', audiobook_file_path = '' WHERE id IN (?, ?)`, both.ID, none.ID)

	got, err := books.GetByID(ctx, both.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID: %v %v", got, err)
	}
	if got.EbookFilePath != "/lib/b/z-first.epub" || got.AudiobookFilePath != "/lib/b/book.m4b" {
		t.Errorf("both: ebook=%q audiobook=%q", got.EbookFilePath, got.AudiobookFilePath)
	}
	got, err = books.GetByID(ctx, none.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID none: %v %v", got, err)
	}
	if got.EbookFilePath != "" || got.AudiobookFilePath != "" {
		t.Errorf("none: ebook=%q audiobook=%q", got.EbookFilePath, got.AudiobookFilePath)
	}
	got, err = books.GetByID(ctx, stored.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID stored: %v %v", got, err)
	}
	if got.EbookFilePath != "/lib/c/stored.epub" {
		t.Errorf("stored column must win over the file rows, got %q", got.EbookFilePath)
	}
}
