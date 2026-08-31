// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openai

// Wire types for the OpenAI Assistants API (v2) and extended Admin API surfaces.
// These map only the minimal-data fields the connector reads: ids, metadata,
// status, counts, timestamps — never prompts, message content, or file bytes.

// --- Assistants (GET /v1/assistants) ---

type assistantsResponse struct {
	Data    []assistantEntry `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

type assistantEntry struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Model        string            `json:"model"`
	Instructions string            `json:"-"` // deliberately unmapped — never read
	Tools        []assistantTool   `json:"tools"`
	Metadata     map[string]string `json:"metadata"`
	CreatedAt    int64             `json:"created_at"`
}

type assistantTool struct {
	Type string `json:"type"`
}

// --- Files (GET /v1/files) ---

type filesResponse struct {
	Data    []fileEntry `json:"data"`
	HasMore bool        `json:"has_more"`
}

type fileEntry struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	Bytes     int64  `json:"bytes"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// --- Vector Stores (GET /v1/vector_stores) ---

type vectorStoresResponse struct {
	Data    []vectorStoreEntry `json:"data"`
	HasMore bool               `json:"has_more"`
	LastID  string             `json:"last_id"`
}

type vectorStoreEntry struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	FileCounts vsFileCounts      `json:"file_counts"`
	UsageBytes int64             `json:"usage_bytes"`
	Metadata   map[string]string `json:"metadata"`
	CreatedAt  int64             `json:"created_at"`
	ExpiresAt  int64             `json:"expires_at"`
}

// vsFileCounts decodes OpenAI's vector-store file_counts object.
//
// The discriminator is NOT "identifier vs string": it is OUR NAME vs a FOREIGN
// PROTOCOL. Ours is normalized; theirs is respected even when the linter
// complains. The Canceled field below is ours, so it takes the US spelling
// misspell asks for; its json tag is OpenAI's own field name on the wire, keeps
// their British spelling, and is exempt — rewriting it would stop the response
// parsing, which is why the exemption is written down instead of assumed.
type vsFileCounts struct {
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	//nolint:misspell // `cancelled` is OpenAI's wire spelling, not ours: the tag is their protocol.
	Canceled int `json:"cancelled"`
	Total    int `json:"total"`
}

// --- Admin: Invites (GET /v1/organization/invites) ---

type invitesResponse struct {
	Data    []inviteEntry `json:"data"`
	HasMore bool          `json:"has_more"`
	LastID  string        `json:"last_id"`
}

type inviteEntry struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	InvitedAt int64  `json:"invited_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// --- Admin: Project Users (GET /v1/organization/projects/{id}/users) ---

type projectUsersResponse struct {
	Data    []projectUserEntry `json:"data"`
	HasMore bool               `json:"has_more"`
	LastID  string             `json:"last_id"`
}

type projectUserEntry struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	Name    string `json:"name"`
	AddedAt int64  `json:"added_at"`
}

// --- Admin: Project Service Accounts (GET /v1/organization/projects/{id}/service_accounts) ---

type projectServiceAccountsResponse struct {
	Data    []projectServiceAccountEntry `json:"data"`
	HasMore bool                         `json:"has_more"`
	LastID  string                       `json:"last_id"`
}

type projectServiceAccountEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"created_at"`
}

// --- Admin: Project API Keys (GET /v1/organization/projects/{id}/api_keys) ---

type projectAPIKeysResponse struct {
	Data    []projectAPIKeyEntry `json:"data"`
	HasMore bool                 `json:"has_more"`
	LastID  string               `json:"last_id"`
}

type projectAPIKeyEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	RedactedValue string `json:"redacted_value"`
	CreatedAt     int64  `json:"created_at"`
	Owner         struct {
		Type           string                            `json:"type"`
		User           *struct{ ID, Name, Email string } `json:"user,omitempty"`
		ServiceAccount *struct{ ID, Name string }        `json:"service_account,omitempty"`
	} `json:"owner"`
}
