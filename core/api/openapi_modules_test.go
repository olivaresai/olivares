// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

type sessionsWorkContractModule struct{}

func (sessionsWorkContractModule) APINamespace() string           { return "sessions" }
func (sessionsWorkContractModule) Permissions() []auth.Permission { return nil }
func (sessionsWorkContractModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/work-items", "sessions:work:write", nil)
	reg.Handle("GET", "/work-items", "sessions:work:read", nil)
	reg.Handle("GET", "/leases", "sessions:lease:read", nil)
	reg.Handle("GET", "/work-items/{id}/lease", "sessions:lease:read", nil)
	reg.Handle("POST", "/work-items/{id}/lease/acquire", "sessions:lease:write", nil)
	reg.Handle("POST", "/work-items/{id}/lease/renew", "sessions:lease:write", nil)
	reg.Handle("POST", "/work-items/{id}/lease/release", "sessions:lease:write", nil)
	reg.Handle("POST", "/work-items/{id}/lease/takeover", "sessions:lease:admin", nil)
	reg.Handle("POST", "/work-items/{id}/lease/revoke", "sessions:lease:admin", nil)
	reg.Handle("POST", "/work-items/{id}/lease/clock-rebase", "sessions:lease:admin", nil)
	reg.Handle("POST", "/work-events/{event_id}/replay", "sessions:work:admin", nil)
	reg.Handle("GET", "/decisions", "sessions:decision:read", nil)
	reg.Handle("GET", "/work-stream", "sessions:work:read", nil)
}

type sessionsRuntimeWorkControlModule struct{}

func (sessionsRuntimeWorkControlModule) APINamespace() string           { return "sessions" }
func (sessionsRuntimeWorkControlModule) Permissions() []auth.Permission { return nil }
func (sessionsRuntimeWorkControlModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/runs/{ref}/input", "sessions:run:write", nil)
	reg.Handle("POST", "/runs/{ref}/stop", "sessions:run:write", nil)
}

type sessionsProtocolBindingContractModule struct{}

func (sessionsProtocolBindingContractModule) APINamespace() string { return "sessions" }
func (sessionsProtocolBindingContractModule) Permissions() []auth.Permission {
	return nil
}
func (sessionsProtocolBindingContractModule) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/protocol-binding-specs", "sessions:protocol-binding:read", nil)
	reg.Handle("POST", "/protocol-binding-specs", "sessions:protocol-binding:write", nil)
	reg.Handle("GET", "/protocol-binding-specs/{id}", "sessions:protocol-binding:read", nil)
	reg.Handle("POST", "/protocol-binding-specs/{id}/activate", "sessions:protocol-binding:admin", nil)
	reg.Handle("POST", "/protocol-binding-specs/{id}/disable", "sessions:protocol-binding:admin", nil)
	reg.Handle("GET", "/protocol-bindings", "sessions:protocol-binding:read", nil)
	reg.Handle("GET", "/protocol-bindings/{id}", "sessions:protocol-binding:read", nil)
	reg.Handle("POST", "/protocol-bindings/{id}/reconcile", "sessions:protocol-binding:write", nil)
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
	"/pod-readyz",
	"/readyz",
	"/status",
	"/v1/access-edges",
	"/v1/agents",
	"/v1/agents/{id}",
	"/v1/audit",
	"/v1/audit/export",
	"/v1/audit/pubkey",
	"/v1/audit/system",
	"/v1/audit/verify",
	"/v1/auth/login",
	"/v1/auth/logout",
	"/v1/auth/refresh",
	"/v1/auth/whoami",
	"/v1/connectors/health",
	"/v1/console/bus",
	"/v1/console/config/effective",
	"/v1/console/connectors",
	"/v1/console/connectors/test",
	"/v1/console/health-summary",
	"/v1/console/keys",
	"/v1/console/license",
	"/v1/console/secrets",
	"/v1/console/setup-status",
	"/v1/console/sources",
	"/v1/console/sso",
	"/v1/console/sso/test",
	"/v1/console/support-bundle",
	"/v1/console/update-check",
	"/v1/members",
	"/v1/memberships",
	"/v1/search",
	"/v1/server-info",
	"/v1/setup",
	"/v1/system/orgs",
	"/v1/system/orgs/{tenant_id}",
	"/v1/system/orgs/{tenant_id}/region",
	"/v1/system/orgs/{tenant_id}/status",
	"/v1/system/residency",
	"/v1/tokens",
	"/v1/tokens/{id}",
	"/v1/tokens/{id}/rotate",
	"/v1/users",
	"/v1/users/superadmins",
	"/v1/users/{id}/disable",
	"/v1/users/{id}/enable",
	"/v1/workspaces",
	"/v1/workspaces/{id}",
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
	// RELAJADO a proposito el 2026-08-19, y aqui esta el motivo para poder revertirlo:
	// esta asercion exigia que la URL nombrara la PAGINA (`api-stability`). La URL
	// apuntaba a docs.olivares.ai, que no resuelve, y la pagina solo existe en el
	// docs-site sin desplegar. Publicarla en olivares.ai/docs cuesta 13 locales -- el
	// sitio enlaza cada pagina a todos -- y el canon pide `sol max` para traduccion
	// tecnica. Asi que la URL pasa a la RAIZ de las docs, que si resuelve, y esto
	// comprueba el host canonico en vez del nombre de la pagina. Cuando la pagina se
	// publique, devolved la cadena a "reference/api-stability".
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

func TestSessionsWorkOpenAPIIsInvocableAndClassifiesWorkStream(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{sessionsWorkContractModule{}})
	paths := doc["paths"].(map[string]any)

	create := mustOp(t, paths, "/v1/m/sessions/work-items", "post")
	for _, want := range []struct{ name, in string }{
		{name: "mode", in: "query"}, {name: "Idempotency-Key", in: "header"},
		{name: "If-Match", in: "header"}, {name: "If-Plan-Hash", in: "header"},
	} {
		if !hasParameter(create, want.name, want.in) {
			t.Errorf("work mutation missing %s %s parameter", want.in, want.name)
		}
	}
	if _, ok := create["requestBody"].(map[string]any); !ok {
		t.Error("work mutation has no JSON WorkCommand requestBody")
	}

	items := mustOp(t, paths, "/v1/m/sessions/work-items", "get")
	for _, name := range []string{"limit", "cursor", "status", "archived", "updated_after"} {
		if !hasParameter(items, name, "query") {
			t.Errorf("work item list missing query parameter %s", name)
		}
	}
	decisions := mustOp(t, paths, "/v1/m/sessions/decisions", "get")
	for _, name := range []string{"work_item_id", "effective", "revoked"} {
		if !hasParameter(decisions, name, "query") {
			t.Errorf("decision list missing query parameter %s", name)
		}
	}
	leases := mustOp(t, paths, "/v1/m/sessions/leases", "get")
	for _, name := range []string{"limit", "cursor", "work_item_id", "holder_sid", "state", "expires_before"} {
		if !hasParameter(leases, name, "query") {
			t.Errorf("lease list missing query parameter %s", name)
		}
	}
	if leases["x-required-permission"] != "sessions:lease:read" {
		t.Errorf("lease list permission = %v", leases["x-required-permission"])
	}

	lease := mustOp(t, paths, "/v1/m/sessions/work-items/{id}/lease", "get")
	if !hasPathParam(lease, "id") || lease["x-required-permission"] != "sessions:lease:read" {
		t.Errorf("nested lease get contract = %#v", lease)
	}
	for _, tc := range []struct {
		path, permission string
	}{
		{path: "/v1/m/sessions/work-items/{id}/lease/acquire", permission: "sessions:lease:write"},
		{path: "/v1/m/sessions/work-items/{id}/lease/renew", permission: "sessions:lease:write"},
		{path: "/v1/m/sessions/work-items/{id}/lease/release", permission: "sessions:lease:write"},
		{path: "/v1/m/sessions/work-items/{id}/lease/takeover", permission: "sessions:lease:admin"},
		{path: "/v1/m/sessions/work-items/{id}/lease/revoke", permission: "sessions:lease:admin"},
		{path: "/v1/m/sessions/work-items/{id}/lease/clock-rebase", permission: "sessions:lease:admin"},
	} {
		op := mustOp(t, paths, tc.path, "post")
		if op["x-required-permission"] != tc.permission || !hasPathParam(op, "id") ||
			!hasParameter(op, "mode", "query") || !hasParameter(op, "If-Match", "header") {
			t.Errorf("lease mutation %s contract = %#v", tc.path, op)
		}
		if _, ok := op["requestBody"].(map[string]any); !ok {
			t.Errorf("lease mutation %s has no WorkCommand body", tc.path)
		}
	}
	acquire := mustOp(t, paths, "/v1/m/sessions/work-items/{id}/lease/acquire", "post")
	leaseSchema := requestBodySchema(t, acquire)
	if leaseSchema["additionalProperties"] != false {
		t.Errorf("lease WorkCommand accepts undeclared fields: %#v", leaseSchema)
	}
	leaseFields := requestBodyProperties(t, acquire)
	for name, wantType := range map[string]string{
		"holder_sid": "string", "holder_run_ref": "string", "holder_agent_ref": "string",
		"ttl_seconds": "integer", "fence": "integer", "force": "boolean",
		"unblock": "boolean", "changes_requested": "boolean",
		"reason": "string", "decision_id": "string", "evidence_ref": "string", "plan_hash": "string",
	} {
		field, ok := leaseFields[name].(map[string]any)
		if !ok || field["type"] != wantType {
			t.Errorf("lease WorkCommand field %s = %#v, want type %s", name, leaseFields[name], wantType)
		}
	}

	replay := mustOp(t, paths, "/v1/m/sessions/work-events/{event_id}/replay", "post")
	if replay["x-required-permission"] != "sessions:work:admin" ||
		!hasPathParam(replay, "event_id") || !hasParameter(replay, "mode", "query") ||
		!hasParameter(replay, "Idempotency-Key", "header") ||
		!hasParameter(replay, "If-Match", "header") ||
		!hasParameter(replay, "If-Plan-Hash", "header") {
		t.Errorf("outbox replay command envelope = %#v", replay)
	}
	replaySchema := requestBodySchema(t, replay)
	if replaySchema["additionalProperties"] != false {
		t.Errorf("outbox replay accepts undeclared fields: %#v", replaySchema)
	}
	replayFields := requestBodyProperties(t, replay)
	if len(replayFields) != 1 || replayFields["plan_hash"] == nil {
		t.Errorf("outbox replay body fields = %#v, want only optional plan_hash", replayFields)
	}

	stream := mustOp(t, paths, "/v1/m/sessions/work-stream", "get")
	content := stream["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["text/event-stream"]; !ok {
		t.Fatalf("work-stream 200 content = %v, want text/event-stream", content)
	}
	if !hasParameter(stream, "Last-Event-ID", "header") {
		t.Error("work-stream missing Last-Event-ID resume header")
	}
}

func TestSessionsRuntimeWorkControlOpenAPI(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{sessionsRuntimeWorkControlModule{}})
	paths := doc["paths"].(map[string]any)

	input := mustOp(t, paths, "/v1/m/sessions/runs/{ref}/input", "post")
	inputBody := input["requestBody"].(map[string]any)
	if inputBody["required"] != true || !hasPathParam(input, "ref") {
		t.Fatalf("run input request contract = %#v", input)
	}
	inputSchema := requestBodySchema(t, input)
	if inputSchema["additionalProperties"] != false {
		t.Errorf("run input accepts undeclared fields: %#v", inputSchema)
	}
	inputFields := requestBodyProperties(t, input)
	fence, _ := inputFields["work_lease_fence"].(map[string]any)
	if fence["type"] != "integer" || fence["format"] != "int64" || fence["minimum"] != 1 {
		t.Errorf("run input fence = %#v", fence)
	}

	stop := mustOp(t, paths, "/v1/m/sessions/runs/{ref}/stop", "post")
	stopBody := stop["requestBody"].(map[string]any)
	if stopBody["required"] != false || !hasPathParam(stop, "ref") {
		t.Fatalf("run stop request contract = %#v", stop)
	}
	stopSchema := requestBodySchema(t, stop)
	if stopSchema["additionalProperties"] != false {
		t.Errorf("run stop accepts undeclared fields: %#v", stopSchema)
	}
	stopFields := requestBodyProperties(t, stop)
	stopFence, _ := stopFields["work_lease_fence"].(map[string]any)
	if stopFence["type"] != "integer" || stopFence["format"] != "int64" || stopFence["minimum"] != 1 {
		t.Errorf("run stop fence = %#v", stopFence)
	}
	reason, _ := stopFields["reason"].(map[string]any)
	if reason["type"] != "string" || reason["maxLength"] != 512 {
		t.Errorf("run stop reason = %#v", reason)
	}
}

func TestSessionsProtocolBindingOpenAPI(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{sessionsProtocolBindingContractModule{}})
	paths := doc["paths"].(map[string]any)

	list := mustOp(t, paths, "/v1/m/sessions/protocol-bindings", "get")
	if list["x-required-permission"] != "sessions:protocol-binding:read" {
		t.Errorf("protocol binding list permission = %v", list["x-required-permission"])
	}
	for _, name := range []string{
		"workspace_id", "binding_spec_id", "work_item_id", "protocol", "peer_authority",
		"owner_kind", "owner_ref", "external_kind", "external_id", "verdict", "terminal",
		"limit", "cursor",
	} {
		if !hasParameter(list, name, "query") {
			t.Errorf("protocol binding list missing query parameter %s", name)
		}
	}
	assertParameterEnum(t, list, "protocol", "query", []string{"a2a", "mcp"})
	assertParameterEnum(t, list, "verdict", "query", []string{"CLEAN", "BROKEN", "UNKNOWN"})
	terminalSchema := parameterOf(t, list, "terminal", "query")["schema"].(map[string]any)
	if terminalSchema["type"] != "boolean" {
		t.Errorf("protocol binding terminal filter schema = %#v", terminalSchema)
	}
	assertPageParameterSchemas(t, list)
	get := mustOp(t, paths, "/v1/m/sessions/protocol-bindings/{id}", "get")
	if get["x-required-permission"] != "sessions:protocol-binding:read" || !hasPathParam(get, "id") {
		t.Errorf("protocol binding get contract = %#v", get)
	}
	assertProtocolBindingIDPathSchema(t, get)

	reconcile := mustOp(t, paths, "/v1/m/sessions/protocol-bindings/{id}/reconcile", "post")
	if reconcile["x-required-permission"] != "sessions:protocol-binding:write" || !hasPathParam(reconcile, "id") {
		t.Fatalf("protocol binding reconcile contract = %#v", reconcile)
	}
	for _, header := range []string{"Idempotency-Key", "If-Match", "If-Plan-Hash"} {
		if !hasParameter(reconcile, header, "header") {
			t.Errorf("protocol binding reconcile missing header %s", header)
		}
	}
	if schema := parameterOf(t, reconcile, "Idempotency-Key", "header")["schema"].(map[string]any); schema["format"] != "uuid" {
		t.Errorf("protocol binding reconcile idempotency schema = %#v", schema)
	}
	for _, header := range []string{"If-Match", "If-Plan-Hash"} {
		schema := parameterOf(t, reconcile, header, "header")["schema"].(map[string]any)
		if schema["pattern"] == nil {
			t.Errorf("protocol binding reconcile %s has no lexical precondition schema: %#v", header, schema)
		}
	}
	assertParameterEnum(t, reconcile, "mode", "query", []string{"validate", "plan", "test", "apply"})

	body := reconcile["requestBody"].(map[string]any)
	if body["required"] != false {
		t.Errorf("protocol binding reconcile body required = %v, want false", body["required"])
	}
	schema := requestBodySchema(t, reconcile)
	if schema["additionalProperties"] != false {
		t.Errorf("protocol binding reconcile accepts undeclared fields: %#v", schema)
	}
	fields := requestBodyProperties(t, reconcile)
	if len(fields) != 1 || fields["plan_hash"] == nil {
		t.Errorf("protocol binding reconcile body fields = %#v, want only plan_hash", fields)
	}
	responses := reconcile["responses"].(map[string]any)
	for _, status := range []string{"412", "428", "503"} {
		if responses[status] == nil {
			t.Errorf("protocol binding reconcile missing response %s", status)
		}
	}

	specList := mustOp(t, paths, "/v1/m/sessions/protocol-binding-specs", "get")
	if specList["x-required-permission"] != "sessions:protocol-binding:read" {
		t.Errorf("protocol binding spec list permission = %v", specList["x-required-permission"])
	}
	for _, name := range []string{
		"workspace_id", "binding_key", "generation", "protocol", "direction", "local_kind",
		"peer_authority", "state", "limit", "cursor",
	} {
		if !hasParameter(specList, name, "query") {
			t.Errorf("protocol binding spec list missing query parameter %s", name)
		}
	}
	assertParameterEnum(t, specList, "protocol", "query", []string{"a2a", "mcp"})
	assertParameterEnum(t, specList, "direction", "query", []string{"inbound", "outbound", "bidirectional"})
	assertParameterEnum(t, specList, "local_kind", "query", []string{"work_item", "agent", "model", "channel"})
	assertParameterEnum(t, specList, "state", "query", []string{"draft", "active", "disabled", "superseded"})
	assertPageParameterSchemas(t, specList)
	specGet := mustOp(t, paths, "/v1/m/sessions/protocol-binding-specs/{id}", "get")
	if specGet["x-required-permission"] != "sessions:protocol-binding:read" || !hasPathParam(specGet, "id") {
		t.Errorf("protocol binding spec get contract = %#v", specGet)
	}
	assertProtocolBindingIDPathSchema(t, specGet)

	create := mustOp(t, paths, "/v1/m/sessions/protocol-binding-specs", "post")
	if create["x-required-permission"] != "sessions:protocol-binding:write" ||
		!hasParameter(create, "Idempotency-Key", "header") ||
		!hasParameter(create, "If-Plan-Hash", "header") ||
		hasParameter(create, "If-Match", "header") {
		t.Errorf("protocol binding spec create contract = %#v", create)
	}
	assertParameterEnum(t, create, "mode", "query", []string{"validate", "plan", "apply"})
	createBody := create["requestBody"].(map[string]any)
	if createBody["required"] != true {
		t.Errorf("protocol binding spec create body required = %v, want true", createBody["required"])
	}
	createSchema := requestBodySchema(t, create)
	if createSchema["additionalProperties"] != false {
		t.Errorf("protocol binding spec create accepts undeclared fields: %#v", createSchema)
	}
	assertSchemaRequires(t, createSchema,
		"workspace_id", "binding_key", "generation", "protocol", "protocol_version",
		"direction", "local_kind", "local_selector", "peer_authority",
		"remote_resource_kind", "remote_resource_ref", "mapping_schema", "mapping",
		"permission_profile_ref", "currency_policy",
	)
	if schemaRequires(createSchema, "validation") {
		t.Errorf("protocol binding create must not require server-derived validation: %#v", createSchema["required"])
	}
	createFields := requestBodyProperties(t, create)
	assertSchemaEnum(t, createFields["protocol"], []string{"a2a", "mcp"})
	assertSchemaEnum(t, createFields["direction"], []string{"inbound", "outbound", "bidirectional"})
	assertSchemaEnum(t, createFields["local_kind"], []string{"work_item", "agent", "model", "channel"})
	assertSchemaEnum(t, createFields["currency_policy"], []string{"pinned"})
	if selector := createFields["local_selector"].(map[string]any); selector["type"] != "object" {
		t.Errorf("protocol binding local_selector schema = %#v", selector)
	}
	mapping := createFields["mapping"].(map[string]any)
	if mapping["minItems"] != 1 || mapping["maxItems"] != 128 {
		t.Errorf("protocol binding mapping bounds = %#v", mapping)
	}
	mappingItem := mapping["items"].(map[string]any)
	if mappingItem["additionalProperties"] != false {
		t.Errorf("protocol binding mapping rule accepts undeclared fields: %#v", mappingItem)
	}
	assertSchemaRequires(t, mappingItem, "source", "target", "cardinality", "transform")
	validation := createFields["validation"].(map[string]any)
	if validation["additionalProperties"] != false {
		t.Errorf("protocol binding validation accepts undeclared fields: %#v", validation)
	}
	if validation["readOnly"] != true {
		t.Errorf("protocol binding validation must be server-derived/read-only: %#v", validation)
	}
	assertSchemaRequires(t, validation, "verdict", "code")
	validationFields := validation["properties"].(map[string]any)
	assertSchemaEnum(t, validationFields["verdict"], []string{"CLEAN", "BROKEN", "UNKNOWN"})
	createResponses := create["responses"].(map[string]any)
	if createResponses["201"] == nil || createResponses["428"] == nil {
		t.Errorf("protocol binding spec create responses = %#v", createResponses)
	}

	for _, action := range []string{"activate", "disable"} {
		path := "/v1/m/sessions/protocol-binding-specs/{id}/" + action
		op := mustOp(t, paths, path, "post")
		if op["x-required-permission"] != "sessions:protocol-binding:admin" || !hasPathParam(op, "id") {
			t.Errorf("protocol binding spec %s contract = %#v", action, op)
		}
		for _, header := range []string{"Idempotency-Key", "If-Plan-Hash", "If-Match"} {
			if !hasParameter(op, header, "header") {
				t.Errorf("protocol binding spec %s missing header %s", action, header)
			}
		}
		if schema := parameterOf(t, op, "If-Match", "header")["schema"].(map[string]any); schema["pattern"] == nil {
			t.Errorf("protocol binding spec %s If-Match has no strong ETag schema: %#v", action, schema)
		}
		assertParameterEnum(t, op, "mode", "query", []string{"validate", "plan", "apply"})
		body := op["requestBody"].(map[string]any)
		if body["required"] != false {
			t.Errorf("protocol binding spec %s body required = %v, want false", action, body["required"])
		}
		transitionFields := requestBodyProperties(t, op)
		if len(transitionFields) != 1 || transitionFields["plan_hash"] == nil {
			t.Errorf("protocol binding spec %s body fields = %#v, want only plan_hash", action, transitionFields)
		}
		responses := op["responses"].(map[string]any)
		for _, status := range []string{"412", "428", "503"} {
			if responses[status] == nil {
				t.Errorf("protocol binding spec %s missing response %s", action, status)
			}
		}
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
// /openapi.beta.json (auth/setup-exempt) while /openapi.json stays the 48-path
// stable contract.
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
	if n := len(stable["paths"].(map[string]any)); n != 53 {
		t.Errorf("/openapi.json = %d paths, want 53 (stable contract incl. the wave-2 routes and /pod-readyz)", n)
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

func hasParameter(op map[string]any, name, in string) bool {
	for _, p := range paramsOf(op) {
		m := p.(map[string]any)
		if m["name"] == name && m["in"] == in {
			return true
		}
	}
	return false
}

func parameterOf(t *testing.T, op map[string]any, name, in string) map[string]any {
	t.Helper()
	for _, p := range paramsOf(op) {
		parameter := p.(map[string]any)
		if parameter["name"] == name && parameter["in"] == in {
			return parameter
		}
	}
	t.Fatalf("operation has no %s parameter %s", in, name)
	return nil
}

func assertParameterEnum(t *testing.T, op map[string]any, name, in string, want []string) {
	t.Helper()
	parameter := parameterOf(t, op, name, in)
	schema, ok := parameter["schema"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s parameter schema = %#v", in, name, parameter["schema"])
	}
	got, ok := schema["enum"].([]any)
	if !ok || len(got) != len(want) {
		t.Fatalf("%s %s parameter enum = %#v", in, name, schema["enum"])
	}
	for i, value := range want {
		if got[i] != value {
			t.Errorf("%s %s parameter enum[%d] = %v, want %s", in, name, i, got[i], value)
		}
	}
}

func assertPageParameterSchemas(t *testing.T, op map[string]any) {
	t.Helper()
	limit := parameterOf(t, op, "limit", "query")["schema"].(map[string]any)
	if limit["type"] != "integer" || limit["minimum"] != 1 || limit["maximum"] != 200 || limit["default"] != 100 {
		t.Errorf("page limit schema = %#v", limit)
	}
	cursor := parameterOf(t, op, "cursor", "query")["schema"].(map[string]any)
	if cursor["type"] != "string" || cursor["format"] != "uuid" {
		t.Errorf("page cursor schema = %#v", cursor)
	}
}

func assertProtocolBindingIDPathSchema(t *testing.T, op map[string]any) {
	t.Helper()
	schema := parameterOf(t, op, "id", "path")["schema"].(map[string]any)
	if schema["format"] != "uuid" || schema["pattern"] == nil {
		t.Errorf("protocol binding id path schema = %#v", schema)
	}
}

func assertSchemaEnum(t *testing.T, raw any, want []string) {
	t.Helper()
	schema, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("schema = %#v", raw)
	}
	got, ok := schema["enum"].([]any)
	if !ok || len(got) != len(want) {
		t.Fatalf("schema enum = %#v, want %v", schema["enum"], want)
	}
	for i, value := range want {
		if got[i] != value {
			t.Errorf("schema enum[%d] = %v, want %s", i, got[i], value)
		}
	}
}

func assertSchemaRequires(t *testing.T, schema map[string]any, want ...string) {
	t.Helper()
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema required = %#v", schema["required"])
	}
	set := make(map[string]bool, len(required))
	for _, name := range required {
		value, ok := name.(string)
		if !ok {
			t.Fatalf("non-string schema required entry = %#v", name)
		}
		set[value] = true
	}
	for _, name := range want {
		if !set[name] {
			t.Errorf("schema does not require %s: %#v", name, required)
		}
	}
}

func schemaRequires(schema map[string]any, name string) bool {
	required, ok := schema["required"].([]any)
	if !ok {
		return false
	}
	for _, entry := range required {
		if entry == name {
			return true
		}
	}
	return false
}

func requestBodyProperties(t *testing.T, op map[string]any) map[string]any {
	t.Helper()
	schema := requestBodySchema(t, op)
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("requestBody schema has no properties")
	}
	return properties
}

func requestBodySchema(t *testing.T, op map[string]any) map[string]any {
	t.Helper()
	body, ok := op["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("operation has no requestBody")
	}
	content, ok := body["content"].(map[string]any)
	if !ok {
		t.Fatal("requestBody has no content")
	}
	media, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatal("requestBody has no application/json content")
	}
	schema, ok := media["schema"].(map[string]any)
	if !ok {
		t.Fatal("requestBody has no schema")
	}
	return schema
}
