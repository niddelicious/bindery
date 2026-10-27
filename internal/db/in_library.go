package db

import "github.com/vavallee/bindery/internal/models"

// InLibraryPredicate returns a SQL boolean expression, with no bind
// parameters, that is true for a book the library holds at least one file of.
// alias is the books table alias in the caller's query ("books", "b").
//
// "In library" is not "status = 'imported'". Status flips to imported only when
// every requested format is on disk, so a dual format book with one file is
// 'wanted' (#1634) and still owns something (#3132). It therefore matches an
// imported book, or a wanted book with a tracked book_files row. 'skipped'
// stays out whether or not a file exists, because it records a user decision.
// EXISTS, not a JOIN, so a book with several files is still one row.
//
// format narrows the file test for a per format view: "ebook" or "audiobook"
// requires a file of that format on a wanted book, any other value means any
// format. A book whose status is already imported matches regardless, since
// that status means everything it wants is on disk.
//
// Every count or list that answers "does the library hold this book" should
// call this so the rule cannot drift between the Books page, the Authors list
// and the author page.
func InLibraryPredicate(alias, format string) string {
	formatTest := ""
	if format == models.MediaTypeEbook || format == models.MediaTypeAudiobook {
		formatTest = " AND lib_bf.format = '" + format + "'"
	}
	return "(" + alias + ".status = '" + models.BookStatusImported + "' OR (" +
		alias + ".status = '" + models.BookStatusWanted + "' AND EXISTS (" +
		"SELECT 1 FROM book_files lib_bf WHERE lib_bf.book_id = " + alias + ".id" + formatTest + ")))"
}
