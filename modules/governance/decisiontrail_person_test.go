// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
)

// decisiontrail_person_test.go pins what the decision trail SERVES.
//
// The trail is the only source every downstream quorum in this estate reads (five gates
// in cmd/olivares, eight counters beyond them). It used to serve `decider` alone — the
// audit ACTOR — while the row had carried `decider_user` since the entity existed. That
// was not a cosmetic omission: Actor() renders "user:<UserID>" for a session and
// "token:<CredID>" for a token, so a consumer holding only `decider` cannot tell two
// people apart from one person with two credentials, and the correct check could not be
// written anywhere downstream.

// TestDecisionTrailServesBothIdentities: every entry must carry the credential AND the
// person, and the person must be the decider's real stable user id — not a rendering of
// the actor string, which is what makes the two fields independent.
func TestDecisionTrailServesBothIdentities(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	deciderID, decider := h.roleUser(admin, tenant, "dec@x.io", "admin")

	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	if rr := h.do("POST", govPath+"/approvals/"+id+"/decisions", decider,
		map[string]any{"decision": "approve", "note": "ok"}, tenantHdr(tenant)); rr.code != http.StatusOK {
		t.Fatalf("decide = %d %s", rr.code, rr.raw)
	}

	trail := items(h.do("GET", govPath+"/approvals/"+id+"/decisions", admin, nil, tenantHdr(tenant)))
	if len(trail) != 1 {
		t.Fatalf("trail should have one entry, got %d", len(trail))
	}
	entry := trail[0].(map[string]any)

	got, _ := entry["decider_user"].(string)
	if got == "" {
		t.Fatal("the trail must serve decider_user; without it no consumer can count PEOPLE")
	}
	if got != deciderID {
		t.Fatalf("decider_user must be the decider's stable user id %q, got %q", deciderID, got)
	}
	// The credential stays too — it is the provenance, and it answers a different
	// question from the person. Serving one without the other is what broke.
	if d, _ := entry["decider"].(string); d == "" {
		t.Fatal("the trail must still serve the audit actor as provenance")
	}
	// And the two must be genuinely independent values: if decider_user were derived
	// from the actor string this assertion would be vacuous, so pin that it is NOT the
	// actor string itself.
	if entry["decider"] == entry["decider_user"] {
		t.Fatalf("decider and decider_user must be distinct identities, both were %q", entry["decider"])
	}
}

// The guard that keeps decider_user non-empty through the sanctioned write path (a
// system token cannot decide) is already pinned at risktier_internal_test.go — it is not
// duplicated here. What the deny-closed handling of a personless row protects against is
// therefore a backstop, not a live case; the reachable case is the one above.
