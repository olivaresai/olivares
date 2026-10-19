// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// Provider profiles ("profile", by profile ref) and AI tools ("tool", by driver key) are
// sources like any other: a binding to a department (workspace), a directory group or a
// user decides who may use them, a department binding reaches its sub-departments, and a
// source with no binding stays open. Assigning is Business; deciding is every edition.

func (h *harness) createWorkspaceUnder(tenant model.TenantID, slug string, parent model.ID) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.Mutate(h.t.Context(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(h.t.Context(), model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive, ParentID: parent})
		id = ws.ID
		return err
	}); err != nil {
		h.t.Fatalf("create workspace %s: %v", slug, err)
	}
	return id
}

// resolveHuman asks the way the launch gate does for a signed-in person: no acting agent.
func (h *harness) resolveHuman(tenant model.TenantID, p auth.Principal, sourceType, sourceRef string) sourcescope.Decision {
	h.t.Helper()
	return h.resolveAgent(tenant, p, "", sourceType, sourceRef)
}

func (h *harness) mustAssign(admin string, tenant model.TenantID, sourceType, sourceRef, tree, ref string) {
	h.t.Helper()
	body := map[string]any{"source_type": sourceType, "source_ref": sourceRef, "scope_tree": tree, "scope_ref": ref, "enabled": true}
	if r := h.createBinding(admin, tenant, body); r.code != http.StatusCreated {
		h.t.Fatalf("assign %s/%s to %s/%s = %d %s", sourceType, sourceRef, tree, ref, r.code, r.raw)
	}
}

func errorCode(r resp) string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestAssigningProfilesAndToolsNeedsBusiness(t *testing.T) {
	for _, source := range []struct{ typ, ref string }{{"profile", "ppf_acme"}, {"tool", "codex"}} {
		t.Run(source.typ, func(t *testing.T) {
			h := newHarness(t) // the Community build: no assignment option
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			h.createWorkspace(tenant, "eng")
			viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)

			r := h.createBinding(admin, tenant, map[string]any{
				"source_type": source.typ, "source_ref": source.ref, "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
			})
			if r.code != http.StatusNotImplemented || errorCode(r) != "business_required" {
				t.Fatalf("assign in Community = %d %s, want 501 business_required", r.code, r.raw)
			}
			// Nothing was stored, so the source stays open to a person it was never given to.
			if d := h.resolveHuman(tenant, viewer, source.typ, source.ref); !d.Allowed || d.Bound {
				t.Fatalf("a refused assignment must leave the source unbound and open, got %+v", d)
			}
			// The other source types keep working in Community.
			if r := h.createBinding(admin, tenant, map[string]any{
				"source_type": "model", "source_ref": "m", "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
			}); r.code != http.StatusCreated {
				t.Fatalf("a model binding in Community = %d %s, want 201", r.code, r.raw)
			}
		})
	}
}

// seedStoredAssignment writes an assignment the way a Business install stored it, behind
// the Community API that refuses to write one.
func seedStoredAssignment(t *testing.T, h *harness, tenant model.TenantID, workspace model.ID) model.ID {
	t.Helper()
	var id model.ID
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sourcescope.binding")
		if err != nil {
			return err
		}
		rec, err := repo.Create(t.Context(), model.Record{
			"source_type": "profile", "source_ref": "ppf_acme", "scope_tree": "workspace", "scope_ref": "eng",
			"workspace_id": workspace.String(), "enabled": true, "created_by": "seed", "effect": "allow",
		})
		id = model.ID(rec.String(model.ColID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUpdatingAStoredAssignmentNeedsBusinessButRevokingDoesNot(t *testing.T) {
	h := newHarness(t) // Community, holding an assignment a Business install stored
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	sales := h.createWorkspace(tenant, "sales")
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	salesBot := h.createAgent(tenant, "sales-bot", sales)
	id := seedStoredAssignment(t, h, tenant, eng)

	if d := h.resolveAgent(tenant, viewer, salesBot.ExternalID, "profile", "ppf_acme"); d.Allowed || !d.Bound {
		t.Fatalf("a stored assignment keeps applying in Community, got %+v", d)
	}
	r := h.do("PUT", "/v1/m/sourcescope/bindings/"+id.String(), admin, map[string]any{"scope_tree": "workspace", "scope_ref": "eng", "enabled": false}, tenantHdr(tenant))
	if r.code != http.StatusNotImplemented || errorCode(r) != "business_required" {
		t.Fatalf("update in Community = %d %s, want 501 business_required", r.code, r.raw)
	}
	// Revoking the last assignment opens the profile to everyone, so it is a relaxing change
	// every edition takes through a second approver; Community keeps that path.
	r = h.do("DELETE", "/v1/m/sourcescope/bindings/"+id.String(), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusAccepted || r.body["op"] != "delete" {
		t.Fatalf("revoke in Community = %d %s, want 202 pending delete", r.code, r.raw)
	}
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+r.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve revoke = %d %s", a.code, a.raw)
	}
	if d := h.resolveAgent(tenant, viewer, salesBot.ExternalID, "profile", "ppf_acme"); !d.Allowed {
		t.Fatalf("a revoked assignment opens the profile again, got %+v", d)
	}
}

// A request a Business install queued, to assign a profile or to change an assignment, is not
// applied by a Community one.
func TestACommunityEngineDoesNotApplyAQueuedAssignment(t *testing.T) {
	for _, op := range []string{"create", "update"} {
		t.Run(op, func(t *testing.T) {
			h := newHarness(t) // Community
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			eng := h.createWorkspace(tenant, "eng")
			approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
			stored := seedStoredAssignment(t, h, tenant, eng)
			request := model.Record{
				"op": op, "source_type": "profile", "source_ref": "ppf_acme", "status": "pending", "reason": "x",
				"proposed": `{"source_type":"profile","source_ref":"ppf_acme","scope_tree":"workspace","scope_ref":"eng","enabled":true}`,
				"proposer": "user:" + model.NewID().String(), "proposer_user": model.NewID().String(),
			}
			if op == "update" {
				request["target_id"] = stored.String()
			}
			var id model.ID
			if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("sourcescope.posture_request")
				if err != nil {
					return err
				}
				rec, err := repo.Create(t.Context(), request)
				id = model.ID(rec.String(model.ColID))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			r := h.do("POST", "/v1/m/sourcescope/posture-requests/"+id.String()+"/approve", approver, nil, tenantHdr(tenant))
			if r.code != http.StatusNotImplemented || errorCode(r) != "business_required" {
				t.Fatalf("approving a queued %s in Community = %d %s, want 501 business_required", op, r.code, r.raw)
			}
		})
	}
}

// Disabling the scoping of a source only relaxes it, like revoking: Community keeps that path
// for an assignment a Business install stored.
func TestACommunityEngineKeepsDisablingTheScopingOfAStoredAssignment(t *testing.T) {
	h := newHarness(t) // Community
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	sales := h.createWorkspace(tenant, "sales")
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	salesBot := h.createAgent(tenant, "sales-bot", sales)
	seedStoredAssignment(t, h, tenant, eng)
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)

	r := h.do("POST", "/v1/m/sourcescope/sources/disable-scoping", admin, map[string]any{"source_type": "profile", "source_ref": "ppf_acme"}, tenantHdr(tenant))
	if r.code != http.StatusAccepted {
		t.Fatalf("disable-scoping in Community = %d %s, want 202 pending", r.code, r.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+r.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approving disable-scoping in Community = %d %s, want 200", a.code, a.raw)
	}
	if d := h.resolveAgent(tenant, viewer, salesBot.ExternalID, "profile", "ppf_acme"); !d.Allowed {
		t.Fatalf("a source whose scoping was disabled is open to everyone again, got %+v", d)
	}
}

// agents of the departments eng > platform and sales, and the people who are not agents.
type departments struct {
	eng, platform, sales model.Agent
	platformWorkspace    model.ID
}

func newDepartments(t *testing.T, h *harness, tenant model.TenantID) departments {
	t.Helper()
	engID := h.createWorkspace(tenant, "eng")
	platformID := h.createWorkspaceUnder(tenant, "platform", engID)
	salesID := h.createWorkspace(tenant, "sales")
	return departments{
		eng:               h.createAgent(tenant, "eng-bot", engID),
		platform:          h.createAgent(tenant, "platform-bot", platformID),
		sales:             h.createAgent(tenant, "sales-bot", salesID),
		platformWorkspace: platformID,
	}
}

func TestProfileAndToolAssignedToDepartments(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	d := newDepartments(t, h, tenant)
	owner := h.principalFor(admin, tenant, "owner@acme.io", auth.RoleAdmin)
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)

	h.mustAssign(admin, tenant, "profile", "ppf_eng", "workspace", "eng")
	h.mustAssign(admin, tenant, "tool", "grok", "workspace", "sales")

	for _, tc := range []struct {
		name      string
		who       auth.Principal
		agent     string
		typ, ref  string
		wantAllow bool
	}{
		{"a profile nobody was assigned stays open", viewer, d.sales.ExternalID, "profile", "ppf_open", true},
		{"the department it is assigned to may use it", viewer, d.eng.ExternalID, "profile", "ppf_eng", true},
		{"a sub-department inherits the assignment", viewer, d.platform.ExternalID, "profile", "ppf_eng", true},
		{"another department may not", viewer, d.sales.ExternalID, "profile", "ppf_eng", false},
		{"a person who is in no department may not", viewer, "", "profile", "ppf_eng", false},
		{"a tenant administrator keeps oversight", owner, "", "profile", "ppf_eng", true},
		{"a tool assigned to another department is refused", viewer, d.eng.ExternalID, "tool", "grok", false},
		{"a tool is open to the department it is assigned to", viewer, d.sales.ExternalID, "tool", "grok", true},
		{"a tool nobody was assigned stays open", viewer, d.eng.ExternalID, "tool", "claude", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := h.resolveAgent(tenant, tc.who, tc.agent, tc.typ, tc.ref)
			if got.Allowed != tc.wantAllow {
				t.Fatalf("%s/%s: allowed=%v (%s), want %v", tc.typ, tc.ref, got.Allowed, got.Reason, tc.wantAllow)
			}
		})
	}
	// The same through the session an agent runs.
	h.createSession(tenant, "platform-session", d.platform.ID, d.platformWorkspace)
	if got, err := h.resolver.ResolveForSession(t.Context(), tenant, viewer, "platform-session", "profile", "ppf_eng"); err != nil || !got.Allowed {
		t.Errorf("a session of a sub-department inherits the assignment, got %+v %v", got, err)
	}
}

// An assignment binds its own tenant: the same reference in another organization stays open.
func TestAProfileAssignmentBindsItsOwnTenantOnly(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment())
	admin := h.adminLogin()
	acme, globex := h.createOrg(admin, "acme"), h.createOrg(admin, "globex")
	h.createWorkspace(acme, "eng")
	h.mustAssign(admin, acme, "profile", "ppf_shared", "workspace", "eng")
	acmeViewer := h.principalFor(admin, acme, "viewer@acme.io", auth.RoleViewer)
	globexViewer := h.principalFor(admin, globex, "viewer@globex.io", auth.RoleViewer)

	if d := h.resolveHuman(acme, acmeViewer, "profile", "ppf_shared"); d.Allowed || !d.Bound {
		t.Errorf("in the tenant that assigned it, a person outside the department must be refused, got %+v", d)
	}
	if d := h.resolveHuman(globex, globexViewer, "profile", "ppf_shared"); !d.Allowed || d.Bound {
		t.Errorf("another tenant's profile with the same reference must stay open and unbound, got %+v", d)
	}
}

func TestProfileAssignedToADirectoryGroupAndForbiddenToAUser(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	member, groups := h.principalWithGroups(admin, tenant, "dev@acme.io", auth.RoleEditor, "devs")
	outsider := h.principalFor(admin, tenant, "other@acme.io", auth.RoleEditor)

	h.mustAssign(admin, tenant, "profile", "ppf_devs", "user_group", groups[0])
	if d := h.resolveHuman(tenant, member, "profile", "ppf_devs"); !d.Allowed {
		t.Errorf("a member of the assigned group may use the profile, got %+v", d)
	}
	if d := h.resolveHuman(tenant, outsider, "profile", "ppf_devs"); d.Allowed {
		t.Errorf("a person outside the group must be refused, got %+v", d)
	}

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "profile", "source_ref": "ppf_devs", "scope_tree": "user", "scope_ref": member.UserID.String(), "effect": "forbid", "enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("forbid = %d %s", r.code, r.raw)
	}
	if d := h.resolveHuman(tenant, member, "profile", "ppf_devs"); d.Allowed {
		t.Errorf("a forbid on the person overrides the group assignment, got %+v", d)
	}
}

// A profile given to several departments is usable by the agents of each, and a forbid on
// one department binds that department and nobody else.
func TestProfileAssignedToSeveralDepartmentsServesEachOfThem(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	approver := h.tokenFor(admin, tenant, "approver@acme.io", auth.RoleAdmin)
	d := newDepartments(t, h, tenant)
	legal := h.createAgent(tenant, "legal-bot", h.createWorkspace(tenant, "legal"))
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)

	h.mustAssign(admin, tenant, "profile", "ppf_shared", "workspace", "eng")
	h.createBindingApproved(admin, approver, tenant, map[string]any{
		"source_type": "profile", "source_ref": "ppf_shared", "scope_tree": "workspace", "scope_ref": "sales", "enabled": true,
	})
	for name, bot := range map[string]model.Agent{"engineering": d.eng, "its sub-department": d.platform, "sales": d.sales} {
		if got := h.resolveAgent(tenant, viewer, bot.ExternalID, "profile", "ppf_shared"); !got.Allowed || !strings.Contains(got.Reason, "in scope") {
			t.Errorf("%s must use a profile assigned to it among several departments, got %+v", name, got)
		}
	}
	if got := h.resolveAgent(tenant, viewer, legal.ExternalID, "profile", "ppf_shared"); got.Allowed {
		t.Errorf("a department the profile was not given to must be refused, got %+v", got)
	}

	h.mustAssign(admin, tenant, "profile", "ppf_open_but_sales", "workspace", "eng")
	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "profile", "source_ref": "ppf_open_but_sales", "scope_tree": "workspace", "scope_ref": "sales", "effect": "forbid", "enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("forbid = %d %s", r.code, r.raw)
	}
	if got := h.resolveAgent(tenant, viewer, d.eng.ExternalID, "profile", "ppf_open_but_sales"); !got.Allowed {
		t.Errorf("a forbid on sales must not deny engineering, got %+v", got)
	}
	if got := h.resolveAgent(tenant, viewer, d.sales.ExternalID, "profile", "ppf_open_but_sales"); got.Allowed {
		t.Errorf("a forbid on sales denies sales, got %+v", got)
	}
}

// A filter on the department cuts the tenant-wide role that reaches it from above: a tenant
// administrator no longer opens the profile there, while the department's own agents keep
// it. Another class on the same node is untouched.
func TestInheritanceFilterCutsTheTenantRoleFromAnAssignedProfile(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	engID := h.createWorkspace(tenant, "eng")
	engBot := h.createAgent(tenant, "eng-bot", engID)
	owner := h.principalFor(admin, tenant, "owner@acme.io", auth.RoleAdmin)
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	h.mustAssign(admin, tenant, "profile", "ppf_eng", "workspace", "eng")
	h.mustAssign(admin, tenant, "tool", "codex", "workspace", "eng")

	if d := h.resolveHuman(tenant, owner, "profile", "ppf_eng"); !d.Allowed {
		t.Fatalf("baseline: the tenant role reaches the profile, got %+v", d)
	}
	r := h.do("POST", "/v1/m/governance/rbac/inheritance-filters", admin,
		map[string]any{"scope_tree": "workspace", "scope_ref": "eng", "scope_class": "provider"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("filter = %d %s", r.code, r.raw)
	}
	for _, ref := range [][2]string{{"profile", "ppf_eng"}, {"tool", "codex"}} {
		if d := h.resolveHuman(tenant, owner, ref[0], ref[1]); d.Allowed {
			t.Errorf("%s: a filter on the department removes the role that reaches it from above, got %+v", ref[0], d)
		}
		if d := h.resolveAgent(tenant, viewer, engBot.ExternalID, ref[0], ref[1]); !d.Allowed {
			t.Errorf("%s: the department's own agents are assigned at the filtered node and keep it, got %+v", ref[0], d)
		}
	}
	if rr := h.do("DELETE", "/v1/m/governance/rbac/inheritance-filters/"+r.body["id"].(string), admin, nil, tenantHdr(tenant)); rr.code != http.StatusNoContent {
		t.Fatalf("delete filter = %d %s", rr.code, rr.raw)
	}
	if d := h.resolveHuman(tenant, owner, "profile", "ppf_eng"); !d.Allowed {
		t.Errorf("with the filter gone the role reaches the profile again, got %+v", d)
	}
}

// A person who administers one department by a scoped grant (not confined to it, not a
// tenant administrator) uses the profiles assigned to that department and to the ones
// below it; an inheritance filter on a sub-department cuts the grant that reaches it from
// above, and the department's own assignment stays. This is the delegated-administrator path
// of the same decision.
func TestAScopedGrantOnADepartmentReachesItsSubDepartmentsUntilAFilterCutsIt(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspace(tenant, "sales")
	delegate := h.principalFor(admin, tenant, "delegate@acme.io", auth.RoleViewer)

	h.mustAssign(admin, tenant, "profile", "ppf_eng", "workspace", "eng")
	h.mustAssign(admin, tenant, "profile", "ppf_platform", "workspace", "platform")
	h.mustAssign(admin, tenant, "profile", "ppf_sales", "workspace", "sales")
	for _, ref := range []string{"ppf_eng", "ppf_platform", "ppf_sales"} {
		if d := h.resolveHuman(tenant, delegate, "profile", ref); d.Allowed {
			t.Fatalf("baseline: a viewer with no grant must not use %s, got %+v", ref, d)
		}
	}

	h.publishGrant(admin, tenant, `permit(principal, action == Action::"provider:read", resource == Resource::"*") when { resource in Workspace::"eng" };`)
	for ref, want := range map[string]bool{"ppf_eng": true, "ppf_platform": true, "ppf_sales": false} {
		if d := h.resolveHuman(tenant, delegate, "profile", ref); d.Allowed != want {
			t.Errorf("with a grant on eng, %s allowed=%v (%s), want %v", ref, d.Allowed, d.Reason, want)
		}
	}

	r := h.do("POST", "/v1/m/governance/rbac/inheritance-filters", admin,
		map[string]any{"scope_tree": "workspace", "scope_ref": "platform", "scope_class": "provider"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("filter = %d %s", r.code, r.raw)
	}
	for ref, want := range map[string]bool{"ppf_eng": true, "ppf_platform": false, "ppf_sales": false} {
		if d := h.resolveHuman(tenant, delegate, "profile", ref); d.Allowed != want {
			t.Errorf("with a filter on platform, %s allowed=%v (%s), want %v", ref, d.Allowed, d.Reason, want)
		}
	}
}

// A reference that would match nothing is refused when it is written, not stored: the
// gate matches it byte for byte and an assignment that never matches leaves the profile open.
func TestProfileAndToolAssignmentsNameWhatTheGateAsksAbout(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	for _, tc := range []struct {
		name, typ, ref string
		want           int
	}{
		{"a profile without its reference prefix", "profile", "engineering tools", http.StatusBadRequest},
		{"a tool that does not exist", "tool", "geminii", http.StatusBadRequest},
		{"a tool is lower-cased", "tool", "Claude", http.StatusCreated},
		{"a real profile reference", "profile", "ppf_" + model.NewID().String(), http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.createBinding(admin, tenant, map[string]any{
				"source_type": tc.typ, "source_ref": tc.ref, "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
			})
			if r.code != tc.want {
				t.Fatalf("assign %s/%s = %d %s, want %d", tc.typ, tc.ref, r.code, r.raw, tc.want)
			}
			if r.code == http.StatusCreated && tc.typ == "tool" && r.body["source_ref"] != "claude" {
				t.Fatalf("stored tool = %v, want claude", r.body["source_ref"])
			}
		})
	}
}
