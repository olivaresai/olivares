// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These tests cover the C3-L1 lazy load. They deliberately do NOT reuse
// typedEvidenceFixture's view counter: it is unsynchronized, and several cases here
// run concurrent callers. Every probe below is atomic or mutex-protected, ordering is
// established with channels and explicit hooks rather than sleeps, and the durable
// invariants are proved against a real store through the actual typed entry points.

var errRuntimeLoadUnexpectedMutate = errors.New("governance test: the lazy load path must never mutate")

var errRuntimeLoadInjected = errors.New("governance test: injected durable read failure")

// runtimeLoadDeadlockGuard bounds the coordinated waits in the post-admission case.
// It is a guard against hanging the suite, NOT a synchronization device: ordering is
// established by channels alone, and if this fires the case fails on its assertions.
const runtimeLoadDeadlockGuard = 30 * time.Second

// runtimeLoadClock is the private operational-clock seam. Admission reads it from
// every caller goroutine, so it is mutex-protected.
type runtimeLoadClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRuntimeLoadClock() *runtimeLoadClock {
	return &runtimeLoadClock{now: time.Date(2036, time.March, 4, 5, 6, 7, 0, time.UTC)}
}

func (c *runtimeLoadClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *runtimeLoadClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// runtimeLoadData is api.ModuleData over a REAL store. It counts every View and
// rejects every Mutate by default, which is how the read-only claim is proved rather
// than asserted. Hooks are installed before any concurrent caller starts.
type runtimeLoadData struct {
	st          store.Store
	views       atomic.Int64
	mutates     atomic.Int64
	allowMutate bool
	viewErr     func(ordinal int64) error
	afterView   func(ordinal int64)
	wrap        func(store.Scope) store.Scope
}

func (d *runtimeLoadData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	ordinal := d.views.Add(1)
	if d.viewErr != nil {
		if err := d.viewErr(ordinal); err != nil {
			return err
		}
	}
	err := d.st.View(ctx, tenant, func(sc store.Scope) error {
		if d.wrap != nil {
			sc = d.wrap(sc)
		}
		return fn(sc)
	})
	if d.afterView != nil {
		d.afterView(ordinal)
	}
	return err
}

func (d *runtimeLoadData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mutates.Add(1)
	if !d.allowMutate {
		return errRuntimeLoadUnexpectedMutate
	}
	return d.st.Mutate(ctx, tenant, fn)
}

var _ api.ModuleData = (*runtimeLoadData)(nil)

type runtimeLoadFixture struct {
	m      *Module
	st     store.Store
	data   *runtimeLoadData
	clock  *runtimeLoadClock
	tenant model.TenantID
}

// newRuntimeLoadFixture constructs the Module FIRST and only then creates the tenant,
// which is the situation this increment exists for: a durable tenant that no boot
// reload ever saw.
func newRuntimeLoadFixture(t *testing.T, opts ...Option) *runtimeLoadFixture {
	t.Helper()
	ctx := context.Background()
	m := New(opts...)
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f := &runtimeLoadFixture{m: m, st: st, clock: newRuntimeLoadClock()}
	f.data = &runtimeLoadData{st: st}
	m.UseData(f.data)
	m.grants.admission.now = f.clock.Now
	f.tenant = f.createTenant(t, "runtime-load")
	return f
}

func (f *runtimeLoadFixture) createTenant(t *testing.T, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := f.st.System(context.Background(), func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(context.Background(), model.Org{
			Name: slug, Slug: slug, Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tenant
}

// countLoads wraps the real private loader so a test can count callbacks without
// replacing the durable behavior being proved.
func (f *runtimeLoadFixture) countLoads() *atomic.Int64 {
	calls := &atomic.Int64{}
	durable := f.m.reloadTenantGrants
	f.m.grants.loadTenant = func(ctx context.Context, tenant model.TenantID) error {
		calls.Add(1)
		return durable(ctx, tenant)
	}
	return calls
}

func (f *runtimeLoadFixture) scoped(t *testing.T) auth.ScopedEvidenceAuthorizer {
	t.Helper()
	scoped, ok := f.m.ScopedGrants().(auth.ScopedEvidenceAuthorizer)
	if !ok {
		t.Fatal("the module's scoped authorizer does not expose typed scoped evidence")
	}
	return scoped
}

func (f *runtimeLoadFixture) restrict(t *testing.T) auth.PolicyEvidenceEvaluator {
	t.Helper()
	restrict, ok := any(f.m.grants).(auth.PolicyEvidenceEvaluator)
	if !ok {
		t.Fatal("the scoped engine does not expose typed restrict-view evidence")
	}
	return restrict
}

// seedActiveCedar publishes one durable authored revision without going through the
// module, so the tenant has policy that this process has never compiled.
func (f *runtimeLoadFixture) seedActiveCedar(t *testing.T, tenant model.TenantID, source string) {
	t.Helper()
	if err := f.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, _, err := appendRevision(context.Background(), sc, surfaceCedar, source, "runtime-load-test", true, true, "")
		return err
	}); err != nil {
		t.Fatalf("seed authored Cedar: %v", err)
	}
}

func runtimeLoadEpoch(t *testing.T, st store.Store, tenant model.TenantID) store.AuthorizationFactRef {
	t.Helper()
	var fact store.AuthorizationFactRef
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		fact, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatalf("read exact authorization epoch: %v", err)
	}
	return fact
}

func runtimeLoadContext(t *testing.T) context.Context {
	t.Helper()
	return typedEvidenceContext(t, time.Now().Add(time.Minute))
}

// requireNoDurableWrite proves the read-only claim from both sides: the counted
// handle saw no Mutate, and the durable freshness row is byte-identical.
func (f *runtimeLoadFixture) requireNoDurableWrite(t *testing.T, before FreshnessRecord, foundBefore bool) {
	t.Helper()
	if got := f.data.mutates.Load(); got != 0 {
		t.Fatalf("Mutate calls = %d, want 0: the lazy load must be read-only", got)
	}
	after, foundAfter, err := PolicyFreshness(context.Background(), f.st, f.tenant)
	if err != nil {
		t.Fatalf("read durable freshness: %v", err)
	}
	if foundBefore != foundAfter || after != before {
		t.Fatalf("durable freshness changed: %v/%#v -> %v/%#v", foundBefore, before, foundAfter, after)
	}
}

func runtimeLoadInFlight(a *evidenceLoadAdmission) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.inFlight)
}

// runtimeLoadCooldownOrder returns the retained cooldown tenants oldest first and
// checks that the FIFO and the index are exactly the same set on every walk.
func runtimeLoadCooldownOrder(t *testing.T, a *evidenceLoadAdmission) []model.TenantID {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	var order []model.TenantID
	for entry := a.oldest; entry != nil; entry = entry.newer {
		if a.cooldown[entry.tenant] != entry {
			t.Fatalf("FIFO node for %s is not the indexed entry", entry.tenant)
		}
		if entry.newer != nil && entry.newer.older != entry {
			t.Fatalf("FIFO links are not symmetric at %s", entry.tenant)
		}
		order = append(order, entry.tenant)
		if len(order) > evidenceLoadCooldownCapacity {
			t.Fatal("FIFO retains more entries than the capacity")
		}
	}
	if len(order) != len(a.cooldown) {
		t.Fatalf("FIFO holds %d entries, the index holds %d", len(order), len(a.cooldown))
	}
	if len(order) > 0 && (a.newest == nil || a.newest.tenant != order[len(order)-1]) {
		t.Fatal("the FIFO tail is not the newest entry")
	}
	return order
}

// Group 1. A durable empty tenant created after Module construction obtains its
// exact-generation ABSTAIN/CLEAN contribution with no fabricated policy or freshness,
// including from a second Module over the same Store.
func TestEvidenceRuntimeLoadServesTenantCreatedAfterConstruction(t *testing.T) {
	t.Run("empty tenant abstains at its exact generation", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		freshBefore, foundBefore, err := PolicyFreshness(context.Background(), f.st, f.tenant)
		if err != nil {
			t.Fatalf("read durable freshness: %v", err)
		}
		if foundBefore {
			t.Fatal("a newly created tenant must have no freshness row")
		}
		loads := f.countLoads()
		if _, loaded := f.m.grants.tenantState(f.tenant); loaded {
			t.Fatal("the tenant must have no runtime state before the first contribution")
		}

		decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil {
			t.Fatalf("ScopedEvidence: %v", err)
		}
		if decision.Effect != auth.EffectAbstain ||
			decision.ResourceGuard.Verdict != auth.CheckClean ||
			decision.ForbidAbsence.Verdict != auth.CheckClean ||
			decision.ForbidAbsence.Code != evidenceCodeScopedClean {
			t.Fatalf("cold typed evidence = %#v, want ABSTAIN with CLEAN guard/forbid", decision)
		}
		fact := runtimeLoadEpoch(t, f.st, f.tenant)
		if len(decision.Facts) == 0 || decision.Facts[0] != fact || fact.Version != 1 {
			t.Fatalf("facts = %#v, want the exact generation %#v", decision.Facts, fact)
		}
		if !decision.FreshUntil.After(decision.ObservedAt) {
			t.Fatalf("window = %s..%s, want a finite forward window", decision.ObservedAt, decision.FreshUntil)
		}
		if got := loads.Load(); got != 1 {
			t.Fatalf("loader calls = %d, want exactly 1", got)
		}
		state, loaded := f.m.grants.tenantState(f.tenant)
		if !loaded || state.set != nil || state.selection != (activationID{}) || state.generation != fact {
			t.Fatalf("installed state = %#v (loaded %v), want an explicitly empty union at %#v", state, loaded, fact)
		}
		f.requireNoDurableWrite(t, freshBefore, foundBefore)

		policy, err := f.restrict(t).EvaluateEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || policy.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("restrict-view evidence = %#v, %v; want CLEAN", policy, err)
		}
		if got := loads.Load(); got != 1 {
			t.Fatalf("loader calls after the warm call = %d, want still 1", got)
		}
	})

	t.Run("a second module over the same store loads independently", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		second := New()
		secondData := &runtimeLoadData{st: f.st}
		second.UseData(secondData)
		second.grants.admission.now = f.clock.Now

		decision, err := second.grants.ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.Effect != auth.EffectAbstain || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("second module evidence = %#v, %v; want ABSTAIN/CLEAN", decision, err)
		}
		if got := secondData.mutates.Load(); got != 0 {
			t.Fatalf("second module Mutate calls = %d, want 0", got)
		}
		if _, loaded := f.m.grants.tenantState(f.tenant); loaded {
			t.Fatal("the second module's load must not install state in the first module")
		}
	})
}

// Group 2. A failed cold load is UNKNOWN and recoverable; a caller canceled during
// the load retains no cooldown.
func TestEvidenceRuntimeLoadRecoversAfterFailure(t *testing.T) {
	t.Run("failed load is unknown then recovers", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		freshBefore, foundBefore, err := PolicyFreshness(context.Background(), f.st, f.tenant)
		if err != nil {
			t.Fatalf("read durable freshness: %v", err)
		}
		loads := f.countLoads()
		f.data.viewErr = func(ordinal int64) error {
			if ordinal == 1 {
				return errRuntimeLoadInjected
			}
			return nil
		}

		decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("failed cold load = %#v, %v; want UNKNOWN with no facts", decision, err)
		}
		if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 1 || order[0] != f.tenant {
			t.Fatalf("cooldown = %v, want exactly the failed tenant", order)
		}
		if got := runtimeLoadInFlight(f.m.grants.admission); got != 0 {
			t.Fatalf("in-flight entries = %d, want 0 after completion", got)
		}

		// While the cooldown is retained the loader is not called again.
		if decision, err = f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant)); err != nil ||
			decision.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("shed call = %#v, %v; want UNKNOWN", decision, err)
		}
		if got := loads.Load(); got != 1 {
			t.Fatalf("loader calls during cooldown = %d, want still 1", got)
		}

		f.clock.advance(evidenceLoadCooldown)
		decision, err = f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.Effect != auth.EffectAbstain || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("recovered call = %#v, %v; want ABSTAIN/CLEAN without a restart", decision, err)
		}
		if got := loads.Load(); got != 2 {
			t.Fatalf("loader calls after recovery = %d, want 2", got)
		}
		if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 0 {
			t.Fatalf("cooldown after success = %v, want empty", order)
		}
		f.requireNoDurableWrite(t, freshBefore, foundBefore)
	})

	t.Run("a caller canceled during the load retains no cooldown", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
		defer cancel()
		durable := f.m.reloadTenantGrants
		f.m.grants.loadTenant = func(loadCtx context.Context, tenant model.TenantID) error {
			cancel()
			_ = durable(loadCtx, tenant)
			return loadCtx.Err()
		}

		decision, err := f.scoped(t).ScopedEvidence(ctx, typedEvidenceRequest(f.tenant))
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("canceled caller = %#v, %v; want UNKNOWN", decision, err)
		}
		if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 0 {
			t.Fatalf("cooldown after cancellation = %v, want empty", order)
		}
		if got := runtimeLoadInFlight(f.m.grants.admission); got != 0 {
			t.Fatalf("in-flight entries = %d, want 0", got)
		}

		f.m.grants.loadTenant = durable
		decision, err = f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.Effect != auth.EffectAbstain || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("healthy retry = %#v, %v; want ABSTAIN/CLEAN", decision, err)
		}
	})
}

// Group 3. Exactly one callback per tenant is in flight; another tenant proceeds; the
// two typed methods share the helper and keep their distinct algorithms.
func TestEvidenceRuntimeLoadAdmitsOneCallbackPerTenant(t *testing.T) {
	t.Run("same tenant overlap invokes one callback while another tenant proceeds", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		other := f.createTenant(t, "runtime-load-other")
		var calls atomic.Int64
		entered := make(chan struct{})
		release := make(chan struct{})
		durable := f.m.reloadTenantGrants
		// The block is OUTSIDE the store transaction on purpose: parking inside a
		// View would serialize the single SQLite connection and prove nothing about
		// admission.
		f.m.grants.loadTenant = func(ctx context.Context, tenant model.TenantID) error {
			if calls.Add(1) == 1 && tenant == f.tenant {
				close(entered)
				<-release
			}
			return durable(ctx, tenant)
		}

		first := make(chan auth.ScopedEvidenceDecision, 1)
		go func() {
			decision, _ := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
			first <- decision
		}()
		<-entered

		overlapping, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || overlapping.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("overlapping same-tenant call = %#v, %v; want UNKNOWN", overlapping, err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("callbacks during same-tenant overlap = %d, want exactly 1", got)
		}

		crossTenant, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(other))
		if err != nil || crossTenant.Effect != auth.EffectAbstain || crossTenant.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("other tenant during the block = %#v, %v; want ABSTAIN/CLEAN", crossTenant, err)
		}

		close(release)
		if decision := <-first; decision.Effect != auth.EffectAbstain || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("blocked caller = %#v, want ABSTAIN/CLEAN", decision)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("total callbacks = %d, want 2 (one per tenant)", got)
		}
		if got := runtimeLoadInFlight(f.m.grants.admission); got != 0 {
			t.Fatalf("in-flight entries = %d, want 0", got)
		}
	})

	t.Run("both typed methods load once and keep distinct algorithms", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		f.seedActiveCedar(t, f.tenant, `permit(principal, action == Action::"agent:read", resource);`)
		loads := f.countLoads()

		// The restrict view evaluates the same policy forbid-only: a permit imposes
		// no restriction there, while the scoped graph reports the grant.
		policy, err := f.restrict(t).EvaluateEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || policy.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("restrict-view cold load = %#v, %v; want CLEAN", policy, err)
		}
		if got := loads.Load(); got != 1 {
			t.Fatalf("loader calls = %d, want exactly 1", got)
		}
		decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.Effect != auth.EffectGrant {
			t.Fatalf("scoped evidence = %#v, %v; want the scoped GRANT effect", decision, err)
		}
		if got := loads.Load(); got != 1 {
			t.Fatalf("loader calls after the warm second method = %d, want still 1", got)
		}
	})
}

// Group 4. The bounded admission structures.
func TestEvidenceLoadAdmissionIsBounded(t *testing.T) {
	newAdmission := func() (*evidenceLoadAdmission, *runtimeLoadClock) {
		clock := newRuntimeLoadClock()
		return newEvidenceLoadAdmission(clock.Now), clock
	}
	failOnce := func(t *testing.T, a *evidenceLoadAdmission, tenant model.TenantID) {
		t.Helper()
		if !a.admit(tenant) {
			t.Fatalf("admit %s: refused", tenant)
		}
		a.release(tenant, evidenceLoadOutcomeCooldown)
	}

	t.Run("capacity evicts the oldest completed entry", func(t *testing.T) {
		a, _ := newAdmission()
		tenants := make([]model.TenantID, 0, evidenceLoadCooldownCapacity+1)
		for i := 0; i < evidenceLoadCooldownCapacity+1; i++ {
			tenants = append(tenants, model.TenantID(model.NewID()))
		}
		for _, tenant := range tenants[:evidenceLoadCooldownCapacity] {
			failOnce(t, a, tenant)
		}
		order := runtimeLoadCooldownOrder(t, a)
		if len(order) != evidenceLoadCooldownCapacity || order[0] != tenants[0] {
			t.Fatalf("cooldown holds %d entries starting at %s, want %d starting at %s",
				len(order), order[0], evidenceLoadCooldownCapacity, tenants[0])
		}

		newest := tenants[evidenceLoadCooldownCapacity]
		failOnce(t, a, newest)
		order = runtimeLoadCooldownOrder(t, a)
		if len(order) != evidenceLoadCooldownCapacity {
			t.Fatalf("cooldown holds %d entries, want the fixed capacity %d", len(order), evidenceLoadCooldownCapacity)
		}
		if order[0] != tenants[1] || order[len(order)-1] != newest {
			t.Fatalf("cooldown runs %s..%s, want the oldest evicted and %s at the tail",
				order[0], order[len(order)-1], newest)
		}
		// The evicted tenant is gone from BOTH structures, so it is admitted again.
		if !a.admit(tenants[0]) {
			t.Fatal("the evicted tenant must not stay refused")
		}
		a.release(tenants[0], evidenceLoadOutcomeNoCooldown)
	})

	// This is an EXPIRATION and reinsertion control, not a retained-entry update.
	// Advancing a whole cooldown expires BOTH entries, so observeLocked sweeps them
	// from the FIFO front and the following failure allocates a fresh node. The
	// retained-update branch is covered by the private bookkeeping test below.
	t.Run("expired entries are reclaimed and the tenant reinserted as a new node", func(t *testing.T) {
		a, clock := newAdmission()
		first, second := model.TenantID(model.NewID()), model.TenantID(model.NewID())
		failOnce(t, a, first)
		failOnce(t, a, second)
		clock.advance(evidenceLoadCooldown)
		failOnce(t, a, first)
		order := runtimeLoadCooldownOrder(t, a)
		if len(order) != 1 || order[0] != first {
			t.Fatalf("cooldown = %v, want only the reinserted tenant after both expired", order)
		}
	})

	// insertCooldownLocked's retained-entry branch is DEFENSIVE bookkeeping, and this
	// test says so rather than dressing it up: normal admission cannot reach it,
	// because admit refuses a tenant whose cooldown is still retained and only the
	// in-flight owner ever inserts one, so a live owner never has a retained entry to
	// update. The helper is therefore driven directly, under the mutex it documents.
	t.Run("private bookkeeping: updating a retained entry reuses its single node", func(t *testing.T) {
		a, clock := newAdmission()
		older, newer := model.TenantID(model.NewID()), model.TenantID(model.NewID())
		failOnce(t, a, older)
		clock.advance(evidenceLoadCooldown / 4)
		failOnce(t, a, newer)
		clock.advance(evidenceLoadCooldown / 4)

		// Both entries must be genuinely retained and UNEXPIRED here, or this would
		// silently repeat the expiration control above instead of proving an update.
		reading := clock.Now()
		a.mu.Lock()
		node, retained := a.cooldown[older]
		companion, companionRetained := a.cooldown[newer]
		var nodeExpiry, companionExpiry time.Time
		if retained {
			nodeExpiry = node.expiry
		}
		if companionRetained {
			companionExpiry = companion.expiry
		}
		a.mu.Unlock()
		if !retained || !companionRetained {
			t.Fatalf("retained older=%v newer=%v, want both still retained", retained, companionRetained)
		}
		if !nodeExpiry.After(reading) || !companionExpiry.After(reading) {
			t.Fatalf("expiries %s and %s are not both after %s: both entries must still be live",
				nodeExpiry, companionExpiry, reading)
		}
		if order := runtimeLoadCooldownOrder(t, a); len(order) != 2 || order[0] != older || order[1] != newer {
			t.Fatalf("cooldown before the update = %v, want the older entry at the head", order)
		}

		a.mu.Lock()
		a.insertCooldownLocked(older, reading)
		a.mu.Unlock()

		// runtimeLoadCooldownOrder re-walks both representations: it fails on a
		// cardinality mismatch, an unindexed node, asymmetric links or a wrong tail.
		order := runtimeLoadCooldownOrder(t, a)
		if len(order) != 2 || order[0] != newer || order[1] != older {
			t.Fatalf("cooldown after the update = %v, want the updated entry alone at the tail", order)
		}
		a.mu.Lock()
		updated, untouched := a.cooldown[older], a.cooldown[newer]
		a.mu.Unlock()
		if updated != node {
			t.Fatal("the retained tenant must keep its single node, not gain a replacement")
		}
		if want := reading.Add(evidenceLoadCooldown); !updated.expiry.Equal(want) {
			t.Fatalf("refreshed expiry = %s, want exactly %s", updated.expiry, want)
		}
		if !updated.expiry.After(nodeExpiry) {
			t.Fatalf("refreshed expiry %s did not advance from %s", updated.expiry, nodeExpiry)
		}
		if untouched != companion || !untouched.expiry.Equal(companionExpiry) {
			t.Fatal("updating one retained tenant must not disturb the other")
		}
		if got := runtimeLoadInFlight(a); got != 0 {
			t.Fatalf("in-flight entries = %d, want 0", got)
		}
	})

	t.Run("expiry, success and clock regression release the tenant", func(t *testing.T) {
		a, clock := newAdmission()
		tenant := model.TenantID(model.NewID())
		failOnce(t, a, tenant)
		if a.admit(tenant) {
			t.Fatal("a retained unexpired cooldown must refuse")
		}
		clock.advance(evidenceLoadCooldown)
		if !a.admit(tenant) {
			t.Fatal("an expired cooldown must admit")
		}
		a.release(tenant, evidenceLoadOutcomeSuccess)
		if order := runtimeLoadCooldownOrder(t, a); len(order) != 0 {
			t.Fatalf("cooldown after success = %v, want empty", order)
		}

		failOnce(t, a, tenant)
		other := model.TenantID(model.NewID())
		if !a.admit(other) {
			t.Fatal("admit the in-flight tenant")
		}
		clock.advance(-time.Hour)
		if !a.admit(tenant) {
			t.Fatal("a clock regression must clear the retained cooldown")
		}
		if got := runtimeLoadInFlight(a); got != 2 {
			t.Fatalf("in-flight entries after the regression = %d, want both retained", got)
		}
		a.release(tenant, evidenceLoadOutcomeNoCooldown)
		a.release(other, evidenceLoadOutcomeNoCooldown)
		if got := runtimeLoadInFlight(a); got != 0 {
			t.Fatalf("in-flight entries = %d, want 0", got)
		}
	})

	t.Run("a panicking loader releases ownership and adds no cooldown", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		f.m.grants.loadTenant = func(context.Context, model.TenantID) error {
			panic("governance test: loader panic")
		}
		decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("panicking loader = %#v, %v; want the existing UNKNOWN boundary", decision, err)
		}
		if got := runtimeLoadInFlight(f.m.grants.admission); got != 0 {
			t.Fatalf("in-flight entries after a panic = %d, want 0", got)
		}
		if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 0 {
			t.Fatalf("cooldown after a panic = %v, want empty", order)
		}
	})
}

// Group 5. Completion uses the actual recaptured state, not the callback's error.
func TestEvidenceRuntimeLoadCompletionUsesRecapturedState(t *testing.T) {
	usable := func(t *testing.T, f *runtimeLoadFixture) scopedTenantState {
		t.Helper()
		return scopedTenantState{
			generation:     runtimeLoadEpoch(t, f.st, f.tenant),
			available:      true,
			freshnessValid: true,
		}
	}

	t.Run("a newer usable writer wins despite the callback error", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		f.m.grants.loadTenant = func(context.Context, model.TenantID) error {
			if _, err := f.m.grants.installIfNotOlder(f.tenant, usable(t, f)); err != nil {
				t.Errorf("install the concurrent writer state: %v", err)
			}
			return errRuntimeLoadInjected
		}
		state, ready := f.m.grants.ensureEvidenceRuntime(runtimeLoadContext(t), f.tenant)
		if !ready {
			t.Fatal("a usable recaptured state must enter the evidence path despite the callback error")
		}
		installed, _ := f.m.grants.tenantState(f.tenant)
		if state.operation == nil || state.operation != installed.operation {
			t.Fatalf("returned state = %#v, want the exact recaptured state %#v", state, installed)
		}
		if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 0 {
			t.Fatalf("cooldown after a usable outcome = %v, want empty", order)
		}
	})

	t.Run("a nil result cannot bless a newer unavailable state", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		f.m.grants.loadTenant = func(context.Context, model.TenantID) error {
			before, loaded := f.m.grants.tenantState(f.tenant)
			f.m.grants.markUnavailableIfStillSame(f.tenant, before, loaded)
			return nil
		}
		if _, ready := f.m.grants.ensureEvidenceRuntime(runtimeLoadContext(t), f.tenant); ready {
			t.Fatal("an unavailable recaptured state must not enter the evidence path")
		}
		if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 1 || order[0] != f.tenant {
			t.Fatalf("cooldown = %v, want the completed failure retained", order)
		}
		decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("typed evidence = %#v, %v; want UNKNOWN", decision, err)
		}
	})

	// The tenant starts COLD, so the helper's first capture cannot short-circuit on
	// the warm path: reaching the post-admission recapture is the only way to answer
	// without a load. The operational-clock seam parks execution inside admit's
	// metadata critical section — after that first capture — while a separate
	// goroutine installs a usable runtime, which is exactly the capture/admission gap
	// the construction requires the recapture to close. The seam only signals and
	// waits; it never takes the runtime lock while admission metadata is held, so the
	// two locks are never nested.
	t.Run("a usable state installed during admission is recaptured without a load", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		loads := f.countLoads()
		if _, loaded := f.m.grants.tenantState(f.tenant); loaded {
			t.Fatal("this case is meaningless unless the runtime starts cold")
		}
		target := usable(t, f) // built on the test goroutine; the writer only installs it

		guard, cancelGuard := context.WithTimeout(context.Background(), runtimeLoadDeadlockGuard)
		defer cancelGuard()
		admitting := make(chan struct{})
		installedRuntime := make(chan struct{})
		var parked atomic.Bool
		reading := f.clock.Now()
		f.m.grants.admission.now = func() time.Time {
			// Park on the FIRST reading only. That reading is taken by admit, under
			// the admission mutex, after ensureEvidenceRuntime's initial capture.
			if parked.CompareAndSwap(false, true) {
				close(admitting)
				select {
				case <-installedRuntime:
				case <-guard.Done(): // deadlock guard, never an ordering device
				}
			}
			return reading
		}

		var installed scopedTenantState
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			select {
			case <-admitting:
			case <-guard.Done():
				return
			}
			if _, err := f.m.grants.installIfNotOlder(f.tenant, target); err != nil {
				t.Errorf("install a usable state during admission: %v", err)
				return
			}
			installed, _ = f.m.grants.tenantState(f.tenant)
			close(installedRuntime)
		}()

		state, ready := f.m.grants.ensureEvidenceRuntime(runtimeLoadContext(t), f.tenant)
		<-writerDone

		if !ready {
			t.Fatal("a state installed during admission must enter the evidence path")
		}
		if got := loads.Load(); got != 0 {
			t.Fatalf("loader calls = %d, want 0: the post-admission recapture must skip the load", got)
		}
		if state.operation == nil || state.operation != installed.operation {
			t.Fatalf("returned state = %#v, want the exact state installed during admission %#v",
				state, installed)
		}
		if got := runtimeLoadInFlight(f.m.grants.admission); got != 0 {
			t.Fatalf("in-flight entries = %d, want 0 after completion", got)
		}
		if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 0 {
			t.Fatalf("cooldown = %v, want empty: this outcome is a success", order)
		}
	})
}

// Group 6. Bounded freshness is never backfilled by this path.
func TestEvidenceRuntimeLoadRespectsBoundedFreshness(t *testing.T) {
	t.Run("an empty selection loads under a bound without backfill", func(t *testing.T) {
		f := newRuntimeLoadFixture(t, WithOfflinePolicyStaleness(time.Hour))
		decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.Effect != auth.EffectAbstain || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("bounded empty tenant = %#v, %v; want ABSTAIN/CLEAN", decision, err)
		}
		f.requireNoDurableWrite(t, FreshnessRecord{}, false)
	})

	t.Run("a selected policy without its anchor stays unavailable", func(t *testing.T) {
		f := newRuntimeLoadFixture(t, WithOfflinePolicyStaleness(time.Hour))
		f.seedActiveCedar(t, f.tenant, `permit(principal, action == Action::"agent:read", resource);`)
		decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("bounded selection without an anchor = %#v, %v; want UNKNOWN", decision, err)
		}
		state, loaded := f.m.grants.tenantState(f.tenant)
		if !loaded || state.available || state.freshnessValid {
			t.Fatalf("installed state = %#v (loaded %v), want the unavailable durable result", state, loaded)
		}
		f.requireNoDurableWrite(t, FreshnessRecord{}, false)
	})
}

// Group 7. A deletion committed before the post-load generation observation yields
// UNKNOWN, and the residual cache entry cannot replace that observation.
func TestEvidenceRuntimeLoadObservesCommittedDeletion(t *testing.T) {
	f := newRuntimeLoadFixture(t)
	var (
		mu       sync.Mutex
		sequence []string
	)
	record := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		sequence = append(sequence, step)
	}
	var opened atomic.Int64
	f.data.wrap = func(sc store.Scope) store.Scope {
		if opened.Add(1) == 1 {
			record("load-view-opened")
		} else {
			record("evidence-view-opened")
		}
		return sc
	}
	f.data.afterView = func(ordinal int64) {
		// Ordinal 1 is the lazy loader's own View; it has closed here, so the drop
		// below commits BEFORE the evidence View opens and observes the generation.
		if ordinal != 1 {
			return
		}
		record("load-view-closed")
		if err := f.st.System(context.Background(), func(sys store.SystemScope) error {
			return sys.DropTenant(context.Background(), f.tenant)
		}); err != nil {
			t.Errorf("commit tenant deletion: %v", err)
			return
		}
		record("deletion-committed")
	}

	decision, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
	if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
		t.Fatalf("evidence after a committed deletion = %#v, %v; want UNKNOWN with no facts", decision, err)
	}
	mu.Lock()
	got := append([]string(nil), sequence...)
	mu.Unlock()
	// The COMPLETE order, not a prefix: the load's View opens and closes, the
	// deletion commits, and only then does the post-load evidence View open and fail
	// to observe a durable generation.
	want := []string{"load-view-opened", "load-view-closed", "deletion-committed", "evidence-view-opened"}
	if len(got) != len(want) {
		t.Fatalf("sequence = %v, want exactly %v", got, want)
	}
	for i, step := range want {
		if got[i] != step {
			t.Fatalf("sequence = %v, want exactly %v", got, want)
		}
	}
	// The pre-drop load did leave a residual cache entry. It has no authority: the
	// absent durable generation decided the contribution.
	if state, loaded := f.m.grants.tenantState(f.tenant); !loaded || !state.available {
		t.Fatalf("residual runtime state = %#v (loaded %v); this test must exercise the durable observation, "+
			"not an emptied cache", state, loaded)
	}
}

// Group 8. Warm and legacy paths never load, and every rejected precondition
// allocates no admission state.
func TestEvidenceRuntimeLoadPreconditions(t *testing.T) {
	t.Run("warm typed and legacy paths invoke no loader", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		loads := f.countLoads()
		req := typedEvidenceRequest(f.tenant)
		if _, err := f.m.grants.Scoped(context.Background(), req); err != nil {
			t.Fatalf("legacy Scoped: %v", err)
		}
		if _, err := f.m.grants.Evaluate(context.Background(), req); err != nil {
			t.Fatalf("legacy Evaluate: %v", err)
		}
		if got := loads.Load(); got != 0 {
			t.Fatalf("loader calls from the legacy paths = %d, want 0", got)
		}

		if _, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), req); err != nil {
			t.Fatalf("ScopedEvidence: %v", err)
		}
		if got := loads.Load(); got != 1 {
			t.Fatalf("loader calls = %d, want exactly 1", got)
		}
		if _, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), req); err != nil {
			t.Fatalf("warm ScopedEvidence: %v", err)
		}
		if _, err := f.restrict(t).EvaluateEvidence(runtimeLoadContext(t), req); err != nil {
			t.Fatalf("warm EvaluateEvidence: %v", err)
		}
		if got := loads.Load(); got != 1 {
			t.Fatalf("loader calls after two warm contributions = %d, want still 1", got)
		}
	})

	t.Run("rejected preconditions perform no load and allocate nothing", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		loads := f.countLoads()
		healthy := runtimeLoadContext(t)
		canceled, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
		cancel()

		for _, tc := range []struct {
			name   string
			ctx    context.Context
			tenant model.TenantID
		}{
			{name: "zero tenant", ctx: healthy, tenant: ""},
			{name: "system tenant", ctx: healthy, tenant: model.SystemTenantID},
			{name: "malformed tenant", ctx: healthy, tenant: model.TenantID("not-a-tenant")},
			{name: "noncanonical tenant", ctx: healthy, tenant: model.TenantID("A0A0A0A0-0000-4000-8000-000000000000")},
			{name: "no deadline", ctx: context.Background(), tenant: f.tenant},
			{name: "canceled caller", ctx: canceled, tenant: f.tenant},
			{name: "expired deadline", ctx: typedEvidenceContext(t, time.Now().Add(-time.Minute)), tenant: f.tenant},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, ready := f.m.grants.ensureEvidenceRuntime(tc.ctx, tc.tenant); ready {
					t.Fatal("a rejected precondition must not enter the evidence path")
				}
				if got := loads.Load(); got != 0 {
					t.Fatalf("loader calls = %d, want 0", got)
				}
				if got := runtimeLoadInFlight(f.m.grants.admission); got != 0 {
					t.Fatalf("in-flight entries = %d, want 0", got)
				}
				if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 0 {
					t.Fatalf("cooldown = %v, want empty", order)
				}
			})
		}
	})

	t.Run("missing wiring performs no load", func(t *testing.T) {
		f := newRuntimeLoadFixture(t)
		for _, tc := range []struct {
			name   string
			engine *scopedEngine
		}{
			{name: "nil engine", engine: nil},
			{name: "no resolver", engine: &scopedEngine{
				loadTenant: f.m.reloadTenantGrants, admission: newEvidenceLoadAdmission(f.clock.Now),
			}},
			{name: "no resolver data", engine: &scopedEngine{
				resolver:   &scopeResolver{},
				loadTenant: f.m.reloadTenantGrants, admission: newEvidenceLoadAdmission(f.clock.Now),
			}},
			{name: "no loader", engine: &scopedEngine{
				resolver: &scopeResolver{data: f.data}, admission: newEvidenceLoadAdmission(f.clock.Now),
			}},
			{name: "no admission", engine: &scopedEngine{
				resolver: &scopeResolver{data: f.data}, loadTenant: f.m.reloadTenantGrants,
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, ready := tc.engine.ensureEvidenceRuntime(runtimeLoadContext(t), f.tenant); ready {
					t.Fatal("miswired engines must not enter the evidence path")
				}
				if tc.engine != nil && tc.engine.admission != nil {
					if got := runtimeLoadInFlight(tc.engine.admission); got != 0 {
						t.Fatalf("in-flight entries = %d, want 0", got)
					}
				}
				decision, err := tc.engine.ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
				if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
					t.Fatalf("typed evidence = %#v, %v; want UNKNOWN", decision, err)
				}
			})
		}
	})
}

// Group 9. A load may warm the cache and consume the caller's whole budget.
func TestEvidenceRuntimeLoadMayWarmCacheWithoutBudget(t *testing.T) {
	f := newRuntimeLoadFixture(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	durable := f.m.reloadTenantGrants
	var loads atomic.Int64
	f.m.grants.loadTenant = func(loadCtx context.Context, tenant model.TenantID) error {
		loads.Add(1)
		err := durable(loadCtx, tenant)
		// The load succeeded and warmed the runtime; the caller's budget is gone.
		cancel()
		return err
	}

	decision, err := f.scoped(t).ScopedEvidence(ctx, typedEvidenceRequest(f.tenant))
	if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
		t.Fatalf("exhausted caller = %#v, %v; want UNKNOWN with no facts", decision, err)
	}
	if got := f.data.views.Load(); got != 1 {
		t.Fatalf("View calls = %d, want only the load's own View: no evidence View may open", got)
	}
	if order := runtimeLoadCooldownOrder(t, f.m.grants.admission); len(order) != 0 {
		t.Fatalf("cooldown after an exhausted caller = %v, want empty", order)
	}
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !state.available {
		t.Fatalf("runtime state = %#v (loaded %v), want the warmed state the caller paid for", state, loaded)
	}

	f.m.grants.loadTenant = durable
	later, err := f.scoped(t).ScopedEvidence(runtimeLoadContext(t), typedEvidenceRequest(f.tenant))
	if err != nil || later.Effect != auth.EffectAbstain || later.ForbidAbsence.Verdict != auth.CheckClean {
		t.Fatalf("later healthy caller = %#v, %v; want ABSTAIN/CLEAN from the warm state", later, err)
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("loader calls = %d, want exactly 1", got)
	}
}
