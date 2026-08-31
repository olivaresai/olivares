// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWorkLeaseSchemaUpgradeBackfillsExactlyOnce(t *testing.T) {
	t.Parallel()

	type backend struct {
		name            string
		engine          store.Engine
		dsn             string
		driver          string
		backfillVersion int
	}
	backends := []backend{{
		name: "sqlite", engine: store.EngineSQLite,
		dsn:    filepath.Join(t.TempDir(), "work-lease-backfill.db"),
		driver: "sqlite", backfillVersion: 30,
	}}
	if enginetest.PostgresAvailable(t) {
		pg := enginetest.IsolatedPostgres(t)
		backends = append(backends, backend{
			name: "postgres", engine: store.EnginePostgres, dsn: pg.App,
			driver: "pgx", backfillVersion: 10,
		})
	} else {
		t.Logf("%s unset: PostgreSQL backfill NOT exercised", enginetest.EnvSuperuserDSN)
	}

	for _, be := range backends {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			open := func() (*Module, store.Store) {
				t.Helper()
				m := New()
				st, err := engine.Open(
					ctx,
					store.Config{Engine: be.engine, DSN: be.dsn, Debug: true},
					m.RegisterSchema,
				)
				if err != nil {
					t.Fatalf("open %s: %v", be.name, err)
				}
				m.UseData(api.NewModuleData(st))
				return m, st
			}

			m, st := open()
			t.Cleanup(func() { _ = st.Close() })
			tenantA, tenantB := workSchemaBackfillTenants(t, st)
			workspaceA, otherA := workSchemaWorkspaces(t, ctx, m, tenantA)
			workspaceB, _ := workSchemaWorkspaces(t, ctx, m, tenantB)
			expected := map[model.TenantID]map[model.ID]model.ID{
				tenantA: {},
				tenantB: {},
			}
			for _, seed := range []struct {
				tenant    model.TenantID
				workspace model.ID
				title     string
			}{
				{tenant: tenantA, workspace: workspaceA, title: "K1 default workspace"},
				{tenant: tenantA, workspace: otherA, title: "K1 other workspace"},
				{tenant: tenantB, workspace: workspaceB, title: "K1 second tenant"},
			} {
				item := workSchemaMustCreate(
					t, ctx, m, seed.tenant, workItemKind,
					workSchemaItem(seed.workspace, seed.title),
				)
				expected[seed.tenant][model.ID(item.String(model.ColID))] = seed.workspace
			}
			for tenant := range expected {
				if got := workSchemaLeaseRows(t, m, tenant); len(got) != 0 {
					t.Fatalf("leases before replay for %s = %d, want 0", tenant, len(got))
				}
			}
			if err := st.Close(); err != nil {
				t.Fatalf("close before upgrade: %v", err)
			}

			workSchemaForgetMigration(t, be.driver, be.dsn, be.backfillVersion)
			m, st = open()
			workSchemaAssertBackfill(t, m, expected)
			if err := st.Close(); err != nil {
				t.Fatalf("close after first backfill: %v", err)
			}

			// Replaying the migration body is the idempotence witness. Merely
			// reopening with its receipt intact would exercise only migrate.Apply's
			// skip path, not the INSERT's own NOT EXISTS/ON CONFLICT behavior.
			workSchemaForgetMigration(t, be.driver, be.dsn, be.backfillVersion)
			m, st = open()
			workSchemaAssertBackfill(t, m, expected)

			for tenant, items := range expected {
				for itemID, workspace := range items {
					_, err := workSchemaCreate(ctx, m, tenant, workLeaseKind, model.Record{
						colWorkWorkspaceID:   workspace.String(),
						colWorkItemID:        itemID.String(),
						colLeaseFence:        int64(0),
						colLeaseState:        workLeaseVacant,
						colLeaseRenewalCount: int64(0),
					})
					if err == nil {
						t.Fatalf("duplicate lease for work item %s succeeded", itemID)
					}
					break
				}
				break
			}
			workSchemaAssertBackfill(t, m, expected)
			if err := st.Close(); err != nil {
				t.Fatalf("close after idempotence replay: %v", err)
			}
		})
	}
}

func TestWorkLeaseSchemaRejectsNonCanonicalSessionIdentityAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			workspace, _ := workSchemaWorkspaces(t, context.Background(), m, tenant)
			invalidSID := "osn_" + strings.Repeat("z", 8) + "-" +
				strings.Repeat("z", 4) + "-" + strings.Repeat("z", 4) + "-" +
				strings.Repeat("z", 4) + "-" + strings.Repeat("z", 12)
			createLease := func(title, sid string) error {
				t.Helper()
				item := workSchemaMustCreate(
					t, context.Background(), m, tenant, workItemKind, workSchemaItem(workspace, title),
				)
				_, err := workSchemaCreate(context.Background(), m, tenant, workLeaseKind, model.Record{
					colWorkWorkspaceID: workspace.String(), colWorkItemID: item.String(model.ColID),
					colLeaseHolderSID: sid, colLeaseHolderRunRef: "run:canonical-sid-test",
					colLeaseHolderAgentRef: "agent:canonical-sid-test", colLeaseFence: int64(1),
					colLeaseState: workLeaseActive, colLeaseAcquiredAt: workSchemaLeaseTime(0),
					colLeaseExpiresAt: workSchemaLeaseTime(10), colLeaseRenewalCount: int64(0),
				})
				return err
			}

			t.Run("rejects non-hex UUID shape", func(t *testing.T) {
				// FIRE: the old SQLite guard accepted any correctly hyphenated
				// 36-byte spelling after osn_.
				if err := createLease(
					"noncanonical SID", invalidSID,
				); err == nil {
					t.Fatal("noncanonical 40-byte SID crossed the schema guard")
				}
			})
			t.Run("accepts canonical SID", func(t *testing.T) {
				// NO-FIRE: a canonical lowercase UUID SID remains a valid
				// materialized lease; the guard is not deny-all.
				if err := createLease("canonical SID", "osn_"+model.NewID().String()); err != nil {
					t.Fatalf("canonical SID rejected: %v", err)
				}
			})
			activateVacant := func(t *testing.T, title, sid string) error {
				t.Helper()
				item := workSchemaMustCreate(
					t, context.Background(), m, tenant, workItemKind, workSchemaItem(workspace, title),
				)
				vacant, err := workSchemaCreate(context.Background(), m, tenant, workLeaseKind, model.Record{
					colWorkWorkspaceID: workspace.String(), colWorkItemID: item.String(model.ColID),
					colLeaseFence: int64(0), colLeaseState: workLeaseVacant,
					colLeaseRenewalCount: int64(0),
				})
				if err != nil {
					return err
				}
				vacant[colLeaseHolderSID] = sid
				vacant[colLeaseHolderRunRef] = "run:canonical-sid-update"
				vacant[colLeaseHolderAgentRef] = "agent:canonical-sid-update"
				vacant[colLeaseFence], vacant[colLeaseState] = int64(1), workLeaseActive
				vacant[colLeaseAcquiredAt], vacant[colLeaseExpiresAt] =
					workSchemaLeaseTime(0), workSchemaLeaseTime(10)
				_, err = workSchemaUpdateLease(t, m, tenant, vacant)
				return err
			}
			t.Run("update rejects non-hex UUID shape", func(t *testing.T) {
				if err := activateVacant(t, "noncanonical SID update", invalidSID); err == nil {
					t.Fatal("noncanonical SID crossed the update schema guard")
				}
			})
			t.Run("update accepts canonical SID", func(t *testing.T) {
				if err := activateVacant(t, "canonical SID update", "osn_"+model.NewID().String()); err != nil {
					t.Fatalf("canonical SID update rejected: %v", err)
				}
			})
		})
	}
}

// TestWorkLeaseSQLiteFinalGuardUpgradeIsAtomicAndRefusesInvalidHistory covers
// the only database state the v32 guard replacement cannot repair honestly.
// Versions 25..31 admitted a correctly sized but non-hex SID; the upgrade must
// keep both old guards and withhold its receipt instead of rewriting authority
// or committing half of the replacement. Once an operator removes the corrupt
// fixture, the exact same migration converges to both final guards.
func TestWorkLeaseSQLiteFinalGuardUpgradeIsAtomicAndRefusesInvalidHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "work-lease-final-guard.db")
	open := func() (*Module, store.Store, error) {
		m := New()
		st, err := engine.Open(ctx, store.Config{
			Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		}, m.RegisterSchema)
		if err == nil {
			m.UseData(api.NewModuleData(st))
		}
		return m, st, err
	}
	m, st, err := open()
	if err != nil {
		t.Fatalf("fresh open: %v", err)
	}
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "K2 migration preflight", Slug: "k2-migration-preflight", Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	workspace, _ := workSchemaWorkspaces(t, ctx, m, tenant)
	item := workSchemaMustCreate(t, ctx, m, tenant, workItemKind,
		workSchemaItem(workspace, "K2 invalid historical SID"))
	itemID := recordID(item)
	if err := st.Close(); err != nil {
		t.Fatalf("close fresh store: %v", err)
	}

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw SQLite: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	workSchemaForgetMigration(t, "sqlite", dsn, 32)
	for _, trigger := range []string{"sessions_work_lease_guard_ins", "sessions_work_lease_guard_upd"} {
		if _, err := raw.ExecContext(ctx, "DROP TRIGGER "+trigger); err != nil {
			t.Fatalf("drop final %s: %v", trigger, err)
		}
		legacy, err := fs.ReadFile(sessionsMigrationsFS,
			"migrations/sqlite/"+map[string]string{
				"sessions_work_lease_guard_ins": "0025_work_lease_guard_ins.sql",
				"sessions_work_lease_guard_upd": "0031_work_lease_renewal_time_boundary.sql",
			}[trigger])
		if err != nil {
			t.Fatalf("read legacy %s: %v", trigger, err)
		}
		if trigger == "sessions_work_lease_guard_upd" {
			// v31 includes its own DROP. The trigger is already absent here.
			legacy = []byte(strings.Replace(string(legacy),
				"DROP TRIGGER IF EXISTS sessions_work_lease_guard_upd;\n", "", 1))
		}
		if _, err := raw.ExecContext(ctx, string(legacy)); err != nil {
			t.Fatalf("restore legacy %s: %v", trigger, err)
		}
	}
	legacySQL := workSchemaSQLiteLeaseTriggerSQL(t, raw)

	// Bypass only the legacy insert guard long enough to reproduce a row that a
	// pre-v32 database could already contain, then restore it before boot.
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER sessions_work_lease_guard_ins"); err != nil {
		t.Fatalf("drop legacy insert guard: %v", err)
	}
	invalidSID := "osn_" + strings.Repeat("z", 8) + "-" + strings.Repeat("z", 4) + "-" +
		strings.Repeat("z", 4) + "-" + strings.Repeat("z", 4) + "-" + strings.Repeat("z", 12)
	now := model.NewTimestamp(time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)).String()
	_, err = raw.ExecContext(ctx, `INSERT INTO sessions_work_lease
(id, tenant_id, created_at, updated_at, version, workspace_id, work_item_id,
 holder_sid, holder_run_ref, holder_agent_ref, fence, state, acquired_at, expires_at, renewal_count)
VALUES (?, ?, ?, ?, 1, ?, ?, ?, 'run:legacy', 'agent:legacy', 1, 'active', ?, ?, 0)`,
		itemID.String(), tenant.String(), now, now, workspace.String(), itemID.String(), invalidSID,
		now, model.NewTimestamp(time.Date(2026, time.August, 12, 10, 5, 0, 0, time.UTC)).String())
	if err != nil {
		t.Fatalf("seed invalid historical lease: %v", err)
	}
	legacyIns, err := fs.ReadFile(sessionsMigrationsFS, "migrations/sqlite/0025_work_lease_guard_ins.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(legacyIns)); err != nil {
		t.Fatalf("restore legacy insert guard: %v", err)
	}
	legacySQL = workSchemaSQLiteLeaseTriggerSQL(t, raw)

	_, failed, err := open()
	if failed != nil {
		_ = failed.Close()
	}
	if err == nil {
		t.Fatal("v32 accepted a historical non-canonical holder SID")
	}
	if got := workSchemaSQLiteLeaseTriggerSQL(t, raw); got != legacySQL {
		t.Fatalf("failed v32 committed a partial trigger replacement:\nlegacy=%q\nafter=%q", legacySQL, got)
	}
	var receipts int
	if err := raw.QueryRowContext(ctx,
		"SELECT count(*) FROM schema_migrations_mod_sessions WHERE version = 32",
	).Scan(&receipts); err != nil {
		t.Fatalf("count v32 receipts: %v", err)
	}
	if receipts != 0 {
		t.Fatalf("failed v32 receipts = %d, want 0", receipts)
	}
	// This is a raw corruption-recovery fixture, not a product repair path: the
	// production no-delete trigger correctly prevents removing a lease. Disable
	// it only for this test's operator cleanup, then restore it before boot.
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER sessions_work_lease_no_delete"); err != nil {
		t.Fatalf("drop no-delete guard for fixture cleanup: %v", err)
	}
	if _, err := raw.ExecContext(ctx,
		"DELETE FROM sessions_work_lease WHERE tenant_id = ? AND work_item_id = ?",
		tenant.String(), itemID.String()); err != nil {
		t.Fatalf("remove corrupt fixture: %v", err)
	}
	noDelete, err := fs.ReadFile(sessionsMigrationsFS, "migrations/sqlite/0027_work_lease_no_delete.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(noDelete)); err != nil {
		t.Fatalf("restore no-delete guard: %v", err)
	}
	_, st, err = open()
	if err != nil {
		t.Fatalf("retry v32 after removing corrupt row: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close upgraded store: %v", err)
	}
	if got := workSchemaSQLiteLeaseTriggerSQL(t, raw); got == legacySQL {
		t.Fatal("successful v32 left both legacy triggers unchanged")
	}
	if err := raw.QueryRowContext(ctx,
		"SELECT count(*) FROM schema_migrations_mod_sessions WHERE version = 32",
	).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("successful v32 receipt = %d, %v; want 1", receipts, err)
	}
}

func workSchemaSQLiteLeaseTriggerSQL(t *testing.T, db *sql.DB) string {
	t.Helper()
	var ins, upd string
	for _, target := range []struct {
		name string
		dst  *string
	}{
		{name: "sessions_work_lease_guard_ins", dst: &ins},
		{name: "sessions_work_lease_guard_upd", dst: &upd},
	} {
		if err := db.QueryRowContext(context.Background(),
			"SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?", target.name,
		).Scan(target.dst); err != nil {
			t.Fatalf("read %s: %v", target.name, err)
		}
	}
	return ins + "\x00" + upd
}

func workSchemaBackfillTenants(t *testing.T, st store.Store) (model.TenantID, model.TenantID) {
	t.Helper()
	var tenantA, tenantB model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(context.Background()); err != nil {
			return err
		}
		orgA, err := sys.CreateOrg(context.Background(), model.Org{
			Name: "Work lease backfill A", Slug: "work-lease-backfill-a", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		orgB, err := sys.CreateOrg(context.Background(), model.Org{
			Name: "Work lease backfill B", Slug: "work-lease-backfill-b", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		tenantA, tenantB = orgA.TenantID, orgB.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision backfill tenants: %v", err)
	}
	return tenantA, tenantB
}

func workSchemaForgetMigration(t *testing.T, driver, dsn string, version int) {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open migration ledger: %v", err)
	}
	defer db.Close() //nolint:errcheck
	placeholder := "?"
	if driver == "pgx" {
		placeholder = "$1"
	}
	result, err := db.ExecContext(
		context.Background(),
		"DELETE FROM schema_migrations_mod_sessions WHERE version = "+placeholder,
		version,
	)
	if err != nil {
		t.Fatalf("remove backfill receipt v%d: %v", version, err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("count removed backfill receipts: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed backfill receipts = %d, want 1", removed)
	}
}

func workSchemaLeaseRows(t *testing.T, m *Module, tenant model.TenantID) []model.Record {
	t.Helper()
	var leases []model.Record
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		leases, err = listAll(context.Background(), repo)
		return err
	}); err != nil {
		t.Fatalf("list work leases for %s: %v", tenant, err)
	}
	return leases
}

func workSchemaAssertBackfill(
	t *testing.T,
	m *Module,
	expected map[model.TenantID]map[model.ID]model.ID,
) {
	t.Helper()
	for tenant, items := range expected {
		leases := workSchemaLeaseRows(t, m, tenant)
		if len(leases) != len(items) {
			t.Fatalf("backfilled leases for %s = %d, want %d", tenant, len(leases), len(items))
		}
		seen := make(map[model.ID]int, len(leases))
		for _, lease := range leases {
			itemID := model.ID(lease.String(colWorkItemID))
			workspace, ok := items[itemID]
			if !ok {
				t.Fatalf("unexpected backfilled work item %s for %s", itemID, tenant)
			}
			seen[itemID]++
			if model.ID(lease.String(model.ColID)) != itemID {
				t.Errorf("backfilled lease id = %s, want work item id %s", lease.String(model.ColID), itemID)
			}
			if lease.String(model.ColTenantID) != tenant.String() ||
				lease.String(colWorkWorkspaceID) != workspace.String() {
				t.Errorf("backfilled lineage for %s = tenant %s workspace %s", itemID,
					lease.String(model.ColTenantID), lease.String(colWorkWorkspaceID))
			}
			if lease.String(colLeaseState) != workLeaseVacant || lease.Int(colLeaseFence) != 0 ||
				lease.Int(colLeaseRenewalCount) != 0 {
				t.Errorf("backfilled lease %s is not vacant: %#v", itemID, lease)
			}
			for _, column := range []string{
				colLeaseHolderSID, colLeaseHolderRunRef, colLeaseHolderAgentRef,
				colLeaseAcquiredAt, colLeaseRenewedAt, colLeaseExpiresAt,
				colLeaseEndedAt, colLeaseEndReason,
			} {
				if !lease.IsNull(column) {
					t.Errorf("backfilled vacant lease %s has %s = %v", itemID, column, lease[column])
				}
			}
		}
		for itemID := range items {
			if seen[itemID] != 1 {
				t.Errorf("backfilled work item %s has %d leases, want 1", itemID, seen[itemID])
			}
		}
	}
}

func TestWorkLeaseSchemaFenceExhaustionAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be

		t.Run(be.name+" exhausted terminal transition persists", func(t *testing.T) {
			m, tenant, _ := be.open(t)
			_, itemID, lease := workSchemaActiveLease(
				t, m, tenant, "exhausted terminal", math.MaxInt64, 3,
			)

			wrongRenewals := workSchemaEndedLease(lease, workLeaseExpired, math.MaxInt64)
			wrongRenewals[colLeaseRenewalCount] = int64(4)
			if _, err := workSchemaUpdateLease(t, m, tenant, wrongRenewals); err == nil {
				t.Fatal("exhausted terminal transition changed renewal_count")
			}

			lease = workSchemaGetLease(t, m, tenant, itemID)
			ended, err := workSchemaUpdateLease(
				t, m, tenant,
				workSchemaEndedLease(lease, workLeaseExpired, math.MaxInt64),
			)
			if err != nil {
				t.Fatalf("active -> expired at exhausted fence: %v", err)
			}
			if ended.String(colLeaseState) != workLeaseExpired ||
				ended.Int(colLeaseFence) != math.MaxInt64 ||
				ended.Int(colLeaseRenewalCount) != 3 {
				t.Fatalf("exhausted terminal row = %#v", ended)
			}

			illegal := workSchemaEndedLease(ended, workLeaseRevoked, math.MaxInt64)
			if _, err := workSchemaUpdateLease(t, m, tenant, illegal); err == nil {
				t.Fatal("terminal -> terminal transition succeeded")
			}
			if got := workSchemaGetLease(t, m, tenant, itemID); got.String(colLeaseState) != workLeaseExpired {
				t.Fatalf("rejected terminal transition changed state to %q", got.String(colLeaseState))
			}
		})

		t.Run(be.name+" ordinary terminal transition still bumps", func(t *testing.T) {
			m, tenant, _ := be.open(t)
			_, itemID, lease := workSchemaActiveLease(t, m, tenant, "ordinary terminal", 41, 2)

			if _, err := workSchemaUpdateLease(
				t, m, tenant, workSchemaEndedLease(lease, workLeaseReleased, 41),
			); err == nil {
				t.Fatal("ordinary terminal transition without a fence bump succeeded")
			}
			lease = workSchemaGetLease(t, m, tenant, itemID)
			ended, err := workSchemaUpdateLease(
				t, m, tenant, workSchemaEndedLease(lease, workLeaseReleased, 42),
			)
			if err != nil {
				t.Fatalf("ordinary terminal transition with one fence bump: %v", err)
			}
			if ended.Int(colLeaseFence) != 42 || ended.String(colLeaseState) != workLeaseReleased {
				t.Fatalf("ordinary terminal row = %#v", ended)
			}
		})

		t.Run(be.name+" exhausted authority cannot transfer", func(t *testing.T) {
			m, tenant, _ := be.open(t)
			_, itemID, lease := workSchemaActiveLease(
				t, m, tenant, "exhausted transfer", math.MaxInt64, 0,
			)

			takeover := workSchemaClone(lease)
			takeover[colLeaseHolderSID] = "osn_" + model.NewID().String()
			takeover[colLeaseHolderRunRef] = "run:replacement"
			takeover[colLeaseHolderAgentRef] = "agent:replacement"
			takeover[colLeaseRenewalCount] = int64(0)
			_, err := workSchemaUpdateLease(t, m, tenant, takeover)
			if err == nil {
				t.Fatal("authority transfer at exhausted fence succeeded")
			}
			if strings.Contains(strings.ToLower(err.Error()), "out of range") {
				t.Fatalf("exhausted transfer evaluated fence + 1: %v", err)
			}

			lease = workSchemaGetLease(t, m, tenant, itemID)
			renewal := workSchemaClone(lease)
			renewal[colLeaseRenewedAt] = workSchemaLeaseTime(2)
			renewal[colLeaseExpiresAt] = workSchemaLeaseTime(12)
			renewal[colLeaseRenewalCount] = int64(1)
			renewed, err := workSchemaUpdateLease(t, m, tenant, renewal)
			if err != nil {
				t.Fatalf("same authority renewal at exhausted fence: %v", err)
			}
			if renewed.Int(colLeaseFence) != math.MaxInt64 || renewed.Int(colLeaseRenewalCount) != 1 {
				t.Fatalf("exhausted renewal row = %#v", renewed)
			}
		})
	}
}

func workSchemaActiveLease(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	title string,
	fence, renewalCount int64,
) (model.ID, model.ID, model.Record) {
	t.Helper()
	workspace, _ := workSchemaWorkspaces(t, context.Background(), m, tenant)
	item := workSchemaMustCreate(
		t, context.Background(), m, tenant, workItemKind, workSchemaItem(workspace, title),
	)
	itemID := model.ID(item.String(model.ColID))
	lease := workSchemaMustCreate(t, context.Background(), m, tenant, workLeaseKind, model.Record{
		colWorkWorkspaceID:     workspace.String(),
		colWorkItemID:          itemID.String(),
		colLeaseHolderSID:      "osn_" + model.NewID().String(),
		colLeaseHolderRunRef:   "run:original",
		colLeaseHolderAgentRef: "agent:original",
		colLeaseFence:          fence,
		colLeaseState:          workLeaseActive,
		colLeaseAcquiredAt:     workSchemaLeaseTime(0),
		colLeaseExpiresAt:      workSchemaLeaseTime(10),
		colLeaseRenewalCount:   renewalCount,
	})
	return workspace, itemID, lease
}

func workSchemaEndedLease(lease model.Record, state string, fence int64) model.Record {
	ended := workSchemaClone(lease)
	ended[colLeaseState] = state
	ended[colLeaseFence] = fence
	ended[colLeaseEndedAt] = workSchemaLeaseTime(10)
	if state == workLeaseReleased {
		ended[colLeaseEndReason] = nil
	} else {
		ended[colLeaseEndReason] = "terminal evidence"
	}
	return ended
}

func workSchemaUpdateLease(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	lease model.Record,
) (model.Record, error) {
	t.Helper()
	var updated model.Record
	err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		updated, err = repo.Update(context.Background(), lease)
		return err
	})
	return updated, err
}

func workSchemaGetLease(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	itemID model.ID,
) model.Record {
	t.Helper()
	var lease model.Record
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		rows, err := listAll(
			context.Background(), repo,
			model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()},
		)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("work lease rows = %d, want 1", len(rows))
		}
		lease = rows[0]
		return nil
	}); err != nil {
		t.Fatalf("get work lease: %v", err)
	}
	return lease
}

func workSchemaLeaseTime(minutes int) string {
	return model.NewTimestamp(
		time.Date(2026, time.August, 9, 12, minutes, 0, 0, time.UTC),
	).String()
}
