// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/store/scopetreetest"
)

// TestAncestorsOverScopeTreeFixture asks the lineage module what contains each node of
// the one scope-tree fixture. The Cedar adapter's answers over the same fixture are
// pinned in modules/governance/scopetree_walks_internal_test.go.
func TestAncestorsOverScopeTreeFixture(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	groups := store.AncestryOptions{SessionAgentGroups: true}
	every := store.AncestryOptions{AllAgentGroups: true}
	both := store.AncestryOptions{SessionAgentGroups: true, AllAgentGroups: true}
	cases := []struct {
		name string
		kind string
		id   model.ID
		opts store.AncestryOptions
		want string
	}{
		{"agent-a", "agent", tree.AgentA.ID, store.AncestryOptions{},
			"ws=ws-a groups=grp-a@ws-a,grp-b@ws-b,grp-lost@missing-workspace folders="},
		{"agent-default", "agent", tree.AgentDefault.ID, store.AncestryOptions{},
			"ws=default groups=grp-a@ws-a,grp-default@default folders="},
		{"agent-lost", "agent", tree.AgentLost.ID, store.AncestryOptions{}, "ws=missing-workspace groups= folders="},
		{"missing agent", "agent", tree.MissingAgent, store.AncestryOptions{}, "not found"},
		{"sess-a", "session", tree.SessionA.ID, store.AncestryOptions{}, "ws=ws-a groups= folders= session=sess-a"},
		// Only the group in the session's own workspace, never grp-b or grp-lost.
		{"sess-a with groups", "session", tree.SessionA.ID, groups, "ws=ws-a groups=grp-a@ws-a folders= session=sess-a"},
		{"sess-default with groups", "session", tree.SessionDefault.ID, groups,
			"ws=default groups=grp-default@default folders= session=sess-default"},
		{"sess-lost with groups", "session", tree.SessionLost.ID, groups, "ws=ws-a groups= folders= session=sess-lost"},
		// AgentGroups is every group of the session's agent in any workspace (what a forbid
		// matches), whether or not Groups is also asked for; an allow still sees only Groups.
		{"sess-a with every group", "session", tree.SessionA.ID, every,
			"ws=ws-a groups= agentgroups=grp-a@ws-a,grp-b@ws-b,grp-lost@missing-workspace folders= session=sess-a"},
		{"sess-a with both", "session", tree.SessionA.ID, both,
			"ws=ws-a groups=grp-a@ws-a agentgroups=grp-a@ws-a,grp-b@ws-b,grp-lost@missing-workspace folders= session=sess-a"},
		{"sess-default with both", "session", tree.SessionDefault.ID, both,
			"ws=default groups=grp-default@default agentgroups=grp-a@ws-a,grp-default@default folders= session=sess-default"},
		{"sess-lost with both", "session", tree.SessionLost.ID, both, "ws=ws-a groups= folders= session=sess-lost"},
		// An agent's Groups already are every group it belongs to: AgentGroups stays empty.
		{"agent-a with every group", "agent", tree.AgentA.ID, every,
			"ws=ws-a groups=grp-a@ws-a,grp-b@ws-b,grp-lost@missing-workspace folders="},
		{"folder-root", "resource", tree.FolderRoot.ID, store.AncestryOptions{}, "ws=ws-a groups= folders= resource=folder-root"},
		{"folder-mid", "resource", tree.FolderMid.ID, store.AncestryOptions{},
			"ws=ws-a groups= folders=folder-root resource=folder-mid"},
		// Root first; the workspace is the leaf's own, not the root's.
		{"folder-leaf", "resource", tree.FolderLeaf.ID, store.AncestryOptions{},
			"ws=ws-b groups= folders=folder-root,folder-mid resource=folder-leaf"},
		{"missing folder", "resource", tree.MissingFolder, store.AncestryOptions{}, "not found"},
		{"grp-a", "agent_group", tree.GroupA.ID, store.AncestryOptions{}, "ws=ws-a groups=grp-a@ws-a folders="},
		{"grp-lost", "agent_group", tree.GroupLost.ID, store.AncestryOptions{},
			"ws=missing-workspace groups=grp-lost@missing-workspace folders="},
		{"zero id", "agent", "", store.AncestryOptions{}, "not found"},
		{"not a tree kind", "model", tree.AgentA.ID, store.AncestryOptions{}, "not found"},
	}
	ctx := context.Background()
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		for _, tc := range cases {
			anc, found, err := store.Ancestors(ctx, sc, tc.kind, tc.id, tc.opts)
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
				continue
			}
			got := "not found"
			if found {
				got = renderAncestry(anc, tree)
			}
			if got != tc.want {
				t.Errorf("Ancestors(%s)\n got: %q\nwant: %q", tc.name, got, tc.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAncestorsSessionGroupsFollowTheSessionWorkspace seeds a session that lives in a
// different workspace than its agent: an allow sees the agent's groups in the SESSION's
// workspace (grp-b), not in the agent's (grp-a), and AgentGroups still holds all of them.
func TestAncestorsSessionGroupsFollowTheSessionWorkspace(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	ctx := context.Background()
	var sessB model.Session
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		sessB, err = sc.Sessions().Create(ctx, model.Session{ExternalID: "sess-b", AgentID: tree.AgentA.ID, WorkspaceID: tree.WorkspaceB.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	const want = "ws=ws-b groups=grp-b@ws-b agentgroups=grp-a@ws-a,grp-b@ws-b,grp-lost@missing-workspace folders= session=sess-b"
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		anc, found, err := store.Ancestors(ctx, sc, "session", sessB.ID, store.AncestryOptions{SessionAgentGroups: true, AllAgentGroups: true})
		if err != nil || !found {
			t.Fatalf("Ancestors(sess-b) = found %v, err %v", found, err)
		}
		if got := strings.Replace(renderAncestry(anc, tree), sessB.ID.String(), "sess-b", 1); got != want {
			t.Errorf("Ancestors(sess-b)\n got: %q\nwant: %q", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAncestorsReturnsStoreErrors pins the fail-closed half: a failed read, of the node
// row or of any read nested under it, is an error and never "not found", which a caller
// would answer with the declared scope instead.
func TestAncestorsReturnsStoreErrors(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	groups := store.AncestryOptions{SessionAgentGroups: true}
	cases := []struct {
		name string
		ctx  context.Context
		fail string // the repository whose reads fail: workspaces, members or groups
		kind string
		id   model.ID
		opts store.AncestryOptions
	}{
		{"agent row", canceled, "", "agent", tree.AgentA.ID, store.AncestryOptions{}},
		{"session row", canceled, "", "session", tree.SessionA.ID, store.AncestryOptions{}},
		{"resource row", canceled, "", "resource", tree.FolderRoot.ID, store.AncestryOptions{}},
		{"agent_group row", canceled, "", "agent_group", tree.GroupA.ID, store.AncestryOptions{}},
		{"agent workspace", context.Background(), "workspaces", "agent", tree.AgentA.ID, store.AncestryOptions{}},
		{"session workspace", context.Background(), "workspaces", "session", tree.SessionA.ID, store.AncestryOptions{}},
		{"resource workspace", context.Background(), "workspaces", "resource", tree.FolderRoot.ID, store.AncestryOptions{}},
		{"agent_group workspace", context.Background(), "workspaces", "agent_group", tree.GroupA.ID, store.AncestryOptions{}},
		// agent-default lives in the default workspace (no read); only its grp-a reads one.
		{"group workspace", context.Background(), "workspaces", "agent", tree.AgentDefault.ID, store.AncestryOptions{}},
		{"agent memberships", context.Background(), "members", "agent", tree.AgentA.ID, store.AncestryOptions{}},
		{"session memberships", context.Background(), "members", "session", tree.SessionA.ID, groups},
		{"session memberships, every group", context.Background(), "members", "session", tree.SessionA.ID,
			store.AncestryOptions{AllAgentGroups: true}},
		// sess-default is in the default workspace (no read), so the failing read is its
		// agent's group grp-a reading its own workspace.
		{"session group workspace, every group", context.Background(), "workspaces", "session", tree.SessionDefault.ID,
			store.AncestryOptions{AllAgentGroups: true}},
		{"agent groups", context.Background(), "groups", "agent", tree.AgentA.ID, store.AncestryOptions{}},
	}
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, tc := range cases {
			_, found, err := store.Ancestors(tc.ctx, faultScope{Scope: sc, fail: tc.fail}, tc.kind, tc.id, tc.opts)
			if err == nil || found || errors.Is(err, store.ErrNotFound) {
				t.Errorf("Ancestors(%s) with a failed read = found %v, err %v; want the read error", tc.name, found, err)
			}
			if tc.fail != "" && !errors.Is(err, errInjected) {
				t.Errorf("Ancestors(%s) = %v; want the injected %s failure", tc.name, err, tc.fail)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestContainsOverScopeTreeFixture asks the lineage module which nodes each node of the
// one scope-tree fixture contains, the answers the delegation ceiling gave before it asked
// the module (pinned in modules/governance/scopetree_walks_internal_test.go). Workspaces
// never contain folders, a slug or folder id with no row still contains itself, and a
// group whose workspace row is gone (grp-lost) is contained by no workspace.
func TestContainsOverScopeTreeFixture(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	type node struct {
		name string
		node store.ScopeNode
	}
	workspace := func(slug string) store.ScopeNode { return store.ScopeNode{Kind: "workspace", Ref: slug} }
	group := func(slug string) store.ScopeNode { return store.ScopeNode{Kind: "agent_group", Ref: slug} }
	folder := func(id model.ID) store.ScopeNode { return store.ScopeNode{Kind: "folder", Ref: id.String()} }
	nodes := []node{
		{"ws:default", workspace(model.DefaultWorkspaceSlug)},
		{"ws:ws-a", workspace(tree.WorkspaceA.Slug)},
		{"ws:ws-b", workspace(tree.WorkspaceB.Slug)},
		{"grp:grp-default", group(tree.GroupDefault.Slug)},
		{"grp:grp-a", group(tree.GroupA.Slug)},
		{"grp:grp-b", group(tree.GroupB.Slug)},
		{"grp:grp-lost", group(tree.GroupLost.Slug)},
		{"grp:missing", group("missing-group")},
		{"folder:folder-root", folder(tree.FolderRoot.ID)},
		{"folder:folder-mid", folder(tree.FolderMid.ID)},
		{"folder:folder-leaf", folder(tree.FolderLeaf.ID)},
		{"folder:missing", folder(tree.MissingFolder)},
	}
	want := map[string]string{
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
	ctx := context.Background()
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		for _, outer := range nodes {
			var got []string
			for _, inner := range nodes {
				ok, err := store.Contains(ctx, sc, outer.node, inner.node)
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
		// A kind that is no node of the tree (tenant-wide scope is the caller's own
		// case) is contained by nothing and contains nothing.
		tenantWide := store.ScopeNode{Kind: "tenant"}
		for _, pair := range [][2]store.ScopeNode{
			{tenantWide, workspace(tree.WorkspaceA.Slug)}, {workspace(tree.WorkspaceA.Slug), tenantWide},
			{folder(tree.FolderRoot.ID), tenantWide}, {tenantWide, tenantWide},
			// An agent group is contained by an equal group ref only, even when another kind
			// carries the same ref; a group with no workspace row matches the empty ref of no
			// workspace.
			{group(tree.GroupA.Slug), workspace(tree.GroupA.Slug)},
			{group(tree.GroupA.Slug), folder(model.ID(tree.GroupA.Slug))},
			{workspace(""), group("missing-group")}, {workspace(""), group(tree.GroupLost.Slug)},
		} {
			if ok, err := store.Contains(ctx, sc, pair[0], pair[1]); err != nil || ok {
				t.Errorf("Contains(%v, %v) = %v, %v; want false", pair[0], pair[1], ok, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// pathResources serves folders with the given materialized paths, to reach path shapes a
// repository never writes (ids are allocated, so no two folders share a textual prefix),
// and a failed read of one folder.
type pathResources struct {
	store.ResourceRepo
	paths  map[model.ID]string
	failID model.ID // the one folder whose read fails
}

func (r pathResources) Get(_ context.Context, id model.ID) (model.Resource, error) {
	if id == r.failID {
		return model.Resource{}, errInjected
	}
	p, ok := r.paths[id]
	if !ok {
		return model.Resource{}, store.ErrNotFound
	}
	return model.Resource{BaseFields: model.BaseFields{ID: id}, Path: p}, nil
}

type pathScope struct {
	store.Scope
	resources store.ResourceRepo
}

func (s pathScope) Resources() store.ResourceRepo { return s.resources }

// TestContainsFolderPathBoundary pins the segment boundary of the folder prefix test: a
// folder contains its descendants and never a sibling whose path merely shares a textual
// prefix, and a folder without a path contains only itself.
func TestContainsFolderPathBoundary(t *testing.T) {
	st, tenant, _ := scopetreetest.Open(t, nil)
	paths := map[model.ID]string{
		"b": "/a/b", "c": "/a/b/c", "bc": "/a/bc", "legacy": "", "other": "/z",
	}
	cases := []struct {
		outer, inner model.ID
		want         bool
	}{
		{"b", "c", true}, {"b", "bc", false}, {"c", "b", false}, {"b", "b", true},
		{"legacy", "c", false}, {"b", "legacy", false}, {"b", "other", false}, {"b", "gone", false}, {"gone", "b", false},
		// A blank ref is no anchor, not even its own, and a whitespace ref has no row: neither
		// contains nor is contained.
		{"", "", false}, {"", "b", false}, {"b", "", false}, {" ", "b", false}, {"b", " ", false},
	}
	ctx := context.Background()
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		scope := pathScope{Scope: sc, resources: pathResources{paths: paths}}
		for _, tc := range cases {
			got, err := store.Contains(ctx, scope,
				store.ScopeNode{Kind: "folder", Ref: tc.outer.String()}, store.ScopeNode{Kind: "folder", Ref: tc.inner.String()})
			if err != nil || got != tc.want {
				t.Errorf("Contains(folder %s, folder %s) = %v, %v; want %v", tc.outer, tc.inner, got, err, tc.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestContainsReturnsStoreErrors pins the fail-closed half: a failed read is an error,
// never "not contained", which a ceiling would answer as a plain refusal.
func TestContainsReturnsStoreErrors(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	ws := store.ScopeNode{Kind: "workspace", Ref: tree.WorkspaceA.Slug}
	grp := store.ScopeNode{Kind: "agent_group", Ref: tree.GroupA.Slug}
	root := store.ScopeNode{Kind: "folder", Ref: tree.FolderRoot.ID.String()}
	leaf := store.ScopeNode{Kind: "folder", Ref: tree.FolderLeaf.ID.String()}
	cases := []struct {
		name         string
		fail         string // the repository whose reads fail: workspaces, groups or resources
		outer, inner store.ScopeNode
	}{
		{"group row", "groups", ws, grp},
		{"group workspace", "workspaces", ws, grp},
		{"outer folder row", "resources", root, leaf},
	}
	ctx := context.Background()
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		for _, tc := range cases {
			ok, err := store.Contains(ctx, faultScope{Scope: sc, fail: tc.fail}, tc.outer, tc.inner)
			if ok || !errors.Is(err, errInjected) {
				t.Errorf("Contains(%s) with a failed %s read = %v, %v; want the injected failure", tc.name, tc.fail, ok, err)
			}
		}
		// The outer folder reads fine and only the inner folder's read fails.
		inner := pathScope{Scope: sc, resources: pathResources{
			paths: map[model.ID]string{"b": "/a/b", "c": "/a/b/c"}, failID: "c",
		}}
		ok, err := store.Contains(ctx, inner,
			store.ScopeNode{Kind: "folder", Ref: "b"}, store.ScopeNode{Kind: "folder", Ref: "c"})
		if ok || !errors.Is(err, errInjected) {
			t.Errorf("Contains(inner folder row) with a failed read = %v, %v; want the injected failure", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var errInjected = errors.New("injected read failure")

// faultScope fails every read of one repository, to reach the reads a lineage nests
// under its node row.
type faultScope struct {
	store.Scope
	fail string
}

func (s faultScope) Workspaces() store.WorkspaceRepo {
	if s.fail == "workspaces" {
		return faultWorkspaces{s.Scope.Workspaces()}
	}
	return s.Scope.Workspaces()
}

// faultWorkspaces fails the row reads of the workspace repository.
type faultWorkspaces struct{ store.WorkspaceRepo }

func (faultWorkspaces) Get(context.Context, model.ID) (model.Workspace, error) {
	return model.Workspace{}, errInjected
}

func (faultWorkspaces) List(context.Context, model.Query) ([]model.Workspace, model.Page, error) {
	return nil, model.Page{}, errInjected
}

func (s faultScope) AgentGroupMembers() store.Repository[model.AgentGroupMember] {
	if s.fail == "members" {
		return faultRepo[model.AgentGroupMember]{s.Scope.AgentGroupMembers()}
	}
	return s.Scope.AgentGroupMembers()
}

func (s faultScope) AgentGroups() store.Repository[model.AgentGroup] {
	if s.fail == "groups" {
		return faultRepo[model.AgentGroup]{s.Scope.AgentGroups()}
	}
	return s.Scope.AgentGroups()
}

func (s faultScope) Resources() store.ResourceRepo {
	if s.fail == "resources" {
		return faultResources{s.Scope.Resources()}
	}
	return s.Scope.Resources()
}

// faultResources fails the row reads of the resource repository.
type faultResources struct{ store.ResourceRepo }

func (faultResources) Get(context.Context, model.ID) (model.Resource, error) {
	return model.Resource{}, errInjected
}

func (faultResources) List(context.Context, model.Query) ([]model.Resource, model.Page, error) {
	return nil, model.Page{}, errInjected
}

type faultRepo[T any] struct{ store.Repository[T] }

func (faultRepo[T]) Get(context.Context, model.ID) (T, error) {
	var zero T
	return zero, errInjected
}

func (faultRepo[T]) List(context.Context, model.Query) ([]T, model.Page, error) {
	return nil, model.Page{}, errInjected
}

// renderAncestry writes an Ancestry as stable words, naming fixture ids (and the
// synthetic slug of a workspace with no row) by their fixture name.
func renderAncestry(a store.Ancestry, tree scopetreetest.Tree) string {
	name := func(s string) string {
		if n := tree.Name(model.ID(s)); n != "?" {
			return n
		}
		return s
	}
	groups := make([]string, 0, len(a.Groups))
	for _, g := range a.Groups {
		groups = append(groups, name(g.Slug)+"@"+name(g.Workspace))
	}
	sort.Strings(groups)
	folders := make([]string, 0, len(a.Folders))
	for _, f := range a.Folders {
		folders = append(folders, name(f.String()))
	}
	out := "ws=" + name(a.Workspace) + " groups=" + strings.Join(groups, ",")
	if len(a.AgentGroups) > 0 {
		every := make([]string, 0, len(a.AgentGroups))
		for _, g := range a.AgentGroups {
			every = append(every, name(g.Slug)+"@"+name(g.Workspace))
		}
		sort.Strings(every)
		out += " agentgroups=" + strings.Join(every, ",")
	}
	out += " folders=" + strings.Join(folders, ",")
	if !a.Session.ID.IsZero() {
		out += " session=" + name(a.Session.ID.String())
	}
	if !a.Resource.ID.IsZero() {
		out += " resource=" + name(a.Resource.ID.String())
	}
	return out
}
