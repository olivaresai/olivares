// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr"
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
		if _, err := parseDRCron(spec); err == nil {
			t.Errorf("parseDRCron(%q) = nil error, want rejection", spec)
		}
	}
}

func TestParseDRCronAcceptsSupportedSyntax(t *testing.T) {
	for _, spec := range []string{"* * * * *", "0 2 * * *", "*/15 * * * *", "0 0 1,15 * *", "30 6 * * 1"} {
		if _, err := parseDRCron(spec); err != nil {
			t.Errorf("parseDRCron(%q) = %v, want ok", spec, err)
		}
	}
}

func TestDRCronMatches(t *testing.T) {
	spec, err := parseDRCron("0 2 * * *") // daily at 02:00 UTC
	if err != nil {
		t.Fatal(err)
	}
	if !spec.matches(time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)) {
		t.Error("02:00 must match")
	}
	if spec.matches(time.Date(2026, 7, 18, 2, 1, 0, 0, time.UTC)) {
		t.Error("02:01 must not match")
	}
	if spec.matches(time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC)) {
		t.Error("03:00 must not match")
	}
}

func TestDRCronDueSince(t *testing.T) {
	spec, err := parseDRCron("0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 2, 0, 30, 0, time.UTC)

	// Never ran (zero last): due when a matching instant sits inside the 24h
	// lookback window.
	if !spec.dueSince(time.Time{}, now) {
		t.Error("fresh schedule at 02:00:30 must be due")
	}
	// Ran at today's 02:00 already: not due again until tomorrow.
	last := time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)
	if spec.dueSince(last, now) {
		t.Error("schedule that ran this instant must not be due again")
	}
	// Ran yesterday: today's 02:00 is due.
	if !spec.dueSince(last.Add(-24*time.Hour), now) {
		t.Error("schedule that last ran yesterday must be due at 02:00 today")
	}
	// Not yet reached today's instant.
	if spec.dueSince(last.Add(-24*time.Hour), time.Date(2026, 7, 18, 1, 59, 0, 0, time.UTC)) {
		t.Error("schedule must not fire before its instant")
	}
}

func TestDRCronNextAfter(t *testing.T) {
	spec, err := parseDRCron("0 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 7, 18, 2, 30, 0, 0, time.UTC)
	next, ok := spec.nextAfter(from)
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
	err = dr.WriteBundle(f, dr.BundleInput{
		Manifest: &dr.Manifest{
			Format:     dr.ManifestFormat,
			CreatedAt:  now.Add(-time.Hour).Format(time.RFC3339),
			EngineKind: "sqlite",
			Store:      dr.StoreSnapshot{Method: dr.MethodPITR, File: "external"},
		},
	})
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
