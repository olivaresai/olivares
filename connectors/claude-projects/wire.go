// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeprojects

// wire.go holds the minimal JSON projection of the Anthropic Organization Admin API
// project endpoints (verified against platform.claude.com/docs/en/api/admin).
// Only the fields the connector maps are declared — membership/invitation detail
// beyond role and API-key material (the secret half) are never modeled.

// projectsResponse is the cursor-paginated list envelope for
// GET /v1/organizations/{organization_id}/projects.
type projectsResponse struct {
	Data    []project `json:"data"`
	HasMore bool      `json:"has_more"`
	FirstID string    `json:"first_id"`
	LastID  string    `json:"last_id"`
}

// project is one Organization Project. The Admin API exposes id/name/archived_at/
// created_at; knowledge-file and instruction content are NOT available through this
// endpoint (the connector documents this honestly as a coverage gap).
type project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	CreatedAt      string `json:"created_at"`
	ArchivedAt     string `json:"archived_at,omitempty"`
	OrganizationID string `json:"organization_id"`
}

// membersResponse is the cursor-paginated list envelope for
// GET /v1/organizations/{organization_id}/projects/{project_id}/members.
type membersResponse struct {
	Data    []member `json:"data"`
	HasMore bool     `json:"has_more"`
	FirstID string   `json:"first_id"`
	LastID  string   `json:"last_id"`
}

// member is one project membership record. The role discriminator maps to a
// PERMITTED access edge (project → member with mode).
type member struct {
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

// apiKeysResponse is the cursor-paginated list envelope for
// GET /v1/organizations/{organization_id}/projects/{project_id}/api_keys.
type apiKeysResponse struct {
	Data    []apiKey `json:"data"`
	HasMore bool     `json:"has_more"`
	FirstID string   `json:"first_id"`
	LastID  string   `json:"last_id"`
}

// apiKey is one project-scoped API key. The connector inventories the key's ID, name
// and status for governance; the secret half is never fetched or modeled.
type apiKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}
