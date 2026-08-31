// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Step 3 — the outcome of the per-handler review, asserted in the suite instead of
// described in a comment. Every property here came out of reading the twelve {id}
// handlers; a property that lives only in prose is one nobody re-checks.

type routeRec struct {
	method, pattern string
	perm            auth.Permission
	entity          *api.EntityRef
}

type routeSpy struct{ out *[]routeRec }

func (s routeSpy) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	*s.out = append(*s.out, routeRec{method: method, pattern: pattern, perm: perm})
}

func (s routeSpy) HandleEntity(method, pattern string, perm auth.Permission, ref api.EntityRef, _ api.ModuleHandler) {
	r := ref
	*s.out = append(*s.out, routeRec{method: method, pattern: pattern, perm: perm, entity: &r})
}

func mountedRoutes(t *testing.T) []routeRec {
	t.Helper()
	var out []routeRec
	New().APIRoutes(routeSpy{out: &out})
	return out
}

// TestEveryEntityRouteDeclaresResolvableLineage: an entity route that names a column the
// entity does not have would authorize against a blank workspace forever — silently back
// to collection level. The declaration must match the schema, and the only way to know
// that is to check it.
func TestEveryEntityRouteDeclaresResolvableLineage(t *testing.T) {
	cols := map[model.Kind]string{
		assignmentKind:  colAssignWsID,
		wsConnectorKind: colWCWsID,
	}
	n := 0
	for _, r := range mountedRoutes(t) {
		if r.entity == nil {
			continue
		}
		n++
		if r.entity.IDParam == "" {
			t.Errorf("%s %s: an entity route must name the id parameter", r.method, r.pattern)
		}
		if !strings.Contains(r.pattern, "{"+r.entity.IDParam+"}") {
			t.Errorf("%s %s: declares IDParam %q, which the pattern does not contain", r.method, r.pattern, r.entity.IDParam)
		}
		want, ok := cols[r.entity.Kind]
		if !ok {
			t.Errorf("%s %s: entity kind %q is not one this module stores with a workspace", r.method, r.pattern, r.entity.Kind)
			continue
		}
		if r.entity.WorkspaceColumn != want {
			t.Errorf("%s %s: declares workspace column %q, the %s rows store it in %q",
				r.method, r.pattern, r.entity.WorkspaceColumn, r.entity.Kind, want)
		}
	}
	if n == 0 {
		t.Fatal("no entity routes are mounted — this test is not exercising anything")
	}
}

// TestPostureRequestRoutesStayCollectionLevel is the review's most important finding,
// pinned so a later sweep cannot "finish the migration" by pattern-matching on {id}.
//
// A posture request carries no workspace, so there is nothing to resolve; and a decision
// on it applies to the BINDING it targets, so authorizing on the request's own scope would
// authorize a mutation of a row that may live in another workspace. Migrating these needs
// a schema answer first, not a registration change.
func TestPostureRequestRoutesStayCollectionLevel(t *testing.T) {
	seen := 0
	for _, r := range mountedRoutes(t) {
		if !strings.HasPrefix(r.pattern, "/posture-requests/{") {
			continue
		}
		seen++
		if r.entity != nil {
			t.Errorf("%s %s was migrated to an entity route, but a posture request has no workspace to resolve and a decision on it applies to the binding it targets — authorizing on the request would authorize the wrong object",
				r.method, r.pattern)
		}
	}
	if seen != 3 {
		t.Errorf("expected the 3 posture-request {id} routes, found %d — the fixture no longer matches the surface", seen)
	}
}

// TestNoEntityRouteDeclaresAWorkspaceRefColumn: the review's generalisable lesson. Three
// modules in this tree carry a column literally called workspace_ref that means something
// else — a cost-attribution dimension, the DECLARING principal's workspace, a filesystem
// root. None of them is lineage. An entity route must point at a resolved workspace id.
func TestNoEntityRouteDeclaresAWorkspaceRefColumn(t *testing.T) {
	for _, r := range mountedRoutes(t) {
		if r.entity == nil {
			continue
		}
		if r.entity.WorkspaceColumn == colAssignWorkspace || r.entity.WorkspaceColumn == colWCWorkspace {
			t.Errorf("%s %s declares %q as its lineage: that column holds a workspace REF (a slug the caller supplied), not the resolved workspace id",
				r.method, r.pattern, r.entity.WorkspaceColumn)
		}
		if strings.HasSuffix(r.entity.WorkspaceColumn, "_ref") {
			t.Errorf("%s %s declares %q: a *_ref column is not a resolved workspace id",
				r.method, r.pattern, r.entity.WorkspaceColumn)
		}
	}
}

// TestEntityRouteCoverageMatchesTheReview records what the review concluded, so the split
// is a fact in the suite rather than a claim in a status file: of the twelve {id} routes,
// five satisfy BOTH halves of the rule and seven deliberately do not: three because the row
// has no workspace at all, three because the request can MOVE the row's workspace, and one
// because its EFFECT reaches beyond that workspace.
func TestEntityRouteCoverageMatchesTheReview(t *testing.T) {
	var entity, collection []string
	for _, r := range mountedRoutes(t) {
		if !strings.Contains(r.pattern, "{") {
			continue
		}
		line := r.method + " " + r.pattern
		if r.entity != nil {
			entity = append(entity, line)
		} else {
			collection = append(collection, line)
		}
	}
	sort.Strings(entity)
	sort.Strings(collection)
	if len(entity) != 5 {
		t.Errorf("expected 5 entity routes, got %d: %v", len(entity), entity)
	}
	if len(collection) != 7 {
		t.Errorf("expected 7 {id} routes to stay collection-level (3 posture-request + 3 binding + the assignment DELETE), got %d: %v", len(collection), collection)
	}
	t.Logf("entity routes (%d): %v", len(entity), entity)
	t.Logf("collection-level {id} routes (%d): %v", len(collection), collection)
}

// TestBindingRoutesStayCollectionLevelBecauseTheirLineageIsMutable is THE RULE this unit
// had to learn the hard way, and it is asserted rather than described because the first
// attempt got it wrong and nothing went red.
//
// A route may anchor authorization to an entity's stored lineage only if the request
// cannot MOVE that lineage. handleUpdateBinding re-resolves the scope from the payload and
// rewrites the stored workspace, so a principal authorized against workspace A can land
// the row in B and nothing ever authorizes B. assignments and workspace-connectors force
// the workspace ref and read the workspace id back out of the STORED row, so their anchor
// holds for the life of the request — which is exactly why they may be entity routes and
// bindings may not.
func TestBindingRoutesStayCollectionLevelBecauseTheirLineageIsMutable(t *testing.T) {
	seen := 0
	for _, r := range mountedRoutes(t) {
		if !strings.HasPrefix(r.pattern, "/bindings/{") {
			continue
		}
		seen++
		if r.entity != nil {
			t.Errorf("%s %s is an entity route, but handleUpdateBinding can move the binding's workspace: the authorization would be about where the row WAS while the effect lands where it WILL BE, and nothing authorizes the destination",
				r.method, r.pattern)
		}
	}
	if seen != 3 {
		t.Errorf("expected the 3 binding {id} routes, found %d — the fixture no longer matches the surface", seen)
	}
}

// TestDeletingTheLastAssignmentStaysCollectionLevel is the SECOND half of the rule, and it
// was learned the same way as the first: from a contrast finding, after the code had
// already shipped in a draft PR.
//
// The full rule is that a route may anchor its authorization to an entity's stored lineage
// only if (a) the request cannot MOVE that lineage AND (b) the effect cannot reach BEYOND
// it. DELETE /assignments/{id} satisfies (a) — the workspace is read back out of the
// stored row — and breaks (b): ConnectorAssigned reports an unassigned connector as
// visible in EVERY workspace, so removing the last assignment relaxes visibility
// tenant-wide. Anchoring that decision to one workspace would hand a tenant-wide
// relaxation to a principal whose entire authority is that workspace.
func TestDeletingTheLastAssignmentStaysCollectionLevel(t *testing.T) {
	seen := 0
	for _, r := range mountedRoutes(t) {
		if r.pattern != "/assignments/{id}" {
			continue
		}
		seen++
		if r.method == "DELETE" && r.entity != nil {
			t.Error("DELETE /assignments/{id} is an entity route, but deleting the LAST assignment makes the connector visible in every workspace: the authorization would be scoped to one workspace while the effect is tenant-wide")
		}
		// The control: GET and PUT MUST stay entity routes, or this test would pass
		// simply because the whole family was reverted.
		if r.method != "DELETE" && r.entity == nil {
			t.Errorf("%s /assignments/{id} must remain an entity route: its effect stays inside the row's workspace", r.method)
		}
	}
	if seen != 3 {
		t.Errorf("expected the 3 assignment {id} routes, found %d", seen)
	}
}
