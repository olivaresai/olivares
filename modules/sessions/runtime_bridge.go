// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// activityWriteInterval throttles last_activity_at writes: a busy session
// produces many frames, but the stored activity timestamp (which drives the
// derived idle state) is only persisted at most this often. The derived idle
// window is far larger, so throttled writes keep idle accurate without a DB write
// per frame.
const activityWriteInterval = 10 * time.Second

// abreVentanaDeReserva marks that the row's effects must WAIT. It is called BEFORE the
// bridge starts, never after: called after, frames arriving in between would be
// applied directly and the window would serve no purpose.
func (lr *liveRun) abreVentanaDeReserva() {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.reservaAbierta = true
}

// difiereSiLaReservaSigueAbierta queues fn and returns true while the window is open.
// false means "apply it yourself", not "it was lost".
func (lr *liveRun) difiereSiLaReservaSigueAbierta(fn func()) bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if !lr.reservaAbierta {
		return false
	}
	lr.diferidos = append(lr.diferidos, fn)
	return true
}

// cierraVentanaYVuelca closes the window and applies what was deferred, IN ORDER.
//
// It is ALWAYS called after the reservation transition, committed or failed: left
// unflushed, the effects would stay queued for ever and `last_activity_at` would freeze
// at launch, so a live run would look idle.
func (lr *liveRun) cierraVentanaYVuelca() {
	lr.aplicaMu.Lock()
	defer lr.aplicaMu.Unlock()
	lr.mu.Lock()
	lr.reservaAbierta = false
	pendientes := lr.diferidos
	lr.diferidos = nil
	lr.mu.Unlock()
	for _, fn := range pendientes {
		fn()
	}
}

// bridge is the per-run I/O pump: it drains the process's output channel into the
// sequenced ring (the attach source of truth), offers each frame to the governed
// recorder (default no-op), and parses the minimal stream-json envelope to drive
// stored state (session id, activity). When the output channel closes (the
// process exited), it finalizes the run.
func (m *Module) bridge(lr *liveRun) {
	ctx := context.Background() // a fresh ctx: the per-run ctx governs the PROCESS, not these DB writes
	sequenceExhausted := false
	for frame := range lr.proc.Output() {
		at := m.now()
		if codex, ok := lr.session.(*codexSession); ok && frame.Stream == streamStdout {
			frame.Data = codex.projectProfileModelResponse(frame.Data)
		}
		if frame.Stream == streamStdout {
			// The person's accepted ACP prompt, ahead of the child's next frame and
			// redacted like it; it is not the child's, so it skips onStdout.
			for _, prompt := range lr.acpEcho.before(frame.Data) {
				m.publishRuntimeOutput(lr, streamStdout, lr.redact.apply(prompt), at)
			}
		}
		// A session's vault secret values never leave it through its output: they
		// are withheld before the ring (the attach stream), the recorder and the
		// frame parser see the line (session_secret_env.go).
		frame.Data = lr.redact.apply(frame.Data)
		seq := m.publishRuntimeOutput(lr, frame.Stream, frame.Data, at)
		if seq == 0 {
			if !sequenceExhausted {
				sequenceExhausted = true
				lr.mu.Lock()
				lr.launchFailed = true
				lr.mu.Unlock()
				lr.stopDeadline()
				m.stopRuntimeCredentialHeartbeat(lr)
				// The existing asynchronous stop keeps this bridge draining and
				// withdraws both credentials even when Stop cannot confirm exit.
				m.terminateForRuntimeCredentialFailure(lr, "session output sequence range exhausted")
			}
			continue
		}
		if frame.Stream == streamStdout {
			m.onStdout(ctx, lr, frame.Data, at)
		}
	}
	// The owned child is gone: fail every in-flight protocol waiter now, so a
	// handshake or a turn returns an error instead of hanging on a dead process.
	if err := m.endSessionCalls(lr, ""); err != nil {
		m.warnf("session tool call cancellation incomplete", "run_ref", lr.runRef)
	}
	m.closeDriverSession(lr)
	lr.outputMu.Lock()
	lr.outputClosed = true
	lr.ring.close()
	// Flush + seal this run's I/O evidence chain once its I/O has ended
	// (only when the run was flagged for recording). A failed seal makes the
	// terminal run failed even when the child exited cleanly or was stopped.
	if lr.recordIO {
		if err := m.rt.Recorder.Finalize(ctx, lr.tenant, lr.runRef); err != nil {
			m.failIOEvidence(lr, "session I/O evidence sealing failed")
		}
	}
	lr.outputMu.Unlock()
	// P1: the Wait error is RETAINED, not discarded. It is used only to classify the
	// observation; its text never reaches a stored field or the audit metadata.
	exit, waitErr := lr.proc.Wait()
	m.finalize(lr, exit, waitErr)
}

// onStdout drives stored state from a stream-json output line: it captures the
// resumable session id from the init message and tracks activity (throttled).
func (m *Module) onStdout(ctx context.Context, lr *liveRun, data []byte, at time.Time) {
	if lr.session != nil {
		// Correlation happens first and is never deferred. An owned RPC child is
		// blocked waiting for the answer to a request this process sent, and the
		// handshake that sent it runs BEFORE the reservation transition — so
		// queueing this behind the window would deadlock the launch against the very
		// window that exists to keep its writes out of the way. It is safe to run
		// here precisely because it touches NO row: it resolves an in-memory waiter
		// and dispatches a protocol reply, and every durable consequence still goes
		// through the deferral below.
		turn := lr.session.ActiveTurn()
		lr.session.Deliver(OutputFrame{Stream: streamStdout, Data: data})
		if turn != "" && lr.session.ActiveTurn() != turn {
			m.finishSessionCalls(lr, turn)
		}
	} else if frame, ok := parseStreamJSON(data); ok && frame.isResult() {
		// The read result ends the turn for a refusal before its calls are cancelled,
		// which can wait on their owners: no refusal may act on it meanwhile.
		lr.endCredentialRefusal()
		m.finishSessionCalls(lr, "")
	} else if ok && frame.refusedCredential() {
		if turn, first := lr.beginCredentialRefusal(); first {
			// Off the bridge: the interrupt's answer arrives through this very loop.
			go m.stopTurnOnRefusedCredential(lr, frame.ErrorStatus, turn)
		}
	}
	aplicar := func() {
		if lr.session != nil {
			// A driver run's conversation is nominated by the CORRELATED ROOT RESPONSE
			// and by nothing else, so no frame captures an id here. What a later frame
			// is good for is the idempotent RETRY of a capture whose store write did
			// not confirm: without it a run whose alias transaction lost a race would
			// keep an id the plane knows and the database does not.
			if m.retryDriverCapture(ctx, lr, at) {
				return
			}
			m.touchActivity(ctx, lr, at)
			return
		}
		if sj, ok := parseStreamJSON(data); ok {
			// The tool's own mode, from the init frame and from the status frame it
			// sends when the mode changes (runtime_tool_mode.go).
			m.recordToolMode(ctx, lr, sj.toolMode())
			if sj.isInit() {
				m.captureSessionID(ctx, lr, sj.SessionID, at)
				return
			}
			// What the turn COST, credited from the frame that reports it
			// (runtime_usage.go). It advances last_activity_at in its own row write, so
			// a credited frame does not also need touchActivity — and a frame that
			// carries no metering falls through to the ordinary activity path.
			if sj.isResult() && m.recordTurnUsage(ctx, lr, data, at) {
				return
			}
		}
		m.touchActivity(ctx, lr, at)
	}
	// Only this is deferred. The ring and the recorder have already run above, in the
	// same turn of the loop: the deferral takes out of the way EXACTLY the writes that
	// collide with the reservation's CAS and nothing else. mutateRunBest's two callers
	// are the two this dispatch reaches, so deferring here covers all of them.
	if lr.difiereSiLaReservaSigueAbierta(aplicar) {
		return
	}
	// The window is closed: apply directly, under the SAME mutex as the flush, so a
	// frame arriving just as it closes cannot overtake what was queued.
	lr.aplicaMu.Lock()
	defer lr.aplicaMu.Unlock()
	aplicar()
}

// captureSessionID records the Claude session id (once) so the session can be
// resumed, and advances activity. Best-effort with one conflict retry.
func (m *Module) captureSessionID(ctx context.Context, lr *liveRun, sessionID string, at time.Time) {
	if lr.profile != nil {
		// A PROFILED run binds its provider id under the profile scope, inside one
		// transaction with the run row, and is marked captured only after that commit.
		m.captureProfiledSessionID(ctx, lr, sessionID, at)
		return
	}
	lr.mu.Lock()
	if lr.sessionIDCaptured {
		lr.mu.Unlock()
		m.touchActivity(ctx, lr, at)
		return
	}
	lr.sessionIDCaptured = true
	lr.lastActivityWrite = at
	lr.mu.Unlock()
	m.mutateRunBest(ctx, lr, func(rec model.Record) {
		if rec.String(colClaudeSessionID) == "" {
			rec[colClaudeSessionID] = sessionID
		}
		rec[colLastActivityAt] = model.NewTimestamp(at).String()
	})
	m.bindProviderSession(ctx, lr, sessionID)
	m.renewLaunchClaim(ctx, lr)
}

// bindProviderSession attaches the PROVIDER's session id to the same canonical
// identity the launch already minted for its own run reference (an operated run
// promotes to canonical identity, and its claude_session_id resolves to that same
// sid). Without this the plane would hold two identities for one
// session — one keyed on the reference Olivares issued, one on the id Claude
// issued — and telemetry arriving under the provider's id would resolve to a
// different session than the one admission governs.
//
// An id already bound to ANOTHER sid is recorded and not forced: that is an
// identity discrepancy worth denouncing, not a reason to kill a running session.
func (m *Module) bindProviderSession(ctx context.Context, lr *liveRun, sessionID string) {
	if sessionID == "" || lr.claim.SID == "" {
		return
	}
	// A bridge from a fenced-out process must not attach aliases after a
	// successor incarnation has taken over this durable run.
	if err := m.assertRuntimeIncarnation(ctx, lr.tenant, lr.runRef, lr.launchID); err != nil {
		return
	}
	err := m.BindAlias(ctx, lr.tenant, lr.claim.SID, SessionBinding{
		Provider: "claude", ExternalID: sessionID, At: m.now(),
	})
	if err != nil && !errors.Is(err, ErrAliasBound) {
		m.warnf("sessions: could not bind the provider session id to the canonical session",
			"run_ref", lr.runRef, "err", redactErr(err))
		return
	}
	if errors.Is(err, ErrAliasBound) {
		m.warnf("sessions: the provider session id already resolves to a DIFFERENT canonical session",
			"run_ref", lr.runRef, "err", redactErr(err))
	}
}

// renewLaunchClaim keeps the launch's lease alive while the process is alive.
//
// Without it the admission plane would lapse after five minutes: the TTL
// (claim.go defaultLeaseTTL) would lapse mid-session with the child still running,
// the fence stamped on the run row would stop matching, and the session would drift
// into being freely takeable while it was still being driven. Liveness is asserted
// by renewal, never assumed. All claimed launches run an independent timer so a
// silent process cannot outlive its Claim. Output also renews on the same throttle
// as activity writes.
//
// Best-effort and quiet on the ordinary loss: a lease this fails to renew lapses,
// and the next governed write refuses. That refusal is the control working, not an
// error to escalate here.
//
// The output-driven call remains useful as a prompt legacy heartbeat. With the
// communication credentials it converges on renewDualRuntimeCredentials, whose in-flight guard coalesces it with
// the timer rather than issuing a heartbeat per frame.
func (m *Module) renewLaunchClaim(ctx context.Context, lr *liveRun) {
	if lr.claim.SID == "" || lr.claim.Holder == "" {
		return
	}
	if m.rt.CommunicationCredentialsEnabled {
		m.renewDualRuntimeCredentials(ctx, lr)
		return
	}
	if _, err := m.Heartbeat(ctx, lr.tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence, 0); err != nil {
		m.warnf("sessions: could not renew the launch claim", "run_ref", lr.runRef, "err", redactErr(err))
		return
	}
	// Extend the SAME exact-SID bearer only after Claim liveness committed. A
	// failed Claim heartbeat must never keep API authority alive independently.
	if m.rt.WorkSessionCreds == nil {
		return
	}
	lr.mu.Lock()
	id, notAfter := lr.workCredentialID, lr.workCredentialNotAfter
	if id.IsZero() || lr.runtimeCredentialsRenewing || notAfter.Sub(m.now()) > workSessionCredentialRenewWindow {
		lr.mu.Unlock()
		return
	}
	lr.runtimeCredentialsRenewing = true
	lr.mu.Unlock()

	renewedUntil, err := m.rt.WorkSessionCreds.Renew(context.WithoutCancel(ctx), id, WorkSessionCredentialRequest{
		Tenant: lr.tenant, SessionRef: lr.claim.SID, RunRef: lr.runRef,
		AgentRef: lr.agentRef, ClaimFence: lr.claim.Fence,
	})
	lr.mu.Lock()
	lr.runtimeCredentialsRenewing = false
	if err == nil && renewedUntil.After(m.now()) && renewedUntil.After(lr.workCredentialNotAfter) {
		lr.workCredentialNotAfter = renewedUntil
	}
	lr.mu.Unlock()
	if err != nil {
		m.warnf("sessions: could not renew work-session credential", "run_ref", lr.runRef)
	} else if !renewedUntil.After(m.now()) {
		m.warnf("sessions: work-session credential source returned an expired renewal", "run_ref", lr.runRef)
	}
}

// Renew only near expiry. The core issuer's fixed lifetime is 30 minutes; a
// ten-minute window gives two thirds of the lifetime without auth writes while
// preserving ample retry time if the store has a transient fault.
const workSessionCredentialRenewWindow = 10 * time.Minute

// touchActivity advances last_activity_at, throttled to activityWriteInterval so
// a busy session does not write per frame. The derived idle state reads this.
func (m *Module) touchActivity(ctx context.Context, lr *liveRun, at time.Time) {
	lr.mu.Lock()
	if !lr.lastActivityWrite.IsZero() && at.Sub(lr.lastActivityWrite) < activityWriteInterval {
		lr.mu.Unlock()
		return
	}
	lr.lastActivityWrite = at
	captured := lr.sessionIDCaptured
	lr.mu.Unlock()
	if lr.profile != nil {
		// The run and its managed row share the same current authority transaction.
		if captured {
			m.touchManagedLive(ctx, lr, at)
		}
	} else {
		m.mutateRunBest(ctx, lr, func(rec model.Record) {
			rec[colLastActivityAt] = model.NewTimestamp(at).String()
		})
	}
	// The session is demonstrably alive, so its lease is renewed on the same
	// throttle. The fence does NOT move on a renewal (claim.go Claim/Heartbeat), so a
	// long session keeps one identity and one token from start to finish.
	m.renewLaunchClaim(ctx, lr)
}

// finalize records the terminal transition exactly once when the process exits.
// A process killed by an operator stop (SIGTERM → non-zero exit) is recorded as
// STOPPED (intentional), not FAILED — the stopRequested flag distinguishes them.
func (m *Module) finalize(lr *liveRun, exit int, waitErr error) {
	lr.mu.Lock()
	if lr.finalized {
		lr.mu.Unlock()
		return
	}
	lr.finalized = true
	lr.processReaped = childWasReaped(waitErr)
	processReaped := lr.processReaped
	requested := lr.stopRequested
	requestedReason := lr.stopReason
	ioEvidenceFailure := lr.ioEvidenceFailure
	accessStopCause := lr.accessStopCause
	lr.mu.Unlock()
	// The process is gone, so the template's duration ceiling has nothing left to
	// end. Released here rather than at the timer's own expiry so a session that exits
	// early leaves no armed timer holding its handle.
	lr.stopDeadline()
	m.stopRuntimeCredentialHeartbeat(lr)

	state, event := stateStopped, "stopped"
	if ioEvidenceFailure != "" || lr.launchFailed || (!requested && exit != 0) {
		state, event = stateFailed, "failed"
	}
	if ioEvidenceFailure != "" {
		requestedReason = ioEvidenceFailure
	}
	ctx := context.Background()
	// Give the claim back BEFORE publishing the terminal state, not after.
	//
	// Publishing `stopped` first would make the run resumable IMMEDIATELY, and a
	// resume by the same actor RENEWS the claim without moving the fence (a renewal is
	// not a new identity). The late release would then still match holder and fence —
	// because those name the actor, not this process — and revoke the successor's
	// authority. There
	// is no lock to close that window with: stopRun holds the per-run lock while it
	// waits on this very finalize, so taking it here would deadlock.
	//
	// Releasing first has no such window: while the row is still non-terminal nobody
	// can resume it, so nobody can be holding a claim for this session that this
	// release could take away.
	currentIncarnation := m.assertRuntimeIncarnation(
		ctx, lr.tenant, lr.runRef, lr.launchID,
	) == nil
	if currentIncarnation {
		m.releaseLaunchClaim(ctx, lr.tenant, lr.claim)
	}
	// The bearer is useful only while this exact supervised process is live.
	// Revoke before publishing the terminal run state; expiry remains the durable
	// backstop if auth storage is temporarily unavailable.
	if err := m.revokeLiveRuntimeCredentials(ctx, lr); err != nil {
		m.warnf("sessions: process-exit runtime credential revocation incomplete",
			"run_ref", lr.runRef)
	}
	// Wait has observed this exact supervised process die. Revoke only work
	// authority still tied to its canonical SID/run generation before making the
	// runtime row terminal and therefore resumable. The callback is synchronous
	// but best-effort: its WorkLease TTL/reaper is the durable recovery path when
	// the store is unavailable. Do not take the per-run operation lock here;
	// stopRun holds it while waiting on finalizedCh.
	deathReason := "runtime_exit"
	if requested {
		deathReason = "runtime_stop"
		if requestedReason != "" {
			deathReason = requestedReason
		}
	} else if exit != 0 {
		deathReason = "runtime_failure"
	}
	if currentIncarnation && lr.claim.SID != "" {
		if err := m.OwnerDied(ctx, lr.tenant, lr.claim.SID, lr.runRef, deathReason); err != nil {
			m.warnf("sessions: could not settle work owner death",
				"run_ref", lr.runRef, "err", redactErr(err))
		}
	}
	if currentIncarnation {
		// The settle's error is handled like every other m.transition caller's: a guard
		// refusal or a real failure would otherwise leave the row unsettled unnoticed.
		// Wait may report incomplete output after collecting the child. Retain
		// that failure without turning confirmed reaping into an unknown process.
		// An unconfirmed collection remains the stronger classification.
		observation := obsProcessExitObserved
		if !processReaped {
			observation = obsProcessWaitUnverified
		}
		detail := "exit " + strconv.Itoa(exit)
		if requestedReason != "" {
			detail = requestedReason
		}
		if !requested && exit != 0 {
			if cause := exitCause(lr.ring); cause != "" {
				driver := providerDriverClaude // the historical frame-driven path
				if lr.driver != nil {
					driver = lr.driver.Key()
				}
				detail += ": " + productCause(driver, cause)
			}
		}
		if outputWasIncomplete(lr.proc, waitErr) {
			detail += "; output incomplete"
		}
		if errors.Is(waitErr, ErrOutputLineTooLong) {
			detail += "; protocol output limit exceeded"
		}
		if !requested && exit != 0 {
			m.warnf("sessions: the session's tool exited on its own",
				"run_ref", lr.runRef, "exit", exit, "reason", detail)
		}
		if _, err := m.transition(ctx, lr.tenant, lr.runRef, transitionInput{
			event: event, toState: state,
			detail: detail, guard: guardRuntimeLaunch(lr.launchID),
			terminalObservation: observation,
			accessStopCause:     accessStopCause,
			mutate: func(rec model.Record) {
				rec[colExitCode] = int64(exit)
				rec[colStoppedAt] = model.NewTimestamp(m.now()).String()
				rec[colPID] = nil
				rec[colRuntimeLaunchID] = nil
			},
		}); runtimeSettleWarrantsWarning(err) {
			m.warnf("sessions: could not settle the runtime row after the process died",
				"run_ref", lr.runRef, "err", redactErr(err))
		}
	}
	// The run's GenAI invoke_agent span covers the process lifetime; it ends
	// here, with the run, and its bearer link goes with it so no later model
	// call parents on an ended span (runtime_trace.go). Nil-safe for the
	// launch-error liveRun, which never started one.
	lr.agentSpan.End()
	m.unlinkTrace(lr)
	lr.cancel()
	close(lr.finalizedCh)
	// The handle stays in the registry with its CLOSED ring so a late attach can
	// replay the buffered tail. reapClosed expires that tail only after confirmed
	// collection; an unverified wait retains custody for stop/shutdown to report.
	go m.reapClosed(lr)
}

// mutateRunBest applies a best-effort field update to a run row (used by the
// bridge for activity/session-id capture), retrying once on a concurrency
// conflict and otherwise ignoring the error (the next frame retries).
func (m *Module) mutateRunBest(ctx context.Context, lr *liveRun, fn func(rec model.Record)) {
	if lr == nil {
		return
	}
	attempt := func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, lr.runRef)
		if err != nil {
			return err
		}
		if err := guardRuntimeLaunch(lr.launchID)(rec); err != nil {
			return err
		}
		antes := rec.String(colLastActivityAt)
		fn(rec)
		conservaElSelloMasNuevo(rec, antes)
		_, err = repo.Update(ctx, rec)
		return err
	}
	if err := m.Data.Mutate(ctx, lr.tenant, attempt); errors.Is(err, store.ErrConflict) {
		_ = m.Data.Mutate(ctx, lr.tenant, attempt)
	}
}

// conservaElSelloMasNuevo applies max(old, new) to last_activity_at.
//
// A frame's stamp is taken when it is RECEIVED, not when it is written. Once an
// effect is DEFERRED during the reservation window, another write fits between the
// two instants: the reservation transition sets a newer stamp and the deferred flush
// would overwrite it with the older one, so last_activity_at would go BACKWARDS and
// the run would look idle too early.
//
// max and not an ordered flush: ordering would need the bridge and the reservation
// goroutines to coordinate, while max is local and correct under ANY flush order.
//
// When either stamp is unreadable nothing is guessed: what the caller wrote stays.
// Inventing an order between two stamps that cannot be compared would be worse.
func conservaElSelloMasNuevo(rec model.Record, antes string) {
	if antes == "" {
		return
	}
	nuevo := rec.String(colLastActivityAt)
	if nuevo == "" {
		rec[colLastActivityAt] = antes
		return
	}
	tAntes, errA := model.ParseTimestamp(antes)
	tNuevo, errN := model.ParseTimestamp(nuevo)
	if errA != nil || errN != nil {
		return
	}
	if tNuevo.Before(tAntes) {
		rec[colLastActivityAt] = antes
	}
}

// runtimeSettleWarrantsWarning decides whether a failed settle of the runtime row after
// process death is worth a line in the log.
//
// Staying quiet is the hard half. guardRuntimeLaunch refuses with conflictErr when a
// newer incarnation already won the row, and that is legitimate and ordinary:
// assertRuntimeIncarnation filters the common case outside the transaction, and the
// guard closes the window between that check and the commit. A bare warning would
// turn that supersession into noise, and a warning that always fires ends up
// silenced, real failures included.
//
// errors.Is(err, store.ErrConflict) alone is not enough: conflictErr returns a
// *runErr that neither wraps the store sentinel nor implements Is. Both forms are
// checked, because both mean "another writer was first" and the module produces both.
func runtimeSettleWarrantsWarning(err error) bool {
	if err == nil {
		return false
	}
	return !isRunConflict(err) && !errors.Is(err, store.ErrConflict)
}

func outputWasIncomplete(proc Process, waitErr error) bool {
	if errors.Is(waitErr, ErrOutputAbandoned) {
		return true
	}
	if reporter, ok := proc.(outputCompletionReporter); ok {
		return reporter.OutputIncomplete()
	}
	return false
}
