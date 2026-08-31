// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

type harness struct {
	t      *testing.T
	srv    *api.Server
	st     store.Store
	authr  *auth.Authenticator
	signer *audit.Signer
	// setupTok is the plaintext one-time setup token; setupTokFile is the token
	// manager behind it, so a test can re-mint the token after setup consumed it
	// (the only way to drive the post-bootstrap failure path of /v1/setup).
	setupTok     string
	setupTokFile *secure.SetupToken
}

func newHarness(t *testing.T, modules ...api.Module) *harness {
	return newHarnessOpts(t, nil, modules...)
}

// newHarnessOpts builds a harness, letting a caller customize the api.Options
// before the server is built (e.g. wire a FederationService for the console
// tests). configure runs after the required fields are set, so it can read
// o.Store. A nil configure is the default harness.
func newHarnessOpts(t *testing.T, configure func(*api.Options), modules ...api.Module) *harness {
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
	o := api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: modules,
	}
	if configure != nil {
		configure(&o)
	}
	srv, err := api.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, st: st, authr: authr, signer: signer, setupTok: plaintext, setupTokFile: tok}
}

// elevate raises the session behind token to AAL3 (the step-up the console runs
// via WebAuthn/PIV), so a test can exercise an AAL3-gated configure action without
// the full hardware ceremony. It re-reads the principal from the token and calls
// the same ElevateSession primitive the ceremonies terminate in.
func (h *harness) elevate(token string) {
	h.t.Helper()
	p, err := h.authr.Authenticate(context.Background(), token)
	if err != nil {
		h.t.Fatalf("authenticate for elevate: %v", err)
	}
	if _, err := h.authr.ElevateSession(context.Background(), p, "webauthn", auth.AAL3); err != nil {
		h.t.Fatalf("elevate: %v", err)
	}
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func (h *harness) do(method, path, token string, body any, hdr map[string]string) resp {
	h.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func tenantHdr(t model.TenantID) map[string]string {
	return map[string]string{"X-Olivares-Tenant": t.String()}
}

// adminLogin runs setup and returns a superadmin session token.
func (h *harness) adminLogin() string {
	h.t.Helper()
	r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) createOrg(token, slug string) model.TenantID {
	h.t.Helper()
	r := h.do("POST", "/v1/system/orgs", token, map[string]any{"name": slug, "slug": slug}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org %s = %d %s", slug, r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

func TestSystemDropOrg(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("DELETE", "/v1/system/orgs/"+tenant.String(), admin, nil, nil)
	if r.code != http.StatusNoContent {
		t.Fatalf("drop org = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/system/orgs", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("list orgs after drop = %d %s", r.code, r.raw)
	}
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		if m["tenant_id"] == tenant.String() {
			t.Fatalf("dropped tenant %s still present in list response: %v", tenant, r.body["items"])
		}
	}

	r = h.do("GET", "/v1/audit/system", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("system audit after drop = %d %s", r.code, r.raw)
	}
	found := false
	for _, it := range r.body["items"].([]any) {
		m := it.(map[string]any)
		if m["action"] == "tenant.drop" && m["target_id"] == tenant.String() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("system audit missing tenant.drop for %s: %v", tenant, r.body["items"])
	}
}

func TestSystemDropOrgUnknownTenant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := model.NewTenantID()

	r := h.do("DELETE", "/v1/system/orgs/"+tenant.String(), admin, nil, nil)
	if r.code != http.StatusNotFound {
		t.Fatalf("drop unknown org = %d %s, want 404", r.code, r.raw)
	}
}

func TestSystemDropOrgUnauthenticated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("DELETE", "/v1/system/orgs/"+tenant.String(), "", nil, nil)
	if r.code != http.StatusUnauthorized {
		t.Fatalf("drop org without auth = %d %s, want 401", r.code, r.raw)
	}
}

func TestSystemDropOrgNonSuperadminForbidden(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": "owner@acme.io", "password": "tenantowner1"}, nil)
	if cr.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleOwner}, nil); g.code != http.StatusCreated {
		t.Fatalf("grant = %d %s", g.code, g.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "owner@acme.io", "password": "tenantowner1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login = %d %s", lr.code, lr.raw)
	}
	bound := lr.body["token"].(string)

	r := h.do("DELETE", "/v1/system/orgs/"+tenant.String(), bound, nil, nil)
	if r.code != http.StatusForbidden {
		t.Fatalf("drop org as tenant owner = %d %s, want 403", r.code, r.raw)
	}
}

func TestSystemDropOrgRejectsSystemTenant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()

	r := h.do("DELETE", "/v1/system/orgs/"+model.SystemTenantID.String(), admin, nil, nil)
	if r.code != http.StatusBadRequest {
		t.Fatalf("drop system tenant = %d %s, want 400", r.code, r.raw)
	}
}

// TestSystemAuditReadSuperadminOnly covers the case where the superadmin reads the
// system-tenant ledger (where cross-tenant ops are recorded) via /v1/audit/system,
// the tenant-scoped /v1/audit still refuses the system tenant, and a tenant-bound
// principal is denied 403.
func TestSystemAuditReadSuperadminOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme") // tenant id for the membership grant below

	// Superadmin reads the system chain and sees the real auth-partition events that
	// land there via AuthMutate — setup creates the superadmin and login mints a
	// session, both recorded as auth.login in the system chain (org.create, by
	// contrast, goes to the new org's OWN chain, not here).
	r := h.do("GET", "/v1/audit/system", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("system audit (superadmin) = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	actions := map[string]bool{}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			a, _ := m["action"].(string)
			actions[a] = true
		}
	}
	if !actions["auth.login"] {
		t.Errorf("system audit chain must contain the superadmin's auth.login; got actions %v", actions)
	}

	// The tenant-scoped path still refuses the reserved system tenant (unchanged).
	if r := h.do("GET", "/v1/audit", admin, nil, map[string]string{"X-Olivares-Tenant": model.SystemTenantID.String()}); r.code != http.StatusBadRequest {
		t.Errorf("/v1/audit forcing the system tenant = %d, want 400 (system stays unreachable on the tenant path)", r.code)
	}

	// A tenant-bound principal — even one holding audit:read in its own tenant — is
	// denied the system ledger (deny-closed; superadmin-only).
	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": "ed@acme.io", "password": "memberpass1"}, nil)
	if cr.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleEditor}, nil); g.code != http.StatusCreated {
		t.Fatalf("grant = %d %s", g.code, g.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "ed@acme.io", "password": "memberpass1"}, nil)
	bound := lr.body["token"].(string)
	if r := h.do("GET", "/v1/audit/system", bound, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("tenant-bound principal reading system audit = %d, want 403", r.code)
	}
}

// TestAuthRefresh covers credential renewal: a session can renew its credential (rotating the
// token and invalidating the old one), while an API token cannot be refreshed and an
// anonymous call is rejected.
func TestAuthRefresh(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()

	// Happy path: refresh returns a NEW token; the new one works, the old one stops.
	r := h.do("POST", "/v1/auth/refresh", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("refresh = %d %s", r.code, r.raw)
	}
	fresh, _ := r.body["token"].(string)
	if fresh == "" || fresh == admin {
		t.Fatalf("refresh must return a new non-empty token; got %q (old %q)", fresh, admin)
	}
	if w := h.do("GET", "/v1/auth/whoami", fresh, nil, nil); w.code != http.StatusOK {
		t.Errorf("whoami with the refreshed token = %d, want 200", w.code)
	}
	if w := h.do("GET", "/v1/auth/whoami", admin, nil, nil); w.code != http.StatusUnauthorized {
		t.Errorf("whoami with the OLD token = %d, want 401 (credential rotated)", w.code)
	}

	// Anonymous refresh is rejected.
	if r := h.do("POST", "/v1/auth/refresh", "", nil, nil); r.code != http.StatusUnauthorized {
		t.Errorf("anonymous refresh = %d, want 401", r.code)
	}

	// An API token is not a renewable session: refresh is a 400 (reissue via /v1/tokens).
	it := h.do("POST", "/v1/tokens", fresh, map[string]any{"name": "ci", "superadmin": true}, nil)
	if it.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", it.code, it.raw)
	}
	apiTok := it.body["token"].(string)
	if r := h.do("POST", "/v1/auth/refresh", apiTok, nil, nil); r.code != http.StatusBadRequest {
		t.Errorf("refresh of an API token = %d, want 400 (tokens are reissued, not refreshed)", r.code)
	}

	// Deny-closed: a REVOKED session is not renewable — refresh must not resurrect it.
	// Log in a second session, log it out (revoke), then try to refresh that token.
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	revokable := lr.body["token"].(string)
	if lo := h.do("POST", "/v1/auth/logout", revokable, nil, nil); lo.code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", lo.code, lo.raw)
	}
	if r := h.do("POST", "/v1/auth/refresh", revokable, nil, nil); r.code != http.StatusUnauthorized {
		t.Errorf("refresh of a revoked session = %d, want 401 (deny-closed; re-login required)", r.code)
	}
}

func TestSetupGateAndFlow(t *testing.T) {
	h := newHarness(t)

	// Before setup: protected routes are gated, health/server-info are not.
	if r := h.do("GET", "/v1/agents", "", nil, nil); r.code != http.StatusConflict {
		t.Fatalf("pre-setup agents = %d, want 409", r.code)
	}
	if r := h.do("GET", "/healthz", "", nil, nil); r.code != http.StatusOK {
		t.Fatalf("healthz = %d", r.code)
	}
	if r := h.do("GET", "/v1/server-info", "", nil, nil); r.code != http.StatusOK || r.body["setup_required"] != true {
		t.Fatalf("server-info pre-setup = %d %v", r.code, r.body)
	}

	// Wrong setup token is rejected.
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": "olst_wrong", "email": "a@b.c", "password": "longenough1"}, nil); r.code != http.StatusForbidden {
		t.Fatalf("bad-token setup = %d %s", r.code, r.raw)
	}
	// Correct setup creates the superadmin and consumes the token.
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil); r.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	// Re-running setup is rejected (token consumed / setup complete).
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "x@y.z", "password": "anotherone1"}, nil); r.code == http.StatusCreated {
		t.Fatalf("second setup unexpectedly succeeded: %s", r.raw)
	}
	if r := h.do("GET", "/v1/server-info", "", nil, nil); r.body["setup_required"] != false {
		t.Fatalf("server-info post-setup = %v", r.body)
	}
}

func TestAuthzAndMultiTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// Create an editor on A and a viewer on A.
	mkUser := func(email, pass, role string, tenant model.TenantID) string {
		r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
		}
		uid := r.body["id"].(string)
		r = h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("grant %s = %d %s", email, r.code, r.raw)
		}
		lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
		if lr.code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
		}
		return lr.body["token"].(string)
	}
	editor := mkUser("editor@acme.com", "editorpass1", auth.RoleEditor, tenantA)
	viewer := mkUser("viewer@acme.com", "viewerpass1", auth.RoleViewer, tenantA)

	// Editor creates an agent in A.
	r := h.do("POST", "/v1/agents", editor, map[string]any{"name": "bot", "kind": "claude-code"}, tenantHdr(tenantA))
	if r.code != http.StatusCreated {
		t.Fatalf("editor create agent = %d %s", r.code, r.raw)
	}
	agentID := r.body["id"].(string)

	// Editor cannot act in tenant B (not a member) — forbidden, not "not found".
	if r := h.do("GET", "/v1/agents", editor, nil, tenantHdr(tenantB)); r.code != http.StatusForbidden {
		t.Fatalf("editor in B = %d, want 403", r.code)
	}

	// Viewer can read but not write in A.
	if r := h.do("GET", "/v1/agents", viewer, nil, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("viewer list = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "x", "kind": "y"}, tenantHdr(tenantA)); r.code != http.StatusForbidden {
		t.Fatalf("viewer create = %d, want 403", r.code)
	}

	// Multi-tenant isolation: tenant B (via superadmin) does not see A's agent.
	if r := h.do("GET", "/v1/agents", admin, nil, tenantHdr(tenantB)); r.code != http.StatusOK {
		t.Fatalf("admin list B = %d %s", r.code, r.raw)
	} else if items := r.body["items"].([]any); len(items) != 0 {
		t.Fatalf("tenant B leaked %d agents from A", len(items))
	}

	// Unauthenticated and not-found.
	if r := h.do("GET", "/v1/agents", "", nil, tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Fatalf("no-auth = %d, want 401", r.code)
	}
	if r := h.do("GET", "/v1/agents/"+model.NewID().String(), editor, nil, tenantHdr(tenantA)); r.code != http.StatusNotFound {
		t.Fatalf("missing agent = %d, want 404", r.code)
	}
	// Reading the just-created agent works.
	if r := h.do("GET", "/v1/agents/"+agentID, editor, nil, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("get agent = %d %s", r.code, r.raw)
	}
}

func TestBoundTokenCannotSwitchTenant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// Issue an admin-bound token for A.
	r := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "ci", "tenant": tenantA.String(), "role": auth.RoleAdmin}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", r.code, r.raw)
	}
	tok := r.body["token"].(string)

	// The token works for A with no header (its bound tenant is authoritative).
	if r := h.do("GET", "/v1/agents", tok, nil, nil); r.code != http.StatusOK {
		t.Fatalf("bound token list A = %d %s", r.code, r.raw)
	}
	// A header naming a DIFFERENT tenant is rejected (confused-deputy guard).
	if r := h.do("GET", "/v1/agents", tok, nil, tenantHdr(tenantB)); r.code != http.StatusForbidden {
		t.Fatalf("bound token to B = %d, want 403", r.code)
	}
	// whoami shows a token principal bound to A.
	if r := h.do("GET", "/v1/auth/whoami", tok, nil, nil); r.code != http.StatusOK || r.body["kind"] != "token" {
		t.Fatalf("whoami = %d %v", r.code, r.body)
	}
}

func TestAuditEndpoints(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Generate some audited activity.
	for i := 0; i < 3; i++ {
		if r := h.do("POST", "/v1/agents", admin, map[string]any{"name": "a", "kind": "k"}, tenantHdr(tenant)); r.code != http.StatusCreated {
			t.Fatalf("create = %d %s", r.code, r.raw)
		}
	}
	// Read the ledger.
	if r := h.do("GET", "/v1/audit", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("audit list = %d %s", r.code, r.raw)
	} else if len(r.body["items"].([]any)) == 0 {
		t.Fatal("audit list empty")
	}
	// Checkpoint then verify: chain + checkpoints both OK.
	if _, _, err := h.signer.Checkpoint(context.Background(), h.st, tenant); err != nil {
		t.Fatal(err)
	}
	r := h.do("GET", "/v1/audit/verify", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["ok"] != true {
		t.Fatalf("verify = %d %s", r.code, r.raw)
	}
	cps := r.body["checkpoints"].(map[string]any)
	if cps["ok"] != true || cps["count"].(float64) < 1 {
		t.Fatalf("checkpoints = %v", cps)
	}
	// Export in CEF.
	ex := h.do("GET", "/v1/audit/export?format=cef", admin, nil, tenantHdr(tenant))
	if ex.code != http.StatusOK || len(ex.raw) == 0 {
		t.Fatalf("export = %d", ex.code)
	}
	// Pubkey is available for offline verification.
	if r := h.do("GET", "/v1/audit/pubkey", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["public_key"] == "" {
		t.Fatalf("pubkey = %d %s", r.code, r.raw)
	}
	// OpenAPI document is served.
	if r := h.do("GET", "/openapi.json", "", nil, nil); r.code != http.StatusOK || r.body["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %d %v", r.code, r.body["openapi"])
	}
}
