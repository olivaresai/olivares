// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
	"golang.org/x/sys/unix"
)

func TestDRBackupPublishesOnlyCompleteBundle(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db"), SignEvent: signer.SignEvent}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit-signing.key"), []byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}

	opened := make(chan *os.File, 1)
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, func(o *api.Options) {
		o.Signer = signer
		o.DR = &api.DRConfig{DataDir: dir, EngineKind: "postgres", PostgresSnapshot: func(ctx context.Context, out string) (api.DRBackupSnapshot, error) {
			payload := []byte("snapshot bytes")
			if err := os.WriteFile(out, payload, 0600); err != nil {
				return api.DRBackupSnapshot{}, err
			}
			sum, size, err := dr.FileSHA256(out)
			return api.DRBackupSnapshot{Store: dr.StoreSnapshot{Method: dr.MethodPgDump, File: "store/dump.pgcustom", SHA256: sum, SizeBytes: size}, Validate: func() error {
				// Hold the sealing reader after the bundle writer has opened its output.
				if err := os.Remove(out); err != nil {
					return err
				}
				if err := unix.Mkfifo(out, 0600); err != nil {
					return err
				}
				go func() { f, _ := os.OpenFile(out, os.O_WRONLY, 0600); opened <- f }()
				return nil
			}}, err
		}}
	})
	admin := h.adminLogin()
	done := make(chan error, 1)
	go func() { done <- h.srv.RunStartupBackup(ctx, "console-backup-test-passphrase", "", "publication test") }()
	var writer *os.File
	select {
	case writer = <-opened:
	case err := <-done:
		t.Fatalf("backup ended before sealing: %v", err)
	case <-time.After(time.Minute):
		t.Fatal("snapshot sealing did not start")
	}
	if writer == nil {
		t.Fatal("open snapshot pipe")
	}
	listed := h.do("GET", "/v1/console/dr/backups", admin, nil, nil)
	// Restore the path for the later digest read; sealing keeps its open pipe.
	removeErr := os.Remove(writer.Name())
	restoreErr := os.WriteFile(writer.Name(), []byte("snapshot bytes"), 0600)
	// Release before reporting an assertion so the job always finishes.
	_, writeErr := fmt.Fprint(writer, "snapshot bytes")
	closeErr := writer.Close()
	if err := <-done; err != nil {
		t.Fatalf("backup: %v", err)
	}
	if removeErr != nil || restoreErr != nil || writeErr != nil || closeErr != nil {
		t.Fatalf("snapshot pipe: %v %v %v %v", removeErr, restoreErr, writeErr, closeErr)
	}
	if listed.code != 200 || len(listed.body["items"].([]any)) != 0 {
		t.Errorf("unfinished bundle was published: %d %s", listed.code, listed.raw)
	}
	listed = h.do("GET", "/v1/console/dr/backups", admin, nil, nil)
	items := listed.body["items"].([]any)
	if listed.code != 200 || len(items) != 1 {
		t.Fatalf("completed bundle missing: %s", listed.raw)
	}
	item := items[0].(map[string]any)
	if item["size_bytes"].(float64) <= 0 {
		t.Fatalf("empty completed bundle: %s", listed.raw)
	}
}
