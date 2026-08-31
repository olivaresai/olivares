// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// runtimeState holds the OPERATE runtime: the governed seams (all deny-closed or
// additive by default), the configuration, and the in-memory registry of LIVE
// processes (the durable state of record is the sessions_run table; this map is
// the live handles keyed by tenant|run_ref).
type runtimeState struct {
	runner     Runner
	creds      CredentialSource
	launchGate LaunchGate
	stopGate   StopGate
	recorder   Recorder
	classifier Classifier // DLP classifier for governed file reads (nil = no labels / deny-mode fails closed)
	// workSessionCreds is late-bound by the composition root after core/auth is
	// constructed. When wired, a launch with a live Claim must receive an exact,
	// short-lived kernel bearer or the launch fails closed.
	workSessionCreds WorkSessionCredentialSource
	// communicationSessionCreds is the independent K3 bearer issuer. The enabled
	// bit distinguishes an intentionally disabled standalone module from K3 that
	// was configured but lost its issuer; the latter must refuse with 503.
	communicationSessionCreds       CommunicationSessionCredentialSource
	communicationCredentialsEnabled bool
	// Promotion recovery crosses service suspension through a custody-only store
	// while retaining residency. These revokers are separate so ordinary
	// mint/renew can never inherit that bypass.
	recoveryWorkSessionCreds          WorkSessionCredentialSource
	recoveryCommunicationSessionCreds CommunicationSessionCredentialSource

	program                     string // the executable to launch ("claude")
	baseURL                     string // optional ANTHROPIC_BASE_URL → Olivares' inference gateway
	idleWindow                  time.Duration
	waitDelay                   time.Duration
	ringFrames                  int
	ringBytes                   int
	credentialHeartbeatInterval time.Duration

	// stopSweepInterval is how often the active kill-switch sweep re-checks
	// live runs against the StopGate and terminates any now under an emergency stop.
	// 0 ⇒ disabled (the standalone default; the composition root enables it alongside
	// the real StopGate). sweepCancel stops the loop on module Stop.
	stopSweepInterval time.Duration
	sweepCancel       func()

	// afterFunc is the TIMER seam behind the template duration ceiling. It is a
	// seam of its own and not a derivative of the module clock, because a controllable
	// clock with an uncontrollable timer measures the HOST: the test would move the fake
	// clock an hour and then wait an hour of wall time for time.AfterFunc to fire. The
	// production value is time.AfterFunc; a test substitutes one it can trigger.
	afterFunc func(time.Duration, func()) runTimer

	mu   sync.Mutex
	live map[string]*liveRun

	// opMu guards opLocks; opLocks serializes lifecycle OPERATIONS on the same run
	// (resume/stop/cleanup/delete) so two operators cannot double-launch or race a
	// transition. The bridge's finalize() runs independently and is made safe by the
	// state-machine guard in transition(), not by this lock (so a stop waiting on
	// finalize cannot deadlock it).
	opMu    sync.Mutex
	opLocks map[string]*opLock
}

// opLock is a per-run mutex with a reference count so it can be reclaimed.
type opLock struct {
	mu   sync.Mutex
	refs int
}

// runTimer is the cancellable one-shot the duration ceiling arms (*time.Timer in
// production).
type runTimer interface{ Stop() bool }

// realAfterFunc is the production timer seam.
func realAfterFunc(d time.Duration, f func()) runTimer { return time.AfterFunc(d, f) }

// newRuntimeState builds the runtime with DENY-CLOSED launch credentials/runner
// and additive (no-op) governance gates.
func newRuntimeState() *runtimeState {
	return &runtimeState{
		runner:                      unwiredRunner{},
		creds:                       denyCredentialSource{},
		launchGate:                  allowLaunchGate{},
		stopGate:                    allowStopGate{},
		recorder:                    noopRecorder{},
		program:                     "claude",
		idleWindow:                  defaultRuntimeIdleWindow,
		waitDelay:                   defaultWaitDelay,
		ringFrames:                  defaultRingFrames,
		ringBytes:                   defaultRingBytes,
		credentialHeartbeatInterval: defaultLeaseTTL / 2,
		live:                        map[string]*liveRun{},
		opLocks:                     map[string]*opLock{},
		afterFunc:                   realAfterFunc,
	}
}

// lockRun acquires the per-run operation lock for key and returns its release.
func (rt *runtimeState) lockRun(key string) func() {
	rt.opMu.Lock()
	l := rt.opLocks[key]
	if l == nil {
		l = &opLock{}
		rt.opLocks[key] = l
	}
	l.refs++
	rt.opMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		rt.opMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(rt.opLocks, key)
		}
		rt.opMu.Unlock()
	}
}

// defaultRuntimeIdleWindow: an operated session with no output within this window
// is DERIVED as idle (the process is not killed; idle is a read-time projection,
// like the observe overlay's cc_state — never a stored flip-flop).
const defaultRuntimeIdleWindow = 5 * time.Minute

// liveRun is a process Olivares currently supervises.
type liveRun struct {
	tenant    model.TenantID
	runRef    string
	runID     model.ID
	transport Transport
	proc      Process
	ring      *outputRing
	cancel    context.CancelFunc // cancels the per-run background context

	// recordIO is the LaunchGate's verdict: record this run's bridged I/O as
	// governed ledger evidence. agentRef is the kill-switch agent dimension the active
	// stop sweep scopes on (empty ⇒ only the estate graduation can stop this run).
	recordIO bool
	agentRef string

	// claim is the admission lease this run was launched under (SG-02-b). The bridge
	// renews it while the process lives and binds the provider's session id to its
	// canonical session; the zero value means the launcher named no holder.
	claim    Lease
	launchID model.ID
	// workCredentialID is the non-sensitive revocation handle of the exact-SID
	// kernel bearer injected at launch. The bearer itself never leaves LaunchSpec.
	workCredentialID                model.ID
	workCredentialNotAfter          time.Time
	communicationWorkspaceID        model.ID
	communicationCredentialID       model.ID
	communicationCredentialNotAfter time.Time
	runtimeCredentialsRenewing      bool
	runtimeHeartbeatRunning         bool
	credentialHeartbeatCancel       context.CancelFunc

	// ⛔ LA VENTANA DE RESERVA (K2, opcion 4). Entre `go m.bridge(lr)` y la transicion que
	// reserva la fila (`launched`/`resumed`) el bridge YA puede escribir, y su escritura
	// compite con el CAS de esa transicion. La opcion 4 sigue CONSUMIENDO igual —ring,
	// recorder y contrapresion intactos— y difiere solo los efectos que tocan la FILA,
	// aplicandolos EN ORDEN en cuanto la reserva commitea.
	//
	// aplicaMu serializa el volcado (gorrutina del launch) contra la aplicacion directa
	// (gorrutina del bridge). HOY onStdout solo lo llama el bridge, asi que es de un solo
	// hilo; diferir introduce un SEGUNDO llamante y sin este mutex `lastActivityWrite`
	// pasaria a estar en carrera. El mutex no es precaucion: es parte del cambio.
	aplicaMu       sync.Mutex
	reservaAbierta bool
	diferidos      []func()

	mu                sync.Mutex
	stopRequested     bool
	stopReason        string
	launchFailed      bool
	finalized         bool
	finalizedCh       chan struct{}
	lastActivityWrite time.Time
	sessionIDCaptured bool
	// deadline is the template's session-duration ceiling, nil when unbounded.
	// It is stopped by finalize/teardown so an early exit leaves no timer behind.
	deadline runTimer
}

func liveKey(tenant model.TenantID, runRef string) string {
	return tenant.String() + "|" + runRef
}

func (rt *runtimeState) getLive(tenant model.TenantID, runRef string) (*liveRun, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	lr, ok := rt.live[liveKey(tenant, runRef)]
	return lr, ok
}

func (rt *runtimeState) putLive(lr *liveRun) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.live[liveKey(lr.tenant, lr.runRef)] = lr
}

func (rt *runtimeState) dropLive(tenant model.TenantID, runRef string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.live, liveKey(tenant, runRef))
}

// runErr is a runtime error carrying the HTTP status the API should return, so a
// handler maps lifecycle failures (denied, conflict, not-found) to the right code
// without re-deriving them.
type runErr struct {
	status int
	msg    string
}

func (e *runErr) Error() string { return e.msg }

func badRequest(msg string) *runErr   { return &runErr{http.StatusBadRequest, msg} }
func notFoundErr() *runErr            { return &runErr{http.StatusNotFound, "session not found"} }
func conflictErr(msg string) *runErr  { return &runErr{http.StatusConflict, msg} }
func forbiddenErr(msg string) *runErr { return &runErr{http.StatusForbidden, msg} }

// CreateRunParams is the validated input to a launch (built from the API body).
type CreateRunParams struct {
	Name           string
	Transport      Transport
	PermissionMode string
	Effort         string
	Model          string
	WorkspaceRef   string
	Isolation      Isolation
	EnvAllow       []string // allowlisted host env var NAMES to forward to the child
	ResumeOf       string   // a run_ref to resume (optional; reserved for future use)
	Actor          string
	ActorKind      string
	// AgentRef is the AUTHENTICATED agent identity driving this launch
	// (auth.Principal.AgentIdentity), and it is a SEPARATE fact from the audit
	// actor on purpose. An agent-OBO token authenticates as kind "token" with
	// actor "token:<cred-id>" — correct evidence, and exactly why the agent
	// dimension cannot be recovered from Actor/ActorKind. It carried no agent at
	// all before, so a run launched over HTTP by an agent stored agent_ref NULL:
	// SessionActsForAgent could not recognize the driver, AND an agent-scoped
	// EMERGENCY STOP did not match the run, because the kill-switch decides scope
	// on exactly this value (sessionStopGate.Check -> st.Stopped(dims.AgentRef)).
	// The empty value was the dangerous state, not the fix.
	//
	// Server-set only: the request DTO has no agent_ref field, so a caller cannot
	// declare somebody else's agent (confused-deputy).
	AgentRef string
	// RecordRequested is the operator's per-launch opt-in to full I/O recording for a
	// non-CRITICAL session (CRITICAL/privileged sessions are recorded regardless — the
	// LaunchGate decides 2026-06-16).
	RecordRequested bool

	// --- The template plane (templateapply.go). ---
	//
	// TemplateID is the workspace template whose terms GOVERN this launch. The server
	// resolves it and merges its terms into these params BEFORE validation and before the
	// governance gates, so everything downstream — the CRITICAL determination, the budget
	// dimensions, the argv — sees the RESTRICTED launch. The four fields below are the
	// merge's OUTPUT and are not accepted from the wire: a caller cannot post an
	// allowlist, only a template that carries one, so "restricted" always traces to a
	// stored, permissioned template rather than to whatever the request asked for.
	TemplateID   string
	AllowedTools []string      // the child's allow rules; enforced with permission-mode dontAsk
	Instructions string        // appended to the child's system prompt
	MaxDuration  time.Duration // the runtime stops the session at this age (0 = unbounded)
	// TemplateVersion is the RESOLVED template's store version at the moment of this
	// launch. It reaches the launch gate in the intent so a governed approval binds to the
	// terms that were approved, and it is persisted so an operator can tell which revision
	// a running child was started from after the template is edited.
	TemplateVersion int64
}

// launchIntentFor builds the references-only launch intent the governance gates
// scope on, from the validated params and the RESOLVED workspace (nil ⇒ no
// workspace). runRef is the run being launched — on a create it is the reference
// minted BEFORE the gate is consulted (SG-02-b; it used to be "" here, which is
// what made an admission decorator unwirable). The agent dimension is the actor
// only when it is an agent NHI; the workspace flags are derived from the resolved
// workspace so the (cmd-side) gate never re-reads the workspace table.
//
// lease is the claim ACQUIRED for this launch (zero value when none was acquired,
// e.g. a caller that named no actor — an admission gate refuses that, the bare
// runtime does not).
func launchIntentFor(action LaunchAction, runRef string, p CreateRunParams, ws *resolvedWorkspace, lease Lease) LaunchIntent {
	intent := LaunchIntent{
		Action: action, RunRef: runRef, Transport: p.Transport,
		PermissionMode: p.PermissionMode, Model: p.Model, WorkspaceRef: p.WorkspaceRef,
		Actor: p.Actor, ActorKind: p.ActorKind, RecordRequested: p.RecordRequested,
		Holder: lease.Holder, Fence: lease.Fence, ClaimSID: lease.SID,
		TemplateRef: p.TemplateID, TemplateVersion: p.TemplateVersion,
		AllowedTools: append([]string(nil), p.AllowedTools...),
	}
	// The authenticated identity wins; the actor-derived form stays for
	// in-process callers that name an agent actor directly and never had a
	// separate AgentRef to give.
	if p.AgentRef != "" {
		intent.AgentRef = p.AgentRef
	} else if p.ActorKind == model.ActorAgent {
		intent.AgentRef = p.Actor
	}
	if ws != nil {
		intent.WorkspaceClassified = ws.dlpMode != dlpOff
		intent.WorkspaceReadWrite = ws.mountMode == mountRW
	}
	return intent
}

// admit is the launch's admission preamble (SG-02-b): it resolves the run's
// canonical session and ACQUIRES the working claim for the launcher.
//
// It acquires rather than verifies, and the difference is the whole design. Until
// the claim routes exist (pack SG-02-c) no caller has a token to present, so a gate
// that demanded one would be a control nobody could satisfy — and a gate that
// looked the current fence up on the caller's behalf would be comparing a value
// with itself. What CAN be asserted here is exclusivity: Claim refuses with
// ErrClaimHeld when another holder's lease is still live, and that refusal is the
// control. The acquired lease is then stamped on the run row, which is what every
// later governed write compares against.
//
// A launcher that names no holder gets no claim and a zero Lease. The bare runtime
// stays permissive for it (the module's gates are additive by contract); a composed
// ClaimAdmission refuses it.
func (m *Module) admit(ctx context.Context, tenant model.TenantID, runRef, holder string) (Lease, error) {
	return m.admitInWorkspace(ctx, tenant, runRef, holder, "")
}

// admitInWorkspace is K4's first-sight form of admission. A normal HTTP launch
// has no authoritative core workspace and calls admit above; a work launch gets
// its workspace only from the stored WorkItem and binds it to the canonical SID
// before Claim or either runtime credential is issued.
func (m *Module) admitInWorkspace(
	ctx context.Context,
	tenant model.TenantID,
	runRef, holder string,
	workspaceID model.ID,
) (Lease, error) {
	if holder == "" || m.data == nil {
		return Lease{}, nil
	}
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: runRef, Origin: OriginOperated,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		// An unreadable identity plane never means "go".
		return Lease{}, denyClosedErr("session identity unavailable", err)
	}
	lease, err := m.Claim(ctx, tenant, sid, holder, 0)
	if err != nil {
		return Lease{}, denyClosedErr("could not claim the session", err)
	}
	return lease, nil
}

// admitResume acquires a fresh claim generation for a new supervised process.
// A normal Claim deliberately renews a live claim held by the same actor without
// moving its fence. That is correct for heartbeats, but wrong for resume: if the
// previous finalizer could not release the claim and revoke its process bearer,
// renewing the same generation would make that stale bearer authoritative again.
//
// Release happens before Claim and before any governance hook or process effect.
// A store failure therefore denies the resume without extending old authority;
// a released/lapsed row is safely reacquired at the next monotonic fence. Another
// live holder still wins through Claim's ordinary ErrClaimHeld path.
func (m *Module) admitResume(
	ctx context.Context,
	tenant model.TenantID,
	runRef, holder string,
	previousFence int64,
) (Lease, error) {
	if holder == "" || m.data == nil || previousFence <= 0 {
		return m.admit(ctx, tenant, runRef, holder)
	}
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: runRef, Origin: OriginOperated,
	})
	if err != nil {
		return Lease{}, denyClosedErr("session identity unavailable", err)
	}
	if err := m.Release(ctx, tenant, sid, holder, previousFence); err != nil &&
		!errors.Is(err, ErrLeaseLost) && !errors.Is(err, ErrNoClaim) {
		return Lease{}, denyClosedErr("could not rotate the session claim", err)
	}
	lease, err := m.Claim(ctx, tenant, sid, holder, 0)
	if err != nil {
		return Lease{}, denyClosedErr("could not claim the session", err)
	}
	if lease.Fence <= previousFence {
		// A missing/regressed identity record must never turn an old process
		// generation into a current one. Best-effort release closes the newly
		// observed claim while expiry remains the durable fallback.
		_ = m.Release(context.WithoutCancel(ctx), tenant, sid, holder, lease.Fence)
		return Lease{}, denyClosedErr("session claim generation did not advance", ErrLeaseLost)
	}
	return lease, nil
}

// denyClosedErr refuses an operation while PRESERVING what kind of failure it was.
//
// Both outcomes refuse, and deny-closed is not in question here or anywhere else in
// this preamble. What differs is what the caller is TOLD. A business verdict —
// somebody else holds the claim, the lease moved, the fence is exhausted — is a
// decision, and a 403 or 409 correctly says "do not retry this". A store that is
// unavailable, or a node that is no longer the leader, is a transient condition with
// its own contract status; flattening it into a 403 told the caller that a failover
// was a policy decision, and copied the backend's own message into the response body
// on the way out (P1-N1, sixth contrast — the same two impacts that made a P1 of the
// version before it).
//
// So a business verdict is given a status here and none of the cause's text, and
// everything else travels WITH its cause to the store mapper, which is where that
// taxonomy is defined (core/api/errors.go, mirrored by writeStoreError).
func denyClosedErr(what string, err error) error {
	switch {
	case errors.Is(err, ErrClaimHeld):
		return &runErr{http.StatusConflict, "session is claimed by another holder with a live lease"}
	case errors.Is(err, ErrLeaseLost), errors.Is(err, ErrNoClaim),
		errors.Is(err, ErrNoHolder), errors.Is(err, ErrFenceExhausted):
		return forbiddenErr(what + " (deny-closed): the session's admission plane refused")
	case errors.Is(err, errNoCredential):
		// NOT the same thing as a mint backend that is merely unreachable, which is
		// what the fallthrough below deliberately protects: this is the CONFIGURED
		// state of having no credential source wired at all, which the operator
		// chose (or has not chosen yet). It is a decision, and it has a remedy.
		//
		// Measured on a real `serve` before this case existed: `POST /runs` with
		// transport stream-json answered `500 {"error":{"message":"internal
		// error"}}` and logged nothing, while the SAME boot had already printed
		// "session runtime: no inference credential source configured; stream-json
		// launches are deny-closed". The engine knew the answer and told the
		// operator it had broken. That is the two-answer failure this repository
		// names: "I refuse, and here is why" is not "I broke".
		//
		// 503 is not a new convention -- it is the one the three sibling issuers in
		// this module already use for an unwired dependency (runtime.go
		// "work-session credential issuer is not available",
		// runtime_communication_credential.go "communication credential issuer is
		// not available" and "session identity is not available"). This was the
		// fourth issuer and the only one that fell through to 500.
		return &runErr{
			http.StatusServiceUnavailable,
			"inference credential source is not wired; stream-json launches are deny-closed " +
				"(set OLIVARES_SESSION_RUNTIME_WIF or OLIVARES_SESSION_RUNTIME_TOKEN_FILE). " +
				"remote-control launches do not need it",
		}
	}
	var re *runErr
	if errors.As(err, &re) {
		return err // a lower layer already decided the status this deserves
	}
	return fmt.Errorf("sessions: %s (deny-closed): %w", what, err)
}

// createRun validates, runs the pre-flight gates, mints a credential, launches the
// process, persists the lifecycle, and starts the I/O bridge. It returns the run
// DTO once the process is running (or a typed error mapped to an HTTP status).
func (m *Module) createRun(ctx context.Context, tenant model.TenantID, p CreateRunParams) (runDTO, error) {
	return m.createRunInternal(ctx, tenant, p, nil)
}

// createRunInternal is shared by the ordinary HTTP launch and K4's private
// RuntimeControl. A non-nil work call adds a durable dispatch reservation and
// lease bind before Runner.Launch; it never turns WorkLaunchSpec into an HTTP
// request or lets orchestration call a handler.
func (m *Module) createRunInternal(
	ctx context.Context,
	tenant model.TenantID,
	p CreateRunParams,
	work *workLaunchCall,
) (runDTO, error) {
	// impose the template's terms FIRST — before validation (which defaults an
	// empty permission_mode, and a defaulted value cannot be told apart from a chosen
	// one) and before every gate below, so the budget, the CRITICAL determination and
	// the HITL approval all judge the RESTRICTED launch instead of the requested one.
	// A launch with no template_id is not touched by this call.
	tpl, tplConflicts, err := m.applyLaunchTemplate(ctx, tenant, &p)
	if err != nil {
		return runDTO{}, err
	}
	if err := validateCreate(&p); err != nil {
		return runDTO{}, err
	}
	if work != nil {
		reservation, existing, replayed, err := m.prepareWorkLaunch(
			ctx, tenant, work.spec, &p,
		)
		if err != nil {
			return runDTO{}, err
		}
		work.reservation, work.replayed = reservation, replayed
		if replayed {
			return m.toRunDTO(existing), nil
		}
	}
	// formalize workspace_ref → governed host path/mount BEFORE the gates so the
	// CRITICAL determination can read the workspace's classification/mount
	// posture. A non-empty ref that is not a registered workspace fails deny-closed.
	ws, err := m.resolveLaunchWorkspace(ctx, tenant, p.WorkspaceRef)
	if err != nil {
		return runDTO{}, err
	}
	// the kill-switch, checked FIRST and on its own. It used to travel inside
	// preflight, which was fine while nothing wrote before the gates; SG-02-b's
	// admission preamble does write, and a stopped estate must still write NOTHING.
	// Stop therefore outranks the admission plane as well as the launch gate.
	agentRef := p.AgentRef
	if agentRef == "" && p.ActorKind == model.ActorAgent {
		agentRef = p.Actor
	}
	if err := m.preflightStop(ctx, tenant, StopDims{AgentRef: agentRef}); err != nil {
		return runDTO{}, err
	}
	// G/K3 is an indivisible dual credential posture. Refuse incomplete
	// composition before admission acquires a Claim or the launch gate can open
	// approvals/provision any authority of its own.
	if err := m.ensureRuntimeCredentialWiring(); err != nil {
		return runDTO{}, err
	}

	// SG-02-b: the reference is minted BEFORE the gate is consulted, and the claim is
	// acquired against it, so the gate is asked about a launch that has an identity and
	// a holder instead of about an empty string.
	runRef := string(model.NewID())
	var identityWorkspace model.ID
	claimHolder := p.Actor
	if work != nil {
		identityWorkspace = work.reservation.workspaceID
		claimHolder = work.reservation.claimHolder
	}
	lease, err := m.admitInWorkspace(ctx, tenant, runRef, claimHolder, identityWorkspace)
	if err != nil {
		return runDTO{}, err
	}

	// governance pre-flight (budget/HITL/PEP). Returns the PEP env to inject and
	// whether to record I/O.
	intent := launchIntentFor(LaunchActionCreate, runRef, p, ws, lease)
	pr, err := m.preflight(ctx, tenant, intent, StopDims{AgentRef: intent.AgentRef})
	if err != nil {
		// The launch was refused AFTER the preamble took the claim. Give it back: a
		// refused launch must not leave the session held by a launcher that never ran.
		// The canonical identity is deliberately kept — "somebody tried to launch this"
		// is a fact, and the identity plane is where facts about sessions live.
		m.releaseLaunchClaim(ctx, tenant, lease)
		return runDTO{}, err
	}
	// Mint the inference credential (stream-json only; remote-control uses the
	// operator's subscription OAuth, which does not mint — §0/§5 of the contract).
	cred, err := m.maybeMint(ctx, tenant, runRef, p.Transport)
	if err != nil {
		m.releaseLaunchClaim(ctx, tenant, lease)
		return runDTO{}, err
	}
	runtimeCreds, err := m.mintRuntimeCredentials(ctx, tenant, runRef, intent.AgentRef, lease)
	if err != nil {
		m.releaseLaunchClaim(ctx, tenant, lease)
		return runDTO{}, err
	}
	runtimeCreds.launchID = model.NewID()

	// Persist the PENDING row + the 'created' ledger event (one transaction), with the
	// admission stamp written under the claim's own authority: the claim row is re-read
	// and CAS-updated in this SAME transaction (F3), so a takeover that commits in the
	// window takes this write down with it rather than letting it land unauthorized.
	var workReservation *workLaunchReservation
	if work != nil {
		workReservation = &work.reservation
	}
	runID, err := m.persistCreateWithWork(
		ctx, tenant, runRef, p, cred.ID, cred.Scheme, govFactsFor(intent, pr), lease,
		workReservation, runtimeCreds,
	)
	if err != nil {
		revokeErr := m.revokeRuntimeCredentialSet(ctx, runtimeCreds, false).err
		m.releaseLaunchClaim(ctx, tenant, lease)
		if work != nil && errors.Is(err, store.ErrConflict) {
			if existing, found, lookupErr := m.findWorkLaunchByDispatch(
				ctx, tenant, work.reservation.dispatchKey,
			); lookupErr != nil {
				return runDTO{}, errors.Join(lookupErr, revokeErr)
			} else if found {
				if replayErr := validateWorkLaunchReplay(existing, work.reservation); replayErr != nil {
					return runDTO{}, errors.Join(replayErr, revokeErr)
				}
				work.replayed = true
				return m.toRunDTO(existing), revokeErr
			}
		}
		return runDTO{}, errors.Join(err, revokeErr)
	}
	if work != nil {
		if err := m.bindWorkLaunch(
			ctx, tenant, p, work.reservation, runRef, lease,
		); err != nil {
			cleanupCtx := context.WithoutCancel(ctx)
			revokeErr := m.revokeRuntimeCredentialSet(cleanupCtx, runtimeCreds, true).err
			m.releaseLaunchClaim(cleanupCtx, tenant, lease)
			workLeaseErr := m.OwnerDied(
				cleanupCtx, tenant, lease.SID, runRef, "runtime_launch_bind_failed",
			)
			_, transitionErr := m.transition(cleanupCtx, tenant, runRef, transitionInput{
				event: "failed", toState: stateFailed,
				detail: "work launch bind failed", actor: p.Actor, actorKind: p.ActorKind,
				guard: guardRuntimeLaunch(runtimeCreds.launchID),
				mutate: func(rec model.Record) {
					rec[colExitCode] = int64(-1)
					rec[colRuntimeLaunchID] = nil
				},
			})
			return runDTO{}, errors.Join(err, revokeErr, workLeaseErr, transitionErr)
		}
	}

	// Launch under a PER-RUN background context (NOT the request ctx) so the process
	// outlives the create request; the lifecycle-persisting writes use a
	// non-cancellable context so a client disconnect cannot strand a launched
	// process with an un-finalized row.
	wctx := context.WithoutCancel(ctx)
	runCtx, cancel := context.WithCancel(context.Background())
	spec := m.buildLaunchSpec(
		p, cred, runtimeCreds.work, runtimeCreds.communication, "", ws, pr.injectEnv,
	)
	proc, lerr := m.rt.runner.Launch(runCtx, spec)
	if lerr != nil || proc == nil {
		// Runner saw both raw bearers in LaunchSpec. Its error is therefore treated
		// as secret-bearing even if the built-in runner currently returns safe text.
		m.warnf("session launch failed", "run_ref", runRef)
		var compensationErr error
		if proc != nil {
			// A Runner may legally return a process together with an error. Register
			// that child before attempting Stop so a failed Stop cannot make it
			// unenumerable. The bridge owns/drains Output and will confirm a later exit.
			lr := &liveRun{
				tenant: tenant, runRef: runRef, runID: runID, transport: p.Transport,
				proc: proc, ring: newOutputRing(m.rt.ringFrames, m.rt.ringBytes),
				cancel: cancel, finalizedCh: make(chan struct{}),
				recordIO: pr.recordIO, agentRef: intent.AgentRef, claim: lease,
				launchID:                        runtimeCreds.launchID,
				workCredentialID:                runtimeCreds.work.ID,
				workCredentialNotAfter:          runtimeCreds.work.NotAfter,
				communicationWorkspaceID:        runtimeCreds.communication.WorkspaceID,
				communicationCredentialID:       runtimeCreds.communication.ID,
				communicationCredentialNotAfter: runtimeCreds.communication.NotAfter,
				stopRequested:                   true, stopReason: "launch_error", launchFailed: true,
			}
			m.rt.putLive(lr)
			go m.bridge(lr)
			stopConfirmed, stopErr := m.teardownLiveWithContext(wctx, lr)
			compensationErr = stopErr
			if !stopConfirmed {
				// Keep Claim + pending generation intact. A later Stop/promotion retries
				// the same registered child; no successor can be admitted meanwhile.
				return runDTO{}, errors.Join(
					&runErr{http.StatusBadGateway, "launch failed (the session runner could not start the process)"},
					compensationErr,
				)
			}
		} else {
			cancel()
			compensationErr = m.revokeRuntimeCredentialSet(wctx, runtimeCreds, true).err
		}
		// No process, so no bridge and no finalize to give the claim back: release it
		// here or the session stays held for the rest of its TTL by a launch that
		// never happened. Caught by contrast.
		m.releaseLaunchClaim(wctx, tenant, lease)
		var workLeaseErr error
		if work != nil && lease.SID != "" {
			workLeaseErr = m.OwnerDied(
				wctx, tenant, lease.SID, runRef, "runtime_launch_failed",
			)
		}
		_, transitionErr := m.transition(wctx, tenant, runRef, transitionInput{
			event: "failed", toState: stateFailed,
			detail: "launch failed", actor: p.Actor, actorKind: p.ActorKind,
			guard: guardRuntimeLaunch(runtimeCreds.launchID),
			mutate: func(rec model.Record) {
				rec[colExitCode] = int64(-1)
				rec[colRuntimeLaunchID] = nil
			},
		})
		return runDTO{}, errors.Join(
			&runErr{http.StatusBadGateway, "launch failed (the session runner could not start the process)"},
			compensationErr, workLeaseErr, transitionErr,
		)
	}

	lr := &liveRun{
		tenant: tenant, runRef: runRef, runID: runID, transport: p.Transport,
		proc: proc, ring: newOutputRing(m.rt.ringFrames, m.rt.ringBytes),
		cancel: cancel, finalizedCh: make(chan struct{}),
		recordIO: pr.recordIO, agentRef: intent.AgentRef, claim: lease,
		launchID:                        runtimeCreds.launchID,
		workCredentialID:                runtimeCreds.work.ID,
		workCredentialNotAfter:          runtimeCreds.work.NotAfter,
		communicationWorkspaceID:        runtimeCreds.communication.WorkspaceID,
		communicationCredentialID:       runtimeCreds.communication.ID,
		communicationCredentialNotAfter: runtimeCreds.communication.NotAfter,
	}
	m.rt.putLive(lr)
	m.startRuntimeCredentialHeartbeat(lr)
	// ⛔ LA VENTANA SE ABRE ANTES DE ARRANCAR EL BRIDGE, no despues: si se abriera despues,
	// los frames que llegaran en medio se aplicarian directos y competirian con el CAS de la
	// reserva — que es exactamente la carrera que esto cierra. Y se vuelca SIEMPRE al salir,
	// commitee o falle: si fallara sin vaciar, los efectos quedarian encolados para siempre
	// y `last_activity_at` se congelaria en el instante del lanzamiento — un run vivo
	// pareceria ocioso, que es peor que la carrera original.
	lr.abreVentanaDeReserva()
	defer lr.cierraVentanaYVuelca()
	go m.bridge(lr)
	m.armRunDeadline(lr, p.MaxDuration)

	// Transition PENDING → RUNNING (+ pid, started_at, 'launched' event). The guard
	// permits 'launched' only FROM pending: if the process already exited and the
	// bridge finalized the row to a terminal state, this loses the race — return the
	// row's TRUE state rather than resurrecting a dead session to running.
	rec, err := m.transition(wctx, tenant, runRef, transitionInput{
		event: "launched", toState: stateRunning,
		detail: joinDetail(pr.contextPolicySummary, templateDetail(tpl, tplConflicts)),
		actor:  p.Actor, actorKind: p.ActorKind, guard: guardRuntimeLaunch(runtimeCreds.launchID),
		mutate: func(rec model.Record) {
			rec[colStartedAt] = model.NewTimestamp(m.now()).String()
			rec[colLastActivityAt] = model.NewTimestamp(m.now()).String()
			if pid := proc.PID(); pid > 0 {
				rec[colPID] = int64(pid)
			}
		},
	})
	if err != nil {
		stopped, teardownErr := m.teardownLiveWithContext(wctx, lr)
		if isRunConflict(err) {
			return m.racedFinalizeDTO(wctx, tenant, runRef, lr, stopped, err, teardownErr)
		}
		return runDTO{}, errors.Join(err, teardownErr)
	}
	return m.toRunDTO(rec), nil
}

// resumeRun relaunches a stopped session against its persisted claude_session_id.
// resumeRun relaunches a stopped run. agentIdentity is the AUTHENTICATED agent
// driving the resume (empty for a human): setRunGovFacts re-derives the
// governance posture on every resume with setOrNull, so without it a resume
// would ERASE an agent run's attribution — and with it, a genuinely human resume
// still clears an attribution that no longer describes the driver, which is the
// behavior runtime_governance_test.go already pins.
func (m *Module) resumeRun(ctx context.Context, tenant model.TenantID, runRef, actor, actorKind, agentIdentity string) (runDTO, error) {
	defer m.rt.lockRun(liveKey(tenant, runRef))() // serialize with stop/cleanup/delete on this run
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return runDTO{}, err
	}
	if err := m.refuseLegacyControlUnderWork(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	switch rec.String(colState) {
	case stateStopped, stateFailed:
		// resumable
	default:
		return runDTO{}, conflictErr("session must be stopped/failed to resume (state=" + rec.String(colState) + ")")
	}
	transport := Transport(rec.String(colTransport))
	claudeID := rec.String(colClaudeSessionID)
	if transport == TransportStreamJSON && claudeID == "" {
		return runDTO{}, conflictErr("session has no captured Claude session id to resume")
	}
	// re-resolve the workspace on resume (it may have been disabled/deregistered
	// or its root may be unavailable on this node — fail the resume deny-closed). Done
	// BEFORE the gates so the CRITICAL determination sees the workspace posture.
	ws, err := m.resolveLaunchWorkspace(ctx, tenant, rec.String(colWorkspaceRef))
	if err != nil {
		return runDTO{}, err
	}
	p := CreateRunParams{
		Transport: transport, PermissionMode: rec.String(colPermissionMode),
		Effort: rec.String(colEffort), Model: rec.String(colRunModelRef),
		WorkspaceRef: rec.String(colWorkspaceRef), Isolation: Isolation(rec.String(colIsolation)),
		// UNIÓN de las dos ramas, no elección: K2 añade `AgentRef` —la identidad de agente
		// AUTENTICADA sobre la que decide `sessionStopGate.Check`— y añade `TemplateID`
		// más la re-resolución de la plantilla al reanudar. Son propiedades independientes:
		// quedarse con un lado deja la reanudación sin identidad o sin postura vigente.
		Name: rec.String(colRunName), Actor: actor, ActorKind: actorKind, AgentRef: agentIdentity,
		TemplateID: rec.String(colTemplateID),
	}
	// the template is RE-RESOLVED on resume, exactly as the governance gates are
	// re-run below, and for the same reason — the posture a session runs under is the
	// CURRENT one, not the one it was born with. A template tightened (or archived, or
	// edited into something this runtime cannot keep) since the last launch therefore
	// refuses the resume instead of relaunching under terms nobody holds any more.
	//
	// The stored permission_mode/effort/model are the PREVIOUS merge's output, so they
	// re-enter applyTo as if the caller had asked for them: a term that has not changed
	// matches and reports nothing, and one that HAS changed reports the real conflict
	// between the running configuration and the current template.
	tpl, tplConflicts, err := m.applyLaunchTemplate(ctx, tenant, &p)
	if err != nil {
		return runDTO{}, err
	}
	// First and alone: a stopped estate must not even reach the admission plane,
	// which writes (SG-02-b).
	agentRef := agentIdentity
	if agentRef == "" && actorKind == model.ActorAgent {
		agentRef = actor
	}
	if err := m.preflightStop(ctx, tenant, StopDims{RunRef: runRef, AgentRef: agentRef}); err != nil {
		return runDTO{}, err
	}
	if err := m.ensureRuntimeCredentialWiring(); err != nil {
		return runDTO{}, err
	}
	// A previous process may have died while auth storage was unavailable. Retry
	// both durable handles before reserving a successor; successful revocations
	// clear only their own handles, while either failure keeps the run resumable
	// and denies this attempt.
	if err := m.revokeStoredRuntimeCredentials(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	previousState := rec.String(colState)
	launchID := model.NewID()
	// Cross-node reservation BEFORE Claim rotation and every mint. The in-process
	// opLock is intentionally insufficient in HA: only this row transition is
	// shared by two Modules. Exactly one stopped/failed -> resuming transition can
	// commit; a loser returns 409 without acquiring (and therefore without
	// releasing) the winner's Claim generation.
	if _, err := m.transition(ctx, tenant, runRef, transitionInput{
		event: "resuming", toState: statePending,
		actor: actor, actorKind: actorKind,
		mutate: func(record model.Record) { record[colRuntimeLaunchID] = launchID.String() },
	}); err != nil {
		return runDTO{}, err
	}
	abortReservation := func(cause error, ownedLease Lease) (runDTO, error) {
		if err := m.assertRuntimeLaunchID(
			context.WithoutCancel(ctx), tenant, runRef, launchID,
		); err != nil {
			return runDTO{}, errors.Join(cause, err)
		}
		m.releaseLaunchClaim(ctx, tenant, ownedLease)
		_, transitionErr := m.transition(context.WithoutCancel(ctx), tenant, runRef, transitionInput{
			event: "resume_aborted", toState: previousState,
			detail: "resume refused before process launch", actor: actor, actorKind: actorKind,
			guard:  guardRuntimeLaunch(launchID),
			mutate: func(record model.Record) { record[colRuntimeLaunchID] = nil },
		})
		return runDTO{}, errors.Join(cause, transitionErr)
	}
	// SG-02-b: acquire the claim on the EXISTING session. Unlike a create, this can
	// genuinely fail — another holder with a live lease refuses it — and that refusal
	// is the control. A lapsed or released claim is taken over here and the fence moves,
	// which is exactly what invalidates whatever the previous holder still carries.
	lease, err := m.admitResume(ctx, tenant, runRef, actor, rec.Int(colClaimFence))
	if err != nil {
		return abortReservation(err, Lease{})
	}
	if err := m.assertRuntimeLaunchID(ctx, tenant, runRef, launchID); err != nil {
		return runDTO{}, err // stale attempt: its Claim may be shared with a successor
	}
	if err := m.stampResumeClaimReservation(
		ctx, tenant, runRef, agentRef, lease, launchID,
	); err != nil {
		return abortReservation(err, lease)
	}
	// governance pre-flight (budget/HITL/PEP), re-run on resume so a budget/policy
	// change since the last launch is honored. recordIO is re-derived from the run's
	// CRITICAL posture (permission_mode + workspace).
	intent := launchIntentFor(LaunchActionResume, runRef, p, ws, lease)
	pr, err := m.preflight(ctx, tenant, intent, StopDims{RunRef: runRef, AgentRef: intent.AgentRef})
	if err != nil {
		return abortReservation(err, lease)
	}
	cred, err := m.maybeMint(ctx, tenant, runRef, transport)
	if err != nil {
		return abortReservation(err, lease)
	}
	runtimeCreds, err := m.mintRuntimeCredentials(ctx, tenant, runRef, intent.AgentRef, lease)
	if err != nil {
		return abortReservation(err, lease)
	}
	runtimeCreds.launchID = launchID
	wctx := context.WithoutCancel(ctx)
	// SG-02-b / F3: the LAST durable act before the process exists is a fenced write.
	// Without it the child would start and only a later transition could discover that
	// authority had moved — compensation after the effect, not admission before it. A
	// spawn cannot be enrolled in a transaction, so the window between this commit and
	// the exec is bounded, not closed; the same residual this repo already declares for
	// its evidence fence (core/store/evidenceops.go:444-449).
	if err := m.persistResumeRuntimeCredentials(
		ctx, tenant, runRef, intent.AgentRef, lease, runtimeCreds,
	); err != nil {
		revokeErr := m.revokeRuntimeCredentialSet(ctx, runtimeCreds, false).err
		return abortReservation(errors.Join(err, revokeErr), lease)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	spec := m.buildLaunchSpec(
		p, cred, runtimeCreds.work, runtimeCreds.communication, claudeID, ws, pr.injectEnv,
	)
	proc, lerr := m.rt.runner.Launch(runCtx, spec)
	if lerr != nil || proc == nil {
		m.warnf("session resume launch failed", "run_ref", runRef)
		var compensationErr error
		if proc != nil {
			lr := &liveRun{
				tenant: tenant, runRef: runRef, runID: model.ID(rec.String(model.ColID)), transport: transport,
				proc: proc, ring: newOutputRing(m.rt.ringFrames, m.rt.ringBytes),
				cancel: cancel, finalizedCh: make(chan struct{}),
				recordIO: pr.recordIO, agentRef: intent.AgentRef, claim: lease,
				launchID:                        launchID,
				workCredentialID:                runtimeCreds.work.ID,
				workCredentialNotAfter:          runtimeCreds.work.NotAfter,
				communicationWorkspaceID:        runtimeCreds.communication.WorkspaceID,
				communicationCredentialID:       runtimeCreds.communication.ID,
				communicationCredentialNotAfter: runtimeCreds.communication.NotAfter,
				stopRequested:                   true, stopReason: "launch_error", launchFailed: true,
			}
			m.rt.putLive(lr)
			go m.bridge(lr)
			stopConfirmed, stopErr := m.teardownLiveWithContext(wctx, lr)
			compensationErr = stopErr
			if !stopConfirmed {
				return runDTO{}, errors.Join(
					&runErr{http.StatusBadGateway, "resume launch failed (the session runner could not start the process)"},
					compensationErr,
				)
			}
		} else {
			cancel()
			compensationErr = m.revokeRuntimeCredentialSet(wctx, runtimeCreds, true).err
		}
		m.releaseLaunchClaim(wctx, tenant, lease) // same reason as the create path
		_, transitionErr := m.transition(wctx, tenant, runRef, transitionInput{
			event: "failed", toState: stateFailed,
			detail: "resume launch failed", actor: actor, actorKind: actorKind,
			guard:  guardRuntimeLaunch(launchID),
			mutate: func(record model.Record) { record[colRuntimeLaunchID] = nil },
		})
		return runDTO{}, errors.Join(
			&runErr{http.StatusBadGateway, "resume launch failed (the session runner could not start the process)"},
			compensationErr, transitionErr,
		)
	}
	lr := &liveRun{
		tenant: tenant, runRef: runRef, runID: model.ID(rec.String(model.ColID)), transport: transport,
		proc: proc, ring: newOutputRing(m.rt.ringFrames, m.rt.ringBytes),
		cancel: cancel, finalizedCh: make(chan struct{}),
		recordIO: pr.recordIO, agentRef: intent.AgentRef, claim: lease,
		launchID:                        launchID,
		workCredentialID:                runtimeCreds.work.ID,
		workCredentialNotAfter:          runtimeCreds.work.NotAfter,
		communicationWorkspaceID:        runtimeCreds.communication.WorkspaceID,
		communicationCredentialID:       runtimeCreds.communication.ID,
		communicationCredentialNotAfter: runtimeCreds.communication.NotAfter,
	}
	m.rt.putLive(lr)
	m.startRuntimeCredentialHeartbeat(lr)
	// ⛔ LA VENTANA SE ABRE ANTES DE ARRANCAR EL BRIDGE, no despues: si se abriera despues,
	// los frames que llegaran en medio se aplicarian directos y competirian con el CAS de la
	// reserva — que es exactamente la carrera que esto cierra. Y se vuelca SIEMPRE al salir,
	// commitee o falle: si fallara sin vaciar, los efectos quedarian encolados para siempre
	// y `last_activity_at` se congelaria en el instante del lanzamiento — un run vivo
	// pareceria ocioso, que es peor que la carrera original.
	lr.abreVentanaDeReserva()
	defer lr.cierraVentanaYVuelca()
	go m.bridge(lr)
	m.armRunDeadline(lr, p.MaxDuration)

	gf := govFactsFor(intent, pr)
	updated, err := m.transition(wctx, tenant, runRef, transitionInput{
		event: "resumed", toState: stateRunning,
		detail: templateDetail(tpl, tplConflicts),
		actor:  actor, actorKind: actorKind, lease: lease, guard: guardRuntimeLaunch(launchID),
		mutate: func(rec model.Record) {
			rec[colStartedAt] = model.NewTimestamp(m.now()).String()
			rec[colLastActivityAt] = model.NewTimestamp(m.now()).String()
			rec[colExitCode] = nil
			rec[colStoppedAt] = nil
			if pid := proc.PID(); pid > 0 {
				rec[colPID] = int64(pid)
			}
			// the RE-MERGED launch parameters. The child was just spawned from `p`,
			// so leaving the row on the previous merge's values would make the panel
			// describe a configuration this process is not running under — and would feed
			// the NEXT resume a stale baseline.
			rec[colPermissionMode] = p.PermissionMode
			setOrNull(rec, colEffort, p.Effort)
			setOrNull(rec, colRunModelRef, p.Model)
			if p.TemplateID != "" {
				rec[colTemplateVersion] = p.TemplateVersion
			}
			if p.MaxDuration > 0 {
				rec[colTemplateCeiling] = int64(p.MaxDuration / time.Second)
			} else {
				rec[colTemplateCeiling] = nil
			}
			// Re-derived governance facts (a budget/policy/workspace change since the last
			// launch is reflected on resume — the panel always shows the current posture).
			setRunGovFacts(rec, gf)
		},
	})
	if err != nil {
		stopped, teardownErr := m.teardownLiveWithContext(wctx, lr)
		if isRunConflict(err) {
			return m.racedFinalizeDTO(wctx, tenant, runRef, lr, stopped, err, teardownErr)
		}
		return runDTO{}, errors.Join(err, teardownErr)
	}
	return m.toRunDTO(updated), nil
}

// stopRun signals a graceful stop and waits for the bridge to finalize the row.
func (m *Module) stopRun(ctx context.Context, tenant model.TenantID, runRef, actor, actorKind string) (runDTO, error) {
	defer m.rt.lockRun(liveKey(tenant, runRef))() // serialize with resume/cleanup/delete on this run
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return runDTO{}, err
	}
	if err := m.refuseLegacyControlUnderWork(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	dto, _, err := m.stopRunLoaded(ctx, tenant, runRef, actor, actorKind, "", rec)
	return dto, err
}

// stopRunLoaded reports whether it crossed the external-effect boundary while
// its caller holds the per-run operation lock. Keeping authority checks outside
// this helper lets the legacy route retain its error taxonomy while StopForWork
// distinguishes a pre-effect refusal from an uncertain attempted stop.
func (m *Module) stopRunLoaded(ctx context.Context, tenant model.TenantID, runRef, actor, actorKind, detail string, rec model.Record) (runDTO, bool, error) {
	if m.rt.communicationCredentialsEnabled && rec.String(colState) == statePending &&
		rec.String(colRuntimeLaunchID) != "" {
		// A pending row with a generation token is an in-flight create/resume on
		// this or another node, not evidence of an orphan. Moving it to stopped
		// would recycle the reservation while its issuers can still be in flight.
		// Promotion recovery is the only path allowed to evict it: that path first
		// fences the old leader and revokes both durable handles before serving.
		if _, locallySupervised := m.rt.getLive(tenant, runRef); !locallySupervised {
			return runDTO{}, false, conflictErr("session launch is being prepared; retry stop")
		}
		// A Runner that returned (Process, error), or a post-Launch transition
		// whose Stop failed, deliberately remains pending with a local handle.
		// Retrying Stop on that exact child is the only safe way to discharge its
		// custody; the launch-id guard on finalize still fences any successor.
	}
	// Idempotent: a stop on an already-terminal session is a no-op (a live handle
	// may linger after a natural exit so the attach tail stays replayable).
	switch rec.String(colState) {
	case stateStopped, stateFailed, stateCleaned:
		if err := m.revokeStoredRuntimeCredentials(ctx, tenant, rec); err != nil {
			return runDTO{}, false, err
		}
		refreshed, err := m.loadRun(ctx, tenant, runRef)
		if err != nil {
			return runDTO{}, false, err
		}
		return m.toRunDTO(refreshed), false, nil
	}
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		if m.rt.communicationCredentialsEnabled && rec.String(colState) == stateRunning &&
			rec.String(colRuntimeLaunchID) != "" {
			// A row owned by another runtime is not proof of an orphan. Only the
			// leader-promotion recovery path may fence its dual authority and make
			// it terminal before the new leader starts serving lifecycle calls.
			return runDTO{}, false, conflictErr(
				"session is supervised by another runtime; leader recovery is required",
			)
		}
		// No live handle: the row is non-terminal but orphaned (process not tracked —
		// e.g. after a runtime restart). Reconcile honestly.
		dto, err := m.reconcileTerminal(ctx, tenant, runRef, rec, actor, actorKind)
		return dto, true, err
	}
	// Record WHO requested the stop, then signal and wait for finalize.
	if _, err := m.transition(ctx, tenant, runRef, transitionInput{
		event: "stopping", actor: actor, actorKind: actorKind, detail: detail,
	}); err != nil {
		return runDTO{}, false, err
	}
	lr.mu.Lock()
	lr.stopRequested = true
	if detail != "" {
		lr.stopReason = detail
	}
	lr.mu.Unlock()
	stopErr := secretSafeCredentialError("session process stop", lr.proc.Stop(ctx))
	revokeErr := m.revokeLiveRuntimeCredentials(ctx, lr)
	select {
	case <-lr.finalizedCh:
	case <-ctx.Done():
		return runDTO{}, true, errors.Join(ctx.Err(), stopErr, revokeErr)
	case <-time.After(2 * m.rt.waitDelay):
		// finalize is taking too long; return the current state honestly.
	}
	dto, err := m.getRun(ctx, tenant, runRef)
	return dto, true, errors.Join(err, stopErr, revokeErr)
}

// cleanupRun releases a stopped session: it blocks resume (clears the Claude
// session id) and marks the row cleaned. The PHYSICAL transcript purge in
// ~/.claude is a documented best-effort gap hardened in — v1 does NOT claim
// to have purged on-disk state, it records the operator's intent to release.
func (m *Module) cleanupRun(ctx context.Context, tenant model.TenantID, runRef, actor, actorKind string) (runDTO, error) {
	defer m.rt.lockRun(liveKey(tenant, runRef))() // serialize with resume/stop/delete on this run
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return runDTO{}, err
	}
	state := rec.String(colState)
	if state != stateStopped && state != stateFailed {
		return runDTO{}, conflictErr("session must be stopped/failed to clean up (state=" + state + ")")
	}
	if err := m.revokeStoredRuntimeCredentials(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	m.rt.dropLive(tenant, runRef) // free the ring (the process is already gone)
	updated, err := m.transition(ctx, tenant, runRef, transitionInput{
		event: "cleaned", toState: stateCleaned,
		detail: "session released (on-disk transcript purge deferred)",
		actor:  actor, actorKind: actorKind,
		mutate: func(rec model.Record) { rec[colClaudeSessionID] = nil },
	})
	if err != nil {
		return runDTO{}, err
	}
	return m.toRunDTO(updated), nil
}

// deleteRun removes a cleaned session's row. The append-only run_event ledger is
// immutable and is intentionally NOT deleted — it remains as permanent evidence.
func (m *Module) deleteRun(ctx context.Context, tenant model.TenantID, runRef string) error {
	defer m.rt.lockRun(liveKey(tenant, runRef))() // serialize with resume/stop/cleanup on this run
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		// Re-read and re-validate the precondition INSIDE the mutation (no TOCTOU).
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		if rec.String(colState) != stateCleaned {
			return conflictErr("session must be cleaned before delete (state=" + rec.String(colState) + ")")
		}
		if rec.String(colWorkCredentialID) != "" ||
			rec.String(colCommunicationCredentialID) != "" {
			return conflictErr("session still has runtime credential handles; cleanup/revocation required")
		}
		id, perr := model.ParseID(rec.String(model.ColID))
		if perr != nil {
			return badRequest("malformed run id")
		}
		return repo.Delete(ctx, id)
	})
}

// sendInput writes one NDJSON line to a live session's stdin.
func (m *Module) sendInput(ctx context.Context, tenant model.TenantID, runRef string, line []byte) error {
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return err
	}
	if err := m.refuseLegacyControlUnderWork(ctx, tenant, rec); err != nil {
		return err
	}
	_, err = m.sendInputLoaded(ctx, tenant, runRef, line, rec)
	return err
}

// sendInputLoaded reports whether Process.Send was invoked after the caller
// selected and authorized the control plane. InputForWork uses that fact to
// distinguish a pre-effect rejection from an uncertain attempted write; the
// legacy route deliberately discards it and preserves its existing taxonomy.
func (m *Module) sendInputLoaded(ctx context.Context, tenant model.TenantID, runRef string, line []byte, rec model.Record) (bool, error) {
	if Transport(rec.String(colTransport)) == TransportRemoteControl {
		return false, conflictErr("remote-control sessions do not bridge input (I/O is relayed to Anthropic cloud)")
	}
	if rec.String(colState) != stateRunning {
		return false, conflictErr("session is not running (state=" + rec.String(colState) + ")")
	}
	// One POST = one NDJSON line. Reject embedded newlines so a single governed
	// input action cannot smuggle MULTIPLE stream-json messages onto the child stdin
	// (the {"message": …} path is already newline-free via json.Compact).
	if bytes.IndexByte(line, '\n') >= 0 || bytes.IndexByte(line, '\r') >= 0 {
		return false, badRequest("input must be a single line (no embedded newlines)")
	}
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		return false, conflictErr("session is not live (state=" + rec.String(colState) + ")")
	}
	if err := lr.proc.Send(ctx, line); err != nil {
		// Process has received the complete LaunchSpec and may echo either bearer
		// in an error. Keep provider text out of logs and API errors.
		m.warnf("session input rejected", "run_ref", runRef)
		return true, badRequest("input rejected (the session is not accepting input)")
	}
	return true, nil
}

// preflightResult carries the governance instructions a passing preflight produces
// for the launch: the PEP environment to inject into the child and whether to
// record the bridged I/O as governed ledger evidence, plus the non-sensitive
// context-policy summary to record in the lifecycle ledger. It also carries the
// non-sensitive governance FACTS the runtime persists on the run for the portal's
// per-session panel: whether PEP env was provisioned, the CRITICAL posture and
// the HITL approval ref.
type preflightResult struct {
	injectEnv            []EnvVar
	contextPolicySummary string
	recordIO             bool
	// pepProvisioned is derived: the gate injected PEP env, so the managed PreToolUse
	// hook can reach the governed PEP (its tool-calls are policed in line). Empty env ⇒
	// the hook is deny-closed per-tool (Q4 2026-06-16).
	pepProvisioned bool
	critical       bool
	approvalRef    string
}

const launchPEPEnvURL = "OLIVARES_HOOK_PEP_URL"

func pepEnvProvisioned(env []EnvVar) bool {
	for _, e := range env {
		if e.Name == launchPEPEnvURL {
			return true
		}
	}
	return false
}

// runGovFacts is the non-sensitive governance posture persisted on the run (panel).
type runGovFacts struct {
	agentRef       string
	pepProvisioned bool
	recordIO       bool
	approvalRef    string
	critical       bool
}

// govFactsFor gathers the run's persisted governance facts from the launch intent
// (agent dimension) and the preflight verdict (PEP/record/CRITICAL/approval).
func govFactsFor(intent LaunchIntent, pr preflightResult) runGovFacts {
	return runGovFacts{
		agentRef:       intent.AgentRef,
		pepProvisioned: pr.pepProvisioned,
		recordIO:       pr.recordIO,
		approvalRef:    pr.approvalRef,
		critical:       pr.critical,
	}
}

// setRunGovFacts writes the governance facts onto a run record. The text refs are set
// UNCONDITIONALLY (empty ⇒ NULL, not skipped) so a RESUME re-deriving the posture clears
// a stale ref — e.g. a run first launched CRITICAL (approval_ref set) then resumed under
// a now-non-critical posture must not keep a dangling approval_ref. Using setIf here
// would leave the old value; the panel must always show the CURRENT posture.
func setRunGovFacts(rec model.Record, gf runGovFacts) {
	setOrNull(rec, colRunAgentRef, gf.agentRef)
	setOrNull(rec, colApprovalRef, gf.approvalRef)
	rec[colPEPProvisioned] = gf.pepProvisioned
	rec[colRecordIO] = gf.recordIO
	rec[colCritical] = gf.critical
}

// setOrNull writes v, or clears the column to NULL when v is empty (so a re-derivation
// can erase a stale value, unlike setIf which leaves it untouched).
func setOrNull(rec model.Record, col, v string) {
	if v == "" {
		rec[col] = nil
		return
	}
	rec[col] = v
}

// preflight runs the deny-closed StopGate (kill-switch) FIRST and the LaunchGate
// (budget + CRITICAL-launch HITL + PEP provisioning) SECOND. The order
// is load-bearing: an emergency stop OUTRANKS every other control, including the
// HITL/break-glass the LaunchGate may open (2026-06-16: stop > break-glass), so
// a stopped estate denies the launch before any approval is opened. Both gates are
// deny-closed when wired (the StopGate treats an unreadable state as stopped; a
// LaunchGate error or Allowed=false blocks). On success it returns the governance
// instructions the runtime applies to the launch.
func (m *Module) preflight(ctx context.Context, tenant model.TenantID, intent LaunchIntent, dims StopDims) (preflightResult, error) {
	if err := m.preflightStop(ctx, tenant, dims); err != nil {
		return preflightResult{}, err
	}
	dec, err := m.rt.launchGate.Authorize(ctx, tenant, intent)
	if err != nil {
		// A gate that ERRORED is not a gate that decided, and the two used to answer
		// the same 403. Deny-closed either way; the classification travels (P1-N1).
		return preflightResult{}, denyClosedErr("launch gate error", err)
	}
	if !dec.Allowed {
		reason := dec.Reason
		if reason == "" {
			reason = "denied by policy"
		}
		status := dec.DeniedStatus
		if status == 0 {
			status = http.StatusForbidden
		}
		return preflightResult{}, &runErr{status, "launch denied: " + reason}
	}
	if err := validateLaunchInjectedEnv(dec.InjectEnv); err != nil {
		return preflightResult{}, denyClosedErr("launch gate returned an invalid environment", err)
	}
	return preflightResult{
		injectEnv:            dec.InjectEnv,
		contextPolicySummary: dec.ContextPolicySummary,
		// the launch gate may only ADD recording, never remove a request for it.
		//
		// The composed gate already ORs its CRITICAL floor over intent.RecordRequested
		// (cmd/olivares/sessiongov.go), so in the product this changes nothing. It matters
		// for the MODULE's own contract: the unwired default gate returns a bare
		// {Allowed:true}, so before this line a template that mandates I/O recording was
		// accepted on a standalone node and silently not honored — the same "declared and
		// not applied" defect this pack exists to end, one layer down.
		//
		// The direction is the whole argument. Turning evidence ON widens nothing, so
		// there is no posture in which a gate needs the power to switch it off; and if one
		// ever did, it would have to say so with a field of its own rather than by
		// omission.
		recordIO:       dec.RecordIO || intent.RecordRequested,
		pepProvisioned: pepEnvProvisioned(dec.InjectEnv),
		critical:       dec.Critical,
		approvalRef:    dec.ApprovalRef,
	}, nil
}

func validateLaunchInjectedEnv(env []EnvVar) error {
	if len(env) > 64 {
		return errors.New("too many injected environment values")
	}
	seen := make(map[string]bool, len(env))
	for _, item := range env {
		name := item.Name
		reserved := name == "ANTHROPIC_AUTH_TOKEN" || name == "ANTHROPIC_BASE_URL" ||
			name == "DISABLE_AUTOUPDATER" || name == "OLIVARES_COMMUNICATION_TOKEN" ||
			strings.HasPrefix(name, "OLIVARES_WORK_") ||
			strings.HasPrefix(name, "CLAUDE_CODE_")
		if !validEnvName(name) || reserved || seen[name] || strings.ContainsRune(item.Value, '\x00') ||
			len(item.Value) > 64*1024 {
			return errors.New("invalid, duplicate, or runtime-reserved injected environment value")
		}
		seen[name] = true
	}
	return nil
}

// preflightStop is the kill-switch half of the pre-flight, split out so a caller
// can run it BEFORE anything durable happens. It is deny-closed on both paths: an
// unreadable stop state is treated as stopped, and an active stop refuses.
//
// SG-02-b split it from preflight because the admission preamble writes (it mints
// identity and takes a claim). A kill-switched estate must write nothing at all, so
// stop has to be consulted before the preamble, not merely before the launch gate.
func (m *Module) preflightStop(ctx context.Context, tenant model.TenantID, dims StopDims) error {
	sd, err := m.rt.stopGate.Check(ctx, tenant, dims)
	if err != nil {
		// Deny-closed is unchanged — an unreadable stop state still refuses. What the
		// caller is told is not: a kill-switch backend that is down is an outage, and
		// a 403 told an operator to stop retrying it (P1-R6-01).
		return denyClosedErr("kill-switch state unreadable; launch denied", err)
	}
	if sd.Stopped {
		return forbiddenErr("emergency stop active (" + sd.StopRef + "); launch denied until a dual-control re-enable")
	}
	return nil
}

// releaseLaunchClaim gives back a claim the admission preamble took for a launch
// that was then refused. Best-effort and loud on failure: a claim that could not be
// released holds the session until its lease lapses, which is recoverable, but it
// is not something to discover from an operator's complaint.
func (m *Module) releaseLaunchClaim(ctx context.Context, tenant model.TenantID, lease Lease) {
	if lease.SID == "" || lease.Holder == "" {
		return
	}
	if err := m.Release(context.WithoutCancel(ctx), tenant, lease.SID, lease.Holder, lease.Fence); err != nil {
		m.warnf("sessions: could not release the claim of a refused launch",
			"sid", lease.SID, "err", redactErr(err))
	}
}

// maybeMint mints the inference credential for a stream-json launch; for
// remote-control it returns an empty credential (operator-provided OAuth).
func (m *Module) maybeMint(ctx context.Context, tenant model.TenantID, runRef string, transport Transport) (Credential, error) {
	if transport != TransportStreamJSON {
		return Credential{}, nil
	}
	cred, err := m.rt.creds.Mint(ctx, CredentialRequest{Tenant: tenant, RunRef: runRef, Transport: transport})
	if err != nil {
		// Same rule as the admission preamble, and this site is why P1-N1 was not
		// closed by fixing that one: a minting backend that is merely unreachable is
		// not a decision about this launcher (P1-R6-01).
		return Credential{}, denyClosedErr("inference credential unavailable", err)
	}
	if cred.Expired(m.now()) {
		return Credential{}, forbiddenErr("minted inference credential is already expired (fail-closed)")
	}
	return cred, nil
}

func (m *Module) maybeMintWorkSession(
	ctx context.Context,
	tenant model.TenantID,
	runRef, agentRef string,
	lease Lease,
) (WorkSessionCredential, error) {
	if m.rt.workSessionCreds == nil || lease.SID == "" {
		return WorkSessionCredential{}, nil
	}
	req := workSessionCredentialRequest(tenant, runRef, agentRef, lease)
	cred, err := m.rt.workSessionCreds.Mint(ctx, req)
	if err != nil {
		revokeErr := m.revokeWorkSessionCredential(ctx, cred.ID, req)
		return WorkSessionCredential{}, errors.Join(
			denyClosedErr("work-session credential unavailable",
				secretSafeCredentialError("work credential mint", err)),
			wrapCredentialCompensation("revoke work credential returned with mint error", revokeErr),
		)
	}
	if cred.ID.IsZero() || cred.Token == "" || cred.Expired(m.now()) ||
		cred.Tenant != tenant || cred.SessionRef != lease.SID || cred.RunRef != runRef ||
		cred.AgentRef != agentRef || cred.ClaimFence != lease.Fence {
		revokeErr := m.revokeWorkSessionCredential(ctx, cred.ID, req)
		return WorkSessionCredential{}, errors.Join(
			forbiddenErr("invalid work-session credential (fail-closed)"),
			wrapCredentialCompensation("revoke invalid work credential", revokeErr),
		)
	}
	return cred, nil
}

func workSessionCredentialRequest(
	tenant model.TenantID,
	runRef, agentRef string,
	lease Lease,
) WorkSessionCredentialRequest {
	return WorkSessionCredentialRequest{
		Tenant: tenant, SessionRef: lease.SID, RunRef: runRef, AgentRef: agentRef,
		ClaimFence: lease.Fence,
	}
}

func (m *Module) revokeWorkSessionCredential(
	ctx context.Context,
	id model.ID,
	expected WorkSessionCredentialRequest,
) error {
	if id.IsZero() {
		return nil
	}
	source := m.rt.workSessionCreds
	if runtimeCredentialRecovery(ctx) && m.recoveryData != nil {
		source = m.rt.recoveryWorkSessionCreds
	}
	if source == nil {
		return &runErr{http.StatusServiceUnavailable, "work-session credential issuer is not available"}
	}
	if err := source.Revoke(context.WithoutCancel(ctx), id, expected); err != nil {
		m.warnf("sessions: could not revoke work-session credential", "credential_id", id.String())
		return secretSafeCredentialError("work credential revoke", err)
	}
	return nil
}

func (m *Module) revokeLiveWorkSessionCredential(ctx context.Context, lr *liveRun) error {
	if lr == nil {
		return nil
	}
	lr.mu.Lock()
	id := lr.workCredentialID
	lr.mu.Unlock()
	if err := m.revokeWorkSessionCredential(ctx, id,
		workSessionCredentialRequest(lr.tenant, lr.runRef, lr.agentRef, lr.claim)); err != nil {
		return err
	}
	lr.mu.Lock()
	if lr.workCredentialID == id {
		lr.workCredentialID = ""
		lr.workCredentialNotAfter = time.Time{}
	}
	lr.mu.Unlock()
	return nil
}

// persistCreate inserts the pending row, seals the 'created' lifecycle event, and
// STAMPS the admission claim on the row — all conditioned on the launcher still
// holding that claim, inside one transaction (F3).
//
// This is the last durable act before the process is spawned, which is why the
// fence belongs here: a takeover that commits between admission and this write
// turns the claim CAS into a version conflict and takes the whole row with it, so
// nothing is launched under authority that has already moved on.
func (m *Module) persistCreate(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	p CreateRunParams,
	credID, credScheme string,
	gf runGovFacts,
	lease Lease,
	runtimeCredentialSet ...runtimeCredentials,
) (model.ID, error) {
	return m.persistCreateWithWork(
		ctx, tenant, runRef, p, credID, credScheme, gf, lease, nil,
		runtimeCredentialSet...,
	)
}

func (m *Module) persistCreateWithWork(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	p CreateRunParams,
	credID, credScheme string,
	gf runGovFacts,
	lease Lease,
	work *workLaunchReservation,
	runtimeCredentialSet ...runtimeCredentials,
) (model.ID, error) {
	if len(runtimeCredentialSet) > 1 ||
		(m.rt.communicationCredentialsEnabled && len(runtimeCredentialSet) != 1) {
		return "", &runErr{
			http.StatusServiceUnavailable,
			"dual runtime credential stamp is required before launch",
		}
	}
	if m.rt.communicationCredentialsEnabled &&
		!runtimeCredentialSet[0].complete(m.now()) {
		return "", &runErr{
			http.StatusServiceUnavailable,
			"complete dual runtime credentials are required before launch",
		}
	}
	var runID model.ID
	err := m.authorizedMutate(ctx, tenant, lease, func(sc store.Scope) error {
		var credentials runtimeCredentials
		if len(runtimeCredentialSet) > 0 {
			credentials = runtimeCredentialSet[0]
			if err := validateRuntimeCredentialStampWithin(
				ctx, sc, tenant, runRef, gf.agentRef, lease, credentials,
			); err != nil {
				return err
			}
		}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		row := model.Record{
			colRunRef:         runRef,
			colTransport:      string(p.Transport),
			colPermissionMode: p.PermissionMode,
			colIsolation:      string(p.Isolation),
			colState:          statePending,
			colLastEventSeq:   int64(0),
		}
		setIf(row, colRunName, p.Name)
		setIf(row, colEffort, p.Effort)
		setIf(row, colRunModelRef, p.Model)
		setIf(row, colWorkspaceRef, p.WorkspaceRef)
		setIf(row, colTemplateID, p.TemplateID)
		if p.TemplateID != "" {
			row[colTemplateVersion] = p.TemplateVersion
		}
		if p.MaxDuration > 0 {
			row[colTemplateCeiling] = int64(p.MaxDuration / time.Second)
		}
		setIf(row, colCredentialID, credID)
		setRunGovFacts(row, gf)
		setClaimStamp(row, lease)
		if work != nil {
			row[colRunWorkItemID] = work.itemID.String()
			row[colRunWorkLeaseFence] = work.leaseFence
			row[colRunWorkDispatchKey] = append([]byte(nil), work.dispatchKey[:]...)
			row[colRunWorkOwnerEpoch] = work.ownerEpoch
			row[colRunWorkLaunchSpecHash] = append([]byte(nil), work.specHash[:]...)
		}
		if len(runtimeCredentialSet) > 0 {
			setRuntimeCredentialStamp(row, lease, credentials)
		}
		created, err := repo.Create(ctx, row)
		if err != nil {
			return err
		}
		runID = model.ID(created.String(model.ColID))
		seq, err := appendRunEvent(ctx, sc, runEventInput{
			runID: runID, runRef: runRef, event: "created",
			toState: statePending, actor: p.Actor, actorKind: p.ActorKind,
			detail: credScheme, at: m.now(),
		})
		if err != nil {
			return err
		}
		created[colLastEventSeq] = seq
		_, err = repo.Update(ctx, created)
		return err
	})
	return runID, err
}

// setClaimStamp records WHICH claim a launch is operating under, on the run row. A
// zero lease clears it to NULL rather than leaving a stale pair behind: a run
// relaunched by a caller that named no holder is not still governed by the previous
// launcher's authority.
func setClaimStamp(rec model.Record, lease Lease) {
	setOrNull(rec, colClaimHolder, lease.Holder)
	if lease.Holder == "" {
		rec[colClaimFence] = nil
		return
	}
	rec[colClaimFence] = lease.Fence
}

// claimStampOf reads the admission stamp back off a run row.
func claimStampOf(rec model.Record) (holder string, fence int64) {
	return rec.String(colClaimHolder), rec.Int(colClaimFence)
}

// authorizedMutate runs fn in ONE transaction whose commit is conditioned on the
// caller still holding the claim it launched under (F3).
//
// The shape, and why each part of it is there:
//
//   - fenceWithin and fn share the CALLER's Scope, so the check and the effect are
//     one transaction. A nested View/Mutate would also deadlock on SQLite's single
//     connection.
//   - the retry wraps the WHOLE attempt, not some inner helper's own transaction.
//     store.ErrConflict is ambiguous — it means a version mismatch OR a unique
//     violation (sqlstore/generic.go:409-412) — and re-reading is what resolves it:
//     the second attempt either succeeds (a benign self-conflict between two writes
//     of the same holder) or fails with ErrLeaseLost (authority really moved).
//   - a lapse the EARLY check observes is retired in THIS transaction, which then
//     commits (nothing governed has been written yet) and refuses afterwards. A lapse
//     the LATE check observes cannot be: the effect is already in the transaction and
//     must roll back, so that one record travels out and is committed before anybody
//     is answered (F5, and R3-01 for why neither shape may re-read the clock).
//   - a fresh clock re-check after fn catches a lease that ran out WHILE the
//     governed work was in flight; the CAS proves nobody else won the row, not that
//     time stood still.
func (m *Module) authorizedMutate(ctx context.Context, tenant model.TenantID, lease Lease, fn func(store.Scope) error) error {
	var obs lapseObservation
	var refusal error
	attempt := func(sc store.Scope) error {
		obs, refusal = lapseObservation{}, nil // a retry starts from no verdict
		if ferr := fenceWithin(ctx, sc, lease.SID, lease.Holder, lease.Fence, m.now()); ferr != nil {
			if errors.Is(ferr, errLeaseLapsed) {
				// The check wrote the retirement it owed into this transaction and fn has
				// not run, so committing here commits that and nothing else.
				refusal = ferr
				return nil
			}
			return ferr
		}
		if err := fn(sc); err != nil {
			return err
		}
		late, serr := stillLive(ctx, sc, lease.SID, m.now())
		obs = late
		return serr
	}
	// The captures are reset per INVOCATION and not only per callback, because a second
	// Mutate can fail before it ever calls the callback (leader gate, BeginTx, tenant
	// bind) and would leave the first attempt's verdict standing.
	//
	// Labeled honestly: removing this line leaves every assertion GREEN — it is a
	// SURVIVING mutant, and calling it anything else would be the overclaiming this
	// pass keeps being caught for. It survives because refusal is only consulted when
	// err is nil below, and err nil means the transaction committed, which means the
	// callback ran, which means it reset. Kept as defense in depth: the invariant holds
	// locally instead of by remote consequence of that ordering.
	run := func() error {
		obs, refusal = lapseObservation{}, nil
		return m.data.Mutate(ctx, tenant, attempt)
	}
	err := run()
	if errors.Is(err, store.ErrConflict) {
		err = run()
	}
	if obs.seen {
		// The late observation, recorded BEFORE this call answers: its transaction
		// rolled back and took the effect with it, so this is the only place left. A
		// follow-up that FAILED is not the declared crash window — it ran and did not
		// commit — so it is reported rather than warned about and swallowed.
		if rerr := m.retireObserved(context.WithoutCancel(ctx), tenant, lease.SID, obs); rerr != nil {
			m.warnf("sessions: could not record a lapsed lease", "sid", lease.SID, "err", redactErr(rerr))
			return lapseNotRecordedErr(rerr)
		}
	}
	if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrNoClaim) {
		// 403, not the 409 the run's state machine uses: this is a refusal of
		// AUTHORITY, and isRunConflict must not mistake it for a raced transition and
		// answer with the row as if nothing had happened.
		//
		// The message is this module's own. Depending on which check refused, the cause
		// can name the session, the expiry, the holders or the fences (claim.go's
		// fenceWithin/stillLive) — never all four, but always more than a refused
		// caller is owed. The detail belongs in the log (P2-R6-01).
		m.warnf("sessions: a governed write was refused by the admission plane",
			"sid", lease.SID, "err", redactErr(err))
		return forbiddenErr("the session authority is no longer valid")
	}
	if err != nil {
		return err // the transaction's verdict outranks the callback's (R4-03)
	}
	if refusal != nil {
		m.warnf("sessions: a governed write was refused by the admission plane",
			"sid", lease.SID, "err", redactErr(refusal))
		return forbiddenErr("the session authority is no longer valid")
	}
	return nil
}

// lapseNotRecordedErr reports that a governed write was refused because its lease had
// lapsed AND that the observation behind the refusal could not be persisted.
//
// It is deliberately NOT the 403 the refusal alone earns. A 403 says "authority moved
// and the store knows it", and half of that would be untrue here: answering it would
// present a retirement that never committed as durable state (R4-03).
//
// It invents no status of its own either, which was the opposite mistake and cost a
// P1. Collapsing every cause into a 500 buried the one that matters most in exactly
// this window: a leadership loss between the rollback and the follow-up is
// store.ErrNotLeader, which the contract publishes as a RETRYABLE 503 so the caller
// goes to the current leader (core/store/errors.go, core/api/errors.go). Flattening
// it also meant copying the backend's own message into the response body. So the
// cause is WRAPPED — errors.Is keeps working all the way up, and the status is left
// to the store mapper, which is where that contract lives.
func lapseNotRecordedErr(err error) error {
	return fmt.Errorf("sessions: the lease lapsed and the observation could not be recorded: %w", err)
}

// transitionInput parameterizes a state transition on an existing run row.
type transitionInput struct {
	event     string
	toState   string // "" keeps the current state (e.g. an intermediate 'stopping')
	detail    string
	actor     string
	actorKind string
	guard     func(rec model.Record) error // optional in-transaction generation precondition
	mutate    func(rec model.Record)       // optional extra field updates
	// lease is the claim the CALLER is acting under (SG-02-b). When it is set, this
	// transition is conditioned on that claim still being the session's, in the same
	// transaction as the write (F3). The zero value means "not an act of a holder" —
	// the bridge's finalize and the kill-switch sweep are acts of the RUNTIME upon a
	// session, and fencing those would leave a dead process unable to close its row.
	lease Lease
}

// allowedFrom is the state-machine guard: the set of stored states each
// lifecycle event may legally transition FROM. It is the single enforcement point
// (transition() re-checks it INSIDE the transaction), so a late/racing writer —
// e.g. createRun's 'launched' losing to the bridge's finalize — cannot resurrect a
// terminal row. 'created' is sealed directly by persistCreate (not via transition)
// and is intentionally absent. An event with no entry is unconstrained.
var allowedFrom = map[string][]string{
	"launched":       {statePending},
	"resuming":       {stateStopped, stateFailed}, // SG-02-b: the fenced write that precedes the spawn
	"resume_aborted": {statePending},
	"resumed":        {statePending},
	"stopping":       {statePending, stateRunning},
	"stopped":        {statePending, stateRunning},
	"failed":         {statePending, stateRunning},
	"cleaned":        {stateStopped, stateFailed},
}

func transitionAllowed(event, from string) bool {
	allowed, ok := allowedFrom[event]
	if !ok {
		return true
	}
	for _, s := range allowed {
		if s == from {
			return true
		}
	}
	return false
}

// errIllegalTransition aborts the transaction when the guard rejects a transition.
var errIllegalTransition = errors.New("sessions: illegal state transition")

// transition reads the run row, ENFORCES the state-machine guard, applies the
// mutation + state change, seals the lifecycle event (both ledgers) in the SAME
// transaction, and persists — with one optimistic-concurrency retry against a
// racing writer (the bridge vs a handler). An illegal transition is rejected with
// a conflict (the racing/late writer loses instead of corrupting state).
func (m *Module) transition(ctx context.Context, tenant model.TenantID, runRef string, in transitionInput) (model.Record, error) {
	var out model.Record
	var illegalFrom string
	var obs lapseObservation
	var refusal error
	attempt := func(sc store.Scope) error {
		illegalFrom = ""
		obs, refusal = lapseObservation{}, nil
		// SG-02-b: when this transition is an act of the claim's holder, the authority
		// check joins THIS transaction's write set (F3). A zero lease is a no-op.
		if ferr := fenceWithin(ctx, sc, in.lease.SID, in.lease.Holder, in.lease.Fence, m.now()); ferr != nil {
			if errors.Is(ferr, errLeaseLapsed) {
				// The retirement is in this transaction and the run row has not been
				// touched: commit the record, refuse after it (R3-01).
				refusal = ferr
				return nil
			}
			return ferr
		}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		from := rec.String(colState)
		if !transitionAllowed(in.event, from) {
			illegalFrom = from
			return errIllegalTransition // roll back; the row is left untouched
		}
		if in.guard != nil {
			if err := in.guard(rec); err != nil {
				return err
			}
		}
		if in.mutate != nil {
			in.mutate(rec)
		}
		to := from
		if in.toState != "" {
			rec[colState] = in.toState
			to = in.toState
		}
		// reason mirrors the last transition's note onto the live row (the contract's
		// API-visible field); the full note also lives in the immutable event ledger.
		if in.detail != "" {
			rec[colReason] = in.detail
		}
		runID := model.ID(rec.String(model.ColID))
		seq, err := appendRunEvent(ctx, sc, runEventInput{
			runID: runID, runRef: runRef, event: in.event,
			fromState: from, toState: to, detail: in.detail,
			actor: in.actor, actorKind: in.actorKind, at: m.now(),
		})
		if err != nil {
			return err
		}
		rec[colLastEventSeq] = seq
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		out = updated
		late, serr := stillLive(ctx, sc, in.lease.SID, m.now())
		obs = late
		return serr
	}
	// Per INVOCATION, for the same reason authorizedMutate does it, and with the same
	// honest label: defense in depth, and removing it leaves the suite green — a
	// surviving mutant, kept deliberately. illegalFrom is in here too; it had the
	// identical shape and predates this pack.
	run := func() error {
		obs, refusal, illegalFrom = lapseObservation{}, nil, ""
		return m.runtimeData(ctx).Mutate(ctx, tenant, attempt)
	}
	err := run()
	if errors.Is(err, store.ErrConflict) {
		err = run()
	}
	if obs.seen {
		// Recorded here, OUTSIDE the rolled-back transaction, or F5's durability goes
		// with the rollback — and recorded as OBSERVED, never re-judged against a clock
		// that may have moved since (retireObserved).
		if rerr := m.retireObserved(context.WithoutCancel(ctx), tenant, in.lease.SID, obs); rerr != nil {
			m.warnf("sessions: could not record a lapsed lease", "sid", in.lease.SID, "err", redactErr(rerr))
			return nil, lapseNotRecordedErr(rerr)
		}
	}
	if illegalFrom != "" && err != nil {
		return nil, conflictErr("illegal transition " + in.event + " from state " + illegalFrom)
	}
	if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrNoClaim) {
		// 403, deliberately not the 409 the state machine uses: isRunConflict maps a
		// 409 on the resume path back to "return the row as it is", which would turn a
		// refusal of AUTHORITY into a 200 nobody notices. The detail goes to the log
		// and not to the caller, for the same reason as in authorizedMutate.
		m.warnf("sessions: a transition was refused by the admission plane",
			"sid", in.lease.SID, "err", redactErr(err))
		return nil, forbiddenErr("the session authority is no longer valid")
	}
	if errors.Is(err, store.ErrConflict) {
		// ⛔ AQUI SE ESCAPABA EL CENTINELA DEL STORE EN CRUDO, Y ESO APAGABA LA
		// COMPENSACION QUE EXISTE PARA ESTE CASO EXACTO.
		//
		// Una perdida de CAS que sobrevive al reintento de arriba es EL MISMO SUCESO que
		// esta funcion ya reporta como 409 cuando la maquina de estados lo ve
		// (`illegal transition …`): otro actor escribio esta fila primero. Pero salia con
		// otro TIPO — `store.ErrConflict`, no `*runErr`—, y `isRunConflict` solo reconoce
		// el segundo. Sus dos unicos llamantes (createRun :701 y resumeRun :965) son la
		// misma compensacion «raced finalize: report the real state», asi que con el error
		// crudo delante NO disparaban: el proceso recien lanzado se desmontaba y el
		// llamante recibia un «version conflict» pelado en vez de la fila.
		//
		// Se ve en un rojo de CI del 2026-08-24 (`control-plane`, job 97305805094):
		//   winner = {RunRef: …} / version conflict
		// con el struct VACIO — que es justo lo que devuelve resumeRun cuando sale por ahi.
		// El texto pelado, sin `illegal transition` ni nada mas, es la firma de esta rama.
		//
		// Y el mensaje NO dice «illegal transition»: no lo fue. La transicion era legal y
		// perdio la carrera, que es una cosa distinta y se cuenta distinta.
		return nil, conflictErr("the run row was written by another actor during " + in.event)
	}
	if err != nil {
		return nil, err // the transaction's verdict outranks the callback's (R4-03)
	}
	if refusal != nil {
		m.warnf("sessions: a transition was refused by the admission plane",
			"sid", in.lease.SID, "err", redactErr(refusal))
		return nil, forbiddenErr("the session authority is no longer valid")
	}
	return out, nil
}

// racedFinalizeDTO answers a launch/resume whose LAST transition lost the row to another
// writer: the child is being torn down, and what the caller gets back is the run's real
// state. Both callers are the same compensation and it lives here so they cannot drift.
//
// ⛔ DOS COSAS QUE ANTES NO SE HACIAN, Y LAS DOS SE MIDIERON:
//
//  1. SE RESPETA `stopConfirmed`. Antes se llamaba a `teardownLive`, que es
//     `_, err := teardownLiveWithContext(...)` — TIRA el bool. La ruta de fallo de
//     lanzamiento SI lo comprueba y contesta 502 cuando el proceso no se pudo parar, y
//     tiene razon: con un hijo todavia vivo, «te devuelvo el estado real de la fila» no
//     es una respuesta, es media. Aqui se contesta lo mismo que alli.
//
//  2. SE ESPERA A QUE EL PUENTE CIERRE LA FILA. `teardownLiveWithContext` para el
//     proceso y revoca las credenciales, pero NO espera al `finalize` del puente, que
//     corre en su goroutine y es quien mueve el estado. Sin esperar, la fila que se
//     devolvia estaba leida A MEDIO DESMONTAJE: medido el 2026-08-24 sobre `main`
//     aec001424 con el `t.Parallel()` de ese test devuelto —
//     winner = {RunRef:01a03363-… State:pending Reason:exit PID:<nil>} / <nil>
//     `pending` con razon `exit` y sin PID es una fila que aun no se ha asentado, no el
//     estado de la corrida. La maquina de estados no estaba atascada (`allowedFrom`
//     permite pending -> stopped/failed): lo que estaba mal era el MOMENTO de leerla.
//
// La espera es la MISMA que `stopRun` ya usa (`finalizedCh`, o `ctx`, o 2*waitDelay) y
// con la misma decision detras: si el finalize tarda de mas, se devuelve el estado
// corriente HONESTAMENTE en vez de colgar al llamante. Reusar su forma es deliberado —
// dos esperas distintas sobre el mismo canal derivan.
func (m *Module) racedFinalizeDTO(
	ctx context.Context, tenant model.TenantID, runRef string,
	lr *liveRun, stopped bool, cause, teardownErr error,
) (runDTO, error) {
	if !stopped {
		// El hijo sigue en pie: no hay «estado real» que devolver todavia.
		return runDTO{}, errors.Join(
			&runErr{http.StatusBadGateway, "the session runner could not stop the process after a raced transition"},
			cause, teardownErr,
		)
	}
	select {
	case <-lr.finalizedCh:
	case <-ctx.Done():
	case <-time.After(2 * m.rt.waitDelay):
		// finalize is taking too long; return the current state honestly.
	}
	dto, err := m.getRun(ctx, tenant, runRef)
	if err != nil {
		return runDTO{}, errors.Join(err, teardownErr)
	}
	if teardownErr != nil {
		return runDTO{}, errors.Join(&racedRunErr{dto: dto}, teardownErr)
	}
	return runDTO{}, &racedRunErr{dto: dto}
}

// racedRunErr es el 409 de un lanzamiento que PERDIO SU FILA, y lleva la fila dentro.
//
// ⛔ DECISION 1 DE K2, tomada ante un HUECO DE SPEC y no contra la spec: los codigos HTTP
// contratados en docs/contracts-… para este caso son CERO. Adjudicada por the planner.
//
// POR QUE 409 Y NO 201: un `201 Created` con una fila `stopped` obliga a CADA cliente a
// mirar el cuerpo para descubrir que no se creo nada, y el que no lo mire trata como creada
// una corrida que no existe. El 409 pone esa comprobacion en el PROTOCOLO en vez de en la
// diligencia de cada cliente: quien ignora un 409 se rompe a gritos. Fail-loud es doctrina.
//
// POR QUE CON LA FILA Y NO UN 409 PELADO: la fila EXISTE —tiene su run_ref, su estado y su
// cadena de eventos— y un 409 vacio tira ese dato y obliga a un segundo viaje. Codigo que
// dice «esto no es la creacion que pediste» + cuerpo que dice «y esto es lo que hay», en un
// solo viaje e inconfundible con exito.
//
// La fila viaja DENTRO del error y no como primer valor de retorno a proposito: dos fuentes
// para el mismo dato derivan, y aqui la que manda es la del error.
type racedRunErr struct{ dto runDTO }

func (e *racedRunErr) Error() string {
	return "the launch lost its row to another writer; the body carries the run's real state"
}

// isRunConflict reports whether err is a 409 runErr (an illegal/raced transition).
func isRunConflict(err error) bool {
	var re *runErr
	return errors.As(err, &re) && re.status == http.StatusConflict
}

// ⛔ `teardownLive` VIVIA AQUI Y SE HA IDO CON SUS DOS LLAMANTES, no por limpieza.
//
// Era `_, err := m.teardownLiveWithContext(context.Background(), lr)`: su unico efecto
// propio era TIRAR el `bool` que dice si el proceso se paro de verdad. Sus dos llamantes
// eran las compensaciones de create y resume, que ahora pasan por `racedFinalizeDTO` y SI
// lo respetan. Dejarla en pie invitaria al siguiente a volver a perder ese bool, que es
// justo el defecto que se acaba de cerrar. Quien necesite la version sin contexto que la
// escriba con el bool a la vista.

func (m *Module) teardownLiveWithContext(parent context.Context, lr *liveRun) (bool, error) {
	lr.stopDeadline()
	m.stopRuntimeCredentialHeartbeat(lr)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*m.rt.waitDelay+30*time.Second)
	defer cancel()
	// The process has crossed the OS launch boundary. End it before withdrawing
	// either bearer, then attempt both revocations independently.
	stopErr := secretSafeCredentialError("session process teardown", lr.proc.Stop(ctx))
	lr.cancel()
	revokeErr := m.revokeLiveRuntimeCredentials(ctx, lr)
	if stopErr == nil {
		m.rt.dropLive(lr.tenant, lr.runRef)
	}
	return stopErr == nil, errors.Join(
		wrapCredentialCompensation("stop launched process", stopErr), revokeErr,
	)
}

// ---------------------------------------------------------------------------
// the template's session-duration ceiling, enforced by the runtime.
// ---------------------------------------------------------------------------

// armRunDeadline starts the wall-clock ceiling a template imposes on a session
// (policies.max_session_duration_minutes). 0 disarms — an ordinary launch keeps no
// timer at all, so a run without a template is untouched by this pack.
//
// The runtime is where this belongs and there is nowhere else it could go: it is the
// only layer that OWNS the child process, so it is the only one that can end a session
// rather than merely decline to start one. The launch gate can refuse a launch; it
// cannot un-launch it an hour later.
//
// It stops the process through the same graceful path an operator stop takes
// (stopRequested + proc.Stop), so the exit is recorded as STOPPED with its reason and
// not misfiled as FAILED — a session that reached its ceiling did what it was told.
func (m *Module) armRunDeadline(lr *liveRun, d time.Duration) {
	if d <= 0 {
		return
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.finalized {
		return // it already exited while we were persisting; nothing to bound
	}
	lr.deadline = m.rt.afterFunc(d, func() { m.expireRun(lr, d) })
}

// stopDeadline cancels a run's duration ceiling. Called from finalize and teardown so a
// session that ends early does not leave a timer holding a reference to its handle.
func (lr *liveRun) stopDeadline() {
	lr.mu.Lock()
	t := lr.deadline
	lr.deadline = nil
	lr.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// expireRun ends a session that reached its template's duration ceiling.
//
// It does NOT take the per-run operation lock. stopRun holds that lock while it waits
// on the bridge's finalize, so a timer that queued behind an in-flight stop would block
// until that stop completed — and the only thing it would then do is stop an already
// stopping process.
//
// ⚠ WHAT MAKES THAT SAFE IS THE IDENTITY CHECK AND THE stopRequested FLAG, NOT THE STATE
// MACHINE, which is a correction of what this comment used to claim. A 'stopping' event
// does not change the stored state, so it stays legal FROM running and a racing operator
// stop does NOT "lose harmlessly" at the guard: both events would simply be appended.
// What actually prevents a duplicate is the flag below, read and set under the run's own
// lock.
//
// ⛔ AND THE CEILING IS NOT DURABLE, which is written here rather than left to be
// discovered: it lives only in this process's timer. A non-graceful restart loses it, and
// this runtime already says elsewhere that a native child can reparent and keep running
// (reconcileTerminal). So the ceiling bounds a session while the engine that launched it
// is alive, and nothing more. Closing it needs an absolute deadline persisted on the row
// and reconciled at boot, which is a lifecycle change, not a line here — the applied
// duration is persisted (colTemplateCeiling) so that work has its input.
func (m *Module) expireRun(lr *liveRun, d time.Duration) {
	// Identity, because Timer.Stop does not wait for a callback that has ALREADY started:
	// this can be in flight while the process finalizes and the run is RESUMED, at which
	// point the registry holds a NEW handle at the same key and this callback would write
	// a max-duration `stopping` onto the successor's ledger while stopping a process that
	// is already gone. The delayed reaper next door has always had this guard (reapClosed's
	// cur == lr). Raised by the Codex sol max contrast, 2026-08-11.
	//
	// This registry identity check is the cheap in-process fast path. The durable
	// protection is the runtime_launch_id guard on the transition below: if this
	// callback pauses here while a successor is admitted, its ledger write loses
	// inside the transaction and the stale process callback cannot alter that
	// successor's row.
	if cur, ok := m.rt.getLive(lr.tenant, lr.runRef); !ok || cur != lr {
		return // a successor owns this run now; its own ceiling governs it
	}
	lr.mu.Lock()
	if lr.finalized || lr.stopRequested {
		lr.mu.Unlock()
		return
	}
	lr.stopRequested = true
	lr.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*m.rt.waitDelay+30*time.Second)
	defer cancel()
	// Record WHY before terminating: an operator finding a stopped session must be able
	// to read that a policy ended it, not guess from a timestamp. No lease travels with
	// it — this is an act of the RUNTIME upon a session, like finalize and the
	// kill-switch sweep, and fencing it would leave a session unable to be ended by the
	// ceiling its own template imposed.
	if _, err := m.transition(ctx, lr.tenant, lr.runRef, transitionInput{
		event:  "stopping",
		detail: "max session duration reached (" + d.String() + ", template policy)",
		actor:  model.ActorSystem, actorKind: model.ActorSystem,
		guard: guardRuntimeLaunch(lr.launchID),
	}); err != nil {
		m.warnf("sessions: could not record the duration-ceiling stop", "run_ref", lr.runRef, "err", redactErr(err))
	}
	if err := secretSafeCredentialError("session process duration stop", lr.proc.Stop(ctx)); err != nil {
		m.warnf("sessions: the duration ceiling could not stop the process gracefully; canceling its context",
			"run_ref", lr.runRef)
		lr.cancel() // SIGKILL the process group — the ceiling is not advisory
	}
	// Stop is only an attempt: a custom runner may return an error without closing
	// Output, so finalize might never observe exit. Withdraw both API authorities
	// synchronously after the attempt instead of relying on that callback.
	if err := m.revokeLiveRuntimeCredentials(ctx, lr); err != nil {
		m.warnf("sessions: duration-ceiling runtime credential revocation incomplete",
			"run_ref", lr.runRef)
	}
}

// templateDetail is the non-sensitive, ledger-bound note a templated launch leaves: the
// template that governed it and how many of the caller's values it overrode. References
// and counts only — never the merged values, which can carry a model name, a prompt
// fragment or a tool spec (minimal-data, docs/SECURITY-HARDENING.md).
func templateDetail(tpl templateDTO, conflicts []mergeConflict) string {
	if tpl.ID == "" {
		return ""
	}
	d := "template " + tpl.ID + " applied"
	if n := len(conflicts); n > 0 {
		d += " (" + strconv.Itoa(n) + " overridden)"
	}
	return d
}

// joinDetail concatenates the non-empty ledger detail fragments of one transition.
func joinDetail(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "; ")
}

// warnf logs a non-secret warning if a logger is set.
func (m *Module) warnf(msg string, args ...any) {
	if m.log != nil {
		m.log.Warn(msg, args...)
	}
}

// closedRetention bounds how long a finalized run's handle (and its closed ring)
// is kept in the registry so a late attach can replay the buffered tail; after it
// the handle is reclaimed, bounding memory to O(live + recently-closed) instead of
// O(all ever-stopped).
const closedRetention = 5 * time.Minute

// reapClosed drops a finalized run's handle after closedRetention, UNLESS it was
// resumed in the meantime (a resume installs a NEW handle at the same key — the
// cur==lr guard ensures this timer never reaps a live successor).
func (m *Module) reapClosed(lr *liveRun) {
	timer := time.NewTimer(closedRetention)
	defer timer.Stop()
	<-timer.C
	key := liveKey(lr.tenant, lr.runRef)
	m.rt.mu.Lock()
	if cur, ok := m.rt.live[key]; ok && cur == lr {
		delete(m.rt.live, key)
	}
	m.rt.mu.Unlock()
}

// reconcileTerminal handles a stop request for a row with no live handle: if the
// row is still in a non-terminal state it is an orphan (the process is gone, e.g.
// after a runtime restart) and is honestly moved to stopped; otherwise it is
// already terminal and returned as-is.
func (m *Module) reconcileTerminal(ctx context.Context, tenant model.TenantID, runRef string, rec model.Record, actor, actorKind string) (runDTO, error) {
	switch rec.String(colState) {
	case statePending, stateRunning:
		generation := runtimeRecoveryGenerationOf(rec)
		launchID := model.ID(rec.String(colRuntimeLaunchID))
		if !launchID.IsZero() {
			lease := Lease{
				SID: rec.String(colRunClaimSID), Holder: rec.String(colClaimHolder),
				Fence: rec.Int(colClaimFence),
			}
			claimEmpty := lease.SID == "" && lease.Holder == "" && lease.Fence == 0
			claimComplete := lease.SID != "" && lease.Holder != "" && lease.Fence > 0
			hasHandles := rec.String(colWorkCredentialID) != "" ||
				rec.String(colCommunicationCredentialID) != ""
			if !claimEmpty && !claimComplete && hasHandles {
				return runDTO{}, forbiddenErr(
					"orphaned runtime Claim binding is incomplete; recovery denied",
				)
			}
			if claimComplete {
				if err := m.Release(
					context.WithoutCancel(ctx), tenant, lease.SID, lease.Holder, lease.Fence,
				); err != nil && !errors.Is(err, ErrLeaseLost) && !errors.Is(err, ErrNoClaim) {
					return runDTO{}, err
				}
			}
		}
		// Release serializes with any already-admitted runtime writer through the
		// Claim row. Reload afterwards: the page/Stop snapshot may have contained
		// zero handles while that writer was waiting to persist both of them. Using
		// the stale record here would terminalize a row whose credentials recovery
		// never attempted to revoke.
		fresh, err := m.loadRun(ctx, tenant, runRef)
		if err != nil {
			return runDTO{}, err
		}
		if !sameRuntimeRecoveryGeneration(generation, fresh) {
			return runDTO{}, conflictErr("runtime recovery generation changed")
		}
		rec = fresh
		generation = runtimeRecoveryGenerationOf(fresh)
		// Release makes an unpersisted post-Mint bearer immediately fail its Claim
		// recheck; now independently revoke every durable handle. A failed handle
		// remains in the row and denies the terminal transition.
		if err := m.revokeStoredRuntimeCredentials(ctx, tenant, rec); err != nil {
			return runDTO{}, err
		}
		// HONEST: we cannot confirm the underlying process actually terminated (the
		// handle is gone, e.g. after a non-graceful runtime restart — the process may
		// have reparented to init and kept running). We move to the terminal `stopped`
		// state (so the operator can resume/clean up) but the reason states the
		// uncertainty rather than asserting a clean termination that we did not perform.
		updated, err := m.transition(ctx, tenant, runRef, transitionInput{
			event: "stopped", toState: stateStopped,
			detail: "orphaned: runtime handle lost; process not confirmed terminated",
			actor:  actor, actorKind: actorKind,
			guard: guardRuntimeRecoveryTerminal(
				generation, rec.String(colCommunicationWorkspaceID),
			),
			mutate: func(rec model.Record) {
				rec[colStoppedAt] = model.NewTimestamp(m.now()).String()
				rec[colPID] = nil
				rec[colRuntimeLaunchID] = nil
			},
		})
		if err != nil {
			return runDTO{}, err
		}
		return m.toRunDTO(updated), nil
	default:
		return m.toRunDTO(rec), nil
	}
}

// getRun returns one session, deriving idle from recency at read time.
func (m *Module) getRun(ctx context.Context, tenant model.TenantID, runRef string) (runDTO, error) {
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return runDTO{}, err
	}
	return m.toRunDTO(rec), nil
}

// loadRun reads the run row by ref (typed not-found).
func (m *Module) loadRun(ctx context.Context, tenant model.TenantID, runRef string) (model.Record, error) {
	var rec model.Record
	err := m.runtimeData(ctx).View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		r, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		rec = r
		return nil
	})
	return rec, err
}

// findRunRec lists the run row by its unique ref within a scope (typed not-found).
func findRunRec(ctx context.Context, repo store.GenericRepo, runRef string) (model.Record, error) {
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colRunRef, runRef)}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, notFoundErr()
	}
	return recs[0], nil
}

// stopAllRuns terminates every supervised process on module shutdown. The rows
// are left in their durable state; a later operation lazily reconciles an
// orphaned 'running' row to stopped. Resume recovers the conversation.
func (m *Module) stopAllRuns(ctx context.Context) error {
	m.rt.mu.Lock()
	runs := make([]*liveRun, 0, len(m.rt.live))
	for _, lr := range m.rt.live {
		runs = append(runs, lr)
	}
	m.rt.mu.Unlock()
	var shutdownErrs []error
	for _, lr := range runs {
		lr.stopDeadline()
		m.stopRuntimeCredentialHeartbeat(lr)
		// Once a bearer has crossed LaunchSpec, process termination precedes both
		// independent revocations on every compensation path.
		if err := secretSafeCredentialError("session process shutdown stop", lr.proc.Stop(ctx)); err != nil {
			shutdownErrs = append(shutdownErrs,
				fmt.Errorf("stop session process for run %s: %w", lr.runRef, err))
		}
		lr.cancel()
		if err := m.revokeLiveRuntimeCredentials(ctx, lr); err != nil {
			shutdownErrs = append(shutdownErrs,
				fmt.Errorf("revoke runtime credentials for run %s: %w", lr.runRef, err))
		}
	}
	for _, lr := range runs {
		select {
		case <-lr.finalizedCh:
		case <-ctx.Done():
			shutdownErrs = append(shutdownErrs, ctx.Err())
		}
		// The first synchronous revoke may have failed, and finalize is also
		// best-effort. Retry while auth storage is still open; a failure here is
		// an UNKNOWN shutdown outcome, never a successful Stop.
		if err := m.revokeLiveRuntimeCredentials(ctx, lr); err != nil {
			shutdownErrs = append(shutdownErrs,
				fmt.Errorf("retry runtime credential revocation for run %s: %w", lr.runRef, err))
		}
	}
	return errors.Join(shutdownErrs...)
}

// now is the runtime clock (injectable for tests via the module clock).
func (m *Module) now() time.Time { return m.clock.Now().Time() }

// validateCreate normalizes and validates launch parameters.
func validateCreate(p *CreateRunParams) error {
	if p.Transport == "" {
		p.Transport = TransportStreamJSON
	}
	if !ValidTransport(p.Transport) {
		return badRequest("invalid transport (want stream-json|remote-control)")
	}
	if p.Isolation == "" {
		p.Isolation = IsolationNative
	}
	if !ValidIsolation(p.Isolation) {
		return badRequest("invalid isolation (want native|container|sandbox)")
	}
	if p.PermissionMode == "" {
		p.PermissionMode = "default"
	}
	if !validPermissionModes[p.PermissionMode] {
		return badRequest("invalid permission_mode")
	}
	if p.Effort != "" && !validEffortLevels[p.Effort] {
		return badRequest("invalid effort (want low|medium|high|xhigh|max)")
	}
	if len(p.EnvAllow) > 64 {
		return badRequest("env_allow has too many names")
	}
	seenEnv := make(map[string]bool, len(p.EnvAllow))
	for i, name := range p.EnvAllow {
		name = strings.TrimSpace(name)
		if !validEnvName(name) || forbiddenInheritedEnvName(name) || seenEnv[name] {
			return badRequest("invalid or reserved env_allow name")
		}
		seenEnv[name] = true
		p.EnvAllow[i] = name
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Model = strings.TrimSpace(p.Model)
	p.WorkspaceRef = strings.TrimSpace(p.WorkspaceRef)
	return nil
}

// redactErr returns a short, non-sensitive error string for an API/ledger
// message (a launch/credential error can carry a path or arg; keep it terse).
func redactErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return msg
}
