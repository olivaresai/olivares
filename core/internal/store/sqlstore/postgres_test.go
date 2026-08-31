// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestPostgresIntegration runs the core behavioral guarantees against a real
// Postgres, on an ISOLATED database provisioned for this test, and skips
// when no Postgres is configured. No Postgres is available in the default
// environment, so this is the path a CI job with a postgres service exercises.
// For the row-level-security backstop to be verified (not just the
// repository-level scoping), the role must be a non-superuser role (docs/SECURITY-HARDENING.md)
// — isolatedPG provisions exactly that.
//
// This test is why isolation was needed at all: it creates a SUPERADMIN USER
// below, and `users` lives in the global auth partition, so on the shared
// database it made every later /v1/setup in the workspace answer 409.
func TestPostgresIntegration(t *testing.T) {
	dsn := isolatedPG(t).App
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer st.Close()

	tenantA := provisionTenant(t, st, "pg-a-"+uniqueSuffix())
	tenantB := provisionTenant(t, st, "pg-b-"+uniqueSuffix())

	// CRUD + cross-tenant isolation.
	agentA := mustCreateAgent(t, st, tenantA, "pa")
	if err := st.View(ctx, tenantB, func(sc store.Scope) error {
		_, err := sc.Agents().Get(ctx, agentA.ID)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("pg isolation: err = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("pg view: %v", err)
	}

	// Hash chain.
	appendN(t, st, tenantA, 3)
	if err := st.View(ctx, tenantA, func(sc store.Scope) error {
		rep, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		if !rep.OK || rep.Checked != 4 { // provisioning + 3
			t.Fatalf("pg verify = %+v, want OK/4", rep)
		}
		return nil
	}); err != nil {
		t.Fatalf("pg audit view: %v", err)
	}

	// Must-fix #1, verified on the engine that actually enforces FORCE RLS:
	// the auth partition (system-tenant rows) is READABLE via AuthView/AuthMutate,
	// which bind SystemTenantID as a normal RLS scope. Doing this through the
	// GUC-clearing System path would match ZERO rows here and silently break login.
	email := "pg-" + uniqueSuffix() + "@example.com"
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.Users().Create(ctx, model.User{Email: email, Status: model.StatusActive, IsSuperadmin: true})
		return e
	}); err != nil {
		t.Fatalf("pg auth mutate: %v", err)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		users, _, e := as.Users().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "email", Op: model.OpEq, Value: email}},
		})
		if e != nil {
			return e
		}
		if len(users) != 1 {
			t.Fatalf("pg auth read under FORCE RLS = %d users, want 1 (must-fix #1)", len(users))
		}
		return nil
	}); err != nil {
		t.Fatalf("pg auth view: %v", err)
	}
	// A normal tenant scope cannot reach a credential table generically.
	if err := st.View(ctx, tenantA, func(sc store.Scope) error {
		if _, e := sc.Ext("core.user"); !errors.Is(e, store.ErrUnknownEntity) {
			t.Fatalf("pg Ext(core.user) = %v, want ErrUnknownEntity", e)
		}
		return nil
	}); err != nil {
		t.Fatalf("pg ext view: %v", err)
	}
}

func TestPostgresAuditAppendLockIncludesGlobalSpoolBudget(t *testing.T) {
	pg := isolatedPGSplit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin,
		MaxConns:           4,
		AuditSpoolMaxBytes: largeAuditSpoolBudget,
	}, nil)
	if err != nil {
		t.Fatalf("open postgres with audit spool budget: %v", err)
	}
	defer st.Close()
	tenantA := provisionTenant(t, st, "pg-audit-lock-a-"+uniqueSuffix())
	tenantB := provisionTenant(t, st, "pg-audit-lock-b-"+uniqueSuffix())

	firstLocked := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- st.Mutate(ctx, tenantA, func(sc store.Scope) error {
			locker, ok := sc.Audit().(store.AuditAppendLocker)
			if !ok {
				return errors.New("postgres audit log lacks append locker")
			}
			if err := locker.LockAppends(ctx); err != nil {
				return err
			}
			close(firstLocked)
			select {
			case <-releaseFirst:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-firstLocked:
	case err := <-firstDone:
		t.Fatalf("first audit append lock ended early: %v", err)
	case <-ctx.Done():
		t.Fatalf("first audit append lock timed out: %v", ctx.Err())
	}

	probeErr := st.Mutate(ctx, tenantB, func(sc store.Scope) error {
		log, ok := sc.Audit().(*auditLog)
		if !ok {
			return errors.New("postgres audit log has unexpected implementation")
		}
		var usage int64
		return log.tx.QueryRowContext(ctx,
			"SELECT bytes FROM "+log.relation(auditSpoolUsageTable)+
				" WHERE id = 1 FOR UPDATE NOWAIT",
		).Scan(&usage)
	})
	var pgErr *pgconn.PgError
	if !errors.As(probeErr, &pgErr) || pgErr.Code != sqlStateLockNotAvailable {
		t.Fatalf("second tenant spool NOWAIT error = %v, want SQLSTATE %s",
			probeErr, sqlStateLockNotAvailable)
	}
	release()
	if err := <-firstDone; err != nil {
		t.Fatalf("release first audit append lock: %v", err)
	}
}

func TestPostgresViewIsOneRepeatableReadSnapshot(t *testing.T) {
	dsn := isolatedPG(t).App
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, nil)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer st.Close()
	tenant := provisionTenant(t, st, "pg-view-snapshot-"+uniqueSuffix())

	err = st.View(ctx, tenant, func(sc store.Scope) error {
		tenantScoped, ok := sc.(*tenantScope)
		if !ok {
			return errors.New("postgres View did not return a SQL tenant scope")
		}
		var isolation, readOnly string
		if err := tenantScoped.tx.QueryRowContext(
			ctx, "SHOW transaction_isolation",
		).Scan(&isolation); err != nil {
			return err
		}
		if err := tenantScoped.tx.QueryRowContext(
			ctx, "SHOW transaction_read_only",
		).Scan(&readOnly); err != nil {
			return err
		}
		if isolation != "repeatable read" || readOnly != "on" {
			t.Fatalf("View transaction isolation/read-only = %q/%q, want repeatable read/on",
				isolation, readOnly)
		}

		before, _, err := sc.Agents().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		writerDone := make(chan error, 1)
		go func() {
			writerDone <- st.Mutate(ctx, tenant, func(writeScope store.Scope) error {
				_, createErr := writeScope.Agents().Create(ctx, model.Agent{
					Name: "interleaved", Status: model.StatusActive,
				})
				return createErr
			})
		}()
		if err := <-writerDone; err != nil {
			return err
		}
		after, _, err := sc.Agents().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(after) != len(before) {
			t.Fatalf("View mixed snapshots: first read=%d agents, second=%d", len(before), len(after))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("repeatable-read View: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		agents, _, err := sc.Agents().List(ctx, model.Query{})
		if err == nil && len(agents) != 1 {
			t.Fatalf("fresh View sees %d agents, want committed writer", len(agents))
		}
		return err
	}); err != nil {
		t.Fatalf("fresh View: %v", err)
	}
}

// TestPostgresRefusesPrivilegedRole is the adversarial check for the RLS-bypass
// boot guard (docs/SECURITY-HARDENING.md): connecting as a superuser/BYPASSRLS role would make the
// FORCE-RLS tenant backstop inert, so Open MUST refuse it. CI sets the superuser
// DSN to the `postgres` role; locally the test skips when it is unset.
func TestPostgresRefusesPrivilegedRole(t *testing.T) {
	// The superuser pointed at THIS test's own database: the AllowPrivilegedRole
	// leg below boots a full store as `postgres`, which would otherwise create
	// superuser-owned objects in a database other tests are using.
	dsn := isolatedPG(t).Superuser
	ctx := context.Background()
	_, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn}, registerWidget)
	if err == nil {
		t.Fatal("Open accepted a superuser/BYPASSRLS role: the RLS backstop would be inert (critical)")
	}
	if !strings.Contains(err.Error(), "BYPASSES row-level security") {
		t.Fatalf("Open failed but not with the RLS-bypass guard: %v", err)
	}

	// The explicit opt-out must let it through (single-tenant/dev escape hatch).
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, AllowPrivilegedRole: true}, registerWidget)
	if err != nil {
		t.Fatalf("AllowPrivilegedRole opt-out should permit a superuser role: %v", err)
	}
	_ = st.Close()
}

// TestPostgresRLSFunctionallyDenies proves FORCE row-level security actually
// DENIES a cross-tenant read at the database layer — independent of the
// repository's "AND tenant_id = ?" predicate. It inserts tenant A's row through
// the repo, then issues a RAW SELECT (no tenant predicate) with the GUC bound to
// tenant B and asserts zero rows; bound to tenant A it sees the row. This is the
// assertion that fails if the policy is ever weakened or bypassed.
func TestPostgresRLSFunctionallyDenies(t *testing.T) {
	dsn := isolatedPG(t).App // NOSUPERUSER NOBYPASSRLS, so the policy really applies
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer st.Close()

	tenantA := provisionTenant(t, st, "pg-rls-a-"+uniqueSuffix())
	tenantB := provisionTenant(t, st, "pg-rls-b-"+uniqueSuffix())
	agentA := mustCreateAgent(t, st, tenantA, "secret-agent")

	db := st.(*sqlStore).db // same-package access to the raw pool

	rawCount := func(tenant model.TenantID) int {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenant.String()); err != nil {
			t.Fatalf("set guc: %v", err)
		}
		// RAW query: NO "tenant_id = ?" predicate. Only RLS can filter here.
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE id = $1", agentA.ID.String()).Scan(&n); err != nil {
			t.Fatalf("raw count: %v", err)
		}
		return n
	}

	if n := rawCount(tenantB); n != 0 {
		t.Fatalf("RLS did NOT deny a cross-tenant raw read: tenant B saw %d of tenant A's rows (cross-tenant leak)", n)
	}
	if n := rawCount(tenantA); n != 1 {
		t.Fatalf("RLS over-denied: tenant A saw %d of its own rows, want 1", n)
	}
}

// TestPostgresAdminPoolCrossTenant proves the dedicated BYPASSRLS admin pool (R2)
// makes ListOrgs return every tenant on Postgres, where the RLS-scoped application
// pool cannot enumerate at all. Gated on a BYPASSRLS, non-superuser admin DSN; CI
// provisions olivares_admin (deploy/postgres/01-app-role.sql).
//
// Re-pinned the first half. It used to assert that the app pool returns an
// EMPTY LIST and a nil error, which is the defect written down as the contract: an
// empty slice reports "there are no tenants" in the same bytes as "I was not
// allowed to look", and two ceremonies read the first meaning and certified over
// what they never enumerated. The assertion is now the opposite and strictly
// stronger — the read must FAIL with store.ErrEnumerationNotAuthoritative — and the
// RLS behavior it originally covered (the query really does match nothing, never a
// cross-tenant leak) is still proven, through ListOrgsVisible below.
func TestPostgresAdminPoolCrossTenant(t *testing.T) {
	// One isolated database, two roles on it: the NOBYPASSRLS app role and the
	// BYPASSRLS admin role, which is the pairing under test.
	pg := isolatedPG(t)
	dsn, adminDSN := pg.App, pg.Admin
	ctx := context.Background()

	// Without the admin pool, cross-tenant ListOrgs cannot answer, and says so.
	stApp, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	provisionTenant(t, stApp, "pg-adm-a-"+uniqueSuffix())
	provisionTenant(t, stApp, "pg-adm-b-"+uniqueSuffix())
	var appErr error
	if err := stApp.System(ctx, func(sys store.SystemScope) error {
		_, appErr = sys.ListOrgs(ctx)
		return nil
	}); err != nil {
		t.Fatalf("System: %v", err)
	}
	if !errors.Is(appErr, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("app-pool ListOrgs err = %v, want ErrEnumerationNotAuthoritative (an RLS-limited read must NOT be reported as a complete estate)", appErr)
	}

	// The named exception still returns the rows and reports them as NOT
	// authoritative — and the row count is still 0, which is what proves the RLS
	// policy is doing its job rather than the error being a blanket refusal.
	appCount, appAuth := -1, true
	if err := stApp.System(ctx, func(sys store.SystemScope) error {
		orgs, ok, e := sys.ListOrgsVisible(ctx)
		appCount, appAuth = len(orgs), ok
		return e
	}); err != nil {
		t.Fatalf("app ListOrgsVisible: %v", err)
	}
	_ = stApp.Close()
	if appAuth {
		t.Fatal("app-pool ListOrgsVisible reported authoritative=true without a BYPASSRLS admin pool")
	}
	if appCount != 0 {
		t.Fatalf("app-pool ListOrgsVisible = %d rows, want 0 (RLS must limit cross-tenant reads on the app role)", appCount)
	}

	// With the admin pool, ListOrgs sees every tenant (the two just created + more).
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, AdminDSN: adminDSN, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer st.Close()
	adminCount := 0
	if err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, e := sys.ListOrgs(ctx)
		adminCount = len(orgs)
		return e
	}); err != nil {
		t.Fatalf("admin ListOrgs: %v", err)
	}
	if adminCount < 2 {
		t.Fatalf("admin-pool ListOrgs = %d, want >= 2 (the BYPASSRLS pool must see across tenants)", adminCount)
	}
	// Exercise the named visibility API as well: it may report authoritative only
	// after the live, exact-role posture and inventory query have completed in the
	// same repeatable-read/read-only AdminDSN transaction.
	visibleCount, visibleAuthoritative := 0, false
	if err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, authoritative, e := sys.ListOrgsVisible(ctx)
		visibleCount, visibleAuthoritative = len(orgs), authoritative
		return e
	}); err != nil {
		t.Fatalf("admin ListOrgsVisible snapshot: %v", err)
	}
	if !visibleAuthoritative || visibleCount < 2 {
		t.Fatalf("admin ListOrgsVisible = (%d,%t), want >=2/authoritative", visibleCount, visibleAuthoritative)
	}
}

// TestPostgresAdminPoolRefusesNonBypassRole proves Open REFUSES an --admin-dsn that
// points at a role which cannot bypass RLS: it would silently return an empty org
// list, so the engine fails loudly instead (R2-b).
func TestPostgresAdminPoolRefusesNonBypassRole(t *testing.T) {
	dsn := isolatedPG(t).App // the NOBYPASSRLS application role
	ctx := context.Background()
	_, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, AdminDSN: dsn}, registerWidget)
	if err == nil {
		t.Fatal("Open accepted a NOBYPASSRLS admin pool: cross-tenant reads would silently return empty")
	}
	if !strings.Contains(err.Error(), "cannot perform cross-tenant reads") {
		t.Fatalf("Open failed but not with the admin-pool guard: %v", err)
	}
}

// TestPostgresBootConvergesLegacyFederationAliases is the regression for the boot
// defect that made EVERY Postgres deployment unbootable between 2026-07-08 and this
// fix: reconcileCoreData ran its federation-alias backfill against an RLS-guarded
// table with NO tenant bound, and pgTenantGuard's policy calls current_setting
// WITHOUT missing_ok on purpose, so the statement raised
// `unrecognized configuration parameter "app.tenant_id"` (SQLSTATE 42704) and the
// store never opened. The whole Postgres suite failed on it, and a three-replica
// kind cluster crash-looped on it.
//
// federation_configs lives in the AUTH partition, so every row's tenant_id is
// SystemTenantID (authscope.go AuthView/AuthMutate pin it) and the per-IdP scope is
// the target_tenant_id COLUMN. The test therefore uses two SCOPES, not two tenants,
// and covers BOTH backfill statements: a scope holding two legacy rows (one is
// promoted to the reserved alias, the other must be given a unique dup- alias) and a
// scope holding one (promoted). Every write is restricted to this test's own scopes.
// (Since the test also owns its database outright, but the scoping is kept: the
// backfill under test is itself a global UPDATE, so asserting on scoped rows is what
// makes the assertion meaningful.)
//
// It asserts BOTH halves, because "boot no longer crashes" would also be satisfied by
// silently skipping the backfill — binding an EMPTY tenant makes the UPDATE match zero
// rows — and that trades a loud failure for a quiet data-integrity hole: an
// un-backfilled NULL alias is DISTINCT in the (tenant_id, target_tenant_id, alias)
// unique index, so it escapes the constraint the index exists to enforce.
func TestPostgresBootConvergesLegacyFederationAliases(t *testing.T) {
	dsn := isolatedPG(t).App
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}
	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}

	// Two scopes of this test's own, so nothing here touches another test's rows.
	pairScope := provisionTenant(t, st, "pg-fed-pair-"+uniqueSuffix())
	loneScope := provisionTenant(t, st, "pg-fed-lone-"+uniqueSuffix())

	mkConfig := func(scope model.TenantID, alias string) {
		t.Helper()
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			_, e := as.FederationConfigs().Create(ctx, model.FederationConfig{
				TargetTenantID: scope,
				Alias:          alias,
				Protocol:       "oidc",
				Status:         model.StatusActive,
				OIDCIssuer:     "https://idp.example",
				OIDCClientID:   "cid",
			})
			return e
		}); err != nil {
			t.Fatalf("create federation config (scope=%s alias=%q): %v", scope, alias, err)
		}
	}
	mkConfig(pairScope, model.DefaultFederationAlias)
	mkConfig(pairScope, "okta")
	mkConfig(loneScope, model.DefaultFederationAlias)

	// Age this test's rows to the PRE-U4 shape (alias NULL) — what an upgraded
	// database holds, and the only state the boot backfill exists to converge. The
	// write binds SystemTenantID because the guard demands a bind; the BOOT path is
	// the one that could not bind, which is the whole defect.
	inBoundTx := func(db *sql.DB, fn func(tx *sql.Tx)) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		if _, err := tx.ExecContext(ctx,
			"SELECT set_config('app.tenant_id', $1, true)", model.SystemTenantID.String()); err != nil {
			t.Fatalf("set guc: %v", err)
		}
		fn(tx)
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	inBoundTx(st.(*sqlStore).db, func(tx *sql.Tx) { // same-package access to the raw pool
		res, err := tx.ExecContext(ctx,
			"UPDATE federation_configs SET alias = NULL WHERE target_tenant_id IN ($1, $2)",
			pairScope.String(), loneScope.String())
		if err != nil {
			t.Fatalf("age the rows to the pre-U4 shape: %v", err)
		}
		// If this rewrites fewer than three rows the fixture is not what the
		// assertions below claim to measure, and the test would "pass" vacuously.
		if n, err := res.RowsAffected(); err != nil || n != 3 {
			t.Fatalf("aged %d rows (err=%v), want 3", n, err)
		}
	})
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Boot again. Before the fix this test never reached here — the FIRST Open above
	// already failed, as it did for every Postgres deployment and every test in this
	// file. After the fix, this second Open is the one that runs the backfill with
	// legacy rows actually present, which is what the assertions below measure.
	st2, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("re-open with legacy (alias NULL) rows present — the regression: %v", err)
	}
	defer st2.Close()

	// Read through the REOPENED store's pool: st.Close() above closed the first one.
	// Select the physical tenant_id too, so the auth-partition invariant the fix relies
	// on is ASSERTED here rather than asserted in a comment.
	aliases := func(scope model.TenantID) []string {
		t.Helper()
		var out []string
		inBoundTx(st2.(*sqlStore).db, func(tx *sql.Tx) {
			rows, err := tx.QueryContext(ctx,
				"SELECT tenant_id, coalesce(alias, '') FROM federation_configs WHERE target_tenant_id = $1 ORDER BY id", scope.String())
			if err != nil {
				t.Fatalf("read back aliases for %s: %v", scope, err)
			}
			defer rows.Close()
			for rows.Next() {
				var tenant, a string
				if err := rows.Scan(&tenant, &a); err != nil {
					t.Fatalf("scan alias: %v", err)
				}
				if tenant != model.SystemTenantID.String() {
					t.Fatalf("federation row for scope %s sits in physical tenant %s, want the auth partition %s — the backfill binds that partition, so a foreign-tenant row would be silently skipped",
						scope, tenant, model.SystemTenantID)
				}
				out = append(out, a)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows: %v", err)
			}
		})
		return out
	}

	// The lone legacy row is promoted to the reserved alias.
	if got := aliases(loneScope); len(got) != 1 || got[0] != model.DefaultFederationAlias {
		t.Errorf("single-row scope after the boot backfill = %v, want [%q] — boot stopped failing but the backfill did not run, so a NULL alias still escapes the unique index",
			got, model.DefaultFederationAlias)
	}
	// The two-row scope gets EXACTLY ONE default; the other is retained under a
	// unique dup- alias rather than lost or collided.
	got := aliases(pairScope)
	if len(got) != 2 {
		t.Fatalf("two-row scope after the boot backfill = %v, want 2 rows (no row may be lost)", got)
	}
	defaults, dups := 0, 0
	for _, a := range got {
		switch {
		case a == model.DefaultFederationAlias:
			defaults++
		case strings.HasPrefix(a, "dup-"):
			dups++
		}
	}
	if defaults != 1 || dups != 1 {
		t.Errorf("two-row scope aliases = %v, want exactly one %q and one dup-<id> (defaults=%d dups=%d)",
			got, model.DefaultFederationAlias, defaults, dups)
	}
}

// uniqueSuffix returns a short random slug suffix so repeated test runs do not
// collide on the unique org slug.
//
// this previously returned string(model.NewID())[:8]. model.NewID is a
// UUIDv7 (core/model/ids.go), whose first 8 hex characters are the TOP 32 bits of
// its 48-bit millisecond timestamp — so they are IDENTICAL for every call within
// the same ~65 second window. Measured: 1000 consecutive calls produced exactly
// one distinct value, still unchanged after a 2 second sleep. It was neither
// unique nor unguessable, and it silently rejoined the "isolated" databases in
// dbsetup_test.go onto one name.
func uniqueSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sqlstore test: read entropy: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
