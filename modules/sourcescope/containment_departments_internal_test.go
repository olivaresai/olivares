// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/store/scopetreetest"
)

// seedDepartmentActor adds eng / eng-platform / eng-sre next to the scope-tree fixture
// and an agent in eng-sre.
func seedDepartmentActor(t *testing.T) (store.Store, model.TenantID) {
	t.Helper()
	st, tenant, _ := scopetreetest.Open(t, nil)
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var parent model.ID
		var sre model.Workspace
		for _, slug := range []string{"eng", "eng-platform", "eng-sre"} {
			ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive, ParentID: parent})
			if err != nil {
				return err
			}
			parent, sre = ws.ID, ws
		}
		_, err := sc.Agents().Create(ctx, model.Agent{Name: "sre-bot", ExternalID: "sre-bot", Kind: "claude-code", Status: model.StatusActive, WorkspaceID: sre.ID})
		return err
	}); err != nil {
		t.Fatalf("seed departments: %v", err)
	}
	return st, tenant
}

// TestWorkspaceBindingContainsTheActorsSubDepartments pins that a workspace binding, an
// allow or a forbid, contains an actor in the bound department or in any department below
// it, and not one beside or above it. A forbid that stopped at the bound department would
// let an allow bound lower in the tree open the source it forbids.
func TestWorkspaceBindingContainsTheActorsSubDepartments(t *testing.T) {
	st, tenant := seedDepartmentActor(t)
	var actor actorScope
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		actor, err = resolveActorScope(context.Background(), sc, actorRef{actorAgent, "sre-bot"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := actor.workspaceSlug + " above=" + strings.Join(actor.workspaceAbove, ","); got != "eng-sre above=eng,eng-platform" {
		t.Fatalf("actor scope = %q, want the workspace and the workspaces above it", got)
	}
	id := actorIdentity{workspaceSlug: actor.workspaceSlug, workspaceAbove: actor.workspaceAbove}
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"eng-sre", true}, {"eng-platform", true}, {"eng", true},
		{"sales", false}, {model.DefaultWorkspaceSlug, false},
	} {
		for _, effect := range []string{effectAllow, effectForbid} {
			b := binding{scopeTree: scopeWorkspace, scopeRef: tc.ref, effect: effect}
			if got := containsActor(id, b); got != tc.want {
				t.Errorf("%s binding on %s contains the eng-sre actor = %v, want %v", effect, tc.ref, got, tc.want)
			}
		}
	}
	// An actor in a department never contains the departments below it.
	up := actorIdentity{workspaceSlug: "eng", workspaceAbove: nil}
	if containsActor(up, binding{scopeTree: scopeWorkspace, scopeRef: "eng-sre"}) {
		t.Error("a binding on eng-sre contains an actor in eng")
	}
}

// TestActorScopeFailsClosedOnABrokenWorkspaceTree pins that an actor whose workspace tree
// is inconsistent is an error, so the source is refused and never opened by a shorter
// lineage.
func TestActorScopeFailsClosedOnABrokenWorkspaceTree(t *testing.T) {
	st, tenant := seedDepartmentActor(t)
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		_, err := resolveActorScope(context.Background(), brokenTreeScope{sc}, actorRef{actorAgent, "sre-bot"})
		return err
	})
	if !errors.Is(err, store.ErrLineageUnavailable) {
		t.Errorf("resolveActorScope over a workspace whose parent row is gone = %v, want ErrLineageUnavailable", err)
	}
}

// brokenTreeScope hides the root department's row, as a parent_id that names no row would.
type brokenTreeScope struct{ store.Scope }

func (s brokenTreeScope) Workspaces() store.WorkspaceRepo {
	return brokenTreeWorkspaces{s.Scope.Workspaces()}
}

type brokenTreeWorkspaces struct{ store.WorkspaceRepo }

func (r brokenTreeWorkspaces) Get(ctx context.Context, id model.ID) (model.Workspace, error) {
	ws, err := r.WorkspaceRepo.Get(ctx, id)
	if err == nil && ws.Slug == "eng" {
		return model.Workspace{}, store.ErrNotFound
	}
	return ws, err
}
