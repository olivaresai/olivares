// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestDirectoryInitializationPostgres(t *testing.T) {
	for _, split := range []bool{false, true} {
		name := "single-role"
		if split {
			name = "split-owner"
		}
		t.Run(name, func(t *testing.T) {
			isolate := isolatedPG
			if split {
				isolate = isolatedPGSplit
			}
			dsns := isolate(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, InitializeDirectoryWriter: func() error { return nil }}
			for attempt := 0; attempt < 2; attempt++ {
				raw, err := Open(context.Background(), cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				status, supported, err := raw.(store.DirectoryStatuser).DirectoryStatus(context.Background())
				_ = raw.Close()
				if err != nil || !supported || status.ControlMode != store.DirectoryControlEnforced || status.ExpectedGeneration != 2 || !status.EpochCoverageComplete {
					t.Fatalf("open %d directory = %+v, supported=%t, err=%v", attempt, status, supported, err)
				}
			}
		})
	}
}

func TestDirectoryInitializationPostgresWithoutInventoryRemainsStaged(t *testing.T) {
	dsns := isolatedPGSplit(t)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner,
		InitializeDirectoryWriter: func() error { t.Fatal("fresh preparation ran without inventory authority"); return nil }}
	raw, err := Open(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	status, supported, err := raw.(store.DirectoryStatuser).DirectoryStatus(t.Context())
	if err != nil || !supported || status.ControlMode != store.DirectoryControlStaged || status.ExpectedGeneration != 1 {
		t.Fatalf("unprovisioned PostgreSQL directory = %+v, supported=%t, err=%v", status, supported, err)
	}
}
