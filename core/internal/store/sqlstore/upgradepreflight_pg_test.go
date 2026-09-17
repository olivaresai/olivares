// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The HC-R1 causal matrix, measured against a real PostgreSQL 16.15 server through the
// project's own split-owner provisioner. No fake database and no skipped Postgres leg:
// the whole contract is about what a REAL server answers for a REAL role, and a fake
// would answer whatever it was written to answer.
//
// The shape every negative case shares is the one that matters: a byte-identical
// before/after snapshot of the estate — every relation, every OID, every ACL, every row
// of every table (which is every tracker and every receipt), every trigger, every
// policy and every event trigger. "Refused before its first durable change" is a claim
// about the database, so it is asserted about the database rather than about the error.

// hcr1Fixture is one complete split-owner estate plus the three connections a causal
// test needs: the app pool (the subject), the owner pool (which holds the grants to
// give and take), and a superuser pool used ONLY to observe. Observation goes through
// the superuser because the snapshot reads every row of every table and the ledger
// carries FORCE row-level security: any other role would silently observe less.
type hcr1Fixture struct {
	pg        pgtest.DSNs
	app       *sql.DB
	owner     *sql.DB
	super     *sql.DB
	appRole   string
	ownerRole string
	cfg       store.Config
}

func hcr1Split(t *testing.T, register func(store.ExtensionRegistry) error) *hcr1Fixture {
	t.Helper()
	ctx := context.Background()
	f := hcr1SplitUnopened(t)
	// A complete estate first. Every case below is a DIFFERENCE from a schema this
	// binary itself produced, which is what makes the difference the cause.
	st, err := Open(ctx, f.cfg, register)
	if err != nil {
		t.Fatalf("build the complete split-owner estate: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close the prepared store: %v", err)
	}
	return f
}

// hcr1SplitUnopened is hcr1Split without the first Open: a provisioned split database
// this binary has never migrated. It exists for the one property that can only be
// observed on a genuinely fresh estate — what the FIRST boot does.
func hcr1SplitUnopened(t *testing.T) *hcr1Fixture {
	t.Helper()
	pg := isolatedPGSplit(t)
	f := &hcr1Fixture{
		pg:    pg,
		app:   hcr1Open(t, pg.App),
		owner: hcr1Open(t, pg.Owner),
		super: hcr1Open(t, pg.Superuser),
		cfg: store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
		},
	}
	f.appRole = hcr1Role(t, f.app)
	f.ownerRole = hcr1Role(t, f.owner)
	if f.appRole == f.ownerRole {
		t.Fatalf("fixture is not split: app and owner both authenticate as %q", f.appRole)
	}
	return f
}

func hcr1Open(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open fixture connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func hcr1Role(t *testing.T, db *sql.DB) string {
	t.Helper()
	var role string
	if err := db.QueryRowContext(context.Background(), `SELECT current_user`).Scan(&role); err != nil {
		t.Fatalf("resolve current role: %v", err)
	}
	return role
}

func hcr1Exec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("fixture statement %.90q: %v", stmt, err)
	}
}

// hcr1RevokeFutureDefaults removes the app role's FUTURE-object default privileges and
// nothing else. Grants already materialized on existing relations survive untouched,
// which is precisely the "provisioned by hand / restored estate" posture: the schema
// looks complete and is complete, and the next relation the owner creates will be
// unreachable to the application.
//
// It runs AS THE OWNER because ALTER DEFAULT PRIVILEGES without FOR ROLE targets
// current_user, and the owner is the role whose future creations are in question.
func hcr1RevokeFutureDefaults(t *testing.T, f *hcr1Fixture) {
	t.Helper()
	hcr1Exec(t, f.owner, `ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM `+quoteIdent(f.appRole))
}

func hcr1GrantFutureDefaults(t *testing.T, f *hcr1Fixture, grantee string) {
	t.Helper()
	hcr1Exec(t, f.owner, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO `+grantee)
}

// hcr1StateSnapshot is the whole estate as text: the logical snapshot (relations,
// columns, constraints, indexes, triggers, policies, routines and EVERY ROW OF EVERY
// TABLE — so every tracker and every receipt) plus the exact OIDs, owners and ACLs, plus
// the event-trigger identities.
//
// OIDs are in it deliberately. A relation dropped and recreated with identical contents
// is NOT the same relation, and a snapshot that compared only names and rows would call
// that a no-op.
func hcr1StateSnapshot(t *testing.T, f *hcr1Fixture) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(pgLogicalSnapshot(t, f.super, dialect.EngineSchema))
	for _, query := range []string{
		`SELECT c.oid::text || ' ' || c.relname || ' ' || c.relkind::text || ' owner=' || c.relowner::text || ' acl=' || COALESCE(c.relacl::text, '')
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' ORDER BY c.oid`,
		`SELECT e.oid::text || ' ' || e.evtname || ' ' || e.evtevent || ' ' || e.evtenabled::text || ' fn=' || e.evtfoid::text
FROM pg_catalog.pg_event_trigger e ORDER BY e.oid`,
		`SELECT d.oid::text || ' ' || d.defaclrole::text || ' ' || d.defaclobjtype::text || ' ' || COALESCE(d.defaclacl::text, '')
FROM pg_catalog.pg_default_acl d ORDER BY d.oid`,
	} {
		for _, line := range pgQueryStrings(t, f.super, query) {
			b.WriteString("CAT\t")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// hcr1RequireUnchanged is the assertion that carries the contract.
func hcr1RequireUnchanged(t *testing.T, f *hcr1Fixture, before, what string) {
	t.Helper()
	if after := hcr1StateSnapshot(t, f); after != before {
		t.Fatalf("%s CHANGED the estate — relations, OIDs, owners, ACLs, rows (every tracker and receipt), guards or the event fence differ\n--- before ---\n%s\n--- after ---\n%s",
			what, before, after)
	}
}

// hcr1RequireNoProbeSurvived proves the probe left nothing behind, under its own name
// prefix rather than a name the test remembers: what must be true is that NO probe
// relation exists, not merely that one particular one is gone.
func hcr1RequireNoProbeSurvived(t *testing.T, f *hcr1Fixture) {
	t.Helper()
	var n int
	if err := f.super.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relname LIKE 'olivares\_fresh\_probe\_%'`, dialect.EngineSchema).Scan(&n); err != nil {
		t.Fatalf("look for surviving probe relations: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d probe relation(s) survived: the probe transaction must always roll back", n)
	}
}

func hcr1RelationExists(t *testing.T, f *hcr1Fixture, name string) bool {
	t.Helper()
	return pgRelationExists(t, f.super, dialect.EngineSchema, name)
}

func hcr1CountRows(t *testing.T, f *hcr1Fixture, table string) int64 {
	t.Helper()
	var n int64
	if err := f.super.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+dialect.EngineSchema+`.`+quoteIdent(table)).Scan(&n); err != nil {
		t.Fatalf("count rows of %q: %v", table, err)
	}
	return n
}

// hcr1MakeRelationsFuture removes relations this binary's plan will create again, which
// is what an upgrade genuinely looks like from the preflight's point of view: objects
// the schema declares and the database does not yet have.
//
// leader_epoch and the module table are the two safe, faithful choices. Neither is a
// core descriptor whose absence the engine correctly treats as CORRUPTION rather than
// as schema growth — dropping core_user_authority, for instance, is damage and the
// engine refuses it, which is why H's membership in the inventory is asserted at the
// inventory level instead (see TestUpgradePreflightInventory...).
func hcr1MakeRelationsFuture(t *testing.T, f *hcr1Fixture, moduleTable string) []string {
	t.Helper()
	hcr1Exec(t, f.owner, `DROP TABLE `+dialect.EngineSchema+`.`+quoteIdent(leaderEpochTable))
	if moduleTable == "" {
		return []string{leaderEpochTable}
	}
	hcr1Exec(t, f.owner, `DROP TABLE `+dialect.EngineSchema+`.`+quoteIdent(moduleTable)+` CASCADE`)
	// The module tracking row goes with it. Left behind, applyModuleTables would skip
	// the table it thinks it already applied, and the test would be measuring a
	// half-repaired estate instead of an upgrade.
	if _, err := f.owner.ExecContext(context.Background(),
		`DELETE FROM `+moduleTablesTracking+` WHERE table_name = $1`, moduleTable); err != nil {
		t.Fatalf("clear the module tracking row for %q: %v", moduleTable, err)
	}
	return []string{leaderEpochTable, moduleTable}
}

// TestUpgradePreflightRefusesFutureRelationsAndChangesNothing is the HC-R1 negative, and
// it is the case the whole contract exists for.
//
// The estate is complete and correct EXCEPT that the application role has no route to
// relations that do not exist yet, and this upgrade creates some. Before HC-R1 that boot
// migrated — advancing the core tracking table and creating relations durably — and then
// failed with 42501, leaving an estate neither the old nor the new binary could serve.
//
// Two assertions carry it. The error must be the typed refusal, and the DATABASE must be
// byte-identical: same relations, same OIDs, same ACLs, same rows in every tracker and
// every receipt, same guards, same event triggers.
func TestUpgradePreflightRefusesFutureRelationsAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)

	hcr1RevokeFutureDefaults(t, f)
	future := hcr1MakeRelationsFuture(t, f, widgetDescriptor.Table)

	before := hcr1StateSnapshot(t, f)
	st, err := Open(ctx, f.cfg, registerWidget)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open ACCEPTED an upgrade whose new relations the application role could not use: this is the causal failure HC-R1 exists to remove")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("refusal does not carry the public sentinel: %v", err)
	}
	for _, relation := range future {
		if !strings.Contains(err.Error(), relation) {
			t.Errorf("the refusal does not name the pending relation %q: %v", relation, err)
		}
	}
	// The refusal has to be actionable, not merely typed: an operator reading it must be
	// able to act without reading this source file.
	for _, want := range []string{"ALTER DEFAULT PRIVILEGES", "migrate apply", "NOTHING HAS BEEN MIGRATED"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal omits %q, so it does not tell the operator what to do: %v", want, err)
		}
	}
	hcr1RequireUnchanged(t, f, before, "the refused upgrade")
	hcr1RequireNoProbeSurvived(t, f)

	// AND THE SAME REFUSAL MUST NOT NAME THE OWNER-ONLY RELATIONS. A preflight that
	// demanded application privileges on the tracking tables, the classification receipt
	// or the guard control plane would refuse the most correctly hardened deployments.
	for _, ownerOnly := range []string{
		coreTrackingTable, moduleTablesTracking,
		dialect.ControlRolloutClassificationTable,
		dialect.GuardGateEventsTable, dialect.GuardReceiptsTable,
	} {
		if strings.Contains(err.Error(), ownerOnly) {
			t.Errorf("the refusal names owner-only relation %q as an application requirement: %v", ownerOnly, err)
		}
	}
}

// TestUpgradePreflightAcceptsWhenOnlyTheGrantsChange is the positive half of the same
// fixture, and the ONLY difference between it and the negative above is the grant.
//
// That is what makes this pair causal rather than two independent observations: same
// binary, same estate, same pending relations, same code path — one route for the
// application role added, and the upgrade proceeds.
func TestUpgradePreflightAcceptsWhenOnlyTheGrantsChange(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	hcr1RevokeFutureDefaults(t, f)
	future := hcr1MakeRelationsFuture(t, f, widgetDescriptor.Table)

	if st, err := Open(ctx, f.cfg, registerWidget); err == nil {
		_ = st.Close()
		t.Fatal("the negative precondition does not hold: Open accepted the estate before the grant")
	}

	// THE SINGLE CAUSAL CHANGE.
	hcr1GrantFutureDefaults(t, f, quoteIdent(f.appRole))

	st, err := Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("Open REFUSED after the only change was to grant the application role its future-object privileges: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, relation := range future {
		if !hcr1RelationExists(t, f, relation) {
			t.Errorf("the accepted upgrade did not create %q", relation)
		}
	}
	hcr1RequireNoProbeSurvived(t, f)
}

// TestUpgradePreflightMeasuresEffectivePrivilegeNotItsProvenance is the reason the probe
// asks has_table_privilege instead of reading pg_default_acl.
//
// A route through a GROUP ROLE and a route through PUBLIC are both fully effective at
// runtime, and neither puts a row in pg_default_acl naming the application role. A
// check built on default-ACL inspection — or on direct grants — would refuse both of
// these correct deployments.
func TestUpgradePreflightMeasuresEffectivePrivilegeNotItsProvenance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant func(t *testing.T, f *hcr1Fixture)
	}{
		{"through a group role the app inherits", func(t *testing.T, f *hcr1Fixture) {
			group := "hcr1_group_" + strings.TrimPrefix(f.pg.Database, "olv_")
			hcr1Exec(t, f.super, `CREATE ROLE `+quoteIdent(group)+` NOLOGIN NOSUPERUSER NOBYPASSRLS`)
			t.Cleanup(func() {
				ctx := context.Background()
				_, _ = f.owner.ExecContext(ctx, `ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM `+quoteIdent(group))
				_, _ = f.super.ExecContext(ctx, `REVOKE `+quoteIdent(group)+` FROM `+quoteIdent(f.appRole))
				_, _ = f.super.ExecContext(ctx, `DROP OWNED BY `+quoteIdent(group))
				_, _ = f.super.ExecContext(ctx, `DROP ROLE `+quoteIdent(group))
			})
			hcr1Exec(t, f.super, `GRANT `+quoteIdent(group)+` TO `+quoteIdent(f.appRole))
			hcr1GrantFutureDefaults(t, f, quoteIdent(group))
		}},
		{"through PUBLIC", func(t *testing.T, f *hcr1Fixture) {
			t.Cleanup(func() {
				_, _ = f.owner.ExecContext(context.Background(),
					`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM PUBLIC`)
			})
			hcr1GrantFutureDefaults(t, f, "PUBLIC")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := hcr1Split(t, registerWidget)
			hcr1RevokeFutureDefaults(t, f)
			hcr1MakeRelationsFuture(t, f, widgetDescriptor.Table)
			if st, err := Open(ctx, f.cfg, registerWidget); err == nil {
				_ = st.Close()
				t.Fatal("the negative precondition does not hold before the inherited route is added")
			}

			tc.grant(t, f)

			// The app role must hold NO DIRECT future route: otherwise this would pass
			// for the ordinary reason and prove nothing about inherited privilege.
			// 'r' is the TABLES object type, and the filter is the point: the
			// provisioner also sets a SEQUENCES default ACL, which this contract says
			// nothing about. Counting it would make the precondition fail for a reason
			// that has nothing to do with the relations under test.
			var direct bool
			if err := f.super.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM pg_catalog.pg_default_acl d
  CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) a
  JOIN pg_catalog.pg_roles r ON r.oid = a.grantee
  WHERE r.rolname = $1 AND d.defaclobjtype = 'r')`, f.appRole).Scan(&direct); err != nil {
				t.Fatalf("read pg_default_acl for the app role: %v", err)
			}
			if direct {
				t.Fatal("the app role still holds a DIRECT default-ACL route, so this case would not distinguish effective privilege from its provenance")
			}

			st, err := Open(ctx, f.cfg, registerWidget)
			if err != nil {
				t.Fatalf("Open refused a route that is fully effective at runtime: %v", err)
			}
			_ = st.Close()
		})
	}
}

// TestUpgradePreflightAcceptsAManualEstateWithNoDefaultACL is the other half of "not
// pg_default_acl": a schema that is already COMPLETE, whose grants were applied by hand,
// and whose pg_default_acl names the application role nowhere.
//
// There is nothing pending, so there is nothing to probe — and demanding a default-ACL
// row from this deployment would refuse an estate that is correct in every respect.
func TestUpgradePreflightAcceptsAManualEstateWithNoDefaultACL(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	hcr1RevokeFutureDefaults(t, f)

	// TABLES only ('r'): the provisioner also grants a SEQUENCES default ACL, and this
	// contract is about relations. A precondition that counted sequences would be
	// asserting something the preflight never asks about.
	var rows int
	if err := f.super.QueryRowContext(ctx, `SELECT COUNT(*)
FROM pg_catalog.pg_default_acl d
CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) a
JOIN pg_catalog.pg_roles r ON r.oid = a.grantee
WHERE r.rolname = $1 AND d.defaclobjtype = 'r'`, f.appRole).Scan(&rows); err != nil {
		t.Fatalf("read pg_default_acl: %v", err)
	}
	if rows != 0 {
		t.Fatalf("precondition: the app role still has %d table default-ACL entries, so this is not the manual posture", rows)
	}

	st, err := Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("Open refused a COMPLETE estate whose grants are manual and whose pg_default_acl is empty for the app role — pg_default_acl is a provisioning mechanism, not the contract: %v", err)
	}
	_ = st.Close()
	hcr1RequireNoProbeSurvived(t, f)
}

// TestUpgradePreflightRefusesAnExistingRelationInBothPurposes covers the reading that is
// NOT about the future: a relation that exists and that the application role cannot use.
//
// Both purposes must refuse it. `migrate apply` omits exactly one test — the one about
// objects that do not exist yet — and an operator who reads it as "apply ignores
// privileges" would be applying schema onto an estate that cannot serve afterwards.
func TestUpgradePreflightRefusesAnExistingRelationInBothPurposes(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	hcr1Exec(t, f.owner, `REVOKE UPDATE ON `+dialect.EngineSchema+`.orgs FROM `+quoteIdent(f.appRole))

	before := hcr1StateSnapshot(t, f)

	st, openErr := Open(ctx, f.cfg, registerWidget)
	if openErr == nil {
		_ = st.Close()
		t.Fatal("Open accepted an estate where the app role cannot UPDATE an existing mutable table")
	}
	if !errors.Is(openErr, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("Open's refusal does not carry the sentinel: %v", openErr)
	}
	applyErr := ApplyMigrations(ctx, f.cfg, registerWidget)
	if applyErr == nil {
		t.Fatal("ApplyMigrations accepted the same estate: the existing-relation reading is not the part `migrate apply` omits")
	}
	if !errors.Is(applyErr, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("ApplyMigrations' refusal does not carry the sentinel: %v", applyErr)
	}
	for _, err := range []error{openErr, applyErr} {
		if !strings.Contains(err.Error(), "orgs") || !strings.Contains(err.Error(), "UPDATE") {
			t.Errorf("the refusal names neither the relation nor the privilege: %v", err)
		}
		if !strings.Contains(err.Error(), "OID") {
			t.Errorf("the refusal does not report the OID it measured: %v", err)
		}
	}
	hcr1RequireUnchanged(t, f, before, "two refused preparations")
}

// TestUpgradePreflightKeepsTheSentinelThatNamesTheDefect is the contract this preflight
// is most likely to break by accident, and it broke twice while being written.
//
// Moving a refusal EARLIER must not rename it. Three conditions already had public
// sentinels that callers — including this package's own regressions — match on, and this
// check now reaches all three before the code that used to report them. A refusal that
// carried only the new sentinel would silently answer "no" to every caller asking "is the
// engine schema unusable?", "is the trigger boundary half-installed?" or "can the ACL be
// verified at all?". So each refusal carries BOTH: the new one says the upgrade was
// refused, the established one says what is wrong.
//
// The last case is the discriminating one: a plain mutable relation must NOT acquire an
// append-only sentinel, or the pairing would be decoration rather than classification.
func TestUpgradePreflightKeepsTheSentinelThatNamesTheDefect(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breakIt func(t *testing.T, f *hcr1Fixture)
		want    error
		notWant error
	}{
		{
			name: "no schema USAGE keeps ErrEngineSchemaUnusable",
			breakIt: func(t *testing.T, f *hcr1Fixture) {
				hcr1Exec(t, f.owner, `REVOKE USAGE ON SCHEMA `+dialect.EngineSchema+` FROM PUBLIC`)
				hcr1Exec(t, f.owner, `REVOKE USAGE ON SCHEMA `+dialect.EngineSchema+` FROM `+quoteIdent(f.appRole))
			},
			want: store.ErrEngineSchemaUnusable,
		},
		{
			name: "no INSERT on the ledger keeps ErrAppendOnlyGrantMissing",
			breakIt: func(t *testing.T, f *hcr1Fixture) {
				hcr1Exec(t, f.owner, `REVOKE INSERT ON `+dialect.EngineSchema+`.`+quoteIdent(auditTable)+` FROM `+quoteIdent(f.appRole))
			},
			want: store.ErrAppendOnlyGrantMissing,
		},
		{
			name: "a plain mutable relation carries NO append-only sentinel",
			breakIt: func(t *testing.T, f *hcr1Fixture) {
				hcr1Exec(t, f.owner, `REVOKE UPDATE ON `+dialect.EngineSchema+`.orgs FROM `+quoteIdent(f.appRole))
			},
			notWant: store.ErrAppendOnlyGrantMissing,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := hcr1Split(t, registerWidget)
			tc.breakIt(t, f)
			// AFTER the fixture's own revoke, not before: what is being measured is what
			// the refused BOOT changed, and a baseline taken before the revoke would
			// report the test's own setup as the boot's doing.
			before := hcr1StateSnapshot(t, f)

			st, err := Open(ctx, f.cfg, registerWidget)
			if err == nil {
				_ = st.Close()
				t.Fatal("Open accepted the broken estate")
			}
			if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
				t.Fatalf("the refusal lost the preflight sentinel: %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("the refusal dropped the established sentinel %v that names this defect: %v", tc.want, err)
			}
			if tc.notWant != nil && errors.Is(err, tc.notWant) {
				t.Fatalf("the refusal claimed %v for a relation that is not append-only: %v", tc.notWant, err)
			}
			// The estate is untouched either way: an earlier refusal is still a refusal
			// before the first durable change.
			hcr1RequireUnchanged(t, f, before, "the refused boot")
		})
	}
}

// TestMigrateApplyCeremonyAppliesSchemaGrantsThenServes is the ceremony end to end, and
// every step is observed rather than assumed.
//
// It is the answer to the negative above that does NOT weaken the preflight: no flag, no
// --force, no silent half-service. The operator declares that grants come after the
// schema by running a DIFFERENT command, and the boundary between migrating, granting
// and serving is observable at each step.
func TestMigrateApplyCeremonyAppliesSchemaGrantsThenServes(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	hcr1RevokeFutureDefaults(t, f)
	future := hcr1MakeRelationsFuture(t, f, widgetDescriptor.Table)

	// 1. The automatic route refuses, and changes nothing.
	before := hcr1StateSnapshot(t, f)
	if st, err := Open(ctx, f.cfg, registerWidget); err == nil {
		_ = st.Close()
		t.Fatal("serve accepted an estate whose pending relations the app role cannot use")
	} else if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("serve refused for another reason: %v", err)
	}
	hcr1RequireUnchanged(t, f, before, "the refused serve")

	// ARM THE RUNTIME-CUT WITNESS, before the phase runs rather than after.
	//
	// resolveBlindingMode runs AFTER the migration lock and, on a ledger whose durable
	// default is the blinded rule, records the actuation. It is the cleanest witness
	// available for "the runtime stretch executed": written there and nowhere else.
	//
	// The bit has to be CLEARED FIRST, and the clearing is what makes step 3 a
	// measurement at all — the fixture's own setup Open already actuated this ledger, so
	// reading the raw value after apply would be reading the SETUP's work and attributing
	// it to apply. (The engine never clears it; one-way is the production contract. This
	// is deliberate fixture surgery as the owner, and step 6 re-establishes it, which is
	// what proves the witness can still be set.)
	hcr1Exec(t, f.owner, `UPDATE `+dialect.EngineSchema+`.`+quoteIdent(dialect.AuditBlindingStateTable)+
		` SET actuated = 0 WHERE id = 1`)
	if hcr1BlindingActuated(t, f) {
		t.Fatal("fixture: the blinding actuation witness could not be cleared, so step 3 would prove nothing")
	}
	backendsBefore := hcr1BackendCount(t, f)

	// 2. The explicit phase applies the schema.
	if err := ApplyMigrations(ctx, f.cfg, registerWidget); err != nil {
		t.Fatalf("migrate apply refused the phase the operator asked for: %v", err)
	}
	for _, relation := range future {
		if !hcr1RelationExists(t, f, relation) {
			t.Fatalf("migrate apply returned success without creating %q", relation)
		}
	}

	// 3. THE RUNTIME STRETCH DID NOT RUN.
	if hcr1BlindingActuated(t, f) {
		t.Error("migrate apply actuated the audit blinding rule: it ran the runtime stretch it must return before")
	}
	// No epoch backfill, no leadership, no seeded fencing row.
	if n := hcr1CountRows(t, f, leaderEpochTable); n != 0 {
		t.Errorf("migrate apply wrote %d fencing epoch row(s): it must materialize the relation and nothing else", n)
	}
	// v10 is applied and H exists — and H is EMPTY. Creating the relation and FILLING it
	// are different acts, and this phase performs only the first.
	if !hcr1RelationExists(t, f, userAuthorityDescriptor.Table) {
		t.Fatalf("migrate apply did not create %q, so it did not complete the v10 schema", userAuthorityDescriptor.Table)
	}
	if n := hcr1CountRows(t, f, userAuthorityDescriptor.Table); n != 0 {
		t.Errorf("migrate apply backfilled %d row(s) into %q: applying v10 must not fill H", n, userAuthorityDescriptor.Table)
	}
	if !hcr1CoreVersionTracked(t, f, coreUserAuthorityMigrationVersion) {
		t.Errorf("migrate apply did not track core migration v%d", coreUserAuthorityMigrationVersion)
	}
	// Every pool this phase opened is closed. A migrate command that leaked a connection
	// would hold a session — and, on a failure path, an advisory lock — for as long as
	// the process lived.
	//
	// It is a DELTA against the count taken before the call, not an absolute zero: this
	// test holds its own app and owner connections for observation, and those are
	// backends too. And it is given a bounded moment, because a client closing a socket
	// is observed by the server asynchronously — an instantaneous read would be
	// measuring scheduling, not leakage.
	hcr1RequireBackendsSettle(t, f, backendsBefore, "migrate apply")

	// 4. Serving is still refused, because the grant is the operator's step and has not
	// happened. This is what stops the ceremony from being read as "apply then serve".
	if st, err := Open(ctx, f.cfg, registerWidget); err == nil {
		_ = st.Close()
		t.Fatal("serve opened after migrate apply but BEFORE the grant: the phases would then be indistinguishable")
	} else if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("serve refused for another reason: %v", err)
	}

	// 5. The grant, on relations that NOW EXIST — which is the whole point of the phase:
	// a manual grant can only name an object that exists. It names exactly the relations
	// this ceremony made future, the same discipline as
	// TestLeaderEpochIsMaterializedByTheSchemaPhase: a blanket `... ON ALL TABLES` also
	// re-grants DELETE and table-wide UPDATE to core v13's login_capability_observation,
	// whose ACL is a closed contract (SELECT+INSERT and column-scoped UPDATE only), and the
	// per-boot verifier refuses that drift instead of repairing it — measured as the step-6
	// refusal on runs 34967128097 and 35004444187 (race-core p2), no DATA RACE involved.
	for _, relation := range future {
		hcr1Exec(t, f.owner, `GRANT SELECT, INSERT, UPDATE, DELETE ON `+dialect.EngineSchema+`.`+quoteIdent(relation)+` TO `+quoteIdent(f.appRole))
	}

	// 6. Serve.
	st, err := Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("serve refused after the schema was applied and the grants were made: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// And NOW the runtime stretch has run, which is what makes step 3 a measurement
	// rather than a tautology about a fact nothing ever sets.
	if !hcr1BlindingActuated(t, f) {
		t.Error("serve did not actuate the audit blinding rule either, so the step-3 assertion proves nothing")
	}
	hcr1RequireNoProbeSurvived(t, f)
}

func hcr1BlindingActuated(t *testing.T, f *hcr1Fixture) bool {
	t.Helper()
	var actuated int64
	if err := f.super.QueryRowContext(context.Background(),
		`SELECT actuated FROM `+dialect.EngineSchema+`.`+quoteIdent(dialect.AuditBlindingStateTable)+` WHERE id = 1`).
		Scan(&actuated); err != nil {
		t.Fatalf("read the blinding actuation record: %v", err)
	}
	return actuated != 0
}

func hcr1CoreVersionTracked(t *testing.T, f *hcr1Fixture, version int) bool {
	t.Helper()
	var n int
	if err := f.super.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+coreTrackingTable+` WHERE version = $1`, version).Scan(&n); err != nil {
		t.Fatalf("read the core tracking table: %v", err)
	}
	return n > 0
}

// hcr1BackendCount counts server backends authenticated as this fixture's app or owner
// role, excluding the test's own observation connections (which authenticate as the
// superuser).
// hcr1RequireBackendsSettle waits, briefly and boundedly, for the server-side backend
// count to fall back to its baseline.
//
// A *sql.DB.Close returns as soon as the client has closed its sockets; PostgreSQL
// reaps the backend when it notices, which is not the same instant. Asserting
// instantaneously would make this test a measurement of scheduling. Waiting forever
// would make a genuine leak look like a hang, so the wait is bounded and the failure
// names the count it settled on.
func hcr1RequireBackendsSettle(t *testing.T, f *hcr1Fixture, baseline int, what string) {
	t.Helper()
	var last int
	for range 100 {
		last = hcr1BackendCount(t, f)
		if last <= baseline {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("%s left %d server backend(s) open above the %d this test itself holds", what, last-baseline, baseline)
}

func hcr1BackendCount(t *testing.T, f *hcr1Fixture) int {
	t.Helper()
	var n int
	if err := f.super.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pg_catalog.pg_stat_activity
WHERE datname = $1 AND usename IN ($2, $3)`, f.pg.Database, f.appRole, f.ownerRole).Scan(&n); err != nil {
		t.Fatalf("count server backends: %v", err)
	}
	return n
}

// TestMigrateApplyNeverOpensAnAdminCredential covers noAdmin and the poisoned
// configuration together.
//
// The AdminDSN here is deliberately unusable. If the schema phase opened it, the boot
// would fail while resolving its identity — so SUCCESS is the proof that it was never
// dialed. Both layers are exercised: ApplyMigrations, which blanks its own copy of the
// field, and openPrepared directly with the field still populated, which is the layer
// that has to survive a future caller forgetting to blank it.
func TestMigrateApplyNeverOpensAnAdminCredential(t *testing.T) {
	ctx := context.Background()
	const poison = "postgres://hcr1-nonexistent:wrong@127.0.0.1:1/nonexistent?sslmode=disable"

	t.Run("ApplyMigrations blanks it", func(t *testing.T) {
		f := hcr1Split(t, registerWidget)
		hcr1MakeRelationsFuture(t, f, widgetDescriptor.Table)
		cfg := f.cfg
		cfg.AdminDSN = poison
		if err := ApplyMigrations(ctx, cfg, registerWidget); err != nil {
			t.Fatalf("migrate apply touched the admin credential it must never use: %v", err)
		}
	})

	t.Run("the purpose gate holds it closed", func(t *testing.T) {
		f := hcr1Split(t, registerWidget)
		hcr1MakeRelationsFuture(t, f, widgetDescriptor.Table)
		cfg := f.cfg
		cfg.AdminDSN = poison
		// Straight to openPrepared with the field POPULATED: this is the seam that
		// enforces the property where the pool is actually opened.
		if _, err := openPrepared(ctx, cfg, registerWidget, prepareSchemaOnly, nil, publicationInputs{}); err != nil {
			t.Fatalf("the schema-only purpose opened a populated AdminDSN: %v", err)
		}
	})

	t.Run("and the ordinary purpose still validates it early", func(t *testing.T) {
		// The other side of the same coin, and the reason the gate is a PURPOSE and not a
		// deletion: `serve` must keep failing fast on an admin credential it cannot use,
		// BEFORE any migration. Losing that would trade one silent half-state for another.
		f := hcr1Split(t, registerWidget)
		cfg := f.cfg
		cfg.AdminDSN = poison
		before := hcr1StateSnapshot(t, f)
		if st, err := Open(ctx, cfg, registerWidget); err == nil {
			_ = st.Close()
			t.Fatal("serve accepted an unusable AdminDSN")
		}
		hcr1RequireUnchanged(t, f, before, "the refused serve with an unusable AdminDSN")
	})
}

// TestUpgradePreflightRefusesWhenTheOwnerCannotAlterARelation is the owner half, and the
// distinction it draws is the one an ACL check gets wrong.
//
// The relation is owned by a THIRD role. The configured owner is given ALL PRIVILEGES on
// it — so every ACL question about it answers yes — and it still may not ALTER it,
// because only the owner or an effective member of the owning role may. A preflight that
// accepted an ACL of ALL as authority would pass this estate and then fail partway
// through its own migration.
func TestUpgradePreflightRefusesWhenTheOwnerCannotAlterARelation(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)

	stranger := "hcr1_stranger_" + strings.TrimPrefix(f.pg.Database, "olv_")
	hcr1Exec(t, f.super, `CREATE ROLE `+quoteIdent(stranger)+` NOLOGIN NOSUPERUSER NOBYPASSRLS`)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = f.super.ExecContext(bg, `ALTER TABLE `+dialect.EngineSchema+`.orgs OWNER TO `+quoteIdent(f.ownerRole))
		_, _ = f.super.ExecContext(bg, `DROP OWNED BY `+quoteIdent(stranger))
		_, _ = f.super.ExecContext(bg, `DROP ROLE `+quoteIdent(stranger))
	})
	hcr1Exec(t, f.super, `ALTER TABLE `+dialect.EngineSchema+`.orgs OWNER TO `+quoteIdent(stranger))
	// ALL PRIVILEGES, deliberately: this is what separates "may write it" from "may
	// administer it".
	hcr1Exec(t, f.super, `GRANT ALL PRIVILEGES ON `+dialect.EngineSchema+`.orgs TO `+quoteIdent(f.ownerRole))
	hcr1Exec(t, f.super, `GRANT ALL PRIVILEGES ON `+dialect.EngineSchema+`.orgs TO `+quoteIdent(f.appRole))

	var authority bool
	if err := f.super.QueryRowContext(ctx,
		`SELECT pg_catalog.pg_has_role($1, c.relowner, 'USAGE')
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $2 AND c.relname = 'orgs'`, f.ownerRole, dialect.EngineSchema).Scan(&authority); err != nil {
		t.Fatalf("measure owner authority: %v", err)
	}
	if authority {
		t.Fatal("precondition: the configured owner is still an effective member of the owning role")
	}

	err := preflightPostgresUpgradePrivileges(ctx, f.owner, mustPostgresDialectHCR1(t),
		guardRoles{
			App:             guardRoleFact{Role: f.appRole, Known: true},
			Owner:           guardRoleFact{Role: f.ownerRole, Known: true},
			OwnerConfigured: true,
		}, coreOnlyRegistry(t), nil, prepareThroughReadiness, upgradePreflightConfig{})
	if err == nil {
		t.Fatal("the preflight accepted an estate whose owner cannot ALTER a relation the plan reconciles: an ACL of ALL is not authority")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("refusal does not carry the sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "orgs") || !strings.Contains(err.Error(), stranger) {
		t.Errorf("the refusal names neither the relation nor its actual owner: %v", err)
	}
}

func mustPostgresDialectHCR1(t *testing.T) dialect.Dialect {
	t.Helper()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	return dia
}

// TestUpgradePreflightProbeRefusesAWrongActingRole pins the subject of the probe.
//
// The probe measures what the app role will hold on relations THIS OWNER creates. Run by
// any other role it would answer about that role's future objects instead — a wrong
// answer that looks exactly like a right one, because both are booleans.
func TestUpgradePreflightProbeRefusesAWrongActingRole(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	before := hcr1StateSnapshot(t, f)

	err := upgradePreflightProbeFuture(ctx, f.owner, f.appRole, f.ownerRole+"_not_this_role",
		[]string{"SELECT"}, []string{leaderEpochTable})
	if err == nil {
		t.Fatal("the probe accepted a session running as a role other than the resolved owner")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("refusal does not carry the sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), f.ownerRole) {
		t.Errorf("the refusal does not name the role it expected: %v", err)
	}
	hcr1RequireUnchanged(t, f, before, "the probe refused for a wrong acting role")
	hcr1RequireNoProbeSurvived(t, f)
}

// hcr1PreRolledExecer hands out a transaction that has already been rolled back.
//
// It injects the ONE failure the probe must never swallow, using nothing but
// database/sql semantics — no production seam, no hook, no timing race. Every later
// operation on that transaction, INCLUDING the probe's own Rollback, returns
// sql.ErrTxDone, which is exactly the shape of "the rollback did not report success".
type hcr1PreRolledExecer struct{ *sql.DB }

func (e hcr1PreRolledExecer) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	tx, err := e.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if rerr := tx.Rollback(); rerr != nil {
		return nil, rerr
	}
	return tx, nil
}

// TestUpgradePreflightProbePropagatesARollbackFailure covers the difference between
// "nothing was created" and "something may have been".
//
// The probe creates a real relation. If its rollback does not complete, the preflight
// cannot state its central claim — and a check that returned the privilege verdict
// anyway would be asserting an unchanged database it never confirmed. The rollback
// failure must therefore become the preflight's own error, WITHOUT losing the cause
// that led to it.
func TestUpgradePreflightProbePropagatesARollbackFailure(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	before := hcr1StateSnapshot(t, f)

	err := upgradePreflightProbeFuture(ctx, hcr1PreRolledExecer{f.owner},
		f.appRole, f.ownerRole, []string{"SELECT"}, []string{leaderEpochTable})
	if err == nil {
		t.Fatal("the probe reported success although its transaction could not be rolled back")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("refusal does not carry the sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("the refusal does not say the rollback is what failed: %v", err)
	}
	// The ORIGINAL cause survives beside it. Replacing one with the other would leave an
	// operator with half the diagnosis.
	if !errors.Is(err, sql.ErrTxDone) {
		t.Errorf("the joined error lost the cause that led to the failed rollback: %v", err)
	}
	hcr1RequireUnchanged(t, f, before, "a probe whose rollback failed")
	hcr1RequireNoProbeSurvived(t, f)
}

// TestUpgradePreflightIsInertOnASingleRoleEstate is the ordinary-topology focal case:
// single role is the DEFAULT deployment, and HC-R1 must be invisible there.
func TestUpgradePreflightIsInertOnASingleRoleEstate(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPG(t)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("first open on the single-role topology: %v", err)
	}
	_ = st.Close()

	super := hcr1Open(t, pg.Superuser)
	// The app role owns the schema here, so there is no separate subject and nothing to
	// probe. Reopening must stay clean, and no probe may ever have run.
	st, err = Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("reopen on the single-role topology: %v", err)
	}
	_ = st.Close()

	var n int
	if err := super.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relname LIKE 'olivares\_fresh\_probe\_%'`, dialect.EngineSchema).Scan(&n); err != nil {
		t.Fatalf("look for probe relations: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d probe relation(s) exist on a single-role estate the preflight must not touch", n)
	}
}

// TestMaintenancePreparationStillReachesItsCallback is the maintenance focal case: the
// F2-A path must be unchanged by the purpose argument.
//
// It shares the whole preparation with `serve` — including this preflight — and it is
// the one caller whose callback runs after readiness work and before any Store is
// returned. A purpose that accidentally short-circuited it would report success while
// silently performing no maintenance at all, which is the failure mode a maintenance
// command can least afford.
func TestMaintenancePreparationStillReachesItsCallback(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	baseline := hcr1BackendCount(t, f)

	// The maintenance path has TWO preconditions of its own, and both belong to F2-A
	// rather than to this change. Meeting them is what puts the callback in reach, so
	// that REACHING IT is the thing this test measures.
	//
	//  - a complete directory inventory authority: on this fixture that is the AdminDSN
	//    (the alternative is the separately attested closed inventory routine);
	//  - the SYSTEM organization witness, which only a real bootstrap creates. Without
	//    it the epoch reconciliation refuses before the callback, exactly as it does for
	//    every other test of this path in the package.
	cfg := f.cfg
	cfg.AdminDSN = f.pg.Admin
	bootstrap, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("open to bootstrap the SYSTEM tenant: %v", err)
	}
	if err := bootstrap.System(ctx, func(sys store.SystemScope) error {
		_, serr := sys.EnsureSystemTenant(ctx)
		return serr
	}); err != nil {
		t.Fatalf("bootstrap the SYSTEM tenant: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close the bootstrap store: %v", err)
	}
	baseline = hcr1BackendCount(t, f)

	// The seam itself, with a callback whose invocation is the assertion. Going through
	// openPrepared rather than OpenDirectoryWriterMaintenance isolates the property under
	// test — "the callback is still reached" — from the activation's own preconditions.
	called := 0
	var sawEngine store.Engine
	st, err := openPrepared(ctx, cfg, registerWidget, prepareThroughReadiness, func(s *sqlStore) error {
		called++
		sawEngine = s.engine
		if s.db == nil || s.dia == nil || s.reg == nil {
			return errors.New("the maintenance callback received an unprepared store")
		}
		// The elector is deliberately NOT constructed yet on this path, which is what
		// makes maintenance different from serving.
		if s.elector != nil {
			return errors.New("the maintenance callback received a store with an elector")
		}
		return nil
	}, publicationInputs{})
	if err != nil {
		t.Fatalf("the maintenance preparation failed on a healthy split estate: %v", err)
	}
	if called != 1 {
		t.Fatalf("the maintenance callback ran %d time(s), want exactly 1", called)
	}
	if sawEngine != store.EnginePostgres {
		t.Fatalf("the maintenance callback saw engine %q", sawEngine)
	}
	// It never returns a Store, and it closes what it opened.
	if st != nil {
		t.Fatal("the maintenance preparation returned a Store: its callback path must hand nothing back")
	}
	hcr1RequireBackendsSettle(t, f, baseline, "the maintenance preparation")

	// And the real caller reaches its own callback too, reporting the estate's posture
	// rather than a preparation failure. What must not happen is a refusal from the
	// preflight or from anywhere else BEFORE the activation: this path shares the whole
	// preparation with `serve`, so a preflight that mis-handled it would take the
	// maintenance command down with it.
	beforeStatus, afterStatus, _, activationErr := OpenDirectoryWriterMaintenance(ctx, cfg, registerWidget, 1)
	if activationErr != nil && errors.Is(activationErr, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("the upgrade preflight refused the maintenance path on a healthy split estate: %v", activationErr)
	}
	if activationErr == nil && (beforeStatus.WriterPosture == "" || afterStatus.WriterPosture == "") {
		t.Fatalf("the activation returned no posture, so its callback did not run: before=%+v after=%+v", beforeStatus, afterStatus)
	}
	if activationErr != nil && !errors.Is(activationErr, store.ErrDirectoryUnavailable) && !errors.Is(activationErr, store.ErrConflict) {
		// Anything else means the preparation failed before the activation could speak.
		t.Fatalf("the maintenance path failed before its callback: %v", activationErr)
	}
	hcr1RequireBackendsSettle(t, f, baseline, "the directory writer maintenance path")
}

// TestMigrateApplyIsIdempotentAndLeavesTheEstateServable is the reassurance case: the
// phase is safe to re-run, and running it on an estate that needs nothing is a no-op
// rather than a refusal or a second application.
func TestMigrateApplyIsIdempotentAndLeavesTheEstateServable(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)

	if err := ApplyMigrations(ctx, f.cfg, registerWidget); err != nil {
		t.Fatalf("migrate apply on an already-complete estate: %v", err)
	}
	before := hcr1StateSnapshot(t, f)
	if err := ApplyMigrations(ctx, f.cfg, registerWidget); err != nil {
		t.Fatalf("second migrate apply: %v", err)
	}
	hcr1RequireUnchanged(t, f, before, "a repeated migrate apply")

	st, err := Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("serve refused an estate that migrate apply had just declared complete: %v", err)
	}
	_ = st.Close()
}

// TestLeaderEpochIsMaterializedByTheSchemaPhase pins the change that makes the ceremony
// completable at all.
//
// The fencing epoch used to be created by the elector — AFTER the point the schema phase
// returns. An operator who applied the schema, granted on everything that existed and
// then started the service would have found the elector creating a relation their grant
// never covered, so `serve` still needed future-object defaults: exactly what the manual
// route does not have. The relation must therefore exist when apply returns, and the
// elector must still be idempotent for every path that never runs the phase.
func TestLeaderEpochIsMaterializedByTheSchemaPhase(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	hcr1RevokeFutureDefaults(t, f)
	hcr1MakeRelationsFuture(t, f, "")

	if hcr1RelationExists(t, f, leaderEpochTable) {
		t.Fatal("precondition: the fencing epoch relation was not removed")
	}
	if err := ApplyMigrations(ctx, f.cfg, registerWidget); err != nil {
		t.Fatalf("migrate apply: %v", err)
	}
	if !hcr1RelationExists(t, f, leaderEpochTable) {
		t.Fatal("migrate apply returned without materializing the fencing epoch, so a manual grant cannot cover it and `serve` would create it afterwards")
	}
	if n := hcr1CountRows(t, f, leaderEpochTable); n != 0 {
		t.Errorf("the schema phase seeded %d fencing row(s): it must create the relation and take no leadership", n)
	}

	// The grant can now name it, which is the property the ceremony needs. It is scoped to
	// the fencing-epoch relation this case just materialized — the ONLY relation
	// hcr1MakeRelationsFuture(f, "") removed and ApplyMigrations re-created without an app
	// route. A blanket `... ON ALL TABLES` would also re-grant DELETE and table-wide UPDATE
	// to core v13's login_capability_observation, whose ACL is a closed contract
	// (SELECT+INSERT and column-scoped UPDATE only); the boot verifier correctly refuses that
	// drift, so widening the grant here would fail an unrelated relation's invariant. Naming
	// the relation directly is also the stronger proof of materialization: the GRANT would
	// error, not silently skip, if the schema phase had not created it.
	hcr1Exec(t, f.owner, `GRANT SELECT, INSERT, UPDATE, DELETE ON `+dialect.EngineSchema+`.`+quoteIdent(leaderEpochTable)+` TO `+quoteIdent(f.appRole))
	var oidBefore int64
	if err := f.super.QueryRowContext(ctx,
		`SELECT c.oid::int8 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relname=$2`, dialect.EngineSchema, leaderEpochTable).Scan(&oidBefore); err != nil {
		t.Fatalf("read the fencing epoch OID: %v", err)
	}

	st, err := Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("serve after apply+grant: %v", err)
	}
	_ = st.Close()

	// The elector reused the relation instead of replacing it: same OID, no new DDL.
	var oidAfter int64
	if err := f.super.QueryRowContext(ctx,
		`SELECT c.oid::int8 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relname=$2`, dialect.EngineSchema, leaderEpochTable).Scan(&oidAfter); err != nil {
		t.Fatalf("re-read the fencing epoch OID: %v", err)
	}
	if oidBefore != oidAfter {
		t.Fatalf("serve REPLACED the fencing epoch relation (OID %d -> %d): the grant the operator applied would not cover the new one", oidBefore, oidAfter)
	}
}

// TestUpgradePreflightUnionCoversRuntimeControlsAndModuleTables is the inventory case,
// measured against a live catalog rather than only against the builder.
//
// Three absent classes must feed the future union — a module descriptor table, the
// rollout state and its transition history — and the owner-only relations beside them
// must not. The rollout pair is the one most easily lost: it is created by
// classifyRolloutControls as the owner, and it is written at RUNTIME through the
// application pool by SetRolloutMode, so it needs application privileges that nothing in
// the schema grants.
func TestUpgradePreflightUnionCoversRuntimeControlsAndModuleTables(t *testing.T) {
	ctx := context.Background()
	// registerWidget declares NO rollout control, which is what makes removing the
	// rollout relations a faithful "not created yet" rather than a lost-state estate:
	// with a declared control, a surviving classification receipt correctly makes their
	// absence a refusal about LOST state, and that is a different contract.
	f := hcr1Split(t, registerWidget)
	hcr1RevokeFutureDefaults(t, f)
	hcr1MakeRelationsFuture(t, f, widgetDescriptor.Table)
	for _, relation := range []string{
		dialect.ControlRolloutStateTable,
		dialect.ControlRolloutTransitionTable,
	} {
		hcr1Exec(t, f.owner, `DROP TABLE `+dialect.EngineSchema+`.`+quoteIdent(relation))
	}

	before := hcr1StateSnapshot(t, f)
	st, err := Open(ctx, f.cfg, registerWidget)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open accepted an estate whose pending runtime-control relations the app role cannot use")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("refusal does not carry the sentinel: %v", err)
	}
	for _, relation := range []string{
		widgetDescriptor.Table, leaderEpochTable,
		dialect.ControlRolloutStateTable, dialect.ControlRolloutTransitionTable,
	} {
		if !strings.Contains(err.Error(), relation) {
			t.Errorf("the future union omits %q: %v", relation, err)
		}
	}
	hcr1RequireUnchanged(t, f, before, "the refused runtime-control upgrade")

	// And the same estate proceeds once the route exists, creating all four.
	hcr1GrantFutureDefaults(t, f, quoteIdent(f.appRole))
	st, err = Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("Open refused after the only change was the grant: %v", err)
	}
	_ = st.Close()
	for _, relation := range []string{
		widgetDescriptor.Table, leaderEpochTable,
		dialect.ControlRolloutStateTable, dialect.ControlRolloutTransitionTable,
	} {
		if !hcr1RelationExists(t, f, relation) {
			t.Errorf("the accepted upgrade did not create %q", relation)
		}
	}
}

// hcr1PendingGapRows counts audit_spool_gaps through the SUPERUSER, because that is the
// only role that can. The table carries FORCE ROW LEVEL SECURITY and its policy calls
// current_setting('app.tenant_id') without missing_ok, so neither the app nor the
// NOBYPASSRLS owner can take a global census of it — which is precisely why the preflight
// states the DELETE obligation as a capability instead of measuring the rows.
func hcr1PendingGapRows(t *testing.T, f *hcr1Fixture) int {
	t.Helper()
	var n int
	if err := f.super.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM `+dialect.EngineSchema+`.`+quoteIdent(dialect.AuditSpoolGapsTable)).Scan(&n); err != nil {
		t.Fatalf("count pending episodes: %v", err)
	}
	return n
}

// hcr1RevokeGapPrivileges takes named privileges away from the app on the episode table.
func hcr1RevokeGapPrivileges(t *testing.T, f *hcr1Fixture, privileges string) {
	t.Helper()
	hcr1Exec(t, f.owner, `REVOKE `+privileges+` ON `+dialect.EngineSchema+`.`+
		quoteIdent(dialect.AuditSpoolGapsTable)+` FROM `+quoteIdent(f.appRole))
}

func hcr1GrantGapPrivileges(t *testing.T, f *hcr1Fixture, privileges string) {
	t.Helper()
	hcr1Exec(t, f.owner, `GRANT `+privileges+` ON `+dialect.EngineSchema+`.`+
		quoteIdent(dialect.AuditSpoolGapsTable)+` TO `+quoteIdent(f.appRole))
}

// hcr1SeedPendingGap leaves one durable degrade episode behind and returns its tenant.
//
// Creating the episode needs a budget, and a budgeted PostgreSQL boot needs an AdminDSN:
// the spool recompute takes a cross-tenant SUM over the FORCE-RLS ledger, which the app
// role cannot do. That is FIXTURE work. Every boot under test afterwards runs with the
// budget off and NO AdminDSN, which is the posture the contract is about.
func hcr1SeedPendingGap(t *testing.T, f *hcr1Fixture) model.TenantID {
	t.Helper()
	ctx := context.Background()

	bootstrap := f.cfg
	bootstrap.AdminDSN = f.pg.Admin
	seed, err := Open(ctx, bootstrap, registerWidget)
	if err != nil {
		t.Fatalf("open to bootstrap the tenant: %v", err)
	}
	if err := seed.System(ctx, func(sys store.SystemScope) error {
		_, serr := sys.EnsureSystemTenant(ctx)
		return serr
	}); err != nil {
		t.Fatalf("bootstrap the SYSTEM tenant: %v", err)
	}
	tenant := provisionTenant(t, seed, "gap-recovery")
	if err := seed.Close(); err != nil {
		t.Fatalf("close the bootstrap store: %v", err)
	}

	degraded := bootstrap
	degraded.Clock = gapTestClock()
	degraded.SignEvent = fakeGapSigner
	degraded.AuditSpoolMaxBytes = 1
	degraded.AuditSpoolOnFull = store.AuditSpoolDegrade
	st, err := Open(ctx, degraded, registerWidget)
	if err != nil {
		t.Fatalf("open under budget+degrade: %v", err)
	}
	appendDroppedEvents(t, st, tenant, 2)
	if err := st.Close(); err != nil {
		t.Fatalf("close the degraded store: %v", err)
	}
	if n := hcr1PendingGapRows(t, f); n != 1 {
		t.Fatalf("fixture: %d pending episode(s) after budget+degrade, want exactly 1", n)
	}
	return tenant
}

// TestUpgradePreflightRefusesARestartThatCannotClearAPersistedEpisode is the causal case
// this correction exists for, and the first cut of the preflight got it wrong.
//
// An episode in audit_spool_gaps is DURABLE STATE. The configuration that created it —
// a positive budget plus the degrade policy — is not the configuration that has to
// consume it. auditLog.Append reads the pending episode before it looks at any budget and
// seals it outside the budget block, so the next boot, with the option switched off, is
// the one that must write the signed marker and DELETE the row.
//
// The earlier rule tied DELETE to the creating configuration. It would have accepted this
// application role, opened the service, and then failed 42501 on the first append that met
// the inherited state — the exact class of "boot says ready, runtime says 42501" failure
// HC-R1 exists to remove, just one table further along.
func TestUpgradePreflightRefusesARestartThatCannotClearAPersistedEpisode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adjust  func(cfg *store.Config)
		adminOK bool
	}{
		{
			// The primary posture: the operator turned the option off after the incident.
			// Budget zero needs no cross-tenant read, so this runs with NO AdminDSN.
			name:   "budget switched off",
			adjust: func(cfg *store.Config) {},
		},
		{
			// The other half of root's condition: the budget survives but the policy is
			// block. recordDrop is unreachable, yet an exempt or in-budget append still
			// seals what is already pending. A budgeted boot needs the AdminDSN for its
			// spool recompute, which is why this subcase carries one.
			name: "budget kept, policy block",
			adjust: func(cfg *store.Config) {
				cfg.AuditSpoolMaxBytes = 1 << 20
				cfg.AuditSpoolOnFull = store.AuditSpoolBlock
			},
			adminOK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := hcr1Split(t, registerWidget)
			tenant := hcr1SeedPendingGap(t, f)

			// The app keeps SELECT and loses everything that could write the table. Under
			// the corrected contract DELETE alone is what the refusal must be about.
			hcr1RevokeGapPrivileges(t, f, "INSERT, UPDATE, DELETE")

			cfg := f.cfg
			cfg.Clock = gapTestClock()
			cfg.SignEvent = fakeGapSigner
			tc.adjust(&cfg)
			if tc.adminOK {
				cfg.AdminDSN = f.pg.Admin
			}

			before := hcr1StateSnapshot(t, f)
			st, err := Open(ctx, cfg, registerWidget)
			if err == nil {
				_ = st.Close()
				t.Fatal("Open ACCEPTED a role that cannot clear an episode this estate is already holding: the service would open and then fail 42501 on the first append that meets it")
			}
			if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
				t.Fatalf("refusal does not carry the sentinel: %v", err)
			}
			if !strings.Contains(err.Error(), dialect.AuditSpoolGapsTable) || !strings.Contains(err.Error(), "DELETE") {
				t.Fatalf("the refusal names neither the episode table nor the missing privilege: %v", err)
			}
			hcr1RequireUnchanged(t, f, before, "the refused restart")
			if n := hcr1PendingGapRows(t, f); n != 1 {
				t.Fatalf("the refused restart changed the pending episode count to %d", n)
			}

			// THE SINGLE CAUSAL CHANGE: give back DELETE and nothing else. INSERT and
			// UPDATE stay revoked, which proves in the same step that the capability to
			// CREATE losses is neither restored nor required.
			hcr1GrantGapPrivileges(t, f, "DELETE")
			st, err = Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatalf("Open refused after the only change was granting DELETE: %v", err)
			}
			defer st.Close() //nolint:errcheck

			// And the recovery really happens: one ordinary unsigned append seals the
			// inherited episode as a signed marker and clears its row.
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
					Actor: "user:recover", ActorKind: model.ActorUser, Action: "agent.update",
				})
				return aerr
			}); err != nil {
				t.Fatalf("the append that must seal the inherited episode failed: %v", err)
			}
			if n := hcr1PendingGapRows(t, f); n != 0 {
				t.Fatalf("%d episode(s) still pending after the sealing append", n)
			}
		})
	}
}

// hcr1GapPreflight runs the preflight itself against this fixture's owner connection and
// returns its verdict, without opening a store.
//
// Calling the function directly is what makes the ABSENT half of the next test possible.
// Nothing in this schema re-creates audit_spool_gaps once it is gone — core v5 is its only
// creator and the engine rightly refuses an edited migration history — so a full Open
// after dropping it would either be refused for the wrong reason or leave a mutilated
// estate behind. The preflight is a pure reader plus a rolled-back probe, so asking it
// directly measures exactly the verdict under test and changes nothing.
func hcr1GapPreflight(t *testing.T, f *hcr1Fixture, cfg store.Config) error {
	t.Helper()
	return preflightPostgresUpgradePrivileges(context.Background(), f.owner,
		mustPostgresDialectHCR1(t),
		guardRoles{
			App:             guardRoleFact{Role: f.appRole, Known: true},
			Owner:           guardRoleFact{Role: f.ownerRole, Known: true},
			OwnerConfigured: true,
		}, coreOnlyRegistry(t), nil, prepareThroughReadiness, upgradePreflightConfigOf(cfg))
}

// TestUpgradePreflightGapObligationDoesNotDependOnTheRelationExisting isolates the
// stability property to its one variable: whether audit_spool_gaps exists.
//
// The obvious fix for the persisted-episode defect — demand SELECT+DELETE once the
// relation exists, but only SELECT while it is still pending — produces a contract that
// invalidates itself. The first boot accepts a posture, its own schema phase creates the
// table, and the next boot with the same binary, configuration, grants and data refuses.
// Nothing changed except that the relation the rule describes came into existence.
//
// So the same posture is put to the preflight twice, present and absent, with nothing else
// touched. Both readings must agree, in both directions.
func TestUpgradePreflightGapObligationDoesNotDependOnTheRelationExisting(t *testing.T) {
	f := hcr1Split(t, registerWidget)
	// Budget off, default policy, no AdminDSN: the posture in which the earlier contract
	// asked for nothing but SELECT here.
	cfg := f.cfg
	before := hcr1StateSnapshot(t, f)

	// (1) PRESENT, with DELETE held. Accept.
	if err := hcr1GapPreflight(t, f, cfg); err != nil {
		t.Fatalf("present + DELETE was refused: %v", err)
	}
	// (2) PRESENT, DELETE withdrawn from that one table. Refuse.
	hcr1RevokeGapPrivileges(t, f, "DELETE")
	err := hcr1GapPreflight(t, f, cfg)
	if err == nil {
		t.Fatal("present without DELETE was ACCEPTED: a persisted episode could never be cleared")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) ||
		!strings.Contains(err.Error(), dialect.AuditSpoolGapsTable) || !strings.Contains(err.Error(), "DELETE") {
		t.Fatalf("the refusal does not name this relation and privilege: %v", err)
	}
	hcr1GrantGapPrivileges(t, f, "DELETE")

	// (3) Now ABSENT. Only the future-object defaults can speak for it, so the existing
	// grants above stop mattering for this table and the probe decides.
	hcr1Exec(t, f.owner, `DROP TABLE `+dialect.EngineSchema+`.`+quoteIdent(dialect.AuditSpoolGapsTable))
	if hcr1RelationExists(t, f, dialect.AuditSpoolGapsTable) {
		t.Fatal("fixture: the episode table is still present")
	}
	// With the budget off audit_spool_usage contributes no requirement at all, and every
	// other relation in this estate exists — so DELETE can enter the future union only
	// through audit_spool_gaps. That is what makes this isolation and not a coincidence.
	if err := hcr1GapPreflight(t, f, cfg); err != nil {
		t.Fatalf("absent + future DELETE was refused, so the two readings disagree: %v", err)
	}
	// (4) ABSENT, future DELETE withdrawn. Refuse — the same verdict the present case
	// gives for the same missing capability.
	hcr1RevokeFutureDefaults(t, f)
	hcr1Exec(t, f.owner, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE ON TABLES TO `+quoteIdent(f.appRole))
	err = hcr1GapPreflight(t, f, cfg)
	if err == nil {
		t.Fatal("absent without future DELETE was ACCEPTED: the schema phase would create a table this role can never clear, and the next boot would refuse the posture this one allowed")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) ||
		!strings.Contains(err.Error(), dialect.AuditSpoolGapsTable) || !strings.Contains(err.Error(), "DELETE") {
		t.Fatalf("the refusal does not name the pending relation and privilege: %v", err)
	}
	// (5) And the same single change flips it back, proving DELETE is the whole cause.
	hcr1Exec(t, f.owner, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT DELETE ON TABLES TO `+quoteIdent(f.appRole))
	if err := hcr1GapPreflight(t, f, cfg); err != nil {
		t.Fatalf("absent was refused after the only change was granting future DELETE: %v", err)
	}

	// None of the five readings mutated anything. The estate differs from the opening
	// snapshot only by the fixture's own DDL, so it is re-taken and compared across the
	// last two readings rather than against the start.
	hcr1RequireNoProbeSurvived(t, f)
	after := hcr1StateSnapshot(t, f)
	if err := hcr1GapPreflight(t, f, cfg); err != nil {
		t.Fatalf("repeat reading: %v", err)
	}
	hcr1RequireUnchanged(t, f, after, "a preflight reading")
	_ = before
}

// TestUpgradePreflightFreshStartAndIdenticalRestartAgree is the same property observed the
// way an operator would meet it: a first boot on a database this binary has never
// migrated, then a restart that changes nothing at all.
//
// This is the sequence the contract has to survive. The first boot's own schema phase
// creates audit_spool_gaps among everything else; if the obligation for a pending relation
// differed from the obligation for an existing one, the second boot — same binary, same
// configuration, same grants, same data — would refuse what the first accepted.
//
// WHAT THIS TEST CANNOT DO, said plainly because a test that looks stronger than it is
// costs more than one that admits its range. On a database this binary has never migrated
// EVERY relation is pending, and most of them need DELETE, so this case cannot attribute
// the refusal in step 1 to audit_spool_gaps: it verifies the whole estate's sequence, not
// that one table's rule. Measured — under a mutant that keeps SELECT+DELETE on the
// existing relation and drops DELETE from the future union for exactly this table, this
// test still passes and
// TestUpgradePreflightGapObligationDoesNotDependOnTheRelationExisting fails. That one is
// where the isolation lives; this one is where the operator's sequence does.
func TestUpgradePreflightFreshStartAndIdenticalRestartAgree(t *testing.T) {
	ctx := context.Background()
	f := hcr1SplitUnopened(t)

	// A first boot with no future DELETE must be refused. On a database this binary has
	// never migrated, every relation is pending, so this is the whole-estate statement of
	// the same rule; the isolation to audit_spool_gaps is the previous test's job.
	hcr1RevokeFutureDefaults(t, f)
	hcr1Exec(t, f.owner, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE ON TABLES TO `+quoteIdent(f.appRole))
	st, err := Open(ctx, f.cfg, registerWidget)
	if err == nil {
		_ = st.Close()
		t.Fatal("the first boot of a fresh estate was accepted with no future DELETE")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("the first boot failed for another reason: %v", err)
	}
	if !strings.Contains(err.Error(), dialect.AuditSpoolGapsTable) {
		t.Errorf("the pending set does not name the episode table: %v", err)
	}
	if hcr1RelationExists(t, f, coreTrackingTable) {
		t.Fatal("the refused first boot created the core tracking table")
	}

	// The single causal change, and then the boot that builds the estate.
	hcr1Exec(t, f.owner, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT DELETE ON TABLES TO `+quoteIdent(f.appRole))
	st, err = Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("the first boot was refused after granting future DELETE: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hcr1RelationExists(t, f, dialect.AuditSpoolGapsTable) {
		t.Fatal("the first boot did not create the episode table, so the restart would prove nothing")
	}

	// THE RESTART. Nothing is changed between these two lines — not the configuration,
	// not a grant, not a row. Only the schema the previous boot itself materialized.
	st, err = Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("the IDENTICAL restart was refused after the first boot created the schema: the obligation changed with the relation's existence, which is the instability this contract must not have: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	hcr1RequireNoProbeSurvived(t, f)

	// And the capability that carries it is specific: take DELETE off that one relation
	// and the same restart refuses, before mutating anything.
	hcr1RevokeGapPrivileges(t, f, "DELETE")
	before := hcr1StateSnapshot(t, f)
	st, err = Open(ctx, f.cfg, registerWidget)
	if err == nil {
		_ = st.Close()
		t.Fatal("the restart was accepted after DELETE was withdrawn from the episode table")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) ||
		!strings.Contains(err.Error(), dialect.AuditSpoolGapsTable) || !strings.Contains(err.Error(), "DELETE") {
		t.Fatalf("the refusal does not name the relation and privilege: %v", err)
	}
	hcr1RequireUnchanged(t, f, before, "the refused restart")
}

// TestUpgradePreflightReportsAConditionalNeedOnlyWhenConfigured is the "no privilege for
// an inactive option" rule measured end to end, against a real revoked privilege.
//
// The degrade episode table is the sharpest case: the application role loses INSERT on
// it, and that must be irrelevant to a deployment running the default block policy and
// fatal to one that selected degrade with a budget. Same estate, same missing privilege,
// two verdicts decided by configuration alone.
func TestUpgradePreflightReportsAConditionalNeedOnlyWhenConfigured(t *testing.T) {
	ctx := context.Background()
	f := hcr1Split(t, registerWidget)
	hcr1Exec(t, f.owner, `REVOKE INSERT ON `+dialect.EngineSchema+`.`+quoteIdent(dialect.AuditSpoolGapsTable)+` FROM `+quoteIdent(f.appRole))

	// Default policy: the episode table is only ever READ, so the missing INSERT costs
	// this deployment nothing.
	st, err := Open(ctx, f.cfg, registerWidget)
	if err != nil {
		t.Fatalf("Open refused a default-policy deployment for a privilege it never uses: %v", err)
	}
	_ = st.Close()

	// Select the policy that writes episodes. Nothing about the DATABASE changed.
	degrading := f.cfg
	degrading.AuditSpoolMaxBytes = 1 << 20
	degrading.AuditSpoolOnFull = store.AuditSpoolDegrade
	// MaxConns stays above 1: the spool recompute needs a second connection, which is a
	// separate refusal and not the one under test.
	st, err = Open(ctx, degrading, registerWidget)
	if err == nil {
		_ = st.Close()
		t.Fatal("Open accepted a degrade-policy deployment whose app role cannot record a dropped-evidence episode")
	}
	if !errors.Is(err, store.ErrPostgresUpgradePrivilegePreflight) {
		t.Fatalf("refusal does not carry the sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), dialect.AuditSpoolGapsTable) || !strings.Contains(err.Error(), "INSERT") {
		t.Errorf("the refusal names neither the relation nor the privilege: %v", err)
	}
}
