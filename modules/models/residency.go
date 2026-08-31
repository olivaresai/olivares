// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// per-workspace inference-geo residency reference. Anthropic's Workspaces
// Admin API publishes a per-workspace data_residency object (allowed_inference_geos
// + default_inference_geo) plus the immutable workspace_geo; the claude-api
// connector already surfaces them on the catalog snapshot
// (modelprovider.WorkspaceRef.Residency / .Geo). This entity persists that
// PERMITTED side per workspace so modules/compliance can probe it by kind (the
// cross-module probe pattern, never an import) and compare it against the OBSERVED
// per-request inference_geo on finops cost samples — drift reuses the
// residency_violation Finding (compliance/residency.go).
//
// Rows are registered through the module API (the same operator/sync-driven path
// as the key/workspace governance references in keys.go): whoever syncs the
// provider catalog upserts one row per workspace that reports residency. The
// semantics of allowed_geos are the provider's: a workspace WITHOUT residency
// restrictions reports an EMPTY list, which is stored as the empty string and
// means unrestricted/unreported — never inferred as denied (the deny-closed side
// lives in the comparison, not in absence of data).

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// WorkspaceResidencyKind is the per-workspace residency entity. The kind string is
// a cross-module contract: modules/compliance probes it verbatim.
const WorkspaceResidencyKind model.Kind = "models.workspace_residency"

const workspaceResidencyTable = "models_workspace_residency"

// Workspace-residency columns (contract with modules/compliance — the scan reads
// workspace_ref and allowed_geos by these names).
const (
	colWSWorkspaceRef = "workspace_ref"
	colWSAllowedGeos  = "allowed_geos"
	colWSDefaultGeo   = "default_geo"
	colWSWorkspaceGeo = "workspace_geo"
	colWSAsOf         = "as_of"
)

// registerWorkspaceResidencySchema registers the residency entity (one row
// per workspace ref per tenant; the unique index leads with tenant_id).
func registerWorkspaceResidencySchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  WorkspaceResidencyKind,
		Table: workspaceResidencyTable,
		Fields: []model.FieldSpec{
			{Name: colWSWorkspaceRef, Kind: model.KindText, Indexed: true},
			{Name: colWSAllowedGeos, Kind: model.KindText, Nullable: true},
			{Name: colWSDefaultGeo, Kind: model.KindText, Nullable: true},
			{Name: colWSWorkspaceGeo, Kind: model.KindText, Nullable: true},
			{Name: colWSAsOf, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name: "models_workspace_residency_uniq", Columns: []string{model.ColTenantID, colWSWorkspaceRef}, Unique: true,
		}},
	})
}

// workspaceResidencyDTO is one workspace's persisted residency reference.
type workspaceResidencyDTO struct {
	ID string `json:"id,omitempty"`
	// WorkspaceRef is the provider workspace id the row describes (the same ref
	// usage/cost rows carry as workspace_id).
	WorkspaceRef string `json:"workspace_ref"`
	// AllowedGeos lists the workspace's allowed_inference_geos. Empty = the
	// provider reports no restriction (unrestricted/unreported, never "denied").
	AllowedGeos []string `json:"allowed_geos"`
	// DefaultGeo is the workspace's default_inference_geo ("" = not reported).
	DefaultGeo string `json:"default_geo,omitempty"`
	// WorkspaceGeo is the immutable workspace_geo (data-at-rest home; today only
	// "us" exists upstream — pass-through, never a sealed enum).
	WorkspaceGeo string `json:"workspace_geo,omitempty"`
	// AsOf stamps when the residency settings were captured from the provider.
	AsOf string `json:"as_of,omitempty"`
}

// normalizeGeos lowercases, trims, dedupes and sorts a geo list, dropping empty
// elements — the stored form is a deterministic comma-separated string, so the
// compliance scan's membership check never depends on the producer's ordering.
func normalizeGeos(geos []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(geos))
	for _, g := range geos {
		g = strings.ToLower(strings.TrimSpace(g))
		if g == "" {
			continue
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

func (d workspaceResidencyDTO) toRecord(at string) model.Record {
	asOf := strings.TrimSpace(d.AsOf)
	if asOf == "" {
		asOf = at
	}
	return model.Record{
		colWSWorkspaceRef: d.WorkspaceRef,
		colWSAllowedGeos:  strings.Join(normalizeGeos(d.AllowedGeos), ","),
		colWSDefaultGeo:   strings.ToLower(strings.TrimSpace(d.DefaultGeo)),
		colWSWorkspaceGeo: strings.ToLower(strings.TrimSpace(d.WorkspaceGeo)),
		colWSAsOf:         asOf,
	}
}

func toWorkspaceResidencyDTO(rec model.Record) workspaceResidencyDTO {
	d := workspaceResidencyDTO{
		ID:           rec.String(model.ColID),
		WorkspaceRef: rec.String(colWSWorkspaceRef),
		AllowedGeos:  []string{},
		DefaultGeo:   rec.String(colWSDefaultGeo),
		WorkspaceGeo: rec.String(colWSWorkspaceGeo),
		AsOf:         rec.String(colWSAsOf),
	}
	if csv := rec.String(colWSAllowedGeos); csv != "" {
		d.AllowedGeos = strings.Split(csv, ",")
	}
	return d
}

// handleListWorkspaceResidency lists the persisted per-workspace residency
// references, optionally filtered by workspace_ref.
func (m *Module) handleListWorkspaceResidency(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("workspace_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colWSWorkspaceRef, v))
	}
	out := listResponse[workspaceResidencyDTO]{Items: []workspaceResidencyDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(WorkspaceResidencyKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toWorkspaceResidencyDTO(rec))
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

// handleUpsertWorkspaceResidency records (or replaces) one workspace's residency
// reference. It is an upsert keyed by workspace_ref: a catalog re-sync replaces the
// geos in place, never duplicates the row. Attributed to the real principal in the
// audit ledger like every other governance write of this module.
func (m *Module) handleUpsertWorkspaceResidency(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in workspaceResidencyDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	in.WorkspaceRef = trimClamp(in.WorkspaceRef)
	if in.WorkspaceRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("workspace_ref is required"))
		return
	}
	var out workspaceResidencyDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(WorkspaceResidencyKind)
		if err != nil {
			return err
		}
		at := model.NewTimestamp(time.Now()).String()
		existing, _, err := repo.List(r.Context(), model.Query{Filters: []model.Filter{eq(colWSWorkspaceRef, in.WorkspaceRef)}, Limit: 1})
		if err != nil {
			return err
		}
		var rec model.Record
		if len(existing) > 0 {
			rec = existing[0]
			for k, v := range in.toRecord(at) {
				rec[k] = v
			}
			rec, err = repo.Update(r.Context(), rec)
		} else {
			rec, err = repo.Create(r.Context(), in.toRecord(at))
		}
		if err != nil {
			return err
		}
		out = toWorkspaceResidencyDTO(rec)
		return auditOwned(r.Context(), sc, mc, WorkspaceResidencyKind, "upsert", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
