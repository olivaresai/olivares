// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SG-00 across BOTH backends.
//
// The SQLite-only suite is not enough here and the tree already says why: SQLite
// masked a Postgres-only defect once before (the access_edges 42702 bug —
// modules/governance/crossbackend_authz_test.go:17-19). It matters twice over
// for this plane, because the two engines disagree on exactly the mechanism the
// identity plane leans on: SQLite's single writer serializes transactions, so
// the losing side of a first-sight race is barely reachable, while Postgres lets
// two read-miss/insert transactions genuinely interleave and the second insert
// waits on the unique index until the winner commits, then errors
// (sqlstore/evidenceops.go:190-198). A guarantee proven only on the engine that
// cannot exercise it is not proven.

// sessOn builds a module against a specific backend.
func sessOn(t *testing.T, eng store.Engine, dsn string) (*Module, model.TenantID, *testClock) {
	t.Helper()
	m := New()
	clk := &testClock{now: baseTime}
	m.clock = clk
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: eng, DSN: dsn, Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open %s: %v", eng, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("tenant %s: %v", eng, err)
	}
	m.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	return m, tenant, clk
}

// backends returns SQLite always, and Postgres when a server is configured. It
// never silently drops the Postgres leg: an absent server is logged as NOT
// exercised, so "the suite was green" can never quietly mean "on one engine".
func backends(t *testing.T) []struct {
	name string
	open func(t *testing.T) (*Module, model.TenantID, *testClock)
} {
	type be = struct {
		name string
		open func(t *testing.T) (*Module, model.TenantID, *testClock)
	}
	out := []be{{name: "sqlite", open: func(t *testing.T) (*Module, model.TenantID, *testClock) {
		return sessOn(t, store.EngineSQLite, ":memory:")
	}}}
	if enginetest.PostgresAvailable(t) {
		out = append(out, be{name: "postgres", open: func(t *testing.T) (*Module, model.TenantID, *testClock) {
			pg := enginetest.IsolatedPostgres(t)
			return sessOn(t, store.EnginePostgres, pg.App)
		}})
	} else {
		t.Logf("%s unset: Postgres NOT exercised (SQLite-only this run)", enginetest.EnvSuperuserDSN)
	}
	return out
}

// The engine-level guarantee, on every engine that will ever hold this table.
// Inserting the same (tenant, provider, external_id) twice must fail in the
// DATABASE — the writer's care is exactly what a second process does not run.
func TestIdentity_CrossBackend_DuplicateTripleRejected(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			ctx := context.Background()
			b := SessionBinding{Provider: "claude", ExternalID: "dup", At: baseTime}

			if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				return bindAlias(ctx, sc, "osn_first", b)
			}); err != nil {
				t.Fatalf("[%s] first bind: %v", be.name, err)
			}
			err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				return bindAlias(ctx, sc, "osn_second", b)
			})
			if err == nil {
				t.Fatalf("[%s] the second insert of the same triple SUCCEEDED; the unique index is not enforcing", be.name)
			}
			if n := countRows(t, m, tenant, aliasKind); n != 1 {
				t.Errorf("[%s] alias rows = %d, want 1", be.name, n)
			}
		})
	}
}

// F8, the interleaving the SQLite suite hid. The original concurrency test gave
// every goroutine the SAME observation instant, so touch() found last_seen_at
// already at or past its target and returned without writing — the CAS it does
// was never exercised. With DISTINCT timestamps every racer tries to advance the
// row, and on Postgres those updates genuinely collide.
//
// The invariant under test is the one a caller depends on: re-delivery must
// resolve, not error. A resolver that surfaces a store conflict to its caller
// because two observations of the SAME session arrived close together has moved
// the race onto the consumer.
func TestIdentity_CrossBackend_ConcurrentRedeliveryDistinctTimestamps(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			ctx := context.Background()

			const n = 12
			sids := make([]string, n)
			errs := make([]error, n)
			var wg sync.WaitGroup
			wg.Add(n)
			for i := 0; i < n; i++ {
				go func(i int) {
					defer wg.Done()
					// DISTINCT instants: each racer tries to move last_seen_at.
					at := baseTime.Add(time.Duration(i) * time.Second)
					sids[i], errs[i] = m.ResolveSession(ctx, tenant,
						SessionBinding{Provider: "claude", ExternalID: "redeliver", At: at})
				}(i)
			}
			wg.Wait()

			for i, err := range errs {
				if err != nil {
					t.Errorf("[%s] racer %d: ResolveSession returned %v; re-delivery must resolve, not surface a store conflict",
						be.name, i, err)
				}
			}
			first := ""
			for i, sid := range sids {
				if sid == "" {
					continue
				}
				if first == "" {
					first = sid
				} else if sid != first {
					t.Errorf("[%s] racer %d resolved %q, want the single canonical %q", be.name, i, sid, first)
				}
			}
			if got := countRows(t, m, tenant, identityKind); got != 1 {
				t.Errorf("[%s] identity rows = %d, want 1", be.name, got)
			}
			if got := countRows(t, m, tenant, aliasKind); got != 1 {
				t.Errorf("[%s] alias rows = %d, want 1", be.name, got)
			}
		})
	}
}

// The claim's exclusivity, on both engines: concurrent claimants on one session
// produce exactly one holder.
func TestClaim_CrossBackend_ConcurrentClaimOneWinner(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			ctx := context.Background()
			sid, err := m.ResolveSession(ctx, tenant,
				SessionBinding{Provider: "claude", ExternalID: "claimed", At: baseTime})
			if err != nil {
				t.Fatalf("[%s] resolve: %v", be.name, err)
			}

			const n = 8
			won := make([]string, n)
			var wg sync.WaitGroup
			wg.Add(n)
			for i := 0; i < n; i++ {
				go func(i int) {
					defer wg.Done()
					h := "holder-" + string(rune('A'+i))
					if l, err := m.Claim(ctx, tenant, sid, h, time.Minute); err == nil {
						won[i] = l.Holder
					}
				}(i)
			}
			wg.Wait()

			winners := 0
			for _, w := range won {
				if w != "" {
					winners++
				}
			}
			if winners != 1 {
				t.Errorf("[%s] %d holders claimed one session, want exactly 1", be.name, winners)
			}
			if got := countRows(t, m, tenant, claimKind); got != 1 {
				t.Errorf("[%s] claim rows = %d, want 1", be.name, got)
			}
		})
	}
}
