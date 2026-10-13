// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vault

import "time"

// Name is the connector's globally unique identifier.
const Name = "olivares.vault"

// Default configuration values.
const (
	defaultBaseURL = "https://127.0.0.1:8200"
	defaultTimeout = 30 * time.Second
)

// headerToken is the Vault authentication header. The token is the request
// credential, not a Bearer scheme.
const headerToken = "X-Vault-Token"

// headerNamespace selects a Vault Enterprise namespace; it is non-secret routing
// metadata, sent as a static header.
const headerNamespace = "X-Vault-Namespace"

// entityResponse is the relevant slice of GET /v1/identity/entity/id/{id}.
type entityResponse struct {
	Data struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Disabled bool     `json:"disabled"`
		Policies []string `json:"policies"`
		GroupIDs []string `json:"group_ids"`
	} `json:"data"`
}

// listResponse is the shape of Vault's LIST (?list=true) replies.
type listResponse struct {
	Data struct {
		Keys []string `json:"keys"`
	} `json:"data"`
}
