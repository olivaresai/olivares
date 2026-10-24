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
	"sync/atomic"
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
	if r := h.do("POST", "/v1/users", admin, map[string]any{"email": "v@acme.com", "password": "viewerpass1", "tenant": tenantA.String(), "role": auth.RoleViewer}, nil); r.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", r.code, r.raw)
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

// A module the node can turn on without a restart: the routes stay mounted and
// every request asks the live answer, so enabling it needs no rebuild of the server.
func TestModuleAvailabilityFollowsTheLiveAnswer(t *testing.T) {
	var enabled atomic.Bool
	h := newHarnessOpts(t, func(opts *api.Options) {
		opts.ModuleEnabled = func(namespace string) bool { return namespace != "demo" || enabled.Load() }
	}, demoModule{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "live-module")
	status := func() int { return h.do("GET", "/v1/m/demo/things", admin, nil, tenantHdr(tenant)).code }
	notEnabled := func() []any {
		list, _ := h.do("GET", "/v1/server-info", "", nil, nil).body["modules_not_enabled"].([]any)
		return list
	}

	if got := status(); got != http.StatusNotFound {
		t.Fatalf("disabled module = %d, want 404 module_not_enabled", got)
	}
	if got := notEnabled(); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("server-info modules_not_enabled = %v, want [demo]", got)
	}
	enabled.Store(true)
	if got := status(); got != http.StatusOK {
		t.Fatalf("enabled module = %d, want 200", got)
	}
	if got := notEnabled(); len(got) != 0 {
		t.Fatalf("server-info modules_not_enabled = %v, want none", got)
	}
	if got := h.do("GET", "/v1/m/demo/things", "", nil, tenantHdr(tenant)).code; got != http.StatusUnauthorized {
		t.Fatalf("enabled module without authentication = %d, want 401", got)
	}
	enabled.Store(false)
	if got := status(); got != http.StatusNotFound {
		t.Fatalf("disabled again = %d, want 404", got)
	}
}

// A module this node does not run is not reachable, listed or published, whoever asks and
// however the module was named: through the live answer or through the fixed list.
func TestDisabledModuleIsHiddenFromEveryone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*api.Options)
	}{
		{"live answer", func(o *api.Options) { o.ModuleEnabled = func(ns string) bool { return ns != "demo" } }},
		{"fixed list", func(o *api.Options) { o.NotEnabledModules = []string{"demo"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOpts(t, tc.configure, demoModule{})
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "hidden-module")
			// Before authentication: the gate runs first, so a caller learns nothing more
			// than the module is off.
			if r := h.do("GET", "/v1/m/demo/things", "", nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
				t.Fatalf("unauthenticated request to a disabled module = %d, want 404", r.code)
			}
			if r := h.do("GET", "/v1/m/demo/things", admin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
				t.Fatalf("authenticated request to a disabled module = %d, want 404", r.code)
			}
			doc := h.do("GET", "/openapi.beta.json", "", nil, nil)
			if paths, _ := doc.body["paths"].(map[string]any); len(paths) != 0 {
				t.Fatalf("the beta document publishes the routes of a module this node does not run: %v", paths)
			}
			info := h.do("GET", "/v1/server-info", "", nil, nil)
			if list, _ := info.body["modules_not_enabled"].([]any); len(list) != 1 || list[0] != "demo" {
				t.Fatalf("server-info modules_not_enabled = %v, want [demo]", list)
			}
		})
	}
}

// With every module running the document publishes the module's routes.
func TestRunningModuleIsPublished(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.ModuleEnabled = func(string) bool { return true } }, demoModule{})
	doc := h.do("GET", "/openapi.beta.json", "", nil, nil)
	paths, _ := doc.body["paths"].(map[string]any)
	if _, ok := paths["/v1/m/demo/things"]; !ok {
		t.Fatalf("the document does not publish /v1/m/demo/things: %v", paths)
	}
}

// eventModule has no routes of its own, like a module that only consumes events.
type eventModule struct{}

func (eventModule) APINamespace() string           { return "eventdemo" }
func (eventModule) Permissions() []auth.Permission { return nil }
func (eventModule) APIRoutes(api.RouteRegistrar)   {}

// A module without routes of its own answers its namespace with module_not_enabled while
// it is off, as every other module does; running, it has no route to answer.
func TestRoutelessModuleAnswersNotEnabled(t *testing.T) {
	answer := func(h *harness, path string) (int, any, any) {
		r := h.do("GET", path, "", nil, nil)
		errBody, _ := r.body["error"].(map[string]any)
		return r.code, errBody["code"], errBody["module"]
	}
	for _, tc := range []struct {
		name      string
		configure func(*api.Options)
	}{
		{"live answer", func(o *api.Options) { o.ModuleEnabled = func(ns string) bool { return ns != "eventdemo" } }},
		{"fixed list", func(o *api.Options) { o.NotEnabledModules = []string{"eventdemo"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOpts(t, tc.configure, eventModule{})
			h.adminLogin() // past setup, which answers every route until it is done
			for _, path := range []string{"/v1/m/eventdemo", "/v1/m/eventdemo/anything"} {
				if code, errCode, module := answer(h, path); code != http.StatusNotFound || errCode != "module_not_enabled" || module != "eventdemo" {
					t.Errorf("GET %s = %d %v %v, want 404 module_not_enabled eventdemo", path, code, errCode, module)
				}
			}
		})
	}
	h := newHarnessOpts(t, func(o *api.Options) { o.ModuleEnabled = func(string) bool { return true } }, eventModule{})
	h.adminLogin()
	if code, errCode, _ := answer(h, "/v1/m/eventdemo"); code != http.StatusNotFound || errCode != "not_found" {
		t.Errorf("GET /v1/m/eventdemo while it runs = %d %v, want 404 not_found", code, errCode)
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
