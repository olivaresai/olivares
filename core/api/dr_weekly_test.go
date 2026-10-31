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
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDRWeeklyVerificationDetectsCorruptBundleAndAudits(t *testing.T) {
	ctx := context.Background()
	bundle, _ := buildTestBundle(t)
	dir := t.TempDir()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	pass := filepath.Join(dir, "pass")
	if err := os.WriteFile(pass, []byte("correct horse battery staple"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), drSvc: newDRService(DRConfig{DataDir: dir, BackupDir: filepath.Dir(bundle), PassphraseFile: pass})}
	sched := drSchedule{drProtection: drProtection{VerifyCron: "0 3 * * 0"}, LastBundle: filepath.Base(bundle)}
	if err := s.saveDRSchedule(ctx, sched); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	if ran, err := s.RunDueScheduledBackup(ctx, now); err != nil || ran {
		t.Fatalf("verification-only tick reports backup=%v error=%v", ran, err)
	}
	if got := s.drSvc.scheduleSnapshot(); got.LastVerifyStatus != "completed" {
		t.Fatalf("valid bundle rejected: %+v", got)
	}
	if err := os.WriteFile(bundle, []byte("corrupted bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunDueScheduledBackup(ctx, now.Add(7*24*time.Hour)); err == nil {
		t.Fatal("corrupted bundle passed verification")
	}
	if err := s.loadDRSchedule(ctx); err != nil {
		t.Fatal(err)
	}
	got := s.drSvc.scheduleSnapshot()
	if got.LastVerifyStatus != "failed" || got.LastVerifyError == "" {
		t.Fatalf("verification failure not persisted: %+v", got)
	}
	var audited int
	if err := st.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(e model.AuditEvent) error {
			if e.Action == "dr.verify.failed" {
				audited++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Fatalf("failure audit entries=%d, want 1", audited)
	}
	// One failed instant is claimed; a minute tick must not produce another failure event.
	if _, err := s.RunDueScheduledBackup(ctx, now.Add(7*24*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
}
