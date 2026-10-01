// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func drMeasuredMajor(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT pg_catalog.current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("DR_MAJOR_MEASURED|server_version_num=%d", version)
	return version / 10000
}

func TestDRControlMajorsPreserveClosedCatalogAndPrivileges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isolate func(testing.TB) pgtest.DSNs
	}{{"single", isolatedPG}, {"split", isolatedPGSplit}} {
		t.Run(tc.name, func(t *testing.T) {
			pg := tc.isolate(t)
			cfg := drPGConfig(pg)
			cfg.AdminDSN, cfg.MaxConns = pg.Admin, 1
			super := drOpenSuper(t, pg.Superuser)
			drMeasuredMajor(t, super)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			spec := PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}
			first, err := InstallPendingRestoreControl(ctx, cfg, spec)
			if err != nil || !first.Present || !first.Created || first.State != opgate.StatePending || first.Revision != 1 {
				t.Fatalf("native installation/readback: %+v %v", first, err)
			}
			if first.Database != pg.Database || first.Schema != dialect.EngineSchema || first.SystemIdentifier == "" || first.OpID != spec.OpID || first.PlanSHA256 != spec.PlanSHA256 {
				t.Fatalf("wrong native destination/operation binding: %+v", first)
			}
			if names := drRelationNames(t, super); !reflect.DeepEqual(names, []string{dialect.DRRestoreControlTable}) {
				t.Fatalf("installer created payload, tracking or directory state: %v", names)
			}
			before := drRawControlSnapshot(t, super)
			again, err := InstallPendingRestoreControl(ctx, cfg, spec)
			if err != nil || again.Created || again.OpID != first.OpID || again.Revision != first.Revision {
				t.Fatalf("idempotent install changed binding: %+v %v", again, err)
			}
			read, err := ReadPostgresRestoreControl(ctx, cfg)
			if err != nil || read.OpID != first.OpID || read.State != first.State {
				t.Fatalf("existing-family native readback: %+v %v", read, err)
			}
			if _, err := InstallPendingRestoreControl(ctx, cfg, PendingRestoreSpec{OpID: drTestOpB, PlanSHA256: drTestPlanA}); err == nil {
				t.Fatal("a different operation adopted the existing control")
			}
			if after := drRawControlSnapshot(t, super); before != after {
				t.Fatal("idempotent readback/refusal repaired or changed the existing control")
			}
		})
	}
}

// All real consumers must refuse the planted drift without modifying it. This
// fixture supplies a complete row, not a restore or a custody ceremony.
func drMajorRefusalWithoutRepair(t *testing.T, pg pgtest.DSNs, cfg store.Config, roles restoreControlRoles, dest drDestination, unreadable bool) {
	t.Helper()
	super := drOpenSuper(t, pg.Superuser)
	before := drRawControlSnapshot(t, super)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	g := verifyDRRestoreControl(ctx, super, dest, roles)
	want := drGateMalformed
	if unreadable {
		// An explicit PK rename also renames its index. The existing compiled
		// scalar index lookup becomes NULL, so the closed reader returns unreadable.
		// Preserve that refusal rather than changing product error classification.
		want = drGateUnreadable
	}
	if g.Verdict != want || g.Cause == nil || (!unreadable && !errors.Is(g.Cause, errDRControlShape)) {
		t.Fatalf("catalog/ACL drift escaped verifier: %s %v", g.Verdict, g.Cause)
	}
	cause := g.Cause.Error()
	if _, err := ReadPostgresRestoreControl(ctx, cfg); err == nil || !strings.Contains(err.Error(), cause) {
		t.Fatalf("report accepted drift or failed elsewhere: %v", err)
	}
	st, err := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	if !errors.Is(err, ErrRestorePublicationFenced) || !strings.Contains(err.Error(), cause) {
		t.Fatalf("actual Open admitted drift or refused elsewhere: %v", err)
	}
	spec := PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA}
	if _, err := InstallPendingRestoreControl(ctx, cfg, spec); err == nil || !errors.Is(err, ErrRestoreControlConflict) || !strings.Contains(err.Error(), cause) {
		t.Fatalf("existing installer accepted drift or refused elsewhere: %v", err)
	}
	if _, err := drTransitionControlForTest(ctx, cfg, spec, restorePredecessor{revision: 1, state: opgate.StateComplete}, opgate.StateQuarantined); err == nil || !errors.Is(err, ErrRestoreControlConflict) || !strings.Contains(err.Error(), cause) {
		t.Fatalf("CAS accepted drift or refused elsewhere: %v", err)
	}
	if after := drRawControlSnapshot(t, super); before != after {
		t.Fatal("refusal changed the planted catalog, authority or row")
	}
}

func TestDRControlMajorsCatalogIdentityAndDrift(t *testing.T) {
	probe := isolatedPGSplit(t)
	major := drMeasuredMajor(t, drOpenSuper(t, probe.Superuser))
	cases := []string{"column_statistics", "missing_not_null", "explicit_pk_name"}
	if major >= 17 {
		cases = append(cases, "legacy_statistics_default")
	}
	if major == 18 {
		cases = append(cases, "renamed_not_null", "duplicate_not_null_identity", "wrong_not_null_column", "unvalidated_not_null", "unenforced_constraint", "period_constraint")
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			pg := isolatedPGSplit(t)
			cfg := drPGConfig(pg)
			cfg.MaxConns = 1
			roles, dest, _ := drCompleteReaderFixture(t, pg, cfg)
			super := drOpenSuper(t, pg.Superuser)
			switch name {
			case "column_statistics":
				drExec(t, super, `ALTER TABLE `+drControlRelation+` ALTER COLUMN op_id SET STATISTICS 30`)
			case "missing_not_null":
				drExec(t, super, `ALTER TABLE `+drControlRelation+` ALTER COLUMN op_id DROP NOT NULL`)
			case "explicit_pk_name":
				drExec(t, super, `ALTER TABLE `+drControlRelation+` RENAME CONSTRAINT olv_dr_restore_control_v1_pkey TO renamed_primary_key`)
			case "legacy_statistics_default":
				drExec(t, super, `UPDATE pg_catalog.pg_attribute SET attstattarget=-1 WHERE attrelid='`+drControlRelation+`'::regclass AND attname='op_id'`)
			case "renamed_not_null":
				if count := renamePostgresNotNullConstraints(t, super, dialect.DRRestoreControlTable); count != 10 {
					t.Fatalf("expected ten actual NOT NULL names, got %d", count)
				}
				before := drRawControlSnapshot(t, super)
				if g := verifyDRRestoreControl(context.Background(), super, dest, roles); g.Verdict != drGateComplete {
					t.Fatalf("generated-name-only drift refused: %s %v", g.Verdict, g.Cause)
				}
				if _, err := ReadPostgresRestoreControl(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				if after := drRawControlSnapshot(t, super); before != after {
					t.Fatal("name-only admission repaired the catalog")
				}
				return
			case "duplicate_not_null_identity":
				drExec(t, super, `UPDATE pg_catalog.pg_constraint SET conkey=ARRAY[3]::smallint[] WHERE conrelid='`+drControlRelation+`'::regclass AND contype='n' AND conkey=ARRAY[2]::smallint[]`)
			case "wrong_not_null_column":
				drExec(t, super, `UPDATE pg_catalog.pg_constraint SET conkey=ARRAY[10]::smallint[] WHERE conrelid='`+drControlRelation+`'::regclass AND contype='n' AND conkey=ARRAY[2]::smallint[]`)
			case "unvalidated_not_null":
				drExec(t, super, `UPDATE pg_catalog.pg_constraint SET convalidated=false WHERE conrelid='`+drControlRelation+`'::regclass AND contype='n' AND conkey=ARRAY[2]::smallint[]`)
			case "unenforced_constraint":
				// PG18 rejects altering this family's enforceability through DDL;
				// inject the catalog bit explicitly on an owned NOT NULL constraint.
				drExec(t, super, `UPDATE pg_catalog.pg_constraint SET conenforced=false WHERE conrelid='`+drControlRelation+`'::regclass AND contype='n' AND conkey=ARRAY[2]::smallint[]`)
			case "period_constraint":
				// Catalog fault injection: PERIOD is not legal on this CHECK/PK family.
				drExec(t, super, `UPDATE pg_catalog.pg_constraint SET conperiod=true WHERE conrelid='`+drControlRelation+`'::regclass AND conname='olv_dr_restore_control_v1_pkey'`)
			}
			drMajorRefusalWithoutRepair(t, pg, cfg, roles, dest, name == "explicit_pk_name")
		})
	}
}

func drMaintainMembership(t *testing.T, super *sql.DB, member, options string) {
	t.Helper()
	if member == dialect.DefaultAppRole {
		t.Cleanup(pgtest.LockSharedRole(t))
	}
	if _, exists := drRoleMemberships(t, super, member)["pg_maintain"]; exists {
		t.Fatal("maintenance membership premise already spent")
	}
	t.Cleanup(func() { drExec(t, super, `REVOKE pg_maintain FROM `+quoteIdent(member)) })
	drExec(t, super, `GRANT pg_maintain TO `+quoteIdent(member)+options)
}

func TestDRControlMajorsMaintainClosure(t *testing.T) {
	probe := isolatedPGSplit(t)
	major := drMeasuredMajor(t, drOpenSuper(t, probe.Superuser))
	if major < 17 {
		t.Log("MAINTAIN does not exist on this major; native owner/readback is covered by the installation test")
		return
	}
	for _, name := range []string{"app_direct", "admin_direct", "unexpected_direct", "public_direct", "grant_option", "app_global", "app_global_set_only", "app_global_admin_only", "admin_global", "unexpected_global", "inventory_global", "inert_global"} {
		t.Run(name, func(t *testing.T) {
			pg := isolatedPGSplit(t)
			cfg := drPGConfig(pg)
			cfg.AdminDSN, cfg.MaxConns = pg.Admin, 1
			roles, dest, _ := drCompleteReaderFixture(t, pg, cfg)
			super := drOpenSuper(t, pg.Superuser)
			switch name {
			case "app_direct", "grant_option":
				grant := `GRANT MAINTAIN ON ` + drControlRelation + ` TO ` + quoteIdent(roles.app)
				if name == "grant_option" {
					grant += ` WITH GRANT OPTION`
				}
				drExec(t, super, grant)
			case "admin_direct":
				drExec(t, super, `GRANT MAINTAIN ON `+drControlRelation+` TO `+quoteIdent(roles.admin))
			case "unexpected_direct":
				drNewRole(t, super, "dr_maintainer")
				drExec(t, super, `GRANT MAINTAIN ON `+drControlRelation+` TO dr_maintainer`)
			case "public_direct":
				drExec(t, super, `GRANT MAINTAIN ON `+drControlRelation+` TO PUBLIC`)
			case "app_global":
				drMaintainMembership(t, super, roles.app, "")
			case "app_global_set_only":
				drMaintainMembership(t, super, roles.app, ` WITH INHERIT FALSE, SET TRUE, ADMIN FALSE`)
			case "app_global_admin_only":
				drMaintainMembership(t, super, roles.app, ` WITH INHERIT FALSE, SET FALSE, ADMIN TRUE`)
			case "admin_global":
				drMaintainMembership(t, super, roles.admin, "")
			case "unexpected_global":
				drNewRole(t, super, "dr_maintainer")
				drMaintainMembership(t, super, "dr_maintainer", "")
			case "inventory_global":
				drSharedInventoryOwner(t, super)
				drMaintainMembership(t, super, directoryInventoryOwner, "")
			case "inert_global":
				drMaintainMembership(t, super, roles.app, ` WITH INHERIT FALSE, SET FALSE, ADMIN FALSE`)
				before := drRawControlSnapshot(t, super)
				if g := verifyDRRestoreControl(context.Background(), super, dest, roles); g.Verdict != drGateComplete {
					t.Fatalf("inert maintenance edge refused: %s %v", g.Verdict, g.Cause)
				}
				if before != drRawControlSnapshot(t, super) {
					t.Fatal("inert-edge admission changed the control")
				}
				return
			}
			drMajorRefusalWithoutRepair(t, pg, cfg, roles, dest, false)
		})
	}
}

func TestDRControlMajorsMaintainRefusalRollsBackNewControl(t *testing.T) {
	pg := isolatedPGSplit(t)
	super := drOpenSuper(t, pg.Superuser)
	if drMeasuredMajor(t, super) < 17 {
		return
	}
	cfg := drPGConfig(pg)
	roles, err := resolveDRControlRoles(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	drMaintainMembership(t, super, roles.app, "")
	before := drRelationNames(t, super)
	_, err = InstallPendingRestoreControl(context.Background(), cfg, PendingRestoreSpec{OpID: drTestOpA, PlanSHA256: drTestPlanA})
	if err == nil || !strings.Contains(err.Error(), "role-membership") {
		t.Fatalf("new control accepted maintenance authority or failed elsewhere: %v", err)
	}
	if after := drRelationNames(t, super); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed new control escaped rollback: %v -> %v", before, after)
	}
}
