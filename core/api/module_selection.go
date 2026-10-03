// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

// ModuleSelectionService reads and changes the deployment's module selection:
// which optional modules the engine runs. The composition root implements it; a
// change is recorded in the deployment settings and applied by restarting the
// engine, so the reply says whether it is restarting.
type ModuleSelectionService interface {
	ModuleSelection(ctx context.Context) (ModuleSelectionDTO, error)
	SelectModules(ctx context.Context, actor auth.Principal, selected []string) (ModuleSelectionDTO, error)
}

// ModuleSelectionDTO is the selection and what each module does on this node.
type ModuleSelectionDTO struct {
	Modules []ModuleStateDTO `json:"modules"`
	// Restarting says the engine is restarting itself to apply the change; the
	// console waits for it and reconnects.
	Restarting bool `json:"restarting,omitempty"`
}

// ModuleStateDTO is one module of the catalog.
type ModuleStateDTO struct {
	Name string `json:"name"`
	// Selected: the administrator chose it.
	Selected bool `json:"selected"`
	// Running: it runs on this node now (selected, required, or always on).
	Running bool `json:"running"`
	// AlwaysOn: the engine cannot run without it; it cannot be deselected.
	AlwaysOn bool `json:"always_on,omitempty"`
	// Requires names the modules it runs with; RequiredBy the running modules
	// that keep it on although it is not selected.
	Requires   []string `json:"requires,omitempty"`
	RequiredBy []string `json:"required_by,omitempty"`
	// HoldsData: its tables hold rows in this installation. A module that is not
	// running keeps that data, but nothing acts on it (finops off: budgets are no
	// longer enforced), so the screen says so before and after it is turned off.
	HoldsData bool `json:"holds_data,omitempty"`
	// ActivatedBy names the active edition add-ons (activation families) that run
	// it although it may not be selected: enabling the family enabled it.
	ActivatedBy []string `json:"activated_by,omitempty"`
}

var (
	// ErrModulesUnavailable: no module selection service is wired. 501.
	ErrModulesUnavailable = errors.New("api: module selection is not available on this engine")
	// ErrUnknownModule: the selection names a module this build does not have. 400.
	ErrUnknownModule = errors.New("api: unknown module")
	// ErrModulesNotRecorded: the selection could not be saved. 503.
	ErrModulesNotRecorded = errors.New("api: module selection not recorded in the deployment settings")
	// ErrModulesRestartUnavailable: the selection is saved, but the engine could
	// not restart itself to apply it. 503.
	ErrModulesRestartUnavailable = errors.New("api: the engine cannot restart itself to apply the module selection")
)

func (s *Server) moduleSelectionSvc(w http.ResponseWriter, r *http.Request) (ModuleSelectionService, bool) {
	if s.moduleSelection == nil {
		s.writeError(w, r, ErrModulesUnavailable)
		return nil, false
	}
	return s.moduleSelection, true
}

func (s *Server) handleModuleSelection(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.moduleSelectionSvc(w, r)
	if !ok {
		return
	}
	dto, err := svc.ModuleSelection(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleSelectModules(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireStepUp(w, r, p) {
		return
	}
	svc, ok := s.moduleSelectionSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		Selected []string `json:"selected"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Selected == nil {
		s.badRequest(w, r, "selected is required: the modules to run, by name")
		return
	}
	dto, err := svc.SelectModules(r.Context(), p, in.Selected)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}
