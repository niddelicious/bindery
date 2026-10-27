package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hookRecorder is a SettingsChangeHook that handles the keys in handles and
// remembers every call.
type hookRecorder struct {
	handles map[string]bool
	err     error
	calls   []string
}

func (r *hookRecorder) hook(_ context.Context, key string) (bool, error) {
	r.calls = append(r.calls, key)
	if !r.handles[key] {
		return false, nil
	}
	return true, r.err
}

func putSettingWithHook(t *testing.T, h *SettingsHandler, key, value string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := withKey(httptest.NewRequest(http.MethodPut, "/api/v1/setting/"+key, strings.NewReader(`{"value":"`+value+`"}`)), key)
	h.Set(rec, adminReq(req))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// TestMetadataProviderSettingsAreLive pins the registry to #1771: the primary
// provider and the Google Books key apply without a restart, so the Advanced
// tab must not badge them "Restart required".
func TestMetadataProviderSettingsAreLive(t *testing.T) {
	live := map[string]bool{
		SettingMetadataPrimaryProvider: true,
		SettingGoogleBooksAPIKey:       true,
		LegacySettingGoogleBooksAPIKey: true,
	}
	found := 0
	for _, d := range SettingDescriptors() {
		if !live[d.Key] {
			continue
		}
		found++
		if d.RestartRequired {
			t.Errorf("%s is applied live by the settings change hook, so restartRequired must be false", d.Key)
		}
		if strings.Contains(strings.ToLower(d.Description), "until bindery restarts") {
			t.Errorf("%s description still promises a restart: %q", d.Key, d.Description)
		}
	}
	if found != len(live) {
		t.Fatalf("found %d of %d descriptors", found, len(live))
	}
}

// TestSettingsChangeHook covers the #1771 hook from the handler's side: a
// stored provider setting runs it exactly once and reports appliedLive, a
// rejected value never reaches it, a hook failure leaves the write standing,
// and keys nothing applies live carry no appliedLive at all.
func TestSettingsChangeHook(t *testing.T) {
	t.Run("valid primary runs the hook once", func(t *testing.T) {
		h, repo, ctx := settingsFixture(t)
		rec := &hookRecorder{handles: map[string]bool{SettingMetadataPrimaryProvider: true}}
		h.WithChangeHook(rec.hook)

		code, body := putSettingWithHook(t, h, SettingMetadataPrimaryProvider, "bnf")
		if code != http.StatusOK {
			t.Fatalf("status = %d, body %v", code, body)
		}
		if len(rec.calls) != 1 || rec.calls[0] != SettingMetadataPrimaryProvider {
			t.Fatalf("hook calls = %v, want one for %s", rec.calls, SettingMetadataPrimaryProvider)
		}
		if body["appliedLive"] != true {
			t.Errorf("appliedLive = %v, want true", body["appliedLive"])
		}
		if s, _ := repo.Get(ctx, SettingMetadataPrimaryProvider); s == nil || s.Value != "bnf" {
			t.Errorf("stored value = %v, want bnf", s)
		}
	})

	t.Run("invalid value is rejected without calling the hook", func(t *testing.T) {
		h, _, _ := settingsFixture(t)
		rec := &hookRecorder{handles: map[string]bool{SettingMetadataPrimaryProvider: true}}
		h.WithChangeHook(rec.hook)

		for _, value := range []string{"goodreads", "hardcover"} { // unknown; hardcover without a token
			if code, body := putSettingWithHook(t, h, SettingMetadataPrimaryProvider, value); code != http.StatusBadRequest {
				t.Errorf("PUT %q: status = %d, want 400 (body %v)", value, code, body)
			}
		}
		if len(rec.calls) != 0 {
			t.Errorf("hook ran for rejected writes: %v", rec.calls)
		}
	})

	t.Run("hook failure does not fail the write", func(t *testing.T) {
		h, repo, ctx := settingsFixture(t)
		rec := &hookRecorder{handles: map[string]bool{SettingMetadataPrimaryProvider: true}, err: errors.New("rebuild failed")}
		h.WithChangeHook(rec.hook)

		code, body := putSettingWithHook(t, h, SettingMetadataPrimaryProvider, "dnb")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %v)", code, body)
		}
		if body["appliedLive"] != false {
			t.Errorf("appliedLive = %v, want false", body["appliedLive"])
		}
		if s, _ := repo.Get(ctx, SettingMetadataPrimaryProvider); s == nil || s.Value != "dnb" {
			t.Errorf("stored value = %v, want dnb kept despite the hook error", s)
		}
	})

	t.Run("secret key reports appliedLive without its value", func(t *testing.T) {
		h, _, _ := settingsFixture(t)
		rec := &hookRecorder{handles: map[string]bool{SettingGoogleBooksAPIKey: true}}
		h.WithChangeHook(rec.hook)

		code, body := putSettingWithHook(t, h, SettingGoogleBooksAPIKey, "AIzaKEY")
		if code != http.StatusOK {
			t.Fatalf("status = %d (body %v)", code, body)
		}
		if body["value"] != "" || body["appliedLive"] != true {
			t.Errorf("body = %v, want empty value and appliedLive true", body)
		}
		if len(rec.calls) != 1 {
			t.Errorf("hook calls = %v, want 1", rec.calls)
		}
	})

	t.Run("unhandled key has no appliedLive", func(t *testing.T) {
		h, _, _ := settingsFixture(t)
		rec := &hookRecorder{}
		h.WithChangeHook(rec.hook)

		_, body := putSettingWithHook(t, h, "autoGrab.enabled", "true")
		if _, ok := body["appliedLive"]; ok {
			t.Errorf("appliedLive present for a key nothing applies live: %v", body)
		}
	})

	t.Run("no hook keeps the old response", func(t *testing.T) {
		h, _, _ := settingsFixture(t)
		code, body := putSettingWithHook(t, h, SettingMetadataPrimaryProvider, "dnb")
		if code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		if _, ok := body["appliedLive"]; ok {
			t.Errorf("appliedLive present without a hook: %v", body)
		}
	})

	t.Run("delete runs the hook", func(t *testing.T) {
		h, repo, ctx := settingsFixture(t)
		if err := repo.Set(ctx, SettingMetadataPrimaryProvider, "dnb"); err != nil {
			t.Fatal(err)
		}
		rec := &hookRecorder{handles: map[string]bool{SettingMetadataPrimaryProvider: true}}
		h.WithChangeHook(rec.hook)

		w := httptest.NewRecorder()
		h.Delete(w, adminReq(withKey(httptest.NewRequest(http.MethodDelete, "/api/v1/setting/"+SettingMetadataPrimaryProvider, nil), SettingMetadataPrimaryProvider)))
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d", w.Code)
		}
		if len(rec.calls) != 1 {
			t.Errorf("hook calls = %v, want 1", rec.calls)
		}
	})
}
