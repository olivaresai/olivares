// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"sort"
	"strings"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/store/scopetreetest"
)

// Two of the three walks that answer "what contains this node" run over the one
// scopetreetest fixture, and each answer is recorded as it is today. The third walk, the
// sourcescope actor scope, records its answers over the same fixture in
// modules/sourcescope/scopetree_walks_internal_test.go. These tests change no behavior;
// they pin it so the walks can move into one module without moving a decision.

// TestScopeTreeWalkCedarAncestors records walk 1, scopeResolver.readScope (grants.go): the
// transitive Cedar ancestors of each fixture node. Session rows are recorded with and
// without Route.SessionInheritsAgentGroups, the opt-in that adds same-workspace groups only.
func TestScopeTreeWalkCedarAncestors(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, New().RegisterSchema)
	r := &scopeResolver{data: st}
	node := func(kind string, id model.ID, inherit bool) auth.Request {
		return auth.Request{
			Tenant:   tenant,
			Resource: auth.ResourceAttrs{Kind: kind, ID: id.String()},
			Route:    auth.RouteMetadata{SessionInheritsAgentGroups: inherit},
		}
	}
	cases := []struct {
		name string
		req  auth.Request
		want string
	}{
		{"agent-a", node("agent", tree.AgentA.ID, false),
			"AgentGroup:grp-a AgentGroup:grp-b AgentGroup:grp-lost Workspace:missing-workspace Workspace:ws-a Workspace:ws-b"},
		{"agent-default", node("agent", tree.AgentDefault.ID, false),
			"AgentGroup:grp-a AgentGroup:grp-default Workspace:default Workspace:ws-a"},
		{"agent-lost", node("agent", tree.AgentLost.ID, false), "Workspace:missing-workspace"},
		{"missing agent", node("agent", tree.MissingAgent, false), ""},
		{"sess-a", node("session", tree.SessionA.ID, false), "Workspace:ws-a"},
		// The session-group difference: only grp-a, the group in the session's own workspace.
		// The sourcescope walk gives grp-a, grp-b and grp-lost for the same session.
		{"sess-a inheriting", node("session", tree.SessionA.ID, true), "AgentGroup:grp-a Workspace:ws-a"},
		{"sess-default", node("session", tree.SessionDefault.ID, false), "Workspace:default"},
		{"sess-default inheriting", node("session", tree.SessionDefault.ID, true),
			"AgentGroup:grp-default Workspace:default"},
		{"sess-lost inheriting", node("session", tree.SessionLost.ID, true), "Workspace:ws-a"},
		{"folder-root", node("resource", tree.FolderRoot.ID, false), "Workspace:ws-a"},
		{"folder-mid", node("resource", tree.FolderMid.ID, false), "Resource:folder-root Workspace:ws-a"},
		// The chain is hung under the LEAF's workspace: folder-root (stored in ws-a) is
		// reached under ws-b here, and ws-a is not an ancestor of folder-leaf.
		{"folder-leaf", node("resource", tree.FolderLeaf.ID, false),
			"Resource:folder-mid Resource:folder-root Workspace:ws-b"},
		{"missing folder", node("resource", tree.MissingFolder, false), ""},
		// An agent group's own entity is among its parents.
		{"grp-a", node("agent_group", tree.GroupA.ID, false), "AgentGroup:grp-a Workspace:ws-a"},
		{"grp-default", node("agent_group", tree.GroupDefault.ID, false), "AgentGroup:grp-default Workspace:default"},
		{"grp-lost", node("agent_group", tree.GroupLost.ID, false), "AgentGroup:grp-lost Workspace:missing-workspace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			em, resUID, _, _, err := r.resolve(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got := cedarAncestors(em, resUID, tree); got != tc.want {
				t.Errorf("ancestors(%s)\n got: %q\nwant: %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestScopeTreeWalkCedarAttributes records the rest of walk 1 over the same fixture: the
// store-authoritative attributes readScope adds to the resource entity (a session's agent,
// a resource's kind and stored sensitivity over the request's), and the caller-declared
// workspace it falls back to for a non-tree kind, a row that is gone or a collection
// action, which a stored row's workspace always overrides.
func TestScopeTreeWalkCedarAttributes(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, New().RegisterSchema)
	r := &scopeResolver{data: st}
	node := func(kind string, id, declared model.ID, sensitivity string) auth.Request {
		return auth.Request{
			Tenant:   tenant,
			Resource: auth.ResourceAttrs{Kind: kind, ID: id.String(), WorkspaceID: declared, Sensitivity: sensitivity},
		}
	}
	cases := []struct {
		name string
		req  auth.Request
		want string
	}{
		{"sess-a", node("session", tree.SessionA.ID, "", ""),
			"attrs: agent=agent-a kind=session sensitivity= | Workspace:ws-a"},
		{"sess-lost", node("session", tree.SessionLost.ID, "", ""),
			"attrs: agent=missing-agent kind=session sensitivity= | Workspace:ws-a"},
		// The stored (empty) sensitivity replaces the request's.
		{"folder-leaf", node("resource", tree.FolderLeaf.ID, "", "public"),
			"attrs: kind=resource resource_kind=folder sensitivity= | Resource:folder-mid Resource:folder-root Workspace:ws-b"},
		{"agent-a", node("agent", tree.AgentA.ID, "", "high"),
			"attrs: kind=agent sensitivity=high | AgentGroup:grp-a AgentGroup:grp-b AgentGroup:grp-lost " +
				"Workspace:missing-workspace Workspace:ws-a Workspace:ws-b"},
		{"grp-b declaring ws-a", node("agent_group", tree.GroupB.ID, tree.WorkspaceA.ID, ""),
			"attrs: kind=agent_group sensitivity= | AgentGroup:grp-b Workspace:ws-b"},
		{"non-tree kind declaring ws-a", node("model", tree.AgentA.ID, tree.WorkspaceA.ID, ""),
			"attrs: kind=model sensitivity= | Workspace:ws-a"},
		{"missing agent declaring ws-b", node("agent", tree.MissingAgent, tree.WorkspaceB.ID, ""),
			"attrs: kind=agent sensitivity= | Workspace:ws-b"},
		{"collection declaring a lost workspace", node("agent", "", tree.MissingWorkspace, ""),
			"attrs: kind=agent sensitivity= | Workspace:missing-workspace"},
		{"collection declaring nothing", node("agent", "", "", ""), "attrs: kind=agent sensitivity= | "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			em, resUID, _, _, err := r.resolve(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			got := "attrs: " + cedarAttributes(em[resUID].Attributes, tree) + " | " + cedarAncestors(em, resUID, tree)
			if got != tc.want {
				t.Errorf("resolve(%s)\n got: %q\nwant: %q", tc.name, got, tc.want)
			}
		})
	}
}

// cedarAttributes renders an entity's attributes as sorted "key=value" words, naming
// fixture ids by their fixture name.
func cedarAttributes(rec cedar.Record, tree scopetreetest.Tree) string {
	var words []string
	for k, v := range rec.All() {
		val := v.String()
		if s, ok := v.(cedar.String); ok {
			val = string(s)
		}
		if n := tree.Name(model.ID(val)); n != "?" {
			val = n
		}
		words = append(words, string(k)+"="+val)
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// cedarAncestors renders every entity reachable from uid through Parents as sorted
// "Type:name" words, naming fixture ids by their fixture name.
func cedarAncestors(em cedar.EntityMap, uid cedar.EntityUID, tree scopetreetest.Tree) string {
	seen := map[cedar.EntityUID]bool{}
	var walk func(cedar.EntityUID)
	walk = func(u cedar.EntityUID) {
		for p := range em[u].Parents.All() {
			if !seen[p] {
				seen[p] = true
				walk(p)
			}
		}
	}
	walk(uid)
	words := make([]string, 0, len(seen))
	for u := range seen {
		id := string(u.ID)
		if n := tree.Name(model.ID(id)); n != "?" {
			id = n
		}
		words = append(words, string(u.Type)+":"+id)
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// TestScopeTreeWalkDelegationCeiling records walk 2, scopeContains (scopedadmin.go): for
// each scope node of the fixture, the nodes it contains. Workspaces never contain folders,
// a slug or folder id with no row still contains itself, and a group whose workspace row
// is gone (grp-lost) is contained by no workspace.
func TestScopeTreeWalkDelegationCeiling(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	type node struct {
		name string
		spec scopeSpec
	}
	folder := func(r model.Resource) node {
		return node{"folder:" + r.Name, scopeSpec{Tree: scopeFolder, Ref: r.ID.String()}}
	}
	nodes := []node{
		{"tenant", scopeSpec{Tree: scopeTenant}},
		{"ws:default", scopeSpec{Tree: scopeWorkspace, Ref: model.DefaultWorkspaceSlug}},
		{"ws:ws-a", scopeSpec{Tree: scopeWorkspace, Ref: tree.WorkspaceA.Slug}},
		{"ws:ws-b", scopeSpec{Tree: scopeWorkspace, Ref: tree.WorkspaceB.Slug}},
		{"grp:grp-default", scopeSpec{Tree: scopeAgentGroup, Ref: tree.GroupDefault.Slug}},
		{"grp:grp-a", scopeSpec{Tree: scopeAgentGroup, Ref: tree.GroupA.Slug}},
		{"grp:grp-b", scopeSpec{Tree: scopeAgentGroup, Ref: tree.GroupB.Slug}},
		{"grp:grp-lost", scopeSpec{Tree: scopeAgentGroup, Ref: tree.GroupLost.Slug}},
		{"grp:missing", scopeSpec{Tree: scopeAgentGroup, Ref: "missing-group"}},
		folder(tree.FolderRoot),
		folder(tree.FolderMid),
		folder(tree.FolderLeaf),
		{"folder:missing", scopeSpec{Tree: scopeFolder, Ref: tree.MissingFolder.String()}},
	}
	all := make([]string, len(nodes))
	for i, n := range nodes {
		all[i] = n.name
	}
	want := map[string]string{
		"tenant":             strings.Join(all, " "),
		"ws:default":         "ws:default grp:grp-default",
		"ws:ws-a":            "ws:ws-a grp:grp-a",
		"ws:ws-b":            "ws:ws-b grp:grp-b",
		"grp:grp-default":    "grp:grp-default",
		"grp:grp-a":          "grp:grp-a",
		"grp:grp-b":          "grp:grp-b",
		"grp:grp-lost":       "grp:grp-lost",
		"grp:missing":        "grp:missing",
		"folder:folder-root": "folder:folder-root folder:folder-mid folder:folder-leaf",
		"folder:folder-mid":  "folder:folder-mid folder:folder-leaf",
		"folder:folder-leaf": "folder:folder-leaf",
		"folder:missing":     "folder:missing",
	}
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, outer := range nodes {
			var got []string
			for _, inner := range nodes {
				ok, err := scopeContains(context.Background(), sc, outer.spec, inner.spec)
				if err != nil {
					return err
				}
				if ok {
					got = append(got, inner.name)
				}
			}
			if g := strings.Join(got, " "); g != want[outer.name] {
				t.Errorf("%s contains\n got: %q\nwant: %q", outer.name, g, want[outer.name])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
