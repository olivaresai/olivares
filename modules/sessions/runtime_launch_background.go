// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// runtime_launch_background.go lets a long worktree setup outlive the request that
// created the session.
//
// A start that has a setup command to run still answers like any other start when the
// setup is quick: the request waits for it, up to worktreeSetupInlineWait, and gets the
// running session or the setup's refusal. A setup that is still running when that wait
// ends is the session's: the request answers with the pending session, and the setup,
// the session's own start and its first turn follow in order. Nothing about the
// published create contract changes for a setup that finishes in time.
//
// The session is supervised from the moment its row exists. Its process is a
// preparingProcess: Stop, the kill switch, shutdown, the claim and credential
// heartbeat, the bridge and the final settlement all reach the setup through the
// same handle that later reaches the tool, so there is one custody path and no second
// lifecycle. The first input of a session waits for that start to settle
// (awaitLaunch), outside the run's operation lock, so a stop is never blocked by it.

// worktreeSetupInlineWait is how long a start waits for its setup before it answers with
// the pending session. ponytail: it is the former setup limit, which kept the create
// request inside the API's 60 s write timeout (30 s of other launch preparation + 20 s +
// 10 s of response headroom); upgrade trigger: a measured response budget that differs.
var worktreeSetupInlineWait = 20 * time.Second

// syncSetupLimit bounds a setup that a request waits for to its end (a resume, or an
// approved queued start): the session does not own it, so it never outlives the request's
// response budget. ponytail: only a create hands a long setup to its session; upgrade
// trigger: retrying a long installer by resume, which then needs a resuming run to answer
// pending (a state no resume answers today).
func syncSetupLimit() time.Duration {
	return min(worktreeSetupTimeout, worktreeSetupInlineWait)
}

// preparingProcess is the Process of a session whose start is still running its
// setup. Output, Wait and Stop answer for whatever the start resolves to: the tool's
// process, a leftover process of a failed setup that stays in custody, or nothing.
type preparingProcess struct {
	// cancelSetup ends the setup and the start's own checks; it never ends the tool.
	cancelSetup context.CancelFunc
	out         chan OutputFrame
	// ready closes once the start resolved to a process or to none; settled closes once
	// the whole start ended: the launch transition committed, or the start failed.
	ready   chan struct{}
	settled chan struct{}

	mu       sync.Mutex
	inner    Process
	resolved bool
}

func newPreparingProcess(cancelSetup context.CancelFunc) *preparingProcess {
	return &preparingProcess{
		cancelSetup: cancelSetup,
		out:         make(chan OutputFrame),
		ready:       make(chan struct{}),
		settled:     make(chan struct{}),
	}
}

// resolve binds the start's outcome once. A nil process means there is none: the
// bridge sees the output end and the wait return, and settles the row.
func (p *preparingProcess) resolve(proc Process) {
	p.mu.Lock()
	if p.resolved {
		p.mu.Unlock()
		return
	}
	p.resolved, p.inner = true, proc
	p.mu.Unlock()
	close(p.ready)
	if proc == nil {
		close(p.out)
		return
	}
	go func() {
		for frame := range proc.Output() {
			p.out <- frame
		}
		close(p.out)
	}()
}

func (p *preparingProcess) current() Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inner
}

func (p *preparingProcess) Send(ctx context.Context, line []byte) error {
	if inner := p.current(); inner != nil {
		return inner.Send(ctx, line)
	}
	return errors.New("sessions: stdin write not attempted: the session is still preparing its worktree")
}

func (p *preparingProcess) Output() <-chan OutputFrame { return p.out }

func (p *preparingProcess) Wait() (int, error) {
	<-p.ready
	if inner := p.current(); inner != nil {
		return inner.Wait()
	}
	return -1, nil
}

// Stop ends the setup and then the process the start resolved to. A start that has
// not resolved when the caller gives up is reported as not reaped, so the caller keeps
// the session's custody and retries the same handle.
func (p *preparingProcess) Stop(ctx context.Context) error {
	p.cancelSetup()
	select {
	case <-p.ready:
	case <-ctx.Done():
		return fmt.Errorf("sessions: the worktree setup did not end before the stop gave up: %w", ErrChildNotReaped)
	}
	if inner := p.current(); inner != nil {
		return inner.Stop(ctx)
	}
	return nil
}

func (p *preparingProcess) PID() int {
	if inner := p.current(); inner != nil {
		return inner.PID()
	}
	return 0
}

// OutputIncomplete and WorktreeSetupCleanup forward the optional evidence the
// settlement reads from a process. Without a process there is nothing incomplete and
// no setup tree left.
func (p *preparingProcess) OutputIncomplete() bool {
	reporter, ok := p.current().(outputCompletionReporter)
	return ok && reporter.OutputIncomplete()
}

func (p *preparingProcess) WorktreeSetupCleanup() (failed, known bool) {
	outcome, ok := p.current().(interface{ WorktreeSetupCleanup() (failed, known bool) })
	if !ok {
		return false, true
	}
	return outcome.WorktreeSetupCleanup()
}

// liveProcess is the process a live run is running now: the tool's, once its start
// resolved to it. Callers that need the concrete process (the preview dialer, the
// command catalog's response routing) ask here instead of the run's handle.
func liveProcess(lr *liveRun) Process {
	if pp, ok := lr.proc.(*preparingProcess); ok {
		if inner := pp.current(); inner != nil {
			return inner
		}
	}
	return lr.proc
}

// launchInputWait is how long an input waits for its session's start. It stays inside the
// API's 60 s write timeout, so a caller gets an answer instead of a dropped connection;
// the console holds its first message and sends it when the session is ready.
var launchInputWait = 30 * time.Second

// awaitLaunch holds a first input until the session's start settled, or launchInputWait
// has passed: an input that waited in vain meets the ordinary refusal of a session that
// is not running yet (409, state=pending), so it never reaches a half-prepared folder.
// A session whose start already settled, or that was never prepared in the background,
// is not held. It runs before the run's operation lock is taken: a stop must reach the
// setup while an input waits for it.
func (m *Module) awaitLaunch(ctx context.Context, tenant model.TenantID, runRef string) error {
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		return nil
	}
	pp, ok := lr.proc.(*preparingProcess)
	if !ok {
		return nil
	}
	timer := time.NewTimer(launchInputWait)
	defer timer.Stop()
	select {
	case <-pp.settled:
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backgroundLaunch is a start whose setup may outlive its request: everything the
// start needs once the session's spec is final, and finish, the part of the launch
// that follows the tool's process (handshake, launch transition, settlement).
type backgroundLaunch struct {
	lr          *liveRun
	pp          *preparingProcess
	setupCtx    context.Context
	cancelSetup context.CancelFunc
	ctx         context.Context // the request's: its values, and its cancellation inside the wait
	wctx        context.Context
	runCtx      context.Context
	tenant      model.TenantID
	runRef      string
	p           CreateRunParams
	decision    childDecision
	spec        LaunchSpec
	finish      func(proc Process) (runDTO, error)
	detached    atomic.Bool // the request's wait is over: its cancellation no longer reaches the start
}

type launchResult struct {
	dto runDTO
	err error
}

// newBackgroundLaunch makes the start's own setup context. It is cancelled by the
// session's stop (through the preparingProcess) and by the session's teardown (runCtx).
func newBackgroundLaunch(ctx, runCtx context.Context) *backgroundLaunch {
	setupCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	context.AfterFunc(runCtx, cancel)
	return &backgroundLaunch{
		pp: newPreparingProcess(cancel), setupCtx: setupCtx, cancelSetup: cancel, ctx: ctx, runCtx: runCtx,
	}
}

// cancelled is the check before the tool starts: the setup's own context, and the
// request's while the request is still being waited for.
func (b *backgroundLaunch) cancelled() error {
	if err := b.setupCtx.Err(); err != nil {
		return err
	}
	if !b.detached.Load() {
		return b.ctx.Err()
	}
	return nil
}

// runLaunchInBackground runs the start and answers like a synchronous one when it
// settles within worktreeSetupInlineWait; otherwise it answers with the pending
// session and the start continues on its own.
func (m *Module) runLaunchInBackground(b *backgroundLaunch) (runDTO, error) {
	result := make(chan launchResult, 1)
	go func() {
		defer close(b.pp.settled)
		defer b.lr.cierraVentanaYVuelca()
		defer b.pp.resolve(nil) // a start that ends without an outcome must not strand the bridge
		result <- m.runBackgroundLaunch(b)
	}()
	defer b.detached.Store(true)
	timer := time.NewTimer(worktreeSetupInlineWait)
	defer timer.Stop()
	select {
	case r := <-result:
		return r.dto, r.err
	case <-timer.C:
		return m.getRun(b.wctx, b.tenant, b.runRef)
	case <-b.ctx.Done():
		// The request left inside its wait: the start fails, as a start that waits does.
		b.cancelSetup()
		r := <-result
		return r.dto, r.err
	}
}

func (m *Module) runBackgroundLaunch(b *backgroundLaunch) launchResult {
	proc, err := m.runWorktreeSetup(b.setupCtx, b.tenant, b.runRef, b.p, b.spec, b.decision.ws, worktreeSetupTimeout)
	if err != nil {
		return m.failBackgroundLaunch(b, proc, worktreeSetupStage, err)
	}
	// The tool store may be replaced while a setup runs; the lock covers only the tool's
	// own start, as it covers the whole start of a request that waits.
	m.rt.toolMu.RLock()
	defer m.rt.toolMu.RUnlock()
	proc, stage, err := m.startChildProcess(b.setupCtx, b.runCtx, b.tenant, b.runRef, &b.p, b.decision.ws, b.spec, b.cancelled)
	if err != nil || proc == nil {
		return m.failBackgroundLaunch(b, proc, stage, err)
	}
	b.pp.resolve(proc)
	dto, err := b.finish(proc)
	return launchResult{dto, err}
}

// failBackgroundLaunch ends a start that did not reach a running session. The final
// settlement is the bridge's: it releases the claim, revokes the credentials and writes
// the failed (or, for a stop that asked for it, stopped) row, as for any other process.
// A process the failed setup left behind is the session's process until it is gone.
func (m *Module) failBackgroundLaunch(b *backgroundLaunch, proc Process, stage string, lerr error) launchResult {
	failure := launchFailedErr(stage, lerr)
	m.warnf("session launch failed", "run_ref", b.runRef, "reason", launchLogReason(stage, lerr))
	lr := b.lr
	lr.mu.Lock()
	lr.launchErr = lerr
	lr.setupProcess = stage == worktreeSetupStage
	if !lr.stopRequested {
		lr.stopRequested, lr.stopReason = true, "launch_error"
		lr.launchFailed, lr.failureDetail = true, failure.msg
	}
	lr.mu.Unlock()
	b.pp.resolve(proc)
	if proc == nil {
		m.awaitFinalize(lr)
		return launchResult{err: failure}
	}
	stopped, stopErr := m.teardownLiveWithContext(b.wctx, lr)
	if stopped {
		m.awaitFinalize(lr)
	}
	return launchResult{err: errors.Join(failure, stopErr)}
}
