// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// ⛔ EL RELEVO DE TRABAJO ENTRE SESIONES ES PRIMERO DE PROPIEDAD, NO DE LEASE.
//
// Esto se escribe AQUI —y no en un documento— porque es donde alguien va a intentar lo
// contrario. Medido contra un motor arrancado el 2026-08-24: un `lease.takeover` directo
// de la sesion A a la sesion B se rechaza con `owner_ineligible`, y el motor tiene razon:
// una sesion NO PUEDE sostener el lease de un item que no es suyo
// (`work_service.go`, el arm "session" de preflightIdentity: el `owner_ref` del item tiene
// que ser el `HolderSID`).
//
// La secuencia que SI funciona, con la cadena que devolvio el motor:
//
//	seq=3  work.lease.acquired   session:osn_…5f99   A adquiere        fence 1  active
//	seq=4  work.owner.changed    user                se mueve el dueno fence 2  REVOKED (A)
//	seq=5  work.lease.acquired   session:osn_…5fb2   B adquiere        fence 3  active
//
// Es decir: **mover la propiedad revoca el lease del titular viejo y avanza el fence en el
// mismo acto**, y solo entonces el nuevo dueno puede adquirir. No hay ventana en la que dos
// sesiones se crean duenas del mismo trabajo, y esa es la propiedad por la que existe el
// fence — no un efecto secundario.
//
// `lease.takeover` sigue teniendo su sitio y NO es el relevo entre pares: exige
// `principal.Admin` y presentar el fence vigente, o sea un operador retirando un lease de
// un titular que sigue siendo el dueno. Confundir los dos es lo que este comentario existe
// para evitar; la evidencia completa esta en
// `an internal design note (not shipped)`.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	workLeaseVacant     = "vacant"
	workLeaseActive     = "active"
	workLeaseReleased   = "released"
	workLeaseExpired    = "expired"
	workLeaseRevoked    = "revoked"
	defaultWorkLeaseTTL = 5 * time.Minute
	minWorkLeaseTTL     = 30 * time.Second
	maxWorkLeaseTTL     = 30 * time.Minute
)

var workLeaseTTLPolicy = fenceTTLPolicy{Default: defaultWorkLeaseTTL, Min: minWorkLeaseTTL, Max: maxWorkLeaseTTL}

func isWorkLeaseCommand(command string) bool { return strings.HasPrefix(command, "lease.") }

func workLeaseCommandNeedsClock(command string) bool {
	if isWorkLeaseCommand(command) {
		return true
	}
	switch command {
	case "item.submit", "item.block", "item.fail", "item.cancel", "item.assign",
		"item.unblock", "item.complete", "acceptance.evaluate":
		return true
	default:
		return false
	}
}

func workLeaseHolderKey(sid, runRef, agentRef string) string {
	return sid + "\x00" + runRef + "\x00" + agentRef
}

func optionalLeaseTime(rec model.Record, column string) (time.Time, error) {
	if rec.IsNull(column) {
		return time.Time{}, nil
	}
	ts, err := model.ParseTimestamp(rec.String(column))
	if err != nil {
		return time.Time{}, unknown("evidence_unavailable", err)
	}
	return ts.Time(), nil
}

func workLeaseFenceState(rec model.Record) (fenceState, error) {
	s := fenceState{Holder: workLeaseHolderKey(rec.String(colLeaseHolderSID), rec.String(colLeaseHolderRunRef), rec.String(colLeaseHolderAgentRef)), Fence: rec.Int(colLeaseFence), RenewalCount: rec.Int(colLeaseRenewalCount), EndReason: rec.String(colLeaseEndReason)}
	switch rec.String(colLeaseState) {
	case workLeaseVacant:
		s.Lifecycle = fenceVacant
	case workLeaseActive:
		s.Lifecycle = fenceActive
	case workLeaseReleased:
		s.Lifecycle = fenceReleased
	case workLeaseExpired:
		s.Lifecycle = fenceExpired
	case workLeaseRevoked:
		s.Lifecycle = fenceRevoked
	default:
		return fenceState{}, unknown("evidence_unavailable", nil)
	}
	var err error
	if s.AcquiredAt, err = optionalLeaseTime(rec, colLeaseAcquiredAt); err != nil {
		return fenceState{}, err
	}
	if s.RenewedAt, err = optionalLeaseTime(rec, colLeaseRenewedAt); err != nil {
		return fenceState{}, err
	}
	if s.ExpiresAt, err = optionalLeaseTime(rec, colLeaseExpiresAt); err != nil {
		return fenceState{}, err
	}
	if s.EndedAt, err = optionalLeaseTime(rec, colLeaseEndedAt); err != nil {
		return fenceState{}, err
	}
	return s, nil
}

func workLeaseLifecycleName(l fenceLifecycle) string {
	switch l {
	case fenceVacant:
		return workLeaseVacant
	case fenceActive:
		return workLeaseActive
	case fenceReleased:
		return workLeaseReleased
	case fenceExpired:
		return workLeaseExpired
	case fenceRevoked:
		return workLeaseRevoked
	}
	return ""
}

func nullableTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return model.NewTimestamp(at).String()
}

func applyWorkLeaseFenceState(rec model.Record, s fenceState, sid, runRef, agentRef string) {
	rec[colLeaseHolderSID], rec[colLeaseHolderRunRef], rec[colLeaseHolderAgentRef] = nullableString(sid), nullableString(runRef), nullableString(agentRef)
	rec[colLeaseFence], rec[colLeaseState] = s.Fence, workLeaseLifecycleName(s.Lifecycle)
	rec[colLeaseAcquiredAt], rec[colLeaseRenewedAt], rec[colLeaseExpiresAt] = nullableTime(s.AcquiredAt), nullableTime(s.RenewedAt), nullableTime(s.ExpiresAt)
	rec[colLeaseEndedAt], rec[colLeaseEndReason], rec[colLeaseRenewalCount] = nullableTime(s.EndedAt), nullableString(s.EndReason), s.RenewalCount
}

func findWorkLease(ctx context.Context, sc store.Scope, itemID model.ID) (model.Record, bool, error) {
	repo, err := sc.Ext(workLeaseKind)
	if err != nil {
		return nil, false, err
	}
	rows, err := listAll(ctx, repo, model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()})
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	if len(rows) != 1 {
		return nil, false, broken(http.StatusConflict, "state_conflict")
	}
	return rows[0], true, nil
}

func transactionNow(ctx context.Context, sc store.Scope) (model.Timestamp, error) {
	clock, ok := sc.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, unknown("clock_unavailable", nil)
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return model.Timestamp{}, unknown("clock_unavailable", err)
	}
	return now, nil
}

func leaseClockGuard(ctx context.Context, sc store.Scope, workspace model.ID) (model.Record, bool, error) {
	repo, err := sc.Ext(workGuardKind)
	if err != nil {
		return nil, false, err
	}
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: workspace.String()},
		model.Filter{Column: colGuardKind, Op: model.OpEq, Value: "lease_clock"})
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	if len(rows) != 1 {
		return nil, false, broken(http.StatusConflict, "state_conflict")
	}
	return rows[0], true, nil
}

func observeLeaseClock(ctx context.Context, sc store.Scope, workspace model.ID) (model.Timestamp, error) {
	now, err := transactionNow(ctx, sc)
	if err != nil {
		return model.Timestamp{}, err
	}
	guard, found, err := leaseClockGuard(ctx, sc, workspace)
	if err != nil {
		return model.Timestamp{}, err
	}
	if !found || guard.IsNull(colGuardLastDBTime) {
		return now, nil
	}
	last, err := model.ParseTimestamp(guard.String(colGuardLastDBTime))
	if err != nil {
		return model.Timestamp{}, unknown("evidence_unavailable", err)
	}
	if now.Before(last) {
		return model.Timestamp{}, unknown("clock_rollback", nil)
	}
	return now, nil
}

func lockLeaseTransaction(ctx context.Context, sc store.Scope, key string) error {
	locker, ok := sc.(store.TransactionLocker)
	if !ok {
		return unknown("coordination_unavailable", nil)
	}
	if err := locker.LockTransaction(ctx, key); err != nil {
		return unknown("coordination_unavailable", err)
	}
	return nil
}

func leaseClockCoordinationKey(tenant model.TenantID, workspace model.ID) string {
	return "sessions.work_lease_clock:" + tenant.String() + ":" + workspace.String()
}
func lockWorkLeaseItem(ctx context.Context, sc store.Scope, tenant model.TenantID, workspace, itemID model.ID) error {
	return lockLeaseTransaction(ctx, sc, "sessions.work_lease:"+tenant.String()+":"+workspace.String()+":"+itemID.String())
}

func lockLeaseClock(ctx context.Context, sc store.Scope, tenant model.TenantID, workspace model.ID) error {
	return lockLeaseTransaction(ctx, sc, leaseClockCoordinationKey(tenant, workspace))
}

func advanceLeaseClock(ctx context.Context, sc store.Scope, tenant model.TenantID, cmd WorkCommand, now model.Timestamp) error {
	if err := lockLeaseClock(ctx, sc, tenant, cmd.WorkspaceID); err != nil {
		return err
	}
	return advanceLeaseClockLocked(ctx, sc, cmd, now)
}

// advanceLeaseClockLocked records one accepted database-time observation after
// the caller has taken the workspace clock lock. Clock rebase deliberately calls
// this only from applyDomain: its plan must first observe the rollback it is
// authorizing, and the guard change must not precede the command's audit anchor.
func advanceLeaseClockLocked(ctx context.Context, sc store.Scope, cmd WorkCommand, now model.Timestamp) error {
	repo, err := sc.Ext(workGuardKind)
	if err != nil {
		return err
	}
	guard, found, err := leaseClockGuard(ctx, sc, cmd.WorkspaceID)
	if err != nil {
		return err
	}
	if !found {
		if cmd.Command == "lease.clock_rebase" {
			return broken(http.StatusConflict, "clock_not_rolled_back")
		}
		_, err = repo.Create(ctx, model.Record{colWorkWorkspaceID: cmd.WorkspaceID.String(), colGuardKind: "lease_clock", colGuardEpoch: int64(1), colGuardLastDBTime: now.String(), colGuardRebaseDecision: nil, colGuardRebaseEvidence: nil})
		return err
	}
	last, err := model.ParseTimestamp(guard.String(colGuardLastDBTime))
	if err != nil {
		return unknown("evidence_unavailable", err)
	}
	rollback := now.Before(last)
	if rollback && cmd.Command != "lease.clock_rebase" {
		return unknown("clock_rollback", nil)
	}
	if !rollback && cmd.Command == "lease.clock_rebase" {
		return broken(http.StatusConflict, "clock_not_rolled_back")
	}
	guard[colGuardEpoch], guard[colGuardLastDBTime] = guard.Int(colGuardEpoch)+1, now.String()
	if rollback {
		guard[colGuardRebaseDecision], guard[colGuardRebaseEvidence] = cmd.DecisionID.String(), cmd.EvidenceRef
	}
	_, err = repo.Update(ctx, guard)
	return err
}

func validateEffectiveLeaseDecision(ctx context.Context, sc store.Scope, item model.Record, decisionID model.ID) error {
	if decisionID.IsZero() {
		return broken(http.StatusUnprocessableEntity, "decision_required")
	}
	if err := effectiveDecision(ctx, sc, item, decisionID); err != nil {
		if we := asWorkError(err); we != nil && we.code == "acceptance_incomplete" {
			return broken(http.StatusUnprocessableEntity, "decision_required")
		}
		return err
	}
	return nil
}

func (m *Module) validateLeaseCommandInScope(ctx context.Context, sc store.Scope, principal WorkPrincipal, cmd WorkCommand, item model.Record) error {
	if cmd.Command == "lease.clock_rebase" {
		if !principal.Admin {
			return broken(http.StatusForbidden, "forbidden")
		}
		if err := validateEffectiveLeaseDecision(ctx, sc, item, cmd.DecisionID); err != nil {
			return err
		}
		_, err := observeLeaseClock(ctx, sc, model.ID(item.String(colWorkWorkspaceID)))
		if we := asWorkError(err); we != nil && we.code == "clock_rollback" {
			return nil
		}
		if err != nil {
			return err
		}
		return broken(http.StatusConflict, "clock_not_rolled_back")
	}
	now, err := observeLeaseClock(ctx, sc, model.ID(item.String(colWorkWorkspaceID)))
	if err != nil {
		return err
	}
	if cmd.Command == "lease.acquire" || cmd.Command == "lease.renew" ||
		cmd.Command == "lease.release" || cmd.Command == "lease.takeover" {
		if err := m.requireLiveWorkHolderClaim(ctx, sc, cmd.HolderSID, principal.SessionFence); err != nil {
			return err
		}
	}
	lease, found, err := findWorkLease(ctx, sc, recordID(item))
	if err != nil {
		return err
	}
	if !found {
		return unknown("evidence_unavailable", nil)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return err
	}
	token := fenceToken{Holder: workLeaseHolderKey(cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef), Fence: cmd.Fence}
	status := item.String(colWorkStatus)
	switch cmd.Command {
	case "lease.acquire", "lease.takeover":
		if !cmd.leaseHolderResolved {
			return unknown("evidence_unavailable", nil)
		}
		if cmd.Command == "lease.takeover" && state.Fence != cmd.Fence {
			return broken(http.StatusConflict, "stale_fence")
		}
		if cmd.Command == "lease.takeover" && !principal.Admin {
			return broken(http.StatusForbidden, "forbidden")
		}
		if cmd.Command == "lease.acquire" && !leasePrincipalMatches(principal, cmd) {
			return broken(http.StatusForbidden, "forbidden")
		}
		switch status {
		case "ready":
			if err := blockersCompleted(ctx, sc, recordID(item)); err != nil {
				return err
			}
		case "blocked":
			if !cmd.Unblock {
				return broken(http.StatusConflict, "illegal_transition")
			}
			if err := blockersCompleted(ctx, sc, recordID(item)); err != nil {
				return err
			}
		case "review":
			if !cmd.ChangesRequested {
				return broken(http.StatusConflict, "illegal_transition")
			}
			if err := validateEffectiveLeaseDecision(ctx, sc, item, cmd.DecisionID); err != nil {
				return err
			}
		case "active":
		default:
			return broken(http.StatusConflict, "illegal_transition")
		}
		if fenceIsLive(state, now.Time()) {
			if cmd.Command != "lease.takeover" || !cmd.Force {
				return broken(http.StatusConflict, "lease_held")
			}
			if !principal.Admin {
				return broken(http.StatusForbidden, "forbidden")
			}
			if err := validateEffectiveLeaseDecision(ctx, sc, item, cmd.DecisionID); err != nil {
				return err
			}
		}
	case "lease.renew", "lease.release":
		if !cmd.leaseHolderResolved {
			return unknown("evidence_unavailable", nil)
		}
		if !leasePrincipalMatches(principal, cmd) {
			return broken(http.StatusForbidden, "forbidden")
		}
		if assertFence(state, token, now.Time()) != nil {
			return broken(http.StatusConflict, "stale_fence")
		}
	case "lease.revoke":
		if !principal.Admin && !cmd.internal {
			return broken(http.StatusForbidden, "forbidden")
		}
		if state.Lifecycle != fenceActive || state.Fence != cmd.Fence {
			return broken(http.StatusConflict, "stale_fence")
		}
	case "lease.expire":
		if !cmd.internal || state.Lifecycle != fenceActive || state.Fence != cmd.Fence || fenceIsLive(state, now.Time()) {
			return broken(http.StatusConflict, "stale_fence")
		}
	case "lease.owner_died":
		if !cmd.internal || state.Lifecycle != fenceActive || state.Fence != cmd.Fence || lease.String(colLeaseHolderSID) != cmd.HolderSID || (cmd.HolderRunRef != "" && lease.String(colLeaseHolderRunRef) != cmd.HolderRunRef) {
			return broken(http.StatusConflict, "stale_fence")
		}
	default:
		return broken(http.StatusBadRequest, "invalid_command")
	}
	return nil
}

func (m *Module) requireLiveWorkHolderClaim(
	ctx context.Context,
	sc store.Scope,
	sid string,
	expectedFence int64,
) error {
	claim, found, err := findClaim(ctx, sc, sid)
	if err != nil {
		return err
	}
	if !found || !claimIsLive(claim, m.now()) ||
		(expectedFence > 0 && claim.Int(colFence) != expectedFence) {
		return broken(http.StatusUnprocessableEntity, "owner_ineligible")
	}
	return nil
}

// touchLiveWorkHolderClaim makes the session-liveness proof part of the same
// transaction as the WorkLease mutation. The no-op Update is intentional: the
// repository writes with an expected version, so a concurrent Claim release or
// takeover either wins before this read (and is observed) or makes this
// transaction lose OCC. Without the write-set touch, OwnerDied could scan before
// acquire committed and no later callback would revoke the dead holder's lease.
func (m *Module) touchLiveWorkHolderClaim(
	ctx context.Context,
	sc store.Scope,
	sid string,
	expectedFence int64,
) error {
	repo, err := sc.Ext(claimKind)
	if err != nil {
		return err
	}
	claim, found, err := findClaim(ctx, sc, sid)
	if err != nil {
		return err
	}
	// Claim owns its own clock semantics (m.clock), whereas WorkLease uses the
	// database clock. Mixing the WorkLease timestamp into this decision would
	// turn node/DB skew into false authority or a false refusal.
	if !found || !claimIsLive(claim, m.now()) ||
		(expectedFence > 0 && claim.Int(colFence) != expectedFence) {
		return broken(http.StatusUnprocessableEntity, "owner_ineligible")
	}
	_, err = repo.Update(ctx, claim)
	return err
}

func leasePrincipalMatches(principal WorkPrincipal, cmd WorkCommand) bool {
	if cmd.internal {
		return true
	}
	// holder_sid is execution authority, not routing metadata. Every external
	// holder operation therefore needs a server-authenticated canonical SID and
	// must match it exactly. AgentIdentity is intentionally not a fallback: one
	// agent credential may drive several sibling sessions and cannot distinguish
	// which one is calling.
	return principal.SessionID != "" && principal.SessionID == cmd.HolderSID
}

func (m *Module) validateExecutionLeaseInScope(ctx context.Context, sc store.Scope, principal WorkPrincipal, cmd WorkCommand, item model.Record) error {
	if !leasePrincipalMatches(principal, cmd) {
		return broken(http.StatusForbidden, "forbidden")
	}
	now, err := observeLeaseClock(ctx, sc, model.ID(item.String(colWorkWorkspaceID)))
	if err != nil {
		return err
	}
	if err := m.requireLiveWorkHolderClaim(ctx, sc, cmd.HolderSID, principal.SessionFence); err != nil {
		return err
	}
	lease, found, err := findWorkLease(ctx, sc, recordID(item))
	if err != nil {
		return err
	}
	if !found {
		return unknown("evidence_unavailable", nil)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return err
	}
	if assertFence(state, fenceToken{Holder: workLeaseHolderKey(cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef), Fence: cmd.Fence}, now.Time()) != nil {
		return broken(http.StatusConflict, "stale_fence")
	}
	return nil
}

func (m *Module) requireNoLiveLeaseInScope(ctx context.Context, sc store.Scope, item model.Record) error {
	now, err := observeLeaseClock(ctx, sc, model.ID(item.String(colWorkWorkspaceID)))
	if err != nil {
		return err
	}
	lease, found, err := findWorkLease(ctx, sc, recordID(item))
	if err != nil || !found {
		return err
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return err
	}
	if fenceIsLive(state, now.Time()) {
		return broken(http.StatusConflict, "lease_held")
	}
	return nil
}

func workLeaseTTL(seconds int64) time.Duration {
	if seconds == 0 {
		return defaultWorkLeaseTTL
	}
	return time.Duration(seconds) * time.Second
}
func mapWorkFenceError(err error) error {
	if errors.Is(err, errFenceHeld) {
		return broken(http.StatusConflict, "lease_held")
	}
	if errors.Is(err, errFenceLost) {
		return broken(http.StatusConflict, "stale_fence")
	}
	if errors.Is(err, ErrFenceExhausted) {
		return &workError{status: http.StatusConflict, code: "fence_exhausted", verdict: VerdictBroken, cause: ErrFenceExhausted}
	}
	return err
}

func createVacantWorkLease(ctx context.Context, sc store.Scope, workspace, itemID model.ID) error {
	repo, err := sc.Ext(workLeaseKind)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{colWorkWorkspaceID: workspace.String(), colWorkItemID: itemID.String(), colLeaseHolderSID: nil, colLeaseHolderRunRef: nil, colLeaseHolderAgentRef: nil, colLeaseFence: int64(0), colLeaseState: workLeaseVacant, colLeaseAcquiredAt: nil, colLeaseRenewedAt: nil, colLeaseExpiresAt: nil, colLeaseEndedAt: nil, colLeaseEndReason: nil, colLeaseRenewalCount: int64(0)})
	return err
}

func resetAcceptanceForChanges(ctx context.Context, sc store.Scope, item model.Record) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo, model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID)})
	if err != nil {
		return err
	}
	for _, row := range rows {
		row[colAccState], row[colAccEvidenceRef], row[colAccEvidenceHash] = "pending", nil, nil
		row[colAccVerifiedByKind], row[colAccVerifiedByRef], row[colAccVerifiedAt], row[colAccWaiverDecisionID] = nil, nil, nil, nil
		if _, err := repo.Update(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func bindRunToWorkLease(ctx context.Context, sc store.Scope, item, lease model.Record) error {
	runRef := lease.String(colLeaseHolderRunRef)
	if runRef == "" {
		return nil
	}
	runs, err := sc.Ext(runKind)
	if err != nil {
		return err
	}
	run, err := findRunRec(ctx, runs, runRef)
	if err != nil {
		return err
	}
	// K4 persists a complete PENDING launch reservation before lease.acquire.
	// Unlike a K2 re-adoption, that row already names the exact generation it is
	// allowed to spawn under. Refuse to rewrite it to a generation won by a race;
	// the whole lease command then rolls back and Runner is never reached.
	if !run.IsNull(colRunWorkLaunchSpecHash) {
		expectedDispatch := runtimeWorkDispatchKey(
			recordID(item), item.Int(colWorkOwnerEpoch), lease.Int(colLeaseFence),
		)
		if len(run.Bytes(colRunWorkLaunchSpecHash)) != len(expectedDispatch) ||
			run.String(colRunWorkItemID) != item.String(model.ColID) ||
			run.Int(colRunWorkLeaseFence) != lease.Int(colLeaseFence) ||
			run.Int(colRunWorkOwnerEpoch) != item.Int(colWorkOwnerEpoch) ||
			!bytesEqual(run.Bytes(colRunWorkDispatchKey), expectedDispatch[:]) {
			return broken(http.StatusConflict, "dispatch_conflict")
		}
	}
	// A run may carry the stamp of a SUPERSEDED generation of THIS SAME item and
	// still be re-bound: we are inside the transaction that just made a newer
	// generation active for it, and the lease row is one-per-item with a fence
	// that only moves forward, so a strictly lower fence on this item is provably
	// over. Requiring fence EQUALITY made a re-acquire answer dispatch_conflict
	// forever once a generation ended (B3): the stamp is never cleared — it is the
	// durable evidence the fenced stop and the ambiguity record read back, see
	// refuseLegacyControlUnderWork — so the run stayed unadoptable for the rest of
	// its life, and the item it had been dispatched for could never take it again.
	//
	// A stamp naming a DIFFERENT item, or a fence at or above this one, is still a
	// conflict: those are two live dispatches, which is what the check is for.
	if runHasWorkBinding(run) && (run.String(colRunWorkItemID) != item.String(model.ColID) ||
		run.Int(colRunWorkLeaseFence) > lease.Int(colLeaseFence)) {
		return broken(http.StatusConflict, "dispatch_conflict")
	}
	dispatch := runtimeWorkDispatchKey(recordID(item), item.Int(colWorkOwnerEpoch), lease.Int(colLeaseFence))
	run[colRunWorkItemID], run[colRunWorkLeaseFence], run[colRunWorkDispatchKey], run[colRunWorkOwnerEpoch] = item.String(model.ColID), lease.Int(colLeaseFence), dispatch[:], item.Int(colWorkOwnerEpoch)
	_, err = runs.Update(ctx, run)
	return err
}

func rememberFenceExhaustion(cmd WorkCommand) {
	if cmd.postCommitRefusal != nil && *cmd.postCommitRefusal == nil {
		*cmd.postCommitRefusal = mapWorkFenceError(ErrFenceExhausted)
	}
}

func terminalAtFenceExhaustion(
	state fenceState,
	now time.Time,
	lifecycle fenceLifecycle,
	reason string,
) fenceState {
	state.Lifecycle = lifecycle
	state.ExpiresAt = now
	state.EndedAt = now
	state.EndReason = reason
	return state
}

type workLeaseEndFact struct {
	present        bool
	leaseID        model.ID
	workStatus     string
	state          string
	holderSID      string
	holderRunRef   string
	holderAgentRef string
	fence          int64
	expiresAt      string
	endReason      string
}

func (m *Module) applyLeaseCommand(
	ctx context.Context,
	sc store.Scope,
	cmd WorkCommand,
	item model.Record,
	now model.Timestamp,
	materializedEnd *workLeaseEndFact,
) (model.Record, model.ID, error) {
	if cmd.Command == "lease.clock_rebase" {
		if err := advanceLeaseClockLocked(ctx, sc, cmd, now); err != nil {
			return nil, "", err
		}
		guard, found, err := leaseClockGuard(ctx, sc, model.ID(item.String(colWorkWorkspaceID)))
		if err != nil {
			return nil, "", err
		}
		if !found {
			return nil, "", unknown("evidence_unavailable", nil)
		}
		updated, err := updateWorkItemWithEvent(ctx, sc, item)
		return updated, recordID(guard), err
	}
	repo, err := sc.Ext(workLeaseKind)
	if err != nil {
		return nil, "", err
	}
	lease, found, err := findWorkLease(ctx, sc, recordID(item))
	if err != nil || !found {
		return nil, "", unknown("evidence_unavailable", err)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return nil, "", err
	}
	holder := workLeaseHolderKey(cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef)
	token := fenceToken{Holder: holder, Fence: cmd.Fence}
	var observedExpiry *workLeaseEndFact
	switch cmd.Command {
	case "lease.acquire", "lease.takeover":
		expiryMaterialized := false
		if state.Lifecycle == fenceActive && !fenceIsLive(state, now.Time()) {
			var changed bool
			state, changed, err = materializeExpiry(state, now.Time(), "lease_expired", true)
			if !changed {
				return nil, "", broken(http.StatusConflict, "lease_held")
			}
			if errors.Is(err, ErrFenceExhausted) {
				rememberFenceExhaustion(cmd)
			} else if err != nil {
				return nil, "", err
			}
			applyWorkLeaseFenceState(lease, state, lease.String(colLeaseHolderSID), lease.String(colLeaseHolderRunRef), lease.String(colLeaseHolderAgentRef))
			lease, err = repo.Update(ctx, lease)
			if err != nil {
				return nil, "", err
			}
			observedExpiry = &workLeaseEndFact{
				present: true, leaseID: recordID(lease), workStatus: item.String(colWorkStatus),
				state: lease.String(colLeaseState), holderSID: lease.String(colLeaseHolderSID),
				holderRunRef: lease.String(colLeaseHolderRunRef), holderAgentRef: lease.String(colLeaseHolderAgentRef),
				fence: lease.Int(colLeaseFence), expiresAt: lease.String(colLeaseExpiresAt),
				endReason: lease.String(colLeaseEndReason),
			}
			expiryMaterialized = true
			if cmd.postCommitRefusal != nil && *cmd.postCommitRefusal != nil {
				if item.String(colWorkStatus) == "active" {
					item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "blocked", "lease_expired", "lease_expired"
				}
				item, err = updateWorkItemWithEvent(ctx, sc, item)
				return item, recordID(lease), err
			}
		}
		if state.Lifecycle == fenceActive && fenceIsLive(state, now.Time()) {
			if !cmd.Force || cmd.Command != "lease.takeover" || state.Holder == holder {
				return nil, "", broken(http.StatusConflict, "lease_held")
			}
			next, err := nextFence(state.Fence)
			if err != nil {
				return nil, "", err
			}
			state = fenceState{Holder: holder, Fence: next, Lifecycle: fenceActive, AcquiredAt: now.Time(), ExpiresAt: now.Time().Add(workLeaseTTL(cmd.TTLSeconds))}
		} else {
			state, err = fenceAcquire(state, holder, now.Time(), workLeaseTTL(cmd.TTLSeconds), workLeaseTTLPolicy)
			if errors.Is(err, ErrFenceExhausted) && expiryMaterialized {
				// Expiry from MaxInt64-1 consumed the final monotonic token. Keep
				// that safe terminal fact and refuse the impossible reacquire only
				// after its event and receipt commit.
				rememberFenceExhaustion(cmd)
				if item.String(colWorkStatus) == "active" {
					item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] =
						"blocked", "lease_expired", "lease_expired"
				}
				item, err = updateWorkItemWithEvent(ctx, sc, item)
				return item, recordID(lease), err
			}
			if err != nil {
				return nil, "", mapWorkFenceError(err)
			}
		}
		applyWorkLeaseFenceState(lease, state, cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef)
		lease, err = repo.Update(ctx, lease)
		if err != nil {
			return nil, "", err
		}
		if err := bindRunToWorkLease(ctx, sc, item, lease); err != nil {
			return nil, "", err
		}
		if item.String(colWorkStatus) == "review" {
			if err := resetAcceptanceForChanges(ctx, sc, item); err != nil {
				return nil, "", err
			}
			item[colWorkAcceptanceRevision] = item.Int(colWorkAcceptanceRevision) + 1
		}
		item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "active", nil, nil
		if item.IsNull(colWorkStartedAt) {
			item[colWorkStartedAt] = now.String()
		}
	case "lease.renew":
		state, err = fenceRenew(state, token, now.Time(), workLeaseTTL(cmd.TTLSeconds), workLeaseTTLPolicy)
		if err != nil {
			return nil, "", mapWorkFenceError(err)
		}
		applyWorkLeaseFenceState(lease, state, cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef)
		lease, err = repo.Update(ctx, lease)
	case "lease.release":
		state, err = fenceRelease(state, token, now.Time(), cmd.Reason, fenceEndPolicy{Lifecycle: fenceReleased, Bump: true, RequireLive: true})
		if errors.Is(err, ErrFenceExhausted) {
			state = terminalAtFenceExhaustion(state, now.Time(), fenceReleased, cmd.Reason)
			rememberFenceExhaustion(cmd)
		} else if err != nil {
			return nil, "", mapWorkFenceError(err)
		}
		applyWorkLeaseFenceState(lease, state, cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef)
		lease, err = repo.Update(ctx, lease)
		if err == nil && item.String(colWorkStatus) == "active" {
			blockedReason := cmd.Reason
			if blockedReason == "" {
				// A voluntary release may omit the lease end_reason, but every
				// blocked WorkItem must carry a bounded explanation. Keep the
				// lease field optional while satisfying the stronger WorkItem
				// state invariant with a stable, non-content-bearing reason.
				blockedReason = "Execution lease released."
			}
			item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "blocked", "lease_released", blockedReason
		}
	case "lease.revoke", "lease.owner_died":
		reason := cmd.Reason
		if reason == "" {
			reason = "lease_revoked"
		}
		state, err = fenceRelease(state, fenceToken{Holder: state.Holder, Fence: cmd.Fence}, now.Time(), reason, fenceEndPolicy{Lifecycle: fenceRevoked, Bump: true})
		if errors.Is(err, ErrFenceExhausted) {
			state = terminalAtFenceExhaustion(state, now.Time(), fenceRevoked, reason)
			rememberFenceExhaustion(cmd)
		} else if err != nil {
			return nil, "", mapWorkFenceError(err)
		}
		applyWorkLeaseFenceState(lease, state, lease.String(colLeaseHolderSID), lease.String(colLeaseHolderRunRef), lease.String(colLeaseHolderAgentRef))
		lease, err = repo.Update(ctx, lease)
		if err == nil && item.String(colWorkStatus) == "active" {
			code := "lease_revoked"
			if cmd.Command == "lease.owner_died" {
				code = "owner_session_died"
			}
			item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "blocked", code, reason
		}
	case "lease.expire":
		var changed bool
		state, changed, err = materializeExpiry(state, now.Time(), "lease_expired", true)
		if !changed {
			return nil, "", broken(http.StatusConflict, "stale_fence")
		}
		if errors.Is(err, ErrFenceExhausted) {
			rememberFenceExhaustion(cmd)
		} else if err != nil {
			return nil, "", err
		}
		applyWorkLeaseFenceState(lease, state, lease.String(colLeaseHolderSID), lease.String(colLeaseHolderRunRef), lease.String(colLeaseHolderAgentRef))
		lease, err = repo.Update(ctx, lease)
		if err == nil && item.String(colWorkStatus) == "active" {
			item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "blocked", "lease_expired", "lease_expired"
		}
	}
	if err != nil {
		return nil, "", err
	}
	if observedExpiry != nil {
		// One command observed two durable domain facts: the lapsed generation
		// ended and a successor generation acquired authority. Reserve two
		// consecutive aggregate sequences while retaining one WorkItem OCC write.
		item[colWorkLastEventSeq] = item.Int(colWorkLastEventSeq) + 1
	}
	item, err = updateWorkItemWithEvent(ctx, sc, item)
	if err == nil && observedExpiry != nil && materializedEnd != nil {
		*materializedEnd = *observedExpiry
	}
	return item, recordID(lease), err
}

func (m *Module) endExecutionLease(ctx context.Context, sc store.Scope, cmd WorkCommand, item model.Record, now model.Timestamp, lifecycle fenceLifecycle, reason string) error {
	lease, found, err := findWorkLease(ctx, sc, recordID(item))
	if err != nil || !found {
		return err
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return err
	}
	state, err = fenceRelease(state, fenceToken{Holder: workLeaseHolderKey(cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef), Fence: cmd.Fence}, now.Time(), reason, fenceEndPolicy{Lifecycle: lifecycle, Bump: true, RequireLive: true})
	if errors.Is(err, ErrFenceExhausted) {
		state = terminalAtFenceExhaustion(state, now.Time(), lifecycle, reason)
		rememberFenceExhaustion(cmd)
	} else if err != nil {
		return mapWorkFenceError(err)
	}
	applyWorkLeaseFenceState(lease, state, cmd.HolderSID, cmd.HolderRunRef, cmd.HolderAgentRef)
	repo, err := sc.Ext(workLeaseKind)
	if err != nil {
		return err
	}
	_, err = repo.Update(ctx, lease)
	return err
}

func (m *Module) revokeLiveLeaseForAdmin(ctx context.Context, sc store.Scope, cmd WorkCommand, item model.Record, now model.Timestamp, reason string) error {
	lease, found, err := findWorkLease(ctx, sc, recordID(item))
	if err != nil || !found {
		return err
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return err
	}
	if state.Lifecycle != fenceActive {
		return nil
	}
	if !fenceIsLive(state, now.Time()) {
		var changed bool
		state, changed, err = materializeExpiry(state, now.Time(), "lease_expired", true)
		if !changed {
			return nil
		}
	} else {
		state, err = fenceRelease(state, fenceToken{Holder: state.Holder, Fence: state.Fence}, now.Time(), reason, fenceEndPolicy{Lifecycle: fenceRevoked, Bump: true, RequireLive: true})
		if errors.Is(err, ErrFenceExhausted) {
			state = terminalAtFenceExhaustion(state, now.Time(), fenceRevoked, reason)
		}
	}
	if errors.Is(err, ErrFenceExhausted) {
		rememberFenceExhaustion(cmd)
	} else if err != nil {
		return mapWorkFenceError(err)
	}
	applyWorkLeaseFenceState(lease, state, lease.String(colLeaseHolderSID), lease.String(colLeaseHolderRunRef), lease.String(colLeaseHolderAgentRef))
	repo, err := sc.Ext(workLeaseKind)
	if err != nil {
		return err
	}
	_, err = repo.Update(ctx, lease)
	return err
}

func workLeaseFromRecord(rec model.Record, now time.Time, verdict AssessmentVerdict, code string) (WorkLease, error) {
	state, err := workLeaseFenceState(rec)
	if err != nil {
		return WorkLease{}, err
	}
	lease := WorkLease{ID: recordID(rec), WorkspaceID: model.ID(rec.String(colWorkWorkspaceID)), WorkItemID: model.ID(rec.String(colWorkItemID)), Version: rec.Int(model.ColVersion), HolderSID: rec.String(colLeaseHolderSID), HolderRunRef: rec.String(colLeaseHolderRunRef), HolderAgentRef: rec.String(colLeaseHolderAgentRef), Fence: rec.Int(colLeaseFence), State: rec.String(colLeaseState), AcquiredAt: rec.String(colLeaseAcquiredAt), RenewedAt: rec.String(colLeaseRenewedAt), ExpiresAt: rec.String(colLeaseExpiresAt), EndedAt: rec.String(colLeaseEndedAt), EndReason: rec.String(colLeaseEndReason), RenewalCount: rec.Int(colLeaseRenewalCount), LivenessVerdict: verdict, LivenessCode: code}
	if verdict == VerdictClean {
		lease.Live = fenceIsLive(state, now)
	}
	return lease, nil
}

func (m *Module) GetLease(ctx context.Context, tenant model.TenantID, _ WorkPrincipal, itemID model.ID) (WorkLease, error) {
	return m.getLeaseWithData(ctx, m.workData(tenant), itemID)
}
func (m *Module) getLeaseWithData(ctx context.Context, data workData, itemID model.ID) (WorkLease, error) {
	out, _, err := m.getLeaseAndWorkVersionWithData(ctx, data, itemID)
	return out, err
}

func (m *Module) getLeaseAndWorkVersionWithData(
	ctx context.Context,
	data workData,
	itemID model.ID,
) (WorkLease, int64, error) {
	var out WorkLease
	var workVersion int64
	err := data.View(ctx, func(sc store.Scope) error {
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := items.Get(ctx, itemID)
		if err != nil {
			return err
		}
		workVersion = item.Int(model.ColVersion)
		rec, found, err := findWorkLease(ctx, sc, itemID)
		if err != nil {
			return err
		}
		if !found {
			return store.ErrNotFound
		}
		now, err := observeLeaseClock(ctx, sc, model.ID(rec.String(colWorkWorkspaceID)))
		if we := asWorkError(err); we != nil && we.verdict == VerdictUnknown {
			out, err = workLeaseFromRecord(rec, time.Time{}, VerdictUnknown, we.code)
			return err
		}
		if err != nil {
			return err
		}
		out, err = workLeaseFromRecord(rec, now.Time(), VerdictClean, "ok")
		return err
	})
	return out, workVersion, classifyWorkStoreError(err)
}

func (m *Module) ListLeases(ctx context.Context, tenant model.TenantID, _ WorkPrincipal, q WorkLeaseQuery) (WorkLeasePage, error) {
	return m.listLeasesWithData(ctx, m.workData(tenant), q)
}
func (m *Module) listLeasesWithData(ctx context.Context, data workData, q WorkLeaseQuery) (WorkLeasePage, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 200 || !validWorkCursor(q.Cursor) {
		return WorkLeasePage{}, broken(http.StatusBadRequest, "invalid_cursor")
	}
	columns := map[string]string{"work_item_id": colWorkItemID, "holder_sid": colLeaseHolderSID, "state": colLeaseState, "expires_before": colLeaseExpiresAt}
	filters := make([]model.Filter, 0, len(q.Filters))
	for name, value := range q.Filters {
		column := columns[name]
		if column == "" {
			return WorkLeasePage{}, broken(http.StatusBadRequest, "invalid_command")
		}
		op := model.OpEq
		if name == "expires_before" {
			if _, err := model.ParseTimestamp(value); err != nil {
				return WorkLeasePage{}, broken(http.StatusBadRequest, "invalid_command")
			}
			op = model.OpLt
		}
		filters = append(filters, model.Filter{Column: column, Op: op, Value: value})
	}
	out := WorkLeasePage{Items: []WorkLease{}}
	err := data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{Filters: filters, Limit: q.Limit, Cursor: q.Cursor})
		if err != nil {
			return err
		}
		for _, rec := range rows {
			now, oerr := observeLeaseClock(ctx, sc, model.ID(rec.String(colWorkWorkspaceID)))
			verdict, code := VerdictClean, "ok"
			if we := asWorkError(oerr); we != nil {
				verdict, code, now = VerdictUnknown, we.code, model.Timestamp{}
			} else if oerr != nil {
				return oerr
			}
			lease, err := workLeaseFromRecord(rec, now.Time(), verdict, code)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, lease)
		}
		out.NextCursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	return out, classifyWorkStoreError(err)
}

func canonicalSIDID(sid string) (model.ID, error) {
	if !strings.HasPrefix(sid, sidPrefix) || len(sid) != len(sidPrefix)+36 {
		return "", fmt.Errorf("sessions: invalid canonical sid")
	}
	id, err := model.ParseID(strings.TrimPrefix(sid, sidPrefix))
	if err != nil || sid != sidPrefix+id.String() {
		return "", fmt.Errorf("sessions: invalid canonical sid")
	}
	return id, nil
}

func validCanonicalSID(sid string) bool {
	_, err := canonicalSIDID(sid)
	return err == nil
}
func internalWorkPrincipal() WorkPrincipal {
	return WorkPrincipal{ActorKind: model.ActorSystem, ActorRef: "sessions-runtime", Actor: "system:sessions-runtime", Admin: true}
}

func (m *Module) OwnerDied(ctx context.Context, tenant model.TenantID, sid, runRef, reason string) error {
	if !validCanonicalSID(sid) || runRef == "" || !boundedText(reason, 1, 512) {
		return broken(http.StatusBadRequest, "invalid_command")
	}
	type target struct {
		itemID         model.ID
		version, fence int64
		holderRunRef   string
	}
	var targets []target
	err := m.workData(tenant).View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		rows, err := listAll(ctx, repo, model.Filter{Column: colLeaseHolderSID, Op: model.OpEq, Value: sid}, model.Filter{Column: colLeaseState, Op: model.OpEq, Value: workLeaseActive})
		if err != nil {
			return err
		}
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		for _, lease := range rows {
			holderRunRef := lease.String(colLeaseHolderRunRef)
			if holderRunRef != "" && holderRunRef != runRef {
				continue
			}
			itemID := model.ID(lease.String(colWorkItemID))
			item, err := items.Get(ctx, itemID)
			if err != nil {
				return err
			}
			targets = append(targets, target{
				itemID: itemID, version: item.Int(model.ColVersion),
				fence: lease.Int(colLeaseFence), holderRunRef: holderRunRef,
			})
		}
		return nil
	})
	if err != nil {
		return classifyWorkStoreError(err)
	}
	var joined error
	for _, initial := range targets {
		t := initial
		settled := false
		for attempt := 0; attempt < 8; attempt++ {
			cmd := WorkCommand{Command: "lease.owner_died", WorkItemID: t.itemID, HolderSID: sid, HolderRunRef: t.holderRunRef, Fence: t.fence, Reason: reason, ExpectedVersion: t.version, IdempotencyKey: model.NewID().String(), internal: true}
			_, applyErr := m.Apply(ctx, tenant, internalWorkPrincipal(), cmd)
			if applyErr == nil {
				settled = true
				break
			}
			we := asWorkError(applyErr)
			if we == nil || (we.code != "stale_fence" && we.code != "version_mismatch") {
				joined = errors.Join(joined, applyErr)
				settled = true
				break
			}

			// A WorkItem update or lease renewal may race the scan without
			// changing who owns authority. Re-read and retry that same live SID;
			// only a genuinely different/non-active holder makes owner death a
			// no-op. Silently swallowing version_mismatch left dead owners live.
			stillHeld := false
			refreshErr := m.workData(tenant).View(ctx, func(sc store.Scope) error {
				lease, found, err := findWorkLease(ctx, sc, t.itemID)
				if err != nil {
					return err
				}
				if !found {
					return unknown("evidence_unavailable", nil)
				}
				holderRunRef := lease.String(colLeaseHolderRunRef)
				if lease.String(colLeaseState) != workLeaseActive ||
					lease.String(colLeaseHolderSID) != sid ||
					(holderRunRef != "" && holderRunRef != runRef) {
					return nil
				}
				items, err := sc.Ext(workItemKind)
				if err != nil {
					return err
				}
				item, err := items.Get(ctx, t.itemID)
				if err != nil {
					return err
				}
				t.version, t.fence, t.holderRunRef = item.Int(model.ColVersion), lease.Int(colLeaseFence), holderRunRef
				stillHeld = true
				return nil
			})
			if refreshErr != nil {
				joined = errors.Join(joined, classifyWorkStoreError(refreshErr))
				settled = true
				break
			}
			if !stillHeld {
				settled = true
				break
			}
		}
		if !settled {
			joined = errors.Join(joined, unknown("observation_unavailable", fmt.Errorf("owner death kept racing for work item %s", t.itemID)))
		}
	}
	return joined
}

func (m *Module) ReapWorkLeases(ctx context.Context, tenant model.TenantID, limit int) (int, error) {
	return m.reapWorkLeasesWithData(ctx, m.workData(tenant), tenant, limit)
}

func (m *Module) reapWorkLeasesWithData(
	ctx context.Context,
	data workData,
	tenant model.TenantID,
	limit int,
) (int, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	type target struct {
		itemID         model.ID
		version, fence int64
		sid, runRef    string
	}
	var targets []target
	var observationErr error
	err := data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(workLeaseKind)
		if err != nil {
			return err
		}
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		type clockObservation struct {
			now model.Timestamp
			err error
		}
		observations := make(map[string]clockObservation)
		query := model.Query{
			Filters: []model.Filter{{Column: colLeaseState, Op: model.OpEq, Value: workLeaseActive}},
			Limit:   200,
		}
		for {
			rows, page, err := repo.List(ctx, query)
			if err != nil {
				return err
			}
			for _, lease := range rows {
				workspace := model.ID(lease.String(colWorkWorkspaceID))
				observed, ok := observations[workspace.String()]
				if !ok {
					observed.now, observed.err = observeLeaseClock(ctx, sc, workspace)
					observations[workspace.String()] = observed
					if observed.err != nil {
						// Rollback is workspace-scoped. Record it once, then keep
						// scanning so a full bad page cannot starve healthy leases.
						observationErr = errors.Join(observationErr, observed.err)
					}
				}
				if observed.err != nil {
					continue
				}
				expires, err := model.ParseTimestamp(lease.String(colLeaseExpiresAt))
				if err != nil {
					return unknown("evidence_unavailable", err)
				}
				if observed.now.Before(expires) {
					continue
				}
				itemID := model.ID(lease.String(colWorkItemID))
				item, err := items.Get(ctx, itemID)
				if err != nil {
					return err
				}
				targets = append(targets, target{itemID, item.Int(model.ColVersion), lease.Int(colLeaseFence), lease.String(colLeaseHolderSID), lease.String(colLeaseHolderRunRef)})
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			query.Cursor = page.Cursor
		}
		return nil
	})
	if err != nil {
		return 0, classifyWorkStoreError(err)
	}
	reaped := 0
	joined := observationErr
	for _, initial := range targets {
		if reaped == limit {
			break
		}
		t := initial
		settled := false
		for attempt := 0; attempt < 8; attempt++ {
			cmd := WorkCommand{Command: "lease.expire", WorkItemID: t.itemID, HolderSID: t.sid, HolderRunRef: t.runRef, Fence: t.fence, Reason: "lease_expired", ExpectedVersion: t.version, IdempotencyKey: model.NewID().String(), internal: true}
			_, applyErr := m.applyWithData(ctx, data, tenant, internalWorkPrincipal(), cmd)
			if applyErr == nil {
				reaped++
				settled = true
				break
			}
			we := asWorkError(applyErr)
			if we == nil || (we.code != "stale_fence" && we.code != "version_mismatch") {
				joined = errors.Join(joined, applyErr)
				settled = true
				break
			}

			// The scan is only a candidate list. If a writer changed the item or
			// lease, re-observe the current generation: expire it when it is still
			// due, skip it when a renewal made it live, and never spend the batch
			// slot in a way that starves later candidates.
			stillDue := false
			refreshErr := data.View(ctx, func(sc store.Scope) error {
				lease, found, err := findWorkLease(ctx, sc, t.itemID)
				if err != nil {
					return err
				}
				if !found {
					return unknown("evidence_unavailable", nil)
				}
				if lease.String(colLeaseState) != workLeaseActive {
					return nil
				}
				now, err := observeLeaseClock(ctx, sc, model.ID(lease.String(colWorkWorkspaceID)))
				if err != nil {
					return err
				}
				expires, err := model.ParseTimestamp(lease.String(colLeaseExpiresAt))
				if err != nil {
					return unknown("evidence_unavailable", err)
				}
				if now.Before(expires) {
					return nil
				}
				items, err := sc.Ext(workItemKind)
				if err != nil {
					return err
				}
				item, err := items.Get(ctx, t.itemID)
				if err != nil {
					return err
				}
				t.version, t.fence = item.Int(model.ColVersion), lease.Int(colLeaseFence)
				t.sid, t.runRef = lease.String(colLeaseHolderSID), lease.String(colLeaseHolderRunRef)
				stillDue = true
				return nil
			})
			if refreshErr != nil {
				joined = errors.Join(joined, classifyWorkStoreError(refreshErr))
				settled = true
				break
			}
			if !stillDue {
				settled = true
				break
			}
		}
		if !settled {
			joined = errors.Join(joined, unknown("observation_unavailable", fmt.Errorf("lease reaper kept racing for work item %s", t.itemID)))
		}
	}
	return reaped, joined
}
