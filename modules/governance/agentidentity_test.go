// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/modules/governance"
)

func TestAgentRegistrationRequiresSponsor(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)

	// Seed a human identity as potential sponsor.
	h.seedIdentity(tenant, "human:alice", "user", "okta", "human", false)

	// Agent registration without sponsor_ref → 400 (deny-closed).
	r := h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
	}, hdr)
	if r.code != http.StatusBadRequest {
		t.Fatalf("agent without sponsor should be 400, got %d %s", r.code, r.raw)
	}

	// Agent registration with non-human sponsor → 400.
	h.seedIdentity(tenant, "vault:approle:ci", "vault_entity", "vault", "nhi", false)
	r = h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
		"sponsor_ref":  "vault:approle:ci",
	}, hdr)
	if r.code != http.StatusBadRequest {
		t.Fatalf("agent with NHI sponsor should be 400, got %d %s", r.code, r.raw)
	}

	// Agent registration with human sponsor → 201.
	r = h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
		"sponsor_ref":  "human:alice",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("agent with human sponsor should be 201, got %d %s", r.code, r.raw)
	}

	// Verify lifecycle row has kind=agent and the sponsor set.
	r = h.do("GET", "/v1/m/governance/nhi/agent:claude-prod", tok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get agent nhi = %d %s", r.code, r.raw)
	}
	if r.body["kind"] != "agent" {
		t.Fatalf("kind should be agent, got %v", r.body["kind"])
	}
	if r.body["sponsor_ref"] != "human:alice" {
		t.Fatalf("sponsor_ref should be human:alice, got %v", r.body["sponsor_ref"])
	}
}

func TestAgentRegistrationDuplicateReturnsConflict(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)

	h.seedIdentity(tenant, "human:alice", "user", "okta", "human", false)

	// First registration succeeds.
	r := h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
		"sponsor_ref":  "human:alice",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("first register = %d %s", r.code, r.raw)
	}

	// Duplicate registration returns 409.
	r = h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
		"sponsor_ref":  "human:alice",
	}, hdr)
	if r.code != http.StatusConflict {
		t.Fatalf("duplicate register should be 409, got %d %s", r.code, r.raw)
	}
}

func TestAgentSponsorChangeMover(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)

	h.seedIdentity(tenant, "human:alice", "user", "okta", "human", false)
	h.seedIdentity(tenant, "human:bob", "user", "okta", "human", false)

	// Register agent with alice as sponsor.
	r := h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
		"sponsor_ref":  "human:alice",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}

	// Mover: change sponsor to bob (via existing PUT /nhi/{ref}/ownership).
	r = h.do("PUT", "/v1/m/governance/nhi/agent:claude-prod/ownership", tok,
		map[string]any{"sponsor_ref": "human:bob"}, hdr)
	if r.code != http.StatusNoContent {
		t.Fatalf("change sponsor = %d %s", r.code, r.raw)
	}

	// Cannot clear sponsor on an agent (deny-closed).
	r = h.do("PUT", "/v1/m/governance/nhi/agent:claude-prod/ownership", tok,
		map[string]any{"sponsor_ref": ""}, hdr)
	if r.code != http.StatusBadRequest {
		t.Fatalf("clear sponsor on agent should be 400, got %d %s", r.code, r.raw)
	}
}

func TestAgentSponsorRevokedOrphansAgent(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)

	h.seedIdentity(tenant, "human:alice", "user", "okta", "human", false)

	// Register agent with alice.
	r := h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
		"sponsor_ref":  "human:alice",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}

	// Disable the sponsor (simulate directory revocation).
	h.setIdentityDisabled(tenant, "human:alice", true)

	// Trigger sweep.
	r = h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr)
	if r.code != http.StatusNoContent && r.code != http.StatusOK {
		t.Fatalf("sweep = %d %s", r.code, r.raw)
	}

	// Agent should be orphaned.
	r = h.do("GET", "/v1/m/governance/nhi/agent:claude-prod", tok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get agent = %d %s", r.code, r.raw)
	}
	if r.body["orphaned"] != true {
		t.Fatalf("agent should be orphaned after sponsor disabled, got %v", r.body["orphaned"])
	}
}

func TestAgentLeaverUsesExistingOffboard(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)
	gate := &fakeGate{status: governance.GateStatusApproved}
	h.gov.UseLifecycleGate(gate)

	h.seedIdentity(tenant, "human:alice", "user", "okta", "human", false)

	// Register agent.
	r := h.do("POST", "/v1/m/governance/agents", tok, map[string]any{
		"identity_ref": "agent:claude-prod",
		"source":       "spiffe",
		"sponsor_ref":  "human:alice",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}

	// Offboard the agent using the existing NHI offboard endpoint.
	r = h.do("POST", "/v1/m/governance/nhi/agent:claude-prod/offboard", tok,
		map[string]any{"reason": "agent decommissioned"}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("offboard = %d %s", r.code, r.raw)
	}

	// Verify offboard state.
	r = h.do("GET", "/v1/m/governance/nhi/agent:claude-prod", tok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if r.body["offboard_state"] != "soft_deleted" {
		t.Fatalf("offboard_state should be soft_deleted, got %v", r.body["offboard_state"])
	}
}
