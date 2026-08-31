// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

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
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/catalog"
	"github.com/olivaresai/olivares/sdk"
)

// harness wires a real store + API server with the catalog module. When signing
// is requested it starts a runtime so the module's Init loads a catalog signing
// key from config (the real production path).
type harness struct {
	t        *testing.T
	srv      *api.Server
	st       store.Store
	cat      *catalog.Module
	setupTok string
}

func newHarness(t *testing.T, signing bool) *harness {
	t.Helper()
	ctx := context.Background()
	cat := catalog.New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, cat.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	cat.UseData(api.NewModuleData(st))

	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{cat},
	})
	if err != nil {
		t.Fatal(err)
	}

	if signing {
		cfg := sdk.Config{Settings: map[string]string{"signing_key_path": filepath.Join(t.TempDir(), "catalog-signing.key")}}
		rt := runtime.New(runtime.Options{})
		if err := rt.AddModule(cat, cfg); err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rt.Stop(ctx) })
	}
	return &harness{t: t, srv: srv, st: st, cat: cat, setupTok: plaintext}
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

func items(r resp) []any {
	if r.body == nil {
		return nil
	}
	out, _ := r.body["items"].([]any)
	return out
}

// mcpEntry returns a valid draft catalog entry body for an MCP server template.
func mcpEntry(slug, version string) map[string]any {
	return map[string]any{
		"kind":      "mcp",
		"name":      "GitHub MCP",
		"slug":      slug,
		"version":   version,
		"summary":   "Approved GitHub MCP server template",
		"owner_ref": "platform-team",
		"spec": map[string]any{
			"transport":   "stdio",
			"endpoint":    "npx -y @modelcontextprotocol/server-github",
			"secret_refs": []map[string]any{{"name": "GITHUB_TOKEN", "ref_kind": "env", "ref": "$GITHUB_TOKEN"}},
		},
	}
}

// createApproved creates and approves an entry, returning its id.
func (h *harness) createApproved(editor, admin string, tenant model.TenantID, slug, version string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry(slug, version), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create entry = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		h.t.Fatalf("approve entry = %d %s", r.code, r.raw)
	}
	return id
}
