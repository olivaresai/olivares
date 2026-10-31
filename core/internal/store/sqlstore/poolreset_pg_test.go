// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the regression: what the FIRST request through the pool sees after a reset of the
// pool's connection (a PostgreSQL restart, failover or administrator
// termination), and what the one after it sees.
//
// Measured on PostgreSQL 16.15 with pgx v5.11.0: terminating the pool's sole
// backend makes the next store.View surface `FATAL: terminating connection due
// to administrator command (SQLSTATE 57P01)` straight from database/sql, class
// 57 and not 08, and a silent sever (TestPostgresPoolSeveredFirstRequestIsUnavailable,
// the commit-outcome proxy fixture's cut, the regression's 19-of-30 observation)
// surfaces pgx's connLockError "conn closed" from BeginTx itself, which unwraps
// to pgconn.ErrConnClosed. Neither shape is driver.ErrBadConn, so database/sql
// does not retry it: the first request answers with the error itself. Before
// this fix both shapes escaped isBackendUnavailable, and view() returned its
// BeginTx error unwrapped even for the shapes that did classify, so the first
// request after a pool reset reached callers raw instead of as
// store.ErrStoreUnavailable. core/api maps that sentinel to 503 store_unavailable
// (poolreset_pg_test.go in core/api shows the status a client sees).
//
// The request AFTER the first was always fine — database/sql discards the dead
// driver connection and dials a fresh one (measured in the same harness) — so
// exactly one request per pooled connection is at stake. That transparent
// recovery is the second half of this test: a fix that answered 503 forever
// would pass the first assertions and fail these.
func TestPostgresPoolResetFirstRequestIsUnavailable(t *testing.T) {
	pg := isolatedPG(t)
	// The package's boot budget (pgisolation_test.go): this context covers a
	// full Open with migrations, and a hung boot must fail with the deadline
	// as its diagnosis rather than ride to the binary-wide timeout.
	ctx, cancel := context.WithTimeout(context.Background(), postgresBootBudget)
	defer cancel()
	// MaxConns 1: the pool owns exactly one connection, so the termination
	// below is certain to be the connection the next request checks out, with
	// no second idle connection to hide behind.
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 1}, registerWidget)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer st.Close()
	tenant := provisionTenant(t, st, "pgreset-"+uniqueSuffix())
	agent := mustCreateAgent(t, st, tenant, "warm")
	s := st.(*sqlStore)

	killer, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open control pool: %v", err)
	}
	defer killer.Close()
	terminatePoolBackend := func() {
		t.Helper()
		var pid int64
		// The pid query rides the pool's own healthy connection, so it is also
		// the checkout that guarantees the backend about to be terminated is
		// the one the next store call will use.
		if err := s.db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatalf("backend pid: %v", err)
		}
		if _, err := killer.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
			t.Fatalf("terminate backend %d: %v", pid, err)
		}
	}
	viewAgent := func() error {
		return st.View(ctx, tenant, func(sc store.Scope) error {
			_, e := sc.Agents().Get(ctx, agent.ID)
			return e
		})
	}

	// The FIRST read after the reset: classified as the store being
	// unavailable, with the server's own diagnosis still matchable in the chain.
	terminatePoolBackend()
	err = viewAgent()
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("first View after pool reset: err = %v, want store.ErrStoreUnavailable in its chain", err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("first View after pool reset: err = %v, want the server's PgError preserved by the wrap", err)
	}

	// The request after the first recovers transparently: database/sql
	// discards the dead connection and the pool dials a fresh one.
	if err := viewAgent(); err != nil {
		t.Fatalf("second View after pool reset: err = %v, want a transparent recovery", err)
	}

	// The FIRST write after a reset: the same classification.
	terminatePoolBackend()
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Agents().Create(ctx, model.Agent{Name: "post-reset"})
		return e
	})
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("first Mutate after pool reset: err = %v, want store.ErrStoreUnavailable in its chain", err)
	}

	// And the next write goes through on the fresh connection.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Agents().Create(ctx, model.Agent{Name: "post-recovery"})
		return e
	}); err != nil {
		t.Fatalf("second Mutate after pool reset: err = %v, want a transparent recovery", err)
	}
}

// TestPostgresPoolSeveredFirstRequestIsUnavailable is the regression's own shape: the
// pool's connections are cut with no notice (the server stays up and nothing
// pings first), then the first request of each store entry point runs. Whether
// the driver notices the cut before BeginTx is a race, so a request either
// recovers on a fresh connection (nil) or fails as ErrStoreUnavailable with
// pgx's "conn closed" still in the chain. A raw error is the defect. Every
// entry point is exercised, because each has its own BeginTx: View, Mutate and
// the two selective envelopes.
func TestPostgresPoolSeveredFirstRequestIsUnavailable(t *testing.T) {
	const rounds = 20
	pg := isolatedPG(t)
	proxy := pgtest.NewSeverProxy(t, pg.App)
	ctx, cancel := context.WithTimeout(context.Background(), postgresBootBudget)
	defer cancel()
	// MaxConns 1: the cut connection is the one the next request checks out.
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: proxy.DSN(), MaxConns: 1}, registerWidget)
	if err != nil {
		t.Fatalf("open postgres through the sever proxy: %v", err)
	}
	defer st.Close()
	tenant := provisionTenant(t, st, "pgsever-"+uniqueSuffix())
	selective := st.(store.SelectiveMutator)
	keyPlan, err := store.NewTransactionLockPlan("poolreset:sever")
	if err != nil {
		t.Fatal(err)
	}
	opPlan, err := store.NewEvidenceOperationPlan("poolreset-sever")
	if err != nil {
		t.Fatal(err)
	}
	// The audit reader holds its own pool on the same database (the audit CLI
	// reads through it, cmd/olivares/cmd_audit_tree.go), so its begin path is an
	// entry point of its own.
	auditR, err := OpenAuditReader(ctx, store.Config{Engine: store.EnginePostgres, DSN: proxy.DSN(), MaxConns: 1})
	if err != nil {
		t.Fatalf("open audit reader through the sever proxy: %v", err)
	}
	defer auditR.Close()
	entryPoints := []struct {
		name string
		run  func() error
	}{
		{"View", func() error {
			return st.View(ctx, tenant, func(store.Scope) error { return nil })
		}},
		{"Mutate", func() error {
			return st.Mutate(ctx, tenant, func(store.Scope) error { return nil })
		}},
		{"MutateCoordination", func() error {
			return selective.MutateCoordination(ctx, tenant, keyPlan, func(store.CoordinationMutationScope) error { return nil })
		}},
		{"MutateEvidenceOperation", func() error {
			return selective.MutateEvidenceOperation(ctx, tenant, opPlan, func(store.EvidenceOperationMutationScope) error { return nil })
		}},
		{"System", func() error {
			return st.System(ctx, func(store.SystemScope) error { return nil })
		}},
		{"ViewAudit", func() error {
			return auditR.ViewAudit(ctx, tenant, func(store.AuditLog) error { return nil })
		}},
		{"SetRolloutMode", func() error {
			// No such control: a request whose transaction began reaches the row
			// lookup and answers ErrNotFound, which is the healthy outcome here.
			_, err := st.(store.RolloutStater).SetRolloutMode(ctx, store.RolloutTransition{
				Key: "poolreset-sever", Mode: store.RolloutEnforced, Actor: "test", Reason: "the regression",
			})
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}},
	}
	for _, ep := range entryPoints {
		t.Run(ep.name, func(t *testing.T) {
			// Every entry shares the proxy, so an earlier entry's sever has cut
			// this entry's pooled connection too; its next request is the
			// transparent-recovery one (nil or, when the dead connection is the
			// one checked out, the unavailable answer under test). Warm the pool
			// — one ignored request, then a live one — so round 0 starts live.
			_ = ep.run()
			if err := ep.run(); err != nil {
				t.Fatalf("warm-up: the second %s request did not run on a fresh connection: %v", ep.name, err)
			}
			unavailable := 0
			for i := 0; i < rounds; i++ {
				// A healthy request first, so the pool holds a live connection to cut.
				if err := ep.run(); err != nil {
					t.Fatalf("round %d: request before the sever: %v", i, err)
				}
				proxy.Sever()
				err := ep.run()
				switch {
				case err == nil:
				case errors.Is(err, store.ErrStoreUnavailable):
					unavailable++
				default:
					t.Fatalf("round %d: first %s after the sever = %v, want nil or store.ErrStoreUnavailable", i, ep.name, err)
				}
			}
			if unavailable == 0 {
				t.Fatalf("COULD_NOT_LOOK: %d severs and no first %s met the dead connection, so nothing was measured", rounds, ep.name)
			}
			t.Logf("%s: %d of %d first requests after a sever were store.ErrStoreUnavailable, the rest recovered", ep.name, unavailable, rounds)
			// The request after a failed one runs on a fresh connection.
			if err := ep.run(); err != nil {
				t.Fatalf("%s after the failed first request: %v, want a transparent recovery", ep.name, err)
			}
		})
	}
}
