package bnf

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// maxTermRunes bounds a search term. A title longer than this is cut, which
// only makes an "all" search a little looser.
const maxTermRunes = 200

// cqlTerm turns user text into one quoted CQL term, or "" when nothing
// searchable is left.
//
// The characters CQL gives a meaning to inside or around a quoted term are
// replaced with a space rather than backslash escaped: the double quote and
// backslash (which end or escape the term), the masking characters *, ? and
// ^, and parentheses, comparison and slash characters for good measure.
// Replacing is safer than escaping because BnF's handling of \" could not be
// confirmed: sent `x\" or bib.title any \"*`, the server echoed the query
// back as `bib.title all "x" or bib.title any "*"`. Searching with "all" or
// "any" matches words, so a dropped punctuation mark costs nothing, and a CQL
// keyword such as "or" inside the quotes is just a word. The apostrophe of
// an elided article ("L'outsider") is kept: it is not special in CQL and BnF
// matches it.
//
// The text is composed (NFC) first: a title typed or pasted on macOS can
// arrive decomposed, "e" plus a combining accent, while BnF's records are
// composed, and the rune cap below should count letters, not marks.
func cqlTerm(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= maxTermRunes {
			break
		}
		switch {
		case strings.ContainsRune(`"\*?^()<>=/`, r), unicode.IsControl(r):
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	term := strings.Join(strings.Fields(b.String()), " ")
	if term == "" {
		return ""
	}
	return `"` + term + `"`
}

// bookFilter restricts a bibliographic search to printed text (doctype "a").
// It keeps out sound recordings and printed music, which include the third
// party music records BnF's terms carve out of the open licence, as well as
// maps, images and objects. parseRecord checks each record again.
const bookFilter = ` and bib.doctype any "a"`
