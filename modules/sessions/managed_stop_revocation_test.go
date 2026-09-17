// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_revocation_test.go (P2 / W2) — T-T2 as ROOT-CORRECTION-1 restates
// it: on a REAL child and a real backend, a persistent store block ends a managed
// Stop unknown within its separate stage bounds, a released block completes it only
// with the exact launch's committed P1, and the legacy Stop under the same block
// keeps its unbounded WithoutCancel wait.
//
// The block is real: an auth transaction on the real store that has written a
// SIBLING credential's token row and has not committed. On SQLite that holds the
// single writer; on PostgreSQL it holds the directory writer lock every auth write
// takes before it touches a row. The target token row itself is left untouched on
// purpose: bumping its version would make the blocked revocation lose its own
// compare-and-swap after release, which measures the hold and not the phase. The
// revoker is the
// product's Authenticator.RevokeWorkSessionCredential behind an adapter identical
// to the composition root's. The managed cases stop a real supervised child; only
// the legacy control keeps a child that ignores Stop, because there the question
// is the legacy wait itself and not what the finalizer publishes.

// revocationObservation is one call into the credential revoker.
type revocationObservation struct {
	bounded        bool
	id             model.ID
	started, ended time.Time
	err            error
}

// authWorkCredentialSource is cmd/olivares' sessionWorkCredentialSource over the
// REAL authenticator, with every revocation observed.
type authWorkCredentialSource struct {
	authr    *auth.Authenticator
	entered  chan revocationObservation
	returned chan revocationObservation
}

func newAuthWorkCredentialSource(a *auth.Authenticator) *authWorkCredentialSource {
	return &authWorkCredentialSource{
		authr:    a,
		entered:  make(chan revocationObservation, 32),
		returned: make(chan revocationObservation, 32),
	}
}

func workSpecOf(req WorkSessionCredentialRequest) auth.WorkSessionCredentialSpec {
	return auth.WorkSessionCredentialSpec{
		Tenant: req.Tenant, SessionRef: req.SessionRef, RunRef: req.RunRef,
		AgentRef: req.AgentRef, ClaimFence: req.ClaimFence,
	}
}

func (s *authWorkCredentialSource) Mint(ctx context.Context, req WorkSessionCredentialRequest) (WorkSessionCredential, error) {
	actor, err := auth.NewSystemOperator("sessions-runtime",
		"mint a purpose-restricted credential for an admitted session process")
	if err != nil {
		return WorkSessionCredential{}, err
	}
	issued, err := s.authr.IssueWorkSessionCredential(ctx, actor, workSpecOf(req))
	if err != nil {
		return WorkSessionCredential{}, err
	}
	return WorkSessionCredential{
		ID: issued.ID, Token: issued.Token, Tenant: issued.Tenant,
		SessionRef: issued.SessionRef, RunRef: issued.RunRef, AgentRef: issued.AgentRef,
		ClaimFence: issued.ClaimFence, NotAfter: issued.ExpiresAt,
	}, nil
}

func (s *authWorkCredentialSource) Renew(ctx context.Context, id model.ID, req WorkSessionCredentialRequest) (time.Time, error) {
	actor, err := auth.NewSystemOperator("sessions-runtime",
		"renew an admitted session credential after its live Claim heartbeat")
	if err != nil {
		return time.Time{}, err
	}
	return s.authr.RenewWorkSessionCredential(ctx, actor, id, workSpecOf(req))
}

func (s *authWorkCredentialSource) Revoke(ctx context.Context, id model.ID, req WorkSessionCredentialRequest) error {
	_, bounded := ctx.Value(boundedRevocationKey{}).(struct{})
	obs := revocationObservation{bounded: bounded, id: id, started: time.Now()}
	select {
	case s.entered <- obs:
	default:
	}
	actor, err := auth.NewSystemOperator("sessions-runtime",
		"revoke the admitted session process credential after its authority ended")
	if err == nil {
		err = s.authr.RevokeWorkSessionCredential(ctx, actor, id, workSpecOf(req))
	}
	obs.ended, obs.err = time.Now(), err
	select {
	case s.returned <- obs:
	default:
	}
	return err
}

// stubbornRunner launches processes that acknowledge Stop and keep running until
// the test finishes them, and runs a one-shot hook at the moment Stop is asked.
type stubbornRunner struct {
	mu     sync.Mutex
	procs  []*fakeProc
	onStop func()
}

type stubbornProc struct {
	*fakeProc
	runner *stubbornRunner
}

func (p stubbornProc) Stop(context.Context) error {
	p.runner.mu.Lock()
	hook := p.runner.onStop
	p.runner.onStop = nil
	p.runner.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (r *stubbornRunner) Launch(_ context.Context, _ LaunchSpec) (Process, error) {
	p := &fakeProc{out: make(chan OutputFrame, 16), stopped: make(chan struct{})}
	r.mu.Lock()
	r.procs = append(r.procs, p)
	r.mu.Unlock()
	return stubbornProc{fakeProc: p, runner: r}, nil
}

func (r *stubbornRunner) setStopHook(hook func()) {
	r.mu.Lock()
	r.onStop = hook
	r.mu.Unlock()
}

func (r *stubbornRunner) finishAll() {
	r.mu.Lock()
	procs := append([]*fakeProc(nil), r.procs...)
	r.mu.Unlock()
	for _, p := range procs {
		p.finish(0)
	}
}

// tokenRowHold is an open auth transaction on the real store that has written one
// token row and has not committed.
type tokenRowHold struct {
	release func()
	done    chan error
	// heldAt is when the uncommitted write was in place: immediately before the
	// revocation phase starts.
	heldAt time.Time
}

func (f *managedStopFixture) holdTokenRow(id model.ID) (*tokenRowHold, error) {
	held, rel := make(chan struct{}), make(chan struct{})
	var heldOnce, relOnce sync.Once
	h := &tokenRowHold{done: make(chan error, 1)}
	h.release = func() { relOnce.Do(func() { close(rel) }) }
	go func() {
		ctx := context.Background()
		h.done <- f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			tok, err := as.Tokens().Get(ctx, id)
			if err != nil {
				return err
			}
			now := model.NewTimestamp(time.Now())
			tok.LastUsedAt = &now
			if _, err := as.Tokens().Update(ctx, tok); err != nil {
				return err
			}
			heldOnce.Do(func() { close(held) })
			<-rel
			return nil
		})
	}()
	select {
	case <-held:
		h.heldAt = time.Now()
		return h, nil
	case err := <-h.done:
		return nil, fmt.Errorf("the hold ended before it held the token row: %w", err)
	case <-time.After(30 * time.Second):
		h.release()
		return nil, errors.New("the hold did not acquire the token row within 30s")
	}
}

// newBoundedRevocationFixture composes the module with the stubborn runner, the
// real work-credential issuer and a short managed revocation bound.
func newBoundedRevocationFixture(t *testing.T, cfg store.Config, bound time.Duration) (
	*managedStopFixture, *stubbornRunner, *authWorkCredentialSource,
) {
	t.Helper()
	runner := &stubbornRunner{}
	f := newManagedStopFixtureWith(t, cfg,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithProductVersion("test"), WithStopWaitDelay(time.Second),
		WithManagedStopAdmissionTimeout(bound),
	)
	t.Cleanup(runner.finishAll)
	src := newAuthWorkCredentialSource(f.authr)
	f.m.UseWorkSessionCredentialSource(src)
	return f, runner, src
}

func (f *managedStopFixture) liveWorkCredential(lr *liveRun) model.ID {
	f.t.Helper()
	lr.mu.Lock()
	id := lr.workCredentialID
	lr.mu.Unlock()
	return id
}

func (f *managedStopFixture) durableWorkCredential(runRef string) string {
	f.t.Helper()
	ctx := context.Background()
	var id string
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		id = rec.String(colWorkCredentialID)
		return err
	}); err != nil {
		f.t.Fatalf("read the durable credential handle of %s: %v", runRef, err)
	}
	return id
}

func (f *managedStopFixture) tokenRevoked(id model.ID) bool {
	f.t.Helper()
	ctx := context.Background()
	var revoked bool
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		tok, err := as.Tokens().Get(ctx, id)
		revoked = tok.Revoked
		return err
	}); err != nil {
		f.t.Fatalf("read token %s: %v", id, err)
	}
	return revoked
}

// armTokenRowHold makes the NEXT Stop request take the hold on the given token
// row before it returns, so the block begins after every pre-effect transaction
// has finished and before the revocation phase starts.
func (f *managedStopFixture) armTokenRowHold(runner interface{ setStopHook(func()) }, id model.ID) (<-chan *tokenRowHold, <-chan error) {
	holds, failures := make(chan *tokenRowHold, 1), make(chan error, 1)
	runner.setStopHook(func() {
		h, err := f.holdTokenRow(id)
		if err != nil {
			failures <- err
			return
		}
		holds <- h
	})
	return holds, failures
}

func awaitHold(t *testing.T, holds <-chan *tokenRowHold, failures <-chan error) *tokenRowHold {
	t.Helper()
	select {
	case h := <-holds:
		t.Cleanup(h.release)
		return h
	case err := <-failures:
		t.Fatalf("arm the token row hold: %v", err)
	case <-time.After(90 * time.Second):
		t.Fatal("the Stop request never reached the process boundary")
	}
	return nil
}

func awaitObservation(t *testing.T, ch <-chan revocationObservation, what string) revocationObservation {
	t.Helper()
	select {
	case obs := <-ch:
		return obs
	case <-time.After(90 * time.Second):
		t.Fatalf("%s: no revocation observed", what)
	}
	return revocationObservation{}
}

// awaitBoundedObservation returns the managed phase's own revocation, skipping any
// unbounded revocation (the legacy finalizer's) that happens to return first.
func awaitBoundedObservation(t *testing.T, ch <-chan revocationObservation) revocationObservation {
	t.Helper()
	deadline := time.After(90 * time.Second)
	for {
		select {
		case obs := <-ch:
			if obs.bounded {
				return obs
			}
			t.Logf("an unbounded revocation returned first: %v", obs.err)
		case <-deadline:
			t.Fatal("no bounded revocation was observed")
			return revocationObservation{}
		}
	}
}

// tt2Setup is one real child with a real work-session credential, a sibling whose
// credential the hold writes, and the managed Stop's request.
type tt2Setup struct {
	f          *managedStopFixture
	runner     *hookedProcRunner
	src        *authWorkCredentialSource
	logs       *managedStopStageLog
	dto        runDTO
	lr         *liveRun
	credential model.ID
	req        managedStopCall
}

func newTT2Setup(t *testing.T, cfg store.Config, name string, admission time.Duration) tt2Setup {
	t.Helper()
	f, runner := newRealChildStopFixture(t, cfg, admission)
	s := tt2Setup{f: f, runner: runner, src: newAuthWorkCredentialSource(f.authr)}
	f.m.UseWorkSessionCredentialSource(s.src)
	s.logs = captureManagedStopStages(f.m)
	op := f.operator(name+"@w2.test", auth.RoleEditor, true, 2*time.Minute)
	s.dto, s.lr = f.launch("thread-" + name)
	s.credential = f.liveWorkCredential(s.lr)
	if s.credential.IsZero() || f.durableWorkCredential(s.dto.RunRef) != s.credential.String() {
		t.Fatalf("the launch minted no durable work-session credential (%q)", s.credential)
	}
	s.req = f.request(op, s.dto, s.lr.launchID, name)
	return s
}

// TestManagedStopUnderAPersistentStoreBlockEndsUnknown is ROOT-CORRECTION-1's first
// T-T2 case. The block holds for the whole call: the explicit revocation fails at
// its own bound and keeps its handle, the finalize wait, the observation read and
// the settlement each end at their own bound, and without a committed exact-launch
// P1 the call returns unknown. After release the finalizer publishes P1 on its
// own, the original journal is NOT completed retroactively, and a replay reports
// what is recorded without adopting anything.
func TestManagedStopUnderAPersistentStoreBlockEndsUnknown(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			const admission, waitDelay = 2 * time.Second, 2 * time.Second
			s := newTT2Setup(t, be.config(t), "tt2-persistent", admission)
			f := s.f
			_, sibling := f.launch("thread-tt2-persistent-sibling")
			holds, failures := f.armTokenRowHold(s.runner, f.liveWorkCredential(sibling))

			done := make(chan managedStopAnswer, 1)
			go func() {
				res, err := f.call(s.req)
				done <- managedStopAnswer{res, err, time.Now()}
			}()
			hold := awaitHold(t, holds, failures)
			obs := awaitBoundedObservation(t, s.src.returned)
			if obs.err == nil {
				t.Fatal("the bounded revocation succeeded although the auth writer was held")
			}
			retained := f.liveWorkCredential(s.lr)

			var got managedStopAnswer
			select {
			case got = <-done:
			case <-time.After(2*waitDelay + 2*admission + 60*time.Second):
				t.Fatal("the managed Stop did not return while the block persisted")
			}
			select {
			case err := <-hold.done:
				t.Fatalf("the hold ended before the managed Stop returned: %v", err)
			default:
			}
			res := got.res
			if got.err != nil {
				t.Fatalf("StopManagedRun: %v (%+v)", got.err, res)
			}
			if res.Outcome != ManagedStopUnknown || res.ProcessOutcome != managedStopUnverified ||
				res.Observation != "" || !res.Attempted {
				t.Fatalf("result under a persistent block = %+v, want an attempted unknown stop with no observation", res)
			}
			if res.CredentialRevocation != managedStopRevokeFailed {
				t.Fatalf("credential revocation = %q, want the phase's own failure", res.CredentialRevocation)
			}
			if retained != s.credential {
				t.Fatalf("the failed phase dropped its handle (%q) at its own boundary", retained)
			}

			op := res.OperationRef
			stop := s.logs.stage(t, op, "stop")
			revocation := s.logs.stage(t, op, "revocation")
			wait := s.logs.stage(t, op, "finalize_wait")
			observation := s.logs.stage(t, op, "observation")
			settlement := s.logs.stage(t, op, "settlement")
			requireAbout(t, "the revocation phase", stageElapsed(revocation), admission)
			if stageFlag(wait, "finalizer_returned") {
				t.Fatal("the finalizer returned although the block persisted")
			}
			requireAbout(t, "the finalize wait", stageElapsed(wait), 2*waitDelay)
			if be.name == "sqlite" {
				// ONE writer: the observation read and the settlement each wait on it
				// until their own bound, and nothing durable is recorded.
				if stageFlag(observation, "read") || stageFlag(settlement, "recorded") {
					t.Fatalf("SQLite read %v / recorded %v through its held single writer", observation, settlement)
				}
				requireAbout(t, "the observation read", stageElapsed(observation), admission)
				requireAbout(t, "the settlement", stageElapsed(settlement), admission)
				if res.Settlement != "" {
					t.Fatalf("settlement %q committed through the held writer", res.Settlement)
				}
			} else {
				if !stageFlag(observation, "read") || !stageFlag(settlement, "recorded") {
					t.Fatalf("PostgreSQL could not read %v / record %v beside an auth-only block", observation, settlement)
				}
				requireAtMost(t, "the observation read", stageElapsed(observation), admission)
				requireAtMost(t, "the settlement", stageElapsed(settlement), admission)
				if res.Settlement != model.EvidenceOpUnknown {
					t.Fatalf("settlement = %q, want the durable unknown", res.Settlement)
				}
			}
			t.Logf("%s persistent block: stop %s, revocation %s, finalize_wait %s, observation %s, settlement %s; "+
				"call returned %s after the block; result %+v", be.name, stageElapsed(stop), stageElapsed(revocation),
				stageElapsed(wait), stageElapsed(observation), stageElapsed(settlement), got.at.Sub(hold.heldAt), res)

			// Release. The legacy finalizer completes in its own order, after the call.
			hold.release()
			if err := <-hold.done; err != nil {
				t.Fatalf("the hold transaction failed: %v", err)
			}
			select {
			case <-s.lr.finalizedCh:
			case <-time.After(60 * time.Second):
				t.Fatal("the finalizer did not complete after the release")
			}
			if p1 := f.exactLaunchObservation(s.dto.RunRef, s.lr.launchID); p1 != obsProcessExitObserved {
				t.Fatalf("the finalizer published P1 %q after the release, want the observed exit", p1)
			}
			row, found := f.journal("tt2-persistent")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			replay, rerr := f.m.ReconcileManagedStop(ctx, f.tenant, s.req.Question, s.req.ManagedStopRequest)
			if rerr != nil {
				t.Fatalf("reconcile: %v", rerr)
			}
			if be.name == "sqlite" {
				if !found || row.State != model.EvidenceOpClaimed {
					t.Fatalf("journal after a blocked settlement = %+v found=%t, want it durably claimed", row, found)
				}
				if replay.Outcome != ManagedStopObservedExitUnsettled || replay.Observation != obsProcessExitObserved {
					t.Fatalf("replay = %+v, want observed_exit_unsettled with the exact-launch P1", replay)
				}
			} else {
				if !found || row.State != model.EvidenceOpUnknown {
					t.Fatalf("journal = %+v found=%t, want the unknown settlement, not a retroactive completion", row, found)
				}
				if replay.Outcome != ManagedStopUnknown || replay.Observation != obsProcessExitObserved {
					t.Fatalf("replay = %+v, want the recorded unknown beside the later P1", replay)
				}
			}
			if after, _ := f.journal("tt2-persistent"); after.Version != row.Version || after.State != row.State {
				t.Fatalf("reconciliation moved the journal row: %+v -> %+v", row, after)
			}
			t.Logf("%s after release: journal %s, replay %s (%s)", be.name, row.State, replay.Outcome, replay.Observation)
		})
	}
}

// TestManagedStopReleasedBlockCompletesOnlyWithExactLaunchP1 is ROOT-CORRECTION-1's
// second T-T2 case. The explicit revocation fails at its bound and keeps its
// handle; the block is then released during the original call's observation
// opportunity, the finalizer completes in its established order, and only the
// committed exact-launch P1 lets the call report exit_observed and settle
// completed. The phase's own revocation failure is still reported.
func TestManagedStopReleasedBlockCompletesOnlyWithExactLaunchP1(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			const admission, waitDelay = 2 * time.Second, 2 * time.Second
			s := newTT2Setup(t, be.config(t), "tt2-released", admission)
			f := s.f
			_, sibling := f.launch("thread-tt2-released-sibling")
			holds, failures := f.armTokenRowHold(s.runner, f.liveWorkCredential(sibling))

			done := make(chan managedStopAnswer, 1)
			go func() {
				res, err := f.call(s.req)
				done <- managedStopAnswer{res, err, time.Now()}
			}()
			hold := awaitHold(t, holds, failures)
			obs := awaitBoundedObservation(t, s.src.returned)
			if obs.err == nil {
				t.Fatal("the bounded revocation succeeded although the auth writer was held")
			}
			retained := f.liveWorkCredential(s.lr)
			hold.release()
			releasedAt := time.Now()
			if err := <-hold.done; err != nil {
				t.Fatalf("the hold transaction failed: %v", err)
			}

			var got managedStopAnswer
			select {
			case got = <-done:
			case <-time.After(2*waitDelay + 2*admission + 60*time.Second):
				t.Fatal("the managed Stop did not return after the release")
			}
			res := got.res
			if got.err != nil {
				t.Fatalf("StopManagedRun: %v (%+v)", got.err, res)
			}
			if retained != s.credential {
				t.Fatalf("the failed phase dropped its handle (%q) at its own boundary", retained)
			}
			if res.CredentialRevocation != managedStopRevokeFailed {
				t.Fatalf("credential revocation = %q; a later finalizer cleanup does not erase the phase failure",
					res.CredentialRevocation)
			}
			if p1 := f.exactLaunchObservation(s.dto.RunRef, s.lr.launchID); p1 != obsProcessExitObserved {
				t.Fatalf("committed P1 = %q; the positive has not qualified", p1)
			}
			if res.Outcome != ManagedStopStopped || res.ProcessOutcome != managedStopExitObserved ||
				res.Observation != obsProcessExitObserved || res.Settlement != model.EvidenceOpCompleted {
				t.Fatalf("result after the release = %+v, want stopped on the exact-launch observed exit", res)
			}
			if row, found := f.journal("tt2-released"); !found || row.State != model.EvidenceOpCompleted {
				t.Fatalf("journal = %+v found=%t, want completed", row, found)
			}
			if processRunning(s.lr.proc.PID()) {
				t.Fatal("the supervised child is still running after an observed exit")
			}
			op := res.OperationRef
			stop := s.logs.stage(t, op, "stop")
			revocation := s.logs.stage(t, op, "revocation")
			wait := s.logs.stage(t, op, "finalize_wait")
			observation := s.logs.stage(t, op, "observation")
			settlement := s.logs.stage(t, op, "settlement")
			requireAbout(t, "the revocation phase", stageElapsed(revocation), admission)
			requireAtMost(t, "the finalize wait", stageElapsed(wait), 2*waitDelay)
			requireAtMost(t, "the observation read", stageElapsed(observation), admission)
			requireAtMost(t, "the settlement", stageElapsed(settlement), admission)
			if !stageFlag(observation, "read") || !stageFlag(settlement, "recorded") {
				t.Fatalf("observation %v / settlement %v after the release", observation, settlement)
			}
			t.Logf("%s released block: stop %s, revocation %s, finalize_wait %s (finalizer returned %v), "+
				"observation %s, settlement %s; released %s after the block, call returned %s after the release",
				be.name, stageElapsed(stop), stageElapsed(revocation), stageElapsed(wait),
				stageFlag(wait, "finalizer_returned"), stageElapsed(observation), stageElapsed(settlement),
				releasedAt.Sub(hold.heldAt), got.at.Sub(releasedAt))
		})
	}
}

// TestLegacyStopRevocationKeepsItsUnboundedWait is T-T2's other half: the legacy
// operator Stop, under the same block, waits well past the managed bound and
// completes its revocation once the block is released.
func TestLegacyStopRevocationKeepsItsUnboundedWait(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			const bound = 2 * time.Second
			f, runner, src := newBoundedRevocationFixture(t, be.config(t), bound)
			dto, lr := f.plainRun(t, "agent:w2-legacy")
			credential := f.liveWorkCredential(lr)
			if credential.IsZero() {
				t.Fatal("the launch minted no work-session credential")
			}
			_, sibling := f.plainRun(t, "agent:w2-legacy-sibling")
			holds, failures := f.armTokenRowHold(runner, f.liveWorkCredential(sibling))
			done := make(chan error, 1)
			go func() {
				_, err := f.m.stopRun(context.Background(), f.tenant, dto.RunRef, "user:u1", model.ActorUser)
				done <- err
			}()
			hold := awaitHold(t, holds, failures)
			// Nothing waits for the issuer to be ENTERED: on SQLite the legacy phase
			// blocks at the durable handle lookup before it reaches the issuer, and that
			// wait is exactly as unbounded as the issuer's own.
			select {
			case obs := <-src.returned:
				t.Fatalf("the legacy revocation returned under the block after %s: %v",
					obs.ended.Sub(obs.started), obs.err)
			case err := <-done:
				t.Fatalf("the legacy Stop returned under the block: %v", err)
			case <-time.After(bound + 2*time.Second):
			}
			hold.release()
			if err := <-hold.done; err != nil {
				t.Fatalf("the hold transaction failed: %v", err)
			}
			obs := awaitObservation(t, src.returned, "the released legacy revocation")
			if obs.err != nil {
				t.Fatalf("the legacy revocation failed after the release: %v", obs.err)
			}
			if obs.bounded {
				t.Fatal("the legacy Stop revoked inside the bounded phase")
			}
			if waited := obs.ended.Sub(hold.heldAt); waited < bound+2*time.Second {
				t.Fatalf("the legacy revocation ended only %s after the block", waited)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("legacy stop: %v", err)
				}
			case <-time.After(60 * time.Second):
				t.Fatal("the legacy Stop did not return after the release")
			}
			if retained := f.liveWorkCredential(lr); !retained.IsZero() {
				t.Fatalf("a completed revocation kept the live handle %q", retained)
			}
			if durable := f.durableWorkCredential(dto.RunRef); durable != "" {
				t.Fatalf("a completed revocation kept the durable handle %q", durable)
			}
			if !f.tokenRevoked(credential) {
				t.Fatal("the legacy revocation completed and the token is not revoked")
			}
			t.Logf("%s: legacy revocation ended %s after the block, once released",
				be.name, obs.ended.Sub(hold.heldAt))
		})
	}
}

type revocationTestKey struct{}

// TestRevocationContextIsUnchangedForLegacyAndSharedInTheManagedPhase pins the
// helper's two shapes: unmarked, exactly context.WithoutCancel; inside a managed
// phase, the SAME context at every site, so all sites share one deadline.
func TestRevocationContextIsUnchangedForLegacyAndSharedInTheManagedPhase(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), revocationTestKey{}, "kept"))
	legacy := revocationContext(parent)
	cancel()
	if legacy.Err() != nil {
		t.Fatal("an unmarked revocation inherited the caller's cancellation")
	}
	if _, ok := legacy.Deadline(); ok {
		t.Fatal("an unmarked revocation acquired a deadline")
	}
	if legacy.Value(revocationTestKey{}) != "kept" {
		t.Fatal("an unmarked revocation lost the caller's values")
	}
	if got, release := legacyRevocationPhase(parent)(); got != parent {
		t.Fatal("the legacy phase did not hand back the caller's own context")
	} else {
		release()
	}

	caller, callerCancel := context.WithTimeout(context.Background(), time.Hour)
	phase, phaseCancel := managedRevocationPhase(caller, 80*time.Millisecond)()
	defer phaseCancel()
	callerCancel()
	if phase.Err() != nil {
		t.Fatal("the managed phase inherited the caller's cancellation")
	}
	workSite, handleSite := revocationContext(phase), revocationContext(phase)
	if workSite != phase || handleSite != phase {
		t.Fatal("a site inside the managed phase detached from the phase deadline")
	}
	deadline, ok := phase.Deadline()
	if !ok || time.Until(deadline) > 80*time.Millisecond {
		t.Fatalf("phase deadline = %v (%t), want the bound from phase start", deadline, ok)
	}
	<-phase.Done()
	if !errors.Is(phase.Err(), context.DeadlineExceeded) {
		t.Fatalf("phase ended with %v, want its own deadline", phase.Err())
	}
	if _, marked := context.Background().Value(boundedRevocationKey{}).(struct{}); marked {
		t.Fatal("an unrelated context carries the bounded marker")
	}
}
