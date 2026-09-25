// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package suspension_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
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

// TestGuardStorePreservesCommitOutcome is HS-W for the suspension decorator.
//
// Unlike residency there is no "does not enforce" fast path here: every
// deployment can suspend a tenant, so this guard is always in the return path of
// every tenant-scoped unit of work in the product. If it flattened the error
// chain, the commit-outcome sentinel would never reach a caller at all.
//
// Class: P-GREEN at every cut. Its mutant is a wrapper that re-wraps with %v.
func TestGuardStorePreservesCommitOutcome(t *testing.T) {
	t.Parallel()

	cause := errors.New("commit acknowledgement never read")
	inner := commitOutcomeFakeStore{
		err: fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, cause),
	}
	guard := suspension.Guard(inner, nil)
	if _, undecorated := guard.(commitOutcomeFakeStore); undecorated {
		t.Fatal("Guard returned the inner store unchanged, so this control would measure nothing")
	}

	tenant := model.NewTenantID()
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "Mutate", run: func() error {
			return guard.Mutate(context.Background(), tenant, func(store.Scope) error { return nil })
		}},
		{name: "View", run: func() error {
			return guard.View(context.Background(), tenant, func(store.Scope) error { return nil })
		}},
		{name: "Custody", run: func() error {
			return guard.Custody(context.Background(), tenant, func(store.CustodyScope) error { return nil })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.run()
			if !errors.Is(got, store.ErrCommitOutcomeUnknown) {
				t.Errorf("the suspension guard dropped the commit-outcome sentinel: %v", got)
			}
			if !errors.Is(got, cause) {
				t.Errorf("the suspension guard dropped the original cause: %v", got)
			}
		})
	}

	t.Run("nil_is_still_nil", func(t *testing.T) {
		t.Parallel()
		clean := suspension.Guard(commitOutcomeFakeStore{}, nil)
		if err := clean.Mutate(context.Background(), model.NewTenantID(),
			func(store.Scope) error { return nil }); err != nil {
			t.Errorf("a committed unit of work came back as %v, want nil", err)
		}
	})
}
