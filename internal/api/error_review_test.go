package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
)

// The fixed messages the leak fixes put in place must still tell the user what
// is wrong. These pin the useful part of each, so a later tightening cannot
// quietly turn them back into "something failed".

func TestRootFolderStatMessage_OneMessagePerCause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing", &fs.PathError{Op: "stat", Path: "/srv/x", Err: syscall.ENOENT}, "path does not exist; check the path, and that the volume is mounted into the container"},
		{"permission", &fs.PathError{Op: "stat", Path: "/srv/x", Err: syscall.EACCES}, "path is not readable by Bindery; check the folder's owner and permissions"},
		{"parent is a file", &fs.PathError{Op: "stat", Path: "/srv/file/x", Err: syscall.ENOTDIR}, "path is not valid: a parent of it is a file, not a folder"},
		{"other", &fs.PathError{Op: "stat", Path: "/srv/x", Err: syscall.EIO}, "path is not accessible"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		got := rootFolderStatMessage(c.err)
		if got != c.want {
			t.Errorf("%s: message = %q, want %q", c.name, got, c.want)
		}
		if strings.Contains(got, "/srv") {
			t.Errorf("%s: message echoes the path: %q", c.name, got)
		}
		if seen[got] {
			t.Errorf("%s: message %q is not distinct", c.name, got)
		}
		seen[got] = true
	}
}

// A provider pause is Bindery's own state and the user needs it: the call will
// keep failing until the reset time or a new token. The token case is rebuilt,
// not relayed, because the cause carries the provider's own words.
func TestUpstreamErrorMessage_ProviderHold(t *testing.T) {
	reset := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	wrapped := fmt.Errorf("lookup: %w", &metadata.DailyQuotaError{ResetAt: reset})
	if got := upstreamErrorMessage(wrapped); !strings.Contains(got, "daily quota") || !strings.Contains(got, "2026-10-12T00:00:00Z") {
		t.Errorf("daily quota message = %q, want the quota and the reset time", got)
	}
	rejected := fmt.Errorf("lookup: %w", &metadata.TokenRejectedError{Provider: "Hardcover", Cause: errors.New("token rejected (HTTP 401: " + leakSecret + ")")})
	got := upstreamErrorMessage(rejected)
	if got != "Hardcover rejected the API token; requests are paused until the token is changed in Settings" {
		t.Errorf("token message = %q", got)
	}
	if strings.Contains(got, leakSecret) {
		t.Errorf("token message relays the provider's text: %q", got)
	}
	if got := upstreamErrorMessage(transportLeak()); got != msgUpstream {
		t.Errorf("transport failure message = %q, want the generic one", got)
	}
}

func TestErrorLeak_CreateAuthorLookupError(t *testing.T) {
	rec := httptest.NewRecorder()
	(&AuthorHandler{}).writeCreateAuthorError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/author", nil), &createAuthorLookupError{Err: transportLeak()})
	wantFixed(t, rec, http.StatusBadGateway, msgUpstream)
}

// A refused or unreachable download client is the user's cue to look at the
// client, not at the release, so the generic message carries Bindery's hint.
func TestWriteGrabError_NetworkFailureKeepsHint(t *testing.T) {
	refused := &sendError{cause: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	rec := httptest.NewRecorder()
	writeGrabError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", nil), refused)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := errorField(t, rec)
	if !strings.HasPrefix(got, msgSendFailed+" (connection refused") {
		t.Errorf("error = %q, want the generic message plus the refused hint", got)
	}

	// Anything else stays exactly the generic text.
	rec = httptest.NewRecorder()
	writeGrabError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/queue/grab", nil), &sendError{cause: errors.New("rejected: apikey=" + leakSecret)})
	wantFixed(t, rec, http.StatusBadGateway, msgSendFailed)
}

func TestUpstreamDetail_FreeTextCredentials(t *testing.T) {
	for _, in := range []string{
		"login failed apikey=" + leakSecret + " for user",
		"Authorization: Bearer " + leakSecret,
		"x-api-key: " + leakSecret,
		`{"token":"` + leakSecret + `"}`,
		"form password=" + leakSecret + "&user=x",
		"Set-Cookie: SID=" + leakSecret + "; Path=/",
		"redirected to https://h/cb#access_token=" + leakSecret,
	} {
		got := upstreamDetail(in)
		if strings.Contains(got, leakSecret) {
			t.Errorf("upstreamDetail(%q) = %q, credential survived", in, got)
		}
		if got == "" {
			t.Errorf("upstreamDetail(%q) is empty", in)
		}
	}
	// Prose that merely mentions a credential word is the useful part.
	if got := upstreamDetail("token: expired, log in again"); got != "token: expired, log in again" {
		t.Errorf("prose was altered: %q", got)
	}
}

// 200 is not a multiple of 3, so a cut at the cap lands inside the third byte
// of a three byte rune unless the cut backs up to a rune boundary.
func TestUpstreamDetail_CapNeverSplitsARune(t *testing.T) {
	in := strings.Repeat("日", 100)
	got := upstreamDetail(in)
	if !strings.HasPrefix(in, got) || len(got) > upstreamDetailMax || len(got) < upstreamDetailMax-3 {
		t.Fatalf("len = %d, want a prefix of at most %d bytes", len(got), upstreamDetailMax)
	}
	if strings.ToValidUTF8(got, "") != got {
		t.Errorf("cut inside a rune: %q", got)
	}
}

func TestErrorLeak_SetRoleToAdminUnknownUser(t *testing.T) {
	h, _ := newUserMgmtFixture(t)
	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"admin"}`, 424242, context.Background()))
	wantFixed(t, rec, http.StatusNotFound, "No user with that id.")
}
