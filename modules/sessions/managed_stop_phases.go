// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_phases.go (P2 / W2) — the bodies of the phases managed_stop.go
// orders. The ORDER is in managed_stop.go; what each phase may touch is here.

// managedStopAuthority is what Phase B retained: the workspace the run's own
// lineage proved, the classified work binding, and the two or three
// authorizations whose FRESHNESS later phases re-verify without re-asking.
//
// The questions travel with their authorizations on purpose: AuthorityFor
// verifies "this authority, for THIS question, at THIS instant", so a retained
// authorization without its exact question cannot be re-verified at all.
type managedStopAuthority struct {
	// principal is the one reconstructed under the admission window; all three
	// questions and every later attribution use it.
	principal          auth.Principal
	workspace          model.ID
	claimSID           string
	bound              bool
	itemWorkspace      model.ID
	entryAuthorization auth.RouteMutationAuthorization
	run                auth.RouteMutationAuthorization
	lease              auth.RouteMutationAuthorization
	entryRequest       auth.Request
	runReq             auth.Request
	leaseReq           auth.Request
}

// authorizeManagedStop is Phase B: read the target's own facts, classify the
// work stamp, then ask each question exactly once — all before any lock and
// outside any mutation, because authorization is policy I/O.
//
// callerCtx is the caller's own lifetime and is consulted ONLY to refuse early.
// Principal resolution, every read and every question run under actx: the
// admission window bounded by the caller's deadline and T (correction 1 §5.2).
func (m *Module) authorizeManagedStop(
	actx, callerCtx context.Context,
	tenant model.TenantID,
	q ManagedStopQuestion,
	req ManagedStopRequest,
	ports *managedStopPorts,
) (managedStopAuthority, ManagedStopResult, error) {
	var out managedStopAuthority
	if err := callerCtx.Err(); err != nil {
		return out, refuseManagedStop(ManagedStopCanceled,
			"the caller's lifetime ended before authorization"), nil
	}
	// B1-B3: reconstruct the caller's authority evidence under actx, from its
	// credential reference and never from a principal the caller built.
	ref, ok := req.Principal.Ref()
	if !ok {
		return out, refuseManagedStop(ManagedStopAuthorityUnavailable,
			"the caller carries no credential reference"), nil
	}
	principal, rerr := ports.resolver.ResolvePrincipalScope(actx, ref, tenant)
	if rerr != nil {
		return out, refuseManagedStop(managedStopAuthOutcome(rerr),
			"the caller's authority evidence could not be reconstructed"), nil
	}
	out.principal = principal
	// B4: the target's own facts, read through the workspace the entry route (today
	// the private session cockpit) already authorized. A run whose lineage is NULL,
	// foreign or absent is concealed here, and the three cases are deliberately ONE
	// answer.
	entryWorkspace := q.Resource.WorkspaceID
	if entryWorkspace.IsZero() {
		return out, refuseManagedStop(ManagedStopConcealed,
			"the entry authorization names no workspace"), nil
	}
	var facts managedStopTargetFacts
	err := m.data.View(actx, tenant, func(raw store.Scope) error {
		sc, cerr := store.ConfineWorkspace(actx, raw, entryWorkspace)
		if cerr != nil {
			return cerr
		}
		var ferr error
		facts, ferr = readManagedStopTarget(actx, sc, entryWorkspace, req)
		return ferr
	})
	if outcome, detail, refused := classifyManagedStopTargetError(err); refused {
		return out, refuseManagedStop(outcome, detail), nil
	}
	out.workspace, out.claimSID = facts.workspace, facts.claimSID
	out.bound, out.itemWorkspace = facts.bound, facts.itemWorkspace

	// B5-B6: each question exactly once, question 3 only for a bound run.
	ask := func(q ManagedStopQuestion) (auth.RouteMutationAuthorization, auth.Request, ManagedStopOutcome) {
		request := auth.Request{
			Principal: out.principal, Permission: q.Permission,
			Tenant: tenant, Resource: q.Resource, Route: q.Route,
		}
		az, aerr := ports.authorizer.AuthorizeRouteMutation(actx, request)
		if aerr != nil {
			return auth.RouteMutationAuthorization{}, request, managedStopAuthOutcome(aerr)
		}
		return az, request, ""
	}
	var outcome ManagedStopOutcome
	if out.entryAuthorization, out.entryRequest, outcome = ask(q); outcome != "" {
		return out, refuseManagedStop(outcome, "the entry authorization was not answered with a permit"), nil
	}
	runQuestion := ManagedStopQuestion{
		Permission: permRunWrite,
		Resource: auth.ResourceAttrs{
			Kind:        permRunWrite.Resource(),
			ID:          req.RunRef,
			WorkspaceID: out.workspace,
		},
		Route: managedRunStopMetadata,
	}
	if out.run, out.runReq, outcome = ask(runQuestion); outcome != "" {
		return out, refuseManagedStop(outcome, "the run question was not answered with a permit"), nil
	}
	if out.bound {
		leaseQuestion := ManagedStopQuestion{
			Permission: permLeaseAdmin,
			Resource: auth.ResourceAttrs{
				Kind:        permLeaseAdmin.Resource(),
				ID:          req.Work.WorkItemID.String(),
				WorkspaceID: out.itemWorkspace,
			},
			Route: managedStopLeaseMetadata,
		}
		if out.lease, out.leaseReq, outcome = ask(leaseQuestion); outcome != "" {
			return out, refuseManagedStop(outcome, "the work-lease question was not answered with a permit"), nil
		}
	}
	return out, ManagedStopResult{}, nil
}

// managedStopAuthOutcome maps an authorization refusal onto the closed
// vocabulary, exactly as correction 1 §5.4 lists it. A step-up or scoped-grant
// requirement is distinct from a denial because the caller's remedy differs, and
// an absent authorizer is the unwired composition rather than a refusal.
//
// ⛔ THE DEFAULT IS AUTHORITY_UNAVAILABLE, NEVER FORBIDDEN. An undecided route, an
// unavailable principal-evidence resolver and any backend fault all mean nothing
// was decided; reporting one of them as a denial would tell an operator "you may
// not" about a question nobody answered.
func managedStopAuthOutcome(err error) ManagedStopOutcome {
	switch {
	case errors.Is(err, auth.ErrStepUpRequired):
		return ManagedStopStepUpRequired
	case errors.Is(err, auth.ErrScopedGrantRequired):
		return ManagedStopScopedGrantRequired
	case errors.Is(err, auth.ErrRouteDenied):
		return ManagedStopForbidden
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ManagedStopCanceled
	case errors.Is(err, auth.ErrAuthorizerUnavailable):
		return ManagedStopUnwired
	default:
		return ManagedStopAuthorityUnavailable
	}
}

// managedStopTargetFacts are the server-owned facts Phase B reads. Not one of
// them comes from the request.
type managedStopTargetFacts struct {
	workspace     model.ID
	claimSID      string
	bound         bool
	itemWorkspace model.ID
}

var (
	errManagedStopConcealed      = errors.New("sessions: managed stop target is concealed")
	errManagedStopWorkRequired   = errors.New("sessions: managed stop target is work-bound")
	errManagedStopWorkUnexpected = errors.New("sessions: managed stop target carries no work binding")
	errManagedStopWorkStale      = errors.New("sessions: managed stop work binding is stale")
)

// classifyManagedStopTargetError turns a target-read failure into a refusal.
// refused=false means there is nothing to refuse.
func classifyManagedStopTargetError(err error) (ManagedStopOutcome, string, bool) {
	switch {
	case err == nil:
		return "", "", false
	case errors.Is(err, errManagedStopConcealed),
		errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrWorkspaceConfinement),
		errors.Is(err, store.ErrWorkspaceLineageRequired):
		return ManagedStopConcealed, "the run is not visible in this workspace", true
	case errors.Is(err, errManagedStopWorkRequired):
		return ManagedStopWorkLeaseRequired, "the run is work-bound and no work binding was presented", true
	case errors.Is(err, errManagedStopWorkUnexpected):
		return ManagedStopWorkLeaseUnexpected, "the run carries no work binding", true
	case errors.Is(err, errManagedStopWorkStale):
		return ManagedStopWorkLeaseStale, "the run's work stamp is not the binding presented", true
	default:
		if we := asWorkError(err); we != nil && we.verdict == VerdictBroken {
			return ManagedStopWorkLeaseStale, "the run's work stamp is not the binding presented", true
		}
		return ManagedStopAuthorityUnavailable, "the run's authority facts could not be read", true
	}
}

// readManagedStopTarget reads the run through the CONFINED scope and classifies
// its work stamp with the runtime's own parser, so a managed Stop and a
// work-fenced control cannot disagree about what "bound" means.
//
// ⛔ BOTH ROWS ARE READ CONFINED, in the ratified order: run, workspace equality,
// stamp classification, work item. The creation producer admits a work-bound run
// only when its item shares the run's lineage, so a lawful item is visible here.
// An item that has since moved outside the run's boundary is hidden, and the Stop
// is CONCEALED rather than asked about a workspace the route never authorized.
// The work lease remains its own question, asked on the item.
func readManagedStopTarget(
	ctx context.Context,
	sc store.Scope,
	boundary model.ID,
	req ManagedStopRequest,
) (managedStopTargetFacts, error) {
	var out managedStopTargetFacts
	repo, err := sc.Ext(runKind)
	if err != nil {
		return out, err
	}
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colRunRef, req.RunRef)},
		Limit:   1,
	})
	if err != nil {
		return out, err
	}
	if len(rows) == 0 {
		return out, errManagedStopConcealed
	}
	rec := rows[0]
	ws, perr := model.ParseID(rec.String(colRunAuthzWorkspaceID))
	if perr != nil || ws.IsZero() || ws != boundary {
		// The confinement filter already excludes an unset or foreign lineage, so
		// none of these is reachable while the boundary holds. The comparison is
		// made anyway and deny-closed, because "unreachable" is a property of the
		// engine as it is today and this is the check that says what the module
		// actually requires: the run's OWN lineage is the authorization scope.
		return out, errManagedStopConcealed
	}
	out.workspace, out.claimSID = ws, rec.String(colRunClaimSID)

	bound := runCarriesWorkStamp(rec)
	switch {
	case !bound && req.Work != nil:
		return out, errManagedStopWorkUnexpected
	case !bound:
		return out, nil
	case req.Work == nil:
		return out, errManagedStopWorkRequired
	}
	stamp, serr := parseRunWorkStamp(rec, req.Work.LeaseFence, false)
	if serr != nil {
		return out, serr
	}
	if stamp.itemID != req.Work.WorkItemID || stamp.ownerEpoch != req.Work.OwnerEpoch {
		return out, errManagedStopWorkStale
	}
	out.bound = true
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return out, err
	}
	itemRec, err := items.Get(ctx, stamp.itemID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return out, errManagedStopConcealed
		}
		return out, err
	}
	itemWS, perr := model.ParseID(itemRec.String(colWorkWorkspaceID))
	if perr != nil || itemWS.IsZero() || itemWS != boundary {
		return out, errManagedStopConcealed
	}
	out.itemWorkspace = itemWS
	return out, nil
}

// runCarriesWorkStamp reports whether the run names any work binding at all. It
// is the SAME four columns parseRunWorkStamp reads, and it exists because that
// parser answers "is this the presented generation?", not "is there one".
func runCarriesWorkStamp(rec model.Record) bool {
	for _, column := range []string{
		colRunWorkItemID, colRunWorkLeaseFence, colRunWorkDispatchKey, colRunWorkOwnerEpoch,
	} {
		if !rec.IsNull(column) {
			return true
		}
	}
	return false
}

// lookupManagedStop is Phase R / C1: does a journal row already answer this
// operation? It binds a LOOKUP handle, which writes nothing, neither restricts
// nor seals its scope, and never signals anything.
func (m *Module) lookupManagedStop(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	req ManagedStopRequest,
	operationRef, semantic string,
) (ManagedStopResult, bool, error) {
	var (
		op          model.EvidenceOperation
		found       bool
		rebind      bool
		observation string
	)
	err := m.data.View(ctx, tenant, func(raw store.Scope) error {
		sc, cerr := store.ConfineWorkspace(ctx, raw, workspace)
		if cerr != nil {
			return cerr
		}
		claimer, cerr := custodialClaimer(sc)
		if cerr != nil {
			return cerr
		}
		h, berr := claimer.BindCustodialEffect(ctx,
			managedStopBinding(store.CustodialLookup, req, operationRef, semantic, 0))
		if berr != nil {
			return berr
		}
		var lerr error
		op, found, lerr = h.Lookup(ctx)
		if errors.Is(lerr, store.ErrEvidenceRebind) {
			rebind = true
			return nil
		}
		if lerr != nil || !found {
			return lerr
		}
		// The exact-launch P1 travels with the recorded row (correction 1 §7). An
		// observation that cannot be read is reported as none, never as an exit.
		if obs, oerr := readExactLaunchObservation(ctx, raw, req.RunRef, req.ExpectedLaunch); oerr == nil {
			observation = obs
		}
		return nil
	})
	switch {
	case rebind:
		return ManagedStopResult{
			Outcome:      ManagedStopOperationConflict,
			OperationRef: operationRef,
			Detail:       "the operation id names another effect in this tenant",
		}, true, nil
	case errors.Is(err, store.ErrCustodyTargetConcealed),
		errors.Is(err, store.ErrWorkspaceConfinement):
		return refuseManagedStop(ManagedStopConcealed,
			"the run is not visible in this workspace"), true, nil
	case errors.Is(err, store.ErrCustodyUnavailable), errors.Is(err, ErrManagedStopUnwired):
		return refuseManagedStop(ManagedStopUnwired,
			"the engine cannot bind the managed stop surface"), true, err
	case err != nil:
		return ManagedStopResult{}, false, err
	case !found:
		return ManagedStopResult{}, false, nil
	}
	return classifyManagedStopRow(op, operationRef, observation), true, nil
}

// classifyManagedStopRow turns a recorded journal row into an answer.
//
// ⛔ A CLAIMED ROW IS REPORTED UNKNOWN HERE, NOT IN PROGRESS. Only the
// originating call — which still holds its handle and its run token — may say
// in_progress, and this path holds neither. Saying otherwise after a restart or
// a leader change would be an adoption of somebody else's operation.
func classifyManagedStopRow(op model.EvidenceOperation, operationRef, observation string) ManagedStopResult {
	res := ManagedStopResult{OperationRef: operationRef, Settlement: op.State, Observation: observation}
	switch op.State {
	case model.EvidenceOpRefused:
		// A burned identity is not a settlement: it is the absence of one.
		res.Settlement = ""
		res.Outcome = ManagedStopSpoolDegraded
		res.Detail = "the operation identity was burned without a ledger anchor"
	case model.EvidenceOpClaimed:
		res.Outcome = ManagedStopUnknown
		res.Detail = "the operation is claimed and is never re-dispatched"
		if observation == obsProcessExitObserved {
			res.Outcome = ManagedStopObservedExitUnsettled
			res.Detail = "the expected launch's exit was observed, and nothing settled the operation"
		}
	case model.EvidenceOpCompleted:
		res.Outcome = ManagedStopStopped
		res.Attempted = true
	default:
		res.Outcome = ManagedStopUnknown
		res.Detail = "the operation was already settled"
	}
	return res
}

// managedStopAdmission is what Phase D decided, plus the facts later phases need
// from it: the leader epoch the claim committed under, and whether the run held
// any runtime credential for the revocation phase to withdraw.
type managedStopAdmission struct {
	result         ManagedStopResult
	epoch          uint64
	hadCredentials bool
}

// admitManagedStop is Phase D: ONE mutation carrying the authority lock, the
// target checks, the work binding and the custodial claim.
func (m *Module) admitManagedStop(
	ctx context.Context,
	tenant model.TenantID,
	req ManagedStopRequest,
	authority managedStopAuthority,
	lr *liveRun,
	operationRef, semantic string,
) (managedStopAdmission, error) {
	var (
		outcome        ManagedStopOutcome
		detail         string
		epoch          uint64
		bound          bool
		hadCredentials bool
	)
	// The confined scope answers for the RUN and for the engine binding; every
	// other row this phase touches — the claim, the provider profile, the work
	// item and its lease — is reached RAW from a fact the confinement already
	// admitted, exactly as the engine reaches the claim it qualifies. Confining
	// them would refuse lawful rows for a reason that is not true.
	err := m.data.Mutate(ctx, tenant, func(raw store.Scope) error {
		sc, cerr := store.ConfineWorkspace(ctx, raw, authority.workspace)
		if cerr != nil {
			return cerr
		}
		// D1: the transaction's own clock, then the retained authorities at that
		// instant, then AM1. No question is re-asked; freshness is re-verified.
		clock, ok := raw.(store.TransactionClock)
		if !ok {
			outcome, detail = ManagedStopAuthorityUnavailable, "the store exposes no transaction clock"
			return errManagedStopRollback
		}
		now1, terr := clock.TransactionNow(ctx)
		if terr != nil {
			return terr
		}
		bundle, berr := m.managedStopBundle(now1.Time(), authority)
		if berr != nil {
			outcome, detail = ManagedStopAuthorityUnavailable, "the retained authority is no longer fresh"
			return errManagedStopRollback
		}
		// D2: the FIRST lock-bearing call of the callback, as the capability's
		// contract requires. Its leased-fact touches carry the engine's own
		// authority_touch origin, which is the one write the custodial gate admits
		// before a binding exists.
		locker, ok := raw.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			outcome, detail = ManagedStopAuthorityUnavailable, "the store exposes no directory authority lock"
			return errManagedStopRollback
		}
		if lerr := locker.LockDirectoryAuthoritySnapshot(ctx, bundle); lerr != nil {
			outcome, detail = ManagedStopAuthorityUnavailable, "the authority snapshot could not be pinned"
			return errManagedStopRollback
		}
		// D2b: re-verify AFTER the locks. The pin is transaction-local; what it
		// cannot do is make an expired evidence window valid again.
		now2, terr := clock.TransactionNow(ctx)
		if terr != nil {
			return terr
		}
		if _, berr := m.managedStopBundle(now2.Time(), authority); berr != nil {
			outcome, detail = ManagedStopAuthorityUnavailable, "the retained authority expired under the locks"
			return errManagedStopRollback
		}
		// D3: the target, re-read IN THIS TRANSACTION and under confinement.
		repo, rerr := sc.Ext(runKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := findRunRec(ctx, repo, req.RunRef)
		if rerr != nil {
			if errors.Is(rerr, store.ErrNotFound) {
				outcome, detail = ManagedStopConcealed, "the run is no longer visible"
				return errManagedStopRollback
			}
			return rerr
		}
		if rec.String(colState) != stateRunning {
			outcome, detail = ManagedStopLaunchSuperseded, "the run is no longer running"
			return errManagedStopRollback
		}
		if rec.String(colRuntimeLaunchID) != req.ExpectedLaunch.String() {
			outcome, detail = ManagedStopLaunchSuperseded, "the run carries another launch generation"
			return errManagedStopRollback
		}
		if aerr := assertLaunchOwnsRow(lr, rec); aerr != nil {
			outcome, detail = ManagedStopClaimLost, "the run no longer answers to this launch"
			return errManagedStopRollback
		}
		if perr := assertCurrentProfileHolds(ctx, raw, rec); perr != nil {
			outcome, detail = managedStopProfileOutcome(perr)
			return errManagedStopRollback
		}
		hadCredentials = rec.String(colWorkCredentialID) != "" || rec.String(colCommunicationCredentialID) != ""
		// D4a: the lapse probe. A lapsed claim COMMITS its retirement and nothing
		// else, and it is reported as a retirement rather than as a stop: the
		// operator's Stop did not happen, and saying it did would be a fabrication.
		//
		// The subject is read from the RUN ROW, which is how the engine reaches the
		// claim it qualifies. Taking it from the live handle would let this probe
		// and the engine's qualification look at two different rows.
		subject := rec.String(colRunClaimSID)
		if subject == "" {
			outcome, detail = ManagedStopClaimLost, "the run carries no admission stamp"
			return errManagedStopRollback
		}
		claimRec, found, cerr2 := findClaim(ctx, raw, subject)
		if cerr2 != nil {
			return cerr2
		}
		if !found {
			outcome, detail = ManagedStopClaimLost, "the run's admission claim is gone"
			return errManagedStopRollback
		}
		if !claimIsLive(claimRec, now2.Time()) {
			if _, _, rerr := retireIfLapsed(ctx, raw, claimRec, now2.Time()); rerr != nil {
				return rerr
			}
			outcome, detail = ManagedStopClaimRetired, "the admission claim had lapsed and was retired"
			return nil
		}
		// D5: the work binding, re-proved under the work item's own cluster lock.
		if authority.bound {
			if werr := m.assertManagedStopWork(ctx, raw, tenant, req, authority); werr != nil {
				outcome, detail = ManagedStopWorkLeaseStale, "the work lease is not the one presented"
				return errManagedStopRollback
			}
		}
		// D6-D8: bind, touch the QUALIFIED claim, and claim the operation.
		claimer, cerr3 := custodialClaimer(sc)
		if cerr3 != nil {
			outcome, detail = ManagedStopUnwired, "the store exposes no custodial effect"
			return errManagedStopRollback
		}
		h, berr := claimer.BindCustodialEffect(ctx, managedStopBinding(
			store.CustodialClaim, req, operationRef, semantic, claimRec.Int(model.ColVersion)))
		if berr != nil {
			outcome, detail = managedStopBindOutcome(berr)
			return errManagedStopRollback
		}
		if terr := h.TouchClaim(ctx); terr != nil {
			outcome, detail = ManagedStopClaimLost, "the admission claim moved under the touch"
			return errManagedStopRollback
		}
		res, cerr4 := h.Claim(ctx)
		if cerr4 != nil {
			outcome, detail = managedStopClaimOutcome(cerr4)
			return errManagedStopRollback
		}
		bound = true
		switch res.Outcome {
		case store.CustodialFreshAnchored:
			epoch, outcome = res.Op.LeaderEpoch, ManagedStopStopped
		case store.CustodialRefusedFresh:
			// The identity is burned and the loss accounting must COMMIT, so this
			// returns nil deliberately. Nothing is dispatched.
			outcome, detail = ManagedStopSpoolDegraded,
				"the claim evidence was dropped and the identity is burned"
		case store.CustodialReplayRefused:
			outcome, detail = ManagedStopSpoolDegraded,
				"the operation identity was burned without a ledger anchor"
		case store.CustodialReplayClaimed:
			outcome, detail = ManagedStopUnknown,
				"the operation is claimed and is never re-dispatched"
		default:
			// A settled replay that raced past phases R and C1. Its recorded row is
			// the answer, and it is the SAME classification those phases apply.
			replay := classifyManagedStopRow(res.Op, operationRef, "")
			outcome, detail = replay.Outcome, replay.Detail
		}
		return nil
	})
	res := refuseManagedStop(outcome, detail)
	if bound {
		res.OperationRef = operationRef
	}
	switch {
	case errors.Is(err, errManagedStopRollback):
		// A deliberate rollback whose verdict is already recorded: the refusal is
		// the answer, and the transaction carrying NOTHING is the point.
		return managedStopAdmission{result: res}, nil
	case err != nil && outcome != "":
		return managedStopAdmission{result: res}, nil
	case err != nil:
		return managedStopAdmission{result: refuseManagedStop(
			ManagedStopAuthorityUnavailable, "the admission transaction refused")}, err
	}
	lr.mu.Lock()
	live := !lr.workCredentialID.IsZero() || !lr.communicationCredentialID.IsZero()
	lr.mu.Unlock()
	return managedStopAdmission{result: res, epoch: epoch, hadCredentials: hadCredentials || live}, nil
}

// errManagedStopRollback carries an already-recorded verdict out of a callback
// while guaranteeing the transaction does not commit. It is never returned to a
// caller and never classified as a fault.
var errManagedStopRollback = errors.New("sessions: managed stop refusal rolls back")

// managedStopBundle re-verifies every retained authorization at ONE instant and
// merges the bundles. AM1's contract is not altered here: it is called, not
// reimplemented, and a merge refusal is a refusal.
func (m *Module) managedStopBundle(
	now time.Time,
	authority managedStopAuthority,
) (store.AuthoritySnapshotBundle, error) {
	entry, err := authority.entryAuthorization.AuthorityFor(now, authority.entryRequest)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	run, err := authority.run.AuthorityFor(now, authority.runReq)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	merged, err := auth.MergeAuthoritySnapshotBundles(entry, run)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	if !authority.bound {
		return merged, nil
	}
	lease, err := authority.lease.AuthorityFor(now, authority.leaseReq)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	return auth.MergeAuthoritySnapshotBundles(merged, lease)
}

// managedStopProfileOutcome separates "the profile is gone or retired" from "I
// could not look". assertCurrentProfileHolds returns a conflict for the first and
// the raw store error for the second, and collapsing them would report an outage
// as a retirement — an operator would go looking for a retirement that never
// happened.
func managedStopProfileOutcome(err error) (ManagedStopOutcome, string) {
	var re *runErr
	if errors.As(err, &re) && re.status == http.StatusConflict {
		return ManagedStopProfileRetired, "the run's provider profile no longer holds"
	}
	return ManagedStopAuthorityUnavailable, "the run's provider profile could not be read"
}

// managedStopBindOutcome maps an engine bind refusal onto the closed vocabulary.
func managedStopBindOutcome(err error) (ManagedStopOutcome, string) {
	switch {
	case errors.Is(err, store.ErrCustodyTargetConcealed):
		return ManagedStopConcealed, "the run is not visible to the engine"
	case errors.Is(err, store.ErrCustodyTargetGeneration):
		return ManagedStopLaunchSuperseded, "the engine sees another launch generation"
	case errors.Is(err, store.ErrCustodyClaimMismatch):
		return ManagedStopClaimLost, "the admission claim does not qualify"
	case errors.Is(err, store.ErrCustodyWriteSet), errors.Is(err, store.ErrCustodySealed):
		return ManagedStopAuthorityUnavailable, "the transaction's write set does not admit a custodial binding"
	case errors.Is(err, store.ErrCustodyLeaderUnavailable):
		return ManagedStopAuthorityUnavailable, "the durable leader fence is unavailable"
	case errors.Is(err, store.ErrCustodyUnavailable):
		return ManagedStopUnwired, "the engine cannot bind the managed stop surface"
	default:
		return ManagedStopAuthorityUnavailable, "the custodial binding refused"
	}
}

// managedStopClaimOutcome maps an engine claim refusal onto the closed
// vocabulary.
func managedStopClaimOutcome(err error) (ManagedStopOutcome, string) {
	switch {
	case errors.Is(err, store.ErrEvidenceRebind):
		return ManagedStopOperationConflict, "the operation id names another effect"
	case errors.Is(err, store.ErrEvidenceRaced):
		return ManagedStopOperationConflict, "a concurrent claim won the operation id"
	case errors.Is(err, store.ErrCustodyEpochChanged):
		return ManagedStopAuthorityUnavailable, "the cluster epoch moved under the claim"
	case errors.Is(err, store.ErrCustodyClaimMismatch):
		return ManagedStopClaimLost, "the admission claim does not qualify"
	default:
		return ManagedStopAuthorityUnavailable, "the custodial claim refused"
	}
}

// assertManagedStopWork re-proves the complete work binding under the item's
// cluster lock, comparing every one of the six facts the caller observed.
//
// The holder SID and run ref are the TARGETED runtime's, never the caller's: a
// work-session token that tried to present itself here has already failed
// question 3.
func (m *Module) assertManagedStopWork(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	req ManagedStopRequest,
	authority managedStopAuthority,
) error {
	e, err := loadLockedRunWork(ctx, sc, tenant, req.RunRef, req.Work.LeaseFence, false)
	if err != nil {
		return err
	}
	if err := assertActiveRunWork(e, req.Work.HolderRunRef, req.Work.HolderSID, req.Work.LeaseFence); err != nil {
		return err
	}
	if e.stamp.itemID != req.Work.WorkItemID || e.stamp.ownerEpoch != req.Work.OwnerEpoch {
		return errManagedStopWorkStale
	}
	if e.workspace != authority.itemWorkspace {
		return errManagedStopWorkStale
	}
	if e.lease.String(colLeaseExpiresAt) != req.Work.LeaseExpiresAt {
		// A renewal after the caller observed the lease makes its observation
		// stale, which is the whole reason the expiry travels with the request.
		return errManagedStopWorkStale
	}
	return nil
}

// managedStopPreEffect is Phase F: the checks that must hold immediately before
// the process boundary is crossed, in the ratified order, with NO I/O in the
// last one. An empty Outcome means "cross it".
//
// settle reports whether the refusal may be RECORDED. Every refusal is, except
// F3's: a node that cannot prove it still leads under the epoch the claim
// committed with must not write an outcome either, so that row stays claimed.
func (m *Module) managedStopPreEffect(
	ctx context.Context,
	tenant model.TenantID,
	req ManagedStopRequest,
	authority managedStopAuthority,
	lr *liveRun,
	ports *managedStopPorts,
	claimEpoch uint64,
) (ManagedStopResult, bool) {
	// F1: the durable claim/launch/profile authority, with the claim row in the
	// write set of its own transaction. This is the existing effect boundary.
	if err := m.assertRunAuthority(ctx, lr); err != nil {
		return refuseManagedStop(ManagedStopClaimLost,
			"the launch is no longer the run's durable authority"), true
	}
	// F2: the work lease, re-proved for a bound run.
	if authority.bound {
		// ⛔ A MUTATE THAT WRITES NOTHING, NOT A VIEW. The re-proof takes the work
		// item's transaction-scoped cluster lock, and a read-only scope refuses
		// coordination locks outright — measured: every lawful bound Stop reported
		// work_lease_stale with "scope is read-only". assertRunWorkLease, the
		// existing pre-effect half of fenced runtime control, has the same shape.
		if err := m.data.Mutate(ctx, tenant, func(raw store.Scope) error {
			return m.assertManagedStopWork(ctx, raw, tenant, req, authority)
		}); err != nil {
			return refuseManagedStop(ManagedStopWorkLeaseStale,
				"the work lease moved between admission and the effect"), true
		}
	}
	// F3: the durable epoch fence, against the epoch the CLAIM committed under.
	if err := store.EvidenceEpochFence(ctx, ports.elector, claimEpoch); err != nil {
		return refuseManagedStop(ManagedStopAuthorityUnavailable,
			"the durable leader fence refused before the effect"), false
	}
	// F4: NO I/O at all. Everything below is already in memory.
	if err := ctx.Err(); err != nil {
		return refuseManagedStop(ManagedStopCanceled, "the admission window ended before the effect"), true
	}
	// Wall clock, for the same reason managedStopAdmissionContext uses it: the
	// witness window this re-verifies was minted against the authorizer's clock.
	// There is no transaction here to ask, which is the whole point of F4.
	if _, aerr := m.managedStopBundle(time.Now(), authority); aerr != nil {
		return refuseManagedStop(ManagedStopAuthorityUnavailable,
			"the retained authority expired before the effect"), true
	}
	if current, ok := m.rt.getLive(tenant, req.RunRef); !ok || current != lr {
		return refuseManagedStop(ManagedStopNotLocallySupervised,
			"the runtime no longer registers this launch"), true
	}
	if lr.launchID != req.ExpectedLaunch {
		return refuseManagedStop(ManagedStopLaunchSuperseded, "the launch generation moved"), true
	}
	select {
	case <-lr.finalizedCh:
		return refuseManagedStop(ManagedStopNotLocallySupervised,
			"the run finalized before the effect"), true
	default:
	}
	return ManagedStopResult{}, false
}

// settleManagedStop records the terminal outcome through a NEW binding in its
// OWN transaction, and reports what was actually settled.
//
// A settlement that cannot be recorded leaves the row claimed, which is the safe
// non-replayable shape: a claimed operation is never re-dispatched. It therefore
// returns the empty state rather than an error — the effect already happened,
// and failing the caller here would invite a retry that cannot help. An empty
// Settlement on a crossed effect is exactly that: nothing durable was recorded.
func (m *Module) settleManagedStop(
	actx context.Context,
	ports *managedStopPorts,
	tenant model.TenantID,
	workspace model.ID,
	req ManagedStopRequest,
	operationRef, semantic string,
	state model.EvidenceOperationState,
	resultDigest, dispatchRef string,
) model.EvidenceOperationState {
	if !state.Terminal() {
		return ""
	}
	// Its OWN bound, detached from actx: an admission window that closed, or a
	// caller that walked away, must not be the reason an outcome is not recorded.
	ctx, cancel := managedStopStageContext(actx, ports)
	defer cancel()
	var settled model.EvidenceOperationState
	err := m.data.Mutate(ctx, tenant, func(raw store.Scope) error {
		sc, cerr := store.ConfineWorkspace(ctx, raw, workspace)
		if cerr != nil {
			return cerr
		}
		claimer, cerr := custodialClaimer(sc)
		if cerr != nil {
			return cerr
		}
		h, berr := claimer.BindCustodialEffect(ctx,
			managedStopBinding(store.CustodialSettle, req, operationRef, semantic, 0))
		if berr != nil {
			return berr
		}
		res, serr := h.Settle(ctx, store.CustodialSettlement{
			State: state, ResultDigest: resultDigest, DispatchRef: dispatchRef,
		})
		if serr != nil {
			return serr
		}
		if res.Dropped {
			// The outcome event was dropped; the row deliberately stays claimed and
			// the loss accounting must commit.
			return nil
		}
		settled = res.Op.State
		return nil
	})
	if err != nil {
		m.warnf("sessions: managed stop settlement was not recorded",
			"operation_ref", operationRef, "state", string(state))
		return ""
	}
	return settled
}
