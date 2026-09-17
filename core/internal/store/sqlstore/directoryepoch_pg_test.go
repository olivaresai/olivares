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
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The estate below is driven only through supported product paths: ordinary
// Open, the DBA inventory installation command seam and the maintenance
// ceremony. No control row or epoch is rewound by raw SQL; the only DBA acts
// are the ones a real operator performs on the closed routine (install, drift
// it, drop it, reinstall it).
func TestDirectoryEpochPostgresSplitOwnerNoAdminStatusAndHeal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	full := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 1,
		Clock: transactionClockFixedAppClock{now: fixed},
	}
	noAdmin := full
	noAdmin.AdminDSN = ""
	st, err := Open(ctx, full, nil)
	if err != nil {
		t.Fatalf("open split-owner directory store: %v", err)
	}
	// Fresh estate, first boot, before SYSTEM genesis: the pinned AdminDSN snapshot
	// proves an empty inventory and the status says so instead of claiming coverage.
	directoryEpochTestWantStatus(t, st, store.DirectoryStatus{
		EpochCoverageComplete:      false,
		ControlMode:                store.DirectoryControlStaged,
		WriterPosture:              store.DirectoryWriterSplitOwner,
		ExpectedGeneration:         1,
		CoverageProtocol:           coverageProtocolLegacy,
		InventoryAuthority:         "admin_dsn",
		InventoryUnavailableReason: "system_bootstrap_pending",
	})
	beforeCreate := time.Now().UTC().Add(-time.Second)
	tenant := provisionTenant(t, st, "directory-pg-split")
	afterCreate := time.Now().UTC().Add(time.Second)
	if err := st.Close(); err != nil {
		t.Fatalf("close split-owner seed boot: %v", err)
	}

	app, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw app pool: %v", err)
	}
	defer app.Close() //nolint:errcheck
	seeded, found := directoryEpochTestReadPostgresRow(t, app, tenant)
	if !found || seeded.id != tenant.String() || seeded.rowTenant != tenant.String() ||
		seeded.version != 1 {
		t.Fatalf("seeded PostgreSQL epoch = %+v found=%t", seeded, found)
	}
	if seeded.createdAt == fixed.String() || seeded.updatedAt != seeded.createdAt {
		t.Fatalf("seeded PostgreSQL epoch timestamps = %s/%s, skew=%s",
			seeded.createdAt, seeded.updatedAt, fixed.String())
	}
	seededTime, err := model.ParseTimestamp(seeded.createdAt)
	if err != nil || seededTime.Time().Before(beforeCreate) || seededTime.Time().After(afterCreate) {
		t.Fatalf("seeded PostgreSQL DB time=%s err=%v outside [%s,%s]",
			seeded.createdAt, err, beforeCreate, afterCreate)
	}
	stagedLegacy := directoryWriterControlState{
		Mode: directoryWriterStaged, ExpectedGeneration: 1, CoverageProtocol: coverageProtocolLegacy,
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, stagedLegacy, seeded)

	// Staged legacy, no AdminDSN, no closed routine: the ordinary Open still takes
	// the writer transaction and the global lock, and publishes an explicitly
	// incomplete witness. The reason is the missing routine, never an attested
	// empty estate, and the counts stay zero because nothing was enumerated.
	incomplete, err := Open(ctx, noAdmin, nil)
	if err != nil {
		t.Fatalf("staged no-admin split-owner boot without routine: %v", err)
	}
	directoryEpochTestWantStatus(t, incomplete, store.DirectoryStatus{
		EpochCoverageComplete:      false,
		ControlMode:                store.DirectoryControlStaged,
		WriterPosture:              store.DirectoryWriterSplitOwner,
		ExpectedGeneration:         1,
		CoverageProtocol:           coverageProtocolLegacy,
		InventoryUnavailableReason: "closed_routine_missing",
	})
	if err := incomplete.Close(); err != nil {
		t.Fatalf("close incomplete boot: %v", err)
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, stagedLegacy, seeded)

	// The incomplete witness is produced UNDER the global writer lock, not by an
	// early read outside the reconcile transaction: while a foreign session holds
	// that lock the same staged no-admin Open is measurably blocked on it and
	// publishes nothing until the holder lets go.
	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open raw superuser pool: %v", err)
	}
	defer super.Close() //nolint:errcheck
	holder, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin foreign writer lock holder: %v", err)
	}
	defer holder.Rollback() //nolint:errcheck // release even if the lock/barrier assertion fails
	if _, err := holder.ExecContext(ctx,
		`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
		directoryWriterLockKey); err != nil {
		t.Fatalf("hold foreign writer lock: %v", err)
	}
	blocked := make(chan error, 1)
	go func() {
		st, err := Open(ctx, noAdmin, nil)
		if err != nil {
			blocked <- err
			return
		}
		got, supported, err := st.(store.DirectoryStatuser).DirectoryStatus(ctx)
		if closeErr := st.Close(); err == nil {
			err = closeErr
		}
		if want := (store.DirectoryStatus{
			ControlMode: store.DirectoryControlStaged, WriterPosture: store.DirectoryWriterSplitOwner,
			ExpectedGeneration: 1, CoverageProtocol: coverageProtocolLegacy, InventoryUnavailableReason: "closed_routine_missing",
		}); err == nil && (!supported || got != want) {
			err = fmt.Errorf("status after the lock was released = %+v supported=%t, want %+v", got, supported, want)
		}
		blocked <- err
	}()
	waitForBlockedBackend(t, ctx, super, pg.Database)
	select {
	case err := <-blocked:
		t.Fatalf("staged no-admin Open published while the writer lock was held: %v", err)
	default:
	}
	if err := holder.Rollback(); err != nil {
		t.Fatalf("release foreign writer lock: %v", err)
	}
	if err := <-blocked; err != nil {
		t.Fatalf("staged no-admin Open after the lock was released: %v", err)
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, stagedLegacy, seeded)

	// Maintenance never accepts the missing routine as an inventory authority,
	// even from the staged legacy prestate where the ordinary Open tolerates it.
	directoryEpochTestWantNoAdminRefusal(t, "staged maintenance without routine",
		func() error {
			_, _, changed, err := OpenDirectoryWriterMaintenance(ctx, noAdmin, nil, 1)
			if err == nil {
				return fmt.Errorf("maintenance returned no error, changed=%t", changed)
			}
			return err
		}, "closed directory inventory routine is required")
	directoryEpochTestWantPostgresEstate(t, app, tenant, stagedLegacy, seeded)

	// The supported DBA installation is the only positive path to the authority.
	install := func(label string) {
		t.Helper()
		result, err := ProvisionPostgres(ctx, pg.Superuser, directoryEpochTestInventorySpec(t, app), true)
		if err != nil || !result.DirectoryInventoryInstalled {
			t.Fatalf("%s: install closed inventory routine: installed=%t err=%v",
				label, result.DirectoryInventoryInstalled, err)
		}
	}
	install("first installation")

	// A malformed authority is a refusal in its own right: EXECUTE leaked to
	// PUBLIC is drift, not absence, so the staged boot does not fall back to the
	// incomplete witness, and the installation command refuses to paper over it.
	if _, err := super.ExecContext(ctx,
		"GRANT EXECUTE ON FUNCTION public.olivares_directory_inventory_v1() TO PUBLIC"); err != nil {
		t.Fatalf("drift closed inventory routine ACL: %v", err)
	}
	directoryEpochTestWantNoAdminRefusal(t, "staged boot with drifted routine",
		func() error {
			candidate, err := Open(ctx, noAdmin, nil)
			if candidate != nil {
				_ = candidate.Close()
			}
			return err
		},
		"authority function olivares_directory_inventory_v1 signature/owner/ACL is not closed")
	if result, err := ProvisionPostgres(ctx, pg.Superuser, directoryEpochTestInventorySpec(t, app), true); err == nil || result.DirectoryInventoryInstalled {
		t.Fatalf("reinstallation over drift: installed=%t err=%v", result.DirectoryInventoryInstalled, err)
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, stagedLegacy, seeded)
	if _, err := super.ExecContext(ctx, "DROP FUNCTION public.olivares_directory_inventory_v1()"); err != nil {
		t.Fatalf("drop drifted routine: %v", err)
	}
	install("reinstallation after the drifted routine was dropped")

	// Closed routine present: the same staged legacy boot now proves the complete
	// inventory (SYSTEM plus the one business organization and its epoch) without
	// an AdminDSN, and the existing epoch is left exactly as it was seeded.
	covered, err := Open(ctx, noAdmin, nil)
	if err != nil {
		t.Fatalf("staged no-admin boot with routine: %v", err)
	}
	directoryEpochTestWantStatus(t, covered, store.DirectoryStatus{
		EpochCoverageComplete:         true,
		ControlMode:                   store.DirectoryControlStaged,
		WriterPosture:                 store.DirectoryWriterSplitOwner,
		ExpectedGeneration:            1,
		CoverageProtocol:              coverageProtocolLegacy,
		UserAuthorityCoverageComplete: true,
		InventoryAuthority:            "closed_routine",
		InventoryOrgCount:             2,
		InventoryBusinessOrgCount:     1,
		InventoryEpochCount:           1,
	})
	if err := covered.Close(); err != nil {
		t.Fatalf("close covered boot: %v", err)
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, stagedLegacy, seeded)

	// The ratified maintenance cutover through the closed routine: G bumps
	// exactly once on the database clock and the control becomes the target.
	beforeCutover := time.Now().UTC().Add(-time.Second)
	before, after, changed, err := OpenDirectoryWriterMaintenance(ctx, noAdmin, nil, 1)
	if err != nil {
		t.Fatalf("no-admin maintenance cutover: %v", err)
	}
	afterCutover := time.Now().UTC().Add(time.Second)
	wantAfter := store.DirectoryStatus{
		EpochCoverageComplete:         true,
		ControlMode:                   store.DirectoryControlEnforced,
		WriterPosture:                 store.DirectoryWriterSplitOwner,
		ExpectedGeneration:            2,
		CoverageProtocol:              coverageProtocolTarget,
		UserAuthorityCoverageComplete: true,
		InventoryAuthority:            "closed_routine",
		InventoryOrgCount:             2,
		InventoryBusinessOrgCount:     1,
		InventoryEpochCount:           1,
	}
	if !changed || before.CoverageProtocol != coverageProtocolLegacy || before.ControlMode != store.DirectoryControlStaged || after != wantAfter {
		t.Fatalf("cutover before=%+v after=%+v changed=%t want after=%+v", before, after, changed, wantAfter)
	}
	enforcedTarget := directoryWriterControlState{
		Mode: directoryWriterEnforced, ExpectedGeneration: 2, CoverageProtocol: coverageProtocolTarget,
	}
	bumped, found := directoryEpochTestReadPostgresRow(t, app, tenant)
	if !found || bumped.version != 2 || bumped.createdAt != seeded.createdAt ||
		bumped.updatedAt == seeded.updatedAt || bumped.updatedAt == fixed.String() {
		t.Fatalf("cutover epoch = %+v found=%t, want version 2 on the database clock over seeded %+v",
			bumped, found, seeded)
	}
	bumpedTime, err := model.ParseTimestamp(bumped.updatedAt)
	if err != nil || bumpedTime.Time().Before(beforeCutover) || bumpedTime.Time().After(afterCutover) {
		t.Fatalf("cutover PostgreSQL DB time=%s err=%v outside [%s,%s]",
			bumped.updatedAt, err, beforeCutover, afterCutover)
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, enforcedTarget, bumped)

	reopened, err := Open(ctx, noAdmin, nil)
	if err != nil {
		t.Fatalf("no-admin target reopen: %v", err)
	}
	directoryEpochTestWantStatus(t, reopened, wantAfter)
	if err := reopened.Close(); err != nil {
		t.Fatalf("close target reopen: %v", err)
	}
	// The same durable estate read through the AdminDSN authority agrees on
	// everything except the authority name.
	wantAdmin := wantAfter
	wantAdmin.InventoryAuthority = "admin_dsn"
	adminReopened, err := Open(ctx, full, nil)
	if err != nil {
		t.Fatalf("admin target reopen: %v", err)
	}
	directoryEpochTestWantStatus(t, adminReopened, wantAdmin)
	if err := adminReopened.Close(); err != nil {
		t.Fatalf("close admin target reopen: %v", err)
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, enforcedTarget, bumped)

	// Enforced target without the routine: neither the ordinary boot nor the
	// exact maintenance retry treats the missing authority as readiness, and
	// neither touches the durable estate.
	if _, err := super.ExecContext(ctx, "DROP FUNCTION public.olivares_directory_inventory_v1()"); err != nil {
		t.Fatalf("drop routine from the enforced estate: %v", err)
	}
	directoryEpochTestWantNoAdminRefusal(t, "enforced boot without routine",
		func() error {
			candidate, err := Open(ctx, noAdmin, nil)
			if candidate != nil {
				_ = candidate.Close()
			}
			return err
		},
		"closed directory inventory routine is required")
	directoryEpochTestWantNoAdminRefusal(t, "enforced maintenance retry without routine",
		func() error {
			_, _, changed, err := OpenDirectoryWriterMaintenance(ctx, noAdmin, nil, 1)
			if err == nil {
				return fmt.Errorf("maintenance retry succeeded with changed=%t", changed)
			}
			return err
		}, "closed directory inventory routine is required")
	directoryEpochTestWantPostgresEstate(t, app, tenant, enforcedTarget, bumped)

	// Reinstalling the routine is the only heal, and it is idempotent: the
	// retry writes nothing and the reopened witness is the cutover's.
	install("reinstallation on the enforced target")
	_, retry, changed, err := OpenDirectoryWriterMaintenance(ctx, noAdmin, nil, 1)
	if err != nil || changed || retry != wantAfter {
		t.Fatalf("exact retry=%+v changed=%t err=%v", retry, changed, err)
	}
	healed, err := Open(ctx, noAdmin, nil)
	if err != nil {
		t.Fatalf("healed no-admin reopen: %v", err)
	}
	directoryEpochTestWantStatus(t, healed, wantAfter)
	if err := healed.Close(); err != nil {
		t.Fatalf("close healed reopen: %v", err)
	}
	directoryEpochTestWantPostgresEstate(t, app, tenant, enforcedTarget, bumped)
}

// directoryEpochTestWantNoAdminRefusal proves a refusal is the typed directory
// sentinel with the named reason, not an unrelated failure that happens to be red.
func directoryEpochTestWantNoAdminRefusal(t *testing.T, label string, run func() error, reason string) {
	t.Helper()
	err := run()
	if !errors.Is(err, store.ErrDirectoryUnavailable) || !strings.Contains(err.Error(), reason) {
		t.Fatalf("%s: err = %v, want ErrDirectoryUnavailable with %q", label, err, reason)
	}
	t.Logf("NOADMIN_REFUSED|%s|%v", label, err)
}

// directoryEpochTestWantPostgresEstate compares the durable directory estate
// after a boot, refusal or ceremony: the control singleton, the one business
// epoch row and the absence of any User authority row, all read through the
// application role. The v10 shape is the schema cut; any runtime H, G, mode or
// generation change shows up here.
func directoryEpochTestWantPostgresEstate(t *testing.T, db *sql.DB, tenant model.TenantID, control directoryWriterControlState, epoch directoryEpochTestRow) {
	t.Helper()
	ctx := context.Background()
	dia, _ := dialect.New(store.EnginePostgres)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin durable estate read: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	state, err := readDirectoryWriterControlState(ctx, tx, dia)
	if err != nil || state != control {
		t.Fatalf("durable control = %+v err=%v, want %+v", state, err, control)
	}
	if err := bindDirectoryTenant(ctx, tx, dia, model.SystemTenantID); err != nil {
		t.Fatalf("bind SYSTEM for durable estate read: %v", err)
	}
	coverage, err := readUserAuthorityCoverage(ctx, tx, dia)
	if err != nil || len(coverage.Rows) != 0 || len(coverage.Users) != 0 || len(coverage.Missing) != 0 {
		t.Fatalf("durable H coverage rows=%d users=%d missing=%d err=%v, want none",
			len(coverage.Rows), len(coverage.Users), len(coverage.Missing), err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("end durable estate read: %v", err)
	}
	if got, found := directoryEpochTestReadPostgresRow(t, db, tenant); !found || got != epoch {
		t.Fatalf("durable epoch = %+v found=%t, want %+v", got, found, epoch)
	}
}

// directoryEpochTestInventorySpec resolves the installation spec from the
// live database the way the DBA command does: the application role is the
// connecting role and the schema owner is whoever owns public.orgs.
func directoryEpochTestInventorySpec(t *testing.T, app *sql.DB) store.PgProvisionSpec {
	t.Helper()
	var database, appRole, owner string
	if err := app.QueryRowContext(context.Background(), `SELECT current_database(), current_user,
 (SELECT r.rolname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  JOIN pg_catalog.pg_roles r ON r.oid=c.relowner WHERE n.nspname='public' AND c.relname='orgs')`).
		Scan(&database, &appRole, &owner); err != nil {
		t.Fatalf("resolve inventory installation roles: %v", err)
	}
	if appRole == owner {
		t.Fatalf("split-owner fixture resolved app=%q owner=%q", appRole, owner)
	}
	return store.PgProvisionSpec{Database: database, App: store.PgRole{Name: appRole}, Owner: store.PgRole{Name: owner}, InstallDirectoryInventory: true}
}

func TestDirectoryEpochPostgresMaxOneRejectsInheritedGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pg := isolatedPG(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 1,
	}, nil)
	if err != nil {
		t.Fatalf("open single-role MaxConns=1 store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// A single-role estate with no AdminDSN and no closed inventory routine cannot
	// enumerate anything: the staged-legacy status names the missing routine as
	// the reason and claims no authority, no counts and no coverage.
	directoryEpochTestWantStatus(t, st, store.DirectoryStatus{
		EpochCoverageComplete:      false,
		ControlMode:                store.DirectoryControlStaged,
		WriterPosture:              store.DirectoryWriterSingleRoleCapability,
		ExpectedGeneration:         1,
		CoverageProtocol:           coverageProtocolLegacy,
		InventoryUnavailableReason: "closed_routine_missing",
	})
	tenant := provisionTenant(t, st, "directory-pg-guc")
	ss := st.(*sqlStore)
	if _, err := ss.db.ExecContext(ctx, `UPDATE public.directory_writer_control
SET mode = 'enforced', expected_generation = 1`); err != nil {
		t.Fatalf("enforce generation for contamination probe: %v", err)
	}
	contaminate := `SELECT
pg_catalog.set_config('app.tenant_id', $1, false),
pg_catalog.set_config('app.directory_writer_generation', '1', false)`
	if _, err := ss.db.ExecContext(ctx, contaminate, tenant.String()); err != nil {
		t.Fatalf("contaminate the only pooled session: %v", err)
	}

	called := false
	err = st.System(ctx, func(store.SystemScope) error {
		called = true
		return nil
	})
	if called || !errors.Is(err, errDirectoryWriterControlInvalid) {
		t.Fatalf("System inherited generation: called=%t err=%v", called, err)
	}
	var inherited string
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COALESCE(pg_catalog.current_setting($1, true), '')",
		dialect.DirectoryWriterGenerationGUC).Scan(&inherited); err != nil {
		t.Fatalf("read inherited generation after refusal: %v", err)
	}
	if inherited != "1" {
		t.Fatalf("refusal unexpectedly hid session contaminant %q", inherited)
	}
	// The control row is still membership-union-v1: this fixture never ran the
	// ceremony. Present that independently known protocol with the inherited
	// generation; do not copy a live control-row string through a normal writer.
	if _, err := ss.db.ExecContext(ctx,
		"SELECT pg_catalog.set_config($1, $2, false)",
		directoryCoverageProtocolGUC, coverageProtocolLegacy,
	); err != nil {
		t.Fatalf("present fixture coverage protocol: %v", err)
	}
	result, err := ss.db.ExecContext(ctx, `UPDATE public.orgs
SET status = 'suspended' WHERE id = $1 AND tenant_id = $2`,
		tenant.String(), tenant.String())
	if err != nil {
		t.Fatalf("single-role raw write did not demonstrate inherited capability: %v", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("single-role raw write affected %d rows, want 1", rows)
	}
	if _, err := ss.db.ExecContext(ctx, `UPDATE public.orgs
SET status = 'active' WHERE id = $1 AND tenant_id = $2`,
		tenant.String(), tenant.String()); err != nil {
		t.Fatalf("restore raw org status: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx, `SELECT
pg_catalog.set_config('app.tenant_id', '', false),
pg_catalog.set_config('app.directory_writer_generation', '', false),
pg_catalog.set_config('app.directory_coverage_protocol', '', false)`); err != nil {
		t.Fatalf("clear session contamination: %v", err)
	}

	tx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin SYSTEM presentation probe: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	state, err := acquireDirectoryWriter(ctx, tx, ss.dia)
	if err != nil {
		t.Fatalf("acquire clean writer: %v", err)
	}
	if err := bindDirectoryTenant(ctx, tx, ss.dia, tenant); err != nil {
		t.Fatalf("bind real tenant: %v", err)
	}
	if err := armDirectoryWriter(ctx, tx, ss.dia, state); err != nil {
		t.Fatalf("arm generation: %v", err)
	}
	if err := restoreSystemDirectoryBaseline(ctx, tx, ss.dia); err != nil {
		t.Fatalf("restore SYSTEM presentation: %v", err)
	}
	var boundTenant, generation string
	if err := tx.QueryRowContext(ctx, `SELECT
COALESCE(pg_catalog.current_setting('app.tenant_id', true), ''),
COALESCE(pg_catalog.current_setting('app.directory_writer_generation', true), '')`).
		Scan(&boundTenant, &generation); err != nil {
		t.Fatalf("read restored presentation: %v", err)
	}
	if boundTenant != model.SystemTenantID.String() || generation != "" {
		t.Fatalf("restored presentation tenant=%q generation=%q", boundTenant, generation)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback presentation probe: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx,
		"UPDATE public.directory_writer_control SET mode = 'staged'"); err != nil {
		t.Fatalf("restore staged control: %v", err)
	}
}

func TestDirectoryEpochPostgresLockOrderInterleavings(t *testing.T) {
	// The fixture — an isolated database, an Open with the compiled migration plan, a tenant
	// provision, a seeded identity and the held epoch delete — runs on an unbounded context.
	// The 15 s budget starts at the reader race below, which is the only thing it is meant
	// to bound; above the fixture it was measuring the migrations, and on a contended runner
	// under -race the wait reported `context deadline exceeded`. Same class and same remedy
	// as 01f81b8e81, 4859cc43f3 and 346bce0c8a.
	setupCtx := context.Background()
	pg := isolatedPG(t)
	st, err := Open(setupCtx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open lock-order store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// The reader must lock epoch(5) before taking Identity's SHARE predicate
	// lock. Hold the epoch row as DropTenant would, start the reader, and prove
	// RowExclusive(identity) remains immediately available rather than cycling.
	tenant := provisionTenant(t, st, "directory-lock-epoch")
	var identity model.Identity
	if err := st.Mutate(setupCtx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(setupCtx, model.Identity{
			Name: "epoch lock identity", Kind: "service", ExternalID: "lock:epoch",
		})
		return err
	}); err != nil {
		t.Fatalf("seed epoch lock identity: %v", err)
	}
	epoch := directoryWriterTestEpoch(t, st, tenant)
	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw lock-order pool: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	holder, err := raw.BeginTx(setupCtx, nil)
	if err != nil {
		t.Fatalf("begin epoch holder: %v", err)
	}
	pgDia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(setupCtx, holder, pgDia, tenant); err != nil {
		t.Fatalf("bind epoch holder: %v", err)
	}
	if _, err := holder.ExecContext(setupCtx,
		"DELETE FROM public.core_directory_epoch WHERE tenant_id = $1", tenant.String()); err != nil {
		t.Fatalf("hold epoch delete: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	readerStarted := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		readerDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			close(readerStarted)
			return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx,
				[]store.AuthorizationFactRef{
					{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: epoch.Version},
					{Kind: identityDescriptor.Kind, ID: identity.ID, Version: identity.Version},
				})
		})
	}()
	<-readerStarted
	select {
	case err := <-readerDone:
		t.Fatalf("epoch reader did not wait on held order-5 row: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := holder.ExecContext(ctx, "SET LOCAL lock_timeout = '500ms'"); err != nil {
		t.Fatalf("set identity table lock timeout: %v", err)
	}
	if _, err := holder.ExecContext(ctx,
		"LOCK TABLE ONLY public.identities IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatalf("reader took Identity SHARE before epoch: %v", err)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatalf("rollback epoch holder: %v", err)
	}
	if err := <-readerDone; err != nil {
		t.Fatalf("epoch-first authority reader: %v", err)
	}

	// The K2-only Identity→Agent snapshot interleaves with the real DropTenant.
	// Pause Drop after Identity(10); the snapshot must wait at SHARE and may not
	// acquire Agent(20) in the opposite direction.
	k2Tenant := provisionTenant(t, st, "directory-lock-k2")
	var k2Identity model.Identity
	var k2Agent model.Agent
	if err := st.Mutate(ctx, k2Tenant, func(sc store.Scope) error {
		var err error
		k2Identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "k2 identity", Kind: "service", ExternalID: "lock:k2",
		})
		if err != nil {
			return err
		}
		k2Agent, err = sc.Agents().Create(ctx, model.Agent{
			Name: "k2 agent", Kind: "test", IdentityID: k2Identity.ID,
			Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed K2 lock facts: %v", err)
	}
	identityDeleted := make(chan struct{})
	releaseDrop := make(chan struct{})
	var identityOnce sync.Once
	tenantDropAfterAuthorizationFactTestHook = func(kind model.Kind) error {
		if kind == identityDescriptor.Kind {
			identityOnce.Do(func() { close(identityDeleted) })
			select {
			case <-releaseDrop:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	t.Cleanup(func() { tenantDropAfterAuthorizationFactTestHook = nil })
	dropDone := make(chan error, 1)
	go func() {
		dropDone <- st.System(ctx, func(sys store.SystemScope) error {
			return sys.DropTenant(ctx, k2Tenant)
		})
	}()
	select {
	case <-identityDeleted:
	case err := <-dropDone:
		t.Fatalf("DropTenant ended before Identity checkpoint: %v", err)
	case <-ctx.Done():
		t.Fatalf("DropTenant did not reach Identity checkpoint: %v", ctx.Err())
	}
	k2Started := make(chan struct{})
	k2CallbackEntered := make(chan struct{})
	k2Done := make(chan error, 1)
	go func() {
		close(k2Started)
		k2Done <- st.Mutate(ctx, k2Tenant, func(sc store.Scope) error {
			close(k2CallbackEntered)
			return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx,
				[]store.AuthorizationFactRef{
					{Kind: identityDescriptor.Kind, ID: k2Identity.ID, Version: k2Identity.Version},
					{Kind: agentDescriptor.Kind, ID: k2Agent.ID, Version: k2Agent.Version},
				})
		})
	}()
	<-k2Started
	select {
	case err := <-k2Done:
		t.Fatalf("K2 snapshot crossed Drop's authority exclusion: %v", err)
	case <-k2CallbackEntered:
		t.Fatal("Mutate callback entered while System held lineage exclusion")
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseDrop)
	if err := <-dropDone; err != nil {
		t.Fatalf("ordered DropTenant: %v", err)
	}
	if err := <-k2Done; !errors.Is(err, store.ErrNotFound) ||
		strings.Contains(strings.ToLower(err.Error()), "deadlock") {
		t.Fatalf("K2 snapshot after ordered Drop err = %v, want non-deadlock ErrNotFound", err)
	}
	tenantDropAfterAuthorizationFactTestHook = nil
}

func TestDirectoryEpochPostgresDropPurgesClosedAuthEstate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 1,
	}, nil)
	if err != nil {
		t.Fatalf("open PostgreSQL auth-estate store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	directoryEpochTestExerciseAuthEstateDrop(t, st)
}

func TestDirectoryEpochPostgresLifecyclePathsShareGlobalWriterLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open lifecycle-lock store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	missing := provisionTenant(t, st, "directory-global-lock-backfill")
	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw lifecycle-lock pool: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	directoryEpochTestDeletePostgresRow(t, raw, missing)

	backfillPaused := make(chan struct{})
	releaseBackfill := make(chan struct{})
	var backfillOnce sync.Once
	directoryEpochBeforeInsertTestHook = func(tenant model.TenantID) error {
		if tenant != missing {
			return nil
		}
		backfillOnce.Do(func() { close(backfillPaused) })
		select {
		case <-releaseBackfill:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { directoryEpochBeforeInsertTestHook = nil })
	type openResult struct {
		store store.Store
		err   error
	}
	backfillDone := make(chan openResult, 1)
	go func() {
		reopened, err := Open(ctx, cfg, nil)
		backfillDone <- openResult{store: reopened, err: err}
	}()
	select {
	case <-backfillPaused:
	case result := <-backfillDone:
		if result.store != nil {
			_ = result.store.Close()
		}
		t.Fatalf("authoritative backfill ended before pause: %v", result.err)
	case <-ctx.Done():
		t.Fatalf("authoritative backfill did not reach pause: %v", ctx.Err())
	}

	createDuringBackfill := make(chan error, 1)
	go func() {
		createDuringBackfill <- st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.CreateOrg(ctx, model.Org{
				Name: "created after backfill lock", Slug: "directory-lock-after-backfill",
				Status: model.StatusActive,
			})
			return err
		})
	}()
	select {
	case err := <-createDuringBackfill:
		t.Fatalf("CreateOrg crossed held backfill writer lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseBackfill)
	result := <-backfillDone
	if result.err != nil {
		t.Fatalf("release authoritative backfill: %v", result.err)
	}
	if result.store == nil {
		t.Fatal("authoritative backfill returned nil store")
	}
	if err := result.store.Close(); err != nil {
		t.Fatalf("close authoritative backfill store: %v", err)
	}
	if err := <-createDuringBackfill; err != nil {
		t.Fatalf("CreateOrg after backfill commit: %v", err)
	}
	directoryEpochBeforeInsertTestHook = nil

	victim := provisionTenant(t, st, "directory-global-lock-drop")
	if err := st.Mutate(ctx, victim, func(sc store.Scope) error {
		_, err := sc.Identities().Create(ctx, model.Identity{
			Name: "global lock identity", Kind: "service", ExternalID: "global:lock",
		})
		return err
	}); err != nil {
		t.Fatalf("seed lifecycle-lock victim: %v", err)
	}
	dropPaused := make(chan struct{})
	releaseDrop := make(chan struct{})
	var dropOnce sync.Once
	tenantDropAfterAuthorizationFactTestHook = func(kind model.Kind) error {
		if kind != identityDescriptor.Kind {
			return nil
		}
		dropOnce.Do(func() { close(dropPaused) })
		select {
		case <-releaseDrop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { tenantDropAfterAuthorizationFactTestHook = nil })
	dropDone := make(chan error, 1)
	go func() {
		dropDone <- st.System(ctx, func(sys store.SystemScope) error {
			return sys.DropTenant(ctx, victim)
		})
	}()
	select {
	case <-dropPaused:
	case err := <-dropDone:
		t.Fatalf("DropTenant ended before writer-lock pause: %v", err)
	case <-ctx.Done():
		t.Fatalf("DropTenant did not reach writer-lock pause: %v", ctx.Err())
	}
	createDuringDrop := make(chan error, 1)
	go func() {
		createDuringDrop <- st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.CreateOrg(ctx, model.Org{
				Name: "created after drop lock", Slug: "directory-lock-after-drop",
				Status: model.StatusActive,
			})
			return err
		})
	}()
	select {
	case err := <-createDuringDrop:
		t.Fatalf("CreateOrg crossed held DropTenant writer lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseDrop)
	if err := <-dropDone; err != nil {
		t.Fatalf("release DropTenant: %v", err)
	}
	if err := <-createDuringDrop; err != nil {
		t.Fatalf("CreateOrg after DropTenant commit: %v", err)
	}
	tenantDropAfterAuthorizationFactTestHook = nil
}

func directoryEpochTestReadPostgresRow(
	t *testing.T,
	db *sql.DB,
	tenant model.TenantID,
) (directoryEpochTestRow, bool) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL epoch read: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(context.Background(), tx, dia, tenant); err != nil {
		t.Fatalf("bind PostgreSQL epoch read: %v", err)
	}
	var row directoryEpochTestRow
	err = tx.QueryRowContext(context.Background(), `SELECT
id, tenant_id, created_at, updated_at, version
FROM public.core_directory_epoch WHERE tenant_id = $1`, tenant.String()).Scan(
		&row.id, &row.rowTenant, &row.createdAt, &row.updatedAt, &row.version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return directoryEpochTestRow{}, false
	}
	if err != nil {
		t.Fatalf("read PostgreSQL directory epoch %s: %v", tenant, err)
	}
	return row, true
}

func directoryEpochTestDeletePostgresRow(t *testing.T, db *sql.DB, tenant model.TenantID) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL epoch delete: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(context.Background(), tx, dia, tenant); err != nil {
		t.Fatalf("bind PostgreSQL epoch delete: %v", err)
	}
	result, err := tx.ExecContext(context.Background(),
		"DELETE FROM public.core_directory_epoch WHERE tenant_id = $1", tenant.String())
	if err != nil {
		t.Fatalf("delete PostgreSQL epoch: %v", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("delete PostgreSQL epoch affected %d rows, want 1", rows)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit PostgreSQL epoch delete: %v", err)
	}
}
