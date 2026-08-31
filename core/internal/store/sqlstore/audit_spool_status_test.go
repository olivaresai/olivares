// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The status capability surfaces the EXACT incremental counter and the pending
// degrade episodes, so the console indicator reports the same numbers the
// writer's guard compares.

func TestAuditSpoolStatusUnconfigured(t *testing.T) {
	st := openSQLiteSpoolTest(t, store.Config{})
	status, configured, err := st.(store.AuditSpoolStatuser).AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("audit spool status: %v", err)
	}
	if configured {
		t.Fatalf("unconfigured audit spool returned configured status: %+v", status)
	}
}

func TestAuditSpoolStatusConfigured(t *testing.T) {
	const maxBytes = int64(1 << 40)
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: filepath.Join(t.TempDir(), "status.db"), AuditSpoolMaxBytes: maxBytes,
	})
	provisionTenant(t, st, "spool-status")

	status, configured, err := st.(store.AuditSpoolStatuser).AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("audit spool status: %v", err)
	}
	if !configured {
		t.Fatal("configured audit spool returned configured=false")
	}
	if status.MaxBytes != maxBytes || status.OnFull != store.AuditSpoolBlock {
		t.Fatalf("audit spool config = max %d mode %q, want max %d mode %q",
			status.MaxBytes, status.OnFull, maxBytes, store.AuditSpoolBlock)
	}
	if want := readAuditSpoolUsage(t, st); status.UsedBytes != want || want <= 0 {
		t.Fatalf("audit spool used bytes = %d, want the exact counter %d (> 0)", status.UsedBytes, want)
	}
	if status.Engaged || status.PendingDropTenants != 0 || status.PendingDrops != 0 {
		t.Fatalf("under-budget status unexpectedly engaged or pending: %+v", status)
	}
}

func TestAuditSpoolStatusTracksPendingDropsAndSeal(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "status-drops.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "spool-status-drops")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	degraded := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	const dropCount = 3
	for i := 0; i < dropCount; i++ {
		if err := degraded.Mutate(ctx, tenant, func(sc store.Scope) error {
			ev, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "agent:1", ActorKind: model.ActorAgent, Action: "tool.invoke",
			})
			if err == nil && ev.Seq != 0 {
				t.Fatalf("degraded append %d returned seq %d, want zero", i, ev.Seq)
			}
			return err
		}); err != nil {
			t.Fatalf("degraded append %d: %v", i, err)
		}
	}
	status, configured, err := degraded.(store.AuditSpoolStatuser).AuditSpoolStatus(ctx)
	if err != nil {
		t.Fatalf("audit spool status with pending drops: %v", err)
	}
	if !configured || !status.Engaged || status.PendingDropTenants != 1 || status.PendingDrops != dropCount {
		t.Fatalf("pending drop status = %+v, want engaged with 1 tenant / %d drops", status, dropCount)
	}
	if err := degraded.Close(); err != nil {
		t.Fatal(err)
	}

	// Raising the budget (a reopen, the operator's recovery path) admits the next
	// append, which seals the loss as the in-chain marker and clears the counters.
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, AuditSpoolMaxBytes: largeAuditSpoolBudget, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "agent:1", ActorKind: model.ActorAgent, Action: "tool.resume",
		})
		return err
	}); err != nil {
		t.Fatalf("append after raising budget: %v", err)
	}
	status, configured, err = st.(store.AuditSpoolStatuser).AuditSpoolStatus(ctx)
	if err != nil {
		t.Fatalf("audit spool status after seal: %v", err)
	}
	if !configured || status.Engaged || status.PendingDropTenants != 0 || status.PendingDrops != 0 {
		t.Fatalf("post-seal status = %+v, want disengaged with no pending drops", status)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		report, err := sc.Audit().Verify(ctx, 1)
		if err == nil && (!report.OK || report.DeclaredGaps != 1) {
			t.Fatalf("post-seal verify = %+v", report)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
