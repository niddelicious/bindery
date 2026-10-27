package api

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// TestAuthorSync_FilingFormTitleDoesNotCreateATwin is the #3122 scenario end to
// end. A library row filed as "Trace of Death, A" (Calibre's sort-title
// convention) and a catalogue that spells the same work "A Trace of Death" must
// meet: the sync matches the existing row instead of creating a second one.
// Both directions are exercised, because the key has to be symmetric.
func TestAuthorSync_FilingFormTitleDoesNotCreateATwin(t *testing.T) {
	for _, tc := range []struct {
		name, stored, incoming string
	}{
		{"stored filing form, catalogue natural order", "Trace of Death, A", "A Trace of Death"},
		{"stored natural order, catalogue filing form", "A Trace of Death", "Trace of Death, A"},
		{"the article", "Hobbit, The", "The Hobbit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := db.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })

			authorRepo := db.NewAuthorRepo(database)
			bookRepo := db.NewBookRepo(database)
			profileRepo := db.NewMetadataProfileRepo(database)

			author := &models.Author{
				ForeignID: "OL3122A", Name: "Filing Author", SortName: "Author, Filing",
				MetadataProvider: "openlibrary",
			}
			if err := authorRepo.Create(ctx, author); err != nil {
				t.Fatal(err)
			}
			existing := &models.Book{
				AuthorID: author.ID, ForeignID: "calibre-3122", Title: tc.stored, SortTitle: tc.stored,
				Language: "eng", Status: models.BookStatusWanted, MetadataProvider: "calibre", Genres: []string{},
			}
			if err := bookRepo.Create(ctx, existing); err != nil {
				t.Fatal(err)
			}

			provider := &stubMetaProvider{
				works: []models.Book{{
					ForeignID: "OL-3122-W", Title: tc.incoming, SortTitle: tc.incoming, Language: "eng",
					Status: models.BookStatusWanted, MetadataProvider: "openlibrary", Genres: []string{},
				}},
			}
			h := NewAuthorHandler(authorRepo, nil, bookRepo, nil, metadata.NewAggregator(provider), nil, profileRepo, nil)
			h.FetchAuthorBooks(author, false, "")

			books, err := bookRepo.ListByAuthorIncludingExcluded(ctx, author.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(books) != 1 {
				var titles []string
				for _, b := range books {
					titles = append(titles, b.Title)
				}
				t.Fatalf("catalogue sync left %d rows %q, want the one existing row and no twin", len(books), titles)
			}
		})
	}
}
