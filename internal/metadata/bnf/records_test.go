package bnf

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The records in this file are invented, in the shape of BnF's UNIMARC.

func record(t *testing.T, leader, body string) marcRecord {
	t.Helper()
	doc := `<record xmlns="info:lc/xmlns/marcxchange-v2" format="UNIMARC" id="ark:/12148/cb12345678x" type="Bibliographic">` +
		`<leader>` + leader + `</leader>` + body + `</record>`
	var r marcRecord
	if err := xml.Unmarshal([]byte(doc), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func df(tag string, subfields ...string) string {
	var b strings.Builder
	b.WriteString(`<datafield tag="` + tag + `" ind1=" " ind2="0">`)
	for i := 0; i+1 < len(subfields); i += 2 {
		b.WriteString(`<subfield code="` + subfields[i] + `">` + subfields[i+1] + `</subfield>`)
	}
	b.WriteString(`</datafield>`)
	return b.String()
}

const bookLeader = "     nam  22        450 "

func TestCredits_Roles(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		author bool
	}{
		{"relator 070 is the author", df("700", "a", "King", "b", "Stephen", "4", "070"), true},
		{"relator 730 is the translator", df("702", "a", "Esch", "b", "Jean", "4", "730"), false},
		{"relator 100 is the author adapted from", df("702", "a", "Carroll", "b", "Lewis", "4", "100"), false},
		{"2026 text role: author", df("702", "a", "Carroll", "b", "Lewis", "c", "Auteur du texte"), true},
		{"2026 text role: both genders", df("702", "a", "King", "b", "Stephen", "c", "Auteur du texte / Autrice du texte"), true},
		{"2026 text role: translator", df("702", "a", "Bué", "b", "Henri", "c", "Traducteur"), false},
		{"main entry with no role", df("700", "a", "Hugo", "b", "Victor"), true},
		{"added entry with no role", df("702", "a", "Tenniel", "b", "John"), false},
		{"name addition is not a role", df("700", "a", "Dumas", "b", "Alexandre", "c", "fils"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ed, ok := parseRecord(record(t, bookLeader, df("200", "a", "Titre")+tc.field))
			if !ok || len(ed.credits) != 1 {
				t.Fatalf("parse = %+v, %v", ed, ok)
			}
			if ed.credits[0].author != tc.author {
				t.Errorf("author = %v, want %v", ed.credits[0].author, tc.author)
			}
		})
	}
}

func TestCredits_NamesWithRolesAndElision(t *testing.T) {
	ed, ok := parseRecord(record(t, bookLeader, df("200", "a", "Titre")+
		df("700", "a", "Carroll", "b", "Lewis (Auteur du texte)", "3", "11885918")+
		df("702", "a", "Bué", "b", "Henri. Traducteur")+
		df("701", "a", "Ormesson", "b", "Jean d'", "4", "070")))
	if !ok || len(ed.credits) != 3 {
		t.Fatalf("parse = %+v, %v", ed, ok)
	}
	if got := ed.credits[0].displayName(); got != "Lewis Carroll" {
		t.Errorf("display = %q, want the role dropped", got)
	}
	// Credits are read 700, 701, 702 in turn.
	if got := ed.credits[2].sortName(); got != "Bué, Henri" {
		t.Errorf("sort = %q, want the role dropped", got)
	}
	if got := ed.credits[1].displayName(); got != "Jean d'Ormesson" {
		t.Errorf("display = %q, want the elided particle joined", got)
	}
	if got := ed.credits[1].sortName(); got != "Ormesson, Jean d'" {
		t.Errorf("sort = %q", got)
	}
}

// Only text monographs are books. The CGU carve out the third party music
// records, so sound recordings and printed music must never get through.
func TestParseRecord_DropsWhatIsNotABook(t *testing.T) {
	title := df("200", "a", "Titre")
	cases := []struct {
		name string
		rec  marcRecord
		keep bool
	}{
		{"printed text", record(t, bookLeader, title), true},
		{"2026 record with a blank leader", record(t, "     a    22        450 ", title+df("181", "c", "txt", "2", "rdacontent")), true},
		{"musical sound recording", record(t, "     njm  22        450 ", title), false},
		{"printed music", record(t, "     ncm  22        450 ", title), false},
		{"spoken word content type", record(t, "     a    22        450 ", title+df("181", "c", "spw", "2", "rdacontent")), false},
		{"periodical issue", record(t, bookLeader, title+df("461", "t", "J'aime lire", "x", "0399-4600", "v", "398")), false},
		// A serial's own title record (leader position 7 "s") carries no 461
		// link, and doctype "a" includes periodicals: a search for "Le Monde
		// diplomatique" came back as ten such records, all read as books.
		{"serial title record", record(t, "     cas  22        450 ", df("011", "a", "0026-9395")+df("200", "a", "Le Monde diplomatique")+df("210", "a", "Paris", "c", "Le Monde", "d", "1954-")), false},
		{"integrating resource", record(t, "     nai  22        450 ", df("200", "a", "Encyclopédie en ligne")), false},
		{"collection of monographs", record(t, "     nac  22        450 ", title), true},
		{"component part", record(t, "     naa  22        450 ", title), true},
		{"no title", record(t, bookLeader, df("010", "a", "978-2-07-062897-1")), false},
	}
	for _, tc := range cases {
		if _, ok := parseRecord(tc.rec); ok != tc.keep {
			t.Errorf("%s: kept = %v, want %v", tc.name, ok, tc.keep)
		}
	}
	auth := record(t, bookLeader, title)
	auth.Type = "Authority"
	if _, ok := parseRecord(auth); ok {
		t.Error("an authority record was read as a book")
	}
	noARK := record(t, bookLeader, title)
	noARK.ID = "ark:/12148/../../x"
	if _, ok := parseRecord(noARK); ok {
		t.Error("a record without a valid ARK was kept")
	}
}

func TestParseRecord_Fields(t *testing.T) {
	ed, ok := parseRecord(record(t, bookLeader,
		df("100", "a", "20190206d2019    m  y0frey50      ba")+
			df("101", "a", "FRE ", "c", "eng")+
			df("200", "a", "\u0098Les \u009cRougon-Macquart", "h", "1", "i", "La fortune des Rougon")+
			`<datafield tag="214" ind1=" " ind2="3"><subfield code="c">Impr. CPI</subfield></datafield>`+
			df("214", "a", "Paris", "c", "Albin Michel", "d", "DL 2019")+
			df("215", "a", "1 vol. (576 p.)")+
			df("330", "a", "Résumé de l'éditeur.", "2", "éditeur")+
			df("454", "t", "The outsider")+
			df("856", "u", "1239559", "b", "Première de couverture")))
	if !ok {
		t.Fatal("not parsed")
	}
	if ed.title != "La fortune des Rougon" {
		t.Errorf("title = %q, want the part title", ed.title)
	}
	if got := cleanTitle("\u0098Les \u009cmisérables /"); got != "Les misérables" {
		t.Errorf("cleanTitle = %q, want the non-sorting markers and ISBD punctuation gone", got)
	}
	if ed.language != "fre" || ed.publisher != "Albin Michel" || ed.pages != 576 || ed.date == nil || ed.date.Year() != 2019 {
		t.Errorf("edition = %+v", ed)
	}
	if ed.originalTitle != "The outsider" || ed.entered != "20190206" {
		t.Errorf("original = %q entered = %q", ed.originalTitle, ed.entered)
	}
	if ed.description != "Résumé de l'éditeur." {
		t.Errorf("description = %q", ed.description)
	}
	b := buildWork([]edition{ed}, "")
	if b.ImageURL != "" || b.Editions[0].ImageURL != "" {
		t.Error("a cover URL was taken from the record; BnF covers are never used")
	}
}

func TestRecordDescription_ThirdPartySummaryIsDropped(t *testing.T) {
	for source, want := range map[string]string{"Electre": "", "Dilicom": "", "éditeur": "texte", "": "texte"} {
		fields := []string{"a", "texte"}
		if source != "" {
			fields = append(fields, "2", source)
		}
		ed, _ := parseRecord(record(t, bookLeader, df("200", "a", "Titre")+df("330", fields...)))
		if ed.description != want {
			t.Errorf("source %q: description = %q, want %q", source, ed.description, want)
		}
	}
}

func edWith(ark, title, orig, lang, entered string, credits ...credit) edition {
	return edition{ark: ark, title: title, originalTitle: orig, language: lang, entered: entered, credits: credits}
}

var (
	carroll = credit{authorityID: "11885918", surname: "Carroll", forename: "Lewis", author: true}
	bue     = credit{authorityID: "12345678", surname: "Bué", forename: "Henri"}
)

func TestGroupWorks_TranslationsOfOneOriginalAreOneWork(t *testing.T) {
	books := groupWorks([]edition{
		edWith("cb10000001a", "Alice au pays des merveilles", "Alice's adventures in Wonderland", "fre", "19900101", carroll),
		edWith("cb10000002b", "Aventures d'Alice au pays des merveilles", "Alice's adventures in Wonderland", "fre", "20000101", carroll),
		edWith("cb10000003c", "Alice au pays des merveilles", "", "fre", "20260601", carroll),
		edWith("cb10000004d", "L’outsider", "", "fre", "20190101", carroll),
		edWith("cb10000005f", "L'outsider", "", "fre", "20200101", carroll),
	}, "")
	if len(books) != 2 {
		t.Fatalf("works = %d, want 2", len(books))
	}
	if books[0].Title != "Alice au pays des merveilles" || len(books[0].Editions) != 3 || books[0].ForeignID != "bnf:cb10000001a" {
		t.Errorf("first work = %q %s with %d editions", books[0].Title, books[0].ForeignID, len(books[0].Editions))
	}
	if books[0].OriginalTitle != "Alice's adventures in Wonderland" {
		t.Errorf("original title = %q", books[0].OriginalTitle)
	}
	if len(books[1].Editions) != 2 {
		t.Errorf("L'outsider editions = %d, want the two apostrophes grouped", len(books[1].Editions))
	}
}

// Accents, ligatures and case are cataloguing variation, not different
// works: older converted records often drop them.
func TestGroupWorks_TitleKeyFoldsAccentsAndLigatures(t *testing.T) {
	books := groupWorks([]edition{
		edWith("cb10000001b", "Les Misérables", "", "fre", "19900101", carroll),
		edWith("cb10000002c", "les miserables", "", "fre", "19950101", carroll),
		edWith("cb10000003d", "Œuvres complètes", "", "fre", "19900101", carroll),
		edWith("cb10000004f", "Oeuvres completes", "", "fre", "19950101", carroll),
		edWith("cb10000005g", "Les Misérables", "", "fre", "19900101", bue),
	}, "")
	if len(books) != 3 {
		for _, b := range books {
			t.Logf("work %s %q with %d editions", b.ForeignID, b.Title, len(b.Editions))
		}
		t.Fatalf("works = %d, want 3: one per title per author", len(books))
	}
	if len(books[0].Editions) != 2 || len(books[1].Editions) != 2 {
		t.Errorf("editions = %d, %d, want the spelling variants grouped", len(books[0].Editions), len(books[1].Editions))
	}
}

// The French edition represents the work even when an edition in another
// language was entered on file earlier and comes first in the results.
func TestGroupWorks_FrenchEditionRepresentsTheWork(t *testing.T) {
	books := groupWorks([]edition{
		edWith("cb10000001a", "Alice's adventures in Wonderland", "", "eng", "19800101", carroll),
		edWith("cb10000002b", "Alice au pays des merveilles", "Alice's adventures in Wonderland", "fre", "20000101", carroll),
		edWith("cb10000003c", "Alicia en el país de las maravillas", "Alice's adventures in Wonderland", "spa", "19700101", carroll),
	}, "")
	if len(books) != 1 {
		t.Fatalf("works = %d, want 1", len(books))
	}
	if b := books[0]; b.ForeignID != "bnf:cb10000002b" || b.Title != "Alice au pays des merveilles" || b.Language != "fre" {
		t.Errorf("work = %s %q %s, want the French edition bnf:cb10000002b", b.ForeignID, b.Title, b.Language)
	}
}

func TestGroupWorks_TranslatorIsNeverTheAuthor(t *testing.T) {
	unlinked := credit{surname: "Carroll", forename: "Lewis", author: true}
	books := groupWorks([]edition{edWith("cb10000001a", "Alice au pays des merveilles", "", "fre", "", unlinked, bue)}, "")
	if len(books) != 1 {
		t.Fatalf("works = %d", len(books))
	}
	if a := books[0].Author; a != nil {
		t.Errorf("author = %+v, want none: the only linked name is the translator's", a)
	}
}

func TestGroupWorks_AuthorFilter(t *testing.T) {
	adaptation := credit{authorityID: "11885918", surname: "Carroll", forename: "Lewis"} // relator 100
	books := groupWorks([]edition{
		edWith("cb10000001a", "Alice", "", "fre", "", carroll),
		edWith("cb10000002b", "Alice (Disney)", "", "fre", "", adaptation),
		edWith("cb10000003c", "Le Hobbit", "", "fre", "", bue),
	}, "11885918")
	if len(books) != 1 || books[0].Title != "Alice" {
		t.Errorf("books = %+v, want only the record crediting him as author", books)
	}
}

func TestParseAuthority(t *testing.T) {
	r := record(t, "     cx  a22        45  ",
		df("200", "a", "Carroll", "b", "Lewis", "f", "1832-1898")+
			df("400", "a", "Dodgson", "b", "Charles Lutwidge")+
			df("400", "a", "Carroll", "b", "Lewis"))
	r.Type = "Authority"
	r.ID = "ark:/12148/cb118859183"
	a, ok := parseAuthority(r)
	if !ok || a.id != "11885918" || a.heading.displayName() != "Lewis Carroll" {
		t.Fatalf("authority = %+v, %v", a, ok)
	}
	if len(a.variants) != 1 || a.variants[0] != "Charles Lutwidge Dodgson" {
		t.Errorf("variants = %v, want the heading itself left out", a.variants)
	}
	r.Type = "Bibliographic"
	if _, ok := parseAuthority(r); ok {
		t.Error("a bibliographic record was read as an authority")
	}
}

func TestCQLTerm(t *testing.T) {
	cases := map[string]string{
		`alice pays merveilles`:                   `"alice pays merveilles"`,
		`x" or bib.title any "*`:                  `"x or bib.title any"`,
		`a\" or "b`:                               `"a or b"`,
		`wild* card? ^anchor`:                     `"wild card anchor"`,
		`(Le petit prince) = x < y > z / w`:       `"Le petit prince x y z w"`,
		`L'Outsider`:                              `"L'Outsider"`,
		`Pride and Prejudice not Zombies or prox`: `"Pride and Prejudice not Zombies or prox"`,
		"tab\tand\nnewline":                       `"tab and newline"`,
		`"*?^\"`:                                  ``,
		`   `:                                     ``,
	}
	for in, want := range cases {
		if got := cqlTerm(in); got != want {
			t.Errorf("cqlTerm(%q) = %q, want %q", in, got, want)
		}
	}
	// Control characters that strings.Fields does not split on (NUL, SOH,
	// DEL, the C1 non-sorting markers) are only removed by the replacement
	// itself. Vertical tab, form feed, NEL and the line separator are
	// whitespace to strings.Fields as well; they are here so a change to
	// either step is seen.
	controls := map[string]string{
		"nul\x00byte":           `"nul byte"`,
		"soh\x01del\x7fend":     `"soh del end"`,
		"\u0098Les \u009cmots":  `"Les mots"`,
		"vt\vff\fnel\u0085ls x": `"vt ff nel ls x"`,
	}
	for in, want := range controls {
		if got := cqlTerm(in); got != want {
			t.Errorf("cqlTerm(%q) = %q, want %q", in, got, want)
		}
	}
	// Decomposed input (what macOS hands back for a typed or pasted title)
	// becomes the composed query, the form BnF's records use.
	for in, want := range map[string]string{
		"Les Misérables":     "\"Les Misérables\"",
		"Œuvres complètes":   "\"Œuvres complètes\"",
		"Céline L'étranger": "\"Céline L'étranger\"",
	} {
		if got := cqlTerm(in); got != want {
			t.Errorf("cqlTerm(%q) = %q (%d bytes), want the NFC %q (%d bytes)", in, got, len(got), want, len(want))
		}
	}
	long := strings.Repeat("a", 500)
	if got := cqlTerm(long); len(got) != maxTermRunes+2 {
		t.Errorf("long term length = %d, want capped at %d", len(got), maxTermRunes+2)
	}
	// Whatever the input, the result holds exactly two quotes: one term.
	for in := range cases {
		if got := cqlTerm(in); got != "" && strings.Count(got, `"`) != 2 {
			t.Errorf("cqlTerm(%q) = %q is not a single quoted term", in, got)
		}
	}
}
