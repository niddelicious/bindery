package metadata

import (
	"context"
	"sync"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type mockProvider struct {
	// mu guards only the call-recording fields below (searchAuthorQueries,
	// searchBookQueries, getBookCalls, gotBookIDs, getByISBNCalls, gotISBNs).
	//
	// The aggregator used to call every provider method from one goroutine, so
	// the recorders needed no synchronisation. #1888 gave the author-works
	// enrichment passes a bounded fan-out, and enrichBook reaches SearchBooks
	// from each worker — so the bare `append` at SearchBooks became a genuine
	// data race, caught by the `race (non-api)` shard.
	//
	// Only the writers lock. Every assertion reads these fields from the test
	// body after the pass has returned, and concurrency.RunBounded waits on its
	// WaitGroup before returning, which establishes the happens-before those
	// reads need. That keeps the ~110 existing read sites untouched.
	//
	// The configuration fields are written once during setup and only read
	// afterwards, so they stay unguarded.
	mu                   sync.Mutex
	name                 string
	searchBooks          []models.Book
	searchBooksByQuery   map[string][]models.Book
	searchBookErr        error
	searchAuthors        []models.Author
	searchAuthorsByQuery map[string][]models.Author
	searchAuthErr        error
	searchAuthorQueries  []string
	getAuthor            *models.Author
	getAuthorErr         error
	getBook              *models.Book
	getBookByID          map[string]*models.Book
	getBookErr           error
	getBookCalls         int
	gotBookIDs           []string
	getEditions          []models.Edition
	getEditionsErr       error
	getByISBN            *models.Book
	getByISBNByISBN      map[string]*models.Book
	getByISBNErr         error
	getByISBNCalls       int
	gotISBNs             []string
	searchBookQueries    []string
	// authorWorks implements worksProvider interface
	authorWorks    []models.Book
	authorWorksErr error
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) SearchAuthors(_ context.Context, query string) ([]models.Author, error) {
	m.mu.Lock()
	m.searchAuthorQueries = append(m.searchAuthorQueries, query)
	m.mu.Unlock()
	if m.searchAuthorsByQuery != nil {
		if authors, ok := m.searchAuthorsByQuery[query]; ok {
			return authors, m.searchAuthErr
		}
	}
	return m.searchAuthors, m.searchAuthErr
}
func (m *mockProvider) SearchBooks(_ context.Context, query string) ([]models.Book, error) {
	m.mu.Lock()
	m.searchBookQueries = append(m.searchBookQueries, query)
	m.mu.Unlock()
	if m.searchBooksByQuery != nil {
		if books, ok := m.searchBooksByQuery[query]; ok {
			return books, m.searchBookErr
		}
	}
	return m.searchBooks, m.searchBookErr
}
func (m *mockProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return m.getAuthor, m.getAuthorErr
}
func (m *mockProvider) GetBook(_ context.Context, foreignID string) (*models.Book, error) {
	m.mu.Lock()
	m.getBookCalls++
	m.gotBookIDs = append(m.gotBookIDs, foreignID)
	m.mu.Unlock()
	if m.getBookByID != nil {
		return m.getBookByID[foreignID], m.getBookErr
	}
	return m.getBook, m.getBookErr
}
func (m *mockProvider) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	return m.getEditions, m.getEditionsErr
}
func (m *mockProvider) GetBookByISBN(_ context.Context, isbn string) (*models.Book, error) {
	m.mu.Lock()
	m.getByISBNCalls++
	m.gotISBNs = append(m.gotISBNs, isbn)
	m.mu.Unlock()
	if m.getByISBNByISBN != nil {
		return m.getByISBNByISBN[isbn], m.getByISBNErr
	}
	return m.getByISBN, m.getByISBNErr
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalCanonicalQueries(a, b []primaryBookCanonicalQuery) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// worksProvider implementation (optional, only attached when needed).
type mockWorksProvider struct {
	mockProvider
	authorWorksCalls int
}

func (m *mockWorksProvider) GetAuthorWorks(_ context.Context, _ string) ([]models.Book, error) {
	m.authorWorksCalls++
	return m.authorWorks, m.authorWorksErr
}

type mockAuthorWorksByNameProvider struct {
	mockProvider
	authorWorksByName    []models.Book
	authorWorksByNameErr error
	gotAuthorName        string
	calls                int
}

func (m *mockAuthorWorksByNameProvider) GetAuthorWorksByName(_ context.Context, authorName string) ([]models.Book, error) {
	m.calls++
	m.gotAuthorName = authorName
	return m.authorWorksByName, m.authorWorksByNameErr
}

func newTestAggregator(primary Provider, enrichers ...Provider) *aggregatorCore {
	return &aggregatorCore{requests: &requestCache{},
		primary:   primary,
		enrichers: enrichers,
		cache:     newTTLCache(time.Minute),
	}
}

// WithAudnexClient on a bare core is the test-only twin of the Aggregator
// method, for fixtures built with newTestAggregator before anything reads them.
func (a *aggregatorCore) WithAudnexClient(client AudnexBookClient) *aggregatorCore {
	a.audnex = client
	return a
}

// core returns the provider set an Aggregator is serving right now, so a test
// can reach the unexported lookup helpers that run on it.
func (a *Aggregator) core() *aggregatorCore { return a.load() }
