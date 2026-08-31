// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// revisions.go is the schedule change history: every configuration
// mutation appends a full post-state snapshot to an append-only revision ledger
// (the module's own decision-ledger pattern, applied to CONFIG instead of
// fire/miss evidence), in the SAME transaction as the mutation.
//
// Restore re-applies the MUTABLE shape of an earlier revision — desired_status,
// subject_ref, cadence_spec, expected interval, grace — with exactly the patch
// verb's semantics: the same validations, the same incoherent-combination
// rejection, and the same consequences (a retarget clears a sticky cadence-miss
// and inherently voids a stale fire approval, because the plan_hash changes).
// The immutable identity (name, subject_kind, trigger_kind) is identical in
// every revision of a schedule, so restore never needs to touch it. Fire events
// are NOT config revisions — they live in the decision ledger.
const (
	schedRevisionKind  model.Kind = "orchestration.schedule_revision"
	schedRevisionTable            = "orchestration_schedule_revision"

	colRevSubject  = "subject_id" // the schedule this revision belongs to
	colRevOp       = "op"         // create | update | restore
	colRevSnapshot = "snapshot"   // scheduleDTO JSON
	colRevActor    = "actor"
	colRevActorK   = "actor_kind"

	revOpCreate  = "create"
	revOpUpdate  = "update"
	revOpRestore = "restore"
)

// schedRevisionDescriptor declares the append-only revision ledger entity.
func schedRevisionDescriptor() model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:       schedRevisionKind,
		Table:      schedRevisionTable,
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
func appendRevision(ctx context.Context, sc store.Scope, mc api.ModuleContext, schedID model.ID, op string, dto scheduleDTO) error {
	repo, err := sc.Ext(schedRevisionKind)
	if err != nil {
		return err
	}
	snap, err := json.Marshal(dto)
	if err != nil {
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colRevSubject:  schedID.String(),
		colRevOp:       op,
		colRevSnapshot: string(snap),
		colRevActor:    mc.Principal.Actor(),
		colRevActorK:   mc.Principal.ActorKind(),
	})
	return err
}

// revisionDTO projects one revision row. Snapshot rides as raw JSON (it IS the
// scheduleDTO of that moment — derived fields like health/missed_at are
// evidence of the moment, never re-applied by restore).
type revisionDTO struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	Snapshot  json.RawMessage `json:"snapshot"`
	Actor     string          `json:"actor"`
	ActorKind string          `json:"actor_kind"`
	At        string          `json:"at"`
}

// handleListRevisions lists a schedule's revision ledger, keyset-paginated by
// the time-ordered row id (chronological by ingestion, the decision-ledger
// convention).
func (m *Module) handleListRevisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("schedule id required"))
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colRevSubject, id.String()))
	out := listResponse[revisionDTO]{Items: []revisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(schedRevisionKind)
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

// restoreRequest names the revision whose mutable shape to re-apply.
type restoreRequest struct {
	RevisionID string `json:"revision_id"`
	// ApprovalRef is phase 2 when the restored shape ACTIVATES the routine and
	// a routine policy requires approval.
	ApprovalRef string `json:"approval_ref,omitempty"`
}

// handleRestoreSchedule re-applies the mutable shape of an earlier revision via
// the patch verb's exact application path (same validation, same cadence-miss
// clearing, same plan_hash consequence).
func (m *Module) handleRestoreSchedule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("schedule id required"))
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
	// restore re-applies desired_status, cadence_spec and the expected
	// interval, so it is a THIRD declaration path: gating only create and patch
	// would leave "restore an old revision" as the way around every control.
	current, ok, lerr := m.loadSchedule(r.Context(), mc, id)
	if lerr != nil {
		writeStoreError(w, lerr)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	snapshot, sok, serr := m.loadRevisionSnapshot(r.Context(), mc, id, in.RevisionID)
	if serr != nil {
		writeStoreError(w, serr)
		return
	}
	if !sok {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	scope := routineScopeOfSchedule(current)
	pol, denial := m.resolvePolicy(r.Context(), mc.Tenant, scope)
	if denial != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.restore.policy_denied", id, denial)
		writeRoutineDenial(w, denial)
		return
	}
	post := routineShape{
		triggerKind: current.String(colTriggerKind), // immutable across revisions
		cadenceSpec: snapshot.CadenceSpec,
		intervalSec: snapshot.ExpectedIvl,
		graceFactor: snapshot.GraceFactor,
		subjectKind: current.String(colSubjectKind),
		subjectRef:  snapshot.SubjectRef,
		active:      snapshot.DesiredStatus == "active",
	}
	if d := m.checkDeclaration(r.Context(), pol, post); d != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.restore.policy_denied", id, d)
		writeRoutineDenial(w, d)
		return
	}
	activating := post.active && current.String(colDesiredStat) != "active"
	if activating && pol.RequireApproval {
		if done := m.routineApproval(w, r, mc, routineApprovalReq{
			planHash: activationPlanHash(mc.Tenant, id, post, scope, pol),
			subject:  id.String(), ref: in.ApprovalRef, auditID: id,
			auditAction: "orchestration.schedule.restore.policy_denied", pol: pol,
		}); done {
			return
		}
	}

	var snap scheduleDTO
	var dto scheduleDTO
	found := false
	badCombo := false
	var capDenial *routineDenial
	raced := false
	err := m.withAdmissionFence(r.Context(), mc, len(pol.ActiveCaps) > 0, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		// Re-derive the activation from the FRESHLY read row (see the same
		// guard in handlePatchSchedule): a concurrent pause must not turn this
		// into an unapproved, uncapped activation.
		freshActivating := post.active && rec.String(colDesiredStat) != "active"
		if freshActivating {
			if !activating {
				raced, found = true, true
				return nil
			}
			// Capacity FIRST, then spend the approval — the same order as
			// create and patch. Reversed, a restore refused for capacity still
			// burns the human decision: the claim row commits with the rest of
			// the transaction, and the retry (once capacity frees up) is
			// refused 409 "already used" for an activation that never happened.
			d, aerr := admitActive(r.Context(), sc, pol, scope, id)
			if aerr != nil {
				return aerr
			}
			if d != nil {
				capDenial, found = d, true
				return nil
			}
		}
		revRepo, err := sc.Ext(schedRevisionKind)
		if err != nil {
			return err
		}
		revRec, err := revRepo.Get(r.Context(), model.ID(in.RevisionID))
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		if revRec.String(colRevSubject) != id.String() {
			// A revision of ANOTHER schedule: not found — do not confirm the
			// foreign revision exists.
			return nil
		}
		if err := json.Unmarshal([]byte(revRec.String(colRevSnapshot)), &snap); err != nil {
			return err
		}
		// Re-validate the restored shape with the patch verb's rules.
		if !validStatuses[snap.DesiredStatus] || !validInterval(snap.ExpectedIvl) ||
			snap.GraceFactor < 1 || snap.GraceFactor > maxGrace || snap.SubjectRef == "" {
			badCombo = true
			found = true
			return nil
		}
		rec[colDesiredStat] = snap.DesiredStatus
		rec[colSubjectRef] = clamp(snap.SubjectRef, maxRefLen)
		if snap.CadenceSpec == "" {
			rec[colCadenceSpec] = nil
		} else {
			rec[colCadenceSpec] = clamp(snap.CadenceSpec, maxRefLen)
		}
		rec[colExpectedIvl] = snap.ExpectedIvl
		rec[colGraceFactor] = snap.GraceFactor
		// The create/patch coherence rule (trigger_kind is immutable, so the
		// stored one is authoritative).
		if rec.Int(colExpectedIvl) > 0 && rec.String(colTriggerKind) != "cron" {
			badCombo = true
			found = true
			return nil
		}
		// A restore retargets by definition: no longer "missed", stale fire
		// approvals void via the plan_hash.
		rec[colMissedAt] = nil
		// The approval is spent LAST, immediately before the row it authorizes.
		// Every refusal in this transaction returns nil rather than an error, so
		// the transaction COMMITS: a claim taken any earlier survives a 400/422
		// and burns a human decision for a change that never landed.
		if freshActivating && pol.RequireApproval {
			cd, cerr := claimActivationApproval(r.Context(), sc, in.ApprovalRef, id.String(),
				activationPlanHash(mc.Tenant, id, post, scope, pol))
			if cerr != nil {
				return cerr
			}
			if cd != nil {
				capDenial, found = cd, true
				return nil
			}
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		observed, oerr := m.lastSubjectActivity(r.Context(), sc, updated.String(colSubjectRef))
		if oerr != nil {
			return oerr
		}
		dto = toScheduleDTO(updated, observed)
		found = true
		if err := appendRevision(r.Context(), sc, mc, id, revOpRestore, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.restore", scheduleKind, id,
			activationMeta(pol, in.ApprovalRef, map[string]any{
				"name": dto.Name, "revision": in.RevisionID, "desired_status": dto.DesiredStatus}))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if raced {
		writeJSON(w, http.StatusConflict, errorBody("the schedule's status changed concurrently; re-submit against the current state"))
		return
	}
	if capDenial != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.restore.policy_denied", id, capDenial)
		writeRoutineDenial(w, capDenial)
		return
	}
	if badCombo {
		writeJSON(w, http.StatusBadRequest, errorBody("the revision's shape is no longer valid for this schedule"))
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// loadRevisionSnapshot reads a revision's stored scheduleDTO snapshot for the
// pre-flight policy check, applying the SAME ownership rule as the restore
// transaction: a revision belonging to another schedule is "not found" rather
// than a confirmation that it exists.
func (m *Module) loadRevisionSnapshot(ctx context.Context, mc api.ModuleContext, schedID model.ID, revisionID string) (scheduleDTO, bool, error) {
	var out scheduleDTO
	found := false
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(schedRevisionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(revisionID))
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		if rec.String(colRevSubject) != schedID.String() {
			return nil
		}
		if uerr := json.Unmarshal([]byte(rec.String(colRevSnapshot)), &out); uerr != nil {
			return uerr
		}
		found = true
		return nil
	})
	return out, found, err
}
