// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// resourceNodeDTO is the wire shape of one node in the tenant's Resource tree — the
// authoritative, navigable source the console's folder/subtree binding picker
// reads to anchor an folder binding to a real Resource id. It carries ONLY the
// non-secret, display-safe columns of model.Resource: it deliberately OMITS URI (a
// natural-identifier locator), Owner and Metadata (free-form context), which could carry
// a connection string or other sensitive value (docs/SECURITY-HARDENING.md minimal-data). The id is the
// value a folder binding stores as scope_ref; Path is the store-managed materialized
// ancestor-id chain — useful for subtree math, made of ids, never a secret.
type resourceNodeDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Path        string `json:"path,omitempty"`
	ParentID    string `json:"parent_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Sensitivity is the operator-assigned label (non-secret), surfaced so the picker can
	// warn before anchoring a binding to a sensitive subtree.
	Sensitivity string `json:"sensitivity,omitempty"`
}

func toResourceNodeDTO(rec model.Resource) resourceNodeDTO {
	d := resourceNodeDTO{
		ID:          rec.ID.String(),
		Name:        rec.Name,
		Kind:        rec.Kind,
		Path:        rec.Path,
		Sensitivity: rec.Sensitivity,
	}
	if !rec.ParentID.IsZero() {
		d.ParentID = rec.ParentID.String()
	}
	if !rec.WorkspaceID.IsZero() {
		d.WorkspaceID = rec.WorkspaceID.String()
	}
	return d
}

// handleListResources serves the tenant's Resource tree for the folder/subtree
// binding picker — the authoritative, navigable enumeration that lets an operator pick a
// real Resource id to anchor an folder binding (binding.go resolveScope validates
// that id deny-closed). It is a LAZY tree read: with no ?parent it returns the tree ROOTS,
// with ?parent=<id> the DIRECT children of that node (Children), and with ?subtree=<id>
// the whole subtree — root + descendants — in one materialized-path scan (Subtree).
// Optional ?kind and ?workspace_id filters and keyset ?limit/?cursor paging. It is
// read-tier (permBindingRead): the picker feeds binding authoring, it never mutates.
//
// Tenant isolation is STRUCTURAL: the engine pins the data handle to the resolved tenant,
// so another tenant's nodes are never returned and an other-tenant subtree root surfaces
// as ErrNotFound → 404 (deny-closed, never a leak of another tenant's tree).
func (m *Module) handleListResources(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
		q.Filters = append(q.Filters, eq("kind", kind))
	}
	if ws := strings.TrimSpace(r.URL.Query().Get("workspace_id")); ws != "" {
		q.Filters = append(q.Filters, eq("workspace_id", ws))
	}
	// parent and subtree are Resource ids; parse + shape-validate UP FRONT (deny-closed on
	// a malformed id) so a bad id is a clean 400, never a store round-trip.
	var parentID, subtreeID model.ID
	if raw := strings.TrimSpace(r.URL.Query().Get("parent")); raw != "" {
		id, err := model.ParseID(raw)
		if err != nil || id.IsZero() {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid parent id"))
			return
		}
		parentID = id
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("subtree")); raw != "" {
		id, err := model.ParseID(raw)
		if err != nil || id.IsZero() {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid subtree id"))
			return
		}
		subtreeID = id
	}

	out := listResponse[resourceNodeDTO]{Items: []resourceNodeDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var (
			recs []model.Resource
			page model.Page
			derr error
		)
		switch {
		case !subtreeID.IsZero():
			recs, page, derr = sc.Resources().Subtree(r.Context(), subtreeID, q)
		case !parentID.IsZero():
			recs, page, derr = sc.Resources().Children(r.Context(), parentID, q)
		default:
			// No anchor → the tree roots (the direct children of the zero parent).
			recs, page, derr = sc.Resources().Children(r.Context(), model.ID(""), q)
		}
		if derr != nil {
			return derr
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toResourceNodeDTO(rec))
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
