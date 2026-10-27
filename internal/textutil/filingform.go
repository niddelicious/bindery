package textutil

import (
	"strings"
	"unicode"
)

// articles is the closed set of whole-word articles that title handling treats
// as an article: dropped from the front by the Review duplicates article rule,
// and recognised behind the last comma in the library filing form. It is one
// list so the two readings cannot drift (#3122): Review duplicates and the
// persisted books.dedup_key must agree on what "Trace of Death, A" is.
//
// Closed on purpose and matched as a complete word, so "Theatre of Blood" does
// not read as "The atre". Every entry is already lowercase.
var articles = map[string]struct{}{
	// English
	"the": {}, "a": {}, "an": {},
	// German
	"der": {}, "die": {}, "das": {},
	// French / Spanish / Portuguese
	"le": {}, "la": {}, "les": {}, "el": {}, "los": {}, "las": {}, "de": {},
	// Italian
	"il": {}, "gli": {}, "una": {},
	// Dutch
	"een": {}, "het": {},
}

// IsArticle reports whether word, compared case-insensitively, is one of the
// recognised articles.
func IsArticle(word string) bool {
	_, ok := articles[strings.ToLower(word)]
	return ok
}

// SplitFilingForm recognises the library filing form, where a title's leading
// article is moved behind a comma: "Trace of Death, A", "Hobbit, The". It
// returns the title without the article ("Trace of Death") and the article as
// written ("A").
//
// Only a LONE article after the LAST comma counts, with a non-empty title in
// front of it, so "Love, Actually", "Me, Myself and I" and "Dust, Ash, and
// Bone" are left alone. The test is on the shape of the tail, not on the title
// as a whole: "Rock, Paper, Scissors, A" is a filing form of "A Rock, Paper,
// Scissors". A title that genuinely ends in ", A" meaning something else is
// the accepted cost of recognising the form at all, and it is the same cost
// Review duplicates already pays.
//
// The tail is read the way Review duplicates always read it, through the shared
// title fold: surrounding punctuation ("The.", "The)", "The!") and diacritics
// ("Él") do not hide the article, while a tail that folds to more than one word
// ("Hello, A World") is not a lone article. The returned article is the tail as
// written minus that surrounding punctuation.
//
// ok is false when title is not in filing form.
func SplitFilingForm(title string) (base, article string, ok bool) {
	t := strings.TrimSpace(title)
	idx := strings.LastIndex(t, ",")
	if idx < 0 {
		return title, "", false
	}
	head := strings.TrimSpace(t[:idx])
	tail := strings.TrimFunc(t[idx+1:], func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if head == "" || tail == "" {
		return title, "", false
	}
	if words := strings.Fields(FoldForSlug(FoldForTitleMatch(tail))); len(words) != 1 || !IsArticle(words[0]) {
		return title, "", false
	}
	return head, tail, true
}

// RestoreFilingForm rewrites a filing-form title into natural order
// ("Trace of Death, A" becomes "A Trace of Death") and returns any other title
// unchanged.
func RestoreFilingForm(title string) string {
	if base, article, ok := SplitFilingForm(title); ok {
		return article + " " + base
	}
	return title
}
