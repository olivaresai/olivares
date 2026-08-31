// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/eventing"
)

// the conformance test for the scope-grantable permission catalog, run against the
// REAL module set this binary ships (buildModules), not a fixture.
//
// It is the anti-regression for P-4's finding: on 2026-08-06 the engine mounted 656 module
// routes and 0 of its 195 module permissions could be conferred by a custom role or a
// scoped grant, because the catalog was exactly the 15 core kinds of the tree.
//
// Division of labor with the other two layers, so none of them is mistaken for the whole:
//   - core/auth/module_catalog_test.go proves the catalog's RULES on hand-registered perms;
//   - core/api/module_catalog_wiring_test.go proves the ENGINE registers at mount;
//   - this file proves the real module set SATISFIES the rules — a fixture cannot.

type catalogRoute struct {
	method, pattern string
	perm            auth.Permission
	ns              string
}

type catalogRecorder struct {
	ns  string
	out *[]catalogRoute
}

func (r catalogRecorder) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}

func (r catalogRecorder) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	*r.out = append(*r.out, catalogRoute{method: method, pattern: pattern, perm: perm, ns: r.ns})
}

func liveModules(t *testing.T) moduleSet {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, k, _ := ed25519.GenerateKey(nil)
	sg, err := audit.NewSigner(k)
	if err != nil {
		t.Fatal(err)
	}
	set, err := buildModules(sg, nil, nil, nil, http.DefaultClient, sourcesConfig{}, log)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// TestEveryRoutePermissionIsDeclared: an undeclared route permission never reaches the
// catalog, so its route can be neither delegated to a scoped admin nor excluded by a
// custom role — it rides the module verb tier alone, invisibly. mountModules rejects this
// at boot; this test names the offenders in the real set instead of failing a server build.
func TestEveryRoutePermissionIsDeclared(t *testing.T) {
	for _, m := range liveModules(t).all {
		declared := map[auth.Permission]bool{}
		for _, p := range m.Permissions() {
			declared[p] = true
		}
		var routes []catalogRoute
		m.APIRoutes(catalogRecorder{ns: m.APINamespace(), out: &routes})
		var missing []string
		seen := map[auth.Permission]bool{}
		for _, r := range routes {
			if !declared[r.perm] && !seen[r.perm] {
				seen[r.perm] = true
				missing = append(missing, string(r.perm))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("module %q mounts routes requiring undeclared permissions %v", m.APINamespace(), missing)
		}
	}
}

// TestEveryModulePermissionIsDelegable is the measurement P-4 turned on its head: it was
// 0 of 195, and it must now be all of them.
func TestEveryModulePermissionIsDelegable(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	set := liveModules(t)
	all := map[auth.Permission]bool{}
	for _, m := range set.all {
		if err := auth.RegisterModulePermissions(m.Permissions()); err != nil {
			t.Fatalf("module %q declares a permission the catalog rejects: %v", m.APINamespace(), err)
		}
		for _, p := range m.Permissions() {
			all[p] = true
		}
		var routes []catalogRoute
		m.APIRoutes(catalogRecorder{ns: m.APINamespace(), out: &routes})
		for _, r := range routes {
			all[r.perm] = true
		}
	}
	// Routes are not the only enforcement point. The eventing per-event RBAC filter gates
	// DELIVERY of each event type on a permission (modules/eventing/catalog.go), and one of
	// those — security:observed:read — was declared by no module at all, so it never
	// reached the registry and stayed undelegable while a declared-only universe reported
	// every permission covered. Enumerating the event catalog here is what makes that class
	// visible instead of leaving it to be rediscovered one string at a time.
	for _, e := range eventing.Catalog() {
		if p := auth.Permission(e.Permission); p.IsModule() {
			all[p] = true
		}
	}
	if len(all) == 0 {
		t.Fatal("no module permissions found — the harness is not exercising the real set")
	}
	var undelegable []string
	for p := range all {
		if !auth.IsGrantablePermission(p) {
			undelegable = append(undelegable, string(p))
		}
	}
	if len(undelegable) > 0 {
		sort.Strings(undelegable)
		t.Errorf("%d of %d module permissions are still undelegable: %v", len(undelegable), len(all), undelegable)
	}
	// A tenant OWNER must be able to delegate every one of them, or the ceiling makes the
	// authoring surface a 403 generator.
	owner := map[auth.Permission]bool{}
	for _, p := range auth.RoleResourcePerms(auth.RoleOwner) {
		owner[p] = true
	}
	var unreachable []string
	for p := range all {
		if !owner[p] {
			unreachable = append(unreachable, string(p))
		}
	}
	if len(unreachable) > 0 {
		sort.Strings(unreachable)
		t.Errorf("%d module permissions are outside a tenant owner's delegation domain: %v", len(unreachable), unreachable)
	}
}

// TestMiddleSegmentTrapIsNotHowTheCatalogDecides pins the false-positive class directly on
// the live set: several real module permissions carry a CORE kind as their middle segment,
// so a catalog that split on that segment would report them grantable with nothing
// registered at all.
func TestMiddleSegmentTrapIsNotHowTheCatalogDecides(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	set := liveModules(t)
	// Deduplicated: the route-only console modules legitimately declare another module's
	// permission, so a raw append would double-count (governance:identity:read appears in
	// both the governance module and the identity console).
	seen := map[string]bool{}
	var trapped []string
	for _, m := range set.all {
		for _, p := range m.Permissions() {
			if p.IsModule() && auth.IsTreeScopeableKind(p.Resource()) && !seen[string(p)] {
				seen[string(p)] = true
				trapped = append(trapped, string(p))
			}
		}
	}
	if len(trapped) == 0 {
		t.Fatal("the live set no longer contains a module permission whose middle segment is a core kind: this test is no longer exercising the trap")
	}
	sort.Strings(trapped)
	for _, p := range trapped {
		if auth.IsGrantablePermission(auth.Permission(p)) {
			t.Errorf("%q is grantable with an EMPTY registry: the catalog is splitting on the middle segment", p)
		}
		kind, _, ok := auth.SplitPermission(auth.Permission(p))
		if !ok || !strings.Contains(kind, ":") {
			t.Errorf("%q: kind %q must be <ns>:<res>", p, kind)
		}
	}
	t.Logf("trap set on the live module registry: %d permissions (%v)", len(trapped), trapped)
}
