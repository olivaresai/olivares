// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SCIM Groups inbound (RFC 7643 §4.2, RFC 7644) — from honest-501 to
// real. The handlers stay thin: decode/encode and SCIM-shaped errors here, all
// store semantics in auth.Authenticator.SCIM*Group* (core/auth/scim_groups.go),
// including the cross-tenant not-found rule, member skip-and-audit, and the
// role-ceiling guard on member adds to a role-mapped group. The group→role
// mapping itself is OPERATOR surface (handleListGroups / handleSetGroupRole
// below, core envelope, not SCIM): the IdP pushes rosters, never roles.

// scimGroupsURL returns the absolute URL of the Groups collection for
// meta.location.
func scimGroupsURL(r *http.Request) string {
	return scimBaseURL(r) + "/Groups"
}

// scimGroupAttr returns a getter over a group's filterable attributes for
// in-memory filter evaluation (the IdP pre-create existence check is
// `displayName eq "..."`).
func scimGroupAttr(g model.UserGroup) func(string) (string, bool) {
	return func(attr string) (string, bool) {
		switch attr {
		case "displayname":
			return g.DisplayName, true
		case "externalid":
			return g.ExternalID, g.ExternalID != ""
		case "id":
			return g.ID.String(), true
		default:
			return "", false
		}
	}
}

// scimGroupInput maps a decoded wire group to the auth input.
func scimGroupInput(in scim.InboundGroup) auth.SCIMGroupInput {
	members := make([]model.ID, 0, len(in.Members))
	for _, m := range in.Members {
		members = append(members, model.ID(m))
	}
	return auth.SCIMGroupInput{DisplayName: in.DisplayName, ExternalID: in.ExternalID, Members: members}
}

// writeSCIMGroupError maps the auth/store error set to SCIM responses; any
// unrecognized error is a logged 500.
func (s *Server) writeSCIMGroupError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeSCIMError(w, scim.NewError(http.StatusNotFound, "", "resource not found"))
	case errors.Is(err, store.ErrConflict):
		writeSCIMError(w, scim.NewError(http.StatusConflict, scim.TypeUniqueness, "a group with this displayName or externalId already exists"))
	case errors.Is(err, auth.ErrGroupVersionChanged):
		// The PATCH retry budget ran out against sustained concurrent writes: the
		// IdP's own retry re-reads fresh state.
		writeSCIMError(w, scim.NewError(http.StatusConflict, "", "the group changed concurrently; retry the request"))
	case errors.Is(err, auth.ErrInvalidScimGroup):
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "displayName is required"))
	case errors.Is(err, auth.ErrRoleCeiling):
		writeSCIMError(w, scim.NewError(http.StatusForbidden, "", "adding members to a role-mapped group requires a credential at or above the mapped role"))
	case errors.Is(err, store.ErrAuditSpoolFull):
		// ADR-0024 Q2 block mode: evidence capacity is exhausted; deny-closed and
		// retryable once the operator restores it. Same mapping as core/api.
		writeSCIMError(w, scim.NewError(http.StatusServiceUnavailable, "", "audit spool full"))
	default:
		s.scimInternal(w, r, err)
	}
}

func (s *Server) scimListGroups(w http.ResponseWriter, r *http.Request) {
	_, tenant, aerr := s.scimAuthz(r, "user:read")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	page := scim.ParsePage(r.URL.Query().Get("startIndex"), r.URL.Query().Get("count"), r.URL.Query().Has("count"))
	filterStr := strings.TrimSpace(r.URL.Query().Get("filter"))

	groups, err := s.authr.SCIMListGroups(r.Context(), tenant)
	if err != nil {
		s.scimInternal(w, r, err)
		return
	}
	matched := groups
	if filterStr != "" {
		f, ferr := scim.ParseFilter(filterStr)
		if ferr != nil {
			writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidFilter, ferr.Error()))
			return
		}
		matched = nil
		for _, g := range groups {
			if f.Match(scimGroupAttr(g.Group)) {
				matched = append(matched, g)
			}
		}
	}

	groupsURL, usersURL := scimGroupsURL(r), scimUsersURL(r)
	pageItems := scim.Slice(matched, page)
	resources := make([]map[string]any, 0, len(pageItems))
	for _, g := range pageItems {
		resources = append(resources, scim.EncodeGroup(g.Group, g.Members, groupsURL, usersURL))
	}
	writeSCIM(w, http.StatusOK, scim.ListResponse(len(matched), page, resources))
}

func (s *Server) scimCreateGroup(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	var body scim.GroupBodyType
	if err := decodeSCIMBody(w, r, &body); err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidSyntax, "invalid SCIM body"))
		return
	}
	in, err := scim.DecodeGroup(body)
	if err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, err.Error()))
		return
	}
	g, err := s.authr.SCIMCreateGroup(r.Context(), p, tenant, scimGroupInput(in))
	if err != nil {
		s.writeSCIMGroupError(w, r, err)
		return
	}
	groupsURL := scimGroupsURL(r)
	w.Header().Set("Location", groupsURL+"/"+g.Group.ID.String())
	writeSCIM(w, http.StatusCreated, scim.EncodeGroup(g.Group, g.Members, groupsURL, scimUsersURL(r)))
}

func (s *Server) scimGetGroup(w http.ResponseWriter, r *http.Request) {
	_, tenant, aerr := s.scimAuthz(r, "user:read")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	g, err := s.authr.SCIMGetGroup(r.Context(), tenant, model.ID(chi.URLParam(r, "id")))
	if err != nil {
		s.writeSCIMGroupError(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, scim.EncodeGroup(g.Group, g.Members, scimGroupsURL(r), scimUsersURL(r)))
}

func (s *Server) scimReplaceGroup(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	var body scim.GroupBodyType
	if err := decodeSCIMBody(w, r, &body); err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidSyntax, "invalid SCIM body"))
		return
	}
	// Okta sends a group RENAME as a PUT carrying the FULL members array: the
	// replace is attributes AND member set, never attributes alone. No version
	// guard: a PUT is the IdP's full intended state (last writer wins).
	in, err := scim.DecodeGroup(body)
	if err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, err.Error()))
		return
	}
	g, err := s.authr.SCIMReplaceGroup(r.Context(), p, tenant, id, scimGroupInput(in), 0)
	if err != nil {
		s.writeSCIMGroupError(w, r, err)
		return
	}
	writeSCIM(w, http.StatusOK, scim.EncodeGroup(g.Group, g.Members, scimGroupsURL(r), scimUsersURL(r)))
}

func (s *Server) scimPatchGroup(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	var body scim.PatchBody
	if err := decodeSCIMBody(w, r, &body); err != nil {
		writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidSyntax, "invalid PatchOp body"))
		return
	}
	// The read-fold-write spans two transactions, so the replace carries the
	// read row's version: a concurrent writer in the gap (an IdP retry racing
	// the original) triggers ErrGroupVersionChanged and the fold re-runs over
	// the fresh state instead of silently erasing the other write.
	var g auth.SCIMGroup
	for attempt := 0; ; attempt++ {
		cur, err := s.authr.SCIMGetGroup(r.Context(), tenant, id)
		if err != nil {
			s.writeSCIMGroupError(w, r, err)
			return
		}
		current := scim.InboundGroup{DisplayName: cur.Group.DisplayName, ExternalID: cur.Group.ExternalID}
		for _, m := range cur.Members {
			current.Members = append(current.Members, m.ID.String())
		}
		patched, perr := scim.ApplyGroupPatch(current, body)
		if perr != nil {
			switch {
			case errors.Is(perr, scim.ErrNoTarget):
				writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeNoTarget, perr.Error()))
			case errors.Is(perr, scim.ErrNestedGroup):
				writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, perr.Error()))
			case errors.Is(perr, scim.ErrUnsupportedFilter):
				writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidPath, perr.Error()))
			default:
				writeSCIMError(w, scim.NewError(http.StatusBadRequest, scim.TypeInvalidSyntax, perr.Error()))
			}
			return
		}
		g, err = s.authr.SCIMReplaceGroup(r.Context(), p, tenant, id, scimGroupInput(patched), cur.Group.Version)
		if errors.Is(err, auth.ErrGroupVersionChanged) && attempt < 2 {
			continue
		}
		if err != nil {
			s.writeSCIMGroupError(w, r, err)
			return
		}
		break
	}
	writeSCIM(w, http.StatusOK, scim.EncodeGroup(g.Group, g.Members, scimGroupsURL(r), scimUsersURL(r)))
}

func (s *Server) scimDeleteGroup(w http.ResponseWriter, r *http.Request) {
	p, tenant, aerr := s.scimAuthz(r, "user:write")
	if aerr != nil {
		writeSCIMError(w, *aerr)
		return
	}
	if err := s.authr.SCIMDeleteGroup(r.Context(), p, tenant, model.ID(chi.URLParam(r, "id"))); err != nil {
		s.writeSCIMGroupError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Operator surface (core envelope, NOT SCIM) -------------------------------
//
// The group→role mapping is how SCIM group memberships land on the multi-tenant
// RBAC: a mapped group ELEVATES its members' effective role in the group's
// tenant (loadGrants folds it for session principals; a direct membership is
// still required — groups never grant base access). The mapping is deliberately
// out of the IdP's reach, so it lives here behind membership:read/write
// (admin-tier), ceiling-checked in core/auth.

// handleListGroups returns the tenant's provisioned groups with their mapped
// roles and member counts — the operator's view of what the IdP pushed and what
// each group confers (the effective-role provenance an admin needs when a
// member's direct role and acting role differ).
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.authzTenant(w, r, "membership:read")
	if !ok {
		return
	}
	groups, err := s.authr.SCIMListGroups(r.Context(), tenant)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		items = append(items, map[string]any{
			"id":              g.Group.ID.String(),
			"display_name":    g.Group.DisplayName,
			"external_id":     g.Group.ExternalID,
			"mapped_role":     g.Group.MappedRole,
			"parent_group_id": g.Group.ParentGroupID.String(),
			"members":         len(g.Members),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": items})
}

// handleSetGroupRole sets (or clears, with role "") the role a group's members
// are elevated to in the group's tenant. Ceiling-checked in core/auth against
// the group's STORED tenant, audited as scim.group.role.map.
func (s *Server) handleSetGroupRole(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "membership:write")
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
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
func (s *Server) handleSetGroupParent(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "membership:write")
	if !ok {
		return
	}
	var in struct {
		ParentID string `json:"parent_id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	g, err := s.authr.ConfigureGroupParent(r.Context(), p, tenant, model.ID(chi.URLParam(r, "id")), model.ID(in.ParentID))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": g.ID.String(), "display_name": g.DisplayName, "parent_group_id": g.ParentGroupID.String(),
	})
}
