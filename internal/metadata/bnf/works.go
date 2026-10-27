package bnf

import (
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// frenchLanguage is the code the representative edition is preferred in.
// This provider exists for French libraries, so a work is named after its
// French edition when it has one.
const frenchLanguage = "fre"

// groupWorks folds edition records into one book per work, in first
// appearance order so BnF's relevance ranking survives.
//
// When authorID is set, only records crediting that authority record as
// author are kept: author2bib also returns the records where the person is
// translator, illustrator or the author a work is adapted from.
//
// Two editions belong to one work when they share a title, or when one's
// title is the other's original title (454 $t). The second rule is what puts
// "Alice au pays des merveilles" and "Aventures d'Alice au pays des
// merveilles" together: both translate "Alice's adventures in Wonderland".
// Titles are compared per author, so two authors' books of the same name
// stay apart. Callers link unlinked credits first (Client.linkUnlinked), so
// an edition BnF has not linked to its author still joins the work.
func groupWorks(eds []edition, authorID string) []models.Book {
	type entry struct {
		ed     edition
		author *credit
	}
	var kept []entry
	for _, ed := range eds {
		a := primaryAuthor(ed, authorID)
		if authorID != "" && a == nil {
			continue
		}
		kept = append(kept, entry{ed: ed, author: a})
	}

	uf := newUnionFind()
	nodes := make([]string, len(kept))
	for i, e := range kept {
		ak := authorKey(e.author)
		nodes[i] = ak + "|" + titleKey(e.ed.title)
		uf.add(nodes[i])
		if e.ed.originalTitle != "" {
			orig := ak + "|" + titleKey(e.ed.originalTitle)
			uf.add(orig)
			uf.union(nodes[i], orig)
		}
	}

	var order []string
	groups := make(map[string][]edition)
	for i, e := range kept {
		root := uf.find(nodes[i])
		if _, ok := groups[root]; !ok {
			order = append(order, root)
		}
		groups[root] = append(groups[root], e.ed)
	}
	books := make([]models.Book, 0, len(order))
	for _, root := range order {
		books = append(books, buildWork(groups[root], authorID))
	}
	return books
}

// buildWork turns one work's editions into a book.
//
// The representative edition supplies the foreign ID. It is the French
// edition entered on file first, so the ID stays put when BnF catalogues a
// new printing; the title is the one most French editions carry, so one
// record's cataloguing cannot rename the book.
func buildWork(group []edition, authorID string) models.Book {
	rep := group[0]
	for _, ed := range group[1:] {
		if betterRepresentative(ed, rep) {
			rep = ed
		}
	}
	b := models.Book{
		ForeignID:        idPrefix + rep.ark,
		Title:            commonTitle(group, rep),
		OriginalTitle:    commonOriginalTitle(group),
		Description:      rep.description,
		Language:         rep.language,
		MetadataProvider: providerName,
		Monitored:        true,
		Status:           models.BookStatusWanted,
		Genres:           []string{},
	}
	b.SortTitle = b.Title
	if a := workAuthor(group, rep, authorID); a != nil {
		author := a.toAuthor()
		b.Author = &author
	}
	seenCredit := make(map[string]bool)
	for _, ed := range group {
		if b.Description == "" {
			b.Description = ed.description
		}
		if ed.date != nil && ed.language == rep.language && (b.ReleaseDate == nil || ed.date.Before(*b.ReleaseDate)) {
			b.ReleaseDate = ed.date
		}
		for _, c := range ed.credits {
			if c.author && c.authorityID != "" && !seenCredit[c.authorityID] {
				seenCredit[c.authorityID] = true
				b.CreditedAuthorForeignIDs = append(b.CreditedAuthorForeignIDs, authorPrefix+c.authorityID)
			}
		}
		b.Editions = append(b.Editions, toEdition(ed))
		b.ProviderISBNs = append(b.ProviderISBNs, ed.isbn13...)
		b.ProviderISBNs = append(b.ProviderISBNs, ed.isbn10...)
	}
	return b
}

// betterRepresentative reports whether a should replace b as the work's
// representative: French first, then the earlier entry date, then the ARK,
// so the choice is the same on every fetch of the same records.
func betterRepresentative(a, b edition) bool {
	if fa, fb := a.language == frenchLanguage, b.language == frenchLanguage; fa != fb {
		return fa
	}
	if a.entered != b.entered {
		if a.entered == "" || b.entered == "" {
			return a.entered != ""
		}
		return a.entered < b.entered
	}
	return a.ark < b.ark
}

// commonTitle is the title most editions in the representative's language
// carry. Ties go to the representative's own title, then to sort order.
func commonTitle(group []edition, rep edition) string {
	counts := make(map[string]int)
	for _, ed := range group {
		if ed.language == rep.language {
			counts[ed.title]++
		}
	}
	return mostCommon(counts, rep.title)
}

func commonOriginalTitle(group []edition) string {
	counts := make(map[string]int)
	for _, ed := range group {
		if ed.originalTitle != "" {
			counts[ed.originalTitle]++
		}
	}
	return mostCommon(counts, "")
}

func mostCommon(counts map[string]int, fallback string) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best := fallback
	for _, k := range keys {
		if counts[k] > counts[best] {
			best = k
		}
	}
	return best
}

func toEdition(ed edition) models.Edition {
	e := models.Edition{
		ForeignID:   idPrefix + ed.ark,
		Title:       ed.title,
		Publisher:   ed.publisher,
		PublishDate: ed.date,
		Language:    ed.language,
		IsEbook:     ed.ebook,
	}
	if len(ed.isbn13) > 0 {
		v := ed.isbn13[0]
		e.ISBN13 = &v
	}
	if len(ed.isbn10) > 0 {
		v := ed.isbn10[0]
		e.ISBN10 = &v
	}
	if ed.pages > 0 {
		pages := ed.pages
		e.NumPages = &pages
	}
	if ed.ebook {
		e.Format = models.MediaTypeEbook
	}
	return e
}

// primaryAuthor is the record's author credit: the one linked to authorID
// when given, otherwise the first author credit, preferring a linked one.
func primaryAuthor(ed edition, authorID string) *credit {
	var first *credit
	for i := range ed.credits {
		c := &ed.credits[i]
		if !c.author {
			continue
		}
		if authorID != "" {
			if c.authorityID == authorID {
				return c
			}
			continue
		}
		if c.authorityID != "" {
			return c
		}
		if first == nil {
			first = c
		}
	}
	return first
}

// workAuthor is the author the book is filed under. Records converted to
// BnF's 2026 model name the author without an authority link; when the
// representative is one of those, a linked credit for the same name on
// another edition of the work stands in, so the book still carries an author
// Bindery can look up. With no linked credit anywhere the book has no
// author: a name alone is not an identity Bindery can bind.
func workAuthor(group []edition, rep edition, authorID string) *credit {
	a := primaryAuthor(rep, authorID)
	if a != nil && a.authorityID != "" {
		return a
	}
	for _, ed := range group {
		for i := range ed.credits {
			c := &ed.credits[i]
			if !c.author || c.authorityID == "" {
				continue
			}
			if a == nil || sameName(*a, *c) {
				return c
			}
		}
	}
	return nil
}

// linkByName gives an unlinked author credit the authority link a credit of
// the exact same name carries elsewhere in the same result set, so the
// editions BnF converted to its 2026 model (which arrive without links) join
// their work instead of forming a second one. A name shared by two linked
// people links nothing. The input is not modified.
func linkByName(eds []edition) []edition {
	ids := make(map[string]string)
	for _, ed := range eds {
		for _, c := range ed.credits {
			if !c.author || c.authorityID == "" {
				continue
			}
			key := textutil.FoldForSearch(c.sortName())
			if prev, ok := ids[key]; ok && prev != c.authorityID {
				ids[key] = "" // ambiguous
				continue
			}
			ids[key] = c.authorityID
		}
	}
	out := make([]edition, len(eds))
	for i, ed := range eds {
		out[i] = ed
		changed := false
		for j, c := range ed.credits {
			if !c.author || c.authorityID != "" {
				continue
			}
			if id := ids[textutil.FoldForSearch(c.sortName())]; id != "" {
				if !changed {
					out[i].credits = append([]credit(nil), ed.credits...)
					changed = true
				}
				out[i].credits[j].authorityID = id
			}
		}
	}
	return out
}

func sameName(a, b credit) bool {
	return textutil.FoldForSearch(a.sortName()) == textutil.FoldForSearch(b.sortName())
}

func authorKey(c *credit) string {
	if c == nil {
		return ""
	}
	if c.authorityID != "" {
		return c.authorityID
	}
	return textutil.FoldForSearch(c.sortName())
}

// titleKey is the grouping key for a title. Spaces are dropped so a
// cataloguing slip that splits a word stays with its work, accents,
// ligatures and case are folded with the search key's fold (older converted
// records drop them: "Les Misérables" and "les miserables", "Œuvres" and
// "Oeuvres" are one work), and a typographic apostrophe is the same as a
// straight one ("L’outsider", "L'outsider").
func titleKey(title string) string {
	title = strings.ReplaceAll(title, "\u2019", "'")
	return strings.ReplaceAll(textutil.FoldForSearch(indexer.CanonicalDedupKey(title)), " ", "")
}

// unionFind joins title keys that name the same work.
type unionFind struct{ parent map[string]string }

func newUnionFind() *unionFind { return &unionFind{parent: make(map[string]string)} }

func (u *unionFind) add(k string) {
	if _, ok := u.parent[k]; !ok {
		u.parent[k] = k
	}
}

func (u *unionFind) find(k string) string {
	for u.parent[k] != k {
		u.parent[k] = u.parent[u.parent[k]]
		k = u.parent[k]
	}
	return k
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}
