// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// fakeFed is a test Federation provider: BeginAuth echoes the core-generated
// params into a fake IdP URL, and ValidateAssertion asserts the core handed it
// the full flow context (nonce + PKCE verifier for OIDC) before returning a fixed
// identity. Raw=="bad" simulates a rejected assertion.
type fakeFed struct{ proto string }

func (f *fakeFed) SAMLMetadata() ([]byte, error) {
	if f.proto != auth.ProtocolSAML {
		return nil, auth.ErrSSONotConfigured
	}
	return []byte(`<?xml version="1.0"?><EntityDescriptor entityID="https://sp.example.test"/>`), nil
}

func (f *fakeFed) Protocol() string { return f.proto }

func (f *fakeFed) BeginAuth(_ context.Context, p auth.AuthParams) (string, error) {
	return "https://idp.example.test/authorize?state=" + url.QueryEscape(p.State) +
		"&code_challenge=" + url.QueryEscape(p.PKCEChallenge) +
		"&redirect_uri=" + url.QueryEscape(p.RedirectURI), nil
}

func (f *fakeFed) ValidateAssertion(_ context.Context, a auth.Assertion) (auth.FederatedIdentity, error) {
	if a.Protocol == auth.ProtocolOIDC && (a.Nonce == "" || a.PKCEVerifier == "") {
		return auth.FederatedIdentity{}, errors.New("core did not pass nonce/verifier")
	}
	if a.Raw == "" || a.Raw == "bad" {
		return auth.FederatedIdentity{}, errors.New("invalid assertion")
	}
	return auth.FederatedIdentity{Subject: "idp-sub-1", Email: "sso-user@acme.com", DisplayName: "SSO User"}, nil
}

// newFedHarness builds a harness whose API server is wired with fed.
func newFedHarness(t *testing.T, fed auth.Federation) *harness {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(context.Background()); return e }); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Federation: fed,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, authr: authr, signer: signer, setupTok: plaintext}
}

// raw issues a request and returns the recorder (for header/cookie inspection).
func (h *harness) raw(method, path string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, http.NoBody)
	req.RemoteAddr = "10.0.0.1:1234"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSSONotConfigured(t *testing.T) {
	h := newHarness(t)
	h.adminLogin() // complete setup so the gate is open
	r := h.raw("GET", "/v1/auth/federation/start", nil)
	if r.Code != http.StatusNotImplemented {
		t.Errorf("start with NoFederation = %d, want 501", r.Code)
	}
	cb := h.raw("GET", "/v1/auth/federation/callback", nil)
	if cb.Code != http.StatusNotImplemented {
		t.Errorf("callback with NoFederation = %d, want 501", cb.Code)
	}
}

func TestCommunitySAMLMetadata(t *testing.T) {
	for _, protocol := range []string{"", auth.ProtocolOIDC, auth.ProtocolSAML} {
		t.Run(protocol, func(t *testing.T) {
			h := newFedHarness(t, &fakeFed{proto: protocol})
			h.adminLogin()
			r := h.raw("GET", "/v1/auth/federation/saml/metadata", nil)
			if protocol != auth.ProtocolSAML {
				if r.Code != http.StatusNotImplemented {
					t.Fatalf("unconfigured/non-SAML metadata = %d, want 501", r.Code)
				}
				return
			}
			if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "application/samlmetadata+xml; charset=utf-8" {
				t.Fatalf("Community metadata = %d %s", r.Code, r.Header().Get("Content-Type"))
			}
			if r.Body.String() != `<?xml version="1.0"?><EntityDescriptor entityID="https://sp.example.test"/>` {
				t.Fatalf("metadata differs: %s", r.Body.String())
			}
			if len(r.Result().Cookies()) != 0 {
				t.Fatal("metadata must not issue a login credential")
			}
		})
	}
}

func TestServerInfoSSOProviders(t *testing.T) {
	for _, protocol := range []string{"", auth.ProtocolOIDC, auth.ProtocolSAML} {
		t.Run(protocol, func(t *testing.T) {
			h := newFedHarness(t, &fakeFed{proto: protocol})
			r := h.raw("GET", "/v1/server-info", nil)
			var info map[string]any
			if err := json.Unmarshal(r.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			providers, present := info["sso_providers"]
			if protocol == "" {
				if present {
					t.Fatal("unconfigured server must omit provider buttons")
				}
				return
			}
			items, ok := providers.([]any)
			if !ok || len(items) != 1 {
				t.Fatalf("configured provider buttons = %v, want one", providers)
			}
			item := items[0].(map[string]any)
			if len(item) != 2 || item["label"] == "" {
				t.Fatalf("provider must expose only label and start_url: %v", item)
			}
			if item["start_url"] != "/v1/auth/federation/start?browser_session=1&return_to=%2F" {
				t.Fatalf("start URL = %v", item["start_url"])
			}
		})
	}
}

func TestServerInfoManagedSSODisplayName(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	input := map[string]any{
		"protocol": "oidc", "enabled": true,
		"oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "test-client-secret",
		"display_name": "Contoso",
	}
	put := h.do("PUT", "/v1/console/sso", admin, input, nil)
	if put.code != http.StatusOK || put.body["display_name"] != "Contoso" {
		t.Fatalf("save provider display name = %d %s", put.code, put.raw)
	}
	assertLabel := func(want string) {
		t.Helper()
		r := h.do("GET", "/v1/server-info", "", nil, nil)
		items, ok := r.body["sso_providers"].([]any)
		if !ok || len(items) != 1 || items[0].(map[string]any)["label"] != want {
			t.Fatalf("provider button = %v, want label %q", r.body["sso_providers"], want)
		}
	}
	assertLabel("Contoso")
	delete(input, "display_name")
	input["oidc_client_secret"] = ""
	if r := h.do("PUT", "/v1/console/sso", admin, input, nil); r.code != http.StatusOK {
		t.Fatalf("older client update = %d %s", r.code, r.raw)
	}
	assertLabel("Contoso")
	input["display_name"] = ""
	if r := h.do("PUT", "/v1/console/sso", admin, input, nil); r.code != http.StatusOK {
		t.Fatalf("clear name = %d %s", r.code, r.raw)
	}
	assertLabel("Single sign-on")
	input["enabled"] = false
	if r := h.do("PUT", "/v1/console/sso", admin, input, nil); r.code != http.StatusOK {
		t.Fatalf("disable provider = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/server-info", "", nil, nil); r.body["sso_providers"] != nil {
		t.Fatalf("disabled provider advertised: %v", r.body["sso_providers"])
	}
}

func TestSSOAssuranceMappingRoundTrip(t *testing.T) {
	h := newConsoleHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	mapping := map[string]any{"amr": []string{"otp"}, "acr": []string{"urn:corp:mfa"}, "saml_contexts": []string{"https://refeds.org/profile/mfa"}}
	input := map[string]any{
		"protocol": "oidc", "enabled": true,
		"oidc_issuer": "https://idp.example", "oidc_client_id": "cid", "oidc_client_secret": "test-client-secret",
		"assurance_mapping": mapping,
	}
	if r := h.do("PUT", "/v1/console/sso", admin, input, nil); r.code != http.StatusOK {
		t.Fatalf("save mapping = %d %s", r.code, r.raw)
	}
	delete(input, "assurance_mapping")
	input["oidc_client_secret"] = ""
	if r := h.do("PUT", "/v1/console/sso", admin, input, nil); r.code != http.StatusOK {
		t.Fatalf("older client update = %d %s", r.code, r.raw)
	}
	r := h.do("GET", "/v1/console/sso", admin, nil, nil)
	got, err := json.Marshal(r.body["assurance_mapping"])
	want, _ := json.Marshal(mapping)
	if err != nil || string(got) != string(want) {
		t.Fatalf("stored mapping = %s, want %s; err %v", got, want, err)
	}
}

func TestSAMLFlowCookieAcceptsCrossSitePOST(t *testing.T) {
	for _, test := range []struct {
		name, protocol, origin string
		forwarded              bool
		secure                 bool
		sameSite               http.SameSite
	}{
		{"oidc", auth.ProtocolOIDC, "https://console.example", false, true, http.SameSiteLaxMode},
		{"saml", auth.ProtocolSAML, "https://console.example", false, true, http.SameSiteNoneMode},
		{"saml behind HTTPS proxy", auth.ProtocolSAML, "http://console.example", true, true, http.SameSiteNoneMode},
		{"same-site HTTP development", auth.ProtocolSAML, "http://console.example", false, false, http.SameSiteLaxMode},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newFedHarness(t, &fakeFed{proto: test.protocol})
			h.adminLogin()
			req := httptest.NewRequest("GET", test.origin+"/v1/auth/federation/start?browser_session=1", http.NoBody)
			if test.forwarded {
				req.Header.Set("X-Forwarded-Proto", "https")
			}
			rec := httptest.NewRecorder()
			h.srv.Handler().ServeHTTP(rec, req)
			cookie := findCookie(rec.Result().Cookies(), "olv_sso")
			if cookie == nil || cookie.SameSite != test.sameSite || cookie.Secure != test.secure || !cookie.HttpOnly {
				t.Fatalf("flow cookie = %+v, want Secure=%v HttpOnly SameSite=%d", cookie, test.secure, test.sameSite)
			}
		})
	}
}

func TestSSOLoginFlow(t *testing.T) {
	h := newFedHarness(t, &fakeFed{proto: auth.ProtocolOIDC})
	h.adminLogin() // complete setup

	// --- start: 302 to the IdP, a flow cookie, state carried in the redirect ---
	start := h.raw("GET", "/v1/auth/federation/start?return_to=/dashboard", nil)
	if start.Code != http.StatusFound {
		t.Fatalf("start = %d, want 302", start.Code)
	}
	loc, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	if state == "" || loc.Query().Get("code_challenge") == "" {
		t.Fatalf("redirect missing state/code_challenge: %s", start.Header().Get("Location"))
	}
	cookie := findCookie(start.Result().Cookies(), "olv_sso")
	if cookie == nil {
		t.Fatal("start set no olv_sso flow cookie")
	}

	// --- callback: validated assertion -> JIT user -> opaque session ---
	cb := h.raw("GET", "/v1/auth/federation/callback?code=good-code&state="+url.QueryEscape(state), []*http.Cookie{cookie})
	if cb.Code != http.StatusOK {
		t.Fatalf("callback = %d %s", cb.Code, cb.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(cb.Body.Bytes(), &body)
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("callback returned no session token")
	}
	if body["return_to"] != "/dashboard" {
		t.Errorf("return_to = %v, want /dashboard", body["return_to"])
	}
	// The session authenticates, and the JIT user has the federated email.
	p, err := h.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate SSO session: %v", err)
	}
	if p.DisplayName != "SSO User" {
		t.Errorf("display name = %q, want the federated name", p.DisplayName)
	}
}

func TestSSOCallbackCSRFAndMissingFlow(t *testing.T) {
	h := newFedHarness(t, &fakeFed{proto: auth.ProtocolOIDC})
	h.adminLogin()

	start := h.raw("GET", "/v1/auth/federation/start", nil)
	cookie := findCookie(start.Result().Cookies(), "olv_sso")

	// Wrong state -> 403 (CSRF).
	bad := h.raw("GET", "/v1/auth/federation/callback?code=good&state=WRONG", []*http.Cookie{cookie})
	if bad.Code != http.StatusForbidden {
		t.Errorf("csrf mismatch = %d, want 403", bad.Code)
	}

	// Missing flow cookie -> 400.
	noCookie := h.raw("GET", "/v1/auth/federation/callback?code=good&state=x", nil)
	if noCookie.Code != http.StatusBadRequest {
		t.Errorf("no cookie = %d, want 400", noCookie.Code)
	}
}

// fakeMultiIDP stands in for the reserved enterprise multi-IdP capability
// (auth.MultiIDP): it selects a tenant's own active config by TargetTenantID.
type fakeMultiIDP struct{}

// AllowsAdditionalActiveIdP: this fake stands for a fully entitled enterprise capability,
// so it always allows. The refusal path is exercised in core/auth, next to the cap itself.
func (fakeMultiIDP) AllowsAdditionalActiveIdP(context.Context) error { return nil }

func (fakeMultiIDP) SelectActive(in auth.SelectionInput, active []model.FederationConfig) (model.FederationConfig, bool) {
	// Home-realm domain match (U5) when no tenant hint, else per-tenant.
	if in.Tenant == "" || in.Tenant == auth.GlobalFederationScope {
		if in.EmailDomain != "" {
			for _, c := range active {
				if c.Status == model.StatusActive && c.Protocol != "" {
					for _, d := range c.ClaimedDomains {
						if d == in.EmailDomain {
							return c, true
						}
					}
				}
			}
		}
		return model.FederationConfig{}, false
	}
	for _, c := range active {
		if c.TargetTenantID == in.Tenant && c.Status == model.StatusActive && c.Protocol != "" {
			return c, true
		}
	}
	return model.FederationConfig{}, false
}

// TestSSOCallback_MultiIDP_NoGlobalIdP is the regression for the review's
// HIGH finding: an enterprise deployment whose only IdPs are PER-TENANT (no global
// config) must still complete a callback. The earlier callback pre-check probed the
// GLOBAL provider with an empty tenant and wrongly answered 501, breaking a valid
// login even though the flow carried a tenant hint whose IdP could authenticate.
func TestSSOCallback_MultiIDP_NoGlobalIdP(t *testing.T) {
	ctx := context.Background()

	var svc *auth.FederationService
	h := newHarnessOpts(t, func(o *api.Options) {
		// Multi-IdP capability wired (enterprise) over the same store the server uses.
		svc = auth.NewFederationService(o.Store, fakeSealer{}, fakeBuilder, auth.NoFederation{}, fakeMultiIDP{})
		o.FederationService = svc
	})
	admin := h.adminLogin()
	// tenantX is a real organization: a first sign-in through its provider provisions
	// the account's membership in it.
	tenantX := h.createOrg(admin, "tenant-x")

	// Store ONLY a per-tenant IdP for tenantX — deliberately NO global config.
	actor := auth.Principal{Kind: auth.KindUser, Superadmin: true, UserID: model.NewID(), CredID: model.NewID()}
	if _, err := svc.PutConfig(ctx, actor, tenantX, auth.FederationConfigInput{
		Protocol: auth.ProtocolOIDC, Enabled: true,
		OIDCIssuer: "https://idp-x.example", OIDCClientID: "client-x", OIDCClientSecret: "secret-x",
		ClaimedDomains: []string{"acme.com"},
	}); err != nil {
		t.Fatalf("store per-tenant config: %v", err)
	}

	// start WITH the tenant hint resolves tenantX's IdP — no global config needed.
	start := h.raw("GET", "/v1/auth/federation/start?tenant="+tenantX.String(), nil)
	if start.Code != http.StatusFound {
		t.Fatalf("start = %d, want 302 (a per-tenant IdP must resolve without a global config)", start.Code)
	}
	loc, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := loc.Query().Get("state")
	cookie := findCookie(start.Result().Cookies(), "olv_sso")
	if cookie == nil {
		t.Fatal("start set no olv_sso flow cookie")
	}

	// The callback must COMPLETE (200), not 501 — the bug was the global pre-check
	// 501ing here because there is no global config.
	cb := h.raw("GET", "/v1/auth/federation/callback?code=good-code&state="+url.QueryEscape(state), []*http.Cookie{cookie})
	if cb.Code != http.StatusOK {
		t.Fatalf("callback = %d %s, want 200 (multi-IdP-without-global must complete)", cb.Code, cb.Body.String())
	}
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}
