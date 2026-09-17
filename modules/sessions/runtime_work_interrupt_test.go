// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The fenced TURN INTERRUPTION, at the port, over a driver whose every seam this
// test controls.
//
// ⛔ WHY A FAKE DRIVER AND NOT THE CODEX CHILD. The end-to-end proof is next door
// on a real owned process (runtime_work_interrupt_driver_test.go), and it is the
// one that shows a turn actually being cancelled. What it CANNOT show is the
// uncertainty boundary: to prove that a refusal before any byte is recorded as a
// refusal, and that a failure after a frame may have crossed is recorded as an
// ambiguity, the test has to decide which of those two happened. Only the driver
// knows that in production, so only a driver the test writes can pin it.
//
// The fake never invents a protocol. It answers the DriverSession contract and
// nothing else, and the two facts it reports — attempted, and the error — are
// exactly the two the runtime consumes.

const fakeTurnDriverKey = "faketurn"

type fakeTurnDriver struct{ session *fakeTurnSession }

func (d fakeTurnDriver) Key() string                      { return fakeTurnDriverKey }
func (d fakeTurnDriver) ConfigHomeEnv() string            { return "FAKETURN_HOME" }
func (d fakeTurnDriver) DefaultProgram() string           { return "faketurn" }
func (d fakeTurnDriver) LaunchArgs(DriverLaunch) []string { return []string{"--app-server"} }
func (d fakeTurnDriver) OpenSession(cfg DriverSessionConfig) DriverSession {
	d.session.cfg = cfg
	return d.session
}

// fakeTurnSession is a DriverSession whose Input and Interrupt are programmable.
// Its defaults are the HONEST ones — a turn starts, an interrupt ends it and
// reports that a frame crossed — so a test that overrides nothing is a positive
// control rather than a stub.
type fakeTurnSession struct {
	cfg DriverSessionConfig

	mu             sync.Mutex
	turn           string
	inputs         int
	interrupts     int
	interruptHook  func()
	interruptFrame *bool  // nil ⇒ the default (a frame crossed)
	interruptErr   error  // nil ⇒ the interrupt succeeded
	conversation   string // nominated at handshake
}

func newFakeTurnSession() *fakeTurnSession {
	return &fakeTurnSession{conversation: "fake-conversation-" + model.NewID().String()[:8]}
}

func (s *fakeTurnSession) Deliver(OutputFrame) {}

func (s *fakeTurnSession) Handshake(context.Context) (DriverHandshake, error) {
	return DriverHandshake{ConversationID: s.conversation, AuthState: AuthStateReady}, nil
}

func (s *fakeTurnSession) Input(_ context.Context, text string) (bool, error) {
	if text == "" {
		return false, badRequest("input text is required for a provider-driven session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs++
	s.turn = "turn-" + model.NewID().String()[:8]
	return true, nil
}

func (s *fakeTurnSession) Interrupt(context.Context) (bool, error) {
	s.mu.Lock()
	if s.turn == "" {
		s.mu.Unlock()
		// The same pre-effect refusal the Codex driver makes, and the same answer:
		// nothing was attempted.
		return false, conflictErr("there is no active provider turn to interrupt")
	}
	s.interrupts++
	hook, frame, err := s.interruptHook, s.interruptFrame, s.interruptErr
	if err == nil {
		s.turn = ""
	}
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	attempted := true
	if frame != nil {
		attempted = *frame
	}
	return attempted, err
}

func (s *fakeTurnSession) Shutdown(context.Context) {}
func (s *fakeTurnSession) AuthState() string        { return AuthStateReady }
func (s *fakeTurnSession) ConversationID() string   { return s.conversation }
func (s *fakeTurnSession) Close(error)              {}

func (s *fakeTurnSession) ActiveTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

func (s *fakeTurnSession) counts() (inputs, interrupts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inputs, s.interrupts
}

func (s *fakeTurnSession) programInterrupt(attempted bool, err error, hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interruptFrame, s.interruptErr, s.interruptHook = &attempted, err, hook
}

// fakeTurnFixture is a WORK-BOUND run driven by the fake protocol driver: the
// exact shape the missing control existed for.
type fakeTurnFixture struct {
	m         *Module
	st        store.Store
	clock     *testClock
	tenant    model.TenantID
	principal WorkPrincipal
	runRef    string
	itemID    model.ID
	itemVer   int64
	fence     int64
	session   *fakeTurnSession
	proc      *workControlProc
	live      *liveRun
}

func newFakeTurnFixture(t *testing.T) *fakeTurnFixture {
	t.Helper()
	ctx := context.Background()
	runner := &workControlRunner{}
	session := newFakeTurnSession()
	m, st, tenant, clock := newRuntimeHarness(t,
		WithRunner(runner),
		WithCredentialSource(staticCred()),
		WithProviderDriver(fakeTurnDriver{session: session}),
		WithWorkIdentityResolver(allowWorkIdentity{}),
		WithWorkContentGuard(allowWorkContent{}),
	)
	m.UseExecutionEnvironmentRef(testEnvRef)
	prof := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: fakeTurnDriverKey, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		DisplayName: "fake-turn", AuthSource: AuthSourceAccountHome,
	})
	itemID, _, agentRef := readyWorkLaunchItem(t, m, st, tenant)
	m.UseWorkIdentityResolver(durableWorkLaunchIdentity{m: m, st: st})
	spec := workLaunchSpec(itemID, agentRef)
	spec.Runtime.ProviderProfileRef = prof.Ref

	managed, err := m.LaunchForWork(ctx, tenant, spec)
	if err != nil {
		t.Fatalf("LaunchForWork under the fake turn driver: %v", err)
	}
	live, ok := m.rt.getLive(tenant, managed.RunRef)
	if !ok {
		t.Fatal("the work-launched run has no live handle")
	}
	if live.session == nil {
		t.Fatal("the work-launched run has no protocol driver session")
	}
	proc := runner.lastProc(t)
	t.Cleanup(func() {
		proc.finish(0)
		select {
		case <-live.finalizedCh:
		case <-time.After(finalizeWaitBudget):
		}
	})
	// The admin principal the fixture uses to move the lease from the outside, the
	// way a successor generation would.
	actorRef := model.NewID().String()
	principal := WorkPrincipal{
		ActorKind: model.ActorUser, ActorRef: actorRef, Actor: "user:" + actorRef,
		Admin: true, SessionID: live.claim.SID,
	}
	snapshot, err := m.Get(ctx, tenant, principal, itemID)
	if err != nil {
		t.Fatalf("read the launched work item: %v", err)
	}
	return &fakeTurnFixture{
		m: m, st: st, clock: clock, tenant: tenant, principal: principal,
		runRef: managed.RunRef, itemID: itemID, itemVer: snapshot.Item.Version,
		fence: managed.WorkLeaseFence, session: session, proc: proc, live: live,
	}
}

// startTurn puts a turn in flight through the fenced text route, which is the
// only way a work-bound driver run can have one.
func (fx *fakeTurnFixture) startTurn(t *testing.T) {
	t.Helper()
	if err := fx.m.TextForWork(context.Background(), fx.tenant, fx.runRef, fx.fence, "do the thing"); err != nil {
		t.Fatalf("fenced text to open a turn: %v", err)
	}
	if fx.session.ActiveTurn() == "" {
		t.Fatal("the fenced text did not open a turn on the driver")
	}
}

// TestInterruptForWorkExactFenceAndTheStaleReleasedDirections is the contract in
// one pass: the exact fence cancels the turn and LEAVES EVERYTHING USABLE, and
// every wrong fence refuses before the driver is touched.
func TestInterruptForWorkExactFenceAndTheStaleReleasedDirections(t *testing.T) {
	t.Parallel()

	fx := newFakeTurnFixture(t)
	ctx := context.Background()
	fx.startTurn(t)

	// A fence above the live one is stale: refused, and the driver never hears it.
	if err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence+1); workErrorCode(err) != "stale_fence" {
		t.Fatalf("stale-fence interrupt = %v, want stale_fence", err)
	}
	if _, interrupts := fx.session.counts(); interrupts != 0 {
		t.Fatalf("a stale fence reached the driver %d time(s)", interrupts)
	}
	// The legacy unfenced route stays shut for a work-bound run, so the fence is
	// not merely one of two doors: it is the only one.
	var re *runErr
	if _, err := fx.m.interruptRun(ctx, fx.tenant, fx.runRef, "user:operator", model.ActorUser); !errors.As(err, &re) ||
		re.status != http.StatusConflict {
		t.Fatalf("unfenced interrupt of a work-bound run = %v, want 409", err)
	}
	if _, interrupts := fx.session.counts(); interrupts != 0 {
		t.Fatalf("the unfenced route reached the driver %d time(s)", interrupts)
	}

	// The exact fence: the turn ends and NOTHING else does.
	if err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence); err != nil {
		t.Fatalf("exact-fence interrupt: %v", err)
	}
	if _, interrupts := fx.session.counts(); interrupts != 1 {
		t.Fatalf("exact-fence interrupt reached the driver %d time(s), want 1", interrupts)
	}
	if fx.session.ActiveTurn() != "" {
		t.Fatal("the interrupted turn is still the active one")
	}
	if got := fx.proc.stopCount(); got != 0 {
		t.Fatalf("the interrupt stopped the process %d time(s); it must never fall back to stop", got)
	}
	rec, err := fx.m.loadRun(ctx, fx.tenant, fx.runRef)
	if err != nil {
		t.Fatalf("re-read the interrupted run: %v", err)
	}
	if rec.String(colState) != stateRunning {
		t.Fatalf("interrupt is NOT terminal; state = %q", rec.String(colState))
	}
	events := eventNames(listRunEvents(t, fx.st, fx.tenant, fx.runRef))
	if !containsAll(events, "interrupting", "interrupted") {
		t.Fatalf("both halves of the interrupt must be audited: %v", events)
	}
	for _, event := range listRunEvents(t, fx.st, fx.tenant, fx.runRef) {
		if event.Event == "interrupting" || event.Event == "interrupted" {
			if event.Actor != model.ActorSystem || event.ActorKind != model.ActorSystem {
				t.Fatalf("internal dispatch interrupt must retain system attribution: %+v", event)
			}
		}
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAccepted); got != 1 {
		t.Fatalf("settled %d %s event(s), want 1", got, workInterruptAccepted)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 0 {
		t.Fatalf("a clean interrupt settled %d ambiguous event(s)", got)
	}
	requireRunEventWorkFence(t, fx.st, fx.tenant, fx.runRef, workInterruptAccepted,
		fx.itemID, fx.live.claim.SID, fx.fence)
	// The run is USABLE: the next fenced turn goes to the same driver session.
	if err := fx.m.TextForWork(ctx, fx.tenant, fx.runRef, fx.fence, "next"); err != nil {
		t.Fatalf("fenced text after the interrupt: %v", err)
	}
	if inputs, _ := fx.session.counts(); inputs != 2 {
		t.Fatalf("driver inputs after the interrupt = %d, want 2", inputs)
	}

	// And once the generation is released, its fence is no longer authority.
	fx.releaseLease(t)
	if err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence); workErrorCode(err) != "stale_fence" {
		t.Fatalf("interrupt under a released lease = %v, want stale_fence", err)
	}
	if _, interrupts := fx.session.counts(); interrupts != 1 {
		t.Fatalf("a released lease reached the driver; interrupts = %d, want 1", interrupts)
	}
}

// TestInterruptForWorkRefusesAnExpiredGenerationBeforeTheDriver moves the clock
// past the lease instead of ending it by command. An expiry is the direction a
// caller never announces: nobody released anything, the fence integer is still
// the one the caller holds, and only the clock says the authority is gone.
func TestInterruptForWorkRefusesAnExpiredGenerationBeforeTheDriver(t *testing.T) {
	t.Parallel()

	fx := newFakeTurnFixture(t)
	ctx := context.Background()
	fx.startTurn(t)

	// Far beyond any lease or claim TTL this fixture could have taken.
	fx.clock.advance(72 * time.Hour)

	err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence)
	if err == nil {
		t.Fatal("an expired generation must not cancel a turn")
	}
	// The session claim and the WorkLease run on the same clock, so either may be
	// the one that refuses; what this pins is that the refusal happens ABOVE the
	// driver, in the fenced vocabulary, and that no frame was attempted.
	if code := workErrorCode(err); code != "stale_fence" {
		t.Fatalf("expired-generation interrupt = %v (code %q), want stale_fence", err, code)
	}
	if _, interrupts := fx.session.counts(); interrupts != 0 {
		t.Fatalf("an expired generation reached the driver %d time(s)", interrupts)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 0 {
		t.Fatalf("a pre-effect expiry recorded %d ambiguous event(s)", got)
	}
	if got := fx.proc.stopCount(); got != 0 {
		t.Fatalf("an expired interrupt stopped the process %d time(s)", got)
	}
}

// TestInterruptForWorkRefusesAfterTheSessionClaimMoves is the OTHER authority.
// The WorkLease is untouched and its fence is exact; what moved is the session
// Claim the process was launched under.
func TestInterruptForWorkRefusesAfterTheSessionClaimMoves(t *testing.T) {
	t.Parallel()

	fx := newFakeTurnFixture(t)
	ctx := context.Background()
	fx.startTurn(t)
	successor := takeOverClaim(t, fx.m, fx.tenant, fx.live, "interrupt-successor")

	err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence)
	if err == nil {
		t.Fatalf("interrupt succeeded after the claim moved from fence %d to %d",
			fx.live.claim.Fence, successor.Fence)
	}
	if _, interrupts := fx.session.counts(); interrupts != 0 {
		t.Fatalf("a superseded launch reached the driver %d time(s)", interrupts)
	}
	if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 0 {
		t.Fatalf("a pre-effect claim refusal recorded %d ambiguous event(s)", got)
	}
	if got := fx.proc.stopCount(); got != 0 {
		t.Fatalf("a refused interrupt stopped the process %d time(s)", got)
	}
}

// TestInterruptForWorkPreEffectRefusalsAreRefusalsAndNotAmbiguities is the
// finding this lot was asked to close: a driver that refuses before writing must
// be able to SAY so, or a perfectly clean answer is filed for ever as UNKNOWN.
func TestInterruptForWorkPreEffectRefusalsAreRefusalsAndNotAmbiguities(t *testing.T) {
	t.Parallel()

	t.Run("the driver has no active turn", func(t *testing.T) {
		t.Parallel()
		fx := newFakeTurnFixture(t)
		// No startTurn: the conversation is idle.
		err := fx.m.InterruptForWork(context.Background(), fx.tenant, fx.runRef, fx.fence)
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusConflict || asWorkError(err) != nil {
			t.Fatalf("idle interrupt = %v, want the direct runtime conflict taxonomy", err)
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 0 {
			t.Fatalf("an idle interrupt recorded %d ambiguous event(s)", got)
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAccepted); got != 0 {
			t.Fatalf("an idle interrupt settled %d accepted event(s)", got)
		}
	})

	t.Run("the driver refuses with nothing on the wire", func(t *testing.T) {
		t.Parallel()
		fx := newFakeTurnFixture(t)
		fx.startTurn(t)
		fx.session.programInterrupt(false, errors.New("test: refused before writing"), nil)

		err := fx.m.InterruptForWork(context.Background(), fx.tenant, fx.runRef, fx.fence)
		if err == nil {
			t.Fatal("a driver refusal must surface")
		}
		if asWorkError(err) != nil {
			t.Fatalf("pre-effect driver refusal = %v, want the runtime taxonomy, not a work verdict", err)
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 0 {
			t.Fatalf("a pre-effect driver refusal recorded %d ambiguous event(s)", got)
		}
	})

	t.Run("a run with no protocol driver is told so, not stopped", func(t *testing.T) {
		t.Parallel()
		fx := newRuntimeWorkControlFixture(t)
		err := fx.m.InterruptForWork(context.Background(), fx.tenant, fx.runRef, fx.lease.Fence)
		var re *runErr
		if !errors.As(err, &re) || re.status != http.StatusConflict || asWorkError(err) != nil {
			t.Fatalf("interrupt of a driverless run = %v, want a direct runtime conflict", err)
		}
		if got := fx.proc.stopCount(); got != 0 {
			t.Fatalf("the refusal stopped the process %d time(s); interrupt never becomes stop", got)
		}
		rec, lerr := fx.m.loadRun(context.Background(), fx.tenant, fx.runRef)
		if lerr != nil || rec.String(colState) != stateRunning {
			t.Fatalf("the run must stay running after a refused interrupt: %v %q", lerr, rec.String(colState))
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 0 {
			t.Fatalf("a driverless refusal recorded %d ambiguous event(s)", got)
		}
	})
}

// TestInterruptForWorkAmbiguityWhenSomethingMayHaveCrossed is the other half of
// the same contract, in both of its shapes: the driver failed AFTER writing, and
// the authority moved after the frame but before the settlement.
func TestInterruptForWorkAmbiguityWhenSomethingMayHaveCrossed(t *testing.T) {
	t.Parallel()

	t.Run("the driver failed after writing", func(t *testing.T) {
		t.Parallel()
		fx := newFakeTurnFixture(t)
		fx.startTurn(t)
		fx.session.programInterrupt(true, errors.New("test: wrote, then failed"), nil)

		err := fx.m.InterruptForWork(context.Background(), fx.tenant, fx.runRef, fx.fence)
		if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != workInterruptAmbiguous {
			t.Fatalf("write-then-error interrupt = %v, want UNKNOWN/%s", err, workInterruptAmbiguous)
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 1 {
			t.Fatalf("settled %d ambiguous event(s), want 1", got)
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAccepted); got != 0 {
			t.Fatalf("a failed interrupt falsely settled %d accepted event(s)", got)
		}
	})

	t.Run("the generation moved between the frame and the settlement", func(t *testing.T) {
		t.Parallel()
		fx := newFakeTurnFixture(t)
		fx.startTurn(t)
		var hookErr error
		// Inside the driver call, so the revocation lands after the frame and before
		// settleRunWorkAction can confirm anything.
		fx.session.programInterrupt(true, nil, func() { hookErr = fx.revokeLeaseErr() })

		err := fx.m.InterruptForWork(context.Background(), fx.tenant, fx.runRef, fx.fence)
		if hookErr != nil {
			t.Fatalf("revoke the fence from the driver hook: %v", hookErr)
		}
		if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown || we.code != workInterruptAmbiguous {
			t.Fatalf("post-effect fence move = %v, want UNKNOWN/%s", err, workInterruptAmbiguous)
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAccepted); got != 0 {
			t.Fatalf("a raced interrupt falsely settled %d accepted event(s)", got)
		}
		if got := countNamedRunEvents(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous); got != 1 {
			t.Fatalf("a raced interrupt settled %d ambiguous event(s), want 1", got)
		}
		// The UNKNOWN names the generation that acted, not the one that superseded it.
		requireRunEventWorkFence(t, fx.st, fx.tenant, fx.runRef, workInterruptAmbiguous,
			fx.itemID, fx.live.claim.SID, fx.fence)
	})
}

// TestInterruptForWorkRefusesAPartiallyWrittenWorkBinding closes the door the
// legacy plane must never open. A stamp with one NULL member is not a non-work
// run: runHasWorkBinding counts any member, so the legacy route stays shut, and
// the fenced route answers "I cannot look" rather than acting on half a binding.
func TestInterruptForWorkRefusesAPartiallyWrittenWorkBinding(t *testing.T) {
	t.Parallel()

	fx := newFakeTurnFixture(t)
	ctx := context.Background()
	fx.startTurn(t)
	if err := mutateRunForWorkTest(fx.m, fx.tenant, fx.runRef, func(rec model.Record) {
		rec[colRunWorkDispatchKey] = nil
	}); err != nil {
		t.Fatalf("corrupt one member of the durable work stamp: %v", err)
	}

	err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence)
	if we := asWorkError(err); we == nil || we.verdict != VerdictUnknown {
		t.Fatalf("interrupt on a partial stamp = %v, want an UNKNOWN verdict", err)
	}
	var re *runErr
	if _, lerr := fx.m.interruptRun(ctx, fx.tenant, fx.runRef, "user:operator", model.ActorUser); !errors.As(lerr, &re) ||
		re.status != http.StatusConflict {
		t.Fatalf("legacy interrupt on a partial stamp = %v, want 409 (never the legacy plane)", lerr)
	}
	if _, interrupts := fx.session.counts(); interrupts != 0 {
		t.Fatalf("a partial stamp reached the driver %d time(s)", interrupts)
	}
	if got := fx.proc.stopCount(); got != 0 {
		t.Fatalf("a partial stamp stopped the process %d time(s)", got)
	}
}

// TestRunControlLockIsTakenOnceAtTheOuterEntry is the serialization half of this
// lot, and it has to prove BOTH directions: that raw input now waits for the
// other controls of its run, and that no control takes the same non-reentrant
// lock twice on its way to the child.
//
// The deadlock arm is not decoration. sendInputLoaded is called by the legacy
// route AND by InputForWork; putting the lock in the shared helper instead of at
// the two entries would compile, pass a casual read, and hang the process that
// owns the child the first time either route was used.
func TestRunControlLockIsTakenOnceAtTheOuterEntry(t *testing.T) {
	t.Parallel()

	t.Run("fenced raw input serializes with a concurrent fenced stop", func(t *testing.T) {
		t.Parallel()
		fx := newRuntimeWorkControlFixture(t)
		ctx := context.Background()
		entered, stopDone := make(chan struct{}), make(chan error, 1)
		fx.proc.setAfterSend(func() {
			// Still inside Process.Send, so InputForWork holds the run lock.
			go func() {
				close(entered)
				stopDone <- fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, "concurrent stop")
			}()
			<-entered
			// Give the stop every chance to overtake us. It cannot: it is queued on
			// the lock this input holds. Before the lock existed, this window is
			// where a stop tore down a process another control was writing into.
			time.Sleep(100 * time.Millisecond)
			if got := fx.proc.stopCount(); got != 0 {
				t.Errorf("a stop reached the process %d time(s) while an input was in flight", got)
			}
		})

		mustReturnWithin(t, "InputForWork", 20*time.Second, func() {
			if err := fx.m.InputForWork(ctx, fx.tenant, fx.runRef, fx.lease.Fence, []byte(`{"type":"serialized"}`)); err != nil {
				t.Errorf("fenced raw input: %v", err)
			}
		})
		select {
		case err := <-stopDone:
			if err != nil {
				t.Fatalf("the queued stop failed once the input released the lock: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the queued stop never ran after the input released the lock")
		}
		if got := fx.proc.stopCount(); got != 1 {
			t.Fatalf("process stops = %d, want exactly 1 after the input completed", got)
		}
	})

	t.Run("legacy raw input serializes with a concurrent legacy stop", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		runner := &workControlRunner{}
		m, _, tenant, _ := newRuntimeHarness(t,
			WithRunner(runner), WithCredentialSource(staticCred()),
		)
		run, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:legacy-serialize", ActorKind: model.ActorUser,
		})
		if err != nil {
			t.Fatalf("create legacy run: %v", err)
		}
		proc := runner.lastProc(t)
		live, ok := m.rt.getLive(tenant, run.RunRef)
		if !ok {
			t.Fatal("legacy run has no live handle")
		}
		t.Cleanup(func() {
			proc.finish(0)
			select {
			case <-live.finalizedCh:
			case <-time.After(finalizeWaitBudget):
			}
		})
		entered, stopDone := make(chan struct{}), make(chan error, 1)
		proc.setAfterSend(func() {
			go func() {
				close(entered)
				_, serr := m.stopRun(ctx, tenant, run.RunRef, "user:operator", model.ActorUser)
				stopDone <- serr
			}()
			<-entered
			time.Sleep(100 * time.Millisecond)
			if got := proc.stopCount(); got != 0 {
				t.Errorf("a legacy stop reached the process %d time(s) during an input", got)
			}
		})

		mustReturnWithin(t, "sendInput", 20*time.Second, func() {
			if err := m.sendInput(ctx, tenant, run.RunRef, []byte(`{"type":"legacy-serialized"}`)); err != nil {
				t.Errorf("legacy raw input: %v", err)
			}
		})
		select {
		case err := <-stopDone:
			if err != nil {
				t.Fatalf("the queued legacy stop failed: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the queued legacy stop never ran after the input released the lock")
		}
	})

	t.Run("every control of one run returns without self-deadlock", func(t *testing.T) {
		t.Parallel()
		fx := newFakeTurnFixture(t)
		ctx := context.Background()
		// The whole fenced surface, back to back on the SAME run and therefore the
		// same lock key. A second acquisition anywhere below an entry point would
		// never return, so reaching the end of this list IS the assertion.
		mustReturnWithin(t, "TextForWork", 20*time.Second, func() {
			if err := fx.m.TextForWork(ctx, fx.tenant, fx.runRef, fx.fence, "one"); err != nil {
				t.Errorf("TextForWork: %v", err)
			}
		})
		mustReturnWithin(t, "InputForWork", 20*time.Second, func() {
			// Raw bytes stay refused on a driver run; what is under test is that the
			// refusal RETURNS, having taken the outer lock and released it.
			if err := fx.m.InputForWork(ctx, fx.tenant, fx.runRef, fx.fence, []byte("raw")); err == nil {
				t.Error("a raw fenced line must stay refused on a driver-backed run")
			}
		})
		mustReturnWithin(t, "InterruptForWork", 20*time.Second, func() {
			if err := fx.m.InterruptForWork(ctx, fx.tenant, fx.runRef, fx.fence); err != nil {
				t.Errorf("InterruptForWork: %v", err)
			}
		})
		mustReturnWithin(t, "StopForWork", 20*time.Second, func() {
			if err := fx.m.StopForWork(ctx, fx.tenant, fx.runRef, fx.fence, "done"); err != nil {
				t.Errorf("StopForWork: %v", err)
			}
		})
	})
}

// mustReturnWithin runs fn and fails the test if it has not returned in budget.
// A self-deadlock on the per-run lock does not panic, log or exit: it simply
// never comes back, so the only way to assert its absence is a deadline.
func mustReturnWithin(t *testing.T, what string, budget time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(budget):
		t.Fatalf("%s did not return within %s: the per-run operation lock was taken more than once", what, budget)
	}
}

// releaseLease ends the fixture's generation from the outside, the way a holder
// that finished its work would.
func (fx *fakeTurnFixture) releaseLease(t *testing.T) {
	t.Helper()
	lease, err := fx.m.GetLease(context.Background(), fx.tenant, fx.principal, fx.itemID)
	if err != nil {
		t.Fatalf("read the live lease before releasing it: %v", err)
	}
	released, err := fx.m.Apply(context.Background(), fx.tenant, fx.principal, WorkCommand{
		Command: "lease.release", WorkItemID: fx.itemID, Fence: fx.fence,
		HolderSID: lease.HolderSID, HolderRunRef: lease.HolderRunRef,
		Reason:          "test: the generation finished while the process stays supervised",
		ExpectedVersion: fx.itemVer,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err != nil {
		t.Fatalf("release the work lease: %v", err)
	}
	fx.itemVer = released.Version
}

// revokeLeaseErr moves the fence from under an in-flight effect. It returns the
// error instead of failing, because it runs inside a driver callback where a
// t.Fatalf would abandon the runtime mid-control.
func (fx *fakeTurnFixture) revokeLeaseErr() error {
	revoked, err := fx.m.Apply(context.Background(), fx.tenant, fx.principal, WorkCommand{
		Command: "lease.revoke", WorkItemID: fx.itemID, Fence: fx.fence,
		Reason:          "test: the fence moved after the interrupt frame",
		ExpectedVersion: fx.itemVer,
		IdempotencyKey:  model.NewID().String(), HTTPMethod: http.MethodPost,
	})
	if err == nil {
		fx.itemVer = revoked.Version
	}
	return err
}
