// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const idlePolicyReason = "idle_policy"
const idlePolicySweepInterval = 30 * time.Second

// idleSweepEvery is the cadence of the idle pass inside the lifecycle sweep.
func (rt *runtimeState) idleSweepEvery() time.Duration {
	if rt.idleSweepInterval > 0 {
		return rt.idleSweepInterval
	}
	return idlePolicySweepInterval
}

// A session the policy cannot reclaim yet is looked at again after this long, so a
// silent session with a large transcript is not re-read on every sweep.
const idlePolicyRecheck = 5 * time.Minute

// The stop effect (process stop, credential revocation) is bounded on its own and
// does not share the budget of the checks that preceded it.
const idlePolicyStopBudget = 30 * time.Second

var errIdleStopNotFinalized = errors.New("sessions: idle policy stop did not finalize")

// SetIdleAutoStopPolicy binds the instance setting before Start. A read failure
// skips optional reclamation; it never authorizes a stop. Nil is off.
func (m *Module) SetIdleAutoStopPolicy(read func(context.Context) (time.Duration, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rt.idleAutoStopPolicy = read
}

func (m *Module) idleAutoStopPolicy() func(context.Context) (time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rt.idleAutoStopPolicy
}

// Every observed output or attempted input resets the window, independently of
// the throttled durable last_activity_at. The version also catches activity while
// a background-work read was in flight, including a fake clock's identical ticks.
func (lr *liveRun) noteIdleActivity(at time.Time) {
	lr.mu.Lock()
	lr.lastIOAt = at
	lr.idleRecheckAt = time.Time{}
	lr.activityVersion++
	lr.mu.Unlock()
}

// holdRunOpen pins an authorized attach against policy-stop admission. The final
// stop check and the pin share lr.mu, so no lifecycle lock is needed; a view that
// arrives after that check sees the stop like any other viewer. The release
// function is idempotent: several views release independently on disconnect.
func (m *Module) holdRunOpen(tenant model.TenantID, ref string) (*liveRun, func()) {
	lr, ok := m.rt.getLive(tenant, ref)
	if !ok {
		return nil, func() {}
	}
	return lr, lr.pinViewer()
}

// pinViewer counts one person's open view of the run and returns its idempotent
// release. Every surface that streams a live run to a person pins through here:
// the HTTP attach and AttachRuntimeOutput. The pin shares lr.mu with the final
// stop check, so a view either holds the run before that check or sees the stop
// like any other viewer.
func (lr *liveRun) pinViewer() func() {
	lr.mu.Lock()
	lr.viewers.Add(1)
	lr.mu.Unlock()
	return sync.OnceFunc(func() { lr.viewers.Add(-1) })
}

// pinPreview counts one in-flight browser-preview request, with the same
// guarantee as pinViewer. It is a separate count because the denial budget reads
// viewers as "somebody can answer a breach", which a person using the app is not.
func (lr *liveRun) pinPreview() func() {
	lr.mu.Lock()
	lr.previewRequests.Add(1)
	lr.mu.Unlock()
	return sync.OnceFunc(func() { lr.previewRequests.Add(-1) })
}

// repinLive moves an attach's pin to the run that is live now. The replay can take
// long enough for the run to start, end or be replaced by a resume; the stream, the
// denial budget and the idle policy must all see the run that is actually served.
func (m *Module) repinLive(tenant model.TenantID, ref string, held *liveRun, release func()) (*liveRun, func()) {
	if cur, _ := m.rt.getLive(tenant, ref); cur == held {
		return held, release
	}
	release()
	return m.holdRunOpen(tenant, ref)
}

func (lr *liveRun) idlePolicyEligibleLocked(now time.Time, window time.Duration) bool {
	return !lr.finalized && !lr.stopRequested && !lr.launchFailed &&
		lr.transport == TransportStreamJSON && lr.viewers.Load() == 0 && lr.previewRequests.Load() == 0 &&
		len(lr.pendingApprovals) == 0 && len(lr.sessionCalls) == 0 &&
		lr.waitingForInputLocked() && !lr.lastIOAt.IsZero() &&
		!now.Before(lr.lastIOAt.Add(window)) && !now.Before(lr.idleRecheckAt)
}

func (m *Module) sweepIdleAutoStop(ctx context.Context) {
	read := m.idleAutoStopPolicy()
	if read == nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	window, err := read(readCtx)
	cancel()
	if err != nil {
		m.noteIdleSettingRead(errorCause(err, "read_failed"))
		return
	}
	m.noteIdleSettingRead("")
	if window <= m.rt.idleWindow {
		return
	} // includes zero: off
	for _, lr := range m.rt.snapshotLive() {
		stopCtx, cancel := context.WithTimeout(ctx, idlePolicyStopBudget)
		err := m.stopIdleRun(stopCtx, lr, window+m.rt.idleWindow)
		cancel()
		m.noteIdleOutcome(lr, err)
		if ctx.Err() != nil {
			return
		}
	}
}

// An unreadable setting is logged once per cause, like a per-run condition, and
// again only after a read succeeded in between. Empty cause means the read worked.
func (m *Module) noteIdleSettingRead(cause string) {
	m.mu.Lock()
	changed := m.rt.idleSettingWarned != cause
	m.rt.idleSettingWarned = cause
	m.mu.Unlock()
	if cause != "" && changed {
		m.warnf("sessions: idle policy setting could not be read; no sessions reclaimed", "cause", cause)
	}
}

// A persistent condition is logged once per cause, not on every 30 s sweep.
func (m *Module) noteIdleOutcome(lr *liveRun, err error) {
	cause := ""
	switch {
	case err == nil:
	case errors.Is(err, errIdleStopNotFinalized):
		cause = "stop_not_finalized"
	case errors.Is(err, store.ErrAuditSpoolFull):
		cause = "audit_unavailable"
	default:
		cause = errorCause(err, "policy_check_failed")
	}
	lr.mu.Lock()
	changed := lr.idleWarned != cause
	lr.idleWarned = cause
	lr.mu.Unlock()
	if cause != "" && changed {
		m.warnf("sessions: idle policy did not complete", "run_ref", lr.runRef, "cause", cause)
	}
}

// backoff leaves the session running and defers the next costly look.
func (m *Module) idleBackoff(lr *liveRun) {
	lr.mu.Lock()
	lr.idleRecheckAt = m.now().Add(idlePolicyRecheck)
	lr.mu.Unlock()
}

func (m *Module) stopIdleRun(ctx context.Context, lr *liveRun, window time.Duration) error {
	lr.mu.Lock()
	eligible, version := lr.idlePolicyEligibleLocked(m.now(), window), lr.activityVersion
	lr.mu.Unlock()
	if !eligible {
		return nil
	}
	// The transcript read is the costly step and holds no lock. Output, input or a
	// view that arrives meanwhile changes the version or the open-view count, both
	// rechecked under lr.mu before the stop is reserved.
	activity, err := m.runActivity(ctx, lr.tenant, lr.runRef)
	var refusal *runErr
	if errors.As(err, &refusal) {
		// A tool or run this policy does not cover (other tool, vault secrets, a
		// transcript over the read bound) stays running.
		m.idleBackoff(lr)
		return nil
	}
	if err != nil {
		return err
	}
	// No missing, partial or changing transcript and no active work is reclaimed by
	// an optional resource policy.
	if activity.Transcript != activityTranscriptRead || activity.Unreadable > 0 || activity.Incomplete || activityHasLiveWork(activity) {
		m.idleBackoff(lr)
		return nil
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(lr.tenant, lr.runRef))
	if err != nil {
		return err
	}
	defer release()
	if current, ok := m.rt.getLive(lr.tenant, lr.runRef); !ok || current != lr {
		return nil
	}
	rec, err := m.loadRun(ctx, lr.tenant, lr.runRef)
	if err != nil {
		return err
	}
	// Existing fenced work and resume contracts remain intact.
	// Each of these stays running and is looked at again after the recheck delay, so
	// the transcript above is not re-read on every sweep.
	if rec.String(colState) != stateRunning || rec.String(colRuntimeLaunchID) != lr.launchID.String() || rec.String(colClaudeSessionID) == "" {
		m.idleBackoff(lr)
		return nil
	}
	if err := m.refuseLegacyControlUnderWork(ctx, lr.tenant, rec); err != nil {
		m.idleBackoff(lr)
		return nil
	}
	// The peer links of the tenant are held steady from the check until the stop is
	// committed; they are released before the process effect.
	releasePeers, err := m.rt.lockRunContext(ctx, idlePeerAdmissionKey(lr.tenant))
	if err != nil {
		return err
	}
	defer releasePeers()
	peers, err := m.idlePolicyHasLivePeers(ctx, lr.tenant, rec)
	if err != nil {
		return err
	}
	if peers {
		m.idleBackoff(lr)
		return nil
	}
	lr.mu.Lock()
	if lr.activityVersion != version || !lr.idlePolicyEligibleLocked(m.now(), window) {
		lr.mu.Unlock()
		return nil
	}
	// Linearize with input, view admission and output: accepted input owns the run
	// operation lock; bridge activity, view pins and approval registration own this mutex.
	lr.stopRequested, lr.stopReason = true, idlePolicyReason
	lr.mu.Unlock()
	_, err = m.transition(ctx, lr.tenant, lr.runRef, transitionInput{
		event: "stopping", actor: model.ActorSystem, actorKind: model.ActorSystem,
		detail: "idle policy: stopped to free memory", keepReason: true, policyStop: true,
		guard:  guardRuntimeLaunch(lr.launchID),
		mutate: func(r model.Record) { r[colReason] = idlePolicyReason },
	})
	releasePeers()
	if err != nil {
		lr.mu.Lock()
		if !lr.finalized && lr.stopReason == idlePolicyReason {
			lr.stopRequested, lr.stopReason = false, ""
		}
		lr.mu.Unlock()
		return err // no audit, no optional stop
	}
	// Once the stop is recorded it runs to its end, on its own budget.
	effectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idlePolicyStopBudget)
	defer cancel()
	outcome, stopErr, revokeErr := m.stopLiveEffect(effectCtx, lr, idlePolicyReason, legacyRevocationPhase(effectCtx))
	if outcome != stopEffectFinalized {
		return errors.Join(errIdleStopNotFinalized, stopErr, revokeErr)
	}
	if stopErr == nil && revokeErr == nil && m.log != nil {
		m.log.Info("sessions: stopped by idle policy", "run_ref", lr.runRef, "reason", idlePolicyReason)
	}
	return errors.Join(stopErr, revokeErr)
}

// Peers can run on another node: inspect durable states in this tenant. Both
// directions of an explicit link and same-template links protect either endpoint.
// A capped or malformed read is uncertain, so it prevents reclamation.
func (m *Module) idlePolicyHasLivePeers(ctx context.Context, tenant model.TenantID, rec model.Record) (bool, error) {
	peers, valid := readRunPeers(rec)
	if !valid {
		return true, nil
	}
	var rows []model.Record
	capped := false
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		var total model.Page
		rows, total, err = repo.List(ctx, model.Query{Filters: []model.Filter{{Column: colState, Op: model.OpIn, Value: []string{statePending, stateRunning, stateIdle, stateWaitingApproval}}}, Limit: 1000})
		capped = err == nil && (len(rows) >= 1000 || total.HasMore)
		return err
	})
	if err != nil || capped {
		return capped, err
	}
	// The live handles are read after the store read: a peer's mutex is never taken
	// inside the store transaction.
	for _, other := range rows {
		// Only a peer that is itself active protects this session; two idle
		// peers do not hold each other open.
		if other.String(colRunRef) == rec.String(colRunRef) || !activityRunState(other.String(colState)) || !m.idlePeerActive(other) {
			continue
		}
		others, valid := readRunPeers(other)
		if !valid {
			return true, nil
		}
		sameTemplate := rec.String(colTemplateID) != "" && rec.String(colTemplateID) == other.String(colTemplateID) &&
			(rec.String(colRunPeersRule) == sameTemplatePeerRule || other.String(colRunPeersRule) == sameTemplatePeerRule)
		if slices.Contains(peers, other.String(colRunClaimSID)) || slices.Contains(others, rec.String(colRunClaimSID)) || sameTemplate {
			return true, nil
		}
	}
	return false, nil
}

// idlePeerActive reports whether a linked peer is working. A peer live on this node
// is working while a turn, an approval or a session tool call is open, however long
// it has been silent (a ten-minute build is silent); otherwise, and for a peer on
// another node, the displayed state decides, where a finished peer is idle.
func (m *Module) idlePeerActive(peer model.Record) bool {
	if lr, ok := m.rt.getLive(model.TenantID(peer.String(model.ColTenantID)), peer.String(colRunRef)); ok && lr.launchID.String() == peer.String(colRuntimeLaunchID) {
		lr.mu.Lock()
		open := lr.workInFlightLocked(m.now(), m.rt.idleWindow)
		lr.mu.Unlock()
		if open {
			return true
		}
	}
	return m.deriveRunState(peer) != stateIdle
}

// workInFlightLocked is true while the plane knows work is open on this run. Unlike
// !waitingForInputLocked it does not count a session that never took an input, which
// is only starting. A driver turn is open once the provider has named it; until then
// an input accepted within the window is the turn.
func (lr *liveRun) workInFlightLocked(now time.Time, window time.Duration) bool {
	if len(lr.pendingApprovals) > 0 || len(lr.sessionCalls) > 0 {
		return true
	}
	if lr.turnInterrupted {
		return false
	}
	if lr.session != nil {
		return lr.session.ActiveTurn() != "" || !lr.driverTurnEnded && !lr.lastIOAt.IsZero() && now.Before(lr.lastIOAt.Add(window))
	}
	return lr.claudePendingTurns > 0 || lr.claudeOutputOpen
}

// Reuse lifecycle admission to serialize local peer links and run reservations.
func idlePeerAdmissionKey(tenant model.TenantID) string { return "idle-peers|" + tenant.String() }

func activityHasLiveWork(activity activityDTO) bool {
	// Codex root records do not establish descendant command completion.
	// Retain any session that spawned an agent until child activity is supported.
	if activity.backgroundActive || (activity.Driver == providerDriverCodex && len(activity.Agents) > 0) {
		return true
	}
	for _, agent := range activity.Agents {
		if agent.State == activityRunning || agent.State == activityScheduled {
			return true
		}
	}
	for _, task := range activity.Tasks {
		if task.State == activityRunning || task.State == activityScheduled {
			return true
		}
	}
	return false
}
