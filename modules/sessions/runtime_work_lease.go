// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type runWorkStamp struct {
	itemID      model.ID
	fence       int64
	dispatchKey []byte
	ownerEpoch  int64
}

// runtimeWorkGeneration is the complete authority generation captured before a
// runtime effect crosses the process boundary. Fence alone is not unique: it
// restarts per WorkItem and the run may already point at a successor when an
// ambiguous result is recorded.
type runtimeWorkGeneration struct {
	itemID    model.ID
	holderSID string
	fence     int64
}

func runtimeWorkGenerationFromLease(lease WorkLease) runtimeWorkGeneration {
	return runtimeWorkGeneration{
		itemID: lease.WorkItemID, holderSID: lease.HolderSID, fence: lease.Fence,
	}
}

type lockedRunWork struct {
	run       model.Record
	item      model.Record
	lease     model.Record
	stamp     runWorkStamp
	now       model.Timestamp
	workspace model.ID
}

// assertRunWorkLease is the pre-effect half of fenced runtime control. It checks
// both authorities: the supervised process still operates under its live session
// claim, and the run's durable work stamp still names the exact live WorkLease.
func (m *Module) assertRunWorkLease(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
) (WorkLease, error) {
	claim, err := m.authorizedLiveRunClaim(ctx, tenant, runRef)
	if err != nil {
		return WorkLease{}, err
	}
	var out WorkLease
	err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		evidence, err := loadLockedRunWork(ctx, sc, tenant, runRef, presentedFence, false)
		if err != nil {
			return err
		}
		if err := assertActiveRunWork(evidence, runRef, claim.SID, presentedFence); err != nil {
			return err
		}
		out, err = workLeaseFromRecord(evidence.lease, evidence.now.Time(), VerdictClean, "ok")
		return err
	})
	return out, classifyWorkStoreError(err)
}

// settleRunWorkAction is the post-effect half. Success is recorded only under
// the generation that can prove its required postcondition. Ambiguous evidence
// deliberately asks less: authority may already have moved, which is the
// uncertainty this event exists to retain.
//
// It asks less of the STAMP too, and that is a correction. This comment used to
// say the stamp "must be exact", which held only because the stamp was write-once
// in practice: bindRunToWorkLease demanded fence equality, so no run could ever
// be re-bound. Now that a later generation of the same item can re-adopt a run
// (B3), an exact-stamp requirement would mean an effect that crossed the process
// boundary under generation N leaves NO durable trace once somebody re-acquires —
// the UNKNOWN silently downgraded to nothing, which is precisely what these
// events exist to prevent, arriving by a new door. Measured, not hypothesized.
//
// The relaxation is scoped to the two ambiguous events and to a stamp that moved
// FORWARD. It is also belt-and-braces rather than load-bearing for the confirmed
// outcomes: assertActiveRunWork and assertStoppedRunWork re-check the fence
// against the LEASE independently of the stamp, so a confirmed settlement about
// a superseded generation still refuses even if this flag were wrong.
func (m *Module) settleRunWorkAction(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	generation runtimeWorkGeneration,
	event string,
) error {
	if generation.itemID.IsZero() || !validCanonicalSID(generation.holderSID) || generation.fence < 1 {
		return broken(http.StatusBadRequest, "invalid_command")
	}
	presentedFence := generation.fence
	var expectedSID string
	if event == workInputAccepted {
		claim, err := m.authorizedLiveRunClaim(ctx, tenant, runRef)
		if err != nil {
			return err
		}
		expectedSID = claim.SID
	} else if event == workStopConfirmed {
		claim, err := m.liveRunClaim(tenant, runRef)
		if err != nil {
			return err
		}
		expectedSID = claim.SID
	} else if event != workInputAmbiguous && event != workStopAmbiguous {
		return broken(http.StatusBadRequest, "invalid_command")
	}

	ambiguous := event == workInputAmbiguous || event == workStopAmbiguous
	attempt := func() error {
		return m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
			evidence, err := loadLockedRunWork(ctx, sc, tenant, runRef, presentedFence, ambiguous)
			if err != nil {
				return err
			}
			if evidence.stamp.itemID != generation.itemID ||
				(!ambiguous && expectedSID != generation.holderSID) {
				return broken(http.StatusConflict, "dispatch_conflict")
			}
			switch event {
			case workInputAccepted:
				err = assertActiveRunWork(evidence, runRef, expectedSID, presentedFence)
			case workStopConfirmed:
				err = assertStoppedRunWork(ctx, sc, evidence, runRef, expectedSID, presentedFence)
			case workInputAmbiguous, workStopAmbiguous:
				// loadLockedRunWork has proved a complete, self-consistent stamp for
				// THIS item; it may name a later generation than the one being
				// settled, and that is the point.
			}
			if err != nil {
				return err
			}
			return appendRuntimeWorkEvent(ctx, sc, evidence.run, event, generation, evidence.now)
		})
	}
	err := attempt()
	if errors.Is(err, store.ErrConflict) {
		err = attempt()
	}
	return classifyWorkStoreError(err)
}

// verifyStoppedWorkLease proves that the death callback, rather than a generic
// terminal-looking run row, revoked this exact generation. It intentionally does
// not call Authority: finalize releases the session claim before OwnerDied to
// prevent a late callback from revoking a resumed successor.
func (m *Module) verifyStoppedWorkLease(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
) error {
	claim, err := m.liveRunClaim(tenant, runRef)
	if err != nil {
		return err
	}
	err = m.workData(tenant).Mutate(ctx, func(sc store.Scope) error {
		evidence, err := loadLockedRunWork(ctx, sc, tenant, runRef, presentedFence, false)
		if err != nil {
			return err
		}
		return assertStoppedRunWork(ctx, sc, evidence, runRef, claim.SID, presentedFence)
	})
	return classifyWorkStoreError(err)
}

// authorizedLiveRunClaim gives Authority its production caller. Authority is a
// read-time admission check; WorkLease enforcement still occurs under the
// transaction-scoped coordination lock in the helpers above.
func (m *Module) authorizedLiveRunClaim(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
) (Lease, error) {
	claim, err := m.liveRunClaim(tenant, runRef)
	if err != nil {
		return Lease{}, err
	}
	if m.data == nil {
		return Lease{}, unknown("observation_unavailable", store.ErrStoreUnavailable)
	}
	if err := m.Authority(ctx, tenant, claim.SID, claim.Holder, claim.Fence); err != nil {
		switch {
		case errors.Is(err, ErrNoClaim), errors.Is(err, ErrNoHolder),
			errors.Is(err, ErrLeaseLost), errors.Is(err, ErrClaimHeld),
			errors.Is(err, ErrFenceExhausted):
			return Lease{}, broken(http.StatusConflict, "stale_fence")
		default:
			classified := classifyWorkStoreError(err)
			if asWorkError(classified) != nil {
				return Lease{}, classified
			}
			return Lease{}, unknown("observation_unavailable", err)
		}
	}
	return claim, nil
}

func (m *Module) liveRunClaim(tenant model.TenantID, runRef string) (Lease, error) {
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		return Lease{}, broken(http.StatusConflict, "stale_fence")
	}
	claim := lr.claim
	if claim.SID == "" || claim.Holder == "" || claim.Fence < 1 {
		return Lease{}, broken(http.StatusConflict, "stale_fence")
	}
	return claim, nil
}

// loadLockedRunWork resolves the immutable item key, acquires the established
// transaction-scoped cluster lock, then re-reads every fact under that lock.
// The first read is only lock routing; it never authorizes an effect.
// supersededOK relaxes ONE comparison, and only the ambiguous-settlement path
// passes it: the run's stamp may already name a LATER generation of the same
// item. See settleRunWorkAction for why that is the difference between keeping
// an UNKNOWN and losing it.
func loadLockedRunWork(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	runRef string,
	presentedFence int64,
	supersededOK bool,
) (lockedRunWork, error) {
	runs, err := sc.Ext(runKind)
	if err != nil {
		return lockedRunWork{}, err
	}
	routeRun, err := findRunRec(ctx, runs, runRef)
	if err != nil {
		return lockedRunWork{}, err
	}
	routeStamp, err := parseRunWorkStamp(routeRun, presentedFence, supersededOK)
	if err != nil {
		return lockedRunWork{}, err
	}
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return lockedRunWork{}, err
	}
	routeItem, err := items.Get(ctx, routeStamp.itemID)
	if err != nil {
		return lockedRunWork{}, err
	}
	workspace, err := model.ParseID(routeItem.String(colWorkWorkspaceID))
	if err != nil {
		return lockedRunWork{}, unknown("evidence_unavailable", err)
	}
	if err := lockWorkLeaseItem(ctx, sc, tenant, workspace, routeStamp.itemID); err != nil {
		return lockedRunWork{}, err
	}

	run, err := findRunRec(ctx, runs, runRef)
	if err != nil {
		return lockedRunWork{}, err
	}
	stamp, err := parseRunWorkStamp(run, presentedFence, supersededOK)
	if err != nil {
		return lockedRunWork{}, err
	}
	if stamp.itemID != routeStamp.itemID {
		return lockedRunWork{}, broken(http.StatusConflict, "dispatch_conflict")
	}
	item, err := items.Get(ctx, stamp.itemID)
	if err != nil {
		return lockedRunWork{}, err
	}
	if item.String(colWorkWorkspaceID) != workspace.String() {
		return lockedRunWork{}, broken(http.StatusConflict, "dispatch_conflict")
	}
	lease, found, err := findWorkLease(ctx, sc, stamp.itemID)
	if err != nil {
		return lockedRunWork{}, err
	}
	if !found || lease.String(colWorkWorkspaceID) != workspace.String() {
		return lockedRunWork{}, unknown("evidence_unavailable", nil)
	}
	now, err := observeLeaseClock(ctx, sc, workspace)
	if err != nil {
		return lockedRunWork{}, err
	}
	return lockedRunWork{
		run: run, item: item, lease: lease, stamp: stamp, now: now, workspace: workspace,
	}, nil
}

func parseRunWorkStamp(rec model.Record, presentedFence int64, supersededOK bool) (runWorkStamp, error) {
	nulls := 0
	for _, column := range []string{
		colRunWorkItemID, colRunWorkLeaseFence, colRunWorkDispatchKey, colRunWorkOwnerEpoch,
	} {
		if rec.IsNull(column) {
			nulls++
		}
	}
	if nulls == 4 {
		return runWorkStamp{}, broken(http.StatusConflict, "stale_fence")
	}
	if nulls != 0 {
		return runWorkStamp{}, unknown("evidence_unavailable", nil)
	}
	itemID, err := model.ParseID(rec.String(colRunWorkItemID))
	if err != nil {
		return runWorkStamp{}, unknown("evidence_unavailable", err)
	}
	stamp := runWorkStamp{
		itemID: itemID, fence: rec.Int(colRunWorkLeaseFence),
		dispatchKey: append([]byte(nil), rec.Bytes(colRunWorkDispatchKey)...),
		ownerEpoch:  rec.Int(colRunWorkOwnerEpoch),
	}
	if stamp.fence < 1 || stamp.ownerEpoch < 1 || len(stamp.dispatchKey) != sha256.Size {
		return runWorkStamp{}, unknown("evidence_unavailable", nil)
	}
	if stamp.fence != presentedFence && !(supersededOK && stamp.fence > presentedFence) {
		return runWorkStamp{}, broken(http.StatusConflict, "stale_fence")
	}
	expected := runtimeWorkDispatchKey(stamp.itemID, stamp.ownerEpoch, stamp.fence)
	if !bytesEqual(stamp.dispatchKey, expected[:]) {
		return runWorkStamp{}, broken(http.StatusConflict, "dispatch_conflict")
	}
	return stamp, nil
}

func runtimeWorkDispatchKey(itemID model.ID, ownerEpoch, fence int64) [sha256.Size]byte {
	return runtimeWorkDispatchKeyFor(itemID, ownerEpoch, fence, WorkLaunchAttemptLeaseBind)
}

func runtimeWorkDispatchKeyFor(
	itemID model.ID,
	ownerEpoch, fence int64,
	attemptKind string,
) [sha256.Size]byte {
	return sha256.Sum256([]byte(itemID.String() + "|" +
		strconv.FormatInt(ownerEpoch, 10) + "|" + strconv.FormatInt(fence, 10) + "|" + attemptKind))
}

func assertActiveRunWork(e lockedRunWork, runRef, sid string, presentedFence int64) error {
	state, err := workLeaseFenceState(e.lease)
	if err != nil {
		return err
	}
	if e.item.Int(colWorkOwnerEpoch) != e.stamp.ownerEpoch ||
		e.lease.String(colWorkItemID) != e.stamp.itemID.String() ||
		e.lease.String(colLeaseHolderSID) != sid ||
		e.lease.String(colLeaseHolderRunRef) != runRef ||
		state.Lifecycle != fenceActive || state.Fence != presentedFence ||
		!fenceIsLive(state, e.now.Time()) {
		return broken(http.StatusConflict, "stale_fence")
	}
	return nil
}

func assertStoppedRunWork(
	ctx context.Context,
	sc store.Scope,
	e lockedRunWork,
	runRef string,
	sid string,
	presentedFence int64,
) error {
	state, err := workLeaseFenceState(e.lease)
	if err != nil {
		return err
	}
	next, err := nextFence(presentedFence)
	if err != nil {
		return err
	}
	if e.run.String(colState) != stateStopped ||
		e.item.Int(colWorkOwnerEpoch) != e.stamp.ownerEpoch ||
		e.lease.String(colWorkItemID) != e.stamp.itemID.String() ||
		e.lease.String(colLeaseHolderSID) != sid ||
		e.lease.String(colLeaseHolderRunRef) != runRef ||
		state.Lifecycle != fenceRevoked || state.Fence != next {
		return broken(http.StatusConflict, "stale_fence")
	}
	return assertOwnerDiedWorkEvent(ctx, sc, e.stamp.itemID, runRef, next)
}

func assertOwnerDiedWorkEvent(
	ctx context.Context,
	sc store.Scope,
	itemID model.ID,
	runRef string,
	revokedFence int64,
) error {
	repo, err := sc.Ext(workEventKind)
	if err != nil {
		return err
	}
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colEventAggregateKind, Op: model.OpEq, Value: string(workItemKind)},
			{Column: colEventAggregateID, Op: model.OpEq, Value: itemID.String()},
			{Column: colEventType, Op: model.OpEq, Value: "work.lease.ended"},
		},
		Sort: []model.Sort{{Column: colEventSeq, Desc: true}}, Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return unknown("evidence_unavailable", nil)
	}
	var payload struct {
		Command      string `json:"command"`
		HolderRunRef string `json:"holder_run_ref"`
		Fence        int64  `json:"fence"`
		LeaseState   string `json:"lease_state"`
	}
	if err := json.Unmarshal([]byte(rows[0].String(colEventPayload)), &payload); err != nil {
		return unknown("evidence_unavailable", err)
	}
	if payload.Command != "lease.owner_died" || payload.HolderRunRef != runRef ||
		payload.Fence != revokedFence || payload.LeaseState != workLeaseRevoked {
		return broken(http.StatusConflict, "stale_fence")
	}
	return nil
}

func appendRuntimeWorkEvent(
	ctx context.Context,
	sc store.Scope,
	run model.Record,
	event string,
	generation runtimeWorkGeneration,
	now model.Timestamp,
) error {
	runID, err := model.ParseID(run.String(model.ColID))
	if err != nil {
		return unknown("evidence_unavailable", err)
	}
	state := run.String(colState)
	seq, err := appendRunEvent(ctx, sc, runEventInput{
		runID: runID, runRef: run.String(colRunRef), event: event,
		fromState: state, toState: state,
		actor: model.ActorSystem, actorKind: model.ActorSystem, at: now.Time(),
		workGeneration: &generation,
	})
	if err != nil {
		return err
	}
	run[colLastEventSeq] = seq
	runs, err := sc.Ext(runKind)
	if err != nil {
		return err
	}
	_, err = runs.Update(ctx, run)
	return err
}
