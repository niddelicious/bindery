package metadata

import (
	"context"
	"errors"
	"net"
	"net/url"

	"github.com/vavallee/bindery/internal/metadata/providererr"
	"github.com/vavallee/bindery/internal/models"
)

// ErrRateLimited matches, through errors.Is, any provider refusal for rate
// limit reasons, whichever provider refused. See package providererr.
var ErrRateLimited = providererr.ErrRateLimited

// ErrUnavailable matches a provider server error that outlived its retries.
// See package providererr.
var ErrUnavailable = providererr.ErrUnavailable

// IsProviderUnavailable reports whether err says the metadata provider, not
// the request, is the problem: a rate limit, a server error, a network
// failure or a timeout. A not found, a parse error or any other error about
// one record is not. Scheduled discovery counts only these toward its failure
// breaker (#2236), so a few permanently broken authors cannot stop the pass.
func IsProviderUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// deferCoverEnrichmentKey is the context key WithDeferredCoverEnrichment sets.
type deferCoverEnrichmentKey struct{}

// WithDeferredCoverEnrichment marks ctx so GetAuthorWorksForAuthor skips its
// per work cover enrichment and leaves it to the caller (#2236).
//
// That enrichment is one enricher round trip, plus an edition sample, for
// every coverless work the author has, and #2578 measured it in the thousands
// for a prolific author. Most of those works are already in the library, and
// the only thing the enrichment gives them is a cover for a book that has none
// (the sync backfills an empty cover, #1748). The scheduled discovery job
// trades that away: it enriches only the works it may create, through
// EnrichMissingCovers, and leaves covers for existing coverless books to a
// manual refresh, which still enriches everything.
//
// A list fetched this way is never cached, because the cached catalogue is
// served to callers that expect covers. A cached, enriched list is still
// served as it is.
func WithDeferredCoverEnrichment(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferCoverEnrichmentKey{}, true)
}

func coverEnrichmentDeferred(ctx context.Context) bool {
	v, _ := ctx.Value(deferCoverEnrichmentKey{}).(bool)
	return v
}

// EnrichMissingCovers runs the cover enrichment GetAuthorWorksForAuthor would
// have run, over books only. It fills ImageURL in place on books that have
// none.
func (a *aggregatorCore) EnrichMissingCovers(ctx context.Context, books []models.Book) {
	if a == nil || len(books) == 0 {
		return
	}
	a.enrichMissingAuthorWorkCovers(ctx, books)
}
