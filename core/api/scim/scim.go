// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package scim implements the protocol layer of an inbound SCIM 2.0 service
// provider (RFC 7643 core schema, RFC 7644 protocol): the resource codecs, the
// filter and PATCH parsers, the ListResponse/Error envelopes, and the discovery
// documents (ServiceProviderConfig / ResourceTypes / Schemas). It is pure wire
// logic — it imports only core/model and the standard library, never the store —
// so the credential-touching half lives in core/auth (Authenticator.SCIM*) and
// the HTTP glue in core/api. It speaks application/scim+json.
package scim

import "strings"

// Resource and message schema URNs (RFC 7643 §A, RFC 7644 §3.x).
const (
	// SchemaUser is the core User resource schema.
	SchemaUser = "urn:ietf:params:scim:schemas:core:2.0:User"
	// SchemaGroup is the core Group resource schema.
	SchemaGroup = "urn:ietf:params:scim:schemas:core:2.0:Group"
	// SchemaServiceProviderConfig is the ServiceProviderConfig schema.
	SchemaServiceProviderConfig = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	// SchemaResourceType is the ResourceType schema.
	SchemaResourceType = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	// SchemaSchema is the Schema schema.
	SchemaSchema = "urn:ietf:params:scim:schemas:core:2.0:Schema"
	// SchemaEnterpriseUser is the enterprise User extension IdPs commonly send.
	SchemaEnterpriseUser = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"

	// SchemaAgentExtension is the SCIM schema extension for agent identity
	// attributes. DRAFT — tracks draft-abbey-scim-agent-extension-00
	// (early WG). Defensive/opt-in: served in /Schemas but never mandatory,
	// never wired to enforcement. If the IdP does not send it, the user is
	// provisioned normally. Design-toward, no conformance claim (docs/SECURITY-HARDENING.md).
	SchemaAgentExtension = "urn:ietf:params:scim:schemas:extension:agent:2.0:User"

	// MsgListResponse is the query/list response message schema.
	MsgListResponse = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	// MsgError is the error response message schema.
	MsgError = "urn:ietf:params:scim:api:messages:2.0:Error"
	// MsgPatchOp is the PATCH request message schema.
	MsgPatchOp = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// ContentType is the SCIM media type (RFC 7644 §3.1).
const ContentType = "application/scim+json"

// MaxPageSize bounds a single SCIM page (RFC 7644 §3.4.2.4 lets the provider cap
// the requested count).
const MaxPageSize = 200

// ServiceProviderConfig returns the /ServiceProviderConfig document advertising
// what this provider supports: PATCH and filtering yes; bulk, sort, etag,
// changePassword and password are not part of the inbound-provisioning contract.
func ServiceProviderConfig(location string) map[string]any {
	return map[string]any{
		"schemas":          []string{SchemaServiceProviderConfig},
		"documentationUri": "",
		"patch":            map[string]any{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": MaxPageSize},
		"changePassword":   map[string]any{"supported": false},
		"sort":             map[string]any{"supported": false},
		"etag":             map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type":        "oauthbearertoken",
			"name":        "OAuth Bearer Token",
			"description": "Authentication via a tenant-bound Olivares API token (Authorization: Bearer olvk_…).",
			"primary":     true,
		}},
		"meta": map[string]any{"resourceType": "ServiceProviderConfig", "location": location},
	}
}

// ResourceTypes returns the /ResourceTypes document: User and (since)
// Group are both provisionable inbound.
func ResourceTypes(baseURL string) []map[string]any {
	return []map[string]any{userResourceType(baseURL), groupResourceType(baseURL)}
}

// ResourceTypeByID returns the single ResourceType document for id (User/Group,
// case-insensitive) and whether it exists — so the handler is not coupled to the
// slice order.
func ResourceTypeByID(baseURL, id string) (map[string]any, bool) {
	for _, rt := range ResourceTypes(baseURL) {
		if name, _ := rt["id"].(string); strings.EqualFold(name, id) {
			return rt, true
		}
	}
	return nil, false
}

func userResourceType(baseURL string) map[string]any {
	return map[string]any{
		"schemas":     []string{SchemaResourceType},
		"id":          "User",
		"name":        "User",
		"endpoint":    "/Users",
		"description": "User Account",
		"schema":      SchemaUser,
		"schemaExtensions": []map[string]any{
			{"schema": SchemaEnterpriseUser, "required": false},
			{"schema": SchemaAgentExtension, "required": false},
		},
		"meta": map[string]any{"resourceType": "ResourceType", "location": baseURL + "/ResourceTypes/User"},
	}
}

func groupResourceType(baseURL string) map[string]any {
	return map[string]any{
		"schemas":     []string{SchemaResourceType},
		"id":          "Group",
		"name":        "Group",
		"endpoint":    "/Groups",
		"description": "Group",
		"schema":      SchemaGroup,
		"meta":        map[string]any{"resourceType": "ResourceType", "location": baseURL + "/ResourceTypes/Group"},
	}
}

// Schemas returns the /Schemas document. It lists the schemas this provider
// actually supports (User + the enterprise extension + the agent extension, and
// Group), not a universal superset — every attribute declared here is one the
// provider honors (parsed when present). The agent extension is declared
// design-toward: served so an IdP that manages agent identities can discover it,
// but never over-promises storage or enforcement (defensive/opt-in, docs/SECURITY-HARDENING.md).
func Schemas(baseURL string) []map[string]any {
	return []map[string]any{
		userSchema(baseURL),
		enterpriseUserSchema(baseURL),
		agentExtensionSchema(baseURL),
		groupSchema(baseURL),
	}
}

// SchemaByID returns the single Schema document for the given schema URN and
// whether it is one this provider declares — so the handler is not coupled to the
// slice order.
func SchemaByID(baseURL, urn string) (map[string]any, bool) {
	for _, s := range Schemas(baseURL) {
		if id, _ := s["id"].(string); id == urn {
			return s, true
		}
	}
	return nil, false
}

// schemaAttr builds one attribute definition with the RFC 7643 §7 metadata an IdP
// (Microsoft Entra / Okta) reads to drive its attribute mappings. Defaults are
// caseExact:false, mutability:readWrite, returned:default, uniqueness:none; a
// caller overrides any of them on the returned map.
func schemaAttr(name, typ string, multi, required bool) map[string]any {
	return map[string]any{
		"name": name, "type": typ, "multiValued": multi, "required": required,
		"caseExact": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none",
	}
}

func userSchema(baseURL string) map[string]any {
	userName := schemaAttr("userName", "string", false, true)
	userName["uniqueness"] = "server"
	// emails is complex+multiValued. The provider keeps a SINGLE work address tied to
	// userName: emails.value round-trips (it seeds userName on input and is returned on
	// output), but type and primary are SERVER-DETERMINED (always "work"/true), so they
	// are declared readOnly — declaring them readWrite would over-promise a write the
	// provider does not honor (declared==honored).
	emailType := schemaAttr("type", "string", false, false)
	emailType["mutability"] = "readOnly"
	emailPrimary := schemaAttr("primary", "boolean", false, false)
	emailPrimary["mutability"] = "readOnly"
	emails := schemaAttr("emails", "complex", true, false)
	emails["subAttributes"] = []map[string]any{
		schemaAttr("value", "string", false, false),
		emailType,
		emailPrimary,
	}
	return map[string]any{
		"schemas":     []string{SchemaSchema},
		"id":          SchemaUser,
		"name":        "User",
		"description": "User Account",
		"attributes": []map[string]any{
			userName,
			emails,
			schemaAttr("displayName", "string", false, false),
			schemaAttr("active", "boolean", false, false),
			schemaAttr("externalId", "string", false, false),
		},
		"meta": map[string]any{"resourceType": "Schema", "location": baseURL + "/Schemas/" + SchemaUser},
	}
}

// enterpriseUserSchema declares the enterprise User extension (RFC 7643 §4.3). It
// declares ONLY the attributes the provider stores write-through (employeeNumber,
// department, manager) — not the full RFC superset (costCenter/organization/
// division/…), so a declared attribute is always an honored one. manager is the
// RFC complex attribute: value is writable, $ref/displayName are server-side
// (readOnly).
func enterpriseUserSchema(baseURL string) map[string]any {
	managerRef := schemaAttr("$ref", "reference", false, false)
	managerRef["referenceTypes"] = []string{"User"}
	managerRef["mutability"] = "readOnly"
	managerDisplay := schemaAttr("displayName", "string", false, false)
	managerDisplay["mutability"] = "readOnly"
	manager := schemaAttr("manager", "complex", false, false)
	manager["subAttributes"] = []map[string]any{
		schemaAttr("value", "string", false, false),
		managerRef,
		managerDisplay,
	}
	return map[string]any{
		"schemas":     []string{SchemaSchema},
		"id":          SchemaEnterpriseUser,
		"name":        "EnterpriseUser",
		"description": "Enterprise User",
		"attributes": []map[string]any{
			schemaAttr("employeeNumber", "string", false, false),
			schemaAttr("department", "string", false, false),
			manager,
		},
		"meta": map[string]any{"resourceType": "Schema", "location": baseURL + "/Schemas/" + SchemaEnterpriseUser},
	}
}

func groupSchema(baseURL string) map[string]any {
	members := schemaAttr("members", "complex", true, false)
	members["subAttributes"] = []map[string]any{
		schemaAttr("value", "string", false, false),
		schemaAttr("display", "string", false, false),
		schemaAttr("type", "string", false, false),
	}
	return map[string]any{
		"schemas":     []string{SchemaSchema},
		"id":          SchemaGroup,
		"name":        "Group",
		"description": "Group",
		"attributes": []map[string]any{
			schemaAttr("displayName", "string", false, true),
			schemaAttr("externalId", "string", false, false),
			members,
		},
		"meta": map[string]any{"resourceType": "Schema", "location": baseURL + "/Schemas/" + SchemaGroup},
	}
}

// agentExtensionSchema declares the agent identity SCIM extension (tracking
// draft-abbey-scim-agent-extension-00). It is defensive/opt-in: served so an IdP
// that manages NHIs can discover it, but no attribute here is mandatory and none
// is wired to enforcement. Declared attributes are parsed when present; absence is
// not an error. Per docs/SECURITY-HARDENING.md this is design-toward with no conformance claim.
func agentExtensionSchema(baseURL string) map[string]any {
	return map[string]any{
		"schemas":     []string{SchemaSchema},
		"id":          SchemaAgentExtension,
		"name":        "AgentExtension",
		"description": "Agent identity attributes (design-toward draft-abbey-scim-agent-extension-00; opt-in, never mandatory)",
		"attributes": []map[string]any{
			schemaAttr("agentKind", "string", false, false),
			schemaAttr("sponsorRef", "string", false, false),
			schemaAttr("delegationScope", "string", false, false),
		},
		"meta": map[string]any{
			"resourceType": "Schema",
			"location":     baseURL + "/Schemas/" + SchemaAgentExtension,
		},
	}
}

// --- Device / NHI resource type — design-toward seam (IDN-11) -----------------
//
// SCIM Device/EndpointApp provisioning (draft-ietf-scim-device-model) is the
// direction-of-travel for treating non-human identities (agents) as first-class
// SCIM-provisioned resources — NHI lifecycle through the same protocol as human
// users. The resource layer here is deliberately LIST-DRIVEN (ResourceTypes and
// Schemas return slices built from per-resource helpers, and EncodeUser-style
// codecs are per-resource): adding a Device resource type when the schema
// stabilizes is one helper + one slice entry + a core/auth provisioning method,
// not a restructuring.
//
// It is NOT implemented or advertised here because draft-ietf-scim-device-model
// is STILL A DRAFT (verified 2026-06, docs-VERIFICATION-LEDGER.md). Per
// The correct posture is track-don't-hard-code: we do not commit the
// draft's schema URN or attribute set against text that can still change. When
// it reaches RFC, register it exactly as the User resource is registered above.
//
// Tracking anchor (do not treat as final; refresh the revision at adoption time):
//
//	draft-ietf-scim-device-model — SCIM Device/EndpointApp schema (IETF SCIM WG)
//
// The schema URN is intentionally left undeclared until the draft is final.
