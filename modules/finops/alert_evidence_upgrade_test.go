// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// A4.2 group 9: the REAL upgrade. A store is created with the alert descriptor as
// it was BEFORE this increment, a historical row is written through it, and the
// SAME storage is reopened with the descriptor that now declares the two nullable
// evidence columns. A fresh :memory: database, or creating the table directly from
// the new descriptor, would prove nothing about an upgrade.
//
// What this proves is storage and CRUD behaviour across that reconciliation: the
// historical row survives untouched and reads as legacy_unversioned, a new row can
// carry evidence, a partially expanded state gains only what it is missing, and a
// second reopen changes nothing. It is NOT a claim that an arbitrary older binary
// remains compatible in every respect.
// -----------------------------------------------------------------------------

// evidenceColumnFilter drops the named evidence columns from the alert descriptor
// on its way to the registry, so an EARLIER schema state can be created on purpose.
type evidenceColumnFilter struct {
	store.ExtensionRegistry
	drop map[string]bool
}

func (f evidenceColumnFilter) Register(d model.EntityDescriptor) error {
	if d.Kind == budgetAlertKind {
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

// registerWithout returns a schema registrar that omits the named columns.
func registerWithout(m *Module, columns ...string) func(store.ExtensionRegistry) error {
	drop := make(map[string]bool, len(columns))
	for _, c := range columns {
		drop[c] = true
	}
	return func(reg store.ExtensionRegistry) error {
		return m.RegisterSchema(evidenceColumnFilter{ExtensionRegistry: reg, drop: drop})
	}
}

// openAlertStore opens (or reopens) one storage with the given registrar and
// returns the store plus the tenant it provisioned or found.
func openAlertStore(t *testing.T, cfg store.Config, register func(store.ExtensionRegistry) error) store.Store {
	t.Helper()
	st, err := engine.Open(context.Background(), cfg, register)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

// seedHistoricalAlert writes one alert row the way a writer without this contract
// would: no evidence, no digest.
func seedHistoricalAlert(t *testing.T, st store.Store, tenant model.TenantID, budget model.ID) model.ID {
	t.Helper()
	var id model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetAlertKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), model.Record{
			colBudgetID:     budget.String(),
			colPeriod:       "monthly",
			colPeriodStart:  model.NewTimestamp(mustPeriodStart("monthly", baseTime)).String(),
			colThresholdPct: int64(50),
			colDimension:    "global",
			colDimKey:       "",
			colAlertSpend:   7 * oneUSD,
			colAlertLimit:   10 * oneUSD,
			colSeverity:     "medium",
			colTriggeredAt:  model.NewTimestamp(baseTime).String(),
		})
		if err != nil {
			return err
		}
		id = model.ID(rec.String(model.ColID))
		return nil
	}); err != nil {
		t.Fatalf("seed historical alert: %v", err)
	}
	return id
}

// readAlert returns one alert row by id.
func readAlert(t *testing.T, st store.Store, tenant model.TenantID, id model.ID) model.Record {
	t.Helper()
	var out model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetAlertKind)
		if err != nil {
			return err
		}
		out, err = repo.Get(context.Background(), id)
		return err
	}); err != nil {
		t.Fatalf("read alert %s: %v", id, err)
	}
	return out
}

// provisionTenant creates an org and returns its tenant.
func provisionTenant(t *testing.T, st store.Store) model.TenantID {
	t.Helper()
	slug := "upgrade-" + uniqueSlugSuffix(t)
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(context.Background()); e != nil {
			return e
		}
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	return tenant
}

// runAlertEvidenceUpgrade is the whole matrix against ONE storage configuration.
func runAlertEvidenceUpgrade(t *testing.T, cfg store.Config) {
	m := New()
	m.host = &fakeHost{}
	budget := model.NewID()

	// 1. The storage as it was BEFORE this increment.
	old := openAlertStore(t, cfg, registerWithout(m, colAlertEvidence, colAlertEvidenceHash))
	tenant := provisionTenant(t, old)
	historical := seedHistoricalAlert(t, old, tenant, budget)
	if err := old.Close(); err != nil {
		t.Fatalf("close old store: %v", err)
	}

	// 2. Reopen the SAME storage with the current descriptor: the reconciler adds the
	//    two nullable columns. Nothing is back-filled.
	upgraded := openAlertStore(t, cfg, m.RegisterSchema)
	row := readAlert(t, upgraded, tenant, historical)
	if row.Int(colAlertSpend) != 7*oneUSD || row.Int(colThresholdPct) != 50 {
		t.Fatalf("the historical row changed across the upgrade: %+v", row)
	}
	if ev := interpretAlertEvidence(row, tenant); ev.State != evidenceUnknownRead || ev.Cause != evidenceCauseLegacyUnversioned {
		t.Fatalf("the historical row reads as %+v, want unknown/legacy_unversioned", ev)
	}

	// 3. A new row written through the upgraded schema carries verifiable evidence.
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(upgraded), st: upgraded})
	m.clock = &fakeClock{t: baseTime}
	newID := writeEvidencedAlert(t, m, upgraded, tenant)
	stored := readAlert(t, upgraded, tenant, newID)
	newEvidence := interpretAlertEvidence(stored, tenant)
	if newEvidence.State != evidenceValid {
		t.Fatalf("the new row's evidence did not verify after storage: %+v", newEvidence)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatalf("close upgraded store: %v", err)
	}

	// 4. A SECOND reopen is idempotent: the same ids, amounts and digests come back
	//    and no DDL is repeated into a different shape.
	// Step 7 closes this one explicitly, so it is not also deferred: a double Close
	// would be a fixture detail nobody should have to reason about.
	reopened := openAlertStore(t, cfg, m.RegisterSchema)
	againHistorical := readAlert(t, reopened, tenant, historical)
	if againHistorical.Int(colAlertSpend) != 7*oneUSD {
		t.Fatalf("the historical amount moved on the second reopen: %+v", againHistorical)
	}
	if ev := interpretAlertEvidence(againHistorical, tenant); ev.State != evidenceUnknownRead {
		t.Fatalf("the historical row acquired evidence it never had: %+v", ev)
	}
	againNew := readAlert(t, reopened, tenant, newID)
	againEvidence := interpretAlertEvidence(againNew, tenant)
	if againEvidence.State != evidenceValid || againEvidence.Digest != newEvidence.Digest {
		t.Fatalf("the stored digest did not survive the second reopen: %+v", againEvidence)
	}
	if againNew.String(model.ColID) != newID.String() {
		t.Fatalf("the alert id moved: %s", againNew.String(model.ColID))
	}

	// 5. A SECOND, DIFFERENT ingestion in the same period. It must commit its own
	//    sample and ledger row while the alert is merely deduplicated. This is the
	//    A4.2-R5 cell inside the upgrade matrix, and on PostgreSQL it is the one that
	//    the delivered writer could not pass: its INSERT reached the unique index,
	//    the violation aborted the transaction, and the absorbed error reported a
	//    success the commit could not deliver.
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(reopened), st: reopened})
	before := len(alertRows(t, reopened, tenant))
	if err := m.onCost(context.Background(), tenant,
		mkCost("openai", "gpt-x", "s-2", 1, 1, oneUSD, baseTime.Add(time.Minute)), nil); err != nil {
		t.Fatalf("a second distinct ingestion was lost to the deduplicated alert: %v", err)
	}
	if after := len(alertRows(t, reopened, tenant)); after != before {
		t.Fatalf("alert rows %d -> %d: the deduplicated crossing wrote another row", before, after)
	}
	dedup := interpretAlertEvidence(readAlert(t, reopened, tenant, newID), tenant)
	if dedup.State != evidenceValid || dedup.Digest != newEvidence.Digest {
		t.Fatalf("the deduplicated crossing rewrote its evidence: %+v", dedup)
	}
	if samples := costSampleRows(t, reopened, tenant); len(samples) != 2 {
		t.Fatalf("cost samples = %d, want both ingestions", len(samples))
	}

	// 6. A SECOND TENANT with valid permissions on this same storage reads nothing of
	//    the first tenant's alert. The delivered matrix checked isolation only through
	//    an API caller with no membership, which is refused by authorization before
	//    the repository is reached — a different property. Here the scope is a
	//    legitimate one, bound to its own tenant, exercising the repository itself.
	neighbour := provisionTenant(t, reopened)
	if neighbour == tenant {
		t.Fatalf("the fixture provisioned the same tenant twice")
	}
	if err := reopened.View(context.Background(), neighbour, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetAlertKind)
		if err != nil {
			return err
		}
		if _, err := repo.Get(context.Background(), newID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another tenant reached the alert by id: err=%v", err)
		}
		rows, _, err := repo.List(context.Background(), model.Query{Limit: listCap})
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			t.Fatalf("another tenant listed %d of this tenant's alerts", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("neighbour view: %v", err)
	}

	// 7. REOPEN WITH THE OLD DESCRIPTOR and write through it. A binary that predates
	//    this contract still creates alerts without evidence, and it must not destroy
	//    the evidence columns or the rows that carry them. The scope of this claim is
	//    storage and CRUD across that descriptor — not that an arbitrary older binary
	//    is compatible in every respect.
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	regressed := openAlertStore(t, cfg, registerWithout(m, colAlertEvidence, colAlertEvidenceHash))
	legacyID := seedHistoricalAlert(t, regressed, tenant, model.NewID())
	if err := regressed.Close(); err != nil {
		t.Fatalf("close regressed store: %v", err)
	}

	final := openAlertStore(t, cfg, m.RegisterSchema)
	defer func() { _ = final.Close() }()
	survivor := interpretAlertEvidence(readAlert(t, final, tenant, newID), tenant)
	if survivor.State != evidenceValid || survivor.Digest != newEvidence.Digest {
		t.Fatalf("an old-descriptor writer destroyed evidence it cannot see: %+v", survivor)
	}
	legacyRow := readAlert(t, final, tenant, legacyID)
	if ev := interpretAlertEvidence(legacyRow, tenant); ev.State != evidenceUnknownRead || ev.Cause != evidenceCauseLegacyUnversioned {
		t.Fatalf("the row an old writer created reads as %+v, want unknown/legacy_unversioned", ev)
	}
	if legacyRow.Int(colAlertSpend) != 7*oneUSD {
		t.Fatalf("the old writer's row changed: %+v", legacyRow)
	}
}

// writeEvidencedAlert records one proven crossing through the real path and returns
// its id.
func writeEvidencedAlert(t *testing.T, m *Module, st store.Store, tenant model.TenantID) model.ID {
	t.Helper()
	id := createBudget(t, st, tenant, "upgraded", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		Action: "block", Thresholds: []float64{1},
	})
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))
	rows := alertRows(t, st, tenant)
	for _, r := range rows {
		if r.String(colBudgetID) == id.String() {
			return model.ID(r.String(model.ColID))
		}
	}
	t.Fatalf("the ingestion wrote no alert for the new budget")
	return ""
}

// TestAlertEvidenceUpgradeSQLite runs the matrix against a real SQLite FILE, which
// is what makes "reopen the same storage" meaningful.
func TestAlertEvidenceUpgradeSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alert-evidence-upgrade.db")
	runAlertEvidenceUpgrade(t, store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true})
}

// TestAlertEvidenceUpgradePostgres runs the SAME matrix against a real, isolated
// PostgreSQL database.
//
// A4.2 CORRECTION: without the backend this used to RETURN NORMALLY after logging,
// which produces a PASS marker for a leg that never ran — the exact shape the
// contract forbids ("never substituted by an omitted branch that looks like a
// pass"). It SKIPS now, so the absence is a skip result in the test output and in
// any count taken from it.
func TestAlertEvidenceUpgradePostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the PostgreSQL upgrade leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
	}
	dsns := enginetest.IsolatedPostgres(t)
	runAlertEvidenceUpgrade(t, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
}

// runPartialEvidenceExpansion: an interrupted expansion can leave one column present
// and the other absent. The next start adds only the missing one and fills no
// evidence into the rows that were written meanwhile.
//
// A4.2 CORRECTION: only ONE direction was covered (the digest column missing), and
// only on SQLite. The reconciler runs separate statements, so either column can be
// the one that did not arrive, and PostgreSQL runs a different dialect's DDL.
func runPartialEvidenceExpansion(t *testing.T, cfg store.Config, missing string) {
	m := New()
	m.host = &fakeHost{}

	partial := openAlertStore(t, cfg, registerWithout(m, missing))
	tenant := provisionTenant(t, partial)
	historical := seedHistoricalAlert(t, partial, tenant, model.NewID())
	row := readAlert(t, partial, tenant, historical)
	if _, present := row[missing]; present {
		t.Fatalf("the dropped column %q is present in the partial state: %+v", missing, row)
	}
	if err := partial.Close(); err != nil {
		t.Fatalf("close partial store: %v", err)
	}

	full := openAlertStore(t, cfg, m.RegisterSchema)
	defer func() { _ = full.Close() }()
	after := readAlert(t, full, tenant, historical)
	if after.Int(colAlertSpend) != 7*oneUSD {
		t.Fatalf("the historical row changed while completing the expansion: %+v", after)
	}
	if ev := interpretAlertEvidence(after, tenant); ev.State != evidenceUnknownRead || ev.Cause != evidenceCauseLegacyUnversioned {
		t.Fatalf("completing the expansion invented evidence: %+v", ev)
	}
	// And the completed schema accepts a row that does carry evidence, whose digest
	// verifies after the storage round trip.
	m.UseData(finopsTestData{ModuleData: api.NewModuleData(full), st: full})
	m.clock = &fakeClock{t: baseTime}
	id := writeEvidencedAlert(t, m, full, tenant)
	stored := readAlert(t, full, tenant, id)
	ev := interpretAlertEvidence(stored, tenant)
	if ev.State != evidenceValid {
		t.Fatalf("the completed schema stored unverifiable evidence: %+v", ev)
	}
	if ev.Digest == "" || ev.Digest != stored.String(colAlertEvidenceHash) {
		t.Fatalf("the digest column did not carry the verified digest: %q vs %q", ev.Digest, stored.String(colAlertEvidenceHash))
	}
}

// TestPartialEvidenceExpansionAddsOnlyWhatIsMissing runs both directions on SQLite.
func TestPartialEvidenceExpansionAddsOnlyWhatIsMissing(t *testing.T) {
	for _, missing := range []string{colAlertEvidenceHash, colAlertEvidence} {
		t.Run("missing "+missing, func(t *testing.T) {
			runPartialEvidenceExpansion(t, store.Config{
				Engine: store.EngineSQLite,
				DSN:    filepath.Join(t.TempDir(), "partial.db"), Debug: true,
			}, missing)
		})
	}
}

// TestPartialEvidenceExpansionPostgres runs both directions against a real, isolated
// PostgreSQL database. Without the backend the leg is SKIPPED: not run is not a pass.
func TestPartialEvidenceExpansionPostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the PostgreSQL partial-expansion leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
	}
	for _, missing := range []string{colAlertEvidenceHash, colAlertEvidence} {
		t.Run("missing "+missing, func(t *testing.T) {
			dsns := enginetest.IsolatedPostgres(t)
			runPartialEvidenceExpansion(t, store.Config{
				Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4,
			}, missing)
		})
	}
}
