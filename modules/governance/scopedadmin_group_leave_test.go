// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A directory group that holds an admin grant on a department administers it for as
// long as the person is in the group, and the same session stops administering it on
// its next request once the directory takes the person out. Nothing is cached between
// those requests: the principal is rebuilt from the group roster each time. The
// enforcement path, the self-rights answer the console asks and the permission set
// whoami hands it must all turn together, however the person leaves: removed from the
// group, removed from a child of the group the grant names, or the group deleted.
// A member who stays keeps administering it, so the leave ends one person's authority
// and not the group's grant — unless the group itself is deleted, which ends it for
// everyone.
func TestGroupAdminOfDepartmentEndsWhenTheMemberLeavesTheGroup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		nested      bool // the grant names a parent group; Bob is only in its child
		deleteGroup bool // the directory deletes the group instead of emptying it
	}{
		{"removed from the group", false, false},
		{"removed from a child of the group", true, false},
		{"group deleted", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, harnessOpts{selfRights: true})
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			hdr := tenantHdr(tenant)
			super, err := h.authr.Authenticate(context.Background(), admin)
			if err != nil {
				t.Fatal(err)
			}

			engineering := h.createWorkspace(tenant, "engineering")
			finance := h.createWorkspace(tenant, "finance")
			bobID, bob := h.roleUser(admin, tenant, "bob@acme.io", auth.RoleViewer)
			carolID, carol := h.roleUser(admin, tenant, "carol@acme.io", auth.RoleViewer)
			// membership is the group Bob is in; granted is the group the grant names.
			membership := h.createGroup(super, tenant, "eng-admins", "cn=eng-admins", bobID, carolID)
			granted := membership
			if tc.nested {
				granted = h.createGroup(super, tenant, "engineering", "cn=engineering")
				h.nestGroup(super, tenant, membership, granted)
			}
			createGrant(t, h, admin, tenant, map[string]any{
				"subject_kind": "group", "subject_ref": granted.String(), "role": auth.RoleAdmin,
				"scope_tree": "workspace", "scope_ref": "engineering",
			})

			deleteIn := func(tok string, ws model.ID, name string) int {
				a := h.createAgentIn(tenant, name, ws)
				return h.do("DELETE", "/v1/agents/"+a.ID.String(), tok, nil, hdr).code
			}
			// What the console asks before it offers "New agent" in a department.
			createState := func(ws model.ID) string {
				r := h.do("POST", "/v1/auth/capabilities", bob, map[string]any{"schema_version": 2, "questions": []map[string]any{
					{"id": "create", "kind": "surface", "operation": "POST /v1/agents", "workspace_id": ws.String()},
				}}, hdr)
				results, _ := r.body["results"].([]any)
				if r.code != http.StatusOK || len(results) != 1 {
					t.Fatalf("capabilities = %d %s", r.code, r.raw)
				}
				state, _ := results[0].(map[string]any)["state"].(string)
				return state
			}
			// What whoami hands the console for the Roles screen. The tenant entry must
			// be there either way, so an absent entry cannot read as "no Roles screen".
			rolesScreen := func(tok string) bool {
				perms, present := whoamiPerms(t, h, tok, tenant)
				if !present {
					t.Fatal("whoami has no grant entry for the tenant")
				}
				return hasPerm(perms, "governance:rbac:admin")
			}

			// Control: while Bob is in the group he administers Engineering and only it.
			if got := deleteIn(bob, engineering, "eng-1"); got != http.StatusNoContent {
				t.Fatalf("member writes in its department = %d, want 204", got)
			}
			if got := deleteIn(bob, finance, "fin-1"); got != http.StatusForbidden {
				t.Fatalf("member writes in a sibling department = %d, want 403", got)
			}
			if got, want := createState(engineering), "reachable"; got != want {
				t.Fatalf("member's create in its department = %s, want %s", got, want)
			}
			if got, want := createState(finance), "not_reachable"; got != want {
				t.Fatalf("member's create in a sibling department = %s, want %s", got, want)
			}
			if !rolesScreen(bob) {
				t.Fatal("member is not offered the Roles screen")
			}

			// The directory takes Bob out. Same session, next request.
			if tc.deleteGroup {
				err = h.authr.SCIMDeleteGroup(context.Background(), super, tenant, membership)
			} else { // a roster that keeps Carol replaces the member list
				_, err = h.authr.SCIMReplaceGroup(context.Background(), super, tenant, membership,
					auth.SCIMGroupInput{DisplayName: "eng-admins", ExternalID: "cn=eng-admins",
						Members: []model.ID{model.ID(carolID)}}, 0)
			}
			if err != nil {
				t.Fatalf("the directory takes Bob out: %v", err)
			}
			if got := deleteIn(bob, engineering, "eng-2"); got != http.StatusForbidden {
				t.Fatalf("leaver still writes in the department = %d, want 403", got)
			}
			if got, want := createState(engineering), "not_reachable"; got != want {
				t.Fatalf("leaver's create in the department = %s, want %s", got, want)
			}
			if rolesScreen(bob) {
				t.Fatal("leaver is still offered the Roles screen")
			}

			// A member who stays keeps the department: the leave ends Bob's authority,
			// not the group's grant. A deleted group ends it for everyone.
			if tc.deleteGroup {
				if got := deleteIn(carol, engineering, "eng-3"); got != http.StatusForbidden {
					t.Fatalf("staying member writes after the group is deleted = %d, want 403", got)
				}
				return
			}
			if got := deleteIn(carol, engineering, "eng-3"); got != http.StatusNoContent {
				t.Fatalf("staying member loses the department = %d, want 204", got)
			}
			if !rolesScreen(carol) {
				t.Fatal("staying member is not offered the Roles screen")
			}
		})
	}
}
