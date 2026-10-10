// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

func TestDRPostgresBackupUsesConfiguredSnapshot(t *testing.T) {
	for _, failure := range []string{"", "native snapshot refused", "snapshot changed before bundling", "unverified tenant"} {
		name := failure
		if name == "" {
			name = "authenticated bundle"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			calls, validations := 0, 0
			ctx := context.Background()
			_, key, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := audit.NewSigner(key)
			if err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(dir, "olivares.db")
			st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dbPath, SignEvent: signer.SignEvent}, nil)
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
				o.DR = &api.DRConfig{DataDir: dir, EngineKind: "postgres", PostgresSnapshot: func(ctx context.Context, out string) (api.DRBackupSnapshot, error) {
					calls++
					if err := ctx.Err(); err != nil {
						return api.DRBackupSnapshot{}, err
					}
					if failure == "native snapshot refused" {
						return api.DRBackupSnapshot{}, errors.New(failure)
					}
					// A stand-in for the native snapshot boundary; native PG format/roles are
					// checked by the command integration and the containing-binary journey.
					if err := os.WriteFile(out, []byte("snapshot bytes"), 0600); err != nil {
						return api.DRBackupSnapshot{}, err
					}
					sum, size, err := dr.FileSHA256(out)
					return api.DRBackupSnapshot{
						Store: dr.StoreSnapshot{Method: dr.MethodPgDump, File: "store/dump.pgcustom", SHA256: sum, SizeBytes: size},
						Validate: func() error {
							validations++
							if failure == "snapshot changed before bundling" {
								return errors.New(failure)
							}
							return nil
						},
					}, err
				}}
			})
			admin := h.adminLogin()
			if failure == "unverified tenant" {
				if err := signer.CheckpointAll(ctx, st); err != nil {
					t.Fatal(err)
				}
				// Corrupt a stored checkpoint, as opposed to mocking a verification result.
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec("DROP TRIGGER audit_events_no_update"); err != nil {
					t.Fatal(err)
				}
				changed, err := db.Exec("UPDATE audit_events SET sig = randomblob(64) WHERE action = 'audit.checkpoint'")
				if err != nil {
					t.Fatal(err)
				}
				if n, err := changed.RowsAffected(); err != nil || n == 0 {
					t.Fatalf("checkpoint tampering: rows=%d err=%v", n, err)
				}
			}
			const passphrase = "console-backup-test-passphrase"
			response := h.do("POST", "/v1/console/dr/backup", admin, map[string]any{"passphrase": passphrase}, nil)
			if response.code != http.StatusAccepted {
				t.Fatalf("backup: %d %s", response.code, response.raw)
			}
			waitForDRJob(t, h, admin, response)
			jobs := h.do("GET", "/v1/console/dr/jobs", admin, nil, nil)
			if jobs.code != http.StatusOK {
				t.Fatalf("jobs: %d %s", jobs.code, jobs.raw)
			}
			items := jobs.body["items"].([]any)
			job := items[0].(map[string]any)
			if calls != 1 {
				t.Fatalf("native snapshots = %d, want 1; job=%s", calls, jobs.raw)
			}
			bundles, err := filepath.Glob(filepath.Join(dir, "backups", "*.drbundle"))
			if err != nil {
				t.Fatal(err)
			}
			snapshots, err := filepath.Glob(filepath.Join(dir, "backups", "snapshot-*"))
			if err != nil || len(snapshots) != 0 {
				t.Fatalf("snapshot cleanup: %v %v", snapshots, err)
			}
			if failure != "" {
				if job["status"] != "failed" || !strings.Contains(job["error"].(string), failure) || len(bundles) != 0 {
					t.Fatalf("refusal must publish no bundle: job=%s bundles=%v", jobs.raw, bundles)
				}
				return
			}
			if job["status"] != "completed" || validations != 1 || len(bundles) != 1 {
				t.Fatalf("backup incomplete: job=%s validations=%d bundles=%v", jobs.raw, validations, bundles)
			}
			f, err := os.Open(bundles[0])
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			extracted := t.TempDir()
			manifest, params, err := dr.ExtractBundle(f, extracted)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.EngineKind != "postgres" || manifest.TipMatch != dr.TipAdvisory || manifest.Store.Method != dr.MethodPgDump {
				t.Fatalf("wrong PostgreSQL manifest: %+v", manifest)
			}
			for _, tip := range manifest.Tenants {
				if !tip.VerifiedAtBackup || tip.Checkpoints != 0 {
					t.Fatalf("fresh estate must verify with pending checkpoints: %+v", tip)
				}
			}
			cipher, err := dr.OpenCipher([]byte(passphrase), params)
			if err != nil {
				t.Fatal(err)
			}
			if err := dr.VerifyBundleIntegrity(extracted, manifest, params, cipher, false); err != nil {
				t.Fatal(err)
			}
			bytes, err := os.ReadFile(filepath.Join(extracted, manifest.Store.File))
			if err != nil || string(bytes) != "snapshot bytes" {
				t.Fatalf("bundled snapshot: %q %v", bytes, err)
			}
		})
	}
}
