package normdrift

import (
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/indexer/newznab"
)

// TestDigraphFlexNeedsARealUmlaut is the #3123 regression. The keyword side
// let every ae, oe and ue match the compact spelling, so the first name
// "Michael" accepted a release naming "Michal". The flex belongs to a digraph
// that came from an ä, ö or ü; one typed in ASCII is exactly what it says.
//
// It asserts through the public producers (SigWords on the keyword side,
// NormalizeRelease on the release side), so it holds whatever representation
// the keyword carries, and it runs both Unicode forms of source and release.
func TestDigraphFlexNeedsARealUmlaut(t *testing.T) {
	cases := []struct {
		source, release string
		want            bool
	}{
		{"Michael", "Michal", false},
		{"Samuel", "Samul", false},
		{"Raphael", "Raphal", false},
		{"Joel", "Jol", false},
		{"Noel", "Nol", false},
		{"Manuel", "Manul", false},
		{"Ruel", "Rul", false},
		{"Michael", "Michael", true},
		{"Müller", "Muller", true},
		{"Müller", "Mueller", true},
		{"Müller", "Müller", true},
		{"Schönberg", "Schonberg", true},
		{"Schönberg", "Schoenberg", true},
		{"Björn", "Bjorn", true},
		{"Björn", "Bjoern", true},
		{"Mueller", "Mueller", true},
		{"Mueller", "Müller", true},
		{"Mueller", "Muller", false},
	}
	forms := []struct {
		name string
		f    norm.Form
	}{{"NFC", norm.NFC}, {"NFD", norm.NFD}}
	for _, tc := range cases {
		for _, form := range forms {
			src := form.f.String(tc.source)
			hay := indexer.NormalizeRelease(form.f.String(tc.release))
			kws := newznab.SigWords(src)
			if len(kws) != 1 {
				t.Fatalf("%s: SigWords(%q) = %v, want one keyword", form.name, src, kws)
			}
			if got := indexer.WordBoundaryRegex(kws[0]).MatchString(hay); got != tc.want {
				t.Errorf("%s: keyword %q from %q against release %q: got %v, want %v",
					form.name, kws[0], tc.source, tc.release, got, tc.want)
			}
			if got := indexer.ContainsPhrase(hay, kws); got != tc.want {
				t.Errorf("%s: phrase %v from %q against release %q: got %v, want %v",
					form.name, kws, tc.source, tc.release, got, tc.want)
			}
		}
	}
}
