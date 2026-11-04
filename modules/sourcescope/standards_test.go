// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// A department carries its standards: the instruction text and the default session template
// are session templates assigned to it by a binding (source types "instructions" and
// "template"), so the department tree, forbids and disabling are the ones every other source
// obeys. Assigning is Business; deciding is every edition.

const (
	tplOrg      = "0195f0a0-0000-7000-8000-000000000001"
	tplPlatform = "0195f0a0-0000-7000-8000-000000000002"
	tplSales    = "0195f0a0-0000-7000-8000-000000000003"
	tplEng      = "0195f0a0-0000-7000-8000-000000000004"
	tplRole     = "0195f0a0-0000-7000-8000-000000000005"
	tplGroup    = "0195f0a0-0000-7000-8000-000000000006"
	tplOk       = "0195f0a0-0000-7000-8000-000000000007"
	tplDevs     = "0195f0a0-0000-7000-8000-000000000008"
	tplA        = "0195f0a0-0000-7000-8000-000000000009"
	tplB        = "0195f0a0-0000-7000-8000-000000000010"
	tplG        = "0195f0a0-0000-7000-8000-000000000011"
	tplH        = "0195f0a0-0000-7000-8000-000000000012"
	tplX        = "0195f0a0-0000-7000-8000-000000000013"
)

// The access preview answers whether an actor may use a source. A standard is not used by an
// actor, it is assigned to a department, so the preview refuses it instead of answering a
// question nobody asked.
func TestTheAccessPreviewRefusesAStandard(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createAgent(tenant, "agent-a", h.createWorkspace(tenant, "team-a"))
	for _, sourceType := range []string{"instructions", "template"} {
		path := "/v1/m/sourcescope/resolve?source_type=" + sourceType + "&source_ref=" + tplOrg + "&actor_kind=agent&actor_ref=agent-a"
		r := h.do("GET", path, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "assigned to a department") {
			t.Errorf("preview of source_type %s = %d %s; want 400 saying it is assigned to a department", sourceType, r.code, r.raw)
		}
	}
}

// Disabling every assignment of a template is a one-way relaxation like disabling the scoping of a
// source, and a second administrator approves it; the request must say what it does to a standard,
// not that the source "becomes global".
func TestDisablingEveryAssignmentOfAStandardSaysWhatItDoes(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, sourceType := range []string{"instructions", "template"} {
		r := h.do("POST", "/v1/m/sourcescope/sources/disable-scoping", admin, map[string]any{"source_type": sourceType, "source_ref": tplOrg}, tenantHdr(tenant))
		reason, _ := r.body["reason"].(string)
		if r.code != http.StatusAccepted || strings.Contains(reason, "global") || !strings.Contains(reason, "no department or group") {
			t.Errorf("disable-scoping of source_type %s = %d %s; want 202 with a reason saying no department or group is given it", sourceType, r.code, r.raw)
		}
	}
}

// A misspelt kind of standard must not read as a department that carries nothing.
func TestAnUnknownKindOfStandardIsAnErrorNotAnEmptyDepartment(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	if got, err := h.resolver.AssignedTo(t.Context(), tenant, auth.Principal{}, []string{"eng"}, "instructoins"); err == nil {
		t.Errorf("AssignedTo with a kind that is not a standard = %v, nil; want an error", got)
	}
}

// assigned is what a department alone carries (a workstation has no person).
func (h *harness) assigned(tenant model.TenantID, department, sourceType string) []string {
	h.t.Helper()
	return h.assignedTo(tenant, auth.Principal{}, []string{department}, sourceType)
}

// assignedTo is what a person who belongs to the departments carries.
func (h *harness) assignedTo(tenant model.TenantID, p auth.Principal, departments []string, sourceType string) []string {
	h.t.Helper()
	got, err := h.resolver.AssignedTo(h.t.Context(), tenant, p, departments, sourceType)
	if err != nil {
		h.t.Fatalf("AssignedTo(%v, %s): %v", departments, sourceType, err)
	}
	if got == nil {
		return []string{}
	}
	return got
}

// confineTo creates a member of the tenant whose membership is confined to one department.
func (h *harness) confineTo(admin string, tenant model.TenantID, email string, workspace model.ID) auth.Principal {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create %s = %d %s", email, r.code, r.raw)
	}
	if err := h.st.AuthMutate(h.t.Context(), func(as store.AuthScope) error {
		_, err := as.Memberships().Create(h.t.Context(), model.Membership{
			UserID: model.ID(r.body["id"].(string)), TargetTenantID: tenant, Role: auth.RoleEditor, WorkspaceID: workspace,
		})
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
	}
	p, err := h.authr.Authenticate(h.t.Context(), lr.body["token"].(string))
	if err != nil {
		h.t.Fatalf("authenticate %s: %v", email, err)
	}
	return p
}

var approvers int

// lift forbids a standard on a department or group. Lifting a standard is the relaxation here, so
// it waits for a second administrator, who approves it.
func (h *harness) lift(admin string, tenant model.TenantID, template, tree, ref string) {
	h.t.Helper()
	h.liftWith(admin, tenant, "forbid", template, tree, ref)
}

// liftWith lifts a standard with the given effect (forbid, or decline on a department).
func (h *harness) liftWith(admin string, tenant model.TenantID, effect, template, tree, ref string) {
	h.t.Helper()
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "instructions", "source_ref": template, "scope_tree": tree, "scope_ref": ref, "effect": effect, "enabled": true,
	})
	if r.code != http.StatusAccepted {
		h.t.Fatalf("lifting a standard with %s = %d %s, want 202 pending a second approver", effect, r.code, r.raw)
	}
	approvers++
	approver := h.tokenFor(admin, tenant, fmt.Sprintf("approver%d@acme.io", approvers), auth.RoleAdmin)
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+r.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		h.t.Fatalf("approving the lift = %d %s", a.code, a.raw)
	}
}

func TestAssigningInstructionsAndTemplatesNeedsBusiness(t *testing.T) {
	for _, typ := range []string{"instructions", "template"} {
		t.Run(typ, func(t *testing.T) {
			h := newHarness(t) // the Community build: no assignment option
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			h.createWorkspace(tenant, "eng")

			r := h.createBinding(admin, tenant, map[string]any{
				"source_type": typ, "source_ref": tplEng, "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
			})
			if r.code != http.StatusNotImplemented || errorCode(r) != "business_required" {
				t.Fatalf("assign in Community = %d %s, want 501 business_required", r.code, r.raw)
			}
			if got := h.assigned(tenant, "eng", typ); len(got) != 0 {
				t.Fatalf("a refused assignment must store nothing, got %v", got)
			}
		})
	}
}

func TestExpiredStandardsAreNotAssigned(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	h.mustAssign(admin, tenant, "instructions", tplPlatform, "workspace", "eng")
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "instructions", "source_ref": tplOrg, "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
		"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create expiring instructions = %d %s", r.code, r.raw)
	}
	setBindingExpiry(t, h, tenant, r.body["id"].(string), time.Now().Add(-time.Minute))
	got := h.assigned(tenant, "eng", "instructions")
	if !reflect.DeepEqual(got, []string{tplPlatform}) {
		t.Fatalf("assigned instructions = %v, want only the unexpired %s", got, tplPlatform)
	}
}

func TestDepartmentStandardsFollowTheDepartmentTree(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspace(tenant, "sales")

	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplPlatform, "workspace", "platform")
	h.mustAssign(admin, tenant, "instructions", tplSales, "workspace", "sales")

	cases := []struct {
		department string
		want       []string
	}{
		{"eng", []string{tplOrg}},
		// A sub-department inherits its parent's, parent first.
		{"platform", []string{tplOrg, tplPlatform}},
		{"sales", []string{tplSales}},
		// A department nobody assigned anything to has none.
		{"unknown", []string{}},
	}
	for _, c := range cases {
		if got := h.assigned(tenant, c.department, "instructions"); !reflect.DeepEqual(got, c.want) {
			t.Errorf("instructions of %s = %v, want %v", c.department, got, c.want)
		}
	}
	// The two source types are separate: an instruction is not a default template.
	if got := h.assigned(tenant, "platform", "template"); len(got) != 0 {
		t.Errorf("templates of platform = %v, want none", got)
	}
}

func TestAForbidAtASubDepartmentHolds(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	platform := h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspaceUnder(tenant, "edge", platform)

	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplPlatform, "workspace", "platform")
	h.lift(admin, tenant, tplOrg, "workspace", "platform")

	for department, want := range map[string][]string{
		"eng":      {tplOrg},
		"platform": {tplPlatform},
		// The forbid reaches the sub-departments below it too, and is absolute.
		"edge": {tplPlatform},
	} {
		if got := h.assigned(tenant, department, "instructions"); !reflect.DeepEqual(got, want) {
			t.Errorf("instructions of %s = %v, want %v", department, got, want)
		}
	}
}

// A sub-department can stop one standard from reaching it without forbidding it: the decline drops
// what an ancestor assigned, and only that one, while a forbid is absolute. What the sub-department
// or a department below it assigns itself still applies.
func TestASubDepartmentDeclinesOneInheritedStandardWithoutForbiddingIt(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	platform := h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspaceUnder(tenant, "edge", platform)

	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplA, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplPlatform, "workspace", "platform")

	// Declining is a relaxation like a forbid: a second administrator approves it, and it
	// applies to departments only, to the two standards only.
	for name, body := range map[string]map[string]any{
		"on a directory group": {"source_type": "instructions", "source_ref": tplOrg, "scope_tree": "user_group", "scope_ref": "devs", "effect": "decline", "enabled": true},
		"on a tool":            {"source_type": "tool", "source_ref": "claude", "scope_tree": "workspace", "scope_ref": "platform", "effect": "decline", "enabled": true},
	} {
		if r := h.createBinding(admin, tenant, body); r.code != http.StatusBadRequest || !strings.Contains(r.raw, "decline applies to") {
			t.Errorf("decline %s = %d %s, want 400 saying where a decline applies", name, r.code, r.raw)
		}
	}
	pending := h.createBinding(admin, tenant, map[string]any{
		"source_type": "instructions", "source_ref": tplOrg, "scope_tree": "workspace", "scope_ref": "platform", "effect": "decline", "enabled": true,
	})
	if pending.code != http.StatusAccepted {
		t.Fatalf("a decline = %d %s, want 202 pending a second administrator", pending.code, pending.raw)
	}
	if got := h.assigned(tenant, "platform", "instructions"); !reflect.DeepEqual(got, []string{tplOrg, tplA, tplPlatform}) {
		t.Fatalf("a pending decline already dropped a standard: %v", got)
	}
	approvers++
	approver := h.tokenFor(admin, tenant, fmt.Sprintf("approver%d@acme.io", approvers), auth.RoleAdmin)
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+pending.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approving the decline = %d %s", a.code, a.raw)
	}

	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "edge") // a department below assigns it back
	for department, want := range map[string][]string{
		"eng":      {tplOrg, tplA},
		"platform": {tplA, tplPlatform}, // tplOrg stops reaching platform; tplA still does
		"edge":     {tplA, tplPlatform, tplOrg},
	} {
		if got := h.assigned(tenant, department, "instructions"); !reflect.DeepEqual(got, want) {
			t.Errorf("instructions of %s = %v, want %v", department, got, want)
		}
	}
}

// A person belongs to every department their directory groups are filed in. A decline drops only
// what reaches its own department from the departments above it: sales is not above platform, so
// what sales assigns still reaches a person in both.
func TestASubDepartmentDeclinesNothingAnotherDepartmentOfThePersonAssigns(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspace(tenant, "sales")

	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "sales")
	h.liftWith(admin, tenant, "decline", tplOrg, "workspace", "platform")

	if got := h.assigned(tenant, "sales", "instructions"); !reflect.DeepEqual(got, []string{tplOrg}) {
		t.Fatalf("sales alone = %v, want [%s]", got, tplOrg)
	}
	if got := h.assignedTo(tenant, auth.Principal{}, []string{"platform", "sales"}, "instructions"); !reflect.DeepEqual(got, []string{tplOrg}) {
		t.Errorf("a person in platform and sales = %v, want [%s]: sales assigns it, and sales is not above platform", got, tplOrg)
	}
}

// A forbid also lifts what the department and the ones below it assign themselves, and creating
// one waits for a second administrator, so turning an approved decline into a forbid waits too.
func TestASubDepartmentDeclinesAndAForbidInItsPlaceWaitsForASecondAdministrator(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	platform := h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspaceUnder(tenant, "edge", platform)

	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "edge")
	h.liftWith(admin, tenant, "decline", tplOrg, "workspace", "platform")
	if got := h.assigned(tenant, "edge", "instructions"); !reflect.DeepEqual(got, []string{tplOrg}) {
		t.Fatalf("edge after the decline = %v, want [%s]", got, tplOrg)
	}
	var id string
	for _, it := range h.do("GET", "/v1/m/sourcescope/bindings?source_type=instructions", admin, nil, tenantHdr(tenant)).body["items"].([]any) {
		if b := it.(map[string]any); b["effect"] == "decline" {
			id = b["id"].(string)
		}
	}
	if id == "" {
		t.Fatal("the decline binding is not listed")
	}
	put := h.do("PUT", "/v1/m/sourcescope/bindings/"+id, admin, map[string]any{
		"source_type": "instructions", "source_ref": tplOrg, "scope_tree": "workspace", "scope_ref": "platform", "effect": "forbid", "enabled": true,
	}, tenantHdr(tenant))
	if put.code != http.StatusAccepted {
		t.Errorf("decline -> forbid = %d %s, want 202 pending a second administrator; edge now carries %v",
			put.code, put.raw, h.assigned(tenant, "edge", "instructions"))
	}
	if got := h.assigned(tenant, "edge", "instructions"); !reflect.DeepEqual(got, []string{tplOrg}) {
		t.Errorf("edge while the forbid waits = %v, want [%s]", got, tplOrg)
	}
}

func TestADisabledAssignmentAssignsNothing(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "instructions", "source_ref": tplEng, "scope_tree": "workspace", "scope_ref": "eng", "enabled": false,
	}); r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if got := h.assigned(tenant, "eng", "instructions"); len(got) != 0 {
		t.Fatalf("a disabled assignment assigned %v", got)
	}
}

// Only a department and a directory group are assigned standards: a binding on a role, a
// person or an agent decides who may use a source, not what someone carries, so it is refused
// instead of being stored and ignored.
func TestOnlyDepartmentsAndGroupsAreAssignedStandards(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	for _, tree := range []struct{ tree, ref string }{{"role", "viewer"}, {"user", "u1"}, {"agent", "bot"}, {"agent_group", "bots"}, {"session", "s1"}} {
		r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "instructions", "source_ref": tplX, "scope_tree": tree.tree, "scope_ref": tree.ref, "enabled": true,
		})
		if r.code != http.StatusBadRequest {
			t.Errorf("instructions on %s = %d %s, want 400", tree.tree, r.code, r.raw)
		}
	}
	if got := h.assigned(tenant, "eng", "instructions"); len(got) != 0 {
		t.Fatalf("a refused binding assigned %v to a department", got)
	}
	h.mustAssign(admin, tenant, "instructions", tplOk, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplGroup, "user_group", "grp-1")
}

func TestADirectoryGroupIsAssignedStandardsToo(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	member, groups := h.principalWithGroups(admin, tenant, "dev@acme.io", auth.RoleEditor, "devs")
	outsider := h.principalFor(admin, tenant, "other@acme.io", auth.RoleEditor)

	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplDevs, "user_group", groups[0])

	// The group's member carries the group's, after the department's.
	if got := h.assignedTo(tenant, member, []string{"eng"}, "instructions"); !reflect.DeepEqual(got, []string{tplOrg, tplDevs}) {
		t.Errorf("a member of devs in eng carries %v, want [tpl_org tpl_devs]", got)
	}
	if got := h.assignedTo(tenant, member, nil, "instructions"); !reflect.DeepEqual(got, []string{tplDevs}) {
		t.Errorf("a member of devs in no department carries %v, want [tpl_devs]", got)
	}
	if got := h.assignedTo(tenant, outsider, nil, "instructions"); len(got) != 0 {
		t.Errorf("a person in no group and no department carries %v", got)
	}
	// A department alone has no person, so no group's standards.
	if got := h.assigned(tenant, "eng", "instructions"); !reflect.DeepEqual(got, []string{tplOrg}) {
		t.Errorf("eng alone carries %v, want [tpl_org]", got)
	}
	// A forbid on the group is absolute for its members.
	h.lift(admin, tenant, tplOrg, "user_group", groups[0])
	if got := h.assignedTo(tenant, member, []string{"eng"}, "instructions"); !reflect.DeepEqual(got, []string{tplDevs}) {
		t.Errorf("with eng's instructions forbidden to devs a member carries %v, want [tpl_devs]", got)
	}
}

func TestADepartmentHasOneDefaultTemplate(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspaceUnder(tenant, "platform", eng)

	h.mustAssign(admin, tenant, "template", tplA, "workspace", "eng")
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "template", "source_ref": tplB, "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
	})
	if r.code != http.StatusConflict {
		t.Fatalf("a second default template on one department = %d %s, want 409", r.code, r.raw)
	}
	// The same holds for a directory group.
	h.mustAssign(admin, tenant, "template", tplG, "user_group", "grp-1")
	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "template", "source_ref": tplH, "scope_tree": "user_group", "scope_ref": "grp-1", "enabled": true,
	}); r.code != http.StatusConflict {
		t.Fatalf("a second default template on one group = %d %s, want 409", r.code, r.raw)
	}
	// A sub-department may have its own, and the nearest one wins.
	h.mustAssign(admin, tenant, "template", tplB, "workspace", "platform")
	if got := h.assigned(tenant, "eng", "template"); !reflect.DeepEqual(got, []string{tplA}) {
		t.Errorf("default of eng = %v, want [tpl_a]", got)
	}
	if got := h.assigned(tenant, "platform", "template"); !reflect.DeepEqual(got, []string{tplA, tplB}) {
		t.Errorf("templates reaching platform = %v, want [tpl_a tpl_b] (nearest last)", got)
	}
}

// The ADR folder of a department is its workspace setting adr_folder, read from the store: a
// department without one or without the setting names none, and a setting that is not text is
// an error, never an empty folder.
func TestTheADRFolderOfADepartmentIsItsWorkspaceSetting(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for slug, settings := range map[string]map[string]any{
			"eng":    {"adr_folder": "docs/adr"},
			"ops":    nil,
			"broken": {"adr_folder": 7},
		} {
			if _, err := sc.Workspaces().Create(context.Background(), model.Workspace{
				Name: slug, Slug: slug, Status: model.StatusActive, Settings: settings,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create departments: %v", err)
	}
	for department, want := range map[string]string{"eng": "docs/adr", "ops": "", "nowhere": ""} {
		if got, err := h.resolver.ADRFolder(t.Context(), tenant, department); err != nil || got != want {
			t.Errorf("ADRFolder(%s) = %q, %v, want %q", department, got, err, want)
		}
	}
	if got, err := h.resolver.ADRFolder(t.Context(), tenant, "broken"); !errors.Is(err, sourcescope.ErrSettingNotText) || got != "" {
		t.Errorf("ADRFolder(broken) = %q, %v, want ErrSettingNotText", got, err)
	}
}

// A mandatory template is a restriction like a forbid: the tenant expiry policy neither
// stamps it with a lapse date nor refuses it, and only a second administrator can give it one.
func TestAMandatoryTemplateKeepsOnlyTheExpiryItsWriterSent(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "Access expiry", Kind: "grant_expiry", Enabled: true,
			Spec: map[string]any{"default_seconds": 3600, "maximum_seconds": 7200},
		})
		return err
	}); err != nil {
		t.Fatalf("seed expiry policy: %v", err)
	}

	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "template", "source_ref": tplA, "scope_tree": "workspace", "scope_ref": "eng", "effect": "require", "enabled": true,
	})
	if got, _ := r.body["expires_at"].(string); r.code != http.StatusCreated || got != "" {
		t.Fatalf("a mandatory template must not inherit the tenant default expiry: %d %s", r.code, r.raw)
	}
	put := h.do("PUT", "/v1/m/sourcescope/bindings/"+r.body["id"].(string), admin, map[string]any{
		"source_type": "template", "source_ref": tplA, "scope_tree": "workspace", "scope_ref": "eng", "effect": "require", "enabled": true,
		"expires_at": time.Now().Add(time.Minute).Format(time.RFC3339),
	}, tenantHdr(tenant))
	if put.code != http.StatusAccepted {
		t.Fatalf("giving a mandatory template an expiry = %d %s, want 202 pending a second administrator", put.code, put.raw)
	}
	if got := h.assigned(tenant, "eng", "required-template"); !reflect.DeepEqual(got, []string{tplA}) {
		t.Errorf("a pending expiry changed what is mandatory: %v", got)
	}
}

// A template assigned with effect require is mandatory: it is also the department's template
// (kind "template"), it is the only one kind "required-template" lists, a sub-department inherits
// it and a forbid lifts it, a department still holds one template, and relaxing it to a default
// waits for a second administrator. Only a template can be required.
func TestADepartmentCanRequireATemplate(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspaceUnder(tenant, "platform", eng)

	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "instructions", "source_ref": tplEng, "scope_tree": "workspace", "scope_ref": "eng", "effect": "require", "enabled": true,
	}); r.code != http.StatusBadRequest || !strings.Contains(r.raw, "template") {
		t.Fatalf("requiring instructions = %d %s, want 400 saying only a template can be required", r.code, r.raw)
	}
	r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "template", "source_ref": tplA, "scope_tree": "workspace", "scope_ref": "eng", "effect": "require", "enabled": true,
	})
	if r.code != http.StatusCreated || r.body["effect"] != "require" {
		t.Fatalf("requiring a template = %d %s, want 201 with effect require", r.code, r.raw)
	}
	if r2 := h.createBinding(admin, tenant, map[string]any{
		"source_type": "template", "source_ref": tplB, "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
	}); r2.code != http.StatusConflict {
		t.Fatalf("a default beside a mandatory template on one department = %d %s, want 409", r2.code, r2.raw)
	}
	h.mustAssign(admin, tenant, "template", tplB, "workspace", "platform") // a default of the sub-department

	for _, c := range []struct {
		department, kind string
		want             []string
	}{
		{"eng", "template", []string{tplA}},
		{"eng", "required-template", []string{tplA}},
		{"platform", "template", []string{tplA, tplB}},
		{"platform", "required-template", []string{tplA}}, // inherited; the sub-department's default is not mandatory
	} {
		if got := h.assigned(tenant, c.department, c.kind); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s of %s = %v, want %v", c.kind, c.department, got, c.want)
		}
	}

	// Relaxing the requirement to a default is a relaxation: a second administrator approves it.
	put := h.do("PUT", "/v1/m/sourcescope/bindings/"+r.body["id"].(string), admin, map[string]any{
		"source_type": "template", "source_ref": tplA, "scope_tree": "workspace", "scope_ref": "eng", "effect": "allow", "enabled": true,
	}, tenantHdr(tenant))
	if put.code != http.StatusAccepted {
		t.Fatalf("relaxing a mandatory template to a default = %d %s, want 202 pending a second administrator", put.code, put.raw)
	}
	if got := h.assigned(tenant, "eng", "required-template"); !reflect.DeepEqual(got, []string{tplA}) {
		t.Errorf("a pending relaxation changed what is mandatory: %v", got)
	}

	// A forbid at the sub-department lifts the requirement there only, once a second
	// administrator approves it.
	lift := h.createBinding(admin, tenant, map[string]any{
		"source_type": "template", "source_ref": tplA, "scope_tree": "workspace", "scope_ref": "platform", "effect": "forbid", "enabled": true,
	})
	if lift.code != http.StatusAccepted {
		t.Fatalf("lifting a mandatory template = %d %s, want 202", lift.code, lift.raw)
	}
	approvers++
	approver := h.tokenFor(admin, tenant, fmt.Sprintf("approver%d@acme.io", approvers), auth.RoleAdmin)
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+lift.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approving the lift = %d %s", a.code, a.raw)
	}
	if got := h.assigned(tenant, "platform", "required-template"); len(got) != 0 {
		t.Errorf("mandatory templates of platform after a forbid = %v, want none", got)
	}
	if got := h.assigned(tenant, "eng", "required-template"); !reflect.DeepEqual(got, []string{tplA}) {
		t.Errorf("a forbid on platform changed eng: %v", got)
	}
}

// Requiring a template asks no second administrator, so a sub-department can require another one
// beside its parent's. The resolver lists the outer requirement first, and a launch obeys the first.
func TestANearerRequirementIsListedAfterTheOuterOne(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspaceUnder(tenant, "platform", eng)

	for _, c := range []struct{ template, department string }{{tplA, "eng"}, {tplB, "platform"}} {
		if r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "template", "source_ref": c.template, "scope_tree": "workspace", "scope_ref": c.department, "effect": "require", "enabled": true,
		}); r.code != http.StatusCreated {
			t.Fatalf("requiring %s on %s = %d %s, want 201", c.template, c.department, r.code, r.raw)
		}
	}
	if got := h.assigned(tenant, "platform", "required-template"); !reflect.DeepEqual(got, []string{tplA, tplB}) {
		t.Errorf("mandatory templates of platform = %v, want the parent's first: [%s %s]", got, tplA, tplB)
	}
}

// Requiring a template asks nobody, so requiring a parent's mandatory template again nearer to
// the launch, on a sub-department or a directory group, must not move it behind a middle
// department's: a mandatory template stands where it is required farthest out.
func TestAParentsRequirementRequiredAgainNearerStillComesFirst(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	platform := h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspaceUnder(tenant, "core", platform)
	member, groups := h.principalWithGroups(admin, tenant, "dev@acme.io", auth.RoleEditor, "devs")

	for _, c := range []struct{ template, tree, ref string }{
		{tplA, "workspace", "eng"}, {tplB, "workspace", "platform"}, {tplA, "workspace", "core"}, {tplA, "user_group", groups[0]},
	} {
		if r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "template", "source_ref": c.template, "scope_tree": c.tree, "scope_ref": c.ref, "effect": "require", "enabled": true,
		}); r.code != http.StatusCreated {
			t.Fatalf("requiring %s on %s = %d %s, want 201", c.template, c.ref, r.code, r.raw)
		}
	}
	want := []string{tplA, tplB}
	if got := h.assigned(tenant, "core", "required-template"); !reflect.DeepEqual(got, want) {
		t.Errorf("mandatory templates of core = %v, want eng's first: %v", got, want)
	}
	if got := h.assignedTo(tenant, member, []string{"platform"}, "required-template"); !reflect.DeepEqual(got, want) {
		t.Errorf("mandatory templates of a devs member in platform = %v, want eng's first: %v", got, want)
	}
}

// A requirement a decline drops does not stand where it was made: when platform declines eng's
// mandatory template and core requires it again, it stands at core, behind what mid requires.
func TestADeclinedRequirementStandsWhereItIsRequiredAgain(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	platform := h.createWorkspaceUnder(tenant, "platform", eng)
	mid := h.createWorkspaceUnder(tenant, "mid", platform)
	h.createWorkspaceUnder(tenant, "core", mid)

	require := func(template, department string) {
		t.Helper()
		if r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "template", "source_ref": template, "scope_tree": "workspace", "scope_ref": department, "effect": "require", "enabled": true,
		}); r.code != http.StatusCreated {
			t.Fatalf("requiring %s on %s = %d %s, want 201", template, department, r.code, r.raw)
		}
	}
	require(tplA, "eng")
	decline := h.createBinding(admin, tenant, map[string]any{
		"source_type": "template", "source_ref": tplA, "scope_tree": "workspace", "scope_ref": "platform", "effect": "decline", "enabled": true,
	})
	if decline.code != http.StatusAccepted {
		t.Fatalf("declining %s on platform = %d %s, want 202", tplA, decline.code, decline.raw)
	}
	approvers++
	approver := h.tokenFor(admin, tenant, fmt.Sprintf("approver%d@acme.io", approvers), auth.RoleAdmin)
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+decline.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approving the decline = %d %s", a.code, a.raw)
	}
	require(tplB, "mid")
	require(tplA, "core")

	if got, want := h.assigned(tenant, "core", "required-template"), []string{tplB, tplA}; !reflect.DeepEqual(got, want) {
		t.Errorf("mandatory templates of core = %v, want mid's first, then core's own: %v", got, want)
	}
}

func TestADepartmentIsWhereAPersonOrAnAgentIsConfined(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	platform := h.createWorkspaceUnder(tenant, "platform", eng)
	bot := h.createAgent(tenant, "platform-bot", platform)
	viewer := h.principalFor(admin, tenant, "viewer@acme.io", auth.RoleViewer)

	department := func(p auth.Principal) string {
		t.Helper()
		got, err := h.resolver.DepartmentOf(t.Context(), tenant, p)
		if err != nil {
			t.Fatalf("DepartmentOf: %v", err)
		}
		return got
	}
	if got := department(viewer); got != "" {
		t.Errorf("a tenant-wide member belongs to no department, got %q", got)
	}
	if got := department(viewer.WithAgentIdentity(bot.ExternalID)); got != "platform" {
		t.Errorf("an agent's department = %q, want platform", got)
	}
	if got := department(viewer.WithAgentIdentity("nobody")); got != "" {
		t.Errorf("an unknown agent belongs to no department, got %q", got)
	}
	if got := department(h.confineTo(admin, tenant, "member@acme.io", eng)); got != "eng" {
		t.Errorf("a member confined to eng = %q, want eng", got)
	}
}

// Assigning a standard imposes it and is never gated; taking one off a department, or lifting it
// with a forbid, is the relaxation and waits for a second administrator.
func TestLiftingAStandardNeedsASecondAdministratorButAssigningDoesNot(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createWorkspaceUnder(tenant, "platform", eng)
	h.createWorkspace(tenant, "sales")

	// The same template to a second department adds nobody's freedom: it applies at once.
	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "sales")

	forbid := h.createBinding(admin, tenant, map[string]any{
		"source_type": "instructions", "source_ref": tplOrg, "scope_tree": "workspace", "scope_ref": "platform", "effect": "forbid", "enabled": true,
	})
	if forbid.code != http.StatusAccepted {
		t.Fatalf("a forbid on an assigned standard = %d %s, want 202", forbid.code, forbid.raw)
	}
	if got := h.assigned(tenant, "platform", "instructions"); !reflect.DeepEqual(got, []string{tplOrg}) {
		t.Fatalf("a pending forbid already lifted the standard: %v", got)
	}

	// Turning an assignment off, and deleting one, wait too.
	bindings := h.do("GET", "/v1/m/sourcescope/bindings?source_type=instructions", admin, nil, tenantHdr(tenant))
	var allow map[string]any
	for _, it := range bindings.body["items"].([]any) {
		if b := it.(map[string]any); b["scope_ref"] == "sales" {
			allow = b
		}
	}
	id := allow["id"].(string)
	if r := h.do("PUT", "/v1/m/sourcescope/bindings/"+id, admin, map[string]any{"scope_tree": "workspace", "scope_ref": "sales", "enabled": false}, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Errorf("turning an assignment off = %d %s, want 202", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/sourcescope/bindings/"+id, admin, nil, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Errorf("deleting an assignment = %d %s, want 202", r.code, r.raw)
	}
	// Taking a forbid away imposes the standard again, so it applies at once.
	h.lift(admin, tenant, tplDevs, "workspace", "platform")
	var lifted string
	for _, it := range h.do("GET", "/v1/m/sourcescope/bindings?source_type=instructions", admin, nil, tenantHdr(tenant)).body["items"].([]any) {
		if b := it.(map[string]any); b["effect"] == "forbid" {
			lifted = b["id"].(string)
		}
	}
	if r := h.do("DELETE", "/v1/m/sourcescope/bindings/"+lifted, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Errorf("deleting a forbid = %d %s, want 204", r.code, r.raw)
	}
}

func TestAStandardIsAssignedByTheIdOfItsTemplate(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	for _, ref := range []string{"eng-standards", "tpl_org", "", strings.ToUpper(tplOrg), "{" + tplOrg + "}", tplOrg + "-x"} {
		r := h.createBinding(admin, tenant, map[string]any{
			"source_type": "instructions", "source_ref": ref, "scope_tree": "workspace", "scope_ref": "eng", "enabled": true,
		})
		if r.code != http.StatusBadRequest {
			t.Errorf("source_ref %q = %d %s, want 400", ref, r.code, r.raw)
		}
	}
	if got := h.assigned(tenant, "eng", "instructions"); len(got) != 0 {
		t.Fatalf("a refused reference assigned %v", got)
	}
}

// A standard is not something anyone may reach, so it must not show on the access map as a
// permitted edge to the template.
func TestAStandardProjectsNoAccessEdge(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	eng := h.createWorkspace(tenant, "eng")
	h.createAgent(tenant, "bot", eng)
	// The control: a source bound to the department does project an edge for the agent in it, so
	// the absence below is the standard's and not the fixture's.
	h.mustBind(admin, tenant, "m-1", "workspace", "eng", "allow")
	controlEdges := len(h.host.edges())
	if controlEdges == 0 {
		t.Fatal("fixture: a model bound to a department published no edge for its agent")
	}
	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	if edges := h.host.edges(); len(edges) != controlEdges {
		t.Fatalf("assigning a standard published %d access edge(s) beyond the %d of the control: %+v", len(edges)-controlEdges, controlEdges, edges)
	}
}

func TestATemplateIsAssignedWhileAnEnabledAllowNamesIt(t *testing.T) {
	h := newHarness(t, sourcescope.WithProfileAssignment(licensed))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "eng")
	assigned := func(ref string) bool {
		got, err := h.resolver.IsAssigned(t.Context(), tenant, ref)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if assigned(tplOrg) {
		t.Fatal("an unassigned template reads as assigned")
	}
	h.mustAssign(admin, tenant, "instructions", tplOrg, "workspace", "eng")
	h.mustAssign(admin, tenant, "template", tplDevs, "workspace", "eng")
	if !assigned(tplOrg) || !assigned(tplDevs) {
		t.Fatal("an assigned template reads as free")
	}
	// A forbid assigns nothing: it lifts a standard from a department.
	h.lift(admin, tenant, tplGroup, "workspace", "eng")
	if assigned(tplGroup) {
		t.Fatal("a template that is only forbidden reads as assigned")
	}
	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "instructions", "source_ref": tplSales, "scope_tree": "workspace", "scope_ref": "eng", "enabled": false,
	}); r.code != http.StatusCreated {
		t.Fatalf("park = %d %s", r.code, r.raw)
	}
	if assigned(tplSales) {
		t.Fatal("a parked assignment reads as assigned")
	}
}
