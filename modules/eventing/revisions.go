// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// revisions.go is the subscription change history: every configuration
// mutation appends a full post-state snapshot to an append-only revision ledger
// (the orchestration decision-ledger pattern), in the SAME transaction as the
// mutation, so the console can render "what changed, by whom, when", diff any
// two revisions and restore an earlier configuration.
//
// Minimal data (docs/SECURITY-HARDENING.md): the snapshot is the subscriptionDTO — the REDACTED
// public projection (secret/auth/sink-credential HINTS only, never a sealed or
// cleartext credential). Restore therefore covers the delivery SHAPE (name,
// enabled, event types, sources, endpoint, role, description, retry policy) and
// deliberately never touches credentials or the sink profile — those stay
// exactly as configured (rotate-secret/rotate-auth own credentials; the rotate
// endpoints are likewise not snapshotted: they change hints, not the shape).
// Restore applies to an EXISTING subscription only — a deleted one is evidence,
// not a resurrection target (its name may have been reused).
const (
	revisionKind  model.Kind = "eventing.subscription_revision"
	revisionTable            = "eventing_subscription_revision"

	colRevSubject  = "subject_id" // the subscription this revision belongs to
	colRevOp       = "op"         // create | update | delete | restore
	colRevSnapshot = "snapshot"   // subscriptionDTO JSON (redacted projection)
	colRevActor    = "actor"
	colRevActorK   = "actor_kind"

	revOpCreate  = "create"
	revOpUpdate  = "update"
	revOpDelete  = "delete"
	revOpRestore = "restore"
)

// revisionDescriptor declares the append-only revision ledger entity.
func revisionDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:       revisionKind,
		Table:      revisionTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colRevSubject, Kind: model.KindText, Indexed: true},
			{Name: colRevOp, Kind: model.KindText},
			{Name: colRevSnapshot, Kind: model.KindText},
			{Name: colRevActor, Kind: model.KindText},
			{Name: colRevActorK, Kind: model.KindText},
		},
	}
}

// appendRevision snapshots dto into the revision ledger inside the caller's
// open transaction (atomic with the mutation it records). Attribution is the
// REAL principal, like the semantic self-audit.
func appendRevision(ctx context.Context, sc store.Scope, mc api.ModuleContext, subID model.ID, op string, dto subscriptionDTO) error {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return err
	}
	snap, err := json.Marshal(dto)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colRevSubject:  subID.String(),
		colRevOp:       op,
		colRevSnapshot: string(snap),
		colRevActor:    mc.Principal.Actor(),
		colRevActorK:   mc.Principal.ActorKind(),
	})
	return err
}

// revisionDTO projects one revision row. Snapshot rides as raw JSON (it IS the
// redacted subscriptionDTO of that moment).
type revisionDTO struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	Snapshot  json.RawMessage `json:"snapshot"`
	Actor     string          `json:"actor"`
	ActorKind string          `json:"actor_kind"`
	At        string          `json:"at"`
}

// handleListRevisions lists a subscription's revision ledger, keyset-paginated
// by the time-ordered row id (chronological by ingestion, the decision-ledger
// convention).
func (m *Module) handleListRevisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colRevSubject, id.String()))
	out := listResponse[revisionDTO]{Items: []revisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		out.Cursor = page.Cursor
		out.HasMore = page.HasMore
		for _, rec := range recs {
			out.Items = append(out.Items, revisionDTO{
				ID:        rec.String(model.ColID),
				Op:        rec.String(colRevOp),
				Snapshot:  json.RawMessage(rec.String(colRevSnapshot)),
				Actor:     rec.String(colRevActor),
				ActorKind: rec.String(colRevActorK),
				At:        rec.String(model.ColCreatedAt),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// restoreRequest names the revision whose shape to re-apply.
type restoreRequest struct {
	RevisionID string `json:"revision_id"`
}

// handleRestoreSubscription re-applies the delivery shape of an earlier
// revision to an EXISTING subscription. The restored shape re-runs the full
// authoring validation (endpoint scheme, type catalog, the role-vs-type RBAC
// ceiling) — a restore is just another authored write, so a snapshot that
// predates a tightened rule cannot smuggle a now-invalid configuration back in.
// Credentials and the sink profile are never touched (see the file doc).
func (m *Module) handleRestoreSubscription(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in restoreRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.RevisionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("revision_id is required"))
		return
	}
	// The destination policy is checked BEFORE the transaction opens, because it
	// performs a DNS lookup and this module's rule is that the store never hosts a
	// network call inside an open transaction (replay.go). The snapshot is read once
	// in a View for that purpose; the Mutate below re-reads it as the authority.
	var snapEndpoint string
	if verr := mc.Data.View(r.Context(), func(sc store.Scope) error {
		revRepo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		revRec, err := revRepo.Get(r.Context(), model.ID(in.RevisionID))
		if err != nil {
			return err
		}
		if revRec.String(colRevSubject) != id.String() {
			return store.ErrNotFound
		}
		var snap subscriptionDTO
		if err := json.Unmarshal([]byte(revRec.String(colRevSnapshot)), &snap); err != nil {
			return err
		}
		snapEndpoint = snap.Endpoint
		return nil
	}); verr != nil {
		writeStoreError(w, verr)
		return
	}
	// A revision predates whatever the operator has since narrowed, so restoring one
	// is precisely how a destination that is no longer permitted gets written back.
	if msg := m.checkEndpointPolicy(r.Context(), mc.Tenant, EgressRestore, id, snapEndpoint); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}

	// Unit H: the fence's requirement, read before the transaction opens — inside it the read
	// would wait on the connection the transaction holds (egresswriterfence.go writerProof).
	proof, perr := m.writerProof(r.Context())
	if perr != nil {
		writeStoreError(w, perr)
		return
	}
	var out subscriptionDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		revRepo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		revRec, err := revRepo.Get(r.Context(), model.ID(in.RevisionID))
		if err != nil {
			return err
		}
		if revRec.String(colRevSubject) != id.String() {
			// A revision of ANOTHER subscription: 404, not 400 — do not confirm
			// the foreign revision exists.
			return store.ErrNotFound
		}
		var snap subscriptionDTO
		if err := json.Unmarshal([]byte(revRec.String(colRevSnapshot)), &snap); err != nil {
			return err
		}
		// Re-validate the restored shape as if authored now. Auth fields ride
		// from the CURRENT record (restore never changes them) so the
		// consistency checks see the real configuration.
		enabled := snap.Enabled
		req := subscriptionRequest{
			Name: snap.Name, Enabled: &enabled,
			EventTypes: snap.EventTypes, MatchSources: snap.MatchSources,
			Endpoint: snap.Endpoint, Role: snap.Role, Description: snap.Description,
			MaxAttempts: snap.MaxAttempts, InitialIntervalSeconds: snap.InitialIntervalSeconds,
			AuthType:       rec.String(colSubAuthType),
			AuthHeaderName: rec.String(colSubAuthHeaderName),
		}
		if msg := m.validateSubscription(&req, mc.Principal, mc.Tenant, false); msg != "" {
			return validationError(msg)
		}
		// Read the CURRENT disposition before the snapshot overwrites it: the fence compares the
		// transition, and a restore can reactivate a subscription an operator had disabled.
		priorEnabled := rec.Bool(colSubEnabled)
		rec[colSubName] = req.Name
		rec[colSubEnabled] = enabled
		rec[colSubTypes] = csvJoin(req.EventTypes)
		rec[colSubSources] = csvJoin(req.MatchSources)
		// Unit H: a restore re-points the live destination to whatever the snapshot held, which
		// is exactly how a destination an operator has since narrowed comes back — and it can also
		// REACTIVATE one that was disabled. Both make a destination effective, so both are governed;
		// the same rule as the update path, and the same one the trigger's WHEN clause carries.
		if rec.String(colSubEndpoint) != req.Endpoint || (!priorEnabled && enabled) {
			if err := proof.StampInto(r.Context(), sc, rec); err != nil {
				return err
			}
		}
		rec[colSubEndpoint] = req.Endpoint
		rec[colSubRole] = req.Role
		rec[colSubDescription] = req.Description
		rec[colSubMaxAttempts] = req.MaxAttempts
		rec[colSubInitInterval] = req.InitialIntervalSeconds
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toSubscriptionDTO(updated)
		if err := m.loadSinkDTO(r.Context(), sc, &out); err != nil {
			return err
		}
		if err := appendRevision(r.Context(), sc, mc, id, revOpRestore, out); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.restore", subscriptionKind, id,
			map[string]any{"name": out.Name, "revision": in.RevisionID, "enabled": out.Enabled})
	})
	if msg, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// The restored shape may re-enable or retarget: wake a worker like update.
	m.nudgeTenant(mc.Tenant)
	writeJSON(w, http.StatusOK, out)
}
