// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// rosterMemberDTO is the wire shape of one row of the console members grid:
// a user plus the tenant-resident facts an operator manages them by (effective
// role, workspace scoping, directory groups). Email and display name ARE returned —
// showing who a member is, to an operator already gated on user:read, is the point
// of a roster — but no secret ever is (no password hash, no token). There is
// deliberately NO "last access" field: the platform tracks no per-user login
// timestamp, so the roster never fabricates one.
type rosterMemberDTO struct {
	UserID       string   `json:"user_id"`
	Email        string   `json:"email"`
	DisplayName  string   `json:"display_name,omitempty"`
	Status       string   `json:"status"`
	ExternalID   string   `json:"external_id,omitempty"`
	SSOOnly      bool     `json:"sso_only"`
	Role         string   `json:"role"`
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	Groups       []string `json:"groups,omitempty"`
}

func toRosterMemberDTO(m auth.RosterMember) rosterMemberDTO {
	d := rosterMemberDTO{
		UserID:      m.User.ID.String(),
		Email:       m.User.Email,
		DisplayName: m.User.DisplayName,
		Status:      string(m.User.Status),
		ExternalID:  m.User.ExternalID,
		// A federated (SSO-only) account carries no password hash. This is a boolean
		// derived from absence — never the hash itself.
		SSOOnly: m.User.PasswordHash == "",
		Role:    m.Role,
	}
	for _, wsID := range m.WorkspaceIDs {
		d.WorkspaceIDs = append(d.WorkspaceIDs, wsID.String())
	}
	d.Groups = append(d.Groups, m.Groups...)
	return d
}

// handleListMembers serves the resolved tenant's member roster for the console
// members grid: every user with a membership in the tenant, enriched with
// their effective role, workspace scoping and directory groups. Read-tier
// (user:read) and tenant-scoped — authzTenant resolves and authorizes the caller's
// single bound tenant, and TenantRoster returns ONLY that tenant's members (the
// isolation is the target_tenant_id filter, deny-closed by construction, so no
// cross-tenant roster is ever reachable). The set is bounded and returned complete
// (no paging), mirroring the SCIM member set.
func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "user:read")
	if !ok {
		return
	}
	roster, err := s.authr.TenantRoster(r.Context(), tenant)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// a workspace-CONFINED caller sees only the members OF its workspace (row-level
	// filter) — a confined admin must not enumerate the tenant's full, cross-workspace user
	// set (reconnaissance-sensitive PII). A tenant-wide caller (ok=false) sees the full
	// roster exactly as before.
	if confinedWS, confined := p.ConfinedWorkspaceIn(tenant); confined {
		roster = filterRosterToWorkspace(roster, confinedWS)
	}
	out := listResponse[rosterMemberDTO]{Items: make([]rosterMemberDTO, 0, len(roster))}
	for _, m := range roster {
		out.Items = append(out.Items, toRosterMemberDTO(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// filterRosterToWorkspace keeps only roster members whose membership is scoped to ws. A
// workspace-confined operator sees the members OF its workspace, never the tenant's
// full set; a tenant-wide member (WorkspaceIDs empty) is deliberately NOT shown to a confined
// operator (it belongs to no single workspace, so surfacing it would leak cross-workspace).
func filterRosterToWorkspace(roster []auth.RosterMember, ws model.ID) []auth.RosterMember {
	out := make([]auth.RosterMember, 0, len(roster))
	for _, m := range roster {
		for _, id := range m.WorkspaceIDs {
			if id == ws {
				out = append(out, m)
				break
			}
		}
	}
	return out
}
