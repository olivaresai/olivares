// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDirectoryInitializationFreshOpenAndReopen(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "fresh.db"), InitializeDirectoryWriter: func() error { return nil }}
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := engine.Open(ctx, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer raw.Close()
			status, supported, err := raw.(store.DirectoryStatuser).DirectoryStatus(ctx)
			if err != nil || !supported || status.ControlMode != store.DirectoryControlEnforced || status.ExpectedGeneration != 2 || !status.EpochCoverageComplete {
				t.Fatalf("open %d directory = %+v, supported=%t, err=%v", attempt, status, supported, err)
			}
			if err := raw.System(ctx, func(sys store.SystemScope) error {
				orgs, err := sys.ListOrgs(ctx)
				if err == nil && (len(orgs) != 1 || orgs[0].ID != model.ID(model.SystemTenantID)) {
					t.Fatalf("genesis organizations = %+v", orgs)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}()
	}
}

func TestDirectoryInitializationDoesNotActivateExistingEstate(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "existing.db")}
	raw, err := engine.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.InitializeDirectoryWriter = func() error { t.Fatal("initialization callback ran on an existing estate"); return nil }
	raw, err = engine.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	status, supported, err := raw.(store.DirectoryStatuser).DirectoryStatus(ctx)
	if err != nil || !supported || status.ControlMode != store.DirectoryControlStaged || status.ExpectedGeneration != 1 {
		t.Fatalf("existing directory = %+v, supported=%t, err=%v", status, supported, err)
	}
	_, err = engine.ActivateDirectoryWriter(ctx, raw, cfg, engine.DirectoryWriterActivationRequest{ExpectedGeneration: 1, Actor: "operator", Reason: "upgrade"})
	if !errors.Is(err, engine.ErrDirectoryWriterActivationAssertion) {
		t.Fatalf("missing rollout assertions: %v", err)
	}
}

func TestDirectoryInitializationFailureLeavesNoGenesis(t *testing.T) {
	preparationErr := errors.New("bootstrap state could not be saved")
	for _, preparationFails := range []bool{false, true} {
		name := "audit"
		if preparationFails {
			name = "preparation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			prepared := 0
			cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "failed.db"), AuditSpoolMaxBytes: 1}
			cfg.InitializeDirectoryWriter = func() error {
				prepared++
				if preparationFails {
					return preparationErr
				}
				return nil
			}
			wantErr := store.ErrAuditSpoolFull
			if preparationFails {
				wantErr = preparationErr
			}
			if raw, err := engine.Open(ctx, cfg, nil); !errors.Is(err, wantErr) {
				if raw != nil {
					_ = raw.Close()
				}
				t.Fatalf("genesis failure = %v, want %v", err, wantErr)
			}
			if prepared != 1 {
				t.Fatalf("preparation calls before genesis = %d, want 1", prepared)
			}
			cfg.AuditSpoolMaxBytes = 0
			raw, err := engine.Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if prepared != 1 {
				t.Fatalf("reopen repeated fresh preparation: %d", prepared)
			}
			status, _, err := raw.(store.DirectoryStatuser).DirectoryStatus(ctx)
			if err != nil || status.ControlMode != store.DirectoryControlStaged || status.ExpectedGeneration != 1 {
				t.Fatalf("failed genesis advanced control: %+v, %v", status, err)
			}
			if err := raw.System(ctx, func(sys store.SystemScope) error {
				_, err := sys.GetOrg(ctx, model.SystemTenantID)
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("failed genesis left SYSTEM: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
