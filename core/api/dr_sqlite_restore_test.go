// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const liveSigningKey = "live signing key\n"

func TestConsoleRestoreRefusesLateJobUntilRestart(t *testing.T) {
	for _, failKey := range []bool{false, true} {
		t.Run(fmt.Sprint("keyFailure=", failKey), func(t *testing.T) {
			var sabotage []func(string)
			if failKey {
				sabotage = append(sabotage, func(dir string) { removeStagedConsoleKey(t, dir) })
			}
			first, dataDir, _, s := runConsoleRestoreJob(t, false, false, sabotage...)
			if first.Status != drJobCompleted && !(failKey && first.Status == drJobFailed) {
				t.Fatalf("first restore did not reach maintenance: %s", first.Error)
			}
			var before []byte
			if !failKey {
				var err error
				before, err = os.ReadFile(filepath.Join(dataDir, "audit-signing.key"))
				if err != nil {
					t.Fatal(err)
				}
			}
			bundle, _ := buildTestBundle(t)
			second := s.drSvc.jobs.create(drJobRestore, "already-authorized late job")
			s.runRestore(context.Background(), second.ID, bundle, "correct horse battery staple", "test")
			got, _ := s.drSvc.jobs.get(second.ID)
			if got.Status != drJobFailed || !strings.Contains(got.Error, "live store is stopped") {
				t.Fatalf("late restore did not refuse before touching the stopped store: status=%s error=%s", got.Status, got.Error)
			}
			if s.drSvc.maintenanceJob != first.ID {
				t.Fatal("late restore replaced the recovery job")
			}
			if !failKey {
				after, err := os.ReadFile(filepath.Join(dataDir, "audit-signing.key"))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("late restore replaced the restored custody")
				}
			}
		})
	}
}

// runConsoleRestoreJob runs the console restore job for a real bundle against a data dir
// whose live olivares.db is a WAL-mode SQLite file held open by the returned handle. With
// hold set, a live writer keeps its transaction open for the whole restore.
func runConsoleRestoreJob(t *testing.T, hold, failKey bool, afterStage ...func(string)) (drJob, string, *sql.DB, *Server) {
	t.Helper()
	bundle, _ := buildTestBundle(t)
	return runConsoleRestoreBundle(t, bundle, hold, failKey, afterStage...)
}

func runConsoleRestoreBundle(t *testing.T, bundle string, hold, failKey bool, afterStage ...func(string)) (drJob, string, *sql.DB, *Server) {
	t.Helper()
	dataDir := t.TempDir()
	managed, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dataDir, "olivares.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	live, err := sql.Open("sqlite", filepath.Join(dataDir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	if _, err := live.Exec("PRAGMA journal_mode=WAL; CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES ('live')"); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dataDir, "audit-signing.key")
	if failKey {
		if err := os.Mkdir(keyPath, 0700); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(keyPath, []byte(liveSigningKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if hold {
		tx, err := live.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		if _, err := tx.Exec("UPDATE marker SET value='held'"); err != nil {
			t.Fatal(err)
		}
	}
	svc := newDRService(DRConfig{DataDir: dataDir, EngineKind: "sqlite", QuiesceStore: func(ctx context.Context) error {
		for _, fn := range afterStage {
			fn(dataDir)
		}
		return managed.(interface{ QuiesceForSQLiteRestore(context.Context) error }).QuiesceForSQLiteRestore(ctx)
	}})
	s := &Server{st: managed, log: slog.New(slog.NewTextHandler(io.Discard, nil)), drSvc: svc, version: "26.1001"}
	job := svc.jobs.create(drJobRestore, "test")
	s.runRestore(context.Background(), job.ID, bundle, "correct horse battery staple", "test")
	got, ok := svc.jobs.get(job.ID)
	if !ok {
		t.Fatal("restore job disappeared from the tracker")
	}
	return got, dataDir, live, s
}

// A key name from the bundle's manifest becomes a file in the data dir: only a plain name
// may, and never one of the store's own files.
func TestValidRestoredKeyName(t *testing.T) {
	for _, name := range []string{"audit-signing.key", "catalog-signing.key", "policy-signing.key"} {
		if err := validRestoredKeyName(name); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", "../x", "a/b", "/etc/passwd", "olivares.db", "olivares.db-wal", "olivares.db-shm", ".gitignore"} {
		if err := validRestoredKeyName(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

// A restore the live store refuses must leave the signing keys alone: keys from the bundle
// beside the old store would not boot after the restart.
func TestConsoleRestoreRefusedByTheStoreLeavesTheSigningKeys(t *testing.T) {
	job, dataDir, _, _ := runConsoleRestoreJob(t, true, false)
	if job.Status != drJobFailed || !strings.Contains(job.Error, "restore store") {
		t.Fatalf("a restore against a held writer: status=%q error=%q, want a refusal at restore store", job.Status, job.Error)
	}
	key, err := os.ReadFile(filepath.Join(dataDir, "audit-signing.key"))
	if err != nil || string(key) != liveSigningKey {
		t.Fatalf("a refused restore replaced the signing key: %q err=%v", key, err)
	}
}

// After a restore the live handle sees the restored data, the store is still WAL, a live
// write still works, and the keys are the bundle's.
func TestConsoleRestoreLeavesTheLiveStoreWritableAndTheKeysRestored(t *testing.T) {
	job, dataDir, live, _ := runConsoleRestoreJob(t, false, false)
	if job.Status != drJobCompleted {
		t.Fatalf("restore job: status=%q phase=%q error=%q", job.Status, job.Phase, job.Error)
	}
	var markers int
	if err := live.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='marker'").Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("the live handle still sees the pre-restore table: count=%d err=%v", markers, err)
	}
	var mode string
	if err := live.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode after the restore = %q err=%v, want wal", mode, err)
	}
	if _, err := live.Exec("CREATE TABLE after_restore(x INTEGER); INSERT INTO after_restore VALUES (1)"); err != nil {
		t.Fatalf("a live write after the restore: %v", err)
	}
	key, err := os.ReadFile(filepath.Join(dataDir, "audit-signing.key"))
	if err != nil || len(key) == 0 || string(key) == liveSigningKey {
		t.Fatalf("the restored signing key is not the bundle's: %q err=%v", key, err)
	}
}

// A refused promotion must preserve committed transactions that still live in
// the WAL. Replacing files outside SQLite ignores its active writer lock.
func TestSQLiteRestoreRefusesBusyDestinationPreservingCommittedWAL(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "live.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES ('backup')"); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := dr.SnapshotSQLite(ctx, dbPath, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE marker SET value='newer committed data'"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath + "-wal")
	if err != nil || len(before) == 0 {
		t.Fatalf("read committed WAL: bytes=%d err=%v", len(before), err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("UPDATE marker SET value='uncommitted writer'"); err != nil {
		t.Fatal(err)
	}
	err = promoteSQLiteSnapshot(ctx, snapshot, dbPath)
	if err == nil {
		t.Fatal("restore ignored the active destination writer")
	}
	if msg := strings.ToLower(err.Error()); !strings.Contains(msg, "locked") && !strings.Contains(msg, "busy") {
		t.Fatalf("the restore was refused for another reason than the held writer: %v", err)
	}
	after, err := os.ReadFile(dbPath + "-wal")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refused restore changed the committed WAL: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM marker").Scan(&value); err != nil || value != "newer committed data" {
		t.Fatalf("refused restore lost committed data: value=%q err=%v", value, err)
	}
}

// A different installation's bundle carries a different audit key. The boot-bound
// store must reject all writes until restart rather than sign the restored chain
// with the previous installation's key.
func TestConsoleRestoreDifferentKeyRefusesLiveWritesUntilRestart(t *testing.T) {
	ctx := context.Background()
	bundle, _ := buildTestBundle(t)
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "audit-signing.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv)), 0600); err != nil {
		t.Fatal(err)
	}
	live, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db"), SignEvent: signer.SignEvent}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	svc := newDRService(DRConfig{DataDir: dir, EngineKind: "sqlite", QuiesceStore: live.(interface{ QuiesceForSQLiteRestore(context.Context) error }).QuiesceForSQLiteRestore})
	s := &Server{st: live, signer: signer, log: slog.Default(), drSvc: svc, version: "26.1001"}
	job := svc.jobs.create(drJobRestore, "test")
	s.runRestore(ctx, job.ID, bundle, "correct horse battery staple", "test")
	got, _ := svc.jobs.get(job.ID)
	if got.Status != drJobCompleted {
		t.Fatalf("restore failed: %s", got.Error)
	}
	var tid model.TenantID
	raw, err := sql.Open("sqlite", filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.QueryRow("SELECT tenant_id FROM orgs WHERE slug='acme'").Scan(&tid); err != nil {
		t.Fatal(err)
	}
	err = live.Mutate(ctx, tid, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: "old engine", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent"})
		return err
	})
	if err == nil {
		t.Fatal("old-key engine appended to the restored ledger before restart")
	}
	restoredBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(restoredBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(restoredKey, priv) {
		t.Fatal("restore did not replace audit custody")
	}
	restoredSigner, err := audit.NewSigner(ed25519.PrivateKey(restoredKey))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db"), SignEvent: restoredSigner.SignEvent}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.Mutate(ctx, tid, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: "restarted engine", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.View(ctx, tid, func(sc store.Scope) error {
		rep, err := audit.VerifyEvents(ctx, sc.Audit(), restoredSigner.PublicKey())
		if err != nil {
			return err
		}
		if !rep.OK {
			t.Fatalf("restored ledger signatures failed: %+v", rep)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func removeStagedConsoleKey(t *testing.T, dir string) {
	t.Helper()
	keys, err := filepath.Glob(filepath.Join(dir, ".dr-staging-*", "keys", "audit-signing.key"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("staged keys=%d err=%v", len(keys), err)
	}
	if err := os.Remove(keys[0]); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleRestoreKeyPromotionFailureRollsBackBeforeRestart(t *testing.T) {
	job, dir, live, s := runConsoleRestoreJob(t, false, false, func(dir string) { removeStagedConsoleKey(t, dir) })
	if job.Status != drJobFailed || !strings.Contains(job.Error, "promote key audit-signing.key") || !strings.Contains(job.Error, "rolled back") || !job.restartSafe {
		t.Fatalf("key failure did not roll back safely: %s", job.Error)
	}
	var value string
	if err := live.QueryRow("SELECT value FROM marker").Scan(&value); err != nil || value != "live" {
		t.Fatalf("rollback lost live database: value=%q err=%v", value, err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "audit-signing.key"))
	if err != nil || string(key) != liveSigningKey {
		t.Fatal("rollback lost the old signing key")
	}
	rec := httptest.NewRecorder()
	s.restoreMaintenance(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("served during maintenance") })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "Restart the engine") || strings.Contains(rec.Body.String(), "Do not restart") {
		t.Fatalf("unsafe rollback response: %s", rec.Body.String())
	}
}

// A completed console restore must leave a usable snapshot of the previous WAL
// database and private copies of every replaced key, and report both custody
// limits and the preservation location through the job stream.
func TestConsoleRestorePreservesPreviousStateAndReportsCustody(t *testing.T) {
	job, dir, _, _ := runConsoleRestoreJob(t, false, false)
	if job.Status != drJobCompleted {
		t.Fatal(job.Error)
	}
	copies, err := filepath.Glob(filepath.Join(dir, "olivares.db.pre-restore-*"))
	if err != nil || len(copies) != 1 {
		t.Fatalf("previous database snapshots=%d err=%v, want 1", len(copies), err)
	}
	old, err := sql.Open("sqlite", copies[0])
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var value string
	if err := old.QueryRow("SELECT value FROM marker").Scan(&value); err != nil || value != "live" {
		t.Fatalf("preserved committed state=%q err=%v", value, err)
	}
	copies, err = filepath.Glob(filepath.Join(dir, "audit-signing.key.pre-restore-*"))
	if err != nil || len(copies) != 1 {
		t.Fatalf("previous key copies=%d err=%v, want 1", len(copies), err)
	}
	b, err := os.ReadFile(copies[0])
	if err != nil || string(b) != liveSigningKey {
		t.Fatal("previous signing key was lost")
	}
	info, err := os.Stat(copies[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("preserved key is not private")
	}
	for _, name := range []string{"secret-store.key", "totp-seed.key", "sso-secret.key", "eventing-secret.key", "pre-restore-"} {
		if !strings.Contains(job.Notes, name) {
			t.Errorf("job omits custody/preservation %s: %s", name, job.Notes)
		}
	}
	if strings.Contains(job.Notes, "key custody intact") {
		t.Fatal("legacy bundle falsely certifies sealer custody")
	}
}

func TestConsoleRestoreInvalidKeyDestinationLeavesDatabaseUntouched(t *testing.T) {
	job, _, live, _ := runConsoleRestoreJob(t, false, true)
	if job.Status != drJobFailed || !job.restartSafe {
		t.Fatalf("invalid key path did not fail safely: %s", job.Error)
	}
	var value string
	if err := live.QueryRow("SELECT value FROM marker").Scan(&value); err != nil || value != "live" {
		t.Fatalf("key refusal replaced live database: value=%q err=%v", value, err)
	}
}

func TestConsoleRestoreUnsupportedEngineWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-signing.key")
	if err := os.WriteFile(path, []byte(liveSigningKey), 0600); err != nil {
		t.Fatal(err)
	}
	bundle, _ := buildTestBundle(t)
	svc := newDRService(DRConfig{DataDir: dir, EngineKind: "postgres"})
	s := &Server{log: slog.Default(), drSvc: svc, version: "26.1001"}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	job := svc.jobs.create(drJobRestore, "test")
	// The engine refusal must precede even opening the bundle.
	s.runRestore(context.Background(), job.ID, bundle+".missing", "correct horse battery staple", "test")
	got, _ := svc.jobs.get(job.ID)
	if got.Status != drJobFailed || !strings.Contains(got.Error, "engine") {
		t.Fatalf("wrong refusal: %s", got.Error)
	}
	after, err := os.ReadDir(dir)
	if err != nil || len(before) != len(after) {
		t.Fatal("unsupported engine wrote to the live data directory")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != liveSigningKey {
		t.Fatal("unsupported engine replaced a key")
	}
}

// A later key failure must undo both an overwritten key and a newly introduced
// key, then restore the old database, without consuming the recovery copies.
func TestConsoleRestorePartialKeyPromotionRollback(t *testing.T) {
	dir := t.TempDir()
	livePath := filepath.Join(dir, "olivares.db")
	live, err := sql.Open("sqlite", livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if _, err := live.Exec("CREATE TABLE marker(value TEXT); INSERT INTO marker VALUES ('live')"); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(dir, ".dr-staging-test")
	for _, sub := range []string{"keys", "previous"} {
		if err := os.MkdirAll(filepath.Join(stageDir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "audit-signing.key"), []byte(liveSigningKey), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "previous", "audit-signing.key"), []byte(liveSigningKey), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit-signing.key", "new.key"} {
		if err := os.WriteFile(filepath.Join(stageDir, "keys", name), []byte("synthetic restored key"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stage := &consoleRestoreStage{dir: stageDir, keys: []string{"audit-signing.key", "new.key", "later.key"}, previous: map[string]bool{"audit-signing.key": true}}
	if err := stage.preserve(context.Background(), dir, ".pre-restore-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec("UPDATE marker SET value='restored'"); err != nil {
		t.Fatal(err)
	}
	count, err := stage.promoteKeys(dir)
	if err == nil || count != 2 {
		t.Fatalf("later key did not fail after partial promotion: count=%d err=%v", count, err)
	}
	if err := stage.rollback(dir, count); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := live.QueryRow("SELECT value FROM marker").Scan(&value); err != nil || value != "live" {
		t.Fatalf("rollback did not restore database: value=%q err=%v", value, err)
	}
	key, err := os.ReadFile(filepath.Join(dir, "audit-signing.key"))
	if err != nil || string(key) != liveSigningKey {
		t.Fatal("rollback lost replaced key")
	}
	if _, err := os.Lstat(filepath.Join(dir, "new.key")); !os.IsNotExist(err) {
		t.Fatal("rollback left a new key in the old installation")
	}
	key, err = os.ReadFile(filepath.Join(dir, "audit-signing.key.pre-restore-test"))
	if err != nil || string(key) != liveSigningKey {
		t.Fatal("rollback consumed recovery copy")
	}
}

// A permission failure during key rollback must retain manual-recovery copies
// and tell the operator to recover custody before restarting.
func TestConsoleRestoreRollbackFailureRetainsRecoveryAdvice(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged filesystem permissions; runs on builder")
	}
	bundle, _ := buildTestBundleWithPassphrase(t, "correct horse battery staple", "later.key")
	job, dir, _, server := runConsoleRestoreBundle(t, bundle, false, false, func(dir string) {
		paths, err := filepath.Glob(filepath.Join(dir, ".dr-staging-*"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("staging dirs=%v err=%v", paths, err)
		}
		stage := paths[0]
		if err := os.Remove(filepath.Join(stage, "keys", "later.key")); err != nil {
			t.Fatal(err)
		}
		previous := filepath.Join(stage, "previous")
		if err := os.Chmod(previous, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(previous, 0700) })
	})
	if job.Status != drJobFailed || job.restartSafe || !strings.Contains(job.Error, "rollback failed") || !strings.Contains(job.Error, "rollback key audit-signing.key") {
		t.Fatalf("rollback failure lost unsafe recovery state: status=%s safe=%t err=%s", job.Status, job.restartSafe, job.Error)
	}
	if !strings.Contains(job.Notes, "pre-restore-"+job.ID) {
		t.Fatalf("missing preservation receipt: %s", job.Notes)
	}
	for _, name := range []string{"olivares.db", "audit-signing.key"} {
		if _, err := os.Stat(filepath.Join(dir, name) + ".pre-restore-" + job.ID); err != nil {
			t.Fatal(err)
		}
	}
	key, err := os.ReadFile(filepath.Join(dir, "audit-signing.key") + ".pre-restore-" + job.ID)
	if err != nil || string(key) != liveSigningKey {
		t.Fatal("lost original key recovery copy")
	}
	rec := httptest.NewRecorder()
	server.restoreMaintenance(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("served after unsafe rollback") })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "Do not restart") {
		t.Fatalf("unsafe restart advice: %s", rec.Body.String())
	}
}
