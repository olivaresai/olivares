// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package suspension_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

// A store.Store decorator must forward every optional capability the wrapped
// store exposes — and must NOT claim one the wrapped store lacks. Both halves are
// load-bearing at boot, and this file pins both for BOTH guards, because the
// suspension guard wraps the residency one and inherits whatever it drops.
//
// The bug this test exists to prevent is not hypothetical: on the code this
// session branched from, residency.Guard swallowed store.RolloutStater, and
// `olivares serve --region eu` died at boot with "eventing: the store does not
// expose durable rollout state, so the egress destination control cannot
// establish whether it is in force" — i.e. NO region-scoped deployment could
// start. Reproduced against a built binary before the fix.

func TestGuardsPreserveRolloutStater(t *testing.T) {
	t.Parallel()
	inner := openStore(t)
	if _, ok := inner.(store.RolloutStater); !ok {
		t.Fatal("precondition: the real store must expose RolloutStater")
	}
	reg, err := residency.NewRegistry("eu", []string{"eu", "us"})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	log := slog.New(slog.DiscardHandler)
	tenant := createOrg(t, inner, "capability-forwarding")
	plan, err := store.NewTransactionLockPlan("capability:forwarding")
	if err != nil {
		t.Fatal(err)
	}

	// The production stack, in the order boot.go composes it: residency inside,
	// suspension outside. If EITHER drops the capability, this fails — which is
	// precisely what a region-scoped boot did.
	for name, st := range map[string]store.Store{
		"suspension":            suspension.Guard(inner, log),
		"residency":             residency.Guard(inner, reg, log),
		"suspension(residency)": suspension.Guard(residency.Guard(inner, reg, log), log),
		"residency(suspension)": residency.Guard(suspension.Guard(inner, log), reg, log),
	} {
		rs, ok := st.(store.RolloutStater)
		if !ok {
			t.Fatalf("%s: the guard swallowed store.RolloutStater; a region-scoped instance cannot boot", name)
		}
		selective, ok := st.(store.SelectiveMutator)
		if !ok {
			t.Fatalf("%s: the guard swallowed store.SelectiveMutator", name)
		}
		ran := false
		if err := selective.MutateCoordination(context.Background(), tenant, plan, func(store.CoordinationMutationScope) error {
			ran = true
			return nil
		}); err != nil || !ran {
			t.Fatalf("%s: forwarded selective mutation ran=%t err=%v", name, ran, err)
		}
		// And it must actually reach the wrapped store, not just satisfy the type.
		if _, err := rs.RolloutState(context.Background(), "eventing.egress.destination.v1"); err == nil {
			continue // classified: forwarded and answered
		} else if err.Error() == "" {
			t.Fatalf("%s: RolloutState returned an empty error", name)
		}
		status, supported, err := st.(store.DirectoryStatuser).DirectoryStatus(context.Background())
		if err != nil || !supported {
			t.Fatalf("%s: directory status=%+v supported=%t err=%v",
				name, status, supported, err)
		}
		if status.Enabled || status.ControlMode != store.DirectoryControlStaged ||
			status.WriterPosture != store.DirectoryWriterSQLiteCapability ||
			status.ExpectedGeneration != 1 {
			t.Fatalf("%s: forwarded directory status = %+v", name, status)
		}
	}
}

// TestGuardsDoNotFabricateRolloutStater is the other half, and it is the reason
// the capability lives on a separate type instead of being implemented
// unconditionally. The composition root REFUSES TO BOOT when the store cannot
// expose durable rollout state — a deliberate deny-closed check. A decorator that
// always satisfied the interface would answer "yes I can" on behalf of a store
// that cannot, and that check could never fire again.
func TestGuardsDoNotFabricateRolloutStater(t *testing.T) {
	t.Parallel()
	reg, _ := residency.NewRegistry("eu", []string{"eu"})
	log := slog.New(slog.DiscardHandler)
	bare := storeWithoutRollout{}

	for name, st := range map[string]store.Store{
		"suspension": suspension.Guard(bare, log),
		"residency":  residency.Guard(bare, reg, log),
	} {
		if _, ok := st.(store.RolloutStater); ok {
			t.Fatalf("%s: the guard fabricated store.RolloutStater over a store that has none — boot's deny-closed check can no longer fire", name)
		}
		if _, ok := st.(store.SelectiveMutator); ok {
			t.Fatalf("%s: the guard fabricated store.SelectiveMutator over a store that has none", name)
		}
		status, supported, err := st.(store.DirectoryStatuser).DirectoryStatus(context.Background())
		if err != nil || supported || status != (store.DirectoryStatus{}) {
			t.Fatalf("%s fabricated directory support: status=%+v supported=%t err=%v",
				name, status, supported, err)
		}
	}
}

func TestGuardsPreserveOptionalCapabilityTruthTable(t *testing.T) {
	t.Parallel()
	reg, _ := residency.NewRegistry("eu", []string{"eu"})
	log := slog.New(slog.DiscardHandler)
	cases := []struct {
		name                    string
		inner                   store.Store
		wantRollout, wantNarrow bool
	}{
		{name: "neither", inner: storeWithoutRollout{}},
		{name: "rollout", inner: rolloutOnlyStore{storeWithoutRollout{}}, wantRollout: true},
		{name: "selective", inner: selectiveOnlyStore{storeWithoutRollout{}}, wantNarrow: true},
		{name: "both", inner: rolloutSelectiveStore{selectiveOnlyStore{storeWithoutRollout{}}}, wantRollout: true, wantNarrow: true},
	}
	for _, tc := range cases {
		for order, wrapped := range map[string]store.Store{
			"suspension(residency)": suspension.Guard(residency.Guard(tc.inner, reg, log), log),
			"residency(suspension)": residency.Guard(suspension.Guard(tc.inner, log), reg, log),
		} {
			_, gotRollout := wrapped.(store.RolloutStater)
			_, gotNarrow := wrapped.(store.SelectiveMutator)
			if gotRollout != tc.wantRollout || gotNarrow != tc.wantNarrow {
				t.Errorf("%s/%s capabilities rollout=%t selective=%t, want %t/%t",
					tc.name, order, gotRollout, gotNarrow, tc.wantRollout, tc.wantNarrow)
			}
		}
	}
}

// storeWithoutRollout is a store.Store that deliberately does NOT implement
// store.RolloutStater. Every method panics: the fabrication test only inspects
// the type, and a panic makes any accidental call impossible to miss.
type storeWithoutRollout struct{}

type selectiveOnlyStore struct{ storeWithoutRollout }

func (selectiveOnlyStore) MutateCoordination(
	context.Context, model.TenantID, store.TransactionLockPlan,
	func(store.CoordinationMutationScope) error,
) error {
	panic("not called")
}

func (selectiveOnlyStore) MutateEvidenceOperation(
	context.Context, model.TenantID, store.EvidenceOperationPlan,
	func(store.EvidenceOperationMutationScope) error,
) error {
	panic("not called")
}

type rolloutOnlyStore struct{ storeWithoutRollout }

func (rolloutOnlyStore) RolloutState(context.Context, string) (store.RolloutState, error) {
	panic("not called")
}
func (rolloutOnlyStore) RolloutHistory(context.Context, string) ([]store.RolloutTransitionRecord, error) {
	panic("not called")
}
func (rolloutOnlyStore) SetRolloutMode(context.Context, store.RolloutTransition) (store.RolloutState, error) {
	panic("not called")
}

type rolloutSelectiveStore struct{ selectiveOnlyStore }

func (rolloutSelectiveStore) RolloutState(context.Context, string) (store.RolloutState, error) {
	panic("not called")
}
func (rolloutSelectiveStore) RolloutHistory(context.Context, string) ([]store.RolloutTransitionRecord, error) {
	panic("not called")
}
func (rolloutSelectiveStore) SetRolloutMode(context.Context, store.RolloutTransition) (store.RolloutState, error) {
	panic("not called")
}

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
