// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// seedDataDir boots a file-backed engine in dir, seeds a tenant with a signed,
// checkpointed ledger, and closes it — leaving the data dir (olivares.db +
// audit-signing.key + catalog-signing.key) a real DR backup operates on.
func seedDataDir(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("seed boot: %v", err)
	}
	var tid model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tid = o.TenantID
		return e
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("create org: %v", err)
	}
	if err := eng.store.Mutate(ctx, tid, func(sc store.Scope) error {
		for i := 0; i < 4; i++ {
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:x", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("append: %v", err)
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		_ = eng.Close()
		t.Fatalf("checkpoint: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close seed engine: %v", err)
	}
}

func runDR(args ...string) (string, error) {
	cmd := newDRCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// TestDRBackupVerifyRestoreCLI is the end-to-end CLI path: seed → backup →
// verify (DR drill) → inspect → restore-into-fresh-dir, all green; plus the
// wrong-passphrase failure.
func TestDRBackupVerifyRestoreCLI(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)

	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")

	out, err := runDR("backup", "--data-dir", src, "--engine", "sqlite", "--out", bundle, "--passphrase-file", pf)
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("bundle not written: %v", err)
	}

	out, err = runDR("verify", "--in", bundle, "--passphrase-file", pf)
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "DR drill PASSED") {
		t.Fatalf("verify output unexpected:\n%s", out)
	}

	out, err = runDR("inspect", "--in", bundle)
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	if !strings.Contains(out, "olivares.dr.manifest.v1") || !strings.Contains(out, "head_seq") {
		t.Fatalf("inspect output unexpected:\n%s", out)
	}

	dst := t.TempDir()
	out, err = runDR("restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--passphrase-file", pf, "--force")
	if err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restore verified") {
		t.Fatalf("restore output unexpected:\n%s", out)
	}
	// The restored data dir must hold BOTH signing keys (audit + catalog) and the
	// store — the multiple-key custody path, exercised end-to-end.
	for _, f := range []string{"audit-signing.key", "catalog-signing.key", "olivares.db"} {
		if _, err := os.Stat(filepath.Join(dst, f)); err != nil {
			t.Fatalf("restored data dir missing %s: %v", f, err)
		}
	}
}

func TestDRWrongPassphraseFails(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := filepath.Join(t.TempDir(), "pass")
	wrong := filepath.Join(t.TempDir(), "wrong")
	_ = os.WriteFile(pf, []byte("right one"), 0o600)
	_ = os.WriteFile(wrong, []byte("nope"), 0o600)
	bundle := filepath.Join(t.TempDir(), "b.drbundle")

	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if _, err := runDR("verify", "--in", bundle, "--passphrase-file", wrong); err == nil {
		t.Fatal("verify with the wrong passphrase should fail")
	}
}

func TestDRBackupRequiresKEK(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	bundle := filepath.Join(t.TempDir(), "b.drbundle")
	_, err := runDR("backup", "--data-dir", src, "--out", bundle)
	if err == nil {
		t.Fatal("backup without a KEK should fail (keys must never be stored in the clear)")
	}
}

// TestDRBackupPostgresRequiresAdminDSNForDirectDump pins the credential
// selection of the DIRECT pg_dump path (no --snapshot-file, no --pitr-ref): the
// dump must run on the BYPASSRLS admin DSN, because pg_dump keeps
// row_security=off and aborts as the application role under FORCE ROW LEVEL
// SECURITY. The command refuses upfront, naming the flag, rather than launching
// a dump that can only fail — the same invariant Helm and the Operator hold for
// their pg_dump initContainers, applied to the binary's own default path.
func TestDRBackupPostgresRequiresAdminDSNForDirectDump(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "b.drbundle")
	_, err := runDR("backup", "--data-dir", src, "--engine", "postgres",
		"--dsn", "postgres://app:x@127.0.0.1:1/never-dialled", "--out", bundle,
		"--passphrase-file", pf)
	if err == nil || !strings.Contains(err.Error(), "--admin-dsn") {
		t.Fatalf("direct postgres dump without --admin-dsn must refuse naming the flag; got: %v", err)
	}

	// The guard firing is not enough: pin WHICH DSN reaches pg_dump. A mutation
	// that keeps the guard but passes the app DSN again would otherwise survive.
	const appDSN = "postgres://app:x@127.0.0.1:1/app-db"
	const adminDSN = "postgres://admin:x@127.0.0.1:1/admin-db"
	var seen string
	orig := pgDumpRunner
	pgDumpRunner = func(_ context.Context, _, dsn, out string) error {
		seen = dsn
		return os.WriteFile(out, []byte("fake custom-format dump"), 0o600)
	}
	t.Cleanup(func() { pgDumpRunner = orig })

	_, _ = runDR("backup", "--data-dir", src, "--engine", "postgres",
		"--dsn", appDSN, "--admin-dsn", adminDSN, "--out", bundle, "--passphrase-file", pf)
	if seen != adminDSN {
		t.Errorf("pg_dump received %q, want the ADMIN DSN %q (the application DSN cannot dump under FORCE RLS)", seen, adminDSN)
	}
}

// TestPruneOldBundles pins the shell-free retention helper: it deletes sibling
// *.drbundle files older than N days, never the bundle just written, and never a
// non-bundle file.
func TestPruneOldBundles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, ageDays int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-time.Duration(ageDays) * 24 * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := write("olivares-dr-old.drbundle", 10)
	recent := write("olivares-dr-recent.drbundle", 1)
	keep := write("olivares-dr-keep.drbundle", 0)
	other := write("notes.txt", 30)

	var buf bytes.Buffer
	pruneOldBundles(dir, keep, 7, now, &buf)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old bundle (10d, retain 7) should be pruned, stat err=%v", err)
	}
	for _, p := range []string{recent, keep, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should survive prune: %v", filepath.Base(p), err)
		}
	}
	if !strings.Contains(buf.String(), "pruned bundle older than 7d") {
		t.Errorf("prune did not report the deletion:\n%s", buf.String())
	}
}

// TestDRBackupRetainDaysWiring proves --retain-days is wired into the backup
// command: a stale sibling bundle in the --out dir is pruned by a real run.
func TestDRBackupRetainDaysWiring(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	outDir := t.TempDir()
	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(outDir, "olivares-dr-stale.drbundle")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(outDir, "olivares-dr-new.drbundle")
	out, err := runDR("backup", "--data-dir", src, "--engine", "sqlite", "--out", bundle, "--passphrase-file", pf, "--retain-days", "14")
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("new bundle missing: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale bundle (30d, retain 14) should be pruned, stat err=%v", err)
	}
}
