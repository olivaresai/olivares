// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/cron"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestParseDRCronRejectsMalformedSpecs(t *testing.T) {
	for _, spec := range []string{
		"",            // empty
		"* * * *",     // 4 fields
		"* * * * * *", // 6 fields
		"60 * * * *",  // minute out of range
		"* 24 * * *",  // hour out of range
		"* * 0 * *",   // day-of-month out of range
		"* * * 13 *",  // month out of range
		"* * * * 7",   // day-of-week out of range (0-6)
		"*/0 * * * *", // zero step
		"a * * * *",   // non-numeric
		"1-5 * * * *", // ranges unsupported (deliberately small matcher)
		"@daily",      // named specs unsupported
	} {
		if _, err := cron.Parse(spec); err == nil {
			t.Errorf("cron.Parse(%q) = nil error, want rejection", spec)
		}
	}
}

func TestParseDRCronAcceptsSupportedSyntax(t *testing.T) {
	for _, spec := range []string{"* * * * *", "0 2 * * *", "*/15 * * * *", "0 0 1,15 * *", "30 6 * * 1"} {
		if _, err := cron.Parse(spec); err != nil {
			t.Errorf("cron.Parse(%q) = %v, want ok", spec, err)
		}
	}
}

func TestDRCronMatches(t *testing.T) {
	spec, err := cron.Parse("0 2 * * *") // daily at 02:00 UTC
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Matches(time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)) {
		t.Error("02:00 must match")
	}
	if spec.Matches(time.Date(2026, 7, 18, 2, 1, 0, 0, time.UTC)) {
		t.Error("02:01 must not match")
	}
	if spec.Matches(time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC)) {
		t.Error("03:00 must not match")
	}
}

func TestDRCronDueSince(t *testing.T) {
	spec, err := cron.Parse("0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 2, 0, 30, 0, time.UTC)

	// Never ran (zero last): due when a matching instant sits inside the 24h
	// lookback window.
	if !spec.DueSince(time.Time{}, now) {
		t.Error("fresh schedule at 02:00:30 must be due")
	}
	// Ran at today's 02:00 already: not due again until tomorrow.
	last := time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)
	if spec.DueSince(last, now) {
		t.Error("schedule that ran this instant must not be due again")
	}
	// Ran yesterday: today's 02:00 is due.
	if !spec.DueSince(last.Add(-24*time.Hour), now) {
		t.Error("schedule that last ran yesterday must be due at 02:00 today")
	}
	// Not yet reached today's instant.
	if spec.DueSince(last.Add(-24*time.Hour), time.Date(2026, 7, 18, 1, 59, 0, 0, time.UTC)) {
		t.Error("schedule must not fire before its instant")
	}
}

func TestDRCronNextAfter(t *testing.T) {
	spec, err := cron.Parse("0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 18, 2, 30, 0, 0, time.UTC)
	next, ok := nextDRCronAfter(spec, from)
	if !ok {
		t.Fatal("nextAfter must find tomorrow's 02:00")
	}
	want := time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("nextAfter = %v, want %v", next, want)
	}
}

func TestScheduleRetentionUsesManifestCreatedAtBeforeFileMTime(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	bundlePath := filepath.Join(dir, "copied-fresh.drbundle")
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := dr.NewRawKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	err = dr.WriteAuthenticatedBundle(f, dr.BundleInput{
		Manifest: &dr.Manifest{
			Format:     dr.ManifestFormat,
			CreatedAt:  now.Add(-time.Hour).Format(time.RFC3339),
			EngineKind: "sqlite",
			Store:      dr.StoreSnapshot{Method: dr.MethodPITR, File: "external"},
		},
		KEK: cipher.Params(),
	}, cipher)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	old := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(bundlePath, old, old); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		drSvc: newDRService(DRConfig{BackupDir: dir}),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s.applyScheduleRetention(7, "", now)
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("fresh manifest was pruned because of copied file mtime: %v", err)
	}
}

func TestScheduleRetentionKeepsUnreadableOldBundle(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	bundlePath := filepath.Join(dir, "unreadable-old.drbundle")
	if err := os.WriteFile(bundlePath, []byte("not a DR bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(bundlePath, old, old); err != nil {
		t.Fatal(err)
	}

	s := &Server{
		drSvc: newDRService(DRConfig{BackupDir: dir}),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s.applyScheduleRetention(7, "", now)
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("unreadable bundle with unprovable age was pruned: %v", err)
	}
}

// TestDRScheduleRTOExceeded: a completed scheduled restore test is flagged when it
// took longer than the operator's RTO target, and the ledger event carries the
// measurement. A failed test, or no target, is never reported as a slow restore.
func TestDRScheduleRTOExceeded(t *testing.T) {
	const minute = 60_000
	for _, tc := range []struct {
		name   string
		status string
		target int
		tookMS int64
		want   bool
	}{
		{"at the target", drJobCompleted, 5, 5 * minute, false},
		{"over the target", drJobCompleted, 5, 5*minute + 1, true},
		{"no target set", drJobCompleted, 0, 99 * minute, false},
		{"a failed test is not a slow one", drJobFailed, 5, 99 * minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := drSchedule{
				drProtection:         drProtection{RTOMinutes: tc.target},
				drVerificationResult: drVerificationResult{LastVerifyStatus: tc.status, LastVerifyRTOMillis: tc.tookMS},
			}
			if got := s.rtoExceeded(); got != tc.want {
				t.Fatalf("rtoExceeded = %v, want %v", got, tc.want)
			}
			meta := drVerifyAuditMeta(s)
			if tc.status != drJobCompleted {
				if _, ok := meta["rto_ms"]; ok {
					t.Fatalf("a %s test recorded a restore time: %v", tc.status, meta)
				}
				return
			}
			if meta["rto_ms"] != tc.tookMS || meta["rto_target_ms"] != int64(tc.target)*minute || meta["rto_exceeded"] != tc.want {
				t.Fatalf("ledger meta = %v", meta)
			}
		})
	}
}

// TestDRScheduleRTOAlertReachesOrganizationsOnly: an exceeded RTO raises one
// finding per organization and none for the system tenant, and what an
// organization can read names neither the bundle nor the timings. A restore
// test inside its target raises nothing.
func TestDRScheduleRTOAlertReachesOrganizationsOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	orgs := map[model.TenantID]string{}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		for _, slug := range []string{"acme", "globex"} {
			o, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			orgs[o.TenantID] = slug
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	var told []model.TenantID
	cfg := DRConfig{Alert: func(_ context.Context, tenant model.TenantID, title string) error {
		told = append(told, tenant)
		calls = append(calls, title)
		return nil
	}}
	s := &Server{st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), drSvc: newDRService(cfg)}
	slow := drSchedule{
		drProtection:         drProtection{RTOMinutes: 5},
		drVerificationResult: drVerificationResult{LastVerifyStatus: drJobCompleted, LastVerifyRTOMillis: 5*60_000 + 1},
		LastBundle:           "estate-secret-name.drbundle",
	}

	s.drSvc.setSchedule(func(d *drSchedule) { *d = slow })
	s.alertRTOExceeded(ctx)
	if len(told) != len(orgs) {
		t.Fatalf("alerts = %d (%v), want one per organization (%d)", len(told), told, len(orgs))
	}
	seen := map[model.TenantID]bool{}
	for _, tenant := range told {
		if tenant == model.SystemTenantID || orgs[tenant] == "" || seen[tenant] {
			t.Fatalf("alert for tenant %v: system, unknown or repeated (%v)", tenant, told)
		}
		seen[tenant] = true
	}
	for _, c := range calls {
		if strings.Contains(c, slow.LastBundle) || strings.Contains(c, "300001") {
			t.Fatalf("an organization-visible alert carries the bundle name or the timing: %q", c)
		}
	}

	// Inside its target the scheduled restore test (a real one, on a real bundle)
	// raises nothing.
	told = nil
	bundle, _ := buildTestBundle(t)
	pass := filepath.Join(dir, "pass")
	if err := os.WriteFile(pass, []byte("correct horse battery staple"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.drSvc = newDRService(DRConfig{DataDir: dir, BackupDir: filepath.Dir(bundle), PassphraseFile: pass, Alert: cfg.Alert})
	if err := s.saveDRSchedule(ctx, drSchedule{drProtection: drProtection{VerifyCron: "0 3 * * 0", RTOMinutes: 5}, LastBundle: filepath.Base(bundle)}); err != nil {
		t.Fatal(err)
	}
	if err := s.loadDRSchedule(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runDueVerification(ctx, time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := s.drSvc.scheduleSnapshot(); got.LastVerifyStatus != drJobCompleted || got.rtoExceeded() {
		t.Fatalf("restore test = %+v", got)
	}
	if len(told) != 0 {
		t.Fatalf("a restore test inside its target alerted %v", told)
	}
}
