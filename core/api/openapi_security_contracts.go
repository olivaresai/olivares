// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type securityRequestBodyKind uint8

const (
	securityBodyless securityRequestBodyKind = iota + 1
	securityBodyful
	securityBodyNoDerivable
	securityBodyPending
)

type securityRequestBodyDeclaration struct {
	kind   securityRequestBodyKind
	schema map[string]any
}

func securityRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := securityRequestBodyDeclarationFor(r)
	if !ok || decl.kind != securityBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func securityRequestBodyDeclarationFor(r moduleRoute) (securityRequestBodyDeclaration, bool) {
	if r.ns != "security" {
		return securityRequestBodyDeclaration{}, false
	}
	var schema map[string]any
	switch r.method + " " + r.pattern {
	case http.MethodPatch + " /findings/{id}":
		schema = securityTriageSchema()
	case http.MethodPost + " /guardrails/inspect":
		schema = securityInspectSchema()
	case http.MethodPut + " /enforcement":
		schema = securityEnforcementSchema()
	case http.MethodPost + " /cases":
		schema = securityCreateCaseSchema()
	case http.MethodPatch + " /cases/{id}":
		schema = securityUpdateCaseSchema()
	case http.MethodPost + " /cases/{id}/links":
		schema = securityCaseLinkSchema()
	default:
		return securityRequestBodyDeclaration{}, false
	}
	return securityRequestBodyDeclaration{kind: securityBodyful, schema: schema}, true
}

func securityNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func securityClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func securityTriageSchema() map[string]any {
	return securityClosedObject(oaObj(
		"status", oaObj("type", "string", "enum", oaEnum("open", "triaged", "resolved", "dismissed")),
	), "status")
}

func securityInspectSchema() map[string]any {
	return securityClosedObject(oaObj(
		"surface", oaObj("type", "string", "description", "After trimming, must be input, output, tool_args or tool_result."),
		"text", securityNullable(oaObj("type", "string", "description", "Inspected only in memory, never stored raw; capped at 1048576 UTF-8 bytes.")),
		"agent_ref", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"session_ref", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"resource_ref", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"enforce", securityNullable(oaObj("type", "boolean", "description", "Requests blocking only when the tenant's governed policy enables it.")),
	), "surface")
}

func securityEnforcementSchema() map[string]any {
	return securityClosedObject(oaObj(
		"class", oaObj("type", "string", "description", "After trimming, must be non-empty; '*' selects all guardrail classes."),
		"enabled", securityNullable(oaObj("type", "boolean", "description", "Omitted or null decodes as false, returning the class to detective mode.")),
		"min_severity", securityNullable(oaObj("type", "string", "description", "After trimming, empty defaults to high; otherwise must be low, medium, high or critical.")),
		"reason", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped before an enablement approval request.")),
	), "class")
}

func securityCreateCaseSchema() map[string]any {
	return securityClosedObject(oaObj(
		"title", oaObj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"severity", securityNullable(oaObj("type", "string", "description", "After trimming, empty defaults to medium; otherwise must be low, medium, high or critical.")),
		"subject_kind", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"subject_ref", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"summary", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
	), "title")
}

func securityUpdateCaseSchema() map[string]any {
	return securityClosedObject(oaObj(
		"status", securityNullable(oaObj("type", "string", "enum", oaEnum("open", "investigating", "contained", "closed"))),
		"severity", securityNullable(oaObj("type", "string", "enum", oaEnum("low", "medium", "high", "critical"))),
		"summary", securityNullable(oaObj("type", "string", "description", "A string is trimmed and byte-capped; null or omission keeps the stored summary.")),
	))
}

func securityCaseLinkSchema() map[string]any {
	schema := securityClosedObject(oaObj(
		"link_kind", oaObj("type", "string", "enum", oaEnum("finding", "audit_seq", "anomaly", "note")),
		"link_ref", securityNullable(oaObj("type", "string", "description", "After trimming, required for every link kind except note; stored with the server byte cap.")),
		"note", securityNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
	), "link_kind")
	schema["allOf"] = []any{oaObj(
		"if", oaObj("properties", oaObj("link_kind", oaObj("enum", oaEnum("finding", "audit_seq", "anomaly")))),
		"then", oaObj(
			"required", oaEnum("link_ref"),
			"properties", oaObj("link_ref", oaObj("type", "string", "minLength", 1)),
		),
	)}
	return schema
}
