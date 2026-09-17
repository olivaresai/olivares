// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

// selectiveCallbackTimeout bounds every wait in this file. It is a deadline for
// a step that must complete, never a sleep whose expiry is read as evidence: the
// one place a budget has to elapse to mean anything (selectiveHoldBudget) says so
// and carries positive observations on both sides of it.
const selectiveCallbackTimeout = 30 * time.Second

// selectiveHoldBudget is how long the L0/planned-key exclusion is watched for a
// break AFTER the request is canceled. Its expiry is not by itself the finding:
// the assertions around it are positive engine observations (a concurrent backend
// still blocked on a lock this transaction holds, this transaction still open,
// and finally a live query through the transaction itself).
const selectiveHoldBudget = 2 * time.Second

// selectiveTxOf reaches the exact transaction behind a coordination scope. It
// returns an error rather than failing the test, because its callers run inside
// callbacks on their own goroutines, where t.Fatal is not allowed.
func selectiveTxOf(sc store.CoordinationMutationScope) (*sql.Tx, error) {
	inner, ok := sc.(*coordinationMutationScope)
	if !ok {
		return nil, fmt.Errorf("coordination scope is %T, not the package's own implementation", sc)
	}
	return inner.sc.tx, nil
}

func selectiveEvidenceTxOf(t *testing.T, sc store.EvidenceOperationMutationScope) *sql.Tx {
	t.Helper()
	inner, ok := sc.(*evidenceOperationMutationScope)
	if !ok {
		t.Fatalf("evidence scope is %T, not the package's own implementation", sc)
	}
	return inner.sc.tx
}

// TestSelectiveOwnedTransactionCancellationStages covers the cancellation stages
// that need no engine-level lock observation: before acquisition, during
// acquisition, and after the callback returned. SQLite's pool is one connection
// wide, which makes the acquisition wait exact rather than arranged.
func TestSelectiveOwnedTransactionCancellationStages(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "selective-cancellation-stages")
	selective := st.(store.SelectiveMutator)
	keyPlan, err := store.NewTransactionLockPlan("selective:cancellation")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("canceled_before_acquisition", func(t *testing.T) {
		dead, cancel := context.WithCancel(ctx)
		cancel()
		ran := false
		err := selective.MutateCoordination(dead, tenant, keyPlan,
			func(store.CoordinationMutationScope) error { ran = true; return nil })
		if ran {
			t.Error("a callback ran for a request that was canceled before acquisition")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want one wrapping context.Canceled", err)
		}
	})

	t.Run("canceled_during_acquisition", func(t *testing.T) {
		// The relay exists so that owning the transaction context does not make
		// connection acquisition deaf to the caller. SQLite's single connection is
		// held by the first transaction, so the second one waits inside BeginTx and
		// can only be released by the relay.
		holding, releaseHolder := make(chan struct{}), make(chan struct{})
		holderDone := make(chan error, 1)
		go func() {
			holderDone <- selective.MutateCoordination(ctx, tenant, keyPlan,
				func(store.CoordinationMutationScope) error {
					close(holding)
					<-releaseHolder
					return nil
				})
		}()
		select {
		case <-holding:
		case err := <-holderDone:
			t.Fatalf("holder never entered its callback: %v", err)
		case <-time.After(selectiveCallbackTimeout):
			t.Fatal("holder callback entry timed out")
		}
		waitCtx, cancelWaiter := context.WithCancel(ctx)
		waiterRan := false
		waiterDone := make(chan error, 1)
		go func() {
			waiterDone <- selective.MutateCoordination(waitCtx, tenant, keyPlan,
				func(store.CoordinationMutationScope) error { waiterRan = true; return nil })
		}()
		select {
		case err := <-waiterDone:
			close(releaseHolder)
			<-holderDone
			t.Fatalf("the waiter acquired a connection the holder owns: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		cancelWaiter()
		select {
		case err := <-waiterDone:
			if waiterRan {
				t.Error("a callback ran for a request canceled while it waited for a connection")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("waiter error = %v, want one wrapping context.Canceled", err)
			}
		case <-time.After(selectiveCallbackTimeout):
			close(releaseHolder)
			<-holderDone
			t.Fatal("cancelling the request did not release a waiter blocked in acquisition")
		}
		close(releaseHolder)
		if err := <-holderDone; err != nil {
			t.Fatalf("holder transaction: %v", err)
		}
	})

	t.Run("canceled_inside_callback_does_not_commit", func(t *testing.T) {
		// A callback that ignores its request context and returns nil must not turn
		// a canceled request into a commit. The transaction is still the envelope's
		// — proven from inside the callback, after the cancellation — so the refusal
		// below is a decision, not the driver rolling the work out from under it.
		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		const opID = "selective-cancel-before-commit"
		plan, err := store.NewEvidenceOperationPlan(opID)
		if err != nil {
			t.Fatal(err)
		}
		before := countAuditAction(t, st, tenant, "mcp.tool.call.claim")
		var liveErr error
		err = selective.MutateEvidenceOperation(callCtx, tenant, plan,
			func(sc store.EvidenceOperationMutationScope) error {
				if _, err := sc.EvidenceOperations().Claim(ctx, testClaim(opID, "digest")); err != nil {
					return err
				}
				tx := selectiveEvidenceTxOf(t, sc)
				cancel()
				<-callCtx.Done()
				var one int
				liveErr = tx.QueryRowContext(context.Background(), "SELECT 1").Scan(&one)
				return nil
			})
		if liveErr != nil {
			t.Errorf("the owned transaction died with the request: %v", liveErr)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want one wrapping context.Canceled", err)
		}
		if _, readErr := getEvidenceOp(t, st, tenant, opID); !errors.Is(readErr, store.ErrNotFound) {
			t.Errorf("a canceled request committed its claim: %v", readErr)
		}
		if got := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); got != before {
			t.Errorf("audit count = %d, want %d", got, before)
		}
	})

	t.Run("callback_error_and_panic_retain_checked_cleanup", func(t *testing.T) {
		primary := errors.New("selective callback primary")
		for _, kind := range []string{"error", "panic"} {
			t.Run(kind, func(t *testing.T) {
				opID := "selective-cleanup-" + kind
				plan, err := store.NewEvidenceOperationPlan(opID)
				if err != nil {
					t.Fatal(err)
				}
				before := countAuditAction(t, st, tenant, "mcp.tool.call.claim")
				var escaped store.EvidenceOperationMutationScope
				var got error
				panicked := false
				func() {
					defer func() {
						if r := recover(); r != nil {
							panicked = true
						}
					}()
					got = selective.MutateEvidenceOperation(ctx, tenant, plan,
						func(sc store.EvidenceOperationMutationScope) error {
							escaped = sc
							if _, err := sc.EvidenceOperations().Claim(ctx, testClaim(opID, "digest")); err != nil {
								return err
							}
							if kind == "panic" {
								panic(primary)
							}
							return primary
						})
				}()
				if kind == "panic" && !panicked {
					t.Fatal("the panic did not propagate through the envelope")
				}
				if kind == "error" && !errors.Is(got, primary) {
					t.Fatalf("error = %v, want the callback's primary error", got)
				}
				if _, readErr := getEvidenceOp(t, st, tenant, opID); !errors.Is(readErr, store.ErrNotFound) {
					t.Errorf("journal residue after a failed callback: %v", readErr)
				}
				if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != before {
					t.Errorf("audit residue after a failed callback: %d, want %d", n, before)
				}
				if _, err := escaped.EvidenceOperations().Claim(ctx, testClaim(opID, "digest")); err == nil {
					t.Error("the escaped repository still worked after cleanup")
				}
			})
		}
	})

	t.Run("no_leaked_connection_after_the_cancellations_above", func(t *testing.T) {
		// SQLite's pool is one connection wide, so a single relay, waiter or
		// transaction left behind by any subtest above makes this hang rather than
		// merely slow down.
		for i := 0; i < 3; i++ {
			if err := selective.MutateCoordination(ctx, tenant, keyPlan,
				func(sc store.CoordinationMutationScope) error {
					_, err := sc.TransactionNow(ctx)
					return err
				}); err != nil {
				t.Fatalf("selective transaction %d after the cancellation stages: %v", i, err)
			}
		}
	})
}

// selectiveBlockedOnLocksHeldBy counts backends that are WAITING for an advisory
// lock this pid already holds. It is a positive engine observation of exclusion:
// unlike a timeout, it names both sides of the wait.
func selectiveBlockedOnLocksHeldBy(t *testing.T, observer *sql.DB, pid int) int {
	t.Helper()
	var blocked int
	if err := observer.QueryRowContext(context.Background(), `
		SELECT count(*)
		  FROM pg_catalog.pg_locks blocked
		  JOIN pg_catalog.pg_locks holder
		    ON holder.locktype = 'advisory' AND blocked.locktype = 'advisory'
		   AND holder.database = blocked.database
		   AND holder.classid  = blocked.classid
		   AND holder.objid    = blocked.objid
		   AND holder.objsubid = blocked.objsubid
		 WHERE holder.pid = $1 AND holder.granted
		   AND blocked.pid <> $1 AND NOT blocked.granted`, pid).Scan(&blocked); err != nil {
		t.Fatalf("observe backends blocked on locks held by %d: %v", pid, err)
	}
	return blocked
}

// selectiveTransactionOpen reports whether the backend still has an open
// transaction. database/sql's automatic rollback closes it, so this distinguishes
// "still holding" from "already released" without inferring either from elapsed
// time.
func selectiveTransactionOpen(t *testing.T, observer *sql.DB, pid int) bool {
	t.Helper()
	var open bool
	err := observer.QueryRowContext(context.Background(),
		`SELECT xact_start IS NOT NULL FROM pg_catalog.pg_stat_activity WHERE pid = $1`, pid).Scan(&open)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("observe transaction state of backend %d: %v", pid, err)
	}
	return open
}

func selectiveWaitForBlockers(t *testing.T, observer *sql.DB, pid, want int) {
	t.Helper()
	deadline := time.Now().Add(selectiveCallbackTimeout)
	for {
		if got := selectiveBlockedOnLocksHeldBy(t, observer, pid); got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d backends ever blocked on locks held by %d, want %d",
				selectiveBlockedOnLocksHeldBy(t, observer, pid), pid, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPostgresSelectiveOwnedTransactionLifetime is the causal for the
// coordination contract this envelope publishes: L0 and the planned keys are held
// until the guarded callback's own cleanup, not until the caller's request
// context happens to end.
//
// Both wrapper orders run, because the policy check the wrappers fold into the
// callback is part of what has to stay inside the held transaction.
func TestPostgresSelectiveOwnedTransactionLifetime(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 8,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner PostgreSQL store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	observer, err := openPGPinnedToEngineSchema(pg.Superuser, 2)
	if err != nil {
		t.Fatalf("open lock observer: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })

	tenant := provisionTenant(t, st, "selective-owned-lifetime")
	reg, err := residency.NewRegistry("eu", []string{"eu"})
	if err != nil {
		t.Fatal(err)
	}
	keyPlan, err := store.NewTransactionLockPlan("selective:owned:lifetime")
	if err != nil {
		t.Fatal(err)
	}

	for _, order := range []string{"suspension(residency)", "residency(suspension)"} {
		t.Run(order, func(t *testing.T) {
			if err := st.System(ctx, func(sys store.SystemScope) error {
				_, err := sys.SetOrgRegion(ctx, tenant, "eu")
				return err
			}); err != nil {
				t.Fatalf("repin tenant to its home region: %v", err)
			}
			wrapped := suspension.Guard(residency.Guard(st, reg, nil), nil)
			if order == "residency(suspension)" {
				wrapped = residency.Guard(suspension.Guard(st, nil), reg, nil)
			}
			selective, ok := wrapped.(store.SelectiveMutator)
			if !ok {
				t.Fatalf("%s: the wrapper stack swallowed SelectiveMutator", order)
			}

			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			entered := make(chan int, 1)
			observed, release := make(chan struct{}), make(chan struct{})
			var liveErr error
			held := make(chan error, 1)
			go func() {
				held <- selective.MutateCoordination(callCtx, tenant, keyPlan,
					func(sc store.CoordinationMutationScope) error {
						tx, err := selectiveTxOf(sc)
						if err != nil {
							return err
						}
						var pid int
						if err := tx.QueryRowContext(ctx,
							"SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil {
							return fmt.Errorf("read the held backend pid: %w", err)
						}
						entered <- pid
						<-observed
						// The request is canceled by now. If the envelope did not own
						// this transaction, database/sql has already rolled it back and
						// this query fails: the callback is still running either way.
						var one int
						liveErr = tx.QueryRowContext(
							context.Background(), "SELECT 1").Scan(&one)
						<-release
						return nil
					})
			}()

			var pid int
			select {
			case pid = <-entered:
			case err := <-held:
				t.Fatalf("%s: the guarded callback never entered: %v", order, err)
			case <-time.After(selectiveCallbackTimeout):
				t.Fatalf("%s: guarded callback entry timed out", order)
			}

			// Two independent claims on what this transaction holds: a System
			// lifecycle mutation (L0 exclusive) and another selective transaction
			// on the SAME planned key. Both are observed BLOCKED on locks this
			// backend holds before anything is canceled.
			systemDone := make(chan error, 1)
			go func() {
				systemDone <- st.System(ctx, func(sys store.SystemScope) error {
					_, err := sys.SetOrgRegion(ctx, tenant, "us")
					return err
				})
			}()
			selectiveWaitForBlockers(t, observer, pid, 1)
			sameKeyRan := false
			sameKeyDone := make(chan error, 1)
			go func() {
				sameKeyDone <- st.(store.SelectiveMutator).MutateCoordination(ctx, tenant, keyPlan,
					func(store.CoordinationMutationScope) error { sameKeyRan = true; return nil })
			}()
			selectiveWaitForBlockers(t, observer, pid, 2)
			t.Logf("%s: two backends blocked on locks held by the live callback's backend %d",
				order, pid)

			// Cancel ONLY the request. The callback has not returned.
			cancel()
			<-callCtx.Done()
			deadline := time.Now().Add(selectiveHoldBudget)
			for time.Now().Before(deadline) {
				select {
				case err := <-systemDone:
					close(observed)
					close(release)
					<-held
					t.Fatalf("%s: the System repin committed (%v) while the guarded callback was still live",
						order, err)
				case err := <-sameKeyDone:
					close(observed)
					close(release)
					<-held
					t.Fatalf("%s: the same planned key was granted (%v, ran=%t) while the guarded callback was still live",
						order, err, sameKeyRan)
				default:
				}
				if !selectiveTransactionOpen(t, observer, pid) {
					close(observed)
					close(release)
					<-held
					t.Fatalf("%s: backend %d's transaction ended with the request, while its callback was still live",
						order, pid)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if got := selectiveBlockedOnLocksHeldBy(t, observer, pid); got < 2 {
				close(observed)
				close(release)
				<-held
				t.Fatalf("%s: after cancellation only %d backends are still blocked on locks held by %d, want 2",
					order, got, pid)
			}
			t.Logf("%s: after request cancellation the callback's backend still holds its locks and both claimants still wait",
				order)

			close(observed)
			// liveErr is written before release is read and read after held is
			// received, so the callback's own query result is ordered by channels.
			close(release)
			err := <-held
			if liveErr != nil {
				t.Errorf("%s: the owned transaction died with the request: %v", order, liveErr)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: released callback returned %v, want one wrapping context.Canceled", order, err)
			}

			// Eventual release: neither claimant is stranded by the ownership.
			select {
			case err := <-systemDone:
				if err != nil {
					t.Errorf("%s: System repin after release: %v", order, err)
				}
			case <-time.After(selectiveCallbackTimeout):
				t.Errorf("%s: the System repin never completed after the callback released", order)
			}
			select {
			case err := <-sameKeyDone:
				if err != nil || !sameKeyRan {
					t.Errorf("%s: same-key selective transaction after release ran=%t err=%v",
						order, sameKeyRan, err)
				}
			case <-time.After(selectiveCallbackTimeout):
				t.Errorf("%s: the same planned key was never released", order)
			}
		})
	}
}

// TestPostgresSelectiveCancelDuringPlannedKeyWait pins the other half of the
// relay's purpose: the planned keys are still waited for on the REQUEST context,
// so a caller queued behind another holder is released when it gives up, and no
// callback runs for it.
func TestPostgresSelectiveCancelDuringPlannedKeyWait(t *testing.T) {
	ctx := context.Background()
	st := openSelectivePGSplit(t, 6)
	tenant := provisionTenant(t, st, "selective-key-wait")
	selective := st.(store.SelectiveMutator)
	keyPlan, err := store.NewTransactionLockPlan("selective:key:wait")
	if err != nil {
		t.Fatal(err)
	}
	holding, releaseHolder := make(chan struct{}), make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- selective.MutateCoordination(ctx, tenant, keyPlan,
			func(store.CoordinationMutationScope) error {
				close(holding)
				<-releaseHolder
				return nil
			})
	}()
	select {
	case <-holding:
	case err := <-holderDone:
		t.Fatalf("holder never entered its callback: %v", err)
	case <-time.After(selectiveCallbackTimeout):
		t.Fatal("holder callback entry timed out")
	}
	waitCtx, cancelWaiter := context.WithCancel(ctx)
	waiterRan := false
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- selective.MutateCoordination(waitCtx, tenant, keyPlan,
			func(store.CoordinationMutationScope) error { waiterRan = true; return nil })
	}()
	select {
	case err := <-waiterDone:
		close(releaseHolder)
		<-holderDone
		t.Fatalf("the waiter took a planned key the holder owns: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	cancelWaiter()
	select {
	case err := <-waiterDone:
		if waiterRan {
			t.Error("a callback ran for a request canceled while it waited for its planned key")
		}
		if err == nil {
			t.Error("a canceled planned-key waiter returned success")
		}
		t.Logf("canceled planned-key waiter released with: %v", err)
	case <-time.After(selectiveCallbackTimeout):
		close(releaseHolder)
		<-holderDone
		t.Fatal("cancelling the request did not release a waiter blocked on its planned key")
	}
	close(releaseHolder)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder transaction: %v", err)
	}
	// The holder's key is genuinely free afterwards.
	ran := false
	if err := selective.MutateCoordination(ctx, tenant, keyPlan,
		func(store.CoordinationMutationScope) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("planned key after the canceled wait: ran=%t err=%v", ran, err)
	}
}

// TestPostgresSelectiveLockFootprintExcludesLineageEnrollment is the owned
// permanent form of the lock-footprint control: the envelope takes L0 shared and
// exactly the deduplicated planned keys, and enrolls no lineage writer.
//
// It matters more after this correction than before it. Admission now runs an
// organization read inside the envelope, and an implementation that reached for
// the legacy directory/lineage machinery to get that read would silently acquire
// L1 and a write lock on the writer-marker relation — turning a coordination
// transaction into a lineage writer. The footprint is observed through pg_locks
// from inside the transaction, which needs no privilege the application role
// lacks.
func TestPostgresSelectiveLockFootprintExcludesLineageEnrollment(t *testing.T) {
	ctx := context.Background()
	st := openSelectivePGSplit(t, 4)
	tenant := provisionTenant(t, st, "selective-lock-footprint")
	// Three entries, two distinct keys: the plan deduplicates, so two exclusive
	// advisory locks is the whole planned footprint.
	plan, err := store.NewTransactionLockPlan(
		"selective:footprint:b", "selective:footprint:a", "selective:footprint:b")
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := st.(store.SelectiveMutator).MutateCoordination(ctx, tenant, plan,
		func(sc store.CoordinationMutationScope) error {
			ran = true
			tx, err := selectiveTxOf(sc)
			if err != nil {
				return err
			}
			var shared, exclusive, marker int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FILTER (WHERE mode = 'ShareLock'),
				       count(*) FILTER (WHERE mode = 'ExclusiveLock')
				  FROM pg_catalog.pg_locks
				 WHERE pid = pg_catalog.pg_backend_pid()
				   AND locktype = 'advisory' AND granted`).Scan(&shared, &exclusive); err != nil {
				return fmt.Errorf("observe advisory locks: %w", err)
			}
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FROM pg_catalog.pg_locks
				 WHERE pid = pg_catalog.pg_backend_pid()
				   AND relation = 'public.core_lineage_writer'::regclass
				   AND mode = 'RowExclusiveLock'`).Scan(&marker); err != nil {
				return fmt.Errorf("observe lineage marker relation locks: %w", err)
			}
			t.Logf("selective callback footprint: advisory shared=%d exclusive=%d, lineage marker writes=%d",
				shared, exclusive, marker)
			if shared != 1 {
				t.Errorf("L0 shared advisory locks = %d, want 1", shared)
			}
			if exclusive != 2 {
				t.Errorf("planned-key exclusive advisory locks = %d, want 2 for a deduplicated two-key plan", exclusive)
			}
			if marker != 0 {
				t.Errorf("lineage writer-marker relation write locks = %d, want 0 (no L1/writer enrollment)", marker)
			}
			return nil
		}); err != nil || !ran {
		t.Fatalf("footprint transaction ran=%t err=%v", ran, err)
	}
}
