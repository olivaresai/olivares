// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"strings"
	"testing"
)

// The file-backed happy path belongs in the serve --seed-demo E2E: runBackup
// needs the store, signer, and key material wired by boot. These tests pin the
// startup seam's fail-fast validation without duplicating that boot harness.
func TestStartupBackupDRUnavailable(t *testing.T) {
	err := (&Server{}).RunStartupBackup(context.Background(), "secret", "notes", "actor")
	if err == nil || !strings.Contains(err.Error(), "dr service unavailable") {
		t.Fatalf("RunStartupBackup() error = %v, want DR unavailable", err)
	}
}

func TestStartupBackupRequiresPassphrase(t *testing.T) {
	srv := &Server{drSvc: newDRService(DRConfig{DataDir: t.TempDir()})}
	err := srv.RunStartupBackup(context.Background(), "", "notes", "actor")
	if err == nil || !strings.Contains(err.Error(), "passphrase is required") {
		t.Fatalf("RunStartupBackup() error = %v, want passphrase required", err)
	}
}
