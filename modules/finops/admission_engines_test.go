// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// THE FIXTURES EVERY ADMISSION TEST STANDS ON. Each one acts on ONE caller, named by
// a context value, so the other callers of a test run at full speed; and each counts
// that caller's own transactions, which is how a test names the point in a decision
// it attacks. On a new or pending key a Reserve writes three times — Mutate 1 claims
// or takes the key over, Mutate 2 creates the holds, Mutate 3 publishes them — and it
// reads twice before the first of them: View 1 is the row lookup, View 2 the budget
// census.
// -----------------------------------------------------------------------------

// pausedCallKey marks the caller the fixtures act on.
type pausedCallKey struct{}

// pausedCtx marks the one caller the fixtures below act on.
func pausedCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, pausedCallKey{}, "old")
}

// marked reports whether ctx belongs to the caller the fixtures act on.
func marked(ctx context.Context) bool { return ctx.Value(pausedCallKey{}) == "old" }

// forEachAdmissionEngine runs one ownership test against every storage this module
// supports. PostgreSQL is not a formality here and SQLite is not a substitute for it: on
// PostgreSQL two writers of one key are separate transactions under READ COMMITTED, and
// the version predicate is the only thing that can decide between them, while SQLite
// admits a single writer and serializes them on its own. A fence proven on one engine says
// nothing about the other.
//
// The PostgreSQL leg SKIPS by name when no database is authorized, rather than being
// omitted: a leg that quietly does not exist reports a pass for work nobody ran.
func forEachAdmissionEngine(t *testing.T, body func(*testing.T, store.Config)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		body(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			t.Skipf("%s unset: this PostgreSQL leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
		}
		body(t, store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 8})
	})
}

// pausedReadData pauses ONE caller after a real read transaction has closed. That is
// what a descheduled caller looks like to the database: its read committed and
// returned real values, and the world then moved on before it acted on them. It does
// not hold a lock open and it does not manufacture a stale value — a paused caller
// that never read the row would prove nothing about a fence that compares what the
// caller read.
//
// nth selects WHICH of the marked caller's completed reads pauses, which is how a test
// chooses the point in the decision the interleaving must attack.
type pausedReadData struct {
	api.ModuleData
	nth     int64
	reads   atomic.Int64
	reached chan struct{}
	resume  chan struct{}
}

func (d *pausedReadData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.ModuleData.View(ctx, tenant, fn)
	if err == nil && marked(ctx) && d.reads.Add(1) == d.nth {
		close(d.reached)
		select {
		case <-d.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// awaitPausedRead waits until the marked caller has completed the read it pauses after.
func awaitPausedRead(t *testing.T, d *pausedReadData) {
	t.Helper()
	select {
	case <-d.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the paused caller never completed the read the interleaving needs")
	}
}

// pausedMutateData pauses the marked caller BEFORE its nth Mutate. The write has not
// begun, so nothing it would write exists yet, while every transaction the caller
// already committed is visible to the others: a caller descheduled between two writes
// of one decision.
type pausedMutateData struct {
	api.ModuleData
	nth     int64
	writes  atomic.Int64
	reached chan struct{}
	resume  chan struct{}
}

func (d *pausedMutateData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if marked(ctx) && d.writes.Add(1) == d.nth {
		close(d.reached)
		select {
		case <-d.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

// awaitPausedMutate waits until the marked caller stands before the write it pauses at.
func awaitPausedMutate(t *testing.T, d *pausedMutateData) {
	t.Helper()
	select {
	case <-d.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the paused caller never reached the write the interleaving needs")
	}
}

// faultMode is where the nth Mutate of the marked caller fails.
type faultMode int

const (
	// faultBeforeCallback: the transaction never opens and the callback never runs.
	faultBeforeCallback faultMode = iota
	// faultAfterRollback: the callback runs to its end and returns nil, and the
	// transaction then rolls back, so nothing it wrote survives.
	faultAfterRollback
	// faultAfterCommit: the transaction commits and its acknowledgment is lost.
	faultAfterCommit
)

// errInjectedFault is the failure faultData reports. It wraps store.ErrStoreUnavailable,
// so a caller classifies it as it would a real lost connection.
var errInjectedFault = fmt.Errorf("finops-test: injected transaction fault: %w", store.ErrStoreUnavailable)

// faultData fails the marked caller's nth Mutate the way mode names. The other
// transactions reach the real store. fired says the injected fault decided that Mutate's
// outcome, because a test whose fault never fired proves nothing about the path it was
// written for: a callback that fails on its own, or a transaction the store itself
// fails, leaves it unset.
type faultData struct {
	api.ModuleData
	nth    int64
	mode   faultMode
	writes atomic.Int64
	fired  atomic.Bool
}

func (d *faultData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if !marked(ctx) || d.writes.Add(1) != d.nth {
		return d.ModuleData.Mutate(ctx, tenant, fn)
	}
	switch d.mode {
	case faultAfterRollback:
		err := d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
			if err := fn(sc); err != nil {
				return err
			}
			return errInjectedFault
		})
		if errors.Is(err, errInjectedFault) {
			d.fired.Store(true)
		}
		return err
	case faultAfterCommit:
		if err := d.ModuleData.Mutate(ctx, tenant, fn); err != nil {
			return err
		}
		d.fired.Store(true)
		return errInjectedFault
	default:
		d.fired.Store(true)
		return errInjectedFault
	}
}

// conflictOnInsertData makes the nth reservation insert made through it lose the seq
// race, exactly as a concurrent reserver that committed first would make it:
// store.ErrConflict from the INSERT and nothing else. Every other statement reaches the
// real store. Under the writer lock two creates of one tenant never interleave, so this
// is the only way to put a seq collision where a test needs it (the precedent is
// conflictingCreateScope in attempt_upgrade_test.go).
type conflictOnInsertData struct {
	api.ModuleData
	nth     int64
	inserts atomic.Int64
}

// conflictOnInsert wraps d so its nth reservation insert returns store.ErrConflict.
func conflictOnInsert(d api.ModuleData, n int64) *conflictOnInsertData {
	return &conflictOnInsertData{ModuleData: d, nth: n}
}

func (d *conflictOnInsertData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(insertConflictScope{Scope: sc, data: d})
	})
}

// insertConflictScope forwards the transaction lock and clock of the scope it wraps:
// hiding them would refuse the write for a reason that has nothing to do with the seq.
type insertConflictScope struct {
	store.Scope
	data *conflictOnInsertData
}

func (s insertConflictScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s insertConflictScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	clock, ok := s.Scope.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, errors.New("finops-test: wrapped scope provides no transaction clock")
	}
	return clock.TransactionNow(ctx)
}

func (s insertConflictScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetReservationKind {
		return repo, err
	}
	return insertConflictRepo{GenericRepo: repo, data: s.data}, nil
}

type insertConflictRepo struct {
	store.GenericRepo
	data *conflictOnInsertData
}

func (r insertConflictRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	if r.data.inserts.Add(1) == r.data.nth {
		return nil, fmt.Errorf("finops-test: lost the seq race: %w", store.ErrConflict)
	}
	return r.GenericRepo.Create(ctx, rec)
}

// ledgerRowsUnder returns every ledger row under h, read straight from the store, so no
// fixture wrapped around the module's data handle takes part in the count.
func ledgerRowsUnder(t testing.TB, st store.Store, tenant model.TenantID, h holdID) []model.Record {
	t.Helper()
	var out []model.Record
	for _, r := range countReservations(t, st, tenant) {
		if r.String(colResvHandle) == h.String() {
			out = append(out, r)
		}
	}
	return out
}

// activeRowsUnder returns the active ledger rows under h, withholding or lapsed.
func activeRowsUnder(t testing.TB, st store.Store, tenant model.TenantID, h holdID) []model.Record {
	t.Helper()
	var out []model.Record
	for _, r := range ledgerRowsUnder(t, st, tenant, h) {
		if r.String(colResvState) == resvStateActive {
			out = append(out, r)
		}
	}
	return out
}

// ledgerCount is the tenant's reservation ledger by state. Active counts every active
// row; ActiveLapsed the active rows whose expiry has passed.
type ledgerCount struct {
	Active, ActiveLapsed, Committed, Released, Expired int
}

// ledgerCounts reads the tenant's reservation ledger by state at now, straight from the
// store.
func ledgerCounts(t testing.TB, st store.Store, tenant model.TenantID, now time.Time) ledgerCount {
	t.Helper()
	var out ledgerCount
	for _, r := range countReservations(t, st, tenant) {
		switch r.String(colResvState) {
		case resvStateActive:
			out.Active++
			exp, err := model.ParseTimestamp(r.String(colResvExpiresAt))
			if err == nil && !exp.Time().After(now) {
				out.ActiveLapsed++
			}
		case resvStateCommitted:
			out.Committed++
		case resvStateReleased:
			out.Released++
		case resvStateExpired:
			out.Expired++
		}
	}
	return out
}

// seedAdmission writes one admission row straight through the store, as the writer that
// produced it left it, and returns it as stored.
func seedAdmission(t testing.TB, st store.Store, tenant model.TenantID, rec model.Record) model.Record {
	t.Helper()
	var out model.Record
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		out, err = repo.Create(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatalf("seed admission row %q: %v", rec.String(colAdmKey), err)
	}
	return out
}

// admissionRecord builds the eight columns of an admission row as an earlier build wrote
// them, byte for byte: an empty slot is "", and a zero stateAt is an undated row, whose
// column is NULL.
func admissionRecord(key, state string, handle, spend holdID, stateAt time.Time) model.Record {
	rec := model.Record{
		colAdmKey:         key,
		colAdmPayloadHash: "hash-" + key,
		colAdmHandle:      handle.String(),
		colAdmSpendHandle: spend.String(),
		colAdmScope:       "model_gateway",
		colAdmEstimate:    2 * oneUSD,
		colAdmState:       state,
		colAdmStateAt:     nil,
	}
	if !stateAt.IsZero() {
		rec[colAdmStateAt] = model.NewTimestamp(stateAt).String()
	}
	return rec
}

// ledgerRow builds one legacy ledger row of hold h: component "b" is a monthly global
// budget and "s" a daily per-seat spend limit of actor "a", the two components one
// request can hold.
func ledgerRow(policy model.ID, component string, h holdID, seq, amount int64, at, expires time.Time, state string) model.Record {
	kind, dimension, scopeKey, period := policyKindBudget, "global", "", "monthly"
	if component == "s" {
		kind, dimension, scopeKey, period = policyKindSpendLimit, "spend_limit", "a", "daily"
	}
	pStart, _ := periodStart(period, at)
	return model.Record{
		colResvPolicyRef:   policy.String(),
		colResvPolicyKind:  kind,
		colResvDimension:   dimension,
		colResvScopeKey:    scopeKey,
		colResvPeriod:      period,
		colResvPeriodStart: model.NewTimestamp(pStart).String(),
		colResvSeq:         seq,
		colResvAmount:      amount,
		colResvActual:      int64(0),
		colResvState:       state,
		colResvHandle:      h.String(),
		colResvExpiresAt:   model.NewTimestamp(expires).String(),
	}
}
