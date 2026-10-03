// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// runtime_killswitch.go is the ACTIVE half of the estate kill-switch over
// operated sessions: the "para" the prompt asks for. Every other actuation surface in
// the product is block-on-next-action only (the StopGate denies a NEW fire/open/apply),
// because those modules do not own a long-running handle to in-flight work. Sessions is
// different — it OWNS the supervised `claude` process (Process.Stop) — so an emergency
// stop can TERMINATE a running session, not merely block the next launch.
//
// Mechanism: a periodic sweep re-reads the live registry and consults the SAME StopGate
// the launch pre-flight uses (the governance kill-switch state). A run that is now
// under a stop (estate, or its agent graduation) is terminated; a StopGate read error is
// treated as STOPPED (deny-closed, the StopGate contract). It composes with the inline
// PEP, which already denies every tool-call a still-running session attempts under a stop
// — so a stopped session is neutralized at the tool boundary IMMEDIATELY and its process
// is reclaimed within one sweep interval (the documented, bounded latency). Recovery is
// the governed dual-control re-enable; the sweep never resurrects, only stops.

// startStopSweep launches the shared active lifecycle sweep when kill-switch
// checking or the owner-standing check is bound. It
// runs under a background context (NOT Start's ctx) so it outlives the boot call, and is
// canceled by Stop. Idempotent: a second call without a Stop is a no-op.
func (m *Module) startStopSweep() {
	if m.rt == nil || (m.rt.stopSweepInterval <= 0 && m.rt.sessionAccessCheck == nil) {
		return
	}
	m.mu.Lock()
	if m.rt.sweepCancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.rt.sweepCancel = cancel
	interval := m.rt.stopSweepInterval
	// Owner passes are scheduled every five seconds even when emergency-stop
	// sweeping is disabled. Keep one timer and preserve the kill-switch cadence.
	if m.rt.sessionAccessCheck != nil && (interval <= 0 || interval > 5*time.Second) {
		interval = 5 * time.Second
	}
	m.mu.Unlock()
	go m.runStopSweep(ctx, interval)
	if m.log != nil {
		m.log.Info("sessions: active access and emergency-stop sweep started", "interval", interval.String())
	}
}

// stopStopSweep cancels the sweep loop (called from Stop).
func (m *Module) stopStopSweep() {
	m.mu.Lock()
	cancel := m.rt.sweepCancel
	m.rt.sweepCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runStopSweep uses one timer for both checks. It schedules their exact due
// times, so the owner cadence never rounds or delays a configured emergency check.
func (m *Module) runStopSweep(ctx context.Context, interval time.Duration) {
	now := time.Now()
	var nextAccess, nextKill time.Time
	if m.rt.sessionAccessCheck != nil {
		nextAccess = now.Add(interval)
	}
	if m.rt.stopSweepInterval > 0 {
		nextKill = now.Add(m.rt.stopSweepInterval)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	var ownerDone <-chan struct{}
	for {
		next := nextAccess
		if next.IsZero() || (!nextKill.IsZero() && nextKill.Before(next)) {
			next = nextKill
		}
		timer.Reset(time.Until(next))
		select {
		case <-ctx.Done():
			return
		case <-ownerDone:
			ownerDone = nil
		case now = <-timer.C:
			if !nextKill.IsZero() && !now.Before(nextKill) {
				m.sweepKillSwitch(ctx)
				nextKill = advanceStopSweep(nextKill, m.rt.stopSweepInterval)
			}
			if !nextAccess.IsZero() && !now.Before(nextAccess) {
				// A slow owner read must not block an emergency deadline. At most
				// one pass runs; missed owner ticks never accumulate queued work.
				if ownerDone == nil {
					done := make(chan struct{})
					ownerDone = done
					go func() {
						defer close(done)
						m.sweepSessionAccess(ctx)
					}()
				}
				nextAccess = advanceStopSweep(nextAccess, interval)
			}
		}
	}
}

// Skip missed deadlines without replacing either check's configured cadence.
func advanceStopSweep(next time.Time, interval time.Duration) time.Time {
	now := time.Now()
	if !next.After(now) {
		next = next.Add((now.Sub(next)/interval + 1) * interval)
	}
	return next
}

const ownerAccessReadGrace = 5 * time.Minute

// sweepSessionAccess checks idle as well as active runs without holding registry
// locks over store reads. A proven withdrawal stops immediately; read failures
// retry with backoff for a bounded grace. Admission still requires a valid read.
func (m *Module) sweepSessionAccess(ctx context.Context) {
	if m.rt.sessionAccessCheck == nil {
		return
	}
	for _, lr := range m.rt.snapshotLive() {
		startedAt := m.now()
		lr.mu.Lock()
		skip := lr.stopRequested || lr.finalized || startedAt.Before(lr.ownerAccessRetryAt)
		lr.mu.Unlock()
		if skip {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		scope, user, err := m.rt.sessionAccessCheck(checkCtx, lr.tenant, lr.runRef)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, auth.ErrSessionAccessEnded) {
			// A proven offboard is already a terminal cause. Give its exact
			// generation bounded teardown admission after the read deadline;
			// fallback must preserve that cause on the captured process too.
			stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			err = m.StopForAccessEnded(stopCtx, scope, user)
			stopCancel()
			if err != nil {
				m.terminateForRuntimeAccessFailure(lr, "Access ended for "+accessLossUser(user), true)
			}
			continue
		}
		lr.mu.Lock()
		if lr.stopRequested || lr.finalized {
			lr.mu.Unlock()
			continue
		}
		// An external PEP or a legacy run may have no owner scope in this
		// issuer. That is neither an unavailable standing read nor withdrawal.
		// Credential admission still refuses this unauthenticated result.
		if err == nil || errors.Is(err, auth.ErrSessionOwnerUnbound) {
			lr.ownerAccessFailureSince = time.Time{}
			lr.ownerAccessRetryAt = time.Time{}
			lr.ownerAccessAttempts = 0
			lr.mu.Unlock()
			continue
		}
		now := m.now()
		if lr.ownerAccessFailureSince.IsZero() {
			lr.ownerAccessFailureSince = now
		}
		lr.ownerAccessAttempts++
		attempt := lr.ownerAccessAttempts
		graceEnd := lr.ownerAccessFailureSince.Add(ownerAccessReadGrace)
		expired := !now.Before(graceEnd)
		backoff := 5 * time.Second
		for n := 1; n < attempt && backoff < 30*time.Second; n++ {
			backoff *= 2
		}
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		lr.ownerAccessRetryAt = now.Add(backoff)
		if lr.ownerAccessRetryAt.After(graceEnd) {
			lr.ownerAccessRetryAt = graceEnd
		}
		lr.mu.Unlock()
		// The reader may carry store paths or credentials in its error text.
		// Log the cause class, never that untrusted text.
		cause := "read_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			cause = "deadline_exceeded"
		} else if errors.Is(err, context.Canceled) {
			cause = "canceled"
		}
		m.warnf("sessions: owner access check failed", "run_ref", lr.runRef, "attempt", attempt, "cause", cause)
		if expired {
			m.terminateForRuntimeAccessFailure(lr, "owner access could not be checked for 5 minutes", false)
		}
	}
}

// sweepKillSwitch terminates every live run now frozen by an emergency stop. It snapshots
// the registry (the StopGate read + Process.Stop must not run under the registry lock),
// skips runs already stopping/finalized, and treats a StopGate error as stopped.
func (m *Module) sweepKillSwitch(ctx context.Context) {
	for _, lr := range m.rt.snapshotLive() {
		lr.mu.Lock()
		skip := lr.stopRequested || lr.finalized
		lr.mu.Unlock()
		if skip {
			continue
		}
		dec, err := m.rt.stopGate.Check(ctx, lr.tenant, StopDims{RunRef: lr.runRef, AgentRef: lr.agentRef})
		if err == nil && !dec.Stopped {
			continue
		}
		stopRef := dec.StopRef
		if err != nil {
			// Deny-closed: an unreadable stop state never means "keep running".
			stopRef = "state-unreadable"
		}
		m.terminateForKillSwitch(ctx, lr, stopRef)
	}
}

// snapshotLive returns a point-in-time copy of the live handles so the caller can act on
// each without holding the registry lock across a blocking Process.Stop.
func (rt *runtimeState) snapshotLive() []*liveRun {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]*liveRun, 0, len(rt.live))
	for _, lr := range rt.live {
		out = append(out, lr)
	}
	return out
}

// terminateForKillSwitch stops one run under an emergency stop. It serializes with
// operator stop/resume/cleanup on this run (the per-run op lock), re-checks the run is
// still fresh inside the lock (no double-stop), records a system-attributed 'stopping'
// transition naming the kill-switch (the evidence leg), then signals the process group.
// It does NOT wait for finalize — the bridge records the terminal 'stopped' transition
// asynchronously, like an operator stop.
func (m *Module) terminateForKillSwitch(ctx context.Context, lr *liveRun, stopRef string) {
	release := m.rt.lockRun(liveKey(lr.tenant, lr.runRef))
	defer release()
	lr.mu.Lock()
	if lr.stopRequested || lr.finalized {
		lr.mu.Unlock()
		return
	}
	lr.stopRequested = true // finalize will record 'stopped' (intentional), not 'failed'
	lr.mu.Unlock()
	if _, err := m.transition(ctx, lr.tenant, lr.runRef, transitionInput{
		event: "stopping", actor: model.ActorSystem, actorKind: model.ActorSystem,
		detail: "emergency stop (kill switch " + stopRef + ")",
		guard:  guardRuntimeLaunch(lr.launchID),
	}); err != nil {
		m.warnf("kill-switch sweep: could not record stopping transition (stopping anyway)", "run_ref", lr.runRef, "err", redactErr(err))
	}
	if err := secretSafeCredentialError("session process kill-switch stop", lr.proc.Stop(ctx)); err != nil {
		m.warnf("kill-switch sweep: process stop failed", "run_ref", lr.runRef)
	}
	if err := m.revokeLiveRuntimeCredentials(ctx, lr); err != nil {
		m.warnf("kill-switch sweep: runtime credential revocation incomplete", "run_ref", lr.runRef)
	}
	m.warnf("session terminated by kill-switch", "run_ref", lr.runRef, "stop", stopRef)
}
