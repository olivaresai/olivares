// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// complianceRequestBodyKind records the result of reading the compliance
// handler for one mutating route. An opaque body is deliberately distinct from
// a bodyless route: the former consumes raw JSON, but the open handler cannot
// prove a property-level schema for the separately wired resolver or packager.
type complianceRequestBodyKind uint8

const (
	complianceBodyless complianceRequestBodyKind = iota + 1
	complianceBodyful
	complianceBodyOpaque
)

type complianceRequestBodyDeclaration struct {
	kind     complianceRequestBodyKind
	required bool
	schema   map[string]any
}

// complianceRequestBody returns an OpenAPI requestBody for every compliance
// handler that reads one. Opaque raw-JSON handlers publish an empty schema: this
// records the real media type without inventing accepted properties.
func complianceRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := complianceRequestBodyDeclarationFor(method, pattern)
	if !ok || (decl.kind != complianceBodyful && decl.kind != complianceBodyOpaque) {
		return nil, false
	}
	return oas.Obj(
		"required", decl.required,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

// complianceRequestBodyDeclarationFor classifies all 33 mutating compliance
// routes registered by modules/compliance. Keep opaque cases explicit: a raw
// JSON document is not permission to guess the downstream parser's fields.
func complianceRequestBodyDeclarationFor(method, pattern string) (complianceRequestBodyDeclaration, bool) {

	switch method + " " + pattern {
	case http.MethodPost + " /frameworks/{id}/evidence":
		return complianceBodyDeclaration(false, complianceObjectSchema(oas.Obj(
			"scope_note", oas.Obj("type", "string"),
		))), true
	case http.MethodPost + " /depth/ccm/snapshot":
		return complianceBodyDeclaration(false, complianceObjectSchema(oas.Obj(
			"frameworks", complianceStringArraySchema(),
			"scope_note", oas.Obj("type", "string"),
		))), true
	case http.MethodPost + " /residency":
		return complianceBodyDeclaration(true, complianceObjectSchema(oas.Obj(
			"region", oas.Obj("type", "string"),
			"perimeter", oas.Obj("type", "string"),
			"self_hosted", oas.Obj("type", "boolean"),
			"encryption_at_rest", oas.Obj("type", "boolean"),
			"data_classes", complianceStringArraySchema(),
			"note", oas.Obj("type", "string"),
		), "region")), true
	case http.MethodPost + " /risk/classify":
		return complianceBodyDeclaration(true, complianceObjectSchema(oas.Obj(
			"subject_kind", oas.Obj("type", "string"),
			"subject_ref", oas.Obj("type", "string"),
			"agent_id", oas.Obj("type", "string"),
		), "subject_ref")), true
	case http.MethodPost + " /risk/{id}/review":
		return complianceBodyDeclaration(true, complianceObjectSchema(oas.Obj(
			"tier", oas.Obj("type", "string", "enum", oas.Enum("unacceptable", "high", "limited", "minimal")),
			"note", oas.Obj("type", "string"),
		), "tier")), true
	case http.MethodPut + " /nis2/incidents/{id}":
		return complianceBodyDeclaration(true, complianceObjectSchema(oas.Obj(
			"phase", oas.Obj("type", "string", "enum", oas.Enum("early_warning", "notification", "intermediate", "final")),
			"note", oas.Obj("type", "string"),
		))), true
	case http.MethodPut + " /retention/policies/{class}":
		return complianceBodyDeclaration(true, complianceObjectSchema(oas.Obj(
			"retention_days", oas.Obj("type", "integer", "minimum", 1, "maximum", 36500),
			"disposition", oas.Obj("type", "string", "enum", oas.Enum("retain", "purge")),
			"basis", oas.Obj("type", "string"),
			"enabled", oas.Obj("type", "boolean"),
		), "retention_days", "disposition")), true
	case http.MethodPost + " /holds":
		return complianceBodyDeclaration(true, complianceObjectSchema(oas.Obj(
			"matter_ref", oas.Obj("type", "string"),
			"title", oas.Obj("type", "string"),
			"scope_kind", oas.Obj("type", "string", "enum", oas.Enum("tenant", "data_class", "subject")),
			"data_class", oas.Obj("type", "string"),
			"subject_kind", oas.Obj("type", "string"),
			"subject_ref", oas.Obj("type", "string"),
			"reason", oas.Obj("type", "string"),
			"on_behalf_of", oas.Obj("type", "string"),
		), "matter_ref", "scope_kind", "reason")), true
	case http.MethodPost + " /holds/{id}/release":
		return complianceBodyDeclaration(false, complianceObjectSchema(oas.Obj(
			"reason", oas.Obj("type", "string"),
			"on_behalf_of", oas.Obj("type", "string"),
		))), true
	case http.MethodPost + " /erasure":
		return complianceBodyDeclaration(true, complianceObjectSchema(oas.Obj(
			"subject_kind", oas.Obj("type", "string", "enum", oas.Enum("user", "agent", "session", "document", "identity")),
			"subject_ref", oas.Obj("type", "string"),
			"aliases", complianceStringArraySchema(),
			"data_classes", complianceStringArraySchema(),
			"case_ref", oas.Obj("type", "string"),
			"reason", oas.Obj("type", "string"),
		), "subject_kind", "subject_ref", "case_ref")), true
	case http.MethodPost + " /erasure/{id}/execute":
		return complianceBodyDeclaration(false, complianceObjectSchema(oas.Obj(
			"reason", oas.Obj("type", "string"),
			"provider_user_ids", complianceStringArraySchema(),
		))), true
	case http.MethodPost + " /data-subjects/{id}/erase":
		return complianceBodyDeclaration(false, complianceObjectSchema(oas.Obj(
			"subject_kind", oas.Obj("type", "string", "enum", oas.Enum("user", "agent", "session", "document", "identity")),
			"aliases", complianceStringArraySchema(),
			"data_classes", complianceStringArraySchema(),
			"case_ref", oas.Obj("type", "string"),
			"reason", oas.Obj("type", "string"),
			"provider_user_ids", complianceStringArraySchema(),
		))), true
	case http.MethodPost + " /claude-files/{id}/erase":
		return complianceBodyDeclaration(false, complianceObjectSchema(oas.Obj(
			"reason", oas.Obj("type", "string"),
		))), true

	case http.MethodDelete + " /aims/pack/{id}",
		http.MethodDelete + " /depth/fedramp/{id}",
		http.MethodDelete + " /depth/sector/{id}",
		http.MethodDelete + " /depth/us-law/{id}",
		http.MethodDelete + " /dora/incidents/{id}",
		http.MethodDelete + " /dora/register/{id}",
		http.MethodDelete + " /nis2/incidents/{id}",
		http.MethodDelete + " /oscal/profiles/{id}",
		http.MethodDelete + " /retention/policies/{class}",
		http.MethodPost + " /depth/ccm/drift",
		http.MethodPost + " /residency/scan",
		http.MethodPost + " /retention/sweep":
		return complianceRequestBodyDeclaration{kind: complianceBodyless}, true

	case http.MethodPost + " /oscal/profiles",
		http.MethodPost + " /dora/register",
		http.MethodPost + " /dora/incidents",
		http.MethodPost + " /aims/pack",
		http.MethodPost + " /depth/us-law",
		http.MethodPost + " /depth/sector",
		http.MethodPost + " /depth/fedramp",
		http.MethodPost + " /nis2/incidents/classify":
		return complianceOpaqueDeclaration(), true
	default:
		return complianceRequestBodyDeclaration{}, false
	}
}

func complianceOpaqueDeclaration() complianceRequestBodyDeclaration {
	return complianceRequestBodyDeclaration{
		kind:     complianceBodyOpaque,
		required: true,
		schema:   oas.Obj(),
	}
}

func complianceBodyDeclaration(required bool, schema map[string]any) complianceRequestBodyDeclaration {
	return complianceRequestBodyDeclaration{
		kind:     complianceBodyful,
		required: required,
		schema:   schema,
	}
}

func complianceObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func complianceStringArraySchema() map[string]any {
	return oas.Obj("type", "array", "items", oas.Obj("type", "string"))
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := complianceRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case complianceBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = complianceRequestBody(method, pattern)
	case complianceBodyOpaque:
		doc.BodyKind = api.ModuleOperationOpaqueBody
		doc.RequestBody, _ = complianceRequestBody(method, pattern)
	case complianceBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	}
	return doc, true
}
