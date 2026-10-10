// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
)

func TestDRBackupCustodyFailureNeverFallsBack(t *testing.T) {
	dir := t.TempDir()
	called := false
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), drSvc: newDRService(DRConfig{
		DataDir: dir, EngineKind: "sqlite", SealKeys: func(*dr.KeyCipher) (map[string][]byte, []dr.KeyRef, []dr.SealerProbe, error) {
			called = true
			return nil, nil, nil, errors.New("custody unavailable")
		},
	})}
	err := srv.RunStartupBackup(context.Background(), "test backup passphrase", "", "test")
	if !called || err == nil || !strings.Contains(err.Error(), "custody unavailable") {
		t.Fatalf("backup custody failure = %v; called=%t", err, called)
	}
	bundles, err := filepath.Glob(filepath.Join(dir, "backups", "*.drbundle"))
	if err != nil || len(bundles) != 0 {
		t.Fatalf("failed custody produced a bundle: count=%d, %v", len(bundles), err)
	}
}
