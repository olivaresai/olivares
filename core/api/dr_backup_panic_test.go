// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// A panic inside a backup must publish a failed job, never a completed one. The scheduler recovers panics,
// so a success published before the unwind would survive as a false success with no bundle behind it.
func TestDRBackupPanicPublishesFailure(t *testing.T) {
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
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, func(o *api.Options) {
		o.Signer = signer
		o.Version = "26.1002"
		o.DR = &api.DRConfig{DataDir: dir, EngineKind: "postgres", PostgresSnapshot: func(context.Context, string) (api.DRBackupSnapshot, error) {
			panic("snapshot callback panicked")
		}}
	})
	admin := h.adminLogin()

	// The startup path runs the job on this goroutine, so the panic reaches the caller. Recover it, then read
	// what the job published before it unwound.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the snapshot callback did not run")
			}
		}()
		_ = h.srv.RunStartupBackup(ctx, "console-backup-test-passphrase", "", "panic test")
	}()

	jobs := h.do("GET", "/v1/console/dr/jobs", admin, nil, nil)
	if jobs.code != 200 {
		t.Fatalf("jobs: %d %s", jobs.code, jobs.raw)
	}
	items := jobs.body["items"].([]any)
	job := items[0].(map[string]any)
	if message, _ := job["error"].(string); job["status"] != "failed" || message == "" {
		t.Fatalf("a panicking backup must publish failed with an error: job=%s", jobs.raw)
	}
	if bundles, _ := filepath.Glob(filepath.Join(dir, "backups", "*.drbundle")); len(bundles) != 0 {
		t.Fatalf("a panicking backup must publish no bundle: %v", bundles)
	}
}
