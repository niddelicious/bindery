package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// blockingWorksProvider holds the catalogue fetch until release is closed,
// as a large catalogue does for minutes.
type blockingWorksProvider struct {
	*worksCountingProvider
	release chan struct{}
}

func (p *blockingWorksProvider) GetAuthorWorks(ctx context.Context, id string) ([]models.Book, error) {
	<-p.release
	return p.worksCountingProvider.GetAuthorWorks(ctx, id)
}

// The author page opened straight after an add must see the sync the add
// started (#3058). The add claims it before answering, so GET /author/{id}
// reports syncInProgress however late the sync's goroutine gets going, and
// the claim is given back when the sync ends.
func TestCreateAuthor_SyncInProgressBeforeAnswering(t *testing.T) {
	for round := 0; round < 20; round++ {
		provider := &blockingWorksProvider{worksCountingProvider: wellsCreateProvider(), release: make(chan struct{})}
		f := newCoreFixture(t, provider)

		body, _ := json.Marshal(map[string]any{
			"foreignAuthorId": wellsCreateParams.ForeignID,
			"authorName":      wellsCreateParams.Name,
			"monitored":       true,
		})
		rec := httptest.NewRecorder()
		f.h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", bytes.NewReader(body)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
		}
		var created models.Author
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}

		if !f.h.runningSyncs.running(created.ID) {
			close(provider.release)
			t.Fatalf("round %d: the add answered before its catalogue sync was counted", round)
		}
		id := strconv.FormatInt(created.ID, 10)
		get := httptest.NewRecorder()
		f.h.Get(get, withURLParam(httptest.NewRequest(http.MethodGet, "/api/v1/author/"+id, nil), "id", id))
		var got models.Author
		if err := json.Unmarshal(get.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.SyncInProgress {
			t.Errorf("round %d: GET /author/%s syncInProgress = false while the sync is fetching", round, id)
		}

		close(provider.release)
		f.drain(t)
		if f.h.runningSyncs.running(created.ID) {
			t.Fatalf("round %d: the add's sync claim was not given back when the sync ended", round)
		}
	}
}
