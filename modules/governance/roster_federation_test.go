// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

// TestRosterSyncFederatedOwnership: a federated agent registry's declared
// owner/sponsor roster attributes land on the lifecycle record during the
// roster sync — with the handler's exact semantics: humans only, no
// half-assignment, closed source set, idempotent re-sync.
func TestRosterSyncFederatedOwnership(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)

	// The accountable human the registry declares (the Entra directory connector
	// would have rostered it; here it is seeded directly).
	h.seedIdentity(tenant, "owner-user-1", "user", "entra", string(identitysource.PrincipalHuman), false)

	graph := identitysource.Graph{
		Source: identitysource.SourceEntraAgent,
		Identities: []identitysource.Identity{
			// A per-agent row with registry-declared ownership → mapped.
			{Ref: "agent-sp-1", Type: identitysource.PrincipalNHI, Kind: identitysource.KindAgentIdentity,
				DisplayName: "triage-agent", Source: identitysource.SourceEntraAgent,
				Attributes: map[string]string{"owner_ref": "owner-user-1", "sponsor_ref": "owner-user-1", "blueprint_id": "bp-1"}},
			// A declared sponsor the roster does not know → the WHOLE identity is
			// skipped (no half-assignment, never a fabricated accountable person).
			{Ref: "agent-sp-2", Type: identitysource.PrincipalNHI, Kind: identitysource.KindAgentIdentity,
				DisplayName: "ghost-sponsored", Source: identitysource.SourceEntraAgent,
				Attributes: map[string]string{"owner_ref": "owner-user-1", "sponsor_ref": "ghost-user"}},
			// A NON-federated source carrying the same attribute names → ignored
			// (closed source set: an arbitrary connector cannot assign ownership).
			{Ref: "ldap-svc-1", Type: identitysource.PrincipalNHI, Kind: "service_account",
				DisplayName: "svc", Source: identitysource.SourceLDAP,
				Attributes: map[string]string{"owner_ref": "owner-user-1"}},
			// A blank Ref cannot anchor a lifecycle row — skipped, never a row
			// keyed on "".
			{Ref: "  ", Type: identitysource.PrincipalNHI, Kind: identitysource.KindAgentIdentity,
				DisplayName: "anchorless", Source: identitysource.SourceEntraAgent,
				Attributes: map[string]string{"owner_ref": "owner-user-1"}},
		},
	}
	if _, err := h.gov.SyncRoster(context.Background(), tenant, graph); err != nil {
		t.Fatalf("SyncRoster: %v", err)
	}

	// The federated agent's lifecycle record carries the declared ownership.
	r := h.do("GET", "/v1/m/governance/nhi/agent-sp-1", tok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get nhi agent-sp-1 = %d %s", r.code, r.raw)
	}
	if r.body["owner_ref"] != "owner-user-1" || r.body["sponsor_ref"] != "owner-user-1" {
		t.Fatalf("federated ownership not mapped: %s", r.raw)
	}
	if orphaned, _ := r.body["orphaned"].(bool); orphaned {
		t.Fatal("a freshly sponsored NHI must not be orphaned")
	}

	// The ghost-sponsored identity got NO lifecycle assignment at all.
	if r := h.do("GET", "/v1/m/governance/nhi/agent-sp-2", tok, nil, hdr); r.code == http.StatusOK {
		if r.body["owner_ref"] == "owner-user-1" || r.body["sponsor_ref"] != "" {
			t.Fatalf("unresolvable sponsor must skip the whole assignment, got %s", r.raw)
		}
	}
	// The non-federated source's attribute was ignored.
	if r := h.do("GET", "/v1/m/governance/nhi/ldap-svc-1", tok, nil, hdr); r.code == http.StatusOK {
		if r.body["owner_ref"] == "owner-user-1" {
			t.Fatalf("non-federated source must not assign ownership, got %s", r.raw)
		}
	}

	// Idempotency: a second sync with the same graph writes nothing new — the
	// append-only event trail still holds exactly one "assigned" event.
	if _, err := h.gov.SyncRoster(context.Background(), tenant, graph); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	ev := h.do("GET", "/v1/m/governance/nhi/agent-sp-1/events", tok, nil, hdr)
	if ev.code != http.StatusOK {
		t.Fatalf("events = %d %s", ev.code, ev.raw)
	}
	if n := len(items(ev)); n != 1 {
		t.Fatalf("assigned events = %d, want exactly 1 (idempotent re-sync)", n)
	}

	// The registry remains the source of truth for what it declares: a changed
	// registry value overwrites on the next sync.
	h.seedIdentity(tenant, "owner-user-2", "user", "entra", string(identitysource.PrincipalHuman), false)
	graph.Identities[0].Attributes["sponsor_ref"] = "owner-user-2"
	if _, err := h.gov.SyncRoster(context.Background(), tenant, graph); err != nil {
		t.Fatalf("third sync: %v", err)
	}
	r = h.do("GET", "/v1/m/governance/nhi/agent-sp-1", tok, nil, hdr)
	if r.body["sponsor_ref"] != "owner-user-2" {
		t.Fatalf("registry-changed sponsor must win on the next sync: %s", r.raw)
	}
	if r.body["owner_ref"] != "owner-user-1" {
		t.Fatalf("undeclared values must never be cleared: %s", r.raw)
	}
}

// TestRosterSyncRegistryOrphan: a registry-asserted orphan
// (Attributes["orphaned"]=="true" — an Entra agent whose blueprint is gone)
// lands on the lifecycle record's registry_orphaned column; the sweep ORs
// it into `orphaned` (so sponsor-liveness recomputation never clobbers it) and
// clears only when the registry's next sync stops asserting it.
func TestRosterSyncRegistryOrphan(t *testing.T) {
	h := newHarness(t)
	tenant, tok := h.nhiTenant()
	hdr := tenantHdr(tenant)

	graph := identitysource.Graph{
		Source: identitysource.SourceEntraAgent,
		Identities: []identitysource.Identity{
			{Ref: "agent-orph-1", Type: identitysource.PrincipalNHI, Kind: identitysource.KindAgentIdentity,
				DisplayName: "orphaned-agent", Source: identitysource.SourceEntraAgent,
				Attributes: map[string]string{"orphaned": "true"}},
		},
	}
	if _, err := h.gov.SyncRoster(context.Background(), tenant, graph); err != nil {
		t.Fatalf("SyncRoster: %v", err)
	}

	// The assertion alone creates the lifecycle row with the registry flag.
	r := h.do("GET", "/v1/m/governance/nhi/agent-orph-1", tok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("get nhi = %d %s", r.code, r.raw)
	}
	if reg, _ := r.body["registry_orphaned"].(bool); !reg {
		t.Fatalf("registry_orphaned not set: %s", r.raw)
	}

	// The sweep ORs it into `orphaned` (and emits the nhi_orphaned finding on
	// the transition — the existing machinery, no parallel path).
	if r := h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("sweep = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/governance/nhi/agent-orph-1", tok, nil, hdr)
	if orph, _ := r.body["orphaned"].(bool); !orph {
		t.Fatalf("sweep must OR the registry assertion into orphaned: %s", r.raw)
	}

	// A second sweep must NOT flip it back (the sponsor-liveness recomputation
	// cannot clobber a registry-asserted orphan — the reviewed failure mode).
	if r := h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("second sweep = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/governance/nhi/agent-orph-1", tok, nil, hdr)
	if orph, _ := r.body["orphaned"].(bool); !orph {
		t.Fatalf("a later sweep clobbered the registry-asserted orphan: %s", r.raw)
	}

	// The registry stops asserting it (the agent re-bound to a live blueprint):
	// the flag clears on sync, and the next sweep clears `orphaned`.
	graph.Identities[0].Attributes = map[string]string{}
	if _, err := h.gov.SyncRoster(context.Background(), tenant, graph); err != nil {
		t.Fatalf("clearing sync: %v", err)
	}
	r = h.do("GET", "/v1/m/governance/nhi/agent-orph-1", tok, nil, hdr)
	if reg, _ := r.body["registry_orphaned"].(bool); reg {
		t.Fatalf("registry flag must clear when no longer asserted: %s", r.raw)
	}
	if r := h.do("POST", "/v1/m/governance/nhi/sweep", tok, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("third sweep = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/governance/nhi/agent-orph-1", tok, nil, hdr)
	if orph, _ := r.body["orphaned"].(bool); orph {
		t.Fatalf("orphaned must clear once the registry stops asserting it: %s", r.raw)
	}
}
