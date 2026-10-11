// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"net/http"
	"testing"
)

func TestCatalogApplyPinsTheApprovedSource(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "catalog")
	spec := map[string]any{"image": "catalog-agent:1"}
	id := h.createDef(admin, tenant, "catalog-agent", spec)
	entry := "catalog entry approved-entry"
	if r := h.do("PUT", "/v1/m/deploy/definitions/"+id, admin, map[string]any{"source_ref": entry, "spec": spec}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("source = %d %s", r.code, r.raw)
	}
	path := "/v1/m/deploy/definitions/" + id + "/apply"
	for _, tc := range []struct {
		name, source, subject string
		spec                  map[string]any
	}{
		{"wrong source", "catalog entry another-entry", "agent", spec},
		{"wrong subject", entry, "mcp_server", spec},
		{"wrong spec", entry, "agent", map[string]any{"image": "another-agent:1"}},
		{"invalid spec", entry, "agent", map[string]any{"unknown": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do("POST", path, admin, map[string]any{"catalog_source": map[string]any{"source_ref": tc.source, "subject_kind": tc.subject, "spec": tc.spec}}, tenantHdr(tenant))
			if r.code != http.StatusConflict || h.gate.requests != 0 || h.exec.applyCalls != 0 {
				t.Fatalf("unmatched catalog source reached governance/executor: %d %s", r.code, r.raw)
			}
		})
	}
	r := h.do("POST", path, admin, map[string]any{"catalog_source": map[string]any{"source_ref": entry, "subject_kind": "agent", "spec": spec}}, tenantHdr(tenant))
	if r.code != http.StatusAccepted {
		t.Fatalf("matched catalog request = %d %s", r.code, r.raw)
	}
	ref := r.body["approval_ref"].(string)
	h.gate.set(ref, StatusApproved)
	r = h.do("POST", path, admin, map[string]any{"approval_ref": ref, "catalog_source": map[string]any{"source_ref": entry, "subject_kind": "agent", "spec": spec}}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["status"] != opStatusApplied || h.exec.applyCalls != 1 {
		t.Fatalf("matched catalog apply = %d %s, calls=%d", r.code, r.raw, h.exec.applyCalls)
	}
}
