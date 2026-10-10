// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

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

// TestScopeTreeWalkActorScope records walk 3 of the scope-tree walks, resolveActorScope (resolver.go),
// over the one scopetreetest fixture: each actor's workspace, the agent groups an allow
// matches (groups) and the agent groups a forbid matches (all). The other two walks record
// theirs over the same fixture in modules/governance/scopetree_walks_internal_test.go.
//
// The session-group answer, decided by C7.2: for sess-a (workspace ws-a) an allow sees only
// grp-a, the session's agent's group in its own workspace, as the Cedar resolver's session
// path does; grp-b (ws-b) and grp-lost (workspace row gone) are not groups of the session.
// A forbid is absolute and still matches every group of the agent (all), so a forbid on
// grp-b denies sess-a.
func TestScopeTreeWalkActorScope(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	cases := []struct {
		name string
		who  actorRef
		want string
	}{
		{"sess-a", actorRef{actorSession, "sess-a"},
			"ws=ws-a groups=grp-a all=grp-a,grp-b,grp-lost session=sess-a agent=agent-a"},
		{"sess-default", actorRef{actorSession, "sess-default"},
			"ws=default groups=grp-default all=grp-a,grp-default session=sess-default agent=agent-default"},
		// The session's agent row is gone: no agent axis and no groups, workspace kept.
		{"sess-lost", actorRef{actorSession, "sess-lost"}, "ws=ws-a groups= all= session=sess-lost agent="},
		{"agent-a", actorRef{actorAgent, "agent-a"},
			"ws=ws-a groups=grp-a,grp-b,grp-lost all=grp-a,grp-b,grp-lost session= agent=agent-a"},
		{"agent-default", actorRef{actorAgent, "agent-default"},
			"ws=default groups=grp-a,grp-default all=grp-a,grp-default session= agent=agent-default"},
		// A workspace id with no row resolves to the raw id, which matches no authored slug.
		{"agent-lost", actorRef{actorAgent, "agent-lost"}, "ws=missing-workspace groups= all= session= agent=agent-lost"},
		{"unknown session", actorRef{actorSession, "no-such-session"}, "ws= groups= all= session= agent="},
		{"unknown agent", actorRef{actorAgent, "no-such-agent"}, "ws= groups= all= session= agent="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordActorScope(t, st, tenant, tree, tc.who); got != tc.want {
				t.Errorf("actor scope(%s)\n got: %q\nwant: %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestScopeTreeWalkActorScopeOrphanMemberships pins a shape the store accepts: memberships
// that name an agent id with no row (nothing ties agent_group_members to agents). The walk
// reads a session's groups by the session's agent id, never through the agent row, so
// sess-lost (whose agent row is gone) still has them: an allow sees the one in ws-a, a
// forbid sees both.
func TestScopeTreeWalkActorScopeOrphanMemberships(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for _, g := range []model.AgentGroup{tree.GroupA, tree.GroupB} {
			if _, err := sc.AgentGroupMembers().Create(context.Background(),
				model.AgentGroupMember{GroupID: g.ID, AgentID: tree.MissingAgent}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed orphan memberships: %v", err)
	}
	const want = "ws=ws-a groups=grp-a all=grp-a,grp-b session=sess-lost agent="
	if got := recordActorScope(t, st, tenant, tree, actorRef{actorSession, "sess-lost"}); got != want {
		t.Errorf("actor scope(sess-lost, orphan memberships)\n got: %q\nwant: %q", got, want)
	}
}

// recordActorScope renders resolveActorScope's answer for who with fixture names.
func recordActorScope(t *testing.T, st store.Store, tenant model.TenantID, tree scopetreetest.Tree, who actorRef) string {
	t.Helper()
	var got actorScope
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		got, err = resolveActorScope(context.Background(), sc, who)
		return err
	}); err != nil {
		t.Fatalf("resolveActorScope: %v", err)
	}
	ws := got.workspaceSlug
	if n := tree.Name(model.ID(ws)); n != "?" {
		ws = n
	}
	sorted := func(in []string) string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	return "ws=" + ws + " groups=" + sorted(got.groups) + " all=" + sorted(got.allGroups) +
		" session=" + got.sessionRef + " agent=" + got.agentExternalID
}

// TestScopeTreeWalkActorScopeSessionWorkspace seeds a session that lives in ws-b while its
// agent (agent-a) lives in ws-a: the allow groups follow the session's workspace (grp-b),
// not the agent's, and a forbid still matches all of the agent's groups.
func TestScopeTreeWalkActorScopeSessionWorkspace(t *testing.T) {
	st, tenant, tree := scopetreetest.Open(t, nil)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Sessions().Create(context.Background(),
			model.Session{ExternalID: "sess-b", AgentID: tree.AgentA.ID, WorkspaceID: tree.WorkspaceB.ID})
		return err
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	const want = "ws=ws-b groups=grp-b all=grp-a,grp-b,grp-lost session=sess-b agent=agent-a"
	if got := recordActorScope(t, st, tenant, tree, actorRef{actorSession, "sess-b"}); got != want {
		t.Errorf("actor scope(sess-b)\n got: %q\nwant: %q", got, want)
	}
}

var errInjected = errors.New("injected read failure")

// failingMembers is a scope whose agent-group membership reads fail.
type failingMembers struct{ store.Scope }

func (s failingMembers) AgentGroupMembers() store.Repository[model.AgentGroupMember] {
	return failingMembersRepo{s.Scope.AgentGroupMembers()}
}

type failingMembersRepo struct {
	store.Repository[model.AgentGroupMember]
}

func (failingMembersRepo) List(context.Context, model.Query) ([]model.AgentGroupMember, model.Page, error) {
	return nil, model.Page{}, errInjected
}

// TestScopeTreeWalkActorScopeFailsClosed: a failed lineage read is an error, never the
// empty scope, which would leave a source with only forbid bindings open to the actor.
func TestScopeTreeWalkActorScopeFailsClosed(t *testing.T) {
	st, tenant, _ := scopetreetest.Open(t, nil)
	for _, who := range []actorRef{{actorSession, "sess-a"}, {actorAgent, "agent-a"}} {
		err := st.View(context.Background(), tenant, func(sc store.Scope) error {
			got, err := resolveActorScope(context.Background(), failingMembers{sc}, who)
			if !errors.Is(err, errInjected) {
				t.Errorf("resolveActorScope(%q) with failing membership reads = %+v, %v; want the read error", who.ref, got, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
