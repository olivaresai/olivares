// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // the fixture is inspected directly, outside the store

	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// public_upgrade_test.go upgrades a database created by the FIRST PUBLIC RELEASE's own
// product build, with this binary's exact product boot callback.
//
// WHAT MAKES IT THE PUBLIC FIXTURE RATHER THAN A LOOKALIKE. The artifact was produced by
// running coreengine.Open inside an extraction of the published tree — commit
// f443e0844dff6259679cc7b1e020058d467a0927, tree
// cea97471e1e3c16af533f508d8bf212de367c6ce, read from a local read-only object store —
// with THAT tree's `rt.RegisterSchema` -> `registerToolPinSchema` ->
// `registerCircuitBreakerSchema`. No hub tree stands in for it, and `migrate manifest`
// output does not either: that recorder omits two of the callback's three calls.
//
// WHAT IT DOES NOT CLAIM. It is a reconstruction from the published SOURCE, not a run of a
// published release archive, so it says nothing about binary reproducibility. Its
// provenance file states that in the same words.
// accessEvidenceFixtureRelations names the four relations by their table names rather
// than by importing the unexported constants: this package is the product boundary, and
// the fixture is about what a deployed database ends up holding.
var accessEvidenceFixtureRelations = []string{
	"policy_artifacts", "authority_transitions", "action_observations", "authorization_decisions",
}

func TestPublicV268SQLiteUpgradesToTheAccessEvidenceEdition(t *testing.T) {
	const (
		artifactSHA = "1b63b4b381129da6da552abe366ecb1923475cdd417c0a4601944a69f8adc404"
		rawSHA      = "51281586fab45afdd7a0b51ca3d65e9127b4da00a6a6b03fadd2c32e88b48b46"
	)
	compressed, err := os.ReadFile("testdata/public-v26.8.0/product.sqlite.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(compressed)); got != artifactSHA {
		t.Fatalf("public artifact SHA = %s, want %s", got, artifactSHA)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if closeErr := zr.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != rawSHA {
		t.Fatalf("public raw DB SHA = %s, want %s", got, rawSHA)
	}
	path := filepath.Join(t.TempDir(), "public-v26.8.0.sqlite")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// THE PRESTATE, read before anything opens it: core v7 is the ceiling, there is no
	// v8 and no v9, and none of the four access-evidence relations exists.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	before := sqliteCoreVersions(t, db)
	if len(before) != 7 || before[len(before)-1] != 7 {
		t.Fatalf("the published fixture records core versions %v, want a contiguous prefix ending at 7", before)
	}
	for _, table := range accessEvidenceFixtureRelations {
		if sqliteTableExists(t, db, table) {
			t.Fatalf("%s already exists in the published fixture; this case would measure nothing", table)
		}
	}
	rowsBefore := sqliteRowCensus(t, db)
	// The module axes are read as identities and versions rather than as row counts, so the
	// comparisons below also reject a substitution that preserves the count.
	moduleTablesBefore := sqliteModuleTableIdentities(t, db)
	sessionsMigrationsBefore := sqliteSessionsMigrationVersions(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// THE UPGRADE, through this binary's exact product callback.
	st, err := openProductStoreForFixture(t, path)
	if err != nil {
		t.Fatalf("upgrade the published v26.8.0 database: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	// THE TARGET IS THIS BINARY'S OWN, AND IT IS MEASURED RATHER THAN WRITTEN DOWN. This
	// package is a separate module and cannot see the store's private plan; a number copied
	// here would be a mirror that drifts silently the first time a core migration is appended
	// — which is exactly how "ending at 9" survived v10. So the authorities are the binary's
	// own: the compiled core plan the store derives from its plan constructor, and the history
	// the same product callback records on a fresh database, which readFreshProductInstall
	// requires to equal that plan. The plan is not an integer range: v12 is reserved for FinOps
	// and unregistered, so this build's history is 1..11 then 13 (ROOT-CONSTRUCTION-R5-1 §2).
	// The upgrade must land the published fixture on exactly that history.
	fresh := readFreshProductInstall(t)
	supported := fresh.coreVersions
	after := sqliteCoreVersions(t, db)
	if !slices.Equal(after, supported) {
		t.Fatalf("the upgraded database records core versions %v, want the compiled core plan a fresh install of this binary records, %v",
			after, supported)
	}
	for _, table := range accessEvidenceFixtureRelations {
		if !sqliteTableExists(t, db, table) {
			t.Fatalf("%s was not created by the upgrade", table)
		}
	}
	// EVERY PRE-EXISTING RELATION KEPT ITS ROWS, and the five that legitimately GREW are
	// named with what they grew by.
	//
	// The exemption list is short and each entry is a durable record this upgrade is
	// supposed to append to: the migration tracker gains every core migration between the
	// published fixture's ceiling and this build's supported version, the inventory gains
	// the four access-evidence activations, and the receipt ledger gains the one edition
	// seal. Everything else — 200-odd relations of real data — must be equal, and a
	// blanket "counts may grow" would have hidden exactly what this test is for.
	//
	// None of the five allowances is a written constant. The two module allowances are each
	// the difference between the fresh target and the published prestate, so neither is taken
	// from the database under test.
	rowsAfter := sqliteRowCensus(t, db)
	moduleTablesAfter := sqliteModuleTableIdentities(t, db)
	sessionsMigrationsAfter := sqliteSessionsMigrationVersions(t, db)
	moduleAppend, err := upgradedAxisAppend("applied_module_tables",
		moduleTablesBefore, moduleTablesAfter, fresh.moduleTables)
	if err != nil {
		t.Fatalf("the module entity registry after the upgrade: %v", err)
	}
	sessionsAppend, err := upgradedAxisAppend("schema_migrations_mod_sessions",
		sessionsMigrationsBefore, sessionsMigrationsAfter, fresh.sessionsMigrations)
	if err != nil {
		t.Fatalf("the sessions migration history after the upgrade: %v", err)
	}
	appended := map[string]int{
		"schema_migrations_core":          len(supported) - len(before),
		"olivares_guard_inventory_events": len(accessEvidenceFixtureRelations),
		"olivares_guard_receipts":         1,
		// The module relations this build adds since v26.8.0, counted from the prestate and
		// the fresh registry. Mutable module relations do not enter the guard manifest, so
		// their arrival must not change the base census; the manifest comparison in
		// TestPublicV268ProductBaselineHasACompiledAccessEvidenceEdge verifies that.
		"applied_module_tables": moduleAppend,
		// The authored sessions migrations this build adds since v26.8.0. Like the
		// mutable relations above they are counted rather than tolerated: a module
		// migration that changed an APPEND-ONLY relation would have moved the census,
		// and the manifest comparison is what proves none of these did.
		"schema_migrations_mod_sessions": sessionsAppend,
	}
	// The census is compared in a fixed order and every mismatch is reported together, so the
	// reported set does not depend on Go map iteration order.
	censusTables := make([]string, 0, len(rowsBefore))
	for table := range rowsBefore {
		censusTables = append(censusTables, table)
	}
	sort.Strings(censusTables)
	var censusFailures []string
	for _, table := range censusTables {
		count := rowsBefore[table]
		got, ok := rowsAfter[table]
		if !ok {
			censusFailures = append(censusFailures,
				fmt.Sprintf("the upgrade dropped the pre-existing relation %s", table))
			continue
		}
		if want := count + appended[table]; got != want {
			censusFailures = append(censusFailures,
				fmt.Sprintf("the upgrade changed %s from %d rows to %d, want %d", table, count, got, want))
		}
	}
	if len(censusFailures) != 0 {
		t.Fatalf("the upgrade did not preserve the row census:\n\t%s", strings.Join(censusFailures, "\n\t"))
	}
	for _, table := range accessEvidenceFixtureRelations {
		if got := rowsAfter[table]; got != 0 {
			t.Fatalf("%s holds %d rows after the upgrade; v9 backfills nothing", table, got)
		}
	}
	// The module relations this build declares and the published product did not. Each must be
	// absent from the historical census and present after the upgrade; a tracking row without
	// its relation would be an activation rather than an upgrade.
	newModuleTables := missingFrom(fresh.moduleTables, moduleTablesBefore)
	for _, table := range newModuleTables {
		if _, existed := rowsBefore[table]; existed {
			t.Fatalf("%s already existed in the published fixture", table)
		}
		if _, ok := rowsAfter[table]; !ok {
			t.Fatalf("%s was not created by the upgrade", table)
		}
	}
	// A second Open is a verification, not a replay.
	reopened, err := openProductStoreForFixture(t, path)
	if err != nil {
		t.Fatalf("reopen the upgraded database: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if got := sqliteRowCensus(t, db); len(got) != len(rowsAfter) {
		t.Fatalf("reopening changed the relation count %d -> %d", len(rowsAfter), len(got))
	}
	t.Logf("PUBLIC_V268_UPGRADE|versions_before=%v|versions_after=%v|relations=%d"+
		"|module_tables=%d->%d|module_tables_fresh=%d|module_append=%d"+
		"|sessions_migrations=%d->%d|sessions_migrations_fresh=%d|sessions_append=%d"+
		"|new_module_tables=%v",
		before, after, len(rowsAfter),
		len(moduleTablesBefore), len(moduleTablesAfter), len(fresh.moduleTables), moduleAppend,
		len(sessionsMigrationsBefore), len(sessionsMigrationsAfter), len(fresh.sessionsMigrations),
		sessionsAppend, newModuleTables)
}

// openProductStoreForFixture opens a store with the EXACT callback cmd/olivares hands
// coreengine.Open, which is the only registrar whose census matches the published one.
func openProductStoreForFixture(t *testing.T, dsn string) (store.Store, error) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, err := audit.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	set, err := buildModules(signer,
		ed25519.NewKeyFromSeed(fixedSeed(1)), ed25519.NewKeyFromSeed(fixedSeed(2)),
		nil, nil, sourcesConfig{}, EditionConfig{}, log)
	if err != nil {
		t.Fatal(err)
	}
	rt := runtime.New(runtime.Options{Logger: log})
	for _, m := range set.all {
		sm, ok := m.(sdk.Module)
		if !ok {
			t.Fatalf("module %q does not satisfy sdk.Module", m.APINamespace())
		}
		if aerr := rt.AddModule(sm, sdk.Config{}); aerr != nil {
			t.Fatalf("register module %q: %v", m.APINamespace(), aerr)
		}
	}
	return coreengine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, MaxConns: 1,
	}, func(reg store.ExtensionRegistry) error {
		if rerr := rt.RegisterSchema(reg); rerr != nil {
			return rerr
		}
		if terr := registerToolPinSchema(reg); terr != nil {
			return terr
		}
		return registerCircuitBreakerSchema(reg)
	})
}

// freshProductInstall holds the three histories one fresh installation by this binary's
// product callback records. It is the upgrade's expected target and is established without
// reading the database under test.
//
// Volatile columns are excluded: applied_at records when an installation ran and differs
// between two installations.
type freshProductInstall struct {
	coreVersions       []int
	moduleTables       []string
	sessionsMigrations []int
}

// readFreshProductInstall performs one fresh installation through openProductStoreForFixture
// and reads the three histories from it. The store's supported core version and its module
// registry are private to another module, so the installation is the authority available to
// this package.
//
// It asserts the properties the comparisons depend on: the core history equals the compiled
// core plan the store reports (coreengine.CoreMigrationPlanVersions), so comparing against it
// means "this build's complete core history" rather than "the same list twice" — a fresh
// install that skipped a planned migration, or recorded a version the plan does not contain,
// is refused here; and both module histories are non-empty, so no comparison against the fresh
// target can hold vacuously.
//
// It replaces freshProductCoreVersions, whose only caller was the core-version comparison
// below. That helper is not retained as a wrapper because it would have no caller and the
// repository's unused linter covers test files.
func readFreshProductInstall(t *testing.T) freshProductInstall {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fresh-product.sqlite")
	st, err := openProductStoreForFixture(t, path)
	if err != nil {
		t.Fatalf("install a fresh database with this binary's product callback: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	versions := sqliteCoreVersions(t, db)
	if len(versions) == 0 {
		t.Fatal("a fresh install by this binary recorded no core migration at all")
	}
	plan, err := coreengine.CoreMigrationPlanVersions(store.EngineSQLite)
	if err != nil {
		t.Fatalf("read this binary's compiled core migration plan: %v", err)
	}
	if err := freshCoreHistoryMatchesPlan(versions, plan); err != nil {
		t.Fatal(err)
	}
	fresh := freshProductInstall{
		coreVersions:       versions,
		moduleTables:       sqliteModuleTableIdentities(t, db),
		sessionsMigrations: sqliteSessionsMigrationVersions(t, db),
	}
	if len(fresh.moduleTables) == 0 {
		t.Fatal("a fresh install by this binary registered no module entity table at all")
	}
	if len(fresh.sessionsMigrations) == 0 {
		t.Fatal("a fresh install by this binary applied no sessions module migration at all")
	}
	return fresh
}

// sqliteModuleTableIdentities reads the module entity registry. applied_module_tables holds
// one row per module relation a database has applied, keyed by table_name, so the sorted
// identities describe the registry's content rather than only its size.
func sqliteModuleTableIdentities(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT table_name FROM applied_module_tables ORDER BY table_name")
	if err != nil {
		t.Fatalf("read the module entity registry: %v", err)
	}
	defer rows.Close() //nolint:errcheck // read-only census
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// sqliteSessionsMigrationVersions reads the sessions module migration ledger. One row is one
// applied migration and its version is the identity, so two ledgers of equal length can still
// record different migrations.
func sqliteSessionsMigrationVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT version FROM schema_migrations_mod_sessions ORDER BY version")
	if err != nil {
		t.Fatalf("read the sessions migration ledger: %v", err)
	}
	defer rows.Close() //nolint:errcheck // read-only census
	var out []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		out = append(out, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// upgradedAxisAppend derives the row allowance for one module axis of the census. It is the
// comparison the controls at the end of this file exercise.
//
// The allowance is the difference between the fresh target and the published prestate, so it
// does not depend on the database under test. Three conditions must hold first:
//
//   - Shape: each history must be sorted and free of duplicates.
//   - Retention: every identity the published product recorded must still be present.
//   - Target: the upgraded history must equal the fresh history exactly, so a missing
//     identity and a same-count substitution are both rejected.
//
// It returns an error rather than taking *testing.T so that its refusals are assertable.
func upgradedAxisAppend[T cmp.Ordered](axis string, before, after, fresh []T) (int, error) {
	for _, side := range []struct {
		name   string
		values []T
	}{{"published", before}, {"upgraded", after}, {"fresh", fresh}} {
		if err := checkSortedUnique(axis, side.name, side.values); err != nil {
			return 0, err
		}
	}
	if len(fresh) == 0 {
		return 0, fmt.Errorf("a fresh install of this binary records no %s history at all, so it is no target", axis)
	}
	if dropped := missingFrom(before, after); len(dropped) != 0 {
		return 0, fmt.Errorf("the upgrade dropped %v from the published %s history, which an upgrade retains",
			dropped, axis)
	}
	if !slices.Equal(after, fresh) {
		return 0, fmt.Errorf(
			"the upgraded %s history is not the one a fresh install of this binary records: missing %v, unexpected %v",
			axis, missingFrom(fresh, after), missingFrom(after, fresh))
	}
	return len(fresh) - len(before), nil
}

// missingFrom is the elements of want that are not in have, in want's order.
func missingFrom[T cmp.Ordered](want, have []T) []T {
	var out []T
	for _, v := range want {
		if !slices.Contains(have, v) {
			out = append(out, v)
		}
	}
	return out
}

// checkSortedUnique rejects a history that cannot be compared as a set. The readers order
// their queries, so an unordered or repeated value reports an unexpected database state rather
// than being normalized.
func checkSortedUnique[T cmp.Ordered](axis, side string, values []T) error {
	for i := 1; i < len(values); i++ {
		switch {
		case values[i] == values[i-1]:
			return fmt.Errorf("the %s %s history records %v twice", side, axis, values[i])
		case values[i] < values[i-1]:
			return fmt.Errorf("the %s %s history is not sorted: %v follows %v", side, axis, values[i], values[i-1])
		}
	}
	return nil
}

// freshCoreHistoryMatchesPlan is the core-history check readFreshProductInstall applies: the
// history a fresh install records must be exactly the compiled plan, in order. It returns an
// error rather than taking *testing.T so its refusals are assertable.
func freshCoreHistoryMatchesPlan(fresh, plan []int) error {
	const axis = "schema_migrations_core"
	if err := checkSortedUnique(axis, "compiled plan", plan); err != nil {
		return err
	}
	if len(plan) == 0 || plan[0] != 1 {
		return fmt.Errorf("the compiled core plan %v does not start at version 1", plan)
	}
	if err := checkSortedUnique(axis, "fresh", fresh); err != nil {
		return err
	}
	if !slices.Equal(fresh, plan) {
		return fmt.Errorf("a fresh install by this binary records core versions %v, not the compiled core plan %v: missing %v, unexpected %v",
			fresh, plan, missingFrom(plan, fresh), missingFrom(fresh, plan))
	}
	return nil
}

// TestFreshCoreHistoryMustEqualTheCompiledPlan covers the core axis. The plan below has the
// reserved gap the current store's plan has; the refusals include the integer-contiguous
// history, which is exactly what a foreign version-12 row would produce.
func TestFreshCoreHistoryMustEqualTheCompiledPlan(t *testing.T) {
	plan := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13}
	for _, tc := range []struct {
		name  string
		fresh []int
		want  string
	}{
		{name: "an unplanned reserved version is recorded", fresh: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}, want: "unexpected [12]"},
		{name: "a planned migration is skipped", fresh: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 13}, want: "missing [11]"},
		{name: "the ceiling migration never applied", fresh: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, want: "missing [13]"},
		{name: "a version is recorded twice", fresh: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 13}, want: "records 13 twice"},
		{name: "the history is not ordered", fresh: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 13, 11}, want: "is not sorted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := freshCoreHistoryMatchesPlan(tc.fresh, plan)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %v, want it to name %q", err, tc.want)
			}
		})
	}
	for _, bad := range [][]int{nil, {2, 3}, {1, 3, 2}} {
		if err := freshCoreHistoryMatchesPlan(bad, bad); err == nil {
			t.Fatalf("a malformed plan %v was accepted as an authority", bad)
		}
	}
	// Positive control: the legitimate gapped history is accepted.
	if err := freshCoreHistoryMatchesPlan(slices.Clone(plan), plan); err != nil {
		t.Fatalf("the history equal to the compiled plan was refused: %v", err)
	}
}

// TestUpgradedModuleRegistryAppendRejectsAMissingOrSubstitutedIdentity covers the module
// entity registry axis of the upgrade census. Each case is a registry an upgrade could
// produce; the substitution and duplicate cases preserve the row count, which a count-based
// expectation would accept.
func TestUpgradedModuleRegistryAppendRejectsAMissingOrSubstitutedIdentity(t *testing.T) {
	const axis = "applied_module_tables"
	published := []string{"sessions_work_item", "sessions_work_lease"}
	fresh := []string{"finops_attempt", "sessions_work_item", "sessions_work_lease"}
	for _, tc := range []struct {
		name  string
		after []string
		want  string
	}{
		{
			name:  "a historical identity is gone",
			after: []string{"finops_attempt", "sessions_work_item"},
			want:  "dropped [sessions_work_lease]",
		},
		{
			name:  "a new identity is substituted, and the count is unchanged",
			after: []string{"finops_lifecycle_scope", "sessions_work_item", "sessions_work_lease"},
			want:  "missing [finops_attempt], unexpected [finops_lifecycle_scope]",
		},
		{
			name:  "the new identity never arrived",
			after: []string{"sessions_work_item", "sessions_work_lease"},
			want:  "missing [finops_attempt], unexpected []",
		},
		{
			name:  "the upgraded registry names one identity twice",
			after: []string{"finops_attempt", "finops_attempt", "sessions_work_item", "sessions_work_lease"},
			want:  "records finops_attempt twice",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := upgradedAxisAppend(axis, published, tc.after, fresh)
			if err == nil {
				t.Fatalf("the comparison accepted %v and computed an allowance of %d", tc.after, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %q, want it to name %q", err, tc.want)
			}
		})
	}
	// Positive control: an upgraded registry equal to the fresh target is accepted and returns
	// the derived allowance. Without it the refusals above would also hold for a comparison
	// that rejects every input.
	got, err := upgradedAxisAppend(axis, published, fresh, fresh)
	if err != nil {
		t.Fatalf("the comparison refused the upgraded registry that equals the fresh target: %v", err)
	}
	if want := len(fresh) - len(published); got != want {
		t.Fatalf("allowance = %d, want %d", got, want)
	}
}

// TestUpgradedSessionsHistoryAppendRejectsAMissingOrSubstitutedVersion covers the sessions
// migration ledger, whose identities are versions rather than names.
func TestUpgradedSessionsHistoryAppendRejectsAMissingOrSubstitutedVersion(t *testing.T) {
	const axis = "schema_migrations_mod_sessions"
	published := []int{1, 2, 3}
	fresh := []int{1, 2, 3, 4, 5}
	for _, tc := range []struct {
		name  string
		after []int
		want  string
	}{
		{
			name:  "a historical version is gone",
			after: []int{1, 2, 4, 5},
			want:  "dropped [3]",
		},
		{
			name:  "a new version is substituted, and the count is unchanged",
			after: []int{1, 2, 3, 4, 6},
			want:  "missing [5], unexpected [6]",
		},
		{
			name:  "the upgraded ledger is not ordered",
			after: []int{1, 2, 3, 5, 4},
			want:  "is not sorted: 4 follows 5",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := upgradedAxisAppend(axis, published, tc.after, fresh)
			if err == nil {
				t.Fatalf("the comparison accepted %v and computed an allowance of %d", tc.after, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal = %q, want it to name %q", err, tc.want)
			}
		})
	}
	got, err := upgradedAxisAppend(axis, published, fresh, fresh)
	if err != nil {
		t.Fatalf("the comparison refused the upgraded ledger that equals the fresh target: %v", err)
	}
	if want := len(fresh) - len(published); got != want {
		t.Fatalf("allowance = %d, want %d", got, want)
	}
}

func sqliteCoreVersions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT version FROM schema_migrations_core ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // read-only census
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sqliteTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// sqliteRowCensus is table -> row count for every ordinary relation, which is what
// "preserved rows" can be measured as without knowing any table's semantics.
func sqliteRowCensus(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(tables)
	out := make(map[string]int, len(tables))
	for _, table := range tables {
		var n int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM main.\""+table+"\"").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// TestPublicV268FixtureProvenanceIsExact keeps the artifact and the story about it from
// drifting apart: the provenance file names the release this fixture came from, and the
// digests it records are the ones the upgrade test verifies before restoring anything.
func TestPublicV268FixtureProvenanceIsExact(t *testing.T) {
	raw, err := os.ReadFile("testdata/public-v26.8.0/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		SourceCommit string `json:"source_commit"`
		SourceTree   string `json:"source_tree"`
		ArtifactSHA  string `json:"artifact_sha256"`
		RawSHA       string `json:"raw_sha256"`
		CoreVersions []int  `json:"core_versions"`
		GuardEpoch   int64  `json:"guard_code_epoch"`
		GuardCodeSHA string `json:"guard_code_sha256"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.SourceCommit != "f443e0844dff6259679cc7b1e020058d467a0927" ||
		p.SourceTree != "cea97471e1e3c16af533f508d8bf212de367c6ce" {
		t.Fatalf("provenance names %s/%s, not the published release", p.SourceCommit, p.SourceTree)
	}
	compressed, err := os.ReadFile("testdata/public-v26.8.0/product.sqlite.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(compressed)); got != p.ArtifactSHA {
		t.Fatalf("artifact SHA = %s, provenance records %s", got, p.ArtifactSHA)
	}
	if p.GuardEpoch != 4 || p.GuardCodeSHA == "" {
		t.Fatalf("provenance records guard edition %d / %q, want the measured epoch-4 tuple",
			p.GuardEpoch, p.GuardCodeSHA)
	}
}
