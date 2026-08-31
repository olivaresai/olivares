// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"net/http"
	"testing"
)

func TestTargetListGetAndRevocationDenyClosed(t *testing.T) {
	h := newHarness(t, fakeSandbox{})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedAgent(tenant, "agent-consent")

	reg := h.do("POST", "/v1/m/redteam/targets", admin, map[string]any{
		"agent_ref": "agent-consent",
		"name":      "Consent target",
		"endpoint":  "sandbox://agent-consent",
		"scope":     "safe prompts only",
	}, tenantHdr(tenant))
	if reg.code != http.StatusCreated {
		t.Fatalf("register target = %d %s", reg.code, reg.raw)
	}
	id := reg.body["id"].(string)
	if reg.body["authorized"] != false || reg.body["status"] != "registered" {
		t.Fatalf("registered target = %+v, want unauthorized registered", reg.body)
	}

	list := h.do("GET", "/v1/m/redteam/targets?status=registered", admin, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("list targets = %d %s", list.code, list.raw)
	}
	items := list.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatalf("registered list = %+v, want target %s", items, id)
	}

	get := h.do("GET", "/v1/m/redteam/targets/"+id, admin, nil, tenantHdr(tenant))
	if get.code != http.StatusOK || get.body["agent_ref"] != "agent-consent" {
		t.Fatalf("get target = %d %+v", get.code, get.body)
	}

	auth := h.do("POST", "/v1/m/redteam/targets/"+id+"/authorize", admin, map[string]any{
		"authorized": true,
		"scope":      "approved scope",
	}, tenantHdr(tenant))
	if auth.code != http.StatusOK || auth.body["status"] != "authorized" {
		t.Fatalf("authorize = %d %+v", auth.code, auth.body)
	}

	revoked := h.do("POST", "/v1/m/redteam/targets/"+id+"/authorize", admin, map[string]any{
		"authorized": false,
	}, tenantHdr(tenant))
	if revoked.code != http.StatusOK || revoked.body["status"] != "revoked" {
		t.Fatalf("revoke = %d %+v", revoked.code, revoked.body)
	}
	run := h.do("POST", "/v1/m/redteam/runs", admin, map[string]any{"target_ref": id}, tenantHdr(tenant))
	if run.code != http.StatusForbidden {
		t.Fatalf("run after revocation = %d, want 403 (%s)", run.code, run.raw)
	}
}
