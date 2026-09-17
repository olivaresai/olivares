// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_observation_test.go (P2 / W2) — Phase G reports only what was
// observed, and every stage past the dispatch fence owns its bound
// (ROOT-CORRECTION-1). Each negative crosses the actual StopManagedRun.

// managedStopAnswer is one StopManagedRun result and when it returned.
type managedStopAnswer struct {
	res ManagedStopResult
	err error
	at  time.Time
}

// scriptedRunner launches in-memory processes whose Stop and Wait behave as the
// next launch was scripted to: exit, ignore Stop, or exit with an unconfirmed Wait.
type scriptedRunner struct {
	mu       sync.Mutex
	procs    []*fakeProc
	onStop   func()
	waitErr  error
	stubborn bool
}

type scriptedProc struct {
	*fakeProc
	runner   *scriptedRunner
	waitErr  error
	stubborn bool
}

func (p scriptedProc) Wait() (int, error) {
	code, _ := p.fakeProc.Wait()
	return code, p.waitErr
}

func (p scriptedProc) Stop(context.Context) error {
	p.runner.mu.Lock()
	hook := p.runner.onStop
	p.runner.onStop = nil
	p.runner.mu.Unlock()
	if hook != nil {
		hook()
	}
	if !p.stubborn {
		p.finish(143)
	}
	return nil
}

func (r *scriptedRunner) Launch(_ context.Context, _ LaunchSpec) (Process, error) {
	p := &fakeProc{out: make(chan OutputFrame, 16), stopped: make(chan struct{})}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.procs = append(r.procs, p)
	return scriptedProc{fakeProc: p, runner: r, waitErr: r.waitErr, stubborn: r.stubborn}, nil
}

// script sets how the NEXT launched process behaves.
func (r *scriptedRunner) script(waitErr error, stubborn bool) {
	r.mu.Lock()
	r.waitErr, r.stubborn = waitErr, stubborn
	r.mu.Unlock()
}

func (r *scriptedRunner) setStopHook(hook func()) {
	r.mu.Lock()
	r.onStop = hook
	r.mu.Unlock()
}

func (r *scriptedRunner) finishAll() {
	r.mu.Lock()
	procs := append([]*fakeProc(nil), r.procs...)
	r.mu.Unlock()
	for _, p := range procs {
		p.finish(0)
	}
}

func newScriptedStopFixture(t *testing.T, cfg store.Config, admission time.Duration) (*managedStopFixture, *scriptedRunner) {
	t.Helper()
	runner := &scriptedRunner{}
	f := newManagedStopFixtureWith(t, cfg,
		WithRunner(runner), WithCredentialSource(staticCred()),
		WithProductVersion("test"), WithStopWaitDelay(time.Second),
		WithManagedStopAdmissionTimeout(admission),
	)
	t.Cleanup(runner.finishAll)
	return f, runner
}

// hookedProcRunner launches REAL children through the product's process runner and
// runs a one-shot hook when a child is told to stop, immediately before the real
// Stop.
type hookedProcRunner struct {
	inner  Runner
	mu     sync.Mutex
	onStop func()
}

type hookedProc struct {
	Process
	runner *hookedProcRunner
}

func (p hookedProc) Stop(ctx context.Context) error {
	p.runner.mu.Lock()
	hook := p.runner.onStop
	p.runner.onStop = nil
	p.runner.mu.Unlock()
	if hook != nil {
		hook()
	}
	return p.Process.Stop(ctx)
}

func (r *hookedProcRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	proc, err := r.inner.Launch(ctx, spec)
	if err != nil {
		return nil, err
	}
	return hookedProc{Process: proc, runner: r}, nil
}

func (r *hookedProcRunner) setStopHook(hook func()) {
	r.mu.Lock()
	r.onStop = hook
	r.mu.Unlock()
}

// newRealChildStopFixture composes the module over REAL supervised codex children
// with a configured admission timeout T.
func newRealChildStopFixture(t *testing.T, cfg store.Config, admission time.Duration) (*managedStopFixture, *hookedProcRunner) {
	t.Helper()
	runner := &hookedProcRunner{inner: NewProcRunner()}
	f := newManagedStopFixtureWith(t, cfg,
		WithRunner(runner),
		WithProviderDriver(NewCodexDriver()),
		WithDriverProgram(providerDriverCodex, os.Args[0]),
		WithProductVersion("test"),
		WithStopWaitDelay(2*time.Second),
		WithDriverTimeouts(20*time.Second, 2*time.Second),
		WithManagedStopAdmissionTimeout(admission),
	)
	return f, runner
}

// managedStopStageLog captures the module's own stage-duration records.
type managedStopStageLog struct {
	mu      sync.Mutex
	records []map[string]any
}

// captureManagedStopStages installs the capture as the module logger. It must run
// before any launch, while nothing else reads the logger.
func captureManagedStopStages(m *Module) *managedStopStageLog {
	l := &managedStopStageLog{}
	m.log = slog.New(l)
	return l
}

func (l *managedStopStageLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *managedStopStageLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *managedStopStageLog) WithGroup(string) slog.Handler            { return l }

func (l *managedStopStageLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "sessions: managed stop stage" {
		return nil
	}
	rec := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		rec[a.Key] = a.Value.Any()
		return true
	})
	l.mu.Lock()
	l.records = append(l.records, rec)
	l.mu.Unlock()
	return nil
}

func (l *managedStopStageLog) stage(t *testing.T, operationRef, name string) map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.records {
		if r["operation_ref"] == operationRef && r["stage"] == name {
			return r
		}
	}
	t.Fatalf("no %s stage was logged for %s", name, operationRef)
	return nil
}

func stageElapsed(rec map[string]any) time.Duration {
	ms, _ := rec["elapsed_ms"].(int64)
	return time.Duration(ms) * time.Millisecond
}

func stageFlag(rec map[string]any, key string) bool {
	b, _ := rec[key].(bool)
	return b
}

// requireAbout asserts a stage ENDED AT its bound: it waited, and not much longer.
func requireAbout(t *testing.T, what string, got, bound time.Duration) {
	t.Helper()
	if got < bound-300*time.Millisecond || got > bound+5*time.Second {
		t.Fatalf("%s took %s, want about its own bound %s", what, got, bound)
	}
}

// requireAtMost asserts a stage stayed within its bound.
func requireAtMost(t *testing.T, what string, got, bound time.Duration) {
	t.Helper()
	if got > bound+2*time.Second {
		t.Fatalf("%s took %s, beyond its own bound %s", what, got, bound)
	}
}

func (f *managedStopFixture) exactLaunchObservation(runRef string, launch model.ID) string {
	f.t.Helper()
	ctx := context.Background()
	var obs string
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		var err error
		obs, err = readExactLaunchObservation(ctx, sc, runRef, launch)
		return err
	}); err != nil {
		f.t.Fatalf("read the P1 of %s: %v", runRef, err)
	}
	return obs
}

// requireUncertainStop is the shared assertion for a stop that crossed the process
// boundary without an observed exit of the expected launch.
func (f *managedStopFixture) requireUncertainStop(t *testing.T, res ManagedStopResult, err error, opID, observation string) {
	t.Helper()
	if err != nil {
		t.Fatalf("StopManagedRun: %v (%+v)", err, res)
	}
	if res.Outcome != ManagedStopUnknown || res.ProcessOutcome != managedStopUnverified ||
		res.Observation != observation || !res.Attempted {
		t.Fatalf("result = %+v, want an attempted unknown stop, unverified, observation %q", res, observation)
	}
	if res.Settlement != model.EvidenceOpUnknown {
		t.Fatalf("settlement = %q, want unknown: only an observed exit settles completed", res.Settlement)
	}
	if row, found := f.journal(opID); !found || row.State != model.EvidenceOpUnknown {
		t.Fatalf("journal = %+v found=%t, want the durable unknown settlement", row, found)
	}
}

func awaitFinalized(t *testing.T, lr *liveRun) {
	t.Helper()
	select {
	case <-lr.finalizedCh:
	case <-time.After(30 * time.Second):
		t.Fatal("the finalizer did not return")
	}
}

// TestManagedStopProcessOutcomeFollowsTheRatifiedClassification pins correction 1
// §12.3 over the actual error shapes the stop path produces.
func TestManagedStopProcessOutcomeFollowsTheRatifiedClassification(t *testing.T) {
	wrap := func(err error) error { return secretSafeCredentialError("session process stop", err) }
	abandoned := wrap(fmt.Errorf("stop: %w", ErrOutputAbandoned))
	notReaped := wrap(fmt.Errorf("stop: %w", ErrChildNotReaped))
	other := wrap(errors.New("signal refused"))
	cases := []struct {
		observation string
		stopErr     error
		want        string
	}{
		{obsProcessExitObserved, nil, managedStopExitObserved},
		{obsProcessExitObserved, abandoned, managedStopExitObserved},
		{obsProcessExitObserved, notReaped, managedStopNotReaped},
		{obsProcessExitObserved, other, managedStopUnverified},
		{obsProcessWaitUnverified, nil, managedStopUnverified},
		{obsHandleLostUnconfirmed, nil, managedStopUnverified},
		{"", nil, managedStopUnverified},
		{"", notReaped, managedStopNotReaped},
	}
	for _, c := range cases {
		if got := managedStopProcessOutcome(c.observation, c.stopErr); got != c.want {
			t.Errorf("managedStopProcessOutcome(%q, %v) = %q, want %q", c.observation, c.stopErr, got, c.want)
		}
	}
	failure := errors.New("issuer unavailable")
	words := []struct {
		err  error
		had  bool
		want string
	}{
		{nil, false, managedStopRevokeNotUsed},
		{nil, true, managedStopRevoked},
		{failure, false, managedStopRevokeFailed},
		{failure, true, managedStopRevokeFailed},
	}
	for _, w := range words {
		if got := managedStopRevocationWord(w.err, w.had); got != w.want {
			t.Errorf("managedStopRevocationWord(%v, %t) = %q, want %q", w.err, w.had, got, w.want)
		}
	}
}

// TestManagedStopScriptedExitIsObserved is the positive control for the scripted
// negatives below: the same harness CAN produce an observed exit, so their
// uncertainty is caused by what each one scripts.
func TestManagedStopScriptedExitIsObserved(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newScriptedStopFixture(t, be.config(t), 10*time.Second)
			op := f.operator("scripted-exit@w2.test", auth.RoleEditor, true, 2*time.Minute)
			runner.script(nil, false)
			dto, lr := f.plainRun(t, "agent:w2-scripted-exit")
			res, err := f.call(f.request(op, dto, lr.launchID, "scripted-exit"))
			if err != nil {
				t.Fatalf("StopManagedRun: %v", err)
			}
			if res.Outcome != ManagedStopStopped || res.ProcessOutcome != managedStopExitObserved ||
				res.Observation != obsProcessExitObserved || res.Settlement != model.EvidenceOpCompleted {
				t.Fatalf("result = %+v, want stopped on the exact-launch observed exit", res)
			}
			if row, found := f.journal("scripted-exit"); !found || row.State != model.EvidenceOpCompleted {
				t.Fatalf("journal = %+v found=%t, want completed", row, found)
			}
		})
	}
}

// TestManagedStopFinalizeTimeoutWithNilStopErrorIsUnknown: Stop returned nil, the
// finalizer never returned within its wait, and no P1 exists. That is uncertainty.
func TestManagedStopFinalizeTimeoutWithNilStopErrorIsUnknown(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newScriptedStopFixture(t, be.config(t), 10*time.Second)
			op := f.operator("timeout@w2.test", auth.RoleEditor, true, 2*time.Minute)
			runner.script(nil, true)
			dto, lr := f.plainRun(t, "agent:w2-timeout")
			res, err := f.call(f.request(op, dto, lr.launchID, "timeout-1"))
			select {
			case <-lr.finalizedCh:
				t.Fatal("the child finalized; this case needs the finalize wait to time out")
			default:
			}
			f.requireUncertainStop(t, res, err, "timeout-1", "")
		})
	}
}

// TestManagedStopChannelClosureWithoutExactLaunchP1IsUnverified: the finalizer
// returns and closes its channel, but its incarnation check fails and it publishes
// no terminal observation. A closed channel is not a process fact.
func TestManagedStopChannelClosureWithoutExactLaunchP1IsUnverified(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newScriptedStopFixture(t, be.config(t), 10*time.Second)
			op := f.operator("no-p1@w2.test", auth.RoleEditor, true, 2*time.Minute)
			runner.script(nil, false)
			dto, lr := f.plainRun(t, "agent:w2-no-p1")
			runner.setStopHook(func() {
				ctx := context.Background()
				if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(runKind)
					if err != nil {
						return err
					}
					rec, err := findRunRec(ctx, repo, dto.RunRef)
					if err != nil {
						return err
					}
					rec[colRuntimeLaunchID] = model.NewID().String()
					_, err = repo.Update(ctx, rec)
					return err
				}); err != nil {
					t.Errorf("move the durable incarnation: %v", err)
				}
			})
			res, err := f.call(f.request(op, dto, lr.launchID, "no-p1"))
			awaitFinalized(t, lr)
			if p1 := f.exactLaunchObservation(dto.RunRef, lr.launchID); p1 != "" {
				t.Fatalf("a P1 %q was committed; this case needs the finalizer to skip it", p1)
			}
			f.requireUncertainStop(t, res, err, "no-p1", "")
		})
	}
}

// TestManagedStopWaitUnverifiedIsUnknown: the finalizer publishes P1, and P1 says
// the Wait was not confirmed. The observation is reported and the stop stays unknown.
func TestManagedStopWaitUnverifiedIsUnknown(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newScriptedStopFixture(t, be.config(t), 10*time.Second)
			op := f.operator("wait-unverified@w2.test", auth.RoleEditor, true, 2*time.Minute)
			runner.script(errors.New("wait was not confirmed"), false)
			dto, lr := f.plainRun(t, "agent:w2-wait-unverified")
			res, err := f.call(f.request(op, dto, lr.launchID, "wait-unverified"))
			awaitFinalized(t, lr)
			f.requireUncertainStop(t, res, err, "wait-unverified", obsProcessWaitUnverified)
		})
	}
}

// TestManagedStopCallerDeadlineBeyondAdmissionIsNotTheWindow: a caller with an
// hour-long deadline does not buy an hour-long admission. Another operation holds
// the run token; the managed Stop gives up when the configured admission timeout
// ends, with nothing written and nothing dispatched.
func TestManagedStopCallerDeadlineBeyondAdmissionIsNotTheWindow(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			const admission = 3 * time.Second
			f, runner := newScriptedStopFixture(t, be.config(t), admission)
			op := f.operator("deadline@w2.test", auth.RoleEditor, true, 2*time.Minute)
			runner.script(nil, false)
			dto, lr := f.plainRun(t, "agent:w2-deadline")
			release, err := f.m.rt.lockRunContext(context.Background(), liveKey(f.tenant, dto.RunRef))
			if err != nil {
				t.Fatalf("hold the run token: %v", err)
			}
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(release) })

			req := f.request(op, dto, lr.launchID, "deadline-1")
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			started := time.Now()
			done := make(chan managedStopAnswer, 1)
			go func() {
				res, err := f.m.StopManagedRun(ctx, f.tenant, req.Question, req.ManagedStopRequest)
				done <- managedStopAnswer{res, err, time.Now()}
			}()
			var got managedStopAnswer
			select {
			case got = <-done:
			case <-time.After(admission + 30*time.Second):
				releaseOnce.Do(release)
				<-done
				t.Fatal("the managed Stop kept waiting for the run token past its configured admission timeout")
			}
			f.refuseWithNoJournalRow(got.res, got.err, "deadline-1", ManagedStopCanceled)
			if waited := got.at.Sub(started); waited < admission-300*time.Millisecond || waited > admission+5*time.Second {
				t.Fatalf("the refusal came after %s, want about the admission timeout %s", waited, admission)
			}
			if state := f.runState(dto.RunRef); state != stateRunning {
				t.Fatalf("a refused admission changed the run to %q", state)
			}
		})
	}
}

// TestManagedStopCancellationAfterDispatchStillObservesAndSettles: the caller walks
// away the moment a REAL child is told to stop. Past the dispatch fence that
// changes nothing: the finalize wait, the P1 read and the settlement each run under
// their own bound, and the observed exit is recorded completed.
func TestManagedStopCancellationAfterDispatchStillObservesAndSettles(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newRealChildStopFixture(t, be.config(t), 10*time.Second)
			op := f.operator("cancel-after@w2.test", auth.RoleEditor, true, 2*time.Minute)
			dto, lr := f.launch("thread-cancel-after-dispatch")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			runner.setStopHook(cancel)
			req := f.request(op, dto, lr.launchID, "cancel-after")
			res, err := f.m.StopManagedRun(ctx, f.tenant, req.Question, req.ManagedStopRequest)
			if ctx.Err() == nil {
				t.Fatal("the caller was not canceled at the process boundary")
			}
			if err != nil {
				t.Fatalf("StopManagedRun: %v (%+v)", err, res)
			}
			if res.Outcome != ManagedStopStopped || res.ProcessOutcome != managedStopExitObserved ||
				res.Observation != obsProcessExitObserved || res.Settlement != model.EvidenceOpCompleted {
				t.Fatalf("result after a post-dispatch cancellation = %+v, want the observed exit settled", res)
			}
			if row, found := f.journal("cancel-after"); !found || row.State != model.EvidenceOpCompleted {
				t.Fatalf("journal = %+v found=%t, want completed", row, found)
			}
			if processRunning(lr.proc.PID()) {
				t.Fatal("the supervised child is still running")
			}
		})
	}
}
