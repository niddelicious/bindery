package calibre

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

var openLibraryEditionIDRe = regexp.MustCompile(`^OL\d+M$`)

// IdentifiersForBook returns the Calibre identifier map that can be derived
// from the local book row and, when available, the specific edition being
// handed off. It intentionally does not perform provider lookups or backfills.
func IdentifiersForBook(book *models.Book, edition *models.Edition) map[string]string {
	if book == nil {
		return nil
	}
	out := map[string]string{}
	setIdentifier(out, "bindery", strconv.FormatInt(book.ID, 10))
	addProviderIdentifier(out, book.MetadataProvider, book.ForeignID)
	setIdentifier(out, "asin", book.ASIN)

	if edition != nil {
		setIdentifier(out, "isbn", firstIdentifierValue(edition.ISBN13, edition.ISBN10))
		if edition.ASIN != nil {
			setIdentifier(out, "asin", *edition.ASIN)
		}
		if id := openLibraryEditionIdentifier(edition.ForeignID); id != "" {
			setIdentifier(out, "openlibrary_edition", id)
		}
	}
	return out
}

func addProviderIdentifier(out map[string]string, provider, foreignID string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	foreignID = strings.TrimSpace(foreignID)
	if provider == "" || foreignID == "" {
		return
	}

	typ := provider
	val := foreignID
	switch provider {
	case "googlebooks", "google":
		typ = "google"
		val = trimIdentifierPrefix(val, "gb:")
	case "dnb":
		val = trimIdentifierPrefix(val, "dnb:")
	case "bnf":
		val = trimIdentifierPrefix(val, "bnf:")
	case "hardcover":
		val = trimIdentifierPrefix(val, "hc:")
	case "openlibrary":
		val = normalizeOpenLibraryIdentifier(val)
	}
	setIdentifier(out, typ, val)
}

func setIdentifier(out map[string]string, typ, val string) {
	typ = cleanIdentifierType(typ)
	val = cleanIdentifierValue(val)
	if typ == "" || val == "" {
		return
	}
	out[typ] = val
}

func firstIdentifierValue(values ...*string) string {
	for _, v := range values {
		if v != nil && strings.TrimSpace(*v) != "" {
			return strings.TrimSpace(*v)
		}
	}
	return ""
}

func openLibraryEditionIdentifier(foreignID string) string {
	id := normalizeOpenLibraryIdentifier(foreignID)
	if openLibraryEditionIDRe.MatchString(id) {
		return id
	}
	return ""
}

func normalizeOpenLibraryIdentifier(value string) string {
	value = strings.TrimSpace(value)
	value = trimIdentifierPrefix(value, "openlibrary:")
	value = trimIdentifierPrefix(value, "/works/")
	value = trimIdentifierPrefix(value, "/books/")
	return value
}

func trimIdentifierPrefix(value, prefix string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix)) {
		return strings.TrimSpace(value[len(prefix):])
	}
	return value
}
