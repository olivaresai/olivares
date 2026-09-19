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
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// activityWriteInterval throttles last_activity_at writes: a busy session
// produces many frames, but the stored activity timestamp (which drives the
// derived idle state) is only persisted at most this often. The derived idle
// window is far larger, so throttled writes keep idle accurate without a DB write
// per frame.
const activityWriteInterval = 10 * time.Second

// abreVentanaDeReserva marca que los efectos de fila deben ESPERAR. Se llama ANTES de
// arrancar el bridge, nunca despues: si se llamara despues, los frames que llegaran en
// medio se aplicarian directos y la ventana no serviria de nada.
func (lr *liveRun) abreVentanaDeReserva() {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.reservaAbierta = true
}

// difiereSiLaReservaSigueAbierta encola fn y devuelve true si la ventana sigue abierta.
// Devolver false significa «aplicalo tu», no «se ha perdido».
func (lr *liveRun) difiereSiLaReservaSigueAbierta(fn func()) bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if !lr.reservaAbierta {
		return false
	}
	lr.diferidos = append(lr.diferidos, fn)
	return true
}

// cierraVentanaYVuelca cierra la ventana y aplica lo diferido EN ORDEN.
//
// Se llama SIEMPRE tras la transicion de reserva —commitee o falle—: si fallara y no se
// vaciara, los efectos quedarian encolados para siempre y `last_activity_at` se congelaria
// en el instante del lanzamiento. Un run vivo pareceria ocioso, que es peor que la carrera
// que esto viene a arreglar.
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
	for frame := range lr.proc.Output() {
		at := m.now()
		seq := lr.ring.append(frame.Stream, frame.Data, at)
		// Governed I/O recording (default no-op). Only runs when the
		// LaunchGate flagged this run for recording (CRITICAL/privileged or opted-in 2026-06-16) — a non-recorded run never anchors I/O, keeping the ledger
		// minimal. Best-effort: a recorder failure must not corrupt the live stream
		// (the recorder emits its own loud gap evidence); the deny-closed posture for
		// privileged sessions is enforced at the LaunchGate, not this fan-out.
		if lr.recordIO {
			_ = m.rt.recorder.Record(ctx, lr.tenant, lr.runRef, RecordedFrame{Seq: seq, Stream: frame.Stream, Data: frame.Data, At: at})
		}
		if frame.Stream == streamStdout {
			m.onStdout(ctx, lr, frame.Data, at)
		}
	}
	// The owned child is gone: fail every in-flight protocol waiter now, so a
	// handshake or a turn returns an error instead of hanging on a dead process.
	m.closeDriverSession(lr)
	lr.ring.close()
	// flush + seal this run's I/O evidence chain once its I/O has ended
	// (only when the run was flagged for recording). Best-effort, like Record.
	if lr.recordIO {
		_ = m.rt.recorder.Finalize(ctx, lr.tenant, lr.runRef)
	}
	// P1: the Wait error is RETAINED, not discarded. It is used only to classify the
	// observation; its text never reaches a stored field or the audit metadata.
	exit, waitErr := lr.proc.Wait()
	m.finalize(lr, exit, waitErr)
}

// onStdout drives stored state from a stream-json output line: it captures the
// resumable session id from the init message and tracks activity (throttled).
func (m *Module) onStdout(ctx context.Context, lr *liveRun, data []byte, at time.Time) {
	if lr.session != nil {
		// ⛔ CORRELATION HAPPENS FIRST AND IS NEVER DEFERRED. An owned RPC child is
		// blocked waiting for the answer to a request this process sent, and the
		// handshake that sent it runs BEFORE the reservation transition — so
		// queueing this behind the window would deadlock the launch against the very
		// window that exists to keep its writes out of the way. It is safe to run
		// here precisely because it touches NO row: it resolves an in-memory waiter
		// and dispatches a protocol reply, and every durable consequence still goes
		// through the deferral below.
		lr.session.Deliver(OutputFrame{Stream: streamStdout, Data: data})
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
	// ⛔ SOLO SE DIFIERE ESTO. El ring y el recorder ya han corrido arriba, en el mismo
	// giro del bucle: la opcion 4 quita del camino EXACTAMENTE las escrituras que colisionan
	// con el CAS de la reserva y nada mas. `mutateRunBest` tiene DOS llamantes y son
	// justamente los dos a los que este despacho llega (medido, no muestreado), asi que
	// diferir aqui cubre el 100 % y no «los que encontre».
	if lr.difiereSiLaReservaSigueAbierta(aplicar) {
		return
	}
	// La ventana ya esta cerrada: se aplica directo, pero bajo el MISMO mutex que el
	// volcado, para que un frame que llega justo al cerrar no se adelante a lo encolado.
	lr.aplicaMu.Lock()
	defer lr.aplicaMu.Unlock()
	aplicar()
}

// captureSessionID records the Claude session id (once) so the session can be
// resumed, and advances activity. Best-effort with one conflict retry.
func (m *Module) captureSessionID(ctx context.Context, lr *liveRun, sessionID string, at time.Time) {
	if lr.profile != nil {
		// B1: a PROFILED run binds its provider id under the profile scope, inside one
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
// identity the launch already minted for its own run reference (SG-00 §6: an
// operated run promotes to canonical identity, and its claude_session_id resolves
// to that same sid). Without this the plane would hold two identities for one
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
// Without it the whole admission plane is theater after five minutes: the TTL
// (claim.go defaultLeaseTTL) would lapse mid-session with the child still running,
// the fence stamped on the run row would stop matching, and the session would drift
// into being freely takeable while it was still being driven. Liveness is asserted
// by renewal, never assumed. Legacy work-only launches renew from I/O on the same
// throttle as activity writes; K3 dual-authority launches additionally run an
// independent timer so a silent process cannot outlive its Claim.
//
// Best-effort and quiet on the ordinary loss: a lease this fails to renew lapses,
// and the next governed write refuses. That refusal is the control working, not an
// error to escalate here.
//
// The output-driven call remains useful as a prompt legacy heartbeat. Under K3 it
// converges on renewDualRuntimeCredentials, whose in-flight guard coalesces it with
// the timer rather than issuing a heartbeat per frame.
func (m *Module) renewLaunchClaim(ctx context.Context, lr *liveRun) {
	if lr.claim.SID == "" || lr.claim.Holder == "" {
		return
	}
	if m.rt.communicationCredentialsEnabled {
		m.renewDualRuntimeCredentials(ctx, lr)
		return
	}
	if _, err := m.Heartbeat(ctx, lr.tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence, 0); err != nil {
		m.warnf("sessions: could not renew the launch claim", "run_ref", lr.runRef, "err", redactErr(err))
		return
	}
	// Extend the SAME exact-SID bearer only after Claim liveness committed. A
	// failed Claim heartbeat must never keep API authority alive independently.
	if m.rt.workSessionCreds == nil {
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

	renewedUntil, err := m.rt.workSessionCreds.Renew(context.WithoutCancel(ctx), id, WorkSessionCredentialRequest{
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
	// SG-02-b: the session is demonstrably alive, so its lease is renewed on the same
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
	requested := lr.stopRequested
	requestedReason := lr.stopReason
	lr.mu.Unlock()
	// the process is gone, so the template's duration ceiling has nothing left to
	// end. Released here rather than at the timer's own expiry so a session that exits
	// early leaves no armed timer holding its handle.
	lr.stopDeadline()
	m.stopRuntimeCredentialHeartbeat(lr)

	state, event := stateStopped, "stopped"
	if lr.launchFailed || (!requested && exit != 0) {
		state, event = stateFailed, "failed"
	}
	ctx := context.Background()
	// SG-02-b: give the claim back BEFORE publishing the terminal state, not after.
	//
	// The order is the fix for a race a contrast found in the other one. Publishing
	// `stopped` first makes the run resumable IMMEDIATELY, and a resume by the same
	// actor RENEWS the claim without moving the fence (a renewal is not a new
	// identity). The late release then still matched holder and fence — because those
	// name the actor, not this process — and revoked the successor's authority. There
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
	// K2: Wait has observed this exact supervised process die. Revoke only work
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
		// ⛔ ESTE ERROR SE TIRABA ENTERO (`_, _ = m.transition(...)`), Y ERA EL UNICO SITIO.
		//
		// Censo de los llamantes no-test de `m.transition` en este modulo: runtime.go 580,
		// 651, 687, 797, 811, 902, 931 y runtime_killswitch.go:129 lo manejan todos; esta
		// linea era la unica que lo descartaba. Y el filo: TRES LINEAS ARRIBA, `:272-277`,
		// este mismo bloque si maneja el error de `OwnerDied` con un `warnf`. El idioma
		// estaba en el fichero y la llamada siguiente lo olvidaba. Lo midio r25 y me lo
		// adjudico; el hueco que tapaba es que un rechazo de la guarda —o un fallo de
		// verdad— dejaba la fila sin asentar y sin que nadie se enterase.
		// P1: nil means this Process exited under the port contract; an error means
		// collection was not confirmed. Not childWasReaped, which classifies STOP
		// errors, not Wait errors.
		observation := obsProcessExitObserved
		if waitErr != nil {
			observation = obsProcessWaitUnverified
		}
		if _, err := m.transition(ctx, lr.tenant, lr.runRef, transitionInput{
			event: event, toState: state,
			detail: "exit " + strconv.Itoa(exit), guard: guardRuntimeLaunch(lr.launchID),
			terminalObservation: observation,
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
	lr.cancel()
	close(lr.finalizedCh)
	// The handle stays in the registry with its CLOSED ring so a late attach can
	// still replay the buffered tail; reapClosed reclaims it after closedRetention
	// (bounding memory), and cleanup/delete/shutdown drop it sooner.
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
	if err := m.data.Mutate(ctx, lr.tenant, attempt); errors.Is(err, store.ErrConflict) {
		_ = m.data.Mutate(ctx, lr.tenant, attempt)
	}
}

// conservaElSelloMasNuevo aplica semantica max(viejo, nuevo) a last_activity_at.
//
// ⛔ POR QUE EXISTE. El sello de un frame se toma AL RECIBIRLO (runtime_bridge.go:33,
// `at := m.now()`), no al escribirlo. En cuanto un efecto se DIFIERE —que es lo que hace la
// opcion 4 durante la ventana de reserva— entre esos dos instantes cabe otra escritura: la
// transicion de reserva pone un sello mas nuevo (runtime.go:719, :960) y el volcado diferido
// lo pisaria con el viejo. Resultado: `last_activity_at` RETROCEDE y la corrida parece ociosa
// antes de tiempo. Lo encontro un contraste externo sobre la nota de diseno, ANTES de que
// existiera el codigo.
//
// ⛔ Y POR QUE `max` Y NO UN VOLCADO ORDENADO. Ordenar exigiria coordinar dos gorrutinas —el
// bridge y la reserva— y esa ordenacion HOY NO EXISTE. `max` es local, no coordina nada, y es
// correcto bajo CUALQUIER orden de volcado, que es justo la propiedad que hace falta cuando
// los efectos se difieren. Decidido por the planner antes de escribir codigo
// (an internal design note (not shipped), decision 2).
//
// Si alguno de los dos sellos no es legible NO SE ADIVINA: se deja lo que escribio el llamante,
// que es el comportamiento anterior. Inventar un orden entre dos sellos que no se pueden
// comparar seria peor que no ordenarlos.
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

// buildLaunchSpec constructs the neutral launch spec from a run's parameters: the
// argv (a proper []string — never a shell-split string), the EXPLICIT env (the minted
// inference token by value, used and discarded; optional gateway base URL; the
// governance PEP env), and the RESOLVED workspace. ws is nil when the run has
// no workspace_ref; injectEnv is the LaunchGate's governance env, empty when
// no gate is wired; providerEnv is the governed managed credential of a non-Claude
// driver (§5.3), empty for every other source.
//
// It has TWO shapes, chosen by the run's driver, and the split is deliberate:
//
//   - the historical Claude path, unchanged to the byte. Its argv, its
//     ANTHROPIC_* bearer, its gateway base URL and its updater pin all belong to
//     that CLI and travel with it;
//   - a REGISTERED driver, whose argv comes from the driver itself and which
//     receives NO Anthropic variable at all. Reusing the Claude bearer for another
//     provider is precisely the confusion §5.1 forbids, and the cheapest way to
//     make it impossible is for this function to never build it.
func (m *Module) buildLaunchSpec(
	p CreateRunParams,
	cred Credential,
	workCred WorkSessionCredential,
	communicationCred CommunicationSessionCredential,
	resumeID string,
	ws *resolvedWorkspace,
	injectEnv []EnvVar,
	providerEnv []EnvVar,
) LaunchSpec {
	driverKey := launchDriverKey(p)
	drv, driven := m.driverFor(driverKey)
	dir, mount := launchWorkspaceTarget(p, ws)

	var args []string
	var driverLaunch DriverLaunch
	program := m.rt.program
	if driven {
		// The driver owns its own official argv. resumeID is deliberately NOT a flag
		// here: an app-server/ACP child resumes through a METHOD on the owned
		// protocol, and the correlated root response is the only thing allowed to
		// nominate the conversation.
		program = m.driverProgram(drv)
		driverLaunch = DriverLaunch{WorkDir: dir, Model: p.Model, Effort: p.Effort}
		if p.ProviderHome != nil {
			driverLaunch.ConfigHome = p.ProviderHome.ConfigHome
			driverLaunch.UserHome = p.ProviderHome.UserHome
		}
		args = drv.LaunchArgs(driverLaunch)
	} else {
		// ⛔ THE CLAUDE ARGV IS NOT BUILT HERE, AND THAT IS THE r3 CORRECTION.
		// This branch used to hold a SECOND copy of the `--print` stream-json form
		// that cliruntime already declared, and the transport declared beside that
		// other copy (cliruntime.LaunchTransport) therefore governed a path
		// production did not take: changing the declaration for a kind changed
		// nothing the engine launched. One table, consulted by the launch path that
		// runs in production, is what makes the declaration load-bearing.
		//
		// the template's terms travel as argv the operator never chose. They
		// are built from the SERVER's merge (templateapply.go), so a caller who
		// skips the console and posts straight to /runs gets the same confinement;
		// cliruntime is where the flag shapes of those terms live.
		claude := cliruntime.LaunchRequest{
			WorkDir:        dir,
			Model:          p.Model,
			Effort:         p.Effort,
			PermissionMode: p.PermissionMode,
			ResumeID:       resumeID,
			AllowedTools:   p.AllowedTools,
			// The profile's declared tool surface (provider_profile_policy.go). A
			// profiled launch always carries the decision; an unprofiled legacy launch
			// carries none and its argv is unchanged to the byte.
			ToolSurface:         p.ToolSurface,
			ToolSurfaceDeclared: p.ToolSurfaceDeclared,
			Instructions:        p.Instructions,
			Name:                p.Name,
		}
		if p.Transport == TransportRemoteControl {
			// Lifecycle-only: I/O is relayed to Anthropic's cloud, not bridged (§0).
			args = cliruntime.ClaudeRemoteControlArgs(claude)
		} else {
			// The governed stream-json form, and the DEFAULT for anything else:
			// validateCreate already normalizes an empty transport to it, and a
			// transport this function does not recognize must not fall through to an
			// argv with no form flag at all — that would launch the vendor CLI
			// INTERACTIVELY under a row that claims a bridged session.
			args = cliruntime.ClaudeArgs(claude)
		}
	}

	var env []EnvVar
	if !driven {
		if cred.Token != "" {
			// Bearer precedence over a (now-stripped) ANTHROPIC_API_KEY; the WIF token is
			// short-lived and never persisted (only its id reaches the row/ledger).
			env = append(env, EnvVar{Name: "ANTHROPIC_AUTH_TOKEN", Value: cred.Token})
		}
		if m.rt.baseURL != "" {
			// Route the operated session's inference through Olivares' own gateway so it
			// is PEP/budget/model-governed (wires the gateway; here it is an env-ref).
			env = append(env, EnvVar{Name: "ANTHROPIC_BASE_URL", Value: m.rt.baseURL})
		}
	}
	// §5.3: the governed managed credential of THIS driver's own adapter, and
	// nothing else. Empty under provider_account_home, where the authorized home is
	// the credential and Olivares injects nothing at all.
	env = append(env, providerEnv...)
	if workCred.Token != "" {
		// Exact-session kernel authority. These are explicit launch values, not
		// inherited host environment, and the token is never persisted/logged.
		env = append(env,
			EnvVar{Name: "OLIVARES_WORK_TOKEN", Value: workCred.Token},
			EnvVar{Name: "OLIVARES_WORK_SESSION_ID", Value: workCred.SessionRef},
			EnvVar{Name: "OLIVARES_WORK_RUN_REF", Value: workCred.RunRef},
		)
	}
	if communicationCred.Token != "" {
		// The K3 bearer is deliberately separate from work authority and injected
		// exactly once. Its tuple is carried inside the authenticated principal; no
		// caller-controlled binding env is needed or accepted.
		env = append(env, EnvVar{
			Name: "OLIVARES_COMMUNICATION_TOKEN", Value: communicationCred.Token,
		})
	}
	// B1: a profiled launch runs under the profile's homes. Both are explicit launch
	// values — they override whatever the host process inherited — and neither is a
	// credential: the configuration home is where the provider keeps its own settings
	// and transcripts, HOME is the child's user home. LaunchSpec.Dir (the workspace) is
	// a third, unrelated path. Which VARIABLE carries the configuration home is the
	// driver's own (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, …): naming another provider's
	// variable would point a child at a home nobody selected. A caller or a gate that
	// names either variable for a profiled launch is refused before this point rather
	// than resolved by order.
	if p.ProviderHome != nil {
		env = append(env,
			EnvVar{Name: envUserHome, Value: p.ProviderHome.UserHome},
			EnvVar{Name: m.configHomeEnvForDriver(p.ProviderHome.Driver), Value: p.ProviderHome.ConfigHome},
		)
		if p.ProviderHome.Driver == providerDriverOpenCode {
			// Runtime-owned XDG mapping beside HOME/ConfigHome. Do not set
			// XDG_RUNTIME_DIR: OC1 refuses an unqualified runtime directory.
			env = append(env, openCodeXDGMapping(p.ProviderHome.UserHome)...)
		}
	}
	if driven {
		// A registered driver may need an explicit, non-secret variable of its OWN
		// official CLI — today, the one that pins the child's version by disabling its
		// background updater, which is the same guarantee the Claude branch below gets
		// from Claude's own variable. It is optional: a driver that declares none
		// builds exactly the environment it built before.
		//
		// A name the profile OWNS is dropped rather than ordered: the whole point of
		// resolving homes server-side is that nothing downstream re-points them, and a
		// driver is downstream of that decision like a caller or a gate.
		if withEnv, ok := drv.(ProviderDriverLaunchEnv); ok {
			for _, item := range withEnv.LaunchEnv(driverLaunch) {
				if providerHomeEnvName(item.Name) ||
					(driverKey == providerDriverOpenCode && openCodeReservedEnvName(item.Name)) {
					m.warnf("sessions: a provider driver named a variable the profile owns; it was dropped",
						"driver", driverKey, "name", item.Name)
					continue
				}
				env = append(env, item)
			}
		}
	} else {
		// A conducted session MUST NOT self-mutate under governance: disable Claude Code's
		// background auto-updater for the child so the binary stays pinned for the session's
		// lifetime (reproducibility + the co-deployment's pinned-artifact guarantee).
		// The deploy artifacts (image/compose/systemd) also set this on the engine, but the
		// procRunner env is a strict ALLOWLIST that would otherwise strip it from the child —
		// so inject it explicitly here, where it actually reaches `claude`. It is Claude's
		// own variable and is not invented for another provider's CLI.
		env = append(env, EnvVar{Name: "DISABLE_AUTOUPDATER", Value: "1"})
	}

	// the governance env the LaunchGate wants on the child — the OLIVARES_HOOK_PEP_*
	// the managed PreToolUse hook reads to reach the governed PEP, so every tool-call the
	// operated session makes is policy-checked in line. Appended last (authoritative over
	// any host value); a per-session PEP bearer is held in memory and never persisted.
	env = append(env, injectEnv...)

	return LaunchSpec{
		Program:   program,
		Args:      args,
		Dir:       dir,
		Env:       env,
		EnvAllow:  p.EnvAllow,
		Isolation: p.Isolation,
		WaitDelay: m.rt.waitDelay,
		Workspace: mount,
	}
}

// launchWorkspaceTarget resolves the child's working directory and, for a
// containerized launch, its bind mount.
//
// ref→path is GOVERNED. A resolved workspace sets the working directory;
// for native that is the canonical host root, for a container it is the
// in-container target plus the bind mount the runner consumes.
//
// ⛔ A RUN WITH NO WORKSPACE NO LONGER RETURNS AN EMPTY DIR, and that is the
// correction of 2026-09-18. An empty Dir is not "no directory": the native runner
// falls back to the ENGINE's process cwd, and the golden path measured a governed
// child reporting the engine's own directory as its cwd, byte for byte, on the
// default path the console composer takes. Such a run is now given a directory of
// its own (runtime_workspace_dir.go) before it is persisted, so this function
// returns it. An empty value here means the caller resolved neither, which
// createRunInternal refuses before reaching this point.
func launchWorkspaceTarget(p CreateRunParams, ws *resolvedWorkspace) (string, *WorkspaceMount) {
	if ws == nil {
		return p.WorkspaceDir, nil
	}
	switch p.Isolation {
	case IsolationContainer, IsolationSandbox:
		return ws.containerTgt, &WorkspaceMount{
			HostPath:        ws.rootReal,
			ContainerTarget: ws.containerTgt,
			ReadOnly:        ws.mountMode == mountRO,
		}
	default: // native
		return ws.rootReal, nil
	}
}

// runtimeSettleWarrantsWarning decides whether a failed settle of the runtime row after
// process death is worth a line in the log.
//
// ⛔ Y EL SILENCIO ES LA MITAD DIFICIL, no el ruido. `guardRuntimeLaunch` rechaza con
// `conflictErr(...)` cuando una encarnacion mas nueva ya gano la fila, y eso es LEGITIMO
// y ORDINARIO: `assertRuntimeIncarnation` filtra el caso comun FUERA de la transaccion,
// pero entre ese chequeo y el commit hay una ventana y la guarda la cierra dentro. Un
// `if err != nil { warn }` a secas convierte esa supersesion en ruido, y un aviso que
// suena siempre se acaba silenciando entero — con el fallo de verdad dentro.
//
// ⛔ Y NO VALE `errors.Is(err, store.ErrConflict)`, que es lo primero que se piensa:
// `conflictErr` devuelve un `*runErr` (runtime.go:214-223) que NO envuelve el centinela
// del store ni implementa `Is`, asi que ese predicado da FALSE sobre el rechazo de la
// guarda y el aviso saltaria justo en el caso que hay que callar. Medido antes de
// escribir esta linea: `errors.Is(guarda, store.ErrConflict)=false`,
// `isRunConflict(guarda)=true`. Se comprueban las DOS formas porque las dos significan
// «otro escribio primero» y el modulo produce ambas.
func runtimeSettleWarrantsWarning(err error) bool {
	if err == nil {
		return false
	}
	return !isRunConflict(err) && !errors.Is(err, store.ErrConflict)
}
