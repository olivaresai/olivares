// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSelectiveMutationSQLitePlansScopesAndPoison(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "selective-sqlite")
	selective, ok := st.(store.SelectiveMutator)
	if !ok {
		t.Fatal("SQL store does not expose SelectiveMutator")
	}

	plan, err := store.NewTransactionLockPlan("key:b", "key:a", "key:b")
	if err != nil {
		t.Fatal(err)
	}
	if err := selective.MutateCoordination(ctx, tenant, plan, func(sc store.CoordinationMutationScope) error {
		if sc.Tenant() != tenant {
			t.Fatalf("tenant = %s, want %s", sc.Tenant(), tenant)
		}
		if _, err := sc.Org(ctx); err != nil {
			return err
		}
		if _, err := sc.TransactionNow(ctx); err != nil {
			return err
		}
		if _, ok := any(sc).(store.Scope); ok {
			t.Fatal("coordination scope can be asserted to full Scope")
		}
		if _, ok := any(sc).(store.TransactionLocker); ok {
			t.Fatal("coordination scope exposes a dynamic TransactionLocker")
		}
		if _, ok := any(sc).(store.AuthoritySnapshotLocker); ok {
			t.Fatal("coordination scope exposes authority")
		}
		if _, ok := any(sc).(store.EvidenceOperationMutationScope); ok {
			t.Fatal("coordination scope exposes evidence operations")
		}
		return nil
	}); err != nil {
		t.Fatalf("coordination transaction: %v", err)
	}

	called := false
	if err := selective.MutateCoordination(ctx, tenant, store.TransactionLockPlan{}, func(store.CoordinationMutationScope) error {
		called = true
		return nil
	}); !errors.Is(err, store.ErrSelectiveMutationPlan) || called {
		t.Fatalf("zero coordination plan = %v called=%t", err, called)
	}
	if err := selective.MutateEvidenceOperation(ctx, tenant, store.EvidenceOperationPlan{}, func(store.EvidenceOperationMutationScope) error {
		called = true
		return nil
	}); !errors.Is(err, store.ErrSelectiveMutationPlan) || called {
		t.Fatalf("zero evidence plan = %v called=%t", err, called)
	}
	if err := selective.MutateCoordination(ctx, model.SystemTenantID, plan, func(store.CoordinationMutationScope) error {
		called = true
		return nil
	}); !errors.Is(err, store.ErrSelectiveMutationPlan) || called {
		t.Fatalf("SYSTEM selective transaction = %v called=%t", err, called)
	}

	evidencePlan, err := store.NewEvidenceOperationPlan("planned-op")
	if err != nil {
		t.Fatal(err)
	}
	beforeAudit := countAuditAction(t, st, tenant, "mcp.tool.call.claim")
	err = selective.MutateEvidenceOperation(ctx, tenant, evidencePlan, func(sc store.EvidenceOperationMutationScope) error {
		if _, ok := any(sc).(store.Scope); ok {
			t.Fatal("evidence scope can be asserted to full Scope")
		}
		if _, ok := any(sc).(store.TransactionClock); ok {
			t.Fatal("evidence scope exposes transaction clock")
		}
		if _, ok := any(sc).(store.TransactionLocker); ok {
			t.Fatal("evidence scope exposes transaction locks")
		}
		if _, ok := any(sc).(store.AuthoritySnapshotLocker); ok {
			t.Fatal("evidence scope exposes authority")
		}
		_, mismatch := sc.EvidenceOperations().Claim(ctx, testClaim("other-op", "digest"))
		if !errors.Is(mismatch, store.ErrSelectiveMutationPlan) {
			t.Fatalf("unplanned operation = %v, want plan violation", mismatch)
		}
		return nil // discarding the repository error must still poison the transaction
	})
	if !errors.Is(err, store.ErrSelectiveMutationPlan) {
		t.Fatalf("discarded plan violation committed: %v", err)
	}
	if got := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); got != beforeAudit {
		t.Fatalf("plan violation changed audit count: got %d want %d", got, beforeAudit)
	}
	if _, err := getEvidenceOp(t, st, tenant, "other-op"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unplanned operation row = %v, want ErrNotFound", err)
	}
}

// TestPostgresSelectiveMutationTransactionFailures kills the exact backend
// owning the narrow transaction at each outcome boundary. A callback failure
// must retain its primary cause and report an unconfirmed rollback; a Commit
// failure remains ambiguous and must not be rewritten as a rollback claim.
func TestPostgresSelectiveMutationTransactionFailures(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPG(t)
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 2}, nil)
	if err != nil {
		t.Fatalf("open PostgreSQL store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	killer, err := openPGPinnedToEngineSchema(pg.Superuser, 1)
	if err != nil {
		t.Fatalf("open PostgreSQL terminator: %v", err)
	}
	t.Cleanup(func() { _ = killer.Close() })
	tenant := provisionTenant(t, st, "selective-transaction-failures")
	plan, err := store.NewTransactionLockPlan("selective:transaction:failure")
	if err != nil {
		t.Fatal(err)
	}
	raw := st.(*sqlStore)
	selective := st.(store.SelectiveMutator)

	t.Run("rollback_failure_joins_primary", func(t *testing.T) {
		primary := errors.New("selective callback primary failure")
		raw.selectiveMutationTestHook = func(hookCtx context.Context, tx *sql.Tx, stage selectiveMutationTestStage) error {
			if stage == selectiveMutationAfterCallbackError {
				terminateSelectiveMutationBackend(t, hookCtx, killer, tx)
			}
			return nil
		}
		t.Cleanup(func() { raw.selectiveMutationTestHook = nil })
		err := selective.MutateCoordination(ctx, tenant, plan, func(store.CoordinationMutationScope) error {
			return primary
		})
		raw.selectiveMutationTestHook = nil
		if !errors.Is(err, primary) {
			t.Fatalf("terminated rollback lost primary error: %v", err)
		}
		if !strings.Contains(err.Error(), "selective mutation rollback could not be confirmed") {
			t.Fatalf("terminated rollback error was not surfaced: %v", err)
		}
	})

	t.Run("commit_failure_remains_ambiguous", func(t *testing.T) {
		raw.selectiveMutationTestHook = func(hookCtx context.Context, tx *sql.Tx, stage selectiveMutationTestStage) error {
			if stage == selectiveMutationBeforeCommit {
				terminateSelectiveMutationBackend(t, hookCtx, killer, tx)
			}
			return nil
		}
		t.Cleanup(func() { raw.selectiveMutationTestHook = nil })
		err := selective.MutateCoordination(ctx, tenant, plan, func(store.CoordinationMutationScope) error {
			return nil
		})
		raw.selectiveMutationTestHook = nil
		if err == nil {
			t.Fatal("terminated commit returned success")
		}
		if strings.Contains(err.Error(), "rollback could not be confirmed") {
			t.Fatalf("commit error was replaced by a rollback claim: %v", err)
		}
	})
}

func terminateSelectiveMutationBackend(
	t *testing.T,
	ctx context.Context,
	killer *sql.DB,
	tx *sql.Tx,
) {
	t.Helper()
	var pid int
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("read selective transaction backend pid: %v", err)
	}
	var terminated bool
	if err := killer.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_terminate_backend($1, 5000)", pid,
	).Scan(&terminated); err != nil {
		t.Fatalf("terminate selective transaction backend %d: %v", pid, err)
	}
	if !terminated {
		t.Fatalf("selective transaction backend %d was not terminated", pid)
	}
}
