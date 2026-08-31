// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

// Entra Agent ID crosswalk — maps the Olivares agent identity model to
// Microsoft Entra Agent ID fields for interoperability. This is an EXPORT
// function: given an Olivares agent identity, produce the Entra representation.
// Parse/verify, not enforcement — we do not control Entra infrastructure.
//
// Entra Agent ID fields (verified against Microsoft public documentation
// 2026-06 — Agent 365 / Entra Agent Identity Management preview):
//   - appId:              unique agent application identifier
//   - displayName:        human-readable agent name
//   - ownerOrganization:  the organization that owns/published the agent
//   - managedBy:          the human identity responsible (sponsor)
//   - capabilities:       what the agent can do (from A2A skills)
//   - trustLevel:         identity verification status
//
// HONESTY (docs/SECURITY-HARDENING.md): this crosswalk targets Entra Agent ID as documented
// in public preview. Microsoft may change the schema. The mapping is
// best-effort interop, not conformance.

// EntraAgentID is the Olivares representation of an Entra Agent Identity,
// suitable for export/sync.
type EntraAgentID struct {
	// AppID is the unique agent identifier (maps to identity_ref / external_id).
	AppID string `json:"appId"`
	// DisplayName is the human-readable agent name.
	DisplayName string `json:"displayName"`
	// OwnerOrganization is the tenant/org that owns the agent.
	OwnerOrganization string `json:"ownerOrganization,omitempty"`
	// ManagedBy is the human sponsor's identity (maps to sponsor_ref).
	ManagedBy string `json:"managedBy"`
	// Capabilities lists what the agent can do (from A2A skills or lifecycle).
	Capabilities []string `json:"capabilities,omitempty"`
	// TrustLevel maps to the verification status of the agent's identity.
	TrustLevel string `json:"trustLevel"`
}

// AgentIdentityInfo is a provider-neutral representation of an Olivares agent
// identity, used as input to the crosswalk functions. This avoids importing
// /core or /modules (Apache-2.0 boundary).
type AgentIdentityInfo struct {
	// IdentityRef is the external_id / identity_ref (convergence anchor).
	IdentityRef string
	// DisplayName is the agent's human-readable name.
	DisplayName string
	// SponsorRef is the human sponsor's external_id.
	SponsorRef string
	// Source is the identity provider (spiffe, okta, etc.).
	Source string
	// Tenant is the tenant/organization context.
	Tenant string
	// Skills are the agent's declared capabilities (from A2A card or config).
	Skills []string
	// TrustLevel is the identity verification status (verified/self-asserted/unsigned/unverified).
	TrustLevel string
	// Blocked reports whether the agent is currently blocked by lifecycle policy.
	Blocked bool
	// Orphaned reports whether the agent's sponsor is revoked/missing.
	Orphaned bool
}

// CrosswalkToEntra maps an Olivares agent identity to Entra Agent ID fields.
// If TrustLevel is explicitly set in the info, it takes precedence. Otherwise,
// the trust level is computed from the agent's lifecycle state (blocked/orphaned).
func CrosswalkToEntra(info AgentIdentityInfo) EntraAgentID {
	trust := info.TrustLevel
	if trust == "" {
		trust = "verified"
		if info.Blocked || info.Orphaned {
			trust = "blocked"
		}
	}
	return EntraAgentID{
		AppID:             info.IdentityRef,
		DisplayName:       info.DisplayName,
		OwnerOrganization: info.Tenant,
		ManagedBy:         info.SponsorRef,
		Capabilities:      info.Skills,
		TrustLevel:        trust,
	}
}

// CrosswalkFromCard maps an A2A Agent Card to an AgentIdentityInfo suitable for
// lifecycle registration or Entra sync. The trustLevel parameter maps the card's
// verification result (verified/self-asserted/unsigned/unverified) to the info.
func CrosswalkFromCard(card AgentCard, trustLevel string) AgentIdentityInfo {
	skills := make([]string, 0, len(card.Skills))
	for _, s := range card.Skills {
		if s.Name != "" {
			skills = append(skills, s.Name)
		}
	}
	provider := ""
	if card.Provider != nil {
		provider = card.Provider.Organization
	}
	return AgentIdentityInfo{
		IdentityRef: agentCardIdentity(card),
		DisplayName: card.Name,
		Source:      "a2a",
		Tenant:      provider,
		Skills:      skills,
		TrustLevel:  trustLevel,
	}
}

// agentCardIdentity derives a stable identity reference from an A2A Agent Card.
// Uses the first supported interface URL as the canonical identifier (the card's
// reachable endpoint is its identity anchor in A2A).
func agentCardIdentity(card AgentCard) string {
	if len(card.SupportedInterfaces) > 0 && card.SupportedInterfaces[0].URL != "" {
		return "a2a:" + card.SupportedInterfaces[0].URL
	}
	if card.URL != "" {
		return "a2a:" + card.URL
	}
	return "a2a:" + card.Name
}
