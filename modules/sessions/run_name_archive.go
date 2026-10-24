// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
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
	m.changeRun(w, r, mc, "sessions.run.renamed", func(rec model.Record) bool {
		if rec.String(colRunName) == name {
			return false
		}
		rec[colRunName] = name
		if name == "" {
			rec[colRunName] = nil
		}
		return true
	})
}

// handleArchiveRun archives the run for every reader, idempotently: an archived
// run keeps its first archived_at.
func (m *Module) handleArchiveRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.refuseForeignSteering(w, r, mc) {
		return
	}
	m.changeRun(w, r, mc, "sessions.run.archived", func(rec model.Record) bool {
		if rec.String(colRunArchivedAt) != "" {
			return false
		}
		rec[colRunArchivedAt] = model.NewTimestamp(m.now()).String()
		return true
	})
}

// handleUnarchiveRun unarchives the run, idempotently.
func (m *Module) handleUnarchiveRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.refuseForeignSteering(w, r, mc) {
		return
	}
	m.changeRun(w, r, mc, "sessions.run.unarchived", func(rec model.Record) bool {
		if rec.String(colRunArchivedAt) == "" {
			return false
		}
		rec[colRunArchivedAt] = nil
		return true
	})
}

// changeRun applies change to the run row under the run operation lock and
// answers the run. A change that finds its state already holding writes nothing
// and records nothing, so both directions of each route are idempotent; one that
// writes is recorded in the same transaction, with the run's reference and not
// the value it set. A released run keeps its row until it is deleted and takes
// both changes, as it takes a peers choice.
func (m *Module) changeRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, action string, change func(model.Record) bool) {
	ref := chi.URLParam(r, "ref")
	release, err := m.rt.lockRunContext(r.Context(), liveKey(mc.Tenant, ref))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	defer release()
	var dto runDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(r.Context(), repo, ref)
		if err != nil {
			return err
		}
		if change(rec) {
			if rec, err = repo.Update(r.Context(), rec); err != nil {
				return err
			}
			if _, err := sc.Audit().Append(r.Context(), model.AuditDraft{
				Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
				Action: action, TargetKind: runKind, TargetID: model.ID(rec.String(model.ColID)),
				Meta: map[string]any{"run_ref": ref},
			}); err != nil {
				return err
			}
		}
		dto = m.toRunDTO(rec)
		return nil
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}
