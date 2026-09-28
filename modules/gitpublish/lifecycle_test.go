// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
)

func TestRuntimeLifecycleRetainsUnconfiguredRefusal(t *testing.T) {
	m := New(Options{})
	mod, ok := any(m).(sdk.Module)
	if !ok {
		t.Fatal("Git publication must satisfy the same SDK lifecycle as the composed engine modules")
	}
	d := mod.Descriptor()
	if d.Name != "olivares.gitpublish" || d.Type != sdk.TypeModule || d.APIVersion != sdk.APIVersion {
		t.Fatalf("runtime descriptor = %+v", d)
	}
	rt := runtime.New(runtime.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := rt.Stop(stopCtx); err != nil {
			t.Errorf("runtime cleanup: %v", err)
		}
	})
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	assertStatus := func(want runtime.Status) {
		t.Helper()
		got := rt.Status()
		if len(got) != 1 || got[0].Name != d.Name || got[0].Status != want {
			t.Fatalf("module status = %+v, want %s", got, want)
		}
	}
	assertStatus(runtime.StatusRunning)
	// Booting the lifecycle does not provision publication authority or ports.
	if err := m.SweepDue(ctx, model.TenantID("unconfigured")); !errors.Is(err, errUnavailable) {
		t.Fatalf("unconfigured sweep after start = %v, want authority_unavailable", err)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	assertStatus(runtime.StatusStopped)
}
