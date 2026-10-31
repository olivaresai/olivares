// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"testing"
)

func TestWorkspaceDataClassRoundTrip(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "data-class")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": t.TempDir(), "data_class": "confidential", "backup": false,
	}, tenantHdr(tenant))
	if created.body["backup"] != false {
		t.Fatalf("backup opt-out lost: %s", created.raw)
	}
	if created.code != http.StatusCreated || created.body["data_class"] != "confidential" {
		t.Fatalf("create=%d %s", created.code, created.raw)
	}
	path := "/v1/m/sessions/workspaces/" + created.body["workspace_ref"].(string)
	for _, class := range []string{"public", "internal", "regulated", "scratch", "tre", ""} {
		patched := h.doJSON("PATCH", path, admin, map[string]any{"data_class": class}, tenantHdr(tenant))
		if patched.code != http.StatusOK || patched.body["data_class"] != class {
			t.Fatalf("patch=%d %s", patched.code, patched.raw)
		}
		got := h.do("GET", path, admin, tenantHdr(tenant))
		if got.code != http.StatusOK || got.body["data_class"] != class {
			t.Fatalf("get=%d %s", got.code, got.raw)
		}
	}
	r := h.doJSON("PATCH", path, admin, map[string]any{"backup": true, "data_class": "internal"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["backup"] != true {
		t.Fatalf("backup opt-in=%d %s", r.code, r.raw)
	}
	r = h.doJSON("PATCH", path, admin, map[string]any{"backup": false, "data_class": "unknown"}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("unknown class=%d %s", r.code, r.raw)
	}
	got := h.do("GET", path, admin, tenantHdr(tenant))
	if got.body["backup"] != true || got.body["data_class"] != "internal" {
		t.Fatalf("refused patch changed metadata: %s", got.raw)
	}

}
