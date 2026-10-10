// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/json"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
)

// An inheritance filter is stored and authored here; the authorizer does not read it
// yet. These tests pin the authoring contract: the row shape, the validation, the
// "only an admin of the node may set it" ceiling, and the audit trail.

func filterBody(tree, ref, class string) map[string]any {
	return map[string]any{"scope_tree": tree, "scope_ref": ref, "scope_class": class}
}

type filterItem struct {
	ID         string `json:"id"`
	ScopeTree  string `json:"scope_tree"`
	ScopeRef   string `json:"scope_ref"`
	ScopeClass string `json:"scope_class"`
	CreatedBy  string `json:"created_by"`
}

func (h *harness) listFilters(token string, tenant model.TenantID) []filterItem {
	h.t.Helper()
	r := h.rbac("GET", "inheritance-filters", token, tenant, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("list filters = %d %s", r.code, r.raw)
	}
	var page struct {
		Items []filterItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.raw), &page); err != nil {
		h.t.Fatalf("decode filter list: %v", err)
	}
	return page.Items
}

// An admin of one resource class does not administer another class on the same node: the
// ceiling reads the domain's permissions, not only its scope.

// id and created_by are output fields: a caller cannot forge the audit evidence, and a
// field the contract does not name is refused.
