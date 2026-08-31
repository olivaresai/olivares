// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/consoleviews"
)

type harness struct {
	t        *testing.T
	srv      *api.Server
	setupTok string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	m := consoleviews.New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, srv: srv, setupTok: plaintext}
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func (h *harness) do(method, path, token string, body any, tenant model.TenantID) resp {
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
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func (h *harness) rootLogin() string {
	h.t.Helper()
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, ""); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, "")
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) createOrg(token, slug string) model.TenantID {
	h.t.Helper()
	r := h.do("POST", "/v1/system/orgs", token, map[string]any{"name": slug, "slug": slug}, "")
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org = %d %s", r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

func (h *harness) roleToken(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, "")
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, ""); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, "")
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func view(feature, name string, shared bool) map[string]any {
	return map[string]any{
		"feature_id": feature, "name": name, "shared": shared,
		"params": map[string]any{"q": "deny", "since": "2026-07-01T00:00:00Z"},
	}
}

func items(r resp) []map[string]any {
	raw, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// TestVisibilityOwnVsShared: own views are always visible; foreign views only
// when shared; an unshared foreign view 404s (existence must not leak); the
// mine flag and ?feature_id= filter behave; a viewer can read but not write.
func TestVisibilityOwnVsShared(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	bob := h.roleToken(root, ten, "bob@acme.io", "editor")
	carol := h.roleToken(root, ten, "carol@acme.io", "viewer")

	if r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "private hunt", false), ten); r.code != http.StatusCreated {
		t.Fatalf("create private = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "team hunt", true), ten)
	if r.code != http.StatusCreated {
		t.Fatalf("create shared = %d %s", r.code, r.raw)
	}
	sharedID := r.body["id"].(string)
	privID := ""
	if lr := h.do("GET", "/v1/m/consoleviews/views", alice, nil, ten); len(items(lr)) != 2 {
		t.Fatalf("alice list = %d items %s", len(items(lr)), lr.raw)
	} else {
		for _, it := range items(lr) {
			if it["name"] == "private hunt" {
				privID = it["id"].(string)
			}
			if it["mine"] != true {
				t.Fatalf("alice's views must be mine=true: %v", it)
			}
		}
	}

	// Bob sees only the shared view, marked mine=false.
	lr := h.do("GET", "/v1/m/consoleviews/views", bob, nil, ten)
	got := items(lr)
	if len(got) != 1 || got[0]["name"] != "team hunt" || got[0]["mine"] != false {
		t.Fatalf("bob list = %s", lr.raw)
	}
	if r := h.do("GET", "/v1/m/consoleviews/views/"+sharedID, bob, nil, ten); r.code != http.StatusOK {
		t.Fatalf("bob get shared = %d", r.code)
	}
	if r := h.do("GET", "/v1/m/consoleviews/views/"+privID, bob, nil, ten); r.code != http.StatusNotFound {
		t.Fatalf("bob get private must 404, got %d", r.code)
	}

	// Feature filter.
	if r := h.do("GET", "/v1/m/consoleviews/views?feature_id=observability", alice, nil, ten); len(items(r)) != 0 {
		t.Fatalf("feature filter must exclude audit views: %s", r.raw)
	}

	// Viewer reads but cannot write (write maps to the editor tier).
	if r := h.do("GET", "/v1/m/consoleviews/views", carol, nil, ten); r.code != http.StatusOK {
		t.Fatalf("viewer list = %d", r.code)
	}
	if r := h.do("POST", "/v1/m/consoleviews/views", carol, view("audit", "nope", false), ten); r.code != http.StatusForbidden {
		t.Fatalf("viewer create must 403, got %d", r.code)
	}
}

// TestValidationAndDuplicates: slug/name/params rules 400; same
// (feature, owner, name) 409; another owner may reuse the name.
func TestValidationAndDuplicates(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	bob := h.roleToken(root, ten, "bob@acme.io", "editor")

	bad := []map[string]any{
		{"feature_id": "Audit", "name": "x", "shared": false, "params": map[string]any{}},
		{"feature_id": "audit", "name": "", "shared": false, "params": map[string]any{}},
		{"feature_id": "audit", "name": "x", "shared": false, "params": []any{"not", "object"}},
		{"feature_id": "audit", "name": "x", "shared": false, "params": map[string]any{"pad": strings.Repeat("a", 5000)}},
		{"feature_id": "audit", "name": strings.Repeat("n", 121), "shared": false, "params": map[string]any{}},
	}
	for i, b := range bad {
		if r := h.do("POST", "/v1/m/consoleviews/views", alice, b, ten); r.code != http.StatusBadRequest {
			t.Fatalf("bad[%d] must 400, got %d %s", i, r.code, r.raw)
		}
	}

	if r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "hunt", false), ten); r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "hunt", true), ten); r.code != http.StatusConflict {
		t.Fatalf("duplicate must 409, got %d %s", r.code, r.raw)
	}
	// A different owner may use the same name on the same feature.
	if r := h.do("POST", "/v1/m/consoleviews/views", bob, view("audit", "hunt", false), ten); r.code != http.StatusCreated {
		t.Fatalf("bob same-name create = %d %s", r.code, r.raw)
	}
}

// TestUpdateOwnerOnlyAndImmutableFeature: only the owner edits; feature_id is
// immutable; params round-trip verbatim on update.
func TestUpdateOwnerOnlyAndImmutableFeature(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	bob := h.roleToken(root, ten, "bob@acme.io", "editor")

	r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "hunt", true), ten)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	upd := view("audit", "hunt v2", true)
	upd["params"] = map[string]any{"q": "allow"}
	if r := h.do("PUT", "/v1/m/consoleviews/views/"+id, bob, upd, ten); r.code != http.StatusForbidden {
		t.Fatalf("non-owner update must 403, got %d %s", r.code, r.raw)
	}
	r = h.do("PUT", "/v1/m/consoleviews/views/"+id, alice, upd, ten)
	if r.code != http.StatusOK {
		t.Fatalf("owner update = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "hunt v2" {
		t.Fatalf("update did not apply: %s", r.raw)
	}
	if p, _ := r.body["params"].(map[string]any); p["q"] != "allow" {
		t.Fatalf("params did not round-trip: %s", r.raw)
	}

	moved := view("observability", "hunt v2", true)
	if r := h.do("PUT", "/v1/m/consoleviews/views/"+id, alice, moved, ten); r.code != http.StatusBadRequest {
		t.Fatalf("feature_id change must 400, got %d %s", r.code, r.raw)
	}
}

// TestDeletePowers: owner deletes own; a plain editor cannot delete a foreign
// shared view; a tenant ADMIN can delete any (cleanup); deleted is gone.
func TestDeletePowers(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	bob := h.roleToken(root, ten, "bob@acme.io", "editor")
	admin := h.roleToken(root, ten, "boss@acme.io", "admin")

	r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "keep", true), ten)
	id1 := r.body["id"].(string)
	r = h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "gone", true), ten)
	id2 := r.body["id"].(string)

	if r := h.do("DELETE", "/v1/m/consoleviews/views/"+id1, bob, nil, ten); r.code != http.StatusForbidden {
		t.Fatalf("editor foreign delete must 403, got %d", r.code)
	}
	if r := h.do("DELETE", "/v1/m/consoleviews/views/"+id1, admin, nil, ten); r.code != http.StatusNoContent {
		t.Fatalf("admin delete-any = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/consoleviews/views/"+id2, alice, nil, ten); r.code != http.StatusNoContent {
		t.Fatalf("owner delete = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/consoleviews/views/"+id2, alice, nil, ten); r.code != http.StatusNotFound {
		t.Fatalf("deleted view must 404, got %d", r.code)
	}
}

// TestTenantIsolationAndAudit: views never cross tenants, and every write
// lands in the tenant audit ledger attributed to the principal.
func TestTenantIsolationAndAudit(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	tenA := h.createOrg(root, "acme")
	tenB := h.createOrg(root, "borg")
	alice := h.roleToken(root, tenA, "alice@acme.io", "editor")
	eve := h.roleToken(root, tenB, "eve@borg.io", "editor")

	r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "hunt", true), tenA)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	if r := h.do("GET", "/v1/m/consoleviews/views", eve, nil, tenB); len(items(r)) != 0 {
		t.Fatalf("cross-tenant list leak: %s", r.raw)
	}
	if r := h.do("GET", "/v1/m/consoleviews/views/"+id, eve, nil, tenB); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant get must 404, got %d", r.code)
	}

	// The create is evidenced in tenant A's ledger.
	ar := h.do("GET", "/v1/audit?limit=1000", alice, nil, tenA)
	if ar.code != http.StatusOK {
		t.Fatalf("audit list = %d", ar.code)
	}
	found := false
	for _, it := range items(ar) {
		if it["action"] == "consoleviews.view.create" {
			found = true
		}
	}
	if !found {
		t.Fatalf("consoleviews.view.create not in ledger: %s", ar.raw)
	}
}

// TestPerOwnerCap: the 201st view of one owner refuses with 422 and a clear
// message; the cap is per owner, so another owner can still save.
func TestPerOwnerCap(t *testing.T) {
	h := newHarness(t)
	root := h.rootLogin()
	ten := h.createOrg(root, "acme")
	alice := h.roleToken(root, ten, "alice@acme.io", "editor")
	bob := h.roleToken(root, ten, "bob@acme.io", "editor")

	for i := 0; i < 200; i++ {
		r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", fmt.Sprintf("v%03d", i), false), ten)
		if r.code != http.StatusCreated {
			t.Fatalf("create %d = %d %s", i, r.code, r.raw)
		}
	}
	if r := h.do("POST", "/v1/m/consoleviews/views", alice, view("audit", "v200", false), ten); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("cap breach must 422, got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/consoleviews/views", bob, view("audit", "mine", false), ten); r.code != http.StatusCreated {
		t.Fatalf("cap must be per-owner: %d %s", r.code, r.raw)
	}
}
