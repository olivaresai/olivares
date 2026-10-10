// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package scopetreetest is ONE scope-tree fixture for the walks that answer "what
// contains this node": the Cedar scope resolver (modules/governance grants.go), the
// delegation ceiling (modules/governance scopedadmin.go) and the sourcescope actor scope
// (modules/sourcescope resolver.go). Each walk's test seeds this same tree and records its
// answers, so a later move of the walks into one module can be checked against them.
//
// It is test support only: no production package imports it.
package scopetreetest

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Tree is the seeded fixture. Slugs and names are fixed; ids are store-assigned.
//
//	workspaces   default (zero id, no row), ws-a, ws-b
//	agent groups grp-default (default), grp-a (ws-a), grp-b (ws-b),
//	             grp-lost (workspace id with no row)
//	agents       agent-a (ws-a)          in grp-a, grp-b, grp-lost and a group id with no row
//	             agent-default (default) in grp-default, grp-a
//	             agent-lost (workspace id with no row), no groups
//	sessions     sess-a (ws-a, agent-a), sess-default (default, agent-default),
//	             sess-lost (ws-a, agent id with no row)
//	folders      folder-root (ws-a) / folder-mid (ws-a) / folder-leaf (ws-b)
//
// The dangling rows are shapes the store accepts: a membership may name a group that does
// not exist, an agent or group may name a workspace that does not exist, a session may name
// an agent that does not exist, and a folder's workspace is independent of its parent's.
type Tree struct {
	WorkspaceA, WorkspaceB                                      model.Workspace
	GroupDefault, GroupA, GroupB, GroupLost                     model.AgentGroup
	AgentA, AgentDefault, AgentLost                             model.Agent
	SessionA, SessionDefault, SessionLost                       model.Session
	FolderRoot, FolderMid, FolderLeaf                           model.Resource
	MissingWorkspace, MissingGroup, MissingAgent, MissingFolder model.ID

	names map[model.ID]string
}

// Name maps a fixture id to its fixture name, so recorded answers read the same on every
// run. An id the fixture did not create is returned as "?".
func (t Tree) Name(id model.ID) string {
	if n, ok := t.names[id]; ok {
		return n
	}
	return "?"
}

// Open opens an in-memory SQLite store, creates one organization and seeds Tree in it.
// register adds module schemas (nil for none).
func Open(tb testing.TB, register func(store.ExtensionRegistry) error) (store.Store, model.TenantID, Tree) {
	tb.Helper()
	ctx := context.Background()
	if register == nil {
		register = func(store.ExtensionRegistry) error { return nil }
	}
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, register)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Scope Tree", Slug: "scope-tree", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		tb.Fatal(err)
	}
	var tree Tree
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var e error
		tree, e = Seed(ctx, sc)
		return e
	}); err != nil {
		tb.Fatalf("seed scope tree: %v", err)
	}
	return st, tenant, tree
}

// Seed writes Tree into sc (a tenant Mutate scope).
func Seed(ctx context.Context, sc store.Scope) (Tree, error) {
	t := Tree{
		MissingWorkspace: model.NewID(),
		MissingGroup:     model.NewID(),
		MissingAgent:     model.NewID(),
		MissingFolder:    model.NewID(),
		names:            map[model.ID]string{},
	}
	s := seeder{ctx: ctx, sc: sc, t: &t}
	t.names[t.MissingWorkspace] = "missing-workspace"
	t.names[t.MissingGroup] = "missing-group"
	t.names[t.MissingAgent] = "missing-agent"
	t.names[t.MissingFolder] = "missing-folder"

	t.WorkspaceA = s.workspace("ws-a")
	t.WorkspaceB = s.workspace("ws-b")

	t.GroupDefault = s.group("grp-default", "")
	t.GroupA = s.group("grp-a", t.WorkspaceA.ID)
	t.GroupB = s.group("grp-b", t.WorkspaceB.ID)
	t.GroupLost = s.group("grp-lost", t.MissingWorkspace)

	t.AgentA = s.agent("agent-a", t.WorkspaceA.ID)
	t.AgentDefault = s.agent("agent-default", "")
	t.AgentLost = s.agent("agent-lost", t.MissingWorkspace)
	s.member(t.GroupA.ID, t.AgentA.ID)
	s.member(t.GroupB.ID, t.AgentA.ID)
	s.member(t.GroupLost.ID, t.AgentA.ID)
	s.member(t.MissingGroup, t.AgentA.ID)
	s.member(t.GroupDefault.ID, t.AgentDefault.ID)
	s.member(t.GroupA.ID, t.AgentDefault.ID)

	t.SessionA = s.session("sess-a", t.AgentA.ID, t.WorkspaceA.ID)
	t.SessionDefault = s.session("sess-default", t.AgentDefault.ID, "")
	t.SessionLost = s.session("sess-lost", t.MissingAgent, t.WorkspaceA.ID)

	t.FolderRoot = s.folder("folder-root", "", t.WorkspaceA.ID)
	t.FolderMid = s.folder("folder-mid", t.FolderRoot.ID, t.WorkspaceA.ID)
	t.FolderLeaf = s.folder("folder-leaf", t.FolderMid.ID, t.WorkspaceB.ID)
	return t, s.err
}

// seeder keeps the first store error so Seed reads as the tree it builds.
type seeder struct {
	ctx context.Context
	sc  store.Scope
	t   *Tree
	err error
}

func (s *seeder) workspace(slug string) model.Workspace {
	if s.err != nil {
		return model.Workspace{}
	}
	ws, err := s.sc.Workspaces().Create(s.ctx, model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
	s.err = err
	s.t.names[ws.ID] = slug
	return ws
}

func (s *seeder) group(slug string, ws model.ID) model.AgentGroup {
	if s.err != nil {
		return model.AgentGroup{}
	}
	g, err := s.sc.AgentGroups().Create(s.ctx, model.AgentGroup{Name: slug, Slug: slug, Status: model.StatusActive, WorkspaceID: ws})
	s.err = err
	s.t.names[g.ID] = slug
	return g
}

func (s *seeder) agent(name string, ws model.ID) model.Agent {
	if s.err != nil {
		return model.Agent{}
	}
	a, err := s.sc.Agents().Create(s.ctx, model.Agent{
		Name: name, ExternalID: name, Kind: "claude-code", Status: model.StatusActive, WorkspaceID: ws,
	})
	s.err = err
	s.t.names[a.ID] = name
	return a
}

func (s *seeder) member(group, agent model.ID) {
	if s.err != nil {
		return
	}
	_, s.err = s.sc.AgentGroupMembers().Create(s.ctx, model.AgentGroupMember{GroupID: group, AgentID: agent})
}

func (s *seeder) session(ref string, agent, ws model.ID) model.Session {
	if s.err != nil {
		return model.Session{}
	}
	sess, err := s.sc.Sessions().Create(s.ctx, model.Session{ExternalID: ref, AgentID: agent, WorkspaceID: ws})
	s.err = err
	s.t.names[sess.ID] = ref
	return sess
}

func (s *seeder) folder(name string, parent, ws model.ID) model.Resource {
	if s.err != nil {
		return model.Resource{}
	}
	res, err := s.sc.Resources().CreateUnder(s.ctx, parent, model.Resource{Name: name, Kind: "folder", WorkspaceID: ws})
	s.err = err
	s.t.names[res.ID] = name
	return res
}
