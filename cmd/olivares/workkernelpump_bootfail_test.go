// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

// SR2C P2 round 2 (2026-10-03): the work-outbox pump registers its nudge
// goroutine and the global sessions callback BEFORE the hook-config refusal —
// so a boot that fails there must unwind them (cancel + join + withdraw), or a
// failed boot leaks a goroutine and a callback into a dead store. This boots
// with a malformed OLIVARES_HOOK_PEP_CONFIG: boot fails after the pump
// registered, and the callback must be gone.
func TestBootFailureAfterPumpRegistrationWithdrawsTheNudge(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "hook-pep.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", bad)
	// The slot is process-global: leave it as found.
	t.Cleanup(func() { sessions.SetWorkOutboxNudge(nil) })

	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true,
	})
	if eng != nil || err == nil {
		t.Fatalf("boot with a malformed hook config = engine:%v err:%v, want nil engine and the refusal", eng, err)
	}
	if sessions.WorkOutboxNudgeRegistered() {
		t.Fatal("a boot that failed after the pump registered left the global outbox nudge pointing at the failed engine")
	}

	// A clean boot over a fresh data dir proves the unwind left nothing
	// behind: the second engine registers its own pump and callback.
	os.Unsetenv("OLIVARES_HOOK_PEP_CONFIG")
	eng2, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true,
	})
	if err != nil {
		t.Fatalf("clean boot after the unwound failure: %v", err)
	}
	defer func() { _ = eng2.Close() }()
	if !sessions.WorkOutboxNudgeRegistered() {
		t.Fatal("a successful boot must register the outbox nudge")
	}
}
