package httpsec

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// An OAuth style credential rides in the fragment (#access_token=...), after
// the query, so a pattern that only looks behind ?, & and ; misses it.
func TestRedactSecretsFragmentCredential(t *testing.T) {
	for _, in := range []string{
		"https://host/cb#access_token=SEKRET123",
		"https://host/cb?a=1#token=SEKRET123&b=2",
	} {
		got := RedactSecrets(in)
		if strings.Contains(got, "SEKRET123") {
			t.Errorf("RedactSecrets(%q) = %q, credential survived", in, got)
		}
	}
	if got := RedactSecrets("https://host/cb#section=2"); got != "https://host/cb#section=2" {
		t.Errorf("a harmless fragment changed: %q", got)
	}
}

func TestRedactURLErrorScrubsUserinfoQueryAndFragment(t *testing.T) {
	ue := &url.Error{Op: "Get", URL: "https://bob:SEKRET123@host/x?apikey=SEKRET123#token=SEKRET123", Err: errors.New("boom")}
	got := RedactURLError(ue)
	if !errors.Is(got, ue) {
		t.Fatal("RedactURLError must return the same error")
	}
	if strings.Contains(got.Error(), "SEKRET123") {
		t.Errorf("error = %q, credential survived", got)
	}
}
