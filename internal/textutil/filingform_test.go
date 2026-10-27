package textutil

import "testing"

func TestSplitFilingForm(t *testing.T) {
	cases := []struct {
		in, base, article string
		ok                bool
	}{
		{"Trace of Death, A", "Trace of Death", "A", true},
		{"Hobbit, The", "Hobbit", "The", true},
		{"hobbit, the", "hobbit", "the", true},
		{"  Prozess, Der  ", "Prozess", "Der", true},
		{"Hobbit, The.", "Hobbit", "The", true},
		// Punctuation and diacritics around the article do not hide it: Review
		// duplicates always read the tail through the title fold (#1691), and
		// the shared rule must not narrow that.
		{"Hobbit, The!", "Hobbit", "The", true},
		{"Hobbit, The)", "Hobbit", "The", true},
		{"Hobbit, \"The\"", "Hobbit", "The", true},
		{"Hobbit, A\u2026", "Hobbit", "A", true},
		{"Hobbit,\u00a0The", "Hobbit", "The", true},
		{"Hobbit, \u00c9l", "Hobbit", "\u00c9l", true},
		{"Hobbit, T.he", "Hobbit, T.he", "", false},
		{"Rock, Paper, Scissors, A", "Rock, Paper, Scissors", "A", true},
		{"Love, Actually", "Love, Actually", "", false},
		{"Me, Myself and I", "Me, Myself and I", "", false},
		{"Dust, Ash, and Bone", "Dust, Ash, and Bone", "", false},
		{"Hello, A World", "Hello, A World", "", false},
		{"The Hobbit", "The Hobbit", "", false},
		{", A", ", A", "", false},
		{"Title,", "Title,", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		base, art, ok := SplitFilingForm(c.in)
		if base != c.base || art != c.article || ok != c.ok {
			t.Errorf("SplitFilingForm(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, base, art, ok, c.base, c.article, c.ok)
		}
	}
}

func TestRestoreFilingForm(t *testing.T) {
	for in, want := range map[string]string{
		"Trace of Death, A": "A Trace of Death",
		"Hobbit, The":       "The Hobbit",
		"The Hobbit":        "The Hobbit",
		"Love, Actually":    "Love, Actually",
	} {
		if got := RestoreFilingForm(in); got != want {
			t.Errorf("RestoreFilingForm(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsArticle(t *testing.T) {
	for _, w := range []string{"a", "An", "THE", "der", "Het", "los"} {
		if !IsArticle(w) {
			t.Errorf("IsArticle(%q) = false", w)
		}
	}
	for _, w := range []string{"", "theatre", "of", "and", "actually"} {
		if IsArticle(w) {
			t.Errorf("IsArticle(%q) = true", w)
		}
	}
}
