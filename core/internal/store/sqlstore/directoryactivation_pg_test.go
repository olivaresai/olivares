// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDirectoryActivationPostgresRejectsAssumedRoleDSNsAtBoot(t *testing.T) {
	t.Run("owner login assumes application role", func(t *testing.T) {
		ctx := context.Background()
		pg := isolatedPGSplit(t)
		appRole := pg.Result.AppPosture.Role
		ownerRole := pg.Result.OwnerPosture.Role
		directoryActivationTestGrantRole(t, pg.Superuser, appRole, ownerRole)
		assumedApp := directoryActivationTestAssumedRoleDSN(t, pg.Owner, appRole)
		directoryActivationTestProveLoginAuthorityRecovery(
			t, assumedApp, ownerRole, appRole, false, true,
		)
		_, err := Open(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: assumedApp, OwnerDSN: pg.Owner,
			AdminDSN: pg.Admin, MaxConns: 4, AllowPrivilegedRole: true,
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "session_user") ||
			!strings.Contains(err.Error(), "current_user") {
			t.Fatalf("Open with owner login assuming app error = %v", err)
		}
		directoryActivationTestWantPostgresRelationAbsent(t, pg.Superuser)
	})

	t.Run("superuser login assumes owner role", func(t *testing.T) {
		ctx := context.Background()
		pg := isolatedPGSplit(t)
		ownerRole := pg.Result.OwnerPosture.Role
		assumedOwner := directoryActivationTestAssumedRoleDSN(t, pg.Superuser, ownerRole)
		directoryActivationTestProveLoginAuthorityRecovery(
			t, assumedOwner, directoryActivationTestDSNUser(t, pg.Superuser), ownerRole, true, false,
		)
		_, err := Open(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: assumedOwner,
			AdminDSN: pg.Admin, MaxConns: 4, AllowPrivilegedRole: true,
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "session_user") ||
			!strings.Contains(err.Error(), "current_user") {
			t.Fatalf("Open with superuser login assuming owner error = %v", err)
		}
		directoryActivationTestWantPostgresRelationAbsent(t, pg.Superuser)
	})

	t.Run("superuser login assumes admin role", func(t *testing.T) {
		ctx := context.Background()
		pg := isolatedPGSplit(t)
		adminRole := pg.Result.AdminPosture.Role
		assumedAdmin := directoryActivationTestAssumedRoleDSN(t, pg.Superuser, adminRole)
		directoryActivationTestProveLoginAuthorityRecovery(
			t, assumedAdmin, directoryActivationTestDSNUser(t, pg.Superuser), adminRole, true, false,
		)
		_, err := Open(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
			AdminDSN: assumedAdmin, MaxConns: 4, AllowPrivilegedRole: true,
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "session_user") ||
			!strings.Contains(err.Error(), "current_user") {
			t.Fatalf("Open with superuser login assuming admin error = %v", err)
		}
		directoryActivationTestWantPostgresRelationAbsent(t, pg.Superuser)
	})
}

func TestDirectoryActivationPostgresSingleAndSplit(t *testing.T) {
	tests := []struct {
		name    string
		isolate func(testing.TB) pgtest.DSNs
		posture store.DirectoryWriterPosture
	}{
		{
			name: "single-role capability", isolate: isolatedPG,
			posture: store.DirectoryWriterSingleRoleCapability,
		},
		{
			name: "split owner", isolate: isolatedPGSplit,
			posture: store.DirectoryWriterSplitOwner,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pg := tc.isolate(t)
			cfg := store.Config{
				Engine: store.EnginePostgres,
				DSN:    pg.App, AdminDSN: pg.Admin, MaxConns: 4,
			}
			if tc.posture == store.DirectoryWriterSplitOwner {
				cfg.OwnerDSN = pg.Owner
			}
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			tenant := provisionTenant(t, raw, "pg-activation-"+strings.ReplaceAll(tc.name, " ", "-"))
			legacyTarget := mustCreateAgent(t, raw, tenant, "before activation")
			cached, _, err := raw.(store.DirectoryStatuser).DirectoryStatus(ctx)
			if err != nil || cached.ControlMode != store.DirectoryControlStaged ||
				cached.ExpectedGeneration != 1 || cached.WriterPosture != tc.posture || cached.Enabled {
				t.Fatalf("cached status = %+v err=%v", cached, err)
			}

			before, after, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
			if err != nil {
				t.Fatalf("ActivateDirectoryWriter: %v", err)
			}
			if !changed || before != cached || after.Enabled ||
				after.ControlMode != store.DirectoryControlEnforced ||
				after.ExpectedGeneration != 2 || after.WriterPosture != tc.posture {
				t.Fatalf("activation before=%+v after=%+v changed=%t", before, after, changed)
			}
			stillCached, _, err := raw.(store.DirectoryStatuser).DirectoryStatus(ctx)
			if err != nil || stillCached != cached {
				t.Fatalf("activation rewrote cached boot witness: got=%+v err=%v want=%+v",
					stillCached, err, cached)
			}

			app, err := openPGPinnedToEngineSchema(pg.App, 2)
			if err != nil {
				t.Fatalf("open raw app: %v", err)
			}
			t.Cleanup(func() { _ = app.Close() })
			tx, err := app.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin raw old writer: %v", err)
			}
			dia, _ := dialect.New(store.EnginePostgres)
			if err := dia.BindTenant(ctx, tx, tenant); err != nil {
				_ = tx.Rollback()
				t.Fatalf("bind raw old writer: %v", err)
			}
			_, err = tx.ExecContext(ctx, `UPDATE public.agents SET name = name
WHERE id = $1 AND tenant_id = $2`, legacyTarget.ID.String(), tenant.String())
			_ = tx.Rollback()
			if err == nil || !strings.Contains(err.Error(), "directory writer generation required") {
				t.Fatalf("raw old writer after activation error = %v", err)
			}
			mustCreateAgent(t, raw, tenant, "after activation")
			directoryWriterTestWantPostgresGenerationBaseline(t, raw.(*sqlStore).db)

			_, retryAfter, retryChanged, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
			if err != nil || retryChanged || retryAfter.ExpectedGeneration != 2 || retryAfter.Enabled {
				t.Fatalf("activation retry after=%+v changed=%t err=%v",
					retryAfter, retryChanged, err)
			}
			if err := raw.Close(); err != nil {
				t.Fatalf("close activated store: %v", err)
			}
			reopened, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("reopen activated store: %v", err)
			}
			defer reopened.Close() //nolint:errcheck
			status, _, err := reopened.(store.DirectoryStatuser).DirectoryStatus(ctx)
			if err != nil || status.Enabled || status.ControlMode != store.DirectoryControlEnforced ||
				status.ExpectedGeneration != 2 || status.WriterPosture != tc.posture ||
				!status.EpochCoverageComplete {
				t.Fatalf("reopened status = %+v err=%v", status, err)
			}
		})
	}
}

func TestDirectoryActivationPostgresAdminAuthorityIsSelectOnly(t *testing.T) {
	t.Run("direct tombstone write", func(t *testing.T) {
		ctx := context.Background()
		pg := isolatedPGSplit(t)
		cfg := store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
			AdminDSN: pg.Admin, MaxConns: 4,
		}
		raw, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer raw.Close() //nolint:errcheck
		adminRole := pg.Result.AdminPosture.Role
		owner, err := sql.Open("pgx", pg.Owner)
		if err != nil {
			t.Fatalf("open owner: %v", err)
		}
		defer owner.Close() //nolint:errcheck
		if _, err := owner.ExecContext(ctx, "GRANT INSERT ON TABLE public."+
			userTombstoneDescriptor.Table+" TO "+quoteIdent(adminRole)); err != nil {
			t.Fatalf("grant admin tombstone INSERT: %v", err)
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil ||
			!strings.Contains(err.Error(), "not read-only") {
			t.Fatalf("activation with writable admin error = %v", err)
		}
		var stillGranted bool
		if err := owner.QueryRowContext(ctx,
			"SELECT pg_catalog.has_table_privilege($1, $2, 'INSERT')",
			adminRole, "public."+userTombstoneDescriptor.Table,
		).Scan(&stillGranted); err != nil || !stillGranted {
			t.Fatalf("refused activation rewrote direct admin grant: granted=%t err=%v",
				stillGranted, err)
		}
		directoryActivationTestWantPostgresControl(t, owner, directoryWriterStaged, 1)
	})

	t.Run("SET ROLE reaches tombstone writer", func(t *testing.T) {
		ctx := context.Background()
		pg := isolatedPGSplit(t)
		cfg := store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
			AdminDSN: pg.Admin, MaxConns: 4,
		}
		raw, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer raw.Close() //nolint:errcheck
		adminRole := pg.Result.AdminPosture.Role
		group := "olv_activation_admin_" + pgtest.Suffix(t)
		if !plainRoleIdent(adminRole) || !plainRoleIdent(group) {
			t.Fatalf("unsafe fixture roles admin=%q group=%q", adminRole, group)
		}
		super, err := sql.Open("pgx", pg.Superuser)
		if err != nil {
			t.Fatalf("open superuser: %v", err)
		}
		t.Cleanup(func() {
			_, _ = super.ExecContext(context.Background(),
				"REVOKE "+quoteIdent(group)+" FROM "+quoteIdent(adminRole))
			_, _ = super.ExecContext(context.Background(), "DROP OWNED BY "+quoteIdent(group))
			_, _ = super.ExecContext(context.Background(), "DROP ROLE "+quoteIdent(group))
			_ = super.Close()
		})
		for _, stmt := range []string{
			"CREATE ROLE " + quoteIdent(group) + " NOLOGIN",
			"ALTER ROLE " + quoteIdent(adminRole) + " NOINHERIT",
			"GRANT INSERT ON TABLE public." + directoryTombstoneDescriptor.Table + " TO " + quoteIdent(group),
			"GRANT " + quoteIdent(group) + " TO " + quoteIdent(adminRole),
		} {
			if _, err := super.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("prepare reachable admin authority %q: %v", stmt, err)
			}
		}
		admin, err := sql.Open("pgx", pg.Admin)
		if err != nil {
			t.Fatalf("open admin: %v", err)
		}
		defer admin.Close() //nolint:errcheck
		var inherited bool
		if err := admin.QueryRowContext(ctx,
			"SELECT pg_catalog.has_table_privilege($1, 'INSERT')",
			"public."+directoryTombstoneDescriptor.Table,
		).Scan(&inherited); err != nil {
			t.Fatalf("read direct admin privilege: %v", err)
		}
		if inherited {
			t.Fatal("NOINHERIT fixture unexpectedly conveyed the group INSERT directly")
		}
		if _, _, _, err := ActivateDirectoryWriter(ctx, raw, cfg, 1); err == nil ||
			!strings.Contains(err.Error(), "reachable from") {
			t.Fatalf("activation with reachable tombstone writer error = %v", err)
		}
		directoryActivationTestWantPostgresControl(t, super, directoryWriterStaged, 1)
	})
}

func TestDirectoryActivationPostgresPinsIdentityToConsumedWitnesses(t *testing.T) {
	ctx := context.Background()
	primary := isolatedPGSplit(t)
	foreign := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: primary.App, OwnerDSN: primary.Owner,
		AdminDSN: primary.Admin, MaxConns: 4,
	}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Open primary: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	ss := raw.(*sqlStore)

	t.Run("admin", func(t *testing.T) {
		foreignAdmin, err := openPGPinnedToEngineSchema(foreign.Admin, 2)
		if err != nil {
			t.Fatalf("open foreign admin: %v", err)
		}
		goodAdmin := ss.adminDB
		ss.adminDB = foreignAdmin
		_, _, _, activateErr := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		ss.adminDB = goodAdmin
		_ = foreignAdmin.Close()
		if activateErr == nil || !strings.Contains(activateErr.Error(), "does not address the owner database") {
			t.Fatalf("foreign admin activation error = %v", activateErr)
		}
	})

	t.Run("application", func(t *testing.T) {
		foreignApp, err := openPGPinnedToEngineSchema(foreign.App, 2)
		if err != nil {
			t.Fatalf("open foreign app: %v", err)
		}
		goodApp := ss.db
		ss.db = foreignApp
		_, _, _, activateErr := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		ss.db = goodApp
		_ = foreignApp.Close()
		if activateErr == nil || !strings.Contains(activateErr.Error(), "does not address the owner database") {
			t.Fatalf("foreign app activation error = %v", activateErr)
		}
	})

	t.Run("application session search path", func(t *testing.T) {
		ss.db.SetMaxOpenConns(1)
		ss.db.SetMaxIdleConns(1)
		directoryActivationTestSetPoolSearchPath(t, ss.db, "pg_catalog")
		_, _, _, activateErr := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		directoryActivationTestSetPoolSearchPath(t, ss.db, dialect.EngineSchema)
		if activateErr == nil || !strings.Contains(activateErr.Error(), "search_path") ||
			!strings.Contains(activateErr.Error(), "application") {
			t.Fatalf("contaminated application search_path activation error = %v", activateErr)
		}
	})

	t.Run("admin session search path", func(t *testing.T) {
		ss.adminDB.SetMaxOpenConns(1)
		ss.adminDB.SetMaxIdleConns(1)
		directoryActivationTestSetPoolSearchPath(t, ss.adminDB, "pg_catalog")
		_, _, _, activateErr := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		directoryActivationTestSetPoolSearchPath(t, ss.adminDB, dialect.EngineSchema)
		if activateErr == nil || !strings.Contains(activateErr.Error(), "search_path") ||
			!strings.Contains(activateErr.Error(), "admin") {
			t.Fatalf("contaminated admin search_path activation error = %v", activateErr)
		}
	})
	owner, err := sql.Open("pgx", primary.Owner)
	if err != nil {
		t.Fatalf("open primary owner: %v", err)
	}
	defer owner.Close() //nolint:errcheck
	directoryActivationTestWantPostgresControl(t, owner, directoryWriterStaged, 1)
}

func TestDirectoryActivationPostgresRejectsOwnerDSNDifferentDatabaseSameRole(t *testing.T) {
	ctx := context.Background()
	primary := isolatedPGSplit(t)
	foreign := isolatedPGSplit(t)
	primaryCfg := store.Config{
		Engine: store.EnginePostgres, DSN: primary.App, OwnerDSN: primary.Owner,
		AdminDSN: primary.Admin, MaxConns: 4,
	}
	primaryRaw, err := Open(ctx, primaryCfg, nil)
	if err != nil {
		t.Fatalf("Open primary: %v", err)
	}
	defer primaryRaw.Close() //nolint:errcheck
	foreignCfg := store.Config{
		Engine: store.EnginePostgres, DSN: foreign.App, OwnerDSN: foreign.Owner,
		AdminDSN: foreign.Admin, MaxConns: 4,
	}
	foreignRaw, err := Open(ctx, foreignCfg, nil)
	if err != nil {
		t.Fatalf("Open foreign: %v", err)
	}
	if err := foreignRaw.Close(); err != nil {
		t.Fatalf("close foreign before ownership transfer: %v", err)
	}

	primaryOwner := primary.Result.OwnerPosture.Role
	foreignOwner := foreign.Result.OwnerPosture.Role
	if !plainRoleIdent(primaryOwner) || !plainRoleIdent(foreignOwner) ||
		!plainRoleIdent(foreign.Database) {
		t.Fatalf("unsafe owner/database fixture primary=%q foreign=%q database=%q",
			primaryOwner, foreignOwner, foreign.Database)
	}
	foreignSuper, err := sql.Open("pgx", foreign.Superuser)
	if err != nil {
		t.Fatalf("open foreign superuser: %v", err)
	}
	defer foreignSuper.Close() //nolint:errcheck
	for _, statement := range []string{
		"REASSIGN OWNED BY " + quoteIdent(foreignOwner) + " TO " + quoteIdent(primaryOwner),
		"ALTER DATABASE " + quoteIdent(foreign.Database) + " OWNER TO " + quoteIdent(primaryOwner),
		"GRANT CONNECT ON DATABASE " + quoteIdent(foreign.Database) + " TO " + quoteIdent(primaryOwner),
	} {
		if _, err := foreignSuper.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare same-owner foreign database %q: %v", statement, err)
		}
	}
	wrongCfg := primaryCfg
	wrongCfg.OwnerDSN = directoryActivationTestDSNForDatabase(
		t, primary.Owner, foreign.Database,
	)
	_, _, changed, activateErr := ActivateDirectoryWriter(ctx, primaryRaw, wrongCfg, 1)
	if activateErr == nil || changed ||
		!strings.Contains(activateErr.Error(), "does not address the owner database") {
		t.Fatalf("cross-database same-owner activation changed=%t error=%v", changed, activateErr)
	}
	primaryOwnerDB, err := sql.Open("pgx", primary.Owner)
	if err != nil {
		t.Fatalf("open primary owner control witness: %v", err)
	}
	defer primaryOwnerDB.Close() //nolint:errcheck
	directoryActivationTestWantPostgresControl(
		t, primaryOwnerDB, directoryWriterStaged, 1,
	)
	directoryActivationTestWantPostgresControl(
		t, foreignSuper, directoryWriterStaged, 1,
	)
}

func TestDirectoryActivationPostgresDrainsOldWriterBeforeAdminSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	app, err := openPGPinnedToEngineSchema(pg.App, 2)
	if err != nil {
		t.Fatalf("open old app: %v", err)
	}
	defer app.Close() //nolint:errcheck
	oldTx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin old writer: %v", err)
	}
	defer oldTx.Rollback() //nolint:errcheck
	tenant := model.NewTenantID()
	dia, _ := dialect.New(store.EnginePostgres)
	if err := dia.BindTenant(ctx, oldTx, tenant); err != nil {
		t.Fatalf("bind old writer: %v", err)
	}
	now := model.NewTimestamp(time.Now().UTC()).String()
	if _, err := oldTx.ExecContext(ctx, `INSERT INTO public.orgs
(id, tenant_id, created_at, updated_at, version, name, slug, status, settings, data_region)
VALUES ($1, $1, $2, $2, 1, 'old writer org', $3, 'active', NULL, NULL)`,
		tenant.String(), now, "old-writer-"+pgtest.Suffix(t)); err != nil {
		t.Fatalf("old writer insert org: %v", err)
	}

	type activationResult struct {
		changed bool
		err     error
	}
	done := make(chan activationResult, 1)
	go func() {
		_, _, changed, err := ActivateDirectoryWriter(ctx, raw, cfg, 1)
		done <- activationResult{changed: changed, err: err}
	}()

	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open lock observer: %v", err)
	}
	defer super.Close() //nolint:errcheck
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		select {
		case got := <-done:
			t.Fatalf("activation returned before old writer drained: changed=%t err=%v", got.changed, got.err)
		case <-deadline.C:
			t.Fatal("activation never waited for SHARE on public.orgs")
		case <-ticker.C:
			if err := super.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_catalog.pg_locks
WHERE relation = 'public.orgs'::pg_catalog.regclass
  AND mode = 'ShareLock' AND NOT granted)`).Scan(&waiting); err != nil {
				t.Fatalf("observe source SHARE waiter: %v", err)
			}
		}
	}
	if err := oldTx.Commit(); err != nil {
		t.Fatalf("commit drained old writer: %v", err)
	}
	select {
	case got := <-done:
		if got.changed || !errors.Is(got.err, store.ErrDirectoryUnavailable) ||
			!strings.Contains(got.err.Error(), "coverage mismatch") {
			t.Fatalf("activation after old writer commit changed=%t err=%v", got.changed, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("activation did not finish after old writer committed")
	}
	owner, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open owner control reader: %v", err)
	}
	defer owner.Close() //nolint:errcheck
	directoryActivationTestWantPostgresControl(t, owner, directoryWriterStaged, 1)
}

func directoryActivationTestWantPostgresControl(
	t *testing.T,
	db *sql.DB,
	mode directoryWriterMode,
	generation int64,
) {
	t.Helper()
	var gotMode string
	var gotGeneration int64
	if err := db.QueryRowContext(context.Background(), `SELECT mode, expected_generation
FROM public.directory_writer_control`).Scan(&gotMode, &gotGeneration); err != nil {
		t.Fatalf("read PostgreSQL directory writer control: %v", err)
	}
	if gotMode != string(mode) || gotGeneration != generation {
		t.Fatalf("PostgreSQL directory writer control = %s/%d, want %s/%d",
			gotMode, gotGeneration, mode, generation)
	}
}

func directoryActivationTestAssumedRoleDSN(
	t *testing.T,
	loginDSN string,
	role string,
) string {
	t.Helper()
	if !plainRoleIdent(role) {
		t.Fatalf("unsafe assumed-role fixture target %q", role)
	}
	u, err := url.Parse(loginDSN)
	if err != nil {
		t.Fatalf("parse assumed-role fixture DSN: %v", err)
	}
	query := u.Query()
	query.Set("options", "-c role="+role)
	u.RawQuery = query.Encode()
	return u.String()
}

func directoryActivationTestDSNForDatabase(
	t *testing.T,
	dsn string,
	database string,
) string {
	t.Helper()
	if !plainRoleIdent(database) {
		t.Fatalf("unsafe database fixture name %q", database)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse database-rewrite fixture DSN: %v", err)
	}
	u.Path = "/" + database
	return u.String()
}

func directoryActivationTestDSNUser(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse fixture DSN user: %v", err)
	}
	if u.User == nil || u.User.Username() == "" {
		t.Fatal("fixture DSN has no login user")
	}
	return u.User.Username()
}

func directoryActivationTestGrantRole(
	t *testing.T,
	superuserDSN string,
	grantedRole string,
	memberRole string,
) {
	t.Helper()
	if !plainRoleIdent(grantedRole) || !plainRoleIdent(memberRole) {
		t.Fatalf("unsafe assumed-role membership fixture roles granted=%q member=%q",
			grantedRole, memberRole)
	}
	super, err := sql.Open("pgx", superuserDSN)
	if err != nil {
		t.Fatalf("open assumed-role fixture superuser: %v", err)
	}
	if _, err := super.ExecContext(context.Background(),
		"GRANT "+quoteIdent(grantedRole)+" TO "+quoteIdent(memberRole),
	); err != nil {
		_ = super.Close()
		t.Fatalf("grant assumed-role fixture membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.ExecContext(context.Background(),
			"REVOKE "+quoteIdent(grantedRole)+" FROM "+quoteIdent(memberRole))
		_ = super.Close()
	})
}

func directoryActivationTestProveLoginAuthorityRecovery(
	t *testing.T,
	dsn string,
	wantSession string,
	wantAssumed string,
	wantSuperuser bool,
	wantDatabaseOwner bool,
) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open assumed-role proof connection: %v", err)
	}
	defer db.Close() //nolint:errcheck
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin assumed-role proof connection: %v", err)
	}
	defer conn.Close() //nolint:errcheck
	var sessionUser, currentUser string
	if err := conn.QueryRowContext(context.Background(),
		"SELECT session_user, current_user",
	).Scan(&sessionUser, &currentUser); err != nil {
		t.Fatalf("read assumed-role identities: %v", err)
	}
	if sessionUser != wantSession || currentUser != wantAssumed || sessionUser == currentUser {
		t.Fatalf("assumed-role identities session=%q current=%q want session=%q current=%q",
			sessionUser, currentUser, wantSession, wantAssumed)
	}
	// A startup `role` becomes RESET ROLE's reset target, but SET ROLE NONE
	// explicitly restores current_user to session_user. This proves the hidden
	// login authority is recoverable by the same connection without reconnecting.
	if _, err := conn.ExecContext(context.Background(), "SET ROLE NONE"); err != nil {
		t.Fatalf("SET ROLE NONE on adversarial connection: %v", err)
	}
	var resetUser string
	var superuser, databaseOwner bool
	if err := conn.QueryRowContext(context.Background(), `SELECT current_user,
role.rolsuper, database.datdba = role.oid
FROM pg_catalog.pg_roles AS role, pg_catalog.pg_database AS database
WHERE role.rolname = current_user AND database.datname = current_database()`).Scan(
		&resetUser, &superuser, &databaseOwner,
	); err != nil {
		t.Fatalf("read recovered login authority: %v", err)
	}
	if resetUser != wantSession || superuser != wantSuperuser || databaseOwner != wantDatabaseOwner {
		t.Fatalf("recovered login authority user=%q superuser=%t database_owner=%t, want %q/%t/%t",
			resetUser, superuser, databaseOwner,
			wantSession, wantSuperuser, wantDatabaseOwner)
	}
}

func directoryActivationTestWantPostgresRelationAbsent(t *testing.T, superuserDSN string) {
	t.Helper()
	db, err := sql.Open("pgx", superuserDSN)
	if err != nil {
		t.Fatalf("open relation absence witness: %v", err)
	}
	defer db.Close() //nolint:errcheck
	var relation sql.NullString
	if err := db.QueryRowContext(context.Background(),
		"SELECT pg_catalog.to_regclass('public.directory_writer_control')",
	).Scan(&relation); err != nil {
		t.Fatalf("read relation absence witness: %v", err)
	}
	if relation.Valid {
		t.Fatalf("assumed-role boot mutated schema: directory writer control=%q", relation.String)
	}
}

func directoryActivationTestSetPoolSearchPath(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin search_path fixture connection: %v", err)
	}
	defer conn.Close() //nolint:errcheck
	var got string
	if err := conn.QueryRowContext(context.Background(),
		"SELECT pg_catalog.set_config('search_path', $1, false)", path,
	).Scan(&got); err != nil {
		t.Fatalf("set search_path fixture to %q: %v", path, err)
	}
	if got != path {
		t.Fatalf("set search_path fixture returned %q, want %q", got, path)
	}
}
