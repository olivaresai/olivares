// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// finopscustodymigration_pg_test.go is the v12 foundation on an OWNED PostgreSQL server:
// the engine whose role model the establishment statements exist for, and the one where
// ENABLE ALWAYS and statement-level TRUNCATE guards mean anything.
//
// THE SAME LABEL AS THE SQLITE FILE APPLIES, and it has to be repeated because the
// temptation to read a green PostgreSQL run as an upgrade proof is stronger here: this is a
// V12-ISOLATED FIXTURE. An empty database owned by a per-test owner role, one plan holding
// v12 alone, applied by the real applier. It is NOT a v10 → v12 production upgrade, and
// nothing here observes v12 arriving after v11 in the core plan.
//
// WHAT IS MEASURED AS EFFECTIVE PERMISSION, not as issued statement: the split-topology
// claim is read back from `has_table_privilege` and `has_any_column_privilege` as the SERVER
// computes them for the application role, which is the only form that includes PUBLIC and
// inherited grants. Asserting that the GRANT text was emitted would prove the test's own
// literal.

// custodyPG provisions an isolated split-owner database and returns the owner pool, the app
// pool, the dialect and the resolved role pair.
func custodyPG(t *testing.T) (owner, app *sql.DB, dia dialect.Dialect, roles guardRoles) {
	t.Helper()
	dsns := isolatedPGSplit(t)
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	owner = openCustodyPGPool(t, dsns.Owner)
	app = openCustodyPGPool(t, dsns.App)

	ownerRole := currentCustodyRole(t, owner)
	appRole := currentCustodyRole(t, app)
	if ownerRole == appRole {
		t.Fatalf("the split fixture resolved one role %q for both pools", ownerRole)
	}
	return owner, app, dia, guardRoles{
		App:             guardRoleFact{Known: true, Role: appRole},
		Owner:           guardRoleFact{Known: true, Role: ownerRole},
		OwnerConfigured: true,
	}
}

func openCustodyPGPool(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open the PostgreSQL pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// currentCustodyRole asks the SERVER which role a pool authenticated as. The DSN's user
// field is not the answer: a connection may authenticate as a role the DSN spells
// differently, and the whole topology classification is about resolved roles.
func currentCustodyRole(t *testing.T, db *sql.DB) string {
	t.Helper()
	var role string
	if err := db.QueryRowContext(context.Background(), "SELECT CURRENT_USER").Scan(&role); err != nil {
		t.Fatalf("resolve the pool's role: %v", err)
	}
	return role
}

// applyCustodyPG applies the v12 plan as the OWNER, which is the pool that holds DDL
// authority in the split topology.
func applyCustodyPG(t *testing.T, owner *sql.DB, dia dialect.Dialect, roles ...guardRoles) {
	t.Helper()
	if err := migrate.Apply(context.Background(), owner, dia, finOpsCustodyTestTrackingTable,
		[]migrate.Migration{coreFinOpsCustodyControlMigration(dia, roles...)}); err != nil {
		t.Fatalf("apply the v12 custody foundation: %v", err)
	}
}

// TestPGCustodyLegalEnrollmentSequence is the accepted path on PostgreSQL, and the control
// positive for every refusal below.
func TestPGCustodyLegalEnrollmentSequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner, _, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)
	enrollThrough(t, ctx, owner, dia, custodyInstanceA)

	var state string
	var rev, high, conf, act int
	if err := owner.QueryRowContext(ctx, dia.Rebind("SELECT state, revision, highest_generation, confirmed_generation, active_generation FROM "+
		directoryWriterRelation(dia, dialect.ControlCustodyEnrollmentTable)+" WHERE custody_instance_id = ?"), custodyInstanceA).
		Scan(&state, &rev, &high, &conf, &act); err != nil {
		t.Fatalf("read the head: %v", err)
	}
	if state != "active" || rev != 3 || high != 1 || conf != 1 || act != 1 {
		t.Fatalf("head after activation is (%s, %d, %d, %d, %d), want (active, 3, 1, 1, 1)", state, rev, high, conf, act)
	}
}

// TestPGCustodyRejectsMalformedScalarsAndOrder covers the NULL, UUID, ordering and
// after-image legs on the engine that enforces column types itself.
//
// THE PER-CASE DATABASE IS DELIBERATE: a refused statement aborts the whole PostgreSQL
// transaction, and a shared one would make every case after the first fail for the
// preceding case's reason. Each subtest therefore runs its own fixture.
func TestPGCustodyRejectsMalformedScalarsAndOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner, _, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)

	for _, tc := range []struct {
		name  string
		patch map[string]any
	}{
		{"null to_highest", map[string]any{"to_highest": nil}},
		{"null actor", map[string]any{"actor": nil}},
		{"uppercase uuid", map[string]any{"custody_instance_id": "7F3A1C2E-4B5D-4E6F-8A9B-0C1D2E3F4A5B"}},
		{"version nibble not 4", map[string]any{"custody_instance_id": "7f3a1c2e-4b5d-1e6f-8a9b-0c1d2e3f4a5b"}},
		{"variant class out of range", map[string]any{"custody_instance_id": "7f3a1c2e-4b5d-4e6f-7a9b-0c1d2e3f4a5b"}},
		{"non-hex character", map[string]any{"custody_instance_id": "7f3a1c2g-4b5d-4e6f-8a9b-0c1d2e3f4a5b"}},
		{"foreign domain", map[string]any{"custody_domain": "finops.policy_recovery.v2"}},
		{"unknown transition", map[string]any{"transition": "rotate_custody"}},
		{"core_version below 12", map[string]any{"core_version": 11}},
		{"guard_epoch outside the closed set", map[string]any{"guard_epoch": 7}},
		{"revision-1 row carrying a before-image", map[string]any{"from_state": "enrolling", "from_highest": 1, "from_confirmed": 0, "from_active": 0}},
		{"partially populated before-image", map[string]any{"from_state": "enrolling"}},
		// The case only the revision-1 equivalence refuses: the `begin_enrollment` row
		// predicate pins from_state and not the other three, so a NULL state with a
		// populated generation image clears every other CHECK in the relation.
		{"null state with a populated generation image", map[string]any{"from_highest": 5}},
		{"empty actor", map[string]any{"actor": ""}},
		{"generation above the cap", map[string]any{"to_highest": 33, "to_confirmed": 33, "to_active": 0}},
	} {
		row := beginEnrollmentRow(custodyInstanceA)
		for k, v := range tc.patch {
			row[k] = v
		}
		if err := insertTransition(ctx, owner, dia, row); err == nil {
			t.Errorf("%s: must be refused, was accepted", tc.name)
		}
	}

	// Control positive after the refusals.
	if err := insertTransition(ctx, owner, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("the unpatched legal row must be accepted: %v", err)
	}
	// And the order guard, on the same database now that a legal rev 1 exists.
	if err := insertHead(ctx, owner, dia, custodyInstanceA); err == nil {
		t.Error("a head with no generation-1 proof must be refused")
	}
}

// TestPGCustodyRefusesIllegalOrderingAndAfterImage is the statement-order and head-advance
// leg, each case on its own fixture.
func TestPGCustodyRefusesIllegalOrderingAndAfterImage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("head before its journal row", func(t *testing.T) {
		t.Parallel()
		owner, _, dia, roles := custodyPG(t)
		applyCustodyPG(t, owner, dia, roles)
		if err := insertHead(ctx, owner, dia, custodyInstanceA); err == nil {
			t.Fatal("a head with no recorded begin_enrollment must be refused")
		}
	})

	t.Run("journal skips a revision", func(t *testing.T) {
		t.Parallel()
		owner, _, dia, roles := custodyPG(t)
		applyCustodyPG(t, owner, dia, roles)
		enrollThrough(t, ctx, owner, dia, custodyInstanceA)
		row := activateRow(custodyInstanceA)
		row["to_revision"] = 5
		if err := insertTransition(ctx, owner, dia, row); err == nil {
			t.Fatal("a journal row that skips its predecessor must be refused")
		}
	})

	t.Run("head advances to an unrecorded after-image", func(t *testing.T) {
		t.Parallel()
		owner, _, dia, roles := custodyPG(t)
		applyCustodyPG(t, owner, dia, roles)
		enrollThrough(t, ctx, owner, dia, custodyInstanceA)
		if err := advanceHead(ctx, owner, dia, custodyInstanceA, "active", 4, 1, 1, 1); err == nil {
			t.Fatal("a head advance with no journal row must be refused")
		}
	})

	t.Run("duplicate live enrollment", func(t *testing.T) {
		t.Parallel()
		owner, _, dia, roles := custodyPG(t)
		applyCustodyPG(t, owner, dia, roles)
		enrollThrough(t, ctx, owner, dia, custodyInstanceA)
		if err := insertTransition(ctx, owner, dia, beginEnrollmentRow(custodyInstanceB)); err == nil {
			t.Fatal("opening a second instance while one is live must be refused")
		}
	})

	t.Run("proof without its minting transition", func(t *testing.T) {
		t.Parallel()
		owner, _, dia, roles := custodyPG(t)
		applyCustodyPG(t, owner, dia, roles)
		if err := insertProof(ctx, owner, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "enrollment"); err == nil {
			t.Fatal("a proof with no minting transition must be refused")
		}
	})
}

// TestPGCustodyRowsAreImmutableRetainedAndUntruncatable is the append-only, retention and
// TRUNCATE leg, measured AS THE OWNER.
//
// As the owner on purpose, and this is the trap the measurement exists to avoid: the
// split-topology ACL denies the application role UPDATE/DELETE/TRUNCATE outright, so its
// statements fail with `permission denied` before any trigger runs. A probe from that pool
// would "pass" while measuring the ACL a second time and the guard not at all. The owner is
// exempt from the revoke, so its statements do reach the trigger, and the assertion is on
// the REASON — the guard's own message — not merely on an error having occurred.
func TestPGCustodyRowsAreImmutableRetainedAndUntruncatable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner, _, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)
	enrollThrough(t, ctx, owner, dia, custodyInstanceA)

	for _, table := range []string{dialect.ControlCustodyProofTable, dialect.ControlCustodyTransitionTable} {
		rel := directoryWriterRelation(dia, table)
		var seeded int
		if err := owner.QueryRowContext(ctx, "SELECT pg_catalog.count(*) FROM "+rel).Scan(&seeded); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if seeded == 0 {
			t.Fatalf("%s must be seeded before the immutability probe", table)
		}
		for _, probe := range []struct{ name, stmt string }{
			{"UPDATE", "UPDATE " + rel + " SET recorded_at = '" + custodyStamp + "'"},
			{"DELETE", "DELETE FROM " + rel},
			{"TRUNCATE", "TRUNCATE TABLE " + rel},
		} {
			_, err := owner.ExecContext(ctx, probe.stmt)
			if err == nil {
				t.Errorf("%s on %s must be refused", probe.name, table)
				continue
			}
			want := table + " is append-only"
			if probe.name == "TRUNCATE" {
				want = "is not truncatable"
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s on %s was refused for the wrong reason: want %q, got %v", probe.name, table, want, err)
			}
		}
		var after int
		if err := owner.QueryRowContext(ctx, "SELECT pg_catalog.count(*) FROM "+rel).Scan(&after); err != nil {
			t.Fatalf("recount %s: %v", table, err)
		}
		if after != seeded {
			t.Errorf("%s holds %d rows after the refused probes, want the retained %d", table, after, seeded)
		}
	}

	// The head: no DELETE, no TRUNCATE.
	head := directoryWriterRelation(dia, dialect.ControlCustodyEnrollmentTable)
	if _, err := owner.ExecContext(ctx, "DELETE FROM "+head); err == nil {
		t.Error("DELETE on the head must be refused")
	} else if !strings.Contains(err.Error(), "is not deletable") {
		t.Errorf("the head DELETE refusal must name the guard, got %v", err)
	}
	if _, err := owner.ExecContext(ctx, "TRUNCATE TABLE "+head); err == nil {
		t.Error("TRUNCATE on the head must be refused")
	} else if !strings.Contains(err.Error(), "is not truncatable") {
		t.Errorf("the head TRUNCATE refusal must name the guard, got %v", err)
	}
}

// TestPGCustodyGuardsAreEnabledAlways reads the catalog's firing state for every v12 trigger.
//
// 'A' and nothing else. A guard left in the default 'O' does not fire in a replica session,
// so an UPDATE arriving through logical replication applies with zero errors — the exact
// failure this repository measured on 15.18 for the C4 logs. The TRUNCATE guards are also
// required to be STATEMENT level, because a FOR EACH ROW guard is silent on TRUNCATE.
func TestPGCustodyGuardsAreEnabledAlways(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner, _, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)

	rows, err := owner.QueryContext(ctx, `SELECT t.tgname, t.tgenabled, (t.tgtype & 1) = 0
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND NOT t.tgisinternal AND c.relname = ANY($2)
ORDER BY t.tgname`, dialect.EngineSchema, pqCustodyTables())
	if err != nil {
		t.Fatalf("read the trigger catalog: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var name, enabled string
		var statementLevel bool
		if err := rows.Scan(&name, &enabled, &statementLevel); err != nil {
			t.Fatalf("scan a trigger row: %v", err)
		}
		seen[name] = true
		if enabled != "A" {
			t.Errorf("trigger %s fires in state %q, want \"A\" (ENABLE ALWAYS)", name, enabled)
		}
		wantStatement := strings.HasSuffix(name, "_no_truncate")
		if statementLevel != wantStatement {
			t.Errorf("trigger %s is statement-level=%v, want %v", name, statementLevel, wantStatement)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the trigger catalog: %v", err)
	}

	want := dialect.PostgresFinOpsCustodyTriggerNames()
	for _, name := range want {
		if !seen[name] {
			t.Errorf("trigger %s is absent from the catalog", name)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("the catalog holds %d custody triggers, want exactly the %d v12 creates", len(seen), len(want))
	}
}

// TestPGCustodySplitEstablishmentIsSelectOnly is the establishment assertion, read as the
// server's EFFECTIVE permissions for the application role.
//
// It asserts the three claims §4.4 makes for the split topology and nothing beyond them:
// PUBLIC holds nothing, the application role is not the owner, and it holds SELECT and
// NOTHING else — no table write, no column-level write, no grant option. The column-level
// and grant-option forms are included because a table-level check alone passes a relation
// whose INSERT was granted one column at a time.
func TestPGCustodySplitEstablishmentIsSelectOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner, app, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)

	// PUBLIC first: it is never the owner and never the application role, so a grant to
	// it would hand the relations to every role in the cluster, including later ones.
	rows, err := owner.QueryContext(ctx, `SELECT c.relname,
  EXISTS (SELECT 1 FROM pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) x WHERE x.grantee = 0),
  EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a
          CROSS JOIN LATERAL pg_catalog.aclexplode(a.attacl) x
          WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped AND x.grantee = 0)
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'r' AND c.relname = ANY($2)
ORDER BY c.relname`, dialect.EngineSchema, pqCustodyTables())
	if err != nil {
		t.Fatalf("read the PUBLIC ACL: %v", err)
	}
	publicRows := 0
	for rows.Next() {
		var name string
		var tableGrant, columnGrant bool
		if err := rows.Scan(&name, &tableGrant, &columnGrant); err != nil {
			rows.Close()
			t.Fatalf("scan a PUBLIC ACL row: %v", err)
		}
		publicRows++
		if tableGrant || columnGrant {
			t.Errorf("%s is open to PUBLIC (table=%v, column=%v)", name, tableGrant, columnGrant)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the PUBLIC ACL: %v", err)
	}
	if publicRows != len(dialect.FinOpsCustodyControlTables()) {
		t.Fatalf("the PUBLIC check saw %d relations, want %d", publicRows, len(dialect.FinOpsCustodyControlTables()))
	}

	// The application role's effective privileges, computed BY THE SERVER so PUBLIC and
	// inherited grants are included.
	appRows, err := owner.QueryContext(ctx, `SELECT c.relname, c.relowner = a.oid,
  pg_catalog.has_table_privilege(a.oid, c.oid, 'SELECT'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'),
  pg_catalog.has_table_privilege(a.oid, c.oid, 'SELECT WITH GRANT OPTION'),
  pg_catalog.has_any_column_privilege(a.oid, c.oid, 'INSERT,UPDATE,REFERENCES'),
  pg_catalog.has_any_column_privilege(a.oid, c.oid, 'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION')
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN pg_catalog.pg_roles a
WHERE n.nspname = $1 AND a.rolname = $2 AND c.relkind = 'r' AND c.relname = ANY($3)
ORDER BY c.relname`, dialect.EngineSchema, roles.App.Role, pqCustodyTables())
	if err != nil {
		t.Fatalf("read the application role ACL: %v", err)
	}
	defer appRows.Close()
	appSeen := 0
	for appRows.Next() {
		var name string
		var owns, canSelect, canWrite, selectGrantOption, columnWrite, columnGrantOption bool
		if err := appRows.Scan(&name, &owns, &canSelect, &canWrite, &selectGrantOption, &columnWrite, &columnGrantOption); err != nil {
			t.Fatalf("scan an application ACL row: %v", err)
		}
		appSeen++
		if owns {
			t.Errorf("%s: the application role owns the relation, so no boundary exists", name)
		}
		if !canSelect {
			t.Errorf("%s: the application role must hold SELECT", name)
		}
		if canWrite || selectGrantOption || columnWrite || columnGrantOption {
			t.Errorf("%s: the application role holds more than SELECT (write=%v, select-grant-option=%v, column-write=%v, column-grant-option=%v)",
				name, canWrite, selectGrantOption, columnWrite, columnGrantOption)
		}
	}
	if err := appRows.Err(); err != nil {
		t.Fatalf("iterate the application ACL: %v", err)
	}
	if appSeen != len(dialect.FinOpsCustodyControlTables()) {
		t.Fatalf("the application ACL check saw %d relations, want %d", appSeen, len(dialect.FinOpsCustodyControlTables()))
	}

	// AND THE BEHAVIOURAL CONFIRMATION, because a catalog answer and a refused statement
	// are different claims: the application pool can read and cannot write.
	for _, table := range dialect.FinOpsCustodyControlTables() {
		rel := directoryWriterRelation(dia, table)
		var n int
		if err := app.QueryRowContext(ctx, "SELECT pg_catalog.count(*) FROM "+rel).Scan(&n); err != nil {
			t.Errorf("the application pool must be able to SELECT %s: %v", table, err)
		}
		if _, err := app.ExecContext(ctx, "TRUNCATE TABLE "+rel); err == nil {
			t.Errorf("the application pool must not be able to TRUNCATE %s", table)
		}
	}
}

// TestPGCustodySingleRoleEstablishesOnlyPublic is the declared LIMIT of the single-role
// topology, asserted rather than described.
//
// Under one role the application pool IS the owner, so no SELECT-only boundary can exist and
// none is claimed. What IS established is the PUBLIC revoke, and the test requires exactly
// that: PUBLIC holds nothing, and the application role still writes — because revoking the
// engine's own writer is the fatal shape an unconditional establishment would have.
func TestPGCustodySingleRoleEstablishesOnlyPublic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the custody single-role leg", pgtest.EnvSuperuserDSN)
	}
	dsns := isolatedPGSplit(t)
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	// The OWNER pool, declared single-role: OwnerConfigured false means the operator
	// configured one role, which is a known answer and not an unresolved one.
	owner := openCustodyPGPool(t, dsns.Owner)
	role := currentCustodyRole(t, owner)
	applyCustodyPG(t, owner, dia, guardRoles{App: guardRoleFact{Known: true, Role: role}})

	// PUBLIC holds nothing.
	for _, table := range dialect.FinOpsCustodyControlTables() {
		var open bool
		if err := owner.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) x
  WHERE n.nspname = $1 AND c.relname = $2 AND x.grantee = 0)`, dialect.EngineSchema, table).Scan(&open); err != nil {
			t.Fatalf("read the PUBLIC ACL of %s: %v", table, err)
		}
		if open {
			t.Errorf("%s is open to PUBLIC under the single-role topology", table)
		}
	}

	// And the one role still writes, through legal transitions. If the establishment had
	// revoked unconditionally this sequence would fail 42501 on its first statement.
	enrollThrough(t, ctx, owner, dia, custodyInstanceA)
}

// pqCustodyTables renders the closed target list as a PostgreSQL text array literal, so one
// bound parameter carries all three names.
func pqCustodyTables() string {
	return "{" + strings.Join(dialect.FinOpsCustodyControlTables(), ",") + "}"
}

// TestPGCustodyCanonicalTextUnchangedUnderNULCorrection is the PostgreSQL side of the
// embedded-NUL correction, and its job is to show that nothing there changed.
//
// WHAT IS ASSERTED, precisely, because the honest claim here is narrower than on SQLite:
//
//  1. the canonical instance id and key reference are still accepted — the correction
//     refuses NUL, it does not narrow the canonical set, and the PostgreSQL predicates were
//     not touched at all;
//  2. the same NUL-bearing values that SQLite now refuses by CHECK are refused here too, and
//     the refusal is REACHED — the statement does not succeed.
//
// WHAT IS NOT ASSERTED, and is named rather than left to be inferred:
//
//   - NOT that the two engines produce the same error string. They do not, and they are not
//     refusing through the same layer: on SQLite a CHECK constraint evaluates a stored value,
//     while here the refusal comes from the text transport before any constraint is consulted.
//   - NOT that a NUL byte was transmitted to the server and stored in a `text` field. It was
//     not; PostgreSQL `text` cannot hold one. The assertion is that the write does not
//     succeed, which is the only property this engine can offer.
//   - NOT that a byte-length CHECK exists on PostgreSQL. None was added, because a predicate
//     has nothing to refuse where the type system already refuses it.
func TestPGCustodyCanonicalTextUnchangedUnderNULCorrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner, _, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)

	// (1) The canonical values, unchanged. This runs FIRST so a transport failure below
	// cannot be mistaken for the relations being unusable.
	if err := insertTransition(ctx, owner, dia, beginEnrollmentRow(custodyInstanceA)); err != nil {
		t.Fatalf("the canonical instance id must still be accepted: %v", err)
	}
	if err := insertProof(ctx, owner, dia, custodyInstanceA, 1, 1, custodyKeyRefG1, "enrollment"); err != nil {
		t.Fatalf("the canonical key reference must still be accepted: %v", err)
	}

	// (2) The NUL-bearing values do not get written. Each runs on its own connection-level
	// statement; a refusal from the transport leaves the pool usable, unlike a server-side
	// error inside a transaction.
	for _, tc := range []struct {
		name  string
		probe func() error
	}{
		{"instance id with a NUL and a suffix", func() error {
			row := beginEnrollmentRow(custodyInstanceB)
			row["custody_instance_id"] = nulSuffixInstance
			return insertTransition(ctx, owner, dia, row)
		}},
		{"instance id padded to 36 bytes by a NUL", func() error {
			row := beginEnrollmentRow(custodyInstanceB)
			row["custody_instance_id"] = nulPaddedInstance
			return insertTransition(ctx, owner, dia, row)
		}},
		{"key reference with a NUL and a suffix", func() error {
			return insertProof(ctx, owner, dia, custodyInstanceA, 2, 2, nulSuffixKeyRef, "generation_add")
		}},
	} {
		if err := tc.probe(); err == nil {
			t.Errorf("%s: the write must not succeed", tc.name)
		}
	}

	// PERSISTED STATE: the canonical rows written in step 1, and nothing the refusals
	// attempted. Counting is the assertion that matters — a refused write that left a row
	// would be the defect, whichever layer refused.
	for _, probe := range []struct {
		table string
		want  int
	}{
		{dialect.ControlCustodyTransitionTable, 1},
		{dialect.ControlCustodyProofTable, 1},
	} {
		var rows int
		if err := owner.QueryRowContext(ctx, "SELECT pg_catalog.count(*) FROM "+
			directoryWriterRelation(dia, probe.table)).Scan(&rows); err != nil {
			t.Fatalf("count %s: %v", probe.table, err)
		}
		if rows != probe.want {
			t.Errorf("%s holds %d rows, want the %d canonical ones", probe.table, rows, probe.want)
		}
	}

	// And the stored canonical widths, read from the server: 36 and 30 bytes, no NUL.
	var idBytes, keyBytes int
	if err := owner.QueryRowContext(ctx, "SELECT pg_catalog.octet_length(custody_instance_id) FROM "+
		directoryWriterRelation(dia, dialect.ControlCustodyTransitionTable)).Scan(&idBytes); err != nil {
		t.Fatalf("measure the stored instance id: %v", err)
	}
	if err := owner.QueryRowContext(ctx, "SELECT pg_catalog.octet_length(custody_key_ref) FROM "+
		directoryWriterRelation(dia, dialect.ControlCustodyProofTable)).Scan(&keyBytes); err != nil {
		t.Fatalf("measure the stored key reference: %v", err)
	}
	if idBytes != 36 || keyBytes != 30 {
		t.Errorf("stored canonical widths are %d and %d bytes, want 36 and 30", idBytes, keyBytes)
	}
}
