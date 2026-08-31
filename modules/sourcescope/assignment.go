// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// connector-to-workspace assignment: the operator (or workspace admin with
// write tier) assigns a deployment-global connector to a workspace. Once ANY
// assignment row exists for a connector, only the assigned workspaces see it
// (deny-closed); a connector with NO assignments remains globally visible
// (back-compat). This is the allocation layer — the connector definition itself
// stays superadmin-managed, but workspace admins choose which ones their workspace
// gets.

// assignmentDTO is the wire shape of a connector→workspace assignment.
type assignmentDTO struct {
	ID            string `json:"id,omitempty"`
	ConnectorName string `json:"connector_name"`
	WorkspaceRef  string `json:"workspace_ref"`
	Mode          string `json:"mode,omitempty"`
	Enabled       bool   `json:"enabled"`
	Note          string `json:"note,omitempty"`
}

func (d *assignmentDTO) validate() string {
	d.ConnectorName = strings.TrimSpace(d.ConnectorName)
	if d.ConnectorName == "" {
		return "connector_name is required"
	}
	d.WorkspaceRef = strings.TrimSpace(d.WorkspaceRef)
	if d.WorkspaceRef == "" {
		return "workspace_ref is required"
	}
	d.Mode = normalizeAssignmentMode(d.Mode)
	if len(d.Note) > maxNoteLen {
		return "note is too long"
	}
	return ""
}

func (d assignmentDTO) fields(wsID model.ID, createdBy string) model.Record {
	return model.Record{
		colAssignConnector: d.ConnectorName,
		colAssignWorkspace: d.WorkspaceRef,
		colAssignWsID:      wsID.String(),
		colAssignMode:      d.Mode,
		colAssignEnabled:   d.Enabled,
		colAssignCreatedBy: createdBy,
		colAssignNote:      d.Note,
	}
}

func toAssignmentDTO(rec model.Record) assignmentDTO {
	return assignmentDTO{
		ID:            rec.String(model.ColID),
		ConnectorName: rec.String(colAssignConnector),
		WorkspaceRef:  rec.String(colAssignWorkspace),
		Mode:          normalizeAssignmentMode(rec.String(colAssignMode)),
		Enabled:       rec.Bool(colAssignEnabled),
		Note:          rec.String(colAssignNote),
	}
}

func normalizeAssignmentMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "r":
		return "r"
	default:
		return "rw"
	}
}

// handleListAssignments lists connector→workspace assignments, optionally filtered by
// ?connector_name / ?workspace_ref.
func (m *Module) handleListAssignments(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	for _, f := range []struct{ param, col string }{
		{"connector_name", colAssignConnector}, {"workspace_ref", colAssignWorkspace},
	} {
		if v := r.URL.Query().Get(f.param); v != "" {
			q.Filters = append(q.Filters, eq(f.col, v))
		}
	}
	out := listResponse[assignmentDTO]{Items: []assignmentDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(assignmentKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toAssignmentDTO(rec))
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

// handleCreateAssignment assigns a global connector to a workspace.
//
// An ENABLED assignment added to an already-assigned connector admits a workspace that
// could not reach it a moment earlier, so it is proposed rather than applied (202), exactly
// like a relaxing binding create. The FIRST assignment — the one that takes the connector
// from globally visible to confined — still applies immediately.
func (m *Module) handleCreateAssignment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in assignmentDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out     assignmentDTO
		pending *postureRequestDTO
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// Everything that can REFUSE the write runs before the classification, the ordering
		// handleCreateBinding documents: whatever a gated create defers to approval, the
		// approver meets as an error on a request with no route to withdraw it. So the
		// workspace is resolved here (an unknown slug is deny-closed at PROPOSE time) and the
		// duplicate is refused here, keeping the 409 this API has always returned.
		wsID, _, err := resolveScope(r.Context(), sc, scopeWorkspace, &in.WorkspaceRef)
		if err != nil {
			return err
		}
		otherRows, duplicate, err := countOtherAssignments(r.Context(), sc, in.ConnectorName, "", in.WorkspaceRef)
		if err != nil {
			return err
		}
		if duplicate {
			return store.ErrConflict
		}
		if relaxing, reason := classifyAssignmentCreate(in, otherRows); relaxing {
			pr, perr := m.createAssignmentPostureRequest(r.Context(), sc, mc, postureOpAssignCreate, "", reason, &in)
			if perr != nil {
				return perr
			}
			pending = &pr
			return nil
		}
		repo, err := sc.Ext(assignmentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.fields(wsID, mc.Principal.Actor()))
		if err != nil {
			return err
		}
		out = toAssignmentDTO(rec)
		return auditAssignment(r.Context(), sc, mc, "create", in)
	})
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if pending != nil {
		writeJSON(w, http.StatusAccepted, *pending) // relaxation awaits a second, distinct approver
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleGetAssignment returns one assignment.
func (m *Module) handleGetAssignment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out assignmentDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(assignmentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toAssignmentDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateAssignment updates an assignment (enabled, note). The connector_name
// and workspace_ref are immutable — they form the unique key.
func (m *Module) handleUpdateAssignment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in assignmentDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	var (
		out     assignmentDTO
		pending *postureRequestDTO
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(assignmentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		old := toAssignmentDTO(rec) // the posture BEFORE the change (relax classification)
		in.ConnectorName = rec.String(colAssignConnector)
		in.WorkspaceRef = rec.String(colAssignWorkspace)
		if strings.TrimSpace(in.Mode) == "" {
			in.Mode = rec.String(colAssignMode)
		}
		if msg := in.validate(); msg != "" {
			return validationError(msg)
		}
		// enabling a parked assignment admits a workspace — never one actor's call.
		if relaxing, reason := classifyAssignmentUpdate(old, in); relaxing {
			pr, perr := m.createAssignmentPostureRequest(r.Context(), sc, mc, postureOpAssignUpdate, id.String(), reason, &in)
			if perr != nil {
				return perr
			}
			pending = &pr
			return nil
		}
		wsID := model.ID(rec.String(colAssignWsID))
		for k, v := range in.fields(wsID, rec.String(colAssignCreatedBy)) {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toAssignmentDTO(rec)
		return auditAssignment(r.Context(), sc, mc, "update", in)
	})
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if pending != nil {
		writeJSON(w, http.StatusAccepted, *pending) // relaxation awaits a second, distinct approver
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteAssignment removes a connector→workspace assignment.
//
// Deleting the LAST assignment does not shrink the connector's audience, it flips the
// connector back to visible from EVERY workspace (ConnectorAssigned, :291-293). That is the
// same relaxation handleDisableScoping always dual-controls and classifyDelete gates on the
// binding surface; it used to be a 204 with one actor.
func (m *Module) handleDeleteAssignment(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var pending *postureRequestDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(assignmentKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		snap := toAssignmentDTO(rec)
		otherRows, _, err := countOtherAssignments(r.Context(), sc, snap.ConnectorName, id.String(), "")
		if err != nil {
			return err
		}
		if relaxing, reason := classifyAssignmentDelete(snap, otherRows); relaxing {
			pr, perr := m.createAssignmentPostureRequest(r.Context(), sc, mc, postureOpAssignDelete, id.String(), reason, &snap)
			if perr != nil {
				return perr
			}
			pending = &pr
			return nil
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditAssignment(r.Context(), sc, mc, "delete", snap)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if pending != nil {
		writeJSON(w, http.StatusAccepted, *pending) // relaxation awaits a second, distinct approver
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// auditAssignment appends a self-audit event for a connector assignment change.
func auditAssignment(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, a assignmentDTO) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "sourcescope.connector_assignment." + verb,
		TargetKind: assignmentKind,
		Meta: map[string]any{
			"connector_name": a.ConnectorName,
			"workspace_ref":  a.WorkspaceRef,
			"mode":           a.Mode,
			"enabled":        a.Enabled,
		},
	})
	return err
}

// ConnectorAssigned reports whether any enabled assignment rows exist for a connector
// in the given workspace. Used by the resolver to gate visibility. If NO assignment
// rows exist at all for the connector, it returns (true, nil) — unassigned connectors
// are globally visible (back-compat).
func ConnectorAssigned(ctx context.Context, sc store.Scope, connectorName, workspaceSlug string) (bool, error) {
	allAssign, err := allExt(ctx, sc, assignmentKind, eq(colAssignConnector, connectorName))
	if err != nil {
		return false, err
	}
	if len(allAssign) == 0 {
		return true, nil
	}
	for _, rec := range allAssign {
		if rec.Bool(colAssignEnabled) && rec.String(colAssignWorkspace) == workspaceSlug {
			return true, nil
		}
	}
	return false, nil
}

// ListAssignedWorkspaces returns the workspace slugs a connector is ENABLED for.
//
// An empty slice does NOT mean the connector is globally visible, and the doc comment said
// it did until. It conflates two states ConnectorAssigned holds apart: no rows at all
// (global) and rows that are all DISABLED (denied everywhere), because the filter here is
// `enabled` while the global test up there is `len(allAssign) == 0`. Reading "[] ⇒ global"
// off this function inverts the answer for the second state — for a caller outside this
// package, that is a deny read as an allow.
//
// It has no caller in this repository today; it is exported module API, so the correction is
// to the contract, not to a live path. A caller that needs "is this connector confined"
// must ask ConnectorAssigned, which is the function the resolver itself uses.
func ListAssignedWorkspaces(ctx context.Context, sc store.Scope, connectorName string) ([]string, error) {
	recs, err := allExt(ctx, sc, assignmentKind, eq(colAssignConnector, connectorName))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		if rec.Bool(colAssignEnabled) {
			out = append(out, rec.String(colAssignWorkspace))
		}
	}
	return out, nil
}
