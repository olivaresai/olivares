// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// THE POSTGRES DR LEG NEVER PROVISIONED THE ROLE MODEL IT SHIPS, and that is why
// a DR path that cannot run in the product's own least-privilege posture was
// green.
//
// newPGFixture (cmd_dr_postgres_test.go) hands every case the SUPERUSER DSN — a
// role the engine's own boot guard refuses — and every `runDR("restore", …)` case
// there deliberately passes `not-a-bundle.drbundle`, so the command fails before
// pg_restore and before the boot. Nothing in the suite ever asked `dr` to complete
// a Postgres restore, and nothing ever asked it to do so as the owner/app split
// that `olivares db init --owner-role` provisions.
//
// The cells below provision the three real roles through the product's OWN
// provisioning path and drive the real `dr backup` / `dr restore` commands
// end to end. They are the tests that fail on the pre-fix tree with
// `SQLSTATE 42501`.

// pgSplitFixture is a throwaway database provisioned with the least-privilege
// role model, through coreengine.ProvisionPostgres — the same code `olivares db
// init` runs. Provisioning it by hand here would test a hand-written copy of the
// grants instead of the ones the product actually applies.
type pgSplitFixture struct {
	db string
	// The role NAMES, kept because some cells alter a role rather than connect as
	// it (a read-only default is set on the role, not carried in a DSN).
	appRole   string
	ownerRole string
	adminRole string
	appDSN    string
	ownerDSN  string
	adminDSN  string
	// split is false for the single-role posture, where the app role owns the
	// schema and ownerDSN is deliberately empty.
	split  bool
	binDir string
}

// newPGSplitFixture provisions a fresh database plus its roles. With split=true
// it creates a distinct owner (the posture in which the app role has NO schema
// CREATE); with split=false it creates today's single-role posture, so the same
// cells can prove the fix did not cost the deployment that already worked.
//
// Role names are unique per fixture because Postgres ROLES are cluster-wide: two
// concurrent cells sharing `olivares_owner` would provision over each other.
func newPGSplitFixture(t *testing.T, label string, split bool) *pgSplitFixture {
	t.Helper()
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	if super == "" {
		t.Skipf("set %s to run the Postgres leg (roles and databases are provisioned from it)", pgProbeDSN)
	}
	maint, err := sql.Open("pgx", super)
	if err != nil {
		t.Fatalf("open %s: %v", pgProbeDSN, err)
	}
	defer func() { _ = maint.Close() }()
	var major int
	if err := maint.QueryRowContext(t.Context(), "SELECT current_setting('server_version_num')::int / 10000").Scan(&major); err != nil {
		t.Fatalf("probe server version over %s: %v", pgProbeDSN, err)
	}
	binDir := pgClientBinDir(t, major)

	// A short unique suffix: role and database identifiers must match
	// store.SafeIdentPattern ([a-z_][a-z0-9_]*, <=63), so no timestamp formatting
	// that could introduce a character the provisioner rightly refuses.
	uniq := fmt.Sprintf("%s_%d", strings.ToLower(label), time.Now().UnixNano()%1_000_000_000)
	dbName := "drsplit_" + uniq
	appRole := "app_" + uniq
	ownerRole := "owner_" + uniq
	adminRole := "admin_" + uniq

	spec := store.PgProvisionSpec{
		Database: dbName,
		// Fixture passwords for a disposable loopback instance. They are never a
		// host or production credential and never leave this process.
		App:     store.PgRole{Name: appRole, Password: "apppw"},
		Admin:   &store.PgRole{Name: adminRole, Password: "adminpw"},
		SSLMode: "disable",
	}
	if split {
		spec.Owner = store.PgRole{Name: ownerRole, Password: "ownerpw"}
	}
	if _, err := coreengine.ProvisionPostgres(t.Context(), super, spec, true); err != nil {
		t.Fatalf("provision the %s posture: %v", postureName(split), err)
	}
	t.Cleanup(func() {
		db, err := sql.Open("pgx", super)
		if err != nil {
			return
		}
		defer func() { _ = db.Close() }()
		_, _ = db.Exec(`DROP DATABASE IF EXISTS "` + dbName + `" WITH (FORCE)`)
		// Roles are cluster-wide, so they outlive the database unless dropped.
		for _, r := range []string{appRole, adminRole, ownerRole} {
			_, _ = db.Exec(`DROP ROLE IF EXISTS "` + r + `"`)
		}
	})

	f := &pgSplitFixture{
		db:        dbName,
		appRole:   appRole,
		adminRole: adminRole,
		appDSN:    roleDSN(t, super, appRole, "apppw", dbName),
		adminDSN:  roleDSN(t, super, adminRole, "adminpw", dbName),
		split:     split,
		binDir:    binDir,
	}
	if split {
		f.ownerRole = ownerRole
		f.ownerDSN = roleDSN(t, super, ownerRole, "ownerpw", dbName)
	}
	return f
}

// superExec runs a cluster-scoped statement as the superuser (roles are
// cluster-wide, so role DDL cannot run in the fixture database's own connection
// as anyone else).
func (f *pgSplitFixture) superExec(t *testing.T, stmt string) {
	t.Helper()
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	db, err := sql.Open("pgx", super)
	if err != nil {
		t.Fatalf("open %s as superuser: %v", pgProbeDSN, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), stmt); err != nil {
		t.Fatalf("superuser exec: %v", err)
	}
}

// superExecInDB runs a statement as the superuser INSIDE the fixture database —
// schema grants are database-scoped and do not take effect from the maintenance
// database.
func (f *pgSplitFixture) superExecInDB(t *testing.T, stmt string) {
	t.Helper()
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	db, err := sql.Open("pgx", roleDSNKeepUser(t, super, f.db))
	if err != nil {
		t.Fatalf("open %s as superuser: %v", f.db, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), stmt); err != nil {
		t.Fatalf("superuser exec in %s: %v", f.db, err)
	}
}

// superExecBestEffort is the cleanup form: a teardown that fails must not fail
// the test it is cleaning up after.
func (f *pgSplitFixture) superExecBestEffort(stmt string) {
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	db, err := sql.Open("pgx", super)
	if err != nil {
		return
	}
	defer func() { _ = db.Close() }()
	_, _ = db.Exec(stmt)
}

func postureName(split bool) string {
	if split {
		return "owner/app split"
	}
	return "single-role"
}

// roleDSN rewrites the maintenance URL's userinfo and database, keeping its host,
// port and query (sslmode) — so the fixture reaches the SAME server the suite was
// pointed at, whatever that is.
func roleDSN(t *testing.T, super, role, password, dbName string) string {
	t.Helper()
	u, err := url.Parse(super)
	if err != nil {
		t.Fatalf("%s is not a URL: %v", pgProbeDSN, err)
	}
	u.User = url.UserPassword(role, password)
	u.Path = "/" + dbName
	return u.String()
}

// drFlagsFor returns the store flags a DR command needs for this fixture. In the
// single-role posture ownerDSN stays EMPTY: passing an owner flag there would be
// the wrong invocation, and the point of that arm is that the documented one
// still works.
func (f *pgSplitFixture) drArgs() []string {
	args := []string{"--engine", "postgres", "--dsn", f.appDSN, "--admin-dsn", f.adminDSN}
	if f.ownerDSN != "" {
		args = append(args, "--owner-dsn", f.ownerDSN)
	}
	return args
}

// seed boots the engine ONCE against this fixture with the posture's own roles,
// migrating the schema, minting the signing keys into dataDir and writing several
// tenant chains — the estate a DR backup then captures.
//
// It boots through the same composition root the commands use, with OwnerDSN set
// exactly as `dr` now sets it, because the schema must be created by the DDL role
// the posture designates: a split estate whose tables were created by the app role
// is not the split, and every ownership assertion downstream would be measuring
// the fixture instead of the code.
//
// FOUR tenants, not one: the manifest carries a per-tenant chain tip and
// RestoreVerify walks each one, so a single-chain estate would let a restore that
// recovered one tenant and lost the rest report itself green.
func (f *pgSplitFixture) seed(t *testing.T, dataDir string) {
	t.Helper()
	ctx := t.Context()
	eng, err := boot(ctx, bootConfig{
		DataDir: dataDir, Engine: "postgres",
		DSN: f.appDSN, OwnerDSN: f.ownerDSN, AdminDSN: f.adminDSN,
		Version: "test",
	})
	if err != nil {
		t.Fatalf("seed boot in the %s posture: %v", postureName(f.split), err)
	}
	defer func() { _ = eng.Close() }()
	for i := range 3 {
		var tid model.TenantID
		if err := eng.store.System(ctx, func(sys store.SystemScope) error {
			o, e := sys.CreateOrg(ctx, model.Org{
				Name:   fmt.Sprintf("acme-%d", i),
				Slug:   fmt.Sprintf("acme-%d", i),
				Status: model.StatusActive,
			})
			tid = o.TenantID
			return e
		}); err != nil {
			t.Fatalf("seed org %d: %v", i, err)
		}
		if err := eng.store.Mutate(ctx, tid, func(sc store.Scope) error {
			for range 4 {
				if _, err := sc.Audit().Append(ctx, model.AuditDraft{
					Actor: "user:x", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent",
				}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("seed audit events for tenant %d: %v", i, err)
		}
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
}

// query runs a scalar query as the SUPERUSER, so an assertion about privileges is
// never limited by the privileges under test.
func (f *pgSplitFixture) superScalar(t *testing.T, q string, args ...any) int {
	t.Helper()
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	db, err := sql.Open("pgx", roleDSNKeepUser(t, super, f.db))
	if err != nil {
		t.Fatalf("open %s as superuser: %v", f.db, err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(t.Context(), q, args...).Scan(&n); err != nil {
		t.Fatalf("query on %s: %v", f.db, err)
	}
	return n
}

// roleDSNKeepUser points the maintenance credentials at another database.
func roleDSNKeepUser(t *testing.T, super, dbName string) string {
	t.Helper()
	u, err := url.Parse(super)
	if err != nil {
		t.Fatalf("%s is not a URL: %v", pgProbeDSN, err)
	}
	u.Path = "/" + dbName
	return u.String()
}

// stampVersion seals the binary's version for the duration of a cell.
//
// A test binary reports version "dev", and core/dr refuses any manifest whose
// producing version is not an ORDERABLE RELEASE VERSION — so an unstamped build
// writes bundles it can never verify or restore. That is a real, separately
// registered defect of `dr backup` (it warns about nothing); it is NOT what these
// cells measure, and the guard is deliberately not softened. Same workaround, and
// the same reasoning, as TestDRBackupVerifyRestoreCLI.
func stampVersion(t *testing.T) {
	t.Helper()
	prev := version
	version = "26.9.0"
	t.Cleanup(func() { version = prev })
}

// passphraseFile writes a KEK passphrase for a cell.
func passphraseFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dr-pass")
	if err := os.WriteFile(p, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDRPostgresRoundTripAcrossBothPostures is the end-to-end cell the suite has
// never had: a REAL `dr backup` of a real Postgres estate, then a REAL
// `dr restore` of that bundle into a freshly provisioned empty target, asserting
// exit 0 and a verified ledger.
//
// It runs in BOTH postures on purpose. The split arm is the one that is red on the
// pre-fix tree — `pg_restore: permission denied for schema public (SQLSTATE
// 42501)`, because the app role has no CREATE and `dr` had no owner pool. The
// single-role arm is the regression guard for the fix: that posture worked before
// and must keep working with NO owner flag at all.
func TestDRPostgresRoundTripAcrossBothPostures(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(postureName(split), func(t *testing.T) {
			stampVersion(t)
			src := newPGSplitFixture(t, "src", split)
			dataDir := t.TempDir()
			bundle := filepath.Join(t.TempDir(), "b.drbundle")
			pf := passphraseFile(t)

			// BACKUP of a real estate: three tenants plus the system tenant, four
			// audit events each, checkpointed.
			src.seed(t, dataDir)
			args := append([]string{"backup", "--data-dir", dataDir}, src.drArgs()...)
			args = append(args, "--pg-dump", src.bin("pg_dump"), "--out", bundle, "--passphrase-file", pf)
			if out, err := runDR(args...); err != nil {
				t.Fatalf("dr backup in the %s posture failed: %v\n%s", postureName(split), err, out)
			}

			// RESTORE into a SEPARATE, freshly provisioned, empty target of the same
			// posture — not back into the source, so the restore really has to create
			// every object.
			dst := newPGSplitFixture(t, "dst", split)
			restoreDir := t.TempDir()
			rargs := append([]string{"restore", "--data-dir", restoreDir}, dst.drArgs()...)
			rargs = append(rargs, "--pg-restore", dst.bin("pg_restore"), "--in", bundle, "--passphrase-file", pf)
			out, err := runDR(rargs...)
			if err != nil {
				t.Fatalf("dr restore into an EMPTY %s target failed: %v\n%s", postureName(split), err, out)
			}

			// The estate really landed: the engine's own tables exist in the target.
			if tables := dst.superScalar(t,
				`SELECT count(*) FROM pg_catalog.pg_class c
				   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				  WHERE n.nspname = 'public' AND c.relkind IN ('r','p')`); tables == 0 {
				t.Fatalf("dr restore reported success but the %s target holds NO tables", postureName(split))
			}

			// OWNERSHIP IS THE ASSERTION THAT KILLS THE MUTANT. The objects a restore
			// creates are owned by the role that created them, so "the owner ran
			// pg_restore" is observable in the catalog and nowhere else. A mutation that
			// keeps the flag, the help text and the preflight but hands runPgRestore
			// sf.dsn again would restore as the APP role — exactly the defect — and
			// every count above would still pass.
			wantOwner := "app_"
			if split {
				wantOwner = "owner_"
			}
			if wrong := dst.superScalar(t,
				`SELECT count(*) FROM pg_catalog.pg_class c
				   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				  WHERE n.nspname = 'public' AND c.relkind IN ('r','p')
				    AND pg_catalog.pg_get_userbyid(c.relowner) NOT LIKE $1`, wantOwner+"%"); wrong != 0 {
				t.Fatalf("%d restored tables are NOT owned by the %s role in the %s posture: the restore ran on the wrong connection",
					wrong, strings.TrimSuffix(wantOwner, "_"), postureName(split))
			}

			// And the split really is the split: the application role must still have
			// NO schema CREATE after the restore. If this passes because the fix
			// granted the app role CREATE, the posture was collapsed, not supported.
			if split {
				if canCreate := dst.superScalar(t,
					`SELECT CASE WHEN pg_catalog.has_schema_privilege($1,'public','CREATE') THEN 1 ELSE 0 END`,
					appRoleOf(t, dst.appDSN)); canCreate != 0 {
					t.Fatal("the application role gained CREATE on schema public: the owner/app split was collapsed, not supported")
				}
			}
		})
	}
}

// appRoleOf extracts the role name from a fixture DSN (test-only; these DSNs are
// built by roleDSN just above).
func appRoleOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		t.Fatalf("fixture DSN has no userinfo")
	}
	return u.User.Username()
}

// TestDRRestorePostgresRefusesAWrongRoleBEFOREItWrites is F4: the role guard used
// to run AFTER pg_restore.
//
// Measured on the pre-fix tree with a superuser --dsn into an EMPTY target:
// pg_restore exited 0 and wrote the whole estate owned by `postgres`, and only
// then did the boot refuse — leaving a restored, unverified estate the
// application role cannot read. The assertions here are therefore not about the
// error alone but about the two things that must be UNTOUCHED when it fires.
func TestDRRestorePostgresRefusesAWrongRoleBEFOREItWrites(t *testing.T) {
	stampVersion(t)
	src := newPGSplitFixture(t, "wrsrc", true)
	dataDir := t.TempDir()
	src.seed(t, dataDir)
	bundle := filepath.Join(t.TempDir(), "b.drbundle")
	pf := passphraseFile(t)
	args := append([]string{"backup", "--data-dir", dataDir}, src.drArgs()...)
	args = append(args, "--pg-dump", src.bin("pg_dump"), "--out", bundle, "--passphrase-file", pf)
	if out, err := runDR(args...); err != nil {
		t.Fatalf("fixture backup failed: %v\n%s", err, out)
	}

	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	for _, tc := range []struct {
		name  string
		dsn   func(dst *pgSplitFixture) string
		owner func(dst *pgSplitFixture) string
		want  string
	}{
		{
			name:  "a SUPERUSER as --dsn",
			dsn:   func(dst *pgSplitFixture) string { return roleDSNKeepUser(t, super, dst.db) },
			owner: func(dst *pgSplitFixture) string { return dst.ownerDSN },
			want:  "SUPERUSER",
		},
		{
			// The read-only BYPASSRLS backup role pointed at the OWNER flag. It is
			// RLS-unsafe for a write pool and it holds no CREATE, so either refusal is
			// correct; what must not happen is a restore.
			name:  "the read-only BYPASSRLS backup role as --owner-dsn",
			dsn:   func(dst *pgSplitFixture) string { return dst.appDSN },
			owner: func(dst *pgSplitFixture) string { return dst.adminDSN },
			want:  "BYPASSRLS",
		},
		{
			name: "an UNREACHABLE owner role",
			dsn:  func(dst *pgSplitFixture) string { return dst.appDSN },
			owner: func(*pgSplitFixture) string {
				return "postgres://nobody:nobody@127.0.0.1:1/nowhere?sslmode=disable&connect_timeout=2"
			},
			want: "UNREACHABLE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := newPGSplitFixture(t, "wrdst", true)
			restoreDir := t.TempDir()
			out, err := runDR("restore", "--engine", "postgres",
				"--dsn", tc.dsn(dst), "--owner-dsn", tc.owner(dst), "--admin-dsn", dst.adminDSN,
				"--data-dir", restoreDir, "--pg-restore", dst.bin("pg_restore"),
				"--in", bundle, "--passphrase-file", pf)
			if err == nil {
				t.Fatalf("the restore was ACCEPTED with %s:\n%s", tc.name, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal must name %q so an operator can act on it, got: %v", tc.want, err)
			}
			// ZERO EFFECTS, which is the whole finding. The target holds no relation of
			// its own beyond what provisioning left (a fresh `db init` database carries
			// none), and the data dir holds no signing key: the refusal came before
			// custody was installed and before pg_restore ran.
			if n := dst.superScalar(t,
				`SELECT count(*) FROM pg_catalog.pg_class c
				   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				  WHERE n.nspname = 'public' AND c.relkind IN ('r','p')`); n != 0 {
				t.Fatalf("the refused restore left %d tables in the target", n)
			}
			keys, _ := filepath.Glob(filepath.Join(restoreDir, "*-signing.key"))
			if len(keys) != 0 {
				t.Fatalf("the refused restore installed custody it never rolled back: %v", keys)
			}
		})
	}
}

// TestDRRestorePostgresRefusesMISMATCHEDTargets pins the coherence half, and it
// is the reason the check reads the CONNECTION and not the DSN string: two DSNs
// that differ only in text can be the same estate behind an alias or a pooler,
// and two that agree in text cannot be different ones. Here they genuinely are
// different databases, which the server reports and no amount of string
// comparison would need to be trusted for.
func TestDRRestorePostgresRefusesMISMATCHEDTargets(t *testing.T) {
	stampVersion(t)
	dst := newPGSplitFixture(t, "mmdst", true)
	other := newPGSplitFixture(t, "mmoth", true)
	bundle := filepath.Join(t.TempDir(), "not-a-bundle.drbundle")
	if err := os.WriteFile(bundle, []byte("not a bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	pf := passphraseFile(t)

	for _, tc := range []struct {
		name, dsn, ownerDSN, adminDSN, want string
	}{
		{
			name: "--owner-dsn on another database", dsn: dst.appDSN, ownerDSN: other.ownerDSN, adminDSN: dst.adminDSN,
			want: "the owner role must run its DDL on the same database the application serves",
		},
		{
			name: "--admin-dsn on another database", dsn: dst.appDSN, ownerDSN: dst.ownerDSN, adminDSN: other.adminDSN,
			want: "would enumerate ANOTHER estate's tenants",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runDR("restore", "--engine", "postgres",
				"--dsn", tc.dsn, "--owner-dsn", tc.ownerDSN, "--admin-dsn", tc.adminDSN,
				"--data-dir", t.TempDir(), "--in", bundle, "--passphrase-file", pf)
			if err == nil {
				t.Fatal("a restore with pools on two different estates was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal must explain the mismatch, got: %v", err)
			}
			// The bundle is deliberately invalid: reaching THAT error instead would
			// mean the coherence check never ran, so this also pins the ORDER.
			if strings.Contains(err.Error(), "not a bundle") {
				t.Fatalf("the mismatch was not caught before the bundle was opened: %v", err)
			}
		})
	}
}

// TestDRBackupPostgresInTheSplitNamesTheOwnerFlag is the named-cause half of the
// fix. Without it the split posture's backup dies inside the boot with
// `create contract probe "olv_k3p_epoch" … permission denied for schema public
// (SQLSTATE 42501)` — a per-boot directory probe an operator has no reason to
// have heard of, naming neither the cause nor the flag that fixes it. Asserting
// the MESSAGE is the point: a mutant that restored the bare 42501 would pass a
// test that only asserted failure.
func TestDRBackupPostgresInTheSplitNamesTheOwnerFlag(t *testing.T) {
	stampVersion(t)
	src := newPGSplitFixture(t, "nocause", true)
	// The estate and its custody exist: this cell must fail on the AUTHORITY, not
	// on an empty data dir.
	dataDir := t.TempDir()
	src.seed(t, dataDir)
	pf := passphraseFile(t)
	// --snapshot-file is the DOCUMENTED split-posture invocation, not a shortcut:
	// deploy/postgres/backup/pg-dump.sh and the Helm CronJob both produce the dump
	// in a separate step (a postgres-client container; the engine image is
	// distroless and has no pg_dump) and hand it to `dr backup` exactly like this.
	// It is also what keeps this cell measuring the AUTHORITY refusal instead of
	// whichever pg_dump happens to be installed.
	snap := filepath.Join(t.TempDir(), "dump.pgcustom")
	if err := os.WriteFile(snap, []byte("fixture dump bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runDR("backup", "--data-dir", dataDir, "--engine", "postgres",
		"--dsn", src.appDSN, "--admin-dsn", src.adminDSN,
		"--snapshot-file", snap,
		"--out", filepath.Join(t.TempDir(), "b.drbundle"), "--passphrase-file", pf)
	if err == nil {
		t.Fatal("a backup in the owner/app split without --owner-dsn was accepted")
	}
	for _, want := range []string{"--owner-dsn", "denied schema CREATE", "olivares db check"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must contain %q so it is actionable, got: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "olv_k3p_epoch") {
		t.Fatalf("the operator still gets the raw contract-probe error: %v", err)
	}
}

// bin resolves a client binary inside this fixture's matched bin dir.
func (f *pgSplitFixture) bin(name string) string {
	if f.binDir == "" {
		return name
	}
	return filepath.Join(f.binDir, name)
}

// TestDRRestorePITRCompanionNeedsTheOwnerPoolAtBOOT is the cell that
// DISCRIMINATES between the two halves of the owner fix, and without it a fix
// that changed only pg_restore's target would pass everything above.
//
// A PITR companion bundle carries NO store bytes: the operator recovered Postgres
// out of band from the basebackup + WAL, and `dr restore` skips pg_restore
// entirely and goes straight to booting that live store to prove continuity. So
// on this path the owner pool is needed ONLY by drBoot — and on the pre-fix tree
// that is exactly where it failed, with `create contract probe "olv_k3p_epoch" …
// permission denied for schema public (SQLSTATE 42501)` AFTER printing that the
// keys were restored.
//
// The two arms are the discrimination: with --owner-dsn the boot completes and
// the ledger verifies; without it the pre-flight refuses and names the flag.
func TestDRRestorePITRCompanionNeedsTheOwnerPoolAtBOOT(t *testing.T) {
	stampVersion(t)
	est := newPGSplitFixture(t, "pitr", true)
	srcDir := t.TempDir()
	est.seed(t, srcDir)
	pf := passphraseFile(t)
	bundle := filepath.Join(t.TempDir(), "pitr.drbundle")

	args := append([]string{"backup", "--data-dir", srcDir}, est.drArgs()...)
	args = append(args, "--pitr-ref", "s3://wal-archive/olivares/2026-09-07T00:00:00Z",
		"--out", bundle, "--passphrase-file", pf)
	if out, err := runDR(args...); err != nil {
		t.Fatalf("PITR companion backup failed: %v\n%s", err, out)
	}

	// The estate is the one recovered out of band, so the target is OCCUPIED and
	// the restore is a declared replacement — that gate is unchanged and still
	// applies. The data dir is fresh: on a DR host the custody arrives in the
	// bundle.
	decl := []string{"--operator", "oncall@example.test", "--reason", "PITR drill"}

	t.Run("without --owner-dsn the pre-flight refuses and names the flag", func(t *testing.T) {
		a := append([]string{"restore", "--engine", "postgres", "--data-dir", t.TempDir(),
			"--dsn", est.appDSN, "--admin-dsn", est.adminDSN,
			"--in", bundle, "--passphrase-file", pf}, decl...)
		_, err := runDR(a...)
		if err == nil {
			t.Fatal("a PITR restore into a split estate was accepted with no owner pool")
		}
		if !strings.Contains(err.Error(), "--owner-dsn") {
			t.Fatalf("the refusal must name --owner-dsn (pg_restore never runs on this path, so the owner is needed by the BOOT): %v", err)
		}
	})

	t.Run("with --owner-dsn the boot completes and the ledger verifies", func(t *testing.T) {
		dstDir := t.TempDir()
		a := append([]string{"restore", "--engine", "postgres", "--data-dir", dstDir,
			"--dsn", est.appDSN, "--owner-dsn", est.ownerDSN, "--admin-dsn", est.adminDSN,
			"--in", bundle, "--passphrase-file", pf}, decl...)
		out, err := runDR(a...)
		if err != nil {
			t.Fatalf("PITR companion restore into the split estate failed: %v\n%s", err, out)
		}
		// The keys really were installed from the bundle, which is the whole payload
		// of a companion: without them the recovered store's chain cannot be verified.
		keys, _ := filepath.Glob(filepath.Join(dstDir, "*-signing.key"))
		if len(keys) == 0 {
			t.Fatal("the PITR restore reported success but installed no signing key")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// R2 — the three authority facts an ACL check cannot see, and the identity a
// database NAME cannot carry. Each cell here answers one blocker of the
// independent review.
// ─────────────────────────────────────────────────────────────────────────────

// TestProbeConnAuthorityCertifiesONESession is the causal control for the
// pool-split defect: ProbeConnAuthority used to take the role posture off a
// *sql.DB and then the remaining facts off the same *sql.DB. A *sql.DB is a POOL.
// On a stable direct route database/sql reuses the idle connection, so that
// version measured green by coincidence; behind a pooler it could certify one
// backend's ROLE together with another backend's database, CREATE grant and
// writability — a session that never existed.
//
// THE FIXTURE IS WHAT MAKES THIS DETERMINISTIC. The probing role is created with
// CONNECTION LIMIT 1. The correct implementation pins ONE connection and runs
// every read on a transaction over it, so one connection is all it ever needs.
// A mutant that re-queries through the pool needs a SECOND connection while the
// first is still pinned, and PostgreSQL refuses it outright.
//
// MEASURED with the mutant in place (facts read via `db` instead of `tx`):
//
//	Reachable=false
//	Err="… failed to connect …: FATAL: too many connections for role
//	     \"probe_one\" (SQLSTATE 53300)"
//
// A timing-dependent assertion would have been worthless here; a server-enforced
// connection limit is not timing-dependent.
func TestProbeConnAuthorityCertifiesONESession(t *testing.T) {
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	if super == "" {
		t.Skipf("set %s to run the Postgres leg", pgProbeDSN)
	}
	fx := newPGSplitFixture(t, "onesess", true)

	role := "probe_one_" + strings.TrimPrefix(fx.db, "drsplit_")
	if len(role) > 63 {
		role = role[:63]
	}
	fx.superExec(t, `CREATE ROLE "`+role+`" LOGIN PASSWORD 'onepw' NOSUPERUSER NOBYPASSRLS CONNECTION LIMIT 1`)
	t.Cleanup(func() { fx.superExecBestEffort(`DROP ROLE IF EXISTS "` + role + `"`) })
	fx.superExec(t, `GRANT CONNECT ON DATABASE "`+fx.db+`" TO "`+role+`"`)
	fx.superExecInDB(t, `GRANT USAGE, CREATE ON SCHEMA public TO "`+role+`"`)

	auth, err := coreengine.ProbeConnAuthority(t.Context(), store.Config{
		Engine: store.EnginePostgres,
		DSN:    roleDSN(t, super, role, "onepw", fx.db),
	})
	if err != nil {
		t.Fatalf("ProbeConnAuthority returned a programmer error: %v", err)
	}
	if !auth.Posture.Reachable {
		t.Fatalf("the probe needed MORE THAN ONE connection for a role limited to one, so its facts do not describe a single session: %s", auth.Posture.Err)
	}
	// The facts really were gathered, not defaulted.
	if auth.Posture.Role != role || auth.Database != fx.db || auth.SystemIdentifier == "" {
		t.Fatalf("the probe reported an incomplete picture: role=%q db=%q sysid=%q", auth.Posture.Role, auth.Database, auth.SystemIdentifier)
	}
}

// TestDRRestoreRefusesAReadOnlyDDLPoolBeforeAnyEffect is IR-F4-03: `CanCreate` is
// an ACL fact, and a GRANT is not permission to write. A session under
// default_transaction_read_only holds CREATE in the catalogue and cannot execute
// one — measured on PostgreSQL 16.15 as
// `can_create_acl=true transaction_read_only=on` followed by
// `ERROR: cannot execute CREATE TABLE in a read-only transaction`.
//
// The pre-flight used to admit exactly that pool. Custody would then be installed
// and pg_restore would discover the impossibility — F4 arriving after the
// destructive step again, by a second route. The assertions are therefore not
// about the message alone but about the two things that must be untouched.
func TestDRRestoreRefusesAReadOnlyDDLPoolBeforeAnyEffect(t *testing.T) {
	stampVersion(t)
	src := newPGSplitFixture(t, "rosrc", true)
	dataDir := t.TempDir()
	src.seed(t, dataDir)
	pf := passphraseFile(t)
	bundle := filepath.Join(t.TempDir(), "b.drbundle")
	args := append([]string{"backup", "--data-dir", dataDir}, src.drArgs()...)
	args = append(args, "--pg-dump", src.bin("pg_dump"), "--out", bundle, "--passphrase-file", pf)
	if out, err := runDR(args...); err != nil {
		t.Fatalf("fixture backup failed: %v\n%s", err, out)
	}

	dst := newPGSplitFixture(t, "rodst", true)
	// The owner keeps every GRANT it had; only its sessions become read-only. That
	// is the whole point: nothing an ACL check can see has changed.
	dst.superExec(t, `ALTER ROLE "`+dst.ownerRole+`" SET default_transaction_read_only = on`)

	restoreDir := t.TempDir()
	rargs := append([]string{"restore", "--data-dir", restoreDir}, dst.drArgs()...)
	rargs = append(rargs, "--pg-restore", dst.bin("pg_restore"), "--in", bundle, "--passphrase-file", pf)
	out, err := runDR(rargs...)
	if err == nil {
		t.Fatalf("a restore whose DDL pool cannot execute one statement of DDL was ACCEPTED:\n%s", out)
	}
	for _, want := range []string{"READ-ONLY session", "transaction_read_only=on", "default_transaction_read_only"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name the cause and its remedy (%q), got: %v", want, err)
		}
	}
	// ZERO EFFECTS, which is the finding: the refusal came before custody and
	// before pg_restore.
	if n := dst.superScalar(t,
		`SELECT count(*) FROM pg_catalog.pg_class c
		   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND c.relkind IN ('r','p')`); n != 0 {
		t.Fatalf("the refused restore left %d tables in the target", n)
	}
	if keys, _ := filepath.Glob(filepath.Join(restoreDir, "*-signing.key")); len(keys) != 0 {
		t.Fatalf("the refused restore installed custody it never rolled back: %v", keys)
	}
}

// pgReplicaDSNEnv and pgUnrelatedDSNEnv point at two clusters a single test
// server cannot be: a PHYSICAL REPLICA of the fixture primary, and an UNRELATED
// cluster that happens to serve a database of the same name. Both cells below
// skip when they are unset — and a skip is not a pass, which is why the R2
// evidence run sets them and records the result.
//
// They must address the SAME database name as the primary's `--roles` fixture
// database (`olivares`), with the same fixture roles; a physical replica has both
// by construction, and the unrelated cluster is provisioned the same way on
// purpose — that is what makes it a discriminator instead of a formality.
const (
	// pgAppDSNEnv is the pair the rest of the suite already gates on: the fixture
	// application role on the fixture database of the PRIMARY.
	pgAppDSNEnv = "OLIVARES_TEST_POSTGRES_DSN"
	// pgAdminDSNEnv is the fixture admin role on the PRIMARY, which the suite's own
	// environment already provides alongside the app pair.
	pgAdminDSNEnv = "OLIVARES_TEST_POSTGRES_ADMIN_DSN"
	// pgReplicaDSNEnv is the ADMIN role on the replica — the pool for which a
	// standby is legitimate. pgReplicaAppDSNEnv is the APPLICATION role on the same
	// replica, and the two are separate on purpose: pointing a DDL pool at the
	// admin DSN is refused on POSTURE (BYPASSRLS) before recovery is ever reached,
	// so a standby cell written against it would pass while measuring nothing.
	pgReplicaDSNEnv    = "OLIVARES_TEST_PG_REPLICA_DSN"
	pgReplicaAppDSNEnv = "OLIVARES_TEST_PG_REPLICA_APP_DSN"
	pgUnrelatedDSNEnv  = "OLIVARES_TEST_PG_UNRELATED_DSN"
)

// TestDRRefusesAnAdminReaderOnAPhysicalReplica is root's R3 correction, and it
// INVERTS what R2 asserted here.
//
// R2 admitted an --admin-dsn on a physical replica, reasoning that a replica
// shares its primary's system identifier and so belongs to the same estate. The
// identity reasoning is correct and the conclusion was not: measured against a
// real pg_basebackup standby, such a deployment CANNOT BOOT. The store's
// directory activation runs a concurrent advisory-lock challenge that proves same
// LIVE SERVER, and a replica is a different server, so it refuses at Open — after
// a restore would already have written. R2's pre-flight was advertising support
// the product does not have.
//
// The engine guard is preserved. The pre-flight now runs the SAME challenge from
// the same factored predicate, before custody, extraction and pg_restore.
//
// Reading through a replica is not closed by this refusal — it needs an
// authoritative snapshot/LSN contract for what the enumerated inventory is a
// snapshot OF. That is separate work; what is fixed here is claiming it works.
func TestDRRefusesAnAdminReaderOnAPhysicalReplica(t *testing.T) {
	appDSN := strings.TrimSpace(os.Getenv(pgAppDSNEnv))
	replicaDSN := strings.TrimSpace(os.Getenv(pgReplicaDSNEnv))
	if appDSN == "" || replicaDSN == "" {
		t.Skipf("set %s and %s to run the physical-replica cell", pgAppDSNEnv, pgReplicaDSNEnv)
	}
	err := preflightPostgresDR(t.Context(), drFlags{
		engineKind: "postgres", dsn: appDSN, adminDSN: replicaDSN,
	}, "backup")
	if err == nil {
		t.Fatal("an admin reader on a PHYSICAL REPLICA was accepted; the engine refuses it at Open, so the pre-flight was advertising support the product does not have")
	}
	if !strings.Contains(err.Error(), "NOT on the same live server") {
		t.Fatalf("the refusal must name the live-server challenge, got: %v", err)
	}
	// It must NOT have been caught by cluster identity — a replica carries its
	// primary's, so a refusal naming that would mean the fixture is not a replica.
	if strings.Contains(err.Error(), "DIFFERENT PostgreSQL cluster") {
		t.Fatalf("the fixture was refused as a foreign CLUSTER, so it is not a physical replica of the primary: %v", err)
	}
}

// TestDRAdmitsAReadOnlyAdminReaderOnTheSameServer is the positive root asked for,
// and it is what keeps the R3 tightening from becoming "refuse every admin pool".
//
// The cross-tenant reader is read-only BY DESIGN. What it must be is on the same
// live server; what it must NOT be required to be is writable. Advisory locks are
// not writes, so a session under default_transaction_read_only answers the
// challenge normally — which is why the challenge is the right instrument for this
// and a writability check would have been the wrong one.
func TestDRAdmitsAReadOnlyAdminReaderOnTheSameServer(t *testing.T) {
	appDSN := strings.TrimSpace(os.Getenv(pgAppDSNEnv))
	adminDSN := strings.TrimSpace(os.Getenv(pgAdminDSNEnv))
	if appDSN == "" || adminDSN == "" {
		t.Skipf("set %s and %s to run the read-only admin cell", pgAppDSNEnv, pgAdminDSNEnv)
	}
	fx := newPGSplitFixture(t, "roadmin", false)
	fx.superExec(t, `ALTER ROLE "`+fx.adminRole+`" SET default_transaction_read_only = on`)
	t.Cleanup(func() {
		fx.superExecBestEffort(`ALTER ROLE "` + fx.adminRole + `" RESET default_transaction_read_only`)
	})
	if err := preflightPostgresDR(t.Context(), drFlags{
		engineKind: "postgres", dsn: fx.appDSN, adminDSN: fx.adminDSN,
	}, "backup"); err != nil {
		t.Fatalf("a READ-ONLY cross-tenant reader on the SAME server was refused; it is read-only by design and only has to be on the right server: %v", err)
	}
}

// TestDRRefusesAnAdminReaderOnAnUnrelatedCluster is the negative half// TestDRRefusesAnAdminReaderOnAnUnrelatedCluster is the negative half, and it is
// the case the retired "same database means replica" exemption waved through: an
// unrelated cluster serving a database of the same name. In backup that reader
// would enumerate estate B's tenants into a manifest built from estate A; in
// restore the extra-tenant check would certify the wrong estate.
func TestDRRefusesAnAdminReaderOnAnUnrelatedCluster(t *testing.T) {
	appDSN := strings.TrimSpace(os.Getenv(pgAppDSNEnv))
	otherDSN := strings.TrimSpace(os.Getenv(pgUnrelatedDSNEnv))
	if appDSN == "" || otherDSN == "" {
		t.Skipf("set %s and %s to run the unrelated-cluster cell", pgAppDSNEnv, pgUnrelatedDSNEnv)
	}
	err := preflightPostgresDR(t.Context(), drFlags{
		engineKind: "postgres", dsn: appDSN, adminDSN: otherDSN,
	}, "backup")
	if err == nil {
		t.Fatal("an admin reader on an UNRELATED cluster with a same-named database was accepted")
	}
	if !strings.Contains(err.Error(), "DIFFERENT PostgreSQL cluster") {
		t.Fatalf("the refusal must name the cluster mismatch rather than the database, got: %v", err)
	}
	// It must NOT have been let through on the database name, which is identical.
	if strings.Contains(err.Error(), "reached database") {
		t.Fatalf("the refusal reasoned about the database NAME, which is the same on both: %v", err)
	}
}

// TestDRRefusesADDLPoolOnAStandby is the recovery half of IR-F4-03. A standby
// holds every GRANT its primary does — pg_basebackup copies the catalogue — so
// `CanCreate` is true there and no ACL check can tell the difference. Only
// pg_is_in_recovery() can.
func TestDRRefusesADDLPoolOnAStandby(t *testing.T) {
	replicaAppDSN := strings.TrimSpace(os.Getenv(pgReplicaAppDSNEnv))
	if replicaAppDSN == "" {
		t.Skipf("set %s to run the standby cell", pgReplicaAppDSNEnv)
	}
	// The replica's APPLICATION role is the DDL pool here, which is exactly the
	// operator mistake this catches: a DR host wired to the read replica. It is
	// RLS-safe, so it gets past the posture guard and the recovery predicate is
	// what has to stop it.
	err := preflightPostgresDR(t.Context(), drFlags{
		engineKind: "postgres", dsn: replicaAppDSN,
	}, "restore")
	if err == nil {
		t.Fatal("a restore whose DDL pool is a hot standby was accepted")
	}
	if !strings.Contains(err.Error(), "IN RECOVERY") {
		t.Fatalf("the refusal must name recovery, not merely read-only, so the remedy is 'point at the primary': %v", err)
	}
	// It must not have been stopped by the posture guard instead — that would make
	// this cell pass while measuring nothing.
	if strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("the standby was refused on ROLE POSTURE, so the recovery predicate was never reached: %v", err)
	}
}

// TestDRRestoreRefusesAReadOnlyAPPPoolBeforeAnyEffect is root's R3 correction to
// the write-pool set, and it is the residual R2 named and left open.
//
// R2 required only the DDL pool to be writable. A restore does not end at
// pg_restore: the engine boots and, when the restore REPLACES an estate, seals the
// operator's declaration into the restored ledger — a write on the APPLICATION
// connection. So a split deployment whose app role carries
// `default_transaction_read_only` passed R2's pre-flight, restored the estate, and
// only then failed to record who replaced it, with the target already overwritten.
//
// THE FIXTURE HAS TO REACH THE PRE-FLIGHT, which is the trap R2's own CLI batch
// fell into twice. The estate-replacement declaration is checked BEFORE authority,
// so a populated target refuses on the missing --operator/--reason and the
// pre-flight is never reached; that run would look like a pass while measuring a
// different gate. Two things are therefore true of this cell on purpose: the owner
// is fully WRITABLE (so the DDL pool cannot be what refuses), and the target is
// CLEAN (so no declaration is demanded). Only the app pool's writability is left
// to decide the outcome.
func TestDRRestoreRefusesAReadOnlyAPPPoolBeforeAnyEffect(t *testing.T) {
	stampVersion(t)
	src := newPGSplitFixture(t, "roappsrc", true)
	dataDir := t.TempDir()
	src.seed(t, dataDir)
	pf := passphraseFile(t)
	bundle := filepath.Join(t.TempDir(), "b.drbundle")
	args := append([]string{"backup", "--data-dir", dataDir}, src.drArgs()...)
	args = append(args, "--pg-dump", src.bin("pg_dump"), "--out", bundle, "--passphrase-file", pf)
	if out, err := runDR(args...); err != nil {
		t.Fatalf("fixture backup failed: %v\n%s", err, out)
	}

	dst := newPGSplitFixture(t, "roappdst", true)
	// ONLY the application role becomes read-only. The owner keeps every grant and
	// stays writable, so the DDL predicate has nothing to say and the app predicate
	// is the only thing that can refuse.
	dst.superExec(t, `ALTER ROLE "`+dst.appRole+`" SET default_transaction_read_only = on`)

	restoreDir := t.TempDir()
	rargs := append([]string{"restore", "--data-dir", restoreDir}, dst.drArgs()...)
	rargs = append(rargs, "--pg-restore", dst.bin("pg_restore"), "--in", bundle, "--passphrase-file", pf)
	out, err := runDR(rargs...)
	if err == nil {
		t.Fatalf("a restore whose APPLICATION pool cannot write was accepted; it would seal no declaration:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--dsn") || !strings.Contains(err.Error(), "READ-ONLY session") {
		t.Fatalf("the refusal must name --dsn and its read-only session, got: %v", err)
	}
	if !strings.Contains(err.Error(), "seals the restore declaration") {
		t.Fatalf("the refusal must say what the application write is FOR, got: %v", err)
	}
	// It must not have been the DDL pool: that would mean the fixture failed to
	// isolate the property under test.
	if strings.Contains(err.Error(), "--owner-dsn") {
		t.Fatalf("the OWNER was refused, so this cell did not measure the application pool: %v", err)
	}
	if n := dst.superScalar(t,
		`SELECT count(*) FROM pg_catalog.pg_class c
		   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND c.relkind IN ('r','p')`); n != 0 {
		t.Fatalf("the refused restore left %d tables in the target", n)
	}
	if keys, _ := filepath.Glob(filepath.Join(restoreDir, "*-signing.key")); len(keys) != 0 {
		t.Fatalf("the refused restore installed custody it never rolled back: %v", keys)
	}
}

// TestDRBackupIsNotRestrictedByARestoreOnlyWrite is the other half of that
// correction, and it is what stops it from over-reaching. Backup writes NOTHING to
// the target, so a read-only application pool is not its problem: refusing it
// would break a working backup-from-a-hardened-role deployment for a reason that
// cannot arise. Same fixture, same read-only app role, opposite verb.
func TestDRBackupIsNotRestrictedByARestoreOnlyWrite(t *testing.T) {
	stampVersion(t)
	src := newPGSplitFixture(t, "robackup", true)
	dataDir := t.TempDir()
	src.seed(t, dataDir)
	src.superExec(t, `ALTER ROLE "`+src.appRole+`" SET default_transaction_read_only = on`)
	t.Cleanup(func() {
		src.superExecBestEffort(`ALTER ROLE "` + src.appRole + `" RESET default_transaction_read_only`)
	})
	if err := preflightPostgresDR(t.Context(), drFlags{
		engineKind: "postgres", dsn: src.appDSN, ownerDSN: src.ownerDSN, adminDSN: src.adminDSN,
	}, "backup"); err != nil {
		t.Fatalf("a BACKUP was refused for a write only a RESTORE performs: %v", err)
	}
}
