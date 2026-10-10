// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/auth"
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
// generic JSON envelope; routes that stream Server-Sent Events (a pattern whose
// last segment is "stream" or "attach") are classified raw here
// (moduleRouteRawContentType) so the SDKs emit a bytes-returning operation
// instead of a JSON decoder that can never succeed.
//
// A module describes its own operations beyond that (request bodies, typed
// responses, parameters, a raw export) by implementing ModuleOperationDocumenter
// in its own package. The core describes no module operation.

const (
	// moduleBetaNotice is the machine-readable banner stamped on info.x-beta-notice.
	moduleBetaNotice = "beta, may change; not covered by the 24-month stable window of the core REST contract"

	// moduleDocDescription is the human banner rendered at the top of the beta
	// reference. It states tenant and deployment scope separately and describes
	// generic JSON bodies unless an operation declares its owned schema.
	moduleDocDescription = "The BETA REST surface of Olivares AI: the module routes under " +
		"/v1/m/<namespace>/ (finops, compliance, governance, identity, sessions, accessmap, " +
		"models, knowledge, …) that make up the product. This is a BETA contract — shapes may " +
		"change with notice and these routes are NOT covered by the 24-month stable window of the " +
		"core contract served at /openapi.json. Tenant-scoped operations authenticate with an opaque " +
		"bearer token (session olvs_… or API key olvk_…), resolving the tenant from a bound token or " +
		"the X-Olivares-Tenant header. Operations marked x-olivares-scope: system require deployment " +
		"authority and ignore caller-selected tenants; their handlers may require a human principal " +
		"and elevated assurance. Each operation also declares " +
		"the permission it requires (x-required-permission). Response bodies are the product's generic " +
		"JSON envelope unless an operation declares otherwise (a few routes stream Server-Sent Events or " +
		"export a non-JSON format). Every mutation declares its handler-reviewed request-body disposition " +
		"in x-olivares-request-body-disposition; bodies that are not ordinary JSON DTOs publish their exact " +
		"media type without inventing fields. " +
		"Ordinary errors use Error: error.code and error.message are required strings; " +
		"message-only beta failures use module_error. Operation-specific result bodies and " +
		"metadata are retained. See info.x-stability-policy for the policy."
)

// moduleRoute is one route a module registered, captured from the RouteRegistrar
// seam (method, the module-relative chi pattern, the permission it requires) plus
// the owning namespace.
type moduleRoute struct {
	system  bool // Deployment scope; no caller-selected tenant.
	ns      string
	method  string
	pattern string // module-relative chi pattern, e.g. "/spend" or "/{id}"
	perm    auth.Permission
	// deniedReadPermission is the entity route's explicit visibility declaration.
	// The mount check requires it to be in the module's permission catalog too.
	deniedReadPermission auth.Permission
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
	meta          RouteMetadata
	documentation *ModuleOperationDocumentation
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
func (r recordingRegistrar) HandleEntity(method, pattern string, perm auth.Permission, ref EntityRef, _ ModuleHandler) {
	*r.out = append(*r.out, moduleRoute{
		ns: r.ns, method: strings.ToUpper(method), pattern: pattern, perm: perm,
		deniedReadPermission: ref.DeniedReadPermission,
	})
}

// HandleNoStore and HandleEntityNoStore record a route that declared response
// headers EXACTLY as their plain counterparts do, because the declaration changes
// what the server SENDS, not which routes exist or what they accept. Their
// presence is what the registrar's own rule demands: an optional capability found
// by assertion puts the burden on every registrar that answers, and a registrar
// that stayed silent would drop these routes from the published document.
//
// The header itself is published by the operation's response contract
// (modules/sessions/openapi_communication.go), where it belongs: this registrar
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
		start := len(routes)
		m.APIRoutes(recordingRegistrar{ns: m.APINamespace(), out: &routes})
		if documenter, ok := m.(ModuleOperationDocumenter); ok {
			for i := start; i < len(routes); i++ {
				if declaration, ok := documenter.OperationDocumentation(routes[i].method, routes[i].pattern); ok {
					routes[i].documentation = &declaration
				}
			}
		}
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
// does NOT return JSON, so the document declares it raw: the SSE streams, every
// route whose last path segment is "stream" or "attach". A module whose other
// routes answer a raw body (an always-CSV export, a stream under another name)
// says so in its own documentation (ModuleOperationDocumenter). Every other
// module route returns the generic JSON envelope.
func moduleRouteRawContentType(r moduleRoute) (string, bool) {
	switch r.pattern[strings.LastIndex(r.pattern, "/")+1:] {
	case "stream", "attach":
		return "text/event-stream", true
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

func oaTenantParam() map[string]any {
	return oas.Obj("name", "X-Olivares-Tenant", "in", "header", "required", false,
		"description", "Target tenant id; required when the principal can act in more than one tenant.",
		"schema", oas.Obj("type", "string", "format", "uuid"))
}

func oaBearer() []any {
	return []any{oas.Obj("bearerAuth", []any{}), oas.Obj("browserSession", []any{})}
}

// moduleResponses preserves each operation's response contract. Ordinary errors
// use components.Error, but non-success statuses can also carry structured results;
// a generic object must not be narrowed to an error solely because of its status.
// Success is JSON by default or the route's raw content type (SSE / export).
func moduleResponses(r moduleRoute) map[string]any {
	if r.documentation != nil && len(r.documentation.SuccessResponses) > 0 {
		responses := oas.Obj("400", oas.JSONResp("bad request"), "401", oas.JSONResp("unauthenticated"), "403", oas.JSONResp("forbidden / step-up required"), "404", oas.JSONResp("not found"), "409", oas.JSONResp("conflict"), "503", oas.JSONResp("required evidence or store unavailable"))
		for status, response := range r.documentation.SuccessResponses {
			responses[status] = response
		}
		return responses
	}
	if r.documentation != nil && len(r.documentation.Responses) > 0 {
		return r.documentation.Responses
	}
	resp := GenericModuleResponses()
	if ct, raw := moduleRouteRawContentType(r); raw {
		resp["200"] = oas.Obj("description", "OK ("+ct+")",
			"content", oas.Obj(ct, oas.Obj("schema", oas.Obj("type", "string"))))
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
	o := oas.Obj(
		"summary", summary,
		"security", oaBearer(),
		"responses", moduleResponses(r),
	)
	doc := r.documentation
	if doc == nil {
		doc = &ModuleOperationDocumentation{}
	}
	if doc.RequiredAssurance > 0 {
		o["x-required-assurance"] = doc.RequiredAssurance
	}
	if r.perm != "" {
		o["x-required-permission"] = string(r.perm)
	}
	if moduleRouteIsMutation(r) {
		o[moduleRequestBodyDispositionExtension] = string(moduleRequestBodyDispositionFor(r))
	}
	if body, ok := moduleRequestBody(r); ok {
		o["requestBody"] = body
	}
	params := []any{}
	for _, parameter := range doc.Parameters {
		params = append(params, parameter)
	}
	if r.system {
		o["x-olivares-scope"] = "system"
	} else {
		params = append(params, oaTenantParam())
	}
	for _, p := range pathParamNames(r.pattern) {
		if parameter, ok := doc.PathParameters[p]; ok {
			params = append(params, parameter)
			continue
		}
		params = append(params, oas.Param(p, "path", "Path parameter "+p+".", true, oas.Obj("type", "string")))
	}
	for _, parameter := range doc.TrailingParameters {
		params = append(params, parameter)
	}
	o["parameters"] = params
	// A module adds x- fields only; it never replaces what the core set, so
	// authorization and scope documentation stay the core's.
	for name, value := range doc.Extensions {
		if _, set := o[name]; !set && strings.HasPrefix(name, "x-") {
			o[name] = value
		}
	}
	return o
}

// moduleRequestBody returns the request body a module documents for its route.
func moduleRequestBody(r moduleRoute) (map[string]any, bool) {
	doc := r.documentation
	if doc == nil || doc.RequestBody == nil {
		return nil, false
	}
	switch doc.BodyKind {
	case ModuleOperationJSONBody, ModuleOperationOpaqueBody:
		return doc.RequestBody, true
	}
	return nil, false
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

	return oas.Obj(
		"openapi", "3.1.0",
		"info", oas.Obj(
			"title", "Olivares AI control plane API — module routes (beta)",
			"version", "v1",
			"description", moduleDocDescription,
			"license", oas.Obj("name", "AGPL-3.0-only"),
			"x-stability-policy", stabilityPolicyURL,
			"x-beta-notice", moduleBetaNotice,
		),
		"servers", []any{oas.Obj("url", "/", "description", "this engine")},
		"security", oaBearer(),
		"components", oas.Obj(
			"securitySchemes", oas.Obj("bearerAuth", oas.Obj(
				"type", "http", "scheme", "bearer",
				"description", "Opaque session (olvs_) or API (olvk_) token.")),
			"schemas", oas.Obj(
				"Error", apiErrorSchema(),
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
func (s *Server) handleOpenAPIBeta(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	writeJSON(w, http.StatusOK, s.betaDocument())
}

// betaDocument is the module-route beta document, built once from the mounted modules.
func (s *Server) betaDocument() map[string]any {
	s.betaOnce.Do(func() { s.openapiBetaDoc = ModuleOpenAPIDocument(s.modules) })
	return s.openapiBetaDoc
}
