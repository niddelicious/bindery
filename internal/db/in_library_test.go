package db

import (
	"context"
	"sort"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestInLibraryPredicate runs the shared predicate through a plain query on an
// aliased books table, the way a count query elsewhere would embed it, so the
// rule is pinned independently of ListPageFiltered.
func TestInLibraryPredicate(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	author := &models.Author{ForeignID: "OL-P", Name: "P", SortName: "P", Monitored: true}
	if err := NewAuthorRepo(database).Create(ctx, author); err != nil {
		t.Fatal(err)
	}
	bookRepo := NewBookRepo(database)
	seed := func(title, status string, files ...string) {
		t.Helper()
		b := &models.Book{ForeignID: "OL-P-" + title, AuthorID: author.ID, Title: title, SortTitle: title,
			Status: status, MediaType: models.MediaTypeBoth, Monitored: true}
		if err := bookRepo.Create(ctx, b); err != nil {
			t.Fatal(err)
		}
		for i, f := range files {
			if _, err := database.ExecContext(ctx, `INSERT INTO book_files (book_id, format, path) VALUES (?, ?, ?)`,
				b.ID, f, "/p/"+title+"/"+string(rune('a'+i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("done", models.BookStatusImported, "ebook", "audiobook")
	seed("ebookOnly", models.BookStatusWanted, "ebook", "ebook")
	seed("audioOnly", models.BookStatusWanted, "audiobook")
	seed("none", models.BookStatusWanted)
	seed("skipped", models.BookStatusSkipped, "ebook")

	run := func(format string) []string {
		t.Helper()
		rows, err := database.QueryContext(ctx, `SELECT b.title FROM books b WHERE `+InLibraryPredicate("b", format))
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(out)
		return out
	}
	cases := []struct {
		format string
		want   []string
	}{
		{"", []string{"audioOnly", "done", "ebookOnly"}},
		{"ebook", []string{"done", "ebookOnly"}},
		{"audiobook", []string{"audioOnly", "done"}},
		// Anything that is not a known format is ignored, never interpolated.
		{"x' OR 1=1 --", []string{"audioOnly", "done", "ebookOnly"}},
	}
	for _, c := range cases {
		got := run(c.format)
		if len(got) != len(c.want) {
			t.Fatalf("format %q = %v, want %v", c.format, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("format %q = %v, want %v", c.format, got, c.want)
			}
		}
	}
}
