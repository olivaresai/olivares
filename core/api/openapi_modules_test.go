// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// betaTestModule exercises the beta builder: a collection, a write, a by-id route
// (path param), and an SSE stream (raw 200). The handlers are nil — the document
// is built from the REGISTRATION (method/pattern/perm), never by invoking them.
type betaTestModule struct{}

func (betaTestModule) APINamespace() string { return "demoapi" }
func (betaTestModule) Permissions() []auth.Permission {
	return []auth.Permission{"demoapi:thing:read", "demoapi:thing:write"}
}

func (betaTestModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/things", "demoapi:thing:read", nil)
	reg.Handle("POST", "/things", "demoapi:thing:write", nil)
	reg.Handle("GET", "/things/{id}", "demoapi:thing:read", nil)
	reg.Handle("GET", "/runs/{id}/stream", "demoapi:thing:read", nil)
}

// TestStableOpenAPIHasNoModuleRoutes guards the stable contract: the published
// /openapi.json is exactly the core paths, none of them /v1/m/…, and every
// operation is the stable tier. Must not leak a module route into it.
// stableContractPaths is the CORE CONTRACT, pinned BY NAME. It used to be pinned by a COUNT
// (`len(paths) != 53`), and a count cannot see a SUBSTITUTION: drop one stable path, add another, and
// the number is still 53 while the published contract has silently changed under a green test. The
// same lesson the ratchets in scripts/ learned — a baseline is a LIST, never a total.
//
// To change this list you have to say WHICH path, in the diff, which is the point.
var stableContractPaths = []string{
	"/healthz",
	"/livez",
	"/metrics",
	"/openapi.json",
	"/openapi.beta.json",
	"/pod-readyz",
	"/readyz",
	"/status",
	"/v1/access-edges",
	"/v1/account/password",
	"/v1/agents",
	"/v1/agents/{id}",
	"/v1/agent-groups",
	"/v1/agent-groups/{id}",
	"/v1/agent-groups/{id}/members",
	"/v1/agent-groups/{id}/members/{agentID}",
	"/v1/audit",
	"/v1/audit/export",
	"/v1/audit/pubkey",
	"/v1/audit/recent",
	"/v1/audit/system",
	"/v1/audit/verify",
	"/v1/auth/totp",
	"/v1/auth/totp/enrol",
	"/v1/auth/totp/activate",
	"/v1/auth/totp/challenge",
	"/v1/auth/totp/status",
	"/v1/auth/totp/policy",
	"/v1/auth/step-up-policy",
	"/v1/auth/browser-session",
	"/v1/auth/login",
	"/v1/auth/logout",
	// OS-account bindings are already published core auth operations with the
	// stable tier; keep their contract instead of moving them to module beta.
	"/v1/auth/os-account-bindings",
	"/v1/auth/os-account-bindings/complete",
	"/v1/auth/os-account-bindings/{id}",
	"/v1/auth/refresh",
	"/v1/auth/whoami",
	"/v1/auth/capabilities",
	"/v1/auth/effective-rights",
	"/v1/auth/webauthn/register/options",
	"/v1/auth/webauthn/register",
	"/v1/auth/webauthn/authenticate/options",
	"/v1/auth/webauthn/authenticate",
	"/v1/auth/webauthn/credentials",
	"/v1/auth/webauthn/credentials/{id}",
	"/v1/auth/piv/status",
	"/v1/auth/piv/elevate",
	"/v1/connectors/health",
	"/v1/console/bus",
	"/v1/console/config/effective",
	"/v1/console/connectors",
	"/v1/console/connectors/test",
	"/v1/console/health-summary",
	"/v1/console/keys",
	"/v1/console/license",
	"/v1/console/mcp-gateway",
	"/v1/console/mcp-gateway/servers",
	"/v1/console/mcp-gateway/servers/{id}",
	"/v1/console/mcp-gateway/servers/{id}/test",
	"/v1/console/mcp-gateway/session-tools",
	"/v1/console/secrets",
	"/v1/console/setup-status",
	"/v1/console/sources",
	"/v1/console/sso",
	"/v1/console/sso/test",
	"/v1/console/support-bundle",
	"/v1/console/update-check",
	"/v1/console/runtime/reload",
	"/v1/console/sources/diff",
	"/v1/console/sso/idps",
	"/v1/console/sso/idps/{alias}",
	"/v1/console/sso/idps/{alias}/test",
	"/v1/console/sso/tenants/{tenant}",
	"/v1/console/sso/tenants/{tenant}/test",
	"/v1/console/sso/tenants/{tenant}/idps",
	"/v1/console/sso/tenants/{tenant}/idps/{alias}",
	"/v1/console/sso/tenants/{tenant}/idps/{alias}/test",
	"/v1/console/dr/backup",
	"/v1/console/dr/backups",
	"/v1/console/dr/backups/{id}",
	"/v1/console/dr/backups/{id}/download",
	"/v1/console/dr/restore/upload",
	"/v1/console/dr/restore/{id}/apply",
	"/v1/console/dr/restore/{id}/approve",
	"/v1/console/dr/restore/pending",
	"/v1/console/dr/jobs",
	"/v1/console/dr/jobs/{id}/stream",
	"/v1/console/dr/schedule",
	"/v1/console/activation",
	"/v1/console/activation/preview",
	"/v1/console/activation/apply",
	"/v1/console/modules",
	"/v1/console/logs/stream",
	"/v1/console/logs/buffer",
	"/v1/members",
	"/v1/groups",
	"/v1/groups/{id}/role",
	"/v1/groups/{id}/parent",
	"/v1/groups/{id}/workspace",
	"/v1/invites",
	"/v1/invites/accept",
	"/v1/invites/{id}",
	"/v1/invites/{id}/resend",
	"/v1/onboard",
	"/v1/memberships",
	"/v1/search",
	"/v1/server-info",
	"/v1/setup",
	"/v1/system/orgs",
	"/v1/system/orgs/{tenant_id}",
	"/v1/system/orgs/{tenant_id}/region",
	"/v1/system/orgs/{tenant_id}/status",
	"/v1/system/residency",
	// Tracing settings are already published as stable core system operations.
	"/v1/system/tracing",
	"/v1/tokens",
	"/v1/tokens/{id}",
	"/v1/tokens/{id}/rotate",
	"/v1/users",
	"/v1/users/superadmins",
	"/v1/users/{id}/disable",
	"/v1/users/{id}/enable",
	"/v1/users/{id}/totp",
	"/v1/users/{id}/totp/reset",
	"/v1/workspaces",
	"/v1/workspaces/{id}",
	"/v1/workspaces/{id}/parent",
	"/v1/workspaces/{id}/summary",
	"/v1/workspaces/{id}/contents",
}

func TestStableOpenAPIHasNoModuleRoutes(t *testing.T) {
	doc := api.OpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	// 49 since stage-2 added /pod-readyz (the leader-agnostic pod-health probe
	// the HA readinessProbe uses). Bumping this number is a CONTRACT change: the
	// count exists so a route cannot slip into the published surface unnoticed.
	// A COUNT, and it is kept ONLY as a tripwire against unintended growth of the
	// stable surface. It is NOT the guard that matters and it never was: a count
	// cannot see three operations declared on the wrong path, which is exactly what
	// had happened — testConnector, testSSOConfig and rotateToken sat on their
	// PARENT path while chi routes them under /test, /test and /{id}/rotate, so
	// generated clients called URLs that 405 with an empty body while this number
	// stayed at 49. The discriminating check is now
	// TestEveryPublishedOperationExistsInTheRouter, which compares SETS against the
	// router. 53 = 49 + those three + the service-withdrawal route, now
	// declared where they actually live.
	// ⛔ COMPARACIÓN POR NOMBRE, NO POR NÚMERO. Antes esto era `len(paths) != 53`, y un total deja
	// pasar la sustitución sin decir nada: una ruta estable fuera y otra dentro suman lo mismo.
	got := make(map[string]bool, len(paths))
	for p := range paths {
		got[p] = true
	}
	want := make(map[string]bool, len(stableContractPaths))
	for _, p := range stableContractPaths {
		want[p] = true
	}
	for _, p := range stableContractPaths {
		if !got[p] {
			t.Errorf("stable OpenAPI: contract path %q DISAPPEARED from the published document", p)
		}
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		if !want[p] {
			t.Errorf("stable OpenAPI: %q is published as stable and is NOT in the pinned contract", p)
		}
	}
	if len(paths) != len(stableContractPaths) {
		t.Errorf("stable OpenAPI = %d paths, pinned contract has %d", len(paths), len(stableContractPaths))
	}
	for p, item := range paths {
		if strings.HasPrefix(p, "/v1/m/") {
			t.Errorf("stable contract leaked a module route: %q", p)
		}
		for method, raw := range item.(map[string]any) {
			switch method {
			case "get", "put", "post", "delete", "options", "head", "patch", "trace":
			default:
				continue // Path-item metadata, including parameters, is not an operation.
			}
			op := raw.(map[string]any)
			if got := op["x-stability"]; got != "stable" {
				t.Errorf("%s %s: stable doc op x-stability = %v, want stable", method, p, got)
			}
		}
	}
}

// TestModuleOpenAPIDocumentShape pins the beta document built from a module's
// registered routes: namespaced paths, path params, the tenant header, bearer
// security, the required permission, the beta tier on every op, the beta banner,
// and the raw classification of an SSE stream.
func TestModuleOpenAPIDocumentShape(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{betaTestModule{}})

	info := doc["info"].(map[string]any)
	if !strings.Contains(strings.ToLower(info["title"].(string)), "beta") {
		t.Errorf("info.title = %v, want a beta title", info["title"])
	}
	if info["x-beta-notice"] == nil || !strings.Contains(info["description"].(string), "BETA") {
		t.Error("beta banner (info.x-beta-notice / description) missing")
	}
	if !strings.Contains(info["x-stability-policy"].(string), "olivares.ai/docs") {
		t.Errorf("info.x-stability-policy = %v", info["x-stability-policy"])
	}

	paths := doc["paths"].(map[string]any)
	// Collection route: namespaced, beta, bearer, tenant header, declared permission.
	coll := mustOp(t, paths, "/v1/m/demoapi/things", "get")
	if coll["x-stability"] != "beta" {
		t.Errorf("module op x-stability = %v, want beta", coll["x-stability"])
	}
	if coll["x-required-permission"] != "demoapi:thing:read" {
		t.Errorf("x-required-permission = %v", coll["x-required-permission"])
	}
	if !hasBearer(coll) {
		t.Error("module op missing bearer security")
	}
	if !hasTenantHeaderParam(coll) {
		t.Error("module op missing X-Olivares-Tenant header parameter")
	}
	// By-id route: the {id} path param becomes an in:path parameter.
	byID := mustOp(t, paths, "/v1/m/demoapi/things/{id}", "get")
	if !hasPathParam(byID, "id") {
		t.Error("/things/{id} missing in:path parameter 'id'")
	}
	// SSE stream: raw 200 (text/event-stream), never a JSON envelope.
	stream := mustOp(t, paths, "/v1/m/demoapi/runs/{id}/stream", "get")
	content := stream["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["text/event-stream"]; !ok {
		t.Errorf("SSE route 200 content = %v, want text/event-stream", content)
	}
	if _, ok := content["application/json"]; ok {
		t.Error("SSE route must NOT declare a JSON 200 body")
	}
}

// TestModuleOpenAPICoversEveryRegisteredRoute is the drift guard at the unit
// level: every route a module registers appears in the beta document. (The guard
// over the real mounted module set — walking the live chi router, plus the
// dep-independence check — lives in cmd/olivares, where the module set is built.)
func TestModuleOpenAPICoversEveryRegisteredRoute(t *testing.T) {
	mods := []api.Module{betaTestModule{}}
	doc := api.ModuleOpenAPIDocument(mods)
	paths := doc["paths"].(map[string]any)
	// (method, spec-path) pairs we registered, with their canonical spec paths.
	want := map[string]string{
		"GET /v1/m/demoapi/things":           "",
		"POST /v1/m/demoapi/things":          "",
		"GET /v1/m/demoapi/things/{id}":      "",
		"GET /v1/m/demoapi/runs/{id}/stream": "",
	}
	for key := range want {
		parts := strings.SplitN(key, " ", 2)
		method, path := strings.ToLower(parts[0]), parts[1]
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("registered route %s missing from beta doc", key)
			continue
		}
		if _, ok := item[method]; !ok {
			t.Errorf("registered route %s missing %s operation", path, method)
		}
	}
}

// TestServedBetaOpenAPIEndpoint proves the engine serves the beta document at
// /openapi.beta.json (auth/setup-exempt) while /openapi.json stays the stable
// contract.
func TestServedBetaOpenAPIEndpoint(t *testing.T) {
	h := newHarness(t, betaTestModule{})

	beta := decodeDoc(t, rawGet(h, "/openapi.beta.json", "", nil))
	bpaths := beta["paths"].(map[string]any)
	if _, ok := bpaths["/v1/m/demoapi/things"]; !ok {
		t.Error("/openapi.beta.json missing the mounted module route")
	}
	for p := range bpaths {
		if !strings.HasPrefix(p, "/v1/m/") {
			t.Errorf("/openapi.beta.json carries a non-module path %q", p)
		}
	}

	stable := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	stablePaths := stable["paths"].(map[string]any)
	want := make(map[string]bool, len(stableContractPaths))
	for _, path := range stableContractPaths {
		want[path] = true
		if _, present := stablePaths[path]; !present {
			t.Errorf("/openapi.json is missing pinned stable path %q", path)
		}
	}
	for path := range stablePaths {
		if !want[path] {
			t.Errorf("/openapi.json carries unpinned stable path %q", path)
		}
	}
	if len(stablePaths) != len(want) {
		t.Errorf("/openapi.json has %d paths, pinned set has %d", len(stablePaths), len(want))
	}
}

// --- helpers ------------------------------------------------------------------

func decodeDoc(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return doc
}

func mustOp(t *testing.T, paths map[string]any, path, method string) map[string]any {
	t.Helper()
	item, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %q missing from document", path)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("%s %s missing", strings.ToUpper(method), path)
	}
	return op
}

func hasBearer(op map[string]any) bool {
	sec, ok := op["security"].([]any)
	if !ok {
		return false
	}
	for _, s := range sec {
		if _, ok := s.(map[string]any)["bearerAuth"]; ok {
			return true
		}
	}
	return false
}

func paramsOf(op map[string]any) []any {
	p, _ := op["parameters"].([]any)
	return p
}

func hasTenantHeaderParam(op map[string]any) bool {
	for _, p := range paramsOf(op) {
		m := p.(map[string]any)
		if m["name"] == "X-Olivares-Tenant" && m["in"] == "header" {
			return true
		}
	}
	return false
}

func hasPathParam(op map[string]any, name string) bool {
	for _, p := range paramsOf(op) {
		m := p.(map[string]any)
		if m["name"] == name && m["in"] == "path" && m["required"] == true {
			return true
		}
	}
	return false
}

// rawSuffixModule registers routes whose last segment looks like a format
// (export) or a stream under another name, next to the two stream suffixes.
type rawSuffixModule struct{}

func (rawSuffixModule) APINamespace() string           { return "rawsuffix" }
func (rawSuffixModule) Permissions() []auth.Permission { return nil }
func (rawSuffixModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/evidence/{id}/export", "", nil)
	reg.Handle("GET", "/work-stream", "", nil)
	reg.Handle("GET", "/runs/{id}/stream", "", nil)
	reg.Handle("GET", "/runs/{id}/attach", "", nil)
}

// TestModuleRawContentTypeFollowsOnlyTheStreamSuffix pins the core's one raw
// rule: a last segment "stream" or "attach" is Server-Sent Events, and nothing
// else is raw unless the owning module documents it. An "export" suffix or a
// stream under another name stays the JSON envelope here.
func TestModuleRawContentTypeFollowsOnlyTheStreamSuffix(t *testing.T) {
	paths := api.ModuleOpenAPIDocument([]api.Module{rawSuffixModule{}})["paths"].(map[string]any)
	for path, want := range map[string]string{
		"/v1/m/rawsuffix/evidence/{id}/export": "application/json",
		"/v1/m/rawsuffix/work-stream":          "application/json",
		"/v1/m/rawsuffix/runs/{id}/stream":     "text/event-stream",
		"/v1/m/rawsuffix/runs/{id}/attach":     "text/event-stream",
	} {
		ok := mustOp(t, paths, path, "get")["responses"].(map[string]any)["200"].(map[string]any)
		content := ok["content"].(map[string]any)
		if _, found := content[want]; !found || len(content) != 1 {
			t.Errorf("GET %s 200 content = %v, want only %s", path, content, want)
		}
	}
}

// undocumentedModule registers, under the namespaces that used to be special in
// the core, the routes that used to get a typed contract there. It documents
// nothing (no ModuleOperationDocumenter), so each route must get only the
// generic envelope: a module's operations are described by the module.
type undocumentedModule struct{ ns string }

func (m undocumentedModule) APINamespace() string         { return m.ns }
func (undocumentedModule) Permissions() []auth.Permission { return nil }
func (m undocumentedModule) APIRoutes(reg api.RouteRegistrar) {
	switch m.ns {
	case "sessions":
		reg.Handle("GET", "/work-stream", "", nil)
		reg.Handle("GET", "/launch-readiness", "", nil)
		reg.Handle("POST", "/provider-accounts", "", nil)
		reg.Handle("PUT", "/workspaces/{ref}/files/raw", "", nil)
	case "finops":
		reg.Handle("GET", "/spend/export", "", nil)
		reg.Handle("GET", "/statements/{id}/export", "", nil)
	case "inventory":
		reg.Handle("GET", "/collections", "", nil)
	}
}

// TestCoreDescribesNoModuleOperation fails if the core grows a branch for a
// module's route again: without a documenter, every route answers the generic
// responses, publishes no request body and carries only the tenant header and
// its path parameters.
func TestCoreDescribesNoModuleOperation(t *testing.T) {
	mods := []api.Module{undocumentedModule{"sessions"}, undocumentedModule{"finops"}, undocumentedModule{"inventory"}}
	paths := api.ModuleOpenAPIDocument(mods)["paths"].(map[string]any)
	for path, item := range paths {
		for method, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			if strings.HasSuffix(path, "/work-stream") {
				continue // the core's one raw rule is covered by TestModuleRawContentTypeFollowsOnlyTheStreamSuffix
			}
			if !reflect.DeepEqual(op["responses"], api.GenericModuleResponses()) {
				t.Errorf("%s %s responses = %v, want the generic module responses", method, path, op["responses"])
			}
			if _, has := op["requestBody"]; has {
				t.Errorf("%s %s publishes a request body the core cannot know", method, path)
			}
			for _, p := range paramsOf(op) {
				name := p.(map[string]any)["name"].(string)
				if name != "X-Olivares-Tenant" && !hasPathParam(op, name) {
					t.Errorf("%s %s has parameter %q the core cannot know", method, path, name)
				}
			}
		}
	}
	if len(paths) != 7 {
		t.Fatalf("document has %d paths, want the 7 registered: %v", len(paths), paths)
	}
}

// extensionModule documents a route with extensions that try to replace what the
// core publishes from the registration.
type extensionModule struct{}

func (extensionModule) APINamespace() string           { return "ext" }
func (extensionModule) Permissions() []auth.Permission { return nil }
func (extensionModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/thing", auth.Permission("ext.read"), nil)
}
func (extensionModule) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	return api.ModuleOperationDocumentation{Extensions: map[string]any{
		"x-required-permission": "ext.admin",
		"security":              []any{},
		"responses":             map[string]any{},
		"x-module-field":        "kept",
	}}, true
}

// TestModuleExtensionsAddOnlyNewXFields pins that a module's documentation adds
// x- fields and never replaces authorization or response documentation the core
// builds from the registration.
func TestModuleExtensionsAddOnlyNewXFields(t *testing.T) {
	paths := api.ModuleOpenAPIDocument([]api.Module{extensionModule{}})["paths"].(map[string]any)
	op := mustOp(t, paths, "/v1/m/ext/thing", "get")
	if got := op["x-required-permission"]; got != "ext.read" {
		t.Errorf("x-required-permission = %v, want the registered ext.read", got)
	}
	if !hasBearer(op) {
		t.Error("security was replaced by a module extension")
	}
	if !reflect.DeepEqual(op["responses"], api.GenericModuleResponses()) {
		t.Errorf("responses = %v, want the generic module responses", op["responses"])
	}
	if got := op["x-module-field"]; got != "kept" {
		t.Errorf("x-module-field = %v, want kept", got)
	}
}
