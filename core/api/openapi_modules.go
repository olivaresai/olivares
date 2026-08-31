// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// The module-route OpenAPI document is the SECOND published REST contract:
// a BETA-tier document covering the /v1/m/<namespace>/ module routes that ARE the
// product (finops, compliance, governance, sessions, models, knowledge, …). It is
// built from the routes the modules actually register — never by hand — so it
// cannot drift from what the engine serves: a module that adds a route adds it to
// this document for free, WITHOUT editing core/api/openapi.go.
//
// It is deliberately distinct from the stable core contract (buildOpenAPI →
// web/openapi/openapi.json, the engine paths): the module routes carry no
// 24-month stable promise, so folding them into the stable document would dilute a
// contract integrators hold us to. Two documents, two tiers (stability.go):
//   - GET /openapi.json       — stable, the core engine paths (intact, unchanged).
//   - GET /openapi.beta.json  — beta, the module routes (this file).
//
// The SDK pipeline (clients/generator) regenerates from the UNION of both, marking
// each beta operation with its language-native stability annotation.
//
// CONTRACT FOR MODULE SESSIONS: a module contributes its REST surface to this
// document simply by registering routes in APIRoutes (modules.go). Nothing else is
// required — the namespace, method, pattern and required permission are captured
// from the RouteRegistrar seam, path parameters become OpenAPI `in: path`
// parameters, and the operation is stamped beta. The 200 body is the product's
// generic JSON envelope; the few routes that stream Server-Sent Events (a pattern
// whose last segment is "stream" or "attach") or always return a non-JSON export
// are classified raw here (moduleRouteRawContentType) so the SDKs emit a
// bytes-returning operation instead of a JSON decoder that can never succeed.

const (
	// moduleBetaNotice is the machine-readable banner stamped on info.x-beta-notice.
	moduleBetaNotice = "beta, may change; not covered by the 24-month stable window of the core REST contract"

	// moduleDocDescription is the human banner rendered at the top of the beta
	// reference. It is deliberately honest: a beta surface, identical auth/tenancy
	// to the core contract, generic JSON bodies unless declared otherwise.
	moduleDocDescription = "The BETA REST surface of Olivares AI: the module routes under " +
		"/v1/m/<namespace>/ (finops, compliance, governance, identity, sessions, accessmap, " +
		"models, knowledge, …) that make up the product. This is a BETA contract — shapes may " +
		"change with notice and these routes are NOT covered by the 24-month stable window of the " +
		"core contract served at /openapi.json. Authentication and tenancy are identical to the " +
		"core contract: an opaque bearer token (session olvs_… or API key olvk_…), with the tenant " +
		"resolved from a bound token or the X-Olivares-Tenant header; each operation also declares " +
		"the permission it requires (x-required-permission). Response bodies are the product's generic " +
		"JSON envelope unless an operation declares otherwise (a few routes stream Server-Sent Events or " +
		"export a non-JSON format). Every mutation declares its handler-reviewed request-body disposition " +
		"in x-olivares-request-body-disposition; bodies that are not ordinary JSON DTOs publish their exact " +
		"media type without inventing fields. " +
		"See info.x-stability-policy for the policy."
)

// moduleRoute is one route a module registered, captured from the RouteRegistrar
// seam (method, the module-relative chi pattern, the permission it requires) plus
// the owning namespace.
type moduleRoute struct {
	ns      string
	method  string
	pattern string // module-relative chi pattern, e.g. "/spend" or "/{id}"
	perm    auth.Permission
}

// recordingRegistrar is a RouteRegistrar that RECORDS routes instead of mounting
// them. Running a module's APIRoutes against it yields exactly the routes the
// engine mounts (the same APIRoutes call), with no store, no server and no handler
// invocation — so the published document is built from the real registration, and
// `olivares openapi --beta` can emit it without a running server.
type recordingRegistrar struct {
	ns  string
	out *[]moduleRoute
}

func (r recordingRegistrar) Handle(method, pattern string, perm auth.Permission, _ ModuleHandler) {
	*r.out = append(*r.out, moduleRoute{
		ns: r.ns, method: strings.ToUpper(method), pattern: pattern, perm: perm,
	})
}

// HandleEntity records an entity route exactly like a collection one: the published
// document describes the HTTP surface, and opting into stored-lineage authorization does
// not change the request or the response.
func (r recordingRegistrar) HandleEntity(method, pattern string, perm auth.Permission, _ EntityRef, h ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}

// collectModuleRoutes runs every module's APIRoutes against a recording registrar
// and returns the captured routes, sorted for a deterministic document.
func collectModuleRoutes(modules []Module) []moduleRoute {
	var routes []moduleRoute
	for _, m := range modules {
		m.APIRoutes(recordingRegistrar{ns: m.APINamespace(), out: &routes})
	}
	sort.Slice(routes, func(i, j int) bool {
		a, b := routes[i], routes[j]
		if a.ns != b.ns {
			return a.ns < b.ns
		}
		if a.pattern != b.pattern {
			return a.pattern < b.pattern
		}
		return a.method < b.method
	})
	return routes
}

// moduleSpecPath is the spec-canonical OpenAPI path for a module route:
// /v1/m/<ns><pattern> with any trailing slash trimmed (so a "/" collection
// pattern reads /v1/m/<ns>, matching the core collection convention).
func moduleSpecPath(r moduleRoute) string {
	return canonicalRoutePattern("/v1/m/" + r.ns + r.pattern)
}

// moduleRouteRawContentType reports the 200 content type of a module route that
// does NOT return JSON, so the document declares it raw. Verified against the
// handlers: the SSE streams (every route whose last path segment is
// "stream" or "attach") and the one always-CSV export. Every other module route —
// including the format-switched routes that DEFAULT to JSON (compliance evidence
// export, /dora and the model-card render: ?format=csv|oscal|md is opt-in) —
// returns the generic JSON envelope.
func moduleRouteRawContentType(r moduleRoute) (string, bool) {
	if r.ns == "sessions" && r.method == http.MethodGet && r.pattern == "/work-stream" {
		return "text/event-stream", true
	}
	switch r.pattern[strings.LastIndex(r.pattern, "/")+1:] {
	case "stream", "attach":
		return "text/event-stream", true
	}
	if r.ns == "finops" && r.method == http.MethodGet && r.pattern == "/spend/export" {
		// modules/finops/focus.go: the FOCUS export is always CSV on 200.
		return "text/csv", true
	}
	return "", false
}

// moduleRouteRawRequestBody reports a raw request body classified directly at
// the shared composition layer. Feature-owned raw bodies (for example an HTML
// template or an NDJSON import) live in their handler-derived producers instead.
// Verified: the workspace file write body IS the file's content
// (modules/sessions/workspace_api.go handleWriteFile reads r.Body raw).
func moduleRouteRawRequestBody(r moduleRoute) (string, bool) {
	if r.ns == "sessions" && r.method == http.MethodPut && r.pattern == "/workspaces/{ref}/files/raw" {
		return "application/octet-stream", true
	}
	return "", false
}

// pathParamNames returns the {param} names of a chi pattern, in order.
func pathParamNames(pattern string) []string {
	var out []string
	for _, seg := range strings.Split(pattern, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && len(seg) > 2 {
			out = append(out, seg[1:len(seg)-1])
		}
	}
	return out
}

// --- the beta document builder ------------------------------------------------
//
// Self-contained helpers (oa*) mirror the closures buildOpenAPI uses for the
// stable document. They are kept separate ON PURPOSE so the stable builder — and
// thus the committed stable snapshot — is never touched by this work.

func oaObj(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func oaJSONResp(desc string) map[string]any {
	return oaObj("description", desc, "content",
		oaObj("application/json", oaObj("schema", oaObj("type", "object"))))
}

func oaTenantParam() map[string]any {
	return oaObj("name", "X-Olivares-Tenant", "in", "header", "required", false,
		"description", "Target tenant id; required when the principal can act in more than one tenant.",
		"schema", oaObj("type", "string", "format", "uuid"))
}

func oaBearer() []any { return []any{oaObj("bearerAuth", []any{})} }

// moduleResponses is the response set of a module operation: the shared error
// envelope plus a 200 that is JSON (the default) or the raw content type the route
// actually returns (SSE / export).
func moduleResponses(r moduleRoute) map[string]any {
	resp := oaObj(
		"400", oaJSONResp("bad request"),
		"401", oaJSONResp("unauthenticated"),
		"403", oaJSONResp("forbidden"),
		"404", oaJSONResp("not found"),
		"409", oaJSONResp("conflict / setup required"),
		"429", oaJSONResp("rate limited"),
	)
	if sessionsWorkRoute(r) {
		resp["412"] = oaJSONResp("ETag or plan precondition failed")
		resp["422"] = oaJSONResp("domain invariant not satisfied")
		resp["423"] = oaJSONResp("work is blocked")
		resp["503"] = oaJSONResp("required evidence, policy, clock or store is unavailable")
	}
	if sessionsProtocolBindingReconcile(r) {
		resp["412"] = oaJSONResp("ETag or plan precondition failed")
		resp["428"] = oaJSONResp("strong ETag required for apply")
		resp["503"] = oaJSONResp("remote observation is unavailable")
	}
	if sessionsProtocolBindingSpecMutation(r) {
		resp["412"] = oaJSONResp("ETag or plan precondition failed")
		resp["428"] = oaJSONResp("apply precondition required")
		unavailable := "required store is unavailable"
		if r.pattern == "/protocol-binding-specs/{id}/activate" {
			unavailable = "fresh capability observation or required store is unavailable"
		}
		resp["503"] = oaJSONResp(unavailable)
		if r.pattern == "/protocol-binding-specs" {
			resp["201"] = oaJSONResp("ProtocolBindingSpec draft created")
		}
	}
	if ct, raw := moduleRouteRawContentType(r); raw {
		resp["200"] = oaObj("description", "OK ("+ct+")",
			"content", oaObj(ct, oaObj("schema", oaObj("type", "string"))))
	} else {
		resp["200"] = oaJSONResp("OK")
	}
	return resp
}

// moduleOperation builds one OpenAPI operation for a module route. The summary is
// derived honestly (the namespace plus the permission the route declares — both
// facts of the registration); no request/response semantics are invented.
func moduleOperation(r moduleRoute) map[string]any {
	summary := r.ns + " module route"
	if r.perm != "" {
		summary += " (requires " + string(r.perm) + ")"
	}
	o := oaObj(
		"summary", summary,
		"security", oaBearer(),
		"responses", moduleResponses(r),
	)
	if r.perm != "" {
		o["x-required-permission"] = string(r.perm)
	}
	if moduleRouteIsMutation(r) {
		o[moduleRequestBodyDispositionExtension] = string(moduleRequestBodyDispositionFor(r))
	}
	if ct, raw := moduleRouteRawRequestBody(r); raw {
		o["requestBody"] = oaObj("required", true, "content",
			oaObj(ct, oaObj("schema", oaObj("type", "string", "format", "binary"))))
	} else if body, ok := moduleRequestBody(r); ok {
		o["requestBody"] = body
	}
	params := []any{oaTenantParam()}
	for _, p := range pathParamNames(r.pattern) {
		schema := oaObj("type", "string")
		if p == "id" && sessionsProtocolBindingRoute(r) {
			schema = oaProtocolBindingIDSchema()
		}
		params = append(params, oaObj("name", p, "in", "path", "required", true,
			"description", "Path parameter "+p+".", "schema", schema))
	}
	params = append(params, moduleRouteParameters(r)...)
	o["parameters"] = params
	return o
}

func oaStringParam(name, in, description string, required bool) map[string]any {
	return oaObj("name", name, "in", in, "required", required,
		"description", description, "schema", oaObj("type", "string"))
}

func oaParam(name, in, description string, required bool, schema map[string]any) map[string]any {
	return oaObj("name", name, "in", in, "required", required,
		"description", description, "schema", schema)
}

func oaEnum(values ...string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func oaProtocolBindingHashSchema() map[string]any {
	return oaObj(
		"type", "string",
		"pattern", "^(sha256:)?[0-9A-Fa-f]{64}$",
	)
}

func oaProtocolBindingIDSchema() map[string]any {
	return oaObj(
		"type", "string", "format", "uuid",
		"pattern", "^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
	)
}

func sessionsWorkRoute(r moduleRoute) bool {
	if r.ns != "sessions" {
		return false
	}
	return strings.HasPrefix(r.pattern, "/work-items") ||
		strings.HasPrefix(r.pattern, "/work-events") ||
		strings.HasPrefix(r.pattern, "/decisions") ||
		strings.HasPrefix(r.pattern, "/leases") || r.pattern == "/work-stream"
}

func sessionsWorkMutation(r moduleRoute) bool {
	return sessionsWorkRoute(r) && r.method != http.MethodGet
}

func sessionsProtocolBindingReconcile(r moduleRoute) bool {
	return r.ns == "sessions" && r.method == http.MethodPost &&
		r.pattern == "/protocol-bindings/{id}/reconcile"
}

func sessionsProtocolBindingRoute(r moduleRoute) bool {
	return r.ns == "sessions" && (strings.HasPrefix(r.pattern, "/protocol-bindings") ||
		strings.HasPrefix(r.pattern, "/protocol-binding-specs"))
}

func sessionsProtocolBindingSpecMutation(r moduleRoute) bool {
	if r.ns != "sessions" || r.method != http.MethodPost {
		return false
	}
	switch r.pattern {
	case "/protocol-binding-specs", "/protocol-binding-specs/{id}/activate",
		"/protocol-binding-specs/{id}/disable":
		return true
	default:
		return false
	}
}

func sessionsProtocolBindingSpecStateMutation(r moduleRoute) bool {
	return sessionsProtocolBindingSpecMutation(r) && r.pattern != "/protocol-binding-specs"
}

func sessionsProtocolBindingSpecList(r moduleRoute) bool {
	return r.ns == "sessions" && r.method == http.MethodGet &&
		r.pattern == "/protocol-binding-specs"
}

func sessionsProtocolBindingList(r moduleRoute) bool {
	return r.ns == "sessions" && r.method == http.MethodGet &&
		r.pattern == "/protocol-bindings"
}

func protocolBindingPageParameters() []any {
	return []any{
		oaParam("limit", "query", "Keyset page size from 1 through 200; default 100.", false,
			oaObj("type", "integer", "minimum", 1, "maximum", 200, "default", 100)),
		oaParam("cursor", "query", "Opaque UUIDv7 keyset cursor returned by the previous page.", false,
			oaProtocolBindingIDSchema()),
	}
}

func protocolBindingSpecListParameters() []any {
	params := []any{
		oaParam("workspace_id", "query",
			"Workspace UUID. It may be omitted only when the authenticated principal is confined to one workspace.",
			false, oaProtocolBindingIDSchema()),
		oaParam("binding_key", "query", "Exact normalized binding-key filter.", false,
			oaObj("type", "string", "minLength", 1, "maxLength", 128)),
		oaParam("generation", "query", "Exact immutable spec generation.", false,
			oaObj("type", "integer", "format", "int64", "minimum", 1)),
		oaParam("protocol", "query", "Exact protocol filter.", false,
			oaObj("type", "string", "enum", oaEnum("a2a", "mcp"))),
		oaParam("direction", "query", "Exact binding direction filter.", false,
			oaObj("type", "string", "enum", oaEnum("inbound", "outbound", "bidirectional"))),
		oaParam("local_kind", "query", "Exact local resource-kind filter.", false,
			oaObj("type", "string", "enum", oaEnum("work_item", "agent", "model", "channel"))),
		oaParam("peer_authority", "query", "Exact normalized peer authority filter.", false,
			oaObj("type", "string", "minLength", 1, "maxLength", 512)),
		oaParam("state", "query", "Exact spec lifecycle-state filter.", false,
			oaObj("type", "string", "enum", oaEnum("draft", "active", "disabled", "superseded"))),
	}
	return append(params, protocolBindingPageParameters()...)
}

func protocolBindingListParameters() []any {
	params := []any{
		oaParam("workspace_id", "query",
			"Workspace UUID. It may be omitted only when the authenticated principal is confined to one workspace.",
			false, oaProtocolBindingIDSchema()),
		oaParam("binding_spec_id", "query", "Exact ProtocolBindingSpec UUID filter.", false,
			oaProtocolBindingIDSchema()),
		oaParam("work_item_id", "query", "Exact WorkItem UUID filter.", false,
			oaProtocolBindingIDSchema()),
		oaParam("protocol", "query", "Exact protocol filter.", false,
			oaObj("type", "string", "enum", oaEnum("a2a", "mcp"))),
		oaParam("peer_authority", "query", "Exact normalized peer authority filter.", false,
			oaObj("type", "string", "minLength", 1, "maxLength", 512)),
		oaParam("owner_kind", "query", "Exact normalized owner kind; owner_ref must be supplied with it.", false,
			oaObj("type", "string", "minLength", 1, "maxLength", 512)),
		oaParam("owner_ref", "query", "Exact owner reference; owner_kind must be supplied with it.", false,
			oaObj("type", "string", "minLength", 1, "maxLength", 512)),
		oaParam("external_kind", "query", "Exact normalized remote result-kind filter.", false,
			oaObj("type", "string", "minLength", 1, "maxLength", 512)),
		oaParam("external_id", "query", "Exact remote resource identifier filter.", false,
			oaObj("type", "string", "minLength", 1, "maxLength", 512)),
		oaParam("verdict", "query", "Exact last-observation verdict filter.", false,
			oaObj("type", "string", "enum", oaEnum("CLEAN", "BROKEN", "UNKNOWN"))),
		oaParam("terminal", "query", "Filter by terminal or non-terminal bindings.", false,
			oaObj("type", "boolean")),
	}
	return append(params, protocolBindingPageParameters()...)
}

// moduleRouteParameters describes the durable work-command envelope from facts enforced
// by the sessions handlers. Unlike guessed DTO fields, these route/header/query
// controls are uniform across every work mutation and are required for a
// generated client to invoke validate, plan and apply correctly.
func moduleRouteParameters(r moduleRoute) []any {
	if sessionsProtocolBindingReconcile(r) {
		mode := oaParam("mode", "query",
			"Mandatory reconciliation phase. validate and plan are local and observational; test reads the peer without a local write; apply revalidates and commits the observation.",
			true, oaObj("type", "string", "enum", oaEnum("validate", "plan", "test", "apply")))
		return []any{
			mode,
			oaParam("Idempotency-Key", "header",
				"UUID required when mode=apply; reuse it only for an exact retry.", false,
				oaObj("type", "string", "format", "uuid")),
			oaParam("If-Match", "header",
				"Strong ProtocolBinding ETag required when mode=apply; when supplied in another mode it must match the current version.",
				false, oaObj("type", "string", "pattern", "^\\\"v[1-9][0-9]*\\\"$")),
			oaParam("If-Plan-Hash", "header",
				"Optional SHA-256 plan hash. It must agree with body.plan_hash when both are supplied and must match the current reconciliation plan.",
				false, oaProtocolBindingHashSchema()),
		}
	}
	if sessionsProtocolBindingSpecMutation(r) {
		mode := oaParam("mode", "query",
			"Mandatory authoring phase. The server derives a fresh capability witness; validate and plan are observational, while apply revalidates and commits the spec transition.",
			true, oaObj("type", "string", "enum", oaEnum("validate", "plan", "apply")))
		params := []any{
			mode,
			oaParam("Idempotency-Key", "header",
				"Canonical UUID required when mode=apply; reuse it only for an exact retry.", false,
				oaObj("type", "string", "format", "uuid")),
			oaParam("If-Plan-Hash", "header",
				"SHA-256 plan hash required when mode=apply; apply must reproduce it. For state transitions it must agree with body.plan_hash when both are supplied.",
				false, oaProtocolBindingHashSchema()),
		}
		if sessionsProtocolBindingSpecStateMutation(r) {
			params = append(params, oaParam("If-Match", "header",
				"Strong ProtocolBindingSpec ETag required when mode=apply; when supplied in validate or plan it must match the current version.",
				false, oaObj("type", "string", "pattern", "^\\\"v[1-9][0-9]*\\\"$")))
		}
		return params
	}
	if sessionsProtocolBindingSpecList(r) {
		return protocolBindingSpecListParameters()
	}
	if sessionsProtocolBindingList(r) {
		return protocolBindingListParameters()
	}
	if !sessionsWorkRoute(r) {
		return nil
	}
	if sessionsWorkMutation(r) {
		mode := oaStringParam("mode", "query",
			"Mandatory command phase. validate and plan are observational; apply revalidates and mutates.", true)
		mode["schema"] = oaObj("type", "string", "enum", []any{"validate", "plan", "apply"})
		return []any{
			mode,
			oaStringParam("Idempotency-Key", "header",
				"UUID required when mode=apply; reuse it only for an exact retry.", false),
			oaStringParam("If-Match", "header",
				"Strong resource ETag required when mode=apply mutates an existing resource.", false),
			oaStringParam("If-Plan-Hash", "header", sessionsWorkPlanHashDescription(r), false),
		}
	}
	params := []any{}
	if r.pattern == "/work-stream" {
		params = append(params,
			oaStringParam("cursor", "query", "Resume strictly after this persisted UUIDv7 WorkEvent id.", false),
			oaStringParam("Last-Event-ID", "header", "SSE resume cursor; must agree with cursor when both are sent.", false),
		)
		return params
	}
	if r.pattern == "/work-items" || r.pattern == "/decisions" || r.pattern == "/leases" ||
		strings.HasSuffix(r.pattern, "/dependencies") || strings.HasSuffix(r.pattern, "/acceptance") ||
		strings.HasSuffix(r.pattern, "/events") {
		params = append(params,
			oaStringParam("limit", "query", "Keyset page size from 1 through 200; default 100.", false),
			oaStringParam("cursor", "query", "Opaque UUIDv7 keyset cursor returned by the previous page.", false),
		)
	}
	if r.pattern == "/work-items" {
		for _, name := range []string{"status", "priority", "work_kind", "owner_kind", "owner_ref",
			"provenance_kind", "provenance_ref", "parent_id", "archived", "due_before", "updated_after"} {
			params = append(params, oaStringParam(name, "query", "Allowlisted WorkItem filter; filters combine with AND.", false))
		}
	}
	if r.pattern == "/decisions" {
		for _, name := range []string{"work_item_id", "decision_key", "subject_kind", "subject_ref",
			"decided_by_kind", "decided_by_ref", "effective", "revoked"} {
			params = append(params, oaStringParam(name, "query", "Allowlisted Decision filter; filters combine with AND.", false))
		}
	}
	if r.pattern == "/leases" {
		for _, name := range []string{"work_item_id", "holder_sid", "state", "expires_before"} {
			params = append(params, oaStringParam(name, "query", "Allowlisted WorkLease filter; filters combine with AND.", false))
		}
	}
	return params
}

func sessionsWorkPlanHashDescription(r moduleRoute) string {
	if r.pattern == "/work-events/{event_id}/replay" {
		return "SHA-256 plan hash required when mode=apply; apply must reproduce it."
	}
	return "Optional SHA-256 plan hash that apply must reproduce."
}

func protocolBindingSpecInputSchema() map[string]any {
	refSchema := func(description string) map[string]any {
		return oaObj(
			"type", "string", "minLength", 1, "maxLength", 512,
			"description", description,
		)
	}
	mappingRule := oaObj(
		"type", "object",
		"additionalProperties", false,
		"required", oaEnum("source", "target", "cardinality", "transform"),
		"properties", oaObj(
			"source", refSchema("Exact local field or projection reference."),
			"target", refSchema("Exact remote field or projection reference."),
			"cardinality", oaObj(
				"type", "string",
				"enum", oaEnum("one_to_one", "one_to_many", "many_to_one"),
			),
			"transform", oaObj(
				"type", "string",
				"enum", oaEnum("identity", "text", "reference", "metadata", "status"),
			),
		),
	)
	knownLoss := oaObj(
		"type", "object",
		"additionalProperties", false,
		"required", oaEnum("field", "reason_code"),
		"properties", oaObj(
			"field", refSchema("Field whose semantics are not preserved by the mapping."),
			"reason_code", oaObj("type", "string", "minLength", 1, "maxLength", 128),
			"accepted", oaObj(
				"type", "boolean", "default", false,
				"description", "Whether the semantic loss has an explicit acceptance witness.",
			),
			"acceptance_ref", refSchema(
				"Required when accepted=true and rejected when accepted=false.",
			),
		),
	)
	validation := oaObj(
		"type", "object",
		"additionalProperties", false,
		"required", oaEnum("verdict", "code"),
		"readOnly", true,
		"description", "Server-derived capability witness. Any client-supplied value is ignored; omit this property. Activation requires a fresh CLEAN witness with a non-empty observed_at.",
		"properties", oaObj(
			"verdict", oaObj("type", "string", "enum", oaEnum("CLEAN", "BROKEN", "UNKNOWN")),
			"code", oaObj("type", "string", "minLength", 1, "maxLength", 128),
			"observed_at", oaObj("type", "string", "format", "date-time"),
		),
	)

	return oaObj(
		"type", "object",
		"description", "Closed ProtocolBindingSpecInput for exactly one immutable, version-pinned protocol mapping generation.",
		"additionalProperties", false,
		"required", oaEnum(
			"workspace_id", "binding_key", "generation", "protocol", "protocol_version",
			"direction", "local_kind", "local_selector", "peer_authority",
			"remote_resource_kind", "remote_resource_ref", "mapping_schema", "mapping",
			"permission_profile_ref", "currency_policy",
		),
		"properties", oaObj(
			"workspace_id", oaProtocolBindingIDSchema(),
			"binding_key", oaObj("type", "string", "minLength", 1, "maxLength", 128),
			"generation", oaObj("type", "integer", "format", "int64", "minimum", 1),
			"protocol", oaObj("type", "string", "enum", oaEnum("a2a", "mcp")),
			"protocol_version", refSchema(
				"Pinned protocol version; latest, current and * are rejected.",
			),
			"direction", oaObj(
				"type", "string", "enum", oaEnum("inbound", "outbound", "bidirectional"),
			),
			"local_kind", oaObj(
				"type", "string", "enum", oaEnum("work_item", "agent", "model", "channel"),
			),
			"local_selector", oaObj(
				"type", "object", "additionalProperties", true,
				"description", "Canonicalizable JSON object selecting the local surface; encoded size is limited to 64 KiB.",
			),
			"peer_authority", refSchema("Peer authority or normalized absolute authority URL."),
			"remote_resource_kind", oaObj("type", "string", "minLength", 1, "maxLength", 128),
			"remote_resource_ref", refSchema("Opaque remote resource reference."),
			"mapping_schema", oaObj("type", "string", "minLength", 1, "maxLength", 128),
			"mapping", oaObj(
				"type", "array", "minItems", 1, "maxItems", 128, "items", mappingRule,
			),
			"known_losses", oaObj(
				"type", "array", "maxItems", 128, "items", knownLoss,
			),
			"rule_refs", oaObj(
				"type", "array", "maxItems", 64, "uniqueItems", true,
				"items", refSchema("Opaque reference to a governing rule."),
			),
			"permission_profile_ref", refSchema("Pinned permission-profile reference."),
			"currency_policy", oaObj("type", "string", "enum", oaEnum("pinned")),
			"validation", validation,
			"supersedes_id", oaProtocolBindingIDSchema(),
		),
	)
}

func protocolBindingPlanHashBodySchema(description string) map[string]any {
	planHash := oaProtocolBindingHashSchema()
	planHash["description"] = "Optional plan precondition; it must agree with If-Plan-Hash when both are supplied."
	return oaObj(
		"type", "object",
		"description", description,
		"additionalProperties", false,
		"properties", oaObj("plan_hash", planHash),
	)
}

// moduleRequestBody returns a request-body declaration for the module routes
// whose body has a contract known at the API composition layer. Registry-backed
// eventing fields come from the engine catalog; sessions work/runtime fields are
// the narrow command envelopes enforced by their registered handlers.
func moduleRequestBody(r moduleRoute) (map[string]any, bool) {
	if body, ok := claudeAgentsRequestBody(r); ok {
		return body, true
	}
	if body, ok := capabilitiesRequestBody(r); ok {
		return body, true
	}
	if body, ok := consoleViewsRequestBody(r); ok {
		return body, true
	}
	if body, ok := healthRequestBody(r); ok {
		return body, true
	}
	if body, ok := inferenceProxyRequestBody(r); ok {
		return body, true
	}
	if body, ok := notifyRequestBody(r); ok {
		return body, true
	}
	if body, ok := claudePolicyRequestBody(r); ok {
		return body, true
	}
	if body, ok := recordingRequestBody(r); ok {
		return body, true
	}
	if body, ok := redTeamRequestBody(r); ok {
		return body, true
	}
	if body, ok := voiceRequestBody(r); ok {
		return body, true
	}
	if body, ok := sandboxRequestBody(r); ok {
		return body, true
	}
	if body, ok := securityRequestBody(r); ok {
		return body, true
	}
	if body, ok := reportingRequestBody(r); ok {
		return body, true
	}
	if body, ok := orchestrationRequestBody(r); ok {
		return body, true
	}
	if body, ok := sourceScopeRequestBody(r); ok {
		return body, true
	}
	if body, ok := finopsRequestBody(r); ok {
		return body, true
	}
	if body, ok := modelsRequestBody(r); ok {
		return body, true
	}
	if body, ok := complianceRequestBody(r); ok {
		return body, true
	}
	if body, ok := governanceRequestBody(r); ok {
		return body, true
	}
	if body, ok := evalsRequestBody(r); ok {
		return body, true
	}
	if body, ok := catalogRequestBody(r); ok {
		return body, true
	}
	if body, ok := eventingRequestBody(r); ok {
		return body, true
	}
	if body, ok := knowledgeRequestBody(r); ok {
		return body, true
	}
	if body, ok := deployRequestBody(r); ok {
		return body, true
	}
	if body, ok := sessionsClosureRequestBody(r); ok {
		return body, true
	}
	if r.ns == "sessions" && r.method == http.MethodPost {
		var schema map[string]any
		required := true
		switch r.pattern {
		case "/protocol-binding-specs":
			schema = protocolBindingSpecInputSchema()
		case "/protocol-binding-specs/{id}/activate", "/protocol-binding-specs/{id}/disable":
			required = false
			schema = protocolBindingPlanHashBodySchema(
				"Optional spec-transition plan precondition. The body may be empty in validate and plan; apply still requires a matching plan hash through this field or If-Plan-Hash.",
			)
		case "/protocol-bindings/{id}/reconcile":
			required = false
			schema = protocolBindingPlanHashBodySchema(
				"Optional reconciliation plan precondition. No body is required in any mode; apply requires If-Match but does not require a plan hash.",
			)
		case "/runs/{ref}/input":
			schema = oaObj(
				"type", "object",
				"description", "One run input. Omit work_lease_fence for legacy non-work runs; a positive value selects fenced WorkItem control.",
				"additionalProperties", false,
				"properties", oaObj(
					"line", oaObj("type", "string", "minLength", 1),
					"message", oaObj("type", "object", "additionalProperties", true),
					"work_lease_fence", oaObj("type", "integer", "format", "int64", "minimum", 1),
				),
			)
		case "/runs/{ref}/stop":
			required = false
			schema = oaObj(
				"type", "object",
				"description", "Optional fenced stop command. An empty body preserves legacy non-work run behavior; reason requires a positive work_lease_fence.",
				"additionalProperties", false,
				"properties", oaObj(
					"work_lease_fence", oaObj("type", "integer", "format", "int64", "minimum", 1),
					"reason", oaObj("type", "string", "minLength", 1, "maxLength", 512),
				),
			)
		}
		if schema != nil {
			return oaObj("required", required, "content", oaObj(
				"application/json", oaObj("schema", schema),
			)), true
		}
	}
	if sessionsWorkMutation(r) {
		properties := oaObj("command", oaObj("type", "string"))
		description := "WorkCommand document shared by validate, plan and apply. Route target fields are server-bound."
		required := true
		if r.pattern == "/work-events/{event_id}/replay" {
			description = "Optional outbox replay plan document. event_id is server-bound; apply uses the required If-Plan-Hash header."
			properties = oaObj("plan_hash", oaObj("type", "string", "minLength", 64, "maxLength", 64))
			required = false
		}
		if strings.Contains(r.pattern, "/lease/") {
			description = "Lease WorkCommand shared by validate, plan and apply. The WorkItem target is server-bound."
			properties["holder_sid"] = oaObj("type", "string")
			properties["holder_run_ref"] = oaObj("type", "string")
			properties["holder_agent_ref"] = oaObj("type", "string")
			properties["ttl_seconds"] = oaObj("type", "integer", "format", "int64")
			properties["fence"] = oaObj("type", "integer", "format", "int64")
			properties["force"] = oaObj("type", "boolean")
			properties["unblock"] = oaObj("type", "boolean")
			properties["changes_requested"] = oaObj("type", "boolean")
			// These fields are a union across the six lease commands. Their
			// command-specific required/empty/length rules are enforced by the
			// domain validator; the shared envelope must not claim that an optional
			// empty value accepted by another lease verb is structurally invalid.
			properties["reason"] = oaObj("type", "string")
			properties["decision_id"] = oaObj("type", "string")
			properties["evidence_ref"] = oaObj("type", "string")
			properties["plan_hash"] = oaObj("type", "string")
		}
		additionalProperties := true
		if strings.Contains(r.pattern, "/lease/") || r.pattern == "/work-events/{event_id}/replay" {
			// The lease handlers decode a closed WorkCommand projection with
			// json.Decoder.DisallowUnknownFields. Keeping this open made generated
			// clients advertise documents the real API rejects.
			additionalProperties = false
		}
		return oaObj("required", required, "content", oaObj("application/json", oaObj("schema", oaObj(
			"type", "object",
			"description", description,
			"additionalProperties", additionalProperties,
			"properties", properties,
		)))), true
	}
	return nil, false
}

// sinkFormatEnum renders the eventing sink_format vocabulary for the OpenAPI
// enum from the sdk/siemwire catalog — auditFormatEnum's pattern applied to the
// beta surface. The empty spelling leads because an unset format is valid and
// selects the surface default.
func sinkFormatEnum() []any {
	toks := siemwire.EventingSinkFormats().Tokens()
	out := make([]any, 0, len(toks)+1)
	out = append(out, "")
	for _, t := range toks {
		out = append(out, string(t))
	}
	return out
}

// buildModuleOpenAPI emits the beta OpenAPI 3.1 document for the given routes.
func buildModuleOpenAPI(routes []moduleRoute) map[string]any {
	paths := map[string]any{}
	for _, r := range routes {
		full := moduleSpecPath(r)
		item, ok := paths[full].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[full] = item
		}
		item[strings.ToLower(r.method)] = moduleOperation(r)
	}
	// Module routes are the beta tier; a deprecation entry (stability.go) still
	// overrides per operation, held to the same minimum windows as the core doc.
	applyStabilityTier(paths, StabilityBeta)
	applyOperationDescriptions(paths)

	return oaObj(
		"openapi", "3.1.0",
		"info", oaObj(
			"title", "Olivares AI control plane API — module routes (beta)",
			"version", "v1",
			"description", moduleDocDescription,
			"license", oaObj("name", "AGPL-3.0-only"),
			"x-stability-policy", stabilityPolicyURL,
			"x-beta-notice", moduleBetaNotice,
		),
		"servers", []any{oaObj("url", "/", "description", "this engine")},
		"security", oaBearer(),
		"components", oaObj(
			"securitySchemes", oaObj("bearerAuth", oaObj(
				"type", "http", "scheme", "bearer",
				"description", "Opaque session (olvs_) or API (olvk_) token.")),
			"schemas", oaObj(
				"Error", oaObj("type", "object", "properties", oaObj("error", oaObj("type", "object",
					"properties", oaObj("code", oaObj("type", "string"), "message", oaObj("type", "string"))))),
			)),
		"paths", paths,
	)
}

// ModuleOpenAPIDocument returns the BETA OpenAPI 3.1 document for the module routes
// of modules — the exact document served at GET /openapi.beta.json and dumped by
// `olivares openapi --beta` for the committed snapshot. It is built from the routes
// the modules register (collectModuleRoutes), so it never drifts from what the
// engine mounts, and it needs no running server (no store, no handler is invoked).
func ModuleOpenAPIDocument(modules []Module) map[string]any {
	return buildModuleOpenAPI(collectModuleRoutes(modules))
}

// handleOpenAPIBeta serves the module-route beta document, building it once on the
// first request (betaOnce) from the modules the engine mounted.
func (s *Server) handleOpenAPIBeta(w http.ResponseWriter, _ *http.Request) {
	s.betaOnce.Do(func() { s.openapiBetaDoc = ModuleOpenAPIDocument(s.modules) })
	writeJSON(w, http.StatusOK, s.openapiBetaDoc)
}
