// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// THE BOUND ON WHAT A SCOPE-TREE GRANT CAN HIDE FROM THE CONSOLE.
//
// /v1/auth/whoami reports the permissions a ROLE confers; a scoped grant is not an input
// (core/api/whoami_perms.go keys the precomputed sets by {role, confined}). So authority
// held by grant is invisible to the console, and since #578 can() HIDES rather than
// over-offers — there is no 403 to notice it by.
//
// How big that blind spot is depends entirely on the grant's SCOPE, and the difference is
// not a detail — it is the whole design:
//
//   - scope tree `tenant` projects `permit(...)` with NO `when` clause
//     (governance/scopedadmin.go cedarScopeWhen), so it matches EVERY request. It is FLAT,
//     and a flat permission set is the right type to carry it.
//   - scope tree `workspace`/`agent_group`/`folder` projects `when { resource in … }`,
//     which can only match a request carrying a resource the engine can place in the tree.
//     A COLLECTION route hands it none: grants.go resolve() short-circuits on
//     `Resource.ID == "" && Resource.WorkspaceID.IsZero()` and no `in` can hold.
//     modules/governance/grants_console_visibility_test.go proves both halves on the wire.
//
// This test bounds the SECOND half, which is the one a flat set cannot express and
// therefore the one that must stay small to be safely declared rather than fixed. It
// measures the module surface by RUNNING APIRoutes against a recorder — the EntityRef is
// an argument to HandleEntity, so nothing short of running the registration observes it.
//
// If it fails, the bound moved: a module mounted a new entity route, which widens the set
// of actions the console can hide from a workspace-scoped operator. That is a real change
// and it needs a decision, not a re-baselined constant.

// entityReachRecorder records which routes opt into entity-scoped authorization, and with
// what declared lineage. A collection route is recorded too, so the ratio is measured
// rather than assumed.
type entityReachRecorder struct {
	ns         string
	collection *int
	entity     *[]entityReachRoute
}

type entityReachRoute struct {
	ns, method, pattern string
	perm                auth.Permission
	workspaceColumn     string
}

func (r entityReachRecorder) Handle(string, string, auth.Permission, api.ModuleHandler) {
	*r.collection++
}

func (r entityReachRecorder) HandleEntity(method, pattern string, perm auth.Permission, ref api.EntityRef, _ api.ModuleHandler) {
	*r.entity = append(*r.entity, entityReachRoute{
		ns: r.ns, method: method, pattern: pattern, perm: perm, workspaceColumn: ref.WorkspaceColumn,
	})
}

// moduleScopeTreeReach is the set of MODULE permissions a workspace/agent-group/folder
// grant can authorize: those on a route that resolves the entity's stored workspace. A
// route whose EntityRef declares no WorkspaceColumn carries no lineage, so no tree grant
// matches it either (core/api EntityRef.WorkspaceColumn).
// The five sessions:work/decision entries entered on 2026-08-10 with K1. K2 adds the
// three sessions:lease entries on 2026-08-12. They are recorded WITH the decision
// this test demands rather than bumped merely to make it green.
//
// THE DECISION. The console does not surface them at all today — the work cockpit does not
// exist, and it deliberately does not: building it against a store that had not landed would
// have produced a screen with no authority. So nothing is being hidden from anybody right now,
// and the honest entry is "in the bound, not yet consoled".
//
// K2 DECISION FOR LEASES. WorkLease has the same stored workspace lineage as its WorkItem,
// so read, holder writes and governed admin recovery remain grantable inside that workspace;
// moving them to tenant scope would make a confined operator unable to recover its own work.
// The kernel lane does not add console UI: the console owner must surface these three permissions
// with their scope-tree limitation when it builds the cockpit. Until then they are explicitly
// "in the bound, not yet consoled", like the five K1 permissions.
//
// K5 DECISION FOR PROTOCOL BINDINGS. Binding specs and binding instances both retain exact
// workspace lineage, so reads, governed writes and administrative state transitions remain
// grantable inside that workspace. The K5 Composer is permission-gated, but a workspace-scoped
// operator still will NOT see these permissions in the flat whoami set because that set cannot
// express "only in this workspace". The console therefore hides the surface rather than probing
// it with a request that would reveal the grant only through a 403.
//
// K1 moved the entity/collection ratio 5/659 -> 18/677; K2 moved it to 25/685; K5 moves it to
// 30/694. The premise the bound rests on — entity routes remain the rare case — is still
// asserted by the test rather than trusted from this dated measurement.
var moduleScopeTreeReach = []string{
	"sessions:decision:admin",
	"sessions:decision:read",
	"sessions:lease:admin",
	"sessions:lease:read",
	"sessions:lease:write",
	"sessions:protocol-binding:admin",
	"sessions:protocol-binding:read",
	"sessions:protocol-binding:write",
	"sessions:work:admin",
	"sessions:work:read",
	"sessions:work:write",
	"sourcescope:assignment:read",
	"sourcescope:assignment:write",
	"sourcescope:workspace_connector:read",
	"sourcescope:workspace_connector:write",
}

func TestScopeTreeGrantReachOverModulesIsBounded(t *testing.T) {
	set := liveModules(t)
	var (
		entity     []entityReachRoute
		collection int
	)
	for _, m := range set.all {
		m.APIRoutes(entityReachRecorder{ns: m.APINamespace(), collection: &collection, entity: &entity})
	}
	if collection == 0 && len(entity) == 0 {
		t.Fatal("the recorder observed no module routes at all; every assertion below is vacuous")
	}
	// The ratio IS the finding: a scope-tree grant is inert on every collection route, and
	// collection routes are almost all of them.
	t.Logf("module routes: %d entity (HandleEntity) of %d total", len(entity), len(entity)+collection)
	if collection < len(entity) {
		t.Errorf("only %d collection routes against %d entity routes: the premise of this bound "+
			"(that entity routes are the rare case) no longer holds", collection, len(entity))
	}

	got := map[string]bool{}
	for _, rt := range entity {
		if rt.workspaceColumn == "" {
			// No lineage: authorized exactly as a collection route would be, so it adds no
			// reach. Recorded rather than silently skipped — a route that LOOKS entity-scoped
			// but declares no workspace is exactly the thing a reader would miscount.
			t.Logf("entity route with NO workspace column (no added reach): %s %s /v1/m/%s%s",
				rt.perm, rt.method, rt.ns, rt.pattern)
			continue
		}
		got[string(rt.perm)] = true
	}
	var have []string
	for p := range got {
		have = append(have, p)
	}
	sort.Strings(have)

	want := append([]string(nil), moduleScopeTreeReach...)
	sort.Strings(want)
	if len(have) == 0 {
		t.Fatal("no module permission is reachable by a scope-tree grant; if that is genuinely " +
			"true the bound changed shape, and this test must be rewritten rather than emptied")
	}
	if !equalStrings(have, want) {
		t.Errorf("the module surface a WORKSPACE-scoped grant can authorize has changed.\n"+
			"  have: %v\n  want: %v\n"+
			"Each addition is an action the console can hide from a workspace-scoped operator "+
			"with no 403 to reveal it. Decide what the console does about it, then update "+
			"this list WITH that decision written down.", have, want)
	}

	// Every one of them must actually be grantable, or the reach is theoretical: a
	// permission a grant cannot carry cannot hide anything.
	registerLiveModulePermissions(t, set)
	for _, p := range have {
		if !auth.IsGrantablePermission(auth.Permission(p)) {
			t.Errorf("%q rides an entity route but is not grantable, so no grant can confer it: "+
				"the reach list is claiming more than the catalog allows", p)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// registerLiveModulePermissions seeds the scope-grantable catalog exactly as the
// composition root does at mount (core/api/server.go mountModules).
func registerLiveModulePermissions(t *testing.T, set moduleSet) {
	t.Helper()
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)
	for _, m := range set.all {
		if err := auth.RegisterModulePermissions(m.Permissions()); err != nil {
			t.Fatalf("register %q: %v", m.APINamespace(), err)
		}
	}
}
