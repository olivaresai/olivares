// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

const drControlRelation = "public." + dialect.DRRestoreControlTable

// TEST ONLY. A complete row installed by owned SQL proves a reader's verdict;
// it does not perform a restore, select signers, or authorize a COMPLETE ceremony.
func drCompleteReaderFixture(t *testing.T, pg pgtest.DSNs, cfg store.Config) (restoreControlRoles, drDestination, opgate.Keyset) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}); err != nil {
		t.Fatal(err)
	}
	k := drFactualKeyset()
	super := drOpenSuper(t, pg.Superuser)
	if _, err := super.ExecContext(ctx, `UPDATE `+drControlRelation+` SET state='complete',keyset_sha256=pg_catalog.decode($1,'hex')`, k.SHA256); err != nil {
		t.Fatal(err)
	}
	roles, err := resolveDRControlRoles(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	binding := drFixtureDestination(t, pg.Superuser, pg.Database)
	dest := drDestination{Database: binding.Database, Schema: binding.Schema, SystemIdentifier: binding.SystemIdentifier, SystemIdentifierKnown: true}
	gate := verifyDRRestoreControl(ctx, super, dest, roles)
	if gate.Verdict != drGateComplete {
		t.Fatalf("intact complete reader fixture: %s: %v", gate.Verdict, gate.Cause)
	}
	t.Logf("reader fixture: owner=%s/%d digest=%s; keyset contains three derived Ed25519 public identities; no restore ceremony", gate.Owner, roles.ownerOID, k.SHA256)
	return roles, dest, k
}
func drExec(t *testing.T, db *sql.DB, stmt string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatalf("fixture SQL %s: %v", stmt, err)
	}
}

// ── R63 C3: mutating the CLUSTER-GLOBAL application role, and giving it back ──
//
// THE DEFECT THIS CLOSES, measured on candidate 330a77b8b8. One fixture below
// promotes the application role to SUPERUSER to prove the split-closure refusal is
// not waived for a configured superuser. That role is cluster-global by design —
// `pgtest.Isolate` keeps it out of tempRoles precisely because every isolated
// database on the server shares it (see the pgtest package doc). The promotion was
// never undone, so every LATER provisioning in the same instance met the production
// refusal "role %q is a SUPERUSER; provisioning will not demote an administrative
// identity as application-role drift" and failed: 298 tests, in both a parallel and
// a fully serialized run.
//
// It is a FIXTURE defect exposed by composition, not a production one. The refusal
// is correct and stays: a real installation whose application identity is already
// administrative must be told so, and demoting it automatically is exactly the drift
// the refusal exists to name. Nothing here weakens provisioning, and nothing here is
// skipped.
//
// The ordering is the pgtest.LockSharedRole contract, and it is not decorative:
//
//   - the lock is taken AFTER provisioning, on a new connection, because taking it
//     inside Provision deadlocks against Provision's own hold;
//   - the RELEASE is registered FIRST and the RESTORE second, so LIFO puts the role
//     back BEFORE the lock is dropped — the only order in which no other package's
//     Provision can observe the promoted role;
//   - the restore is registered BEFORE the mutation, so a t.Fatal anywhere in the
//     test still returns the role.
//
// It restores ONLY what changed: cleanup re-reads the attributes and emits words for
// the ones that differ from what was observed. A shared role is a shared object, and
// blindly rewriting attributes this test never touched would be a second kind of
// drift.
type drRoleAttributes struct {
	superuser   bool
	bypassRLS   bool
	createRole  bool
	createDB    bool
	replication bool
	canLogin    bool
	inherit     bool
}

func (a drRoleAttributes) words(other drRoleAttributes) []string {
	type attr struct {
		mine, theirs bool
		on, off      string
	}
	var out []string
	for _, at := range []attr{
		{a.superuser, other.superuser, "SUPERUSER", "NOSUPERUSER"},
		{a.bypassRLS, other.bypassRLS, "BYPASSRLS", "NOBYPASSRLS"},
		{a.createRole, other.createRole, "CREATEROLE", "NOCREATEROLE"},
		{a.createDB, other.createDB, "CREATEDB", "NOCREATEDB"},
		{a.replication, other.replication, "REPLICATION", "NOREPLICATION"},
		{a.canLogin, other.canLogin, "LOGIN", "NOLOGIN"},
		{a.inherit, other.inherit, "INHERIT", "NOINHERIT"},
	} {
		if at.mine == at.theirs {
			continue
		}
		if at.mine {
			out = append(out, at.on)
		} else {
			out = append(out, at.off)
		}
	}
	return out
}

// drSharedInventoryOwner makes the directory-inventory owner available to a fixture
// WITHOUT assuming it is absent (R63 C3, second site).
//
// MEASURED, serialized, on the corrected candidate: the three inventory cases below
// failed with `role "olivares_directory_inventory_owner" already exists` (SQLSTATE
// 42710) on the FIRST of them. `drNewRole` creates and drops, which is right for the
// disposable `dr_*` roles — but this name is not disposable. It is
// `directoryInventoryOwner` from directoryinventory_postgres.go: production
// provisions it, it is cluster-global like the application role, and an earlier test
// in this same package legitimately leaves it in place.
//
// So the fixture ADOPTS it when it is there: the shared-role lock is taken, its exact
// attributes are restored by the same LIFO cleanup as the application role, and the
// cluster-global MEMBERSHIPS this case grants are revoked back to what was observed.
// Object grants inside the isolated database need no undo — the database is dropped.
// When the role is genuinely absent the disposable create/drop path is still correct.
func drSharedInventoryOwner(t *testing.T, db *sql.DB) {
	t.Helper()
	if !drRoleExists(t, db, directoryInventoryOwner) {
		drNewRole(t, db, directoryInventoryOwner)
		return
	}
	drHoldSharedRoleAttributes(t, db, directoryInventoryOwner)
	observed := drRoleMemberships(t, db, directoryInventoryOwner)
	t.Cleanup(func() {
		for granted := range drRoleMemberships(t, db, directoryInventoryOwner) {
			if _, had := observed[granted]; had {
				continue
			}
			drExec(t, db, `REVOKE `+quoteIdent(granted)+` FROM `+quoteIdent(directoryInventoryOwner))
		}
	})
}

func drRoleExists(t *testing.T, db *sql.DB, role string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
		t.Fatalf("probe for role %q: %v", role, err)
	}
	return exists
}

// drRoleMemberships is the set of roles this role is a MEMBER of. Membership lives in
// the cluster-global pg_auth_members, so it outlives the isolated database.
func drRoleMemberships(t *testing.T, db *sql.DB, role string) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT g.rolname FROM pg_catalog.pg_auth_members m
		   JOIN pg_catalog.pg_roles g ON g.oid=m.roleid
		   JOIN pg_catalog.pg_roles r ON r.oid=m.member
		  WHERE r.rolname=$1`, role)
	if err != nil {
		t.Fatalf("read memberships of %q: %v", role, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan membership of %q: %v", role, err)
		}
		out[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read memberships of %q: %v", role, err)
	}
	return out
}

func drReadRoleAttributes(t *testing.T, db *sql.DB, role string) drRoleAttributes {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var a drRoleAttributes
	err := db.QueryRowContext(ctx,
		`SELECT rolsuper,rolbypassrls,rolcreaterole,rolcreatedb,rolreplication,rolcanlogin,rolinherit
		   FROM pg_catalog.pg_roles WHERE rolname=$1`, role).
		Scan(&a.superuser, &a.bypassRLS, &a.createRole, &a.createDB, &a.replication, &a.canLogin, &a.inherit)
	if err != nil {
		t.Fatalf("read the attributes of shared role %q: %v", role, err)
	}
	return a
}

// drHoldSharedRoleAttributes serialises this test against every other package's
// provisioning and guarantees the exact observed attributes come back. Call it
// AFTER provisioning and BEFORE the mutation.
func drHoldSharedRoleAttributes(t *testing.T, super *sql.DB, role string) drRoleAttributes {
	t.Helper()
	// Registered FIRST → released LAST, after the restore below has run.
	t.Cleanup(pgtest.LockSharedRole(t))
	observed := drReadRoleAttributes(t, super, role)
	// Registered SECOND → runs FIRST, and it is registered before the mutation so a
	// fatal on any path still reaches it.
	t.Cleanup(func() {
		current := drReadRoleAttributes(t, super, role)
		words := observed.words(current)
		if len(words) == 0 {
			return
		}
		drExec(t, super, `ALTER ROLE `+quoteIdent(role)+` `+strings.Join(words, " "))
		if back := drReadRoleAttributes(t, super, role); back != observed {
			t.Fatalf("shared role %q was not restored: %+v, want %+v", role, back, observed)
		}
	})
	return observed
}
func drNewRole(t *testing.T, db *sql.DB, role string) {
	t.Helper()
	drExec(t, db, "CREATE ROLE "+quoteIdent(role)+" NOLOGIN")
	t.Cleanup(func() { drExec(t, db, "DROP OWNED BY "+quoteIdent(role)); drExec(t, db, "DROP ROLE "+quoteIdent(role)) })
}

// Canonical raw catalog/row snapshots prove refusal did not silently repair DDL
// or grants. This oracle is independent of the verifier's booleans and definitions.
func drRawControlSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	queries := []string{
		`SELECT pg_catalog.row_to_json(c)::text FROM pg_catalog.pg_class c WHERE c.oid='` + drControlRelation + `'::pg_catalog.regclass`,
		`SELECT pg_catalog.row_to_json(a)::text FROM pg_catalog.pg_attribute a WHERE a.attrelid='` + drControlRelation + `'::pg_catalog.regclass ORDER BY a.attnum`,
		`SELECT pg_catalog.row_to_json(c)::text FROM pg_catalog.pg_constraint c WHERE c.conrelid='` + drControlRelation + `'::pg_catalog.regclass ORDER BY c.oid`,
		`SELECT pg_catalog.row_to_json(i)::text FROM pg_catalog.pg_index i WHERE i.indrelid='` + drControlRelation + `'::pg_catalog.regclass ORDER BY i.indexrelid`,
		`SELECT pg_catalog.row_to_json(a)::text FROM pg_catalog.pg_auth_members a ORDER BY a.roleid,a.member,a.grantor`,
		`SELECT pg_catalog.row_to_json(r)::text FROM pg_catalog.pg_roles r WHERE r.oid>=16384 ORDER BY r.oid`,
		`SELECT pg_catalog.row_to_json(t)::text FROM pg_catalog.pg_type t WHERE t.typnamespace='public'::pg_catalog.regnamespace ORDER BY t.oid`,
		`SELECT pg_catalog.row_to_json(c)::text FROM pg_catalog.pg_class c WHERE c.relnamespace IN ('public'::pg_catalog.regnamespace,'pg_toast'::pg_catalog.regnamespace) AND c.oid>=16384 ORDER BY c.oid`,
		`SELECT pg_catalog.row_to_json(a)::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid WHERE c.relnamespace IN ('public'::pg_catalog.regnamespace,'pg_toast'::pg_catalog.regnamespace) AND c.oid>=16384 ORDER BY a.attrelid,a.attnum`,
		`SELECT pg_catalog.row_to_json(d)::text FROM pg_catalog.pg_description d WHERE d.objoid>=16384 ORDER BY d.classoid,d.objoid,d.objsubid`,
		`SELECT pg_catalog.row_to_json(c)::text FROM ONLY ` + drControlRelation + ` c ORDER BY c.control_key`,
	}
	var result []string
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			result = append(result, s)
		}
		if err := closeCoreDirectoryRows(rows); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}
func TestDRControlHealthyReadersAndFootprint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isolate func(testing.TB) pgtest.DSNs
	}{{"single", isolatedPG}, {"split", isolatedPGSplit}} {
		t.Run(tc.name, func(t *testing.T) {
			pg := tc.isolate(t)
			cfg := drPGConfig(pg)
			cfg.AdminDSN = pg.Admin
			cfg.MaxConns = 1
			roles, dest, k := drCompleteReaderFixture(t, pg, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			report, err := ReadPostgresRestoreControl(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if report.Owner != roles.owner || report.State != "complete" || report.KeysetSHA256 != k.SHA256 {
				t.Fatalf("wrong report: %+v", report)
			}
			// The actual early publication consumer must pass. There is no local witness,
			// so successful commitment validation cannot depend on one being supplied.
			fence, _, err := acquirePostgresPublicationFence(ctx, cfg, RestoreEnrolmentWitness{})
			if err != nil {
				t.Fatalf("healthy actual early consumer: %v", err)
			}
			fence.release()
			super := drOpenSuper(t, pg.Superuser)
			if got := drRelationNames(t, super); !reflect.DeepEqual(got, []string{dialect.DRRestoreControlTable}) {
				t.Fatalf("max0 gained product state: %v", got)
			}
			var ownerWrite, appWrite bool
			if err := super.QueryRowContext(ctx, `SELECT pg_catalog.has_table_privilege($1,$3,'UPDATE'),pg_catalog.has_table_privilege($2,$3,'UPDATE')`, roles.owner, roles.app, drControlRelation).Scan(&ownerWrite, &appWrite); err != nil {
				t.Fatal(err)
			}
			if !ownerWrite || appWrite != (roles.owner == roles.app) {
				t.Fatalf("owner inherent write=%t app write=%t", ownerWrite, appWrite)
			}
			t.Logf("actual destination=%+v owner retains inherent write/grant authority; app_write=%t", dest, appWrite)
		})
	}
}
func TestDRControlCatalogAndPrivilegeDriftRefusesWithoutRepair(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, restoreControlRoles)
		reason string
	}{
		{"missing_check", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_complete_keyset`)
			drExec(t, db, `UPDATE `+drControlRelation+` SET keyset_sha256=NULL`)
		}, "missing constraints"},
		{"weakened_check", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_revision, ADD CONSTRAINT olv_dr_restore_control_v1_revision CHECK (revision>=0)`)
		}, "constraint"},
		{"unvalidated_check", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_revision, ADD CONSTRAINT olv_dr_restore_control_v1_revision CHECK (revision>0) NOT VALID`)
		}, "constraint"},
		{"noinherit_check", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_revision, ADD CONSTRAINT olv_dr_restore_control_v1_revision CHECK (revision>0) NO INHERIT`)
		}, "constraint"},
		{"different_pk", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_pkey, ADD CONSTRAINT olv_dr_restore_control_v1_pkey PRIMARY KEY (op_id)`)
		}, "constraint"},
		{"deferred_pk", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_pkey, ADD CONSTRAINT olv_dr_restore_control_v1_pkey PRIMARY KEY (control_key) DEFERRABLE`)
		}, "relation identity"},
		{"index_options", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER INDEX public.olv_dr_restore_control_v1_pkey SET (fillfactor=50)`)
		}, "primary index"},
		{"extra_index", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `CREATE INDEX extra ON `+drControlRelation+`(op_id)`)
		}, "primary index"},
		{"index_column_statistics", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			// Owned catalog fault injection: PG does not expose a plain-index-column
			// statistics setter, but the verifier must close the catalog property.
			drExec(t, db, `UPDATE pg_catalog.pg_attribute SET attstattarget=30 WHERE attrelid='public.olv_dr_restore_control_v1_pkey'::regclass AND attnum=1`)
		}, "primary index"},
		{"toast_options", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` SET (toast.autovacuum_enabled=false)`)
		}, "TOAST"},
		{"toast_attribute_injected", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			// Explicit catalog fault on the automatic subordinate, not ordinary DDL.
			drExec(t, db, `UPDATE pg_catalog.pg_attribute SET attstattarget=30 WHERE attrelid=(SELECT reltoastrelid FROM pg_catalog.pg_class WHERE oid='public.olv_dr_restore_control_v1'::regclass) AND attnum=3`)
		}, "TOAST"},
		{"row_type_acl", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drNewRole(t, db, "dr_type_reader")
			drExec(t, db, `GRANT USAGE ON TYPE `+drControlRelation+` TO dr_type_reader`)
		}, "row/array"},
		{"array_type_name_injected", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			// PG refuses ALTER TYPE RENAME on an automatic array type. This is
			// explicitly an owned catalog fault, not an available ordinary DDL action.
			drExec(t, db, `UPDATE pg_catalog.pg_type SET typname='dr_other_array' WHERE oid=(SELECT typarray FROM pg_catalog.pg_type WHERE oid='public.olv_dr_restore_control_v1'::regtype)`)
		}, "row/array"},
		{"dependent_view", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `CREATE VIEW public.dr_extra_view AS SELECT * FROM `+drControlRelation)
		}, "dependent family"},
		{"index_comment", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `COMMENT ON INDEX public.olv_dr_restore_control_v1_pkey IS 'undeclared'`)
		}, "comments"},
		{"default", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` ALTER COLUMN revision SET DEFAULT 1`)
		}, "attribute"},
		{"foreign_type_identity", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `CREATE SCHEMA dr_type; CREATE DOMAIN dr_type.text AS pg_catalog.text; ALTER TABLE `+drControlRelation+` ALTER COLUMN destination_database TYPE dr_type.text`)
		}, "attribute"},
		{"typmod", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` ALTER COLUMN destination_database TYPE varchar(100)`)
		}, "attribute"},
		{"collation", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` ALTER COLUMN destination_database TYPE text COLLATE "POSIX"`)
		}, "attribute"},
		{"dropped_attribute", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` ADD COLUMN extra text; ALTER TABLE `+drControlRelation+` DROP COLUMN extra`)
		}, "attribute"},
		{"unlogged", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` SET UNLOGGED`)
		}, "relation identity"},
		{"inheritance", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `CREATE TABLE dr_child () INHERITS (`+drControlRelation+`)`)
		}, "relation identity"},
		{"rls", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `ALTER TABLE `+drControlRelation+` ENABLE ROW LEVEL SECURITY`)
		}, "relation identity"},
		{"rule", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `CREATE RULE dr_skip AS ON DELETE TO `+drControlRelation+` DO INSTEAD NOTHING`)
		}, "relation identity"},
		{"trigger", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `CREATE FUNCTION public.dr_fixture_trigger() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'; CREATE TRIGGER dr_extra BEFORE UPDATE ON `+drControlRelation+` FOR EACH ROW EXECUTE FUNCTION public.dr_fixture_trigger()`)
		}, "relation identity"},
		{"wrong_owner", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drNewRole(t, db, "dr_wrong_owner")
			drExec(t, db, `ALTER TABLE `+drControlRelation+` OWNER TO dr_wrong_owner`)
		}, "observed owner \"dr_wrong_owner\""},
		{"public_update", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `GRANT UPDATE ON `+drControlRelation+` TO PUBLIC`)
		}, "explicit ACL"},
		{"public_select", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `GRANT SELECT ON `+drControlRelation+` TO PUBLIC`)
		}, "explicit ACL"},
		{"extra_select", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drNewRole(t, db, "dr_extra_role")
			drExec(t, db, `GRANT SELECT ON `+drControlRelation+` TO dr_extra_role`)
		}, "explicit ACL"},
		{"grant_option", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `GRANT SELECT ON `+drControlRelation+` TO `+quoteIdent(r.app)+` WITH GRANT OPTION`)
		}, "explicit ACL"},
		{"column_write", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `GRANT UPDATE (state) ON `+drControlRelation+` TO `+quoteIdent(r.app))
			var effective bool
			if err := db.QueryRow(`SELECT pg_catalog.has_column_privilege($1,$2,'state','UPDATE')`, r.app, drControlRelation).Scan(&effective); err != nil || !effective {
				t.Fatalf("column privilege not actual: %t %v", effective, err)
			}
		}, "column ACL"},
		{"inherited_owner", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `GRANT `+quoteIdent(r.owner)+` TO `+quoteIdent(r.app)+` WITH INHERIT TRUE, SET FALSE`)
		}, "role-membership"},
		{"set_owner", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drExec(t, db, `GRANT `+quoteIdent(r.owner)+` TO `+quoteIdent(r.app)+` WITH INHERIT FALSE, SET TRUE`)
		}, "role-membership"},
		{"global_read_membership", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drNewRole(t, db, "dr_global_reader")
			drExec(t, db, `GRANT pg_read_all_data TO dr_global_reader WITH INHERIT TRUE`)
		}, "role-membership"},
		{"admin_intermediate", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drNewRole(t, db, "dr_mid")
			drExec(t, db, `GRANT `+quoteIdent(r.owner)+` TO dr_mid WITH SET TRUE; GRANT dr_mid TO `+quoteIdent(r.app)+` WITH SET FALSE, INHERIT FALSE, ADMIN TRUE`)
		}, "role-membership"},
		{"extra_inherited_select", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drNewRole(t, db, "dr_extra_role")
			drExec(t, db, `GRANT `+quoteIdent(r.app)+` TO dr_extra_role WITH INHERIT TRUE`)
		}, "role-membership"},
		{"inventory_inherited_select", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drSharedInventoryOwner(t, db)
			drExec(t, db, `GRANT `+quoteIdent(r.app)+` TO `+quoteIdent(directoryInventoryOwner)+` WITH INHERIT TRUE`)
		}, "role-membership"},
		{"inventory_column_select", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drSharedInventoryOwner(t, db)
			drExec(t, db, `GRANT SELECT (state) ON `+drControlRelation+` TO `+quoteIdent(directoryInventoryOwner))
		}, "column ACL"},
		{"inventory_superuser", func(t *testing.T, db *sql.DB, r restoreControlRoles) {
			drSharedInventoryOwner(t, db)
			drExec(t, db, `ALTER ROLE `+quoteIdent(directoryInventoryOwner)+` SUPERUSER`)
		}, "role-membership"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pg := isolatedPGSplit(t)
			cfg := drPGConfig(pg)
			cfg.MaxConns = 1
			roles, dest, _ := drCompleteReaderFixture(t, pg, cfg)
			super := drOpenSuper(t, pg.Superuser)
			tc.mutate(t, super, roles)
			before := drRawControlSnapshot(t, super)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			gate := verifyDRRestoreControl(ctx, super, dest, roles)
			if gate.Verdict != drGateMalformed || !strings.Contains(fmt.Sprint(gate.Cause), tc.reason) {
				t.Fatalf("wrong contract refusal: %s %v; expected %s", gate.Verdict, gate.Cause, tc.reason)
			}
			// Read/report, early real Open, existing installer and CAS all consume the
			// family refusal. No later custody/guard failure can satisfy this assertion.
			if _, err := ReadPostgresRestoreControl(ctx, cfg); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("report: %v", err)
			}
			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("actual Open: %v", err)
			}
			if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("installer repaired/adopted drift: %v", err)
			}
			if _, err := drTransitionControlForTest(ctx, cfg, PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}, restorePredecessor{revision: 1, state: opgate.StateComplete}, opgate.StateQuarantined); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("CAS accepted drift: %v", err)
			}
			after := drRawControlSnapshot(t, super)
			if before != after {
				t.Fatal("consumer refusal changed the planted catalog, ACL, membership or row")
			}
		})
	}
}
func TestDRControlNewDefaultsAreRemovedOnlyOnTheNewObject(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	cfg.AdminDSN = pg.Admin
	cfg.MaxConns = 1
	super := drOpenSuper(t, pg.Superuser)
	roles, err := resolveDRControlRoles(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	drNewRole(t, super, "dr_default_extra")
	drExec(t, super, `ALTER DEFAULT PRIVILEGES FOR ROLE `+quoteIdent(roles.owner)+` IN SCHEMA public GRANT SELECT,UPDATE ON TABLES TO PUBLIC,dr_default_extra`)
	// Deliberately planted fixture defaults, not production installer behavior.
	var before, after string
	q := `SELECT COALESCE(pg_catalog.jsonb_agg(pg_catalog.to_jsonb(d) ORDER BY d.oid)::text,'[]') FROM pg_catalog.pg_default_acl d`
	if err := super.QueryRow(q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	spec := PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}
	first, err := InstallPendingRestoreControl(ctx, cfg, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.State != "pending" {
		t.Fatalf("not pending: %+v", first)
	}
	raw := drRawControlSnapshot(t, super)
	second, err := InstallPendingRestoreControl(ctx, cfg, spec)
	if err != nil || second.Created {
		t.Fatalf("exact idempotence: %+v %v", second, err)
	}
	if raw != drRawControlSnapshot(t, super) {
		t.Fatal("idempotent installer mutated the control")
	}
	if err := super.QueryRow(q).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("installer edited database-wide defaults")
	}
	if got := drRelationNames(t, super); !reflect.DeepEqual(got, []string{dialect.DRRestoreControlTable}) {
		t.Fatal(got)
	}
	// Nonpending initial input is now unrepresentable. The focused public-surface
	// test checks the closed PendingRestoreSpec fields; this test still verifies
	// actual pending installation, exact idempotence, ACLs and no other footprint.

}

func TestDRControlRowValidationDoesNotTrustSQLConstraints(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	_, _, _ = drCompleteReaderFixture(t, pg, cfg)
	super := drOpenSuper(t, pg.Superuser)
	cases := []struct{ name, sql string }{
		{"complete_null_keyset", `UPDATE ` + drControlRelation + ` SET keyset_sha256=NULL`},
		{"complete_empty_keyset", `UPDATE ` + drControlRelation + ` SET keyset_sha256=''::bytea`},
		{"complete_short_keyset", `UPDATE ` + drControlRelation + ` SET keyset_sha256='abc'::bytea`},
		{"noncomplete_keyset", `UPDATE ` + drControlRelation + ` SET state='pending'`},
		{"unknown_state", `UPDATE ` + drControlRelation + ` SET state='finished'`},
		{"unknown_format", `UPDATE ` + drControlRelation + ` SET format=2`},
		{"zero_revision", `UPDATE ` + drControlRelation + ` SET revision=0`},
		{"short_op", `UPDATE ` + drControlRelation + ` SET op_id='abc'`},
		{"uppercase_op", `UPDATE ` + drControlRelation + ` SET op_id=upper(op_id)`},
		{"short_plan", `UPDATE ` + drControlRelation + ` SET plan_sha256='abc'::bytea`},
		{"missing_database", `UPDATE ` + drControlRelation + ` SET destination_database=''`},
		{"missing_schema", `UPDATE ` + drControlRelation + ` SET destination_schema=''`},
		{"missing_cluster", `UPDATE ` + drControlRelation + ` SET destination_system_identifier=''`},
		{"cluster_overflow", `UPDATE ` + drControlRelation + ` SET destination_system_identifier='99999999999999999999'`},
		{"short_report", `UPDATE ` + drControlRelation + ` SET report_sha256='a'::bytea`},
		{"infinite_time", `UPDATE ` + drControlRelation + ` SET observed_at='infinity'`},
		{"zero_rows", `DELETE FROM ` + drControlRelation},
		{"wrong_singleton", `UPDATE ` + drControlRelation + ` SET control_key='other'`},
		{"two_rows", `INSERT INTO ` + drControlRelation + ` SELECT * FROM ` + drControlRelation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := super.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			contract, err := dialect.DRRestoreControlContract(16)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range contract.Checks {
				if _, err := tx.ExecContext(ctx, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT `+quoteIdent(dialect.DRRestoreControlTable+"_"+c.Suffix)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_pkey`); err != nil {
				t.Fatal(err)
			}
			// First show the real row passes with all constraints removed. The following
			// refusal must come from independent value validation, not the shape verifier.
			if g := readDRRestoreControlRow(ctx, tx); g.Verdict != drGateComplete {
				t.Fatalf("unconstrained positive: %s %v", g.Verdict, g.Cause)
			}
			if _, err := tx.ExecContext(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			if g := readDRRestoreControlRow(ctx, tx); g.Verdict != drGateMalformed {
				t.Fatalf("row reader accepted damaged values without constraints: %s %v", g.Verdict, g.Cause)
			}
		})
	}
}

func TestDRControlInertMembershipAndDestinationBinding(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	roles, dest, _ := drCompleteReaderFixture(t, pg, cfg)
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	drExec(t, super, `GRANT `+quoteIdent(roles.owner)+` TO `+quoteIdent(roles.app)+` WITH SET FALSE, INHERIT FALSE, ADMIN FALSE`)
	if g := verifyDRRestoreControl(ctx, super, dest, roles); g.Verdict != drGateComplete {
		t.Fatalf("inert membership rejected: %s %v", g.Verdict, g.Cause)
	}
	for _, tc := range []struct {
		name   string
		change func(*drDestination)
	}{
		{"database", func(d *drDestination) { d.Database = "other" }},
		{"schema", func(d *drDestination) { d.Schema = "other" }},
		{"cluster", func(d *drDestination) { d.SystemIdentifier = "1" }},
		{"unknown_cluster", func(d *drDestination) { d.SystemIdentifierKnown = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := dest
			tc.change(&bad)
			if g := verifyDRRestoreControl(ctx, super, bad, roles); g.Verdict != drGateMalformed || g.OpID != "" {
				t.Fatalf("foreign/unreadable destination earned OP_ID: %+v", g)
			}
		})
	}
	a := opgate.PostgresDestination{Database: "a.b", Schema: "c", SystemIdentifier: "1"}
	b := opgate.PostgresDestination{Database: "a", Schema: "b.c", SystemIdentifier: "1"}
	if a == b {
		t.Fatal("dotted identifiers alias")
	}
	if (RestoreEnrolmentWitness{Enrolled: true, Destination: a}).namesDestination(drDestination{Database: b.Database, Schema: b.Schema, SystemIdentifier: b.SystemIdentifier, SystemIdentifierKnown: true}) {
		t.Fatal("ambiguous witness equality")
	}
}

func TestDRControlNewObjectRollsBackOnEffectivePrivilegeRefusal(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	super := drOpenSuper(t, pg.Superuser)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	roles, err := resolveDRControlRoles(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	drExec(t, super, `GRANT `+quoteIdent(roles.owner)+` TO `+quoteIdent(roles.app)+` WITH SET TRUE, INHERIT FALSE`)
	before := drRelationNames(t, super)
	_, err = InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA})
	if err == nil || !strings.Contains(err.Error(), "role-membership") {
		t.Fatalf("wrong transactional readback: %v", err)
	}
	if after := drRelationNames(t, super); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed new control escaped rollback: %v -> %v", before, after)
	}
}

func TestDRControlRoleMeasurementCannotHideSessionAuthority(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	super := drOpenSuper(t, pg.Superuser)
	conn, err := super.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	roles, err := resolveDRControlRoles(ctx, drPGConfig(pg))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `SET ROLE `+quoteIdent(roles.app)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = readDRControlRole(ctx, conn); err == nil || !strings.Contains(err.Error(), "session authority") {
		t.Fatalf("SET ROLE hid reset capability: %v", err)
	}
}

func TestDRControlConfiguredSuperuserIsNotExemptFromSplitClosure(t *testing.T) {
	pg := isolatedPGSplit(t)
	cfg := drPGConfig(pg)
	roles, dest, _ := drCompleteReaderFixture(t, pg, cfg)
	super := drOpenSuper(t, pg.Superuser)
	// R63 C3. The promotion below is the test's PREMISE and stays live through every
	// refusal assertion; it is the cleanup that is new. Held under the shared-role
	// lock and restored by a cleanup registered before the ALTER, so no later
	// provisioning in this instance ever sees an administrative application role.
	observed := drHoldSharedRoleAttributes(t, super, roles.app)
	if observed.superuser {
		t.Fatalf("premise already spent: shared role %q was ALREADY a superuser before this test promoted it", roles.app)
	}
	drExec(t, super, `ALTER ROLE `+quoteIdent(roles.app)+` SUPERUSER`)
	if !drReadRoleAttributes(t, super, roles.app).superuser {
		t.Fatalf("the negative premise did not take: %q is not a superuser", roles.app)
	}
	before := drRawControlSnapshot(t, super)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gate := verifyDRRestoreControl(ctx, super, dest, roles)
	if gate.Verdict != drGateMalformed || !strings.Contains(fmt.Sprint(gate.Cause), "role-membership") {
		t.Fatalf("configured superuser escaped verifier: %v", gate.Cause)
	}
	fence, _, err := acquirePostgresPublicationFence(ctx, cfg, RestoreEnrolmentWitness{})
	if fence != nil {
		fence.release()
	}
	if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), "role-membership") {
		t.Fatalf("actual DR consumer did not refuse split superuser: %v", err)
	}
	// Ordinary Open already refuses a superuser before reaching the DR reader.
	// Preserve that refusal and identify its actual cause; do not weaken the role
	// gate to turn this into evidence of a later control decision.
	st, err := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "is a SUPERUSER") || errors.Is(err, ErrRestorePublicationFenced) {
		t.Fatalf("expected earlier ordinary role refusal: %v", err)
	}
	if after := drRawControlSnapshot(t, super); after != before {
		t.Fatal("refusal changed superuser drift")
	}
}

func TestDRControlCompiledMajorSupportIsExplicit(t *testing.T) {
	for _, major := range []int{0, 14, 15, 16, 17, 18} {
		t.Run(fmt.Sprint(major), func(t *testing.T) {
			c, err := dialect.DRRestoreControlContract(major)
			if major == 16 {
				if err != nil || c.Major != major || c.DDL() == "" {
					t.Fatalf("compiled contract: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("unsupported major accepted: %v", err)
			}
		})
	}
}

func TestDRControlBusyAttributionRequiresAVerifiedBoundFamily(t *testing.T) {
	for _, name := range []string{"intact_single", "intact_split", "foreign_destination", "wrong_owner", "public_update", "complete_null"} {
		t.Run(name, func(t *testing.T) {
			var pg pgtest.DSNs
			if name == "intact_single" {
				pg = isolatedPG(t)
			} else {
				pg = isolatedPGSplit(t)
			}
			cfg := drPGConfig(pg)
			cfg.MaxConns = 1
			_, dest, _ := drCompleteReaderFixture(t, pg, cfg)
			super := drOpenSuper(t, pg.Superuser)
			switch name {
			case "foreign_destination":
				drExec(t, super, `UPDATE `+drControlRelation+` SET destination_database='elsewhere'`)
			case "wrong_owner":
				drNewRole(t, super, "dr_busy_wrong_owner")
				drExec(t, super, `ALTER TABLE `+drControlRelation+` OWNER TO dr_busy_wrong_owner`)
			case "public_update":
				drExec(t, super, `GRANT UPDATE ON `+drControlRelation+` TO PUBLIC`)
			case "complete_null":
				drExec(t, super, `ALTER TABLE `+drControlRelation+` DROP CONSTRAINT olv_dr_restore_control_v1_complete_keyset; UPDATE `+drControlRelation+` SET keyset_sha256=NULL`)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := super.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := forceDiscard(conn); err != nil {
					t.Error(err)
				}
			}()
			var held bool
			var pid int
			if err := conn.QueryRowContext(ctx, `SELECT pg_catalog.pg_try_advisory_lock(pg_catalog.hashtextextended($1,0)),pg_catalog.pg_backend_pid()`, drRestoreLockName+":"+dest.Database+":"+dest.Schema).Scan(&held, &pid); err != nil || !held {
				t.Fatalf("exclusive fixture: %t %v", held, err)
			}
			before := drRawControlSnapshot(t, super)
			coord, err := openDRCoordinationShared(ctx, cfg)
			if coord != nil {
				_ = coord.close()
				t.Fatal("a shared lock escaped the actual exclusive holder")
			}
			if !errors.Is(err, ErrRestorePublicationBusy) {
				t.Fatalf("one-try busy: %v", err)
			}
			wantID := strings.HasPrefix(name, "intact_")
			if strings.Contains(err.Error(), drTestOpA) != wantID {
				t.Fatalf("operation attribution=%t: %v", wantID, err)
			}
			if !wantID && !strings.Contains(err.Error(), "no operation identity") {
				t.Fatalf("no explicit attribution refusal: %v", err)
			}
			if after := drRawControlSnapshot(t, super); before != after {
				t.Fatal("busy diagnostic mutated the control")
			}
			t.Logf("actual exclusive backend=%d; bounded busy attribution=%t", pid, wantID)
		})
	}
}
