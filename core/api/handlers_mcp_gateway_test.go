// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// Admission runs before any gateway service, config read, secret resolution or network call.
func TestMCPGatewayConsoleAdmission(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "mcp-admission")
	viewer := h.mkMember(admin, "viewer@mcp.test", "viewerpass1", auth.RoleViewer, tenant)
	operator := h.mkMember(admin, "admin@mcp.test", "adminpass1", auth.RoleAdmin, tenant)
	// Members are added before the passkey step-up is required: adding a person asks
	// for the same step-up (HU-28).
	h.requirePasskeyStepUp()
	base := "/v1/console/mcp-gateway"
	for _, route := range []struct{ method, path string }{
		{"GET", base}, {"POST", base + "/servers"},
		{"PUT", base + "/servers/" + tenant.String()},
		{"DELETE", base + "/servers/" + tenant.String()},
		{"POST", base + "/servers/" + tenant.String() + "/test"},
		{"PUT", base + "/session-tools"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			if got := h.do(route.method, route.path, "", map[string]any{}, tenantHdr(tenant)); got.code != http.StatusUnauthorized {
				t.Fatalf("anonymous = %d %s", got.code, got.raw)
			}
			if got := h.do(route.method, route.path, viewer, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusForbidden {
				t.Fatalf("viewer = %d %s", got.code, got.raw)
			}
			if route.method != "GET" {
				if got := h.do(route.method, route.path, operator, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusForbidden || got.body["error"].(map[string]any)["code"] != "step_up_required" {
					t.Fatalf("admin without AAL3 = %d %s", got.code, got.raw)
				}
			}
		})
	}
}

type gatewayServiceFixture struct {
	*auth.MCPGatewayStore
	effects int
}

func (g *gatewayServiceFixture) Get(ctx context.Context, tenant model.TenantID) (auth.MCPGatewaySnapshot, error) {
	g.effects++
	return g.MCPGatewayStore.Get(ctx, tenant)
}
func (g *gatewayServiceFixture) PutServer(ctx context.Context, p auth.Principal, tenant model.TenantID, v int64, id string, in auth.MCPGatewayServerInput) (auth.MCPGatewaySnapshot, error) {
	g.effects++
	return g.MCPGatewayStore.PutServer(ctx, p, tenant, v, id, in)
}
func (g *gatewayServiceFixture) TestServer(ctx context.Context, p auth.Principal, tenant model.TenantID, v int64, id string) (auth.MCPGatewaySnapshot, error) {
	g.effects++
	return g.MCPGatewayStore.SaveProbe(ctx, p, tenant, v, id, auth.MCPGatewayProbe{State: "refused", TestedAt: "2026-09-30T00:00:00Z", Tools: []auth.MCPGatewayTool{}})
}
func (g *gatewayServiceFixture) DeleteServer(ctx context.Context, p auth.Principal, tenant model.TenantID, v int64, id string) (auth.MCPGatewaySnapshot, error) {
	g.effects++
	return g.MCPGatewayStore.DeleteServer(ctx, p, tenant, v, id)
}
func (g *gatewayServiceFixture) SetSessionTools(ctx context.Context, p auth.Principal, tenant model.TenantID, v int64, enabled bool) (auth.MCPGatewaySnapshot, error) {
	g.effects++
	return g.MCPGatewayStore.SetSessionTools(ctx, p, tenant, v, enabled)
}

func TestMCPGatewayConsoleLifecycleAndConfinement(t *testing.T) {
	var gateway *gatewayServiceFixture
	h := newHarnessOpts(t, func(o *api.Options) {
		gateway = &gatewayServiceFixture{MCPGatewayStore: auth.NewMCPGatewayStore(o.Store)}
		o.MCPGateway = gateway
	})
	root := h.adminLogin()
	tenant := h.createOrg(root, "mcp-api")
	other := h.createOrg(root, "mcp-api-other")
	base := "/v1/console/mcp-gateway"
	// New tenants default to session tools on. An explicit off choice in the
	// other tenant makes the isolation assertion independent of that default.
	initial := h.do("GET", base, root, nil, tenantHdr(other))
	if initial.code != http.StatusOK || initial.body["session_tools"] != true {
		t.Fatal("fresh tenant did not receive the default-on session tools")
	}
	disabled := h.do("PUT", base+"/session-tools", root, map[string]any{"version": initial.body["version"], "enabled": false}, tenantHdr(other))
	if disabled.code != http.StatusOK || disabled.body["session_tools"] != false {
		t.Fatal("other tenant explicit off choice was not saved")
	}
	admin := h.mkMember(root, "mcp-admin@api.test", "fixturepass1", auth.RoleAdmin, tenant)
	viewer := h.mkMember(root, "mcp-viewer@api.test", "fixturepass1", auth.RoleViewer, tenant)
	// The admin's refusal below is the passkey step-up's; the default policy asks for
	// none. Members are added first: adding a person asks for the same step-up (HU-28).
	h.requirePasskeyStepUp()
	input := map[string]any{"version": 0, "server": map[string]any{"name": "Fixture", "transport": "streamable_http", "url": "https://tools.test/mcp", "trust": map[string]any{}, "enabled": false}}
	before := gateway.effects
	for _, tok := range []string{"", viewer, admin} {
		got := h.do("POST", base+"/servers", tok, input, tenantHdr(tenant))
		if got.code != 401 && got.code != 403 {
			t.Fatalf("deny=%d", got.code)
		}
	}
	if gateway.effects != before {
		t.Fatal("denial reached service")
	}
	h.elevate(admin)
	if got := h.do("POST", base+"/servers", admin, input, tenantHdr(other)); got.code != 403 || gateway.effects != before {
		t.Fatal("foreign tenant reached service")
	}
	for _, field := range []string{"tenant", "actor", "probe", "upstream_auth"} {
		body := map[string]any{"version": 0, "server": input["server"], field: "forged"}
		got := h.do("POST", base+"/servers", admin, body, tenantHdr(tenant))
		if got.code != 400 || gateway.effects != before {
			t.Fatalf("unknown %s accepted", field)
		}
	}
	workspaces := h.do("GET", "/v1/workspaces", root, nil, tenantHdr(tenant))
	workspace := workspaces.body["items"].([]any)[0].(map[string]any)["id"]
	h.elevate(root) // adding a person under the passkey step-up asks for it (HU-28)
	created := h.do("POST", "/v1/users", root, map[string]any{"email": "mcp-confined@api.test", "password": "fixturepass1", "tenant": tenant.String(), "role": "admin", "workspace_id": workspace}, nil)
	if created.code != 201 {
		t.Fatalf("confined fixture=%d %s", created.code, created.raw)
	}
	confined := h.login("mcp-confined@api.test", "fixturepass1")
	h.elevate(confined)
	if got := h.do("GET", base, confined, nil, tenantHdr(tenant)); got.code != 403 || gateway.effects != before {
		t.Fatal("confined admin inventoried tenant gateway")
	}
	got := h.do("POST", base+"/servers", admin, input, tenantHdr(tenant))
	if got.code != 201 {
		t.Fatalf("create=%d %s", got.code, got.raw)
	}
	id := got.body["servers"].([]any)[0].(map[string]any)["id"].(string)
	version := got.body["version"]
	got = h.do("POST", base+"/servers/"+id+"/test", admin, map[string]any{"version": version}, tenantHdr(tenant))
	if got.code != 200 {
		t.Fatalf("test=%d %s", got.code, got.raw)
	}
	stale := h.do("DELETE", base+"/servers/"+id, admin, map[string]any{"version": version}, tenantHdr(tenant))
	if stale.code != 409 {
		t.Fatal("stale writer removed server")
	}
	got = h.do("PUT", base+"/session-tools", admin, map[string]any{"version": got.body["version"], "enabled": false}, tenantHdr(tenant))
	if got.code != http.StatusOK || got.body["session_tools"] != false {
		t.Fatalf("off switch=%d %s", got.code, got.raw)
	}
	got = h.do("PUT", base+"/session-tools", admin, map[string]any{"version": got.body["version"], "enabled": true}, tenantHdr(tenant))
	if got.code != 200 || got.body["session_tools"] != true {
		t.Fatalf("switch=%d %s", got.code, got.raw)
	}
	got = h.do("DELETE", base+"/servers/"+id, admin, map[string]any{"version": got.body["version"]}, tenantHdr(tenant))
	if got.code != 200 || len(got.body["servers"].([]any)) != 0 {
		t.Fatal("remove")
	}
	got = h.do("GET", base, root, nil, tenantHdr(other))
	if got.code != 200 || len(got.body["servers"].([]any)) != 0 || got.body["session_tools"] != false {
		t.Fatal("configuration crossed tenants")
	}
}

type gatewayRuntimeFixture struct{ calls int }

func TestMCPGatewayWritesRequireExplicitVersionBeforeEffects(t *testing.T) {
	var gateway *gatewayServiceFixture
	h := newHarnessOpts(t, func(o *api.Options) {
		gateway = &gatewayServiceFixture{MCPGatewayStore: auth.NewMCPGatewayStore(o.Store)}
		o.MCPGateway = gateway
	})
	root := h.adminLogin()
	tenant := h.createOrg(root, "mcp-version-envelope")
	admin := h.mkMember(root, "mcp-version@api.test", "fixturepass1", auth.RoleAdmin, tenant)
	h.elevate(admin)
	base := "/v1/console/mcp-gateway"
	server := map[string]any{"name": "Fixture", "transport": "streamable_http", "url": "https://tools.test/mcp", "trust": map[string]any{}, "enabled": false}
	for _, route := range []struct{ method, path string }{
		{"POST", base + "/servers"}, {"PUT", base + "/servers/" + tenant.String()},
		{"DELETE", base + "/servers/" + tenant.String()},
		{"POST", base + "/servers/" + tenant.String() + "/test"},
		{"PUT", base + "/session-tools"},
	} {
		for _, kind := range []string{"missing", "null", "negative"} {
			t.Run(route.method+route.path+kind, func(t *testing.T) {
				body := map[string]any{}
				if route.method == "PUT" && route.path == base+"/session-tools" {
					body["enabled"] = true
				} else if route.method == "PUT" || route.path == base+"/servers" {
					body["server"] = server
				}
				if kind == "null" {
					body["version"] = nil
				}
				if kind == "negative" {
					body["version"] = -1
				}
				before := gateway.effects
				got := h.do(route.method, route.path, admin, body, tenantHdr(tenant))
				if got.code != 400 || gateway.effects != before {
					t.Errorf("%s version=%s returned %d; reached service=%v", route.method, kind, got.code, gateway.effects != before)
				}
			})
		}
	}
}

func (g *gatewayRuntimeFixture) ServeGatewayHTTP(w http.ResponseWriter, r *http.Request) {
	g.calls++
	w.WriteHeader(418)
}
func (g *gatewayRuntimeFixture) ServeSessionHTTP(w http.ResponseWriter, r *http.Request) {
	g.calls++
	w.WriteHeader(418)
}
func TestMCPProtocolAuthDelegationCannotExemptManagement(t *testing.T) {
	runtime := &gatewayRuntimeFixture{}
	h := newHarnessOpts(t, func(o *api.Options) { o.MCPGatewayRuntime = runtime })
	root := h.adminLogin()
	tenant := h.createOrg(root, "mcp-protocol")
	id := model.NewID().String()
	path := "/mcp/gateway/" + tenant.String() + "/" + id
	for _, allowed := range []string{path, "/session/mcp", "/.well-known/oauth-protected-resource" + path} {
		got := h.do("POST", allowed, "external-protocol-bearer", nil, nil)
		if got.code != 418 {
			t.Fatalf("delegation: %s = %d", allowed, got.code)
		}
	}
	before := runtime.calls
	for _, refused := range []string{"/v1/console/mcp-gateway", "/v1/console/mcp-gateway/servers", path + "/extra", "/session/mcp/extra", "/mcp/gateway/invalid/" + id} {
		got := h.do("GET", refused, "external-protocol-bearer", nil, nil)
		if got.code != 401 || runtime.calls != before {
			t.Fatalf("exception escaped leaf: %s = %d", refused, got.code)
		}
	}
}
