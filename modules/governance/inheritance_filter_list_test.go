// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The console needs the complete set, not the first storage page. This endpoint
// drains pages internally and does not expose a client pagination contract.
func TestInheritanceFilterListDrainsStoragePages(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "filter-pages")
	other := h.createOrg(admin, "filter-other")
	const count = 1001 // One more than governance's internal listCap.
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.inheritance_filter"))
		if err != nil {
			return err
		}
		for i := count - 1; i >= 0; i-- {
			if _, err := repo.Create(t.Context(), model.Record{
				"scope_tree": "workspace", "scope_ref": fmt.Sprintf("workspace-%04d", i),
				"scope_class": "session", "created_by": "test",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		tenant     model.TenantID
		want       int
	}{
		{"complete set", "inheritance-filters", tenant, count},
		{"no client pagination", "inheritance-filters?limit=1", tenant, count},
		{"tenant isolation", "inheritance-filters", other, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.rbac(http.MethodGet, tc.path, admin, tc.tenant, nil)
			if r.code != http.StatusOK {
				t.Fatalf("list = %d %s", r.code, r.raw)
			}
			var out struct {
				Items   []filterItem `json:"items"`
				HasMore bool         `json:"has_more"`
				Cursor  string       `json:"cursor"`
			}
			if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
				t.Fatal(err)
			}
			if out.Items == nil || len(out.Items) != tc.want || out.HasMore || out.Cursor != "" {
				t.Fatalf("items=%d, has_more=%v, cursor=%q; want a complete %d-row array", len(out.Items), out.HasMore, out.Cursor, tc.want)
			}
			for i, item := range out.Items {
				if item.ScopeRef != fmt.Sprintf("workspace-%04d", i) || item.ScopeTree != "workspace" || item.ScopeClass != "session" || item.ID == "" {
					t.Fatalf("row %d = %+v; want the sorted, complete filter set", i, item)
				}
			}
		})
	}
}
