// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine_test

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

func TestDirectoryWriterActivationRequiresExplicitOperatorAssertions(t *testing.T) {
	valid := engine.DirectoryWriterActivationRequest{
		ExpectedGeneration: 1,
		WritersUpgraded:    true,
		WritersDrained:     true,
		Actor:              "operator@example.test",
		Reason:             "completed cluster-wide Slice C rollout",
	}
	tests := map[string]func(*engine.DirectoryWriterActivationRequest){
		"writers not upgraded": func(r *engine.DirectoryWriterActivationRequest) { r.WritersUpgraded = false },
		"writers not drained":  func(r *engine.DirectoryWriterActivationRequest) { r.WritersDrained = false },
		"zero generation":      func(r *engine.DirectoryWriterActivationRequest) { r.ExpectedGeneration = 0 },
		"maximum generation":   func(r *engine.DirectoryWriterActivationRequest) { r.ExpectedGeneration = math.MaxInt64 },
		"blank actor":          func(r *engine.DirectoryWriterActivationRequest) { r.Actor = "  " },
		"blank reason":         func(r *engine.DirectoryWriterActivationRequest) { r.Reason = "\t" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			req := valid
			mutate(&req)
			result, err := engine.ActivateDirectoryWriter(
				context.Background(), nil,
				store.Config{Engine: store.EngineSQLite}, req,
			)
			if !errors.Is(err, engine.ErrDirectoryWriterActivationAssertion) {
				t.Fatalf("error = %v, want %v", err, engine.ErrDirectoryWriterActivationAssertion)
			}
			if result != (engine.DirectoryWriterActivationResult{}) {
				t.Fatalf("failed assertion result = %+v", result)
			}
		})
	}
}

func TestDirectoryWriterActivationPublicSeamReportsReopen(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{
		Engine: store.EngineSQLite,
		DSN:    filepath.Join(t.TempDir(), "engine-activation.db"),
	}
	raw, err := engine.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	req := engine.DirectoryWriterActivationRequest{
		ExpectedGeneration: 1,
		WritersUpgraded:    true,
		WritersDrained:     true,
		Actor:              "cluster-operator",
		Reason:             "all old writers drained",
	}
	result, err := engine.ActivateDirectoryWriter(ctx, raw, cfg, req)
	if err != nil {
		t.Fatalf("ActivateDirectoryWriter: %v", err)
	}
	if !result.Changed || !result.ReopenRequired || result.After.Enabled ||
		result.Before.ControlMode != store.DirectoryControlStaged ||
		result.Before.ExpectedGeneration != 1 ||
		result.After.ControlMode != store.DirectoryControlEnforced ||
		result.After.ExpectedGeneration != 2 {
		t.Fatalf("activation result = %+v", result)
	}

	retry, err := engine.ActivateDirectoryWriter(ctx, raw, cfg, req)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if retry.Changed || !retry.ReopenRequired || retry.After.Enabled ||
		retry.After.ControlMode != store.DirectoryControlEnforced ||
		retry.After.ExpectedGeneration != 2 {
		t.Fatalf("retry result = %+v", retry)
	}
}

func TestDirectoryWriterActivationRejectsDecoratedStores(t *testing.T) {
	ctx := context.Background()
	cfg := store.Config{
		Engine: store.EngineSQLite,
		DSN:    filepath.Join(t.TempDir(), "engine-activation-decorators.db"),
	}
	raw, err := engine.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	reg, err := residency.NewRegistry("eu", []string{"eu", "us"})
	if err != nil {
		t.Fatalf("residency registry: %v", err)
	}
	log := slog.New(slog.DiscardHandler)
	req := engine.DirectoryWriterActivationRequest{
		ExpectedGeneration: 1,
		WritersUpgraded:    true,
		WritersDrained:     true,
		Actor:              "cluster-operator",
		Reason:             "decorator capability test",
	}
	for name, decorated := range map[string]store.Store{
		"residency":  residency.Guard(raw, reg, log),
		"suspension": suspension.Guard(raw, log),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := engine.ActivateDirectoryWriter(ctx, decorated, cfg, req)
			if err == nil {
				t.Fatal("decorated Store unexpectedly acquired activation authority")
			}
			if result.ReopenRequired || result.Changed {
				t.Fatalf("decorated Store failure result = %+v", result)
			}
		})
	}

	// Both rejected calls were non-mutating: the undecorated engine seam can
	// still perform the single staged-1 cutover.
	result, err := engine.ActivateDirectoryWriter(ctx, raw, cfg, req)
	if err != nil || !result.Changed || result.After.ExpectedGeneration != 2 {
		t.Fatalf("raw activation after decorator refusals result=%+v err=%v", result, err)
	}
}
