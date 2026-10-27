package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/auth/oidc"
)

const providersWithGroupsAndFailure = `[{"id":"corp","name":"Corp SSO","issuer":"https://idp.internal.invalid","client_id":"bindery","client_secret":"s3cr3t-value","scopes":["openid","email"],"allowed_groups":["admins","finance-team"]}]`

func getProvidersAs(t *testing.T, role string) (*httptest.ResponseRecorder, []providerWithStatus) {
	t.Helper()
	settings, _ := covOIDCSettings(t)
	if err := settings.Set(t.Context(), SettingOIDCProviders, providersWithGroupsAndFailure); err != nil {
		t.Fatal(err)
	}
	mgr := oidc.NewManager()
	// The issuer does not resolve, so discovery fails and the manager records a
	// failed status carrying the lookup error.
	mgr.Reload(context.Background(), []oidc.ProviderConfig{{
		ID: "corp", Name: "Corp SSO", Issuer: "https://idp.internal.invalid", ClientID: "bindery", ClientSecret: "s3cr3t-value",
	}})
	h := NewOIDCHandler(mgr, nil, settings, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/providers", nil)
	if role != "" {
		req = req.WithContext(auth.WithUserRole(req.Context(), role))
	}
	rec := httptest.NewRecorder()
	h.GetProviders(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got []providerWithStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return rec, got
}

// S6: the list is public so the login page can draw its buttons, which need the
// id, the name and whether discovery worked. The discovery error text (it can
// name an internal host and the failure) and the group allow list are for the
// admin settings screen only.
func TestGetProviders_PublicViewHidesDiscoveryErrorAndGroups(t *testing.T) {
	for _, role := range []string{"", "user"} {
		rec, got := getProvidersAs(t, role)
		body := rec.Body.String()
		if len(got) != 1 || got[0].ID != "corp" || got[0].Name != "Corp SSO" {
			t.Fatalf("role %q: providers = %+v", role, got)
		}
		if got[0].Status == nil || got[0].Status.State != "failed" {
			t.Fatalf("role %q: the login page still needs status.state, got %+v", role, got[0].Status)
		}
		// The issuer URL itself stays (it is in the authorize redirect anyway);
		// the resolver failure text that quotes it, and everything below, must go.
		for _, leak := range []string{"last_error", "allowed_groups", "finance-team", "no such host", "s3cr3t-value", "last_attempt"} {
			if strings.Contains(body, leak) {
				t.Errorf("role %q: public provider list contains %q: %s", role, leak, body)
			}
		}
	}
}

// The admin settings screen keeps every field.
func TestGetProviders_AdminViewKeepsDetail(t *testing.T) {
	rec, got := getProvidersAs(t, "admin")
	if len(got) != 1 {
		t.Fatalf("providers = %+v", got)
	}
	if got[0].Issuer != "https://idp.internal.invalid" || got[0].ClientID != "bindery" || len(got[0].Scopes) != 2 {
		t.Errorf("admin view lost fields: %+v", got[0])
	}
	if len(got[0].AllowedGroups) != 2 {
		t.Errorf("admin view lost allowed_groups: %+v", got[0].AllowedGroups)
	}
	if got[0].Status == nil || got[0].Status.State != "failed" || got[0].Status.LastError == "" {
		t.Errorf("admin view lost the discovery error: %+v", got[0].Status)
	}
	if strings.Contains(rec.Body.String(), "s3cr3t-value") {
		t.Errorf("client secret leaked: %s", rec.Body.String())
	}
}

// Other ways of being the install's admin keep the full view too: the API key
// (the middleware does not stamp it on a public path) and an auth mode that
// admits the caller (disabled mode, where the settings screen has no session).
func TestGetProviders_APIKeyAndModeGrantKeepDetail(t *testing.T) {
	settings, _ := covOIDCSettings(t)
	ctx := t.Context()
	if err := settings.Set(ctx, SettingOIDCProviders, providersWithGroupsAndFailure); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, SettingAuthAPIKey, "the-api-key-value"); err != nil {
		t.Fatal(err)
	}
	h := NewOIDCHandler(oidc.NewManager(), nil, settings, nil, nil)
	get := func(mutate func(*http.Request)) string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/providers", nil)
		mutate(req)
		rec := httptest.NewRecorder()
		h.GetProviders(rec, req)
		return rec.Body.String()
	}

	if body := get(func(r *http.Request) {}); strings.Contains(body, "allowed_groups") {
		t.Errorf("anonymous caller got the admin view: %s", body)
	}
	if body := get(func(r *http.Request) { r.Header.Set("X-Api-Key", "wrong-key") }); strings.Contains(body, "allowed_groups") {
		t.Errorf("wrong API key got the admin view: %s", body)
	}
	if body := get(func(r *http.Request) { r.Header.Set("X-Api-Key", "the-api-key-value") }); !strings.Contains(body, "allowed_groups") {
		t.Errorf("valid API key lost the admin view: %s", body)
	}
	if err := settings.Set(ctx, SettingAuthMode, string(auth.ModeDisabled)); err != nil {
		t.Fatal(err)
	}
	if body := get(func(r *http.Request) { r.Host = "localhost" }); !strings.Contains(body, "allowed_groups") {
		t.Errorf("disabled mode (caller is the admin) lost the admin view: %s", body)
	}
}
