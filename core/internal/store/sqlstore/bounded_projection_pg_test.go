// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The bounded reader's projections and artifact read on an owned PostgreSQL 16
// engine. PostgreSQL Mutate keeps the reader's selected-row locks for every
// read, including reads of append-only kinds; no read is exempt from them.

// projectionLockAmount selects the lock fixture row by a value the lock holder
// never changes.
const projectionLockAmount = 700001

type projectionPGFixture struct {
	tenant              model.TenantID
	otherWS             model.ID
	single, confinedOwn model.ID
	locked              model.ID
	artifact            model.PolicyArtifact
}

func seedProjectionPG(t *testing.T, st store.Store) projectionPGFixture {
	t.Helper()
	ctx := context.Background()
	var f projectionPGFixture
	f.tenant = provisionTenant(t, st, "projection-pg")
	var defaultWS model.ID
	defaultWS, f.otherWS = distinctProjectionWorkspaces(t, st, f.tenant)
	if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		f.single = seedBoundedItem(ctx, t, sc, defaultWS, "pg-single")
		low := seedBoundedItem(ctx, t, sc, defaultWS, "pg-dup")
		high := seedBoundedItem(ctx, t, sc, defaultWS, "pg-dup")
		pgBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = $1 WHERE id = $2", "far over the note bound", low.String())
		pgBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = 'ok' WHERE id = $1", high.String())
		seedBoundedItem(ctx, t, sc, defaultWS, "pg-shared")
		f.confinedOwn = seedBoundedItem(ctx, t, sc, f.otherWS, "pg-shared")
		f.locked = seedBoundedItem(ctx, t, sc, defaultWS, "pg-lock")
		pgBoundedExec(ctx, t, sc, "UPDATE brt_item SET amount = $1 WHERE id = $2", int64(projectionLockAmount), f.locked.String())
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f.artifact = retainArtifact(t, st, f.tenant, "projection-pg-artifact", "permit(principal, action, resource);")
	return f
}

// TestBoundedProjectionPostgres runs the primitive through two qualified pgx
// modes: the published shape and its witness charge, ambiguity before any row
// statement in View, confinement without fallback, and the bounded artifact
// read's equality and unread denormalized columns.
func TestBoundedProjectionPostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	seedStore := openBoundedPG(t, pg, pgExecModeCacheStatement)
	f := seedProjectionPG(t, seedStore)
	_ = seedStore.Close()

	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx := context.Background()
			st := openBoundedPG(t, pg, mode)
			defer func() { _ = st.Close() }()
			view := func(name string, fn func(sc store.Scope) error) {
				t.Helper()
				if err := st.View(ctx, f.tenant, fn); err != nil {
					t.Errorf("%s view: %v", name, err)
				}
			}

			view("published shape", func(sc store.Scope) error {
				reader := newBoundedTestReader(t, sc, boundedTestLimits())
				rec, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("pg-single"), store.BoundedColumn{Name: "amount"}))
				if err != nil {
					return err
				}
				if want := (model.Record{model.ColID: f.single.String(), "amount": int64(9)}); !reflect.DeepEqual(rec, want) {
					t.Errorf("published %#v, want %#v", rec, want)
				}
				// PostgreSQL observes its settings on every method: 8+(8+5x17) = 101;
				// keys 148, inspection 101 and payload 140 as on SQLite UTF-8.
				if u := reader.Usage(); u.ReservedUnits != 101+148+101+140 || u.PayloadRowsObserved != 1 || u.Terminal {
					t.Errorf("usage %+v", u)
				}
				return nil
			})

			view("ambiguity before row statements", func(sc store.Scope) error {
				reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), 0)
				_, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("pg-dup"), store.BoundedColumn{Name: "note", MaxBytes: 4}))
				if !errors.Is(err, store.ErrBoundedReadMetadata) || errors.Is(err, store.ErrBoundedReadLimit) ||
					!strings.Contains(err.Error(), "selection is not unique") {
					t.Errorf("duplicate: %v", err)
				}
				if probe.counts["keys"] != 1 || probe.counts["lock"] != 0 || probe.counts["inspection"] != 0 || probe.counts["payload"] != 0 {
					t.Errorf("ambiguity decided after a row statement: %s", projectionStatements(probe))
				}
				return nil
			})

			view("confinement", func(sc store.Scope) error {
				confined, err := store.ConfineWorkspace(ctx, sc, f.otherWS)
				if err != nil {
					return err
				}
				reader := newBoundedTestReader(t, confined, boundedTestLimits())
				label := store.BoundedColumn{Name: "label", MaxBytes: 64}
				plain := store.BoundedProjection{Kind: boundedPlainEntity.Kind, Columns: []store.BoundedColumn{label}}
				if _, err := reader.ProjectBoundedOne(ctx, plain); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
					t.Errorf("confined lineage-less projection: %v", err)
				}
				if _, err := reader.GetPolicyArtifact(ctx, f.artifact.ID, projectionArtifactBounds); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
					t.Errorf("confined artifact: %v", err)
				}
				assertBoundedUsageUntouched(t, reader)
				rec, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("pg-shared"), label))
				if err != nil || rec.String(model.ColID) != f.confinedOwn.String() {
					t.Errorf("confined projection: rec=%v err=%v", rec, err)
				}
				return nil
			})

			view("artifact", func(sc store.Scope) error {
				want, err := sc.AccessEvidence().PolicyArtifact(ctx, f.artifact.ID)
				if err != nil {
					return err
				}
				reader, capture := capturedReader(t, sc, boundedTestLimits())
				got, err := reader.GetPolicyArtifact(ctx, f.artifact.ID, projectionArtifactBounds)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("bounded artifact differs from the ordinary read: err=%v", err)
				}
				assertNoDenormalizedArtifactColumns(t, capture)
				tight := projectionArtifactBounds
				tight.MetadataBytes = 8
				limited := newBoundedTestReader(t, sc, boundedTestLimits())
				if _, err := limited.GetPolicyArtifact(ctx, f.artifact.ID, tight); !errors.Is(err, store.ErrBoundedReadLimit) ||
					!strings.Contains(err.Error(), "does not fit MaxBytes") || !limited.Usage().Terminal {
					t.Errorf("metadata over its bound: %v", err)
				}
				assertIdentityDiffersAtLoad(ctx, t, sc, f.artifact.ID, model.TenantID(model.NewID().String()),
					projectionArtifactBounds)
				return nil
			})
		})
	}
}

// TestBoundedProjectionPostgresMutateOneDecidesBeforeLock: in PostgreSQL
// Mutate a duplicate selection is refused with no row lock, inspection or
// payload statement, while a unique selection still locks its row before
// measuring it.
func TestBoundedProjectionPostgresMutateOneDecidesBeforeLock(t *testing.T) {
	pg := isolatedPGSplit(t)
	seedStore := openBoundedPG(t, pg, pgExecModeCacheStatement)
	f := seedProjectionPG(t, seedStore)
	_ = seedStore.Close()

	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx := context.Background()
			st := openBoundedPG(t, pg, mode)
			defer func() { _ = st.Close() }()
			err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
				duplicate, probe := boundedGroupReader(t, sc, boundedTestLimits(), 0)
				_, err := duplicate.ProjectBoundedOne(ctx, projectionOf(labelIs("pg-dup"), store.BoundedColumn{Name: "note", MaxBytes: 4}))
				if !errors.Is(err, store.ErrBoundedReadMetadata) || !strings.Contains(err.Error(), "selection is not unique") {
					t.Errorf("Mutate duplicate: %v", err)
				}
				if probe.counts["lock"] != 0 {
					t.Errorf("One locked a row before deciding cardinality: %s", projectionStatements(probe))
				}
				if probe.counts["inspection"] != 0 || probe.counts["payload"] != 0 {
					t.Errorf("One inspected or loaded a duplicate: %s", projectionStatements(probe))
				}

				single, order := boundedGroupReader(t, sc, boundedTestLimits(), 0)
				if _, err := single.ProjectBoundedOne(ctx, projectionOf(labelIs("pg-single"), store.BoundedColumn{Name: "amount"})); err != nil {
					t.Errorf("Mutate single: %v", err)
				}
				var kinds []string
				for _, s := range order.statements {
					kinds = append(kinds, s.kind)
				}
				if want := []string{"representation", "keys", "lock", "inspection", "payload"}; !reflect.DeepEqual(kinds, want) {
					t.Errorf("Mutate single statements %v, want %v", kinds, want)
				}
				return errBoundedGroupRollback
			})
			if !errors.Is(err, errBoundedGroupRollback) {
				t.Fatalf("mutate: %v", err)
			}
		})
	}
}

// TestBoundedProjectionPostgresMutateLockWaitAndHold keeps the reader's lock
// semantics for a single projection. Wait: a holder's committed growth is
// measured after the observed lock wait. Hold: a later writer is observed
// waiting on the reader's row lock until the reader's transaction ends. The
// bounded deadlines only guard the test; the lock is proven by observation.
func TestBoundedProjectionPostgresMutateLockWaitAndHold(t *testing.T) {
	pg := isolatedPGSplit(t)
	seedStore := openBoundedPG(t, pg, pgExecModeCacheStatement)
	f := seedProjectionPG(t, seedStore)
	_ = seedStore.Close()
	monitor, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	defer func() { _ = monitor.Close() }()
	lockWaiters := func(ctx context.Context, like string) (bool, error) {
		var waiting int
		err := monitor.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_stat_activity "+
			"WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock' "+
			"AND wait_event IN ('transactionid', 'tuple') AND query LIKE $1", like).Scan(&waiting)
		return waiting > 0, err
	}
	byAmount := []model.Filter{{Column: "amount", Op: model.OpEq, Value: int64(projectionLockAmount)}}

	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			st := openBoundedPG(t, pg, mode)
			defer func() { _ = st.Close() }()
			restore := func() {
				t.Helper()
				if _, err := monitor.ExecContext(ctx, "UPDATE public.brt_item SET label = 'pg-lock' WHERE id = $1", f.locked.String()); err != nil {
					t.Fatalf("restore label: %v", err)
				}
			}

			t.Run("wait", func(t *testing.T) {
				restore()
				lr := boundedLockRace{monitor: monitor, st: st, tenant: f.tenant, row: f.locked, budget: 60 * time.Second}
				readErr, err := lr.run(ctx, strings.Repeat("g", 2000), func(qctx context.Context) (bool, error) {
					return lockWaiters(qctx, "%FOR UPDATE")
				}, func(rctx context.Context, r store.BoundedReader) error {
					_, err := r.ProjectBoundedOne(rctx, projectionOf(byAmount, store.BoundedColumn{Name: "label", MaxBytes: 1000}))
					return err
				})
				if err != nil {
					t.Fatalf("growth race: %v", err)
				}
				if !errors.Is(readErr, store.ErrBoundedReadLimit) || !strings.Contains(readErr.Error(), "does not fit MaxBytes") {
					t.Errorf("growth while waiting: %v", readErr)
				}
				restore()
			})

			t.Run("hold", func(t *testing.T) {
				restore()
				writerCtx, cancelWriter := context.WithTimeout(ctx, time.Minute)
				defer cancelWriter()
				writerDone := make(chan error, 1)
				var holdErr error
				err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
					reader := newBoundedTestReader(t, sc, boundedTestLimits())
					if _, err := reader.ProjectBoundedOne(ctx, projectionOf(byAmount, store.BoundedColumn{Name: "label", MaxBytes: 64})); err != nil {
						return err
					}
					// The writer's wait on the row lock is observed in
					// pg_stat_activity; nothing else is taken as its start.
					go func() {
						_, err := monitor.ExecContext(writerCtx, "UPDATE public.brt_item SET label = 'written' WHERE id = $1", f.locked.String())
						writerDone <- err
					}()
					holdErr = awaitProjectionLockWait(ctx, lockWaiters, writerDone)
					return nil
				})
				if err != nil || holdErr != nil {
					t.Fatalf("hold: tx=%v wait=%v", err, holdErr)
				}
				select {
				case err := <-writerDone:
					if err != nil {
						t.Fatalf("writer after the reader's transaction: %v", err)
					}
				case <-time.After(30 * time.Second):
					t.Fatal("writer did not finish after the reader's transaction ended")
				}
				restore()
			})
		})
	}
}

// awaitProjectionLockWait polls until the writer is observed waiting on a row
// lock. A writer that finishes first fails the hold; the bounded deadline
// guards the test and is not evidence of a lock.
func awaitProjectionLockWait(ctx context.Context, lockWaiters func(context.Context, string) (bool, error), writerDone <-chan error) error {
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		waiting, err := lockWaiters(qctx, "UPDATE public.brt_item%")
		cancel()
		if err != nil {
			return fmt.Errorf("observe the writer's lock wait: %w", err)
		}
		if waiting {
			return nil
		}
		select {
		case err := <-writerDone:
			return fmt.Errorf("writer finished while the reader held the row lock: %v", err)
		case <-deadline.C:
			return errors.New("writer never waited on the reader's row lock within 20s")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
