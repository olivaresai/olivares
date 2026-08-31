// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudecompliance

// wire.go holds the minimal JSON projection of the Anthropic Compliance API Activity
// Feed response (verified against platform.claude.com/docs/en/api/compliance). Only
// the fields the connector maps are declared — chat/message CONTENT is never modeled
// or fetched (this connector reads the activity LOG, not user data).

// activitiesResponse is the cursor-paginated list envelope: data + has_more, with
// first_id/last_id as the cursor (feed last_id back as after_id to advance).
type activitiesResponse struct {
	Data    []activity `json:"data"`
	HasMore bool       `json:"has_more"`
	FirstID string     `json:"first_id"`
	LastID  string     `json:"last_id"`
}

// activity is one Activity Feed record. organization_id/uuid are null for events not
// tied to an org (sign-in/out, Compliance API calls). type is the activity type
// (e.g. "claude_chat_created"); chat/project ids are top-level type-specific fields.
type activity struct {
	ID               string `json:"id"`
	CreatedAt        string `json:"created_at"`
	OrganizationID   string `json:"organization_id"`
	OrganizationUUID string `json:"organization_uuid"`
	Type             string `json:"type"`
	Actor            actor  `json:"actor"`
	// Type-specific fields (present on the relevant activity types).
	ClaudeChatID    string `json:"claude_chat_id,omitempty"`
	ClaudeProjectID string `json:"claude_project_id,omitempty"`
	Filename        string `json:"filename,omitempty"`
}

// actor is the discriminated union of who performed the activity. type is the
// discriminator (user_actor / api_actor / admin_api_key_actor /
// unauthenticated_user_actor / anthropic_actor / scim_directory_sync_actor / ...);
// ip_address, user_agent, email_address, and actor-specific identifiers are hashed,
// never surfaced. Anthropic's forward-compatible rule is to pass through unrecognized
// type and actor.type values (VERIFIED 2026-07-03).
type actor struct {
	Type                        string `json:"type"`
	IPAddress                   string `json:"ip_address,omitempty"`
	UserAgent                   string `json:"user_agent,omitempty"`
	EmailAddress                string `json:"email_address,omitempty"`
	UserID                      string `json:"user_id,omitempty"`
	APIKeyID                    string `json:"api_key_id,omitempty"`
	AdminAPIKeyID               string `json:"admin_api_key_id"`
	UnauthenticatedEmailAddress string `json:"unauthenticated_email_address"`
	WorkOSEventID               string `json:"workos_event_id"`
	DirectoryID                 string `json:"directory_id"`
	IDPConnectionType           string `json:"idp_connection_type"`
}
