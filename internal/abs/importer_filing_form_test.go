package abs

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestImporter_FilingFormTitleLinksInsteadOfCreatingATwin is the #3122 import
// path: a library row filed as "Trace of Death, A" (Calibre's sort title
// convention) and an Audiobookshelf item named "A Trace of Death" are one work,
// so the import links the existing row instead of creating a second one. Both
// directions run, because the key has to be symmetric.
func TestImporter_FilingFormTitleLinksInsteadOfCreatingATwin(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, stored, incoming string }{
		{"stored filing form, ABS natural order", "Trace of Death, A", "A Trace of Death"},
		{"stored natural order, ABS filing form", "A Trace of Death", "Trace of Death, A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			importer, authorRepo, bookRepo, _, _, _, _, _, _, _ := newABSImporterFixture(t)
			ctx := context.Background()
			author := &models.Author{
				ForeignID: "ol:author:filing", Name: "Filing Author", SortName: "Author, Filing",
				MetadataProvider: "openlibrary", Monitored: true,
			}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			if err := bookRepo.Create(ctx, &models.Book{
				ForeignID: "ol:book:filing", AuthorID: author.ID, Title: tc.stored, SortTitle: tc.stored,
				Status: models.BookStatusWanted, Monitored: true, AnyEditionOK: true,
				MediaType: models.MediaTypeAudiobook, MetadataProvider: "openlibrary",
			}); err != nil {
				t.Fatal(err)
			}

			item := sampleABSItem()
			item.ItemID = "li-filing"
			item.Title = tc.incoming
			item.Authors = []NormalizedAuthor{{ID: "author-filing", Name: "Filing Author"}}
			importer.enumerateFn = func(ctx context.Context, libraryID string, fn func(context.Context, NormalizedLibraryItem) error) (EnumerationStats, error) {
				if err := fn(ctx, item); err != nil {
					return EnumerationStats{}, err
				}
				return EnumerationStats{PagesScanned: 1, ItemsSeen: 1, ItemsNormalized: 1}, nil
			}

			stats, err := importer.Run(ctx, ImportConfig{
				SourceID: DefaultSourceID, BaseURL: "https://abs.example.com", APIKey: "secret",
				LibraryID: "lib-books", Label: "Shelf", Enabled: true,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if stats.BooksCreated != 0 || stats.BooksLinked != 1 {
				t.Fatalf("stats = %+v, want the existing row linked and no book created", stats)
			}
			books, err := bookRepo.ListIncludingExcluded(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				t.Fatalf("books = %d, want 1 (no twin)", len(books))
			}
		})
	}
}
