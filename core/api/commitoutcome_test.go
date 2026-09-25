// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// commitOutcomeFakeStore returns one prepared error from every unit of work,
// WITHOUT invoking the callback. That is deliberate: the decorator passes its
// own wrapped callback to the inner store, so an inner store that never calls it
// isolates exactly the property under test — what the decorator does with the
// error it gets BACK.
type commitOutcomeFakeStore struct{ err error }

func (f commitOutcomeFakeStore) View(context.Context, model.TenantID, func(store.Scope) error) error {
	return f.err
}

func (f commitOutcomeFakeStore) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return f.err
}

func (f commitOutcomeFakeStore) Custody(context.Context, model.TenantID, func(store.CustodyScope) error) error {
	return f.err
}

func (f commitOutcomeFakeStore) Export(context.Context, model.TenantID, func(store.ExportScope) error) error {
	return f.err
}

func (f commitOutcomeFakeStore) System(context.Context, func(store.SystemScope) error) error {
	return f.err
}

func (f commitOutcomeFakeStore) AuthView(context.Context, func(store.AuthScope) error) error {
	return f.err
}

func (f commitOutcomeFakeStore) AuthMutate(context.Context, func(store.AuthScope) error) error {
	return f.err
}

func (f commitOutcomeFakeStore) Engine() store.Engine {
	return store.EngineSQLite
}

func (f commitOutcomeFakeStore) Ping(context.Context) error {
	return nil
}

func (f commitOutcomeFakeStore) Leader() store.LeaderElector {
	return nil
}

func (f commitOutcomeFakeStore) Close() error {
	return nil
}

// TestModuleDataPreservesCommitOutcome is HS-W for the least-privilege data
// seam every in-process module receives.
//
// This is the layer a product module actually holds, so it is the last place the
// commit-outcome sentinel can be lost before a module decides what to tell its
// caller. ModuleData wraps only the CALLBACK (workspace confinement); it never
// touches the error coming back, and this control is what keeps that true.
//
// LIMIT, stated rather than implied: the confined path cannot be driven from an
// external test package, because the context marker is package-private. What the
// source shows, and what this control therefore pins, is the return path — which
// is identical in both cases, since confineIfMarked substitutes fn and returns
// the inner store's error unchanged.
//
// Class: P-GREEN at every cut. Its mutant is a wrapper that re-wraps with %v.
func TestModuleDataPreservesCommitOutcome(t *testing.T) {
	t.Parallel()

	cause := errors.New("commit acknowledgement never read")
	inner := commitOutcomeFakeStore{
		err: fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, cause),
	}
	tenant := model.NewTenantID()
	data := api.NewModuleData(inner)
	scoped := api.NewScopedData(inner, tenant)

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "ModuleData_Mutate", run: func() error {
			return data.Mutate(context.Background(), tenant, func(store.Scope) error { return nil })
		}},
		{name: "ModuleData_View", run: func() error {
			return data.View(context.Background(), tenant, func(store.Scope) error { return nil })
		}},
		{name: "ScopedData_Mutate", run: func() error {
			return scoped.Mutate(context.Background(), func(store.Scope) error { return nil })
		}},
		{name: "ScopedData_View", run: func() error {
			return scoped.View(context.Background(), func(store.Scope) error { return nil })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.run()
			if !errors.Is(got, store.ErrCommitOutcomeUnknown) {
				t.Errorf("the module data seam dropped the commit-outcome sentinel: %v", got)
			}
			if !errors.Is(got, cause) {
				t.Errorf("the module data seam dropped the original cause: %v", got)
			}
		})
	}

	t.Run("nil_is_still_nil", func(t *testing.T) {
		t.Parallel()
		clean := api.NewModuleData(commitOutcomeFakeStore{})
		if err := clean.Mutate(context.Background(), tenant,
			func(store.Scope) error { return nil }); err != nil {
			t.Errorf("a committed unit of work came back as %v, want nil", err)
		}
	})
}
