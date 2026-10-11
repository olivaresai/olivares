// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

func TestFinOpsCustodyVerifierPostgresRefusesGuardFunctionAdministration(t *testing.T) {
	for _, reachable := range []bool{false, true} {
		t.Run(map[bool]string{false: "app_owner", true: "reachable_owner"}[reachable], func(t *testing.T) {
			ctx := context.Background()
			dsns := isolatedPGSplit(t)
			owner := openCustodyPGPool(t, dsns.Owner)
			app := openCustodyPGPool(t, dsns.App)
			super := openCustodyPGPool(t, dsns.Superuser)
			dia, _ := dialect.New(store.EnginePostgres)
			roles := guardRoles{App: guardRoleFact{Known: true, Role: currentCustodyRole(t, app)}, Owner: guardRoleFact{Known: true, Role: currentCustodyRole(t, owner)}, OwnerConfigured: true}
			m := coreFinOpsCustodyControlMigration(dia, roles)
			m.After = nil
			if err := migrate.Apply(ctx, owner, dia, finOpsCustodyTestTrackingTable, []migrate.Migration{m}); err != nil {
				t.Fatal(err)
			}
			if err := verifyFinOpsCustodyControlOwnerACL(ctx, owner, dia, roles); err != nil {
				t.Fatalf("valid custody ACL refused: %v", err)
			}
			function := "public." + dialect.PostgresCustodyGuardFunction + "()"
			newOwner := roles.App.bindable()
			if reachable {
				newOwner = dsns.Database + "_guard_delegate"
				if _, err := super.ExecContext(ctx, "CREATE ROLE "+quoteIdent(newOwner)); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = super.ExecContext(ctx, "ALTER FUNCTION "+function+" OWNER TO "+quoteIdent(roles.Owner.bindable()))
					_, _ = super.ExecContext(ctx, "REVOKE "+quoteIdent(newOwner)+" FROM "+quoteIdent(roles.App.bindable()))
					if _, err := super.ExecContext(ctx, "DROP ROLE "+quoteIdent(newOwner)); err != nil {
						t.Errorf("remove owned guard delegate: %v", err)
					}
				})
				if _, err := super.ExecContext(ctx, "GRANT "+quoteIdent(newOwner)+" TO "+quoteIdent(roles.App.bindable())); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := super.ExecContext(ctx, "ALTER FUNCTION "+function+" OWNER TO "+quoteIdent(newOwner)); err != nil {
				t.Fatal(err)
			}
			before := finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres)
			err := verifyFinOpsCustodyControlOwnerACL(ctx, owner, dia, roles)
			if !errors.Is(err, store.ErrAppendOnlyACLOpen) {
				t.Errorf("app/reachable function owner admitted: %v", err)
			} else if !strings.Contains(err.Error(), fmt.Sprintf("%q", newOwner)) {
				t.Errorf("function-administration refusal does not name owner %q: %v", newOwner, err)
			}
			if after := finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres); after != before {
				t.Fatal("function-administration refusal repaired the durable catalog")
			}
			// Establish the authority behind the refusal on the disposable fixture.
			conn, err := app.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if reachable {
				if _, err := conn.ExecContext(ctx, "SET ROLE "+quoteIdent(newOwner)); err != nil {
					t.Fatal(err)
				}
				defer conn.ExecContext(ctx, "RESET ROLE") //nolint:errcheck // isolated fixture cleanup
			}
			if _, err := conn.ExecContext(ctx, "DROP FUNCTION "+function+" CASCADE"); err != nil {
				t.Fatalf("fixture did not expose function-owner administration: %v", err)
			}
		})
	}
}

func TestFinOpsCustodyVerifierPostgresCanonicalReference(t *testing.T) {
	ctx := context.Background()
	owner, _, dia, roles := custodyPG(t)
	// Isolate the existing constructor from registration's After hook so this
	// comparison proves exactly which canonical projection differs on this server.
	m := coreFinOpsCustodyControlMigration(dia, roles)
	m.After = nil
	if err := migrate.Apply(ctx, owner, dia, finOpsCustodyTestTrackingTable, []migrate.Migration{m}); err != nil {
		t.Fatal(err)
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	want, err := calibrateFinOpsCustodyControl(ctx, tx, dia)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readFinOpsCustodyPostgresShape(ctx, tx, "public")
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("constructor projection %d:\nreference=%s\ninstalled=%s", i, want[i], got[i])
		}
	}
}

func TestFinOpsCustodyVerifierPostgresLeavesDurableCatalogUnchanged(t *testing.T) {
	ctx := context.Background()
	owner, _, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)
	enrollThrough(t, ctx, owner, dia, custodyInstanceA)
	before := finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres)
	for _, change := range []string{"valid", "extra_column", "foreign_guard"} {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if change == "extra_column" {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE public.control_custody_proof ADD COLUMN untrusted TEXT"); err != nil {
				t.Fatal(err)
			}
		}
		if change == "foreign_guard" {
			if _, err := tx.ExecContext(ctx, "CREATE SCHEMA custody_guard_shadow"); err != nil {
				t.Fatal(err)
			}
			for _, statement := range dia.FinOpsCustodyControlStmts() {
				if strings.HasPrefix(statement, "CREATE FUNCTION public.") {
					statement = strings.Replace(statement, "CREATE FUNCTION public.", "CREATE FUNCTION custody_guard_shadow.", 1)
					if _, err := tx.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasPrefix(statement, "CREATE TRIGGER control_custody_transition_order ") {
					if _, err := tx.ExecContext(ctx, "DROP TRIGGER control_custody_transition_order ON public.control_custody_transition"); err != nil {
						t.Fatal(err)
					}
					statement = strings.Replace(statement, "EXECUTE FUNCTION public.", "EXECUTE FUNCTION custody_guard_shadow.", 1)
					if _, err := tx.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		err = verifyFinOpsCustodyControlExact(ctx, tx, dia)
		if (err != nil) != (change != "valid") {
			t.Fatalf("exact verifier change=%s: %v", change, err)
		}
		// Both calibration success and a later comparison refusal preserve the
		// caller's transaction and remove every temporary reference object.
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_catalog.pg_class WHERE relnamespace = pg_catalog.pg_my_temp_schema() AND relname LIKE 'control_custody_%'`).Scan(&references); err != nil || references != 0 {
			t.Fatalf("reference residue=%d err=%v", references, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if after := finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres); after != before {
			t.Fatal("custody verification changed the durable catalog")
		}
	}
	var revision int
	if err := owner.QueryRowContext(ctx, "SELECT revision FROM public.control_custody_enrollment").Scan(&revision); err != nil || revision != 3 {
		t.Fatalf("verification changed enrolled data: revision=%d err=%v", revision, err)
	}
}

func TestFinOpsCustodyVerifierPostgresCalibrationRefusals(t *testing.T) {
	ctx := context.Background()
	owner, app, dia, roles := custodyPG(t)
	applyCustodyPG(t, owner, dia, roles)
	t.Run("read_only", func(t *testing.T) {
		tx, err := owner.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := verifyFinOpsCustodyControlExact(ctx, tx, dia); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("read-only calibration refusal=%v", err)
		}
		var usable int
		if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&usable); err != nil || usable != 1 {
			t.Fatalf("read-only refusal aborted its transaction: %v", err)
		}
	})
	t.Run("no_temp", func(t *testing.T) {
		var database string
		if err := owner.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ExecContext(ctx, "REVOKE TEMP ON DATABASE "+quoteIdent(database)+" FROM PUBLIC, "+quoteIdent(roles.App.bindable())); err != nil {
			t.Fatal(err)
		}
		before := finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres)
		tx, err := app.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyFinOpsCustodyControlExact(ctx, tx, dia); err == nil || !strings.Contains(err.Error(), "TEMP") {
			t.Fatalf("no-TEMP calibration refusal=%v", err)
		}
		var usable int
		if err := tx.QueryRowContext(ctx, "SELECT 1").Scan(&usable); err != nil || usable != 1 {
			t.Fatalf("no-TEMP refusal aborted its transaction: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if after := finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres); after != before {
			t.Fatal("no-TEMP refusal changed the durable catalog")
		}
	})
	t.Run("reference_collision", func(t *testing.T) {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE control_custody_proof (retained INTEGER)"); err != nil {
			t.Fatal(err)
		}
		if err := verifyFinOpsCustodyControlExact(ctx, tx, dia); err == nil {
			t.Fatal("reference collision passed")
		}
		var retained int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pg_temp.control_custody_proof").Scan(&retained); err != nil {
			t.Fatalf("calibration failure damaged the caller's temporary relation: %v", err)
		}
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_catalog.pg_class WHERE relnamespace = pg_catalog.pg_my_temp_schema() AND relname = 'control_custody_enrollment'`).Scan(&references); err != nil || references != 0 {
			t.Fatalf("partial calibration residue=%d err=%v", references, err)
		}
	})
}

func TestFinOpsCustodyCalibrationRefusesStandbyCapability(t *testing.T) {
	if err := finOpsCustodyCalibrationCapability(true, false, true); err == nil {
		t.Fatal("hot-standby capability passed")
	}
	if err := finOpsCustodyCalibrationCapability(true, false, false); err != nil {
		t.Fatal(err)
	}
}
