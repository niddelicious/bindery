package textutil

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

// TestFoldForKeyword pins the one way the keyword fold differs from
// FoldForTitleMatch (#3123): a real umlaut survives, and everything else is
// folded identically.
func TestFoldForKeyword(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"keeps umlauts", "Die Höhle", "die höhle"},
		{"keeps all three", "Äpfel Öl Über", "äpfel öl über"},
		{"typed digraph stays a digraph", "Die Hoehle", "die hoehle"},
		{"eszett still expands", "Straße", "strasse"},
		{"composes decomposed input", norm.NFD.String("Müller"), "müller"},
		{"deletes apostrophes", "Ender's Game", "enders game"},
		{"collapses punctuation", "Guards! Guards!", "guards guards"},
		{"lowercases uppercase umlauts", "ÄÖÜ", "äöü"},
	}
	for _, tc := range cases {
		if got := FoldForKeyword(tc.in); got != tc.want {
			t.Errorf("%s: FoldForKeyword(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestFoldForKeywordDiffersOnlyByUmlauts: expanding the umlauts of a keyword
// gives back exactly the release alphabet, so the two folds cannot drift.
func TestFoldForKeywordDiffersOnlyByUmlauts(t *testing.T) {
	for _, in := range []string{"Müller", "Der Prozeß", "Fräulein Smillas Gespür für Schnee", "Ender's Game", "Nesbø", "Åsa Larsson", "刘慈欣", "Michael Samuel Joel"} {
		if got, want := TransliterateUmlauts(FoldForKeyword(in)), FoldForTitleMatch(in); got != want {
			t.Errorf("TransliterateUmlauts(FoldForKeyword(%q)) = %q, want FoldForTitleMatch = %q", in, got, want)
		}
	}
}
