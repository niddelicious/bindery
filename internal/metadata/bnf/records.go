package bnf

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

// arkNameRe matches the name part of a BnF catalogue ARK: "cb", eight or nine
// digits and a NOID check character. It is validated before it is put in a
// query and before it becomes a foreign ID.
var arkNameRe = regexp.MustCompile(`^cb[0-9]{8,9}[0-9bcdfghjkmnpqrstvwxz]$`)

// arkInTextRe finds a catalogue ARK in a record's id attribute or its 003
// control field ("http://catalogue.bnf.fr/ark:/12148/cb48780493c").
var arkInTextRe = regexp.MustCompile(`ark:/12148/(cb[0-9a-z]+)`)

// authorityIDRe matches a BnF authority record number, the value of $3 in a
// UNIMARC 7XX field and of aut.recordid.
var authorityIDRe = regexp.MustCompile(`^[0-9]{8}$`)

// edition is what Bindery reads from one bibliographic record. BnF catalogues
// manifestations, so one record is one edition; works are assembled from
// them in works.go.
type edition struct {
	ark           string // "cb48780493c"
	title         string
	originalTitle string // 454 $t, the title of the work this translates
	language      string // normalised ISO 639-2/B
	publisher     string
	date          *time.Time
	entered       string // 100 $a date entered on file, YYYYMMDD
	isbn13        []string
	isbn10        []string
	pages         int
	ebook         bool
	description   string
	credits       []credit
}

// credit is one name in a UNIMARC 700, 701 or 702 field.
type credit struct {
	authorityID string // BnF authority record number, "" when the record has no link
	surname     string
	forename    string
	author      bool
}

// sortName is the catalogue form, "Carroll, Lewis".
func (c credit) sortName() string {
	if c.forename == "" {
		return c.surname
	}
	return c.surname + ", " + c.forename
}

// displayName is "Lewis Carroll". A forename ending in an elided particle
// ("Jean d'") joins the surname without a space: "Jean d'Ormesson".
func (c credit) displayName() string {
	if c.forename == "" {
		return c.surname
	}
	if strings.HasSuffix(c.forename, "'") || strings.HasSuffix(c.forename, "\u2019") {
		return c.forename + c.surname
	}
	return c.forename + " " + c.surname
}

func (c credit) toAuthor() models.Author {
	return models.Author{
		ForeignID:        authorPrefix + c.authorityID,
		Name:             c.displayName(),
		SortName:         c.sortName(),
		MetadataProvider: providerName,
	}
}

// parseRecord turns a bibliographic record into an edition. ok is false for
// anything that is not a printed or electronic text monograph Bindery can
// use: authority records, sound recordings and printed music (the CGU's
// third party music records among them), issues of a periodical, and records
// without an ARK or a title. Every field is optional; a missing one is left
// empty.
func parseRecord(r marcRecord) (edition, bool) {
	if !isBookRecord(r) {
		return edition{}, false
	}
	ark := recordARK(r)
	if ark == "" {
		return edition{}, false
	}
	credits := recordCredits(r)
	surnames := make([]string, 0, len(credits))
	for _, c := range credits {
		surnames = append(surnames, c.surname)
	}
	title := recordTitle(r, surnames)
	if title == "" {
		return edition{}, false
	}
	ed := edition{
		ark:           ark,
		title:         title,
		originalTitle: originalTitle(r, title, surnames),
		language:      models.NormalizeLanguageCode(r.first("101", "a")),
		description:   recordDescription(r),
		credits:       credits,
	}
	if f := r.first("100", "a"); len(f) >= 8 && allDigits(f[:8]) {
		ed.entered = f[:8]
	}
	ed.publisher, ed.date = publication(r)
	if ed.date == nil {
		// 100 $a positions 9 to 12 hold the first publication date.
		if f := r.first("100", "a"); len(f) >= 13 {
			ed.date = parseYear(f[9:13])
		}
	}
	ed.isbn13, ed.isbn10 = recordISBNs(r)
	ed.pages = pageCount(r.first("215", "a"))
	ed.ebook = isElectronic(r)
	return ed, true
}

// isBookRecord keeps text monographs. The SRU query already asks for
// bib.doctype "a" (printed text); this is the second check, on the record
// itself, because BnF's record format is changing (Noemi, 2026) and a
// response must not be trusted to honour the filter.
//
//   - Authority records are never books.
//   - Leader position 6 is the type of record: "a" is language material. A
//     blank is tolerated because records converted from BnF's new model in
//     2026 leave the leader positions empty; anything else (music, sound
//     recordings, images, maps) is dropped.
//   - Leader position 7 is the bibliographic level. A serial ("s") or an
//     integrating resource ("i", a loose leaf or a website) is not a book,
//     and bib.doctype "a" does include periodicals: a search for "Le Monde
//     diplomatique" returned ten serial title records. Monographs ("m"),
//     collections ("c"), component parts ("a") and the blank of a 2026
//     record are kept.
//   - An RDA content type (181 $c), when present, must include "txt".
//   - A record linked to a periodical by ISSN (461 $x) is an issue of a
//     magazine, not a book.
func isBookRecord(r marcRecord) bool {
	if r.Type != "" && !strings.EqualFold(r.Type, "Bibliographic") {
		return false
	}
	if len(r.Leader) > 6 {
		switch r.Leader[6] {
		case 'a', ' ':
		default:
			return false
		}
	}
	if len(r.Leader) > 7 {
		switch r.Leader[7] {
		case 's', 'i':
			return false
		}
	}
	if content := r.all("181", "c"); len(content) > 0 {
		text := false
		for _, c := range content {
			if strings.EqualFold(c, "txt") {
				text = true
			}
		}
		if !text {
			return false
		}
	}
	for _, df := range r.fields("461") {
		if df.first("x") != "" {
			return false
		}
	}
	return true
}

// recordARK returns the ARK name ("cb48780493c") from the record's id
// attribute or its 003 control field, or "" when neither holds a valid one.
func recordARK(r marcRecord) string {
	for _, s := range []string{r.ID, r.controlField("003")} {
		if m := arkInTextRe.FindStringSubmatch(s); m != nil && arkNameRe.MatchString(m[1]) {
			return m[1]
		}
	}
	return ""
}

// recordTitle is the title proper (200 $a). A volume catalogued as a part of
// a larger work carries that work's name in $a and its own title in $i, so
// the part title wins: otherwise every volume of a multi-part work would be
// named after the whole. surnames are the record's people (7XX $a), which
// tell a statement of responsibility from a title (splitTitleStatement).
func recordTitle(r marcRecord, surnames []string) string {
	fs := r.fields("200")
	if len(fs) == 0 {
		return ""
	}
	if part := cleanTitle(fs[0].first("i")); part != "" {
		return part
	}
	title, _ := splitTitleStatement(fs[0].first("a"), surnames)
	return title
}

// originalTitle is the title of the work a translation renders: 454 $t, else
// a bracketed original in an older record's title statement, else the
// uniform title (500 $a) when it differs from the record's own title. BnF's
// 2026 records give a French uniform title for a French edition, which is
// not an original title, so that case is left out.
func originalTitle(r marcRecord, title string, surnames []string) string {
	if t := cleanTitle(r.first("454", "t")); t != "" {
		return t
	}
	if fs := r.fields("200"); len(fs) > 0 {
		if _, orig := splitTitleStatement(fs[0].first("a"), surnames); orig != "" {
			return orig
		}
	}
	if t := cleanTitle(r.first("500", "a")); t != "" && titleKey(t) != titleKey(title) {
		return t
	}
	return ""
}

// statementCutRe finds where a title statement stops being the title, in
// records BnF converted from its older catalogues, whose 200 $a holds the
// whole statement: "Alice au pays des merveilles ["Alice's adventures in
// wonderland"]. Traduction de Henriette Rouillard", "Alice au pays des
// merveilles , par Lewis Carroll. Adaptation Gisèle Vallerey". It matches a
// bracket, a " / " or " , " separator, or a full stop followed by a word that
// opens a statement of responsibility.
var statementCutRe = regexp.MustCompile(`(?i)\s\[|\s/\s|\s,\s|\.\s+(?:traduit|traduction|trad\.|adapt|illustr|préf|texte|version|par\s|d'après|introd|notes?\s|avec\s|dessins|images)`)

// byStatementRe finds a ", by" or ", par" with no space before the comma:
// "Sylvie and Bruno, by Lewis Carroll". Unlike " , " this is also how a
// title can read ("Ruby, by Moonlight"), so splitTitleStatement cuts there
// only when what follows names one of the record's people or is a plain
// responsibility cue (respCueRe).
var byStatementRe = regexp.MustCompile(`(?i),\s+(?:by|par)\s+`)

// respCueRe is a statement of responsibility that names nobody: "by the
// author of Alice in Wonderland", "par l'auteur de ...".
var respCueRe = regexp.MustCompile(`(?i)^(?:the\s+author\s+of\b|l['’]auteur\s+d)`)

// bracketedOriginalRe reads a bracketed original title: ["Alice in Wonderland"].
var bracketedOriginalRe = regexp.MustCompile(`\[\s*"([^"\]]+)"\s*\]`)

// leadingSicRe reads a title statement that opens with a name the
// cataloguer transcribed as printed and flagged: `L. Carrol ["sic"]. Alice au
// pays des merveilles`. Group 1 is the name, group 2 the rest.
var leadingSicRe = regexp.MustCompile(`(?i)^(.{1,60}?)\s*\[\s*"?\s*sic\s*"?\s*\]\s*\.\s+(\S.*)$`)

// splitTitleStatement returns the title proper of a 200 $a and, when the
// statement carries one in brackets, the original title. A modern record's
// $a is only the title and comes back unchanged.
//
// surnames are the record's people. A ", by <name>" is cut only when the
// name is one of theirs, and a leading name flagged [sic] is dropped only
// when it is a misspelling of one of theirs, so a title is never cut on
// punctuation alone. A bracketed "sic" is the cataloguer's marker, never an
// original title.
func splitTitleStatement(s string, surnames []string) (title, original string) {
	s = cleanTitle(s)
	if m := leadingSicRe.FindStringSubmatch(s); m != nil && namesSomeone(m[1], surnames, true) {
		s = m[2]
	}
	for _, m := range bracketedOriginalRe.FindAllStringSubmatch(s, -1) {
		if o := cleanTitle(m[1]); o != "" && !strings.EqualFold(o, "sic") {
			original = o
			break
		}
	}
	cut := -1
	if loc := statementCutRe.FindStringIndex(s); loc != nil && loc[0] > 0 {
		cut = loc[0]
	}
	for _, loc := range byStatementRe.FindAllStringIndex(s, -1) {
		if loc[0] == 0 || (cut >= 0 && loc[0] >= cut) {
			continue
		}
		if follow := s[loc[1]:]; respCueRe.MatchString(follow) || namesSomeone(follow, surnames, false) {
			cut = loc[0]
			break
		}
	}
	if cut > 0 {
		s = cleanTitle(s[:cut])
	}
	return s, original
}

// namesSomeone reports whether text names one of surnames: every word of a
// surname appears as a whole word of text, ignoring case and accents. With
// misspelt set, a word of four letters or more that a surname word starts
// with, or that starts with one, also counts: the names a cataloguer flags
// [sic] are misspellings ("L. Carrol" for Carroll).
func namesSomeone(text string, surnames []string, misspelt bool) bool {
	words := foldWords(text)
	for _, surname := range surnames {
		sw := foldWords(surname)
		if len(sw) == 0 {
			continue
		}
		all := true
		for _, w := range sw {
			if !hasWord(words, w, misspelt) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func hasWord(words []string, w string, misspelt bool) bool {
	for _, t := range words {
		if t == w {
			return true
		}
		if misspelt && len(t) >= 4 && len(w) >= 4 && (strings.HasPrefix(w, t) || strings.HasPrefix(t, w)) {
			return true
		}
	}
	return false
}

// foldWords splits s into case and accent folded words.
func foldWords(s string) []string {
	return strings.FieldsFunc(textutil.FoldForSearch(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// cleanTitle strips the non-sorting markers some UNIMARC exports put around a
// leading article (U+0098 and U+009C, C1 control characters like every other
// control character dropped here), collapses whitespace and trims trailing
// ISBD punctuation. An elided article ("L'outsider") is part of the title and
// is kept as is.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(strings.TrimRight(s, " /:;,.="))
}

// recordDescription returns the record's summary (330 $a) when it is the
// cataloguer's own or the publisher's, and "" otherwise. A summary whose
// source ($2) names a third party, such as the Electre database, is that
// party's text, not BnF data, and is left out.
func recordDescription(r marcRecord) string {
	for _, df := range r.fields("330") {
		source := strings.ToLower(strings.TrimSpace(df.first("2")))
		if source != "" && source != "éditeur" && source != "editeur" {
			continue
		}
		if text := strings.Join(strings.Fields(df.first("a")), " "); text != "" {
			return text
		}
	}
	return ""
}

// publication reads the publisher and year from 214 (the current field,
// indicator 2 "0" for publication; "3" is the printer) or the older 210.
func publication(r marcRecord) (string, *time.Time) {
	for _, df := range r.fields("214") {
		if strings.TrimSpace(df.Ind2) != "0" {
			continue
		}
		if pub, date := cleanPublisher(df.first("c")), parseYear(df.first("d")); pub != "" || date != nil {
			return pub, date
		}
	}
	for _, df := range r.fields("210") {
		if pub, date := cleanPublisher(df.first("c")), parseYear(df.first("d")); pub != "" || date != nil {
			return pub, date
		}
	}
	return "", nil
}

func cleanPublisher(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(strings.Trim(s, " []:;,"))
}

// recordISBNs collects ISBNs from 010 $a and the EAN in 073 $a, which for a
// book is its ISBN-13.
func recordISBNs(r marcRecord) (isbn13, isbn10 []string) {
	seen := make(map[string]bool)
	add := func(raw string) {
		i13, i10 := isbnutil.Extract(raw)
		if i13 != "" && !seen[i13] {
			seen[i13] = true
			isbn13 = append(isbn13, i13)
		}
		if i10 != "" && !seen[i10] {
			seen[i10] = true
			isbn10 = append(isbn10, i10)
		}
	}
	for _, v := range r.all("010", "a") {
		add(v)
	}
	for _, v := range r.all("073", "a") {
		if strings.HasPrefix(v, "978") || strings.HasPrefix(v, "979") {
			add(v)
		}
	}
	return isbn13, isbn10
}

var pagesRe = regexp.MustCompile(`(\d+)\s*p\b`)

// pageCount reads "185 p.", "1 vol. (569 p.)" or "1 volume 160 p" from the
// extent (215 $a). 0 when none is stated.
func pageCount(extent string) int {
	m := pagesRe.FindStringSubmatch(extent)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 || n > 20000 {
		return 0
	}
	return n
}

// isElectronic reports an e-book record: BnF describes one as "Texte
// électronique" (200 $b) or "ressource dématérialisée" (215 $a).
func isElectronic(r marcRecord) bool {
	for _, v := range r.all("200", "b") {
		if strings.Contains(strings.ToLower(v), "électronique") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(r.first("215", "a")), "dématérialisée")
}

// recordCredits lists the people in 700 (main responsibility), 701 and 702.
// Corporate bodies (71X) are not people and are not read.
func recordCredits(r marcRecord) []credit {
	var out []credit
	for _, tag := range []string{"700", "701", "702"} {
		for _, df := range r.fields(tag) {
			surname := cleanName(df.first("a"))
			if surname == "" {
				continue
			}
			c := credit{
				surname:  surname,
				forename: cleanName(df.first("b")),
				author:   isAuthorCredit(tag, df),
			}
			if id := df.first("3"); authorityIDRe.MatchString(id) {
				c.authorityID = id
			}
			out = append(out, c)
		}
	}
	return out
}

// cleanName trims a name part and drops a role BnF's Dublin Core export
// appends to it ("Lewis (Auteur du texte)", "Lewis. Traducteur"), should the
// same text reach a UNIMARC subfield.
func cleanName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, " ("); i > 0 && strings.HasSuffix(s, ")") && isRoleText(s[i+2:len(s)-1]) {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ". "); i > 0 && isRoleText(s[i+2:]) {
		s = s[:i]
	}
	return strings.TrimSpace(strings.TrimRight(s, " ,;:"))
}

// authorRelator is the UNIMARC relator code for an author. Others seen on
// BnF records: 730 translator, 440 illustrator, 340 editor, 100 the author a
// work is adapted from ("d'après").
const authorRelator = "070"

// isAuthorCredit reports whether a 7XX field credits the person as author of
// the text. A relator code ($4) decides when there is one. Records converted
// from BnF's 2026 model carry the role as text in $c instead ("Auteur du
// texte", "Traducteur"). With neither, only 700, the main responsibility
// field, counts as the author.
func isAuthorCredit(tag string, df dataField) bool {
	if codes := df.all("4"); len(codes) > 0 {
		for _, code := range codes {
			if code == authorRelator {
				return true
			}
		}
		return false
	}
	if roles := df.all("c"); len(roles) > 0 {
		for _, role := range roles {
			if isAuthorRole(role) {
				return true
			}
		}
		if anyRoleText(roles) {
			return false
		}
	}
	return tag == "700"
}

// authorRoles are the role labels meaning author of the text, lower case.
var authorRoles = map[string]bool{
	"auteur du texte": true, "autrice du texte": true, "auteur": true, "autrice": true,
}

// otherRoles are labels BnF uses for people who are not the author. The list
// only has to recognise a $c as a role rather than a name addition; any role
// not in authorRoles is not an author.
var otherRoles = map[string]bool{
	"traducteur": true, "traductrice": true, "illustrateur": true, "illustratrice": true,
	"préfacier": true, "préfacière": true, "postfacier": true, "éditeur scientifique": true,
	"éditrice scientifique": true, "adaptateur": true, "adaptatrice": true, "narrateur": true,
	"narratrice": true, "commentateur": true, "commentatrice": true, "auteur adapté": true,
	"autrice adaptée": true, "collaborateur": true, "collaboratrice": true,
}

func roleParts(role string) []string {
	var parts []string
	for _, p := range strings.Split(strings.ToLower(role), "/") {
		if p = strings.TrimSpace(strings.Trim(p, " .()")); p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func isAuthorRole(role string) bool {
	for _, p := range roleParts(role) {
		if authorRoles[p] {
			return true
		}
	}
	return false
}

func isRoleText(s string) bool {
	for _, p := range roleParts(s) {
		if authorRoles[p] || otherRoles[p] {
			return true
		}
	}
	return false
}

func anyRoleText(roles []string) bool {
	for _, r := range roles {
		if isRoleText(r) {
			return true
		}
	}
	return false
}

// parseYear reads the first plausible four-digit year in s: "2019",
// "DL 2019", "cop. 2010", "[2025]".
func parseYear(s string) *time.Time {
	run := 0
	for i, r := range s {
		if r < '0' || r > '9' {
			run = 0
			continue
		}
		run++
		if run == 4 {
			year, _ := strconv.Atoi(s[i-3 : i+1])
			if year < 1400 || year > 2100 {
				run = 0
				continue
			}
			t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			return &t
		}
	}
	return nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// authority is what Bindery reads from a BnF person authority record.
type authority struct {
	id       string
	heading  credit
	variants []string
}

// parseAuthority reads a person authority record. ok is false for anything
// else: a record that is not an authority, or one without a 200 heading.
// Only personal names are read; Bindery never asks for the Dewey or subject
// authority files.
func parseAuthority(r marcRecord) (authority, bool) {
	if !strings.EqualFold(r.Type, "Authority") {
		return authority{}, false
	}
	id := ""
	if m := arkInTextRe.FindStringSubmatch(r.ID); m != nil && len(m[1]) == 11 {
		id = m[1][2:10]
	}
	if f := r.controlField("001"); id == "" && strings.HasPrefix(f, "FRBNF") && len(f) >= 13 {
		id = f[5:13]
	}
	if !authorityIDRe.MatchString(id) {
		return authority{}, false
	}
	h := r.fields("200")
	if len(h) == 0 {
		return authority{}, false
	}
	heading := credit{authorityID: id, surname: cleanName(h[0].first("a")), forename: cleanName(h[0].first("b")), author: true}
	if heading.surname == "" {
		return authority{}, false
	}
	a := authority{id: id, heading: heading}
	seen := map[string]bool{heading.displayName(): true}
	for _, df := range r.fields("400") {
		v := credit{surname: cleanName(df.first("a")), forename: cleanName(df.first("b"))}
		if v.surname == "" || seen[v.displayName()] {
			continue
		}
		seen[v.displayName()] = true
		a.variants = append(a.variants, v.displayName())
	}
	return a, true
}
