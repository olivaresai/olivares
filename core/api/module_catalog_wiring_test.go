// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// mounting a module is what makes its permissions grantable. Every catalog test in
// core/auth registers by hand; only this file proves the ENGINE does it, which is the
// difference between a feature and a feature nobody wired.

// catalogModule declares two permissions and mounts routes for both.
type catalogModule struct{}

func (catalogModule) APINamespace() string { return "catdemo" }
func (catalogModule) Permissions() []auth.Permission {
	return []auth.Permission{"catdemo:thing:read", "catdemo:thing:write"}
}
func (catalogModule) APIRoutes(reg api.RouteRegistrar) {
	noop := func(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) { w.WriteHeader(http.StatusNoContent) }
	reg.Handle("GET", "/things", "catdemo:thing:read", noop)
	reg.Handle("POST", "/things", "catdemo:thing:write", noop)
}

// undeclaredRouteModule mounts a route requiring a permission it never declares — the
// exact shape governance shipped for five agent-risk routes until.
type undeclaredRouteModule struct{}

func (undeclaredRouteModule) APINamespace() string { return "undecl" }
func (undeclaredRouteModule) Permissions() []auth.Permission {
	return []auth.Permission{"undecl:thing:read"}
}
func (undeclaredRouteModule) APIRoutes(reg api.RouteRegistrar) {
	noop := func(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {}
	reg.Handle("GET", "/things", "undecl:thing:read", noop)
	reg.Handle("PUT", "/things/{id}", "undecl:thing:admin", noop) // never declared
}

// coreDeclaringModule tries to declare a CORE permission, which would let a module widen
// the code-defined core catalog.
type coreDeclaringModule struct{}

func (coreDeclaringModule) APINamespace() string           { return "coredecl" }
func (coreDeclaringModule) Permissions() []auth.Permission { return []auth.Permission{"agent:read"} }
func (coreDeclaringModule) APIRoutes(api.RouteRegistrar)   {}

func newServerWith(t *testing.T, mods ...api.Module) (*api.Server, error) {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	return api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: tok, Modules: mods,
	})
}

func TestMountRegistersModulePermissions(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	// Before the mount the permissions are not grantable...
	if auth.IsGrantablePermission("catdemo:thing:read") {
		t.Fatal("fixture broken: the permission is grantable before any mount")
	}
	if _, err := newServerWith(t, catalogModule{}); err != nil {
		t.Fatalf("server build: %v", err)
	}
	// ...and after it they are. This is the wiring the whole unit depends on.
	for _, p := range []auth.Permission{"catdemo:thing:read", "catdemo:thing:write"} {
		if !auth.IsGrantablePermission(p) {
			t.Errorf("mounting the module must make %q grantable", p)
		}
	}
	if !auth.IsScopeableKind("catdemo:thing") {
		t.Error("mounting must register the module permission KIND")
	}
	// A tenant admin's projected domain must carry them, or the delegation ceiling
	// rejects every grant that names one.
	var sawRead, sawWrite bool
	for _, p := range auth.RoleResourcePerms(auth.RoleAdmin) {
		switch p {
		case "catdemo:thing:read":
			sawRead = true
		case "catdemo:thing:write":
			sawWrite = true
		}
	}
	if !sawRead || !sawWrite {
		t.Error("an admin's RoleResourcePerms must carry the mounted module's read+write")
	}
}

func TestMountRebuildsTheCatalogRatherThanAccumulating(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	if _, err := newServerWith(t, catalogModule{}); err != nil {
		t.Fatalf("first server: %v", err)
	}
	if !auth.IsGrantablePermission("catdemo:thing:read") {
		t.Fatal("first mount did not register")
	}
	// A second server with a DIFFERENT module set must not leave the first set grantable:
	// the catalog describes the mounted engine, not the history of the process.
	if _, err := newServerWith(t, demoModule{}); err != nil {
		t.Fatalf("second server: %v", err)
	}
	if auth.IsGrantablePermission("catdemo:thing:read") {
		t.Error("mounting a new module set must rebuild the catalog, not accumulate the old one")
	}
	if !auth.IsGrantablePermission("demo:thing:read") {
		t.Error("the second module set must be registered")
	}
}

func TestMountRejectsUndeclaredRoutePermission(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	_, err := newServerWith(t, undeclaredRouteModule{})
	if err == nil {
		t.Fatal("a route requiring an undeclared permission must fail the mount: it can never be granted or delegated")
	}
	if !strings.Contains(err.Error(), "undecl:thing:admin") {
		t.Errorf("the error must name the offending permission, got: %v", err)
	}
}

func TestMountRejectsAModuleDeclaringACorePermission(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	_, err := newServerWith(t, coreDeclaringModule{})
	if err == nil {
		t.Fatal("a module must not be able to declare a CORE permission")
	}
	// Assert the CLAUSE, not just that something failed: a core-form permission is also
	// caught by the kind-shape check, so "err != nil" cannot tell the two apart.
	if !strings.Contains(err.Error(), "agent:read") || !strings.Contains(err.Error(), "is a CORE permission") {
		t.Errorf("the error must name the offending permission AND why, got: %v", err)
	}
}
