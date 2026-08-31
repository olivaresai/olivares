// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
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

// demoModule is a minimal Module used to prove the route/permission/data seam:
// it mounts one route that requires a namespaced permission and reads tenant-scoped
// data through the least-privilege ModuleData handle.
type demoModule struct{}

func (demoModule) APINamespace() string { return "demo" }

func (demoModule) Permissions() []auth.Permission { return []auth.Permission{"demo:thing:read"} }

func (demoModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/things", "demo:thing:read", func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		// Prove the data seam works (and is tenant-scoped) by counting agents.
		count := 0
		err := mc.Data.View(r.Context(), func(sc store.Scope) error {
			agents, _, e := sc.Agents().List(r.Context(), model.Query{})
			count = len(agents)
			return e
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant": mc.Tenant.String(), "actor": mc.Principal.Actor(), "agent_count": count,
		})
	})
}

func TestModuleSeam(t *testing.T) {
	h := newHarness(t, demoModule{})
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// A viewer of A gets the module's read permission by verb tier.
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": "v@acme.com", "password": "viewerpass1"}, nil)
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenantA.String(), "role": auth.RoleViewer}, nil); r.code != http.StatusCreated {
		t.Fatalf("grant = %d %s", r.code, r.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "v@acme.com", "password": "viewerpass1"}, nil)
	viewer := lr.body["token"].(string)

	// The module route is mounted under /v1/m/demo/ and is reachable with authz.
	if r := h.do("GET", "/v1/m/demo/things", viewer, nil, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("module route = %d %s", r.code, r.raw)
	} else if r.body["tenant"] != tenantA.String() {
		t.Fatalf("module ctx tenant = %v", r.body["tenant"])
	}

	// The viewer cannot reach the module route in a tenant it is not a member of.
	if r := h.do("GET", "/v1/m/demo/things", viewer, nil, tenantHdr(tenantB)); r.code != http.StatusForbidden {
		t.Fatalf("module route in B = %d, want 403", r.code)
	}

	// Unauthenticated access is rejected by the wrapping middleware.
	if r := h.do("GET", "/v1/m/demo/things", "", nil, tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Fatalf("module route no-auth = %d, want 401", r.code)
	}
}

// badModule claims a reserved API segment; building a server with it must fail.
type badModule struct{}

func (badModule) APINamespace() string             { return "auth" } // reserved
func (badModule) Permissions() []auth.Permission   { return nil }
func (badModule) APIRoutes(reg api.RouteRegistrar) {}

func TestModuleReservedNamespaceRejected(t *testing.T) {
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	_, err = api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Modules: []api.Module{badModule{}},
	})
	if err == nil {
		t.Fatal("a module claiming the reserved 'auth' namespace must be rejected")
	}
}
