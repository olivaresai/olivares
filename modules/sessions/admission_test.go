// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SG-02-b F1 — the launch seam carries WHO is launching and under WHICH authority.
//
// The finding these pin: the gate used to be consulted with an empty run reference
// on a create (launchIntentFor(…, "", …) with the ref minted five lines later), so
// an admission decorator could not be composed at all without refusing every launch
// in the estate.
//
// The mutants below are the ones the adversarial contrast CORRECTED. Two of the
// first cut's mutants were measured to leave the test green: asserting a merely
// non-zero fence passes with a hardcoded 1, because a fresh claim is always fence 1.
// So these assert the EXACT fence, and the resume case forces a takeover first so
// the expected value cannot be 1 by accident.

// spyGate captures the intent it was asked about and answers yes.
type spyGate struct {
	seen  []LaunchIntent
	inner LaunchDecision
}

func (g *spyGate) Authorize(_ context.Context, _ model.TenantID, i LaunchIntent) (LaunchDecision, error) {
	g.seen = append(g.seen, i)
	dec := g.inner
	if dec.Reason == "" && !dec.Allowed {
		dec = LaunchDecision{Allowed: true}
	}
	return dec, nil
}

func (g *spyGate) last(t *testing.T) LaunchIntent {
	t.Helper()
	if len(g.seen) == 0 {
		t.Fatal("the launch gate was never consulted")
	}
	return g.seen[len(g.seen)-1]
}

// A create reaches the gate with a real reference, a holder and the fence the store
// minted — the three things F1 said it could not carry.
func TestLaunchIntent_CreateCarriesTheAcquiredClaim(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	got := gate.last(t)

	// F1 itself: the reference is no longer empty when the gate is asked.
	if got.RunRef == "" {
		t.Error("the gate was consulted with an EMPTY run reference — F1 is not fixed")
	}
	if got.RunRef != dto.RunRef {
		t.Errorf("gate saw run ref %q, the run is %q", got.RunRef, dto.RunRef)
	}
	if got.Holder != "user:u1" {
		t.Errorf("holder = %q, want the authenticated principal", got.Holder)
	}
	if got.ClaimSID == "" {
		t.Error("the intent carries no canonical session for the claim")
	}
	// The EXACT fence, read back from the store. "non-zero" would pass with a
	// hardcoded 1 on a fresh claim, which is why the contrast rejected that assertion.
	lease, live, err := m.ActiveClaim(ctx, tenant, got.ClaimSID)
	if err != nil || !live {
		t.Fatalf("the launch left no live claim: live=%v err=%v", live, err)
	}
	if got.Fence != lease.Fence {
		t.Errorf("intent fence = %d, the claim's fence is %d", got.Fence, lease.Fence)
	}
	if lease.Holder != "user:u1" {
		t.Errorf("claim holder = %q, want user:u1", lease.Holder)
	}

	// And the run row carries the stamp every later governed write compares against.
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("loadRun: %v", err)
	}
	h, f := claimStampOf(rec)
	if h != "user:u1" || f != lease.Fence {
		t.Errorf("run stamp = (%q,%d), want (user:u1,%d)", h, f, lease.Fence)
	}
}

// A resume carries the fence of the claim it ACQUIRED, and that fence is greater
// than 1 because the session changed hands — so a test that merely checked for
// "non-zero" would not have noticed a hardcoded value.
func TestLaunchIntent_ResumeCarriesTheTakenOverFence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{initSID: "sess-resume-fence"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "claude session id captured", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-resume-fence"
	})
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	createFence := gate.last(t).Fence

	// A DIFFERENT principal resumes. The previous holder's claim was released when its
	// process died, so this is a legitimate takeover — and a takeover MOVES the fence.
	if _, err := m.resumeRun(ctx, tenant, dto.RunRef, "user:u2", "user", ""); err != nil {
		t.Fatalf("resumeRun: %v", err)
	}
	got := gate.last(t)
	if got.Action != LaunchActionResume {
		t.Fatalf("the last gate call was %q, want a resume", got.Action)
	}
	if got.Holder != "user:u2" {
		t.Errorf("holder = %q, want the resuming principal", got.Holder)
	}
	if got.Fence <= createFence {
		t.Errorf("resume fence %d did not advance past the create's %d — a takeover must move it",
			got.Fence, createFence)
	}
	lease, live, err := m.ActiveClaim(ctx, tenant, got.ClaimSID)
	if err != nil || !live {
		t.Fatalf("resume left no live claim: live=%v err=%v", live, err)
	}
	if got.Fence != lease.Fence {
		t.Errorf("intent fence = %d, the claim's fence is %d", got.Fence, lease.Fence)
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("loadRun: %v", err)
	}
	if h, f := claimStampOf(rec); h != "user:u2" || f != lease.Fence {
		t.Errorf("run stamp after resume = (%q,%d), want (user:u2,%d)", h, f, lease.Fence)
	}
}

// A resume while ANOTHER holder's lease is live is refused, and refused before the
// inner gate is reached — an unadmitted launch must not spend budget or open an
// approval on its way to being denied.
func TestAdmission_ResumeRefusedWhileAnotherHolderIsLive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	fr := &fakeRunner{initSID: "sess-contended"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "claude session id captured", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-contended"
	})
	sid := gate.last(t).ClaimSID
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	// Somebody else takes the session while it is stopped.
	if _, err := m.Claim(ctx, tenant, sid, "user:squatter", time.Minute); err != nil {
		t.Fatalf("the squatter could not claim: %v", err)
	}
	calls := len(gate.seen)

	_, err = m.resumeRun(ctx, tenant, dto.RunRef, "user:u2", "user", "")
	if err == nil {
		t.Fatal("a resume of a session held by another live holder was ALLOWED")
	}
	if !errIsStatus(err, 409) {
		t.Errorf("resume refusal = %v, want a 409 conflict", err)
	}
	if len(gate.seen) != calls {
		t.Errorf("the inner gate ran for a launch that was never admissible (%d extra calls)",
			len(gate.seen)-calls)
	}
}

// The launcher that cannot name itself, denied at the HOLDER guard specifically —
// and denied VISIBLY.
//
// This test was vacuous on its first cut and mutation is what caught it, which is
// the whole reason the method is mandatory here. The first version passed an intent
// for a session with no claim at all, so the refusal came from the earlier
// "no live claim" branch and the holder guard was never reached: dropping the
// signal from the holder branch left the test green. The session is therefore
// CLAIMED first, so the only thing left to refuse the launch is the missing holder.
func TestAdmission_UnidentifiedLauncherIsDeniedAndSignalled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, st, tenant, _ := newSess(t)

	// A live claim exists, held by somebody. The launcher below is not it — it is
	// nobody at all.
	sid, _ := claimed(t, m, tenant, "run-anon", "user:holder")

	inner := launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
		t.Error("the inner gate was reached by a launcher that never identified itself")
		return LaunchDecision{Allowed: true}, nil
	})
	gate := NewClaimAdmission(inner, m, ProviderOperated, IntentHolder)

	dec, err := gate.Authorize(ctx, tenant, LaunchIntent{RunRef: "run-anon", ClaimSID: sid})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if dec.Allowed {
		t.Fatal("a launch with no holder was ALLOWED against a live claim")
	}
	// The deny has to be visible. A refusal nobody can see is indistinguishable from
	// a session that never tried, which is the silence this whole plane exists against.
	if rec, found := getLive(t, m, st, tenant, "run-anon"); !found || rec.String(colUnclaimedAt) == "" {
		t.Error("the refusal left no visible signal on the live row")
	}
}

// A launcher that names itself but is not the holder is refused too, and this is
// the guard that carries the weight: the empty-holder check above is belt and
// braces over this comparison, which is why no single mutant kills both.
func TestAdmission_ForeignHolderIsDeniedAndSignalled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, st, tenant, _ := newSess(t)
	sid, lease := claimed(t, m, tenant, "run-foreign", "user:holder")

	inner := launchGateFunc(func(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
		t.Error("the inner gate was reached by a launcher riding somebody else's claim")
		return LaunchDecision{Allowed: true}, nil
	})
	gate := NewClaimAdmission(inner, m, ProviderOperated, IntentHolder)

	// The right fence, the wrong holder.
	dec, err := gate.Authorize(ctx, tenant, LaunchIntent{
		RunRef: "run-foreign", ClaimSID: sid, Holder: "user:intruder", Fence: lease.Fence,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if dec.Allowed {
		t.Fatal("a launcher riding another holder's claim was ALLOWED")
	}
	if rec, found := getLive(t, m, st, tenant, "run-foreign"); !found || rec.String(colUnclaimedAt) == "" {
		t.Error("the refusal left no visible signal on the live row")
	}
}

// The right holder at a STALE fence is refused. Separate from the test above on
// purpose: one mutant removes the holder comparison, the other removes the fence
// comparison, and a single test covering both would survive either.
func TestAdmission_StaleFenceIsDenied(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, _, tenant, _ := newSess(t)
	sid, lease := claimed(t, m, tenant, "run-stale", "user:holder")

	gate := NewClaimAdmission(nil, m, ProviderOperated, IntentHolder)
	dec, err := gate.Authorize(ctx, tenant, LaunchIntent{
		RunRef: "run-stale", ClaimSID: sid, Holder: "user:holder", Fence: lease.Fence - 1,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if dec.Allowed {
		t.Fatal("a stale fence was ALLOWED; the fence must be part of the answer")
	}
}

// IntentHolder is the seam the composition root wires. It must refuse to vouch for
// a launch with no holder — this is the unit whose mutant (dropping the emptiness
// test) makes the test above go green.
func TestIntentHolder_RefusesAnEmptyHolder(t *testing.T) {
	t.Parallel()

	if _, _, ok := IntentHolder(LaunchIntent{Holder: "", Fence: 7}); ok {
		t.Error("IntentHolder vouched for a launch with no holder")
	}
	h, f, ok := IntentHolder(LaunchIntent{Holder: "user:u1", Fence: 7})
	if !ok || h != "user:u1" || f != 7 {
		t.Errorf("IntentHolder = (%q,%d,%v), want (user:u1,7,true)", h, f, ok)
	}
}

// A launch that the gate refuses must not leave the session held by a launcher that
// never ran: the admission preamble gives the claim back.
func TestAdmission_RefusedLaunchGivesTheClaimBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: false, Reason: "budget"}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
		WithLaunchGate(gate))

	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	}); err == nil {
		t.Fatal("a refused launch returned no error")
	}
	sid := gate.last(t).ClaimSID
	if sid == "" {
		t.Fatal("the gate never saw a claim to release")
	}
	if _, live, err := m.ActiveClaim(ctx, tenant, sid); err != nil || live {
		t.Errorf("a refused launch left the session claimed (live=%v err=%v)", live, err)
	}
}

// The kill-switch outranks the admission plane, not just the launch gate. A stopped
// estate must write NOTHING — no identity, no claim — so stop is checked before the
// preamble rather than inside the pre-flight that follows it.
func TestAdmission_StoppedEstateWritesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
		WithLaunchGate(gate), WithStopGate(stopGateFunc(func(context.Context, model.TenantID, StopDims) (StopDecision, error) {
			return StopDecision{Stopped: true, StopRef: "stop-1", Scope: "estate"}, nil
		})))

	if _, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	}); err == nil {
		t.Fatal("a launch under an emergency stop was allowed")
	}
	if len(gate.seen) != 0 {
		t.Error("the launch gate was consulted despite an active emergency stop")
	}
	if n := countRows(t, m, tenant, claimKind); n != 0 {
		t.Errorf("a stopped estate wrote %d claim row(s); it must write none", n)
	}
	if n := countRows(t, m, tenant, identityKind); n != 0 {
		t.Errorf("a stopped estate minted %d identity row(s); it must mint none", n)
	}
}

// ---------------------------------------------------------------------------
// R3-02 — the order inside finalize, DISCRIMINATED.
// ---------------------------------------------------------------------------

// resumeAtTerminal wraps a ModuleData so a resume runs at the exact instant the run
// row becomes terminal — the instant the session becomes resumable, and the start of
// the window the release used to sit in.
//
// The third contrast showed the previous test could not reach that window at all: it
// went through stopRun, which waits on finalizedCh, and that channel closes after
// BOTH the terminal transition and the release. Anything sequenced behind it is, by
// construction, after the whole of finalize. So the barrier cannot be a wait — it has
// to be a hook that fires between the two operations, and it is built by wrapping
// api.ModuleData rather than by bending finalize into a shape production does not
// need. The hook runs AFTER the inner transaction has ended (a nested Mutate would
// wait for a connection the caller already holds — claim.go documents that hang), and
// it is installed before the run exists, so nothing races the assignment.
type resumeAtTerminal struct {
	inner  api.ModuleData
	m      *Module
	tenant model.TenantID
	actor  string

	mu       sync.Mutex
	fired    bool
	lease    Lease
	live     bool
	err      error
	resumeCh chan struct{}
}

func (r *resumeAtTerminal) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return r.inner.View(ctx, tenant, fn)
}

func (r *resumeAtTerminal) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := r.inner.Mutate(ctx, tenant, fn)
	r.maybeFire(ctx)
	return err
}

// maybeFire resumes, once, as soon as a run row has reached a terminal state. It does
// write its own fields (fired, and the captured result) — under the mutex, from the
// bridge goroutine. What the row-based condition buys is that the TEST goroutine never
// has to reassign m.data after the launch, which is the assignment that would race.
func (r *resumeAtTerminal) maybeFire(ctx context.Context) {
	r.mu.Lock()
	if r.fired {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	ref, ok := terminalRunRef(ctx, r.inner, r.tenant)
	if !ok {
		return
	}
	r.mu.Lock()
	if r.fired {
		r.mu.Unlock()
		return
	}
	r.fired = true
	r.mu.Unlock()

	// The resume of the SAME holder, in the window. finalize holds no per-run lock
	// while it runs, so this is the ordinary path and not a contrived one.
	_, err := r.m.resumeRun(ctx, r.tenant, ref, r.actor, "user", "")
	r.mu.Lock()
	r.err = err
	if err == nil {
		var sid string
		if lr, found := r.m.rt.getLive(r.tenant, ref); found {
			sid = lr.claim.SID
		}
		r.lease, r.live, _ = r.m.ActiveClaim(ctx, r.tenant, sid)
	}
	r.mu.Unlock()
	close(r.resumeCh)
}

// terminalRunRef returns the reference of a run row that has reached stopped/failed.
func terminalRunRef(ctx context.Context, data api.ModuleData, tenant model.TenantID) (string, bool) {
	ref, found := "", false
	_ = data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 16})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if s := rec.String(colState); s == stateStopped || s == stateFailed {
				ref, found = rec.String(colRunRef), true
				return nil
			}
		}
		return nil
	})
	return ref, found
}

// THE R3-02 TEST. finalize gives the claim back BEFORE it publishes the terminal
// state, and this is what tells the two orders apart.
//
// The race the order fixes: publishing `stopped` first makes the run resumable
// immediately, a resume by the SAME actor RENEWS the claim without moving the fence
// (a renewal is not a new identity), and the release that follows still matches that
// holder and that fence — so it revokes the successor's authority. The window has no
// lock to close it with: stopRun holds the per-run lock while waiting on this very
// finalize.
//
// Two assertions, and they fail for different reasons on purpose:
//
//   - the successor's claim is a TAKEOVER, not a renewal. With the release first, the
//     row is `released` when the resume arrives and the fence MOVES. With the release
//     last, the row is still `active` at the launch fence and the resume renews it —
//     which is precisely the precondition the late release then exploits. This one is
//     evaluated inside the window, so it discriminates wherever the release is moved to.
//   - the successor's claim is still LIVE once finalize has finished, which is the
//     outcome itself.
func TestFinalize_TheClaimIsGivenBackBeforeTheRunBecomesResumable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-finalize-order"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))

	// Installed BEFORE anything launches: m.data is never reassigned while the bridge
	// goroutine is alive.
	hook := &resumeAtTerminal{inner: m.data, m: m, tenant: tenant, actor: "user:u1", resumeCh: make(chan struct{})}
	m.data = hook

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "claude session id captured", func() bool {
		d, _ := m.getRun(ctx, tenant, dto.RunRef)
		return d.ClaudeSessionID == "sess-finalize-order"
	})
	launch, live, err := m.ActiveClaim(ctx, tenant, sidOfRun(t, m, tenant, dto.RunRef))
	if err != nil || !live {
		t.Fatalf("the launch left no live claim: live=%v err=%v", live, err)
	}

	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("no live handle for the launched run")
	}
	// The process exits on its own. stopRun is deliberately NOT used: it would hold the
	// per-run lock the resume needs, and it waits on the channel that closes after both
	// operations — which is why the previous test could not reach the window.
	fr.lastProc().finish(0)

	<-hook.resumeCh
	<-lr.finalizedCh

	hook.mu.Lock()
	rerr, got, stillLive := hook.err, hook.lease, hook.live
	hook.mu.Unlock()

	if rerr != nil {
		t.Fatalf("the resume in the window failed, so this test proved nothing: %v", rerr)
	}
	if got.Fence <= launch.Fence {
		t.Errorf("the successor's fence is %d against the launch's %d: the claim had not been given "+
			"back when the run became resumable, so the resume RENEWED it — and the release that "+
			"follows matches that holder and that fence", got.Fence, launch.Fence)
	}
	if !stillLive {
		t.Error("the resume that took over in the window got no live claim")
	}
	// The outcome, once finalize has finished everything it does.
	after, aliveNow, err := m.ActiveClaim(ctx, tenant, got.SID)
	if err != nil {
		t.Fatalf("ActiveClaim: %v", err)
	}
	if !aliveNow {
		t.Fatal("finalize revoked the claim of the resume that succeeded it: the release ran after " +
			"the terminal state was published, and it still matched the holder and the fence")
	}
	if after.Fence != got.Fence || after.Holder != "user:u1" {
		t.Errorf("the surviving claim is (%q,%d), want the successor's (user:u1,%d)",
			after.Holder, after.Fence, got.Fence)
	}
}

// sidOfRun reads the canonical session a run was launched against, off its live handle.
func sidOfRun(t *testing.T, m *Module, tenant model.TenantID, runRef string) string {
	t.Helper()
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		t.Fatalf("no live handle for %s", runRef)
	}
	return lr.claim.SID
}

// errIsStatus reports whether err is a runErr carrying status.
func errIsStatus(err error, status int) bool {
	var re *runErr
	return errors.As(err, &re) && re.status == status
}

// stopGateFunc adapts a function to the StopGate seam.
type stopGateFunc func(context.Context, model.TenantID, StopDims) (StopDecision, error)

func (f stopGateFunc) Check(ctx context.Context, t model.TenantID, d StopDims) (StopDecision, error) {
	return f(ctx, t, d)
}
