// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func seedClassifiedMemory(t *testing.T, h *harness) (model.TenantID, string, string) {
	t.Helper()
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	adminToken := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	for _, classification := range []string{classPublic, classInternal, classConfidential} {
		h.putMemoryScoped(editor, tenant, map[string]any{
			"agent_ref":      "agent-1",
			"key":            classification,
			"content":        classification + " memory",
			"classification": classification,
		})
	}
	return tenant, editor, adminToken
}

func memoryClassifications(t *testing.T, r resp) map[string]bool {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("list memory = %d %s", r.code, r.raw)
	}
	got := make(map[string]bool)
	for _, item := range listItems(r) {
		got[item["classification"].(string)] = true
	}
	return got
}

func TestMemoryListFiltersByReaderClearance(t *testing.T) {
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Clearance: classInternal,
	}}))
	tenant, editor, admin := seedClassifiedMemory(t, h)

	list := h.do("GET", "/v1/m/knowledge/memory?agent_ref=agent-1", editor, nil, tenantHdr(tenant))
	got := memoryClassifications(t, list)
	if len(got) != 2 || !got[classPublic] || !got[classInternal] || got[classConfidential] {
		t.Fatalf("internal-clearance memory = %v, want public+internal only", got)
	}

	all := h.do("GET", "/v1/m/knowledge/memory/all?agent_ref=agent-1", admin, nil, tenantHdr(tenant))
	if gotAll := memoryClassifications(t, all); len(gotAll) != 3 || !gotAll[classConfidential] {
		t.Fatalf("admin memory view = %v, want all classifications", gotAll)
	}
}

// classifiedMemoryIDs returns the {classification: id} map from the admin governance view
// (which applies no classification filter), so a test can address a specific entry by id.
func classifiedMemoryIDs(t *testing.T, h *harness, admin string, tenant model.TenantID) map[string]string {
	t.Helper()
	all := h.do("GET", "/v1/m/knowledge/memory/all?agent_ref=agent-1", admin, nil, tenantHdr(tenant))
	if all.code != http.StatusOK {
		t.Fatalf("memory/all = %d %s", all.code, all.raw)
	}
	out := map[string]string{}
	for _, item := range listItems(all) {
		out[item["classification"].(string)] = item["id"].(string)
	}
	return out
}

// TestMemoryGetEnforcesReaderClearance is the E5 red repro: GET /memory/{id} must apply the
// SAME classification clearance filter as GET /memory (handleListMemory), so a reader that
// knows or guesses the id of an entry classified ABOVE their clearance cannot retrieve it
// whole. Denial is a 404, indistinguishable from absent (no existence leak).
func TestMemoryGetEnforcesReaderClearance(t *testing.T) {
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Clearance: classInternal,
	}}))
	tenant, editor, admin := seedClassifiedMemory(t, h)
	ids := classifiedMemoryIDs(t, h, admin, tenant)
	if ids[classConfidential] == "" || ids[classInternal] == "" {
		t.Fatal("setup: expected internal + confidential entries")
	}

	// Control: an entry at/below the reader's clearance stays fetchable.
	if r := h.do("GET", "/v1/m/knowledge/memory/"+ids[classInternal]+"?agent_ref=agent-1", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("internal entry must be fetchable at internal clearance = %d %s", r.code, r.raw)
	}
	// A confidential entry ABOVE the reader's clearance must be 404.
	if r := h.do("GET", "/v1/m/knowledge/memory/"+ids[classConfidential]+"?agent_ref=agent-1", editor, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("E5: confidential entry above clearance must be 404 (no read leak), got %d %s", r.code, r.raw)
	}
}

// TestMemoryDeleteEnforcesReaderClearance: DELETE /memory/{id} shares the "you can only act
// on what you can see" invariant — a writer with only internal clearance must not be able to
// destroy a confidential entry it cannot read (404), and the entry must survive.
func TestMemoryDeleteEnforcesReaderClearance(t *testing.T) {
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Clearance: classInternal,
	}}))
	tenant, editor, admin := seedClassifiedMemory(t, h)
	ids := classifiedMemoryIDs(t, h, admin, tenant)
	if ids[classConfidential] == "" {
		t.Fatal("setup: expected a confidential entry")
	}

	if r := h.do("DELETE", "/v1/m/knowledge/memory/"+ids[classConfidential]+"?agent_ref=agent-1", editor, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("E5: deleting a confidential entry above clearance must be 404, got %d %s", r.code, r.raw)
	}
	// The entry must still exist (the denied delete must not have destroyed it).
	if after := classifiedMemoryIDs(t, h, admin, tenant); after[classConfidential] == "" {
		t.Fatal("E5: a clearance-denied delete must not destroy the entry")
	}
}

func TestMemoryListClearanceDenyClosedFallbacks(t *testing.T) {
	tests := []struct {
		name string
		opt  Option
	}{
		{name: "nil guard", opt: WithRetrievalGuard(nil)},
		{name: "resolve error", opt: WithRetrievalGuard(errorGuard{})},
		{name: "guard denial", opt: WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: false, Clearance: classConfidential,
		}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarnessWith(t, tt.opt)
			tenant, editor, _ := seedClassifiedMemory(t, h)

			list := h.do("GET", "/v1/m/knowledge/memory?agent_ref=agent-1", editor, nil, tenantHdr(tenant))
			got := memoryClassifications(t, list)
			if len(got) != 1 || !got[classPublic] {
				t.Fatalf("deny-closed memory = %v, want public only", got)
			}
		})
	}
}
