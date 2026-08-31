// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestWsConnectorCRUD exercises the workspace connector write API round-trip:
// create, get, list, update, delete.
func TestWsConnectorCRUD(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")

	// Create workspace connector.
	r := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, map[string]any{
		"name": "github-eng", "kind": "vault", "workspace_ref": "engineering",
		"config":  map[string]string{"addr": "https://vault.eng:8200"},
		"enabled": true,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %s", r.raw)
	}
	if r.body["name"] != "github-eng" || r.body["kind"] != "vault" || r.body["workspace_ref"] != "engineering" {
		t.Fatalf("field mismatch: %s", r.raw)
	}

	// Get.
	g := h.do("GET", "/v1/m/sourcescope/workspace-connectors/"+id, admin, nil, tenantHdr(tenant))
	if g.code != http.StatusOK || g.body["name"] != "github-eng" {
		t.Fatalf("get = %d %s", g.code, g.raw)
	}

	// List all.
	l := h.do("GET", "/v1/m/sourcescope/workspace-connectors", admin, nil, tenantHdr(tenant))
	if l.code != http.StatusOK || len(items(l)) != 1 {
		t.Fatalf("list = %d, want 1 item: %s", l.code, l.raw)
	}

	// List filtered by workspace_ref.
	lw := h.do("GET", "/v1/m/sourcescope/workspace-connectors?workspace_ref=engineering", admin, nil, tenantHdr(tenant))
	if lw.code != http.StatusOK || len(items(lw)) != 1 {
		t.Fatalf("list by ws = %d, want 1: %s", lw.code, lw.raw)
	}

	// List filtered by kind.
	lk := h.do("GET", "/v1/m/sourcescope/workspace-connectors?kind=vault", admin, nil, tenantHdr(tenant))
	if lk.code != http.StatusOK || len(items(lk)) != 1 {
		t.Fatalf("list by kind = %d, want 1: %s", lk.code, lk.raw)
	}

	// Update: change note, keep immutable fields.
	u := h.do("PUT", "/v1/m/sourcescope/workspace-connectors/"+id, admin, map[string]any{
		"config":  map[string]string{"addr": "https://vault2.eng:8200"},
		"enabled": false, "note": "updated",
	}, tenantHdr(tenant))
	if u.code != http.StatusOK {
		t.Fatalf("update = %d %s", u.code, u.raw)
	}
	if u.body["note"] != "updated" || u.body["name"] != "github-eng" {
		t.Fatalf("update fields mismatch: %s", u.raw)
	}

	// Delete.
	d := h.do("DELETE", "/v1/m/sourcescope/workspace-connectors/"+id, admin, nil, tenantHdr(tenant))
	if d.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", d.code, d.raw)
	}

	// Verify deleted.
	gd := h.do("GET", "/v1/m/sourcescope/workspace-connectors/"+id, admin, nil, tenantHdr(tenant))
	if gd.code != http.StatusNotFound {
		t.Fatalf("after delete = %d, want 404", gd.code)
	}
}

// TestWsConnectorValidation rejects malformed workspace connectors.
func TestWsConnectorValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing name", map[string]any{"name": "", "kind": "vault", "workspace_ref": "payments"}, 400},
		{"missing kind", map[string]any{"name": "x", "kind": "", "workspace_ref": "payments"}, 400},
		{"missing workspace", map[string]any{"name": "x", "kind": "vault", "workspace_ref": ""}, 400},
		{"unknown workspace", map[string]any{"name": "x", "kind": "vault", "workspace_ref": "ghost"}, 400},
		{"negative poll", map[string]any{"name": "x", "kind": "vault", "workspace_ref": "payments", "poll_seconds": -1}, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, c.body, tenantHdr(tenant))
			if r.code != c.want {
				t.Errorf("got %d, want %d: %s", r.code, c.want, r.raw)
			}
		})
	}
}

// TestWsConnectorUniquePerWorkspace verifies the unique constraint on (workspace, name).
func TestWsConnectorUniquePerWorkspace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")

	body := map[string]any{"name": "github", "kind": "vault", "workspace_ref": "engineering", "enabled": true}
	r := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("first create = %d %s", r.code, r.raw)
	}

	// Duplicate in same workspace: conflict.
	r2 := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, body, tenantHdr(tenant))
	if r2.code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409: %s", r2.code, r2.raw)
	}

	// Same name in different workspace: allowed.
	body2 := map[string]any{"name": "github", "kind": "vault", "workspace_ref": "marketing", "enabled": true}
	r3 := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, body2, tenantHdr(tenant))
	if r3.code != http.StatusCreated {
		t.Fatalf("same name different ws = %d, want 201: %s", r3.code, r3.raw)
	}
}

// TestWsConnectorSecretRedaction verifies that secret references are redacted in responses.
func TestWsConnectorSecretRedaction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")

	r := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, map[string]any{
		"name": "pg", "kind": "pgaudit", "workspace_ref": "engineering", "enabled": true,
		"secrets": map[string]string{"dsn": "vault:secret/pg#dsn"},
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	g := h.do("GET", "/v1/m/sourcescope/workspace-connectors/"+id, admin, nil, tenantHdr(tenant))
	if g.code != http.StatusOK {
		t.Fatalf("get = %d %s", g.code, g.raw)
	}
	secrets, ok := g.body["secrets"].(map[string]any)
	if !ok {
		t.Fatalf("secrets missing from response: %s", g.raw)
	}
	if secrets["dsn"] != "***" {
		t.Fatalf("secret not redacted, got %q, want ***", secrets["dsn"])
	}
}

// TestWsConnectorRejectsPlaintextSecretSmuggledThroughConfig attacks the split
// config/secrets DTO: a client must not bypass sealing by placing a credential
// value under a credential-bearing key in the nominally non-secret config map.
func TestWsConnectorRejectsPlaintextSecretSmuggledThroughConfig(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")
	const plaintext = "AUDIT-WORKSPACE-PLAINTEXT-API-KEY"

	r := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, map[string]any{
		"name": "smuggled", "kind": "vault", "workspace_ref": "engineering", "enabled": true,
		"config": map[string]string{"addr": "https://vault.eng:8200", "api_key": plaintext},
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("plaintext secret smuggled through config = %d %s, want 400", r.code, r.raw)
	}
	if strings.Contains(r.raw, plaintext) {
		t.Fatalf("credential rejection echoed the plaintext value: %s", r.raw)
	}

	list := h.do("GET", "/v1/m/sourcescope/workspace-connectors", admin, nil, tenantHdr(tenant))
	if list.code != http.StatusOK || len(items(list)) != 0 {
		t.Fatalf("rejected plaintext connector reached storage: %d %s", list.code, list.raw)
	}
}

// TestWsConnectorCrossTenantIsolation verifies workspace connectors do not leak
// across tenants.
func TestWsConnectorCrossTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant1 := h.createOrg(admin, "acme")
	tenant2 := h.createOrg(admin, "corp")
	h.createWorkspace(tenant1, "eng")
	h.createWorkspace(tenant2, "eng")

	r := h.do("POST", "/v1/m/sourcescope/workspace-connectors", admin, map[string]any{
		"name": "pg", "kind": "pgaudit", "workspace_ref": "eng", "enabled": true,
	}, tenantHdr(tenant1))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}

	l := h.do("GET", "/v1/m/sourcescope/workspace-connectors", admin, nil, tenantHdr(tenant2))
	if l.code != http.StatusOK || len(items(l)) != 0 {
		t.Fatalf("cross-tenant list = %d, items=%d, want 0: %s", l.code, len(items(l)), l.raw)
	}
}
