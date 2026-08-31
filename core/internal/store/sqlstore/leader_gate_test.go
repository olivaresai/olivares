// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// toggleElector is a hand-driven elector for the write-gate test: active() flips
// between leader (writes allowed) and standby (writes rejected) on command.
type toggleElector struct{ activeVal bool }

func (e *toggleElector) IsLeader() bool                        { return e.activeVal }
func (e *toggleElector) Active() bool                          { return e.activeVal }
func (e *toggleElector) active() bool                          { return e.activeVal }
func (e *toggleElector) Run(context.Context) error             { return nil }
func (e *toggleElector) Resign(context.Context) error          { return nil }
func (e *toggleElector) Epoch() uint64                         { return 0 }
func (e *toggleElector) OnPromote(func(context.Context) error) {}

func TestWriteGateRejectsStandbyWrites(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	// Provision the system tenant + a tenant while still the (always)leader, then
	// demote this node to a standby and prove every write fails closed.
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	tenant := provisionTenant(t, st, "acme")

	el := &toggleElector{activeVal: false}
	st.(*sqlStore).elector = el

	// Tenant write: rejected.
	if err := st.Mutate(ctx, tenant, func(store.Scope) error { return nil }); !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("Mutate on a standby must return ErrNotLeader, got %v", err)
	}
	// Auth write (routes through Mutate): rejected.
	if err := st.AuthMutate(ctx, func(store.AuthScope) error { return nil }); !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("AuthMutate on a standby must return ErrNotLeader, got %v", err)
	}
	// System provisioning write: rejected.
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.CreateOrg(ctx, model.Org{Name: "beta", Slug: "beta", Status: model.StatusActive})
		return e
	}); !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("CreateOrg on a standby must return ErrNotLeader, got %v", err)
	}
	// Tenant deletion write: rejected.
	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, tenant)
	}); !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("DropTenant on a standby must return ErrNotLeader, got %v", err)
	}

	// READS stay allowed on a standby (it can serve verification / be ready to take
	// over): a View must not be gated.
	if err := st.View(ctx, tenant, func(store.Scope) error { return nil }); err != nil {
		t.Fatalf("View on a standby must be allowed, got %v", err)
	}

	// Promote back to leader: writes flow again.
	el.activeVal = true
	if err := st.Mutate(ctx, tenant, func(store.Scope) error { return nil }); err != nil {
		t.Fatalf("Mutate after promotion must succeed, got %v", err)
	}
}
