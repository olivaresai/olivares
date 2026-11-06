// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A session's NAME and its ARCHIVED mark (#2795), set from its row. Both are the
// session's own state, the way its peers are (session_peers.go): one value on the
// run row that every reader sees, changed with the run's write permission and
// recorded in the ledger. They are not a person's interface state like a pin
// (run_pin.go). Archiving hides nothing on the server: GET /runs returns archived
// runs with archived_at, and each client decides what its list shows.

// setRunNameRequest is the body of PUT /runs/{ref}/name. Name is required; an
// empty one clears the name, and clients fall back to their own naming.
type setRunNameRequest struct {
	Name *string `json:"name"`
}

// handleSetRunName renames the run; an empty name clears it. The route supplies
// run-write authorization in the run's recorded workspace.
func (m *Module) handleSetRunName(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.refuseForeignSteering(w, r, mc) {
		return
	}
	var body setRunNameRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.Name == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required; an empty name clears it"))
		return
	}
	name := strings.TrimSpace(*body.Name)
	if err := checkRunName(name); err != nil {
		writeRunErr(w, err)
		return
	}
	m.changeRun(w, r, mc, "sessions.run.renamed", func(ctx context.Context, sc store.Scope, rec model.Record) (bool, error) {
		if rec.String(colRunName) == name {
			return false, nil
		}
		// The one rule for every write of a name, under the run-name lock with the
		// write that follows: a session that is not cleaned already holds it. The
		// run's own row is excluded, so keeping its name is never refused.
		if err := refuseHeldRunNameIn(ctx, sc, mc.Tenant, name, rec.String(colRunRef)); err != nil {
			return false, err
		}
		rec[colRunName] = name
		if name == "" {
			rec[colRunName] = nil
		}
		return true, nil
	})
}

// handleArchiveRun archives the run for every reader, idempotently: an archived
// run keeps its first archived_at.
func (m *Module) handleArchiveRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.refuseForeignSteering(w, r, mc) {
		return
	}
	m.changeRun(w, r, mc, "sessions.run.archived", func(_ context.Context, _ store.Scope, rec model.Record) (bool, error) {
		if rec.String(colRunArchivedAt) != "" {
			return false, nil
		}
		rec[colRunResumeCancelled] = rec.String(colRunResumeAt) != "" || rec.Bool(colRunResumeAfterLimit)
		rec[colRunResumeAt] = nil
		rec[colRunResumeAfterLimit] = false
		rec[colRunResumeDismissed] = true
		rec[colRunArchivedAt] = model.NewTimestamp(m.now()).String()
		return true, nil
	})
}

// handleUnarchiveRun unarchives the run, idempotently.
func (m *Module) handleUnarchiveRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.refuseForeignSteering(w, r, mc) {
		return
	}
	m.changeRun(w, r, mc, "sessions.run.unarchived", func(_ context.Context, _ store.Scope, rec model.Record) (bool, error) {
		if rec.String(colRunArchivedAt) == "" {
			return false, nil
		}
		rec[colRunArchivedAt] = nil
		return true, nil
	})
}

// changeRun applies change to the run row under the run operation lock and
// answers the run. A change that finds its state already holding writes nothing
// and records nothing, so both directions of each route are idempotent; one that
// writes is recorded in the same transaction, with the run's reference and not
// the value it set. A released run keeps its row until it is deleted and takes
// both changes, as it takes a peers choice.
func (m *Module) changeRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, action string, change func(context.Context, store.Scope, model.Record) (bool, error)) {
	rec, err := m.applyRunChange(r.Context(), mc, chi.URLParam(r, "ref"), action, change)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	dto := m.toRunDTO(rec)
	m.showTitle(r.Context(), mc, rec, &dto)
	writeJSON(w, http.StatusOK, dto)
}

// applyRunChange is changeRun without the answer: the run row after change, written
// and recorded only when change reports it changed something.
func (m *Module) applyRunChange(ctx context.Context, mc api.ModuleContext, ref, action string, change func(context.Context, store.Scope, model.Record) (bool, error)) (model.Record, error) {
	release, err := m.rt.lockRunContext(ctx, liveKey(mc.Tenant, ref))
	if err != nil {
		return nil, err
	}
	defer release()
	var rec model.Record
	err = mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err = findRunRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		changed, cerr := change(ctx, sc, rec)
		if cerr != nil {
			return cerr
		}
		if !changed {
			return nil
		}
		if rec, err = repo.Update(ctx, rec); err != nil {
			return err
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: action, TargetKind: runKind, TargetID: model.ID(rec.String(model.ColID)),
			Meta: map[string]any{"run_ref": ref},
		})
		return err
	})
	return rec, err
}
