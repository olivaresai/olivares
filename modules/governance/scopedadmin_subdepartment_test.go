// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// creatingDepartments is the Business department surface as far as creation goes:
// it creates the child under its parent in one write and audits it, as the Business
// service does once its license gate passes. The moves are not under test here.
type creatingDepartments struct {
	api.DepartmentService
	st    store.Store
	calls int
}

func (d *creatingDepartments) CreateWorkspace(ctx context.Context, p auth.Principal, tenant model.TenantID, ws model.Workspace) (model.Workspace, error) {
	d.calls++
	var created model.Workspace
	err := d.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if created, err = sc.Workspaces().Create(ctx, ws); err != nil {
			return err
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: p.Actor(), ActorKind: p.ActorKind(), Action: "workspace.create",
			TargetKind: "core.workspace", TargetID: created.ID,
			Meta: map[string]any{"parent": ws.ParentID.String()},
		})
		return err
	})
	return created, err
}

// A department administrator creates a sub-department inside its own department, and
// only there: its admin grant on D makes it an admin of D, as for authoring grants in
// D, and the new child is under D at once, so the grant reaches it and the same
// administrator creates an agent in it. Without a parent, with a sibling or a
// sibling's child as parent, with only an editor grant, or as an owner confined to
// the department, the create is refused before the department service runs. A tenant
// admin creates under any department but no root; the owner still creates a root.
// The grant's subject is the user or a directory group the user is in.
func TestDepartmentAdminCreatesASubDepartmentInsideItsDepartment(t *testing.T) {
	for _, subject := range []string{"user", "group"} {
		t.Run(subject, func(t *testing.T) {
			departments := &creatingDepartments{}
			h := newHarnessWith(t, harnessOpts{departments: func(*auth.Authenticator) api.DepartmentService { return departments }})
			departments.st = h.st
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			hdr := tenantHdr(tenant)
			super, err := h.authr.Authenticate(context.Background(), admin)
			if err != nil {
				t.Fatal(err)
			}

			engineering := h.createWorkspace(tenant, "engineering")
			finance := h.createWorkspace(tenant, "finance")
			payables := h.createWorkspaceUnder(tenant, finance, "finance-payables")
			bobID, bob := h.roleUser(admin, tenant, "bob@acme.io", auth.RoleViewer)
			carolID, carol := h.roleUser(admin, tenant, "carol@acme.io", auth.RoleViewer)
			_, owner := h.roleUser(admin, tenant, "owner@acme.io", auth.RoleOwner)
			_, tenantAdmin := h.roleUser(admin, tenant, "it@acme.io", auth.RoleAdmin)
			grant := func(user, role string) {
				ref := user
				if subject == "group" {
					ref = h.createGroup(super, tenant, role+"-"+user, "cn="+role+"-"+user, user).String()
				}
				createGrant(t, h, admin, tenant, map[string]any{
					"subject_kind": subject, "subject_ref": ref, "role": role,
					"scope_tree": "workspace", "scope_ref": "engineering",
				})
			}
			// A member confined to the department, even its owner, does not place
			// departments: where its department sits is the organization's decision.
			if r := h.do("POST", "/v1/users", admin, map[string]any{"email": "dora@acme.io", "password": "memberpass1", "tenant": tenant.String(), "role": auth.RoleOwner, "workspace_id": engineering.String()}, nil); r.code != http.StatusCreated {
				t.Fatalf("create confined owner = %d %s", r.code, r.raw)
			}
			login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "dora@acme.io", "password": "memberpass1"}, nil)
			if login.code != http.StatusOK {
				t.Fatalf("confined owner login = %d %s", login.code, login.raw)
			}
			confined := login.body["token"].(string)
			h.stepUp(confined)
			grant(bobID, auth.RoleAdmin)
			grant(carolID, auth.RoleEditor)

			create := func(token, slug string, parent model.ID) resp {
				body := map[string]any{"name": slug, "slug": slug}
				if !parent.IsZero() {
					body["parent_id"] = parent.String()
				}
				return h.do("POST", "/v1/workspaces", token, body, hdr)
			}

			r := create(bob, "eng-platform", engineering)
			if r.code != http.StatusCreated {
				t.Fatalf("department admin creates a child of its department = %d %s, want 201", r.code, r.raw)
			}
			if got := r.body["parent_id"]; got != engineering.String() {
				t.Fatalf("answered parent_id = %v, want %s", got, engineering)
			}
			child := model.ID(r.body["id"].(string))
			if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
				stored, err := sc.Workspaces().Get(context.Background(), child)
				if err == nil && stored.ParentID != engineering {
					t.Errorf("stored parent = %q, want %s", stored.ParentID, engineering)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			agent := h.do("POST", "/v1/agents", bob, map[string]any{"name": "platform-bot", "kind": "claude-code", "workspace_id": child.String()}, hdr)
			if agent.code != http.StatusCreated {
				t.Fatalf("department admin creates an agent in the new child = %d %s, want 201", agent.code, agent.raw)
			}

			calls := departments.calls
			for _, tc := range []struct {
				name   string
				token  string
				parent model.ID
			}{
				{"no parent", bob, ""},
				{"a sibling department", bob, finance},
				{"a child of the sibling", bob, payables},
				{"an editor of the department", carol, engineering},
				{"an owner confined to the department", confined, engineering},
			} {
				if r := create(tc.token, "refused", tc.parent); r.code != http.StatusForbidden {
					t.Errorf("%s: create = %d %s, want 403", tc.name, r.code, r.raw)
				}
			}
			if departments.calls != calls {
				t.Errorf("a refused create reached the department service %d times", departments.calls-calls)
			}

			// A tenant administrator administers every department, as for authoring
			// grants in it, so it creates under any of them; a root stays the owner's.
			if r := create(tenantAdmin, "finance-receivables", finance); r.code != http.StatusCreated {
				t.Errorf("tenant admin creates under a department = %d %s, want 201", r.code, r.raw)
			}
			if r := create(tenantAdmin, "refused-root", ""); r.code != http.StatusForbidden {
				t.Errorf("tenant admin creates a root = %d %s, want 403", r.code, r.raw)
			}
			// A parent the decoder would drop (a duplicate key) is read as no parent.
			dup := json.RawMessage(`{"name":"dup","slug":"dup","parent_id":"` + finance.String() + `","parent_id":""}`)
			if r := h.do("POST", "/v1/workspaces", tenantAdmin, dup, hdr); r.code != http.StatusForbidden {
				t.Errorf("tenant admin creates a root through a duplicate parent_id = %d %s, want 403", r.code, r.raw)
			}

			r = create(owner, "finance-payables-eu", payables)
			if r.code != http.StatusCreated || r.body["parent_id"] != payables.String() {
				t.Fatalf("owner creates under any department = %d %s, want 201 under %s", r.code, r.raw, payables)
			}
			r = create(owner, "sales", "")
			if r.code != http.StatusCreated {
				t.Fatalf("owner creates a root department = %d %s, want 201", r.code, r.raw)
			}
			if got, present := r.body["parent_id"]; present {
				t.Fatalf("owner's root department has parent_id %v", got)
			}
		})
	}
}
