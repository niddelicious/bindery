package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// pngBytes is enough of a PNG for content sniffing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func coverFixture(t *testing.T) (*BookHandler, *models.Book, func(body []byte) *httptest.ResponseRecorder) {
	t.Helper()
	h, books, _, author, ctx := bookFixture(t)
	h.WithCoverStore(covers.NewStore(t.TempDir()))
	book := &models.Book{
		ForeignID: "B1", AuthorID: author.ID, Title: "T", SortTitle: "t", Status: "wanted",
		ImageURL: "https://example.invalid/old.jpg", Genres: []string{}, MetadataProvider: "openlibrary",
	}
	if err := books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	post := func(body []byte) *httptest.ResponseRecorder {
		id := strconv.FormatInt(book.ID, 10)
		req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+id+"/cover", bytes.NewReader(body)), "id", id)
		rec := httptest.NewRecorder()
		h.UploadCover(rec, req)
		return rec
	}
	return h, book, post
}

func TestUploadCover_StoresAndSetsCover(t *testing.T) {
	h, book, post := coverFixture(t)
	rec := post(pngBytes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got, err := h.books.GetByID(t.Context(), book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !covers.IsRef(got.ImageURL) {
		t.Fatalf("image_url = %q, want a stored-cover reference", got.ImageURL)
	}
	if _, ct, ok := h.coverStore.Resolve(got.ImageURL); !ok || ct != "image/png" {
		t.Errorf("stored cover resolves = %v, content type %q", ok, ct)
	}
	// The response is shaped like GET /book/{id}: a URL the browser can load,
	// not the stored reference.
	var resp models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if want := ProxyImageURL(got.ImageURL); resp.ImageURL != want {
		t.Errorf("response imageUrl = %q, want %q", resp.ImageURL, want)
	}
}

// Anything that is not an image, or too large, is refused and the book keeps
// its cover.
func TestUploadCover_RejectsBadUploads(t *testing.T) {
	for name, tc := range map[string]struct {
		body []byte
		want int
	}{
		"not an image": {[]byte("<html>hello</html>"), http.StatusUnsupportedMediaType},
		"empty":        {nil, http.StatusUnsupportedMediaType},
		"too large":    {append(append([]byte{}, pngBytes...), make([]byte, covers.MaxBytes)...), http.StatusRequestEntityTooLarge},
		// One byte over the cap gets past the request reader, which allows
		// that byte so it can tell, and is refused by the store's own cap.
		"one byte over": {append(append([]byte{}, pngBytes...), make([]byte, covers.MaxBytes+1-len(pngBytes))...), http.StatusRequestEntityTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			h, book, post := coverFixture(t)
			if rec := post(tc.body); rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			got, _ := h.books.GetByID(t.Context(), book.ID)
			if got.ImageURL != book.ImageURL {
				t.Errorf("image_url changed to %q", got.ImageURL)
			}
		})
	}
}

func TestUploadCover_NoStoreIsUnavailable(t *testing.T) {
	h, _, post := coverFixture(t)
	h.coverStore = nil
	if rec := post(pngBytes); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

// Another user's book is not found, as for every per-book route.
func TestUploadCover_OtherUsersBook(t *testing.T) {
	auth.SetEnforceTenancyForTests(t, true)
	f := seedTwoUserAuthors(t)
	ctx := t.Context()
	book := &models.Book{ForeignID: "B2", AuthorID: f.a1.ID, Title: "T", SortTitle: "t", Status: "wanted", Genres: []string{}, OwnerUserID: f.u1}
	if err := f.books.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	h := NewBookHandler(f.books, nil, nil, nil).WithCoverStore(covers.NewStore(t.TempDir()))
	id := strconv.FormatInt(book.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+id+"/cover", bytes.NewReader(pngBytes)), "id", id)
	req = req.WithContext(withAuthCtx(req.Context(), f.u2, "user"))
	rec := httptest.NewRecorder()
	h.UploadCover(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got, _ := f.books.GetByID(ctx, book.ID); got.ImageURL != "" {
		t.Errorf("another user's book got a cover: %q", got.ImageURL)
	}
}

// errReader fails the read partway, as a dropped connection does.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// A body that cannot be read is a 400, and the book keeps its cover.
func TestUploadCover_UnreadableBody(t *testing.T) {
	h, book, _ := coverFixture(t)
	id := strconv.FormatInt(book.ID, 10)
	req := withURLParam(httptest.NewRequest(http.MethodPost, "/api/v1/book/"+id+"/cover", errReader{}), "id", id)
	rec := httptest.NewRecorder()
	h.UploadCover(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got, _ := h.books.GetByID(t.Context(), book.ID); got.ImageURL != book.ImageURL {
		t.Errorf("image_url changed to %q", got.ImageURL)
	}
}

// A store that cannot write (its folder is a file here, as a full or
// read-only volume would fail too) is a server error, and the book keeps its
// cover.
func TestUploadCover_StoreWriteFails(t *testing.T) {
	h, book, post := coverFixture(t)
	notADir := filepath.Join(t.TempDir(), "covers")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h.WithCoverStore(covers.NewStore(notADir))
	if rec := post(pngBytes); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if got, _ := h.books.GetByID(t.Context(), book.ID); got.ImageURL != book.ImageURL {
		t.Errorf("image_url changed to %q", got.ImageURL)
	}
}

// The page replaces its book with this response, so it must carry what the
// book detail GET does: the tracked files and the provider identifiers.
// Without them a multi file book loses its files table until a reload.
func TestUploadCover_ResponseMatchesDetail(t *testing.T) {
	h, book, post := coverFixture(t)
	ctx := t.Context()
	if err := h.books.AddBookFile(ctx, book.ID, "ebook", filepath.Join(t.TempDir(), "a.epub")); err != nil {
		t.Fatal(err)
	}
	if err := h.books.UpsertBookIdentifier(ctx, book.ID, "hc:42"); err != nil {
		t.Fatal(err)
	}
	rec := post(pngBytes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp models.Book
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.BookFiles) != 1 {
		t.Errorf("response bookFiles = %d, want the tracked file", len(resp.BookFiles))
	}
	if len(resp.Identifiers) == 0 {
		t.Errorf("response identifiers is empty, want the provider identifiers")
	}
}
