// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestGuardianRuleDeleteHTTPContract(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "guardian-delete")
	_, viewer := h.roleUser(admin, tenant, "guardian-viewer@x.io", "viewer")
	_, editor := h.roleUser(admin, tenant, "guardian-editor@x.io", "editor")

	created := h.do(http.MethodPost, govPath+"/guardian/rules", admin, map[string]any{
		"name":         "delete-me",
		"match_kinds":  "anomaly_detected",
		"min_severity": "high",
		"action":       "stop_agent",
		"mode":         "approval",
	}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create guardian rule = %d %s", created.code, created.raw)
	}
	id, _ := created.body["id"].(string)
	if id == "" {
		t.Fatalf("created guardian rule has no id: %s", created.raw)
	}

	for role, token := range map[string]string{"read": viewer, "write": editor} {
		if got := h.do(http.MethodDelete, govPath+"/guardian/rules/"+id, token, nil, tenantHdr(tenant)); got.code != http.StatusForbidden {
			t.Fatalf("%s-tier delete = %d %s, want 403", role, got.code, got.raw)
		}
	}

	deleted := h.do(http.MethodDelete, govPath+"/guardian/rules/"+id, admin, nil, tenantHdr(tenant))
	if deleted.code != http.StatusNoContent || deleted.raw != "" {
		t.Fatalf("admin delete = %d %q, want empty 204", deleted.code, deleted.raw)
	}
	listed := h.do(http.MethodGet, govPath+"/guardian/rules", admin, nil, tenantHdr(tenant))
	if listed.code != http.StatusOK || len(items(listed)) != 0 {
		t.Fatalf("rules after delete = %d %s", listed.code, listed.raw)
	}
	if !contains(h.auditActions(tenant), "governance.guardian.rule.delete") {
		t.Fatalf("missing guardian rule delete audit: %v", h.auditActions(tenant))
	}

	unknown := h.do(http.MethodDelete, govPath+"/guardian/rules/"+model.NewID().String(), admin, nil, tenantHdr(tenant))
	if unknown.code != http.StatusNotFound {
		t.Fatalf("unknown rule delete = %d %s, want 404", unknown.code, unknown.raw)
	}
}
