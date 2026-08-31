// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A synchronization point INSIDE the transaction.
//
// The window F3 is about opens between the moment a governed write reads the
// claim and the moment it commits, and it cannot be reproduced from outside: a
// mutex race between two goroutines proves only that goroutines interleave, not
// that a transaction does. What is needed is a barrier the test can hold OPEN
// while a takeover commits, sitting between the module's own read of the claim
// row and its own write.
//
// It is built by wrapping, not by putting a hook in production code. api.ModuleData
// is an interface (core/api/modules.go:99) and store.Scope is an interface too, so
// EMBEDDING both lets this file override exactly one method — the Ext repository
// lookup — and leave the other thirty alone.

// barrierData wraps a ModuleData so a chosen entity's reads can be intercepted
// inside a live transaction.
type barrierData struct {
	inner     api.ModuleData
	kind      model.Kind
	afterRead func()
	// beforeCallback runs after the underlying transaction has been OPENED and before
	// the production closure runs in it. That is the only place a latency window
	// between "the caller decided to write" and "the transaction is live" can be
	// simulated without touching production code — see the F6 test.
	beforeCallback func()
	// afterMutate runs once, after a write transaction has ENDED and before the
	// caller's next statement. It is the gap R3-01 lives in: the instant in which the
	// process holds an observation that the database does not, and in which a clock
	// can move backwards under it.
	afterMutate func()
}

func (b *barrierData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return b.inner.View(ctx, tenant, func(sc store.Scope) error {
		return fn(&barrierScope{Scope: sc, owner: b})
	})
}

func (b *barrierData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := b.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		if b.beforeCallback != nil {
			hold := b.beforeCallback
			b.beforeCallback = nil // one shot
			hold()
		}
		return fn(&barrierScope{Scope: sc, owner: b})
	})
	if b.afterMutate != nil {
		fire := b.afterMutate
		b.afterMutate = nil // one shot: the gap is after ONE transaction, not every one
		fire()
	}
	return err
}

// deadAfterData models a process that stops executing after a fixed number of WRITE
// transactions. Reads keep working, because the assertion afterwards has to be able
// to look at the row the dead process left behind.
//
// It is how "the observation and its record are the SAME transaction" is asserted
// rather than asserted-about. A path that still needs a follow-up transaction to make
// its observation durable cannot get one here, so it leaves the row `active` and the
// test goes red — which is exactly the probe the third contrast ran, and exactly what
// R3-01 was.
type deadAfterData struct {
	inner  api.ModuleData
	budget int
}

// errProcessGone is what a transaction attempted past the budget gets: no execution
// at all, the way a process that is no longer running executes nothing.
var errProcessGone = errors.New("test: the process is gone; no further transaction runs")

func (d *deadAfterData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *deadAfterData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if d.budget <= 0 {
		return errProcessGone
	}
	d.budget--
	return d.inner.Mutate(ctx, tenant, fn)
}

// mutateFaults injects a failure at the two points a "commit and then refuse" shape
// can be lied to by, and neither is reachable by writing a normal test.
//
// A callback that returns nil is NOT the same event as a transaction that committed:
// the store still has to commit, and that can fail. A shape that hands back the
// callback's business verdict without looking at what Mutate returned is claiming a
// retirement that may never have landed.
type mutateFaults struct {
	inner api.ModuleData
	// fault is consulted per Mutate call, 1-based. (err, true) fails the call WITHOUT
	// the callback ever running — a transaction that never opened. (err, false) lets
	// the callback run and then fails the transaction, so everything it wrote reverts.
	fault func(call int) (error, bool)
	mu    sync.Mutex
	calls int
}

// nthViewFaultData fails one chosen read before its callback starts. ClaimAdmission
// performs an ActiveClaim read and then an Authority read; faulting the second one
// proves the exported authority check is a production caller, not dead API or a
// cached comparison against the first observation.
type nthViewFaultData struct {
	inner  api.ModuleData
	failAt int
	err    error
	views  int
}

func (f *nthViewFaultData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	f.views++
	if f.failAt > 0 && f.views == f.failAt {
		return f.err
	}
	return f.inner.View(ctx, tenant, fn)
}

func (f *nthViewFaultData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return f.inner.Mutate(ctx, tenant, fn)
}

func (f *mutateFaults) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return f.inner.View(ctx, tenant, fn)
}

func (f *mutateFaults) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	var injected error
	skip := false
	if f.fault != nil {
		injected, skip = f.fault(call)
	}
	if injected != nil && skip {
		return injected
	}
	return f.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := fn(sc); err != nil {
			return err
		}
		return injected // nil unless this call is meant to fail at commit
	})
}

// errCommitFailed and errNeverOpened name the two injected failures, so an assertion
// can tell "the store failed" from "the business logic refused".
var (
	errCommitFailed = errors.New("test: the transaction did not commit")
	errNeverOpened  = errors.New("test: the transaction never opened")
)

func TestClaimAdmissionCallsFreshAuthorityCheck(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()
	sid, lease := claimed(t, m, tenant, "authority-caller", "worker:a")

	innerCalls := 0
	inner := launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
		innerCalls++
		return LaunchDecision{Allowed: true}, nil
	})
	real := m.data
	faults := &nthViewFaultData{inner: real, failAt: 2, err: store.ErrNotLeader}
	m.data = faults
	gate := NewClaimAdmission(inner, m, ProviderOperated, IntentHolder)
	intent := LaunchIntent{
		RunRef: "authority-caller", ClaimSID: sid, Holder: "worker:a", Fence: lease.Fence,
	}

	decision, err := gate.Authorize(ctx, tenant, intent)
	if decision.Allowed || !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("faulted Authority = allowed:%v err:%v, want denied ErrNotLeader", decision.Allowed, err)
	}
	if faults.views != 2 {
		t.Fatalf("View calls = %d, want ActiveClaim then Authority", faults.views)
	}
	if innerCalls != 0 {
		t.Fatalf("inner gate ran %d times after Authority failed", innerCalls)
	}
	if _, found := getLive(t, m, st, tenant, intent.RunRef); found {
		t.Fatal("an unreadable Authority was falsely signaled as unclaimed activity")
	}

	// Non-trigger direction: with the second read healthy, the same exact live
	// holder and fence proceed through the inner admission gate.
	m.data = real
	decision, err = gate.Authorize(ctx, tenant, intent)
	if err != nil || !decision.Allowed {
		t.Fatalf("healthy Authority = allowed:%v err:%v, want allowed", decision.Allowed, err)
	}
	if innerCalls != 1 {
		t.Fatalf("inner gate calls = %d, want 1", innerCalls)
	}
}

// barrierScope embeds the real Scope and overrides only Ext.
type barrierScope struct {
	store.Scope
	owner *barrierData
}

func (s *barrierScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != s.owner.kind {
		return repo, err
	}
	return &barrierRepo{GenericRepo: repo, owner: s.owner}, nil
}

// barrierRepo embeds the real repository and overrides only List, which is how
// findClaim reads the claim row (claim.go:455-468).
type barrierRepo struct {
	store.GenericRepo
	owner *barrierData
}

func (r *barrierRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	recs, page, err := r.GenericRepo.List(ctx, q)
	if err == nil && r.owner.afterRead != nil {
		fire := r.owner.afterRead
		r.owner.afterRead = nil // one shot: the barrier is for ONE read, not every read
		fire()
	}
	return recs, page, err
}

// ---------------------------------------------------------------------------
// F3 — the write that arrives after authority moved.
// ---------------------------------------------------------------------------

// pgSess opens a module on an ISOLATED Postgres database, or skips.
//
// Postgres is not a nicety here, it is the only engine on which the defect is
// reachable: the SQLite store runs every transaction on ONE connection
// (sqlstore/store.go:760-761), so the takeover this test needs to commit MID-
// transaction cannot even open its own. A green SQLite run would prove nothing,
// which is exactly the class of false comfort the cross-backend suite already
// documents (identity_crossbackend_test.go:17-29).
func pgSess(t *testing.T) (*Module, model.TenantID, *testClock) {
	t.Helper()
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the F3 interleaving is NOT exercised (it is unreachable on SQLite)", enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	m := New()
	clk := &testClock{now: baseTime}
	m.clock = clk
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: pg.App, Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return m, tenant, clk
}

// THE F3 TEST. The fence advances BETWEEN the check and the write, and the late
// write must not land.
//
// Authority answers "this holder could act", which is a statement about the past
// by the time the effect happens. What makes the token mean something is that the
// WRITE is conditioned on it: the claim row is re-read and updated inside the
// governed transaction, so the store's version predicate turns a takeover into a
// conflict and rolls the effect back with it.
//
// The barrier is what makes the window reproducible. It sits INSIDE the governed
// transaction, between the module's own read of the claim and its own write, so
// the takeover commits in precisely the gap the defect lives in. A mutex race
// between two goroutines would not do it — it would prove that goroutines
// interleave, not that transactions do.
func TestF3_LateWriteAfterTakeoverIsRejected(t *testing.T) {
	t.Parallel()

	m, tenant, clk := pgSess(t)
	ctx := context.Background()

	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: "run-late", Origin: OriginOperated, At: baseTime,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	lease, err := m.Claim(ctx, tenant, sid, "user:first", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The barrier: when the governed transaction reads the claim row, let a takeover
	// commit before letting it continue to its write.
	real := m.data
	took := make(chan struct{})
	bar := &barrierData{inner: real, kind: claimKind}
	bar.afterRead = func() {
		// The first holder's lease lapses and somebody else takes the session. This
		// runs on ANOTHER connection, inside its own transaction, and commits before
		// the governed write below reaches its CAS.
		clk.advance(2 * time.Minute)
		if _, terr := (&Module{data: real, clock: clk}).Claim(ctx, tenant, sid, "user:second", time.Minute); terr != nil {
			t.Errorf("the takeover could not commit: %v", terr)
		}
		close(took)
	}
	m.data = bar

	// The governed write, still carrying the authority it was admitted with. This is
	// the real production consumer, not a stand-in.
	_, err = m.persistCreate(ctx, tenant, "run-late", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)

	<-took
	m.data = real

	if err == nil {
		t.Fatal("the late write LANDED: a writer whose authority had already moved on was allowed to commit")
	}
	if !errIsStatus(err, 403) {
		t.Errorf("late write refused with %v, want a 403 refusal of authority", err)
	}
	// And the effect itself must be gone, not merely reported as failed.
	if _, lerr := m.loadRun(ctx, tenant, "run-late"); !errors.Is(lerr, store.ErrNotFound) {
		var re *runErr
		if !errors.As(lerr, &re) || re.status != 404 {
			t.Errorf("the governed effect survived the refusal: loadRun = %v", lerr)
		}
	}
}

// A CONCURRENT takeover, started from inside the governed transaction and racing
// it rather than being sequenced before it.
//
// Read the name carefully, because the first one overclaimed and a contrast said
// so: this does NOT prove the "takeover still in flight when the CAS runs"
// interleaving. It has no barrier holding the takeover open between its update and
// its commit, so it can and often does collapse into the already-committed case the
// test above covers. What it does prove is that the refusal survives when the
// takeover runs concurrently instead of being neatly ordered first. Pinning the
// in-flight branch needs a second barrier inside the takeover's own transaction —
// pack SG-02-f.
func TestF3_ConcurrentTakeoverStillRefusesTheLateWrite(t *testing.T) {
	t.Parallel()

	m, tenant, clk := pgSess(t)
	ctx := context.Background()

	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: "run-inflight", Origin: OriginOperated, At: baseTime,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	lease, err := m.Claim(ctx, tenant, sid, "user:first", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	real := m.data
	takeoverDone := make(chan error, 1)
	bar := &barrierData{inner: real, kind: claimKind}
	bar.afterRead = func() {
		// Start the takeover and let it run to completion on its own connection. The
		// governed CAS below then contends with a row this transaction has already
		// touched; whether it blocks first or reads the new version first, the answer
		// must be the same.
		clk.advance(2 * time.Minute)
		go func() {
			_, terr := (&Module{data: real, clock: clk}).Claim(ctx, tenant, sid, "user:second", time.Minute)
			takeoverDone <- terr
		}()
		time.Sleep(150 * time.Millisecond) // let the takeover reach its own write
	}
	m.data = bar

	_, err = m.persistCreate(ctx, tenant, "run-inflight", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if terr := <-takeoverDone; terr != nil {
		t.Fatalf("the takeover failed, so this test proved nothing: %v", terr)
	}
	if err == nil {
		t.Fatal("the late write LANDED with a takeover running concurrently (which may have " +
			"committed first — see this test's contract above)")
	}
	if _, lerr := m.loadRun(ctx, tenant, "run-inflight"); lerr == nil {
		t.Error("the governed effect survived the refusal")
	}
}

// F5 through the fenced write. The first observer of a lapse may well BE a governed
// write, and the retirement it records must survive the refusal that follows it.
//
// The shape has been wrong twice and the comment is worth keeping accurate, because
// both wrong shapes leave the row `active` and are indistinguishable afterwards.
// Retiring inside and then RETURNING the denial rolls the retirement back with
// everything else. Retiring in a follow-up transaction leaves a gap. What it does now
// is neither: the early check runs before the governed effect, so the retirement is
// the only thing in the transaction, the transaction COMMITS, and the refusal is
// delivered after the commit. This asserts the durable state, not the error.
func TestF5_AFencedWriteThatObservesALapseRetiresItDurably(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-lapse", "user:first")
	clk.advance(2 * time.Minute) // the lease runs out; nobody notices yet

	_, err := m.persistCreate(ctx, tenant, "run-lapse", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	if err == nil {
		t.Fatal("a write under a lapsed lease was allowed")
	}

	// The observation is DURABLE: the row says expired, so a clock rollback cannot
	// make the old holder live again.
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q after a fenced write observed the lapse, want %q", got, claimExpired)
	}
	clk.advance(-90 * time.Second) // back INSIDE the old lease window
	if err := m.Authority(ctx, tenant, sid, "user:first", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a rolled-back clock resurrected authority: %v", err)
	}
}

// The same guarantee on the heartbeat path, which had the identical defect in main:
// it retired a lapsed lease and then returned the denial from the same callback, so
// the retirement was rolled back. Found by the contrast, fixed here, pinned now.
func TestF5_AHeartbeatThatObservesALapseRetiresItDurably(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-hb", "user:first")
	clk.advance(2 * time.Minute)

	if _, err := m.Heartbeat(ctx, tenant, sid, "user:first", lease.Fence, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a heartbeat on a lapsed lease = %v, want ErrLeaseLost", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q after the heartbeat observed the lapse, want %q — the retirement was rolled back", got, claimExpired)
	}
}

// ---------------------------------------------------------------------------
// R3-01 — the observation and its record are ONE transaction, and where they
// cannot be, the record is of the observation and never a re-judgement of it.
// ---------------------------------------------------------------------------

// The gap R3-01 named, on the path that still has one: the late re-check's
// transaction is dirty with the governed effect, so it must roll back and the record
// travels to a follow-up. What must NOT travel with it is the DECISION.
//
// The clock moves backwards inside that gap here — no crash, no race, just a clock
// that went back the way F5 exists to survive. The old follow-up re-read `now` and
// re-asked "has this lapsed?", so it answered no and left `active` a lease the
// transaction before it had already watched die. The follow-up now records the
// observation it was handed, gated on the generation it was made against — the row
// still being `active` at the same fence — and on no clock at all, so a clock cannot
// talk it out of it.
func TestF5_TheLateRecheckSurvivesAClockRollbackInTheFollowUpGap(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-gap-rollback", "user:first")

	real := m.data
	bar := &barrierData{inner: real, kind: claimKind}
	// The lease is live when the fence check passes and runs out while the governed
	// body is in flight, so the LATE re-check is the first observer.
	bar.afterRead = func() { clk.advance(2 * time.Minute) }
	// ...and the clock goes back the instant that transaction ends, before the record
	// of the observation is written. This is the gap, reproduced.
	bar.afterMutate = func() { clk.advance(-2 * time.Minute) }
	m.data = bar
	_, err := m.persistCreate(ctx, tenant, "run-gap-rollback", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if !errIsStatus(err, 403) {
		t.Fatalf("a write whose lease expired mid-flight = %v, want a 403 refusal", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q, want %q — a clock that moved backwards in the gap talked the "+
			"follow-up out of recording a lapse that had already been observed (R3-01)", got, claimExpired)
	}
	// And the point of recording it: the old holder cannot ride the rolled-back clock.
	if err := m.Authority(ctx, tenant, sid, "user:first", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a rolled-back clock resurrected authority: %v", err)
	}
}

// A stale observation must NOT retire a session somebody has since taken over.
//
// This is the other edge of dropping the clock from the follow-up. Without a guard,
// "record what was observed" would let a lapse seen on one generation expire the
// live claim of the holder that legitimately took over in the gap. The guard is the
// FENCE — a takeover mints a new one — and this is the test that keeps the guard
// from being deleted as redundant.
func TestF5_AnObservationDoesNotRetireTheSuccessorThatTookOverInTheGap(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-gap-takeover", "user:first")

	real := m.data
	bar := &barrierData{inner: real, kind: claimKind}
	bar.afterRead = func() { clk.advance(2 * time.Minute) }
	// In the gap, somebody takes the session over legitimately. The observation the
	// rolled-back transaction is carrying is now about a generation that is gone.
	bar.afterMutate = func() {
		if _, terr := (&Module{data: real, clock: clk}).Claim(ctx, tenant, sid, "user:second", time.Minute); terr != nil {
			t.Errorf("the takeover could not commit: %v", terr)
		}
	}
	m.data = bar
	_, err := m.persistCreate(ctx, tenant, "run-gap-takeover", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if err == nil {
		t.Fatal("the write under a lease that expired mid-flight was allowed")
	}
	got, live, aerr := m.ActiveClaim(ctx, tenant, sid)
	if aerr != nil {
		t.Fatalf("ActiveClaim: %v", aerr)
	}
	if !live {
		t.Fatalf("the successor's claim was retired by an observation about the generation before it")
	}
	if got.Holder != "user:second" || got.Fence <= lease.Fence {
		t.Errorf("live claim = (%q,%d), want user:second at a fence above %d", got.Holder, got.Fence, lease.Fence)
	}
}

// The EARLY check needs no gap at all, and this is what says so.
//
// The process is given exactly ONE write transaction and then stops executing, the
// way a killed process does. The fence check is the first thing in that transaction
// and the governed effect has not run, so the retirement it owes is written there and
// the transaction commits it; the refusal is handed back afterwards. A shape that
// still needed a follow-up would find no transaction left to write in, and would
// leave the row `active` — which is the probe the third contrast ran red.
func TestF5_AFencedWriteRetiresWithinItsOwnTransaction(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-nogap", "user:first")
	clk.advance(2 * time.Minute) // the lease runs out; this write is the first observer

	real := m.data
	m.data = &deadAfterData{inner: real, budget: 1}
	_, err := m.persistCreate(ctx, tenant, "run-nogap", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if !errIsStatus(err, 403) {
		t.Fatalf("a write under a lapsed lease = %v, want a 403 refusal", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q with no second transaction available, want %q — the observation "+
			"needed a follow-up, and a process that dies in that gap is R3-01", got, claimExpired)
	}
	// The retirement committed; the governed effect did NOT ride along with it.
	if _, lerr := m.loadRun(ctx, tenant, "run-nogap"); lerr == nil {
		t.Error("the governed effect committed alongside the retirement")
	}
}

// The same, on the heartbeat. A heartbeat that finds itself dead writes nothing
// else, so there is no reason for its retirement to wait for a second transaction.
func TestF5_AHeartbeatRetiresWithinItsOwnTransaction(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-nogap-hb", "user:first")
	clk.advance(2 * time.Minute)

	real := m.data
	m.data = &deadAfterData{inner: real, budget: 1}
	_, err := m.Heartbeat(ctx, tenant, sid, "user:first", lease.Fence, time.Minute)
	m.data = real

	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a heartbeat on a lapsed lease = %v, want ErrLeaseLost", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q with no second transaction available, want %q (R3-01)", got, claimExpired)
	}
}

// The same, on the exhausted-fence refusal — the entry the second contrast opened F5
// through, and the one the third contrast showed was still only half closed.
func TestF5_AnExhaustedFenceRetiresWithinItsOwnTransaction(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, _ := claimed(t, m, tenant, "run-nogap-fence", "user:first")
	seedFenceCeiling(t, m, tenant, sid)
	clk.advance(2 * time.Minute)

	real := m.data
	m.data = &deadAfterData{inner: real, budget: 1}
	_, err := m.Claim(ctx, tenant, sid, "user:second", time.Minute)
	m.data = real

	if err == nil {
		t.Fatal("a takeover from an exhausted fence was allowed; the token would go backwards")
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q with no second transaction available, want %q (R3-01)", got, claimExpired)
	}
}

// The read path cannot record anything — a View writes nothing — so what it owes is
// narrower and this states it exactly: a DENIAL is never delivered on the strength of
// an observation the store does not hold. Deny the process every write transaction
// and Authority must fail loudly rather than return the tidy refusal it cannot back.
func TestF5_TheReadPathNeverAnswersAnObservationItCouldNotRecord(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-read-nogap", "user:first")
	clk.advance(2 * time.Minute)

	real := m.data
	m.data = &deadAfterData{inner: real, budget: 0} // no write transaction at all
	err := m.Authority(ctx, tenant, sid, "user:first", lease.Fence)
	m.data = real

	if !errors.Is(err, errProcessGone) {
		t.Fatalf("Authority = %v, want the store failure: a refusal returned here would be a "+
			"denial delivered on an observation nothing recorded", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimActive {
		t.Fatalf("claim_state = %q, want %q: nothing could have been written", got, claimActive)
	}
}

// ---------------------------------------------------------------------------
// R4 — what the fourth contrast found in the shape above.
// ---------------------------------------------------------------------------

// R4-01. The follow-up of a READ must RECORD what the read saw, never re-decide it.
//
// This one was worse than the defect it replaced. Re-deciding meant taking a second
// clock reading, and with the clock moved backwards in the gap the second decision
// saw an `active` row as live and returned nil — Authority GRANTING the very fence
// the read had just watched die. Not a lost marker: a granted one.
func TestF5_AuthorityDoesNotReDecideAfterItsReadObservedTheLapse(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-auth-regap", "user:first")
	clk.advance(2 * time.Minute) // the lease runs out; this read is the first observer

	real := m.data
	bar := &barrierData{inner: real, kind: claimKind}
	// The clock goes back the instant the read has taken its own `now` and read the
	// row — i.e. inside the gap between the observation and its record.
	bar.afterRead = func() { clk.advance(-2 * time.Minute) }
	m.data = bar
	err := m.Authority(ctx, tenant, sid, "user:first", lease.Fence)
	m.data = real

	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Authority = %v, want ErrLeaseLost: a clock that moved backwards in the gap "+
			"talked the follow-up into granting a fence the read had already seen dead", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q, want %q", got, claimExpired)
	}
}

// R4-02, first half. ActiveClaim answering live=false on a row still marked `active`
// IS an observation of a lapse, and it used to drop it on the floor.
func TestF5_ActiveClaimRecordsTheLapseItObserves(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-active-observer", "user:first")
	clk.advance(2 * time.Minute)

	if _, live, err := m.ActiveClaim(ctx, tenant, sid); err != nil || live {
		t.Fatalf("ActiveClaim = live:%v err:%v, want dead", live, err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q after ActiveClaim observed the lapse, want %q", got, claimExpired)
	}
	// The point of recording it.
	clk.advance(-90 * time.Second)
	if _, err := m.Heartbeat(ctx, tenant, sid, "user:first", lease.Fence, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a rolled-back clock let the old holder renew after ActiveClaim saw it die: %v", err)
	}
}

// R4-02, second half. leaseOf is the recovery read after a lost race, and it refused
// with ErrLeaseLost while leaving the row `active` for the same rollback to revive.
func TestF5_LeaseOfRecordsTheLapseItObserves(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-leaseof-observer", "user:first")
	clk.advance(2 * time.Minute)

	if _, err := m.leaseOf(ctx, tenant, sid, "user:first"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("leaseOf on a lapsed claim = %v, want ErrLeaseLost", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q after leaseOf observed the lapse, want %q", got, claimExpired)
	}
	clk.advance(-90 * time.Second)
	if err := m.Authority(ctx, tenant, sid, "user:first", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a rolled-back clock resurrected authority leaseOf had seen die: %v", err)
	}
}

// R4-03. A refusal decided inside a callback is worth nothing until the transaction
// carrying its retirement COMMITS — so a transaction that fails must be what the
// caller hears about, on every one of the four paths that hold a verdict back.
//
// Without this, each of them answers its tidy business error while the row is still
// `active`: a denial presented as backed by durable state that does not exist.
func TestF5_ARefusalIsNotDeliveredWhenItsTransactionDidNotCommit(t *testing.T) {
	t.Parallel()

	failFirst := func(call int) (error, bool) {
		if call == 1 {
			return errCommitFailed, false // the callback runs, the commit fails
		}
		return nil, false
	}

	t.Run("claim, exhausted fence", func(t *testing.T) {
		m, _, tenant, clk := newSess(t)
		ctx := context.Background()
		sid, _ := claimed(t, m, tenant, "run-nc-fence", "user:first")
		seedFenceCeiling(t, m, tenant, sid)
		clk.advance(2 * time.Minute)

		real := m.data
		m.data = &mutateFaults{inner: real, fault: failFirst}
		_, err := m.Claim(ctx, tenant, sid, "user:second", time.Minute)
		m.data = real
		assertNotCommitted(t, m, tenant, sid, err)
	})

	t.Run("heartbeat", func(t *testing.T) {
		m, _, tenant, clk := newSess(t)
		ctx := context.Background()
		sid, lease := claimed(t, m, tenant, "run-nc-hb", "user:first")
		clk.advance(2 * time.Minute)

		real := m.data
		m.data = &mutateFaults{inner: real, fault: failFirst}
		_, err := m.Heartbeat(ctx, tenant, sid, "user:first", lease.Fence, time.Minute)
		m.data = real
		assertNotCommitted(t, m, tenant, sid, err)
	})

	t.Run("governed write", func(t *testing.T) {
		m, _, tenant, clk := newSess(t)
		ctx := context.Background()
		sid, lease := claimed(t, m, tenant, "run-nc-write", "user:first")
		clk.advance(2 * time.Minute)

		real := m.data
		m.data = &mutateFaults{inner: real, fault: failFirst}
		_, err := m.persistCreate(ctx, tenant, "run-nc-write", CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
			Actor: "user:first", ActorKind: "user",
		}, "", "", runGovFacts{}, lease)
		m.data = real
		assertNotCommitted(t, m, tenant, sid, err)
	})

	t.Run("transition", func(t *testing.T) {
		m, _, tenant, clk := newSess(t)
		ctx := context.Background()
		sid, lease := claimed(t, m, tenant, "run-nc-tr", "user:first")
		if _, err := m.persistCreate(ctx, tenant, "run-nc-tr", CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
			Actor: "user:first", ActorKind: "user",
		}, "", "", runGovFacts{}, lease); err != nil {
			t.Fatalf("persistCreate: %v", err)
		}
		clk.advance(2 * time.Minute)

		real := m.data
		m.data = &mutateFaults{inner: real, fault: failFirst}
		_, err := m.transition(ctx, tenant, "run-nc-tr", transitionInput{
			event: "launched", toState: stateRunning, lease: lease,
		})
		m.data = real
		assertNotCommitted(t, m, tenant, sid, err)
	})
}

// assertNotCommitted is the shared verdict of the four subtests above: the caller
// hears the transaction failure, and the row is exactly where the rollback left it.
func assertNotCommitted(t *testing.T, m *Module, tenant model.TenantID, sid string, err error) {
	t.Helper()
	if !errors.Is(err, errCommitFailed) {
		t.Errorf("err = %v, want the transaction failure: a business refusal here would be "+
			"presented as backed by a retirement that never committed", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimActive {
		t.Errorf("claim_state = %q, want %q: the transaction rolled back", got, claimActive)
	}
}

// R4-03, the retry. What this actually pins is the PRECEDENCE: the first attempt
// decides a refusal, its commit conflicts, and the retry never reaches the callback —
// so the only honest answer is the second attempt's failure.
//
// Named for what it kills, after the fifth contrast pointed out the first name
// promised more. It does NOT discriminate resetting the captures per invocation:
// that mutant survives (declared at the reset itself). It kills the mutant that
// hands back a business verdict without looking at what Mutate returned.
func TestF5_ARetryThatNeverRanDoesNotAnswerWithTheFirstAttemptsVerdict(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-stale-refusal", "user:first")
	clk.advance(2 * time.Minute)

	real := m.data
	m.data = &mutateFaults{inner: real, fault: func(call int) (error, bool) {
		switch call {
		case 1:
			return store.ErrConflict, false // the callback decides, then the commit conflicts
		case 2:
			return errNeverOpened, true // the retry never reaches the callback
		}
		return nil, false
	}}
	_, err := m.persistCreate(ctx, tenant, "run-stale-refusal", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if !errors.Is(err, errNeverOpened) {
		t.Fatalf("err = %v, want the second attempt's failure: the first attempt's refusal "+
			"survived a retry that never ran and answered in its place", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimActive {
		t.Errorf("claim_state = %q, want %q: neither transaction committed", got, claimActive)
	}
}

// R4-03, the follow-up. A late observation whose record FAILS is not the crash window
// stillLive declares — that one is a process that never ran the follow-up. This one
// ran it and it did not commit, so answering the plain 403 would present a retirement
// that is not there.
func TestF5_ALateObservationWhoseRecordFailsIsNotAnsweredAsARefusal(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-followup-fails", "user:first")

	real := m.data
	bar := &barrierData{inner: real, kind: claimKind}
	bar.afterRead = func() { clk.advance(2 * time.Minute) } // lapses mid-flight
	m.data = &mutateFaults{inner: bar, fault: func(call int) (error, bool) {
		if call == 2 { // 1 is the governed transaction, 2 is the record of the observation
			return errCommitFailed, true
		}
		return nil, false
	}}
	_, err := m.persistCreate(ctx, tenant, "run-followup-fails", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if errIsStatus(err, 403) {
		t.Fatalf("err = %v: a 403 says authority moved AND the store knows it, and the second "+
			"half of that is false when the record failed", err)
	}
	// The CAUSE survives. Inventing a status here buried the one that matters most in
	// this window, so the answer carries the store's own error and the mapper decides.
	if !errors.Is(err, errCommitFailed) {
		t.Fatalf("err = %v, want the durability failure with its cause intact", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimActive {
		t.Errorf("claim_state = %q, want %q: the record is what failed", got, claimActive)
	}
}

// The same on the OTHER branch. transition has its own follow-up, and the test above
// only ever enters through authorizedMutate — a mutant that swallowed transition's
// failure alone would have survived it (P2-01, fifth contrast).
func TestF5_ALateObservationInATransitionWhoseRecordFailsIsNotAnsweredAsARefusal(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-tr-followup", "user:first")
	if _, err := m.persistCreate(ctx, tenant, "run-tr-followup", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease); err != nil {
		t.Fatalf("persistCreate: %v", err)
	}

	real := m.data
	bar := &barrierData{inner: real, kind: claimKind}
	bar.afterRead = func() { clk.advance(2 * time.Minute) } // lapses mid-flight
	m.data = &mutateFaults{inner: bar, fault: func(call int) (error, bool) {
		if call == 2 { // 1 is the transition, 2 is the record of the observation
			return errCommitFailed, true
		}
		return nil, false
	}}
	_, err := m.transition(ctx, tenant, "run-tr-followup", transitionInput{
		event: "launched", toState: stateRunning, lease: lease,
	})
	m.data = real

	if errIsStatus(err, 403) {
		t.Fatalf("err = %v: the refusal was answered as if its record had landed", err)
	}
	if !errors.Is(err, errCommitFailed) {
		t.Fatalf("err = %v, want the durability failure with its cause intact", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimActive {
		t.Errorf("claim_state = %q, want %q", got, claimActive)
	}
}

// P1-01. A leadership loss in that window is RETRYABLE, and it used to be flattened
// into a fixed 500 that nobody retries — with the backend's own text copied into the
// body. The cause has to survive so the store mapper can publish it as the 503 the
// contract promises.
func TestF5_ARecordThatFailedOverIsStillReportedAsRetryable(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	_, lease := claimed(t, m, tenant, "run-notleader", "user:first")

	real := m.data
	bar := &barrierData{inner: real, kind: claimKind}
	bar.afterRead = func() { clk.advance(2 * time.Minute) }
	m.data = &mutateFaults{inner: bar, fault: func(call int) (error, bool) {
		if call == 2 {
			return store.ErrNotLeader, true // the node stopped being the writer in the gap
		}
		return nil, false
	}}
	_, err := m.persistCreate(ctx, tenant, "run-notleader", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("err = %v, want store.ErrNotLeader to survive: a caller that cannot see it "+
			"will not retry against the current leader", err)
	}
	// And the HTTP mapping this module owes it, which it did not have at all.
	rec := httptest.NewRecorder()
	writeRunErr(rec, err)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 for a retryable leadership loss", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "sessions: the lease lapsed") {
		t.Errorf("the response body carries internal error text: %s", body)
	}
}

// P1-N1. The classification has to survive the LAUNCH path too, and it did not: the
// admission preamble replaced whatever the store said with a flat 403 carrying the
// cause's text, so writeRunErr answered that and the mapper never saw it. A failover
// during a launch was published as a policy decision nobody retries.
func TestAdmission_AStoreFailureDuringALaunchIsNotPublishedAsAPolicyDenial(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))

	real := m.data
	// The node stops being the writer the moment the preamble tries to acquire.
	m.data = &mutateFaults{inner: real, fault: func(int) (error, bool) {
		return store.ErrNotLeader, true
	}}
	_, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	m.data = real

	if err == nil {
		t.Fatal("a launch whose admission plane could not write was ALLOWED")
	}
	if errIsStatus(err, 403) {
		t.Fatalf("err = %v: a 403 tells the caller this was a decision and not to retry a "+
			"node that simply is not the leader any more", err)
	}
	if !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("err = %v, want store.ErrNotLeader to survive the preamble", err)
	}
	rec := httptest.NewRecorder()
	writeRunErr(rec, err)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "not the leader") {
		t.Errorf("the response body carries the backend's own message: %s", body)
	}
}

// The two sites the closure had MISSED, which is why P1-N1 was not closed by fixing
// the preamble alone: a launch touches more than one backend, and any of them can be
// down without that being a decision about the launcher.
//
// EXACTLY two seams, both on create, both with store.ErrNotLeader — the credential
// mint and the kill-switch read. Of the rest: the IDENTITY seam is covered above (that
// test fails the first Mutate, which is ResolveSession's, so it never reaches Claim),
// and the launch GATE seam is covered below. The Claim seam itself, the
// persist/transition seams and the whole resume path have NO outage test here.
func TestAdmission_TheMintAndKillSwitchOutagesKeepTheirRetryableStatus(t *testing.T) {
	t.Parallel()

	t.Run("credential mint", func(t *testing.T) {
		ctx := context.Background()
		creds := CredentialSourceFunc(func(context.Context, CredentialRequest) (Credential, error) {
			return Credential{}, store.ErrNotLeader
		})
		m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(creds))

		_, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: "user",
		})
		assertRefusedAsAnOutage(t, err)
	})

	t.Run("kill-switch backend", func(t *testing.T) {
		ctx := context.Background()
		stop := stopGateFunc(func(context.Context, model.TenantID, StopDims) (StopDecision, error) {
			return StopDecision{}, store.ErrNotLeader
		})
		m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}),
			WithCredentialSource(staticCred()), WithStopGate(stop))

		_, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: "user",
		})
		assertRefusedAsAnOutage(t, err)
	})
}

// assertRefusedAsAnOutage is the shared verdict. It asserts a DENIAL — err != nil is
// the first thing it checks — published as an outage rather than as a verdict about
// the launcher, and with nothing of the backend's own message in the body.
func assertRefusedAsAnOutage(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("a launch whose backend was unavailable was ALLOWED")
	}
	if errIsStatus(err, 403) {
		t.Fatalf("err = %v: an outage was published as a decision about this launcher", err)
	}
	if !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("err = %v, want store.ErrNotLeader to survive", err)
	}
	rec := httptest.NewRecorder()
	writeRunErr(rec, err)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "not the leader") {
		t.Errorf("the response body carries the backend's own message: %s", body)
	}
}

// P2-R6-01. A refusal of AUTHORITY is a 403, and its message is this module's own.
// Which of the session, the expiry, the holders or the fences the cause would have
// carried depends on which check refused; a refusal is not the place to tell a caller
// any of them. This case exercises the holder mismatch.
func TestF5_AnAuthorityRefusalDoesNotPublishWhoHoldsTheClaim(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-quiet-refusal", "user:first")
	clk.advance(2 * time.Minute)
	if _, err := m.Claim(ctx, tenant, sid, "user:second", time.Minute); err != nil {
		t.Fatalf("takeover: %v", err)
	}

	_, err := m.persistCreate(ctx, tenant, "run-quiet-refusal", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)

	if !errIsStatus(err, 403) {
		t.Fatalf("err = %v, want a 403 refusal of authority", err)
	}
	for _, leak := range []string{"user:second", "user:first", sid} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the refusal publishes %q to the caller: %v", leak, err)
		}
	}
}

// The SECOND call site of the same rule. transition has its own refusal branch, and
// the test above enters through authorizedMutate — so an isolated regression that put
// the cause back into transition's 403 alone would have survived it (P2-R7-01).
func TestF5_ATransitionRefusalDoesNotPublishWhoHoldsTheClaim(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-quiet-transition", "user:first")
	if _, err := m.persistCreate(ctx, tenant, "run-quiet-transition", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease); err != nil {
		t.Fatalf("persistCreate: %v", err)
	}
	clk.advance(2 * time.Minute)
	if _, err := m.Claim(ctx, tenant, sid, "user:second", time.Minute); err != nil {
		t.Fatalf("takeover: %v", err)
	}

	_, err := m.transition(ctx, tenant, "run-quiet-transition", transitionInput{
		event: "launched", toState: stateRunning, lease: lease,
	})
	if !errIsStatus(err, 403) {
		t.Fatalf("err = %v, want a 403 refusal of authority", err)
	}
	for _, leak := range []string{"user:second", "user:first", sid} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the refusal publishes %q to the caller: %v", leak, err)
		}
	}
}

// The same rule at the OTHER site. A gate that ERRORED is not a gate that decided,
// and both used to answer the same 403 — so a store failure surfacing through the
// admission decorator was published as a policy denial too.
func TestAdmission_AGateErrorIsNotPublishedAsAPolicyDenial(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
		// What ClaimAdmission does when the claim is unreadable: refuse, and hand up
		// the store's own error.
		return LaunchDecision{Allowed: false, Reason: "admission: claim unreadable"}, store.ErrNotLeader
	})
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}),
		WithCredentialSource(staticCred()), WithLaunchGate(gate))

	_, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatal("a launch whose gate could not decide was ALLOWED")
	}
	if errIsStatus(err, 403) {
		t.Fatalf("err = %v: the gate did not decide, it broke", err)
	}
	if !errors.Is(err, store.ErrNotLeader) {
		t.Fatalf("err = %v, want store.ErrNotLeader to survive the gate seam", err)
	}
	rec := httptest.NewRecorder()
	writeRunErr(rec, err)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// A gate that DECIDED no keeps its 403, which is the line the fix must not cross.
func TestAdmission_AGateDenialKeepsItsForbidden(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: false, Reason: "budget exhausted"}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}),
		WithCredentialSource(staticCred()), WithLaunchGate(gate))

	_, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if !errIsStatus(err, 403) {
		t.Fatalf("err = %v, want a 403: a policy decision is not an outage", err)
	}
}

// The other half of the same rule: a BUSINESS verdict must keep its status. Deny
// stays deny — this is not a license to turn refusals into 500s.
func TestAdmission_ABusinessRefusalKeepsItsStatusAndLeaksNoCause(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-held"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(&spyGate{inner: LaunchDecision{Allowed: true}}))

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "claude session id captured", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-held"
	})
	sid := sidOfRun(t, m, tenant, dto.RunRef)
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.Claim(ctx, tenant, sid, "user:squatter", time.Minute); err != nil {
		t.Fatalf("the squatter could not claim: %v", err)
	}

	_, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u2", "user", "")
	if !errIsStatus(err, 409) {
		t.Fatalf("err = %v, want a 409: another live holder is a decision, not an outage", err)
	}
	if strings.Contains(err.Error(), "user:squatter") {
		t.Errorf("the refusal names the holder of the claim to the caller: %v", err)
	}
}

// P2-N1. The module's mapper had drifted from the contract core/api publishes, and
// two of those states are reachable causes of the follow-up this pass added.
func TestWriteStoreError_MatchesTheContractStatuses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"not found", store.ErrNotFound, http.StatusNotFound},
		{"unknown entity", store.ErrUnknownEntity, http.StatusNotFound},
		{"conflict", store.ErrConflict, http.StatusConflict},
		{"residency", store.ErrResidencyViolation, http.StatusForbidden},
		{"not leader", store.ErrNotLeader, http.StatusServiceUnavailable},
		{"audit spool full", store.ErrAuditSpoolFull, http.StatusServiceUnavailable},
		{"cursor with sort", store.ErrCursorWithSort, http.StatusBadRequest},
		{"opaque", errors.New("something broke"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeStoreError(rec, tc.err)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (core/api/errors.go is the contract)", rec.Code, tc.want)
			}
		})
	}
}

// The two other readers propagate a failed record too. Authority had this covered and
// they did not, so a mutant that dropped the check in either would have survived
// (P2-01).
func TestF5_TheOtherReadersAlsoRefuseToAnswerAnUnrecordedObservation(t *testing.T) {
	t.Parallel()

	t.Run("ActiveClaim", func(t *testing.T) {
		m, _, tenant, clk := newSess(t)
		ctx := context.Background()
		sid, _ := claimed(t, m, tenant, "run-ac-nogap", "user:first")
		clk.advance(2 * time.Minute)

		real := m.data
		m.data = &deadAfterData{inner: real, budget: 0}
		_, live, err := m.ActiveClaim(ctx, tenant, sid)
		m.data = real

		if !errors.Is(err, errProcessGone) {
			t.Fatalf("ActiveClaim = %v, want the store failure", err)
		}
		if live {
			t.Error("ActiveClaim answered about liveness on an observation nothing recorded")
		}
	})

	t.Run("leaseOf", func(t *testing.T) {
		m, _, tenant, clk := newSess(t)
		ctx := context.Background()
		sid, _ := claimed(t, m, tenant, "run-lo-nogap", "user:first")
		clk.advance(2 * time.Minute)

		real := m.data
		m.data = &deadAfterData{inner: real, budget: 0}
		_, err := m.leaseOf(ctx, tenant, sid, "user:first")
		m.data = real

		if !errors.Is(err, errProcessGone) {
			t.Fatalf("leaseOf = %v, want the store failure", err)
		}
	})
}

// seedFenceCeiling drives the stored fence to the ceiling F9 refuses to wrap past.
func seedFenceCeiling(t *testing.T, m *Module, tenant model.TenantID, sid string) {
	t.Helper()
	ctx := context.Background()
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		rec, _, err := findClaim(ctx, sc, sid)
		if err != nil {
			return err
		}
		rec[colFence] = int64(math.MaxInt64)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("seed the ceiling: %v", err)
	}
}

// claimStateOf reads the durable claim state.
func claimStateOf(t *testing.T, m *Module, tenant model.TenantID, sid string) string {
	t.Helper()
	var out string
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		rec, found, err := findClaim(context.Background(), sc, sid)
		if err != nil || !found {
			return err
		}
		out = rec.String(colClaimState)
		return nil
	}); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	return out
}

// F6 with a REAL clock — the proof could not construct and recorded as owed.
//
// F6 is that `now` used to be sampled BEFORE the transaction opened. Corrected
// it in five places and then found it could not write a discriminating test:
// with an injected clock, the instant before the Mutate and the instant inside it
// are the same value, so every candidate assertion stayed green with the defect
// restored. It shipped as "verified by construction" and handed the proof to this
// pack.
//
// The missing ingredient was not a better assertion, it was a real latency window.
// This test uses the SYSTEM clock and a barrier that holds the transaction open
// after it is created and before the production closure runs — which is exactly
// what a contended write lock does. With the clock read INSIDE the transaction the
// lease has expired by the time it is consulted and the heartbeat is refused; with
// the read hoisted above m.data.Mutate it captures a pre-barrier instant and renews
// a lease that is already dead.
func TestF6_TheClockIsReadInsideTheTransaction(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	m.clock = model.SystemClock{} // no injected time: the window has to be real
	ctx := context.Background()

	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: "run-f6", Origin: OriginOperated,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	const ttl = 120 * time.Millisecond
	lease, err := m.Claim(ctx, tenant, sid, "user:first", ttl)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The heartbeat is issued while the lease is still live, and then waits — the way
	// a real one waits behind another writer.
	real := m.data
	m.data = &barrierData{
		inner: real, kind: claimKind,
		beforeCallback: func() { time.Sleep(2 * ttl) },
	}
	_, err = m.Heartbeat(ctx, tenant, sid, "user:first", lease.Fence, ttl)
	m.data = real

	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a heartbeat that entered its transaction AFTER its lease expired = %v, want ErrLeaseLost: "+
			"the clock was read before the transaction, so a stale instant renewed a dead lease", err)
	}
	// And the lapse it observed is durable, as F5 requires of every first observer.
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Errorf("claim_state = %q, want %q", got, claimExpired)
	}
}

// F5 when the FIRST observer of the lapse is the late re-check, not the early one.
//
// The second contrast reopened F5 here: stillLive returned ErrLeaseLost without
// saying it had just seen an `active` row run out, so the transaction rolled back
// and nobody retired it — leaving the row alive for a clock that later moves
// backwards. The observation has to survive the rollback that follows it.
func TestF5_ALapseObservedByTheLateRecheckIsStillRetired(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, lease := claimed(t, m, tenant, "run-late-lapse", "user:first")

	// The lease is LIVE when the transaction opens and the fence check passes; it runs
	// out while the governed body is in flight. The barrier is the body's own clock
	// advance, which is what a slow effect looks like from the claim's point of view.
	real := m.data
	bar := &barrierData{inner: real, kind: claimKind}
	bar.afterRead = func() { clk.advance(2 * time.Minute) }
	m.data = bar
	_, err := m.persistCreate(ctx, tenant, "run-late-lapse", CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, PermissionMode: "default",
		Actor: "user:first", ActorKind: "user",
	}, "", "", runGovFacts{}, lease)
	m.data = real

	if !errIsStatus(err, 403) {
		t.Fatalf("a write whose lease expired mid-flight = %v, want a 403 refusal", err)
	}
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q, want %q — the late observation was rolled back with the write", got, claimExpired)
	}
	clk.advance(-90 * time.Second)
	if err := m.Authority(ctx, tenant, sid, "user:first", lease.Fence); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a rolled-back clock resurrected authority: %v", err)
	}
}

// F5 on the exhausted-fence path. A takeover that cannot mint a higher fence must
// refuse — and a refusal RETURNED from the callback rolls the transaction back, so
// the refusal is not returned from there at all: the retirement is written, the
// transaction commits it, and the caller is refused afterwards.
func TestF5_AnExhaustedFenceRefusesWithoutLosingTheRetirement(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()

	sid, _ := claimed(t, m, tenant, "run-exhausted", "user:first")
	seedFenceCeiling(t, m, tenant, sid)
	clk.advance(2 * time.Minute) // the lease lapses; a takeover would be due

	if _, err := m.Claim(ctx, tenant, sid, "user:second", time.Minute); err == nil {
		t.Fatal("a takeover from an exhausted fence was allowed; the token would go backwards")
	}
	// The refusal OBSERVED a lapse, and an observation nobody records is what a
	// clock rollback exploits. Reordering alone is not enough — writing it inside a
	// transaction that then rolls back, and not writing it at all, are
	// indistinguishable afterwards, since both leave the row active.
	if got := claimStateOf(t, m, tenant, sid); got != claimExpired {
		t.Fatalf("claim_state = %q after the refusal observed the lapse, want %q", got, claimExpired)
	}
	clk.advance(-90 * time.Second) // back inside the old lease window
	if err := m.Authority(ctx, tenant, sid, "user:first", math.MaxInt64); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a rolled-back clock resurrected authority the refusal had seen die: %v", err)
	}
}
