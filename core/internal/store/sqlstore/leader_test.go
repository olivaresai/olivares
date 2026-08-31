// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// sharedLock models one cluster's single advisory lock: at most one fakeBackend
// holds it. Two backends pointed at the same sharedLock contend exactly as two
// nodes against one Postgres do.
type sharedLock struct {
	mu     sync.Mutex
	heldBy *fakeBackend
	epoch  uint64
}

// fakeBackend is an in-memory lockBackend so the elector state machine is tested
// deterministically, with no Postgres and no wall-clock sleeps (the notify -race
// flake lesson: drive state, never sleep). Faults are injectable.
type fakeBackend struct {
	shared     *sharedLock
	healthyErr error // set to simulate the lock session dying under a leader
	lockErr    error
	bumpErr    error
	ensureErr  error
}

func (b *fakeBackend) ensure(context.Context) error { return b.ensureErr }

func (b *fakeBackend) tryLock(context.Context) (bool, error) {
	if b.lockErr != nil {
		return false, b.lockErr
	}
	b.shared.mu.Lock()
	defer b.shared.mu.Unlock()
	if b.shared.heldBy != nil && b.shared.heldBy != b {
		return false, nil
	}
	b.shared.heldBy = b
	return true, nil
}

func (b *fakeBackend) bumpEpoch(context.Context) (uint64, error) {
	if b.bumpErr != nil {
		return 0, b.bumpErr
	}
	b.shared.mu.Lock()
	defer b.shared.mu.Unlock()
	b.shared.epoch++
	return b.shared.epoch, nil
}

func (b *fakeBackend) healthy(context.Context) error {
	if b.healthyErr != nil {
		return b.healthyErr
	}
	b.shared.mu.Lock()
	defer b.shared.mu.Unlock()
	if b.shared.heldBy != b {
		return errors.New("lock lost")
	}
	return nil
}

func (b *fakeBackend) verifyHeldEpoch(context.Context) (uint64, error) {
	if b.healthyErr != nil {
		return 0, b.healthyErr
	}
	b.shared.mu.Lock()
	defer b.shared.mu.Unlock()
	if b.shared.heldBy != b {
		return 0, errors.New("lock lost")
	}
	return b.shared.epoch, nil
}

func (b *fakeBackend) unlock(context.Context) error {
	b.shared.mu.Lock()
	defer b.shared.mu.Unlock()
	if b.shared.heldBy == b {
		b.shared.heldBy = nil
	}
	return nil
}

func (b *fakeBackend) close() error { return nil }

// newTestElector wires a pgElector to a fake backend with a poll interval long
// enough that the background loop never fires during a test — the test drives the
// state machine by calling tick() directly.
func newTestElector(be lockBackend) *pgElector {
	return &pgElector{backend: be, log: slog.New(slog.DiscardHandler), poll: time.Hour}
}

func TestElectorUnarmedIsActive(t *testing.T) {
	// Before Run, the elector is unarmed: it reports Active (writes allowed) but not
	// IsLeader. This is the backward-compatibility property — a store opened directly
	// (every test, the embedded mode) behaves as the historical single writer.
	e := newTestElector(&fakeBackend{shared: &sharedLock{}})
	if e.IsLeader() {
		t.Fatal("unarmed elector must not report IsLeader")
	}
	if !e.Active() {
		t.Fatal("unarmed elector must report Active (single-writer fallback)")
	}
}

func TestElectorAcquiresAtRun(t *testing.T) {
	e := newTestElector(&fakeBackend{shared: &sharedLock{}})
	ctx := context.Background()
	if err := e.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer e.Resign(ctx) //nolint:errcheck
	if !e.IsLeader() || !e.Active() {
		t.Fatalf("expected leader+active after Run, got leader=%v active=%v", e.IsLeader(), e.Active())
	}
	if e.Epoch() != 1 {
		t.Fatalf("expected epoch 1 on first acquisition, got %d", e.Epoch())
	}
}

func TestElectorStandbyThenFailover(t *testing.T) {
	shared := &sharedLock{}
	leaderBE := &fakeBackend{shared: shared}
	standbyBE := &fakeBackend{shared: shared}
	ctx := context.Background()

	leaderEl := newTestElector(leaderBE)
	if err := leaderEl.Run(ctx); err != nil {
		t.Fatalf("leader Run: %v", err)
	}
	standbyEl := newTestElector(standbyBE)
	if err := standbyEl.Run(ctx); err != nil {
		t.Fatalf("standby Run: %v", err)
	}

	// Exactly one leader; the standby is armed-and-not-active (the LB drains it).
	if !leaderEl.IsLeader() {
		t.Fatal("first node should be leader")
	}
	if standbyEl.IsLeader() || standbyEl.Active() {
		t.Fatalf("second node must be a drained standby, got leader=%v active=%v", standbyEl.IsLeader(), standbyEl.Active())
	}

	// The standby keeps failing to acquire while the leader holds the lock.
	standbyEl.tick(ctx)
	if standbyEl.IsLeader() {
		t.Fatal("standby acquired leadership while the leader still held the lock")
	}

	// The leader resigns (graceful handoff): the standby's next tick promotes it.
	if err := leaderEl.Resign(ctx); err != nil {
		t.Fatalf("leader Resign: %v", err)
	}
	standbyEl.tick(ctx)
	if !standbyEl.IsLeader() || !standbyEl.Active() {
		t.Fatalf("standby should have taken over after the leader resigned, got leader=%v active=%v", standbyEl.IsLeader(), standbyEl.Active())
	}
	if standbyEl.Epoch() != 2 {
		t.Fatalf("epoch should advance to 2 on the second acquisition, got %d", standbyEl.Epoch())
	}
	_ = standbyEl.Resign(ctx)
}

func TestElectorStepsDownOnLostSession(t *testing.T) {
	be := &fakeBackend{shared: &sharedLock{}}
	e := newTestElector(be)
	ctx := context.Background()
	if err := e.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer e.Resign(ctx) //nolint:errcheck
	if !e.IsLeader() {
		t.Fatal("expected leader")
	}
	// The held lock session dies (a network partition / Postgres reaping a frozen
	// backend). The next maintenance tick must step the node down so it stops writing
	// and /readyz drains it — no split brain.
	be.healthyErr = errors.New("connection reset")
	e.tick(ctx)
	if e.IsLeader() || e.Active() {
		t.Fatalf("a leader that lost its lock session must step down, got leader=%v active=%v", e.IsLeader(), e.Active())
	}
}

func TestElectorPromoteFailureStaysFollower(t *testing.T) {
	shared := &sharedLock{}
	be := &fakeBackend{shared: shared}
	e := newTestElector(be)
	bootErr := errors.New("bootstrap blew up")
	e.OnPromote(func(context.Context) error { return bootErr })

	err := e.Run(context.Background())
	if !errors.Is(err, bootErr) {
		t.Fatalf("Run should surface the promotion-bootstrap error, got %v", err)
	}
	if e.IsLeader() || e.Active() {
		t.Fatalf("a node whose bootstrap failed must not lead, got leader=%v active=%v", e.IsLeader(), e.Active())
	}
	// The lock must have been released so another node (or a retry) can take it.
	shared.mu.Lock()
	held := shared.heldBy
	shared.mu.Unlock()
	if held != nil {
		t.Fatal("the lock must be released after a failed promotion")
	}
}

func TestElectorActiveDuringPromoteBootstrap(t *testing.T) {
	// The OnPromote bootstrap (provisioning the system tenant) writes, so the
	// store-private gate must already permit writes WHILE it runs. Public Active
	// and IsLeader must stay false until that work completes, otherwise /readyz or
	// a public pump could serve a stale runtime snapshot during promotion.
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "promotion-bootstrap")
	be := &fakeBackend{shared: &sharedLock{}}
	e := newTestElector(be)
	st.(*sqlStore).elector = e
	var privateGateDuringBootstrap bool
	var publicActiveDuringBootstrap bool
	e.OnPromote(func(ctx context.Context) error {
		privateGateDuringBootstrap = e.active()
		publicActiveDuringBootstrap = e.Active()
		if e.IsLeader() {
			t.Error("IsLeader must not be advertised until the bootstrap completes")
		}
		if e.Active() {
			t.Error("public Active must remain false until the bootstrap completes")
		}
		// Pre-bump FencedEpoch is not a usable durable fact: a real Postgres row can
		// still name the prior holder. It must nevertheless pass the local
		// `promoting` gate and reach its durable verifier; this injected verifier
		// error distinguishes that behavior from a premature standby rejection.
		preBumpFenceErr := errors.New("durable fence not established until epoch bump")
		be.healthyErr = preBumpFenceErr
		_, fenceErr := e.FencedEpoch(ctx)
		be.healthyErr = nil
		if !errors.Is(fenceErr, preBumpFenceErr) {
			t.Errorf("FencedEpoch during promotion = %v, want durable verifier error", fenceErr)
		}
		if err := st.Mutate(ctx, tenant, func(store.Scope) error { return nil }); err != nil {
			t.Errorf("Store.Mutate during promotion bootstrap: %v", err)
			return err
		}
		return nil
	})
	if err := e.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer e.Resign(ctx) //nolint:errcheck
	if !privateGateDuringBootstrap {
		t.Fatal("the write-gate must be open during the promotion bootstrap")
	}
	if publicActiveDuringBootstrap {
		t.Fatal("public Active must not be true during the promotion bootstrap")
	}
	if !e.IsLeader() || !e.Active() || !e.active() {
		t.Fatalf("after bootstrap expected established leadership and both gates, got leader=%v active=%v private=%v",
			e.IsLeader(), e.Active(), e.active())
	}
}

func TestPGElectorFencedEpoch(t *testing.T) {
	shared := &sharedLock{}
	be := &fakeBackend{shared: shared}
	e := newTestElector(be)
	ctx := context.Background()

	// Unarmed (pre-Run): the single-writer fallback — no lock session or cluster
	// to verify against, so the fence returns the local (zero) epoch.
	if ep, err := e.FencedEpoch(ctx); err != nil || ep != 0 {
		t.Fatalf("unarmed fence = (%d, %v), want (0, nil)", ep, err)
	}

	if err := e.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ep, err := e.FencedEpoch(ctx); err != nil || ep != 1 {
		t.Fatalf("leader fence = (%d, %v), want (1, nil)", ep, err)
	}

	// The held lock session dies. The POINT of the durable fence (review
	// P1): the local cache still says leader (no maintenance tick has run), yet
	// the fence must fail NOW — Active()/Epoch() would lie until the next tick.
	be.healthyErr = errors.New("connection reset")
	if !e.IsLeader() {
		t.Fatal("precondition: local state must still believe it leads")
	}
	if _, err := e.FencedEpoch(ctx); err == nil {
		t.Fatal("fence passed with a dead lock session")
	}
	be.healthyErr = nil

	// Another node steals the lock and bumps the persisted epoch while this
	// process still caches leader/epoch 1: the durable read exposes the foreign
	// holder and the fence refuses.
	other := &fakeBackend{shared: shared}
	shared.mu.Lock()
	shared.heldBy = other
	shared.epoch++
	shared.mu.Unlock()
	if !e.IsLeader() || e.Epoch() != 1 {
		t.Fatalf("precondition: cached state should lag (leader=%v epoch=%d)", e.IsLeader(), e.Epoch())
	}
	if _, err := e.FencedEpoch(ctx); err == nil {
		t.Fatal("fence passed after another node took the lock and bumped the epoch")
	}

	// Resigned: the fence refuses outright.
	if err := e.Resign(ctx); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	if _, err := e.FencedEpoch(ctx); err == nil {
		t.Fatal("fence passed after Resign")
	}
}

func TestAlwaysLeader(t *testing.T) {
	a := newAlwaysLeader()
	if !a.IsLeader() || !a.Active() || !a.active() {
		t.Fatal("alwaysLeader must always be leader and active")
	}
	var promoted bool
	a.OnPromote(func(context.Context) error { promoted = true; return nil })
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !promoted {
		t.Fatal("alwaysLeader.Run must fire OnPromote so the single-node bootstrap runs")
	}
	if err := a.Resign(context.Background()); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	// After Resign the single-node elector fences too (review P1): the
	// process is shutting down, so no further governed write or external effect
	// may claim leadership — including through the durable epoch fence.
	if a.IsLeader() || a.Active() || a.active() {
		t.Fatalf("alwaysLeader still active after Resign: leader=%v active=%v", a.IsLeader(), a.Active())
	}
	if _, err := a.FencedEpoch(context.Background()); err == nil {
		t.Fatal("alwaysLeader fence passed after Resign")
	}
}

func TestAlwaysLeaderFencedEpoch(t *testing.T) {
	a := newAlwaysLeader()
	ep, err := a.FencedEpoch(context.Background())
	if err != nil || ep != 1 {
		t.Fatalf("alwaysLeader fence = (%d, %v), want (1, nil)", ep, err)
	}
}
