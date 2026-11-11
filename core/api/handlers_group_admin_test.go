// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// adminDepartments stands for the Business service on the organization-structure
// routes that create and remove a group, edit its roster and suspend a person: it
// records what the routes asked, so the test tells a refusal before the service
// from one by it.
type adminDepartments struct {
	storeDepartments
	calls *[]string
}

func (d adminDepartments) CreateGroup(_ context.Context, _ auth.Principal, _ model.TenantID, displayName string) (model.UserGroup, error) {
	*d.calls = append(*d.calls, "create:"+displayName)
	if displayName == "taken" {
		return model.UserGroup{}, store.ErrConflict
	}
	return model.UserGroup{BaseFields: model.BaseFields{ID: "g1"}, DisplayName: displayName, ProvisionedBy: auth.GroupProvisionerOperator}, nil
}

func (d adminDepartments) DeleteGroup(_ context.Context, _ auth.Principal, _ model.TenantID, id model.ID) error {
	*d.calls = append(*d.calls, "delete:"+id.String())
	return nil
}

func (d adminDepartments) SetGroupMember(_ context.Context, _ auth.Principal, _ model.TenantID, group, user model.ID, member bool) error {
	verb := "remove"
	if member {
		verb = "add"
	}
	*d.calls = append(*d.calls, verb+":"+group.String()+":"+user.String())
	return nil
}

func (d adminDepartments) SetMemberSuspended(_ context.Context, _ auth.Principal, _ model.TenantID, user model.ID, suspended bool) error {
	verb := "reactivate"
	if suspended {
		verb = "suspend"
	}
	*d.calls = append(*d.calls, verb+":"+user.String())
	return nil
}

// The Community build wires no department service: creating and deleting a group,
// editing its roster and suspending a person answer 501 departments_unavailable.
func TestGroupAdministrationIsBusinessOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "group-admin-community")
	h.elevate(admin)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/groups", map[string]any{"display_name": "Platform"}},
		{http.MethodDelete, "/v1/groups/g1", nil},
		{http.MethodPut, "/v1/groups/g1/members/u1", nil},
		{http.MethodDelete, "/v1/groups/g1/members/u1", nil},
		{http.MethodPost, "/v1/members/u1/suspend", nil},
		{http.MethodPost, "/v1/members/u1/reactivate", nil},
	} {
		r := h.do(c.method, c.path, admin, c.body, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented || errorCodeOf(r) != "departments_unavailable" {
			t.Errorf("%s %s = %d %s, want 501 departments_unavailable", c.method, c.path, r.code, r.raw)
		}
	}
}

// With the department service wired, each route decodes its request, reaches the
// service once and maps its answer: 201 with the new group, 204 for the rest, 400
// for a nameless group and 409 for a name already taken.
func TestGroupAdministrationReachesTheDepartmentService(t *testing.T) {
	var calls []string
	h := newHarnessOpts(t, func(o *api.Options) { o.Departments = adminDepartments{storeDepartments{o.Store}, &calls} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "group-admin-business")
	h.elevate(admin)
	do := func(method, path string, body any) resp {
		return h.do(method, path, admin, body, tenantHdr(tenant))
	}
	if r := do(http.MethodPost, "/v1/groups", map[string]any{"display_name": "Platform"}); r.code != http.StatusCreated ||
		r.body["id"] != "g1" || r.body["display_name"] != "Platform" || r.body["provisioned_by"] != auth.GroupProvisionerOperator {
		t.Errorf("create group = %d %s, want 201 with the operator group", r.code, r.raw)
	}
	if r := do(http.MethodPost, "/v1/groups", map[string]any{"display_name": " "}); r.code != http.StatusBadRequest {
		t.Errorf("create nameless group = %d %s, want 400", r.code, r.raw)
	}
	if r := do(http.MethodPost, "/v1/groups", map[string]any{"display_name": "taken"}); r.code != http.StatusConflict {
		t.Errorf("create taken group = %d %s, want 409", r.code, r.raw)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/v1/groups/g1/members/u1"},
		{http.MethodDelete, "/v1/groups/g1/members/u1"},
		{http.MethodDelete, "/v1/groups/g1"},
		{http.MethodPost, "/v1/members/u1/suspend"},
		{http.MethodPost, "/v1/members/u1/reactivate"},
	} {
		if r := do(c.method, c.path, nil); r.code != http.StatusNoContent {
			t.Errorf("%s %s = %d %s, want 204", c.method, c.path, r.code, r.raw)
		}
	}
	want := []string{"create:Platform", "create:taken", "add:g1:u1", "remove:g1:u1", "delete:g1", "suspend:u1", "reactivate:u1"}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("service calls = %v, want %v", calls, want)
	}
}
