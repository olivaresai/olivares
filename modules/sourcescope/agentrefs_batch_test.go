// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CUTS A4 (2026-10-02): agentRefsByGroup read one agent per member row (AU2-06
// measured 1,001 queries for one group). Now it reads the member list plus ONE
// batched agents query. This test pins the identical output and counts the
// queries.

// agentRefsByGroupReference is the pre-batch algorithm, frozen verbatim from the
// 2026-10-01 accessmap.go.
func agentRefsByGroupReference(ctx context.Context, sc store.Scope, groupID model.ID) ([]string, bool, error) {
	out := make([]string, 0, 16)
	q := model.Query{Filters: []model.Filter{eq("group_id", groupID.String())}, Limit: listCap}
	for {
		members, page, err := sc.AgentGroupMembers().List(ctx, q)
		if err != nil {
			return nil, false, err
		}
		for _, mem := range members {
			a, err := sc.Agents().Get(ctx, mem.AgentID)
			if err != nil {
				continue
			}
			if a.ExternalID == "" {
				continue
			}
			out = append(out, a.ExternalID)
			if len(out) >= maxProjectedAgents {
				return out, true, nil
			}
		}
		if !page.HasMore || page.Cursor == "" {
			return out, false, nil
		}
		q.Cursor = page.Cursor
	}
}

type countingAgents struct {
	inner store.Repository[model.Agent]
	gets  int
	lists int
	maxIn int // largest id set one List bound (SR5C round 2: the parameter regression)
}

func (c *countingAgents) Get(ctx context.Context, id model.ID) (model.Agent, error) {
	c.gets++
	return c.inner.Get(ctx, id)
}
func (c *countingAgents) List(ctx context.Context, q model.Query) ([]model.Agent, model.Page, error) {
	c.lists++
	for _, f := range q.Filters {
		if f.Op == model.OpIn {
			if ids, ok := f.Value.([]model.ID); ok && len(ids) > c.maxIn {
				c.maxIn = len(ids)
			}
		}
	}
	return c.inner.List(ctx, q)
}
func (c *countingAgents) Create(ctx context.Context, v model.Agent) (model.Agent, error) {
	return c.inner.Create(ctx, v)
}
func (c *countingAgents) Update(ctx context.Context, v model.Agent) (model.Agent, error) {
	return c.inner.Update(ctx, v)
}
func (c *countingAgents) Delete(ctx context.Context, id model.ID) error {
	return c.inner.Delete(ctx, id)
}

type countingScope struct {
	store.Scope
	agents *countingAgents
}

func (s countingScope) Agents() store.Repository[model.Agent] { return s.agents }

func TestAgentRefsByGroupBatchedMatchesPerRow(t *testing.T) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "a4", Slug: "a4", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}

	// 300 agents: 298 with external ids, 1 with none (skipped), 1 orphan
	// (created, joined, then deleted — the production shape of an orphan member).
	const members = 300
	var groupID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		grp, err := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "bots", Slug: "bots", Status: model.StatusActive})
		if err != nil {
			return err
		}
		groupID = grp.ID
		for i := 0; i < members; i++ {
			external := fmt.Sprintf("ext-%04d", i)
			if i == members-2 {
				external = ""
			}
			a, err := sc.Agents().Create(ctx, model.Agent{Name: fmt.Sprintf("agent-%04d", i), Kind: "claude-code", ExternalID: external})
			if err != nil {
				return err
			}
			if _, err := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: grp.ID, AgentID: a.ID}); err != nil {
				return err
			}
			if i == members-1 {
				if err := sc.Agents().Delete(ctx, a.ID); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var want []string
	var wantTrunc bool
	var got []string
	var gotTrunc bool
	var refGets, newGets, newLists int
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		counter := &countingAgents{inner: sc.Agents()}
		wrapped := countingScope{Scope: sc, agents: counter}
		var err error
		want, wantTrunc, err = agentRefsByGroupReference(ctx, wrapped, groupID)
		refGets = counter.gets
		if err != nil {
			return err
		}
		counter.gets = 0
		got, gotTrunc, err = agentRefsByGroup(ctx, wrapped, groupID)
		newGets, newLists = counter.gets, counter.lists
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if wantTrunc || gotTrunc {
		t.Fatalf("299 projected agents < cap %d: truncation must be false (ref %v, new %v)", maxProjectedAgents, wantTrunc, gotTrunc)
	}
	if len(want) != members-2 || len(got) != members-2 {
		t.Fatalf("projected = %d/%d, want %d (298 external ids; orphan and id-less skipped)", len(want), len(got), members-2)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("output[%d] = %q, want %q — the batched read changed member order", i, got[i], want[i])
		}
	}
	if refGets != members {
		t.Fatalf("the per-row walk issued %d Gets, want one per member (%d)", refGets, members)
	}
	if newGets != 0 || newLists != 1 {
		t.Fatalf("the batched read must issue 0 Gets and 1 List (300 < 500-chunk), got %d Gets, %d Lists", newGets, newLists)
	}
}

func BenchmarkAgentRefsByGroup(b *testing.B) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "a4b", Slug: "a4b", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		b.Fatal(err)
	}
	var groupID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		grp, err := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "bots", Slug: "bots", Status: model.StatusActive})
		if err != nil {
			return err
		}
		groupID = grp.ID
		for i := 0; i < 300; i++ {
			a, err := sc.Agents().Create(ctx, model.Agent{Name: fmt.Sprintf("agent-%04d", i), Kind: "claude-code", ExternalID: fmt.Sprintf("ext-%04d", i)})
			if err != nil {
				return err
			}
			if _, err := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: grp.ID, AgentID: a.ID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	b.Run("per-row", func(b *testing.B) {
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			for i := 0; i < b.N; i++ {
				if _, _, err := agentRefsByGroupReference(ctx, sc, groupID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	})
	b.Run("batched", func(b *testing.B) {
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			for i := 0; i < b.N; i++ {
				if _, _, err := agentRefsByGroup(ctx, sc, groupID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	})
}

// SR5C (2026-10-03): past the List page cap the batched read must CHUNK, or
// the projection silently skips the agents beyond the first page — and one IN
// for the whole group would exceed PostgreSQL's 65,535-parameter limit long
// before the projection cap (round 2). 1,500 members > listCap: the answer
// must equal the per-row reference exactly (composition and order), in exactly
// three bounded queries, none binding more than idChunk parameters.
func TestAgentRefsByGroupBatchedPagesPastTheCap(t *testing.T) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	agentRefsBigGroupSuite(t, st)
}

// SR5C round 2: the same parameter regression on PostgreSQL — the limit the
// chunk bound exists for. NOT RUN (and said so) when no PostgreSQL is
// configured; a skip is not a pass.
func TestAgentRefsByGroupBatchedPagesPastTheCapPostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the PostgreSQL leg of the parameter regression is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
	}
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	agentRefsBigGroupSuite(t, st)
}

func agentRefsBigGroupSuite(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "a4big", Slug: "a4big", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}

	const members = 1500 // > listCap (1,000)
	var groupID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		grp, err := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "many", Slug: "many", Status: model.StatusActive})
		if err != nil {
			return err
		}
		groupID = grp.ID
		for i := 0; i < members; i++ {
			a, err := sc.Agents().Create(ctx, model.Agent{Name: fmt.Sprintf("agent-%04d", i), Kind: "claude-code", ExternalID: fmt.Sprintf("ext-%04d", i)})
			if err != nil {
				return err
			}
			if _, err := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: grp.ID, AgentID: a.ID}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var want []string
	var wantTrunc bool
	var got []string
	var gotTrunc bool
	var newLists, maxIn int
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		counter := &countingAgents{inner: sc.Agents()}
		wrapped := countingScope{Scope: sc, agents: counter}
		var err error
		want, wantTrunc, err = agentRefsByGroupReference(ctx, wrapped, groupID)
		if err != nil {
			return err
		}
		got, gotTrunc, err = agentRefsByGroup(ctx, wrapped, groupID)
		newLists, maxIn = counter.lists, counter.maxIn
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if !wantTrunc || !gotTrunc {
		t.Fatalf("1,500 members exceed the projection cap %d: truncation must be true (ref %v, new %v)", maxProjectedAgents, wantTrunc, gotTrunc)
	}
	if len(want) != maxProjectedAgents || len(got) != maxProjectedAgents {
		t.Fatalf("projected = %d/%d, want the full cap %d", len(want), len(got), maxProjectedAgents)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("output[%d] = %q, want %q — the capped agents query changed the projection's composition or order", i, got[i], want[i])
		}
	}
	if maxIn > idChunk {
		t.Fatalf("one query bound %d ids — PostgreSQL's 65,535-parameter limit forbids an unbounded IN; chunk at %d", maxIn, idChunk)
	}
	if newLists != 3 {
		t.Fatalf("1,500 ids in chunks of %d must take exactly 3 Lists, got %d", idChunk, newLists)
	}
}
