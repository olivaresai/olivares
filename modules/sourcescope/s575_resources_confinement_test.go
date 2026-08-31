// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// B-03 — the second shape of the module-route confinement leak, on the READ
// (mc.Data.View) path.
//
// The governance regressions cover the Mutate path (a governed read that self-audits).
// This one covers View, and it covers the distinction a naive assertion misses: this
// handler used to APPEND the caller's ?workspace_id to the query
// (modules/sourcescope/resources.go), so a confined caller could name another
// workspace. Asserting only "the foreign row is absent" would ALSO pass if the two
// workspace predicates were ANDed into an empty result — a different bug that looks
// identical from outside. So the caller-supplied case asserts the caller still gets
// its OWN row: the mandatory filter must REPLACE the caller's, exactly as core's
// parseFilteredListQuery does, not intersect with it.

// confinedUserAt creates a user whose membership is confined to ws and returns its token.
func (h *harness) confinedUserAt(admin string, tenant model.TenantID, email string, ws model.ID) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	uid, _ := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{
		"user_id": uid, "tenant": tenant.String(), "role": "admin", "workspace_id": ws.String(),
	}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant confined %s = %d %s", email, r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	tok, _ := r.body["token"].(string)
	return tok
}

func TestModuleReadRouteIsWorkspaceConfined(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "resconf")
	hdr := tenantHdr(tenant)

	wsA := h.createWorkspace(tenant, "wa")
	wsB := h.createWorkspace(tenant, "wb")
	h.createFolder(tenant, "own-docs", model.ID(""), wsA)
	h.createFolder(tenant, "hr-secrets-store", model.ID(""), wsB)

	// The unconfined superadmin sees both roots. This proves the fixture really holds
	// two rows, so a one-row answer below is the filter working and not an empty
	// fixture — the failure mode where a test passes because nothing was created.
	if r := h.do("GET", "/v1/m/sourcescope/resources", admin, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("superadmin roots = %d %s", r.code, r.raw)
	} else if n := len(items(r)); n != 2 {
		t.Fatalf("fixture: superadmin must see both roots, got %d: %s", n, r.raw)
	}

	confined := h.confinedUserAt(admin, tenant, "conf@resconf.io", wsA)

	r := h.do("GET", "/v1/m/sourcescope/resources", confined, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("confined roots = %d %s, want 200 (the read is permitted, the ROWS are confined)", r.code, r.raw)
	}
	got := items(r)
	if len(got) != 1 {
		t.Fatalf("a wa-confined operator must see ONLY its workspace's root, got %d: %s", len(got), r.raw)
	}
	node, _ := got[0].(map[string]any)
	if name, _ := node["name"].(string); name != "own-docs" {
		t.Errorf("the only visible root must be wa's own-docs, got %q: %s", name, r.raw)
	}
}

func TestModuleReadRouteOverridesCallerWorkspaceRatherThanIntersecting(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "resoverride")
	hdr := tenantHdr(tenant)

	wsA := h.createWorkspace(tenant, "wa")
	wsB := h.createWorkspace(tenant, "wb")
	h.createFolder(tenant, "own-docs", model.ID(""), wsA)
	h.createFolder(tenant, "hr-secrets-store", model.ID(""), wsB)

	confined := h.confinedUserAt(admin, tenant, "conf@resoverride.io", wsA)

	// Explicitly ASKING for the other workspace must neither widen the view NOR empty
	// it: the caller's predicate is replaced, so the answer is still the caller's own
	// row. The second half of this assertion is what tells "override" apart from "AND
	// produced nothing".
	r := h.do("GET", "/v1/m/sourcescope/resources?workspace_id="+wsB.String(), confined, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("naming another workspace = %d %s, want 200 with own rows", r.code, r.raw)
	}
	got := items(r)
	if len(got) != 1 {
		t.Fatalf("naming wb must still return exactly the wa row, got %d: %s", len(got), r.raw)
	}
	node, _ := got[0].(map[string]any)
	if name, _ := node["name"].(string); name != "own-docs" {
		t.Fatalf("naming wb returned %q; the caller must get its OWN row, never wb's: %s", name, r.raw)
	}
	if ws, _ := node["workspace_id"].(string); ws != wsA.String() {
		t.Errorf("the returned row must belong to wa (%s), got workspace_id=%q", wsA, ws)
	}
}
