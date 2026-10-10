// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/store/scopetreetest"
)

// departments is a department tree seeded next to the scopetreetest fixture:
//
//	eng / eng-platform / eng-sre   three workspaces, each under the one before
//	agent-sre (eng-sre) in group grp-platform (eng-platform)
//	sess-sre (eng-sre, agent-sre), folder-platform (eng-platform)
type departments struct {
	eng, platform, sre model.Workspace
	agent              model.Agent
	group              model.AgentGroup
	session            model.Session
	folder             model.Resource
}

func seedDepartments(t *testing.T) (store.Store, model.TenantID, departments) {
	t.Helper()
	st, tenant, _ := scopetreetest.Open(t, nil)
	ctx := context.Background()
	var d departments
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		workspace := func(slug string, parent model.ID) model.Workspace {
			if err != nil {
				return model.Workspace{}
			}
			var ws model.Workspace
			ws, err = sc.Workspaces().Create(ctx, model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive, ParentID: parent})
			return ws
		}
		d.eng = workspace("eng", "")
		d.platform = workspace("eng-platform", d.eng.ID)
		d.sre = workspace("eng-sre", d.platform.ID)
		if err != nil {
			return err
		}
		if d.agent, err = sc.Agents().Create(ctx, model.Agent{Name: "agent-sre", ExternalID: "agent-sre", Kind: "claude-code", Status: model.StatusActive, WorkspaceID: d.sre.ID}); err != nil {
			return err
		}
		if d.group, err = sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "grp-platform", Slug: "grp-platform", Status: model.StatusActive, WorkspaceID: d.platform.ID}); err != nil {
			return err
		}
		if _, err = sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: d.group.ID, AgentID: d.agent.ID}); err != nil {
			return err
		}
		if d.session, err = sc.Sessions().Create(ctx, model.Session{ExternalID: "sess-sre", AgentID: d.agent.ID, WorkspaceID: d.sre.ID}); err != nil {
			return err
		}
		d.folder, err = sc.Resources().CreateUnder(ctx, "", model.Resource{Name: "folder-platform", Kind: "folder", WorkspaceID: d.platform.ID})
		return err
	}); err != nil {
		t.Fatalf("seed departments: %v", err)
	}
	return st, tenant, d
}

// renderDepartment prints a node's workspace with the workspaces above it, root
// first, then each group the same way.
func renderDepartment(a store.Ancestry) string {
	out := "ws=" + a.Workspace + " above=" + strings.Join(a.WorkspaceAncestors, ",")
	for _, g := range a.Groups {
		out += " group=" + g.Slug + "@" + g.Workspace + " above=" + strings.Join(g.WorkspaceAncestors, ",")
	}
	return out
}

// TestAncestorsFollowWorkspaceAncestors pins that the lineage walk reports the workspaces
// above a node's own, root first, for every kind of node and for the workspace of each
// group, so a grant on a department can reach its sub-departments.
func TestAncestorsFollowWorkspaceAncestors(t *testing.T) {
	st, tenant, d := seedDepartments(t)
	cases := []struct {
		name string
		kind string
		id   model.ID
		opts store.AncestryOptions
		want string
	}{
		{"agent", "agent", d.agent.ID, store.AncestryOptions{},
			"ws=eng-sre above=eng,eng-platform group=grp-platform@eng-platform above=eng"},
		{"session", "session", d.session.ID, store.AncestryOptions{}, "ws=eng-sre above=eng,eng-platform"},
		// A session group is a group of the session's own workspace only: grp-platform is not.
		{"session with groups", "session", d.session.ID, store.AncestryOptions{SessionAgentGroups: true},
			"ws=eng-sre above=eng,eng-platform"},
		{"resource", "resource", d.folder.ID, store.AncestryOptions{}, "ws=eng-platform above=eng"},
		{"agent group", "agent_group", d.group.ID, store.AncestryOptions{},
			"ws=eng-platform above=eng group=grp-platform@eng-platform above=eng"},
	}
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, tc := range cases {
			anc, found, err := store.Ancestors(context.Background(), sc, tc.kind, tc.id, tc.opts)
			if err != nil || !found {
				t.Errorf("Ancestors(%s) = found %v, err %v", tc.name, found, err)
				continue
			}
			if got := renderDepartment(anc); got != tc.want {
				t.Errorf("Ancestors(%s)\n got: %q\nwant: %q", tc.name, got, tc.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAncestorsOfARootWorkspaceHaveNoWorkspaceAncestors pins the no-parent shape every
// workspace had before departments: the default workspace, a root workspace and a root
// whose row carries no path (a row written before core v25) have none, whatever else the
// tenant holds.
func TestAncestorsOfARootWorkspaceHaveNoWorkspaceAncestors(t *testing.T) {
	st, tenant, d := seedDepartments(t)
	ctx := context.Background()
	var inEng, inDefault model.Agent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if inEng, err = sc.Agents().Create(ctx, model.Agent{Name: "in-eng", ExternalID: "in-eng", Kind: "claude-code", Status: model.StatusActive, WorkspaceID: d.eng.ID}); err != nil {
			return err
		}
		inDefault, err = sc.Agents().Create(ctx, model.Agent{Name: "in-default", ExternalID: "in-default", Kind: "claude-code", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	unplaced := func(ws model.Workspace) (model.Workspace, error) {
		if ws.ID == d.eng.ID {
			ws.Path = "" // the root as core v25 left a workspace it never placed
		}
		return ws, nil
	}
	for _, tc := range []struct {
		name  string
		scope func(store.Scope) store.Scope
		id    model.ID
		want  string
	}{
		{"root", func(sc store.Scope) store.Scope { return sc }, inEng.ID, "ws=eng above="},
		{"unplaced root", func(sc store.Scope) store.Scope { return patchedScope{sc, unplaced} }, inEng.ID, "ws=eng above="},
		{"default workspace", func(sc store.Scope) store.Scope { return sc }, inDefault.ID, "ws=default above="},
		// The unplaced root still anchors its sub-departments' paths: "/" + its id.
		{"below an unplaced root", func(sc store.Scope) store.Scope { return patchedScope{sc, unplaced} }, d.agent.ID,
			"ws=eng-sre above=eng,eng-platform group=grp-platform@eng-platform above=eng"},
	} {
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			anc, found, err := store.Ancestors(ctx, tc.scope(sc), "agent", tc.id, store.AncestryOptions{})
			if err != nil || !found {
				t.Errorf("Ancestors(%s) = found %v, err %v", tc.name, found, err)
				return nil
			}
			if got := renderDepartment(anc); got != tc.want {
				t.Errorf("Ancestors(%s)\n got: %q\nwant: %q", tc.name, got, tc.want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestAncestorsFailClosedOnAnInconsistentWorkspaceTree pins that a workspace tree the
// store would never write is an error, never a shorter lineage: no foreign key ties
// parent_id or path to a row, and a shorter lineage would grant less where it should
// refuse, so a caller that tolerated one could not tell it from a root.
func TestAncestorsFailClosedOnAnInconsistentWorkspaceTree(t *testing.T) {
	st, tenant, d := seedDepartments(t)
	// Each patch is on eng or eng-platform, which every node of the fixture's chain
	// passes through or sits in, so the same tree is refused for all four kinds. A node
	// whose OWN workspace row is gone keeps its synthetic slug, as before departments.
	cases := []struct {
		name  string
		patch func(model.Workspace) (model.Workspace, error)
	}{
		{"ancestor row missing", func(ws model.Workspace) (model.Workspace, error) {
			if ws.ID == d.eng.ID {
				return model.Workspace{}, store.ErrNotFound
			}
			return ws, nil
		}},
		{"path is not the parent's path and the id", func(ws model.Workspace) (model.Workspace, error) {
			if ws.ID == d.platform.ID {
				ws.Path = "/" + d.sre.ID.String() + "/" + ws.ID.String()
			}
			return ws, nil
		}},
		{"a parent and no path", func(ws model.Workspace) (model.Workspace, error) {
			if ws.ID == d.platform.ID {
				ws.Path = ""
			}
			return ws, nil
		}},
		{"a root whose path is not its own id", func(ws model.Workspace) (model.Workspace, error) {
			if ws.ID == d.eng.ID {
				ws.Path = "/" + d.sre.ID.String()
			}
			return ws, nil
		}},
		{"a cycle", func(ws model.Workspace) (model.Workspace, error) {
			if ws.ID == d.eng.ID {
				ws.ParentID = d.sre.ID
			}
			return ws, nil
		}},
	}
	for _, tc := range cases {
		for _, kind := range []struct {
			name string
			id   model.ID
		}{{"agent", d.agent.ID}, {"session", d.session.ID}, {"resource", d.folder.ID}, {"agent_group", d.group.ID}} {
			t.Run(tc.name+"/"+kind.name, func(t *testing.T) {
				err := st.View(context.Background(), tenant, func(sc store.Scope) error {
					anc, found, err := store.Ancestors(context.Background(), patchedScope{sc, tc.patch}, kind.name, kind.id, store.AncestryOptions{})
					if found || !errors.Is(err, store.ErrLineageUnavailable) {
						t.Errorf("Ancestors = %q, found %v, err %v; want ErrLineageUnavailable", renderDepartment(anc), found, err)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// patchedScope serves workspace rows through patch, which stands in for a tree the
// store would never write; an error from patch is the read's error.
type patchedScope struct {
	store.Scope
	patch func(model.Workspace) (model.Workspace, error)
}

func (s patchedScope) Workspaces() store.WorkspaceRepo {
	return patchedWorkspaces{s.Scope.Workspaces(), s.patch}
}

type patchedWorkspaces struct {
	store.WorkspaceRepo
	patch func(model.Workspace) (model.Workspace, error)
}

func (r patchedWorkspaces) Get(ctx context.Context, id model.ID) (model.Workspace, error) {
	ws, err := r.WorkspaceRepo.Get(ctx, id)
	if err != nil {
		return ws, err
	}
	return r.patch(ws)
}

// TestAncestorsFollowARealSubtreeMove walks real rows through SetParent, which rewrites the
// path of the node and of every descendant: the grandchild's lineage must follow each move
// and a node moved to the root must have none above it.
func TestAncestorsFollowARealSubtreeMove(t *testing.T) {
	st, tenant, d := seedDepartments(t)
	ctx := context.Background()
	var ops model.Workspace
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		ops, err = sc.Workspaces().Create(ctx, model.Workspace{Name: "ops", Slug: "ops", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	above := func() string {
		var got string
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			anc, found, err := store.Ancestors(ctx, sc, "agent", d.agent.ID, store.AncestryOptions{})
			if err != nil || !found {
				t.Fatalf("Ancestors(agent-sre) = found %v, err %v", found, err)
			}
			got = renderDepartment(anc)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	move := func(node, parent model.ID) {
		t.Helper()
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Workspaces().SetParent(ctx, node, parent)
			return err
		}); err != nil {
			t.Fatalf("SetParent: %v", err)
		}
	}
	if got, want := above(), "ws=eng-sre above=eng,eng-platform group=grp-platform@eng-platform above=eng"; got != want {
		t.Fatalf("before any move\n got: %q\nwant: %q", got, want)
	}
	move(d.platform.ID, ops.ID) // eng-platform and eng-sre both change path
	if got, want := above(), "ws=eng-sre above=ops,eng-platform group=grp-platform@eng-platform above=ops"; got != want {
		t.Errorf("after eng-platform moved under ops\n got: %q\nwant: %q", got, want)
	}
	move(d.platform.ID, "") // a root again: its path is its own id
	if got, want := above(), "ws=eng-sre above=eng-platform group=grp-platform@eng-platform above="; got != want {
		t.Errorf("after eng-platform became a root\n got: %q\nwant: %q", got, want)
	}
}

// TestAncestorsOfADepartmentUnderTheDefaultWorkspace pins that the default workspace is a
// valid parent, with the path the repository heals into it when it gains its first child.
func TestAncestorsOfADepartmentUnderTheDefaultWorkspace(t *testing.T) {
	st, tenant, _ := scopetreetest.Open(t, nil)
	ctx := context.Background()
	var agent model.Agent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		ops, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "ops", Slug: "ops", Status: model.StatusActive, ParentID: def.ID})
		if err != nil {
			return err
		}
		agent, err = sc.Agents().Create(ctx, model.Agent{Name: "ops-bot", ExternalID: "ops-bot", Kind: "claude-code", Status: model.StatusActive, WorkspaceID: ops.ID})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		anc, found, err := store.Ancestors(ctx, sc, "agent", agent.ID, store.AncestryOptions{})
		if err != nil || !found {
			t.Fatalf("Ancestors = found %v, err %v", found, err)
		}
		if got, want := renderDepartment(anc), "ws=ops above=default"; got != want {
			t.Errorf("Ancestors\n got: %q\nwant: %q", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAncestorsFailClosedOnARootWithAForeignPath pins the root check on its own: the node
// sits directly in the root, so no child step can refuse the tree first.
func TestAncestorsFailClosedOnARootWithAForeignPath(t *testing.T) {
	st, tenant, d := seedDepartments(t)
	ctx := context.Background()
	var inEng model.Agent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		inEng, err = sc.Agents().Create(ctx, model.Agent{Name: "in-eng", ExternalID: "in-eng", Kind: "claude-code", Status: model.StatusActive, WorkspaceID: d.eng.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	foreign := func(ws model.Workspace) (model.Workspace, error) {
		if ws.ID == d.eng.ID {
			ws.Path = "/" + d.sre.ID.String()
		}
		return ws, nil
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, found, err := store.Ancestors(ctx, patchedScope{sc, foreign}, "agent", inEng.ID, store.AncestryOptions{})
		if found || !errors.Is(err, store.ErrLineageUnavailable) {
			t.Errorf("Ancestors = found %v, err %v; want ErrLineageUnavailable", found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestContainsStaysEqualityOnAWorkspace pins what this change leaves alone: a workspace
// contains itself and its own agent groups, not its sub-departments, and no workspace
// contains one above it. The delegation ceiling stays narrower than the reach of a grant
// until a later step decides what a department administrator may delegate.
func TestContainsStaysEqualityOnAWorkspace(t *testing.T) {
	st, tenant, _ := seedDepartments(t)
	ctx := context.Background()
	ws := func(slug string) store.ScopeNode { return store.ScopeNode{Kind: "workspace", Ref: slug} }
	group := store.ScopeNode{Kind: "agent_group", Ref: "grp-platform"}
	for _, tc := range []struct {
		name         string
		outer, inner store.ScopeNode
		want         bool
	}{
		{"itself", ws("eng"), ws("eng"), true},
		{"a sub-department", ws("eng"), ws("eng-platform"), false},
		{"the parent department", ws("eng-platform"), ws("eng"), false},
		{"its own group", ws("eng-platform"), group, true},
		{"a group of a sub-department", ws("eng"), group, false},
	} {
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			got, err := store.Contains(ctx, sc, tc.outer, tc.inner)
			if err != nil || got != tc.want {
				t.Errorf("Contains(%s) = %v, %v; want %v", tc.name, got, err, tc.want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAncestorsThroughAConfinedScopeReportARoot pins what the doc of WorkspaceLineage says:
// a workspace-confined scope hides tree positions, so it reports no ancestors and no error.
// The resolvers read through the tenant scope for that reason.
func TestAncestorsThroughAConfinedScopeReportARoot(t *testing.T) {
	st, tenant, d := seedDepartments(t)
	ctx := context.Background()
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, d.platform.ID)
		if err != nil {
			return err
		}
		anc, found, err := store.Ancestors(ctx, confined, "resource", d.folder.ID, store.AncestryOptions{})
		if err != nil || !found {
			t.Fatalf("Ancestors through a confined scope = found %v, err %v", found, err)
		}
		if len(anc.WorkspaceAncestors) != 0 {
			t.Errorf("a confined scope reported ancestors %v", anc.WorkspaceAncestors)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
