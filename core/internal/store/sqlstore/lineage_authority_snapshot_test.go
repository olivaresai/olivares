// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These are STORE witnesses, not unit mocks: the fence a K3 request now depends
// on is the one this transaction actually takes. A request that pins a lineage
// generation must fail closed when that generation has moved, and must hold the
// row while it decides.
func TestLineageAuthoritySnapshotFencesStaleGenerationSQLite(t *testing.T) {
	testLineageAuthoritySnapshotFencesStaleGeneration(t, openSQLiteTest(t, nil))
}

func TestLineageAuthoritySnapshotFencesStaleGenerationPostgres(t *testing.T) {
	testLineageAuthoritySnapshotFencesStaleGeneration(t, openLineagePG(t))
}

func testLineageAuthoritySnapshotFencesStaleGeneration(t *testing.T, st store.Store) {
	ctx := context.Background()
	tenant := provisionTenant(t, st, "lineage-authority-fence")
	other := provisionTenant(t, st, "lineage-authority-foreign")
	lock := func(facts ...store.AuthorizationFactRef) error {
		return st.Mutate(ctx, tenant, func(sc store.Scope) error {
			return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, facts)
		})
	}

	stale := lineageTestFacts(t, st, tenant)["core.workspace"]
	if err := lock(stale); err != nil {
		t.Fatalf("the current workspace lineage generation was refused: %v", err)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "fence", Slug: "fence"})
		return err
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	current := lineageTestFacts(t, st, tenant)["core.workspace"]
	if current.Version != stale.Version+1 {
		t.Fatalf("workspace lineage generation = %d, want %d", current.Version, stale.Version+1)
	}

	if err := lock(stale); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a stale lineage generation was pinned: %v", err)
	}
	if err := lock(current); err != nil {
		t.Fatalf("the advanced lineage generation was refused: %v", err)
	}

	// Every closed lineage kind is lockable, and only on its own tenant.
	facts := lineageTestFacts(t, st, tenant)
	for _, relation := range lineageRelations {
		kind, _ := model.LineageEpochKind(relation.kind)
		fact := facts[relation.kind]
		if fact.Kind != kind || fact.ID != model.ID(tenant) || fact.Version < 1 {
			t.Fatalf("lineage fact for %s = %#v", relation.kind, fact)
		}
		if err := lock(fact); err != nil {
			t.Fatalf("lineage epoch %s was refused: %v", kind, err)
		}
		foreign := store.AuthorizationFactRef{Kind: kind, ID: model.ID(other), Version: 1}
		if err := lock(foreign); err == nil {
			t.Fatalf("lineage epoch %s crossed tenants", kind)
		}
	}

	leased, err := store.NewLeaseFenceAuthorizationFactRef(
		current.Kind, current.ID, current.Version, "subject", 1,
		model.NewTimestamp(time.Now().Add(time.Hour)),
	)
	if err != nil {
		t.Fatalf("build leased reference: %v", err)
	}
	if err := lock(leased); err == nil {
		t.Fatal("a lineage epoch accepted a leased witness its descriptor never declared")
	}
}

// lineageAuthoritySnapshotWaitForBlockedWriter waits until a backend OTHER THAN THE HOLDER
// is blocked on an ungranted lock in this database, which is the observable form of "the
// concurrent bump is now waiting on the snapshot holder".
//
// ⛔ IT REPLACES A `time.Sleep(750 * time.Millisecond)`, and the sleep was wrong in both
// directions. Too long on a quiet box — the writer blocks in a few milliseconds and the
// test paid three quarters of a second for nothing — and, on a contended runner under
// -race, possibly too short: if the writer had not reached its lock yet, the holder
// released first and `bumpedAt.After(releasedAt)` passed without the race ever happening.
// A green that proves nothing is the failure mode this repository keeps paying for.
//
// ⛔ AND THE PREDICATE EXCLUDES THE HOLDER ITSELF. A first version counted ANY ungranted
// lock in the database, so an unrelated waiter — or the holder's own pending request —
// released the snapshot after ~5 ms and reinstated exactly the blind release it was
// written to remove. `pg_backend_pid()` is read through the same admin pool, so the
// exclusion names the session doing the asking, not a guess.
//
// It does NOT fail when the wait is not observed inside the bound: the assertions below are
// untouched and still decide the verdict. It says SO, in the log, in every one of the three
// exits — observed, timed out, or query error — because "released after observing the
// waiter" and "released blind because the probe failed" produce the same duration and must
// not produce the same silence.
func lineageAuthoritySnapshotWaitForBlockedWriter(t *testing.T, ss *sqlStore, holderPID int) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		err := ss.adminDB.QueryRowContext(context.Background(),
			`SELECT count(*) FROM pg_catalog.pg_locks l
			 JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
			 WHERE a.datname = current_database() AND NOT l.granted AND l.pid <> $1`,
			holderPID).Scan(&blocked)
		if err != nil {
			t.Logf("RELEASED BLIND: the probe for the blocked concurrent bump failed after %s: %v",
				time.Since(started), err)
			return
		}
		if blocked > 0 {
			t.Logf("the concurrent bump was observed blocked after %s", time.Since(started))
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Logf("RELEASED BLIND: the concurrent bump was never observed blocked within %s; the "+
		"assertions below still decide the verdict", time.Since(started))
}

// TestLineageAuthoritySnapshotHoldsAgainstAConcurrentBumpPostgres shows what a
// K3 write is protected by while its PostgreSQL transaction waits: a
// concurrent writer that would advance the pinned generation cannot commit
// until the snapshot holder releases the row, and the pin it took is refused
// afterwards.
func TestLineageAuthoritySnapshotHoldsAgainstAConcurrentBumpPostgres(t *testing.T) {
	st := openLineagePG(t)
	ss := st.(*sqlStore)
	ctx := context.Background()
	tenant := provisionTenant(t, st, "lineage-authority-wait")
	pinned := lineageTestFacts(t, st, tenant)["core.workspace"]

	locked := make(chan struct{})
	released := make(chan time.Time, 1)
	bumped := make(chan time.Time, 1)
	writer := make(chan error, 1)

	go func() {
		<-locked
		writer <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "waiter", Slug: "waiter"})
			return err
		})
		bumped <- time.Now()
	}()

	holderErr := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
			ctx, []store.AuthorizationFactRef{pinned},
		); err != nil {
			return err
		}
		close(locked)
		var holderPID int
		if err := sc.(*tenantScope).tx.QueryRowContext(ctx,
			`SELECT pg_catalog.pg_backend_pid()`).Scan(&holderPID); err != nil {
			return err
		}
		lineageAuthoritySnapshotWaitForBlockedWriter(t, ss, holderPID)
		released <- time.Now()
		return nil
	})
	if holderErr != nil {
		t.Fatalf("snapshot holder: %v", holderErr)
	}
	if err := <-writer; err != nil {
		t.Fatalf("concurrent lineage writer: %v", err)
	}
	releasedAt, bumpedAt := <-released, <-bumped
	if !bumpedAt.After(releasedAt) {
		t.Fatalf("the concurrent bump committed at %s, before the snapshot released at %s",
			bumpedAt, releasedAt)
	}

	after := lineageTestFacts(t, st, tenant)["core.workspace"]
	if after.Version != pinned.Version+1 {
		t.Fatalf("workspace lineage generation = %d, want %d", after.Version, pinned.Version+1)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(
			ctx, []store.AuthorizationFactRef{pinned},
		)
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("the generation the waiter advanced is still pinnable: %v", err)
	}
}
