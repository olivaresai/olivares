// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// TestInstantiateGoverned proves governed self-service: only an APPROVED entry can
// be instantiated; the request records its provenance; the governance flow
// (approve → active) is admin-tier and the whole flow is self-audited. The
// approval POLICY itself belongs to — this module exposes and records the flow.
func TestInstantiateGoverned(t *testing.T) {
	h := newHarness(t, false)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	editor := h.roleToken(root, tenant, "e@acme.com", auth.RoleEditor)
	adminRole := h.roleToken(root, tenant, "a@acme.com", auth.RoleAdmin)

	// A draft entry cannot be instantiated.
	r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry("github", "1.0.0"), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	draftID := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/catalog/entries/"+draftID+"/instantiate", editor, map[string]any{"name": "gh-prod"}, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("instantiate draft = %d, want 409", r.code)
	}

	// Approve it, then a self-service instantiation request (editor / write tier).
	if r := h.do("POST", "/v1/m/catalog/entries/"+draftID+"/approve", adminRole, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/m/catalog/entries/"+draftID+"/instantiate", editor, map[string]any{"name": "gh-prod", "target_ref": "env:prod"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("instantiate = %d %s", r.code, r.raw)
	}
	instID := r.body["id"].(string)
	if r.body["status"] != "requested" {
		t.Errorf("instance status = %v, want requested", r.body["status"])
	}
	if r.body["entry_version"] != "1.0.0" || r.body["entry_slug"] != "github" {
		t.Errorf("instance provenance = %v", r.body)
	}

	// The governance transition is admin-tier; an editor cannot decide.
	if r := h.do("POST", "/v1/m/catalog/instances/"+instID+"/transition", editor, map[string]any{"status": "approved"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("editor transition = %d, want 403", r.code)
	}
	// Admin approves, then activates.
	if r := h.do("POST", "/v1/m/catalog/instances/"+instID+"/transition", adminRole, map[string]any{"status": "approved"}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("approve instance = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/instances/"+instID+"/transition", adminRole, map[string]any{"status": "active"}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["status"] != "active" {
		t.Fatalf("activate instance = %d %s", r.code, r.raw)
	}
	// An invalid flow (active → approved) is rejected by the state machine.
	if r := h.do("POST", "/v1/m/catalog/instances/"+instID+"/transition", adminRole, map[string]any{"status": "approved"}, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("invalid transition = %d, want 409", r.code)
	}

	// Listing and provenance.
	if r := h.do("GET", "/v1/m/catalog/instances", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || len(items(r)) != 1 {
		t.Fatalf("list instances = %d, %d items", r.code, len(items(r)))
	}

	// The whole governance flow is self-audited to the real principals.
	r = h.do("GET", "/v1/audit?limit=100", root, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("audit = %d %s", r.code, r.raw)
	}
	for _, want := range []string{"catalog.entry.approve", "catalog.instance.instantiate", "catalog.instance.transition"} {
		if !strings.Contains(r.raw, want) {
			t.Errorf("audit ledger missing action %q", want)
		}
	}
}
