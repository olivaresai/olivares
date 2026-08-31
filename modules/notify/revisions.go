// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// revisions.go is the route change history: every configuration mutation
// appends a full post-state snapshot to an append-only revision ledger (the
// eventing subscription-revision pattern), in the SAME transaction as the
// mutation, so the console can render "what changed, by whom, when", diff any
// two revisions and restore an earlier configuration.
//
// A route holds no credential (destination is a provisioned NAME), so the
// snapshot is simply the routeDTO. Restore re-applies everything the update
// verb may change — enabled, the whole predicate, destination, windows,
// priority — and, like update, never the natural key (name). It re-runs the
// full authoring validation, so a snapshot that predates a tightened rule
// cannot smuggle a now-invalid predicate back in. Restore targets an EXISTING
// route only — a deleted one is evidence, not a resurrection target.
const (
	routeRevisionKind  model.Kind = "notify.route_revision"
	routeRevisionTable            = "notify_route_revision"

	colRevSubject  = "subject_id" // the route this revision belongs to
	colRevOp       = "op"         // create | update | delete | restore
	colRevSnapshot = "snapshot"   // routeDTO JSON
	colRevActor    = "actor"
	colRevActorK   = "actor_kind"

	revOpCreate  = "create"
	revOpUpdate  = "update"
	revOpDelete  = "delete"
	revOpRestore = "restore"
)

// routeRevisionDescriptor declares the append-only revision ledger entity.
func routeRevisionDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:       routeRevisionKind,
		Table:      routeRevisionTable,
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
func appendRevision(ctx context.Context, sc store.Scope, mc api.ModuleContext, routeID model.ID, op string, dto routeDTO) error {
	repo, err := sc.Ext(routeRevisionKind)
	if err != nil {
		return err
	}
	snap, err := json.Marshal(dto)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colRevSubject:  routeID.String(),
		colRevOp:       op,
		colRevSnapshot: string(snap),
		colRevActor:    mc.Principal.Actor(),
		colRevActorK:   mc.Principal.ActorKind(),
	})
	return err
}

// revisionDTO projects one revision row. Snapshot rides as raw JSON (it IS the
// routeDTO of that moment).
type revisionDTO struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	Snapshot  json.RawMessage `json:"snapshot"`
	Actor     string          `json:"actor"`
	ActorKind string          `json:"actor_kind"`
	At        string          `json:"at"`
}

// handleListRevisions lists a route's revision ledger, keyset-paginated by the
// time-ordered row id (chronological by ingestion, the decision-ledger
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
		repo, err := sc.Ext(routeRevisionKind)
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

// restoreRequest names the revision whose configuration to re-apply.
type restoreRequest struct {
	RevisionID string `json:"revision_id"`
}

// handleRestoreRoute re-applies the configuration of an earlier revision to an
// EXISTING route (never the name — the natural key, exactly like update).
func (m *Module) handleRestoreRoute(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
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
	var snap routeDTO
	var dto routeDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		revRepo, err := sc.Ext(routeRevisionKind)
		if err != nil {
			return err
		}
		revRec, err := revRepo.Get(r.Context(), model.ID(in.RevisionID))
		if err != nil {
			return err
		}
		if revRec.String(colRevSubject) != id.String() {
			// A revision of ANOTHER route: 404, not 400 — do not confirm the
			// foreign revision exists.
			return store.ErrNotFound
		}
		if err := json.Unmarshal([]byte(revRec.String(colRevSnapshot)), &snap); err != nil {
			return err
		}
		// Re-validate the restored configuration as if authored now.
		if snap.Destination == "" {
			return errRestoreValidation("destination is required")
		}
		if !validSeverity(snap.MinSeverity) {
			return errRestoreValidation("min_severity must be empty or info|low|medium|high|critical")
		}
		if msg := validateMatchTypes(snap.MatchTypes); msg != "" {
			return errRestoreValidation(msg)
		}
		// The destination is re-validated like create and update, and NOT taken on
		// trust from the snapshot. A revision predates whatever the operator has since
		// narrowed, so restoring one was exactly how a destination the tenant may no
		// longer address got written back — which is what this file's own header
		// promises cannot happen.
		if msg := m.validateDestination(mc, snap.Destination); msg != "" {
			return errRestoreValidation(msg)
		}
		rec[colEnabled] = snap.Enabled
		rec[colMatchTypes] = csvJoin(snap.MatchTypes)
		rec[colMatchKinds] = csvJoin(snap.MatchKinds)
		rec[colMinSeverity] = snap.MinSeverity
		rec[colMatchSources] = csvJoin(snap.MatchSources)
		rec[colMatchSubjects] = csvJoin(snap.MatchSubjectKinds)
		rec[colDestination] = clamp(snap.Destination, maxNameLen)
		rec[colDedupWindow] = nonNeg(snap.DedupWindowSeconds)
		rec[colThrottleWin] = nonNeg(snap.ThrottleWindowSeconds)
		rec[colPriority] = snap.Priority
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = toRouteDTO(updated)
		if err := appendRevision(r.Context(), sc, mc, id, revOpRestore, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "notify.route.restore", routeKind, id, map[string]any{
			"name": dto.Name, "revision": in.RevisionID, "destination": dto.Destination, "enabled": dto.Enabled,
		})
	})
	if msg, ok := asRestoreValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// errRestoreValidation marks a caller mistake distinguishable from a store
// failure inside the restore transaction.
type errRestoreValidation string

func (e errRestoreValidation) Error() string { return string(e) }

// asRestoreValidation unwraps an errRestoreValidation, if err is one.
func asRestoreValidation(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if v, ok := err.(errRestoreValidation); ok {
		return string(v), true
	}
	return "", false
}
