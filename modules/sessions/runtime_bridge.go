// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strconv"
	"strings"
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
	lr.ring.close()
	// flush + seal this run's I/O evidence chain once its I/O has ended
	// (only when the run was flagged for recording). Best-effort, like Record.
	if lr.recordIO {
		_ = m.rt.recorder.Finalize(ctx, lr.tenant, lr.runRef)
	}
	exit, _ := lr.proc.Wait()
	m.finalize(lr, exit)
}

// onStdout drives stored state from a stream-json output line: it captures the
// resumable session id from the init message and tracks activity (throttled).
func (m *Module) onStdout(ctx context.Context, lr *liveRun, data []byte, at time.Time) {
	aplicar := func() {
		if sj, ok := parseStreamJSON(data); ok && sj.isInit() {
			m.captureSessionID(ctx, lr, sj.SessionID, at)
			return
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
	lr.mu.Unlock()
	m.mutateRunBest(ctx, lr, func(rec model.Record) {
		rec[colLastActivityAt] = model.NewTimestamp(at).String()
	})
	// SG-02-b: the session is demonstrably alive, so its lease is renewed on the same
	// throttle. The fence does NOT move on a renewal (claim.go Claim/Heartbeat), so a
	// long session keeps one identity and one token from start to finish.
	m.renewLaunchClaim(ctx, lr)
}

// finalize records the terminal transition exactly once when the process exits.
// A process killed by an operator stop (SIGTERM → non-zero exit) is recorded as
// STOPPED (intentional), not FAILED — the stopRequested flag distinguishes them.
func (m *Module) finalize(lr *liveRun, exit int) {
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
		if _, err := m.transition(ctx, lr.tenant, lr.runRef, transitionInput{
			event: event, toState: state,
			detail: "exit " + strconv.Itoa(exit), guard: guardRuntimeLaunch(lr.launchID),
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
// no gate is wired.
func (m *Module) buildLaunchSpec(p CreateRunParams, cred Credential, workCred WorkSessionCredential, communicationCred CommunicationSessionCredential, resumeID string, ws *resolvedWorkspace, injectEnv []EnvVar) LaunchSpec {
	var args []string
	switch p.Transport {
	case TransportStreamJSON:
		// The supported headless control transport: bidirectional NDJSON over stdio.
		args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--print")
	case TransportRemoteControl:
		// Lifecycle-only: I/O is relayed to Anthropic's cloud, not bridged (§0).
		args = append(args, "--remote-control")
		if p.Name != "" {
			args = append(args, "--name", p.Name)
		}
	}
	args = append(args, "--permission-mode", p.PermissionMode)
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	if p.Effort != "" {
		args = append(args, "--effort", p.Effort)
	}
	// the template's terms, as argv the operator never chose. This is the line
	// where a workspace template stops being a description and becomes a restriction —
	// it is built from the SERVER's merge (templateapply.go), so a caller who skips the
	// console and posts straight to /runs gets the same confinement.
	//
	// The allowlist travels as ONE comma-separated value rather than the flag's
	// space-separated variadic form: a variadic `--allowedTools A B` would swallow the
	// flags that follow it. Tool specs routinely contain spaces (`Bash(git *)`) and
	// never commas — templateTerms refuses one that does, so the join is lossless.
	//
	// It is only a restriction in company: `--allowedTools` AUTO-APPROVES, it does not
	// confine (connectors/claude SDKEvaluationOrder). What denies everything it does not
	// name is the permission mode dontAsk that the merge pins alongside it. Emitting
	// this flag without that mode would read as a lock-down and be a widening.
	if len(p.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(p.AllowedTools, ","))
	}
	if p.Instructions != "" {
		args = append(args, "--append-system-prompt", p.Instructions)
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}

	var env []EnvVar
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
	// A conducted session MUST NOT self-mutate under governance: disable Claude Code's
	// background auto-updater for the child so the binary stays pinned for the session's
	// lifetime (reproducibility + the co-deployment's pinned-artifact guarantee).
	// The deploy artifacts (image/compose/systemd) also set this on the engine, but the
	// procRunner env is a strict ALLOWLIST that would otherwise strip it from the child —
	// so inject it explicitly here, where it actually reaches `claude`.
	env = append(env, EnvVar{Name: "DISABLE_AUTOUPDATER", Value: "1"})

	// the governance env the LaunchGate wants on the child — the OLIVARES_HOOK_PEP_*
	// the managed PreToolUse hook reads to reach the governed PEP, so every tool-call the
	// operated session makes is policy-checked in line. Appended last (authoritative over
	// any host value); a per-session PEP bearer is held in memory and never persisted.
	env = append(env, injectEnv...)

	spec := LaunchSpec{
		Program:   m.rt.program,
		Args:      args,
		Env:       env,
		EnvAllow:  p.EnvAllow,
		Isolation: p.Isolation,
		WaitDelay: m.rt.waitDelay,
	}
	// ref→path is now GOVERNED. A run with no workspace keeps the empty Dir
	// (the native runner falls back to the process cwd). A resolved workspace sets the
	// working directory; for native that is the canonical host root, for a container it
	// is the in-container target plus the bind mount the runner consumes.
	if ws != nil {
		switch p.Isolation {
		case IsolationContainer, IsolationSandbox:
			spec.Dir = ws.containerTgt
			spec.Workspace = &WorkspaceMount{
				HostPath:        ws.rootReal,
				ContainerTarget: ws.containerTgt,
				ReadOnly:        ws.mountMode == mountRO,
			}
		default: // native
			spec.Dir = ws.rootReal
		}
	}
	return spec
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
