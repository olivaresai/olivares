// SPDX-FileCopyrightText: 2026 Olivares AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

// Route publication classification (census of 2026-10-06).
//
// Two OpenAPI documents are the published REST contract (stability.go): the
// stable core document (buildOpenAPI) and the beta module-route document
// (openapi_modules.go, a reflector that needs no per-route work). Every route
// the engine mounts is one of:
//
//   - published in one of the two documents (the Community public API), or
//   - listed here with a class and a reason why it is NOT a published
//     operation of the Olivares REST contract.
//
// The classes are exactly the ones the 2026-10-06 census classified
// (docs/openapi-route-classification.md holds the full census, including the
// Business rows registered in olivares-enterprise, which this repository
// cannot see):
//
//   - protocol: the route implements an external protocol whose own
//     specification is the contract (SCIM 2.0, SSF/CAEP, OAuth 2.0/OIDC/SAML
//     federation, OpenID AuthZEN 1.0, MCP transports). An external conformant
//     client — an IdP, a PEP, an MCP host — is the consumer, not the Olivares
//     SDK clients, and the wire shapes are fixed by the protocol, not chosen
//     here.
//   - internal: an engine mechanism that is not a client contract. (The
//     2026-10-06 census classified no Community route internal; the class
//     exists because "not a public contract" is a legitimate answer a future
//     census must be able to give with a reason, in this table, instead of
//     silently omitting the route.)
//
// cmd/olivares/openapi_route_classification_test.go walks the REAL router and
// fails when a mounted operation is neither published nor listed here, and
// when a listed route stops being mounted (unless optional says the engine
// mounts it only when its service is wired). A route MUST NOT be added to
// this table to avoid publishing it: publication gaps for Community public
// routes are the defect this table exists to expose, not to hide.
type routePublicationClass string

const (
	classProtocol routePublicationClass = "protocol"
	classInternal routePublicationClass = "internal"
)

// routeClassification is one non-published route's standing: the class, the
// reason a reader can check, and whether the engine mounts it only when its
// backing service is wired (the MCP gateway routes exist only when the
// gateway runtime is).
type routeClassification struct {
	class    routePublicationClass
	reason   string
	optional bool
}

// RouteClassification is the exported read a test or tool uses to ask how a
// route stands. Empty class means the route is not in the table (the census
// answer for a Community public route: it belongs in a published document).
type RouteClassification struct {
	Class    routePublicationClass
	Reason   string
	Optional bool
}

// ClassifiedRoutes returns the whole table, keyed as routeClassifications is.
// The classification test walks it so a row whose route is gone is red.
func ClassifiedRoutes() map[string]RouteClassification {
	out := make(map[string]RouteClassification, len(routeClassifications))
	for k, c := range routeClassifications {
		out[k] = RouteClassification{Class: c.class, Reason: c.reason, Optional: c.optional}
	}
	return out
}

// routeClassifications is keyed by "<METHOD> <path>" in the OpenAPI spelling
// of the route (no trailing slash; named path parameters in braces). The
// classification test normalises both this table's keys and the router's
// routes to the same shape before comparing, so parameter NAMES may differ
// between the router and the key but the route may not.
var routeClassifications = map[string]routeClassification{
	// ── SCIM 2.0 (RFC 7644) ────────────────────────────────────────────────
	"GET /v1/scim/v2/Users":                 scimClass,
	"POST /v1/scim/v2/Users":                scimClass,
	"GET /v1/scim/v2/Users/{id}":            scimClass,
	"PUT /v1/scim/v2/Users/{id}":            scimClass,
	"PATCH /v1/scim/v2/Users/{id}":          scimClass,
	"DELETE /v1/scim/v2/Users/{id}":         scimClass,
	"GET /v1/scim/v2/Groups":                scimClass,
	"POST /v1/scim/v2/Groups":               scimClass,
	"GET /v1/scim/v2/Groups/{id}":           scimClass,
	"PUT /v1/scim/v2/Groups/{id}":           scimClass,
	"PATCH /v1/scim/v2/Groups/{id}":         scimClass,
	"DELETE /v1/scim/v2/Groups/{id}":        scimClass,
	"GET /v1/scim/v2/ServiceProviderConfig": scimClass,
	"GET /v1/scim/v2/ResourceTypes":         scimClass,
	"GET /v1/scim/v2/ResourceTypes/{type}":  scimClass,
	"GET /v1/scim/v2/Schemas":               scimClass,
	"GET /v1/scim/v2/Schemas/{urn}":         scimClass,
	"POST /v1/scim/v2/Events":               scimClass,

	// ── Shared Signals Framework (SSF/CAEP) ────────────────────────────────
	"POST /v1/ssf/events": {class: classProtocol,
		reason: "SSF/CAEP event receiver: the Shared Signals Framework stream an identity provider pushes to; the framework's event formats are the contract."},

	// ── OpenID AuthZEN 1.0 Authorization API ───────────────────────────────
	"GET /.well-known/authzen-configuration": authzenClass,
	"POST /access/v1/evaluation":             authzenClass,
	"POST /access/v1/evaluations":            authzenClass,
	"POST /access/v1/search/subject":         authzenClass,
	"POST /access/v1/search/resource":        authzenClass,
	"POST /access/v1/search/action":          authzenClass,
	"POST /access/v1/access-review/export":   authzenClass,

	// ── OAuth 2.0 / OIDC / SAML federation ─────────────────────────────────
	"GET /.well-known/oauth-authorization-server": {class: classProtocol,
		reason: "OIDC Authorization Server Metadata (RFC 8414) discovery document; published at the spec's well-known path for OAuth/MCP clients."},
	"POST /v1/auth/token": {class: classProtocol,
		reason: "OAuth 2.0 token endpoint serving the RFC 7523 JWT-bearer grant; the grant's wire format is the contract."},
	"POST /v1/auth/token-exchange": {class: classProtocol,
		reason: "OAuth 2.0 Token Exchange (RFC 8693); the RFC's request and token response formats are the contract."},
	"GET /v1/auth/federation/start": {class: classProtocol,
		reason: "Browser SSO redirect flow: hands the browser to the tenant's IdP (SAML/OIDC); consumed by a browser following a redirect, not by an API client."},
	"GET /v1/auth/federation/saml/metadata": {class: classProtocol,
		reason: "SAML 2.0 metadata document for the engine's service-provider role; the SAML metadata schema is the contract."},
	"GET /v1/auth/federation/callback":  federationCallbackClass,
	"POST /v1/auth/federation/callback": federationCallbackClass,

	// ── MCP transports (mounted only when the gateway runtime is wired) ────
	"* /session/mcp": {class: classProtocol, optional: true,
		reason: "MCP streamable-HTTP session transport; the MCP wire protocol is the contract, negotiated by MCP clients."},
	"* /mcp/gateway/{tenant}/{id}": {class: classProtocol, optional: true,
		reason: "MCP streamable-HTTP gateway transport for one tenant-scoped gateway server; the MCP wire protocol is the contract."},
	"* /.well-known/oauth-protected-resource/mcp/gateway/{tenant}/{id}": {class: classProtocol, optional: true,
		reason: "OAuth 2.0 Protected Resource Metadata (RFC 9728) for the MCP gateway; the RFC's metadata document is the contract."},

	// ── Session browser preview (mounted only when the sessions module serves it) ──
	"* /session/preview/*": {class: classInternal, optional: true,
		reason: "Browser-only passthrough to the app a live session serves on a loopback port, authorized by the token a published POST /v1/m/sessions/runs/{ref}/preview returns; the session's own app defines what it answers, not an SDK client."},
}

var scimClass = routeClassification{class: classProtocol,
	reason: "SCIM 2.0 provisioning protocol (RFC 7644): users, groups, discovery and event push for the tenant's identity provider; the RFC's schemas are the contract, consumed by the IdP rather than Olivares SDK clients."}

var authzenClass = routeClassification{class: classProtocol,
	reason: "OpenID AuthZEN 1.0 Authorization API at the spec's conventional paths; server.go mounts it for an external PEP and records that it is not part of the SDK/OpenAPI surface."}

var federationCallbackClass = routeClassification{class: classProtocol,
	reason: "SSO federation callback: the IdP-redirected browser lands here with protocol-specific parameters (SAML POST response or OIDC code); the federation protocol's callback contract, not a client API."}
