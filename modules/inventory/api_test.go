// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory_test

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
	"github.com/olivaresai/olivares/modules/inventory"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// fakeSource is an in-test source connector that emits a scripted batch of
// observations shaped exactly like the contract, then returns. It exercises
// the full path connector → runtime sink → bus → module → store, so the module
// is proven to load via the runtime and persist what it consumes.
type fakeSource struct {
	edges []sdkmodel.EdgeObservation
	costs []sdkmodel.CostSample
}

func (f *fakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.inventory-source", Version: "0.0.1", APIVersion: sdk.APIVersion, Type: sdk.TypeSource}
}
func (f *fakeSource) Open(context.Context, sdk.Config) error { return nil }
func (f *fakeSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for _, e := range f.edges {
		if err := sink.Emit(ctx, e); err != nil {
			return err
		}
	}
	for _, c := range f.costs {
		if err := sink.Emit(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeSource) Close(context.Context) error { return nil }

func mkEdge(originKind, originRef, resKind, resRef string, mode sdkmodel.AccessMode, src sdkmodel.SignalSource, tool string) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: originKind, OriginRef: originRef, ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Source: src, Confidence: sdkmodel.ConfidenceAttributed, ToolRef: tool,
		ObservedAt: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	}
}

// harness wires a real store, the api server with the inventory module mounted,
// and the auth bootstrap.
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	setupTok string
}

func newHarness(t *testing.T, m *inventory.Module) *harness {
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

// viewerToken creates a viewer of tenant and returns its session token.
func (h *harness) viewerToken(admin string, tenant model.TenantID, email string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "viewerpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": auth.RoleViewer}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "viewerpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("viewer login = %d %s", r.code, r.raw)
	}
	return r.body["token"].(string)
}

// waitCatalog polls the store until the tenant has at least n catalog entries.
func (h *harness) waitCatalog(tenant model.TenantID, n int) {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		count := 0
		_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
			repo, err := sc.Ext("inventory.catalog_entry")
			if err != nil {
				return err
			}
			recs, _, err := repo.List(context.Background(), model.Query{Limit: 1000})
			count = len(recs)
			return err
		})
		if count >= n {
			return
		}
		select {
		case <-deadline:
			h.t.Fatalf("catalog reached %d entries, want >= %d", count, n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestEndToEndDiscoveryAndAPI(t *testing.T) {
	m := inventory.New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// Drive the module through the real runtime + bus from a source connector
	// configured for tenant A.
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{
		edges: []sdkmodel.EdgeObservation{
			mkEdge("session", "sess-1", "file", "/etc/app/config.yaml", sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read"),
			mkEdge("session", "sess-1", "mcp.tool", "github/create_issue", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, "mcp__github__create_issue"),
			mkEdge("mcp_server", "github", "mcp.tool", "github/create_issue", sdkmodel.ModeReadWrite, sdkmodel.SignalMCPAnnotation, "create_issue"),
		},
		costs: []sdkmodel.CostSample{{
			ProviderRef: "anthropic", ModelRef: "claude-opus-4-8", SessionRef: "sess-1",
			InputTokens: 10, OutputTokens: 5, CostMicroUSD: 99, OccurredAt: time.Now(),
		}},
	}
	if err := rt.AddSource(src, sdk.Config{}, tenantA.String()); err != nil {
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

	// session, mcp_server, tool, resource, provider, model => >= 6 entries.
	h.waitCatalog(tenantA, 6)

	viewer := h.viewerToken(admin, tenantA, "v@acme.com")

	// Summary reflects the discovered estate.
	r := h.do("GET", "/v1/m/inventory/summary", viewer, nil, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("summary = %d %s", r.code, r.raw)
	}
	byKind, _ := r.body["by_kind"].(map[string]any)
	if byKind["session"] == nil || byKind["mcp_server"] == nil || byKind["model"] == nil {
		t.Fatalf("summary missing kinds: %v", r.body["by_kind"])
	}

	// List sessions and read the session's id back.
	r = h.do("GET", "/v1/m/inventory/entities?kind=session", viewer, nil, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("list sessions = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want 1 session entry, got %d (%s)", len(items), r.raw)
	}
	entry := items[0].(map[string]any)
	sessionID := entry["entity_id"].(string)
	if entry["status"] != "active" {
		t.Errorf("session status = %v", entry["status"])
	}

	// Entity detail surfaces the core session.
	r = h.do("GET", "/v1/m/inventory/entities/session/"+sessionID, viewer, nil, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("entity detail = %d %s", r.code, r.raw)
	}
	if d, _ := r.body["detail"].(map[string]any); d["external_id"] != "sess-1" {
		t.Errorf("detail external_id = %v", d)
	}

	// (The R/RW access graph — topology, drift, unexpected accesses — is module
	// III, /modules/access-map, and is exercised by that module's own suite.)

	// Tenant isolation: the viewer of A is not a member of B → 403, and B's
	// catalog is empty anyway.
	if r := h.do("GET", "/v1/m/inventory/summary", viewer, nil, tenantHdr(tenantB)); r.code != http.StatusForbidden {
		t.Errorf("cross-tenant summary = %d, want 403", r.code)
	}
	// Unauthenticated access is rejected by the wrapping middleware.
	if r := h.do("GET", "/v1/m/inventory/summary", "", nil, tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d, want 401", r.code)
	}
}
