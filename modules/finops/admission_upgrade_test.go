// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// THE REAL UPGRADES into the admission table, from both populated states a database
// can be in: one from before the table existed, which has reservation rows only, and
// one an earlier build wrote, whose admission rows have eight columns and no owed list.
// Each store is created at the earlier descriptor, populated, closed, and the SAME
// storage is reopened with the current one — a fresh database, or a table created from
// the new descriptor, would prove nothing about an upgrade.
//
// What the upgrade itself must do is nothing to the rows. It adds a table or one nullable
// column. It retires no claim, settles no hold and back-fills no list: whether a legacy
// writer has stopped is a fact about the deployment that no schema step can observe.
// -----------------------------------------------------------------------------

// admissionSchemaFilter registers the module schema as an earlier state had it: without
// the admission table at all, or with its table minus the named columns.
type admissionSchemaFilter struct {
	store.ExtensionRegistry
	dropTable bool
	drop      map[string]bool
}

func (f admissionSchemaFilter) Register(d model.EntityDescriptor) error {
	if d.Kind == admissionIdempotencyKind {
		if f.dropTable {
			return nil
		}
		kept := make([]model.FieldSpec, 0, len(d.Fields))
		for _, field := range d.Fields {
			if !f.drop[field.Name] {
				kept = append(kept, field)
			}
		}
		d.Fields = kept
	}
	return f.ExtensionRegistry.Register(d)
}

// registerAdmissionWithout returns a registrar whose admission row omits the columns.
func registerAdmissionWithout(m *Module, columns ...string) func(store.ExtensionRegistry) error {
	drop := make(map[string]bool, len(columns))
	for _, c := range columns {
		drop[c] = true
	}
	return func(reg store.ExtensionRegistry) error {
		return m.RegisterSchema(admissionSchemaFilter{ExtensionRegistry: reg, drop: drop})
	}
}

// registerAsMain returns a registrar with the schema from before the admission table.
func registerAsMain(m *Module) func(store.ExtensionRegistry) error {
	return func(reg store.ExtensionRegistry) error {
		return m.RegisterSchema(admissionSchemaFilter{ExtensionRegistry: reg, dropTable: true})
	}
}

// forEachUpgradeEngine runs an upgrade against a SQLite FILE, because a :memory:
// database cannot be reopened, and against an isolated PostgreSQL database, whose
// dialect runs different DDL. The PostgreSQL leg SKIPS by name without a backend.
func forEachUpgradeEngine(t *testing.T, body func(*testing.T, store.Config)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		body(t, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "admission-upgrade.db"), Debug: true})
	})
	t.Run("postgres", func(t *testing.T) {
		if !enginetest.PostgresAvailable(t) {
			t.Skipf("%s unset: this PostgreSQL upgrade leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
		}
		body(t, store.Config{Engine: store.EnginePostgres, DSN: enginetest.IsolatedPostgres(t).App, MaxConns: 4})
	})
}

// rowsByID reads every row of kind and indexes it by id.
func rowsByID(t *testing.T, st store.Store, tenant model.TenantID, kind model.Kind) map[string]model.Record {
	t.Helper()
	out := make(map[string]model.Record)
	for _, r := range extRows(t, st, tenant, kind) {
		out[r.String(model.ColID)] = r
	}
	return out
}

// changedColumns names every column of before whose stored value is not the same in
// after, in a stable order.
func changedColumns(before, after model.Record) []string {
	var out []string
	for col, v := range before {
		if got, ok := after[col]; !ok || !reflect.DeepEqual(got, v) {
			out = append(out, col)
		}
	}
	sort.Strings(out)
	return out
}

// assertRowsUnchanged fails for every row of want that is missing from got or whose
// stored columns moved.
func assertRowsUnchanged(t *testing.T, what string, want, got map[string]model.Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d rows after the upgrade, %d before", what, len(got), len(want))
	}
	for id, before := range want {
		after, ok := got[id]
		if !ok {
			t.Errorf("%s: row %s is gone", what, id)
			continue
		}
		if moved := changedColumns(before, after); len(moved) > 0 {
			t.Errorf("%s: row %s changed %v across the upgrade", what, id, moved)
		}
	}
}

// TestUpgradeFromMain upgrades a populated database from before the admission table.
// Every reservation row, of every state and of both linkages (legacy and v1), is
// byte-identical afterwards, version included, and the admission table exists, carries
// the owed list, and is empty.
func TestUpgradeFromMain(t *testing.T) {
	forEachUpgradeEngine(t, runUpgradeFromMain)
}

func runUpgradeFromMain(t *testing.T, cfg store.Config) {
	ctx := context.Background()
	m := New()
	m.host = &fakeHost{}
	now := baseTime
	budget, limit := model.NewID(), model.NewID()

	// 1. Before the upgrade: reservation rows of every state, and no admission table.
	old := openAlertStore(t, cfg, registerAsMain(m))
	tenant := provisionTenant(t, old)
	if err := old.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Ext(admissionIdempotencyKind)
		return err
	}); err == nil {
		t.Fatal("fixture: the store before the upgrade already declares the admission table")
	}
	settled := func(rec model.Record, state string, actual int64, at time.Time) model.Record {
		rec[colResvState] = state
		rec[colResvActual] = actual
		rec[colResvSettledAt] = model.NewTimestamp(at).String()
		return rec
	}
	v1 := ledgerRow(budget, "b", newHoldID(), 6, oneUSD, now, now.Add(-time.Hour), resvStateActive)
	v1[colResvAttemptRef] = "0123456789abcdef0123456789abcdef"
	v1[colResvLifecycleVersion] = lifecycleLinkageVersion
	for _, rec := range []model.Record{
		ledgerRow(budget, "b", newHoldID(), 1, 2*oneUSD, now, now.Add(reservationTTL), resvStateActive),
		ledgerRow(budget, "b", newHoldID(), 2, 2*oneUSD, now, now.Add(-time.Minute), resvStateActive),
		settled(ledgerRow(budget, "b", newHoldID(), 3, 2*oneUSD, now, now.Add(-time.Hour), resvStateActive), resvStateCommitted, 1500000, now.Add(-2*time.Hour)),
		settled(ledgerRow(limit, "s", newHoldID(), 1, 2*oneUSD, now, now.Add(-time.Hour), resvStateActive), resvStateReleased, 0, now.Add(-2*time.Hour)),
		settled(ledgerRow(budget, "b", newHoldID(), 4, 2*oneUSD, now, now.Add(-time.Hour), resvStateActive), resvStateExpired, 0, now.Add(-time.Minute)),
		v1,
	} {
		seedReservation(t, old, tenant, rec)
	}
	before := rowsByID(t, old, tenant, budgetReservationKind)
	if len(before) != 6 {
		t.Fatalf("fixture: %d reservation rows seeded, want 6", len(before))
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the store from before the upgrade: %v", err)
	}

	// 2. The current schema, twice: the second reopen must find nothing left to do.
	for _, pass := range []string{"first reopen", "second reopen"} {
		upgraded := openAlertStore(t, cfg, m.RegisterSchema)
		after := rowsByID(t, upgraded, tenant, budgetReservationKind)
		assertRowsUnchanged(t, pass+": reservation ledger", before, after)
		for id, r := range after {
			if r.String(colResvAttemptRef) != "" {
				if link, _ := linkageOf(r); link != linkageV1 {
					t.Errorf("%s: the v1 child %s no longer reads as v1", pass, id)
				}
			}
		}
		// The upgrade writes no admission row; the one row after the first reopen is the
		// row this test writes below.
		wantAdmissions := 0
		if pass == "second reopen" {
			wantAdmissions = 1
		}
		if rows := extRows(t, upgraded, tenant, admissionIdempotencyKind); len(rows) != wantAdmissions {
			t.Errorf("%s: the admission table holds %d rows, want %d", pass, len(rows), wantAdmissions)
		}
		if pass == "first reopen" {
			// The table was created from the current descriptor: it takes an owed list.
			owed := owedHolds{newHoldID(), newHoldID()}
			cell, err := owed.encode()
			if err != nil {
				t.Fatal(err)
			}
			rec := admissionRecord("after-upgrade", admStatePending, newHoldID(), "", now)
			rec[colAdmOwedHandles] = cell
			seedAdmission(t, upgraded, tenant, rec)
			var (
				row   admissionRow
				found bool
			)
			if err := upgraded.View(ctx, tenant, func(sc store.Scope) error {
				var lerr error
				row, found, lerr = rowOfKey(ctx, sc, "after-upgrade")
				return lerr
			}); err != nil || !found {
				t.Fatalf("read the row written after the upgrade: found=%v err=%v", found, err)
			}
			if !reflect.DeepEqual(row.owed, owed) {
				t.Errorf("the created table did not keep the owed list: %v, want %v", row.owed, owed)
			}
		}
		if err := upgraded.Close(); err != nil {
			t.Fatalf("close after the %s: %v", pass, err)
		}
	}
}

// TestUpgradeFromR7KeepsColumns upgrades a populated database whose admission rows were
// written by an earlier build, in every state that build left rows in, and finds every
// row exactly as it was: the eight old columns and the base columns are byte-identical,
// owed_handles exists and is NULL, the ledger is untouched — and every legacy claim is
// still a claim, whatever its age: nothing here can know that the writer that staged it
// has stopped.
func TestUpgradeFromR7KeepsColumns(t *testing.T) {
	forEachUpgradeEngine(t, runUpgradeFromR7KeepsColumns)
}

func runUpgradeFromR7KeepsColumns(t *testing.T, cfg store.Config) {
	ctx := context.Background()
	m := New()
	m.host = &fakeHost{}
	tu := baseTime // the upgrade instant
	budget, limit := model.NewID(), model.NewID()
	ago := func(d time.Duration) time.Time { return tu.Add(-d) }

	// 1. An earlier build: the admission table with its eight columns.
	old := openAlertStore(t, cfg, registerAdmissionWithout(m, colAdmOwedHandles))
	tenant := provisionTenant(t, old)
	var seq int64
	ledger := func(policy model.ID, component string, h holdID, created time.Time, state string) {
		seq++
		rec := ledgerRow(policy, component, h, seq, 2*oneUSD, created, created.Add(reservationTTL), state)
		if state != resvStateActive {
			rec[colResvSettledAt] = model.NewTimestamp(created.Add(time.Second)).String()
		}
		if state == resvStateCommitted {
			rec[colResvActual] = int64(1500000)
		}
		seedReservation(t, old, tenant, rec)
	}
	type legacy struct {
		key, state    string
		handle, spend holdID
		stateAt       time.Time
	}
	hc1, hc2, hd1, he1, he2 := newHoldID(), newHoldID(), newHoldID(), newHoldID(), newHoldID()
	hg1, hg2, hh1, hj2, hj22, hk1 := newHoldID(), newHoldID(), newHoldID(), newHoldID(), newHoldID(), newHoldID()
	rows := []legacy{
		{"a", admStatePending, "", "", ago(10 * time.Second)},
		{"b", admStatePending, "", "", ago(600 * time.Second)},
		{"i", admStatePending, "", "", time.Time{}},
		{"c", admStateReserved, hc1, hc2, ago(60 * time.Second)},
		{"d", admStateReserved, hd1, "", ago(400 * time.Second)},
		{"e", admStateCommitted, he1, he2, ago(1000 * time.Second)},
		{"f", admStateReleased, "", "", ago(1000 * time.Second)},
		{"g", admStateOwesRelease, hg1, hg2, ago(20 * time.Second)},
		{"h", admStateOwesRelease, hh1, "", ago(500 * time.Second)},
		{"j", admStateReserved, "", hj2, ago(30 * time.Second)},
		{"j2", admStateReserved, "", hj22, ago(30 * time.Second)},
		{"k", admStateReserved, hk1, "", time.Time{}},
	}
	for _, r := range rows {
		seedAdmission(t, old, tenant, admissionRecord("model_gateway/upgraded-"+r.key, r.state, r.handle, r.spend, r.stateAt))
	}
	// The money each row names: withholding, lapsed or committed, as the row's state has it.
	ledger(budget, "b", hc1, ago(60*time.Second), resvStateActive)
	ledger(limit, "s", hc2, ago(60*time.Second), resvStateActive)
	ledger(budget, "b", hd1, ago(400*time.Second), resvStateActive)
	ledger(budget, "b", he1, ago(1000*time.Second), resvStateCommitted)
	ledger(limit, "s", he2, ago(1000*time.Second), resvStateCommitted)
	ledger(budget, "b", hg1, ago(20*time.Second), resvStateActive)
	ledger(limit, "s", hg2, ago(20*time.Second), resvStateActive)
	ledger(budget, "b", hh1, ago(500*time.Second), resvStateActive)
	ledger(limit, "s", hj2, ago(30*time.Second), resvStateActive)
	ledger(limit, "s", hj22, ago(30*time.Second), resvStateActive)
	ledger(budget, "b", hk1, ago(90*time.Second), resvStateActive)

	beforeRows := rowsByID(t, old, tenant, admissionIdempotencyKind)
	beforeLedger := rowsByID(t, old, tenant, budgetReservationKind)
	if len(beforeRows) != len(rows) || len(beforeLedger) != 11 {
		t.Fatalf("fixture: %d admission rows and %d ledger rows seeded", len(beforeRows), len(beforeLedger))
	}
	for id, r := range beforeRows {
		if _, has := r[colAdmOwedHandles]; has {
			t.Fatalf("fixture: the earlier-build row %s already has an owed list column", id)
		}
		if len(r) != 8+5 {
			t.Fatalf("fixture: the earlier-build row %s has %d columns, want eight and the five base columns", id, len(r))
		}
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the earlier-build store: %v", err)
	}

	// 2. The current schema, twice.
	for _, pass := range []string{"first reopen", "second reopen"} {
		upgraded := openAlertStore(t, cfg, m.RegisterSchema)
		afterRows := rowsByID(t, upgraded, tenant, admissionIdempotencyKind)
		assertRowsUnchanged(t, pass+": admission rows", beforeRows, afterRows)
		assertRowsUnchanged(t, pass+": reservation ledger", beforeLedger, rowsByID(t, upgraded, tenant, budgetReservationKind))
		for id, r := range afterRows {
			cell, has := r[colAdmOwedHandles]
			if !has || cell != nil {
				t.Errorf("%s: row %s owed_handles present=%v value=%v; want the added column, NULL", pass, id, has, cell)
			}
		}

		for _, want := range rows {
			key := "model_gateway/upgraded-" + want.key
			var (
				row   admissionRow
				found bool
			)
			if err := upgraded.View(ctx, tenant, func(sc store.Scope) error {
				var err error
				row, found, err = rowOfKey(ctx, sc, key)
				return err
			}); err != nil || !found {
				t.Fatalf("%s: read (%s): found=%v err=%v", pass, want.key, found, err)
			}
			if row.state != want.state || row.handle != want.handle || row.spendHandle != want.spend {
				t.Errorf("%s: (%s) decoded as %s %q %q, want %s %q %q", pass, want.key,
					row.state, row.handle, row.spendHandle, want.state, want.handle, want.spend)
			}
			if len(row.owed) != 0 || row.owedErr != nil {
				t.Errorf("%s: (%s) decoded an owed list %v (%v) from a NULL column", pass, want.key, row.owed, row.owedErr)
			}
			if want.stateAt.IsZero() != row.stateAt.IsZero() {
				t.Errorf("%s: (%s) dated=%v after the upgrade, want dated=%v", pass, want.key, !row.stateAt.IsZero(), !want.stateAt.IsZero())
			}
			if want.state == admStatePending && row.state != admStatePending {
				t.Errorf("%s: the legacy claim (%s) was retired by the upgrade: nothing establishes that its writer stopped", pass, want.key)
			}
		}
		if err := upgraded.Close(); err != nil {
			t.Fatalf("close after the %s: %v", pass, err)
		}
	}
}
