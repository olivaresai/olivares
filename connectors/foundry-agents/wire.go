// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package foundryagents

// This file holds the ARM and Foundry Agent Service JSON wire shapes the
// connector reads. Only fields mapped into the roster or posture are present.
// Microsoft is migrating the REST reference to ai.azure.com and the learn REST
// pages 404; the LIST response envelope is pinned from the SDK semantics
// (`ItemPaged[AgentDetails]`, params `kind`, `limit`, `order`, `before`), not
// from a captured wire sample. Decode DEFENSIVELY: accept the items array under
// `data` OR `value`; follow pagination only when the payload carries
// `has_more == true` AND a non-empty `last_id` (retry with `after={last_id}`),
// bounded by `max_pages`; terminate cleanly otherwise.

type armList[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"nextLink"`
}

// account is one Cognitive Services account. The Agent Service base is not the
// Cognitive Services inference endpoint used by azure-openai; unless the
// operator overrides data_plane_base, it is derived from the account name as
// https://{account}.services.ai.azure.com.
type account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type application struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Properties struct {
		DisplayName             string        `json:"displayName"`
		BaseURL                 string        `json:"baseUrl"`
		Agents                  []appAgentRef `json:"agents"`
		IsEnabled               bool          `json:"isEnabled"`
		ProvisioningState       string        `json:"provisioningState"`
		AgentIdentityBlueprint  identityLink  `json:"agentIdentityBlueprint"`
		DefaultInstanceIdentity identityLink  `json:"defaultInstanceIdentity"`
	} `json:"properties"`
}

type appAgentRef struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
}

// identityLink is string-open. tenantId is decoded for shape completeness but
// not emitted; the client/principal ids are the roster correlation anchors.
type identityLink struct {
	ClientID    string `json:"clientId"`
	PrincipalID string `json:"principalId"`
	TenantID    string `json:"tenantId"`
}

// agentDeployment deliberately omits authorizationPolicy and
// trafficRoutingPolicy: those properties are documented on ARM templates but
// their shape was not verified for this connector.
type agentDeployment struct {
	Name       string `json:"name"`
	Properties struct {
		State          string   `json:"state"`
		DeploymentType string   `json:"deploymentType"`
		Protocols      []string `json:"protocols"`
	} `json:"properties"`
}
