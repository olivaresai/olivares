// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/dr"
)

func TestDRApplianceFirstBootDefaultsAndKeyCustody(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	newAppliance := func() *harness {
		return newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, func(o *api.Options) {
			o.Version = "26.900"
			o.DR = &api.DRConfig{DataDir: dir, EngineKind: "sqlite", Appliance: true}
		})
	}
	h := newAppliance()
	keyPath := filepath.Join(dir, "dr-recovery.key")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(string(key))
	if err != nil || len(raw) != 32 {
		t.Fatal("recovery key must encode 32 random bytes")
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("recovery key must be private")
	}
	if r := h.do("GET", "/v1/server-info", "", nil, nil); r.body["backup_key_path"] != keyPath {
		t.Fatalf("setup named the wrong recovery file: %s", r.raw)
	}
	setup := map[string]any{"token": h.setupTok, "email": "root@x.io", "password": "supersecret1"}
	if r := h.do("POST", "/v1/setup", "", setup, nil); r.code != http.StatusBadRequest {
		t.Fatalf("setup without key custody = %d %s", r.code, r.raw)
	}
	setup["backup_key_saved"] = true
	if r := h.do("POST", "/v1/setup", "", setup, nil); r.code != http.StatusCreated {
		t.Fatalf("setup with key custody = %d %s", r.code, r.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatal(lr.raw)
	}
	admin := lr.body["token"].(string)
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.code != http.StatusOK || r.body["enabled"] != true || r.body["cron"] != "0 2 * * *" || r.body["verify_cron"] != "0 3 * * 0" {
		t.Fatalf("appliance defaults = %d %s", r.code, r.raw)
	}
	if ran, err := h.srv.RunDueScheduledBackup(context.Background(), time.Date(2026, 10, 28, 2, 1, 0, 0, time.UTC)); err != nil || !ran {
		t.Fatalf("default next-day backup: %v %v", ran, err)
	}
	r = h.do("GET", "/v1/console/dr/backups", admin, nil, nil)
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("default backup is absent from the page's listing: %s", r.raw)
	}
	// Disabling survives a restart; boot must never overwrite the operator's policy or key.
	if r := h.do("PUT", "/v1/console/dr/schedule", admin, map[string]any{"enabled": false, "cron": "0 2 * * *", "retain_days": 30}, nil); r.code != http.StatusOK {
		t.Fatal(r.raw)
	}
	h2 := newAppliance()
	if configured, err := h2.srv.ScheduledBackupConfigured(context.Background()); err != nil || !configured {
		// Verification remains configured independently of the backup toggle.
		t.Fatalf("verification should remain configured: %v %v", configured, err)
	}
	r = h2.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["enabled"] != false {
		t.Fatalf("restart re-enabled backups: %s", r.raw)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(after) != string(key) {
		t.Fatal("restart changed recovery key")
	}
	if ran, err := h2.srv.RunDueScheduledBackup(context.Background(), time.Date(2026, 10, 28, 2, 1, 0, 0, time.UTC)); err != nil || ran {
		t.Fatalf("disabled backup ran: %v %v", ran, err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	h3 := newAppliance()
	if r := h3.do("GET", "/v1/console/dr/schedule", admin, nil, nil); r.code != http.StatusNotImplemented {
		t.Fatalf("missing recovery key was accepted: %d %s", r.code, r.raw)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("boot regenerated a lost key")
	}
}

// Every install, not only the appliance, protects itself from its first start, and a
// schedule an administrator emptied on purpose stays empty across a restart.
func TestDRFirstStartDefaultsOnEveryInstall(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	h := newDRHarness(t, dir, st, "")
	admin := h.adminLogin()
	if info, err := os.Stat(filepath.Join(dir, "dr-recovery.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("first start left no private recovery key: %v", err)
	}
	if ran, err := h.srv.RunDueScheduledBackup(context.Background(), time.Date(2026, 10, 28, 2, 1, 0, 0, time.UTC)); err != nil || !ran {
		t.Fatalf("default backup: %v %v", ran, err)
	}
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.code != http.StatusOK || r.body["enabled"] != true || r.body["cron"] != "0 2 * * *" || r.body["verify_cron"] != "0 3 * * 0" {
		t.Fatalf("first-start defaults = %d %s", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/console/dr/schedule", admin, map[string]any{"enabled": false, "cron": "", "retain_days": 0, "verify_cron": ""}, nil); r.code != http.StatusOK {
		t.Fatal(r.raw)
	}
	r = newDRHarness(t, dir, st, "").do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["enabled"] != false || r.body["cron"] != "" || r.body["verify_cron"] != nil {
		t.Fatalf("restart replaced an emptied schedule: %s", r.raw)
	}
}

// An install upgraded with bundles made on demand and no stored schedule keeps
// them: the first-start default must not prune what the operator already holds,
// even when another node on the same store, with no bundles, starts first.
func TestDRFirstStartDefaultKeepsEarlierBundles(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	newDRHarness(t, t.TempDir(), st, "")
	passFile := filepath.Join(dir, "dr-pass.txt")
	if err := os.WriteFile(passFile, []byte("test-schedule-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := filepath.Join(backupDir, "olivares-operator-old.drbundle")
	f, err := os.Create(old)
	if err != nil {
		t.Fatal(err)
	}
	err = writeDRMetadataBundle(t, f, &dr.Manifest{
		Format:     dr.ManifestFormat,
		CreatedAt:  now.Add(-60 * 24 * time.Hour).Format(time.RFC3339),
		EngineKind: "sqlite",
		Store:      dr.StoreSnapshot{Method: dr.MethodPITR, File: "external"},
	})
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	h := newDRHarness(t, dir, st, passFile)
	if ran, err := h.srv.RunDueScheduledBackup(context.Background(), now); err != nil || !ran {
		t.Fatalf("scheduled run: ran=%v err=%v", ran, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("operator bundle from before the upgrade was deleted: %v", err)
	}
}

func TestDRAppliancePreservesExplicitPassphraseFile(t *testing.T) {
	dir := t.TempDir()
	pass := filepath.Join(dir, "operator-passphrase")
	if err := os.WriteFile(pass, []byte("operator-owned-backup-passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: openDRStore(t, dir)}, func(o *api.Options) {
		o.DR = &api.DRConfig{DataDir: dir, EngineKind: "sqlite", Appliance: true, PassphraseFile: pass}
	})
	r := h.do("GET", "/v1/server-info", "", nil, nil)
	if r.body["backup_key_path"] != pass {
		t.Fatalf("setup ignored the configured recovery file: %s", r.raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "dr-recovery.key")); !os.IsNotExist(err) {
		t.Fatal("appliance generated a second recovery mechanism despite an override")
	}
}

func TestDRScheduleVerificationAndOffsiteConfiguration(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	h := newDRHarness(t, dir, st, "")
	admin := h.adminLogin()
	input := map[string]any{
		"enabled": true, "cron": "0 2 * * *", "retain_days": 30,
		"verify_cron": "0 3 * * 0", "rpo_hours": 24,
		"offsite": map[string]any{"endpoint": "https://backup.example.com", "bucket": "recovery", "region": "auto", "credential_ref": "store:dr/offsite", "gfs": map[string]any{"daily": 7, "weekly": 4, "monthly": 12, "keep_last": 1}},
	}
	r := h.do("PUT", "/v1/console/dr/schedule", admin, input, nil)
	if r.code != http.StatusOK {
		t.Fatalf("save offsite = %d %s", r.code, r.raw)
	}
	h2 := newDRHarness(t, dir, st, "")
	r = h2.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	offsite, _ := r.body["offsite"].(map[string]any)
	if r.body["verify_cron"] != "0 3 * * 0" || offsite["credential_ref"] != "store:dr/offsite" {
		t.Fatalf("restart lost schedule fields: %s", r.raw)
	}
	for _, invalid := range []map[string]any{
		{"verify_cron": "bad"},
		{"offsite": map[string]any{"bucket": "recovery", "credential_ref": "file:/etc/shadow"}},
		{"offsite": map[string]any{"endpoint": "http://backup.example.com", "bucket": "recovery", "credential_ref": "store:dr/offsite"}},
		{"offsite": map[string]any{"endpoint": "https://user:password@backup.example.com", "bucket": "recovery", "credential_ref": "store:dr/offsite"}},
	} {
		invalid["cron"] = "0 2 * * *"
		if r := h.do("PUT", "/v1/console/dr/schedule", admin, invalid, nil); r.code != http.StatusBadRequest {
			t.Errorf("invalid config accepted: %d", r.code)
		}
	}
}

func TestDRAppliancePreservesExplicitDefaultRecoveryPath(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "missing"}[present], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "dr-recovery.key")
			const passphrase = "operator-owned-passphrase"
			if present {
				if err := os.WriteFile(path, []byte(passphrase), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: openDRStore(t, dir)}, func(o *api.Options) {
				o.DR = &api.DRConfig{DataDir: dir, EngineKind: "sqlite", Appliance: true, PassphraseFile: path}
			})
			configured, err := h.srv.ScheduledBackupConfigured(context.Background())
			if err != nil || configured != present {
				t.Fatalf("explicit recovery path configured=%v, want %v: %v", configured, present, err)
			}
			raw, err := os.ReadFile(path)
			if present && (err != nil || string(raw) != passphrase) {
				t.Fatal("explicit passphrase was replaced or rejected")
			}
			if !present && !os.IsNotExist(err) {
				t.Fatal("an explicitly configured missing recovery file was generated")
			}
		})
	}
}
