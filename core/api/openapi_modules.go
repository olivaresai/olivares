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
	// governed says the route came through HandlePolicy/HandleSealed. It is RECORDED and not
	// derived: a governed route may carry zero metadata, so deriving it would make emptying a
	// route's policy a silent downgrade.
	governed bool
	// meta is the policy the governed door was given.
	//
	// ⛔ SE GUARDA ENTERA, Y ANTES SE TIRABA. El registrador conservaba el BIT de gobernanza y
	// descartaba la metadata, así que el inventario sabía que una ruta era gobernada y no CON QUÉ:
	// ni su acción, ni su suelo de AAL, ni si exige grant. Con eso, ni el documento publicado puede
	// describir la superficie que se monta, ni el arranque puede comprobar que la acción sea del
	// módulo — que es justo la comprobación que este replay hace ahora.
	meta RouteMetadata
	// cedarAction is meta's action, kept flat because that is what the boot check compares.
	cedarAction string
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

// HandleNoStore and HandleEntityNoStore record a route that declared response
// headers EXACTLY as their plain counterparts do, because the declaration changes
// what the server SENDS, not which routes exist or what they accept. Their
// presence is what the registrar's own rule demands: an optional capability found
// by assertion puts the burden on every registrar that answers, and a registrar
// that stayed silent would drop these routes from the published document.
//
// The header itself is published by the operation's response contract
// (openapi_sessions_communication_contracts.go), where it belongs: this registrar
// records registrations, not response shapes.
func (r recordingRegistrar) HandleNoStore(
	method, pattern string, perm auth.Permission, h ModuleHandler,
) {
	r.Handle(method, pattern, perm, h)
}

func (r recordingRegistrar) HandleEntityNoStore(
	method, pattern string, perm auth.Permission, ref EntityRef, h ModuleHandler,
) {
	r.HandleEntity(method, pattern, perm, ref, h)
}

// HandlePolicy records a governed route, and its ABSENCE was a defect with two faces.
//
// ⛔ THE GOVERNED DOOR IS AN OPTIONAL CAPABILITY FOUND BY TYPE ASSERTION, and this registrar did
// not carry it. A module that asks for it here either panics mid-inventory — measured: the first
// module in the tree to use the governed door panicked this registrar in
// checkRoutePermsDeclared — or, obeying HandlePolicy's own rule that a module MUST NOT fall back
// to Handle for a governed route, withholds the route entirely. Either way the published document
// describes a surface that is not the mounted one, and it does so SILENTLY.
//
// ⇒ THE GENERAL SHAPE, worth more than this fix: AN OPTIONAL CAPABILITY FOUND BY ASSERTION PUTS
// THE BURDEN ON EVERY REGISTRAR THAT ANSWERS. The pattern is right for letting a MODULE ask; each
// registrar must then implement it or the routes that use it vanish from whatever it exists for.
func (r recordingRegistrar) HandlePolicy(
	method, pattern string, perm auth.Permission, meta RouteMetadata, _ ModuleHandler,
) {
	*r.out = append(*r.out, moduleRoute{
		ns: r.ns, method: strings.ToUpper(method), pattern: pattern, perm: perm,
		governed: true, meta: meta, cedarAction: meta.CedarAction,
	})
}

// HandleSealed records a sealed route, and its absence would have been the same defect one door
// along: a module that reaches for the sealed door on this registrar finds nothing, and either
// panics mid-inventory or — obeying the rule that it must not fall back — withholds the route.
// Every registrar implements every door, or the routes that use the missing one vanish from
// whatever that registrar exists for.
func (r recordingRegistrar) HandleSealed(
	method, pattern string, perm auth.Permission, sealed SealedRoute, h ModuleHandler,
) {
	r.HandlePolicy(method, pattern, perm, sealed.Metadata(), h)
}

// WithCollectionScope records nothing new and returns the same recorder, because the
// declaration changes what the server DECIDES — which workspace the authorization
// resource carries — and not which routes exist, what they accept or what they return.
// The published document is unchanged by it.
//
// ⛔ IT IS IMPLEMENTED HERE FOR THE REASON WRITTEN ABOVE HandlePolicy, AND THE FIRST RUN
// OF THE PROBE MODULE PROVED IT AGAIN: an optional capability found by type assertion
// puts the burden on EVERY registrar that answers. A module that asks this recorder for
// the capability and finds it absent either panics mid-inventory or withholds the route,
// and then the published document describes a surface that is not the mounted one. In
// tree, modules/sessions falls back safely (it loses a scoped grant, never a guard); a
// module that chose to panic instead found this door missing, which is how it was caught.
func (r recordingRegistrar) WithCollectionScope(CollectionScopeRef) RouteRegistrar {
	return r
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
// "stream" or "attach") and the TWO always-CSV FinOps exports. Every other module
// route — including the format-switched routes that DEFAULT to JSON (compliance
// evidence export, /dora and the model-card render: ?format=csv|oscal|md is
// opt-in) — returns the generic JSON envelope.
//
// ⛔ THE CSV EXPORTS ARE MATCHED BY EXACT TUPLE — namespace, method AND whole
// pattern — NEVER by the trailing "export" segment. Fifteen module routes end in
// "export" and only these two are always CSV; the rest answer the JSON envelope
// unless a ?format query asks otherwise. A suffix rule would fix this defect by
// committing it thirteen more times, in the opposite direction, and the generated
// clients would decode CSV where the server sends JSON.
// openapi_finops_contracts_test.go TestModuleRouteRawContentTypeIsExactNotBySuffix
// pins both halves.
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
	if r.ns == "finops" && r.method == http.MethodGet && r.pattern == "/statements/{id}/export" {
		// modules/finops/statements.go handleExportStatement: the ONLY success path
		// sets "text/csv; charset=utf-8" and writes rows with encoding/csv. The
		// charset parameter belongs to the HTTP header; "text/csv" is the media-type
		// key the document publishes. Every error branch stays writeJSON.
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
	if sessionsLaunchReadinessRoute(r) {
		// A typed contract, not the generic envelope: this route's whole value is
		// that a console can render causes and pending checks without re-deriving
		// the server's state machine, and it can only do that if the states, codes
		// and remediations are published as closed enums.
		return sessionsLaunchReadinessResponses()
	}
	if sessionsHostToolsRoute(r) {
		// Same reason as the readiness sibling above: the value of this read is its
		// closed state and group vocabulary, and the generic envelope publishes none
		// of it. It declares NO query parameters, so it adds no branch to
		// moduleRouteParameters - the ordinary path ref from the module registration
		// is the whole parameter set, and a non-empty query string is a 400.
		return sessionsHostToolsResponses()
	}
	if responses, ok := sessionsCommunicationResponses(r); ok {
		return responses
	}
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
	// ⛔ THE THREE RUN CONTROLS ARE PUBLISHED LAST, AND THEY OVERRIDE.
	//
	// The generic set above assumes every module operation succeeds with 200 and
	// answers with an unspecified object. For input/interrupt/stop both halves were
	// measurably false, and an independent review read the published document
	// against the handlers to prove it: input answers 202 on BOTH of its success
	// paths while the contract advertised only 200, and all three can answer 503
	// with a WorkError UNKNOWN — reproduced over real HTTP, SQLite and an owned
	// child by removing one member of the four-column K2 authority stamp — while
	// none of them published 503, because moduleResponses only adds it for the
	// routes sessionsWorkRoute recognizes and that list is the work PLANE, not
	// /runs/*.
	//
	// A drift-clean generator cannot notice either: openapi:check and sdk:check
	// compare the artifact to the generator, and both agreed on the same wrong
	// answer. What notices is a test that drives the real handler and compares what
	// came back to what this function published — see the sessions module's
	// run-control contract test.
	if sessionsRunControlRoute(r) {
		resp["503"] = oaWorkUnknownResp()
		switch r.pattern {
		case "/runs/{ref}/input":
			// 202, and the 200 is DELETED rather than kept beside it: leaving both
			// would publish a success this handler cannot produce, which is the same
			// defect one size smaller.
			delete(resp, "200")
			resp["202"] = oaObj(
				"description", "the input was accepted for the owned child; delivery is not confirmed by this response",
				"content", oaObj("application/json", oaObj("schema", oaRunInputAcceptedSchema())),
			)
		case "/runs/{ref}/interrupt", "/runs/{ref}/stop":
			resp["200"] = oaObj(
				"description", "the run resource after the control was applied",
				"content", oaObj("application/json", oaObj("schema", oaRunResourceSchema())),
			)
		}
	}
	if finopsEvidenceReadRoute(r) {
		resp["200"] = finopsEvidenceResponse(r)
		resp["500"] = oaJSONResp("store or evaluation failure")
	}
	if inventoryObservationHistoryRoute(r) {
		resp["200"] = inventoryObservationPageResponse()
		resp["500"] = oaJSONResp("stored observation evidence failed an integrity invariant, or the member repository cannot project distinct receipts; the body is the generic error and the reason goes to the operator log, never to the client")
	}
	if inferenceProxyContentFirewallRoute(r) {
		resp["200"] = inferenceProxyContentFirewallResponse()
	}
	return resp
}

// sessionsRunControlRoute is the three OPERATE controls that reach an owned child
// and can therefore answer with the fenced work plane's uncertainty verdict. It is
// deliberately an explicit list rather than a prefix over /runs: the read routes,
// attach, resume, cleanup and delete do not share this response contract.
func sessionsRunControlRoute(r moduleRoute) bool {
	if r.ns != "sessions" || r.method != http.MethodPost {
		return false
	}
	switch r.pattern {
	case "/runs/{ref}/input", "/runs/{ref}/interrupt", "/runs/{ref}/stop":
		return true
	}
	return false
}

// oaRunInputAcceptedSchema is the exact body POST /runs/{ref}/input returns on
// success. It is closed because the handler writes this one key and nothing else.
func oaRunInputAcceptedSchema() map[string]any {
	return oaObj(
		"type", "object", "additionalProperties", false,
		"required", oaEnum("accepted"),
		"properties", oaObj("accepted", oaObj(
			"type", "boolean",
			"description", "Always true; a non-2xx status is the only way this operation reports refusal.",
		)),
	)
}

// oaRunResourceSchema is the run interrupt and stop answer with. It names the two
// fields a caller must be able to rely on and stays OPEN on purpose: the run
// resource carries further non-sensitive lifecycle facts, and freezing today's
// field list here would publish a contract that is wrong the next time one is
// added — the failure mode this whole section exists to remove.
func oaRunResourceSchema() map[string]any {
	return oaObj(
		"type", "object", "additionalProperties", true,
		"required", oaEnum("run_ref", "state"),
		"properties", oaObj(
			"run_ref", oaObj("type", "string", "description", "The operated session's stable reference."),
			"state", oaObj("type", "string", "description", "The run's derived lifecycle state after the control."),
		),
	)
}

// oaWorkUnknownResp is the fenced control plane's third answer, published with
// its real shape rather than as an unspecified object: the effect may or may not
// have crossed to the child, and a caller that cannot tell that apart from a
// refusal has been told nothing useful.
func oaWorkUnknownResp() map[string]any {
	envelope := oaObj(
		"type", "object", "additionalProperties", true,
		"required", oaEnum("verdict", "code", "error"),
		"properties", oaObj(
			"verdict", oaObj("type", "string", "enum", oaEnum("NO_HE_PODIDO_MIRAR"),
				"description", "The uncertain verdict: the outcome could not be observed."),
			"code", oaObj("type", "string",
				"description", "The durable outcome name, e.g. work_input_ambiguous or evidence_unavailable."),
			"error", oaObj(
				"type", "object", "additionalProperties", true,
				"required", oaEnum("code", "message"),
				"properties", oaObj(
					"code", oaObj("type", "string"),
					"message", oaObj("type", "string"),
				),
			),
			"evidence_ref", oaObj("type", "string",
				"description", "Present when the refusal names the field or evidence it could not resolve."),
		),
	)
	return oaObj(
		"description", "the outcome is UNKNOWN: required authority, evidence or store could not be observed, and any external effect may or may not have crossed",
		"content", oaObj("application/json", oaObj("schema", envelope)),
	)
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
	if sessionsCommunicationRoute(r) {
		o["x-olivares-sdk-family"] = sessionsCommunicationSDKFamily
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
		description := "Path parameter " + p + "."
		if p == "id" && sessionsProtocolBindingRoute(r) {
			schema = oaProtocolBindingIDSchema()
		}
		if p == "id" && inventoryObservationHistoryRoute(r) {
			schema = oaInventoryCanonicalIDSchema("Canonical lowercase nonzero UUID.")
			description = "The catalog entity's core id as a canonical lowercase nonzero UUID; any other spelling is 400 before a row is read. The exact (kind, id) pair must exist in the tenant's catalog, or the answer is 404."
		}
		params = append(params, oaObj("name", p, "in", "path", "required", true,
			"description", description, "schema", schema))
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
	if inventoryObservationHistoryRoute(r) {
		return inventoryObservationParameters()
	}
	if finopsEvidenceReadRoute(r) && r.pattern == "/alerts" {
		return finopsAlertParameters()
	}
	if sessionsLaunchReadinessRoute(r) {
		return sessionsLaunchReadinessParameters()
	}
	if params, ok := sessionsCommunicationParameters(r); ok {
		return params
	}
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

// --- inventory: the entity observation history ---------------------------------
//
// GET /v1/m/inventory/entities/{kind}/{id}/observations is the first inventory
// route published with a CLOSED contract instead of the generic envelope. The
// handler (modules/inventory/api.go handleListEntityObservations) and its reader
// (modules/inventory/provenance_read.go) already enforce every shape below; what
// was missing was the document saying so, and an independent review measured the
// cost: the beta document carried the route with no limit or cursor parameter, a
// 200 of {type: object} and no 500, so the generated web client typed the query
// as `never` and the page as `Record<string, never>`. A doc comment on the
// handler is not a contract a generator can consume.
//
// The bounds here (1..25, default 25) mirror observationPageDefault and
// observationPageMax in the inventory reader, which this package cannot import
// (the dependency runs the other way). openapi_inventory_contracts_test.go pins
// them: a change to the reader's bound is a change here, in the same commit.

// inventoryObservationHistoryRoute recognizes EXACTLY the observation history
// route of the inventory module — namespace, method and the whole pattern, never
// a prefix. The catalog list (/entities), the detail route (/entities/{kind}/{id})
// and any other module's route of the same shape keep the generic envelope.
func inventoryObservationHistoryRoute(r moduleRoute) bool {
	return r.ns == "inventory" && r.method == http.MethodGet &&
		r.pattern == "/entities/{kind}/{id}/observations"
}

// inventoryCanonicalIDPattern is the exact text the inventory reader accepts for
// an entity id, a receipt id and the page cursor: the lowercase hyphenated form
// the store writes (model.ID.String). Braces, upper case, the URN prefix and the
// 32-hex form parse elsewhere but are refused there, as request data (400) and
// as stored data (evidence corruption). The all-zero value matches this pattern
// and is refused too: OpenAPI has no exclusion keyword every generator honors,
// so that half is stated in the descriptions.
const inventoryCanonicalIDPattern = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"

func oaInventoryCanonicalIDSchema(description string) map[string]any {
	return oaObj("type", "string", "format", "uuid", "pattern", inventoryCanonicalIDPattern,
		"description", description)
}

// inventoryObservationParameters is the page request the handler parses
// (parseObservationQuery): each parameter at most once, limit in canonical decimal
// within 1..25 (25 when absent), cursor a canonical nonzero receipt id. OpenAPI
// cannot say "at most once" about a query parameter, so the 400 for a repeated
// value is stated in the descriptions rather than left to discovery.
func inventoryObservationParameters() []any {
	return []any{
		oaParam("limit", "query",
			"Page size in distinct receipts, 1 through 25; 25 when absent. Canonical decimal only and at most once: a repeated, empty, non-decimal or out-of-range value is 400.",
			false, oaObj("type", "integer", "minimum", 1, "maximum", 25, "default", 25)),
		oaParam("cursor", "query",
			"Exclusive keyset anchor, at most once: the cursor returned with the previous page, which is the receipt id of its last item; the page starts strictly after it in ascending receipt-id order. A canonical lowercase nonzero UUID; any other form, an empty value or a repeated cursor is 400.",
			false, oaInventoryCanonicalIDSchema("Canonical lowercase nonzero receipt id.")),
	}
}

// inventoryObservationPageResponse is the 200 of the observation history: the
// engine-wide list envelope (listresponse.go) carrying this route's item schema,
// closed at every level. cursor is present only when has_more is true.
func inventoryObservationPageResponse() map[string]any {
	schema := oaObj(
		"type", "object", "additionalProperties", false,
		"required", oaEnum("items", "has_more"),
		"properties", oaObj(
			"items", oaObj("type", "array", "maxItems", 25, "items", inventoryObservationItemSchema(),
				"description", "One item per distinct receipt that names the entity, ascending by receipt id. Empty when the tenant stores no receipt for the entity, which does not prove it was never observed."),
			"cursor", oaInventoryCanonicalIDSchema("The receipt id of the last item, present only when has_more is true; pass it as ?cursor to continue strictly after it."),
			"has_more", oaObj("type", "boolean",
				"description", "True when at least one more distinct matching receipt id exists past this page. It certifies nothing about that receipt's integrity: the page that composes it decides, and a lookahead is not a certificate."),
		),
	)
	return oaObj(
		"description", "One page of the entity's observation history. Every receipt on the page was validated against the writer's own encoding before any item was published: the page is whole, or it is refused with 500.",
		"content", oaObj("application/json", oaObj("schema", schema)),
	)
}

// inventoryObservationItemSchema is observationDTO (modules/inventory/dto.go)
// field by field: a closed allowlist, so nothing reaches the wire that is not
// named here. Absent on purpose — and asserted absent by the contract test — are
// the event id, receipt key, facts hash and raw facts, the member's name,
// reference, signal, host and native reference, the source label and binding
// reference, the edge and cost payloads, and anything about the source's current
// roster state, health, owner, grants or coverage.
func inventoryObservationItemSchema() map[string]any {
	instant := func(description string) map[string]any {
		return oaObj("type", "string", "format", "date-time", "description", description)
	}
	return oaObj(
		"type", "object", "additionalProperties", false,
		"required", oaEnum("receipt_id", "event_type", "registration", "first_received_at", "last_received_at", "deliveries", "conflicting_redelivery"),
		"properties", oaObj(
			"receipt_id", oaInventoryCanonicalIDSchema("The platform receipt id. One item per receipt: a receipt whose members name this entity twice is still one item."),
			"event_type", oaObj("type", "string", "enum", oaEnum("edge.observed", "cost.sampled"),
				"description", "The first-party observation type the receipt recorded."),
			"registration", inventoryObservationRegistrationSchema(),
			"source_occurred_at", instant("The instant the SOURCE declared for the fact, taken from the validated member that names this entity; the same claim that feeds the catalog entry's occurred_at. Omitted when the source declared none."),
			"first_received_at", instant("This platform's reception clock for the original delivery of the retained facts."),
			"last_received_at", instant("This platform's reception clock for the last delivery that carried facts equal to the retained ones. A conflicting redelivery has its own counters and is not folded in."),
			"deliveries", oaObj("type", "integer", "format", "int64", "minimum", 1,
				"description", "Deliveries of the receipt with equal facts: the original plus exact replays. Not distinct activity, and conflicting variants are excluded."),
			"conflicting_redelivery", oaObj("type", "boolean",
				"description", "Whether at least one redelivery of this receipt carried DIFFERENT facts and was retained separately. A boolean by design: the conflicting facts, their count and their times are not published."),
		),
	)
}

// inventoryObservationRegistrationSchema is observationRegistrationDTO as the
// three shapes it actually takes, each closed: the components exist ONLY for
// registered_snapshot, where the recorded snapshot was complete. An incomplete
// snapshot is published as invalid with no components (a partial identity is
// never presented as one), and unattributed records that none was stamped.
func inventoryObservationRegistrationSchema() map[string]any {
	state := func(value, description string) map[string]any {
		return oaObj("type", "string", "const", value, "description", description)
	}
	registered := oaObj(
		"type", "object", "additionalProperties", false,
		"required", oaEnum("registration_state", "source_id", "source_revision", "environment_ref"),
		"properties", oaObj(
			"registration_state", state("registered_snapshot", "The receipt recorded a complete registration snapshot when it was received."),
			"source_id", oaObj("type", "string", "minLength", 1,
				"description", "Persistent id of the roster row the snapshot named. A historical identifier, not a grant to read that row."),
			"source_revision", oaObj("type", "integer", "format", "int64", "minimum", 1,
				"description", "The roster revision the snapshot says was applied when the observation was produced; not the roster's current revision."),
			"environment_ref", oaObj("type", "string", "minLength", 1,
				"description", "The persistent local execution environment the snapshot recorded; not a host and not a workspace."),
		),
	)
	unattributed := oaObj(
		"type", "object", "additionalProperties", false,
		"required", oaEnum("registration_state"),
		"properties", oaObj("registration_state", state("unattributed", "No registration snapshot was stamped on the observation.")),
	)
	invalid := oaObj(
		"type", "object", "additionalProperties", false,
		"required", oaEnum("registration_state"),
		"properties", oaObj("registration_state", state("invalid", "A snapshot was stamped but it was incomplete; its partial components are not published.")),
	)
	return oaObj(
		"description", "The registration snapshot copied into the receipt when it was received: what the producer's registration looked like THEN. It is historical, not an attestation that the source is registered, healthy or readable now.",
		"oneOf", []any{registered, unattributed, invalid},
	)
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
				"description", "One run input. line/message are RAW frames for a Claude stream-json child; text is a turn for a session driven by an owned provider protocol, which refuses a raw frame. Send one form, never both. Omit work_lease_fence for legacy non-work runs; a positive value selects fenced WorkItem control and is accepted with either form, so a work-bound driver run is spoken to through the same fence as its other controls.",
				"additionalProperties", false,
				"properties", oaObj(
					"line", oaObj("type", "string", "minLength", 1),
					"message", oaObj("type", "object", "additionalProperties", true),
					"text", oaObj("type", "string", "minLength", 1),
					"work_lease_fence", oaObj("type", "integer", "format", "int64", "minimum", 1),
				),
			)
		case "/runs/{ref}/interrupt":
			required = false
			schema = oaObj(
				"type", "object",
				"description", "Optional fenced turn interruption. An empty body preserves legacy non-work run behavior; a positive work_lease_fence selects the same fenced WorkItem control plane this run's input and stop use. It cancels the active provider turn only: the owned process, its conversation and the run stay usable for the next input, and it never falls back to stop.",
				"additionalProperties", false,
				"properties", oaObj(
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
