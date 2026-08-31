// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"fmt"
	"net/http"
	"testing"
)

// Step 3 — the behavior the entity routes were migrated FOR, asserted per route.
//
// A principal with no tenant membership, holding only a workspace-scoped Cedar grant,
// must reach the rows of ITS workspace and be refused the identical row in another. That
// is only possible because the route now resolves the entity's STORED workspace before
// authorizing; with the route back at collection level the engine sees no lineage and the
// two cases become indistinguishable.
//
// It is table-driven ON PURPOSE: removing the seam from one route fails that route's
// subtest by name, instead of a single opaque failure that could come from any of them.

// scopedUser creates a user with NO membership (so it has no RBAC floor at all) and
// returns its id and session token. Its entire authority is the grant published below.
func scopedUser(t *testing.T, h *harness, admin, email string) (string, string) {
	t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", r.code, r.raw)
	}
	uid := r.body["id"].(string)
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login = %d %s", lr.code, lr.raw)
	}
	return uid, lr.body["token"].(string)
}

func TestEntityRoutesResolveTheStoredWorkspace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)
	h.createWorkspace(tenant, "payments")
	h.createWorkspace(tenant, "other")

	// One row of each entity kind in EACH workspace. Same shape, different lineage — the
	// only thing that may separate them is the workspace the row is stored under.
	mk := func(path string, body map[string]any) string {
		t.Helper()
		r := h.do("POST", "/v1/m/sourcescope/"+path, admin, body, hdr)
		if r.code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", path, r.code, r.raw)
		}
		id, _ := r.body["id"].(string)
		if id == "" {
			t.Fatalf("create %s returned no id: %s", path, r.raw)
		}
		return id
	}
	ids := map[string]map[string]string{"payments": {}, "other": {}}
	for _, ws := range []string{"payments", "other"} {
		ids[ws]["assignments"] = mk("assignments", map[string]any{
			"connector_name": "conn-" + ws, "workspace_ref": ws, "enabled": true,
		})
		ids[ws]["workspace-connectors"] = mk("workspace-connectors", map[string]any{
			"name": "wc-" + ws, "kind": "http", "workspace_ref": ws, "enabled": true,
		})
	}

	uid, tok := scopedUser(t, h, admin, "scoped@acme.com")
	h.publishGrant(admin, tenant, fmt.Sprintf(`permit(
		principal in User::%q,
		action in [
			Action::"sourcescope:assignment:read", Action::"sourcescope:assignment:write",
			Action::"sourcescope:workspace_connector:read", Action::"sourcescope:workspace_connector:write"
		],
		resource
	) when { resource in Workspace::"payments" };`, uid))

	cases := []struct {
		method, family string
		body           map[string]any
	}{
		{"GET", "assignments", nil},
		{"PUT", "assignments", map[string]any{"enabled": true, "note": "n"}},
		{"GET", "workspace-connectors", nil},
		{"PUT", "workspace-connectors", map[string]any{"enabled": true}},
		{"DELETE", "workspace-connectors", nil},
	}
	for _, c := range cases {
		t.Run(c.method+" /"+c.family+"/{id}", func(t *testing.T) {
			// OUTSIDE the grant's workspace: forbidden. Without the stored lineage the
			// engine cannot tell this row from the one below.
			out := h.do(c.method, "/v1/m/sourcescope/"+c.family+"/"+ids["other"][c.family], tok, c.body, hdr)
			if out.code != http.StatusForbidden {
				t.Errorf("row in workspace 'other' = %d, want 403: the route is not resolving the entity's stored workspace\n%s", out.code, out.raw)
			}
			// INSIDE it: the grant reaches the row. This is the CONTROL — without it a
			// blanket 403 (say, the grant never applying at all) would satisfy the case
			// above for entirely the wrong reason.
			in := h.do(c.method, "/v1/m/sourcescope/"+c.family+"/"+ids["payments"][c.family], tok, c.body, hdr)
			if in.code >= 400 {
				t.Errorf("row in workspace 'payments' = %d, want the grant to reach it and the request to SUCCEED: asserting merely 'not 403' would pass on a 401 or a 500 and prove nothing\n%s", in.code, in.raw)
			}
		})
	}
}
