// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap_test

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

	accessmap "github.com/olivaresai/olivares/modules/access-map"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	m        *accessmap.Module
	setupTok string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	m := accessmap.New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true},
		func(store.ExtensionRegistry) error { return nil })
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
	return &harness{t: t, srv: srv, st: st, m: m, setupTok: plaintext}
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func (h *harness) do(method, path, token string, hdr map[string]string) resp {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(nil))
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

func (h *harness) post(path, token string, body any) resp {
	h.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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
	if r := h.post("/v1/setup", "", map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}); r.code != http.StatusCreated {
		h.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	r := h.post("/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"})
	if r.code != http.StatusOK {
		h.t.Fatalf("login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) createOrg(token, slug string) model.TenantID {
	h.t.Helper()
	r := h.post("/v1/system/orgs", token, map[string]any{"name": slug, "slug": slug})
	if r.code != http.StatusCreated {
		h.t.Fatalf("create org = %d %s", r.code, r.raw)
	}
	return model.TenantID(r.body["tenant_id"].(string))
}

func (h *harness) viewerToken(admin string, tenant model.TenantID, email string) string {
	return h.memberToken(admin, tenant, email, auth.RoleViewer)
}

func (h *harness) editorToken(admin string, tenant model.TenantID, email string) string {
	return h.memberToken(admin, tenant, email, auth.RoleEditor)
}

// memberToken creates a user, grants it role in tenant, and logs it in.
func (h *harness) memberToken(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.post("/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"})
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.post("/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.post("/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"})
	if r.code != http.StatusOK {
		h.t.Fatalf("%s login = %d %s", role, r.code, r.raw)
	}
	return r.body["token"].(string)
}

func (h *harness) auditCount(tenant model.TenantID) int {
	h.t.Helper()
	n := 0
	_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(model.AuditEvent) error { n++; return nil })
	})
	return n
}

func obs(originKind, originRef, resKind, resRef string, mode sdkmodel.AccessMode, src sdkmodel.SignalSource, conf sdkmodel.Confidence) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: originKind, OriginRef: originRef, ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Source: src, Confidence: conf, ObservedAt: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	}
}

func TestAPI_GraphDriftPrivilegedAuditedIsolated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// Seed the graph for tenant A through the real reactor: an observed access and
	// a matching grant (reconciles), plus an observed access with no grant
	// (unexpected) and a grant with no access (unused).
	ctx := context.Background()
	for _, e := range []sdkmodel.EdgeObservation{
		obs("agent", "agent-1", "postgres.table", "appdb.public.customers", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed),
		obs("agent", "agent-1", "postgres.table", "appdb.public.secrets", sdkmodel.ModeRead, sdkmodel.SignalPGAudit, sdkmodel.ConfidenceAttributed),  // unexpected
		obs("agent", "agent-1", "postgres.table", "appdb.public.customers", sdkmodel.ModeRead, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed), // grant matches first
		obs("agent", "agent-1", "postgres.table", "appdb.public.archive", sdkmodel.ModeWrite, sdkmodel.SignalPolicy, sdkmodel.ConfidenceAttributed),  // unused grant
	} {
		if _, err := h.m.Ingest(ctx, tenantA.String(), e); err != nil {
			t.Fatalf("seed ingest: %v", err)
		}
	}

	// Viewing the access graph is PRIVILEGED (docs/SECURITY-HARDENING.md): the lowest viewer role
	// is refused; an operational (editor) role or higher is required, on top of the
	// per-read self-audit. Prove the viewer is forbidden on both views first.
	viewer := h.viewerToken(admin, tenantA, "v@acme.com")
	if r := h.do("GET", "/v1/m/accessmap/graph", viewer, tenantHdr(tenantA)); r.code != http.StatusForbidden {
		t.Fatalf("viewer graph = %d, want 403 (graph read is privileged)", r.code)
	}
	if r := h.do("GET", "/v1/m/accessmap/drift", viewer, tenantHdr(tenantA)); r.code != http.StatusForbidden {
		t.Fatalf("viewer drift = %d, want 403 (drift read is privileged)", r.code)
	}

	editor := h.editorToken(admin, tenantA, "e@acme.com")

	// /graph returns the React Flow node+edge contract, audited.
	before := h.auditCount(tenantA)
	r := h.do("GET", "/v1/m/accessmap/graph", editor, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("graph = %d %s", r.code, r.raw)
	}
	nodes, _ := r.body["nodes"].([]any)
	edges, _ := r.body["edges"].([]any)
	if len(nodes) == 0 || len(edges) == 0 {
		t.Fatalf("graph empty: nodes=%d edges=%d (%s)", len(nodes), len(edges), r.raw)
	}
	if after := h.auditCount(tenantA); after <= before {
		t.Errorf("graph read did not self-audit: before=%d after=%d", before, after)
	}

	// /drift returns the unexpected access (secrets) and the unused grant (archive).
	r = h.do("GET", "/v1/m/accessmap/drift", editor, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("drift = %d %s", r.code, r.raw)
	}
	if got := jsonNum(r.body["unexpected_count"]); got != 1 {
		t.Errorf("unexpected_count = %v, want 1 (%s)", r.body["unexpected_count"], r.raw)
	}
	if got := jsonNum(r.body["unused_count"]); got != 1 {
		t.Errorf("unused_count = %v, want 1 (%s)", r.body["unused_count"], r.raw)
	}

	// Tenant isolation: editor of A cannot read B (not a member → 403).
	if r := h.do("GET", "/v1/m/accessmap/graph", editor, tenantHdr(tenantB)); r.code != http.StatusForbidden {
		t.Errorf("cross-tenant graph = %d, want 403", r.code)
	}
	// Unauthenticated is rejected by the wrapping middleware.
	if r := h.do("GET", "/v1/m/accessmap/graph", "", tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d, want 401", r.code)
	}
}

func jsonNum(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return -1
}
