// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// WorkLaunchAttemptLeaseBind is the first K4 launch-attempt vocabulary. It is
// intentionally the value K2 already committed into runtimeWorkDispatchKey, so
// work-bound rows created before and after K4 share one verifiable key format.
const WorkLaunchAttemptLeaseBind = "lease-bind"

// WorkLaunchSpec is the private in-process request to turn one WorkItem
// generation into a supervised session. Runtime contains the ordinary launch
// choices; work authority is always re-read from the durable WorkItem/WorkLease.
//
// AuditActorRef is required. OwnerEpoch, WorkLeaseFence, AttemptKind, and
// DispatchKey may be omitted; the service fills them from the observed
// claimable generation. Supplying them turns each value into an exact
// precondition, which is what orchestration uses on durable replay.
type WorkLaunchSpec struct {
	WorkItemID      model.ID
	OwnerEpoch      int64
	WorkLeaseFence  int64
	AttemptKind     string
	DispatchKey     [sha256.Size]byte
	LeaseTTLSeconds int64
	// AuditActorRef is the canonical authenticated actor reference. Runtime.Actor
	// remains the display/audit principal (for example token:<credential>), while
	// the executor and Claim holder are derived exclusively from the WorkItem's
	// durable agent owner.
	AuditActorRef string
	Runtime       CreateRunParams
}

// ManagedRunRef is the references-only result consumed by orchestration. It
// deliberately exposes neither process handles nor launch credentials.
type ManagedRunRef struct {
	RunRef         string
	SessionID      string
	WorkItemID     model.ID
	WorkspaceID    model.ID
	WorkLeaseFence int64
	OwnerEpoch     int64
	DispatchKey    string
	State          string
	Replayed       bool
}

// RuntimeControl is the narrow process-control port used by K4 orchestration.
// It does not expose the HTTP handler or the runtime's in-memory Process.
type RuntimeControl interface {
	LaunchForWork(context.Context, model.TenantID, WorkLaunchSpec) (ManagedRunRef, error)
	InputForWork(context.Context, model.TenantID, string, int64, []byte) error
	StopForWork(context.Context, model.TenantID, string, int64, string) error
}

var _ RuntimeControl = (*Module)(nil)

type workLaunchReservation struct {
	itemID        model.ID
	workspaceID   model.ID
	ownerEpoch    int64
	leaseFence    int64
	attemptKind   string
	dispatchKey   [sha256.Size]byte
	specHash      [sha256.Size]byte
	itemVersion   int64
	ttlSeconds    int64
	auditActorRef string
	ownerRef      string
	claimHolder   string
}

type workLaunchCall struct {
	spec        WorkLaunchSpec
	reservation workLaunchReservation
	replayed    bool
}

// WorkLaunchDispatchKey returns the tenant-local deterministic dispatch key.
// Tenant is excluded because the unique index and every lookup are already
// tenant-scoped; the same WorkItem-shaped fixture in another tenant must not
// become a global lock.
func WorkLaunchDispatchKey(
	itemID model.ID,
	ownerEpoch, leaseFence int64,
	attemptKind string,
) [sha256.Size]byte {
	return runtimeWorkDispatchKeyFor(itemID, ownerEpoch, leaseFence, attemptKind)
}

// LaunchForWork reserves a complete work-bound pending row, acquires and binds
// its WorkLease, and only then lets the shared runtime path spawn the process.
// An exact dispatch replay returns the durable row and never calls Runner again.
func (m *Module) LaunchForWork(
	ctx context.Context,
	tenant model.TenantID,
	spec WorkLaunchSpec,
) (ManagedRunRef, error) {
	dto, reservation, replayed, err := m.createRunForWork(ctx, tenant, spec)
	if err != nil {
		return ManagedRunRef{}, err
	}
	rec, err := m.loadRun(ctx, tenant, dto.RunRef)
	if err != nil {
		return ManagedRunRef{}, err
	}
	result, err := managedRunRef(rec, reservation)
	if err != nil {
		return ManagedRunRef{}, err
	}
	result.State = dto.State
	result.Replayed = replayed
	return result, nil
}

func (m *Module) createRunForWork(
	ctx context.Context,
	tenant model.TenantID,
	spec WorkLaunchSpec,
) (runDTO, workLaunchReservation, bool, error) {
	call := &workLaunchCall{spec: spec}
	dto, err := m.createRunInternal(ctx, tenant, spec.Runtime, call)
	return dto, call.reservation, call.replayed, err
}

func managedRunRef(rec model.Record, reservation workLaunchReservation) (ManagedRunRef, error) {
	stamp, err := parseRunWorkStamp(rec, reservation.leaseFence, false)
	if err != nil {
		return ManagedRunRef{}, err
	}
	if stamp.itemID != reservation.itemID || stamp.ownerEpoch != reservation.ownerEpoch ||
		!bytesEqual(stamp.dispatchKey, reservation.dispatchKey[:]) ||
		!bytesEqual(rec.Bytes(colRunWorkLaunchSpecHash), reservation.specHash[:]) {
		return ManagedRunRef{}, broken(http.StatusConflict, "dispatch_conflict")
	}
	sid := rec.String(colRunClaimSID)
	if !validCanonicalSID(sid) {
		return ManagedRunRef{}, unknown("evidence_unavailable", nil)
	}
	return ManagedRunRef{
		RunRef: rec.String(colRunRef), SessionID: sid,
		WorkItemID: reservation.itemID, WorkspaceID: reservation.workspaceID,
		WorkLeaseFence: reservation.leaseFence, OwnerEpoch: reservation.ownerEpoch,
		DispatchKey: hex.EncodeToString(reservation.dispatchKey[:]),
		State:       rec.String(colState),
	}, nil
}

func (m *Module) prepareWorkLaunch(
	ctx context.Context,
	tenant model.TenantID,
	spec WorkLaunchSpec,
	params *CreateRunParams,
) (workLaunchReservation, model.Record, bool, error) {
	if params == nil {
		return workLaunchReservation{}, nil, false, broken(http.StatusBadRequest, "invalid_command")
	}
	if tenant.IsZero() || tenant.IsSystem() || spec.WorkItemID.IsZero() ||
		spec.OwnerEpoch < 0 || spec.WorkLeaseFence < 0 || spec.LeaseTTLSeconds < 0 {
		return workLaunchReservation{}, nil, false, broken(http.StatusBadRequest, "invalid_command")
	}
	if !boundedText(params.Actor, 1, 512) ||
		!validWorkLaunchAuditActor(params.ActorKind, spec.AuditActorRef) {
		return workLaunchReservation{}, nil, false, broken(http.StatusBadRequest, "invalid_command")
	}
	attemptKind := spec.AttemptKind
	if attemptKind == "" {
		attemptKind = WorkLaunchAttemptLeaseBind
	}
	// K2 validators can prove the existing lease-bind format. A later K4 tranche
	// may add another persisted attempt vocabulary; accepting one before the row
	// can prove it would make fenced input fail after launch.
	if attemptKind != WorkLaunchAttemptLeaseBind {
		return workLaunchReservation{}, nil, false, broken(http.StatusBadRequest, "invalid_command")
	}

	var item, lease model.Record
	var executorAgentRef string
	var now model.Timestamp
	err := m.workData(tenant).View(ctx, func(sc store.Scope) error {
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err = items.Get(ctx, spec.WorkItemID)
		if err != nil {
			return err
		}
		var found bool
		lease, found, err = findWorkLease(ctx, sc, spec.WorkItemID)
		if err != nil {
			return err
		}
		if !found {
			return unknown("evidence_unavailable", nil)
		}
		if item.String(colWorkOwnerKind) != "agent" {
			return broken(http.StatusUnprocessableEntity, "owner_ineligible")
		}
		ownerID, err := model.ParseID(item.String(colWorkOwnerRef))
		if err != nil || ownerID.IsZero() {
			return unknown("evidence_unavailable", err)
		}
		identity, err := sc.Identities().Get(ctx, ownerID)
		if err != nil {
			return err
		}
		executorAgentRef = identity.ExternalID
		if !boundedText(executorAgentRef, 1, 512) {
			return unknown("evidence_unavailable", nil)
		}
		now, err = observeLeaseClock(ctx, sc, model.ID(item.String(colWorkWorkspaceID)))
		return err
	})
	if err != nil {
		return workLaunchReservation{}, nil, false, classifyWorkStoreError(err)
	}
	workspaceID, err := model.ParseID(item.String(colWorkWorkspaceID))
	if err != nil {
		return workLaunchReservation{}, nil, false, unknown("evidence_unavailable", err)
	}
	if err := m.checkParticipant(
		ctx, tenant, workspaceID, "agent", item.String(colWorkOwnerRef),
	); err != nil {
		return workLaunchReservation{}, nil, false, err
	}
	// AgentRef is server-set runtime attribution. Ignore any value carried by the
	// caller and bind the new SID to the current durable owner identity instead.
	params.AgentRef = executorAgentRef
	storedOwnerEpoch := item.Int(colWorkOwnerEpoch)
	ownerEpoch := spec.OwnerEpoch
	if ownerEpoch == 0 {
		ownerEpoch = storedOwnerEpoch
	}
	if ownerEpoch < 1 || storedOwnerEpoch < 1 {
		return workLaunchReservation{}, nil, false, unknown("evidence_unavailable", nil)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return workLaunchReservation{}, nil, false, err
	}
	leaseFence := spec.WorkLeaseFence
	if leaseFence == 0 {
		if fenceIsLive(state, now.Time()) && lease.String(colLeaseHolderRunRef) != "" {
			leaseFence = state.Fence
		} else {
			leaseFence, err = nextWorkLaunchFence(state, now.Time())
			if err != nil {
				return workLaunchReservation{}, nil, false, err
			}
		}
	}
	dispatchKey := WorkLaunchDispatchKey(spec.WorkItemID, ownerEpoch, leaseFence, attemptKind)
	if spec.DispatchKey != ([sha256.Size]byte{}) && spec.DispatchKey != dispatchKey {
		return workLaunchReservation{}, nil, false, broken(http.StatusConflict, "dispatch_conflict")
	}

	semantic := struct {
		WorkItemID     model.ID        `json:"work_item_id"`
		WorkspaceID    model.ID        `json:"workspace_id"`
		OwnerEpoch     int64           `json:"owner_epoch"`
		WorkLeaseFence int64           `json:"work_lease_fence"`
		AttemptKind    string          `json:"attempt_kind"`
		LeaseTTL       int64           `json:"lease_ttl_seconds"`
		AuditActorRef  string          `json:"audit_actor_ref"`
		Runtime        CreateRunParams `json:"runtime"`
	}{
		WorkItemID: spec.WorkItemID, WorkspaceID: workspaceID,
		OwnerEpoch: ownerEpoch, WorkLeaseFence: leaseFence,
		AttemptKind: attemptKind, LeaseTTL: spec.LeaseTTLSeconds,
		AuditActorRef: spec.AuditActorRef, Runtime: *params,
	}
	encoded, err := canonicalJSON(semantic)
	if err != nil {
		return workLaunchReservation{}, nil, false, unknown("evidence_unavailable", err)
	}
	reservation := workLaunchReservation{
		itemID: spec.WorkItemID, workspaceID: workspaceID,
		ownerEpoch: ownerEpoch, leaseFence: leaseFence, attemptKind: attemptKind,
		dispatchKey: dispatchKey, specHash: sha256.Sum256(encoded),
		itemVersion: item.Int(model.ColVersion), ttlSeconds: spec.LeaseTTLSeconds,
		auditActorRef: spec.AuditActorRef,
		ownerRef:      item.String(colWorkOwnerRef),
		claimHolder:   executorAgentRef,
	}
	if existing, found, err := m.findWorkLaunchByDispatch(ctx, tenant, dispatchKey); err != nil {
		return workLaunchReservation{}, nil, false, err
	} else if found {
		if err := validateWorkLaunchReplay(existing, reservation); err != nil {
			return workLaunchReservation{}, nil, false, err
		}
		return reservation, existing, true, nil
	}
	if ownerEpoch != storedOwnerEpoch {
		return workLaunchReservation{}, nil, false, broken(http.StatusConflict, "dispatch_conflict")
	}
	if fenceIsLive(state, now.Time()) {
		return workLaunchReservation{}, nil, false, broken(http.StatusConflict, "lease_held")
	}
	predicted, err := nextWorkLaunchFence(state, now.Time())
	if err != nil {
		return workLaunchReservation{}, nil, false, err
	}
	if predicted != leaseFence {
		return workLaunchReservation{}, nil, false, broken(http.StatusConflict, "stale_fence")
	}
	return reservation, nil, false, nil
}

func validWorkLaunchAuditActor(kind, ref string) bool {
	if !boundedText(ref, 1, 512) || ref != strings.TrimSpace(ref) {
		return false
	}
	switch kind {
	case model.ActorUser:
		id, err := model.ParseID(ref)
		return err == nil && !id.IsZero()
	case model.ActorAgent, model.ActorSystem:
		return true
	default:
		return false
	}
}

func nextWorkLaunchFence(state fenceState, now time.Time) (int64, error) {
	if fenceIsLive(state, now) {
		return 0, broken(http.StatusConflict, "lease_held")
	}
	current := state.Fence
	if state.Lifecycle == fenceActive {
		var err error
		current, err = nextFence(current) // materializeExpiry invalidates first.
		if err != nil {
			return 0, mapWorkFenceError(err)
		}
	}
	next, err := nextFence(current)
	if err != nil {
		return 0, mapWorkFenceError(err)
	}
	return next, nil
}

func (m *Module) findWorkLaunchByDispatch(
	ctx context.Context,
	tenant model.TenantID,
	key [sha256.Size]byte,
) (model.Record, bool, error) {
	if m.data == nil {
		return nil, false, &runErr{http.StatusServiceUnavailable, "session runtime store is not available"}
	}
	var out model.Record
	found := false
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colRunWorkDispatchKey, Op: model.OpEq, Value: key[:],
		}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) > 1 {
			return unknown("evidence_unavailable", errors.New("duplicate runtime dispatch key"))
		}
		if len(rows) == 1 {
			out, found = rows[0], true
		}
		return nil
	})
	return out, found, err
}

func validateWorkLaunchReplay(rec model.Record, reservation workLaunchReservation) error {
	stamp, err := parseRunWorkStamp(rec, reservation.leaseFence, false)
	if err != nil {
		return err
	}
	if stamp.itemID != reservation.itemID || stamp.ownerEpoch != reservation.ownerEpoch ||
		!bytesEqual(stamp.dispatchKey, reservation.dispatchKey[:]) ||
		!bytesEqual(rec.Bytes(colRunWorkLaunchSpecHash), reservation.specHash[:]) {
		return broken(http.StatusConflict, "dispatch_conflict")
	}
	return nil
}

func (m *Module) bindWorkLaunch(
	ctx context.Context,
	tenant model.TenantID,
	params CreateRunParams,
	reservation workLaunchReservation,
	runRef string,
	claim Lease,
) error {
	principal := WorkPrincipal{
		ActorKind: params.ActorKind, ActorRef: reservation.auditActorRef, Actor: params.Actor,
		SessionID: claim.SID, SessionRunRef: runRef, SessionFence: claim.Fence,
	}
	_, err := m.Apply(ctx, tenant, principal, WorkCommand{
		Command: "lease.acquire", WorkItemID: reservation.itemID,
		WorkspaceID: reservation.workspaceID, HolderSID: claim.SID,
		HolderRunRef: runRef, HolderAgentRef: reservation.ownerRef,
		TTLSeconds:      reservation.ttlSeconds,
		ExpectedVersion: reservation.itemVersion, IdempotencyKey: runRef,
		HTTPMethod: http.MethodPost, CommandScope: "runtime.launch:" + reservation.itemID.String(),
	})
	if err != nil {
		return err
	}
	lease, err := m.GetLease(ctx, tenant, principal, reservation.itemID)
	if err != nil {
		return err
	}
	if !lease.Live || lease.Fence != reservation.leaseFence || lease.HolderSID != claim.SID ||
		lease.HolderRunRef != runRef || lease.WorkspaceID != reservation.workspaceID {
		return broken(http.StatusConflict, "stale_fence")
	}
	return nil
}
