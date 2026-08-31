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
func TestTriggerBackstopBlocksCrossTenantInsert(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenantA := provisionTenant(t, st, "alpha")
	tenantB := provisionTenant(t, st, "bravo")

	ss, ok := st.(*sqlStore)
	if !ok {
		t.Fatalf("store is %T, want *sqlStore", st)
	}
	tx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := ss.dia.BindTenant(ctx, tx, tenantA); err != nil {
		t.Fatal(err)
	}

	insert := func(tenant model.TenantID) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agents
(id, tenant_id, created_at, updated_at, version, name, kind, status)
VALUES (?,?,?,?,?,?,?,?)`,
			model.NewID().String(), tenant.String(),
			ss.clock.Now().String(), ss.clock.Now().String(), 1, "x", "k", "active")
		return err
	}

	// Bound tenant A: a direct insert claiming tenant A is allowed.
	if err := insert(tenantA); err != nil {
		t.Fatalf("insert for bound tenant: %v", err)
	}
	// Claiming tenant B while bound to A: the trigger must abort.
	if err := insert(tenantB); err == nil || !strings.Contains(strings.ToLower(err.Error()), "tenant scope violation") {
		t.Fatalf("cross-tenant insert: err = %v, want tenant scope violation", err)
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
