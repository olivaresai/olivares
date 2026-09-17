// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestTenantIsolationReads is the core DoD check: one tenant cannot see another
// tenant's data, and a foreign id is indistinguishable from a missing one.
func TestTenantIsolationReads(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenantA := provisionTenant(t, st, "alpha")
	tenantB := provisionTenant(t, st, "bravo")

	agentA := mustCreateAgent(t, st, tenantA, "a-bot")
	_ = mustCreateAgent(t, st, tenantB, "b-bot")

	// B cannot Get A's agent — and gets ErrNotFound, not a "forbidden" oracle.
	err := st.View(ctx, tenantB, func(sc store.Scope) error {
		_, err := sc.Agents().Get(ctx, agentA.ID)
		return err
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("B.Get(A's id): err = %v, want ErrNotFound", err)
	}

	// B's List shows only B's own agents.
	if err := st.View(ctx, tenantB, func(sc store.Scope) error {
		agents, _, err := sc.Agents().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(agents) != 1 || agents[0].Name != "b-bot" {
			return fmt.Errorf("B.List = %d agents %v, want only b-bot", len(agents), names(agents))
		}
		return nil
	}); err != nil {
		t.Fatalf("isolation list: %v", err)
	}

	// B cannot Update or Delete A's agent (it is invisible to B).
	err = st.Mutate(ctx, tenantB, func(sc store.Scope) error {
		return sc.Agents().Delete(ctx, agentA.ID)
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("B.Delete(A's id): err = %v, want ErrNotFound", err)
	}
}

// TestTriggerBackstopBlocksCrossTenantInsert proves the SQLite write backstop:
// even a hand-crafted INSERT that bypasses the repository layer cannot place a
// row in a tenant other than the bound one — the tripwire trigger aborts it.
//
// It checks that on two relations, because the engine no longer treats them the
// same way. An UNGUARDED tenant relation (skills) meets the tripwire directly,
// which is the shape this case has always tested. A GUARDED one (agents) is also
// covered by the lineage writer protocol, whose BEFORE INSERT trigger fires
// FIRST and refuses any writer that has not enrolled: on the engine connection,
// outside Mutate, it aborts with "lineage writer protocol required"
// (SQLITE_CONSTRAINT_TRIGGER 1811) before tenancy is ever considered, so that
// shape can no longer say anything about tenant scope on its own. The guarded
// arm therefore enrols the protocol the way a legitimate transaction does — for
// both tenants, so the enrolment clause is satisfied for the foreign claim too —
// and keeps the unarmed refusal as an explicit control instead of letting it
// fail in setup.
//
// Measured, not assumed: on a guarded relation the foreign claim is refused by
// the tripwire on the lineage EPOCH table, which the guard trigger updates for
// NEW.tenant_id while the pin still names the bound tenant. Same refusal, same
// row count, one fence further in — which is why the unguarded arm below is not
// redundant: it is the only one that still exercises <table>_scope_ins itself.
func TestTriggerBackstopBlocksCrossTenantInsert(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenantA := provisionTenant(t, st, "alpha")
	tenantB := provisionTenant(t, st, "bravo")

	ss, ok := st.(*sqlStore)
	if !ok {
		t.Fatalf("store is %T, want *sqlStore", st)
	}
	rows := func(table string, tenant model.TenantID) int {
		t.Helper()
		var n int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM "+table+" WHERE tenant_id = ?", tenant.String(),
		).Scan(&n); err != nil {
			t.Fatalf("count %s for %s: %v", table, tenant, err)
		}
		return n
	}

	// --- Unguarded relation: the tripwire, unmediated ------------------------
	//
	// skills carries no lineage guard, so a hand-crafted INSERT reaches
	// skills_scope_ins exactly as this case has always required.
	const insertSkill = `INSERT INTO skills
(id, tenant_id, created_at, updated_at, version, name, status)
VALUES (?,?,?,?,?,?,?)`
	skillArgs := func(tenant model.TenantID) []any {
		return []any{
			model.NewID().String(), tenant.String(),
			ss.clock.Now().String(), ss.clock.Now().String(), 1, "x", "active",
		}
	}
	tx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := ss.dia.BindTenant(ctx, tx, tenantA); err != nil {
		t.Fatal(err)
	}
	// Bound tenant A: a direct insert claiming tenant A is allowed.
	if _, err := tx.ExecContext(ctx, insertSkill, skillArgs(tenantA)...); err != nil {
		t.Fatalf("insert for bound tenant: %v", err)
	}
	// Claiming tenant B while bound to A: the trigger must abort.
	if _, err := tx.ExecContext(ctx, insertSkill, skillArgs(tenantB)...); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "tenant scope violation") {
		t.Fatalf("cross-tenant insert: err = %v, want tenant scope violation", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// --- Guarded relation: the same backstop behind the writer protocol ------
	const insertAgent = `INSERT INTO agents
(id, tenant_id, created_at, updated_at, version, name, kind, status)
VALUES (?,?,?,?,?,?,?,?)`
	agentArgs := func(tenant model.TenantID) []any {
		return []any{
			model.NewID().String(), tenant.String(),
			ss.clock.Now().String(), ss.clock.Now().String(), 1, "x", "k", "active",
		}
	}

	// Control: the identical hand-crafted INSERT on the engine connection, with
	// no lineage writer enrolled, must still abort and must land nothing. The
	// armed case below satisfies that clause, so without this control nothing
	// here would notice if the lineage guard stopped refusing an unarmed writer.
	unarmed, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ss.dia.BindTenant(ctx, unarmed, tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := unarmed.ExecContext(ctx, insertAgent, agentArgs(tenantA)...); err == nil ||
		!strings.Contains(err.Error(), "lineage writer protocol required") {
		t.Fatalf("unarmed hand-crafted insert = %v, want the lineage writer refusal", err)
	}
	if err := unarmed.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := rows("agents", tenantA); got != 0 {
		t.Fatalf("refused unarmed insert landed %d agent rows, want 0", got)
	}

	if err := st.Mutate(ctx, tenantA, func(sc store.Scope) error {
		ts := sc.(*tenantScope)
		// Enrol tenant B in the writer protocol as well. Nothing here writes on
		// B's behalf through a repository; the enrolment exists so the lineage
		// clause cannot be what refuses the foreign claim below.
		if err := ts.lineageWriter.arm(ctx, tenantB); err != nil {
			t.Fatalf("enrol the writer protocol for the foreign tenant: %v", err)
		}
		if _, err := ts.tx.ExecContext(ctx, insertAgent, agentArgs(tenantA)...); err != nil {
			t.Fatalf("armed insert for bound tenant: %v", err)
		}
		if _, err := ts.tx.ExecContext(ctx, insertAgent, agentArgs(tenantB)...); err == nil ||
			!strings.Contains(strings.ToLower(err.Error()), "tenant scope violation") {
			t.Fatalf("armed cross-tenant insert: err = %v, want tenant scope violation", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("armed backstop transaction: %v", err)
	}
	// Only the bound-tenant row survived; the foreign claim left nothing in B.
	if got := rows("agents", tenantA); got != 1 {
		t.Fatalf("bound-tenant agents = %d, want 1", got)
	}
	if got := rows("agents", tenantB); got != 0 {
		t.Fatalf("refused cross-tenant insert landed %d agent rows in B, want 0", got)
	}
}

// TestConcurrentTenantsRace exercises the store under concurrency with the race
// detector: many tenants mutating and auditing in parallel must each end with an
// intact, correctly-counted chain and no cross-tenant bleed.
func TestConcurrentTenantsRace(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	const tenants, perTenant = 6, 8
	ids := make([]model.TenantID, tenants)
	for i := range ids {
		ids[i] = provisionTenant(t, st, fmt.Sprintf("t%d", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, tenants)
	for ti, tenant := range ids {
		wg.Add(1)
		go func(ti int, tenant model.TenantID) {
			defer wg.Done()
			for j := 0; j < perTenant; j++ {
				err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
					if _, err := sc.Agents().Create(ctx, model.Agent{
						Name: fmt.Sprintf("a-%d-%d", ti, j), Status: model.StatusActive,
					}); err != nil {
						return err
					}
					_, err := sc.Audit().Append(ctx, model.AuditDraft{
						Actor: "system", ActorKind: model.ActorSystem, Action: "agent.create",
					})
					return err
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(ti, tenant)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent mutate: %v", err)
	}

	// Each tenant sees exactly its own agents and an intact chain.
	for ti, tenant := range ids {
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			agents, _, err := sc.Agents().List(ctx, model.Query{Limit: 1000})
			if err != nil {
				return err
			}
			if len(agents) != perTenant {
				return fmt.Errorf("tenant %d: %d agents, want %d", ti, len(agents), perTenant)
			}
			rep, err := sc.Audit().Verify(ctx, 1)
			if err != nil {
				return err
			}
			// 1 provisioning event (org.create) + perTenant explicit appends.
			if !rep.OK || rep.Checked != perTenant+1 {
				return fmt.Errorf("tenant %d: verify %+v (checked want %d)", ti, rep, perTenant+1)
			}
			return nil
		}); err != nil {
			t.Fatalf("post-check: %v", err)
		}
	}
}

func names(agents []model.Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.Name
	}
	return out
}
