// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// costcenter.go implements the first-class CostCenter entity (accounting
// code for internal billing), the CostCenterMapping rules (dimension-to-CC
// binding), and the ingestion-time resolution that stamps cost_center_ref on
// each cost sample. Cost centers are a flat list (no hierarchy); mappings bind
// attribution dimensions (team, workspace, project, agent, provider, identity)
// to a cost center code. Resolution at ingestion follows the mapping rules by
// priority and sets attr.CostCenterRef — the same denormalized pattern as
// team/project/workspace_ref.

// --- DTOs -------------------------------------------------------------------

type costCenterDTO struct {
	ID          string            `json:"id,omitempty"`
	Code        string            `json:"code"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	Status      string            `json:"status"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   string            `json:"created_at,omitempty"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
}

func toCostCenterDTO(rec model.Record) costCenterDTO {
	dto := costCenterDTO{
		ID:          rec.String(model.ColID),
		Code:        rec.String(colCCCode),
		Name:        rec.String(colCCName),
		Description: rec.String(colCCDescription),
		Owner:       rec.String(colCCOwner),
		Status:      rec.String(colCCStatus),
		CreatedAt:   rec.String(model.ColCreatedAt),
		UpdatedAt:   rec.String(model.ColUpdatedAt),
	}
	if raw := rec.String(colCCMetadata); raw != "" {
		_ = json.Unmarshal([]byte(raw), &dto.Metadata)
	}
	return dto
}

// validate rejects a cost center the engine cannot store, and DEFAULTS its status.
//
// THE RECEIVER IS A POINTER, and that is not style. With a VALUE receiver — as it was — the
// `d.Status = "active"` below mutated a COPY. Validation passed, because by then the copy had a
// status, and the CALLER kept the empty string, which is what reached the record.
//
// Measured 2026-08-17: `PUT /cost-centers/{id}` is a full replace, so omitting `status` stored
// `""`, and cost attribution requires EXACTLY "active" (see the mapping resolver below). That cost
// center then attributed nothing, ever, and the "unattributed spend" figure never moved — the
// opposite of what the cost-centers screen exists to do.
//
// `handleCreateCostCenter` did not suffer it: it defaults by hand before calling. That redundancy
// is precisely what hid the fact that this default reached nobody. And no test saw it because every
// fixture in costcenter_test.go writes `colCCStatus: "active"` by hand — a fixture that supplies
// what the code needs cannot disagree with it.
func (d *costCenterDTO) validate() string {
	if d.Code == "" {
		return "code is required"
	}
	if d.Name == "" {
		return "name is required"
	}
	if d.Status == "" {
		d.Status = "active"
	}
	if d.Status != "active" && d.Status != "archived" {
		return "status must be active or archived"
	}
	return ""
}

type costCenterMappingDTO struct {
	ID              string `json:"id,omitempty"`
	CostCenterID    string `json:"cost_center_id"`
	SourceDimension string `json:"source_dimension"`
	SourceKey       string `json:"source_key"`
	Priority        int64  `json:"priority"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

func toCostCenterMappingDTO(rec model.Record) costCenterMappingDTO {
	return costCenterMappingDTO{
		ID:              rec.String(model.ColID),
		CostCenterID:    rec.String(colCCMappingCostCenterID),
		SourceDimension: rec.String(colCCMappingDimension),
		SourceKey:       rec.String(colCCMappingKey),
		Priority:        rec.Int(colCCMappingPriority),
		CreatedAt:       rec.String(model.ColCreatedAt),
		UpdatedAt:       rec.String(model.ColUpdatedAt),
	}
}

func (d costCenterMappingDTO) validate() string {
	if d.SourceDimension == "" {
		return "source_dimension is required"
	}
	if !validMappingDimensions[d.SourceDimension] {
		return "source_dimension must be one of team, workspace, project, agent, provider, identity"
	}
	if d.SourceKey == "" {
		return "source_key is required"
	}
	return ""
}

// --- HTTP Handlers: Cost Centers --------------------------------------------

func (m *Module) handleListCostCenters(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if st := r.URL.Query().Get("status"); st != "" {
		q.Filters = append(q.Filters, eq(colCCStatus, st))
	}
	out := listResponse[costCenterDTO]{Items: []costCenterDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toCostCenterDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleCreateCostCenter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in costCenterDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out costCenterDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		metaJSON := ""
		if len(in.Metadata) > 0 {
			b, _ := json.Marshal(in.Metadata)
			metaJSON = string(b)
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colCCCode:        in.Code,
			colCCName:        in.Name,
			colCCDescription: in.Description,
			colCCOwner:       in.Owner,
			colCCStatus:      in.Status,
			colCCMetadata:    metaJSON,
		})
		if err != nil {
			return err
		}
		out = toCostCenterDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleGetCostCenter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out costCenterDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toCostCenterDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleUpdateCostCenter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in costCenterDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	// ⛔ ¿VENÍA EL ESTADO EN EL CUERPO? Hay que capturarlo AQUÍ, antes de validar: `validate()` lo
	// defaultea a "active" y a partir de ese punto «no lo mandó» y «mandó active» son
	// indistinguibles.
	estadoOmitido := in.Status == ""
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out costCenterDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// ⛔ UN RENOMBRADO NO CAMBIA EL CICLO DE VIDA, y esta línea existe porque el arreglo anterior
		// lo rompía en la dirección contraria. Medido por un panel adversarial interno el
		// 2026-08-18: con `validate()` defaulteando a "active", un PUT que omite `status` sobre un
		// centro ARCHIVADO lo RESUCITABA en silencio — volvía a atribuir gasto (`:475`) y a entrar en
		// los extractos (`statements.go:138`).
		//
		// Antes del arreglo del receptor guardaba "" (roto hacia el silencio); con el arreglo a secas
		// activaba (roto hacia el ruido). Ninguna de las dos es lo que un renombrado debe hacer: el
		// campo omitido SE CONSERVA.
		if estadoOmitido {
			in.Status = rec.String(colCCStatus)
			// Registro heredado con el estado vacío —los que dejó el defecto del receptor por valor—:
			// un PUT lo repara en vez de perpetuarlo.
			if in.Status == "" {
				in.Status = "active"
			}
		}
		rec[colCCCode] = in.Code
		rec[colCCName] = in.Name
		rec[colCCDescription] = in.Description
		rec[colCCOwner] = in.Owner
		rec[colCCStatus] = in.Status
		if len(in.Metadata) > 0 {
			b, _ := json.Marshal(in.Metadata)
			rec[colCCMetadata] = string(b)
		} else {
			rec[colCCMetadata] = ""
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toCostCenterDTO(updated)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleDeleteCostCenter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		// Delete associated mappings first.
		mapRepo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		mappings, _, err := mapRepo.List(r.Context(), model.Query{
			Filters: []model.Filter{eq(colCCMappingCostCenterID, id.String())},
			Limit:   listCap,
		})
		if err != nil {
			return err
		}
		for _, mr := range mappings {
			if err := mapRepo.Delete(r.Context(), model.ID(mr.String(model.ColID))); err != nil {
				return err
			}
		}
		return repo.Delete(r.Context(), id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// --- HTTP Handlers: Mappings ------------------------------------------------

func (m *Module) handleListCostCenterMappings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ccID := chi.URLParam(r, "id")
	out := listResponse[costCenterMappingDTO]{Items: []costCenterMappingDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		q := listQuery(r)
		q.Filters = append(q.Filters, eq(colCCMappingCostCenterID, ccID))
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toCostCenterMappingDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleCreateCostCenterMapping(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ccID := chi.URLParam(r, "id")
	var in costCenterMappingDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	in.CostCenterID = ccID
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out costCenterMappingDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colCCMappingCostCenterID: ccID,
			colCCMappingDimension:    in.SourceDimension,
			colCCMappingKey:          in.SourceKey,
			colCCMappingPriority:     in.Priority,
		})
		if err != nil {
			return err
		}
		out = toCostCenterMappingDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleDeleteCostCenterMapping(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	mid := model.ID(chi.URLParam(r, "mid"))
	if mid.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid mapping id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		return repo.Delete(r.Context(), mid)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// --- Cost Center Resolution (called from ingest.go) -------------------------

// mappingDimValue extracts the attribution field for a mapping dimension.
func mappingDimValue(attr *attribution, dim string) string {
	switch dim {
	case "team":
		return attr.Team
	case "workspace":
		return attr.WorkspaceRef
	case "project":
		return attr.Project
	case "agent":
		return attr.AgentRef
	case "provider":
		return attr.ProviderRef
	case "identity":
		return attr.IdentityRef
	}
	return ""
}

// resolveCostCenter resolves the cost center for a cost sample by querying the
// mapping rules. It checks each dimension that has a value in attr and picks
// the matching rule with the highest priority. If no rule matches, CostCenterRef
// is left empty (unmapped traffic).
func resolveCostCenter(ctx context.Context, sc store.Scope, attr *attribution) error {
	repo, err := sc.Ext(costCenterMappingKind)
	if err != nil {
		return err
	}

	// Build candidate lookups: for each dimension that has a value in the
	// attribution, check if a mapping rule exists.
	dims := []string{"team", "workspace", "project", "agent", "provider", "identity"}
	bestPriority := int64(-1)
	bestCCID := ""

	for _, dim := range dims {
		val := mappingDimValue(attr, dim)
		if val == "" {
			continue
		}
		recs, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{
				eq(colCCMappingDimension, dim),
				eq(colCCMappingKey, val),
			},
			Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			continue
		}
		prio := recs[0].Int(colCCMappingPriority)
		if prio > bestPriority {
			bestPriority = prio
			bestCCID = recs[0].String(colCCMappingCostCenterID)
		}
	}

	if bestCCID == "" {
		return nil
	}

	// Resolve the cost center code from the CC entity.
	ccRepo, err := sc.Ext(costCenterKind)
	if err != nil {
		return err
	}
	ccRec, err := ccRepo.Get(ctx, model.ID(bestCCID))
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if ccRec.String(colCCStatus) != "active" {
		return nil
	}
	attr.CostCenterRef = ccRec.String(colCCCode)
	return nil
}

// isNotFound checks if an error is a store not-found error.
func isNotFound(err error) bool {
	return err != nil && err.Error() == "not found"
}
