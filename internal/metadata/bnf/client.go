// Package bnf provides a read-only client for the Bibliothèque nationale de
// France (BnF) catalogue général, through its public SRU 1.2 service. No API
// key is required.
//
// Role: opt-in primary. Legal deposit puts nearly every book published in
// France in the BnF catalogue, under its French edition title, where
// OpenLibrary and Hardcover tend to carry a translated book under its
// original English title (#3144). It is wired only when selected as the
// primary provider, so an install that did not choose it never calls BnF.
//
// Endpoint: https://catalogue.bnf.fr/api/SRU (operations explain and
// searchRetrieve). Records are requested as UNIMARC in marcxchange
// (recordSchema=unimarcXchange): unlike BnF's Dublin Core, it gives the
// author's role as a relator code, the authority record behind each name, the
// original title of a translation and the title without its statement of
// responsibility. BnF changed its cataloguing model in June 2026 ("Noemi")
// and is migrating records to the end of 2026; records converted so far carry
// the role as text and no authority link, and parsing accepts both forms.
// Every field is optional: a missing or unknown one is left empty.
//
// Licence: BnF's bibliographic records are under the Licence Ouverte / Open
// Licence 2.0 (Etalab), which asks for credit to the source and the date. See
// docs/third-party-data.md. Bindery fetches no cover image from BnF, Gallica
// or data.bnf.fr, and never queries the Dewey classification authority
// records.
package bnf

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/isbnutil"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/metadata/providerhttp"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/textutil"
)

const (
	sruBase      = "https://catalogue.bnf.fr/api/SRU"
	providerName = "bnf"
	idPrefix     = "bnf:"
	authorPrefix = "bnf:author:"
	recordSchema = "unimarcXchange"

	// searchPageSize is the records asked for by a search: one request, as
	// the aggregator gives each provider a few seconds per search.
	searchPageSize = 30
	// authorSearchSize is the person records asked for by an author search.
	authorSearchSize = 20
	// maxNameLookups bounds the authority lookups linkUnlinked adds to one
	// search, at one request each.
	maxNameLookups = 2
	// worksPageSize and maxWorksPages bound an author's catalogue walk at
	// 1000 records in at most 10 requests. BnF's Explain allows up to 1000
	// records a request but publishes no limit on how often; 100 keeps a
	// page well under maxResponseBytes (a UNIMARC record is 2 to 8 KiB).
	worksPageSize = 100
	maxWorksPages = 10
	// recentPageSize and maxRecentPages bound the second, name based query
	// for records BnF has not yet linked to the author (see
	// GetAuthorWorksSnapshot).
	recentPageSize = 100
	maxRecentPages = 2
	// noemiCutover is when BnF started cataloguing in its new model. Records
	// created since may lack the authority link author2bib follows.
	noemiCutover = "20260601"

	// maxResponseBytes caps the body handed to the XML decoder. A body cut
	// off here fails to decode and is reported as an error, never read as a
	// short result.
	maxResponseBytes = 4 << 20
	// maxXMLDepth bounds element nesting in a response. A real SRU response
	// nests about eight deep; without a bound, a 4 MiB body of nested
	// elements grows the decoder's element stack to about 260 MiB.
	maxXMLDepth = 64
	// requestTimeout bounds one request, pacing wait included.
	requestTimeout = 20 * time.Second
)

// defaultGate holds every BnF request in the process while BnF is refusing
// (HTTP 429 or 503 with Retry-After), shared like defaultPacer.
var defaultGate = providerhttp.NewGate()

// Client implements metadata.Provider for the BnF catalogue.
type Client struct {
	http *http.Client
	// gate holds requests while BnF is refusing. Nil (a test Client) holds
	// each call on its own.
	gate *providerhttp.Gate
	// base is the SRU endpoint; empty means sruBase. Tests point it at a
	// fake server.
	base string
}

// New creates a BnF client. Every client shares one request pacer and one
// refusal gate, so the limits in pacer.go hold for the whole process.
func New() *Client {
	return &Client{
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: &pacedTransport{base: httpsec.DefaultProxyTransport(), pacer: defaultPacer},
		},
		gate: defaultGate,
	}
}

func (c *Client) Name() string { return providerName }

func (c *Client) ready() error {
	if c == nil || c.http == nil {
		return metadata.ErrProviderNotConfigured
	}
	return nil
}

// SearchAuthors finds people in the BnF authority file whose authorised
// name heading matches every word of query. Each is keyed by authority
// record, so two people sharing a name stay apart. A person who matches only
// through a variant (a pseudonym, a transliteration) is left out, so
// "carroll lewis" offers Lewis Carroll and not the singer Johnny Carroll,
// whose variant is "Carroll, Johnny Lewis".
func (c *Client) SearchAuthors(ctx context.Context, query string) ([]models.Author, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	term := cqlTerm(query)
	if term == "" {
		return nil, nil
	}
	words := strings.Fields(strings.Trim(term, `"`))
	auths, _, err := c.searchAuthorities(ctx, term, authorSearchSize)
	if err != nil {
		return nil, fmt.Errorf("bnf search authors: %w", err)
	}
	authors := []models.Author{}
	for _, a := range auths {
		if !nameMatches(a.heading, words) {
			continue
		}
		author := a.heading.toAuthor()
		author.AlternateNames = a.variants
		authors = append(authors, author)
	}
	return authors, nil
}

// SearchBooks searches every field of the book records (title, names,
// publisher) and folds the matching editions into works. A query that is
// nothing but an ISBN is answered by GetBookByISBN.
func (c *Client) SearchBooks(ctx context.Context, query string) ([]models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	bare := query
	if len(bare) > 5 && strings.EqualFold(bare[:5], "isbn:") {
		bare = strings.TrimSpace(bare[5:])
	}
	if isbn13, isbn10 := isbnutil.Extract(bare); bare != "" && isbnutil.Normalize(bare) == firstNonEmpty(isbn13, isbn10) {
		b, err := c.GetBookByISBN(ctx, bare)
		if err != nil || b == nil {
			return nil, err
		}
		return []models.Book{*b}, nil
	}
	term := cqlTerm(query)
	if term == "" {
		return nil, nil
	}
	page, err := c.searchRetrieve(ctx, "bib.anywhere all "+term+bookFilter, 1, searchPageSize)
	if err != nil {
		return nil, fmt.Errorf("bnf search books: %w", err)
	}
	return groupWorks(c.linkUnlinked(ctx, parseRecords(page)), ""), nil
}

// linkUnlinked gives author credits that carry no authority link one, by
// looking their exact name up in the authority file. BnF's records converted
// to its 2026 model, and its sparse publisher supplied records, name the
// author without a link, and a book whose author Bindery cannot identify
// cannot be added. A name is linked only when exactly one person record has
// that heading; a name shared by two people stays unlinked. Credits whose
// name is linked elsewhere in the same results are linked from there first
// (linkByName), and at most maxNameLookups names are looked up, so a search
// costs at most that many extra requests. Best effort: a failed lookup
// leaves the credit as it was.
func (c *Client) linkUnlinked(ctx context.Context, eds []edition) []edition {
	eds = linkByName(eds)
	resolved := make(map[string]string)
	var names []credit
	for _, ed := range eds {
		for _, cr := range ed.credits {
			if !cr.author || cr.authorityID != "" {
				continue
			}
			key := textutil.FoldForSearch(cr.sortName())
			if _, ok := resolved[key]; ok {
				continue
			}
			resolved[key] = ""
			names = append(names, cr)
		}
	}
	if len(names) == 0 {
		return eds
	}
	if len(names) > maxNameLookups {
		names = names[:maxNameLookups]
	}
	for _, name := range names {
		ids, err := c.namesakes(ctx, name)
		if err != nil || len(ids) != 1 {
			continue // no one, or two people with this heading: ambiguous
		}
		resolved[textutil.FoldForSearch(name.sortName())] = ids[0]
	}
	out := make([]edition, len(eds))
	for i, ed := range eds {
		out[i] = ed
		copied := false
		for j, cr := range ed.credits {
			if !cr.author || cr.authorityID != "" {
				continue
			}
			if id := resolved[textutil.FoldForSearch(cr.sortName())]; id != "" {
				if !copied {
					out[i].credits = append([]credit(nil), ed.credits...)
					copied = true
				}
				out[i].credits[j].authorityID = id
			}
		}
	}
	return out
}

// errTooManyNamesakes reports an authority lookup whose matches did not fit
// in one page, so a second person with the same heading may be among those
// not read.
var errTooManyNamesakes = errors.New("bnf: more people match this name than one lookup reads")

// namesakes returns the authority record numbers of every person whose
// heading is exactly name's, from one lookup. When the lookup matched more
// records than it read, a namesake could be among the rest, so it reports
// errTooManyNamesakes rather than an answer that may be short.
func (c *Client) namesakes(ctx context.Context, name credit) ([]string, error) {
	term := cqlTerm(name.surname + " " + name.forename)
	if term == "" {
		return nil, nil
	}
	auths, total, err := c.searchAuthorities(ctx, term, authorSearchSize)
	if err != nil {
		return nil, err
	}
	if total < 0 || total > authorSearchSize {
		return nil, errTooManyNamesakes
	}
	var ids []string
	for _, a := range auths {
		if sameName(a.heading, name) {
			ids = append(ids, a.id)
		}
	}
	return ids, nil
}

// searchAuthorities searches person authority records by name, and returns
// the number of records the search matched (-1 when BnF does not say). Only
// the "pep" (person) type is ever asked for: Bindery has no use for, and
// never queries, the Dewey or subject authority files.
func (c *Client) searchAuthorities(ctx context.Context, term string, maxRecords int) ([]authority, int, error) {
	page, err := c.searchRetrieve(ctx, "aut.accesspoint all "+term+` and aut.type any "pep"`, 1, maxRecords)
	if err != nil {
		return nil, 0, err
	}
	var out []authority
	for _, r := range page.Records {
		if a, ok := parseAuthority(r.Data.Record); ok {
			out = append(out, a)
		}
	}
	return out, page.total(), nil
}

// GetAuthor resolves "bnf:author:<authority record number>" through the BnF
// person authority file. (nil, nil) when there is no such person record.
func (c *Client) GetAuthor(ctx context.Context, foreignID string) (*models.Author, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	id, err := authorityIDFromForeignID(foreignID)
	if err != nil {
		return nil, err
	}
	auth, found, err := c.authority(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("bnf get author %s: %w", foreignID, err)
	}
	if !found {
		return nil, nil
	}
	a := auth.heading.toAuthor()
	// Bindery keeps only the variants that are another spelling of the same
	// name as aliases (LatinAliasBinds), so a pseudonym's real name is not
	// adopted as one.
	a.AlternateNames = auth.variants
	return &a, nil
}

// GetAuthorWorks returns the author's catalogue, folded into works.
func (c *Client) GetAuthorWorks(ctx context.Context, authorForeignID string) ([]models.Book, error) {
	books, _, err := c.GetAuthorWorksSnapshot(ctx, authorForeignID)
	return books, err
}

// GetAuthorWorksSnapshot is GetAuthorWorks plus whether the catalogue is
// complete, and it never is. The catalogue is the book records linked to the
// author's authority record (bib.author2bib), but records BnF created in its
// new model since June 2026 are not linked yet: they are found by a second,
// name based query limited to records created since then, and a name is not
// proof. So catalogue reconciliation must not read a missing work as
// removed, and the snapshot says so. The walk is bounded by maxWorksPages
// and maxRecentPages.
//
// A failed lookup is an error, never an empty catalogue, and an authority
// record that no longer exists returns nothing rather than an empty complete
// catalogue (BnF merges duplicate records).
func (c *Client) GetAuthorWorksSnapshot(ctx context.Context, authorForeignID string) ([]models.Book, bool, error) {
	if err := c.ready(); err != nil {
		return nil, false, err
	}
	id, err := authorityIDFromForeignID(authorForeignID)
	if err != nil {
		return nil, false, err
	}
	auth, found, err := c.authority(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("bnf get author works %s: %w", authorForeignID, err)
	}
	if !found {
		return nil, false, nil
	}
	linked, err := c.searchAll(ctx, `bib.author2bib any "`+id+`"`+bookFilter, worksPageSize, maxWorksPages)
	if err != nil {
		return nil, false, fmt.Errorf("bnf get author works %s: %w", authorForeignID, err)
	}
	eds := parseRecords(linked...)

	// Records not yet linked to the authority record: the same name, created
	// since the cutover. Best effort: a failure here leaves the linked
	// catalogue, which is already marked incomplete.
	if term := cqlTerm(auth.heading.surname + " " + auth.heading.forename); term != "" {
		q := "bib.author all " + term + bookFilter + ` and bib.creationdate >= "` + noemiCutover + `"`
		if recent, err := c.searchAll(ctx, q, recentPageSize, maxRecentPages); err == nil {
			if adopted := adoptUnlinked(parseRecords(recent...), auth.heading); len(adopted) > 0 && c.soleHolder(ctx, auth.heading) {
				eds = append(eds, adopted...)
			}
		}
	}
	return groupWorks(dedupeEditions(eds), id), false, nil
}

// soleHolder reports whether heading's person is the only one in the
// authority file with that exact heading. A name is not an identity: when a
// namesake exists (two people called "Martin, Jean"), an unlinked credit
// could be either, so nothing is adopted. One lookup, made only when there
// is something to adopt; a failed or inconclusive lookup adopts nothing.
func (c *Client) soleHolder(ctx context.Context, heading credit) bool {
	ids, err := c.namesakes(ctx, heading)
	return err == nil && len(ids) == 1 && ids[0] == heading.authorityID
}

// adoptUnlinked keeps the editions crediting, without an authority link, an
// author whose name is exactly the authority heading's, and links that credit
// to it. Editions already linked to someone are left to author2bib. The
// caller checks that no namesake shares the heading (soleHolder).
func adoptUnlinked(eds []edition, heading credit) []edition {
	var out []edition
	for _, ed := range eds {
		match := -1
		for i, cr := range ed.credits {
			if cr.author && cr.authorityID == "" && sameName(cr, heading) {
				match = i
				break
			}
		}
		if match < 0 {
			continue
		}
		ed.credits = append([]credit(nil), ed.credits...)
		ed.credits[match].authorityID = heading.authorityID
		out = append(out, ed)
	}
	return out
}

func dedupeEditions(eds []edition) []edition {
	seen := make(map[string]bool, len(eds))
	out := eds[:0:0]
	for _, ed := range eds {
		if seen[ed.ark] {
			continue
		}
		seen[ed.ark] = true
		out = append(out, ed)
	}
	return out
}

// GetBook fetches the record "bnf:<ARK name>" and returns the work it belongs
// to: the record plus the other editions of the same author and title, found
// with one more search. When that search fails or the record has no linked
// author, the record alone is returned.
func (c *Client) GetBook(ctx context.Context, foreignID string) (*models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	ark, err := arkFromForeignID(foreignID)
	if err != nil {
		return nil, err
	}
	page, err := c.searchRetrieve(ctx, `bib.persistentid all "ark:/12148/`+ark+`"`, 1, 2)
	if err != nil {
		return nil, fmt.Errorf("bnf get book %s: %w", foreignID, err)
	}
	eds := c.linkUnlinked(ctx, parseRecords(page))
	if len(eds) == 0 {
		return nil, nil
	}
	ed := eds[0]
	if b := c.workOf(ctx, ed); b != nil {
		b.ForeignID = foreignID
		return b, nil
	}
	books := groupWorks(eds[:1], "")
	if len(books) == 0 {
		return nil, nil
	}
	// Keep the requested ID: BnF redirects a merged record's ARK, and
	// callers look the book up by the ID they asked for.
	books[0].ForeignID = foreignID
	return &books[0], nil
}

// workOf finds the other editions of ed's work: the records linked to the
// same author whose title matches. Best effort; nil when it cannot.
func (c *Client) workOf(ctx context.Context, ed edition) *models.Book {
	a := primaryAuthor(ed, "")
	if a == nil || a.authorityID == "" {
		return nil
	}
	term := cqlTerm(ed.title)
	if term == "" {
		return nil
	}
	q := `bib.author2bib any "` + a.authorityID + `" and bib.title all ` + term + bookFilter
	page, err := c.searchRetrieve(ctx, q, 1, worksPageSize)
	if err != nil {
		return nil
	}
	books := groupWorks(dedupeEditions(append([]edition{ed}, parseRecords(page)...)), a.authorityID)
	want := idPrefix + ed.ark
	for i := range books {
		for _, e := range books[i].Editions {
			if e.ForeignID == want {
				return &books[i]
			}
		}
	}
	return nil
}

// GetEditions returns the editions of the work bookForeignID belongs to, as
// GetBook assembles it.
func (c *Client) GetEditions(ctx context.Context, bookForeignID string) ([]models.Edition, error) {
	b, err := c.GetBook(ctx, bookForeignID)
	if err != nil || b == nil {
		return nil, err
	}
	return b.Editions, nil
}

// GetBookByISBN looks a book up by ISBN-10, ISBN-13 or EAN (bib.fuzzyIsbn
// matches any of them).
func (c *Client) GetBookByISBN(ctx context.Context, isbn string) (*models.Book, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	isbn13, isbn10 := isbnutil.Extract(isbn)
	want := firstNonEmpty(isbn13, isbn10)
	if want == "" {
		return nil, nil
	}
	page, err := c.searchRetrieve(ctx, `bib.fuzzyIsbn all "`+want+`"`+bookFilter, 1, 5)
	if err != nil {
		return nil, fmt.Errorf("bnf get book by ISBN: %w", err)
	}
	books := groupWorks(c.linkUnlinked(ctx, parseRecords(page)), "")
	if len(books) == 0 {
		return nil, nil
	}
	return &books[0], nil
}

// authority fetches a person authority record. found is false when BnF has
// no such person record.
func (c *Client) authority(ctx context.Context, id string) (authority, bool, error) {
	page, err := c.searchRetrieve(ctx, `aut.recordid all "`+id+`" and aut.type any "pep"`, 1, 2)
	if err != nil {
		return authority{}, false, err
	}
	for _, r := range page.Records {
		if a, ok := parseAuthority(r.Data.Record); ok && a.id == id {
			return a, true, nil
		}
	}
	return authority{}, false, nil
}

// searchAll pages through a query, at most maxPages pages of pageSize. It
// stops at the end of the results, at an empty page, or at the page cap,
// whichever comes first, so a server whose cursor never ends costs at most
// maxPages requests and pageSize*maxPages records.
func (c *Client) searchAll(ctx context.Context, cql string, pageSize, maxPages int) ([]*sruResponse, error) {
	var pages []*sruResponse
	start, seen := 1, 0
	for p := 0; p < maxPages; p++ {
		page, err := c.searchRetrieve(ctx, cql, start, pageSize)
		if err != nil {
			return nil, err
		}
		pages = append(pages, page)
		n := len(page.Records)
		seen += n
		total := page.total()
		if n == 0 || (total >= 0 && start+n > total) || seen >= pageSize*maxPages {
			break
		}
		start += n
	}
	return pages, nil
}

// searchRetrieve runs one SRU searchRetrieve request. A response with zero
// records is an empty result; an SRU diagnostic is an error.
func (c *Client) searchRetrieve(ctx context.Context, cql string, start, maxRecords int) (*sruResponse, error) {
	params := url.Values{
		"version":        {"1.2"},
		"operation":      {"searchRetrieve"},
		"query":          {cql},
		"recordSchema":   {recordSchema},
		"maximumRecords": {strconv.Itoa(maxRecords)},
		"startRecord":    {strconv.Itoa(start)},
	}
	base := c.base
	if base == "" {
		base = sruBase
	}
	var out sruResponse
	if err := c.get(ctx, base+"?"+params.Encode(), &out); err != nil {
		return nil, err
	}
	if len(out.Diagnostics) > 0 {
		d := out.Diagnostics[0]
		return nil, fmt.Errorf("bnf SRU diagnostic %s: %s %s", d.URI, d.Message, d.Details)
	}
	if len(out.Records) > maxRecords {
		out.Records = out.Records[:maxRecords]
	}
	return &out, nil
}

// get GETs endpoint and decodes the size-limited body into out.
//
// The shared provider request loop retries a 429, 502, 503 or 504 with
// Retry-After (capped) or exponential backoff with jitter, holds every BnF
// request while one is refused, and never retries another 4xx. What outlives
// the retries is marked for the aggregator and scheduled discovery: a 429 or
// a local pacing refusal as providererr.ErrRateLimited, a 5xx or a network
// failure as providererr.ErrUnavailable.
func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	resp, err := providerhttp.Do(ctx, c.http, c.gate, providerhttp.Request{
		Provider: providerName,
		URL:      endpoint,
		Header:   http.Header{"Accept": {"application/xml, text/xml"}},
	})
	if err != nil {
		return classify(err)
	}
	defer resp.Body.Close()
	if err := decodeXML(io.LimitReader(resp.Body, maxResponseBytes), out); err != nil {
		return fmt.Errorf("decode SRU response: %w", err)
	}
	return nil
}

func classify(err error) error {
	var statusErr *providerhttp.StatusError
	var heldErr *providerhttp.HeldError
	switch {
	case errors.Is(err, errPaced):
		return fmt.Errorf("%w: %w", providererr.ErrRateLimited, err)
	case errors.As(err, &statusErr), errors.As(err, &heldErr), errors.Is(err, context.Canceled):
		return err
	default:
		return fmt.Errorf("%w: %w", providererr.ErrUnavailable, err)
	}
}

// errTooDeep reports a response nested deeper than maxXMLDepth.
var errTooDeep = errors.New("bnf: response elements nest deeper than any SRU response")

// decodeXML decodes r with element nesting bounded by maxXMLDepth, so a
// hostile or broken body is refused before the decoder's element stack grows
// with it. (encoding/xml skips a leading byte order mark itself.)
func decodeXML(r io.Reader, out any) error {
	return xml.NewTokenDecoder(&depthLimiter{d: xml.NewDecoder(r), max: maxXMLDepth}).Decode(out)
}

// depthLimiter passes tokens through and fails once elements nest deeper
// than max.
type depthLimiter struct {
	d     *xml.Decoder
	depth int
	max   int
}

func (l *depthLimiter) Token() (xml.Token, error) {
	tok, err := l.d.Token()
	switch tok.(type) {
	case xml.StartElement:
		l.depth++
		if l.depth > l.max {
			return nil, errTooDeep
		}
	case xml.EndElement:
		l.depth--
	}
	return tok, err
}

func parseRecords(pages ...*sruResponse) []edition {
	var out []edition
	for _, p := range pages {
		if p == nil {
			continue
		}
		for _, r := range p.Records {
			if ed, ok := parseRecord(r.Data.Record); ok {
				out = append(out, ed)
			}
		}
	}
	return out
}

func authorityIDFromForeignID(foreignID string) (string, error) {
	trimmed := strings.TrimSpace(foreignID)
	id := strings.TrimPrefix(trimmed, authorPrefix)
	if id == trimmed || !authorityIDRe.MatchString(id) {
		return "", errors.New("bnf: not an author id: " + strconv.Quote(foreignID))
	}
	return id, nil
}

func arkFromForeignID(foreignID string) (string, error) {
	trimmed := strings.TrimSpace(foreignID)
	ark := strings.TrimPrefix(trimmed, idPrefix)
	if ark == trimmed || !arkNameRe.MatchString(ark) {
		return "", errors.New("bnf: not a book id: " + strconv.Quote(foreignID))
	}
	return ark, nil
}

// nameMatches reports whether every query word appears in the credit's name,
// ignoring case and diacritics.
func nameMatches(c credit, words []string) bool {
	folded := textutil.FoldForSearch(c.sortName() + " " + c.displayName())
	for _, w := range words {
		if !strings.Contains(folded, textutil.FoldForSearch(w)) {
			return false
		}
	}
	return true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
