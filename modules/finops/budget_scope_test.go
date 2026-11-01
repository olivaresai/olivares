// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops_test

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
)

func TestBudgetDepartmentAdmission(t *testing.T) {
	gov := governance.New()
	h := newHarness(t, finops.New(), gov)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "budget-scope")
	member := h.roleToken(admin, tenant, "department@budget.io", auth.RoleViewer)
	tenantAdmin := h.roleToken(admin, tenant, "admin@budget.io", auth.RoleAdmin)
	ctx := context.Background()
	var department, sales model.Workspace
	var sibling model.Policy
	var own model.Policy
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, slug := range []string{"engineering", "sales"} {
			ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			if slug == "engineering" {
				department = ws
			} else {
				sales = ws
			}
			p, err := sc.Policies().Create(ctx, model.Policy{Name: slug, Kind: "budget", Enabled: true, Spec: map[string]any{"dimension": "workspace", "key": slug, "limit_micro_usd": int64(1000)}})
			if err != nil {
				return err
			}
			if slug == "sales" {
				sibling = p
			} else {
				own = p
			}
		}
		if _, err := sc.Policies().Create(ctx, model.Policy{Name: "model", Kind: "budget", Enabled: true, Spec: map[string]any{"dimension": "model", "key": "engineering", "limit_micro_usd": int64(1000)}}); err != nil {
			return err
		}
		_, err := sc.Policies().Create(ctx, model.Policy{Name: "global", Kind: "budget", Enabled: true, Spec: map[string]any{"dimension": "global", "limit_micro_usd": int64(1000)}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A department-only profile: grant inside Engineering and forbid budget access
	// outside it. Use the stored Cedar engine, including its deny-overrides behavior.
	testsupport.SeedCedar(t, h.st, tenant, `
 permit(principal in Role::"viewer", action, resource) when { resource in Workspace::"engineering" };
 forbid(principal in Role::"viewer", action, resource) when { action == Action::"finops:budget:read" || action == Action::"finops:budget:write" } unless { resource in Workspace::"engineering" };
 `, gov)
	hdr := tenantHdr(tenant)
	body := func(dimension, key string) map[string]any {
		return map[string]any{"name": "new budget", "dimension": dimension, "key": key, "limit_micro_usd": 1000, "enabled": true}
	}
	// Community must retain its edition refusal; an assembled Business tree writes
	// the real policy and returns 201 through the same route declaration.
	wantCreate := http.StatusNotImplemented
	if os.Getenv("OLIVARES_TEST_BUDGET_AUTHORING") == "1" {
		wantCreate = http.StatusCreated
	}
	create := h.do("POST", "/v1/m/finops/budgets", member, body("workspace", "engineering"), hdr)
	if create.code != wantCreate {
		t.Errorf("department create = %d %s, want %d", create.code, create.raw, wantCreate)
	}
	t.Logf("department create = %d %s", create.code, create.raw)
	listed := h.do("GET", "/v1/m/finops/budgets?workspace_id="+department.ID.String(), member, nil, hdr)
	if listed.code != http.StatusOK {
		t.Fatalf("department list = %d %s", listed.code, listed.raw)
	}
	items := listed.body["items"].([]any)
	found := false
	createdFound := create.code != http.StatusCreated
	for _, item := range items {
		row := item.(map[string]any)
		if row["dimension"] != "workspace" || row["key"] != "engineering" {
			t.Errorf("list leaked budget: %v", row)
		}
		found = found || row["id"] == own.ID.String()
		createdFound = createdFound || (create.body["id"] != "" && row["id"] == create.body["id"])
	}
	if !createdFound {
		t.Errorf("created budget absent from stored department list: %s", listed.raw)
	}
	if !found {
		t.Errorf("own budget absent: %s", listed.raw)
	}
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/budgets", body("workspace", "sales")},
		{"POST", "/budgets", body("global", "")},
		{"GET", "/budgets", nil},
		{"GET", "/budgets?workspace_id=" + sales.ID.String(), nil},
		{"GET", "/budgets/" + sibling.ID.String(), nil},
		{"GET", "/budgets/" + sibling.ID.String() + "/status", nil},
		{"PUT", "/budgets/" + sibling.ID.String() + "?workspace_id=" + department.ID.String(), body("workspace", "engineering")},
		{"DELETE", "/budgets/" + sibling.ID.String() + "?workspace_id=" + department.ID.String(), nil},
	} {
		r := h.do(tc.method, "/v1/m/finops"+tc.path, member, tc.body, hdr)
		if r.code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403", tc.method, tc.path, r.code, r.raw)
		}
	}
	for _, query := range []string{"?workspace_id=bad", "?workspace_id=", "?workspace_id=" + department.ID.String() + "&workspace_id=" + department.ID.String()} {
		r := h.do("GET", "/v1/m/finops/budgets"+query, member, nil, hdr)
		if r.code != http.StatusBadRequest {
			t.Errorf("malformed selector %s = %d %s", query, r.code, r.raw)
		}
	}
	mismatch := h.do("POST", "/v1/m/finops/budgets?workspace_id="+sales.ID.String(), member, body("workspace", "engineering"), hdr)
	if mismatch.code != http.StatusBadRequest {
		t.Errorf("mismatched selector = %d %s", mismatch.code, mismatch.raw)
	}

	all := h.do("GET", "/v1/m/finops/budgets", tenantAdmin, nil, hdr)
	if all.code != http.StatusOK || len(all.body["items"].([]any)) < 3 {
		t.Fatalf("tenant admin list = %d %s", all.code, all.raw)
	}
	external := h.do("POST", "/v1/m/finops/budgets", tenantAdmin, body("workspace", "provider workspace/ref"), hdr)
	if external.code != wantCreate {
		t.Errorf("tenant external workspace create = %d %s; want %d", external.code, external.raw, wantCreate)
	}
	tenantCreate := h.do("POST", "/v1/m/finops/budgets", tenantAdmin, body("global", ""), hdr)
	if tenantCreate.code != create.code {
		t.Errorf("tenant create = %d %s; department create = %d", tenantCreate.code, tenantCreate.raw, create.code)
	}
}
