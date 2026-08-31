// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openDRStore opens a FILE-backed store rooted at dir (so the DR snapshot path
// <dir>/olivares.db is the real database) and provisions the system tenant.
func openDRStore(t *testing.T, dir string) store.Store {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite,
		DSN:    filepath.Join(dir, "olivares.db"),
	}, nil)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(context.Background())
		return err
	}); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	return st
}

// newDRHarness builds a harness whose API server has the DR surface over the
// given store/dir (and optionally an unattended-backup passphrase file).
func newDRHarness(t *testing.T, dir string, st store.Store, passFile string) *harness {
	t.Helper()
	return newHarnessOpts(t, func(o *api.Options) {
		o.Store = st
		o.Authenticator = auth.NewAuthenticator(st, nil)
		o.DR = &api.DRConfig{DataDir: dir, EngineKind: "sqlite", PassphraseFile: passFile}
	})
}

func TestDRSchedulePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	h1 := newDRHarness(t, dir, st, "")
	admin := h1.adminLogin()

	// PUT the full schedule — including the RequireDualControl restore gate,
	// which the pre handler silently dropped.
	r := h1.do("PUT", "/v1/console/dr/schedule", admin, map[string]any{
		"enabled": true, "cron": "0 2 * * *", "retain_days": 7,
		"require_dual_control_restore": true,
	}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("PUT schedule = %d %s", r.code, r.raw)
	}
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("PUT response dropped require_dual_control_restore: %s", r.raw)
	}
	if next, _ := r.body["next_run"].(string); next == "" {
		t.Fatalf("enabled schedule has no derived next_run: %s", r.raw)
	}

	// "Restart": a SECOND server over the SAME store must reload the persisted
	// schedule — before a restart reset everything, dual-control included.
	h2 := newDRHarness(t, dir, st, "")
	r = h2.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET schedule after restart = %d %s", r.code, r.raw)
	}
	if r.body["enabled"] != true || r.body["cron"] != "0 2 * * *" ||
		r.body["retain_days"] != float64(7) || r.body["require_dual_control_restore"] != true {
		t.Fatalf("schedule did not survive the restart: %s", r.raw)
	}
	if next, _ := r.body["next_run"].(string); next == "" {
		t.Fatalf("restarted schedule has no derived next_run: %s", r.raw)
	}
}

func TestDRScheduleLegacyRecordDefaultsDualControlFailClosed(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	err := st.Mutate(context.Background(), model.SystemTenantID, func(sc store.Scope) error {
		org, err := sc.Org(context.Background())
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		// Simulate a legacy/hand-edited record predating the destructive-policy
		// field. Decoding a missing bool as false would fail open after restart.
		settings["dr.schedule"] = `{"enabled":false,"cron":"","retain_days":0}`
		_, err = sc.SetOrgSettings(context.Background(), settings)
		return err
	})
	if err != nil {
		t.Fatalf("seed legacy schedule: %v", err)
	}

	h := newDRHarness(t, dir, st, "")
	admin := h.adminLogin()
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET legacy schedule = %d %s", r.code, r.raw)
	}
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("legacy schedule defaulted dual-control open: %s", r.raw)
	}
}

func TestDRScheduleRunnerRefreshesStandbyBootState(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	leader := newDRHarness(t, dir, st, "")
	standby := newDRHarness(t, dir, st, "") // built before the schedule update
	admin := leader.adminLogin()

	if r := leader.do("PUT", "/v1/console/dr/schedule", admin, map[string]any{
		"enabled": true, "cron": "* * * * *", "retain_days": 0,
		"require_dual_control_restore": true,
	}, nil); r.code != http.StatusOK {
		t.Fatalf("PUT schedule = %d %s", r.code, r.raw)
	}

	// The promoted node's in-memory schedule is still the boot-time zero value.
	// RunDue must refresh the shared estate: reaching the expected passphrase
	// error proves it saw and claimed the newly enabled schedule.
	ran, err := standby.srv.RunDueScheduledBackup(context.Background(), time.Now().UTC())
	if ran {
		t.Fatal("backup ran without a configured passphrase file")
	}
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("standby runner did not refresh the persisted schedule: %v", err)
	}
	r := standby.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["require_dual_control_restore"] != true || r.body["last_run_status"] != "failed" {
		t.Fatalf("standby state was not refreshed/recorded fail-closed: %s", r.raw)
	}
}

func TestDRSchedulePutValidation(t *testing.T) {
	dir := t.TempDir()
	h := newDRHarness(t, dir, openDRStore(t, dir), "")
	admin := h.adminLogin()

	for name, body := range map[string]map[string]any{
		"invalid cron":         {"enabled": true, "cron": "not a cron", "retain_days": 7},
		"enabled without cron": {"enabled": true, "cron": "", "retain_days": 7},
		"negative retain":      {"enabled": false, "cron": "", "retain_days": -1},
	} {
		if r := h.do("PUT", "/v1/console/dr/schedule", admin, body, nil); r.code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d %s, want 400", name, r.code, r.raw)
		}
	}
}

func TestDRScheduledBackupRunsAndAppliesRetention(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	passFile := filepath.Join(dir, "dr-pass.txt")
	if err := os.WriteFile(passFile, []byte("test-schedule-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newDRHarness(t, dir, st, passFile)
	admin := h.adminLogin()

	// A stale bundle past the 7-day retention, and a fresh one inside it.
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(backupDir, "olivares-stale.drbundle")
	fresh := filepath.Join(backupDir, "olivares-fresh.drbundle")
	now := time.Now().UTC()
	for path, createdAt := range map[string]time.Time{
		stale: now.Add(-10 * 24 * time.Hour),
		fresh: now.Add(-time.Hour),
	} {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		err = dr.WriteBundle(f, dr.BundleInput{Manifest: &dr.Manifest{
			Format:     dr.ManifestFormat,
			CreatedAt:  createdAt.Format(time.RFC3339),
			EngineKind: "sqlite",
			Store:      dr.StoreSnapshot{Method: dr.MethodPITR, File: "external"},
		}})
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("write fixture bundle: %v", err)
		}
	}

	if r := h.do("PUT", "/v1/console/dr/schedule", admin, map[string]any{
		"enabled": true, "cron": "* * * * *", "retain_days": 7,
	}, nil); r.code != http.StatusOK {
		t.Fatalf("PUT schedule = %d %s", r.code, r.raw)
	}

	ran, err := h.srv.RunDueScheduledBackup(context.Background(), now)
	if err != nil {
		t.Fatalf("RunDueScheduledBackup: %v", err)
	}
	if !ran {
		t.Fatal("due schedule did not run")
	}

	// A real bundle was written; the stale one was pruned; the fresh one kept.
	entries, err := filepath.Glob(filepath.Join(backupDir, "*.drbundle"))
	if err != nil {
		t.Fatal(err)
	}
	var sawNew bool
	for _, e := range entries {
		if e == stale {
			t.Error("stale bundle survived retention")
		}
		if e != stale && e != fresh {
			sawNew = true
		}
	}
	if !sawNew {
		t.Fatalf("no new bundle written; dir = %v", entries)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh bundle inside retention was pruned: %v", err)
	}

	// The run is recorded in the schedule bookkeeping.
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET schedule = %d %s", r.code, r.raw)
	}
	if r.body["last_run_status"] != "completed" {
		t.Fatalf("last_run_status = %v, want completed: %s", r.body["last_run_status"], r.raw)
	}
	if lastRun, _ := r.body["last_run"].(string); lastRun == "" {
		t.Fatalf("last_run not recorded: %s", r.raw)
	}

	// Immediately re-evaluating at the same instant must NOT run again.
	ran, err = h.srv.RunDueScheduledBackup(context.Background(), now)
	if err != nil {
		t.Fatalf("second RunDueScheduledBackup: %v", err)
	}
	if ran {
		t.Fatal("schedule ran twice for the same instant")
	}
}

func TestDRScheduledBackupWithoutPassphraseFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	st := openDRStore(t, dir)
	h := newDRHarness(t, dir, st, "") // no passphrase file configured
	admin := h.adminLogin()

	if r := h.do("PUT", "/v1/console/dr/schedule", admin, map[string]any{
		"enabled": true, "cron": "* * * * *", "retain_days": 0,
	}, nil); r.code != http.StatusOK {
		t.Fatalf("PUT schedule = %d %s", r.code, r.raw)
	}

	ran, err := h.srv.RunDueScheduledBackup(context.Background(), time.Now().UTC())
	if ran {
		t.Fatal("backup must not run without a passphrase")
	}
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("err = %v, want a loud passphrase error", err)
	}

	// The failure is recorded honestly for the console.
	r := h.do("GET", "/v1/console/dr/schedule", admin, nil, nil)
	if r.body["last_run_status"] != "failed" {
		t.Fatalf("last_run_status = %v, want failed: %s", r.body["last_run_status"], r.raw)
	}
	if msg, _ := r.body["last_run_error"].(string); !strings.Contains(msg, "passphrase") {
		t.Fatalf("last_run_error = %q, want a passphrase explanation", msg)
	}
}
