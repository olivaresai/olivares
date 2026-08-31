// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
)

// B-03 — workspace confinement must reach MODULE routes, not only core REST.
//
// The confinement filter existed only where a core handler remembered to ask for it
// (core/api/handlers_core.go parseFilteredListQuery, reached from handlers_core.go,
// handlers_scoping.go, handlers_members.go and core/api/search.go). A module route receives
// an api.ModuleContext whose Data handle hands out a RAW store.Scope, so every core
// repository is reachable from a module with no workspace axis applied at all — the same
// sc.Agents() the core list route row-filters.
//
// These are the regressions for the seat of the filter moving into ScopedData: a module
// handler that never mentions confinement must still be confined.

// TestModuleRouteBindingsAreWorkspaceConfined is the named leak: GET /v1/m/governance/bindings
// walks sc.Agents().List with a bare model.Query (modules/governance/identity.go), so a
// wa-confined operator saw the agent→identity topology of the WHOLE tenant. The topology is
// recon-relevant by the module's own doc comment (governance.go:30), which is why the read
// self-audits — auditing a read that should never have returned those rows is not a mitigation.
func TestModuleRouteBindingsAreWorkspaceConfined(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "bindconf")
	hdr := tenantHdr(tenant)

	wsA := h.createWorkspace(tenant, "wa")
	wsB := h.createWorkspace(tenant, "wb")

	inA := h.createAgentIn(tenant, "a-bot", wsA)
	inB := h.createAgentIn(tenant, "b-bot", wsB)

	// Bind each agent to a distinct identity so both appear in /bindings.
	if r := h.do("POST", govPath+"/agents/"+inA.ID.String()+"/identity", admin,
		map[string]any{"identity_ref": "svc-wa"}, hdr); r.code != http.StatusOK {
		t.Fatalf("bind wa agent = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", govPath+"/agents/"+inB.ID.String()+"/identity", admin,
		map[string]any{"identity_ref": "svc-wb"}, hdr); r.code != http.StatusOK {
		t.Fatalf("bind wb agent = %d %s", r.code, r.raw)
	}

	_, confined := h.confinedUser(admin, tenant, "conf@bindconf.io", "admin", wsA)

	// The unconfined superadmin sees both bindings — this proves the fixture really has two,
	// so a one-row result below is the filter working and not an empty fixture (the failure
	// mode where a test passes because nothing was ever created).
	if r := h.do("GET", govPath+"/bindings", admin, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("superadmin /bindings = %d %s", r.code, r.raw)
	} else if n := len(items(r)); n != 2 {
		t.Fatalf("fixture: superadmin must see both bindings, got %d: %s", n, r.raw)
	}

	r := h.do("GET", govPath+"/bindings", confined, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("confined operator /bindings = %d %s, want 200 (the read is permitted, the ROWS are confined)", r.code, r.raw)
	}
	got := items(r)
	if len(got) != 1 {
		t.Fatalf("a wa-confined operator must see ONLY its workspace's binding, got %d rows: %s", len(got), r.raw)
	}
	b, _ := got[0].(map[string]any)
	if ref, _ := b["identity_ref"].(string); ref != "svc-wa" {
		t.Errorf("the only visible binding must be wa's (svc-wa), got identity_ref=%q: %s", ref, r.raw)
	}
	if id, _ := b["agent_id"].(string); id != inA.ID.String() {
		t.Errorf("the only visible binding must be the wa agent %s, got agent_id=%q", inA.ID, id)
	}
}

// TestModuleRouteConfinementSurvivesCallerSuppliedWorkspace closes the second shape of the
// leak: a module route that honors a caller-supplied ?workspace_id must not let a confined
// caller name someone else's workspace. Core's parseFilteredListQuery OVERRIDES the query
// parameter for a confined caller (handlers_core.go); a module route that merely APPENDS the
// caller's value hands over the other workspace on request.
func TestModuleRouteConfinementSurvivesCallerSuppliedWorkspace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wsparam")
	hdr := tenantHdr(tenant)

	wsA := h.createWorkspace(tenant, "wa")
	wsB := h.createWorkspace(tenant, "wb")
	inA := h.createAgentIn(tenant, "a-bot", wsA)
	inB := h.createAgentIn(tenant, "b-bot", wsB)

	if r := h.do("POST", govPath+"/agents/"+inA.ID.String()+"/identity", admin,
		map[string]any{"identity_ref": "svc-wa"}, hdr); r.code != http.StatusOK {
		t.Fatalf("bind wa agent = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", govPath+"/agents/"+inB.ID.String()+"/identity", admin,
		map[string]any{"identity_ref": "svc-wb"}, hdr); r.code != http.StatusOK {
		t.Fatalf("bind wb agent = %d %s", r.code, r.raw)
	}

	_, confined := h.confinedUser(admin, tenant, "conf@wsparam.io", "admin", wsA)

	// Explicitly ASKING for the other workspace must not widen what the caller may see.
	r := h.do("GET", govPath+"/bindings?workspace_id="+wsB.String(), confined, nil, hdr)
	if r.code == http.StatusOK {
		for _, it := range items(r) {
			b, _ := it.(map[string]any)
			if ref, _ := b["identity_ref"].(string); ref == "svc-wb" {
				t.Fatalf("a wa-confined operator named wb and received it: %s", r.raw)
			}
		}
	} else if r.code != http.StatusForbidden {
		t.Fatalf("naming another workspace must be 200-with-own-rows or 403, got %d %s", r.code, r.raw)
	}
}

// TestCoreWorkspaceListIsConfined closes the CORE-side counterpart of the same leak,
// authorized separately because it is not a module route: GET /v1/workspaces returned
// every workspace of the tenant to a confined operator — the names, slugs and ids of
// the scopes it may not act in.
//
// The axis here is not a workspace_id column. A workspace does not carry one: it IS
// the node, so the forced predicate is on the row's own id. Applying the generic
// workspace filter would have filtered on a column this table does not have.
func TestCoreWorkspaceListIsConfined(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wsconf")
	hdr := tenantHdr(tenant)

	wsA := h.createWorkspace(tenant, "wa")
	wsB := h.createWorkspace(tenant, "wb")

	// The superadmin sees the two created workspaces plus the tenant's default, so a
	// one-row answer below is the filter and not an empty fixture.
	r := h.do("GET", "/v1/workspaces", admin, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("superadmin /v1/workspaces = %d %s", r.code, r.raw)
	}
	if n := len(items(r)); n < 2 {
		t.Fatalf("fixture: superadmin must see at least the two created workspaces, got %d: %s", n, r.raw)
	}

	_, confined := h.confinedUser(admin, tenant, "conf@wsconf.io", "admin", wsA)

	r = h.do("GET", "/v1/workspaces", confined, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("confined /v1/workspaces = %d %s, want 200 with its own row", r.code, r.raw)
	}
	got := items(r)
	if len(got) != 1 {
		t.Fatalf("a wa-confined operator must see ONLY its own workspace, got %d: %s", len(got), r.raw)
	}
	row, _ := got[0].(map[string]any)
	if id, _ := row["id"].(string); id != wsA.String() {
		t.Errorf("the only visible workspace must be wa (%s), got %q", wsA, id)
	}

	// Naming the other workspace by id must be indistinguishable from absent, or the
	// route stays an oracle for the ids the list no longer enumerates.
	if r := h.do("GET", "/v1/workspaces/"+wsB.String(), confined, nil, hdr); r.code != http.StatusNotFound {
		t.Errorf("GET another workspace by id = %d %s, want 404", r.code, r.raw)
	}
	// Its own is still readable: the fix confines, it does not lock the operator out.
	if r := h.do("GET", "/v1/workspaces/"+wsA.String(), confined, nil, hdr); r.code != http.StatusOK {
		t.Errorf("GET its own workspace = %d %s, want 200", r.code, r.raw)
	}
}
