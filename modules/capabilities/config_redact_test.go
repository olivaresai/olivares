// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A config, revision or server read never returns URL userinfo or query values;
// a write that sends the redacted endpoint back keeps the stored one.
func TestConfigAndServerReadsRedactEndpointValues(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	const endpoint = "https://mcp.example.test/sse?region=query-value-never-read-back"
	const redacted = "https://mcp.example.test/sse?region=<redacted>"
	c := validConfig()
	c["transport"] = "sse"
	c["endpoint"] = endpoint
	r := h.do("POST", "/v1/m/capabilities/configs", editor, c, tenantHdr(tenant))
	if r.code != http.StatusCreated || r.body["endpoint"] != redacted {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	for _, path := range []string{"/v1/m/capabilities/configs/" + id, "/v1/m/capabilities/configs", "/v1/m/capabilities/configs/" + id + "/revisions"} {
		if r := h.do("GET", path, editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || strings.Contains(r.raw, "query-value-never-read-back") || !strings.Contains(r.raw, "redacted") {
			t.Fatalf("GET %s = %d %s", path, r.code, r.raw)
		}
	}
	c["endpoint"] = redacted
	c["scope"] = "team-b"
	if r := h.do("PUT", "/v1/m/capabilities/configs/"+id, editor, c, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("write back = %d %s", r.code, r.raw)
	}
	var stored string
	_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("capabilities.mcp_config")
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(id))
		stored = rec.String("endpoint_ref")
		return err
	})
	if stored != endpoint {
		t.Fatalf("stored endpoint = %q, want the kept %q", stored, endpoint)
	}
	c["endpoint"] = "https://other.example.test/sse?region=<redacted>"
	if r := h.do("PUT", "/v1/m/capabilities/configs/"+id, editor, c, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("marker with nothing stored to keep = %d %s", r.code, r.raw)
	}

	// A discovered server's endpoint is redacted the same way.
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.MCPServers().Create(context.Background(), model.MCPServer{Name: "remote", Transport: "http", Endpoint: "https://alice:userinfo-never-read-back@mcp.example.test/mcp?sig=query-never-read-back", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r = h.do("GET", "/v1/m/capabilities/servers", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || strings.Contains(r.raw, "never-read-back") || len(items(r)) != 1 {
		t.Fatalf("servers = %d %s", r.code, r.raw)
	}
	sid := items(r)[0].(map[string]any)["id"].(string)
	if r := h.do("GET", "/v1/m/capabilities/servers/"+sid, editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || strings.Contains(r.raw, "never-read-back") {
		t.Fatalf("server = %d %s", r.code, r.raw)
	}
}

// An endpoint query value under a credential-looking name is refused, and the
// refusal points to a secret reference.
func TestConfigRefusesCredentialQueryNames(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	for _, name := range []string{"key", "Sig", "SIGNATURE", "auth", "bearer", "pass", "pwd", "Credential", "X-Amz-Signature"} {
		c := validConfig()
		c["server_ref"] = "server-" + strings.ToLower(name)
		c["transport"] = "http"
		c["endpoint"] = "https://mcp.example.test/mcp?" + name + "=private-value"
		r := h.do("POST", "/v1/m/capabilities/configs", editor, c, tenantHdr(tenant))
		if r.code != http.StatusBadRequest || strings.Contains(r.raw, "private-value") || !strings.Contains(r.raw, "secret reference") {
			t.Errorf("%s: code = %d, want 400 naming a secret reference (%s)", name, r.code, r.raw)
		}
	}
}
