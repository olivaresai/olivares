// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Operator surface for groups (core envelope, NOT SCIM). A group is stored by the
// directory that provisioned it (SCIM, LDAP/AD or the operator).
//
// The group→role mapping is how group memberships land on the multi-tenant
// RBAC: a mapped group ELEVATES its members' effective role in the group's
// tenant (loadGrants folds it for session principals; a direct membership is
// still required — groups never grant base access). The mapping is deliberately
// out of the IdP's reach, so it lives here behind membership:read/write
// (admin-tier), ceiling-checked in core/auth.

// handleListGroups returns the tenant's provisioned groups with their mapped
// roles and member counts — the operator's view of what the IdP pushed and what
// each group confers (the effective-role provenance an admin needs when a
// member's direct role and acting role differ).
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	tenant := mc.Tenant
	groups, err := s.authr.SCIMListGroups(r.Context(), tenant)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// A workspace-confined caller reads the place of its own workspace only: the
	// id of another workspace is not its to learn.
	confinedWS, confined := mc.Principal.ConfinedWorkspaceIn(tenant)
	items := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		place := g.Group.WorkspaceID.String()
		if confined && g.Group.WorkspaceID != confinedWS {
			place = ""
		}
		items = append(items, map[string]any{
			"id":              g.Group.ID.String(),
			"display_name":    g.Group.DisplayName,
			"external_id":     g.Group.ExternalID,
			"provisioned_by":  g.Group.ProvisionedBy,
			"mapped_role":     g.Group.MappedRole,
			"parent_group_id": g.Group.ParentGroupID.String(),
			"workspace_id":    place,
			"members":         len(g.Members),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": items})
}

// handleSetGroupRole sets (or clears, with role "") the role a group's members
// are elevated to in the group's tenant. Ceiling-checked in core/auth against
// the group's STORED tenant, audited as scim.group.role.map.
func (s *Server) handleSetGroupRole(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
	var in struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	g, err := s.authr.ConfigureGroupRole(r.Context(), p, tenant, model.ID(chi.URLParam(r, "id")), in.Role)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidGroupRole) {
			s.badRequest(w, r, "unknown role")
			return
		}
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": g.ID.String(), "display_name": g.DisplayName, "mapped_role": g.MappedRole,
	})
}

// handleSetGroupParent nests (or, with parent_id "", un-nests) a group under
// another group of the SAME tenant — the S256 group hierarchy. A member of the
// child is then ALSO a member of the parent for authorization, so a scoped grant
// on the parent reaches the child's members. Reshaping the hierarchy needs
// OWNER (or superadmin) authority in the group's tenant (ConfigureGroupParent),
// is refused if it would create a cycle (ErrGroupCycle → 409), and is audited as
// scim.group.nest. Like the role mapping, this is operator-only — never SCIM.
// Group nesting structures an organization, so it is the Business department
// surface (DepartmentService): the Community build answers 501
// departments_unavailable once the route's own authentication and
// membership:write check pass, and a nesting already stored keeps applying to
// authorization and stays listed. The Business service gates and then calls
// ConfigureGroupParent.
func (s *Server) handleSetGroupParent(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.departments == nil {
		s.writeError(w, r, ErrDepartmentsUnavailable)
		return
	}
	p := mc.Principal
	tenant := mc.Tenant
	var in struct {
		ParentID string `json:"parent_id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	g, err := s.departments.SetGroupParent(r.Context(), p, tenant, model.ID(chi.URLParam(r, "id")), model.ID(in.ParentID))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": g.ID.String(), "display_name": g.DisplayName, "parent_group_id": g.ParentGroupID.String(),
	})
}

// handleSetGroupWorkspace places (or, with workspace_id "", takes out) a user
// group in a workspace of its tenant, so the workspace's contents list it. It is
// the Business department surface (DepartmentService): the Community build
// answers 501 departments_unavailable, and a place already stored stays listed.
// Like the role mapping, this is operator-only — never SCIM.
func (s *Server) handleSetGroupWorkspace(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.departments == nil {
		s.writeError(w, r, ErrDepartmentsUnavailable)
		return
	}
	var in struct {
		WorkspaceID *string `json:"workspace_id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if in.WorkspaceID == nil {
		s.badRequest(w, r, "workspace_id is required; use an empty string to clear the group placement")
		return
	}
	g, err := s.departments.SetGroupWorkspace(r.Context(), mc.Principal, mc.Tenant, model.ID(chi.URLParam(r, "id")), model.ID(*in.WorkspaceID))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": g.ID.String(), "display_name": g.DisplayName, "workspace_id": g.WorkspaceID.String(),
	})
}
