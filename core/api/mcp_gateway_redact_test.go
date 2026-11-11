// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// A gateway read never returns a saved environment value. The value comes back
// as the redaction marker, a write that sends the marker back keeps the stored
// value, and the read keeps its shape: env stays an object of strings.
func TestMCPGatewayReadRedactsEnvValuesAndKeepsThemOnWrite(t *testing.T) {
	var gateway *gatewayServiceFixture
	h := newHarnessOpts(t, func(o *api.Options) {
		gateway = &gatewayServiceFixture{MCPGatewayStore: auth.NewMCPGatewayStore(o.Store)}
		o.MCPGateway = gateway
	})
	root := h.adminLogin()
	tenant := h.createOrg(root, "mcp-redact")
	base := "/v1/console/mcp-gateway"
	const value = "env-value-never-read-back"
	server := map[string]any{"name": "Local", "command": "npx", "args": []string{"-y", "server"}, "env": map[string]string{"MODE": value}, "trust": map[string]any{}, "enabled": false}
	created := h.do("POST", base+"/servers", root, map[string]any{"version": 0, "server": server}, tenantHdr(tenant))
	if created.code != http.StatusCreated || strings.Contains(created.raw, value) {
		t.Fatalf("create=%d returned the env value: %s", created.code, created.raw)
	}
	got := h.do("GET", base, root, nil, tenantHdr(tenant))
	if got.code != http.StatusOK || strings.Contains(got.raw, value) {
		t.Fatalf("read=%d returned the env value: %s", got.code, got.raw)
	}
	row := got.body["servers"].([]any)[0].(map[string]any)
	env, ok := row["env"].(map[string]any)
	if !ok || env["MODE"] != "<redacted>" {
		t.Fatalf("env is not an object holding the marker: %v", row["env"])
	}
	// Send the read back unchanged except for a new public argument.
	back := map[string]any{"name": row["name"], "command": row["command"], "args": []string{"-y", "server", "--verbose"}, "env": env, "trust": map[string]any{}, "enabled": false}
	put := h.do("PUT", base+"/servers/"+row["id"].(string), root, map[string]any{"version": got.body["version"], "server": back}, tenantHdr(tenant))
	if put.code != http.StatusOK || strings.Contains(put.raw, value) {
		t.Fatalf("write back=%d %s", put.code, put.raw)
	}
	stored, err := gateway.MCPGatewayStore.Get(t.Context(), tenant)
	if err != nil || stored.Servers[0].Env["MODE"] != value || len(stored.Servers[0].Args) != 3 {
		t.Fatalf("the marker did not keep the stored value: %+v %v", stored.Servers, err)
	}
}
