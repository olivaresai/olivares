// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/store"
)

// elector is the store-internal leadership seam. It is store.LeaderElector plus
// active(), the predicate the store's own write-gate consults on the hot path
// without an interface assertion. active may remain true during an OnPromote
// bootstrap while public Active stays false until leadership is established.
// Every sqlStore holds exactly one: alwaysLeader for SQLite/single-node,
// pgElector for Postgres.
type elector interface {
	store.LeaderElector
	// active is the in-package write-gate. It is true when the elector is unarmed
	// (single-node / pre-Run), holds established leadership, or is executing its
	// promotion bootstrap; public Active deliberately excludes that last state.
	active() bool
}

// alwaysLeader is the elector for a store with no one to elect against: the
// embedded SQLite engine (one process owns the file) and any single-node use. It
// is the leader for its whole lifetime, so the write-gate, /readyz and the
// checkpointer all behave exactly as they did before. Run still fires
// OnPromote once, so the shared write-side bootstrap (provisioning the system
// tenant) runs through the same seam on every engine.
//
// Resign is NOT a no-op (review P1): after Resign the elector reports
// inactive and its fence refuses, so a straggling write or external-effect
// dispatch racing a graceful shutdown fails closed instead of riding a
// permanently-true Active. The only in-tree caller is sqlStore.Close (which
// closes the pool right after), so nothing depends on writes after Resign —
// verified by grep before this change.
type alwaysLeader struct {
	onPromote func(context.Context) error
	resigned  atomic.Bool
}

func newAlwaysLeader() *alwaysLeader { return &alwaysLeader{} }

func (a *alwaysLeader) IsLeader() bool { return !a.resigned.Load() }
func (a *alwaysLeader) Active() bool   { return !a.resigned.Load() }
func (a *alwaysLeader) active() bool   { return !a.resigned.Load() }
func (*alwaysLeader) Epoch() uint64    { return 1 }

func (a *alwaysLeader) OnPromote(fn func(context.Context) error) { a.onPromote = fn }

// Run fires the promotion callback once (the node is, and always was, the
// leader) so the system-tenant bootstrap runs identically to the HA path. A
// callback error aborts boot, exactly as a failed EnsureSystemTenant did before.
func (a *alwaysLeader) Run(ctx context.Context) error {
	if a.onPromote != nil {
		return a.onPromote(ctx)
	}
	return nil
}

// Resign marks the single-node elector inactive: there is no standby to hand
// off to, but the process is shutting down, so the write-gate and the epoch
// fence must stop reporting this node as the writer.
func (a *alwaysLeader) Resign(context.Context) error {
	a.resigned.Store(true)
	return nil
}

// FencedEpoch implements store.EpochFencer for the single-node elector. There
// is no cluster state to verify — one process owns the SQLite file — so the
// "durable" truth IS the process's liveness: the fence passes with the constant
// epoch until Resign, then refuses.
func (a *alwaysLeader) FencedEpoch(context.Context) (uint64, error) {
	if a.resigned.Load() {
		return 0, fmt.Errorf("leader-election: resigned (shutting down); refusing the epoch fence")
	}
	return 1, nil
}

var _ store.EpochFencer = (*alwaysLeader)(nil)
