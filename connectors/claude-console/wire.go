// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeconsole

// JSON wire shapes of the Anthropic Admin API organization-IAM responses this
// connector reads. Only the minimal-data fields the connector needs are mapped:
// member/invite/workspace ids, emails, roles and workspace membership — never a
// credential value (the Admin API returns no key secrets). All list endpoints use
// cursor pagination (has_more + last_id; the next page is after_id=<last_id>).

// usersResponse is GET /v1/organizations/users.
type usersResponse struct {
	Data    []userEntry `json:"data"`
	HasMore bool        `json:"has_more"`
	LastID  string      `json:"last_id"`
}

// userEntry is one org member. Role is the org-level role
// (user/claude_code_user/developer/billing/admin).
type userEntry struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// invitesResponse is GET /v1/organizations/invites.
type invitesResponse struct {
	Data    []inviteEntry `json:"data"`
	HasMore bool          `json:"has_more"`
	LastID  string        `json:"last_id"`
}

// inviteEntry is one pending invitation.
type inviteEntry struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// workspacesResponse is GET /v1/organizations/workspaces.
type workspacesResponse struct {
	Data    []workspaceEntry `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

// workspaceEntry is one workspace's inventory metadata.
type workspaceEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ArchivedAt string `json:"archived_at"`
}

// wsMembersResponse is GET /v1/organizations/workspaces/{id}/members.
type wsMembersResponse struct {
	Data    []wsMember `json:"data"`
	HasMore bool       `json:"has_more"`
	LastID  string     `json:"last_id"`
}

// wsMember is one workspace membership row (the user id + workspace role).
type wsMember struct {
	UserID        string `json:"user_id"`
	WorkspaceRole string `json:"workspace_role"`
}

// --- Claude Enterprise RBAC groups + custom roles (ce-user-management-2026-07-13
// beta; VERIFIED 2026-07-20 against platform.claude.com/docs/en/manage-claude/
// user-management + /api/admin-api). These live under the SAME /v1/organizations/
// Admin API but, unlike members/invites/workspaces, (a) require the
// anthropic-beta: ce-user-management-2026-07-13 header (omitting it → 404), (b) are
// Claude Enterprise-only, and (c) use OPAQUE-CURSOR pagination (page → next_page),
// not the members' after_id/last_id cursor. Custom roles are READ-ONLY via the API.

// rbacGroupsResponse is GET /v1/organizations/rbac_groups.
type rbacGroupsResponse struct {
	Data     []rbacGroup `json:"data"`
	HasMore  bool        `json:"has_more"`
	NextPage string      `json:"next_page"`
}

// rbacGroup is one Claude Enterprise RBAC group. source_type ∈ {direct, scim} is the
// governance prize: it reveals SCIM provisioning AT GROUP GRANULARITY, which the
// pre-beta Admin API could not observe at all. roles is the set of custom-role ids
// granted to the group (null — not [] — when role data is temporarily unavailable).
type rbacGroup struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SourceType string   `json:"source_type"`
	Roles      []string `json:"roles"`
}

// rbacGroupMembersResponse is GET /v1/organizations/rbac_groups/{group_id}/members.
type rbacGroupMembersResponse struct {
	Data     []rbacGroupMember `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage string            `json:"next_page"`
}

// rbacGroupMember is one group membership row (the org user id + email).
type rbacGroupMember struct {
	GroupID string `json:"group_id"`
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
}

// rbacRolesResponse is GET /v1/organizations/rbac_roles (read-only custom roles).
type rbacRolesResponse struct {
	Data     []rbacRole `json:"data"`
	HasMore  bool       `json:"has_more"`
	NextPage string     `json:"next_page"`
}

// rbacRole is one Claude Enterprise custom role (id + name only; permissions are a
// separate read this connector does not enumerate — the roster only needs the role
// as an assignable collection). Roles are API-read-only (authored in claude.ai).
type rbacRole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
