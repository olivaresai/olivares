// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the server-gated half of the trigger-boundary proof: it runs
// wherever a PostgreSQL DSN is configured — locally (the dev container carries a
// live PostgreSQL; export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN and
// OLIVARES_TEST_POSTGRES_DSN, see CONTRIBUTING.md) and in CI. Without the DSN it
// skips OUTSIDE CI only; inside CI a missing fixture FAILS (see
// requirePostgresSuperuserDSN), because a skipped regression is not evidence. The
// pure policy matrix runs everywhere — see schema_invariant_policy_test.go.
// (An earlier revision of this header claimed the container had no PostgreSQL and
// labeled all of this CI-only/UNVERIFIED LOCALLY. That was false — the server was
// here and nobody ran pg_isready — and it is exactly how a red PostgreSQL leg hid
// behind a green check. Do not reintroduce that claim without running the leg.)
//
// What only a real server can settle — and no pure-Go test can — is not
// one claim but six:
//
//   - that PostgreSQL stores the tgenabled characters 'O'/'D'/'R'/'A' this code
//     maps, for the four ALTER TABLE forms that produce them;
//   - that those characters mean what the mapping claims — proved by FIRING the
//     guard, not by reading the catalog back;
//   - that two same-named triggers on different tables really are two catalog
//     rows, so the (schema, table, name) key does not collapse them;
//   - that the refusal is WIRED into Open, not merely available to a unit test;
//   - that the OTHER half of the boundary holds in an owner/app split: the app role
//     can still write the fact the guard protects;
//   - that canonical definition evidence changes when either the trigger row or
//     its executable function changes, and returns exactly after restoration.
//
// Each test provisions its own database and role from the superuser DSN, so it is
// re-runnable without cleanup and cannot collide with a sibling test.

// requirePostgresSuperuserDSN returns the superuser DSN, or ends the test.
//
// The distinction it draws is the whole point. OUTSIDE CI a missing DSN means "no
// PostgreSQL is CONFIGURED here" — a legitimate skip on a machine without one, but
// note the dev container DOES carry a live server (CONTRIBUTING.md has the DSNs).
// INSIDE CI it means the fixture the workflow promises has gone missing, and a test
// that skips there is not evidence of anything while still reporting green. That is
// how a PostgreSQL regression can sit in the suite for months and never once run.
//
// Every job that runs ordinary Go tests over this package declares a postgres:16
// service and this variable: mainline-ci's control-plane (:62-81) and race-hot
// (:239-254), and the weekly race-full sweep (race-full.yml:44-59, :112-114). No other
// job does — fuzz runs with -run '^$', provider-ci runs in a different module, and
// e2e-claude selects by -run — so a hard failure here cannot fire spuriously. If such a
// job is ever added, this failure is the correct outcome: it forces a decision instead
// of silently dropping coverage.
func requirePostgresSuperuserDSN(t *testing.T, what string) string {
	t.Helper()
	dsn := os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN")
	if dsn != "" {
		return dsn
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN is unset in CI, so %s did not run. "+
			"A skipped PostgreSQL regression is not coverage: restore the postgres service "+
			"and this variable for this job, or move the test out of it deliberately", what)
	}
	t.Skipf("set OLIVARES_TEST_POSTGRES_SUPERUSER_DSN to run %s (the dev container has a live PostgreSQL; see CONTRIBUTING.md)", what)
	return ""
}

// pgTestDB gives a test its own database through pgtest and returns a pool
// connected as the APP role, plus its DSN for a full Open.
//
// It used to mint a per-test `olv_tg_*` role. That was the defect that hid a P0:
// the schema's append-only REVOKE hard-codes dialect.DefaultAppRole, so a per-test
// role left that ACL targeting nobody while every assertion below still passed, and
// the migration whose LOCK TABLE needs the revoked privileges never failed here.
// pgtest keeps the APP role production-named for exactly this reason and makes only
// the DATABASE per-test; see core/internal/pgtest/pgtest.go.
func pgTestDB(t *testing.T, superDSN string) (*sql.DB, string) {
	t.Helper()
	_ = superDSN // pgtest reads the gate variables itself; kept so callers stay unchanged.
	pg := isolatedPG(t)
	db, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open as the app role: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("connect as the app role: %v", err)
	}
	// FIXTURE PRECONDITION. The whole point of pgtest is that this pool is the
	// PRODUCTION app role, so the hard-coded append-only REVOKE applies to it. Assert
	// the identity rather than trusting the DSN: connecting as anyone else silently
	// restores the very hole this fixture was rewritten to close.
	var connectedAs string
	if err := db.QueryRowContext(context.Background(), "SELECT current_user").Scan(&connectedAs); err != nil {
		t.Fatalf("read current_user: %v", err)
	}
	if connectedAs != dialect.DefaultAppRole {
		t.Fatalf("fixture precondition: connected as %q, want %q — the append-only REVOKE "+
			"hard-codes that name, so any other role leaves the ACL targeting nobody and "+
			"these assertions prove nothing about it", connectedAs, dialect.DefaultAppRole)
	}
	// FIXTURE PRECONDITION, not product coverage. current_schema() is the first
	// effective entry of search_path, whose default is "$user", public — so a schema
	// named after the connecting role would win, and both SchemaName and the
	// SchemaTriggers filter would then talk about a different schema than the one the
	// migrations wrote to. ProvisionPostgres creates no such schema, so this holds;
	// asserting it means a future change to provisioning fails HERE, with a clear
	// reason, instead of turning every trigger below into a mystery.
	var schema string
	if err := db.QueryRowContext(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatalf("read current_schema(): %v", err)
	}
	if schema != "public" {
		t.Fatalf("fixture precondition: current_schema() = %q, want \"public\" — the objects "+
			"this test creates would not be the ones the dialect reads", schema)
	}
	return db, pg.App
}

// mustExec runs one statement and fails the test with the statement in the message.
func mustExec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// liveTriggerInfo reads one trigger through the production dialect.
func liveTriggerInfo(t *testing.T, db *sql.DB, table, name string) dialect.TriggerInfo {
	t.Helper()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no postgres dialect")
	}
	live, err := dia.SchemaTriggers(context.Background(), db)
	if err != nil {
		t.Fatalf("SchemaTriggers: %v", err)
	}
	schema, err := dia.SchemaName(context.Background(), db)
	if err != nil {
		t.Fatalf("SchemaName: %v", err)
	}
	info, ok := live[dialect.TriggerKey{Schema: schema, Table: table, Name: name}]
	if !ok {
		t.Fatalf("trigger %s.%s.%s absent from the catalog read; got %d triggers",
			schema, table, name, len(live))
	}
	return info
}

// liveState reads one trigger's enable state through the production dialect.
func liveState(t *testing.T, db *sql.DB, table, name string) dialect.TriggerEnableState {
	t.Helper()
	return liveTriggerInfo(t, db, table, name).EnableState
}

// TestPostgresTriggerDefinitionBindsTriggerAndFunction proves that the catalog
// evidence hashed by SchemaTrigger covers both halves of PostgreSQL's object:
// the trigger row and the executable function. Keeping one half byte-identical
// while replacing the other must change the definition digest, and restoring
// the original must restore it exactly.
func TestPostgresTriggerDefinitionBindsTriggerAndFunction(t *testing.T) {
	superDSN := requirePostgresSuperuserDSN(t, "the trigger/function definition digest regression")
	db, _ := pgTestDB(t, superDSN)

	const (
		table            = "definition_probe"
		trigger          = "definition_probe_guard"
		function         = "definition_probe_block"
		originalFunction = `CREATE OR REPLACE FUNCTION definition_probe_block() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'definition probe blocked';
END;
$$ LANGUAGE plpgsql`
	)
	mustExec(t, db, originalFunction)
	mustExec(t, db, "CREATE TABLE "+table+" (id BIGINT PRIMARY KEY, note TEXT)")
	mustExec(t, db, "CREATE TRIGGER "+trigger+" BEFORE UPDATE ON "+table+
		" FOR EACH ROW EXECUTE FUNCTION "+function+"()")

	baselineInfo := liveTriggerInfo(t, db, table, trigger)
	if baselineInfo.Definition == "" {
		t.Fatal("PostgreSQL trigger catalog returned no definition evidence")
	}
	baseline := sha256.Sum256([]byte(baselineInfo.Definition))

	mustExec(t, db, `CREATE OR REPLACE FUNCTION definition_probe_block() RETURNS trigger AS $$
BEGIN
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
	functionMutant := sha256.Sum256([]byte(liveTriggerInfo(t, db, table, trigger).Definition))
	if functionMutant == baseline {
		t.Fatal("replacing only the executable function body did not change trigger evidence")
	}
	mustExec(t, db, originalFunction)
	if restored := sha256.Sum256([]byte(liveTriggerInfo(t, db, table, trigger).Definition)); restored != baseline {
		t.Fatal("restoring the original function did not restore canonical trigger evidence")
	}

	mustExec(t, db, "DROP TRIGGER "+trigger+" ON "+table)
	mustExec(t, db, "CREATE TRIGGER "+trigger+" AFTER UPDATE ON "+table+
		" FOR EACH ROW EXECUTE FUNCTION "+function+"()")
	triggerMutant := sha256.Sum256([]byte(liveTriggerInfo(t, db, table, trigger).Definition))
	if triggerMutant == baseline {
		t.Fatal("replacing only the trigger timing did not change trigger evidence")
	}
	mustExec(t, db, "DROP TRIGGER "+trigger+" ON "+table)
	mustExec(t, db, "CREATE TRIGGER "+trigger+" BEFORE UPDATE ON "+table+
		" FOR EACH ROW EXECUTE FUNCTION "+function+"()")
	if restored := sha256.Sum256([]byte(liveTriggerInfo(t, db, table, trigger).Definition)); restored != baseline {
		t.Fatal("restoring the original trigger did not restore canonical trigger evidence")
	}
}

// guardBlocks reports whether the immutability guard actually fired: it attempts the
// UPDATE the trigger exists to reject. This is the assertion that makes the whole
// enable-state mapping mean something — a catalog character is a claim, a rejected
// UPDATE is the behavior.
func guardBlocks(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		fmt.Sprintf("UPDATE %s SET note = 'touched' WHERE id = 1", table))
	if err == nil {
		return false
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("the UPDATE on %s failed for a reason that is not the guard, so this "+
			"proves nothing about whether the trigger fired: %v", table, err)
	}
	return true
}

// TestPostgresTriggerEnableStateMatrix is the O/D/R/A regression against a real
// server, and it asserts Behavior alongside the catalog value.
//
// The four ALTER TABLE forms are the realistic vectors, not exotic tampering: an
// operator silencing a guard to push a data fix, a restore tool that disables
// triggers around a bulk load, or a replication setup that flips a guard to
// REPLICA. In every case the trigger stays listed in pg_trigger, so a self-test
// that reads names sees a healthy boundary while nothing is enforced.
//
// Server-gated: runs wherever the PostgreSQL DSNs are configured — locally against
// the dev container's live server (verified by execution, 2026-07-26) and in CI.
func TestPostgresTriggerEnableStateMatrix(t *testing.T) {
	superDSN := requirePostgresSuperuserDSN(t, "the tgenabled O/D/R/A regression")
	db, _ := pgTestDB(t, superDSN)

	// Two tables carrying a trigger of the SAME name. PostgreSQL only requires a
	// trigger name to be unique per table, so this catalog is legal — and it is the
	// shape that a name-keyed map silently collapses into one entry.
	const guard = "enable_probe_guard"
	mustExec(t, db, `CREATE FUNCTION enable_probe_block() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'table is append-only';
END;
$$ LANGUAGE plpgsql`)
	for _, table := range []string{"enable_probe_a", "enable_probe_b"} {
		mustExec(t, db, fmt.Sprintf("CREATE TABLE %s (id BIGINT PRIMARY KEY, note TEXT)", table))
		mustExec(t, db, fmt.Sprintf("INSERT INTO %s (id, note) VALUES (1, 'seed')", table))
		mustExec(t, db, fmt.Sprintf(
			"CREATE TRIGGER %s BEFORE UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION enable_probe_block()",
			guard, table))
	}

	// Both homonyms must exist as SEPARATE catalog entries under the structured key.
	if got := liveState(t, db, "enable_probe_a", guard); got != dialect.TriggerFiresOrigin {
		t.Fatalf("a freshly created trigger on enable_probe_a is %q, want %q", got, dialect.TriggerFiresOrigin)
	}
	if got := liveState(t, db, "enable_probe_b", guard); got != dialect.TriggerFiresOrigin {
		t.Fatalf("a freshly created trigger on enable_probe_b is %q, want %q", got, dialect.TriggerFiresOrigin)
	}

	for _, tc := range []struct {
		alter      string
		wantState  dialect.TriggerEnableState
		wantFires  bool
		wantBlocks bool
	}{
		{"DISABLE TRIGGER", dialect.TriggerNeverFires, false, false},
		{"ENABLE REPLICA TRIGGER", dialect.TriggerFiresReplicaOnly, false, false},
		// ENABLE ALWAYS is characterized here as a FACT — the catalog says 'A' and the
		// guard demonstrably blocks — and nothing more. Whether a deployment that has
		// set ALWAYS on a boundary trigger should be ACCEPTED is a separate posture
		// question that is still open: an ALWAYS trigger also fires on a subscriber
		// applying replicated rows, which is why the rollout migration deliberately
		// leaves its own triggers at 'O'. That decision belongs to product, and this
		// test does not pre-empt it — it only records what PostgreSQL does.
		{"ENABLE ALWAYS TRIGGER", dialect.TriggerFiresAlways, true, true},
		{"ENABLE TRIGGER", dialect.TriggerFiresOrigin, true, true},
	} {
		t.Run(tc.alter, func(t *testing.T) {
			// Applied to enable_probe_a ONLY. enable_probe_b is the control: it shares
			// the trigger NAME, so if the catalog read collapsed the two, its state
			// would move too.
			mustExec(t, db, fmt.Sprintf("ALTER TABLE enable_probe_a %s %s", tc.alter, guard))

			if got := liveState(t, db, "enable_probe_a", guard); got != tc.wantState {
				t.Fatalf("after `ALTER TABLE enable_probe_a %s`, tgenabled = %q, want %q",
					tc.alter, got, tc.wantState)
			}
			if got := liveState(t, db, "enable_probe_b", guard); got != dialect.TriggerFiresOrigin {
				t.Fatalf("`ALTER TABLE enable_probe_a %s` moved the homonym on enable_probe_b to %q: the two "+
					"triggers are being read as ONE catalog entry", tc.alter, got)
			}
			if got := tc.wantState.Fires(); got != tc.wantFires {
				t.Fatalf("Fires() for %q = %v, want %v", tc.wantState, got, tc.wantFires)
			}

			// The behavioral half: does the guard actually run? This is what turns the
			// catalog character into a security statement. The session is 'origin'
			// (Open refuses anything else), so 'R' must NOT block even though it reads
			// as "enabled for replicas".
			if got := guardBlocks(t, db, "enable_probe_a"); got != tc.wantBlocks {
				t.Fatalf("after `ALTER TABLE enable_probe_a %s` the guard blocking = %v, want %v — "+
					"the catalog says %q, so the mapping and the behavior disagree",
					tc.alter, got, tc.wantBlocks, tc.wantState)
			}
			// The control table's guard is untouched and must still block.
			if !guardBlocks(t, db, "enable_probe_b") {
				t.Fatalf("`ALTER TABLE enable_probe_a %s` silenced the homonym on enable_probe_b", tc.alter)
			}
		})
	}
}

// --- end-to-end: the refusal must be wired into Open ---------------------------

// tgmNamespace is the throwaway module the boot test mounts. Its table is
// append-only so it also populates the security-boundary set the split path checks.
const (
	tgmNamespace = "tgm"
	tgmTable     = "tgm_fact"
	tgmTable2    = "tgm_note"
	// ONE name, declared on TWO tables — the shape the old name-keyed map could not
	// represent. It is also deliberately engine-neutral: the guard means "this
	// table's boundary", and each engine implements it with the strongest statement
	// it has, so naming it after one engine's statement would make the other
	// declaration read as a lie.
	tgmTrigger = "tgm_boundary"
)

// tgmMigrations builds the per-engine migration filesystem the module registers.
// PostgreSQL guards TRUNCATE (which its append-only trigger does not cover);
// SQLite has no TRUNCATE at all, so its half guards DELETE. The SQLite half is not
// optional: SchemaInvariants requires every supported engine to be declared
// (registry.go:141) — a module that needs an invariant needs it everywhere — and a
// declaration whose migration is missing refuses to boot, which is the point.
// One statement per file: loadFileMigrations turns each file into a single-statement
// migration (modulemig.go:71), and pgx's extended protocol refuses several commands
// in one Exec.
func tgmMigrations() fs.FS {
	mapfs := fstest.MapFS{}
	for i, table := range []string{tgmTable, tgmTable2} {
		mapfs[fmt.Sprintf("postgres/000%d_guard_%s.sql", i+1, table)] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf(
				"CREATE TRIGGER %s BEFORE TRUNCATE ON %s FOR EACH STATEMENT EXECUTE FUNCTION olivares_block_mutation()",
				tgmTrigger, table))}
		mapfs[fmt.Sprintf("sqlite/000%d_guard_%s.sql", i+1, table)] = &fstest.MapFile{
			Data: []byte(fmt.Sprintf(
				"CREATE TRIGGER %s BEFORE DELETE ON %s BEGIN SELECT RAISE(ABORT, 'table is append-only'); END",
				tgmTrigger, table))}
	}
	return mapfs
}

func registerTriggerModule(reg store.ExtensionRegistry) error {
	for _, table := range []string{tgmTable, tgmTable2} {
		if err := reg.Register(model.EntityDescriptor{
			Kind:       model.Kind(tgmNamespace + "." + strings.TrimPrefix(table, tgmNamespace+"_")),
			Table:      table,
			Fields:     []model.FieldSpec{{Name: "note", Kind: model.KindText}},
			AppendOnly: true,
		}); err != nil {
			return err
		}
	}
	if err := reg.Migrations(tgmNamespace, tgmMigrations()); err != nil {
		return err
	}
	both := []store.SchemaTrigger{
		{Name: tgmTrigger, Table: tgmTable},
		{Name: tgmTrigger, Table: tgmTable2},
	}
	return reg.SchemaInvariants(tgmNamespace, map[store.Engine][]store.SchemaTrigger{
		store.EnginePostgres: both,
		store.EngineSQLite:   both,
	})
}

// TestPostgresOpenAcceptsTwoRequiredHomonyms is THE discriminating regression for
// the typed trigger key, and the one the close review asked for.
//
// It is worth being precise about why the obvious test is not the right one. Dropping
// the required guard and leaving a namesake on some decoy table was ALSO refused by
// the previous, name-keyed design — it found the namesake and rejected it through a
// `info.Table != required.Table` branch (5ad5e3b3^:selftest.go:61-70). A test built on
// that shape discriminates only on the wording of an error message.
//
// The shape the old design genuinely could not express is this one: two invariants
// with the SAME NAME on DIFFERENT tables, both healthy. `live[required.Name]` holds
// one entry, so one of the two requirements necessarily saw the other's table and the
// store refused a schema in which nothing whatsoever is wrong. The failure is
// deterministic — it does not depend on which catalog row won the map — so requiring
// Open to SUCCEED here is a real red against that design and green against this one.
//
// The second phase then proves the two are judged independently rather than merged:
// silencing one must refuse and must name that one.
//
// Server-gated: runs wherever the PostgreSQL DSNs are configured — locally against
// the dev container's live server (verified by execution, 2026-07-26) and in CI.
func TestPostgresOpenAcceptsTwoRequiredHomonyms(t *testing.T) {
	superDSN := requirePostgresSuperuserDSN(t, "the homonym boot regression")
	ctx := context.Background()
	db, dsn := pgTestDB(t, superDSN)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}

	st, err := Open(ctx, cfg, registerTriggerModule)
	if err != nil {
		t.Fatalf("a module declaring the SAME trigger name on two of its own tables was "+
			"refused, though both guards exist and fire: %v", err)
	}
	_ = st.Close()

	// Both really are two separate catalog rows, not one entry read twice.
	for _, table := range []string{tgmTable, tgmTable2} {
		if got := liveState(t, db, table, tgmTrigger); got != dialect.TriggerFiresOrigin {
			t.Fatalf("%s.%s is %q, want %q", table, tgmTrigger, got, dialect.TriggerFiresOrigin)
		}
	}

	// The application role owns its tables in a single-role deployment, so it can
	// silence its own guard — no superuser needed. That is exactly why the boot
	// self-test exists, and why the owner/app split is the stronger posture.
	mustExec(t, db, fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER %s", tgmTable, tgmTrigger))
	if got := liveState(t, db, tgmTable, tgmTrigger); got != dialect.TriggerNeverFires {
		t.Fatalf("precondition: the guard is %q, want %q", got, dialect.TriggerNeverFires)
	}

	st, err = Open(ctx, cfg, registerTriggerModule)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open accepted a database whose declared guard is DISABLED: the trigger is " +
			"listed in the catalog and never runs, so the invariant is unenforced")
	}
	if !errors.Is(err, store.ErrSchemaTriggerInert) {
		t.Fatalf("Open failed for a reason that is not the inert-trigger verdict, so this "+
			"proves nothing about it: %v", err)
	}
	if !strings.Contains(err.Error(), tgmTable+"."+tgmTrigger) {
		t.Fatalf("the refusal does not name the guard that is inert: %v", err)
	}
	if strings.Contains(err.Error(), tgmTable2+"."+tgmTrigger) {
		t.Fatalf("the refusal also blames the healthy homonym on %s: the two are being "+
			"judged as one: %v", tgmTable2, err)
	}

	// Restore and reopen: without this the test would also pass if Open were broken
	// outright.
	mustExec(t, db, fmt.Sprintf("ALTER TABLE %s ENABLE TRIGGER %s", tgmTable, tgmTrigger))
	st, err = Open(ctx, cfg, registerTriggerModule)
	if err != nil {
		t.Fatalf("Open refused a healthy database after the guard was re-enabled: %v", err)
	}
	_ = st.Close()
}

// TestPostgresOpenRefusesAHalfInstalledBoundaryInASplit covers the OTHER half of the
// trigger boundary — the one that is not a trigger at all.
//
// In an owner/app split the guard belongs to the owner and the application is a
// non-owner that must be GRANTED access to the fact table the guard protects. A
// deployment where the trigger fires perfectly but the app cannot INSERT the fact is
// not a working boundary; it is a boundary that fails at first use, in production,
// with a runtime error rather than a refusal to start.
//
// This check (checkInvariantTablePrivileges) had NO test before: it is the impure
// half that the pure-policy extraction deliberately did not absorb, and an untested
// impure half is exactly how a refactor quietly loses coverage. Closing it here is
// not the P1-5 topology matrix — that remains open — it is this unit refusing to
// leave behind the thing it just refactored around.
//
// Verified against PostgreSQL 15.18 locally; also runs in CI.
func TestPostgresOpenRefusesAHalfInstalledBoundaryInASplit(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx := context.Background()
	appDSN, ownerDSN := pg.App, pg.Owner
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: appDSN, OwnerDSN: ownerDSN, MaxConns: 4,
	}

	st, err := Open(ctx, cfg, registerTriggerModule)
	if err != nil {
		t.Fatalf("baseline split open (owner runs DDL, app serves): %v", err)
	}
	_ = st.Close()

	// The REVOKE runs as the OWNER, because in a split the owner is what holds the
	// grant to give or take. Doing it as the superuser would prove nothing about the
	// role model this deployment actually uses.
	ownerDB, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open the owner pool: %v", err)
	}
	defer ownerDB.Close() //nolint:errcheck
	mustExec(t, ownerDB, fmt.Sprintf("REVOKE INSERT ON %s FROM %s", tgmTable, dialect.DefaultAppRole))

	st, err = Open(ctx, cfg, registerTriggerModule)
	if err == nil {
		_ = st.Close()
		t.Fatalf("Open accepted a split where the app role cannot INSERT into %q: the guard "+
			"fires, but the fact it guards can never be written", tgmTable)
	}
	if !errors.Is(err, store.ErrSchemaBoundaryGrantMissing) {
		t.Fatalf("Open failed for a reason that is not the boundary-grant check: %v", err)
	}
	if !strings.Contains(err.Error(), tgmTable) {
		t.Fatalf("the refusal does not name the table whose grant is missing: %v", err)
	}

	// Restore and reopen, so the test cannot pass by refusing everything.
	mustExec(t, ownerDB, fmt.Sprintf("GRANT INSERT ON %s TO %s", tgmTable, dialect.DefaultAppRole))
	st, err = Open(ctx, cfg, registerTriggerModule)
	if err != nil {
		t.Fatalf("Open refused a healthy split after the grant was restored: %v", err)
	}
	_ = st.Close()
}

// TestPostgresOpenRefusesAGuardMissingFromItsTable covers the plainer fault: the
// declared name exists in the schema, on some other table, and the table that must be
// guarded has nothing.
//
// Unlike the test above this is NOT a discriminator for the typed key — the previous
// design refused it too, via its wrong-table branch — so it is here as regression
// coverage for a real operator mistake (a migration recreating a guard against the
// wrong table), not as evidence for the key change. Saying so is the difference
// between coverage and a claim.
//
// Server-gated: runs wherever the PostgreSQL DSNs are configured — locally against
// the dev container's live server (verified by execution, 2026-07-26) and in CI.
func TestPostgresOpenRefusesAGuardMissingFromItsTable(t *testing.T) {
	superDSN := requirePostgresSuperuserDSN(t, "the missing-guard boot regression")
	ctx := context.Background()
	db, dsn := pgTestDB(t, superDSN)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}

	st, err := Open(ctx, cfg, registerTriggerModule)
	if err != nil {
		t.Fatalf("baseline open: %v", err)
	}
	_ = st.Close()

	mustExec(t, db, "CREATE TABLE tgm_decoy (id BIGINT PRIMARY KEY)")
	mustExec(t, db, fmt.Sprintf("DROP TRIGGER %s ON %s", tgmTrigger, tgmTable))
	mustExec(t, db, fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE TRUNCATE ON tgm_decoy FOR EACH STATEMENT EXECUTE FUNCTION olivares_block_mutation()",
		tgmTrigger))

	st, err = Open(ctx, cfg, registerTriggerModule)
	if err == nil {
		_ = st.Close()
		t.Fatalf("Open accepted a schema where %q is absent from %q, though the name exists elsewhere",
			tgmTrigger, tgmTable)
	}
	if !errors.Is(err, store.ErrSchemaTriggerMissing) {
		t.Fatalf("Open failed for a reason that is not the identity check: %v", err)
	}
	if !strings.Contains(err.Error(), tgmTable+"."+tgmTrigger) {
		t.Fatalf("the refusal does not name the unguarded table: %v", err)
	}
	if strings.Contains(err.Error(), "tgm_decoy") {
		t.Fatalf("the error blames the decoy table instead of the unguarded one: %v", err)
	}
}
