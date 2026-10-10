// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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

func securityRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := securityRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != securityBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func securityRequestBodyDeclarationFor(method, pattern string) (securityRequestBodyDeclaration, bool) {
	var schema map[string]any
	switch method + " " + pattern {
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
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func securityClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func securityTriageSchema() map[string]any {
	return securityClosedObject(oas.Obj(
		"status", oas.Obj("type", "string", "enum", oas.Enum("open", "triaged", "resolved", "dismissed")),
	), "status")
}

func securityInspectSchema() map[string]any {
	return securityClosedObject(oas.Obj(
		"surface", oas.Obj("type", "string", "description", "After trimming, must be input, output, tool_args or tool_result."),
		"text", securityNullable(oas.Obj("type", "string", "description", "Inspected only in memory, never stored raw; capped at 1048576 UTF-8 bytes.")),
		"agent_ref", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"session_ref", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"resource_ref", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"enforce", securityNullable(oas.Obj("type", "boolean", "description", "Requests blocking only when the tenant's governed policy enables it.")),
	), "surface")
}

func securityEnforcementSchema() map[string]any {
	return securityClosedObject(oas.Obj(
		"class", oas.Obj("type", "string", "description", "After trimming, must be non-empty; '*' selects all guardrail classes."),
		"enabled", securityNullable(oas.Obj("type", "boolean", "description", "Omitted or null decodes as false, returning the class to detective mode.")),
		"min_severity", securityNullable(oas.Obj("type", "string", "description", "After trimming, empty defaults to high; otherwise must be low, medium, high or critical.")),
		"reason", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped before an enablement approval request.")),
	), "class")
}

func securityCreateCaseSchema() map[string]any {
	return securityClosedObject(oas.Obj(
		"title", oas.Obj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"severity", securityNullable(oas.Obj("type", "string", "description", "After trimming, empty defaults to medium; otherwise must be low, medium, high or critical.")),
		"subject_kind", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"subject_ref", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"summary", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
	), "title")
}

func securityUpdateCaseSchema() map[string]any {
	return securityClosedObject(oas.Obj(
		"status", securityNullable(oas.Obj("type", "string", "enum", oas.Enum("open", "investigating", "contained", "closed"))),
		"severity", securityNullable(oas.Obj("type", "string", "enum", oas.Enum("low", "medium", "high", "critical"))),
		"summary", securityNullable(oas.Obj("type", "string", "description", "A string is trimmed and byte-capped; null or omission keeps the stored summary.")),
	))
}

func securityCaseLinkSchema() map[string]any {
	schema := securityClosedObject(oas.Obj(
		"link_kind", oas.Obj("type", "string", "enum", oas.Enum("finding", "audit_seq", "anomaly", "note")),
		"link_ref", securityNullable(oas.Obj("type", "string", "description", "After trimming, required for every link kind except note; stored with the server byte cap.")),
		"note", securityNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
	), "link_kind")
	schema["allOf"] = []any{oas.Obj(
		"if", oas.Obj("properties", oas.Obj("link_kind", oas.Obj("enum", oas.Enum("finding", "audit_seq", "anomaly")))),
		"then", oas.Obj(
			"required", oas.Enum("link_ref"),
			"properties", oas.Obj("link_ref", oas.Obj("type", "string", "minLength", 1)),
		),
	)}
	return schema
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := securityRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case securityBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = securityRequestBody(method, pattern)
	case securityBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case securityBodyNoDerivable, securityBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}
