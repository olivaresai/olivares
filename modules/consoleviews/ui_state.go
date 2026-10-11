// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews

import (
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A user's console INTERFACE STATE: today only the sidebar width ("full" or the 56 px
// "rail"), kept on the server so it follows the user to another browser, as favorites do.
// One private saved-view row per user and tenant under its own reserved feature slug, beside
// the favorites row, so no new table. Gated by the module's READ permission like favorites:
// it is the caller's own interface state, every signed-in role keeps its own, nothing is
// shared, and a sidebar width is not an auditable act on the tenant.
const (
	uiStateFeature = "ui-state"
	uiStateName    = "ui-state"
)

type uiStateBody struct {
	Sidebar string `json:"sidebar"`
}

type uiStateDTO struct {
	Sidebar string `json:"sidebar,omitempty"`
	// Stored is false when the caller has no row yet (the console may then move what it
	// kept locally to the server once).
	Stored    bool   `json:"stored"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (b uiStateBody) validate() string {
	if b.Sidebar != "full" && b.Sidebar != "rail" {
		return `sidebar must be "full" or "rail"`
	}
	return ""
}

// ownUIState finds the caller's interface-state row, or nil.
func ownUIState(r *http.Request, sc store.Scope, caller string) (*model.Record, error) {
	recs, err := drain(r.Context(), sc, eq(colOwner, caller), eq(colFeature, uiStateFeature))
	if err != nil {
		return nil, err
	}
	for i := range recs {
		if recs[i].String(colName) == uiStateName {
			return &recs[i], nil
		}
	}
	return nil, nil
}

func uiStateOf(rec *model.Record) uiStateDTO {
	if rec == nil {
		return uiStateDTO{}
	}
	var body uiStateBody
	if err := json.Unmarshal([]byte(rec.String(colParams)), &body); err != nil || body.validate() != "" {
		// A row that no longer validates is read as nothing stored rather than failing the shell.
		return uiStateDTO{}
	}
	return uiStateDTO{Sidebar: body.Sidebar, Stored: true, UpdatedAt: rec.String(model.ColUpdatedAt)}
}

// handleUIStateGet returns the caller's own console interface state; stored is false when
// none was saved yet.
func (m *Module) handleUIStateGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	caller := mc.Principal.Actor()
	var out uiStateDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, err := ownUIState(r, sc, caller)
		if err != nil {
			return err
		}
		out = uiStateOf(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUIStatePut replaces the caller's console interface state.
func (m *Module) handleUIStatePut(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in uiStateBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	params, err := json.Marshal(in)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not encode the interface state"))
		return
	}
	caller := mc.Principal.Actor()
	var out uiStateDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(SavedViewKind)
		if err != nil {
			return err
		}
		rec, err := ownUIState(r, sc, caller)
		if err != nil {
			return err
		}
		if rec == nil {
			created, err := repo.Create(r.Context(), model.Record{
				colFeature: uiStateFeature, colName: uiStateName, colDesc: "",
				colParams: string(params), colOwner: caller, colShared: false,
			})
			if err != nil {
				return err
			}
			out = uiStateOf(&created)
			return nil
		}
		rec.Set(colParams, string(params))
		updated, err := repo.Update(r.Context(), *rec)
		if err != nil {
			return err
		}
		out = uiStateOf(&updated)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
