package calibre

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_FilingFormTitleMergesInsteadOfCreatingATwin is the #3122 import
// path: a catalogue row named "A Trace of Death" and a Calibre book whose title
// is filed "Trace of Death, A" are one work, so the import binds the existing
// row instead of creating a second one. Both directions run.
func TestImporter_FilingFormTitleMergesInsteadOfCreatingATwin(t *testing.T) {
	for _, tc := range []struct{ name, stored, incoming string }{
		{"catalogue natural order, Calibre filing form", "A Trace of Death", "Trace of Death, A"},
		{"catalogue filing form, Calibre natural order", "Trace of Death, A", "A Trace of Death"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imp, fr, authorRepo, bookRepo, _, _, _ := newImporterFixture(t)
			ctx := context.Background()
			author := &models.Author{
				ForeignID: "ol:A1", Name: "Alice Author", SortName: "Author, Alice",
				MetadataProvider: "openlibrary", Monitored: true,
			}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatalf("seed author: %v", err)
			}
			if err := bookRepo.Create(ctx, &models.Book{
				ForeignID: "ol:B3122", AuthorID: author.ID, Title: tc.stored, SortTitle: tc.stored,
				Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
				MetadataProvider: "openlibrary",
			}); err != nil {
				t.Fatalf("seed book: %v", err)
			}

			fr.books = []CalibreBook{sampleCalibreBook(42, tc.incoming, "Alice Author")}
			stats, err := imp.Run(ctx, "/lib")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if stats.DuplicatesMerged != 1 {
				t.Errorf("DuplicatesMerged = %d, want 1", stats.DuplicatesMerged)
			}
			books, err := bookRepo.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				t.Fatalf("books = %d, want 1 (no twin)", len(books))
			}
			if books[0].CalibreID == nil || *books[0].CalibreID != 42 {
				t.Errorf("merged book calibre_id = %v, want 42", books[0].CalibreID)
			}
		})
	}
}
