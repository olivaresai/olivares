// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A restore over a PostgreSQL database that already holds an estate is the one
// destructive act of `dr restore` on that engine. Four cells measure it against a
// real server, in both role postures, with a real bundle and a real estate:
//
//   - without the declaration it is refused;
//   - declared but not forced it is refused and the estate is where it was;
//   - declared and forced it replaces the estate with the backup, keeps the
//     previous custody on disk, and records the act in the restored ledger.
//
// "Where it was" is measured, not assumed: the cells compare the post-backup state
// (an organisation created after the bundle was taken) and the signing key bytes
// before and after each refusal.

// moveOnAfterBackup boots the estate again and adds what a live estate has and its
// last backup does not: one organisation with one audit event.
func (f *pgSplitFixture) moveOnAfterBackup(t *testing.T, dataDir string) {
	t.Helper()
	prepareCompositionTestBoot(t)
	defer prepareCompositionTestBoot(t)
	ctx := t.Context()
	eng, err := boot(ctx, bootConfig{
		DataDir: dataDir, Engine: "postgres",
		DSN: f.appDSN, OwnerDSN: f.ownerDSN, AdminDSN: f.adminDSN,
		Version: "test",
	})
	if err != nil {
		t.Fatalf("boot the estate after its backup: %v", err)
	}
	defer func() { _ = eng.Close() }()
	var tid model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(ctx, model.Org{Name: "late", Slug: "late", Status: model.StatusActive})
		tid = o.TenantID
		return e
	}); err != nil {
		t.Fatalf("create the post-backup org: %v", err)
	}
	if err := eng.store.Mutate(ctx, tid, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:x", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent",
		})
		return err
	}); err != nil {
		t.Fatalf("append the post-backup event: %v", err)
	}
}

func readFileOrFatal(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// occupiedMaxLocksEnv overrides the lock table the main cell runs with (default
// 256). Replacing an estate drops and recreates every relation in ONE transaction,
// which holds a lock for each; how many a server needs is measured, not assumed.
const occupiedMaxLocksEnv = "OLIVARES_TEST_OCCUPIED_MAX_LOCKS"

// TestDRRestoreOverAnOccupiedPostgresTarget runs on a server of each supported
// major that the cell starts itself.
func TestDRRestoreOverAnOccupiedPostgresTarget(t *testing.T) {
	locks := os.Getenv(occupiedMaxLocksEnv)
	if locks == "" {
		locks = "256"
	}
	for _, major := range pitrMajors {
		t.Run(fmt.Sprintf("PostgreSQL %d", major), func(t *testing.T) {
			bin := pitrBinDir(t, major)
			server := startCluster(t, major, bin, filepath.Join(t.TempDir(), "server"), "max_locks_per_transaction = "+locks)
			server.useMajor()
			t.Setenv(pgProbeDSN, server.superDSN())
			occupiedTargetCells(t, server, bin)
		})
	}
}

// lockFixture is an estate, its bundle and a live state that has moved on, for
// the cells that replace it on a server with a given lock table.
type lockFixture struct {
	est       *pgSplitFixture
	dataDir   string
	bundle    string
	pf        string
	keyFile   string
	keyBefore []byte
}

func newLockFixture(t *testing.T, server *pitrCluster, bin string) *lockFixture {
	t.Helper()
	stampVersion(t)
	f := &lockFixture{
		est:     provisionPGSplitFixture(t, server.superDSN(), bin, "lock", false),
		dataDir: t.TempDir(),
		pf:      passphraseFile(t),
		bundle:  filepath.Join(t.TempDir(), "b.drbundle"),
	}
	f.est.seed(t, f.dataDir)
	args := append([]string{"backup", "--data-dir", f.dataDir}, f.est.drArgs()...)
	args = append(args, "--pg-dump", f.est.bin("pg_dump"), "--out", f.bundle, "--passphrase-file", f.pf)
	if out, err := runDR(t, args...); err != nil {
		t.Fatalf("dr backup: %v\n%s", err, out)
	}
	f.est.moveOnAfterBackup(t, f.dataDir)
	keyFiles, _ := filepath.Glob(filepath.Join(f.dataDir, "*-signing.key"))
	if len(keyFiles) == 0 {
		t.Fatal("fixture: no signing key in the data dir")
	}
	f.keyFile, f.keyBefore = keyFiles[0], readFileOrFatal(t, keyFiles[0])
	return f
}

func (f *lockFixture) late(t *testing.T) int {
	return f.est.superScalar(t, `SELECT count(*) FROM orgs WHERE slug = 'late'`)
}

func (f *lockFixture) forcedRestore(t *testing.T) (string, error) {
	rargs := append([]string{"restore", "--data-dir", f.dataDir}, f.est.drArgs()...)
	rargs = append(rargs, "--pg-restore", f.est.bin("pg_restore"), "--pg-dump", f.est.bin("pg_dump"),
		"--in", f.bundle, "--passphrase-file", f.pf,
		"--force", "--operator", "oncall@example.test", "--reason", "INC-3040 lock table")
	return runDR(t, rargs...)
}

// assertUntouched holds what a restore that failed whole must leave: the live
// estate, the signing key, no half-way custody and no safety copy that is no
// longer needed.
func (f *lockFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if f.late(t) != 1 {
		t.Fatal("a failed replacement changed the live estate")
	}
	if got := readFileOrFatal(t, f.keyFile); string(got) != string(f.keyBefore) {
		t.Fatal("a failed replacement replaced the signing key")
	}
	assertOnlySafetyCopy(t, f.dataDir)
}

// assertOnlySafetyCopy: after a replacement that failed, the previous keys are back
// in place (no *-signing.key.pre-restore-* is left) and the only thing kept is the
// pg_dump of the database, which is the one copy of the old estate if the client's
// view of the failure was wrong.
func assertOnlySafetyCopy(t *testing.T, dataDir string) []string {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(dataDir, "*.pre-restore-*"))
	for _, name := range left {
		if !strings.HasPrefix(filepath.Base(name), "database.pgcustom.pre-restore-") {
			t.Fatalf("a failed replacement left preserved keys behind: %v", left)
		}
	}
	if partial, _ := filepath.Glob(filepath.Join(dataDir, "*.partial")); len(partial) != 0 {
		t.Fatalf("a safety copy under a .partial name was left behind: %v", partial)
	}
	return left
}

// TestDRRestoreReplaceFailsWholeWhenTheLockTableIsTooSmall is deterministic on
// every major: a lock table of 10 per transaction (1000 for 100 connections) cannot
// hold the 2556 relations of the engine schema, so the replacement MUST fail, whole,
// and name the setting to raise.
func TestDRRestoreReplaceFailsWholeWhenTheLockTableIsTooSmall(t *testing.T) {
	for _, major := range pitrMajors {
		t.Run(fmt.Sprintf("PostgreSQL %d", major), func(t *testing.T) {
			bin := pitrBinDir(t, major)
			server := startCluster(t, major, bin, filepath.Join(t.TempDir(), "server"), "max_locks_per_transaction = 10")
			server.useMajor()
			t.Setenv(pgProbeDSN, server.superDSN())
			f := newLockFixture(t, server, bin)
			_, err := f.forcedRestore(t)
			if err == nil {
				t.Fatal("a replacement that cannot fit the lock table reported success")
			}
			for _, want := range []string{"nothing was dropped", "raise max_locks_per_transaction", "safety copy of the database taken before the replacement is kept as"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the failure must say %q: %v", want, err)
				}
			}
			f.assertUntouched(t)
			if kept, _ := filepath.Glob(filepath.Join(f.dataDir, "database.pgcustom.pre-restore-*")); len(kept) != 1 {
				t.Fatalf("the safety copy must be kept after a failed replacement, got %v", kept)
			}
		})
	}
}

// TestDRRestoreReplaceIsWholeOrNotAtAllOnTheDefaultLockTable runs the same
// replacement on a server with the DEFAULT lock table (64 per transaction, 100
// connections). Whether that is enough depends on the major, and the cell does not
// pretend otherwise: it records which outcome it measured and holds the invariant
// that matters on both. Either the estate was replaced by the backup, or the
// restore failed whole, named max_locks_per_transaction, and left the estate and
// the signing keys exactly as they were.
func TestDRRestoreReplaceIsWholeOrNotAtAllOnTheDefaultLockTable(t *testing.T) {
	for _, major := range pitrMajors {
		t.Run(fmt.Sprintf("PostgreSQL %d", major), func(t *testing.T) {
			bin := pitrBinDir(t, major)
			server := startCluster(t, major, bin, filepath.Join(t.TempDir(), "server"), "")
			server.useMajor()
			t.Setenv(pgProbeDSN, server.superDSN())
			f := newLockFixture(t, server, bin)
			orgsBefore := f.est.superScalar(t, `SELECT count(*) FROM orgs`)
			out, err := f.forcedRestore(t)
			switch {
			case err == nil:
				if f.late(t) != 0 || f.est.superScalar(t, `SELECT count(*) FROM orgs`) != orgsBefore-1 {
					t.Fatalf("the restore reported success but the estate is not the backup's:\n%s", out)
				}
				t.Logf("PostgreSQL %d: the default lock table (64 x 100) is enough to replace this estate", major)
			default:
				if !strings.Contains(err.Error(), "raise max_locks_per_transaction") {
					t.Fatalf("the restore failed for a reason other than the lock table: %v", err)
				}
				f.assertUntouched(t)
				t.Logf("PostgreSQL %d: the default lock table (64 x 100) is NOT enough; the restore failed whole and named max_locks_per_transaction", major)
			}
		})
	}
}

func occupiedTargetCells(t *testing.T, server *pitrCluster, bin string) {
	for _, split := range []bool{false, true} {
		t.Run(postureName(split), func(t *testing.T) {
			stampVersion(t)
			est := provisionPGSplitFixture(t, server.superDSN(), bin, "occ", split)
			dataDir := t.TempDir()
			pf := passphraseFile(t)
			bundle := filepath.Join(t.TempDir(), "b.drbundle")

			est.seed(t, dataDir)
			args := append([]string{"backup", "--data-dir", dataDir}, est.drArgs()...)
			args = append(args, "--pg-dump", est.bin("pg_dump"), "--out", bundle, "--passphrase-file", pf)
			if out, err := runDR(t, args...); err != nil {
				t.Fatalf("dr backup: %v\n%s", err, out)
			}
			est.moveOnAfterBackup(t, dataDir)

			orgs := func() int { return est.superScalar(t, `SELECT count(*) FROM orgs`) }
			late := func() int { return est.superScalar(t, `SELECT count(*) FROM orgs WHERE slug = 'late'`) }
			if late() != 1 {
				t.Fatal("fixture: the post-backup organisation is not in the live estate")
			}
			orgsBefore := orgs()
			keyFiles, err := filepath.Glob(filepath.Join(dataDir, "*-signing.key"))
			if err != nil || len(keyFiles) == 0 {
				t.Fatalf("fixture: no signing key in the data dir: %v", err)
			}
			keyBefore := readFileOrFatal(t, keyFiles[0])

			restore := func(dir string, extra ...string) (string, error) {
				a := append([]string{"restore", "--data-dir", dir}, est.drArgs()...)
				a = append(a, "--pg-restore", est.bin("pg_restore"), "--pg-dump", est.bin("pg_dump"), "--in", bundle, "--passphrase-file", pf)
				return runDR(t, append(a, extra...)...)
			}
			untouched := func(t *testing.T, what string) {
				t.Helper()
				if late() != 1 || orgs() != orgsBefore {
					t.Fatalf("%s changed the live estate: late=%d orgs=%d (want 1 and %d)", what, late(), orgs(), orgsBefore)
				}
				if got := readFileOrFatal(t, keyFiles[0]); string(got) != string(keyBefore) {
					t.Fatalf("%s replaced the signing key", what)
				}
			}
			decl := []string{"--operator", "oncall@example.test", "--reason", "INC-3040 restore over live"}

			t.Run("no declaration: refused", func(t *testing.T) {
				_, err := restore(dataDir)
				if err == nil || !strings.Contains(err.Error(), "--operator") {
					t.Fatalf("a restore over an occupied target needs the declaration, got: %v", err)
				}
				untouched(t, "the undeclared restore")
			})

			t.Run("declared, not forced, keys on disk: refused", func(t *testing.T) {
				_, err := restore(dataDir, decl...)
				if err == nil || !strings.Contains(err.Error(), "--force") {
					t.Fatalf("a declared restore over existing custody must ask for --force, got: %v", err)
				}
				untouched(t, "the unforced restore")
			})

			t.Run("declared, not forced, fresh data dir: refused whole", func(t *testing.T) {
				// Custody arrives in the bundle on a DR host, so the data dir is empty:
				// the only thing between this command and the live estate is the
				// restore itself, which must fail as one transaction.
				_, err := restore(t.TempDir(), decl...)
				if err == nil || !strings.Contains(err.Error(), "SINGLE TRANSACTION") || !strings.Contains(err.Error(), "nothing was dropped") {
					t.Fatalf("a restore over an occupied target without --force must fail as one transaction, got: %v", err)
				}
				untouched(t, "the unforced restore into a fresh data dir")
			})

			t.Run("forced but not declared: refused", func(t *testing.T) {
				_, err := restore(dataDir, "--force")
				if err == nil || !strings.Contains(err.Error(), "--operator") {
					t.Fatalf("--force alone must not replace an estate, got: %v", err)
				}
				untouched(t, "the undeclared forced restore")
			})

			t.Run("declared and forced, but a live object blocks a drop: refused whole", func(t *testing.T) {
				// A view over orgs is not in the backup, so --clean leaves it, and it
				// stops DROP TABLE orgs: the one transaction must fail and take
				// nothing with it.
				est.superExecInDB(t, `CREATE VIEW live_only_view AS SELECT id FROM orgs`)
				defer est.superExecInDB(t, `DROP VIEW IF EXISTS live_only_view`)
				_, err := restore(dataDir, append([]string{"--force"}, decl...)...)
				if err == nil || !strings.Contains(err.Error(), "nothing was dropped") {
					t.Fatalf("a blocked drop must fail the replacement whole, got: %v", err)
				}
				untouched(t, "the blocked replacement")
				kept := assertOnlySafetyCopy(t, dataDir)
				if len(kept) != 1 || !strings.Contains(err.Error(), kept[0]) {
					t.Fatalf("the failure must name the one safety copy it kept (%v): %v", kept, err)
				}
				for _, k := range kept {
					if rmErr := os.Remove(k); rmErr != nil {
						t.Fatal(rmErr)
					}
				}
			})

			t.Run("declared and forced onto an EMPTY database: no admin role needed, no copy taken", func(t *testing.T) {
				// Keys in the data dir alone make the restore a declared one; the
				// database holds nothing, so there is nothing to copy or to drop.
				keysDir := t.TempDir()
				for _, k := range keyFiles {
					if err := os.WriteFile(filepath.Join(keysDir, filepath.Base(k)), readFileOrFatal(t, k), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				empty := provisionPGSplitFixture(t, server.superDSN(), bin, "emp", split)
				a := []string{"restore", "--data-dir", keysDir, "--engine", "postgres", "--dsn", empty.appDSN, "--admin-dsn", empty.adminDSN}
				if empty.ownerDSN != "" {
					a = append(a, "--owner-dsn", empty.ownerDSN)
				}
				a = append(a, "--pg-restore", empty.bin("pg_restore"), "--in", bundle, "--passphrase-file", pf, "--force")
				if out, err := runDR(t, append(a, decl...)...); err != nil {
					t.Fatalf("a forced restore onto an empty database failed: %v\n%s", err, out)
				}
				if copies, _ := filepath.Glob(filepath.Join(keysDir, "database.pgcustom.*")); len(copies) != 0 {
					t.Fatalf("a safety copy of an EMPTY database was taken: %v", copies)
				}
				if empty.superScalar(t, `SELECT count(*) FROM orgs`) != orgsBefore-1 {
					t.Fatal("the backup was not restored onto the empty database")
				}
			})

			t.Run("declared and forced: replaces the estate and records it", func(t *testing.T) {
				t.Logf("PostgreSQL %d, %s: the estate holds %d relations in schema public; max_locks_per_transaction=%s max_connections=%s",
					server.major, postureName(split),
					est.superScalar(t, `SELECT count(*) FROM pg_catalog.pg_class WHERE relnamespace = 'public'::regnamespace`),
					server.scalar(`SHOW max_locks_per_transaction`), server.scalar(`SHOW max_connections`))
				// An object the live estate lost and the backup holds is created
				// (--if-exists lets the drop of the absent one pass).
				est.superExecInDB(t, `DROP TABLE public.account_offers CASCADE`)
				out, err := restore(dataDir, append([]string{"--force"}, decl...)...)
				if err != nil {
					t.Fatalf("a declared, forced restore over an occupied target failed: %v\n%s", err, out)
				}
				if est.superScalar(t, `SELECT (to_regclass('public.account_offers') IS NOT NULL)::int`) != 1 {
					t.Fatal("a table the backup holds and the live estate had lost was not recreated")
				}
				// The database it replaced is kept as a pg_dump, readable by its owner only.
				safety, _ := filepath.Glob(filepath.Join(dataDir, "database.pgcustom.pre-restore-*"))
				if len(safety) != 1 {
					t.Fatalf("want exactly one safety copy of the replaced database, got %v", safety)
				}
				if info, err := os.Stat(safety[0]); err != nil || info.Size() == 0 || info.Mode().Perm() != 0o600 {
					t.Fatalf("the safety copy must be a non-empty 0600 file: %v %v", info, err)
				}
				if late() != 0 {
					t.Fatal("the post-backup organisation survived: the estate was not replaced by the backup")
				}
				if got := orgs(); got != orgsBefore-1 {
					t.Fatalf("orgs after the restore = %d, want %d (the backup's)", got, orgsBefore-1)
				}
				// The act is in the restored ledger, under the declared operator.
				if got := est.superScalar(t, `SELECT count(*) FROM audit_events
				    WHERE action = $1 AND actor = $2 AND meta::text LIKE $3`,
					drRestoreDeclarationAction, "operator:oncall@example.test", "%INC-3040 restore over live%"); got != 1 {
					t.Fatalf("the restore was recorded %d times in the restored ledger, want 1", got)
				}
				// The previous custody stayed on disk for recovery.
				if kept, _ := filepath.Glob(filepath.Join(dataDir, "*.pre-restore-*")); len(kept) == 0 {
					t.Fatal("the previous signing keys were not preserved as *.pre-restore-*")
				}
				// The restored tables are owned as the posture requires.
				wantOwner := "app_"
				if split {
					wantOwner = "owner_"
				}
				if wrong := est.superScalar(t,
					`SELECT count(*) FROM pg_catalog.pg_class c
					   JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
					  WHERE n.nspname = 'public' AND c.relkind IN ('r','p')
					    AND pg_catalog.pg_get_userbyid(c.relowner) NOT LIKE $1`, wantOwner+"%"); wrong != 0 {
					t.Fatalf("%d tables are not owned by the %s role after the replacement", wrong, strings.TrimSuffix(wantOwner, "_"))
				}
			})
		})
	}
}
