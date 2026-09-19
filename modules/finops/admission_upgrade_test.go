// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// THE REAL UPGRADE of the admission idempotency row. A store is created with the
// descriptor as it was BEFORE the state-entry column existed, admissions are written
// through it by the module's own Reserve and Commit, and the SAME storage is reopened
// with the descriptor that declares the column. The reconciler adds it as NULL, and
// what this asserts is the RECOVERY PATH on those rows: an undated row neither orphans
// the live hold it recorded nor carries an unbounded stale admission.
//
// The pre-upgrade rows are produced by REPLAYING THE SQL of that state — the store's
// own DDL and CRUD at the pre-column descriptor — rather than by checking the earlier
// package out into a scratch directory. The row an undated writer left behind is a row
// with no state_at, and at this descriptor the column does not exist to be written, so
// the rows are the rows that build produced. What is NOT claimed is that an arbitrary
// older binary is compatible in every other respect.
// -----------------------------------------------------------------------------

// admissionColumnFilter drops named columns from the admission idempotency descriptor
// on its way to the registry, so an EARLIER schema state can be created on purpose.
type admissionColumnFilter struct {
	store.ExtensionRegistry
	drop map[string]bool
}

func (f admissionColumnFilter) Register(d model.EntityDescriptor) error {
	if d.Kind == admissionIdempotencyKind {
		kept := make([]model.FieldSpec, 0, len(d.Fields))
		for _, field := range d.Fields {
			if f.drop[field.Name] {
				continue
			}
			kept = append(kept, field)
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
		return m.RegisterSchema(admissionColumnFilter{ExtensionRegistry: reg, drop: drop})
	}
}

func runAdmissionStateEntryUpgrade(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := context.Background()
	m := New()
	m.host = &fakeHost{}
	m.clock = &fakeClock{t: baseTime}

	// 1. The storage as it was BEFORE the column: two admissions written by the real
	//    Reserve/Commit path, one still holding money and one already settled.
	old := openAlertStore(t, cfg, registerAdmissionWithout(m, colAdmStateAt))
	tenant := provisionTenant(t, old)
	createBudget(t, old, tenant, "cap", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(old), st: old})

	live := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/pre-upgrade-live", EstimateMicroUSD: oneUSD}
	settled := AdmissionRequest{Scope: AdmissionScopeModelGateway, IdempotencyKey: "model_gateway/pre-upgrade-settled", EstimateMicroUSD: oneUSD}
	preLive, err := m.Reserve(ctx, tenant, live)
	if err != nil || !preLive.Allowed || preLive.Handle == "" {
		t.Fatalf("pre-upgrade live admission: %+v %v", preLive, err)
	}
	preSettled, err := m.Reserve(ctx, tenant, settled)
	if err != nil || !preSettled.Allowed || preSettled.Handle == "" {
		t.Fatalf("pre-upgrade settled admission: %+v %v", preSettled, err)
	}
	if err := m.Commit(ctx, tenant, preSettled.Handle, oneUSD); err != nil {
		t.Fatalf("pre-upgrade commit: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the pre-upgrade store: %v", err)
	}

	// 2. Reopen the SAME storage with the current descriptor. The reconciler adds the
	//    nullable column and back-fills nothing, so both rows are undated.
	upgraded := openAlertStore(t, cfg, m.RegisterSchema)
	defer func() { _ = upgraded.Close() }()
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(upgraded), st: upgraded})

	for _, req := range []AdmissionRequest{live, settled} {
		row, found, lerr := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
		if lerr != nil || !found {
			t.Fatalf("read %s after the upgrade: found=%v err=%v", req.IdempotencyKey, found, lerr)
		}
		if !row.stateAt.IsZero() {
			t.Fatalf("%s came out of the upgrade dated %v; the fixture is not testing an undated row",
				req.IdempotencyKey, row.stateAt)
		}
	}

	// 3. RECOVERY OF THE LIVE ONE. It is re-evaluated, because an undated row cannot be
	//    shown to be a young retry — and the hold it recorded stops being withheld, so
	//    the call that reserved before the upgrade does not end up holding twice.
	recovered, err := m.Reserve(ctx, tenant, live)
	if err != nil || !recovered.Allowed {
		t.Fatalf("the undated live admission was not recovered: %+v %v", recovered, err)
	}
	if recovered.Replayed || recovered.Handle == preLive.Handle {
		t.Fatalf("an undated row was replayed instead of re-evaluated: %+v", recovered)
	}
	report, err := m.InspectReservations(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if report.Active != 1 || report.IdempotencyOrphans != 0 || report.Drift {
		t.Fatalf("the upgrade left the ledger drifted: %+v (pre=%s recovered=%s)",
			report, preLive.Handle, recovered.Handle)
	}

	// 4. RECOVERY OF THE SETTLED ONE, which is the other half: an undated row must not
	//    carry its old verdict for ever. Blow the cap and the retry is refused by the
	//    ledger, not answered from a row nobody can date.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 50*oneUSD, baseTime))
	stale, err := m.Reserve(ctx, tenant, settled)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Allowed || stale.Replayed {
		t.Fatalf("an undated settled row granted an unbounded stale admission over a blown cap: %+v", stale)
	}
	if stale.Action != "block" {
		t.Fatalf("Action = %q, want the budget's block", stale.Action)
	}
}

// TestAdmissionStateEntryUpgradeSQLite runs the upgrade against a real SQLite file: a
// :memory: database cannot be reopened, and creating the table from the new descriptor
// would prove nothing about a migration.
func TestAdmissionStateEntryUpgradeSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admission-state-entry-upgrade.db")
	runAdmissionStateEntryUpgrade(t, store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true})
}

// TestAdmissionStateEntryUpgradePostgres runs the SAME upgrade against a real, isolated
// PostgreSQL database, whose dialect runs different DDL. It SKIPS without the backend:
// a leg that returned normally after logging would produce a PASS marker for a leg that
// never ran.
func TestAdmissionStateEntryUpgradePostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the PostgreSQL upgrade leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
	}
	dsns := enginetest.IsolatedPostgres(t)
	runAdmissionStateEntryUpgrade(t, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
}
