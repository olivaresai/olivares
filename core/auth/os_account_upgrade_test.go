// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestOSAccountBindingUpgradePreservesWorkflowAndExactCredential(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true}
	checkOSAccountUpgrade(t, cfg)
}

func TestOSAccountBindingPostgresUpgradeAndImmutableOwnership(t *testing.T) {
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, Debug: true}
	f, b, native := checkOSAccountUpgrade(t, cfg)
	admin := osAdminSession(f)
	ceremony, err := b.Begin(f.deadline(), admin, f.tenant, f.admin.ID, native.login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Complete(f.deadline(), admin, ceremony.ID, []byte("secret")); err == nil {
		t.Fatal("Postgres admitted foreign UID reservation")
	}
	if err = b.Revoke(f.deadline(), admin, f.tenant, f.userA.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Resolve(f.deadline(), native.uid, native.login); err == nil {
		t.Fatal("Postgres resolved revoked reservation")
	}
	subject, _, _ := f.session(f.userA, nil)
	ceremony, err = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, native.login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Complete(f.deadline(), subject, ceremony.ID, []byte("secret")); err == nil {
		t.Fatal("Postgres reopened permanent revoked reservation")
	}
}

func checkOSAccountUpgrade(t *testing.T, cfg store.Config) (*credentialBindingFixture, *OSAccountBindings, *osAccountNative) {
	t.Helper()
	f := newCredentialBindingFixtureConfig(t, cfg)
	principal, token, _ := f.session(f.userA, nil)
	subject := f.subject(model.NewID(), f.userA.ID)
	handle := f.mustBind(principal, subject)
	before := f.mustResolve(handle, subject)
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the v19 nullable-column shape and its contiguous tracking prefix.
	// Later additive schema is left in place; its migrations are idempotent. The
	// real workflow row, seal, credential and lifecycle history stay intact.
	driver, dsn := "sqlite", cfg.DSN
	if cfg.Engine == store.EnginePostgres {
		driver, dsn = "pgx", cfg.OwnerDSN
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DROP INDEX core_credential_bindings_os_uid_uniq",
		"DROP INDEX core_credential_bindings_os_account_uniq",
		"DROP INDEX core_credential_bindings_os_subject_uniq",
		"ALTER TABLE core_credential_bindings DROP COLUMN os_uid",
		"ALTER TABLE core_credential_bindings DROP COLUMN os_account",
		"DELETE FROM schema_migrations_core WHERE version>=20",
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlstore.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.st = reopened
	f.a = NewAuthenticator(reopened, nil)
	after := f.mustResolve(handle, subject)
	if pinnedRevision(before) != pinnedRevision(after) {
		t.Fatal("old workflow credential changed across upgrade")
	}
	current, err := f.a.Authenticate(f.ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	n := &osAccountNative{uid: 1501, login: "native-upgrade"}
	b := osBindings(f, n)
	c, err := b.Begin(f.deadline(), osAdminSession(f), f.tenant, f.userA.ID, n.login)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Complete(f.deadline(), current, c.ID, []byte("native-secret")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Resolve(f.deadline(), n.uid, n.login); err != nil {
		t.Fatal(err)
	}
	return f, b, n
}
