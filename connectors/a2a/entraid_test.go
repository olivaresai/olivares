// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import "testing"

func TestCrosswalkToEntra(t *testing.T) {
	info := AgentIdentityInfo{
		IdentityRef: "agent:claude-prod",
		DisplayName: "Claude Production Agent",
		SponsorRef:  "human:alice@acme.com",
		Source:      "spiffe",
		Tenant:      "acme-corp",
		Skills:      []string{"code-review", "deploy"},
	}
	eid := CrosswalkToEntra(info)
	if eid.AppID != "agent:claude-prod" {
		t.Fatalf("AppID = %q, want agent:claude-prod", eid.AppID)
	}
	if eid.ManagedBy != "human:alice@acme.com" {
		t.Fatalf("ManagedBy = %q, want human:alice@acme.com", eid.ManagedBy)
	}
	if eid.TrustLevel != "verified" {
		t.Fatalf("TrustLevel = %q, want verified", eid.TrustLevel)
	}
	if eid.OwnerOrganization != "acme-corp" {
		t.Fatalf("OwnerOrganization = %q, want acme-corp", eid.OwnerOrganization)
	}
}

func TestCrosswalkToEntraBlockedAgent(t *testing.T) {
	info := AgentIdentityInfo{
		IdentityRef: "agent:claude-prod",
		Blocked:     true,
	}
	eid := CrosswalkToEntra(info)
	if eid.TrustLevel != "blocked" {
		t.Fatalf("blocked agent TrustLevel = %q, want blocked", eid.TrustLevel)
	}
}

func TestCrosswalkToEntraOrphanedAgent(t *testing.T) {
	info := AgentIdentityInfo{
		IdentityRef: "agent:claude-prod",
		Orphaned:    true,
	}
	eid := CrosswalkToEntra(info)
	if eid.TrustLevel != "blocked" {
		t.Fatalf("orphaned agent TrustLevel = %q, want blocked", eid.TrustLevel)
	}
}

func TestCrosswalkFromCard(t *testing.T) {
	card := AgentCard{
		Name:        "Test Agent",
		Description: "A test agent",
		Version:     "1.0",
		SupportedInterfaces: []AgentInterface{
			{URL: "https://agent.example.com/a2a"},
		},
		Skills: []agentSkill{
			{Name: "code-review"},
			{Name: "testing"},
		},
	}
	info := CrosswalkFromCard(card, "verified")
	if info.IdentityRef != "a2a:https://agent.example.com/a2a" {
		t.Fatalf("IdentityRef = %q", info.IdentityRef)
	}
	if info.DisplayName != "Test Agent" {
		t.Fatalf("DisplayName = %q", info.DisplayName)
	}
	if len(info.Skills) != 2 {
		t.Fatalf("Skills = %v, want 2", info.Skills)
	}
}

func TestCrosswalkFromCardFallbackURL(t *testing.T) {
	// Pre-1.0 card: no supportedInterfaces, only legacy URL field.
	card := AgentCard{
		Name: "Legacy Agent",
		URL:  "https://legacy.example.com/a2a",
	}
	info := CrosswalkFromCard(card, "self-asserted")
	if info.IdentityRef != "a2a:https://legacy.example.com/a2a" {
		t.Fatalf("IdentityRef = %q, want a2a:https://legacy.example.com/a2a", info.IdentityRef)
	}
}

func TestCrosswalkFromCardFallbackName(t *testing.T) {
	// No URL, no interfaces: fall back to card name.
	card := AgentCard{
		Name: "NameOnly Agent",
	}
	info := CrosswalkFromCard(card, "unverified")
	if info.IdentityRef != "a2a:NameOnly Agent" {
		t.Fatalf("IdentityRef = %q, want a2a:NameOnly Agent", info.IdentityRef)
	}
}

func TestCrosswalkFromCardProvider(t *testing.T) {
	prov := &agentProvider{Organization: "example-corp"}
	card := AgentCard{
		Name:     "Org Agent",
		Provider: prov,
		SupportedInterfaces: []AgentInterface{
			{URL: "https://org.example.com/a2a"},
		},
	}
	info := CrosswalkFromCard(card, "verified")
	if info.Tenant != "example-corp" {
		t.Fatalf("Tenant = %q, want example-corp", info.Tenant)
	}
	if info.Source != "a2a" {
		t.Fatalf("Source = %q, want a2a", info.Source)
	}
}

func TestCrosswalkFromCardTrustLevel(t *testing.T) {
	// Test that trustLevel flows through CrosswalkFromCard.
	card := AgentCard{
		Name:        "Test Agent",
		Description: "A test agent",
		Version:     "1.0",
		SupportedInterfaces: []AgentInterface{
			{URL: "https://agent.example.com/a2a"},
		},
	}
	tests := []string{"verified", "self-asserted", "unsigned", "unverified"}
	for _, trustLevel := range tests {
		info := CrosswalkFromCard(card, trustLevel)
		if info.TrustLevel != trustLevel {
			t.Fatalf("CrosswalkFromCard trustLevel = %q, want %q", info.TrustLevel, trustLevel)
		}
	}
}

func TestCrosswalkFromCardToEntraTrustLevel(t *testing.T) {
	// Test the full flow: card -> info -> EntraAgentID with explicit trustLevel.
	card := AgentCard{
		Name:        "Test Agent",
		Description: "A test agent",
		Version:     "1.0",
		SupportedInterfaces: []AgentInterface{
			{URL: "https://agent.example.com/a2a"},
		},
	}
	info := CrosswalkFromCard(card, "self-asserted")
	eid := CrosswalkToEntra(info)
	if eid.TrustLevel != "self-asserted" {
		t.Fatalf("Entra TrustLevel = %q, want self-asserted", eid.TrustLevel)
	}
}

func TestCrosswalkToEntraExplicitTrustLevelOverridesBlocked(t *testing.T) {
	// Explicit TrustLevel takes precedence even if Blocked is true.
	info := AgentIdentityInfo{
		IdentityRef: "agent:blocked-but-explicit",
		TrustLevel:  "verified",
		Blocked:     true,
	}
	eid := CrosswalkToEntra(info)
	if eid.TrustLevel != "verified" {
		t.Fatalf("Explicit TrustLevel = %q, want verified (should override Blocked)", eid.TrustLevel)
	}
}

func TestCrosswalkToEntraCapabilities(t *testing.T) {
	info := AgentIdentityInfo{
		IdentityRef: "agent:x",
		Skills:      []string{"read", "write", "execute"},
	}
	eid := CrosswalkToEntra(info)
	if len(eid.Capabilities) != 3 {
		t.Fatalf("Capabilities = %v, want 3", eid.Capabilities)
	}
	if eid.Capabilities[0] != "read" {
		t.Fatalf("Capabilities[0] = %q, want read", eid.Capabilities[0])
	}
}

func TestCrosswalkToEntraDisplayName(t *testing.T) {
	info := AgentIdentityInfo{
		IdentityRef: "agent:y",
		DisplayName: "My Agent",
	}
	eid := CrosswalkToEntra(info)
	if eid.DisplayName != "My Agent" {
		t.Fatalf("DisplayName = %q, want My Agent", eid.DisplayName)
	}
}
