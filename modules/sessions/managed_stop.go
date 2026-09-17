// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"crypto/sha256"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop.go (P2 / W2) — the module owner of a managed Stop.
//
// A managed Stop differs from the legacy operator Stop in one way that decides
// everything else: it is a GOVERNED, SINGLE-USE operation with a durable journal
// row, so its identity must be burned before the process is touched and settled
// after. The engine owns that journal (W1's custodial effect); this file owns the
// authority, the ordering and the refusals.
//
// ⛔ THE THREE QUESTIONS ARE ASKED ONCE, BEFORE THE TOKEN, AND NEVER AGAIN.
// Authorization is policy I/O and must not happen under a lock or a transaction,
// so it happens first and its RESULT is retained. What is checked afterwards is
// the retained authority's freshness, not a new opinion — a re-ask under the
// token would be a second decision nobody could reconcile with the first.
//
// ⛔ AND THE ORIGINAL ACTOR SURVIVES ALL OF IT. The operator is the actor on the
// claim audit, on the journal row and on the settlement. The work lease's holder
// SID and run ref name the TARGETED runtime and are never compared with the
// caller; a work-session token fails question 2 or 3 before any transaction.

// ManagedStopWork is the complete work binding a caller observed through the two
// existing read routes. All six facts are required together: a partial binding
// cannot be compared against the stamp and would make a stale observation look
// like an absent one.
type ManagedStopWork struct {
	WorkItemID     model.ID
	LeaseFence     int64
	OwnerEpoch     int64
	LeaseExpiresAt string
	HolderSID      string
	HolderRunRef   string
}

// ManagedStopLink names the private session link this Stop was requested through.
// It is digest input and attribution; it authorizes nothing.
type ManagedStopLink struct {
	CoreSessionID  string
	SessionSID     string
	LinkGeneration int64
}

// ManagedStopQuestion is one complete authorization question. The caller supplies
// the entry route's — question 1, already authorized by the route that admitted the
// request (today the private session cockpit; the mechanism itself is Community
// foundation) and re-asked here against the same principal — and the module builds
// the other two from server-owned facts.
type ManagedStopQuestion struct {
	Permission auth.Permission
	Resource   auth.ResourceAttrs
	Route      auth.RouteMetadata
}

// ManagedStopRequest is one managed Stop.
type ManagedStopRequest struct {
	// Principal is the RECONSTRUCTED caller. No body field, header or server
	// token supplies identity.
	Principal auth.Principal
	// RunRef names the run. ExpectedLaunch is the exact runtime generation the
	// caller observed; a superseded launch refuses rather than stopping its
	// successor.
	RunRef         string
	ExpectedLaunch model.ID
	// Work is the complete work binding, or nil to assert the run is unbound.
	Work *ManagedStopWork
	// OperationID is the client half of the single-use journal identity.
	OperationID string
	Reason      string
	Link        ManagedStopLink
}

// ManagedStopOutcome is the CLOSED refusal and result vocabulary. It is closed on
// purpose: a caller maps these and nothing else, and an unlisted string would be
// an outcome nobody decided what to do with.
type ManagedStopOutcome string

const (
	// Terminal successes. stopped is reported ONLY with an observed exit of the
	// expected launch; every uncertain stop is unknown.
	ManagedStopStopped ManagedStopOutcome = "stopped"
	// ManagedStopInProgress is this runtime's own originating call, still running.
	ManagedStopInProgress ManagedStopOutcome = "in_progress"
	// ManagedStopUnknown is a claimed row nobody in this process owns. It is never
	// re-dispatched: the single-use identity is burned.
	ManagedStopUnknown ManagedStopOutcome = "unknown"
	// ManagedStopObservedExitUnsettled is a replay of a claimed, never-settled
	// operation whose exact-launch exit WAS observed afterwards (correction 1 §7).
	// It is not stopped: nothing settled it, and a replay never adopts.
	ManagedStopObservedExitUnsettled ManagedStopOutcome = "observed_exit_unsettled"

	// Refusals with no effect and no journal row.
	ManagedStopConcealed            ManagedStopOutcome = "concealed"
	ManagedStopForbidden            ManagedStopOutcome = "forbidden"
	ManagedStopScopedGrantRequired  ManagedStopOutcome = "scoped_grant_required"
	ManagedStopStepUpRequired       ManagedStopOutcome = "step_up_required"
	ManagedStopAuthorityUnavailable ManagedStopOutcome = "authority_unavailable"
	ManagedStopLaunchSuperseded     ManagedStopOutcome = "launch_superseded"
	ManagedStopClaimLost            ManagedStopOutcome = "claim_lost"
	ManagedStopProfileRetired       ManagedStopOutcome = "profile_retired"
	ManagedStopWorkLeaseRequired    ManagedStopOutcome = "work_lease_required"
	ManagedStopWorkLeaseUnexpected  ManagedStopOutcome = "work_lease_unexpected"
	ManagedStopWorkLeaseStale       ManagedStopOutcome = "work_lease_stale"
	ManagedStopNotLocallySupervised ManagedStopOutcome = "not_locally_supervised"
	ManagedStopCanceled             ManagedStopOutcome = "canceled_before_dispatch"
	ManagedStopUnwired              ManagedStopOutcome = "unwired"

	// Journal verdicts.
	ManagedStopSpoolDegraded     ManagedStopOutcome = "spool_degraded"
	ManagedStopOperationConflict ManagedStopOutcome = "operation_conflict"
	// ManagedStopClaimRetired is the lapse path: the commit carries the retirement
	// and the authority touches and nothing else. It is reported as what it is.
	ManagedStopClaimRetired ManagedStopOutcome = "claim_retired"
)

// ManagedStopResult is what one call decided. Attempted reports whether the
// external effect was crossed; a caller that sees false knows nothing reached the
// child.
type ManagedStopResult struct {
	Outcome ManagedStopOutcome
	// OperationRef is the journal identity, present once a binding was made.
	OperationRef string
	// Settlement is the terminal settlement recorded for this operation, empty
	// when none was attempted.
	Settlement model.EvidenceOperationState
	// ProcessOutcome is exit_observed, not_reaped or unverified (correction 1
	// §12.3). Only exit_observed permits a completed settlement.
	ProcessOutcome string
	// Observation is the committed P1 string for the EXPECTED launch, empty when
	// none is committed or none could be read. It is the evidence, not the verdict.
	Observation string
	// CredentialRevocation is the explicit managed revocation phase's own result:
	// revoked, failed or not_applicable. It never changes the process fact.
	CredentialRevocation string
	// Attempted is true only once the process boundary was crossed.
	Attempted bool
	// Detail is a non-sensitive diagnostic. It never carries a credential, a
	// policy body or a reason a caller supplied.
	Detail string
}

// ErrManagedStopUnwired is the deny-closed state of a composition that has not
// supplied the managed Stop's ports. It is returned BEFORE any authority work.
var ErrManagedStopUnwired = errors.New("sessions: managed stop is not wired")

// managedStopSurfacePrefix is the reserved journal identity prefix. The engine
// refuses any generic producer that names it, so the only writer is the handle.
const managedStopSurfacePrefix = store.ManagedStopOperationPrefix

// ManagedStopPrincipalResolver reconstructs the caller's authority evidence. The
// module calls it itself, under the admission window, so the evidence the three
// questions rest on never outlives the admission that uses it (correction 1 §5.2
// step 3). *auth.Authenticator implements it.
type ManagedStopPrincipalResolver interface {
	ResolvePrincipalScope(ctx context.Context, ref auth.PrincipalRef, tenant model.TenantID) (auth.Principal, error)
}

// managedStopPorts are what a managed Stop needs and the module cannot own: the
// principal resolver, the mutation authorizer, the durable leader fence, and the
// configured admission timeout T.
type managedStopPorts struct {
	resolver         ManagedStopPrincipalResolver
	authorizer       *auth.Authorizer
	elector          store.LeaderElector
	admissionTimeout time.Duration
}

// UseManagedStopAuthority late-binds the managed Stop's composition ports.
//
// Nil is a MEANINGFUL deny-closed state and not a default: without the resolver no
// evidence can be reconstructed, without the authorizer no question can be asked,
// and without the elector no epoch can be fenced. A module composed without them
// refuses every managed Stop with ErrManagedStopUnwired before it reads anything.
func (m *Module) UseManagedStopAuthority(
	resolver ManagedStopPrincipalResolver,
	authorizer *auth.Authorizer,
	elector store.LeaderElector,
) {
	m.managedStop = &managedStopPorts{resolver: resolver, authorizer: authorizer, elector: elector}
}

// WithManagedStopAdmissionTimeout sets the managed Stop admission timeout T
// (correction 1 §5.3). A non-positive value leaves the default in place: the
// composition root refuses an invalid configured value before it reaches here.
func WithManagedStopAdmissionTimeout(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.managedStopAdmissionTimeout = d
		}
	}
}

// defaultManagedStopAdmissionTimeout is T when none is configured. It is an
// operating default, not a latency guarantee, and it does not bound teardown: each
// post-dispatch stage receives its own bound (managed_stop_observation.go).
const defaultManagedStopAdmissionTimeout = 10 * time.Second

// managedStopReady reports the ports with T resolved, or the deny-closed reason.
func (m *Module) managedStopReady() (*managedStopPorts, error) {
	if m == nil || m.data == nil || m.rt == nil {
		return nil, ErrManagedStopUnwired
	}
	p := m.managedStop
	if p == nil || p.resolver == nil || p.authorizer == nil || p.elector == nil {
		return nil, ErrManagedStopUnwired
	}
	ports := *p
	ports.admissionTimeout = m.managedStopAdmissionTimeout
	if ports.admissionTimeout <= 0 {
		ports.admissionTimeout = defaultManagedStopAdmissionTimeout
	}
	return &ports, nil
}

// managedStopAdmissionContext builds actx (correction 1 §5.2 step 1): the caller's
// own context, cancellation included, with its deadline at the EARLIER of the
// caller's and now + T. A caller's longer deadline is not the admission timeout.
// Principal resolution, the three questions, replay, the run token and the
// admission transaction all run under it.
//
// ⛔ NOTHING PAST THE DISPATCH FENCE IS BOUNDED BY actx. Teardown, observation and
// settlement each derive their own cancellation-independent context, so a caller
// that walks away, or an admission window that closes, cannot strand a claimed
// operation half-observed.
func (m *Module) managedStopAdmissionContext(
	ctx context.Context,
	ports *managedStopPorts,
) (context.Context, context.CancelFunc, ManagedStopResult) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, refuseManagedStop(ManagedStopCanceled,
			"the caller's lifetime is already over")
	}
	actx, cancel := context.WithTimeout(ctx, ports.admissionTimeout)
	return actx, cancel, ManagedStopResult{}
}

// refuse builds a no-effect refusal.
func refuseManagedStop(outcome ManagedStopOutcome, detail string) ManagedStopResult {
	return ManagedStopResult{Outcome: outcome, Detail: detail}
}

// StopManagedRun performs one governed, single-use Stop of a supervised run.
//
// The phases are fixed and their ORDER is the contract:
//
//	A  validate and check wiring — no I/O
//	B  the three authorization questions, in a read-only View, before any lock
//	R  replay: a journal row that already exists answers, and never signals
//	C  take the run token; a canceled caller stops here with nothing written
//	C1 replay again under the token, catching a same-identity Stop that just finished
//	C2 in memory: this runtime's live handle, at exactly the expected launch
//	D  ONE mutation: authority lock, target checks, work binding, custody claim
//	E  classify what D decided
//	F  pre-effect checks, in order, with no I/O in the last one
//	F5 the effect, through the same code the legacy stop uses
//	G  observe and settle — the originating call only
//
// It returns an error only for a fault the caller cannot classify; every decision
// is a ManagedStopResult.
func (m *Module) StopManagedRun(
	ctx context.Context,
	tenant model.TenantID,
	q ManagedStopQuestion,
	req ManagedStopRequest,
) (ManagedStopResult, error) {
	// ---- Phase A: validate and wiring, no I/O -------------------------
	ports, err := m.managedStopReady()
	if err != nil {
		return refuseManagedStop(ManagedStopUnwired, "composition ports are absent"), err
	}
	if err := validateManagedStopRequest(tenant, q, req); err != nil {
		return refuseManagedStop(ManagedStopConcealed, err.Error()), err
	}
	operationRef := managedStopSurfacePrefix + req.OperationID

	actx, cancel, res := m.managedStopAdmissionContext(ctx, ports)
	if res.Outcome != "" {
		return res, nil
	}
	defer cancel()

	// ---- Phase B: resolve, then the three questions, before any lock ---
	authority, res, err := m.authorizeManagedStop(actx, ctx, tenant, q, req, ports)
	if err != nil || res.Outcome != "" {
		return res, err
	}
	// Everything downstream attributes and digests the RECONSTRUCTED principal.
	req.Principal = authority.principal

	semantic := managedStopSemanticDigest(tenant, q, req)

	// ---- Phase R: replay before the token -----------------------------
	if replay, found, err := m.lookupManagedStop(actx, tenant, authority.workspace, req, operationRef, semantic); found {
		return replay, err
	} else if err != nil {
		return refuseManagedStop(ManagedStopAuthorityUnavailable, "the journal could not be read"), err
	}

	// ---- Phase C: the run token, inside the admission window ----------
	release, err := m.rt.lockRunContext(actx, liveKey(tenant, req.RunRef))
	if err != nil {
		// The caller went away, or the admission window closed while another
		// operation held the token. Nothing was written and nothing was dispatched.
		return refuseManagedStop(ManagedStopCanceled,
			"the admission window ended before the run token was acquired"), nil
	}
	defer release()

	// ---- Phase C1: replay again, under the token ----------------------
	if replay, found, err := m.lookupManagedStop(actx, tenant, authority.workspace, req, operationRef, semantic); found {
		return replay, err
	} else if err != nil {
		return refuseManagedStop(ManagedStopAuthorityUnavailable, "the journal could not be read"), err
	}

	// ---- Phase C2: this runtime's live handle -------------------------
	lr, ok := m.rt.getLive(tenant, req.RunRef)
	if !ok {
		return refuseManagedStop(ManagedStopNotLocallySupervised,
			"this runtime does not supervise the run"), nil
	}
	if lr.launchID != req.ExpectedLaunch {
		return refuseManagedStop(ManagedStopLaunchSuperseded,
			"the run carries a different runtime launch generation"), nil
	}

	// ---- Phase D: one mutation ----------------------------------------
	admission, err := m.admitManagedStop(actx, tenant, req, authority, lr, operationRef, semantic)
	if err != nil {
		return admission.result, err
	}
	// ---- Phase E: classify --------------------------------------------
	if admission.result.Outcome != ManagedStopStopped {
		return admission.result, nil
	}

	// ---- Phase F: pre-effect checks, in order -------------------------
	if res, settle := m.managedStopPreEffect(actx, tenant, req, authority, lr, ports, admission.epoch); res.Outcome != "" {
		res.OperationRef = operationRef
		if settle {
			res.Settlement = m.settleManagedStop(actx, ports, tenant, authority.workspace, req, operationRef, semantic,
				managedStopSettlementFor(res.Outcome), "", "")
		}
		return res, nil
	}

	// ---- Phase F5 and G: the effect, then what was actually observed ---
	return m.stopAndObserveManagedRun(actx, ports, tenant, req, authority, admission, lr, operationRef, semantic), nil
}

// ReconcileManagedStop is phases A, B and R ONLY. It settles nothing, takes no
// token, claims nothing and signals nothing: adopting another node's claimed
// operation would be exactly the unfenced effect the journal exists to prevent.
func (m *Module) ReconcileManagedStop(
	ctx context.Context,
	tenant model.TenantID,
	q ManagedStopQuestion,
	req ManagedStopRequest,
) (ManagedStopResult, error) {
	ports, err := m.managedStopReady()
	if err != nil {
		return refuseManagedStop(ManagedStopUnwired, "composition ports are absent"), err
	}
	if err := validateManagedStopRequest(tenant, q, req); err != nil {
		return refuseManagedStop(ManagedStopConcealed, err.Error()), err
	}
	operationRef := managedStopSurfacePrefix + req.OperationID
	actx, cancel, res := m.managedStopAdmissionContext(ctx, ports)
	if res.Outcome != "" {
		return res, nil
	}
	defer cancel()
	authority, res, err := m.authorizeManagedStop(actx, ctx, tenant, q, req, ports)
	if err != nil || res.Outcome != "" {
		return res, err
	}
	req.Principal = authority.principal
	semantic := managedStopSemanticDigest(tenant, q, req)
	replay, found, err := m.lookupManagedStop(actx, tenant, authority.workspace, req, operationRef, semantic)
	switch {
	case found:
		return replay, err
	case err != nil:
		return refuseManagedStop(ManagedStopAuthorityUnavailable, "the journal could not be read"), err
	default:
		return refuseManagedStop(ManagedStopUnknown, "no journal row names this operation"), nil
	}
}

// validateManagedStopRequest refuses a malformed request before any I/O.
func validateManagedStopRequest(tenant model.TenantID, q ManagedStopQuestion, req ManagedStopRequest) error {
	switch {
	case tenant.IsZero():
		return store.ErrNoTenant
	case req.RunRef == "" || !boundedText(req.RunRef, 1, 128):
		return errors.New("sessions: managed stop requires a run reference")
	case req.ExpectedLaunch.IsZero():
		return errors.New("sessions: managed stop requires the expected runtime launch")
	case req.OperationID == "" || !boundedText(req.OperationID, 1, 128):
		return errors.New("sessions: managed stop requires a client operation id")
	case req.Reason != "" && !boundedText(req.Reason, 1, 512):
		return errors.New("sessions: managed stop reason is not acceptable")
	case q.Permission == "":
		return errors.New("sessions: managed stop requires the entry authorization question")
	}
	if w := req.Work; w != nil {
		if w.WorkItemID.IsZero() || w.LeaseFence < 1 || w.OwnerEpoch < 1 ||
			w.LeaseExpiresAt == "" || !boundedText(w.HolderSID, 1, 128) ||
			!boundedText(w.HolderRunRef, 1, 128) {
			return errors.New("sessions: managed stop work binding is incomplete")
		}
	}
	return nil
}

// managedStopSettlementFor maps a pre-effect refusal to its settlement state.
// Authority failures are BLOCKED: nothing reached the upstream. Cancellation and
// a natural exit are NOT_SENT for the same reason, stated the other way round.
func managedStopSettlementFor(outcome ManagedStopOutcome) model.EvidenceOperationState {
	switch outcome {
	case ManagedStopCanceled, ManagedStopNotLocallySupervised:
		return model.EvidenceOpNotSent
	default:
		return model.EvidenceOpBlocked
	}
}

// managedStopResultDigest is the opaque digest of the outcome. It carries no
// payload: the words above and the launch are references.
func managedStopResultDigest(res ManagedStopResult) string {
	h := sha256.New()
	h.Write([]byte("olivares.sessions.managed-stop.result.v1"))
	h.Write([]byte{0x00})
	for i, field := range []string{string(res.Outcome), res.ProcessOutcome, res.CredentialRevocation, res.Observation} {
		var framing [9]byte
		framing[0] = byte(i + 1)
		binary.BigEndian.PutUint64(framing[1:], uint64(len(field)))
		h.Write(framing[:])
		h.Write([]byte(field))
	}
	return "msr1:" + hex.EncodeToString(h.Sum(nil))
}

// managedStopSemanticDigest is the caller-side digest of the request semantics.
//
// It is framed exactly like the engine's: an ASCII domain, one NUL, then tagged,
// length-prefixed fields, no field omitted. It deliberately EXCLUDES the
// credential generation, the authority window, the transaction time and every
// current row value, so a retry after a token refresh or after the process has
// exited recomputes the same digest and replays. A substituted actor, a changed
// target, a changed launch or a changed work binding under the same operation id
// rebinds instead.
func managedStopSemanticDigest(tenant model.TenantID, q ManagedStopQuestion, req ManagedStopRequest) string {
	actor, actorKind := req.Principal.Actor(), req.Principal.ActorKind()
	work := req.Work
	present, fence, item, epoch, expires, holderSID, holderRun := "0", int64(0), "", int64(0), "", "", ""
	if work != nil {
		present, fence = "1", work.LeaseFence
		item, epoch = work.WorkItemID.String(), work.OwnerEpoch
		expires, holderSID, holderRun = work.LeaseExpiresAt, work.HolderSID, work.HolderRunRef
	}
	h := sha256.New()
	h.Write([]byte("olivares.sessions.managed-stop.effect.v1"))
	h.Write([]byte{0x00})
	texts := []string{
		tenant.String(),
		store.ManagedStopSurface,
		store.ManagedStopAction,
		string(q.Permission),
		q.Route.CedarAction,
		actorKind,
		actor,
		req.RunRef,
		req.ExpectedLaunch.String(),
		present,
		req.Reason,
		req.Link.CoreSessionID,
		req.Link.SessionSID,
		req.OperationID,
		item,
		expires,
		holderSID,
		holderRun,
	}
	for i, field := range texts {
		var framing [9]byte
		framing[0] = byte(i + 1)
		binary.BigEndian.PutUint64(framing[1:], uint64(len(field)))
		h.Write(framing[:])
		h.Write([]byte(field))
	}
	for i, n := range []int64{fence, epoch, req.Link.LinkGeneration} {
		var framing [9]byte
		framing[0] = byte(len(texts) + i + 1)
		binary.BigEndian.PutUint64(framing[1:], uint64(n))
		h.Write(framing[:])
	}
	return "msv1:" + hex.EncodeToString(h.Sum(nil))
}

// managedStopBinding builds the engine binding for one mode.
func managedStopBinding(
	mode store.CustodialMode,
	req ManagedStopRequest,
	operationRef, semantic string,
	claimVersion int64,
) store.CustodialEffectBinding {
	return store.CustodialEffectBinding{
		Mode:           mode,
		OperationID:    operationRef,
		SemanticDigest: semantic,
		RunRef:         req.RunRef,
		RunLaunchID:    req.ExpectedLaunch,
		ClaimVersion:   claimVersion,
		// ⛔ THE ORIGINAL OPERATOR IS THE ACTOR ON EVERY LEDGER EVENT THIS WRITES.
		// Not the runtime, not the work holder, not a service identity: a managed
		// Stop that attributed itself to the engine would erase the only party who
		// decided it.
		Actor:     req.Principal.Actor(),
		ActorKind: req.Principal.ActorKind(),
	}
}

// custodialClaimer resolves the engine capability on a confined scope, or the
// deny-closed reason.
func custodialClaimer(sc store.Scope) (store.CustodialEffectClaimer, error) {
	claimer, ok := sc.(store.CustodialEffectClaimer)
	if !ok {
		return nil, fmt.Errorf("%w: the store exposes no custodial effect", ErrManagedStopUnwired)
	}
	return claimer, nil
}
