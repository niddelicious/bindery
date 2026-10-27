package bnf

import (
	"context"
	"net/url"
	"testing"
)

// Records BnF converted from its older catalogues hold the whole title
// statement in 200 $a, give the original title in brackets or only as a
// uniform title (500), and credit adaptations with relator 100.
func TestOlderRecords(t *testing.T) {
	fs := newFixtureServer(t, func(url.Values) string { return "older_records_alice.xml" })
	page, err := testClient(fs.URL).searchRetrieve(context.Background(), "x", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	eds := parseRecords(page)
	if len(eds) != 3 {
		t.Fatalf("editions = %d, want 3", len(eds))
	}
	byARK := map[string]edition{}
	for _, ed := range eds {
		byARK[ed.ark] = ed
	}

	statement := byARK["cb32941384z"]
	if statement.title != "Alice au pays des merveilles" {
		t.Errorf("title = %q, want the statement of responsibility cut off", statement.title)
	}
	if statement.originalTitle != "Alice's adventures in wonderland" {
		t.Errorf("original = %q, want the bracketed original", statement.originalTitle)
	}
	if a := primaryAuthor(statement, ""); a == nil || a.displayName() != "Lewis Carroll" {
		t.Errorf("author = %+v", a)
	}

	adaptation := byARK["cb31911075d"]
	if adaptation.originalTitle != "Alice's adventures in Wonderland" {
		t.Errorf("original = %q, want the uniform title", adaptation.originalTitle)
	}
	if a := primaryAuthor(adaptation, ""); a != nil {
		t.Errorf("author = %+v, want none: relator 100 is the author adapted from", a)
	}

	// Relator 330 is a dubious attribution, not the author.
	if a := primaryAuthor(byARK["cb34614049g"], ""); a != nil {
		t.Errorf("author = %+v, want none for relator 330", a)
	}
}

func TestSplitTitleStatement(t *testing.T) {
	cases := []struct{ in, title, original string }{
		{"L'outsider", "L'outsider", ""},
		{`Alice au pays des merveilles ["Alice's adventures in wonderland"]. Traduction de Henriette Rouillard`, "Alice au pays des merveilles", "Alice's adventures in wonderland"},
		{"Alice au pays des merveilles. Traduit par Françoise Durane", "Alice au pays des merveilles", ""},
		{"Alice au pays des merveilles , par Lewis Carroll. Adaptation Gisèle Vallerey", "Alice au pays des merveilles", ""},
		{"Alice au Pays des Merveilles , d'après Lewis Carroll. Par Walt Disney", "Alice au Pays des Merveilles", ""},
		{"Le Petit prince / Antoine de Saint-Exupéry", "Le Petit prince", ""},
		{"Vingt mille lieues sous les mers", "Vingt mille lieues sous les mers", ""},
		{"M. Ripois et la Némésis", "M. Ripois et la Némésis", ""},
	}
	for _, tc := range cases {
		title, original := splitTitleStatement(tc.in, nil)
		if title != tc.title || original != tc.original {
			t.Errorf("splitTitleStatement(%q) = %q, %q; want %q, %q", tc.in, title, original, tc.title, tc.original)
		}
	}
}

// English and French ", by" / ", par" statements of responsibility are cut
// only when what follows names one of the record's people or is a plain
// responsibility cue, so a title that merely contains ", by" keeps it. The
// first four inputs are 200 $a values from real BnF records.
func TestSplitTitleStatement_ResponsibilityAfterComma(t *testing.T) {
	carroll := []string{"Carroll"}
	cases := []struct {
		name, in string
		names    []string
		title    string
		original string
	}{
		{"by the record's author", "Sylvie and Bruno, by Lewis Carroll", carroll, "Sylvie and Bruno", ""},
		{"by, then an illustrator statement", "Phantasmagoria and other poems, by Lewis Carroll. With illustrations by Arthur B. Frost", carroll, "Phantasmagoria and other poems", ""},
		{"question marks and spacing kept", "Rhyme ? and reason ?, by Lewis Carroll", carroll, "Rhyme ? and reason ?", ""},
		{"leading name flagged sic", `L. Carrol ["sic"]. Alice au pays des merveilles . Illustrations de J. Sornani...`, carroll, "Alice au pays des merveilles", ""},
		{"par and the record's author", "Le Rouge et le Noir, par Stendhal", []string{"Stendhal"}, "Le Rouge et le Noir", ""},
		{"case insensitive", "Sylvie and Bruno, BY LEWIS CARROLL", carroll, "Sylvie and Bruno", ""},
		{"responsibility cue without a name", "Sylvie and Bruno concluded, by the author of Alice in Wonderland", nil, "Sylvie and Bruno concluded", ""},
		{"title containing , by", "Ruby, by Moonlight", carroll, "Ruby, by Moonlight", ""},
		{"a name inside a word is not the name", "Ruby, by Moonlight", []string{"Moon"}, "Ruby, by Moonlight", ""},
		{"no people on the record", "Sylvie and Bruno, by Lewis Carroll", nil, "Sylvie and Bruno, by Lewis Carroll", ""},
		{"sic is never an original title", `Ce que j'ai vu ["sic"]. Souvenirs`, []string{"Dupont"}, "Ce que j'ai vu", ""},
		{"sic alongside a real original", `Alice au pays des merveilles [sic] ["Alice's adventures in wonderland"]`, carroll, "Alice au pays des merveilles", "Alice's adventures in wonderland"},
	}
	for _, tc := range cases {
		title, original := splitTitleStatement(tc.in, tc.names)
		if title != tc.title || original != tc.original {
			t.Errorf("%s: splitTitleStatement(%q) = %q, %q; want %q, %q", tc.name, tc.in, title, original, tc.title, tc.original)
		}
	}
}

// The record the review found: its title became "L. Carrol" and its original
// title "sic".
func TestParseRecord_SicStatement(t *testing.T) {
	ed, ok := parseRecord(record(t, bookLeader,
		df("200", "a", `L. Carrol ["sic"]. Alice au pays des merveilles . Illustrations de J. Sornani...`, "b", "Texte imprimé")+
			df("700", "3", "11885918", "a", "Carroll", "b", "Lewis", "4", "070")))
	if !ok {
		t.Fatal("not parsed")
	}
	if ed.title != "Alice au pays des merveilles" || ed.originalTitle != "" {
		t.Errorf("title = %q original = %q, want Alice au pays des merveilles and no original", ed.title, ed.originalTitle)
	}
}
