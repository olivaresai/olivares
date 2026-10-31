// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestDROffsiteScheduleLiveWriteOnlyCredential runs the engine's scheduled copy
// against the Object Lock bucket and write-only credential that
// scripts/dr-offsite-targets.sh provisions on MinIO. The ordinary suite skips it.
func TestDROffsiteScheduleLiveWriteOnlyCredential(t *testing.T) {
	endpoint := os.Getenv("OLIVARES_TEST_OFFSITE_ENDPOINT")
	lockBucket := os.Getenv("OLIVARES_TEST_OFFSITE_LOCK_BUCKET")
	if endpoint == "" || lockBucket == "" {
		t.Skip("run scripts/dr-offsite-targets.sh for the Object Lock qualification")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	secrets := auth.NewSecretStore(st, drTestSealer{})
	read := func(env string) string {
		t.Helper()
		b, err := os.ReadFile(os.Getenv(env))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	putCredential := func(name, access, secret, token string) {
		t.Helper()
		c := map[string]string{"access_key_id": read(access), "secret_access_key": read(secret)}
		if token != "" {
			c["session_token"] = read(token)
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := secrets.Put(ctx, auth.Principal{Kind: auth.KindUser, UserID: model.NewID()}, auth.GlobalSecretScope, name, string(raw), ""); err != nil {
			t.Fatal(err)
		}
	}
	putCredential("dr/writer", "OLIVARES_TEST_OFFSITE_WRITER_ACCESS_FILE", "OLIVARES_TEST_OFFSITE_WRITER_SECRET_FILE", "OLIVARES_TEST_OFFSITE_WRITER_TOKEN_FILE")
	putCredential("dr/root", "OLIVARES_TEST_OFFSITE_ACCESS_FILE", "OLIVARES_TEST_OFFSITE_SECRET_FILE", "")
	s := &Server{secretStore: secrets, st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	bundle := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("engine bundle "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// The bucket the script creates has Object Lock without a default retention:
	// the engine's unlocked puts land, and the console is told so.
	writer := &drOffsite{Endpoint: endpoint, Bucket: lockBucket, Region: "us-east-1", Prefix: "engine", CredentialRef: "store:dr/writer"}
	lock, err := s.replicateScheduledBackup(ctx, writer, bundle("a-old.drbundle"), time.Now())
	if err != nil || lock != "no_default" {
		t.Fatalf("first copy: lock=%q err=%v, want no_default and no error", lock, err)
	}
	time.Sleep(1100 * time.Millisecond) // LastModified has one-second resolution.
	writer.GFS = drGFS{KeepLast: 1}
	_, err = s.replicateScheduledBackup(ctx, writer, bundle("b-new.drbundle"), time.Now())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("retention DELETE with the write-only credential: %v, want the bucket's 403", err)
	}
	audited := 0
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(e model.AuditEvent) error {
			if e.Action == "dr.offsite.delete_failed" {
				audited++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Fatalf("refused offsite delete audit entries = %d, want 1", audited)
	}

	unlocked := &drOffsite{Endpoint: endpoint, Bucket: "dr-qualification", Region: "us-east-1", Prefix: "engine", CredentialRef: "store:dr/root"}
	if lock, err := s.replicateScheduledBackup(ctx, unlocked, bundle("c.drbundle"), time.Now()); err != nil || lock != "none" {
		t.Fatalf("bucket without Object Lock: lock=%q err=%v, want none", lock, err)
	}
}
