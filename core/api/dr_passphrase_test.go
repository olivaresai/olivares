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

	"github.com/olivaresai/olivares/core/api"
)

// A DR bundle carries the estate's signing keys, and its ONLY protection at rest
// is the operator passphrase. New encryption therefore requires at least 12
// runes. Restore remains backward-compatible with existing shorter passphrases.

func drHarness(t *testing.T) (*harness, string) {
	t.Helper()
	dataDir := t.TempDir()
	h := newHarnessOpts(t, func(o *api.Options) {
		o.DR = &api.DRConfig{DataDir: dataDir, EngineKind: "sqlite"}
	})
	// The service's default backup dir (newDRService): <DataDir>/backups.
	return h, filepath.Join(dataDir, "backups")
}

func TestDRBackupPassphraseFloor(t *testing.T) {
	h, _ := drHarness(t)
	admin := h.adminLogin()

	// 1 character — the exact regression: accepted before.
	r := h.do("POST", "/v1/console/dr/backup", admin, map[string]any{"passphrase": "x"}, nil)
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "12") {
		t.Fatalf("backup with 1-char passphrase = %d %s, want 400 mentioning the 12-char floor", r.code, r.raw)
	}
	// 11 characters — still under the floor.
	r = h.do("POST", "/v1/console/dr/backup", admin, map[string]any{"passphrase": "elevenchars"}, nil)
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "12") {
		t.Fatalf("backup with 11-char passphrase = %d %s, want 400 mentioning the 12-char floor", r.code, r.raw)
	}

	// The floor counts RUNES: 12 multibyte characters are 12 characters.
	r = h.do("POST", "/v1/console/dr/backup", admin, map[string]any{"passphrase": "ññññññññññññ"}, nil)
	if r.code == http.StatusBadRequest {
		t.Fatalf("backup with 12-rune passphrase = %d %s, floor must count runes not bytes", r.code, r.raw)
	}

	// Empty stays its own explicit error (required), not a floor message.
	r = h.do("POST", "/v1/console/dr/backup", admin, map[string]any{"passphrase": ""}, nil)
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "required") {
		t.Fatalf("backup with empty passphrase = %d %s, want 400 required", r.code, r.raw)
	}
}

func TestDRBackupRejectsFiveCharacterPassphrase(t *testing.T) {
	h, _ := drHarness(t)
	admin := h.adminLogin()

	r := h.do("POST", "/v1/console/dr/backup", admin, map[string]any{"passphrase": "short"}, nil)
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "12") {
		t.Fatalf("backup with 5-char passphrase = %d %s, want 400 mentioning the 12-char floor", r.code, r.raw)
	}
}

func TestDRRestoreApplyAllowsLegacyShortPassphrase(t *testing.T) {
	h, dir := drHarness(t)
	admin := h.adminLogin()

	// A present upload so the handler reaches the passphrase validation.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "upload-1"), []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := h.do("POST", "/v1/console/dr/restore/upload-1/apply", admin, map[string]any{"passphrase": "short"}, nil)
	if r.code != http.StatusAccepted {
		t.Fatalf("restore apply with legacy 5-char passphrase = %d %s, want 202", r.code, r.raw)
	}
}

func TestDRRestoreApproveAllowsLegacyShortPassphrase(t *testing.T) {
	h, _ := drHarness(t)
	admin := h.adminLogin()

	r := h.do("POST", "/v1/console/dr/restore/upload-1/approve", admin,
		map[string]any{"request_id": "drr_x", "passphrase": "short"}, nil)
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "no pending restore") || strings.Contains(r.raw, "12") {
		t.Fatalf("restore approve with legacy 5-char passphrase = %d %s, want pending-request validation rather than the creation floor", r.code, r.raw)
	}
}

func TestStartupBackupPassphraseFloor(t *testing.T) {
	h, _ := drHarness(t)
	err := h.srv.RunStartupBackup(context.Background(), "x", "notes", "actor")
	if err == nil || !strings.Contains(err.Error(), "12") {
		t.Fatalf("RunStartupBackup with 1-char passphrase error = %v, want the 12-char floor", err)
	}
}
