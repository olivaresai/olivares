// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package residency_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
)

// A store.Store decorator must forward every optional capability the wrapped
// store exposes, and must never claim one it lacks. Guard forwarded
// AuditSpoolStatuser but SWALLOWED store.RolloutStater, and that made a
// region-scoped instance unbootable — the composition root asks the store for
// durable rollout state and refuses to start without it:
//
//	$ olivares serve --insecure --region eu --data-dir …
//	INFO  residency: region-scoped instance active — serving only this region's tenants
//	Error: eventing: the store does not expose durable rollout state, so the egress
//	       destination control cannot establish whether it is in force
//
// Reproduced against a built binary. No `--region` deployment could start,
// and nothing in the residency suite noticed, because every test here drives the
// guard directly and never boots the composition root.

func TestGuardPreservesRolloutStater(t *testing.T) {
	t.Parallel()
	inner := openStore(t)
	if _, ok := inner.(store.RolloutStater); !ok {
		t.Fatal("precondition: the real store must expose RolloutStater")
	}
	reg, err := residency.NewRegistry("eu", []string{"eu", "us"})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if _, ok := residency.Guard(inner, reg, nil).(store.RolloutStater); !ok {
		t.Fatal("the guard swallowed store.RolloutStater: a region-scoped instance cannot boot")
	}
	wrapped := residency.Guard(inner, reg, nil)
	status, supported, err := wrapped.(store.DirectoryStatuser).DirectoryStatus(context.Background())
	if err != nil || !supported {
		t.Fatalf("the guard swallowed DirectoryStatuser: status=%+v supported=%t err=%v",
			status, supported, err)
	}
	if status.Enabled || !status.EpochCoverageComplete ||
		status.ControlMode != store.DirectoryControlStaged ||
		status.WriterPosture != store.DirectoryWriterSQLiteCapability ||
		status.ExpectedGeneration != 1 {
		t.Fatalf("forwarded directory status = %+v, want SQLite staged/OFF complete generation 1", status)
	}
}

// TestGuardDoesNotFabricateRolloutStater is the other half, and the reason the
// capability lives on a separate type rather than being implemented
// unconditionally. Boot REFUSES to start when the store cannot expose durable
// rollout state — a deliberate deny-closed check. A decorator that always
// satisfied the interface would answer "yes I can" for a store that cannot, and
// that check could never fire again.
func TestGuardDoesNotFabricateRolloutStater(t *testing.T) {
	t.Parallel()
	reg, _ := residency.NewRegistry("eu", []string{"eu", "us"})
	wrapped := residency.Guard(storeWithoutRollout{}, reg, nil)
	if _, ok := wrapped.(store.RolloutStater); ok {
		t.Fatal("the guard fabricated store.RolloutStater over a store that has none — boot's deny-closed check can no longer fire")
	}
	status, supported, err := wrapped.(store.DirectoryStatuser).DirectoryStatus(context.Background())
	if err != nil || supported || status != (store.DirectoryStatus{}) {
		t.Fatalf("guard fabricated directory support: status=%+v supported=%t err=%v",
			status, supported, err)
	}
}

// storeWithoutRollout is a store.Store that deliberately does NOT implement
// store.RolloutStater. Every method panics: the fabrication test inspects only
// the type, so any accidental call must be impossible to miss.
type storeWithoutRollout struct{}

func (storeWithoutRollout) View(context.Context, model.TenantID, func(store.Scope) error) error {
	panic("not called")
}

func (storeWithoutRollout) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	panic("not called")
}
func (storeWithoutRollout) Custody(context.Context, model.TenantID, func(store.CustodyScope) error) error {
	panic("not called")
}
func (storeWithoutRollout) Export(context.Context, model.TenantID, func(store.ExportScope) error) error {
	panic("not called")
}
func (storeWithoutRollout) System(context.Context, func(store.SystemScope) error) error {
	panic("not called")
}
func (storeWithoutRollout) AuthView(context.Context, func(store.AuthScope) error) error {
	panic("not called")
}
func (storeWithoutRollout) AuthMutate(context.Context, func(store.AuthScope) error) error {
	panic("not called")
}
func (storeWithoutRollout) Leader() store.LeaderElector { panic("not called") }
func (storeWithoutRollout) Engine() store.Engine        { panic("not called") }
func (storeWithoutRollout) Ping(context.Context) error  { panic("not called") }
func (storeWithoutRollout) Close() error                { panic("not called") }
