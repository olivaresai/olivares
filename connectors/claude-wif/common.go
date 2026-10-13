// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

// apiKey is one /v1/organizations/api_keys row (key inventory metadata, no secret).
type apiKey struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	WorkspaceID    string `json:"workspace_id"`
	Status         string `json:"status"` // active|inactive|archived
	PartialKeyHint string `json:"partial_key_hint"`
	CreatedAt      string `json:"created_at"`
}

// Anthropic Workload Identity Federation id prefixes (verified against the WIF
// reference: platform.claude.com/docs/en/manage-claude/wif-reference).
const (
	prefixIssuer         = "fdis_"   // federation issuer
	prefixRule           = "fdrl_"   // federation rule
	prefixServiceAccount = "svac_"   // service account
	prefixWorkspace      = "wrkspc_" // workspace
	workspaceDefault     = "default" // the org's default workspace (non-prefixed literal)
)

// The two oauth scopes a federation rule can grant (verified WIF reference; finer
// grained scopes are not currently available). An empty scope defaults to
// scopeWorkspaceDeveloper.
const (
	scopeWorkspaceDeveloper = "workspace:developer" // all non-administrative API endpoints in the workspace
	scopeManageTunnels      = "org:manage_tunnels"  // the MCP tunnels API
)

// Name is the connector's globally unique identifier.
const Name = "olivares.claude-wif"

const defaultBaseURL = "https://api.anthropic.com"

const defaultAnthropicVersion = "2023-06-01"

const kindAPIKey = "api_key"

// pathAPIKeys is shared by credential revocation and Business inventory.
const pathAPIKeys = "/v1/organizations/api_keys"

// kindServiceAccount is shared by the credential actuator kind check.
const kindServiceAccount = "service_account"
