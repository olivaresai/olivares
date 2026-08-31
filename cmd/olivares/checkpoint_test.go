// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/store"
)

// TestCheckpointerWritesVerifiableAnchor proves the scheduled-checkpoint fix
// (docs/SECURITY-HARDENING.md): a serve deployment produces a signed, verifiable tamper-evidence
// anchor without anyone running the CLI. Here the graceful-shutdown path (stop)
// writes the final checkpoint; VerifyCheckpoints then confirms it.
func TestCheckpointerWritesVerifiableAnchor(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true,
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer func() { _ = eng.Close() }()

	// A long interval means the ticker never fires during the test; stop() must
	// still write the final shutdown checkpoint.
	cp := startCheckpointer(eng.signer, eng.store, time.Hour, eng.log, eng.metrics)
	cp.stop(ctx)

	if eng.demoTenant.IsZero() {
		t.Fatal("demo tenant not seeded")
	}
	if err := eng.store.View(ctx, eng.demoTenant, func(sc store.Scope) error {
		cr, err := audit.VerifyCheckpoints(ctx, sc.Audit(), eng.signer.PublicKey())
		if err != nil {
			return err
		}
		if cr.Checkpoints < 1 {
			t.Fatalf("scheduler/shutdown wrote no checkpoint (checkpoints=%d)", cr.Checkpoints)
		}
		if !cr.OK {
			t.Fatalf("checkpoint did not verify: %s at seq %d", cr.Reason, cr.FirstBadSeq)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify checkpoints: %v", err)
	}
}

// TestCheckpointerDisabled: interval<=0 disables the scheduler and stop() is a
// no-op (no panic, no checkpoint), for deployments that drive checkpoints out of band.
func TestCheckpointerDisabled(t *testing.T) {
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Logger: slog.Default(), DemoSeed: true})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer func() { _ = eng.Close() }()

	cp := startCheckpointer(eng.signer, eng.store, 0, eng.log, eng.metrics)
	cp.stop(ctx) // must not panic or block

	_ = eng.store.View(ctx, eng.demoTenant, func(sc store.Scope) error {
		cr, err := audit.VerifyCheckpoints(ctx, sc.Audit(), eng.signer.PublicKey())
		if err != nil {
			return err
		}
		if cr.Checkpoints != 0 {
			t.Fatalf("disabled scheduler still wrote %d checkpoint(s)", cr.Checkpoints)
		}
		return nil
	})
}
