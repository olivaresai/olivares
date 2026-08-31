// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// fakeSource emits a scripted batch of cost samples, exercising the full path
// connector → runtime sink → bus → module → store so the module is proven to load
// via the runtime and enrich what it consumes.
type fakeSource struct{ costs []sdkmodel.CostSample }

func (f *fakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.models-source", Version: "0.0.1", APIVersion: sdk.APIVersion, Type: sdk.TypeSource}
}
func (f *fakeSource) Open(context.Context, sdk.Config) error { return nil }
func (f *fakeSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for _, c := range f.costs {
		if err := sink.Emit(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeSource) Close(context.Context) error { return nil }

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	setupTok string
}

func newHarness(t *testing.T, m *models.Module) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))

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
	return &harness{t: t, srv: srv, st: st, setupTok: plaintext}
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

func (h *harness) adminLogin() string {
	h.t.Helper()
	if r := h.do("POST", "/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
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

// roleToken creates a user with role of tenant and returns its session token.
func (h *harness) roleToken(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) waitModels(tenant model.TenantID, n int) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		count := 0
		_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			ms, _, err := sc.Models().List(context.Background(), model.Query{Limit: 100})
			count = len(ms)
			return err
		})
		if count >= n {
			return
		}
		select {
		case <-deadline:
			h.t.Fatalf("estate reached %d models, want >= %d", count, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestCatalogAndFeatures(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	// The declared reference catalog is readable by a viewer.
	r := h.do("GET", "/v1/m/models/catalog", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	mods, _ := r.body["models"].([]any)
	if len(mods) == 0 {
		t.Fatal("catalog has no models")
	}
	if r.body["pricing_as_of"] == nil {
		t.Error("catalog missing pricing_as_of provenance")
	}

	// The capability matrix lists families per feature.
	r = h.do("GET", "/v1/m/models/features", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("features = %d %s", r.code, r.raw)
	}
}

func TestRoutingPolicyLifecycleAndResolve(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	// Populate the governed estate through the real runtime + bus.
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{costs: []sdkmodel.CostSample{
		{ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", InputTokens: 10, OutputTokens: 5, CostMicroUSD: 99, OccurredAt: time.Now()},
		{ProviderRef: "google", ModelRef: "gemini-1.5-flash", InputTokens: 10, OutputTokens: 5, CostMicroUSD: 1, OccurredAt: time.Now()},
	}}
	if err := rt.AddSource(src, sdk.Config{}, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	h.waitModels(tenant, 2)

	// A viewer may not create a policy.
	if r := h.do("POST", "/v1/m/models/routing-policies", viewer, map[string]any{"name": "x", "strategy": "cost"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer create = %d, want 403", r.code)
	}

	// An editor creates a cost-routing policy.
	r := h.do("POST", "/v1/m/models/routing-policies", editor, map[string]any{
		"name": "cheap-default", "enabled": true, "strategy": "cost",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create policy = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r.body["strategy"] != "cost" {
		t.Errorf("strategy = %v", r.body["strategy"])
	}

	// List and get.
	if r := h.do("GET", "/v1/m/models/routing-policies", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}

	// Resolve picks the cheapest governed model (gemini-1.5-flash).
	r = h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("resolve = %d %s", r.code, r.raw)
	}
	if r.body["resolved"] != true {
		t.Fatalf("resolve not resolved: %s", r.raw)
	}
	primary, _ := r.body["primary"].(map[string]any)
	if primary["model_ref"] != "gemini-1.5-flash" {
		t.Errorf("resolved primary = %v, want gemini-1.5-flash", primary["model_ref"])
	}

	// Delete.
	if r := h.do("DELETE", "/v1/m/models/routing-policies/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Errorf("delete = %d %s", r.code, r.raw)
	}
}

// TestResolveModelAccessForbidPreview proves the resolve model-access PREVIEW
// end-to-end: a tenant-wide forbid on the caller's role drops the forbidden model from
// the read-only resolve (no acting session needed), promoting the survivor; forbidding
// every candidate returns 403. It also exercises the `effect` field through the REST
// authoring path.
func TestResolveModelAccessForbidPreview(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	// Governed estate: a cheap gemini and a pricier opus.
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{costs: []sdkmodel.CostSample{
		{ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", InputTokens: 10, OutputTokens: 5, CostMicroUSD: 99, OccurredAt: time.Now()},
		{ProviderRef: "google", ModelRef: "gemini-1.5-flash", InputTokens: 10, OutputTokens: 5, CostMicroUSD: 1, OccurredAt: time.Now()},
	}}
	if err := rt.AddSource(src, sdk.Config{}, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	h.waitModels(tenant, 2)

	r := h.do("POST", "/v1/m/models/routing-policies", editor, map[string]any{
		"name": "cheap-default", "enabled": true, "strategy": "cost",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create policy = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	// Baseline: cost strategy picks the cheapest (gemini).
	r = h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("baseline resolve = %d %s", r.code, r.raw)
	}
	if primary, _ := r.body["primary"].(map[string]any); primary["model_ref"] != "gemini-1.5-flash" {
		t.Fatalf("baseline primary = %v, want gemini-1.5-flash", primary["model_ref"])
	}

	// A tenant-wide forbid on role viewer for gemini (authored via REST, effect=forbid).
	if r := h.do("POST", "/v1/m/models/model-access", admin, map[string]any{
		"subject_kind": "role", "subject_ref": auth.RoleViewer, "target_kind": "model",
		"target_ref": "gemini-1.5-flash", "effect": "forbid",
	}, tenantHdr(tenant)); r.code != http.StatusCreated || r.body["effect"] != "forbid" {
		t.Fatalf("create forbid = %d %s", r.code, r.raw)
	}

	// /resolve now drops gemini (tenant-wide forbid is decidable without a session) and
	// promotes opus.
	r = h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["resolved"] != true {
		t.Fatalf("post-forbid resolve = %d %s", r.code, r.raw)
	}
	if primary, _ := r.body["primary"].(map[string]any); primary["model_ref"] != "claude-opus-4-8" {
		t.Errorf("post-forbid primary = %v, want claude-opus-4-8 (gemini forbidden)", primary["model_ref"])
	}

	// Forbid the remaining candidate too ⇒ the whole preview is denied (403).
	if r := h.do("POST", "/v1/m/models/model-access", admin, map[string]any{
		"subject_kind": "role", "subject_ref": auth.RoleViewer, "target_kind": "model",
		"target_ref": "claude-opus-4-8", "effect": "forbid",
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("create second forbid = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/m/models/routing-policies/"+id+"/resolve", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Errorf("all-forbidden resolve = %d, want 403 (%s)", r.code, r.raw)
	}
}

func TestKeyGovernanceMinimalData(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	// A viewer cannot register a key reference.
	if r := h.do("POST", "/v1/m/models/keys", viewer, map[string]any{"ref_kind": "api_key", "provider_ref": "anthropic", "ext_id": "k1"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer create key = %d, want 403", r.code)
	}

	// An editor registers a key reference (metadata only, masked hint).
	r := h.do("POST", "/v1/m/models/keys", editor, map[string]any{
		"ref_kind": "api_key", "provider_ref": "anthropic", "ext_id": "apikey_01",
		"name": "ci-bot key", "status": "active", "hint": "sk-ant-…aB12", "owner_ref": "team:ci",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create key = %d %s", r.code, r.raw)
	}
	if r.body["hint"] != "sk-ant-…aB12" {
		t.Errorf("hint = %v", r.body["hint"])
	}

	// A full credential pasted into the hint is rejected (minimal-data guard).
	long := "sk-ant-api03-this-is-a-very-long-value-that-looks-like-a-full-secret-aaaaaaaa"
	if r := h.do("POST", "/v1/m/models/keys", editor, map[string]any{
		"ref_kind": "api_key", "provider_ref": "anthropic", "ext_id": "apikey_02", "hint": long,
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("long hint = %d, want 400", r.code)
	}

	// A bad ref_kind is rejected.
	if r := h.do("POST", "/v1/m/models/keys", editor, map[string]any{"ref_kind": "secret", "provider_ref": "x", "ext_id": "y"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("bad ref_kind = %d, want 400", r.code)
	}

	// The viewer can read the inventory.
	r = h.do("GET", "/v1/m/models/keys?ref_kind=api_key", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list keys = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Errorf("want 1 key ref, got %d", len(items))
	}

	// The credential-governance mutation is self-audited to the REAL principal
	// (the editor), not the system actor — docs/SECURITY-HARDENING.md.
	found := false
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			if e.Action == "models.key_ref.create" {
				found = true
				if e.ActorKind != "user" || e.Actor == "system" {
					t.Errorf("key_ref.create audited as %s/%s, want a real user", e.Actor, e.ActorKind)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("audit walk: %v", err)
	}
	if !found {
		t.Error("no audit event recorded for the key creation")
	}
}

// /catalog and /features listed a table ROW per entry, not a FAMILY. Two families
// need a second row because one of their id forms does not start with the alias
// prefix, and the catalog DTO does not project Prefix — so the caller received two
// byte-identical objects it could neither tell apart nor justify, and every one of
// the 13 capability rows named those families twice. The console keys its rows on
// family, so it painted duplicate rows under duplicate React keys.
//
// The test that covered these two routes asserted 200 and "not empty", which is why
// this was green from the day it was written.
func TestCatalogAndFeaturesEmitEachFamilyOnce(t *testing.T) {
	m := models.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	r := h.do("GET", "/v1/m/models/catalog", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	mods, _ := r.body["models"].([]any)
	if len(mods) == 0 {
		t.Fatal("catalog has no models")
	}
	seen := map[string]int{}
	for _, it := range mods {
		e, _ := it.(map[string]any)
		fam, _ := e["family"].(string)
		seen[fam]++
	}
	for fam, n := range seen {
		if n > 1 {
			t.Errorf("/catalog emits family %q %d times: the DTO carries no field that tells the copies apart", fam, n)
		}
	}

	r = h.do("GET", "/v1/m/models/features", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("features = %d %s", r.code, r.raw)
	}
	caps, _ := r.body["capabilities"].([]any)
	if len(caps) == 0 {
		t.Fatal("features has no capability rows")
	}
	for _, row := range caps {
		rw, _ := row.(map[string]any)
		name, _ := rw["capability"].(string)
		fams, _ := rw["families"].([]any)
		in := map[string]int{}
		for _, f := range fams {
			s, _ := f.(string)
			in[s]++
		}
		for fam, n := range in {
			if n > 1 {
				t.Errorf("/features lists family %q %d times under capability %q", fam, n, name)
			}
		}
	}
}
