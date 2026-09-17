// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var pgBoundedModes = []pgExecModeFact{
	pgExecModeCacheStatement, pgExecModeCacheDescribe, pgExecModeDescribeExec,
	pgExecModeExec, pgExecModeSimpleProtocol,
}

func openBoundedPG(t *testing.T, pg pgtest.DSNs, mode pgExecModeFact) store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sep := "?"
	if strings.Contains(pg.App, "?") {
		sep = "&"
	}
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App + sep + "default_query_exec_mode=" + mode.String(),
		OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4,
	}, registerBoundedTestEntities)
	if err != nil {
		t.Fatalf("open postgres store (%s): %v", mode, err)
	}
	if got := st.(*sqlStore).pgExecMode; got != mode {
		t.Fatalf("store recorded mode %s, want %s", got, mode)
	}
	return st
}

func pgBoundedExec(ctx context.Context, t *testing.T, sc store.Scope, query string, args ...any) {
	t.Helper()
	if _, err := sc.(*tenantScope).tx.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("raw exec: %v", err)
	}
}

type pgBoundedFixture struct {
	tenant                             model.TenantID
	otherWS                            model.ID
	latin, euro, large                 model.ID
	itemNull, itemEmpty, itemFull, row model.ID
}

func seedBoundedPG(t *testing.T, st store.Store) pgBoundedFixture {
	t.Helper()
	ctx := context.Background()
	var f pgBoundedFixture
	f.tenant = provisionTenant(t, st, "bounded-pg")
	_, f.otherWS = distinctProjectionWorkspaces(t, st, f.tenant)
	if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		specs := map[*model.ID]any{new(model.ID): nil, new(model.ID): "", &f.latin: "ñé", &f.euro: "€",
			&f.large: strings.Repeat("x", 5000), new(model.ID): "{\"exact\":1}"}
		i := 0
		for target, spec := range specs {
			p, err := sc.Policies().Create(ctx, model.Policy{
				Name: "pg" + string(rune('a'+i)), Kind: "recovery", Spec: map[string]any{"seed": true}, Enabled: i%2 == 0,
			})
			if err != nil {
				return err
			}
			i++
			*target = p.ID
			pgBoundedExec(ctx, t, sc, "UPDATE policies SET spec = $1 WHERE id = $2 AND tenant_id = $3",
				spec, p.ID.String(), sc.Tenant().String())
		}
		f.itemNull = seedBoundedItem(ctx, t, sc, ws.ID, "null")
		f.itemEmpty = seedBoundedItem(ctx, t, sc, ws.ID, "empty")
		f.itemFull = seedBoundedItem(ctx, t, sc, ws.ID, "full")
		f.row = seedBoundedItem(ctx, t, sc, ws.ID, "match")
		seedBoundedItem(ctx, t, sc, f.otherWS, "other")
		pgBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = NULL, payload = NULL, ratio = NULL, doc = NULL WHERE id = $1",
			f.itemNull.String())
		pgBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = '', payload = '\\x'::bytea, ratio = 1.5, doc = '{}', active = false WHERE id = $1",
			f.itemEmpty.String())
		pgBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = 'ñé', payload = '\\x000102'::bytea, ratio = 2, amount = $1 WHERE id = $2",
			int64(math.MinInt64), f.itemFull.String())
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return f
}

// TestBoundedReaderPostgresQualifiedModes exercises every accepted pgx mode on
// an owned PostgreSQL 16 engine: R0/generic parity over multi-field paged
// Policy and Ext reads, NULL/empty TEXT and BYTEA, complete page refusal,
// row-wide suppression of every field, transaction-local encoding contraction,
// 22P05 conversion refusal, driver simple-protocol refusal, SQL_ASCII client
// refusal, the text-result BYTEA hex requirement, and confinement.
func TestBoundedReaderPostgresQualifiedModes(t *testing.T) {
	pg := isolatedPGSplit(t)
	seedStore := openBoundedPG(t, pg, pgExecModeCacheStatement)
	f := seedBoundedPG(t, seedStore)
	_ = seedStore.Close()

	for _, mode := range pgBoundedModes {
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

			view("parity", func(sc store.Scope) error {
				want, _, err := policySnapshots(t, sc).ListPolicySnapshots(ctx, model.Query{Limit: 1000})
				if err != nil {
					return err
				}
				reader := newBoundedTestReader(t, sc, boundedTestLimits())
				var got []store.PolicySnapshot
				cursor := ""
				for {
					page, next, err := reader.ListPolicySnapshots(ctx, model.Query{Limit: 2, Cursor: cursor})
					if err != nil {
						return err
					}
					got = append(got, page...)
					if !next.HasMore {
						break
					}
					cursor = next.Cursor
				}
				if len(got) != 6 || !reflect.DeepEqual(got, want) {
					t.Errorf("policy traversal differs from R0: got %d want %d", len(got), len(want))
				}
				repo, err := sc.Ext(boundedTestEntity.Kind)
				if err != nil {
					return err
				}
				generic, _, err := repo.List(ctx, model.Query{Limit: 1000})
				if err != nil {
					return err
				}
				var records []model.Record
				cursor = ""
				for {
					page, next, err := reader.ListExtensions(ctx, boundedTestEntity.Kind, model.Query{Limit: 2, Cursor: cursor})
					if err != nil {
						return err
					}
					records = append(records, page...)
					if !next.HasMore {
						break
					}
					cursor = next.Cursor
				}
				if len(records) != len(generic) {
					t.Fatalf("ext rows %d, generic %d", len(records), len(generic))
				}
				for i, rec := range records {
					for col, v := range generic[i] {
						if col != "payload" && !reflect.DeepEqual(rec[col], v) {
							t.Errorf("row %d column %s: bounded %#v generic %#v", i, col, rec[col], v)
						}
					}
					switch model.ID(rec.String(model.ColID)) {
					case f.itemNull:
						if v, ok := rec["payload"]; !ok || v != nil || rec["note"] != nil {
							t.Errorf("NULL row = %#v", rec)
						}
					case f.itemEmpty:
						if b, ok := rec["payload"].([]byte); !ok || b == nil || len(b) != 0 || rec["note"] != "" || rec["active"] != false {
							t.Errorf("empty row payload %#v note %#v", rec["payload"], rec["note"])
						}
					case f.itemFull:
						if !reflect.DeepEqual(rec["payload"], []byte{0, 1, 2}) || rec["note"] != "ñé" ||
							rec["amount"] != int64(math.MinInt64) {
							t.Errorf("full row = %#v", rec)
						}
					}
				}
				u := reader.Usage()
				if !u.ObservedComplete || u.Terminal || u.ObservedUnits > u.ReservedUnits ||
					u.PayloadRowsObserved != uint64(len(got)+len(records)) {
					t.Errorf("usage: %+v", u)
				}
				return nil
			})

			view("page refusal", func(sc store.Scope) error {
				limits := boundedTestLimits()
				limits.MaxCellBytes = 1000
				reader := newBoundedTestReader(t, sc, limits)
				page, _, err := reader.ListPolicySnapshots(ctx, model.Query{Limit: 10})
				if !errors.Is(err, store.ErrBoundedReadLimit) || page != nil {
					t.Errorf("oversize page: rows=%d err=%v", len(page), err)
				}
				if u := reader.Usage(); u.PayloadRowsReserved != 0 || !u.Terminal {
					t.Errorf("page refusal usage: %+v", u)
				}
				return nil
			})

			view("row-wide suppression", func(sc store.Scope) error {
				reader := newBoundedTestReader(t, sc, boundedTestLimits()).(*boundedReader)
				call, err := reader.begin()
				if err != nil {
					return err
				}
				defer func() { _ = call.finish(nil) }()
				target, err := reader.extensionTarget(boundedTestEntity.Kind)
				if err != nil {
					return err
				}
				enc, err := call.representation(ctx, target)
				if err != nil {
					return err
				}
				where, args, err := target.where(reader.sc.tenant, nil, false)
				if err != nil {
					return err
				}
				row, found, err := call.inspect(ctx, target, where, args, f.itemFull.String(), enc)
				if err != nil || !found {
					t.Fatalf("inspect: found=%v err=%v", found, err)
				}
				admissions := row.admissions
				for i := range admissions {
					if admissions[i].column == "note" {
						if admissions[i].octets != 4 {
							t.Errorf("UTF8 note admitted %d octets, want 4", admissions[i].octets)
						}
						admissions[i].octets-- // one field no longer fits
					}
				}
				before := reader.Usage().ObservedUnits
				rec, err := call.load(ctx, target, boundedPayload{
					relation: target.sql.relation(), where: where, id: f.itemFull.String(), args: args, row: row, pg: true,
				})
				if !errors.Is(err, store.ErrBoundedReadConsistency) || rec != nil ||
					!strings.Contains(err.Error(), "no longer matches") {
					t.Fatalf("row beyond admission: rec=%v err=%v", rec != nil, err)
				}
				shape := boundedResultUnits + boundedRowUnits + boundedFixedUnits +
					boundedNullUnits*uint64(len(admissions)) + boundedFixedUnits*uint64(countNullable(admissions))
				if got := reader.Usage().ObservedUnits - before; got != shape {
					t.Errorf("rejected row observed %d units, want all-NULL %d", got, shape)
				}
				return nil
			})

			if mode == pgExecModeSimpleProtocol {
				// The driver refuses every query once the transaction's client encoding
				// is not UTF8; the reader must report unavailability, not fall back.
				_ = st.View(ctx, f.tenant, func(sc store.Scope) error {
					pgBoundedExec(ctx, t, sc, "SET LOCAL client_encoding = 'LATIN1'")
					reader := newBoundedTestReader(t, sc, boundedTestLimits())
					_, err := reader.GetPolicySnapshot(ctx, f.latin)
					if !errors.Is(err, store.ErrBoundedReadUnavailable) || !strings.Contains(err.Error(), "simple protocol") {
						t.Errorf("simple protocol LATIN1: %v", err)
					}
					if u := reader.Usage(); !u.Terminal || u.ReservedUnits == 0 || u.PayloadRowsReserved != 0 {
						t.Errorf("simple protocol refusal usage: %+v", u)
					}
					return nil
				})
			} else {
				view("LATIN1 client", func(sc store.Scope) error {
					pgBoundedExec(ctx, t, sc, "SET LOCAL client_encoding = 'LATIN1'")
					want, err := policySnapshots(t, sc).GetPolicySnapshot(ctx, f.latin)
					if err != nil {
						return err
					}
					reader := newBoundedTestReader(t, sc, boundedTestLimits())
					got, err := reader.GetPolicySnapshot(ctx, f.latin)
					if err != nil || !reflect.DeepEqual(got, want) || got.Spec == nil || len(*got.Spec) != 2 {
						t.Errorf("LATIN1 contraction: err=%v spec bytes=%v", err, got.Spec != nil && len(*got.Spec) == 2)
					}
					// The admitted bound is the returned client representation (2 bytes),
					// not the 4 UTF8 server octets.
					probe := newBoundedTestReader(t, sc, boundedTestLimits()).(*boundedReader)
					call, err := probe.begin()
					if err != nil {
						return err
					}
					target, _ := probe.policyTarget()
					enc, err := call.representation(ctx, target)
					if err != nil {
						_ = call.finish(err)
						return err
					}
					where, args, _ := target.where(probe.sc.tenant, nil, false)
					row, found, err := call.inspect(ctx, target, where, args, f.latin.String(), enc)
					_ = call.finish(nil)
					if err != nil || !found {
						return err
					}
					for _, a := range row.admissions {
						if a.column == "spec" && (a.octets != 2 || a.bound != 2) {
							t.Errorf("LATIN1 spec admitted octets=%d bound=%d, want 2", a.octets, a.bound)
						}
					}
					euro := newBoundedTestReader(t, sc, boundedTestLimits())
					_, err = euro.GetPolicySnapshot(ctx, f.euro)
					var pgErr *pgconn.PgError
					if !errors.Is(err, store.ErrBoundedReadUnavailable) || !errors.As(err, &pgErr) || pgErr.Code != "22P05" {
						t.Errorf("untranslatable euro: %v", err)
					}
					if !euro.Usage().Terminal {
						t.Error("conversion failure left the reader usable")
					}
					return nil
				})
				_ = st.View(ctx, f.tenant, func(sc store.Scope) error {
					pgBoundedExec(ctx, t, sc, "SET LOCAL client_encoding = 'SQL_ASCII'")
					reader := newBoundedTestReader(t, sc, boundedTestLimits())
					_, err := reader.GetPolicySnapshot(ctx, f.latin)
					if !errors.Is(err, store.ErrBoundedReadUnavailable) || !strings.Contains(err.Error(), "client encoding class") {
						t.Errorf("SQL_ASCII client: %v", err)
					}
					if u := reader.Usage(); u.ReservedUnits != boundedResultUnits+boundedRowUnits+boundedFixedUnits*pgSettingsColumns {
						t.Errorf("SQL_ASCII refusal charged beyond the settings statement: %+v", u)
					}
					return nil
				})
			}

			view("bytea escape", func(sc store.Scope) error {
				pgBoundedExec(ctx, t, sc, "SET LOCAL bytea_output = 'escape'")
				reader := newBoundedTestReader(t, sc, boundedTestLimits())
				rec, err := reader.GetExtension(ctx, boundedTestEntity.Kind, f.itemFull)
				if mode.textResults() {
					if !errors.Is(err, store.ErrBoundedReadUnavailable) || !strings.Contains(err.Error(), "bytea_output=hex") {
						t.Errorf("text-result escape BYTEA: %v", err)
					}
					policyReader := newBoundedTestReader(t, sc, boundedTestLimits())
					if _, err := policyReader.GetPolicySnapshot(ctx, f.latin); err != nil {
						t.Errorf("BYTEA-free policy under escape output: %v", err)
					}
				} else if err != nil || !reflect.DeepEqual(rec["payload"], []byte{0, 1, 2}) {
					t.Errorf("described-mode escape BYTEA: rec=%v err=%v", rec, err)
				}
				return nil
			})

			view("confinement", func(sc store.Scope) error {
				confined, err := store.ConfineWorkspace(ctx, sc, f.otherWS)
				if err != nil {
					return err
				}
				reader := newBoundedTestReader(t, confined, boundedTestLimits())
				if _, err := reader.GetPolicySnapshot(ctx, f.latin); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
					t.Errorf("confined policy: %v", err)
				}
				if _, err := reader.GetExtension(ctx, boundedTestEntity.Kind, f.itemFull); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("confined foreign row: %v", err)
				}
				recs, _, err := reader.ListExtensions(ctx, boundedTestEntity.Kind, model.Query{Limit: 10})
				if err != nil || len(recs) != 1 || recs[0].String("label") != "other" {
					t.Errorf("confined list: rows=%d err=%v", len(recs), err)
				}
				return nil
			})
		})
	}
}

// boundedLockRace is one holder/reader race with finite contexts. The holder
// is an external transaction (another process or binary) that holds only the
// row lock. Release (exactly once), holder rollback, reader cancellation and
// bounded joins are installed before any failure can return, so a failed or
// blocked monitor never abandons owned work (PG-C1).
type boundedLockRace struct {
	monitor *sql.DB
	st      store.Store
	tenant  model.TenantID
	row     model.ID
	budget  time.Duration
}

type boundedLockRaceResult struct {
	readErr, txErr error
}

func (lr boundedLockRace) run(
	parent context.Context,
	label string,
	observe func(context.Context) (bool, error),
	read func(context.Context, store.BoundedReader) error,
) (readErr error, err error) {
	raceCtx, cancelRace := context.WithTimeout(parent, lr.budget)
	defer cancelRace()
	join := func(what string, wait func(<-chan time.Time) bool) error {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		if !wait(timer.C) {
			return fmt.Errorf("%s did not finish within its bounded join", what)
		}
		return nil
	}

	holderCtx, cancelHolder := context.WithCancel(raceCtx)
	defer cancelHolder()
	holderTx, err := lr.monitor.BeginTx(holderCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("holder begin: %w", err)
	}
	res, err := holderTx.ExecContext(holderCtx, "UPDATE public.brt_item SET label = $1 WHERE id = $2", label, lr.row.String())
	if err == nil {
		if n, rowsErr := res.RowsAffected(); rowsErr != nil || n != 1 {
			err = fmt.Errorf("holder updated %d rows: %v", n, rowsErr)
		}
	}
	if err != nil {
		_ = holderTx.Rollback()
		return nil, fmt.Errorf("holder update: %w", err)
	}
	release := make(chan bool, 1) // true commits, false rolls back
	var releaseOnce sync.Once
	releaseHolder := func(commit bool) { releaseOnce.Do(func() { release <- commit }) }
	holderDone := make(chan error, 1)
	go func() {
		var commit bool
		select {
		case commit = <-release:
		case <-holderCtx.Done():
		}
		if commit {
			holderDone <- holderTx.Commit()
			return
		}
		holderDone <- holderTx.Rollback()
	}()

	readerCtx, cancelReader := context.WithCancel(raceCtx)
	readerDone := make(chan boundedLockRaceResult, 1)
	var holderJoined, readerJoined bool
	var holderErr error
	var reader boundedLockRaceResult
	// Cleanup precedes every later return: roll back unless already committed,
	// cancel the reader, and join both with bounded waits.
	defer func() {
		releaseHolder(false)
		cancelReader()
		if !holderJoined {
			if joinErr := join("holder", func(timeout <-chan time.Time) bool {
				select {
				case holderErr = <-holderDone:
					holderJoined = true
				case <-timeout:
				}
				return holderJoined
			}); joinErr != nil && err == nil {
				err = joinErr
			}
		}
		if !readerJoined {
			if joinErr := join("reader", func(timeout <-chan time.Time) bool {
				select {
				case reader = <-readerDone:
					readerJoined = true
				case <-timeout:
				}
				return readerJoined
			}); joinErr != nil && err == nil {
				err = joinErr
			}
		}
	}()
	go func() {
		var result boundedLockRaceResult
		result.txErr = lr.st.Mutate(readerCtx, lr.tenant, func(sc store.Scope) error {
			factory, ok := sc.(store.BoundedReaderFactory)
			if !ok {
				result.readErr = errors.New("scope lacks BoundedReaderFactory")
				return nil
			}
			limits := boundedTestLimits()
			limits.MaxCellBytes = 1000
			r, factoryErr := factory.NewBoundedReader(store.BoundedReadOptions{Limits: limits})
			if factoryErr != nil {
				result.readErr = factoryErr
				return nil
			}
			result.readErr = read(readerCtx, r)
			return nil
		})
		readerDone <- result
	}()

	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		queryCtx, cancelQuery := context.WithTimeout(raceCtx, 5*time.Second)
		waiting, observeErr := observe(queryCtx)
		cancelQuery()
		if observeErr != nil {
			return nil, fmt.Errorf("observe lock wait: %w", observeErr)
		}
		if waiting {
			break
		}
		select {
		case reader = <-readerDone:
			readerJoined = true
			// The no-lock oracle: a reader that never waits reads the old
			// committed version and finishes while the holder still commits nothing.
			return nil, fmt.Errorf("bounded reader finished without waiting on the row lock (read=%v tx=%v)",
				reader.readErr, reader.txErr)
		case <-deadline.C:
			return nil, errors.New("bounded reader never waited on the row lock within 20s")
		case <-raceCtx.Done():
			return nil, fmt.Errorf("race budget expired before the lock wait: %w", raceCtx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	releaseHolder(true)
	if joinErr := join("holder commit", func(timeout <-chan time.Time) bool {
		select {
		case holderErr = <-holderDone:
			holderJoined = true
		case <-timeout:
		}
		return holderJoined
	}); joinErr != nil {
		return nil, joinErr
	}
	if holderErr != nil {
		return nil, fmt.Errorf("holder commit: %w", holderErr)
	}
	if joinErr := join("reader", func(timeout <-chan time.Time) bool {
		select {
		case reader = <-readerDone:
			readerJoined = true
		case <-timeout:
		}
		return readerJoined
	}); joinErr != nil {
		return nil, joinErr
	}
	if reader.txErr != nil {
		return nil, fmt.Errorf("reader transaction: %w", reader.txErr)
	}
	return reader.readErr, nil
}

// TestBoundedReaderPostgresMutateLocksBeforeMeasuring holds a concurrent row
// lock, observes the bounded reader waiting on that exact lock, then commits
// a change. Growth beyond the limit and a filter change must both be seen
// after the wait; reading the old committed version would pass silently.
func TestBoundedReaderPostgresMutateLocksBeforeMeasuring(t *testing.T) {
	pg := isolatedPGSplit(t)
	seedStore := openBoundedPG(t, pg, pgExecModeCacheStatement)
	f := seedBoundedPG(t, seedStore)
	_ = seedStore.Close()
	monitor, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	defer func() { _ = monitor.Close() }()
	observeWait := func(ctx context.Context) (bool, error) {
		var waiting int
		err := monitor.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_stat_activity "+
			"WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock' "+
			"AND wait_event IN ('transactionid', 'tuple') AND query LIKE '%FOR UPDATE'").Scan(&waiting)
		return waiting > 0, err
	}

	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			st := openBoundedPG(t, pg, mode)
			defer func() { _ = st.Close() }()
			kind := boundedTestEntity.Kind
			lr := boundedLockRace{monitor: monitor, st: st, tenant: f.tenant, row: f.row, budget: 60 * time.Second}
			setLabel := func(label string) {
				t.Helper()
				setCtx, setCancel := context.WithTimeout(ctx, 30*time.Second)
				defer setCancel()
				if err := st.Mutate(setCtx, f.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(kind)
					if err != nil {
						return err
					}
					rec, err := repo.Get(setCtx, f.row)
					if err != nil {
						return err
					}
					rec["label"] = label
					_, err = repo.Update(setCtx, rec)
					return err
				}); err != nil {
					t.Fatalf("set label: %v", err)
				}
			}

			setLabel("match")
			readErr, err := lr.run(ctx, strings.Repeat("g", 2000), observeWait, func(rctx context.Context, r store.BoundedReader) error {
				_, err := r.GetExtension(rctx, kind, f.row)
				return err
			})
			if err != nil {
				t.Fatalf("growth race: %v", err)
			}
			if !errors.Is(readErr, store.ErrBoundedReadLimit) || !strings.Contains(readErr.Error(), "MaxCellBytes") {
				t.Errorf("growth while waiting: %v", readErr)
			}

			setLabel("match")
			readErr, err = lr.run(ctx, "moved", observeWait, func(rctx context.Context, r store.BoundedReader) error {
				_, _, err := r.ListExtensions(rctx, kind, model.Query{Limit: 10, Filters: []model.Filter{{
					Column: "label", Op: model.OpEq, Value: "match",
				}}})
				return err
			})
			if err != nil {
				t.Fatalf("filter race: %v", err)
			}
			if !errors.Is(readErr, store.ErrBoundedReadConsistency) {
				t.Errorf("filter change while waiting: %v", readErr)
			}

			if mode == pgExecModeCacheStatement {
				// Fault path: the monitor fails immediately. The race must return
				// that failure only after rolling the holder back and joining both
				// goroutines, leaving no lock and no unchanged-label writer behind.
				setLabel("match")
				started := time.Now()
				_, err = lr.run(ctx, "never-committed", func(context.Context) (bool, error) {
					return false, errors.New("injected monitor failure")
				}, func(rctx context.Context, r store.BoundedReader) error {
					_, err := r.GetExtension(rctx, kind, f.row)
					return err
				})
				if err == nil || !strings.Contains(err.Error(), "injected monitor failure") ||
					strings.Contains(err.Error(), "did not finish") {
					t.Errorf("fault race: %v", err)
				}
				if elapsed := time.Since(started); elapsed > 30*time.Second {
					t.Errorf("fault cleanup took %s", elapsed)
				}
				checkCtx, checkCancel := context.WithTimeout(ctx, 10*time.Second)
				defer checkCancel()
				if waiting, err := observeWait(checkCtx); err != nil || waiting {
					t.Errorf("lock wait remains after fault cleanup: waiting=%v err=%v", waiting, err)
				}
				repoErr := st.View(checkCtx, f.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(kind)
					if err != nil {
						return err
					}
					rec, err := repo.Get(checkCtx, f.row)
					if err != nil {
						return err
					}
					if rec.String("label") != "match" {
						t.Errorf("fault holder was not rolled back: label %q", rec.String("label"))
					}
					return nil
				})
				if repoErr != nil {
					t.Errorf("fault check view: %v", repoErr)
				}
			}

			setLabel("match")
			if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
				reader := newBoundedTestReader(t, sc, boundedTestLimits())
				recs, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 10, Filters: []model.Filter{{
					Column: "label", Op: model.OpEq, Value: "match",
				}}})
				if err != nil || len(recs) != 1 {
					t.Errorf("uncontended Mutate list: rows=%d err=%v", len(recs), err)
				}
				_, err = reader.GetExtension(ctx, kind, model.NewID())
				if !errors.Is(err, store.ErrNotFound) || reader.Usage().Terminal {
					t.Errorf("Mutate absent Get: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatalf("uncontended mutate: %v", err)
			}
		})
	}
}

func TestBoundedReaderPostgresEligibilityAndModeFacts(t *testing.T) {
	for query, want := range map[string]pgExecModeFact{
		"": pgExecModeCacheStatement,
		"?default_query_exec_mode=cache_statement": pgExecModeCacheStatement,
		"?default_query_exec_mode=cache_describe":  pgExecModeCacheDescribe,
		"?default_query_exec_mode=describe_exec":   pgExecModeDescribeExec,
		"?default_query_exec_mode=exec":            pgExecModeExec,
		"?default_query_exec_mode=simple_protocol": pgExecModeSimpleProtocol,
	} {
		got := observePGExecMode(store.Config{Engine: store.EnginePostgres, DSN: "postgres://br89@127.0.0.1:1/br89" + query})
		if got != want {
			t.Errorf("mode %q: got %s want %s", query, got, want)
		}
	}
	if got := observePGExecMode(store.Config{Engine: store.EnginePostgres, DSN: "postgres://br89@127.0.0.1:1/x?default_query_exec_mode=bogus"}); got != pgExecModeUnobserved {
		t.Errorf("invalid mode observed as %s", got)
	}
	if got := observePGExecMode(store.Config{Engine: store.EngineSQLite, DSN: "file::memory:"}); got != pgExecModeUnobserved {
		t.Errorf("SQLite observed PG mode %s", got)
	}

	ok := pgSettingClasses{server: pgClassUTF8, client: pgClassUTF8, byteaOutput: pgClassHex, standardStrings: pgClassOn, version: 1}
	for _, c := range []struct {
		name     string
		mode     pgExecModeFact
		mutate   func(*pgSettingClasses)
		hasBytes bool
		refuse   string
	}{
		{"qualified", pgExecModeSimpleProtocol, func(*pgSettingClasses) {}, true, ""},
		{"LATIN1 server UTF8 client", pgExecModeSimpleProtocol, func(s *pgSettingClasses) { s.server = pgClassLATIN1 }, false, ""},
		{"LATIN1 client extended", pgExecModeExec, func(s *pgSettingClasses) { s.client = pgClassLATIN1 }, false, ""},
		{"unobserved mode", pgExecModeUnobserved, func(*pgSettingClasses) {}, false, "execution mode"},
		{"SQL_ASCII server", pgExecModeCacheStatement, func(s *pgSettingClasses) { s.server = pgClassUnknown }, false, "server encoding"},
		{"unknown client", pgExecModeCacheStatement, func(s *pgSettingClasses) { s.client = pgClassUnknown }, false, "client encoding"},
		{"other major", pgExecModeCacheStatement, func(s *pgSettingClasses) { s.version = 0 }, false, "server version"},
		{"simple LATIN1 client", pgExecModeSimpleProtocol, func(s *pgSettingClasses) { s.client = pgClassLATIN1 }, false, "simple protocol"},
		{"simple strings off", pgExecModeSimpleProtocol, func(s *pgSettingClasses) { s.standardStrings = 2 }, false, "simple protocol"},
		{"exec escape bytea", pgExecModeExec, func(s *pgSettingClasses) { s.byteaOutput = 2 }, true, "bytea_output"},
		{"exec escape no bytea", pgExecModeExec, func(s *pgSettingClasses) { s.byteaOutput = 2 }, false, ""},
		{"described escape bytea", pgExecModeDescribeExec, func(s *pgSettingClasses) { s.byteaOutput = 2 }, true, ""},
	} {
		s := ok
		c.mutate(&s)
		err := pgBoundedEligibility(c.mode, s, c.hasBytes)
		if c.refuse == "" && err != nil || c.refuse != "" && (!errors.Is(err, store.ErrBoundedReadUnavailable) || !strings.Contains(err.Error(), c.refuse)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	for _, code := range []string{"22P05", "22021"} {
		cause := &pgconn.PgError{Code: code}
		err := boundedBackendError(cause)
		var pgErr *pgconn.PgError
		if !errors.Is(err, store.ErrBoundedReadUnavailable) || !errors.As(err, &pgErr) || pgErr.Code != code {
			t.Errorf("conversion %s: %v", code, err)
		}
	}
	if err := boundedBackendError(&pgconn.PgError{Code: "40001"}); errors.Is(err, store.ErrBoundedReadUnavailable) {
		t.Errorf("serialization failure classified as unavailable: %v", err)
	}
}
